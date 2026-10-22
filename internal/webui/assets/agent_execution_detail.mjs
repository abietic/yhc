import { canReadTaskExplorer } from './task_explorer.mjs';

const FIELDS = new Set(['sessionID', 'agentID', 'generation', 'executionSessionID', 'threadID', 'includeOutput']);
const MAX_GENERATION = 9223372036854775807n;
const text = value => typeof value === 'string' ? [...value].slice(0, 512).join('') : '';
const count = value => Number.isSafeInteger(value) && value >= 0 ? value : 0;
const validID = value => typeof value === 'string' && value.length > 0 && value.trim() === value &&
  [...value].length <= 512 && !/\p{Cc}/u.test(value) && value.isWellFormed();
const validGeneration = value => typeof value === 'string' && /^[1-9][0-9]{0,18}$/.test(value) && BigInt(value) <= MAX_GENERATION;
const targetFor = (sessionID, row) => ({ sessionID, agentID: row.agent_id, generation: row.generation,
  executionSessionID: row.execution_session_id, threadID: row.thread_id });
const matches = (target, row) => target.agentID === row.agent_id && target.generation === row.generation &&
  target.executionSessionID === row.execution_session_id && target.threadID === row.thread_id;

export function executionDetailQuery(payload) {
  if (!payload || typeof payload !== 'object' || Array.isArray(payload) ||
      Object.keys(payload).some(key => !FIELDS.has(key)) ||
      ![payload.agentID, payload.executionSessionID, payload.threadID].every(validID) ||
      !validGeneration(payload.generation) ||
      (payload.includeOutput !== undefined && typeof payload.includeOutput !== 'boolean')) {
    throw new TypeError('Exact execution identity required.');
  }
  return new URLSearchParams({ agent_id: payload.agentID, generation: payload.generation,
    execution_session_id: payload.executionSessionID, thread_id: payload.threadID,
    include_output: String(payload.includeOutput === true) });
}

export function executionDetailEligibility(row) {
  if (row?.replay_only) return 'Replay-only execution. No live detail attachment.';
  if (row?.predispatch) return 'Execution has not been dispatched.';
  if (!validGeneration(row?.generation) || ![row?.agent_id, row?.execution_session_id, row?.thread_id].every(validID)) {
    return 'Exact execution lineage is unavailable.';
  }
  return '';
}

function outputTail(value) {
  const bytes = new TextEncoder().encode(typeof value === 'string' ? value : '');
  if (bytes.length <= 65536) return { text: new TextDecoder().decode(bytes), truncated: false };
  let start = bytes.length - 65536;
  while ((bytes[start] & 0xC0) === 0x80) start += 1;
  return { text: new TextDecoder().decode(bytes.subarray(start)), truncated: true };
}

export function normalizeAgentExecutionDetail(value, target, includeOutput) {
  if (!value || value.session_id !== target.sessionID || !matches(target, value) ||
      typeof value.revision !== 'string' || !/^(0|[1-9][0-9]{0,19})$/.test(value.revision) ||
      BigInt(value.revision) > 18446744073709551615n || !value.metadata ||
      value.output?.requested !== includeOutput || typeof value.output.text !== 'string' ||
      typeof value.output.truncated !== 'boolean' ||
      !['', 'output_read_failed'].includes(value.output.error_category)) {
    throw new TypeError('Execution detail identity or response is invalid.');
  }
  const metadata = Object.fromEntries(['name', 'task', 'description', 'activity', 'status', 'last_tool_name',
    'parent_session_id', 'parent_thread_id', 'parent_agent_id', 'parent_tool_use_id'].map(key => [key, text(value.metadata[key])]));
  metadata.tool_use_count = count(value.metadata.tool_use_count);
  metadata.token_count = count(value.metadata.token_count);
  const output = includeOutput && !value.output.error_category ? outputTail(value.output.text) : { text: '', truncated: false };
  return { ...target, revision: value.revision, metadata, output: { requested: includeOutput,
    text: output.text, truncated: output.truncated || (includeOutput && !value.output.error_category && value.output.truncated),
    error_category: includeOutput ? value.output.error_category : '' } };
}

