"""Explicit native price snapshots and incremental estimates, never invoices."""

from __future__ import annotations

import re
from datetime import datetime
from decimal import Decimal, localcontext
from urllib.parse import urlsplit
from uuid import UUID

PROVIDERS = {"agenticdeepseek", "agenticglm", "agenticclaude", "agenticgemini",
             "agenticopenai", "agenticark", "agenticqwen"}


def decimal_amount(value: object, places: int = 12) -> str:
    if not isinstance(value, str) or not re.fullmatch(r"[0-9]{1,18}(?:\.[0-9]{1," + str(places) + r"})?", value):
        raise ValueError("Amount must be a nonnegative bounded decimal string")
    return value


def validate_price_card(card: object) -> dict:
    keys = {"version", "currency", "source", "as_of", "window", "routes"}
    if not isinstance(card, dict) or set(card) != keys or type(card["version"]) is not int or card["version"] != 1:
        raise ValueError("Price card requires the version 1 public schema")
    if not isinstance(card["currency"], str) or not re.fullmatch(r"[A-Z]{3}", card["currency"]):
        raise ValueError("Price card requires a native currency")
    source = card["source"]
    if not isinstance(source, str) or len(source) > 512 or any(ord(c) <= 32 for c in source):
        raise ValueError("Invalid public price source")
    url = urlsplit(source)
    if url.scheme not in ("http", "https") or not url.netloc or url.username is not None or url.query or url.fragment:
        raise ValueError("Price source must be public HTTP(S) without credentials or query")
    stamp = card["as_of"]
    if not isinstance(stamp, str) or len(stamp) > 64 or not re.fullmatch(r"\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(?:\.\d+)?(?:Z|[+-]\d{2}:\d{2})", stamp):
        raise ValueError("Price date must be RFC3339")
    datetime.fromisoformat(stamp.replace("Z", "+00:00"))
    if card["window"] not in ("fixed", "peak", "off_peak", "upper_bound"):
        raise ValueError("Invalid price window")
    routes = card["routes"]
    if not isinstance(routes, list) or not 1 <= len(routes) <= 128:
        raise ValueError("Price card requires 1..128 routes")
    seen = set()
    route_keys = {"provider", "model", "cached_input_per_million", "uncached_input_per_million", "output_per_million"}
    for route in routes:
        if not isinstance(route, dict) or set(route) != route_keys:
            raise ValueError("Invalid public price route")
        model = route["model"]
        if (not isinstance(route["provider"], str) or route["provider"] not in PROVIDERS or not isinstance(model, str) or model == "unknown"
                or len(model) > 128 or "://" in model or not re.fullmatch(r"[A-Za-z0-9_.:/\[\]-]+", model)):
            raise ValueError("Price route requires canonical provider and model")
        key = (route["provider"], model)
        if key in seen:
            raise ValueError("Duplicate price route")
        seen.add(key)
        for rate in route_keys - {"provider", "model"}:
            decimal_amount(route[rate], 6)
    return {**card, "routes": [dict(route) for route in routes]}


def validated_cost(value: object, usage: dict) -> dict | None:
    try:
        keys = {"version", "segment_id", "price_card", "known_estimated_amount", "actual_billed_amount",
                "priced_calls", "unpriced_calls", "complete", "limit_mode"}
        if not isinstance(value, dict) or set(value) != keys or type(value["version"]) is not int or value["version"] != 1:
            return None
        segment = value["segment_id"]
        if not isinstance(segment, str) or len(segment) != 36 or str(UUID(segment)) != segment or UUID(segment).int == 0:
            return None
        card = validate_price_card(value["price_card"])
        amount = value["known_estimated_amount"]
        # Aggregates may exceed the bounded user-configured decimal amount.
        if not isinstance(amount, str) or not re.fullmatch(r"[0-9]{1,60}\.[0-9]{12}", amount):
            return None
        if (value["actual_billed_amount"] is not None or type(value["complete"]) is not bool
                or value["limit_mode"] not in ("disabled", "post_response_threshold")
                or any(type(value[key]) is not int or value[key] < 0 for key in ("priced_calls", "unpriced_calls"))):
            return None
        if (value["priced_calls"] > usage["known_calls"] or usage["known_calls"] > usage["provider_calls"]
                or value["priced_calls"] + value["unpriced_calls"] + usage["in_flight"] != usage["provider_calls"]
                or value["complete"] != (not (value["unpriced_calls"] or usage["in_flight"] or usage["untracked_calls"]))):
            return None
        if "call_ledger" in usage and segment != usage["call_ledger"]["segment_id"]:
            return None
        if value["priced_calls"] == 0 and Decimal(amount) != 0:
            return None
        if "call_ledger" in usage and not reconcile_call_costs(value, usage["call_ledger"]):
            return None
        return {**value, "price_card": card}
    except (ValueError, TypeError, KeyError):
        return None


