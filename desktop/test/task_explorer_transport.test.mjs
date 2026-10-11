import test from 'node:test';
import assert from 'node:assert/strict';
import { createRequire } from 'node:module';
import { operationPath } from '../../internal/webui/assets/transport.mjs';

const { desktopOperation } = createRequire(import.meta.url)('../request.cjs');
for (const [surface, operation] of Object.entries({ desktop: desktopOperation, web: operationPath })) {
  test(`${surface} task overview is an owned-session GET without actions or paths`, () => {
    assert.deepEqual(operation('getTaskExplorer', { sessionID: 'session-1' }), [
      '/v1/sessions/session-1/tasks', 'GET',
    ]);
    for (const payload of [null, [], {}, { sessionID: '../other' }, { sessionID: 's', action: 'pause' },
      { sessionID: 's', path: '/private/data' }, { sessionID: 's', generation: '1' },
      { sessionID: 's', query: 'all' }]) {
      assert.throws(() => operation('getTaskExplorer', payload));
    }
  });
}
