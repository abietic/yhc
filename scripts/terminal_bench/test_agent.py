"""Contract tests against real Harbor types; no provider calls or Docker needed."""

import asyncio
import hashlib
import json
import tempfile
import unittest
from pathlib import Path
from types import SimpleNamespace
from unittest.mock import AsyncMock, patch

from harbor.agents.installed.base import NonZeroAgentExitCodeError
from harbor.models.agent.context import AgentContext

from scripts.terminal_bench.yhc_agent import YHCAgent, read_result


def terminal(status="completed", exit_code=0):
    return {"schema_version": 1, "type": "result", "result": {
        "status": status, "exit_code": exit_code, "session_id": "test-session",
    }}


class ResultTests(unittest.TestCase):
    def test_only_one_closing_result_is_accepted(self):
        event = {"schema_version": 1, "type": "event", "event": {"kind": "tool"}}
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / "events.jsonl"
            cases = [([], False), ([event], False), ([terminal(), event], False),
                     ([terminal(), terminal()], False), ([event, terminal()], True)]
            for records, valid in cases:
                with self.subTest(records=records):
                    path.write_text("".join(json.dumps(row) + "\n" for row in records))
                    if valid:
                        self.assertEqual(read_result(path)["status"], "completed")
                    else:
                        with self.assertRaises(ValueError):
                            read_result(path)

    def test_rejects_malformed_or_future_schema(self):
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / "events.jsonl"
            for data in ["not-json\n", "[]\n", '{"schema_version":2}\n',
                         json.dumps({**terminal(), "result": {"status": "completed"}})]:
                path.write_text(data)
                with self.assertRaises(ValueError):
                    read_result(path)


class AgentFixture:
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory()
        self.addCleanup(self.temporary.cleanup)
        self.root = Path(self.temporary.name)
        self.binary = self.root / "yhc"
        header = bytearray(64)
        header[:6] = b"\x7fELF\x02\x01"
        header[18:20] = (62).to_bytes(2, "little")
        self.binary.write_bytes(header)

    def agent(self, **kwargs):
        return YHCAgent(logs_dir=self.root / "logs", binary_path=str(self.binary),
                        model_name="deepseek/deepseek-chat", **kwargs)


class AgentTests(AgentFixture, unittest.TestCase):
    def test_key_is_environment_only_and_task_environment_is_preserved(self):
        with patch.dict("os.environ", {"YHC_BENCH_API_KEY": "private-key",
                                      "UNRELATED_SECRET": "unrelated"}, clear=True):
            agent = self.agent()
            env = agent.execution_env()
            command = agent.execution_command()
            self.assertEqual(env["PROV_API_KEY"], "private-key")
            self.assertNotIn("private-key", command)
            self.assertNotIn("UNRELATED_SECRET", env)
            self.assertNotIn("HOME", env)
            self.assertNotIn("SSL_CERT_FILE", env)
            self.assertNotIn("cd ", command)
            self.assertIn("--sandbox danger-full-access", command)
            self.assertIn("--max-turns 0", command)

    def test_missing_credentials_fails_before_starting_environment(self):
        with patch.dict("os.environ", {}, clear=True):
            with self.assertRaisesRegex(ValueError, "YHC_BENCH_API_KEY"):
                self.agent()

    def test_binary_must_be_linux_elf(self):
        self.binary.write_bytes(b"Mach-O binary")
        with self.assertRaisesRegex(ValueError, "ELF"):
            self.agent()

    def test_invalid_turn_budget_is_rejected(self):
        with self.assertRaisesRegex(ValueError, "max_turns"):
            self.agent(max_turns=-1)

    def test_context_does_not_invent_token_usage(self):
        with patch.dict("os.environ", {"YHC_BENCH_API_KEY": "test-key"}):
            agent = self.agent()
        agent.logs_dir.mkdir()
        (agent.logs_dir / "yhc.jsonl").write_text(json.dumps(terminal("max_turns", 1)) + "\n")
        context = AgentContext()
        agent.populate_context_post_run(context)
        self.assertEqual(context.metadata["yhc"]["status"], "max_turns")
        self.assertIsNone(context.n_input_tokens)
        self.assertIsNone(context.cost_usd)


