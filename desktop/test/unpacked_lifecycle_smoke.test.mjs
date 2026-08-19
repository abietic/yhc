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
  NO_RESTART_OBSERVATION_MS,
  appendBounded,
  crashContainmentMatches,
  killVerifiedBackend,
  makeIsolatedEnvironment,
  observeStable,
  observeSpawnedChild,
  parseArguments,
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

test('smoke arguments admit one explicit crash mode and reject ambiguity', () => {
  const app = path.resolve('/opt/yhc/yhc-desktop');
  assert.deepEqual(
    parseArguments(['--app', app]),
    { appPath: app, crashBackend: false, disableSandbox: false },
  );
  assert.deepEqual(
    parseArguments(['--crash-backend', '--app', app, '--no-sandbox']),
    { appPath: app, crashBackend: true, disableSandbox: true },
  );
  for (const invalid of [
    [],
    ['--app'],
    ['--app', app, '--app', app],
    ['--app', app, '--crash-backend', '--crash-backend'],
    ['--app', app, '--no-sandbox', '--no-sandbox'],
    ['--app', app, '--unknown'],
  ]) {
    assert.throws(() => parseArguments(invalid), /usage: unpacked_lifecycle_smoke/);
  }
});

test('crash injection requires a matching current backend identity', () => {
  const expected = {
    pid: 4242,
    state: 'S',
    startTime: '987654',
    executable: '/opt/yhc/resources/bin/yhc',
  };
  const signals = [];
  killVerifiedBackend(expected, {
    readIdentity: () => ({ ...expected }),
    signalProcess: (pid, signal) => signals.push([pid, signal]),
  });
  assert.deepEqual(signals, [[expected.pid, 'SIGKILL']]);

  for (const current of [
    { ...expected, pid: 4243 },
    { ...expected, startTime: '987655' },
    { ...expected, executable: '/tmp/reused' },
    { ...expected, state: 'Z' },
  ]) {
    assert.throws(
      () => killVerifiedBackend(expected, {
        readIdentity: () => current,
        signalProcess: () => assert.fail('mismatched process must not be signalled'),
      }),
      /backend process identity changed before crash injection/,
    );
  }
});

test('crash containment stays stable beyond the Host bootstrap budget', async () => {
  const contained = {
    bootstrapReady: true,
    notificationCount: 1,
    backendUnavailable: true,
    notice: 'Backend stopped unexpectedly. Restart YHC to reconnect.',
    status: 'Offline',
    checkedControlsDisabled: true,
  };
  assert.equal(crashContainmentMatches(contained), true);
  for (const changed of [
    { ...contained, notificationCount: 2 },
    { ...contained, backendUnavailable: false },
    { ...contained, bootstrapReady: false },
    { ...contained, checkedControlsDisabled: false },
  ]) {
    assert.equal(crashContainmentMatches(changed), false);
  }
  const main = await readFile(new URL('../main.cjs', import.meta.url), 'utf8');
  const budgetMatch = main.match(/const BOOTSTRAP_TIMEOUT_MS = ([0-9_]+);/);
  assert.ok(budgetMatch, 'Host bootstrap budget must remain explicit');
  const bootstrapBudget = Number(budgetMatch[1].replaceAll('_', ''));
  assert.ok(NO_RESTART_OBSERVATION_MS > bootstrapBudget);

  let currentTime = 0;
  let samples = 0;
  await observeStable(
    () => {
      samples += 1;
      return true;
    },
    250,
    'stable fixture',
    {
      now: () => currentTime,
      wait: async (milliseconds) => { currentTime += milliseconds; },
    },
  );
  assert.equal(samples, 3);

  currentTime = 0;
  samples = 0;
  await assert.rejects(
    observeStable(
      () => {
        samples += 1;
        return samples < 3;
      },
      500,
      'changing fixture',
      {
        now: () => currentTime,
        wait: async (milliseconds) => { currentTime += milliseconds; },
      },
    ),
    /changed during observation/,
  );
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
  assert.match(
    makefile,
    /desktop-unpacked-crash-containment-smoke-linux-amd64: desktop-package-smoke-linux-amd64/,
  );
  assert.match(
    makefile,
    /unpacked_lifecycle_smoke\.cjs --app desktop\/dist\/linux-unpacked\/yhc-desktop --crash-backend\n/,
  );
  assert.match(
    makefile,
    /desktop-unpacked-crash-containment-smoke-linux-amd64-ci: desktop-package-smoke-linux-amd64/,
  );
  assert.match(
    makefile,
    /unpacked_lifecycle_smoke\.cjs --app desktop\/dist\/linux-unpacked\/yhc-desktop --crash-backend --no-sandbox/,
  );
  assert.match(workflow, /desktop-unpacked-lifecycle-smoke-linux-amd64-ci/);
  assert.match(workflow, /desktop-unpacked-crash-containment-smoke-linux-amd64-ci/);
  assert.doesNotMatch(workflow, /make desktop-unpacked-lifecycle-smoke-linux-amd64\s*(?:\n|$)/);
  assert.doesNotMatch(
    workflow,
    /make desktop-unpacked-crash-containment-smoke-linux-amd64\s*(?:\n|$)/,
  );
  assert.doesNotMatch(workflow, /make desktop-package-smoke-linux-amd64/);
  assert.match(app, /document\.documentElement\.dataset\.yhcBootstrap = 'ready'/);
  assert.match(app, /document\.documentElement\.dataset\.yhcBootstrap = 'error'/);
});
