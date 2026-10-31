"""Opt-in native-client gateway for one already-claimed fresh sample.

No key lookup, funding admission, sample claim, provider destination or resume
operation lives here. The operator supplies the guarded connection factory and
headers; containers receive only the generated opaque token. This component is
not a paid launcher or a native checkpoint/financial-closure certificate.
"""

import hashlib
import http.server
import json
import os
import secrets
import stat
import threading
import time
from pathlib import Path

from scripts.terminal_bench.budgeted_stream import AttemptFailure, forward_attempt, unique_object, no_constant
from scripts.terminal_bench.fresh_budget import BudgetStop
from scripts.terminal_bench.reservation_bound import MAX_BODY_BYTES, request_bytes
from scripts.terminal_bench.no_dispatch import NONCE_HEADER, REASONS, ReceiptError, issue_receipt, request_binding


class GatewayStop(ValueError):
    pass


def save(path, value):
    """Path-based metadata for adapter-owned logs, not the gateway evidence store."""
    path = Path(path)
    temporary = path.with_name(path.name + '.' + secrets.token_hex(8) + '.tmp')
    with temporary.open('xb') as out:
        os.fchmod(out.fileno(), 0o600)
        out.write(json.dumps(value, sort_keys=True, allow_nan=False).encode());out.flush();os.fsync(out.fileno())
    temporary.replace(path)
    fd = os.open(path.parent, os.O_RDONLY | os.O_DIRECTORY | os.O_NOFOLLOW)
    try: os.fsync(fd)
    finally: os.close(fd)


