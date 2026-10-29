import assert from 'node:assert/strict';
import { createRequire } from 'node:module';
import test from 'node:test';
import { operationPath } from '../../internal/webui/assets/transport.mjs';

const require = createRequire(import.meta.url);
const { desktopOperation } = require('../request.cjs');
for (const [surface, operation] of Object.entries({ desktop: desktopOperation, web: operationPath })) {
  test(`${surface} command completion uses a bounded POST and rune cursor`, () => {
    assert.deepEqual(operation('completeCommand', { sessionID: 'session-1', input: '/model 中文👩‍💻', cursor: 12 }), [
      '/v1/sessions/session-1/command-completions', 'POST', { input: '/model 中文👩‍💻', cursor: 12 },
    ]);
    for (const patch of [{ input: '\ud800' }, { input: '/mod\n' }, { input: '/\u007f' }, { input: 'a'.repeat(2049) }, { cursor: -1 }, { cursor: 99 }, { cursor: 1.5 }, { path: '/private' }, { sessionID: '../escape' }]) {
      assert.throws(() => operation('completeCommand', { sessionID: 'session-1', input: '/mod', cursor: 4, ...patch }));
    }
  });
}
