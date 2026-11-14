import hashlib
import json
from pathlib import Path
import tempfile
import unittest

from scripts.terminal_bench.compare_runs import build_report, load_run, markdown


class ZeroModeComparisonTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)

    def write(self, name, *, zero_mode=True, include_marker=True, calls=0):
        root = self.root / name
        root.mkdir()
        entries = []
        for agent in ("dsh", "yhc"):
            sample = name + "-" + agent
            entries.append(dict(sample=dict(agent=agent, sample_id=sample,
                task="generic-task", task_ref="sha256:fixture"), normal_completion=False,
                official_reward={"rewards": {"reward": 0}},
                summary=dict(calls=calls, unknown_calls=0, usage_complete=True,
                    input_tokens=100 * calls, cached_input_tokens=90 * calls,
                    output_tokens=8 * calls, thinking_tokens=None,
                    known_peak_estimate_cny="0.001" if calls else "0")))
            wire = root / "wire" / sample
            wire.mkdir(parents=True)
            records = [dict(id=str(i), state="known", input_tokens=100,
                cached_input_tokens=90, output_tokens=8, thinking_tokens=None,
                known_peak_estimate_cny="0.001", requested_model="fixture-model", effort="high")
                for i in range(calls)]
            (wire / "ledger.json").write_text(json.dumps(dict(sample=sample, protocol="responses",
                currency="CNY", records=records, denials=[])))
        board = dict(status="zero_preflight_passed", real_provider_calls=0, samples=entries)
        if include_marker:
            board["zero_mode"] = zero_mode
        (root / "scoreboard.json").write_text(json.dumps(board))
        return root

    def test_two_arm_zero_admission_is_not_a_recorded_model_run(self):
        root = self.write("admission")
        report = build_report([root])
        self.assertEqual(len(report["samples"]), 2)
        for sample in report["samples"]:
            self.assertEqual(sample["evidence_kind"], "zero_mode_preflight")
            self.assertEqual(sample["classification_source_sha256"],
                             hashlib.sha256((root / "scoreboard.json").read_bytes()).hexdigest())
            self.assertEqual(sample["calls"], 0)
            self.assertEqual(sample["status"], "interrupted_or_unverified")
            self.assertIsNone(sample["thinking"]["known_tokens"])
        self.assertFalse(report["paired_tasks"])
        self.assertIn("zero_mode_preflight", markdown(report))

    def test_local_fixture_counters_are_preserved_but_cannot_form_real_pairs(self):
        # Explicit provenance, rather than the presence of metrics or wire files,
        # determines whether local scripted responses are model evidence.
        root = self.write("scripted", calls=1)
        report = build_report([root])
        self.assertFalse(report["paired_tasks"])
        for sample in report["samples"]:
            self.assertEqual(sample["evidence_kind"], "zero_mode_preflight")
            self.assertEqual(sample["calls"], 1)
            self.assertEqual(sample["input_tokens"], 100)
            self.assertEqual(sample["known_peak_estimate_cny"], "0.001")

    def test_zero_calls_do_not_reclassify_an_unmarked_or_false_marked_real_run(self):
        for index, include_marker in enumerate((False, True)):
            root = self.write(str(index), zero_mode=False, include_marker=include_marker)
            for sample in load_run(root):
                self.assertEqual(sample["evidence_kind"], "recorded_run")
                self.assertIsNone(sample["classification_source_sha256"])

    def test_invalid_or_contradictory_zero_markers_are_rejected(self):
        cases = [dict(zero_mode=v) for v in ("true", 1, None)]
        cases += [dict(real_provider_calls=v) for v in (1, False, None, -1, "0")]
        cases.append(dict(remove_real_provider_calls=True))
        for index, change in enumerate(cases):
            root = self.write(str(index))
            path = root / "scoreboard.json"
            data = json.loads(path.read_text())
            if change.get("remove_real_provider_calls"):
                data.pop("real_provider_calls")
            else:
                data.update(change)
            path.write_text(json.dumps(data))
            with self.subTest(change=change), self.assertRaises(ValueError):
                load_run(root)


if __name__ == "__main__":
    unittest.main()
