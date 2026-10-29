"""Owned stdio-relay proof, not a production provider or spending entrypoint.

Only the literal synthetic credential is supported. The host chooses one
loopback endpoint; frame contents cannot select a URL, header or executable.
HTTP completion attests bytes only, never provider usage or resume authority.
"""

import base64
import hashlib
import http.client
import http.server
import ipaddress
import json
import os
import select
import socket
import struct
import sys
import threading
import time
import uuid

MAX_FRAME = 8 << 20
MAX_REQUEST = 4 << 20
MAX_RESPONSE = 8 << 20
CHUNK = 32768


class PipeFailure(Exception): pass


def require(value, code):
    if not value: raise PipeFailure(code)


def unique_object(pairs):
    result = {}
    for key, value in pairs:
        require(key not in result, "frame_duplicate_key")
        result[key] = value
    return result


def reject_constant(value): raise PipeFailure("frame_nonfinite")


class Frames:
    protocol = "yhc-offline-host-pipe-v1"

    def __init__(self, read_fd, write_fd, nonce, timeout=40):
        try: valid = isinstance(nonce, str) and str(uuid.UUID(nonce)) == nonce and uuid.UUID(nonce).int != 0
        except ValueError: valid = False
        require(os.name == "posix" and valid and type(timeout) in (int, float) and 0 < timeout <= 60, "pipe_identity_or_timeout")
        self.read_fd, self.write_fd, self.nonce = read_fd, write_fd, nonce
        self.deadline = time.monotonic() + timeout
        os.set_blocking(read_fd, False); os.set_blocking(write_fd, False)

    def remaining(self):
        result = self.deadline - time.monotonic()
        require(result > 0, "transport_deadline")
        return result

    def transfer(self, size=None, payload=None):
        reading = payload is None
        fd = self.read_fd if reading else self.write_fd
        result, offset = bytearray(), 0
        wanted = size if reading else len(payload)
        while offset < wanted:
            ready = select.select([fd] if reading else [], [] if reading else [fd], [], self.remaining())
            require(bool(ready[0] if reading else ready[1]), "transport_deadline")
            try:
                if reading:
                    block = os.read(fd, min(wanted - offset, 65536))
                    require(bool(block), "pipe_closed")
                    result.extend(block); offset += len(block)
                else: offset += os.write(fd, payload[offset:offset + 65536])
            except BlockingIOError: continue
            except OSError: raise PipeFailure("pipe_closed") from None
        return bytes(result)

    def send(self, kind, **body):
        raw = json.dumps(dict(protocol=self.protocol, nonce=self.nonce, kind=kind, **body),
                         separators=(",", ":"), allow_nan=False).encode()
        require(0 < len(raw) <= MAX_FRAME, "frame_size")
        self.transfer(payload=struct.pack("!I", len(raw)) + raw)

    def receive(self):
        length = struct.unpack("!I", self.transfer(size=4))[0]
        require(0 < length <= MAX_FRAME, "frame_size")
        try: value = json.loads(self.transfer(size=length), object_pairs_hook=unique_object, parse_constant=reject_constant)
        except (ValueError, UnicodeError): raise PipeFailure("frame_json") from None
        require(isinstance(value, dict) and value.get("protocol") == self.protocol
                and value.get("nonce") == self.nonce and isinstance(value.get("kind"), str), "frame_identity")
        value.pop("protocol"); value.pop("nonce")
        return value


