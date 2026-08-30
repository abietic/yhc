import test from 'node:test';
import assert from 'node:assert/strict';

import {
  activeSession,
  activeInteraction,
  buildPermissionResolution,
  buildPlanResolution,
  buildQuestionResolution,
  buildRepeatedToolResolution,
  canImportDurableSession,
  canEditDraft,
  canQueuePrompt,
  canSubmitTurn,
  descriptors,
  initialState,
  interactionDraft,
  liveDescriptor,
  modelRebindSelector,
  reducer,
  retainedClosedDescriptor,
  sameQueueAttempt,
  sessionMatchesQuery,
  unverifiedPersistedDescriptor,
} from '../../internal/webui/assets/state.mjs';

function event(id, type, data = {}, sessionID = 's1', turnID = 'turn-1') {
  return {
    id,
    type,
    data,
    session_id: sessionID,
    turn_id: turnID,
    engine_sequence: id,
  };
}

test('event cursors are independent for each session', () => {
  let state = reducer(
    initialState(),
    { type: 'EVENT', event: event(8, 'turn.accepted') },
  );
  state = reducer(
    state,
    { type: 'EVENT', event: event(1, 'turn.accepted', {}, 's2') },
  );
  assert.equal(state.sessions.s1.cursor, 8);
  assert.equal(state.sessions.s2.cursor, 1);
});

test('user and assistant stream events build one ordered turn', () => {
  let state = reducer(
    initialState(),
    { type: 'EVENT', event: event(1, 'user_message', { content: 'hello' }) },
  );
  state = reducer(state, {
    type: 'EVENT',
    event: event(2, 'stream_event', { message: { content: 'hel' } }),
  });
  state = reducer(state, {
    type: 'EVENT',
    event: event(3, 'assistant', {
      message: {
        content: 'lo',
        tool_calls: [{ id: 'tool-1', function: { name: 'Read' } }],
      },
    }),
  });
  state = reducer(state, { type: 'SESSION_SELECT', id: 's1' });

  assert.deepEqual(
    activeSession(state).messages.map((item) => item.content),
    ['hello', 'hello'],
  );
  assert.equal(activeSession(state).messages[1].toolCalls.length, 1);
});

test('runtime snapshot replaces stale projection and advances its cursor', () => {
  let state = reducer(initialState(), {
    type: 'SESSION_UPSERT',
    session: { id: 's1', workspace_label: '/tmp/project', title: 'project' },
  });
  state = reducer(state, {
    type: 'SESSION_SNAPSHOT',
    id: 's1',
    snapshot: {
      session: {
        id: 's1',
        workspace_label: '/tmp/project',
        title: 'project',
        status: 'waiting',
      },
      event_cursor: 41,
      messages: [{
        id: 'message-1',
        role: 'assistant',
        content: 'restored',
        turn_id: 'turn-1',
        completed: true,
      }],
      interactions: [
        { request_id: 'permission-1', kind: 'permission' },
        { request_id: 'permission-1', kind: 'permission' },
      ],
      queued_prompts: [{
        id: 'queue-1', display: 'run focused tests', state: 'pending',
        enqueued_at: '2026-08-30T04:00:00Z',
      }],
      queued_prompts_revision: 3,
    },
  });

  assert.equal(state.sessions.s1.cursor, 41);
  assert.equal(state.sessions.s1.messages[0].content, 'restored');
  assert.equal(state.sessions.s1.interactions.length, 1);
  assert.equal(state.sessions.s1.attention, true);
  assert.equal(state.sessions.s1.queuedPromptRevision, 3);
  assert.deepEqual(state.sessions.s1.queuedPrompts, [{
    id: 'queue-1',
    display: 'run focused tests',
    state: 'pending',
    enqueuedAt: '2026-08-30T04:00:00Z',
    unavailable: false,
  }]);
});

test('queue projection is server-owned across SSE refresh and replay snapshots', () => {
  let state = reducer(initialState(), {
    type: 'SESSION_UPSERT',
    session: { id: 's1', live: true, queuedPrompts: [{ id: 'stale' }] },
  });
  state = reducer(state, {
    type: 'EVENT',
    event: event(1, 'queue.updated', { revision: 1, items: [
      {
        id: 'queue-1', display: 'next task', state: 'pending',
        enqueued_at: '2026-08-30T04:00:00Z', secret: 'not projected',
      },
      { id: '../unsafe', display: 'ignored', state: 'pending' },
      {
        id: 'queue-2', display: '', state: 'pending', unavailable: true,
        enqueued_at: '2026-08-30T04:01:00Z',
      },
    ] }, 's1', ''),
  });
  assert.deepEqual(state.sessions.s1.queuedPrompts, [
    {
      id: 'queue-1', display: 'next task', state: 'pending',
      enqueuedAt: '2026-08-30T04:00:00Z', unavailable: false,
    },
    {
      id: 'queue-2', display: '', state: 'pending',
      enqueuedAt: '2026-08-30T04:01:00Z', unavailable: true,
    },
  ]);
  state = reducer(state, {
    type: 'SESSION_SNAPSHOT', id: 's1', snapshot: {
      session: { id: 's1', live: true, status: 'idle' },
      event_cursor: 2, messages: [], interactions: [], activity: [],
      queued_prompts: [],
      queued_prompts_revision: 2,
    },
  });
  assert.deepEqual(state.sessions.s1.queuedPrompts, []);
  assert.equal(state.sessions.s1.queuedPromptRevision, 2);
});

test('started queued commands promote one user row before queue removal', () => {
  let state = reducer(initialState(), {
    type: 'SESSION_UPSERT',
    session: {
      id: 's1', live: true, status: 'running', active_turn_id: 'turn-1',
      queuedPrompts: [{
        id: 'queue-1', display: 'steer now', state: 'pending',
        enqueuedAt: '2026-08-30T04:00:00Z', unavailable: false,
      }],
      queuedPromptRevision: 1,
    },
  });
  const started = event(1, 'command_lifecycle', {
    CommandUUID: 'queue-1', Phase: 'started',
  }, 's1', 'turn-1');
  state = reducer(state, { type: 'EVENT', event: started });
  assert.equal(state.sessions.s1.queuedPrompts.length, 0);
  assert.deepEqual(
    state.sessions.s1.messages.map((message) => [message.role, message.content]),
    [['user', 'steer now']],
  );

  state = reducer(state, { type: 'EVENT', event: started });
  state = reducer(state, {
    type: 'EVENT',
    event: event(2, 'queue.updated', { revision: 2, items: [] }, 's1', 'turn-1'),
  });
  assert.equal(state.sessions.s1.messages.length, 1);
  assert.equal(state.sessions.s1.queuedPrompts.length, 0);
  assert.equal(state.sessions.s1.queuedPromptRevision, 2);
});

test('queue projections reject stale mutation responses and snapshots', () => {
  let state = reducer(initialState(), {
    type: 'SESSION_UPSERT',
    session: { id: 's1', live: true },
  });
  state = reducer(state, {
    type: 'SESSION_QUEUE_SYNC', id: 's1', revision: 5,
    items: [{
      id: 'queue-new', display: 'newer', state: 'pending',
      enqueued_at: '2026-08-30T04:00:00Z',
    }],
  });
  state = reducer(state, {
    type: 'SESSION_QUEUE_SYNC', id: 's1', revision: 4,
    items: [{
      id: 'queue-old', display: 'older', state: 'pending',
      enqueued_at: '2026-08-30T03:00:00Z',
    }],
  });
  state = reducer(state, {
    type: 'SESSION_SNAPSHOT', id: 's1', snapshot: {
      session: { id: 's1', live: true, status: 'running' },
      event_cursor: 9, messages: [], interactions: [], activity: [],
      queued_prompts_revision: 3, queued_prompts: [],
    },
  });
  assert.equal(state.sessions.s1.queuedPromptRevision, 5);
  assert.equal(state.sessions.s1.queuedPrompts[0].id, 'queue-new');
});

