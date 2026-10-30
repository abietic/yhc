"""Physical history provenance and the independent logical-state projection."""

import hashlib
import json
import re

from scripts.terminal_bench.service_snapshot import digest, identity, require
from scripts.terminal_bench.workspace_io import session_identity

PROOF_KEYS = ("version", "scope", "session_id", "root", "root_identity", "journal_sha256", "journal_bytes",
              "restore_sha256", "restore_bytes", "origin_sha256", "origin_bytes", "frame_sequence", "restore_state",
              "semantic_sha256", "sha256")
RECEIPT_KEYS = {"version", "scope", "session_id", "migration_id", "binding_sha256", "source", "root", "root_identity",
                "journal_sha256", "journal_bytes", "restore_sha256", "restore_bytes", "archive_directory", "can_resume_agent"}


def sha(value):
    return isinstance(value, str) and re.fullmatch(r"[0-9a-f]{64}", value) is not None


def proof_digest(value):
    # Version 1 freezes the Go struct's field order and JSON HTML escaping.
    projected = {key: "" if key == "sha256" else value[key] for key in PROOF_KEYS}
    raw = json.dumps(projected, ensure_ascii=False, separators=(",", ":"))
    for character, escaped in (("&", "\\u0026"), ("<", "\\u003c"), (">", "\\u003e"), ("\u2028", "\\u2028"), ("\u2029", "\\u2029")):
        raw = raw.replace(character, escaped)
    return hashlib.sha256(raw.encode()).hexdigest()


def history_proof(value, session, cwd):
    require(isinstance(value, dict) and set(value) == set(PROOF_KEYS)
            and type(value["version"]) is int and value["version"] == 1
            and value["scope"] == "workspace-history-state-v1" and session_identity(session)
            and value["session_id"] == session and value["root"] == cwd
            and isinstance(value["root_identity"], str) and re.fullmatch(r"unix:[0-9]+:[0-9]+", value["root_identity"])
            and sha(value["semantic_sha256"]) and sha(value["sha256"]), "history_proof_invalid")
    for name, maximum in (("journal", 32 << 20), ("restore", 12 << 20), ("origin", 16384)):
        size, checksum = value[name + "_bytes"], value[name + "_sha256"]
        require(type(size) is int and 0 <= size <= maximum and (sha(checksum) or checksum == "" and size == 0), "history_file_binding_invalid")
    require(type(value["frame_sequence"]) is int and 0 <= value["frame_sequence"] <= 1200
            and (value["journal_sha256"] != "" or value["frame_sequence"] == 0)
            and (value["restore_sha256"] == "" and value["restore_state"] == "none"
                 or sha(value["restore_sha256"]) and value["restore_state"] in ("completed", "rolled_back"))
            and proof_digest(value) == value["sha256"], "history_proof_digest_or_state_invalid")
    return value


def check_history_staging(entries, session):
    prefix = ".yhc/transcripts/" + session + ".workspace-history"
    for entry in entries:
        path = entry["path"]
        require(path != prefix + ".cold-import.json", "workspace_history_migration_incomplete")
        require(not any(part.startswith(".yhc-restore-") and part.endswith((".backup", ".tmp", ".rollback"))
                        for part in path.split("/")), "workspace_restore_artifacts_unverified")


def history_entries(entries, session, native, *, verified):
    check_history_staging(entries, session)
    prefix = ".yhc/transcripts/" + session + ".workspace-history"
    selected = [entry for entry in entries if entry["path"] == prefix or entry["path"].startswith(prefix + "/")]
    if not selected:
        require(not native or "workspace_history" not in native, "history_proof_without_files")
        return None
    require(verified and isinstance(native, dict) and "workspace_history" in native, "workspace_history_cold_migration_unverified")
    proof = history_proof(native["workspace_history"], session, native["cwd"])
    expected = {prefix}
    for name, file in (("journal", "revisions.jsonl"), ("restore", "restore.json"), ("origin", "cold-origin.json")):
        if proof[name + "_sha256"]:
            path = prefix + "/" + file
            expected.add(path)
            require(any(e["path"] == path and e["kind"] == "file" and e["mode"] == 0o600
                        and e["size"] == proof[name + "_bytes"] and e["sha256"] == proof[name + "_sha256"] for e in selected),
                    "history_files_do_not_match_native_proof")
    require({e["path"] for e in selected} == expected
            and any(e["path"] == prefix and e["kind"] == "directory" and e["mode"] == 0o700 for e in selected), "history_layout_unverified")
    return proof


def target_binding(bundle_sha, attempt, target):
    require(sha(bundle_sha) and identity(attempt), "history_target_binding_invalid")
    return digest(dict(version=1, scope="history-import-target-v1", bundle_sha256=bundle_sha, attempt_id=attempt, workspace=target))


def migration_receipt(value, source, binding, attempt, cwd):
    require(isinstance(value, dict) and set(value) == RECEIPT_KEYS
            and type(value["version"]) is int and value["version"] == 1 and value["scope"] == "workspace-history-import-v1"
            and value["source"] == source and value["session_id"] == source["session_id"]
            and value["binding_sha256"] == binding and value["migration_id"] == attempt and identity(attempt)
            and value["root"] == cwd and isinstance(value["root_identity"], str)
            and re.fullmatch(r"unix:[0-9]+:[0-9]+", value["root_identity"])
            and value["archive_directory"] == source["session_id"] + ".workspace-history.cold-source-" + attempt
            and value["can_resume_agent"] is False, "history_migration_receipt_invalid")
    for name, maximum in (("journal", 32 << 20), ("restore", 12 << 20)):
        require(type(value[name + "_bytes"]) is int and 0 <= value[name + "_bytes"] <= maximum
                and (sha(value[name + "_sha256"]) or value[name + "_sha256"] == "" and value[name + "_bytes"] == 0),
                "history_migration_files_invalid")
    return value


def verify_imported(native, source, receipt, entries, session, cwd, binding, attempt):
    migration_receipt(receipt, source, binding, attempt, cwd)
    observed = history_entries(entries, session, native, verified=True)
    require(observed is not None and observed["semantic_sha256"] == source["semantic_sha256"]
            and observed["root_identity"] == receipt["root_identity"]
            and native.get("workspace_history_origin") == receipt, "history_imported_state_mismatch")
