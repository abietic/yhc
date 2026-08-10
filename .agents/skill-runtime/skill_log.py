#!/usr/bin/env python3
"""Append-only, privacy-conscious execution logs for project skills."""

from __future__ import annotations

import argparse
from contextlib import contextmanager
import datetime as dt
import json
import math
import os
from pathlib import Path
import re
import stat
import subprocess
import sys
import uuid
from typing import Any

if os.name == "nt":
    import msvcrt
else:
    import fcntl


DEFAULT_KEEP = 2000
# Logs created before schema v2 used this default without recording it in the
# run_started event. Keep it as a conservative censor diagnostic for retained
# histories that stop exactly at the former boundary.
LEGACY_RETENTION_KEEPS = (200,)
SKILL_NAME = re.compile(r"^[a-z0-9][a-z0-9-]{0,63}$")
EVENT_NAME = re.compile(r"^[a-z][a-z0-9_]{0,63}$")
SAFE_ATOM = re.compile(r"^[A-Za-z0-9._/@:+#=,~?&%\\-]{1,256}$")
RESERVED_EVENTS = frozenset(("run_started", "run_finished"))
CORRELATION_FIELDS = ("session_id", "thread_id", "turn_id", "goal_id", "task_id")
USAGE_FIELDS = (
    "input_tokens",
    "output_tokens",
    "cached_input_tokens",
    "reasoning_tokens",
    "total_tokens",
)
TOKEN_FIELDS = USAGE_FIELDS + ("parent_total_tokens", "baseline_parent_total_tokens")
BASELINE_KINDS = ("historical", "parallel-control", "manual-estimate")
MEASURED_BASELINE_KINDS = ("historical", "parallel-control")
MEASUREMENT_MODES = ("audit", "roi")
REPORT_PROFILES = ("operational", "delegation", "roi", "full")
TASK_FAMILIES = (
    "exploration",
    "review",
    "implementation",
    "testing",
    "documentation",
    "other",
)
DEFAULT_TASK_FAMILY = {
    "terra_explorer": "exploration",
    "terra_reviewer": "review",
    "terra_worker": "implementation",
    "other": "other",
}
LEGACY_SKILL_ALIASES = {
    "eino-defect-investigation": "defect-investigation",
    "eino-iteration-closeout": "iteration-closeout",
    "eino-migration-slice": "migration-slice",
    "eino-reference-parity-audit": "reference-parity-audit",
    "eino-runtime-depth-change": "runtime-depth-change",
    "eino-skill-runtime": "skill-runtime",
    "eino-tui-runtime-change": "tui-runtime-change",
    "eino-write-docs": "write-docs",
}
SENSITIVE_KEY = re.compile(
    r"(?:authorization|cookie|credential|password|prompt|secret|token|api[_-]?key)",
    re.IGNORECASE,
)
CONTENT_KEY = re.compile(
    r"^(?:body|command|content|message|output|prompt|response|source|stderr|stdout|"
    r"summary|text|transcript)$",
    re.IGNORECASE,
)
INLINE_SECRET = re.compile(
    r"(?i)\b(authorization|cookie|credential|password|secret|token|api[_-]?key)"
    r"\s*[:=]\s*(?:bearer\s+)?[^\s,;]+"
)
SAFE_DATA_STRING_FIELDS = frozenset(
    (
        "acceptance_id",
        "artifact_id",
        "branch",
        "category",
        "classification",
        "commit",
        "contract_id",
        "decision",
        "delegated_skill",
        "failure_category",
        "gate",
        "gate_name",
        "gate_result",
        "kind",
        "mode",
        "outcome",
        "owner",
        "path",
        "phase",
        "plan_item",
        "plan_item_id",
        "pr",
        "reason_category",
        "reference",
        "result",
        "signal",
        "skill",
        "status",
        "subsystem",
        "symbol",
        "test",
        "test_kind",
        "unresolved_id",
        "validation_status",
        "version",
    )
)
SAFE_DATA_LIST_FIELDS = frozenset(
    (
        "acceptance_ids",
        "artifact_ids",
        "changed_paths",
        "classifications",
        "contract_ids",
        "delegated_skills",
        "evidence_paths",
        "failure_categories",
        "files",
        "gate_names",
        "gates",
        "owners",
        "paths",
        "plan_item_ids",
        "plan_items",
        "pty_scenarios",
        "references",
        "subsystems",
        "symbols",
        "test_kinds",
        "tests",
        "unresolved_ids",
        "widths",
    )
)
SAFE_DATA_NUMBER_FIELDS = frozenset(
    (
        "changed_lines",
        "count",
        "duration_seconds",
        "elapsed_seconds",
        "line_count",
        "wall_seconds",
        "width",
    )
)
SAFE_DATA_BOOLEAN_FIELDS = frozenset(("changed", "passed", "ready", "verified"))
SAFE_DATA_NUMBER_MAP_FIELDS = frozenset(("classification_counts", "observed_metrics"))
SAFE_DATA_STRING_MAP_FIELDS = frozenset(("gate_results",))
MAX_DATA_FIELDS = 32
MAX_DATA_COLLECTION = 100


def default_log_dir() -> Path:
    override = os.environ.get("EINO_SKILL_LOG_DIR")
    if override:
        return Path(override).expanduser()
    if sys.platform == "darwin":
        return Path.home() / "Library" / "Logs" / "eino-agent-skills"
    state_home = Path(
        os.environ.get("XDG_STATE_HOME", Path.home() / ".local" / "state")
    ).expanduser()
    return state_home / "eino-agent-skills"


def utc_now() -> str:
    return dt.datetime.now(dt.timezone.utc).isoformat(timespec="milliseconds")


def redact(value: Any, key: str = "") -> Any:
    if CONTENT_KEY.fullmatch(key):
        return "[REDACTED_TEXT]" if value not in (None, "") else value
    if SENSITIVE_KEY.search(key):
        if key in TOKEN_FIELDS and isinstance(value, (int, float)):
            try:
                finite_token = (
                    not isinstance(value, bool)
                    and math.isfinite(float(value))
                    and value >= 0
                )
            except OverflowError:
                finite_token = False
            if finite_token:
                return value
        return "[REDACTED]"
    if isinstance(value, dict):
        return {str(k): redact(v, str(k)) for k, v in value.items()}
    if isinstance(value, list):
        redacted: list[Any] = []
        redact_next = False
        for item in value:
            if redact_next:
                redacted.append("[REDACTED]")
                redact_next = False
                continue
            redacted.append(redact(item))
            if isinstance(item, str):
                flag = item.lstrip("-").replace("_", "-")
                redact_next = bool(SENSITIVE_KEY.search(flag)) and "=" not in item
        return redacted
    if isinstance(value, str):
        return INLINE_SECRET.sub(lambda match: f"{match.group(1)}=[REDACTED]", value)
    return value


def git_snapshot(cwd: Path) -> dict[str, Any]:
    def run(*args: str) -> str:
        completed = subprocess.run(
            ["git", *args],
            cwd=cwd,
            text=True,
            stdout=subprocess.PIPE,
            stderr=subprocess.DEVNULL,
            check=False,
        )
        return completed.stdout.rstrip("\n") if completed.returncode == 0 else ""

    head = run("rev-parse", "--short", "HEAD").strip()
    status = run("status", "--short")
    dirty_paths = []
    for line in status.splitlines():
        if len(line) >= 4:
            dirty_paths.append(line[3:])
    return {"head": head, "dirty_paths": dirty_paths}


def ensure_private_dir(path: Path) -> None:
    path.mkdir(parents=True, exist_ok=True, mode=0o700)
    try:
        path.chmod(0o700)
    except OSError:
        pass


def validate_skill_name(name: str) -> None:
    if not SKILL_NAME.fullmatch(name):
        raise ValueError("--skill must contain only lowercase letters, digits, and hyphens")


def ensure_log_boundary(path: Path, log_root: Path) -> tuple[Path, Path]:
    resolved_path = path.expanduser().resolve()
    resolved_root = log_root.expanduser().resolve()
    try:
        resolved_path.relative_to(resolved_root)
    except ValueError as exc:
        raise ValueError(f"execution log escapes log root: {resolved_path}") from exc
    if path.is_symlink():
        raise ValueError(f"execution log must not be a symlink: {path}")
    return resolved_path, resolved_root