test('runtime queue claim failures project a bounded error and exact recovery', () => {
  let state = reducer(initialState(), {
    type: 'SESSION_UPSERT',
    session: { id: 's1', live: true, status: 'idle' },
  });
  state = reducer(state, {
    type: 'EVENT',
    event: event(1, 'queue.rewake_blocked', {
      code: 'runtime_queue_blocked', secret: 'not rendered',
    }, 's1', ''),
  });
  assert.equal(state.sessions.s1.status, 'error');
  assert.equal(
    state.sessions.s1.last_error,
    'Queued work needs attention before it can continue.',
  );
  assert.equal(state.sessions.s1.notice, state.sessions.s1.last_error);

  state = reducer(state, {
    type: 'EVENT',
    event: event(2, 'queue.rewake_ready', {
      code: 'runtime_queue_ready', secret: 'not rendered',
    }, 's1', ''),
  });
  assert.equal(state.sessions.s1.status, 'idle');
  assert.equal(state.sessions.s1.last_error, '');
  assert.equal(state.sessions.s1.notice, 'Ready.');
});

test('queue attempt identity prevents a late response from owning a newer draft', () => {
  const first = { prompt: 'first', clientQueueID: 'queue-a' };
  assert.equal(sameQueueAttempt(first, { ...first }), true);
  assert.equal(sameQueueAttempt(
    { prompt: 'second', clientQueueID: 'queue-b' }, first,
  ), false);
  assert.equal(sameQueueAttempt(undefined, first), false);
});

test('runtime snapshot replaces Activity with a safe coalesced server projection', () => {
  const secret = 'SECRET_ACTIVITY_PAYLOAD';
  let state = reducer(initialState(), {
    type: 'SESSION_UPSERT',
    session: {
      id: 's1',
      activity: [{ id: 'stale', turnID: 'old', kind: 'turn', state: 'started', category: '', timestamp: '' }],
    },
  });
  state = reducer(state, {
    type: 'SESSION_SNAPSHOT',
    id: 's1',
    snapshot: {
      session: { id: 's1', status: 'running' },
      event_cursor: 12,
      messages: [],
      interactions: [],
      activity: [
        {
          id: 'activity-1', turn_id: 'turn-1', kind: 'tool', state: 'running',
          category: 'command', timestamp: '2026-08-11T10:00:00Z', message: secret,
        },
        {
          id: 'activity-1', turn_id: 'turn-1', kind: 'tool', state: 'completed',
          category: 'command', timestamp: '2026-08-11T10:00:01Z', content: secret,
        },
        {
          id: 'activity-1', turn_id: 'turn-1', kind: 'tool', state: 'running',
          category: 'command', timestamp: '2026-08-11T10:00:02Z', content: secret,
        },
        {
          id: 'unsafe', turn_id: 'turn-1', kind: 'assistant', state: 'completed',
          category: 'tool', timestamp: '2026-08-11T10:00:02Z', error: secret,
        },
      ],
    },
  });

  assert.deepEqual(state.sessions.s1.activity, [{
    id: 'activity-1',
    turnID: 'turn-1',
    kind: 'tool',
    state: 'completed',
    category: 'command',
    timestamp: '2026-08-11T10:00:01Z',
  }]);
  assert.doesNotMatch(JSON.stringify(state.sessions.s1.activity), new RegExp(secret));
});

test('only semantic Activity events change the Activity projection', () => {
  const secret = 'SECRET_RAW_EVENT';
  const rawEvents = [
    event(1, 'user_message', { content: secret }),
    event(2, 'stream_event', { message: { content: secret, reasoning_content: secret } }),
    event(3, 'assistant', { message: { content: secret } }),
    event(4, 'tool_result', { message: { content: secret, tool_name: secret } }),
    event(5, 'canonical_projection', { content: secret }),
    event(6, 'terminal', { error: secret, reason: 'model_error' }),
  ];
  let state = initialState();
  for (const wire of rawEvents) {
    state = reducer(state, { type: 'EVENT', event: wire });
  }
  assert.deepEqual(state.sessions.s1.activity, []);
  assert.equal(state.sessions.s1.cursor, 6);

  state = reducer(state, {
    type: 'EVENT',
    event: event(7, 'activity', {
      id: 'activity-safe', turn_id: 'turn-1', kind: 'interaction', state: 'waiting',
      category: 'question', timestamp: '2026-08-11T10:00:00Z', input: secret,
    }),
  });
  state = reducer(state, {
    type: 'EVENT',
    event: event(8, 'activity', {
      id: 'activity-safe', turn_id: 'turn-1', kind: 'interaction', state: 'resolved',
      timestamp: '2026-08-11T10:00:01Z', message: secret,
    }),
  });
  state = reducer(state, {
    type: 'EVENT',
    event: event(9, 'activity', {
      id: 'activity-safe', turn_id: 'turn-1', kind: 'interaction', state: 'waiting',
      category: 'question', timestamp: '2026-08-11T10:00:02Z', input: secret,
    }),
  });

  assert.deepEqual(state.sessions.s1.activity, [{
    id: 'activity-safe',
    turnID: 'turn-1',
    kind: 'interaction',
    state: 'resolved',
    category: 'question',
    timestamp: '2026-08-11T10:00:01Z',
  }]);
  assert.doesNotMatch(JSON.stringify(state.sessions.s1.activity), new RegExp(secret));
});

test('semantic Activity keeps only the newest 100 coalesced identities', () => {
  let state = initialState();
  for (let index = 0; index < 105; index += 1) {
    state = reducer(state, {
      type: 'EVENT',
      event: event(index + 1, 'activity', {
        id: `activity-${index}`,
        turn_id: 'turn-1',
        kind: 'tool',
        state: 'completed',
        category: 'file_read',
        timestamp: `2026-08-11T10:00:${String(index % 60).padStart(2, '0')}Z`,
      }),
    });
  }
  assert.equal(state.sessions.s1.activity.length, 100);
  assert.equal(state.sessions.s1.activity[0].id, 'activity-5');
  assert.equal(state.sessions.s1.activity.at(-1).id, 'activity-104');
});

test('tool and terminal events remain visible and preserve terminal status', () => {
  let state = reducer(initialState(), {
    type: 'EVENT',
    event: event(1, 'tool_result', {
      message: { content: 'done', tool_call_id: 'call-1' },
    }),
  });
  state = reducer(state, {
    type: 'EVENT',
    event: event(2, 'turn.finished', { reason: 'waiting_input' }),
  });
  assert.equal(state.sessions.s1.messages[0].role, 'tool');
  assert.equal(state.sessions.s1.status, 'waiting');
  assert.equal(state.sessions.s1.notice, 'Waiting for input.');

  state = reducer(state, {
    type: 'EVENT',
    event: event(3, 'turn.finished', { error: 'boom' }),
  });
  assert.equal(state.sessions.s1.status, 'error');
  assert.equal(state.sessions.s1.last_error, 'boom');
});

test('successful terminal events clear the working notice', () => {
  let state = reducer(initialState(), {
    type: 'EVENT',
    event: event(1, 'turn.accepted', { turn_id: 'turn-1' }),
  });
  assert.equal(state.sessions.s1.notice, 'Agent is working.');

  state = reducer(state, {
    type: 'EVENT',
    event: event(2, 'turn.finished', { reason: 'completed' }),
  });
  assert.equal(state.sessions.s1.status, 'idle');
  assert.equal(state.sessions.s1.notice, 'Ready');
});

