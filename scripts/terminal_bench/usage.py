"""Provider-reported usage validation and aggregation, without inferred billing."""

from __future__ import annotations

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
    routes = value.get("routes")
    if (isinstance(routes, list) and routes
            and all(isinstance(route, dict) and isinstance(route.get("model"), str)
                    and route["model"] and route["model"] == route["model"].strip()
                    for route in routes)):
        result["routes"] = [{"model": model} for model in sorted({route["model"] for route in routes})]
    for key in ("provider_duration_ms", "denied_calls", "released_calls"):
        if key in value:
            if type(value[key]) is not int or value[key] < 0:
                return None
            result[key] = value[key]
    return result


def sum_usage(values: list) -> dict | None:
    """Missing segments invalidate the whole total; known segments remain separate."""
    usages = [validated_usage(value) for value in values]
    if not usages or any(value is None for value in usages):
        return None
    result = {key: sum(value[key] for value in usages) for key in USAGE_FIELDS}
    result["complete"] = all(value["complete"] for value in usages)
    if all("routes" in value for value in usages):
        result["routes"] = [{"model": model} for model in sorted({
            route["model"] for value in usages for route in value["routes"]})]
    for key in ("provider_duration_ms", "denied_calls", "released_calls"):
        if all(key in value for value in usages):
            result[key] = sum(value[key] for value in usages)
    return result
