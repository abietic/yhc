"""Budget, identity and single-use launch oracles; no provider or Docker."""

import copy
import json
import os
import subprocess
import sys
import tempfile
import unittest
import uuid
from pathlib import Path
from types import SimpleNamespace
from unittest import mock

from scripts.terminal_bench.cold_dispatch import ColdBenchmarkLauncher, dispatch_lock, validate_grant
from scripts.terminal_bench.recovery import write_report
from scripts.terminal_bench.service_snapshot import SnapshotError, digest
from scripts.terminal_bench.test_paused_recovery import inputs
from scripts.terminal_bench.test_recovery_bundle import history, invocation
from scripts.terminal_bench.workspace_io import bounded_exec
from scripts.terminal_bench.workspace_recovery import DockerWorkspace, WORKSPACE_COHORT, WORKSPACE_ROLE


def grant_for(body, bundle_digest="d" * 64, attempt=None, now=1000):
    return dict(version=1, scope="benchmark-cold-dispatch-v1", dispatch_id=str(uuid.uuid4()),
                bundle_sha256=bundle_digest, attempt_id=attempt or str(uuid.uuid4()),
                usage_history_sha256=digest(body["usage_history"]), expires_unix=now + 120,
                currency="CNY", max_cumulative_estimated_cost="6", binary_sha256="a" * 64,
                route=dict(provider="deepseek", model="deepseek-v4-flash", base_url="http://127.0.0.1:12345"),
                tools=["Read", "Write", "Edit"], price_card=body["usage_history"]["segments"][-1]["usage"]["cost"]["price_card"],
                limits=dict(max_provider_calls=2, max_total_tokens=0, max_output_tokens_per_call=16,
                            max_estimated_cost="5", timeout_sec=20))


