const SESSION_ID_PATTERN = /^[A-Za-z0-9][A-Za-z0-9._-]{0,255}$/;
const UUID_PATTERN = /^[0-9A-Fa-f]{8}-[0-9A-Fa-f]{4}-[0-9A-Fa-f]{4}-[0-9A-Fa-f]{4}-[0-9A-Fa-f]{12}$/;
const ATTACH_TURN_FIELDS = new Set(['sessionID', 'prompt', 'clientTurnID']);
const QUEUE_PROMPT_FIELDS = new Set(['sessionID', 'prompt', 'clientQueueID']);
const DURABLE_IMPORT_FIELDS = new Set(['sessionID', 'confirmLegacyStopped']);
const NEW_SESSION_FIELDS = new Set(['workspaceHandle']);

const EXECUTION_FIELDS = new Set([
  'model',
  'reasoning_effort',
  'permission_mode',
]);

function requireSessionID(value) {
  if (typeof value !== 'string' || !SESSION_ID_PATTERN.test(value)) {
    throw new TypeError('valid session id required');
  }
  return value;
}

function optionalSessionID(value) {
  return value === undefined || value === null || value === ''
    ? undefined
    : requireSessionID(value);
}

function requireWorkspaceHandle(value) {
  if (typeof value !== 'string' || value.length === 0 || value.length > 512) {
    throw new TypeError('valid workspace handle required');
  }
  return value;
}

function requireNewSession(payload) {
  if (payload === null || typeof payload !== 'object') {
    throw new TypeError('new session payload required');
  }
  for (const field of Object.keys(payload)) {
    if (!NEW_SESSION_FIELDS.has(field)) {
      throw new TypeError('unsupported new session field');
    }
  }
  return { workspace_handle: requireWorkspaceHandle(payload.workspaceHandle) };
}

function requireAttachTurn(payload) {
  if (payload === null || typeof payload !== 'object') {
    throw new TypeError('attach turn payload required');
  }
  for (const field of Object.keys(payload)) {
    if (!ATTACH_TURN_FIELDS.has(field)) {
      throw new TypeError('unsupported attach turn field');
    }
  }
  requireSessionID(payload.sessionID);
  const clientTurnID = requireUUID(payload.clientTurnID, 'client turn id');
  return {
    prompt: requirePrompt(payload.prompt),
    client_turn_id: clientTurnID,
  };
}

function requireUUID(value, label) {
  if (typeof value !== 'string' || !UUID_PATTERN.test(value)) {
    throw new TypeError(`${label} must be a UUID`);
  }
  return value.toLowerCase();
}

function requirePrompt(value) {
  if (typeof value !== 'string') {
    throw new TypeError('prompt must be a string');
  }
  const prompt = value.trim();
  if (prompt === '') {
    throw new TypeError('prompt is required');
  }
  for (let index = 0; index < prompt.length; index += 1) {
    const code = prompt.charCodeAt(index);
    if (code >= 0xD800 && code <= 0xDBFF) {
      if (index + 1 >= prompt.length || prompt.charCodeAt(index + 1) < 0xDC00 || prompt.charCodeAt(index + 1) > 0xDFFF) {
        throw new TypeError('prompt must be valid UTF-8');
      }
      index += 1;
    } else if (code >= 0xDC00 && code <= 0xDFFF) {
      throw new TypeError('prompt must be valid UTF-8');
    }
  }
  if (new TextEncoder().encode(prompt).byteLength > 262144) {
    throw new TypeError('prompt exceeds 262144 bytes');
  }
  return prompt;
}

function requireQueuePrompt(payload) {
  if (payload === null || typeof payload !== 'object') {
    throw new TypeError('queue prompt payload required');
  }
  for (const field of Object.keys(payload)) {
    if (!QUEUE_PROMPT_FIELDS.has(field)) {
      throw new TypeError('unsupported queue prompt field');
    }
  }
  requireSessionID(payload.sessionID);
  return {
    prompt: requirePrompt(payload.prompt),
    client_queue_id: requireUUID(payload.clientQueueID, 'client queue id'),
  };
}

function requireDurableImport(payload) {
  if (payload === null || typeof payload !== 'object') {
    throw new TypeError('durable import payload required');
  }
  for (const field of Object.keys(payload)) {
    if (!DURABLE_IMPORT_FIELDS.has(field)) {
      throw new TypeError('unsupported durable import field');
    }
  }
  requireSessionID(payload.sessionID);
  if (payload.confirmLegacyStopped !== true) {
    throw new TypeError('stopped producer attestation required');
  }
  return { confirm_legacy_stopped: true };
}

