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
  const id = () => encodeURIComponent(requireSessionID(payload.sessionID));
  switch (operation) {
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

module.exports = { desktopOperation, requireSessionID, requireWorkspaceHandle };
