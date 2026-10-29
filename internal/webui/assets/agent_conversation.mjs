import { canReadTaskExplorer } from './task_explorer.mjs';
import { executionDetailEligibility, executionDetailQuery } from './agent_execution_detail.mjs';
import { renderMessageContent } from './markdown.mjs';

const FIELDS = new Set(['sessionID', 'agentID', 'generation', 'executionSessionID', 'threadID', 'cursor', 'limit']);
const MODES = ['live_attach', 'replay_only', 'evicted_transcript'];
const validID = value => typeof value === 'string' && value.length > 0 && value.trim() === value &&
  value.isWellFormed() && [...value].length <= 512 && !/\p{Cc}/u.test(value);
const validCursor = value => typeof value === 'string' && /^[!-~]{1,128}$/.test(value);
const matches = (target, row) => target.agentID === row.agent_id && target.generation === row.generation &&
  target.executionSessionID === row.execution_session_id && target.threadID === row.thread_id;
const bounded = (value, limit) => {
  if (typeof value !== 'string' || !value.isWellFormed()) throw new TypeError('Invalid conversation text.');
  const bytes = new TextEncoder().encode(value);
  let end = Math.min(bytes.length, limit);
  while (end < bytes.length && (bytes[end] & 0xC0) === 0x80) end -= 1;
  return new TextDecoder().decode(bytes.subarray(0, end));
};

export function agentConversationQuery(payload) {
  if (!payload || Array.isArray(payload) || typeof payload !== 'object' ||
      Object.keys(payload).some(key => !FIELDS.has(key)) ||
      (payload.cursor !== undefined && !validCursor(payload.cursor)) ||
      (payload.limit !== undefined && (!Number.isInteger(payload.limit) || payload.limit < 1 || payload.limit > 32))) {
    throw new TypeError('Invalid conversation page request.');
  }
  const { cursor, limit, ...identity } = payload;
  const query = executionDetailQuery(identity);
  query.delete('include_output');
  if (cursor !== undefined) query.set('cursor', cursor);
  query.set('limit', String(limit ?? 32));
  return query;
}

export function normalizeAgentConversationPage(value, target, limit = 32) {
  if (!value || value.session_id !== target.sessionID || !matches(target, value) ||
      typeof value.revision !== 'string' || !/^(0|[1-9][0-9]{0,19})$/.test(value.revision) ||
      BigInt(value.revision) > 18446744073709551615n || !MODES.includes(value.mode) ||
      value.replay !== (value.mode !== 'live_attach') || value.storage !== 'durable' ||
      typeof value.has_more !== 'boolean' || typeof value.next_cursor !== 'string' ||
      (value.has_more ? !validCursor(value.next_cursor) : value.next_cursor !== '') ||
      !Array.isArray(value.messages) || value.messages.length > limit) {
    throw new TypeError('Invalid conversation page identity or response.');
  }
  const seen = new Set();
  const messages = value.messages.map(message => {
    if (!message || !validID(message.id) || seen.has(message.id) ||
        (message.transcript_entry_id === '' ? message.source !== 'runtime' : !validID(message.transcript_entry_id)) ||
        !['user', 'assistant', 'tool', 'system'].includes(message.role) ||
        !['durable', 'runtime', 'durable+runtime'].includes(message.source) ||
        typeof message.completed !== 'boolean' || typeof message.replay !== 'boolean' ||
        typeof message.truncated !== 'boolean' || !Array.isArray(message.tool_calls) ||
        !Array.isArray(message.prompt_parts) || message.tool_calls.length > 8 || message.prompt_parts.length > 16) {
      throw new TypeError('Invalid conversation message.');
    }
    seen.add(message.id);
    const content = bounded(message.content, 16384);
    const reasoningContent = bounded(message.reasoning_content, 8192);
    const toolCalls = message.tool_calls.map(call => ({ id: bounded(call.id, 2048), name: bounded(call.name, 2048),
      inputPreview: bounded(call.input_preview, 4096) }));
    const promptParts = message.prompt_parts.map(part => ({ kind: bounded(part.kind, 128), text: bounded(part.text, 4096),
      mimeType: bounded(part.mime_type, 512), imageDetail: bounded(part.image_detail, 256),
      sizeBytes: Number.isSafeInteger(part.size_bytes) && part.size_bytes >= 0 ? part.size_bytes : 0,
      width: Number.isSafeInteger(part.width) && part.width >= 0 ? part.width : 0,
      height: Number.isSafeInteger(part.height) && part.height >= 0 ? part.height : 0 }));
    return { id: message.id, transcriptEntryID: message.transcript_entry_id, role: message.role, content,
      reasoningContent, toolCalls, promptParts, toolName: bounded(message.tool_name, 2048),
      toolCallID: bounded(message.tool_call_id, 2048), completed: message.completed, replay: message.replay,
      source: message.source, truncated: message.truncated || content !== message.content || reasoningContent !== message.reasoning_content };
  });
  return { ...target, revision: value.revision, mode: value.mode, replay: value.replay, messages,
    nextCursor: value.next_cursor, hasMore: value.has_more,
    corruptions: Number.isSafeInteger(value.corruptions) && value.corruptions >= 0 ? value.corruptions : 0 };
}