test('user cancellation settles partial output and restores the idle composer', () => {
  let state = reducer(initialState(), {
    type: 'SESSION_UPSERT',
    session: { id: 's1', live: true, activation: 'live' },
  });
  state = reducer(state, {
    type: 'EVENT',
    event: event(1, 'turn.accepted', { turn_id: 'turn-a' }, 's1', 'turn-a'),
  });
  state = reducer(state, {
    type: 'EVENT',
    event: event(2, 'stream_event', {
      message: { content: 'partial response' },
    }, 's1', 'turn-a'),
  });
  state = reducer(state, {
    type: 'EVENT',
    event: event(3, 'turn.cancel.requested', {
      mode: 'immediate',
      reason: 'desktop user requested cancellation',
    }, 's1', 'turn-a'),
  });
  assert.equal(state.sessions.s1.status, 'stopping');
  assert.equal(state.sessions.s1.notice, 'Stopping the active turn…');

  state = reducer(state, {
    type: 'EVENT',
    event: event(4, 'turn.finished', {
      reason: 'aborted_streaming',
      error: '',
    }, 's1', 'turn-a'),
  });
  const session = state.sessions.s1;
  assert.equal(session.active_turn_id, '');
  assert.equal(session.status, 'idle');
  assert.equal(session.notice, 'Ready');
  assert.deepEqual(session.settledTurnIDs, ['turn-a']);
  assert.equal(session.messages.at(-1).content, 'partial response');
  assert.equal(session.messages.at(-1).completed, true);
  assert.equal(canEditDraft(session), true);
  assert.equal(canSubmitTurn(session), true);
});

test('typed interactions retain request order, drafts, and failed resolutions', () => {
  let state = reducer(initialState(), { type: 'EVENT', event: event(1,
    'interaction_requested', { interaction: { request_id: 'one', kind: 'permission' } }) });
  state = reducer(state, { type: 'EVENT', event: event(2,
    'interaction_requested', { interaction: { request_id: 'two', kind: 'question' } }) });
  state = reducer(state, { type: 'INTERACTION_DRAFT_UPDATE', id: 's1', requestID: 'two',
    patch: { answers: { q: { optionIDs: ['a'], text: '' } } } });
  state = reducer(state, { type: 'EVENT', event: event(3,
    'interaction_requested', { interaction: { request_id: 'one', kind: 'permission', permission: { grant_scopes: ['allow_once'] } } }) });
  assert.deepEqual(state.sessions.s1.interactions.map((item) => item.request_id), ['one', 'two']);
  assert.equal(activeInteraction(state.sessions.s1).request_id, 'one');
  assert.deepEqual(interactionDraft(state.sessions.s1, 'two').answers, { q: { optionIDs: ['a'], text: '' } });
  state = reducer(state, { type: 'INTERACTION_SUBMITTING', id: 's1', requestID: 'one' });
  state = reducer(state, { type: 'INTERACTION_SUBMIT_FAILED', id: 's1', requestID: 'one' });
  assert.equal(state.sessions.s1.resolvingRequestID, '');
  assert.equal(activeInteraction(state.sessions.s1).request_id, 'one');
  state = reducer(state, { type: 'EVENT', event: event(4,
    'interaction_resolved', { request_id: 'one' }) });
  assert.deepEqual(state.sessions.s1.interactions.map((item) => item.request_id), ['two']);
});

test('late interaction requests cannot recreate forms after resolution', () => {
  let state = reducer(initialState(), { type: 'EVENT', event: event(1,
    'interaction_requested', { interaction: { request_id: 'plan-1', kind: 'plan_approval' } }) });
  state = reducer(state, { type: 'INTERACTION_PLAN_LOADING', id: 's1', requestID: 'plan-1' });
  state = reducer(state, { type: 'EVENT', event: event(2,
    'interaction_resolved', { request_id: 'plan-1' }) });

  const settled = state;
  for (const action of [
    { type: 'INTERACTION_DRAFT_UPDATE', patch: { feedback: 'late' } },
    { type: 'INTERACTION_PLAN_SUCCESS', review: { revision: 1, digest: 'late' } },
    { type: 'INTERACTION_PLAN_FAILED', error: 'late' },
    { type: 'INTERACTION_SUBMITTING' },
    { type: 'INTERACTION_SUBMIT_FAILED', error: 'late' },
  ]) {
    state = reducer(state, {
      ...action,
      id: 's1',
      requestID: 'plan-1',
    });
    assert.equal(state, settled);
  }
  assert.deepEqual(state.sessions.s1.interactionForms, {});
  assert.equal(state.sessions.s1.resolvingRequestID, '');
});

test('waiting input remains resumable by its matching interaction resolution', () => {
  let state = reducer(initialState(), {
    type: 'EVENT', event: event(1, 'turn.accepted', { turn_id: 'turn-1' }),
  });
  state = reducer(state, {
    type: 'EVENT',
    event: event(2, 'interaction_requested', {
      interaction: { request_id: 'question-1', kind: 'question' },
    }),
  });
  state = reducer(state, {
    type: 'EVENT', event: event(3, 'turn.finished', { reason: 'waiting_input' }),
  });
  state = reducer(state, {
    type: 'EVENT',
    event: event(4, 'interaction_resolved', { request_id: 'question-1' }),
  });

  assert.deepEqual(state.sessions.s1.interactions, []);
  assert.equal(state.sessions.s1.attention, false);
  assert.equal(state.sessions.s1.settledTurnIDs.includes('turn-1'), false);
});

test('plan review state accepts only the exact bounded server revision and digest', () => {
  const digest = `sha256:${'a'.repeat(64)}`;
  let state = reducer(initialState(), { type: 'EVENT', event: event(1,
    'interaction_requested', { interaction: {
      request_id: 'plan-1', kind: 'plan_approval', plan_approval: { revision: 2 },
    } }) });

  for (const review of [
    { content: '# stale', revision: 1, digest },
    { content: '# invalid digest', revision: 2, digest: 'sha256:invalid' },
    { content: 'x'.repeat((1 << 20) + 1), revision: 2, digest },
  ]) {
    state = reducer(state, {
      type: 'INTERACTION_PLAN_SUCCESS', id: 's1', requestID: 'plan-1', review,
    });
    assert.equal(interactionDraft(state.sessions.s1, 'plan-1').planReview.status, 'error');
  }

  state = reducer(state, {
    type: 'INTERACTION_PLAN_SUCCESS', id: 's1', requestID: 'plan-1',
    review: { content: '# reviewed', revision: 2, digest, private_path: '/secret' },
  });
  assert.deepEqual(interactionDraft(state.sessions.s1, 'plan-1').planReview, {
    status: 'ready', content: '# reviewed', revision: 2, digest, error: '',
  });
});

