"""Calibrate the offline provider's history oracle without running an Agent."""

import copy
import datetime
import json
import tempfile
import unittest
from pathlib import Path

from scripts.terminal_bench.test_recovery_bundle_docker import validate_proof_directory
from scripts.terminal_bench.testdata.native_handoff import BUDGET_TEMPLATE, COLD_PROMPT, CONTEXT_TEMPLATE, PROMPT, verify_input

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
    def test_post_restore_read_then_edit_keeps_exact_native_history(self):
        body = request()
        body["input"][0]["content"] = body["input"][0]["content"].replace("provider_call_limit=1", "provider_call_limit=3")
        body["input"].extend([
            dict(type="message", role="assistant", content=" "),
            dict(type="function_call", call_id="continued-write", name="Write", arguments=json.dumps(dict(file_path="/workspace/continued.txt", content="continued once\n"))),
            dict(type="function_call_output", call_id="continued-write", output="Wrote 15 bytes to /workspace/continued.txt"),
            dict(type="message", role="assistant", content="Model error: provider usage accounting failed closed: run usage budget exceeded"),
            dict(type="message", role="user", content=COLD_PROMPT),
            dict(type="message", role="assistant", content=" "),
            dict(type="function_call", call_id="cold-read", name="Read", arguments=json.dumps(dict(file_path="/workspace/continued.txt"))),
            dict(type="function_call_output", call_id="cold-read", output="     1→operator original\n"),
        ])
        verify_input(body, NOW, "linux/arm64", provider_call_limit=3, cold_round=2, restored_read=True)
        prefix = "[system] [Session Memory — context from previous interactions:]\n\n"
        memory = dict(type="message", role="user", content=prefix + "- [] File modified: effects.count (via Write)\n")
        attached = copy.deepcopy(body)
        attached["input"].append(memory)
        verify_input(attached, NOW, "linux/arm64", provider_call_limit=3, cold_round=2, restored_read=True)
        body["input"].append(memory)
        body["input"].extend([
            dict(type="message", role="assistant", content=" "),
            dict(type="function_call", call_id="cold-edit", name="Edit", arguments=json.dumps(dict(file_path="/workspace/continued.txt", old_string="operator original", new_string="edited after cold restore"))),
            dict(type="function_call_output", call_id="cold-edit", output="Replaced 1 occurrence in /workspace/continued.txt"),
        ])
        verify_input(body, NOW, "linux/arm64", provider_call_limit=3, cold_round=3, restored_read=True)
        for fault in ("old_result", "read_result", "stale_edit", "unrelated_memory", "moved_memory", "duplicate_memory"):
            changed = copy.deepcopy(body)
            if fault == "old_result": changed["input"][10]["output"] = "replayed"
            if fault == "read_result": changed["input"][15]["output"] = "old bytes"
            if fault == "stale_edit": changed["input"][18]["arguments"] = json.dumps(dict(file_path="/workspace/continued.txt", old_string="continued once", new_string="edited after cold restore"))
            if fault == "unrelated_memory": changed["input"].append(dict(type="message", role="user", content=prefix + "- [] unrelated fact\n"))
            if fault == "moved_memory": changed["input"].insert(8, changed["input"].pop(16))
            if fault == "duplicate_memory": changed["input"].insert(16, copy.deepcopy(memory))
            with self.subTest(fault=fault), self.assertRaises(ValueError):
                verify_input(changed, NOW, "linux/arm64", provider_call_limit=3, cold_round=3, restored_read=True)

    def test_cold_turn_preserves_budget_diagnostic_and_exact_restored_memory_facts(self):
        body = request()
        body["input"][0]["content"] = body["input"][0]["content"].replace("provider_call_limit=1", "provider_call_limit=2")
        body["input"].extend([
            dict(type="message", role="assistant", content=" "),
            dict(type="function_call", call_id="continued-write", name="Write", arguments=json.dumps(dict(file_path="/workspace/continued.txt", content="continued once\n"))),
            dict(type="function_call_output", call_id="continued-write", output="Wrote 15 bytes to /workspace/continued.txt"),
            dict(type="message", role="assistant", content="Model error: provider usage accounting failed closed: run usage budget exceeded"),
            dict(type="message", role="user", content=COLD_PROMPT),
        ])
        verify_input(body, NOW, "linux/arm64", provider_call_limit=2, cold_round=1)
        body["input"].extend([
            dict(type="message", role="assistant", content=" "),
            dict(type="function_call", call_id="cold-edit", name="Edit", arguments=json.dumps(dict(file_path="/workspace/continued.txt", old_string="continued once", new_string="edited after cold restore"))),
            dict(type="function_call_output", call_id="cold-edit", output="Replaced 1 occurrence in /workspace/continued.txt"),
            dict(type="message", role="user", content="[system] [Session Memory — context from previous interactions:]\n\n- [] File modified: /workspace/continued.txt (via Write)\n- [] File modified: effects.count (via Write)\n"),
        ])
        verify_input(body, NOW, "linux/arm64", provider_call_limit=2, cold_round=2)
        for fault in ("diagnostic", "old_result", "extra_prompt", "memory_fact"):
            changed = copy.deepcopy(body)
            if fault == "diagnostic": changed["input"].pop(11)
            if fault == "old_result": changed["input"].pop(10)
            if fault == "extra_prompt": changed["input"].insert(12, dict(type="message", role="user", content="extra"))
            if fault == "memory_fact": changed["input"][-1]["content"] += "- [] unrelated fact\n"
            with self.subTest(fault=fault), self.assertRaises(ValueError):
                verify_input(changed, NOW, "linux/arm64", provider_call_limit=2, cold_round=2)

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