@contextmanager
def exclusive_lock(handle: Any) -> Any:
    if os.name == "nt":
        handle.seek(0)
        msvcrt.locking(handle.fileno(), msvcrt.LK_LOCK, 1)
        try:
            yield
        finally:
            handle.seek(0)
            msvcrt.locking(handle.fileno(), msvcrt.LK_UNLCK, 1)
    else:
        fcntl.flock(handle.fileno(), fcntl.LOCK_EX)
        try:
            yield
        finally:
            fcntl.flock(handle.fileno(), fcntl.LOCK_UN)


def append_event(
    path: Path,
    run_id: str,
    name: str,
    schema_version: int = 2,
    **fields: Any,
) -> None:
    payload = redact(
        {
            "schema_version": schema_version,
            "timestamp": utc_now(),
            "run_id": run_id,
            "event": name,
            **fields,
        }
    )
    flags = os.O_WRONLY | os.O_CREAT | os.O_APPEND
    fd = os.open(path, flags, 0o600)
    os.fchmod(fd, stat.S_IRUSR | stat.S_IWUSR)
    with os.fdopen(fd, "a", encoding="utf-8") as handle:
        handle.write(json.dumps(payload, ensure_ascii=False, sort_keys=True) + "\n")


def parse_time(value: Any) -> dt.datetime:
    text = str(value).replace("Z", "+00:00")
    parsed = dt.datetime.fromisoformat(text)
    if parsed.tzinfo is None:
        parsed = parsed.replace(tzinfo=dt.timezone.utc)
    return parsed


def parse_log(lines: list[str], run_id: str) -> list[dict[str, Any]]:
    if not lines:
        raise ValueError("execution log is empty")
    events = [json.loads(line) for line in lines]
    if not all(isinstance(event, dict) and event.get("run_id") == run_id for event in events):
        raise ValueError("run id does not match every execution log event")
    first = events[0]
    if first.get("event") != "run_started" or first.get("run_id") != run_id:
        raise ValueError("run id does not match the execution log")
    return events


def validate_log_payload(
    lines: list[str],
    run_id: str,
    allow_terminal: bool = False,
) -> list[dict[str, Any]]:
    events = parse_log(lines, run_id)
    if not allow_terminal and any(event.get("event") == "run_finished" for event in events):
        raise ValueError("execution log is already finished")
    return events


def validate_open_log(path: Path, run_id: str, log_root: Path | None = None) -> None:
    if log_root is not None:
        path, _ = ensure_log_boundary(path, log_root)
    if not path.is_file():
        raise ValueError(f"execution log does not exist: {path}")
    lines = path.read_text(encoding="utf-8").splitlines()
    validate_log_payload(lines, run_id)


def append_existing_event(
    path: Path,
    log_root: Path,
    run_id: str,
    name: str,
    allow_terminal: bool = False,
    check: Any = None,
    **fields: Any,
) -> None:
    if not EVENT_NAME.fullmatch(name):
        raise ValueError("event name must be lowercase snake_case")
    if name in RESERVED_EVENTS:
        raise ValueError(f"reserved event name is not allowed here: {name}")
    path, _ = ensure_log_boundary(path, log_root)
    if not path.is_file():
        raise ValueError(f"execution log does not exist: {path}")
    payload = redact(
        {
            "schema_version": 2,
            "timestamp": utc_now(),
            "run_id": run_id,
            "event": name,
            **fields,
        }
    )
    with path.open("r+", encoding="utf-8") as handle:
        with exclusive_lock(handle):
            lines = handle.read().splitlines()
            events = validate_log_payload(lines, run_id, allow_terminal)
            if check:
                check(events)
            handle.seek(0, os.SEEK_END)
            handle.write(json.dumps(payload, ensure_ascii=False, sort_keys=True) + "\n")
            handle.flush()
            os.fsync(handle.fileno())


def prune(directory: Path, keep: int) -> None:
    logs = sorted(
        directory.glob("*.jsonl"),
        key=lambda path: path.stat().st_mtime,
        reverse=True,
    )
    for old in logs[max(1, keep) :]:
        try:
            old.unlink()
        except OSError:
            pass


def safe_atom(value: Any, label: str, *, allow_empty: bool = False) -> str:
    if allow_empty and value == "":
        return ""
    if not isinstance(value, str) or not SAFE_ATOM.fullmatch(value):
        raise ValueError(
            f"{label} must be a non-sensitive identifier or path without whitespace"
        )
    return value


def safe_atom_list(value: Any, label: str) -> list[str]:
    if not isinstance(value, list) or len(value) > MAX_DATA_COLLECTION:
        raise ValueError(f"{label} must be a list of at most {MAX_DATA_COLLECTION} atoms")
    return [safe_atom(item, label) for item in value]


def safe_number(value: Any, label: str) -> float | int:
    if isinstance(value, bool) or not isinstance(value, (int, float)):
        raise ValueError(f"{label} must be a finite number")
    try:
        finite = math.isfinite(float(value))
    except OverflowError:
        finite = False
    if not finite:
        raise ValueError(f"{label} must be a finite number")
    return value


def safe_data_map(value: Any, label: str, *, strings: bool) -> dict[str, Any]:
    if not isinstance(value, dict) or len(value) > MAX_DATA_COLLECTION:
        raise ValueError(
            f"{label} must be an object with at most {MAX_DATA_COLLECTION} fields"
        )
    result: dict[str, Any] = {}
    for key, item in value.items():
        if not isinstance(key, str) or not EVENT_NAME.fullmatch(key):
            raise ValueError(f"{label} contains an unsafe metric name")
        if strings:
            result[key] = safe_atom(item, f"{label}.{key}")
        elif isinstance(item, bool):
            result[key] = item
        else:
            result[key] = safe_number(item, f"{label}.{key}")
    return result


def sanitize_data(data: dict[str, Any]) -> dict[str, Any]:
    if len(data) > MAX_DATA_FIELDS:
        raise ValueError(f"--data-json allows at most {MAX_DATA_FIELDS} fields")
    result: dict[str, Any] = {}
    for key, value in data.items():
        if key in SAFE_DATA_STRING_FIELDS:
            result[key] = safe_atom(value, f"--data-json.{key}")
        elif key in SAFE_DATA_LIST_FIELDS:
            result[key] = safe_atom_list(value, f"--data-json.{key}")
        elif key in SAFE_DATA_NUMBER_FIELDS:
            result[key] = numeric(value, f"data-json.{key}")
        elif key in SAFE_DATA_BOOLEAN_FIELDS:
            if not isinstance(value, bool):
                raise ValueError(f"--data-json.{key} must be a boolean")
            result[key] = value
        elif key in SAFE_DATA_NUMBER_MAP_FIELDS:
            result[key] = safe_data_map(
                value,
                f"--data-json.{key}",
                strings=False,
            )
        elif key in SAFE_DATA_STRING_MAP_FIELDS:
            result[key] = safe_data_map(
                value,
                f"--data-json.{key}",
                strings=True,
            )
        else:
            raise ValueError(f"unsupported --data-json field: {key}")
    return result


def parse_data(raw: str | None) -> dict[str, Any]:
    if not raw:
        return {}
    parsed = json.loads(raw)
    if not isinstance(parsed, dict):
        raise ValueError("--data-json must decode to an object")
    return sanitize_data(parsed)


def cmd_start(args: argparse.Namespace) -> int:
    if args.keep <= 0:
        raise ValueError("--keep must be positive")
    validate_skill_name(args.skill)
    kind = safe_atom(args.kind, "--kind")
    scope = safe_atom_list(args.scope, "--scope")
    parent_run_id = safe_atom(
        args.parent_run_id,
        "--parent-run-id",
        allow_empty=True,
    )
    root_run_id = safe_atom(
        args.root_run_id,
        "--root-run-id",
        allow_empty=True,
    )
    invoked_by = safe_atom(args.invoked_by, "--invoked-by")
    measurement_mode = getattr(args, "measurement_mode", "audit")
    if measurement_mode not in MEASUREMENT_MODES:
        raise ValueError("--measurement-mode must be audit or roi")
    experiment_id = safe_atom(
        getattr(args, "experiment_id", ""),
        "--experiment-id",
        allow_empty=True,
    )
    if measurement_mode == "roi" and not experiment_id:
        raise ValueError("--measurement-mode roi requires --experiment-id")
    if measurement_mode != "roi" and experiment_id:
        raise ValueError("--experiment-id is only valid with --measurement-mode roi")
    run_id = uuid.uuid4().hex
    root = args.log_dir.expanduser().resolve()
    directory = root / args.skill
    ensure_private_dir(directory)
    stamp = dt.datetime.now().strftime("%Y%m%d-%H%M%S")
    log_file = directory / f"{stamp}-{run_id}.jsonl"
    cwd = args.cwd.expanduser().resolve()
    correlation = {
        field: safe_atom(
            getattr(args, field, "")
            or os.environ.get(f"CODEX_{field.upper()}", ""),
            f"--{field.replace('_', '-')}",
            allow_empty=True,
        )
        for field in CORRELATION_FIELDS
    }
    append_event(
        log_file,
        run_id,
        "run_started",
        skill=args.skill,
        kind=kind,
        cwd=str(cwd),
        scope=scope,
        parent_run_id=parent_run_id,
        root_run_id=root_run_id or parent_run_id or run_id,
        invoked_by=invoked_by,
        measurement_mode=measurement_mode,
        experiment_id=experiment_id,
        retention_keep=args.keep,
        git=git_snapshot(cwd),
        **correlation,
    )
    prune(directory, args.keep)
    print(json.dumps({"run_id": run_id, "log_file": str(log_file)}, ensure_ascii=False))
    return 0


