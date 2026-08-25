import assert from 'node:assert/strict';
import { createRequire } from 'node:module';
import test from 'node:test';

const require = createRequire(import.meta.url);
const { desktopOperation } = require('../request.cjs');
const sessionID = 'f47ac10b-58cc-4372-a567-0e02b2c3d479';
const legacySessionID = 'session-1785514608818950000';

test('desktop request map preserves every trusted operation', () => {
  const permission = { decision: 'allow_once' };
  const cases = [
    ['health', {}, ['/v1/health', 'GET']],
    ['listSessions', {}, ['/v1/sessions', 'GET']],
    ['listDurableSessions', {
      cursor: 'next page', limit: 100, search: 'desktop app',
    }, ['/v1/durable-sessions?cursor=next+page&limit=100&search=desktop+app', 'GET']],
    ['createSession', { workspaceHandle: 'opaque-workspace-handle' }, [
      '/v1/sessions', 'POST', { workspace_handle: 'opaque-workspace-handle' },
    ]],
    ['createWorkspace', { cwd: '/sentinel/absolute/path' }, [
      '/v1/workspaces', 'POST', { cwd: '/sentinel/absolute/path' },
    ]],
    ['getSession', { sessionID }, [`/v1/sessions/${sessionID}`, 'GET']],
    ['closeSession', { sessionID }, [`/v1/sessions/${sessionID}`, 'DELETE']],
    ['snapshot', { sessionID }, [`/v1/sessions/${sessionID}/snapshot`, 'GET']],
    ['transcriptPage', { sessionID, cursor: 'before 100', limit: 50 }, [
      `/v1/sessions/${sessionID}/transcript?cursor=before+100&limit=50`, 'GET',
    ]],
    ['durableTranscriptPage', { sessionID, cursor: 'before 100', limit: 50 }, [
      `/v1/durable-sessions/${sessionID}/transcript?cursor=before+100&limit=50`, 'GET',
    ]],
    ['reviewDiff', { sessionID, ignoreWhitespace: true }, [
      `/v1/sessions/${sessionID}/review-diff?ignore_whitespace=true`, 'GET',
    ]],
    ['startTurn', { sessionID, prompt: 'hello', clientTurnID: 'client-1' }, [
      `/v1/sessions/${sessionID}/turns`, 'POST', {
        prompt: 'hello', client_turn_id: 'client-1',
      },
    ]],
    ['attachTurn', {
      sessionID, prompt: 'hello', clientTurnID: 'a47ac10b-58cc-4372-a567-0e02b2c3d479',
    }, [
      `/v1/durable-sessions/${sessionID}/attach-turn`, 'POST', {
        prompt: 'hello', client_turn_id: 'a47ac10b-58cc-4372-a567-0e02b2c3d479',
      },
    ]],
    ['importDurableSession', { sessionID, confirmLegacyStopped: true }, [
      `/v1/durable-sessions/${sessionID}/import`, 'POST', {
        confirm_legacy_stopped: true,
      },
    ]],
    ['cancelTurn', { sessionID, turnID: 'turn-1', mode: 'immediate', reason: 'cancel' }, [
      `/v1/sessions/${sessionID}/cancel`, 'POST', {
        turn_id: 'turn-1', mode: 'immediate', reason: 'cancel',
      },
    ]],
    ['resolveInteraction', { sessionID, requestID: 'request/1', result: permission }, [
      `/v1/sessions/${sessionID}/interactions/request%2F1/resolve`, 'POST', permission,
    ]],
    ['getInteractionPlan', { sessionID, requestID: 'request/1' }, [
      `/v1/sessions/${sessionID}/interactions/request%2F1/plan`, 'GET',
    ]],
  ];
  for (const [operation, payload, expected] of cases) {
    assert.deepEqual(desktopOperation(operation, payload), expected, operation);
  }
});

