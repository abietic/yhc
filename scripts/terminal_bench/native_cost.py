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


def _receipt_amount(value: object) -> bool:
    """Amounts in a receipt are fixed-scale and may exceed a configured cap."""
    return isinstance(value, str) and re.fullmatch(r"[0-9]{1,60}\.[0-9]{12}", value) is not None


def _model_label(value: object) -> bool:
    return (isinstance(value, str) and 0 < len(value) <= 128 and "//" not in value
            and re.fullmatch(r"[A-Za-z0-9_.:/\[\]-]+", value) is not None)


def _reservation(value: object) -> dict | None:
    keys = {"provider", "model", "input_tokens", "output_tokens", "reserved_amount", "retained_amount"}
    if not isinstance(value, dict) or set(value) not in (keys, keys | {"requests"}):
        return None
    if (not isinstance(value["provider"], str) or value["provider"] not in PROVIDERS
            or not _model_label(value["model"]) or value["model"] == "unknown"
            or any(type(value[key]) is not int or not 0 <= value[key] <= 2**64 - 1
                   for key in ("input_tokens", "output_tokens"))
            or not _receipt_amount(value["reserved_amount"])
            or not _receipt_amount(value["retained_amount"])
            or Decimal(value["retained_amount"]) > Decimal(value["reserved_amount"])):
        return None
    if "requests" in value:
        if type(value["requests"]) is not int or value["requests"] != 1 or value["input_tokens"] or value["output_tokens"]:
            return None
    elif not value["input_tokens"] or not value["output_tokens"]:
        return None
    return dict(value)


def _root_reservation(value: object) -> dict | None:
    keys = {"outstanding_amount", "retained_amount", "peak_exposure_amount", "bound_violations"}
    if (not isinstance(value, dict) or set(value) != keys
            or any(not _receipt_amount(value[key]) for key in keys - {"bound_violations"})
            or type(value["bound_violations"]) is not int or value["bound_violations"] < 0):
        return None
    return dict(value)


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
        if not isinstance(route, dict) or not route_keys <= set(route) or set(route) - route_keys - {"api_model", "per_request"}:
            raise ValueError("Invalid public price route")
        model = route["model"]
        if (not isinstance(route["provider"], str) or route["provider"] not in PROVIDERS or not isinstance(model, str) or model == "unknown"
                or len(model) > 128 or "://" in model or not re.fullmatch(r"[A-Za-z0-9_.:/\[\]-]+", model)):
            raise ValueError("Price route requires canonical provider and model")
        key = (route["provider"], model)
        if key in seen:
            raise ValueError("Duplicate price route")
        seen.add(key)
        if "api_model" in route and (not _model_label(route["api_model"]) or route["api_model"] == "unknown"):
            raise ValueError("Price route api_model requires a canonical model label")
        for rate in route_keys - {"provider", "model"}:
            decimal_amount(route[rate], 6)
        if "per_request" in route:
            decimal_amount(route["per_request"])
            if any(Decimal(route[rate]) for rate in route_keys - {"provider", "model"}):
                raise ValueError("Request routes require zero token rates")
    return {**card, "routes": [dict(route) for route in routes]}


