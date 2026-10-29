import test from 'node:test';
import assert from 'node:assert/strict';
import { createRequire } from 'node:module';
import { operationPath } from '../../internal/webui/assets/transport.mjs';
import { createAgentConversationController, normalizeAgentConversationPage } from '../../internal/webui/assets/agent_conversation.mjs';

const { desktopOperation } = createRequire(import.meta.url)('../request.cjs');
const row = { agent_id: 'agent', generation: '9007199254740993', execution_session_id: 'child', thread_id: 'thread' };
const target = { sessionID: 'owned', agentID: row.agent_id, generation: row.generation, executionSessionID: 'child', threadID: 'thread' };
const message = (id, patch = {}) => ({ id, transcript_entry_id: id, role: 'assistant', content: `message ${id}`,
  reasoning_content: '', tool_call_id: '', tool_name: '', tool_calls: [], prompt_parts: [],
  completed: true, source: 'durable', replay: false, truncated: false, ...patch });
const page = (patch = {}) => ({ session_id: 'owned', ...row, revision: '18446744073709551615', mode: 'live_attach',
  storage: 'durable', replay: false, messages: [message('newest')], has_more: true, next_cursor: 'cursor-1', corruptions: 0, ...patch });
const context = () => ({ backendReady: true, session: { id: 'owned', live: true, status: 'running', draft: 'parent draft',
  tasks: { snapshot: { executions: [row] }, stale: false } } });
const tick = () => new Promise(resolve => setImmediate(resolve));
function fixture() {
  const pending = [];
  const controller = createAgentConversationController({ read: payload => new Promise((resolve, reject) => pending.push({ payload, resolve, reject })) });
  const ctx = context(); controller.updateContext(ctx);
  return { controller, pending, ctx };
}

for (const [surface, operation] of Object.entries({ desktop: desktopOperation, web: operationPath })) {
  test(`${surface} conversation paging carries the full lossless identity on GET only`, () => {
    assert.deepEqual(operation('getAgentTranscript', target), [
      '/v1/sessions/owned/agent-transcript?agent_id=agent&generation=9007199254740993&execution_session_id=child&thread_id=thread&limit=32', 'GET',
    ]);
    assert.match(operation('getAgentTranscript', { ...target, cursor: 'opaque&token', limit: 1 })[0], /cursor=opaque%26token&limit=1$/);
    for (const patch of [{ generation: 1 }, { generation: '01' }, { generation: '9223372036854775808' },
      { threadID: '' }, { executionSessionID: ' other' }, { cursor: '' }, { cursor: 'a'.repeat(129) },
      { cursor: '界' }, { cursor: 'a b' }, { limit: 0 }, { limit: 33 }, { limit: '1' },
      { includeOutput: true }, { path: '/synthetic' }, { action: 'resume' }]) {
      assert.throws(() => operation('getAgentTranscript', { ...target, ...patch }));
    }
  });
}

test('conversation normalization binds all identities, exact IDs and modes; copies only bounded fields', () => {
  const result = normalizeAgentConversationPage(page({ path: '/internal/synthetic', messages: [message('m', {
    content: '界'.repeat(10000), cwd: '/internal/synthetic', prompt_parts: [{ kind: 'image', text: '', mime_type: 'image/png',
      size_bytes: 100, width: 2, height: 3, image_detail: 'high', data: 'secret-media' }],
  })] }), target);
  assert.equal(result.revision, '18446744073709551615');
  assert.equal(result.messages[0].truncated, true);
  assert.ok(new TextEncoder().encode(result.messages[0].content).length <= 16384);
  assert.equal(result.messages[0].content.includes('\uFFFD'), false);
  assert.equal(JSON.stringify(result).includes('/internal/synthetic'), false);
  assert.equal(JSON.stringify(result).includes('secret-media'), false);
  for (const patch of [{ session_id: 'other' }, { agent_id: 'other' }, { generation: '9007199254740992' },
    { execution_session_id: 'other' }, { thread_id: 'other' }, { revision: 1 }, { mode: 'writable' },
    { replay: true }, { messages: [message('same'), message('same')] }, { messages: [message('界'.repeat(513))] },
    { has_more: false }, { has_more: true, next_cursor: '' }]) {
    assert.throws(() => normalizeAgentConversationPage(page(patch), target));
  }
  for (const mode of ['replay_only', 'evicted_transcript']) {
    assert.equal(normalizeAgentConversationPage(page({ mode, replay: true }), target).mode, mode);
  }
});

