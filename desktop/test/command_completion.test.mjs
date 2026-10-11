import assert from 'node:assert/strict';
import test from 'node:test';
import { boundedCompletion, completionKey, createCompletionQuery, insertCompletionEdit } from '../../internal/webui/assets/command_completion.mjs';

const snapshot = () => ({ enabled: true, sessionID: 'session-1', input: '/mod', cursor: 4, selectionEnd: 4, fence: 'idle' });
const response = { start: 0, end: 4, candidates: [{ value: '/model', replacement: '/model ', source: 'command' }] };
const tick = () => new Promise(resolve => setTimeout(resolve, 5));

test('completion identity fences input, Unicode rune cursor, selection, session and runtime', () => {
  const initial = snapshot();
  for (const patch of [{ enabled: false }, { input: '/mod\n' }, { selectionEnd: 3 }, { input: 'ordinary' }, { sessionID: '' }]) assert.equal(completionKey({ ...initial, ...patch }), '');
  for (const patch of [{ input: '/model' }, { cursor: 3, selectionEnd: 3 }, { sessionID: 'session-2' }, { fence: 'route-changed' }]) assert.notEqual(completionKey({ ...initial, ...patch }), completionKey(initial));
});

test('late responses after edit, runtime change, and dismissal never replace current candidates', async () => {
  let current = snapshot();
  let rendered;
  const pending = [];
  const query = createCompletionQuery({ snapshot: () => current, request: () => new Promise(resolve => pending.push(resolve)), publish: value => { rendered = value; }, delay: 0 });
  query.refresh();
  await tick();
  current = { ...current, input: '/per', cursor: 4, selectionEnd: 4 };
  query.refresh();
  await tick();
  pending[0](response);
  await tick();
  assert.equal(rendered, null);
  pending[1]({ ...response, candidates: [{ value: '/permissions', replacement: '/permissions ' }] });
  await tick();
  assert.equal(rendered.candidates[0].value, '/permissions');
  current = { ...current, fence: 'busy' };
  assert.equal(query.current(), null);
  query.refresh();
  await tick();
  query.dismiss();
  pending[2](response);
  await tick();
  assert.equal(rendered, null);
  query.refresh();
  await tick();
  assert.equal(pending.length, 3, 'Escape dismissal stays closed until an edit');
});

test('candidate validation rejects invalid ranges, controls, malformed Unicode, and unbounded data', () => {
  assert.equal(boundedCompletion({ ...response, start: -1 }, '/mod'), null);
  assert.equal(boundedCompletion({ ...response, end: 5 }, '/mod'), null);
  const candidates = ['\u0000', '\ud800', 'a'.repeat(2049), ''].map(replacement => ({ value: 'x', replacement }));
  assert.equal(boundedCompletion({ ...response, candidates }, '/mod').candidates.length, 0);
  assert.equal(boundedCompletion({ ...response, candidates: Array(90).fill(response.candidates[0]) }, '/mod').candidates.length, 50);
});

test('acceptance invalidation opens the next shared argument stage, not the prior command stage', async () => {
  let current = snapshot();
  let calls = 0;
  const query = createCompletionQuery({ snapshot: () => current, request: async () => { calls++; return response; }, publish: () => {}, delay: 0 });
  query.refresh();
  await tick();
  current = { ...current, input: '/model ', cursor: 7, selectionEnd: 7 };
  query.invalidate();
  query.refresh();
  await tick();
  assert.equal(calls, 2);
});

function editInput(exec) {
  const listeners = new Map();
  const input = {
    value: '/model 😀中 suffix', selectionStart: 10, selectionEnd: 10, selectionDirection: 'none',
    fallbacks: 0,
    focus() {},
    setSelectionRange(start, end, direction = 'none') {
      this.selectionStart = start; this.selectionEnd = end; this.selectionDirection = direction;
    },
    setRangeText(text, start, end) {
      this.fallbacks++;
      this.value = this.value.slice(0, start) + text + this.value.slice(end);
      this.setSelectionRange(start + text.length, start + text.length);
    },
    addEventListener(name, listener) { listeners.set(name, listener); },
    removeEventListener(name) { listeners.delete(name); },
  };
  input.ownerDocument = { execCommand: exec && ((...args) => exec(input, listeners, ...args)) };
  return input;
}

test('native completion edit uses a plain-text edit and preserves the untouched Unicode suffix', () => {
  const input = editInput((node, _listeners, command, showUI, replacement) => {
    assert.equal(command, 'insertText');
    assert.equal(showUI, false);
    assert.equal(node.selectionStart, 7);
    assert.equal(node.selectionEnd, 10);
    node.value = node.value.slice(0, 7) + replacement + node.value.slice(10);
    return false; // Actual mutation, not a browser return flag, prevents double insertion.
  });
  assert.equal(insertCompletionEdit(input, '"中文 model"', 7, 10), true);
  assert.equal(input.value, '/model "中文 model" suffix');
  assert.equal(input.fallbacks, 0);
});

test('unavailable native completion edit falls back once without losing the suffix', () => {
  for (const exec of [undefined, () => false, () => { throw new Error('unsupported'); }]) {
    const input = editInput(exec);
    assert.equal(insertCompletionEdit(input, '"中文 model"', 7, 10), true);
    assert.equal(input.value, '/model "中文 model" suffix');
    assert.equal(input.fallbacks, 1);
    assert.equal(input.selectionStart, 17);
  }
});

test('a cancelled beforeinput is not bypassed by a completion fallback', () => {
  const input = editInput((_node, listeners) => {
    listeners.get('beforeinput')({ defaultPrevented: true });
    return false;
  });
  assert.equal(insertCompletionEdit(input, '"中文 model"', 7, 10), false);
  assert.equal(input.value, '/model 😀中 suffix');
  assert.equal(input.fallbacks, 0);
  assert.equal(input.selectionStart, 10);
});

test('a native edit that already mutated before throwing is never inserted twice', () => {
  const input = editInput(node => {
    node.value = '/model "中文 model" suffix';
    throw new Error('after edit');
  });
  assert.equal(insertCompletionEdit(input, '"中文 model"', 7, 10), true);
  assert.equal(input.fallbacks, 0);
});
