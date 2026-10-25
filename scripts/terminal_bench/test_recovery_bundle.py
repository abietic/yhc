"""Independent file round trips and fail-closed component binding oracles."""

import base64
import copy
import hashlib
import json
import os
import tempfile
import unittest
import uuid
from pathlib import Path

from scripts.terminal_bench.recovery_bundle import RecoveryBundle
from scripts.terminal_bench.service_snapshot import ServiceSnapshots, SnapshotError, digest
from scripts.terminal_bench.test_service_snapshot import FakeBackend, Time, contract
from scripts.terminal_bench.workspace_io import inventory, operate, PROTOCOL, validate_entries
from scripts.terminal_bench.workspace_recovery import native_proof, usage_history


def invocation(unknown=False):
    model = "legacy:deepseek-v4-flash"
    zero, amount = "0.000000000000", "0.000036000000"
    usage = dict(provider_calls=1, known_calls=int(not unknown), unknown_calls=int(unknown), in_flight=0,
                 untracked_calls=0, prompt_tokens=0 if unknown else 10, completion_tokens=0 if unknown else 4,
                 total_tokens=0 if unknown else 14, cached_prompt_tokens=0 if unknown else 6,
                 reasoning_tokens=0 if unknown else 2, complete=not unknown)
    cost = dict(version=1, segment_id=str(uuid.uuid4()), price_card=dict(version=1, currency="CNY",
                source="https://example.org/synthetic-prices", as_of="2026-10-25T00:00:00Z", window="upper_bound",
                routes=[dict(provider="agenticdeepseek", model=model, api_model="deepseek-v4-flash",
                             cached_input_per_million="1", uncached_input_per_million="2", output_per_million="4")]),
                known_estimated_amount=zero if unknown else "0.000030000000", actual_billed_amount=None,
                priced_calls=int(not unknown), unpriced_calls=int(unknown), complete=not unknown,
                limit_mode="pre_dispatch_reservation", reservation=dict(outstanding_amount=zero,
                retained_amount=amount if unknown else zero, peak_exposure_amount=amount, bound_violations=0))
    record = dict(ordinal=1, call_id=str(uuid.uuid4()), logical_round_id="unknown", logical_request_id="unknown",
                  model_attempt_id="unknown", attempt_index=0, retry_index=0, provider="agenticdeepseek",
                  requested_model=model, resolved_model="deepseek-v4-flash", source="sdk", role="main", effort="low",
                  state="unknown" if unknown else "known", started_offset_ms=0, provider_duration_ms=1,
                  tokens=None if unknown else {**{k: usage[k] for k in ("prompt_tokens", "completion_tokens", "total_tokens",
                  "cached_prompt_tokens", "reasoning_tokens")}, "uncached_prompt_tokens": 4},
                  cost=dict(state="unpriced" if unknown else "priced", price_index=0, estimated_amount=None,
                            reservation=dict(provider="agenticdeepseek", model="deepseek-v4-flash", input_tokens=10,
                            output_tokens=4, reserved_amount=amount, retained_amount=amount if unknown else zero)))
    if not unknown:
        record["cost"]["estimated_amount"] = "0.000030000000"
    return {**usage, "cost": cost, "call_ledger": dict(version=1, segment_id=cost["segment_id"], dropped_records=0, records=[record])}


def history(session_id, values=None):
    values = values if values is not None else [invocation(), invocation()]
    return dict(version=1, session_id=session_id, segment_ids=[u["call_ledger"]["segment_id"] for u in values],
                segments=[dict(ordinal=i, session_id=session_id, usage=u) for i, u in enumerate(values, 1)])


def proof(session_id, message_digest):
    return dict(version=1, scope="canonical_administration_restore", session_id=session_id, thread_id=session_id,
                cwd="/workspace", message_count=2, messages_sha256=message_digest, process_nonce=str(uuid.uuid4()),
                native_usage=dict(version=1, prompt_tokens=7, completion_tokens=3, total_tokens=10, responses_with_metadata=1,
                responses_without_metadata=0, last_prompt_tokens=7, last_completion_tokens=3, last_total_tokens=10,
                current_context_prompt_tokens=7, current_context_usage_known=True, last_response_had_usage_metadata=True),
                validation_complete=True, blocking_reasons=[], can_resume_agent=False)


