import { replayTranscriptFenceMatches } from './replay.mjs';

export const initialState = () => ({
  sessions: {},
  activeID: null,
});

const executionDefaults = (execution = {}) => ({
  status: 'idle',
  requestID: 0,
  model: '',
  models: [],
  reasoningEffort: 'default',
  reasoningEffortSupported: false,
  reasoningEffortOptions: ['default'],
  permissionMode: 'default',
  permissionModeOptions: [],
  dispatchBlock: null,
  error: '',
  ...execution,
});

const privatePathKeys = new Set([
  'c' + 'wd',
  'transcript_' + 'dir',
  'repository_' + 'root',
  'workspace_' + 'path',
]);
const MAX_SETTLED_TURNS = 64;

function withoutPrivatePaths(value) {
  if (!value || typeof value !== 'object' || Array.isArray(value)) return value;
  return Object.fromEntries(Object.entries(value).filter(([key]) => !privatePathKeys.has(key)));
}

export function reducer(state, action) {
  switch (action.type) {
    case 'SESSION_UPSERT':
      return upsertSession(state, action.session);
    case 'SESSION_SELECT':
      return state.sessions[action.id] ? { ...state, activeID: action.id } : state;
    case 'SESSION_REMOVE':
      return removeSession(state, action.id);
    case 'SESSION_NOTICE':
      return updateSession(state, action.id, (session) => ({
        ...session,
        notice: action.notice || '',
        status: action.status || session.status,
      }));
    case 'SESSION_REPLAY_GAP':
      return updateSession(state, action.id, (session) => ({
        ...session,
        replaying: true,
        notice: 'Event history expired; rebuilding from the runtime snapshot.',
      }));
    case 'SESSION_SNAPSHOT':
      return applySnapshot(state, action.id, action.snapshot);
    case 'SESSION_TRANSCRIPT_PAGE':
      return applyTranscriptPage(
        state,
        action.id,
        action.page,
        action.replace,
        action.eventCursorFence,
      );
    case 'DURABLE_SESSION_PAGE':
      return applyDurableSessionPage(state, action.sessions);
    case 'SESSION_REVIEW_LOADING':
      return beginReview(state, action);
    case 'SESSION_REVIEW_SUCCESS':
      return settleReview(state, action, false);
    case 'SESSION_REVIEW_FAILED':
      return settleReview(state, action, true);
    case 'EXECUTION_SETTINGS_LOADING':
      return beginExecutionSettings(state, action);
    case 'EXECUTION_SETTINGS_SUCCESS':
      return settleExecutionSettings(state, action, false);
    case 'EXECUTION_SETTINGS_FAILED':
      return settleExecutionSettings(state, action, true);
    case 'ATTACH_STARTED':
      return beginAttach(state, action);
    case 'ATTACH_ACCEPTED':
      return settleAttach(state, action, 'turn_accepted');
    case 'ATTACH_INTERACTION_REQUIRED':
      return settleAttach(state, action, 'interaction_required');
    case 'ATTACH_FAILED':
      return settleAttach(state, action, 'failed');
    case 'DURABLE_IMPORT_STARTED':
      return beginDurableImport(state, action);
    case 'DURABLE_IMPORT_COMPLETED':
      return settleDurableImport(state, action, false);
    case 'DURABLE_IMPORT_FAILED':
      return settleDurableImport(state, action, true);
    case 'SESSION_DRAFT':
      return updateSession(state, action.id, (session) => ({
        ...session,
        draft: action.draft,
      }));
    case 'INTERACTION_DRAFT_UPDATE':
      if (!hasPendingInteraction(state.sessions[action.id], action.requestID)) return state;
      return updateSession(state, action.id, (session) => ({
        ...session,
        interactionForms: { ...session.interactionForms, [action.requestID]: {
          ...interactionDraft(session, action.requestID), ...action.patch,
          answers: Object.fromEntries(Object.entries({
            ...interactionDraft(session, action.requestID).answers, ...(action.patch?.answers || {}),
          }).map(([questionID, answer]) => [questionID, {
            optionIDs: Array.isArray(answer?.optionIDs) ? answer.optionIDs : [],
            text: typeof answer?.text === 'string' ? answer.text : '',
          }])),
          planReview: { ...interactionDraft(session, action.requestID).planReview, ...(action.patch?.planReview || {}) },
        } },
      }));
    case 'INTERACTION_PLAN_LOADING':
      if (!hasPendingInteraction(state.sessions[action.id], action.requestID)) return state;
      return updateSession(state, action.id, (session) => ({
        ...session,
        interactionForms: { ...session.interactionForms, [action.requestID]: {
          ...interactionDraft(session, action.requestID), planReview: { ...interactionDraft(session, action.requestID).planReview, status: 'loading', error: '' },
        } },
      }));
    case 'INTERACTION_PLAN_SUCCESS':
    case 'INTERACTION_PLAN_FAILED':
      if (!hasPendingInteraction(state.sessions[action.id], action.requestID)) return state;
      return updateSession(state, action.id, (session) => {
        const draft = interactionDraft(session, action.requestID);
        const review = action.type === 'INTERACTION_PLAN_SUCCESS'
          ? validatedPlanReview(session, action.requestID, action.review)
          : null;
        return {
          ...session,
          interactionForms: { ...session.interactionForms, [action.requestID]: {
            ...draft,
            planReview: review
              ? { ...draft.planReview, ...review, status: 'ready', error: '' }
              : {
                ...draft.planReview,
                status: 'error',
                error: String(action.error || (
                  action.type === 'INTERACTION_PLAN_SUCCESS'
                    ? 'Plan review response was invalid.'
                    : 'Plan review failed.'
                )),
              },
          } },
        };
      });
    case 'INTERACTION_SUBMITTING':
      if (!hasPendingInteraction(state.sessions[action.id], action.requestID)) return state;
      return updateSession(state, action.id, (session) => ({
        ...session,
        resolvingRequestID: action.requestID,
        interactionForms: { ...session.interactionForms, [action.requestID]: { ...interactionDraft(session, action.requestID), submitting: true, error: '' } },
      }));
    case 'INTERACTION_SUBMIT_FAILED':
      if (!hasPendingInteraction(state.sessions[action.id], action.requestID)) return state;
      return updateSession(state, action.id, (session) => ({
        ...session,
        resolvingRequestID: '',
        interactionForms: { ...session.interactionForms, [action.requestID]: { ...interactionDraft(session, action.requestID), submitting: false, error: String(action.error || 'Interaction submission failed.') } },
      }));
    case 'EVENT':
      return applyEvent(state, action.event);
    default:
      return state;
  }
}

