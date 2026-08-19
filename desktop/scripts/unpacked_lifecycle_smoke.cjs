const { spawn } = require('node:child_process');
const fs = require('node:fs');
const os = require('node:os');
const path = require('node:path');
const { pathToFileURL } = require('node:url');

const MAX_DIAGNOSTIC_CHARS = 32 << 10;
const DEVTOOLS_TIMEOUT_MS = 30_000;
const RENDERER_TIMEOUT_MS = 20_000;
const APP_EXIT_TIMEOUT_MS = 15_000;
const BACKEND_EXIT_TIMEOUT_MS = 10_000;
const NO_RESTART_OBSERVATION_MS = 11_000;
const CLEANUP_TIMEOUT_MS = 3_000;
const CDP_COMMAND_TIMEOUT_MS = 5_000;
const POLL_INTERVAL_MS = 100;
const BACKEND_UNAVAILABLE_NOTICE =
  'Backend stopped unexpectedly. Restart YHC to reconnect.';

function appendBounded(current, chunk, maximum = MAX_DIAGNOSTIC_CHARS) {
  if (!Number.isSafeInteger(maximum) || maximum <= 0) {
    throw new TypeError('positive diagnostic limit required');
  }
  return `${current}${Buffer.from(chunk).toString('utf8')}`.slice(-maximum);
}

function parseDevToolsEndpoint(output) {
  const matches = [...String(output).matchAll(/DevTools listening on (\S+)/g)];
  if (matches.length === 0) return null;
  const endpoints = new Set(matches.map((match) => match[1]));
  if (endpoints.size !== 1) throw new Error('ambiguous DevTools endpoint');
  const endpoint = [...endpoints][0];
  let parsed;
  try {
    parsed = new URL(endpoint);
  } catch {
    throw new Error('invalid loopback DevTools endpoint');
  }
  if (
    parsed.protocol !== 'ws:' ||
    parsed.hostname !== '127.0.0.1' ||
    !/^[0-9]+$/.test(parsed.port) ||
    Number(parsed.port) < 1 ||
    Number(parsed.port) > 65535 ||
    !parsed.pathname.startsWith('/devtools/browser/') ||
    parsed.username ||
    parsed.password ||
    parsed.search ||
    parsed.hash
  ) {
    throw new Error('invalid loopback DevTools endpoint');
  }
  return parsed.href;
}

function selectPageTarget(targets, expectedURL) {
  if (!Array.isArray(targets) || typeof expectedURL !== 'string' || expectedURL.length === 0) {
    throw new TypeError('bounded target list and URL required');
  }
  const matches = targets.filter((target) => (
    target?.type === 'page' && target.url === expectedURL
  ));
  if (matches.length > 1) throw new Error('ambiguous packaged renderer target');
  return matches[0] || null;
}

function parseProcStat(contents) {
  const text = String(contents).trim();
  const open = text.indexOf(' (');
  const close = text.lastIndexOf(') ');
  if (open < 1 || close <= open + 2) throw new Error('invalid Linux process stat');
  const pid = Number(text.slice(0, open));
  const fields = text.slice(close + 2).trim().split(/\s+/);
  const state = fields[0];
  const startTime = fields[19];
  if (
    !Number.isSafeInteger(pid) || pid <= 1 ||
    !/^[A-Za-z]$/.test(state || '') ||
    !/^[0-9]+$/.test(startTime || '')
  ) {
    throw new Error('invalid Linux process stat');
  }
  return { pid, state, startTime };
}

function safeProcessGroupID(pid) {
  if (!Number.isSafeInteger(pid) || pid <= 1) throw new Error('unsafe process group');
  return -pid;
}

function delay(milliseconds) {
  return new Promise((resolve) => setTimeout(resolve, milliseconds));
}

