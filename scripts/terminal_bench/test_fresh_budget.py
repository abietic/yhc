"""Offline financial invariants for new samples; never opens a provider."""

import copy
import itertools
import json
import os
import subprocess
import sys
import tempfile
import threading
import unittest
from concurrent.futures import ThreadPoolExecutor
from decimal import Decimal
from pathlib import Path
from unittest import mock

from scripts.terminal_bench.fresh_budget import BudgetStop, FreshBudget, assess_plan, digest
from scripts.terminal_bench.reservation_bound import request_bound


def price_card():
    return dict(version=1, currency="CNY", source="https://api-docs.deepseek.com/zh-cn/quick_start/pricing/",
                as_of="2026-10-31T00:00:00Z", window="upper_bound", routes=[dict(
                    provider="agenticdeepseek", model="bench", api_model="deepseek-flash",
                    cached_input_per_million="0.04", uncached_input_per_million="2", output_per_million="8")])


def body(protocol="responses", output=65536):
    if protocol == "responses":
        return dict(model="deepseek-flash", max_output_tokens=output, input="fixture", stream=True)
    return dict(model="deepseek-flash", max_tokens=output, messages=[dict(role="user", content="fixture")], stream=True)


def plan(protocols=("responses", "responses"), cap="6.6", sample_cap="3.3"):
    return dict(version=1, scope="benchmark-fresh-isolated-budget-v1", cohort_id="cohort-fixture", currency="CNY",
                max_estimated_cost=cap, max_output_tokens_per_call=65536, max_provider_calls=200,
                price_card=price_card(), samples=[dict(sample_id="sample-" + str(i), protocol=p,
                    max_estimated_cost=sample_cap) for i, p in enumerate(protocols)])


class BoundTests(unittest.TestCase):
    def test_rectangular_bound_uses_context_and_output_ceiling_not_observed_cache(self):
        proof = request_bound(body(), "responses", price_card(), 65536)
        self.assertEqual((proof["input_tokens"], proof["output_tokens"], proof["reserved_amount"]),
                         (1048576, 65536, "2.621440000000"))
        with_oversized_prompt = body()
        with_oversized_prompt["input"] = "中文" * 20000
        self.assertEqual(request_bound(with_oversized_prompt, "responses", price_card(), 65536)["reserved_amount"],
                         proof["reserved_amount"])
        self.assertNotEqual(request_bound(with_oversized_prompt, "responses", price_card(), 65536)["body_sha256"],
                            proof["body_sha256"])

    def test_messages_does_not_assume_requested_max_tokens_is_hard(self):
        for cap in (1, 65536):
            proof = request_bound(body("messages", cap), "messages", price_card(), 65536)
            self.assertEqual(proof["output_tokens"], 393216)
            self.assertEqual(proof["reserved_amount"], "5.242880000000")
            self.assertEqual(proof["output_bound_source"], "model_maximum_compatibility_fallback")

    def test_worst_input_rate_and_exact_decimal_are_used(self):
        card = price_card()
        card["routes"][0]["cached_input_per_million"] = "3"
        proof = request_bound(body(output=1), "responses", card, 65536)
        self.assertEqual(proof["reserved_amount"], "3.145736000000")

    def test_unbounded_or_unpriced_inputs_fail_before_reservation(self):
        cases = []
        for key, value in [("model", "claude-sonnet"), ("max_output_tokens", True),
                           ("max_output_tokens", 0), ("max_output_tokens", 65537)]:
            request = body(); request[key] = value; cases.append((request, "responses", price_card()))
        request = body(); request["tools"] = [{"type": "web_search"}]; cases.append((request, "responses", price_card()))
        request = body("messages"); request["tools"] = [{"type": "web_search_20250305", "name": "web_search"}]
        cases.append((request, "messages", price_card()))
        card = price_card(); card["window"] = "off_peak"; cases.append((body(), "responses", card))
        card = price_card(); card["routes"][0]["api_model"] = "unverified"; cases.append((body(), "responses", card))
        for request, protocol, card in cases:
            with self.subTest(request_keys=list(request), protocol=protocol), self.assertRaises(ValueError):
                request_bound(request, protocol, card, 65536)