test('typed interaction result builders enforce the server capabilities', () => {
  const reviewedDigest = `sha256:${'b'.repeat(64)}`;
  assert.deepEqual(buildPermissionResolution({ permission: { grant_scopes: ['allow_once'] } },
    { decision: 'allow_once' }), { kind: 'permission', permission: { decision: 'allow_once' } });
  assert.throws(() => buildPermissionResolution({ permission: { grant_scopes: ['allow_once'] } },
    { decision: 'allow_once', message: 'grant reason' }));
  assert.deepEqual(buildPermissionResolution({ permission: { grant_scopes: [] } },
    { decision: 'deny', message: 'not now' }), { kind: 'permission', permission: { decision: 'deny', message: 'not now' } });
  assert.throws(() => buildPermissionResolution({ permission: { grant_scopes: [] } }, { decision: 'allow_session' }));
  const question = { question: { questions: [{ id: 'q', multi_select: false,
    options: [{ id: 'a' }, { id: 'b' }], free_text: false }] } };
  assert.deepEqual(buildQuestionResolution(question, { outcome: 'submit', answers: [{ question_id: 'q', option_ids: ['a'] }] }),
    { kind: 'question', question: { outcome: 'submit', answers: [{ question_id: 'q', option_ids: ['a'] }] } });
  assert.deepEqual(buildQuestionResolution(question, { outcome: 'submit', answers: [{ question_id: 'q', text: 'Other answer' }] }),
    { kind: 'question', question: { outcome: 'submit', answers: [{ question_id: 'q', text: 'Other answer' }] } });
  assert.throws(() => buildQuestionResolution(question, { outcome: 'submit', answers: [{ question_id: 'q', option_ids: ['missing'] }] }));
  assert.throws(() => buildQuestionResolution(question, { outcome: 'submit', answers: [{ question_id: 'q', text: 'x'.repeat(16 * 1024 + 1) }] }));
  const plan = { plan_approval: { revision: 2, target_modes: ['default', 'bypassPermissions'] } };
  assert.deepEqual(buildPlanResolution(plan, { outcome: 'approve', target_mode: 'bypassPermissions', confirmed: true },
    { status: 'ready', revision: 2, digest: reviewedDigest }), { kind: 'plan_approval', plan_approval: { outcome: 'approve', revision: 2, target_mode: 'bypassPermissions', reviewed_digest: reviewedDigest, confirmed: true } });
  assert.throws(() => buildPlanResolution(plan, { outcome: 'approve', target_mode: 'default', confirmed: false },
    { status: 'error', revision: 2, digest: 'sha256:stale' }));
  assert.throws(() => buildPlanResolution(plan, { outcome: 'approve', target_mode: 'default', confirmed: false }, null));
  assert.deepEqual(buildRepeatedToolResolution({ repeated_tool: { outcomes: ['continue'] } }, { outcome: 'continue' }),
    { kind: 'repeated_tool', repeated_tool: { outcome: 'continue' } });
});

test('same-session event replay is idempotent', () => {
  const state = reducer(initialState(), {
    type: 'EVENT',
    event: event(4, 'user_message', { content: 'once' }),
  });
  assert.equal(
    reducer(state, {
      type: 'EVENT',
      event: event(4, 'user_message', { content: 'once' }),
    }),
    state,
  );
});

test('transcript pages prepend idempotently and retain the next cursor', () => {
  let state = reducer(initialState(), {
    type: 'SESSION_UPSERT',
    session: { id: 's1', workspace_label: '/tmp/project' },
  });
  state = reducer(state, {
    type: 'SESSION_TRANSCRIPT_PAGE',
    id: 's1',
    replace: true,
    page: {
      messages: [
        { id: 'newer', role: 'assistant', content: 'newer' },
      ],
      next_cursor: 'older-page',
      has_more: true,
    },
  });
  state = reducer(state, {
    type: 'SESSION_TRANSCRIPT_PAGE',
    id: 's1',
    page: {
      messages: [
        { id: 'older', role: 'user', content: 'older' },
        { id: 'newer', role: 'assistant', content: 'newer' },
      ],
      next_cursor: '',
      has_more: false,
    },
  });
  assert.deepEqual(
    state.sessions.s1.messages.map((message) => message.id),
    ['older', 'newer'],
  );
  assert.equal(state.sessions.s1.transcriptNextCursor, '');
  assert.equal(state.sessions.s1.transcriptHasMore, false);
});

test('runtime snapshot merges live messages into durable transcript history', () => {
  let state = reducer(initialState(), {
    type: 'SESSION_UPSERT',
    session: { id: 's1', workspace_label: '/tmp/project' },
  });
  state = reducer(state, {
    type: 'SESSION_TRANSCRIPT_PAGE',
    id: 's1',
    replace: true,
    page: {
      messages: [{ id: 'durable', role: 'user', content: 'durable' }],
    },
  });
  state = reducer(state, {
    type: 'SESSION_SNAPSHOT',
    id: 's1',
    snapshot: {
      session: { id: 's1', workspace_label: '/tmp/project', status: 'running' },
      event_cursor: 17,
      messages: [
        { id: 'durable', role: 'user', content: 'durable', completed: true },
        { id: 'live', role: 'assistant', content: 'partial', completed: false },
      ],
      interactions: [],
    },
  });
  assert.deepEqual(
    state.sessions.s1.messages.map((message) => message.id),
    ['durable', 'live'],
  );
  assert.equal(state.sessions.s1.cursor, 17);
});

test('snapshot and transcript copies with one durable id render once', () => {
  let state = reducer(initialState(), {
    type: 'SESSION_UPSERT',
    session: { id: 's1', workspace_label: '/tmp/project' },
  });
  state = reducer(state, {
    type: 'SESSION_TRANSCRIPT_PAGE',
    id: 's1',
    replace: true,
    page: {
      messages: [{
        id: 'durable-1', role: 'assistant', content: 'answer', source: 'durable',
      }],
    },
  });
  state = reducer(state, {
    type: 'SESSION_SNAPSHOT',
    id: 's1',
    snapshot: {
      session: { id: 's1', workspace_label: '/tmp/project' },
      messages: [{
        id: 'durable-1', role: 'assistant', content: 'answer', source: 'durable', completed: true,
      }],
      interactions: [],
    },
  });
  assert.deepEqual(state.sessions.s1.messages.map((message) => message.id), ['durable-1']);
});

test('equal text with distinct durable ids remains distinct', () => {
  let state = reducer(initialState(), {
    type: 'SESSION_UPSERT',
    session: { id: 's1', workspace_label: '/tmp/project' },
  });
  state = reducer(state, {
    type: 'SESSION_TRANSCRIPT_PAGE',
    id: 's1',
    replace: true,
    page: {
      messages: [
        { id: 'durable-1', role: 'user', content: 'again', source: 'durable' },
        { id: 'durable-2', role: 'user', content: 'again', source: 'durable' },
      ],
    },
  });
  assert.deepEqual(
    state.sessions.s1.messages.map((message) => message.id),
    ['durable-1', 'durable-2'],
  );
});

test('transcript replacement removes fallback copies but keeps live tail', () => {
  let state = reducer(initialState(), {
    type: 'SESSION_UPSERT',
    session: { id: 's1', workspace_label: '/tmp/project' },
  });
  state = reducer(state, {
    type: 'SESSION_SNAPSHOT',
    id: 's1',
    snapshot: {
      session: { id: 's1', workspace_label: '/tmp/project' },
      messages: [
        { role: 'user', content: 'fallback', source: 'conversation-fallback', completed: true },
        { id: 'runtime-1', role: 'assistant', content: 'partial', source: 'runtime', completed: false },
      ],
      interactions: [],
    },
  });
  state = reducer(state, {
    type: 'SESSION_TRANSCRIPT_PAGE',
    id: 's1',
    replace: true,
    page: {
      messages: [{
        id: 'durable-1', role: 'user', content: 'fallback', source: 'durable',
      }],
    },
  });
  assert.deepEqual(
    state.sessions.s1.messages.map((message) => message.id),
    ['durable-1', 'runtime-1'],
  );
  assert.deepEqual(
    state.sessions.s1.messages.map((message) => message.source),
    ['durable', 'runtime'],
  );
});

test('stale replay transcript replacement cannot erase a newer stream message', () => {
  let state = reducer(initialState(), {
    type: 'SESSION_SNAPSHOT',
    id: 's1',
    snapshot: {
      session: { id: 's1', workspace_label: '/tmp/project', live: true },
      event_cursor: 41,
      messages: [],
      interactions: [],
    },
  });
  state = reducer(state, {
    type: 'EVENT',
    event: event(42, 'user_message', { content: 'arrived after snapshot' }),
  });

  const afterStreamEvent = state;
  state = reducer(state, {
    type: 'SESSION_TRANSCRIPT_PAGE',
    id: 's1',
    replace: true,
    eventCursorFence: 41,
    page: {
      messages: [{ id: 'durable-before-gap', role: 'user', content: 'older' }],
    },
  });

  assert.equal(state, afterStreamEvent);
  assert.equal(state.sessions.s1.cursor, 42);
  assert.deepEqual(
    state.sessions.s1.messages.map((message) => message.content),
    ['arrived after snapshot'],
  );
});

