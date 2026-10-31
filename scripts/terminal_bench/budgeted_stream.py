"""One reserved HTTP attempt with native terminal usage, never a paid launcher.

The caller owns authorization, funding/legacy liability, account/price/model/time
guards, the connection's destination and credentials, and native checkpoints.
Use a fresh http.client connection factory; no SDK, redirect or retry owner may
sit behind it. Only this module serializes/sends the returned reserved bytes.
It neither reads credentials nor claims, reopens or resumes samples. A higher
Messages financial ceiling does not authorize a higher operational output cap.
"""

import json
import re
import time

from scripts.terminal_bench.fresh_budget import BudgetStop
from scripts.terminal_bench.http_stream import open_http_stream, valid_deadline
from scripts.terminal_bench.reservation_bound import request_bytes

MAX_LINE = 1 << 20
MAX_EVENT = 2 << 20
MAX_STREAM = 64 << 20
SERVER_TYPES = {"server_tool_use", "web_search_tool_result", "web_fetch_tool_result",
                "web_search_call", "web_fetch_call", "mcp_call", "tool_search_call"}


class AttemptFailure(ValueError):
    def __init__(self, code, call_id=None, usage_known=False, stream_diagnostics=None):
        super().__init__(code)
        self.call_id, self.usage_known = call_id, usage_known
        self.stream_diagnostics = stream_diagnostics


def require(value, code):
    if not value:
        raise AttemptFailure(code)


def counter(value):
    require(type(value) is int and 0 <= value <= 2**64 - 1, "invalid_usage_counter")
    return value


def unique_object(pairs):
    value = {}
    for key, item in pairs:
        require(key not in value, "duplicate_event_key")
        value[key] = item
    return value


def no_constant(value):
    raise AttemptFailure("nonfinite_event_value")


def usage_counters(protocol, usage):
    require(isinstance(usage, dict), "usage_missing")
    service = usage.get("server_tool_use", {})
    require(isinstance(service, dict) and all(type(n) is int and n == 0 for n in service.values()),
            "unpriced_server_tool_usage")
    output = counter(usage.get("output_tokens"))
    details = usage.get("output_tokens_details")
    require(details is None or isinstance(details, dict), "invalid_usage_details")
    thinking = (details or {}).get("reasoning_tokens", usage.get("thinking_tokens", usage.get("reasoning_tokens")))
    if thinking is not None:
        counter(thinking)
        require(thinking <= output, "thinking_exceeds_output")
    if protocol == "responses":
        inp = counter(usage.get("input_tokens"))
        details = usage.get("input_tokens_details")
        require(isinstance(details, dict), "cache_usage_missing")
        cached = counter(details.get("cached_tokens"))
        total = counter(usage.get("total_tokens"))
        require(total == inp + output, "usage_total_inconsistent")
    else:
        cached = counter(usage.get("cache_read_input_tokens"))
        inp = counter(usage.get("input_tokens")) + cached + counter(usage.get("cache_creation_input_tokens"))
        total = inp + output
    require(cached <= inp, "cache_exceeds_input")
    return dict(input_tokens=inp, cached_input_tokens=cached, output_tokens=output,
                thinking_tokens=thinking, total_tokens=total)


def check_items(items):
    require(isinstance(items, list), "invalid_output_items")
    for item in items:
        require(isinstance(item, dict) and item.get("type") not in SERVER_TYPES, "unpriced_server_tool_event")


class Receipt:
    """Native closure only: EOF, HTTP success and a usage delta are insufficient."""
    def __init__(self, protocol):
        self.protocol, self.identity, self.message_usage, self.final_delta = protocol, None, {}, False

    def observe(self, event):
        kind = event.get("type")
        require(isinstance(kind, str), "event_identity")
        for field in ("item", "content_block"):
            if event.get(field) is not None:
                check_items([event[field]])
        if self.protocol == "responses":
            if kind == "response.created" or kind in ("response.completed", "response.incomplete", "response.failed"):
                response = event.get("response")
                require(isinstance(response, dict) and isinstance(response.get("id"), str) and response["id"]
                        and response.get("model") == "deepseek-flash", "response_identity")
                check_items(response.get("output", []))
                if kind == "response.created":
                    require(self.identity is None and response.get("status") == "in_progress", "duplicate_or_invalid_start")
                    self.identity = response["id"]
                else:
                    require(self.identity == response["id"] and response.get("status") == kind.split(".")[1], "terminal_identity")
                    return usage_counters(self.protocol, response.get("usage"))
            elif kind == "error":
                raise AttemptFailure("provider_error_without_receipt")
        else:
            if kind == "message_start":
                message = event.get("message")
                require(self.identity is None and isinstance(message, dict) and isinstance(message.get("id"), str)
                        and message["id"] and message.get("model") == "deepseek-flash"
                        and message.get("role") == "assistant", "duplicate_or_invalid_start")
                check_items(message.get("content", []))
                require(isinstance(message.get("usage"), dict), "message_usage_missing")
                self.identity, self.message_usage = message["id"], dict(message["usage"])
            elif kind == "message_delta":
                require(self.identity is not None and not self.final_delta and isinstance(event.get("usage"), dict)
                        and isinstance(event.get("delta"), dict), "message_delta_order")
                usage = event["usage"]
                for name in ("input_tokens", "cache_read_input_tokens", "cache_creation_input_tokens", "output_tokens"):
                    if name in usage:
                        require(counter(usage[name]) >= counter(self.message_usage.get(name, 0)), "usage_counter_regressed")
                self.message_usage.update(usage)
                reason = event["delta"].get("stop_reason")
                if reason is not None:
                    require(isinstance(reason, str) and reason and "output_tokens" in usage, "final_usage_missing")
                    self.final_delta = True
            elif kind == "message_stop":
                require(self.identity is not None and self.final_delta, "message_start_or_final_usage_missing")
                return usage_counters(self.protocol, self.message_usage)
            elif kind == "error":
                raise AttemptFailure("provider_error_without_receipt")
            elif self.final_delta and kind.startswith("content_block_"):
                raise AttemptFailure("message_output_after_final_usage")
        return None


