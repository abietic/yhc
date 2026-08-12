import { marked } from './vendor/marked.esm.js';

export const MAX_ASSISTANT_MARKDOWN_LENGTH = 262_144;

const text = (value) => ({ kind: 'text', text: String(value || '') });
const element = (tag, children = [], className, attrs) => ({
  kind: 'element',
  tag,
  ...(className ? { className } : {}),
  ...(attrs ? { attrs } : {}),
  children,
});
const literal = (source) => ({ mode: 'literal', children: [text(source)] });

function literalToken(token) {
  return text(token?.raw || token?.text || '');
}

function safeHTTPURL(value) {
  const raw = String(value || '');
  if (!raw || /[\u0000-\u001f\u007f]/u.test(raw)) return '';
  try {
    const url = new URL(raw);
    if (!['http:', 'https:'].includes(url.protocol)) return '';
    if (url.username || url.password) return '';
    return url.href;
  } catch {
    return '';
  }
}

function inline(tokens) {
  const children = [];
  for (const token of tokens || []) {
    switch (token.type) {
      case 'text':
      case 'escape':
        children.push(text(token.text));
        break;
      case 'strong':
        children.push(element('strong', inline(token.tokens)));
        break;
      case 'em':
        children.push(element('em', inline(token.tokens)));
        break;
      case 'del':
        children.push(element('del', inline(token.tokens)));
        break;
      case 'codespan':
        children.push(element('code', [text(token.text)]));
        break;
      case 'br':
        children.push(element('br'));
        break;
      case 'link': {
        const label = inline(token.tokens);
        const href = safeHTTPURL(token.href);
        if (href) children.push(element('a', label, undefined, { href }));
        else children.push(...label);
        break;
      }
      case 'image':
        children.push(element('span', [text(`Image: ${token.text || ''}`)], 'markdown-image-reference'));
        break;
      default:
        children.push(literalToken(token));
    }
  }
  return children;
}

function tableCell(cell, tag) {
  return element(tag, inline(cell.tokens));
}

function listItem(item) {
  const children = [];
  if (item.task) {
    children.push(element('input', [], undefined, {
      type: 'checkbox',
      disabled: true,
      checked: Boolean(item.checked),
    }));
  }
  for (const child of block(item.tokens)) {
    if (!item.loose && child.tag === 'p') children.push(...child.children);
    else children.push(child);
  }
  return element('li', children);
}

function block(tokens) {
  const children = [];
  for (const token of tokens || []) {
    switch (token.type) {
      case 'space':
      case 'checkbox':
        break;
      case 'paragraph':
        children.push(element('p', inline(token.tokens)));
        break;
      case 'text':
        children.push(element('p', inline(token.tokens || [token])));
        break;
      case 'heading':
        children.push(element(`h${Math.min(6, Math.max(1, (token.depth || 1) + 1))}`, inline(token.tokens)));
        break;
      case 'hr':
        children.push(element('hr'));
        break;
      case 'blockquote':
        children.push(element('blockquote', block(token.tokens)));
        break;
      case 'list': {
        const attrs = token.ordered && Number.isInteger(token.start) && token.start > 1
          ? { start: String(token.start) }
          : undefined;
        children.push(element(token.ordered ? 'ol' : 'ul', token.items.map(listItem), undefined, attrs));
        break;
      }
      case 'list_item':
        children.push(listItem(token));
        break;
      case 'code':
        children.push(element('pre', [element('code', [text(token.text)])]));
        break;
      case 'table': {
        const header = element('thead', [element('tr', token.header.map((cell) => tableCell(cell, 'th')))]);
        const body = element('tbody', token.rows.map((row) => element('tr', row.map((cell) => tableCell(cell, 'td')))));
        children.push(element('div', [element('table', [header, body])], 'markdown-table-wrap'));
        break;
      }
      default:
        children.push(literalToken(token));
    }
  }
  return children;
}

export function projectAssistantMarkdown(source) {
  const input = String(source);
  if (input.length > MAX_ASSISTANT_MARKDOWN_LENGTH) return literal(input);
  try {
    return { mode: 'markdown', children: block(marked.lexer(String(source), { gfm: true })) };
  } catch {
    return literal(input);
  }
}

const DOM_TAGS = new Set([
  'a', 'blockquote', 'br', 'code', 'del', 'div', 'em', 'h1', 'h2', 'h3', 'h4',
  'h5', 'h6', 'hr', 'input', 'li', 'ol', 'p', 'pre', 'span', 'strong', 'table',
  'tbody', 'td', 'th', 'thead', 'tr', 'ul',
]);

function renderNode(ownerDocument, node) {
  if (node.kind === 'text') return ownerDocument.createTextNode(node.text);
  if (!DOM_TAGS.has(node.tag)) return ownerDocument.createTextNode('');
  const value = ownerDocument.createElement(node.tag);
  if (node.className === 'markdown-table-wrap' || node.className === 'markdown-image-reference') {
    value.className = node.className;
  }
  if (node.tag === 'a') {
    const href = safeHTTPURL(node.attrs?.href);
    if (href) {
      value.href = href;
      value.target = '_blank';
      value.rel = 'noopener noreferrer';
    }
  } else if (node.tag === 'ol' && /^[1-9]\d*$/.test(node.attrs?.start || '')) {
    value.start = Number(node.attrs.start);
  } else if (node.tag === 'input') {
    value.type = 'checkbox';
    value.disabled = true;
    value.checked = Boolean(node.attrs?.checked);
  }
  value.append(...node.children.map((child) => renderNode(ownerDocument, child)));
  return value;
}

export function renderMessageContent(ownerDocument, role, source) {
  const content = ownerDocument.createElement('div');
  content.className = 'message-content';
  const input = String(source);
  if (role !== 'assistant') {
    content.textContent = input;
    return content;
  }
  const projection = projectAssistantMarkdown(input);
  if (projection.mode === 'literal') {
    content.textContent = input;
    return content;
  }
  content.className = 'message-content markdown-body';
  content.append(...projection.children.map((node) => renderNode(ownerDocument, node)));
  return content;
}
