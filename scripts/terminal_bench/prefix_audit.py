"""Audit captured request prefixes offline without printing their contents.

python -m scripts.terminal_bench.prefix_audit --wire WIRE_DIR --output audit.json

Byte/message prefix stability is diagnostic evidence, not a provider tokenizer,
cache implementation, billing receipt, or prediction of a cache-hit target.
"""

from __future__ import annotations

import argparse
import json
from pathlib import Path

from scripts.terminal_bench.compare_runs import atom, count, fingerprint, read_json


def canonical(value):
    return json.dumps(value, ensure_ascii=False, sort_keys=True, separators=(",", ":"), allow_nan=False).encode("utf-8")


def common_bytes(a, b):
    for i, (left, right) in enumerate(zip(a, b)):
        if left != right:
            return i
    return min(len(a), len(b))


def shape(body, protocol):
    history_key = "input" if protocol == "responses" else "messages"
    if protocol not in ("responses", "messages") or history_key not in body:
        raise ValueError("unsupported captured request dialect")
    history = body[history_key]
    if isinstance(history, str):
        history = [history]
    if not isinstance(history, list):
        raise ValueError("history must be a string or list")
    static = {key: body.get(key) for key in ("instructions", "system", "tools")}
    static["other"] = {key: value for key, value in body.items() if key not in (*static, history_key)}
    return history, static


def compare_requests(previous, current, protocol):
    old, before = shape(previous, protocol)
    new, after = shape(current, protocol)
    prefix = 0
    for a, b in zip(old, new):
        if canonical(a) != canonical(b):
            break
        prefix += 1
    changed = [key for key in before if canonical(before[key]) != canonical(after[key])]
    return dict(previous_messages=len(old), current_messages=len(new), unchanged_leading_messages=prefix,
                history_append_only=prefix == len(old) and len(new) >= len(old),
                first_changed_previous_message=prefix if prefix < len(old) else None,
                common_history_prefix_bytes=common_bytes(canonical(old), canonical(new)),
                changed_static_components=changed, static_components_stable=not changed,
                canonical_request_unchanged=canonical(previous) == canonical(current))


def audit(wire):
    root = Path(wire).resolve()
    ledger_path = root / "ledger.json"
    ledger = read_json(ledger_path)
    records, protocol = ledger["records"], ledger["protocol"]
    if protocol not in ("responses", "messages"):
        raise ValueError("unsupported ledger dialect")
    identifiers = [atom(row["id"]) for row in records]
    if len(set(identifiers)) != len(records) or any("/" in key or key in (".", "..") for key in identifiers):
        raise ValueError("unsafe or duplicate call identifier")
    rows, previous, previous_raw = [], None, None
    input_tokens, cached_tokens, known_calls = 0, 0, 0
    for index, record in enumerate(records):
        path = root / (record["id"] + ".request.json")
        item = dict(call_index=index, request_available=path.is_file(), comparison=None)
        if record["state"] == "known":
            prompt = count(record["input_tokens"])
            cached = count(record["cached_input_tokens"])
            if cached > prompt:
                raise ValueError("cached tokens exceed input")
            input_tokens += prompt
            cached_tokens += cached
            known_calls += 1
        elif record["state"] != "unknown":
            raise ValueError("call not closed")
        if path.is_file():
            if not path.resolve().is_relative_to(root) or path.resolve() != path:
                raise ValueError("request must not use symbolic links")
            body = read_json(path)
            shape(body, protocol)
            raw = path.read_bytes()
            item["request_sha256"] = fingerprint(path)
            if previous is not None:
                item["comparison"] = compare_requests(previous, body, protocol)
                item["comparison"]["common_wire_prefix_bytes"] = common_bytes(previous_raw, raw)
            previous, previous_raw = body, raw
        else:
            # A gap must break adjacency; never skip missing requests and call
            # the resulting non-adjacent pair an append-only transition.
            previous, previous_raw = None, None
        rows.append(item)
    return dict(schema_version=1, authority="diagnostic_only", protocol=atom(protocol),
                ledger_sha256=fingerprint(ledger_path), calls=len(records), known_usage_calls=known_calls,
                input_tokens_known=input_tokens, cached_input_tokens_known=cached_tokens,
                cache_hit_rate_known_including_first=cached_tokens/input_tokens if input_tokens else None,
                usage_complete=known_calls == len(records), transitions=rows,
                cache_token_prediction=None, actual_billed_cny=None,
                limitations=["canonical_json_is_not_provider_tokenization_or_cache_key",
                             "adjacent_requests_may_belong_to_different_conversation_branches",
                             "wire_whitespace_or_key_order_may_differ_without_semantic_change",
                             "static_field_changes_do_not_by_themselves_prove_cache_misses",
                             "missing_requests_break_adjacency_and_are_not_reconstructed",
                             "no_request_text_or_thinking_content_is_exported"])


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--wire", type=Path, required=True)
    parser.add_argument("--output", type=Path, required=True)
    args = parser.parse_args()
    report = audit(args.wire)
    with args.output.open("x", encoding="utf-8") as stream:
        json.dump(report, stream, indent=2, allow_nan=False)
        stream.write("\n")


if __name__ == "__main__":
    main()