class Workspace:
    def __init__(self, root, cohort, session_id, attempt=None, fault=None):
        self.root, self.cohort, self.session_id, self.attempt, self.fault = root, cohort, session_id, attempt, fault
        self.calls, self.stopped, self.source_removed = [], False, True
    def inventory(self):
        return dict(container_id=("b" if self.attempt else "a") * 64, image="fixture-image", daemon_id="fixture-daemon",
                    volume="target-volume" if self.attempt else "source-volume", cwd="/workspace", user="0:0", pid=1,
                    started_at="fixture", restart_count=0, cohort_id=self.cohort, attempt_id=self.attempt)
    def rpc(self, operation, **body):
        self.calls.append(operation)
        if operation == "native_probe":
            data = (self.root / ".yhc/transcripts" / (self.session_id + ".jsonl")).read_bytes()
            value = proof(self.session_id, hashlib.sha256(data).hexdigest())
            if self.fault == "native":
                value["messages_sha256"] = "f" * 64
            return value
        if operation == "write" and self.fault == "write":
            raise SnapshotError("fixture_partial_write")
        return operate(dict(protocol=PROTOCOL, root=str(self.root), operation=operation, **body))
    def assert_source_absent(self, source):
        if not self.source_removed:
            raise SnapshotError("workspace_source_container_still_exists")
    def quarantine(self, target):
        self.stopped = True


class HistoryTests(unittest.TestCase):
    def test_restored_cumulative_history_preserves_currency_and_cannot_grant_spending(self):
        value = history("session")
        actual = usage_history(json.loads(json.dumps(value)), "session")
        self.assertTrue(actual["coverage_complete"])
        self.assertEqual(actual["totals"]["total_tokens"], 28)
        self.assertEqual([s["cost"]["price_card"]["currency"] for s in actual["totals"]["cost_segments"]], ["CNY", "CNY"])
        self.assertFalse(actual["new_spending_authorized"])
        value["segments"].pop()
        with self.assertRaisesRegex(SnapshotError, "missing_segments"):
            usage_history(value, "session")

    def test_unknown_invocation_retains_reservation_and_blocks_complete_proof(self):
        value = history("session", [invocation(), invocation(True)])
        actual = usage_history(value, "session")
        self.assertFalse(actual["coverage_complete"])
        self.assertEqual(actual["totals"]["unknown_calls"], 1)
        self.assertEqual(actual["totals"]["cost_segments"][1]["cost"]["reservation"]["retained_amount"], "0.000036000000")
        self.assertEqual(value["segments"][1]["usage"]["unknown_calls"], 1)

    def test_cross_invocation_duplicate_calls_and_unverified_native_state_are_rejected(self):
        value = history("session")
        value["segments"][1]["usage"]["call_ledger"]["records"][0]["call_id"] = value["segments"][0]["usage"]["call_ledger"]["records"][0]["call_id"]
        with self.assertRaisesRegex(SnapshotError, "identity_repeated"):
            usage_history(value, "session")
        actual = proof("session", "a" * 64)
        actual["validation_complete"] = False
        actual["blocking_reasons"] = ["goal_state"]
        with self.assertRaisesRegex(SnapshotError, "incomplete"):
            native_proof(actual, "session", "/workspace")

    def test_unknown_context_or_last_response_usage_cannot_hide_behind_complete_boolean(self):
        for field in ("current_context_usage_known", "last_response_had_usage_metadata"):
            actual = proof("session", "a" * 64)
            actual["native_usage"][field] = False
            with self.subTest(field=field), self.assertRaisesRegex(SnapshotError, "usage_unverified"):
                native_proof(actual, "session", "/workspace")


