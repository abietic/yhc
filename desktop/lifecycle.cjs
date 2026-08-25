const APP_SERVER_SHUTDOWN_BUDGET_MS = 15_000;
const BACKEND_GRACEFUL_STOP_TIMEOUT_MS = APP_SERVER_SHUTDOWN_BUDGET_MS + 2_000;
const BACKEND_FORCE_EXIT_TIMEOUT_MS = 3_000;

function activeTurnSessions(response) {
  if (!response || !Array.isArray(response.sessions)) {
    throw new TypeError('bounded session summaries array required');
  }
  return response.sessions.filter(
    (session) => typeof session?.active_turn_id === 'string' && session.active_turn_id.length > 0,
  );
}

function activeTurnQuitPrompt(count) {
  const singular = count === 1;
  return {
    type: 'warning',
    buttons: ['Keep working', 'Quit and stop turns'],
    defaultId: 0,
    cancelId: 0,
    noLink: true,
    message: `${count} active turn${singular ? '' : 's'} ${singular ? 'is' : 'are'} still running`,
    detail: 'Quitting now stops those turns. Durable transcripts remain resumable.',
  };
}

function quitInspectionFailurePrompt() {
  return {
    type: 'warning',
    buttons: ['Keep working', 'Quit and stop backend'],
    defaultId: 0,
    cancelId: 0,
    noLink: true,
    message: 'Unable to verify active turns',
  };
}

function backendStopFailurePrompt() {
  return {
    type: 'error',
    buttons: ['Keep working'],
    defaultId: 0,
    cancelId: 0,
    noLink: true,
    message: 'Unable to stop the local backend',
    detail: 'YHC is still open. Try quitting again after the backend finishes stopping.',
  };
}

async function startDesktopHost({
  prepareWindow,
  startBackend,
  backendAvailable,
  loadRenderer,
  stopBackend,
} = {}) {
  for (const dependency of [
    prepareWindow,
    startBackend,
    backendAvailable,
    loadRenderer,
    stopBackend,
  ]) {
    if (typeof dependency !== 'function') {
      throw new TypeError('Desktop startup dependencies required');
    }
  }
  const targetWindow = prepareWindow();
  try {
    await startBackend();
    if (backendAvailable() !== true) {
      throw new Error('backend stopped during startup');
    }
    await loadRenderer(targetWindow);
  } catch (startupError) {
    try {
      await stopBackend();
    } catch (cleanupError) {
      throw new AggregateError(
        [startupError, cleanupError],
        'Desktop startup failed and backend cleanup failed',
        { cause: startupError },
      );
    }
    throw startupError;
  }
}

function createWindowRestoreCoordinator({
  currentWindow,
  backendAvailable,
  createWindow,
  loadRenderer,
} = {}) {
  for (const dependency of [
    currentWindow,
    backendAvailable,
    createWindow,
    loadRenderer,
  ]) {
    if (typeof dependency !== 'function') {
      throw new TypeError('Desktop window restoration dependencies required');
    }
  }
  let inFlight = null;

  const focusExistingWindow = (targetWindow) => {
    if (!targetWindow || targetWindow.isDestroyed()) return null;
    try {
      if (targetWindow.isMinimized()) targetWindow.restore();
      targetWindow.focus();
      return targetWindow;
    } catch (error) {
      if (targetWindow.isDestroyed()) return null;
      throw error;
    }
  };

  const restoreWindow = async () => {
    const existingWindow = focusExistingWindow(currentWindow());
    if (existingWindow) return existingWindow;
    if (backendAvailable() !== true) return null;

    const targetWindow = createWindow();
    if (!targetWindow || targetWindow.isDestroyed()) {
      throw new Error('Desktop window is unavailable');
    }
    try {
      await loadRenderer(targetWindow);
    } catch (loadError) {
      try {
        if (currentWindow() === targetWindow && !targetWindow.isDestroyed()) {
          targetWindow.destroy();
        }
      } catch (cleanupError) {
        throw new AggregateError(
          [loadError, cleanupError],
          'Desktop window restoration failed and cleanup failed',
          { cause: loadError },
        );
      }
      throw loadError;
    }
    return targetWindow;
  };

  return Object.freeze({
    restore() {
      if (inFlight) return inFlight;
      const restoring = Promise.resolve().then(restoreWindow);
      const tracked = restoring.finally(() => {
        if (inFlight === tracked) inFlight = null;
      });
      inFlight = tracked;
      return tracked;
    },
  });
}

function stopBackendProcess(child, {
  setTimeout: setTimer = globalThis.setTimeout,
  clearTimeout: clearTimer = globalThis.clearTimeout,
} = {}) {
  if (!child || child.exitCode !== null) return Promise.resolve();
  return new Promise((resolve, reject) => {
    let settled = false;
    let forceTimer;
    let giveUpTimer;

    const cleanup = () => {
      if (forceTimer) clearTimer(forceTimer);
      if (giveUpTimer) clearTimer(giveUpTimer);
      child.removeListener('exit', onExit);
      child.removeListener('error', onError);
    };
    const finish = (error) => {
      if (settled) return;
      settled = true;
      cleanup();
      if (error) reject(error);
      else resolve();
    };
    const onExit = () => finish();
    const onError = (error) => finish(
      error instanceof Error ? error : new Error('backend process failed while stopping'),
    );
    const sendSignal = (signal) => {
      try {
        const sent = child.kill(signal);
        if (!sent && child.exitCode === null) {
          finish(new Error(`backend did not accept ${signal}`));
        }
      } catch (error) {
        finish(error);
      }
    };

    child.once('exit', onExit);
    child.once('error', onError);
    forceTimer = setTimer(() => {
      if (settled || child.exitCode !== null) return;
      sendSignal('SIGKILL');
      if (settled || child.exitCode !== null) return;
      giveUpTimer = setTimer(() => {
        finish(new Error('backend did not stop in time'));
      }, BACKEND_FORCE_EXIT_TIMEOUT_MS);
      giveUpTimer.unref?.();
    }, BACKEND_GRACEFUL_STOP_TIMEOUT_MS);
    forceTimer.unref?.();
    sendSignal('SIGINT');
  });
}

function createBackendStopCoordinator({
  markStopping = () => {},
  unmarkStopping = () => {},
  stopEventStreams = () => {},
  setTimeout,
  clearTimeout,
} = {}) {
  const inFlight = new WeakMap();
  return Object.freeze({
    stop(child) {
      if (!child || child.exitCode !== null) return Promise.resolve();
      const existing = inFlight.get(child);
      if (existing) return existing;
      let marked = false;
      let stopping;
      try {
        markStopping(child);
        marked = true;
        stopEventStreams();
        stopping = stopBackendProcess(child, { setTimeout, clearTimeout });
      } catch (error) {
        stopping = Promise.reject(error);
      }
      const tracked = stopping.catch((error) => {
        if (marked) unmarkStopping(child);
        throw error;
      }).finally(() => {
        if (inFlight.get(child) === tracked) inFlight.delete(child);
      });
      inFlight.set(child, tracked);
      return tracked;
    },
  });
}

module.exports = {
  APP_SERVER_SHUTDOWN_BUDGET_MS,
  BACKEND_FORCE_EXIT_TIMEOUT_MS,
  BACKEND_GRACEFUL_STOP_TIMEOUT_MS,
  activeTurnSessions,
  activeTurnQuitPrompt,
  backendStopFailurePrompt,
  createBackendStopCoordinator,
  createWindowRestoreCoordinator,
  quitInspectionFailurePrompt,
  startDesktopHost,
};
