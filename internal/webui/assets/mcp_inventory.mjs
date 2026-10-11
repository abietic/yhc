const choice = (value, values) => values.includes(value) ? value : 'unknown';
const text = value => typeof value === 'string' ? [...value.replace(/[\u0000-\u001f\u007f]/g, ' ')].slice(0, 512).join('') : '';
const count = value => Number.isSafeInteger(value) && value >= 0 ? value : 0;
const diagnostics = Object.freeze({
  connect_failed: 'Connection failed', approval_denied: 'Connection approval denied',
  connection_closed: 'Connection closed', inspection_only_no_connection: 'Configured only · connection not probed',
  tool_discovery_failed: 'Tool discovery failed', tool_identity_invalid: 'Tool identity validation failed',
  reconnect_failed: 'Reconnect failed', unavailable: 'Diagnostic unavailable',
});
const statusLabels = Object.freeze({connected: 'Connected', connecting: 'Connecting', disconnected: 'Disconnected', error: 'Error', failed: 'Failed', 'needs-auth': 'Needs authentication', disabled: 'Disabled', configured: 'Configured only', unknown: 'Unknown state'});
const healthLabels = Object.freeze({healthy: 'Healthy', degraded: 'Degraded', unavailable: 'Unavailable', unprobed: 'Not probed', unknown: 'Unknown health'});
const sourceLabels = Object.freeze({'runtime-manager': 'Session runtime', configuration: 'Saved configuration', unknown: 'Unknown source'});

export function normalizeMCPInventory(raw, sessionID) {
  if (!sessionID || raw?.session_id !== sessionID || typeof raw.revision !== 'string' ||
      !/^(0|[1-9][0-9]{0,19})$/.test(raw.revision) || BigInt(raw.revision) > 18446744073709551615n ||
      !Array.isArray(raw.servers) || raw.servers.length > 64) throw new TypeError('Invalid MCP inventory');
  let total = 0;
  const servers = raw.servers.map(server => {
    if (!server || !Array.isArray(server.tools) || server.tools.length > 64 || (total += server.tools.length) > 512) throw new TypeError('Invalid MCP tools');
    return {
      name: text(server.name), source: choice(server.source, ['runtime-manager', 'configuration']),
      status: choice(server.status, ['connected', 'connecting', 'disconnected', 'error', 'failed', 'needs-auth', 'disabled', 'configured']),
      health: choice(server.health, ['healthy', 'degraded', 'unavailable', 'unprobed']),
      diagnostic: server.diagnostic === '' ? '' : Object.hasOwn(diagnostics, server.diagnostic) ? server.diagnostic : 'unavailable',
      tools: server.tools.map(tool => {
        if (!tool || typeof tool.name !== 'string' || typeof tool.description !== 'string') throw new TypeError('Invalid MCP tool');
        return {name: text(tool.name), description: text(tool.description), truncated: tool.truncated === true};
      }),
      hidden_tools: count(server.hidden_tools), truncated: server.truncated === true,
    };
  });
  return {session_id: sessionID, revision: raw.revision, source: choice(raw.source, ['runtime', 'configuration']), servers, hidden_servers: count(raw.hidden_servers), hidden_tools: count(raw.hidden_tools)};
}

export function createMCPInventoryController({api, onChange}) {
  let context = {};
  let epoch = 0;
  let value = {status: 'idle', data: null, error: ''};
  const available = () => context.visible && context.backendReady && Boolean(context.sessionID);
  const publish = next => {value = next; onChange(value);};
  return {
    state: () => value,
    updateContext(next) {
      if (next.sessionID !== context.sessionID || next.visible !== context.visible || next.backendReady !== context.backendReady) {
        epoch++; context = {...next}; publish({status: 'idle', data: null, error: ''});
      } else context = {...next};
    },
    async load() {
      if (!available()) return;
      const request = ++epoch;
      const sessionID = context.sessionID;
      publish({status: 'loading', data: null, error: ''});
      try {
        const raw = await api('getMCPInventory', {sessionID});
        if (request !== epoch || !available()) return;
        publish({status: 'ready', data: normalizeMCPInventory(raw, sessionID), error: ''});
      } catch (error) {
        if (request !== epoch || !available()) return;
        publish({status: 'error', data: null, error: error?.code === 'mcp_inventory_unavailable' ? 'MCP inspection is unavailable for this runtime.' : error?.code === 'session_not_found' ? 'This session is no longer available. Open a live session.' : 'Could not load MCP inventory. Refresh to retry.'});
      }
    },
  };
}

