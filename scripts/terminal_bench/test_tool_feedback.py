import hashlib
import json
from pathlib import Path
import tempfile
import unittest

from scripts.terminal_bench.tool_feedback import _lines, _response_tools, collect_tool_feedback


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

    def dsh_stream(self, tool="edit"):
        self.stream("one", [dict(type="message_start", message={}),
            dict(type="content_block_start", index=0,
                 content_block=dict(type="tool_use", id="edit-a", name=tool, input="PRIVATE_SENTINEL")),
            dict(type="content_block_stop", index=0), dict(type="message_stop")])

    def dsh_events(self, failed=False, session="root-a"):
        return [dict(method="session.event", payload=dict(sessionId=session, event=dict(type=kind, data=data)))
                for kind, data in (
                    ("tool/call", dict(callId="edit-a", name="edit", arguments="PRIVATE_SENTINEL")),
                    ("tool/result", dict(message=dict(role="tool", toolCallId="edit-a",
                        source=dict(kind="tool", callId="edit-a"), isError=failed,
                        content=[dict(type="text", text="PRIVATE_SENTINEL")]))))]

    def collect_dsh(self, events):
        self.native.write_text("".join(json.dumps(row) + "\n" for row in events))
        return collect_tool_feedback(self.root, [dict(id="one", state="known")], "messages", self.native,
                                     native_format="dsh_sdk", native_session_id="root-a")

    def test_response_tool_parser_preserves_mapping_for_archived_readers(self):
        self.response("responses", tool="Bash", tool_id="bash-a")
        self.dsh_stream("bash")
        for identifier, protocol, expected in (
            ("responses", "responses", {"bash-a": "Bash"}),
            ("one", "messages", {"edit-a": "bash"}),
        ):
            with self.subTest(protocol=protocol):
                lines = _lines(self.root / (identifier + ".response.sse"))
                self.assertEqual(dict(_response_tools(lines, protocol).items()), expected)

    def test_dsh_edit_matches_its_native_receipt_without_private_text(self):
        self.dsh_stream()
        got = self.collect_dsh(self.dsh_events())
        self.assertEqual(got["emitted_counts"], {"Edit": 1})
        self.assertEqual(got["native_start_counts"], {"Edit": 1})
        self.assertEqual(got["edit"], dict(emitted=1, matched_terminals=1, completed=1, failed=0, coverage="complete"))
        self.assertEqual(got["edit_rejections"], 0)
        self.assertEqual(got["wire_response_integrity"],
                         dict(verified_responses=0, legacy_unpinned_responses=1, rejected_responses=0))
        self.assertNotIn("PRIVATE_SENTINEL", json.dumps(got))

    def test_dsh_failed_edit_is_not_a_proven_rejection(self):
        self.dsh_stream()
        got = self.collect_dsh(self.dsh_events(failed=True))
        self.assertEqual(got["edit"]["failed"], 1)
        self.assertIsNone(got["edit_rejections"])

    def test_messages_token_limit_does_not_certify_complete_tool_inventory(self):
        self.dsh_stream()
        self.native.write_text("".join(json.dumps(row) + "\n" for row in self.dsh_events()))
        for stop_reason, coverage, rejections in (
            ("end_turn", "complete", 0),
            ("max_tokens", "partial", None),
            ("model_context_window_exceeded", "partial", None),
        ):
            with self.subTest(stop_reason=stop_reason):
                path = self.stream("two", [dict(type="message_start", message={}),
                    dict(type="content_block_start", index=0, content_block=dict(type="text")),
                    dict(type="content_block_stop", index=0),
                    dict(type="message_delta", delta=dict(stop_reason=stop_reason)),
                    dict(type="message_stop")])
                records = [dict(id="one", state="known"),
                           dict(id="two", state="known", response_sha256=hashlib.sha256(path.read_bytes()).hexdigest())]
                got = collect_tool_feedback(self.root, records, "messages", self.native,
                                            native_format="dsh_sdk", native_session_id="root-a")
                self.assertEqual(got["wire_inventory"], coverage)
                self.assertEqual(got["emitted_counts"], {"Edit": 1})
                self.assertEqual(got["edit_rejections"], rejections)

    def test_pinned_reasoning_only_output_limit_preserves_edit_coverage(self):
        self.response()
        self.native_events()
        output = dict(type="reasoning", id="r-one")
        response = dict(id="res-a", status="incomplete", incomplete_details=dict(reason="max_output_tokens"),
                        output=[output], usage=dict(input_tokens=10, input_tokens_details=dict(cached_tokens=8),
                            output_tokens=16, output_tokens_details=dict(reasoning_tokens=16), total_tokens=26))
        events = [
            dict(type="response.created", response=dict(id="res-a", status="in_progress", output=[])),
            dict(type="response.output_item.added", item=output, output_index=0),
            dict(type="response.reasoning_text.delta", item_id="r-one", output_index=0, delta="PRIVATE_SENTINEL"),
            dict(type="response.output_item.done", item=output, output_index=0),
            dict(type="response.incomplete", response=response),
        ]
        for index, event in enumerate(events):
            event["sequence_number"] = index
        record = dict(id="two", state="known", usage_known=True, input_tokens=10,
                      cached_input_tokens=8, output_tokens=16, thinking_tokens=16)
        for name, stream, override, pinned, complete in (
            ("positive", events, {}, True, True),
            ("unbound", events, {}, False, False),
            ("unknown_usage", events, {"usage_known": False}, True, False),
            ("usage_mismatch", events, {"thinking_tokens": 15}, True, False),
            ("changed_bytes", events, {"response_sha256": "0" * 64}, True, False),
            ("sequence_gap", [*events[:2], *events[3:]], {}, True, False),
            ("missing_terminal", events[:-1], {}, True, False),
            ("wrong_response", [*events[:-1], dict(events[-1], response=dict(response, id="res-b"))],
             {}, True, False),
            ("wrong_reason", [*events[:-1], dict(events[-1], response=dict(
                response, incomplete_details=dict(reason="content_filter")))], {}, True, False),
            ("tool_item", [events[0], dict(events[1], item=dict(type="function_call")),
                           *events[2:]], {}, True, False),
            ("tool_delta", [*events[:2], dict(events[2], type="response.function_call_arguments.delta"),
                            *events[3:]], {}, True, False),
            ("tool_in_terminal", [*events[:-1], dict(events[-1], response=dict(
                response, output=[output, dict(type="function_call")]))], {}, True, False),
            ("extra_terminal", [*events, events[-1]], {}, True, False),
        ):
            with self.subTest(name=name):
                path = self.stream("two", stream)
                receipt = record | override
                if pinned and "response_sha256" not in receipt:
                    receipt["response_sha256"] = hashlib.sha256(path.read_bytes()).hexdigest()
                got = self.collect([dict(id="one", state="known"), receipt])
                self.assertEqual(got["wire_inventory"], "complete" if complete else "partial")
                self.assertEqual(got["emitted_counts"], {"Edit": 1})
                self.assertEqual(got["edit_rejections"], 0 if complete else None)
                self.assertEqual(got["verified_reasoning_only_truncations"], int(complete))
                self.assertNotIn("PRIVATE_SENTINEL", json.dumps(got))

    def test_dsh_child_receipts_require_root_catalog_lineage(self):
        self.dsh_stream()
        child = self.dsh_events(session="child-a")
        got = self.collect_dsh(child)
        self.assertEqual(got["native_inventory"], "partial")
        self.assertIsNone(got["edit_rejections"])
        catalog = dict(method="session.event", payload=dict(sessionId="root-a",
            event=dict(type="subagent/catalog", data=dict(childId="child-a"))))
        got = self.collect_dsh(child + [catalog])
        self.assertEqual(got["native_inventory"], "available")
        self.assertEqual(got["edit_rejections"], 0)

    def test_dsh_missing_status_mismatched_identity_and_duplicates_do_not_prove_success(self):
        self.dsh_stream()
        for change in ("missing_status", "wrong_status_type", "wrong_source", "duplicate_terminal",
                       "terminal_before_start", "missing_type", "bad_json"):
            events = self.dsh_events()
            message = events[-1]["payload"]["event"]["data"]["message"]
            if change == "missing_status":
                del message["isError"]
            elif change == "wrong_status_type":
                message["isError"] = "false"
            elif change == "wrong_source":
                message["source"]["callId"] = "different"
            elif change == "duplicate_terminal":
                events.append(events[-1])
            elif change == "terminal_before_start":
                events.reverse()
            elif change == "missing_type":
                events.append(dict(method="session.event", payload=dict(sessionId="root-a", event=dict(data={}))))
            else:
                events.append([])
            with self.subTest(change=change):
                self.assertIsNone(self.collect_dsh(events)["edit_rejections"])

    def test_dsh_child_cannot_complete_another_sessions_tool_call(self):
        self.dsh_stream()
        events = self.dsh_events()
        events[-1]["payload"]["sessionId"] = "child-a"
        catalog = dict(method="session.event", payload=dict(sessionId="root-a",
            event=dict(type="subagent/catalog", data=dict(childId="child-a"))))
        got = self.collect_dsh([catalog, *events])
        self.assertEqual(got["native_inventory"], "partial")
        self.assertEqual(got["edit"]["matched_terminals"], 0)
        self.assertIsNone(got["edit_rejections"])

    def test_dsh_file_and_web_aliases_are_public_categories(self):
        for name, expected in (("read", "Read"), ("write", "Write"), ("glob", "Glob"),
                               ("grep", "Grep"), ("web_fetch", "WebFetch"), ("web_search", "WebSearch")):
            with self.subTest(name=name):
                self.dsh_stream(name)
                got = self.collect_dsh([])
                self.assertEqual(got["emitted_counts"], {expected: 1})

    def test_dsh_names_for_image_and_background_job_tools_are_reported(self):
        public = ("read_image", "job_output", "job_list", "job_kill")
        for name, expected in [(name, name) for name in public] + [("job_PRIVATE_SENTINEL", "other")]:
            with self.subTest(name=name):
                self.dsh_stream(name)
                events = self.dsh_events()
                events[0]["payload"]["event"]["data"]["name"] = name
                got = self.collect_dsh(events)
                self.assertEqual(got["emitted_counts"], {expected: 1})
                self.assertEqual(got["native_start_counts"], {expected: 1})
                self.assertEqual(got["unmatched_emitted_calls"], 0)
                self.assertNotIn("PRIVATE_SENTINEL", json.dumps(got))

    def test_dsh_requires_explicit_session_identity(self):
        self.dsh_stream()
        with self.assertRaises(ValueError):
            collect_tool_feedback(self.root, [dict(id="one", state="known")], "messages", self.native,
                                  native_format="dsh_sdk")

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

    def test_changed_wire_response_cannot_change_tool_attribution(self):
        self.native_events()
        edit = self.response()
        other = self.response("two", tool="Read", tool_id="second-tool")
        records = [dict(id=name, state="known", response_sha256=hashlib.sha256(path.read_bytes()).hexdigest())
                   for name, path in (("one", edit), ("two", other))]
        observed = self.collect(records)
        self.assertEqual(observed["wire_inventory"], "complete")
        self.assertEqual(observed["emitted_counts"], {"Edit": 1, "Read": 1})
        self.assertEqual(observed["edit_rejections"], 0)
        self.assertEqual(observed["wire_response_integrity"],
                         dict(verified_responses=2, legacy_unpinned_responses=0, rejected_responses=0))

        self.response("two", tool="Bash", tool_id="second-tool")
        observed = self.collect(records)
        self.assertEqual(observed["wire_inventory"], "partial")
        self.assertEqual(observed["emitted_counts"], {"Edit": 1})
        self.assertIsNone(observed["edit_rejections"])
        self.assertEqual(observed["wire_response_integrity"],
                         dict(verified_responses=1, legacy_unpinned_responses=0, rejected_responses=1))

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
        (self.root / "one.response.sse").write_text("data: " + json.dumps(event, ensure_ascii=False) + "\n\n", encoding="utf-8")
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