function sessionDefaults(session) {
  const publicSession = withoutPrivatePaths(session || {});
  const review = {
    status: 'idle',
    requestID: 0,
    ignoreWhitespace: false,
    generatedAt: '',
    sources: [],
    error: '',
    ...withoutPrivatePaths(publicSession.review || {}),
  };
  const normalized = {
    id: '',
    title: 'Untitled',
    workspace_label: '',
    status: 'idle',
    active_turn_id: '',
    settledTurnIDs: [],
    messages: [],
    interactions: [],
    interactionForms: {},
    activity: [],
    cursor: 0,
    attention: false,
    replaying: false,
    transcriptNextCursor: '',
    transcriptHasMore: false,
    draft: '',
    durable: false,
    resumable: false,
    import_required: false,
    live: false,
    activation: 'detached',
    git_branch: '',
    resolvingRequestID: '',
    notice: '',
    ...publicSession,
    review,
    execution: executionDefaults(publicSession.execution),
  };
  const settledTurnIDs = [...new Set(
    (Array.isArray(normalized.settledTurnIDs) ? normalized.settledTurnIDs : [])
      .filter((id) => typeof id === 'string' && id.length > 0 && id.length <= 512),
  )].slice(-MAX_SETTLED_TURNS);
  return {
    ...normalized,
    import_required: Boolean(normalized.import_required),
    settledTurnIDs,
    activation: sessionActivation(normalized),
  };
}

function sessionActivation(session = {}) {
  if (session?.live) {
    return session.activation === 'interaction_required'
      ? 'interaction_required'
      : 'live';
  }
  if (['attaching', 'failed', 'importing'].includes(session?.activation)) {
    return session.activation;
  }
  return 'detached';
}

function beginExecutionSettings(state, action) {
  return updateSession(state, action.id, (session) => ({
    ...session,
    execution: executionDefaults({
      ...session.execution,
      status: action.mutation ? 'updating' : 'loading',
      requestID: action.requestID,
      error: '',
    }),
  }));
}

function settleExecutionSettings(state, action, failed) {
  const current = state.sessions[action.id];
  if (!current || current.execution?.requestID !== action.requestID) return state;
  if (failed) {
    return updateSession(state, action.id, (session) => ({
      ...session,
      execution: executionDefaults({
        ...session.execution,
        status: 'error',
        error: String(action.error || 'Execution settings failed.'),
      }),
    }));
  }
  const response = action.response || {};
  return updateSession(state, action.id, (session) => ({
    ...session,
    execution: executionDefaults({
      ...session.execution,
      status: 'ready',
      model: typeof response.model === 'string' ? response.model : '',
      models: Array.isArray(response.models) ? response.models : [],
      reasoningEffort: typeof response.reasoning_effort === 'string'
        ? response.reasoning_effort
        : 'default',
      reasoningEffortSupported: Boolean(response.reasoning_effort_supported),
      reasoningEffortOptions: Array.isArray(response.reasoning_effort_options)
        ? response.reasoning_effort_options
        : ['default'],
      permissionMode: typeof response.permission_mode === 'string'
        ? response.permission_mode
        : 'default',
      permissionModeOptions: Array.isArray(response.permission_mode_options)
        ? response.permission_mode_options
        : [],
      dispatchBlock: response.dispatch_block || null,
      error: '',
    }),
  }));
}

function detachedAttachEligible(session) {
  return Boolean(
    session?.durable && session.resumable && !session.import_required && !session.live,
  ) &&
    ['detached', 'failed'].includes(sessionActivation(session)) &&
    !['offline', 'restoring', 'archived'].includes(session.status);
}