async function deadline(promise, milliseconds, label) {
  let timer;
  try {
    return await Promise.race([
      promise,
      new Promise((_, reject) => {
        timer = setTimeout(() => reject(new Error(`${label} timed out`)), milliseconds);
      }),
    ]);
  } finally {
    clearTimeout(timer);
  }
}

async function poll(probe, milliseconds, label) {
  const expires = Date.now() + milliseconds;
  let lastError;
  while (Date.now() < expires) {
    try {
      const result = await probe();
      if (result !== null && result !== undefined && result !== false) return result;
    } catch (error) {
      lastError = error;
    }
    await delay(POLL_INTERVAL_MS);
  }
  throw new Error(`${label} timed out${lastError ? `: ${lastError.message}` : ''}`);
}

async function observeStable(
  probe,
  milliseconds,
  label,
  { now = Date.now, wait = delay } = {},
) {
  if (!Number.isSafeInteger(milliseconds) || milliseconds <= 0) {
    throw new TypeError('positive observation window required');
  }
  const expires = now() + milliseconds;
  let samples = 0;
  while (now() < expires) {
    if (await probe() !== true) throw new Error(`${label} changed during observation`);
    samples += 1;
    const remaining = expires - now();
    if (remaining > 0) await wait(Math.min(POLL_INTERVAL_MS, remaining));
  }
  if (samples === 0) throw new Error(`${label} was not observed`);
  return samples;
}

function readLinuxProcessIdentity(pid) {
  if (!Number.isSafeInteger(pid) || pid <= 1) {
    throw new Error('invalid backend process ID');
  }
  const parsed = parseProcStat(fs.readFileSync(`/proc/${pid}/stat`, 'utf8'));
  const executable = fs.realpathSync(`/proc/${pid}/exe`);
  return { ...parsed, executable };
}

function originalProcessAlive(identity) {
  try {
    const current = parseProcStat(fs.readFileSync(`/proc/${identity.pid}/stat`, 'utf8'));
    return current.startTime === identity.startTime && current.state !== 'Z';
  } catch (error) {
    if (error?.code === 'ENOENT') return false;
    throw error;
  }
}

function killVerifiedBackend(
  identity,
  {
    readIdentity = readLinuxProcessIdentity,
    signalProcess = process.kill,
  } = {},
) {
  const current = readIdentity(identity?.pid);
  if (
    current.pid !== identity?.pid ||
    current.startTime !== identity?.startTime ||
    current.executable !== identity?.executable ||
    current.state === 'Z'
  ) {
    throw new Error('backend process identity changed before crash injection');
  }
  signalProcess(identity.pid, 'SIGKILL');
}

function crashContainmentMatches(result) {
  return result?.bootstrapReady === true &&
    result.notificationCount === 1 &&
    result.backendUnavailable === true &&
    result.notice === BACKEND_UNAVAILABLE_NOTICE &&
    result.status === 'Offline' &&
    result.checkedControlsDisabled === true;
}

class CDPConnection {
  constructor(socket) {
    this.socket = socket;
    this.nextID = 1;
    this.pending = new Map();
    socket.addEventListener('message', (event) => this.handleMessage(event.data));
    socket.addEventListener('close', () => this.rejectPending(new Error('CDP connection closed')));
    socket.addEventListener('error', () => this.rejectPending(new Error('CDP connection failed')));
  }

  static async open(endpoint) {
    if (typeof WebSocket !== 'function') throw new Error('Node WebSocket support is required');
    const socket = new WebSocket(endpoint);
    await deadline(new Promise((resolve, reject) => {
      socket.addEventListener('open', resolve, { once: true });
      socket.addEventListener('error', () => reject(new Error('CDP connection failed')), {
        once: true,
      });
    }), 5_000, 'CDP connection');
    return new CDPConnection(socket);
  }

