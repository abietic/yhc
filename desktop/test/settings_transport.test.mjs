import assert from 'node:assert/strict';
import { createRequire } from 'node:module';
import test from 'node:test';

import { operationPath } from '../../internal/webui/assets/transport.mjs';

const require = createRequire(import.meta.url);
const { desktopOperation } = require('../request.cjs');
const transports = { desktop: desktopOperation, browser: operationPath };
const sessionID = 'session-1785514608818950000';

test('Desktop and Web settings reads address the same user or owned workspace scope', () => {
  const cases = [
    [{}, '/v1/settings?scope=user'],
    [{ scope: 'user' }, '/v1/settings?scope=user'],
    [{ scope: 'user', sessionID }, `/v1/settings?scope=user&session_id=${sessionID}`],
    [{ scope: 'project', sessionID }, `/v1/settings?scope=project&session_id=${sessionID}`],
  ];
  for (const [name, operation] of Object.entries(transports)) {
    for (const [input, path] of cases) {
      assert.deepEqual(operation('getSettings', input), [path, 'GET', undefined], name);
    }
  }
});

test('settings mutations preserve the revision and typed changes on both transports', () => {
  const changes = {
    'ui.theme': 'polar-night',
    'ui.compact': false,
    'budget.max_tokens': 1024,
    'model': null,
  };
  for (const [name, operation] of Object.entries(transports)) {
    for (const scope of ['user', 'project']) {
      const input = Object.freeze({
        scope,
        sessionID,
        revision: 'revision-before-save',
        changes: Object.freeze(changes),
      });
      assert.deepEqual(operation('updateSettings', input), [
        `/v1/settings?scope=${scope}&session_id=${sessionID}`,
        'PATCH',
        { revision: 'revision-before-save', changes },
      ], name);
    }
  }
});

test('settings transport never promotes extra input into a path or request body', () => {
  for (const [name, operation] of Object.entries(transports)) {
    const input = {
      scope: 'user',
      revision: 'revision',
      changes: { 'ui.theme': 'daybreak' },
      method: 'DELETE',
      path: '/v1/sessions/other',
      apiKey: 'fixture-must-not-be-forwarded',
    };
    assert.deepEqual(operation('updateSettings', input), [
      '/v1/settings?scope=user',
      'PATCH',
      { revision: 'revision', changes: { 'ui.theme': 'daybreak' } },
    ], name);
    assert.deepEqual(operation('getSettings', input), [
      '/v1/settings?scope=user', 'GET', undefined,
    ], name);
  }
});

test('both settings operations reject unsupported scopes on both surfaces', () => {
  for (const [name, operation] of Object.entries(transports)) {
    for (const kind of ['getSettings', 'updateSettings']) {
      for (const scope of ['session', 'workspace', '../user', 'USER', 'user&scope=project', {}, [], 1, true]) {
        assert.throws(() => operation(kind, { scope, sessionID }),
          /invalid settings scope/, `${name} ${kind}: ${String(scope)}`);
      }
    }
  }
});

test('both settings operations reject malformed optional session identities', () => {
  const invalidIDs = [
    '.', '..', '-leading', '../escape', 'bad/id', 'bad\\id',
    'other?scope=project', 'other&scope=project', 'space inside',
    'x\u0000y', 'a'.repeat(257), 1, true, {}, [],
  ];
  for (const [name, operation] of Object.entries(transports)) {
    for (const kind of ['getSettings', 'updateSettings']) {
      for (const scope of ['user', 'project']) {
        for (const identity of invalidIDs) {
          assert.throws(() => operation(kind, { scope, sessionID: identity }),
            /valid session id required/, `${name} ${kind}: ${String(identity)}`);
        }
      }
    }
  }
});
