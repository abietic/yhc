const { spawn, spawnSync } = require('node:child_process');
const fs = require('node:fs');
const http = require('node:http');
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
const WINDOWS_PROCESS_QUERY_TIMEOUT_MS = 10_000;
const WINDOWS_PROCESS_QUERY_MAX_BUFFER = 1 << 20;
const DARWIN_IOREG_MAX_BUFFER = 256 << 10;
const ACTIVE_TURN_PROVIDER_MAX_BODY = 1 << 20;
const ACTIVE_TURN_SEED_TIMEOUT_MS = 20_000;
const ACTIVE_TURN_PROVIDER_KEY = 'desktop-active-turn-fixture-key';
const ACTIVE_TURN_PROVIDER_MODEL = 'fixture-model';
const ACTIVE_TURN_SEED_PROMPT = 'YHC_DESKTOP_ACTIVE_CRASH_SEED_7Q9';
const ACTIVE_TURN_SEED_OUTPUT = 'Desktop active-turn seed completed.';
const ACTIVE_TURN_PROMPT = 'YHC_DESKTOP_ACTIVE_CRASH_STREAM_7Q9';
const ACTIVE_TURN_PARTIAL = 'Desktop active turn is still streaming.';
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

function normalizeWindowsExecutable(value) {
  if (
    typeof value !== 'string' ||
    value.length === 0 ||
    /[\u0000-\u001f\u007f]/.test(value)
  ) {
    throw new Error('invalid Windows process identity');
  }
  const normalized = path.win32.normalize(value);
  if (!/^[A-Za-z]:\\/.test(normalized) || path.win32.basename(normalized).length === 0) {
    throw new Error('invalid Windows process identity');
  }
  return normalized.toLowerCase();
}

function normalizeWindowsStartTime(value) {
  if (
    typeof value !== 'string' ||
    !/^[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}\.[0-9]{7}Z$/.test(value) ||
    !Number.isFinite(Date.parse(value))
  ) {
    throw new Error('invalid Windows process identity');
  }
  return value;
}

function normalizeWindowsProcessRecord(record, { identityRequired = true } = {}) {
  if (!record || typeof record !== 'object' || Array.isArray(record)) {
    throw new Error('invalid Windows process identity');
  }
  const pid = Number(record.ProcessId);
  const parentPid = Number(record.ParentProcessId);
  if (
    !Number.isSafeInteger(pid) || pid <= 1 ||
    !Number.isSafeInteger(parentPid) || parentPid < 0
  ) {
    throw new Error('invalid Windows process identity');
  }
  let startTime = null;
  let executable = null;
  if (record.CreationTimeUtc !== null && record.CreationTimeUtc !== undefined) {
    startTime = normalizeWindowsStartTime(record.CreationTimeUtc);
  }
  if (record.ExecutablePath !== null && record.ExecutablePath !== undefined) {
    executable = normalizeWindowsExecutable(record.ExecutablePath);
  }
  if (identityRequired && (!startTime || !executable)) {
    throw new Error('invalid Windows process identity');
  }
  return { pid, parentPid, startTime, executable };
}

function parseWindowsProcessIdentity(contents, expectedPID) {
  if (!Number.isSafeInteger(expectedPID) || expectedPID <= 1) {
    throw new Error('invalid backend process ID');
  }
  try {
    const identity = normalizeWindowsProcessRecord(JSON.parse(String(contents)));
    if (identity.pid !== expectedPID) throw new Error('Windows process ID changed');
    return identity;
  } catch (error) {
    if (error?.message === 'invalid backend process ID') throw error;
    throw new Error('invalid Windows process identity', { cause: error });
  }
}

function parseWindowsProcessSnapshot(contents) {
  try {
    const records = JSON.parse(String(contents));
    if (!Array.isArray(records) || records.length > 65_536) {
      throw new Error('bounded Windows process array required');
    }
    const snapshot = records.map((record) => (
      normalizeWindowsProcessRecord(record, { identityRequired: false })
    ));
    if (new Set(snapshot.map((record) => record.pid)).size !== snapshot.length) {
      throw new Error('duplicate Windows process ID');
    }
    return snapshot;
  } catch (error) {
    throw new Error('invalid Windows process snapshot', { cause: error });
  }
}

function windowsPowerShellOptions(maxBuffer) {
  return {
    encoding: 'utf8',
    maxBuffer,
    shell: false,
    timeout: WINDOWS_PROCESS_QUERY_TIMEOUT_MS,
    windowsHide: true,
  };
}

function readWindowsProcessIdentity(
  pid,
  { runPowerShell = spawnSync } = {},
) {
  if (!Number.isSafeInteger(pid) || pid <= 1) {
    throw new Error('invalid backend process ID');
  }
  const projection = "Select-Object ProcessId,ParentProcessId,@{Name='CreationTimeUtc';Expression={$_.CreationDate.ToUniversalTime().ToString('O')}},ExecutablePath";
  const script = [
    "$ErrorActionPreference='Stop'",
    `$process = Get-CimInstance Win32_Process -Filter 'ProcessId = ${pid}'`,
    'if ($null -eq $process) { exit 3 }',
    'if (@($process).Count -ne 1) { exit 4 }',
    `$selected = $process | ${projection}`,
    'ConvertTo-Json -InputObject $selected -Compress',
  ].join('; ');
  const result = runPowerShell('powershell.exe', [
    '-NoLogo',
    '-NoProfile',
    '-NonInteractive',
    '-Command',
    script,
  ], windowsPowerShellOptions(PROCESS_QUERY_MAX_BUFFER));
  if (result?.error) {
    throw new Error('Windows process inspection failed', { cause: result.error });
  }
  const stdout = Buffer.from(result?.stdout || '').toString('utf8');
  const stderr = Buffer.from(result?.stderr || '').toString('utf8');
  if (result?.status === 3 && stdout.trim() === '' && stderr.trim() === '') return null;
  if (result?.status !== 0) throw new Error('Windows process inspection failed');
  return parseWindowsProcessIdentity(stdout, pid);
}