def cmd_event(args: argparse.Namespace) -> int:
    data = parse_data(args.data_json)
    append_existing_event(
        args.log_file,
        args.log_dir,
        args.run_id,
        args.name,
        status=args.status,
        data=data,
    )
    return 0


def cmd_finish(args: argparse.Namespace) -> int:
    data = parse_data(args.data_json)
    category = safe_atom(args.category, "--category", allow_empty=True)
    log_file, _ = ensure_log_boundary(args.log_file, args.log_dir)
    if not log_file.is_file():
        raise ValueError(f"execution log does not exist: {log_file}")
    snapshot = git_snapshot(args.cwd.expanduser().resolve())
    with log_file.open("r+", encoding="utf-8") as handle:
        with exclusive_lock(handle):
            lines = handle.read().splitlines()
            events = validate_log_payload(lines, args.run_id)
            started_at = parse_time(events[0]["timestamp"])
            wall_seconds = max(
                0.0,
                (dt.datetime.now(dt.timezone.utc) - started_at).total_seconds(),
            )
            payload = redact(
                {
                    "schema_version": 2,
                    "timestamp": utc_now(),
                    "run_id": args.run_id,
                    "event": "run_finished",
                    "outcome": args.outcome,
                    "category": category,
                    "summary": args.summary,
                    "data": data,
                    "git": snapshot,
                    "wall_seconds": wall_seconds,
                }
            )
            handle.seek(0, os.SEEK_END)
            handle.write(json.dumps(payload, ensure_ascii=False, sort_keys=True) + "\n")
            handle.flush()
            os.fsync(handle.fileno())
    return 0


def numeric(value: Any, name: str, integer: bool = False) -> float | int:
    option = f"--{name.replace('_', '-')}"
    if integer:
        if isinstance(value, bool):
            raise ValueError(f"{option} must be a non-negative integer")
        if isinstance(value, int):
            if value < 0:
                raise ValueError(f"{option} must be a non-negative integer")
            return value
        if isinstance(value, float):
            if not math.isfinite(value) or value < 0 or not value.is_integer():
                raise ValueError(f"{option} must be a non-negative integer")
            return int(value)
        if isinstance(value, str) and value.isdigit():
            return int(value)
        raise ValueError(f"{option} must be a non-negative integer")
    if isinstance(value, bool):
        raise ValueError(f"{option} must be a non-negative finite number")
    try:
        result = float(value)
    except (TypeError, ValueError, OverflowError) as exc:
        raise ValueError(f"{option} must be a non-negative finite number") from exc
    if not math.isfinite(result) or result < 0:
        raise ValueError(f"{option} must be a non-negative finite number")
    return result


def delegation_index(events: list[dict[str, Any]]) -> dict[str, list[dict[str, Any]]]:
    result: dict[str, list[dict[str, Any]]] = {}
    for event in events:
        delegation_id = event.get("delegation_id")
        if isinstance(delegation_id, str):
            result.setdefault(delegation_id, []).append(event)
    return result


def cmd_delegate_start(args: argparse.Namespace) -> int:
    if not SKILL_NAME.fullmatch(args.task_kind):
        raise ValueError("--task-kind must be safe lowercase hyphen form")
    scope = safe_atom_list(args.scope, "--scope")
    child_task_id = safe_atom(
        args.child_task_id,
        "--child-task-id",
        allow_empty=True,
    )
    child_session_id = safe_atom(
        args.child_session_id,
        "--child-session-id",
        allow_empty=True,
    )
    task_family = getattr(args, "task_family", "") or DEFAULT_TASK_FAMILY[args.executor]
    if task_family not in TASK_FAMILIES:
        raise ValueError(
            "--task-family must be exploration, review, implementation, testing, "
            "documentation, or other"
        )
    delegation_id = uuid.uuid4().hex
    append_existing_event(
        args.log_file,
        args.log_dir,
        args.run_id,
        "delegation_started",
        delegation_id=delegation_id,
        executor=args.executor,
        task_kind=args.task_kind,
        task_family=task_family,
        mode=args.mode,
        scope=scope,
        child_task_id=child_task_id,
        child_session_id=child_session_id,
    )
    print(json.dumps({"delegation_id": delegation_id}, sort_keys=True))
    return 0


def cmd_delegate_finish(args: argparse.Namespace) -> int:
    delegation_id = safe_atom(args.delegation_id, "--delegation-id")
    child_task_id = safe_atom(
        args.child_task_id,
        "--child-task-id",
        allow_empty=True,
    )
    child_session_id = safe_atom(
        args.child_session_id,
        "--child-session-id",
        allow_empty=True,
    )
    patch_sha256 = safe_atom(
        args.patch_sha256,
        "--patch-sha256",
        allow_empty=True,
    )

    def check(events: list[dict[str, Any]]) -> None:
        related = delegation_index(events).get(delegation_id, [])
        if not any(event.get("event") == "delegation_started" for event in related):
            raise ValueError("unknown delegation id")
        if any(event.get("event") == "delegation_finished" for event in related):
            raise ValueError("delegation is already finished")

    fields = {
        name: numeric(getattr(args, name), name, name in USAGE_FIELDS)
        for name in ("duration_seconds", *USAGE_FIELDS)
        if getattr(args, name) is not None
    }
    if args.changed_lines is not None:
        fields["changed_lines"] = numeric(args.changed_lines, "changed_lines", True)
    append_existing_event(
        args.log_file,
        args.log_dir,
        args.run_id,
        "delegation_finished",
        delegation_id=delegation_id,
        outcome=args.outcome,
        child_task_id=child_task_id,
        child_session_id=child_session_id,
        patch_sha256=patch_sha256,
        check=check,
        **fields,
    )
    return 0


def cmd_delegate_assess(args: argparse.Namespace) -> int:
    delegation_id = safe_atom(args.delegation_id, "--delegation-id")
    reason_category = safe_atom(
        args.reason_category,
        "--reason-category",
        allow_empty=True,
    )
    commit = safe_atom(args.commit, "--commit", allow_empty=True)
    pr = safe_atom(args.pr, "--pr", allow_empty=True)
    baseline_ref = safe_atom(
        args.baseline_ref,
        "--baseline-ref",
        allow_empty=True,
    )

    def check(events: list[dict[str, Any]]) -> None:
        related = delegation_index(events).get(delegation_id, [])
        if not any(event.get("event") == "delegation_finished" for event in related):
            raise ValueError("delegation must finish before assessment")

    fields = {
        name: numeric(getattr(args, name), name)
        for name in ("parent_wait_seconds", "review_seconds", "rework_seconds")
        if getattr(args, name) is not None
    }
    baseline = (
        "baseline_kind",
        "baseline_wall_seconds",
        "baseline_parent_total_tokens",
        "baseline_ref",
    )
    baseline_values = {
        "baseline_kind": args.baseline_kind,
        "baseline_wall_seconds": args.baseline_wall_seconds,
        "baseline_parent_total_tokens": args.baseline_parent_total_tokens,
        "baseline_ref": baseline_ref,
    }
    supplied = [baseline_values[name] not in (None, "") for name in baseline]
    if any(supplied) and (
        not all(supplied)
        or args.task_wall_seconds is None
        or args.parent_total_tokens is None
    ):
        raise ValueError(
            "baseline fields require a complete baseline and actual task metrics"
        )
    if any(supplied) and args.baseline_kind not in BASELINE_KINDS:
        raise ValueError(
            "--baseline-kind must be historical, parallel-control, or manual-estimate"
        )
    if args.task_wall_seconds is not None:
        fields["task_wall_seconds"] = numeric(args.task_wall_seconds, "task_wall_seconds")
    if args.parent_total_tokens is not None:
        fields["parent_total_tokens"] = numeric(
            args.parent_total_tokens,
            "parent_total_tokens",
            True,
        )
    if any(supplied):
        fields.update(
            {
                "baseline_kind": args.baseline_kind,
                "baseline_ref": baseline_ref,
                "baseline_wall_seconds": numeric(
                    args.baseline_wall_seconds,
                    "baseline_wall_seconds",
                ),
                "baseline_parent_total_tokens": numeric(
                    args.baseline_parent_total_tokens,
                    "baseline_parent_total_tokens",
                    True,
                ),
            }
        )
    append_existing_event(
        args.log_file,
        args.log_dir,
        args.run_id,
        "delegation_assessed",
        allow_terminal=True,
        delegation_id=delegation_id,
        disposition=args.disposition,
        reason_category=reason_category,
        commit=commit,
        pr=pr,
        check=check,
        **fields,
    )
    return 0


