import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';
import test from 'node:test';

import {
  BACKEND_UNAVAILABLE_NOTICE,
  backendAvailable,
  descriptors,
  initialState,
  reducer,
  unverifiedPersistedDescriptor,
} from '../../internal/webui/assets/state.mjs';

const asset = (name) => new URL(`../../internal/webui/assets/${name}`, import.meta.url);

test('backend loss atomically retires process-owned session capabilities', () => {
  const messages = [{
    id: 'assistant-1',
    role: 'assistant',
    content: 'partial output',
    turnID: 'turn-1',
    completed: false,
  }];
  const activity = [{
    id: 'tool-1',
    turnID: 'turn-1',
    kind: 'tool',
    state: 'running',
    category: 'command',
    timestamp: '2026-08-20T00:00:00Z',
  }];
  let state = reducer(initialState(), {
    type: 'SESSION_UPSERT',
    session: {
      id: 'live-1',
      title: 'Live session',
      workspace_label: 'workspace',
      status: 'waiting',
      active_turn_id: 'turn-1',
      messages,
      interactions: [{ request_id: 'question-1', turn_id: 'turn-1', kind: 'question' }],
      interactionForms: {
        'question-1': { step: 1, answers: { q1: { optionIDs: ['a'], text: '' } } },
      },
      activity,
      cursor: 42,
      attention: true,
      replaying: true,
      draft: 'keep this draft',
      durable: true,
      resumable: true,
      live: true,
      activation: 'interaction_required',
      resolvingRequestID: 'question-1',
      review: { status: 'loading', requestID: 9 },
      execution: { status: 'updating', requestID: 11, model: 'model-a' },
    },
  });
  state = reducer(state, {
    type: 'SESSION_UPSERT',
    session: {
      id: 'saved-1',
      title: 'Saved session',
      status: 'saved',
      durable: true,
      resumable: true,
      live: false,
      draft: 'saved draft',
    },
  });

  state = reducer(state, {
    type: 'BACKEND_UNAVAILABLE',
    error: 'PRIVATE HOST DIAGNOSTIC',
  });

  assert.equal(backendAvailable(state), false);
  for (const session of Object.values(state.sessions)) {
    assert.equal(session.live, false);
    assert.equal(session.status, 'offline');
    assert.equal(session.active_turn_id, '');
    assert.deepEqual(session.interactions, []);
    assert.deepEqual(session.interactionForms, {});
    assert.equal(session.attention, false);
    assert.equal(session.replaying, false);
    assert.equal(session.activation, 'detached');
    assert.equal(session.resolvingRequestID, '');
    assert.equal(session.notice, BACKEND_UNAVAILABLE_NOTICE);
    assert.equal(session.review.status, 'idle');
    assert.equal(session.review.requestID, 0);
    assert.equal(session.execution.status, 'idle');
    assert.equal(session.execution.requestID, 0);
  }
  assert.deepEqual(state.sessions['live-1'].messages, messages);
  assert.deepEqual(state.sessions['live-1'].activity, activity);
  assert.equal(state.sessions['live-1'].draft, 'keep this draft');
  assert.equal(state.sessions['live-1'].durable, true);
  assert.equal(state.sessions['live-1'].resumable, true);
  assert.equal(state.sessions['live-1'].cursor, 42);
  assert.equal(state.sessions['live-1'].messages[0].completed, false);
  assert.equal(state.sessions['saved-1'].draft, 'saved draft');
  assert.doesNotMatch(JSON.stringify(state), /PRIVATE HOST DIAGNOSTIC/);

  const descriptor = descriptors(state).find((item) => item.id === 'live-1');
  assert.equal('status' in descriptor, false);
  assert.equal('backendAvailable' in descriptor, false);
  let cold = reducer(initialState(), {
    type: 'SESSION_UPSERT',
    session: unverifiedPersistedDescriptor({
      ...descriptor,
      draft: state.sessions['live-1'].draft,
    }),
  });
  cold = reducer(cold, { type: 'SESSION_SELECT', id: 'live-1' });
  assert.equal(backendAvailable(cold), true);
  assert.equal(cold.sessions['live-1'].live, false);
  assert.equal(cold.sessions['live-1'].active_turn_id, '');
  assert.equal(cold.sessions['live-1'].activation, 'detached');
  assert.equal(cold.sessions['live-1'].resumable, false);
  assert.equal(cold.sessions['live-1'].draft, 'keep this draft');
});