const empty = () => ({ target: null, data: null, status: 'idle', error: '', stale: false });
function readError(error) {
  if (['agent_transcript_selection_changed', 'agent_execution_selection_changed'].includes(error?.code)) {
    return 'This exact conversation changed. Return to the parent conversation and refresh Tasks.';
  }
  if (error?.code === 'invalid_cursor') return 'This history cursor expired. Refresh the newest page.';
  if (error?.code === 'agent_transcript_unavailable') return 'Conversation reading is unavailable in this backend.';
  return 'Conversation could not be read. Retry or return to the parent conversation.';
}

// Presentation only. No session descriptor, draft, runtime reducer or mutation
// operation is created for the child. Cursors stay in this bounded view.
export function createAgentConversationController({ read, changed = () => {} }) {
  let context;
  let current = empty();
  let requestID = 0;
  const eligible = target => canReadTaskExplorer(context?.session, context?.backendReady) &&
    context.session.id === target.sessionID &&
    context.session.tasks?.snapshot?.executions.filter(row => matches(target, row) && !executionDetailEligibility(row)).length === 1;
  function close() { requestID += 1; current = empty(); changed(current); }
  function load(older = false) {
    if (!current.target || !eligible(current.target) || (older &&
        (current.status === 'loading' || !current.data?.hasMore || current.data.messages.length >= 256))) return;
    const target = current.target;
    const before = current.data;
    const activityAtRead = context.session.tasks;
    const cursor = older ? before.nextCursor : undefined;
    const limit = older ? Math.min(32, 256 - before.messages.length) : 32;
    const id = ++requestID;
    current.status = 'loading'; current.error = ''; changed(current);
    new Promise(resolve => resolve(read({ ...target, ...(cursor ? { cursor } : {}), limit }))).then(value => {
      if (id !== requestID || current.target !== target || !eligible(target)) return;
      try {
        const page = normalizeAgentConversationPage(value, target, limit);
        if (older) {
          if (page.hasMore && (page.nextCursor === cursor || page.messages.length === 0)) throw new TypeError('History did not advance.');
          const existing = new Set(before.messages.map(message => message.id));
          const additions = page.messages.filter(message => !existing.has(message.id));
          if (page.hasMore && additions.length === 0) throw new TypeError('History identity did not advance.');
          // An older page does not replace the newest page's lifecycle mode.
          current.data = { ...before, messages: [...additions, ...before.messages], nextCursor: page.nextCursor,
            hasMore: page.hasMore, corruptions: before.corruptions + page.corruptions };
        } else {
          current.data = page;
          current.stale = context.session.tasks !== activityAtRead && Boolean(context.session.tasks.stale);
        }
        current.status = 'ready';
      } catch { current.status = 'error'; current.error = 'Conversation page identity or response is invalid.'; current.stale = Boolean(current.data); }
      changed(current);
    }, error => {
      if (id !== requestID || current.target !== target || !eligible(target)) return;
      current.status = 'error'; current.error = readError(error); current.stale = Boolean(current.data); changed(current);
    });
  }
  return {
    state: () => current,
    updateContext(next) {
      const previous = context; context = next;
      if (current.target && (!eligible(current.target) || previous?.session?.tasks?.snapshot !== next.session?.tasks?.snapshot)) close();
      else if (current.target && next.session.tasks?.stale && previous?.session?.tasks !== next.session.tasks) {
        current.stale = true; changed(current);
      }
    },
    open(target) {
      if (!target || !eligible(target)) return;
      current = { ...empty(), target: { ...target } }; load();
    },
    refresh: () => load(), older: () => load(true), close,
  };
}

