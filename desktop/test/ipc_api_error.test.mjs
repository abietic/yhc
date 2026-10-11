import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { createRequire } from 'node:module';
import path from 'node:path';
import test from 'node:test';
import { fileURLToPath } from 'node:url';
import vm from 'node:vm';

import { createTransport } from '../../internal/webui/assets/transport.mjs';

const mainPath = new URL('../main.cjs', import.meta.url);
const preloadPath = new URL('../preload.cjs', import.meta.url);
const mainRequire = createRequire(mainPath);
const message = 'settings changed elsewhere; reload before saving';

// Electron copies plain return data, but only the message of a thrown Error
// survives either IPC or contextBridge. Model both boundaries independently.
async function copyBoundary(invoke) {
  try {
    return structuredClone(await invoke());
  } catch (error) {
    throw new Error(error.message);
  }
}

function controlledTimers() {
  const timers = [];
  return {
    timers,
    setTimeout(callback, delay) {
      const timer = { callback, delay, cleared: false };
      timers.push(timer);
      return timer;
    },
    clearTimeout(timer) {
      timer.cleared = true;
    },
    fire(index = 0) {
      const timer = timers[index];
      if (!timer.cleared) timer.callback();
    },
  };
}

function hostFixture(fetch, timers = globalThis) {
  const handlers = new Map();
  const sender = {};
  const electron = {
    app: { requestSingleInstanceLock: () => false, quit: () => {} },
    ipcMain: { handle: (channel, handler) => handlers.set(channel, handler) },
  };
  const context = vm.createContext({
    require: (name) => {
      if (name === 'electron') return electron;
      if (name === 'node:child_process') {
        return { spawn: () => assert.fail('fixture must not launch a backend') };
      }
      return mainRequire(name);
    },
    __dirname: path.dirname(fileURLToPath(mainPath)),
    process: { platform: 'darwin', env: {} },
    fetch,
    URL: globalThis.URL,
    AbortController: globalThis.AbortController,
    setTimeout: timers.setTimeout,
    clearTimeout: timers.clearTimeout,
    fixtureSender: sender,
  });
  vm.runInContext(readFileSync(mainPath, 'utf8'), context, { filename: 'main.cjs' });
  vm.runInContext(`
    bootstrap = { url: 'http://fixture.invalid', token: 'fixture-only-token' };
    mainWindow = { isDestroyed: () => false, webContents: fixtureSender };
  `, context);
  let bridge;
  vm.runInNewContext(readFileSync(preloadPath, 'utf8'), {
    require: (name) => {
      assert.equal(name, 'electron', 'sandboxed preload must use only Electron');
      return {
        contextBridge: {
          exposeInMainWorld: (name, value) => {
            assert.equal(name, 'yhcDesktop');
            assert.equal(Object.isFrozen(value), true);
            bridge = Object.fromEntries(Object.entries(value).map(([key, invoke]) => [
              key, (...args) => copyBoundary(() => invoke(...args)),
            ]));
          },
        },
        ipcRenderer: {
          invoke: (channel, ...args) => copyBoundary(() => {
            assert.equal(typeof handlers.get(channel), 'function', channel);
            return handlers.get(channel)({ sender }, ...args);
          }),
        },
      };
    },
  }, { filename: 'preload.cjs' });
  return { bridge, handlers, sender };
}

async function withTransport(bridge, invoke) {
  const descriptor = Object.getOwnPropertyDescriptor(globalThis, 'window');
  globalThis.window = { yhcDesktop: bridge };
  try {
    return await invoke(createTransport());
  } finally {
    if (descriptor) Object.defineProperty(globalThis, 'window', descriptor);
    else delete globalThis.window;
  }
}

async function withBrowserTransport(fetch, timers, invoke) {
  const originals = new Map(['window', 'fetch', 'setTimeout', 'clearTimeout'].map((key) => [
    key, Object.getOwnPropertyDescriptor(globalThis, key),
  ]));
  Object.defineProperties(globalThis, {
    window: { configurable: true, writable: true, value: { location: { origin: 'http://fixture.invalid' } } },
    fetch: { configurable: true, writable: true, value: fetch },
    setTimeout: { configurable: true, writable: true, value: timers.setTimeout },
    clearTimeout: { configurable: true, writable: true, value: timers.clearTimeout },
  });
  try {
    return await invoke(createTransport());
  } finally {
    for (const [key, descriptor] of originals) {
      if (descriptor) Object.defineProperty(globalThis, key, descriptor);
      else delete globalThis[key];
    }
  }
}

function failureResponse(code = 'settings_conflict', status = 409) {
  return {
    ok: false, status,
    json: async () => ({ error: { message, code, private_detail: 'private-sentinel' } }),
  };
}