test('replay transcript replacement applies while the snapshot cursor is current', () => {
  let state = reducer(initialState(), {
    type: 'SESSION_SNAPSHOT',
    id: 's1',
    snapshot: {
      session: { id: 's1', workspace_label: '/tmp/project', live: true },
      event_cursor: 41,
      messages: [],
      interactions: [],
    },
  });
  state = reducer(state, {
    type: 'SESSION_TRANSCRIPT_PAGE',
    id: 's1',
    replace: true,
    eventCursorFence: 41,
    page: {
      messages: [{ id: 'durable-before-gap', role: 'user', content: 'older' }],
    },
  });

  assert.equal(state.sessions.s1.cursor, 41);
  assert.deepEqual(
    state.sessions.s1.messages.map((message) => message.id),
    ['durable-before-gap'],
  );
});

test('resume replacement discards completed canonical assistant projections', () => {
  let state = reducer(initialState(), {
    type: 'EVENT',
    event: event(1, 'turn.accepted', { turn_id: 'turn-1' }),
  });
  state = reducer(state, {
    type: 'EVENT',
    event: event(2, 'assistant', {
      source: 'canonical_projection',
      message: { content: 'Read', tool_calls: [{ id: 'call-read' }] },
    }),
  });
  state = reducer(state, {
    type: 'EVENT',
    event: event(3, 'tool_result', {
      source: 'durable',
      transcript_entry_id: 'durable-tool',
      message: { content: 'hello', tool_call_id: 'call-read' },
    }),
  });
  state = reducer(state, {
    type: 'EVENT',
    event: event(4, 'assistant', {
      source: 'canonical_projection',
      message: { content: 'done' },
    }),
  });
  state = reducer(state, {
    type: 'EVENT',
    event: event(5, 'turn.finished', { reason: 'completed' }),
  });
  assert.ok(state.sessions.s1.messages
    .filter((message) => message.role === 'assistant')
    .every((message) => message.completed));

  state = reducer(state, {
    type: 'SESSION_TRANSCRIPT_PAGE',
    id: 's1',
    replace: true,
    page: {
      messages: [
        { id: 'durable-call', role: 'assistant', content: '', source: 'durable' },
        { id: 'durable-tool', role: 'tool', content: 'hello', source: 'durable' },
        { id: 'durable-final', role: 'assistant', content: 'done', source: 'durable' },
      ],
    },
  });
  assert.deepEqual(
    state.sessions.s1.messages.map((message) => message.id),
    ['durable-call', 'durable-tool', 'durable-final'],
  );
  assert.ok(state.sessions.s1.messages.every((message) => message.source === 'durable'));
});

test('a persisted user message marks the session as resumable', () => {
  let state = reducer(initialState(), {
    type: 'SESSION_UPSERT',
    session: { id: 's1', workspace_label: '/tmp/project', durable: false },
  });
  state = reducer(state, {
    type: 'EVENT',
    event: event(1, 'user_message', { content: 'persist me' }),
  });
  assert.equal(state.sessions.s1.durable, true);
  assert.equal(state.sessions.s1.resumable, true);
});

test('live discovery does not invent durable state after renderer reload', () => {
  const summary = { id: 's1', workspace_label: '/tmp/project', status: 'idle' };
  assert.deepEqual(liveDescriptor({}, summary), {
    ...summary,
    draft: '',
    durable: false,
    resumable: false,
    import_required: false,
    live: true,
    activation: 'live',
  });
  assert.equal(liveDescriptor({
    id: 's1', durable: true, resumable: true, draft: 'keep',
  }, summary).resumable, true);
  assert.equal(liveDescriptor({
    id: 'child', durable: true, resumable: false,
  }, { ...summary, id: 'child' }).resumable, false);
});

test('persisted descriptors cannot authorize resume before catalog verification', () => {
  assert.deepEqual(unverifiedPersistedDescriptor({
    id: 'child',
    workspace_label: '/tmp/project',
    durable: true,
    resumable: true,
    status: 'idle',
    active_turn_id: 'stale-turn',
  }), {
    id: 'child',
    workspace_label: '/tmp/project',
    durable: true,
    resumable: false,
    import_required: false,
    status: 'offline',
    active_turn_id: '',
    live: false,
    activation: 'detached',
    notice: 'Checking durable session metadata…',
  });
});

test('closing a live durable session retains its descriptor but clears live state', () => {
  let state = reducer(initialState(), {
    type: 'SESSION_UPSERT',
    session: { id: 's1', workspace_label: '/tmp/project', durable: true, resumable: true, live: true },
  });
  state = reducer(state, {
    type: 'EVENT',
    event: event(1, 'session.closed'),
  });
  assert.equal(state.sessions.s1.live, false);
  assert.equal(state.sessions.s1.durable, true);
  assert.equal(state.sessions.s1.resumable, true);
});

test('closing durable state never promotes a non-resumable session', () => {
  assert.deepEqual(retainedClosedDescriptor({
    id: 'child',
    durable: true,
    resumable: false,
    live: true,
    status: 'idle',
  }), {
    id: 'child',
    durable: true,
    resumable: false,
    import_required: false,
    live: false,
    status: 'archived',
    active_turn_id: '',
    attention: false,
    activation: 'detached',
    notice: 'This durable session is available as read-only history.',
  });
  assert.equal(retainedClosedDescriptor({
    id: 'empty', durable: false, resumable: false,
  }), null);
});

test('session descriptors retain durable discovery metadata', () => {
  const state = reducer(initialState(), {
    type: 'SESSION_UPSERT',
    session: {
      id: 's1',
      workspace_label: '/tmp/project',
      title: 'Project',
      durable: true,
      resumable: true,
      git_branch: 'feat/desktop',
      updated_at: '2026-07-27T01:00:00Z',
      execution: { status: 'ready', model: 'private-model' },
    },
  });
  assert.deepEqual(descriptors(state), [{
    id: 's1',
    workspace_label: '/tmp/project',
    title: 'Project',
    durable: true,
    resumable: true,
    import_required: false,
    git_branch: 'feat/desktop',
    created_at: '',
    updated_at: '2026-07-27T01:00:00Z',
  }]);
});

test('durable catalog metadata cannot overwrite live session state', () => {
  const liveMessages = [{ id: 'live-message', content: 'partial' }];
  const liveInteractions = [{ request_id: 'interaction-1', kind: 'permission' }];
  const liveActivity = [{ type: 'turn.accepted' }];
  const liveReview = { status: 'ready', sources: [{ id: 'worktree' }] };
  const liveExecution = { model: 'current-model', status: 'ready' };
  let state = reducer(initialState(), {
    type: 'SESSION_UPSERT',
    session: {
      id: 's1',
      workspace_label: '/live/worktree',
      title: 'Live title',
      status: 'running',
      active_turn_id: 'turn-1',
      last_error: 'live error',
      attention: true,
      draft: 'keep draft',
      messages: liveMessages,
      interactions: liveInteractions,
      activity: liveActivity,
      execution: liveExecution,
      review: liveReview,
      live: true,
    },
  });

  state = reducer(state, {
    type: 'DURABLE_SESSION_PAGE',
    replace: true,
    sessions: [{
      id: 's1',
      workspace_label: '/durable/worktree',
      title: 'Durable title',
      status: 'saved',
      active_turn_id: '',
      git_branch: 'feat/catalog',
      parent_session_id: 'parent-1',
      updated_at: '2026-08-07T02:00:00Z',
      resumable: true,
    }],
  });

  const session = state.sessions.s1;
  assert.equal(session.live, true);
  assert.equal(session.status, 'running');
  assert.equal(session.active_turn_id, 'turn-1');
  assert.equal(session.last_error, 'live error');
  assert.equal(session.attention, true);
  assert.equal(session.draft, 'keep draft');
  assert.equal(session.messages, liveMessages);
  assert.equal(session.interactions, liveInteractions);
  assert.equal(session.activity, liveActivity);
  assert.equal(session.execution.model, liveExecution.model);
  assert.equal(session.execution.status, liveExecution.status);
  assert.equal(session.review.sources[0].id, 'worktree');
  assert.equal(session.workspace_label, '/durable/worktree');
  assert.equal(session.title, 'Durable title');
  assert.equal(session.git_branch, 'feat/catalog');
  assert.equal(session.parent_session_id, 'parent-1');
  assert.equal(session.updated_at, '2026-08-07T02:00:00Z');
  assert.equal(session.durable, true);
  assert.equal(session.resumable, true);
});

