import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';
import test from 'node:test';
import { runInNewContext } from 'node:vm';

const asset = (name) => new URL(`../../internal/webui/assets/${name}`, import.meta.url);

test('provider dialog preserves every supported saved selection and never preloads a key', async () => {
  const app = await readFile(asset('app.mjs'), 'utf8');
  const start = app.indexOf('function openProviderDialog(');
  const end = app.indexOf('function closeProviderDialog(', start);
  assert.ok(start >= 0 && end > start);
  const providers = ['anthropic', 'openai', 'google', 'deepseek', 'glm', 'qwen', 'ark'];
  for (const provider of [...providers, 'unknown-provider']) {
    const nodes = new Map();
    const node = id => {
      if (!nodes.has(id)) nodes.set(id, {
        value: '', options: providers.map(value => ({ value })), open: false,
        classList: { toggle() {} }, focus() {}, showModal() { this.open = true; },
      });
      return nodes.get(id);
    };
    node('provider-api-key').value = 'stale-test-input';
    runInNewContext(`${app.slice(start, end)}\nopenProviderDialog();`, {
      $: node, state: {}, sessionCreation: { busy: () => false },
      backendAvailable: () => true,
      providerSetup: { setupAvailable: true, configured: true, provider,
        model: 'fixture-model', baseURL: 'https://fixture.example.invalid/' },
      providerSetupMessage: () => '', renderProviderSetup() {},
      requestAnimationFrame: callback => callback(),
      api() { assert.fail('Opening provider setup must not perform an API operation.'); },
    });
    assert.equal(node('provider-select').value, providers.includes(provider) ? provider : 'openai', provider);
    assert.equal(node('provider-model').value, 'fixture-model');
    assert.equal(node('provider-base-url').value, 'https://fixture.example.invalid/');
    assert.equal(node('provider-api-key').value, '');
    assert.equal(node('provider-dialog').open, true);
  }
});

test('renderer exposes a semantic Desktop provider setup and reachable history', async () => {
  const html = await readFile(asset('index.html'), 'utf8');
  assert.match(html, /class="mark"[^>]*>Y<\/span>/);
  assert.match(html, /class="empty workbench-home"[^>]*aria-labelledby="empty-title"/);
  assert.match(html, /id="home-sessions"/);
  assert.doesNotMatch(html, /class="(?:empty-)?mark"[^>]*>E<\/span>/);
  assert.match(html, /id="provider-settings"/);
  assert.match(html, /id="toggle-session-history"/);
  assert.match(html, /<dialog id="provider-dialog"/);
  assert.match(html, /id="provider-select"/);
  assert.match(html, /id="provider-model"/);
  assert.match(html, /id="provider-base-url"/);
  assert.match(html, /id="provider-api-key"[^>]*type="password"/s);
  assert.match(html, /id="provider-api-key"[^>]*autocomplete="off"/s);
  assert.match(html, /id="provider-form"/);
  assert.match(html, /id="settings-open"[^>]*type="button"/);
  assert.match(html, /<dialog id="settings-dialog"[^>]*aria-labelledby="settings-title"/);
  assert.match(html, /id="settings-close"[^>]*aria-label="Close settings"/);
  assert.match(html, /id="settings-search"[^>]*type="search"/);
  assert.match(html, /id="settings-scope"/);
  assert.match(html, /id="settings-status"[^>]*role="status"/);
});

test('Web onboarding offers browsing and does not expose a dead new-session action', async () => {
  const app = await readFile(asset('app.mjs'), 'utf8');
  const creationControls = app.slice(
    app.indexOf('function renderSessionCreationControls()'),
    app.indexOf('function option('),
  );
  const empty = app.slice(
    app.indexOf('function renderEmptyState()'),
    app.indexOf('function sessionDotClass('),
  );
  assert.match(creationControls, /button\.hidden = surface === 'web'/);
  assert.match(creationControls, /button\.disabled = creating \|\| providerSetupBusy \|\| !backendAvailable\(state\)/);
  assert.match(empty, /workspaceButton\.hidden = surface === 'web'/);
  assert.match(empty, /browseButton\.hidden = surface !== 'web'/);
  assert.match(empty, /openSheet\('navigation', trigger\)/);
  assert.match(empty, /\$\('session-filter'\)\.focus\(\)/);
});