class FreshGateway:
    def __init__(self, root, budget, sample, protocol, *, connection_factory, headers, before_admit,
                 bind_host='127.0.0.1', deadline, stream_timeout=900, drain_timeout=30, receipt_key=None):
        if (os.name != 'posix' or protocol not in ('responses', 'messages') or bind_host not in ('127.0.0.1', '0.0.0.0')
                or not callable(connection_factory) or not callable(before_admit)
                or not isinstance(headers, dict) or not 0 < drain_timeout <= 30
                or not drain_timeout <= stream_timeout <= 900 or time.monotonic() >= deadline
                or (receipt_key is not None and (not isinstance(receipt_key,bytes) or len(receipt_key) != 32))):
            raise GatewayStop('gateway_configuration_invalid')
        self.root = Path(root).absolute()
        if self.root.parent.resolve() != self.root.parent:
            raise GatewayStop('gateway_parent_symlink')
        self.root.mkdir(mode=0o700)
        self._root_fd = None
        try:
            self._root_fd = os.open(self.root, os.O_RDONLY | os.O_DIRECTORY | os.O_NOFOLLOW)
            st = os.fstat(self._root_fd)
            self._root_identity = (st.st_dev, st.st_ino)
            self._check_root()
        except BaseException:
            self._close_root()
            raise
        self.budget, self.sample, self.protocol = budget, sample, protocol
        self.connection_factory, self.headers, self.before_admit = connection_factory, dict(headers), before_admit
        self.deadline, self.stream_timeout, self.drain_timeout = deadline, stream_timeout, drain_timeout
        self.token = secrets.token_urlsafe(32)
        self.receipt_key = receipt_key  # Host verifier only; never copied to task auth/headers/logs.
        self.lock = threading.RLock();self.condition = threading.Condition(self.lock)
        self.records, self.denials, self.streams = [], [], set()
        self.denials_dropped = 0
        self.active_handlers = 0;self.closing = self.closed = self.accounting_error = False
        self.require_agent_start, self.agent_active, self.native_end = False, False, None
        gateway = self

        class Handler(http.server.BaseHTTPRequestHandler):
            def log_message(self, *args): pass

            def setup(self):
                super().setup()
                self.connection.settimeout(5)

            def do_POST(self):
                record, upstream, capture = None, None, None
                receipt_binding = None
                authenticated = False
                response_hash, response_bytes = hashlib.sha256(), 0
                self.headers_sent = False
                with gateway.lock:
                    if gateway.closed:
                        self.close_connection = True
                        return
                    gateway.active_handlers += 1
                try:
                    self.connection.settimeout(5)
                    route = '/responses' if gateway.protocol == 'responses' else '/v1/messages'
                    auth = self.headers.get('Authorization', '') if gateway.protocol == 'responses' else self.headers.get('x-api-key', '')
                    expected = 'Bearer ' + gateway.token if gateway.protocol == 'responses' else gateway.token
                    if self.path != route or not secrets.compare_digest(auth, expected):
                        raise GatewayStop('route_or_auth')
                    authenticated = True
                    lengths = self.headers.get_all('Content-Length', [])
                    if self.headers.get_all('Transfer-Encoding') or len(lengths) != 1 or not lengths[0].isascii() or not lengths[0].isdigit():
                        raise GatewayStop('request_framing')
                    length = int(lengths[0])
                    if not 0 < length <= MAX_BODY_BYTES: raise GatewayStop('request_bound')
                    raw = self.rfile.read(length)
                    if len(raw) != length: raise GatewayStop('request_truncated')
                    try: body = json.loads(raw, object_pairs_hook=unique_object, parse_constant=no_constant)
                    except (ValueError, UnicodeError, RecursionError): raise GatewayStop('request_json') from None
                    if not isinstance(body, dict): raise GatewayStop('request_json')
                    nonces = self.headers.get_all(NONCE_HEADER, [])
                    if gateway.receipt_key is not None and len(nonces) == 1:
                        try: receipt_binding = request_binding(gateway.budget.plan_sha256,gateway.sample,gateway.protocol,raw,nonces[0])
                        except ReceiptError: pass  # No bound receipt; the ordinary denial remains conservative.
                    with gateway.lock:
                        if gateway.closing or gateway.closed or gateway.accounting_error or time.monotonic() >= gateway.deadline:
                            raise GatewayStop('sample_closed_or_unverified')
                        if gateway.require_agent_start and not gateway.agent_active:
                            raise GatewayStop('agent_not_running')
                        gateway._check_root()
                        frozen = request_bytes(body)
                        try: gateway.before_admit(body)
                        except BaseException: raise GatewayStop('admission_guard_failed') from None
                        if request_bytes(body) != frozen: raise GatewayStop('request_guard_mutated_body')
                        # Do not retain the guard's mutable object in the send path.
                        body = json.loads(frozen)
                    absolute = min(gateway.deadline, time.monotonic() + gateway.stream_timeout)

                    def reserved(reservation):
                        nonlocal record, capture
                        if reservation.protocol != gateway.protocol:
                            raise GatewayStop('reserved_protocol_mismatch')
                        record = dict(id=reservation.call_id, sample=gateway.sample, protocol=reservation.protocol,
                                      body_sha256=reservation.body_sha256, state='reserved', result=None,
                                      error=None, usage_known=False, upstream_attempted=False,
                                      response_headers_received=False, stream_diagnostics=None)
                        with gateway.lock:
                            gateway.records.append(record)
                            if gateway.closing: raise GatewayStop('sample_closed_or_unverified')
                            try:
                                with gateway._open_file(record['id']+'.request.json') as out:
                                    os.fchmod(out.fileno(), 0o600);out.write(reservation.wire_body);out.flush();os.fsync(out.fileno())
                                capture = gateway._open_file(record['id']+'.response.sse')
                                os.fchmod(capture.fileno(), 0o600)
                                gateway.persist()
                            except BaseException:
                                gateway.accounting_error = True
                                raise

                    def opened(stream):
                        nonlocal upstream
                        upstream = stream
                        with gateway.lock:
                            gateway.streams.add(stream)
                            record['response_headers_received'] = True
                            if gateway.native_end is not None: stream.record_client_end(gateway.native_end)
                            if gateway.closing: stream.set_deadline(time.monotonic()+gateway.drain_timeout, reason='shutdown_drain')

                    def sink(line):
                        nonlocal response_bytes
                        try:
                            gateway._check_root()
                            if capture.write(line) != len(line): raise OSError('short_capture_write')
                            response_hash.update(line);response_bytes += len(line)
                        except (OSError, GatewayStop):
                            gateway.accounting_error = True
                            raise GatewayStop('response_capture_failed') from None
                        if not self.headers_sent:
                            self.headers_sent = True
                            self.send_response(200);self.send_header('Content-Type', 'text/event-stream')
                            self.send_header('Connection', 'close');self.end_headers()
                        self.wfile.write(line);self.wfile.flush()

                    def connection():
                        with gateway.lock:
                            if gateway.closing: raise GatewayStop('sample_closed_or_unverified')
                            record['upstream_attempted'] = True
                            gateway.persist()
                        return gateway.connection_factory()

                    result = forward_attempt(gateway.budget, gateway.sample, body,
                        connection_factory=connection, headers=gateway.headers,
                        deadline=absolute, write_line=sink, on_reserved=reserved, on_open=opened)
                    record.update(result=result, usage_known=True)
                except BaseException as exc:
                    code = str(exc) if isinstance(exc, (GatewayStop, BudgetStop, AttemptFailure)) else 'gateway_operation_failed'
                    no_dispatch_receipt = None
                    with gateway.lock:
                        if authenticated: gateway.closing = True
                        if record is None:
                            if len(gateway.denials) == 128:
                                gateway.denials.pop(0);gateway.denials_dropped += 1
                            denial = dict(error=code, upstream_opened=False)
                            gateway.denials.append(denial)
                            # Only an authenticated, parsed, request-bound denial
                            # BEFORE reservation can make this assertion. A
                            # capture, connection or stream failure never can.
                            if receipt_binding is not None and code in REASONS and not gateway.accounting_error:
                                try:
                                    candidate = issue_receipt(gateway.receipt_key,receipt_binding,code)
                                    name = 'denial-'+candidate['payload']['receipt_id']+'.no-dispatch.json'
                                    raw_receipt = json.dumps(candidate,sort_keys=True,separators=(',',':'),allow_nan=False).encode()
                                    with gateway._open_file(name) as out:
                                        out.write(raw_receipt);out.flush();os.fsync(out.fileno())
                                    os.fsync(gateway._root_fd)
                                    denial['receipt'] = dict(path=name,sha256=hashlib.sha256(raw_receipt).hexdigest())
                                    gateway.persist()  # Persist ledger reference BEFORE exposing the receipt.
                                    no_dispatch_receipt = candidate
                                except BaseException:
                                    gateway.accounting_error = True
                                    denial.pop('receipt',None)
                        else:
                            record.update(error=code, usage_known=getattr(exc, 'usage_known', False),
                                          stream_diagnostics=getattr(exc, 'stream_diagnostics', None))
                    if not self.headers_sent:
                        try:
                            error_value = dict(error=dict(type='evaluation_stopped',message='evaluation_gateway_stopped'))
                            if no_dispatch_receipt is not None: error_value['no_dispatch_receipt'] = no_dispatch_receipt
                            error_body = json.dumps(error_value,separators=(',',':')).encode()
                            self.send_response(402);self.send_header('Content-Type','application/json')
                            self.send_header('Content-Length', str(len(error_body)))
                            self.send_header('Connection','close');self.end_headers()
                            self.wfile.write(error_body)
                        except OSError: pass
                finally:
                    self.close_connection = True
                    with gateway.lock:
                        try:
                            if capture is not None: capture.flush();os.fsync(capture.fileno())
                        except OSError:
                            gateway.accounting_error = True
                        finally:
                            try:
                                if capture is not None: capture.close()
                            except OSError: gateway.accounting_error = True
                            if upstream is not None: gateway.streams.discard(upstream)
                        if record is not None:
                            try:
                                view = gateway.budget.inspect_call(record['id'])
                                record['call'] = view['call'];record['state'] = view['call']['state']
                                record['usage_known'] = view['call']['state'] == 'known'
                                record['response_sha256'] = response_hash.hexdigest()
                                record['response_bytes'] = response_bytes
                                if record['usage_known']:
                                    record.update({k:view['call'][k] for k in ('input_tokens','cached_input_tokens','output_tokens','known_cost')})
                                    record['thinking_tokens'] = (record.get('result') or {}).get('usage', {}).get('thinking_tokens')
                                gateway.accounting_error |= view['accounting_error']
                            except (OSError, BudgetStop): gateway.accounting_error = True
                        try: gateway.persist()
                        except (OSError, ValueError, TypeError): gateway.accounting_error = True
                        finally:
                            gateway.active_handlers -= 1;gateway.condition.notify_all()
                            if gateway.closed and not gateway.active_handlers: gateway._close_root()

        try:
            self.server = http.server.ThreadingHTTPServer((bind_host, 0), Handler)
            self.server.daemon_threads = True;self.server.block_on_close = False
            self.thread = threading.Thread(target=self.server.serve_forever, kwargs=dict(poll_interval=.05), daemon=True)
            self.persist()
            parent = os.open(self.root.parent, os.O_RDONLY | os.O_DIRECTORY | os.O_NOFOLLOW)
            try: os.fsync(parent)
            finally: os.close(parent)
            self._check_root()
        except BaseException:
            if hasattr(self, 'server'): self.server.server_close()
            self._close_root()
            raise

    def _close_root(self):
        if self._root_fd is not None:
            os.close(self._root_fd);self._root_fd = None

    def _check_root(self):
        try:
            if self._root_fd is None: raise GatewayStop('gateway_evidence_closed')
            current, opened = self.root.lstat(), os.fstat(self._root_fd)
            if (self.root.parent.resolve() != self.root.parent or not stat.S_ISDIR(current.st_mode)
                    or (current.st_dev, current.st_ino) != self._root_identity
                    or (opened.st_dev, opened.st_ino) != self._root_identity
                    or current.st_uid != os.geteuid() or stat.S_IMODE(current.st_mode) != 0o700):
                raise GatewayStop('gateway_directory_changed')
        except OSError:
            raise GatewayStop('gateway_directory_changed') from None

    def _open_file(self, name):
        self._check_root()
        if not isinstance(name, str) or Path(name).name != name: raise GatewayStop('gateway_evidence_name_invalid')
        fd = os.open(name, os.O_WRONLY | os.O_CREAT | os.O_EXCL | os.O_NOFOLLOW, 0o600, dir_fd=self._root_fd)
        return os.fdopen(fd, 'wb')

    def _write_ledger(self, value):
        self._check_root()
        name = 'ledger.'+secrets.token_hex(8)+'.tmp'
        with self._open_file(name) as out:
            out.write(json.dumps(value,sort_keys=True,allow_nan=False).encode());out.flush();os.fsync(out.fileno())
        self._check_root()
        os.replace(name, 'ledger.json', src_dir_fd=self._root_fd, dst_dir_fd=self._root_fd)
        os.fsync(self._root_fd)
        self._check_root()

    def persist(self):
        with self.lock:
            try:
                self._write_ledger(dict(version=1, sample=self.sample, protocol=self.protocol,
                    currency=self.budget.snapshot()['currency'],
                    records=self.records, denials=self.denials, cohort=self.budget.snapshot(),
                    denials_dropped=self.denials_dropped,
                    accounting_error=self.accounting_error, can_resume_agent=False, automatic_paid_resume=False))
            except BaseException:
                self.accounting_error = True
                raise

    def start(self):
        self.thread.start()
        return self

    def client_ended(self, reason):
        if reason not in ('idle_timeout','completed','cancelled','error'): raise GatewayStop('native_end_invalid')
        with self.lock:
            self.native_end, self.agent_active, self.closing = reason, False, True
            for stream in self.streams: stream.record_client_end(reason)

    def wait_idle(self, seconds=35):
        end = time.monotonic()+seconds
        with self.condition:
            while self.active_handlers and time.monotonic() < end:
                self.condition.wait(min(1, end-time.monotonic()))
            return self.active_handlers == 0

    def close(self):
        with self.lock:
            if self.closed: return
            self.closing = True
            for stream in self.streams: stream.set_deadline(time.monotonic()+self.drain_timeout, reason='shutdown_drain')
        if self.thread.is_alive(): self.server.shutdown()
        idle = self.wait_idle(self.drain_timeout+5)
        self.server.server_close()
        if self.thread.is_alive(): self.thread.join(timeout=5)
        self.closed = True
        with self.lock:
            if not self.active_handlers: self._close_root()
        if not idle:
            self.accounting_error = True
            raise GatewayStop('gateway_handlers_not_closed')