export function createAgentConversationView({ document, read, onToggle, returnFocus }) {
  const find = id => document.getElementById(id);
  const root = find('agent-conversation');
  const timeline = find('agent-conversation-timeline');
  let target = null;
  let rendered = new Map();
  const node = (tag, className, value = '') => {
    const item = document.createElement(tag); item.className = className; item.textContent = value; return item;
  };
  function render(value) {
    const wasOpen = Boolean(target);
    target = value.target;
    // Capture parent scroll before display:none makes its offset unobservable.
    if (wasOpen !== Boolean(target)) onToggle(Boolean(target), target);
    root.hidden = !target;
    document.getElementById('conversation').classList.toggle('agent-reading', Boolean(target));
    if (!target) {
      rendered.clear(); timeline.replaceChildren();
      for (const id of ['agent-conversation-identity', 'agent-conversation-status']) find(id).textContent = '';
      return;
    }
    find('agent-conversation-identity').textContent = `${target.agentID} · generation ${target.generation}\n${target.executionSessionID} / ${target.threadID}`;
    const data = value.data;
    const notices = [];
    if (value.status === 'loading') notices.push(data ? 'Reading… Accepted history remains visible.' : 'Reading exact conversation…');
    if (value.error) notices.push(value.error);
    if (value.stale) notices.push('Session activity changed. Refresh explicitly to read the newest page.');
    if (data) {
      notices.push(`${data.mode === 'live_attach' ? 'Current execution snapshot' : data.mode === 'replay_only' ? 'Completed execution history' : 'Evicted thread history'} · runtime revision ${data.revision}`);
      notices.push('Active context only; earlier compaction or checkpoint history is not included.');
      if (!data.messages.length) notices.push('No messages returned for this exact conversation.');
      if (data.corruptions) notices.push('Some transcript records could not be read.');
      if (data.messages.length >= 256 && data.hasMore) notices.push('History window limit reached. Refresh to return to the newest page.');
    }
    find('agent-conversation-status').textContent = notices.join('\n');
    find('refresh-agent-conversation').disabled = value.status === 'loading';
    find('agent-conversation-older').hidden = !data?.hasMore;
    find('agent-conversation-older').disabled = value.status === 'loading' || data?.messages.length >= 256;
    const oldHeight = timeline.scrollHeight;
    const oldTop = timeline.scrollTop;
    const previousFirst = timeline.firstChild;
    const next = new Map();
    for (const message of data?.messages || []) {
      const signature = JSON.stringify(message);
      const prior = rendered.get(message.id);
      let card = prior?.signature === signature ? prior.node : null;
      if (!card) {
        card = node('article', `message ${message.role}`);
        card.dataset.messageId = message.id;
        card.append(node('span', 'label', `${message.role}${message.completed ? '' : ' · unfinished snapshot'}`));
        if (message.promptParts.length) {
          for (const part of message.promptParts) {
            card.append(part.kind === 'text' ? renderMessageContent(document, message.role, part.text) :
              node('p', 'composer-hint', `${part.kind} · ${part.mimeType || 'media descriptor'} · ${part.sizeBytes} bytes${part.width ? ` · ${part.width}×${part.height}` : ''}`));
          }
        } else card.append(renderMessageContent(document, message.role, message.content));
        if (message.reasoningContent) {
          const details = node('details', 'reasoning');
          details.append(node('summary', '', 'Reasoning'), node('div', '', message.reasoningContent));
          if (prior?.node.querySelector('details.reasoning')?.open) details.open = true;
          card.append(details);
        }
        for (const call of message.toolCalls) {
          const details = node('details', 'reasoning');
          details.append(node('summary', '', `${call.name || 'Tool'} · read-only call`), node('pre', 'agent-tool-preview', call.inputPreview));
          card.append(details);
        }
        if (message.truncated) card.append(node('p', 'composer-hint', 'This message preview is truncated.'));
      }
      next.set(message.id, { signature, node: card });
    }
    const nodes = new Set([...next.values()].map(item => item.node));
    for (const child of [...timeline.childNodes]) if (!nodes.has(child)) child.remove();
    let cursor = timeline.firstChild;
    for (const { node: card } of next.values()) {
      if (card === cursor) cursor = cursor.nextSibling; else timeline.insertBefore(card, cursor);
    }
    rendered = next;
    if (previousFirst && timeline.contains(previousFirst) && timeline.firstChild !== previousFirst) {
      timeline.scrollTop = oldTop + timeline.scrollHeight - oldHeight;
    }
  }
  const controller = createAgentConversationController({ read, changed: render });
  function close(focus = false) {
    const previous = controller.state().target; controller.close();
    if (focus && previous) returnFocus(previous);
  }
  find('back-to-parent-conversation').onclick = () => close(true);
  find('refresh-agent-conversation').onclick = () => controller.refresh();
  find('agent-conversation-older').onclick = () => controller.older();
  return {
    render(session, backendReady) { controller.updateContext({ session, backendReady }); render(controller.state()); },
    open(value) { controller.open(value); if (controller.state().target) find('back-to-parent-conversation').focus(); },
    isOpen: () => Boolean(controller.state().target), close,
  };
}
