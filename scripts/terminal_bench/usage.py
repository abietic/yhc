"""Provider-reported usage validation and aggregation, without inferred billing."""

from __future__ import annotations

import re
from uuid import UUID

USAGE_FIELDS = ("provider_calls", "known_calls", "unknown_calls", "in_flight",
                "untracked_calls", "prompt_tokens", "completion_tokens", "total_tokens",
                "cached_prompt_tokens", "reasoning_tokens")


def validated_usage(value: object) -> dict | None:
    if not isinstance(value, dict) or type(value.get("complete")) is not bool:
        return None
    if any(type(value.get(key)) is not int or value[key] < 0 for key in USAGE_FIELDS):
        return None
    if (value["cached_prompt_tokens"] > value["prompt_tokens"]
            or value["reasoning_tokens"] > value["completion_tokens"]
            or value["total_tokens"] < value["prompt_tokens"] + value["completion_tokens"]
            or value["provider_calls"] != value["known_calls"] + value["unknown_calls"] + value["in_flight"]):
        return None
    complete = not (value["unknown_calls"] or value["in_flight"] or value["untracked_calls"])
    if value["complete"] != complete:
        return None
    result = {**{key: value[key] for key in USAGE_FIELDS}, "complete": complete}
    if "routes" in value:
        routes = value["routes"]
        if (not isinstance(routes, list) or any(not isinstance(route, dict)
                or not isinstance(route.get("model"), str) for route in routes)):
            return None
        result["routes"] = [dict(route) for route in routes]
    for key in ("provider_duration_ms", "denied_calls", "released_calls"):
        if key in value:
            if type(value[key]) is not int or value[key] < 0:
                return None
            result[key] = value[key]
    _ledger_projection(value, result)
    return result


def sum_usage(values: list) -> dict | None:
    """Missing segments invalidate the whole total; known segments remain separate."""
    usages = [validated_usage(value) for value in values]
    if not usages or any(value is None for value in usages):
        return None
    ledgers = []
    for value in usages:
        ledgers.extend([value["call_ledger"]] if "call_ledger" in value else value.get("call_ledgers", []))
    if len({ledger["segment_id"] for ledger in ledgers}) != len(ledgers):
        return None  # Repeated invocation snapshot is never another increment.
    result = {key: sum(value[key] for value in usages) for key in USAGE_FIELDS}
    result["complete"] = all(value["complete"] for value in usages)
    if any("routes" in value for value in usages):
        result["routes"] = []
        for value in usages:
            routes = value.get("routes") or []
            result["routes"].extend(routes)
            if value["provider_calls"] and not routes:
                # A partially described model history cannot inherit another
                # segment's model price. Older all-route-less usage stays valid.
                result["routes"].append({"model": ""})
    for key in ("provider_duration_ms", "denied_calls", "released_calls"):
        if all(key in value for value in usages):
            result[key] = sum(value[key] for value in usages)
    if any(key in value for value in usages for key in LEDGER_FIELDS):
        result["call_ledgers"] = ledgers
        result["call_ledger_complete"] = all(value.get("call_ledger_complete", False) for value in usages)

    return result


LEDGER_FIELDS = ("call_ledger", "call_ledgers", "call_ledger_complete")
LEDGER_TOKEN_FIELDS = ("prompt_tokens", "completion_tokens", "total_tokens",
                      "cached_prompt_tokens", "uncached_prompt_tokens", "reasoning_tokens")


def _uuid(value: object) -> bool:
    if not isinstance(value, str) or len(value) != 36:
        return False
    try:
        return str(UUID(value)) == value and UUID(value).int != 0
    except ValueError:
        return False


def _model_label(value: object) -> bool:
    return (isinstance(value, str) and 0 < len(value) <= 128
            and "://" not in value and re.fullmatch(r"[A-Za-z0-9_.:/\[\]-]+", value) is not None)


