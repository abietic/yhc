"""Independent consistency oracles for an externally anchored tool pause."""

import copy
import unittest
import uuid

from scripts.terminal_bench.paused_recovery import SCOPE, cold_tool_handoff, verify_paused_joint
from scripts.terminal_bench.service_snapshot import SnapshotError, digest
from scripts.terminal_bench.test_recovery_bundle import history, invocation, proof


def inputs(session_id=None):
    session = session_id if session_id is not None else str(uuid.uuid4())
    native = proof(session, "a" * 64)
    native.update(validation_complete=False, blocking_reasons=["native_turn_incomplete"],
                  tool_boundary=dict(version=1, kind="committed_tool_results", call_count=2,
                                     tool_names=["Read", "Write"], sha256="b" * 64, workspace_local=True))
    snapshot = dict(invocation(), denied_calls=1, elapsed_ms=10, stop_reason="run_budget_exceeded", budget_stop_cause="provider_calls")
    receipt = dict(snapshot, elapsed_ms=11, checkpoint=dict(version=1, sequence=5, failed=False))
    segment = receipt["call_ledger"]["segment_id"]
    checkpoint = dict(version=1, segment_id=segment, sequence=5, phase="sealed", admission_closed=True, snapshot=snapshot,
                      token_usage_complete=True, cost_complete=True, accounting_complete=True,
                      can_resume_agent=False, new_spending_authorized=False)
    terminal = dict(schema_version=1, session_id=session, status="failed", exit_code=1, terminal_reason="run_budget_exceeded",
                    error=dict(code="run_budget_exceeded"), usage=receipt)
    record = dict(version=1, scope=SCOPE, session_id=session, segment_id=segment,
                  checkpoint_path=".yhc/usage-checkpoints/owned-segment", terminal=terminal, native_proof=copy.deepcopy(native))
    return record, native, checkpoint, history(session, [invocation(), receipt])


