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

    def test_route_mismatch_or_missing_segment_model_is_not_priced_as_flash(self):
        flash = {**self.usage, "routes": [{"model": "flash"}]}
        for segments in ([flash, self.usage],
                         [{**self.usage, "routes": [{"model": "pro"}]}],
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

    def test_same_model_continuations_keep_price_and_old_usage_remains_compatible(self):
        flash = {**self.usage, "routes": [{"model": "flash"}]}
        for segments in ([flash, flash], [self.usage, self.usage]):
            with self.subTest(segments=segments):
                trial = build_report([{**self.row, "usage": sum_usage(segments)}],
                                     [self.card])["trials"][0]
                self.assertEqual(trial["cost_scenarios"]["scenario"], "0.196")

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
