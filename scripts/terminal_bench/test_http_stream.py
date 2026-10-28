"""Offline timing/privacy oracles; no provider, credential or model artifacts."""
import http.client
import http.server
import json
import socket
import threading
import time
import unittest
from unittest import mock

from scripts.terminal_bench import http_stream


class ManualTimer:
    def __init__(self, delay, callback, args=()):
        self.delay, self.callback, self.args = delay, callback, args
        self.cancelled = False

    def start(self): pass
    def cancel(self): self.cancelled = True
    def fire(self): self.callback(*self.args)


class StreamTests(unittest.TestCase):
    def setUp(self):
        self.now, self.timers = 100.0, []
        self.clock = mock.patch.object(http_stream.time, "monotonic", side_effect=lambda: self.now)
        self.timer = mock.patch.object(http_stream.threading, "Timer", side_effect=self.new_timer)
        self.clock.start(); self.timer.start()
        self.addCleanup(self.clock.stop); self.addCleanup(self.timer.stop)
        self.connection = mock.Mock(sock=mock.Mock())
        self.response = mock.Mock(status=200)
        self.connection.getresponse.return_value = self.response

    def new_timer(self, *args, **kwargs):
        timer = ManualTimer(*args, **kwargs)
        self.timers.append(timer)
        return timer

    def opened(self):
        stream = http_stream.open_http_stream(self.connection, "/fixture", b"synthetic body",
                                             {"Authorization": "Bearer synthetic-canary"}, 1000)
        self.addCleanup(stream.close)
        return stream

    def test_native_idle_then_shutdown_drain_keeps_distinct_timestamps(self):
        stream = self.opened()
        self.now = 110
        self.response.read1.return_value = b"data: synthetic\n"
        self.assertEqual(stream.readline(100), b"data: synthetic\n")
        self.now = 410
        stream.record_client_end("idle_timeout")
        stream.set_deadline(440, reason="shutdown_drain")
        self.now = 440
        self.timers[-1].fire()
        self.response.read1.return_value = b""
        with self.assertRaises(TimeoutError): stream.readline(100)
        data = stream.diagnostics()
        self.assertEqual(data["last_byte_seconds"], 10)
        self.assertEqual(data["client_end"], {"reason": "idle_timeout", "elapsed_seconds": 310})
        self.assertEqual(data["timeout_reason"], "shutdown_drain")
        self.assertEqual(data["deadline_revisions"], {"absolute_deadline": 900, "shutdown_drain": 340})
        self.assertIsNone(data["terminal_event"])
        self.assertEqual(data["authority"], "diagnostic_only")

    def test_body_socket_timeout_before_timer_uses_its_configured_deadline(self):
        stream = self.opened()
        self.now = 120
        self.response.read1.side_effect = socket.timeout("private upstream detail")
        with self.assertRaises(TimeoutError): stream.readline(100)
        self.assertFalse(self.timers[0].cancelled)
        self.assertEqual(stream.diagnostics()["timeout_reason"], "absolute_deadline")
        self.assertEqual(stream.diagnostics()["timeout_source"], "socket_timeout")

    def test_header_socket_timeout_before_timer_is_normalized_without_text(self):
        self.connection.getresponse.side_effect = socket.timeout("private early socket detail")
        with self.assertRaises(TimeoutError) as failure: self.opened()
        self.assertEqual(type(failure.exception), TimeoutError)
        self.assertEqual(str(failure.exception), "stream_deadline")
        data = failure.exception.stream_diagnostics
        self.assertEqual(data["timeout_reason"], "absolute_deadline")
        self.assertEqual(data["timeout_source"], "socket_timeout")
        self.assertNotIn("private early socket detail", json.dumps(data))

    def test_absolute_deadline_is_not_extended_by_slow_bytes(self):
        stream = self.opened()
        self.response.read1.side_effect = [b"x\n", b"more\n"]
        self.now = 999
        self.assertEqual(stream.readline(10), b"x\n")
        stream.set_deadline(2000, reason="shutdown_drain")
        self.now = 1000
        self.timers[0].fire()
        with self.assertRaises(TimeoutError): stream.readline(10)
        self.assertEqual(stream.diagnostics()["timeout_reason"], "absolute_deadline")
        self.assertEqual(len(self.timers), 1)

    def test_cancelled_timer_cannot_overwrite_the_new_deadline_reason(self):
        stream = self.opened()
        old = self.timers[0]
        stream.set_deadline(200, reason="downstream_drain")
        self.now = 200
        old.fire()
        self.assertFalse(stream.timed_out)
        self.timers[-1].fire()
        self.assertEqual(stream.diagnostics()["timeout_reason"], "downstream_drain")

    def test_diagnostics_never_include_body_headers_path_or_exception_text(self):
        stream = self.opened()
        self.response.read1.side_effect = OSError("private upstream detail")
        with self.assertRaises(OSError): stream.readline(100)
        text = json.dumps(stream.diagnostics())
        for secret in ("synthetic body", "synthetic-canary", "/fixture", "private upstream detail"):
            self.assertNotIn(secret, text)
        self.assertEqual(stream.diagnostics()["error_code"], "upstream_read_error")

    def test_close_is_idempotent_and_cannot_turn_a_partial_stream_into_usage(self):
        stream = self.opened()
        self.response.read1.return_value = b"data: partial\n"
        stream.readline(100)
        stream.close(reason="owner_shutdown")
        stream.close()
        self.assertEqual(stream.diagnostics()["close_reason"], "owner_shutdown")
        self.assertIsNone(stream.diagnostics()["terminal_event"])
        self.connection.close.assert_called_once()
        self.assertTrue(self.timers[-1].cancelled)

    def test_partial_lines_are_timed_before_a_complete_event_is_available(self):
        stream = self.opened()
        def fragment(_):
            self.now += 1
            return next(parts)
        parts = iter((b"data:", b" fixture\n\n"))
        self.response.read1.side_effect = fragment
        self.assertEqual(stream.readline(100), b"data: fixture\n")
        self.now += 5
        self.assertEqual(stream.readline(100), b"\n")
        data = stream.diagnostics()
        self.assertEqual(data["first_byte_seconds"], 1)
        self.assertEqual(data["last_byte_seconds"], 2)
        self.assertEqual(data["bytes_received"], len(b"data: fixture\n\n"))
        self.assertEqual(data["read_calls"], 2)
        self.assertEqual(data["silence_seconds"], 5)

    def test_first_read_error_survives_later_shutdown_deadline(self):
        stream = self.opened()
        self.response.read1.side_effect = OSError()
        with self.assertRaises(OSError): stream.readline(100)
        stream.set_deadline(200, reason="shutdown_drain")
        self.now = 200
        self.timers[-1].fire()
        data = stream.diagnostics()
        self.assertEqual(data["first_error_code"], "upstream_read_error")
        self.assertIsNone(data["first_timeout_reason"])
        self.assertEqual(data["timeout_reason"], "shutdown_drain")

    def test_buffered_lines_cannot_bypass_an_expired_deadline(self):
        stream = self.opened()
        self.response.read1.return_value = b"first\nsecond\n"
        self.assertEqual(stream.readline(100), b"first\n")
        self.now = 1000
        with self.assertRaises(TimeoutError): stream.readline(100)

    def test_line_limit_keeps_bytes_without_counting_buffered_data_twice(self):
        stream = self.opened()
        self.response.read1.side_effect = [b"ab", b"c\n", b""]
        self.assertEqual(stream.readline(2), b"ab")
        self.assertEqual(stream.readline(2), b"c\n")
        self.assertEqual(stream.readline(2), b"")
        self.assertEqual(stream.diagnostics()["bytes_received"], 4)
        self.assertIsNotNone(stream.diagnostics()["eof_seconds"])

    def test_request_send_failure_is_distinct_from_unattempted_connect(self):
        self.connection.request.side_effect = OSError("private send detail")
        with self.assertRaises(OSError) as failure: self.opened()
        data = failure.exception.stream_diagnostics
        self.assertEqual(data["request_attempt_seconds"], 0)
        self.assertIsNone(data["request_sent_seconds"])
        self.assertEqual(data["error_code"], "request_send_error")
        self.connection.request.side_effect = None
        self.connection.connect.side_effect = OSError("private connect detail")
        with self.assertRaises(OSError) as failure: self.opened()
        self.assertIsNone(failure.exception.stream_diagnostics["request_attempt_seconds"])

    def test_body_read_tail_is_bounded_without_losing_totals(self):
        stream = self.opened()
        self.response.read1.return_value = b"x\n"
        for _ in range(100):
            self.now += 1
            self.assertEqual(stream.readline(100), b"x\n")
        data = stream.diagnostics()
        self.assertEqual(data["read_calls"], 100)
        self.assertEqual(data["bytes_received"], 200)
        self.assertEqual(data["bytes_returned"], 200)
        self.assertEqual(len(data["body_reads"]), 64)
        self.assertEqual(data["body_reads_dropped"], 36)
        self.assertEqual(data["body_reads"][0]["seq"], 37)
        self.assertEqual(data["body_reads"][-1]["seq"], 100)

    def test_truncated_chunk_keeps_partial_byte_timing_without_returning_it(self):
        stream = self.opened()
        self.now = 115
        self.response.read1.side_effect = http.client.IncompleteRead(b"partial", 10)
        with self.assertRaises(http.client.IncompleteRead): stream.readline(100)
        data = stream.diagnostics()
        self.assertEqual(data["bytes_received"], 7)
        self.assertEqual(data["bytes_returned"], 0)
        self.assertEqual(data["last_byte_seconds"], 15)
        self.assertTrue(data["body_reads"][-1]["partial"])

    def test_bytes_received_after_deadline_are_observed_but_never_returned(self):
        stream = self.opened()
        def delayed(_):
            self.now = 1001
            return b"late\n"
        self.response.read1.side_effect = delayed
        with self.assertRaises(TimeoutError): stream.readline(100)
        data = stream.diagnostics()
        self.assertEqual(data["bytes_received"], 5)
        self.assertEqual(data["bytes_returned"], 0)
        self.assertTrue(data["body_reads"][-1]["expired"])

    def test_terminal_event_is_observation_only_and_snapshot_is_detached(self):
        stream = self.opened()
        stream.record_terminal("message_stop")
        data = stream.diagnostics()
        data["deadline_revisions"]["absolute_deadline"] = -1
        self.assertEqual(stream.diagnostics()["deadline_revisions"]["absolute_deadline"], 900)
        self.assertEqual(stream.diagnostics()["terminal_event"], "message_stop")
        self.assertEqual(stream.diagnostics()["authority"], "diagnostic_only")
        for name in ("known_cost", "accounting_complete", "can_resume_agent"):
            self.assertNotIn(name, data)

    def test_invalid_or_nonfinite_deadline_is_rejected_without_new_timer(self):
        stream = self.opened()
        for value in (float("nan"), float("inf"), True):
            with self.subTest(value=value), self.assertRaises(ValueError): stream.set_deadline(value)
        self.assertEqual(len(self.timers), 1)
        with self.assertRaises(ValueError): stream.set_deadline(150, reason="arbitrary private detail")

    def test_header_failure_closes_socket_without_leaking_exception_text(self):
        self.connection.getresponse.side_effect = http.client.BadStatusLine("private headers")
        with self.assertRaises(http.client.BadStatusLine): self.opened()
        self.connection.close.assert_called_once()
        self.connection.sock.close.assert_called_once()
        self.assertTrue(self.timers[-1].cancelled)

    def test_cleanup_error_cannot_replace_the_original_header_failure(self):
        self.connection.getresponse.side_effect = http.client.BadStatusLine("private headers")
        self.connection.close.side_effect = OSError("private close detail")
        with self.assertRaises(http.client.BadStatusLine) as failure: self.opened()
        data = failure.exception.stream_diagnostics
        self.assertEqual(data["error_code"], "headers_error")
        self.assertEqual(data["cleanup_error"], "stream_close_error")
        self.connection.sock.close.assert_called_once()

    def test_cancellation_is_preserved_even_when_the_deadline_also_elapsed(self):
        def interrupted():
            self.now = 1001
            raise KeyboardInterrupt()
        self.connection.getresponse.side_effect = interrupted
        with self.assertRaises(KeyboardInterrupt): self.opened()
        self.connection.close.assert_called_once()
        self.connection.sock.close.assert_called_once()


