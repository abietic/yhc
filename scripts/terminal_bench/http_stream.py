"""Single-request benchmark HTTP stream with redacted, diagnostic-only timing.

The caller still owns credentials, protocol receipts, spending and recovery.
No redirect, retry, usage inference or provider selection happens here.

Launchers opt in explicitly and persist ``diagnostics()`` in host-owned logs.
Use ``record_client_end`` for a classified native-client outcome, and label
shortened deadlines as shutdown_drain or downstream_drain. Terminal events
are recorded only after the caller's independent protocol validation. Neither
HTTP EOF nor a terminal observation proves closed usage or resumability.

Body timestamps describe decoded HTTP body reads, including partial lines,
not TCP packets. Only the latest 64 reads are retained, with full counters
and an explicit dropped-event count. No body, URL, header or exception text
is retained. Existing sealed launches must not be edited to opt in.
"""
import copy
from datetime import datetime, timezone
import http.client
import math
import socket
import threading
import time

DEADLINE_REASONS = frozenset(("absolute_deadline", "shutdown_drain", "downstream_drain"))
CLIENT_END_REASONS = frozenset(("idle_timeout", "completed", "cancelled", "error"))
CLOSE_REASONS = frozenset(("caller_close", "owner_shutdown", "setup_failed"))
TERMINAL_EVENTS = frozenset(("message_stop", "response.completed", "response.incomplete", "response.failed"))
MAX_BODY_READS = 64


def initial_data(started):
    return dict(version=1, authority="diagnostic_only", started_at_utc=started[1],
                connected_seconds=None, request_attempt_seconds=None, request_sent_seconds=None,
                headers_seconds=None, first_byte_seconds=None,
                last_byte_seconds=None, bytes_received=0, read_calls=0, eof_seconds=None,
                bytes_returned=0, body_reads=[], body_reads_dropped=0,
                terminal_event=None, terminal_seconds=None, client_end=None,
                deadline_revisions={}, deadline_update_seconds={}, timeout_reason=None, error_code=None,
                first_error_code=None, first_error_seconds=None, first_timeout_reason=None,
                timeout_source=None, first_timeout_source=None,
                close_reason=None, close_requested_seconds=None, closed_seconds=None, cleanup_error=None)


def start_time():
    return time.monotonic(), datetime.now(timezone.utc).isoformat()


def valid_deadline(deadline):
    if type(deadline) not in (int, float) or not math.isfinite(deadline):
        raise ValueError("invalid_stream_deadline")


