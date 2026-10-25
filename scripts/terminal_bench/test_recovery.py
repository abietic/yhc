"""Independent lifecycle counterexamples; no providers or Docker required."""

import copy
import json
import os
import subprocess
import sys
import tempfile
import unittest
from pathlib import Path
from unittest.mock import patch

from scripts.terminal_bench.recovery import (
    RecoveryBoundaryError, continuation_report, retained_checkpoint_report,
    runtime_inventory, same_live_runtime, write_report,
)


def container(service="main", token="a"):
    return {"Id": token * 64, "Image": "sha256:" + "f" * 64, "RestartCount": 0,
            "Config": {"Labels": {"com.docker.compose.project": "fixture",
                                   "com.docker.compose.service": service},
                       "Env": ["PROV_API_KEY=must-not-be-copied"]},
            "Mounts": [{"Source": "/private/host/path"}],
            "State": {"Pid": 100 + ord(token), "StartedAt": "2026-10-25T00:00:00Z",
                      "Running": True, "Paused": False, "Restarting": False}}


def live_inventory():
    return runtime_inventory("fixture", ["main", "api"], [container(), container("api", "b")])


class RecoveryTests(unittest.TestCase):
    def test_same_service_generations_allow_only_live_continuation(self):
        before, after = live_inventory(), live_inventory()
        self.assertEqual(after["containers"][0]["health_status"], "not_configured")
        self.assertTrue(same_live_runtime(before, after))
        result = continuation_report(before, after, session_identified=True,
                                     usage_complete=True, grant_available=True)
        self.assertTrue(result["warm_continuation_eligible_at_observation"])
        self.assertFalse(result["cold_resume_supported"])
        self.assertIn("clock_continuity_not_verified", result["cold_resume_blockers"])

    def test_stopped_restarted_paused_or_replaced_sidecar_blocks_continuation(self):
        before = live_inventory()
        for change in ({"running": False}, {"started_at": "later", "restart_count": 1},
                       {"container_id": "c" * 64}, {"pid": 999}, {"paused": True},
                       {"restarting": True}, {"health_status": "unhealthy"}, {"image": "sha256:" + "e" * 64}):
            with self.subTest(change=change):
                after = copy.deepcopy(before)
                after["containers"][0].update(change)
                self.assertFalse(same_live_runtime(before, after))
        for value in (None, {}, {"inventory_complete": False},
                      {**before, "services": ["main"]}, {**before, "containers": []}):
            self.assertFalse(same_live_runtime(before, value))

    def test_complete_service_inventory_requires_exact_ownership_and_coverage(self):
        rows = [container(), container("api", "b")]
        cases = [rows[:1], rows + [container("unlisted", "c")], rows + [rows[0]],
                 [container(), {**rows[1], "Id": "not-an-id"}]]
        wrong_owner = copy.deepcopy(rows)
        wrong_owner[1]["Config"]["Labels"]["com.docker.compose.project"] = "another-chat"
        cases.append(wrong_owner)
        for inspected in cases:
            with self.subTest(inspected=inspected):
                with self.assertRaises(RecoveryBoundaryError):
                    runtime_inventory("fixture", ["main", "api"], inspected)

    def test_inventory_never_copies_credentials_mounts_or_arbitrary_docker_data(self):
        value = json.dumps(live_inventory())
        for private in ("PROV_API_KEY", "must-not-be-copied", "/private/host/path", "Env", "Mounts"):
            self.assertNotIn(private, value)

    def test_complete_capture_and_matching_archive_do_not_prove_cold_restore(self):
        manifest = {"capture": "complete", "workspace_in_archive": True,
                    "retained_environment_required": False,
                    "native_session_files": [{"path": "app/.yhc/session.json"}],
                    "environment_handles": [{"id": "a" * 64, "service": "main"},
                                            {"id": "b" * 64, "service": "api"}]}
        current = live_inventory()
        for row in current["containers"]:
            row.update(running=False, pid=0)
        report = retained_checkpoint_report(manifest, archive_verified=True, current=current)
        self.assertFalse(report["can_resume"])
        self.assertFalse(report["service_inventory_complete"])
        self.assertEqual(report["native_session"], "retained_not_load_verified")
        self.assertEqual(report["workspace"], "archived_not_restore_verified")
        self.assertTrue(all(r["process_memory"] == "lost_on_stop" for r in report["services"]))

    def test_live_identity_never_overrides_usage_or_authorization(self):
        runtime = live_inventory()
        for fields in ({"usage_complete": False}, {"grant_available": False}, {"session_identified": False}):
            kwargs = dict(session_identified=True, usage_complete=True, grant_available=True)
            kwargs.update(fields)
            result = continuation_report(runtime, runtime, **kwargs)
            self.assertFalse(result["warm_continuation_eligible_at_observation"])

    @unittest.skipUnless(os.name == "posix", "POSIX private report permissions")
    def test_report_never_overwrites_files_or_follows_output_link(self):
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory).resolve() / "report.json"
            write_report(path, {"can_resume": False})
            self.assertEqual(path.stat().st_mode & 0o777, 0o600)
            with self.assertRaises(FileExistsError):
                write_report(path, {})
            link = Path(directory).resolve() / "link"
            link.symlink_to(path)
            with self.assertRaises(FileExistsError):
                write_report(link, {})
            self.assertEqual(json.loads(path.read_text()), {"can_resume": False})

    @unittest.skipUnless(os.name == "posix", "POSIX private report permissions")
    def test_report_rejects_shared_or_symlinked_parent(self):
        with tempfile.TemporaryDirectory() as directory:
            parent = Path(directory).resolve()
            shared = parent / "shared"
            shared.mkdir(mode=0o755)
            with self.assertRaises(RecoveryBoundaryError):
                write_report(shared / "report.json", {})
            link = parent / "link"
            link.symlink_to(parent, target_is_directory=True)
            with self.assertRaises(RecoveryBoundaryError):
                write_report(link / "report.json", {})

    def test_private_report_fails_closed_on_unsupported_host(self):
        with patch("scripts.terminal_bench.recovery.os.name", "nt"):
            with self.assertRaisesRegex(RecoveryBoundaryError, "requires_posix"):
                write_report("unused.json", {})

    @unittest.skipUnless(os.name == "posix", "POSIX private report permissions")
    def test_cli_accepts_legacy_manifest_but_will_not_claim_resume(self):
        import hashlib
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory).resolve()
            (root / "native-checkpoint.tar.gz").write_bytes(b"retained opaque bytes")
            manifest = {"capture": "complete", "workspace_in_archive": True,
                        "native_session_files": [{"path": "session"}],
                        "checkpoint_sha256": hashlib.sha256(b"retained opaque bytes").hexdigest()}
            path, output = root / "manifest.json", root / "report.json"
            path.write_text(json.dumps(manifest))
            subprocess.run([sys.executable, "-m", "scripts.terminal_bench.recovery",
                            str(path), "--output", str(output)], check=True, capture_output=True)
            report = json.loads(output.read_text())
            self.assertTrue(report["archive_digest_verified"])
            self.assertFalse(report["can_resume"])
            self.assertFalse(report["cold_resume_supported"])


if __name__ == "__main__":
    unittest.main()
