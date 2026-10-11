// Candidates are engine-owned advisory edits; this module owns only transient
// request identity, selection, and presentation. It never submits a command.
export function completionKey(snapshot) {
  if (!snapshot?.enabled || !snapshot.sessionID || typeof snapshot.input !== 'string' ||
      !snapshot.input.startsWith('/') || /[\n\r\t]/u.test(snapshot.input) ||
      [...snapshot.input].length > 2048 || snapshot.cursor !== snapshot.selectionEnd) return '';
  return JSON.stringify([snapshot.sessionID, snapshot.input, snapshot.cursor, snapshot.fence]);
}

export function boundedCompletion(response, input) {
  const length = [...input].length;
  if (!Number.isInteger(response?.start) || !Number.isInteger(response?.end) ||
      response.start < 0 || response.end < response.start || response.end > length ||
      !Array.isArray(response.candidates)) return null;
  const safe = (value, limit) => typeof value === 'string' && [...value].length <= limit &&
    !/[\u0000-\u001f\u007f-\u009f\uD800-\uDFFF]/u.test(value);
  return {
    start: response.start, end: response.end,
    command: safe(response.command, 256) ? response.command : '',
    hint: safe(response.hint, 512) ? response.hint : '',
    candidates: response.candidates.slice(0, 50).filter(candidate =>
      candidate && safe(candidate.value, 512) && safe(candidate.replacement, 2048) && candidate.replacement !== '',
    ).map(candidate => ({
      value: candidate.value, replacement: candidate.replacement,
      source: safe(candidate.source, 64) ? candidate.source : '',
      description: safe(candidate.description, 512) ? candidate.description : '',
    })),
  };
}

export function createCompletionQuery({ snapshot, request, publish, delay = 80 }) {
  let serial = 0;
  let key = '';
  let timer;
  let current = null;
  function reset(nextKey = '') {
    serial++;
    clearTimeout(timer);
    key = nextKey;
    current = null;
    publish(null);
  }
  return {
    refresh() {
      const captured = snapshot();
      const nextKey = completionKey(captured);
      if (nextKey === key) return;
      reset(nextKey);
      if (!nextKey) return;
      const requestID = serial;
      timer = setTimeout(async () => {
        try {
          const result = boundedCompletion(await request({ sessionID: captured.sessionID, input: captured.input, cursor: captured.cursor }), captured.input);
          if (requestID !== serial || nextKey !== completionKey(snapshot())) return;
          current = result;
          publish(result);
        } catch {
          if (requestID === serial) publish(null);
        }
      }, delay);
    },
    dismiss() { reset(completionKey(snapshot())); },
    invalidate() { reset(); },
    current() { return key && key === completionKey(snapshot()) ? current : null; },
  };
}

export function insertCompletionEdit(input, replacement, start, end) {
  const before = input.value;
  const expected = before.slice(0, start) + replacement + before.slice(end);
  const selection = [input.selectionStart, input.selectionEnd, input.selectionDirection];
  let beforeInput;
  const observe = event => { beforeInput = event; };
  input.focus();
  input.setSelectionRange(start, end);
  input.addEventListener('beforeinput', observe);
  try {
    // insertText is plain text, not HTML or clipboard access. Despite being
    // deprecated, execCommand is the browser edit that preserves textarea undo;
    // setRangeText alone bypasses history and can corrupt subsequent redo.
    input.ownerDocument.execCommand?.('insertText', false, replacement);
  } catch {
    // Older browsers can lack this edit; fall back only if nothing changed.
  } finally {
    input.removeEventListener('beforeinput', observe);
  }
  if (beforeInput?.defaultPrevented && input.value === before) {
    input.setSelectionRange(...selection);
    return false;
  }
  if (input.value === before && before !== expected) {
    input.setRangeText(replacement, start, end, 'end');
  }
  if (input.value !== expected) return false;
  input.setSelectionRange(start + replacement.length, start + replacement.length);
  return true;
}