class SocketTests(unittest.TestCase):
    def server(self, mode):
        release = threading.Event()
        class Handler(http.server.BaseHTTPRequestHandler):
            protocol_version = "HTTP/1.1"
            def log_message(self, *args): pass
            def do_POST(self):
                self.rfile.read(int(self.headers["Content-Length"]))
                try:
                    if mode == "headers":
                        self.wfile.write(b"HTTP/1.1 200 OK\r\nX-Partial:")
                        self.wfile.flush(); release.wait(2)
                        return
                    self.send_response(200)
                    self.send_header("Transfer-Encoding", "chunked")
                    self.end_headers()
                    payload = b"event: message_stop\ndata: {}\n\n" if mode == "terminal" else b"data: partial\n\n"
                    self.wfile.write(format(len(payload), "x").encode() + b"\r\n" + payload + b"\r\n")
                    self.wfile.flush()
                    if mode == "eof":
                        self.wfile.write(b"0\r\n\r\n"); self.wfile.flush()
                    elif mode == "drip":
                        for _ in range(100):
                            if release.wait(.01): break
                            self.wfile.write(b"1\r\nx\r\n"); self.wfile.flush()
                    else: release.wait(2)
                except OSError: pass
                finally: self.close_connection = True
        server = http.server.HTTPServer(("127.0.0.1", 0), Handler)
        thread = threading.Thread(target=lambda: server.serve_forever(poll_interval=.01), daemon=True)
        thread.start()
        def close():
            release.set(); server.shutdown(); server.server_close(); thread.join(2)
            self.assertFalse(thread.is_alive())
        self.addCleanup(close)
        connection = http.client.HTTPConnection("127.0.0.1", server.server_port, timeout=1)
        self.addCleanup(connection.close)
        return connection

    def opened(self, mode, timeout=1):
        stream = http_stream.open_http_stream(self.server(mode), "/fixture", b"{}", {}, time.monotonic()+timeout)
        self.addCleanup(stream.close)
        return stream

    def test_real_silent_stream_records_client_end_and_shortened_drain(self):
        stream = self.opened("body")
        self.assertEqual(stream.readline(100), b"data: partial\n")
        self.assertEqual(stream.readline(100), b"\n")
        stream.record_client_end("idle_timeout")
        stream.set_deadline(time.monotonic()+.05, reason="shutdown_drain")
        with self.assertRaises(TimeoutError): stream.readline(100)
        data = stream.diagnostics()
        self.assertEqual(data["timeout_reason"], "shutdown_drain")
        self.assertLessEqual(data["last_byte_seconds"], data["client_end"]["elapsed_seconds"])
        self.assertIsNone(data["terminal_event"])

    def test_real_active_partial_line_hits_absolute_deadline_without_idle_inference(self):
        stream = self.opened("drip", timeout=.15)
        self.assertEqual(stream.readline(100), b"data: partial\n")
        self.assertEqual(stream.readline(100), b"\n")
        with self.assertRaises(TimeoutError): stream.readline(100)
        data = stream.diagnostics()
        self.assertEqual(data["timeout_reason"], "absolute_deadline")
        self.assertGreater(data["bytes_received"], len(b"data: partial\n\n"))
        self.assertLess(data["silence_seconds"], .15)

    def test_real_incomplete_headers_keep_setup_diagnostic_on_exception(self):
        with self.assertRaises(TimeoutError) as failure: self.opened("headers", timeout=.05)
        data = failure.exception.stream_diagnostics
        self.assertIsNone(data["headers_seconds"])
        self.assertIsNone(data["first_byte_seconds"])
        self.assertEqual(data["timeout_reason"], "absolute_deadline")

    def test_real_eof_and_protocol_terminal_are_different_observations(self):
        stream = self.opened("eof")
        self.assertEqual(stream.readline(100), b"data: partial\n")
        self.assertEqual(stream.readline(100), b"\n")
        self.assertEqual(stream.readline(100), b"")
        self.assertIsNone(stream.diagnostics()["terminal_event"])
        stream = self.opened("terminal")
        self.assertEqual(stream.readline(100), b"event: message_stop\n")
        self.assertEqual(stream.readline(100), b"data: {}\n")
        self.assertEqual(stream.readline(100), b"\n")
        stream.record_terminal("message_stop")
        stream.close()
        self.assertIsNone(stream.diagnostics()["eof_seconds"])
        self.assertEqual(stream.diagnostics()["terminal_event"], "message_stop")


if __name__ == "__main__": unittest.main()