def blob(value, maximum):
    require(isinstance(value, str) and len(value) <= ((maximum + 2) // 3) * 4, "blob_size")
    try: raw = base64.b64decode(value, validate=True)
    except (ValueError, UnicodeError): raise PipeFailure("blob_encoding") from None
    require(len(raw) <= maximum, "blob_size")
    return raw


def close_socket(connection):
    try: connection.shutdown(socket.SHUT_RDWR)
    except OSError: pass


class LiteralConnection(http.client.HTTPConnection):
    """Use a numeric socket directly; no DNS, proxy, redirect or retry owner."""
    def connect(self):
        family = socket.AF_INET6 if ipaddress.ip_address(self.host).version == 6 else socket.AF_INET
        self.sock = socket.socket(family, socket.SOCK_STREAM)
        self.sock.settimeout(self.timeout)
        self.sock.connect((self.host, self.port))


def forward(channel, host, port, container_port, calls):
    try:
        address = ipaddress.ip_address(host)
        literal = isinstance(host, str) and "%" not in host and address.is_loopback and getattr(address, "ipv4_mapped", None) is None
    except ValueError: literal = False
    require(literal and type(port) is int and 0 < port <= 65535
            and type(container_port) is int and 0 < container_port <= 65535
            and type(calls) is int and 0 < calls <= 20, "host_endpoint_or_limit")
    ready = channel.receive()
    require(ready == dict(kind="ready", port=container_port), "relay_not_ready")
    count, total = 0, 0
    while True:
        message = channel.receive()
        if message["kind"] == "finished":
            require(message == dict(kind="finished", requests=count, can_resume_agent=False, new_spending_authorized=False)
                    and type(message["requests"]) is int and message["can_resume_agent"] is False
                    and message["new_spending_authorized"] is False and count == calls, "relay_finished_unverified")
            return dict(transport_complete=True, requests=count, response_bytes=total,
                        can_resume_agent=False, new_spending_authorized=False, provider_usage_verified=False)
        require(set(message) == {"kind", "seq", "body", "sha256"} and message["kind"] == "request"
                and type(message["seq"]) is int and message["seq"] == count + 1 and count < calls, "request_sequence")
        raw = blob(message["body"], MAX_REQUEST)
        require(bool(raw) and message["sha256"] == hashlib.sha256(raw).hexdigest(), "request_integrity")
        count += 1
        connection = LiteralConnection(host, port, timeout=channel.remaining())
        timer, response = None, None
        try:
            connection.connect()
            active_socket = connection.sock
            # Socket inactivity timeouts alone do not bound slow-drip headers.
            timer = threading.Timer(channel.remaining(), close_socket, args=(active_socket,))
            timer.daemon = True; timer.start()
            connection.request("POST", "/responses", raw,
                               headers={"Authorization": "Bearer fixture", "Content-Type": "application/json"})
            response = connection.getresponse()
            content_type = response.getheader("Content-Type", "application/octet-stream")
            require(len(content_type) <= 128 and content_type.isascii()
                    and not any(c in content_type for c in "\r\n\0"), "upstream_headers")
            channel.send("headers", seq=count, status=response.status, content_type=content_type)
            checksum, length = hashlib.sha256(), 0
            while True:
                channel.remaining()
                chunk = response.read1(CHUNK)
                if not chunk:
                    require(response.length in (None, 0), "upstream_incomplete")
                    break
                total += len(chunk); length += len(chunk)
                require(total <= MAX_RESPONSE, "response_size")
                checksum.update(chunk)
                channel.send("data", seq=count, data=base64.b64encode(chunk).decode())
            channel.send("end", seq=count, size=length, sha256=checksum.hexdigest())
        except (OSError, http.client.HTTPException, PipeFailure):
            try: channel.send("failure", seq=count, reason="upstream_or_pipe_failed")
            except PipeFailure: pass
            raise PipeFailure("upstream_or_pipe_failed") from None
        finally:
            if timer is not None: timer.cancel(); timer.join(1)
            if response is not None: response.close()
            connection.close()


def run_container(channel):
    start = channel.receive()
    require(set(start) == {"kind", "port", "requests"} and start["kind"] == "start"
            and type(start["port"]) is int and 0 < start["port"] <= 65535
            and type(start["requests"]) is int and 0 < start["requests"] <= 20, "relay_start")
    state = dict(requests=0, failed=False)

    class Handler(http.server.BaseHTTPRequestHandler):
        protocol_version = "HTTP/1.1"
        def log_message(self, *args): pass
        def setup(self):
            super().setup()
            self.connection.settimeout(channel.remaining())
            self.watchdog = threading.Timer(channel.remaining(), close_socket, args=(self.connection,))
            self.watchdog.daemon = True; self.watchdog.start()
        def finish(self):
            try: super().finish()
            finally: self.watchdog.cancel(); self.watchdog.join(1)
        def do_POST(self):
            self.close_connection = True
            try:
                lengths = self.headers.get_all("Content-Length") or []
                require(self.path == "/responses" and self.headers.get_all("Authorization") == ["Bearer fixture"]
                        and not self.headers.get("Transfer-Encoding") and not self.headers.get("Expect")
                        and len(lengths) == 1 and lengths[0].isascii() and lengths[0].isdigit()
                        and 0 < int(lengths[0]) <= MAX_REQUEST, "local_request_rejected")
                raw = self.rfile.read(int(lengths[0]))
                require(len(raw) == int(lengths[0]), "local_request_incomplete")
                state["requests"] += 1
                seq = state["requests"]
                channel.send("request", seq=seq, body=base64.b64encode(raw).decode(), sha256=hashlib.sha256(raw).hexdigest())
                header = channel.receive()
                require(set(header) == {"kind", "seq", "status", "content_type"} and header["kind"] == "headers"
                        and type(header["seq"]) is int and header["seq"] == seq
                        and type(header["status"]) is int and 200 <= header["status"] <= 599
                        and isinstance(header["content_type"], str) and len(header["content_type"]) <= 128
                        and header["content_type"].isascii() and not any(c in header["content_type"] for c in "\r\n\0"), "relay_headers")
                self.send_response(header["status"])
                self.send_header("Content-Type", header["content_type"])
                self.send_header("Transfer-Encoding", "chunked")
                self.send_header("Connection", "close"); self.end_headers()
                checksum, length = hashlib.sha256(), 0
                while True:
                    message = channel.receive()
                    require(type(message.get("seq")) is int and message["seq"] == seq, "response_sequence")
                    if message["kind"] == "end":
                        require(set(message) == {"kind", "seq", "size", "sha256"}
                                and type(message["size"]) is int and message["size"] == length
                                and message["sha256"] == checksum.hexdigest(), "response_integrity")
                        self.wfile.write(b"0\r\n\r\n"); self.wfile.flush()
                        break
                    require(set(message) == {"kind", "seq", "data"} and message["kind"] == "data", "response_incomplete")
                    raw = blob(message["data"], CHUNK)
                    require(bool(raw), "empty_response_chunk")
                    length += len(raw); require(length <= MAX_RESPONSE, "response_size")
                    checksum.update(raw)
                    self.wfile.write(format(len(raw), "x").encode() + b"\r\n" + raw + b"\r\n"); self.wfile.flush()
            except (OSError, PipeFailure, ValueError):
                state["failed"] = True
                close_socket(self.connection)

    class Server(http.server.HTTPServer):
        def handle_error(self, *args): state["failed"] = True

    server = Server(("127.0.0.1", start["port"]), Handler)
    try:
        channel.send("ready", port=server.server_port)
        while state["requests"] < start["requests"] and not state["failed"]:
            server.timeout = min(0.5, channel.remaining()); server.handle_request()
        require(not state["failed"], "relay_request_failed")
        channel.send("finished", requests=state["requests"], can_resume_agent=False, new_spending_authorized=False)
    finally: server.server_close()


if __name__ == "__main__":
    try:
        require(len(sys.argv) == 2, "relay_arguments")
        run_container(Frames(sys.stdin.fileno(), sys.stdout.fileno(), sys.argv[1]))
    except (OSError, PipeFailure, ValueError): sys.exit(1)
