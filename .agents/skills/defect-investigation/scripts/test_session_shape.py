import json
import pathlib
import subprocess
import tempfile
import unittest


SCRIPT = pathlib.Path(__file__).with_name("session_shape.py")
FIELDS = ("entrypoint", "tool_kind", "event_kind", "terminal_reason", "transition")
RECORD = {
    "entrypoint": "headless.exec",
    "tool_kind": "Write",
    "event_kind": "tool_result",
    "terminal_reason": "success",
    "transition": "started-to-terminal",
}


class SessionShapeTest(unittest.TestCase):
    def run_tool(self, content, *extra):
        with tempfile.TemporaryDirectory() as directory:
            path = pathlib.Path(directory) / "sanitized.jsonl"
            path.write_bytes(content)
            return subprocess.run(
                ["python3", str(SCRIPT), "--input", str(path), *extra],
                capture_output=True,
                text=True,
                check=False,
            )

    def test_counts_are_sorted_and_deterministic(self):
        records = [
            RECORD,
            {**RECORD, "entrypoint": "tui", "tool_kind": "Edit"},
            {**RECORD, "tool_kind": "Bash", "event_kind": "terminal"},
        ]
        content = "".join(json.dumps(record) + "\n" for record in records).encode()
        first = self.run_tool(content, "--output", "json")
        second = self.run_tool(content, "--output", "json")
        self.assertEqual(first.returncode, 0, first.stderr)
        self.assertEqual(first.stdout, second.stdout)
        self.assertEqual(
            json.loads(first.stdout),
            {
                "schema_version": 1,
                "records": 3,
                "counts": {
                    "entrypoint": {"headless.exec": 2, "tui": 1},
                    "tool_kind": {"Bash": 1, "Edit": 1, "Write": 1},
                    "event_kind": {"terminal": 1, "tool_result": 2},
                    "terminal_reason": {"success": 3},
                    "transition": {"started-to-terminal": 3},
                },
            },
        )

    def test_markdown_is_sorted_counts_only(self):
        result = self.run_tool((json.dumps(RECORD) + "\n").encode(), "--output", "markdown")
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertIn("# Sanitized Session Shape", result.stdout)
        self.assertNotIn("session", result.stdout.lower().replace("sanitized session shape", ""))

    def test_rejects_invalid_records(self):
        cases = [
            b"not json\n",
            b"[]\n",
            (json.dumps({**RECORD, "unknown": "value"}) + "\n").encode(),
            (json.dumps({key: value for key, value in RECORD.items() if key != "transition"}) + "\n").encode(),
            (json.dumps({**RECORD, "entrypoint": "has space"}) + "\n").encode(),
            (json.dumps({**RECORD, "entrypoint": "x" * 65}) + "\n").encode(),
            (b"{" + b"x" * 4096 + b"}\n"),
            (json.dumps(RECORD)).encode(),
        ]
        for content in cases:
            with self.subTest(content=content[:20]):
                self.assertNotEqual(self.run_tool(content).returncode, 0)

    def test_rejects_more_than_record_limit(self):
        content = (json.dumps(RECORD) + "\n").encode() * 100001
        self.assertNotEqual(self.run_tool(content).returncode, 0)

    def test_rejects_privacy_keys_without_leaking_markers(self):
        marker = "SYNTHETIC_PRIVACY_MARKER"
        for key in ("prompt", "response", "title", "transcript", "credential", "environment", "source", "argv"):
            with self.subTest(key=key):
                result = self.run_tool((json.dumps({**RECORD, key: marker}) + "\n").encode())
                self.assertNotEqual(result.returncode, 0)
                self.assertNotIn(marker, result.stdout)
                self.assertNotIn(marker, result.stderr)


if __name__ == "__main__":
    unittest.main()
