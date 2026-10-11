// A bounded presentation of the canonical engine selector, not a task reducer.
const MAX_ROWS = 128;
const MAX_UINT64 = 18446744073709551615n;
const text = (value) => typeof value === 'string' ? [...value].slice(0, 512).join('') : '';
const count = (value) => Number.isSafeInteger(value) && value >= 0 ? value : 0;
const rows = (value) => Array.isArray(value) ? value.slice(0, MAX_ROWS) : [];
const strings = (value) => rows(value).map(text);

function decimal(value) {
  if (typeof value !== 'string' || !/^(0|[1-9][0-9]{0,19})$/.test(value) || BigInt(value) > MAX_UINT64) {
    throw new TypeError('Invalid task snapshot identity or revision.');
  }
  return value;
}

function identity(value) {
  const result = text(value);
  if (!result || result !== value) throw new TypeError('Invalid task snapshot row identity.');
  return result;
}

const attention = (value) => rows(value).map((item) => ({
  category: text(item?.category), work_item_id: text(item?.work_item_id),
  agent_id: text(item?.agent_id), generation: decimal(item?.generation),
  sequence: decimal(item?.sequence), reason: text(item?.reason),
}));
const hiddenMap = (value) => Object.fromEntries(
  Object.entries(value && typeof value === 'object' && !Array.isArray(value) ? value : {})
    .sort(([a], [b]) => a.localeCompare(b)).slice(0, 32)
    .map(([key, value]) => [[...key].slice(0, 128).join(''), count(value)]),
);

export function normalizeTaskExplorerSnapshot(value, sessionID) {
  if (!value || value.session_id !== sessionID || typeof value.available !== 'boolean') {
    throw new TypeError('Task snapshot belongs to another session or is malformed.');
  }
  if (['work_items', 'executions', 'links', 'attention', 'diagnostics'].some((key) => !Array.isArray(value[key]))) {
    throw new TypeError('Malformed task snapshot lists.');
  }
  const snapshot = {
    session_id: sessionID, available: value.available,
    unavailable_reason: text(value.unavailable_reason), board_id: text(value.board_id),
    revision: { board: decimal(value.revision?.board), runtime: decimal(value.revision?.runtime) },
    work_items: rows(value.work_items).map((item) => ({
      board_id: identity(item?.board_id), id: identity(item?.id), revision: decimal(item?.revision),
      order: count(item?.order), status: text(item?.status), title: text(item?.title),
      description: text(item?.description), active_form: text(item?.active_form), owner: text(item?.owner),
      result_summary: text(item?.result_summary), terminal_reason: text(item?.terminal_reason),
      blocks: strings(item?.blocks), blocked_by: strings(item?.blocked_by), attention: strings(item?.attention),
    })),
    executions: rows(value.executions).map((item) => ({
      agent_id: identity(item?.agent_id), generation: decimal(item?.generation), name: text(item?.name),
      execution_session_id: text(item?.execution_session_id) === item?.execution_session_id ? item.execution_session_id : '',
      thread_id: text(item?.thread_id) === item?.thread_id ? item.thread_id : '',
      task: text(item?.task), description: text(item?.description), activity: text(item?.activity),
      status: text(item?.status), phase: text(item?.phase), display_mode: text(item?.display_mode),
      last_tool_name: text(item?.last_tool_name), tool_use_count: count(item?.tool_use_count),
      token_count: count(item?.token_count), replay_only: item?.replay_only === true,
      predispatch: item?.predispatch === true, observation_ordinal: decimal(item?.observation_ordinal),
      ordinal_present: item?.ordinal_present === true, attention: strings(item?.attention),
    })),
    links: rows(value.links).map((item) => ({
      board_id: text(item?.board_id), work_item_id: text(item?.work_item_id), agent_id: text(item?.agent_id),
      generation: decimal(item?.generation), state: text(item?.state), unavailable_reason: text(item?.unavailable_reason),
    })),
    attention: attention(value.attention),
    diagnostics: rows(value.diagnostics).map((item) => ({
      sequence: decimal(item?.sequence), kind: text(item?.kind), item_id: text(item?.item_id), message: text(item?.message),
    })),
    hidden: {
      work_items: hiddenMap(value.hidden?.work_items), executions: hiddenMap(value.hidden?.executions),
      links: count(value.hidden?.links), attention: hiddenMap(value.hidden?.attention),
      work_board_outside_primary: count(value.hidden?.work_board_outside_primary),
      runtime_events_dropped: decimal(value.hidden?.runtime_events_dropped ?? '0'),
      execution_generations_evicted: decimal(value.hidden?.execution_generations_evicted ?? '0'),
      hidden_live_executions: decimal(value.hidden?.hidden_live_executions ?? '0'),
    },
  };
  const keys = taskExplorerRows(snapshot).map((row) => row.key);
  if (new Set(keys).size !== keys.length) throw new TypeError('Duplicate task snapshot identity.');
  return snapshot;
}

