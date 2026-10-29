import test from 'node:test';
import assert from 'node:assert/strict';
import { createRequire } from 'node:module';
import { operationPath } from '../../internal/webui/assets/transport.mjs';
import {
  createAgentExecutionDetailController, executionDetailEligibility, normalizeAgentExecutionDetail,
} from '../../internal/webui/assets/agent_execution_detail.mjs';

const { desktopOperation } = createRequire(import.meta.url)('../request.cjs');
const row = (patch = {}) => ({ agent_id: 'worker', generation: '9007199254740993',
  execution_session_id: 'child', thread_id: 'thread', name: 'Worker', ...patch });
const target = (patch = {}) => ({ sessionID: 'owned', agentID: 'worker', generation: '9007199254740993',
  executionSessionID: 'child', threadID: 'thread', ...patch });
const response = (output = false, patch = {}) => ({ session_id: 'owned', agent_id: 'worker',
  generation: '9007199254740993', execution_session_id: 'child', thread_id: 'thread',
  revision: '18446744073709551615', metadata: { name: 'Worker', status: 'completed', parent_agent_id: 'parent' },
  output: { requested: output, text: output ? '<script>synthetic output</script>' : '', truncated: false, error_category: '' }, ...patch });
const context = (patch = {}) => ({ session: { id: 'owned', live: true, status: 'idle', tasks: {
  snapshot: { executions: [row()] }, stale: false,
} }, backendReady: true, visible: true, ...patch });
function fixture() {
  const pending = [];
  const controller = createAgentExecutionDetailController({ read: payload => new Promise((resolve, reject) => pending.push({ payload, resolve, reject })) });
  const ctx = context(); controller.updateContext(ctx);
  return { controller, pending, ctx };
}
const tick = () => new Promise(resolve => setImmediate(resolve));

for (const [surface, operation] of Object.entries({ desktop: desktopOperation, web: operationPath })) {
  test(`${surface} exact-detail transport has a strict, lossless GET contract`, () => {
    assert.deepEqual(operation('getAgentExecutionDetail', target()), [
      '/v1/sessions/owned/agent-execution-detail?agent_id=worker&generation=9007199254740993&execution_session_id=child&thread_id=thread&include_output=false', 'GET',
    ]);
    assert.match(operation('getAgentExecutionDetail', { ...target(), includeOutput: true })[0], /include_output=true$/);
    for (const payload of [null, [], {}, target({ generation: 9007199254740993 }), target({ generation: '01' }),
      target({ generation: '0' }), target({ generation: '9223372036854775808' }), target({ threadID: '' }),
      target({ agentID: ' worker' }), target({ agentID: '\u0000' }), target({ executionSessionID: '界'.repeat(513) }),
      target({ includeOutput: 'true' }), target({ action: 'cancel' }), target({ path: '/synthetic/output' })]) {
      assert.throws(() => operation('getAgentExecutionDetail', payload));
    }
  });
}

test('detail eligibility requires positive exact lineage, not a name or replay attachment', () => {
  assert.equal(executionDetailEligibility(row()), '');
  for (const patch of [{ generation: '0' }, { generation: '9223372036854775808' }, { execution_session_id: '' },
    { thread_id: '' }, { replay_only: true }, { predispatch: true }]) assert.ok(executionDetailEligibility(row(patch)));
});

test('detail DTO matches every identity, bounds Unicode output, and strips private extras', () => {
  const dto = normalizeAgentExecutionDetail(response(true, { output_file: '/private/synthetic',
    metadata: { name: '界'.repeat(600), cwd: '/private/synthetic', tool_use_count: -1 },
    output: { requested: true, text: '界'.repeat(30000), truncated: false, error_category: '' } }), target(), true);
  assert.equal(dto.revision, '18446744073709551615');
  assert.equal([...dto.metadata.name].length, 512);
  assert.equal(dto.metadata.tool_use_count, 0);
  assert.ok(new TextEncoder().encode(dto.output.text).length <= 65536);
  assert.equal(dto.output.truncated, true);
  assert.equal(dto.output.text.includes('\uFFFD'), false);
  assert.equal(JSON.stringify(dto).includes('/private/synthetic'), false);
  for (const patch of [{ session_id: 'other' }, { agent_id: 'other' }, { generation: '9007199254740992' },
    { execution_session_id: 'other' }, { thread_id: 'other' }, { revision: 1 }]) {
    assert.throws(() => normalizeAgentExecutionDetail(response(true, patch), target(), true));
  }
  assert.throws(() => normalizeAgentExecutionDetail(response(true), target(), false));
  const error = normalizeAgentExecutionDetail(response(true, { output: {
    requested: true, text: '/private/synthetic', error_category: 'output_read_failed', truncated: true,
  } }), target(), true);
  assert.equal(error.output.text, '');
  assert.equal(error.output.truncated, false);
});

