#!/usr/bin/env python3
"""Aggregate only strict, pre-sanitized session-shape metadata."""

import argparse
import collections
import json
import re
import sys


FIELDS = ("entrypoint", "tool_kind", "event_kind", "terminal_reason", "transition")
ATOM = re.compile(r"^[A-Za-z0-9][A-Za-z0-9._:/-]{0,63}$")
MAX_LINE_BYTES = 4 * 1024
MAX_RECORDS = 100_000


class InputError(Exception):
    pass


def parse_args():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--input", action="append", required=True, metavar="SANITIZED_JSONL")
    parser.add_argument("--output", choices=("json", "markdown"), default="json")
    args = parser.parse_args()
    if any(path == "-" for path in args.input):
        parser.error("stdin input is not permitted")
    return args


def validate_record(raw):
    if not isinstance(raw, dict) or set(raw) != set(FIELDS):
        raise InputError("record must contain exactly the allowed fields")
    for field in FIELDS:
        value = raw[field]
        if not isinstance(value, str) or not ATOM.fullmatch(value):
            raise InputError("record contains an invalid categorical atom")


def aggregate(paths):
    counts = {field: collections.Counter() for field in FIELDS}
    records = 0
    for path in paths:
        try:
            source = open(path, "rb")
        except OSError as error:
            raise InputError("unable to read input") from error
        with source:
            while True:
                line = source.readline(MAX_LINE_BYTES + 1)
                if not line:
                    break
                if len(line) > MAX_LINE_BYTES or not line.endswith(b"\n"):
                    raise InputError("input line exceeds the size limit")
                try:
                    raw = json.loads(line.decode("utf-8"))
                except (UnicodeDecodeError, json.JSONDecodeError) as error:
                    raise InputError("input line is not valid JSON") from error
                validate_record(raw)
                records += 1
                if records > MAX_RECORDS:
                    raise InputError("input exceeds the record limit")
                for field in FIELDS:
                    counts[field][raw[field]] += 1
    return {
        "schema_version": 1,
        "records": records,
        "counts": {field: dict(sorted(counts[field].items())) for field in FIELDS},
    }


def render_markdown(report):
    lines = ["# Sanitized Session Shape", "", f"Records: {report['records']}", ""]
    for field, values in report["counts"].items():
        lines.extend((f"## {field}", ""))
        lines.extend(f"- `{value}`: {count}" for value, count in values.items())
        lines.append("")
    return "\n".join(lines)


def main():
    args = parse_args()
    try:
        report = aggregate(args.input)
    except InputError as error:
        print(f"session-shape: {error}", file=sys.stderr)
        return 2
    if args.output == "json":
        print(json.dumps(report, separators=(",", ":"), sort_keys=False))
    else:
        print(render_markdown(report))
    return 0


if __name__ == "__main__":
    sys.exit(main())