export function canImportDurableSession(session) {
  return Boolean(
    session?.durable && session.import_required && !session.live &&
    sessionActivation(session) === 'detached' &&
    !['offline', 'restoring'].includes(session.status),
  );
}

function beginDurableImport(state, action) {
  const current = state.sessions[action.id];
  if (!canImportDurableSession(current)) return state;
  return updateSession(state, action.id, (session) => ({
    ...session,
    activation: 'importing',
    status: 'importing',
    notice: 'Importing legacy session…',
  }));
}

function settleDurableImport(state, action, failed) {
  const current = state.sessions[action.id];
  if (!current || sessionActivation(current) !== 'importing') return state;
  if (failed) {
    return updateSession(state, action.id, (session) => ({
      ...session,
      activation: 'detached',
      status: 'import required',
      notice: String(action.error || 'Legacy session import failed.'),
    }));
  }
  return updateSession(state, action.id, (session) => ({
    ...session,
    activation: 'detached',
    status: 'archived',
    resumable: false,
    import_required: false,
    notice: 'Import completed. Refreshing canonical session metadata…',
  }));
}

function beginAttach(state, action) {
  const current = state.sessions[action.id];
  if (!detachedAttachEligible(current)) return state;
  return updateSession(state, action.id, (session) => ({
    ...session,
    live: false,
    activation: 'attaching',
    active_turn_id: '',
    notice: 'Attaching saved session…',
  }));
}

function settleAttach(state, action, outcome) {
  const current = state.sessions[action.id];
  if (!current || sessionActivation(current) !== 'attaching') return state;
  const response = action.response || {};
  if (outcome === 'failed') {
    return updateSession(state, action.id, (session) => ({
      ...session,
      live: false,
      activation: 'failed',
      active_turn_id: '',
      status: session.resumable ? 'saved' : 'archived',
      notice: String(action.error || 'Could not attach saved session.'),
    }));
  }
  if (response.status !== outcome || response.session?.id !== action.id) return state;
  if (action.clientTurnID && response.client_turn_id !== action.clientTurnID) return state;
  if (outcome === 'turn_accepted' && !response.turn_id) return state;
  if (outcome === 'interaction_required' &&
    (!response.interaction?.request_id || !response.interaction?.turn_id ||
      !response.interaction?.kind ||
      response.session?.active_turn_id !== response.interaction.turn_id)) {
    return state;
  }
  return updateSession(state, action.id, (session) => {
    const attached = {
      ...session,
      ...response.session,
      live: true,
      activation: outcome === 'turn_accepted' ? 'live' : 'interaction_required',
    };
    if (outcome === 'turn_accepted') {
      return {
        ...attached,
        draft: '',
        status: 'running',
        active_turn_id: response.turn_id,
        notice: 'Agent is working.',
      };
    }
    const interactions = mergeInteractions(session.interactions, response.interaction);
    return {
      ...attached,
      status: 'waiting',
      active_turn_id: response.interaction.turn_id,
      interactions,
      interactionForms: pruneInteractionForms(session.interactionForms, interactions),
      attention: interactions.length > 0,
      notice: 'A decision is required.',
    };
  });
}

function applyDurableSessionPage(state, rows) {
  const sessions = { ...state.sessions };
  let firstID = '';
  for (const incoming of Array.isArray(rows) ? rows : []) {
    if (!incoming?.id) continue;
    firstID ||= incoming.id;
    const existing = sessions[incoming.id];
    const current = sessionDefaults(existing || {
      id: incoming.id,
    });
    const importRequired = current.live ? false : Boolean(incoming.import_required);
    const resumable = current.live
      ? Boolean(current.resumable || incoming.resumable)
      : Boolean(incoming.resumable && !importRequired);
    const merged = sessionDefaults({
      ...current,
      ...incoming,
      durable: true,
      resumable,
      import_required: importRequired,
      live: current.live,
    });
    if (current.live) {
      for (const field of [
        'status',
        'active_turn_id',
        'last_error',
        'attention',
        'draft',
        'messages',
        'interactions',
        'interactionForms',
        'activity',
        'execution',
        'review',
        'live',
        'notice',
      ]) {
        merged[field] = current[field];
      }
    } else {
      merged.status = importRequired
        ? 'import required'
        : (resumable ? 'saved' : 'archived');
      merged.active_turn_id = '';
      merged.attention = false;
      merged.live = false;
      merged.activation = current.activation === 'importing' && importRequired
        ? 'importing'
        : 'detached';
      merged.execution = executionDefaults();
      merged.notice = merged.activation === 'importing'
        ? current.notice
        : (importRequired
          ? 'Import this legacy session before continuing.'
          : (resumable
            ? 'Saved session. Select it to view history.'
            : 'This catalog entry is available as read-only history.'));
    }
    sessions[incoming.id] = merged;
  }
  return {
    ...state,
    activeID: state.activeID || firstID || null,
    sessions,
  };
}

