"""Offline byte-channel contracts; synthetic credentials and owned servers only."""

import base64
import hashlib
import http.server
import json
import os
import socket
import struct
import threading
import time
import unittest
import uuid
from unittest import mock

from scripts.terminal_bench.testdata import host_gateway_pipe as pipe_fixture
from scripts.terminal_bench.testdata.host_gateway_pipe import Frames, PipeFailure, forward


@unittest.skipUnless(os.name == "posix", "proof transport requires POSIX descriptors")
class FrameTests(unittest.TestCase):
    def pair(self, timeout=1):
        a, b = socket.socketpair()
        self.addCleanup(a.close); self.addCleanup(b.close)
        nonce = str(uuid.uuid4())
        return a, b, Frames(a.fileno(), a.fileno(), nonce, timeout), Frames(b.fileno(), b.fileno(), nonce, timeout)

    def test_frame_round_trip_keeps_opaque_bytes_and_exact_nonce(self):
        _, _, sender, receiver = self.pair()
        payload = base64.b64encode(b"\0opaque\xffbytes").decode()
        sender.send("data", seq=1, data=payload)
        self.assertEqual(receiver.receive(), dict(kind="data", seq=1, data=payload))

    def test_rejects_duplicate_json_keys_wrong_nonce_and_oversized_prefix(self):
        for fault in ("duplicate", "nonce", "oversize", "nonfinite"):
            a, _, sender, receiver = self.pair()
            if fault == "oversize": raw = struct.pack("!I", (8 << 20) + 1)
            else:
                value = dict(protocol=sender.protocol, nonce=str(uuid.uuid4()) if fault == "nonce" else sender.nonce, kind="data")
                body = json.dumps(value).encode()
                if fault == "duplicate": body = body[:-1] + b',"kind":"other"}'
                if fault == "nonfinite": body = body[:-1] + b',"seq":NaN}'
                raw = struct.pack("!I", len(body)) + body
            a.sendall(raw)
            with self.subTest(fault=fault), self.assertRaises(PipeFailure): receiver.receive()

    def test_absolute_deadline_and_eof_never_become_a_success_frame(self):
        a, _, _, receiver = self.pair(timeout=0.05)
        started = time.monotonic()
        with self.assertRaisesRegex(PipeFailure, "deadline"): receiver.receive()
        self.assertLess(time.monotonic() - started, 1)
        a.close()
        with self.assertRaises(PipeFailure): receiver.receive()