@unittest.skipUnless(os.name == "posix" and hasattr(os, "listxattr"), "Linux file capsule; real Docker round trip covers macOS host")
class BundleTests(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        self.root = Path(self.tmp.name).resolve()
        self.control = self.root / "control"
        self.control.mkdir(mode=0o700)
        self.source_root, self.target_root = self.root / "source", self.root / "target"
        self.source_root.mkdir(); self.target_root.mkdir()
        self.session = str(uuid.uuid4())
        (self.source_root / ".yhc/transcripts").mkdir(parents=True)
        (self.source_root / ".yhc/transcripts" / (self.session + ".jsonl")).write_text("owned native fixture")
        (self.source_root / "script.sh").write_bytes(b"#!/bin/sh\nexit 0\n")
        (self.source_root / "script.sh").chmod(0o751)
        (self.source_root / "empty-dir").mkdir()
        (self.source_root / "binary.dat").write_bytes(b"\x00binary\xffdata")
        self.plan, self.timer = contract(), Time()
        self.cohort, self.attempt = str(uuid.uuid4()), str(uuid.uuid4())
        self.services_source = FakeBackend(self.plan, self.timer)
        for app in self.services_source.apps.values():
            app.mutate()
        self.service_path = self.root / "services"
        self.source_services = self.services(self.services_source)
        self.service_digest = digest(self.source_services.capture(self.service_path, self.cohort))
        self.source = Workspace(self.source_root, self.cohort, self.session)
        self.path = self.root / "bundle"
        self.hist = history(self.session)
    def services(self, backend):
        return ServiceSnapshots(self.plan, backend, control_dir=self.control, wall_ns=lambda: self.timer.wall,
                                monotonic_ns=lambda: self.timer.mono, sleep=self.timer.sleep)
    def capture(self):
        result = RecoveryBundle(self.source_services, self.source).capture(self.path, self.service_path, self.service_digest, self.session, self.hist)
        return result["bundle_sha256"]
    def target(self, fault=None):
        backend = FakeBackend(self.plan, self.timer, target=True)
        backend.source_removed = True
        workspace = Workspace(self.target_root, self.cohort, self.session, self.attempt, fault)
        return RecoveryBundle(self.services(backend), workspace), workspace, backend

    def test_real_files_modes_mtime_and_each_historical_invocation_restore_once(self):
        root_digest = self.capture()
        expected = inventory(self.source_root)
        bundle, target, services = self.target()
        result = bundle.restore(self.path, root_digest, self.service_path, self.attempt)
        self.assertEqual(inventory(self.target_root), expected)
        self.assertEqual((self.target_root / "binary.dat").read_bytes(), b"\x00binary\xffdata")
        self.assertEqual(result["status"], "restored_components_verified")
        self.assertEqual(result["accounting"]["totals"]["total_tokens"], 28)
        self.assertEqual(result["native_proof"]["native_usage"]["total_tokens"], 10)
        self.assertEqual(len(result["accounting"]["totals"]["call_ledgers"]), 2)
        self.assertFalse(result["can_resume_agent"])
        self.assertFalse(result["accounting"]["new_spending_authorized"])
        with self.assertRaisesRegex(SnapshotError, "already_claimed"):
            bundle.restore(self.path, root_digest, self.service_path, self.attempt)
        extra = copy.deepcopy(self.hist)
        more = invocation()
        extra["segment_ids"].append(more["call_ledger"]["segment_id"])
        extra["segments"].append(dict(ordinal=3, session_id=self.session, usage=more))
        self.assertEqual(usage_history(extra, self.session)["totals"]["total_tokens"], 42)

    def test_unknown_usage_and_retained_reservation_are_preserved_without_target_mutation(self):
        self.hist = history(self.session, [invocation(), invocation(unknown=True)])
        root_digest = self.capture()
        bundle, target, services = self.target()
        body, _, accounting = bundle.load(self.path, root_digest, self.service_path)
        self.assertEqual(body["usage_history"], self.hist)
        self.assertFalse(accounting["coverage_complete"])
        self.assertEqual(accounting["totals"]["unknown_calls"], 1)
        self.assertEqual(accounting["totals"]["cost_segments"][1]["cost"]["reservation"]["retained_amount"], "0.000036000000")
        with self.assertRaisesRegex(SnapshotError, "usage_incomplete"):
            bundle.restore(self.path, root_digest, self.service_path, self.attempt)
        self.assertEqual(target.calls, [])
        self.assertFalse(any(op == "import" for _, op in services.calls))

    def test_missing_reordered_repeated_or_cross_session_history_is_rejected(self):
        for fault in ("missing", "order", "call", "session"):
            value = copy.deepcopy(self.hist)
            if fault == "missing": value["segments"].pop()
            if fault == "order": value["segments"].reverse()
            if fault == "call":
                value["segments"][1]["usage"]["call_ledger"]["records"][0]["call_id"] = value["segments"][0]["usage"]["call_ledger"]["records"][0]["call_id"]
            if fault == "session": value["segments"][1]["session_id"] = "other-session"
            with self.subTest(fault=fault), self.assertRaises(SnapshotError):
                usage_history(value, self.session)

    def test_partial_file_import_or_native_mismatch_quarantines_both_targets(self):
        for fault in ("write", "native"):
            with self.subTest(fault=fault):
                # An interrupted restore consumes its capture claim. Independent
                # cases use separate captures, not a cleared/reused claim.
                if fault == "native":
                    self.path = self.root / "bundle-native"
                    self.hist = history(self.session)
                    self.target_root = self.root / "target-native"
                    self.target_root.mkdir()
                root_digest = self.capture()
                bundle, target, services = self.target(fault)
                with self.assertRaises(SnapshotError):
                    bundle.restore(self.path, root_digest, self.service_path, self.attempt)
                self.assertTrue(target.stopped)
                self.assertTrue(services.stopped)
                self.assertFalse(any(op == "activate" for _, op in services.calls))

    def test_links_and_path_escape_are_unsupported_not_followed(self):
        (self.source_root / "link").symlink_to("binary.dat")
        with self.assertRaises(ValueError): inventory(self.source_root)
        for path in ("../escape", "/absolute", "folder//file"):
            with self.assertRaises(ValueError):
                validate_entries([dict(path=".", kind="directory", mode=0o700, mtime_ns=0),
                                  dict(path=path, kind="directory", mode=0o700, mtime_ns=0)])


if __name__ == "__main__":
    unittest.main()