test('durable catalog pages merge one row per session id', () => {
  let state = reducer(initialState(), {
    type: 'DURABLE_SESSION_PAGE',
    replace: true,
    sessions: [
      { id: 's1', title: 'First page', resumable: true },
      { id: '', title: 'Invalid' },
    ],
  });
  state = reducer(state, {
    type: 'DURABLE_SESSION_PAGE',
    replace: false,
    sessions: [
      { id: 's1', title: 'Updated title', resumable: true },
      { id: 's2', title: 'Second session', resumable: true },
    ],
  });

  assert.deepEqual(Object.keys(state.sessions).sort(), ['s1', 's2']);
  assert.equal(state.sessions.s1.title, 'Updated title');
  assert.equal(state.sessions.s1.live, false);
  assert.equal(state.sessions.s1.durable, true);
  assert.equal(state.sessions.s2.resumable, true);
});

test('durable lifecycle metadata stays detached until explicit attach', () => {
  const state = reducer(initialState(), {
    type: 'DURABLE_SESSION_PAGE',
    replace: true,
    sessions: [{
      id: 'session-legacy',
      title: 'Saved work',
      status: 'completed',
      resumable: true,
    }],
  });
  const session = state.sessions['session-legacy'];
  assert.equal(session.status, 'saved');
  assert.equal(session.live, false);
  assert.equal(session.resumable, true);
  assert.equal(canSubmitTurn(session), true);
});

test('non-resumable catalog rows remain read-only', () => {
  const state = reducer(initialState(), {
    type: 'DURABLE_SESSION_PAGE',
    sessions: [{ id: 'child-session', status: 'completed', resumable: false }],
  });
  assert.equal(state.sessions['child-session'].status, 'archived');
  assert.equal(state.sessions['child-session'].resumable, false);
  assert.equal(canSubmitTurn(state.sessions['child-session']), false);
});

test('legacy catalog rows require explicit import before attach authority', () => {
  let state = reducer(initialState(), {
    type: 'DURABLE_SESSION_PAGE',
    sessions: [{
      id: 'legacy-session', status: 'saved', resumable: false,
      import_required: true,
    }],
  });
  assert.equal(state.sessions['legacy-session'].status, 'import required');
  assert.equal(state.sessions['legacy-session'].resumable, false);
  assert.equal(canEditDraft(state.sessions['legacy-session']), true);
  assert.equal(canSubmitTurn(state.sessions['legacy-session']), false);
  assert.equal(canImportDurableSession(state.sessions['legacy-session']), true);

  state = reducer(state, { type: 'DURABLE_IMPORT_STARTED', id: 'legacy-session' });
  assert.equal(state.sessions['legacy-session'].activation, 'importing');
  assert.equal(canImportDurableSession(state.sessions['legacy-session']), false);
  state = reducer(state, {
    type: 'DURABLE_IMPORT_FAILED', id: 'legacy-session', error: 'catalog unavailable',
  });
  assert.equal(state.sessions['legacy-session'].import_required, true);
  assert.equal(canImportDurableSession(state.sessions['legacy-session']), true);

  state = reducer(state, { type: 'DURABLE_IMPORT_STARTED', id: 'legacy-session' });
  state = reducer(state, { type: 'DURABLE_IMPORT_COMPLETED', id: 'legacy-session' });
  assert.equal(state.sessions['legacy-session'].import_required, false);
  assert.equal(canSubmitTurn(state.sessions['legacy-session']), false);

  state = reducer(state, {
    type: 'DURABLE_SESSION_PAGE',
    sessions: [{
      id: 'legacy-session', status: 'saved', resumable: true,
      import_required: false,
    }],
  });
  assert.equal(canSubmitTurn(state.sessions['legacy-session']), true);
});

test('late events from a settled turn cannot corrupt a newer active turn', () => {
  let state = reducer(initialState(), {
    type: 'EVENT', event: event(1, 'turn.accepted', { turn_id: 'turn-a' }, 's1', 'turn-a'),
  });
  state = reducer(state, {
    type: 'EVENT', event: event(2, 'turn.finished', { reason: 'aborted_streaming' }, 's1', 'turn-a'),
  });
  const active = reducer(state, {
    type: 'EVENT', event: event(3, 'turn.accepted', { turn_id: 'turn-b' }, 's1', 'turn-b'),
  });
  state = reducer(active, {
    type: 'EVENT', event: event(4, 'turn.finished', { reason: 'completed' }, 's1', 'turn-a'),
  });
  assert.equal(state.sessions.s1.active_turn_id, 'turn-b');
  assert.equal(state.sessions.s1.status, 'running');

  const lateEvents = [
    ['assistant', { message: { content: 'late-a' } }],
    ['stream_event', { message: { content: 'late-stream-a' } }],
    ['tool_result', { message: { content: 'late-tool-a' } }],
    ['user_message', { message: { content: 'late-user-a' } }],
    ['turn.cancel.requested', {}],
    ['interaction_requested', {
      interaction: { request_id: 'late-interaction-a', kind: 'permission' },
    }],
    ['interaction_resolved', { request_id: 'late-interaction-a' }],
    ['turn.accepted', { turn_id: 'turn-a' }],
  ];
  for (const [type, data] of lateEvents) {
    const projected = reducer(active, {
      type: 'EVENT', event: event(4, type, data, 's1', 'turn-a'),
    });
    assert.equal(projected.sessions.s1.active_turn_id, 'turn-b', type);
    assert.equal(projected.sessions.s1.status, 'running', type);
    assert.equal(projected.sessions.s1.attention, false, type);
    assert.deepEqual(projected.sessions.s1.messages, active.sessions.s1.messages, type);
    assert.deepEqual(projected.sessions.s1.interactions, [], type);
  }
});

test('catalog resumability overrides stale non-live persistence', () => {
  let state = reducer(initialState(), {
    type: 'SESSION_UPSERT',
    session: {
      id: 'child-session',
      workspace_label: '/tmp/project',
      durable: true,
      resumable: true,
      live: false,
      status: 'idle',
    },
  });
  state = reducer(state, {
    type: 'DURABLE_SESSION_PAGE',
    sessions: [{
      id: 'child-session',
      workspace_label: '/tmp/project',
      status: 'completed',
      resumable: false,
    }],
  });
  assert.equal(state.sessions['child-session'].resumable, false);
  assert.equal(state.sessions['child-session'].status, 'archived');
  assert.equal(state.sessions['child-session'].live, false);
});

test('verified durable catalog rows normalize to detached activation without execution authority', () => {
  const state = reducer(initialState(), {
    type: 'DURABLE_SESSION_PAGE',
    sessions: [{ id: 'resumable', resumable: true }, { id: 'archived', resumable: false }],
  });
  assert.equal(state.sessions.resumable.activation, 'detached');
  assert.equal(canEditDraft(state.sessions.resumable), true);
  assert.equal(canSubmitTurn(state.sessions.resumable), true);
  assert.equal(state.sessions.resumable.execution.status, 'idle');
  assert.equal(state.sessions.archived.activation, 'detached');
  assert.equal(canEditDraft(state.sessions.archived), false);
  assert.equal(canSubmitTurn(state.sessions.archived), false);
  assert.equal(canEditDraft(unverifiedPersistedDescriptor({
    id: 'unverified', durable: true, resumable: true,
  })), false);
});

