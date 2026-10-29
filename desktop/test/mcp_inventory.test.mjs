import assert from 'node:assert/strict';
import {createRequire} from 'node:module';
import test from 'node:test';
import {operationPath} from '../../internal/webui/assets/transport.mjs';
import {createMCPInventoryController, createMCPInventoryView, normalizeMCPInventory} from '../../internal/webui/assets/mcp_inventory.mjs';

const {desktopOperation} = createRequire(import.meta.url)('../request.cjs');
const sessionID = 'owned-session';
const response = (patch = {}) => ({session_id: sessionID, revision: '18446744073709551615', source: 'runtime', servers: [{name: 'docs', status: 'connected', source: 'runtime-manager', health: 'healthy', diagnostic: '', tools: [{name: 'search', description: '<script>fixture</script>', truncated: false}], hidden_tools: 0, truncated: false}], hidden_servers: 0, hidden_tools: 0, ...patch});
const tick = () => new Promise(resolve => setImmediate(resolve));

test('MCP transports accept only an owned session read', () => {
  for (const operation of [operationPath, desktopOperation]) {
    assert.deepEqual(operation('getMCPInventory', {sessionID}), [`/v1/sessions/${sessionID}/mcp-inventory`, 'GET']);
    for (const input of [{sessionID, path: '/private'}, {sessionID, method: 'POST'}, {}, {sessionID: '../escape'}]) assert.throws(() => operation('getMCPInventory', input));
  }
});

test('MCP response binds identity and strips unapproved metadata', () => {
  const raw = response(); raw.servers[0].url = 'fixture-secret'; raw.servers[0].tools[0].input_schema = {secret: 'fixture-secret'};
  const got = normalizeMCPInventory(raw, sessionID);
  assert.equal(got.revision, raw.revision);
  assert.equal(got.servers[0].tools[0].description, '<script>fixture</script>');
  assert.equal(JSON.stringify(got).includes('fixture-secret'), false);
  for (const patch of [{session_id: 'other'}, {revision: 7}, {revision: '01'}, {servers: null}]) assert.throws(() => normalizeMCPInventory(response(patch), sessionID));
});

test('MCP reads are explicit and all context boundaries retire late results', async () => {
  for (const change of [{visible: false}, {backendReady: false}, {sessionID: 'other'}, {sessionID: undefined}]) {
    const pending = []; const c = createMCPInventoryController({api: (op, payload) => new Promise(resolve => pending.push({op, payload, resolve})), onChange() {}});
    const ctx = {sessionID, visible: true, backendReady: true}; c.updateContext(ctx);
    assert.equal(pending.length, 0);
    c.load(); assert.equal(pending.length, 1); assert.equal(pending[0].op, 'getMCPInventory');
    c.updateContext({...ctx, ...change}); pending[0].resolve(response()); await tick();
    assert.equal(c.state().data, null);
  }
});

test('MCP refreshed data cannot be overwritten by an older read or unsafe error', async () => {
  const pending = []; const c = createMCPInventoryController({api: () => new Promise((resolve, reject) => pending.push({resolve, reject})), onChange() {}});
  c.updateContext({sessionID, visible: true, backendReady: true}); c.load(); c.load();
  pending[1].resolve(response({revision: '2'})); await tick(); pending[0].resolve(response({revision: '1'})); await tick();
  assert.equal(c.state().data.revision, '2');
  c.load(); pending[2].reject(new Error('secret /private/fixture')); await tick();
  assert.equal(c.state().data, null); assert.equal(c.state().error.includes('private'), false);
  assert.match(c.state().error, /Refresh/);
});

test('equivalent runtime updates neither poll nor repaint an expanded inventory', async () => {
  let reads = 0; let paints = 0;
  const c = createMCPInventoryController({api: async () => {reads++; return response();}, onChange: () => {paints++;}});
  const context = {sessionID, visible: true, backendReady: true}; c.updateContext(context); await c.load();
  const previous = paints;
  for (let i = 0; i < 10; i++) c.updateContext({...context});
  assert.equal(reads, 1); assert.equal(paints, previous);
});

test('retiring an MCP snapshot clears its generation tooltip too', async () => {
  class Element {
    children = []; title = '';
    append(...items) {this.children.push(...items);}
    replaceChildren(...items) {this.children = items;}
    setAttribute() {}
  }
  const previous = globalThis.document;
  globalThis.document = {createElement: () => new Element()};
  try {
    const root = new Element();
    let current = {sessionID, backendReady: true};
    let fail = false;
    const view = createMCPInventoryView({root, context: () => current, api: async () => {if (fail) throw new Error('fixture'); return response();}});
    const status = root.children[2];
    view.setVisible(true); await tick(); assert.match(status.title, /18446744073709551615/);
    view.setVisible(false); assert.equal(status.title, '');
    view.setVisible(true); await tick();
    current = {...current, backendReady: false}; view.updateContext(); assert.equal(status.title, '');
    current = {...current, backendReady: true}; view.updateContext(); root.children[0].children[1].onclick(); await tick();
    fail = true; root.children[0].children[1].onclick(); await tick();
    assert.equal(status.title, ''); assert.match(status.textContent, /Refresh to retry/);
  } finally {globalThis.document = previous;}
});