@unittest.skipUnless(os.name == "posix", "proof transport requires POSIX descriptors")
class ForwardTests(unittest.TestCase):
    def owned_server(self, reset=False, redirect=False, stalled_headers=False, dripping_body=False):
        requests = []
        release = threading.Event()
        class Handler(http.server.BaseHTTPRequestHandler):
            protocol_version = "HTTP/1.1"
            def log_message(self, *args): pass
            def do_POST(self):
                raw = self.rfile.read(int(self.headers["Content-Length"]))
                requests.append((self.path, self.headers.get("Authorization"), raw))
                if stalled_headers:
                    self.wfile.write(b"HTTP/1.1 200 OK\r\nX-Partial:"); self.wfile.flush()
                    release.wait(2); self.close_connection = True
                    return
                self.send_response(307 if redirect else 200)
                self.send_header("Content-Type", "text/event-stream")
                self.send_header("Transfer-Encoding", "chunked")
                if redirect: self.send_header("Location", "/forbidden")
                self.end_headers()
                body = b"data: opaque-prefix\n\n"
                self.wfile.write(format(len(body), "x").encode() + b"\r\n" + body + b"\r\n"); self.wfile.flush()
                if dripping_body:
                    try:
                        for _ in range(100):
                            if release.wait(0.02): break
                            self.wfile.write(b"1\r\nx\r\n"); self.wfile.flush()
                    except OSError: pass
                    self.close_connection = True
                    return
                if reset:
                    self.connection.shutdown(socket.SHUT_RDWR); self.close_connection = True
                else: self.wfile.write(b"0\r\n\r\n"); self.wfile.flush(); self.close_connection = True
        server = http.server.HTTPServer(("127.0.0.1", 0), Handler)
        thread = threading.Thread(target=server.serve_forever, daemon=True)
        thread.start()
        def close():
            release.set()
            server.shutdown(); server.server_close(); thread.join(2)
            self.assertFalse(thread.is_alive())
        self.addCleanup(close)
        self.release_server = release
        return server.server_port, requests

    def exchange(self, port, *, replay=False, timeout=2, drop_pipe=False):
        a, b = socket.socketpair()
        self.addCleanup(a.close); self.addCleanup(b.close)
        nonce = str(uuid.uuid4())
        host, child = Frames(a.fileno(), a.fileno(), nonce, timeout), Frames(b.fileno(), b.fileno(), nonce, timeout)
        observed = []
        self.observed = observed
        body = b'{"input":"owned opaque input"}'
        request = dict(seq=1, body=base64.b64encode(body).decode(), sha256=hashlib.sha256(body).hexdigest())
        def run():
            try:
                child.send("ready", port=12345)
                child.send("request", **request)
                while True:
                    message = child.receive(); observed.append(message)
                    if drop_pipe and message["kind"] == "data":
                        b.shutdown(socket.SHUT_RDWR)
                        break
                    if message["kind"] in ("end", "failure"): break
                if observed[-1]["kind"] == "end":
                    if replay: child.send("request", **request)
                    else: child.send("finished", requests=1, can_resume_agent=False, new_spending_authorized=False)
            except PipeFailure: pass
        thread = threading.Thread(target=run, daemon=True); thread.start()
        try:
            with mock.patch("socket.getaddrinfo", side_effect=AssertionError("DNS must not be used")):
                result = forward(host, "127.0.0.1", port, 12345, 1 if not replay else 2)
            return result, observed, body
        finally:
            try: a.shutdown(socket.SHUT_RDWR)
            except OSError: pass
            thread.join(3)
            self.assertFalse(thread.is_alive())

    def test_streams_bytes_without_dns_or_usage_authority(self):
        port, requests = self.owned_server()
        result, observed, body = self.exchange(port)
        self.assertEqual(requests, [("/responses", "Bearer fixture", body)])
        raw = b"".join(base64.b64decode(row["data"]) for row in observed if row["kind"] == "data")
        self.assertEqual(raw, b"data: opaque-prefix\n\n")
        self.assertEqual(observed[-1]["sha256"], hashlib.sha256(raw).hexdigest())
        self.assertTrue(result["transport_complete"])
        self.assertFalse(result["can_resume_agent"] or result["new_spending_authorized"] or result["provider_usage_verified"])

    def test_upstream_truncation_forwards_prefix_but_never_an_end_or_retry(self):
        port, requests = self.owned_server(reset=True)
        with self.assertRaisesRegex(PipeFailure, "upstream"):
            self.exchange(port)
        self.assertEqual(len(requests), 1)
        self.assertTrue(any(row["kind"] == "data" for row in self.observed))
        self.assertFalse(any(row["kind"] == "end" for row in self.observed))

    def test_replayed_request_cannot_reach_upstream_twice(self):
        port, requests = self.owned_server()
        with self.assertRaisesRegex(PipeFailure, "sequence"):
            self.exchange(port, replay=True)
        self.assertEqual(len(requests), 1)

    def test_pipe_loss_mid_response_cannot_retry_or_manufacture_completion(self):
        port, requests = self.owned_server()
        with self.assertRaises(PipeFailure): self.exchange(port, drop_pipe=True)
        self.assertEqual(len(requests), 1)
        self.assertFalse(any(row["kind"] == "end" for row in self.observed))

    def test_absolute_deadline_closes_a_response_with_unfinished_headers(self):
        port, requests = self.owned_server(stalled_headers=True)
        started = time.monotonic()
        try:
            with self.assertRaises(PipeFailure): self.exchange(port, timeout=0.1)
            self.assertLess(time.monotonic() - started, 1)
            self.assertEqual(len(requests), 1)
        finally: self.release_server.set()

    def test_response_byte_exhaustion_does_not_emit_an_end(self):
        port, requests = self.owned_server()
        with mock.patch.object(pipe_fixture, "MAX_RESPONSE", 8), self.assertRaises(PipeFailure): self.exchange(port)
        self.assertEqual(len(requests), 1)
        self.assertFalse(any(row["kind"] == "end" for row in self.observed))

    def test_continuous_body_progress_cannot_extend_the_absolute_deadline(self):
        port, requests = self.owned_server(dripping_body=True)
        started = time.monotonic()
        try:
            with self.assertRaises(PipeFailure): self.exchange(port, timeout=0.15)
            self.assertLess(time.monotonic() - started, 1)
            self.assertEqual(len(requests), 1)
            self.assertTrue(any(row["kind"] == "data" for row in self.observed))
        finally: self.release_server.set()

    def test_invalid_request_integrity_or_sequence_cannot_open_an_upstream_socket(self):
        for fault in ("sequence", "body", "hash", "bool"):
            raw = b"owned"
            request = dict(kind="request", seq=1, body=base64.b64encode(raw).decode(), sha256=hashlib.sha256(raw).hexdigest())
            if fault == "sequence": request["seq"] = 2
            if fault == "body": request["body"] = "!"
            if fault == "hash": request["sha256"] = "a" * 64
            if fault == "bool": request["seq"] = True
            channel = mock.Mock()
            channel.receive.side_effect = [dict(kind="ready", port=12345), request]
            with self.subTest(fault=fault), mock.patch.object(pipe_fixture.LiteralConnection, "connect") as connect:
                with self.assertRaises(PipeFailure): forward(channel, "127.0.0.1", 12345, 12345, 1)
                connect.assert_not_called()

    def test_redirect_is_returned_without_following_it(self):
        port, requests = self.owned_server(redirect=True)
        _, observed, _ = self.exchange(port)
        self.assertEqual(observed[0]["status"], 307)
        self.assertEqual(len(requests), 1)

    def test_nonliteral_or_nonloopback_endpoint_fails_before_reading_pipe(self):
        for host in ("localhost", "example.invalid", "0.0.0.0", "192.0.2.1", "127.1", "::1%lo0", "::ffff:127.0.0.1", "::ffff:7f00:1"):
            channel = mock.Mock()
            with self.subTest(host=host), self.assertRaises(PipeFailure): forward(channel, host, 12345, 12345, 1)
            channel.receive.assert_not_called()


if __name__ == "__main__": unittest.main()
