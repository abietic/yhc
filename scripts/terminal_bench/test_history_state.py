"""Independent metadata admission checks; no provider, container or filesystem effects."""

import copy
import unittest
import uuid

from scripts.terminal_bench.history_state import (
    check_history_staging, history_entries, history_proof, migration_receipt,
    proof_digest, target_binding, verify_imported,
)
from scripts.terminal_bench.service_snapshot import SnapshotError


def source_proof():
    value = dict(version=1, scope="workspace-history-state-v1", session_id="session-1", root="/workspace",
                 root_identity="unix:1:2", journal_sha256="a" * 64, journal_bytes=3, restore_sha256="",
                 restore_bytes=0, origin_sha256="", origin_bytes=0, frame_sequence=2, restore_state="none",
                 semantic_sha256="b" * 64, sha256="")
    value["sha256"] = proof_digest(value)
    return value


def entries(proof):
    prefix = ".yhc/transcripts/" + proof["session_id"] + ".workspace-history"
    value = [dict(path=prefix, kind="directory", mode=0o700)]
    for name, filename in (("journal", "revisions.jsonl"), ("restore", "restore.json"), ("origin", "cold-origin.json")):
        if proof[name + "_sha256"]:
            value.append(dict(path=prefix + "/" + filename, kind="file", mode=0o600,
                              size=proof[name + "_bytes"], sha256=proof[name + "_sha256"]))
    return value


class HistoryStateTests(unittest.TestCase):
    def test_go_json_canonical_digest_includes_unicode_and_escaping(self):
        value = source_proof()
        value["root"] = "/workspace/资料<&>\u2028"
        self.assertEqual(proof_digest(value), "8fee5a1c5690219f3c8ec5bef40fe7e185268741bd1b82fd7d2a826cd3bf0c45")

    def test_proof_rejects_unknown_partial_and_ambiguous_metadata(self):
        original = source_proof()
        self.assertEqual(history_proof(original, "session-1", "/workspace"), original)
        for field, value in (("version", True), ("frame_sequence", True), ("journal_bytes", -1),
                             ("journal_bytes", 32 * (1 << 20) + 1), ("root_identity", "unknown"),
                             ("restore_state", "applying"), ("restore_state", "partial"),
                             ("sha256", "c" * 64), ("session_id", "other"), ("root", "/elsewhere")):
            with self.subTest(field=field, value=value):
                damaged = {**original, field: value}
                with self.assertRaises(SnapshotError):
                    history_proof(damaged, "session-1", "/workspace")
        with self.assertRaises(SnapshotError):
            history_proof({**original, "extra": False}, "session-1", "/workspace")

    def test_only_closed_restore_states_can_bind_a_restore_file(self):
        for state in ("completed", "rolled_back"):
            value = {**source_proof(), "restore_state": state, "restore_sha256": "c" * 64, "restore_bytes": 10}
            value["sha256"] = proof_digest(value)
            self.assertEqual(history_proof(value, "session-1", "/workspace"), value)
        value = {**source_proof(), "restore_state": "completed"}
        value["sha256"] = proof_digest(value)
        with self.assertRaises(SnapshotError):
            history_proof(value, "session-1", "/workspace")

    def test_physical_file_binding_is_required_even_with_a_semantic_proof(self):
        proof = source_proof()
        native = dict(cwd="/workspace", workspace_history=proof)
        self.assertEqual(history_entries(entries(proof), "session-1", native, verified=True), proof)
        for fault in ("bytes", "mode", "extra", "missing", "no_proof", "legacy"):
            tree, observed = copy.deepcopy(entries(proof)), copy.deepcopy(native)
            if fault == "bytes": tree[1]["sha256"] = "d" * 64
            if fault == "mode": tree[1]["mode"] = 0o644
            if fault == "extra": tree.append(dict(path=tree[0]["path"] + "/restore.tmp"))
            if fault == "missing": tree.pop()
            if fault == "no_proof": observed.pop("workspace_history")
            with self.subTest(fault=fault), self.assertRaises(SnapshotError):
                history_entries(tree, "session-1", observed, verified=fault != "legacy")
        with self.assertRaises(SnapshotError):
            history_entries([], "session-1", native, verified=True)

    def test_pending_marker_blocks_even_when_active_history_is_absent(self):
        for path in (".yhc/transcripts/session-1.workspace-history.cold-import.json", ".yhc-restore-operation.tmp"):
            with self.subTest(path=path), self.assertRaises(SnapshotError):
                check_history_staging([dict(path=path)], "session-1")

    def test_import_provenance_binds_target_generation_and_never_grants_dispatch(self):
        source, attempt = source_proof(), str(uuid.uuid4())
        binding = target_binding("a" * 64, attempt, dict(volume="new", container_id="b" * 64))
        self.assertNotEqual(binding, target_binding("a" * 64, attempt, dict(volume="other", container_id="b" * 64)))
        receipt = dict(version=1, scope="workspace-history-import-v1", session_id="session-1", migration_id=attempt,
                       binding_sha256=binding, source=source, root="/workspace", root_identity="unix:3:4",
                       journal_sha256="c" * 64, journal_bytes=3, restore_sha256="", restore_bytes=0,
                       archive_directory="session-1.workspace-history.cold-source-" + attempt, can_resume_agent=False)
        imported = {**source, "root_identity": "unix:3:4", "journal_sha256": "c" * 64,
                    "origin_sha256": "d" * 64, "origin_bytes": 40}
        imported["sha256"] = proof_digest(imported)
        native = dict(cwd="/workspace", workspace_history=imported, workspace_history_origin=receipt)
        verify_imported(native, source, receipt, entries(imported), "session-1", "/workspace", binding, attempt)
        for field, value in (("can_resume_agent", True), ("migration_id", str(uuid.uuid4())),
                             ("binding_sha256", "e" * 64), ("root_identity", "unknown")):
            with self.subTest(field=field), self.assertRaises(SnapshotError):
                migration_receipt({**receipt, field: value}, source, binding, attempt, "/workspace")
        damaged = copy.deepcopy(native)
        damaged["workspace_history"]["semantic_sha256"] = "e" * 64
        damaged["workspace_history"]["sha256"] = proof_digest(damaged["workspace_history"])
        with self.assertRaises(SnapshotError):
            verify_imported(damaged, source, receipt, entries(damaged["workspace_history"]), "session-1", "/workspace", binding, attempt)
        with self.assertRaises(SnapshotError):
            verify_imported({**native, "workspace_history_origin": None}, source, receipt, entries(imported), "session-1", "/workspace", binding, attempt)


if __name__ == "__main__":
    unittest.main()
