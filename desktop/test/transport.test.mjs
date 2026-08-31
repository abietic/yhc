import assert from 'node:assert/strict';
import test from 'node:test';

import {
  operationPath,
  parseSSEFrames,
} from '../../internal/webui/assets/transport.mjs';

test('transcript API operation encodes a session cursor and limit', () => {
  const [pathname, method] = operationPath('transcriptPage', {
    sessionID: 'f47ac10b-58cc-4372-a567-0e02b2c3d479',
    cursor: 'before-100',
    limit: 50,
  });
  assert.equal(method, 'GET');
  assert.equal(
    pathname,
    '/v1/sessions/f47ac10b-58cc-4372-a567-0e02b2c3d479/' +
      'transcript?cursor=before-100&limit=50',
  );
});

test('durable session discovery encodes bounded paging and search', () => {
  const [pathname, method] = operationPath('listDurableSessions', {
    cursor: 'next page',
    limit: 100,
    search: 'desktop app',
  });
  assert.equal(method, 'GET');
  assert.equal(
    pathname,
    '/v1/durable-sessions?cursor=next+page&limit=100&search=desktop+app',
  );
});

test('durable transcript and attach turn use the exact durable session contract', () => {
  const sessionID = 'f47ac10b-58cc-4372-a567-0e02b2c3d479';
  const clientTurnID = 'a47ac10b-58cc-4372-a567-0e02b2c3d479';
  assert.deepEqual(operationPath('durableTranscriptPage', {
    sessionID, cursor: 'before 100', limit: 50,
  }), [
    `/v1/durable-sessions/${sessionID}/transcript?cursor=before+100&limit=50`,
    'GET',
  ]);
  assert.deepEqual(operationPath('attachTurn', {
    sessionID, prompt: ' hello ', clientTurnID,
  }), [
    `/v1/durable-sessions/${sessionID}/attach-turn`,
    'POST',
    { prompt: 'hello', client_turn_id: clientTurnID },
  ]);
  assert.deepEqual(operationPath('importDurableSession', {
    sessionID, confirmLegacyStopped: true,
  }), [
    `/v1/durable-sessions/${sessionID}/import`,
    'POST',
    { confirm_legacy_stopped: true },
  ]);
});

test('durable attach turn rejects invalid ids, prompts, and extra fields', () => {
  const clientTurnID = 'a47ac10b-58cc-4372-a567-0e02b2c3d479';
  assert.throws(() => operationPath('durableTranscriptPage', {
    sessionID: '../escape',
  }), /valid session id required/);
  assert.throws(() => operationPath('attachTurn', {
    sessionID: 'f47ac10b-58cc-4372-a567-0e02b2c3d479',
    prompt: 'hello',
    clientTurnID: 'not-a-uuid',
  }), /client turn id must be a UUID/);
  assert.throws(() => operationPath('attachTurn', {
    sessionID: 'f47ac10b-58cc-4372-a567-0e02b2c3d479',
    prompt: '   ',
    clientTurnID,
  }), /prompt is required/);
  assert.throws(() => operationPath('attachTurn', {
    sessionID: 'f47ac10b-58cc-4372-a567-0e02b2c3d479',
    prompt: `x${'y'.repeat(262144)}`,
    clientTurnID,
  }), /prompt exceeds 262144 bytes/);
  assert.throws(() => operationPath('attachTurn', {
    sessionID: 'f47ac10b-58cc-4372-a567-0e02b2c3d479',
    prompt: '\uD800',
    clientTurnID,
  }), /prompt must be valid UTF-8/);
  assert.throws(() => operationPath('attachTurn', {
    sessionID: 'f47ac10b-58cc-4372-a567-0e02b2c3d479',
    prompt: 'hello',
    clientTurnID,
    workspace_handle: 'not-accepted-here',
  }), /unsupported attach turn field/);
  assert.throws(() => operationPath('importDurableSession', {
    sessionID: 'f47ac10b-58cc-4372-a567-0e02b2c3d479',
    confirmLegacyStopped: false,
  }), /stopped producer attestation required/);
  assert.throws(() => operationPath('importDurableSession', {
    sessionID: 'f47ac10b-58cc-4372-a567-0e02b2c3d479',
    confirmLegacyStopped: true,
    source: '/not-accepted',
  }), /unsupported durable import field/);
});

test('Web transport cannot create a workspace session or accept a path payload', () => {
  assert.throws(() => operationPath('createSession', {
    workspaceHandle: 'opaque-workspace-handle',
  }), /Desktop App/);
  assert.throws(() => operationPath('createWorkspace', {
    cwd: '/sentinel/absolute/path',
  }), /unsupported client API operation/);
});