class RunTests(AgentFixture, unittest.IsolatedAsyncioTestCase):
    def setUp(self):
        super().setUp()
        keys = patch.dict("os.environ", {"YHC_BENCH_API_KEY": "test-key"})
        keys.start()
        self.addCleanup(keys.stop)
        self.subject = self.agent()
        self.subject.exec_as_root = AsyncMock()
        self.environment = SimpleNamespace(upload_file=AsyncMock(), exec=AsyncMock(),
                                           download_file=AsyncMock())

    async def test_cancel_or_download_failure_keeps_harbor_post_run_hook_eligible(self):
        for failed_operation, error in [("exec", asyncio.CancelledError()),
                                        ("download_file", OSError("download failed"))]:
            with self.subTest(operation=failed_operation):
                self.environment.exec.side_effect = None
                self.environment.exec.return_value = SimpleNamespace(return_code=0)
                self.environment.download_file.side_effect = None
                getattr(self.environment, failed_operation).side_effect = error
                context = AgentContext()
                with self.assertRaises(type(error)):
                    await self.subject.run("literal $(touch forbidden)", self.environment, context)
                self.assertTrue(context.is_empty())
                self.subject.populate_context_post_run(context)
                self.assertEqual(context.metadata["yhc"]["status"], "incomplete_stream")

    async def test_real_exit_and_stream_must_both_succeed(self):
        cases = [(0, terminal(), None), (1, terminal(), NonZeroAgentExitCodeError),
                 (0, terminal("max_turns", 1), ValueError), (0, None, ValueError)]
        for exit_code, record, expected_error in cases:
            with self.subTest(exit_code=exit_code, record=record):
                async def download(_source, target):
                    Path(target).write_text(json.dumps(record) + "\n" if record else "")
                self.environment.download_file.side_effect = download
                self.environment.exec.return_value = SimpleNamespace(return_code=exit_code)
                context = AgentContext()
                if expected_error:
                    with self.assertRaises(expected_error):
                        await self.subject.run("task", self.environment, context)
                else:
                    await self.subject.run("task", self.environment, context)
                    self.assertEqual(context.metadata["yhc"]["status"], "completed")


