import assert from 'node:assert/strict';
import test from 'node:test';

import { createTimelineRenderer } from '../../internal/webui/assets/timeline_view.mjs';

class Node {
  constructor(tagName) {
    this.tagName = tagName;
    this.childNodes = [];
    this.parentNode = null;
    this.className = '';
    this.textContent = '';
    this.open = false;
  }

  get firstChild() { return this.childNodes[0] || null; }
  get nextSibling() {
    if (!this.parentNode) return null;
    return this.parentNode.childNodes[this.parentNode.childNodes.indexOf(this) + 1] || null;
  }

  append(...nodes) { for (const node of nodes) this.insertBefore(node, null); }
  insertBefore(node, reference) {
    if (node === reference) return;
    if (reference !== null && reference.parentNode !== this) throw new Error('invalid reference');
    if (node.parentNode) node.parentNode.removeChild(node);
    const index = reference === null ? this.childNodes.length : this.childNodes.indexOf(reference);
    this.childNodes.splice(index, 0, node);
    node.parentNode = this;
  }
  removeChild(node) {
    const index = this.childNodes.indexOf(node);
    if (index < 0) throw new Error('missing child');
    this.childNodes.splice(index, 1);
    node.parentNode = null;
    return node;
  }
  replaceChildren(...nodes) {
    for (const child of [...this.childNodes]) this.removeChild(child);
    this.append(...nodes);
  }
  querySelectorAll(tagName) {
    return this.childNodes.flatMap((child) => [
      ...(child.tagName === tagName ? [child] : []),
      ...child.querySelectorAll(tagName),
    ]);
  }
  querySelector(tagName) { return this.querySelectorAll(tagName)[0] || null; }
}

function message(id, content = id, extra = {}) {
  return { id, role: 'assistant', content, reasoningContent: 'thinking', toolCalls: [], ...extra };
}

function fixture() {
  const container = new Node('main');
  let renders = 0;
  const renderer = createTimelineRenderer(container, (input) => {
    renders += 1;
    const row = new Node('article');
    row.textContent = input.content;
    const details = new Node('details');
    details.className = 'reasoning';
    const summary = new Node('summary');
    summary.textContent = 'Reasoning';
    details.append(summary);
    row.append(details);
    return row;
  });
  return { container, renderer, renders: () => renders };
}

test('equivalent snapshots reuse nodes and preserve disclosure state', () => {
  const { container, renderer, renders } = fixture();
  const messages = [message('first'), message('last')];
  renderer.render('session', messages);
  const nodes = [...container.childNodes];
  nodes[0].querySelector('details').open = true;

  renderer.render('session', messages.map((entry) => ({ ...entry, sequence: 42 })));

  assert.equal(renders(), 2);
  assert.deepEqual(container.childNodes, nodes);
  assert.equal(nodes[0].querySelector('details').open, true);
});

test('stream changes replace only their row and retain disclosure state', () => {
  const { container, renderer, renders } = fixture();
  const stream = message('', 'partial', { turnID: 'turn', toolCallID: 'call' });
  renderer.render('session', [message('stable'), stream]);
  const [stable, oldStream] = container.childNodes;
  oldStream.querySelector('details').open = true;

  renderer.render('session', [message('stable'), { ...stream, content: 'complete' }]);

  assert.equal(renders(), 3);
  assert.equal(container.childNodes[0], stable);
  assert.notEqual(container.childNodes[1], oldStream);
  assert.equal(container.childNodes[1].querySelector('details').open, true);
});

test('durable IDs promote the matching stream alias without replacing its node', () => {
  const { container, renderer, renders } = fixture();
  const stream = message('', 'partial', { turnID: 'turn', toolCallID: 'call' });
  renderer.render('session', [stream]);
  const node = container.firstChild;
  node.querySelector('details').open = true;

  renderer.render('session', [{ ...stream, id: 'durable' }]);

  assert.equal(renders(), 1);
  assert.equal(container.firstChild, node);
  assert.equal(container.firstChild.querySelector('details').open, true);
});

test('pagination keeps existing nodes while inserting earlier history', () => {
  const { container, renderer } = fixture();
  const recent = [message('recent'), message('latest')];
  renderer.render('session', recent);
  const nodes = [...container.childNodes];
  nodes[0].querySelector('details').open = true;

  renderer.render('session', [message('earlier'), ...recent]);

  assert.deepEqual(container.childNodes.slice(1), nodes);
  assert.equal(nodes[0].querySelector('details').open, true);
});

test('a session boundary clears reusable nodes and disclosure state', () => {
  const { container, renderer } = fixture();
  renderer.render('first', [message('same')]);
  const first = container.firstChild;
  first.querySelector('details').open = true;

  renderer.render('second', [message('same')]);

  assert.notEqual(container.firstChild, first);
  assert.equal(container.firstChild.querySelector('details').open, false);
  assert.equal(first.parentNode, null);
});
