import assert from 'node:assert/strict';
import test from 'node:test';

import {
  createPendingWorkspaceRetry,
  executionForDisplay,
  prioritizeSessionRows,
  providerSetupProjection,
  shouldDeferWorkspaceForProvider,
} from '../../internal/webui/assets/provider_setup.mjs';

test('pending workspace survives failure, clears on success, and retries exactly once', async () => {
  const attempted = [];
  let fail = true;
  const workspace = { workspace_handle: 'handle-one', workspace_label: 'One' };
  const retry = createPendingWorkspaceRetry(async (selected) => {
    attempted.push(selected);
    if (fail) throw new Error('provider unavailable');
    return { id: 'session-1' };
  });

  await assert.rejects(retry.attempt(workspace), /provider unavailable/);
  assert.deepEqual(retry.pending(), workspace);

  fail = false;
  await retry.retry();
  assert.deepEqual(attempted, [workspace, workspace]);
  assert.equal(retry.pending(), null);
  assert.equal(await retry.retry(), null);
});

test('pending workspace creation is single-flight and a later clear cannot erase a selection', async () => {
  let release;
  let calls = 0;
  const firstWorkspace = { workspace_handle: 'handle-one', workspace_label: 'One' };
  const secondWorkspace = { workspace_handle: 'handle-two', workspace_label: 'Two' };
  const retry = createPendingWorkspaceRetry((workspace) => {
    calls += 1;
    return new Promise((resolve) => { release = () => resolve({ workspace }); });
  });

  const first = retry.attempt(firstWorkspace);
  const second = retry.attempt(secondWorkspace);
  assert.strictEqual(first, second);
  assert.equal(calls, 1);
  assert.deepEqual(retry.pending(), firstWorkspace);
  release();
  await first;
  assert.equal(retry.pending(), null);
  assert.throws(() => createPendingWorkspaceRetry(null), TypeError);
});

test('provider setup can defer a selected workspace without creating a live session', async () => {
  let calls = 0;
  const retry = createPendingWorkspaceRetry(async () => { calls += 1; });

  const workspace = { workspace_handle: 'handle-first', workspace_label: 'First' };
  assert.deepEqual(retry.defer(workspace), workspace);
  assert.deepEqual(retry.pending(), workspace);
  assert.equal(calls, 0);
  await retry.retry();
  assert.equal(calls, 1);
  assert.equal(retry.pending(), null);
});

test('provider projection contains public setup metadata only', () => {
  const projection = providerSetupProjection('desktop', {
    configured: true,
    secureStorageAvailable: false,
    provider: ' openai ',
    model: ' gpt-test ',
    baseURL: 'https://secret@example.test/v1?api_key=not-public#fragment',
    errorCode: ' storage_unavailable ',
    apiKey: 'must-not-escape',
    unknown: 'must-not-escape',
  });

  assert.deepEqual(projection, {
    setupAvailable: true,
    hostGuidance: false,
    configured: true,
    launchReady: true,
    secureStorageAvailable: false,
    submitDisabled: true,
    provider: 'openai',
    model: 'gpt-test',
    baseURL: 'https://example.test/v1',
    errorCode: 'storage_unavailable',
  });
  assert.equal(JSON.stringify(projection).includes('must-not-escape'), false);
  assert.equal(JSON.stringify(projection).includes('apiKey'), false);
});

test('desktop defers workspace creation only when no managed or ambient launch is ready', () => {
  const missing = providerSetupProjection('desktop', {
    configured: false,
    launchReady: false,
  });
  const ambient = providerSetupProjection('desktop', {
    configured: false,
    launchReady: true,
  });

  assert.equal(shouldDeferWorkspaceForProvider('desktop', missing), true);
  assert.equal(shouldDeferWorkspaceForProvider('desktop', ambient), false);
  assert.equal(shouldDeferWorkspaceForProvider('web', missing), false);
});

test('web projection offers host guidance rather than provider setup', () => {
  assert.deepEqual(providerSetupProjection('web', {
    configured: true,
    provider: 'openai',
  }), {
    setupAvailable: false,
    hostGuidance: true,
    configured: false,
    launchReady: false,
    secureStorageAvailable: true,
    submitDisabled: true,
    provider: '',
    model: '',
    baseURL: '',
    errorCode: '',
  });
});

test('only a live session exposes execution settings for display', () => {
  const execution = { model: 'available' };
  assert.strictEqual(executionForDisplay({ live: true, execution }), execution);
  assert.equal(executionForDisplay({ live: false, execution }), null);
  assert.equal(executionForDisplay({ execution }), null);
});

test('session rows preserve newest-first ordering while hiding only offline history', () => {
  const sessions = [
    { id: 'old-archived', updated_at: '2026-08-01T00:00:00Z', archived: true },
    { id: 'new-live', updated_at: '2026-08-05T00:00:00Z', live: true },
    { id: 'middle-saved', updated_at: '2026-08-04T00:00:00Z', resumable: true },
    { id: 'old-saved', updated_at: '2026-08-02T00:00:00Z', resumable: true },
  ];

  const compact = prioritizeSessionRows(sessions, { limit: 2 });
  assert.deepEqual(compact.visible.map((session) => session.id), ['new-live', 'middle-saved']);
  assert.equal(compact.hiddenCount, 2);
  assert.deepEqual(sessions.map((session) => session.id), [
    'old-archived', 'new-live', 'middle-saved', 'old-saved',
  ]);

  const revealed = prioritizeSessionRows(sessions, { historyExpanded: true, limit: 2 });
  assert.deepEqual(revealed.visible.map((session) => session.id), [
    'new-live', 'middle-saved', 'old-saved', 'old-archived',
  ]);
  assert.equal(revealed.hiddenCount, 0);

  const searched = prioritizeSessionRows(sessions, { query: 'saved', limit: 1 });
  assert.equal(searched.visible.length, 4);
  assert.equal(searched.hiddenCount, 0);

  const mixed = prioritizeSessionRows([
    { id: 'old-live', updated_at: '2026-08-01T00:00:00Z', live: true },
    { id: 'new-saved', updated_at: '2026-08-06T00:00:00Z', resumable: true },
  ], { limit: 2 });
  assert.deepEqual(mixed.visible.map((session) => session.id), ['new-saved', 'old-live']);
});
