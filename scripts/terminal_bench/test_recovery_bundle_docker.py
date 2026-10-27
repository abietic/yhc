"""Destroyed-runtime recovery and opt-in public execution with a fake provider."""

import copy
import hashlib
import json
import os
import select
import subprocess
import sys
import time
import unittest
import uuid
from pathlib import Path

from scripts.terminal_bench.cold_recovery import DockerServices
from scripts.terminal_bench.cold_dispatch import ColdBenchmarkLauncher
from scripts.terminal_bench.cohort_admission import CohortAdmission
from scripts.terminal_bench.recovery import write_report
from scripts.terminal_bench.recovery_bundle import RecoveryBundle, user_files
from scripts.terminal_bench.paused_recovery import SCOPE, cold_tool_handoff
from scripts.terminal_bench.service_snapshot import ServiceSnapshots, SnapshotError, digest
from scripts.terminal_bench.test_cold_dispatch import grant_for
from scripts.terminal_bench.test_cohort_admission import cohort_plan
from scripts.terminal_bench.test_recovery_bundle import history
from scripts.terminal_bench import test_service_snapshot_docker as service_fixture
from scripts.terminal_bench.workspace_recovery import ATTEMPT, WORKSPACE_COHORT, WORKSPACE_ROLE, DockerWorkspace, native_proof, usage_history


def validate_proof_directory(value):
    if not value:
        return
    path = Path(value)
    if not (path.is_absolute() and path.is_dir() and path.resolve() == path
            and path.stat().st_uid == os.geteuid() and not path.stat().st_mode & 0o077):
        raise RuntimeError("proof directory must already exist, be private and host-owned")