function readWindowsProcessSnapshot({ runPowerShell = spawnSync } = {}) {
  const projection = "Select-Object ProcessId,ParentProcessId,@{Name='CreationTimeUtc';Expression={$_.CreationDate.ToUniversalTime().ToString('O')}},ExecutablePath";
  const script = [
    "$ErrorActionPreference='Stop'",
    `$selected = @(Get-CimInstance Win32_Process | ${projection})`,
    'ConvertTo-Json -InputObject $selected -Compress',
  ].join('; ');
  const result = runPowerShell('powershell.exe', [
    '-NoLogo',
    '-NoProfile',
    '-NonInteractive',
    '-Command',
    script,
  ], windowsPowerShellOptions(WINDOWS_PROCESS_QUERY_MAX_BUFFER));
  if (result?.error || result?.status !== 0) {
    throw new Error('Windows process tree inspection failed', {
      ...(result?.error ? { cause: result.error } : {}),
    });
  }
  return parseWindowsProcessSnapshot(result.stdout || '');
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
    platform = process.platform,
    readIdentity = readLinuxProcessIdentity,
    runTaskkill = spawnSync,
    signalProcess = process.kill,
  } = {},
) {
  const current = readIdentity(identity?.pid);
  if (!sameProcessIdentity(identity, current)) {
    throw new Error('backend process identity changed before crash injection');
  }
  if (platform === 'win32') {
    const result = runTaskkill('taskkill.exe', [
      '/PID',
      String(identity.pid),
      '/F',
    ], {
      encoding: 'utf8',
      maxBuffer: PROCESS_QUERY_MAX_BUFFER,
      shell: false,
      timeout: WINDOWS_PROCESS_QUERY_TIMEOUT_MS,
      windowsHide: true,
    });
    if (result?.error || result?.status !== 0) {
      throw new Error('Windows backend crash injection failed', {
        ...(result?.error ? { cause: result.error } : {}),
      });
    }
    return;
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

function activeTurnCrashContainmentMatches(result) {
  return crashContainmentMatches(result) &&
    typeof result.activeSessionID === 'string' &&
    result.activeSessionID.length > 0 &&
    typeof result.activeTurnID === 'string' &&
    result.activeTurnID.length > 0 &&
    result.snapshotActiveTurnID === result.activeTurnID &&
    typeof result.snapshotPartialTurnID === 'string' &&
    result.snapshotPartialTurnID.length > 0 &&
    result.snapshotPartialCount === 1 &&
    result.snapshotPartialCompleted === false &&
    result.partialVisible === true &&
    result.activePromptVisible === true &&
    result.interactionHidden === true &&
    result.sessionIdentityVisible === true &&
    result.terminalEventCount === 0;
}

function requireLoopbackProviderURL(candidate) {
  let parsed;
  try {
    parsed = new URL(candidate);
  } catch {
    throw new Error('active-turn provider URL must be loopback HTTP');
  }
  if (
    parsed.protocol !== 'http:' ||
    parsed.hostname !== '127.0.0.1' ||
    !/^[0-9]+$/.test(parsed.port) ||
    Number(parsed.port) < 1 ||
    Number(parsed.port) > 65535 ||
    parsed.pathname !== '/' ||
    parsed.username ||
    parsed.password ||
    parsed.search ||
    parsed.hash
  ) {
    throw new Error('active-turn provider URL must be loopback HTTP');
  }
  return parsed.origin;
}

function activeTurnFixtureEnvironment(base, baseURL) {
  if (!base || typeof base !== 'object' || Array.isArray(base)) {
    throw new TypeError('isolated environment required');
  }
  return {
    ...base,
    NO_PROXY: '127.0.0.1,localhost',
    PROV: 'openai',
    PROV_MODEL: ACTIVE_TURN_PROVIDER_MODEL,
    PROV_API_KEY: ACTIVE_TURN_PROVIDER_KEY,
    PROV_BASE_URL: requireLoopbackProviderURL(baseURL),
  };
}

function latestUserInput(input) {
  if (!Array.isArray(input)) return '';
  for (let index = input.length - 1; index >= 0; index -= 1) {
    if (input[index]?.role === 'user') return JSON.stringify(input[index]);
  }
  return '';
}

function writeProviderEvent(response, name, value) {
  response.write(`event: ${name}\ndata: ${JSON.stringify(value)}\n\n`);
}

function providerTextPart(text) {
  return { type: 'output_text', text, annotations: [], logprobs: [] };
}

function providerTextItem(identity, text, status = 'completed') {
  return {
    id: `msg-${identity}`,
    type: 'message',
    role: 'assistant',
    status,
    content: text ? [providerTextPart(text)] : [],
  };
}

function writeProviderDelta(response, text, identity) {
  const itemID = `msg-${identity}`;
  writeProviderEvent(response, 'response.output_item.added', {
    type: 'response.output_item.added',
    sequence_number: 0,
    output_index: 0,
    item: providerTextItem(identity, '', 'in_progress'),
  });
  writeProviderEvent(response, 'response.content_part.added', {
    type: 'response.content_part.added',
    sequence_number: 1,
    item_id: itemID,
    output_index: 0,
    content_index: 0,
    part: providerTextPart(''),
  });
  writeProviderEvent(response, 'response.output_text.delta', {
    type: 'response.output_text.delta',
    sequence_number: 2,
    item_id: itemID,
    output_index: 0,
    content_index: 0,
    delta: text,
    logprobs: [],
  });
}

function completeProviderResponse(response, text) {
  writeProviderDelta(response, text, 'seed');
  const item = providerTextItem('seed', text);
  writeProviderEvent(response, 'response.content_part.done', {
    type: 'response.content_part.done',
    sequence_number: 3,
    item_id: item.id,
    output_index: 0,
    content_index: 0,
    part: providerTextPart(text),
  });
  writeProviderEvent(response, 'response.output_item.done', {
    type: 'response.output_item.done',
    sequence_number: 4,
    output_index: 0,
    item,
  });
  writeProviderEvent(response, 'response.completed', {
    type: 'response.completed',
    sequence_number: 5,
    response: {
      id: 'resp-seed',
      object: 'response',
      status: 'completed',
      model: ACTIVE_TURN_PROVIDER_MODEL,
      output: [item],
      usage: { input_tokens: 1, output_tokens: 1, total_tokens: 2 },
    },
  });
  response.end();
}

async function startActiveTurnProvider({ createServer = http.createServer } = {}) {
  let resolveActive;
  const activeRequest = new Promise((resolve) => { resolveActive = resolve; });
  const counts = { active: 0, seed: 0, total: 0 };
  const sockets = new Set();
  let closed = false;
  const server = createServer((request, response) => {
    counts.total += 1;
    if (request.method !== 'POST' || request.url !== '/responses') {
      response.writeHead(404).end();
      return;
    }
    if (request.headers.authorization !== `Bearer ${ACTIVE_TURN_PROVIDER_KEY}`) {
      response.writeHead(401).end();
      return;
    }
    const contentType = String(request.headers['content-type'] || '').toLowerCase();
    if (!contentType.startsWith('application/json')) {
      response.writeHead(415).end();
      return;
    }

    let body = '';
    let bodyBytes = 0;
    let rejected = false;
    request.setEncoding('utf8');
    request.on('data', (chunk) => {
      if (rejected) return;
      body += chunk;
      bodyBytes += Buffer.byteLength(chunk);
      if (bodyBytes > ACTIVE_TURN_PROVIDER_MAX_BODY) {
        rejected = true;
        response.writeHead(413).end();
        request.destroy();
      }
    });
    request.on('end', () => {
      if (rejected) return;
      let input;
      try {
        input = JSON.parse(body);
      } catch {
        response.writeHead(400).end();
        return;
      }
      if (input?.model !== ACTIVE_TURN_PROVIDER_MODEL || input?.stream !== true) {
        response.writeHead(422).end();
        return;
      }
      const prompt = latestUserInput(input.input);
      response.writeHead(200, {
        'Cache-Control': 'no-cache',
        Connection: 'keep-alive',
        'Content-Type': 'text/event-stream',
      });
      if (prompt.includes(ACTIVE_TURN_SEED_PROMPT)) {
        counts.seed += 1;
        if (counts.seed !== 1) {
          response.destroy();
          return;
        }
        completeProviderResponse(response, ACTIVE_TURN_SEED_OUTPUT);
        return;
      }
      if (!prompt.includes(ACTIVE_TURN_PROMPT)) {
        response.destroy();
        return;
      }
      counts.active += 1;
      if (counts.active !== 1) {
        response.destroy();
        return;
      }
      writeProviderDelta(response, ACTIVE_TURN_PARTIAL, 'active');
      const keepAlive = setInterval(() => response.write(': keepalive\n\n'), 500);
      const settle = () => clearInterval(keepAlive);
      request.once('aborted', settle);
      response.once('close', settle);
      resolveActive();
    });
  });
  server.on('connection', (socket) => {
    sockets.add(socket);
    socket.once('close', () => sockets.delete(socket));
  });
  server.on('clientError', (_error, socket) => socket.destroy());
  await deadline(new Promise((resolve, reject) => {
    const onError = (error) => reject(error);
    server.once('error', onError);
    server.listen(0, '127.0.0.1', () => {
      server.off('error', onError);
      resolve();
    });
  }), CLEANUP_TIMEOUT_MS, 'active-turn provider listen');
  const address = server.address();
  if (!address || typeof address === 'string' || address.address !== '127.0.0.1') {
    for (const socket of sockets) socket.destroy();
    server.close();
    throw new Error('active-turn provider did not bind loopback');
  }
  const baseURL = requireLoopbackProviderURL(`http://127.0.0.1:${address.port}`);
  return {
    baseURL,
    requestCounts: () => ({ ...counts }),
    waitForActiveRequest: () => activeRequest,
    async close() {
      if (closed) return;
      closed = true;
      const closing = new Promise((resolve, reject) => {
        server.close((error) => (error ? reject(error) : resolve()));
      });
      for (const socket of sockets) socket.destroy();
      await deadline(closing, CLEANUP_TIMEOUT_MS, 'active-turn provider cleanup');
    },
  };
}

function decodeSeedSession(stdout) {
  let envelope;
  try {
    envelope = JSON.parse(String(stdout).trim());
  } catch {
    throw new Error('packaged session seed returned invalid JSON');
  }
  if (
    envelope?.schema_version !== 1 ||
    envelope.status !== 'completed' ||
    envelope.output !== ACTIVE_TURN_SEED_OUTPUT ||
    envelope.terminal_reason !== 'completed' ||
    envelope.exit_code !== 0 ||
    typeof envelope.session_id !== 'string' ||
    !/^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$/.test(envelope.session_id)
  ) {
    throw new Error('packaged session seed did not complete');
  }
  return envelope.session_id;
}

async function seedActiveTurnSession({
  backendPath,
  baseURL,
  environment,
  spawnChild = spawn,
  workspace,
}) {
  if (typeof backendPath !== 'string' || backendPath.length === 0) {
    throw new TypeError('packaged backend path required');
  }
  if (typeof workspace !== 'string' || workspace.length === 0) {
    throw new TypeError('active-turn workspace required');
  }
  const args = [
    'exec',
    ACTIVE_TURN_SEED_PROMPT,
    '--output-format', 'json',
    '--max-turns', '1',
    '--tools', '',
  ];
  if (
    environment?.PROV !== 'openai' ||
    environment?.PROV_MODEL !== ACTIVE_TURN_PROVIDER_MODEL ||
    environment?.PROV_API_KEY !== ACTIVE_TURN_PROVIDER_KEY ||
    environment?.PROV_BASE_URL !== requireLoopbackProviderURL(baseURL)
  ) {
    throw new Error('packaged session seed provider environment did not match');
  }
  const child = spawnChild(backendPath, args, {
    cwd: workspace,
    env: environment,
    shell: false,
    stdio: ['ignore', 'pipe', 'pipe'],
    windowsHide: true,
  });
  const exit = observeSpawnedChild(child, { requireProcessGroup: false });
  let stdout = '';
  let stderr = '';
  child.stdout.on('data', (chunk) => {
    stdout = appendBounded(stdout, chunk);
  });
  child.stderr.on('data', (chunk) => {
    stderr = appendBounded(stderr, chunk);
  });
  let result;
  try {
    result = await deadline(exit, ACTIVE_TURN_SEED_TIMEOUT_MS, 'packaged session seed');
  } catch (error) {
    if (child.exitCode === null && child.signalCode === null) child.kill('SIGKILL');
    try {
      await deadline(exit, CLEANUP_TIMEOUT_MS, 'packaged session seed cleanup');
    } catch (cleanupError) {
      throw combineSmokeFailure(error, cleanupError);
    }
    throw error;
  }
  if (result.code !== 0 || result.signal !== null) {
    const diagnostic = stderr.trim();
    throw new Error(
      `packaged session seed exited abnormally (${result.code ?? result.signal})` +
      `${diagnostic ? `\nSeed stderr tail:\n${diagnostic}` : ''}`,
    );
  }
  return decodeSeedSession(stdout);
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

async function closePackagedDesktop(
  connection,
  rendererSessionID,
  closeStrategy,
  exit,
  { waitForExit = (pending) => deadline(pending, APP_EXIT_TIMEOUT_MS, 'Desktop exit') } = {},
) {
  if (closeStrategy === 'browser') {
    try {
      await connection.send('Browser.close');
    } catch (error) {
      if (!/^(?:CDP Browser\.close timed out|CDP connection (?:closed|failed|is not open))$/.test(
        error.message,
      )) {
        throw error;
      }
    }
  } else {
    try {
      await evaluate(connection, rendererSessionID, 'globalThis.close(); true');
    } catch (error) {
      if (!/^CDP connection (?:closed|is not open)$/.test(error.message)) throw error;
    }
  }
  connection.close();
  return waitForExit(exit);
}

function shouldForceCrashCleanup(error, crashRequested) {
  return crashRequested === true && error?.message === 'Desktop exit timed out';
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

function requireExecutable(candidate, label, { platform = process.platform } = {}) {
  const info = fs.lstatSync(candidate);
  if (
    !info.isFile() ||
    info.isSymbolicLink() ||
    (platform !== 'win32' && (info.mode & 0o111) === 0)
  ) {
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

function sourceEnvironmentValue(sourceEnvironment, name) {
  const direct = sourceEnvironment[name];
  if (typeof direct === 'string' && direct.length > 0) return direct;
  const matching = Object.keys(sourceEnvironment).find((candidate) => (
    candidate.toLowerCase() === name.toLowerCase()
  ));
  const value = matching ? sourceEnvironment[matching] : undefined;
  return typeof value === 'string' && value.length > 0 ? value : undefined;
}

function makeIsolatedEnvironment(
  root,
  sourceEnvironment = process.env,
  { platform = process.platform } = {},
) {
  const environment = {};
  for (const name of [
    'PATH',
    'LANG',
    'LC_ALL',
    'TZ',
    'SystemRoot',
    'WINDIR',
    'ComSpec',
    'PATHEXT',
  ]) {
    const value = sourceEnvironmentValue(sourceEnvironment, name);
    if (value) environment[name] = value;
  }
  if (!environment.PATH) throw new Error('PATH is required');
  for (const directory of ['home', 'tmp', 'config', 'cache', 'data', 'runtime']) {
    const target = path.join(root, directory);
    fs.mkdirSync(target, { recursive: true, mode: 0o700 });
    fs.chmodSync(target, 0o700);
  }
  const isolated = {
    ...environment,
    HOME: path.join(root, 'home'),
    TMPDIR: path.join(root, 'tmp'),
    XDG_CONFIG_HOME: path.join(root, 'config'),
    XDG_CACHE_HOME: path.join(root, 'cache'),
    XDG_DATA_HOME: path.join(root, 'data'),
    XDG_RUNTIME_DIR: path.join(root, 'runtime'),
    NO_AT_BRIDGE: '1',
  };
  if (platform === 'win32') {
    isolated.USERPROFILE = path.join(root, 'home');
    isolated.APPDATA = path.join(root, 'config');
    isolated.LOCALAPPDATA = path.join(root, 'data');
    isolated.TEMP = path.join(root, 'tmp');
    isolated.TMP = path.join(root, 'tmp');
  }
  return isolated;
}

function waitForChildExit(child) {
  return new Promise((resolve, reject) => {
    child.once('error', reject);
    child.once('exit', (code, signal) => resolve({ code, signal }));
  });
}

function safeProcessID(pid) {
  if (!Number.isSafeInteger(pid) || pid <= 1) throw new Error('unsafe process');
  return pid;
}

function observeSpawnedChild(child, { requireProcessGroup = true } = {}) {
  const exit = waitForChildExit(child);
  // A spawn failure is emitted asynchronously. Attach a rejection handler before
  // validating the PID so an invalid or missing PID cannot leave an unhandled
  // child error behind while the caller unwinds.
  void exit.catch(() => {});
  if (requireProcessGroup) {
    safeProcessGroupID(child.pid);
  } else {
    safeProcessID(child.pid);
  }
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

function windowsProcessTreeMembers(snapshot, expectedRoot, expectedBackend) {
  if (!Array.isArray(snapshot)) throw new Error('Windows process tree inspection failed');
  const rootMatches = snapshot.filter((record) => record?.pid === expectedRoot.pid);
  if (rootMatches.length !== 1 || !sameProcessIdentity(expectedRoot, rootMatches[0])) {
    throw new Error('Windows cleanup ownership could not be confirmed');
  }
  const allowedExecutables = new Set([expectedRoot.executable]);
  if (expectedBackend?.executable) allowedExecutables.add(expectedBackend.executable);
  const members = [rootMatches[0]];
  const visited = new Set([expectedRoot.pid]);
  for (let index = 0; index < members.length; index += 1) {
    const parent = members[index];
    for (const candidate of snapshot) {
      if (candidate?.parentPid !== parent.pid || visited.has(candidate.pid)) continue;
      if (
        !candidate.startTime ||
        !candidate.executable ||
        Date.parse(candidate.startTime) < Date.parse(parent.startTime)
      ) {
        throw new Error('Windows process tree lineage could not be confirmed');
      }
      if (!allowedExecutables.has(candidate.executable)) {
        throw new Error('Windows process tree contained an unowned executable');
      }
      visited.add(candidate.pid);
      members.push(candidate);
    }
  }
  if (expectedBackend && !members.some((record) => (
    sameProcessIdentity(expectedBackend, record)
  ))) {
    throw new Error('Windows backend ownership could not be confirmed');
  }
  return members;
}

async function terminateWindowsProcessTree(
  expectedRoot,
  {
    expectedBackend,
    readIdentity = readWindowsProcessIdentity,
    readSnapshot = readWindowsProcessSnapshot,
    runTaskkill = spawnSync,
    waitFor = poll,
  } = {},
) {
  safeProcessID(expectedRoot?.pid);
  if (!expectedRoot.startTime || !expectedRoot.executable) {
    throw new Error('Windows cleanup ownership could not be confirmed');
  }
  const currentRoot = readIdentity(expectedRoot.pid);
  if (!currentRoot) {
    if (expectedBackend && originalProcessAlive(expectedBackend, readIdentity)) {
      throw new Error('Windows root exited while its backend remained');
    }
    return;
  }
  if (!sameProcessIdentity(expectedRoot, currentRoot)) {
    throw new Error('Windows cleanup ownership could not be confirmed');
  }
  let currentBackend = null;
  if (expectedBackend) {
    currentBackend = readIdentity(expectedBackend.pid);
    if (currentBackend && !sameProcessIdentity(expectedBackend, currentBackend)) {
      throw new Error('Windows backend ownership could not be confirmed');
    }
  }
  windowsProcessTreeMembers(
    readSnapshot(),
    expectedRoot,
    currentBackend ? expectedBackend : undefined,
  );
  const freshRoot = readIdentity(expectedRoot.pid);
  if (!sameProcessIdentity(expectedRoot, freshRoot)) {
    throw new Error('Windows cleanup ownership could not be confirmed');
  }
  const result = runTaskkill('taskkill.exe', [
    '/PID',
    String(expectedRoot.pid),
    '/T',
    '/F',
  ], {
    encoding: 'utf8',
    maxBuffer: PROCESS_QUERY_MAX_BUFFER,
    shell: false,
    timeout: WINDOWS_PROCESS_QUERY_TIMEOUT_MS,
    windowsHide: true,
  });
  if (result?.error || result?.status !== 0) {
    throw new Error('Windows process tree cleanup failed', {
      ...(result?.error ? { cause: result.error } : {}),
    });
  }
  await waitFor(() => (
    !originalProcessAlive(expectedRoot, readIdentity) &&
    (!expectedBackend || !originalProcessAlive(expectedBackend, readIdentity))
  ), CLEANUP_TIMEOUT_MS, 'Windows process tree cleanup');
}

function windowsFileURL(candidate) {
  const normalized = path.win32.resolve(candidate);
  if (!/^[A-Za-z]:\\/.test(normalized)) {
    throw new Error('invalid Windows unpacked Desktop application path');
  }
  const drive = normalized.slice(0, 2);
  const segments = normalized.slice(3).split('\\').map(encodeURIComponent);
  return `file:///${drive}/${segments.join('/')}`;
}

function resolveUnpackedLayout(
  appCandidate,
  { platform = process.platform, arch = process.arch } = {},
) {
  if (typeof appCandidate !== 'string' || appCandidate.length === 0) {
    throw new TypeError('unpacked Desktop application path required');
  }
  const pathAPI = platform === 'win32' ? path.win32 : path;
  const appPath = pathAPI.resolve(appCandidate);
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
  if (platform === 'win32' && arch === 'x64') {
    const applicationPath = pathAPI.dirname(appPath);
    if (
      pathAPI.basename(appPath).toLowerCase() !== 'yhc.exe' ||
      pathAPI.basename(applicationPath).toLowerCase() !== 'win-unpacked'
    ) {
      throw new Error('invalid Windows unpacked Desktop application path');
    }
    const resourcesPath = pathAPI.join(applicationPath, 'resources');
    return {
      appPath,
      arch,
      backendPath: pathAPI.join(resourcesPath, 'bin', 'yhc.exe'),
      cleanupStrategy: 'windows-tree',
      closeStrategy: 'browser',
      launcher: 'direct',
      platform,
      rendererURL: windowsFileURL(pathAPI.join(resourcesPath, 'webui', 'index.html')),
      resourcesPath,
    };
  }
  if (platform === 'win32') {
    throw new Error('unpacked lifecycle smoke requires Windows x64');
  }
  throw new Error(`unsupported unpacked lifecycle platform ${platform}/${arch}`);
}

function validatePlatformOptions(
  platform,
  {
    crashActiveTurn = false,
    crashBackend = false,
    disableSandbox = false,
    reopenWindow = false,
  } = {},
) {
  const normalized = {
    crashActiveTurn: crashActiveTurn === true,
    crashBackend: crashBackend === true,
    disableSandbox: disableSandbox === true,
    reopenWindow: reopenWindow === true,
  };
  const crashModes = Number(normalized.crashBackend) + Number(normalized.crashActiveTurn);
  if (crashModes > 1 || (crashModes === 1 && normalized.reopenWindow)) {
    throw new Error('backend crash modes and window restoration smoke are mutually exclusive');
  }
  if (platform === 'linux') {
    if (normalized.reopenWindow) throw new Error('--reopen-window is macOS-only');
    return normalized;
  }
  if (platform === 'darwin') {
    if (normalized.disableSandbox) throw new Error('--no-sandbox is Linux-only');
    return normalized;
  }
  if (platform === 'win32') {
    if (normalized.disableSandbox) throw new Error('--no-sandbox is Linux-only');
    if (normalized.reopenWindow) throw new Error('--reopen-window is macOS-only');
    return normalized;
  }
  if (platform !== 'darwin' && normalized.reopenWindow) {
    throw new Error('--reopen-window is macOS-only');
  }
  if (crashModes > 0 || normalized.disableSandbox) {
    throw new Error('crash injection and --no-sandbox are supported only on Linux, macOS, and Windows');
  }
  return normalized;
}

function parseArguments(argv) {
  let appPath;
  let crashActiveTurn = false;
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
    } else if (argument === '--crash-active-turn' && !crashActiveTurn) {
      crashActiveTurn = true;
    } else if (argument === '--no-sandbox' && !disableSandbox) {
      disableSandbox = true;
    } else if (argument === '--reopen-window' && !reopenWindow) {
      reopenWindow = true;
    } else {
      invalid = true;
      break;
    }
  }
  const crashModes = Number(crashBackend) + Number(crashActiveTurn);
  if (invalid || !appPath || crashModes > 1 || (crashModes === 1 && reopenWindow)) {
    throw new Error(
      'usage: unpacked_lifecycle_smoke.cjs --app PATH ' +
        '[--crash-backend|--crash-active-turn] [--no-sandbox] [--reopen-window]',
    );
  }
  return {
    appPath: path.resolve(appPath),
    crashActiveTurn,
    crashBackend,
    disableSandbox,
    reopenWindow,
  };
}

async function installCrashObservers(connection, rendererSessionID, activeSessionID = '') {
  const subscribed = await evaluate(connection, rendererSessionID, `(() => {
    const bridge = globalThis.yhcDesktop;
    if (typeof bridge?.onBackendExit !== 'function') return false;
    globalThis.__yhcCrashCount = 0;
    globalThis.__yhcCrashUnsubscribe = bridge.onBackendExit(() => {
      globalThis.__yhcCrashCount += 1;
    });
    globalThis.__yhcTerminalEvents = [];
    const watchedSessionID = ${JSON.stringify(activeSessionID)};
    if (watchedSessionID) {
      if (typeof bridge.onEventStream !== 'function') return false;
      globalThis.__yhcEventUnsubscribe = bridge.onEventStream((payload) => {
        const type = payload?.event?.type || '';
        if (payload?.sessionID !== watchedSessionID) return;
        if (type === 'terminal' || type === 'turn.finished') {
          globalThis.__yhcTerminalEvents.push({
            type,
            turnID: payload?.event?.turn_id || '',
          });
        }
      });
    }
    return typeof globalThis.__yhcCrashUnsubscribe === 'function' &&
      (!watchedSessionID || typeof globalThis.__yhcEventUnsubscribe === 'function');
  })()`);
  if (subscribed !== true) throw new Error('backend crash observer was not installed');
}

async function prepareActiveTurnCrash(
  connection,
  rendererSessionID,
  activeSessionID,
  provider,
) {
  await poll(async () => {
    const result = await evaluate(connection, rendererSessionID, `(() => {
      const rows = [...document.querySelectorAll('#session-list > .session-row')];
      const active = rows.filter((row) => row.classList.contains('active'));
      const timelineText = document.querySelector('#timeline')?.textContent || '';
      return {
        rowCount: rows.length,
        activeCount: active.length,
        promptEnabled: document.querySelector('#prompt')?.disabled === false,
        sendEnabled: document.querySelector('#send')?.disabled === false,
        seedPromptVisible: timelineText.includes(${JSON.stringify(ACTIVE_TURN_SEED_PROMPT)}),
        seedOutputVisible: timelineText.includes(${JSON.stringify(ACTIVE_TURN_SEED_OUTPUT)}),
      };
    })()`);
    const ready = result?.rowCount === 1 &&
      result.activeCount === 1 &&
      result.promptEnabled === true &&
      result.sendEnabled === true &&
      result.seedPromptVisible === true &&
      result.seedOutputVisible === true;
    if (!ready) {
      throw new Error(`seeded session state ${JSON.stringify(result)}`);
    }
    return true;
  }, RENDERER_TIMEOUT_MS, 'seeded Desktop session discovery');

  const submitted = await evaluate(connection, rendererSessionID, `(() => {
    const prompt = document.querySelector('#prompt');
    const composer = document.querySelector('#composer');
    if (!prompt || !composer || prompt.disabled) return false;
    prompt.value = ${JSON.stringify(ACTIVE_TURN_PROMPT)};
    prompt.dispatchEvent(new Event('input', { bubbles: true }));
    composer.requestSubmit();
    return true;
  })()`);
  if (submitted !== true) throw new Error('active-turn fixture prompt was not submitted');
  try {
    await deadline(
      provider.waitForActiveRequest(),
      RENDERER_TIMEOUT_MS,
      'active-turn provider request',
    );
  } catch (error) {
    const state = await evaluate(connection, rendererSessionID, `(() => ({
      status: document.querySelector('#status')?.textContent || '',
      notice: document.querySelector('#turn-state')?.textContent || '',
      promptDisabled: document.querySelector('#prompt')?.disabled === true,
      sendDisabled: document.querySelector('#send')?.disabled === true,
      cancelDisabled: document.querySelector('#cancel')?.disabled === true,
    }))()`);
    throw new Error(
      `${error.message}: active submit state ${JSON.stringify(state)}, ` +
      `provider counts ${JSON.stringify(provider.requestCounts())}`,
    );
  }

  let lastProjection;
  try {
    return await poll(async () => {
      const result = await evaluate(connection, rendererSessionID, `(async () => {
      const bridge = globalThis.yhcDesktop;
      const snapshot = await bridge.api('snapshot', {
        sessionID: ${JSON.stringify(activeSessionID)},
      });
      const snapshotActiveTurnID = snapshot?.session?.active_turn_id || '';
      const partials = Array.isArray(snapshot?.messages)
        ? snapshot.messages.filter((message) => (
          message?.role === 'assistant' &&
          String(message?.content || '').includes(${JSON.stringify(ACTIVE_TURN_PARTIAL)})
        ))
        : [];
      const partial = partials.length === 1 ? partials[0] : null;
      const snapshotMessageFacts = Array.isArray(snapshot?.messages)
        ? snapshot.messages.slice(-8).map((message) => ({
          role: String(message?.role || ''),
          turnMatches: message?.turn_id === snapshotActiveTurnID,
          completed: message?.completed,
          containsPartial: String(message?.content || '')
            .includes(${JSON.stringify(ACTIVE_TURN_PARTIAL)}),
          contentLength: String(message?.content || '').length,
          source: String(message?.source || ''),
        }))
        : [];
      const timelineText = document.querySelector('#timeline')?.textContent || '';
      return {
        activeSessionID: snapshot?.session?.id || '',
        activeTurnID: snapshotActiveTurnID,
        snapshotActiveTurnID,
        snapshotPartialTurnID: partial?.turn_id || '',
        snapshotPartialCount: partials.length,
        snapshotPartialCompleted: partial?.completed,
        snapshotMessageFacts,
        partialVisible: timelineText.includes(${JSON.stringify(ACTIVE_TURN_PARTIAL)}),
        activePromptVisible: timelineText.includes(${JSON.stringify(ACTIVE_TURN_PROMPT)}),
        sessionTitle: document.querySelector('#session-title')?.textContent || '',
        workspaceLabel: document.querySelector('#session-path')?.textContent || '',
        status: document.querySelector('#status')?.textContent || '',
        cancelEnabled: document.querySelector('#cancel')?.disabled === false,
        sendDisabled: document.querySelector('#send')?.disabled === true,
        busySession: Boolean(document.querySelector(
          '#session-list > .session-row.active > .session-dot.is-busy',
        )),
      };
      })()`);
      lastProjection = result;
      return result?.activeSessionID === activeSessionID &&
        result.activeTurnID &&
        result.snapshotActiveTurnID === result.activeTurnID &&
        result.snapshotPartialTurnID &&
        result.snapshotPartialCount === 1 &&
        result.snapshotPartialCompleted === false &&
        result.partialVisible === true &&
        result.activePromptVisible === true &&
        result.sessionTitle &&
        result.workspaceLabel &&
        result.status === 'running' &&
        result.cancelEnabled === true &&
        result.sendDisabled === true &&
        result.busySession === true
        ? result
        : null;
    }, RENDERER_TIMEOUT_MS, 'active-turn renderer projection');
  } catch (error) {
    throw new Error(
      `${error.message}: last projection ${JSON.stringify(lastProjection)}, ` +
      `provider counts ${JSON.stringify(provider.requestCounts())}`,
    );
  }
}

async function removeCrashObservers(connection, rendererSessionID) {
  await evaluate(connection, rendererSessionID, `(() => {
    globalThis.__yhcCrashUnsubscribe?.();
    globalThis.__yhcEventUnsubscribe?.();
    delete globalThis.__yhcCrashUnsubscribe;
    delete globalThis.__yhcEventUnsubscribe;
    delete globalThis.__yhcTerminalEvents;
    return true;
  })()`);
}

async function runSmoke(
  appCandidate,
  {
    crashActiveTurn = false,
    crashBackend = false,
    disableSandbox = false,
    reopenWindow = false,
  } = {},
) {
  let layout = resolveUnpackedLayout(appCandidate);
  const options = validatePlatformOptions(layout.platform, {
    crashActiveTurn,
    crashBackend,
    disableSandbox,
    reopenWindow,
  });
  const appPath = requireExecutable(layout.appPath, 'unpacked Desktop application', {
    platform: layout.platform,
  });
  layout = resolveUnpackedLayout(appPath, {
    arch: layout.arch,
    platform: layout.platform,
  });
  const expectedRendererURL = layout.rendererURL;
  const expectedBackend = requireExecutable(
    layout.backendPath,
    'packaged backend',
    { platform: layout.platform },
  );
  const expectedAppIdentityPath = layout.platform === 'win32'
    ? normalizeWindowsExecutable(appPath)
    : appPath;
  const expectedBackendIdentityPath = layout.platform === 'win32'
    ? normalizeWindowsExecutable(expectedBackend)
    : expectedBackend;
  const readBackendIdentity = layout.platform === 'linux'
    ? readLinuxProcessIdentity
    : layout.platform === 'darwin'
      ? readDarwinProcessIdentity
      : readWindowsProcessIdentity;
  const temporaryRoot = fs.realpathSync(
    fs.mkdtempSync(path.join(os.tmpdir(), 'yhc-desktop-smoke-')),
  );
  fs.chmodSync(temporaryRoot, 0o700);
  let environment = makeIsolatedEnvironment(temporaryRoot, process.env, {
    platform: layout.platform,
  });
  const xvfbRun = layout.launcher === 'xvfb'
    ? findCommand('xvfb-run', environment.PATH)
    : null;
  let stderr = '';
  let connection;
  let child;
  let rootIdentity;
  let backendIdentity;
  let activeTurnProvider;
  let activeTurnSessionID = '';
  let activeTurnWorkspace = '';
  let secondChild;
  let secondRootIdentity;
  let failure;
  let passed = false;
  try {
    if (options.crashActiveTurn) {
      const workspaceCandidate = path.join(temporaryRoot, 'workspace');
      fs.mkdirSync(workspaceCandidate, { recursive: true, mode: 0o700 });
      fs.chmodSync(workspaceCandidate, 0o700);
      activeTurnWorkspace = fs.realpathSync(workspaceCandidate);
      activeTurnProvider = await startActiveTurnProvider();
      environment = activeTurnFixtureEnvironment(environment, activeTurnProvider.baseURL);
      activeTurnSessionID = await seedActiveTurnSession({
        backendPath: expectedBackend,
        baseURL: activeTurnProvider.baseURL,
        environment,
        workspace: activeTurnWorkspace,
      });
    }
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
      ...(options.crashActiveTurn ? { cwd: activeTurnWorkspace } : {}),
      detached: layout.platform !== 'win32',
      env: environment,
      stdio: ['ignore', 'ignore', 'pipe'],
      windowsHide: layout.platform === 'win32',
    });
    const exit = observeSpawnedChild(child, {
      requireProcessGroup: layout.platform !== 'win32',
    });
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
    } else if (layout.platform === 'win32') {
      rootIdentity = await poll(
        () => readWindowsProcessIdentity(child.pid),
        RENDERER_TIMEOUT_MS,
        'Windows Desktop process identity',
      );
      if (rootIdentity.executable !== expectedAppIdentityPath) {
        throw new Error('Windows Desktop process identity did not match');
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
    backendIdentity = backend;
    if (
      !backend ||
      String(backend.state || '').startsWith('Z') ||
      backend.executable !== expectedBackendIdentityPath ||
      (layout.platform === 'darwin' && backend.pgid !== child.pid)
    ) {
      throw new Error('packaged backend process identity did not match');
    }
    const crashRequested = options.crashBackend || options.crashActiveTurn;
    if (crashRequested) {
      await installCrashObservers(
        connection,
        renderer.sessionId,
        options.crashActiveTurn ? activeTurnSessionID : '',
      );
      const activeTurnProbe = options.crashActiveTurn
        ? await prepareActiveTurnCrash(
          connection,
          renderer.sessionId,
          activeTurnSessionID,
          activeTurnProvider,
        )
        : null;
      killVerifiedBackend(backend, {
        platform: layout.platform,
        readIdentity: readBackendIdentity,
      });
      await poll(
        () => !originalProcessAlive(backend, readBackendIdentity),
        BACKEND_EXIT_TIMEOUT_MS,
        'crashed backend exit',
      );
      const readCrashContainment = async () => {
        const result = await evaluate(connection, renderer.sessionId, `(async () => {
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
          const timelineText = document.querySelector('#timeline')?.textContent || '';
          const activeTurnID = ${JSON.stringify(activeTurnProbe?.activeTurnID || '')};
          return {
            bootstrapReady: document.documentElement.dataset.yhcBootstrap === 'ready',
            notificationCount: globalThis.__yhcCrashCount,
            backendUnavailable: info === null,
            notice: document.querySelector('#turn-state')?.textContent?.trim() || '',
            status: document.querySelector('#status')?.textContent?.trim() || '',
            checkedControlsDisabled: controls.every((selector) => (
              document.querySelector(selector)?.disabled === true
            )),
            activeSessionID: ${JSON.stringify(activeTurnProbe?.activeSessionID || '')},
            activeTurnID,
            snapshotActiveTurnID: ${JSON.stringify(
              activeTurnProbe?.snapshotActiveTurnID || '',
            )},
            snapshotPartialTurnID: ${JSON.stringify(
              activeTurnProbe?.snapshotPartialTurnID || '',
            )},
            snapshotPartialCount: ${JSON.stringify(
              activeTurnProbe?.snapshotPartialCount || 0,
            )},
            snapshotPartialCompleted: ${JSON.stringify(
              activeTurnProbe?.snapshotPartialCompleted,
            )},
            partialVisible: timelineText.includes(${JSON.stringify(ACTIVE_TURN_PARTIAL)}),
            activePromptVisible: timelineText.includes(${JSON.stringify(ACTIVE_TURN_PROMPT)}),
            interactionHidden: document.querySelector('#interaction-panel')?.hidden === true,
            sessionIdentityVisible:
              (document.querySelector('#session-title')?.textContent || '') ===
                ${JSON.stringify(activeTurnProbe?.sessionTitle || '')} &&
              (document.querySelector('#session-path')?.textContent || '') ===
                ${JSON.stringify(activeTurnProbe?.workspaceLabel || '')},
            terminalEventCount: (globalThis.__yhcTerminalEvents || []).length,
          };
        })()`);
        return result;
      };
      const containmentMatches = options.crashActiveTurn
        ? activeTurnCrashContainmentMatches
        : crashContainmentMatches;
      await poll(async () => {
        const result = await readCrashContainment();
        return containmentMatches(result) ? result : null;
      }, RENDERER_TIMEOUT_MS, options.crashActiveTurn
        ? 'active-turn backend crash containment'
        : 'backend crash containment');
      await observeStable(
        async () => containmentMatches(await readCrashContainment()),
        NO_RESTART_OBSERVATION_MS,
        options.crashActiveTurn
          ? 'active-turn backend crash containment'
          : 'backend crash containment',
      );
      if (options.crashActiveTurn) {
        const counts = activeTurnProvider.requestCounts();
        if (counts.total !== 2 || counts.seed !== 1 || counts.active !== 1) {
          throw new Error('active-turn provider request count did not match');
        }
      }
      await removeCrashObservers(connection, renderer.sessionId);
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
    let appExit;
    let crashCleanup = 'normal-exit';
    try {
      appExit = await closePackagedDesktop(
        connection,
        renderer.sessionId,
        layout.closeStrategy,
        exit,
      );
      connection = null;
    } catch (error) {
      if (!shouldForceCrashCleanup(error, crashRequested)) throw error;
      if (layout.cleanupStrategy === 'windows-tree') {
        await terminateWindowsProcessTree(rootIdentity, {
          expectedBackend: backendIdentity,
        });
      } else {
        await terminateProcessGroup(child.pid, layout.platform === 'darwin'
          ? {
            expectedIdentity: rootIdentity,
            readIdentity: readDarwinProcessIdentity,
          }
          : undefined);
      }
      await deadline(exit, CLEANUP_TIMEOUT_MS, 'Desktop crash cleanup exit');
      connection = null;
      crashCleanup = 'owned-process-tree';
    }
    if (appExit && (appExit.code !== 0 || appExit.signal !== null)) {
      throw new Error(`Desktop exited abnormally (${appExit.code ?? appExit.signal})`);
    }
    if (!crashRequested) {
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
      crash_containment: crashRequested ? 'pass' : 'not_run',
      active_turn_crash: options.crashActiveTurn ? 'pass' : 'not_run',
      window_reopen: options.reopenWindow ? 'pass' : 'not_run',
      crash_cleanup: crashRequested ? crashCleanup : 'not_run',
      no_restart_observation_ms: crashRequested ? NO_RESTART_OBSERVATION_MS : 0,
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
        if (layout.cleanupStrategy === 'windows-tree') {
          await terminateWindowsProcessTree(rootIdentity, {
            expectedBackend: backendIdentity,
          });
        } else {
          await terminateProcessGroup(child.pid, layout.platform === 'darwin'
            ? {
              expectedIdentity: rootIdentity,
              readIdentity: readDarwinProcessIdentity,
            }
            : undefined);
        }
      } catch (cleanupError) {
        failure = combineSmokeFailure(failure, cleanupError);
      }
    }
    if (activeTurnProvider) {
      try {
        await activeTurnProvider.close();
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
    crashActiveTurn: input.crashActiveTurn,
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
  normalizeWindowsExecutable,
  observeStable,
  observeSpawnedChild,
  originalPageTargetClosed,
  parseArguments,
  parseDarwinProcessIdentity,
  parseDarwinScreenLockState,
  parseDevToolsEndpoint,
  parseProcStat,
  parseWindowsProcessIdentity,
  parseWindowsProcessSnapshot,
  readDarwinProcessIdentity,
  readDarwinScreenLockState,
  readWindowsProcessIdentity,
  readWindowsProcessSnapshot,
  requireExecutable,
  resolveUnpackedLayout,
  safeProcessID,
  safeProcessGroupID,
  seedActiveTurnSession,
  sameProcessIdentity,
  selectPageTarget,
  selectReplacementPageTarget,
  shouldForceCrashCleanup,
  startActiveTurnProvider,
  targetID,
  terminateProcessGroup,
  terminateWindowsProcessTree,
  validatePlatformOptions,
  windowsProcessTreeMembers,
};