test('a healthy welcome screen reports its connection and saved sessions explain Send', async () => {
  const [app, html] = await Promise.all([
    readFile(asset('app.mjs'), 'utf8'),
    readFile(asset('index.html'), 'utf8'),
  ]);
  assert.match(app, /!surface \? 'Connecting' : backendReady \? 'Connected' : 'Offline'/);
  assert.match(app, /Send a message to continue this saved session\./);
  assert.doesNotMatch(app, /Select this saved session to resume it\./);
  assert.match(html, /id="workbench-notice"[^>]*role="alert"/);
  assert.match(html, /id="prompt"[^>]*aria-label=/s);
});

test('renderer keeps provider key DOM-local and clears it after submit', async () => {
  const app = await readFile(asset('app.mjs'), 'utf8');
  assert.match(app, /transport\.getProviderStatus\(\)/);
  assert.match(app, /transport\.configureProvider\(submission\)/);
  assert.match(app, /apiKeyInput\.value = ''/);
  assert.match(app, /executionForDisplay\(session\)/);
  assert.match(app, /createPendingWorkspaceRetry/);
  assert.match(app, /createSessionCreationGate\(createSession/);
  assert.match(app, /sessionCreation\.begin\(\)/);
  assert.match(app, /button\.disabled = creating/);
  assert.match(
    app,
    /workspaceButton\.disabled = !backendReady \|\| providerSetupBusy \|\| sessionCreation\.busy\(\)/,
  );
  assert.match(app, /createDurableHistoryLoader/);
  assert.match(app, /pendingWorkspace\.attempt\(workspace\)/);
  assert.doesNotMatch(app, /shouldDeferWorkspaceForProvider/);
  assert.doesNotMatch(app, /state\.(?:provider|setup)[^\n]*(?:apiKey|PROV_API_KEY)/);
  assert.doesNotMatch(app, /localStorage[^\n]*(?:apiKey|provider-api-key|PROV_API_KEY)/);
});

test('workspace creation lets the runtime resolve shared configuration before requesting credentials', async () => {
  const app = await readFile(asset('app.mjs'), 'utf8');
  const create = app.slice(
    app.indexOf('async function createSession()'),
    app.indexOf('async function loadExecutionSettings('),
  );
  assert.match(create, /await selectWorkspace\(\)/);
  assert.match(create, /pendingWorkspace\.attempt\(workspace\)/);
  assert.doesNotMatch(create, /providerSetup|openProviderDialog|pendingWorkspace\.defer/);
});

test('new Desktop sessions activate before target-scoped hydration', async () => {
  const app = await readFile(asset('app.mjs'), 'utf8');
  const create = app.slice(
    app.indexOf('async function createSessionForWorkspace(workspace)'),
    app.indexOf('async function createSession()'),
  );
  const synchronize = app.slice(
    app.indexOf('function prepareSessionHydration(summary'),
    app.indexOf('async function restore()'),
  );

  assert.match(create, /activateCreatedSession\(summary/);
  assert.match(
    create,
    /prepareSessionHydration\(created, '', false\)[\s\S]*?SESSION_SELECT[\s\S]*?hydratePreparedSession\(created\)/,
  );
  assert.doesNotMatch(synchronize, /SESSION_SELECT/);
  assert.match(synchronize, /status: 'restoring'/);
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
    /for \(const descriptor of saved\.sessions\)[\s\S]*?unverifiedPersistedDescriptor\(descriptor\)[\s\S]*?await api\('listSessions'\)/,
  );
  assert.match(
    restore,
    /catalogReady = backendAvailable\(state\)[\s\S]*?await catalog\.reset\(''\)[\s\S]*?catalogReady && current\?\.durable && !current\.live[\s\S]*?await loadTranscript\(current\.id, true\)/,
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
