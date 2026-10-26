"""Shared native-currency accounting oracles, with no network or provider."""

import copy
import json
import os
import subprocess
import sys
import tempfile
import unittest
import uuid
from pathlib import Path
from unittest import mock

from scripts.terminal_bench.cohort_admission import CohortAdmission
from scripts.terminal_bench.service_snapshot import SnapshotError, digest
from scripts.terminal_bench.test_cold_dispatch import grant_for
from scripts.terminal_bench.test_recovery_bundle import history, invocation


def cohort_plan(histories, *, cohort=None, cap="6", holds=None):
    return dict(version=1, scope="benchmark-cohort-admission-v1", cohort_id=cohort or str(uuid.uuid4()),
                currency="CNY", max_estimated_cost=cap, expires_unix=10000,
                sessions=[dict(session_id=h["session_id"], max_estimated_cost="6", usage_history=h) for h in histories],
                external_reservations=holds or [])


def dispatch_body(plan, index=0):
    hist = copy.deepcopy(plan["sessions"][index]["usage_history"])
    return dict(cohort_id=plan["cohort_id"], usage_history=hist, paused_invocation=dict(record=dict(
        session_id=hist["session_id"], segment_id=hist["segment_ids"][-1])))


def execution_for(body, grant, *, unknown=False, exit_code=0):
    usage = invocation(unknown)
    usage["limits"] = dict(grant["limits"], reserve_cost_before_dispatch=True)
    usage["limits"].pop("timeout_sec")
    usage["checkpoint"] = dict(version=1, sequence=2, failed=False)
    usage["elapsed_ms"] = 1
    snapshot = copy.deepcopy(usage)
    snapshot.pop("checkpoint")
    return dict(exit_code=exit_code, terminal=dict(session_id=body["usage_history"]["session_id"],
                exit_code=exit_code, status="completed" if exit_code == 0 else "stopped", usage=usage),
                checkpoint=dict(snapshot=snapshot, sequence=2, segment_id=usage["call_ledger"]["segment_id"],
                                admission_closed=True, phase="sealed", accounting_complete=not unknown))


