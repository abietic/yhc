import assert from 'node:assert/strict';
import { EventEmitter } from 'node:events';
import { createRequire } from 'node:module';
import test from 'node:test';

const require = createRequire(import.meta.url);
const {
  APP_SERVER_SHUTDOWN_BUDGET_MS,
  BACKEND_FORCE_EXIT_TIMEOUT_MS,
  BACKEND_GRACEFUL_STOP_TIMEOUT_MS,
  activeTurnQuitPrompt,
  activeTurnSessions,
  backendStopFailurePrompt,
  createBackendStopCoordinator,
  createQuitRequestScheduler,
  createWindowRestoreCoordinator,
  quitInspectionFailurePrompt,
  startDesktopHost,
} = require('../lifecycle.cjs');

function controlledTimers() {
  const timers = [];
  return {
    timers,
    setTimeout(callback, delay) {
      const timer = {
        callback,
        cleared: false,
        delay,
        unrefCalled: false,
        unref() { this.unrefCalled = true; },
      };
      timers.push(timer);
      return timer;
    },
    clearTimeout(timer) {
      timer.cleared = true;
    },
    fire(index) {
      const timer = timers[index];
      if (!timer.cleared) timer.callback();
    },
  };
}

function fakeChild(order) {
  const child = new EventEmitter();
  child.exitCode = null;
  child.kill = (signal) => {
    order.push(`kill:${signal}`);
    return true;
  };
  return child;
}

function deferred() {
  let resolve;
  let reject;
  const promise = new Promise((resolvePromise, rejectPromise) => {
    resolve = resolvePromise;
    reject = rejectPromise;
  });
  return { promise, reject, resolve };
}

function fakeWindow(order, { destroyed = false, minimized = false } = {}) {
  return {
    destroy() {
      order.push('destroy');
      destroyed = true;
    },
    focus() {
      order.push('focus');
    },
    isDestroyed() {
      return destroyed;
    },
    isMinimized() {
      return minimized;
    },
    restore() {
      order.push('restore');
      minimized = false;
    },
  };
}

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

test('quit requests start only after the before-quit handler returns', () => {
  const order = [];
  const pending = [];
  const scheduler = createQuitRequestScheduler({
    requestQuit: () => order.push('request-quit'),
    schedule: (callback) => {
      order.push('schedule');
      pending.push(callback);
    },
  });

  scheduler.request();
  scheduler.request();
  assert.deepEqual(order, ['schedule']);
  assert.equal(pending.length, 1);

  pending.shift()();
  assert.deepEqual(order, ['schedule', 'request-quit']);
});

test('Desktop startup prepares the hidden host before backend secure-storage work', async () => {
  const order = [];
  const targetWindow = {};
  await startDesktopHost({
    prepareWindow: () => {
      order.push('prepare-window');
      return targetWindow;
    },
    startBackend: async () => order.push('start-backend'),
    backendAvailable: () => {
      order.push('verify-backend');
      return true;
    },
    loadRenderer: async (target) => {
      assert.strictEqual(target, targetWindow);
      order.push('load-renderer');
    },
    stopBackend: async () => order.push('stop-backend'),
  });
  assert.deepEqual(order, [
    'prepare-window',
    'start-backend',
    'verify-backend',
    'load-renderer',
  ]);
});

test('window restoration focuses a live window without requiring the backend', async () => {
  const order = [];
  const current = fakeWindow(order, { minimized: true });
  const coordinator = createWindowRestoreCoordinator({
    currentWindow: () => current,
    backendAvailable: () => false,
    createWindow: () => assert.fail('live window must not be replaced'),
    loadRenderer: () => assert.fail('live window must not be reloaded'),
  });

  assert.strictEqual(await coordinator.restore(), current);
  assert.deepEqual(order, ['restore', 'focus']);
});

