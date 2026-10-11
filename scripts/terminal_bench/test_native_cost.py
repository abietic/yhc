"""Native cost contracts using synthetic rates; no paid API or Docker."""

import copy
import json
import unittest
from pathlib import Path
from types import SimpleNamespace
from uuid import uuid4
from unittest.mock import patch, AsyncMock

from harbor.models.agent.context import AgentContext
from harbor.agents.installed.base import NonZeroAgentExitCodeError

from scripts.terminal_bench.native_cost import validate_price_card, native_totals
from scripts.terminal_bench.usage import sum_usage, validated_usage
from scripts.terminal_bench.test_agent import AgentFixture, budget_terminal
from scripts.terminal_bench.report_costs import convert_estimate, aggregate_native_estimates


def price_card(currency="CNY", window="fixed", api_model=None):
    route = dict(provider="agenticdeepseek", model="legacy:deepseek-v4-flash",
                 cached_input_per_million="1", uncached_input_per_million="2", output_per_million="4")
    if api_model is not None:
        route["api_model"] = api_model
    return dict(version=1, currency=currency, source="https://example.org/pricing",
                as_of="2026-10-11T00:00:00Z", window=window, routes=[route])


def priced_usage(currency="CNY", strict=False):
    return dict(provider_calls=1, known_calls=1, unknown_calls=0, in_flight=0, untracked_calls=0,
                prompt_tokens=10, completion_tokens=4, total_tokens=14, cached_prompt_tokens=6,
                reasoning_tokens=2, complete=True, cost=dict(version=1, segment_id=str(uuid4()),
                    price_card=price_card(currency, window="upper_bound" if strict else "fixed", api_model="deepseek-v4-flash" if strict else None), known_estimated_amount="0.000030000000",
                    actual_billed_amount=None, priced_calls=1, unpriced_calls=0, complete=True,
                    limit_mode="pre_dispatch_reservation" if strict else "post_response_threshold",
                    **({"reservation": dict(outstanding_amount="0.000000000000", retained_amount="0.000000000000",
                                             peak_exposure_amount="0.000036000000", bound_violations=0)} if strict else {})))