def validated_cost(value: object, usage: dict) -> dict | None:
    try:
        keys = {"version", "segment_id", "price_card", "known_estimated_amount", "actual_billed_amount",
                "priced_calls", "unpriced_calls", "complete", "limit_mode"}
        if not isinstance(value, dict) or set(value) not in (keys, keys | {"reservation"}) or type(value["version"]) is not int or value["version"] != 1:
            return None
        segment = value["segment_id"]
        if not isinstance(segment, str) or len(segment) != 36 or str(UUID(segment)) != segment or UUID(segment).int == 0:
            return None
        card = validate_price_card(value["price_card"])
        strict = value.get("limit_mode") == "pre_dispatch_reservation"
        if strict and (card["window"] != "upper_bound" or any(not route.get("api_model") for route in card["routes"])):
            return None
        amount = value["known_estimated_amount"]
        # Aggregates may exceed the bounded user-configured decimal amount.
        if not _receipt_amount(amount):
            return None
        if (value["actual_billed_amount"] is not None or type(value["complete"]) is not bool
                or value["limit_mode"] not in ("disabled", "post_response_threshold", "pre_dispatch_reservation")
                or any(type(value[key]) is not int or value[key] < 0 for key in ("priced_calls", "unpriced_calls"))):
            return None
        reservation = _root_reservation(value.get("reservation")) if "reservation" in value else None
        with localcontext() as ctx:
            ctx.prec = 90
            reservation_open = bool(reservation and (
                reservation["bound_violations"] or Decimal(reservation["outstanding_amount"])
                or Decimal(reservation["retained_amount"])))
            if ("reservation" in value and reservation is None
                    or strict and reservation is None
                    or value["priced_calls"] > usage["known_calls"] or usage["known_calls"] > usage["provider_calls"]
                    or value["priced_calls"] + value["unpriced_calls"] + usage["in_flight"] != usage["provider_calls"]
                    or value["complete"] != (not (value["unpriced_calls"] or usage["in_flight"]
                                                    or usage["untracked_calls"] or reservation_open))):
                return None
            if reservation is not None and (Decimal(amount) + Decimal(reservation["outstanding_amount"])
                                            + Decimal(reservation["retained_amount"])
                                            > Decimal(reservation["peak_exposure_amount"])):
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
    outstanding = retained = Decimal(0)
    with localcontext() as ctx:
        ctx.prec = 90
        for record in ledger["records"]:
            value = record.get("cost")
            if value is None:
                return False
            index = value["price_index"]
            strict = cost["limit_mode"] == "pre_dispatch_reservation"
            if (index >= len(cost["price_card"]["routes"])
                    or (strict or "reservation" in cost) and index < 0):
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
                if "per_request" in route:
                    if "requests" not in record or any(tokens.values()):
                        return False
                    expected += Decimal(record["requests"]) * Decimal(route["per_request"])
                elif "requests" in record:
                    return False
                if expected != Decimal(value["estimated_amount"]):
                    return False
                if "reservation" in cost:
                    reservation = value.get("reservation")
                    if (reservation is None or expected > Decimal(reservation["reserved_amount"])
                            or tokens["prompt_tokens"] > reservation["input_tokens"]
                            or tokens["completion_tokens"] > reservation["output_tokens"]):
                        return False
                observed += expected
                priced += 1
            elif value["state"] == "unpriced":
                unpriced += 1
            if "reservation" in cost:
                reservation = value.get("reservation")
                if reservation is None:
                    return False
                if (reservation["provider"] != record["provider"]
                        or reservation["model"] != route.get("api_model", record["requested_model"])):
                    return False
                bound = (Decimal(reservation["input_tokens"]) * max(
                    Decimal(route["cached_input_per_million"]), Decimal(route["uncached_input_per_million"]))
                         + Decimal(reservation["output_tokens"]) * Decimal(route["output_per_million"])) / Decimal(1000000)
                if "per_request" in route:
                    if reservation.get("requests") != 1:
                        return False
                    bound += Decimal(route["per_request"])
                elif "requests" in reservation:
                    return False
                if Decimal(reservation["reserved_amount"]) != bound:
                    return False
                if value["state"] == "in_flight":
                    outstanding += Decimal(reservation["reserved_amount"])
                elif value["state"] == "unpriced":
                    retained += Decimal(reservation["retained_amount"])
        if "reservation" in cost:
            root = cost["reservation"]
            if ledger["dropped_records"]:
                if outstanding > Decimal(root["outstanding_amount"]) or retained > Decimal(root["retained_amount"]):
                    return False
            elif (outstanding != Decimal(root["outstanding_amount"])
                  or retained != Decimal(root["retained_amount"])):
                return False
    if ledger["dropped_records"]:
        return priced <= cost["priced_calls"] and unpriced <= cost["unpriced_calls"] and observed <= Decimal(cost["known_estimated_amount"])
    return priced == cost["priced_calls"] and unpriced == cost["unpriced_calls"] and observed == Decimal(cost["known_estimated_amount"])


def validated_call_cost(value: object, state: str) -> dict | None:
    keys = {"state", "price_index", "estimated_amount"}
    if not isinstance(value, dict) or set(value) not in (keys, keys | {"reservation"}):
        return None
    price = value["price_index"]
    cost_state, amount = value["state"], value["estimated_amount"]
    if type(price) is not int or not -1 <= price < 128:
        return None
    reservation = _reservation(value.get("reservation")) if "reservation" in value else None
    if "reservation" in value and reservation is None:
        return None
    if cost_state == "priced":
        if state != "known" or price < 0 or not _receipt_amount(amount):
            return None
    elif cost_state in ("in_flight", "released", "unpriced"):
        if amount is not None or (cost_state != "unpriced" and cost_state != state) or (cost_state == "unpriced" and state not in ("known", "unknown")):
            return None
    else:
        return None
    if reservation is not None:
        retained = Decimal(reservation["retained_amount"])
        reserved = Decimal(reservation["reserved_amount"])
        if ((cost_state in ("priced", "released", "in_flight") and retained != 0)
                or (cost_state == "unpriced" and retained != reserved)):
            return None
    return dict(value)


def cost_projection(value: dict, result: dict) -> None:
    if any(key in value and key not in result for key in ("call_ledger", "call_ledgers")):
        # A rejected supplied ledger must not turn into a trusted legacy
        # aggregate with no call history, especially for per-request fees.
        result["cost_complete"] = False
        return
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
                segment_id = cost["segment_id"]
                ledgers = result.get("call_ledgers", [])
                if "call_ledger" in result:
                    ledgers = [result["call_ledger"]]
                matching = [ledger for ledger in ledgers if ledger["segment_id"] == segment_id]
                if (ledgers and not matching
                        and any("per_request" in route for route in cost["price_card"]["routes"])):
                    break
                if matching:
                    # Restored aggregates must reconcile the same per-call
                    # request units and reservations as individual segments.
                    if not reconcile_call_costs(cost, matching[0]):
                        break
                costs.append({"cost": cost, "usage": dict(entry["usage"])})
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
