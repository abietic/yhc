"""Offline admission and transport checks; no real provider or credential."""

import copy
import http.client
import hashlib
import json
import socket
import subprocess
import tempfile
import threading
import time
import unittest
import uuid
from pathlib import Path
from types import SimpleNamespace
from unittest import mock

from scripts.terminal_bench import host_gateway_transport as transport
from scripts.terminal_bench.cold_dispatch import ColdBenchmarkLauncher
from scripts.terminal_bench.service_snapshot import SnapshotError, digest
from scripts.terminal_bench.recovery import write_report
from scripts.terminal_bench.test_cold_dispatch import grant_for
from scripts.terminal_bench.test_paused_recovery import inputs
from scripts.terminal_bench import test_host_gateway_pipe as pipe_oracle


def binding(grant, container="c" * 64, token="fixture"):
    return dict(version=1, scope="benchmark-host-gateway-transport-v1", grant_sha256=digest(grant),
                workspace_container_id=container, upstream_base_url="http://127.0.0.1:12346", expires_unix=4000,
                gateway_token_sha256=hashlib.sha256(token.encode()).hexdigest())


class AdmissionTests(unittest.TestCase):
    def setUp(self):
        record, native, checkpoint, hist = inputs()
        self.body = dict(usage_history=hist, paused_invocation=dict(record=record), native_proof=native)
        self.body["source_workspace"] = dict(image="sha256:" + "a" * 64)
        self.grant = grant_for(self.body)
        self.config = binding(self.grant)

    def test_config_is_anchored_without_reading_provider_credentials(self):
        value = transport.HostGatewayTransport(self.config, digest(self.config))
        self.config["upstream_base_url"] = "http://example.invalid"
        self.assertEqual(value.endpoint, ("127.0.0.1", 12346))
        for fault in ("anchor", "external", "mapped", "secret", "scope"):
            config = binding(self.grant)
            if fault == "external": config["upstream_base_url"] = "https://example.invalid"
            if fault == "mapped": config["upstream_base_url"] = "http://[::ffff:127.0.0.1]:12346"
            if fault == "secret": config["api_key"] = "synthetic-canary-not-a-real-key"
            if fault == "scope": config["scope"] = "other"
            with self.subTest(fault=fault), self.assertRaises(transport.PipeFailure):
                transport.HostGatewayTransport(config, "f" * 64 if fault == "anchor" else digest(config))

    def test_preflight_binds_grant_image_helper_and_port_without_http_request(self):
        value = transport.HostGatewayTransport(self.config, digest(self.config))
        workspace = SimpleNamespace(container="c" * 64,
            inventory=lambda:dict(container_id="c" * 64, image=self.body["source_workspace"]["image"]), rpc=mock.Mock(return_value=dict(
            helper_sha256=transport.helper_digest(), port_available=True)))
        with mock.patch.object(transport.LiteralConnection, "connect") as connect, \
             mock.patch.object(transport.LiteralConnection, "request") as request:
            value.preflight(workspace, self.grant, digest(self.grant), now=1000, gateway_token="fixture",
                            expected_image=self.body["source_workspace"]["image"])
        connect.assert_called_once(); request.assert_not_called()
        self.assertEqual(workspace.rpc.call_args.args, ("host_gateway_probe",))

    def test_unready_or_unbound_transport_cannot_take_source_claim_or_budget(self):
        for fault in ("expired", "grant", "workspace", "image", "helper", "unreachable", "token"):
            with self.subTest(fault=fault), tempfile.TemporaryDirectory() as temporary:
                config = binding(self.grant)
                if fault == "expired": config["expires_unix"] = 1
                if fault == "grant": config["grant_sha256"] = "f" * 64
                if fault == "workspace": config["workspace_container_id"] = "b" * 64
                value = transport.HostGatewayTransport(config, digest(config))
                workspace = SimpleNamespace(container="c" * 64,
                    inventory=lambda:dict(container_id="c" * 64, image="sha256:" + "b" * 64 if fault == "image" else self.body["source_workspace"]["image"]), rpc=mock.Mock(return_value=dict(
                    helper_sha256="0" * 64 if fault == "helper" else transport.helper_digest(), port_available=True)),
                    launch_cold=mock.Mock(), quarantine=mock.Mock())
                cohort = mock.Mock()
                bundle = SimpleNamespace(control=Path(temporary), workspace=workspace,
                    load=lambda *args: (self.body, {}, {}), prepare_dispatch=mock.Mock())
                with mock.patch.object(transport.LiteralConnection, "connect", side_effect=
                        OSError("synthetic readiness failure") if fault == "unreachable" else None), \
                     self.assertRaises(transport.PipeFailure):
                    ColdBenchmarkLauncher(bundle, cohort=cohort, transport=value, wall=lambda: 1000).dispatch(
                        None, "d" * 64, None, self.grant, digest(self.grant), "changed" if fault == "token" else "fixture")
                cohort.admit.assert_not_called(); bundle.prepare_dispatch.assert_not_called()
                workspace.launch_cold.assert_not_called(); workspace.quarantine.assert_not_called()
                self.assertTrue(all(p.suffix == ".lock" for p in Path(temporary).iterdir()))

    def test_consumed_source_rejects_before_transport_or_native_probe(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary).resolve()
            source = self.body["paused_invocation"]["record"]["segment_id"]
            write_report(root / ("dispatch." + source + ".json"), dict(status="claimed"))
            value = mock.Mock()
            value.preflight.side_effect = AssertionError("readiness ran before consumed check")
            workspace = mock.Mock(); cohort = mock.Mock()
            bundle = SimpleNamespace(control=root, workspace=workspace, load=lambda *args:(self.body, {}, {}))
            with self.assertRaisesRegex(SnapshotError, "already_claimed"):
                ColdBenchmarkLauncher(bundle, cohort=cohort, transport=value, wall=lambda:1000).dispatch(
                    None, "d" * 64, None, self.grant, digest(self.grant), "fixture")
            value.preflight.assert_not_called(); workspace.inventory.assert_not_called(); workspace.rpc.assert_not_called()
            cohort.admit.assert_not_called()

    def test_rpc_eof_reaps_owned_client_and_never_inherits_provider_keys(self):
        grant = copy.deepcopy(self.grant); grant["expires_unix"] = int(time.time()) + 120
        config = binding(grant); config["expires_unix"] = int(time.time()) + 120
        value = transport.HostGatewayTransport(config, digest(config))
        with tempfile.TemporaryFile() as incoming, tempfile.TemporaryFile() as outgoing:
            child = SimpleNamespace(pid=424242, stdout=incoming, stdin=outgoing,
                                    poll=lambda:1, wait=mock.Mock(return_value=1))
            workspace = SimpleNamespace(container="c" * 64, context="offline-fixture", cwd="/workspace")
            with mock.patch.dict("os.environ", {"PROV_API_KEY":"synthetic-host-only-canary", "HTTPS_PROXY":"http://forbidden.invalid"}), \
                 mock.patch("subprocess.Popen", return_value=child) as popen, \
                 mock.patch("os.killpg") as kill, self.assertRaises(transport.PipeFailure):
                value.rpc(workspace, dict(grant=grant), "fixture")
            command, options = popen.call_args.args[0], popen.call_args.kwargs
            self.assertNotIn("PROV_API_KEY", options["env"]); self.assertNotIn("HTTPS_PROXY", options["env"])
            self.assertNotIn("fixture", command)
            self.assertTrue(options["start_new_session"])
            kill.assert_called_once(); self.assertEqual(kill.call_args.args[0], 424242)
            self.assertTrue(incoming.closed and outgoing.closed)

    def test_failed_reap_still_closes_both_owned_pipes(self):
        grant = copy.deepcopy(self.grant); grant["expires_unix"] = int(time.time()) + 120
        config = binding(grant); config["expires_unix"] = int(time.time()) + 120
        value = transport.HostGatewayTransport(config, digest(config))
        with tempfile.TemporaryFile() as incoming, tempfile.TemporaryFile() as outgoing:
            child = SimpleNamespace(pid=424242, stdout=incoming, stdin=outgoing, poll=lambda:None,
                                    wait=mock.Mock(side_effect=subprocess.TimeoutExpired("owned-fixture", 5)))
            workspace = SimpleNamespace(container="c" * 64, context="offline-fixture", cwd="/workspace")
            with mock.patch("subprocess.Popen", return_value=child), mock.patch("os.killpg"), \
                 self.assertRaises(subprocess.TimeoutExpired): value.rpc(workspace, dict(grant=grant), "fixture")
            self.assertTrue(incoming.closed and outgoing.closed)


