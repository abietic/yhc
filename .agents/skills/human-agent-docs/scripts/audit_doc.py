#!/usr/bin/env python3
"""Audit structural risks in Markdown and HTML documentation.

This script is intentionally advisory. It cannot prove that architecture claims
match production code; use source and focused tests for semantic verification.
"""

from __future__ import annotations

import argparse
import json
import re
import sys
from dataclasses import asdict, dataclass
from html.parser import HTMLParser
from pathlib import Path
from typing import Iterable
from urllib.parse import unquote, urlparse


QUALIFIED_LINE_ANCHOR_RE = re.compile(
    r"(?<![\w])(?:[\w.@+-]+/)*[\w.@+-]+\."
    r"(?:go|md|py|js|ts|tsx|jsx|html|css|ya?ml|json|toml):\d+(?:-\d+)?"
)
HASH_LINE_ANCHOR_RE = re.compile(
    r"(?<![\w])(?:[\w.@+-]+/)*[\w.@+-]+\."
    r"(?:go|md|py|js|ts|tsx|jsx|html|css|ya?ml|json|toml)"
    r"#L\d+(?:-L?\d+)?"
)
ABSOLUTE_PATH_RE = re.compile(r"(?<![\w])(?:/Users/|/home/|[A-Za-z]:\\Users\\)")
MARKDOWN_LINK_RE = re.compile(r"!?\[[^\]]*]\(([^)]+)\)")
VOLATILE_COUNT_RE = re.compile(
    r"(?:测试|文件|工具|节点|package|packages|tests?|files?)"
    r"[^\n。；;]{0,16}\b\d+\b|\b\d+\b[^\n。；;]{0,8}"
    r"(?:个测试|个文件|个工具|tests?|files?)",
    re.IGNORECASE,
)
GENERIC_HEADINGS = {
    "概述",
    "总览",
    "介绍",
    "其他",
    "misc",
    "miscellaneous",
    "overview",
    "introduction",
}


@dataclass
class Issue:
    severity: str
    code: str
    line: int
    message: str


@dataclass
class Audit:
    path: str
    kind: str
    metrics: dict[str, int]
    issues: list[Issue]


def line_number(text: str, offset: int) -> int:
    return text.count("\n", 0, offset) + 1


def add_local_link_issues(
    path: Path, links: Iterable[tuple[str, int]], issues: list[Issue]
) -> None:
    for raw_target, line in links:
        target = raw_target.strip().strip("<>")
        if not target or target.startswith(("#", "mailto:", "data:", "javascript:")):
            continue
        parsed = urlparse(target)
        if parsed.scheme or parsed.netloc:
            continue
        local = unquote(parsed.path)
        if not local:
            continue
        candidate = (path.parent / local).resolve()
        if not candidate.exists():
            issues.append(
                Issue(
                    "warning",
                    "BROKEN_LOCAL_LINK",
                    line,
                    f"本地链接目标不存在：{local}",
                )
            )


