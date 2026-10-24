import assert from 'node:assert/strict';
import test from 'node:test';
import { createRequire } from 'node:module';
import { createPromptSuggestion, suggestionKey } from '../../internal/webui/assets/prompt_suggestion.mjs';
import { operationPath } from '../../internal/webui/assets/transport.mjs';

const { desktopOperation } = createRequire(import.meta.url)('../request.cjs');
const initial = () => ({ enabled: true, sessionID: 'session-1', input: '', fence: 'idle:route-1' });
for (const [surface, operation] of Object.entries({ desktop: desktopOperation, web: operationPath })) {
  test(`${surface} suggestion is an explicit bounded POST with no client context`, () => {
    assert.deepEqual(operation('promptSuggestion', { sessionID: 'session-1' }), ['/v1/sessions/session-1/prompt-suggestion', 'POST', {}]);
    for (const patch of [{ sessionID: '../escape' }, { prompt: 'private' }, { cwd: '/private' }, { model: 'override' }]) {
      assert.throws(() => operation('promptSuggestion', { sessionID: 'session-1', ...patch }));
    }
  });
}

test('suggestions never request automatically and acceptance only returns draft text', async () => {
  let calls = 0;
  let current = initial();
  const query = createPromptSuggestion({ snapshot: () => current, publish() {}, request: async () => { calls++; return { session_id: current.sessionID, suggestion: 'run the focused tests' }; } });
  query.refresh(); query.refresh();
  assert.equal(calls, 0);
  await query.request();
  assert.equal(calls, 1);
  assert.equal(query.current().text, 'run the focused tests');
  assert.equal(current.input, '');
  assert.equal(query.accept(), 'run the focused tests');
  assert.equal(query.accept(), '');
  assert.equal(calls, 1);
});

test('typing then deleting, session, route, runtime and dismissal fence late suggestions', async () => {
  for (const change of ['draft', 'session', 'route', 'busy', 'dismiss']) {
    let current = initial();
    let resolve;
    const query = createPromptSuggestion({ snapshot: () => current, publish() {}, request: () => new Promise(done => { resolve = done; }) });
    const work = query.request();
    if (change === 'draft') { current.input = 'x'; query.refresh(); current.input = ''; query.refresh(); }
    if (change === 'session') current.sessionID = 'session-2';
    if (change === 'route') current.fence = 'route-2';
    if (change === 'busy') current.enabled = false;
    if (change === 'dismiss') query.dismiss();
    resolve({ session_id: 'session-1', suggestion: 'run the focused tests' });
    await work;
    assert.equal(query.current().text, '', change);
    assert.equal(query.accept(), '', change);
  }
});

test('duplicate clicks, malformed responses, unavailable suggestions and request errors fail closed', async () => {
  const current = initial();
  for (const response of [{}, { session_id: 'other', suggestion: 'run tests' }, { session_id: current.sessionID, suggestion: 'a'.repeat(100) }, { session_id: current.sessionID, suggestion: '\ud800' }, { session_id: current.sessionID, suggestion: 'run\n tests' }, { session_id: current.sessionID, suggestion: '' }]) {
    let calls = 0;
    let resolve;
    const query = createPromptSuggestion({ snapshot: () => current, publish() {}, request: () => { calls++; return new Promise(done => { resolve = done; }); } });
    const work = query.request();
    await query.request();
    assert.equal(calls, 1);
    resolve(response); await work;
    assert.equal(query.current().busy, false);
    assert.equal(query.accept(), '');
  }
  const query = createPromptSuggestion({ snapshot: () => current, publish() {}, request: async () => { throw new Error('private host details'); } });
  await query.request();
  assert.equal(query.current().status.includes('private'), false);
  assert.equal(suggestionKey({ ...current, input: ' ' }), '');
});

test('typed suggestion outcomes explain failures without exposing provider or unknown reason text', async () => {
  for (const [status, reason, expected] of [
    ['suppressed', 'early_conversation', 'two assistant messages'],
    ['suppressed', 'disabled', 'disabled'],
    ['suppressed', 'goal_active', 'Goal'],
    ['empty', '', 'No next step'],
    ['filtered', '', 'No suitable'],
    ['timed_out', '', 'timed out'],
    ['cancelled', '', 'cancelled'],
    ['stale', '', 'model route changed'],
    ['failed', 'private provider details', 'failed'],
    ['suppressed', 'private provider details', 'unavailable'],
  ]) {
    const current = initial();
    const query = createPromptSuggestion({
      snapshot: () => current, publish() {},
      request: async () => ({ session_id: current.sessionID, status, reason, suggestion: 'must not become a candidate' }),
    });
    await query.request();
    assert.equal(query.current().text, '');
    assert.ok(query.current().status.includes(expected), query.current().status);
    assert.equal(query.current().status.includes('private'), false);
    assert.equal(query.accept(), '');
  }
});