def events(stream, deadline):
    lines, name, event_size, size = [], None, 0, 0
    while True:
        require(time.monotonic() < deadline, "stream_deadline")
        line = stream.readline(MAX_LINE + 1)
        require(bool(line), "terminal_receipt_missing")
        size += len(line); event_size += len(line)
        require(len(line) <= MAX_LINE and size <= MAX_STREAM and event_size <= MAX_EVENT, "stream_bound")
        value = None
        if not line.rstrip(b"\r\n"):
            if lines:
                try:
                    value = json.loads(b"\n".join(lines), object_pairs_hook=unique_object, parse_constant=no_constant)
                except (ValueError, UnicodeError, RecursionError):
                    raise AttemptFailure("invalid_event_json") from None
                require(isinstance(value, dict) and (name is None or value.get("type") == name), "event_identity")
            lines, name, event_size = [], None, 0
        elif line.startswith(b"data:"):
            lines.append(line[5:].lstrip(b" ").rstrip(b"\r\n"))
        elif line.startswith(b"event:"):
            try:
                name = line[6:].strip().decode("ascii")
            except UnicodeError:
                raise AttemptFailure("invalid_event_name") from None
        yield line, value


def forward_attempt(budget, sample, body, *, connection_factory, headers, deadline, write_line):
    """Reserve -> one exact POST -> terminal receipt -> durable known settlement.

    Failures before a valid receipt retain the whole reservation, even connect
    errors, HTTP rejection and redirects. No exception text or payload enters
    the result. A downstream disconnect drains only this attempt (at most 30s).
    Any raised error stops the caller; it must not implement an implicit retry.
    """
    valid_deadline(deadline)
    # Freeze once before projection/validation. Caller mutation cannot change
    # the stream policy or the operational cap after financial admission.
    try:
        body = json.loads(request_bytes(body))
    except (ValueError, TypeError, RecursionError):
        raise AttemptFailure("invalid_request_json") from None
    require(time.monotonic() < deadline and isinstance(body, dict) and body.get("stream") is True,
            "stream_request_required")
    require(isinstance(headers, dict) and all(isinstance(k, str) and re.fullmatch(r"[A-Za-z0-9-]+", k)
            and k.lower() not in ("content-length", "transfer-encoding", "host", "connection", "proxy-authorization")
            and isinstance(v, str) and len(v) <= 65536 and not any(c in v for c in "\r\n")
            for k, v in headers.items()), "invalid_transport_headers")
    headers = dict(headers)
    reservation = budget.reserve(sample, body)
    requested = body["max_output_tokens" if reservation.protocol == "responses" else "max_tokens"]
    stream, connection, failure, result = None, None, None, None
    known, disconnected = False, False
    try:
        connection = connection_factory()  # Never called before durable admission.
        path = "/responses" if reservation.protocol == "responses" else "/anthropic/v1/messages"
        stream = open_http_stream(connection, path, reservation.wire_body, headers, deadline)
        require(stream.status == 200, "upstream_non_200")  # No redirect or retry.
        receipt = Receipt(reservation.protocol)
        for line, event in events(stream, deadline):
            usage = receipt.observe(event) if event is not None else None
            if usage is not None:
                budget.settle(reservation.call_id, input_tokens=usage["input_tokens"],
                              cached_input_tokens=usage["cached_input_tokens"], output_tokens=usage["output_tokens"])
                known = True
                stream.record_terminal(event["type"])
                result = dict(call_id=reservation.call_id, body_sha256=reservation.body_sha256,
                              protocol=reservation.protocol, terminal_event=event["type"], usage=usage,
                              can_resume_agent=False, automatic_paid_resume=False)
            if not disconnected:
                try:
                    write_line(line)
                except OSError:
                    disconnected = True
                    stream.record_client_end("error")
                    stream.set_deadline(min(deadline, time.monotonic() + 30), reason="downstream_drain")
            if known:
                require(usage["output_tokens"] <= requested, "requested_output_limit_exceeded")
                break  # A native terminal settles usage; do not wait for EOF.
    except BaseException as exc:
        failure = exc
    finally:
        try:
            if stream is not None:
                stream.close()
            elif connection is not None:
                connection.close()
        except BaseException as exc:
            if failure is None:
                failure = exc if isinstance(exc, (KeyboardInterrupt, SystemExit)) else AttemptFailure("transport_cleanup_failed")
    if failure is not None:
        if not known:
            try:
                budget.mark_unknown(reservation.call_id)
            except (OSError, BudgetStop):
                # Persistence failures leave the full reservation outstanding;
                # FreshBudget independently latches accounting_unavailable.
                pass
        if isinstance(failure, (KeyboardInterrupt, SystemExit)):
            raise failure
        code = str(failure) if isinstance(failure, (AttemptFailure, BudgetStop)) else "upstream_attempt_failed"
        diagnostics = stream.diagnostics() if stream is not None else getattr(failure, "stream_diagnostics", None)
        raise AttemptFailure(code, reservation.call_id, known, diagnostics) from None
    result.update(downstream_disconnected=disconnected, stream_diagnostics=stream.diagnostics())
    return result
