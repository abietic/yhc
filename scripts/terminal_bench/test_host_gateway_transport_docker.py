"""Production host gateway wiring with owned synthetic services and no model calls."""

import base64
import hashlib
import http.server
import json
import os
import socket
import threading
import time
import unittest
import uuid
from decimal import Decimal
from pathlib import Path
from unittest import mock

from scripts.terminal_bench import test_recovery_bundle_docker as native_fixture
from scripts.terminal_bench.cold_dispatch import ColdBenchmarkLauncher
from scripts.terminal_bench.cohort_admission import CohortAdmission
from scripts.terminal_bench.host_gateway_transport import HostGatewayTransport, PipeFailure
from scripts.terminal_bench.recovery import write_report
from scripts.terminal_bench.service_snapshot import SnapshotError, digest
from scripts.terminal_bench.test_cold_dispatch import grant_for
from scripts.terminal_bench.test_cohort_admission import cohort_plan
from scripts.terminal_bench.test_host_gateway_transport import binding
from scripts.terminal_bench.test_recovery_bundle import history
from scripts.terminal_bench.testdata.native_handoff import OUTPUT, verify_input


@unittest.skipUnless(os.environ.get("YHC_HOST_GATEWAY_TRANSPORT_DOCKER_TEST") == "1"
                     and os.environ.get("YHC_NATIVE_RECOVERY_DOCKER_TEST") == "1", "explicit native and host-gateway Docker opt-in")
