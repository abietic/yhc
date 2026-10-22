import { executionDetailQuery } from './agent_execution_detail.mjs';
import { agentConversationQuery } from './agent_conversation.mjs';

const SESSION_ID_PATTERN = /^[A-Za-z0-9][A-Za-z0-9._-]{0,255}$/;
const UUID_PATTERN = /^[0-9A-Fa-f]{8}-[0-9A-Fa-f]{4}-[0-9A-Fa-f]{4}-[0-9A-Fa-f]{4}-[0-9A-Fa-f]{12}$/;
const ATTACH_TURN_FIELDS = new Set(['sessionID', 'prompt', 'clientTurnID']);
const QUEUE_PROMPT_FIELDS = new Set(['sessionID', 'prompt', 'clientQueueID']);
const DURABLE_IMPORT_FIELDS = new Set(['sessionID', 'confirmLegacyStopped']);
const EXECUTION_FIELDS = new Set([
  'model',
  'reasoning_effort',
  'permission_mode',
]);
const SETTINGS_REQUEST_TIMEOUT_MS = 15_000;
const SETTINGS_TIMEOUT_MESSAGE = 'Settings request timed out. Reload before saving.';

function settingsTimeoutError() {
  const error = new Error(SETTINGS_TIMEOUT_MESSAGE);
  error.code = 'settings_timeout';
  return error;
}

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

function requireInteractionRequestID(value) {
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

async function responseError(response, fallback) {
  const payload = await response.json().catch(() => ({}));
  const error = new Error(
    payload.error?.message || `${fallback} (${response.status})`,
  );
  error.code = payload.error?.code || '';
  error.status = response.status;
  return error;
}

function parseSSEFrames(input, emit) {
  let buffered = input;
  for (;;) {
    const boundary = buffered.search(/\r?\n\r?\n/);
    if (boundary < 0) return buffered;
    const separator = buffered.slice(boundary).match(/^\r?\n\r?\n/)[0];
    const frame = buffered.slice(0, boundary);
    buffered = buffered.slice(boundary + separator.length);
    const data = frame
      .split(/\r?\n/)
      .filter((line) => line.startsWith('data:'))
      .map((line) => line.slice(5).trimStart())
      .join('\n');
    if (!data) continue;
    try {
      emit({ kind: 'event', event: JSON.parse(data) });
    } catch (error) {
      emit({ kind: 'error', error: `invalid event payload: ${error.message}` });
    }
  }
}

function operationPath(operation, payload = {}) {
  const id = () => encodeURIComponent(requireSessionID(payload.sessionID));
  switch (operation) {
    case 'getMCPInventory':
      if (!payload || typeof payload !== 'object' || Array.isArray(payload) || Object.keys(payload).some(key => key !== 'sessionID')) throw new TypeError('MCP inventory requires only a session id');
      return [`/v1/sessions/${id()}/mcp-inventory`, 'GET'];
    case 'getSettings':
    case 'updateSettings': {
      const scope = payload.scope || 'user';
      if (!['user', 'project'].includes(scope)) throw new TypeError('invalid settings scope');
      const query = new URLSearchParams({ scope });
      if (payload.sessionID) query.set('session_id', requireSessionID(payload.sessionID));
      return [`/v1/settings?${query}`, operation === 'getSettings' ? 'GET' : 'PATCH',
        operation === 'getSettings' ? undefined : { revision: payload.revision, changes: payload.changes }];
    }
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
      throw new TypeError('new workspace sessions are available only in the Desktop App');
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
    case 'getAgentExecutionDetail': {
      const query = executionDetailQuery(payload);
      return [`/v1/sessions/${id()}/agent-execution-detail?${query}`, 'GET'];
    }
    case 'getAgentTranscript': {
      const query = agentConversationQuery(payload);
      return [`/v1/sessions/${id()}/agent-transcript?${query}`, 'GET'];
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
      const requestID = encodeURIComponent(requireInteractionRequestID(payload.requestID));
      return [
        `/v1/sessions/${id()}/interactions/${requestID}/resolve`,
        'POST',
        payload.result,
      ];
    }
    case 'getInteractionPlan':
      return [`/v1/sessions/${id()}/interactions/${encodeURIComponent(requireInteractionRequestID(payload.requestID))}/plan`, 'GET'];
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
      throw new TypeError('unsupported client API operation');
  }
}

function commandCompletionInput(payload) {
  if (Object.keys(payload).some(key => !['sessionID', 'input', 'cursor'].includes(key)) ||
      typeof payload.input !== 'string' || /[\u0000-\u001f\u007f-\u009f]/u.test(payload.input) ||
      !Number.isInteger(payload.cursor) || payload.cursor < 0 || payload.cursor > [...payload.input].length || [...payload.input].length > 2048 ||
      /[\uD800-\uDFFF]/u.test(payload.input)) {
    throw new TypeError('valid bounded command input and rune cursor required');
  }
  return { input: payload.input, cursor: payload.cursor };
}

async function exchangeBrowserPairing() {
  const params = new URLSearchParams(window.location.hash.slice(1));
  const pairingToken = params.get('pair');
  if (pairingToken) {
    // The one-time capability must not survive reloads, bookmarks, or logs.
    history.replaceState(
      null,
      document.title,
      `${location.pathname}${location.search}`,
    );
    const response = await fetch('/v1/auth/browser-session', {
      method: 'POST',
      credentials: 'same-origin',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ pairing_token: pairingToken }),
    });
    if (!response.ok) {
      throw await responseError(response, 'browser pairing failed');
    }
  }
  const response = await fetch('/v1/auth/browser-session', {
    credentials: 'same-origin',
  });
  if (!response.ok) {
    throw await responseError(response, 'browser session unavailable');
  }
  return response.json();
}

