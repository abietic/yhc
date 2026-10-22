import assert from 'node:assert/strict';
import test from 'node:test';

import { createSettingsView } from '../../internal/webui/assets/settings_view.mjs';

class Node {
  constructor(tagName = 'div') {
    this.tagName = tagName;
    this.childNodes = [];
    this.parentNode = null;
    this.dataset = {};
    this.className = '';
    this.textContent = '';
    this.value = '';
    this.disabled = false;
    this.hidden = false;
    this.open = false;
    this.selectionStart = 0;
    this.selectionEnd = 0;
    this.selectionDirection = 'none';
  }

  append(...nodes) { for (const item of nodes) this.appendChild(item); }
  appendChild(item) { this.childNodes.push(item); item.parentNode = this; return item; }
  prepend(item) { this.childNodes.unshift(item); item.parentNode = this; }
  replaceChildren(...nodes) { this.childNodes = []; this.append(...nodes); }
  setAttribute(name, value) { this[name] = String(value); }
  addEventListener(type, listener) { this[type] = listener; }
  focus() { document.activeElement = this; }
  setSelectionRange(start, end, direction) { this.selectionStart = start; this.selectionEnd = end; this.selectionDirection = direction; }
  closest(selector) {
    for (let item = this; item; item = item.parentNode) {
      if (matches(item, selector)) return item;
    }
    return null;
  }
  querySelectorAll(selector) {
    return this.childNodes.flatMap((item) => [
      ...(matches(item, selector) ? [item] : []),
      ...item.querySelectorAll(selector),
    ]);
  }
  querySelector(selector) { return this.querySelectorAll(selector)[0] || null; }
}

function matches(node, selector) {
  if (/^[a-z]+$/.test(selector)) return node.tagName === selector;
  if (selector.startsWith('.')) return node.className.split(' ').includes(selector.slice(1));
  if (selector === '[data-setting]') return node.dataset.setting !== undefined;
  if (selector === '[data-settings-group]') return node.dataset.settingsGroup !== undefined;
  if (selector === '[value="project"]') return node.value === 'project';
  return false;
}

function snapshot(value = 1) {
  return {
    revision: 'revision-1',
    fields: {
      max_turns: {
        type: 'integer', label: 'Maximum turns', description: 'Turn limit', group: 'execution',
        stored_value: value, effective_value: value, source: 'user', overridden: true,
      },
    },
  };
}

function fixture(result) {
  const priorDocument = globalThis.document;
  const elements = new Map();
  const document = {
    activeElement: null,
    documentElement: { dataset: {} },
    createElement: (tag) => new Node(tag),
    getElementById: (id) => elements.get(id),
  };
  globalThis.document = document;
  const add = (id, tag = 'div') => {
    const item = new Node(tag);
    item.id = id;
    elements.set(id, item);
    return item;
  };
  const dialog = add('settings-dialog', 'dialog');
  dialog.showModal = () => { dialog.open = true; };
  dialog.close = () => { dialog.open = false; };
  const form = add('settings-form', 'form');
  add('settings-fields');
  add('settings-categories');
  add('settings-mcp');
  add('settings-scope-label');
  add('settings-save', 'button');
  const scope = add('settings-scope', 'select'); scope.value = 'user'; scope.append(new Node('option')); scope.childNodes[0].value = 'project';
  add('settings-reload', 'button');
  add('settings-close', 'button');
  add('settings-credentials-open', 'button');
  const credentials = add('settings-credentials', 'details'); credentials.open = false;
  add('settings-search', 'input');
  add('settings-body');
  add('settings-status');
  add('settings-context');
  add('settings-credentials-copy');
  const calls = [];
  const view = createSettingsView({
    api: async (operation, input) => { calls.push([operation, input]); return result; },
    context: () => ({ sessionID: '', workspace: '', backendReady: true, manageCredentials: false, legacyProfile: false }),
    confirmDiscard: async () => true,
  });
  return { document, priorDocument, elements, form, view, calls, restore: () => { globalThis.document = priorDocument; } };
}

async function withView(result, action) {
  const state = fixture(result);
  try {
    await state.view.open(new Node('button'));
    await action(state);
  } finally {
    state.restore();
  }
}

