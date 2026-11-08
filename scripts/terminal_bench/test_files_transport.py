"""Native Files transport contract; loopback peers only, no provider credentials."""

import http.client
import http.server
import json
import tempfile
import threading
import time
import unittest
from datetime import datetime, timezone
from pathlib import Path
from unittest import mock

from scripts.terminal_bench.fresh_budget import FreshBudget, digest
from scripts.terminal_bench.fresh_gateway import FreshGateway
from scripts.terminal_bench.test_fresh_budget import body, plan
from scripts.terminal_bench.test_budgeted_stream import message_events


def multipart(data=b'\x89PNG\r\n\x1a\nimage', filename='image.png'):
    boundary = 'test-upload-boundary'
    raw = (f'--{boundary}\r\nContent-Disposition: form-data; name="file"; filename="{filename}"\r\n'
           'Content-Type: image/png\r\n\r\n').encode() + data + f'\r\n--{boundary}--\r\n'.encode()
    return raw, 'multipart/form-data; boundary='+boundary


class FilesTests(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory();self.addCleanup(self.tmp.cleanup)
        self.root = Path(self.tmp.name).resolve()
        self.upstream = [];self.guard_calls = []
        self.reply_status = 200;self.reply_body = None;self.extra_reply_length = 0
        self.peer_started = threading.Event();self.peer_release = threading.Event();self.stall = False
        self.metadata = dict(id='file-api-owned', type='file', size_bytes=13,
                             created_at=datetime.now(timezone.utc).isoformat(),
                             filename='image.png', mime_type='image/png')
        owner = self
        class Peer(http.server.BaseHTTPRequestHandler):
            def log_message(self, *args): pass
            def dispatch(self):
                raw = self.rfile.read(int(self.headers.get('Content-Length', 0)))
                owner.upstream.append((self.command, self.path, dict(self.headers), raw))
                owner.peer_started.set()
                if owner.stall: owner.peer_release.wait(4)
                if self.path.endswith('/messages'):
                    status, data = 200, message_events()
                else:
                    value = (dict(id=owner.metadata['id'], type='file_deleted')
                             if self.command == 'DELETE' else owner.metadata)
                    status = owner.reply_status
                    data = owner.reply_body if owner.reply_body is not None else json.dumps(value).encode()
                self.send_response(status);self.send_header('Content-Length', str(len(data)+owner.extra_reply_length))
                self.end_headers()
                try: self.wfile.write(data)
                except OSError: pass
            do_POST = do_GET = do_DELETE = dispatch
        self.peer = http.server.ThreadingHTTPServer(('127.0.0.1', 0), Peer)
        self.thread = threading.Thread(target=self.peer.serve_forever, daemon=True);self.thread.start()
        self.addCleanup(self.close_peer)

    def close_peer(self):
        self.peer_release.set()
        self.peer.shutdown();self.peer.server_close();self.thread.join(3)

    def create(self, enabled=True, protocol='messages', **options):
        from scripts.terminal_bench.files_transport import FilesTransport
        value = plan((protocol,), cap='6', sample_cap='6')
        budget = FreshBudget.create(self.root/'budget', value, digest(value));budget.claim_sample('sample-0')
        self.addCleanup(budget.close)
        transport = FilesTransport(before_request=lambda method: self.guard_calls.append(method), **options) if enabled else None
        gateway = FreshGateway(self.root/'wire', budget, 'sample-0', protocol,
            connection_factory=lambda: http.client.HTTPConnection('127.0.0.1', self.peer.server_port, timeout=2),
            headers={'x-api-key':'host-only-test-key'}, before_admit=lambda request: None,
            files_transport=transport, deadline=time.monotonic()+30, drain_timeout=.2).start()
        self.addCleanup(gateway.close)
        self.gateway, self.budget = gateway, budget
        return gateway

    def request(self, method, path, data=None, headers=None, wait=True):
        connection = http.client.HTTPConnection('127.0.0.1', self.gateway.server.server_port, timeout=3)
        values = {'x-api-key': self.gateway.token, 'anthropic-version':'2023-06-01',
                  'anthropic-beta':'files-api-2025-04-14'}
        values.update(headers or {})
        try:
            connection.request(method, path, data, values)
            response = connection.getresponse();result = response.status, response.read()
        finally: connection.close()
        if wait: self.assertTrue(self.gateway.wait_idle(3))
        return result

    def upload(self):
        raw, content_type = multipart()
        return self.request('POST', '/v1/files', raw, {'Content-Type': content_type})

    def model(self, file_id='file-api-owned', wait=True):
        value = body('messages')
        value['messages'] = [{'role':'user', 'content':[{'type':'image', 'source':{'type':'file', 'file_id':file_id}}]}]
        return self.request('POST', '/v1/messages', json.dumps(value), {'Content-Type':'application/json'}, wait=wait)

    def after_ack(self, method, path, first, second, first_status=200):
        """The client has all response bytes while the first handler still exists."""
        from scripts.terminal_bench.files_transport import FilesTransport
        acknowledged, release = threading.Event(), threading.Event()
        original = FilesTransport.reply
        def paused(handler, status, value):
            original(handler, status, value)
            if handler.command == method and handler.path == path and not acknowledged.is_set():
                acknowledged.set()
                release.wait(5)
        with mock.patch.object(FilesTransport, 'reply', new=staticmethod(paused)):
            try:
                first_result = first()
                self.assertTrue(acknowledged.wait(2))
                self.assertEqual(first_result[0], first_status)
                result = second()
            finally:
                release.set()
                self.assertTrue(self.gateway.wait_idle(3))
        return result

    def test_upload_ack_allows_immediate_model_before_handler_cleanup(self):
        self.create();raw, content_type = multipart()
        result = self.after_ack('POST', '/v1/files',
            lambda: self.request('POST', '/v1/files', raw, {'Content-Type':content_type}, wait=False),
            lambda: self.model(wait=False))
        self.assertEqual(result[0], 200)
        self.assertEqual(len(self.gateway.records), 1)
        self.assertEqual(self.gateway.active_model_handlers, 0)
        self.assertFalse(self.gateway.closing)

    def test_list_ack_allows_immediate_retrieve_before_handler_cleanup(self):
        self.create();self.assertEqual(self.upload()[0], 200)
        result = self.after_ack('GET', '/v1/files',
            lambda: self.request('GET', '/v1/files', wait=False),
            lambda: self.request('GET', '/v1/files/file-api-owned', wait=False))
        self.assertEqual(result[0], 200)
        self.assert_no_model_attempt();self.assertFalse(self.gateway.closing)

    def test_retrieve_ack_allows_immediate_delete_before_handler_cleanup(self):
        self.create();self.assertEqual(self.upload()[0], 200)
        result = self.after_ack('GET', '/v1/files/file-api-owned',
            lambda: self.request('GET', '/v1/files/file-api-owned', wait=False),
            lambda: self.request('DELETE', '/v1/files/file-api-owned', wait=False))
        self.assertEqual(result[0], 200)
        self.assertEqual(self.gateway.files_transport.owned['file-api-owned']['state'], 'deleted')
        self.assert_no_model_attempt();self.assertFalse(self.gateway.closing)

    def test_rejected_upload_ack_allows_immediate_inline_fallback(self):
        self.create();self.reply_status = 413;raw, content_type = multipart()
        result = self.after_ack('POST', '/v1/files',
            lambda: self.request('POST', '/v1/files', raw, {'Content-Type':content_type}, wait=False),
            lambda: self.request('POST', '/v1/messages', json.dumps(body('messages')), wait=False),
            first_status=413)
        self.assertEqual(result[0], 200);self.assertFalse(self.gateway.closing)
        self.assertEqual(self.gateway.files_transport.operations[0]['state'], 'rejected')
        self.assertEqual(len(self.gateway.records), 1)

    def test_delete_ack_allows_immediate_replacement_upload(self):
        self.create();self.assertEqual(self.upload()[0], 200)
        raw, content_type = multipart()
        def replace():
            self.metadata['id'] = 'file-api-replacement'
            return self.request('POST', '/v1/files', raw, {'Content-Type':content_type}, wait=False)
        result = self.after_ack('DELETE', '/v1/files/file-api-owned',
            lambda: self.request('DELETE', '/v1/files/file-api-owned', wait=False), replace)
        self.assertEqual(result[0], 200)
        self.assertEqual(self.gateway.files_transport.owned['file-api-owned']['state'], 'deleted')
        self.assertEqual(self.gateway.files_transport.owned['file-api-replacement']['state'], 'available')
        self.assert_no_model_attempt()

    def test_upstream_close_failure_cannot_follow_a_success_ack(self):
        from scripts.terminal_bench import files_transport
        self.create();original = files_transport.open_http_stream
        def opened(*args, **kwargs):
            stream = original(*args, **kwargs);close = stream.close
            def failed_close(*a, **kw):
                close(*a, **kw)
                raise OSError('private close detail')
            stream.close = failed_close
            return stream
        with mock.patch.object(files_transport, 'open_http_stream', side_effect=opened):
            self.assertEqual(self.upload()[0], 400)
        self.assertTrue(self.gateway.closing);self.assertTrue(self.gateway.files_transport.unverified)
        self.assertNotIn('private close detail', (self.gateway.root/'ledger.json').read_text())
        self.assert_no_model_attempt()

    def test_acknowledged_files_handler_still_counts_toward_shutdown(self):
        self.create();raw, content_type = multipart()
        def check_drain():
            self.assertEqual(self.gateway.active_handlers, 1)
            self.assertEqual(self.gateway.active_model_handlers, 0)
            self.assertFalse(self.gateway.files_transport.busy)
            self.assertFalse(self.gateway.wait_idle(.01))
            self.assertIsNotNone(self.gateway._root_fd)
            return True
        self.assertTrue(self.after_ack('POST', '/v1/files',
            lambda: self.request('POST', '/v1/files', raw, {'Content-Type':content_type}, wait=False), check_drain))

    def test_previous_ack_handler_cannot_release_the_next_files_operation(self):
        from scripts.terminal_bench.files_transport import FilesTransport
        self.create();raw, content_type = multipart()
        first_ack, release_first = threading.Event(), threading.Event()
        original = FilesTransport.reply;results = []
        def paused(handler, status, value):
            original(handler, status, value)
            if value.get('id') == 'file-api-owned' and status == 200:
                first_ack.set();release_first.wait(5)
        with mock.patch.object(FilesTransport, 'reply', new=staticmethod(paused)):
            thread = None
            try:
                self.assertEqual(self.request('POST', '/v1/files', raw, {'Content-Type':content_type}, wait=False)[0], 200)
                self.assertTrue(first_ack.wait(2))
                self.metadata['id'] = 'file-api-second';self.stall = True;self.peer_started.clear()
                thread = threading.Thread(target=lambda: results.append(self.request('POST', '/v1/files',
                    raw, {'Content-Type':content_type}, wait=False)))
                thread.start();self.assertTrue(self.peer_started.wait(2))
                release_first.set()
                with self.gateway.condition:
                    self.assertTrue(self.gateway.condition.wait_for(lambda: self.gateway.active_handlers == 1, timeout=2))
                    self.assertTrue(self.gateway.files_transport.busy)
            finally:
                release_first.set();self.peer_release.set()
                if thread is not None: thread.join(3)
        self.assertTrue(self.gateway.wait_idle(3))
        self.assertEqual(results[0][0], 200)
        self.assertEqual(len(self.gateway.files_transport.owned), 2)
        self.assert_no_model_attempt()

    def test_upload_once_and_three_round_reuse_preserves_model_body_and_budget(self):
        self.create()
        self.assertEqual(self.upload()[0], 200)
        for _ in range(3): self.assertEqual(self.model()[0], 200)
        files = [r for r in self.upstream if r[1].endswith('/files')]
        models = [r for r in self.upstream if r[1].endswith('/messages')]
        self.assertEqual(len(files), 1);self.assertEqual(len(models), 3)
        self.assertEqual(files[0][1], '/anthropic/v1/files')
        self.assertEqual(files[0][3], multipart()[0])
        for record, sent in zip(self.gateway.records, models):
            self.assertEqual(sent[3], (self.gateway.root/(record['id']+'.request.json')).read_bytes())
            self.assertEqual(sent[2]['anthropic-beta'], 'files-api-2025-04-14')
            self.assertEqual(record['call']['bound']['reserved_amount'], '5.242880000000')
        self.assertEqual(len(json.loads((self.budget.root/'ledger.json').read_bytes())['calls']), 3)
        ledger = (self.gateway.root/'ledger.json').read_text()
        self.assertNotIn('host-only-test-key', ledger)
        self.assertNotIn(self.gateway.token, ledger)
        self.assertFalse(json.loads(ledger)['can_resume_agent'])

    def assert_no_model_attempt(self):
        self.assertFalse(self.gateway.records)
        self.assertFalse(json.loads((self.budget.root/'ledger.json').read_bytes())['calls'])
        self.assertFalse(self.budget.snapshot()['unknown_usage'])

    def test_default_off_rejects_unverified_media_before_reservation(self):
        self.create(enabled=False)
        self.assertEqual(self.model()[0], 402)
        self.assert_no_model_attempt();self.assertFalse(self.upstream)

    def test_foreign_auth_cannot_revoke_or_disclose_owned_files(self):
        self.create()
        self.assertEqual(self.request('GET', '/v1/files', headers={'x-api-key':'foreign-token'})[0], 400)
        self.assertFalse(self.gateway.closing)
        self.assertEqual(self.upload()[0], 200)
        self.assertEqual(self.model()[0], 200)

    def test_missing_beta_is_not_forwarded(self):
        self.create()
        self.assertEqual(self.request('GET', '/v1/files', headers={'anthropic-beta':''})[0], 400)
        self.assertTrue(self.gateway.closing);self.assertFalse(self.upstream)
        self.assert_no_model_attempt()

    def test_scoped_listing_retrieve_delete_and_deleted_id_denied(self):
        self.create();self.assertEqual(self.upload()[0], 200)
        status, raw = self.request('GET', '/v1/files?limit=1')
        self.assertEqual(status, 200);self.assertEqual([v['id'] for v in json.loads(raw)['data']], ['file-api-owned'])
        self.assertEqual(len(self.upstream), 1)  # Never list the provider's account.
        self.assertEqual(self.request('GET', '/v1/files/file-api-owned')[0], 200)
        self.assertEqual(self.request('DELETE', '/v1/files/file-api-owned')[0], 200)
        self.assertEqual(json.loads(self.request('GET', '/v1/files')[1])['data'], [])
        self.assertEqual(self.model()[0], 402);self.assert_no_model_attempt()

    def test_cross_trial_id_blocked_without_provider_request(self):
        self.create();self.assertEqual(self.request('DELETE', '/v1/files/file-api-other-trial')[0], 400)
        self.assertFalse(self.upstream);self.assertTrue(self.gateway.closing)

    def test_definite_upload_reject_preserves_native_inline_fallback(self):
        self.create();self.reply_status = 413
        self.assertEqual(self.upload()[0], 413);self.assertFalse(self.gateway.closing)
        value = body('messages')
        self.assertEqual(self.request('POST', '/v1/messages', json.dumps(value))[0], 200)
        self.assertEqual(self.gateway.files_transport.operations[0]['state'], 'rejected')
        self.assertEqual(len(self.gateway.records), 1)

    def test_quota_rejection_keeps_native_classification_without_error_text(self):
        self.create();self.reply_status = 429
        self.reply_body = b'{"error":{"message":"storage quota exceeded; private upstream detail"}}'
        status, raw = self.upload()
        self.assertEqual(status, 429);self.assertIn(b'quota', raw)
        self.assertNotIn(b'private upstream detail', raw)
        self.assertEqual(json.loads(self.request('GET', '/v1/files')[1])['data'], [])
        self.assertFalse(self.gateway.closing);self.assert_no_model_attempt()

    def test_created_status_is_a_valid_upload_acknowledgement(self):
        self.create();self.reply_status = 201
        self.assertEqual(self.upload()[0], 201)
        self.reply_status = 200
        self.assertEqual(self.model()[0], 200)

    def test_files_beta_keeps_other_trusted_host_betas(self):
        self.create();self.gateway.headers['Anthropic-Beta'] = 'trusted-feature'
        self.assertEqual(self.upload()[0], 200);self.assertEqual(self.model()[0], 200)
        for request in self.upstream:
            self.assertEqual(request[2]['anthropic-beta'], 'trusted-feature,files-api-2025-04-14')

    def test_redirect_is_not_followed_and_is_not_budget_pause(self):
        self.create();self.reply_status = 307;self.reply_body = b'provider-key-must-not-leak'
        self.assertEqual(self.upload()[0], 400)
        self.assertEqual(len(self.upstream), 1);self.assertTrue(self.gateway.closing)
        row = self.gateway.files_transport.operations[0]
        self.assertEqual(row['error'], 'files_upstream_unverified')
        self.assertNotIn('provider-key-must-not-leak', (self.gateway.root/'ledger.json').read_text())
        self.assert_no_model_attempt()

    def test_malformed_metadata_and_duplicate_json_keys_block_dispatch(self):
        self.create();self.reply_body = b'{"id":"file-api-owned","id":"file-api-other"}'
        self.assertEqual(self.upload()[0], 400)
        self.assertEqual(self.model()[0], 402);self.assert_no_model_attempt()
        self.assertFalse(self.gateway.files_transport.owned)

    def test_upload_size_or_filename_mismatch_does_not_create_owned_id(self):
        self.create();self.metadata['size_bytes'] += 1
        self.assertEqual(self.upload()[0], 400)
        self.assertFalse(self.gateway.files_transport.owned)
        self.assertEqual(self.gateway.files_transport.operations[0]['error'], 'files_upload_unverified')

    def test_response_size_limit_blocks_metadata_and_later_model(self):
        self.create();self.reply_body = b' ' * 65537
        self.assertEqual(self.upload()[0], 400)
        self.assertEqual(self.model()[0], 402);self.assert_no_model_attempt()
        self.assertEqual(self.gateway.files_transport.operations[0]['error'], 'files_response_bound')

    def test_truncated_response_is_not_accepted_even_with_complete_json(self):
        self.create();self.extra_reply_length = 1
        self.assertEqual(self.upload()[0], 400)
        self.assertEqual(self.gateway.files_transport.operations[0]['error'], 'files_response_truncated')
        self.assertFalse(self.gateway.files_transport.owned)

    def test_upload_bound_rejected_without_reading_or_forwarding_body(self):
        from scripts.terminal_bench.files_transport import MAX_MULTIPART_BYTES
        self.create()
        self.assertEqual(self.request('POST', '/v1/files', b'', {'Content-Length':str(MAX_MULTIPART_BYTES+1)})[0], 400)
        self.assertFalse(self.upstream);self.assert_no_model_attempt()

    def test_expired_owned_file_is_not_dispatchable(self):
        self.create();self.assertEqual(self.upload()[0], 200)
        self.gateway.files_transport.owned['file-api-owned']['expires_at'] = time.time()-1
        self.assertEqual(self.model()[0], 402);self.assert_no_model_attempt()

    def test_responses_media_id_is_blocked_without_reservation(self):
        self.create(enabled=False, protocol='responses')
        value = body('responses');value['input'] = [{'role':'user', 'content':[{'type':'input_image', 'file_id':'file-api-other'}]}]
        status, _ = self.request('POST', '/responses', json.dumps(value), {'Authorization':'Bearer '+self.gateway.token})
        self.assertEqual(status, 402);self.assert_no_model_attempt();self.assertFalse(self.upstream)

    def test_malformed_path_cannot_select_an_upstream_destination(self):
        self.create()
        self.assertEqual(self.request('GET', '/v1/files/file-api-owned%2f..%2fother')[0], 400)
        self.assertFalse(self.upstream);self.assert_no_model_attempt()

    def test_guard_failure_and_capture_failure_are_zero_upstream(self):
        self.create()
        def denied(method): raise RuntimeError('private credential text')
        self.gateway.files_transport.before_request = denied
        self.assertEqual(self.upload()[0], 400)
        self.assertFalse(self.upstream);self.assertTrue(self.gateway.closing)
        self.assertNotIn('private credential text', (self.gateway.root/'ledger.json').read_text())

    def test_publication_failure_prevents_upload(self):
        self.create()
        with mock.patch.object(self.gateway, '_write_ledger', side_effect=OSError('disk failed')):
            self.assertEqual(self.upload()[0], 400)
        self.assertFalse(self.upstream);self.assertTrue(self.gateway.accounting_error)
        self.assert_no_model_attempt()

    def test_not_started_native_cannot_upload(self):
        self.create();self.gateway.require_agent_start = True
        self.assertEqual(self.upload()[0], 400);self.assertFalse(self.upstream)
        self.assert_no_model_attempt()

    def test_shutdown_interrupts_upload_waiting_for_headers_and_blocks_later_dispatch(self):
        self.create();self.stall = True
        results = []
        thread = threading.Thread(target=lambda: results.append(self.upload()));thread.start()
        self.assertTrue(self.peer_started.wait(2))
        self.gateway.client_ended('cancelled')
        began = time.monotonic();self.gateway.close();thread.join(3)
        self.assertFalse(thread.is_alive());self.assertLess(time.monotonic()-began, 2)
        self.assertEqual(results[0][0], 400)
        self.assertEqual(self.gateway.files_transport.operations[0]['state'], 'unverified')
        self.assert_no_model_attempt()

    def test_concurrent_delete_cannot_invalidate_an_admitted_model_request(self):
        self.create();self.assertEqual(self.upload()[0], 200)
        self.peer_started.clear();self.stall = True
        results = []
        thread = threading.Thread(target=lambda: results.append(self.model()));thread.start()
        self.assertTrue(self.peer_started.wait(2))
        self.assertEqual(self.request('DELETE', '/v1/files/file-api-owned', wait=False)[0], 400)
        self.assertFalse(any(r[0] == 'DELETE' for r in self.upstream))
        self.peer_release.set();thread.join(3)
        self.assertEqual(self.gateway.files_transport.owned['file-api-owned']['state'], 'available')

    def test_guard_cannot_bypass_model_reservation_after_upload(self):
        self.create();self.assertEqual(self.upload()[0], 200)
        def denied(request): raise RuntimeError('no paid authorization')
        self.gateway.before_admit = denied
        self.assertEqual(self.model()[0], 402);self.assert_no_model_attempt()
        self.assertEqual(len(self.upstream), 1)

    def test_single_trial_transport_cannot_be_rebound(self):
        from scripts.terminal_bench.files_transport import FilesStop
        self.create()
        with self.assertRaises(FilesStop): self.gateway.files_transport.bind('messages')

    def test_tool_argument_named_file_id_is_not_media_reference(self):
        self.create(enabled=False)
        value = body('messages')
        value['messages'] = [{'role':'assistant', 'content':[{'type':'tool_use', 'id':'tool',
                             'name':'read', 'input':{'file_id':'ordinary-project-field'}}]}]
        self.assertEqual(self.request('POST', '/v1/messages', json.dumps(value))[0], 200)

    def test_duplicate_length_and_chunked_upload_never_forward(self):
        self.create()
        conn = http.client.HTTPConnection('127.0.0.1', self.gateway.server.server_port, timeout=3)
        try:
            conn.putrequest('POST', '/v1/files')
            for key, value in [('x-api-key', self.gateway.token), ('anthropic-version', '2023-06-01'),
                               ('anthropic-beta', 'files-api-2025-04-14'),
                               ('Content-Length', '1'), ('Content-Length', '1'), ('Transfer-Encoding', 'chunked')]:
                conn.putheader(key, value)
            conn.endheaders();self.assertEqual(conn.getresponse().status, 400)
        finally: conn.close()
        self.assertTrue(self.gateway.wait_idle(3));self.assertFalse(self.upstream)


class MultipartTests(unittest.TestCase):
    def test_rejects_bad_image_traversal_duplicate_fields_and_missing_boundary_end(self):
        from scripts.terminal_bench.files_transport import FilesStop, upload_metadata
        for raw, content_type in (multipart(b'not-an-image'), multipart(filename='../image.png'),
                                  (multipart()[0][:-4], multipart()[1]),
                                  (multipart()[0].replace(b'--test-upload-boundary--', multipart()[0][:-26]+b'--test-upload-boundary--'), multipart()[1])):
            with self.subTest(raw_length=len(raw)):
                with self.assertRaises(FilesStop): upload_metadata(raw, content_type)


if __name__ == '__main__': unittest.main()