test('renderer retains HTTP error code and status across IPC and contextBridge', async () => {
  const { bridge } = hostFixture(async () => failureResponse());
  await withTransport(bridge, async (transport) => {
    await assert.rejects(transport.api('updateSettings', {
      scope: 'user', revision: 'fixture-revision', changes: { model: 'fixture-model' },
    }), (error) => {
      assert.equal(error.message, message);
      assert.equal(error.code, 'settings_conflict');
      assert.equal(error.status, 409);
      assert.deepEqual(Object.keys(error).sort(), ['code', 'status']);
      return true;
    });
  });
});

test('structured API projects only public fields and leaves legacy API unchanged', async () => {
  const { bridge } = hostFixture(async () => failureResponse());
  assert.equal(typeof bridge.apiResult, 'function');
  assert.deepEqual(await bridge.apiResult('getSettings', { scope: 'user' }), {
    ok: false, error: { message, code: 'settings_conflict', status: 409 },
  });
  await assert.rejects(bridge.api('getSettings', { scope: 'user' }), (error) => {
    assert.equal(error.message, message);
    assert.equal(error.code, undefined);
    return true;
  });
});

test('success data and 204 null round trip without changing legacy API results', async () => {
  for (const [status, value] of [[200, { sessions: [] }], [204, null]]) {
    const { bridge } = hostFixture(async () => ({
      ok: true, status, json: async () => value,
    }));
    assert.deepEqual(await bridge.api('listSessions'), value);
    assert.deepEqual(await bridge.apiResult('listSessions'), { ok: true, value });
    await withTransport(bridge, async (transport) => {
      assert.deepEqual(await transport.api('listSessions'), value);
    });
  }
});

test('both API channels retain sender, operation and native picker restrictions', async () => {
  let requests = 0;
  const { bridge, handlers, sender } = hostFixture(async () => {
    requests++;
    assert.fail('rejected requests must not reach the backend');
  });
  for (const channel of ['app:api', 'app:api-result']) {
    assert.equal(typeof handlers.get(channel), 'function');
    await assert.rejects(async () => handlers.get(channel)(
      { sender: {} }, 'listSessions', {},
    ), /untrusted desktop IPC sender/);
  }
  for (const operation of ['createWorkspace', '/v1/settings', 'unknownOperation']) {
    await assert.rejects(bridge.api(operation, { cwd: '/fixture/not-a-workspace' }));
    const result = await bridge.apiResult(operation, { cwd: '/fixture/not-a-workspace' });
    assert.equal(result.ok, false);
    assert.equal(typeof result.error.message, 'string');
    assert.equal(Object.keys(result.error).length, 1);
  }
  assert.equal(requests, 0);
  assert.ok(sender);
});

test('unknown backend failures do not project private network details', async () => {
  const { bridge } = hostFixture(async () => {
    throw new Error('private-network-sentinel', { cause: { token: 'private-token' } });
  });
  const result = await bridge.apiResult('listSessions');
  assert.deepEqual(result, {
    ok: false, error: { message: 'desktop backend request failed' },
  });
});

test('invalid backend error codes are not copied into the structured result', async () => {
  const { bridge } = hostFixture(async () => failureResponse('private-code/invalid'));
  assert.deepEqual(await bridge.apiResult('listSessions'), {
    ok: false, error: { message, status: 409 },
  });
});

test('settings IPC aborts after the fixed deadline and exposes only the public timeout result', async () => {
  const clock = controlledTimers();
  let signal;
  const { bridge } = hostFixture((_url, init) => new Promise((resolve, reject) => {
    signal = init.signal;
    signal.addEventListener('abort', () => reject(Object.assign(new Error('private abort detail'), { name: 'AbortError' })));
  }), clock);
  const pending = bridge.apiResult('updateSettings', {
    scope: 'user', revision: 'fixture-revision', changes: { model: 'fixture-model' },
  });
  assert.equal(clock.timers.length, 1);
  assert.equal(clock.timers[0].delay, 15_000);
  assert.ok(signal);
  clock.fire();
  assert.deepEqual(await pending, {
    ok: false,
    error: { message: 'Settings request timed out. Reload before saving.', code: 'settings_timeout' },
  });
  assert.equal(clock.timers[0].cleared, true);
});

test('network errors cannot impersonate a locally-owned settings deadline', async () => {
  const networkError = new Error('private-network-sentinel');
  networkError.code = 'settings_timeout';
  const { bridge } = hostFixture(async () => { throw networkError; });
  assert.deepEqual(await bridge.apiResult('listSessions'), {
    ok: false,
    error: { message: 'desktop backend request failed' },
  });

  const clock = controlledTimers();
  const { bridge: bounded } = hostFixture(async (_url, init) => {
    assert.equal(init.signal, undefined);
    return { ok: true, status: 200, json: async () => ({ sessions: [] }) };
  }, clock);
  assert.deepEqual(await bounded.apiResult('listSessions'), { ok: true, value: { sessions: [] } });
  assert.equal(clock.timers.length, 0);
});

