const { app, BrowserWindow, dialog, ipcMain, safeStorage, shell } = require('electron');
const { spawn } = require('node:child_process');
const fs = require('node:fs');
const path = require('node:path');
const {
  activeTurnQuitPrompt,
  activeTurnSessions,
  quitInspectionFailurePrompt,
} = require('./lifecycle.cjs');
const {
  ambientProviderConfigured,
  createProviderRestartCoordinator,
  encryptionUsable,
  providerLaunchEnvironment,
  providerProfileStatus,
  readProviderLaunchProfile,
  writeProviderProfile,
} = require('./provider_setup.cjs');
const { desktopOperation, requireSessionID } = require('./request.cjs');

const BOOTSTRAP_TIMEOUT_MS = 10_000;
const STOP_TIMEOUT_MS = 3_000;
const MAX_BOOTSTRAP_BYTES = 1 << 20;
const PROVIDER_PROFILE_NAME = 'provider-profile.v1.json';

let backend = null;
let bootstrap = null;
let mainWindow = null;
let managedProviderStatus = Object.freeze({ configured: false });
let providerRestartCoordinator = null;
let quitAllowed = false;
let quitDecision = null;
const eventStreams = new Map();
const observedSenders = new Set();
const stoppingBackends = new WeakSet();

function executableName() {
  return process.platform === 'win32' ? 'yhc.exe' : 'yhc';
}

function resolveBackend() {
  const name = executableName();
  const candidates = [
    process.env.YHC_BIN,
    path.join(process.resourcesPath || '', 'bin', name),
    path.join(__dirname, 'resources', 'bin', name),
  ].filter(Boolean);
  for (const candidate of candidates) {
    if (fs.existsSync(candidate)) return candidate;
  }
  return name;
}

function notifyBackendExit(payload) {
  if (mainWindow && !mainWindow.isDestroyed()) {
    mainWindow.webContents.send('app:backend-exit', payload);
  }
}

function stopAllEventStreams() {
  for (const entry of eventStreams.values()) entry.controller.abort();
  eventStreams.clear();
}

function providerProfileDependencies() {
  return {
    platform: process.platform,
    profilePath: path.join(app.getPath('userData'), PROVIDER_PROFILE_NAME),
    safeStorage,
  };
}

function publicProviderStatus(status, errorCode, ambientReady = false) {
  const configured = status?.configured === true;
  return Object.freeze({
    configured,
    launchReady: configured || ambientReady,
    secureStorageAvailable: encryptionUsable(safeStorage, process.platform),
    ...(configured ? {
      provider: status.provider,
      model: status.model,
      ...(status.baseURL ? { baseURL: status.baseURL } : {}),
    } : {}),
    ...(errorCode ? { errorCode } : {}),
  });
}

async function backendLaunchEnvironment() {
  const deps = providerProfileDependencies();
  const status = await providerProfileStatus(deps);
  if (!status.configured) {
    managedProviderStatus = publicProviderStatus(
      status,
      status.errorCode,
      ambientProviderConfigured(process.env),
    );
    return { ...process.env };
  }
  try {
    const profile = await readProviderLaunchProfile(deps);
    managedProviderStatus = publicProviderStatus(status);
    return providerLaunchEnvironment(process.env, profile);
  } catch {
    managedProviderStatus = publicProviderStatus(
      { configured: false },
      'stored_profile_unavailable',
      ambientProviderConfigured(process.env),
    );
    return { ...process.env };
  }
}