class InstallTests(AgentFixture, unittest.IsolatedAsyncioTestCase):
    def setUp(self):
        super().setUp()
        keys = patch.dict("os.environ", {"YHC_BENCH_API_KEY": "test-key"})
        keys.start()
        self.addCleanup(keys.stop)
        self.environment = SimpleNamespace(upload_file=AsyncMock(), exec=AsyncMock())

    def mock_commands(self, agent, rg_available):
        async def run(_environment, command):
            if command == "uname -m":
                return SimpleNamespace(return_code=0, stdout="x86_64\n")
            if "rg" in command and "--version" in command:
                return SimpleNamespace(return_code=0 if rg_available else 127,
                                       stdout="ripgrep test" if rg_available else "")
            return SimpleNamespace(return_code=0, stdout="")
        agent.exec_as_agent = AsyncMock(side_effect=run)
        agent.exec_as_root = AsyncMock()
        self.environment.exec.return_value = SimpleNamespace(
            return_code=0 if rg_available else 127, stdout="ripgrep test" if rg_available else "")

    async def test_missing_ripgrep_fails_during_install(self):
        agent = self.agent()
        self.mock_commands(agent, False)
        with self.assertRaisesRegex(ValueError, "ripgrep_path"):
            await agent.install(self.environment)
        self.environment.upload_file.assert_not_awaited()

    async def test_bundled_ripgrep_is_uploaded_and_selected(self):
        rg = self.root / "rg"
        rg.write_bytes(self.binary.read_bytes())
        agent = self.agent(ripgrep_path=str(rg))
        self.mock_commands(agent, True)
        await agent.install(self.environment)
        self.environment.upload_file.assert_any_await(rg.resolve(), agent.remote_dir + "/rg")
        self.assertIn('export PATH=', agent.execution_command())
        self.assertIn('"$PATH"', agent.execution_command())
        self.assertEqual(agent.ripgrep_sha256, agent.binary_sha256)

    async def test_writable_project_needs_no_privileged_state_changes(self):
        agent = self.agent()
        agent.exec_as_root = AsyncMock()
        self.environment.exec.return_value = SimpleNamespace(return_code=0)
        await agent.prepare_project_state(self.environment)
        agent.exec_as_root.assert_not_awaited()
        self.assertFalse(agent.state_dir_provisioned)

    async def test_install_prepares_project_state_as_part_of_setup(self):
        agent = self.agent()
        self.mock_commands(agent, True)
        agent.prepare_project_state = AsyncMock()
        await agent.install(self.environment)
        agent.prepare_project_state.assert_awaited_once_with(self.environment)

    async def test_readonly_project_gets_only_new_agent_owned_private_state(self):
        agent = self.agent()
        self.environment.exec.return_value = SimpleNamespace(return_code=1)
        agent.exec_as_agent = AsyncMock(side_effect=[
            SimpleNamespace(stdout="/task with spaces\n1001\n1002\n"),
            SimpleNamespace(return_code=0),
        ])
        agent.exec_as_root = AsyncMock()
        await agent.prepare_project_state(self.environment)
        command = agent.exec_as_root.await_args.kwargs["command"]
        self.assertEqual(command, "mkdir -m 700 -- '/task with spaces/.yhc' && "
                         "chown -h -- 1001:1002 '/task with spaces/.yhc'")
        self.assertTrue(agent.state_dir_provisioned)

    async def test_existing_inaccessible_state_is_never_reowned(self):
        agent = self.agent()
        self.environment.exec.return_value = SimpleNamespace(return_code=1)
        agent.exec_as_agent = AsyncMock(side_effect=RuntimeError("existing state refused"))
        agent.exec_as_root = AsyncMock()
        with self.assertRaises(RuntimeError):
            await agent.prepare_project_state(self.environment)
        agent.exec_as_root.assert_not_awaited()

    async def test_invalid_project_identity_fails_before_privileged_changes(self):
        for output in ["relative\n1\n1\n", "/app\n1;id\n1\n", "/app\n1\n",
                       "/app\n-1\n1\n", "/app\n1\n1\nextra\n"]:
            with self.subTest(output=output):
                agent = self.agent()
                self.environment.exec.return_value = SimpleNamespace(return_code=1)
                agent.exec_as_agent = AsyncMock(return_value=SimpleNamespace(stdout=output))
                agent.exec_as_root = AsyncMock()
                with self.assertRaises(ValueError):
                    await agent.prepare_project_state(self.environment)
                agent.exec_as_root.assert_not_awaited()

    def test_bundled_ripgrep_must_match_linux_architecture(self):
        rg = self.root / "rg"
        for data in (b"not ELF", self.binary.read_bytes()[:18] + (183).to_bytes(2, "little") + bytes(44)):
            rg.write_bytes(data)
            with self.subTest(data=data[:20]), self.assertRaises(ValueError):
                self.agent(ripgrep_path=str(rg))

    async def test_ca_bundle_is_scoped_to_agent_and_attested(self):
        ca = Path(__file__).with_name("testdata") / "ca.pem"
        agent = self.agent(ca_bundle_path=str(ca))
        self.mock_commands(agent, True)
        await agent.install(self.environment)
        remote_ca = agent.remote_dir + "/ca-certificates.crt"
        self.environment.upload_file.assert_any_await(ca.resolve(), remote_ca)
        self.assertEqual(agent.execution_env()["SSL_CERT_FILE"], remote_ca)
        self.assertNotIn("SSL_CERT_FILE", agent.execution_command())
        for call in agent.exec_as_root.await_args_list:
            self.assertNotIn("/etc/ssl", call.kwargs["command"])
        context = AgentContext()
        agent.populate_context_post_run(context)
        self.assertEqual(context.metadata["yhc"]["ca_bundle_sha256"],
                         hashlib.sha256(ca.read_bytes()).hexdigest())

    def test_invalid_or_private_key_bundle_is_rejected_before_provisioning(self):
        ca = self.root / "bad-ca.pem"
        public = (Path(__file__).with_name("testdata") / "ca.pem").read_bytes()
        for data in (b"not a certificate", b"-----BEGIN CERTIFICATE-----\ninvalid\n-----END CERTIFICATE-----",
                     public + b"\n-----BEGIN PRIVATE KEY-----\nnot-for-upload\n-----END PRIVATE KEY-----"):
            with self.subTest(data=data[:20]):
                ca.write_bytes(data)
                with self.assertRaises(ValueError):
                    self.agent(ca_bundle_path=str(ca))


if __name__ == "__main__":
    unittest.main()
