import unittest
import json
import tempfile
from pathlib import Path

from scripts.terminal_bench.report_costs import build_report, hydrate_continuation
from scripts.terminal_bench.usage import sum_usage


class CostTests(unittest.TestCase):
    def setUp(self):
        self.usage = dict(provider_calls=1, known_calls=1, unknown_calls=0, in_flight=0,
                          untracked_calls=0, prompt_tokens=1000000, completion_tokens=100000,
                          total_tokens=1100000, cached_prompt_tokens=800000, reasoning_tokens=90000,
                          complete=True)
        self.card = dict(name="scenario", currency="USD", as_of="2026-10-03", source="test fixture",
                         models={"flash": dict(cached_input_per_million="0.01",
                                               uncached_input_per_million="0.2", output_per_million="0.5")})
        self.row = dict(trial_path="trial-one", manifest={"model_request": "flash"},
                        reward={"reward": 1}, usage=self.usage)

    def test_subsets_are_not_double_billed_and_successes_are_separate(self):
        failed = {**self.row, "trial_path": "trial-two", "reward": {"reward": 0}}
        result = build_report([self.row, failed], [self.card])
        self.assertEqual(result["trials"][0]["cost_scenarios"]["scenario"], "0.098")
        self.assertEqual(result["all_supplied_trials"]["currency_estimates"]["scenario"]["known_amount"], "0.196")
        self.assertEqual(result["successful_trials"]["trials"], 1)
        self.assertIsNone(result["all_supplied_trials"]["actual_billed_cost"])

    def test_official_glm_native_prices_and_unknown_calls(self):
        path = Path(__file__).parent / "rates" / "glm-2026-10-05-cny.json"
        card = json.loads(path.read_text())
        expected = {"glm-5.3": "6", "glm-5.3-flash": "0.624", "glm-5.3-flashx": "1.556"}
        for model, cost in expected.items():
            row = {**self.row, "manifest": {"model_request": model},
                   "usage": {**self.usage, "routes": [{"model": model}]}}
            result = build_report([row], [card], settlement_currency="CNY")
            estimate = result["all_supplied_trials"]["currency_estimates"][card["name"]]
            self.assertEqual(estimate["known_amount"], cost)
            self.assertEqual(estimate["currency"], "CNY")
            self.assertTrue(estimate["complete"])
        unknown = {**self.usage, "known_calls": 0, "unknown_calls": 1, "complete": False,
                   "prompt_tokens": 0, "completion_tokens": 0, "total_tokens": 0,
                   "cached_prompt_tokens": 0, "reasoning_tokens": 0}
        result = build_report([{**self.row, "usage": unknown}], [card])
        self.assertFalse(result["all_supplied_trials"]["currency_estimates"][card["name"]]["complete"])
        with self.assertRaises(ValueError):
            build_report([self.row], [{**card, "as_of": "yesterday"}])

    def test_missing_and_recovered_usage_remain_unknown_and_lower_bound(self):
        partial = {**self.row, "usage": None, "partial_usage_lower_bound": {
            "complete": False, "totals": self.usage}}
        missing = {**self.row, "trial_path": "missing", "usage": None}
        result = build_report([partial, missing], [self.card])
        costs = result["successful_trials"]
        self.assertEqual(costs["coverage"], {"complete": 0, "lower_bound": 1, "unknown": 1})
        self.assertFalse(costs["currency_estimates"]["scenario"]["complete"])
        self.assertIsNone(result["trials"][1]["cost_scenarios"]["scenario"])

    def test_invalid_prices_or_usage_and_duplicate_trials_fail_closed(self):
        with self.assertRaises(ValueError):
            build_report([self.row, self.row], [self.card])
        for value in (True, "NaN", "Infinity", -1, None):
            self.card["models"]["flash"]["output_per_million"] = value
            with self.subTest(value=value), self.assertRaises(ValueError):
                build_report([self.row], [self.card])
        self.usage["cached_prompt_tokens"] = 1000001
        with self.assertRaises(ValueError):
            build_report([self.row], [])

    def test_continuation_sums_each_segment_once_and_never_hides_missing_segment(self):
        total = sum_usage([self.usage, self.usage])
        self.assertEqual(total["total_tokens"], 2200000)
        self.assertEqual(total["provider_calls"], 2)
        self.assertTrue(total["complete"])
        self.assertIsNone(sum_usage([self.usage, None]))
        self.assertIsNone(sum_usage([]))

    def test_report_recovers_all_continuations_instead_of_pricing_only_last_stream(self):
        with tempfile.TemporaryDirectory() as directory:
            row = {**self.row, "trial_path": directory}
            history = [{"usage": self.usage}, {"usage": self.usage}]
            metadata = {"usage": sum_usage([self.usage, self.usage]),
                        "continuation": {"segments": 2, "history": history}}
            path = Path(directory) / "result.json"
            path.write_text(json.dumps({"agent_result": {"metadata": {"yhc": metadata}}}))
            result = build_report([hydrate_continuation(row)], [self.card])
            self.assertEqual(result["trials"][0]["cost_scenarios"]["scenario"], "0.196")
            history.pop()
            metadata.pop("usage")
            path.write_text(json.dumps({"agent_result": {"metadata": {"yhc": metadata}}}))
            result = build_report([hydrate_continuation(row)], [self.card])
            self.assertEqual(result["trials"][0]["cost_scenarios"]["scenario"], "0.098")
            self.assertEqual(result["trials"][0]["usage_coverage"], "lower_bound")

    def test_mixed_model_routes_do_not_get_one_model_price(self):
        self.usage["routes"] = [{"model": "flash"}, {"model": "pro"}]
        result = build_report([self.row], [self.card])
        self.assertIsNone(result["trials"][0]["cost_scenarios"]["scenario"])

    def test_continuation_retains_model_routes_in_harbor_history(self):
        first = {**self.usage, "routes": [{"model": "flash"}]}
        second = {**self.usage, "routes": [{"model": "pro"}]}
        with tempfile.TemporaryDirectory() as directory:
            metadata = {"usage": sum_usage([first, second]), "continuation": {
                "segments": 2, "history": [{"usage": first}, {"usage": second}]}}
            for old_aggregate in (False, True):
                with self.subTest(old_aggregate=old_aggregate):
                    if old_aggregate:
                        metadata["usage"].pop("routes")
                    (Path(directory) / "result.json").write_text(json.dumps(
                        {"agent_result": {"metadata": {"yhc": metadata}}}))
                    result = build_report([hydrate_continuation(
                        {**self.row, "trial_path": directory})], [self.card])
                    trial = result["trials"][0]
                    self.assertEqual(trial["tokens"]["total_tokens"], 2200000)
                    self.assertEqual(trial["cost_model_assumption"], "unpriced_mixed_routes")
                    self.assertIsNone(trial["cost_scenarios"]["scenario"])

    def test_unknown_or_missing_segment_model_is_not_priced_as_flash(self):
        flash = {**self.usage, "routes": [{"model": "flash"}]}
        for segments in ([flash, self.usage],
                         [{**self.usage, "routes": [{"model": ""}]}],
                         [{**self.usage, "routes": []}]):
            with self.subTest(segments=segments):
                row = {**self.row, "usage": sum_usage(segments)}
                trial = build_report([row], [self.card])["trials"][0]
                self.assertIsNone(trial["cost_scenarios"]["scenario"])
                if len(segments) == 1:
                    trial = build_report([{**self.row, "usage": segments[0]}],
                                         [self.card])["trials"][0]
                    self.assertIsNone(trial["cost_scenarios"]["scenario"])

    def test_named_profile_uses_explicit_experiment_model_assumption(self):
        usage = sum_usage([{**self.usage, "routes": [{"model": "bench"}]}])
        trial = build_report([{**self.row, "usage": usage}], [self.card])["trials"][0]
        self.assertEqual(trial["cost_scenarios"]["scenario"], "0.098")
        self.assertEqual(trial["cost_model_assumption"], "experiment_model_applies_to_recorded_calls")

    def test_incomplete_continuation_retains_known_mixed_models_without_pricing(self):
        first = {**self.usage, "routes": [{"model": "flash"}]}
        second = {**self.usage, "routes": [{"model": "pro"}]}
        with tempfile.TemporaryDirectory() as directory:
            metadata = {"continuation": {"segments": 3,
                "history": [{"usage": first}, {"usage": second}]}}
            (Path(directory) / "result.json").write_text(json.dumps(
                {"agent_result": {"metadata": {"yhc": metadata}}}))
            result = build_report([hydrate_continuation(
                {**self.row, "trial_path": directory})], [self.card])
            trial = result["trials"][0]
            self.assertEqual(trial["usage_coverage"], "lower_bound")
            self.assertEqual(trial["tokens"]["total_tokens"], 2200000)
            self.assertIsNone(trial["cost_scenarios"]["scenario"])

    def test_same_model_continuations_keep_price_and_old_usage_remains_compatible(self):
        flash = {**self.usage, "routes": [{"model": "flash"}]}
        for segments in ([flash, flash], [self.usage, self.usage]):
            with self.subTest(segments=segments):
                trial = build_report([{**self.row, "usage": sum_usage(segments)}],
                                     [self.card])["trials"][0]
                self.assertEqual(trial["cost_scenarios"]["scenario"], "0.196")

    def test_nested_recovered_routes_do_not_price_mixed_profiles(self):
        totals = sum_usage([{**self.usage, "routes": [{"model": "flash"}]},
                            {**self.usage, "routes": [{"model": "pro"}]}])
        row = {**self.row, "usage": None, "partial_usage_lower_bound": {
            "complete": False, "totals": totals}}
        trial = build_report([row], [self.card])["trials"][0]
        self.assertEqual(trial["tokens"]["total_tokens"], 2200000)
        self.assertEqual(trial["usage_coverage"], "lower_bound")
        self.assertIsNone(trial["cost_scenarios"]["scenario"])

    def test_invalid_profile_routes_fail_closed(self):
        for routes in ([], [{"model": ""}], [{"model": " flash "}]):
            with self.subTest(routes=routes):
                row = {**self.row, "usage": {**self.usage, "routes": routes}}
                self.assertIsNone(build_report([row], [self.card])["trials"][0]["cost_scenarios"]["scenario"])
        for routes in (None, [{"other": "flash"}]):
            with self.subTest(routes=routes), self.assertRaises(ValueError):
                build_report([{**self.row, "usage": {**self.usage, "routes": routes}}], [self.card])

    def test_native_prices_and_fx_conversion_are_separate(self):
        native = {**self.card, "name": "native", "currency": "CNY"}
        fx = dict(base="USD", quote="CNY", rate="7", as_of="2026-10-03", source="fixture")
        result = build_report([self.row], [self.card, native], settlement_currency="CNY",
                              display_currency="CNY", fx_snapshots=[fx])
        self.assertEqual(result["settlement_rate_cards"], ["native"])
        trial = result["trials"][0]
        self.assertEqual(trial["cost_scenarios"]["native"], "0.098")
        converted = trial["converted_estimates"]["scenario"]
        self.assertEqual(converted["amount"], "0.686")
        self.assertEqual(converted["original_amount"], "0.098")
        self.assertEqual(converted["fx_snapshot"], fx)
        self.assertEqual(trial["converted_estimates"]["native"]["amount"], "0.098")
        self.assertIsNone(trial["actual_billed_cost"])

    def test_fx_snapshot_changes_display_without_rewriting_original_amount(self):
        fx = dict(base="CNY", quote="USD", rate="0.14", as_of="2026-10-03", source="fixture")
        native = {**self.card, "currency": "CNY"}
        first = build_report([self.row], [native], display_currency="USD", fx_snapshots=[fx])
        later = build_report([self.row], [native], display_currency="USD",
                             fx_snapshots=[{**fx, "rate": "0.15", "as_of": "2026-10-04"}])
        self.assertEqual(first["trials"][0]["converted_estimates"]["scenario"]["amount"], "0.01372")
        self.assertEqual(later["trials"][0]["converted_estimates"]["scenario"]["amount"], "0.01470")
        self.assertEqual(first["trials"][0]["cost_scenarios"], later["trials"][0]["cost_scenarios"])
        self.assertEqual(first["fx_snapshots"], [fx])

    def test_inverse_missing_fx_and_partial_usage_preserve_coverage(self):
        fx = dict(base="CNY", quote="USD", rate="0.125", as_of="2026-10-03", source="fixture")
        partial = {**self.row, "usage": None, "partial_usage_lower_bound": {
            "complete": False, "totals": self.usage}}
        result = build_report([partial], [self.card], display_currency="CNY", fx_snapshots=[fx])
        converted = result["all_supplied_trials"]["converted_estimates"]["scenario"]
        self.assertEqual(converted["amount"], "0.784")
        self.assertTrue(converted["inverted"])
        self.assertFalse(converted["complete"])
        missing = build_report([self.row], [self.card], display_currency="CNY")
        self.assertEqual(missing["trials"][0]["converted_estimates"]["scenario"]["status"], "missing_fx")
        self.assertIsNone(missing["trials"][0]["converted_estimates"]["scenario"]["amount"])
        self.assertFalse(missing["all_supplied_trials"]["converted_estimates"]["scenario"]["complete"])
        unknown = build_report([{**self.row, "usage": None}], [self.card],
                               display_currency="CNY", fx_snapshots=[fx])
        self.assertIsNone(unknown["all_supplied_trials"]["converted_estimates"]["scenario"]["amount"])

    def test_invalid_or_ambiguous_currency_inputs_fail_closed(self):
        fx = dict(base="USD", quote="CNY", rate="7", as_of="2026-10-03", source="fixture")
        invalid = [{**fx, "rate": value} for value in (0, -1, True, None, "NaN", "Infinity")]
        invalid.extend([{**fx, "as_of": "yesterday"}, {**fx, "source": ""},
                        {**fx, "base": "usd"}, {**fx, "quote": "USD"}])
        for snapshot in invalid:
            with self.subTest(snapshot=snapshot), self.assertRaises(ValueError):
                build_report([self.row], [self.card], display_currency="CNY", fx_snapshots=[snapshot])
        for snapshots in ([fx, fx], [fx, {**fx, "base": "CNY", "quote": "USD"}]):
            with self.assertRaises(ValueError):
                build_report([self.row], [self.card], display_currency="CNY", fx_snapshots=snapshots)
        with self.assertRaises(ValueError):
            build_report([self.row], [self.card], fx_snapshots=[fx])
        with self.assertRaises(ValueError):
            build_report([self.row], [{**self.card, "currency": "RMB"}], settlement_currency="cny")


