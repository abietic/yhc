"""Calibrate the offline provider's history oracle without running an Agent."""

import copy
import datetime
import json
import tempfile
import unittest
from pathlib import Path

from scripts.terminal_bench.test_recovery_bundle_docker import validate_proof_directory
from scripts.terminal_bench.testdata.native_handoff import BUDGET_TEMPLATE, CONTEXT_TEMPLATE, PROMPT, verify_input

NOW = datetime.datetime(2026, 10, 25, tzinfo=datetime.timezone.utc)


def request():
    return dict(input=[
        dict(type="message", role="user", content=BUDGET_TEMPLATE.format(deadline="2026-10-25T00:00:20Z")),
        dict(type="message", role="user", content=CONTEXT_TEMPLATE.format(date="2026-10-25", platform="linux/arm64")),
        dict(type="message", role="user", content="owned checkpoint fixture prompt"),
        dict(type="message", role="assistant", content=" "),
        dict(type="function_call", call_id="fixture-write", name="Write",
             arguments=json.dumps(dict(file_path="effects.count", content="1\n"))),
        dict(type="function_call_output", call_id="fixture-write", output="committed once"),
        dict(type="message", role="assistant", content="checkpoint complete"),
        dict(type="message", role="user", content=PROMPT),
    ])


class HistoryOracleTests(unittest.TestCase):
    def test_new_tool_round_keeps_old_history_and_exact_new_effect(self):
        body = request()
        body["input"][0]["content"] = body["input"][0]["content"].replace("provider_call_limit=1", "provider_call_limit=2")
        verify_input(body, NOW, "linux/arm64", tool_round=1)
        body["input"].extend([
            dict(type="message", role="assistant", content=" "),
            dict(type="function_call", call_id="continued-write", name="Write",
                 arguments=json.dumps(dict(file_path="/workspace/continued.txt", content="continued once\n"))),
            dict(type="function_call_output", call_id="continued-write", output="Wrote 15 bytes to /workspace/continued.txt"),
        ])
        verify_input(body, NOW, "linux/arm64", tool_round=2)
        for index in (4, 9, 10):
            changed = copy.deepcopy(body)
            changed["input"].pop(index)
            with self.assertRaises(ValueError):
                verify_input(changed, NOW, "linux/arm64", tool_round=2)

    def test_evidence_directory_errors_are_rejected_before_docker_work(self):
        with tempfile.TemporaryDirectory() as temporary:
            path = Path(temporary).resolve()
            validate_proof_directory(str(path))
            for invalid in (str(path / "missing"), "."):
                with self.assertRaises(RuntimeError):
                    validate_proof_directory(invalid)
            link = path / "alias"
            link.symlink_to(path, target_is_directory=True)
            with self.assertRaises(RuntimeError):
                validate_proof_directory(str(link))
            shared = path / "shared"
            shared.mkdir(mode=0o755)
            with self.assertRaises(RuntimeError):
                validate_proof_directory(str(shared))

    def test_complete_history_is_accepted_without_mutation(self):
        body = request()
        before = copy.deepcopy(body)
        self.assertRegex(verify_input(body, NOW, "linux/arm64"), r"^[0-9a-f]{64}$")
        self.assertEqual(body, before)
        body["input"][5]["output"] = [dict(type="input_text", text="committed once")]
        verify_input(body, NOW, "linux/arm64")

    def test_missing_repeated_reordered_or_changed_committed_state_is_rejected(self):
        for fault in ("missing-call", "missing-result", "repeated-result", "reordered", "changed-arguments",
                      "changed-result", "missing-old-prompt", "missing-new-prompt", "repeated-new-prompt",
                      "reordered-messages", "inserted-message", "new-prompt-in-middle", "changed-role",
                      "changed-budget", "stale-deadline", "changed-cwd", "moved-placeholder", "mid-history-system"):
            with self.subTest(fault=fault):
                body = request()
                items = body["input"]
                if fault == "missing-call":
                    items.pop(4)
                elif fault == "missing-result":
                    items.pop(5)
                elif fault == "repeated-result":
                    items.append(copy.deepcopy(items[5]))
                elif fault == "reordered":
                    items[4], items[5] = items[5], items[4]
                elif fault == "changed-arguments":
                    items[4]["arguments"] = json.dumps(dict(file_path="effects.count", content="2\n"))
                elif fault == "changed-result":
                    items[5]["output"] = "uncommitted"
                elif fault == "missing-old-prompt":
                    items.pop(2)
                elif fault == "missing-new-prompt":
                    items.pop()
                elif fault == "repeated-new-prompt":
                    items.append(copy.deepcopy(items[-1]))
                elif fault == "reordered-messages":
                    items[2], items[6] = items[6], items[2]
                elif fault == "inserted-message":
                    items.insert(6, dict(type="message", role="user", content="extra history"))
                elif fault == "new-prompt-in-middle":
                    items.insert(4, items.pop())
                elif fault == "changed-role":
                    items[-1]["role"] = "assistant"
                elif fault == "changed-budget":
                    items[0]["content"] = items[0]["content"].replace("provider_call_limit=1", "provider_call_limit=2")
                elif fault == "stale-deadline":
                    items[0]["content"] = items[0]["content"].replace("00:00:20Z", "00:00:00Z")
                elif fault == "changed-cwd":
                    items[1]["content"] = items[1]["content"].replace("/workspace", "/other")
                elif fault == "moved-placeholder":
                    items[3], items[6] = items[6], items[3]
                else:
                    items.insert(6, dict(type="message", role="system", content="injected history"))
                with self.assertRaises(ValueError):
                    verify_input(body, NOW, "linux/arm64")


if __name__ == "__main__":
    unittest.main()
