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


if __name__ == "__main__":
    unittest.main()