test('reads are explicit; lineage and output caches are separate and duplicate settlement is inert', async () => {
  const { controller: c, pending: p, ctx } = fixture();
  assert.equal(p.length, 0);
  c.updateContext(ctx); assert.equal(p.length, 0, 'render does not read');
  c.select(row()); assert.equal(p.length, 1); assert.equal(p[0].payload.includeOutput, false);
  p[0].resolve(response()); await tick();
  assert.equal(c.state().pages.lineage.status, 'ready');
  c.selectTab('output'); assert.equal(p.length, 2); assert.equal(p[1].payload.includeOutput, true);
  p[1].resolve(response(true)); await tick();
  c.selectTab('lineage'); c.selectTab('output'); assert.equal(p.length, 2, 'cached tabs do not read');
  c.updateContext({ ...ctx, session: { ...ctx.session, tasks: { ...ctx.session.tasks, stale: true } } });
  assert.equal(c.state().pages.output.stale, true); assert.equal(p.length, 2, 'runtime activity never polls');
  c.refresh(); assert.equal(p.length, 3);
  assert.ok(c.state().pages.output.data, 'same-target cache remains visible during refresh');
  p[2].reject(Object.assign(new Error('/private/synthetic'), { code: 'agent_execution_selection_changed' })); await tick();
  assert.match(c.state().pages.output.error, /no longer/);
  assert.equal(c.state().pages.output.error.includes('/private'), false);
  assert.equal(c.state().pages.output.stale, true);
});

test('switching tabs retires pending data; superseded refresh responses cannot overwrite new data', async () => {
  const { controller: c, pending: p } = fixture();
  c.select(row()); c.selectTab('output');
  p[0].resolve(response()); await tick();
  assert.equal(c.state().pages.lineage.data, null);
  p[1].resolve(response(true)); await tick();
  c.refresh(); c.refresh();
  p[3].resolve(response(true, { revision: '12' })); await tick();
  p[2].resolve(response(true, { revision: '11' })); await tick();
  assert.equal(c.state().pages.output.data.revision, '12');
  c.selectTab('lineage'); assert.equal(p.length, 5, 'retired lineage load remains reloadable');
});

test('back, filter/list replacement, hidden view, inactive/closed/offline sessions retire all late responses', async () => {
  for (const retire of [
    (c) => c.close(),
    (c, ctx) => c.updateContext({ ...ctx, visible: false }),
    (c, ctx) => c.updateContext({ ...ctx, backendReady: false }),
    (c, ctx) => c.updateContext({ ...ctx, session: null }),
    (c, ctx) => c.updateContext({ ...ctx, session: { ...ctx.session, id: 'other' } }),
    (c, ctx) => c.updateContext({ ...ctx, session: { ...ctx.session, status: 'closed' } }),
    (c, ctx) => c.updateContext({ ...ctx, session: { ...ctx.session, live: false } }),
    (c, ctx) => c.updateContext({ ...ctx, session: { ...ctx.session, tasks: { snapshot: { executions: [row()] } } } }),
  ]) {
    const { controller: c, pending: p, ctx } = fixture(); c.select(row()); retire(c, ctx);
    p[0].resolve(response()); await tick(); assert.equal(c.state().target, null);
  }
  const { controller: c, pending: p, ctx } = fixture();
  c.select(row({ thread_id: 'wrong' })); assert.equal(p.length, 0, 'selection must be in the canonical snapshot');
  c.updateContext({ ...ctx, visible: false }); c.select(row()); assert.equal(p.length, 0);
});