@unittest.skipUnless(os.environ.get("YHC_NATIVE_RECOVERY_DOCKER_TEST") == "1", "explicit native cold Docker opt-in")
class NativeColdDockerTests(unittest.TestCase):
    docker = staticmethod(service_fixture.ColdDockerTests.docker)
    compose = service_fixture.ColdDockerTests.compose
    provision = service_fixture.ColdDockerTests.provision
    business = service_fixture.ColdDockerTests.business
    cli = service_fixture.ColdDockerTests.cli
    setUp = service_fixture.ColdDockerTests.setUp

    @classmethod
    def setUpClass(cls):
        validate_proof_directory(os.environ.get("YHC_NATIVE_RECOVERY_PROOF_DIR"))
        binary = Path(os.environ["YHC_NATIVE_RECOVERY_BINARY"]).resolve()
        seed = Path(os.environ["YHC_NATIVE_RECOVERY_SEED"]).resolve()
        if not binary.is_file() or not seed.is_file():
            raise RuntimeError("compiled Linux fixture inputs required")
        # The cached generic fixture has Git and system Python. Add only an
        # interpreter alias in an owned immutable image; no package install.
        compat = "yhc-native-base:" + uuid.uuid4().hex[:12]
        setup = "yhc-native-base-" + uuid.uuid4().hex[:12]
        cached = os.environ.get("YHC_NATIVE_RECOVERY_BASE", "yhc-generic-concurrency:20261020-arm64")
        handle = cls.docker("create", "--user", "0:0", "--network", "none", "--name", setup, "--entrypoint", "/bin/sh", cached,
                            "-c", "mkdir -p /usr/local/bin && ln -s /usr/bin/python3 /usr/local/bin/python3")
        try:
            cls.docker("start", "--attach", handle)
            cls.docker("commit", "--change", "USER 0:0", handle, compat)
        finally:
            cls.docker("rm", "--force", handle)
        cls.addClassCleanup(cls.docker, "image", "rm", compat)
        previous = os.environ.get("YHC_RECOVERY_DOCKER_IMAGE")
        os.environ["YHC_RECOVERY_DOCKER_IMAGE"] = compat
        try:
            service_fixture.ColdDockerTests.setUpClass.__func__(cls)
        finally:
            if previous is None:
                os.environ.pop("YHC_RECOVERY_DOCKER_IMAGE", None)
            else:
                os.environ["YHC_RECOVERY_DOCKER_IMAGE"] = previous
        base = cls.image
        cls.image = "yhc-native-cold-proof:" + uuid.uuid4().hex[:12]
        helper = "yhc-native-image-" + uuid.uuid4().hex[:12]
        cls.docker("run", "--pull", "never", "--network", "none", "--name", helper, "--entrypoint", "/bin/mkdir", base, "-p", "/opt/yhc")
        try:
            cls.docker("cp", str(binary), helper + ":/opt/yhc/yhc")
            cls.docker("cp", str(seed), helper + ":/opt/checkpoint/seed-native")
            cls.docker("cp", str(Path(__file__).parent / "workspace_io.py"), helper + ":/opt/checkpoint/workspace_io.py")
            cls.docker("cp", str(Path(__file__).parent / "host_gateway_transport.py"), helper + ":/opt/checkpoint/host_gateway_transport.py")
            cls.docker("cp", str(Path(__file__).parent / "testdata/native_handoff.py"), helper + ":/opt/checkpoint/native_handoff.py")
            cls.docker("commit", helper, cls.image)
        finally:
            cls.docker("rm", "--force", helper)
        cls.addClassCleanup(cls.docker, "image", "rm", cls.image)

    def remove_workspace(self, container, volume):
        present = self.docker("ps", "--all", "--quiet", "--no-trunc", "--filter", "id=" + container).split()
        if container in present:
            self.docker("rm", "--force", container)
        volumes = self.docker("volume", "ls", "--quiet", "--filter", "name=^" + volume + "$").split()
        if volume in volumes:
            self.docker("volume", "rm", volume)

    def provision_workspace(self, *, target=False):
        attempt = self.attempt if target else None
        volume = "yhc-native-state-" + uuid.uuid4().hex[:12]
        self.docker("volume", "create", "--label", WORKSPACE_COHORT + "=" + self.cohort, volume)
        name = "yhc-native-space-" + uuid.uuid4().hex[:12]
        labels = ["--label", WORKSPACE_COHORT + "=" + self.cohort, "--label", WORKSPACE_ROLE + "=idle-native-workspace"]
        if attempt:
            labels += ["--label", ATTEMPT + "=" + attempt]
        container = self.docker("run", "--detach", "--pull", "never", "--read-only", "--cap-drop", "ALL",
                                "--network", "none", "--ipc", "private", "--memory", "384m", "--cpus", "1",
                                "--tmpfs", "/tmp:rw,noexec,nosuid,size=64m", "--mount", "type=volume,src=" + volume + ",dst=/workspace",
                                "--workdir", "/workspace", "--name", name, *labels, "--entrypoint", "/bin/sleep", self.image, "infinity")
        self.addCleanup(self.remove_workspace, container, volume)
        return DockerWorkspace(container, "/workspace", self.cohort, attempt), volume

    def capture_joint(self, unbound=False, tool_ready=False, paused=False, environment_key=False, generated_session=False, history_journal=False, restored_history=False):
        self.session, self.attempt = str(uuid.uuid4()), str(uuid.uuid4())
        source_project, source_definition, _ = self.provision()
        observed = {}
        for name in self.plan["services"]:
            self.business(source_project, source_definition, name, "mutate")
            observed[name] = self.business(source_project, source_definition, name, "state")
        source, volume = self.provision_workspace()
        args = ["--unbound-model"] if unbound else ["--tool-ready"] if tool_ready or paused else []
        seeded = self.docker("exec", "--workdir", "/workspace", source.container, "/opt/checkpoint/seed-native",
                             "--generated-session-id" if generated_session else self.session, *args)
        if generated_session:
            self.session = seeded.strip()
            self.assertRegex(self.session, r"^session-[0-9]+$")
        git_status = self.docker("exec", "--workdir", "/workspace", source.container, "git", "status", "--porcelain=v1")
        supplied_history = history(self.session)
        pause_record = None
        if paused:
            price = supplied_history["segments"][0]["usage"]["cost"]["price_card"]
            self.paused_execution = json.loads(self.docker("exec", "--workdir", "/workspace", source.container,
                "/usr/local/bin/python3", "-I", "/opt/checkpoint/native_handoff.py", self.session,
                "pause_tool_env" if environment_key else "pause_tool", json.dumps(price)))
            execution = self.paused_execution
            self.assertFalse(execution["provider_protocol_errors"], execution)
            self.assertEqual(execution["observed_provider_requests"], 1)
            self.assertEqual(execution["exit_code"], 1)
            pause_record = dict(version=1, scope=SCOPE, session_id=self.session,
                segment_id=execution["durable_checkpoint"]["segment_id"], checkpoint_path=execution["checkpoint_path"],
                terminal=execution["result"], native_proof=execution["native_after_pause"])
            # This is the trusted host launcher receipt, outside the task tree.
            write_report(self.control / "operator-pause.json", pause_record)
            evidence = os.environ.get("YHC_NATIVE_RECOVERY_PROOF_DIR")
            if evidence:
                write_report(Path(evidence) / ("paused-source-observed-" + self.session + ".json"),
                             dict(record=pause_record, checkpoint=execution["durable_checkpoint"], verification_complete=False))
            supplied_history = history(self.session, [s["usage"] for s in supplied_history["segments"]] + [execution["result"]["usage"]])
            git_status = self.docker("exec", "--workdir", "/workspace", source.container, "git", "status", "--porcelain=v1")
        if history_journal:
            self.docker("exec", "--workdir", "/workspace", source.container, "/opt/checkpoint/seed-native", self.session, "--history-journal")
        if restored_history:
            self.docker("exec", "--workdir", "/workspace", source.container, "/opt/checkpoint/seed-native", self.session, "--restored-history")
        if paused and (history_journal or restored_history):
            # The trusted operator has changed only closed history, never the
            # transcript or financial checkpoint. Reobserve and anchor it.
            pause_record["native_proof"] = source.rpc("native_probe", session_id=self.session)
            write_report(self.control / "operator-history-pause.json", pause_record)
        original_rpc = source.rpc
        def source_rpc(operation, **body):
            value = original_rpc(operation, **body)
            if operation == "native_probe" and value.get("validation_complete") is not True and not paused:
                debug = json.loads(self.docker("exec", "--workdir", "/workspace", source.container, "/usr/bin/env",
                    "HOME=/tmp/yhc-proof-home", "YHC_SESSION_CATALOG=/workspace/.yhc/session-roots.json",
                    "/opt/yhc/yhc", "sessions", "resume", self.session, "--recovery-proof", "--output-format", "json"))
                raise AssertionError(json.dumps({"blocking_reasons": value.get("blocking_reasons"),
                                                "fixture_warnings": debug.get("result", {}).get("warnings"),
                                                "native_usage": value.get("native_usage")}))
            return value
        source.rpc = source_rpc
        captured = self.cli("capture", "--project", source_project, "--definition", str(source_definition), "--cohort-id", self.cohort)
        self.service_digest = captured["snapshot_sha256"]
        source_services = ServiceSnapshots(self.plan, DockerServices(source_project, source_definition, self.plan), control_dir=self.control)
        self.bundle_path = self.root / "bundle"
        captured = RecoveryBundle(source_services, source).capture(
            self.bundle_path, self.snapshot, self.service_digest, self.session, supplied_history,
            pause_record=pause_record, pause_sha256=digest(pause_record) if pause_record else None)
        self.bundle_digest = captured["bundle_sha256"]
        manifest = json.loads((self.bundle_path / "manifest.json").read_text())
        old_service = json.loads((self.snapshot / "manifest.json").read_text())
        old_ids = [r["container_id"] for r in old_service["source"]["containers"]] + [source.container]
        self.compose(source_project, source_definition, "down", "--volumes", "--remove-orphans")
        self.remove_workspace(source.container, volume)
        missing = subprocess.run(["docker", "inspect", *old_ids], capture_output=True)
        self.assertNotEqual(missing.returncode, 0)
        self.assertNotIn(volume, self.docker("volume", "ls", "--quiet").split())
        return manifest, observed, git_status

    def test_clean_budget_pause_restores_exact_tools_closed_checkpoint_and_services(self):
        manifest, observed, git_status = self.capture_joint(paused=True)
        bundle = self.target_bundle()
        receipt = bundle.restore(self.bundle_path, self.bundle_digest, self.snapshot, self.attempt)
        self.assertEqual(manifest["version"], 3)
        self.assertFalse(receipt["native_proof"]["validation_complete"])
        self.assertEqual(receipt["native_proof"]["blocking_reasons"], ["native_turn_incomplete"])
        joint = receipt["paused_joint_proof"]
        self.assertTrue(joint["accounting_complete"])
        self.assertEqual(joint, manifest["paused_invocation"]["joint_proof"])
        self.assertFalse(joint["can_resume_agent"] or joint["new_spending_authorized"] or receipt["can_resume_agent"])
        self.assertEqual(receipt["accounting"]["totals"]["provider_calls"], 3)
        self.assertEqual(receipt["accounting"]["totals"]["total_tokens"], 42)
        self.assertEqual(receipt["native_proof"]["native_usage"]["total_tokens"], 32)
        self.assertEqual(receipt["native_proof"]["tool_boundary"]["tool_names"], ["Write"])
        self.assertNotEqual(receipt["native_proof"]["process_nonce"], manifest["native_proof"]["process_nonce"])
        self.assertEqual(self.docker("exec", bundle.workspace.container, "/bin/cat", "/workspace/continued.txt"), "continued once")
        self.assertEqual(self.docker("exec", bundle.workspace.container, "/bin/cat", "/workspace/effects.count"), "1")
        self.assertEqual(self.docker("exec", "--workdir", "/workspace", bundle.workspace.container, "git", "status", "--porcelain=v1"), git_status)
        claim = (self.control / ("bundle-restore." + self.bundle_digest + ".json")).read_bytes()
        with self.assertRaisesRegex(SnapshotError, "already_claimed"):
            bundle.restore(self.bundle_path, self.bundle_digest, self.snapshot, self.attempt)
        self.assertEqual((self.control / ("bundle-restore." + self.bundle_digest + ".json")).read_bytes(), claim)
        for name, original in observed.items():
            actual = json.loads(self.docker("exec", bundle.services.backend.handles[name], "/usr/local/bin/python3", "-I",
                                          "/opt/checkpoint/service.py", "business", "state"))
            self.assertEqual((actual["memory"], actual["durable"], actual["next_random"]),
                             (original["memory"], original["durable"], original["next_random"]))
            self.assertGreaterEqual(actual["logical_ns"], receipt["services"]["expected_clocks"][name]["logical_ns"])
        evidence = os.environ.get("YHC_NATIVE_RECOVERY_PROOF_DIR")
        if evidence:
            write_report(Path(evidence) / "paused-joint-roundtrip.json", dict(manifest=manifest, receipt=receipt,
                         offline_pause=self.paused_execution, source_containers_and_volumes_destroyed=True,
                         effect_count=1, real_provider_requests=0, can_resume_agent=False))

    def test_unclosed_checkpoint_after_import_quarantines_before_service_activation(self):
        manifest, _, _ = self.capture_joint(paused=True)
        bundle = self.target_bundle()
        original = bundle.workspace.rpc
        def unclose_before_usage_probe(operation, **body):
            if operation == "usage_probe":
                path = "/workspace/" + manifest["paused_invocation"]["record"]["checkpoint_path"] + "/checkpoint.json"
                code = ("import hashlib,json; from pathlib import Path; p=Path(" + repr(path) + "); "
                        "e=json.loads(p.read_text()); d=e['data']; d['admission_closed']=False; d['phase']='settled'; "
                        "raw=json.dumps(d,separators=(',',':')); e['data']=d; e['sha256']=hashlib.sha256(raw.encode()).hexdigest(); "
                        "p.write_text(json.dumps(e,separators=(',',':')))")
                self.docker("exec", bundle.workspace.container, "/usr/local/bin/python3", "-I", "-c", code)
            return original(operation, **body)
        bundle.workspace.rpc = unclose_before_usage_probe
        with self.assertRaisesRegex(SnapshotError, "paused_usage_checkpoint_not_closed"):
            bundle.restore(self.bundle_path, self.bundle_digest, self.snapshot, self.attempt)
        self.assertFalse((self.control / ("activation." + self.service_digest + ".json")).exists())
        self.assertTrue((self.control / ("bundle-restore." + self.bundle_digest + ".json")).exists())
        self.assertFalse(json.loads(self.docker("inspect", bundle.workspace.container))[0]["State"]["Running"])

    def check_cold_tool_dispatch(self, scenario):
        manifest, observed, _ = self.capture_joint(paused=True)
        bundle = self.target_bundle()
        restored = bundle.restore(self.bundle_path, self.bundle_digest, self.snapshot, self.attempt)
        pause = manifest["paused_invocation"]
        handoff = cold_tool_handoff(pause["record"], pause["record_sha256"], restored["native_proof"], pause["checkpoint"],
                                   manifest["usage_history"], "/workspace")
        before_source = bundle.workspace.rpc("read", entry=next(e for e in bundle.workspace.rpc("inventory")
            if e["path"] == handoff["checkpoint_path"] + "/checkpoint.json"))
        claim_path = self.control / ("bundle-restore." + self.bundle_digest + ".json")
        restore_claim = claim_path.read_bytes()
        price = pause["record"]["terminal"]["usage"]["cost"]["price_card"]
        def run(mode):
            return json.loads(self.docker("exec", "--workdir", "/workspace", bundle.workspace.container,
                "/usr/local/bin/python3", "-I", "/opt/checkpoint/native_handoff.py", self.session, mode, json.dumps(price),
                json.dumps(dict(handoff=handoff, fixture_port=self.paused_execution["provider_port"]))))
        result = run(scenario)
        evidence = os.environ.get("YHC_NATIVE_RECOVERY_PROOF_DIR")
        if evidence:
            write_report(Path(evidence) / (scenario + "-observed.json"), dict(offline_execution=result, verification_complete=False))
        self.assertFalse(result["provider_protocol_errors"], result)
        self.assertEqual(result["real_provider_requests"], 0)
        terminal, checkpoint = result["result"], result["durable_checkpoint"]
        if scenario in ("cold_edit", "cold_race"):
            self.assertEqual(result["observed_provider_requests"], 2)
            self.assertEqual((result["exit_code"], terminal["status"]), (0, "completed"))
            proof = terminal["cold_recovery"]
            self.assertTrue(proof["read_guards_restored"] and proof["dispatch_claimed"])
            self.assertFalse(proof["historical_budget_used"])
            self.assertEqual(proof["source_segment_id"], handoff["checkpoint"]["segment_id"])
            self.assertEqual(proof["new_segment_id"], terminal["usage"]["call_ledger"]["segment_id"])
            self.assertTrue(checkpoint["accounting_complete"] and terminal["read_guards"]["complete"])
            self.assertEqual(self.docker("exec", bundle.workspace.container, "/bin/cat", "/workspace/continued.txt"), "edited after cold restore")
            repeated = run("cold_repeated")
            self.assertEqual(repeated["observed_provider_requests"], 0)
            self.assertEqual(repeated["result"]["error"]["code"], "cold_tool_handoff_invalid")
            self.assertEqual(checkpoint["snapshot"]["known_calls"], 2)
            if scenario == "cold_race":
                self.assertEqual(len(result["concurrent_launch_probes"]), 1)
                self.assertTrue(result["concurrent_launch_probes"][0]["winner_transcript_unchanged"])
                self.assertTrue(result["concurrent_launch_probes"][0]["winner_claim_unchanged"])
        elif scenario in ("cold_changed", "cold_checkpoint_open"):
            self.assertEqual(result["observed_provider_requests"], 0)
            self.assertEqual(terminal["error"]["code"], "cold_tool_handoff_invalid")
        elif scenario == "cold_denied":
            self.assertEqual(result["observed_provider_requests"], 0)
            self.assertEqual(terminal["error"]["code"], "run_budget_exceeded")
            self.assertTrue(checkpoint["accounting_complete"])
            self.assertTrue(terminal["cold_recovery"]["dispatch_claimed"])
        elif scenario == "cold_route_changed":
            self.assertEqual(result["observed_provider_requests"], 0)
            self.assertIn("model_binding_route_revision_changed", terminal["error"]["message"])
            self.assertTrue(terminal["cold_recovery"]["dispatch_claimed"])
            repeated = run("cold_repeated")
            self.assertEqual(repeated["observed_provider_requests"], 0)
            self.assertEqual(repeated["result"]["error"]["code"], "cold_tool_handoff_invalid")
        elif scenario == "cold_unknown":
            self.assertEqual(result["observed_provider_requests"], 1)
            self.assertEqual(terminal["error"]["code"], "run_usage_unknown")
            self.assertFalse(checkpoint["accounting_complete"])
            self.assertNotEqual(checkpoint["snapshot"]["cost"]["reservation"]["retained_amount"], "0.000000000000")
        elif scenario == "cold_crash":
            self.assertEqual(result["observed_provider_requests"], 1)
            self.assertEqual(result["exit_code"], -9)
            self.assertIsNone(terminal)
            self.assertFalse(checkpoint["accounting_complete"])
            self.assertEqual(checkpoint["snapshot"]["in_flight"], 1)
            self.assertNotEqual(checkpoint["snapshot"]["cost"]["reservation"]["outstanding_amount"], "0.000000000000")
            repeated = run("cold_repeated")
            self.assertEqual(repeated["observed_provider_requests"], 0)
        self.assertEqual(claim_path.read_bytes(), restore_claim)
        self.assertEqual(self.docker("exec", bundle.workspace.container, "/bin/cat", "/workspace/effects.count"), "1")
        current = bundle.workspace.rpc("inventory")
        if scenario != "cold_checkpoint_open":
            after_source = bundle.workspace.rpc("read", entry=next(e for e in current if e["path"] == handoff["checkpoint_path"] + "/checkpoint.json"))
            self.assertEqual(before_source, after_source)
        if terminal and "usage" in terminal:
            extended = history(self.session, [s["usage"] for s in manifest["usage_history"]["segments"]] + [terminal["usage"]])
            self.assertEqual(extended["segments"][:-1], manifest["usage_history"]["segments"])
            self.assertEqual(usage_history(extended, self.session)["coverage_complete"], scenario in ("cold_edit", "cold_race", "cold_denied", "cold_route_changed"))
        for name, original in observed.items():
            state = json.loads(self.docker("exec", bundle.services.backend.handles[name], "/usr/local/bin/python3", "-I", "/opt/checkpoint/service.py", "business", "state"))
            self.assertEqual((state["memory"], state["durable"], state["next_random"]), (original["memory"], original["durable"], original["next_random"]))
            self.assertGreaterEqual(state["logical_ns"], restored["services"]["expected_clocks"][name]["logical_ns"])
        if evidence:
            write_report(Path(evidence) / (scenario + ".json"), dict(restored_components=restored, offline_execution=result,
                         source_checkpoint_unchanged=scenario != "cold_checkpoint_open", real_provider_requests=0,
                         automatic_paid_dispatch=False, verification_complete=True))

    def test_cold_dispatch_restores_guard_and_edits_without_replaying_write(self):
        self.check_cold_tool_dispatch("cold_edit")

    def test_cold_dispatch_competing_process_cannot_mutate_winner_session_or_claim(self):
        self.check_cold_tool_dispatch("cold_race")

    def test_cold_dispatch_rejects_changed_guard_bytes_before_provider(self):
        self.check_cold_tool_dispatch("cold_changed")

    def test_cold_dispatch_rejects_open_source_accounting_before_provider(self):
        self.check_cold_tool_dispatch("cold_checkpoint_open")

    def test_cold_dispatch_unknown_usage_retains_reservation(self):
        self.check_cold_tool_dispatch("cold_unknown")

    def test_cold_dispatch_denied_reservation_keeps_claim_and_progress(self):
        self.check_cold_tool_dispatch("cold_denied")

    def test_cold_dispatch_changed_route_blocks_without_reopening_claim(self):
        self.check_cold_tool_dispatch("cold_route_changed")

    def test_cold_dispatch_crash_retains_claim_and_inflight_reservation(self):
        self.check_cold_tool_dispatch("cold_crash")

    def check_benchmark_launcher(self, scenario, *, generated_session=False):
        manifest, observed, unused = self.capture_joint(paused=True, environment_key=scenario != "auth", generated_session=generated_session)
        self.assertEqual(manifest["usage_history"]["session_id"], self.session)
        bundle = self.target_bundle()
        restored = bundle.restore(self.bundle_path, self.bundle_digest, self.snapshot, self.attempt)
        grant = grant_for(manifest, self.bundle_digest, self.attempt, int(time.time()))
        grant["binary_sha256"] = hashlib.sha256(Path(os.environ["YHC_NATIVE_RECOVERY_BINARY"]).read_bytes()).hexdigest()
        grant["route"]["base_url"] = "http://127.0.0.1:" + str(self.paused_execution["provider_port"])
        if scenario == "expired": grant["expires_unix"] = int(time.time())
        if scenario == "history": grant["usage_history_sha256"] = "f" * 64
        if scenario == "budget": grant["max_cumulative_estimated_cost"] = "5"
        if scenario == "binary": grant["binary_sha256"] = "f" * 64
        if scenario == "external_gateway": grant["route"]["base_url"] = "https://gateway.example.invalid"
        if scenario == "workspace":
            self.docker("exec", bundle.workspace.container, "/usr/local/bin/python3", "-I", "-c",
                        "from pathlib import Path; Path('/workspace/continued.txt').write_text('changed')")
        if scenario == "service":
            self.docker("exec", bundle.services.backend.handles["main"], "/usr/local/bin/python3", "-I",
                        "/opt/checkpoint/service.py", "business", "mutate")
        calls, provider = [], {}
        original_rpc = bundle.workspace.rpc
        def rpc(operation, **body):
            calls.append(operation)
            if operation != "cold_dispatch": return original_rpc(operation, **body)
            if scenario == "detached":
                program = """import importlib.util,json,os,sys
from pathlib import Path
s=importlib.util.spec_from_file_location('owned_io','/opt/checkpoint/workspace_io.py')
m=importlib.util.module_from_spec(s);s.loader.exec_module(m)
child='import os,time\\nif os.fork(): os._exit(0)\\nos.setsid()\\nopen(\"/tmp/escaped-owned.pid\",\"w\").write(str(os.getpid()))\\ntry:\\n while True: os.write(2,b\"x\"*65536)\\nexcept OSError:\\n while True: time.sleep(1)\\n'
try: m.bounded_exec([sys.executable,'-c',child],Path.cwd(),{},b'',5)
except ValueError:
    pid=int(Path('/tmp/escaped-owned.pid').read_text());os.kill(pid,0)
    print(json.dumps(dict(escaped_pid=pid,output_capped=True)))
else: raise AssertionError('uncapped child')
"""
                state = json.loads(self.docker("exec", "--workdir", "/workspace", bundle.workspace.container,
                                   "/usr/local/bin/python3", "-I", "-c", program))
                self.assertTrue(state["output_capped"])
                self.assertGreater(state["escaped_pid"], 1)
                raise SnapshotError("fixture_output_limit")
            # Test-only loopback transport starts after all host admission checks.
            # Production code executes only the immutable helper and YHC binary.
            carrier = dict(handoff=body["launch"]["handoff"], fixture_port=self.paused_execution["provider_port"],
                           checkpoint_path=body["launch"]["checkpoint_path"])
            process = subprocess.Popen(["docker", "exec", "--workdir", "/workspace", bundle.workspace.container,
                "/usr/local/bin/python3", "-I", "/opt/checkpoint/native_handoff.py", self.session,
                "launcher_unknown" if scenario == "unknown" else "launcher_edit", json.dumps(grant["price_card"]),
                json.dumps(carrier)], stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True)
            try:
                self.assertTrue(select.select([process.stdout], [], [], 5)[0], "fixture provider did not become ready")
                ready = json.loads(process.stdout.readline())
                self.assertTrue(ready["ready"])
                result = original_rpc(operation, **body)
                evidence = os.environ.get("YHC_NATIVE_RECOVERY_PROOF_DIR")
                if evidence:
                    suffix = "-native-session" if generated_session else ""
                    write_report(Path(evidence) / ("launcher-" + scenario + suffix + "-observed.json"), result)
                if result.get("checkpoint", {}).get("snapshot", {}).get("provider_calls") == 0:
                    self.docker("exec", bundle.workspace.container, "/bin/kill", "-TERM", str(ready["pid"]))
                try:
                    stdout, stderr = process.communicate(timeout=10)
                except subprocess.TimeoutExpired:
                    raise AssertionError("fixture received no expected request: " + json.dumps(dict(exit_code=result.get("exit_code"),
                              error=(result.get("terminal") or {}).get("error"),
                              provider_calls=result.get("checkpoint", {}).get("snapshot", {}).get("provider_calls")))) from None
                self.assertEqual(process.returncode, 0, stderr[:1024])
                provider.update(json.loads(stdout))
                return result
            finally:
                if process.poll() is None:
                    process.kill(); process.communicate(timeout=5)
        bundle.workspace.rpc = rpc
        plan = cohort_plan([manifest["usage_history"], history(str(uuid.uuid4()))], cohort=manifest["cohort_id"],
                           cap="0.1" if scenario == "cohort_budget" else "6")
        plan["expires_unix"] = int(time.time()) + 2000
        cohort = CohortAdmission.create(self.control / "cohort", plan, digest(plan))
        before_cohort = cohort.inspect()
        launcher = ColdBenchmarkLauncher(bundle, cohort=cohort)
        anchor = digest(grant)
        if scenario == "detached":
            with self.assertRaisesRegex(SnapshotError, "fixture_output_limit"):
                launcher.dispatch(self.bundle_path, self.bundle_digest, self.snapshot, grant, anchor, "fixture")
            stopped = json.loads(self.docker("inspect", bundle.workspace.container))[0]["State"]
            self.assertFalse(stopped["Running"] or stopped["Restarting"])
            self.assertEqual(stopped["Pid"], 0)
            segment = manifest["paused_invocation"]["record"]["segment_id"]
            self.assertTrue((self.control / ("dispatch." + segment + ".json")).is_file())
            self.assertFalse((self.control / ("dispatch-receipt." + segment + ".json")).exists())
            self.assertEqual(calls.count("cold_dispatch"), 1)
            self.assertTrue(cohort.inspect()["blocked"])
            self.assertEqual(cohort.inspect()["retained_amount"], "5.000000000000")
            volume = restored["workspace_runtime"]["volume"]
            self.docker("volume", "inspect", volume)
            retained = self.docker("run", "--rm", "--pull", "never", "--read-only", "--network", "none", "--cap-drop", "ALL",
                                  "--mount", "type=volume,src=" + volume + ",dst=/proof,readonly", "--entrypoint",
                                  "/usr/local/bin/python3", self.image, "-I", "-c",
                                  "from pathlib import Path; print(Path('/proof/continued.txt').read_bytes().hex())")
            self.assertEqual(bytes.fromhex(retained), b"continued once\n")
            evidence = os.environ.get("YHC_NATIVE_RECOVERY_PROOF_DIR")
            if evidence:
                write_report(Path(evidence) / "launcher-detached.json", dict(verification_complete=True,
                             workspace_stopped=True, source_claim_retained=True, volume_retained=True,
                             source_progress_retained=True, real_provider_requests=0))
            return
        if scenario not in ("known", "unknown", "auth"):
            with self.assertRaises(SnapshotError):
                launcher.dispatch(self.bundle_path, self.bundle_digest, self.snapshot, grant, anchor, "fixture")
            self.assertNotIn("cold_dispatch", calls)
            self.assertFalse((self.control / ("dispatch." + manifest["paused_invocation"]["record"]["segment_id"] + ".json")).exists())
            self.assertEqual(cohort.inspect(), before_cohort)
            if scenario in ("cohort_budget", "external_gateway"):
                self.assertEqual(calls, [])
                retained = self.docker("exec", bundle.workspace.container, "/usr/local/bin/python3", "-I", "-c",
                                       "from pathlib import Path; print(Path('/workspace/continued.txt').read_bytes().hex())")
                self.assertEqual(bytes.fromhex(retained), b"continued once\n")
            evidence = os.environ.get("YHC_NATIVE_RECOVERY_PROOF_DIR")
            if evidence and scenario in ("cohort_budget", "external_gateway"):
                suffix = "-native-session" if generated_session else ""
                write_report(Path(evidence) / ("launcher-" + scenario + suffix + ".json"),
                             dict(verification_complete=True, session_id=self.session, generated_session=generated_session,
                                  dispatched=False, claim_consumed=False, progress_retained=True,
                                  cohort_accounting=cohort.inspect(), real_provider_requests=0))
            return
        result = launcher.dispatch(self.bundle_path, self.bundle_digest, self.snapshot, grant, anchor, "fixture")
        self.assertFalse(provider["provider_protocol_errors"], provider)
        self.assertEqual(provider["real_provider_requests"], 0)
        self.assertEqual(provider["observed_provider_requests"], 1 if scenario == "unknown" else 0 if scenario == "auth" else 2)
        self.assertEqual(result["usage_history"]["segments"][:-1], manifest["usage_history"]["segments"])
        self.assertEqual(result["accounting_complete"], scenario != "unknown")
        self.assertEqual(result["status"], "completed" if scenario == "known" else "stopped")
        self.assertFalse(result["automatic_paid_resume"])
        accounting = cohort.inspect()
        self.assertEqual(accounting, CohortAdmission(cohort.root, digest(plan)).inspect())
        self.assertEqual(accounting["usage_histories"][plan["sessions"][1]["session_id"]], plan["sessions"][1]["usage_history"])
        self.assertEqual(accounting["blocked"], scenario == "unknown")
        if scenario == "unknown":
            self.assertNotEqual(accounting["retained_amount"], "0.000000000000")
            self.assertEqual(accounting["usage_histories"][self.session], manifest["usage_history"])
        else:
            self.assertEqual(accounting["retained_amount"], "0.000000000000")
            self.assertEqual(accounting["usage_histories"][self.session], result["usage_history"])
        with self.assertRaisesRegex(SnapshotError, "already_claimed"):
            launcher.dispatch(self.bundle_path, self.bundle_digest, self.snapshot, grant, anchor, "fixture")
        self.assertEqual(calls.count("cold_dispatch"), 1)
        if scenario == "known":
            self.assertEqual(self.docker("exec", bundle.workspace.container, "/bin/cat", "/workspace/continued.txt"), "edited after cold restore")
        elif scenario == "unknown":
            self.assertNotEqual(result["execution"]["checkpoint"]["snapshot"]["cost"]["reservation"]["retained_amount"], "0.000000000000")
        else:
            self.assertIn("model_binding_route_revision_changed", result["execution"]["terminal"]["error"]["message"])
        evidence = os.environ.get("YHC_NATIVE_RECOVERY_PROOF_DIR")
        if evidence:
            suffix = "-native-session" if generated_session else ""
            write_report(Path(evidence) / ("launcher-" + scenario + suffix + ".json"), dict(verification_complete=True,
                         session_id=self.session, generated_session=generated_session,
                         dispatch=result, cohort_accounting=accounting, provider=provider, original_restore_receipt=restored))

    def test_benchmark_launcher_native_generated_session_completes_with_closed_cumulative_usage(self):
        self.check_benchmark_launcher("known", generated_session=True)

    def test_benchmark_launcher_native_generated_session_unknown_usage_still_retains_full_allocation(self):
        self.check_benchmark_launcher("unknown", generated_session=True)

    def test_benchmark_launcher_native_generated_session_exhaustion_still_preserves_progress(self):
        self.check_benchmark_launcher("cohort_budget", generated_session=True)

    def test_benchmark_launcher_shared_budget_exhaustion_preserves_real_restored_progress(self):
        self.check_benchmark_launcher("cohort_budget")

    def test_benchmark_launcher_external_gateway_keeps_claim_budget_and_real_progress(self):
        self.check_benchmark_launcher("external_gateway")

    def test_benchmark_launcher_joins_real_recovery_and_cumulative_usage_before_dispatch(self):
        self.check_benchmark_launcher("known")

    def test_benchmark_launcher_unknown_usage_retains_claim_and_cumulative_history(self):
        self.check_benchmark_launcher("unknown")

    def test_benchmark_launcher_rejects_changed_service_before_dispatch(self):
        self.check_benchmark_launcher("service")

    def test_benchmark_launcher_rejects_changed_workspace_before_dispatch(self):
        self.check_benchmark_launcher("workspace")
        evidence = os.environ.get("YHC_NATIVE_RECOVERY_PROOF_DIR")
        if evidence:
            self.assertFalse((Path(evidence) / "launcher-workspace.json").exists(), "changed progress cannot get a preserved-progress receipt")

    def test_benchmark_launcher_rejects_expired_grant_before_dispatch(self):
        self.check_benchmark_launcher("expired")

    def test_benchmark_launcher_rejects_rebound_history_before_dispatch(self):
        self.check_benchmark_launcher("history")

    def test_benchmark_launcher_rejects_insufficient_cumulative_budget_before_dispatch(self):
        self.check_benchmark_launcher("budget")

    def test_benchmark_launcher_blocks_changed_credential_reference_without_provider_calls(self):
        self.check_benchmark_launcher("auth")

    def test_benchmark_launcher_rejects_changed_binary_before_dispatch(self):
        self.check_benchmark_launcher("binary")

    def test_benchmark_launcher_quarantines_detached_descendant_before_return(self):
        self.check_benchmark_launcher("detached")
        evidence = os.environ.get("YHC_NATIVE_RECOVERY_PROOF_DIR")
        if evidence:
            receipt = json.loads((Path(evidence) / "launcher-detached.json").read_bytes())
            self.assertTrue(receipt["verification_complete"] and receipt["workspace_stopped"] and receipt["source_progress_retained"])

    def test_container_dispatch_transport_bounds_both_output_pipes(self):
        workspace, unused_volume = self.provision_workspace()
        program = """import importlib.util, json, os, sys
from pathlib import Path
spec=importlib.util.spec_from_file_location('owned_workspace_io','/opt/checkpoint/workspace_io.py')
m=importlib.util.module_from_spec(spec); spec.loader.exec_module(m)
checks=[]
for fd in (1,2):
    child='import os\\nwhile True: os.write('+str(fd)+',b\"x\"*65536)\\n'
    try: m.bounded_exec([sys.executable,'-c',child],Path.cwd(),{},b'',5)
    except ValueError as error:
        assert str(error)=='unsupported_workspace_state'
        checks.append(fd)
    else: raise AssertionError('uncapped child output')
print(json.dumps(dict(capped_streams=checks,real_provider_requests=0)))
"""
        result = json.loads(self.docker("exec", "--workdir", "/workspace", workspace.container,
                            "/usr/local/bin/python3", "-I", "-c", program))
        self.assertEqual(result, dict(capped_streams=[1, 2], real_provider_requests=0))

    def target_bundle(self):
        project, definition, unused_attempt = self.provision(target=True)
        # Existing generic fixture provisions an independent attempt; bind this
        # cohort's service and workspace targets to that exact same attempt.
        self.attempt = unused_attempt
        workspace, _ = self.provision_workspace(target=True)
        services = ServiceSnapshots(self.plan, DockerServices(project, definition, self.plan), control_dir=self.control)
        return RecoveryBundle(services, workspace)

    def test_destroyed_workspace_services_native_session_and_usage_restore_together(self):
        manifest, observed, git_status = self.capture_joint()
        bundle = self.target_bundle()
        receipt = bundle.restore(self.bundle_path, self.bundle_digest, self.snapshot, self.attempt)
        self.assertEqual(receipt["status"], "restored_components_verified")
        self.assertFalse(receipt["can_resume_agent"])
        self.assertTrue(receipt["workspace_restored"] and receipt["native_session_loaded"] and receipt["cumulative_usage_restored"])
        self.assertEqual(receipt["accounting"]["totals"]["total_tokens"], 28)
        self.assertEqual(receipt["native_proof"]["native_usage"]["total_tokens"], 18)
        self.assertNotEqual(receipt["native_proof"]["process_nonce"], manifest["native_proof"]["process_nonce"])
        self.assertEqual(self.docker("exec", "--workdir", "/workspace", bundle.workspace.container, "git", "status", "--porcelain=v1"), git_status)
        self.assertEqual(self.docker("exec", bundle.workspace.container, "/bin/cat", "/workspace/effects.count"), "1")
        for name, original in observed.items():
            handle = bundle.services.backend.handles[name]
            actual = json.loads(self.docker("exec", handle, "/usr/local/bin/python3", "-I", "/opt/checkpoint/service.py", "business", "state"))
            self.assertEqual((actual["memory"], actual["durable"], actual["next_random"]),
                             (original["memory"], original["durable"], original["next_random"]))
        evidence = os.environ.get("YHC_NATIVE_RECOVERY_PROOF_DIR")
        if evidence:
            write_report(Path(evidence) / "joint-roundtrip.json", dict(manifest=manifest, receipt=receipt,
                         git_status_preserved=True, committed_effect_count=1, source_containers_and_volumes_destroyed=True,
                         provider_requests=0))

    def test_actual_native_loader_failure_quarantines_workspace_and_service_targets(self):
        self.capture_joint()
        bundle = self.target_bundle()
        original = bundle.workspace.rpc
        def corrupt_before_native_load(operation, **body):
            if operation == "native_probe":
                code = "from pathlib import Path; Path('/workspace/.yhc/transcripts/" + self.session + ".jsonl').write_text('corrupt native record\\n')"
                self.docker("exec", bundle.workspace.container, "/usr/local/bin/python3", "-I", "-c", code)
            return original(operation, **body)
        bundle.workspace.rpc = corrupt_before_native_load
        with self.assertRaises(SnapshotError):
            bundle.restore(self.bundle_path, self.bundle_digest, self.snapshot, self.attempt)
        service_ids = [r["container_id"] for r in json.loads((self.control / ("bundle-restore." + self.bundle_digest + ".json")).read_text())["services"]["containers"]]
        rows = json.loads(self.docker("inspect", bundle.workspace.container, *service_ids))
        self.assertTrue(all(not r["State"]["Running"] for r in rows))
        self.assertFalse((self.control / ("activation." + self.service_digest + ".json")).exists())

    def check_public_execution_handoff(self, scenario):
        manifest, observed, git_status = self.capture_joint(unbound=scenario == "unbound", tool_ready=scenario == "tool")
        bundle = self.target_bundle()
        receipt = bundle.restore(self.bundle_path, self.bundle_digest, self.snapshot, self.attempt)
        original_history = copy.deepcopy(manifest["usage_history"])
        original_claim = (self.control / ("bundle-restore." + self.bundle_digest + ".json")).read_bytes()
        original_tree = bundle.workspace.rpc("inventory")
        price = original_history["segments"][0]["usage"]["cost"]["price_card"]
        try:
            result = json.loads(self.docker("exec", "--workdir", "/workspace", bundle.workspace.container,
                                "/usr/local/bin/python3", "-I", "/opt/checkpoint/native_handoff.py",
                                self.session, scenario, json.dumps(price)))
        except subprocess.CalledProcessError as error:
            self.fail("owned offline handoff fixture: " + error.stderr)
        self.assertEqual(result["scope"], "offline_public_exec_resume")
        self.assertEqual(result["provider"], "synthetic_loopback")
        self.assertEqual(result["real_provider_requests"], 0)
        terminal = result["result"]
        evidence = os.environ.get("YHC_NATIVE_RECOVERY_PROOF_DIR")
        if evidence:
            write_report(Path(evidence) / ("native-handoff-observed-" + self.attempt + ".json"),
                         dict(scenario=scenario, bundle_sha256=self.bundle_digest, offline_execution=result,
                              verification_complete=False, can_resume_agent=False))
        self.assertFalse(result["provider_protocol_errors"], result["provider_protocol_errors"])
        self.assertEqual(terminal["session_id"], self.session)
        new_usage = terminal["usage"]
        extended = history(self.session, [s["usage"] for s in original_history["segments"]] + [new_usage])
        accounting = usage_history(extended, self.session)
        self.assertEqual(extended["segments"][:-1], original_history["segments"])
        self.assertEqual(len(set(extended["segment_ids"])), 3)
        repeated = history(self.session, [s["usage"] for s in extended["segments"]] + [new_usage])
        with self.assertRaises(SnapshotError):
            usage_history(repeated, self.session)
        # This is an offline fixture receipt, not the production budget pool.
        # A new trusted process must reconstruct the same totals from disk.
        history_receipt = dict(version=1, scope="offline_fixture_receipt", session_id=self.session,
                               bundle_sha256=self.bundle_digest, attempt_id=self.attempt, history=extended)
        receipt_path = self.control / ("handoff-history." + self.attempt + ".json")
        write_report(receipt_path, history_receipt)
        history_digest = digest(history_receipt)
        code = ("import json,sys; from scripts.terminal_bench.service_snapshot import digest,private_json; "
                "from scripts.terminal_bench.workspace_recovery import usage_history; "
                "v=private_json(sys.argv[1]); assert digest(v)==sys.argv[2]; "
                "assert v['bundle_sha256']==sys.argv[3] and v['attempt_id']==sys.argv[4]; "
                "print(json.dumps(usage_history(v['history'],v['session_id'])))")
        readback = subprocess.run([sys.executable, "-c", code, str(receipt_path), history_digest,
                                   self.bundle_digest, self.attempt], cwd=Path(__file__).resolve().parents[2],
                                  capture_output=True, text=True, timeout=10, check=True)
        self.assertEqual(json.loads(readback.stdout), accounting)
        self.assertFalse(accounting["new_spending_authorized"])
        self.assertEqual(result["observed_provider_requests"], 2 if scenario == "tool" else int(scenario in ("known", "unknown")), terminal)
        native_after = None
        if scenario in ("known", "tool"):
            self.assertEqual((result["exit_code"], terminal["status"], terminal["output"]), (0, "completed", "offline continuation complete"))
            self.assertTrue(accounting["coverage_complete"])
            calls = 2 if scenario == "tool" else 1
            self.assertEqual((new_usage["provider_calls"], new_usage["total_tokens"]), (calls, 14 * calls))
            self.assertEqual((accounting["totals"]["provider_calls"], accounting["totals"]["total_tokens"]), (2 + calls, 28 + 14 * calls))
            after = bundle.workspace.rpc("native_probe", session_id=self.session)
            native_after = after
            self.assertTrue(after["validation_complete"], after["blocking_reasons"])
            self.assertEqual(after["message_count"], receipt["native_proof"]["message_count"] + (4 if scenario == "tool" else 2))
            self.assertEqual(after["native_usage"]["total_tokens"], receipt["native_proof"]["native_usage"]["total_tokens"] + 14 * calls)
            self.assertNotEqual(after["messages_sha256"], receipt["native_proof"]["messages_sha256"])
        else:
            self.assertNotEqual(result["exit_code"], 0)
            expected = "run_budget_exceeded" if scenario == "denied" else "run_usage_unknown"
            self.assertEqual(terminal["error"]["code"], expected)
            self.assertEqual(accounting["coverage_complete"], scenario != "unknown")
            self.assertEqual(accounting["totals"]["total_tokens"], 28)
            if scenario == "unknown":
                self.assertEqual(new_usage["unknown_calls"], 1)
                self.assertNotEqual(new_usage["cost"]["reservation"]["retained_amount"], "0.000000000000")
            else:
                self.assertEqual(new_usage["provider_calls"], 0)
        current_tree = bundle.workspace.rpc("inventory")
        current_files = user_files(current_tree)
        if scenario == "tool":
            self.assertEqual(self.docker("exec", bundle.workspace.container, "/bin/cat", "/workspace/continued.txt"), "continued once")
            self.assertEqual(len(result["durable_admissions"]), 2)
            checkpoint = result["durable_checkpoint"]
            self.assertTrue(checkpoint["accounting_complete"] and checkpoint["admission_closed"])
            self.assertEqual(checkpoint["snapshot"]["call_ledger"], new_usage["call_ledger"])
            self.assertEqual(checkpoint["snapshot"]["cost"], new_usage["cost"])
            self.assertFalse(checkpoint["can_resume_agent"] or checkpoint["new_spending_authorized"])
            current_files = [row for row in current_files if row["path"] != "continued.txt"]
        self.assertEqual(current_files, user_files(original_tree))
        actual_git = self.docker("exec", "--workdir", "/workspace", bundle.workspace.container, "git", "status", "--porcelain=v1")
        self.assertEqual(actual_git.replace("?? continued.txt\n", "") if scenario == "tool" else actual_git, git_status)
        self.assertEqual(self.docker("exec", bundle.workspace.container, "/bin/cat", "/workspace/effects.count"), "1")
        self.assertEqual(bundle.workspace.inventory(), receipt["workspace_runtime"])
        self.assertEqual(original_claim, (self.control / ("bundle-restore." + self.bundle_digest + ".json")).read_bytes())
        with self.assertRaisesRegex(SnapshotError, "already_claimed"):
            bundle.restore(self.bundle_path, self.bundle_digest, self.snapshot, self.attempt)
        services = {}
        for name, original in observed.items():
            actual = json.loads(self.docker("exec", bundle.services.backend.handles[name], "/usr/local/bin/python3", "-I",
                                           "/opt/checkpoint/service.py", "business", "state"))
            self.assertEqual((actual["memory"], actual["durable"], actual["next_random"]),
                             (original["memory"], original["durable"], original["next_random"]))
            self.assertNotEqual(actual["instance"], original["instance"])
            self.assertGreaterEqual(actual["logical_ns"], receipt["services"]["expected_clocks"][name]["logical_ns"])
            services[name] = actual
        self.assertFalse(receipt["can_resume_agent"] or result["can_resume_agent"])
        if evidence:
            write_report(Path(evidence) / ("native-handoff-" + scenario + ".json"),
                         dict(bundle_sha256=self.bundle_digest, attempt_id=self.attempt, restored_components=receipt,
                              source_containers_and_volumes_destroyed=True, offline_execution=result,
                              original_history_sha256=digest(original_history), extended_history=extended, accounting=accounting,
                              history_receipt_sha256=history_digest, history_readback_process_verified=True,
                              history_receipt_scope="offline_fixture_receipt",
                              native_after=native_after,
                              services_after=services, committed_effect_count=1, original_user_files_preserved=True,
                              new_tool_effects=1 if scenario == "tool" else 0,
                              verification_complete=True, can_resume_agent=False, real_provider_requests=0))

    def test_public_exec_first_turn_preserves_native_service_and_usage_state(self):
        self.check_public_execution_handoff("known")

    def test_public_exec_new_tool_after_restore_has_durable_invocation_accounting(self):
        self.check_public_execution_handoff("tool")

    def check_interrupted_handoff(self, scenario):
        after_tool = scenario == "crash_after_tool"
        manifest, observed, unused_git_status = self.capture_joint(tool_ready=after_tool)
        bundle = self.target_bundle()
        receipt = bundle.restore(self.bundle_path, self.bundle_digest, self.snapshot, self.attempt)
        claim = self.control / ("bundle-restore." + self.bundle_digest + ".json")
        original_claim = claim.read_bytes()
        original_tree = user_files(bundle.workspace.rpc("inventory"))
        price = manifest["usage_history"]["segments"][0]["usage"]["cost"]["price_card"]
        try:
            result = json.loads(self.docker("exec", "--workdir", "/workspace", bundle.workspace.container,
                                "/usr/local/bin/python3", "-I", "/opt/checkpoint/native_handoff.py",
                                self.session, scenario, json.dumps(price)))
        except subprocess.CalledProcessError as error:
            self.fail("owned crash fixture: " + error.stderr)
        evidence = os.environ.get("YHC_NATIVE_RECOVERY_PROOF_DIR")
        if evidence:
            write_report(Path(evidence) / ("native-interrupted-observed-" + self.attempt + ".json"),
                         dict(scenario=scenario, offline_execution=result, verification_complete=False, can_resume_agent=False))
        self.assertTrue(result["killed_by_fixture"])
        self.assertEqual(result["exit_code"], -9)
        self.assertIsNone(result["result"])
        self.assertEqual(result["real_provider_requests"], 0)
        self.assertFalse(result["provider_protocol_errors"], result["provider_protocol_errors"])
        self.assertEqual(result["observed_provider_requests"], 2 if after_tool else 1)
        checkpoint = result["durable_checkpoint"]
        self.assertFalse(checkpoint["admission_closed"] or checkpoint["accounting_complete"]
                         or checkpoint["can_resume_agent"] or checkpoint["new_spending_authorized"])
        usage = checkpoint["snapshot"]
        self.assertEqual((usage["in_flight"], usage["known_calls"], usage["total_tokens"]), (1, int(after_tool), 14 if after_tool else 0))
        self.assertNotEqual(usage["cost"]["reservation"]["outstanding_amount"], "0.000000000000")
        extended = history(self.session, [s["usage"] for s in manifest["usage_history"]["segments"]] + [usage])
        accounting = usage_history(extended, self.session)
        self.assertFalse(accounting["coverage_complete"] or accounting["new_spending_authorized"])
        native = result["native_after_crash"]
        self.assertFalse(native["validation_complete"] or native["can_resume_agent"])
        self.assertIn("native_turn_incomplete", native["blocking_reasons"])
        # Older valid receipts and the completed first tool response cannot
        # substitute for the interrupted latest turn or its outstanding call.
        self.assertTrue(native["native_usage"]["current_context_usage_known"])
        self.assertTrue(native["native_usage"]["last_response_had_usage_metadata"])
        self.assertEqual(native["native_usage"]["total_tokens"], 32 if after_tool else 18)
        self.assertEqual(native["message_count"], 7 if after_tool else 5)
        with self.assertRaisesRegex(SnapshotError, "native_restore_state_or_usage_incomplete"):
            native_proof(native, self.session, "/workspace")
        current_tree = user_files(bundle.workspace.rpc("inventory"))
        if after_tool:
            self.assertEqual(self.docker("exec", bundle.workspace.container, "/bin/cat", "/workspace/continued.txt"), "continued once")
            current_tree = [row for row in current_tree if row["path"] != "continued.txt"]
        self.assertEqual(current_tree, original_tree)
        self.assertEqual(self.docker("exec", bundle.workspace.container, "/bin/cat", "/workspace/effects.count"), "1")
        self.assertEqual(claim.read_bytes(), original_claim)
        with self.assertRaisesRegex(SnapshotError, "already_claimed"):
            bundle.restore(self.bundle_path, self.bundle_digest, self.snapshot, self.attempt)
        self.assertEqual(bundle.workspace.inventory(), receipt["workspace_runtime"])
        for name, old in observed.items():
            actual = json.loads(self.docker("exec", bundle.services.backend.handles[name], "/usr/local/bin/python3", "-I",
                                           "/opt/checkpoint/service.py", "business", "state"))
            self.assertEqual((actual["memory"], actual["durable"], actual["next_random"]), (old["memory"], old["durable"], old["next_random"]))
        if evidence:
            write_report(Path(evidence) / ("native-handoff-" + scenario + ".json"),
                         dict(scenario=scenario, bundle_sha256=self.bundle_digest, restored_components=receipt,
                              offline_execution=result, extended_history=extended, accounting=accounting,
                              source_containers_and_volumes_destroyed=True, original_user_files_preserved=True,
                              new_tool_effects=int(after_tool), original_effect_count=1, claim_unchanged=True,
                              verification_complete=True, can_resume_agent=False, new_spending_authorized=False))

    def test_interrupted_first_request_retains_reservation_and_incomplete_native_turn(self):
        self.check_interrupted_handoff("crash_before_response")

    def test_interrupted_request_after_committed_tool_cannot_replay_or_reopen_claim(self):
        self.check_interrupted_handoff("crash_after_tool")

    def test_public_exec_unknown_usage_stops_without_retry_and_retains_reservation(self):
        self.check_public_execution_handoff("unknown")

    def test_public_exec_reservation_denial_preserves_progress_without_dispatch(self):
        self.check_public_execution_handoff("denied")

    def test_public_exec_missing_model_identity_cannot_consume_a_spending_allowance(self):
        self.check_public_execution_handoff("unbound")


if __name__ == "__main__":
    unittest.main()
