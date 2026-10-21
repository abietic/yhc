"""Budget attribution stays diagnostic and never changes token or money totals."""

import unittest

from scripts.terminal_bench.test_agent import budget_terminal
from scripts.terminal_bench.usage import sum_usage, validated_usage


class BudgetStopTests(unittest.TestCase):
    def test_each_known_cause_survives_projection_and_single_segment_aggregation(self):
        for cause in ("provider_calls", "total_tokens", "estimated_cost_threshold", "estimated_cost_reservation"):
            with self.subTest(cause=cause):
                value = budget_terminal()["result"]["usage"]
                value.update(stop_reason="run_budget_exceeded", budget_stop_cause=cause)
                projected = validated_usage(value)
                self.assertEqual(projected["budget_stop_cause"], cause)
                self.assertEqual(validated_usage(projected), projected)
                self.assertEqual(sum_usage([projected])["budget_stop_cause"], cause)
                self.assertEqual(projected["provider_calls"], 1)
                self.assertEqual(projected["total_tokens"], 14)

    def test_multisegment_total_does_not_inherit_an_earlier_pause_cause(self):
        paused = budget_terminal()["result"]["usage"]
        paused.update(stop_reason="run_budget_exceeded", budget_stop_cause="provider_calls")
        resumed = budget_terminal()["result"]["usage"]
        aggregate = sum_usage([paused, resumed])
        self.assertNotIn("budget_stop_cause", aggregate)
        self.assertNotIn("stop_reason", aggregate)
        self.assertEqual(aggregate["provider_calls"], 2)
        self.assertEqual(aggregate["total_tokens"], 28)
        self.assertEqual(validated_usage(paused)["budget_stop_cause"], "provider_calls")

    def test_unknown_malformed_and_legacy_details_do_not_erase_usage(self):
        original = budget_terminal()["result"]["usage"]
        for cause in (None, [], {}, 1, True, "", "future_cause"):
            with self.subTest(cause=cause):
                projected = validated_usage(dict(original, stop_reason="run_budget_exceeded", budget_stop_cause=cause))
                self.assertEqual(projected["total_tokens"], 14)
                self.assertTrue(projected["complete"])
                self.assertNotIn("budget_stop_cause", projected)
        self.assertNotIn("budget_stop_cause", validated_usage(original))
        unknown = validated_usage(dict(original, stop_reason="run_usage_unknown", budget_stop_cause="provider_calls"))
        self.assertNotIn("budget_stop_cause", unknown)


if __name__ == "__main__":
    unittest.main()
