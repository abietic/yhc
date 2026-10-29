import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';
import test from 'node:test';

import { createPendingWorkspaceRetry } from '../../internal/webui/assets/provider_setup.mjs';
import { createSessionCreationGate } from '../../internal/webui/assets/session_creation.mjs';

const source = await readFile(new URL('../../internal/webui/assets/app.mjs', import.meta.url), 'utf8');
const start = source.indexOf('async function retryAfterProviderSetup()');
const end = source.indexOf('async function configureProvider(event)', start);
assert.ok(start >= 0 && end > start, 'execute the production recovery callback');
// Bind only the callback's collaborators, not a second implementation of its
// control flow. Browser acceptance covers the complete provider form wiring.
const bindRecovery = new Function('backendAvailable', 'state', 'pendingWorkspace', 'sessionCreation',
  source.slice(start, end) + '\nreturn retryAfterProviderSetup;');
const oldWorkspace = { workspace_handle: 'retired-backend-handle', workspace_label: 'Project' };
const newWorkspace = { workspace_handle: 'new-backend-handle', workspace_label: 'Project' };

function fixture(selectWorkspace, { online = true, pending = true, failCreation = false } = {}) {
  const trace = [];
  const retry = createPendingWorkspaceRetry(async workspace => {
    trace.push(['create', workspace.workspace_handle]);
    assert.notEqual(workspace.workspace_handle, oldWorkspace.workspace_handle,
      'a new backend cannot accept its predecessor capability');
    if (failCreation) throw new Error('new backend creation failed');
    return { id: 'new-session' };
  });
  if (pending) retry.defer(oldWorkspace);
  const gate = createSessionCreationGate(async () => {
    trace.push(['picker']);
    const selected = await selectWorkspace();
    return selected ? retry.attempt(selected) : null;
  });
  return { retry, gate, trace, recover: bindRecovery(() => online, {}, retry, gate) };
}

test('provider restart obtains a fresh native selection before creating a session', async () => {
  const current = fixture(async () => newWorkspace);
  await current.recover();
  assert.deepEqual(current.trace, [['picker'], ['create', newWorkspace.workspace_handle]]);
  assert.equal(current.retry.pending(), null);
  assert.equal(current.gate.busy(), false);
  const configure = source.slice(source.indexOf('async function configureProvider(event)'), source.indexOf('async function createSessionForWorkspace('));
  assert.match(configure, /if \(!configured\) return;[\s\S]*closeProviderDialog\(\);[\s\S]*await retryAfterProviderSetup\(\)/,
    'fresh selection follows successful setup and closing the credential dialog');
});

test('cancelling the fresh selection retires the old pending capability without a create', async () => {
  const current = fixture(async () => null);
  await current.recover();
  assert.deepEqual(current.trace, [['picker']]);
  assert.equal(current.retry.pending(), null);
  await current.recover();
  assert.deepEqual(current.trace, [['picker']], 'no pending recovery means no unsolicited second picker');
});

test('an offline backend or absent pending selection never starts recovery', async () => {
  for (const options of [{ online: false }, { pending: false }]) {
    const current = fixture(async () => { throw new Error('unexpected native selection'); }, options);
    await current.recover();
    assert.deepEqual(current.trace, []);
    assert.equal(current.gate.busy(), false);
  }
});

test('recovery is single flight while native selection is pending', async () => {
  let select;
  const current = fixture(() => new Promise(resolve => { select = resolve; }));
  const first = current.recover();
  const second = current.recover();
  const settled = Promise.allSettled([first, second]);
  try {
    assert.deepEqual(current.trace, [['picker']]);
    assert.equal(current.gate.busy(), true);
  } finally {
    select?.(newWorkspace);
    await settled;
  }
  assert.equal((await settled).every(result => result.status === 'fulfilled'), true);
  assert.deepEqual(current.trace, [['picker'], ['create', newWorkspace.workspace_handle]]);
  assert.equal(current.gate.busy(), false);
});

test('a failed fresh creation retains only the new backend capability for an explicit retry', async () => {
  const current = fixture(async () => newWorkspace, { failCreation: true });
  await assert.rejects(current.recover(), /new backend creation failed/);
  assert.deepEqual(current.trace, [['picker'], ['create', newWorkspace.workspace_handle]]);
  assert.deepEqual(current.retry.pending(), newWorkspace);
  assert.equal(current.gate.busy(), false);
});

test('a picker failure cannot leave the retired backend capability available', async () => {
  const current = fixture(async () => { throw new Error('native selection unavailable'); });
  await assert.rejects(current.recover(), /native selection unavailable/);
  assert.deepEqual(current.trace, [['picker']]);
  assert.equal(current.retry.pending(), null);
  assert.equal(current.gate.busy(), false);
});

test('provider submission cannot replace the backend while an older creation is in flight', async () => {
  const current = fixture(async () => newWorkspace);
  let release;
  const oldCreation = current.gate.begin(() => new Promise(resolve => { release = resolve; }));
  let configureCalls = 0;
  const controls = new Map([
    ['provider-api-key', { value: 'isolated-test-key' }],
    ['provider-select', { value: 'openai' }],
    ['provider-model', { value: 'default' }],
    ['provider-base-url', { value: '' }],
    ['provider-dialog-status', { textContent: '', classList: { remove() {}, add() {} } }],
  ]);
  const configureStart = source.indexOf('async function configureProvider(event)');
  const configureEnd = source.indexOf('async function createSessionForWorkspace(', configureStart);
  const bindConfigure = new Function('backendAvailable', 'state', 'sessionCreation', 'providerSetupBusy',
    'providerSetup', '$', 'transport', 'providerSetupProjection', 'surface', 'renderProviderSetup',
    'render', 'closeProviderDialog', 'retryAfterProviderSetup',
    source.slice(configureStart, configureEnd) + '\nreturn configureProvider;');
  const configure = bindConfigure(() => true, {}, current.gate, false, { setupAvailable: true },
    id => controls.get(id), { configureProvider: async () => { configureCalls++; return {}; } },
    () => ({}), 'desktop', () => {}, () => {}, () => {}, current.recover);
  const submission = configure({ preventDefault() {} });
  const settled = Promise.allSettled([oldCreation, submission]);
  try {
    assert.equal(configureCalls, 0, 'the old creation must settle before the provider can restart its backend');
    assert.deepEqual(current.retry.pending(), oldWorkspace, 'a blocked submission does not discard recovery intent');
  } finally {
    release();
    await settled;
  }
  assert.equal((await settled).every(result => result.status === 'fulfilled'), true);
  assert.equal(current.gate.busy(), false);
});

test('session entrypoint cannot start a creation while provider submission is in flight', () => {
  const beginStart = source.indexOf('function beginSessionCreation()');
  const beginEnd = source.indexOf("$('new-session').onclick", beginStart);
  const bindBegin = new Function('surface', 'backendAvailable', 'state', 'providerSetupBusy',
    'sessionCreation', 'openSheetKind', 'showError',
    source.slice(beginStart, beginEnd) + '\nreturn beginSessionCreation;');
  let calls = 0;
  const begin = bindBegin('desktop', () => true, {}, true,
    { begin: () => { calls++; return Promise.resolve(); } }, '', () => {});
  begin();
  assert.equal(calls, 0, 'the provider restart must settle before native workspace selection');
});