class HTTPStream:
    """An HTTP response with an absolute, shortening-only socket deadline."""

    def __init__(self, connection, deadline, *, started=None):
        valid_deadline(deadline)
        self.connection = connection
        self.socket = connection.sock
        self.started = started or start_time()
        self._data = initial_data(self.started)
        self._data["connected_seconds"] = self.elapsed()
        self.response = None
        self.deadline = float('inf')
        self.timed_out = False
        self.timer = None
        self.lock = threading.Lock()
        self.generation = 0
        self.closed = False
        self._buffer = bytearray()
        self._eof = False
        self.deadline_reason = "absolute_deadline"
        self.set_deadline(deadline)

    def elapsed(self):
        return round(max(0, time.monotonic() - self.started[0]), 6)

    def diagnostics(self):
        with self.lock:
            data = copy.deepcopy(self._data)
            data["elapsed_seconds"] = data["closed_seconds"] if data["closed_seconds"] is not None else self.elapsed()
            if data["body_reads"]:
                data["elapsed_seconds"] = max(data["elapsed_seconds"], data["body_reads"][-1]["finished_seconds"])
            if data["client_end"] is not None:
                data["elapsed_seconds"] = max(data["elapsed_seconds"], data["client_end"]["elapsed_seconds"])
            last = data["last_byte_seconds"]
            data["silence_seconds"] = None if last is None else round(max(0, data["elapsed_seconds"] - last), 6)
            return data

    def _record_read(self, seq, began, block, *, error_code=None, partial=False, expired=False):
        elapsed = self.elapsed()
        if block:
            if self._data["first_byte_seconds"] is None: self._data["first_byte_seconds"] = elapsed
            self._data["last_byte_seconds"] = elapsed
            self._data["bytes_received"] += len(block)
        elif error_code is None and self._data["eof_seconds"] is None:
            self._data["eof_seconds"] = elapsed
        self._data["body_reads"].append(dict(seq=seq, started_seconds=began, finished_seconds=elapsed,
            bytes=len(block), eof=not block and error_code is None, error_code=error_code, partial=partial, expired=expired))
        if len(self._data["body_reads"]) > MAX_BODY_READS:
            del self._data["body_reads"][0]
            self._data["body_reads_dropped"] += 1

    def _record_error(self, code, timeout_reason=None, timeout_source=None):
        # Called with self.lock held; only fixed categorical values enter data.
        self._data["error_code"] = code
        if self._data["first_error_code"] is None:
            self._data["first_error_code"] = code
            self._data["first_error_seconds"] = self.elapsed()
            self._data["first_timeout_reason"] = timeout_reason
            self._data["first_timeout_source"] = timeout_source
        if timeout_reason is not None: self._data["timeout_reason"] = timeout_reason
        if timeout_source is not None: self._data["timeout_source"] = timeout_source

    def record_client_end(self, reason):
        if reason not in CLIENT_END_REASONS: raise ValueError("invalid_client_end_reason")
        with self.lock:
            if self._data["client_end"] is None:
                self._data["client_end"] = dict(reason=reason, elapsed_seconds=self.elapsed())

    def record_terminal(self, event):
        if event not in TERMINAL_EVENTS: raise ValueError("invalid_terminal_event")
        with self.lock:
            if self._data["terminal_event"] is None:
                self._data["terminal_event"] = event
                self._data["terminal_seconds"] = self.elapsed()

    @property
    def status(self):
        return self.response.status

    def abort(self):
        # shutdown interrupts reads held by makefile references; close alone
        # does not reliably unblock a concurrent buffered read.
        try:
            self.socket.shutdown(socket.SHUT_RDWR)
        except OSError:
            pass

    def expire(self, generation):
        with self.lock:
            if self.closed or generation != self.generation:
                return
            self.timed_out = True
            self._record_error("stream_deadline", self.deadline_reason, "timer")
            self.abort()

    def set_deadline(self, deadline, *, reason="absolute_deadline"):
        valid_deadline(deadline)
        if reason not in DEADLINE_REASONS: raise ValueError("invalid_deadline_reason")
        with self.lock:
            if self.closed or self.timed_out or deadline >= self.deadline:
                return
            self.deadline = deadline
            self.deadline_reason = reason
            self.generation += 1
            self._data["deadline_revisions"][reason] = round(deadline - self.started[0], 6)
            self._data["deadline_update_seconds"][reason] = self.elapsed()
            if self.timer is not None:
                self.timer.cancel()
            self.socket.settimeout(max(.001, deadline - time.monotonic()))
            self.timer = threading.Timer(max(0, deadline - time.monotonic()), self.expire, args=(self.generation,))
            self.timer.daemon = True
            self.timer.start()

    def _read(self, limit):
        with self.lock:
            self._data["read_calls"] += 1
            seq, began = self._data["read_calls"], self.elapsed()
        try:
            line = self.response.read1(limit)
        except (OSError, http.client.HTTPException) as exc:
            with self.lock:
                expired = self.timed_out or time.monotonic() >= self.deadline
                owned_timeout = expired or isinstance(exc, socket.timeout)
                code = "stream_deadline" if owned_timeout else "upstream_read_error"
                reason = self.deadline_reason if owned_timeout else None
                source = "timer" if self.timed_out else "socket_timeout" if isinstance(exc, socket.timeout) else "monotonic" if expired else None
                self._record_error(code, reason, source)
                partial = exc.partial if isinstance(exc, http.client.IncompleteRead) and isinstance(exc.partial, bytes) else b""
                self._record_read(seq, began, partial, error_code=code, partial=bool(partial), expired=expired)
            if expired or isinstance(exc, socket.timeout):
                raise TimeoutError('stream_deadline') from None
            raise
        with self.lock:
            expired = self.timed_out or time.monotonic() >= self.deadline
            self._record_read(seq, began, line, expired=expired)
            if expired:
                self._record_error("stream_deadline", self.deadline_reason, "timer" if self.timed_out else "monotonic")
        if expired:
            raise TimeoutError('stream_deadline')
        return line

    def readline(self, limit):
        """Preserve bounded line framing; time each body read, including partial lines."""
        if type(limit) is not int or limit <= 0: raise ValueError("invalid_line_limit")
        while True:
            with self.lock:
                if self.closed: raise OSError("stream_closed")
                if self.timed_out or time.monotonic() >= self.deadline:
                    self._record_error("stream_deadline", self.deadline_reason, "timer" if self.timed_out else "monotonic")
                    raise TimeoutError("stream_deadline")
            newline = self._buffer.find(b"\n", 0, limit)
            if newline >= 0 or len(self._buffer) >= limit or self._eof:
                end = newline + 1 if newline >= 0 else min(limit, len(self._buffer))
                line = bytes(self._buffer[:end])
                del self._buffer[:end]
                with self.lock: self._data["bytes_returned"] += len(line)
                return line
            block = self._read(min(32768, limit - len(self._buffer)))
            self._buffer.extend(block)
            self._eof = not block

    def close(self, *, reason="caller_close"):
        if reason not in CLOSE_REASONS: raise ValueError("invalid_close_reason")
        with self.lock:
            if self.closed: return
            self.closed = True
            self._data["close_reason"] = reason
            self._data["close_requested_seconds"] = self.elapsed()
            if self.timer is not None:
                self.timer.cancel()
        self.abort()
        try:
            if self.response is not None: self.response.close()
        finally:
            try: self.connection.close()
            finally:
                try: self.socket.close()
                finally:
                    with self.lock: self._data["closed_seconds"] = self.elapsed()


