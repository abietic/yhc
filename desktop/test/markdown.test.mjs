import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';
import test from 'node:test';

import {
  MAX_ASSISTANT_MARKDOWN_LENGTH,
  projectAssistantMarkdown,
  renderMessageContent,
} from '../../internal/webui/assets/markdown.mjs';

function collectTags(node) {
  if (node.kind === 'text') return [];
  return [node.tag, ...node.children.flatMap(collectTags)];
}

function collectText(node) {
  return node.kind === 'text'
    ? node.text
    : node.children.map(collectText).join('');
}

function find(node, predicate) {
  if (predicate(node)) return node;
  for (const child of node.children || []) {
    const match = find(child, predicate);
    if (match) return match;
  }
  return undefined;
}

function findAll(node, predicate, results = []) {
  if (predicate(node)) results.push(node);
  for (const child of node.children || []) findAll(child, predicate, results);
  return results;
}

class FakeNode {
  constructor(tag) {
    this.tag = tag;
    this.children = [];
    this.className = '';
    this.textContent = '';
  }

  append(...nodes) {
    this.children.push(...nodes);
  }
}

const fakeDocument = {
  createElement(tag) {
    return new FakeNode(tag);
  },
  createTextNode(value) {
    const node = new FakeNode('#text');
    node.textContent = String(value);
    return node;
  },
};

test('assistant markdown projects the reported formatting surface', () => {
  const projected = projectAssistantMarkdown([
    '**3. 命令面板会过滤不可用命令**',
    '',
    '- `internal/tui/command_palette_test.go:108`',
    '- **测试失败条件**',
    '',
    '## 结论',
    '',
    '| 当前模型 | `/effort` |',
    '|---|---|',
    '| DeepSeek | 隐藏 |',
  ].join('\n'));

  assert.equal(projected.mode, 'markdown');
  assert.deepEqual(projected.children.flatMap(collectTags), [
    'p', 'strong', 'ul', 'li', 'code', 'li', 'strong', 'h3',
    'div', 'table', 'thead', 'tr', 'th', 'th', 'code', 'tbody', 'tr', 'td', 'td',
  ]);
  assert.match(projected.children.map(collectText).join(''), /命令面板会过滤不可用命令/);
  assert.match(projected.children.map(collectText).join(''), /DeepSeek隐藏/);
  assert.match(projected.children.map(collectText).join(''), /\/effort/);
  assert.doesNotMatch(projected.children.map(collectText).join(''), /`\/effort`/);
  assert.equal(projectAssistantMarkdown('# top').children[0].tag, 'h2');
  assert.equal(find({ kind: 'element', children: projected.children }, (node) => node.tag === 'div').className, 'markdown-table-wrap');
});

test('local hostile tokens stay visible while valid Markdown continues to project', () => {
  const projected = projectAssistantMarkdown('<img src=x onerror=alert(1)>\n\n**safe**');
  assert.equal(projected.mode, 'markdown');
  assert.deepEqual(projected.children.flatMap(collectTags), ['p', 'strong']);
  assert.equal(collectText(projected.children[0]), '<img src=x onerror=alert(1)>');
  assert.equal(collectText(projected.children[1]), 'safe');
});

test('only safe absolute HTTP links create anchors and images are text references', () => {
  const projected = projectAssistantMarkdown([
    '[javascript](javascript:alert(1))',
    '[data](data:text/plain,x)',
    '[file](file:///tmp/x)',
    '[relative](/x)',
    '[credentials](https://user:pass@example.com/private)',
    '[control](https://example.com/\u0001)',
    '[safe](https://example.com/path)',
    '![pixel](https://tracker.example/pixel.gif)',
  ].join('\n\n'));
  const anchors = [];
  for (const child of projected.children) {
    const anchor = find(child, (node) => node.tag === 'a');
    if (anchor) anchors.push(anchor);
  }
  assert.deepEqual(anchors.map((node) => node.attrs), [{ href: 'https://example.com/path' }]);
  assert.equal(find({ kind: 'element', children: projected.children }, (node) => node.tag === 'img'), undefined);
  assert.equal(find({ kind: 'element', children: projected.children }, (node) => node.className === 'markdown-image-reference').className, 'markdown-image-reference');
  assert.match(projected.children.map(collectText).join(''), /Image: pixel/);
});

test('maps nested inline formatting and remaining approved block tokens', () => {
  const projected = projectAssistantMarkdown([
    '**outer `code`**',
    '',
    '---',
    '',
    '> *em* and ~~del~~  ',
    '> hard break',
    '',
    '```go',
    '<>&',
    '```',
  ].join('\n'));
  const tags = projected.children.flatMap(collectTags);
  for (const tag of ['strong', 'code', 'hr', 'blockquote', 'em', 'del', 'br', 'pre']) {
    assert.ok(tags.includes(tag), `missing ${tag}`);
  }
  assert.match(projected.children.map(collectText).join(''), /<>&/);
});