function upsertSession(state, incoming) {
  if (!incoming?.id) return state;
  const current = state.sessions[incoming.id];
  const session = sessionDefaults({
    ...current,
    ...incoming,
    review: {
      ...(current?.review || {}),
      ...(incoming.review || {}),
    },
  });
  return {
    ...state,
    activeID: state.activeID || session.id,
    sessions: { ...state.sessions, [session.id]: session },
  };
}

function beginReview(state, action) {
  return updateSession(state, action.id, (session) => ({
    ...session,
    review: {
      ...session.review,
      status: 'loading',
      requestID: action.requestID,
      ignoreWhitespace: Boolean(action.ignoreWhitespace),
      error: '',
    },
  }));
}

function settleReview(state, action, failed) {
  const current = state.sessions[action.id];
  if (!current || current.review?.requestID !== action.requestID) return state;
  const response = action.response || {};
  return updateSession(state, action.id, (session) => ({
    ...session,
    review: {
      ...session.review,
      status: failed ? 'error' : 'ready',
      generatedAt: failed
        ? session.review.generatedAt
        : (response.generated_at || ''),
      sources: failed
        ? session.review.sources
        : (Array.isArray(response.sources)
          ? response.sources.map((source) => withoutPrivatePaths(source))
          : []),
      error: failed ? String(action.error || 'Review failed.') : '',
    },
  }));
}

function updateSession(state, id, update) {
  const current = state.sessions[id];
  if (!current) return state;
  return {
    ...state,
    sessions: { ...state.sessions, [id]: sessionDefaults(update(current)) },
  };
}

function removeSession(state, id) {
  if (!state.sessions[id]) return state;
  const sessions = { ...state.sessions };
  delete sessions[id];
  const activeID = state.activeID === id
    ? Object.keys(sessions)[0] || null
    : state.activeID;
  return { ...state, sessions, activeID };
}

function snapshotMessage(message) {
  return {
    id: message.id || '',
    role: message.role || 'assistant',
    content: String(message.content || ''),
    reasoningContent: String(message.reasoning_content || ''),
    toolCalls: Array.isArray(message.tool_calls) ? message.tool_calls : [],
    toolCallID: message.tool_call_id || '',
    toolName: message.tool_name || '',
    turnID: message.turn_id || '',
    sequence: Number(message.sequence || 0),
    completed: Boolean(message.completed),
    source: String(message.source || 'runtime'),
  };
}

function retainLiveProjection(message) {
  if (message.source === 'conversation-fallback') return false;
  if (message.source === 'durable') return true;
  return !message.completed;
}

const activityKinds = new Set(['turn', 'tool', 'task', 'agent', 'interaction']);
const activityStates = {
  turn: new Set(['started', 'waiting', 'completed', 'stopped', 'failed']),
  tool: new Set(['running', 'paused', 'completed', 'stopped', 'failed']),
  task: new Set(['running', 'paused', 'completed', 'stopped', 'failed']),
  agent: new Set(['running', 'paused', 'completed', 'stopped', 'failed']),
  interaction: new Set(['waiting', 'resolved']),
};
const activityCategories = {
  turn: new Set(['']),
  tool: new Set([
    'file_read', 'file_search', 'file_change', 'command', 'network',
    'task', 'agent', 'tool',
  ]),
  task: new Set(['task']),
  agent: new Set(['agent']),
  interaction: new Set(['', 'permission', 'question', 'plan_approval', 'repeated_tool']),
};

function activityIdentity(value, maxLength = 512) {
  if (typeof value !== 'string' || value.length < 1 || value.length > maxLength) return '';
  return /^[A-Za-z0-9._:-]+$/.test(value) ? value : '';
}

function normalizeActivityEntry(value) {
  if (!value || typeof value !== 'object') return null;
  const id = activityIdentity(value.id, 128);
  const turnID = activityIdentity(value.turn_id ?? value.turnID);
  const kind = typeof value.kind === 'string' ? value.kind : '';
  const state = typeof value.state === 'string' ? value.state : '';
  const category = typeof value.category === 'string' ? value.category : '';
  const timestamp = typeof value.timestamp === 'string' && value.timestamp.length <= 64 &&
    Number.isFinite(Date.parse(value.timestamp))
    ? value.timestamp
    : '';
  if (!id || !turnID || !activityKinds.has(kind) ||
    !activityStates[kind]?.has(state) || !activityCategories[kind]?.has(category) ||
    !timestamp) {
    return null;
  }
  return { id, turnID, kind, state, category, timestamp };
}

function activityStateIsTerminal(kind, state) {
  if (kind === 'interaction') return state === 'resolved';
  if (kind === 'turn' && state === 'waiting') return true;
  return ['completed', 'stopped', 'failed'].includes(state);
}

function mergeActivity(current, incoming) {
  const entry = normalizeActivityEntry(incoming);
  if (!entry) return current;
  const index = current.findIndex((item) => item.id === entry.id);
  if (index < 0) return [...current, entry].slice(-100);
  const existing = current[index];
  if (existing.kind !== entry.kind || existing.turnID !== entry.turnID) return current;
  if (activityStateIsTerminal(existing.kind, existing.state) && existing.state !== entry.state) {
    return current;
  }
  const merged = {
    ...entry,
    category: entry.category || existing.category,
  };
  return [
    ...current.slice(0, index),
    ...current.slice(index + 1),
    merged,
  ].slice(-100);
}

