import assert from 'node:assert/strict';
import { createRequire } from 'node:module';
import test from 'node:test';

const require = createRequire(import.meta.url);
const {
  activeTurnQuitPrompt,
  activeTurnSessions,
  quitInspectionFailurePrompt,
} = require('../lifecycle.cjs');

test('active turn detection uses bounded live summaries', () => {
  assert.deepEqual(activeTurnSessions({ sessions: [
    { id: 'idle', status: 'idle', active_turn_id: '' },
    { id: 'running', status: 'running', active_turn_id: 'turn-1' },
  ] }).map((session) => session.id), ['running']);
});

test('malformed live summaries fail closed', () => {
  assert.throws(() => activeTurnSessions(), TypeError);
  assert.throws(() => activeTurnSessions({}), TypeError);
  assert.throws(() => activeTurnSessions({ sessions: {} }), TypeError);
});

test('empty summaries and malformed rows have no active turns', () => {
  assert.deepEqual(activeTurnSessions({ sessions: [] }), []);
  assert.deepEqual(activeTurnSessions({ sessions: [
    { id: 'missing' },
    { id: 'number', active_turn_id: 1 },
    { id: 'empty', active_turn_id: '' },
  ] }), []);
});

test('quit prompt keeps working by default', () => {
  assert.deepEqual(activeTurnQuitPrompt(2), {
    type: 'warning',
    buttons: ['Keep working', 'Quit and stop turns'],
    defaultId: 0,
    cancelId: 0,
    noLink: true,
    message: '2 active turns are still running',
    detail: 'Quitting now stops those turns. Durable transcripts remain resumable.',
  });
});

test('quit prompt uses singular grammar for one turn', () => {
  assert.equal(activeTurnQuitPrompt(1).message, '1 active turn is still running');
});

test('inspection failure prompt keeps working by default', () => {
  assert.deepEqual(quitInspectionFailurePrompt(), {
    type: 'warning',
    buttons: ['Keep working', 'Quit and stop backend'],
    defaultId: 0,
    cancelId: 0,
    noLink: true,
    message: 'Unable to verify active turns',
  });
});