def validated_ledger(value: object) -> dict | None:
    """Project only numeric fields, model labels and opaque generated identities."""
    if (not isinstance(value, dict) or type(value.get("version")) is not int
            or value["version"] != 1 or not _uuid(value.get("segment_id"))
            or type(value.get("dropped_records")) is not int or value["dropped_records"] < 0
            or not isinstance(value.get("records"), list) or len(value["records"]) > 1024
            or value["dropped_records"] > 0 and len(value["records"]) != 1024):
        return None
    records = []
    ids = set()
    for ordinal, record in enumerate(value["records"], 1):
        if (not isinstance(record, dict) or type(record.get("ordinal")) is not int
                or record["ordinal"] != ordinal or not _uuid(record.get("call_id"))
                or record["call_id"] in ids):
            return None
        ids.add(record["call_id"])
        identities = ("logical_round_id", "logical_request_id", "model_attempt_id")
        numbers = ("attempt_index", "retry_index", "started_offset_ms", "provider_duration_ms")
        if (any(record.get(key) != "unknown" and not _uuid(record.get(key)) for key in identities)
                or any(type(record.get(key)) is not int or record[key] < 0 for key in numbers)
                or any(not _model_label(record.get(key)) for key in ("requested_model", "resolved_model"))
                or record.get("provider") not in ("unknown", "agenticdeepseek", "agenticglm", "agenticclaude", "agenticgemini", "agenticopenai", "agenticark", "agenticqwen")
                or record.get("source") not in ("other", "repl_main_thread", "sdk", "agent", "compact", "independent_verification", "independent_verification_coverage", "prompt_suggestion_generation", "tool_use_summary_generation", "yolo_classifier", "permission_explainer", "approval_review", "long_session_background")
                or record.get("role") not in ("other", "main", "explore", "plan", "general", "summary")
                or record.get("effort") not in ("unknown", "none", "low", "medium", "high", "max", "xhigh")
                or record.get("state") not in ("in_flight", "known", "unknown", "released")):
            return None
        tokens = record.get("tokens")
        if record["state"] == "known":
            if (not isinstance(tokens, dict) or any(type(tokens.get(key)) is not int or tokens[key] < 0 for key in LEDGER_TOKEN_FIELDS)
                    or tokens["cached_prompt_tokens"] > tokens["prompt_tokens"]
                    or tokens["uncached_prompt_tokens"] != tokens["prompt_tokens"] - tokens["cached_prompt_tokens"]
                    or tokens["reasoning_tokens"] > tokens["completion_tokens"]
                    or tokens["total_tokens"] < tokens["prompt_tokens"] + tokens["completion_tokens"]):
                return None
            tokens = {key: tokens[key] for key in LEDGER_TOKEN_FIELDS}
        elif tokens is not None:
            return None
        projected = {key: record[key] for key in (*identities, *numbers, "ordinal", "call_id", "requested_model", "resolved_model", "provider", "source", "role", "effort", "state")}
        records.append({**projected, "tokens": tokens})
    return {"version": 1, "segment_id": value["segment_id"], "dropped_records": value["dropped_records"], "records": records}


def _ledger_projection(value: dict, result: dict) -> None:
    if not any(key in value for key in LEDGER_FIELDS):
        return  # Historical usage remains valid without invented call history.
    single = "call_ledger" in value
    raw = [value["call_ledger"]] if single else value.get("call_ledgers")
    if "call_ledger" in value and "call_ledgers" in value:
        raw = None
    ledgers = [validated_ledger(ledger) for ledger in raw] if isinstance(raw, list) and 0 < len(raw) <= 17 else []
    valid = bool(ledgers) and all(ledger is not None for ledger in ledgers)
    if valid:
        valid = len({ledger["segment_id"] for ledger in ledgers}) == len(ledgers)
    if valid:
        records = [record for ledger in ledgers for record in ledger["records"]]
        exact = all(not ledger["dropped_records"] for ledger in ledgers) and value.get("call_ledger_complete", True) is True
        states = (("known", "known_calls"), ("unknown", "unknown_calls"), ("in_flight", "in_flight"))
        if "released_calls" in result:
            states += (("released", "released_calls"),)
        observed = {count: sum(record["state"] == state for record in records) for state, count in states}
        observed.update({key: sum(record["tokens"][key] for record in records if record["state"] == "known")
                         for key in LEDGER_TOKEN_FIELDS if key != "uncached_prompt_tokens"})
        # Incomplete histories remain usable lower bounds, never exceed totals.
        valid = all(count == result[key] if exact else count <= result[key] for key, count in observed.items())
    if valid:
        result["call_ledger" if single else "call_ledgers"] = ledgers[0] if single else ledgers
    result["call_ledger_complete"] = bool(valid and not result["untracked_calls"]
                                         and all(not ledger["dropped_records"] for ledger in ledgers)
                                         and value.get("call_ledger_complete", True) is True)
