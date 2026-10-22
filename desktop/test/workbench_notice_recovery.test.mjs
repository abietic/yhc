import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';
import test from 'node:test';

import { createPendingWorkspaceRetry } from '../../internal/webui/assets/provider_setup.mjs';
import { activateCreatedSession, createSessionCreationGate } from '../../internal/webui/assets/session_creation.mjs';

const source = await readFile(new URL('../../internal/webui/assets/app.mjs', import.meta.url), 'utf8');
function productionBetween(start, end) {
  const first = source.indexOf(start);
  const last = source.indexOf(end, first);
  assert.ok(first >= 0 && last > first, `production boundary: ${start}`);
  return source.slice(first, last);
}
const bind = new Function('$', 'state', 'activeSession', 'dispatch', 'api', 'activateCreatedSession',
  'backendAvailable', 'prepareSessionHydration', 'hydratePreparedSession', 'loadReview', 'inspectorView',
  'sessionCreation', 'surface', 'providerSetupBusy', 'openSheetKind', 'closeSheet', 'pendingWorkspace', 'selectWorkspace', `
  let workbenchNoticeReceipt = null;
  let sessionCreationNoticeReceipt = null;
  ${productionBetween('function showError(error)', 'function handleBackendExit()')}
  ${productionBetween('async function createSessionForWorkspace(', 'async function loadExecutionSettings(')}
  ${productionBetween('function beginSessionCreation()', "$('new-session').onclick")}
  return { beginSessionCreation, createSession, createSessionForWorkspace, showError, dismissWorkbenchNotice };
`);
const failure = new Error('Fixture workspace could not start.');
const created = { id: 'created', workspace_label: 'Fixture' };

function fixture({ existing = false, hydrate = async () => {} } = {}) {
  const nodes = {
    'workbench-notice': { hidden: true },
    'workbench-notice-copy': { textContent: '' },
    'turn-state': { textContent: '' },
  };
  const state = { sessions: existing ? { previous: { id: 'previous', notice: 'Keep previous notice' } } : {}, activeID: existing ? 'previous' : '' };
  const trace = [];
  let create = async () => { throw failure; };
  let select = async () => ({ workspace_handle: 'fixture-capability', workspace_label: 'Fixture' });
  let functions;
  let flight;
  let online = true;
  const retry = createPendingWorkspaceRetry(workspace => functions.createSessionForWorkspace(workspace));
  const gate = createSessionCreationGate(() => functions.createSession());
  functions = bind(id => nodes[id], state, () => state.sessions[state.activeID], action => {
    trace.push(action.type);
    if (action.type === 'SESSION_SELECT') state.activeID = action.id;
    if (action.type === 'SESSION_NOTICE') state.sessions[action.id].notice = action.notice;
  }, async (op) => { assert.equal(op, 'createSession'); return create(); }, activateCreatedSession,
  () => online, summary => { state.sessions[summary.id] = { ...summary, live: true }; }, hydrate, async () => {}, 'activity',
  { begin: () => { flight = gate.begin(); return flight; } }, 'desktop', false, '', () => {}, retry, () => select());
  return {
    nodes, state, trace, functions,
    create: callback => { create = callback; },
    select: callback => { select = callback; },
    offline: () => { online = false; },
    begin() { functions.beginSessionCreation(); return flight; },
  };
}

test('successful workspace activation retires the earlier creation banner', async () => {
  const current = fixture();
  await assert.rejects(current.begin(), /Fixture workspace/);
  assert.equal(current.nodes['workbench-notice'].hidden, false);
  current.create(async () => created);
  await current.begin();
  assert.equal(current.state.activeID, created.id);
  assert.equal(current.nodes['workbench-notice'].hidden, true);
  assert.equal(current.nodes['workbench-notice-copy'].textContent, '');
});

