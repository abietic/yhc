import assert from 'node:assert/strict';
import { EventEmitter } from 'node:events';
import { createRequire } from 'node:module';
import fs from 'node:fs';
import { readFile } from 'node:fs/promises';
import os from 'node:os';
import path from 'node:path';
import { PassThrough } from 'node:stream';
import test from 'node:test';

const require = createRequire(import.meta.url);
const {
  ACTIVE_TURN_PARTIAL,
  ACTIVE_TURN_PROMPT,
  ACTIVE_TURN_SEED_PROMPT,
  NO_RESTART_OBSERVATION_MS,
  activeTurnCrashContainmentMatches,
  activeTurnFixtureEnvironment,
  appendBounded,
  closePackagedDesktop,
  combineSmokeFailure,
  crashContainmentMatches,
  decodeSeedSession,
  killVerifiedBackend,
  makeIsolatedEnvironment,
  observeStable,
  observeSpawnedChild, originalPageTargetClosed,
  parseArguments,
  parseDarwinProcessIdentity,
  parseDarwinScreenLockState,
  parseDevToolsEndpoint,
  parseProcStat,
  readDarwinProcessIdentity,
  readDarwinScreenLockState,
  resolveUnpackedLayout,
  safeProcessGroupID,
  seedActiveTurnSession,
  sameProcessIdentity,
  selectPageTarget, selectReplacementPageTarget, targetID,
  shouldForceCrashCleanup,
  startActiveTurnProvider,
  terminateProcessGroup,
  validatePlatformOptions,
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

test('browser close response timeout defers to bounded process-exit evidence', async () => {
  const calls = [];
  const connection = {
    async send(method) {
      calls.push(method);
      throw new Error('CDP Browser.close timed out');
    },
    close() {
      calls.push('socket.close');
    },
  };
  const exited = Promise.resolve({ code: 0, signal: null });
  const result = await closePackagedDesktop(
    connection,
    'renderer-session',
    'browser',
    exited,
    {
      async waitForExit(pending) {
        calls.push('waitForExit');
        return pending;
      },
    },
  );
  assert.deepEqual(result, { code: 0, signal: null });
  assert.deepEqual(calls, ['Browser.close', 'socket.close', 'waitForExit']);

  await assert.rejects(
    closePackagedDesktop(connection, 'renderer-session', 'browser', new Promise(() => {}), {
      waitForExit: async () => {
        throw new Error('Desktop exit timed out');
      },
    }),
    /Desktop exit timed out/,
  );

  await assert.rejects(
    closePackagedDesktop({
      send: async () => {
        throw new Error('CDP Browser.close failed: denied');
      },
      close: () => assert.fail('unexpected CDP failure must not close through success path'),
    }, 'renderer-session', 'browser', exited),
    /CDP Browser\.close failed: denied/,
  );
});

test('only a crash scenario may replace a timed-out normal exit with owned cleanup', () => {
  const timeout = new Error('Desktop exit timed out');
  assert.equal(shouldForceCrashCleanup(timeout, true), true);
  assert.equal(shouldForceCrashCleanup(timeout, false), false);
  assert.equal(shouldForceCrashCleanup(new Error('CDP Browser.close failed: denied'), true), false);
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

test('Darwin process identity parser accepts one normalized ps row with a spaced executable', () => {
  const parsed = parseDarwinProcessIdentity(
    '4242 4000 S Wed Aug 20 12:34:56 2026 /Applications/YHC Preview.app/Contents/Resources/bin/yhc',
  );
  assert.deepEqual(parsed, {
    pid: 4242,
    pgid: 4000,
    state: 'S',
    startTime: 'Wed Aug 20 12:34:56 2026',
    executable: '/Applications/YHC Preview.app/Contents/Resources/bin/yhc',
  });
  for (const invalid of [
    '',
    '4242 4242 S Wed Aug 20 12:34:56 2026 relative/yhc',
    '4242 0 S Wed Aug 20 12:34:56 2026 /Applications/YHC.app/Contents/Resources/bin/yhc',
    '4242 4242 S Wed Aug 20 12:34:56 2026 /Applications/YHC.app/Contents/Resources/bin/yhc\nextra',
    '4242 4242 S Wed Aug 20 12:34:56 2026 /Applications/YHC.app/Contents/Resources/\u0000yhc',
  ]) {
    assert.throws(() => parseDarwinProcessIdentity(invalid), /invalid Darwin process identity/);
  }
});

test('Darwin process identity reader runs one locale-pinned ps query and normalizes its executable', () => {
  const calls = [];
  const actual = readDarwinProcessIdentity(4242, {
    runPS: (command, args, options) => {
      calls.push({ command, args, options });
      return {
        status: 0,
        stdout: '4242 4000 S Wed Aug 20 12:34:56 2026 /private/var/YHC.app/Contents/Resources/bin/yhc\n',
        stderr: '',
      };
    },
    realpath: (candidate) => `/resolved${candidate}`,
  });
  assert.deepEqual(actual, {
    pid: 4242,
    pgid: 4000,
    state: 'S',
    startTime: 'Wed Aug 20 12:34:56 2026',
    executable: '/resolved/private/var/YHC.app/Contents/Resources/bin/yhc',
  });
  assert.deepEqual(calls, [{
    command: '/bin/ps',
    args: [
      '-ww', '-p', '4242',
      '-o', 'pid=', '-o', 'pgid=', '-o', 'state=', '-o', 'lstart=', '-o', 'comm=',
    ],
    options: {
      encoding: 'utf8',
      env: { PATH: '/usr/bin:/bin', LANG: 'C', LC_ALL: 'C', TZ: 'UTC' },
      maxBuffer: 4096,
      timeout: 1000,
    },
  }]);
  assert.equal(readDarwinProcessIdentity(4242, {
    runPS: () => ({ status: 1, stdout: '', stderr: '' }),
    realpath: () => assert.fail('missing process must not resolve a path'),
  }), null);
  assert.throws(() => readDarwinProcessIdentity(4242, {
    runPS: () => ({ status: 1, stdout: 'unexpected', stderr: '' }),
    realpath: () => '',
  }), /Darwin process inspection failed/);
  assert.throws(() => readDarwinProcessIdentity(4242, {
    runPS: () => ({ status: 2, stdout: '', stderr: 'ps failed' }),
    realpath: () => '',
  }), /Darwin process inspection failed/);
});

test('Darwin lock diagnostics are bounded, locale-pinned, and fail closed', () => {
  assert.equal(parseDarwinScreenLockState('"IOConsoleLocked" = Yes'), true);
  assert.equal(parseDarwinScreenLockState('"CGSSessionScreenIsLocked"=Yes'), true);
  assert.equal(parseDarwinScreenLockState('"IOConsoleLocked" = No'), false);
  assert.equal(parseDarwinScreenLockState([
    '"IOConsoleLocked" = Yes',
    '"CGSSessionScreenIsLocked" = No',
  ].join('\n')), null);
  assert.equal(parseDarwinScreenLockState('unrelated'), null);

  const calls = [];
  assert.equal(readDarwinScreenLockState({
    runIOReg: (command, args, options) => {
      calls.push({ command, args, options });
      return { status: 0, stdout: '"IOConsoleLocked" = Yes', stderr: '' };
    },
  }), true);
  assert.deepEqual(calls, [{
    command: '/usr/sbin/ioreg',
    args: ['-n', 'Root', '-d1'],
    options: {
      encoding: 'utf8',
      env: { PATH: '/usr/bin:/bin:/usr/sbin', LANG: 'C', LC_ALL: 'C' },
      maxBuffer: 262144,
      timeout: 1000,
    },
  }]);
  assert.equal(readDarwinScreenLockState({
    runIOReg: () => ({ status: 1, stdout: '', stderr: 'unavailable' }),
  }), null);
  assert.equal(readDarwinScreenLockState({
    runIOReg: () => ({ error: new Error('timeout') }),
  }), null);
  assert.equal(readDarwinScreenLockState({
    runIOReg: () => {
      throw new Error('unavailable');
    },
  }), null);
});

test('unpacked layouts select direct macOS launch and preserve Linux layout', () => {
  const macApp = '/tmp/yhc/mac-arm64/YHC.app/Contents/MacOS/YHC';
  const mac = resolveUnpackedLayout(macApp, { platform: 'darwin', arch: 'arm64' });
  assert.equal(mac.appPath, macApp);
  assert.equal(mac.resourcesPath, '/tmp/yhc/mac-arm64/YHC.app/Contents/Resources');
  assert.equal(mac.backendPath, '/tmp/yhc/mac-arm64/YHC.app/Contents/Resources/bin/yhc');
  assert.equal(mac.rendererURL, 'file:///tmp/yhc/mac-arm64/YHC.app/Contents/Resources/webui/index.html');
  assert.equal(mac.launcher, 'direct');
  assert.equal(mac.closeStrategy, 'browser');

  const linuxApp = '/tmp/yhc/linux-unpacked/yhc-desktop';
  const linux = resolveUnpackedLayout(linuxApp, { platform: 'linux', arch: 'x64' });
  assert.equal(linux.resourcesPath, '/tmp/yhc/linux-unpacked/resources');
  assert.equal(linux.backendPath, '/tmp/yhc/linux-unpacked/resources/bin/yhc');
  assert.equal(linux.rendererURL, 'file:///tmp/yhc/linux-unpacked/resources/webui/index.html');
  assert.equal(linux.launcher, 'xvfb');
  assert.equal(linux.closeStrategy, 'renderer');
  const macIntelPath = '/tmp/yhc/mac/YHC.app/Contents/MacOS/YHC';
  const macIntel = resolveUnpackedLayout(macIntelPath, { platform: 'darwin', arch: 'x64' });
  assert.equal(macIntel.appPath, macIntelPath);
  assert.equal(macIntel.arch, 'x64');
});

test('platform options admit crash injection on supported native hosts and keep no-sandbox Linux-only', () => {
  assert.deepEqual(
    validatePlatformOptions('linux', { crashBackend: true, disableSandbox: true }),
    {
      crashActiveTurn: false,
      crashBackend: true,
      disableSandbox: true,
      reopenWindow: false,
    },
  );
  assert.deepEqual(
    validatePlatformOptions('darwin', { crashBackend: true, disableSandbox: false }),
    {
      crashActiveTurn: false,
      crashBackend: true,
      disableSandbox: false,
      reopenWindow: false,
    },
  );
  assert.deepEqual(
    validatePlatformOptions('win32', { crashBackend: true, disableSandbox: false }),
    {
      crashActiveTurn: false,
      crashBackend: true,
      disableSandbox: false,
      reopenWindow: false,
    },
  );
  assert.deepEqual(
    validatePlatformOptions('linux', { crashActiveTurn: true }),
    {
      crashActiveTurn: true,
      crashBackend: false,
      disableSandbox: false,
      reopenWindow: false,
    },
  );
  assert.throws(
    () => validatePlatformOptions('darwin', { disableSandbox: true }),
    /Linux-only/,
  );
  assert.throws(
    () => validatePlatformOptions('win32', { disableSandbox: true }),
    /Linux-only/,
  );
});

test('Darwin process identity changes make the original process unavailable', () => {
  const expected = {
    pid: 4242,
    pgid: 4242,
    state: 'S',
    startTime: 'Wed Aug 20 12:34:56 2026',
    executable: '/Applications/YHC.app/Contents/Resources/bin/yhc',
  };
  assert.equal(sameProcessIdentity(expected, { ...expected }), true);
  assert.equal(sameProcessIdentity(expected, { ...expected, state: 'R' }), true);
  assert.equal(sameProcessIdentity(expected, { ...expected, state: 'Z' }), false);
  assert.equal(sameProcessIdentity(expected, null), false);
  for (const field of ['pid', 'pgid', 'startTime', 'executable']) {
    const changed = {
      ...expected,
      [field]: field === 'pid' || field === 'pgid' ? 4243 : `${expected[field]} changed`,
    };
    assert.equal(sameProcessIdentity(expected, changed), false, field);
  }
});

test('diagnostics and process-group cleanup remain bounded', () => {
  assert.equal(appendBounded('', Buffer.from('abc'), 5), 'abc');
  assert.equal(appendBounded('abc', Buffer.from('def'), 5), 'bcdef');
  assert.equal(safeProcessGroupID(42), -42);
  for (const pid of [-1, 0, 1, 1.5, Number.NaN]) {
    assert.throws(() => safeProcessGroupID(pid), /unsafe process group/);
  }
});

test('Darwin cleanup rechecks root ownership before every process-group signal', async () => {
  const expected = {
    pid: 4242,
    pgid: 4242,
    state: 'S',
    startTime: 'Wed Aug 20 12:34:56 2026',
    executable: '/Applications/YHC.app/Contents/MacOS/YHC',
  };
  const signals = [];
  let alive = true;
  await terminateProcessGroup(expected.pid, {
    expectedIdentity: expected,
    readIdentity: () => ({ ...expected }),
    groupAlive: () => alive,
    signalProcess: (target, signal) => {
      signals.push([target, signal]);
      alive = false;
    },
    waitFor: async (probe) => assert.equal(await probe(), true),
  });
  assert.deepEqual(signals, [[-expected.pid, 'SIGTERM']]);

  for (const current of [
    null,
    { ...expected, pgid: 4243 },
    { ...expected, state: 'Z' },
    { ...expected, startTime: 'Wed Aug 20 12:34:57 2026' },
    { ...expected, executable: '/tmp/reused' },
  ]) {
    await assert.rejects(
      terminateProcessGroup(expected.pid, {
        expectedIdentity: expected,
        readIdentity: () => current,
        groupAlive: () => true,
        signalProcess: () => assert.fail('unowned process group must not be signalled'),
        waitFor: async () => assert.fail('unowned process group must not be polled'),
      }),
      /cleanup ownership could not be confirmed/,
    );
  }

  await assert.rejects(
    terminateProcessGroup(expected.pid, {
      expectedIdentity: undefined,
      readIdentity: () => ({ ...expected }),
      groupAlive: () => true,
      signalProcess: () => assert.fail('missing snapshot must not be signalled'),
      waitFor: async () => assert.fail('missing snapshot must not be polled'),
    }),
    /cleanup ownership could not be confirmed/,
  );
});

test('Darwin cleanup refuses SIGKILL when ownership changes after SIGTERM', async () => {
  const expected = {
    pid: 4242,
    pgid: 4242,
    state: 'S',
    startTime: 'Wed Aug 20 12:34:56 2026',
    executable: '/Applications/YHC.app/Contents/MacOS/YHC',
  };
  const signals = [];
  let reads = 0;
  await assert.rejects(
    terminateProcessGroup(expected.pid, {
      expectedIdentity: expected,
      readIdentity: () => {
        reads += 1;
        return reads === 1
          ? { ...expected }
          : { ...expected, startTime: 'Wed Aug 20 12:34:57 2026' };
      },
      groupAlive: () => true,
      signalProcess: (target, signal) => signals.push([target, signal]),
      waitFor: async () => { throw new Error('fixture timeout'); },
    }),
    /cleanup ownership could not be confirmed/,
  );
  assert.deepEqual(signals, [[-expected.pid, 'SIGTERM']]);
});

test('Linux cleanup remains unguarded and cleanup diagnostics preserve the primary failure', async () => {
  const signals = [];
  let alive = true;
  await terminateProcessGroup(4242, {
    groupAlive: () => alive,
    signalProcess: (target, signal) => {
      signals.push([target, signal]);
      alive = false;
    },
    waitFor: async (probe) => assert.equal(await probe(), true),
  });
  assert.deepEqual(signals, [[-4242, 'SIGTERM']]);

  const primary = new Error('renderer bootstrap failed');
  const cleanup = new Error('cleanup ownership could not be confirmed');
  const combined = combineSmokeFailure(primary, cleanup);
  assert.match(combined.message, /^renderer bootstrap failed\nCleanup failed:/);
  assert.ok(combined.cause instanceof AggregateError);
  assert.deepEqual(combined.cause.errors, [primary, cleanup]);
});

test('smoke arguments admit one explicit crash mode and reject ambiguity', () => {
  const app = path.resolve('/opt/yhc/yhc-desktop');
  assert.deepEqual(
    parseArguments(['--app', app]),
    {
      appPath: app,
      crashActiveTurn: false,
      crashBackend: false,
      disableSandbox: false,
      reopenWindow: false,
    },
  );
  assert.deepEqual(
    parseArguments(['--crash-backend', '--app', app, '--no-sandbox']),
    {
      appPath: app,
      crashActiveTurn: false,
      crashBackend: true,
      disableSandbox: true,
      reopenWindow: false,
    },
  );
  assert.deepEqual(
    parseArguments(['--app', app, '--crash-active-turn']),
    {
      appPath: app,
      crashActiveTurn: true,
      crashBackend: false,
      disableSandbox: false,
      reopenWindow: false,
    },
  );
  for (const invalid of [
    [],
    ['--app'],
    ['--app', app, '--app', app],
    ['--app', app, '--crash-backend', '--crash-backend'],
    ['--app', app, '--crash-active-turn', '--crash-active-turn'],
    ['--app', app, '--crash-active-turn', '--crash-backend'],
    ['--app', app, '--crash-active-turn', '--reopen-window'],
    ['--app', app, '--no-sandbox', '--no-sandbox'],
    ['--app', app, '--unknown'],
  ]) {
    assert.throws(() => parseArguments(invalid), /usage: unpacked_lifecycle_smoke/);
  }
});

test('active-turn fixture environment injects only smoke-owned loopback provider values', () => {
  const root = fs.mkdtempSync(path.join(os.tmpdir(), 'yhc-active-turn-env-test-'));
  try {
    const isolated = makeIsolatedEnvironment(root, {
      PATH: '/usr/bin',
      PROV: 'private-provider',
      PROV_MODEL: 'private-model',
      PROV_API_KEY: 'private-provider-key',
      PROV_BASE_URL: 'https://private.example.test',
      OPENAI_API_KEY: 'private-openai-key',
    });
    const environment = activeTurnFixtureEnvironment(
      isolated,
      'http://127.0.0.1:43127',
    );
    assert.deepEqual({
      PROV: environment.PROV,
      PROV_MODEL: environment.PROV_MODEL,
      PROV_API_KEY: environment.PROV_API_KEY,
      PROV_BASE_URL: environment.PROV_BASE_URL,
      NO_PROXY: environment.NO_PROXY,
    }, {
      PROV: 'openai',
      PROV_MODEL: 'fixture-model',
      PROV_API_KEY: 'desktop-active-turn-fixture-key',
      PROV_BASE_URL: 'http://127.0.0.1:43127',
      NO_PROXY: '127.0.0.1,localhost',
    });
    assert.equal(Object.hasOwn(environment, 'OPENAI_API_KEY'), false);
    assert.equal(Object.hasOwn(isolated, 'PROV_API_KEY'), false);
    for (const invalid of [
      'https://127.0.0.1:43127',
      'http://localhost:43127',
      'http://127.0.0.1:43127/v1',
    ]) {
      assert.throws(
        () => activeTurnFixtureEnvironment(isolated, invalid),
        /loopback HTTP/,
      );
    }
  } finally {
    fs.rmSync(root, { recursive: true, force: true });
  }
});

test('packaged session seed uses bounded exec argv and accepts only a completed JSON envelope', async () => {
  const calls = [];
  const spawnChild = (command, args, options) => {
    calls.push({ command, args, options });
    const child = new EventEmitter();
    child.pid = 4242;
    child.stdout = new PassThrough();
    child.stderr = new PassThrough();
    child.kill = () => assert.fail('completed seed must not be killed');
    queueMicrotask(() => {
      child.stdout.end(`${JSON.stringify({
        schema_version: 1,
        status: 'completed',
        output: 'Desktop active-turn seed completed.',
        session_id: 'session-fixture-1',
        terminal_reason: 'completed',
        exit_code: 0,
      })}\n`);
      child.stderr.end('Resume this session with a fixture command.\n');
      child.emit('exit', 0, null);
    });
    return child;
  };
  const environment = activeTurnFixtureEnvironment(
    { PATH: '/usr/bin', HOME: '/tmp/fixture-home' },
    'http://127.0.0.1:43127',
  );
  const sessionID = await seedActiveTurnSession({
    backendPath: '/opt/yhc/resources/bin/yhc',
    baseURL: 'http://127.0.0.1:43127',
    environment,
    spawnChild,
    workspace: '/tmp/fixture-workspace',
  });
  assert.equal(sessionID, 'session-fixture-1');
  assert.equal(calls.length, 1);
  assert.equal(calls[0].command, '/opt/yhc/resources/bin/yhc');
  assert.deepEqual(calls[0].args, [
    'exec',
    ACTIVE_TURN_SEED_PROMPT,
    '--output-format', 'json',
    '--max-turns', '1',
    '--tools', '',
  ]);
  assert.equal(calls[0].args.includes('desktop-active-turn-fixture-key'), false);
  assert.equal(calls[0].args.includes('http://127.0.0.1:43127'), false);
  assert.equal(calls[0].args.includes('fixture-model'), false);
  assert.equal(calls[0].options.cwd, '/tmp/fixture-workspace');
  assert.strictEqual(calls[0].options.env, environment);
  assert.equal(calls[0].options.shell, false);
  await assert.rejects(
    seedActiveTurnSession({
      backendPath: '/opt/yhc/resources/bin/yhc',
      baseURL: 'http://127.0.0.1:43127',
      environment: { ...environment, PROV_MODEL: 'wrong-model' },
      spawnChild,
      workspace: '/tmp/fixture-workspace',
    }),
    /provider environment did not match/,
  );
  assert.equal(calls.length, 1);
});

test('packaged seed envelope rejects incomplete and ambiguous session identity', () => {
  const completed = {
    schema_version: 1,
    status: 'completed',
    output: 'Desktop active-turn seed completed.',
    session_id: 'session-fixture-1',
    terminal_reason: 'completed',
    exit_code: 0,
  };
  assert.equal(decodeSeedSession(JSON.stringify(completed)), 'session-fixture-1');
  for (const invalid of [
    '',
    JSON.stringify({ ...completed, status: 'failed' }),
    JSON.stringify({ ...completed, output: 'unexpected' }),
    JSON.stringify({ ...completed, session_id: '' }),
    JSON.stringify({ ...completed, session_id: '../session' }),
    `${JSON.stringify(completed)}\n${JSON.stringify(completed)}`,
  ]) {
    assert.throws(() => decodeSeedSession(invalid), /packaged session seed/);
  }
});

test('active-turn provider completes only the seed and holds the active partial stream', async () => {
  const provider = await startActiveTurnProvider();
  try {
    const endpoint = new URL(provider.baseURL);
    assert.equal(endpoint.hostname, '127.0.0.1');
    assert.notEqual(endpoint.port, '');

    const unauthorized = await fetch(`${provider.baseURL}/responses`, {
      method: 'POST',
      headers: { authorization: 'Bearer wrong', 'content-type': 'application/json' },
      body: JSON.stringify({ model: 'fixture-model', stream: true, input: [] }),
    });
    assert.equal(unauthorized.status, 401);

    const wrongModel = await fetch(`${provider.baseURL}/responses`, {
      method: 'POST',
      headers: {
        authorization: 'Bearer desktop-active-turn-fixture-key',
        'content-type': 'application/json',
      },
      body: JSON.stringify({ model: 'wrong-model', stream: true, input: [] }),
    });
    assert.equal(wrongModel.status, 422);

    const seed = await fetch(`${provider.baseURL}/responses`, {
      method: 'POST',
      headers: {
        authorization: 'Bearer desktop-active-turn-fixture-key',
        'content-type': 'application/json',
      },
      body: JSON.stringify({
        model: 'fixture-model',
        stream: true,
        input: [{ role: 'user', content: ACTIVE_TURN_SEED_PROMPT }],
      }),
    });
    assert.equal(seed.status, 200);
    const seedEvents = await seed.text();
    assert.match(seedEvents, /response\.output_item\.added/);
    assert.match(seedEvents, /response\.content_part\.added/);
    assert.match(seedEvents, /response\.output_text\.delta/);
    assert.match(seedEvents, /response\.output_item\.done/);
    assert.match(seedEvents, /response\.completed/);

    const activeResponsePromise = fetch(`${provider.baseURL}/responses`, {
      method: 'POST',
      headers: {
        authorization: 'Bearer desktop-active-turn-fixture-key',
        'content-type': 'application/json',
      },
      body: JSON.stringify({
        model: 'fixture-model',
        stream: true,
        input: [{ role: 'user', content: ACTIVE_TURN_PROMPT }],
      }),
    });
    await provider.waitForActiveRequest();
    const activeResponse = await activeResponsePromise;
    const reader = activeResponse.body.getReader();
    let activeEvents = '';
    for (let index = 0; index < 4 && !activeEvents.includes(ACTIVE_TURN_PARTIAL); index += 1) {
      const chunk = await reader.read();
      assert.equal(chunk.done, false);
      activeEvents += Buffer.from(chunk.value).toString('utf8');
    }
    assert.match(activeEvents, /response\.output_item\.added/);
    assert.match(activeEvents, /response\.content_part\.added/);
    assert.match(activeEvents, /response\.output_text\.delta/);
    assert.match(activeEvents, new RegExp(ACTIVE_TURN_PARTIAL));
    assert.doesNotMatch(activeEvents, /response\.completed/);
    await reader.cancel();
    assert.deepEqual(provider.requestCounts(), { active: 1, seed: 1, total: 4 });
  } finally {
    await provider.close();
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

  const windowsExpected = {
    pid: 5252,
    parentPid: 5151,
    startTime: '2026-08-24T04:00:00.1234567Z',
    executable: 'd:\\a\\yhc\\resources\\bin\\yhc.exe',
  };
  const taskkills = [];
  killVerifiedBackend(windowsExpected, {
    platform: 'win32',
    readIdentity: () => ({ ...windowsExpected }),
    runTaskkill(command, args, options) {
      taskkills.push({ command, args, options });
      return { status: 0, stdout: '', stderr: '' };
    },
    signalProcess: () => assert.fail('Windows crash injection must use bounded taskkill'),
  });
  assert.equal(taskkills.length, 1);
  assert.equal(taskkills[0].command, 'taskkill.exe');
  assert.deepEqual(taskkills[0].args, ['/PID', '5252', '/F']);
  assert.equal(taskkills[0].options.shell, false);
  assert.equal(taskkills[0].options.windowsHide, true);
  assert.throws(
    () => killVerifiedBackend(windowsExpected, {
      platform: 'win32',
      readIdentity: () => ({
        ...windowsExpected,
        startTime: '2026-08-24T04:00:01.1234567Z',
      }),
      runTaskkill: () => assert.fail('reused Windows PID must not be terminated'),
    }),
    /backend process identity changed before crash injection/,
  );
  assert.throws(
    () => killVerifiedBackend(windowsExpected, {
      platform: 'win32',
      readIdentity: () => ({ ...windowsExpected }),
      runTaskkill: () => ({ status: 1, stdout: '', stderr: 'access denied' }),
    }),
    /Windows backend crash injection failed/,
  );

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

test('active-turn crash oracle requires unfinished snapshot truth and preserved projection', () => {
  const result = {
    bootstrapReady: true,
    notificationCount: 1,
    backendUnavailable: true,
    notice: 'Backend stopped unexpectedly. Restart YHC to reconnect.',
    status: 'Offline',
    checkedControlsDisabled: true,
    activeSessionID: 'session-fixture-1',
    activeTurnID: 'turn-fixture-1',
    snapshotActiveTurnID: 'turn-fixture-1',
    snapshotPartialTurnID: 'engine-turn-fixture-1',
    snapshotPartialCount: 1,
    snapshotPartialCompleted: false,
    partialVisible: true,
    activePromptVisible: true,
    interactionHidden: true,
    sessionIdentityVisible: true,
    terminalEventCount: 0,
  };
  assert.equal(activeTurnCrashContainmentMatches(result), true);
  for (const changed of [
    { ...result, activeSessionID: '' },
    { ...result, activeTurnID: '' },
    { ...result, snapshotActiveTurnID: 'turn-other' },
    { ...result, snapshotPartialTurnID: '' },
    { ...result, snapshotPartialCount: 2 },
    { ...result, snapshotPartialCompleted: true },
    { ...result, partialVisible: false },
    { ...result, activePromptVisible: false },
    { ...result, interactionHidden: false },
    { ...result, sessionIdentityVisible: false },
    { ...result, terminalEventCount: 1 },
  ]) {
    assert.equal(activeTurnCrashContainmentMatches(changed), false);
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
  assert.match(
    makefile,
    /desktop-unpacked-active-turn-crash-smoke-linux-amd64: desktop-package-smoke-linux-amd64/,
  );
  assert.match(
    makefile,
    /unpacked_lifecycle_smoke\.cjs --app desktop\/dist\/linux-unpacked\/yhc-desktop --crash-active-turn\n/,
  );
  assert.match(
    makefile,
    /desktop-unpacked-active-turn-crash-smoke-linux-amd64-ci: desktop-package-smoke-linux-amd64/,
  );
  assert.match(
    makefile,
    /unpacked_lifecycle_smoke\.cjs --app desktop\/dist\/linux-unpacked\/yhc-desktop --crash-active-turn --no-sandbox/,
  );
  assert.match(
    makefile,
    /desktop-unpacked-lifecycle-smoke-darwin-arm64: desktop-package-smoke-darwin-arm64/,
  );
  assert.match(
    makefile,
    /node desktop\/scripts\/unpacked_lifecycle_smoke\.cjs --app desktop\/dist\/mac-arm64\/YHC\.app\/Contents\/MacOS\/YHC/,
  );
  assert.match(workflow, /desktop-unpacked-lifecycle-smoke-linux-amd64-ci/);
  assert.match(workflow, /desktop-unpacked-crash-containment-smoke-linux-amd64-ci/);
  assert.match(workflow, /desktop-unpacked-active-turn-crash-smoke-linux-amd64-ci/);
  assert.doesNotMatch(workflow, /make desktop-unpacked-lifecycle-smoke-linux-amd64\s*(?:\n|$)/);
  assert.doesNotMatch(
    workflow,
    /make desktop-unpacked-crash-containment-smoke-linux-amd64\s*(?:\n|$)/,
  );
  assert.doesNotMatch(workflow, /make desktop-package-smoke-linux-amd64/);
  assert.match(workflow, /macos-15/);
  assert.match(workflow, /desktop-unpacked-native-smokes-darwin-arm64/);
  assert.match(app, /document\.documentElement\.dataset\.yhcBootstrap = 'ready'/);
  assert.match(app, /document\.documentElement\.dataset\.yhcBootstrap = 'error'/);
});

test('window-reopen arguments and platform admission fail closed', () => {
  const app = path.resolve('/opt/yhc/YHC.app/Contents/MacOS/YHC');
  assert.deepEqual(parseArguments(['--app', app, '--reopen-window']), {
    appPath: app,
    crashActiveTurn: false,
    crashBackend: false,
    disableSandbox: false,
    reopenWindow: true,
  });
  assert.deepEqual(validatePlatformOptions('darwin', { reopenWindow: true }), {
    crashActiveTurn: false,
    crashBackend: false,
    disableSandbox: false,
    reopenWindow: true,
  });
  for (const invalid of [
    ['--app', app, '--reopen-window', '--reopen-window'],
    ['--app', app, '--crash-backend', '--reopen-window'],
  ]) {
    assert.throws(() => parseArguments(invalid), /usage: unpacked_lifecycle_smoke/);
  }
  assert.throws(
    () => validatePlatformOptions('linux', { reopenWindow: true }),
    /macOS-only/,
  );
  assert.throws(
    () => validatePlatformOptions('darwin', { crashBackend: true, reopenWindow: true }),
    /mutually exclusive/,
  );
});

test('window-reopen target selection proves the old renderer is gone and identity changes', () => {
  const expectedURL = 'file:///Applications/YHC.app/Contents/Resources/webui/index.html';
  const original = { targetId: 'renderer-old', type: 'page', url: expectedURL };
  const replacement = { targetId: 'renderer-new', type: 'page', url: expectedURL };
  assert.equal(targetID(original), 'renderer-old');
  assert.equal(originalPageTargetClosed([original], expectedURL, 'renderer-old'), false);
  assert.equal(originalPageTargetClosed([], expectedURL, 'renderer-old'), true);
  assert.throws(
    () => originalPageTargetClosed([replacement], expectedURL, 'renderer-old'),
    /replaced before the second instance launched/,
  );
  assert.equal(selectReplacementPageTarget([], expectedURL, 'renderer-old'), null);
  assert.strictEqual(
    selectReplacementPageTarget([replacement], expectedURL, 'renderer-old'),
    replacement,
  );
  assert.throws(
    () => selectReplacementPageTarget([original], expectedURL, 'renderer-old'),
    /target identity was reused/,
  );
  assert.throws(
    () => selectReplacementPageTarget([
      replacement,
      { ...replacement, targetId: 'renderer-extra' },
    ], expectedURL, 'renderer-old'),
    /ambiguous packaged renderer target/,
  );
});

test('repository wiring runs Darwin window restore and crash containment on arm64', async () => {
  const [makefile, workflow] = await Promise.all([
    readFile(new URL('../../Makefile', import.meta.url), 'utf8'),
    readFile(new URL('../../.github/workflows/ci.yml', import.meta.url), 'utf8'),
  ]);
  assert.match(
    makefile,
    /desktop-unpacked-window-reopen-smoke-darwin-arm64: desktop-package-smoke-darwin-arm64/,
  );
  assert.match(
    makefile,
    /unpacked_lifecycle_smoke\.cjs --app desktop\/dist\/mac-arm64\/YHC\.app\/Contents\/MacOS\/YHC --reopen-window/,
  );
  assert.match(
    makefile,
    /desktop-unpacked-crash-containment-smoke-darwin-arm64: desktop-package-smoke-darwin-arm64/,
  );
  assert.match(
    makefile,
    /unpacked_lifecycle_smoke\.cjs --app desktop\/dist\/mac-arm64\/YHC\.app\/Contents\/MacOS\/YHC --crash-backend/,
  );
  assert.match(
    makefile,
    /desktop-unpacked-native-smokes-darwin-arm64: desktop-package-smoke-darwin-arm64/,
  );
  assert.match(
    workflow,
    /platform: macos-arm64[\s\S]*?runner: macos-15[\s\S]*?target: desktop-unpacked-native-smokes-darwin-arm64/,
  );
});

test('repository wiring runs Darwin window restore and crash containment on Intel', async () => {
  const [makefile, workflow] = await Promise.all([
    readFile(new URL('../../Makefile', import.meta.url), 'utf8'),
    readFile(new URL('../../.github/workflows/ci.yml', import.meta.url), 'utf8'),
  ]);
  assert.match(
    makefile,
    /desktop-unpacked-window-reopen-smoke-darwin-amd64: desktop-package-smoke-darwin-amd64/,
  );
  assert.match(
    makefile,
    /unpacked_lifecycle_smoke\.cjs --app desktop\/dist\/mac\/YHC\.app\/Contents\/MacOS\/YHC --reopen-window/,
  );
  assert.match(
    makefile,
    /desktop-unpacked-crash-containment-smoke-darwin-amd64: desktop-package-smoke-darwin-amd64/,
  );
  assert.match(
    makefile,
    /unpacked_lifecycle_smoke\.cjs --app desktop\/dist\/mac\/YHC\.app\/Contents\/MacOS\/YHC --crash-backend/,
  );
  assert.match(
    makefile,
    /desktop-unpacked-native-smokes-darwin-amd64: desktop-package-smoke-darwin-amd64/,
  );
  assert.match(
    workflow,
    /platform: macos-intel[\s\S]*?runner: macos-15-intel[\s\S]*?target: desktop-unpacked-native-smokes-darwin-amd64/,
  );
});

const {
  normalizeWindowsExecutable,
  parseWindowsProcessIdentity,
  parseWindowsProcessSnapshot,
  readWindowsProcessIdentity,
  requireExecutable,
  terminateWindowsProcessTree,
} = require('../scripts/unpacked_lifecycle_smoke.cjs');

test('Windows unpacked layout selects the packaged executable and backend', () => {
  const app = 'D:\\a\\yhc\\desktop\\dist\\win-unpacked\\YHC.exe';
  const layout = resolveUnpackedLayout(app, { platform: 'win32', arch: 'x64' });
  assert.equal(layout.appPath, app);
  assert.equal(layout.backendPath, 'D:\\a\\yhc\\desktop\\dist\\win-unpacked\\resources\\bin\\yhc.exe');
  assert.equal(layout.rendererURL, 'file:///D:/a/yhc/desktop/dist/win-unpacked/resources/webui/index.html');
  assert.equal(layout.launcher, 'direct');
  assert.equal(layout.closeStrategy, 'browser');
  assert.equal(layout.cleanupStrategy, 'windows-tree');
  assert.throws(
    () => resolveUnpackedLayout('D:\\a\\yhc\\desktop\\dist\\YHC.exe', {
      platform: 'win32',
      arch: 'x64',
    }),
    /invalid Windows unpacked Desktop application path/,
  );
  assert.throws(
    () => resolveUnpackedLayout(app, { platform: 'win32', arch: 'arm64' }),
    /requires Windows x64/,
  );
});

test('Windows identity parsing requires PID, creation time, and absolute executable', () => {
  const record = {
    ProcessId: 4321,
    ParentProcessId: 1234,
    CreationTimeUtc: '2026-08-21T04:00:00.1234567Z',
    ExecutablePath: 'D:\\A\\YHC\\YHC.exe',
  };
  assert.deepEqual(parseWindowsProcessIdentity(JSON.stringify(record), 4321), {
    pid: 4321,
    parentPid: 1234,
    startTime: record.CreationTimeUtc,
    executable: 'd:\\a\\yhc\\yhc.exe',
  });
  assert.equal(normalizeWindowsExecutable('D:\\A\\YHC\\..\\YHC\\YHC.exe'), 'd:\\a\\yhc\\yhc.exe');
  for (const invalid of [
    { ...record, ProcessId: 4322 },
    { ...record, CreationTimeUtc: '' },
    { ...record, CreationTimeUtc: 'yesterday' },
    { ...record, ExecutablePath: null },
    { ...record, ExecutablePath: 'YHC.exe' },
  ]) {
    assert.throws(
      () => parseWindowsProcessIdentity(JSON.stringify(invalid), 4321),
      /invalid Windows process identity/,
    );
  }
  assert.throws(
    () => parseWindowsProcessIdentity(JSON.stringify([record]), 4321),
    /invalid Windows process identity/,
  );
});

test('Windows process snapshots retain incomplete unrelated rows for fail-closed traversal', () => {
  const snapshot = parseWindowsProcessSnapshot(JSON.stringify([
    {
      ProcessId: 4321,
      ParentProcessId: 1234,
      CreationTimeUtc: '2026-08-21T04:00:00.1234567Z',
      ExecutablePath: 'D:\\A\\YHC\\YHC.exe',
    },
    {
      ProcessId: 7,
      ParentProcessId: 4,
      CreationTimeUtc: null,
      ExecutablePath: null,
    },
  ]));
  assert.deepEqual(snapshot[0], {
    pid: 4321,
    parentPid: 1234,
    startTime: '2026-08-21T04:00:00.1234567Z',
    executable: 'd:\\a\\yhc\\yhc.exe',
  });
  assert.deepEqual(snapshot[1], {
    pid: 7,
    parentPid: 4,
    startTime: null,
    executable: null,
  });
  assert.throws(
    () => parseWindowsProcessSnapshot('{"ProcessId":4321}'),
    /invalid Windows process snapshot/,
  );
});

test('Windows identity reader uses a bounded no-profile CIM query', () => {
  const calls = [];
  const identity = readWindowsProcessIdentity(4321, {
    runPowerShell(command, args, options) {
      calls.push({ command, args, options });
      return {
        status: 0,
        stdout: JSON.stringify({
          ProcessId: 4321,
          ParentProcessId: 1234,
          CreationTimeUtc: '2026-08-21T04:00:00.1234567Z',
          ExecutablePath: 'D:\\A\\YHC\\YHC.exe',
        }),
        stderr: '',
      };
    },
  });
  assert.equal(identity.pid, 4321);
  assert.equal(calls[0].command, 'powershell.exe');
  assert.deepEqual(calls[0].args.slice(0, 3), ['-NoLogo', '-NoProfile', '-NonInteractive']);
  assert.match(calls[0].args.at(-1), /ProcessId = 4321/);
  assert.equal(calls[0].options.shell, false);
  assert.equal(calls[0].options.windowsHide, true);
  assert.equal(readWindowsProcessIdentity(4321, {
    runPowerShell: () => ({ status: 3, stdout: '', stderr: '' }),
  }), null);
  assert.throws(
    () => readWindowsProcessIdentity(4321, {
      runPowerShell: () => ({ status: 1, stdout: '', stderr: 'denied' }),
    }),
    /Windows process inspection failed/,
  );
});

test('Windows executable admission does not rely on POSIX mode bits', () => {
  const root = fs.mkdtempSync(path.join(os.tmpdir(), 'yhc-windows-executable-'));
  const executable = path.join(root, 'YHC.exe');
  try {
    fs.writeFileSync(executable, 'fixture', { mode: 0o600 });
    assert.equal(
      requireExecutable(executable, 'Windows fixture', { platform: 'win32' }),
      fs.realpathSync(executable),
    );
    assert.throws(
      () => requireExecutable(executable, 'Unix fixture', { platform: 'linux' }),
      /not an executable regular file/,
    );
  } finally {
    fs.rmSync(root, { recursive: true, force: true });
  }
});

test('Windows cleanup validates the visible tree and fresh root identity before taskkill', async () => {
  const root = {
    pid: 4321,
    parentPid: 111,
    startTime: '2026-08-21T04:00:00.1234567Z',
    executable: 'd:\\a\\yhc\\yhc.exe',
  };
  const backend = {
    pid: 4322,
    parentPid: 4321,
    startTime: '2026-08-21T04:00:01.1234567Z',
    executable: 'd:\\a\\yhc\\resources\\bin\\yhc.exe',
  };
  const helper = {
    pid: 4323,
    parentPid: 4321,
    startTime: '2026-08-21T04:00:01.2234567Z',
    executable: root.executable,
  };
  let killed = false;
  const calls = [];
  await terminateWindowsProcessTree(root, {
    expectedBackend: backend,
    readIdentity: (pid) => killed ? null : (pid === root.pid ? root : backend),
    readSnapshot: () => [root, backend, helper],
    runTaskkill(command, args, options) {
      calls.push({ command, args, options });
      killed = true;
      return { status: 0, stdout: '', stderr: '' };
    },
    waitFor: async (probe) => assert.equal(probe(), true),
  });
  assert.equal(calls.length, 1);
  assert.equal(calls[0].command, 'taskkill.exe');
  assert.deepEqual(calls[0].args, ['/PID', '4321', '/T', '/F']);
  assert.equal(calls[0].options.shell, false);
  assert.equal(calls[0].options.windowsHide, true);
});

test('Windows cleanup refuses unknown descendants and PID reuse', async () => {
  const root = {
    pid: 4321,
    parentPid: 111,
    startTime: '2026-08-21T04:00:00.1234567Z',
    executable: 'd:\\a\\yhc\\yhc.exe',
  };
  let signals = 0;
  await assert.rejects(
    terminateWindowsProcessTree(root, {
      readIdentity: () => root,
      readSnapshot: () => [
        root,
        {
          pid: 4322,
          parentPid: root.pid,
          startTime: '2026-08-21T04:00:01.1234567Z',
          executable: 'c:\\windows\\system32\\cmd.exe',
        },
      ],
      runTaskkill: () => {
        signals += 1;
        return { status: 0, stdout: '', stderr: '' };
      },
    }),
    /Windows process tree contained an unowned executable/,
  );
  let reads = 0;
  await assert.rejects(
    terminateWindowsProcessTree(root, {
      readIdentity: () => (++reads === 1 ? root : { ...root, startTime: '2026-08-21T04:01:00Z' }),
      readSnapshot: () => [root],
      runTaskkill: () => {
        signals += 1;
        return { status: 0, stdout: '', stderr: '' };
      },
    }),
    /Windows cleanup ownership could not be confirmed/,
  );
  assert.equal(signals, 0);
});

test('repository wiring runs Windows lifecycle and crash containment after package verification', async () => {
  const [makefile, workflow] = await Promise.all([
    readFile(new URL('../../Makefile', import.meta.url), 'utf8'),
    readFile(new URL('../../.github/workflows/ci.yml', import.meta.url), 'utf8'),
  ]);
  assert.match(
    makefile,
    /desktop-unpacked-lifecycle-smoke-windows-amd64: desktop-package-smoke-windows-amd64/,
  );
  assert.match(
    makefile,
    /unpacked_lifecycle_smoke\.cjs --app desktop\/dist\/win-unpacked\/YHC\.exe/,
  );
  assert.match(
    makefile,
    /desktop-unpacked-crash-containment-smoke-windows-amd64: desktop-package-smoke-windows-amd64/,
  );
  assert.match(
    makefile,
    /unpacked_lifecycle_smoke\.cjs --app desktop\/dist\/win-unpacked\/YHC\.exe --crash-backend/,
  );
  assert.match(
    makefile,
    /desktop-unpacked-native-smokes-windows-amd64: desktop-package-smoke-windows-amd64/,
  );
  assert.match(
    workflow,
    /platform: windows-x64[\s\S]*?runner: windows-2025[\s\S]*?target: desktop-unpacked-native-smokes-windows-amd64/,
  );
});