function browserTransport() {
  let csrfToken = '';
  let eventListener = () => {};
  const streams = new Map();

  async function request(operation, payload) {
    const [pathname, method, body] = operationPath(operation, payload);
    const headers = {};
    if (body !== undefined) headers['Content-Type'] = 'application/json';
    if (!['GET', 'HEAD', 'OPTIONS'].includes(method)) {
      headers['X-YHC-CSRF'] = csrfToken;
    }
    const settingsRequest = (operation === 'getSettings' || operation === 'updateSettings') &&
      new URL(pathname, window.location.origin).pathname === '/v1/settings';
    const controller = settingsRequest ? new AbortController() : null;
    const timeout = controller ? setTimeout(() => controller.abort(), SETTINGS_REQUEST_TIMEOUT_MS) : null;
    try {
      const response = await fetch(pathname, {
        method,
        credentials: 'same-origin',
        headers,
        body: body === undefined ? undefined : JSON.stringify(body),
        ...(controller ? { signal: controller.signal } : {}),
      });
      if (!response.ok) {
        throw await responseError(response, 'browser request failed');
      }
      return response.status === 204 ? null : await response.json();
    } catch (error) {
      if (controller?.signal.aborted) throw settingsTimeoutError();
      throw error;
    } finally {
      if (timeout !== null) clearTimeout(timeout);
    }
  }

  return {
    async getInfo() {
      const session = await exchangeBrowserPairing();
      if (
        session.protocol_version !== 2 ||
        typeof session.csrf_token !== 'string'
      ) {
        throw new Error('browser backend protocol mismatch');
      }
      csrfToken = session.csrf_token;
      return { protocolVersion: session.protocol_version, surface: 'web' };
    },
    getProviderStatus: async () => ({
      configured: false,
      secureStorageAvailable: false,
      hostManaged: true,
    }),
    configureProvider: async () => {
      throw new Error('Provider setup is available only in the Desktop App.');
    },
    selectWorkspace: async () => null,
    api: request,
    onEventStream(listener) {
      eventListener = listener;
      return () => {
        eventListener = () => {};
      };
    },
    onBackendExit: () => () => {},
    async startEvents(sessionID, after) {
      requireSessionID(sessionID);
      const controller = new AbortController();
      streams.get(sessionID)?.abort();
      streams.set(sessionID, controller);
      const response = await fetch(
        `/v1/sessions/${encodeURIComponent(sessionID)}/events?after=${Number(after || 0)}`,
        {
          credentials: 'same-origin',
          signal: controller.signal,
        },
      );
      if (response.status === 409) {
        await response.body?.cancel();
        if (streams.get(sessionID) === controller) streams.delete(sessionID);
        return { status: 'gap' };
      }
      if (!response.ok || !response.body) {
        if (streams.get(sessionID) === controller) streams.delete(sessionID);
        throw await responseError(response, 'event stream unavailable');
      }
      void (async () => {
        const reader = response.body.getReader();
        const decoder = new TextDecoder();
        let buffered = '';
        let failure = null;
        try {
          for (;;) {
            const { value, done } = await reader.read();
            if (done) break;
            buffered = parseSSEFrames(
              buffered + decoder.decode(value, { stream: true }),
              (payload) => eventListener({ ...payload, sessionID }),
            );
          }
          parseSSEFrames(
            buffered + decoder.decode(),
            (payload) => eventListener({ ...payload, sessionID }),
          );
        } catch (error) {
          if (error.name !== 'AbortError') failure = error.message;
        } finally {
          if (streams.get(sessionID) === controller) {
            streams.delete(sessionID);
            eventListener({
              kind: failure ? 'error' : 'closed',
              sessionID,
              error: failure,
            });
          }
        }
      })();
      return { status: 'open' };
    },
    async stopEvents(sessionID) {
      streams.get(sessionID)?.abort();
      streams.delete(sessionID);
    },
    openWeb: async () => {},
  };
}