test('a cancelled picker or repeated creation failure does not declare recovery', async () => {
  const current = fixture();
  await assert.rejects(current.begin(), /Fixture workspace/);
  current.select(async () => null);
  await current.begin();
  assert.equal(current.nodes['workbench-notice'].hidden, false);
  assert.equal(current.nodes['workbench-notice-copy'].textContent, failure.message);
  current.select(async () => ({ workspace_handle: 'next-fixture', workspace_label: 'Fixture' }));
  await assert.rejects(current.begin(), /Fixture workspace/);
  assert.equal(current.nodes['workbench-notice'].hidden, false);
});

test('a later unrelated banner survives activation even when it reuses the same Error object', async () => {
  const current = fixture();
  await assert.rejects(current.begin(), /Fixture workspace/);
  let release;
  current.create(() => new Promise(resolve => { release = resolve; }));
  const next = current.begin();
  const settled = Promise.allSettled([next]);
  try {
    await Promise.resolve();
    current.functions.showError(failure);
    assert.equal(current.nodes['workbench-notice'].hidden, false);
  } finally {
    release(created);
    await settled;
  }
  assert.equal((await settled)[0].status, 'fulfilled');
  assert.equal(current.nodes['workbench-notice'].hidden, false);
  assert.equal(current.nodes['workbench-notice-copy'].textContent, failure.message);
});

test('starting a new workspace must not assign its failure to the current conversation', async () => {
  const current = fixture({ existing: true });
  await assert.rejects(current.begin(), /Fixture workspace/);
  assert.equal(current.state.sessions.previous.notice, 'Keep previous notice');
  assert.equal(current.trace.includes('SESSION_NOTICE'), false);
});

test('activation clears only creation failure before hydration can report a distinct problem', async () => {
  let current;
  current = fixture({ hydrate: async () => {
    current.functions.showError(new Error('Fixture history warning'));
  } });
  await assert.rejects(current.begin(), /Fixture workspace/);
  current.create(async () => created);
  await current.begin();
  assert.equal(current.nodes['workbench-notice'].hidden, false);
  assert.equal(current.nodes['workbench-notice-copy'].textContent, 'Fixture history warning');
});

test('a post-creation failure stays scoped to its created session, not a later workspace', async () => {
  let firstHydration = true;
  const current = fixture({ hydrate: async () => {
    if (firstHydration) { firstHydration = false; throw new Error('Fixture review failed'); }
  } });
  current.create(async () => created);
  await assert.rejects(current.begin(), /Fixture review/);
  assert.equal(current.state.sessions.created.notice, 'Fixture review failed');
  current.create(async () => ({ id: 'second-created', workspace_label: 'Fixture' }));
  await current.begin();
  assert.equal(current.nodes['workbench-notice'].hidden, false);
  assert.equal(current.nodes['workbench-notice-copy'].textContent, 'Fixture review failed');
});

test('late success from an unavailable backend does not retire the creation failure', async () => {
  const current = fixture();
  await assert.rejects(current.begin(), /Fixture workspace/);
  let release;
  current.create(() => new Promise(resolve => { release = resolve; }));
  const next = current.begin();
  const settled = Promise.allSettled([next]);
  try {
    await Promise.resolve();
    current.offline();
  } finally {
    release(created);
    await settled;
  }
  assert.equal(current.nodes['workbench-notice'].hidden, false);
});

test('explicit dismissal does not erase the current conversation notice or own a later banner', async () => {
  const current = fixture({ existing: true });
  await assert.rejects(current.begin(), /Fixture workspace/);
  current.functions.dismissWorkbenchNotice();
  assert.equal(current.nodes['workbench-notice'].hidden, true);
  assert.equal(current.nodes['workbench-notice-copy'].textContent, '');
  assert.equal(current.state.sessions.previous.notice, 'Keep previous notice');
  current.functions.showError(new Error('Fixture later warning'));
  current.create(async () => created);
  await current.begin();
  assert.equal(current.nodes['workbench-notice'].hidden, false);
  assert.equal(current.nodes['workbench-notice-copy'].textContent, 'Fixture later warning');
});