export function createMCPInventoryView({root, api, context}) {
  const node = (tag, className, value) => {const item = document.createElement(tag); item.className = className; if (value !== undefined) item.textContent = value; return item;};
  const heading = node('div', 'mcp-heading');
  const title = node('h3', '', 'MCP servers');
  const refresh = node('button', 'quiet', 'Refresh'); refresh.type = 'button';
  heading.append(title, refresh);
  const intro = node('p', 'setting-description', 'Current session · Read-only connection inventory. Permission rules and the active model tool list may differ.');
  const status = node('p', 'mcp-status'); status.setAttribute('role', 'status'); status.setAttribute('aria-live', 'polite');
  const list = node('div', 'mcp-servers');
  root.append(heading, intro, status, list);
  let visible = false;
  const controller = createMCPInventoryController({api, onChange: paint});
  function paint(value) {
    const current = context();
    refresh.disabled = !current.sessionID || !current.backendReady || value.status === 'loading';
    status.title = '';
    list.replaceChildren();
    if (!current.backendReady) {status.textContent = 'Backend offline. Reconnect before inspecting MCP.'; return;}
    if (!current.sessionID) {status.textContent = 'Open a live session to inspect its MCP servers.'; return;}
    if (value.status === 'loading') {status.textContent = 'Loading MCP inventory…'; return;}
    if (value.error) {status.textContent = value.error; return;}
    if (!value.data) {status.textContent = 'Refresh to inspect this session.'; return;}
    const data = value.data;
    status.title = `Inventory generation ${data.revision}`;
    status.textContent = `${data.servers.length} servers · Read-only snapshot` +
      (data.hidden_servers || data.hidden_tools ? ` · ${data.hidden_servers} servers and ${data.hidden_tools} tools omitted by display limits` : '');
    if (!data.servers.length) list.append(node('p', 'mcp-empty', 'No MCP servers in this session inventory. Reading this view does not connect or probe servers.'));
    for (const server of data.servers) {
      const card = node('section', 'mcp-server');
      const header = node('div', 'mcp-server-heading');
      header.append(node('h4', '', server.name || 'Unnamed server'), node('span', 'mcp-state', statusLabels[server.status]));
      card.append(header, node('p', 'setting-source', `${sourceLabels[server.source]} · ${healthLabels[server.health]} · ${server.tools.length} tool${server.tools.length === 1 ? '' : 's'} shown`));
      if (server.diagnostic) card.append(node('p', 'mcp-diagnostic', diagnostics[server.diagnostic]));
      if (server.truncated || server.hidden_tools) card.append(node('p', 'mcp-limit', `${server.hidden_tools} tools omitted${server.truncated ? ' · Server name shortened' : ''}`));
      if (server.tools.length) {
        const details = node('details', 'mcp-tools'); details.append(node('summary', '', 'View tools'));
        for (const tool of server.tools) {
          const item = node('div', 'mcp-tool'); item.append(node('code', '', tool.name), node('p', '', tool.description));
          if (tool.truncated) item.append(node('span', 'mcp-limit', 'Description or name shortened'));
          details.append(item);
        }
        card.append(details);
      }
      list.append(card);
    }
  }
  function updateContext() {controller.updateContext({...context(), visible});}
  refresh.onclick = () => {updateContext(); void controller.load();};
  return {
    setVisible(next) {const entered = next && !visible; visible = next; root.hidden = !next; updateContext(); if (entered) void controller.load();},
    updateContext,
  };
}