function activityQueue(items) {
  return (Array.isArray(items) ? items : []).reduce(
    (queue, item) => mergeActivity(queue, item),
    [],
  );
}

function applySnapshot(state, id, snapshot) {
  if (!snapshot?.session || snapshot.session.id !== id) return state;
  const current = sessionDefaults(state.sessions[id] || { id });
  const interactions = interactionQueue(snapshot.interactions);
  const activity = activityQueue(snapshot.activity);
  const interactionForms = pruneInteractionForms(current.interactionForms, interactions);
  const snapshotMessages = Array.isArray(snapshot.messages)
    ? snapshot.messages.map(snapshotMessage)
    : [];
  const activeTurnID = String(snapshot.session.active_turn_id || '');
  const settledTurnIDs = [...new Set([
    ...current.settledTurnIDs,
    ...snapshotMessages
      .filter((message) => (
        message.completed && message.turnID && message.turnID !== activeTurnID
      ))
      .map((message) => message.turnID),
  ])].slice(-MAX_SETTLED_TURNS);
  const retained = current.messages.filter(retainLiveProjection);
  return {
    ...state,
    sessions: {
      ...state.sessions,
      [id]: sessionDefaults({
        ...current,
        ...snapshot.session,
        cursor: Number(snapshot.event_cursor || 0),
        messages: mergeMessages(retained, snapshotMessages),
        interactions,
        interactionForms,
        activity,
        settledTurnIDs,
        attention: interactions.length > 0,
        activation: interactions.length > 0 ? 'interaction_required' : 'live',
        replaying: false,
        resolvingRequestID: '',
        notice: 'Session state synchronized.',
      }),
    },
  };
}

function transcriptMessage(entry) {
  const message = entry?.message || entry || {};
  return {
    id: entry?.id || entry?.transcript_entry_id || message.id || '',
    role: message.role || entry?.role || 'assistant',
    content: String(message.content || entry?.content || ''),
    reasoningContent: String(
      message.reasoning_content || entry?.reasoning_content || '',
    ),
    toolCalls: Array.isArray(message.tool_calls)
      ? message.tool_calls
      : (entry?.tool_calls || []),
    toolCallID: message.tool_call_id || entry?.tool_call_id || '',
    toolName: message.tool_name || entry?.tool_name || '',
    turnID: entry?.turn_id || message.turn_id || '',
    sequence: Number(entry?.sequence || message.sequence || 0),
    completed: true,
    source: String(entry?.source || message.source || 'durable'),
  };
}

function mergeMessages(...groups) {
  const seen = new Set();
  return groups.flat().filter((message) => {
    const identity = String(message?.id || '');
    if (!identity) return true;
    if (seen.has(identity)) return false;
    seen.add(identity);
    return true;
  });
}

function applyTranscriptPage(
  state,
  id,
  page,
  replace = false,
  eventCursorFence = null,
) {
  const current = sessionDefaults(state.sessions[id] || { id });
  const fencedReplacement = replace && eventCursorFence !== null;
  if (
    fencedReplacement &&
    (
      !state.sessions[id] ||
      !replayTranscriptFenceMatches(eventCursorFence, current.cursor)
    )
  ) {
    return state;
  }
  const entries = Array.isArray(page?.entries)
    ? page.entries
    : (page?.messages || []);
  const incoming = entries.map(transcriptMessage);
  const existing = replace
    ? current.messages.filter(retainLiveProjection)
    : current.messages;
  const messages = mergeMessages(incoming, existing);
  return {
    ...state,
    sessions: {
      ...state.sessions,
      [id]: sessionDefaults({
        ...current,
        messages,
        transcriptNextCursor: typeof page?.next_cursor === 'string'
          ? page.next_cursor
          : '',
        transcriptHasMore: Boolean(page?.has_more),
        replaying: false,
      }),
    },
  };
}

function wireMessage(data, role, turnID, sequence) {
  const message = data.message || {};
  return {
    id: data.transcript_entry_id || '',
    role,
    content: String(message.content || data.content || ''),
    reasoningContent: String(message.reasoning_content || ''),
    toolCalls: Array.isArray(message.tool_calls) ? message.tool_calls : [],
    toolCallID: message.tool_call_id || '',
    toolName: message.tool_name || '',
    turnID: turnID || '',
    sequence: Number(sequence || 0),
    completed: role !== 'assistant',
    source: String(data.source || (data.transcript_entry_id ? 'durable' : 'runtime')),
  };
}

function mergeToolCalls(current, incoming) {
  const merged = [...current];
  for (const call of incoming) {
    const identity = call.id || call.name || call.function?.name;
    const index = identity
      ? merged.findIndex((candidate) => (
        candidate.id ||
        candidate.name ||
        candidate.function?.name
      ) === identity)
      : -1;
    if (index >= 0) merged[index] = { ...merged[index], ...call };
    else merged.push(call);
  }
  return merged;
}