def audit_markdown(path: Path, text: str) -> Audit:
    issues: list[Issue] = []
    headings: list[tuple[int, str, int]] = []

    in_fence = False
    fence_marker = ""
    mermaid_start = 0
    mermaid_lines: list[str] = []
    links: list[tuple[str, int]] = []
    paragraph: list[tuple[int, str]] = []
    paragraphs: list[tuple[int, str]] = []

    def flush_paragraph() -> None:
        if paragraph:
            paragraphs.append(
                (paragraph[0][0], " ".join(part.strip() for _, part in paragraph))
            )
            paragraph.clear()

    lines = text.splitlines()
    for number, raw in enumerate(lines, start=1):
        stripped = raw.strip()
        fence = re.match(r"^(```+|~~~+)\s*([\w-]*)", stripped)
        if fence:
            flush_paragraph()
            marker, language = fence.groups()
            if not in_fence:
                in_fence = True
                fence_marker = marker[0]
                if language.lower() == "mermaid":
                    mermaid_start = number
                    mermaid_lines = []
            elif marker.startswith(fence_marker):
                if mermaid_start:
                    diagram = "\n".join(mermaid_lines)
                    if "accTitle:" not in diagram:
                        issues.append(
                            Issue(
                                "warning",
                                "MERMAID_ACC_TITLE",
                                mermaid_start,
                                "Mermaid 图缺少 accTitle。",
                            )
                        )
                    if "accDescr:" not in diagram:
                        issues.append(
                            Issue(
                                "warning",
                                "MERMAID_ACC_DESCRIPTION",
                                mermaid_start,
                                "Mermaid 图缺少 accDescr。",
                            )
                        )
                in_fence = False
                fence_marker = ""
                mermaid_start = 0
                mermaid_lines = []
            continue

        if in_fence:
            if mermaid_start:
                mermaid_lines.append(raw)
            continue

        heading = re.match(r"^(#{1,6})\s+(.+?)\s*#*\s*$", raw)
        if heading:
            flush_paragraph()
            headings.append((len(heading.group(1)), heading.group(2), number))
        elif not stripped or re.match(r"^(?:[-*_]\s*){3,}$", stripped):
            flush_paragraph()
        elif not stripped.startswith((">", "-", "*", "+", "|")) and not re.match(
            r"^\d+[.)]\s", stripped
        ):
            paragraph.append((number, stripped))
        else:
            flush_paragraph()

        for match in MARKDOWN_LINK_RE.finditer(raw):
            links.append((match.group(1), number))

    flush_paragraph()
    if in_fence:
        issues.append(
            Issue("error", "UNCLOSED_FENCE", len(lines), "代码围栏没有闭合。")
        )

    h1_count = sum(level == 1 for level, _, _ in headings)
    if h1_count != 1:
        issues.append(
            Issue(
                "error",
                "H1_COUNT",
                1,
                f"应有且仅有一个 H1，当前为 {h1_count}。",
            )
        )

    previous_level = 0
    seen: dict[str, int] = {}
    for level, title, number in headings:
        if previous_level and level > previous_level + 1:
            issues.append(
                Issue(
                    "warning",
                    "HEADING_JUMP",
                    number,
                    f"标题从 H{previous_level} 跳到 H{level}。",
                )
            )
        previous_level = level
        normalized = re.sub(r"\s+", " ", title.strip().lower())
        if normalized in GENERIC_HEADINGS:
            issues.append(
                Issue(
                    "info",
                    "GENERIC_HEADING",
                    number,
                    f"标题“{title}”没有说明读者问题或结论。",
                )
            )
        if normalized in seen:
            issues.append(
                Issue(
                    "info",
                    "DUPLICATE_HEADING",
                    number,
                    f"标题“{title}”与第 {seen[normalized]} 行重复。",
                )
            )
        else:
            seen[normalized] = number

    long_paragraphs = 0
    mixed_lifecycle = 0
    for number, content in paragraphs:
        if len(content) > 600:
            long_paragraphs += 1
            issues.append(
                Issue(
                    "warning",
                    "LONG_PARAGRAPH",
                    number,
                    f"段落长度为 {len(content)} 个字符，建议拆分判断。",
                )
            )
        current_terms = re.search(r"当前|已经|现有|production|currently", content, re.I)
        plan_terms = re.search(r"计划|未来|待完成|尚未|TODO|planned", content, re.I)
        if current_terms and plan_terms:
            mixed_lifecycle += 1
            issues.append(
                Issue(
                    "info",
                    "MIXED_LIFECYCLE",
                    number,
                    "同一段同时出现 current 与 plan 词汇，请确认边界清晰。",
                )
            )

    qualified_anchors = list(QUALIFIED_LINE_ANCHOR_RE.finditer(text))
    shorthand_anchors: list[re.Match[str]] = []
    hash_anchors = list(HASH_LINE_ANCHOR_RE.finditer(text))
    anchors = sorted(
        [*qualified_anchors, *shorthand_anchors, *hash_anchors],
        key=lambda match: match.start(),
    )
    if anchors:
        issues.append(
            Issue(
                "warning",
                "VOLATILE_LINE_ANCHORS",
                line_number(text, anchors[0].start()),
                f"发现 {len(anchors)} 个文件行号锚点；导读应优先使用稳定符号。",
            )
        )

    absolute_paths = list(ABSOLUTE_PATH_RE.finditer(text))
    if absolute_paths:
        issues.append(
            Issue(
                "warning",
                "MACHINE_PATHS",
                line_number(text, absolute_paths[0].start()),
                f"发现 {len(absolute_paths)} 个机器相关绝对路径。",
            )
        )

    volatile_counts = list(VOLATILE_COUNT_RE.finditer(text))
    if len(volatile_counts) >= 3:
        issues.append(
            Issue(
                "info",
                "VOLATILE_COUNTS",
                line_number(text, volatile_counts[0].start()),
                f"发现 {len(volatile_counts)} 处可能易漂移的数量陈述。",
            )
        )

    add_local_link_issues(path, links, issues)
    metrics = {
        "lines": len(lines),
        "headings": len(headings),
        "h1": h1_count,
        "mermaid": sum(
            1 for line in lines if re.match(r"^```+\s*mermaid\s*$", line.strip(), re.I)
        ),
        "line_anchors": len(anchors),
        "qualified_line_anchors": len(qualified_anchors),
        "shorthand_line_anchors": len(shorthand_anchors),
        "hash_line_anchors": len(hash_anchors),
        "absolute_paths": len(absolute_paths),
        "long_paragraphs": long_paragraphs,
        "mixed_lifecycle_paragraphs": mixed_lifecycle,
    }
    return Audit(str(path), "markdown", metrics, issues)