def percentile(values: list[float], fraction: float) -> float | None:
    if not values:
        return None
    return sorted(values)[max(0, math.ceil(len(values) * fraction) - 1)]


def coverage(items: list[dict[str, Any]], field: str) -> dict[str, Any]:
    count = sum(field in item and item[field] not in (None, "") for item in items)
    total = len(items)
    return {"count": count, "total": total, "percent": (count * 100 / total if total else 0.0)}


def scalar_counts(values: list[dict[str, Any]], field: str) -> dict[str, int]:
    counted = [
        value
        for event in values
        for value in [report_atom(event.get(field))]
        if value is not None
    ]
    return {value: counted.count(value) for value in sorted(set(counted))}


def valid_number(value: Any) -> float | None:
    if isinstance(value, bool) or not isinstance(value, (int, float)):
        return None
    try:
        result = float(value)
    except OverflowError:
        return None
    return result if math.isfinite(result) and result >= 0 else None


def valid_integer(value: Any) -> int | None:
    if isinstance(value, bool):
        return None
    if isinstance(value, int):
        return value if value >= 0 else None
    if isinstance(value, float):
        if math.isfinite(value) and value >= 0 and value.is_integer():
            return int(value)
    return None


def complete_baseline(event: dict[str, Any]) -> bool:
    baseline_kind = event.get("baseline_kind")
    return (
        baseline_kind in BASELINE_KINDS
        and isinstance(event.get("baseline_ref"), str)
        and bool(event["baseline_ref"])
        and valid_number(event.get("baseline_wall_seconds")) is not None
        and valid_number(event.get("task_wall_seconds")) is not None
        and valid_integer(event.get("baseline_parent_total_tokens")) is not None
        and valid_integer(event.get("parent_total_tokens")) is not None
    )


def valid_coverage(
    items: list[dict[str, Any]],
    field: str,
    *,
    integer: bool = False,
) -> dict[str, Any]:
    validator = valid_integer if integer else valid_number
    count = sum(validator(item.get(field)) is not None for item in items)
    total = len(items)
    return {
        "count": count,
        "total": total,
        "percent": count * 100 / total if total else 0.0,
    }


def ratio_percent(numerator: int, denominator: int) -> float:
    return numerator * 100 / denominator if denominator else 0.0


def comparison_summary(events: list[dict[str, Any]]) -> dict[str, Any]:
    wall_deltas: list[float] = []
    token_deltas: list[float] = []
    for event in events:
        baseline_wall = valid_number(event.get("baseline_wall_seconds"))
        task_wall = valid_number(event.get("task_wall_seconds"))
        baseline_tokens = valid_integer(event.get("baseline_parent_total_tokens"))
        parent_tokens = valid_integer(event.get("parent_total_tokens"))
        if None in (baseline_wall, task_wall, baseline_tokens, parent_tokens):
            continue
        wall_deltas.append(baseline_wall - task_wall)
        token_deltas.append(float(baseline_tokens - parent_tokens))
    return {
        "count": len(wall_deltas),
        "baseline_kinds": scalar_counts(events, "baseline_kind"),
        "wall_seconds_delta": sum(wall_deltas),
        "wall_seconds_delta_p50": percentile(wall_deltas, 0.5),
        "parent_total_tokens_delta": int(sum(token_deltas)),
        "parent_total_tokens_delta_p50": percentile(token_deltas, 0.5),
        "positive_delta_means_savings": True,
    }


def dimension_summary(
    states: list[dict[str, Any]],
    field: str,
    diagnostics: dict[str, int] | None = None,
) -> dict[str, dict[str, Any]]:
    groups: dict[str, list[dict[str, Any]]] = {}
    for state in states:
        raw_value = state.get("start", {}).get(field)
        value = report_atom(raw_value)
        if value is None:
            if raw_value not in (None, "") and diagnostics is not None:
                diagnostics["unsafe_dimension_values"] += 1
            continue
        groups.setdefault(value, []).append(state)
    result: dict[str, dict[str, Any]] = {}
    for name, grouped_states in sorted(groups.items()):
        durations = [
            state["effective_duration"]
            for state in grouped_states
            if state["effective_duration"] is not None
        ]
        success = sum(
            state["finish"].get("outcome") == "success"
            for state in grouped_states
        )
        result[name] = {
            "finished": len(grouped_states),
            "success": success,
            "success_rate": ratio_percent(success, len(grouped_states)),
            "outcomes": scalar_counts(
                [state["finish"] for state in grouped_states],
                "outcome",
            ),
            "effective_duration_seconds": {
                "sum": sum(durations),
                "p50": percentile(durations, 0.5),
                "p90": percentile(durations, 0.9),
            },
        }
    return result


def report_atom(value: Any) -> str | None:
    """Return a bounded, non-sensitive dimension value suitable for reports."""
    if not isinstance(value, str) or not value or len(value) > 64:
        return None
    return value if SAFE_ATOM.fullmatch(value) else None


def canonical_skill_name(value: Any) -> Any:
    if not isinstance(value, str):
        return value
    return LEGACY_SKILL_ALIASES.get(value, value)


def run_dimension_summary(
    states: list[dict[str, Any]],
    value_for: Any,
) -> dict[str, dict[str, Any]]:
    groups: dict[str, list[dict[str, Any]]] = {}
    for state in states:
        name = report_atom(value_for(state))
        if name is not None:
            groups.setdefault(name, []).append(state)
    result: dict[str, dict[str, Any]] = {}
    for name, grouped in sorted(groups.items()):
        finished = [state for state in grouped if state["finish"] is not None]
        walls = [
            state["wall_seconds"]
            for state in finished
            if state["wall_seconds"] is not None
        ]
        success = sum(
            state["finish"].get("outcome") == "success" for state in finished
        )
        result[name] = {
            "total": len(grouped),
            "finished": len(finished),
            "success": success,
            "success_rate": ratio_percent(success, len(finished)),
            "outcomes": scalar_counts(
                [state["finish"] for state in finished], "outcome"
            ),
            "wall_seconds": {
                "sum": sum(walls),
                "p50": percentile(walls, 0.5),
                "p90": percentile(walls, 0.9),
            },
        }
    return result


def retention_summary(runs: list[list[dict[str, Any]]]) -> dict[str, Any]:
    counts: dict[str, int] = {}
    limits: dict[str, set[int]] = {}
    legacy_skills: set[str] = set()
    for run in runs:
        start = run[0]
        skill = report_atom(start.get("skill"))
        if skill is None:
            continue
        counts[skill] = counts.get(skill, 0) + 1
        keep = valid_integer(start.get("retention_keep"))
        if keep is not None and keep > 0:
            limits.setdefault(skill, set()).add(keep)
        if start.get("schema_version") != 2 or keep is None:
            legacy_skills.add(skill)

    observed = sorted({keep for values in limits.values() for keep in values})
    reached = sorted(
        {
            keep
            for skill, count in counts.items()
            for keep in (
                limits.get(skill, set())
                | (set(LEGACY_RETENTION_KEEPS) if skill in legacy_skills else set())
            )
            if count >= keep
        }
    )
    return {
        "observed_keep": observed,
        "retention_keep_values": observed,
        "reached_keep_limits": reached,
        "potentially_censored": bool(reached),
    }