async function desktopAPI(bridge, operation, payload) {
  if (typeof bridge.apiResult !== 'function') return bridge.api(operation, payload);
  const result = await bridge.apiResult(operation, payload);
  if (result?.ok === true && Object.hasOwn(result, 'value')) return result.value;
  const failure = result?.error;
  if (
    result?.ok !== false || !failure ||
    typeof failure.message !== 'string' || !failure.message ||
    (failure.code !== undefined && (
      typeof failure.code !== 'string' || !/^[a-z][a-z0-9_]{0,127}$/.test(failure.code)
    )) ||
    (failure.status !== undefined && (
      !Number.isInteger(failure.status) || failure.status < 400 || failure.status > 599
    ))
  ) {
    throw new Error('invalid desktop API result');
  }
  // Construct Error in the renderer: IPC and contextBridge drop custom Error
  // properties, whereas the plain result envelope crosses both boundaries.
  const error = new Error(failure.message);
  error.code = failure.code || '';
  if (failure.status !== undefined) error.status = failure.status;
  throw error;
}

function desktopTransport(bridge) {
  return {
    getInfo: async () => ({ ...(await bridge.getInfo()), surface: 'desktop' }),
    getProviderStatus: () => bridge.getProviderStatus(),
    configureProvider: (input) => bridge.configureProvider(input),
    selectWorkspace: () => bridge.selectWorkspace(),
    api: (operation, payload) => desktopAPI(bridge, operation, payload),
    startEvents: (sessionID, after) => bridge.startEvents(sessionID, after),
    stopEvents: (sessionID) => bridge.stopEvents(sessionID),
    onEventStream: (listener) => bridge.onEventStream(listener),
    onBackendExit: (listener) => bridge.onBackendExit(listener),
    openWeb: () => bridge.openWeb(),
  };
}

export function createTransport() {
  return window.yhcDesktop
    ? desktopTransport(window.yhcDesktop)
    : browserTransport();
}

export { operationPath, parseSSEFrames };