class GrantTests(unittest.TestCase):
    def setUp(self):
        record, native, checkpoint, hist = inputs()
        self.body = dict(usage_history=hist, paused_invocation=dict(record=record), native_proof=native)
        self.grant = grant_for(self.body)

    def test_cumulative_native_cost_is_preserved_before_reserving_new_segment(self):
        result = validate_grant(self.grant, digest(self.grant), self.body, "d" * 64, self.grant["attempt_id"], 1000)
        self.assertEqual(result["historical_estimated_amount"], "0.000060000000")
        self.assertEqual(result["new_reserved_limit"], "5")
        self.assertEqual(result["cumulative_exposure_limit"], "6")

    def test_tampered_expired_incomplete_or_over_total_grants_cannot_dispatch(self):
        for fault in ("anchor", "history", "attempt", "expired", "limit", "currency", "unknown", "calls", "tokens", "bool", "route"):
            g, body = copy.deepcopy(self.grant), copy.deepcopy(self.body)
            if fault == "history": g["usage_history_sha256"] = "e" * 64
            if fault == "attempt": g["attempt_id"] = str(uuid.uuid4())
            if fault == "expired": g["expires_unix"] = 1001
            if fault == "limit": g["max_cumulative_estimated_cost"] = "5"
            if fault == "currency": g["currency"] = "USD"
            if fault == "unknown": body["usage_history"]["segments"].pop()
            if fault == "calls": g["limits"]["max_provider_calls"] = 0
            if fault == "tokens": g["limits"]["max_output_tokens_per_call"] = 0
            if fault == "bool": g["limits"]["timeout_sec"] = True
            if fault == "route": g["route"]["base_url"] = "http://user:secret@127.0.0.1"
            anchor = "e" * 64 if fault == "anchor" else digest(g)
            with self.subTest(fault=fault), self.assertRaises(SnapshotError):
                validate_grant(g, anchor, body, "d" * 64, self.grant["attempt_id"], 1000)

    @unittest.skipUnless(os.name == "posix", "private process locks require POSIX")
    def test_source_claim_excludes_another_process_and_stays_consumed(self):
        with tempfile.TemporaryDirectory() as directory:
            control, segment = Path(directory).resolve(), str(uuid.uuid4())
            code = ("from pathlib import Path; from scripts.terminal_bench.cold_dispatch import dispatch_lock; "
                    "from scripts.terminal_bench.service_snapshot import SnapshotError; import sys\n"
                    "try:\n with dispatch_lock(Path(sys.argv[1]),sys.argv[2]): sys.exit(2)\n"
                    "except SnapshotError: sys.exit(0)\n")
            with dispatch_lock(control, segment) as claim:
                loser = subprocess.run([sys.executable, "-c", code, str(control), segment], capture_output=True, timeout=5)
                self.assertEqual(loser.returncode, 0, loser.stderr)
                write_report(claim, dict(source_segment_id=segment, status="claimed"))
                original = claim.read_bytes()
            with self.assertRaisesRegex(SnapshotError, "already_claimed"):
                with dispatch_lock(control, segment): self.fail("consumed claim reopened")
            self.assertEqual(claim.read_bytes(), original)

    def test_full_history_cannot_spend_then_fail_to_append_the_new_segment(self):
        session = self.body["usage_history"]["session_id"]
        self.body["usage_history"] = history(session, [invocation() for _ in range(16)] +
                                            [self.body["usage_history"]["segments"][-1]["usage"]])
        self.grant["usage_history_sha256"] = digest(self.body["usage_history"])
        with self.assertRaisesRegex(SnapshotError, "capacity_exhausted"):
            validate_grant(self.grant, digest(self.grant), self.body, "d" * 64, self.grant["attempt_id"], 1000)

    @unittest.skipUnless(os.name == "posix", "private process locks require POSIX")
    def test_transport_failure_retains_claim_exposure_and_checkpoint_without_receipt(self):
        from scripts.terminal_bench.cohort_admission import CohortAdmission
        from scripts.terminal_bench.test_cohort_admission import cohort_plan
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory).resolve()
            checkpoint = root / "owned-checkpoint"
            calls = []
            quarantined = []
            def fail(launch, token):
                calls.append(launch)
                checkpoint.write_text("admitted checkpoint remains unresolved")
                raise SnapshotError("workspace_transport_failed")
            workspace = SimpleNamespace(attempt=self.grant["attempt_id"], launch_cold=fail,
                quarantine=lambda expected: quarantined.append(expected),
                rpc=lambda op: dict(binary_sha256=self.grant["binary_sha256"]))
            bundle = SimpleNamespace(control=root, workspace=workspace,
                load=lambda *args: (self.body, None, None),
                prepare_dispatch=lambda *args: (self.body, {}, dict(workspace=dict(attempt_id=self.grant["attempt_id"]), tree_sha256="e" * 64)))
            plan = cohort_plan([self.body["usage_history"]])
            self.body["cohort_id"] = plan["cohort_id"]
            cohort = CohortAdmission.create(root / "cohort", plan, digest(plan))
            launcher = ColdBenchmarkLauncher(bundle, cohort=cohort, wall=lambda: 1000)
            for attempt in range(2):
                with self.assertRaisesRegex(SnapshotError, "transport_failed" if attempt == 0 else "already_claimed"):
                    launcher.dispatch(None, "d" * 64, None, self.grant, digest(self.grant), "fixture")
            self.assertEqual(len(calls), 1)
            self.assertEqual(len(quarantined), 1)
            self.assertEqual(checkpoint.read_text(), "admitted checkpoint remains unresolved")
            segment = self.body["paused_invocation"]["record"]["segment_id"]
            claim = json.loads((root / ("dispatch." + segment + ".json")).read_text())
            self.assertEqual(claim["usage_history"], self.body["usage_history"])
            self.assertEqual(claim["budget"]["new_reserved_limit"], "5")
            self.assertFalse((root / ("dispatch-receipt." + segment + ".json")).exists())
            self.assertEqual(cohort.inspect()["retained_amount"], "5.000000000000")
            self.assertTrue(cohort.inspect()["blocked"])

    def test_missing_cohort_authority_blocks_before_loading_or_probing(self):
        bundle = mock.Mock()
        with self.assertRaisesRegex(SnapshotError, "shared_cohort_admission_required"):
            ColdBenchmarkLauncher(bundle).dispatch(None, None, None, None, None, "fixture")
        bundle.load.assert_not_called()

    def test_failed_host_receipt_cannot_release_funds_or_remove_completed_progress(self):
        from scripts.terminal_bench.cohort_admission import CohortAdmission
        from scripts.terminal_bench.test_cohort_admission import cohort_plan, execution_for
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory).resolve()
            plan = cohort_plan([self.body["usage_history"]])
            self.body["cohort_id"] = plan["cohort_id"]
            cohort = CohortAdmission.create(root / "cohort", plan, digest(plan))
            def launch(*args):
                (root / "progress.txt").write_text("finished work survives receipt failure")
                return execution_for(self.body, self.grant)
            workspace = SimpleNamespace(attempt=self.grant["attempt_id"], launch_cold=mock.Mock(side_effect=launch),
                                        rpc=lambda op: dict(binary_sha256=self.grant["binary_sha256"]))
            bundle = SimpleNamespace(control=root, workspace=workspace, load=lambda *args: (self.body, None, None),
                prepare_dispatch=lambda *args: (self.body, {}, dict(workspace=dict(attempt_id=self.grant["attempt_id"]), tree_sha256="e" * 64)))
            def report(path, value):
                if path.name.startswith("dispatch-receipt."): raise OSError("synthetic host receipt failure")
                return write_report(path, value)
            with mock.patch("scripts.terminal_bench.cold_dispatch.write_report", side_effect=report):
                with self.assertRaisesRegex(OSError, "receipt failure"):
                    ColdBenchmarkLauncher(bundle, cohort=cohort, wall=lambda: 1000).dispatch(
                        None, "d" * 64, None, self.grant, digest(self.grant), "fixture")
            self.assertEqual(workspace.launch_cold.call_count, 1)
            self.assertEqual((root / "progress.txt").read_text(), "finished work survives receipt failure")
            self.assertTrue(cohort.inspect()["blocked"])
            self.assertEqual(cohort.inspect()["retained_amount"], "5.000000000000")

    def test_exhausted_shared_cohort_blocks_before_native_probe_and_keeps_progress(self):
        from scripts.terminal_bench.cohort_admission import CohortAdmission
        from scripts.terminal_bench.test_cohort_admission import cohort_plan
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory).resolve()
            plan = cohort_plan([self.body["usage_history"]], cap="0.1")
            self.body["cohort_id"] = plan["cohort_id"]
            cohort = CohortAdmission.create(root / "cohort", plan, digest(plan))
            bundle = mock.Mock(control=root, workspace=SimpleNamespace(attempt=self.grant["attempt_id"]))
            bundle.load.return_value = (self.body, None, None)
            original = copy.deepcopy(self.body)
            before = cohort.inspect()
            with self.assertRaisesRegex(SnapshotError, "cohort_budget_insufficient"):
                ColdBenchmarkLauncher(bundle, cohort=cohort, wall=lambda: 1000).dispatch(
                    None, "d" * 64, None, self.grant, digest(self.grant), "fixture")
            bundle.prepare_dispatch.assert_not_called()
            self.assertEqual(self.body, original)
            self.assertEqual(cohort.inspect(), before)
            self.assertFalse(any(root.glob("dispatch.*.json")))

    def test_missing_or_automatic_restart_policy_is_rejected_at_inventory(self):
        workspace = object.__new__(DockerWorkspace)
        workspace.container, workspace.cwd = "a" * 64, "/workspace"
        workspace.cohort, workspace.attempt = str(uuid.uuid4()), None
        row = dict(Config=dict(Labels={WORKSPACE_COHORT: workspace.cohort, WORKSPACE_ROLE: "idle-native-workspace"},
                   Entrypoint=["/bin/sleep"], Cmd=["infinity"]),
                   HostConfig=dict(ReadonlyRootfs=True, Privileged=False, CapDrop=["ALL"], NetworkMode="none", IpcMode="private"),
                   State=dict(Running=True, Paused=False, Restarting=False, Pid=1))
        for policy in (None, {}, {"Name": ""}, {"Name": "always"}, {"Name": "unless-stopped"}, {"Name": "on-failure"}):
            with self.subTest(policy=policy):
                row["HostConfig"]["RestartPolicy"] = policy
                workspace.docker = mock.Mock(return_value=json.dumps([row]).encode())
                with self.assertRaisesRegex(SnapshotError, "not_isolated"):
                    workspace.inventory()
                workspace.docker.assert_called_once_with("inspect", workspace.container)

    def test_external_or_dns_gateway_cannot_consume_claim_or_shared_allocation(self):
        from scripts.terminal_bench.cohort_admission import CohortAdmission
        from scripts.terminal_bench.test_cohort_admission import cohort_plan
        hosts = ("gateway.example.invalid", "host.docker.internal", "localhost", "0.0.0.0",
                 "10.0.0.2", "127.0.0.1.example.invalid", "127.1", "[::ffff:127.0.0.1]")
        for host in hosts:
            with self.subTest(host=host), tempfile.TemporaryDirectory() as directory:
                root = Path(directory).resolve()
                body, grant = copy.deepcopy(self.body), copy.deepcopy(self.grant)
                grant["route"]["base_url"] = "http://" + host + ":12345"
                plan = cohort_plan([body["usage_history"]])
                body["cohort_id"] = plan["cohort_id"]
                cohort = CohortAdmission.create(root / "cohort", plan, digest(plan))
                workspace = SimpleNamespace(attempt=grant["attempt_id"],
                    rpc=mock.Mock(return_value=dict(binary_sha256=grant["binary_sha256"])),
                    launch_cold=mock.Mock(side_effect=SnapshotError("fixture_network_unreachable")), quarantine=mock.Mock())
                bundle = SimpleNamespace(control=root, workspace=workspace,
                    load=mock.Mock(return_value=(body, None, None)), prepare_dispatch=mock.Mock(return_value=(body, {},
                        dict(workspace=dict(attempt_id=grant["attempt_id"]), tree_sha256="e" * 64))))
                original = copy.deepcopy(body)
                before = cohort.inspect()
                with self.assertRaisesRegex(SnapshotError, "dispatch_gateway_transport_unverified"):
                    ColdBenchmarkLauncher(bundle, cohort=cohort, wall=lambda: 1000).dispatch(
                        None, "d" * 64, None, grant, digest(grant), "fixture")
                bundle.prepare_dispatch.assert_not_called()
                workspace.rpc.assert_not_called()
                workspace.launch_cold.assert_not_called()
                self.assertFalse(any(root.glob("dispatch.*.json")))
                self.assertEqual(cohort.inspect(), before)
                self.assertEqual(body, original)

    def test_gateway_preflight_accepts_literal_loopback_without_dns_or_io(self):
        from scripts.terminal_bench.cold_dispatch import validate_gateway_transport
        with mock.patch("socket.getaddrinfo", side_effect=AssertionError("DNS must not run")):
            for host in ("127.0.0.1", "127.0.0.2", "[::1]"):
                grant = copy.deepcopy(self.grant)
                grant["route"]["base_url"] = "http://" + host + ":12345"
                validate_gateway_transport(grant)
            for value in (None, {}, {"route": None}, {"route": {"base_url": 4}},
                          {"route": {"base_url": "http://[broken"}},
                          {"route": {"base_url": "http://[::1%25lo]:80"}},
                          {"route": {"base_url": "http://127.0.0.1:0"}},
                          {"route": {"base_url": "http://127.0.0.1:70000"}},
                          {"route": {"base_url": "http://127.0.0.1:bad"}}):
                with self.assertRaises(SnapshotError): validate_gateway_transport(value)

    def test_mapped_loopback_is_rejected_even_when_runtime_classifies_it_as_loopback(self):
        from scripts.terminal_bench.cold_dispatch import validate_gateway_transport
        runtime_address = SimpleNamespace(is_loopback=True, ipv4_mapped=object())
        with mock.patch("scripts.terminal_bench.cold_dispatch.ipaddress.ip_address", return_value=runtime_address):
            with self.assertRaisesRegex(SnapshotError, "dispatch_gateway_transport_unverified"):
                validate_gateway_transport(self.grant)


