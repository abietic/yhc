import copy
import hashlib
import json
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest

from scripts.terminal_bench.compare_runs import build_report, load_run, markdown


def row(agent="yhc", sample="sample-a"):
    return dict(sample=dict(sample_id=sample, agent=agent, task="generic-task", task_ref="sha256:abc"),
                normal_completion=True, official_reward={"rewards": {"reward": 1}},
                deadline=dict(agent_elapsed_seconds=3, reason="native_completed"),
                summary=dict(calls=2, unknown_calls=0, usage_complete=True,
                             input_tokens=100, cached_input_tokens=90, output_tokens=8,
                             thinking_tokens=4, known_peak_estimate_cny="0.001"))


def call(identifier, prompt=50, cached=45, thinking=2):
    return dict(id=identifier, state="known", input_tokens=prompt, cached_input_tokens=cached,
                output_tokens=4, thinking_tokens=thinking, known_peak_estimate_cny="0.0005",
                elapsed_seconds=1, requested_model="flash", effort="high")


def reference_trace():
    return dict(schema_version="ATIF-v1.7", agent=dict(name="reference-agent", model_name="reference-model"),
        steps=[dict(step_id=1, source="user", message="PRIVATE_SENTINEL"),
               dict(step_id=2, source="agent", llm_call_count=1, message="PRIVATE_SENTINEL",
                    tool_calls=[dict(function_name="Read"), dict(function_name="Read")],
                    metrics=dict(prompt_tokens=100, cached_tokens=80, completion_tokens=8,
                                 extra=dict(output_tokens_details=dict(thinking_tokens=4)))),
               dict(step_id=3, source="agent", llm_call_count=1, message="PRIVATE_SENTINEL",
                    metrics=dict(prompt_tokens=300, cached_tokens=250, completion_tokens=12,
                                 extra=dict(output_tokens_details=dict(reasoning_tokens=6))))],
        final_metrics=dict(total_prompt_tokens=400, total_cached_tokens=330, total_completion_tokens=20))


class ComparisonTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)

    def write(self, entry=None, records=None, name="one", synthetic=False):
        root = self.root / name
        root.mkdir()
        entry = row() if entry is None else entry
        if synthetic:
            data = dict(case="dsh-healthy", all_usage_synthetic=True, sample=entry["sample"],
                        result={"verifier_result": entry["official_reward"]},
                        operator_deadline=entry["deadline"], totals=entry["summary"], native={})
            (root / "report.json").write_text(json.dumps(data))
        else:
            (root / "scoreboard.json").write_text(json.dumps(dict(status="completed", samples=[entry])))
        if records is not None:
            wire = root / "wire"
            if not synthetic:
                wire /= entry["sample"]["sample_id"]
            wire.mkdir(parents=True)
            (wire / "ledger.json").write_text(json.dumps(dict(protocol="messages" if synthetic else "responses",
                sample=entry["sample"]["sample_id"], currency="CNY", records=records, denials=[])))
        return root

    def test_weighted_ratio_partial_thinking_and_retained_unknown(self):
        entry = row()
        entry["normal_completion"] = False
        entry["summary"].update(calls=3, unknown_calls=1, usage_complete=False,
            input_tokens=100, cached_input_tokens=9, thinking_tokens=None)
        records = [call("1", prompt=10, cached=9), call("2", prompt=90, cached=0, thinking=None),
                   dict(id="3", state="unknown", reserved_cny="2.621440", error="TimeoutError")]
        report = build_report([self.write(entry, records)])
        got = report["samples"][0]
        self.assertEqual(got["cache_hit_rate_known_including_first"], 0.09)
        self.assertFalse(got["usage_complete"])
        self.assertEqual(got["thinking"], dict(known_tokens=2, reported_calls=1, total_calls=3, complete=False))
        self.assertEqual(got["retained_unknown_cny"], "2.621440")
        self.assertEqual(got["known_peak_estimate_cny"], "0.0010")
        self.assertIsNone(got["actual_billed_cny"])
        self.assertIsNone(got["tool_seconds"])
        self.assertEqual(got["call_details"][2]["error_category"], "timeout")
        self.assertIsNone(got["call_details"][2]["transport_timings"]["request_sent_seconds"])

    def test_unstarted_is_not_a_zero_reward_or_known_zero_usage(self):
        (self.root / "plan.json").write_text(json.dumps(dict(target_task="generic-task", task_ref="sha256:abc",
            arms=[dict(agent="dsh", new_sample_id="next")], model="flash", effort="high")))
        got = load_run(self.root)[0]
        self.assertEqual(got["status"], "not_started")
        self.assertIsNone(got["reward"])
        self.assertIsNone(got["calls"])

    def test_unstarted_plan_rejects_execution_evidence_without_scoreboard(self):
        for index, marker in enumerate(("dispatch-claim.json", "dispatch-progress.json", "cohort/ledger.json",
                                        "wire/sample/ledger.json", "trials/sample/agent/native-result.json")):
            root = self.root / str(index)
            root.mkdir()
            (root / "plan.json").write_text(json.dumps(dict(target_task="generic-task", task_ref="sha256:abc",
                arms=[dict(agent="dsh", new_sample_id="next")], model="flash", effort="high")))
            evidence = root / marker
            evidence.parent.mkdir(parents=True, exist_ok=True)
            evidence.write_text('{"private_payload":"PRIVATE_SENTINEL"}')
            with self.subTest(marker=marker):
                with self.assertRaises(ValueError) as error:
                    build_report([root])
                self.assertNotIn("PRIVATE_SENTINEL", str(error.exception))
                self.assertEqual(evidence.read_text(), '{"private_payload":"PRIVATE_SENTINEL"}')

    def test_unstarted_plan_rejects_dangling_execution_marker(self):
        (self.root / "plan.json").write_text(json.dumps(dict(target_task="generic-task", task_ref="sha256:abc",
            arms=[dict(agent="dsh", new_sample_id="next")], model="flash", effort="high")))
        try:
            (self.root / "dispatch-claim.json").symlink_to(self.root / "missing-claim")
        except OSError:
            self.skipTest("symlink creation is unavailable")
        with self.assertRaises(ValueError):
            load_run(self.root)

    def test_nested_zero_call_evidence_does_not_start_the_paid_plan(self):
        (self.root / "plan.json").write_text(json.dumps(dict(target_task="generic-task", task_ref="sha256:abc",
            arms=[dict(agent="dsh", new_sample_id="next")], model="flash", effort="high")))
        zero = self.root / "matching-zero"
        (zero / "cohort").mkdir(parents=True)
        (zero / "wire").mkdir()
        (zero / "trials").mkdir()
        (zero / "dispatch-claim.json").write_text("{}")
        self.assertEqual(load_run(self.root)[0]["status"], "not_started")

    def test_cli_does_not_publish_unstarted_report_after_dispatch_claim(self):
        (self.root / "plan.json").write_text(json.dumps(dict(target_task="generic-task", task_ref="sha256:abc",
            arms=[dict(agent="dsh", new_sample_id="next")], model="flash", effort="high")))
        (self.root / "dispatch-claim.json").write_text("{}")
        output, human = self.root / "comparison.json", self.root / "comparison.md"
        result = subprocess.run([sys.executable, "-m", "scripts.terminal_bench.compare_runs", "--run", str(self.root),
            "--output", str(output), "--markdown", str(human)], capture_output=True, text=True)
        self.assertNotEqual(result.returncode, 0)
        self.assertFalse(output.exists())
        self.assertFalse(human.exists())

    def test_dsh_uses_sdk_notifications_instead_of_yhc_receipts(self):
        entry = row("dsh", "dsh-sample")
        root = self.write(entry, [call("1"), call("2")])
        wire = root / "wire/dsh-sample"
        ledger = json.loads((wire / "ledger.json").read_text())
        ledger["protocol"] = "messages"
        (wire / "ledger.json").write_text(json.dumps(ledger))
        frames = [dict(type="message_start", message={}),
                  dict(type="content_block_start", index=0,
                       content_block=dict(type="tool_use", id="edit-a", name="edit", input={})),
                  dict(type="content_block_stop", index=0), dict(type="message_stop")]
        (wire / "1.response.sse").write_text("".join("data: " + json.dumps(e) + "\n\n" for e in frames))
        (wire / "2.response.sse").write_text('data: {"type":"message_start","message":{}}\n\ndata: {"type":"message_stop"}\n\n')
        native = root / "trials/dsh-sample/agent"
        native.mkdir(parents=True)
        events = [dict(type="tool/call", data=dict(callId="edit-a", name="edit")),
                  dict(type="tool/result", data=dict(message=dict(role="tool", toolCallId="edit-a",
                       source=dict(kind="tool", callId="edit-a"), isError=False)))]
        (native / "notifications.jsonl").write_text("".join(json.dumps(dict(method="session.event",
            payload=dict(sessionId="dsh-sample", event=e))) + "\n" for e in events))
        # A different harness's receipt cannot supply or contradict the DSH result.
        (native / "yhc.jsonl").write_text(json.dumps(dict(type="event", schema_version=1,
            event=dict(kind="tool_terminal", tool=dict(tool_call_id="edit-a", outcome="failed")))) + "\n")
        got = load_run(root)[0]
        self.assertEqual(got["tool_feedback"]["edit"]["completed"], 1)
        self.assertEqual(got["edit_rejections"], 0)

    def test_synthetic_does_not_pair_with_real_and_missing_thinking_is_not_zero(self):
        real = self.write(records=[call("1"), call("2")])
        entry = row("dsh", "synthetic")
        entry["summary"]["thinking_tokens"] = None
        synthetic = self.write(entry, [call("1", thinking=None), call("2", thinking=None)], "two", True)
        report = build_report([real, synthetic])
        self.assertFalse(report["paired_tasks"])
        self.assertIsNone(report["samples"][1]["thinking"]["known_tokens"])
        self.assertIn("synthetic_preflight", markdown(report))

    def mark_scoreboard_synthetic(self, root, entry):
        path = root / "report.json"
        path.write_text(json.dumps(dict(all_usage_synthetic=True, real_provider_calls=0,
            case=entry["sample"]["agent"] + "-healthy", totals=entry["summary"],
            private_payload="PRIVATE_SENTINEL")))
        return path

    def test_synthetic_scoreboard_preserves_ledger_but_cannot_pair_with_real(self):
        entry = row()
        synthetic = self.write(entry, [call("1"), call("2")])
        marker = self.mark_scoreboard_synthetic(synthetic, entry)
        real = self.write(row("dsh", "sample-b"), [call("3"), call("4")], name="two")
        report = build_report([synthetic, real])
        got = report["samples"][0]
        self.assertEqual(got["evidence_kind"], "synthetic_preflight")
        self.assertEqual(got["call_evidence"], "per_call_checked")
        self.assertEqual((got["calls"], got["reward"]), (2, 1))
        self.assertEqual(got["classification_source_sha256"], hashlib.sha256(marker.read_bytes()).hexdigest())
        self.assertFalse(report["paired_tasks"])
        self.assertNotIn("PRIVATE_SENTINEL", json.dumps(report))

    def test_malformed_or_contradictory_synthetic_marker_fails_closed(self):
        for index, change in enumerate((dict(all_usage_synthetic=1), dict(real_provider_calls=1),
                                        dict(real_provider_calls=False))):
            root = self.write(records=[call("1"), call("2")], name=str(index))
            marker = self.mark_scoreboard_synthetic(root, row())
            data = json.loads(marker.read_text())
            data.update(change)
            marker.write_text(json.dumps(data))
            with self.subTest(change=change), self.assertRaises(ValueError):
                load_run(root)

    def test_synthetic_marker_must_cover_the_same_scoreboard(self):
        for index, field in enumerate(("totals", "case")):
            root = self.write(records=[call("1"), call("2")], name=str(index))
            marker = self.mark_scoreboard_synthetic(root, row())
            data = json.loads(marker.read_text())
            if field == "totals":
                data[field]["calls"] += 1
            else:
                data[field] = "dsh-healthy"
            marker.write_text(json.dumps(data))
            with self.subTest(field=field), self.assertRaises(ValueError):
                load_run(root)

    def test_recorded_scoreboard_with_other_report_keeps_its_classification(self):
        for index, extra in enumerate((dict(all_usage_synthetic=False, real_provider_calls=2), dict(status="completed"))):
            root = self.write(records=[call("1"), call("2")], name=str(index))
            (root / "report.json").write_text(json.dumps(extra))
            self.assertEqual(load_run(root)[0]["evidence_kind"], "recorded_run")

    def test_summary_only_remains_unverified_at_call_level(self):
        got = load_run(self.write())[0]
        self.assertEqual(got["call_evidence"], "summary_only")
        self.assertIsNone(got["retained_unknown_cny"])
        self.assertIsNone(got["thinking"]["reported_calls"])

    def test_child_tool_inventory_does_not_require_parent_native_events(self):
        root = self.write(records=[call("1"), call("2")])
        wire = root / "wire/sample-a"
        for identifier, name, tool_id in (("1", "Edit", "parent-edit"), ("2", "Bash", "child-bash")):
            item = dict(type="function_call", name=name, call_id=tool_id, arguments="PRIVATE_SENTINEL")
            event = dict(type="response.completed", response=dict(output=[item]))
            (wire / (identifier + ".response.sse")).write_text("data: " + json.dumps(event) + "\n\n")
        agent = root / "trials/sample-a/agent"
        agent.mkdir(parents=True)
        events = [dict(schema_version=1, type="event", event=dict(kind=kind, tool=tool)) for kind, tool in (
            ("tool_start", dict(tool_call_id="parent-edit", tool_name="Edit")),
            ("tool_terminal", dict(tool_call_id="parent-edit", outcome="completed", raw_output="PRIVATE_SENTINEL")))]
        (agent / "yhc.jsonl").write_text("".join(json.dumps(e) + "\n" for e in events))
        report = build_report([root])
        got = report["samples"][0]
        self.assertEqual(got["tool_feedback"]["emitted_counts"], {"Edit": 1, "Bash": 1})
        self.assertEqual(got["tool_feedback"]["unmatched_emitted_calls"], 1)
        self.assertEqual(got["edit_rejections"], 0)
        self.assertNotIn("PRIVATE_SENTINEL", json.dumps(report))
        self.assertIn("Bash=1, Edit=1", markdown(report))

    def test_duplicate_call_or_sample_bad_counters_and_conflicting_summary_fail(self):
        cases = [[call("1"), call("1")], [call("1", cached=51), call("2")],
                 [dict(call("1"), input_tokens=True), call("2")],
                 [dict(call("1"), known_peak_estimate_cny="NaN"), call("2")],
                 [call("1")], [dict(call("1"), state="reserved"), call("2")]]
        for index, records in enumerate(cases):
            with self.subTest(index=index), self.assertRaises(ValueError):
                load_run(self.write(records=records, name=str(index)))
        path = self.write(records=[call("a"), call("b")], name="dupe")
        with self.assertRaises(ValueError):
            build_report([path, path])

    def test_two_harnesses_pair_by_task_without_claiming_stability(self):
        a = self.write(records=[call("1"), call("2")])
        b = self.write(row("dsh", "sample-b"), [call("3"), call("4")], name="two")
        report = build_report([a, b])
        self.assertEqual(len(report["paired_tasks"]), 1)
        self.assertFalse(report["stability_established"])

    def test_invalid_and_private_fields_cannot_enter_output(self):
        entry = row()
        entry["native"] = {"prompt": "PRIVATE_SENTINEL"}
        entry["summary"]["thinking_characters"] = 1000000
        records = [dict(call("1"), raw_usage={"secret": "PRIVATE_SENTINEL"}, error="PRIVATE_SENTINEL",
                        stream_diagnostics={"payload": "PRIVATE_SENTINEL", "connected_seconds": 0.1}), call("2")]
        report = build_report([self.write(entry, records)])
        self.assertNotIn("PRIVATE_SENTINEL", json.dumps(report))
        self.assertNotIn(str(self.root), json.dumps(report))
        entry = copy.deepcopy(entry)
        entry["sample"]["sample_id"] = "../escape"
        with self.assertRaises(ValueError):
            load_run(self.write(entry, name="unsafe"))

    def test_cli_does_not_replace_existing_output(self):
        root = self.write(records=[call("1"), call("2")])
        target = self.root / "report.json"
        target.write_text("keep")
        p = subprocess.run([sys.executable, "-m", "scripts.terminal_bench.compare_runs", "--run", str(root),
                            "--output", str(target)], capture_output=True, text=True)
        self.assertNotEqual(p.returncode, 0)
        self.assertEqual(target.read_text(), "keep")

    def test_same_root_ledger_symlink_is_not_original_evidence(self):
        root = self.write(records=[call("1"), call("2")])
        path = root / "wire/sample-a/ledger.json"
        original = path.with_name("other.json")
        path.rename(original)
        try:
            path.symlink_to(original.name)
        except OSError:
            self.skipTest("symlink creation is unavailable")
        with self.assertRaises(ValueError):
            load_run(root)

    def reference(self, value=None):
        path = self.root / "reference.json"
        path.write_text(json.dumps(reference_trace() if value is None else value))
        return path

    def test_reference_thinking_aliases_count_metrics_once_per_step_and_stay_separate(self):
        path = self.reference()
        report = build_report([self.write()], reference_trajectories=[path])
        got = report["reference_trajectories"][0]
        self.assertEqual((got["model_calls"], got["input_tokens"], got["output_tokens"]), (2, 400, 20))
        self.assertEqual(got["cache_hit_rate_known_including_first"], 0.825)
        self.assertEqual(got["thinking"], dict(known_tokens=10, reported_steps=2, total_steps=2, complete=True))
        self.assertEqual(got["source_sha256"], hashlib.sha256(path.read_bytes()).hexdigest())
        self.assertIsNone(got["reward"])
        self.assertFalse(report["paired_tasks"])
        self.assertNotIn("PRIVATE_SENTINEL", json.dumps(report))
        self.assertIn("reference-model", markdown(report))

    def test_reference_missing_thinking_stays_partial_or_unknown(self):
        for missing, expected in (([2], 4), ([1, 2], None)):
            trace = reference_trace()
            for index in missing:
                trace["steps"][index]["metrics"]["extra"] = {}
            got = build_report([], reference_trajectories=[self.reference(trace)])["reference_trajectories"][0]
            self.assertEqual(got["thinking"]["known_tokens"], expected)
            self.assertFalse(got["thinking"]["complete"])

    def test_reference_copied_context_and_deterministic_steps_are_not_new_calls(self):
        trace = reference_trace()
        trace["steps"].append(dict(trace["steps"][1], step_id=4, is_copied_context=True))
        trace["steps"].append(dict(step_id=5, source="agent", llm_call_count=0))
        got = build_report([], reference_trajectories=[self.reference(trace)])["reference_trajectories"][0]
        self.assertEqual((got["model_calls"], got["input_tokens"]), (2, 400))
        trace["steps"][2].pop("llm_call_count")
        got = build_report([], reference_trajectories=[self.reference(trace)])["reference_trajectories"][0]
        self.assertIsNone(got["model_calls"])

    def test_reference_partial_token_coverage_does_not_infer_zero_or_call_count(self):
        trace = reference_trace()
        trace["steps"][1]["llm_call_count"] = 3
        trace["steps"][2]["metrics"]["cached_tokens"] = None
        got = build_report([], reference_trajectories=[self.reference(trace)])["reference_trajectories"][0]
        self.assertEqual(got["model_calls"], 4)
        self.assertFalse(got["usage_complete"])
        self.assertEqual(got["reported_metric_steps"]["cached_input_tokens"], 1)
        self.assertEqual(got["cache_hit_rate_known_including_first"], 0.8)
        self.assertEqual(got["thinking"]["known_tokens"], 10)
        trace["steps"][2]["model_name"] = "other-model"
        got = build_report([], reference_trajectories=[self.reference(trace)])["reference_trajectories"][0]
        self.assertEqual(got["model"], "mixed")
        self.assertEqual(got["models"], ["other-model", "reference-model"])
        with self.assertRaises(ValueError):
            path = self.reference()
            build_report([], reference_trajectories=[path, path])

    def test_reference_invalid_counters_alias_conflicts_and_totals_fail_closed(self):
        for change in (dict(thinking_tokens=True), dict(thinking_tokens=-1), dict(thinking_tokens=9),
                       dict(thinking_tokens=4, reasoning_tokens=5)):
            trace = reference_trace()
            trace["steps"][1]["metrics"]["extra"]["output_tokens_details"] = change
            with self.subTest(change=change), self.assertRaises(ValueError):
                build_report([], reference_trajectories=[self.reference(trace)])
        for field, value in (("cached_tokens", 101), ("prompt_tokens", True)):
            trace = reference_trace()
            trace["steps"][1]["metrics"][field] = value
            with self.subTest(field=field), self.assertRaises(ValueError):
                build_report([], reference_trajectories=[self.reference(trace)])
        trace = reference_trace()
        trace["final_metrics"]["total_prompt_tokens"] = 401
        with self.assertRaises(ValueError):
            build_report([], reference_trajectories=[self.reference(trace)])

    def test_fresh_gateway_receipts_preserve_known_cost_and_unknown_liability(self):
        entry = row()
        entry.update(normal_completion=False)
        entry["summary"].update(calls=3, unknown_calls=1, usage_complete=False, thinking_tokens=None)
        records = [call("1"), call("2")]
        for record in records:
            record["known_cost"] = record.pop("known_peak_estimate_cny")
            record["call"] = dict(id=record["id"], state="known", known_cost=record["known_cost"])
        records.append(dict(id="3", state="unknown", call=dict(id="3", state="unknown", liability="2.621440")))
        root = self.write(entry, records)
        got = build_report([root])["samples"][0]
        self.assertEqual(got["known_peak_estimate_cny"], "0.0010")
        self.assertEqual(got["retained_unknown_cny"], "2.621440")
        self.assertIsNone(got["actual_billed_cny"])
        records[0]["call"]["known_cost"] = "0.1"
        ledger = root / "wire/sample-a/ledger.json"
        ledger.write_text(json.dumps(dict(protocol="responses", sample="sample-a", currency="CNY", records=records)))
        with self.assertRaises(ValueError):
            build_report([root])

    def test_cli_includes_reference_metrics_without_replacing_old_reports(self):
        root = self.write()
        path = self.reference()
        target = self.root / "combined.json"
        p = subprocess.run([sys.executable, "-m", "scripts.terminal_bench.compare_runs", "--run", str(root),
                            "--reference-trajectory", str(path), "--output", str(target)], capture_output=True, text=True)
        self.assertEqual(p.returncode, 0, p.stderr)
        self.assertEqual(json.loads(target.read_text())["reference_trajectories"][0]["thinking"]["known_tokens"], 10)