class NativeCostTests(unittest.TestCase):
    def test_pre_dispatch_receipt_rejects_unsettled_or_over_bound_reservation(self):
        usage = priced_usage(strict=True)
        record = dict(ordinal=1, call_id=str(uuid4()), logical_round_id="unknown", logical_request_id="unknown", model_attempt_id="unknown",
                      attempt_index=0, retry_index=0, provider="agenticdeepseek", requested_model="legacy:deepseek-v4-flash", resolved_model="unknown",
                      source="sdk", role="main", effort="low", state="known", started_offset_ms=0, provider_duration_ms=1,
                      tokens={"prompt_tokens": 10, "completion_tokens": 4, "total_tokens": 14, "cached_prompt_tokens": 6,
                              "uncached_prompt_tokens": 4, "reasoning_tokens": 2},
                      cost=dict(state="priced", price_index=0, estimated_amount="0.000030000000",
                                reservation=dict(provider="agenticdeepseek", model="deepseek-v4-flash",
                                                 input_tokens=10, output_tokens=4, reserved_amount="0.000036000000",
                                                 retained_amount="0.000000000000")))
        usage["call_ledger"] = dict(version=1, segment_id=usage["cost"]["segment_id"], dropped_records=0, records=[record])
        self.assertTrue(validated_usage(usage)["cost_complete"])
        usage["cost"]["reservation"]["bound_violations"] = 1
        self.assertFalse(validated_usage(usage)["cost_complete"])
        usage["cost"]["reservation"]["bound_violations"] = 0
        record["cost"]["reservation"]["reserved_amount"] = "0.000020000000"
        self.assertFalse(validated_usage(usage)["cost_complete"])
    def test_call_prices_reconcile_exactly_and_malformed_cost_does_not_erase_tokens(self):
        usage = priced_usage()
        record = dict(ordinal=1, call_id=str(uuid4()), logical_round_id="unknown", logical_request_id="unknown", model_attempt_id="unknown",
                      attempt_index=0, retry_index=0, provider="agenticdeepseek", requested_model="legacy:deepseek-v4-flash", resolved_model="unknown",
                      source="sdk", role="main", effort="low", state="known", started_offset_ms=0, provider_duration_ms=1,
                      tokens={**{key: usage[key] for key in ("prompt_tokens", "completion_tokens", "total_tokens", "cached_prompt_tokens", "reasoning_tokens")}, "uncached_prompt_tokens": 4},
                      cost=dict(state="priced", price_index=0, estimated_amount="0.000030000000"))
        usage["call_ledger"] = dict(version=1, segment_id=usage["cost"]["segment_id"], dropped_records=0, records=[record])
        self.assertTrue(validated_usage(usage)["cost_complete"])
        record["cost"]["estimated_amount"] = "0.000020000000"
        projected = validated_usage(usage)
        self.assertTrue(projected["complete"])
        self.assertFalse(projected["cost_complete"])
        self.assertNotIn("cost", projected)

    def test_exact_incremental_native_totals_and_duplicate_rejection(self):
        a, b = priced_usage(), priced_usage()
        usage = sum_usage([a, b])
        self.assertTrue(usage["cost_complete"])
        self.assertEqual(native_totals(usage)["CNY"]["known_estimated_amount"], "0.000060000000")
        self.assertEqual(validated_usage(usage), usage)
        self.assertIsNone(sum_usage([a, a]))
        self.assertEqual(len(usage["cost_segments"]), 2)

    def test_currency_groups_and_explicit_fx_display(self):
        usage = sum_usage([priced_usage(), priced_usage("USD")])
        totals = native_totals(usage)
        self.assertEqual(set(totals), {"CNY", "USD"})
        fx = dict(base="CNY", quote="USD", rate="0.1", source="synthetic", as_of="2026-10-11T00:00:00Z")
        converted = convert_estimate(totals["CNY"]["known_estimated_amount"], "CNY", "USD", [fx])
        self.assertEqual(converted["amount"], "0.0000030000000")
        self.assertEqual(totals["CNY"]["known_estimated_amount"], "0.000030000000")

    def test_missing_or_invalid_cost_keeps_tokens_but_never_claims_complete_cost(self):
        a = priced_usage()
        ambiguous = {**a, "cost_segments": []}
        self.assertFalse(validated_usage(ambiguous)["cost_complete"])
        self.assertNotIn("cost", validated_usage(ambiguous))
        b = {k: v for k, v in priced_usage().items() if k != "cost"}
        usage = sum_usage([a, b])
        self.assertTrue(usage["complete"])
        self.assertFalse(usage["cost_complete"])
        self.assertFalse(native_totals(usage)["CNY"]["complete"])
        for change in ({"known_estimated_amount": "NaN"}, {"priced_calls": 2},
                       {"actual_billed_amount": "1"}, {"complete": False}, {"private_prompt": "secret"}):
            bad = copy.deepcopy(a)
            bad["cost"].update(change)
            observed = validated_usage(bad)
            self.assertEqual(observed["total_tokens"], 14)
            self.assertFalse(observed["cost_complete"])
            self.assertNotIn("cost", observed)

    def test_successful_cohort_cost_and_old_missing_cost_are_distinct(self):
        row = {"invocation_native_estimates": native_totals(validated_usage(priced_usage()))}
        totals = aggregate_native_estimates([row, row])
        self.assertEqual(totals["CNY"]["known_estimated_amount"], "0.000060000000")
        self.assertTrue(totals["CNY"]["complete"])
        self.assertFalse(aggregate_native_estimates([row, {"invocation_native_estimates": {}}])["CNY"]["complete"])

    def test_public_card_rejects_secrets_and_invalid_decimals(self):
        for change in ({"source": "https://user:password@example.org"}, {"source": "https://example.org/?key=secret"},
                       {"api_key": "secret"}, {"currency": "cny"}, {"as_of": "yesterday"}):
            card = price_card()
            card.update(change)
            with self.subTest(change=change), self.assertRaises(ValueError):
                validate_price_card(card)
        for rate in (True, 1.2, "NaN", "-1", "1e3", "0.0000001"):
            card = price_card()
            card["routes"][0]["output_per_million"] = rate
            with self.subTest(rate=rate), self.assertRaises(ValueError):
                validate_price_card(card)


