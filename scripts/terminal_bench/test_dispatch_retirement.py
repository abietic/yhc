"""Offline receipts and irreversible NEW-cohort dispatch retirement."""

import copy
import hashlib
import http.client
import http.server
import json
import os
import select
import tempfile
import threading
import time
import unittest
from pathlib import Path
from unittest import mock

from scripts.terminal_bench.fresh_budget import BudgetStop, FreshBudget, digest
from scripts.terminal_bench.test_fresh_budget import body, plan
from scripts.terminal_bench import test_fresh_gateway as gateway_fixture
from scripts.terminal_bench import test_budgeted_stream as stream_fixture
from scripts.terminal_bench.fresh_gateway import FreshGateway
from scripts.terminal_bench.no_dispatch import ReceiptError, request_binding, verify_receipt, verify_stored_receipt
from scripts.terminal_bench.cohort_retirement import verify_retirement, project_funding


@unittest.skipUnless(os.name == 'posix', 'private gateway requires POSIX')
class ReceiptTests(unittest.TestCase):
    setUp = gateway_fixture.GatewayTests.setUp
    post = gateway_fixture.GatewayTests.post

    def create(self, *args, **kwargs):
        budget, gateway = gateway_fixture.GatewayTests.create(self,*args, **kwargs)
        # Fixture key belongs to the host verifier, never to the native client.
        gateway.receipt_key = bytes(range(32))
        return budget, gateway

    def denied(self, gateway, payload=None, nonce='a'*64):
        raw = payload or json.dumps(body(gateway.protocol), indent=2).encode()
        expected = request_binding(gateway.budget.plan_sha256, gateway.sample, gateway.protocol, raw, nonce)
        status, response = self.post(gateway, payload=raw, extra={'X-YHC-Request-Nonce':nonce})
        self.assertEqual(status, 402)
        envelope = json.loads(response)['no_dispatch_receipt']
        saved = json.loads((gateway.root/'ledger.json').read_bytes())
        ref = saved['denials'][-1]['receipt']
        verified = verify_stored_receipt(gateway.root, ref, gateway.receipt_key, expected)
        self.assertEqual(verified['request_body_sha256'], hashlib.sha256(raw).hexdigest())
        self.assertFalse(verified['can_resume_agent'])
        self.assertFalse(verified['funding_release_authorized'])
        return envelope, expected, ref

    def test_verified_call_limit_denial_has_exact_original_bytes_and_no_second_upstream(self):
        budget, gateway = self.create(max_calls=1)
        self.assertEqual(self.post(gateway)[0], 200)
        envelope, expected, ref = self.denied(gateway)
        self.assertEqual(verify_receipt(envelope, gateway.receipt_key, expected)['reason'], 'sample_call_limit')
        self.assertEqual(len(self.connections), 1)
        self.assertEqual(len(budget._state['calls']), 1)
        self.assertFalse(budget.snapshot()['unknown_usage'])
        self.assertEqual(json.loads((gateway.root/ref['path']).read_bytes()), envelope)

    def test_messages_uses_the_same_request_bound_host_receipt(self):
        budget, gateway = self.create('messages', max_calls=1)
        self.assertEqual(self.post(gateway)[0], 200)
        self.denied(gateway)
        self.assertEqual(len(self.connections), 1)

    def test_receipt_cannot_be_replayed_for_other_nonce_body_scope_or_protocol(self):
        budget, gateway = self.create(max_calls=1)
        self.post(gateway)
        envelope, expected, ref = self.denied(gateway)
        for key, value in [('nonce','b'*64), ('request_body_sha256','b'*64), ('sample','other'),
                           ('plan_sha256','b'*64), ('protocol','messages'), ('path','/v1/messages'), ('model','other')]:
            changed = dict(expected, **{key:value})
            with self.subTest(key=key), self.assertRaises(ReceiptError):
                verify_receipt(envelope, gateway.receipt_key, changed)
        for changed in (dict(envelope, mac='0'*64), dict(envelope, extra=True)):
            with self.assertRaises(ReceiptError): verify_receipt(changed, gateway.receipt_key, expected)
        with self.assertRaises(ReceiptError): verify_receipt(envelope, b'z'*32, expected)

    def test_unbound_http_error_is_never_a_no_dispatch_receipt(self):
        budget, gateway = self.create(max_calls=1)
        self.post(gateway)
        status, raw = self.post(gateway)
        self.assertEqual(status, 402)
        self.assertNotIn('no_dispatch_receipt', json.loads(raw))
        self.assertNotIn('receipt', gateway.denials[-1])

    def test_reserved_storage_failure_and_upstream_failure_never_issue_a_receipt(self):
        budget, gateway = self.create()
        original = gateway._open_file
        def fail(name):
            if name.endswith('.request.json'): raise OSError('fixture')
            return original(name)
        with mock.patch.object(gateway, '_open_file', side_effect=fail):
            status, raw = self.post(gateway, extra={'X-YHC-Request-Nonce':'a'*64})
        self.assertNotIn('no_dispatch_receipt', json.loads(raw))
        self.assertEqual(budget.snapshot()['retained'], '2.621440000000')
        self.assertFalse(list(gateway.root.glob('*.no-dispatch.json')))

    def test_receipt_publication_fault_does_not_emit_uncommitted_proof(self):
        budget, gateway = self.create(max_calls=1)
        self.post(gateway)
        original = gateway._write_ledger
        def fail(value):
            if any('receipt' in row for row in value['denials']): raise OSError('fixture')
            return original(value)
        with mock.patch.object(gateway, '_write_ledger', side_effect=fail):
            status, raw = self.post(gateway, extra={'X-YHC-Request-Nonce':'a'*64})
        self.assertNotIn('no_dispatch_receipt', json.loads(raw))
        self.assertTrue(gateway.accounting_error)
        self.assertEqual(len(self.connections), 1)

    def test_stored_receipt_rejects_swapped_hardlinked_or_nonprivate_files(self):
        budget, gateway = self.create(max_calls=1);self.post(gateway)
        envelope, expected, ref = self.denied(gateway)
        path = gateway.root/ref['path']
        original = path.read_bytes()
        path.write_bytes(original+b' ')
        with self.assertRaises(ReceiptError): verify_stored_receipt(gateway.root, ref, gateway.receipt_key, expected)
        path.write_bytes(original);path.chmod(0o644)
        with self.assertRaises(ReceiptError): verify_stored_receipt(gateway.root, ref, gateway.receipt_key, expected)
        path.chmod(0o600);os.link(path, gateway.root/'alias')
        with self.assertRaises(ReceiptError): verify_stored_receipt(gateway.root, ref, gateway.receipt_key, expected)

    def test_upstream_error_has_no_release_receipt(self):
        budget, gateway = self.create()
        with mock.patch.object(gateway,'connection_factory',side_effect=OSError('fixture')):
            status, raw = self.post(gateway,extra={'X-YHC-Request-Nonce':'a'*64})
        self.assertNotIn('no_dispatch_receipt',json.loads(raw))
        self.assertEqual(budget.snapshot()['retained'],'2.621440000000')
        self.assertTrue(gateway.records[0]['upstream_attempted'])

    def test_forged_upstream_false_and_wrong_types_never_verify(self):
        budget,gateway = self.create(max_calls=1);self.post(gateway)
        envelope,expected,ref = self.denied(gateway)
        for key,value in [('version',True),('upstream_attempted',True),('reason',{}),('receipt_id','invalid')]:
            changed=copy.deepcopy(envelope);changed['payload'][key]=value
            with self.subTest(key=key),self.assertRaises(ReceiptError):
                verify_receipt(changed,gateway.receipt_key,expected)

    def test_malformed_nonce_never_becomes_an_authenticated_receipt(self):
        budget,gateway=self.create(max_calls=1);self.post(gateway)
        status,raw=self.post(gateway,extra={'X-YHC-Request-Nonce':'not-a-nonce'})
        self.assertNotIn('no_dispatch_receipt',json.loads(raw))
        self.assertNotIn('receipt',gateway.denials[-1])

    def test_real_loopback_entry_closes_usage_then_rejects_without_a_second_send(self):
        # Both protocol routes use real HTTP/SSE peers. No provider hostname,
        # credential lookup, task workspace or native agent is involved.
        for protocol in ('responses','messages'):
            with self.subTest(protocol=protocol):
                root=self.root/protocol;root.mkdir()
                value=plan((protocol,'responses'),cap='12',sample_cap='6');value['max_provider_calls']=1
                with FreshBudget.create(root/'budget',value,digest(value)) as budget:
                    budget.claim_sample('sample-0');requests=[]
                    raw=stream_fixture.response_events() if protocol == 'responses' else stream_fixture.message_events()
                    class Peer(http.server.BaseHTTPRequestHandler):
                        def log_message(self,*args): pass
                        def do_POST(self):
                            requests.append((self.path,self.rfile.read(int(self.headers['Content-Length']))))
                            self.send_response(200);self.send_header('Content-Type','text/event-stream')
                            self.send_header('Content-Length',str(len(raw)));self.end_headers();self.wfile.write(raw)
                    server=http.server.ThreadingHTTPServer(('127.0.0.1',0),Peer)
                    thread=threading.Thread(target=server.serve_forever,kwargs={'poll_interval':.01},daemon=True);thread.start()
                    try:
                        gateway=FreshGateway(root/'wire',budget,'sample-0',protocol,headers={},before_admit=lambda body:None,
                            connection_factory=lambda:http.client.HTTPConnection('127.0.0.1',server.server_port,timeout=2),
                            receipt_key=bytes(range(32)),deadline=time.monotonic()+20,drain_timeout=.2).start()
                        try:
                            self.assertEqual(self.post(gateway)[0],200)
                            self.denied(gateway)
                            self.assertEqual(len(requests),1)
                            self.assertEqual(requests[0][0],'/responses' if protocol == 'responses' else '/anthropic/v1/messages')
                            self.assertFalse(budget.snapshot()['unknown_usage'])
                            budget.finish_sample('sample-0','budget_paused')
                            proof=budget.retire_dispatch()
                            self.assertTrue(proof['dispatch_retired'])
                            self.assertEqual(proof['unstarted_samples'],['sample-1'])
                            self.assertFalse(proof['native_joint_closure_verified'])
                        finally:gateway.close()
                    finally:server.shutdown();server.server_close();thread.join(2)