async function startBackend() {
  const launchEnvironment = await backendLaunchEnvironment();
  return new Promise((resolve, reject) => {
    const child = spawn(resolveBackend(), ['serve', 'app', '--web'], {
      env: launchEnvironment,
      stdio: ['ignore', 'pipe', 'pipe'],
      windowsHide: true,
    });
    backend = child;
    let settled = false;
    let output = '';

    const settle = (error, value) => {
      if (settled) return;
      settled = true;
      clearTimeout(timeout);
      if (error) reject(error);
      else resolve(value);
    };
    const timeout = setTimeout(() => {
      child.kill('SIGKILL');
      settle(new Error('backend bootstrap timed out'));
    }, BOOTSTRAP_TIMEOUT_MS);

    child.on('error', () => {
      if (!settled) {
        settle(new Error('backend process could not be started'));
        return;
      }
      if (backend === child && !stoppingBackends.has(child)) {
        notifyBackendExit({ code: null, signal: null, error: 'backend process failed' });
      }
    });
    child.once('exit', (code, signal) => {
      const owned = backend === child;
      if (owned) {
        backend = null;
        bootstrap = null;
        stopAllEventStreams();
      }
      if (!settled) {
        settle(new Error('backend exited before completing startup'));
      } else if (owned && !stoppingBackends.has(child)) {
        notifyBackendExit({ code, signal, error: 'backend stopped unexpectedly' });
      }
      stoppingBackends.delete(child);
    });
    child.stderr.resume();
    child.stdout.on('data', (chunk) => {
      if (settled) return;
      output += chunk.toString('utf8');
      if (output.length > MAX_BOOTSTRAP_BYTES) {
        child.kill('SIGKILL');
        settle(new Error('backend bootstrap exceeded the size limit'));
        return;
      }
      const newline = output.indexOf('\n');
      if (newline < 0) return;
      try {
        const value = JSON.parse(output.slice(0, newline));
        if (
          !value ||
          typeof value.url !== 'string' ||
          typeof value.token !== 'string' ||
          value.protocol_version !== 2
        ) {
          throw new Error('invalid backend bootstrap');
        }
        bootstrap = Object.freeze({
          url: value.url,
          token: value.token,
          webURL: typeof value.web_url === 'string'
            ? value.web_url
            : (typeof value.webURL === 'string' ? value.webURL : ''),
          protocolVersion: value.protocol_version,
          pid: value.pid,
        });
        settle(null, bootstrap);
      } catch (error) {
        child.kill('SIGKILL');
        settle(error);
      }
    });
  });
}

function stopBackend({ requireExit = false } = {}) {
  const child = backend;
  if (!child || child.exitCode !== null) return Promise.resolve();
  stoppingBackends.add(child);
  stopAllEventStreams();
  return new Promise((resolve, reject) => {
    let finished = false;
    const finish = (error) => {
      if (finished) return;
      finished = true;
      clearTimeout(forceTimer);
      clearTimeout(giveUpTimer);
      if (error) reject(error);
      else resolve();
    };
    const forceTimer = setTimeout(() => {
      if (child.exitCode === null) child.kill('SIGKILL');
    }, STOP_TIMEOUT_MS);
    const giveUpTimer = setTimeout(() => {
      finish(requireExit ? new Error('backend did not stop in time') : null);
    }, STOP_TIMEOUT_MS * 2);
    forceTimer.unref();
    giveUpTimer.unref();
    child.once('exit', () => finish());
    child.kill('SIGINT');
  });
}

function getProviderRestartCoordinator() {
  if (providerRestartCoordinator) return providerRestartCoordinator;
  providerRestartCoordinator = createProviderRestartCoordinator({
    inspectSessions: () => operationRequest('listSessions'),
    persistProfile: async (submission) => {
      const status = await writeProviderProfile(providerProfileDependencies(), submission);
      managedProviderStatus = publicProviderStatus(status);
      return status;
    },
    stopEventStreams: () => stopAllEventStreams(),
    stopBackend: () => stopBackend({ requireExit: true }),
    startBackend,
  });
  return providerRestartCoordinator;
}

function showQuitMessageBox(options) {
  if (mainWindow && !mainWindow.isDestroyed()) {
    return dialog.showMessageBox(mainWindow, options);
  }
  return dialog.showMessageBox(options);
}

async function requestQuit() {
  if (quitAllowed || quitDecision) return;
  quitDecision = (async () => {
    if (!backend || !bootstrap) {
      quitAllowed = true;
      app.quit();
      return;
    }
    let active = [];
    let inspectionFailed = false;
    try {
      active = activeTurnSessions(await operationRequest('listSessions'));
    } catch {
      inspectionFailed = true;
    }
    if (inspectionFailed) {
      const choice = await showQuitMessageBox(quitInspectionFailurePrompt());
      if (choice.response !== 1) return;
    }
    if (!inspectionFailed && active.length > 0) {
      const choice = await showQuitMessageBox(activeTurnQuitPrompt(active.length));
      if (choice.response !== 1) return;
    }
    await stopBackend();
    quitAllowed = true;
    app.quit();
  })().finally(() => {
    quitDecision = null;
  });
  await quitDecision;
}

