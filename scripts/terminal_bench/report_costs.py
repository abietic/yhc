"""Offline cost scenarios for benchmark statistics; never a provider bill.

python -m scripts.terminal_bench.report_costs --statistics statistics.json \
    --rates rates.json --output costs.json
Rates are explicit, dated cards with a source and cached/input/output prices per
million tokens. No model call or provider balance access occurs.
"""

from __future__ import annotations

import argparse
import json
import re
from datetime import datetime
from decimal import Decimal, InvalidOperation
from pathlib import Path

from scripts.terminal_bench.usage import LEDGER_FIELDS, sum_usage, validated_usage

TOKEN_FIELDS = ("prompt_tokens", "completion_tokens", "total_tokens",
                "cached_prompt_tokens", "reasoning_tokens")


def validate_currency(currency: str) -> str:
    if not isinstance(currency, str) or not re.fullmatch(r"[A-Z]{3}", currency):
        raise ValueError("Currency must be an explicit three-letter uppercase code, e.g. CNY or USD")
    return currency


def validate_fx_snapshots(snapshots: list[dict]) -> list[dict]:
    pairs = set()
    for snapshot in snapshots:
        if not isinstance(snapshot, dict):
            raise ValueError("Invalid FX snapshot")
        base, quote = (validate_currency(snapshot.get(key)) for key in ("base", "quote"))
        pair = tuple(sorted((base, quote)))
        if base == quote or pair in pairs:
            raise ValueError("FX requires one unambiguous snapshot per currency pair")
        pairs.add(pair)
        if any(not isinstance(snapshot.get(key), str) or not snapshot[key].strip()
               for key in ("as_of", "source")):
            raise ValueError("FX requires a date and source")
        try:
            datetime.fromisoformat(snapshot["as_of"].replace("Z", "+00:00"))
            rate = Decimal(str(snapshot.get("rate")))
        except (ValueError, InvalidOperation) as exc:
            raise ValueError("Invalid FX date or rate") from exc
        if isinstance(snapshot.get("rate"), bool) or not rate.is_finite() or rate <= 0:
            raise ValueError("FX rate must be finite and positive")
    return snapshots


def convert_estimate(amount: str | None, currency: str, target: str,
                     snapshots: list[dict]) -> dict:
    result = {"original_amount": amount, "original_currency": currency, "currency": target,
              "amount": None, "fx_snapshot": None, "inverted": False, "status": "unpriced"}
    if amount is None:
        return result
    if currency == target:
        return {**result, "amount": amount, "status": "original_currency"}
    for snapshot in snapshots:
        if {snapshot["base"], snapshot["quote"]} == {currency, target}:
            inverted = snapshot["base"] != currency
            rate = Decimal(str(snapshot["rate"]))
            converted = Decimal(amount) / rate if inverted else Decimal(amount) * rate
            return {**result, "amount": str(converted), "fx_snapshot": snapshot,
                    "inverted": inverted, "status": "converted_estimate"}
    # Never derive an FX rate from two independently published token price cards.
    return {**result, "status": "missing_fx"}


def add_conversions(report: dict, cards: list[dict], target: str, snapshots: list[dict]) -> None:
    for row in report["trials"]:
        row["converted_estimates"] = {
            card["name"]: convert_estimate(row["cost_scenarios"][card["name"]], card["currency"], target, snapshots)
            for card in cards}
    for key in ("all_supplied_trials", "successful_trials"):
        converted = {}
        for card in cards:
            value = report[key]["currency_estimates"][card["name"]]
            estimate = convert_estimate(value["known_amount"], card["currency"], target, snapshots)
            converted[card["name"]] = {**estimate,
                "complete": value["complete"] and estimate["amount"] is not None,
                "priced_trials": value["priced_trials"], "unpriced_trials": value["unpriced_trials"]}
        report[key]["converted_estimates"] = converted


def hydrate_continuation(stat: dict) -> dict:
    """Old summarizers read only the latest stream; recover all segments from Harbor."""
    path = Path(stat["trial_path"]) / "result.json"
    if not path.exists():
        return stat
    result = json.loads(path.read_text())
    metadata = ((result.get("agent_result") or {}).get("metadata") or {}).get("yhc") or {}
    continuation = metadata.get("continuation")
    if continuation is None:
        return stat
    history = continuation.get("history") if isinstance(continuation, dict) else None
    segments = continuation.get("segments") if isinstance(continuation, dict) else None
    if (not isinstance(history, list) or type(segments) is not int or not 2 <= segments <= 17
            or len(history) > segments or any(not isinstance(entry, dict) for entry in history)):
        raise ValueError("Invalid continuation accounting history")
    values = [entry.get("usage") for entry in history]
    if len(history) == segments:
        total = sum_usage(values)
        if total is not None:
            reported = validated_usage(metadata.get("usage"))
            # Older adapter aggregates omitted routes even when segment
            # receipts retained them. Compare accounting totals independently.
            if reported is not None and (
                    {key: value for key, value in reported.items() if key not in ("routes", *LEDGER_FIELDS)}
                    != {key: value for key, value in total.items() if key not in ("routes", *LEDGER_FIELDS)}):
                raise ValueError("Continuation aggregate disagrees with segment usage")
            if reported is not None and "routes" in reported and (
                    {route["model"] for route in reported["routes"]}
                    != {route["model"] for route in total.get("routes", [])}):
                raise ValueError("Continuation aggregate disagrees with segment models")
            return {**stat, "usage": total}
    known = [value for value in values if validated_usage(value) is not None]
    total = sum_usage(known)
    # A missing latest stream remains unknown; retain only the proven lower bound.
    partial = {"complete": False, "totals": token_totals(total)} if total else None
    if partial is not None and "routes" in total:
        partial["routes"] = total["routes"]
    return {**stat, "usage": None, "partial_usage_lower_bound": partial}