  handleMessage(data) {
    let message;
    try {
      message = JSON.parse(typeof data === 'string' ? data : Buffer.from(data).toString('utf8'));
    } catch {
      this.rejectPending(new Error('invalid CDP response'));
      return;
    }
    if (!Number.isSafeInteger(message.id)) return;
    const pending = this.pending.get(message.id);
    if (!pending) return;
    this.pending.delete(message.id);
    clearTimeout(pending.timer);
    if (message.error) {
      pending.reject(new Error(`CDP ${pending.method} failed: ${message.error.message}`));
      return;
    }
    pending.resolve(message.result || {});
  }

  rejectPending(error) {
    for (const pending of this.pending.values()) {
      clearTimeout(pending.timer);
      pending.reject(error);
    }
    this.pending.clear();
  }

  send(method, params = {}, sessionId) {
    if (this.socket.readyState !== WebSocket.OPEN) {
      return Promise.reject(new Error('CDP connection is not open'));
    }
    const id = this.nextID++;
    const message = { id, method, params };
    if (sessionId) message.sessionId = sessionId;
    return new Promise((resolve, reject) => {
      const timer = setTimeout(() => {
        this.pending.delete(id);
        reject(new Error(`CDP ${method} timed out`));
      }, CDP_COMMAND_TIMEOUT_MS);
      this.pending.set(id, { method, resolve, reject, timer });
      try {
        this.socket.send(JSON.stringify(message));
      } catch (error) {
        clearTimeout(timer);
        this.pending.delete(id);
        reject(error);
      }
    });
  }

  close() {
    if (this.socket.readyState === WebSocket.OPEN) this.socket.close();
  }
}

async function evaluate(connection, sessionId, expression) {
  const response = await connection.send('Runtime.evaluate', {
    expression,
    awaitPromise: true,
    returnByValue: true,
  }, sessionId);
  if (response.exceptionDetails) throw new Error('renderer evaluation failed');
  return response.result?.value;
}

function requireExecutable(candidate, label) {
  const info = fs.lstatSync(candidate);
  if (!info.isFile() || info.isSymbolicLink() || (info.mode & 0o111) === 0) {
    throw new Error(`${label} is not an executable regular file`);
  }
  return fs.realpathSync(candidate);
}

function findCommand(name, searchPath) {
  for (const directory of String(searchPath || '').split(path.delimiter)) {
    if (!directory) continue;
    const candidate = path.join(directory, name);
    try {
      fs.accessSync(candidate, fs.constants.X_OK);
      if (!fs.statSync(candidate).isFile()) continue;
      return fs.realpathSync(candidate);
    } catch {
      // Continue to the next bounded PATH entry.
    }
  }
  throw new Error(`${name} is required`);
}

function makeIsolatedEnvironment(root, sourceEnvironment = process.env) {
  const environment = {};
  for (const name of ['PATH', 'LANG', 'LC_ALL', 'TZ']) {
    if (
      typeof sourceEnvironment[name] === 'string' &&
      sourceEnvironment[name].length > 0
    ) {
      environment[name] = sourceEnvironment[name];
    }
  }
  if (!environment.PATH) throw new Error('PATH is required');
  for (const directory of ['home', 'tmp', 'config', 'cache', 'data', 'runtime']) {
    const target = path.join(root, directory);
    fs.mkdirSync(target, { recursive: true, mode: 0o700 });
    fs.chmodSync(target, 0o700);
  }
  return {
    ...environment,
    HOME: path.join(root, 'home'),
    TMPDIR: path.join(root, 'tmp'),
    XDG_CONFIG_HOME: path.join(root, 'config'),
    XDG_CACHE_HOME: path.join(root, 'cache'),
    XDG_DATA_HOME: path.join(root, 'data'),
    XDG_RUNTIME_DIR: path.join(root, 'runtime'),
    NO_AT_BRIDGE: '1',
  };
}

function waitForChildExit(child) {
  return new Promise((resolve, reject) => {
    child.once('error', reject);
    child.once('exit', (code, signal) => resolve({ code, signal }));
  });
}