@unittest.skipUnless(os.name == "posix", "private cohort storage requires POSIX")
class CohortTests(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        self.root = Path(self.tmp.name).resolve() / "cohort"
        self.plan = cohort_plan([history(str(uuid.uuid4())), history(str(uuid.uuid4()))], cap="5.1")
        self.anchor = digest(self.plan)
        self.ledger = CohortAdmission.create(self.root, self.plan, self.anchor)
        self.body = dispatch_body(self.plan)
        self.grant = grant_for(self.body)

    def admit(self, body=None, grant=None):
        return self.ledger.admit(body or self.body, grant or self.grant, digest(grant or self.grant), now=1000)

    def reconcile(self, allocation, body, grant, execution=None):
        from scripts.terminal_bench.cold_dispatch import reconcile_execution
        result = reconcile_execution(body, "d" * 64, grant, digest(grant), execution or execution_for(body, grant))
        allocation.reconcile(result)
        return result

    def test_complete_histories_count_once_and_actual_cost_releases_unused_allocation(self):
        self.assertEqual(self.ledger.inspect()["known_estimated_amount"], "0.000120000000")
        with self.admit() as allocation:
            allocation.reserve(self.body, self.grant, digest(self.grant), now=1000)
            self.assertEqual(allocation.snapshot()["outstanding_amount"], "5.000000000000")
            self.assertEqual(allocation.snapshot()["exposure_amount"], "5.000120000000")
            self.reconcile(allocation, self.body, self.grant)
        actual = CohortAdmission(self.root, self.anchor).inspect()
        self.assertEqual(actual["known_estimated_amount"], "0.000150000000")
        self.assertEqual(actual["retained_amount"], "0.000000000000")
        self.assertEqual(len(actual["usage_histories"][self.body["usage_history"]["session_id"]]["segments"]), 3)
        other = dispatch_body(self.plan, 1)
        with self.admit(other, grant_for(other)):
            pass  # No reservation yet; admission checks alone consume nothing.
        self.assertEqual(self.ledger.inspect(), actual)

    def test_exhaustion_preserves_journal_and_each_session_history_without_dispatch(self):
        self.plan["max_estimated_cost"] = "0.1"
        ledger = CohortAdmission.create(self.root.parent / "small", self.plan, digest(self.plan))
        before = ledger.inspect()
        files = {p.name: p.read_bytes() for p in ledger.root.iterdir()}
        with self.assertRaisesRegex(SnapshotError, "cohort_budget_insufficient"):
            with ledger.admit(self.body, self.grant, digest(self.grant), now=1000):
                self.fail("exhausted cohort admitted")
        self.assertEqual(ledger.inspect(), before)
        self.assertEqual({p.name: p.read_bytes() for p in ledger.root.iterdir()}, files)

    def test_another_sessions_settled_spending_reduces_shared_remaining_margin(self):
        plan = copy.deepcopy(self.plan)
        plan["max_estimated_cost"] = "0.00016"
        ledger = CohortAdmission.create(self.root.parent / "cumulative", plan, digest(plan))
        self.grant["limits"]["max_estimated_cost"] = "0.00004"
        with ledger.admit(self.body, self.grant, digest(self.grant), now=1000) as allocation:
            allocation.reserve(self.body, self.grant, digest(self.grant), now=1000)
            self.reconcile(allocation, self.body, self.grant)
        other = dispatch_body(plan, 1)
        grant = grant_for(other)
        grant["limits"]["max_estimated_cost"] = "0.00004"
        before = ledger.inspect()
        with self.assertRaisesRegex(SnapshotError, "cohort_budget_insufficient"):
            with ledger.admit(other, grant, digest(grant), now=1000): pass
        self.assertEqual(ledger.inspect(), before)
        self.assertEqual(before["remaining_amount"], "0.000010000000")

    def test_replayed_source_and_closed_allocation_cannot_spend_again(self):
        with self.admit() as allocation:
            allocation.reserve(self.body, self.grant, digest(self.grant), now=1000)
            self.reconcile(allocation, self.body, self.grant)
            with self.assertRaisesRegex(SnapshotError, "not_open"):
                self.reconcile(allocation, self.body, self.grant)
        with self.assertRaisesRegex(SnapshotError, "closed"):
            allocation.reserve(self.body, self.grant, digest(self.grant), now=1000)
        with self.assertRaises(SnapshotError):
            with self.admit(): pass
        self.assertEqual(self.ledger.inspect()["known_estimated_amount"], "0.000150000000")

    def test_ambiguous_exit_keeps_full_allocation_and_blocks_another_session(self):
        with self.admit() as allocation:
            allocation.reserve(self.body, self.grant, digest(self.grant), now=1000)
        snapshot = self.ledger.inspect()
        self.assertEqual(snapshot["retained_amount"], "5.000000000000")
        self.assertTrue(snapshot["blocked"])
        other = dispatch_body(self.plan, 1)
        with self.assertRaisesRegex(SnapshotError, "cohort_accounting_unverified"):
            with self.admit(other, grant_for(other)): pass

    def test_partial_known_cost_plus_retained_remainder_never_double_counts(self):
        with self.admit() as allocation:
            allocation.reserve(self.body, self.grant, digest(self.grant), now=1000)
            actual = execution_for(self.body, self.grant)
            actual["checkpoint"]["accounting_complete"] = False
            result = self.reconcile(allocation, self.body, self.grant, actual)
            self.assertFalse(result["accounting_complete"])
        snapshot = self.ledger.inspect()
        self.assertEqual(snapshot["known_estimated_amount"], "0.000150000000")
        self.assertEqual(snapshot["retained_amount"], "4.999970000000")
        self.assertEqual(snapshot["exposure_amount"], "5.000120000000")
        self.assertTrue(snapshot["blocked"])
        self.assertEqual(snapshot["usage_histories"][self.body["usage_history"]["session_id"]], self.body["usage_history"])

    def test_incomplete_history_and_external_holds_are_conserved_and_blocking(self):
        for state in ("outstanding", "retained", "unknown_history"):
            plan = copy.deepcopy(self.plan)
            if state == "unknown_history":
                plan["sessions"][1]["usage_history"] = history(plan["sessions"][1]["session_id"], [invocation(True)])
            else:
                plan["external_reservations"] = [dict(reservation_id=str(uuid.uuid4()), amount="1", state=state)]
            ledger = CohortAdmission.create(self.root.parent / state, plan, digest(plan))
            snap = ledger.inspect()
            self.assertTrue(snap["blocked"])
            self.assertEqual(snap["exposure_amount"], "0.000096000000" if state == "unknown_history" else "1.000120000000")
            with self.assertRaisesRegex(SnapshotError, "cohort_accounting_unverified"):
                with ledger.admit(self.body, self.grant, digest(self.grant), now=1000): pass

    def test_global_segment_and_call_identity_cannot_be_imported_twice(self):
        for field in ("segment", "call"):
            plan = copy.deepcopy(self.plan)
            a, b = (s["usage_history"] for s in plan["sessions"])
            if field == "segment":
                b["segments"][0]["usage"] = copy.deepcopy(a["segments"][0]["usage"])
                b["segment_ids"][0] = a["segment_ids"][0]
            else:
                b["segments"][0]["usage"]["call_ledger"]["records"][0]["call_id"] = a["segments"][0]["usage"]["call_ledger"]["records"][0]["call_id"]
            with self.assertRaisesRegex(SnapshotError, "cohort_usage_identity_repeated"):
                CohortAdmission.create(self.root.parent / field, plan, digest(plan))

    def test_anchor_currency_expiry_scope_caps_and_history_reset_fail_closed(self):
        for fault in ("anchor", "currency", "expiry", "cohort", "session", "history", "ceiling"):
            body, grant = copy.deepcopy(self.body), copy.deepcopy(self.grant)
            now, anchor = 1000, digest(grant)
            if fault == "anchor": anchor = "e" * 64
            if fault == "currency": grant["currency"] = "USD"
            if fault == "expiry": now = 10000
            if fault == "cohort": body["cohort_id"] = str(uuid.uuid4())
            if fault == "session": body["usage_history"]["session_id"] = str(uuid.uuid4())
            if fault == "history": body["usage_history"] = history(body["usage_history"]["session_id"])
            if fault == "ceiling": grant["max_cumulative_estimated_cost"] = "7"
            if fault != "anchor": anchor = digest(grant)
            with self.subTest(fault=fault), self.assertRaises(SnapshotError):
                with self.ledger.admit(body, grant, anchor, now=now): pass

    def test_storage_failure_never_acknowledges_allocation_or_frees_ambiguous_exposure(self):
        with self.admit() as allocation:
            with mock.patch("scripts.terminal_bench.cohort_admission.os.fsync", side_effect=OSError("synthetic sync failure")):
                with self.assertRaises((OSError, SnapshotError)):
                    allocation.reserve(self.body, self.grant, digest(self.grant), now=1000)
        with self.assertRaises(SnapshotError):
            self.ledger.inspect()  # A torn event poisons the ledger; never ignored.

    def test_every_settlement_sync_failure_retains_or_blocks_the_full_allocation(self):
        original_sync = os.fsync
        for failed_sync in range(1, 6):
            ledger = CohortAdmission.create(self.root.parent / ("settle-%d" % failed_sync), self.plan, self.anchor)
            calls = []
            def sync(fd):
                calls.append(fd)
                if len(calls) == failed_sync: raise OSError("synthetic settlement sync failure")
                return original_sync(fd)
            with self.subTest(failed_sync=failed_sync), self.assertRaises((OSError, SnapshotError)):
                with ledger.admit(self.body, self.grant, digest(self.grant), now=1000) as allocation:
                    allocation.reserve(self.body, self.grant, digest(self.grant), now=1000)
                    with mock.patch("scripts.terminal_bench.cohort_admission.os.fsync", side_effect=sync):
                        self.reconcile(allocation, self.body, self.grant)
            reserved = json.loads((ledger.root / "event.00000001.json").read_text())["payload"]
            self.assertEqual(reserved["grant"]["limits"]["max_estimated_cost"], "5")
            try:
                snapshot = CohortAdmission(ledger.root, self.anchor).inspect()
            except (OSError, SnapshotError):
                pass  # A torn transaction remains blocked, never auto-repaired.
            else:
                self.assertTrue(snapshot["blocked"])
                self.assertEqual(snapshot["retained_amount"], "5.000000000000")
                self.assertEqual(snapshot["usage_histories"][self.body["usage_history"]["session_id"]], self.body["usage_history"])
            other = dispatch_body(self.plan, 1)
            other_grant = grant_for(other)
            with self.assertRaises((OSError, SnapshotError)):
                with ledger.admit(other, other_grant, digest(other_grant), now=1000): pass

    def test_missing_lock_changed_plan_gap_torn_event_and_duplicate_settle_are_blocked(self):
        for fault in ("lock", "plan", "gap", "tail", "torn", "duplicate"):
            root = self.root.parent / fault
            ledger = CohortAdmission.create(root, self.plan, self.anchor)
            with ledger.admit(self.body, self.grant, digest(self.grant), now=1000) as allocation:
                allocation.reserve(self.body, self.grant, digest(self.grant), now=1000)
                self.reconcile(allocation, self.body, self.grant)
            if fault == "lock": (root / "owner.lock").unlink()
            if fault == "plan":
                (root / "plan.json").chmod(0o600); (root / "plan.json").write_text("{}")
            if fault == "gap": (root / "event.00000001.json").unlink()
            if fault == "tail": (root / "event.00000002.json").unlink()
            if fault == "torn": (root / "event.00000003.json").write_text("{")
            if fault == "duplicate":
                event = json.loads((root / "event.00000002.json").read_text())
                event["sequence"], event["previous_sha256"] = 3, digest(event)
                target = root / "event.00000003.json"
                target.write_text(json.dumps(event)); target.chmod(0o400)
            with self.subTest(fault=fault), self.assertRaises((OSError, SnapshotError)):
                ledger.inspect()

    def test_cross_process_competitor_cannot_reserve_or_reset_and_crash_retains_funds(self):
        child = """import os,sys
from pathlib import Path
from scripts.terminal_bench.cohort_admission import CohortAdmission
from scripts.terminal_bench.service_snapshot import digest
from scripts.terminal_bench.test_cohort_admission import dispatch_body
from scripts.terminal_bench.test_cold_dispatch import grant_for
ledger=CohortAdmission(Path(sys.argv[1]),sys.argv[2])
body=dispatch_body(ledger.plan)
grant=grant_for(body)
with ledger.admit(body,grant,digest(grant),now=1000) as allocation:
 allocation.reserve(body,grant,digest(grant),now=1000)
 print('reserved',flush=True)
 sys.stdin.read(1)
 os._exit(0)
"""
        process = subprocess.Popen([sys.executable, "-c", child, str(self.root), self.anchor],
                                   stdin=subprocess.PIPE, stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True)
        try:
            self.assertEqual(process.stdout.readline().strip(), "reserved")
            other = dispatch_body(self.plan, 1)
            with self.assertRaisesRegex(SnapshotError, "cohort_dispatch_running"):
                with self.admit(other, grant_for(other)): pass
            process.communicate("x", timeout=5)
            self.assertEqual(process.returncode, 0)
        finally:
            if process.poll() is None: process.kill(); process.communicate(timeout=5)
        snapshot = CohortAdmission(self.root, self.anchor).inspect()
        self.assertEqual(snapshot["retained_amount"], "5.000000000000")
        self.assertTrue(snapshot["blocked"])
        with self.assertRaisesRegex(SnapshotError, "cohort_accounting_unverified"):
            with self.admit(other, grant_for(other)): pass
        with self.assertRaises(FileExistsError):
            CohortAdmission.create(self.root, self.plan, self.anchor)


if __name__ == "__main__":
    unittest.main()
