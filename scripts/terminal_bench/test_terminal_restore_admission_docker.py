"""Real terminal-save crash and ordinary CLI admission; no paid provider."""

import json
import os
import subprocess
import unittest
import uuid
from pathlib import Path

from scripts.terminal_bench import test_recovery_bundle_docker as native_fixture
from scripts.terminal_bench.recovery import write_report
from scripts.terminal_bench.recovery_bundle import user_files
from scripts.terminal_bench.service_snapshot import SnapshotError


@unittest.skipUnless(os.environ.get("YHC_NATIVE_RECOVERY_DOCKER_TEST") == "1"
                     and os.environ.get("YHC_TERMINAL_RESTORE_DOCKER_TEST") == "1", "explicit terminal restore Docker opt-in")
class TerminalRestoreAdmissionDockerTests(native_fixture.NativeColdDockerTests):
    @classmethod
    def setUpClass(cls):
        binary = Path(os.environ["YHC_TERMINAL_RESTORE_TEST_BINARY"]).resolve()
        if not binary.is_file():
            raise RuntimeError("owned Linux crash fixture required")
        super().setUpClass()
        previous = cls.image
        cls.image = "yhc-terminal-admission:" + uuid.uuid4().hex[:12]
        helper = "yhc-terminal-admission-build-" + uuid.uuid4().hex[:12]
        cls.docker("create", "--network", "none", "--name", helper, "--entrypoint", "/bin/true", previous)
        try:
            cls.docker("cp", str(binary), helper + ":/opt/checkpoint/terminal-restore.test")
            cls.docker("commit", helper, cls.image)
        finally:
            cls.docker("rm", "--force", helper)
        cls.addClassCleanup(cls.docker, "image", "rm", cls.image)

    def check_terminal_crash(self, direction):
        manifest, observed, _ = self.capture_joint(paused=True, environment_key=True,
                                                   generated_session=True, restored_history=True)
        bundle = self.target_bundle()
        restored = bundle.restore(self.bundle_path, self.bundle_digest, self.snapshot, self.attempt)
        workspace = bundle.workspace
        self.assertTrue(restored["history_migration"])
        record = manifest["paused_invocation"]["record"]
        usage_before = workspace.rpc("usage_probe", checkpoint_path=record["checkpoint_path"], segment_id=record["segment_id"])
        crash = subprocess.run(["docker", "exec", "--workdir", "/workspace",
            "-e", "YHC_OWNED_TERMINAL_RESTORE_FIXTURE=1", "-e", "YHC_TERMINAL_CRASH_SESSION=" + self.session,
            "-e", "YHC_TERMINAL_CRASH_DIRECTION=" + direction, workspace.container,
            "/opt/checkpoint/terminal-restore.test", "-test.run", "^TestTerminalRestoreProcessFixture$"],
            capture_output=True, timeout=30)
        self.assertEqual(crash.returncode, 73)
        prefix = "/workspace/.yhc/transcripts/" + self.session + ".workspace-history/"
        operation = self.docker("exec", workspace.container, "/bin/cat", prefix + "restore.json")
        journal = self.docker("exec", workspace.container, "/bin/cat", prefix + "revisions.jsonl")
        envelope = json.loads(operation)
        self.assertEqual(envelope["operation"]["State"], "completed" if direction == "restore" else "rolled_back")
        before = workspace.rpc("inventory")
        self.assertTrue(any(".yhc-restore-" in entry["path"] and entry["path"].endswith(".backup") for entry in before))
        with self.assertRaises(SnapshotError):
            bundle.prepare_dispatch(self.bundle_path, self.bundle_digest, self.snapshot)
        price = manifest["usage_history"]["segments"][-1]["usage"]["cost"]["price_card"]
        attempt = json.loads(self.docker("exec", "--workdir", "/workspace", workspace.container,
            "/usr/local/bin/python3", "-I", "/opt/checkpoint/native_handoff.py", self.session, "resume_guard", json.dumps(price),
            json.dumps(dict(fixture_port=self.paused_execution["provider_port"]))))
        journal_unchanged = self.docker("exec", workspace.container, "/bin/cat", prefix + "revisions.jsonl") == journal
        evidence = os.environ.get("YHC_NATIVE_RECOVERY_PROOF_DIR")
        if evidence:
            write_report(Path(evidence) / ("terminal-observed-" + direction + "-" + self.session + ".json"), dict(
                scope="owned_diagnostic_not_authority", verification_complete=False,
                provider_requests=attempt["observed_provider_requests"], journal_unchanged=journal_unchanged,
                terminal_reason=attempt["result"]["terminal_reason"], error=attempt["result"].get("error"),
                real_provider_requests=0))
        self.assertEqual(attempt["observed_provider_requests"], 0)
        self.assertEqual(attempt["provider_protocol_errors"], [])
        self.assertEqual(attempt["exit_code"], 1)
        self.assertEqual(attempt["result"]["terminal_reason"], "persistence_error")
        self.assertEqual(self.docker("exec", workspace.container, "/bin/cat", prefix + "restore.json"), operation)
        self.assertTrue(journal_unchanged, "ordinary admission published unverified restore receipts")
        self.assertEqual(user_files(workspace.rpc("inventory")), user_files(before))
        self.assertEqual(workspace.rpc("usage_probe", checkpoint_path=record["checkpoint_path"], segment_id=record["segment_id"]), usage_before)
        for name, original in observed.items():
            actual = json.loads(self.docker("exec", bundle.services.backend.handles[name],
                "/usr/local/bin/python3", "-I", "/opt/checkpoint/service.py", "business", "state"))
            self.assertEqual((actual["memory"], actual["durable"], actual["next_random"]),
                             (original["memory"], original["durable"], original["next_random"]))
        if evidence:
            write_report(Path(evidence) / ("terminal-crash-" + direction + ".json"), dict(
                verification_complete=True, imported=True, direction=direction, crash_exit=73,
                terminal_state=envelope["operation"]["State"], old_joint_blocked=True,
                ordinary_provider_requests=0, source_usage_unchanged=True,
                user_files_and_artifacts_preserved=True, service_state_preserved=True,
                ordinary_terminal=attempt["result"], real_provider_requests=0))

    def test_imported_completed_crash_blocks_ordinary_and_cold_dispatch(self):
        self.check_terminal_crash("restore")

    def test_imported_rollback_crash_blocks_ordinary_and_cold_dispatch(self):
        self.check_terminal_crash("rollback")


def load_tests(loader, tests, pattern):
    return unittest.TestSuite(TerminalRestoreAdmissionDockerTests(name) for name in (
        "test_imported_completed_crash_blocks_ordinary_and_cold_dispatch",
        "test_imported_rollback_crash_blocks_ordinary_and_cold_dispatch"))