const emptyPage = () => ({ status: 'idle', data: null, error: '', stale: false });
const emptyState = () => ({ target: null, tab: 'lineage', pages: { lineage: emptyPage(), output: emptyPage() } });
function readError(error) {
  if (error?.code === 'agent_execution_selection_changed') return 'This exact execution is no longer current. Return to Tasks and refresh.';
  if (error?.code === 'agent_execution_not_found') return 'This exact execution is no longer available. Return to Tasks and refresh.';
  if (error?.code === 'agent_execution_detail_unavailable') return 'Execution detail is unavailable in this backend.';
  return 'Execution detail could not be read. Retry or return to Tasks.';
}

// Local view state, never durable task state. Every async completion must still
// belong to the active view, owned session, exact lineage, tab and request.
export function createAgentExecutionDetailController({ read, changed = () => {} }) {
  let context = null;
  let current = emptyState();
  let requestID = 0;
  const active = () => context?.visible && canReadTaskExplorer(context.session, context.backendReady);
  const eligible = target => active() && context.session.id === target.sessionID &&
    context.session.tasks?.snapshot?.executions.filter(row => matches(target, row) && !executionDetailEligibility(row)).length === 1;
  function close() { requestID += 1; current = emptyState(); changed(current); }
  function retirePending() {
    requestID += 1;
    for (const page of Object.values(current.pages)) if (page.status === 'loading') page.status = page.data ? 'ready' : 'idle';
  }
  function load() {
    if (!current.target || !eligible(current.target)) return;
    const id = ++requestID;
    const target = current.target;
    const tab = current.tab;
    const page = current.pages[tab];
    Object.assign(page, { status: 'loading', error: '', stale: false });
    changed(current);
    new Promise(resolve => resolve(read({ ...target, includeOutput: tab === 'output' }))).then(value => {
      if (id !== requestID || current.target !== target || current.tab !== tab || !eligible(target)) return;
      try { page.data = normalizeAgentExecutionDetail(value, target, tab === 'output'); page.status = 'ready'; }
      catch { page.status = 'error'; page.error = 'Execution detail identity or response is invalid.'; page.stale = Boolean(page.data); }
      changed(current);
    }, error => {
      if (id !== requestID || current.target !== target || current.tab !== tab || !eligible(target)) return;
      Object.assign(page, { status: 'error', error: readError(error), stale: Boolean(page.data) }); changed(current);
    });
  }
  return {
    state: () => current,
    updateContext(next) {
      const previous = context;
      context = next;
      if (current.target && (!eligible(current.target) || previous?.session?.tasks?.snapshot !== next.session?.tasks?.snapshot)) close();
      else if (current.target && next.session.tasks?.stale && previous?.session?.tasks !== next.session.tasks) {
        for (const page of Object.values(current.pages)) if (page.data || page.status === 'loading') page.stale = true;
        changed(current);
      }
    },
    select(row) {
      if (!active() || executionDetailEligibility(row)) return;
      const target = targetFor(context.session.id, row);
      if (!eligible(target)) return;
      retirePending(); current = emptyState(); current.target = target; load();
    },
    selectTab(tab) {
      if (!current.target || !['lineage', 'output'].includes(tab) || current.tab === tab) return;
      retirePending(); current.tab = tab;
      if (current.pages[tab].status === 'idle') load(); else changed(current);
    },
    refresh: load,
    close,
  };
}

