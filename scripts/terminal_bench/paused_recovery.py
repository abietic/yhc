"""Operator-bound proof of a clean tool pause; never a continuation grant.

The terminal result and native observation must be recorded by the trusted
launcher outside the task workspace. Their external digest is the authority;
neither task-authored JSON nor a self-rewritten checkpoint supplies it.
"""

from pathlib import PurePosixPath

from scripts.terminal_bench.service_snapshot import digest, identity, require
from scripts.terminal_bench.workspace_recovery import native_proof, usage_history

SCOPE = "yhc-operator-tool-pause-v1"
FILE_TOOLS = {"Read", "Write", "Edit"}


def paused_native_proof(value, session_id, cwd):
    require(isinstance(value, dict) and value.get("validation_complete") is False
            and value.get("blocking_reasons") == ["native_turn_incomplete"], "paused_native_has_other_blockers")
    boundary = value.get("tool_boundary")
    require(isinstance(boundary, dict) and set(boundary) == {"version", "kind", "call_count", "tool_names", "sha256", "workspace_local"}
            and type(boundary["version"]) is int and boundary["version"] == 1
            and boundary["kind"] == "committed_tool_results"
            and boundary["workspace_local"] is True
            and type(boundary["call_count"]) is int and boundary["call_count"] > 0
            and isinstance(boundary["tool_names"], list) and bool(boundary["tool_names"])
            and all(isinstance(name, str) and name in FILE_TOOLS for name in boundary["tool_names"])
            and boundary["tool_names"] == sorted(set(boundary["tool_names"]))
            and len(boundary["tool_names"]) <= boundary["call_count"]
            and isinstance(boundary["sha256"], str) and len(boundary["sha256"]) == 64
            and all(c in "0123456789abcdef" for c in boundary["sha256"]), "paused_tool_state_unverified")
    # Only this one explicitly checked blocker is discharged by the joint
    # protocol. Ordinary native_proof remains completed-turn-only.
    semantic = native_proof(dict(value, validation_complete=True, blocking_reasons=[]), session_id, cwd)
    return dict(semantic, tool_boundary=boundary)


def verify_paused_joint(record, expected_digest, native, checkpoint, history, cwd):
    require(isinstance(record, dict) and digest(record) == expected_digest
            and set(record) == {"version", "scope", "session_id", "segment_id", "checkpoint_path", "terminal", "native_proof"}
            and type(record["version"]) is int and record["version"] == 1 and record["scope"] == SCOPE
            and identity(record["session_id"]) and identity(record["segment_id"]), "paused_record_anchor_or_identity_mismatch")
    session_id, segment_id = record["session_id"], record["segment_id"]
    path = record["checkpoint_path"]
    require(isinstance(path, str) and len(path) <= 512 and "\0" not in path, "paused_checkpoint_path_invalid")
    parsed = PurePosixPath(path)
    require(parsed.as_posix() == path and not parsed.is_absolute() and ".." not in parsed.parts
            and len(parsed.parts) >= 3 and parsed.parts[:2] == (".yhc", "usage-checkpoints"), "paused_checkpoint_path_invalid")
    original = paused_native_proof(record["native_proof"], session_id, cwd)
    require(digest(paused_native_proof(native, session_id, cwd)) == digest(original), "paused_native_binding_mismatch")
    accounting = usage_history(history, session_id)
    require(accounting["coverage_complete"] is True and history["segment_ids"][-1] == segment_id,
            "paused_cumulative_usage_incomplete_or_wrong_segment")
    terminal = record["terminal"]
    require(isinstance(terminal, dict) and type(terminal.get("schema_version")) is int and terminal["schema_version"] == 1
            and terminal.get("session_id") == session_id and terminal.get("status") == "failed"
            and type(terminal.get("exit_code")) is int and terminal["exit_code"] == 1
            and terminal.get("terminal_reason") == "run_budget_exceeded" and isinstance(terminal.get("error"), dict)
            and terminal["error"].get("code") == "run_budget_exceeded", "paused_terminal_not_clean_budget_stop")
    receipt = terminal.get("usage")
    require(isinstance(receipt, dict) and digest(receipt) == digest(history["segments"][-1]["usage"]), "paused_terminal_history_mismatch")
    require(isinstance(checkpoint, dict) and set(checkpoint) == {"version", "segment_id", "sequence", "phase", "admission_closed",
            "snapshot", "token_usage_complete", "cost_complete", "accounting_complete", "can_resume_agent", "new_spending_authorized"}
            and type(checkpoint["version"]) is int and checkpoint["version"] == 1 and checkpoint["segment_id"] == segment_id
            and type(checkpoint["sequence"]) is int and checkpoint["sequence"] > 0
            and checkpoint["phase"] == "sealed" and checkpoint["admission_closed"] is True
            and all(checkpoint[key] is True for key in ("token_usage_complete", "cost_complete", "accounting_complete"))
            and checkpoint["can_resume_agent"] is False and checkpoint["new_spending_authorized"] is False,
            "paused_usage_checkpoint_not_closed")
    status = receipt.get("checkpoint")
    require(status == dict(version=1, sequence=checkpoint["sequence"], failed=False)
            and type(status.get("version")) is int and type(status.get("sequence")) is int
            and status.get("failed") is False, "paused_checkpoint_sequence_mismatch")
    snapshot = checkpoint["snapshot"]
    require(isinstance(snapshot, dict) and snapshot.get("complete") is True
            and snapshot.get("stop_reason") == "run_budget_exceeded"
            and type(snapshot.get("denied_calls")) is int and snapshot["denied_calls"] > 0
            and snapshot.get("budget_stop_cause") in ("provider_calls", "total_tokens", "estimated_cost_threshold", "estimated_cost_reservation")
            and type(snapshot.get("known_calls")) is int and snapshot["known_calls"] > 0,
            "paused_usage_not_clean_budget_stop")
    # The terminal snapshot is taken just after checkpoint close. Only elapsed
    # wall time and the terminal persistence-status wrapper differ legitimately.
    # Persistence also canonicalizes an unspecified route effort to "unknown".
    # Admit only that observed representation change; all other fields bind
    # exactly, including every route's counters and every call/cost record.
    projected = dict(receipt)
    projected.pop("checkpoint")
    expected = dict(snapshot)
    if "routes" in projected:
        require(isinstance(projected["routes"], list) and all(isinstance(route, dict) for route in projected["routes"]),
                "paused_routes_invalid")
        projected["routes"] = [dict(route, effort="unknown") if route.get("effort") == "" else dict(route)
                               for route in projected["routes"]]
    for value in (projected, expected):
        require(type(value.get("elapsed_ms")) is int and value["elapsed_ms"] >= 0, "paused_elapsed_invalid")
    require(projected.pop("elapsed_ms") >= expected.pop("elapsed_ms") and digest(projected) == digest(expected),
            "paused_checkpoint_receipt_mismatch")
    return dict(version=1, scope="paused_components_joint_proof", session_id=session_id, segment_id=segment_id,
                pause_record_sha256=expected_digest, native_semantic_sha256=digest(original),
                checkpoint_sha256=digest(checkpoint), checkpoint_sequence=checkpoint["sequence"],
                tool_boundary=original["tool_boundary"], accounting_complete=True,
                tool_runtime_restored=False,
                can_resume_agent=False, new_spending_authorized=False)