function appendAssistant(messages, data, turnID, sequence) {
  const next = wireMessage(data, 'assistant', turnID, sequence);
  const last = messages.at(-1);
  if (last?.role === 'assistant' && last.turnID === turnID) {
    return [
      ...messages.slice(0, -1),
      {
        ...last,
        id: next.id || last.id,
        content: last.content + next.content,
        reasoningContent: last.reasoningContent + next.reasoningContent,
        toolCalls: mergeToolCalls(last.toolCalls, next.toolCalls),
        sequence: next.sequence || last.sequence,
      },
    ];
  }
  return [...messages, next];
}

function markTurnSettled(next, turnID) {
  if (!turnID) return;
  next.settledTurnIDs = [...new Set([
    ...next.settledTurnIDs,
    turnID,
  ])].slice(-MAX_SETTLED_TURNS);
}

function applyTerminal(next, data, turnID) {
  const completedTurnID = turnID || next.active_turn_id;
  const waitingForInput = data.reason === 'waiting_input';
  if (completedTurnID) {
    next.messages = next.messages.map((message) => (
      message.role === 'assistant' && message.turnID === completedTurnID
        ? { ...message, completed: true }
        : message
    ));
    if (!waitingForInput) markTurnSettled(next, completedTurnID);
  }
  if (turnID && next.active_turn_id && turnID !== next.active_turn_id) {
    return;
  }
  next.active_turn_id = '';
  next.status = waitingForInput
    ? 'waiting'
    : (data.error ? 'error' : 'idle');
  next.last_error = data.error || '';
  next.notice = data.error || (waitingForInput
    ? 'Waiting for input.'
    : 'Ready');
}

function applyEvent(state, event) {
  if (
    !event?.session_id ||
    !Number.isSafeInteger(event.id) ||
    event.id <= 0
  ) {
    return state;
  }
  const current = sessionDefaults(
    state.sessions[event.session_id] || { id: event.session_id },
  );
  if (event.id <= current.cursor) return state;

  const data = event.data || {};
  const next = { ...current, cursor: event.id, replaying: false };
  if (
    event.turn_id &&
    current.settledTurnIDs.includes(event.turn_id) &&
    !['terminal', 'turn.finished', 'activity'].includes(event.type)
  ) {
    return {
      ...state,
      activeID: state.activeID || event.session_id,
      sessions: { ...state.sessions, [event.session_id]: next },
    };
  }
  switch (event.type) {
    case 'session.created':
      next.workspace_label = data.workspace_label || next.workspace_label;
      next.title = data.title || next.title;
      break;
    case 'session.closed':
      next.status = 'closed';
      next.active_turn_id = '';
      next.live = false;
      next.activation = 'detached';
      break;
    case 'user_message':
      next.messages = [
        ...current.messages,
        wireMessage(data, 'user', event.turn_id, event.engine_sequence),
      ];
      next.durable = true;
      next.resumable = true;
      break;
    case 'stream_event':
    case 'assistant':
      next.messages = appendAssistant(
        current.messages,
        data,
        event.turn_id,
        event.engine_sequence,
      );
      if (next.active_turn_id) next.status = 'running';
      break;
    case 'tool_result':
      next.messages = [
        ...current.messages,
        wireMessage(data, 'tool', event.turn_id, event.engine_sequence),
      ];
      break;
    case 'turn.accepted':
      next.status = 'running';
      next.active_turn_id = data.turn_id || event.turn_id;
      next.settledTurnIDs = current.settledTurnIDs.filter(
        (turnID) => turnID !== next.active_turn_id,
      );
      next.notice = 'Agent is working.';
      break;
    case 'turn.cancel.requested':
      next.status = 'stopping';
      next.notice = 'Stopping the active turn…';
      break;
    case 'terminal':
    case 'turn.finished':
      applyTerminal(next, data, event.turn_id);
      break;
    case 'interaction_requested': {
      next.interactions = mergeInteractions(current.interactions, data.interaction || data);
      next.status = 'waiting';
      next.attention = next.interactions.length > 0;
      next.activation = next.live ? 'interaction_required' : next.activation;
      next.notice = 'A decision is required.';
      break;
    }
    case 'interaction_resolved':
      next.interactions = current.interactions.filter(
        (item) => item.request_id !== data.request_id,
      );
      next.interactionForms = pruneInteractionForms(
        current.interactionForms, next.interactions,
      );
      next.attention = next.interactions.length > 0;
      next.resolvingRequestID = '';
      if (next.active_turn_id) next.status = 'running';
      if (!next.attention && next.live) next.activation = 'live';
      next.notice = next.attention ? 'Another decision is required.' : 'Decision received.';
      break;
    case 'activity':
      next.activity = mergeActivity(current.activity, data);
      break;
    default:
      break;
  }
  return {
    ...state,
    activeID: state.activeID || event.session_id,
    sessions: { ...state.sessions, [event.session_id]: next },
  };
}

function interactionQueue(items) {
  return (Array.isArray(items) ? items : []).reduce(
    (queue, item) => mergeInteractions(queue, item),
    [],
  );
}

