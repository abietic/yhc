import copy
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

    def test_synthetic_does_not_pair_with_real_and_missing_thinking_is_not_zero(self):
        real = self.write(records=[call("1"), call("2")])
        entry = row("dsh", "synthetic")
        entry["summary"]["thinking_tokens"] = None
        synthetic = self.write(entry, [call("1", thinking=None), call("2", thinking=None)], "two", True)
        report = build_report([real, synthetic])
        self.assertFalse(report["paired_tasks"])
        self.assertIsNone(report["samples"][1]["thinking"]["known_tokens"])
        self.assertIn("synthetic_preflight", markdown(report))

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
