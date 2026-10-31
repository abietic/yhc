"""Single-attempt financial oracles; every HTTP peer is a local fixture."""

import hashlib
import http.client
import http.server
import io
import json
import os
import socket
import tempfile
import threading
import time
import unittest
from pathlib import Path
from unittest import mock

from scripts.terminal_bench.budgeted_stream import AttemptFailure, forward_attempt
from scripts.terminal_bench.fresh_budget import BudgetStop, FreshBudget, digest
from scripts.terminal_bench.test_fresh_budget import body, plan


def event(kind, **fields):
    return b"event: " + kind.encode() + b"\ndata: " + json.dumps(dict(type=kind, **fields)).encode() + b"\n\n"


def response_events(terminal="completed", thinking=8, output=20):
    usage = dict(input_tokens=100, input_tokens_details=dict(cached_tokens=80), output_tokens=output,
                 output_tokens_details=dict(reasoning_tokens=thinking), total_tokens=100 + output)
    return (event("response.created", response=dict(id="response-fixture", model="deepseek-flash", status="in_progress"))
            + event("response." + terminal, response=dict(id="response-fixture", model="deepseek-flash",
                                                         status=terminal, usage=usage)))


def message_events(stop=True, output=20):
    raw = event("message_start", message=dict(id="message-fixture", model="deepseek-flash", role="assistant",
        usage=dict(input_tokens=10, cache_read_input_tokens=80, cache_creation_input_tokens=10, output_tokens=0)))
    raw += event("message_delta", delta=dict(stop_reason="end_turn"), usage=dict(output_tokens=output))
    return raw + (event("message_stop") if stop else b"")