class StructureHTMLParser(HTMLParser):
    def __init__(self) -> None:
        super().__init__(convert_charrefs=True)
        self.tags: dict[str, int] = {}
        self.headings: list[tuple[int, int]] = []
        self.links: list[tuple[str, int]] = []
        self.html_lang = ""
        self.has_viewport = False
        self.has_title = False
        self.external_assets: list[tuple[str, int]] = []
        self.hidden_content: list[int] = []
        self.skip_main = False

    def handle_starttag(
        self, tag: str, attrs_list: list[tuple[str, str | None]]
    ) -> None:
        tag = tag.lower()
        attrs = {key.lower(): value or "" for key, value in attrs_list}
        self.tags[tag] = self.tags.get(tag, 0) + 1
        line, _ = self.getpos()

        if tag == "html":
            self.html_lang = attrs.get("lang", "")
        elif tag == "meta" and attrs.get("name", "").lower() == "viewport":
            self.has_viewport = True
        elif tag == "title":
            self.has_title = True
        elif re.fullmatch(r"h[1-6]", tag):
            self.headings.append((int(tag[1]), line))
        elif tag == "a":
            href = attrs.get("href", "")
            self.links.append((href, line))
            classes = set(attrs.get("class", "").split())
            if href == "#main" and ("skip-link" in classes or "skip" in classes):
                self.skip_main = True

        if tag == "script" and attrs.get("src", "").startswith(("http://", "https://")):
            self.external_assets.append((attrs["src"], line))
        if tag == "link" and attrs.get("href", "").startswith(("http://", "https://")):
            self.external_assets.append((attrs["href"], line))
        if "hidden" in attrs or "display:none" in attrs.get("style", "").replace(" ", ""):
            self.hidden_content.append(line)