test('browser attach validates and sends the same normalized prompt bytes', () => {
  const sessionID = 'session-1';
  const clientTurnID = 'a47ac10b-58cc-4372-a567-0e02b2c3d479';
  assert.deepEqual(operationPath('attachTurn', {
    sessionID,
    prompt: `${' '.repeat(262144)}x  `,
    clientTurnID,
  }), [
    `/v1/durable-sessions/${sessionID}/attach-turn`,
    'POST',
    { prompt: 'x', client_turn_id: clientTurnID },
  ]);
});

test('review diff operation is scoped to the owned session id', () => {
  const [pathname, method] = operationPath('reviewDiff', {
    sessionID: 'f47ac10b-58cc-4372-a567-0e02b2c3d479',
    ignoreWhitespace: true,
  });
  assert.equal(method, 'GET');
  assert.equal(
    pathname,
    '/v1/sessions/f47ac10b-58cc-4372-a567-0e02b2c3d479/' +
      'review-diff?ignore_whitespace=true',
  );
});

test('execution settings use fixed session-scoped operations', () => {
  const sessionID = 'f47ac10b-58cc-4372-a567-0e02b2c3d479';
  assert.deepEqual(operationPath('getExecutionSettings', { sessionID }), [
    `/v1/sessions/${sessionID}/execution-settings`,
    'GET',
  ]);
  assert.deepEqual(operationPath('updateExecutionSetting', {
    sessionID,
    field: 'model',
    value: 'primary',
  }), [
    `/v1/sessions/${sessionID}/execution-settings`,
    'PATCH',
    { model: 'primary' },
  ]);
  assert.throws(() => operationPath('updateExecutionSetting', {
    sessionID,
    field: 'bypass',
    value: 'true',
  }), /supported execution setting required/);
});

test('runtime queue uses retry-stable typed session operations', () => {
  const sessionID = 'f47ac10b-58cc-4372-a567-0e02b2c3d479';
  const queueID = 'a47ac10b-58cc-4372-a567-0e02b2c3d479';
  assert.deepEqual(operationPath('listQueuedPrompts', { sessionID }), [
    `/v1/sessions/${sessionID}/queued-prompts`, 'GET',
  ]);
  assert.deepEqual(operationPath('queuePrompt', {
    sessionID, prompt: ' after this ', clientQueueID: queueID,
  }), [
    `/v1/sessions/${sessionID}/queued-prompts`, 'POST',
    { prompt: 'after this', client_queue_id: queueID },
  ]);
  assert.deepEqual(operationPath('cancelQueuedPrompt', { sessionID, queueID }), [
    `/v1/sessions/${sessionID}/queued-prompts/${queueID}`, 'DELETE',
  ]);
  assert.throws(() => operationPath('queuePrompt', {
    sessionID, prompt: 'queued', clientQueueID: 'not-a-uuid',
  }), /client queue id must be a UUID/);
  assert.throws(() => operationPath('cancelQueuedPrompt', {
    sessionID, queueID: '../escape',
  }), /queue id must be a UUID/);
});

test('browser transport accepts server-issued legacy durable session ids', () => {
  const sessionID = 'session-1785514608818950000';
  assert.deepEqual(operationPath('snapshot', { sessionID }), [
    `/v1/sessions/${sessionID}/snapshot`,
    'GET',
  ]);
  assert.throws(() => operationPath('snapshot', {
    sessionID: '../escape',
  }), /valid session id required/);
});

test('SSE frames preserve the shared callback event shape', () => {
  const received = [];
  const remaining = parseSSEFrames(
    'data: {"id":9}\n\npartial',
    (payload) => received.push(payload),
  );
  assert.deepEqual(received, [{ kind: 'event', event: { id: 9 } }]);
  assert.equal(remaining, 'partial');
});

test('interaction operations use only typed v2 endpoints', () => {
  const sessionID = 'f47ac10b-58cc-4372-a567-0e02b2c3d479';
  assert.deepEqual(operationPath('resolveInteraction', { sessionID, requestID: 'request/1', result: { kind: 'permission' } }),
    [`/v1/sessions/${sessionID}/interactions/request%2F1/resolve`, 'POST', { kind: 'permission' }]);
  assert.deepEqual(operationPath('getInteractionPlan', { sessionID, requestID: 'request/1' }),
    [`/v1/sessions/${sessionID}/interactions/request%2F1/plan`, 'GET']);
  assert.throws(() => operationPath('resolvePermission', { sessionID, requestID: 'r' }), /unsupported client API operation/);
  assert.throws(() => operationPath('resolveInteraction', { sessionID, requestID: '' }), /valid interaction request id required/);
});