export function mountCommandCompletion({ input, popup, status, snapshot, request, onEdit }) {
  let selected = -1;
  let composing = false;
  const query = createCompletionQuery({
    snapshot: () => ({ ...snapshot(), enabled: !composing && document.activeElement === input && !input.disabled && snapshot().enabled }),
    request,
    publish: render,
  });
  function render(result) {
    selected = -1;
    popup.replaceChildren();
    popup.hidden = !result?.candidates.length;
    input.removeAttribute('aria-activedescendant');
    status.textContent = result?.candidates.length ? `${result.candidates.length} command candidates. Use arrows to preview, Tab to insert, Escape to dismiss.` : '';
    if (popup.hidden) return;
    if (result.hint) {
      const hint = document.createElement('p');
      hint.className = 'completion-hint';
      hint.textContent = `/${result.command} ${result.hint}`;
      popup.append(hint);
    }
    const list = document.createElement('div');
    list.id = 'command-completion-list';
    list.setAttribute('role', 'listbox');
    list.setAttribute('aria-label', 'Command candidates');
    result.candidates.forEach((candidate, index) => {
      const row = document.createElement('button');
      row.type = 'button';
      row.tabIndex = -1;
      row.className = 'completion-option';
      row.id = `command-candidate-${index}`;
      row.setAttribute('role', 'option');
      row.setAttribute('aria-selected', 'false');
      const value = document.createElement('span');
      value.className = 'completion-value';
      value.textContent = candidate.value;
      const detail = document.createElement('span');
      detail.className = 'completion-detail';
      detail.textContent = candidate.description || candidate.source;
      row.append(value, detail);
      row.onmousedown = event => event.preventDefault();
      row.onclick = () => accept(index);
      list.append(row);
    });
    popup.append(list);
  }
  function accept(index) {
    const result = query.current();
    const candidate = result?.candidates[index];
    if (!candidate) return false;
    const text = [...input.value];
    const start = text.slice(0, result.start).join('').length;
    const end = text.slice(0, result.end).join('').length;
    if (!insertCompletionEdit(input, candidate.replacement, start, end)) return false;
    onEdit(input.value);
    query.invalidate();
    input.focus();
    query.refresh();
    return true;
  }
  input.addEventListener('input', () => query.refresh());
  input.addEventListener('click', () => query.refresh());
  input.addEventListener('select', () => query.refresh());
  input.addEventListener('keyup', () => query.refresh());
  input.addEventListener('focus', () => query.refresh());
  input.addEventListener('blur', () => query.dismiss());
  input.addEventListener('compositionstart', () => { composing = true; query.dismiss(); });
  input.addEventListener('compositionend', () => { composing = false; query.refresh(); });
  return {
    refresh: () => query.refresh(),
    dismiss: () => query.dismiss(),
    keydown(event) {
      if (composing || event.isComposing) return false;
      if (event.shiftKey && event.key !== 'Escape') return false;
      if (event.metaKey || event.ctrlKey || event.altKey) { query.dismiss(); return false; }
      const result = query.current();
      if (!result?.candidates.length) return false;
      if (event.key === 'Escape') { query.dismiss(); return true; }
      if (event.key === 'Tab' && !event.shiftKey) return accept(selected < 0 ? 0 : selected);
      if (event.key === 'Enter' && selected >= 0 && !event.shiftKey) return accept(selected);
      if (!['ArrowDown', 'ArrowUp'].includes(event.key)) return false;
      selected = selected < 0 ? (event.key === 'ArrowDown' ? 0 : result.candidates.length - 1) :
        (selected + (event.key === 'ArrowDown' ? 1 : -1) + result.candidates.length) % result.candidates.length;
      for (const [index, row] of [...popup.querySelectorAll('[role="option"]')].entries()) {
        row.setAttribute('aria-selected', String(index === selected));
        if (index === selected) {
          input.setAttribute('aria-activedescendant', row.id);
          row.scrollIntoView({ block: 'nearest' });
        }
      }
      return true;
    },
  };
}