test('settings IPC aborts a hanging response body after the fixed deadline', async () => {
  const clock = controlledTimers();
  let signal;
  let markBodyStarted;
  const bodyStarted = new Promise((resolve) => { markBodyStarted = resolve; });
  const { bridge } = hostFixture(async (_url, init) => {
    signal = init.signal;
    return {
      ok: true,
      status: 200,
      json: () => new Promise((resolve, reject) => {
        markBodyStarted();
        signal.addEventListener('abort', () => reject(Object.assign(
          new Error('private response body abort'), { name: 'AbortError' },
        )));
      }),
    };
  }, clock);
  const pending = bridge.apiResult('getSettings', { scope: 'user' });
  await bodyStarted;
  assert.ok(signal);
  assert.equal(clock.timers.length, 1);
  clock.fire();
  assert.deepEqual(await pending, {
    ok: false,
    error: { message: 'Settings request timed out. Reload before saving.', code: 'settings_timeout' },
  });
  assert.equal(clock.timers[0].cleared, true);
});

test('browser settings requests abort after the fixed deadline and retain it through response JSON', async () => {
  const clock = controlledTimers();
  let signal;
  await withBrowserTransport((_url, init) => new Promise((resolve, reject) => {
    signal = init.signal;
    signal.addEventListener('abort', () => reject(Object.assign(new Error('private abort detail'), { name: 'AbortError' })));
  }), clock, async (transport) => {
    const pending = transport.api('updateSettings', {
      scope: 'user', revision: 'fixture-revision', changes: { model: 'fixture-model' },
    });
    assert.equal(clock.timers.length, 1);
    assert.equal(clock.timers[0].delay, 15_000);
    assert.ok(signal);
    clock.fire();
    await assert.rejects(pending, (error) => {
      assert.equal(error.message, 'Settings request timed out. Reload before saving.');
      assert.equal(error.code, 'settings_timeout');
      assert.equal(error.status, undefined);
      return true;
    });
    assert.equal(clock.timers[0].cleared, true);
  });

  const bodyClock = controlledTimers();
  let bodySignal;
  let markBrowserBodyStarted;
  const browserBodyStarted = new Promise((resolve) => { markBrowserBodyStarted = resolve; });
  await withBrowserTransport(async (_url, init) => {
    bodySignal = init.signal;
    return {
      ok: true,
      status: 200,
      json: () => new Promise((resolve, reject) => {
        markBrowserBodyStarted();
        bodySignal.addEventListener('abort', () => reject(Object.assign(
          new Error('private response body abort'), { name: 'AbortError' },
        )));
      }),
    };
  }, bodyClock, async (transport) => {
    const pending = transport.api('getSettings', { scope: 'user' });
    await browserBodyStarted;
    assert.ok(bodySignal);
    assert.equal(bodyClock.timers.length, 1);
    bodyClock.fire();
    await assert.rejects(pending, (error) => {
      assert.equal(error.message, 'Settings request timed out. Reload before saving.');
      assert.equal(error.code, 'settings_timeout');
      assert.equal(error.status, undefined);
      return true;
    });
    assert.equal(bodyClock.timers[0].cleared, true);
  });

  const responseClock = controlledTimers();
  let finishJSON;
  await withBrowserTransport(async () => ({
    ok: true,
    status: 200,
    json: () => new Promise((resolve) => { finishJSON = resolve; }),
  }), responseClock, async (transport) => {
    const pending = transport.api('getSettings', { scope: 'user' });
    await Promise.resolve(); await Promise.resolve();
    assert.equal(responseClock.timers.length, 1);
    assert.equal(responseClock.timers[0].cleared, false);
    finishJSON({ scope: 'user' });
    assert.deepEqual(await pending, { scope: 'user' });
    assert.equal(responseClock.timers[0].cleared, true);
  });
});

test('renderer rejects malformed result envelopes without using the legacy route', async () => {
  for (const result of [
    null, {}, { ok: true }, { ok: 'false', value: null },
    { ok: false, error: {} },
    { ok: false, error: { message: 'failure', code: 'invalid/code' } },
    { ok: false, error: { message: 'failure', status: 200 } },
  ]) {
    await withTransport({
      apiResult: async () => result,
      api: () => assert.fail('invalid result must not retry a mutation'),
    }, async (transport) => {
      await assert.rejects(transport.api('listSessions'), /invalid desktop API result/);
    });
  }
});

test('renderer supports an older bridge without apiResult', async () => {
  await withTransport({
    api: async (operation, payload) => ({ operation, payload }),
  }, async (transport) => {
    assert.deepEqual(await transport.api('listSessions', { limit: 10 }), {
      operation: 'listSessions', payload: { limit: 10 },
    });
  });
});