@unittest.skipUnless(os.name == "posix", "private fresh budget storage requires POSIX")
class FreshBudgetTests(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        self.root = Path(self.tmp.name).resolve() / "budget"

    def create(self, value=None):
        value = value or plan()
        b = FreshBudget.create(self.root, value, digest(value))
        self.addCleanup(b.close)
        return b

    def test_underfunded_fair_batch_stops_before_first_claim_or_directory(self):
        value = plan(("responses", "messages", "messages"), cap="3.8", sample_cap="1.266666666666")
        result = assess_plan(value, digest(value))
        self.assertEqual(result["status"], "blocked")
        self.assertEqual(len(result["blocked_samples"]), 3)
        self.assertFalse(result["paid_dispatch_authorized"])
        self.assertEqual(result["real_provider_calls"], 0)
        with self.assertRaisesRegex(BudgetStop, "batch_request_reservation_unfunded"):
            self.create(value)
        with self.assertRaises(BudgetStop):
            FreshBudget(self.root, value, digest(value))
        self.assertFalse(self.root.exists())

    def test_overlapping_caps_reject_shared_first_come_first_served_plan(self):
        value = plan(cap="3.8", sample_cap="3.8")
        result = assess_plan(value, digest(value))
        self.assertIn("sample_caps_exceed_shared_cap", result["blocking_reasons"])
        with self.assertRaises(BudgetStop):
            self.create(value)
        self.assertFalse(self.root.exists())

    def test_sample_exhaustion_never_spends_another_sample_allocation_in_any_order(self):
        for order in itertools.permutations(("sample-0", "sample-1")):
            root = Path(self.tmp.name).resolve() / ("budget-" + order[0])
            value = plan()
            with FreshBudget.create(root, value, digest(value)) as b:
                for sample in order:
                    b.claim_sample(sample)
                    for count in (250000, 100000):
                        call = b.reserve(sample, body())
                        b.settle(call.call_id, input_tokens=count, cached_input_tokens=0, output_tokens=0)
                    with self.assertRaisesRegex(BudgetStop, "sample_budget_exhausted"):
                        b.reserve(sample, body())
                    b.finish_sample(sample, "budget_paused")
                self.assertEqual(b.snapshot()["known_cost"], "1.400000000000")
                self.assertEqual(b.snapshot()["outstanding"], "0.000000000000")
                self.assertEqual(b.snapshot()["retained"], "0.000000000000")

    def test_request_is_frozen_and_durable_before_any_upstream_side_effect(self):
        b = self.create(); b.claim_sample("sample-0"); request = body()
        call = b.reserve("sample-0", request); request["input"] = "mutated"
        self.assertEqual(json.loads(call.wire_body)["input"], "fixture")
        stored = json.loads((self.root / "ledger.json").read_text())
        self.assertEqual(stored["calls"][0]["id"], call.call_id)
        self.assertEqual(stored["calls"][0]["state"], "reserved")
        self.assertEqual(stored["calls"][0]["bound"]["body_sha256"], call.body_sha256)
        self.assertEqual(b.snapshot()["outstanding"], "2.621440000000")

    def test_unknown_and_over_bound_usage_retain_liability_and_stop_all_samples(self):
        for kind in ("unknown", "over_bound", "invalid_usage"):
            root = Path(self.tmp.name).resolve() / kind; value = plan()
            with FreshBudget.create(root, value, digest(value)) as b:
                b.claim_sample("sample-0"); call = b.reserve("sample-0", body())
                if kind == "unknown":
                    b.mark_unknown(call.call_id)
                else:
                    with self.assertRaises(BudgetStop):
                        b.settle(call.call_id, input_tokens=1048577 if kind == "over_bound" else True,
                                 cached_input_tokens=0, output_tokens=0)
                b.finish_sample("sample-0", "unknown_usage")
                with self.assertRaisesRegex(BudgetStop, "unknown_usage"):
                    b.claim_sample("sample-1")
                self.assertGreaterEqual(Decimal(b.snapshot()["retained"]), Decimal("2.62144"))
                self.assertFalse(b.snapshot()["can_resume_agent"])

    def test_output_above_requested_messages_cap_is_still_covered(self):
        b = self.create(plan(("messages",), cap="6", sample_cap="6")); b.claim_sample("sample-0")
        call = b.reserve("sample-0", body("messages", 1))
        b.settle(call.call_id, input_tokens=100, cached_input_tokens=0, output_tokens=65537)
        self.assertEqual(b.snapshot()["known_cost"], "0.524496000000")
        self.assertFalse(b.snapshot()["unknown_usage"])

    def test_concurrent_reservations_cannot_overdraw_one_sample(self):
        b = self.create(); b.claim_sample("sample-0"); barrier = threading.Barrier(2)
        def reserve():
            barrier.wait()
            try: return b.reserve("sample-0", body()).call_id
            except BudgetStop: return None
        with ThreadPoolExecutor(max_workers=2) as pool:
            results = list(pool.map(lambda _: reserve(), range(2)))
        self.assertEqual(sum(v is not None for v in results), 1)
        self.assertEqual(b.snapshot()["outstanding"], "2.621440000000")

    def test_interrupted_writer_never_reopens_or_releases_pending_liability(self):
        value = plan(); plan_path = Path(self.tmp.name) / "plan.json"; plan_path.write_text(json.dumps(value))
        code = """import json,os,sys
from pathlib import Path
from scripts.terminal_bench.fresh_budget import FreshBudget,digest
p=json.loads(Path(sys.argv[2]).read_text());b=FreshBudget.create(Path(sys.argv[1]),p,digest(p))
b.claim_sample('sample-0');b.reserve('sample-0',dict(model='deepseek-flash',input='fixture',max_output_tokens=65536))
os._exit(19)
"""
        p = subprocess.run([sys.executable, "-B", "-c", code, str(self.root), str(plan_path)], check=False)
        self.assertEqual(p.returncode, 19)
        original = (self.root / "ledger.json").read_bytes()
        with self.assertRaises(FileExistsError): self.create(value)
        self.assertEqual((self.root / "ledger.json").read_bytes(), original)
        self.assertEqual(json.loads(original)["calls"][0]["state"], "reserved")

    def test_disk_fault_blocks_next_dispatch_and_keeps_previous_record(self):
        b = self.create(); b.claim_sample("sample-0")
        with mock.patch("scripts.terminal_bench.fresh_budget.os.fsync", side_effect=OSError("fixture")):
            with self.assertRaises(OSError): b.reserve("sample-0", body())
        with self.assertRaisesRegex(BudgetStop, "accounting_unavailable"):
            b.reserve("sample-0", body())
        self.assertGreaterEqual(Decimal(b.snapshot()["outstanding"]), Decimal("2.62144"))

    def test_settlement_publication_fault_never_releases_durable_reservation(self):
        for fault in ("file_sync", "replace", "directory_sync"):
            with self.subTest(fault=fault):
                root = Path(self.tmp.name).resolve() / fault; value = plan()
                with FreshBudget.create(root, value, digest(value)) as b:
                    b.claim_sample("sample-0"); call = b.reserve("sample-0", body())
                    sync = os.fsync; count = 0
                    def fail_sync(fd):
                        nonlocal count
                        count += 1
                        if count == (1 if fault == "file_sync" else 2):
                            raise OSError("fixture")
                        return sync(fd)
                    patch = (mock.patch("scripts.terminal_bench.fresh_budget.os.replace", side_effect=OSError("fixture"))
                             if fault == "replace" else mock.patch("scripts.terminal_bench.fresh_budget.os.fsync", side_effect=fail_sync))
                    with patch, self.assertRaises(OSError):
                        b.settle(call.call_id, input_tokens=0, cached_input_tokens=0, output_tokens=0)
                    snapshot = b.snapshot()
                    self.assertTrue(snapshot["accounting_error"])
                    self.assertGreaterEqual(Decimal(snapshot["exposure"]), Decimal("2.62144"))
                    self.assertGreaterEqual(Decimal(snapshot["retained"]), Decimal("2.62144"))
                    with self.assertRaises(BudgetStop): b.reserve("sample-0", body())

    def test_plan_drift_repeated_claim_settlement_and_call_limit_are_blocked(self):
        value = plan(); value["max_provider_calls"] = 1; b = self.create(value)
        value["samples"][0]["max_estimated_cost"] = "100"
        b.claim_sample("sample-0"); call = b.reserve("sample-0", body())
        b.settle(call.call_id, input_tokens=1, cached_input_tokens=0, output_tokens=1)
        with self.assertRaisesRegex(BudgetStop, "duplicate_or_unknown_settlement"):
            b.settle(call.call_id, input_tokens=1, cached_input_tokens=0, output_tokens=1)
        with self.assertRaisesRegex(BudgetStop, "sample_call_limit"):
            b.reserve("sample-0", body())
        b.finish_sample("sample-0", "completed")
        with self.assertRaisesRegex(BudgetStop, "sample_already_started"):
            b.claim_sample("sample-0")
        with self.assertRaises(BudgetStop):
            assess_plan(value, b.plan_sha256)

    def test_tampered_ledger_lock_or_directory_blocks_without_repairing_them(self):
        for kind in ("ledger", "owner", "directory"):
            with self.subTest(kind=kind):
                value = plan(); root = Path(self.tmp.name).resolve() / kind
                with FreshBudget.create(root, value, digest(value)) as b:
                    b.claim_sample("sample-0")
                    if kind == "ledger":
                        p = root / "ledger.json"; p.write_text("{}")
                    elif kind == "owner":
                        p = root / "owner.lock"; p.unlink(); p.write_text("replacement"); p.chmod(0o600)
                    else:
                        moved = root.with_name(kind + "-old"); root.rename(moved); root.mkdir(mode=0o700); p = root
                    with self.assertRaises(BudgetStop): b.reserve("sample-0", body())
                    self.assertEqual(len(b._state["calls"]), 0)
                    if kind == "ledger": self.assertEqual(p.read_text(), "{}")

    def test_large_over_bound_receipt_keeps_more_than_original_reservation(self):
        value = plan(("responses",), cap="999999999999999999", sample_cap="999999999999999999")
        value["price_card"]["routes"][0].update(cached_input_per_million="0", uncached_input_per_million="0",
                                               output_per_million="900000000000000000")
        b = self.create(value); b.claim_sample("sample-0"); call = b.reserve("sample-0", body())
        with self.assertRaises(BudgetStop):
            b.settle(call.call_id, input_tokens=0, cached_input_tokens=0, output_tokens=2**64-1)
        self.assertTrue(b.snapshot()["unknown_usage"])
        self.assertGreater(Decimal(b.snapshot()["retained"]), Decimal(value["max_estimated_cost"]))

    def test_assessment_cli_is_offline_and_cannot_dispatch_or_resume(self):
        value = plan(); path = Path(self.tmp.name) / "plan.json"; path.write_text(json.dumps(value))
        code = """import os,socket,sys
from unittest.mock import patch
from scripts.terminal_bench.fresh_budget import main
with patch.dict(os.environ,{},clear=True),patch.object(socket,'socket',side_effect=AssertionError('network forbidden')):
 sys.argv=['fresh_budget','--plan',sys.argv[1],'--plan-sha256',sys.argv[2]]
 raise SystemExit(main())
"""
        p = subprocess.run([sys.executable, "-B", "-c", code, str(path), digest(value)], capture_output=True, text=True)
        self.assertEqual(p.returncode, 0, p.stderr)
        self.assertFalse(json.loads(p.stdout)["paid_dispatch_authorized"])
        self.assertFalse(self.root.exists())


if __name__ == "__main__":
    unittest.main()
