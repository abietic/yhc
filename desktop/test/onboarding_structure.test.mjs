import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';
import test from 'node:test';

const asset = (name) => new URL(`../../internal/webui/assets/${name}`, import.meta.url);

test('renderer exposes a semantic Desktop provider setup and reachable history', async () => {
  const html = await readFile(asset('index.html'), 'utf8');
  assert.match(html, /id="provider-settings"/);
  assert.match(html, /id="toggle-session-history"/);
  assert.match(html, /<dialog id="provider-dialog"/);
  assert.match(html, /id="provider-select"/);
  assert.match(html, /id="provider-model"/);
  assert.match(html, /id="provider-base-url"/);
  assert.match(html, /id="provider-api-key"[^>]*type="password"/s);
  assert.match(html, /id="provider-api-key"[^>]*autocomplete="off"/s);
  assert.match(html, /id="provider-form"/);
});

test('renderer keeps provider key DOM-local and clears it after submit', async () => {
  const app = await readFile(asset('app.mjs'), 'utf8');
  assert.match(app, /transport\.getProviderStatus\(\)/);
  assert.match(app, /transport\.configureProvider\(submission\)/);
  assert.match(app, /apiKeyInput\.value = ''/);
  assert.match(app, /executionForDisplay\(session\)/);
  assert.match(app, /createPendingWorkspaceRetry/);
  assert.match(app, /createDurableHistoryLoader/);
  assert.match(app, /shouldDeferWorkspaceForProvider/);
  assert.match(app, /pendingWorkspace\.defer\(workspace\)/);
  assert.doesNotMatch(app, /state\.(?:provider|setup)[^\n]*(?:apiKey|PROV_API_KEY)/);
  assert.doesNotMatch(app, /localStorage[^\n]*(?:apiKey|provider-api-key|PROV_API_KEY)/);
});

test('WebUI does not retain or solicit absolute workspace paths', async () => {
  const [app, state, transport, html, provider] = await Promise.all([
    readFile(asset('app.mjs'), 'utf8'),
    readFile(asset('state.mjs'), 'utf8'),
    readFile(asset('transport.mjs'), 'utf8'),
    readFile(asset('index.html'), 'utf8'),
    readFile(asset('provider_setup.mjs'), 'utf8'),
  ]);
  const renderer = [app, state, transport, html, provider].join('\n');
  assert.doesNotMatch(renderer, /workspace-path|absolute workspace path|id="workspace-dialog"/);
  assert.doesNotMatch(renderer, /\bcwd\b/);
  assert.match(app, /workspace_label/);
  assert.match(app, /workspace_handle/);
  assert.doesNotMatch(transport, /\/v1\/workspaces/);
});

test('saved session selection is history-only and Send uniquely activates it', async () => {
  const app = await readFile(asset('app.mjs'), 'utf8');
  const selectSession = app.slice(
    app.indexOf('async function selectSession(id)'),
    app.indexOf('async function closeSession(session)'),
  );
  const retryProvider = app.slice(
    app.indexOf('async function retryAfterProviderSetup()'),
    app.indexOf('async function configureProvider(event)'),
  );
  const detachedSelection = selectSession.match(
    /if \(!session\.live\) \{([\s\S]*?)\n  \}/,
  )?.[1] || '';
  const updateDraft = app.slice(
    app.indexOf('function updateDraft(value)'),
    app.indexOf('async function send(event)'),
  );
  const send = app.slice(
    app.indexOf('async function send(event)'),
    app.indexOf('async function cancel()'),
  );
  const restore = app.slice(
    app.indexOf('async function restore()'),
    app.indexOf('function completeConfirmation(value)'),
  );

  assert.match(detachedSelection, /await loadTranscript\(id, true\)/);
  assert.doesNotMatch(
    detachedSelection,
    /createSession|snapshot|startStream|loadExecutionSettings/,
  );
  assert.doesNotMatch(
    detachedSelection,
    /openProviderDialog|shouldDeferWorkspaceForProvider/,
  );
  assert.doesNotMatch(retryProvider, /attachTurn|resume|sessionRestorer/);
  assert.equal((app.match(/api\('attachTurn'/g) || []).length, 1);
  assert.match(updateDraft, /attachAttempts\.delete\(current\.id\)/);
  assert.doesNotMatch(app, /attachAttempts\.clear/);
  assert.match(
    send,
    /attachAttempts\.get\(current\.id\)[\s\S]*?attempt\.prompt !== prompt[\s\S]*?crypto\.randomUUID\(\)/,
  );
  assert.match(
    send,
    /await api\('startTurn'[\s\S]*?dispatch\(\{ type: 'SESSION_DRAFT', id: current\.id, draft: '' \}\)/,
  );
  assert.match(
    restore,
    /catalogReady = await catalog\.reset\(''\)[\s\S]*?catalogReady && current\?\.durable && !current\.live[\s\S]*?await loadTranscript\(current\.id, true\)/,
  );
  assert.doesNotMatch(app, /pendingResumeID|resumeDescriptor|createDurableSessionRestorer/);
});

test('Desktop transport alone projects provider configuration capability', async () => {
  const transport = await readFile(asset('transport.mjs'), 'utf8');
  assert.match(transport, /getProviderStatus:\s*\(\)\s*=>\s*bridge\.getProviderStatus\(\)/);
  assert.match(transport, /configureProvider:\s*\(input\)\s*=>\s*bridge\.configureProvider\(input\)/);
  assert.match(transport, /provider setup is available only in the Desktop App/i);
  assert.doesNotMatch(transport, /case ['"]configureProvider['"]/);
});