function requireRequestID(value) {
  if (typeof value !== 'string' || value.length === 0 || value.length > 512) {
    throw new TypeError('valid interaction request id required');
  }
  return value;
}

function executionUpdate(payload) {
  if (!EXECUTION_FIELDS.has(payload.field)) {
    throw new TypeError('supported execution setting required');
  }
  if (typeof payload.value !== 'string') {
    throw new TypeError('execution setting value must be a string');
  }
  return { [payload.field]: payload.value };
}

function desktopOperation(operation, payload = {}) {
	if (operation === 'getPermissionSettings' || operation === 'updatePermissionSettings') {
		const update = operation === 'updatePermissionSettings';
		if (!payload || typeof payload !== 'object' || Array.isArray(payload) || Object.keys(payload).some(key => !['scope', 'sessionID', ...(update ? ['revision', 'changes'] : [])].includes(key))) throw new TypeError('invalid permission settings payload');
		const scope = payload.scope ?? 'user';
		if (!['user', 'project', 'local'].includes(scope) || (scope !== 'user' && !payload.sessionID)) throw new TypeError('invalid permission settings scope');
		const query = new URLSearchParams({scope});
		if (payload.sessionID !== undefined) query.set('session_id', requireSessionID(payload.sessionID));
		if (update && (typeof payload.revision !== 'string' || !/^[a-f0-9]{64}$/i.test(payload.revision) || !Array.isArray(payload.changes) || !payload.changes.length || payload.changes.length > 32 || payload.changes.some(change => !change || typeof change !== 'object' || Array.isArray(change) || Object.keys(change).some(key => !['operation', 'action', 'rule'].includes(key)) || !['add', 'remove'].includes(change.operation) || !['allow', 'deny', 'ask'].includes(change.action) || typeof change.rule !== 'string' || !change.rule.isWellFormed() || !change.rule.trim() || change.rule.includes('\0') || [...change.rule].length > 4096))) throw new TypeError('invalid permission rule changes');
		return [`/v1/permission-settings?${query}`, update ? 'PATCH' : 'GET', update ? {revision: payload.revision, changes: payload.changes.map(({operation, action, rule}) => ({operation, action, rule}))} : undefined];
	}
  if (operation === 'getSettings' || operation === 'updateSettings') {
    const scope = payload.scope || 'user';
    if (!['user', 'project'].includes(scope)) throw new TypeError('invalid settings scope');
    const query = new URLSearchParams({ scope });
    if (payload.sessionID) query.set('session_id', requireSessionID(payload.sessionID));
    return [`/v1/settings?${query}`, operation === 'getSettings' ? 'GET' : 'PATCH',
      operation === 'getSettings' ? undefined : { revision: payload.revision, changes: payload.changes }];
  }
  const id = () => encodeURIComponent(requireSessionID(payload.sessionID));
  switch (operation) {
    case 'getGoal':
      if (!payload || typeof payload !== 'object' || Array.isArray(payload) || Object.keys(payload).some(key => key !== 'sessionID')) throw new TypeError('invalid goal payload');
      return [`/v1/sessions/${id()}/goal`, 'GET'];
    case 'controlGoal':
      return [`/v1/sessions/${id()}/goal/control`, 'POST', goalControlPayload(payload)];
    case 'getMCPInventory':
      if (!payload || typeof payload !== 'object' || Array.isArray(payload) || Object.keys(payload).some(key => key !== 'sessionID')) throw new TypeError('MCP inventory requires only a session id');
      return [`/v1/sessions/${id()}/mcp-inventory`, 'GET'];
    case 'health':
      return ['/v1/health', 'GET'];
    case 'listSessions':
      return ['/v1/sessions', 'GET'];
    case 'listDurableSessions': {
      const query = new URLSearchParams();
      if (payload.cursor) query.set('cursor', payload.cursor);
      if (payload.limit) query.set('limit', String(payload.limit));
      if (payload.search) query.set('search', payload.search);
      const suffix = query.size ? `?${query}` : '';
      return [`/v1/durable-sessions${suffix}`, 'GET'];
    }
    case 'createSession':
      return ['/v1/sessions', 'POST', requireNewSession(payload)];
    // This operation is intentionally not available through app:api. Electron
    // main calls it only after a native directory picker returns a local path.
    case 'createWorkspace':
      if (typeof payload.cwd !== 'string' || payload.cwd.length === 0) {
        throw new TypeError('workspace path required');
      }
      return ['/v1/workspaces', 'POST', { cwd: payload.cwd }];
    case 'getSession':
      return [`/v1/sessions/${id()}`, 'GET'];
    case 'closeSession':
      return [`/v1/sessions/${id()}`, 'DELETE'];
    case 'snapshot':
      return [`/v1/sessions/${id()}/snapshot`, 'GET'];
    case 'getTaskExplorer':
      if (!payload || typeof payload !== 'object' || Array.isArray(payload) ||
          Object.keys(payload).some((key) => key !== 'sessionID')) {
        throw new TypeError('task overview requires only a session id');
      }
      return [`/v1/sessions/${id()}/tasks`, 'GET'];
    case 'getAgentTranscript':
    case 'getAgentExecutionDetail': {
      const paging = operation === 'getAgentTranscript';
      const fields = new Set(['sessionID', 'agentID', 'generation', 'executionSessionID', 'threadID', ...(paging ? ['cursor', 'limit'] : ['includeOutput'])]);
      const validID = value => typeof value === 'string' && value.length > 0 && value.trim() === value &&
        [...value].length <= 512 && !/\p{Cc}/u.test(value) && value.isWellFormed();
      if (!payload || typeof payload !== 'object' || Array.isArray(payload) ||
          Object.keys(payload).some(key => !fields.has(key)) ||
          ![payload.agentID, payload.executionSessionID, payload.threadID].every(validID) ||
          typeof payload.generation !== 'string' || !/^[1-9][0-9]{0,18}$/.test(payload.generation) ||
          BigInt(payload.generation) > 9223372036854775807n ||
          (payload.includeOutput !== undefined && typeof payload.includeOutput !== 'boolean') ||
          (paging && payload.cursor !== undefined && (typeof payload.cursor !== 'string' || !/^[!-~]{1,128}$/.test(payload.cursor))) ||
          (paging && payload.limit !== undefined && (!Number.isInteger(payload.limit) || payload.limit < 1 || payload.limit > 32))) {
        throw new TypeError('Exact execution identity required.');
      }
      const query = new URLSearchParams({ agent_id: payload.agentID, generation: payload.generation,
        execution_session_id: payload.executionSessionID, thread_id: payload.threadID });
      if (paging) {
        if (payload.cursor !== undefined) query.set('cursor', payload.cursor);
        query.set('limit', String(payload.limit ?? 32));
        return [`/v1/sessions/${id()}/agent-transcript?${query}`, 'GET'];
      }
      query.set('include_output', String(payload.includeOutput === true));
      return [`/v1/sessions/${id()}/agent-execution-detail?${query}`, 'GET'];
    }
    case 'transcriptPage': {
      const query = new URLSearchParams();
      if (payload.cursor) query.set('cursor', payload.cursor);
      if (payload.limit) query.set('limit', String(payload.limit));
      const suffix = query.size ? `?${query}` : '';
      return [`/v1/sessions/${id()}/transcript${suffix}`, 'GET'];
    }
    case 'durableTranscriptPage': {
      const query = new URLSearchParams();
      if (payload.cursor) query.set('cursor', payload.cursor);
      if (payload.limit) query.set('limit', String(payload.limit));
      const suffix = query.size ? `?${query}` : '';
      return [`/v1/durable-sessions/${id()}/transcript${suffix}`, 'GET'];
    }
    case 'reviewDiff': {
      const query = new URLSearchParams({
        ignore_whitespace: String(Boolean(payload.ignoreWhitespace)),
      });
      return [`/v1/sessions/${id()}/review-diff?${query}`, 'GET'];
    }
    case 'startTurn':
      return [`/v1/sessions/${id()}/turns`, 'POST', {
        prompt: payload.prompt,
        client_turn_id: payload.clientTurnID,
      }];
    case 'listQueuedPrompts':
      return [`/v1/sessions/${id()}/queued-prompts`, 'GET'];
    case 'queuePrompt':
      return [
        `/v1/sessions/${id()}/queued-prompts`,
        'POST',
        requireQueuePrompt(payload),
      ];
    case 'cancelQueuedPrompt': {
      const queueID = encodeURIComponent(requireUUID(payload.queueID, 'queue id'));
      return [`/v1/sessions/${id()}/queued-prompts/${queueID}`, 'DELETE'];
    }
    case 'attachTurn':
      return [
        `/v1/durable-sessions/${id()}/attach-turn`,
        'POST',
        requireAttachTurn(payload),
      ];
    case 'importDurableSession':
      return [
        `/v1/durable-sessions/${id()}/import`,
        'POST',
        requireDurableImport(payload),
      ];
    case 'cancelTurn':
      return [`/v1/sessions/${id()}/cancel`, 'POST', {
        turn_id: payload.turnID,
        mode: payload.mode,
        reason: payload.reason,
      }];
    case 'resolveInteraction': {
      const requestID = encodeURIComponent(requireRequestID(payload.requestID));
      return [`/v1/sessions/${id()}/interactions/${requestID}/resolve`, 'POST', payload.result];
    }
    case 'getInteractionPlan':
      return [`/v1/sessions/${id()}/interactions/${encodeURIComponent(requireRequestID(payload.requestID))}/plan`, 'GET'];
    case 'getExecutionSettings':
      return [`/v1/sessions/${id()}/execution-settings`, 'GET'];
    case 'completeCommand':
      return [`/v1/sessions/${id()}/command-completions`, 'POST', commandCompletionInput(payload)];
    case 'promptSuggestion':
      if (Object.keys(payload).some(key => key !== 'sessionID')) throw new TypeError('suggestion accepts only a session id');
      return [`/v1/sessions/${id()}/prompt-suggestion`, 'POST', {}];
    case 'updateExecutionSetting':
      return [
        `/v1/sessions/${id()}/execution-settings`,
        'PATCH',
        executionUpdate(payload),
      ];
    default:
      throw new TypeError('unsupported desktop API operation');
  }
}