class FrameTests(pipe_oracle.FrameTests):
    def setUp(self):
        patch = mock.patch.multiple(pipe_oracle, Frames=transport.DispatchFrames, PipeFailure=transport.PipeFailure)
        patch.start(); self.addCleanup(patch.stop)


class WireTests(unittest.TestCase):
    owned_server = pipe_oracle.ForwardTests.owned_server

    def exchange(self, port, *, timeout=2, body=None, reported_calls=1, retry_after_unknown=False, invocation_timeout=20):
        record, native, checkpoint, hist = inputs()
        grant = grant_for(dict(usage_history=hist))
        grant["expires_unix"] = int(time.time()) + 120
        grant["limits"]["timeout_sec"] = invocation_timeout
        with socket.socket() as listener:
            listener.bind(("127.0.0.1", 0)); local_port = listener.getsockname()[1]
        grant["route"]["base_url"] = "http://127.0.0.1:" + str(local_port)
        token = "opaque-offline-fixture"
        config = binding(grant, token=token); config["upstream_base_url"] = "http://127.0.0.1:" + str(port)
        config["expires_unix"] = int(time.time()) + 120
        value = transport.HostGatewayTransport(config, digest(config))
        a, b = socket.socketpair()
        nonce = str(uuid.uuid4())
        host = transport.DispatchFrames(a.fileno(), a.fileno(), nonce, timeout)
        child = transport.DispatchFrames(b.fileno(), b.fileno(), nonce, timeout)
        observed = dict(errors=[], received=[], requests=[])
        request = dict(protocol="yhc-workspace-files-v1", request_id=str(uuid.uuid4()), root="/workspace",
                       operation="cold_dispatch", launch=dict(grant=grant), gateway_token=token)
        def execute(actual):
            self.assertEqual(actual, request)
            observed["requests"].append(actual)
            unknown = False
            for attempt in range(2 if retry_after_unknown else 1):
                connection = http.client.HTTPConnection("127.0.0.1", local_port, timeout=timeout + .5)
                try:
                    raw = json.dumps(body or dict(model=grant["route"]["model"], max_output_tokens=16, stream=True, input=[])).encode()
                    connection.request("POST", "/responses", raw, headers={"Authorization":"Bearer " + token, "Content-Type":"application/json"})
                    response = connection.getresponse()
                    observed["status"] = response.status
                    unknown = unknown or response.status >= 300
                    observed["received"].append(response.read())
                except (OSError, http.client.HTTPException): unknown = True
                finally: connection.close()
            return dict(exit_code=1 if unknown else 0, terminal=None, checkpoint=dict(accounting_complete=not unknown,
                        snapshot=dict(provider_calls=reported_calls, unknown_calls=1 if unknown else 0)))
        def run():
            try: transport.run_container(child, execute)
            except (transport.PipeFailure, OSError): observed["errors"].append("container_failed_closed")
        thread = threading.Thread(target=run, daemon=True); thread.start()
        try:
            return value.exchange(host, request, token), observed
        finally:
            for endpoint in (a, b):
                try: endpoint.shutdown(socket.SHUT_RDWR)
                except OSError: pass
                endpoint.close()
            thread.join(timeout + 2); self.assertFalse(thread.is_alive())

    def test_production_transport_finishes_before_max_calls_and_preserves_bytes(self):
        port, requests = self.owned_server()
        result, observed = self.exchange(port)
        self.assertEqual(observed["received"], [b"data: opaque-prefix\n\n"])
        self.assertEqual(result["checkpoint"]["snapshot"]["provider_calls"], 1)
        self.assertEqual(requests[0][0:2], ("/responses", "Bearer opaque-offline-fixture"))
        self.assertEqual(len(requests), 1)
        self.assertTrue(transport.probe(observed["requests"][0]["launch"]["grant"]["route"])["port_available"])

    def test_route_or_output_override_cannot_reach_host_gateway(self):
        for field, value in (("model", "different-model"), ("max_output_tokens", 17), ("max_output_tokens", True), ("stream", False)):
            with self.subTest(field=field, value=value):
                port, requests = self.owned_server()
                body = dict(model="deepseek-v4-flash", max_output_tokens=16, stream=True); body[field] = value
                with self.assertRaises(transport.PipeFailure): self.exchange(port, body=body, timeout=.3)
                self.assertEqual(requests, [])

    def test_forwarded_count_cannot_be_settled_as_fewer_cli_calls(self):
        port, requests = self.owned_server()
        with self.assertRaisesRegex(transport.PipeFailure, "accounting_mismatch"):
            self.exchange(port, reported_calls=0)
        self.assertEqual(len(requests), 1)

    def test_truncated_gateway_body_retains_closed_unknown_result_without_retry(self):
        port, requests = self.owned_server(reset=True)
        result, observed = self.exchange(port)
        self.assertEqual(len(requests), 1)
        self.assertFalse(result["checkpoint"]["accounting_complete"])
        self.assertEqual(result["checkpoint"]["snapshot"]["unknown_calls"], 1)

    def test_redirects_are_not_followed(self):
        port, requests = self.owned_server(redirect=True)
        result, observed = self.exchange(port)
        self.assertEqual(observed["status"], 307)
        self.assertEqual(len(requests), 1)

    def test_transport_failure_or_non_2xx_forbids_a_second_forward(self):
        for fault in ("reset", "redirect"):
            port, requests = self.owned_server(**{fault: True})
            with self.subTest(fault=fault), self.assertRaises(transport.PipeFailure):
                self.exchange(port, retry_after_unknown=True, reported_calls=2, timeout=.5)
            self.assertEqual(len(requests), 1)

    def test_slow_headers_and_continuing_body_cannot_extend_absolute_deadline(self):
        for fault in ("stalled_headers", "dripping_body"):
            port, requests = self.owned_server(**{fault: True})
            start = time.monotonic()
            with self.subTest(fault=fault), self.assertRaises(transport.PipeFailure): self.exchange(port, timeout=.15)
            self.assertLess(time.monotonic() - start, 2)
            self.assertEqual(len(requests), 1)

    def test_stream_socket_closes_at_call_window_while_checkpoint_grace_remains(self):
        port, requests = self.owned_server(dripping_body=True)
        sockets = []
        original = transport.LiteralConnection.connect
        def connect(connection):
            original(connection); sockets.append(connection.sock)
        started = time.monotonic()
        with mock.patch.object(transport.LiteralConnection, "connect", connect):
            result, observed = self.exchange(port, timeout=3, invocation_timeout=1)
        self.assertLess(time.monotonic() - started, 2)
        self.assertEqual(len(requests), 1)
        self.assertEqual([connection.fileno() for connection in sockets], [-1])
        self.assertFalse(result["checkpoint"]["accounting_complete"])
        self.assertEqual(result["checkpoint"]["snapshot"]["unknown_calls"], 1)