def token_totals(value: object) -> dict:
    if not isinstance(value, dict) or any(type(value.get(key)) is not int or value[key] < 0 for key in TOKEN_FIELDS):
        raise ValueError("Invalid token totals")
    if (value["cached_prompt_tokens"] > value["prompt_tokens"]
            or value["reasoning_tokens"] > value["completion_tokens"]
            or value["total_tokens"] < value["prompt_tokens"] + value["completion_tokens"]):
        raise ValueError("Inconsistent token totals")
    return {key: value[key] for key in TOKEN_FIELDS}


def usage_for_row(row: dict) -> tuple[dict | None, str]:
    if row.get("usage") is not None:
        usage = validated_usage(row["usage"])
        if usage is None:
            raise ValueError("Invalid usage; cannot calculate a cost scenario")
        return token_totals(usage), "complete" if usage["complete"] else "lower_bound"
    partial = row.get("partial_usage_lower_bound")
    if partial is not None:
        if not isinstance(partial, dict) or partial.get("complete") is not False:
            raise ValueError("Recovered usage must be explicitly marked incomplete")
        totals = token_totals(partial.get("totals"))
        route_usage = partial if "routes" in partial else partial["totals"]
        if "routes" in route_usage and (not isinstance(route_usage["routes"], list)
                or any(not isinstance(route, dict) or not isinstance(route.get("model"), str)
                       for route in route_usage["routes"])):
            raise ValueError("Invalid recovered model routes")
        return totals, "lower_bound"
    return None, "unknown"


def validate_rate_card(card: dict) -> dict:
    if not isinstance(card, dict) or any(not isinstance(card.get(key), str) or not card[key].strip()
                                        for key in ("name", "currency", "as_of", "source")):
        raise ValueError("Rates require name, currency, as_of and source")
    validate_currency(card["currency"])
    try:
        datetime.fromisoformat(card["as_of"].replace("Z", "+00:00"))
    except ValueError as exc:
        raise ValueError("Invalid rate card date") from exc
    if not isinstance(card.get("models"), dict) or not card["models"]:
        raise ValueError("Rates require explicit model prices")
    for prices in card["models"].values():
        if not isinstance(prices, dict):
            raise ValueError("Invalid model prices")
        for key in ("cached_input_per_million", "uncached_input_per_million", "output_per_million"):
            value = prices.get(key)
            if isinstance(value, bool):
                raise ValueError("Invalid token price")
            try:
                price = Decimal(str(value))
            except InvalidOperation as exc:
                raise ValueError("Invalid token price") from exc
            if not price.is_finite() or price < 0:
                raise ValueError("Invalid token price")
    return card


def estimate(tokens: dict | None, model: str | None, card: dict) -> str | None:
    prices = card["models"].get(model)
    if tokens is None or prices is None:
        return None
    # Thinking is part of output; cached input is part of input. Neither is added twice.
    amount = (tokens["cached_prompt_tokens"] * Decimal(str(prices["cached_input_per_million"]))
              + (tokens["prompt_tokens"] - tokens["cached_prompt_tokens"]) * Decimal(str(prices["uncached_input_per_million"]))
              + tokens["completion_tokens"] * Decimal(str(prices["output_per_million"]))) / Decimal(1000000)
    return str(amount)


def aggregate(rows: list[dict], cards: list[dict]) -> dict:
    known = [row for row in rows if row["tokens"] is not None]
    counts = {kind: sum(row["usage_coverage"] == kind for row in rows)
              for kind in ("complete", "lower_bound", "unknown")}
    result = {"trials": len(rows), "coverage": counts,
              "known_tokens": {key: sum(row["tokens"][key] for row in known) for key in TOKEN_FIELDS},
              "currency_estimates": {}, "actual_billed_cost": None}
    for card in cards:
        values = [row["cost_scenarios"][card["name"]] for row in rows]
        known_costs = [value for value in values if value is not None]
        result["currency_estimates"][card["name"]] = {
            "known_amount": str(sum((Decimal(value) for value in known_costs), Decimal(0))) if known_costs else None,
            "priced_trials": len(known_costs), "unpriced_trials": len(rows) - len(known_costs),
            "complete": counts["lower_bound"] == counts["unknown"] == 0 and len(known_costs) == len(rows),
            "currency": card["currency"],
        }
    return result


