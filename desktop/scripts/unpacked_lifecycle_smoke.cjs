const { spawn, spawnSync } = require('node:child_process');
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
const PROCESS_QUERY_TIMEOUT_MS = 1_000;
const PROCESS_QUERY_MAX_BUFFER = 4 << 10;
const DARWIN_IOREG_MAX_BUFFER = 256 << 10;
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

function targetID(target) {
  const identity = target?.targetId || target?.id;
  if (typeof identity !== 'string' || identity.length === 0) {
    throw new Error('packaged renderer target lacks identity');
  }
  return identity;
}

function originalPageTargetClosed(targets, expectedURL, originalTargetID) {
  if (!Array.isArray(targets) || typeof originalTargetID !== 'string') {
    throw new TypeError('bounded target list and original target identity required');
  }
  if (targets.some((target) => (target?.targetId || target?.id) === originalTargetID)) {
    return false;
  }
  if (selectPageTarget(targets, expectedURL)) {
    throw new Error('packaged renderer was replaced before the second instance launched');
  }
  return true;
}

function selectReplacementPageTarget(targets, expectedURL, originalTargetID) {
  const replacement = selectPageTarget(targets, expectedURL);
  if (!replacement) return null;
  if (targetID(replacement) === originalTargetID) {
    throw new Error('packaged renderer target identity was reused');
  }
  return replacement;
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

function parseDarwinProcessIdentity(contents) {
  const lines = String(contents)
    .split(/\r?\n/)
    .filter((line) => line.trim().length > 0);
  if (lines.length !== 1) throw new Error('invalid Darwin process identity');
  const line = lines[0];
  if (/[\u0000-\u0008\u000b\u000c\u000e-\u001f\u007f]/.test(line)) {
    throw new Error('invalid Darwin process identity');
  }
  const match = line.match(
    /^\s*([0-9]+)\s+([0-9]+)\s+(\S+)\s+((?:Sun|Mon|Tue|Wed|Thu|Fri|Sat)\s+(?:Jan|Feb|Mar|Apr|May|Jun|Jul|Aug|Sep|Oct|Nov|Dec)\s+[0-9]{1,2}\s+[0-9]{2}:[0-9]{2}:[0-9]{2}\s+[0-9]{4})\s+(.+?)\s*$/,
  );
  if (!match) throw new Error('invalid Darwin process identity');
  const pid = Number(match[1]);
  const pgid = Number(match[2]);
  const state = match[3];
  const startTime = match[4].replace(/\s+/g, ' ');
  const executable = match[5];
  if (
    !Number.isSafeInteger(pid) || pid <= 1 ||
    !Number.isSafeInteger(pgid) || pgid <= 1 ||
    !/^[A-Za-z]/.test(state) ||
    !path.isAbsolute(executable)
  ) {
    throw new Error('invalid Darwin process identity');
  }
  return { pid, pgid, state, startTime, executable };
}

function readDarwinProcessIdentity(
  pid,
  {
    runPS = spawnSync,
    realpath = fs.realpathSync,
  } = {},
) {
  if (!Number.isSafeInteger(pid) || pid <= 1) {
    throw new Error('invalid backend process ID');
  }
  const result = runPS('/bin/ps', [
    '-ww',
    '-p', String(pid),
    '-o', 'pid=',
    '-o', 'pgid=',
    '-o', 'state=',
    '-o', 'lstart=',
    '-o', 'comm=',
  ], {
    encoding: 'utf8',
    env: {
      PATH: '/usr/bin:/bin',
      LANG: 'C',
      LC_ALL: 'C',
      TZ: 'UTC',
    },
    maxBuffer: PROCESS_QUERY_MAX_BUFFER,
    timeout: PROCESS_QUERY_TIMEOUT_MS,
  });
  if (result?.error) {
    throw new Error('Darwin process inspection failed', { cause: result.error });
  }
  const stdout = Buffer.from(result?.stdout || '').toString('utf8');
  const stderr = Buffer.from(result?.stderr || '').toString('utf8');
  if (result?.status === 1 && stdout.trim() === '' && stderr.trim() === '') return null;
  if (result?.status !== 0) throw new Error('Darwin process inspection failed');
  const identity = parseDarwinProcessIdentity(stdout);
  if (identity.pid !== pid) throw new Error('Darwin process ID changed during observation');
  return { ...identity, executable: realpath(identity.executable) };
}

function parseDarwinScreenLockState(contents) {
  const states = new Set(
    [...String(contents).matchAll(
      /"(?:IOConsoleLocked|CGSSessionScreenIsLocked)"\s*=\s*(Yes|No)\b/g,
    )].map((match) => match[1]),
  );
  if (states.size !== 1) return null;
  return states.has('Yes');
}

function readDarwinScreenLockState({ runIOReg = spawnSync } = {}) {
  try {
    const result = runIOReg('/usr/sbin/ioreg', ['-n', 'Root', '-d1'], {
      encoding: 'utf8',
      env: {
        PATH: '/usr/bin:/bin:/usr/sbin',
        LANG: 'C',
        LC_ALL: 'C',
      },
      maxBuffer: DARWIN_IOREG_MAX_BUFFER,
      timeout: PROCESS_QUERY_TIMEOUT_MS,
    });
    if (result?.error || result?.status !== 0) return null;
    return parseDarwinScreenLockState(result.stdout || '');
  } catch {
    return null;
  }
}

function sameProcessIdentity(expected, current) {
  return current !== null &&
    current !== undefined &&
    current.pid === expected?.pid &&
    current.startTime === expected?.startTime &&
    current.executable === expected?.executable &&
    (expected?.pgid === undefined || current.pgid === expected.pgid) &&
    !String(current.state || '').startsWith('Z');
}

function originalProcessAlive(identity, readIdentity = readLinuxProcessIdentity) {
  try {
    return sameProcessIdentity(identity, readIdentity(identity.pid));
  } catch (error) {
    if (error?.code === 'ENOENT' || error?.code === 'ESRCH') return false;
    throw error;
  }
}

function combineSmokeFailure(primary, cleanup) {
  if (!(cleanup instanceof Error)) throw new TypeError('cleanup error required');
  if (!(primary instanceof Error)) {
    return new Error(`Cleanup failed: ${cleanup.message}`, { cause: cleanup });
  }
  return new Error(`${primary.message}\nCleanup failed: ${cleanup.message}`, {
    cause: new AggregateError([primary, cleanup]),
  });
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

async function attachRendererTarget(connection, target) {
  const identity = targetID(target);
  const attached = await connection.send('Target.attachToTarget', {
    targetId: identity,
    flatten: true,
  });
  if (typeof attached.sessionId !== 'string' || attached.sessionId.length === 0) {
    throw new Error('packaged renderer attachment failed');
  }
  await connection.send('Runtime.enable', {}, attached.sessionId);
  return { sessionId: attached.sessionId, targetId: identity };
}

async function waitForRendererBootstrap(connection, sessionId) {
  return poll(async () => {
    const result = await evaluate(connection, sessionId, `(async () => {
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
}

function rendererContractMatches(probe, expectedRendererURL) {
  return probe?.bridgeFrozen === true &&
    probe.noNodeEscape === true &&
    probe.protocolVersion === 2 &&
    probe.surface === 'desktop' &&
    Number.isSafeInteger(probe.backendPID) &&
    probe.backendPID > 1 &&
    probe.webAvailable === true &&
    probe.rendererURL === expectedRendererURL &&
    probe.title === 'YHC' &&
    probe.newSessionEnabled === true &&
    probe.requiredDOM === true;
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

function processGroupCleanupOwnershipConfirmed(pid, expectedIdentity, readIdentity) {
  if (
    !expectedIdentity ||
    typeof readIdentity !== 'function' ||
    expectedIdentity.pid !== pid ||
    expectedIdentity.pgid !== pid
  ) {
    return false;
  }
  try {
    const current = readIdentity(pid);
    return current?.pid === pid &&
      current.pgid === pid &&
      sameProcessIdentity(expectedIdentity, current);
  } catch {
    return false;
  }
}

async function terminateProcessGroup(pid, options = {}) {
  const group = safeProcessGroupID(pid);
  const {
    expectedIdentity,
    readIdentity,
    groupAlive = processGroupAlive,
    signalProcess = process.kill,
    waitFor = poll,
  } = options;
  const guarded = Object.hasOwn(options, 'expectedIdentity') ||
    Object.hasOwn(options, 'readIdentity');
  const verifyOwnership = () => {
    if (!guarded) return;
    if (!processGroupCleanupOwnershipConfirmed(pid, expectedIdentity, readIdentity)) {
      throw new Error('process group cleanup ownership could not be confirmed');
    }
  };
  if (!groupAlive(pid)) return;
  verifyOwnership();
  try {
    signalProcess(group, 'SIGTERM');
  } catch (error) {
    if (error?.code !== 'ESRCH') throw error;
  }
  try {
    await waitFor(() => !groupAlive(pid), CLEANUP_TIMEOUT_MS, 'process group cleanup');
    return;
  } catch {
    // Escalate only the isolated process group created by this smoke.
  }
  if (!groupAlive(pid)) return;
  verifyOwnership();
  try {
    signalProcess(group, 'SIGKILL');
  } catch (error) {
    if (error?.code !== 'ESRCH') throw error;
  }
  await waitFor(() => !groupAlive(pid), CLEANUP_TIMEOUT_MS, 'process group kill');
}

function resolveUnpackedLayout(
  appCandidate,
  { platform = process.platform, arch = process.arch } = {},
) {
  if (typeof appCandidate !== 'string' || appCandidate.length === 0) {
    throw new TypeError('unpacked Desktop application path required');
  }
  const appPath = path.resolve(appCandidate);
  if (platform === 'linux' && arch === 'x64') {
    const resourcesPath = path.join(path.dirname(appPath), 'resources');
    return {
      appPath,
      arch,
      backendPath: path.join(resourcesPath, 'bin', 'yhc'),
      closeStrategy: 'renderer',
      launcher: 'xvfb',
      platform,
      rendererURL: pathToFileURL(path.join(resourcesPath, 'webui', 'index.html')).href,
      resourcesPath,
    };
  }
  if (platform === 'darwin' && (arch === 'arm64' || arch === 'x64')) {
    const macOSPath = path.dirname(appPath);
    const contentsPath = path.dirname(macOSPath);
    const bundlePath = path.dirname(contentsPath);
    if (
      path.basename(appPath) !== 'YHC' ||
      path.basename(macOSPath) !== 'MacOS' ||
      path.basename(contentsPath) !== 'Contents' ||
      path.basename(bundlePath) !== 'YHC.app'
    ) {
      throw new Error('invalid macOS unpacked Desktop application path');
    }
    const resourcesPath = path.join(contentsPath, 'Resources');
    return {
      appPath,
      arch,
      backendPath: path.join(resourcesPath, 'bin', 'yhc'),
      closeStrategy: 'browser',
      launcher: 'direct',
      platform,
      rendererURL: pathToFileURL(path.join(resourcesPath, 'webui', 'index.html')).href,
      resourcesPath,
    };
  }
  if (platform === 'darwin') {
    throw new Error('unpacked lifecycle smoke requires macOS arm64 or x64');
  }
  throw new Error(`unsupported unpacked lifecycle platform ${platform}/${arch}`);
}

function validatePlatformOptions(
  platform,
  { crashBackend = false, disableSandbox = false, reopenWindow = false } = {},
) {
  const normalized = {
    crashBackend: crashBackend === true,
    disableSandbox: disableSandbox === true,
    reopenWindow: reopenWindow === true,
  };
  if (normalized.crashBackend && normalized.reopenWindow) {
    throw new Error('backend crash and window restoration smokes are mutually exclusive');
  }
  if (platform === 'linux') {
    if (normalized.reopenWindow) throw new Error('--reopen-window is macOS-only');
    return normalized;
  }
  if (normalized.crashBackend || normalized.disableSandbox) {
    throw new Error('crash injection and --no-sandbox are Linux-only');
  }
  if (platform !== 'darwin' && normalized.reopenWindow) {
    throw new Error('--reopen-window is macOS-only');
  }
  return normalized;
}

function parseArguments(argv) {
  let appPath;
  let crashBackend = false;
  let disableSandbox = false;
  let reopenWindow = false;
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
    } else if (argument === '--reopen-window' && !reopenWindow) {
      reopenWindow = true;
    } else {
      invalid = true;
      break;
    }
  }
  if (invalid || !appPath || (crashBackend && reopenWindow)) {
    throw new Error(
      'usage: unpacked_lifecycle_smoke.cjs --app PATH [--crash-backend] ' +
        '[--no-sandbox] [--reopen-window]',
    );
  }
  return {
    appPath: path.resolve(appPath),
    crashBackend,
    disableSandbox,
    reopenWindow,
  };
}

async function runSmoke(
  appCandidate,
  { crashBackend = false, disableSandbox = false, reopenWindow = false } = {},
) {
  let layout = resolveUnpackedLayout(appCandidate);
  const options = validatePlatformOptions(layout.platform, {
    crashBackend,
    disableSandbox,
    reopenWindow,
  });
  const appPath = requireExecutable(layout.appPath, 'unpacked Desktop application');
  layout = resolveUnpackedLayout(appPath);
  const expectedRendererURL = layout.rendererURL;
  const expectedBackend = requireExecutable(
    layout.backendPath,
    'packaged backend',
  );
  const readBackendIdentity = layout.platform === 'linux'
    ? readLinuxProcessIdentity
    : readDarwinProcessIdentity;
  const temporaryRoot = fs.mkdtempSync(path.join(os.tmpdir(), 'yhc-desktop-smoke-'));
  fs.chmodSync(temporaryRoot, 0o700);
  const environment = makeIsolatedEnvironment(temporaryRoot);
  const xvfbRun = layout.launcher === 'xvfb'
    ? findCommand('xvfb-run', environment.PATH)
    : null;
  let stderr = '';
  let connection;
  let child;
  let rootIdentity;
  let secondChild;
  let secondRootIdentity;
  let failure;
  let passed = false;
  try {
    const appArguments = [
      ...(options.disableSandbox ? ['--no-sandbox'] : []),
      '--disable-gpu',
      '--remote-debugging-address=127.0.0.1',
      '--remote-debugging-port=0',
      `--user-data-dir=${path.join(temporaryRoot, 'profile')}`,
    ];
    const launchCommand = layout.launcher === 'xvfb' ? xvfbRun : appPath;
    const launchArguments = layout.launcher === 'xvfb'
      ? [
        '-a',
        '-s',
        '-screen 0 1440x920x24 -nolisten tcp',
        appPath,
        ...appArguments,
      ]
      : appArguments;
    child = spawn(launchCommand, launchArguments, {
      detached: true,
      env: environment,
      stdio: ['ignore', 'ignore', 'pipe'],
    });
    const exit = observeSpawnedChild(child);
    if (layout.platform === 'darwin') {
      rootIdentity = await poll(
        () => readDarwinProcessIdentity(child.pid),
        RENDERER_TIMEOUT_MS,
        'Darwin Desktop process identity',
      );
      if (
        rootIdentity.pgid !== child.pid ||
        rootIdentity.executable !== appPath ||
        rootIdentity.state.startsWith('Z')
      ) {
        throw new Error('Darwin Desktop process identity did not match');
      }
    }
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
    let renderer = await attachRendererTarget(connection, target);
    let probe = await waitForRendererBootstrap(connection, renderer.sessionId);
    if (!rendererContractMatches(probe, expectedRendererURL)) {
      throw new Error('packaged renderer contract did not match');
    }

    const backend = readBackendIdentity(probe.backendPID);
    if (
      !backend ||
      backend.state.startsWith('Z') ||
      backend.executable !== expectedBackend ||
      (layout.platform === 'darwin' && backend.pgid !== child.pid)
    ) {
      throw new Error('packaged backend process identity did not match');
    }
    if (options.crashBackend) {
      const subscribed = await evaluate(connection, renderer.sessionId, `(() => {
        const bridge = globalThis.yhcDesktop;
        if (typeof bridge?.onBackendExit !== 'function') return false;
        globalThis.__yhcCrashCount = 0;
        globalThis.__yhcCrashUnsubscribe = bridge.onBackendExit(() => {
          globalThis.__yhcCrashCount += 1;
        });
        return typeof globalThis.__yhcCrashUnsubscribe === 'function';
      })()`);
      if (subscribed !== true) throw new Error('backend crash observer was not installed');

      killVerifiedBackend(backend, { readIdentity: readBackendIdentity });
      await poll(
        () => !originalProcessAlive(backend, readBackendIdentity),
        BACKEND_EXIT_TIMEOUT_MS,
        'crashed backend exit',
      );
      const readCrashContainment = () => evaluate(
        connection,
        renderer.sessionId,
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
      await evaluate(connection, renderer.sessionId, `(() => {
        globalThis.__yhcCrashUnsubscribe?.();
        delete globalThis.__yhcCrashUnsubscribe;
        return true;
      })()`);
    }
    if (options.reopenWindow) {
      const originalRendererTargetID = renderer.targetId;
      await evaluate(connection, renderer.sessionId, 'globalThis.close(); true');
      await poll(async () => {
        const result = await connection.send('Target.getTargets');
        return originalPageTargetClosed(
          result.targetInfos,
          expectedRendererURL,
          originalRendererTargetID,
        );
      }, RENDERER_TIMEOUT_MS, 'original packaged renderer exit');
      if (!originalProcessAlive(rootIdentity, readDarwinProcessIdentity)) {
        throw new Error('Darwin Desktop process changed after its last window closed');
      }
      if (!originalProcessAlive(backend, readBackendIdentity)) {
        throw new Error('packaged backend changed after the last window closed');
      }

      secondChild = spawn(appPath, appArguments, {
        detached: true,
        env: environment,
        stdio: ['ignore', 'ignore', 'pipe'],
      });
      const secondExit = observeSpawnedChild(secondChild);
      secondChild.stderr.on('data', (chunk) => {
        stderr = appendBounded(stderr, chunk);
      });
      secondRootIdentity = readDarwinProcessIdentity(secondChild.pid);
      if (secondRootIdentity && (
        secondRootIdentity.pgid !== secondChild.pid ||
        secondRootIdentity.executable !== appPath ||
        secondRootIdentity.state.startsWith('Z')
      )) {
        throw new Error('second Darwin Desktop process identity did not match');
      }
      const secondAppExit = await deadline(
        secondExit,
        APP_EXIT_TIMEOUT_MS,
        'second Desktop instance exit',
      );
      if (secondAppExit.code !== 0 || secondAppExit.signal !== null) {
        throw new Error(
          `second Desktop instance exited abnormally (${secondAppExit.code ?? secondAppExit.signal})`,
        );
      }
      if (!originalProcessAlive(rootIdentity, readDarwinProcessIdentity)) {
        throw new Error('Darwin Desktop process changed after the second instance exited');
      }
      if (!originalProcessAlive(backend, readBackendIdentity)) {
        throw new Error('packaged backend changed after the second instance exited');
      }

      const replacement = await poll(async () => {
        const result = await connection.send('Target.getTargets');
        return selectReplacementPageTarget(
          result.targetInfos,
          expectedRendererURL,
          originalRendererTargetID,
        );
      }, RENDERER_TIMEOUT_MS, 'replacement packaged renderer discovery');
      renderer = await attachRendererTarget(connection, replacement);
      const replacementProbe = await waitForRendererBootstrap(connection, renderer.sessionId);
      if (
        !rendererContractMatches(replacementProbe, expectedRendererURL) ||
        replacementProbe.backendPID !== backend.pid
      ) {
        throw new Error('replacement packaged renderer contract did not match');
      }
      probe = replacementProbe;
      if (!originalProcessAlive(rootIdentity, readDarwinProcessIdentity)) {
        throw new Error('Darwin Desktop process changed during window restoration');
      }
      if (!originalProcessAlive(backend, readBackendIdentity)) {
        throw new Error('packaged backend changed during window restoration');
      }
    }
    if (layout.closeStrategy === 'browser') {
      try {
        await connection.send('Browser.close');
      } catch (error) {
        if (!/CDP connection (?:closed|failed|is not open)/.test(error.message)) throw error;
      }
    } else {
      try {
        await evaluate(connection, renderer.sessionId, 'globalThis.close(); true');
      } catch (error) {
        if (!/CDP connection (?:closed|is not open)/.test(error.message)) throw error;
      }
    }
    connection.close();
    connection = null;

    const appExit = await deadline(exit, APP_EXIT_TIMEOUT_MS, 'Desktop exit');
    if (appExit.code !== 0 || appExit.signal !== null) {
      throw new Error(`Desktop exited abnormally (${appExit.code ?? appExit.signal})`);
    }
    if (!options.crashBackend) {
      await poll(
        () => !originalProcessAlive(backend, readBackendIdentity),
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
      platform: `${layout.platform}-${layout.arch}`,
      crash_containment: options.crashBackend ? 'pass' : 'not_run',
      window_reopen: options.reopenWindow ? 'pass' : 'not_run',
      no_restart_observation_ms: options.crashBackend ? NO_RESTART_OBSERVATION_MS : 0,
    })}\n`);
  } catch (error) {
    const diagnostic = stderr.trim();
    const lockedSessionDiagnostic = layout.platform === 'darwin' &&
      readDarwinScreenLockState() === true
      ? '\nDarwin GUI session is locked; unlock the host before rerunning the lifecycle smoke.'
      : '';
    failure = new Error(
      `${error.message}${lockedSessionDiagnostic}` +
      `${diagnostic ? `\nElectron stderr tail:\n${diagnostic}` : ''}`,
    );
  } finally {
    try {
      connection?.close();
    } catch (cleanupError) {
      failure = combineSmokeFailure(failure, cleanupError);
    }
    if (!passed && secondChild?.pid) {
      try {
        if (processGroupAlive(secondChild.pid)) {
          secondRootIdentity ||= readDarwinProcessIdentity(secondChild.pid);
          await terminateProcessGroup(secondChild.pid, {
            expectedIdentity: secondRootIdentity,
            readIdentity: readDarwinProcessIdentity,
          });
        }
      } catch (cleanupError) {
        failure = combineSmokeFailure(failure, cleanupError);
      }
    }
    if (!passed && child?.pid) {
      try {
        await terminateProcessGroup(child.pid, layout.platform === 'darwin'
          ? {
            expectedIdentity: rootIdentity,
            readIdentity: readDarwinProcessIdentity,
          }
          : undefined);
      } catch (cleanupError) {
        failure = combineSmokeFailure(failure, cleanupError);
      }
    }
    fs.rmSync(temporaryRoot, { recursive: true, force: true });
  }
  if (failure) throw failure;
}

async function main() {
  const input = parseArguments(process.argv.slice(2));
  await runSmoke(input.appPath, {
    crashBackend: input.crashBackend,
    disableSandbox: input.disableSandbox,
    reopenWindow: input.reopenWindow,
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
  combineSmokeFailure,
  crashContainmentMatches,
  killVerifiedBackend,
  makeIsolatedEnvironment,
  observeStable,
  observeSpawnedChild,
  originalPageTargetClosed,
  parseArguments,
  parseDarwinProcessIdentity,
  parseDarwinScreenLockState,
  parseDevToolsEndpoint,
  parseProcStat,
  readDarwinProcessIdentity,
  readDarwinScreenLockState,
  resolveUnpackedLayout,
  safeProcessGroupID,
  sameProcessIdentity,
  selectPageTarget,
  selectReplacementPageTarget,
  targetID,
  terminateProcessGroup,
  validatePlatformOptions,
};