test('detached attach transitions retain history and draft until matching server acceptance', () => {
  const messages = [{ id: 'history', content: 'saved' }];
  const clientTurnID = 'client-turn-1';
  let state = reducer(initialState(), {
    type: 'SESSION_UPSERT',
    session: { id: 's1', durable: true, resumable: true, messages, draft: 'attach this' },
  });
  assert.equal(state.sessions.s1.activation, 'detached');
  assert.equal(canEditDraft(state.sessions.s1), true);
  assert.equal(canSubmitTurn(state.sessions.s1), true);
  state = reducer(state, { type: 'ATTACH_STARTED', id: 's1' });
  assert.equal(state.sessions.s1.activation, 'attaching');
  assert.equal(state.sessions.s1.messages, messages);
  assert.equal(state.sessions.s1.draft, 'attach this');
  assert.equal(canEditDraft(state.sessions.s1), false);
  assert.equal(canSubmitTurn(state.sessions.s1), false);

  const stale = reducer(state, {
    type: 'ATTACH_ACCEPTED', id: 's1', clientTurnID, response: {
      status: 'turn_accepted', session: { id: 'other' },
      client_turn_id: clientTurnID, turn_id: 'turn-2',
    },
  });
  assert.equal(stale, state);

  state = reducer(state, {
    type: 'ATTACH_ACCEPTED', id: 's1', clientTurnID, response: {
      status: 'turn_accepted', session: { id: 's1', title: 'Attached' },
      client_turn_id: clientTurnID, turn_id: 'turn-2',
    },
  });
  assert.equal(state.sessions.s1.live, true);
  assert.equal(state.sessions.s1.activation, 'live');
  assert.equal(state.sessions.s1.status, 'running');
  assert.equal(state.sessions.s1.active_turn_id, 'turn-2');
  assert.equal(state.sessions.s1.draft, '');
  assert.equal(state.sessions.s1.messages, messages);
  const afterLateFailure = reducer(state, {
    type: 'ATTACH_FAILED', id: 's1', error: 'late failure',
  });
  assert.equal(afterLateFailure, state);
});

test('attach interaction and failure preserve draft history and explicit retry admission', () => {
  const messages = [{ id: 'history', content: 'saved' }];
  const clientTurnID = 'client-turn-2';
  let state = reducer(initialState(), {
    type: 'SESSION_UPSERT',
    session: { id: 's1', durable: true, resumable: true, messages, draft: 'keep me' },
  });
  state = reducer(state, { type: 'ATTACH_STARTED', id: 's1' });
  state = reducer(state, {
    type: 'ATTACH_INTERACTION_REQUIRED', id: 's1', clientTurnID, response: {
      status: 'interaction_required',
      session: { id: 's1', active_turn_id: 'restored-turn' },
      client_turn_id: clientTurnID,
      interaction: {
        request_id: 'recover-1', turn_id: 'restored-turn', kind: 'permission',
        permission: { available: false, evidence: [], grant_scopes: ['allow_once'] },
      },
    },
  });
  assert.equal(state.sessions.s1.live, true);
  assert.equal(state.sessions.s1.activation, 'interaction_required');
  assert.equal(state.sessions.s1.active_turn_id, 'restored-turn');
  assert.equal(state.sessions.s1.draft, 'keep me');
  assert.equal(state.sessions.s1.messages, messages);
  assert.equal(activeInteraction(state.sessions.s1).request_id, 'recover-1');
  assert.equal(canEditDraft(state.sessions.s1), true);
  assert.equal(canSubmitTurn(state.sessions.s1), false);

  state = reducer(state, { type: 'SESSION_UPSERT', session: {
    id: 'retry', durable: true, resumable: true, messages, draft: 'retry me',
  } });
  state = reducer(state, { type: 'ATTACH_STARTED', id: 'retry' });
  state = reducer(state, { type: 'ATTACH_FAILED', id: 'retry', error: 'lease lost' });
  assert.equal(state.sessions.retry.live, false);
  assert.equal(state.sessions.retry.activation, 'failed');
  assert.equal(state.sessions.retry.draft, 'retry me');
  assert.equal(state.sessions.retry.messages, messages);
  assert.equal(canEditDraft(state.sessions.retry), true);
  assert.equal(canSubmitTurn(state.sessions.retry), true);
});

test('review responses ignore stale request generations', () => {
  let state = reducer(initialState(), {
    type: 'SESSION_UPSERT',
    session: { id: 's1', workspace_label: '/tmp/project' },
  });
  state = reducer(state, {
    type: 'SESSION_REVIEW_LOADING',
    id: 's1',
    requestID: 1,
    ignoreWhitespace: false,
  });
  state = reducer(state, {
    type: 'SESSION_REVIEW_LOADING',
    id: 's1',
    requestID: 2,
    ignoreWhitespace: true,
  });
  const unchanged = reducer(state, {
    type: 'SESSION_REVIEW_SUCCESS',
    id: 's1',
    requestID: 1,
    response: { sources: [{ id: 'stale' }] },
  });
  assert.equal(unchanged, state);

  state = reducer(state, {
    type: 'SESSION_REVIEW_SUCCESS',
    id: 's1',
    requestID: 2,
    response: {
      workspace_label: '/tmp/project',
      generated_at: '2026-07-27T02:00:00Z',
      sources: [{ id: 'worktree', diff: '+current' }],
    },
  });
  assert.equal(state.sessions.s1.review.status, 'ready');
  assert.equal(state.sessions.s1.review.ignoreWhitespace, true);
  assert.equal(state.sessions.s1.review.sources[0].id, 'worktree');
});

test('execution settings ignore stale request generations', () => {
  let state = reducer(initialState(), {
    type: 'SESSION_UPSERT', session: { id: 's1', workspace_label: '/tmp/project' },
  });
  state = reducer(state, {
    type: 'EXECUTION_SETTINGS_LOADING', id: 's1', requestID: 1, mutation: false,
  });
  state = reducer(state, {
    type: 'EXECUTION_SETTINGS_LOADING', id: 's1', requestID: 2, mutation: true,
  });
  const stale = reducer(state, {
    type: 'EXECUTION_SETTINGS_SUCCESS', id: 's1', requestID: 1,
    response: { model: 'stale' },
  });
  assert.equal(stale, state);
  const staleFailure = reducer(state, {
    type: 'EXECUTION_SETTINGS_FAILED', id: 's1', requestID: 1, error: 'stale',
  });
  assert.equal(staleFailure, state);
});

test('partial execution input retains reducer defaults', () => {
  const state = reducer(initialState(), {
    type: 'SESSION_UPSERT',
    session: { id: 's1', workspace_label: '/tmp/project', execution: { model: 'primary' } },
  });
  assert.equal(state.sessions.s1.execution.model, 'primary');
  assert.equal(state.sessions.s1.execution.status, 'idle');
  assert.deepEqual(state.sessions.s1.execution.reasoningEffortOptions, ['default']);
});

test('execution settings normalize wire fields and retain local failures', () => {
  let state = reducer(initialState(), {
    type: 'SESSION_UPSERT', session: { id: 's1', workspace_label: '/tmp/project' },
  });
  state = reducer(state, {
    type: 'EXECUTION_SETTINGS_LOADING', id: 's1', requestID: 1, mutation: false,
  });
  state = reducer(state, {
    type: 'EXECUTION_SETTINGS_SUCCESS', id: 's1', requestID: 1,
    response: {
      model: 'primary', models: [{ selector: 'primary' }],
      reasoning_effort: 'high', reasoning_effort_supported: true,
      reasoning_effort_options: ['default', 'high'],
      permission_mode: 'plan', permission_mode_options: ['default', 'plan'],
      dispatch_block: { code: 'model_binding_invalid', context_only: false },
    },
  });
  assert.equal(state.sessions.s1.execution.reasoningEffort, 'high');
  assert.equal(state.sessions.s1.execution.permissionMode, 'plan');
  state = reducer(state, {
    type: 'EXECUTION_SETTINGS_LOADING', id: 's1', requestID: 2, mutation: true,
  });
  state = reducer(state, {
    type: 'EXECUTION_SETTINGS_FAILED', id: 's1', requestID: 2, error: 'rejected',
  });
  assert.equal(state.sessions.s1.execution.error, 'rejected');
  assert.equal(state.sessions.s1.status, 'idle');
});