def event_time(event: dict[str, Any]) -> dt.datetime | None:
    try:
        return parse_time(event.get("timestamp"))
    except (TypeError, ValueError):
        return None


def stale(events: list[dict[str, Any]], now: dt.datetime, threshold: float) -> bool:
    times = [value for value in (event_time(event) for event in events) if value]
    return bool(times) and (now - max(times)).total_seconds() > threshold


def delegation_data_quality(states: list[dict[str, Any]]) -> dict[str, Any]:
    finishes = [state["finish"] for state in states]
    assessed = [state for state in states if "assessment" in state]
    assessments = [state["assessment"] for state in assessed]
    quality = {
        key: coverage(finishes, key)
        for key in ("child_session_id", "patch_sha256")
    }
    quality["changed_lines"] = valid_coverage(
        finishes,
        "changed_lines",
        integer=True,
    )
    quality["explicit_duration"] = valid_coverage(finishes, "duration_seconds")
    for field in USAGE_FIELDS:
        quality[field] = valid_coverage(finishes, field, integer=True)
    effective_timing_count = sum(
        state.get("effective_duration") is not None for state in states
    )
    quality["effective_timing"] = {
        "count": effective_timing_count,
        "total": len(states),
        "percent": ratio_percent(effective_timing_count, len(states)),
    }
    quality["assessment"] = {
        "count": len(assessed),
        "total": len(states),
        "percent": ratio_percent(len(assessed), len(states)),
    }
    commit_pr_count = sum(
        bool(event.get("commit") or event.get("pr")) for event in assessments
    )
    quality["commit_pr"] = {
        "count": commit_pr_count,
        "total": len(states),
        "percent": ratio_percent(commit_pr_count, len(states)),
    }
    effort_keys = ("parent_wait_seconds", "review_seconds", "rework_seconds")
    parent_effort_count = sum(
        all(valid_number(event.get(key)) is not None for key in effort_keys)
        for event in assessments
    )
    quality["parent_effort"] = {
        "count": parent_effort_count,
        "total": len(states),
        "percent": ratio_percent(parent_effort_count, len(states)),
    }
    complete_baseline_count = sum(complete_baseline(event) for event in assessments)
    measured_baseline_count = sum(
        complete_baseline(event)
        and event.get("baseline_kind") in MEASURED_BASELINE_KINDS
        for event in assessments
    )
    quality["complete_baseline"] = {
        "count": complete_baseline_count,
        "total": len(states),
        "percent": ratio_percent(complete_baseline_count, len(states)),
    }
    quality["measured_baseline"] = {
        "count": measured_baseline_count,
        "total": len(states),
        "percent": ratio_percent(measured_baseline_count, len(states)),
    }
    return quality


def roi_readiness(
    started: list[dict[str, Any]],
    ended: list[dict[str, Any]],
    open_delegations: list[dict[str, Any]],
    roi_runs: list[list[dict[str, Any]]],
) -> tuple[bool, bool, bool, bool, list[str]]:
    effort_keys = ("parent_wait_seconds", "review_seconds", "rework_seconds")
    open_runs = [
        run
        for run in roi_runs
        if not any(event.get("event") == "run_finished" for event in run)
    ]
    experiment_ids = {
        experiment_id
        for run in roi_runs
        if (experiment_id := report_atom(run[0].get("experiment_id"))) is not None
    }
    missing_experiment_id = any(
        report_atom(run[0].get("experiment_id")) is None for run in roi_runs
    )
    experiment_ready = (
        bool(roi_runs)
        and not missing_experiment_id
        and len(experiment_ids) == 1
    )
    comparison_ready = (
        bool(ended)
        and not open_delegations
        and not open_runs
        and experiment_ready
        and all(
            state.get("effective_duration") is not None
            and valid_integer(state["finish"].get("total_tokens")) is not None
            and "assessment" in state
            and all(
                valid_number(state["assessment"].get(key)) is not None
                for key in effort_keys
            )
            and complete_baseline(state["assessment"])
            for state in ended
        )
    )
    benefit_ready = comparison_ready and all(
        state["assessment"].get("baseline_kind") in MEASURED_BASELINE_KINDS
        for state in ended
    )
    cost_ready = False
    roi_ready = benefit_ready and cost_ready

    blockers: list[str] = []
    if not started:
        blockers.append("no_roi_cohort")
    else:
        if open_delegations:
            blockers.append("open_roi_delegations")
        if any(state.get("effective_duration") is None for state in ended):
            blockers.append("missing_effective_timing")
        if any(
            valid_integer(state["finish"].get("total_tokens")) is None
            for state in ended
        ):
            blockers.append("missing_total_tokens")
        if any("assessment" not in state for state in ended):
            blockers.append("missing_assessment")
        assessed = [state["assessment"] for state in ended if "assessment" in state]
        if any(
            any(valid_number(event.get(key)) is None for key in effort_keys)
            for event in assessed
        ):
            blockers.append("missing_parent_effort")
        if any(not complete_baseline(event) for event in assessed):
            blockers.append("missing_complete_baseline")
        if assessed and any(
            event.get("baseline_kind") not in MEASURED_BASELINE_KINDS
            for event in assessed
        ):
            blockers.append("missing_measured_baseline")
    if open_runs:
        blockers.append("open_roi_runs")
    if missing_experiment_id:
        blockers.append("missing_experiment_id")
    if len(experiment_ids) > 1:
        blockers.append("multiple_experiment_ids")
    if not cost_ready:
        blockers.append("cost_model_unavailable")
    return comparison_ready, benefit_ready, cost_ready, roi_ready, blockers


def report_for_profile(report: dict[str, Any], profile: str) -> dict[str, Any]:
    if profile == "full":
        return {"profile": profile, **report}
    if profile == "operational":
        runs = {
            key: value
            for key, value in report["runs"].items()
            if key not in ("by_kind", "categories")
        }
        return {
            "profile": profile,
            "runs": runs,
            "diagnostics": report["diagnostics"],
        }
    if profile == "delegation":
        delegations = {
            key: value
            for key, value in report["delegations"].items()
            if key != "by_task_kind"
        }
        return {
            "profile": profile,
            "delegations": delegations,
            "data_quality": report["data_quality"],
            "diagnostics": report["diagnostics"],
        }
    if profile == "roi":
        return {
            "profile": profile,
            "roi": report["roi"],
            "diagnostics": report["diagnostics"],
        }
    raise ValueError(f"unsupported report profile: {profile}")


def compact_markdown(report: dict[str, Any], profile: str) -> str:
    if profile == "operational":
        runs = report["runs"]
        lines = [
            "# Skill operational report",
            "",
            "| Metric | Value |",
            "|---|---:|",
            f"| Runs finished/total | {runs['finished']}/{runs['total']} |",
            f"| Terminal success rate | {runs['success_rate']:.1f}% |",
            f"| Outcomes | {json.dumps(runs['outcomes'], sort_keys=True)} |",
            f"| Open / stale | {runs['open']} / {runs['stale_open']} |",
            "",
            "| Skill | Total | Finished | Success rate |",
            "|---|---:|---:|---:|",
        ]
        for skill, summary in runs["by_skill"].items():
            lines.append(
                f"| {skill} | {summary['total']} | {summary['finished']} | "
                f"{summary['success_rate']:.1f}% |"
            )
        diagnostics = report["diagnostics"]
        lines.extend(
            [
                "",
                f"- Diagnostics: {diagnostics['malformed_files']} malformed, "
                f"{diagnostics['mixed_run_id_files']} mixed-run, "
                f"{diagnostics['duplicate_delegation_events']} duplicate delegation, "
                f"{diagnostics['unrelated_files']} unrelated files.",
                f"- Files: {diagnostics['scanned_files']} scanned, "
                f"{diagnostics['matched_files']} matched, "
                f"{diagnostics['excluded_files']} excluded by filters.",
            ]
        )
        if runs["retention"]["potentially_censored"]:
            lines.append("- Warning: retained history may be censored by a keep limit.")
        return "\n".join(lines)
    if profile == "delegation":
        delegations = report["delegations"]
        quality = report["data_quality"]
        lines = [
            "# Skill delegation report",
            "",
            "| Metric | Value |",
            "|---|---:|",
            f"| Finished/started | {delegations['finishes']}/{delegations['starts']} |",
            f"| Success rate | {delegations['success_rate']:.1f}% |",
            f"| Assessed acceptance rate | {delegations['assessed_acceptance_rate']:.1f}% |",
            f"| Dispositions | {json.dumps(delegations['dispositions'], sort_keys=True)} |",
            f"| Open / stale | {delegations['open']} / {delegations['stale_open']} |",
            "",
            "| Task family | Finished | Success rate | Duration p50 / p90 (s) |",
            "|---|---:|---:|---:|",
        ]
        for family, summary in delegations["by_task_family"].items():
            timing = summary["effective_duration_seconds"]
            lines.append(
                f"| {family} | {summary['finished']} | {summary['success_rate']:.1f}% | "
                f"{timing['p50']} / {timing['p90']} |"
            )
        lines.extend(
            [
                "",
                f"- Data quality: assessment {quality['assessment']['percent']:.1f}%, "
                f"tokens {quality['total_tokens']['percent']:.1f}%, parent effort "
                f"{quality['parent_effort']['percent']:.1f}%.",
                f"- Diagnostics: {report['diagnostics']['duplicate_delegation_events']} "
                "duplicate, "
                f"{report['diagnostics']['orphan_delegation_finishes']} orphan finishes, "
                f"{report['diagnostics']['orphan_delegation_assessments']} orphan "
                "assessments.",
            ]
        )
        return "\n".join(lines)
    if profile == "roi":
        roi = report["roi"]
        cohort = roi["cohort"]
        return "\n".join(
            [
                "# Skill ROI experiment report",
                "",
                "| Metric | Value |",
                "|---|---:|",
                f"| Cohort runs | {cohort['runs']} |",
                f"| Delegations finished/total | {cohort['finished']}/{cohort['delegations']} |",
                f"| Measured comparisons | {roi['measured_comparisons']['count']} |",
                f"| Comparison ready | {roi['comparison_ready']} |",
                f"| Benefit ROI ready | {roi['benefit_roi_ready']} |",
                f"| Cost ROI ready | {roi['cost_roi_ready']} |",
                f"| Full ROI ready | {roi['roi_ready']} |",
                f"| Blockers | {json.dumps(roi['blockers'])} |",
            ]
        )
    raise ValueError(f"unsupported compact Markdown profile: {profile}")