test('open and older are explicit reads; prepend preserves distinct equal-text rows and the newest lifecycle', async () => {
  const { controller: c, pending: p, ctx } = fixture();
  c.updateContext(ctx); assert.equal(p.length, 0);
  c.open(target); assert.equal(p.length, 1); assert.deepEqual(p[0].payload, { ...target, limit: 32 });
  p[0].resolve(page()); await tick();
  c.older(); c.older(); assert.equal(p.length, 2, 'loading older page is single-flight');
  assert.equal(p[1].payload.cursor, 'cursor-1');
  p[1].resolve(page({ mode: 'replay_only', replay: true, messages: [message('older', { content: 'message newest' })],
    has_more: false, next_cursor: '' })); await tick();
  assert.deepEqual(c.state().data.messages.map(m => m.id), ['older', 'newest']);
  assert.equal(c.state().data.mode, 'live_attach', 'older reads cannot rewrite newest snapshot mode');
  c.older(); assert.equal(p.length, 2);
  assert.equal(ctx.session.draft, 'parent draft');
  c.updateContext({ ...ctx, session: { ...ctx.session, tasks: { ...ctx.session.tasks, stale: true } } });
  assert.equal(c.state().stale, true); assert.equal(p.length, 2, 'activity never polls');
});

test('only runtime-first messages may omit durable entry identity; rich parts retain their order', () => {
  const parts = [{ kind: 'text', text: 'before', mime_type: '', size_bytes: 0, width: 0, height: 0, image_detail: '' },
    { kind: 'image', text: '', mime_type: 'image/png', size_bytes: 10, width: 2, height: 3, image_detail: 'auto' },
    { kind: 'text', text: 'after', mime_type: '', size_bytes: 0, width: 0, height: 0, image_detail: '' }];
  const result = normalizeAgentConversationPage(page({ messages: [message('runtime-first', {
    source: 'runtime', transcript_entry_id: '', prompt_parts: parts,
  })] }), target);
  assert.deepEqual(result.messages[0].promptParts.map(part => [part.kind, part.text]),
    [['text', 'before'], ['image', ''], ['text', 'after']]);
  for (const source of ['durable', 'durable+runtime']) {
    assert.throws(() => normalizeAgentConversationPage(page({ messages: [message('missing', {
      source, transcript_entry_id: '',
    })] }), target));
  }
});

test('refresh supersedes older responses, resets the cursor chain only on success, and preserves cache on safe errors', async () => {
  const { controller: c, pending: p } = fixture();
  c.open(target); p[0].resolve(page()); await tick();
  c.older(); c.refresh();
  p[2].resolve(page({ messages: [message('refreshed')], next_cursor: 'cursor-new' })); await tick();
  p[1].resolve(page({ messages: [message('late')], next_cursor: 'cursor-old' })); await tick();
  assert.deepEqual(c.state().data.messages.map(m => m.id), ['refreshed']);
  assert.equal(c.state().data.nextCursor, 'cursor-new');
  c.older(); p[3].reject(Object.assign(new Error('/private/raw-error'), { code: 'invalid_cursor' })); await tick();
  assert.match(c.state().error, /expired/); assert.equal(c.state().error.includes('/private'), false);
  assert.deepEqual(c.state().data.messages.map(m => m.id), ['refreshed']);
});

test('bounded window stops at 256 rows without persisting or re-reading', async () => {
  const { controller: c, pending: p } = fixture(); c.open(target);
  for (let i = 0; i < 8; i++) {
    p[i].resolve(page({ messages: Array.from({ length: 32 }, (_, j) => message(`${i}-${j}`)), next_cursor: `cursor-${i + 1}` }));
    await tick(); c.older();
  }
  assert.equal(c.state().data.messages.length, 256); assert.equal(p.length, 8);
  c.close(); assert.equal(c.state().data, null);
});

test('return, close, session/backend loss, lineage change and snapshot replacement reject every late response', async () => {
  for (const mutate of [
    (c) => c.close(),
    (c, ctx) => c.updateContext({ ...ctx, backendReady: false }),
    (c, ctx) => c.updateContext({ ...ctx, session: null }),
    (c, ctx) => c.updateContext({ ...ctx, session: { ...ctx.session, id: 'other' } }),
    (c, ctx) => c.updateContext({ ...ctx, session: { ...ctx.session, live: false } }),
    (c, ctx) => c.updateContext({ ...ctx, session: { ...ctx.session, status: 'closed' } }),
    (c, ctx) => c.updateContext({ ...ctx, session: { ...ctx.session, tasks: { snapshot: { executions: [row] } } } }),
  ]) {
    const { controller: c, pending: p, ctx } = fixture(); c.open(target); mutate(c, ctx);
    p[0].resolve(page()); await tick(); assert.equal(c.state().target, null); assert.equal(c.state().data, null);
  }
  const { controller: c, pending: p, ctx } = fixture();
  c.open({ ...target, threadID: 'wrong' }); assert.equal(p.length, 0);
  c.updateContext({ ...ctx, session: { ...ctx.session, tasks: { snapshot: { executions: [row, row] } } } });
  c.open(target); assert.equal(p.length, 0, 'duplicate canonical rows cannot grant selection');
});