def audit_html(path: Path, text: str) -> Audit:
    issues: list[Issue] = []
    parser = StructureHTMLParser()
    try:
        parser.feed(text)
        parser.close()
    except Exception as exc:  # HTMLParser is forgiving; retain a hard parse guard.
        issues.append(Issue("error", "HTML_PARSE", 1, f"HTML 解析失败：{exc}"))

    if not re.match(r"\s*<!doctype\s+html", text, re.I):
        issues.append(Issue("error", "DOCTYPE", 1, "缺少 HTML5 doctype。"))
    if not parser.html_lang:
        issues.append(Issue("error", "HTML_LANG", 1, "html 元素缺少 lang。"))
    if not parser.has_viewport:
        issues.append(Issue("error", "VIEWPORT", 1, "缺少 viewport meta。"))
    if not parser.has_title:
        issues.append(Issue("error", "TITLE", 1, "缺少 title 元素。"))

    h1_count = sum(level == 1 for level, _ in parser.headings)
    if h1_count != 1:
        issues.append(
            Issue("error", "H1_COUNT", 1, f"应有且仅有一个 H1，当前为 {h1_count}。")
        )
    if parser.tags.get("main", 0) != 1:
        issues.append(
            Issue(
                "error",
                "MAIN_COUNT",
                1,
                f"应有且仅有一个 main，当前为 {parser.tags.get('main', 0)}。",
            )
        )
    if not parser.tags.get("nav"):
        issues.append(Issue("warning", "NAV", 1, "没有 nav 导航区域。"))
    if not parser.skip_main:
        issues.append(
            Issue("warning", "SKIP_LINK", 1, "没有指向 #main 的跳过导航链接。")
        )

    previous_level = 0
    for level, number in parser.headings:
        if previous_level and level > previous_level + 1:
            issues.append(
                Issue(
                    "warning",
                    "HEADING_JUMP",
                    number,
                    f"标题从 H{previous_level} 跳到 H{level}。",
                )
            )
        previous_level = level

    figures = parser.tags.get("figure", 0)
    captions = parser.tags.get("figcaption", 0)
    if figures > captions:
        issues.append(
            Issue(
                "warning",
                "FIGURE_CAPTION",
                1,
                f"{figures} 个 figure 只有 {captions} 个 figcaption。",
            )
        )

    if parser.external_assets:
        issues.append(
            Issue(
                "warning",
                "EXTERNAL_ASSETS",
                parser.external_assets[0][1],
                f"发现 {len(parser.external_assets)} 个外部脚本或样式依赖。",
            )
        )
    if parser.hidden_content:
        issues.append(
            Issue(
                "info",
                "HIDDEN_CONTENT",
                parser.hidden_content[0],
                f"发现 {len(parser.hidden_content)} 个默认隐藏元素；确认无脚本时核心内容可见。",
            )
        )

    qualified_anchors = list(QUALIFIED_LINE_ANCHOR_RE.finditer(text))
    shorthand_anchors: list[re.Match[str]] = []
    hash_anchors = list(HASH_LINE_ANCHOR_RE.finditer(text))
    anchors = sorted(
        [*qualified_anchors, *shorthand_anchors, *hash_anchors],
        key=lambda match: match.start(),
    )
    if anchors:
        issues.append(
            Issue(
                "warning",
                "VOLATILE_LINE_ANCHORS",
                line_number(text, anchors[0].start()),
                f"发现 {len(anchors)} 个文件行号锚点；导读应优先使用稳定符号。",
            )
        )
    absolute_paths = list(ABSOLUTE_PATH_RE.finditer(text))
    if absolute_paths:
        issues.append(
            Issue(
                "warning",
                "MACHINE_PATHS",
                line_number(text, absolute_paths[0].start()),
                f"发现 {len(absolute_paths)} 个机器相关绝对路径。",
            )
        )

    add_local_link_issues(path, parser.links, issues)
    metrics = {
        "lines": len(text.splitlines()),
        "headings": len(parser.headings),
        "h1": h1_count,
        "main": parser.tags.get("main", 0),
        "nav": parser.tags.get("nav", 0),
        "figures": figures,
        "figcaptions": captions,
        "line_anchors": len(anchors),
        "qualified_line_anchors": len(qualified_anchors),
        "shorthand_line_anchors": len(shorthand_anchors),
        "hash_line_anchors": len(hash_anchors),
        "absolute_paths": len(absolute_paths),
        "external_assets": len(parser.external_assets),
    }
    return Audit(str(path), "html", metrics, issues)


def audit_file(path: Path) -> Audit:
    text = path.read_text(encoding="utf-8")
    suffix = path.suffix.lower()
    if suffix in {".md", ".markdown"}:
        return audit_markdown(path, text)
    if suffix in {".html", ".htm"}:
        return audit_html(path, text)
    raise ValueError(f"不支持的文件类型：{path}")


def render_text(audits: list[Audit]) -> None:
    for audit in audits:
        print(f"{audit.path} [{audit.kind}]")
        print("  metrics: " + ", ".join(f"{k}={v}" for k, v in audit.metrics.items()))
        if not audit.issues:
            print("  issues: none")
            continue
        for issue in audit.issues:
            print(
                f"  {issue.severity.upper():7} "
                f"{issue.code}:{issue.line} {issue.message}"
            )


def main() -> int:
    parser = argparse.ArgumentParser(
        description="审计 Markdown/HTML 技术文档中的结构性风险。"
    )
    parser.add_argument("paths", nargs="+", type=Path)
    parser.add_argument("--format", choices=("text", "json"), default="text")
    parser.add_argument(
        "--strict",
        action="store_true",
        help="存在 error 或 warning 时返回非零；info 仍为建议。",
    )
    args = parser.parse_args()

    audits: list[Audit] = []
    try:
        for path in args.paths:
            if not path.is_file():
                raise FileNotFoundError(path)
            audits.append(audit_file(path))
    except (OSError, UnicodeError, ValueError) as exc:
        print(f"audit_doc.py: {exc}", file=sys.stderr)
        return 2

    if args.format == "json":
        print(json.dumps([asdict(audit) for audit in audits], ensure_ascii=False, indent=2))
    else:
        render_text(audits)

    if args.strict:
        return int(
            any(
                issue.severity in {"error", "warning"}
                for audit in audits
                for issue in audit.issues
            )
        )
    return int(any(issue.severity == "error" for audit in audits for issue in audit.issues))


if __name__ == "__main__":
    raise SystemExit(main())