function observeSpawnedChild(child) {
  const exit = waitForChildExit(child);
  // A spawn failure is emitted asynchronously. Attach a rejection handler before
  // validating the PID so an invalid or missing PID cannot leave an unhandled
  // child error behind while the caller unwinds.
  void exit.catch(() => {});
  safeProcessGroupID(child.pid);
  return exit;
}

function processGroupAlive(pid) {
  try {
    process.kill(safeProcessGroupID(pid), 0);
    return true;
  } catch (error) {
    if (error?.code === 'ESRCH') return false;
    throw error;
  }
}

async function terminateProcessGroup(pid) {
  const group = safeProcessGroupID(pid);
  if (!processGroupAlive(pid)) return;
  try {
    process.kill(group, 'SIGTERM');
  } catch (error) {
    if (error?.code !== 'ESRCH') throw error;
  }
  try {
    await poll(() => !processGroupAlive(pid), CLEANUP_TIMEOUT_MS, 'process group cleanup');
    return;
  } catch {
    // Escalate only the isolated process group created by this smoke.
  }
  try {
    process.kill(group, 'SIGKILL');
  } catch (error) {
    if (error?.code !== 'ESRCH') throw error;
  }
  await poll(() => !processGroupAlive(pid), CLEANUP_TIMEOUT_MS, 'process group kill');
}

function parseArguments(argv) {
  let appPath;
  let crashBackend = false;
  let disableSandbox = false;
  let invalid = false;
  for (let index = 0; index < argv.length; index += 1) {
    const argument = argv[index];
    if (argument === '--app') {
      const candidate = argv[index + 1];
      if (appPath || !candidate || candidate.startsWith('--')) {
        invalid = true;
        break;
      }
      appPath = candidate;
      index += 1;
    } else if (argument === '--crash-backend' && !crashBackend) {
      crashBackend = true;
    } else if (argument === '--no-sandbox' && !disableSandbox) {
      disableSandbox = true;
    } else {
      invalid = true;
      break;
    }
  }
  if (invalid || !appPath) {
    throw new Error(
      'usage: unpacked_lifecycle_smoke.cjs --app PATH [--crash-backend] [--no-sandbox]',
    );
  }
  return {
    appPath: path.resolve(appPath),
    crashBackend,
    disableSandbox,
  };
}