test('unsafe typed integer never reaches updateSettings, including direct submit', async () => {
  await withView(snapshot(1), async ({ elements, form, calls }) => {
    const row = elements.get('settings-fields').querySelector('[data-setting]');
    assert.ok(row, JSON.stringify(elements.get('settings-fields').childNodes.map((item) => [item.tagName, item.textContent, item.dataset, item.childNodes.map((child) => [child.tagName, child.dataset])])));
    const input = row.querySelector('input');
    input.value = '2';
    input.oninput();
    input.value = '9007199254740993';
    input.oninput();

    assert.equal(elements.get('settings-save').disabled, true);
    assert.equal(elements.get('settings-status').textContent, 'Check Maximum turns: enter a nonnegative browser-safe integer using decimal digits.');
    await form.onsubmit({ preventDefault() {} });
    assert.deepEqual(calls, [['getSettings', { scope: 'user', sessionID: '' }]]);
  });
});

test('unsafe JSON-decoded integer snapshot is fail-closed without displaying a rounded value', async () => {
  await withView(snapshot(JSON.parse('9007199254740993')), async ({ elements, form, calls }) => {
    assert.equal(elements.get('settings-status').textContent, 'integer outside browser-safe range; use TUI/CLI then reload');
    assert.equal(elements.get('settings-fields').querySelector('[data-setting]'), null);
    assert.equal(elements.get('settings-save').disabled, true);
    await form.onsubmit({ preventDefault() {} });
    assert.deepEqual(calls, [['getSettings', { scope: 'user', sessionID: '' }]]);
  });
});

test('unsafe shared defaults keep independent MCP diagnostics reachable without writing', async () => {
  await withView(snapshot(JSON.parse('9007199254740993')), async ({ elements, calls }) => {
    const category = elements.get('settings-categories').querySelectorAll('button').find(button => button.dataset.settingsCategory === 'mcp');
    assert.ok(category);
    assert.equal(category.disabled, false);
    category.onclick();
    assert.equal(elements.get('settings-mcp').hidden, false);
    assert.equal(elements.get('settings-mcp').querySelector('.mcp-status').textContent, 'Open a live session to inspect its MCP servers.');
    assert.equal(elements.get('settings-save').disabled, true);
    assert.deepEqual(calls, [['getSettings', { scope: 'user', sessionID: '' }]]);
  });
});

for (const raw of ['1.0000000000000001', '9007199254740991.1', '1e3']) {
  test(`integer edits reject non-decimal-integer raw input ${raw}`, async () => {
    await withView(snapshot(3), async ({ elements, form, calls }) => {
      const input = elements.get('settings-fields').querySelector('[data-setting]').querySelector('input');
      input.value = raw;
      input.oninput();
      assert.equal(elements.get('settings-save').disabled, true);
      await form.onsubmit({ preventDefault() {} });
      assert.deepEqual(calls, [['getSettings', { scope: 'user', sessionID: '' }]]);
    });
  });
}

test('zero stays a valid integer patch', async () => {
  await withView(snapshot(1), async ({ elements, form, calls }) => {
    const row = elements.get('settings-fields').querySelector('[data-setting]');
    assert.ok(row, JSON.stringify(elements.get('settings-fields').childNodes.map((item) => [item.tagName, item.textContent, item.dataset, item.childNodes.map((child) => [child.tagName, child.dataset])])));
    const input = row.querySelector('input');
    input.value = '0';
    input.oninput();
    await form.onsubmit({ preventDefault() {} });
    assert.deepEqual(calls, [
      ['getSettings', { scope: 'user', sessionID: '' }],
      ['updateSettings', { scope: 'user', sessionID: '', revision: 'revision-1', changes: { max_turns: 0 } }],
    ]);
  });
});

test('reset can submit null after invalid input and undo restores its save block', async () => {
  await withView(snapshot(1), async ({ elements, form, calls }) => {
    const row = elements.get('settings-fields').querySelector('[data-setting]');
    assert.ok(row, JSON.stringify(elements.get('settings-fields').childNodes.map((item) => [item.tagName, item.textContent, item.dataset, item.childNodes.map((child) => [child.tagName, child.dataset])])));
    const input = row.querySelector('input');
    const reset = row.querySelector('button');
    input.value = '9007199254740993';
    input.oninput();
    reset.onclick();
    reset.onclick();
    assert.equal(input.value, '9007199254740993');
    assert.equal(elements.get('settings-save').disabled, true);

    reset.onclick();
    await form.onsubmit({ preventDefault() {} });
    assert.deepEqual(calls[1], ['updateSettings', { scope: 'user', sessionID: '', revision: 'revision-1', changes: { max_turns: null } }]);
  });
});
