#!/usr/bin/env python3
"""Focused tests for human-agent-docs structural auditing."""

from __future__ import annotations

import tempfile
import unittest
from pathlib import Path

import audit_doc


class AuditDocTest(unittest.TestCase):
    def test_markdown_detects_github_style_line_anchor(self) -> None:
        with tempfile.TemporaryDirectory() as temp:
            path = Path(temp) / "guide.md"
            text = "# Guide\n\nOwner: `engine/query.go#L12-L18`.\n"

            audit = audit_doc.audit_markdown(path, text)

            self.assertEqual(audit.metrics["line_anchors"], 1)
            self.assertEqual(audit.metrics["hash_line_anchors"], 1)
            self.assertIn(
                "VOLATILE_LINE_ANCHORS",
                {issue.code for issue in audit.issues},
            )

    def test_html_detects_github_style_line_anchor(self) -> None:
        with tempfile.TemporaryDirectory() as temp:
            path = Path(temp) / "guide.html"
            text = (
                "<!doctype html><html lang=\"zh-CN\"><head>"
                "<meta name=\"viewport\" content=\"width=device-width\">"
                "<title>Guide</title></head><body><main><h1>Guide</h1>"
                "<p>Owner: engine/query.go#L12.</p></main></body></html>"
            )

            audit = audit_doc.audit_html(path, text)

            self.assertEqual(audit.metrics["line_anchors"], 1)
            self.assertEqual(audit.metrics["hash_line_anchors"], 1)

    def test_markdown_detects_qualified_colon_line_anchor(self) -> None:
        with tempfile.TemporaryDirectory() as temp:
            path = Path(temp) / "guide.md"
            audit = audit_doc.audit_markdown(
                path,
                "# Guide\n\nOwner: `engine/query.go:12-18`.\n",
            )

            self.assertEqual(audit.metrics["line_anchors"], 1)
            self.assertEqual(audit.metrics["qualified_line_anchors"], 1)

    def test_markdown_ignores_chinese_numeric_values(self) -> None:
        with tempfile.TemporaryDirectory() as temp:
            path = Path(temp) / "guide.md"
            audit = audit_doc.audit_markdown(
                path,
                "# Guide\n\n端口:8080，错误码:404，时间 12:30。\n",
            )

            self.assertEqual(audit.metrics["line_anchors"], 0)
            self.assertNotIn(
                "VOLATILE_LINE_ANCHORS",
                {issue.code for issue in audit.issues},
            )


if __name__ == "__main__":
    unittest.main()