export function createAgentExecutionDetailView({ document, root, overview, read, returnFocus, openConversation }) {
  const find = id => document.getElementById(id);
  const controller = createAgentExecutionDetailController({ read, changed: render });
  const tabs = ['lineage', 'output'];
  function render(state) {
    root.hidden = !state.target;
    overview.hidden = Boolean(state.target);
    if (!state.target) {
      find('execution-lineage-facts').replaceChildren();
      for (const id of ['execution-detail-title', 'execution-detail-identity', 'execution-detail-status', 'execution-output', 'execution-output-note']) {
        find(id).textContent = '';
      }
      return;
    }
    const page = state.pages[state.tab];
    const data = page.data;
    find('open-agent-conversation').disabled = !state.pages.lineage.data || page.status === 'loading';
    find('execution-detail-title').textContent = data?.metadata.name || state.target.agentID;
    find('execution-detail-identity').textContent = `${state.target.agentID} · generation ${state.target.generation}`;
    find('refresh-execution-detail').disabled = page.status === 'loading';
    for (const tab of tabs) {
      const selected = state.tab === tab;
      const button = find(`execution-${tab}-tab`);
      button.setAttribute('aria-selected', String(selected)); button.tabIndex = selected ? 0 : -1;
      find(`execution-${tab}-panel`).hidden = !selected;
    }
    const notices = [];
    if (page.status === 'loading') notices.push(page.data ? 'Refreshing… Previous detail remains visible.' : 'Reading exact execution…');
    if (page.error) notices.push(page.error);
    if (page.stale) notices.push('Session activity changed or the read failed. Refresh to check this exact execution.');
    if (data) notices.push(`Observed runtime revision ${data.revision}`);
    const status = notices.join('\n');
    if (find('execution-detail-status').textContent !== status) find('execution-detail-status').textContent = status;
    const lines = data ? [
      ['Execution session', data.executionSessionID], ['Thread', data.threadID], ['Status', data.metadata.status],
      ['Task', data.metadata.task], ['Description', data.metadata.description], ['Activity', data.metadata.activity],
      ['Last tool', data.metadata.last_tool_name], ['Tool uses', String(data.metadata.tool_use_count)], ['Tokens', String(data.metadata.token_count)],
      ['Parent session', data.metadata.parent_session_id], ['Parent thread', data.metadata.parent_thread_id],
      ['Parent Agent', data.metadata.parent_agent_id], ['Spawn tool use', data.metadata.parent_tool_use_id],
    ] : [];
    find('execution-lineage-facts').replaceChildren(...lines.filter(([, value]) => value).map(([label, value]) => {
      const item = document.createElement('div'); item.className = 'task-fact';
      const dt = document.createElement('dt'); dt.textContent = label;
      const dd = document.createElement('dd'); dd.textContent = value;
      item.append(dt, dd); return item;
    }));
    const outputText = state.tab === 'output' ? data?.output.text || '' : '';
    if (find('execution-output').textContent !== outputText) find('execution-output').textContent = outputText;
    find('execution-output-note').textContent = state.tab !== 'output' || !data ? '' :
      (data.output.error_category ? 'Output could not be read. No path or raw error is displayed.' :
        (data.output.truncated ? 'Showing the last 64 KiB or less; earlier output is omitted.' :
          (!data.output.text ? 'No output returned for this exact execution. Output is read only for a current terminal generation.' : 'Read-only output for this exact execution.')));
  }
  function close(focus = false) {
    const target = controller.state().target;
    controller.close();
    if (focus && target) returnFocus(target);
  }
  find('back-to-tasks').onclick = () => close(true);
  find('refresh-execution-detail').onclick = () => controller.refresh();
  find('open-agent-conversation').onclick = () => {
    if (controller.state().pages.lineage.data) openConversation(controller.state().target);
  };
  tabs.forEach((tab, index) => {
    const button = find(`execution-${tab}-tab`);
    button.onclick = () => controller.selectTab(tab);
    button.onkeydown = event => {
      let target;
      if (['ArrowLeft', 'ArrowRight'].includes(event.key)) target = 1 - index;
      if (event.key === 'Home') target = 0;
      if (event.key === 'End') target = 1;
      if (target !== undefined) {
        event.preventDefault(); controller.selectTab(tabs[target]); find(`execution-${tabs[target]}-tab`).focus();
      }
    };
  });
  return {
    render(session, backendReady, visible) { controller.updateContext({ session, backendReady, visible }); render(controller.state()); },
    select(row) { controller.select(row); if (controller.state().target) find('execution-lineage-tab').focus(); },
    close,
    isOpen: () => Boolean(controller.state().target),
  };
}