function mergeInteractions(current, incoming) {
  if (!incoming?.request_id) return current;
  const index = current.findIndex((item) => item.request_id === incoming.request_id);
  if (index < 0) return [...current, incoming];
  return current.map((item, itemIndex) => itemIndex === index ? incoming : item);
}

function pruneInteractionForms(forms, interactions) {
  const allowed = new Set(interactions.map((item) => item.request_id));
  return Object.fromEntries(Object.entries(forms || {}).filter(([id]) => allowed.has(id)));
}

export const activeInteraction = (session) => session?.interactions?.[0] || null;
function hasPendingInteraction(session, requestID) {
  return Boolean(
    requestID &&
    session?.interactions?.some((interaction) => interaction.request_id === requestID),
  );
}

function validatedPlanReview(session, requestID, review) {
  const interaction = session?.interactions?.find(
    (item) => item.request_id === requestID,
  );
  if (
    typeof review?.content !== 'string' ||
    new TextEncoder().encode(review.content).length > (1 << 20) ||
    !Number.isSafeInteger(review.revision) ||
    review.revision <= 0 ||
    review.revision !== interaction?.plan_approval?.revision ||
    !/^sha256:[a-f0-9]{64}$/.test(review.digest || '')
  ) {
    return null;
  }
  return {
    content: review.content,
    revision: review.revision,
    digest: review.digest,
  };
}

const emptyInteractionDraft = () => ({
  step: 0, answers: {}, planReview: { status: 'idle', content: '', revision: 0, digest: '', error: '' },
  targetMode: '', feedback: '', submitting: false, error: '',
});
export const interactionDraft = (session, requestID) => ({
  ...emptyInteractionDraft(), ...(session?.interactionForms?.[requestID] || {}),
  answers: { ...(session?.interactionForms?.[requestID]?.answers || {}) },
  planReview: { ...emptyInteractionDraft().planReview, ...(session?.interactionForms?.[requestID]?.planReview || {}) },
});

function invalidInteractionResult() { throw new TypeError('valid interaction result required'); }

export function buildPermissionResolution(interaction, input = {}) {
  const decision = input.decision;
  const grantScopes = interaction?.permission?.grant_scopes || [];
  if (!['deny', 'cancelled'].includes(decision) && !grantScopes.includes(decision)) invalidInteractionResult();
  if (!['allow_once', 'allow_session', 'allow_always', 'deny', 'cancelled'].includes(decision)) invalidInteractionResult();
  const permission = { decision };
  const message = typeof input.message === 'string' ? input.message : '';
  if (message && !['deny', 'cancelled'].includes(decision)) invalidInteractionResult();
  if (new TextEncoder().encode(message).length > 4 * 1024) invalidInteractionResult();
  if (message) permission.message = message;
  return { kind: 'permission', permission };
}

export function buildQuestionResolution(interaction, input = {}) {
  const outcome = input.outcome;
  if (!['submit', 'discuss', 'cancel'].includes(outcome)) invalidInteractionResult();
  if (outcome !== 'submit') {
    if (input.answers?.length) invalidInteractionResult();
    return { kind: 'question', question: { outcome } };
  }
  const questions = interaction?.question?.questions;
  const answers = input.answers;
  if (!Array.isArray(questions) || questions.length < 1 || questions.length > 4 || !Array.isArray(answers) || answers.length !== questions.length) invalidInteractionResult();
  let answerBytes = 0;
  const normalized = answers.map((answer, index) => {
    const question = questions[index];
    if (!question || answer?.question_id !== question.id || !answer.question_id) invalidInteractionResult();
    const optionIDs = Array.isArray(answer.option_ids) ? answer.option_ids : [];
    const known = new Set((question.options || []).map((option) => option.id));
    if (optionIDs.some((id) => !known.has(id)) || new Set(optionIDs).size !== optionIDs.length || (!question.multi_select && optionIDs.length > 1)) invalidInteractionResult();
    const text = typeof answer.text === 'string' ? answer.text : '';
    const textBytes = new TextEncoder().encode(text).length;
    answerBytes += textBytes;
    if (textBytes > 16 * 1024 || answerBytes > 32 * 1024) invalidInteractionResult();
    const textPresent = Boolean(text.trim());
    if ((question.options || []).length === 0) {
      if (optionIDs.length || !textPresent) invalidInteractionResult();
    } else if (question.multi_select) {
      if (!optionIDs.length && !textPresent) invalidInteractionResult();
    } else if ((optionIDs.length === 1) === textPresent) {
      invalidInteractionResult();
    }
    const result = { question_id: answer.question_id };
    if (optionIDs.length) result.option_ids = optionIDs;
    if (text) result.text = text;
    return result;
  });
  return { kind: 'question', question: { outcome, answers: normalized } };
}