async function runSmoke(
  appCandidate,
  { crashBackend = false, disableSandbox = false } = {},
) {
  if (process.platform !== 'linux' || process.arch !== 'x64') {
    throw new Error('unpacked lifecycle smoke requires Linux x64');
  }
  const appPath = requireExecutable(appCandidate, 'unpacked Desktop application');
  const unpackedRoot = path.dirname(appPath);
  const expectedRendererURL = pathToFileURL(
    path.join(unpackedRoot, 'resources', 'webui', 'index.html'),
  ).href;
  const expectedBackend = requireExecutable(
    path.join(unpackedRoot, 'resources', 'bin', 'yhc'),
    'packaged backend',
  );
  const temporaryRoot = fs.mkdtempSync(path.join(os.tmpdir(), 'yhc-desktop-smoke-'));
  fs.chmodSync(temporaryRoot, 0o700);
  const environment = makeIsolatedEnvironment(temporaryRoot);
  const xvfbRun = findCommand('xvfb-run', environment.PATH);
  let stderr = '';
  let connection;
  let child;
  let passed = false;
  try {
    const appArguments = [
      appPath,
      ...(disableSandbox ? ['--no-sandbox'] : []),
      '--disable-gpu',
      '--remote-debugging-address=127.0.0.1',
      '--remote-debugging-port=0',
      `--user-data-dir=${path.join(temporaryRoot, 'profile')}`,
    ];
    child = spawn(xvfbRun, [
      '-a',
      '-s',
      '-screen 0 1440x920x24 -nolisten tcp',
      ...appArguments,
    ], {
      detached: true,
      env: environment,
      stdio: ['ignore', 'ignore', 'pipe'],
    });
    const exit = observeSpawnedChild(child);
    const endpoint = await deadline(new Promise((resolve, reject) => {
      child.stderr.on('data', (chunk) => {
        stderr = appendBounded(stderr, chunk);
        try {
          const discovered = parseDevToolsEndpoint(stderr);
          if (discovered) resolve(discovered);
        } catch (error) {
          reject(error);
        }
      });
      exit.then(({ code, signal }) => {
        reject(new Error(`Desktop exited before DevTools discovery (${code ?? signal})`));
      }, reject);
    }), DEVTOOLS_TIMEOUT_MS, 'DevTools discovery');

    connection = await CDPConnection.open(endpoint);
    const target = await poll(async () => {
      const result = await connection.send('Target.getTargets');
      return selectPageTarget(result.targetInfos, expectedRendererURL);
    }, RENDERER_TIMEOUT_MS, 'packaged renderer discovery');
    const targetId = target.targetId || target.id;
    if (typeof targetId !== 'string' || targetId.length === 0) {
      throw new Error('packaged renderer target lacks identity');
    }
    const attached = await connection.send('Target.attachToTarget', {
      targetId,
      flatten: true,
    });
    if (typeof attached.sessionId !== 'string' || attached.sessionId.length === 0) {
      throw new Error('packaged renderer attachment failed');
    }
    await connection.send('Runtime.enable', {}, attached.sessionId);
    const probe = await poll(async () => {
      const result = await evaluate(connection, attached.sessionId, `(async () => {
        const marker = document.documentElement.dataset.yhcBootstrap || '';
        if (marker === 'error') {
          return { error: document.querySelector('#turn-state')?.textContent || 'bootstrap failed' };
        }
        const bridge = globalThis.yhcDesktop;
        if (marker !== 'ready' || typeof bridge?.getInfo !== 'function') return null;
        const info = await bridge.getInfo();
        return {
          bridgeFrozen: Object.isFrozen(bridge),
          noNodeEscape: typeof globalThis.require === 'undefined' &&
            typeof globalThis.process === 'undefined',
          protocolVersion: info?.protocolVersion,
          surface: info?.surface,
          backendPID: info?.pid,
          webAvailable: info?.webAvailable,
          rendererURL: location.href,
          title: document.title,
          newSessionEnabled: document.querySelector('#new-session')?.disabled === false,
          requiredDOM: Boolean(
            document.querySelector('#session-title') &&
            document.querySelector('#composer') &&
            document.querySelector('#turn-state')
          ),
        };
      })()`);
      if (result?.error) throw new Error(`renderer bootstrap failed: ${result.error}`);
      return result;
    }, RENDERER_TIMEOUT_MS, 'renderer bootstrap');
    if (
      probe.bridgeFrozen !== true ||
      probe.noNodeEscape !== true ||
      probe.protocolVersion !== 2 ||
      probe.surface !== 'desktop' ||
      !Number.isSafeInteger(probe.backendPID) ||
      probe.backendPID <= 1 ||
      probe.webAvailable !== true ||
      probe.rendererURL !== expectedRendererURL ||
      probe.title !== 'YHC' ||
      probe.newSessionEnabled !== true ||
      probe.requiredDOM !== true
    ) {
      throw new Error('packaged renderer contract did not match');
    }

    const backend = readLinuxProcessIdentity(probe.backendPID);
    if (backend.state === 'Z' || backend.executable !== expectedBackend) {
      throw new Error('packaged backend process identity did not match');
    }
    if (crashBackend) {
      const subscribed = await evaluate(connection, attached.sessionId, `(() => {
        const bridge = globalThis.yhcDesktop;
        if (typeof bridge?.onBackendExit !== 'function') return false;
        globalThis.__yhcCrashCount = 0;
        globalThis.__yhcCrashUnsubscribe = bridge.onBackendExit(() => {
          globalThis.__yhcCrashCount += 1;
        });
        return typeof globalThis.__yhcCrashUnsubscribe === 'function';
      })()`);
      if (subscribed !== true) throw new Error('backend crash observer was not installed');

      killVerifiedBackend(backend);
      await poll(
        () => !originalProcessAlive(backend),
        BACKEND_EXIT_TIMEOUT_MS,
        'crashed backend exit',
      );
      const readCrashContainment = () => evaluate(
        connection,
        attached.sessionId,
        `(async () => {
          const bridge = globalThis.yhcDesktop;
          const info = typeof bridge?.getInfo === 'function'
            ? await bridge.getInfo()
            : undefined;
          const controls = [
            '#new-session',
            '#prompt',
            '#send',
            '#cancel',
            '#open-web',
            '#provider-settings',
          ];
          return {
            bootstrapReady: document.documentElement.dataset.yhcBootstrap === 'ready',
            notificationCount: globalThis.__yhcCrashCount,
            backendUnavailable: info === null,
            notice: document.querySelector('#turn-state')?.textContent?.trim() || '',
            status: document.querySelector('#status')?.textContent?.trim() || '',
            checkedControlsDisabled: controls.every((selector) => (
              document.querySelector(selector)?.disabled === true
            )),
          };
        })()`,
      );
      await poll(async () => {
        const result = await readCrashContainment();
        return crashContainmentMatches(result) ? result : null;
      }, RENDERER_TIMEOUT_MS, 'backend crash containment');
      await observeStable(
        async () => crashContainmentMatches(await readCrashContainment()),
        NO_RESTART_OBSERVATION_MS,
        'backend crash containment',
      );
      await evaluate(connection, attached.sessionId, `(() => {
        globalThis.__yhcCrashUnsubscribe?.();
        delete globalThis.__yhcCrashUnsubscribe;
        return true;
      })()`);
    }
    try {
      await evaluate(connection, attached.sessionId, 'globalThis.close(); true');
    } catch (error) {
      if (!/CDP connection (?:closed|is not open)/.test(error.message)) throw error;
    }
    connection.close();
    connection = null;

    const appExit = await deadline(exit, APP_EXIT_TIMEOUT_MS, 'Desktop exit');
    if (appExit.code !== 0 || appExit.signal !== null) {
      throw new Error(`Desktop exited abnormally (${appExit.code ?? appExit.signal})`);
    }
    if (!crashBackend) {
      await poll(
        () => !originalProcessAlive(backend),
        BACKEND_EXIT_TIMEOUT_MS,
        'packaged backend exit',
      );
    }
    passed = true;
    process.stdout.write(`${JSON.stringify({
      status: 'pass',
      protocol_version: probe.protocolVersion,
      surface: probe.surface,
      backend_pid: probe.backendPID,
      crash_containment: crashBackend ? 'pass' : 'not_run',
      no_restart_observation_ms: crashBackend ? NO_RESTART_OBSERVATION_MS : 0,
    })}\n`);
  } catch (error) {
    const diagnostic = stderr.trim();
    throw new Error(
      `${error.message}${diagnostic ? `\nElectron stderr tail:\n${diagnostic}` : ''}`,
    );
  } finally {
    connection?.close();
    try {
      if (!passed && child?.pid) await terminateProcessGroup(child.pid);
    } finally {
      fs.rmSync(temporaryRoot, { recursive: true, force: true });
    }
  }
}

async function main() {
  const input = parseArguments(process.argv.slice(2));
  await runSmoke(input.appPath, {
    crashBackend: input.crashBackend,
    disableSandbox: input.disableSandbox,
  });
}

if (require.main === module) {
  main().catch((error) => {
    process.stderr.write(`unpacked lifecycle smoke failed: ${error.message}\n`);
    process.exitCode = 1;
  });
}

module.exports = {
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
};