function createWindow() {
  mainWindow = new BrowserWindow({
    title: 'YHC',
    width: 1440,
    height: 920,
    minWidth: 700,
    minHeight: 560,
    show: false,
    backgroundColor: '#11131a',
    webPreferences: {
      preload: path.join(__dirname, 'preload.cjs'),
      contextIsolation: true,
      sandbox: true,
      nodeIntegration: false,
      webSecurity: true,
      devTools: !app.isPackaged,
    },
  });
  mainWindow.webContents.setWindowOpenHandler(() => ({ action: 'deny' }));
  mainWindow.webContents.on('will-navigate', (event) => event.preventDefault());
  const renderer = app.isPackaged
    ? path.join(process.resourcesPath, 'webui', 'index.html')
    : path.join(__dirname, '..', 'internal', 'webui', 'assets', 'index.html');
  mainWindow.loadFile(renderer);
  mainWindow.once('ready-to-show', () => mainWindow.show());
}

function assertTrustedSender(event) {
  if (!mainWindow || mainWindow.isDestroyed() || event.sender !== mainWindow.webContents) {
    throw new Error('untrusted desktop IPC sender');
  }
}

async function backendRequest(pathname, method = 'GET', body) {
  if (!bootstrap) throw new Error('backend is unavailable');
  const headers = { Authorization: `Bearer ${bootstrap.token}` };
  if (body !== undefined) headers['Content-Type'] = 'application/json';
  const response = await fetch(`${bootstrap.url}${pathname}`, {
    method,
    headers,
    body: body === undefined ? undefined : JSON.stringify(body),
  });
  if (!response.ok) {
    const payload = await response.json().catch(() => ({}));
    throw new Error(payload.error?.message || `backend request failed (${response.status})`);
  }
  return response.status === 204 ? null : response.json();
}

function operationRequest(operation, payload = {}) {
  const [pathname, method, body] = desktopOperation(operation, payload);
  return backendRequest(pathname, method, body);
}

function eventStreamKey(sender, sessionID) {
  return `${sender.id}:${sessionID}`;
}

function observeSender(sender) {
  if (observedSenders.has(sender.id)) return;
  observedSenders.add(sender.id);
  sender.once('destroyed', () => {
    observedSenders.delete(sender.id);
    const prefix = `${sender.id}:`;
    for (const [key, entry] of eventStreams) {
      if (!key.startsWith(prefix)) continue;
      entry.controller.abort();
      eventStreams.delete(key);
    }
  });
}

async function startEventStream(sender, payload) {
  const sessionID = requireSessionID(payload.sessionID);
  const after = Number(payload.after || 0);
  if (!Number.isSafeInteger(after) || after < 0) {
    throw new TypeError('valid event cursor required');
  }
  if (!bootstrap) throw new Error('backend is unavailable');
  observeSender(sender);
  const key = eventStreamKey(sender, sessionID);
  eventStreams.get(key)?.controller.abort();
  const entry = { controller: new AbortController() };
  eventStreams.set(key, entry);
  let response;
  try {
    response = await fetch(
      `${bootstrap.url}/v1/sessions/${encodeURIComponent(sessionID)}/events?after=${after}`,
      {
        headers: { Authorization: `Bearer ${bootstrap.token}` },
        signal: entry.controller.signal,
      },
    );
  } catch (error) {
    if (eventStreams.get(key) === entry) eventStreams.delete(key);
    throw error;
  }
  if (response.status === 409) {
    const gap = await response.json().catch(() => ({}));
    if (eventStreams.get(key) === entry) eventStreams.delete(key);
    return { status: 'gap', gap };
  }
  if (!response.ok || !response.body) {
    if (eventStreams.get(key) === entry) eventStreams.delete(key);
    throw new Error(`event stream unavailable (${response.status})`);
  }
  void pumpEventStream(sender, sessionID, key, entry, response.body);
  return { status: 'open' };
}

async function pumpEventStream(sender, sessionID, key, entry, body) {
  const reader = body.getReader();
  const decoder = new TextDecoder();
  let buffered = '';
  let failure = null;
  try {
    while (!entry.controller.signal.aborted) {
      const { value, done } = await reader.read();
      if (done) break;
      buffered += decoder.decode(value, { stream: true });
      buffered = emitSSEFrames(sender, sessionID, buffered);
    }
    buffered += decoder.decode();
    emitSSEFrames(sender, sessionID, buffered);
  } catch (error) {
    if (error.name !== 'AbortError' && !entry.controller.signal.aborted) {
      failure = error.message;
    }
  } finally {
    if (eventStreams.get(key) === entry) {
      eventStreams.delete(key);
      if (!sender.isDestroyed()) {
        sender.send('app:event-stream', {
          kind: failure ? 'error' : 'closed',
          sessionID,
          error: failure,
        });
      }
    }
  }
}

