const assert = require('node:assert/strict');
// Optional renderer acceptance. This script is not part of the default test gate.
// Install Playwright separately or set PLAYWRIGHT_MODULE_PATH to an existing
// module. YHC_UI_BROWSER_EXECUTABLE optionally selects an installed browser.
// Usage: node desktop/scripts/workbench_ui_acceptance.cjs --output-dir DIR
// Fixtures model the public configuration schema, not a live Go/provider service.
const { chromium } = require(process.env.PLAYWRIGHT_MODULE_PATH || 'playwright');
const http = require('node:http');
const fs = require('node:fs/promises');
const path = require('node:path');
const os = require('node:os');
const { listenFixtureServer } = require('./fixture_server_startup.cjs');

const root = path.resolve(__dirname, '../../internal/webui/assets');
const results = [];
const errors = [];
const streams = new Map();
const heldReads = [];
const heldWrites = [];
let holdNextSettingsRead = false;
let holdNextSettingsWrite = false;
let failNextSettingsRead = false;
let failNextSettingsWrite = false;
let conflictNext = false;
let revision = 1;
const stored = { theme: 'daybreak', provider: 'openai' };
const projectStored = {};
const settingsWrites = [];
let creations = 0;
let sessionSettingWrites = 0;
let turnRequests = 0;
const session = { id: 'acceptance-session', title: 'Improve the search experience', workspace_label: 'demo-workspace', status: 'idle', active_turn_id: '', resumable: true };
const secondSession = { ...session, id: 'acceptance-second-session', title: 'Review the keyboard flow' };
const messages = [
  { id: 'user-1', role: 'user', content: 'Make search fast and easy to understand.', completed: true, turn_id: 'old-turn' },
  { id: 'assistant-1', role: 'assistant', content: '## A clearer search experience\n\nThe search flow should preserve your context and show useful results immediately.\n\n- Search by session title or workspace.\n- Keep your draft when changing settings.\n- Show a useful empty state.\n\n```js\nconst matches = sessions.filter(matchesQuery);\n```', reasoning_content: 'I checked the current interaction and selected a small, testable improvement.', completed: true, turn_id: 'old-turn' },
];
// Keep this bounded, secret-free fixture aligned with
// engine/config/workbench_settings.go:workbenchSettingDefinitions.
const definitions = {
  provider: { label: 'Provider', type: 'string', group: 'model', description: 'Default provider; launch flags and environment may override this value.', choice_only: true, options: ['anthropic', 'openai', 'google', 'deepseek', 'glm', 'qwen', 'ark'], default: '' },
  model: { label: 'Model', type: 'string', group: 'model', description: 'Default model for new sessions. Use a provider model identifier or configured alias.', default: 'default' },
  model_profile: { label: 'Model profile', type: 'string', group: 'model', description: 'Select an existing user-owned model profile; cannot be combined with legacy provider fields.', choice_only: true, default: '' },
  api_base_url: { label: 'API endpoint', type: 'string', group: 'model', description: 'Optional HTTP(S) provider endpoint, without credentials, query parameters, or fragment.', default: '' },
  max_turns: { label: 'Maximum turns', type: 'integer', group: 'execution', description: 'Maximum agent turns per interaction; zero means unlimited.', default: 0 },
  permission_mode: { label: 'Permission mode', type: 'string', group: 'execution', description: 'Default permission mode for new sessions. Bypass is available only through explicitly confirmed execution controls.', choice_only: true, options: ['default', 'plan', 'acceptEdits', 'dontAsk', 'auto'], default: 'default' },
  theme: { label: 'Theme', type: 'string', group: 'appearance', description: 'Shared theme preference for the next application or terminal session.', choice_only: true, options: ['polar-night', 'daybreak', 'dark-ansi', 'light-ansi', 'snowy', 'aubergine', 'dark', 'light'], aliases: { dark: 'polar-night', light: 'daybreak' }, default: 'dark' },
  reduced_motion: { label: 'Reduced motion', type: 'boolean', group: 'appearance', description: 'Disable non-essential animation.', default: false },
  prompt_suggestions: { label: 'Prompt suggestions', type: 'boolean', group: 'appearance', description: 'Enable model-backed next-prompt suggestions in supported clients.', default: true },
};
definitions.model_profile.conflicts_with = ['provider', 'model', 'api_base_url'];
for (const key of definitions.model_profile.conflicts_with) definitions[key].conflicts_with = ['model_profile'];
function settings(scope = 'user') {
  const selected = scope === 'project' ? projectStored : stored;
  return { scope, revision: String(revision).padStart(64, '0'), fields: Object.fromEntries(
    Object.entries(definitions).map(([key, definition]) => [key, {
      ...definition,
      stored_value: Object.hasOwn(selected, key) ? selected[key] : null,
      effective_value: Object.hasOwn(projectStored, key) ? projectStored[key] : Object.hasOwn(stored, key) ? stored[key] : definition.default,
      source: Object.hasOwn(projectStored, key) ? 'project' : Object.hasOwn(stored, key) ? 'user' : 'default',
      overridden: scope === 'user' && Object.hasOwn(projectStored, key),
      apply: 'new_session',
    }]),
  ) };
}
function operation(op, input = {}, conversation = false) {
  if (op === 'createSession') { creations++; return session; }
  if (op === 'startTurn') { turnRequests++; return { turn_id: 'fixture-turn' }; }
  if (op === 'listSessions') return { sessions: conversation ? [session, secondSession] : [] };
  if (op === 'listDurableSessions') return { sessions: [], has_more: false };
  if (op === 'transcriptPage') return { entries: [], has_more: false };
  if (op === 'snapshot') return { session: input.sessionID === secondSession.id ? secondSession : session, event_cursor: 1, messages, interactions: [], activity: [] };
  if (op === 'getExecutionSettings') return { model: 'default', models: [{ selector: 'default', display_name: 'Default model', provider: 'openai' }], reasoning_effort: 'default', reasoning_effort_options: ['default'], permission_mode: 'default', permission_mode_options: ['default', 'acceptEdits'] };
  if (op === 'getSettings') return settings(input.scope);
  if (op === 'updateSettings') {
    settingsWrites.push(structuredClone(input));
    if (failNextSettingsWrite) {
      failNextSettingsWrite = false;
      return { fixtureError: true, status: 503, code: 'fixture_unavailable', message: 'Fixture settings write is unavailable.' };
    }
    if (conflictNext || input.revision !== String(revision).padStart(64, '0')) {
      conflictNext = false;
      return { fixtureError: true, code: 'settings_conflict', message: 'Settings changed in another client. Reload before saving.', status: 409 };
    }
    const selected = input.scope === 'project' ? projectStored : stored;
    for (const [key, value] of Object.entries(input.changes || {})) {
      if (!Object.hasOwn(definitions, key)) throw new Error('Unknown fixture setting');
      if (value === null) delete selected[key];
      else selected[key] = value;
    }
    revision++;
    return settings(input.scope);
  }
  if (op === 'reviewDiff') return { sources: [], generated_at: '2026-09-05T00:00:00Z' };
  throw new Error('Unexpected fixture operation: ' + op);
}
async function readBody(req) {
  let input = '';
  for await (const chunk of req) input += chunk;
  return input ? JSON.parse(input) : {};
}
function reply(res, body) {
  const status = body?.fixtureError ? body.status : 200;
  if (!res.headersSent) res.writeHead(status, { 'Content-Type': 'application/json', 'Cache-Control': 'no-store' });
  res.end(JSON.stringify(body?.fixtureError ? { error: { code: body.code, message: body.message } } : body));
}
async function check(name, action) {
  try { await action(); results.push({ name, status: 'pass' }); console.log('PASS ' + name); }
  catch (error) { results.push({ name, status: 'fail', error: error.message }); console.log('FAIL ' + name + ': ' + error.message); }
}
async function focusIs(page, id) {
  try { await page.waitForFunction(expected => document.activeElement.id === expected, id); }
  catch {
    const observed = await page.evaluate(() => {
      const close = document.querySelector('#inspector-close');
      const css = getComputedStyle(close);
      const inspectorCSS = getComputedStyle(document.querySelector('#inspector'));
      return { active: document.activeElement.id, navigationInert: document.querySelector('#navigation').inert, conversationInert: document.querySelector('#conversation').inert, inspectorInert: document.querySelector('#inspector').inert, navigationExpanded: document.querySelector('#navigation-toggle').getAttribute('aria-expanded'), inspectorExpanded: document.querySelector('#inspector-toggle').getAttribute('aria-expanded'), closeDisplay: css.display, closeVisibility: css.visibility, inspectorVisibility: inspectorCSS.visibility, closeRect: close.getBoundingClientRect().toJSON() };
    });
    throw new Error(`Expected focus ${id}; observed ${JSON.stringify(observed)}`);
  }
}
async function waitForHeldRead(queue = heldReads) {
  for (let attempt = 0; attempt < 100; attempt++) {
    if (queue.length) return;
    await new Promise(resolve => setTimeout(resolve, 10));
  }
  throw new Error('The expected settings request was not received');
}
function releaseReads() {
  for (const { res, body } of heldReads.splice(0)) reply(res, body);
  for (const { res, body } of heldWrites.splice(0)) reply(res, body);
}
async function main() {
  const args = process.argv.slice(2);
  if (args.length && (args.length !== 2 || args[0] !== '--output-dir')) {
    throw new Error('Usage: node desktop/scripts/workbench_ui_acceptance.cjs [--output-dir DIR]');
  }
  const outputDir = args.length
    ? path.resolve(args[1])
    : await fs.mkdtemp(path.join(os.tmpdir(), 'yhc-workbench-ui-acceptance-'));
  await fs.mkdir(outputDir, { recursive: true });
  const server = http.createServer(async (req, res) => {
    const url = new URL(req.url, 'http://localhost');
    const conversation = String(req.headers.cookie || '').includes('fixture=conversation');
    try {
      if (url.pathname === '/fixture/rpc') {
        const input = await readBody(req);
        const body = operation(input.op, input.input, conversation);
        if (input.op === 'updateSettings' && holdNextSettingsWrite) {
          holdNextSettingsWrite = false; heldWrites.push({ res, body }); return;
        }
        reply(res, body); return;
      }
      if (url.pathname === '/v1/auth/browser-session') { reply(res, { protocol_version: 2, csrf_token: 'isolated-fixture-csrf' }); return; }
      if (url.pathname === '/v1/settings') {
        const body = operation(req.method === 'PATCH' ? 'updateSettings' : 'getSettings', { ...Object.fromEntries(url.searchParams), ...await readBody(req) }, conversation);
        if (req.method === 'PATCH' && holdNextSettingsWrite) {
          holdNextSettingsWrite = false; heldWrites.push({ res, body }); return;
        }
        if (req.method === 'GET' && holdNextSettingsRead) {
          holdNextSettingsRead = false;
          res.writeHead(200, { 'Content-Type': 'application/json', 'Cache-Control': 'no-store' });
          res.flushHeaders();
          heldReads.push({ res, body }); return;
        }
        if (req.method === 'GET' && failNextSettingsRead) {
          failNextSettingsRead = false;
          reply(res, { fixtureError: true, status: 503, code: 'fixture_unavailable', message: 'Fixture settings are temporarily unavailable.' }); return;
        }
        reply(res, body); return;
      }
      if (url.pathname === '/v1/sessions') { reply(res, operation('listSessions', {}, conversation)); return; }
      if (url.pathname === '/v1/durable-sessions') { reply(res, operation('listDurableSessions')); return; }
      if (url.pathname.endsWith('/events')) {
        res.writeHead(200, { 'Content-Type': 'text/event-stream', 'Cache-Control': 'no-cache', Connection: 'keep-alive' });
        res.write(': connected\n\n'); streams.set(res, url.pathname.split('/').at(-2)); req.on('close', () => streams.delete(res)); return;
      }
      const end = url.pathname.split('/').at(-1);
      if (end === 'execution-settings' && req.method === 'PATCH') sessionSettingWrites++;
      const mapped = { transcript: 'transcriptPage', snapshot: 'snapshot', 'execution-settings': 'getExecutionSettings', 'review-diff': 'reviewDiff', turns: 'startTurn' }[end];
      if (mapped) { reply(res, operation(mapped, { sessionID: url.pathname.split('/').at(-2) })); return; }
      if (url.pathname === '/favicon.ico') { res.writeHead(204).end(); return; }
      const file = path.join(root, url.pathname === '/' ? 'index.html' : url.pathname);
      res.setHeader('Content-Type', /\.m?js$/.test(file) ? 'text/javascript' : file.endsWith('.css') ? 'text/css' : 'text/html');
      res.end(await fs.readFile(file));
    } catch (error) { res.writeHead(500).end(String(error.message)); }
  });
  let base;
  let browser;
  const screenshot = async (page, name) => {
    await page.evaluate(() => new Promise(resolve => requestAnimationFrame(() => requestAnimationFrame(resolve))));
    return page.screenshot({ path: path.join(outputDir, `${name}.png`), animations: 'disabled' });
  };
  async function createPage(surface, conversation = false, noVault = false, savedProvider = 'openai') {
    const context = await browser.newContext({ viewport: { width: 1440, height: 1000 }, colorScheme: 'light' });
    if (conversation) await context.addCookies([{ name: 'fixture', value: 'conversation', url: base }]);
    const page = await context.newPage();
    page.setDefaultTimeout(6000);
    page.on('pageerror', error => errors.push(error.message));
    if (surface === 'desktop') await page.addInitScript(({ noVault, savedProvider }) => {
      window.yhcDesktop = {
        getInfo: async () => ({ protocolVersion: 2, webAvailable: true }),
        getProviderStatus: async () => ({ configured: !noVault, provider: noVault ? '' : savedProvider, model: 'default', launchReady: !noVault, secureStorageAvailable: true }),
        configureProvider: async (input) => ({ configured: true, provider: input.provider, model: input.model, launchReady: true, secureStorageAvailable: true }),
        api: async (op, input) => {
          const response = await fetch('/fixture/rpc', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ op, input }) });
          const body = await response.json();
          if (!response.ok) { const error = new Error(body.error.message); error.code = body.error.code; throw error; }
          return body;
        },
        apiResult: async (op, input) => {
          const response = await fetch('/fixture/rpc', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ op, input }) });
          const body = await response.json();
          return response.ok
            ? { ok: true, value: body }
            : { ok: false, error: { message: body.error.message, code: body.error.code, status: response.status } };
        },
        onEventStream: () => () => {}, onBackendExit: () => () => {},
        startEvents: async () => ({ status: 'open' }), stopEvents: async () => {},
        selectWorkspace: async () => noVault ? ({ workspace_handle: 'isolated-fixture-workspace', workspace_label: 'demo-workspace' }) : null, openWeb: async () => {},
      };
    }, { noVault, savedProvider });
    await page.goto(base);
    await page.waitForFunction(() => document.documentElement.dataset.yhcBootstrap === 'ready');
    return page;
  }
  try {
    await listenFixtureServer(server);
    base = `http://127.0.0.1:${server.address().port}`;
    browser = await chromium.launch({
      headless: true,
      ...(process.env.YHC_UI_BROWSER_EXECUTABLE
        ? { executablePath: process.env.YHC_UI_BROWSER_EXECUTABLE }
        : {}),
    });
    const desktop = await createPage('desktop');
    await check('Desktop welcome is connected without a disabled composer', async () => {
      assert.equal(await desktop.locator('#status').textContent(), 'Connected');
      assert.equal(await desktop.locator('#composer').isVisible(), false);
      assert.equal(await desktop.locator('#new-session').isVisible(), true);
      await screenshot(desktop, 'home');
    });
    await check('Wide inspector toggles without modal ownership or blocked conversation', async () => {
      assert.equal(await desktop.locator('#inspector').isVisible(), false);
      await desktop.locator('#inspector-toggle').click();
      assert.equal(await desktop.locator('#inspector').isVisible(), true);
      assert.equal(await desktop.locator('#inspector').getAttribute('aria-modal'), null);
      assert.equal(await desktop.locator('#conversation').evaluate(node => node.inert), false);
      assert.equal(await desktop.locator('#navigation').evaluate(node => node.inert), false);
      await desktop.locator('#inspector-toggle').click();
      assert.equal(await desktop.locator('#inspector').isVisible(), false);
    });
    await check('Narrow sheets own focus and Escape returns to the trigger', async () => {
      await desktop.setViewportSize({ width: 390, height: 844 });
      await desktop.locator('#navigation-toggle').click();
      await focusIs(desktop, 'navigation-close');
      assert.equal(await desktop.locator('#conversation').evaluate(node => node.inert), true);
      await screenshot(desktop, 'narrow');
      await desktop.keyboard.press('Escape');
      await focusIs(desktop, 'navigation-toggle');
      await desktop.locator('#inspector-toggle').click();
      await focusIs(desktop, 'inspector-close');
      assert.equal(await desktop.locator('#inspector').getAttribute('aria-modal'), 'true');
      await desktop.keyboard.press('Escape');
      await focusIs(desktop, 'inspector-toggle');
      await desktop.setViewportSize({ width: 1440, height: 1000 });
    });
    await desktop.setViewportSize({ width: 1440, height: 1000 });
    await desktop.keyboard.press('Escape');
    await check('Settings load, search, save shared values and apply appearance', async () => {
      await desktop.locator('#settings-open').click();
      await desktop.locator('#setting-model').waitFor();
      await desktop.locator('#settings-search').fill('does-not-exist');
      assert.equal(await desktop.locator('.settings-no-results').isVisible(), true);
      await desktop.locator('#settings-search').fill('appearance');
      assert.equal(await desktop.locator('[data-setting="model"]').isVisible(), false);
      await desktop.locator('#settings-search').fill('');
      await desktop.locator('#setting-theme').selectOption('polar-night');
      await desktop.locator('#settings-save').click();
      await desktop.waitForFunction(() => document.querySelector('#settings-status').textContent.startsWith('Saved.'));
      assert.equal(await desktop.locator('html').getAttribute('data-theme'), 'polar-night');
      assert.equal(await desktop.locator('#settings-save').isDisabled(), true);
      await desktop.locator('#settings-dialog').evaluate(node => { node.scrollTop = 0; });
      await screenshot(desktop, 'settings');
    });
    await check('Revision conflict preserves edited values and requires reload', async () => {
      conflictNext = true;
      await desktop.locator('#setting-model').fill('unsaved-fixture-model');
      await desktop.locator('#settings-save').click();
      await desktop.waitForFunction(() => document.querySelector('#settings-status').textContent.includes('another client'));
      assert.equal(await desktop.locator('#setting-model').inputValue(), 'unsaved-fixture-model');
      assert.equal(await desktop.locator('#settings-save').isDisabled(), true);
      await desktop.locator('#settings-close').click();
      await desktop.locator('#confirm-accept').click();
      await desktop.waitForFunction(() => !document.querySelector('#settings-dialog').open);
    });
    const savedGLM = await createPage('desktop', false, false, 'glm');
    await check('Saved GLM credentials stay selected when opened from shared settings and can be saved', async () => {
      await savedGLM.locator('#settings-open').click();
      await savedGLM.locator('#setting-model').waitFor();
      await savedGLM.locator('#settings-credentials summary').click();
      await savedGLM.locator('#settings-credentials-open').click();
      await savedGLM.locator('#provider-dialog[open]').waitFor();
      assert.equal(await savedGLM.locator('#provider-select').inputValue(), 'glm');
      await savedGLM.locator('#provider-api-key').fill('fixture-only-key');
      await savedGLM.locator('#provider-save').click();
      await savedGLM.waitForFunction(() => !document.querySelector('#provider-dialog').open);
      await savedGLM.locator('#provider-settings').click();
      await savedGLM.locator('#provider-dialog[open]').waitFor();
      assert.equal(await savedGLM.locator('#provider-select').inputValue(), 'glm');
      await savedGLM.locator('#provider-cancel').click();
    });
    await savedGLM.close();
    await check('Integer settings reject blank, negative and fractional values before saving', async () => {
      await desktop.locator('#settings-open').click();
      await desktop.getByRole('button', { name: 'Agent behavior', exact: true }).click();
      await desktop.locator('#setting-max_turns').waitFor();
      await desktop.locator('#setting-max_turns').fill('3');
      const before = revision;
      for (const invalid of ['', '-1', '1.5']) {
        await desktop.locator('#setting-max_turns').fill(invalid);
        assert.equal(await desktop.locator('#setting-max_turns').evaluate(node => node.checkValidity()), false);
        await desktop.locator('#settings-save').click();
        assert.equal(revision, before);
      }
      await desktop.locator('#setting-max_turns').fill('4');
      await desktop.locator('#settings-save').click();
      await desktop.waitForFunction(() => document.querySelector('#settings-status').textContent.startsWith('Saved.'));
      assert.equal(stored.max_turns, 4);
      await desktop.locator('#settings-close').click();
      await desktop.waitForFunction(() => !document.querySelector('#settings-dialog').open);
    });
    const web = await createPage('web');
    await check('Web welcome exposes browsing instead of a dead new-session button', async () => {
      assert.equal(await web.locator('#new-session').isVisible(), false);
      await web.getByRole('button', { name: 'Browse sessions', exact: true }).click();
      assert.equal(await web.evaluate(() => document.activeElement.id), 'session-filter');
      await screenshot(web, 'web-home');
    });
    await check('Searching settings is safe while the read is pending or has failed', async () => {
      holdNextSettingsRead = true;
      await web.locator('#settings-open').click();
      await waitForHeldRead();
      await web.locator('#settings-search').fill('theme');
      releaseReads();
      await web.locator('#setting-theme').waitFor();
      assert.equal(await web.locator('[data-setting="model"]').isVisible(), false);
      failNextSettingsRead = true;
      await web.locator('#settings-reload').click();
      await web.waitForFunction(() => document.querySelector('#settings-status').textContent.includes('Could not load'));
      await web.locator('#settings-search').fill('model');
      await web.locator('#settings-close').click();
      await web.waitForFunction(() => !document.querySelector('#settings-dialog').open);
    });
    const settingsReadTimeout = await createPage('web');
    await check('Settings read timeout aborts without a write and a late response cannot refill the editor', async () => {
      await settingsReadTimeout.clock.install();
      const writesBefore = settingsWrites.length;
      holdNextSettingsRead = true;
      try {
        await settingsReadTimeout.locator('#settings-open').click();
        await waitForHeldRead();
        await settingsReadTimeout.clock.fastForward(15_000);
        await settingsReadTimeout.waitForFunction(() => document.querySelector('#settings-status').textContent === 'Could not load settings. Reload to retry.');
        assert.equal(await settingsReadTimeout.locator('#settings-close').isEnabled(), true);
        assert.equal(settingsWrites.length, writesBefore);
        stored.model = 'fresh-read-after-timeout'; revision++;
        releaseReads();
        await new Promise(resolve => setTimeout(resolve, 25));
        assert.equal(await settingsReadTimeout.locator('#settings-status').textContent(), 'Could not load settings. Reload to retry.');
        assert.equal(await settingsReadTimeout.locator('#setting-model').count(), 0);
        await settingsReadTimeout.locator('#settings-close').click();
        await settingsReadTimeout.waitForFunction(() => !document.querySelector('#settings-dialog').open);
        await settingsReadTimeout.locator('#settings-open').click();
        await settingsReadTimeout.locator('#setting-model').waitFor();
        assert.equal(await settingsReadTimeout.locator('#setting-model').inputValue(), 'fresh-read-after-timeout');
        await settingsReadTimeout.locator('#settings-close').click();
      } finally {
        holdNextSettingsRead = false;
        releaseReads();
      }
    });
    await settingsReadTimeout.close();
    const settingsTimeout = await createPage('web');
    await check('Settings write timeout aborts, preserves the draft and requires an explicit reload', async () => {
      await settingsTimeout.clock.install();
      const writesBefore = settingsWrites.length;
      holdNextSettingsWrite = true;
      try {
        await settingsTimeout.locator('#settings-open').click();
        await settingsTimeout.locator('#setting-model').waitFor();
        await settingsTimeout.locator('#setting-model').fill('timeout-fixture-model');
        await settingsTimeout.locator('#settings-save').click();
        await waitForHeldRead(heldWrites);
        await settingsTimeout.clock.fastForward(15_000);
        await settingsTimeout.waitForFunction(() => document.querySelector('#settings-status').textContent === 'Settings request timed out. Reload before saving.');
        assert.equal(await settingsTimeout.locator('#setting-model').inputValue(), 'timeout-fixture-model');
        assert.equal(await settingsTimeout.locator('#settings-save').isDisabled(), true);
        assert.equal(await settingsTimeout.locator('#settings-close').isEnabled(), true);
        assert.equal(settingsWrites.length, writesBefore + 1, 'timeout does not retry the write');
        assert.equal(stored.model, 'timeout-fixture-model', 'fixture commit occurs before the held acknowledgement');
        releaseReads();
        await new Promise(resolve => setTimeout(resolve, 25));
        assert.equal(await settingsTimeout.locator('#settings-status').textContent(), 'Settings request timed out. Reload before saving.');
        assert.equal(await settingsTimeout.locator('#setting-model').inputValue(), 'timeout-fixture-model');
        assert.equal(await settingsTimeout.locator('#settings-save').isDisabled(), true);
        await settingsTimeout.locator('#settings-reload').click();
        await settingsTimeout.locator('#confirm-accept').click();
        await settingsTimeout.locator('#setting-model').waitFor();
        await settingsTimeout.waitForFunction(() => document.querySelector('#settings-status').textContent === 'Up to date');
        assert.equal(await settingsTimeout.locator('#setting-model').inputValue(), 'timeout-fixture-model');
        assert.equal(await settingsTimeout.locator('#settings-save').isDisabled(), true);
        await settingsTimeout.locator('#settings-close').click();
        await settingsTimeout.waitForFunction(() => !document.querySelector('#settings-dialog').open);
      } finally {
        holdNextSettingsWrite = false;
        releaseReads();
      }
    });
    await settingsTimeout.close();
    for (const surface of ['desktop', 'web']) {
      const choices = await createPage(surface);
      await check(`${surface} Empty profile catalog stays select-only through edit, save and reload`, async () => {
        await choices.locator('#settings-open').click();
        await choices.locator('#setting-model').waitFor();
        const profile = choices.locator('#setting-model_profile');
        assert.equal(await profile.evaluate(node => node.tagName), 'SELECT');
        assert.equal(await profile.isDisabled(), true);
        assert.equal(await profile.locator('option:checked').textContent(), 'No saved choices available');
        await choices.locator('#setting-model').fill(`${surface}-choice-fixture-model`);
        assert.equal(await profile.isDisabled(), true);
        holdNextSettingsWrite = true;
        try {
          await choices.locator('#settings-save').click();
          await waitForHeldRead(heldWrites);
          assert.equal(await profile.isDisabled(), true);
          assert.equal(await choices.locator('[data-setting="model_profile"] .setting-reset').isDisabled(), true);
          assert.equal(await choices.locator('#setting-model').isDisabled(), true);
          assert.equal(await choices.locator('#settings-credentials-open').isDisabled(), true);
        } finally {
          holdNextSettingsWrite = false; releaseReads();
        }
        await choices.waitForFunction(() => document.querySelector('#settings-status').textContent.startsWith('Saved.'));
        assert.equal(await profile.isDisabled(), true);
        assert.equal(await choices.locator('[data-setting="model_profile"] .setting-reset').isDisabled(), true);
        assert.equal(Object.hasOwn(stored, 'model_profile'), false);
        assert.equal(stored.model, `${surface}-choice-fixture-model`);
        await choices.locator('#settings-reload').click();
        await choices.waitForFunction(() => document.querySelector('#settings-status').textContent === 'Up to date');
        assert.equal(await profile.isDisabled(), true);
      });
      await check(`${surface} Unavailable external profile stays visible and can reset; catalog choices enable selection`, async () => {
        stored.model_profile = 'external-fixture-profile'; revision++;
        try {
          await choices.locator('#settings-reload').click();
          await choices.waitForFunction(() => document.querySelector('#settings-status').textContent === 'Up to date');
          const profile = choices.locator('#setting-model_profile');
          assert.equal(await profile.inputValue(), 'external-fixture-profile');
          assert.equal(await profile.isDisabled(), true);
          assert.equal(await profile.locator('option:checked').textContent(), 'external-fixture-profile (configured externally)');
          const reset = choices.locator('[data-setting="model_profile"] .setting-reset');
          assert.equal(await reset.isEnabled(), true);
          await reset.click();
          await choices.locator('#settings-save').click();
          await choices.waitForFunction(() => document.querySelector('#settings-status').textContent.startsWith('Saved.'));
          assert.equal(Object.hasOwn(stored, 'model_profile'), false);
          assert.equal(await profile.isDisabled(), true);
          definitions.model_profile.options = ['primary'];
          await choices.locator('#settings-reload').click();
          await choices.waitForFunction(() => document.querySelector('#settings-status').textContent === 'Up to date');
          assert.equal(await profile.isEnabled(), true);
          await profile.selectOption('primary');
          assert.equal(await profile.inputValue(), 'primary');
          assert.equal(await choices.locator('#settings-save').isEnabled(), true);
          // No fake persistence claim: profile/legacy exclusivity belongs to Go.
        } finally {
          delete definitions.model_profile.options; delete stored.model_profile;
        }
      });
      await choices.close();
      for (const scenario of ['no-override', 'pending-reset', 'invalid-draft-conflict']) {
        await check(`${surface} Settings reset ${scenario} is a reversible explicit-save draft`, async () => {
          const original = { ...stored };
          const originalProject = { ...projectStored };
          const editor = await createPage(surface, true);
          const writesBefore = settingsWrites.length;
          delete stored.model_profile;
          Object.assign(stored, { model: 'saved-model', max_turns: 0, reduced_motion: false, api_base_url: '' });
          if (scenario === 'no-override') delete stored.max_turns;
          if (scenario === 'pending-reset') {
            Object.assign(stored, { model: 'user-model', max_turns: 5, reduced_motion: true, api_base_url: 'https://api.example/v1' });
            Object.assign(projectStored, { model: 'saved-model', max_turns: 0, reduced_motion: false, api_base_url: 'https://workspace.example/v1' });
          }
          revision++;
          const row = key => editor.locator(`[data-setting="${key}"]`);
          const reset = key => row(key).locator('.setting-reset');
          try {
            await editor.locator('#settings-open').click();
            await editor.locator('#setting-model').waitFor();
            await editor.getByRole('button', { name: 'All settings', exact: true }).click();
            if (scenario === 'no-override') {
              const source = await row('max_turns').locator('.setting-source').textContent();
              assert.equal(await reset('max_turns').isDisabled(), true);
              assert.equal(await reset('reduced_motion').isEnabled(), true);
              assert.equal(await reset('api_base_url').isEnabled(), true);
              await editor.locator('#setting-max_turns').fill('9');
              // Choosing the inherited value can still be an explicit override.
              await editor.locator('#setting-max_turns').fill('0');
              assert.equal(await reset('max_turns').textContent(), 'Discard change');
              await reset('max_turns').click();
              await focusIs(editor, 'setting-max_turns');
              assert.equal(await editor.locator('#setting-max_turns').inputValue(), '0');
              assert.equal(await reset('max_turns').isDisabled(), true);
              assert.equal(await editor.locator('#settings-save').isDisabled(), true);
              assert.equal(await row('max_turns').locator('.setting-source').textContent(), source);
              assert.equal(settingsWrites.length, writesBefore);
            } else if (scenario === 'pending-reset') {
              await editor.locator('#settings-scope').selectOption('project');
              await editor.waitForFunction(() => document.querySelector('#settings-status').textContent === 'Up to date');
              await editor.getByRole('button', { name: 'All settings', exact: true }).click();
              assert.equal(await reset('model').textContent(), 'Use user default');
              const source = await row('model').locator('.setting-source').textContent();
              await editor.locator('#setting-model').fill('unsaved-model');
              await editor.locator('#setting-model').evaluate(node => node.setSelectionRange(2, 7, 'backward'));
              await reset('model').click();
              assert.equal(await row('model').locator('.setting-pending-reset').isVisible(), true);
              assert.equal(await editor.locator('#setting-model').isVisible(), false);
              assert.equal(await editor.locator('#setting-model').isDisabled(), true);
              assert.equal(await row('model').locator('.setting-source').textContent(), source);
              assert.equal(await reset('model').textContent(), 'Undo reset');
              assert.equal(settingsWrites.length, writesBefore);
              await editor.getByRole('button', { name: 'Agent behavior', exact: true }).click();
              await editor.locator('#settings-search').fill('model');
              assert.equal(await row('model').locator('.setting-pending-reset').isVisible(), true);
              await reset('model').click();
              assert.equal(await editor.locator('#setting-model').inputValue(), 'unsaved-model');
              assert.equal(await editor.locator('#setting-model').isEnabled(), true);
              await focusIs(editor, 'setting-model');
              assert.deepEqual(await editor.locator('#setting-model').evaluate(node => [node.selectionStart, node.selectionEnd, node.selectionDirection]), [2, 7, 'backward']);
              await reset('model').click();
              await editor.getByRole('button', { name: 'All settings', exact: true }).click();
              for (const key of ['max_turns', 'reduced_motion', 'api_base_url']) await reset(key).click();
              await screenshot(editor, `settings-reset-${surface}`);
              await editor.setViewportSize({ width: 390, height: 844 });
              assert.equal(await row('model').locator('.setting-pending-reset').isVisible(), true);
              assert.equal(await editor.locator('#settings-dialog').evaluate(node => node.scrollWidth <= node.clientWidth), true);
              await screenshot(editor, `settings-reset-narrow-${surface}`);
              holdNextSettingsWrite = true;
              await editor.locator('#settings-save').click();
              await waitForHeldRead(heldWrites);
              assert.equal(await reset('model').isDisabled(), true);
              assert.equal(await editor.locator('#setting-model').isDisabled(), true);
              assert.deepEqual(settingsWrites.at(-1).changes, { model: null, max_turns: null, reduced_motion: null, api_base_url: null });
              assert.equal(settingsWrites.at(-1).scope, 'project');
              releaseReads();
              await editor.waitForFunction(() => document.querySelector('#settings-status').textContent.startsWith('Saved.'));
              assert.equal(await reset('model').isDisabled(), true);
              assert.equal(await editor.locator('#setting-model').isVisible(), true);
              assert.equal(await editor.locator('#setting-model').inputValue(), 'user-model');
              assert.match(await row('model').locator('.setting-source').textContent(), /From user/);
              for (const [key, value] of Object.entries({ model: 'user-model', max_turns: 5, reduced_motion: true, api_base_url: 'https://api.example/v1' })) {
                assert.equal(Object.hasOwn(projectStored, key), false);
                assert.equal(stored[key], value);
              }
            } else {
              await editor.locator('#setting-max_turns').fill('7');
              await editor.locator('#setting-max_turns').fill('');
              await reset('max_turns').click();
              assert.equal(await editor.locator('#setting-max_turns').isDisabled(), true);
              await reset('max_turns').click();
              assert.equal(await editor.locator('#setting-max_turns').inputValue(), '');
              assert.equal(await editor.locator('#setting-max_turns').evaluate(node => node.checkValidity()), false);
              await editor.locator('#settings-save').click();
              assert.equal(settingsWrites.length, writesBefore);
              await reset('max_turns').click();
              failNextSettingsWrite = true;
              await editor.locator('#settings-save').click();
              await editor.waitForFunction(() => document.querySelector('#settings-status').textContent.includes('write is unavailable'));
              assert.deepEqual(settingsWrites.at(-1).changes, { max_turns: null });
              assert.equal(stored.max_turns, 0);
              assert.equal(await row('max_turns').locator('.setting-pending-reset').isVisible(), true);
              assert.equal(await editor.locator('#settings-save').isEnabled(), true);
              conflictNext = true;
              await editor.locator('#settings-save').click();
              await editor.waitForFunction(() => document.querySelector('#settings-status').textContent.includes('another client'));
              assert.deepEqual(settingsWrites.at(-1).changes, { max_turns: null });
              assert.equal(await row('max_turns').locator('.setting-pending-reset').isVisible(), true);
              assert.equal(await reset('max_turns').isEnabled(), true);
              await reset('max_turns').click();
              assert.equal(await editor.locator('#setting-max_turns').inputValue(), '');
              assert.equal(await editor.locator('#settings-save').isDisabled(), true);
              await editor.locator('#settings-reload').click();
              await editor.locator('#confirm-accept').click();
              await editor.waitForFunction(() => document.querySelector('#settings-status').textContent === 'Up to date');
              assert.equal(await editor.locator('#setting-max_turns').inputValue(), '0');
              assert.equal(await row('max_turns').locator('.setting-pending-reset').isVisible(), false);
            }
          } finally {
            holdNextSettingsWrite = false; failNextSettingsWrite = false; conflictNext = false; releaseReads();
            await editor.close();
            for (const key of Object.keys(stored)) delete stored[key];
            Object.assign(stored, original); revision++;
            for (const key of Object.keys(projectStored)) delete projectStored[key];
            Object.assign(projectStored, originalProject);
          }
        });
      }
      for (const scenario of ['confirmation', 'revision-conflict', 'noneditable-blocker']) {
        await check(`${surface} Model selection ${scenario} is explicit and preserves the original draft`, async () => {
          const original = { ...stored };
          const selection = await createPage(surface);
          const writesBefore = settingsWrites.length;
          const turnsBefore = turnRequests;
          const sessionWritesBefore = sessionSettingWrites;
          Object.assign(stored, { provider: 'openai', model: 'before-model', api_base_url: 'https://api.example/v1' });
          delete stored.model_profile;
          definitions.model_profile.options = ['primary'];
          if (scenario === 'noneditable-blocker') definitions.model_profile.blocked_by = ['fallback_model'];
          revision++;
          try {
            await selection.locator('#settings-open').click();
            await selection.locator('#setting-model_profile').waitFor();
            await selection.locator('#setting-model_profile').selectOption('primary');
            const before = revision;
            await selection.locator('#settings-save').click();
            if (scenario === 'noneditable-blocker') {
              await selection.waitForFunction(() => document.querySelector('#settings-status').textContent.includes('fallback_model'), null, { timeout: 1500 });
              assert.equal(await selection.locator('#confirm-dialog').evaluate(node => node.open), false);
              assert.equal(settingsWrites.length, writesBefore);
              assert.equal(revision, before);
              assert.equal(await selection.locator('#setting-model_profile').inputValue(), 'primary');
              assert.equal(await selection.locator('#settings-save').isEnabled(), true);
              return;
            }
            await selection.locator('#confirm-dialog[open]').waitFor({ timeout: 1500 });
            assert.equal(await selection.locator('#confirm-copy').textContent(), 'Save Model profile and remove Provider, Model, API endpoint overrides from User defaults? Other scopes and current sessions will stay unchanged.');
            assert.equal(settingsWrites.length, writesBefore);
            assert.equal(revision, before);
            assert.equal(await selection.locator('#settings-close').isDisabled(), true);
            assert.equal(await selection.locator('#setting-model').isDisabled(), true);
            await screenshot(selection, `settings-selection-${surface}-${scenario}`);
            await selection.keyboard.press('Escape');
            await selection.locator('#confirm-dialog[open]').waitFor({ state: 'hidden' });
            await focusIs(selection, 'setting-model_profile');
            assert.equal(settingsWrites.length, writesBefore);
            assert.equal(await selection.locator('#setting-model_profile').inputValue(), 'primary');
            assert.equal(await selection.locator('#setting-provider').inputValue(), 'openai');
            assert.equal(await selection.locator('#setting-model').inputValue(), 'before-model');
            assert.equal(await selection.locator('#setting-api_base_url').inputValue(), 'https://api.example/v1');
            assert.equal(await selection.locator('#settings-save').isEnabled(), true);
            await selection.locator('#settings-save').click();
            await selection.locator('#confirm-dialog[open]').waitFor({ timeout: 1500 });
            if (scenario === 'revision-conflict') conflictNext = true;
            await selection.locator('#confirm-accept').click();
            const expected = { model_profile: 'primary', provider: null, model: null, api_base_url: null };
            if (scenario === 'revision-conflict') {
              await selection.waitForFunction(() => document.querySelector('#settings-status').textContent.includes('another client'));
              assert.equal(revision, before);
              assert.equal(await selection.locator('#setting-model_profile').inputValue(), 'primary');
              assert.equal(await selection.locator('#setting-provider').inputValue(), 'openai');
              assert.equal(await selection.locator('#setting-model').inputValue(), 'before-model');
              assert.equal(await selection.locator('#settings-save').isDisabled(), true);
            } else {
              await selection.waitForFunction(() => document.querySelector('#settings-status').textContent.startsWith('Saved.'));
              assert.equal(revision, before + 1);
              assert.equal(stored.model_profile, 'primary');
              for (const key of ['provider', 'model', 'api_base_url']) assert.equal(Object.hasOwn(stored, key), false);
              assert.equal(stored.theme, original.theme);
            }
            assert.equal(settingsWrites.length, writesBefore + 1);
            assert.deepEqual(settingsWrites.at(-1).changes, expected);
            if (scenario === 'confirmation') {
              // Editing legacy fields chooses that route; the profile removal is also explicit.
              await selection.locator('#setting-model').fill('after-model');
              await selection.locator('#settings-save').click();
              await selection.locator('#confirm-dialog[open]').waitFor({ timeout: 1500 });
              assert.match(await selection.locator('#confirm-copy').textContent(), /remove Model profile overrides/);
              await selection.locator('#confirm-accept').click();
              await selection.waitForFunction(() => document.querySelector('#settings-status').textContent.startsWith('Saved.'));
              assert.deepEqual(settingsWrites.at(-1).changes, { model: 'after-model', model_profile: null });
              assert.equal(Object.hasOwn(stored, 'model_profile'), false);
            }
            assert.equal(turnRequests, turnsBefore);
            assert.equal(sessionSettingWrites, sessionWritesBefore);
          } finally {
            await selection.close();
            for (const key of Object.keys(stored)) delete stored[key];
            Object.assign(stored, original);
            delete definitions.model_profile.options; delete definitions.model_profile.blocked_by;
            conflictNext = false; revision++;
          }
        });
      }
      const categories = await createPage(surface, true);
      await check(`${surface} Settings starts with model setup and keyboard categories retain focus`, async () => {
        await categories.locator('#settings-open').click();
        await categories.locator('#setting-model').waitFor();
        assert.deepEqual(await categories.locator('[data-setting]:visible').evaluateAll(rows => rows.map(row => row.dataset.setting).sort()),
          ['api_base_url', 'model', 'model_profile', 'provider']);
        assert.equal(await categories.locator('.settings-group-heading:visible').count(), 1);
        const behavior = categories.getByRole('button', { name: 'Agent behavior', exact: true });
        await behavior.focus();
        await behavior.press('Enter');
        assert.equal(await behavior.getAttribute('aria-pressed'), 'true');
        assert.equal(await behavior.evaluate(node => document.activeElement === node), true);
        assert.deepEqual(await categories.locator('[data-setting]:visible').evaluateAll(rows => rows.map(row => row.dataset.setting).sort()),
          ['max_turns', 'permission_mode']);
        await screenshot(categories, `settings-categories-${surface}`);
      });
      await check(`${surface} Category navigation and global search preserve hidden unsaved values`, async () => {
        await categories.getByRole('button', { name: 'Model & provider', exact: true }).click();
        await categories.locator('#setting-model').fill(`${surface}-category-fixture-model`);
        await categories.evaluate(() => { globalThis.categoryModelNode = document.querySelector('#setting-model'); });
        await categories.getByRole('button', { name: 'Appearance & input', exact: true }).click();
        await categories.locator('#setting-reduced_motion').selectOption('true');
        await categories.locator('#settings-search').fill('maximum turns');
        assert.equal(await categories.getByRole('button', { name: 'All settings', exact: true }).getAttribute('aria-pressed'), 'true');
        assert.deepEqual(await categories.locator('[data-setting]:visible').evaluateAll(rows => rows.map(row => row.dataset.setting)), ['max_turns']);
        await categories.locator('#setting-max_turns').fill('9');
        await categories.getByRole('button', { name: 'Model & provider', exact: true }).click();
        assert.equal(await categories.locator('#settings-search').inputValue(), '');
        assert.equal(await categories.locator('#setting-model').inputValue(), `${surface}-category-fixture-model`);
        assert.equal(await categories.evaluate(() => globalThis.categoryModelNode === document.querySelector('#setting-model')), true);
        await categories.locator('#settings-save').click();
        await categories.waitForFunction(() => document.querySelector('#settings-status').textContent.startsWith('Saved.'));
        assert.equal(stored.model, `${surface}-category-fixture-model`);
        assert.equal(stored.reduced_motion, true);
        assert.equal(stored.max_turns, 9);
      });
      await check(`${surface} Narrow Settings categories do not clip and hidden invalid fields regain focus`, async () => {
        await categories.setViewportSize({ width: 390, height: 844 });
        const navigation = categories.locator('#settings-categories');
        assert.equal(await navigation.evaluate(node => node.scrollWidth <= node.clientWidth), true);
        await categories.getByRole('button', { name: 'Agent behavior', exact: true }).click();
        await categories.locator('#setting-max_turns').fill('10');
        await categories.locator('#setting-max_turns').fill('');
        await categories.getByRole('button', { name: 'Model & provider', exact: true }).click();
        const before = revision;
        await categories.locator('#settings-save').click();
        assert.equal(revision, before);
        assert.equal(await categories.locator('#setting-max_turns').isVisible(), true);
        await focusIs(categories, 'setting-max_turns');
        await screenshot(categories, `settings-narrow-${surface}`);
      });
      await categories.close();
      for (const scenario of ['discovery', 'scroll-shell', 'credential-entry']) {
        await check(`${surface} Settings task shell ${scenario} preserves contextual ownership`, async () => {
          const editor = await createPage(surface);
          const writesBefore = settingsWrites.length;
          const turnsBefore = turnRequests;
          try {
            await editor.locator('#settings-open').click();
            await editor.locator('#setting-model').waitFor();
            const credentials = editor.locator('.settings-credentials');
            const summary = credentials.locator('summary');
            if (scenario === 'discovery') {
              assert.equal(await credentials.evaluate(node => node.tagName), 'DETAILS');
              assert.equal(await credentials.evaluate(node => node.open), false);
              assert.equal(await credentials.isVisible(), true);
              assert.equal(await editor.locator('#settings-credentials-copy').isVisible(), false);
              await editor.locator('#setting-model').fill(`${surface}-task-shell-draft`);
              await editor.evaluate(() => { globalThis.shellModelNode = document.querySelector('#setting-model'); });
              for (const name of ['Agent behavior', 'Appearance & input']) {
                await editor.getByRole('button', { name, exact: true }).click();
                assert.equal(await credentials.isVisible(), false);
              }
              await editor.locator('#settings-search').fill('api key');
              assert.equal(await credentials.isVisible(), true);
              assert.equal(await editor.locator('[data-setting]:visible').count(), 0);
              assert.equal(await editor.locator('.settings-no-results').isVisible(), false);
              await summary.focus();
              await summary.press('Enter');
              assert.equal(await credentials.evaluate(node => node.open), true);
              assert.equal(await editor.locator('#settings-credentials-copy').isVisible(), true);
              assert.equal(await summary.evaluate(node => document.activeElement === node), true);
              await summary.press('Space');
              assert.equal(await credentials.evaluate(node => node.open), false);
              await summary.press('Enter');
              await editor.locator('#settings-search').fill('theme');
              assert.equal(await credentials.isVisible(), false);
              await editor.locator('#settings-search').fill('missing setting');
              assert.equal(await credentials.isVisible(), false);
              assert.equal(await editor.locator('.settings-no-results').isVisible(), true);
              await editor.getByRole('button', { name: 'Model & provider', exact: true }).click();
              assert.equal(await credentials.evaluate(node => node.open), true, 'filtering preserves the disclosure choice');
              assert.equal(await editor.locator('#setting-model').inputValue(), `${surface}-task-shell-draft`);
              assert.equal(await editor.evaluate(() => globalThis.shellModelNode === document.querySelector('#setting-model')), true);
              await screenshot(editor, `settings-task-model-${surface}`);
            } else if (scenario === 'scroll-shell') {
              assert.equal(await editor.locator('#settings-body').count(), 1);
              await editor.getByRole('button', { name: 'All settings', exact: true }).click();
              for (const viewport of [{ width: 1440, height: 1000 }, { width: 390, height: 844 }]) {
                await editor.setViewportSize(viewport);
                const positions = async () => editor.locator('#settings-dialog').evaluate(node =>
                  ['settings-title', 'settings-scope', 'settings-categories', 'settings-save'].map(id => {
                    const r = document.getElementById(id).getBoundingClientRect();
                    return [id, r.top, r.bottom];
                  }));
                const before = await positions();
                await editor.locator('#settings-body').evaluate(node => { node.scrollTop = node.scrollHeight; });
                assert.equal(await editor.locator('#settings-body').evaluate(node => node.scrollTop > 0), true);
                assert.deepEqual(await positions(), before);
                for (const [, top, bottom] of before) assert.equal(top >= 0 && bottom <= viewport.height, true);
                assert.equal(await editor.locator('#settings-dialog').evaluate(node => node.scrollWidth <= node.clientWidth), true);
                await screenshot(editor, `settings-task-scroll-${viewport.width}-${surface}`);
              }
              await editor.getByRole('button', { name: 'Agent behavior', exact: true }).click();
              assert.equal(await editor.locator('#settings-body').evaluate(node => node.scrollTop), 0);
              await editor.setViewportSize({ width: 390, height: 430 });
              await editor.getByRole('button', { name: 'All settings', exact: true }).click();
              assert.equal(await editor.locator('#settings-dialog').evaluate(node => node.scrollHeight > node.clientHeight), true);
              await editor.locator('#setting-prompt_suggestions').focus();
              assert.equal(await editor.locator('#setting-prompt_suggestions').evaluate(node => {
                const r = node.getBoundingClientRect();
                return r.top >= 0 && r.bottom <= document.querySelector('.settings-footer').getBoundingClientRect().top;
              }), true);
              await screenshot(editor, `settings-task-short-field-${surface}`);
              await editor.locator('#setting-max_turns').fill('');
              await editor.locator('#setting-prompt_suggestions').selectOption('false');
              await editor.getByRole('button', { name: 'Appearance & input', exact: true }).click();
              assert.equal(await editor.locator('[data-setting="max_turns"]').isVisible(), false);
              await editor.locator('#settings-save').click();
              const invalidState = await editor.locator('#setting-max_turns').evaluate(node => {
                const r = node.getBoundingClientRect();
                const footer = document.querySelector('.settings-footer').getBoundingClientRect();
                return { focus: document.activeElement?.id, top: r.top, bottom: r.bottom, footerTop: footer.top, viewportHeight: innerHeight };
              });
              assert.equal(invalidState.focus === 'setting-max_turns' && invalidState.top >= 0 && invalidState.bottom <= Math.min(invalidState.viewportHeight, invalidState.footerTop), true,
                'hidden invalid input is revealed, focused and not covered in a short window: ' + JSON.stringify(invalidState));
              assert.equal(await editor.getByRole('button', { name: 'Agent behavior', exact: true }).getAttribute('aria-pressed'), 'true');
              assert.equal(await editor.locator('#settings-dialog').evaluate(node => node.scrollWidth <= node.clientWidth), true);
              await screenshot(editor, `settings-task-short-invalid-${surface}`);
              await editor.locator('#settings-save').focus();
              assert.equal(await editor.locator('#settings-save').evaluate(node => {
                const r = node.getBoundingClientRect();
                return document.activeElement === node && r.top >= 0 && r.bottom <= innerHeight;
              }), true, 'short-window save action remains naturally focusable');
              await editor.locator('#settings-close').focus();
              assert.equal(await editor.locator('#settings-close').evaluate(node => {
                const r = node.getBoundingClientRect();
                return document.activeElement === node && r.top >= 0 && r.bottom <= innerHeight;
              }), true);
              await screenshot(editor, `settings-task-short-${surface}`);
            } else {
              assert.equal(await summary.count(), 1);
              await summary.click();
              const manage = editor.locator('#settings-credentials-open');
              assert.equal(await manage.isVisible(), surface === 'desktop');
              if (surface === 'desktop') {
                await editor.locator('#setting-model').fill('credential-entry-unsaved-model');
                await manage.click();
                await editor.locator('#confirm-dialog[open]').waitFor();
                await editor.locator('#confirm-cancel').click();
                assert.equal(await editor.locator('#settings-dialog').evaluate(node => node.open), true);
                assert.equal(await editor.locator('#provider-dialog').evaluate(node => node.open), false);
                assert.equal(await editor.locator('#setting-model').inputValue(), 'credential-entry-unsaved-model');
                await manage.click();
                await editor.locator('#confirm-accept').click();
                await editor.locator('#provider-dialog[open]').waitFor();
                assert.equal(await editor.locator('#settings-dialog').evaluate(node => node.open), false);
                assert.equal(await editor.locator('#provider-api-key').inputValue(), '');
                await editor.keyboard.press('Escape');
                await editor.locator('#settings-open').click();
                await editor.locator('#setting-model').waitFor();
                assert.equal(await credentials.evaluate(node => node.open), false, 'fresh opens collapse the help');
              } else {
                assert.match(await editor.locator('#settings-credentials-copy').textContent(), /host environment/);
              }
            }
            assert.equal(settingsWrites.length, writesBefore);
            assert.equal(turnRequests, turnsBefore);
          } finally { await editor.close(); }
        });
      }
    }
    const sharedConfig = await createPage('desktop', false, true);
    await check('Desktop without a vault profile delegates workspace creation to shared runtime configuration', async () => {
      const before = creations;
      await sharedConfig.locator('#new-session').click();
      await sharedConfig.waitForFunction(() => document.querySelector('#session-title').textContent === 'Improve the search experience');
      assert.equal(creations, before + 1);
      assert.equal(await sharedConfig.locator('#provider-dialog').evaluate(node => node.open), false);
      assert.equal(await sharedConfig.locator('#composer').isVisible(), true);
    });
    const conversation = await createPage('web', true);
    for (const surface of ['desktop', 'web']) {
      await check(`${surface} narrow sheets restore their triggering toggle after Settings closes`, async () => {
        const page = await createPage(surface, true);
        try {
          await page.setViewportSize({ width: 390, height: 844 });
          for (const [toggle, close] of [['navigation-toggle', 'navigation-close'], ['inspector-toggle', 'inspector-close']]) {
            await page.locator(`#${toggle}`).click();
            await focusIs(page, close);
            await page.keyboard.press('ControlOrMeta+,');
            await page.locator('#settings-dialog[open]').waitFor();
            await page.locator('#settings-close').click();
            await page.waitForFunction(() => !document.querySelector('#settings-dialog').open);
            await focusIs(page, toggle);
          }
        } finally { await page.close(); }
      });
    }
    await check('Closing Settings restores the current conversation draft and messages', async () => {
      await conversation.locator('#prompt').fill('Keep this unfinished request while I check the settings.');
      const text = await conversation.locator('#timeline').textContent();
      await conversation.locator('#settings-open').click();
      await conversation.locator('#setting-model').waitFor();
      await conversation.locator('#settings-close').click();
      await conversation.waitForFunction(() => !document.querySelector('#settings-dialog').open);
      assert.equal(await conversation.locator('#prompt').inputValue(), 'Keep this unfinished request while I check the settings.');
      assert.equal(await conversation.locator('#timeline').textContent(), text);
      await screenshot(conversation, 'conversation');
    });
    await check('Saving shared model defaults does not mutate current-session execution settings', async () => {
      const before = sessionSettingWrites;
      assert.equal(await conversation.locator('#model-select').inputValue(), 'default');
      await conversation.locator('#settings-open').click();
      await conversation.locator('#setting-model').waitFor();
      await conversation.locator('#setting-model').fill('new-default-only');
      await conversation.locator('#settings-save').click();
      await conversation.waitForFunction(() => document.querySelector('#settings-status').textContent.startsWith('Saved.'));
      assert.equal(await conversation.locator('#setting-model').inputValue(), 'new-default-only');
      await conversation.locator('#settings-close').click();
      await conversation.waitForFunction(() => !document.querySelector('#settings-dialog').open);
      assert.equal(await conversation.locator('#model-select').inputValue(), 'default');
      assert.equal(sessionSettingWrites, before);
      assert.equal(await conversation.locator('#prompt').inputValue(), 'Keep this unfinished request while I check the settings.');
    });
    await check('A late session preference read cannot overwrite a successfully saved theme', async () => {
      holdNextSettingsRead = true;
      await conversation.locator('.session-select').filter({ hasText: secondSession.title }).click();
      await waitForHeldRead();
      await conversation.locator('#settings-open').click();
      await conversation.getByRole('button', { name: 'Appearance & input', exact: true }).click();
      await conversation.locator('#setting-theme').waitFor();
      await conversation.locator('#setting-theme').selectOption('daybreak');
      await conversation.locator('#settings-save').click();
      await conversation.waitForFunction(() => document.querySelector('#settings-status').textContent.startsWith('Saved.'));
      const lateResponse = conversation.waitForEvent('requestfinished', {
        predicate: request => request.url().includes('/v1/settings?') && request.method() === 'GET',
      });
      releaseReads();
      await lateResponse;
      await conversation.evaluate(() => new Promise(resolve => requestAnimationFrame(() => requestAnimationFrame(resolve))));
      assert.equal(await conversation.locator('html').getAttribute('data-theme'), 'daybreak');
      await conversation.locator('#settings-close').click();
      await conversation.waitForFunction(() => !document.querySelector('#settings-dialog').open);
      await screenshot(conversation, 'conversation-light');
      await conversation.locator('.session-select').filter({ hasText: session.title }).click();
    });
    let nextEventID = 2;
    async function emit(type, data = {}, turnID = 'active-fixture-turn') {
      const event = { id: nextEventID++, type, data, session_id: session.id, turn_id: turnID, engine_sequence: nextEventID };
      assert.ok(streams.size > 0, 'Web SSE stream must be connected');
      for (const [response, owner] of streams) {
        if (owner === session.id) response.write(`data: ${JSON.stringify(event)}\n\n`);
      }
      return event.id;
    }
    await check('A real SSE event preserves expanded history and message node identity', async () => {
      await conversation.locator('details.reasoning summary').click();
      await conversation.evaluate(() => { window.fixtureMessageNode = document.querySelector('#timeline article.assistant'); });
      await emit('turn.accepted', { turn_id: 'active-fixture-turn' });
      await conversation.waitForFunction(() => document.querySelector('#status').textContent === 'running');
      assert.equal(await conversation.locator('details.reasoning').evaluate(node => node.open), true);
      assert.equal(await conversation.evaluate(() => window.fixtureMessageNode === document.querySelector('#timeline article.assistant')), true);
    });
    await check('Unrelated SSE updates preserve question input focus, value and DOM identity', async () => {
      await emit('interaction_requested', { interaction: {
        request_id: 'question-acceptance', turn_id: 'active-fixture-turn', kind: 'question',
        question: { questions: [{ id: 'q-1', header: 'Direction', text: 'What would make the search flow clearer?', multi_select: false, free_text: true, options: [] }] },
      } });
      await conversation.locator('#interaction-body textarea').waitFor();
      await conversation.locator('#interaction-body textarea').fill('Keep my current context.');
      await conversation.evaluate(() => { window.fixtureInputNode = document.querySelector('#interaction-body textarea'); });
      await emit('queue.updated', { revision: 1, items: [{ id: 'queued-fixture', display: 'Run focused tests', state: 'pending', enqueued_at: '2026-09-05T00:00:00Z' }] });
      await conversation.locator('.queued-prompt').waitFor();
      assert.equal(await conversation.evaluate(() => window.fixtureInputNode === document.querySelector('#interaction-body textarea')), true);
      assert.equal(await conversation.locator('#interaction-body textarea').inputValue(), 'Keep my current context.');
      assert.equal(await conversation.evaluate(() => document.activeElement === window.fixtureInputNode), true);
      await screenshot(conversation, 'interaction');
    });
    await check('Streaming durable-ID promotion retains expanded reasoning and focus', async () => {
      await emit('assistant', { message: { reasoning_content: 'A synthetic streaming explanation.' } });
      const row = conversation.locator('#timeline article.assistant').last();
      const summary = row.locator('details.reasoning summary');
      await summary.click();
      await summary.focus();
      await conversation.evaluate(() => {
        window.fixtureStreamRow = document.querySelector('#timeline article.assistant:last-child');
        window.fixtureStreamSummary = window.fixtureStreamRow.querySelector('details.reasoning summary');
      });
      await emit('assistant', { transcript_entry_id: 'durable-stream-fixture', message: {} });
      // The queue event follows promotion on the same ordered SSE connection.
      // Its visible projection is an independent receipt/processing barrier.
      await emit('queue.updated', { revision: 2, items: [{ id: 'promotion-checkpoint', display: 'Promotion checkpoint', state: 'pending', enqueued_at: '2026-10-05T00:00:00Z' }] });
      await conversation.locator('.queued-prompt').filter({ hasText: 'Promotion checkpoint' }).waitFor();
      assert.equal(await row.locator('details.reasoning').evaluate(node => node.open), true);
      assert.equal(await conversation.evaluate(() => window.fixtureStreamRow === document.querySelector('#timeline article.assistant:last-child')), true);
      assert.equal(await conversation.evaluate(() => document.activeElement === window.fixtureStreamSummary), true);
    });
    await check('SSE updates preserve keyboard focus in the session navigation', async () => {
      const selected = conversation.locator('.session-select').filter({ hasText: session.title });
      await selected.focus();
      await emit('queue.updated', { revision: 3, items: [] });
      await conversation.waitForFunction(() => document.querySelector('#queued-prompts').hidden);
      assert.equal(await conversation.evaluate(title => document.activeElement.matches('.session-select') && document.activeElement.textContent.includes(title), session.title), true);
    });
    assert.deepEqual(errors, [], 'Renderer emitted no unhandled page errors');
  } catch (error) {
    results.push({ name: 'Acceptance runner', status: 'fail', error: error.message });
  } finally {
    try {
      await browser?.close();
    } catch (error) {
      results.push({ name: 'Browser cleanup', status: 'fail', error: error.message });
    }
    try {
      for (const response of streams.keys()) response.end();
      releaseReads();
      server.closeAllConnections();
      await new Promise(resolve => server.close(resolve));
    } catch (error) {
      results.push({ name: 'Fixture cleanup', status: 'fail', error: error.message });
    }
    const report = {
      boundary: 'Isolated Chromium renderer with controlled HTTP/API fixtures; actual browser SSE parser; no Electron native UI or live-provider validation.',
      outputDir, results, errors,
    };
    await fs.writeFile(path.join(outputDir, 'results.json'), JSON.stringify(report, null, 2) + '\n');
    const passed = results.filter(result => result.status === 'pass').length;
    console.log(`${passed}/${results.length} UI checks passed; artifacts: ${outputDir}`);
    if (passed !== results.length) process.exitCode = 1;
  }
}
main().catch(error => { console.error(error); process.exitCode = 1; });
