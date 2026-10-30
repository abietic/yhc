import json
from pathlib import Path
import tempfile
import unittest

from scripts.terminal_bench.tool_feedback import collect_tool_feedback


class ToolFeedbackTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name).resolve()
        self.native = self.root / "native.jsonl"

    def stream(self, identifier, events):
        path = self.root / (identifier + ".response.sse")
        path.write_text("".join("data: " + json.dumps(e) + "\n\n" for e in events), encoding="utf-8")
        return path

    def response(self, identifier="one", tool="Edit", tool_id="edit-a"):
        return self.stream(identifier, [dict(type="response.completed", response=dict(output=[
            dict(type="function_call", name=tool, call_id=tool_id, arguments="PRIVATE_SENTINEL")]))])

    def native_events(self, tool="Edit", tool_id="edit-a", outcome="completed"):
        rows = [dict(schema_version=1, type="event", event=dict(kind=kind, tool=value)) for kind, value in (
            ("tool_start", dict(tool_name=tool, tool_call_id=tool_id)),
            ("tool_terminal", dict(tool_call_id=tool_id, outcome=outcome, raw_output="PRIVATE_SENTINEL")))]
        self.native.write_text("".join(json.dumps(e) + "\n" for e in rows), encoding="utf-8")

    def collect(self, records=None, protocol="responses"):
        records = [dict(id="one", state="known")] if records is None else records
        return collect_tool_feedback(self.root, records, protocol, self.native)

    def test_failed_edit_is_not_an_inferred_rejection_category(self):
        self.response()
        self.native_events(outcome="failed")
        got = self.collect()
        self.assertEqual(got["edit"], dict(emitted=1, matched_terminals=1, completed=0, failed=1, coverage="complete"))
        self.assertIsNone(got["edit_rejections"])
        self.assertNotIn("PRIVATE_SENTINEL", json.dumps(got))

    def test_missing_or_unfinished_response_does_not_prove_zero_rejections(self):
        self.native_events()
        for body in (None, 'data: {"type":"response.completed"}',
                     'data: {"type":"response.failed"}\n\n',
                     'data: {"type":"response.completed","response":[]}\n\n',
                     'data: {"type":"response.completed","response":{}}\n\n',
                     'data: {"type":"response.completed","type":"response.completed"}\n\n'):
            with self.subTest(body=body):
                if body is not None:
                    (self.root / "one.response.sse").write_text(body)
                got = self.collect()
                self.assertEqual(got["wire_inventory"], "unavailable")
                self.assertIsNone(got["edit_rejections"])
                self.assertIsNone(got["emitted_counts"])

    def test_unknown_call_keeps_inventory_partial(self):
        self.response()
        self.native_events()
        self.response("two", tool="Bash", tool_id="child-bash")
        got = self.collect([dict(id="one", state="known"), dict(id="two", state="unknown")])
        self.assertEqual(got["wire_inventory"], "partial")
        self.assertEqual(got["emitted_counts"], {"Edit": 1})
        self.assertIsNone(got["edit_rejections"])

    def test_parent_outcome_cannot_cover_a_child_edit_or_reused_tool_id(self):
        self.response()
        self.native_events()
        for child_id in ("child-edit", "edit-a"):
            with self.subTest(child_id=child_id):
                self.response("two", tool_id=child_id)
                got = self.collect([dict(id="one", state="known"), dict(id="two", state="known")])
                self.assertEqual(got["emitted_counts"], {"Edit": 2})
                self.assertEqual(got["edit"]["coverage"], "partial")
                self.assertIsNone(got["edit_rejections"])

    def test_native_name_mismatch_duplicate_or_truncated_events_cannot_prove_success(self):
        self.response()
        for case in ("name", "duplicate", "truncated", "schema"):
            with self.subTest(case=case):
                self.native_events(tool="Read" if case == "name" else "Edit")
                text = self.native.read_text()
                if case == "duplicate":
                    text += text
                elif case == "truncated":
                    text += '{"type":'
                elif case == "schema":
                    text = text.replace('"schema_version": 1', '"schema_version": true')
                self.native.write_text(text)
                self.assertIsNone(self.collect()["edit_rejections"])

    def test_messages_inventory_requires_closed_blocks_and_message_terminal(self):
        events = [dict(type="message_start", message={}),
                  dict(type="content_block_start", index=0, content_block=dict(type="tool_use", id="bash-a", name="bash", input="PRIVATE_SENTINEL")),
                  dict(type="content_block_stop", index=0), dict(type="message_stop")]
        self.stream("one", events)
        got = self.collect(protocol="messages")
        self.assertEqual(got["emitted_counts"], {"Bash": 1})
        self.assertEqual(got["wire_inventory"], "complete")
        self.assertEqual(got["edit"]["coverage"], "not_applicable")
        self.assertIsNone(got["edit_rejections"])
        self.assertIsNone(got["native_start_counts"])
        for partial in (events[:-1], events[1:], events[:2] + events[3:], events + [events[1]]):
            with self.subTest(partial=partial):
                self.stream("one", partial)
                self.assertEqual(self.collect(protocol="messages")["wire_inventory"], "unavailable")

    def test_private_names_text_and_unicode_separators_do_not_enter_counts(self):
        event = dict(type="response.completed", response=dict(output=[
            dict(type="message", text="PRIVATE_SENTINEL\u2028still data"),
            dict(type="function_call", name="PRIVATE_SENTINEL", call_id="private-call", arguments="PRIVATE_SENTINEL")]))
        (self.root / "one.response.sse").write_text("data: " + json.dumps(event, ensure_ascii=False) + "\n\n")
        self.native_events(tool="PRIVATE_SENTINEL", tool_id="private-call")
        got = self.collect()
        self.assertEqual(got["emitted_counts"], {"other": 1})
        self.assertEqual(got["native_start_counts"], {"other": 1})
        self.assertNotIn("PRIVATE_SENTINEL", json.dumps(got))

    def test_complete_empty_inventory_is_distinct_from_missing_evidence(self):
        self.stream("one", [dict(type="response.completed", response=dict(output=[]))])
        got = self.collect()
        self.assertEqual(got["wire_inventory"], "complete")
        self.assertEqual(got["emitted_counts"], {})
        self.assertIsNone(got["edit_rejections"])
        zero = self.collect([])
        self.assertEqual(zero["wire_inventory"], "complete")
        self.assertEqual(zero["emitted_counts"], {})

    def test_multiline_sse_comments_and_duplicate_terminals(self):
        body = ': keepalive\nevent: response.completed\ndata: {"type":"response.completed",\ndata: "response":{"output":[]}}\n\n'
        path = self.root / "one.response.sse"
        path.write_text(body)
        self.assertEqual(self.collect()["wire_inventory"], "complete")
        path.write_text(body + body)
        self.assertEqual(self.collect()["wire_inventory"], "unavailable")

    def test_events_after_stream_or_response_terminal_cannot_prove_zero_rejections(self):
        path = self.response()
        completed = path.read_text()
        self.native_events()
        trailing_event = 'data: {"type":"response.output_item.added"}\n\n'
        for body in ('data: [DONE]\n\n' + completed,
                     completed + 'data: [DONE]\n\n' + trailing_event,
                     completed + trailing_event):
            with self.subTest(body=body):
                path.write_text(body)
                got = self.collect()
                self.assertEqual(got["wire_inventory"], "unavailable")
                self.assertIsNone(got["edit_rejections"])
        path.write_text(completed + 'data: [DONE]\n\n: final keepalive\n\n')
        self.assertEqual(self.collect()["edit_rejections"], 0)

    def test_missing_native_or_unsupported_protocol_is_not_a_success_receipt(self):
        self.response()
        self.assertIsNone(self.collect()["edit_rejections"])
        self.native_events()
        got = self.collect(protocol="unsupported")
        self.assertEqual(got["wire_inventory"], "unavailable")
        self.assertIsNone(got["edit_rejections"])

    def test_symlinks_and_wire_path_traversal_are_not_evidence(self):
        path = self.response()
        original = path.with_name("original.sse")
        path.rename(original)
        try:
            path.symlink_to(original.name)
        except OSError:
            self.skipTest("symlinks unavailable")
        with self.assertRaises(ValueError):
            self.collect()
        path.unlink()
        path.write_bytes(original.read_bytes())
        self.native_events()
        native_original = self.native.with_name("original.jsonl")
        self.native.rename(native_original)
        self.native.symlink_to(native_original.name)
        with self.assertRaises(ValueError):
            self.collect()
        for identifier in ("../outside", ".", "..", "sub/path"):
            with self.subTest(identifier=identifier), self.assertRaises(ValueError):
                self.collect([dict(id=identifier, state="known")])


if __name__ == "__main__":
    unittest.main()