export function taskExplorerRows(snapshot, search = '', kind = 'all') {
  if (!snapshot) return [];
  const ordered = [
    ...snapshot.work_items.map((data) => ({
      kind: 'work', key: JSON.stringify([snapshot.session_id, 'work', data.board_id, data.id]),
      title: data.title || data.id, status: data.status || 'unknown', data,
    })),
    ...snapshot.executions.map((data) => ({
      kind: 'execution', key: JSON.stringify([snapshot.session_id, 'execution', data.agent_id, data.generation]),
      title: data.name || data.agent_id, status: data.phase || data.status || 'unknown', data,
    })),
  ];
  const query = text(search).trim().toLocaleLowerCase();
  return ordered.filter((row) => (kind === 'all' || row.kind === kind) &&
    (!query || [row.title, row.status, row.data.id, row.data.agent_id, row.data.description,
      row.data.task, row.data.owner, row.data.activity].some((value) => value?.toLocaleLowerCase().includes(query))));
}

export function canReadTaskExplorer(session, backendReady = true) {
  return Boolean(backendReady && session?.live &&
    !['closed', 'offline', 'restoring', 'saved', 'archived'].includes(session.status));
}

const readable = (value) => String(value || '').replaceAll('_', ' ');

export function createTaskExplorerView({ document, list, status, summary, refresh, search, kind,
  onInspect, inspectionReason = () => 'Execution detail unavailable.' }) {
  let previous = null;
  let nodes = new Map();
  const element = (tag, className, content) => {
    const node = document.createElement(tag);
    node.className = className;
    if (content !== undefined) node.textContent = content;
    return node;
  };
  function facts(row, snapshot) {
    const data = row.data;
    const lines = row.kind === 'work' ? [
      ['Board', data.board_id], ['Work item', data.id], ['Revision', data.revision],
      ['Owner', data.owner], ['Description', data.description], ['Active form', data.active_form],
      ['Result', data.result_summary], ['Terminal reason', data.terminal_reason],
      ['Blocks', data.blocks.join(', ')], ['Blocked by', data.blocked_by.join(', ')],
    ] : [
      ['Agent', data.agent_id], ['Generation', data.generation], ['Status', readable(data.status)],
      ['Phase', readable(data.phase)], ['Mode', readable(data.display_mode)], ['Task', data.task],
      ['Description', data.description], ['Activity', data.activity], ['Last tool', data.last_tool_name],
      ['Tool uses', String(data.tool_use_count)], ['Tokens', String(data.token_count)],
      ['Attachment', data.replay_only ? 'Replay only' : (data.predispatch ? 'Pre-dispatch' : '')],
    ];
    for (const link of snapshot.links) {
      const matches = row.kind === 'work'
        ? link.board_id === data.board_id && link.work_item_id === data.id
        : link.agent_id === data.agent_id && link.generation === data.generation;
      if (matches) lines.push(['Link', `${link.work_item_id} ↔ ${link.agent_id} · generation ${link.generation} · ${readable(link.state)}${link.unavailable_reason ? ` · ${link.unavailable_reason}` : ''}`]);
    }
    for (const item of data.attention) lines.push(['Attention', readable(item)]);
    return lines.filter(([, value]) => value !== '').map(([label, value]) => {
      const item = element('div', 'task-fact');
      item.append(element('dt', '', label), element('dd', '', value));
      return item;
    });
  }
  function render(session, backendReady) {
    const taskState = session?.tasks || {};
    const available = canReadTaskExplorer(session, backendReady);
    const snapshot = available ? taskState.snapshot : null;
    refresh.disabled = !available || taskState.status === 'loading';
    search.disabled = !snapshot;
    kind.disabled = !snapshot;
    const visible = taskExplorerRows(snapshot, search.value, kind.value);
    const summaryText = snapshot
      ? `${snapshot.work_items.length} work items · ${snapshot.executions.length} executions · ${visible.length} shown`
      : 'Read-only task overview';
    if (summary.textContent !== summaryText) summary.textContent = summaryText;
    const notices = [];
    if (!backendReady) notices.push('Backend stopped. Restart YHC to reconnect.');
    else if (!available) notices.push('Open a live session to inspect tasks. Saved history is not attached by this view.');
    else if (taskState.status === 'loading') notices.push(snapshot ? 'Refreshing… Previous snapshot remains visible.' : 'Reading tasks…');
    else if (taskState.status === 'error') notices.push(`Task read failed: ${taskState.error}`);
    else if (!snapshot) notices.push('Refresh to read the current session’s tasks.');
    if (snapshot) {
      if (!snapshot.available) notices.push(`Work board unavailable: ${snapshot.unavailable_reason || 'unknown'}. Available execution rows remain visible.`);
      if (taskState.stale) notices.push('Session activity changed or the refresh failed. Refresh to check the latest task status.');
      notices.push(`Observed revisions · board ${snapshot.revision.board} · runtime ${snapshot.revision.runtime}`);
      const hidden = snapshot.hidden;
      const counts = [
        ...Object.entries(hidden.work_items).map(([key, value]) => [`work ${key}`, value]),
        ...Object.entries(hidden.executions).map(([key, value]) => [`execution ${key}`, value]),
        ...Object.entries(hidden.attention).map(([key, value]) => [`attention ${key}`, value]),
        ['links', hidden.links], ['other board items', hidden.work_board_outside_primary],
        ['runtime events dropped', hidden.runtime_events_dropped],
        ['execution generations evicted', hidden.execution_generations_evicted],
        ['live executions hidden', hidden.hidden_live_executions],
      ].filter(([, value]) => value !== 0 && value !== '0');
      if (counts.length) notices.push(`Not shown · ${counts.map(([label, value]) => `${label}: ${value}`).join(' · ')}`);
    }
    const statusText = notices.join('\n');
    if (status.textContent !== statusText) status.textContent = statusText;
    const signature = [session?.id, snapshot, taskState.status, taskState.stale, available, search.value, kind.value];
    if (previous && signature.every((value, index) => value === previous[index])) return;
    previous = signature;
    const active = document.activeElement;
    const focusedKey = [...nodes].find(([, node]) => node.contains(active))?.[0];
    const scrollTop = list.scrollTop;
    const next = new Map();
    for (const row of visible) {
      let node = nodes.get(row.key);
      if (!node) {
        node = element('details', 'task-row');
        node.dataset.taskKey = row.key;
        node.dataset.kind = row.kind;
        node.append(element('summary', 'task-row-heading'), element('dl', 'task-facts'));
      }
      const header = node.firstElementChild;
      header.replaceChildren(element('span', 'task-kind', row.kind === 'work' ? 'Work item' : 'Execution'),
        element('strong', 'task-title', row.title),
        ...(row.kind === 'execution' ? [element('span', 'task-generation', `generation ${row.data.generation}`)] : []),
        element('span', 'task-phase', readable(row.status)));
      node.lastElementChild.replaceChildren(...facts(row, snapshot));
      if (row.kind === 'execution') {
        const action = element('div', 'task-fact');
        action.append(element('dt', '', 'Exact execution detail'));
        const content = element('dd', 'task-actions');
        const button = node.inspectButton || element('button', 'quiet task-inspect', 'Inspect execution');
        node.inspectButton = button; button.type = 'button';
        node.inspectionData = row.data;
        const reason = inspectionReason(row.data);
        button.disabled = !available || Boolean(reason);
        button.onclick = () => onInspect?.(row.data);
        content.append(button);
        if (reason) content.append(element('span', 'task-status', reason));
        action.append(content); node.lastElementChild.append(action);
      }
      next.set(row.key, node);
    }
    for (const [key, node] of nodes) if (!next.has(key)) node.remove();
    for (const child of [...list.children]) if (!child.matches('.task-row')) child.remove();
    let index = 0;
    for (const node of next.values()) {
      if (list.children[index] !== node) list.insertBefore(node, list.children[index] || null);
      index += 1;
    }
    if (!visible.length) list.append(element('p', 'activity-empty', !snapshot
      ? 'Tasks appear here after a live-session read.'
      : (search.value || kind.value !== 'all' ? 'No tasks match these filters.' : 'No tasks in this snapshot.')));
    if (snapshot) {
      for (const item of snapshot.attention) list.append(element('p', 'task-notice', `Attention · ${readable(item.category)} · ${item.reason}`));
      for (const item of snapshot.diagnostics) list.append(element('p', 'task-notice', `Diagnostic · ${item.kind} · ${item.message}`));
    }
    nodes = next;
    if (focusedKey && next.has(focusedKey) && document.activeElement !== active) {
      next.get(focusedKey).firstElementChild.focus({ preventScroll: true });
    }
    if (focusedKey && !next.has(focusedKey) && available) {
      // Missing identities never transfer disclosure/focus to another Agent.
      (refresh.disabled ? search : refresh).focus({ preventScroll: true });
    }
    list.scrollTop = scrollTop;
  }
  return { render, focusExecution(target) {
    const key = JSON.stringify([target.sessionID, 'execution', target.agentID, target.generation]);
    const node = nodes.get(key);
    if (node?.inspectButton && !node.inspectButton.disabled &&
        node.inspectionData.execution_session_id === target.executionSessionID && node.inspectionData.thread_id === target.threadID) {
      node.open = true; node.inspectButton.focus({ preventScroll: true });
    } else (refresh.disabled ? search : refresh).focus({ preventScroll: true });
  } };
}