@unittest.skipUnless(os.name == "posix", "process groups and pipe selectors require POSIX")
class OutputBoundaryTests(unittest.TestCase):
    def test_oversized_stdout_stderr_and_orphaned_pipe_kill_only_owned_group(self):
        original = subprocess.Popen
        for channel, orphan in ((1, False), (2, False), (2, True)):
            with self.subTest(channel=channel, orphan=orphan):
                started = []
                def spawn(*args, **kwargs):
                    child = original(*args, **kwargs); started.append(child); return child
                program = "import os\n"
                if orphan: program += "if os.fork(): os._exit(0)\n"
                program += "while True: os.write(" + str(channel) + ", b'x'*65536)\n"
                with mock.patch("scripts.terminal_bench.workspace_io.subprocess.Popen", side_effect=spawn), \
                        mock.patch("scripts.terminal_bench.workspace_io.os.killpg", wraps=os.killpg) as killed:
                    with self.assertRaisesRegex(ValueError, "unsupported_workspace_state"):
                        bounded_exec([sys.executable, "-c", program], Path.cwd(), {}, b"", 5)
                    self.assertIsNotNone(started[0].poll())
                    self.assertEqual(killed.call_args.args[0], started[0].pid)
                    self.assertTrue(started[0].stdout.closed and started[0].stderr.closed)


if __name__ == "__main__":
    unittest.main()