class NativeAgentTests(AgentFixture, unittest.TestCase):
    def test_strict_reservation_rejects_unsafe_configuration_and_emits_bounds(self):
        path = self.root / "upper-prices.json"
        path.write_text(json.dumps(price_card(window="upper_bound", api_model="deepseek-v4-flash")))
        with patch.dict("os.environ", {"YHC_BENCH_API_KEY": "test-key"}):
            with self.assertRaises(ValueError):
                self.agent(price_card_path=str(path), max_estimated_cost="1", reserve_cost_before_dispatch=True)
            with self.assertRaises(ValueError):
                self.agent(price_card_path=str(path), max_estimated_cost="1", max_total_estimated_cost="2")
            agent = self.agent(price_card_path=str(path), max_estimated_cost="1", reserve_cost_before_dispatch=True,
                               max_output_tokens_per_call=64, max_total_estimated_cost="2")
            command = agent.execution_command()
            self.assertIn("--reserve-cost-before-dispatch", command)
            self.assertIn("--max-output-tokens-per-call 64", command)
    def test_optional_amount_limit_and_finite_continuation_flags(self):
        path = self.root / "prices.json"
        path.write_text(json.dumps(price_card()))
        with patch.dict("os.environ", {"YHC_BENCH_API_KEY": "test-key"}):
            self.assertNotIn("--max-estimated-cost", self.agent().execution_command())
            agent = self.agent(price_card_path=str(path), max_estimated_cost="1.5",
                               continuation_budgets=[{"max_estimated_cost": "2.5"}])
            self.assertIn("--max-estimated-cost 1.5", agent.execution_command())
            self.assertIn("--price-card", agent.execution_command())
            agent._segment_session = "saved-session"
            agent._segment_limits = agent.continuation_budgets[0]
            command = agent.execution_command()
            self.assertIn("--resume saved-session", command)
            self.assertIn("--max-estimated-cost 2.5", command)
            self.assertNotIn("--max-estimated-cost 1.5", command)
            for amount in ("NaN", "-1", "1e2", 1.0, True):
                with self.subTest(amount=amount), self.assertRaises(ValueError):
                    self.agent(price_card_path=str(path), max_estimated_cost=amount)
            with self.assertRaises(ValueError):
                self.agent(max_estimated_cost="1")
            with self.assertRaises(ValueError):
                self.agent(price_card_path=str(path), max_estimated_cost="1", continuation_budgets=[{"max_estimated_cost": "0"}])