function emitSSEFrames(sender, sessionID, input) {
  let buffered = input;
  for (;;) {
    const boundary = buffered.search(/\r?\n\r?\n/);
    if (boundary < 0) return buffered;
    const separator = buffered.slice(boundary).match(/^\r?\n\r?\n/)[0];
    const frame = buffered.slice(0, boundary);
    buffered = buffered.slice(boundary + separator.length);
    const data = frame
      .split(/\r?\n/)
      .filter((line) => line.startsWith('data:'))
      .map((line) => line.slice(5).trimStart())
      .join('\n');
    if (!data || sender.isDestroyed()) continue;
    try {
      sender.send('app:event-stream', {
        kind: 'event',
        sessionID,
        event: JSON.parse(data),
      });
    } catch (error) {
      sender.send('app:event-stream', {
        kind: 'error',
        sessionID,
        error: `invalid event payload: ${error.message}`,
      });
    }
  }
}

function stopEventStream(sender, sessionID) {
  const key = eventStreamKey(sender, requireSessionID(sessionID));
  const entry = eventStreams.get(key);
  if (!entry) return;
  eventStreams.delete(key);
  entry.controller.abort();
}

ipcMain.handle('app:get-info', (event) => {
  assertTrustedSender(event);
  return bootstrap
    ? {
      protocolVersion: bootstrap.protocolVersion,
      pid: bootstrap.pid,
      surface: 'desktop',
      webAvailable: Boolean(bootstrap.webURL),
    }
    : null;
});
ipcMain.handle('app:provider-status', (event) => {
  assertTrustedSender(event);
  return managedProviderStatus;
});
ipcMain.handle('app:configure-provider', (event, input) => {
  assertTrustedSender(event);
  return getProviderRestartCoordinator().configure(input);
});
ipcMain.handle('app:select-workspace', async (event) => {
  assertTrustedSender(event);
  const result = await dialog.showOpenDialog(mainWindow, {
    properties: ['openDirectory', 'createDirectory'],
  });
  if (result.canceled || !result.filePaths[0]) return null;
  const workspace = await operationRequest('createWorkspace', { cwd: result.filePaths[0] });
  if (
    !workspace ||
    typeof workspace.workspace_handle !== 'string' ||
    typeof workspace.workspace_label !== 'string'
  ) {
    throw new Error('backend returned an invalid workspace selection');
  }
  return Object.freeze({
    workspace_handle: workspace.workspace_handle,
    workspace_label: workspace.workspace_label,
  });
});
ipcMain.handle('app:api', (event, operation, payload) => {
  assertTrustedSender(event);
  if (operation === 'createWorkspace') {
    throw new Error('workspace creation is available only through the native picker');
  }
  return operationRequest(operation, payload);
});
ipcMain.handle('app:events-start', (event, payload) => {
  assertTrustedSender(event);
  return startEventStream(event.sender, payload);
});
ipcMain.handle('app:events-stop', (event, sessionID) => {
  assertTrustedSender(event);
  stopEventStream(event.sender, sessionID);
});
ipcMain.handle('app:open-web', async (event) => {
  assertTrustedSender(event);
  if (!bootstrap?.webURL) return false;
  const pairing = await backendRequest('/v1/auth/browser-pairing', 'POST');
  const target = new URL(pairing?.web_url || '');
  if (
    target.origin !== bootstrap.url ||
    target.pathname !== '/' ||
    !target.hash.startsWith('#pair=')
  ) {
    throw new Error('backend returned an invalid Web launch URL');
  }
  await shell.openExternal(target.toString());
  return true;
});

const hasSingleInstanceLock = app.requestSingleInstanceLock();
if (!hasSingleInstanceLock) {
  app.quit();
} else {
  app.on('second-instance', () => {
    if (!mainWindow) return;
    if (mainWindow.isMinimized()) mainWindow.restore();
    mainWindow.focus();
  });
  app.whenReady().then(async () => {
    try {
      await startBackend();
      if (!bootstrap) throw new Error('backend stopped during startup');
      createWindow();
    } catch {
      await dialog.showMessageBox({
        type: 'error',
        message: 'Unable to start YHC',
        detail: 'The local backend could not be started. Restart the App and try again.',
      });
      quitAllowed = true;
      app.quit();
    }
  });
  app.on('activate', () => {
    if (BrowserWindow.getAllWindows().length === 0 && bootstrap) createWindow();
  });
  app.on('window-all-closed', () => {
    if (process.platform !== 'darwin') app.quit();
  });
  app.on('before-quit', (event) => {
    if (quitAllowed) return;
    event.preventDefault();
    void requestQuit();
  });
}