test('window restoration replaces a destroyed owner when the backend is available', async () => {
  const order = [];
  let current = fakeWindow(order, { destroyed: true });
  const replacement = fakeWindow(order);
  const coordinator = createWindowRestoreCoordinator({
    currentWindow: () => current,
    backendAvailable: () => true,
    createWindow: () => {
      order.push('create');
      current = replacement;
      return replacement;
    },
    loadRenderer: async (target) => {
      assert.strictEqual(target, replacement);
      order.push('load');
    },
  });

  assert.strictEqual(await coordinator.restore(), replacement);
  assert.deepEqual(order, ['create', 'load']);
});

test('overlapping window restoration requests share one create-load attempt', async () => {
  const order = [];
  const loading = deferred();
  let current = null;
  const replacement = fakeWindow(order);
  const coordinator = createWindowRestoreCoordinator({
    currentWindow: () => current,
    backendAvailable: () => true,
    createWindow: () => {
      order.push('create');
      current = replacement;
      return replacement;
    },
    loadRenderer: (target) => {
      assert.strictEqual(target, replacement);
      order.push('load');
      return loading.promise;
    },
  });

  const first = coordinator.restore();
  const second = coordinator.restore();
  assert.strictEqual(second, first);
  await Promise.resolve();
  assert.deepEqual(order, ['create', 'load']);
  loading.resolve();
  assert.strictEqual(await first, replacement);
});

test('failed restoration destroys only its owned target and permits a retry', async () => {
  const order = [];
  const loadFailure = new Error('renderer failed');
  let current = null;
  let attempts = 0;
  const coordinator = createWindowRestoreCoordinator({
    currentWindow: () => current,
    backendAvailable: () => true,
    createWindow: () => {
      attempts += 1;
      order.push(`create:${attempts}`);
      current = fakeWindow(order);
      return current;
    },
    loadRenderer: async () => {
      order.push(`load:${attempts}`);
      if (attempts === 1) throw loadFailure;
    },
  });

  await assert.rejects(coordinator.restore(), (error) => error === loadFailure);
  assert.deepEqual(order, ['create:1', 'load:1', 'destroy']);
  assert.strictEqual(await coordinator.restore(), current);
  assert.deepEqual(order, ['create:1', 'load:1', 'destroy', 'create:2', 'load:2']);
});

test('failed restoration cannot destroy a newer window owner', async () => {
  const order = [];
  const loading = deferred();
  let current = null;
  const attempted = fakeWindow(order);
  const newer = fakeWindow(order);
  const coordinator = createWindowRestoreCoordinator({
    currentWindow: () => current,
    backendAvailable: () => true,
    createWindow: () => {
      current = attempted;
      return attempted;
    },
    loadRenderer: () => loading.promise,
  });

  const restoring = coordinator.restore();
  await Promise.resolve();
  current = newer;
  loading.reject(new Error('renderer failed'));
  await assert.rejects(restoring, /renderer failed/);
  assert.deepEqual(order, []);
});

test('window restoration stays idle when both window and backend are unavailable', async () => {
  const coordinator = createWindowRestoreCoordinator({
    currentWindow: () => null,
    backendAvailable: () => false,
    createWindow: () => assert.fail('missing backend must prevent window creation'),
    loadRenderer: () => assert.fail('missing backend must prevent renderer loading'),
  });

  assert.equal(await coordinator.restore(), null);
});

test('window restoration never reloads an existing hidden startup window', async () => {
  const order = [];
  const current = fakeWindow(order);
  const coordinator = createWindowRestoreCoordinator({
    currentWindow: () => current,
    backendAvailable: () => false,
    createWindow: () => assert.fail('startup window must remain the owner'),
    loadRenderer: () => assert.fail('startup renderer load has a separate owner'),
  });

  assert.strictEqual(await coordinator.restore(), current);
  assert.deepEqual(order, ['focus']);
});

test('Desktop startup never loads the renderer without an accepted backend', async () => {
  const order = [];
  await assert.rejects(startDesktopHost({
    prepareWindow: () => order.push('prepare-window'),
    startBackend: async () => order.push('start-backend'),
    backendAvailable: () => false,
    loadRenderer: async () => order.push('load-renderer'),
    stopBackend: async () => order.push('stop-backend'),
  }), /backend stopped during startup/);
  assert.deepEqual(order, ['prepare-window', 'start-backend', 'stop-backend']);
});