if __name__ == "__main__":
    unittest.main()


class UsageLedgerTests(unittest.TestCase):
    def usage(self, segment=None):
        from uuid import uuid4
        identity = str(uuid4())
        tokens = dict(prompt_tokens=10, completion_tokens=4, total_tokens=14,
                      cached_prompt_tokens=6, uncached_prompt_tokens=4, reasoning_tokens=2)
        record = dict(ordinal=1, call_id=identity, logical_round_id=identity,
                      logical_request_id="unknown", model_attempt_id="unknown", attempt_index=0,
                      retry_index=0, provider="agenticdeepseek", requested_model="deepseek-flash",
                      resolved_model="deepseek-v4-flash", source="agent", role="main", effort="low",
                      state="known", started_offset_ms=0, provider_duration_ms=30, tokens=tokens)
        return dict(provider_calls=1, known_calls=1, unknown_calls=0, in_flight=0,
                    untracked_calls=0, released_calls=0, prompt_tokens=10, completion_tokens=4,
                    total_tokens=14, cached_prompt_tokens=6, reasoning_tokens=2, complete=True,
                    call_ledger=dict(version=1, segment_id=segment or str(uuid4()),
                                     records=[record], dropped_records=0))

    def test_each_invocation_delta_once_and_snapshots_detached(self):
        from scripts.terminal_bench.usage import validated_usage
        first, second = self.usage(), self.usage()
        total = sum_usage([first, second])
        self.assertEqual(total["provider_calls"], 2)
        self.assertEqual(len(total["call_ledgers"]), 2)
        self.assertTrue(total["call_ledger_complete"])
        self.assertEqual(validated_usage(total), total)
        self.assertIsNone(sum_usage([first, first]))
        total["call_ledgers"][0]["records"][0]["tokens"]["total_tokens"] = 999
        self.assertEqual(first["call_ledger"]["records"][0]["tokens"]["total_tokens"], 14)
        legacy = dict(first)
        legacy.pop("call_ledger")
        mixed = sum_usage([first, legacy])
        self.assertFalse(mixed["call_ledger_complete"])
        self.assertEqual(validated_usage(mixed)["call_ledgers"], mixed["call_ledgers"])

    def test_unknown_and_released_are_not_known_zero(self):
        from scripts.terminal_bench.usage import validated_usage
        for state in ("unknown", "in_flight", "released"):
            value = self.usage()
            value.update(known_calls=0, prompt_tokens=0, completion_tokens=0, total_tokens=0,
                         cached_prompt_tokens=0, reasoning_tokens=0, complete=state == "released",
                         provider_calls=int(state != "released"), unknown_calls=int(state == "unknown"),
                         in_flight=int(state == "in_flight"), released_calls=int(state == "released"))
            value["call_ledger"]["records"][0].update(state=state, tokens=None)
            result = validated_usage(value)
            self.assertTrue(result["call_ledger_complete"])
            self.assertIsNone(result["call_ledger"]["records"][0]["tokens"])

    def test_invalid_history_preserves_aggregate_but_loses_ledger_coverage(self):
        from scripts.terminal_bench.usage import validated_usage
        for mutation in (lambda ledger: ledger.update(version=True),
                         lambda ledger: ledger["records"][0].update(ordinal=True),
                         lambda ledger: ledger["records"][0].update(source="private text"),
                         lambda ledger: ledger["records"][0]["tokens"].update(total_tokens=999),
                         lambda ledger: ledger.update(dropped_records=1)):
            value = self.usage()
            mutation(value["call_ledger"])
            result = validated_usage(value)
            self.assertEqual(result["total_tokens"], 14)
            self.assertFalse(result["call_ledger_complete"])
            self.assertNotIn("call_ledger", result)

    def test_projection_never_copies_payload_fields(self):
        from scripts.terminal_bench.usage import validated_usage
        value = self.usage()
        value["call_ledger"]["prompt"] = "private payload"
        value["call_ledger"]["records"][0]["thinking"] = "private payload"
        value["call_ledger"]["records"][0]["tokens"]["command"] = "private payload"
        self.assertNotIn("private payload", json.dumps(validated_usage(value)))

    def test_truncation_retains_accurate_totals_and_explicit_missing_history(self):
        from copy import deepcopy
        from uuid import uuid4
        from scripts.terminal_bench.usage import validated_usage
        value = self.usage()
        record = value["call_ledger"]["records"][0]
        value["call_ledger"]["records"] = []
        for i in range(1024):
            item = deepcopy(record)
            item.update(ordinal=i+1, call_id=str(uuid4()))
            value["call_ledger"]["records"].append(item)
        value["call_ledger"]["dropped_records"] = 1
        for key in ("provider_calls", "known_calls", "prompt_tokens", "completion_tokens", "total_tokens", "cached_prompt_tokens", "reasoning_tokens"):
            value[key] *= 1025
        result = validated_usage(value)
        self.assertTrue(result["complete"])
        self.assertFalse(result["call_ledger_complete"])
        self.assertEqual(len(result["call_ledger"]["records"]), 1024)
        self.assertEqual(result["total_tokens"], 14350)
        value["total_tokens"] = 14
        value["prompt_tokens"] = value["completion_tokens"] = 0
        value["cached_prompt_tokens"] = value["reasoning_tokens"] = 0
        self.assertNotIn("call_ledger", validated_usage(value))

    def test_report_hydrates_new_segment_ledgers_when_old_aggregate_has_no_history(self):
        first, second = self.usage(), self.usage()
        total = sum_usage([first, second])
        with tempfile.TemporaryDirectory() as directory:
            aggregate = {key: val for key, val in total.items() if not key.startswith("call_ledger")}
            metadata = {"usage": aggregate, "continuation": {"segments": 2,
                        "history": [{"usage": first}, {"usage": second}]}}
            (Path(directory) / "result.json").write_text(json.dumps({"agent_result": {"metadata": {"yhc": metadata}}}))
            recovered = hydrate_continuation({"trial_path": directory, "usage": aggregate})
            self.assertTrue(recovered["usage"]["call_ledger_complete"])
            self.assertEqual(len(recovered["usage"]["call_ledgers"]), 2)

    def test_coverage_source_remains_distinct_in_continuation_history(self):
        value=self.usage()
        value["call_ledger"]["records"][0].update(source="independent_verification_coverage",role="summary")
        total=sum_usage([value])
        self.assertTrue(total["call_ledger_complete"])
        self.assertEqual(total["call_ledgers"][0]["records"][0]["source"],"independent_verification_coverage")
