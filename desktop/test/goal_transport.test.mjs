import assert from 'node:assert/strict';
import { createRequire } from 'node:module';
import test from 'node:test';
import { operationPath } from '../../internal/webui/assets/transport.mjs';
const require = createRequire(import.meta.url);
const { desktopOperation } = require('../request.cjs');
const sessionID = 'f47ac10b-58cc-4372-a567-0e02b2c3d479';
const payload = { sessionID, operation: 'edit', expected_goal_id: 'goal-1', expected_revision: '9007199254740993', objective: '目标' };

test('goal transports preserve decimal strings and allowlist request fields', () => {
  for (const map of [operationPath, desktopOperation]) {
    assert.deepEqual(map('getGoal', { sessionID }), [`/v1/sessions/${sessionID}/goal`, 'GET']);
    assert.deepEqual(map('controlGoal', payload), [`/v1/sessions/${sessionID}/goal/control`, 'POST', {
      operation: 'edit', expected_goal_id: 'goal-1', expected_revision: '9007199254740993', objective: '目标',
    }]);
    assert.throws(() => map('controlGoal', { ...payload, secret: 'no' }), /goal payload/);
    assert.throws(() => map('controlGoal', { ...payload, expected_revision: 3 }), /goal payload/);
  }
});