class NativeRunTests(AgentFixture, unittest.IsolatedAsyncioTestCase):
    async def test_strict_completed_receipt_over_dispatched_limit_is_rejected(self):
        path = self.root / "upper-prices.json"
        path.write_text(json.dumps(price_card(window="upper_bound", api_model="deepseek-v4-flash")))
        keys = patch.dict("os.environ", {"YHC_BENCH_API_KEY": "test-key"})
        keys.start()
        self.addCleanup(keys.stop)
        agent = self.agent(price_card_path=str(path), max_estimated_cost="0.000020", reserve_cost_before_dispatch=True,
                           max_output_tokens_per_call=4)
        agent.exec_as_root, agent.exec_as_agent = AsyncMock(), AsyncMock()
        environment = SimpleNamespace(upload_file=AsyncMock(), exec=AsyncMock(), download_file=AsyncMock())
        record = budget_terminal()
        record["result"].update(status="completed", exit_code=0, terminal_reason="completed", error=None, usage=priced_usage(strict=True))
        environment.exec.return_value = SimpleNamespace(return_code=0)
        async def download(_source, target):
            Path(target).write_text(json.dumps(record) + "\n")
        environment.download_file.side_effect = download
        with self.assertRaises(ValueError):
            await agent.run("original instruction", environment, AgentContext())

    async def test_strict_unsettled_reservation_does_not_spend_a_continuation_grant(self):
        path = self.root / "upper-prices.json"
        path.write_text(json.dumps(price_card(window="upper_bound", api_model="deepseek-v4-flash")))
        keys = patch.dict("os.environ", {"YHC_BENCH_API_KEY": "test-key"})
        keys.start()
        self.addCleanup(keys.stop)
        agent = self.agent(price_card_path=str(path), max_estimated_cost="0.000040", reserve_cost_before_dispatch=True,
                           max_output_tokens_per_call=4, continuation_budgets=[{"max_estimated_cost": "0.000040"}])
        agent.exec_as_root, agent.exec_as_agent = AsyncMock(), AsyncMock()
        environment = SimpleNamespace(upload_file=AsyncMock(), exec=AsyncMock(), download_file=AsyncMock())
        record = budget_terminal()
        usage = priced_usage(strict=True)
        usage["cost"].update(priced_calls=0, unpriced_calls=1, complete=False, known_estimated_amount="0.000000000000")
        usage["cost"]["reservation"].update(retained_amount="0.000036000000", peak_exposure_amount="0.000036000000")
        record["result"]["usage"] = usage
        commands = []
        async def execute(**kwargs):
            if "env" in kwargs:
                commands.append(kwargs["command"])
            return SimpleNamespace(return_code=1)
        async def download(_source, target):
            Path(target).write_text(json.dumps(record) + "\n")
        environment.exec.side_effect, environment.download_file.side_effect = execute, download
        with self.assertRaises(ValueError):
            await agent.run("original instruction", environment, AgentContext())
        self.assertEqual(len(commands), 1)

    async def test_strict_total_envelope_uses_remaining_amount_and_stops_with_unsettled_receipt(self):
        path = self.root / "upper-prices.json"
        path.write_text(json.dumps(price_card(window="upper_bound", api_model="deepseek-v4-flash")))
        keys = patch.dict("os.environ", {"YHC_BENCH_API_KEY": "test-key"})
        keys.start()
        self.addCleanup(keys.stop)
        agent = self.agent(price_card_path=str(path), max_estimated_cost="0.000040", reserve_cost_before_dispatch=True,
                           max_output_tokens_per_call=4, max_total_estimated_cost="0.000050",
                           continuation_budgets=[{"max_provider_calls": 1}])
        agent.exec_as_root, agent.exec_as_agent = AsyncMock(), AsyncMock()
        environment = SimpleNamespace(upload_file=AsyncMock(), exec=AsyncMock(), download_file=AsyncMock())
        first, second = budget_terminal(), budget_terminal()
        first["result"]["usage"] = priced_usage(strict=True)
        second["result"]["usage"] = dict(provider_calls=1, known_calls=1, unknown_calls=0, in_flight=0, untracked_calls=0,
                                             prompt_tokens=4, completion_tokens=3, total_tokens=7, cached_prompt_tokens=0,
                                             reasoning_tokens=0, complete=True, cost=dict(version=1, segment_id=str(uuid4()),
                                             price_card=price_card(window="upper_bound", api_model="deepseek-v4-flash"),
                                             known_estimated_amount="0.000020000000", actual_billed_amount=None,
                                             priced_calls=1, unpriced_calls=0, complete=True, limit_mode="pre_dispatch_reservation",
                                             reservation=dict(outstanding_amount="0.000000000000", retained_amount="0.000000000000",
                                                              peak_exposure_amount="0.000020000000", bound_violations=0)))
        second["result"].update(status="completed", exit_code=0, terminal_reason="completed", error=None)
        commands = []
        async def execute(**kwargs):
            if "env" in kwargs:
                commands.append(kwargs["command"])
            return SimpleNamespace(return_code=1 if len(commands) == 1 else 0)
        async def download(_source, target):
            Path(target).write_text(json.dumps([first, second][len(commands) - 1]) + "\n")
        environment.exec.side_effect, environment.download_file.side_effect = execute, download
        await agent.run("original instruction", environment, AgentContext())
        self.assertEqual(len(commands), 2)
        self.assertIn("--max-estimated-cost 0.000020", commands[1])
        self.assertIn("--resume test-session", commands[1])
    async def test_money_pause_continues_same_trial_with_incremental_cost(self):
        await self.exercise_money_continuation(known=True)

    async def test_unknown_pricing_does_not_burn_another_grant(self):
        await self.exercise_money_continuation(known=False)

    async def exercise_money_continuation(self, known):
        path = self.root / "prices.json"
        path.write_text(json.dumps(price_card()))
        keys = patch.dict("os.environ", {"YHC_BENCH_API_KEY": "test-key"})
        keys.start()
        self.addCleanup(keys.stop)
        agent = self.agent(price_card_path=str(path), max_estimated_cost="0.000025",
                           continuation_budgets=[{"max_estimated_cost": "0.000060"}])
        agent.exec_as_root, agent.exec_as_agent = AsyncMock(), AsyncMock()
        environment = SimpleNamespace(upload_file=AsyncMock(), exec=AsyncMock(), download_file=AsyncMock())
        commands = []
        records = [budget_terminal(), budget_terminal()]
        for record in records:
            record["result"]["usage"] = priced_usage()
        if not known:
            records[0]["result"]["usage"]["cost"].update(priced_calls=0, unpriced_calls=1, complete=False, known_estimated_amount="0.000000000000")
        records[1]["result"].update(status="completed", exit_code=0, terminal_reason="completed", error=None)

        async def execute(**kwargs):
            if "env" in kwargs:
                commands.append(kwargs["command"])
            return SimpleNamespace(return_code=1 if len(commands) == 1 else 0)

        async def download(_source, target):
            Path(target).write_text(json.dumps(records[len(commands) - 1]) + "\n")

        environment.exec.side_effect, environment.download_file.side_effect = execute, download
        context = AgentContext()
        if not known:
            with self.assertRaises(NonZeroAgentExitCodeError):
                await agent.run("original instruction", environment, context)
            self.assertEqual(len(commands), 1)
            return
        await agent.run("original instruction", environment, context)
        self.assertEqual(len(commands), 2)
        self.assertIn("--resume test-session", commands[1])
        self.assertIn("--max-estimated-cost 0.000060", commands[1])
        usage = context.metadata["yhc"]["usage"]
        self.assertEqual(native_totals(usage)["CNY"]["known_estimated_amount"], "0.000060000000")
        self.assertEqual(len(usage["cost_segments"]), 2)
        self.assertIsNone(context.cost_usd)