@unittest.skipUnless(os.name == "posix", "private budget requires POSIX")
class ForwardingTests(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        self.root = Path(self.tmp.name).resolve() / "budget"
        self.connections = []

    def budget(self, protocol="responses", cap="6"):
        value = plan((protocol,), cap=cap, sample_cap=cap)
        budget = FreshBudget.create(self.root, value, digest(value))
        self.addCleanup(budget.close)
        budget.claim_sample("sample-0")
        return budget

    def fixture_connection(self, raw, status=200, before_send=None):
        class Response:
            def __init__(self): self.status, self.data = status, io.BytesIO(raw)
            def read1(self, size): return self.data.read(size)
            def close(self): self.data.close()

        connection = mock.Mock(sock=mock.Mock())
        connection.getresponse.return_value = Response()
        connection.request.side_effect = before_send
        self.connections.append(connection)
        return connection

    def forward(self, budget, raw=None, *, factory=None, request=None, sink=None):
        return forward_attempt(budget, "sample-0", request or body(),
            connection_factory=factory or (lambda: self.fixture_connection(raw or response_events())),
            headers={"Content-Type": "application/json", "Authorization": "Bearer sentinel-provider-credential"},
            deadline=time.monotonic() + 20, write_line=sink or (lambda line: None))

    def test_exact_reserved_bytes_and_schema_order_reach_http_after_durable_reserve(self):
        budget = self.budget()
        request = body()
        request["tools"] = [dict(type="function", name="fixture", parameters=dict(type="object",
            properties={"z_first": dict(type="string"), "a_second": dict(type="string")}))]
        observed = []
        def before_send(method, path, raw, headers):
            ledger = json.loads((self.root / "ledger.json").read_bytes())
            call = ledger["calls"][0]
            self.assertEqual((method, path, call["state"], call["liability"]),
                             ("POST", "/responses", "reserved", "2.621440000000"))
            self.assertEqual(hashlib.sha256(raw).hexdigest(), call["bound"]["body_sha256"])
            self.assertLess(raw.index(b"z_first"), raw.index(b"a_second"))
            self.assertNotIn(b"mutated-after-reserve", raw)
            observed.append(raw)
        def factory():
            request["input"] = "mutated-after-reserve"
            return self.fixture_connection(response_events(), before_send=before_send)
        result = self.forward(budget, factory=factory, request=request)
        self.assertEqual(result["body_sha256"], hashlib.sha256(observed[0]).hexdigest())
        self.assertEqual(result["usage"]["thinking_tokens"], 8)
        self.assertEqual(budget.snapshot()["known_cost"], "0.000203200000")
        self.assertEqual(budget.snapshot()["outstanding"], "0.000000000000")
        self.assertNotIn("sentinel-provider-credential", json.dumps(result))

    def test_exhausted_sample_never_creates_connection_or_retries(self):
        budget = self.budget(cap="2.621440")
        self.forward(budget)
        factory = mock.Mock()
        with self.assertRaisesRegex(BudgetStop, "sample_budget_exhausted"):
            self.forward(budget, factory=factory)
        factory.assert_not_called()

    def test_invalid_transport_envelope_has_no_reservation_or_connection(self):
        budget = self.budget()
        factory = mock.Mock()
        invalid = [(dict(body(), stream=False), {}, time.monotonic() + 20),
                   (body(), {"Content-Length": "0"}, time.monotonic() + 20),
                   (body(), {"X-Fixture": "value\r\nHost: wrong"}, time.monotonic() + 20),
                   (body(), {}, time.monotonic() - 1)]
        for request, headers, deadline in invalid:
            with self.subTest(headers=list(headers)), self.assertRaises(AttemptFailure):
                forward_attempt(budget, "sample-0", request, connection_factory=factory,
                                headers=headers, deadline=deadline, write_line=lambda line: None)
        factory.assert_not_called()
        self.assertEqual(json.loads((self.root / "ledger.json").read_bytes())["calls"], [])

    def test_unknown_connect_send_headers_read_and_redirect_never_retry(self):
        for stage in ("factory", "connect", "request", "getresponse", "read", "redirect", "eof"):
            with self.subTest(stage=stage), tempfile.TemporaryDirectory() as temporary:
                value = plan(("responses",), cap="6", sample_cap="6")
                budget = FreshBudget.create(Path(temporary).resolve() / "budget", value, digest(value))
                with budget:
                    budget.claim_sample("sample-0")
                    connection = self.fixture_connection(b"" if stage == "eof" else response_events(),
                                                         status=307 if stage == "redirect" else 200)
                    factory = mock.Mock(return_value=connection)
                    if stage == "factory": factory.side_effect = socket.timeout("private network text")
                    elif stage in ("connect", "request", "getresponse"):
                        getattr(connection, stage).side_effect = OSError("private network text")
                    elif stage == "read":
                        connection.getresponse.return_value.read1 = mock.Mock(side_effect=OSError("private network text"))
                    with self.assertRaises(AttemptFailure) as failed:
                        self.forward(budget, factory=factory)
                    self.assertNotIn("private network text", str(failed.exception))
                    self.assertFalse(failed.exception.usage_known)
                    diagnostic = failed.exception.stream_diagnostics
                    if stage == "connect": self.assertEqual(diagnostic["first_error_code"], "connect_error")
                    if stage == "read": self.assertEqual(diagnostic["first_error_code"], "upstream_read_error")
                    self.assertNotIn("private network text", json.dumps(diagnostic))
                    self.assertEqual(factory.call_count, 1)
                    self.assertLessEqual(connection.request.call_count, 1)
                    snap = budget.snapshot()
                    self.assertTrue(snap["unknown_usage"])
                    self.assertEqual(snap["retained"], "2.621440000000")
                    with self.assertRaisesRegex(BudgetStop, "unknown_usage"):
                        self.forward(budget, factory=factory)
                    self.assertEqual(factory.call_count, 1)

    def test_messages_requires_start_final_usage_and_stop_and_preserves_unknown_thinking(self):
        budget = self.budget("messages")
        result = self.forward(budget, raw=message_events(), request=body("messages"))
        self.assertEqual(self.connections[0].request.call_args.args[1], "/anthropic/v1/messages")
        self.assertEqual(result["terminal_event"], "message_stop")
        self.assertEqual(result["usage"], dict(input_tokens=100, cached_input_tokens=80,
            output_tokens=20, thinking_tokens=None, total_tokens=120))
        self.assertEqual(budget.snapshot()["known_cost"], "0.000203200000")

    def test_messages_partial_final_delta_is_not_closed_usage(self):
        budget = self.budget("messages")
        with self.assertRaisesRegex(AttemptFailure, "terminal_receipt_missing"):
            self.forward(budget, raw=message_events(stop=False), request=body("messages"))
        self.assertEqual(budget.snapshot()["retained"], "5.242880000000")

    def test_incomplete_or_failed_response_with_valid_usage_is_known_spend_not_task_success(self):
        for terminal in ("incomplete", "failed"):
            with self.subTest(terminal=terminal), tempfile.TemporaryDirectory() as temporary:
                value = plan(("responses",), cap="6", sample_cap="6")
                with FreshBudget.create(Path(temporary).resolve() / "budget", value, digest(value)) as budget:
                    budget.claim_sample("sample-0")
                    result = self.forward(budget, raw=response_events(terminal))
                    self.assertEqual(result["terminal_event"], "response." + terminal)
                    self.assertEqual(budget.snapshot()["known_cost"], "0.000203200000")
                    self.assertFalse(result["can_resume_agent"])

    def test_invalid_terminal_usage_server_tool_or_event_identity_keeps_full_reserve(self):
        created = response_events().split(b"event: response.completed")[0]
        terminal = json.loads(response_events().split(b"data: ")[-1])
        cases = []
        for path, value in [("cached_tokens", 101), ("reasoning_tokens", 21), ("output_tokens", True)]:
            row = json.loads(json.dumps(terminal))
            if path == "cached_tokens": row["response"]["usage"]["input_tokens_details"][path] = value
            elif path == "reasoning_tokens": row["response"]["usage"]["output_tokens_details"][path] = value
            else: row["response"]["usage"][path] = value
            cases.append(created + b"data: " + json.dumps(row).encode() + b"\n\n")
        row = json.loads(json.dumps(terminal)); row["response"]["id"] = "wrong-id"
        cases.extend([created + b"data: " + json.dumps(row).encode() + b"\n\n",
            created + event("response.output_item.added", item=dict(type="web_search_call")),
            created + b'event: wrong\ndata: {"type":"response.completed"}\n\n',
            created + b'data: {"type":"response.completed","type":"response.failed"}\n\n'])
        for raw in cases:
            with self.subTest(size=len(raw)), tempfile.TemporaryDirectory() as temporary:
                value = plan(("responses",), cap="6", sample_cap="6")
                with FreshBudget.create(Path(temporary).resolve() / "budget", value, digest(value)) as budget:
                    budget.claim_sample("sample-0")
                    with self.assertRaises(AttemptFailure): self.forward(budget, raw=raw)
                    self.assertEqual(budget.snapshot()["retained"], "2.621440000000")

    def test_downstream_disconnect_drains_same_attempt_to_closed_usage_without_retry(self):
        budget = self.budget()
        sink = mock.Mock(side_effect=BrokenPipeError("private downstream text"))
        result = self.forward(budget, sink=sink)
        self.assertTrue(result["downstream_disconnected"])
        self.assertEqual(result["usage"]["output_tokens"], 20)
        self.assertEqual(sink.call_count, 1)
        self.assertEqual(len(self.connections), 1)
        self.assertFalse(budget.snapshot()["unknown_usage"])

    def test_stalled_disconnect_has_shortened_drain_deadline_and_full_retention(self):
        budget = self.budget()
        now = [100.0]
        connection = self.fixture_connection(b"")
        reads = []
        def stalled_read(size):
            reads.append(size)
            if len(reads) == 1: return b": fixture preamble\n\n"
            now[0] = 131.0
            return b""
        connection.getresponse.return_value.read1 = stalled_read
        with mock.patch("time.monotonic", side_effect=lambda: now[0]), \
                mock.patch("scripts.terminal_bench.http_stream.threading.Timer", return_value=mock.Mock()):
            with self.assertRaises(AttemptFailure) as failure:
                forward_attempt(budget, "sample-0", body(), connection_factory=lambda: connection,
                    headers={}, deadline=160.0, write_line=mock.Mock(side_effect=BrokenPipeError))
        diagnostic = failure.exception.stream_diagnostics
        self.assertEqual(diagnostic["deadline_revisions"]["downstream_drain"], 30.0)
        self.assertEqual(diagnostic["first_timeout_reason"], "downstream_drain")
        self.assertEqual(budget.snapshot()["retained"], "2.621440000000")

    def test_storage_failure_before_send_never_opens_upstream(self):
        budget = self.budget()
        factory = mock.Mock()
        with mock.patch("scripts.terminal_bench.fresh_budget.os.replace", side_effect=OSError("disk")):
            with self.assertRaises(OSError): self.forward(budget, factory=factory)
        factory.assert_not_called()
        self.assertTrue(budget.snapshot()["accounting_error"])

    def test_settlement_publication_failure_after_terminal_keeps_full_reservation(self):
        budget = self.budget()
        replace = os.replace
        publications = []
        def fail_second(*args, **kwargs):
            publications.append(True)
            if len(publications) == 2: raise OSError("private disk detail")
            return replace(*args, **kwargs)
        with mock.patch("scripts.terminal_bench.fresh_budget.os.replace", side_effect=fail_second):
            with self.assertRaises(AttemptFailure) as failure: self.forward(budget)
        self.assertFalse(failure.exception.usage_known)
        self.assertNotIn("private disk detail", str(failure.exception))
        snap = budget.snapshot()
        self.assertTrue(snap["unknown_usage"])
        self.assertTrue(snap["accounting_error"])
        self.assertEqual(snap["known_cost"], "0.000000000000")
        self.assertEqual(snap["exposure"], "2.621440000000")

    def test_messages_financial_ceiling_does_not_authorize_exceeding_requested_output(self):
        budget = self.budget("messages")
        with self.assertRaisesRegex(AttemptFailure, "requested_output_limit_exceeded") as failure:
            self.forward(budget, raw=message_events(output=70000), request=body("messages"))
        self.assertTrue(failure.exception.usage_known)
        self.assertEqual(budget.snapshot()["known_cost"], "0.560043200000")
        self.assertFalse(budget.snapshot()["unknown_usage"])

    def test_missing_thinking_stays_unknown_while_closed_token_cost_is_known(self):
        budget = self.budget()
        result = self.forward(budget, raw=response_events(thinking=None))
        self.assertIsNone(result["usage"]["thinking_tokens"])
        self.assertEqual(budget.snapshot()["known_cost"], "0.000203200000")

    def test_keyboard_interrupt_is_preserved_after_conservative_accounting(self):
        budget = self.budget()
        factory = mock.Mock(side_effect=KeyboardInterrupt)
        with self.assertRaises(KeyboardInterrupt): self.forward(budget, factory=factory)
        self.assertEqual(factory.call_count, 1)
        self.assertEqual(budget.snapshot()["retained"], "2.621440000000")

    def test_cleanup_failure_after_known_receipt_cannot_erase_known_spend(self):
        budget = self.budget()
        connection = self.fixture_connection(response_events())
        connection.close.side_effect = OSError("private cleanup detail")
        with self.assertRaisesRegex(AttemptFailure, "transport_cleanup_failed") as failure:
            self.forward(budget, factory=lambda: connection)
        self.assertTrue(failure.exception.usage_known)
        self.assertEqual(budget.snapshot()["known_cost"], "0.000203200000")

    def test_bad_message_order_and_stream_bounds_do_not_authorize_settlement(self):
        cases = [message_events().split(b"event: message_delta")[0] + event("message_stop"),
                 event("message_delta", delta=dict(stop_reason="end_turn"), usage=dict(output_tokens=2)) + event("message_stop"),
                 message_events(stop=False) + event("content_block_delta", delta=dict(type="text_delta", text="fixture")),
                 message_events().split(b"event: message_delta")[0] + event("message_delta",
                    delta=dict(stop_reason="end_turn"), usage=dict(input_tokens=1, output_tokens=2)) + event("message_stop"),
                 b"data: " + b"x" * (1 << 20)]
        for raw in cases:
            with self.subTest(size=len(raw)), tempfile.TemporaryDirectory() as temporary:
                value = plan(("messages",), cap="6", sample_cap="6")
                with FreshBudget.create(Path(temporary).resolve() / "budget", value, digest(value)) as budget:
                    budget.claim_sample("sample-0")
                    with self.assertRaises(AttemptFailure): self.forward(budget, raw=raw, request=body("messages"))
                    self.assertEqual(budget.snapshot()["retained"], "5.242880000000")

    def test_closed_first_sample_and_unknown_second_keep_cumulative_liability(self):
        value = plan(("responses", "messages"), cap="9", sample_cap="3")
        value["samples"][1]["max_estimated_cost"] = "6"
        with FreshBudget.create(self.root, value, digest(value)) as budget:
            budget.claim_sample("sample-0")
            first = self.forward(budget)
            budget.finish_sample("sample-0", "completed")
            budget.claim_sample("sample-1")
            with self.assertRaises(AttemptFailure):
                forward_attempt(budget, "sample-1", body("messages"),
                    connection_factory=lambda: self.fixture_connection(message_events(stop=False)),
                    headers={}, deadline=time.monotonic() + 20, write_line=lambda line: None)
            budget.finish_sample("sample-1", "unknown_usage")
            snapshot = budget.snapshot()
            self.assertEqual(snapshot["known_cost"], "0.000203200000")
            self.assertEqual(snapshot["retained"], "5.242880000000")
            self.assertEqual(snapshot["exposure"], "5.243083200000")
            rows = json.loads((self.root / "ledger.json").read_bytes())["calls"]
            self.assertEqual(rows[0]["bound"]["body_sha256"], first["body_sha256"])
            self.assertEqual([r["state"] for r in rows], ["known", "unknown"])
        with self.assertRaises(FileExistsError): FreshBudget.create(self.root, value, digest(value))

    def test_local_http_peer_observes_exact_durable_hash_and_one_post(self):
        budget = self.budget()
        observations = []
        class Handler(http.server.BaseHTTPRequestHandler):
            def log_message(self, *args): pass
            def do_POST(self):
                raw = self.rfile.read(int(self.headers["Content-Length"]))
                row = json.loads((budget.root / "ledger.json").read_bytes())["calls"][0]
                observations.append((self.path, hashlib.sha256(raw).hexdigest(), row["bound"]["body_sha256"], row["state"]))
                self.send_response(200); self.end_headers(); self.wfile.write(response_events())
        with http.server.HTTPServer(("127.0.0.1", 0), Handler) as server:
            thread = threading.Thread(target=server.handle_request)
            thread.start()
            try:
                self.forward(budget, factory=lambda: http.client.HTTPConnection("127.0.0.1", server.server_port, timeout=2))
            finally:
                thread.join(5)
        self.assertFalse(thread.is_alive())
        self.assertEqual(len(observations), 1)
        self.assertEqual(observations[0][0], "/responses")
        self.assertEqual(observations[0][1], observations[0][2])
        self.assertEqual(observations[0][3], "reserved")


if __name__ == "__main__":
    unittest.main()