test('task markers and ordered starts use fixed boolean projection attributes', () => {
  const projected = projectAssistantMarkdown([
    '- [x] done',
    '- [ ] waiting',
    '',
    '3. third',
  ].join('\n'));
  const inputs = findAll({ kind: 'element', children: projected.children }, (node) => node.tag === 'input');
  assert.deepEqual(inputs.map((node) => node.attrs), [
    { type: 'checkbox', disabled: true, checked: true },
    { type: 'checkbox', disabled: true, checked: false },
  ]);
  assert.equal(find({ kind: 'element', children: projected.children }, (node) => node.tag === 'ol').attrs.start, '3');

  const rendered = renderMessageContent(fakeDocument, 'assistant', '- [x] done\n- [ ] waiting\n\n3. third');
  const renderedInputs = findAll(rendered, (node) => node.tag === 'input');
  assert.deepEqual(renderedInputs.map((node) => [node.type, node.disabled, node.checked]), [
    ['checkbox', true, true],
    ['checkbox', true, false],
  ]);
  assert.equal(find(rendered, (node) => node.tag === 'ol').start, 3);
});

test('partial and oversized assistant Markdown remain visible', () => {
  assert.doesNotThrow(() => projectAssistantMarkdown('```go\nfunc main() {'));
  const partial = projectAssistantMarkdown('**unfinished and `open');
  assert.match(partial.children.map(collectText).join(''), /unfinished.*open/);
  const oversized = 'x'.repeat(MAX_ASSISTANT_MARKDOWN_LENGTH + 1);
  assert.deepEqual(projectAssistantMarkdown(oversized), {
    mode: 'literal',
    children: [{ kind: 'text', text: oversized }],
  });
});

test('escaped punctuation preserves visible text without literal backslashes', () => {
  const projected = projectAssistantMarkdown('\\*escaped\\*');
  assert.equal(projected.children.map(collectText).join(''), '*escaped*');
  assert.deepEqual(projected.children.flatMap(collectTags), ['p']);
});

test('non-assistant and reasoning-owned callers remain literal text', () => {
  for (const role of ['user', 'tool', 'system', 'reasoning']) {
    const rendered = renderMessageContent(fakeDocument, role, '**literal**');
    assert.equal(rendered.tag, 'div');
    assert.equal(rendered.className, 'message-content');
    assert.equal(rendered.textContent, '**literal**');
    assert.equal(rendered.children.length, 0);
  }
});

test('fake DOM materialization applies only fixed safe properties', () => {
  const rendered = renderMessageContent(fakeDocument, 'assistant', '[safe](https://example.com)');
  const anchor = rendered.children[0].children[0];
  assert.equal(rendered.className, 'message-content markdown-body');
  assert.deepEqual([anchor.href, anchor.target, anchor.rel], [
    'https://example.com/', '_blank', 'noopener noreferrer',
  ]);
  assert.equal(anchor.attributes, undefined);
});

test('shared workbench delegates only message bodies to the safe projection', async () => {
  const [app, css] = await Promise.all([
    readFile(new URL('../../internal/webui/assets/app.mjs', import.meta.url), 'utf8'),
    readFile(new URL('../../internal/webui/assets/styles.css', import.meta.url), 'utf8'),
  ]);

  assert.match(app, /import \{ renderMessageContent \} from '\.\/markdown\.mjs';/);
  assert.match(app, /const body = message\.content \|\|\n\s*\(message\.toolCalls\.length > 0 \? 'Tool call requested\.' : ''\);/);
  assert.match(app, /renderMessageContent\(document, message\.role, body\)/);
  assert.doesNotMatch(app, /innerHTML|outerHTML|DOMParser|createContextualFragment/);
  assert.match(css, /\.message-content\.markdown-body\s*\{[^}]*white-space:\s*normal/s);
  assert.match(css, /\.markdown-table-wrap\s*\{[^}]*overflow-x:\s*auto/s);
  assert.match(css, /\.markdown-body pre\s*\{[^}]*white-space:\s*pre/s);
  assert.match(css, /\.markdown-body code\s*\{[^}]*overflow-wrap:\s*anywhere[^}]*white-space:\s*break-spaces/s);
  assert.match(css, /\.markdown-body \.markdown-table-wrap code\s*\{[^}]*white-space:\s*nowrap/s);
  assert.match(css, /\.markdown-body pre code\s*\{[^}]*white-space:\s*inherit/s);
});