function goalControlPayload(payload) {
  const allowed = new Set(['sessionID', 'operation', 'expected_goal_id', 'expected_revision', 'objective', 'token_budget']);
  if (!payload || typeof payload !== 'object' || Array.isArray(payload) || Object.keys(payload).some(key => !allowed.has(key)) || !['create', 'edit', 'pause', 'resume', 'budget', 'clear', 'continue'].includes(payload.operation) || typeof payload.expected_goal_id !== 'string' || typeof payload.expected_revision !== 'string' || !/^(0|[1-9][0-9]*)$/.test(payload.expected_revision) || (payload.objective !== undefined && (typeof payload.objective !== 'string' || !payload.objective.isWellFormed() || payload.objective.includes('\0') || [...payload.objective].length > 4000)) || (payload.token_budget !== undefined && (typeof payload.token_budget !== 'string' || !/^[1-9][0-9]*$/.test(payload.token_budget)))) throw new TypeError('invalid goal payload');
  if (payload.expected_goal_id.length > 256 || !payload.expected_goal_id.isWellFormed() || payload.expected_goal_id.includes('\0') || (payload.operation === 'create' && (payload.expected_goal_id !== '' || payload.expected_revision !== '0' || typeof payload.objective !== 'string')) || (payload.operation === 'edit' && (typeof payload.objective !== 'string' || payload.token_budget !== undefined)) || (payload.operation === 'budget' && (typeof payload.token_budget !== 'string' || payload.objective !== undefined)) || (!['create', 'edit', 'budget'].includes(payload.operation) && (payload.objective !== undefined || payload.token_budget !== undefined))) throw new TypeError('invalid goal payload');
  if (payload.expected_revision.length > 20 || BigInt(payload.expected_revision) > 18446744073709551615n || (payload.token_budget !== undefined && (payload.token_budget.length > 20 || BigInt(payload.token_budget) > 18446744073709551615n)) || (payload.operation !== 'create' && (!payload.expected_goal_id || payload.expected_revision === '0')) || (['create', 'edit'].includes(payload.operation) && !payload.objective.trim())) throw new TypeError('invalid goal payload');
  requireSessionID(payload.sessionID);
  const result = { operation: payload.operation, expected_goal_id: payload.expected_goal_id, expected_revision: payload.expected_revision };
  if (payload.objective !== undefined) result.objective = payload.objective;
  if (payload.token_budget !== undefined) result.token_budget = payload.token_budget;
  return result;
}

module.exports = { desktopOperation, requireSessionID, requireWorkspaceHandle };

function commandCompletionInput(payload) {
  if (Object.keys(payload).some(key => !['sessionID', 'input', 'cursor'].includes(key)) ||
      typeof payload.input !== 'string' || /[\u0000-\u001f\u007f-\u009f]/u.test(payload.input) ||
      !Number.isInteger(payload.cursor) || payload.cursor < 0 || payload.cursor > [...payload.input].length || [...payload.input].length > 2048 ||
      /[\uD800-\uDFFF]/u.test(payload.input)) {
    throw new TypeError('valid bounded command input and rune cursor required');
  }
  return { input: payload.input, cursor: payload.cursor };
}
