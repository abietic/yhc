"""Native HTTP surface and durable receipt invariants; local peers only."""

import hashlib
import http.client
import json
import os
import tempfile
import threading
import time
import unittest
from types import SimpleNamespace
from pathlib import Path
from unittest import mock

from scripts.terminal_bench.fresh_budget import FreshBudget, digest
from scripts.terminal_bench.fresh_gateway import FreshGateway, GatewayStop
from scripts.terminal_bench import test_budgeted_stream as stream_fixture
from scripts.terminal_bench.test_fresh_budget import body, plan


@unittest.skipUnless(os.name == 'posix', 'private budget requires POSIX')
class GatewayTests(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory();self.addCleanup(self.tmp.cleanup)
        self.root = Path(self.tmp.name).resolve()
        self.connections, self.guards = [], []

    def create(self, protocol='responses', max_calls=200, raw=None, guard=None):
        value = plan((protocol,), cap='6', sample_cap='6');value['max_provider_calls'] = max_calls
        budget = FreshBudget.create(self.root/'budget', value, digest(value));budget.claim_sample('sample-0')
        self.addCleanup(budget.close)
        fixture = stream_fixture.ForwardingTests()
        fixture.connections = self.connections
        def connection():
            return fixture.fixture_connection(raw or (stream_fixture.response_events() if protocol == 'responses' else stream_fixture.message_events()))
        def allowed(request):
            self.guards.append(request['model'])
            if guard is not None: guard(request)
        gateway = FreshGateway(self.root/'wire', budget, 'sample-0', protocol,
            connection_factory=connection, headers={}, before_admit=allowed,
            deadline=time.monotonic()+30, drain_timeout=.2).start()
        self.addCleanup(gateway.close)
        return budget, gateway

    def post(self, gateway, payload=None, token=None, extra=None):
        connection = http.client.HTTPConnection('127.0.0.1', gateway.server.server_port, timeout=3)
        headers = {'Content-Type':'application/json'}
        if gateway.protocol == 'responses': headers['Authorization'] = 'Bearer '+(token or gateway.token)
        else: headers['x-api-key'] = token or gateway.token
        headers.update(extra or {})
        try:
            connection.request('POST','/responses' if gateway.protocol == 'responses' else '/v1/messages',
                               payload or json.dumps(body(gateway.protocol)).encode(), headers)
            response = connection.getresponse();status, raw = response.status, response.read()
        finally: connection.close()
        self.assertTrue(gateway.wait_idle(3))
        return status, raw

    def test_native_routes_share_one_durable_bound_and_closed_usage(self):
        budget, gateway = self.create()
        status, raw = self.post(gateway)
        self.assertEqual(status,200);self.assertEqual(raw,stream_fixture.response_events())
        saved = json.loads((gateway.root/'ledger.json').read_bytes())
        row = saved['records'][0]
        request = (gateway.root/(row['id']+'.request.json')).read_bytes()
        capture = (gateway.root/(row['id']+'.response.sse')).read_bytes()
        self.assertEqual(hashlib.sha256(request).hexdigest(),row['call']['bound']['body_sha256'])
        self.assertEqual(hashlib.sha256(capture).hexdigest(),row['response_sha256'])
        self.assertEqual(row['state'],'known');self.assertTrue(row['usage_known'])
        self.assertEqual(row['known_cost'],'0.000203200000')
        self.assertEqual(row['thinking_tokens'],8)
        self.assertFalse(saved['accounting_error']);self.assertFalse(saved['can_resume_agent'])
        self.assertEqual(self.connections[0].request.call_args.args[2],request)
        self.assertEqual(self.guards,['deepseek-flash'])
        self.assertEqual(budget.snapshot()['known_cost'],row['known_cost'])

    def test_messages_receipt_keeps_missing_thinking_unknown(self):
        budget, gateway = self.create('messages')
        self.assertEqual(self.post(gateway)[0],200)
        row=gateway.records[0]
        self.assertIsNone(row['thinking_tokens'])
        self.assertEqual(row['call']['bound']['reserved_amount'],'5.242880000000')
        self.assertEqual(row['input_tokens'],100)

    def test_foreign_authentication_cannot_revoke_the_owned_session(self):
        budget, gateway = self.create()
        self.assertEqual(self.post(gateway,token='sentinel-provider-credential')[0],402)
        self.assertFalse(gateway.closing)
        self.assertEqual(self.post(gateway)[0],200)
        self.assertEqual(len(self.connections),1)

    def test_guard_and_not_started_native_are_zero_attempt_denials(self):
        budget, gateway = self.create()
        gateway.require_agent_start=True
        self.assertEqual(self.post(gateway)[0],402)
        self.assertFalse(self.connections)
        self.assertFalse(json.loads((budget.root/'ledger.json').read_bytes())['calls'])
        self.assertEqual(gateway.denials[-1]['error'],'agent_not_running')

    def test_call_limit_retains_closed_cost_and_opens_no_second_upstream(self):
        budget, gateway = self.create(max_calls=1)
        self.assertEqual(self.post(gateway)[0],200)
        self.assertEqual(self.post(gateway)[0],402)
        self.assertEqual(len(self.connections),1)
        self.assertEqual(len(gateway.records),1)
        self.assertEqual(gateway.denials[-1]['error'],'sample_call_limit')
        self.assertEqual(budget.snapshot()['known_cost'],'0.000203200000')
        self.assertFalse(budget.snapshot()['unknown_usage'])

    def test_missing_terminal_keeps_full_liability_and_no_later_attempt(self):
        budget, gateway = self.create(raw=stream_fixture.response_events().split(b'event: response.completed')[0])
        self.post(gateway)
        self.assertEqual(gateway.records[0]['state'],'unknown')
        self.assertEqual(budget.snapshot()['retained'],'2.621440000000')
        self.assertEqual(self.post(gateway)[0],402)
        self.assertEqual(len(self.connections),1)

    def test_request_capture_failure_prevents_upstream_and_stops_accounting(self):
        budget, gateway = self.create()
        original=gateway._open_file
        def failed(name):
            if name.endswith('.request.json'): raise OSError('private storage failure')
            return original(name)
        with mock.patch.object(gateway,'_open_file',side_effect=failed): self.post(gateway)
        self.assertFalse(self.connections)
        self.assertTrue(gateway.accounting_error)
        self.assertTrue(budget.snapshot()['unknown_usage'])
        self.assertEqual(budget.snapshot()['retained'],'2.621440000000')
        self.assertEqual(gateway.active_handlers,0)

    def test_native_end_revokes_new_admission_and_close_is_idempotent(self):
        budget,gateway=self.create()
        gateway.client_ended('completed')
        self.assertEqual(self.post(gateway)[0],402)
        self.assertFalse(self.connections)
        gateway.close();gateway.close()
        self.assertFalse(gateway.thread.is_alive())

    def test_inspection_is_a_copy_not_funding_or_resume_authority(self):
        budget,gateway=self.create();self.post(gateway)
        call=budget.inspect_call(gateway.records[0]['id'])
        self.assertFalse(call['can_resume_agent'])
        call['call']['known_cost']='0'
        self.assertEqual(budget.snapshot()['known_cost'],'0.000203200000')

    def test_guard_must_not_rewrite_native_model_input(self):
        def rewrite(request): request['input']='guard-rewritten-input'
        budget,gateway=self.create(guard=rewrite)
        self.assertEqual(self.post(gateway)[0],402)
        self.assertFalse(self.connections)
        self.assertEqual(gateway.denials[-1]['error'],'request_guard_mutated_body')

    def test_external_guard_exception_text_is_not_evidence(self):
        def failed(request): raise GatewayStop('sentinel-provider-credential')
        budget,gateway=self.create(guard=failed)
        self.assertEqual(self.post(gateway)[0],402)
        self.assertEqual(gateway.denials[-1]['error'],'admission_guard_failed')
        self.assertNotIn('sentinel-provider-credential',(gateway.root/'ledger.json').read_text())

    def test_foreign_denials_are_bounded_without_revoking_admission(self):
        budget,gateway=self.create()
        for _ in range(130): self.assertEqual(self.post(gateway,token='sentinel-provider-credential')[0],402)
        self.assertEqual(len(gateway.denials),128)
        self.assertEqual(gateway.denials_dropped,2)
        self.assertEqual(self.post(gateway)[0],200)

    def test_final_wire_publication_failure_keeps_known_spend_but_blocks_proof(self):
        budget,gateway=self.create();original=gateway._write_ledger
        def fail_final(value):
            if any(r['state']=='known' for r in value['records']): raise OSError('private storage failure')
            original(value)
        with mock.patch.object(gateway,'_write_ledger',side_effect=fail_final): self.post(gateway)
        self.assertEqual(gateway.active_handlers,0)
        self.assertTrue(gateway.accounting_error)
        self.assertEqual(budget.snapshot()['known_cost'],'0.000203200000')
        self.assertEqual(json.loads((gateway.root/'ledger.json').read_bytes())['records'][0]['state'],'reserved')
        self.assertEqual(self.post(gateway)[0],402)

    def test_capture_close_failure_cannot_strand_the_handler(self):
        budget,gateway=self.create();original=gateway._open_file
        class Capture:
            def __init__(self,f):self.file=f
            def __getattr__(self,name):return getattr(self.file,name)
            def close(self):self.file.close();raise OSError('private close detail')
        def opened(name):
            f=original(name)
            return Capture(f) if name.endswith('.response.sse') else f
        with mock.patch.object(gateway,'_open_file',side_effect=opened):self.post(gateway)
        self.assertEqual(gateway.active_handlers,0)
        self.assertTrue(gateway.accounting_error)
        gateway.close()
        self.assertEqual(budget.snapshot()['known_cost'],'0.000203200000')

    def test_native_cancel_drains_active_stream_and_retains_unknown_liability(self):
        budget,gateway=self.create()
        ready,release=threading.Event(),threading.Event()
        fixture=stream_fixture.ForwardingTests();fixture.connections=self.connections
        connection=fixture.fixture_connection(b'')
        def blocked(size):ready.set();release.wait(3);return b''
        connection.getresponse.return_value.read1=blocked
        connection.sock.shutdown.side_effect=lambda *args:release.set()
        gateway.connection_factory=lambda:connection
        errors=[]
        def client():
            try:self.post(gateway)
            except Exception as exc:errors.append(type(exc).__name__)
        thread=threading.Thread(target=client)
        thread.start()
        try:
            self.assertTrue(ready.wait(3))
            gateway.client_ended('cancelled');gateway.close()
        finally:release.set();thread.join(3)
        self.assertFalse(thread.is_alive());self.assertFalse(errors)
        self.assertEqual(budget.snapshot()['retained'],'2.621440000000')
        diagnostic=gateway.records[0]['stream_diagnostics']
        self.assertEqual(diagnostic['client_end']['reason'],'cancelled')
        self.assertEqual(diagnostic['timeout_reason'],'shutdown_drain')

    def test_preexisting_symlink_or_directory_is_not_reopened(self):
        target=self.root/'foreign';target.mkdir()
        (self.root/'wire').symlink_to(target,target_is_directory=True)
        with self.assertRaises(FileExistsError):self.create()
        self.assertEqual(list(target.iterdir()),[])

    def test_runtime_directory_replacement_cannot_redirect_capture(self):
        budget,gateway=self.create();foreign=self.root/'foreign';foreign.mkdir()
        gateway.root.rename(self.root/'moved-wire');gateway.root.symlink_to(foreign,target_is_directory=True)
        self.assertEqual(self.post(gateway)[0],402)
        self.assertEqual(list(foreign.iterdir()),[])
        self.assertFalse(self.connections)
        self.assertFalse(json.loads((budget.root/'ledger.json').read_bytes())['calls'])

    def test_root_mode_change_blocks_before_reservation(self):
        budget,gateway=self.create();gateway.root.chmod(0o755)
        self.assertEqual(self.post(gateway)[0],402)
        self.assertFalse(self.connections)
        self.assertTrue(gateway.accounting_error)
        self.assertFalse(json.loads((budget.root/'ledger.json').read_bytes())['calls'])

    def test_root_owner_mismatch_is_rejected_and_latched(self):
        budget,gateway=self.create();original=Path.lstat;st=gateway.root.lstat()
        def changed(path,*args,**kwargs):
            if path==gateway.root:return SimpleNamespace(st_mode=st.st_mode,st_uid=st.st_uid+1,st_dev=st.st_dev,st_ino=st.st_ino)
            return original(path,*args,**kwargs)
        with mock.patch.object(Path,'lstat',changed):
            with self.assertRaisesRegex(GatewayStop,'gateway_directory_changed'):gateway.persist()
        self.assertTrue(gateway.accounting_error)
        self.assertEqual(self.post(gateway)[0],402)
        self.assertFalse(self.connections)

    def test_replacement_during_guard_has_no_redirect_or_upstream(self):
        foreign=self.root/'foreign';foreign.mkdir()
        def replaced(request):
            path=self.root/'wire';path.rename(self.root/'moved-wire');path.symlink_to(foreign,target_is_directory=True)
        budget,gateway=self.create(guard=replaced)
        self.assertEqual(self.post(gateway)[0],402)
        self.assertFalse(self.connections)
        self.assertEqual(list(foreign.iterdir()),[])
        self.assertEqual(budget.snapshot()['retained'],'2.621440000000')


if __name__=='__main__': unittest.main()