@unittest.skipUnless(os.name == 'posix', 'private budget requires POSIX')
class RetirementTests(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory();self.addCleanup(self.tmp.cleanup)
        self.root = Path(self.tmp.name).resolve()
        self.value = plan();self.anchor = digest(self.value)
        self.budget = FreshBudget.create(self.root/'budget', self.value, self.anchor)
        self.addCleanup(self.budget.close)

    def close_first(self):
        self.budget.claim_sample('sample-0')
        call = self.budget.reserve('sample-0', body())
        self.budget.settle(call.call_id, input_tokens=100, cached_input_tokens=32, output_tokens=20)
        self.budget.finish_sample('sample-0', 'budget_paused')

    def test_partial_retirement_partitions_inventory_and_proves_exact_unused_cap(self):
        self.close_first()
        result = self.budget.retire_dispatch()
        ledger = json.loads((self.budget.root/'ledger.json').read_bytes())
        proof = verify_retirement(self.value, self.anchor, ledger, digest(ledger))
        self.assertEqual(result, proof)
        self.assertEqual(proof['started_samples'], ['sample-0'])
        self.assertEqual(proof['unstarted_samples'], ['sample-1'])
        self.assertEqual(proof['known_cost'], '0.000297280000')
        self.assertEqual(proof['unused_cap'], '6.599702720000')
        self.assertTrue(proof['dispatch_retired'])
        self.assertFalse(proof['funding_release_authorized'])
        self.assertFalse(proof['can_resume_agent'])
        for operation in (lambda:self.budget.claim_sample('sample-1'), lambda:self.budget.reserve('sample-0',body()),
                          self.budget.retire_dispatch):
            with self.assertRaises(BudgetStop): operation()
        self.budget.close()
        with self.assertRaises(FileExistsError): FreshBudget.create(self.budget.root, self.value, self.anchor)

    def test_running_unknown_or_outstanding_samples_cannot_retire(self):
        self.budget.claim_sample('sample-0')
        with self.assertRaises(BudgetStop): self.budget.retire_dispatch()
        call = self.budget.reserve('sample-0', body())
        with self.assertRaises(BudgetStop): self.budget.retire_dispatch()
        self.budget.mark_unknown(call.call_id);self.budget.finish_sample('sample-0','unknown_usage')
        with self.assertRaises(BudgetStop): self.budget.retire_dispatch()
        self.assertNotIn('retirement', json.loads((self.budget.root/'ledger.json').read_bytes()))

    def test_failure_before_or_after_publish_keeps_owner_unusable_and_fails_closed(self):
        self.close_first()
        original = self.budget._persist
        def fail():
            original();raise OSError('uncertain publication')
        with mock.patch.object(self.budget,'_persist',side_effect=fail):
            with self.assertRaises(OSError): self.budget.retire_dispatch()
        with self.assertRaises(BudgetStop): self.budget.claim_sample('sample-1')

    @unittest.skipUnless(hasattr(os,'fork'), 'requires POSIX fork')
    def test_fork_inherited_owner_cannot_write_or_acquire_the_copied_mutex(self):
        self.close_first()
        reader,writer=os.pipe()
        # Hold the lock in a DIFFERENT thread while forking: the child must
        # reject its PID before trying to acquire that permanently held copy.
        locked,release=threading.Event(),threading.Event()
        def hold():
            with self.budget._mutex:locked.set();release.wait(5)
        thread=threading.Thread(target=hold);thread.start();self.assertTrue(locked.wait(2))
        child=os.fork()
        if child == 0:
            os.close(reader)
            try:
                operations=[lambda:self.budget.claim_sample('sample-1'),lambda:self.budget.reserve('sample-0',body()),
                            lambda:self.budget.mark_unknown('unknown'),lambda:self.budget.settle('unknown',input_tokens=0,cached_input_tokens=0,output_tokens=0),
                            lambda:self.budget.finish_sample('sample-0','completed'),self.budget.retire_dispatch,
                            self.budget.snapshot,lambda:self.budget.inspect_call('unknown'),self.budget._persist]
                outcomes=[]
                for operation in operations:
                    try:operation();outcomes.append('unexpected-success')
                    except BudgetStop as exc:outcomes.append(str(exc))
                self.budget.close()  # Child descriptors only; never unlock the parent's flock.
                os.write(writer,json.dumps(outcomes).encode())
            finally:os._exit(0)
        os.close(writer)
        try:
            self.assertTrue(select.select([reader],[],[],3)[0], 'forked owner blocked on inherited mutex')
            outcomes=json.loads(os.read(reader,4096))
            self.assertEqual(outcomes,['budget_owner_process_changed']*9)
        finally:
            release.set();thread.join(3);os.close(reader)
            waited,status=os.waitpid(child,os.WNOHANG)
            if not waited:os.kill(child,9);os.waitpid(child,0)
        import fcntl
        other=os.open(self.budget.root/'owner.lock',os.O_WRONLY)
        try:
            with self.assertRaises(BlockingIOError):fcntl.flock(other,fcntl.LOCK_EX|fcntl.LOCK_NB)
        finally:os.close(other)
        proof=self.budget.retire_dispatch()
        self.assertTrue(proof['dispatch_retired'])
        self.assertEqual(json.loads((self.budget.root/'ledger.json').read_bytes())['retirement']['retirement_id'],proof['retirement_id'])

    def test_verifier_rejects_unretired_rewritten_duplicated_and_understated_usage(self):
        self.close_first()
        live = json.loads((self.budget.root/'ledger.json').read_bytes())
        with self.assertRaises(BudgetStop): verify_retirement(self.value,self.anchor,live,digest(live))
        self.budget.retire_dispatch()
        ledger = json.loads((self.budget.root/'ledger.json').read_bytes())
        cases = []
        for mutate in (lambda d:d['retirement']['unstarted_samples'].clear(),
                       lambda d:d['calls'].append(copy.deepcopy(d['calls'][0])),
                       lambda d:d['calls'][0].update(known_cost='0'),
                       lambda d:d['samples']['sample-1'].update(status='pending'),
                       lambda d:d.update(unknown_usage=True)):
            changed = copy.deepcopy(ledger);mutate(changed);cases.append(changed)
        for changed in cases:
            with self.assertRaises(BudgetStop): verify_retirement(self.value,self.anchor,changed,digest(changed))
        with self.assertRaises(BudgetStop): verify_retirement(self.value,self.anchor,ledger,'0'*64)

    def funding(self):
        self.close_first();self.budget.retire_dispatch()
        ledger=json.loads((self.budget.root/'ledger.json').read_bytes())
        return dict(version=1,scope='benchmark-retired-capacity-projection-v1',source_id='source-fixture',
                    currency='CNY',source_cap='30',source_known_cost='1',allocations=[
                        dict(allocation_id='retired-fixture',cap='6.6',retirement=dict(plan=self.value,
                            plan_sha256=self.anchor,ledger=ledger,ledger_sha256=digest(ledger))),
                        dict(allocation_id='active-fixture',cap='6',retirement=None)],
                    historical_reservations=[dict(reservation_id='unknown-fixture',amount='2.228224',
                        ledger_sha256='a'*64)])

    def test_funding_projection_keeps_active_caps_and_unknown_holds_in_full(self):
        value=self.funding();result=project_funding(value,digest(value))
        self.assertEqual(result['retired_known_cost'],'0.000297280000')
        self.assertEqual(result['active_committed_caps'],'6.000000000000')
        self.assertEqual(result['historical_retained'],'2.228224000000')
        self.assertEqual(result['unallocated_arithmetic'],'20.771478720000')
        self.assertEqual(result['funds_released'],'0.000000000000')
        self.assertFalse(result['funding_release_authorized'])
        self.assertFalse(result['can_resume_agent'])

    def test_funding_projection_rejects_duplicate_retirement_calls_and_source_ids(self):
        value=self.funding()
        for mutate in (lambda d:d['allocations'].append(copy.deepcopy(d['allocations'][0])),
                       lambda d:d['allocations'].append(dict(d['allocations'][0],allocation_id='other')),
                       lambda d:d['historical_reservations'].append(copy.deepcopy(d['historical_reservations'][0])),
                       lambda d:d.update(source_cap='2'),
                       lambda d:d['allocations'][0].update(cap='5')):
            changed=copy.deepcopy(value);mutate(changed)
            with self.assertRaises(BudgetStop): project_funding(changed,digest(changed))
        changed=copy.deepcopy(value)
        second=copy.deepcopy(changed['allocations'][0]);second['allocation_id']='other'
        second['retirement']['plan']['cohort_id']='other-cohort'
        second['retirement']['plan_sha256']=digest(second['retirement']['plan'])
        # A reused call must be rejected even if all surrounding identities are
        # newly and consistently anchored in an otherwise valid second proof.
        projected=second['retirement'];state=projected['ledger'];state.pop('retirement')
        state['samples']['sample-1']['status']='pending';state['plan_sha256']=projected['plan_sha256']
        from scripts.terminal_bench.cohort_retirement import retirement_record
        record=retirement_record(projected['plan'],projected['plan_sha256'],state)
        state['samples']['sample-1']['status']='retired';state['retirement']=record
        projected['ledger_sha256']=digest(state);changed['allocations'].append(second)
        with self.assertRaisesRegex(BudgetStop,'retirement_call_reused'):
            project_funding(changed,digest(changed))


if __name__ == '__main__': unittest.main()