def cmd_report(args: argparse.Namespace) -> int:
    since = parse_time(args.since) if args.since else None
    profile = getattr(args, "profile", "full")
    if profile not in REPORT_PROFILES:
        raise ValueError(f"unsupported report profile: {profile}")
    experiment_id = safe_atom(
        getattr(args, "experiment_id", None) or "",
        "--experiment-id",
        allow_empty=True,
    )
    if profile == "roi" and not experiment_id:
        raise ValueError("--profile roi requires --experiment-id")
    threshold = numeric(args.stale_after_seconds, "stale_after_seconds")
    now = dt.datetime.now(dt.timezone.utc)
    diagnostics = {
        "files": 0,
        "scanned_files": 0,
        "matched_files": 0,
        "excluded_files": 0,
        "malformed_lines": 0,
        "malformed_files": 0,
        "unrelated_files": 0,
        "mixed_run_id_files": 0,
        "duplicate_delegation_events": 0,
        "unsafe_dimension_values": 0,
    }
    runs: list[list[dict[str, Any]]] = []
    for path in sorted(args.log_dir.expanduser().rglob("*.jsonl")):
        diagnostics["scanned_files"] += 1
        events: list[dict[str, Any]] = []
        malformed = False
        try:
            for line in path.read_text(encoding="utf-8").splitlines():
                try:
                    event = json.loads(line)
                    if not isinstance(event, dict):
                        raise ValueError()
                    events.append(event)
                except (ValueError, json.JSONDecodeError):
                    diagnostics["malformed_lines"] += 1
                    malformed = True
            if malformed:
                diagnostics["malformed_files"] += 1
                continue
            if not events or events[0].get("event") != "run_started":
                raise ValueError()
            run_id = events[0].get("run_id")
            if not isinstance(run_id, str) or any(
                event.get("run_id") != run_id for event in events
            ):
                diagnostics["mixed_run_id_files"] += 1
                continue
            if args.skill and canonical_skill_name(
                events[0].get("skill")
            ) != canonical_skill_name(args.skill):
                diagnostics["excluded_files"] += 1
                continue
            if since and (not event_time(events[0]) or event_time(events[0]) < since):
                diagnostics["excluded_files"] += 1
                continue
            if experiment_id and events[0].get("experiment_id") != experiment_id:
                diagnostics["excluded_files"] += 1
                continue
            runs.append(events)
            diagnostics["matched_files"] += 1
        except (OSError, ValueError, KeyError):
            diagnostics["unrelated_files"] += 1
    diagnostics["files"] = diagnostics["matched_files"]
    terminals = [
        next(
            (
                event
                for event in reversed(run)
                if event.get("event") == "run_finished"
            ),
            None,
        )
        for run in runs
    ]
    finished = [event for event in terminals if event]
    linked: dict[tuple[str, str], dict[str, Any]] = {}
    legacy_unlinked_events = 0
    for run in runs:
        for event in run:
            name = event.get("event")
            if name not in ("delegation_started", "delegation_finished", "delegation_assessed"):
                continue
            if event.get("schema_version") != 2:
                legacy_unlinked_events += 1
                continue
            delegation_id = event.get("delegation_id")
            if not isinstance(delegation_id, str) or not delegation_id:
                legacy_unlinked_events += 1
                continue
            if name == "delegation_started" and not event.get("task_family"):
                executor = event.get("executor")
                event = {
                    **event,
                    "task_family": DEFAULT_TASK_FAMILY.get(
                        executor if isinstance(executor, str) else "",
                        "other",
                    ),
                }
            state = linked.setdefault(
                (event["run_id"], delegation_id),
                {"events": [], "run_start": run[0]},
            )
            state["events"].append(event)
            if name == "delegation_started" and "start" not in state:
                state["start"] = event
            elif name == "delegation_started":
                diagnostics["duplicate_delegation_events"] += 1
            elif name == "delegation_finished" and "finish" not in state:
                state["finish"] = event
            elif name == "delegation_finished":
                diagnostics["duplicate_delegation_events"] += 1
            elif name == "delegation_assessed":
                if "assessment" in state:
                    diagnostics["duplicate_delegation_events"] += 1
                state["assessment"] = event
    delegations = list(linked.values())
    started = [state for state in delegations if "start" in state]
    ended = [state for state in delegations if "finish" in state]
    assessed = [state for state in ended if "assessment" in state]
    open_delegations = [state for state in started if "finish" not in state]
    diagnostics["orphan_delegation_finishes"] = sum(
        "finish" in state and "start" not in state for state in delegations
    )
    diagnostics["orphan_delegation_assessments"] = sum(
        "assessment" in state and "finish" not in state for state in delegations
    )
    duration_events = [state["finish"] for state in ended]
    assess_events = [state["assessment"] for state in assessed]

    def elapsed(start: dict[str, Any], finish: dict[str, Any], field: str) -> float | None:
        value = valid_number(finish.get(field))
        if value is not None:
            return value
        started_at, finished_at = event_time(start), event_time(finish)
        if started_at and finished_at:
            return max(0.0, (finished_at - started_at).total_seconds())
        return None

    for state in ended:
        state["effective_duration"] = (
            elapsed(state["start"], state["finish"], "duration_seconds")
            if "start" in state else None
        )
    durations = [
        state["effective_duration"]
        for state in ended
        if state["effective_duration"] is not None
    ]
    walls = [
        value for run, terminal in zip(runs, terminals) if terminal
        for value in [elapsed(run[0], terminal, "wall_seconds")]
        if value is not None
    ]
    run_states = [
        {
            "start": run[0],
            "finish": terminal,
            "wall_seconds": (
                elapsed(run[0], terminal, "wall_seconds") if terminal else None
            ),
        }
        for run, terminal in zip(runs, terminals)
    ]
    categories = run_dimension_summary(
        [state for state in run_states if state["finish"] is not None],
        lambda state: state["finish"].get("category"),
    )
    by_kind = run_dimension_summary(
        run_states,
        lambda state: state["start"].get("kind"),
    )
    by_skill = run_dimension_summary(
        run_states,
        lambda state: canonical_skill_name(state["start"].get("skill")),
    )
    correlation = {
        field: {
            "count": sum(
                report_atom(run[0].get(field)) is not None for run in runs
            ),
            "total": len(runs),
            "percent": ratio_percent(
                sum(report_atom(run[0].get(field)) is not None for run in runs),
                len(runs),
            ),
        }
        for field in (
            "session_id",
            "thread_id",
            "task_id",
            "parent_run_id",
            "root_run_id",
        )
    }
    retention = retention_summary(runs)
    tokens = {
        key: sum(valid_number(event.get(key)) or 0 for event in duration_events)
        for key in USAGE_FIELDS
    }
    effort_keys = ("parent_wait_seconds", "review_seconds", "rework_seconds")
    effort = {
        key: sum(valid_number(event.get(key)) or 0 for event in assess_events)
        for key in effort_keys
    }
    quality = delegation_data_quality(ended)
    roi_runs = [
        run for run in runs if run[0].get("measurement_mode") == "roi"
    ]
    roi_started = [
        state
        for state in started
        if state["run_start"].get("measurement_mode") == "roi"
    ]
    roi_ended = [
        state
        for state in ended
        if state["run_start"].get("measurement_mode") == "roi"
    ]
    roi_assessed = [state for state in roi_ended if "assessment" in state]
    roi_open = [state for state in roi_started if "finish" not in state]
    roi_assess_events = [state["assessment"] for state in roi_assessed]
    roi_quality = delegation_data_quality(roi_ended)
    complete_comparisons = [
        event for event in roi_assess_events if complete_baseline(event)
    ]
    measured_comparisons = [
        event
        for event in complete_comparisons
        if event.get("baseline_kind") in MEASURED_BASELINE_KINDS
    ]
    estimated_comparisons = [
        event
        for event in complete_comparisons
        if event.get("baseline_kind") == "manual-estimate"
    ]
    (
        comparison_ready,
        benefit_roi_ready,
        cost_roi_ready,
        roi_ready,
        roi_blockers,
    ) = roi_readiness(
        roi_started,
        roi_ended,
        roi_open,
        roi_runs,
    )
    run_success = sum(event.get("outcome") == "success" for event in finished)
    delegation_success = sum(
        event.get("outcome") == "success" for event in duration_events
    )
    accepted = sum(
        event.get("disposition") == "accepted" for event in assess_events
    )
    measured_summary = comparison_summary(measured_comparisons)
    estimated_summary = comparison_summary(estimated_comparisons)
    roi_summary = {
        "cohort": {
            "runs": len(roi_runs),
            "finished_runs": sum(
                any(event.get("event") == "run_finished" for event in run)
                for run in roi_runs
            ),
            "delegations": len(roi_started),
            "finished": len(roi_ended),
            "assessments": len(roi_assessed),
            "open": len(roi_open),
            "experiment_ids": scalar_counts(
                [run[0] for run in roi_runs],
                "experiment_id",
            ),
        },
        "measured_comparisons": measured_summary,
        "estimated_comparisons": estimated_summary,
        "data_quality": roi_quality,
        "comparison_ready": comparison_ready,
        "benefit_roi_ready": benefit_roi_ready,
        "cost_roi_ready": cost_roi_ready,
        "roi_ready": roi_ready,
        "blockers": roi_blockers,
    }
    report = {
        "runs": {
            "total": len(runs),
            "finished": len(finished),
            "success": run_success,
            "success_rate": ratio_percent(run_success, len(finished)),
            "outcomes": scalar_counts(finished, "outcome"),
            "wall_seconds": {
                "sum": sum(walls),
                "p50": percentile(walls, 0.5),
                "p90": percentile(walls, 0.9),
            },
            "open": len(runs) - len(finished),
            "stale_open": sum(
                stale(run, now, threshold)
                for run, terminal in zip(runs, terminals)
                if not terminal
            ),
            "categories": categories,
            "by_kind": by_kind,
            "by_skill": by_skill,
            "correlation_coverage": correlation,
            "retention": {
                **retention,
            },
        },
        "delegations": {
            "starts": len(started),
            "finishes": len(ended),
            "success": delegation_success,
            "success_rate": ratio_percent(delegation_success, len(ended)),
            "assessments": len(assessed),
            "legacy_unlinked_events": legacy_unlinked_events,
            "outcomes": scalar_counts(duration_events, "outcome"),
            "executors": scalar_counts(
                [state["start"] for state in started],
                "executor",
            ),
            "by_executor": dimension_summary(ended, "executor", diagnostics),
            "by_task_kind": dimension_summary(ended, "task_kind", diagnostics),
            "by_task_family": dimension_summary(ended, "task_family", diagnostics),
            "by_mode": dimension_summary(ended, "mode", diagnostics),
            "dispositions": scalar_counts(assess_events, "disposition"),
            "assessed_acceptance_rate": ratio_percent(accepted, len(assess_events)),
            "duration_seconds": {
                "sum": sum(durations),
                "p50": percentile(durations, 0.5),
                "p90": percentile(durations, 0.9),
            },
            "parent_effort_seconds": effort,
            "actual_tokens": tokens,
            "open": len(open_delegations),
            "stale_open": sum(
                stale(state["events"], now, threshold)
                for state in open_delegations
            ),
        },
        "roi": roi_summary,
        "measured_comparisons": measured_summary,
        "estimated_comparisons": estimated_summary,
        "data_quality": quality,
        "comparison_ready": comparison_ready,
        "roi_ready": roi_ready,
        "benefit_roi_ready": benefit_roi_ready,
        "cost_roi_ready": cost_roi_ready,
        "diagnostics": diagnostics,
    }
    projected = report_for_profile(report, profile)
    if args.format == "markdown" and profile != "full":
        print(compact_markdown(report, profile))
        return 0
    if args.format == "markdown":
        measured = report["measured_comparisons"]
        estimated = report["estimated_comparisons"]
        delegation_duration = (
            f"{percentile(durations, 0.5)} / {percentile(durations, 0.9)} s"
        )
        run_wall = report["runs"]["wall_seconds"]
        stale_delegations = sum(
            stale(state["events"], now, threshold) for state in open_delegations
        )
        lines = [
            "# Skill telemetry report",
            "",
            "| Metric | Value |",
            "|---|---:|",
            f"| Runs finished/total | {len(finished)}/{len(runs)} |",
            f"| Run terminal success rate | {ratio_percent(run_success, len(finished)):.1f}% |",
            f"| Run outcomes | {json.dumps(report['runs']['outcomes'], sort_keys=True)} |",
            f"| Run wall p50 / p90 / sum | {run_wall['p50']} / "
            f"{run_wall['p90']} / {run_wall['sum']} s |",
            f"| Open / stale runs | {report['runs']['open']} / {report['runs']['stale_open']} |",
            f"| Delegations finished/started | {len(ended)}/{len(started)} |",
            f"| Delegation success rate | {ratio_percent(delegation_success, len(ended)):.1f}% |",
            f"| Assessed acceptance rate | {ratio_percent(accepted, len(assess_events)):.1f}% |",
            f"| Delegation duration p50 / p90 | {delegation_duration} |",
            f"| Measured comparisons | {measured['count']} |",
            f"| Measured wall delta | {measured['wall_seconds_delta']:.3f} s |",
            f"| Measured parent-token delta | {measured['parent_total_tokens_delta']} |",
            f"| Manual-estimate comparisons | {estimated['count']} |",
            f"| Measured benefit ROI ready | {benefit_roi_ready} |",
            f"| Open stale delegations | {stale_delegations} |",
            "",
            "Positive comparison deltas mean the delegated path used less wall "
            "time or fewer parent tokens than its baseline.",
            "",
            "| Executor | Finished | Success rate | Duration p50 / p90 (s) |",
            "|---|---:|---:|---:|",
        ]
        for executor, summary in report["delegations"]["by_executor"].items():
            timing = summary["effective_duration_seconds"]
            lines.append(
                f"| {executor} | {summary['finished']} | "
                f"{summary['success_rate']:.1f}% | {timing['p50']} / {timing['p90']} |"
            )
        lines.extend(
            [
                "",
                "| Run kind | Total | Finished | Success rate | Outcomes | "
                "Wall p50 / p90 / sum (s) |",
                "|---|---:|---:|---:|---|---:|",
            ]
        )
        for kind, summary in report["runs"]["by_kind"].items():
            timing = summary["wall_seconds"]
            lines.append(
                f"| {kind} | {summary['total']} | {summary['finished']} | "
                f"{summary['success_rate']:.1f}% | "
                f"{json.dumps(summary['outcomes'], sort_keys=True)} | "
                f"{timing['p50']} / {timing['p90']} / {timing['sum']} |"
            )
        if report["runs"]["categories"]:
            lines.extend(
                [
                    "",
                    "| Run category | Finished | Success rate | Outcomes |",
                    "|---|---:|---:|---|",
                ]
            )
            for category, summary in report["runs"]["categories"].items():
                lines.append(
                    f"| {category} | {summary['finished']} | "
                    f"{summary['success_rate']:.1f}% | "
                    f"{json.dumps(summary['outcomes'], sort_keys=True)} |"
                )
        lines.extend(
            [
                "",
                f"- Data quality: assessment {quality['assessment']['percent']:.1f}%, "
                f"effective timing {quality['effective_timing']['percent']:.1f}%, "
                f"total tokens {quality['total_tokens']['percent']:.1f}%, "
                f"measured baseline {quality['measured_baseline']['percent']:.1f}%.",
                f"- Diagnostics: {diagnostics['malformed_files']} malformed files, "
                f"{diagnostics['mixed_run_id_files']} mixed-run files, "
                f"{diagnostics['duplicate_delegation_events']} duplicate delegation events, "
                f"{diagnostics['unsafe_dimension_values']} unsafe dimension values, "
                f"{legacy_unlinked_events} legacy unlinked events.",
                f"- Files: {diagnostics['scanned_files']} scanned, "
                f"{diagnostics['matched_files']} matched, "
                f"{diagnostics['excluded_files']} excluded by filters.",
                "- Correlation coverage: " + ", ".join(
                    f"{field} {value['percent']:.1f}%"
                    for field, value in report["runs"]["correlation_coverage"].items()
                ) + ".",
                "- Cost ROI is unavailable because model pricing is not recorded.",
            ]
        )
        if report["runs"]["retention"]["potentially_censored"]:
            lines.append(
                "- Warning: retained run count reached an observed retention limit; "
                "historical results may be censored."
            )
        print("\n".join(lines))
    else:
        print(json.dumps(projected, ensure_ascii=False, sort_keys=True))
    return 0