class PausedProofTests(unittest.TestCase):
    def test_native_generated_session_name_preserves_exact_joint_binding(self):
        session = "session-1700000000000000001"
        record, native, checkpoint, cumulative = inputs(session)
        result = verify_paused_joint(record, digest(record), native, checkpoint, cumulative, "/workspace")
        self.assertEqual(result["session_id"], session)
        self.assertTrue(result["accounting_complete"])
        self.assertFalse(result["can_resume_agent"] or result["new_spending_authorized"])

    def test_native_session_name_never_relaxes_checkpoint_segment_identity(self):
        record, native, checkpoint, cumulative = inputs("session-1700000000000000001")
        record["segment_id"] = record["session_id"]
        with self.assertRaisesRegex(SnapshotError, "identity_mismatch"):
            verify_paused_joint(record, digest(record), native, checkpoint, cumulative, "/workspace")

    def test_unsafe_session_names_still_fail_operator_record_admission(self):
        for session in ("../session", "a/b", "-session", "a b", "aé", "a" * 129):
            record, native, checkpoint, cumulative = inputs(session)
            with self.subTest(session=session), self.assertRaisesRegex(SnapshotError, "identity_mismatch"):
                verify_paused_joint(record, digest(record), native, checkpoint, cumulative, "/workspace")

    def test_cold_tool_handoff_requires_complete_explicit_guard_capture(self):
        record, native, checkpoint, cumulative = inputs()
        with self.assertRaisesRegex(SnapshotError, "not_captured"):
            cold_tool_handoff(record, digest(record), native, checkpoint, cumulative, "/workspace")
        record["terminal"]["read_guards"] = dict(version=1, complete=True, snapshot=dict(version=1, cwd="/workspace", entries=[
            dict(path="file.txt", sha256="c" * 64, size=5, is_partial=False)]))
        actual = cold_tool_handoff(record, digest(record), native, checkpoint, cumulative, "/workspace")
        self.assertEqual(actual["read_guards"], record["terminal"]["read_guards"]["snapshot"])
        self.assertEqual(actual["checkpoint"], checkpoint)
        for fault in ("partial_missing", "escape", "duplicate", "unknown_capture", "unknown_history"):
            changed = copy.deepcopy(record)
            cap = changed["terminal"]["read_guards"]
            if fault == "partial_missing": cap["snapshot"]["entries"][0].pop("is_partial")
            if fault == "escape": cap["snapshot"]["entries"][0]["path"] = "../outside"
            if fault == "duplicate": cap["snapshot"]["entries"].append(copy.deepcopy(cap["snapshot"]["entries"][0]))
            if fault == "unknown_capture": cap["complete"] = False
            history_value = copy.deepcopy(cumulative)
            if fault == "unknown_history": history_value["segments"].pop()
            with self.subTest(fault=fault), self.assertRaises(SnapshotError):
                cold_tool_handoff(changed, digest(changed), native, checkpoint, history_value, "/workspace")

    def test_full_closed_checkpoint_and_exact_operator_receipt_form_no_spending_grant(self):
        record, native, checkpoint, cumulative = inputs()
        original = copy.deepcopy((record, native, checkpoint, cumulative))
        actual = verify_paused_joint(record, digest(record), native, checkpoint, cumulative, "/workspace")
        self.assertTrue(actual["accounting_complete"])
        self.assertFalse(actual["can_resume_agent"] or actual["new_spending_authorized"])
        self.assertEqual(actual["checkpoint_sha256"], digest(checkpoint))
        self.assertEqual(actual["checkpoint_sequence"], 5)
        self.assertEqual(original, (record, native, checkpoint, cumulative))

    def test_known_usage_never_substitutes_for_closed_accounting_or_exact_binding(self):
        for fault in ("admission_open", "settled_only", "accounting_false", "unknown_tokens", "cost_incomplete", "sequence",
                      "other_segment", "wrong_terminal_session", "success_terminal", "unknown_terminal", "checkpoint_failed",
                      "native_hash", "native_usage", "native_blocker", "missing_tools", "external_tool", "invalid_tool_count",
                      "wrong_tool_digest", "path_escape", "untracked_field", "bool_sequence", "terminal_cost", "history_gap",
                      "duplicate_call", "missing_denial", "elapsed_regression", "float_counter", "external_file"):
            with self.subTest(fault=fault):
                record, native, checkpoint, cumulative = inputs()
                if fault == "admission_open": checkpoint["admission_closed"] = False
                if fault == "settled_only": checkpoint["phase"] = "settled"
                if fault == "accounting_false": checkpoint["accounting_complete"] = False
                if fault == "unknown_tokens": checkpoint["token_usage_complete"] = False
                if fault == "cost_incomplete": checkpoint["cost_complete"] = False
                if fault == "sequence": checkpoint["sequence"] += 1
                if fault == "other_segment": checkpoint["segment_id"] = str(uuid.uuid4())
                if fault == "wrong_terminal_session": record["terminal"]["session_id"] = str(uuid.uuid4())
                if fault == "success_terminal": record["terminal"]["status"] = "completed"
                if fault == "unknown_terminal": record["terminal"]["error"]["code"] = "run_usage_unknown"
                if fault == "checkpoint_failed": record["terminal"]["usage"]["checkpoint"]["failed"] = True
                if fault == "native_hash": native["messages_sha256"] = "c" * 64
                if fault == "native_usage": native["native_usage"]["total_tokens"] += 1
                if fault == "native_blocker": native["blocking_reasons"].append("graph_interrupt")
                if fault == "missing_tools": native.pop("tool_boundary")
                if fault == "external_tool": native["tool_boundary"]["tool_names"] = ["Bash"]
                if fault == "invalid_tool_count": native["tool_boundary"]["call_count"] = True
                if fault == "wrong_tool_digest": native["tool_boundary"]["sha256"] = "d" * 64
                if fault == "path_escape": record["checkpoint_path"] = ".yhc/usage-checkpoints/../elsewhere"
                if fault == "untracked_field": checkpoint["new_unverified_state"] = True
                if fault == "bool_sequence": checkpoint["sequence"] = True
                if fault == "terminal_cost": record["terminal"]["usage"]["cost"]["known_estimated_amount"] = "0.000000000000"
                if fault == "history_gap": cumulative["segments"].pop()
                if fault == "duplicate_call":
                    cumulative["segments"][0]["usage"]["call_ledger"]["records"][0]["call_id"] = checkpoint["snapshot"]["call_ledger"]["records"][0]["call_id"]
                if fault == "missing_denial": checkpoint["snapshot"]["denied_calls"] = 0
                if fault == "elapsed_regression": record["terminal"]["usage"]["elapsed_ms"] = 9
                if fault == "float_counter": checkpoint["snapshot"]["known_calls"] = 1.0
                if fault == "external_file": native["tool_boundary"]["workspace_local"] = False
                with self.assertRaises(SnapshotError):
                    verify_paused_joint(record, digest(record), native, checkpoint, cumulative, "/workspace")

    def test_external_digest_is_required_even_when_all_internal_fields_agree(self):
        record, native, checkpoint, cumulative = inputs()
        anchor = digest(record)
        record["terminal"]["output"] = "changed host receipt"
        with self.assertRaisesRegex(SnapshotError, "anchor"):
            verify_paused_joint(record, anchor, native, checkpoint, cumulative, "/workspace")

    def test_only_unspecified_effort_is_canonicalized_without_dropping_route_facts(self):
        record, native, checkpoint, cumulative = inputs()
        record["terminal"]["usage"]["routes"] = [dict(model="deepseek-v4-flash", effort="", known_calls=1)]
        checkpoint["snapshot"]["routes"] = [dict(model="deepseek-v4-flash", effort="unknown", known_calls=1)]
        verify_paused_joint(record, digest(record), native, checkpoint, cumulative, "/workspace")
        for field, value in (("model", "another-model"), ("known_calls", 2), ("effort", "high")):
            changed = copy.deepcopy(checkpoint)
            changed["snapshot"]["routes"][0][field] = value
            with self.subTest(field=field), self.assertRaisesRegex(SnapshotError, "receipt_mismatch"):
                verify_paused_joint(record, digest(record), native, changed, cumulative, "/workspace")


if __name__ == "__main__":
    unittest.main()
