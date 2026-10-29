import test from 'node:test';
import assert from 'node:assert/strict';
import { descriptors, initialState, reducer } from '../../internal/webui/assets/state.mjs';

const snapshot = (sessionID = 's1') => ({
  session_id: sessionID, available: true, board_id: 'b1',
  revision: { board: '9007199254740993', runtime: '18446744073709551615' },
  work_items: [], executions: [], links: [], attention: [], diagnostics: [], hidden: {},
});
const liveState = () => reducer(initialState(), {
  type: 'SESSION_UPSERT', session: { id: 's1', live: true },
});
const loading = (requestID = 1) => ({ type: 'SESSION_TASKS_LOADING', id: 's1', requestID });
const success = (requestID = 1, response = snapshot()) => ({
  type: 'SESSION_TASKS_SUCCESS', id: 's1', requestID, response,
});

test('task overview accepts only the pending owned session and preserves decimal identities', () => {
  let state = reducer(liveState(), loading());
  assert.equal(state.sessions.s1.tasks.status, 'loading');
  assert.equal(reducer(state, success(2)), state);
  state = reducer(state, success());
  assert.equal(state.sessions.s1.tasks.snapshot.revision.runtime, '18446744073709551615');
  assert.equal(state.sessions.s1.tasks.status, 'ready');
  assert.equal(reducer(state, success()), state, 'duplicate completion is retired');
  assert.equal(JSON.stringify(descriptors(state)).includes('revision'), false, 'no durable task cache');
  state = reducer(state, loading(3));
  state = reducer(state, success(3, snapshot('another')));
  assert.equal(state.sessions.s1.tasks.status, 'error');
  assert.equal(state.sessions.s1.tasks.snapshot.session_id, 's1', 'bad response retains only previously accepted data');
});

test('refresh preserves cached data and marks activity racing its read as stale', () => {
  let state = reducer(reducer(liveState(), loading()), success());
  state = reducer(state, loading(2));
  state = reducer(state, { type: 'EVENT', event: {
    id: 1, session_id: 's1', type: 'task_progress', data: {},
  } });
  assert.equal(state.sessions.s1.tasks.stale, true);
  state = reducer(state, success(2));
  assert.equal(state.sessions.s1.tasks.stale, true);
  const cached = state.sessions.s1.tasks.snapshot;
  state = reducer(state, loading(3));
  assert.equal(state.sessions.s1.tasks.stale, false);
  state = reducer(state, { type: 'SESSION_TASKS_FAILED', id: 's1', requestID: 3, error: 'read failed' });
  assert.equal(state.sessions.s1.tasks.snapshot, cached);
  assert.equal(state.sessions.s1.tasks.stale, true);
  assert.equal(state.sessions.s1.notice, '', 'panel failure is not a conversation error');
});

test('closed, removed, detached and backend-lost sessions cannot accept task reads', () => {
  const pending = reducer(liveState(), loading());
  for (const retired of [
    reducer(pending, { type: 'EVENT', event: { id: 1, session_id: 's1', type: 'session.closed', data: {} } }),
    reducer(pending, { type: 'SESSION_REMOVE', id: 's1' }),
    reducer(pending, { type: 'BACKEND_UNAVAILABLE' }),
    reducer(pending, { type: 'SESSION_UPSERT', session: { id: 's1', live: false, status: 'saved' } }),
  ]) {
    assert.equal(reducer(retired, success()), retired);
    assert.equal(reducer(retired, loading(2)), retired);
  }
});
