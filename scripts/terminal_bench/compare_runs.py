"""Read-only comparison of native harness scoreboards and per-call receipts.

python -m scripts.terminal_bench.compare_runs --run YHC_ROOT --run DSH_ROOT \
    --output comparison.json

This accepts explicit local operator artifacts, never executes/imports them,
never queries a provider, and never changes financial or recovery authority.
"""

from __future__ import annotations

import argparse
from decimal import Decimal, InvalidOperation
import hashlib
import json
import math
from pathlib import Path
import re

from scripts.terminal_bench.tool_feedback import collect_tool_feedback


def atom(value):
    if not isinstance(value, str) or not re.fullmatch(r"[A-Za-z0-9_.:/-]{1,180}", value):
        raise ValueError("invalid evidence identifier")
    return value


def count(value):
    if type(value) is not int or value < 0:
        raise ValueError("invalid token or call counter")
    return value


def amount(value):
    if not isinstance(value, str):
        raise ValueError("money requires a decimal string")
    try:
        result = Decimal(value)
    except InvalidOperation as exc:
        raise ValueError("invalid amount") from exc
    if not result.is_finite() or result < 0:
        raise ValueError("invalid amount")
    return result


def seconds(value):
    if value is None:
        return None
    if type(value) not in (int, float) or not math.isfinite(value) or value < 0:
        raise ValueError("invalid duration")
    return value


def read_json(path):
    path = Path(path)
    if path.is_symlink() or not path.is_file():
        raise ValueError("evidence must be a regular non-symlink file")
    if path.stat().st_size > 64 * 1024 * 1024:
        raise ValueError("evidence file too large")
    def unique(pairs):
        out = {}
        for key, value in pairs:
            if key in out:
                raise ValueError("duplicate JSON key")
            out[key] = value
        return out
    return json.loads(path.read_text(encoding="utf-8"), object_pairs_hook=unique)


def fingerprint(path):
    return hashlib.sha256(Path(path).read_bytes()).hexdigest()


def reward(value):
    value = (value or {}).get("rewards", {}).get("reward")
    if value is not None and (type(value) not in (int, float) or not math.isfinite(value) or not 0 <= value <= 1):
        raise ValueError("invalid reward")
    return value


def call_detail(index, record):
    diagnostics = record.get("stream_diagnostics") or {}
    timings = {key: seconds(diagnostics.get(key)) for key in (
        "connected_seconds", "request_attempt_seconds", "request_sent_seconds", "headers_seconds",
        "first_byte_seconds", "terminal_seconds", "closed_seconds", "elapsed_seconds")}
    error = record.get("error")
    # Never copy provider/exception prose into a report. These categories also
    # do not establish whether the provider received or billed a request.
    category = ("timeout" if error in ("TimeoutError", "socket_timeout", "stream_deadline") else
                "usage_missing" if error in ("usage_missing", "stream_ended_without_complete_usage") else
                "other" if error else None)
    return dict(call_index=index, state=record["state"], elapsed_seconds=seconds(record.get("elapsed_seconds")),
                transport_timings=timings, timing_evidence_available=bool(diagnostics),
                error_category=category, authority="diagnostic_only")