test('send admission blocks engine and settings ownership but not context-only remediation', () => {
  const session = {
    status: 'idle', active_turn_id: '', live: true,
    execution: { status: 'ready', dispatchBlock: {
      code: 'model_binding_invalid', context_only: false,
    } },
  };
  assert.equal(canSubmitTurn(session), false);
  assert.equal(canSubmitTurn({
    ...session, execution: { status: 'ready', dispatchBlock: { context_only: true } },
  }), true);
  assert.equal(canSubmitTurn({ ...session, execution: { status: 'loading' } }), false);
  assert.equal(canSubmitTurn({ ...session, execution: { status: 'updating' } }), false);
  assert.equal(canSubmitTurn({ ...session, status: 'running' }), false);
  assert.equal(canSubmitTurn({ ...session, active_turn_id: 'turn-1' }), false);
  for (const status of ['offline', 'restoring', 'saved', 'stopping']) {
    assert.equal(canSubmitTurn({ ...session, status }), false, status);
  }
});

test('queue admission is available only for one live active turn', () => {
  const active = {
    live: true, activation: 'live', status: 'running', active_turn_id: 'turn-1',
  };
  assert.equal(canQueuePrompt(active), true);
  assert.equal(canQueuePrompt({ ...active, status: 'waiting' }), true);
  assert.equal(canQueuePrompt({ ...active, status: 'stopping' }), true);
  assert.equal(canQueuePrompt({ ...active, active_turn_id: '' }), false);
  assert.equal(canQueuePrompt({ ...active, live: false }), false);
  for (const status of ['idle', 'offline', 'restoring', 'saved', 'error']) {
    assert.equal(canQueuePrompt({ ...active, status }), false, status);
  }
});

test('draft admission remains editable while turn submission is blocked', () => {
  const blocked = {
    live: true,
    status: 'idle',
    active_turn_id: '',
    execution: {
      status: 'ready',
      model: 'only-model',
      models: [{ selector: 'only-model' }],
      dispatchBlock: {
        code: 'model_binding_route_revision_changed',
        context_only: false,
      },
    },
  };
  assert.equal(canEditDraft(blocked), true);
  assert.equal(canSubmitTurn(blocked), false);
  assert.equal(canEditDraft({
    ...blocked,
    status: 'running',
    active_turn_id: 'turn-1',
  }), true);
  assert.equal(canEditDraft({
    ...blocked,
    execution: { ...blocked.execution, status: 'updating' },
  }), true);
  for (const status of ['offline', 'restoring', 'saved']) {
    assert.equal(canEditDraft({ ...blocked, status }), false, status);
  }
  assert.equal(canEditDraft(null), false);
});

test('same-selector rebind requires one safe available blocked model', () => {
  const blocked = {
    live: true,
    status: 'idle',
    active_turn_id: '',
    execution: {
      status: 'ready',
      model: 'only-model',
      models: [{ selector: 'only-model' }],
      dispatchBlock: {
        code: 'model_binding_route_revision_changed',
        context_only: false,
      },
    },
  };
  assert.equal(modelRebindSelector(blocked), 'only-model');
  assert.equal(modelRebindSelector({
    ...blocked,
    execution: {
      ...blocked.execution,
      model: 'raw-provider-model',
      models: [{ selector: 'canonical-provider:raw-provider-model' }],
      dispatchBlock: {
        code: 'model_binding_metadata_incompatible',
        context_only: false,
        selector: 'canonical-provider:raw-provider-model',
      },
    },
  }), '');
  assert.equal(modelRebindSelector({
    ...blocked,
    execution: {
      ...blocked.execution,
      dispatchBlock: {
        ...blocked.execution.dispatchBlock,
        selector: 'unavailable-model',
      },
    },
  }), '');
  assert.equal(modelRebindSelector({
    ...blocked,
    execution: {
      ...blocked.execution,
      dispatchBlock: { context_only: true },
    },
  }), '');
  assert.equal(modelRebindSelector({
    ...blocked,
    execution: { ...blocked.execution, models: [] },
  }), '');
  assert.equal(modelRebindSelector({
    ...blocked,
    execution: { ...blocked.execution, status: 'updating' },
  }), '');
  assert.equal(modelRebindSelector({
    ...blocked,
    status: 'running',
    active_turn_id: 'turn-1',
  }), '');
});

test('execution settings preserve draft while rebind resolves', () => {
  let state = reducer(initialState(), {
    type: 'SESSION_UPSERT',
    session: {
      id: 's1',
      live: true,
      draft: 'preserve me',
      execution: {
        model: 'only-model',
        models: [{ selector: 'only-model' }],
        dispatchBlock: { context_only: false },
      },
    },
  });
  state = reducer(state, {
    type: 'EXECUTION_SETTINGS_LOADING', id: 's1', requestID: 1, mutation: true,
  });
  assert.equal(state.sessions.s1.draft, 'preserve me');
  state = reducer(state, {
    type: 'EXECUTION_SETTINGS_FAILED', id: 's1', requestID: 1,
    error: 'rebind failed',
  });
  assert.equal(state.sessions.s1.draft, 'preserve me');
  state = reducer(state, {
    type: 'EXECUTION_SETTINGS_LOADING', id: 's1', requestID: 2, mutation: true,
  });
  state = reducer(state, {
    type: 'EXECUTION_SETTINGS_SUCCESS', id: 's1', requestID: 2,
    response: { model: 'only-model', models: [{ selector: 'only-model' }] },
  });
  assert.equal(state.sessions.s1.draft, 'preserve me');
});

test('session filtering matches title, workspace label, status, and branch', () => {
  const session = {
    title: 'Desktop implementation',
    workspace_label: '/workspace/yhc',
    status: 'saved',
    git_branch: 'feat/review',
  };
  assert.equal(sessionMatchesQuery(session, 'desktop'), true);
  assert.equal(sessionMatchesQuery(session, 'YHC'), true);
  assert.equal(sessionMatchesQuery(session, 'saved'), true);
  assert.equal(sessionMatchesQuery(session, 'feat/review'), true);
  assert.equal(sessionMatchesQuery(session, 'unrelated'), false);
});

test('renderer state strips path-bearing fields from session and review responses', () => {
  const pathKey = 'c' + 'wd';
  const rootKey = 'repository_' + 'root';
  let state = reducer(initialState(), {
    type: 'SESSION_UPSERT',
    session: {
      id: 's1',
      workspace_label: 'Project',
      [pathKey]: '/sentinel/absolute/path',
    },
  });
  state = reducer(state, {
    type: 'SESSION_REVIEW_LOADING', id: 's1', requestID: 1,
  });
  state = reducer(state, {
    type: 'SESSION_REVIEW_SUCCESS', id: 's1', requestID: 1,
    response: {
      [pathKey]: '/sentinel/absolute/path',
      sources: [{ id: 'worktree', [rootKey]: '/sentinel/absolute/path', diff: '+ok' }],
    },
  });
  const serialized = JSON.stringify(state);
  assert.equal(serialized.includes('/sentinel/absolute/path'), false);
  assert.equal(Object.hasOwn(state.sessions.s1, pathKey), false);
  assert.equal(Object.hasOwn(state.sessions.s1.review.sources[0], rootKey), false);
});