test('desktop execution settings map only the three supported fields', () => {
  assert.deepEqual(desktopOperation('getExecutionSettings', { sessionID }), [
    `/v1/sessions/${sessionID}/execution-settings`, 'GET',
  ]);
  for (const field of ['model', 'reasoning_effort', 'permission_mode']) {
    assert.deepEqual(desktopOperation('updateExecutionSetting', {
      sessionID, field, value: 'selected',
    }), [
      `/v1/sessions/${sessionID}/execution-settings`, 'PATCH', { [field]: 'selected' },
    ]);
  }
  assert.throws(() => desktopOperation('updateExecutionSetting', {
    sessionID, field: 'bypass', value: 'true',
  }), /supported execution setting required/);
});

test('desktop request map accepts a bounded legacy durable session id', () => {
  assert.deepEqual(desktopOperation('getExecutionSettings', {
    sessionID: legacySessionID,
  }), [
    `/v1/sessions/${legacySessionID}/execution-settings`,
    'GET',
  ]);
  assert.equal(desktopOperation('getSession', { sessionID: legacySessionID })[0],
    `/v1/sessions/${legacySessionID}`);
});

test('desktop operation map rejects invalid session ids and arbitrary operations', () => {
  for (const invalid of [
    '', '.', '..', '-leading', '../escape', 'bad/id', 'bad\\id', `a${'b'.repeat(256)}`,
  ]) {
    assert.throws(() => desktopOperation('getExecutionSettings', {
      sessionID: invalid,
    }), /valid session id required/);
  }
  assert.throws(() => desktopOperation('createSession', {
    workspaceHandle: '',
  }), /valid workspace handle required/);
  assert.throws(() => desktopOperation('createSession', {
    workspaceHandle: 'valid', title: 'not permitted',
  }), /unsupported new session field/);
  assert.throws(() => desktopOperation('resolveInteraction', {
    sessionID, requestID: '', result: {},
  }), /valid interaction request id required/);
  assert.throws(() => desktopOperation('nope'), /unsupported desktop API operation/);
  assert.throws(() => desktopOperation('updateExecutionSetting', {
    sessionID, field: 'model', value: 3,
  }), /execution setting value must be a string/);
  assert.throws(() => desktopOperation('attachTurn', {
    sessionID, prompt: 'hello', clientTurnID: 'not-a-uuid',
  }), /client turn id must be a UUID/);
  assert.throws(() => desktopOperation('attachTurn', {
    sessionID, prompt: '', clientTurnID: 'a47ac10b-58cc-4372-a567-0e02b2c3d479',
  }), /prompt is required/);
  assert.throws(() => desktopOperation('attachTurn', {
    sessionID,
    prompt: `x${'y'.repeat(262144)}`,
    clientTurnID: 'a47ac10b-58cc-4372-a567-0e02b2c3d479',
  }), /prompt exceeds 262144 bytes/);
  assert.throws(() => desktopOperation('attachTurn', {
    sessionID, prompt: '\uD800', clientTurnID: 'a47ac10b-58cc-4372-a567-0e02b2c3d479',
  }), /prompt must be valid UTF-8/);
  assert.throws(() => desktopOperation('attachTurn', {
    sessionID, prompt: 'hello', clientTurnID: 'a47ac10b-58cc-4372-a567-0e02b2c3d479', title: 'nope',
  }), /unsupported attach turn field/);
  assert.throws(() => desktopOperation('importDurableSession', {
    sessionID, confirmLegacyStopped: false,
  }), /stopped producer attestation required/);
  assert.throws(() => desktopOperation('importDurableSession', {
    sessionID, confirmLegacyStopped: true, cwd: '/not-accepted',
  }), /unsupported durable import field/);
});

test('desktop attach validates and sends the same normalized prompt bytes', () => {
  const clientTurnID = 'a47ac10b-58cc-4372-a567-0e02b2c3d479';
  assert.deepEqual(desktopOperation('attachTurn', {
    sessionID,
    prompt: `${' '.repeat(262144)}x  `,
    clientTurnID,
  }), [
    `/v1/durable-sessions/${sessionID}/attach-turn`,
    'POST',
    { prompt: 'x', client_turn_id: clientTurnID },
  ]);
});