def open_http_stream(connection, path, data, headers, deadline):
    """Also used by local fixtures; no redirect or automatic retry mechanism."""
    valid_deadline(deadline)
    started = start_time()
    stream = None
    try:
        connection.connect()
        stream = HTTPStream(connection, deadline, started=started)
        if time.monotonic() >= deadline:
            raise TimeoutError('stream_deadline')
        with stream.lock: stream._data["request_attempt_seconds"] = stream.elapsed()
        connection.request('POST', path, data, headers)
        with stream.lock: stream._data["request_sent_seconds"] = stream.elapsed()
        stream.response = connection.getresponse()
        with stream.lock:
            if stream.timed_out or time.monotonic() >= stream.deadline:
                raise TimeoutError("stream_deadline")
            stream._data["headers_seconds"] = stream.elapsed()
        return stream
    except BaseException as exc:
        normalize_timeout = False
        if stream is not None:
            with stream.lock:
                expired = stream.timed_out or time.monotonic() >= stream.deadline
                normalize_timeout = isinstance(exc, (OSError, http.client.HTTPException)) and (expired or isinstance(exc, socket.timeout))
                stage = "request_send_error" if stream._data["request_sent_seconds"] is None else "headers_error"
                code = "stream_deadline" if normalize_timeout else stage
                reason = stream.deadline_reason if normalize_timeout else None
                source = "timer" if stream.timed_out else "socket_timeout" if isinstance(exc, socket.timeout) else "monotonic" if expired else None
                stream._record_error(code, reason, source)
            try: stream.close(reason="setup_failed")
            except OSError:
                with stream.lock: stream._data["cleanup_error"] = "stream_close_error"
            diagnostic = stream.diagnostics()
        else:
            diagnostic = initial_data(started)
            # No HTTPStream exists yet, so its first-cause recorder cannot run.
            # Capture the connect failure before cleanup; this remains timing
            # evidence only, never a receipt of zero provider usage.
            failed_at = round(max(0, time.monotonic() - started[0]), 6)
            reason = "connect_timeout" if isinstance(exc, socket.timeout) else None
            source = "socket_timeout" if reason else None
            diagnostic.update(first_error_code="connect_error", first_error_seconds=failed_at,
                              timeout_reason=reason, first_timeout_reason=reason,
                              timeout_source=source, first_timeout_source=source)
            try: connection.close()
            except OSError: diagnostic["cleanup_error"] = "connection_close_error"
            diagnostic.update(error_code="connect_error", close_reason="setup_failed",
                              elapsed_seconds=round(max(0, time.monotonic() - started[0]), 6))
        if normalize_timeout:
            error = TimeoutError("stream_deadline")
            error.stream_diagnostics = diagnostic
            raise error from None
        exc.stream_diagnostics = diagnostic
        raise
