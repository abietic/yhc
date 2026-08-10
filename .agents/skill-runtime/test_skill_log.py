#!/usr/bin/env python3

from __future__ import annotations

import argparse
import json
import os
from pathlib import Path
import stat
import subprocess
import tempfile
import threading
import unittest
from unittest import mock

import skill_log
import validate_skills


class SkillLogTest(unittest.TestCase):
    def delegate_args(self, root: Path, path: Path, **overrides: object) -> argparse.Namespace:
        values: dict[str, object] = {
            "log_file": path, "log_dir": root, "run_id": "run-1", "executor": "terra_worker",
            "task_kind": "focused-test", "task_family": "implementation",
            "mode": "blocking", "scope": ["tools/"],
            "child_task_id": "child-1", "child_session_id": "session-1", "delegation_id": "",
            "outcome": "success", "patch_sha256": "abc", "changed_lines": 3,
            "duration_seconds": 1, "input_tokens": 2, "output_tokens": 3,
            "cached_input_tokens": 4, "reasoning_tokens": 5, "total_tokens": 14,
            "disposition": "accepted", "reason_category": "", "parent_wait_seconds": 1,
            "review_seconds": 2, "rework_seconds": 3, "commit": "abc", "pr": "42",
            "task_wall_seconds": None, "parent_total_tokens": None, "baseline_kind": None,
            "baseline_wall_seconds": None, "baseline_parent_total_tokens": None,
            "baseline_ref": "",
        }
        values.update(overrides)
        return argparse.Namespace(**values)

    def report(self, root: Path, **overrides: object) -> dict[str, object]:
        args = {
            "log_dir": root,
            "skill": None,
            "since": None,
            "experiment_id": None,
            "stale_after_seconds": 86400,
            "profile": "full",
            "format": "json",
        }
        args.update(overrides)
        with mock.patch("sys.stdout") as output:
            skill_log.cmd_report(argparse.Namespace(**args))
        return json.loads("".join(call.args[0] for call in output.write.call_args_list))

    def write_measured_run(
        self,
        root: Path,
        *,
        run_id: str,
        measurement_mode: str,
        experiment_id: str = "",
        task_family: str = "review",
    ) -> None:
        directory = root / "safe-skill"
        directory.mkdir(exist_ok=True)
        path = directory / f"{run_id}.jsonl"
        timestamps = (
            "2026-01-01T00:00:00+00:00",
            "2026-01-01T00:00:01+00:00",
            "2026-01-01T00:00:03+00:00",
            "2026-01-01T00:00:04+00:00",
            "2026-01-01T00:00:05+00:00",
        )
        events = [
            {
                "schema_version": 2,
                "timestamp": timestamps[0],
                "run_id": run_id,
                "event": "run_started",
                "skill": "safe-skill",
                "kind": f"kind-{run_id}",
                "measurement_mode": measurement_mode,
                "experiment_id": experiment_id,
                "retention_keep": 2000,
            },
            {
                "schema_version": 2,
                "timestamp": timestamps[1],
                "run_id": run_id,
                "event": "delegation_started",
                "delegation_id": f"delegate-{run_id}",
                "executor": "terra_reviewer",
                "task_kind": f"task-{run_id}",
                "task_family": task_family,
                "mode": "parallel",
            },
            {
                "schema_version": 2,
                "timestamp": timestamps[2],
                "run_id": run_id,
                "event": "delegation_finished",
                "delegation_id": f"delegate-{run_id}",
                "outcome": "success",
                "duration_seconds": 2,
                "total_tokens": 10,
            },
            {
                "schema_version": 2,
                "timestamp": timestamps[3],
                "run_id": run_id,
                "event": "delegation_assessed",
                "delegation_id": f"delegate-{run_id}",
                "disposition": "accepted",
                "parent_wait_seconds": 1,
                "review_seconds": 1,
                "rework_seconds": 0,
                "task_wall_seconds": 4,
                "parent_total_tokens": 20,
                "baseline_kind": "parallel-control",
                "baseline_wall_seconds": 8,
                "baseline_parent_total_tokens": 30,
                "baseline_ref": f"control-{run_id}",
            },
            {
                "schema_version": 2,
                "timestamp": timestamps[4],
                "run_id": run_id,
                "event": "run_finished",
                "outcome": "success",
                "category": f"category-{run_id}",
                "wall_seconds": 5,
            },
        ]
        path.write_text(
            "".join(json.dumps(event) + "\n" for event in events),
            encoding="utf-8",
        )

    def test_append_is_private_and_redacted(self) -> None:
        with tempfile.TemporaryDirectory() as temp:
            path = Path(temp) / "run.jsonl"
            skill_log.append_event(
                path,
                "run-1",
                "test",
                token="visible-token",
                detail="api_key=visible-key Authorization: Bearer visible-auth safe",
            )

            mode = stat.S_IMODE(path.stat().st_mode)
            self.assertEqual(mode, 0o600)
            content = path.read_text(encoding="utf-8")
            self.assertNotIn("visible-token", content)
            self.assertNotIn("visible-key", content)
            self.assertNotIn("visible-auth", content)
            payload = json.loads(content)
            self.assertEqual(payload["token"], "[REDACTED]")

    def test_redact_hides_flag_values(self) -> None:
        redacted = skill_log.redact(["--token", "visible-token", "--mode", "safe"])
        self.assertEqual(redacted, ["--token", "[REDACTED]", "--mode", "safe"])
        self.assertNotIn("visible-cookie", skill_log.redact("Cookie: visible-cookie"))
        self.assertEqual(skill_log.redact(2, "input_tokens"), 2)
        self.assertEqual(skill_log.redact("2", "input_tokens"), "[REDACTED]")

    def test_skill_and_event_names_are_restricted(self) -> None:
        skill_log.validate_skill_name("migration-slice")
        with self.assertRaises(ValueError):
            skill_log.validate_skill_name("../../outside")
        with tempfile.TemporaryDirectory() as temp:
            root = Path(temp)
            path = root / "run.jsonl"
            skill_log.append_event(path, "run-1", "run_started")
            with self.assertRaises(ValueError):
                skill_log.append_existing_event(
                    path,
                    root,
                    "run-1",
                    "run_finished",
                )

    def test_log_path_must_stay_under_root(self) -> None:
        with tempfile.TemporaryDirectory() as temp:
            base = Path(temp)
            root = base / "logs"
            root.mkdir()
            outside = base / "outside.jsonl"
            outside.write_text("{}\n", encoding="utf-8")
            with self.assertRaises(ValueError):
                skill_log.ensure_log_boundary(outside, root)

    def test_git_snapshot_preserves_first_status_path(self) -> None:
        with tempfile.TemporaryDirectory() as temp:
            root = Path(temp)
            subprocess.run(["git", "init", "-q"], cwd=root, check=True)
            tracked = root / "Makefile"
            tracked.write_text("first\n", encoding="utf-8")
            subprocess.run(["git", "add", "Makefile"], cwd=root, check=True)
            subprocess.run(
                [
                    "git",
                    "-c",
                    "user.name=Skill Test",
                    "-c",
                    "user.email=skill-test@example.invalid",
                    "commit",
                    "-qm",
                    "baseline",
                ],
                cwd=root,
                check=True,
            )
            tracked.write_text("second\n", encoding="utf-8")
            self.assertEqual(skill_log.git_snapshot(root)["dirty_paths"], ["Makefile"])

    def test_parse_data_requires_object(self) -> None:
        self.assertEqual(skill_log.parse_data('{"gate":"pass"}'), {"gate": "pass"})
        with self.assertRaises(ValueError):
            skill_log.parse_data("[]")
        with self.assertRaises(ValueError):
            skill_log.parse_data('{"message":"raw prompt"}')
        with self.assertRaises(ValueError):
            skill_log.parse_data('{"status":"raw prompt"}')
        with self.assertRaises(ValueError):
            skill_log.parse_data('{"observed_metrics":{"tokens":1e999}}')
        self.assertEqual(
            skill_log.parse_data('{"gate_results":{"make_test":"pass"}}'),
            {"gate_results": {"make_test": "pass"}},
        )
        self.assertEqual(
            skill_log.redact({"message": "raw prompt"}),
            {"message": "[REDACTED_TEXT]"},
        )
        self.assertEqual(
            skill_log.redact(10**1000, "total_tokens"),
            "[REDACTED]",
        )

    def test_validate_open_log_rejects_wrong_or_finished_run(self) -> None:
        with tempfile.TemporaryDirectory() as temp:
            path = Path(temp) / "run.jsonl"
            skill_log.append_event(path, "run-1", "run_started")
            skill_log.validate_open_log(path, "run-1")
            with self.assertRaises(ValueError):
                skill_log.validate_open_log(path, "run-2")
            skill_log.append_event(path, "run-1", "run_finished")
            with self.assertRaises(ValueError):
                skill_log.validate_open_log(path, "run-1")

    def test_finish_writes_terminal_event_once(self) -> None:
        with tempfile.TemporaryDirectory() as temp:
            root = Path(temp)
            path = root / "run.jsonl"
            skill_log.append_event(path, "run-1", "run_started")
            args = argparse.Namespace(
                log_file=path,
                log_dir=root,
                run_id="run-1",
                outcome="success",
                category="validated",
                summary="done",
                data_json='{"gate":"pass"}',
                cwd=Path.cwd(),
            )
            self.assertEqual(skill_log.cmd_finish(args), 0)
            events = [
                json.loads(line)["event"]
                for line in path.read_text(encoding="utf-8").splitlines()
            ]
            self.assertEqual(events, ["run_started", "run_finished"])
            terminal = json.loads(path.read_text().splitlines()[-1])
            self.assertGreaterEqual(terminal["wall_seconds"], 0)
            self.assertEqual(terminal["summary"], "[REDACTED_TEXT]")
            with self.assertRaises(ValueError):
                skill_log.cmd_finish(args)

    def test_finish_is_atomic_under_concurrency(self) -> None:
        with tempfile.TemporaryDirectory() as temp:
            root = Path(temp)
            path = root / "run.jsonl"
            skill_log.append_event(path, "run-1", "run_started")
            args = argparse.Namespace(
                log_file=path,
                log_dir=root,
                run_id="run-1",
                outcome="success",
                category="validated",
                summary="raw prompt",
                data_json=None,
                cwd=Path.cwd(),
            )
            results: list[bool] = []

            def finish() -> None:
                try:
                    skill_log.cmd_finish(args)
                    results.append(True)
                except ValueError:
                    results.append(False)

            threads = [threading.Thread(target=finish) for _ in range(2)]
            for thread in threads:
                thread.start()
            for thread in threads:
                thread.join()

            events = [
                json.loads(line)["event"]
                for line in path.read_text(encoding="utf-8").splitlines()
            ]
            self.assertEqual(results.count(True), 1)
            self.assertEqual(events, ["run_started", "run_finished"])

    def test_start_correlation_uses_environment_defaults(self) -> None:
        with tempfile.TemporaryDirectory() as temp, mock.patch.dict(os.environ, {"CODEX_SESSION_ID": "session"}):
            root = Path(temp)
            args = argparse.Namespace(
                skill="safe-skill", kind="test", scope=[], parent_run_id="",
                root_run_id="", invoked_by="test", measurement_mode="audit",
                experiment_id="", cwd=root, log_dir=root, keep=1, session_id="",
                thread_id="", turn_id="", goal_id="", task_id="",
            )
            skill_log.cmd_start(args)
            event = json.loads(next((root / "safe-skill").glob("*.jsonl")).read_text())
            self.assertEqual(event["session_id"], "session")
            self.assertEqual(event["schema_version"], 2)
            self.assertEqual(event["retention_keep"], 1)
            self.assertEqual(event["measurement_mode"], "audit")
            self.assertEqual(event["experiment_id"], "")
            args.scope = ["raw output"]
            with self.assertRaises(ValueError):
                skill_log.cmd_start(args)
        self.assertEqual(skill_log.DEFAULT_KEEP, 2000)

    def test_start_requires_explicit_roi_experiment(self) -> None:
        with tempfile.TemporaryDirectory() as temp:
            root = Path(temp)
            args = argparse.Namespace(
                skill="safe-skill", kind="test", scope=[], parent_run_id="",
                root_run_id="", invoked_by="test", measurement_mode="roi",
                experiment_id="", cwd=root, log_dir=root, keep=10,
                session_id="", thread_id="", turn_id="", goal_id="", task_id="",
            )
            with self.assertRaises(ValueError):
                skill_log.cmd_start(args)

            args.experiment_id = "roi-1"
            self.assertEqual(skill_log.cmd_start(args), 0)
            event = json.loads(next((root / "safe-skill").glob("*.jsonl")).read_text())
            self.assertEqual(event["measurement_mode"], "roi")
            self.assertEqual(event["experiment_id"], "roi-1")

            args.measurement_mode = "audit"
            with self.assertRaises(ValueError):
                skill_log.cmd_start(args)

    def test_delegation_lifecycle_and_assessment_after_finish(self) -> None:
        with tempfile.TemporaryDirectory() as temp:
            root, path = Path(temp), Path(temp) / "run.jsonl"
            skill_log.append_event(path, "run-1", "run_started")
            started = self.delegate_args(root, path)
            skill_log.cmd_delegate_start(started)
            delegation_id = json.loads(path.read_text().splitlines()[-1])["delegation_id"]
            finished = self.delegate_args(root, path, delegation_id=delegation_id)
            with self.assertRaises(ValueError): skill_log.cmd_delegate_assess(self.delegate_args(root, path, delegation_id=delegation_id))
            self.assertEqual(skill_log.cmd_delegate_finish(finished), 0)
            with self.assertRaises(ValueError): skill_log.cmd_delegate_finish(finished)
            finish = argparse.Namespace(log_file=path, log_dir=root, run_id="run-1", outcome="success", category="", summary="", data_json=None, cwd=root)
            skill_log.cmd_finish(finish)
            assessed = self.delegate_args(root, path, delegation_id=delegation_id)
            self.assertEqual(skill_log.cmd_delegate_assess(assessed), 0)
            with self.assertRaises(ValueError): skill_log.cmd_event(argparse.Namespace(log_file=path, log_dir=root, run_id="run-1", name="late", status="info", data_json=None))
            with self.assertRaises(ValueError): skill_log.cmd_delegate_start(self.delegate_args(root, path))

    def test_delegation_rejects_unknown_and_invalid_metrics(self) -> None:
        with tempfile.TemporaryDirectory() as temp:
            root, path = Path(temp), Path(temp) / "run.jsonl"
            skill_log.append_event(path, "run-1", "run_started")
            with self.assertRaises(ValueError): skill_log.cmd_delegate_finish(self.delegate_args(root, path, delegation_id="missing"))
            skill_log.cmd_delegate_start(self.delegate_args(root, path))
            delegation_id = json.loads(path.read_text().splitlines()[-1])["delegation_id"]
            with self.assertRaises(ValueError): skill_log.cmd_delegate_finish(self.delegate_args(root, path, delegation_id=delegation_id, duration_seconds="nan"))
            with self.assertRaises(ValueError): skill_log.cmd_delegate_finish(self.delegate_args(root, path, delegation_id=delegation_id, input_tokens=1.5))

    def test_delegate_finish_is_atomic_and_log_ids_must_match(self) -> None:
        with tempfile.TemporaryDirectory() as temp:
            root, path = Path(temp), Path(temp) / "run.jsonl"
            skill_log.append_event(path, "run-1", "run_started")
            skill_log.cmd_delegate_start(self.delegate_args(root, path))
            delegation_id = json.loads(path.read_text().splitlines()[-1])["delegation_id"]
            args = self.delegate_args(root, path, delegation_id=delegation_id)
            results: list[bool] = []
            threads = [threading.Thread(target=lambda: results.append(self._finish_ok(args))) for _ in range(2)]
            for thread in threads: thread.start()
            for thread in threads: thread.join()
            self.assertEqual(results.count(True), 1)
            path.write_text(path.read_text() + json.dumps({"run_id": "other", "event": "info"}) + "\n")
            with self.assertRaises(ValueError): skill_log.validate_open_log(path, "run-1")

    @staticmethod
    def _finish_ok(args: argparse.Namespace) -> bool:
        try:
            skill_log.cmd_delegate_finish(args)
            return True
        except ValueError:
            return False

    def test_report_legacy_stale_latest_and_baselines(self) -> None:
        with tempfile.TemporaryDirectory() as temp:
            root, directory = Path(temp), Path(temp) / "safe-skill"
            directory.mkdir()
            path = directory / "run.jsonl"
            old = "2000-01-01T00:00:00+00:00"
            events = [
                {"schema_version": 1, "timestamp": old, "run_id": "run-1", "event": "run_started", "skill": "safe-skill"},
                {"schema_version": 1, "timestamp": old, "run_id": "run-1", "event": "delegation_started"},
                {"schema_version": 1, "timestamp": old, "run_id": "run-1", "event": "delegation_finished"},
                {"schema_version": 1, "timestamp": old, "run_id": "run-1", "event": "delegation_finished", "delegation_id": "bad", "outcome": []},
            ]
            path.write_text("".join(json.dumps(event) + "\n" for event in events), encoding="utf-8")
            report = self.report(root, stale_after_seconds=1)
            self.assertEqual(report["delegations"]["legacy_unlinked_events"], 3)
            self.assertEqual(report["delegations"]["open"], 0)
            self.assertEqual(report["runs"]["stale_open"], 1)
            self.assertEqual(self.report(root, since="2000-01-01")["runs"]["total"], 1)

            paired = directory / "paired.jsonl"
            paired.write_text("".join(json.dumps(event) + "\n" for event in [
                {"schema_version": 1, "timestamp": "2000-01-02T00:00:00+00:00", "run_id": "run-2", "event": "run_started", "skill": "safe-skill"},
                {"schema_version": 1, "timestamp": "2000-01-02T00:00:02+00:00", "run_id": "run-2", "event": "delegation_started", "delegation_id": "paired", "executor": "other"},
                {"schema_version": 1, "timestamp": "2000-01-02T00:00:05+00:00", "run_id": "run-2", "event": "delegation_finished", "delegation_id": "paired", "outcome": "success"},
                {"schema_version": 1, "timestamp": "2000-01-02T00:00:10+00:00", "run_id": "run-2", "event": "run_finished", "outcome": "success"},
            ]), encoding="utf-8")
            derived = self.report(root)
            self.assertEqual(derived["runs"]["wall_seconds"]["p50"], 10)
            self.assertIsNone(derived["delegations"]["duration_seconds"]["p50"])
            self.assertEqual(derived["delegations"]["legacy_unlinked_events"], 5)

            path.unlink()
            paired.unlink()
            skill_log.append_event(
                path,
                "run-1",
                "run_started",
                skill="safe-skill",
                measurement_mode="roi",
                experiment_id="roi-legacy-test",
            )
            skill_log.cmd_delegate_start(self.delegate_args(root, path))
            delegation_id = json.loads(path.read_text().splitlines()[-1])["delegation_id"]
            skill_log.cmd_delegate_finish(self.delegate_args(root, path, delegation_id=delegation_id))
            baseline = self.delegate_args(root, path, delegation_id=delegation_id, task_wall_seconds=7, parent_total_tokens=10, baseline_kind="parallel-control", baseline_wall_seconds=11, baseline_parent_total_tokens=16, baseline_ref="control-1")
            skill_log.cmd_delegate_assess(baseline)
            skill_log.cmd_delegate_assess(self.delegate_args(root, path, delegation_id=delegation_id, disposition="rejected", task_wall_seconds=7, parent_total_tokens=10, baseline_kind="parallel-control", baseline_wall_seconds=11, baseline_parent_total_tokens=16, baseline_ref="control-2"))
            skill_log.cmd_finish(argparse.Namespace(
                log_file=path, log_dir=root, run_id="run-1", outcome="success",
                category="", summary="", data_json=None, cwd=root,
            ))
            records = [json.loads(line) for line in path.read_text().splitlines()]
            for record in records:
                if record["event"] == "delegation_finished":
                    record.pop("cached_input_tokens")
                    record.pop("reasoning_tokens")
                    record.pop("output_tokens")
            path.write_text("".join(json.dumps(record) + "\n" for record in records), encoding="utf-8")
            report = self.report(root)
            self.assertEqual(report["delegations"]["dispositions"], {"rejected": 1})
            self.assertTrue(report["comparison_ready"])
            self.assertTrue(report["benefit_roi_ready"])
            self.assertFalse(report["roi_ready"])
            self.assertLess(report["data_quality"]["cached_input_tokens"]["percent"], 100)
            self.assertFalse(report["cost_roi_ready"])
            self.assertEqual(report["measured_comparisons"]["wall_seconds_delta"], 4)
            self.assertEqual(report["measured_comparisons"]["parent_total_tokens_delta"], 6)
            with self.assertRaises(ValueError): skill_log.cmd_delegate_assess(self.delegate_args(root, path, delegation_id=delegation_id, baseline_kind="parallel-control"))

    def test_report_handles_legacy_metrics_and_markdown(self) -> None:
        with tempfile.TemporaryDirectory() as temp:
            root, directory = Path(temp), Path(temp) / "safe-skill"
            directory.mkdir()
            path = directory / "legacy.jsonl"
            skill_log.append_event(path, "run-1", "run_started", schema_version=1, skill="safe-skill")
            start = self.delegate_args(root, path)
            skill_log.cmd_delegate_start(start)
            delegation_id = json.loads(path.read_text().splitlines()[-1])["delegation_id"]
            skill_log.cmd_delegate_finish(self.delegate_args(root, path, delegation_id=delegation_id))
            skill_log.cmd_delegate_assess(self.delegate_args(root, path, delegation_id=delegation_id))
            finish = argparse.Namespace(log_file=path, log_dir=root, run_id="run-1", outcome="success", category="", summary="", data_json=None, cwd=root)
            skill_log.cmd_finish(finish)
            (directory / "bad.jsonl").write_text("not-json\n", encoding="utf-8")
            with mock.patch("sys.stdout") as output:
                skill_log.cmd_report(argparse.Namespace(log_dir=root, skill=None, since=None, stale_after_seconds=86400, format="json"))
                report = json.loads("".join(call.args[0] for call in output.write.call_args_list))
            self.assertEqual(report["runs"]["success"], 1)
            self.assertEqual(report["delegations"]["finishes"], 1)
            self.assertFalse(report["roi_ready"])
            with mock.patch("sys.stdout") as output:
                skill_log.cmd_report(argparse.Namespace(log_dir=root, skill=None, since=None, stale_after_seconds=86400, format="markdown"))
                self.assertIn("# Skill telemetry report", "".join(call.args[0] for call in output.write.call_args_list))

    def test_report_isolates_runs_and_defensively_validates_legacy_data(self) -> None:
        with tempfile.TemporaryDirectory() as temp:
            root, directory = Path(temp), Path(temp) / "safe-skill"
            directory.mkdir()
            timestamp = "2026-01-01T00:00:00+00:00"
            def write(name: str, run_id: str, executor: str) -> None:
                events = [
                    {"timestamp": timestamp, "run_id": run_id, "event": "run_started", "skill": "safe-skill"},
                    {"schema_version": 2, "timestamp": timestamp, "run_id": run_id, "event": "delegation_started", "delegation_id": "same", "executor": executor, "task_kind": "focused-test"},
                    {"schema_version": 2, "timestamp": timestamp, "run_id": run_id, "event": "delegation_finished", "delegation_id": "same", "outcome": "success", "total_tokens": 1},
                ]
                (directory / name).write_text("".join(json.dumps(event) + "\n" for event in events), encoding="utf-8")
            write("one.jsonl", "run-1", "terra_worker")
            write("two.jsonl", "run-2", "other")
            (directory / "mixed.jsonl").write_text("\n".join([
                json.dumps({"timestamp": timestamp, "run_id": "run-3", "event": "run_started", "skill": "safe-skill"}),
                json.dumps({"timestamp": timestamp, "run_id": "other", "event": "delegation_started", "delegation_id": "x"}),
            ]) + "\n", encoding="utf-8")
            (directory / "duplicate.jsonl").write_text("\n".join([
                json.dumps({"timestamp": timestamp, "run_id": "run-4", "event": "run_started", "skill": "safe-skill"}),
                json.dumps({"schema_version": 2, "timestamp": timestamp, "run_id": "run-4", "event": "delegation_started", "delegation_id": "x", "executor": "other", "task_kind": "focused-test"}),
                json.dumps({"schema_version": 2, "timestamp": timestamp, "run_id": "run-4", "event": "delegation_started", "delegation_id": "x", "executor": "other", "task_kind": "focused-test"}),
                json.dumps({"schema_version": 2, "timestamp": timestamp, "run_id": "run-4", "event": "delegation_finished", "delegation_id": "x", "outcome": "success", "total_tokens": 1}),
                json.dumps({"schema_version": 2, "timestamp": timestamp, "run_id": "run-4", "event": "delegation_finished", "delegation_id": "x", "outcome": "success", "total_tokens": 1}),
            ]) + "\n", encoding="utf-8")
            report = self.report(root)
            self.assertEqual(report["delegations"]["finishes"], 3)
            self.assertEqual(report["delegations"]["by_executor"]["terra_worker"]["finished"], 1)
            self.assertEqual(report["diagnostics"]["mixed_run_id_files"], 1)
            self.assertEqual(report["diagnostics"]["duplicate_delegation_events"], 2)

    def test_report_baseline_strings_do_not_crash_or_enable_roi(self) -> None:
        with tempfile.TemporaryDirectory() as temp:
            root, directory = Path(temp), Path(temp) / "safe-skill"
            directory.mkdir()
            timestamp = "2026-01-01T00:00:00+00:00"
            events = [
                {"timestamp": timestamp, "run_id": "run", "event": "run_started", "skill": "safe-skill"},
                {"schema_version": 2, "timestamp": timestamp, "run_id": "run", "event": "delegation_started", "delegation_id": "id", "executor": "other", "task_kind": "focused-test"},
                {"schema_version": 2, "timestamp": timestamp, "run_id": "run", "event": "delegation_finished", "delegation_id": "id", "outcome": "success", "total_tokens": 1},
                {"schema_version": 2, "timestamp": timestamp, "run_id": "run", "event": "delegation_assessed", "delegation_id": "id", "parent_wait_seconds": 1, "review_seconds": 1, "rework_seconds": 1, "baseline_kind": "historical", "baseline_ref": "bad", "baseline_wall_seconds": "nan", "baseline_parent_total_tokens": "x", "task_wall_seconds": "1", "parent_total_tokens": "1"},
            ]
            (directory / "bad.jsonl").write_text("".join(json.dumps(event) + "\n" for event in events), encoding="utf-8")
            report = self.report(root)
            self.assertEqual(report["measured_comparisons"]["count"], 0)
            self.assertFalse(report["roi_ready"])

    def test_manual_estimate_is_not_reported_as_measured_roi(self) -> None:
        with tempfile.TemporaryDirectory() as temp:
            root = Path(temp)
            directory = root / "safe-skill"
            directory.mkdir()
            path = directory / "run.jsonl"
            skill_log.append_event(
                path,
                "run-1",
                "run_started",
                skill="safe-skill",
                measurement_mode="roi",
                experiment_id="roi-estimate-test",
            )
            skill_log.cmd_delegate_start(self.delegate_args(root, path))
            delegation_id = json.loads(path.read_text().splitlines()[-1])[
                "delegation_id"
            ]
            skill_log.cmd_delegate_finish(
                self.delegate_args(root, path, delegation_id=delegation_id)
            )
            skill_log.cmd_delegate_assess(
                self.delegate_args(
                    root,
                    path,
                    delegation_id=delegation_id,
                    task_wall_seconds=7,
                    parent_total_tokens=10,
                    baseline_kind="manual-estimate",
                    baseline_wall_seconds=11,
                    baseline_parent_total_tokens=16,
                    baseline_ref="estimate-1",
                )
            )
            skill_log.cmd_finish(argparse.Namespace(
                log_file=path, log_dir=root, run_id="run-1", outcome="success",
                category="", summary="", data_json=None, cwd=root,
            ))

            report = self.report(root)
            self.assertEqual(report["measured_comparisons"]["count"], 0)
            self.assertEqual(report["estimated_comparisons"]["count"], 1)
            self.assertTrue(report["comparison_ready"])
            self.assertFalse(report["benefit_roi_ready"])
            self.assertFalse(report["roi_ready"])

    def test_report_profiles_use_stable_dimensions(self) -> None:
        with tempfile.TemporaryDirectory() as temp:
            root = Path(temp)
            self.write_measured_run(
                root,
                run_id="audit",
                measurement_mode="audit",
            )

            operational = self.report(root, profile="operational")
            self.assertEqual(operational["profile"], "operational")
            self.assertEqual(operational["runs"]["by_skill"]["safe-skill"]["total"], 1)
            self.assertNotIn("by_kind", operational["runs"])
            self.assertNotIn("categories", operational["runs"])
            self.assertNotIn("delegations", operational)

            delegation = self.report(root, profile="delegation")
            self.assertEqual(delegation["profile"], "delegation")
            self.assertEqual(
                delegation["delegations"]["by_task_family"]["review"]["finished"],
                1,
            )
            self.assertNotIn("by_task_kind", delegation["delegations"])

            full = self.report(root, profile="full")
            self.assertEqual(full["profile"], "full")
            self.assertIn("by_kind", full["runs"])
            self.assertIn("by_task_kind", full["delegations"])

    def test_report_normalizes_legacy_project_skill_names(self) -> None:
        with tempfile.TemporaryDirectory() as temp:
            root = Path(temp)
            for index, skill in enumerate(
                ("eino-runtime-depth-change", "runtime-depth-change")
            ):
                directory = root / skill
                directory.mkdir()
                run_id = f"run-{index}"
                events = [
                    {
                        "schema_version": 2,
                        "timestamp": "2026-01-01T00:00:00+00:00",
                        "run_id": run_id,
                        "event": "run_started",
                        "skill": skill,
                        "kind": "runtime-depth-change",
                    },
                    {
                        "schema_version": 2,
                        "timestamp": "2026-01-01T00:00:01+00:00",
                        "run_id": run_id,
                        "event": "run_finished",
                        "outcome": "success",
                    },
                ]
                (directory / f"{run_id}.jsonl").write_text(
                    "".join(json.dumps(event) + "\n" for event in events),
                    encoding="utf-8",
                )

            report = self.report(
                root,
                profile="operational",
                skill="runtime-depth-change",
            )
            self.assertEqual(report["runs"]["total"], 2)
            self.assertEqual(
                report["runs"]["by_skill"]["runtime-depth-change"]["total"],
                2,
            )
            self.assertNotIn("eino-runtime-depth-change", report["runs"]["by_skill"])

    def test_roi_requires_explicit_experiment_cohort(self) -> None:
        with tempfile.TemporaryDirectory() as temp:
            root = Path(temp)
            self.write_measured_run(
                root,
                run_id="audit",
                measurement_mode="audit",
            )

            full = self.report(root, profile="full")
            self.assertEqual(full["roi"]["cohort"]["delegations"], 0)
            self.assertEqual(full["measured_comparisons"]["count"], 0)
            self.assertFalse(full["comparison_ready"])
            self.assertIn("no_roi_cohort", full["roi"]["blockers"])

            self.write_measured_run(
                root,
                run_id="roi",
                measurement_mode="roi",
                experiment_id="roi-1",
            )
            roi = self.report(root, profile="roi", experiment_id="roi-1")
            self.assertEqual(roi["profile"], "roi")
            self.assertEqual(roi["roi"]["cohort"]["runs"], 1)
            self.assertEqual(roi["roi"]["cohort"]["delegations"], 1)
            self.assertEqual(roi["roi"]["measured_comparisons"]["count"], 1)
            self.assertTrue(roi["roi"]["comparison_ready"])
            self.assertTrue(roi["roi"]["benefit_roi_ready"])
            self.assertFalse(roi["roi"]["cost_roi_ready"])
            self.assertFalse(roi["roi"]["roi_ready"])
            self.assertEqual(roi["roi"]["blockers"], ["cost_model_unavailable"])

    def test_roi_profile_rejects_unfiltered_multi_experiment_report(self) -> None:
        with tempfile.TemporaryDirectory() as temp:
            root = Path(temp)
            self.write_measured_run(
                root,
                run_id="roi-a",
                measurement_mode="roi",
                experiment_id="experiment-a",
            )
            self.write_measured_run(
                root,
                run_id="roi-b",
                measurement_mode="roi",
                experiment_id="experiment-b",
            )

            with self.assertRaisesRegex(ValueError, "requires --experiment-id"):
                self.report(root, profile="roi")

            full = self.report(root, profile="full")
            self.assertFalse(full["comparison_ready"])
            self.assertFalse(full["benefit_roi_ready"])
            self.assertIn("multiple_experiment_ids", full["roi"]["blockers"])

            filtered = self.report(
                root,
                profile="roi",
                experiment_id="experiment-a",
            )
            self.assertTrue(filtered["roi"]["comparison_ready"])
            self.assertTrue(filtered["roi"]["benefit_roi_ready"])

    def test_open_roi_root_run_blocks_benefit_readiness(self) -> None:
        with tempfile.TemporaryDirectory() as temp:
            root = Path(temp)
            self.write_measured_run(
                root,
                run_id="roi-open",
                measurement_mode="roi",
                experiment_id="experiment-open",
            )
            path = root / "safe-skill" / "roi-open.jsonl"
            events = [
                json.loads(line) for line in path.read_text(encoding="utf-8").splitlines()
            ]
            path.write_text(
                "".join(
                    json.dumps(event) + "\n"
                    for event in events
                    if event["event"] != "run_finished"
                ),
                encoding="utf-8",
            )

            report = self.report(
                root,
                profile="roi",
                experiment_id="experiment-open",
            )
            self.assertFalse(report["roi"]["comparison_ready"])
            self.assertFalse(report["roi"]["benefit_roi_ready"])
            self.assertIn("open_roi_runs", report["roi"]["blockers"])

    def test_cli_defaults_to_operational_report_and_audit_measurement(self) -> None:
        parser = skill_log.build_parser()
        report = parser.parse_args(["report"])
        self.assertEqual(report.profile, "operational")
        start = parser.parse_args(
            ["start", "--skill", "safe-skill", "--kind", "test"]
        )
        self.assertEqual(start.measurement_mode, "audit")
        self.assertEqual(start.experiment_id, "")

    def test_report_filters_nested_logs_and_reports_safe_run_metrics(self) -> None:
        with tempfile.TemporaryDirectory() as temp:
            root = Path(temp)
            closeout = root / "closeout" / "nested"
            closeout.mkdir(parents=True)
            for index in range(200):
                started = {
                    "schema_version": 2,
                    "timestamp": "2026-01-01T00:00:00+00:00",
                    "run_id": f"run-{index}",
                    "event": "run_started",
                    "skill": "closeout",
                    "kind": "closeout",
                    "retention_keep": 2000,
                    "session_id": "session-1",
                    "thread_id": "thread-1",
                    "parent_run_id": "parent-1",
                    "root_run_id": "root-1",
                }
                if index % 2 == 0:
                    started["task_id"] = "task-1"
                finished = {
                    "schema_version": 2,
                    "timestamp": "2026-01-01T00:00:10+00:00",
                    "run_id": f"run-{index}",
                    "event": "run_finished",
                    "outcome": "success" if index % 2 == 0 else "failure",
                    "category": "validated",
                    "wall_seconds": 10,
                }
                (closeout / f"{index}.jsonl").write_text(
                    json.dumps(started) + "\n" + json.dumps(finished) + "\n",
                    encoding="utf-8",
                )
            other = root / "other"
            other.mkdir()
            (other / "run.jsonl").write_text(
                json.dumps({"run_id": "other", "event": "run_started", "skill": "other"}) + "\n",
                encoding="utf-8",
            )
            (root / "unrelated.jsonl").write_text("{}\n", encoding="utf-8")

            report = self.report(root, skill="closeout")
            diagnostics = report["diagnostics"]
            self.assertEqual(diagnostics["scanned_files"], 202)
            self.assertEqual(diagnostics["matched_files"], 200)
            self.assertEqual(diagnostics["excluded_files"], 1)
            self.assertEqual(diagnostics["files"], 200)
            self.assertFalse(report["runs"]["retention"]["potentially_censored"])
            self.assertEqual(report["runs"]["retention"]["observed_keep"], [2000])
            self.assertEqual(
                report["runs"]["retention"]["retention_keep_values"], [2000]
            )
            self.assertEqual(
                report["runs"]["retention"]["reached_keep_limits"], []
            )
            self.assertEqual(
                report["runs"]["categories"]["validated"]["success"], 100
            )
            kind = report["runs"]["by_kind"]["closeout"]
            self.assertEqual(kind["total"], 200)
            self.assertEqual(kind["finished"], 200)
            self.assertEqual(kind["wall_seconds"]["sum"], 2000)
            self.assertEqual(
                report["runs"]["correlation_coverage"]["task_id"]["percent"], 50
            )
            self.assertEqual(
                report["runs"]["correlation_coverage"]["session_id"]["percent"], 100
            )
            with mock.patch("sys.stdout") as output:
                skill_log.cmd_report(argparse.Namespace(
                    log_dir=root, skill="closeout", since=None,
                    stale_after_seconds=86400, format="markdown",
                ))
            markdown = "".join(call.args[0] for call in output.write.call_args_list)
            self.assertIn("Run outcomes", markdown)
            self.assertIn("Run wall p50 / p90 / sum", markdown)
            self.assertIn("| closeout | 200 | 200 |", markdown)
            self.assertIn('"failure": 100', markdown)
            self.assertIn("| validated | 200 |", markdown)
            self.assertIn("202 scanned, 200 matched, 1 excluded", markdown)
            self.assertNotIn("historical results may be censored", markdown)

    def test_retention_summary_uses_legacy_limit_only_for_legacy_runs(self) -> None:
        v2_runs = [
            [
                {
                    "schema_version": 2,
                    "skill": "closeout",
                    "retention_keep": 2000,
                }
            ]
            for _ in range(200)
        ]
        self.assertFalse(
            skill_log.retention_summary(v2_runs)["potentially_censored"]
        )

        legacy_runs = list(v2_runs)
        legacy_runs[0] = [{"schema_version": 1, "skill": "closeout"}]
        retention = skill_log.retention_summary(legacy_runs)
        self.assertTrue(retention["potentially_censored"])
        self.assertEqual(retention["retention_keep_values"], [2000])
        self.assertEqual(retention["reached_keep_limits"], [200])

    def test_report_drops_unsafe_legacy_dimensions_from_markdown(self) -> None:
        with tempfile.TemporaryDirectory() as temp:
            root = Path(temp)
            directory = root / "safe-skill"
            directory.mkdir()
            timestamp = "2026-01-01T00:00:00+00:00"
            unsafe = "unsafe | injected"
            events = [
                {
                    "schema_version": 2,
                    "timestamp": timestamp,
                    "run_id": "run-1",
                    "event": "run_started",
                    "skill": "safe-skill",
                    "kind": "safe-kind",
                },
                {
                    "schema_version": 2,
                    "timestamp": timestamp,
                    "run_id": "run-1",
                    "event": "delegation_started",
                    "delegation_id": "delegate-1",
                    "executor": unsafe,
                    "task_kind": "safe-kind",
                    "mode": "parallel",
                },
                {
                    "schema_version": 2,
                    "timestamp": timestamp,
                    "run_id": "run-1",
                    "event": "delegation_finished",
                    "delegation_id": "delegate-1",
                    "outcome": unsafe,
                },
                {
                    "schema_version": 2,
                    "timestamp": timestamp,
                    "run_id": "run-1",
                    "event": "run_finished",
                    "outcome": "success",
                },
            ]
            (directory / "run.jsonl").write_text(
                "".join(json.dumps(event) + "\n" for event in events),
                encoding="utf-8",
            )

            report = self.report(root)
            self.assertEqual(report["delegations"]["by_executor"], {})
            self.assertEqual(report["delegations"]["outcomes"], {})
            self.assertEqual(report["diagnostics"]["unsafe_dimension_values"], 1)
            with mock.patch("sys.stdout") as output:
                skill_log.cmd_report(argparse.Namespace(
                    log_dir=root, skill=None, since=None,
                    stale_after_seconds=86400, format="markdown",
                ))
            markdown = "".join(call.args[0] for call in output.write.call_args_list)
            self.assertNotIn(unsafe, markdown)

    def test_prune_keeps_newest_logs(self) -> None:
        with tempfile.TemporaryDirectory() as temp:
            root = Path(temp)
            files = []
            for index in range(3):
                path = root / f"{index}.jsonl"
                path.write_text("{}\n", encoding="utf-8")
                path.touch()
                files.append(path)
            skill_log.prune(root, 2)
            self.assertEqual(len(list(root.glob("*.jsonl"))), 2)
            self.assertFalse(files[0].exists())

    def test_validator_discovers_new_project_skill(self) -> None:
        with tempfile.TemporaryDirectory() as temp:
            root = Path(temp)
            skill = root / ".agents" / "skills" / "new-skill"
            agent = skill / "agents" / "openai.yaml"
            agent.parent.mkdir(parents=True)
            (skill / "SKILL.md").write_text(
                "---\nname: new-skill\ndescription: A focused test skill.\n---\n\n# New Skill\n",
                encoding="utf-8",
            )
            agent.write_text(
                "interface:\n"
                "  display_name: New Skill\n"
                "  short_description: Focused test skill\n"
                "  default_prompt: Use the new skill.\n",
                encoding="utf-8",
            )
            self.assertEqual(
                validate_skills.discover_skills(root / ".agents" / "skills"),
                ["new-skill"],
            )
            self.assertEqual(validate_skills.validate(root), [])
            agent.unlink()
            self.assertTrue(
                any("missing" in error for error in validate_skills.validate(root))
            )

    def test_validator_requires_shared_iteration_workflow(self) -> None:
        with tempfile.TemporaryDirectory() as temp:
            root = Path(temp)
            skills = root / ".agents" / "skills"

            lifecycle = skills / "skill-runtime"
            lifecycle_agent = lifecycle / "agents" / "openai.yaml"
            lifecycle_agent.parent.mkdir(parents=True)
            (lifecycle / "SKILL.md").write_text(
                "---\n"
                "name: skill-runtime\n"
                "description: Own safe lifecycle telemetry for project skills.\n"
                "---\n\n"
                "skill_log.py start\n"
                "skill_log.py event\n"
                "skill_log.py finish\n"
                "skill_log.py delegate-start\n"
                "skill_log.py delegate-finish\n"
                "skill_log.py delegate-assess\n",
                encoding="utf-8",
            )
            lifecycle_agent.write_text(
                "interface:\n"
                "  display_name: Skill Runtime\n"
                "  short_description: Record privacy safe skill lifecycle facts\n"
                "  default_prompt: Apply $skill-runtime admission and lifecycle rules.\n",
                encoding="utf-8",
            )

            caller = skills / "migration-slice"
            caller_agent = caller / "agents" / "openai.yaml"
            caller_agent.parent.mkdir(parents=True)
            (caller / "SKILL.md").write_text(
                "---\n"
                "name: migration-slice\n"
                "description: Execute exactly one accepted migration slice.\n"
                "---\n\n"
                "## Telemetry admission\n\n"
                "Apply $skill-runtime, then hand off to $iteration-workflow.\n",
                encoding="utf-8",
            )
            caller_agent.write_text(
                "interface:\n"
                "  display_name: Migration Slice\n"
                "  short_description: Execute one accepted migration slice safely\n"
                "  default_prompt: Use $migration-slice for one accepted slice.\n",
                encoding="utf-8",
            )

            errors = validate_skills.validate(root)
            self.assertTrue(
                any("missing shared iteration workflow" in error for error in errors)
            )

    def test_validator_enforces_iteration_workflow_contract(self) -> None:
        with tempfile.TemporaryDirectory() as temp:
            root = Path(temp)
            skills = root / ".agents" / "skills"

            lifecycle = skills / "skill-runtime"
            lifecycle_agent = lifecycle / "agents" / "openai.yaml"
            lifecycle_agent.parent.mkdir(parents=True)
            (lifecycle / "SKILL.md").write_text(
                "---\n"
                "name: skill-runtime\n"
                "description: Own safe lifecycle telemetry for project skills.\n"
                "---\n\n"
                "skill_log.py start\n"
                "skill_log.py event\n"
                "skill_log.py finish\n"
                "skill_log.py delegate-start\n"
                "skill_log.py delegate-finish\n"
                "skill_log.py delegate-assess\n",
                encoding="utf-8",
            )
            lifecycle_agent.write_text(
                "interface:\n"
                "  display_name: Skill Runtime\n"
                "  short_description: Record privacy safe skill lifecycle facts\n"
                "  default_prompt: Apply $skill-runtime admission and lifecycle rules.\n",
                encoding="utf-8",
            )

            workflow = skills / "iteration-workflow"
            workflow_agent = workflow / "agents" / "openai.yaml"
            workflow_agent.parent.mkdir(parents=True)
            (workflow / "SKILL.md").write_text(
                "---\n"
                "name: iteration-workflow\n"
                "description: Plan and verify one repository iteration.\n"
                "---\n\n"
                "# Iteration Workflow\n\n"
                "## Admission\n\n"
                "Apply $skill-runtime admission.\n",
                encoding="utf-8",
            )
            workflow_agent.write_text(
                "interface:\n"
                "  display_name: Iteration Workflow\n"
                "  short_description: Deliver one evidence bound repository iteration\n"
                "  default_prompt: Use $iteration-workflow for one repository iteration.\n",
                encoding="utf-8",
            )

            errors = validate_skills.validate(root)
            for marker in validate_skills.ITERATION_WORKFLOW_MARKERS[1:]:
                self.assertTrue(
                    any(marker in error for error in errors),
                    f"missing validator error for {marker!r}: {errors}",
                )

    def test_validator_enforces_domain_workflow_routing_and_invariants(self) -> None:
        for name, sentinels in validate_skills.WORKFLOW_CALLER_INVARIANTS.items():
            text = "$iteration-closeout\nmake fmt\nmake lint\nmake test\nmake build\n"
            errors = validate_skills.validate_iteration_routing(name, text)
            self.assertTrue(any("$iteration-workflow" in error for error in errors))
            self.assertTrue(any("$iteration-closeout" in error for error in errors))
            for sentinel in sentinels:
                self.assertTrue(
                    any(sentinel in error for error in errors),
                    f"missing invariant error for {name}:{sentinel}: {errors}",
                )
            self.assertTrue(any("raw repository gate list" in error for error in errors))

    def test_validator_rejects_retired_closeout_alias(self) -> None:
        errors = validate_skills.validate_iteration_routing(
            "iteration-closeout",
            "Compatibility wrapper. Apply $iteration-workflow only.\n",
        )
        self.assertTrue(any("retired skill" in error for error in errors))

        errors = validate_skills.validate_iteration_routing(
            "unrelated-skill",
            "Route shared mechanics through $iteration-closeout.\n",
        )
        self.assertTrue(any("retired reference" in error for error in errors))

        errors = validate_skills.validate_retired_references(
            "unrelated-skill agent manifest",
            "default_prompt: Use $iteration-closeout.\n",
        )
        self.assertTrue(any("retired reference" in error for error in errors))

    def test_validator_accepts_upstream_matt_skill_metadata(self) -> None:
        with tempfile.TemporaryDirectory() as temp:
            root = Path(temp)
            skill = root / ".agents" / "skills" / "ask-matt"
            agent = skill / "agents" / "openai.yaml"
            agent.parent.mkdir(parents=True)
            (skill / "SKILL.md").write_text(
                "---\n"
                "name: ask-matt\n"
                "description: Route a request to the right engineering skill.\n"
                "disable-model-invocation: true\n"
                "---\n\n"
                "# Ask Matt\n",
                encoding="utf-8",
            )
            agent.write_text(
                "interface:\n"
                "  display_name: Ask Matt\n"
                "  short_description: Find the right skill or workflow\n",
                encoding="utf-8",
            )

            self.assertEqual(validate_skills.validate(root), [])

    def test_validator_accepts_upstream_superpowers_skill_without_agent_metadata(self) -> None:
        with tempfile.TemporaryDirectory() as temp:
            root = Path(temp)
            skill = root / ".agents" / "skills" / "brainstorming"
            skill.mkdir(parents=True)
            (skill / "SKILL.md").write_text(
                "---\n"
                "name: brainstorming\n"
                "description: Refine an idea into an approved design.\n"
                "---\n\n"
                "# Brainstorming\n",
                encoding="utf-8",
            )

            self.assertEqual(validate_skills.validate(root), [])

    def test_validator_requires_shared_lifecycle_and_removed_skill(self) -> None:
        with tempfile.TemporaryDirectory() as temp:
            root = Path(temp)
            skill = root / ".agents" / "skills" / "migration-slice"
            agent = skill / "agents" / "openai.yaml"
            agent.parent.mkdir(parents=True)
            (skill / "SKILL.md").write_text(
                "---\n"
                "name: migration-slice\n"
                "description: Execute one migration slice.\n"
                "---\n\n"
                "skill_log.py start\n"
                "skill_log.py event\n"
                "skill_log.py finish\n"
                "skill_log.py delegate-start\n"
                "skill_log.py delegate-finish\n"
                "$kimi-offload\n",
                encoding="utf-8",
            )
            agent.write_text(
                "interface:\n"
                "  display_name: Migration Slice\n"
                "  short_description: Execute one slice\n"
                "  default_prompt: Use the migration skill.\n",
                encoding="utf-8",
            )

            errors = validate_skills.validate(root)
            self.assertTrue(
                any("shared lifecycle reference" in error for error in errors)
            )
            self.assertTrue(
                any("telemetry admission rule" in error for error in errors)
            )
            self.assertTrue(
                any("shared lifecycle skill" in error for error in errors)
            )
            self.assertTrue(
                any("removed skill reference" in error for error in errors)
            )

    def test_validator_enforces_analyzed_skill_interface_contract(self) -> None:
        with tempfile.TemporaryDirectory() as temp:
            root = Path(temp)
            skill = root / ".agents" / "skills" / "migration-loop"
            agent = skill / "agents" / "openai.yaml"
            agent.parent.mkdir(parents=True)
            (skill / "SKILL.md").write_text(
                "---\n"
                "name: migration-loop\n"
                "description: Route the explicit product evolution loop.\n"
                "extra: forbidden\n"
                "---\n\n"
                "skill_log.py start\n"
                "skill_log.py event\n"
                "skill_log.py finish\n"
                "skill_log.py delegate-start\n"
                "skill_log.py delegate-finish\n"
                "skill_log.py delegate-assess\n"
                "make fmt\n"
                "make lint\n"
                "make test\n"
                "make build\n",
                encoding="utf-8",
            )
            agent.write_text(
                "interface:\n"
                "  display_name: Eino Product Evolution Loop\n"
                "  short_description: This description is long enough for the interface\n"
                "  default_prompt: Run one explicit evolution phase.\n",
                encoding="utf-8",
            )

            errors = validate_skills.validate(root)

            self.assertTrue(
                any("frontmatter must contain only" in error for error in errors)
            )
            self.assertTrue(
                any("default_prompt must mention" in error for error in errors)
            )
            self.assertTrue(
                any("legacy Eino prefix" in error for error in errors)
            )
            self.assertTrue(
                any("allow_implicit_invocation" in error for error in errors)
            )
            self.assertTrue(
                any("raw repository gate list" in error for error in errors)
            )
            self.assertTrue(
                {
                    "defect-investigation",
                    "write-docs",
                    "human-agent-docs",
                }
                <= validate_skills.LOGGED_SKILLS
            )


if __name__ == "__main__":
    unittest.main()
