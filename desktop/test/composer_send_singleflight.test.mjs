import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';
import test from 'node:test';

import { canClearSubmittedDraft } from '../../internal/webui/assets/state.mjs';

const source = await readFile(new URL('../../internal/webui/assets/app.mjs', import.meta.url), 'utf8');

function productionBetween(start, end) {
  const first = source.indexOf(start);
  const last = source.indexOf(end, first);
  assert.ok(first >= 0 && last > first, `production boundary: ${start}`);
  return source.slice(first, last);
}

const bind = new Function('initialState', 'activeSession', 'reducer', 'canSubmitTurn', 'canQueuePrompt',
  'canClearSubmittedDraft', 'api', 'crypto', '$', 'commandCompletion', 'promptSuggestion', 'agentConversationView', `
  const attachAttempts = new Map();
  const queueAttempts = new Map();
  const turnDraftAttempts = new Map();
  let state = initialState;
  let persistenceEnabled = false;
  function dispatch(action) { state = reducer(state, action); }
  ${productionBetween('function updateDraft(value)', 'async function send(event)')}
  ${productionBetween('async function send(event)', 'async function cancel()')}
  ${productionBetween('function sessionScopedError(error, sessionID)', 'function handleBackendExit()')}
  return { send, updateDraft, select: id => { state = { ...state, activeID: id }; }, state: () => state };
`);

function deferred() {
  let resolve;
  let reject;
  const promise = new Promise((nextResolve, nextReject) => { resolve = nextResolve; reject = nextReject; });
  return { promise, resolve, reject };
}

function fixture({ childConversationOpen = false } = {}) {
  const prompt = { value: '' };
  const calls = [];
  const requests = [];
  const initialState = {
    activeID: 'first',
    sessions: {
      first: { id: 'first', live: true, draft: '' },
      second: { id: 'second', live: true, draft: '' },
    },
  };
  const app = bind(
    initialState,
    state => state.sessions[state.activeID],
    (state, action) => action.type === 'SESSION_DRAFT'
      ? { ...state, sessions: { ...state.sessions, [action.id]: { ...state.sessions[action.id], draft: action.draft } } }
      : state,
    current => current.live,
    () => false,
    canClearSubmittedDraft,
    (op, input) => {
      assert.equal(op, 'startTurn');
      calls.push(input);
      const request = deferred();
      requests.push(request);
      return request.promise;
    },
    { randomUUID: () => `fixture-turn-${calls.length + 1}` },
    id => { assert.equal(id, 'prompt'); return prompt; },
    { dismiss() {} },
    { dismiss() {} },
    { isOpen: () => childConversationOpen },
  );
  const event = { preventDefault() {} };
  return {
    app,
    calls,
    requests,
    prompt,
    send: () => app.send(event),
    edit(value) { prompt.value = value; app.updateDraft(value); },
    select(id) { app.select(id); },
  };
}

test('a read-only child conversation cannot submit or clear the parent draft', async () => {
  const current = fixture({ childConversationOpen: true });
  current.edit('Draft X');
  await current.send();
  assert.equal(current.calls.length, 0);
  assert.equal(current.app.state().sessions.first.draft, 'Draft X');
});

test('ordinary Send admits one unchanged live-session draft until its request settles', async () => {
  const current = fixture();
  current.edit('Draft X');
  const first = current.send();
  const duplicate = current.send();
  assert.equal(current.calls.length, 1);
  current.requests[0].resolve({ accepted: true });
  await Promise.all([first, duplicate]);
  assert.equal(current.app.state().sessions.first.draft, '');
});

test('a failed ordinary Send retains its draft and releases it for an explicit retry', async () => {
  const current = fixture();
  current.edit('Draft X');
  const first = current.send();
  current.requests[0].reject(new Error('fixture failure'));
  await assert.rejects(first, /fixture failure/);
  assert.equal(current.app.state().sessions.first.draft, 'Draft X');
  const retry = current.send();
  assert.equal(current.calls.length, 2);
  current.requests[1].resolve({ accepted: true });
  await retry;
  assert.equal(current.app.state().sessions.first.draft, '');
});

test('editing revokes the old clear right and permits a distinct follow-up Send', async () => {
  const current = fixture();
  current.edit('Draft X');
  const first = current.send();
  current.edit('Draft Y');
  const second = current.send();
  assert.equal(current.calls.length, 2);
  current.requests[0].resolve({ accepted: true });
  await first;
  assert.equal(current.app.state().sessions.first.draft, 'Draft Y');
  current.requests[1].resolve({ accepted: true });
  await second;
  assert.equal(current.app.state().sessions.first.draft, '');
});

test('ordinary Send stays isolated by session', async () => {
  const current = fixture();
  current.edit('Draft X');
  const first = current.send();
  current.select('second');
  current.edit('Draft X');
  const second = current.send();
  assert.equal(current.calls.length, 2);
  current.requests[0].resolve({ accepted: true });
  current.requests[1].resolve({ accepted: true });
  await Promise.all([first, second]);
  assert.equal(current.app.state().sessions.first.draft, '');
  assert.equal(current.app.state().sessions.second.draft, '');
});