def _usage(summary, ledger):
    fields = ("input_tokens", "cached_input_tokens", "output_tokens")
    total = count(summary["calls"])
    unknown = count(summary["unknown_calls"])
    complete = summary["usage_complete"]
    if type(complete) is not bool or unknown > total or complete != (unknown == 0):
        raise ValueError("inconsistent usage coverage")
    tokens = {k: count(summary[k]) for k in fields}
    if tokens["cached_input_tokens"] > tokens["input_tokens"]:
        raise ValueError("cached tokens exceed input")
    thinking = summary.get("thinking_tokens")
    if thinking is not None and count(thinking) > tokens["output_tokens"]:
        raise ValueError("thinking tokens exceed output")
    result = dict(calls=total, known_calls=total-unknown, unknown_calls=unknown, usage_complete=complete,
        **tokens, call_evidence="summary_only", retained_unknown_cny=None, protocol=None, routes=[],
        thinking=dict(known_tokens=thinking, reported_calls=None, total_calls=total, complete=False),
        known_peak_estimate_cny=str(amount(summary["known_peak_estimate_cny"])),
        known_idle_estimate_cny=(str(amount(summary["known_idle_estimate_cny"]))
                                 if summary.get("known_idle_estimate_cny") is not None else None),
        actual_billed_cny=None, sum_request_elapsed_seconds=None, timed_calls=0, tool_seconds=None,
        edit_rejections=None, compaction_events=None, call_details=None)
    if ledger is not None:
        records = ledger["records"]
        if (ledger.get("currency") != "CNY" or not isinstance(records, list) or len(records) != total
                or len({atom(r["id"]) for r in records}) != total):
            raise ValueError("per-call ledger inventory mismatch")
        known = [r for r in records if r["state"] == "known"]
        if len(known) != total-unknown or any(r["state"] not in ("known", "unknown") for r in records):
            raise ValueError("unclosed or conflicting call state")
        for key in fields:
            if sum(count(r[key]) for r in known) != tokens[key]:
                raise ValueError("summary and per-call token counters disagree")
        for r in known:
            if count(r["cached_input_tokens"]) > r["input_tokens"]:
                raise ValueError("cached tokens exceed call input")
            if r.get("thinking_tokens") is not None and count(r["thinking_tokens"]) > r["output_tokens"]:
                raise ValueError("thinking tokens exceed call output")
        cost = sum((amount(r["known_peak_estimate_cny"]) for r in known), Decimal(0))
        if cost != amount(result["known_peak_estimate_cny"]):
            raise ValueError("summary and per-call cost disagree")
        reported = [r["thinking_tokens"] for r in known if r.get("thinking_tokens") is not None]
        all_reported = len(reported) == len(known)
        if thinking is not None and (not all_reported or thinking != sum(reported)):
            raise ValueError("summary and per-call thinking disagree")
        timed = [seconds(r.get("elapsed_seconds")) for r in records if r.get("elapsed_seconds") is not None]
        result.update(call_evidence="per_call_checked", protocol=atom(ledger["protocol"]),
            retained_unknown_cny=str(sum((amount(r["reserved_cny"]) for r in records if r["state"] == "unknown"), Decimal(0))),
            thinking=dict(known_tokens=sum(reported) if reported else None, reported_calls=len(reported),
                          total_calls=total, complete=complete and len(reported) == total),
            known_peak_estimate_cny=str(cost), sum_request_elapsed_seconds=sum(timed) if timed else None,
            call_details=[call_detail(i, r) for i, r in enumerate(records)],
            timed_calls=len(timed), routes=(sorted({(atom(r["requested_model"]), atom(r["effort"])) for r in records})
                                           if all(r.get("requested_model") and r.get("effort") for r in records) else []))
        if all(r.get("known_idle_estimate_cny") is not None for r in known):
            idle = sum((amount(r["known_idle_estimate_cny"]) for r in known), Decimal(0))
            if result["known_idle_estimate_cny"] is not None and idle != amount(result["known_idle_estimate_cny"]):
                raise ValueError("summary and per-call idle estimate disagree")
            result["known_idle_estimate_cny"] = str(idle)
        else:
            result["known_idle_estimate_cny"] = None
    result["cache_hit_rate_known_including_first"] = (tokens["cached_input_tokens"] / tokens["input_tokens"]
                                                      if tokens["input_tokens"] else None)
    return result