test('Desktop startup stops an accepted backend when renderer loading fails', async () => {
  const order = [];
  const rendererFailure = new Error('renderer failed');
  await assert.rejects(startDesktopHost({
    prepareWindow: () => order.push('prepare-window'),
    startBackend: async () => order.push('start-backend'),
    backendAvailable: () => true,
    loadRenderer: async () => {
      order.push('load-renderer');
      throw rendererFailure;
    },
    stopBackend: async () => order.push('stop-backend'),
  }), (error) => error === rendererFailure);
  assert.deepEqual(order, [
    'prepare-window',
    'start-backend',
    'load-renderer',
    'stop-backend',
  ]);
});

test('Desktop startup reports cleanup failure without losing the primary failure', async () => {
  const rendererFailure = new Error('renderer failed');
  const cleanupFailure = new Error('backend cleanup failed');
  await assert.rejects(startDesktopHost({
    prepareWindow: () => {},
    startBackend: async () => {},
    backendAvailable: () => true,
    loadRenderer: async () => { throw rendererFailure; },
    stopBackend: async () => { throw cleanupFailure; },
  }), (error) => {
    assert.ok(error instanceof AggregateError);
    assert.equal(error.message, 'Desktop startup failed and backend cleanup failed');
    assert.equal(error.cause, rendererFailure);
    assert.deepEqual(error.errors, [rendererFailure, cleanupFailure]);
    return true;
  });
});

test('backend graceful stop budget cannot preempt app-server cleanup', () => {
  assert.equal(APP_SERVER_SHUTDOWN_BUDGET_MS, 15_000);
  assert.ok(BACKEND_GRACEFUL_STOP_TIMEOUT_MS > APP_SERVER_SHUTDOWN_BUDGET_MS);
  assert.equal(BACKEND_FORCE_EXIT_TIMEOUT_MS, 3_000);
});

test('backend stop waits for graceful exit before escalation', async () => {
  const order = [];
  const child = fakeChild(order);
  const clock = controlledTimers();
  const coordinator = createBackendStopCoordinator({
    markStopping: () => order.push('mark-stopping'),
    stopEventStreams: () => order.push('stop-streams'),
    setTimeout: clock.setTimeout,
    clearTimeout: clock.clearTimeout,
  });
  const stopping = coordinator.stop(child);

  assert.deepEqual(order, ['mark-stopping', 'stop-streams', 'kill:SIGINT']);
  assert.deepEqual(clock.timers.map((timer) => timer.delay), [
    BACKEND_GRACEFUL_STOP_TIMEOUT_MS,
  ]);
  assert.deepEqual(clock.timers.map((timer) => timer.unrefCalled), [true]);

  child.exitCode = 0;
  child.emit('exit', 0, null);
  await stopping;
  clock.fire(0);
  assert.deepEqual(order, ['mark-stopping', 'stop-streams', 'kill:SIGINT']);
  assert.deepEqual(clock.timers.map((timer) => timer.cleared), [true]);
  assert.equal(child.listenerCount('exit'), 0);
});

test('backend stop escalates only after the graceful budget', async () => {
  const order = [];
  const child = fakeChild(order);
  const clock = controlledTimers();
  const coordinator = createBackendStopCoordinator({
    markStopping: () => order.push('mark-stopping'),
    stopEventStreams: () => order.push('stop-streams'),
    setTimeout: clock.setTimeout,
    clearTimeout: clock.clearTimeout,
  });
  const stopping = coordinator.stop(child);

  clock.fire(0);
  assert.deepEqual(order, [
    'mark-stopping', 'stop-streams', 'kill:SIGINT', 'kill:SIGKILL',
  ]);
  assert.deepEqual(clock.timers.map((timer) => timer.delay), [
    BACKEND_GRACEFUL_STOP_TIMEOUT_MS,
    BACKEND_FORCE_EXIT_TIMEOUT_MS,
  ]);
  assert.equal(clock.timers[1].unrefCalled, true);
  child.exitCode = 0;
  child.emit('exit', 0, 'SIGKILL');
  await stopping;
  assert.equal(child.listenerCount('exit'), 0);
});

