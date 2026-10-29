import copy
import json
from pathlib import Path
import tempfile
import unittest

from scripts.terminal_bench.prefix_audit import audit, canonical, compare_requests


class PrefixTests(unittest.TestCase):
    def test_append_only_and_static_changes_are_distinct(self):
        before = dict(model="flash", instructions="PRIVATE_INSTRUCTION", tools=[{"name": "one"}, {"name": "two"}],
                      input=[dict(role="user", content="PRIVATE_MESSAGE")])
        after = copy.deepcopy(before)
        after["input"].append(dict(role="assistant", content="PRIVATE_THINKING"))
        got = compare_requests(before, after, "responses")
        self.assertTrue(got["history_append_only"])
        self.assertTrue(got["static_components_stable"])
        self.assertEqual(got["unchanged_leading_messages"], 1)
        after["tools"].reverse()
        changed = compare_requests(before, after, "responses")
        self.assertTrue(changed["history_append_only"])
        self.assertEqual(changed["changed_static_components"], ["tools"])
        self.assertNotIn("PRIVATE", json.dumps(changed))

    def test_rewrite_truncation_and_messages_dialect(self):
        before = dict(system="stable", messages=[dict(role="user", content="a"), dict(role="assistant", content="b")])
        for current in ([dict(role="user", content="changed")], []):
            got = compare_requests(before, dict(system="stable", messages=current), "messages")
            self.assertFalse(got["history_append_only"])
            self.assertEqual(got["first_changed_previous_message"], 0)

    def test_canonical_key_order_unicode_and_config(self):
        a = dict(input="你好", model="flash")
        b = dict(model="flash", input="你好")
        got = compare_requests(a, b, "responses")
        self.assertTrue(got["canonical_request_unchanged"])
        self.assertEqual(got["common_history_prefix_bytes"], len(canonical(["你好"])))
        b["reasoning"] = dict(effort="high")
        self.assertEqual(compare_requests(a, b, "responses")["changed_static_components"], ["other"])

    def test_missing_request_breaks_adjacency_and_unknown_is_not_zero_usage(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            records = [dict(id="a", state="known", input_tokens=10, cached_input_tokens=9),
                       dict(id="b", state="unknown"),
                       dict(id="c", state="known", input_tokens=90, cached_input_tokens=0)]
            (root / "ledger.json").write_text(json.dumps(dict(records=records, protocol="responses")))
            for key in ("a", "c"):
                (root / (key + ".request.json")).write_text(json.dumps(dict(input="PRIVATE_SENTINEL")))
            report = audit(root)
            self.assertEqual(report["cache_hit_rate_known_including_first"], 0.09)
            self.assertFalse(report["usage_complete"])
            self.assertIsNone(report["transitions"][2]["comparison"])
            self.assertIsNone(report["cache_token_prediction"])
            self.assertNotIn("PRIVATE_SENTINEL", json.dumps(report))
            self.assertNotIn(directory, json.dumps(report))

    def test_unclosed_duplicate_or_escaping_calls_are_rejected(self):
        for records in ([dict(id="a", state="reserved")], [dict(id="../a", state="unknown")],
                        [dict(id="a", state="unknown"), dict(id="a", state="unknown")]):
            with self.subTest(records=records), tempfile.TemporaryDirectory() as directory:
                root = Path(directory)
                (root / "ledger.json").write_text(json.dumps(dict(records=records, protocol="messages")))
                with self.assertRaises(ValueError):
                    audit(root)

    def test_invalid_shape_or_nonfinite_json_cannot_produce_prefix_evidence(self):
        for value in (dict(model="flash"), dict(input=True), dict(input=[float("nan")])):
            with self.subTest(value=value), self.assertRaises(ValueError):
                compare_requests(value, value, "responses")

    def test_same_root_request_symlink_cannot_fabricate_a_prefix(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            records = [dict(id="a", state="unknown"), dict(id="b", state="unknown")]
            (root / "ledger.json").write_text(json.dumps(dict(records=records, protocol="messages")))
            (root / "b.request.json").write_text(json.dumps(dict(messages=[dict(role="user", content="private")])))
            try:
                (root / "a.request.json").symlink_to("b.request.json")
            except OSError:
                self.skipTest("symlink creation is unavailable")
            with self.assertRaises(ValueError):
                audit(root)