def load_run(root):
    root = Path(root).resolve()
    report_path = root / "report.json"
    control = read_json(report_path) if report_path.is_file() else None
    synthetic = False
    if control is not None:
        if not isinstance(control, dict):
            raise ValueError("invalid report adapter")
        if "all_usage_synthetic" in control and type(control["all_usage_synthetic"]) is not bool:
            raise ValueError("invalid synthetic evidence marker")
        synthetic = control.get("all_usage_synthetic") is True
        if synthetic and "real_provider_calls" in control and count(control["real_provider_calls"]) != 0:
            raise ValueError("synthetic evidence contains real provider calls")
    board = root / "scoreboard.json"
    scoreboard_layout = board.is_file()
    if scoreboard_layout:
        data = read_json(board)
        entries = data["samples"]
        kind = "synthetic_preflight" if synthetic else "recorded_run"
        if synthetic:
            # Actual-entry controls publish the same scoreboard and nested wire
            # layout as paid runs. Storage layout cannot establish provenance.
            if (len(entries) != 1
                    or atom(control.get("case", "")).split("-", 1)[0] != entries[0]["sample"]["agent"]
                    or json.dumps(control.get("totals"), sort_keys=True, allow_nan=False)
                    != json.dumps(entries[0]["summary"], sort_keys=True, allow_nan=False)):
                raise ValueError("synthetic marker and scoreboard disagree")
    elif control is not None:
        board, data = report_path, control
        if not synthetic:
            raise ValueError("unsupported report adapter")
        ledger = read_json(root / "wire/ledger.json")
        agent = data["case"].split("-", 1)[0]
        entries = [dict(sample=dict(agent=agent, sample_id=ledger["sample"], task="synthetic-control",
                                   task_ref="synthetic-control"), summary=data["totals"],
                        deadline=data["operator_deadline"], official_reward=data.get("result", {}).get("verifier_result"),
                        normal_completion=data.get("native", {}).get("finish_reason") == "completed")]
        kind = "synthetic_preflight"
    else:
        plan = read_json(root / "plan.json")
        return [dict(agent=atom(a["agent"]), sample_id=atom(a["new_sample_id"]), task=atom(plan["target_task"]),
                     task_ref=atom(plan["task_ref"]), status="not_started", reward=None, calls=None,
                     evidence_kind="unstarted", source_sha256=fingerprint(root / "plan.json")) for a in plan["arms"]]
    rows = []
    for entry in entries:
        sample = entry["sample"]
        identifier = atom(sample["sample_id"])
        if "/" in identifier or identifier in (".", ".."):
            raise ValueError("unsafe sample identifier")
        path = root / "wire" / identifier / "ledger.json" if scoreboard_layout else root / "wire/ledger.json"
        if path.is_file() and (not path.resolve().is_relative_to(root) or path.resolve() != path):
            raise ValueError("ledger must not use symbolic links")
        ledger = read_json(path) if path.is_file() else None
        if ledger is not None and ledger["sample"] != identifier:
            raise ValueError("ledger sample mismatch")
        usage = _usage(entry["summary"], ledger)
        if ledger is not None:
            feedback = collect_tool_feedback(path.parent, ledger["records"], ledger["protocol"],
                                             root / "trials" / identifier / "agent/yhc.jsonl")
            usage.update(tool_feedback=feedback, edit_rejections=feedback["edit_rejections"])
        deadline = entry.get("deadline") or {}
        rows.append(dict(agent=atom(sample["agent"]), sample_id=identifier, task=atom(sample["task"]),
            task_ref=atom(sample["task_ref"]), evidence_kind=kind, source_sha256=fingerprint(board),
            classification_source_sha256=fingerprint(report_path) if synthetic else None,
            ledger_sha256=fingerprint(path) if ledger is not None else None,
            status="completed" if entry.get("normal_completion") is True else "interrupted_or_unverified",
            reward=reward(entry.get("official_reward")), agent_seconds=seconds(deadline.get("agent_elapsed_seconds")),
            terminal_reason=atom(deadline["reason"]) if deadline.get("reason") else None, **usage))
    return rows