test('backend stop rejects when forced termination is not observed', async () => {
  const order = [];
  const child = fakeChild(order);
  const clock = controlledTimers();
  const coordinator = createBackendStopCoordinator({
    markStopping: () => order.push('mark-stopping'),
    unmarkStopping: () => order.push('unmark-stopping'),
    stopEventStreams: () => {},
    setTimeout: clock.setTimeout,
    clearTimeout: clock.clearTimeout,
  });
  const stopping = coordinator.stop(child);

  clock.fire(0);
  clock.fire(1);
  await assert.rejects(stopping, /backend did not stop in time/);
  assert.deepEqual(order, [
    'mark-stopping', 'kill:SIGINT', 'kill:SIGKILL', 'unmark-stopping',
  ]);
  assert.equal(child.listenerCount('exit'), 0);
  assert.equal(child.listenerCount('error'), 0);
});

test('overlapping backend stops share one signal lifecycle', async () => {
  const order = [];
  const child = fakeChild(order);
  const clock = controlledTimers();
  const coordinator = createBackendStopCoordinator({
    markStopping: () => order.push('mark-stopping'),
    stopEventStreams: () => order.push('stop-streams'),
    setTimeout: clock.setTimeout,
    clearTimeout: clock.clearTimeout,
  });

  const first = coordinator.stop(child);
  const second = coordinator.stop(child);
  assert.strictEqual(second, first);
  assert.deepEqual(order, ['mark-stopping', 'stop-streams', 'kill:SIGINT']);
  assert.equal(clock.timers.length, 1);

  child.exitCode = 0;
  child.emit('exit', 0, null);
  await Promise.all([first, second]);
});

test('backend stop rejects failed signals without timer or listener leaks', async () => {
  for (const failedSignal of ['SIGINT', 'SIGKILL']) {
    const order = [];
    const child = fakeChild(order);
    child.kill = (signal) => {
      order.push(`kill:${signal}`);
      return signal !== failedSignal;
    };
    const clock = controlledTimers();
    const coordinator = createBackendStopCoordinator({
      setTimeout: clock.setTimeout,
      clearTimeout: clock.clearTimeout,
    });
    const stopping = coordinator.stop(child);
    if (failedSignal === 'SIGKILL') clock.fire(0);
    await assert.rejects(stopping, new RegExp(`backend did not accept ${failedSignal}`));
    assert.ok(clock.timers.every((timer) => timer.cleared));
    assert.equal(child.listenerCount('exit'), 0);
    assert.equal(child.listenerCount('error'), 0);
  }
});

test('backend process errors reject one stop lifecycle and clean observers', async () => {
  const order = [];
  const child = fakeChild(order);
  const clock = controlledTimers();
  const coordinator = createBackendStopCoordinator({
    setTimeout: clock.setTimeout,
    clearTimeout: clock.clearTimeout,
  });
  const stopping = coordinator.stop(child);
  child.emit('error', new Error('process unavailable'));
  await assert.rejects(stopping, /process unavailable/);
  assert.deepEqual(clock.timers.map((timer) => timer.cleared), [true]);
  assert.equal(child.listenerCount('exit'), 0);
  assert.equal(child.listenerCount('error'), 0);
});

test('quit stop failure keeps the app open with fixed guidance', () => {
  assert.deepEqual(backendStopFailurePrompt(), {
    type: 'error',
    buttons: ['Keep working'],
    defaultId: 0,
    cancelId: 0,
    noLink: true,
    message: 'Unable to stop the local backend',
    detail: 'YHC is still open. Try quitting again after the backend finishes stopping.',
  });
});