class OperatorCLITests(unittest.TestCase):
    def arguments(self, grant):
        return ["bundle", "dispatch", "--service-contract", "contract", "--service-definition", "definition",
                "--service-project", "project", "--service-snapshot", "service", "--workspace-container", "c" * 64,
                "--cwd", "/workspace", "--cohort-id", "cohort", "--attempt-id", grant["attempt_id"],
                "--snapshot", "bundle", "--bundle-sha256", "d" * 64, "--control-dir", "control",
                "--dispatch-grant", "grant", "--dispatch-grant-sha256", digest(grant),
                "--cohort-ledger", "ledger", "--cohort-plan-sha256", "e" * 64]

    def test_public_cli_passes_explicit_transport_and_only_opaque_token(self):
        import contextlib
        import io
        from scripts.terminal_bench import recovery_bundle, cold_dispatch, cohort_admission
        record, native, checkpoint, hist = inputs()
        grant = grant_for(dict(usage_history=hist)); config = binding(grant)
        for enabled in (False, True):
            argv = self.arguments(grant)
            if enabled: argv += ["--host-gateway-transport", "transport", "--host-gateway-transport-sha256", digest(config)]
            with self.subTest(enabled=enabled), mock.patch("sys.argv", argv), \
                 mock.patch.dict("os.environ", {"YHC_BENCH_GATEWAY_TOKEN":"fixture", "PROV_API_KEY":"synthetic-host-only-canary"}), \
                 mock.patch.object(recovery_bundle, "private_json", side_effect=lambda p:{"contract":{},"grant":grant,"transport":config}[p.name]), \
                 mock.patch.object(recovery_bundle, "DockerServices"), mock.patch.object(recovery_bundle, "ServiceSnapshots"), \
                 mock.patch.object(recovery_bundle, "DockerWorkspace"), mock.patch.object(recovery_bundle, "RecoveryBundle") as bundle, \
                 mock.patch.object(cohort_admission, "CohortAdmission") as cohort, \
                 mock.patch.object(cold_dispatch, "ColdBenchmarkLauncher") as launcher, contextlib.redirect_stdout(io.StringIO()):
                launcher.return_value.dispatch.return_value = dict(status="completed", source_segment_id="offline", accounting_complete=True, automatic_paid_resume=False)
                recovery_bundle.main()
                self.assertEqual(launcher.call_args.args, (bundle.return_value,))
                self.assertIs(launcher.call_args.kwargs["cohort"], cohort.return_value)
                self.assertEqual(isinstance(launcher.call_args.kwargs["transport"], transport.HostGatewayTransport), enabled)
                self.assertEqual(launcher.return_value.dispatch.call_args.args[-1], "fixture")

    def test_partial_binding_or_non_dispatch_flags_stop_before_backend(self):
        import contextlib
        import io
        from scripts.terminal_bench import recovery_bundle
        record, native, checkpoint, hist = inputs(); grant = grant_for(dict(usage_history=hist))
        for fault in ("partial", "restore"):
            argv = self.arguments(grant) + ["--host-gateway-transport", "transport"]
            if fault == "restore": argv[1] = "restore"; argv += ["--host-gateway-transport-sha256", "e" * 64]
            with self.subTest(fault=fault), mock.patch("sys.argv", argv), \
                 mock.patch.object(recovery_bundle, "DockerServices") as backend, \
                 contextlib.redirect_stderr(io.StringIO()), self.assertRaises(SystemExit) as exited:
                recovery_bundle.main()
            self.assertEqual(exited.exception.code, 1); backend.assert_not_called()


class CleanupSignalTests(unittest.TestCase):
    def test_signal_permission_error_still_waits_and_closes_owned_client(self):
        record, native, checkpoint, hist = inputs()
        grant = grant_for(dict(usage_history=hist)); grant["expires_unix"] = int(time.time()) + 120
        config = binding(grant); config["expires_unix"] = int(time.time()) + 120
        value = transport.HostGatewayTransport(config, digest(config))
        with tempfile.TemporaryFile() as incoming, tempfile.TemporaryFile() as outgoing:
            child = SimpleNamespace(pid=424242, stdout=incoming, stdin=outgoing, poll=lambda:None,
                                    wait=mock.Mock(return_value=137))
            workspace = SimpleNamespace(container="c" * 64, context="offline-fixture", cwd="/workspace")
            with mock.patch("subprocess.Popen", return_value=child), \
                 mock.patch("os.killpg", side_effect=PermissionError(1, "synthetic signal denial")), \
                 self.assertRaises(PermissionError): value.rpc(workspace, dict(grant=grant), "fixture")
            child.wait.assert_called_once()
            self.assertTrue(incoming.closed and outgoing.closed)


if __name__ == "__main__":
    unittest.main()
