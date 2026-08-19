import assert from 'node:assert/strict';
import { EventEmitter } from 'node:events';
import { createRequire } from 'node:module';
import fs from 'node:fs';
import { readFile } from 'node:fs/promises';
import os from 'node:os';
import path from 'node:path';
import test from 'node:test';

const require = createRequire(import.meta.url);
const {
  appendBounded,
  makeIsolatedEnvironment,
  observeSpawnedChild,
  parseDevToolsEndpoint,
  parseProcStat,
  safeProcessGroupID,
  selectPageTarget,
} = require('../scripts/unpacked_lifecycle_smoke.cjs');

test('DevTools discovery accepts one ephemeral loopback browser endpoint', () => {
  const endpoint = 'ws://127.0.0.1:43127/devtools/browser/01234567-89ab-cdef-0123-456789abcdef';
  assert.equal(
    parseDevToolsEndpoint(`noise\nDevTools listening on ${endpoint}\n`),
    endpoint,
  );
  assert.equal(parseDevToolsEndpoint('startup still pending'), null);
  for (const invalid of [
    'ws://0.0.0.0:43127/devtools/browser/id',
    'ws://example.test:43127/devtools/browser/id',
    'wss://127.0.0.1:43127/devtools/browser/id',
    'ws://127.0.0.1:43127/devtools/page/id',
  ]) {
    assert.throws(
      () => parseDevToolsEndpoint(`DevTools listening on ${invalid}`),
      /invalid loopback DevTools endpoint/,
    );
  }
});

test('target selection requires one exact packaged renderer URL', () => {
  const expected = 'file:///opt/yhc/resources/webui/index.html';
  const target = { id: 'renderer', type: 'page', url: expected };
  assert.deepEqual(selectPageTarget([
    { id: 'other', type: 'page', url: 'file:///tmp/other.html' },
    { id: 'worker', type: 'service_worker', url: expected },
    target,
  ], expected), target);
  assert.equal(selectPageTarget([], expected), null);
  assert.throws(
    () => selectPageTarget([target, { ...target, id: 'duplicate' }], expected),
    /ambiguous packaged renderer target/,
  );
});

test('Linux process identity parsing retains start time across complex names', () => {
  const fields = ['S', ...Array(18).fill('0'), '987654', '0', '0'];
  assert.deepEqual(parseProcStat(`123 (yhc worker) helper) ${fields.join(' ')}`), {
    pid: 123,
    state: 'S',
    startTime: '987654',
  });
  assert.throws(() => parseProcStat('malformed'), /invalid Linux process stat/);
});

test('diagnostics and process-group cleanup remain bounded', () => {
  assert.equal(appendBounded('', Buffer.from('abc'), 5), 'abc');
  assert.equal(appendBounded('abc', Buffer.from('def'), 5), 'bcdef');
  assert.equal(safeProcessGroupID(42), -42);
  for (const pid of [-1, 0, 1, 1.5, Number.NaN]) {
    assert.throws(() => safeProcessGroupID(pid), /unsafe process group/);
  }
});

test('spawn observation handles an asynchronous error before PID validation', async () => {
  const child = new EventEmitter();
  child.pid = undefined;
  assert.throws(() => observeSpawnedChild(child), /unsafe process group/);
  assert.doesNotThrow(() => child.emit('error', new Error('spawn failed')));
  await new Promise((resolve) => setImmediate(resolve));
});

test('smoke environment isolates profiles and drops inherited provider secrets', () => {
  const root = fs.mkdtempSync(path.join(os.tmpdir(), 'yhc-smoke-env-test-'));
  try {
    const environment = makeIsolatedEnvironment(root, {
      PATH: '/usr/bin',
      LANG: 'C.UTF-8',
      HOME: '/private/home',
      YHC_BIN: '/private/yhc',
      PROV_API_KEY: 'private-provider-key',
      OPENAI_API_KEY: 'private-openai-key',
      LD_LIBRARY_PATH: '/private/libraries',
    });
    assert.equal(environment.PATH, '/usr/bin');
    assert.equal(environment.LANG, 'C.UTF-8');
    assert.equal(environment.HOME, path.join(root, 'home'));
    for (const forbidden of [
      'YHC_BIN', 'PROV_API_KEY', 'OPENAI_API_KEY', 'LD_LIBRARY_PATH',
    ]) {
      assert.equal(Object.hasOwn(environment, forbidden), false, forbidden);
    }
    assert.equal(fs.statSync(environment.XDG_RUNTIME_DIR).mode & 0o777, 0o700);
  } finally {
    fs.rmSync(root, { recursive: true, force: true });
  }
});

test('repository wiring runs the real unpacked lifecycle after artifact verification', async () => {
  const [makefile, workflow, app] = await Promise.all([
    readFile(new URL('../../Makefile', import.meta.url), 'utf8'),
    readFile(new URL('../../.github/workflows/ci.yml', import.meta.url), 'utf8'),
    readFile(new URL('../../internal/webui/assets/app.mjs', import.meta.url), 'utf8'),
  ]);
  assert.match(
    makefile,
    /desktop-unpacked-lifecycle-smoke-linux-amd64: desktop-package-smoke-linux-amd64/,
  );
  assert.match(
    makefile,
    /node desktop\/scripts\/unpacked_lifecycle_smoke\.cjs --app desktop\/dist\/linux-unpacked\/yhc-desktop/,
  );
  assert.match(
    makefile,
    /desktop-unpacked-lifecycle-smoke-linux-amd64-ci: desktop-package-smoke-linux-amd64/,
  );
  assert.match(
    makefile,
    /unpacked_lifecycle_smoke\.cjs --app desktop\/dist\/linux-unpacked\/yhc-desktop --no-sandbox/,
  );
  assert.match(workflow, /make desktop-unpacked-lifecycle-smoke-linux-amd64-ci/);
  assert.doesNotMatch(workflow, /make desktop-unpacked-lifecycle-smoke-linux-amd64\s*(?:\n|$)/);
  assert.doesNotMatch(workflow, /make desktop-package-smoke-linux-amd64/);
  assert.match(app, /document\.documentElement\.dataset\.yhcBootstrap = 'ready'/);
  assert.match(app, /document\.documentElement\.dataset\.yhcBootstrap = 'error'/);
});