def build_report(statistics: list[dict], rate_cards: list[dict], *,
                 settlement_currency: str | None = None, display_currency: str | None = None,
                 fx_snapshots: list[dict] | None = None) -> dict:
    cards = [validate_rate_card(card) for card in rate_cards]
    if settlement_currency is not None:
        validate_currency(settlement_currency)
    if display_currency is not None:
        validate_currency(display_currency)
    snapshots = validate_fx_snapshots(fx_snapshots or [])
    if snapshots and display_currency is None:
        raise ValueError("FX snapshots require an explicit display currency")
    if len({card["name"] for card in cards}) != len(cards):
        raise ValueError("Rate scenario names must be unique")
    rows = []
    seen = set()
    for stat in statistics:
        path = stat.get("trial_path")
        if not isinstance(path, str) or not path or path in seen:
            raise ValueError("Each trial requires a unique trial_path; duplicate costs are forbidden")
        seen.add(path)
        tokens, coverage = usage_for_row(stat)
        manifest = stat.get("manifest") or {}
        model = manifest.get("model_request")
        recorded_usage = stat.get("usage") or stat.get("partial_usage_lower_bound") or {}
        route_usage = (recorded_usage if "routes" in recorded_usage else
                       recorded_usage.get("totals") or recorded_usage)
        routes = route_usage.get("routes") or []
        route_models = {route.get("model") for route in routes}
        mixed_routes = len(route_models) > 1
        # Routes name configured profiles (e.g. "bench"), not resolved vendor
        # IDs. One known profile retains the explicit experiment-model scenario.
        unpriced_routes = mixed_routes or any(
            not profile or profile != profile.strip() for profile in route_models) or (
            "routes" in route_usage and tokens and tokens["total_tokens"] > 0 and not routes)
        reward = stat.get("reward")
        if isinstance(reward, dict):
            reward = reward.get("reward")
        rows.append({"trial_path": path, "task": manifest.get("task"), "model_request": model,
                     "effort": stat.get("effort"), "started_at": manifest.get("started_at"),
                     "reward": reward, "successful": type(reward) in (int, float) and reward > 0,
                     "agent_seconds": stat.get("agent_seconds"), "usage_coverage": coverage, "tokens": tokens,
                     "provider_calls": (stat.get("usage") or {}).get("provider_calls"),
                     "cache_hit_rate": tokens["cached_prompt_tokens"] / tokens["prompt_tokens"] if tokens and tokens["prompt_tokens"] else None,
                     "cost_model_assumption": ("unpriced_mixed_routes" if mixed_routes else
                         "unpriced_unknown_model_routes" if unpriced_routes else "experiment_model_applies_to_recorded_calls"),
                     "cost_scenarios": {card["name"]: None if unpriced_routes else estimate(tokens, model, card) for card in cards},
                     "actual_billed_cost": None})
    # Only this explicitly supplied cohort is covered, never the user's account total.
    successful = [row for row in rows if row["successful"]]
    report = {"schema_version": 2, "billing_kind": "explicit_rate_scenarios_not_bill",
            "settlement_currency": settlement_currency,
            "settlement_rate_cards": [card["name"] for card in cards if card["currency"] == settlement_currency],
            "display_currency": display_currency, "fx_snapshots": snapshots,
            "rate_cards": cards, "trials": rows, "all_supplied_trials": aggregate(rows, cards),
            "successful_trials": aggregate(successful, cards)}
    if display_currency is not None:
        add_conversions(report, cards, display_currency, snapshots)
    return report


def main() -> None:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--statistics", type=Path, action="append", required=True)
    parser.add_argument("--rates", type=Path, action="append", default=[])
    parser.add_argument("--settlement-currency", help="Explicit account currency; not inferred from provider identity")
    parser.add_argument("--display-currency", help="Optional conversion view; original amounts remain unchanged")
    parser.add_argument("--fx", type=Path, action="append", default=[], help="Dated FX snapshot JSON, one per pair")
    parser.add_argument("--output", type=Path, required=True)
    args = parser.parse_args()
    rows = []
    for path in args.statistics:
        value = json.loads(path.read_text())
        if not isinstance(value, list) or any(not isinstance(row, dict) for row in value):
            raise ValueError("statistics must be a JSON list of trial rows")
        rows.extend(hydrate_continuation(row) for row in value)
    report = build_report(rows, [json.loads(path.read_text()) for path in args.rates],
                          settlement_currency=args.settlement_currency, display_currency=args.display_currency,
                          fx_snapshots=[json.loads(path.read_text()) for path in args.fx])
    # A new path protects previous accounting snapshots from accidental replacement.
    with args.output.open("x", encoding="utf-8") as stream:
        json.dump(report, stream, ensure_ascii=False, indent=2)
        stream.write("\n")


if __name__ == "__main__":
    main()