test('late backend projections cannot revive capabilities after backend loss', () => {
  let state = reducer(initialState(), {
    type: 'SESSION_UPSERT',
    session: { id: 'session-1', live: true, status: 'idle' },
  });
  state = reducer(state, { type: 'BACKEND_UNAVAILABLE' });
  const contained = state;

  for (const action of [
    {
      type: 'EVENT',
      event: {
        id: 1,
        type: 'turn.accepted',
        session_id: 'session-1',
        turn_id: 'late-turn',
        data: { turn_id: 'late-turn' },
      },
    },
    {
      type: 'SESSION_SNAPSHOT',
      id: 'session-1',
      snapshot: {
        session: { id: 'session-1', live: true, active_turn_id: 'late-turn' },
      },
    },
  ]) {
    assert.strictEqual(reducer(contained, action), contained);
  }

  const lateUpsert = reducer(contained, {
    type: 'SESSION_UPSERT',
    session: {
      id: 'session-1',
      live: true,
      status: 'running',
      active_turn_id: 'late-turn',
      draft: 'stale draft',
      messages: [{ id: 'stale', content: 'stale' }],
      activity: [{ id: 'stale' }],
      cursor: 999,
    },
  });
  assert.strictEqual(lateUpsert, contained);
});

test('renderer turns an unexpected backend exit into fixed restart guidance', async () => {
  const [app, preload] = await Promise.all([
    readFile(asset('app.mjs'), 'utf8'),
    readFile(new URL('../preload.cjs', import.meta.url), 'utf8'),
  ]);
  const handler = app.slice(
    app.indexOf('function handleBackendExit()'),
    app.indexOf('async function bootstrapApp()'),
  );
  const bootstrap = app.slice(
    app.indexOf('async function bootstrapApp()'),
    app.indexOf('function beginSessionCreation()'),
  );
  const render = app.slice(
    app.indexOf('function render()'),
    app.indexOf('function renderLegacyImport'),
  );

  assert.match(
    handler,
    /completeConfirmation\(false\)[\s\S]*?dispatch\(\{ type: 'BACKEND_UNAVAILABLE' \}\)/,
  );
  assert.doesNotMatch(handler, /SESSION_UPSERT|payload\.|restart/i);
  assert.ok(
    bootstrap.indexOf('transport.onBackendExit(handleBackendExit)') <
      bootstrap.indexOf('await transport.getInfo()'),
    'backend exit subscription must precede every bootstrap await',
  );
  assert.match(render, /const backendReady = backendAvailable\(state\)/);
  assert.match(render, /BACKEND_UNAVAILABLE_NOTICE/);
  assert.match(render, /\$\('prompt'\)\.disabled = !backendReady/);
  assert.match(render, /\$\('send'\)\.disabled = !backendReady/);
  assert.match(render, /\$\('cancel'\)\.disabled = !backendReady/);
  assert.match(render, /\$\('open-web'\)\.disabled = !backendReady/);
  assert.match(render, /loadEarlier\.disabled = !backendReady/);
  assert.match(app, /button\.disabled = creating \|\| !backendAvailable\(state\)/);
  assert.match(app, /settings\.disabled = providerSetupBusy \|\| !backendAvailable\(state\)/);
  assert.match(app, /providerButton\.disabled = !backendReady/);
  assert.match(app, /workspaceButton\.disabled = !backendReady/);
  assert.match(app, /loadMore\.disabled = !backendAvailable\(state\)/);
  assert.match(app, /const inactive = !backendAvailable\(state\)/);
  assert.match(app, /const controlled = backendAvailable\(state\)/);
  assert.match(
    app,
    /function api\(operation, payload\)[\s\S]*?if \(!backendAvailable\(state\)\)/,
  );
  assert.doesNotMatch(preload, /restartBackend|app:backend-restart/);
});