def reconcile_call_costs(cost: dict, ledger: dict) -> bool:
    priced = unpriced = 0
    observed = Decimal(0)
    with localcontext() as ctx:
        ctx.prec = 90
        for record in ledger["records"]:
            value = record.get("cost")
            if value is None:
                return False
            index = value["price_index"]
            if index >= len(cost["price_card"]["routes"]):
                return False
            if index >= 0:
                route = cost["price_card"]["routes"][index]
                if route["provider"] != record["provider"] or route["model"] != record["requested_model"]:
                    return False
            if value["state"] == "priced":
                tokens = record["tokens"]
                expected = sum(Decimal(tokens[token]) * Decimal(route[rate]) for token, rate in (
                    ("cached_prompt_tokens", "cached_input_per_million"),
                    ("uncached_prompt_tokens", "uncached_input_per_million"),
                    ("completion_tokens", "output_per_million"))) / Decimal(1000000)
                if expected != Decimal(value["estimated_amount"]):
                    return False
                observed += expected
                priced += 1
            elif value["state"] == "unpriced":
                unpriced += 1
    if ledger["dropped_records"]:
        return priced <= cost["priced_calls"] and unpriced <= cost["unpriced_calls"] and observed <= Decimal(cost["known_estimated_amount"])
    return priced == cost["priced_calls"] and unpriced == cost["unpriced_calls"] and observed == Decimal(cost["known_estimated_amount"])


def validated_call_cost(value: object, state: str) -> dict | None:
    if not isinstance(value, dict) or set(value) != {"state", "price_index", "estimated_amount"}:
        return None
    price = value["price_index"]
    cost_state, amount = value["state"], value["estimated_amount"]
    if type(price) is not int or not -1 <= price < 128:
        return None
    if cost_state == "priced":
        if state != "known" or price < 0 or not isinstance(amount, str) or not re.fullmatch(r"[0-9]{1,60}\.[0-9]{12}", amount):
            return None
    elif cost_state in ("in_flight", "released", "unpriced"):
        if amount is not None or (cost_state != "unpriced" and cost_state != state) or (cost_state == "unpriced" and state not in ("known", "unknown")):
            return None
    else:
        return None
    return dict(value)


def cost_projection(value: dict, result: dict) -> None:
    if "cost" in value and "cost_segments" in value:
        result["cost_complete"] = False
        return
    if "cost" in value:
        cost = validated_cost(value["cost"], result)
        if cost is not None:
            result["cost"] = cost
        result["cost_complete"] = cost is not None and cost["complete"]
    elif "cost_segments" in value:
        segments = value["cost_segments"]
        # Aggregated segments are already individually validated by sum_usage;
        # reconstruct per-segment call counts for validation on archived input.
        costs = []
        fields = ("provider_calls", "known_calls", "in_flight", "untracked_calls")
        if isinstance(segments, list) and len(segments) <= 17:
            for entry in segments:
                if not isinstance(entry, dict) or set(entry) != {"cost", "usage"} or not isinstance(entry["usage"], dict):
                    break
                counts = entry["usage"]
                if set(counts) != set(fields) or any(type(counts[k]) is not int or counts[k] < 0 for k in fields):
                    break
                cost = validated_cost(entry["cost"], counts)
                if cost is None:
                    break
                costs.append({"cost": cost, "usage": dict(counts)})
            else:
                if (len({entry["cost"]["segment_id"] for entry in costs}) == len(costs)
                        and all(sum(entry["usage"][k] for entry in costs) <= result[k] for k in fields)):
                    result["cost_segments"] = costs
        result["cost_complete"] = (
            bool(costs) and "cost_segments" in result
            and all(e["cost"]["complete"] for e in costs)
            and all(sum(e["usage"][k] for e in costs) == result[k] for k in fields)
            and value.get("cost_complete") is True)


def sum_costs(usages: list[dict], result: dict) -> bool:
    segments = []
    for usage in usages:
        if "cost" in usage:
            segments.append({"cost": usage["cost"], "usage": {key: usage[key] for key in ("provider_calls", "known_calls", "in_flight", "untracked_calls")}})
        else:
            segments.extend(usage.get("cost_segments", []))
    if len(segments) > 17 or len({e["cost"]["segment_id"] for e in segments}) != len(segments):
        return False
    if segments or any("cost_complete" in value for value in usages):
        result["cost_segments"] = segments
        result["cost_complete"] = all(value.get("cost_complete", False) for value in usages)
    return True


def native_totals(usage: dict) -> dict:
    segments = ([{"cost": usage["cost"]}] if "cost" in usage else usage.get("cost_segments", []))
    result = {}
    with localcontext() as ctx:
        ctx.prec = 90
        for entry in segments:
            cost = entry["cost"]
            currency = cost["price_card"]["currency"]
            result[currency] = result.get(currency, Decimal(0)) + Decimal(cost["known_estimated_amount"])
    return {currency: {"known_estimated_amount": str(amount), "complete": usage.get("cost_complete", False), "actual_billed_amount": None}
            for currency, amount in result.items()}
