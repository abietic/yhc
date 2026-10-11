const assert = require('node:assert/strict');
// Optional renderer acceptance. This script is not part of the default test gate.
// Install Playwright separately or set PLAYWRIGHT_MODULE_PATH to an existing
// module. YHC_UI_BROWSER_EXECUTABLE optionally selects an installed browser.
// Usage: node desktop/scripts/workbench_ui_acceptance.cjs --output-dir DIR
// Fixtures model the public configuration schema, not a live Go/provider service.
const { chromium } = require(process.env.PLAYWRIGHT_MODULE_PATH || 'playwright');
const http = require('node:http');
const fs = require('node:fs/promises');
const path = require('node:path');
const os = require('node:os');
const { listenFixtureServer } = require('./fixture_server_startup.cjs');

const root = path.resolve(__dirname, '../../internal/webui/assets');
const results = [];
const errors = [];
const streams = new Map();
const heldReads = [];
const heldWrites = [];
const heldPermissionWrites = [];
const heldSuggestions = [];
const heldTurns = [];
const heldCloses = [];
const heldTasks = [];
const heldDetails = [];
let holdNextDetail = false;
let failNextDetail = false;
let detailMode = 'normal';
const detailReads = [];
const heldAgentPages = [];
const agentPageReads = [];
let holdNextAgentPage = false;
let failNextAgentPage = false;
let agentPageMode = 'live_attach';
let holdNextTasks = false;
let failNextTasks = false;
let taskSnapshotMode = 'normal';
let taskReads = 0;
let holdNextTurn = false;
let holdNextClose = false;
let failNextTurn = false;
let holdNextSuggestion = false;
let suggestionRequests = 0;
let holdNextSettingsRead = false;
let holdNextSettingsWrite = false;
let holdNextPermissionWrite = false;
let failNextSettingsRead = false;
let failNextSettingsWrite = false;
let conflictNext = false;
let revision = 1;
const stored = { theme: 'daybreak', provider: 'openai' };
const projectStored = {};
const settingsWrites = [];
let unsafeIntegerSnapshot = false;
let mcpReads = 0;
let mcpMode = 'normal';
let permissionReads = 0;
const permissionWrites = [];
let conflictNextPermission = false;
const permissionRules = new Map();
let creations = 0;
let sessionSettingWrites = 0;
let turnRequests = 0;
let homeHistory = false;
const observedOperations = [];
const goals = new Map();
const goalWrites = [];
let conflictNextGoal = false;
function goalState(id) {
  return { session_id: id, available: true, diagnostic: '',
    default_token_budget: '9007199254740993', goal: goals.get(id) || null };
}
const session = { id: 'acceptance-session', title: 'Improve the search experience', workspace_label: 'demo-workspace', status: 'idle', active_turn_id: '', resumable: true };
const secondSession = { ...session, id: 'acceptance-second-session', title: 'Review the keyboard flow' };
const messages = [
  { id: 'user-1', role: 'user', content: 'Make search fast and easy to understand.', completed: true, turn_id: 'old-turn' },
  { id: 'assistant-1', role: 'assistant', content: '## A clearer search experience\n\nThe search flow should preserve your context and show useful results immediately.\n\n- Search by session title or workspace.\n- Keep your draft when changing settings.\n- Show a useful empty state.\n\n```js\nconst matches = sessions.filter(matchesQuery);\n```', reasoning_content: 'I checked the current interaction and selected a small, testable improvement.', completed: true, turn_id: 'old-turn' },
];
// Keep this bounded, secret-free fixture aligned with
// engine/config/workbench_settings.go:workbenchSettingDefinitions.
const definitions = {
  provider: { label: 'Provider', type: 'string', group: 'model', description: 'Default provider; launch flags and environment may override this value.', choice_only: true, options: ['anthropic', 'openai', 'google', 'deepseek', 'glm', 'qwen', 'ark'], default: '' },
  model: { label: 'Model', type: 'string', group: 'model', description: 'Default model for new sessions. Use a provider model identifier or configured alias.', default: 'default' },
  model_profile: { label: 'Model profile', type: 'string', group: 'model', description: 'Select an existing user-owned model profile; cannot be combined with legacy provider fields.', choice_only: true, default: '' },
  api_base_url: { label: 'API endpoint', type: 'string', group: 'model', description: 'Optional HTTP(S) provider endpoint, without credentials, query parameters, or fragment.', default: '' },
  max_turns: { label: 'Maximum turns', type: 'integer', group: 'execution', description: 'Maximum agent turns per interaction; zero means unlimited.', default: 0 },
  permission_mode: { label: 'Permission mode', type: 'string', group: 'execution', description: 'Default permission mode for new sessions. Bypass is available only through explicitly confirmed execution controls.', choice_only: true, options: ['default', 'plan', 'acceptEdits', 'dontAsk', 'auto'], default: 'default' },
  theme: { label: 'Theme', type: 'string', group: 'appearance', description: 'Shared theme preference for the next application or terminal session.', choice_only: true, options: ['polar-night', 'daybreak', 'dark-ansi', 'light-ansi', 'snowy', 'aubergine', 'dark', 'light'], aliases: { dark: 'polar-night', light: 'daybreak' }, default: 'dark' },
  reduced_motion: { label: 'Reduced motion', type: 'boolean', group: 'appearance', description: 'Disable non-essential animation.', default: false },
  prompt_suggestions: { label: 'Prompt suggestions', type: 'boolean', group: 'appearance', description: 'Enable model-backed next-prompt suggestions in supported clients.', default: true },
};
definitions.model_profile.conflicts_with = ['provider', 'model', 'api_base_url'];
for (const key of definitions.model_profile.conflicts_with) definitions[key].conflicts_with = ['model_profile'];
function settings(scope = 'user') {
  const selected = scope === 'project' ? projectStored : stored;
  return { scope, revision: String(revision).padStart(64, '0'), fields: Object.fromEntries(
    Object.entries(definitions).map(([key, definition]) => [key, {
      ...definition,
      stored_value: unsafeIntegerSnapshot && key === 'max_turns' ? JSON.parse('9007199254740993') : Object.hasOwn(selected, key) ? selected[key] : null,
      effective_value: unsafeIntegerSnapshot && key === 'max_turns' ? JSON.parse('9007199254740993') : Object.hasOwn(projectStored, key) ? projectStored[key] : Object.hasOwn(stored, key) ? stored[key] : definition.default,
      source: Object.hasOwn(projectStored, key) ? 'project' : Object.hasOwn(stored, key) ? 'user' : 'default',
      overridden: scope === 'user' && Object.hasOwn(projectStored, key),
      apply: 'new_session',
    }]),
  ) };
}
function operation(op, input = {}, conversation = false) {
  observedOperations.push(op);
  if (op === 'getGoal') return goalState(input.sessionID);
  if (op === 'controlGoal') {
    goalWrites.push(structuredClone(input));
    const goal = goals.get(input.sessionID);
    if (conflictNextGoal || input.expected_goal_id !== (goal?.goal_id || '') ||
      input.expected_revision !== (goal?.revision || '0')) {
      conflictNextGoal = false;
      return { fixtureError: true, status: 409, code: 'goal_conflict', message: '/private/fixture-goal-error' };
    }
    const next = goal ? { ...goal, revision: String(BigInt(goal.revision) + 1n) } : {
      goal_id: 'goal-fixture', objective: input.objective, status: 'active', revision: '1',
      token_budget: input.token_budget || '9007199254740993', tokens_used: '0',
      root_active_time_ms: '0', usage_coverage: 'complete', status_reason_code: '', status_reason: '',
    };
    if (input.operation === 'edit') next.objective = input.objective;
    if (input.operation === 'budget') next.token_budget = input.token_budget;
    if (input.operation === 'pause') next.status = 'paused';
    if (input.operation === 'resume') next.status = 'active';
    if (input.operation === 'continue') next.revision = goal.revision;
    if (input.operation === 'clear') goals.delete(input.sessionID);
    else goals.set(input.sessionID, next);
    return { state: goalState(input.sessionID), saved: input.operation !== 'continue', started: false };
  }
  if (op === 'getPermissionSettings' || op === 'updatePermissionSettings') {
    const scope=input.scope || 'user', owner=input.sessionID || input.session_id || '';
    const key=scope==='user' ? scope : `${owner}:${scope}`;
    if(!permissionRules.has(key)) permissionRules.set(key,[
      {action:'deny',value:'<img src=x>',format:'structured',editable:true,truncated:false,tool_name:'<img src=x>',input_pattern:''},
      {action:'ask',value:'Bash(echo *):ask',format:'legacy',editable:false,truncated:false,tool_name:'Bash',input_pattern:'echo *'},
    ]);
    if(op==='getPermissionSettings') permissionReads++;
    else {
      permissionWrites.push(structuredClone(input));
      if(conflictNextPermission || input.revision!==String(revision).padStart(64,'0')) {
        conflictNextPermission=false;
        return {fixtureError:true,status:409,code:'settings_conflict',message:'/private/synthetic-permission-error'};
      }
      for(const change of input.changes) {
        if(change.operation==='remove') permissionRules.set(key,permissionRules.get(key).filter(row=>!(row.format==='structured' && row.action===change.action && row.value===change.rule)));
        else permissionRules.get(key).push({action:change.action,value:change.rule,format:'structured',editable:true,truncated:false,tool_name:change.rule,input_pattern:''});
      }
      revision++;
    }
    return {session_id:owner,scope,revision:String(revision).padStart(64,'0'),rules:permissionRules.get(key),hidden_rules:0,runtime_applied:op==='updatePermissionSettings' && Boolean(owner),diagnostic:''};
  }
  if (op === 'getMCPInventory') {
    mcpReads++;
    if (mcpMode === 'error') return {fixtureError: true, status: 503, code: 'fixture_unavailable', message: '/private/synthetic-mcp-error'};
    return {session_id: input.sessionID, revision: '18446744073709551615', source: 'runtime', hidden_servers: 0, hidden_tools: 0,
      servers: mcpMode === 'empty' ? [] : [
        {name: 'Documentation', source: 'runtime-manager', status: 'connected', health: 'healthy', diagnostic: '', truncated: false, hidden_tools: 0,
          tools: [{name: 'search', description: '<script>window.__mcpUnsafe = true</script> <SCRIPT>window.__mcpUnsafe = true</SCRIPT>', truncated: false}]},
        {name: 'Unavailable server', source: 'runtime-manager', status: 'failed', health: 'unavailable', diagnostic: 'connect_failed', tools: [], hidden_tools: 3, truncated: false},
      ]};
  }
  if (op === 'getTaskExplorer') {
    taskReads++;
    if (failNextTasks) {
      failNextTasks = false;
      return { fixtureError: true, status: 503, code: 'fixture_unavailable', message: 'Fixture tasks are temporarily unavailable.' };
    }
    const work = (board, title) => ({ board_id: board, id: 'same-item', revision: '9007199254740993',
      order: 1, title, status: 'in_progress', description: 'Read-only canonical board item', owner: 'Builder',
      blocks: [], blocked_by: [], attention: [] });
    const execution = (generation) => ({ agent_id: 'same-agent', generation, name: 'Builder', task: 'Verify the task flow',
      execution_session_id: 'synthetic-child', thread_id: `synthetic-thread-${generation}`,
      description: 'Exact generation', status: 'running', phase: 'waiting_input', activity: 'Running-looking activity text',
      display_mode: 'background', observation_ordinal: '0', ordinal_present: false, tool_use_count: 3, token_count: 21,
      attention: ['permission'], transcript_path: '/private/synthetic-task-data', allowed_actions: ['pause'] });
    const body = { session_id: input.sessionID, available: taskSnapshotMode !== 'partial',
      unavailable_reason: taskSnapshotMode === 'partial' ? 'workboard_unavailable' : '', board_id: 'primary-board',
      revision: { board: '9007199254740993', runtime: '18446744073709551615' },
      work_items: [work('primary-board', 'Implement task overview'), work('other-board', 'Review task overview')],
      executions: [execution('9007199254740992'), execution('9007199254740993')],
      links: [{ board_id: 'primary-board', work_item_id: 'same-item', agent_id: 'same-agent', generation: '9007199254740993', state: 'valid', unavailable_reason: '' }],
      attention: [], diagnostics: [], hidden: { work_items: {}, executions: { other: 2 }, attention: {}, links: 0,
        runtime_events_dropped: '0', execution_generations_evicted: '0', hidden_live_executions: '0' },
    };
    if (taskSnapshotMode === 'reordered') { body.work_items.reverse(); body.executions.reverse(); }
    if (taskSnapshotMode === 'partial') body.work_items = [];
    if (taskSnapshotMode === 'empty') { body.work_items = []; body.executions = []; body.links = []; body.hidden.executions = {}; }
    return body;
  }
  if (op === 'getAgentExecutionDetail') {
    detailReads.push(input);
    if (failNextDetail) {
      failNextDetail = false;
      return { fixtureError: true, status: 409, code: 'agent_execution_selection_changed', message: '/private/synthetic-detail-path' };
    }
    return { session_id: input.sessionID, agent_id: input.agentID, generation: input.generation,
      execution_session_id: input.executionSessionID, thread_id: detailMode === 'wrong-lineage' ? 'different-thread' : input.threadID,
      revision: '18446744073709551615', metadata: { name: 'Builder', status: detailMode === 'live' ? 'running' : 'completed',
        task: 'Verify the exact detail flow', parent_session_id: 'synthetic-parent-session', parent_thread_id: 'synthetic-parent-thread',
        parent_agent_id: 'synthetic-parent-agent', parent_tool_use_id: 'synthetic-spawn', last_tool_name: 'Bash', tool_use_count: 3, token_count: 21,
        cwd: '/private/synthetic-detail-path' },
      output: { requested: input.includeOutput, text: input.includeOutput && detailMode !== 'live'
        ? `${detailMode === 'long' ? '界'.repeat(30000) : ''}<script>synthetic exact output</script>` : '',
        truncated: false, error_category: '' }, output_file: '/private/synthetic-detail-path' };
  }
  if (op === 'promptSuggestion') {
    suggestionRequests++;
    return { session_id: input.sessionID, suggestion: 'run the focused tests' };
  }
  if (op === 'getAgentTranscript') {
    agentPageReads.push(input);
    if (failNextAgentPage) {
      failNextAgentPage = false;
      return { fixtureError: true, status: 409, code: 'agent_transcript_selection_changed', message: '/private/synthetic-page-error' };
    }
    const item = (id, content) => ({ id, transcript_entry_id: id, role: 'assistant', content,
      reasoning_content: 'Synthetic reasoning evidence', tool_call_id: '', tool_name: '', tool_calls: [], prompt_parts: [],
      completed: true, source: 'durable', replay: agentPageMode !== 'live_attach', truncated: false });
    const newest = item('agent-newest', '## Exact child conversation\n\nRead-only synthetic child evidence.\n\n<script>not executable</script>');
    const part = (kind, text, mime = '') => ({ kind, text, mime_type: mime, image_detail: '', size_bytes: 10, width: 0, height: 0 });
    newest.prompt_parts = [part('text', newest.content), part('image', '', 'image/png'), part('text', 'After media')];
    return { session_id: input.sessionID, agent_id: input.agentID, generation: input.generation,
      execution_session_id: input.executionSessionID, thread_id: input.threadID, revision: '18446744073709551615',
      mode: agentPageMode, replay: agentPageMode !== 'live_attach', storage: 'durable',
      messages: input.cursor ? [item('agent-older-1', 'Equal conversation text'), item('agent-older-2', 'Equal conversation text')]
        : [newest],
      has_more: !input.cursor, next_cursor: input.cursor ? '' : 'synthetic-older-cursor', corruptions: 0,
      transcript_path: '/private/synthetic-page-path' };
  }
  if (op === 'startTurn') {
    turnRequests++;
    if (failNextTurn) {
      failNextTurn = false;
      return { fixtureError: true, status: 503, code: 'fixture_unavailable', message: 'Fixture turn admission is unavailable.' };
    }
    return { session_id: input.sessionID, turn_id: input.clientTurnID, accepted: true };
  }
  if (op === 'createSession') { creations++; return session; }
  if (op === 'closeSession') return {};
  if (op === 'listSessions') return { sessions: conversation ? [session, secondSession] : [] };
  if (op === 'listDurableSessions') return { sessions: homeHistory ? [
    { id: 'home-older', title: 'Older saved task', workspace_label: 'second-project', updated_at: '2026-10-01T00:00:00Z', resumable: true },
    { id: 'home-newer', title: 'Most recent saved task', workspace_label: 'first-project', updated_at: '2026-10-02T00:00:00Z', resumable: true },
  ] : [], has_more: false };
  if (op === 'durableTranscriptPage') return { entries: [], has_more: false };
  if (op === 'transcriptPage') return { entries: [], has_more: false };
  if (op === 'snapshot') return { session: input.sessionID === secondSession.id ? secondSession : session, event_cursor: 1, messages, interactions: [], activity: [] };
  if (op === 'getExecutionSettings') return { model: 'default', models: [{ selector: 'default', display_name: 'Default model', provider: 'openai' }], reasoning_effort: 'default', reasoning_effort_options: ['default'], permission_mode: 'default', permission_mode_options: ['default', 'acceptEdits'] };
  if (op === 'completeCommand') {
    const suffix = input.input === '/model 😀中 suffix';
    const values = input.input === '/mod' ? ['/model'] : input.input === '/model ' || suffix ? ['中文 model'] : [];
    const argument = input.input.startsWith('/model ');
    return { start: argument ? 7 : 0, end: suffix ? 9 : [...input.input].length, command: argument ? 'model' : '', hint: argument ? '[model]' : '', candidates: values.map(value => ({ value, description: 'Isolated completion fixture', source: argument ? 'model' : 'command', replacement: argument ? '"中文 model"' + (suffix ? '' : ' ') : '/model ' })) };
  }
  if (op === 'getSettings') return settings(input.scope);
  if (op === 'updateSettings') {
    settingsWrites.push(structuredClone(input));
    if (failNextSettingsWrite) {
      failNextSettingsWrite = false;
      return { fixtureError: true, status: 503, code: 'fixture_unavailable', message: 'Fixture settings write is unavailable.' };
    }
    if (conflictNext || input.revision !== String(revision).padStart(64, '0')) {
      conflictNext = false;
      return { fixtureError: true, code: 'settings_conflict', message: 'Settings changed in another client. Reload before saving.', status: 409 };
    }
    const selected = input.scope === 'project' ? projectStored : stored;
    for (const [key, value] of Object.entries(input.changes || {})) {
      if (!Object.hasOwn(definitions, key)) throw new Error('Unknown fixture setting');
      if (value === null) delete selected[key];
      else selected[key] = value;
    }
    revision++;
    return settings(input.scope);
  }
  if (op === 'reviewDiff') return { sources: [], generated_at: '2026-09-05T00:00:00Z' };
  throw new Error('Unexpected fixture operation: ' + op);
}
async function readBody(req) {
  let input = '';
  for await (const chunk of req) input += chunk;
  return input ? JSON.parse(input) : {};
}
function reply(res, body) {
  const status = body?.fixtureError ? body.status : 200;
  if (!res.headersSent) res.writeHead(status, { 'Content-Type': 'application/json', 'Cache-Control': 'no-store' });
  res.end(JSON.stringify(body?.fixtureError ? { error: { code: body.code, message: body.message } } : body));
}
async function check(name, action) {
  try { await action(); results.push({ name, status: 'pass' }); console.log('PASS ' + name); }
  catch (error) { results.push({ name, status: 'fail', error: error.message }); console.log('FAIL ' + name + ': ' + error.message); }
}
async function focusIs(page, id) {
  try { await page.waitForFunction(expected => document.activeElement.id === expected, id); }
  catch {
    const observed = await page.evaluate(() => {
      const close = document.querySelector('#inspector-close');
      const css = getComputedStyle(close);
      const inspectorCSS = getComputedStyle(document.querySelector('#inspector'));
      return { active: document.activeElement.id, navigationInert: document.querySelector('#navigation').inert, conversationInert: document.querySelector('#conversation').inert, inspectorInert: document.querySelector('#inspector').inert, navigationExpanded: document.querySelector('#navigation-toggle').getAttribute('aria-expanded'), inspectorExpanded: document.querySelector('#inspector-toggle').getAttribute('aria-expanded'), closeDisplay: css.display, closeVisibility: css.visibility, inspectorVisibility: inspectorCSS.visibility, closeRect: close.getBoundingClientRect().toJSON() };
    });
    throw new Error(`Expected focus ${id}; observed ${JSON.stringify(observed)}`);
  }
}
async function waitForHeldRead(queue = heldReads, count = 1) {
  for (let attempt = 0; attempt < 100; attempt++) {
    if (queue.length >= count) return;
    await new Promise(resolve => setTimeout(resolve, 10));
  }
  throw new Error('The expected settings request was not received');
}
function releaseHeld(queue) {
  for (const { res, body } of queue.splice(0)) reply(res, body);
}
function releaseReads() {
  for (const queue of [heldReads, heldWrites, heldPermissionWrites, heldSuggestions, heldTurns, heldCloses, heldTasks, heldDetails, heldAgentPages]) releaseHeld(queue);
}
async function main() {
  const args = process.argv.slice(2);
  if (args.length && (args.length !== 2 || args[0] !== '--output-dir')) {
    throw new Error('Usage: node desktop/scripts/workbench_ui_acceptance.cjs [--output-dir DIR]');
  }
  const outputDir = args.length
    ? path.resolve(args[1])
    : await fs.mkdtemp(path.join(os.tmpdir(), 'yhc-workbench-ui-acceptance-'));
  await fs.mkdir(outputDir, { recursive: true });
  const server = http.createServer(async (req, res) => {
    const url = new URL(req.url, 'http://localhost');
    const conversation = String(req.headers.cookie || '').includes('fixture=conversation');
    try {
      if (url.pathname === '/fixture/rpc') {
        const input = await readBody(req);
        const body = operation(input.op, input.input, conversation);
        if (input.op === 'getSettings' && holdNextSettingsRead) {
          holdNextSettingsRead = false; heldReads.push({ res, body }); return;
        }
        if (input.op === 'getTaskExplorer' && holdNextTasks) {
          holdNextTasks = false; heldTasks.push({ res, body }); return;
        }
        if (input.op === 'getAgentExecutionDetail' && holdNextDetail) {
          holdNextDetail = false; heldDetails.push({ res, body }); return;
        }
        if (input.op === 'getAgentTranscript' && holdNextAgentPage) {
          holdNextAgentPage = false; heldAgentPages.push({ res, body }); return;
        }
        if (input.op === 'startTurn' && holdNextTurn) {
          holdNextTurn = false; heldTurns.push({ res, body }); return;
        }
        if (input.op === 'closeSession' && holdNextClose) {
          holdNextClose = false; heldCloses.push({ res, body }); return;
        }
        if (input.op === 'promptSuggestion' && holdNextSuggestion) {
          holdNextSuggestion = false; heldSuggestions.push({ res, body }); return;
        }
        if (input.op === 'updateSettings' && holdNextSettingsWrite) {
          holdNextSettingsWrite = false; heldWrites.push({ res, body }); return;
        }
        reply(res, body); return;
      }
      if (url.pathname === '/v1/auth/browser-session') { reply(res, { protocol_version: 2, csrf_token: 'isolated-fixture-csrf' }); return; }
      if (url.pathname === '/v1/permission-settings') {
        const body=operation(req.method==='PATCH' ? 'updatePermissionSettings' : 'getPermissionSettings',{...Object.fromEntries(url.searchParams),...await readBody(req)},conversation);
        if(req.method==='PATCH' && holdNextPermissionWrite) {
          holdNextPermissionWrite=false; res.writeHead(200,{'Content-Type':'application/json','Cache-Control':'no-store'}); res.flushHeaders(); heldPermissionWrites.push({res,body}); return;
        }
        reply(res,body); return;
      }
      if (url.pathname === '/v1/settings') {
        const body = operation(req.method === 'PATCH' ? 'updateSettings' : 'getSettings', { ...Object.fromEntries(url.searchParams), ...await readBody(req) }, conversation);
        if (req.method === 'PATCH' && holdNextSettingsWrite) {
          holdNextSettingsWrite = false; heldWrites.push({ res, body }); return;
        }
        if (req.method === 'GET' && holdNextSettingsRead) {
          holdNextSettingsRead = false;
          res.writeHead(200, { 'Content-Type': 'application/json', 'Cache-Control': 'no-store' });
          res.flushHeaders();
          heldReads.push({ res, body }); return;
        }
        if (req.method === 'GET' && failNextSettingsRead) {
          failNextSettingsRead = false;
          reply(res, { fixtureError: true, status: 503, code: 'fixture_unavailable', message: 'Fixture settings are temporarily unavailable.' }); return;
        }
        reply(res, body); return;
      }
      if (url.pathname === '/v1/sessions') { reply(res, operation('listSessions', {}, conversation)); return; }
      if (req.method === 'DELETE' && /^\/v1\/sessions\/[^/]+$/.test(url.pathname)) {
        const body = operation('closeSession', { sessionID: url.pathname.split('/').at(-1) });
        if (holdNextClose) { holdNextClose = false; heldCloses.push({ res, body }); return; }
        reply(res, body); return;
      }
      if (url.pathname === '/v1/durable-sessions') { reply(res, operation('listDurableSessions')); return; }
      if (/^\/v1\/durable-sessions\/[^/]+\/transcript$/.test(url.pathname)) {
        reply(res, operation('durableTranscriptPage', { sessionID: url.pathname.split('/').at(-2) })); return;
      }
      if (url.pathname.endsWith('/events')) {
        res.writeHead(200, { 'Content-Type': 'text/event-stream', 'Cache-Control': 'no-cache', Connection: 'keep-alive' });
        res.write(': connected\n\n'); streams.set(res, url.pathname.split('/').at(-2)); req.on('close', () => streams.delete(res)); return;
      }
      const end = url.pathname.split('/').at(-1);
      if (end === 'goal' || (end === 'control' && url.pathname.endsWith('/goal/control'))) {
        const parts = url.pathname.split('/');
        const sessionID = parts[3];
        reply(res, operation(end === 'goal' ? 'getGoal' : 'controlGoal',
          { ...await readBody(req), sessionID })); return;
      }
      if (end === 'tasks') {
        const body = operation('getTaskExplorer', { sessionID: url.pathname.split('/').at(-2) });
        if (holdNextTasks) { holdNextTasks = false; heldTasks.push({ res, body }); return; }
        reply(res, body); return;
      }
      if (end === 'agent-execution-detail') {
        const body = operation('getAgentExecutionDetail', { sessionID: url.pathname.split('/').at(-2),
          agentID: url.searchParams.get('agent_id'), generation: url.searchParams.get('generation'),
          executionSessionID: url.searchParams.get('execution_session_id'), threadID: url.searchParams.get('thread_id'),
          includeOutput: url.searchParams.get('include_output') === 'true' });
        if (holdNextDetail) { holdNextDetail = false; heldDetails.push({ res, body }); return; }
        reply(res, body); return;
      }
      if (end === 'prompt-suggestion') {
        const body = operation('promptSuggestion', { sessionID: url.pathname.split('/').at(-2) });
        if (holdNextSuggestion) { holdNextSuggestion = false; heldSuggestions.push({ res, body }); return; }
        reply(res, body); return;
      }
      if (end === 'agent-transcript') {
        const body = operation('getAgentTranscript', { sessionID: url.pathname.split('/').at(-2),
          agentID: url.searchParams.get('agent_id'), generation: url.searchParams.get('generation'),
          executionSessionID: url.searchParams.get('execution_session_id'), threadID: url.searchParams.get('thread_id'),
          ...(url.searchParams.has('cursor') ? { cursor: url.searchParams.get('cursor') } : {}), limit: Number(url.searchParams.get('limit')) });
        if (holdNextAgentPage) { holdNextAgentPage = false; heldAgentPages.push({ res, body }); return; }
        reply(res, body); return;
      }
      if (end === 'turns' && req.method === 'POST') {
        const input = await readBody(req);
        const body = operation('startTurn', { sessionID: url.pathname.split('/').at(-2), prompt: input.prompt, clientTurnID: input.client_turn_id });
        if (holdNextTurn) { holdNextTurn = false; heldTurns.push({ res, body }); return; }
        reply(res, body); return;
      }
      if (end === 'command-completions') { reply(res, operation('completeCommand', await readBody(req))); return; }
      if (end === 'execution-settings' && req.method === 'PATCH') sessionSettingWrites++;
      const mapped = { transcript: 'transcriptPage', snapshot: 'snapshot', 'execution-settings': 'getExecutionSettings', 'review-diff': 'reviewDiff', turns: 'startTurn', 'mcp-inventory': 'getMCPInventory' }[end];
      if (mapped) { reply(res, operation(mapped, { sessionID: url.pathname.split('/').at(-2) })); return; }
      if (url.pathname === '/favicon.ico') { res.writeHead(204).end(); return; }
      const file = path.join(root, url.pathname === '/' ? 'index.html' : url.pathname);
      res.setHeader('Content-Type', /\.m?js$/.test(file) ? 'text/javascript' : file.endsWith('.css') ? 'text/css' : 'text/html');
      res.end(await fs.readFile(file));
    } catch (error) { res.writeHead(500).end(String(error.message)); }
  });
  let base;
  let browser;
  const screenshot = async (page, name) => {
    await page.evaluate(() => new Promise(resolve => requestAnimationFrame(() => requestAnimationFrame(resolve))));
    return page.screenshot({ path: path.join(outputDir, `${name}.png`), animations: 'disabled' });
  };
  async function createPage(surface, conversation = false, noVault = false, savedProvider = 'openai', recovery = '', profile = null) {
    const context = await browser.newContext({ viewport: { width: 1440, height: 1000 }, colorScheme: 'light' });
    if (conversation) await context.addCookies([{ name: 'fixture', value: 'conversation', url: base }]);
    const page = await context.newPage();
    page.setDefaultTimeout(6000);
    page.on('pageerror', error => errors.push(error.message));
    if (surface === 'desktop') await page.addInitScript(({ noVault, savedProvider, recovery, profile }) => {
      const recoveryState = { pickerCalls: 0, configureCalls: 0, createHandles: [], generation: 0, saved: false };
      let fixtureProfile = profile || { provider: savedProvider, model: 'default', baseURL: '' };
      window.__fixtureProviderSetupCalls = 0;
      window.__fixtureProviderSubmitted = null;
      if (recovery) window.__workspaceRecovery = recoveryState;
      window.yhcDesktop = {
        getInfo: async () => ({ protocolVersion: 2, webAvailable: true }),
        getProviderStatus: async () => ({ configured: !noVault, ...fixtureProfile,
          provider: noVault ? '' : fixtureProfile.provider, launchReady: !noVault, secureStorageAvailable: true }),
        api: async (op, input) => {
          const response = await fetch('/fixture/rpc', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ op, input }) });
          const body = await response.json();
          if (!response.ok) { const error = new Error(body.error.message); error.code = body.error.code; throw error; }
          return body;
        },
        apiResult: async (op, input) => {
          if (recovery && op === 'createSession') {
            recoveryState.createHandles.push(input.workspaceHandle);
            if (!recoveryState.saved && recovery === 'held-create') {
              await new Promise(resolve => { window.__releaseWorkspaceCreate = resolve; });
            }
            if (!recoveryState.saved) return { ok: false, error: { status: 422, code: 'session_create_failed', message: 'Fixture provider configuration is required.' } };
            if (recovery === 'newer-notice') {
              await new Promise(resolve => { window.__releaseRecoveredCreate = resolve; });
            }
            const valid = input.workspaceHandle === `fixture-${recoveryState.generation}-${recoveryState.pickerCalls}`;
            if (!valid) return { ok: false, error: { status: 422, code: 'workspace_handle_invalid', message: 'workspace_handle is invalid or already used' } };
          }
          const response = await fetch('/fixture/rpc', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ op, input }) });
          const body = await response.json();
          return response.ok
            ? { ok: true, value: body }
            : { ok: false, error: { message: body.error.message, code: body.error.code, status: response.status } };
        },
        onEventStream: listener => { window.__fixtureTaskEvent = listener; return () => { window.__fixtureTaskEvent = null; }; },
        onBackendExit: listener => { window.__fixtureBackendExit = listener; return () => { window.__fixtureBackendExit = null; }; },
        startEvents: async () => ({ status: 'open' }), stopEvents: async () => {},
        selectWorkspace: async () => {
          if (recovery) {
            recoveryState.pickerCalls++;
            if (recoveryState.saved && recovery === 'cancel') return null;
            return { workspace_handle: `fixture-${recoveryState.generation}-${recoveryState.pickerCalls}`, workspace_label: 'demo-workspace' };
          }
          return noVault ? { workspace_handle: 'isolated-fixture-workspace', workspace_label: 'demo-workspace' } : null;
        },
        configureProvider: async (input) => {
          if (recovery) recoveryState.configureCalls++;
          window.__fixtureProviderSetupCalls++;
          // Capture only public selection metadata and a presence bit, never a key.
          window.__fixtureProviderSubmitted = { provider: input.provider, model: input.model,
            baseURL: input.baseURL || '', credentialSupplied: Boolean(input.apiKey) };
          if (recovery === 'held-provider') {
            await new Promise(resolve => { window.__releaseProviderConfigure = resolve; });
          }
          if (recovery === 'save-failure') throw new Error('Fixture profile could not be saved.');
          if (recovery) {
            recoveryState.saved = true;
            recoveryState.generation++;
          }
          fixtureProfile = { provider: input.provider, model: input.model, baseURL: input.baseURL || '' };
          return { configured: true, ...fixtureProfile, launchReady: true, secureStorageAvailable: true };
        },
        openWeb: async () => {
          if (recovery === 'newer-notice') throw new Error('Fixture browser could not open.');
        },
      };
    }, { noVault, savedProvider, recovery, profile });
    await page.goto(base);
    await page.waitForFunction(() => document.documentElement.dataset.yhcBootstrap === 'ready');
    return page;
  }
  try {
    await listenFixtureServer(server);
    base = `http://127.0.0.1:${server.address().port}`;
    browser = await chromium.launch({
      headless: true,
      ...(process.env.YHC_UI_BROWSER_EXECUTABLE
        ? { executablePath: process.env.YHC_UI_BROWSER_EXECUTABLE }
        : {}),
    });
    const desktop = await createPage('desktop');
    await check('Desktop welcome is connected without a disabled composer', async () => {
      assert.equal(await desktop.locator('#status').textContent(), 'Connected');
      assert.equal(await desktop.locator('#composer').isVisible(), false);
      assert.equal(await desktop.locator('#new-session').isVisible(), true);
      await screenshot(desktop, 'home');
    });
    await check('Wide inspector toggles without modal ownership or blocked conversation', async () => {
      assert.equal(await desktop.locator('#inspector').isVisible(), false);
      await desktop.locator('#inspector-toggle').click();
      assert.equal(await desktop.locator('#inspector').isVisible(), true);
      assert.equal(await desktop.locator('#inspector').getAttribute('aria-modal'), null);
      assert.equal(await desktop.locator('#conversation').evaluate(node => node.inert), false);
      assert.equal(await desktop.locator('#navigation').evaluate(node => node.inert), false);
      await desktop.locator('#inspector-toggle').click();
      assert.equal(await desktop.locator('#inspector').isVisible(), false);
    });
    await check('Narrow sheets own focus and Escape returns to the trigger', async () => {
      await desktop.setViewportSize({ width: 390, height: 844 });
      await desktop.locator('#navigation-toggle').click();
      await focusIs(desktop, 'navigation-close');
      assert.equal(await desktop.locator('#conversation').evaluate(node => node.inert), true);
      await screenshot(desktop, 'narrow');
      await desktop.keyboard.press('Escape');
      await focusIs(desktop, 'navigation-toggle');
      await desktop.locator('#inspector-toggle').click();
      await focusIs(desktop, 'inspector-close');
      assert.equal(await desktop.locator('#inspector').getAttribute('aria-modal'), 'true');
      await desktop.keyboard.press('Escape');
      await focusIs(desktop, 'inspector-toggle');
      await desktop.setViewportSize({ width: 1440, height: 1000 });
    });
    await desktop.setViewportSize({ width: 1440, height: 1000 });
    await desktop.keyboard.press('Escape');
    await check('Settings load, search, save shared values and apply appearance', async () => {
      await desktop.locator('#settings-open').click();
      await desktop.locator('#setting-model').waitFor();
      await desktop.locator('#settings-search').fill('does-not-exist');
      assert.equal(await desktop.locator('.settings-no-results').isVisible(), true);
      await desktop.locator('#settings-search').fill('appearance');
      assert.equal(await desktop.locator('[data-setting="model"]').isVisible(), false);
      await desktop.locator('#settings-search').fill('');
      await desktop.locator('#setting-theme').selectOption('polar-night');
      await desktop.locator('#settings-save').click();
      await desktop.waitForFunction(() => document.querySelector('#settings-status').textContent.startsWith('Saved.'));
      assert.equal(await desktop.locator('html').getAttribute('data-theme'), 'polar-night');
      assert.equal(await desktop.locator('#settings-save').isDisabled(), true);
      await desktop.locator('#settings-dialog').evaluate(node => { node.scrollTop = 0; });
      await screenshot(desktop, 'settings');
    });
    await check('Revision conflict preserves edited values and requires reload', async () => {
      conflictNext = true;
      await desktop.locator('#setting-model').fill('unsaved-fixture-model');
      await desktop.locator('#settings-save').click();
      await desktop.waitForFunction(() => document.querySelector('#settings-status').textContent.includes('another client'));
      assert.equal(await desktop.locator('#setting-model').inputValue(), 'unsaved-fixture-model');
      assert.equal(await desktop.locator('#settings-save').isDisabled(), true);
      await desktop.locator('#settings-close').click();
      await desktop.locator('#confirm-accept').click();
      await desktop.waitForFunction(() => !document.querySelector('#settings-dialog').open);
    });
    const savedGLM = await createPage('desktop', false, false, 'glm');
    await check('Saved GLM credentials stay selected when opened from shared settings and can be saved', async () => {
      await savedGLM.locator('#settings-open').click();
      await savedGLM.locator('#setting-model').waitFor();
      await savedGLM.locator('#settings-credentials summary').click();
      await savedGLM.locator('#settings-credentials-open').click();
      await savedGLM.locator('#provider-dialog[open]').waitFor();
      assert.equal(await savedGLM.locator('#provider-select').inputValue(), 'glm');
      await savedGLM.locator('#provider-api-key').fill('fixture-only-key');
      await savedGLM.locator('#provider-save').click();
      await savedGLM.waitForFunction(() => !document.querySelector('#provider-dialog').open);
      await savedGLM.locator('#provider-settings').click();
      await savedGLM.locator('#provider-dialog[open]').waitFor();
      assert.equal(await savedGLM.locator('#provider-select').inputValue(), 'glm');
      await savedGLM.locator('#provider-cancel').click();
    });
    await savedGLM.close();
    await check('Integer settings reject invalid values before saving or direct submit', async () => {
      await desktop.locator('#settings-open').click();
      await desktop.getByRole('button', { name: 'Agent behavior', exact: true }).click();
      await desktop.locator('#setting-max_turns').waitFor();
      await desktop.locator('#setting-max_turns').fill('3');
      const before = revision;
      const writesBefore = settingsWrites.length;
      for (const invalid of ['', '-1', '1.5']) {
        await desktop.locator('#setting-max_turns').fill(invalid);
        assert.equal(await desktop.locator('#setting-max_turns').evaluate(node => node.checkValidity()), false);
        assert.equal(await desktop.locator('#settings-save').isDisabled(), true);
        assert.equal(await desktop.locator('#settings-status').textContent(), 'Check Maximum turns: enter a nonnegative browser-safe integer using decimal digits.');
        await desktop.locator('#settings-form').evaluate(async form => form.onsubmit({ preventDefault() {} }));
        assert.equal(revision, before);
        assert.equal(settingsWrites.length, writesBefore, 'invalid integer does not send a PATCH');
      }
      for (const invalid of ['9007199254740993', '1.0000000000000001', '9007199254740991.1', '1e3']) {
        await desktop.locator('#setting-max_turns').fill(invalid);
        assert.equal(await desktop.locator('#settings-save').isDisabled(), true);
        assert.equal(await desktop.locator('#settings-status').textContent(), 'Check Maximum turns: enter a nonnegative browser-safe integer using decimal digits.');
        await desktop.locator('#settings-form').evaluate(async form => form.onsubmit({ preventDefault() {} }));
        assert.equal(revision, before);
        assert.equal(settingsWrites.length, writesBefore, 'unsafe integer spelling does not send a PATCH');
      }
      await desktop.locator('#setting-max_turns').fill('4');
      await desktop.locator('#settings-save').click();
      await desktop.waitForFunction(() => document.querySelector('#settings-status').textContent.startsWith('Saved.'));
      assert.equal(stored.max_turns, 4);
      await desktop.locator('#settings-close').click();
      await desktop.waitForFunction(() => !document.querySelector('#settings-dialog').open);
    });
    await check('Desktop and web Settings fail closed for unsafe integer snapshots', async () => {
      const writesBefore = settingsWrites.length;
      unsafeIntegerSnapshot = true;
      try {
        for (const surface of ['desktop', 'web']) {
          const page = await createPage(surface, true);
          try {
            await page.locator('#settings-open').click();
            await page.waitForFunction(() => document.querySelector('#settings-status').textContent === 'integer outside browser-safe range; use TUI/CLI then reload');
            assert.equal(await page.locator('[data-setting="max_turns"]').count(), 0);
            assert.equal(await page.locator('#settings-save').isDisabled(), true);
            assert.equal(await page.locator('#settings-reload').isEnabled(), true);
            assert.equal(await page.locator('#settings-close').isEnabled(), true);
            await page.locator('#settings-reload').click();
            await page.waitForFunction(() => document.querySelector('#settings-status').textContent === 'integer outside browser-safe range; use TUI/CLI then reload');
            const readsBefore = mcpReads;
            await page.getByRole('button', { name: 'Extensions · MCP', exact: true }).click();
            await page.locator('.mcp-status').filter({ hasText: 'Read-only snapshot' }).waitFor();
            assert.equal(mcpReads, readsBefore + 1, 'MCP remains a separate explicit read when defaults are unsafe');
            assert.equal(await page.locator('[data-setting="max_turns"]').count(), 0);
            assert.equal(await page.locator('#settings-save').isDisabled(), true);
            await page.locator('#settings-close').click();
            await page.waitForFunction(() => !document.querySelector('#settings-dialog').open);
          } finally { await page.close(); }
        }
        assert.equal(settingsWrites.length, writesBefore, 'unsafe snapshots cannot produce a PATCH');
      } finally {
        unsafeIntegerSnapshot = false;
      }
    });
    const web = await createPage('web');
    await check('Web welcome exposes browsing instead of a dead new-session button', async () => {
      assert.equal(await web.locator('#new-session').isVisible(), false);
      await web.getByRole('button', { name: 'Browse sessions', exact: true }).click();
      assert.equal(await web.evaluate(() => document.activeElement.id), 'session-filter');
      await screenshot(web, 'web-home');
    });
    await check('Searching settings is safe while the read is pending or has failed', async () => {
      holdNextSettingsRead = true;
      await web.locator('#settings-open').click();
      await waitForHeldRead();
      await web.locator('#settings-search').fill('theme');
      releaseReads();
      await web.locator('#setting-theme').waitFor();
      assert.equal(await web.locator('[data-setting="model"]').isVisible(), false);
      failNextSettingsRead = true;
      await web.locator('#settings-reload').click();
      await web.waitForFunction(() => document.querySelector('#settings-status').textContent.includes('Could not load'));
      await web.locator('#settings-search').fill('model');
      await web.locator('#settings-close').click();
      await web.waitForFunction(() => !document.querySelector('#settings-dialog').open);
    });
    const settingsReadTimeout = await createPage('web');
    await check('Settings read timeout aborts without a write and a late response cannot refill the editor', async () => {
      await settingsReadTimeout.clock.install();
      const writesBefore = settingsWrites.length;
      holdNextSettingsRead = true;
      try {
        await settingsReadTimeout.locator('#settings-open').click();
        await waitForHeldRead();
        await settingsReadTimeout.clock.fastForward(15_000);
        await settingsReadTimeout.waitForFunction(() => document.querySelector('#settings-status').textContent === 'Could not load settings. Reload to retry.');
        assert.equal(await settingsReadTimeout.locator('#settings-close').isEnabled(), true);
        assert.equal(settingsWrites.length, writesBefore);
        stored.model = 'fresh-read-after-timeout'; revision++;
        releaseReads();
        await new Promise(resolve => setTimeout(resolve, 25));
        assert.equal(await settingsReadTimeout.locator('#settings-status').textContent(), 'Could not load settings. Reload to retry.');
        assert.equal(await settingsReadTimeout.locator('#setting-model').count(), 0);
        await settingsReadTimeout.locator('#settings-close').click();
        await settingsReadTimeout.waitForFunction(() => !document.querySelector('#settings-dialog').open);
        await settingsReadTimeout.locator('#settings-open').click();
        await settingsReadTimeout.locator('#setting-model').waitFor();
        assert.equal(await settingsReadTimeout.locator('#setting-model').inputValue(), 'fresh-read-after-timeout');
        await settingsReadTimeout.locator('#settings-close').click();
      } finally {
        holdNextSettingsRead = false;
        releaseReads();
      }
    });
    await settingsReadTimeout.close();
    const settingsTimeout = await createPage('web');
    await check('Settings write timeout aborts, preserves the draft and requires an explicit reload', async () => {
      await settingsTimeout.clock.install();
      const writesBefore = settingsWrites.length;
      holdNextSettingsWrite = true;
      try {
        await settingsTimeout.locator('#settings-open').click();
        await settingsTimeout.locator('#setting-model').waitFor();
        await settingsTimeout.locator('#setting-model').fill('timeout-fixture-model');
        await settingsTimeout.locator('#settings-save').click();
        await waitForHeldRead(heldWrites);
        await settingsTimeout.clock.fastForward(15_000);
        await settingsTimeout.waitForFunction(() => document.querySelector('#settings-status').textContent === 'Settings request timed out. Reload before saving.');
        assert.equal(await settingsTimeout.locator('#setting-model').inputValue(), 'timeout-fixture-model');
        assert.equal(await settingsTimeout.locator('#settings-save').isDisabled(), true);
        assert.equal(await settingsTimeout.locator('#settings-close').isEnabled(), true);
        assert.equal(settingsWrites.length, writesBefore + 1, 'timeout does not retry the write');
        assert.equal(stored.model, 'timeout-fixture-model', 'fixture commit occurs before the held acknowledgement');
        releaseReads();
        await new Promise(resolve => setTimeout(resolve, 25));
        assert.equal(await settingsTimeout.locator('#settings-status').textContent(), 'Settings request timed out. Reload before saving.');
        assert.equal(await settingsTimeout.locator('#setting-model').inputValue(), 'timeout-fixture-model');
        assert.equal(await settingsTimeout.locator('#settings-save').isDisabled(), true);
        await settingsTimeout.locator('#settings-reload').click();
        await settingsTimeout.locator('#confirm-accept').click();
        await settingsTimeout.locator('#setting-model').waitFor();
        await settingsTimeout.waitForFunction(() => document.querySelector('#settings-status').textContent === 'Up to date');
        assert.equal(await settingsTimeout.locator('#setting-model').inputValue(), 'timeout-fixture-model');
        assert.equal(await settingsTimeout.locator('#settings-save').isDisabled(), true);
        await settingsTimeout.locator('#settings-close').click();
        await settingsTimeout.waitForFunction(() => !document.querySelector('#settings-dialog').open);
      } finally {
        holdNextSettingsWrite = false;
        releaseReads();
      }
    });
    await settingsTimeout.close();
    for (const provider of ['anthropic', 'openai', 'google', 'deepseek', 'glm', 'qwen', 'ark']) {
      await check(`Desktop saved ${provider} credentials retain their selection through cancel and explicit save`, async () => {
        const profile = { provider, model: 'saved-fixture-model', baseURL: 'https://fixture.example.invalid/' };
        const page = await createPage('desktop', false, false, 'openai', '', profile);
        const before = observedOperations.length, writes = settingsWrites.length;
        try {
          await page.locator('#provider-settings').click();
          assert.equal(await page.locator('#provider-select').inputValue(), provider);
          assert.equal(await page.locator('#provider-model').inputValue(), profile.model);
          assert.equal(await page.locator('#provider-base-url').inputValue(), profile.baseURL);
          assert.equal(await page.locator('#provider-api-key').inputValue(), '');
          assert.equal(await page.evaluate(() => window.__fixtureProviderSetupCalls), 0);
          await page.locator('#provider-cancel').click();
          assert.equal(await page.evaluate(() => window.__fixtureProviderSetupCalls), 0);
          await page.locator('#provider-settings').click();
          assert.equal(await page.locator('#provider-select').inputValue(), provider);
          await page.locator('#provider-api-key').fill('isolated-fixture-key-not-a-real-credential');
          await page.locator('#provider-save').click();
          await page.waitForFunction(() => !document.querySelector('#provider-dialog').open);
          assert.equal(await page.evaluate(() => window.__fixtureProviderSetupCalls), 1);
          assert.deepEqual(await page.evaluate(() => window.__fixtureProviderSubmitted), { ...profile, credentialSupplied: true });
          assert.equal(await page.locator('#provider-api-key').inputValue(), '');
          assert.equal(settingsWrites.length, writes);
          assert.equal(observedOperations.slice(before).some(op => ['createSession', 'attachTurn', 'startTurn', 'controlGoal'].includes(op)), false);
        } finally { await page.context().close(); }
      });
    }
    for (const surface of ['desktop', 'web']) {
      homeHistory = true;
      const home = await createPage(surface, false, true);
      await home.locator('#workbench-home').click();
      try {
        await check(`${surface} workbench home explains configuration sources and keeps setup reachable`, async () => {
          assert.equal(await home.locator('#empty-title').textContent(), 'Your workbench');
          assert.equal(await home.locator('#home-model-title').textContent(), 'Shared defaults detected');
          assert.match(await home.locator('#home-model-selection').textContent(), /openai.*User defaults/s);
          assert.match(await home.locator('#home-model-detail').textContent(), /not a connection check/);
          assert.equal(await home.locator('[data-empty-action="workspace"]').isVisible(), surface === 'desktop');
          assert.equal(await home.locator('[data-empty-action="provider"]').isVisible(), surface === 'desktop');
          await home.locator('[data-home-action="settings"]').click();
          assert.equal(await home.locator('#settings-dialog').isVisible(), true);
          await home.locator('#setting-provider').waitFor();
          assert.equal(await home.locator('#settings-scope').inputValue(), 'user');
          await home.locator('#settings-close').click();
          await screenshot(home, `${surface}-workbench-home-wide`);
        });
        await check(`${surface} Home accepts an asynchronous defaults refresh without another session event`, async () => {
          const hadModel = Object.hasOwn(stored, 'model'), originalModel = stored.model;
          try {
            await home.locator('[data-home-session-id="home-older"]').click();
            await home.waitForFunction(() => document.querySelector('#session-title').textContent === 'Older saved task');
            stored.model = 'home-refreshed-fixture-model';
            holdNextSettingsRead = true;
            const before = observedOperations.length;
            await home.locator('#workbench-home').click();
            await waitForHeldRead();
            assert.doesNotMatch(await home.locator('#home-model-selection').textContent(), /home-refreshed-fixture-model/);
            releaseReads();
            await home.waitForFunction(() => document.querySelector('#home-model-selection').textContent.includes('home-refreshed-fixture-model'));
            assert.equal(observedOperations.slice(before).some(op => ['updateSettings', 'createSession', 'attachTurn', 'startTurn', 'controlGoal'].includes(op)), false);
          } finally {
            holdNextSettingsRead = false;
            releaseReads();
            if (hadModel) stored.model = originalModel;
            else delete stored.model;
          }
        });
        await check(`${surface} narrow home fits and Browse all returns focus to session navigation`, async () => {
          await home.setViewportSize({ width: 390, height: 844 });
          assert.equal(await home.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth), true);
          await home.locator('[data-home-action="all-sessions"]').click();
          assert.equal(await home.locator('#navigation').getAttribute('aria-modal'), 'true');
          await focusIs(home, 'navigation-close');
          await home.keyboard.press('Escape');
          await home.waitForFunction(() => document.activeElement?.dataset.homeAction === 'all-sessions');
          assert.equal(await home.evaluate(() => document.activeElement.dataset.homeAction), 'all-sessions');
          await screenshot(home, `${surface}-workbench-home-narrow`);
        });
        await check(`${surface} recent saved history is newest first and never activates a runtime`, async () => {
          assert.deepEqual(await home.locator('[data-home-session-id]').evaluateAll(rows => rows.map(row => row.dataset.homeSessionId)), ['home-newer', 'home-older']);
          const before = observedOperations.length;
          await home.locator('[data-home-session-id="home-newer"]').click();
          await home.waitForFunction(() => document.querySelector('#session-title').textContent === 'Most recent saved task');
          await home.waitForFunction(() => /Send a message to continue/.test(document.querySelector('#turn-state').textContent));
          const actions = observedOperations.slice(before);
          assert.equal(actions.includes('durableTranscriptPage'), true);
          assert.equal(actions.some(op => ['createSession', 'attachTurn', 'startTurn', 'snapshot', 'getExecutionSettings', 'controlGoal'].includes(op)), false);
          await home.locator('#navigation-toggle').click();
          await home.locator('#workbench-home').click();
          assert.equal(await home.locator('#timeline').evaluate(node => node.scrollTop), 0);
        });
      } finally { await home.context().close(); homeHistory = false; }
      const conversationHome = await createPage(surface, true);
      await check(`${surface} Home preserves live conversation drafts and never closes or starts work`, async () => {
        await conversationHome.locator('.session-row').first().click();
        await conversationHome.locator('#prompt').fill('Keep my input while I review the workbench.');
        const before = observedOperations.length;
        await conversationHome.locator('#workbench-home').click();
        await conversationHome.locator('#empty-title').waitFor();
        assert.equal(await conversationHome.locator('#composer').isVisible(), false);
        await conversationHome.locator('[data-home-session-id="acceptance-session"]').click();
        await conversationHome.locator('#prompt').waitFor();
        assert.equal(await conversationHome.locator('#prompt').inputValue(), 'Keep my input while I review the workbench.');
        assert.equal(observedOperations.slice(before).some(op => ['closeSession', 'createSession', 'attachTurn', 'startTurn', 'controlGoal'].includes(op)), false);
      });
      await conversationHome.context().close();
    }
    for (const surface of ['desktop', 'web']) {
      goals.clear();
      const goalPage = await createPage(surface, true);
      const goalAction = op => goalPage.locator('[data-goal-operation="' + op + '"]');
      const objective = goalPage.getByRole('textbox', { name: 'Goal 目标', exact: true });
      const budget = goalPage.getByRole('textbox', { name: 'Goal 令牌预算', exact: true });
      await check(surface + ' Goal create is explicit, exact-budget and honest about not-started admission', async () => {
        await goalPage.locator('.session-row').first().click();
        await goalPage.locator('.goal-toggle').click();
        await goalPage.waitForFunction(() => document.querySelector('.goal-status').textContent.includes('尚未创建'));
        assert.equal(await goalAction('create').isDisabled(), true);
        await objective.fill('<img src=x> 改善搜索体验');
        await budget.fill('18446744073709551616');
        assert.equal(await goalAction('create').isDisabled(), true);
        await budget.fill('9007199254740993');
        await goalAction('create').click();
        await goalPage.waitForFunction(() => document.querySelector('.goal-status').textContent.includes('尚未开始'));
        assert.equal(goalWrites.at(-1).token_budget, '9007199254740993');
        assert.equal(goalWrites.at(-1).expected_revision, '0');
        assert.equal(await goalPage.locator('#goal-panel img').count(), 0);
        assert.equal(await goalPage.locator('#prompt').isDisabled(), false);
        await screenshot(goalPage, surface + '-goal-wide');
      });
      await check(surface + ' Goal independent field saves retain drafts, conflict safely and use explicit continue', async () => {
        await objective.fill('保存目标但保留预算');
        await budget.fill('9007199254740994');
        await goalAction('edit').click();
        await goalPage.waitForFunction(() => document.querySelector('.goal-status').textContent === '已保存。');
        assert.equal(await budget.inputValue(), '9007199254740994');
        await goalAction('budget').click();
        await goalPage.waitForFunction(() => document.querySelector('[data-goal-operation="budget"]').disabled === true);
        assert.equal(goalWrites.at(-1).expected_revision, '2');
        await goalAction('continue').click();
        await goalPage.waitForFunction(() => document.querySelector('.goal-status').textContent.includes('目标仍有效'));
        await goalAction('pause').click();
        await goalPage.waitForFunction(() => !document.querySelector('[data-goal-operation="resume"]').hidden);
        await goalAction('resume').click();
        await goalPage.waitForFunction(() => !document.querySelector('[data-goal-operation="continue"]').hidden);
        await objective.fill('保留冲突草稿');
        conflictNextGoal = true;
        await goalAction('edit').click();
        await goalPage.waitForFunction(() => document.querySelector('.goal-status').textContent.includes('草稿版本'));
        assert.equal(await objective.inputValue(), '保留冲突草稿');
        assert.equal((await goalPage.locator('#goal-panel').textContent()).includes('/private'), false);
        await goalAction('reload').click();
        assert.equal(await goalPage.locator('#confirm-title').textContent(), '丢弃 Goal 草稿？');
        await goalPage.locator('#confirm-accept').click();
        await goalPage.waitForFunction(() => !document.querySelector('#confirm-dialog').open);
        await goalAction('clear').click();
        assert.equal(await goalPage.locator('#confirm-title').textContent(), '清除 Goal？');
        await goalPage.locator('#confirm-cancel').click();
        assert.equal(goals.size, 1);
        await goalPage.setViewportSize({ width: 390, height: 844 });
        assert.equal(await goalPage.evaluate(() => document.documentElement.scrollWidth <= innerWidth), true);
        await screenshot(goalPage, surface + '-goal-narrow');
        await goalAction('clear').click();
        await goalPage.locator('#confirm-accept').click();
        await goalPage.waitForFunction(() => !document.querySelector('[data-goal-operation="create"]').hidden);
        assert.equal(goals.size, 0);
      });
      await goalPage.context().close();
    }
    for (const surface of ['desktop', 'web']) {
      const mcpPage = await createPage(surface, true);
      await check(`${surface} MCP is a lazy read-only settings category preserving unsaved defaults`, async () => {
        await mcpPage.locator('.session-row').first().click();
        await mcpPage.locator('#prompt').waitFor({state: 'visible'});
        const readsBefore = mcpReads;
        const writesBefore = settingsWrites.length;
        await mcpPage.locator('#settings-open').click();
        await mcpPage.locator('#setting-model').waitFor();
        assert.equal(mcpReads, readsBefore);
        await mcpPage.getByRole('button', {name: 'Appearance & input', exact: true}).click();
        const initialTheme = await mcpPage.locator('#setting-theme').inputValue();
        const draftTheme = initialTheme === 'daybreak' ? 'polar-night' : 'daybreak';
        await mcpPage.locator('#setting-theme').selectOption(draftTheme);
        await mcpPage.getByRole('button', {name: 'Extensions · MCP', exact: true}).click();
        await mcpPage.locator('.mcp-server').first().waitFor();
        assert.equal(mcpReads, readsBefore + 1);
        assert.equal(await mcpPage.locator('#settings-scope-label').isVisible(), false);
        assert.equal(await mcpPage.locator('#settings-save').isVisible(), false);
        assert.equal(await mcpPage.locator('.mcp-server').count(), 2);
        await mcpPage.locator('.mcp-tools summary').click();
        for (const tag of ['<script>', '<SCRIPT>']) assert.equal((await mcpPage.locator('.mcp-tool').textContent()).includes(tag), true);
        assert.equal(await mcpPage.locator('#settings-mcp script').count(), 0);
        assert.equal(await mcpPage.evaluate(() => window.__mcpUnsafe), undefined);
        assert.match(await mcpPage.locator('#settings-mcp').textContent(), /Connection failed/);
        await screenshot(mcpPage, `${surface}-mcp-wide`);
        await mcpPage.setViewportSize({width: 390, height: 844});
        assert.equal(await mcpPage.evaluate(() => document.documentElement.scrollWidth <= innerWidth), true);
        await screenshot(mcpPage, `${surface}-mcp-narrow`);
        await mcpPage.getByRole('button', {name: 'Appearance & input', exact: true}).click();
        assert.equal(await mcpPage.locator('#setting-theme').inputValue(), draftTheme);
        assert.equal(settingsWrites.length, writesBefore);
        await mcpPage.locator('#setting-theme').selectOption(initialTheme);
        await mcpPage.locator('#settings-close').click();
      });
      await check(`${surface} MCP search, explicit refresh, empty and safe-error states`, async () => {
        await mcpPage.setViewportSize({width: 1440, height: 1000});
        await mcpPage.locator('#settings-open').click();
        await mcpPage.locator('#setting-model').waitFor();
        await mcpPage.locator('#settings-search').fill('mcp');
        await mcpPage.locator('.mcp-server').first().waitFor();
        mcpMode = 'empty';
        await mcpPage.locator('#settings-mcp button').click();
        await mcpPage.locator('.mcp-empty').waitFor();
        mcpMode = 'error';
        await mcpPage.locator('#settings-mcp button').click();
        await mcpPage.waitForFunction(() => document.querySelector('.mcp-status').textContent.includes('Refresh to retry'));
        assert.equal((await mcpPage.locator('#settings-mcp').textContent()).includes('/private'), false);
        mcpMode = 'normal';
        await mcpPage.locator('#settings-mcp button').click();
        await mcpPage.locator('.mcp-server').first().waitFor();
        await mcpPage.locator('#settings-close').click();
      });
      await mcpPage.context().close();
    }
    for (const surface of ['desktop', 'web']) {
      const permissionPage=await createPage(surface,true);
      await check(`${surface} Permission editor shares typed scopes and preserves unsaved defaults`,async()=>{
        await permissionPage.locator('.session-row').first().click();
        await permissionPage.locator('#prompt').waitFor({state:'visible'});
        const reads=permissionReads, writes=permissionWrites.length, defaults=settingsWrites.length;
        await permissionPage.locator('#settings-open').click(); await permissionPage.locator('#setting-model').waitFor();
        assert.equal(permissionReads,reads);
        await permissionPage.getByRole('button',{name:'Appearance & input',exact:true}).click();
        const theme=await permissionPage.locator('#setting-theme').inputValue(), changed=theme==='daybreak' ? 'polar-night' : 'daybreak';
        await permissionPage.locator('#setting-theme').selectOption(changed);
        await permissionPage.getByRole('button',{name:'Permission rules',exact:true}).click();
        await permissionPage.locator('#settings-permissions .permission-rule-row').first().waitFor();
        assert.equal(await permissionPage.locator('#settings-permissions img').count(),0);
        assert.match(await permissionPage.locator('#settings-permissions').textContent(),/<img src=x>/);
        assert.equal(await permissionPage.locator('#settings-permissions .permission-rule-list .permission-rule-row').nth(1).getByRole('button',{name:'Remove',exact:true}).isDisabled(),true);
        await permissionPage.locator('#permission-rule-scope').selectOption('local');
        await permissionPage.waitForFunction(()=>document.querySelector('#settings-permissions .mcp-status').textContent.includes('Selected-file'));
        await permissionPage.locator('#permission-rule-action').selectOption('deny');
        await permissionPage.locator('#permission-rule-input').fill('Edit');
        await permissionPage.getByRole('button',{name:'Add to draft',exact:true}).click();
        assert.equal(permissionWrites.length,writes);
        await permissionPage.getByRole('button',{name:'Appearance & input',exact:true}).click();
        assert.equal(await permissionPage.locator('#setting-theme').inputValue(),changed);
        await permissionPage.getByRole('button',{name:'Permission rules',exact:true}).click();
        assert.match(await permissionPage.locator('.permission-rule-draft').textContent(),/add deny Edit/);
        assert.equal(await permissionPage.getByRole('button',{name:'Save rule changes',exact:true}).isVisible(),true);
        await screenshot(permissionPage,`${surface}-permissions-wide`);
        await permissionPage.setViewportSize({width:390,height:844});
        assert.equal(await permissionPage.evaluate(()=>document.documentElement.scrollWidth<=innerWidth),true);
        const saveBounds=await permissionPage.getByRole('button',{name:'Save rule changes',exact:true}).boundingBox();
        assert.ok(saveBounds && saveBounds.y>=0 && saveBounds.y+saveBounds.height<=844,'narrow save stays in fixed footer');
        await screenshot(permissionPage,`${surface}-permissions-narrow`);
        await permissionPage.getByRole('button',{name:'Save rule changes',exact:true}).click();
        await permissionPage.waitForFunction(()=>document.querySelector('#settings-permissions .mcp-status').textContent.includes('Saved and reloaded'));
        assert.equal(permissionWrites.length,writes+1); assert.equal(permissionWrites.at(-1).scope,'local'); assert.equal(settingsWrites.length,defaults);
        assert.equal(await permissionPage.locator('.permission-rule-draft').textContent(),'');
        await permissionPage.getByRole('button',{name:'Appearance & input',exact:true}).click(); await permissionPage.locator('#setting-theme').selectOption(theme);
        await permissionPage.locator('#settings-close').click();
      });
      await check(`${surface} Permission conflicts retain drafts and discard is explicit`,async()=>{
        await permissionPage.setViewportSize({width:1440,height:1000});
        await permissionPage.locator('#settings-open').click(); await permissionPage.locator('#setting-model').waitFor();
        await permissionPage.getByRole('button',{name:'Permission rules',exact:true}).click();
        await permissionPage.locator('#permission-rule-input').waitFor();
        await permissionPage.waitForFunction(()=>!document.querySelector('#permission-rule-input').disabled);
        await permissionPage.locator('#permission-rule-input').fill('Read'); await permissionPage.getByRole('button',{name:'Add to draft',exact:true}).click();
        conflictNextPermission=true; await permissionPage.getByRole('button',{name:'Save rule changes',exact:true}).click();
        await permissionPage.waitForFunction(()=>document.querySelector('#settings-permissions .mcp-status').textContent.includes('draft is preserved'));
        assert.equal((await permissionPage.locator('#settings-permissions').textContent()).includes('/private'),false);
        assert.match(await permissionPage.locator('.permission-rule-draft').textContent(),/Read/);
        assert.equal(await permissionPage.getByRole('button',{name:'Save rule changes',exact:true}).isDisabled(),true);
        await permissionPage.locator('#settings-close').click(); await permissionPage.locator('#confirm-cancel').click();
        assert.equal(await permissionPage.locator('#settings-dialog').evaluate(node=>node.open),true);
        await permissionPage.locator('#settings-permissions').getByRole('button',{name:'Reload',exact:true}).click(); await permissionPage.locator('#confirm-accept').click();
        await permissionPage.waitForFunction(()=>document.querySelector('#settings-permissions .mcp-status').textContent.includes('Selected-file'));
        assert.equal(await permissionPage.locator('.permission-rule-draft').textContent(),'');
        await permissionPage.locator('#settings-close').click();
      });
      await check(`${surface} Mixed search serializes defaults and permission saves without draft loss`,async()=>{
        await permissionPage.locator('#settings-open').click(); await permissionPage.locator('#setting-model').waitFor();
        await permissionPage.locator('#settings-search').fill('permission');
        await permissionPage.locator('#permission-rule-input').waitFor();
        await permissionPage.waitForFunction(()=>!document.querySelector('#permission-rule-input').disabled);
        const mode=await permissionPage.locator('#setting-permission_mode').inputValue();
        await permissionPage.locator('#setting-permission_mode').selectOption(mode==='default' ? 'plan' : 'default');
        await permissionPage.locator('#permission-rule-input').fill('Write'); await permissionPage.getByRole('button',{name:'Add to draft',exact:true}).click();
        const writes=permissionWrites.length; holdNextSettingsWrite=true;
        try {
          await permissionPage.locator('#settings-save').click();
          await permissionPage.waitForFunction(()=>document.querySelector('#settings-save').textContent==='Working…');
          for(const locator of ['#permission-rule-scope','#permission-rule-action','#permission-rule-input']) assert.equal(await permissionPage.locator(locator).isDisabled(),true);
          for(const name of ['Save rule changes','Discard draft','Add to draft','Undo']) assert.equal(await permissionPage.getByRole('button',{name,exact:true}).isDisabled(),true);
          assert.equal(await permissionPage.locator('#settings-permissions').getByRole('button',{name:'Reload',exact:true}).isDisabled(),true);
          await permissionPage.getByRole('button',{name:'Save rule changes',exact:true}).evaluate(button=>button.click());
          assert.equal(permissionWrites.length,writes); assert.match(await permissionPage.locator('.permission-rule-draft').textContent(),/Write/);
        } finally {holdNextSettingsWrite=false; releaseReads();}
        await permissionPage.waitForFunction(()=>!document.querySelector('#permission-rule-input').disabled);
        assert.equal(await permissionPage.getByRole('button',{name:'Save rule changes',exact:true}).isEnabled(),true);
        await permissionPage.getByRole('button',{name:'Discard draft',exact:true}).click(); await permissionPage.locator('#confirm-accept').click();
        await permissionPage.locator('#settings-close').click();
      });
      await permissionPage.context().close();
    }
    const permissionTimeoutPage=await createPage('web',true);
    await check('Web permission write timeout preserves the committed draft until explicit reload',async()=>{
      await permissionTimeoutPage.clock.install();
      holdNextPermissionWrite=true;
      try {
        await permissionTimeoutPage.locator('#settings-open').click(); await permissionTimeoutPage.locator('#setting-model').waitFor();
        await permissionTimeoutPage.getByRole('button',{name:'Permission rules',exact:true}).click();
        await permissionTimeoutPage.locator('#permission-rule-input').waitFor();
        await permissionTimeoutPage.waitForFunction(()=>!document.querySelector('#permission-rule-input').disabled);
        const reads=permissionReads,writes=permissionWrites.length,rule='TimeoutFixture';
        await permissionTimeoutPage.locator('#permission-rule-input').fill(rule);
        await permissionTimeoutPage.getByRole('button',{name:'Add to draft',exact:true}).click();
        await permissionTimeoutPage.getByRole('button',{name:'Save rule changes',exact:true}).click();
        await waitForHeldRead(heldPermissionWrites);
        assert.equal(permissionWrites.length,writes+1); assert.ok(permissionRules.get('user').some(row=>row.value===rule));
        await permissionTimeoutPage.clock.fastForward(15_000);
        await permissionTimeoutPage.waitForFunction(()=>document.querySelector('#settings-permissions .mcp-status').textContent.includes('may have completed'));
        assert.match(await permissionTimeoutPage.locator('.permission-rule-draft').textContent(),/TimeoutFixture/);
        assert.equal(await permissionTimeoutPage.getByRole('button',{name:'Save rule changes',exact:true}).isDisabled(),true);
        assert.equal(await permissionTimeoutPage.locator('#permission-rule-input').isDisabled(),true);
        assert.equal(await permissionTimeoutPage.getByRole('button',{name:'Add to draft',exact:true}).isDisabled(),true);
        await permissionTimeoutPage.getByRole('button',{name:'Save rule changes',exact:true}).evaluate(button=>button.click());
        assert.equal(permissionWrites.length,writes+1,'timeout must not retry PATCH');
        releaseHeld(heldPermissionWrites); await new Promise(resolve=>setTimeout(resolve,25));
        assert.match(await permissionTimeoutPage.locator('.permission-rule-draft').textContent(),/TimeoutFixture/);
        await permissionTimeoutPage.locator('#settings-permissions').getByRole('button',{name:'Reload',exact:true}).click();
        await permissionTimeoutPage.locator('#confirm-cancel').click();
        assert.equal(permissionReads,reads); assert.match(await permissionTimeoutPage.locator('.permission-rule-draft').textContent(),/TimeoutFixture/);
        await permissionTimeoutPage.locator('#settings-permissions').getByRole('button',{name:'Reload',exact:true}).click();
        await permissionTimeoutPage.locator('#confirm-accept').click();
        await permissionTimeoutPage.waitForFunction(()=>document.querySelector('#settings-permissions .mcp-status').textContent.includes('Selected-file'));
        assert.equal(permissionReads,reads+1); assert.equal(await permissionTimeoutPage.locator('.permission-rule-draft').textContent(),'');
        assert.match(await permissionTimeoutPage.locator('#settings-permissions .permission-rule-list').textContent(),/TimeoutFixture/);
      } finally {holdNextPermissionWrite=false; releaseHeld(heldPermissionWrites);}
    });
    await permissionTimeoutPage.close();
    for (const surface of ['desktop', 'web']) {
      const choices = await createPage(surface);
      await check(`${surface} Empty profile catalog stays select-only through edit, save and reload`, async () => {
        await choices.locator('#settings-open').click();
        await choices.locator('#setting-model').waitFor();
        const profile = choices.locator('#setting-model_profile');
        assert.equal(await profile.evaluate(node => node.tagName), 'SELECT');
        assert.equal(await profile.isDisabled(), true);
        assert.equal(await profile.locator('option:checked').textContent(), 'No saved choices available');
        await choices.locator('#setting-model').fill(`${surface}-choice-fixture-model`);
        assert.equal(await profile.isDisabled(), true);
        holdNextSettingsWrite = true;
        try {
          await choices.locator('#settings-save').click();
          await waitForHeldRead(heldWrites);
          assert.equal(await profile.isDisabled(), true);
          assert.equal(await choices.locator('[data-setting="model_profile"] .setting-reset').isDisabled(), true);
          assert.equal(await choices.locator('#setting-model').isDisabled(), true);
          assert.equal(await choices.locator('#settings-credentials-open').isDisabled(), true);
        } finally {
          holdNextSettingsWrite = false; releaseReads();
        }
        await choices.waitForFunction(() => document.querySelector('#settings-status').textContent.startsWith('Saved.'));
        assert.equal(await profile.isDisabled(), true);
        assert.equal(await choices.locator('[data-setting="model_profile"] .setting-reset').isDisabled(), true);
        assert.equal(Object.hasOwn(stored, 'model_profile'), false);
        assert.equal(stored.model, `${surface}-choice-fixture-model`);
        await choices.locator('#settings-reload').click();
        await choices.waitForFunction(() => document.querySelector('#settings-status').textContent === 'Up to date');
        assert.equal(await profile.isDisabled(), true);
      });
      await check(`${surface} Unavailable external profile stays visible and can reset; catalog choices enable selection`, async () => {
        stored.model_profile = 'external-fixture-profile'; revision++;
        try {
          await choices.locator('#settings-reload').click();
          await choices.waitForFunction(() => document.querySelector('#settings-status').textContent === 'Up to date');
          const profile = choices.locator('#setting-model_profile');
          assert.equal(await profile.inputValue(), 'external-fixture-profile');
          assert.equal(await profile.isDisabled(), true);
          assert.equal(await profile.locator('option:checked').textContent(), 'external-fixture-profile (configured externally)');
          const reset = choices.locator('[data-setting="model_profile"] .setting-reset');
          assert.equal(await reset.isEnabled(), true);
          await reset.click();
          await choices.locator('#settings-save').click();
          await choices.waitForFunction(() => document.querySelector('#settings-status').textContent.startsWith('Saved.'));
          assert.equal(Object.hasOwn(stored, 'model_profile'), false);
          assert.equal(await profile.isDisabled(), true);
          definitions.model_profile.options = ['primary'];
          await choices.locator('#settings-reload').click();
          await choices.waitForFunction(() => document.querySelector('#settings-status').textContent === 'Up to date');
          assert.equal(await profile.isEnabled(), true);
          await profile.selectOption('primary');
          assert.equal(await profile.inputValue(), 'primary');
          assert.equal(await choices.locator('#settings-save').isEnabled(), true);
          // No fake persistence claim: profile/legacy exclusivity belongs to Go.
        } finally {
          delete definitions.model_profile.options; delete stored.model_profile;
        }
      });
      await choices.close();
      for (const scenario of ['no-override', 'pending-reset', 'invalid-draft-conflict']) {
        await check(`${surface} Settings reset ${scenario} is a reversible explicit-save draft`, async () => {
          const original = { ...stored };
          const originalProject = { ...projectStored };
          const editor = await createPage(surface, true);
          const writesBefore = settingsWrites.length;
          delete stored.model_profile;
          Object.assign(stored, { model: 'saved-model', max_turns: 0, reduced_motion: false, api_base_url: '' });
          if (scenario === 'no-override') delete stored.max_turns;
          if (scenario === 'pending-reset') {
            Object.assign(stored, { model: 'user-model', max_turns: 5, reduced_motion: true, api_base_url: 'https://api.example/v1' });
            Object.assign(projectStored, { model: 'saved-model', max_turns: 0, reduced_motion: false, api_base_url: 'https://workspace.example/v1' });
          }
          revision++;
          const row = key => editor.locator(`[data-setting="${key}"]`);
          const reset = key => row(key).locator('.setting-reset');
          try {
            await editor.locator('#settings-open').click();
            await editor.locator('#setting-model').waitFor();
            await editor.getByRole('button', { name: 'All settings', exact: true }).click();
            if (scenario === 'no-override') {
              const source = await row('max_turns').locator('.setting-source').textContent();
              assert.equal(await reset('max_turns').isDisabled(), true);
              assert.equal(await reset('reduced_motion').isEnabled(), true);
              assert.equal(await reset('api_base_url').isEnabled(), true);
              await editor.locator('#setting-max_turns').fill('9');
              // Choosing the inherited value can still be an explicit override.
              await editor.locator('#setting-max_turns').fill('0');
              assert.equal(await reset('max_turns').textContent(), 'Discard change');
              await reset('max_turns').click();
              await focusIs(editor, 'setting-max_turns');
              assert.equal(await editor.locator('#setting-max_turns').inputValue(), '0');
              assert.equal(await reset('max_turns').isDisabled(), true);
              assert.equal(await editor.locator('#settings-save').isDisabled(), true);
              assert.equal(await row('max_turns').locator('.setting-source').textContent(), source);
              assert.equal(settingsWrites.length, writesBefore);
            } else if (scenario === 'pending-reset') {
              await editor.locator('#settings-scope').selectOption('project');
              await editor.waitForFunction(() => document.querySelector('#settings-status').textContent === 'Up to date');
              await editor.getByRole('button', { name: 'All settings', exact: true }).click();
              assert.equal(await reset('model').textContent(), 'Use user default');
              const source = await row('model').locator('.setting-source').textContent();
              await editor.locator('#setting-model').fill('unsaved-model');
              await editor.locator('#setting-model').evaluate(node => node.setSelectionRange(2, 7, 'backward'));
              await reset('model').click();
              assert.equal(await row('model').locator('.setting-pending-reset').isVisible(), true);
              assert.equal(await editor.locator('#setting-model').isVisible(), false);
              assert.equal(await editor.locator('#setting-model').isDisabled(), true);
              assert.equal(await row('model').locator('.setting-source').textContent(), source);
              assert.equal(await reset('model').textContent(), 'Undo reset');
              assert.equal(settingsWrites.length, writesBefore);
              await editor.getByRole('button', { name: 'Agent behavior', exact: true }).click();
              await editor.locator('#settings-search').fill('model');
              assert.equal(await row('model').locator('.setting-pending-reset').isVisible(), true);
              await reset('model').click();
              assert.equal(await editor.locator('#setting-model').inputValue(), 'unsaved-model');
              assert.equal(await editor.locator('#setting-model').isEnabled(), true);
              await focusIs(editor, 'setting-model');
              assert.deepEqual(await editor.locator('#setting-model').evaluate(node => [node.selectionStart, node.selectionEnd, node.selectionDirection]), [2, 7, 'backward']);
              await reset('model').click();
              await editor.getByRole('button', { name: 'All settings', exact: true }).click();
              for (const key of ['max_turns', 'reduced_motion', 'api_base_url']) await reset(key).click();
              await screenshot(editor, `settings-reset-${surface}`);
              await editor.setViewportSize({ width: 390, height: 844 });
              assert.equal(await row('model').locator('.setting-pending-reset').isVisible(), true);
              assert.equal(await editor.locator('#settings-dialog').evaluate(node => node.scrollWidth <= node.clientWidth), true);
              await screenshot(editor, `settings-reset-narrow-${surface}`);
              holdNextSettingsWrite = true;
              await editor.locator('#settings-save').click();
              await waitForHeldRead(heldWrites);
              assert.equal(await reset('model').isDisabled(), true);
              assert.equal(await editor.locator('#setting-model').isDisabled(), true);
              assert.deepEqual(settingsWrites.at(-1).changes, { model: null, max_turns: null, reduced_motion: null, api_base_url: null });
              assert.equal(settingsWrites.at(-1).scope, 'project');
              releaseReads();
              await editor.waitForFunction(() => document.querySelector('#settings-status').textContent.startsWith('Saved.'));
              assert.equal(await reset('model').isDisabled(), true);
              assert.equal(await editor.locator('#setting-model').isVisible(), true);
              assert.equal(await editor.locator('#setting-model').inputValue(), 'user-model');
              assert.match(await row('model').locator('.setting-source').textContent(), /From user/);
              for (const [key, value] of Object.entries({ model: 'user-model', max_turns: 5, reduced_motion: true, api_base_url: 'https://api.example/v1' })) {
                assert.equal(Object.hasOwn(projectStored, key), false);
                assert.equal(stored[key], value);
              }
            } else {
              await editor.locator('#setting-max_turns').fill('7');
              await editor.locator('#setting-max_turns').fill('');
              await reset('max_turns').click();
              assert.equal(await editor.locator('#setting-max_turns').isDisabled(), true);
              await reset('max_turns').click();
              assert.equal(await editor.locator('#setting-max_turns').inputValue(), '');
              assert.equal(await editor.locator('#setting-max_turns').evaluate(node => node.checkValidity()), false);
              assert.equal(await editor.locator('#settings-save').isDisabled(), true);
              assert.equal(settingsWrites.length, writesBefore);
              await reset('max_turns').click();
              failNextSettingsWrite = true;
              await editor.locator('#settings-save').click();
              await editor.waitForFunction(() => document.querySelector('#settings-status').textContent.includes('write is unavailable'));
              assert.deepEqual(settingsWrites.at(-1).changes, { max_turns: null });
              assert.equal(stored.max_turns, 0);
              assert.equal(await row('max_turns').locator('.setting-pending-reset').isVisible(), true);
              assert.equal(await editor.locator('#settings-save').isEnabled(), true);
              conflictNext = true;
              await editor.locator('#settings-save').click();
              await editor.waitForFunction(() => document.querySelector('#settings-status').textContent.includes('another client'));
              assert.deepEqual(settingsWrites.at(-1).changes, { max_turns: null });
              assert.equal(await row('max_turns').locator('.setting-pending-reset').isVisible(), true);
              assert.equal(await reset('max_turns').isEnabled(), true);
              await reset('max_turns').click();
              assert.equal(await editor.locator('#setting-max_turns').inputValue(), '');
              assert.equal(await editor.locator('#settings-save').isDisabled(), true);
              await editor.locator('#settings-reload').click();
              await editor.locator('#confirm-accept').click();
              await editor.waitForFunction(() => document.querySelector('#settings-status').textContent === 'Up to date');
              assert.equal(await editor.locator('#setting-max_turns').inputValue(), '0');
              assert.equal(await row('max_turns').locator('.setting-pending-reset').isVisible(), false);
            }
          } finally {
            holdNextSettingsWrite = false; failNextSettingsWrite = false; conflictNext = false; releaseReads();
            await editor.close();
            for (const key of Object.keys(stored)) delete stored[key];
            Object.assign(stored, original); revision++;
            for (const key of Object.keys(projectStored)) delete projectStored[key];
            Object.assign(projectStored, originalProject);
          }
        });
      }
      for (const scenario of ['confirmation', 'revision-conflict', 'noneditable-blocker']) {
        await check(`${surface} Model selection ${scenario} is explicit and preserves the original draft`, async () => {
          const original = { ...stored };
          const selection = await createPage(surface);
          const writesBefore = settingsWrites.length;
          const turnsBefore = turnRequests;
          const sessionWritesBefore = sessionSettingWrites;
          Object.assign(stored, { provider: 'openai', model: 'before-model', api_base_url: 'https://api.example/v1' });
          delete stored.model_profile;
          definitions.model_profile.options = ['primary'];
          if (scenario === 'noneditable-blocker') definitions.model_profile.blocked_by = ['fallback_model'];
          revision++;
          try {
            await selection.locator('#settings-open').click();
            await selection.locator('#setting-model_profile').waitFor();
            await selection.locator('#setting-model_profile').selectOption('primary');
            const before = revision;
            await selection.locator('#settings-save').click();
            if (scenario === 'noneditable-blocker') {
              await selection.waitForFunction(() => document.querySelector('#settings-status').textContent.includes('fallback_model'), null, { timeout: 1500 });
              assert.equal(await selection.locator('#confirm-dialog').evaluate(node => node.open), false);
              assert.equal(settingsWrites.length, writesBefore);
              assert.equal(revision, before);
              assert.equal(await selection.locator('#setting-model_profile').inputValue(), 'primary');
              assert.equal(await selection.locator('#settings-save').isEnabled(), true);
              return;
            }
            await selection.locator('#confirm-dialog[open]').waitFor({ timeout: 1500 });
            assert.equal(await selection.locator('#confirm-copy').textContent(), 'Save Model profile and remove Provider, Model, API endpoint overrides from User defaults? Other scopes and current sessions will stay unchanged.');
            assert.equal(settingsWrites.length, writesBefore);
            assert.equal(revision, before);
            assert.equal(await selection.locator('#settings-close').isDisabled(), true);
            assert.equal(await selection.locator('#setting-model').isDisabled(), true);
            await screenshot(selection, `settings-selection-${surface}-${scenario}`);
            await selection.keyboard.press('Escape');
            await selection.locator('#confirm-dialog[open]').waitFor({ state: 'hidden' });
            await focusIs(selection, 'setting-model_profile');
            assert.equal(settingsWrites.length, writesBefore);
            assert.equal(await selection.locator('#setting-model_profile').inputValue(), 'primary');
            assert.equal(await selection.locator('#setting-provider').inputValue(), 'openai');
            assert.equal(await selection.locator('#setting-model').inputValue(), 'before-model');
            assert.equal(await selection.locator('#setting-api_base_url').inputValue(), 'https://api.example/v1');
            assert.equal(await selection.locator('#settings-save').isEnabled(), true);
            await selection.locator('#settings-save').click();
            await selection.locator('#confirm-dialog[open]').waitFor({ timeout: 1500 });
            if (scenario === 'revision-conflict') conflictNext = true;
            await selection.locator('#confirm-accept').click();
            const expected = { model_profile: 'primary', provider: null, model: null, api_base_url: null };
            if (scenario === 'revision-conflict') {
              await selection.waitForFunction(() => document.querySelector('#settings-status').textContent.includes('another client'));
              assert.equal(revision, before);
              assert.equal(await selection.locator('#setting-model_profile').inputValue(), 'primary');
              assert.equal(await selection.locator('#setting-provider').inputValue(), 'openai');
              assert.equal(await selection.locator('#setting-model').inputValue(), 'before-model');
              assert.equal(await selection.locator('#settings-save').isDisabled(), true);
            } else {
              await selection.waitForFunction(() => document.querySelector('#settings-status').textContent.startsWith('Saved.'));
              assert.equal(revision, before + 1);
              assert.equal(stored.model_profile, 'primary');
              for (const key of ['provider', 'model', 'api_base_url']) assert.equal(Object.hasOwn(stored, key), false);
              assert.equal(stored.theme, original.theme);
            }
            assert.equal(settingsWrites.length, writesBefore + 1);
            assert.deepEqual(settingsWrites.at(-1).changes, expected);
            if (scenario === 'confirmation') {
              // Editing legacy fields chooses that route; the profile removal is also explicit.
              await selection.locator('#setting-model').fill('after-model');
              await selection.locator('#settings-save').click();
              await selection.locator('#confirm-dialog[open]').waitFor({ timeout: 1500 });
              assert.match(await selection.locator('#confirm-copy').textContent(), /remove Model profile overrides/);
              await selection.locator('#confirm-accept').click();
              await selection.waitForFunction(() => document.querySelector('#settings-status').textContent.startsWith('Saved.'));
              assert.deepEqual(settingsWrites.at(-1).changes, { model: 'after-model', model_profile: null });
              assert.equal(Object.hasOwn(stored, 'model_profile'), false);
            }
            assert.equal(turnRequests, turnsBefore);
            assert.equal(sessionSettingWrites, sessionWritesBefore);
          } finally {
            await selection.close();
            for (const key of Object.keys(stored)) delete stored[key];
            Object.assign(stored, original);
            delete definitions.model_profile.options; delete definitions.model_profile.blocked_by;
            conflictNext = false; revision++;
          }
        });
      }
      const categories = await createPage(surface, true);
      await check(`${surface} Settings starts with model setup and keyboard categories retain focus`, async () => {
        await categories.locator('#settings-open').click();
        await categories.locator('#setting-model').waitFor();
        assert.deepEqual(await categories.locator('[data-setting]:visible').evaluateAll(rows => rows.map(row => row.dataset.setting).sort()),
          ['api_base_url', 'model', 'model_profile', 'provider']);
        assert.equal(await categories.locator('.settings-group-heading:visible').count(), 1);
        const behavior = categories.getByRole('button', { name: 'Agent behavior', exact: true });
        await behavior.focus();
        await behavior.press('Enter');
        assert.equal(await behavior.getAttribute('aria-pressed'), 'true');
        assert.equal(await behavior.evaluate(node => document.activeElement === node), true);
        assert.deepEqual(await categories.locator('[data-setting]:visible').evaluateAll(rows => rows.map(row => row.dataset.setting).sort()),
          ['max_turns', 'permission_mode']);
        await screenshot(categories, `settings-categories-${surface}`);
      });
      await check(`${surface} Category navigation and global search preserve hidden unsaved values`, async () => {
        await categories.getByRole('button', { name: 'Model & provider', exact: true }).click();
        await categories.locator('#setting-model').fill(`${surface}-category-fixture-model`);
        await categories.evaluate(() => { globalThis.categoryModelNode = document.querySelector('#setting-model'); });
        await categories.getByRole('button', { name: 'Appearance & input', exact: true }).click();
        await categories.locator('#setting-reduced_motion').selectOption('true');
        await categories.locator('#settings-search').fill('maximum turns');
        assert.equal(await categories.getByRole('button', { name: 'All settings', exact: true }).getAttribute('aria-pressed'), 'true');
        assert.deepEqual(await categories.locator('[data-setting]:visible').evaluateAll(rows => rows.map(row => row.dataset.setting)), ['max_turns']);
        await categories.locator('#setting-max_turns').fill('9');
        await categories.getByRole('button', { name: 'Model & provider', exact: true }).click();
        assert.equal(await categories.locator('#settings-search').inputValue(), '');
        assert.equal(await categories.locator('#setting-model').inputValue(), `${surface}-category-fixture-model`);
        assert.equal(await categories.evaluate(() => globalThis.categoryModelNode === document.querySelector('#setting-model')), true);
        await categories.locator('#settings-save').click();
        await categories.waitForFunction(() => document.querySelector('#settings-status').textContent.startsWith('Saved.'));
        assert.equal(stored.model, `${surface}-category-fixture-model`);
        assert.equal(stored.reduced_motion, true);
        assert.equal(stored.max_turns, 9);
      });
      await check(`${surface} Narrow Settings categories do not clip and hidden invalid fields regain focus`, async () => {
        await categories.setViewportSize({ width: 390, height: 844 });
        const navigation = categories.locator('#settings-categories');
        assert.equal(await navigation.evaluate(node => node.scrollWidth <= node.clientWidth), true);
        await categories.getByRole('button', { name: 'Agent behavior', exact: true }).click();
        await categories.locator('#setting-max_turns').fill('10');
        await categories.locator('#setting-max_turns').fill('');
        await categories.getByRole('button', { name: 'Model & provider', exact: true }).click();
        const before = revision;
        assert.equal(await categories.locator('#settings-save').isDisabled(), true);
        assert.equal(await categories.locator('#settings-form').evaluate(form => form.reportValidity()), false);
        assert.equal(revision, before);
        assert.equal(await categories.locator('#setting-max_turns').isVisible(), true);
        await focusIs(categories, 'setting-max_turns');
        await screenshot(categories, `settings-narrow-${surface}`);
      });
      await categories.close();
      for (const scenario of ['discovery', 'scroll-shell', 'credential-entry']) {
        await check(`${surface} Settings task shell ${scenario} preserves contextual ownership`, async () => {
          const editor = await createPage(surface);
          const writesBefore = settingsWrites.length;
          const turnsBefore = turnRequests;
          try {
            await editor.locator('#settings-open').click();
            await editor.locator('#setting-model').waitFor();
            const credentials = editor.locator('.settings-credentials');
            const summary = credentials.locator('summary');
            if (scenario === 'discovery') {
              assert.equal(await credentials.evaluate(node => node.tagName), 'DETAILS');
              assert.equal(await credentials.evaluate(node => node.open), false);
              assert.equal(await credentials.isVisible(), true);
              assert.equal(await editor.locator('#settings-credentials-copy').isVisible(), false);
              await editor.locator('#setting-model').fill(`${surface}-task-shell-draft`);
              await editor.evaluate(() => { globalThis.shellModelNode = document.querySelector('#setting-model'); });
              for (const name of ['Agent behavior', 'Appearance & input']) {
                await editor.getByRole('button', { name, exact: true }).click();
                assert.equal(await credentials.isVisible(), false);
              }
              await editor.locator('#settings-search').fill('api key');
              assert.equal(await credentials.isVisible(), true);
              assert.equal(await editor.locator('[data-setting]:visible').count(), 0);
              assert.equal(await editor.locator('.settings-no-results').isVisible(), false);
              await summary.focus();
              await summary.press('Enter');
              assert.equal(await credentials.evaluate(node => node.open), true);
              assert.equal(await editor.locator('#settings-credentials-copy').isVisible(), true);
              assert.equal(await summary.evaluate(node => document.activeElement === node), true);
              await summary.press('Space');
              assert.equal(await credentials.evaluate(node => node.open), false);
              await summary.press('Enter');
              await editor.locator('#settings-search').fill('theme');
              assert.equal(await credentials.isVisible(), false);
              await editor.locator('#settings-search').fill('missing setting');
              assert.equal(await credentials.isVisible(), false);
              assert.equal(await editor.locator('.settings-no-results').isVisible(), true);
              await editor.getByRole('button', { name: 'Model & provider', exact: true }).click();
              assert.equal(await credentials.evaluate(node => node.open), true, 'filtering preserves the disclosure choice');
              assert.equal(await editor.locator('#setting-model').inputValue(), `${surface}-task-shell-draft`);
              assert.equal(await editor.evaluate(() => globalThis.shellModelNode === document.querySelector('#setting-model')), true);
              await screenshot(editor, `settings-task-model-${surface}`);
            } else if (scenario === 'scroll-shell') {
              assert.equal(await editor.locator('#settings-body').count(), 1);
              await editor.getByRole('button', { name: 'All settings', exact: true }).click();
              for (const viewport of [{ width: 1440, height: 1000 }, { width: 390, height: 844 }]) {
                await editor.setViewportSize(viewport);
                const positions = async () => editor.locator('#settings-dialog').evaluate(node =>
                  ['settings-title', 'settings-scope', 'settings-categories', 'settings-save'].map(id => {
                    const r = document.getElementById(id).getBoundingClientRect();
                    return [id, r.top, r.bottom];
                  }));
                const before = await positions();
                await editor.locator('#settings-body').evaluate(node => { node.scrollTop = node.scrollHeight; });
                assert.equal(await editor.locator('#settings-body').evaluate(node => node.scrollTop > 0), true);
                assert.deepEqual(await positions(), before);
                for (const [, top, bottom] of before) assert.equal(top >= 0 && bottom <= viewport.height, true);
                assert.equal(await editor.locator('#settings-dialog').evaluate(node => node.scrollWidth <= node.clientWidth), true);
                await screenshot(editor, `settings-task-scroll-${viewport.width}-${surface}`);
              }
              await editor.getByRole('button', { name: 'Agent behavior', exact: true }).click();
              assert.equal(await editor.locator('#settings-body').evaluate(node => node.scrollTop), 0);
              await editor.setViewportSize({ width: 390, height: 430 });
              await editor.getByRole('button', { name: 'All settings', exact: true }).click();
              assert.equal(await editor.locator('#settings-dialog').evaluate(node => node.scrollHeight > node.clientHeight), true);
              await editor.locator('#setting-prompt_suggestions').focus();
              assert.equal(await editor.locator('#setting-prompt_suggestions').evaluate(node => {
                const r = node.getBoundingClientRect();
                return r.top >= 0 && r.bottom <= document.querySelector('.settings-footer').getBoundingClientRect().top;
              }), true);
              await screenshot(editor, `settings-task-short-field-${surface}`);
              await editor.locator('#setting-max_turns').fill('');
              await editor.locator('#setting-prompt_suggestions').selectOption('false');
              await editor.getByRole('button', { name: 'Appearance & input', exact: true }).click();
              assert.equal(await editor.locator('[data-setting="max_turns"]').isVisible(), false);
              assert.equal(await editor.locator('#settings-save').isDisabled(), true);
              assert.equal(await editor.locator('#settings-form').evaluate(form => form.reportValidity()), false);
              const invalidState = await editor.locator('#setting-max_turns').evaluate(node => {
                const r = node.getBoundingClientRect();
                const footer = document.querySelector('.settings-footer').getBoundingClientRect();
                return { focus: document.activeElement?.id, top: r.top, bottom: r.bottom, footerTop: footer.top, viewportHeight: innerHeight };
              });
              assert.equal(invalidState.focus === 'setting-max_turns' && invalidState.top >= 0 && invalidState.bottom <= Math.min(invalidState.viewportHeight, invalidState.footerTop), true,
                'hidden invalid input is revealed, focused and not covered in a short window: ' + JSON.stringify(invalidState));
              assert.equal(await editor.getByRole('button', { name: 'Agent behavior', exact: true }).getAttribute('aria-pressed'), 'true');
              assert.equal(await editor.locator('#settings-dialog').evaluate(node => node.scrollWidth <= node.clientWidth), true);
              await screenshot(editor, `settings-task-short-invalid-${surface}`);
              await editor.locator('#setting-max_turns').fill('0');
              assert.equal(await editor.locator('#settings-save').isEnabled(), true);
              await editor.locator('#settings-save').focus();
              assert.equal(await editor.locator('#settings-save').evaluate(node => {
                const r = node.getBoundingClientRect();
                return document.activeElement === node && r.top >= 0 && r.bottom <= innerHeight;
              }), true, 'short-window save action remains naturally focusable');
              await editor.locator('#settings-close').focus();
              assert.equal(await editor.locator('#settings-close').evaluate(node => {
                const r = node.getBoundingClientRect();
                return document.activeElement === node && r.top >= 0 && r.bottom <= innerHeight;
              }), true);
              await screenshot(editor, `settings-task-short-${surface}`);
            } else {
              assert.equal(await summary.count(), 1);
              await summary.click();
              const manage = editor.locator('#settings-credentials-open');
              assert.equal(await manage.isVisible(), surface === 'desktop');
              if (surface === 'desktop') {
                await editor.locator('#setting-model').fill('credential-entry-unsaved-model');
                await manage.click();
                await editor.locator('#confirm-dialog[open]').waitFor();
                await editor.locator('#confirm-cancel').click();
                assert.equal(await editor.locator('#settings-dialog').evaluate(node => node.open), true);
                assert.equal(await editor.locator('#provider-dialog').evaluate(node => node.open), false);
                assert.equal(await editor.locator('#setting-model').inputValue(), 'credential-entry-unsaved-model');
                await manage.click();
                await editor.locator('#confirm-accept').click();
                await editor.locator('#provider-dialog[open]').waitFor();
                assert.equal(await editor.locator('#settings-dialog').evaluate(node => node.open), false);
                assert.equal(await editor.locator('#provider-api-key').inputValue(), '');
                await editor.keyboard.press('Escape');
                await editor.locator('#settings-open').click();
                await editor.locator('#setting-model').waitFor();
                assert.equal(await credentials.evaluate(node => node.open), false, 'fresh opens collapse the help');
              } else {
                assert.match(await editor.locator('#settings-credentials-copy').textContent(), /host environment/);
              }
            }
            assert.equal(settingsWrites.length, writesBefore);
            assert.equal(turnRequests, turnsBefore);
          } finally { await editor.close(); }
        });
      }
    }
    for (const recovery of ['create', 'cancel', 'save-failure', 'held-create', 'held-provider', 'newer-notice']) {
      await check(`Desktop provider recovery ${recovery} never reuses a retired workspace handle`, async () => {
        const recovering = await createPage('desktop', false, true, 'openai', recovery);
        const turnsBefore = turnRequests;
        const settingsBefore = settingsWrites.length;
        const creationsBefore = creations;
        try {
          await recovering.locator('[data-empty-action="workspace"]').click();
          if (recovery === 'held-create') {
            await recovering.waitForFunction(() => typeof window.__releaseWorkspaceCreate === 'function');
            assert.equal(await recovering.locator('#provider-settings').isDisabled(), true);
            assert.equal(await recovering.locator('[data-empty-action="provider"]').isDisabled(), true);
            assert.equal(await recovering.locator('#provider-save').isDisabled(), true);
            await recovering.evaluate(() => {
              document.querySelector('#provider-settings').onclick();
              document.querySelector('#provider-api-key').value = 'isolated-acceptance-fixture-key';
              document.querySelector('#provider-form').dispatchEvent(new Event('submit', { cancelable: true }));
            });
            assert.equal(await recovering.locator('#provider-dialog').evaluate(node => node.open), false);
            assert.equal(await recovering.evaluate(() => window.__workspaceRecovery.configureCalls), 0);
            await recovering.evaluate(() => window.__releaseWorkspaceCreate());
          }
          await recovering.locator('#workbench-notice-copy').filter({ hasText: 'Fixture provider configuration is required.' }).waitFor();
          assert.deepEqual(await recovering.evaluate(() => window.__workspaceRecovery.createHandles), ['fixture-0-1']);
          await recovering.locator('[data-empty-action="provider"]').click();
          await recovering.locator('#provider-dialog[open]').waitFor();
          await recovering.locator('#provider-api-key').fill('isolated-acceptance-fixture-key');
          await recovering.locator('#provider-save').click();
          if (recovery === 'newer-notice') {
            await recovering.waitForFunction(() => typeof window.__releaseRecoveredCreate === 'function');
            await recovering.locator('#open-web').click();
            await recovering.locator('#workbench-notice-copy').filter({ hasText: 'Fixture browser could not open.' }).waitFor();
            await recovering.evaluate(() => window.__releaseRecoveredCreate());
          }
          if (recovery === 'held-provider') {
            await recovering.waitForFunction(() => typeof window.__releaseProviderConfigure === 'function');
            assert.equal(await recovering.locator('#new-session').isDisabled(), true);
            assert.equal(await recovering.locator('[data-empty-action="workspace"]').isDisabled(), true);
            await recovering.evaluate(() => {
              document.querySelector('#new-session').onclick();
              document.querySelector('[data-empty-action="workspace"]').onclick();
            });
            assert.equal(await recovering.evaluate(() => window.__workspaceRecovery.pickerCalls), 1);
            assert.equal(await recovering.locator('#new-session').getAttribute('aria-busy'), 'false');
            await recovering.evaluate(() => window.__releaseProviderConfigure());
          }
          if (recovery === 'save-failure') {
            await recovering.locator('#provider-dialog-status').filter({ hasText: 'Fixture profile could not be saved.' }).waitFor();
          } else {
            await recovering.waitForFunction(() => window.__workspaceRecovery.pickerCalls === 2 && document.querySelector('#new-session').getAttribute('aria-busy') === 'false');
            assert.equal(await recovering.locator('#provider-dialog').evaluate(node => node.open), false);
          }
          const observed = await recovering.evaluate(() => window.__workspaceRecovery);
          assert.equal(observed.configureCalls, 1);
          assert.equal(observed.pickerCalls, recovery === 'save-failure' ? 1 : 2);
          const createsSession = ['create', 'held-create', 'held-provider', 'newer-notice'].includes(recovery);
          assert.deepEqual(observed.createHandles, createsSession ? ['fixture-0-1', 'fixture-1-2'] : ['fixture-0-1']);
          assert.equal(creations, creationsBefore + (createsSession ? 1 : 0));
          assert.equal(await recovering.locator('#provider-api-key').inputValue(), '');
          assert.equal(await recovering.locator('#composer').isVisible(), createsSession);
          assert.equal(await recovering.locator('#workbench-notice').isVisible(), !createsSession || recovery === 'newer-notice');
          assert.equal(await recovering.locator('#workbench-notice-copy').textContent(),
            recovery === 'newer-notice' ? 'Fixture browser could not open.' : createsSession ? '' : 'Fixture provider configuration is required.');
          assert.equal(turnRequests, turnsBefore);
          assert.equal(settingsWrites.length, settingsBefore);
          await screenshot(recovering, `provider-recovery-${recovery}`);
        } finally {
          await recovering.evaluate(() => {
            window.__releaseWorkspaceCreate?.();
            window.__releaseProviderConfigure?.();
            window.__releaseRecoveredCreate?.();
          });
          await recovering.close();
        }
      });
    }
    await check('Desktop explicit workspace retry resolves only its earlier creation notice', async () => {
      const recovering = await createPage('desktop', false, true, 'openai', 'manual-retry');
      const turnsBefore = turnRequests;
      const settingsBefore = settingsWrites.length;
      try {
        await recovering.locator('#new-session').click();
        await recovering.locator('#workbench-notice-copy').filter({ hasText: 'Fixture provider configuration is required.' }).waitFor();
        // Model a repaired runtime configuration, not a Desktop vault save.
        await recovering.evaluate(() => { window.__workspaceRecovery.saved = true; });
        await recovering.locator('#new-session').click();
        await recovering.waitForFunction(() => document.querySelector('#new-session').getAttribute('aria-busy') === 'false' && document.querySelector('#session-title').textContent === 'Improve the search experience');
        assert.equal(await recovering.locator('#workbench-notice').isVisible(), false);
        assert.equal(await recovering.locator('#workbench-notice-copy').textContent(), '');
        const observed = await recovering.evaluate(() => window.__workspaceRecovery);
        assert.equal(observed.configureCalls, 0);
        assert.equal(observed.pickerCalls, 2);
        assert.deepEqual(observed.createHandles, ['fixture-0-1', 'fixture-0-2']);
        assert.equal(turnRequests, turnsBefore);
        assert.equal(settingsWrites.length, settingsBefore);
        await screenshot(recovering, 'creation-notice-manual-retry');
      } finally { await recovering.close(); }
    });
    await check('Desktop new-workspace failure preserves the selected conversation and draft', async () => {
      const existing = await createPage('desktop', true, true, 'openai', 'manual-retry');
      const turnsBefore = turnRequests;
      const settingsBefore = settingsWrites.length;
      try {
        await existing.locator(`[data-session-id="${session.id}"] .session-select`).click();
        await existing.locator('#composer').waitFor();
        await existing.locator('#prompt').fill('Keep this conversation draft.');
        const noticeBefore = await existing.locator('#turn-state').textContent();
        await existing.locator('#new-session').click();
        await existing.locator('#workbench-notice-copy').filter({ hasText: 'Fixture provider configuration is required.' }).waitFor();
        assert.equal(await existing.locator('#session-title').textContent(), session.title);
        assert.equal(await existing.locator('#prompt').inputValue(), 'Keep this conversation draft.');
        assert.equal(await existing.locator('#turn-state').textContent(), noticeBefore);
        assert.equal(turnRequests, turnsBefore);
        assert.equal(settingsWrites.length, settingsBefore);
        await screenshot(existing, 'creation-notice-existing-conversation');
      } finally { await existing.close(); }
    });
    const sharedConfig = await createPage('desktop', false, true);
    await check('Desktop without a vault profile delegates workspace creation to shared runtime configuration', async () => {
      const before = creations;
      await sharedConfig.locator('#new-session').click();
      await sharedConfig.waitForFunction(() => document.querySelector('#session-title').textContent === 'Improve the search experience');
      assert.equal(creations, before + 1);
      assert.equal(await sharedConfig.locator('#provider-dialog').evaluate(node => node.open), false);
      assert.equal(await sharedConfig.locator('#composer').isVisible(), true);
    });
    const conversation = await createPage('web', true);
    await check('Web command candidates edit the draft without submitting and share argument quoting', async () => {
      const prompt = conversation.locator('#prompt');
      await prompt.fill('/mod');
      await conversation.locator('[role="option"]').first().waitFor();
      assert.equal(await prompt.inputValue(), '/mod');
      await prompt.press('ArrowDown');
      assert.equal(await prompt.inputValue(), '/mod');
      await prompt.press('Enter');
      assert.equal(await prompt.inputValue(), '/model ');
      await conversation.locator('[role="option"]').first().waitFor();
      await prompt.press('Tab');
      assert.equal(await prompt.inputValue(), '/model "中文 model" ');
      await prompt.fill('/mod');
      await conversation.locator('[role="option"]').first().waitFor();
      await prompt.press('Escape');
      assert.equal(await prompt.inputValue(), '/mod');
      assert.equal(await conversation.locator('#command-completion').isVisible(), false);
      await prompt.fill('/mod');
      await prompt.evaluate(node => node.dispatchEvent(new CompositionEvent('compositionstart')));
      assert.equal(await conversation.locator('#command-completion').isVisible(), false);
      await prompt.evaluate(node => node.dispatchEvent(new CompositionEvent('compositionend')));
      await conversation.locator('[role="option"]').first().waitFor();
      await conversation.locator('[role="option"]').first().click();
      assert.equal(await prompt.inputValue(), '/model ');
      assert.equal(await conversation.locator('#status').textContent(), 'idle');
      await prompt.fill('');
    });
    const desktopCommands = await createPage('desktop', true);
    for (const surface of ['web', 'desktop']) {
      async function settleHeldReply(page, queue, op, index = 0) {
        const response = page.waitForResponse(response => {
          if (surface === 'web') return op === 'startTurn'
            ? response.request().method() === 'POST' && response.url().endsWith('/turns')
            : response.request().method() === 'DELETE' && /\/v1\/sessions\/[^/]+$/.test(response.url());
          return response.url().endsWith('/fixture/rpc') && response.request().postDataJSON()?.op === op;
        });
        const held = queue.splice(index, 1)[0];
        assert.ok(held, 'the selected response is held');
        reply(held.res, held.body);
        await (await response).finished();
        // Let the settled fetch promise and synchronous render run, then inspect
        // the next painted frame. No timing sleep stands in for reply admission.
        await page.evaluate(() => new Promise(resolve => requestAnimationFrame(() => requestAnimationFrame(resolve))));
      }
      const settleHeldTurn = page => settleHeldReply(page, heldTurns, 'startTurn');
      await check(`${surface} duplicate ordinary Send shares its unchanged draft request`, async () => {
        const page = await createPage(surface, true);
        try {
          const before = turnRequests;
          await page.locator('#prompt').fill('Submit this draft');
          holdNextTurn = true;
          await page.locator('#send').click();
          await waitForHeldRead(heldTurns);
          await page.locator('#send').click();
          await page.evaluate(() => new Promise(resolve => requestAnimationFrame(() => requestAnimationFrame(resolve))));
          assert.equal(turnRequests, before + 1, 'an unchanged in-flight draft dispatches once');
          await settleHeldTurn(page);
          assert.equal(await page.locator('#prompt').inputValue(), '');
        } finally { holdNextTurn = false; releaseReads(); await page.close(); }
      });
      await check(`${surface} ordinary send clears only its unchanged draft`, async () => {
        const page = await createPage(surface, true);
        try {
          const before = turnRequests;
          await page.locator('#prompt').fill('  Submit this draft  ');
          holdNextTurn = true;
          await page.locator('#send').click();
          await waitForHeldRead(heldTurns);
          assert.equal(await page.locator('#prompt').inputValue(), '  Submit this draft  ');
          await settleHeldTurn(page);
          assert.equal(await page.locator('#prompt').inputValue(), '');
          assert.equal(turnRequests, before + 1);
        } finally { holdNextTurn = false; releaseReads(); await page.close(); }
      });
      await check(`${surface} late ordinary-send replies preserve new and restored input`, async () => {
        for (const replacement of ['A newer draft', 'Submit this draft']) {
          const page = await createPage(surface, true);
          try {
            const before = turnRequests;
            await page.locator('#prompt').fill('Submit this draft');
            holdNextTurn = true;
            await page.locator('#send').click();
            await waitForHeldRead(heldTurns);
            await page.locator('#prompt').fill('A temporary edit');
            await page.locator('#prompt').fill(replacement);
            await settleHeldTurn(page);
            assert.equal(await page.locator('#prompt').inputValue(), replacement);
            assert.equal(turnRequests, before + 1, 'typing while pending never dispatches');
          } finally { holdNextTurn = false; releaseReads(); await page.close(); }
        }
      });
      await check(`${surface} late ordinary-send replies cannot clear another session draft`, async () => {
        const page = await createPage(surface, true);
        try {
          const before = turnRequests;
          const prompt = page.locator('#prompt');
          await prompt.fill('Submit this draft');
          holdNextTurn = true;
          await page.locator('#send').click();
          await waitForHeldRead(heldTurns);
          await prompt.fill('A newer source-session draft');
          await page.getByText(secondSession.title, { exact: true }).click();
          await page.waitForFunction(() => document.querySelector('#session-title').textContent === 'Review the keyboard flow');
          await prompt.fill('The other session draft');
          await settleHeldTurn(page);
          assert.equal(await prompt.inputValue(), 'The other session draft');
          await page.getByText(session.title, { exact: true }).click();
          await page.waitForFunction(() => document.querySelector('#session-title').textContent === 'Improve the search experience');
          assert.equal(await prompt.inputValue(), 'A newer source-session draft');
          assert.equal(turnRequests, before + 1);
        } finally { holdNextTurn = false; releaseReads(); await page.close(); }
      });
      await check(`${surface} failed ordinary-send replies retain input and allow a later explicit send`, async () => {
        const page = await createPage(surface, true);
        try {
          const before = turnRequests;
          const prompt = page.locator('#prompt');
          await prompt.fill('Submit this draft');
          holdNextTurn = true; failNextTurn = true;
          await page.locator('#send').click();
          await waitForHeldRead(heldTurns);
          await prompt.fill('The replacement draft');
          await settleHeldTurn(page);
          assert.equal(await prompt.inputValue(), 'The replacement draft');
          assert.ok((await page.locator('#turn-state').textContent()).includes('Fixture turn admission is unavailable.'));
          assert.equal(turnRequests, before + 1);
          holdNextTurn = true;
          await page.locator('#send').click();
          await waitForHeldRead(heldTurns);
          await settleHeldTurn(page);
          assert.equal(await prompt.inputValue(), '');
          assert.equal(turnRequests, before + 2, 'only the explicit second send dispatches');
        } finally { holdNextTurn = false; failNextTurn = false; releaseReads(); await page.close(); }
      });
      await check(`${surface} closing a session retains the latest settled draft`, async () => {
        for (const { editDuringClose, closeFirst } of [
          { editDuringClose: false, closeFirst: false },
          { editDuringClose: true, closeFirst: false },
          { editDuringClose: false, closeFirst: true },
        ]) {
          const page = await createPage(surface, true);
          try {
            const prompt = page.locator('#prompt');
            await prompt.fill('Submit this draft');
            holdNextTurn = true;
            await page.locator('#send').click();
            await waitForHeldRead(heldTurns);
            // Re-select through the public UI so the row captures the submitted
            // draft, rather than only the empty bootstrap descriptor.
            await page.locator(`[data-session-id="${session.id}"] .session-select`).click();
            holdNextClose = true;
            await page.locator(`[data-session-id="${session.id}"] .session-close`).click();
            await page.locator('#confirm-accept').click();
            await waitForHeldRead(heldCloses);
            if (editDuringClose) await prompt.fill('Edited while closing');
            const expected = closeFirst ? 'Submit this draft' : editDuringClose ? 'Edited while closing' : '';
            if (!closeFirst) {
              await settleHeldTurn(page);
              assert.equal(await prompt.inputValue(), expected);
            }
            await settleHeldReply(page, heldCloses, 'closeSession');
            await page.locator(`[data-session-id="${session.id}"] .session-close`).waitFor({ state: 'detached' });
            assert.equal(await prompt.inputValue(), expected, 'closing cannot restore a stale captured draft');
            if (closeFirst) {
              await settleHeldTurn(page);
              assert.equal(await prompt.inputValue(), expected, 'a retired attempt cannot clear the saved draft');
            }
          } finally { holdNextTurn = false; holdNextClose = false; releaseReads(); await page.close(); }
        }
      });
      await check(`${surface} overlapping explicit sends keep attempt cleanup isolated`, async () => {
        for (const order of ['older-first', 'newer-first', 'older-failure']) {
          const page = await createPage(surface, true);
          try {
            const before = turnRequests;
            const prompt = page.locator('#prompt');
            await prompt.fill('First submitted draft');
            holdNextTurn = true; failNextTurn = order === 'older-failure';
            await page.locator('#send').click();
            await waitForHeldRead(heldTurns);
            await prompt.fill('Second submitted draft');
            holdNextTurn = true;
            await page.locator('#send').click();
            await waitForHeldRead(heldTurns, 2);
            if (order === 'newer-first') {
              await settleHeldReply(page, heldTurns, 'startTurn', 1);
              assert.equal(await prompt.inputValue(), '');
              await prompt.fill('A third unsent draft');
              await settleHeldTurn(page);
              assert.equal(await prompt.inputValue(), 'A third unsent draft');
            } else {
              await settleHeldTurn(page);
              assert.equal(await prompt.inputValue(), 'Second submitted draft');
              await settleHeldTurn(page);
              assert.equal(await prompt.inputValue(), '', 'older cleanup must not remove the newer attempt');
            }
            assert.equal(turnRequests, before + 2, 'only the two explicit sends dispatch');
          } finally { holdNextTurn = false; failNextTurn = false; releaseReads(); await page.close(); }
        }
      });
    }
    for (const surface of ['web', 'desktop']) {
      stored.prompt_suggestions = true; revision++;
      const suggestionPage = await createPage(surface, true);
      await check(`${surface} explicit next-step suggestion is draft-only and undoable`, async () => {
        const beforeCalls = suggestionRequests, beforeTurns = turnRequests;
        await suggestionPage.locator('#suggest-prompt').waitFor();
        assert.equal(suggestionRequests, beforeCalls, 'opening a session never requests a suggestion');
        await suggestionPage.locator('#suggest-prompt').click();
        const candidate = suggestionPage.locator('#accept-suggestion');
        await candidate.waitFor();
        assert.equal(await suggestionPage.locator('#prompt').inputValue(), '');
        assert.equal(suggestionRequests, beforeCalls + 1);
        await screenshot(suggestionPage, `prompt-suggestion-${surface}`);
        await candidate.click();
        const prompt = suggestionPage.locator('#prompt');
        assert.equal(await prompt.inputValue(), 'run the focused tests');
        await focusIs(suggestionPage, 'prompt');
        await prompt.press('ControlOrMeta+z');
        assert.equal(await prompt.inputValue(), '');
        await prompt.press('ControlOrMeta+Shift+z');
        assert.equal(await prompt.inputValue(), 'run the focused tests');
        assert.equal(turnRequests, beforeTurns, 'proposal, acceptance, undo and redo never send');
        await prompt.fill('');
      });
      await check(`${surface} edited and switched drafts reject delayed suggestions`, async () => {
        const prompt = suggestionPage.locator('#prompt');
        holdNextSuggestion = true;
        await suggestionPage.locator('#suggest-prompt').click();
        await waitForHeldRead(heldSuggestions);
        await prompt.fill('my own next step');
        await prompt.fill('');
        releaseReads();
        await suggestionPage.waitForTimeout(100);
        assert.equal(await suggestionPage.locator('#accept-suggestion').isVisible(), false);
        assert.equal(await prompt.inputValue(), '');
        holdNextSuggestion = true;
        await suggestionPage.locator('#suggest-prompt').click();
        await waitForHeldRead(heldSuggestions);
        await suggestionPage.getByText(secondSession.title, { exact: true }).click();
        await suggestionPage.waitForFunction(() => document.querySelector('#session-title').textContent === 'Review the keyboard flow');
        releaseReads();
        await suggestionPage.waitForTimeout(100);
        assert.equal(await suggestionPage.locator('#accept-suggestion').isVisible(), false);
        assert.equal(await prompt.inputValue(), '');
        assert.equal(await suggestionPage.locator('#timeline').textContent().then(text => text.includes('run the focused tests')), false);
        await suggestionPage.close();
      });
    }
    await check('Desktop command menu stays in the textarea and Tab does not submit', async () => {
      const prompt = desktopCommands.locator('#prompt');
      await prompt.fill('/mod');
      await desktopCommands.locator('[role="option"]').first().waitFor();
      await prompt.press('Tab');
      assert.equal(await prompt.inputValue(), '/model ');
      await focusIs(desktopCommands, 'prompt');
      await desktopCommands.locator('[role="option"]').first().waitFor();
      await screenshot(desktopCommands, 'command-completion');
    });
    for (const surface of ['web', 'desktop']) {
      const completionUndo = await createPage(surface, true);
      await check(`${surface} completion is one native undoable edit per accepted candidate`, async () => {
        const prompt = completionUndo.locator('#prompt');
        const before = turnRequests;
        await prompt.pressSequentially('/mod');
        await completionUndo.locator('[role="option"]').first().waitFor();
        await prompt.press('Tab');
        assert.equal(await prompt.inputValue(), '/model ');
        await prompt.press('ControlOrMeta+z');
        assert.equal(await prompt.inputValue(), '/mod');
        await prompt.press('ControlOrMeta+Shift+z');
        assert.equal(await prompt.inputValue(), '/model ');
        await completionUndo.locator('[role="option"]').first().waitFor();
        await completionUndo.locator('[role="option"]').first().click();
        assert.equal(await prompt.inputValue(), '/model "中文 model" ');
        await prompt.press('ControlOrMeta+z');
        assert.equal(await prompt.inputValue(), '/model ');
        await prompt.press('ControlOrMeta+Shift+z');
        assert.equal(await prompt.inputValue(), '/model "中文 model" ');
        assert.equal(turnRequests, before, 'acceptance, undo and redo never submit a turn');
        await focusIs(completionUndo, 'prompt');
        await completionUndo.close();
      });
    }
    const suffixUndo = await createPage('web', true);
    await check('Completion undo preserves the Unicode argument, suffix and draft across Settings', async () => {
      const prompt = suffixUndo.locator('#prompt');
      const before = turnRequests;
      await prompt.pressSequentially('/model 😀中 suffix');
      await prompt.evaluate(node => node.setSelectionRange(10, 10));
      await suffixUndo.locator('[role="option"]').first().waitFor();
      await prompt.press('Tab');
      assert.equal(await prompt.inputValue(), '/model "中文 model" suffix');
      assert.equal(await prompt.evaluate(node => node.selectionStart), 17);
      await prompt.press('ControlOrMeta+z');
      assert.equal(await prompt.inputValue(), '/model 😀中 suffix');
      await suffixUndo.locator('#settings-open').click();
      await suffixUndo.locator('#setting-model').waitFor();
      await suffixUndo.locator('#settings-close').click();
      await suffixUndo.waitForFunction(() => !document.querySelector('#settings-dialog').open);
      assert.equal(await prompt.inputValue(), '/model 😀中 suffix', 'renderer state contains the undone draft');
      await prompt.focus();
      await prompt.press('ControlOrMeta+Shift+z');
      assert.equal(await prompt.inputValue(), '/model "中文 model" suffix');
      assert.equal(turnRequests, before);
      await suffixUndo.close();
    });
    for (const surface of ['desktop', 'web']) {
      await check(`${surface} narrow sheets restore their triggering toggle after Settings closes`, async () => {
        const page = await createPage(surface, true);
        try {
          await page.setViewportSize({ width: 390, height: 844 });
          for (const [toggle, close] of [['navigation-toggle', 'navigation-close'], ['inspector-toggle', 'inspector-close']]) {
            await page.locator(`#${toggle}`).click();
            await focusIs(page, close);
            await page.keyboard.press('ControlOrMeta+,');
            await page.locator('#settings-dialog[open]').waitFor();
            await page.locator('#settings-close').click();
            await page.waitForFunction(() => !document.querySelector('#settings-dialog').open);
            await focusIs(page, toggle);
          }
        } finally { await page.close(); }
      });
    }
    await check('Closing Settings restores the current conversation draft and messages', async () => {
      await conversation.locator('#prompt').fill('Keep this unfinished request while I check the settings.');
      const text = await conversation.locator('#timeline').textContent();
      await conversation.locator('#settings-open').click();
      await conversation.locator('#setting-model').waitFor();
      await conversation.locator('#settings-close').click();
      await conversation.waitForFunction(() => !document.querySelector('#settings-dialog').open);
      assert.equal(await conversation.locator('#prompt').inputValue(), 'Keep this unfinished request while I check the settings.');
      assert.equal(await conversation.locator('#timeline').textContent(), text);
      await screenshot(conversation, 'conversation');
    });
    await check('Saving shared model defaults does not mutate current-session execution settings', async () => {
      const before = sessionSettingWrites;
      assert.equal(await conversation.locator('#model-select').inputValue(), 'default');
      await conversation.locator('#settings-open').click();
      await conversation.locator('#setting-model').waitFor();
      await conversation.locator('#setting-model').fill('new-default-only');
      await conversation.locator('#settings-save').click();
      await conversation.waitForFunction(() => document.querySelector('#settings-status').textContent.startsWith('Saved.'));
      assert.equal(await conversation.locator('#setting-model').inputValue(), 'new-default-only');
      await conversation.locator('#settings-close').click();
      await conversation.waitForFunction(() => !document.querySelector('#settings-dialog').open);
      assert.equal(await conversation.locator('#model-select').inputValue(), 'default');
      assert.equal(sessionSettingWrites, before);
      assert.equal(await conversation.locator('#prompt').inputValue(), 'Keep this unfinished request while I check the settings.');
    });
    await check('A late session preference read cannot overwrite a successfully saved theme', async () => {
      holdNextSettingsRead = true;
      await conversation.locator('.session-select').filter({ hasText: secondSession.title }).click();
      await waitForHeldRead();
      await conversation.locator('#settings-open').click();
      await conversation.getByRole('button', { name: 'Appearance & input', exact: true }).click();
      await conversation.locator('#setting-theme').waitFor();
      await conversation.locator('#setting-theme').selectOption('daybreak');
      await conversation.locator('#settings-save').click();
      await conversation.waitForFunction(() => document.querySelector('#settings-status').textContent.startsWith('Saved.'));
      const lateResponse = conversation.waitForEvent('requestfinished', {
        predicate: request => request.url().includes('/v1/settings?') && request.method() === 'GET',
      });
      releaseReads();
      await lateResponse;
      await conversation.evaluate(() => new Promise(resolve => requestAnimationFrame(() => requestAnimationFrame(resolve))));
      assert.equal(await conversation.locator('html').getAttribute('data-theme'), 'daybreak');
      await conversation.locator('#settings-close').click();
      await conversation.waitForFunction(() => !document.querySelector('#settings-dialog').open);
      await screenshot(conversation, 'conversation-light');
      await conversation.locator('.session-select').filter({ hasText: session.title }).click();
    });
    let nextEventID = 2;
    async function emit(type, data = {}, turnID = 'active-fixture-turn') {
      const event = { id: nextEventID++, type, data, session_id: session.id, turn_id: turnID, engine_sequence: nextEventID };
      assert.ok(streams.size > 0, 'Web SSE stream must be connected');
      for (const [response, owner] of streams) {
        if (owner === session.id) response.write(`data: ${JSON.stringify(event)}\n\n`);
      }
      return event.id;
    }
    await check('A real SSE event preserves expanded history and message node identity', async () => {
      await conversation.locator('details.reasoning summary').click();
      await conversation.evaluate(() => { window.fixtureMessageNode = document.querySelector('#timeline article.assistant'); });
      await emit('turn.accepted', { turn_id: 'active-fixture-turn' });
      await conversation.waitForFunction(() => document.querySelector('#status').textContent === 'running');
      assert.equal(await conversation.locator('details.reasoning').evaluate(node => node.open), true);
      assert.equal(await conversation.evaluate(() => window.fixtureMessageNode === document.querySelector('#timeline article.assistant')), true);
    });
    await check('Unrelated SSE updates preserve question input focus, value and DOM identity', async () => {
      await emit('interaction_requested', { interaction: {
        request_id: 'question-acceptance', turn_id: 'active-fixture-turn', kind: 'question',
        question: { questions: [{ id: 'q-1', header: 'Direction', text: 'What would make the search flow clearer?', multi_select: false, free_text: true, options: [] }] },
      } });
      await conversation.locator('#interaction-body textarea').waitFor();
      await conversation.locator('#interaction-body textarea').fill('Keep my current context.');
      await conversation.evaluate(() => { window.fixtureInputNode = document.querySelector('#interaction-body textarea'); });
      await emit('queue.updated', { revision: 1, items: [{ id: 'queued-fixture', display: 'Run focused tests', state: 'pending', enqueued_at: '2026-09-05T00:00:00Z' }] });
      await conversation.locator('.queued-prompt').waitFor();
      assert.equal(await conversation.evaluate(() => window.fixtureInputNode === document.querySelector('#interaction-body textarea')), true);
      assert.equal(await conversation.locator('#interaction-body textarea').inputValue(), 'Keep my current context.');
      assert.equal(await conversation.evaluate(() => document.activeElement === window.fixtureInputNode), true);
      await screenshot(conversation, 'interaction');
    });
    await check('Streaming durable-ID promotion retains expanded reasoning and focus', async () => {
      await emit('assistant', { message: { reasoning_content: 'A synthetic streaming explanation.' } });
      const row = conversation.locator('#timeline article.assistant').last();
      const summary = row.locator('details.reasoning summary');
      await summary.click();
      await summary.focus();
      await conversation.evaluate(() => {
        window.fixtureStreamRow = document.querySelector('#timeline article.assistant:last-child');
        window.fixtureStreamSummary = window.fixtureStreamRow.querySelector('details.reasoning summary');
      });
      await emit('assistant', { transcript_entry_id: 'durable-stream-fixture', message: {} });
      // The queue event follows promotion on the same ordered SSE connection.
      // Its visible projection is an independent receipt/processing barrier.
      await emit('queue.updated', { revision: 2, items: [{ id: 'promotion-checkpoint', display: 'Promotion checkpoint', state: 'pending', enqueued_at: '2026-10-05T00:00:00Z' }] });
      await conversation.locator('.queued-prompt').filter({ hasText: 'Promotion checkpoint' }).waitFor();
      assert.equal(await row.locator('details.reasoning').evaluate(node => node.open), true);
      assert.equal(await conversation.evaluate(() => window.fixtureStreamRow === document.querySelector('#timeline article.assistant:last-child')), true);
      assert.equal(await conversation.evaluate(() => document.activeElement === window.fixtureStreamSummary), true);
    });
    await check('SSE updates preserve keyboard focus in the session navigation', async () => {
      const selected = conversation.locator('.session-select').filter({ hasText: session.title });
      await selected.focus();
      await emit('queue.updated', { revision: 3, items: [] });
      await conversation.waitForFunction(() => document.querySelector('#queued-prompts').hidden);
      assert.equal(await conversation.evaluate(title => document.activeElement.matches('.session-select') && document.activeElement.textContent.includes(title), session.title), true);
    });
    let taskEventID = 10000;
    for (const surface of ['desktop', 'web']) {
      taskSnapshotMode = 'normal';
      const taskPage = await createPage(surface, true);
      const writesBefore = settingsWrites.length;
      const sessionWritesBefore = sessionSettingWrites;
      const turnsBefore = turnRequests;
      const creationsBefore = creations;
      const emitTask = async () => {
        const event = { id: taskEventID++, session_id: session.id, type: 'task_progress', data: {}, engine_sequence: taskEventID };
        if (surface === 'desktop') await taskPage.evaluate(event => window.__fixtureTaskEvent({ kind: 'event', sessionID: event.session_id, event }), event);
        else for (const [response, owner] of streams) if (owner === session.id) response.write(`data: ${JSON.stringify(event)}\n\n`);
      };
      await check(`${surface} Tasks reads canonical mixed rows with exact decimal generations`, async () => {
        const before = taskReads;
        assert.equal(await taskPage.locator('#inspector-toggle').getAttribute('aria-label'), 'Show activity, tasks and changes');
        await taskPage.locator('#inspector-toggle').click();
        await taskPage.locator('#tasks-tab').click();
        await taskPage.waitForFunction(() => document.querySelectorAll('.task-row').length === 4);
        assert.equal(taskReads, before + 1);
        assert.match(await taskPage.locator('#task-status').textContent(), /18446744073709551615/);
        const executionRows = taskPage.locator('.task-row[data-kind="execution"]');
        assert.equal(await executionRows.count(), 2);
        assert.equal(await executionRows.first().locator('.task-phase').textContent(), 'waiting input');
        await executionRows.first().locator('summary').click();
        await executionRows.last().locator('summary').click();
        assert.match(await executionRows.first().textContent(), /9007199254740992/);
        assert.equal(await executionRows.first().locator('dt').filter({ hasText: /^Link$/ }).count(), 0);
        assert.equal(await executionRows.last().locator('dt').filter({ hasText: /^Link$/ }).count(), 1);
        assert.equal((await taskPage.locator('#task-list').textContent()).includes('/private/synthetic-task-data'), false);
        await screenshot(taskPage, `${surface}-tasks-wide`);
      });
      const inspect = async () => {
        const row = taskPage.locator('.task-row[data-kind="execution"]').last();
        if (!await row.evaluate(node => node.open)) await row.locator('summary').click();
        await row.locator('.task-inspect').click();
      };
      const waitDetail = () => taskPage.waitForFunction(() =>
        !document.querySelector('#refresh-execution-detail').disabled &&
        document.querySelector('#execution-detail-status').textContent.includes('Observed runtime revision'));
      const openAgent = async () => {
        if (!await taskPage.locator('#inspector').isVisible()) await taskPage.locator('#inspector-toggle').click();
        await inspect(); await waitDetail(); await taskPage.locator('#open-agent-conversation').click();
      };
      const waitAgent = () => taskPage.waitForFunction(() => !document.querySelector('#refresh-agent-conversation').disabled &&
        document.querySelector('#agent-conversation-status').textContent.includes('runtime revision'));
      await check(`${surface} exact execution lineage is explicit, path-free and preserves the task context`, async () => {
        const before = detailReads.length;
        await taskPage.locator('#prompt').fill('Keep this exact detail draft');
        await inspect(); await waitDetail();
        assert.equal(detailReads.length, before + 1);
        assert.equal(detailReads.at(-1).includeOutput, false);
        assert.equal(detailReads.at(-1).generation, '9007199254740993');
        assert.match(await taskPage.locator('#execution-detail-identity').textContent(), /9007199254740993/);
        assert.match(await taskPage.locator('#execution-lineage-facts').textContent(), /synthetic-parent-agent/);
        assert.equal((await taskPage.locator('#execution-detail').textContent()).includes('/private/synthetic-detail-path'), false);
        await focusIs(taskPage, 'execution-lineage-tab');
        await screenshot(taskPage, `${surface}-execution-lineage-wide`);
        await taskPage.locator('#back-to-tasks').click();
        assert.equal(await taskPage.locator('.task-row[data-kind="execution"]').last().locator('.task-inspect').evaluate(node => document.activeElement === node), true);
        assert.equal(await taskPage.locator('#prompt').inputValue(), 'Keep this exact detail draft');
      });
      await check(`${surface} Output is lazy, plain text, bounded and independently cached`, async () => {
        await inspect(); await waitDetail(); detailMode = 'long';
        const before = detailReads.length;
        await taskPage.locator('#execution-lineage-tab').press('End'); await waitDetail();
        await focusIs(taskPage, 'execution-output-tab');
        assert.equal(detailReads.length, before + 1);
        assert.equal(detailReads.at(-1).includeOutput, true);
        assert.match(await taskPage.locator('#execution-output').textContent(), /<script>synthetic exact output<\/script>$/);
        assert.equal(await taskPage.locator('#execution-output script').count(), 0);
        assert.ok(await taskPage.locator('#execution-output').evaluate(node => new TextEncoder().encode(node.textContent).length <= 65536));
        assert.match(await taskPage.locator('#execution-output-note').textContent(), /last 64 KiB/);
        await taskPage.locator('#execution-output-tab').press('Home'); await taskPage.locator('#execution-lineage-tab').press('ArrowRight');
        assert.equal(detailReads.length, before + 1, 'tab cache does not poll');
        await screenshot(taskPage, `${surface}-execution-output-wide`);
        await taskPage.locator('#back-to-tasks').click(); detailMode = 'normal';
      });
      await check(`${surface} entering an exact child conversation preserves parent DOM, draft, selection, scroll and native undo`, async () => {
        await taskPage.locator('#prompt').fill('Parent navigation draft');
        await taskPage.locator('#prompt').press('End'); await taskPage.locator('#prompt').pressSequentially(' +keep');
        await taskPage.evaluate(() => {
          window.fixtureParentPrompt = document.querySelector('#prompt');
          window.fixtureParentMessage = document.querySelector('#timeline article.assistant');
          window.fixtureParentMessage.querySelector('details.reasoning').open = true;
          window.fixtureParentPrompt.setSelectionRange(3, 8, 'forward');
          const timeline = document.querySelector('#timeline');
          timeline.style.maxHeight = '40px';
          timeline.scrollTop = Math.min(80, timeline.scrollHeight - timeline.clientHeight);
          window.fixtureParentScroll = timeline.scrollTop;
        });
        assert.ok(await taskPage.evaluate(() => window.fixtureParentScroll > 0), 'parent scroll fixture must actually overflow');
        const before = agentPageReads.length;
        await openAgent(); await waitAgent();
        await focusIs(taskPage, 'back-to-parent-conversation');
        assert.equal(agentPageReads.length, before + 1);
        assert.equal(agentPageReads.at(-1).generation, '9007199254740993');
        assert.equal(agentPageReads.at(-1).threadID, 'synthetic-thread-9007199254740993');
        assert.match(await taskPage.locator('#agent-conversation').textContent(), /Read only/);
        assert.equal(await taskPage.locator('#composer').isVisible(), false);
        assert.equal(await taskPage.locator('#interaction-panel').isVisible(), false);
        assert.equal(await taskPage.locator('#agent-conversation script').count(), 0);
        assert.equal((await taskPage.locator('#agent-conversation').textContent()).includes('/private/synthetic-page-path'), false);
        assert.deepEqual(await taskPage.locator('#agent-conversation-timeline article').first().evaluate(card =>
          [...card.children].slice(1, 4).map(node => node.textContent.trim())),
        ['Exact child conversationRead-only synthetic child evidence.<script>not executable</script>', 'image · image/png · 10 bytes', 'After media']);
        await screenshot(taskPage, `${surface}-agent-conversation-wide`);
        await taskPage.locator('#back-to-parent-conversation').click();
        await focusIs(taskPage, 'prompt');
        assert.deepEqual(await taskPage.locator('#prompt').evaluate(input => [input === window.fixtureParentPrompt, input.value,
          input.selectionStart, input.selectionEnd, input.selectionDirection]), [true, 'Parent navigation draft +keep', 3, 8, 'forward']);
        assert.equal(await taskPage.evaluate(() => document.querySelector('#timeline article.assistant') === window.fixtureParentMessage &&
          window.fixtureParentMessage.querySelector('details.reasoning').open), true);
        await taskPage.waitForFunction(() => document.querySelector('#timeline').scrollTop === window.fixtureParentScroll);
        await taskPage.evaluate(() => document.querySelector('#timeline').style.removeProperty('max-height'));
        await taskPage.keyboard.press('ControlOrMeta+z');
        assert.equal(await taskPage.locator('#prompt').inputValue(), 'Parent navigation draft');
      });
      await check(`${surface} completed and evicted conversations remain explicitly read-only`, async () => {
        for (const mode of ['replay_only', 'evicted_transcript']) {
          agentPageMode = mode; await openAgent(); await waitAgent();
          assert.match(await taskPage.locator('#agent-conversation-status').textContent(),
            mode === 'replay_only' ? /Completed execution history/ : /Evicted thread history/);
          assert.equal(await taskPage.locator('#composer').isVisible(), false);
          assert.match(await taskPage.locator('#agent-conversation').textContent(), /Read only/);
          await taskPage.keyboard.press('Escape'); await focusIs(taskPage, 'prompt');
        }
        agentPageMode = 'live_attach';
      });
      await check(`${surface} child history paging is explicit, ordered and snapshot-only through activity and errors`, async () => {
        await openAgent(); await waitAgent();
        const before = agentPageReads.length;
        await taskPage.locator('#agent-conversation-timeline details.reasoning summary').click();
        await taskPage.locator('#agent-conversation-older').click(); await waitAgent();
        assert.equal(agentPageReads.length, before + 1);
        assert.equal(agentPageReads.at(-1).cursor, 'synthetic-older-cursor');
        assert.deepEqual(await taskPage.locator('#agent-conversation-timeline article').evaluateAll(nodes => nodes.map(node => node.dataset.messageId)),
          ['agent-older-1', 'agent-older-2', 'agent-newest']);
        assert.equal(await taskPage.locator('#agent-conversation-timeline details.reasoning').last().evaluate(node => node.open), true);
        assert.equal(await taskPage.locator('#agent-conversation-older').isVisible(), false);
        await emitTask();
        await taskPage.waitForFunction(() => document.querySelector('#agent-conversation-status').textContent.includes('Session activity changed'));
        assert.equal(agentPageReads.length, before + 1);
        failNextAgentPage = true; await taskPage.locator('#refresh-agent-conversation').click();
        await taskPage.waitForFunction(() => document.querySelector('#agent-conversation-status').textContent.includes('exact conversation changed'));
        assert.equal(await taskPage.locator('#agent-conversation-timeline article').count(), 3);
        assert.equal((await taskPage.locator('#agent-conversation').textContent()).includes('/private/synthetic-page-error'), false);
        await taskPage.keyboard.press('Escape'); await focusIs(taskPage, 'prompt');
      });
      await check(`${surface} leaving or switching parent sessions retires pending child content without persistence`, async () => {
        holdNextAgentPage = true; await openAgent(); await waitForHeldRead(heldAgentPages);
        const held = heldAgentPages.shift(); await taskPage.locator('#back-to-parent-conversation').click();
        reply(held.res, held.body); await openAgent(); await waitAgent();
        assert.equal(await taskPage.locator('#agent-conversation-timeline article').count(), 1);
        holdNextAgentPage = true; await taskPage.locator('#refresh-agent-conversation').click(); await waitForHeldRead(heldAgentPages);
        const switched = heldAgentPages.shift();
        await taskPage.locator('.session-select').filter({ hasText: secondSession.title }).click();
        await taskPage.waitForFunction(id => document.querySelector('.session-row.active')?.dataset.sessionId === id, secondSession.id);
        reply(switched.res, switched.body);
        assert.equal(await taskPage.locator('#agent-conversation').isVisible(), false);
        assert.equal(await taskPage.locator('#agent-conversation-timeline').textContent(), '');
        await taskPage.locator('.session-select').filter({ hasText: session.title }).click();
        await taskPage.waitForFunction(id => document.querySelector('.session-row.active')?.dataset.sessionId === id &&
          !document.querySelector('#refresh-tasks').disabled, session.id);
        const saved = await taskPage.evaluate(() => JSON.stringify({ ...localStorage }));
        assert.equal(saved.includes('synthetic-older-cursor'), false);
        assert.equal(saved.includes('synthetic child evidence'), false);
        await taskPage.locator('#inspector-toggle').click();
      });
      await check(`${surface} detail activity and failed refresh keep only exact cached facts without polling`, async () => {
        await inspect(); await waitDetail();
        const before = detailReads.length;
        await emitTask();
        await taskPage.waitForFunction(() => document.querySelector('#execution-detail-status').textContent.includes('Session activity changed'));
        assert.equal(detailReads.length, before);
        failNextDetail = true;
        await taskPage.locator('#refresh-execution-detail').click();
        await taskPage.waitForFunction(() => document.querySelector('#execution-detail-status').textContent.includes('no longer current'));
        assert.match(await taskPage.locator('#execution-lineage-facts').textContent(), /synthetic-parent-agent/);
        assert.equal((await taskPage.locator('#execution-detail').textContent()).includes('/private/synthetic-detail-path'), false);
        await taskPage.locator('#back-to-tasks').click();
      });
      await check(`${surface} tab switches, Back and list refresh retire late exact-detail responses`, async () => {
        holdNextDetail = true; await inspect(); await waitForHeldRead(heldDetails);
        const lineage = heldDetails.shift();
        await taskPage.locator('#execution-output-tab').click(); await waitDetail();
        reply(lineage.res, { ...lineage.body, metadata: { name: 'OLD LINEAGE RESPONSE' } });
        // A completed refresh is a processing barrier following the old reply.
        await taskPage.locator('#refresh-execution-detail').click(); await waitDetail();
        assert.equal((await taskPage.locator('#execution-detail').textContent()).includes('OLD LINEAGE RESPONSE'), false);
        holdNextDetail = true; await taskPage.locator('#refresh-execution-detail').click(); await waitForHeldRead(heldDetails);
        const back = heldDetails.shift(); await taskPage.locator('#back-to-tasks').click(); reply(back.res, back.body);
        await taskPage.locator('#refresh-tasks').click();
        await taskPage.waitForFunction(() => !document.querySelector('#refresh-tasks').disabled);
        assert.equal(await taskPage.locator('#execution-detail').isVisible(), false);
      });
      await check(`${surface} mismatched lineage and live empty output do not borrow another generation`, async () => {
        detailMode = 'wrong-lineage'; await inspect();
        await taskPage.waitForFunction(() => document.querySelector('#execution-detail-status').textContent.includes('response is invalid'));
        assert.equal(await taskPage.locator('#execution-lineage-facts').textContent(), '');
        await taskPage.locator('#back-to-tasks').click(); detailMode = 'live';
        await inspect(); await waitDetail(); await taskPage.locator('#execution-output-tab').click(); await waitDetail();
        assert.equal(await taskPage.locator('#execution-output').textContent(), '');
        assert.match(await taskPage.locator('#execution-output-note').textContent(), /current terminal generation/);
        await taskPage.locator('#back-to-tasks').click(); detailMode = 'normal';
      });
      await check(`${surface} session switch retires a held detail response without attaching or reopening it`, async () => {
        holdNextDetail = true; await inspect(); await waitForHeldRead(heldDetails);
        const old = heldDetails.shift();
        await taskPage.locator('.session-select').filter({ hasText: secondSession.title }).click();
        await taskPage.waitForFunction(id => document.querySelector('.session-row.active')?.dataset.sessionId === id, secondSession.id);
        reply(old.res, { ...old.body, metadata: { name: 'OLD SESSION DETAIL RESPONSE' } });
        await taskPage.waitForFunction(() => !document.querySelector('#refresh-tasks').disabled);
        assert.equal(await taskPage.locator('#execution-detail').isVisible(), false);
        assert.equal(await taskPage.locator('#execution-lineage-facts').textContent(), '');
        await taskPage.locator('.session-select').filter({ hasText: session.title }).click();
        await taskPage.waitForFunction(id => document.querySelector('.session-row.active')?.dataset.sessionId === id && !document.querySelector('#refresh-tasks').disabled, session.id);
        assert.equal(await taskPage.locator('#execution-detail').isVisible(), false);
      });
      await check(`${surface} task filters are local and all inspector tabs support roving keyboard focus`, async () => {
        const before = taskReads;
        await taskPage.locator('#task-kind').selectOption('execution');
        await taskPage.locator('#task-search').fill('Builder');
        assert.equal(await taskPage.locator('.task-row').count(), 2);
        await taskPage.locator('#task-search').fill('unmatched');
        assert.equal(await taskPage.locator('.task-row').count(), 0);
        assert.match(await taskPage.locator('#task-list').textContent(), /No tasks match/);
        assert.equal(taskReads, before, 'search/filter cannot perform a server read');
        await taskPage.locator('#task-search').fill('');
        await taskPage.locator('#task-kind').selectOption('all');
        await taskPage.locator('#tasks-tab').focus();
        await taskPage.keyboard.press('End'); await focusIs(taskPage, 'review-tab');
        await taskPage.keyboard.press('Home'); await focusIs(taskPage, 'activity-tab');
        await taskPage.keyboard.press('ArrowRight'); await focusIs(taskPage, 'tasks-tab');
        await taskPage.waitForFunction(() => !document.querySelector('#refresh-tasks').disabled);
        assert.deepEqual(await taskPage.locator('.inspector-tab').evaluateAll(nodes => nodes.map(node => node.tabIndex)), [-1, 0, -1]);
      });
      await check(`${surface} task refresh and reorder preserve the exact expanded row and focus`, async () => {
        const first = taskPage.locator('.task-row[data-kind="execution"]').first();
        if (!await first.evaluate(node => node.open)) await first.locator('summary').click();
        await first.evaluate(node => { window.__taskRow = node; window.__taskSummary = node.firstElementChild; });
        taskSnapshotMode = 'reordered'; holdNextTasks = true;
        await taskPage.locator('#refresh-tasks').click(); await waitForHeldRead(heldTasks);
        await taskPage.evaluate(() => window.__taskSummary.focus());
        assert.equal(await taskPage.locator('.task-row').count(), 4, 'refresh retains previous snapshot');
        const held = heldTasks.shift(); reply(held.res, held.body);
        await taskPage.waitForFunction(() => !document.querySelector('#refresh-tasks').disabled);
        assert.equal(await taskPage.evaluate(() => window.__taskRow.isConnected && window.__taskRow.open), true);
        assert.equal(await taskPage.evaluate(() => document.activeElement === window.__taskSummary), true);
        assert.equal(await taskPage.locator('.task-row[data-kind="execution"]').last().evaluate(node => node === window.__taskRow), true);
      });
      await check(`${surface} task activity during a read stays visibly stale without automatic polling or draft loss`, async () => {
        await taskPage.locator('#prompt').fill('Keep this task draft');
        holdNextTasks = true;
        await taskPage.locator('#refresh-tasks').click(); await waitForHeldRead(heldTasks);
        const before = taskReads;
        await emitTask();
        await taskPage.waitForFunction(() => document.querySelector('#task-status').textContent.includes('Session activity changed'));
        const held = heldTasks.shift(); reply(held.res, held.body);
        await taskPage.waitForFunction(() => !document.querySelector('#refresh-tasks').disabled);
        assert.match(await taskPage.locator('#task-status').textContent(), /Session activity changed/);
        assert.equal(taskReads, before);
        assert.equal(await taskPage.locator('#prompt').inputValue(), 'Keep this task draft');
      });
      await check(`${surface} partial and failed task reads retain honest diagnostics and accepted data`, async () => {
        taskSnapshotMode = 'partial';
        await taskPage.locator('#refresh-tasks').click();
        await taskPage.waitForFunction(() => document.querySelector('#task-status').textContent.includes('workboard_unavailable'));
        assert.equal(await taskPage.locator('.task-row').count(), 2);
        assert.match(await taskPage.locator('#task-status').textContent(), /execution other: 2/);
        failNextTasks = true;
        await taskPage.locator('#refresh-tasks').click();
        await taskPage.waitForFunction(() => document.querySelector('#task-status').textContent.includes('Task read failed'));
        assert.equal(await taskPage.locator('.task-row').count(), 2);
        assert.equal(await taskPage.locator('#workbench-notice').isVisible(), false, 'failure remains panel-local');
        taskSnapshotMode = 'empty';
        holdNextTasks = true;
        await taskPage.locator('#refresh-tasks').click();
        await waitForHeldRead(heldTasks);
        await taskPage.locator('.task-row summary').first().focus();
        const held = heldTasks.shift(); reply(held.res, held.body);
        await taskPage.waitForFunction(() => document.querySelector('#task-list').textContent.includes('No tasks in this snapshot'));
        assert.equal(await taskPage.locator('.task-row').count(), 0);
        await focusIs(taskPage, 'refresh-tasks');
      });
      await check(`${surface} a late task response remains with its session and narrow Tasks stays usable`, async () => {
        taskSnapshotMode = 'normal'; holdNextTasks = true;
        await taskPage.locator('#refresh-tasks').click(); await waitForHeldRead(heldTasks);
        await taskPage.locator('.session-select').filter({ hasText: secondSession.title }).click();
        await taskPage.waitForFunction(id => document.querySelector('.session-row.active')?.dataset.sessionId === id, secondSession.id);
        const held = heldTasks.shift(); reply(held.res, { ...held.body, work_items: [{ ...held.body.work_items[0], title: 'OLD SESSION RESPONSE' }] });
        await taskPage.waitForFunction(() => document.querySelectorAll('.task-row').length === 4);
        assert.equal((await taskPage.locator('#task-list').textContent()).includes('OLD SESSION RESPONSE'), false);
        await taskPage.locator('#inspector-toggle').click();
        await taskPage.setViewportSize({ width: 360, height: 800 });
        await taskPage.locator('#inspector-toggle').click();
        assert.equal(await taskPage.locator('#inspector').getAttribute('aria-modal'), 'true');
        assert.equal(await taskPage.locator('#conversation').evaluate(node => node.inert), true);
        assert.equal(await taskPage.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth), true);
        await screenshot(taskPage, `${surface}-tasks-narrow`);
        await inspect(); await waitDetail();
        await taskPage.locator('#execution-output-tab').click(); await waitDetail();
        assert.equal(await taskPage.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth), true);
        await screenshot(taskPage, `${surface}-execution-output-narrow`);
        await taskPage.locator('#execution-output-tab').press('Home');
        await taskPage.locator('#open-agent-conversation').click(); await waitAgent();
        assert.equal(await taskPage.locator('#conversation').evaluate(node => node.inert), false);
        assert.equal(await taskPage.locator('#inspector').isVisible(), false);
        assert.equal(await taskPage.evaluate(() => document.documentElement.scrollWidth <= innerWidth), true);
        await screenshot(taskPage, `${surface}-agent-conversation-narrow`);
        await taskPage.keyboard.press('Escape'); await focusIs(taskPage, 'prompt');
        await taskPage.locator('#inspector-toggle').click(); await inspect(); await waitDetail();
        await taskPage.locator('#execution-output-tab').click(); await waitDetail();
        await taskPage.keyboard.press('Escape');
        assert.equal(await taskPage.locator('#inspector').getAttribute('aria-modal'), 'true', 'first Escape returns to Tasks');
        assert.equal(await taskPage.locator('.task-row[data-kind="execution"]').last().locator('.task-inspect').evaluate(node => document.activeElement === node), true);
        assert.equal(await taskPage.locator('#execution-output').textContent(), '', 'retired output is cleared, not just hidden');
        await taskPage.keyboard.press('Escape'); await focusIs(taskPage, 'inspector-toggle');
        const saved = await taskPage.evaluate(() => JSON.stringify({ ...localStorage }));
        assert.equal(saved.includes('18446744073709551615'), false, 'task cache is not persisted');
        assert.equal(saved.includes('/private/synthetic-task-data'), false);
        assert.equal(saved.includes('synthetic exact output'), false);
        assert.equal(settingsWrites.length, writesBefore);
        assert.equal(sessionSettingWrites, sessionWritesBefore);
        assert.equal(turnRequests, turnsBefore);
        assert.equal(creations, creationsBefore);
      });
      holdNextTasks = false; failNextTasks = false; releaseHeld(heldTasks);
      if (surface === 'desktop') await check('Desktop backend loss retires pending task reads and controls', async () => {
        await taskPage.setViewportSize({ width: 1440, height: 1000 });
        await openAgent(); await waitAgent();
        holdNextAgentPage = true;
        await taskPage.locator('#refresh-agent-conversation').click(); await waitForHeldRead(heldAgentPages);
        await taskPage.locator('#inspector-toggle').click();
        holdNextTasks = true;
        await taskPage.locator('#refresh-tasks').click(); await waitForHeldRead(heldTasks);
        holdNextDetail = true; await inspect(); await waitForHeldRead(heldDetails);
        await taskPage.evaluate(() => window.__fixtureBackendExit({}));
        const held = heldTasks.shift(); reply(held.res, held.body);
        const detail = heldDetails.shift(); reply(detail.res, detail.body);
        const conversation = heldAgentPages.shift(); reply(conversation.res, conversation.body);
        await taskPage.waitForFunction(() => document.querySelector('#task-status').textContent.includes('Backend stopped'));
        assert.equal(await taskPage.locator('#refresh-tasks').isDisabled(), true);
        assert.equal(await taskPage.locator('.task-row').count(), 0);
        assert.equal(await taskPage.locator('#execution-detail').isVisible(), false);
        assert.equal(await taskPage.locator('#execution-lineage-facts').textContent(), '');
        assert.equal(await taskPage.locator('#agent-conversation').isVisible(), false);
        assert.equal(await taskPage.locator('#agent-conversation-timeline').textContent(), '');
      });
      holdNextTasks = false; failNextTasks = false; releaseHeld(heldTasks);
      holdNextDetail = false; failNextDetail = false; releaseHeld(heldDetails);
      holdNextAgentPage = false; failNextAgentPage = false; releaseHeld(heldAgentPages);
      await taskPage.context().close();
    }
    taskSnapshotMode = 'normal';
    assert.deepEqual(errors, [], 'Renderer emitted no unhandled page errors');
  } catch (error) {
    results.push({ name: 'Acceptance runner', status: 'fail', error: error.message });
  } finally {
    try {
      await browser?.close();
    } catch (error) {
      results.push({ name: 'Browser cleanup', status: 'fail', error: error.message });
    }
    try {
      for (const response of streams.keys()) response.end();
      releaseReads();
      server.closeAllConnections();
      await new Promise(resolve => server.close(resolve));
    } catch (error) {
      results.push({ name: 'Fixture cleanup', status: 'fail', error: error.message });
    }
    const report = {
      boundary: 'Isolated Chromium renderer with controlled HTTP/API fixtures; actual browser SSE parser; no Electron native UI or live-provider validation.',
      outputDir, results, errors,
    };
    await fs.writeFile(path.join(outputDir, 'results.json'), JSON.stringify(report, null, 2) + '\n');
    const passed = results.filter(result => result.status === 'pass').length;
    console.log(`${passed}/${results.length} UI checks passed; artifacts: ${outputDir}`);
    if (passed !== results.length) process.exitCode = 1;
  }
}
main().catch(error => { console.error(error); process.exitCode = 1; });
