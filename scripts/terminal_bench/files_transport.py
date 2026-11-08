"""Opt-in, single-trial Anthropic Files transport for the evaluation host.

The caller owns the fixed connection origin, authentication and live guards.
Files are free auxiliary operations, never model attempts or usage receipts.
Account-wide listing/cleanup and cold recovery are deliberately unavailable.
"""

import hashlib
import json
import re
import secrets
import socket
import threading
import time
from datetime import datetime
from email import policy
from email.parser import BytesParser
from urllib.parse import parse_qs, urlsplit

from scripts.terminal_bench.budgeted_stream import unique_object, no_constant
from scripts.terminal_bench.http_stream import open_http_stream

BETA = 'files-api-2025-04-14'
MAX_UPLOAD_BYTES = 64 * 1024 * 1024
MAX_MULTIPART_BYTES = MAX_UPLOAD_BYTES + 16384
MAX_METADATA_BYTES = 65536
FILE_ID = re.compile(r'file-api-[A-Za-z0-9_-]{1,180}\Z')


class FilesStop(ValueError):
    pass


def beta_headers(headers):
    """Add Files beta to trusted host headers, preserving other admitted betas."""
    result = {k:v for k,v in headers.items() if k.lower() != 'anthropic-beta'}
    values = [part.strip() for key, value in headers.items() if key.lower() == 'anthropic-beta'
              for part in value.split(',') if part.strip()]
    result['anthropic-beta'] = ','.join(dict.fromkeys(values+[BETA]))
    return result


def file_references(body, protocol):
    """Inspect media content only; tool inputs/schemas may contain ordinary IDs."""
    result = []
    def content(value):
        if not isinstance(value, list): return
        for block in value:
            if not isinstance(block, dict): continue
            kind = block.get('type')
            if kind == 'tool_result': content(block.get('content'))
            elif kind in ('image', 'document', 'file', 'input_image', 'input_file'):
                source = block.get('source')
                if isinstance(source, dict) and (source.get('type') == 'file' or 'file_id' in source):
                    result.append(source.get('file_id'))
                if 'file_id' in block: result.append(block['file_id'])
    for message in body.get('messages' if protocol == 'messages' else 'input', []):
        if isinstance(message, dict): content([message]);content(message.get('content'))
    content(body.get('system'))
    return result


def upload_metadata(raw, content_type):
    """Validate bounded multipart without changing a byte sent to the provider."""
    if not isinstance(content_type, str) or '\r' in content_type or '\n' in content_type:
        raise FilesStop('files_content_type')
    message = BytesParser(policy=policy.default).parsebytes(
        b'Content-Type: '+content_type.encode('ascii')+b'\r\nMIME-Version: 1.0\r\n\r\n'+raw)
    boundary = message.get_boundary()
    if (message.get_content_type() != 'multipart/form-data' or not boundary
            or not re.fullmatch(r'[A-Za-z0-9\x27()+_,./:=?-]{1,70}', boundary)
            or message.defects or not message.is_multipart()
            or not raw.startswith(('--'+boundary+'\r\n').encode())
            or not raw.endswith(('--'+boundary+'--\r\n').encode())):
        raise FilesStop('files_multipart')
    fields = {};image = None
    for part in message.iter_parts():
        if (part.defects or part.is_multipart() or part.get_content_disposition() != 'form-data'
                or part.get_all('Content-Transfer-Encoding') or len(part.get_all('Content-Disposition', [])) != 1
                or len(part.get_all('Content-Type', [])) > 1):
            raise FilesStop('files_multipart')
        name = part.get_param('name', header='content-disposition')
        if name in fields or name not in ('file', 'purpose', 'expires_after[anchor]', 'expires_after[seconds]'):
            raise FilesStop('files_fields')
        data = part.get_payload(decode=True)
        if not isinstance(data, bytes): raise FilesStop('files_multipart')
        fields[name] = data
        if name == 'file':
            filename, mime = part.get_filename(), part.get_content_type()
            signatures = {'image/png': data.startswith(b'\x89PNG\r\n\x1a\n'),
                          'image/jpeg': data.startswith(b'\xff\xd8\xff'),
                          'image/gif': data.startswith((b'GIF87a', b'GIF89a')),
                          'image/webp': data.startswith(b'RIFF') and data[8:12] == b'WEBP'}
            if (not filename or len(filename.encode('utf-8')) > 512 or filename in ('.', '..')
                    or any(c in filename for c in ('/', '\\', '\x00', '\r', '\n'))
                    or not 0 < len(data) <= MAX_UPLOAD_BYTES or not signatures.get(mime)):
                raise FilesStop('files_image')
            image = dict(filename=filename, mime_type=mime, size_bytes=len(data),
                         sha256=hashlib.sha256(data).hexdigest())
        elif part.get_filename() is not None or len(data) > 32:
            raise FilesStop('files_fields')
    if image is None or fields.get('purpose', b'user_data') != b'user_data': raise FilesStop('files_fields')
    anchor, seconds = fields.get('expires_after[anchor]'), fields.get('expires_after[seconds]')
    if anchor is not None or seconds is not None:
        if anchor != b'created_at' or seconds is None or not re.fullmatch(rb'[0-9]{4,7}', seconds) or not 3600 <= int(seconds) <= 2592000:
            raise FilesStop('files_expiry')
        image['expiry_seconds'] = int(seconds)
    return image