def build_report(roots):
    rows = [row for root in roots for row in load_run(root)]
    if len({r["sample_id"] for r in rows}) != len(rows):
        raise ValueError("duplicate sample would double count")
    pairs = {}
    for row in rows:
        if row["evidence_kind"] != "recorded_run" or not row.get("routes"):
            continue
        key = (row["task_ref"], tuple(tuple(r) for r in row["routes"]))
        pairs.setdefault(key, []).append(row)
    return dict(schema_version=1, authority="diagnostic_only", samples=rows,
                paired_tasks=[dict(task_ref=k[0], samples=[r["sample_id"] for r in group])
                              for k, group in pairs.items() if {r["agent"] for r in group} >= {"dsh", "yhc"}],
                stability_established=False,
                limitations=["known_usage_cache_ratio_is_not_full_coverage_when_unknown_calls_exist",
                             "request_duration_sum_is_not_wall_time_or_tool_time",
                             "thinking_characters_are_not_token_counters",
                             "paired_aliases_do_not_pin_provider_weights_or_isolate_protocol_effects",
                             "operator_artifacts_are_not_provider_bills_or_resume_authority"])


def markdown(report):
    lines = ["# Offline harness comparison", "", "Diagnostic only; no stability or billing authority.", "",
             "| Agent | Sample | Evidence | Reward | Known/total calls | Known CNY (idle / peak) | Unknown reserve CNY | Cache, known calls | Known thinking tokens |",
             "|---|---|---|---:|---:|---:|---:|---:|---:|"]
    for r in report["samples"]:
        value = lambda key: "unavailable" if r.get(key) is None else str(r[key])
        cache = r.get("cache_hit_rate_known_including_first")
        thinking = r.get("thinking", {}).get("known_tokens")
        lines.append(f"| {r['agent']} | {r['sample_id']} | {r['evidence_kind']} | {value('reward')} | "
                     f"{value('known_calls')}/{value('calls')} | {value('known_idle_estimate_cny')} / {value('known_peak_estimate_cny')} | "
                     f"{value('retained_unknown_cny')} | "
                     f"{format(cache, '.2%') if cache is not None else 'unavailable'} | "
                     f"{thinking if thinking is not None else 'unavailable'} |")
    lines += ["", "Emitted tool requests and native terminal receipts have separate coverage; neither proves side effects.", "",
              "| Agent | Wire inventory | Emitted tool requests | Unmatched emitted calls | Edit completed/failed/emitted | Edit rejections |",
              "|---|---|---|---:|---:|---:|"]
    for r in report["samples"]:
        feedback = r.get("tool_feedback") or {}
        counts = feedback.get("emitted_counts")
        counts = ", ".join(f"{name}={count}" for name, count in sorted(counts.items())) if counts else (
            "0" if counts == {} else "unavailable")
        edit = feedback.get("edit") or {}
        value = lambda v: "unavailable" if v is None else str(v)
        lines.append(f"| {r['agent']} | {feedback.get('wire_inventory', 'unavailable')} | {counts} | "
                     f"{value(feedback.get('unmatched_emitted_calls'))} | "
                     f"{value(edit.get('completed'))}/{value(edit.get('failed'))}/{value(edit.get('emitted'))} "
                     f"({edit.get('coverage', 'unavailable')}) | {value(r.get('edit_rejections'))} |")
    return "\n".join(lines) + "\n"


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--run", type=Path, action="append", required=True)
    parser.add_argument("--output", type=Path, required=True)
    parser.add_argument("--markdown", type=Path)
    args = parser.parse_args()
    report = build_report(args.run)
    # Reject both destination collisions before publishing either report.
    if args.output.exists() or args.markdown is not None and (args.markdown.exists() or args.markdown == args.output):
        raise FileExistsError("report destination already exists")
    with args.output.open("x", encoding="utf-8") as stream:
        json.dump(report, stream, indent=2, allow_nan=False)
        stream.write("\n")
    if args.markdown is not None:
        with args.markdown.open("x", encoding="utf-8") as stream:
            stream.write(markdown(report))


if __name__ == "__main__":
    main()