class HostGatewayDockerTests(native_fixture.NativeColdDockerTests):
    def check_gateway(self, scenario, *, restored_history=False):
        manifest, observed, _ = self.capture_joint(paused=True, environment_key=True, generated_session=True, restored_history=restored_history)
        bundle = self.target_bundle()
        restored = bundle.restore(self.bundle_path, self.bundle_digest, self.snapshot, self.attempt)
        self.assertEqual(manifest["version"], 3)
        self.assertEqual(restored["history_migration"]["source"], manifest["native_proof"]["workspace_history"])
        if restored_history:
            before = manifest["native_proof"]["workspace_history"]
            after = restored["native_proof"]["workspace_history"]
            self.assertEqual(before["restore_state"], "completed")
            self.assertEqual(before["semantic_sha256"], after["semantic_sha256"])
            source_path = "/workspace/.yhc/transcripts/" + restored["history_migration"]["archive_directory"] + "/restore.json"
            original = self.docker("exec", bundle.workspace.container, "/bin/cat", source_path).encode()
            self.assertEqual(hashlib.sha256(original).hexdigest(), before["restore_sha256"])
            self.assertEqual(self.docker("exec", bundle.workspace.container, "/bin/cat", "/workspace/side.txt"), "operator original")
        grant = grant_for(manifest, self.bundle_digest, self.attempt, int(time.time()))
        grant["binary_sha256"] = hashlib.sha256(Path(os.environ["YHC_NATIVE_RECOVERY_BINARY"]).read_bytes()).hexdigest()
        # Keep the exact source URL and opaque credential for native route binding.
        # The container port is inherited from capture, never selected by a task.
        grant["route"]["base_url"] = "http://127.0.0.1:" + str(self.paused_execution["provider_port"])
        fixture_platform = self.docker("image", "inspect", self.image, "--format", "{{.Os}}/{{.Architecture}}")
        plan = cohort_plan([manifest["usage_history"], history(str(uuid.uuid4()))], cohort=manifest["cohort_id"],
                           cap="0.1" if scenario == "budget" else "6")
        plan["expires_unix"] = int(time.time()) + 2000
        cohort = CohortAdmission.create(self.control / "host-gateway-cohort", plan, digest(plan))
        before = cohort.inspect()
        state = dict(case=scenario + ("-terminal" if restored_history else ""), requests=0, errors=[], admissions=[], launch=dict(checkpoint_path=".yhc/usage-checkpoints/cold-" + grant["dispatch_id"]))
        fixture = self
        def observe_failure_or_completion():
            evidence = os.environ.get("YHC_NATIVE_RECOVERY_PROOF_DIR")
            if evidence:
                write_report(Path(evidence) / ("host-gateway-observed-" + scenario + "-" + self.session + ".json"),
                    dict(verification_complete=False, scope="diagnostic_not_authority", observed=state,
                         cohort=cohort.inspect(), real_provider_requests=0))
        self.addCleanup(observe_failure_or_completion)

        class Handler(http.server.BaseHTTPRequestHandler):
            protocol_version = "HTTP/1.1"
            def log_message(self, *args): pass
            def do_POST(self):
                stage = "request_route"
                try:
                    fixture.assertEqual(self.path, "/responses")
                    fixture.assertEqual(self.headers.get("Authorization"), "Bearer fixture")
                    length = int(self.headers["Content-Length"])
                    fixture.assertTrue(0 < length <= 4 << 20)
                    body = json.loads(self.rfile.read(length))
                    state["requests"] += 1
                    count = state["requests"]
                    fixture.assertLessEqual(count, 2 if scenario == "known" else 1)
                    stage = "native_history"
                    verify_input(body, platform_name=fixture_platform, provider_call_limit=2, cold_round=count,
                                 cold_prompt="Continue the saved task using the verified restored state. Do not repeat completed side effects.")
                    checkpoint = state["launch"]["checkpoint_path"]
                    stage = "durable_admission"
                    program = ("import json,os,sys;from pathlib import Path;"
                               "p=Path('/workspace')/sys.argv[1];s=json.loads((p/'owner.json').read_bytes())['segment_id'];"
                               "os.execv('/opt/yhc/yhc',['yhc','usage','inspect','--checkpoint-dir',str(p),'--segment-id',s])")
                    view = json.loads(fixture.docker("exec", "--workdir", "/workspace", bundle.workspace.container,
                                      "/usr/local/bin/python3", "-I", "-c", program, checkpoint))
                    snapshot = view["snapshot"]
                    fixture.assertEqual(view["phase"], "admitted")
                    fixture.assertFalse(view["admission_closed"])
                    fixture.assertEqual(snapshot["provider_calls"], count)
                    fixture.assertEqual(snapshot["in_flight"], 1)
                    fixture.assertEqual(snapshot["known_calls"], count - 1)
                    fixture.assertNotEqual(snapshot["cost"]["reservation"]["outstanding_amount"], "0.000000000000")
                    stage = "cohort_claim"
                    claim = json.loads((fixture.control / ("dispatch." + manifest["paused_invocation"]["record"]["segment_id"] + ".json")).read_bytes())
                    fixture.assertEqual(Decimal(claim["cohort_admission"]["reserved_amount"]), Decimal("5"))
                    state["admissions"].append(view)
                except Exception as error:
                    state["errors"].append(stage + ":" + type(error).__name__)
                    self.send_error(400); self.close_connection = True
                    return
                if scenario == "helper_eof":
                    program = """import os,signal,uuid
from pathlib import Path
found=[]
for p in Path('/proc').iterdir():
    if not p.name.isdigit(): continue
    try: c=(p/'cmdline').read_bytes().split(bytes([0]))
    except FileNotFoundError: continue
    if len(c)==6 and c[:4]==[b'/usr/local/bin/python3',b'-I',b'/opt/checkpoint/workspace_io.py',b'--host-gateway-pipe']:
        assert str(uuid.UUID(c[4].decode())).encode()==c[4]
        found.append(int(p.name))
assert len(found)==1
os.kill(found[0],signal.SIGKILL)
"""
                    fixture.docker("exec", bundle.workspace.container, "/usr/local/bin/python3", "-I", "-c", program)
                    self.close_connection = True
                    return
                self.send_response(200)
                self.send_header("Content-Type", "text/event-stream")
                self.send_header("Transfer-Encoding", "chunked")
                self.end_headers()
                if scenario == "reset":
                    events = [dict(type="response.output_text.delta", sequence_number=0, item_id="offline-final",
                                   output_index=0, content_index=0, delta="owned partial response")]
                else:
                    item = dict(type="message", id="offline-final", role="assistant", status="completed",
                                content=[dict(type="output_text", text=OUTPUT, annotations=[])])
                    event = dict(type="response.output_text.delta", sequence_number=0, item_id="offline-final",
                                 output_index=0, content_index=0, delta=OUTPUT)
                    if state["requests"] == 1:
                        item = dict(type="function_call", id="cold-edit-item", call_id="cold-edit", name="Edit", status="completed",
                                    arguments=json.dumps(dict(file_path="/workspace/continued.txt", old_string="continued once", new_string="edited after cold restore")))
                        event = dict(type="response.output_item.done", sequence_number=0, output_index=0, item=item)
                    events = [event, dict(type="response.completed", sequence_number=1, response=dict(id="offline-response", object="response",
                              status="completed", model="deepseek-v4-flash", output=[item], usage=dict(input_tokens=10, output_tokens=4, total_tokens=14,
                              input_tokens_details=dict(cached_tokens=6), output_tokens_details=dict(reasoning_tokens=2))))]
                for event in events:
                    raw = ("event: " + event["type"] + "\ndata: " + json.dumps(event) + "\n\n").encode()
                    self.wfile.write(format(len(raw), "x").encode() + b"\r\n" + raw + b"\r\n"); self.wfile.flush()
                if scenario == "reset": self.connection.shutdown(socket.SHUT_RDWR)
                else: self.wfile.write(b"0\r\n\r\n"); self.wfile.flush()
                self.close_connection = True

        server = http.server.HTTPServer(("127.0.0.1", 0), Handler)
        server_thread = threading.Thread(target=server.serve_forever, daemon=True)
        server_thread.start()
        def close_server():
            server.shutdown(); server.server_close(); server_thread.join(3)
            self.assertFalse(server_thread.is_alive())
        self.addCleanup(close_server)

        config = binding(grant, bundle.workspace.container)
        config["upstream_base_url"] = "http://127.0.0.1:" + str(server.server_port)
        config["expires_unix"] = int(time.time()) + 2000
        if scenario == "unready":
            close_server()
            with socket.socket() as unavailable:
                unavailable.bind(("127.0.0.1", 0))
                config["upstream_base_url"] = "http://127.0.0.1:" + str(unavailable.getsockname()[1])
                self.dispatch_gateway_case(scenario, bundle, cohort, manifest, before, grant, config, state, restored, observed)
            return
        self.dispatch_gateway_case(scenario, bundle, cohort, manifest, before, grant, config, state, restored, observed)

    def dispatch_gateway_case(self, scenario, bundle, cohort, manifest, before, grant, config, state, restored, observed):
        transport = HostGatewayTransport(config, digest(config))
        launcher = ColdBenchmarkLauncher(bundle, cohort=cohort, transport=transport)
        if scenario in ("budget", "unready"):
            expected = SnapshotError if scenario == "budget" else PipeFailure
            with self.assertRaises(expected):
                launcher.dispatch(self.bundle_path, self.bundle_digest, self.snapshot, grant, digest(grant), "fixture")
            self.assertEqual(cohort.inspect(), before)
            self.assertEqual(state["requests"], 0)
            self.assertFalse(any(self.control.glob("dispatch.*.json")))
            result = None
        elif scenario == "helper_eof":
            import subprocess
            owned = []
            original_popen = subprocess.Popen
            def observe_popen(*args, **kwargs):
                process = original_popen(*args, **kwargs)
                if args and isinstance(args[0], list) and "--host-gateway-pipe" in args[0]:
                    owned.append(process)
                    self.addCleanup(lambda: process.wait(timeout=5) if process.returncode is None else None)
                return process
            with mock.patch.object(subprocess, "Popen", observe_popen), self.assertRaises((PipeFailure, OSError)) as failure:
                launcher.dispatch(self.bundle_path, self.bundle_digest, self.snapshot, grant, digest(grant), "fixture")
            self.assertEqual(len(owned), 1)
            process = owned[0]
            self.assertIsNotNone(process.returncode)
            self.assertTrue(process.stdin.closed and process.stdout.closed)
            state["host_client"] = dict(reaped=True, returncode=process.returncode, stdin_closed=True, stdout_closed=True,
                                        failure_type=type(failure.exception).__name__, errno=getattr(failure.exception, "errno", None))
            self.assertEqual(state["errors"], [])
            self.assertEqual(state["requests"], 1)
            self.assertTrue(cohort.inspect()["blocked"])
            self.assertEqual(cohort.inspect()["retained_amount"], "5.000000000000")
            stopped = json.loads(self.docker("inspect", bundle.workspace.container))[0]["State"]
            self.assertFalse(stopped["Running"] or stopped["Restarting"]); self.assertEqual(stopped["Pid"], 0)
            volume = restored["workspace_runtime"]["volume"]
            text = self.docker("run", "--rm", "--pull", "never", "--read-only", "--network", "none", "--cap-drop", "ALL",
                "--mount", "type=volume,src=" + volume + ",dst=/proof,readonly", "--entrypoint", "/bin/cat", self.image, "/proof/continued.txt")
            self.assertEqual(text, "continued once")
            self.assertTrue((self.control / ("dispatch." + manifest["paused_invocation"]["record"]["segment_id"] + ".json")).is_file())
            evidence = os.environ.get("YHC_NATIVE_RECOVERY_PROOF_DIR")
            if evidence:
                write_report(Path(evidence) / "host-gateway-helper_eof.json", dict(verification_complete=True,
                    scope="production_transport_offline", observed=state, cohort=cohort.inspect(), workspace_stopped=True,
                    original_progress_retained=True, real_provider_requests=0, real_credentials_verified=False))
            return
        else:
            canary = b"synthetic-upstream-only-canary"
            with mock.patch.dict(os.environ, {"PROV_API_KEY":canary.decode(), "DEEPSEEK_API_KEY":canary.decode()}):
                result = launcher.dispatch(self.bundle_path, self.bundle_digest, self.snapshot, grant, digest(grant), "fixture")
            self.assertEqual(state["errors"], [])
            self.assertEqual(state["requests"], 2 if scenario == "known" else 1)
            self.assertEqual(len(state["admissions"]), state["requests"])
            self.assertEqual(result["status"], "completed" if scenario == "known" else "stopped")
            self.assertEqual(result["accounting_complete"], scenario == "known")
            self.assertEqual(result["usage_history"]["segments"][:-1], manifest["usage_history"]["segments"])
            self.assertEqual(cohort.inspect()["blocked"], scenario != "known")
            self.assertEqual(cohort.inspect()["retained_amount"], "0.000000000000" if scenario == "known" else "5.000000000000")
            self.assertEqual(cohort.inspect(), CohortAdmission(cohort.root, cohort.anchor).inspect())
            checkpoint = result["execution"]["checkpoint"]
            self.assertTrue(checkpoint["admission_closed"]); self.assertEqual(checkpoint["phase"], "sealed")
            self.assertEqual(checkpoint["snapshot"]["unknown_calls"], 0 if scenario == "known" else 1)
            with self.assertRaisesRegex(SnapshotError, "already_claimed"):
                launcher.dispatch(self.bundle_path, self.bundle_digest, self.snapshot, grant, digest(grant), "fixture")
            checked = 0
            for entry in bundle.workspace.rpc("inventory"):
                if entry["kind"] == "file":
                    raw = base64.b64decode(bundle.workspace.rpc("read", entry=entry)["data"], validate=True)
                    self.assertNotIn(canary, raw); checked += 1
            state["workspace_files_checked_for_canary"] = checked
            self.assertGreater(checked, 0)
        self.assertEqual(self.docker("exec", bundle.workspace.container, "/bin/cat", "/workspace/continued.txt"),
                         "edited after cold restore" if scenario == "known" else "continued once")
        self.assertEqual(self.docker("exec", bundle.workspace.container, "/bin/cat", "/workspace/effects.count"), "1")
        for name, original in observed.items():
            actual = json.loads(self.docker("exec", bundle.services.backend.handles[name],
                "/usr/local/bin/python3", "-I", "/opt/checkpoint/service.py", "business", "state"))
            self.assertEqual((actual["memory"], actual["durable"], actual["next_random"]),
                             (original["memory"], original["durable"], original["next_random"]))
        evidence = os.environ.get("YHC_NATIVE_RECOVERY_PROOF_DIR")
        if evidence:
            write_report(Path(evidence) / ("host-gateway-" + state["case"] + ".json"), dict(verification_complete=True,
                scope="production_transport_offline", session_id=self.session, result=result, cohort=cohort.inspect(),
                observed=state, original_restore=restored, network_profile="none", services_preserved_after_dispatch=True,
                real_provider_requests=0, real_credentials_verified=False))

    def test_production_host_gateway_known_cold_dispatch(self): self.check_gateway("known")
    def test_completed_restore_transaction_cold_dispatch(self): self.check_gateway("known", restored_history=True)
    def test_production_host_gateway_reset_retains_closed_unknown_usage(self): self.check_gateway("reset")
    def test_production_host_gateway_budget_denial_preserves_claim(self): self.check_gateway("budget")
    def test_production_host_gateway_unready_precedes_claim(self): self.check_gateway("unready")
    def test_production_host_gateway_helper_eof_quarantines_workspace(self): self.check_gateway("helper_eof")

    def test_legacy_history_blocks_before_restore_claim_and_dispatch(self):
        # Recreate an anchored bundle from the old capturer. The real current
        # loader must not rely on an inode failing to be reused by Docker.
        with mock.patch("scripts.terminal_bench.recovery_bundle.require_portable_history", return_value=None):
            manifest, _, _ = self.capture_joint(paused=True, environment_key=True, generated_session=True, history_journal=True)
        bundle = self.target_bundle()
        plan = cohort_plan([manifest["usage_history"], history(str(uuid.uuid4()))], cohort=manifest["cohort_id"], cap="6")
        cohort = CohortAdmission.create(self.control / "history-rejection-cohort", plan, digest(plan))
        before = cohort.inspect()
        observed = []
        original_rpc = bundle.workspace.rpc
        def observe_rpc(operation, **body):
            observed.append(operation)
            return original_rpc(operation, **body)
        bundle.workspace.rpc = observe_rpc
        with mock.patch.object(bundle.services, "restore", side_effect=AssertionError("services activated before native admission")) as activate:
            with self.assertRaisesRegex(SnapshotError, "workspace_history_cold_migration_unverified"):
                bundle.restore(self.bundle_path, self.bundle_digest, self.snapshot, self.attempt)
            activate.assert_not_called()
        self.assertEqual(observed, [])
        self.assertEqual(cohort.inspect(), before)
        self.assertFalse((self.control / ("bundle-receipt." + self.bundle_digest + ".json")).exists())
        self.assertFalse((self.control / ("bundle-restore." + self.bundle_digest + ".json")).exists())
        self.assertEqual([entry["path"] for entry in original_rpc("inventory")], ["."])
        evidence = os.environ.get("YHC_NATIVE_RECOVERY_PROOF_DIR")
        if evidence:
            write_report(Path(evidence) / "host-gateway-history-rejected.json", dict(
                blocking_reason="workspace_history_cold_migration_unverified", target_restore_rpc_calls=0,
                service_activation_calls=0, agent_dispatched=False, restore_claim_created=False,
                cohort_unchanged=True, target_unchanged=True, real_provider_requests=0, can_resume_agent=False))


def load_tests(loader, tests, pattern):
    return unittest.TestSuite(HostGatewayDockerTests(name) for name in (
        "test_production_host_gateway_known_cold_dispatch", "test_completed_restore_transaction_cold_dispatch", "test_production_host_gateway_reset_retains_closed_unknown_usage",
        "test_production_host_gateway_budget_denial_preserves_claim", "test_production_host_gateway_unready_precedes_claim",
        "test_production_host_gateway_helper_eof_quarantines_workspace",
        "test_legacy_history_blocks_before_restore_claim_and_dispatch"))
