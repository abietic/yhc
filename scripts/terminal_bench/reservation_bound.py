"""Contract-conditional DeepSeek token bounds, never fitted token estimates.

The operator must independently pin and refresh the upper-bound price card and
model contract before live admission. No tokenizer, historical cache ratio,
character ratio, or post-response violation detector proves a smaller bound.
Messages compatibility may raise max_tokens; reserve the model output ceiling.
Sources: https://api-docs.deepseek.com/zh-cn/guides/anthropic_api/ and
https://api-docs.deepseek.com/zh-cn/quick_start/pricing/ (checked 2026-10-31).
This module performs no I/O, credential lookup, or provider request.
"""

import hashlib
import json
from decimal import Decimal, localcontext

from scripts.terminal_bench.native_cost import validate_price_card

CONTEXT_TOKENS = 1048576
MODEL_OUTPUT_TOKENS = 393216
MODEL = "deepseek-flash"
MAX_BODY_BYTES = 32 << 20


def canonical(value):
    return json.dumps(value, sort_keys=True, ensure_ascii=False, separators=(",", ":"), allow_nan=False).encode("utf-8")


def request_bytes(body):
    # Keep tool-schema property order. Canonical plan hashing is not a request
    # projection policy and must not reorder model-visible object fields.
    return json.dumps(body, ensure_ascii=False, separators=(",", ":"), allow_nan=False).encode("utf-8")


def digest(value):
    return hashlib.sha256(canonical(value)).hexdigest()


def price_route(card):
    card = validate_price_card(card)
    if card["window"] != "upper_bound" or len(card["routes"]) != 1:
        raise ValueError("explicit_upper_bound_token_price_required")
    route = card["routes"][0]
    if route["provider"] != "agenticdeepseek" or route.get("api_model") != MODEL or "per_request" in route:
        raise ValueError("unsupported_reservation_route")
    return route


def token_cost(card, input_tokens, cached_input_tokens, output_tokens):
    route = price_route(card)
    with localcontext() as context:
        context.prec = 90
        cost = (Decimal(input_tokens - cached_input_tokens) * Decimal(route["uncached_input_per_million"])
                + Decimal(cached_input_tokens) * Decimal(route["cached_input_per_million"])
                + Decimal(output_tokens) * Decimal(route["output_per_million"])) / Decimal(1000000)
        return format(cost, ".12f")


def request_bound(body, protocol, card, output_cap):
    """Bind a conservative rectangular bound to one complete frozen JSON body.

    Model ceilings and an operator-verified rate ceiling are assumptions, not
    receipts from a provider. Context includes tools, media and hidden framing;
    an unverified frontend/tokenizer cannot replace its full bound with bytes.
    """
    if (not isinstance(body, dict) or body.get("model") != MODEL or protocol not in ("responses", "messages")
            or type(output_cap) is not int or not 1 <= output_cap <= MODEL_OUTPUT_TOKENS):
        raise ValueError("unsupported_request_bound")
    key = "max_output_tokens" if protocol == "responses" else "max_tokens"
    requested = body.get(key)
    if type(requested) is not int or not 1 <= requested <= output_cap:
        raise ValueError("explicit_output_cap_required")
    tools = body.get("tools", [])
    if not isinstance(tools, list) or any(not isinstance(t, dict) or (
            t.get("type") not in ("function", "custom") if protocol == "responses" else "type" in t) for t in tools):
        raise ValueError("unpriced_server_tools_forbidden")
    if protocol == "responses" and any(t.get("type") == "custom" and t.get("name") != "apply_patch" for t in tools):
        raise ValueError("unsupported_custom_tool")
    route = price_route(card)
    raw = request_bytes(body)
    if len(raw) > MAX_BODY_BYTES:
        raise ValueError("request_body_bound")
    output = requested if protocol == "responses" else MODEL_OUTPUT_TOKENS
    with localcontext() as context:
        context.prec = 90
        # Cache hits are not promised. Some tariffs can price hits above misses.
        input_rate = max(Decimal(route["cached_input_per_million"]), Decimal(route["uncached_input_per_million"]))
        cost = (Decimal(CONTEXT_TOKENS) * input_rate
                + Decimal(output) * Decimal(route["output_per_million"])) / Decimal(1000000)
    return dict(version=1, provider="agenticdeepseek", model=MODEL, protocol=protocol,
                currency=card["currency"], body_sha256=hashlib.sha256(raw).hexdigest(),
                price_card_sha256=digest(card), input_tokens=CONTEXT_TOKENS, output_tokens=output,
                requested_output_tokens=requested, input_bound_source="model_context_ceiling",
                output_bound_source=("responses_explicit_cap" if protocol == "responses"
                                     else "model_maximum_compatibility_fallback"),
                reserved_amount=format(cost, ".12f"))
