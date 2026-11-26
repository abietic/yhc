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


def closed_native_usage():
    return dict(complete=True, call_ledger_complete=True, provider_calls=2, known_calls=2,
        unknown_calls=0, in_flight=0, untracked_calls=0, prompt_tokens=100,
        cached_prompt_tokens=9, completion_tokens=8, reasoning_tokens=4,
        call_ledger=dict(dropped_records=0, records=[
            dict(call_id="native-1", state="known", source="sdk", role="main",
                 resolved_model="flash", effort="high", private_payload="PRIVATE_SENTINEL",
                 tokens=dict(prompt_tokens=10, cached_prompt_tokens=9, completion_tokens=4, reasoning_tokens=2)),
            dict(call_id="native-2", state="known", source="webfetch_ai_processing", role="other",
                 resolved_model="flash", effort="high",
                 tokens=dict(prompt_tokens=90, cached_prompt_tokens=0, completion_tokens=4, reasoning_tokens=2))]))


class ComparisonTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)

    def write(self, entry=None, records=None, name="one", synthetic=False, ledger_extra=None):
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
            protocol = "messages" if synthetic or entry["sample"]["agent"] == "dsh" else "responses"
            (wire / "ledger.json").write_text(json.dumps(dict(protocol=protocol,
                sample=entry["sample"]["sample_id"], currency="CNY", records=records, denials=[],
                **(ledger_extra or {}))))
        return root

    def test_reports_file_requests_separately_from_model_attempts(self):
        entry = row(agent="dsh")
        entry["summary"].update(calls=3, unknown_calls=1, usage_complete=False)
        records = [dict(call("1"), upstream_attempted=True), dict(call("2"), upstream_attempted=True),
                   dict(id="3", state="unknown", reserved_cny="2.621440", upstream_attempted=False)]
        operations = [dict(method="POST", upstream_attempted=True, state="verified", file_id="PRIVATE_SENTINEL"),
                      dict(method="GET", upstream_attempted=False, state="verified"),
                      dict(method="POST", upstream_attempted=True, state="unverified")]
        report = build_report([self.write(entry, records, ledger_extra={"files": {"operations": operations}})])
        got, = report["samples"]
        self.assertEqual(got["calls"], 3)
        self.assertEqual(got["upstream_attempts"], {"model": 2, "files": 2})
        rendered = markdown(report)
        self.assertIn("Model upstream attempts", rendered)
        self.assertIn("| dsh | sample-a | 2 | 2 |", rendered)
        self.assertNotIn("PRIVATE_SENTINEL", json.dumps(report))

    def test_upstream_attempts_retain_legacy_and_disabled_evidence(self):
        legacy = self.write(records=[dict(call("1"), upstream_attempted=True), call("2")], name="legacy")
        self.assertEqual(load_run(legacy)[0]["upstream_attempts"], {"model": None, "files": None})
        disabled = self.write(records=[dict(call("1"), upstream_attempted=True),
                                       dict(call("2"), upstream_attempted=True)], name="disabled",
                              ledger_extra={"files": None})
        self.assertEqual(load_run(disabled)[0]["upstream_attempts"], {"model": 2, "files": 0})

    def test_rejects_invalid_upstream_attempt_markers(self):
        for name, records, extra in (
            ("model", [dict(call("1"), upstream_attempted="true"), call("2")], {}),
            ("files", [call("1"), call("2")], {"files": {"operations": [{"upstream_attempted": 1}]}}),
            ("inventory", [call("1"), call("2")], {"files": {"operations": None}}),
        ):
            with self.subTest(name=name), self.assertRaisesRegex(ValueError, "invalid upstream attempt"):
                load_run(self.write(records=records, name=name, ledger_extra=extra))

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
        self.assertFalse(got["wire_usage_complete"])
        self.assertIsNone(got["native_usage_coverage"])
        self.assertEqual(got["thinking"], dict(known_tokens=2, reported_calls=1, total_calls=3, complete=False))
        self.assertEqual(got["retained_unknown_cny"], "2.621440")
        self.assertEqual(got["known_peak_estimate_cny"], "0.0010")
        self.assertIsNone(got["actual_billed_cny"])
        self.assertIsNone(got["tool_seconds"])
        self.assertEqual(got["call_details"][2]["error_category"], "timeout")
        self.assertIsNone(got["call_details"][2]["transport_timings"]["request_sent_seconds"])

    def test_successful_request_timing_is_read_from_forwarding_receipt(self):
        first = call("1")
        del first["elapsed_seconds"]
        first.update(stream_diagnostics=None, result=dict(call_id="1", stream_diagnostics={
            "first_byte_seconds": 0.25, "terminal_seconds": 1.5, "elapsed_seconds": 1.75,
        }))
        second = dict(call("2"), elapsed_seconds=0)
        sample, = load_run(self.write(records=[first, second]))
        self.assertEqual(sample["timed_calls"], 2)
        self.assertEqual(sample["sum_request_elapsed_seconds"], 1.75)
        self.assertTrue(sample["call_details"][0]["timing_evidence_available"])
        self.assertEqual(sample["call_details"][0]["transport_timings"]["first_byte_seconds"], 0.25)
        self.assertEqual(sample["call_details"][0]["elapsed_seconds"], 1.75)
        self.assertEqual(sample["call_details"][1]["elapsed_seconds"], 0)
        self.assertIsNone(sample["tool_seconds"])

    def test_nested_timing_requires_the_same_call_identity(self):
        first = call("1")
        del first["elapsed_seconds"]
        first["result"] = dict(call_id="foreign", stream_diagnostics={"elapsed_seconds": 2.5})
        sample, = load_run(self.write(records=[first, call("2")]))
        self.assertEqual(sample["timed_calls"], 1)
        self.assertEqual(sample["sum_request_elapsed_seconds"], 1)
        self.assertFalse(sample["call_details"][0]["timing_evidence_available"])

    def test_report_classifies_wrapped_transport_timeouts_from_first_cause(self):
        entry = row()
        entry["normal_completion"] = False
        entry["summary"].update(calls=3, unknown_calls=1, usage_complete=False)
        cases = (
            ("deadline", {"first_error_code": "stream_deadline", "request_sent_seconds": 0.08}, "timeout"),
            ("last-error-only", {"error_code": "stream_deadline"}, "other"),
            ("connect-timeout", {"first_error_code": "connect_error", "first_timeout_reason": "connect_timeout",
                                 "first_timeout_source": "socket_timeout"}, "timeout"),
            ("connect-failure", {"first_error_code": "connect_error"}, "other"),
            ("cleanup-deadline", {"first_error_code": "upstream_read_error", "error_code": "stream_deadline"}, "other"),
            ("unavailable", {}, "other"),
        )
        for name, diagnostics, expected in cases:
            with self.subTest(name=name):
                records = [call("1"), call("2"), dict(
                    id="3", state="unknown", reserved_cny="2.621440",
                    error="upstream_attempt_failed", stream_diagnostics=diagnostics)]
                sample, = load_run(self.write(entry, records, name=name))
                self.assertEqual(sample["call_details"][-1]["error_category"], expected)
                self.assertFalse(sample["usage_complete"])
                self.assertEqual(sample["retained_unknown_cny"], "2.621440")

    def test_complete_gateway_does_not_hide_native_unknown_attempt(self):
        entry = row()
        entry["normal_completion"] = False
        entry["native"] = dict(native_usage=dict(complete=False, provider_calls=3, unknown_calls=1,
            cost=dict(reservation=dict(retained_amount="2.621440"))))
        report = build_report([self.write(entry, [call("1"), call("2")])])
        got, = report["samples"]
        self.assertTrue(got["wire_usage_complete"])
        self.assertFalse(got["usage_complete"])
        self.assertEqual(got["native_usage_coverage"], dict(complete=False, attempts=3, unknown_calls=1,
                                                            in_flight=None, untracked_calls=None,
                                                            retained_unknown_cny="2.621440"))
        self.assertEqual((got["calls"], got["unknown_calls"], got["retained_unknown_cny"]), (2, 0, "0"))
        self.assertEqual(got["thinking"], dict(known_tokens=4, reported_calls=2, total_calls=2, complete=True))
        self.assertIn("Gateway known/total calls", markdown(report))
        self.assertIn("| yhc | sample-a | 3 | 1 | 2.621440 |", markdown(report))

    def test_complete_native_cannot_omit_in_flight_or_untracked_attempts(self):
        for key in ("unknown_calls", "in_flight", "untracked_calls"):
            entry = row()
            entry["native"] = dict(native_usage=dict(complete=True, provider_calls=2, **{key: 1}))
            with self.subTest(key=key), self.assertRaisesRegex(ValueError, "inconsistent native usage coverage"):
                load_run(self.write(entry, [call("1"), call("2")], name=key))

    def test_idle_summary_is_preserved_with_explicit_evidence_coverage(self):
        for name, values, coverage in (("summary", [None, None], "summary_only"),
                                       ("partial", ["0.0002", None], "summary_only"),
                                       ("receipts", ["0.0002", "0.0002"], "per_call_checked")):
            entry = row()
            entry["summary"]["known_idle_estimate_cny"] = "0.0004"
            records = [call("1"), call("2")]
            for record, value in zip(records, values):
                if value is not None:
                    record["known_idle_estimate_cny"] = value
            with self.subTest(name=name):
                report = build_report([self.write(entry, records, name=name)])
                got = report["samples"][0]
                self.assertEqual(got["known_idle_estimate_cny"], "0.0004")
                self.assertEqual(got["known_idle_estimate_evidence"], coverage)
                self.assertTrue(got["wire_usage_complete"])
                self.assertTrue(got["usage_complete"])
                self.assertIsNone(got["native_usage_coverage"])
                self.assertEqual((got["calls"], got["known_calls"], got["unknown_calls"],
                                  got["retained_unknown_cny"]), (2, 2, 0, "0"))
                self.assertIn("0.0004 /", markdown(report))
                self.assertIn(coverage, markdown(report))
                self.assertIsNone(got["actual_billed_cny"])

    def test_idle_receipt_conflicts_are_not_hidden_by_partial_coverage(self):
        for index, values in enumerate((["0.0005", None], ["0.0002", "0.0003"], ["NaN", None])):
            entry = row()
            entry["summary"]["known_idle_estimate_cny"] = "0.0004"
            records = [call("1"), call("2")]
            for record, value in zip(records, values):
                if value is not None:
                    record["known_idle_estimate_cny"] = value
            with self.subTest(values=values), self.assertRaises(ValueError):
                load_run(self.write(entry, records, name=str(index)))

    def test_closed_native_main_and_auxiliary_usage_is_reconciled(self):
        entry = row()
        entry["summary"]["cached_input_tokens"] = 9
        entry["native"] = dict(native_usage=closed_native_usage())
        report = build_report([self.write(entry, [call("1", prompt=10, cached=9), call("2", prompt=90, cached=0)])])
        got = report["samples"][0]
        breakdown = got["usage_breakdown"]
        self.assertEqual(breakdown["evidence"], "native_aggregate_checked")
        routes = {r["source"]: r for r in breakdown["routes"]}
        self.assertEqual((routes["sdk"]["calls"], routes["sdk"]["cache_hit_rate"], routes["sdk"]["thinking_tokens"]), (1, 0.9, 2))
        self.assertEqual((routes["webfetch_ai_processing"]["calls"], routes["webfetch_ai_processing"]["cache_hit_rate"]), (1, 0))
        self.assertEqual(got["cache_hit_rate_known_including_first"], 0.09)
        self.assertIn("webfetch_ai_processing", markdown(report))
        self.assertNotIn("PRIVATE_SENTINEL", json.dumps(report))

    def test_native_breakdown_rejects_conflicting_closed_inventory(self):
        for index, mutation in enumerate(("tokens", "thinking", "duplicate", "dropped")):
            native = closed_native_usage()
            if mutation == "tokens":
                native["call_ledger"]["records"][0]["tokens"]["prompt_tokens"] += 1
            elif mutation == "thinking":
                native["call_ledger"]["records"][0]["tokens"]["reasoning_tokens"] = 1
            elif mutation == "duplicate":
                native["call_ledger"]["records"][1]["call_id"] = "native-1"
            else:
                native["call_ledger"]["dropped_records"] = 1
            entry = row()
            entry["summary"]["cached_input_tokens"] = 9
            entry["native"] = dict(native_usage=native)
            with self.subTest(mutation=mutation), self.assertRaises(ValueError):
                load_run(self.write(entry, [call("1", prompt=10, cached=9), call("2", prompt=90, cached=0)], name=str(index)))

    def test_unavailable_native_or_wire_thinking_is_not_fabricated(self):
        for index, complete in enumerate((True, False)):
            entry = row()
            entry["summary"].update(cached_input_tokens=9, thinking_tokens=None)
            native = closed_native_usage()
            native["complete"] = complete
            entry["native"] = dict(native_usage=native)
            got = load_run(self.write(entry, [call("1", prompt=10, cached=9, thinking=None),
                                              call("2", prompt=90, cached=0, thinking=None)], name=str(index)))[0]
            self.assertIsNone(got["thinking"]["known_tokens"])
            if complete:
                self.assertTrue(got["usage_breakdown"]["routes"])
                self.assertTrue(all(r["thinking_tokens"] is None for r in got["usage_breakdown"]["routes"]))
            else:
                self.assertEqual(got["usage_breakdown"], dict(evidence="unavailable", routes=[]))

    def test_budget_interruption_reason_is_separate_from_reward(self):
        entry = row()
        entry.update(normal_completion=False, outcome=dict(ledger_status="budget_paused", reason="budget_admission_denied"))
        entry["deadline"]["reason"] = "native_error"
        report = build_report([self.write(entry, [call("1"), call("2")])])
        got = report["samples"][0]
        self.assertEqual((got["reward"], got["operator_status"], got["outcome_reason"]), (1, "budget_paused", "budget_admission_denied"))
        self.assertEqual(got["terminal_reason"], "native_error")
        self.assertIn("budget_admission_denied", markdown(report))

    def test_zero_call_native_inventory_does_not_invent_thinking(self):
        entry = row()
        entry["summary"].update(calls=0, input_tokens=0, cached_input_tokens=0, output_tokens=0,
                                thinking_tokens=None, known_peak_estimate_cny="0")
        native = closed_native_usage()
        for key in ("provider_calls", "known_calls", "prompt_tokens", "cached_prompt_tokens",
                    "completion_tokens", "reasoning_tokens"):
            native[key] = 0
        native["call_ledger"]["records"] = []
        entry["native"] = dict(native_usage=native)
        got = load_run(self.write(entry, []))[0]
        self.assertIsNone(got["thinking"]["known_tokens"])
        self.assertEqual(got["usage_breakdown"], dict(evidence="native_aggregate_checked", routes=[]))

    def test_unstarted_is_not_a_zero_reward_or_known_zero_usage(self):
        (self.root / "plan.json").write_text(json.dumps(dict(target_task="generic-task", task_ref="sha256:abc",
            arms=[dict(agent="dsh", new_sample_id="next")], model="flash", effort="high")))
        got = load_run(self.root)[0]
        self.assertEqual(got["status"], "not_started")
        self.assertIsNone(got["reward"])
        self.assertIsNone(got["calls"])

    def test_unstarted_reads_current_operator_plan_with_two_arms(self):
        path = self.root / "plan.json"
        raw = json.dumps(dict(task="generic-task", task_ref="sha256:abc", arms=[
            dict(agent="dsh", sample_id="next-dsh"), dict(agent="yhc", sample_id="next-yhc")])).encode()
        path.write_bytes(raw)
        dsh, yhc = load_run(self.root)

        for entry, agent, sample in ((dsh, "dsh", "next-dsh"), (yhc, "yhc", "next-yhc")):
            self.assertEqual(entry, dict(agent=agent, sample_id=sample, task="generic-task", task_ref="sha256:abc",
                status="not_started", reward=None, calls=None, evidence_kind="unstarted",
                source_sha256=hashlib.sha256(raw).hexdigest()))

        (self.root / "dispatch-claim.json").write_text("{}")
        with self.assertRaisesRegex(ValueError, "execution evidence exists"):
            load_run(self.root)

    def test_unstarted_plan_rejects_conflicting_identifier_aliases(self):
        for aliases in (dict(target_task="other-task"), dict(arms=[
                dict(agent="dsh", sample_id="next-dsh", new_sample_id="different")])):
            with self.subTest(aliases=aliases):
                plan = dict(task="generic-task", task_ref="sha256:abc", arms=[dict(agent="dsh", sample_id="next-dsh")])
                (self.root / "plan.json").write_text(json.dumps(dict(plan, **aliases)))
                with self.assertRaisesRegex(ValueError, "conflicting plan identifiers"):
                    load_run(self.root)

    def test_canonical_closeout_wins_over_a_running_scoreboard(self):
        root = self.write()
        provisional = row()
        provisional["normal_completion"] = False
        provisional["official_reward"] = {"rewards": {"reward": 0}}
        (root / "scoreboard.json").write_text(json.dumps(dict(status="running", samples=[provisional])))
        closeout = root / "dispatch-closeout.json"
        closeout.write_text(json.dumps(dict(status="completed", samples=[row()])))

        got, = build_report([root])["samples"]
        self.assertEqual(got["status"], "completed")
        self.assertEqual(got["reward"], 1)
        self.assertEqual(got["source_sha256"], hashlib.sha256(closeout.read_bytes()).hexdigest())

    def test_canonical_closeout_with_nested_wire_needs_no_scoreboard(self):
        root = self.write(records=[call("1"), call("2")])
        (root / "scoreboard.json").rename(root / "dispatch-closeout.json")

        got, = load_run(root)
        self.assertEqual(got["reward"], 1)
        self.assertEqual(got["known_calls"], 2)
        self.assertIsNotNone(got["ledger_sha256"])

    def test_nonterminal_run_cannot_supply_comparison_rows(self):
        for name in ("scoreboard.json", "dispatch-closeout.json"):
            with self.subTest(name=name):
                root = self.write(name=name)
                (root / name).write_text(json.dumps(dict(status="running", samples=[row()])))
                with self.assertRaisesRegex(ValueError, "execution record is not terminal"):
                    build_report([root])

    def test_invalid_canonical_closeout_cannot_fall_back_to_scoreboard(self):
        root = self.write()
        (root / "dispatch-closeout.json").write_text("{")
        with self.assertRaises(ValueError):
            load_run(root)

    def test_cohort_scoreboard_cannot_replace_missing_canonical_closeout(self):
        cases = (
            ("completed", dict(factory_calls=2, validated_zero_requests={})),
            ("budget_paused", dict(factory_calls=2, validated_zero_requests={})),
            ("zero_preflight_passed", dict(factory_calls=0, validated_zero_requests={"sample-a": 1},
                                          zero_mode=True, real_provider_calls=0)),
            ("completed", dict(finalization_checks_passed=True)),
        )
        for index, (status, markers) in enumerate(cases):
            with self.subTest(status=status, markers=markers):
                root = self.write(name=str(index), records=[call("1"), call("2")])
                data = dict(status=status, samples=[row()], **markers)
                (root / "scoreboard.json").write_text(json.dumps(data))
                with self.assertRaisesRegex(ValueError, "canonical dispatch closeout is missing"):
                    build_report([root])

                data["finalization_checks_passed"] = True
                (root / "dispatch-closeout.json").write_text(json.dumps(data))
                self.assertEqual(build_report([root])["samples"][0]["reward"], 1)

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
        frames = [dict(type="message_start", message={}),
                  dict(type="content_block_start", index=0,
                       content_block=dict(type="tool_use", id="edit-a", name="edit", input={})),
                  dict(type="content_block_stop", index=0), dict(type="message_stop")]
        (wire / "1.response.sse").write_text("".join("data: " + json.dumps(e) + "\n\n" for e in frames))
        (wire / "2.response.sse").write_text('data: {"type":"message_start","message":{}}\n\ndata: {"type":"message_stop"}\n\n')
        reply = dict(role="user", content=[dict(type="tool_result", tool_use_id="edit-a", content="PRIVATE_SENTINEL")])
        for record, messages in zip(ledger["records"], ([], [reply])):
            identifier = record["id"]
            request = json.dumps({"messages": messages}).encode()
            (wire / (identifier + ".request.json")).write_bytes(request)
            record.update(body_sha256=hashlib.sha256(request).hexdigest(), upstream_attempted=True,
                          response_sha256=hashlib.sha256((wire / (identifier + ".response.sse")).read_bytes()).hexdigest())
        (wire / "ledger.json").write_text(json.dumps(ledger))
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
        report = build_report([root])
        got = report["samples"][0]
        self.assertEqual(got["tool_feedback"]["edit"]["completed"], 1)
        self.assertEqual(got["edit_rejections"], 0)
        self.assertEqual(got["tool_feedback"]["wire_text_feedback"]["observed_returns"], 1)
        self.assertIn("| dsh | complete | Edit=1 | 0 | 0 | 2/2 |", markdown(report))
        self.assertNotIn("PRIVATE_SENTINEL", markdown(report))

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

    def test_comparison_renders_bash_exit_marker_separately_from_native_completion(self):
        first_response = ("data: " + json.dumps(dict(type="response.completed", response=dict(output=[
            dict(type="function_call", name="Bash", call_id="bash-a", arguments="PRIVATE_SENTINEL")]))) + "\n\n").encode()
        later_request = json.dumps(dict(input=[dict(type="function_call_output", call_id="bash-a",
            output="PRIVATE_SENTINEL\n[exit code: 7]\n")])).encode()
        root = self.write(records=[
            dict(call("1"), response_sha256=hashlib.sha256(first_response).hexdigest()),
            dict(call("2"), upstream_attempted=True, body_sha256=hashlib.sha256(later_request).hexdigest()),
        ])
        wire = root / "wire/sample-a"
        (wire / "1.response.sse").write_bytes(first_response)
        (wire / "2.request.json").write_bytes(later_request)
        agent = root / "trials/sample-a/agent"
        agent.mkdir(parents=True)
        rows = [
            dict(kind="tool_start", tool=dict(tool_call_id="bash-a", tool_name="Bash")),
            dict(kind="tool_terminal", tool=dict(tool_call_id="bash-a", outcome="completed")),
        ]
        (agent / "yhc.jsonl").write_text("".join(
            json.dumps(dict(schema_version=1, type="event", event=item)) + "\n" for item in rows))

        report = build_report([root])
        self.assertEqual(report["samples"][0]["tool_feedback"]["bash_exit_markers"],
                         dict(text_returns=1, nonzero=1, native_completed_nonzero=1))
        rendered = markdown(report)
        self.assertIn("Bash text returns | Nonzero exit marker | Also native completed", rendered)
        self.assertIn("| yhc | sample-a | 1 | 1 | 1 |", rendered)
        self.assertNotIn("PRIVATE_SENTINEL", rendered)

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

    def test_reference_out_of_range_thinking_keeps_independent_usage(self):
        trace = reference_trace()
        trace["steps"][1]["metrics"]["extra"]["output_tokens_details"] = {"thinking_tokens": 9}
        report = build_report([], reference_trajectories=[self.reference(trace)])
        got = report["reference_trajectories"][0]
        self.assertEqual((got["model_calls"], got["input_tokens"], got["output_tokens"]), (2, 400, 20))
        self.assertTrue(got["usage_complete"])
        self.assertEqual(got["cache_hit_rate_known_including_first"], 0.825)
        self.assertEqual(got["thinking"], dict(known_tokens=6, reported_steps=1, total_steps=2,
                                               complete=False, out_of_range_steps=1))
        self.assertIn("1/2 (1 out of range)", markdown(report))

        trace["steps"][2]["metrics"]["extra"]["output_tokens_details"] = {"reasoning_tokens": 13}
        got = build_report([], reference_trajectories=[self.reference(trace)])["reference_trajectories"][0]
        self.assertEqual(got["thinking"], dict(known_tokens=None, reported_steps=0, total_steps=2,
                                               complete=False, out_of_range_steps=2))

    def test_reference_copied_context_and_deterministic_steps_are_not_new_calls(self):
        trace = reference_trace()
        trace["steps"].append(dict(trace["steps"][1], step_id=4, is_copied_context=True))
        trace["steps"].append(dict(step_id=5, source="agent", llm_call_count=0))
        got = build_report([], reference_trajectories=[self.reference(trace)])["reference_trajectories"][0]
        self.assertEqual((got["model_calls"], got["input_tokens"]), (2, 400))
        trace["steps"][2].pop("llm_call_count")
        got = build_report([], reference_trajectories=[self.reference(trace)])["reference_trajectories"][0]
        self.assertIsNone(got["model_calls"])

    def test_claude_synthetic_reference_message_is_separate_from_provider_steps(self):
        trace = reference_trace()
        trace["agent"]["name"] = "claude-code"
        placeholder = dict(step_id=4, source="agent", model_name="<synthetic>", llm_call_count=1,
            message="PRIVATE_SENTINEL", metrics=dict(prompt_tokens=0, cached_tokens=0, completion_tokens=0,
                extra=dict(output_tokens_details=None, server_tool_use={"web_fetch_requests": 0})))
        trace["steps"].append(placeholder)
        report = build_report([], reference_trajectories=[self.reference(trace)])
        got = report["reference_trajectories"][0]
        self.assertEqual(got["synthetic_agent_steps"], 1)
        self.assertEqual((got["model_calls"], got["input_tokens"], got["output_tokens"]), (2, 400, 20))
        self.assertEqual(got["models"], ["reference-model"])
        self.assertEqual(got["thinking"], dict(known_tokens=10, reported_steps=2, total_steps=2, complete=True))
        self.assertIn("1 Claude Code synthetic", markdown(report))
        self.assertNotIn("PRIVATE_SENTINEL", json.dumps(report))

        for invalid in (dict(completion_tokens=1), dict(prompt_tokens=True),
                        dict(extra=dict(output_tokens_details={"reasoning_tokens": 1})),
                        dict(extra=dict(server_tool_use={"web_fetch_requests": 1}))):
            trace["steps"][-1] = dict(placeholder, metrics=dict(placeholder["metrics"], **invalid))
            with self.subTest(invalid=invalid), self.assertRaises(ValueError):
                build_report([], reference_trajectories=[self.reference(trace)])

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
        for change in (dict(thinking_tokens=True), dict(thinking_tokens=-1),
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
