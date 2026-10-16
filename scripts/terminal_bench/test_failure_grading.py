"""Real Harbor failure routing with synthetic receipts; no Docker or provider."""

import copy
import json
import unittest
from pathlib import Path
from types import SimpleNamespace
from unittest.mock import AsyncMock, Mock, patch

from harbor.agents.installed.base import NonZeroAgentExitCodeError
from harbor.models.agent.context import AgentContext
from harbor.models.task.config import TaskConfig
from harbor.trial.single_step import SingleStepTrial

from scripts.terminal_bench.test_agent import AgentFixture, budget_terminal
from scripts.terminal_bench.test_native_cost import price_card, priced_usage
from scripts.terminal_bench.usage import validated_usage


def unknown_terminal():
    record = budget_terminal(complete=False)
    usage = priced_usage(strict=True)
    usage.update(known_calls=0, unknown_calls=1, complete=False,
                 **{key: 0 for key in ("prompt_tokens", "completion_tokens", "total_tokens",
                                       "cached_prompt_tokens", "reasoning_tokens")})
    usage["cost"].update(priced_calls=0, unpriced_calls=1, complete=False,
                         known_estimated_amount="0.000000000000")
    usage["cost"]["reservation"].update(retained_amount="0.000036000000")
    record["result"].update(terminal_reason="run_usage_unknown",
                            error={"code": "run_usage_unknown"}, usage=usage)
    return record


class FailureGradingTests(AgentFixture, unittest.IsolatedAsyncioTestCase):
    def setUp(self):
        super().setUp()
        keys = patch.dict("os.environ", {"YHC_BENCH_API_KEY": "test-key"})
        keys.start()
        self.addCleanup(keys.stop)
        card = self.root / "upper-prices.json"
        card.write_text(json.dumps(price_card(window="upper_bound", api_model="deepseek-v4-flash")))
        self.subject = self.agent(price_card_path=str(card), max_estimated_cost="0.000040",
                                  reserve_cost_before_dispatch=True, max_output_tokens_per_call=4,
                                  max_total_estimated_cost="0.000040",
                                  continuation_budgets=[{"max_estimated_cost": "0.000040"}])
        self.subject.exec_as_root, self.subject.exec_as_agent = AsyncMock(), AsyncMock()
        self.environment = SimpleNamespace(upload_file=AsyncMock(), exec=AsyncMock(), download_file=AsyncMock())

    async def harbor_run(self, record, return_code=1):
        self.environment.exec.return_value = SimpleNamespace(return_code=return_code)

        async def download(_source, target):
            Path(target).write_text(json.dumps(record) + "\n")

        self.environment.download_file.side_effect = download
        context = AgentContext()

        async def agent_phase(**_kwargs):
            await self.subject.run("fixture task", self.environment, context)

        trial = SimpleNamespace(
            task=SimpleNamespace(config=TaskConfig(), instruction="fixture task"),
            result=SimpleNamespace(), _agent_timeout_sec=1, _load_trajectory=None,
            _run_agent_phase=agent_phase, _record_exception=Mock(),
            _sync_agent_output=AsyncMock(), _upload_agent_logs=AsyncMock(),
            _collect_artifacts=AsyncMock(), _run_verifier=AsyncMock(),
            _stop_agent_environment=AsyncMock())
        trial._run_agent = lambda: SingleStepTrial._run_agent(trial)
        try:
            await SingleStepTrial._run(trial)
        finally:
            self.trial, self.context = trial, context

    async def test_bounded_unknown_failure_is_graded_without_continuation_or_cost_release(self):
        record = unknown_terminal()
        self.assertIn("cost", validated_usage(record["result"]["usage"]))
        await self.harbor_run(record)
        self.trial._run_verifier.assert_awaited_once()
        failure = self.trial._record_exception.call_args.args[0]
        self.assertIsInstance(failure, NonZeroAgentExitCodeError)
        self.assertEqual(self.environment.exec.await_count, 1)
        metadata = self.context.metadata["yhc"]
        self.assertEqual(metadata["terminal_reason"], "run_usage_unknown")
        self.assertFalse(metadata["usage"]["complete"])
        self.assertFalse(metadata["usage"]["cost_complete"])
        self.assertEqual(len(metadata["usage"]["cost_segments"]), 1)
        self.assertEqual(metadata["usage"]["cost_segments"][0]["cost"]["reservation"]["retained_amount"],
                         "0.000036000000")
        self.assertIsNone(self.context.n_input_tokens)
        self.assertIsNone(self.context.cost_usd)
        self.assertFalse((self.subject.logs_dir / "yhc.segment-0000.jsonl").exists())

    async def test_untrusted_or_conflicting_failure_remains_a_trial_error(self):
        original = unknown_terminal()
        cases = [
            ("wrong reason", lambda r: r["result"].update(terminal_reason="run_budget_exceeded")),
            ("wrong code", lambda r: r["result"]["error"].update(code="other")),
            ("missing usage", lambda r: r["result"].pop("usage")),
            ("invalid amount", lambda r: r["result"]["usage"]["cost"].update(known_estimated_amount="NaN")),
            ("wrong card", lambda r: r["result"]["usage"]["cost"]["price_card"].update(currency="USD")),
            ("outstanding", lambda r: r["result"]["usage"]["cost"]["reservation"].update(
                outstanding_amount="0.000001000000", peak_exposure_amount="0.000037000000")),
            ("over limit", lambda r: r["result"]["usage"]["cost"]["reservation"].update(
                retained_amount="0.000041000000", peak_exposure_amount="0.000041000000")),
            ("bound violation", lambda r: r["result"]["usage"]["cost"]["reservation"].update(bound_violations=1)),
        ]
        for name, change in cases:
            with self.subTest(name=name):
                record = copy.deepcopy(original)
                change(record)
                self.environment.exec.reset_mock()
                with self.assertRaises(ValueError):
                    await self.harbor_run(record)
                self.trial._run_verifier.assert_not_awaited()
                self.assertEqual(self.environment.exec.await_count, 1)
        with self.assertRaises(ValueError):
            await self.harbor_run(original, return_code=0)
        self.trial._run_verifier.assert_not_awaited()