def build_parser() -> argparse.ArgumentParser:
    parser = argparse.ArgumentParser(description=__doc__)
    subparsers = parser.add_subparsers(dest="command", required=True)

    start = subparsers.add_parser("start", help="create a per-run JSONL log")
    start.add_argument("--skill", required=True)
    start.add_argument("--kind", required=True, help="short non-sensitive task category")
    start.add_argument("--scope", action="append", default=[], help="allowed path or subsystem")
    start.add_argument("--parent-run-id", default="")
    start.add_argument("--root-run-id", default="")
    start.add_argument("--invoked-by", default="main-agent")
    start.add_argument(
        "--measurement-mode",
        choices=MEASUREMENT_MODES,
        default="audit",
        help="audit records operations; roi requires an explicit controlled experiment",
    )
    start.add_argument("--experiment-id", default="")
    for field in CORRELATION_FIELDS:
        start.add_argument(f"--{field.replace('_', '-')}", default="")
    start.add_argument("--cwd", type=Path, default=Path.cwd())
    start.add_argument("--log-dir", type=Path, default=default_log_dir())
    start.add_argument("--keep", type=int, default=DEFAULT_KEEP)
    start.set_defaults(handler=cmd_start)

    event = subparsers.add_parser("event", help="append a structured milestone")
    event.add_argument("--log-file", type=Path, required=True)
    event.add_argument("--log-dir", type=Path, default=default_log_dir())
    event.add_argument("--run-id", required=True)
    event.add_argument("--name", required=True)
    event.add_argument(
        "--status",
        choices=("started", "pass", "fail", "skip", "info"),
        default="info",
    )
    event.add_argument("--data-json")
    event.set_defaults(handler=cmd_event)

    finish = subparsers.add_parser("finish", help="append the final outcome")
    finish.add_argument("--log-file", type=Path, required=True)
    finish.add_argument("--log-dir", type=Path, default=default_log_dir())
    finish.add_argument("--run-id", required=True)
    finish.add_argument(
        "--outcome",
        choices=("success", "failure", "blocked", "cancelled"),
        required=True,
    )
    finish.add_argument("--category", default="")
    finish.add_argument("--summary", default="", help="short non-sensitive result summary")
    finish.add_argument("--data-json")
    finish.add_argument("--cwd", type=Path, default=Path.cwd())
    finish.set_defaults(handler=cmd_finish)

    delegate_start = subparsers.add_parser(
        "delegate-start",
        help="start a correlated Terra delegation and return its ID",
    )
    for option in ("log_file", "log_dir", "run_id"):
        delegate_start.add_argument(
            f"--{option.replace('_', '-')}",
            type=Path if option != "run_id" else str,
            required=option != "log_dir",
            default=default_log_dir() if option == "log_dir" else None,
        )
    delegate_start.add_argument(
        "--executor",
        choices=("terra_explorer", "terra_reviewer", "terra_worker", "other"),
        required=True,
    )
    delegate_start.add_argument("--task-kind", required=True)
    delegate_start.add_argument("--task-family", choices=TASK_FAMILIES, default="")
    delegate_start.add_argument("--mode", choices=("parallel", "blocking"), required=True)
    delegate_start.add_argument("--scope", action="append", default=[])
    delegate_start.add_argument("--child-task-id", default="")
    delegate_start.add_argument("--child-session-id", default="")
    delegate_start.set_defaults(handler=cmd_delegate_start)

    delegate_finish = subparsers.add_parser(
        "delegate-finish",
        help="record the child outcome and observed child metrics",
    )
    delegate_finish.add_argument("--log-file", type=Path, required=True)
    delegate_finish.add_argument("--log-dir", type=Path, default=default_log_dir())
    delegate_finish.add_argument("--run-id", required=True)
    delegate_finish.add_argument("--delegation-id", required=True)
    delegate_finish.add_argument(
        "--outcome", choices=("success", "failure", "declined", "cancelled"), required=True,
    )
    for option in ("child_task_id", "child_session_id", "patch_sha256"):
        delegate_finish.add_argument(f"--{option.replace('_', '-')}", default="")
    delegate_finish.add_argument("--changed-lines", type=int, default=None)
    for option in ("duration_seconds", *USAGE_FIELDS):
        delegate_finish.add_argument(
            f"--{option.replace('_', '-')}",
            default=None,
        )
    delegate_finish.set_defaults(handler=cmd_delegate_finish)

    delegate_assess = subparsers.add_parser(
        "delegate-assess",
        help="record the parent disposition, effort, and optional baseline",
    )
    delegate_assess.add_argument("--log-file", type=Path, required=True)
    delegate_assess.add_argument("--log-dir", type=Path, default=default_log_dir())
    delegate_assess.add_argument("--run-id", required=True)
    delegate_assess.add_argument("--delegation-id", required=True)
    delegate_assess.add_argument(
        "--disposition", choices=("accepted", "partial", "rejected", "not_actionable"),
        required=True,
    )
    for option in ("reason_category", "commit", "pr", "baseline_ref"):
        delegate_assess.add_argument(f"--{option.replace('_', '-')}", default="")
    delegate_assess.add_argument(
        "--baseline-kind",
        choices=BASELINE_KINDS,
        help="manual estimates are reported separately from measured baselines",
    )
    for option in (
        "parent_wait_seconds", "review_seconds", "rework_seconds", "task_wall_seconds",
        "parent_total_tokens", "baseline_wall_seconds", "baseline_parent_total_tokens",
    ):
        delegate_assess.add_argument(f"--{option.replace('_', '-')}", default=None)
    delegate_assess.set_defaults(handler=cmd_delegate_assess)

    report = subparsers.add_parser("report", help="summarize skill telemetry")
    report.add_argument("--log-dir", type=Path, default=default_log_dir())
    report.add_argument("--skill", help="limit the report to one skill")
    report.add_argument("--since", help="inclusive ISO-8601 run start time")
    report.add_argument(
        "--experiment-id",
        help="limit the report to one ROI experiment; required by --profile roi",
    )
    report.add_argument("--stale-after-seconds", default=86400)
    report.add_argument(
        "--profile",
        choices=REPORT_PROFILES,
        default="operational",
        help="select a concise operational, delegation, ROI, or compatibility view",
    )
    report.add_argument("--format", choices=("json", "markdown"), default="json")
    report.set_defaults(handler=cmd_report)
    return parser


def main() -> int:
    parser = build_parser()
    args = parser.parse_args()
    try:
        return args.handler(args)
    except (OSError, ValueError, json.JSONDecodeError) as exc:
        print(f"skill_log: {exc}", file=sys.stderr)
        return 2


if __name__ == "__main__":
    raise SystemExit(main())
