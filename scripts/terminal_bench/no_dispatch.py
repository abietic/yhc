"""Host-only authenticated evidence for one rejected, never-forwarded request.

The verifier key stays outside task containers and native clients. This HMAC
protocol is operator evidence, NOT a native zero-usage receipt, refund, funding
admission or recovery grant. A client must not trust a bare HTTP error or this
JSON without independently pinned request inputs and the host verifier key.
"""

import hashlib
import hmac
import json
import os
import re
import stat
import uuid
from pathlib import Path

from scripts.terminal_bench.reservation_bound import MAX_BODY_BYTES, canonical, digest

NONCE_HEADER = 'X-YHC-Request-Nonce'
SCOPE = 'benchmark-host-no-dispatch-v1'
REASONS = frozenset(('sample_call_limit', 'sample_budget_exhausted', 'cohort_budget_exhausted',
                     'agent_not_running', 'admission_guard_failed', 'request_guard_mutated_body'))


class ReceiptError(ValueError):
    pass


def require(value, code):
    if not value: raise ReceiptError(code)


def sha(value):
    return isinstance(value, str) and re.fullmatch('[0-9a-f]{64}', value) is not None


def identity(value):
    return isinstance(value, str) and re.fullmatch('[A-Za-z0-9][A-Za-z0-9_-]{0,127}', value) is not None


def _unique(pairs):
    result = {}
    for key, value in pairs:
        require(key not in result, 'receipt_json_duplicate')
        result[key] = value
    return result


def _constant(value):
    raise ReceiptError('receipt_json_nonfinite')


def validate_binding(value):
    require(isinstance(value, dict) and set(value) == {'plan_sha256', 'sample', 'protocol', 'method', 'path',
            'model', 'nonce', 'request_body_sha256'} and sha(value['plan_sha256']) and identity(value['sample'])
            and value['protocol'] in ('responses','messages') and value['method'] == 'POST'
            and value['path'] == ('/responses' if value['protocol'] == 'responses' else '/v1/messages')
            and isinstance(value['model'], str) and re.fullmatch('[A-Za-z0-9][A-Za-z0-9._-]{0,127}', value['model'])
            and sha(value['nonce']) and sha(value['request_body_sha256']), 'receipt_request_binding_invalid')


def request_binding(plan_sha256, sample, protocol, raw, nonce):
    require(isinstance(raw, bytes) and 0 < len(raw) <= MAX_BODY_BYTES, 'receipt_request_size')
    try: body = json.loads(raw, object_pairs_hook=_unique, parse_constant=_constant)
    except (ValueError, UnicodeError, RecursionError): raise ReceiptError('receipt_request_json') from None
    require(isinstance(body, dict), 'receipt_request_json')
    value = dict(plan_sha256=plan_sha256, sample=sample, protocol=protocol, method='POST',
                 path='/responses' if protocol == 'responses' else '/v1/messages', model=body.get('model'),
                 nonce=nonce, request_body_sha256=hashlib.sha256(raw).hexdigest())
    validate_binding(value)
    return value


def _key(key):
    require(isinstance(key, bytes) and len(key) == 32, 'receipt_host_key_invalid')


def issue_receipt(key, binding, reason):
    _key(key);validate_binding(binding)
    require(isinstance(reason,str) and reason in REASONS, 'receipt_reason_not_proven')
    payload = dict(version=1, scope=SCOPE, receipt_id=str(uuid.uuid4()), request=dict(binding),
                   reason=reason, upstream_attempted=False)
    return dict(payload=payload, mac=hmac.new(key, SCOPE.encode()+b'\0'+canonical(payload), hashlib.sha256).hexdigest())


def verify_receipt(envelope, key, expected):
    _key(key);validate_binding(expected)
    require(isinstance(envelope, dict) and set(envelope) == {'payload','mac'} and sha(envelope['mac']), 'receipt_envelope_invalid')
    payload = envelope['payload']
    require(isinstance(payload, dict) and set(payload) == {'version','scope','receipt_id','request','reason','upstream_attempted'}
            and type(payload['version']) is int and payload['version'] == 1 and payload['scope'] == SCOPE
            and isinstance(payload['reason'],str) and payload['reason'] in REASONS
            and payload['upstream_attempted'] is False, 'receipt_payload_invalid')
    try:
        parsed = uuid.UUID(payload['receipt_id'])
        require(parsed.version == 4 and str(parsed) == payload['receipt_id'], 'receipt_identity_invalid')
    except (ValueError, TypeError, AttributeError): raise ReceiptError('receipt_identity_invalid') from None
    validate_binding(payload['request'])
    require(canonical(payload['request']) == canonical(expected), 'receipt_request_mismatch')
    mac = hmac.new(key, SCOPE.encode()+b'\0'+canonical(payload), hashlib.sha256).hexdigest()
    require(hmac.compare_digest(envelope['mac'], mac), 'receipt_authentication_failed')
    return dict(version=1, scope=SCOPE, receipt_id=payload['receipt_id'], reason=payload['reason'],
                request_body_sha256=expected['request_body_sha256'], receipt_sha256=digest(envelope),
                upstream_attempted=False, host_verified=True, native_usage_reconciled=False,
                funding_release_authorized=False, can_resume_agent=False, automatic_paid_resume=False)


def verify_stored_receipt(root, reference, key, expected):
    """Read the exact private receipt named by the independently pinned host ledger."""
    require(isinstance(reference, dict) and set(reference) == {'path','sha256'} and sha(reference['sha256'])
            and isinstance(reference['path'], str)
            and re.fullmatch('denial-[0-9a-f-]{36}\\.no-dispatch\\.json', reference['path']), 'receipt_reference_invalid')
    root = Path(root).absolute()
    try:
        require(root.parent.resolve() == root.parent, 'receipt_directory_unverified')
        fd = os.open(root, os.O_RDONLY | os.O_DIRECTORY | os.O_NOFOLLOW)
        try:
            before = os.fstat(fd)
            require(before.st_uid == os.geteuid() and stat.S_IMODE(before.st_mode) == 0o700, 'receipt_directory_unverified')
            child = os.open(reference['path'], os.O_RDONLY | os.O_NOFOLLOW | os.O_NONBLOCK, dir_fd=fd)
            with os.fdopen(child, 'rb') as file:
                info = os.fstat(file.fileno())
                require(stat.S_ISREG(info.st_mode) and info.st_uid == os.geteuid() and info.st_nlink == 1
                        and stat.S_IMODE(info.st_mode) == 0o600 and info.st_size <= 16384, 'receipt_file_unverified')
                raw = file.read(16385)
            after = root.lstat()
            require(stat.S_ISDIR(after.st_mode) and (before.st_dev,before.st_ino) == (after.st_dev,after.st_ino)
                    and after.st_uid == os.geteuid() and stat.S_IMODE(after.st_mode) == 0o700
                    and root.parent.resolve() == root.parent, 'receipt_directory_changed')
        finally: os.close(fd)
        require(len(raw) <= 16384 and hashlib.sha256(raw).hexdigest() == reference['sha256'], 'receipt_storage_mismatch')
        envelope = json.loads(raw, object_pairs_hook=_unique, parse_constant=_constant)
        return verify_receipt(envelope,key,expected)
    except ReceiptError: raise
    except (OSError,ValueError,TypeError,RecursionError): raise ReceiptError('receipt_storage_unverified') from None
