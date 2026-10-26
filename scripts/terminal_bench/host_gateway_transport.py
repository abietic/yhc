"""Bounded cold-dispatch transport to an explicitly bound host-loopback gateway.

Only the existing opaque gateway token crosses the pipe. This module neither
loads provider keys nor authorizes, retries or settles spending. The isolated
container runs only the existing immutable workspace/CLI operation.
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
    maximum_timeout = 60
    protocol = "yhc-offline-host-pipe-v1"

    def __init__(self, read_fd, write_fd, nonce, timeout=40):
        try: valid = isinstance(nonce, str) and str(uuid.UUID(nonce)) == nonce and uuid.UUID(nonce).int != 0
        except ValueError: valid = False
        require(os.name == "posix" and valid and type(timeout) in (int, float) and 0 < timeout <= self.maximum_timeout, "pipe_identity_or_timeout")
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


class DispatchFrames(Frames):
    protocol = "yhc-host-gateway-transport-v1"
    maximum_timeout = 915


def canonical(value):
    return json.dumps(value, sort_keys=True, separators=(",", ":"), ensure_ascii=False, allow_nan=False).encode()


def helper_digest():
    from pathlib import Path
    return hashlib.sha256(Path(__file__).read_bytes()).hexdigest()


def endpoint(value):
    from urllib.parse import urlsplit
    try:
        url = urlsplit(value)
        address = ipaddress.ip_address(url.hostname)
        require(url.scheme == "http" and address.is_loopback and getattr(address, "ipv4_mapped", None) is None
                and "%" not in url.hostname and not url.username and not url.password
                and url.path in ("", "/") and not url.query and not url.fragment
                and type(url.port) is int and 0 < url.port <= 65535, "gateway_endpoint_invalid")
        return str(address), url.port
    except (TypeError, ValueError):
        raise PipeFailure("gateway_endpoint_invalid") from None


def request_body(raw, grant):
    try: body = json.loads(raw, object_pairs_hook=unique_object, parse_constant=reject_constant)
    except (ValueError, UnicodeError): raise PipeFailure("gateway_request_json") from None
    limit = grant["limits"]["max_output_tokens_per_call"]
    require(isinstance(body, dict) and body.get("model") == grant["route"]["model"]
            and body.get("stream") is True and type(body.get("max_output_tokens")) is int
            and 0 < body["max_output_tokens"] <= limit, "gateway_request_route_or_limit")
    return body


def probe(route):
    host, port = endpoint(route["base_url"])
    family = socket.AF_INET6 if ipaddress.ip_address(host).version == 6 else socket.AF_INET
    with socket.socket(family, socket.SOCK_STREAM) as listener:
        listener.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
        listener.bind((host, port))
    return dict(helper_sha256=helper_digest(), port_available=True)


def forward_response(channel, message, host, port, token, seq, allowance, deadline):
    raw = blob(message["body"], MAX_REQUEST)
    require(bool(raw) and message["sha256"] == hashlib.sha256(raw).hexdigest(), "request_integrity")
    request_body(raw, allowance["grant"])
    def remaining():
        value = min(channel.remaining(), deadline - time.monotonic())
        require(value > 0, "gateway_call_deadline")
        return value
    connection = LiteralConnection(host, port, timeout=remaining())
    timer, response = None, None
    try:
        connection.connect()
        timer = threading.Timer(remaining(), close_socket, args=(connection.sock,))
        timer.daemon = True; timer.start()
        connection.request("POST", "/responses", raw,
                           headers={"Authorization": "Bearer " + token, "Content-Type": "application/json"})
        response = connection.getresponse()
        content_type = response.getheader("Content-Type", "application/octet-stream")
        require(len(content_type) <= 128 and content_type.isascii()
                and not any(c in content_type for c in "\r\n\0"), "gateway_headers")
        channel.send("headers", seq=seq, status=response.status, content_type=content_type)
        checksum, length = hashlib.sha256(), 0
        while True:
            remaining()
            chunk = response.read1(CHUNK)
            if not chunk:
                require(response.length in (None, 0), "gateway_incomplete")
                break
            length += len(chunk); allowance["bytes"] += len(chunk)
            require(allowance["bytes"] <= MAX_RESPONSE, "gateway_response_size")
            checksum.update(chunk)
            channel.send("data", seq=seq, data=base64.b64encode(chunk).decode())
        channel.send("end", seq=seq, size=length, sha256=checksum.hexdigest())
        return 200 <= response.status < 300
    except (OSError, http.client.HTTPException, PipeFailure):
        try: channel.send("failure", seq=seq, reason="gateway_or_pipe_failed")
        except PipeFailure: raise PipeFailure("gateway_or_pipe_failed") from None
        return False
    finally:
        if timer is not None: timer.cancel(); timer.join(1)
        if response is not None: response.close()
        connection.close()


class HostGatewayTransport:
    """Explicit host-loopback gateway dependency, never a funding authority."""

    def __init__(self, config, anchor):
        from types import MappingProxyType
        fields = {"version", "scope", "grant_sha256", "workspace_container_id", "upstream_base_url", "expires_unix", "gateway_token_sha256"}
        require(isinstance(config, dict) and set(config) == fields
                and type(config["version"]) is int and config["version"] == 1
                and config["scope"] == "benchmark-host-gateway-transport-v1"
                and hashlib.sha256(canonical(config)).hexdigest() == anchor
                and type(config["expires_unix"]) is int, "gateway_binding_invalid")
        for name in ("grant_sha256", "workspace_container_id", "gateway_token_sha256"):
            value = config[name]
            require(isinstance(value, str) and len(value) == 64
                    and all(c in "0123456789abcdef" for c in value), "gateway_binding_invalid")
        endpoint(config["upstream_base_url"])
        self._config = MappingProxyType(dict(config))

    @property
    def endpoint(self):
        return endpoint(self._config["upstream_base_url"])

    def validate(self, workspace, grant, anchor, now):
        require(self._config["grant_sha256"] == anchor == hashlib.sha256(canonical(grant)).hexdigest()
                and self._config["workspace_container_id"] == workspace.container, "gateway_binding_mismatch")
        limits = grant["limits"]
        require(type(limits["timeout_sec"]) is int and 0 < limits["timeout_sec"] <= 900
                and type(limits["max_provider_calls"]) is int and 0 < limits["max_provider_calls"] <= 200
                and type(limits["max_output_tokens_per_call"]) is int and 0 < limits["max_output_tokens_per_call"] <= 65536
                and self._config["expires_unix"] >= now + limits["timeout_sec"]
                and grant["expires_unix"] >= now + limits["timeout_sec"]
                and grant["route"]["provider"] == "deepseek", "gateway_binding_expired_or_route_invalid")
        endpoint(grant["route"]["base_url"])

    def validate_token(self, token):
        require(isinstance(token, str) and 0 < len(token) <= 512 and token.isascii()
                and not any(c.isspace() for c in token)
                and hashlib.sha256(token.encode()).hexdigest() == self._config["gateway_token_sha256"], "gateway_token_invalid")

    def preflight(self, workspace, grant, anchor, *, now, gateway_token, expected_image):
        self.validate_token(gateway_token)
        self.validate(workspace, grant, anchor, now)
        inventory = workspace.inventory()
        require(inventory["container_id"] == self._config["workspace_container_id"]
                and inventory["image"] == expected_image, "gateway_workspace_unverified")
        observation = workspace.rpc("host_gateway_probe", route=grant["route"])
        require(observation == dict(helper_sha256=helper_digest(), port_available=True), "gateway_helper_unverified")
        connection = LiteralConnection(*self.endpoint, timeout=2)
        try: connection.connect()
        except OSError: raise PipeFailure("gateway_not_ready") from None
        finally: connection.close()

    def exchange(self, channel, request, token):
        self.validate_token(token)
        grant = request["launch"]["grant"]
        call_deadline = min(channel.deadline, time.monotonic() + min(grant["limits"]["timeout_sec"],
            self._config["expires_unix"] - time.time(), grant["expires_unix"] - time.time()))
        channel.send("dispatch", request=request)
        ready = channel.receive()
        host, port = endpoint(grant["route"]["base_url"])
        require(ready == dict(kind="ready", host=host, port=port), "gateway_relay_not_ready")
        channel.send("start")
        count, failed, allowance = 0, False, dict(bytes=0, grant=grant)
        while True:
            message = channel.receive()
            if message["kind"] == "execution":
                require(set(message) == {"kind", "body"} and isinstance(message["body"], dict), "gateway_execution_invalid")
                result = message["body"]
                require(set(result) == {"exit_code", "terminal", "checkpoint"} and type(result["exit_code"]) is int
                        and isinstance(result["checkpoint"], dict), "gateway_execution_invalid")
                usage = result["checkpoint"].get("snapshot")
                require(isinstance(usage, dict) and type(usage.get("provider_calls")) is int
                        and usage["provider_calls"] == count, "gateway_call_accounting_mismatch")
                require(not failed or (result["checkpoint"].get("accounting_complete") is False
                        and type(usage.get("unknown_calls")) is int and usage["unknown_calls"] > 0),
                        "gateway_fault_usage_unverified")
                return result
            require(set(message) == {"kind", "seq", "body", "sha256"} and message["kind"] == "request"
                    and type(message["seq"]) is int and message["seq"] == count + 1
                    and count < grant["limits"]["max_provider_calls"] and not failed, "gateway_request_sequence_or_stopped")
            count += 1
            require(time.monotonic() < call_deadline and time.time() < min(self._config["expires_unix"], grant["expires_unix"]),
                    "gateway_call_window_expired")
            failed = not forward_response(channel, message, *self.endpoint, token, count, allowance, call_deadline) or failed

    def rpc(self, workspace, launch, token):
        import signal
        import subprocess
        self.validate(workspace, launch["grant"], hashlib.sha256(canonical(launch["grant"])).hexdigest(), time.time())
        self.validate_token(token)
        nonce = str(uuid.uuid4())
        request = dict(protocol="yhc-workspace-files-v1", request_id=str(uuid.uuid4()), root=workspace.cwd,
                       operation="cold_dispatch", launch=launch, gateway_token=token)
        env = {k: os.environ[k] for k in ("PATH", "HOME", "LANG", "TMPDIR", "DOCKER_CONFIG") if k in os.environ}
        process = subprocess.Popen(["docker", "--context", workspace.context, "exec", "--interactive", "--",
            workspace.container, "/usr/local/bin/python3", "-I", "/opt/checkpoint/workspace_io.py",
            "--host-gateway-pipe", nonce], stdin=subprocess.PIPE, stdout=subprocess.PIPE,
            stderr=subprocess.DEVNULL, start_new_session=True, env=env)
        success = False
        try:
            channel = DispatchFrames(process.stdout.fileno(), process.stdin.fileno(), nonce, launch["grant"]["limits"]["timeout_sec"] + 15)
            result = self.exchange(channel, request, token)
            process.stdin.close()
            require(process.wait(timeout=min(5, channel.remaining())) == 0, "gateway_helper_exit")
            success = True
            return result
        finally:
            try:
                if not success:
                    try: os.killpg(process.pid, signal.SIGKILL)
                    except ProcessLookupError: pass
                if process.poll() is None: process.wait(timeout=5)
            finally:
                try: process.stdin.close()
                finally: process.stdout.close()


def run_container(channel, execute):
    """Serve a bounded loopback hop during the existing immutable CLI operation."""
    message = channel.receive()
    require(set(message) == {"kind", "request"} and message["kind"] == "dispatch", "gateway_dispatch_frame")
    request = message["request"]
    require(isinstance(request, dict) and set(request) == {"protocol", "request_id", "root", "operation", "launch", "gateway_token"}
            and request["protocol"] == "yhc-workspace-files-v1" and request["operation"] == "cold_dispatch", "gateway_dispatch_request")
    grant, token = request["launch"]["grant"], request["gateway_token"]
    timeout = grant["limits"]["timeout_sec"]
    require(type(timeout) is int and 0 < timeout <= 900, "gateway_dispatch_timeout")
    channel.deadline = min(channel.deadline, time.monotonic() + timeout + 15)
    host, port = endpoint(grant["route"]["base_url"])
    require(isinstance(token, str) and 0 < len(token) <= 512 and token.isascii()
            and not any(c.isspace() for c in token), "gateway_token_invalid")
    state = dict(requests=0, failed=False)
    stopped = threading.Event()

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
                require(self.path == "/responses" and self.headers.get_all("Authorization") == ["Bearer " + token]
                        and not self.headers.get("Transfer-Encoding") and not self.headers.get("Expect")
                        and len(lengths) == 1 and lengths[0].isascii() and lengths[0].isdigit()
                        and 0 < int(lengths[0]) <= MAX_REQUEST
                        and state["requests"] < grant["limits"]["max_provider_calls"], "gateway_local_request")
                raw = self.rfile.read(int(lengths[0]))
                require(len(raw) == int(lengths[0]), "gateway_local_body")
                state["requests"] += 1; seq = state["requests"]
                channel.send("request", seq=seq, body=base64.b64encode(raw).decode(), sha256=hashlib.sha256(raw).hexdigest())
                header = channel.receive()
                require(set(header) == {"kind", "seq", "status", "content_type"} and header["kind"] == "headers"
                        and type(header["seq"]) is int and header["seq"] == seq
                        and type(header["status"]) is int and 200 <= header["status"] <= 599
                        and isinstance(header["content_type"], str) and len(header["content_type"]) <= 128
                        and header["content_type"].isascii()
                        and not any(c in header["content_type"] for c in "\r\n\0"), "gateway_response_headers")
                self.send_response(header["status"]); self.send_header("Content-Type", header["content_type"])
                self.send_header("Transfer-Encoding", "chunked"); self.send_header("Connection", "close"); self.end_headers()
                checksum, length = hashlib.sha256(), 0
                while True:
                    frame = channel.receive()
                    require(type(frame.get("seq")) is int and frame["seq"] == seq, "gateway_response_sequence")
                    if frame["kind"] == "end":
                        require(set(frame) == {"kind", "seq", "size", "sha256"} and type(frame["size"]) is int
                                and frame["size"] == length and frame["sha256"] == checksum.hexdigest(), "gateway_response_integrity")
                        self.wfile.write(b"0\r\n\r\n"); self.wfile.flush(); break
                    require(set(frame) == {"kind", "seq", "data"} and frame["kind"] == "data", "gateway_response_incomplete")
                    raw = blob(frame["data"], CHUNK)
                    require(bool(raw), "gateway_empty_chunk")
                    length += len(raw); require(length <= MAX_RESPONSE, "gateway_response_size")
                    checksum.update(raw)
                    self.wfile.write(format(len(raw), "x").encode() + b"\r\n" + raw + b"\r\n"); self.wfile.flush()
            except (OSError, PipeFailure, ValueError):
                state["failed"] = True; close_socket(self.connection)

    class Server(http.server.HTTPServer):
        address_family = socket.AF_INET6 if ipaddress.ip_address(host).version == 6 else socket.AF_INET
        allow_reuse_address = True
        def handle_error(self, *args): state["failed"] = True

    server = Server((host, port), Handler)
    def serve():
        try:
            while not stopped.is_set() and not state["failed"]:
                server.timeout = min(.1, channel.remaining()); server.handle_request()
        except (OSError, PipeFailure): state["failed"] = True
    thread = threading.Thread(target=serve, daemon=True)
    started = False
    try:
        channel.send("ready", host=host, port=port)
        require(channel.receive() == dict(kind="start"), "gateway_start_unverified")
        thread.start(); started = True
        result = execute(request)
    finally:
        stopped.set()
        if started: thread.join(2)
        server.server_close()
    require(not thread.is_alive(), "gateway_server_unclosed")
    channel.send("execution", body=result)