def metadata(value):
    if (not isinstance(value, dict) or not isinstance(value.get('id'), str) or not FILE_ID.fullmatch(value['id'])
            or value.get('type') != 'file' or type(value.get('size_bytes')) is not int
            or not 0 < value['size_bytes'] <= MAX_UPLOAD_BYTES
            or not isinstance(value.get('filename'), str) or not isinstance(value.get('created_at'), str)
            or not re.fullmatch(r'\d{4}-\d\d-\d\dT\d\d:\d\d:\d\d(?:\.\d+)?(?:Z|[+-]\d\d:\d\d)', value['created_at'])
            or value.get('mime_type') not in ('image/png', 'image/jpeg', 'image/gif', 'image/webp')):
        raise FilesStop('files_metadata_unverified')
    try:
        created = datetime.fromisoformat(value['created_at'].replace('Z', '+00:00'))
        if created.tzinfo is None or created.timestamp() > time.time()+60: raise ValueError()
    except (ValueError, OverflowError): raise FilesStop('files_metadata_unverified') from None
    return {key: value[key] for key in ('id', 'type', 'size_bytes', 'created_at', 'filename', 'mime_type')}


class FilesTransport:
    def __init__(self, *, before_request, max_operations=1024):
        if not callable(before_request) or type(max_operations) is not int or not 1 <= max_operations <= 10000:
            raise FilesStop('files_configuration')
        self.before_request, self.max_operations = before_request, max_operations
        self.bound = self.busy = self.unverified = False
        self.owned, self.operations = {}, []

    def bind(self, protocol):
        if self.bound or protocol != 'messages': raise FilesStop('files_binding')
        self.bound = True

    def snapshot(self):
        return dict(operations=self.operations, owned=self.owned, unverified=self.unverified,
                    can_resume_agent=False)

    def validate_model(self, references):
        if self.busy or self.unverified: raise FilesStop('files_state_unverified')
        for file_id in references:
            item = self.owned.get(file_id) if isinstance(file_id, str) else None
            if item is None or item['state'] != 'available' or time.time() >= item['expires_at']:
                raise FilesStop('files_reference_unverified')

    def serve(self, handler, gateway):
        """Own an auxiliary handler through durable closure, sharing gateway drain."""
        authenticated = False;operation = None;stream = None;started = False
        with gateway.lock:
            if gateway.closed: handler.close_connection = True;return
            gateway.active_handlers += 1
        try:
            # Reject foreign clients without revoking the owned native session.
            auth = handler.headers.get_all('x-api-key', [])
            if len(auth) != 1 or not secrets.compare_digest(auth[0], gateway.token):
                raise FilesStop('files_auth')
            authenticated = True
            if (handler.headers.get_all('anthropic-version', []) != ['2023-06-01']
                    or handler.headers.get_all('anthropic-beta', []) != [BETA]):
                raise FilesStop('files_headers')
            parsed = urlsplit(handler.path)
            if parsed.fragment or parsed.netloc or parsed.scheme: raise FilesStop('files_path')
            collection = parsed.path == '/v1/files'
            file_id = parsed.path.removeprefix('/v1/files/')
            if not collection and (not parsed.path.startswith('/v1/files/') or not FILE_ID.fullmatch(file_id)):
                raise FilesStop('files_path')
            if handler.command not in ('POST', 'GET', 'DELETE') or (handler.command == 'POST' and not collection):
                raise FilesStop('files_method')
            if collection and handler.command == 'DELETE': raise FilesStop('files_method')
            with gateway.lock:
                if (gateway.closing or gateway.accounting_error or self.unverified or self.busy
                        or time.monotonic() >= gateway.deadline or gateway.active_model_handlers
                        or gateway.require_agent_start and not gateway.agent_active):
                    raise FilesStop('files_admission')
                gateway._check_root()
                if len(self.operations) >= self.max_operations: raise FilesStop('files_operation_limit')
                if not collection: self.validate_model([file_id])
                operation = dict(method=handler.command, collection=collection, file_id=None if collection else file_id,
                                 state='receiving', upstream_attempted=False)
                self.operations.append(operation);self.busy = started = True
                gateway.persist()
            lengths = handler.headers.get_all('Content-Length', [])
            if (handler.headers.get_all('Transfer-Encoding') or len(lengths) > 1
                    or lengths and (not lengths[0].isascii() or not lengths[0].isdigit())):
                raise FilesStop('files_framing')
            length = int(lengths[0]) if lengths else 0
            if handler.command == 'POST':
                if not 0 < length <= MAX_MULTIPART_BYTES or parsed.query: raise FilesStop('files_bound')
                types = handler.headers.get_all('Content-Type', [])
                if len(types) != 1: raise FilesStop('files_content_type')
                # A trickling client cannot hold a handler beyond shutdown's bounded drain.
                def abort_body():
                    try: handler.connection.shutdown(socket.SHUT_RDWR)
                    except OSError: pass
                timer = threading.Timer(max(.001, min(gateway.deadline-time.monotonic(), gateway.drain_timeout)), abort_body)
                timer.daemon = True;timer.start()
                try: raw = handler.rfile.read(length)
                finally: timer.cancel()
                if len(raw) != length: raise FilesStop('files_truncated')
                image = upload_metadata(raw, types[0])
            else:
                if length or (not collection and parsed.query): raise FilesStop('files_framing')
                raw, image = b'', None
            with gateway.lock:
                if (gateway.closing or gateway.accounting_error or self.unverified
                        or time.monotonic() >= gateway.deadline
                        or gateway.require_agent_start and not gateway.agent_active):
                    raise FilesStop('files_admission')
                gateway._check_root()
                try: self.before_request(handler.command)
                except BaseException: raise FilesStop('files_guard_failed') from None
                operation['state'] = 'admitted'
                if image: operation.update(request_sha256=hashlib.sha256(raw).hexdigest(), upload=image)
                gateway.persist()  # Failed evidence publication prevents the request.
            if collection and handler.command == 'GET':
                result = self.page(parsed.query);status = 200
            else:
                with gateway.lock:
                    if gateway.closing: raise FilesStop('files_admission')
                    if handler.command == 'DELETE': self.owned[file_id]['state'] = 'deleting'
                    operation['upstream_attempted'] = True;gateway.persist()
                headers = {k:v for k,v in beta_headers(gateway.headers).items()
                           if k.lower() not in ('content-type', 'content-length', 'transfer-encoding', 'anthropic-version')}
                headers['anthropic-version'] = '2023-06-01'
                if image: headers['Content-Type'] = types[0]
                deadline = min(gateway.deadline, time.monotonic()+min(600, gateway.stream_timeout))
                def connected(opened):
                    nonlocal stream
                    stream = opened
                    with gateway.lock:
                        gateway.streams.add(opened)
                        if gateway.native_end is not None: opened.record_client_end(gateway.native_end)
                        if gateway.closing: opened.set_deadline(time.monotonic()+gateway.drain_timeout, reason='shutdown_drain')
                try: connection = gateway.connection_factory()
                except BaseException: raise FilesStop('files_connection_failed') from None
                stream = open_http_stream(connection, '/anthropic'+parsed.path, raw, headers,
                                          deadline, method=handler.command, on_connected=connected)
                status = stream.status
                response = bytearray()
                while True:
                    line = stream.readline(MAX_METADATA_BYTES+1)
                    if not line: break
                    response.extend(line)
                    if len(response) > MAX_METADATA_BYTES: raise FilesStop('files_response_bound')
                if stream.response.length not in (None, 0): raise FilesStop('files_response_truncated')
                operation.update(http_status=status, response_sha256=hashlib.sha256(response).hexdigest(),
                                 response_bytes=len(response))
                if image and status in (400, 413, 415, 422, 429):
                    # Definite rejection: preserve native SDK inline fallback; no model call was made.
                    try:
                        error = json.loads(response, object_pairs_hook=unique_object, parse_constant=no_constant).get('error', {})
                        detail = ' '.join(error[k] for k in ('type', 'code', 'message') if isinstance(error.get(k), str))
                    except (ValueError, AttributeError, TypeError): detail = ''
                    # Preserve native quota classification without disclosing provider error text.
                    quota = re.search(r'quota|storage|stored files|file count|too many files', detail, re.I)
                    result = dict(error=dict(type='invalid_request_error',
                        message='files_storage_quota_exceeded' if quota else 'files_upload_rejected'))
                    operation['state'] = 'rejected'
                elif status not in ((200, 201) if image else (200,)):
                    raise FilesStop('files_upstream_unverified')  # No redirect, retry or error-body leak.
                else:
                    value = json.loads(response, object_pairs_hook=unique_object, parse_constant=no_constant)
                    with gateway.lock:
                        if gateway.closing: raise FilesStop('files_admission')
                        if handler.command == 'DELETE':
                            if value != dict(id=file_id, type='file_deleted'): raise FilesStop('files_delete_unverified')
                            self.owned[file_id]['state'] = 'deleted';result = value
                        else:
                            result = metadata(value)
                            if image:
                                if result['id'] in self.owned or any(result[k] != image[k] for k in ('filename', 'size_bytes', 'mime_type')):
                                    raise FilesStop('files_upload_unverified')
                                created = datetime.fromisoformat(result['created_at'].replace('Z', '+00:00')).timestamp()
                                expiry = created+image['expiry_seconds'] if 'expiry_seconds' in image else float('inf')
                                if expiry <= time.time(): raise FilesStop('files_expiry_unverified')
                                self.owned[result['id']] = dict(metadata=result, sha256=image['sha256'], state='available',
                                                               expires_at=expiry if expiry != float('inf') else 1e30)
                                operation['file_id'] = result['id']
                            elif result != self.owned[file_id]['metadata']:
                                raise FilesStop('files_retrieve_unverified')
            # A successful reply must not precede upstream cleanup or leave an
            # admission slot held until the HTTP handler's final drain.
            if stream is not None: stream.close()
            with gateway.lock:
                if gateway.closing or gateway.accounting_error: raise FilesStop('files_admission')
                if operation['state'] != 'rejected': operation['state'] = 'verified'
                self.busy = started = False
                gateway.persist()  # Only release validated metadata after its ownership is durable.
            self.reply(handler, status, result)
        except BaseException as exc:
            with gateway.lock:
                if authenticated:
                    gateway.closing = self.unverified = True
                if operation is not None:
                    operation.update(state='unverified', error=str(exc) if isinstance(exc, FilesStop) else 'files_operation_failed')
                    if stream is not None: operation['stream_diagnostics'] = stream.diagnostics()
                try: gateway.persist()
                except BaseException: gateway.accounting_error = True
            try: self.reply(handler, 400, dict(error=dict(type='invalid_request_error', message='files_transport_stopped')))
            except OSError: pass
        finally:
            if stream is not None:
                try: stream.close()
                except OSError:
                    with gateway.lock: gateway.closing = self.unverified = True
            handler.close_connection = True
            with gateway.lock:
                if stream is not None: gateway.streams.discard(stream)
                if started: self.busy = False
                try: gateway.persist()
                except BaseException: gateway.accounting_error = gateway.closing = True
                gateway.active_handlers -= 1;gateway.condition.notify_all()
                if gateway.closed and not gateway.active_handlers: gateway._close_root()

    def page(self, query):
        params = parse_qs(query, keep_blank_values=True, strict_parsing=True) if query else {}
        if (set(params)-{'after_id', 'before_id', 'limit'} or any(len(v) != 1 for v in params.values())
                or 'after_id' in params and 'before_id' in params): raise FilesStop('files_pagination')
        limit = params.get('limit', ['20'])[0]
        if not re.fullmatch(r'[0-9]{1,4}', limit) or not 1 <= int(limit) <= 1000: raise FilesStop('files_pagination')
        # Account files never enter the task's view, even when native quota cleanup lists them.
        items = [item['metadata'] for item in reversed(list(self.owned.values()))
                 if item['state'] == 'available' and time.time() < item['expires_at']]
        for name in ('after_id', 'before_id'):
            if name not in params: continue
            ids = [v['id'] for v in items]
            if params[name][0] not in ids: raise FilesStop('files_cursor_unverified')
            pos = ids.index(params[name][0]);items = items[pos+1:] if name == 'after_id' else items[:pos]
        page = items[:int(limit)]
        return dict(data=page, first_id=page[0]['id'] if page else None,
                    last_id=page[-1]['id'] if page else None, has_more=len(items) > len(page))

    @staticmethod
    def reply(handler, status, value):
        raw = json.dumps(value, separators=(',', ':'), allow_nan=False).encode()
        handler.send_response(status);handler.send_header('Content-Type', 'application/json')
        handler.send_header('Content-Length', str(len(raw)));handler.send_header('Connection', 'close')
        handler.end_headers();handler.wfile.write(raw)