export function buildPlanResolution(interaction, input = {}, review) {
  const plan = interaction?.plan_approval;
  const outcome = input.outcome;
  if (!plan || review?.status !== 'ready' || review.revision !== plan.revision || !/^sha256:[a-f0-9]{64}$/.test(review.digest || '') || !['approve', 'revise', 'cancel'].includes(outcome) || !plan.target_modes?.includes(input.target_mode)) invalidInteractionResult();
  const feedback = typeof input.feedback === 'string' ? input.feedback.trim() : '';
  if ((outcome === 'revise' && (!feedback || new TextEncoder().encode(feedback).length > 16 * 1024)) || (outcome === 'approve' && feedback) || (outcome !== 'approve' && input.confirmed)) invalidInteractionResult();
  if (outcome === 'approve' && Boolean(input.confirmed) !== (input.target_mode === 'bypassPermissions')) invalidInteractionResult();
  const planApproval = { outcome, revision: plan.revision, target_mode: input.target_mode, reviewed_digest: review.digest, confirmed: Boolean(input.confirmed) };
  if (feedback) planApproval.feedback = feedback;
  return { kind: 'plan_approval', plan_approval: planApproval };
}

export function buildRepeatedToolResolution(interaction, input = {}) {
  if (!interaction?.repeated_tool?.outcomes?.includes(input.outcome) || !['continue', 'stop'].includes(input.outcome)) invalidInteractionResult();
  return { kind: 'repeated_tool', repeated_tool: { outcome: input.outcome } };
}

export const activeSession = (state) => state.sessions[state.activeID] || null;

export function liveDescriptor(existing = {}, summary = {}) {
  const resumable = Boolean(existing.resumable || summary.resumable);
  const durable = Boolean(
    existing.durable ||
    resumable ||
    summary.durable ||
    summary.resumable,
  );
  return {
    ...existing,
    ...summary,
    draft: existing.draft || '',
    durable,
    resumable,
    import_required: false,
    live: true,
    activation: 'live',
  };
}

export function unverifiedPersistedDescriptor(descriptor = {}) {
  return {
    ...descriptor,
    status: 'offline',
    active_turn_id: '',
    resumable: false,
    import_required: false,
    live: false,
    activation: 'detached',
    notice: 'Checking durable session metadata…',
  };
}

export function retainedClosedDescriptor(session = {}) {
  const importRequired = Boolean(session.import_required && !session.live);
  const resumable = Boolean(session.resumable && !importRequired);
  const durable = Boolean(session.durable || resumable || importRequired);
  if (!durable) return null;
  return {
    ...session,
    status: importRequired ? 'import required' : (resumable ? 'saved' : 'archived'),
    active_turn_id: '',
    attention: false,
    resumable,
    import_required: importRequired,
    durable: true,
    live: false,
    activation: 'detached',
    notice: importRequired
      ? 'Import this legacy session before continuing.'
      : (resumable
        ? 'Saved session. Select it to view history.'
        : 'This durable session is available as read-only history.'),
  };
}

export function canSubmitTurn(session) {
  if (!session?.live) return detachedAttachEligible(session);
  if (sessionActivation(session) !== 'live' || session.active_turn_id) return false;
  if (session.interactions?.length) return false;
  if (['running', 'stopping', 'offline', 'restoring', 'saved'].includes(session.status)) {
    return false;
  }
  const execution = executionDefaults(session.execution);
  if (['loading', 'updating'].includes(execution.status)) return false;
  return Boolean(execution.dispatchBlock?.context_only) || !execution.dispatchBlock;
}

export function canEditDraft(session) {
  if (!session?.live) {
    return detachedAttachEligible(session) || Boolean(
      session?.durable && session.import_required &&
      !['offline', 'restoring'].includes(session.status),
    );
  }
  return !['offline', 'restoring', 'saved'].includes(session.status);
}

export function modelRebindSelector(session) {
  if (!session?.live || session.active_turn_id) return '';
  if (['running', 'stopping', 'offline', 'restoring', 'saved'].includes(session.status)) {
    return '';
  }
  const execution = executionDefaults(session.execution);
  if (['loading', 'updating'].includes(execution.status)) return '';
  if (!execution.dispatchBlock || execution.dispatchBlock.context_only) return '';
  if (execution.dispatchBlock.code === 'model_binding_metadata_incompatible') return '';
  const selector = String(
    execution.dispatchBlock.selector || execution.model || '',
  ).trim();
  if (!selector) return '';
  return execution.models.some(
    (model) => String(model?.selector || '').trim() === selector,
  ) ? selector : '';
}

export function sessionMatchesQuery(session, query) {
  const normalized = String(query || '').trim().toLocaleLowerCase();
  if (!normalized) return true;
  return [
    session?.title,
    session?.workspace_label,
    session?.status,
    session?.git_branch,
  ].some((value) => String(value || '').toLocaleLowerCase().includes(normalized));
}

export const descriptors = (state) => Object.values(state.sessions).map(
  ({
    id,
    workspace_label: workspaceLabel,
    title,
    durable,
    resumable,
    import_required: importRequired,
    git_branch: gitBranch,
    created_at: createdAt,
    updated_at: updatedAt,
  }) => ({
    id,
    workspace_label: workspaceLabel,
    title,
    durable: Boolean(durable),
    resumable: Boolean(resumable),
    import_required: Boolean(importRequired),
    git_branch: gitBranch || '',
    created_at: createdAt || '',
    updated_at: updatedAt || '',
  }),
);
