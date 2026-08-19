import {
  activeInteraction,
  activeSession,
  buildPermissionResolution,
  buildPlanResolution,
  buildQuestionResolution,
  buildRepeatedToolResolution,
  canImportDurableSession,
  canEditDraft,
  canSubmitTurn,
  descriptors,
  initialState,
  interactionDraft,
  liveDescriptor,
  modelRebindSelector,
  reducer,
  retainedClosedDescriptor,
  sessionMatchesQuery,
  unverifiedPersistedDescriptor,
} from './state.mjs';
import { activityPresentation } from './activity.mjs';
import {
  createDurableCatalog,
  createDurableHistoryLoader,
} from './catalog.mjs';
import {
  normalizeOpenSheet,
  sheetProjection,
  shouldCloseSheetOnEscape,
} from './layout.mjs';
import { renderMessageContent } from './markdown.mjs';
import {
  createPendingWorkspaceRetry,
  executionForDisplay,
  prioritizeSessionRows,
  providerSetupProjection,
  shouldDeferWorkspaceForProvider,
} from './provider_setup.mjs';
import {
  activateCreatedSession,
  createSessionCreationGate,
} from './session_creation.mjs';
import { createTransport } from './transport.mjs';
import { interactionViewModel } from './view_models.mjs';

const DESCRIPTOR_KEY = 'yhc.desktop.sessions.v1';
const DRAFT_KEY = 'yhc.desktop.drafts.v1';
const TRANSCRIPT_LIMIT = 100;

const streams = new Map();
const transcriptLoads = new Set();
const attachAttempts = new Map();
let state = initialState();
let transport;
let surface = '';
let persistenceEnabled = false;
let confirmationResolve = null;
let inspectorView = 'activity';
let openSheetKind = null;
let sheetTrigger = null;
let reviewRequestID = 0;
let executionRequestID = 0;
let catalog;
let catalogSearchTimer = null;
let historyExpanded = false;
let providerSetup = providerSetupProjection('', {});
let providerSetupBusy = false;

const $ = (id) => document.getElementById(id);
const timeline = $('timeline');
const sessionList = $('session-list');
const activity = $('activity');
const pendingWorkspace = createPendingWorkspaceRetry(createSessionForWorkspace);
const durableHistoryLoader = createDurableHistoryLoader((input) => api(
  'durableTranscriptPage', input,
));
const sessionCreation = createSessionCreationGate(createSession, () => render());

function timelinePosition() {
  return {
    activeID: state.activeID,
    height: timeline.scrollHeight,
    top: timeline.scrollTop,
    nearBottom:
      timeline.scrollHeight - timeline.scrollTop - timeline.clientHeight < 80,
  };
}

function dispatch(action, scrollMode = 'preserve') {
  const before = timelinePosition();
  state = reducer(state, action);
  if (persistenceEnabled) persist();
  render();
  if (before.activeID !== state.activeID || scrollMode === 'bottom') {
    timeline.scrollTop = timeline.scrollHeight;
    return;
  }
  if (scrollMode === 'prepend') {
    timeline.scrollTop = before.top + (timeline.scrollHeight - before.height);
    return;
  }
  timeline.scrollTop = before.nearBottom ? timeline.scrollHeight : before.top;
}

function rerenderPreservingTimeline() {
  const before = timelinePosition();
  render();
  if (before.activeID === state.activeID) {
    timeline.scrollTop = before.nearBottom ? timeline.scrollHeight : before.top;
  }
}

function persist() {
  localStorage.setItem(DESCRIPTOR_KEY, JSON.stringify({
    activeID: state.activeID,
    sessions: descriptors(state),
  }));
  localStorage.setItem(DRAFT_KEY, JSON.stringify(Object.fromEntries(
    Object.values(state.sessions)
      .filter((session) => session.draft)
      .map((session) => [session.id, session.draft]),
  )));
}

function api(operation, payload) {
  return transport.api(operation, payload);
}

function element(tag, className, text) {
  const value = document.createElement(tag);
  if (className) value.className = className;
  if (text !== undefined) value.textContent = text;
  return value;
}

function projectSheetRegion(region, hidden, expanded, inert = hidden) {
  region.classList.toggle('sheet-open', expanded);
  region.setAttribute('aria-hidden', String(hidden));
  region.inert = inert;
  if (expanded) {
    region.setAttribute('role', 'dialog');
    region.setAttribute('aria-modal', 'true');
  } else {
    region.removeAttribute('role');
    region.removeAttribute('aria-modal');
  }
}

function renderSheets() {
  const projection = sheetProjection(window.innerWidth, openSheetKind);
  if (projection.openSheetKind !== openSheetKind) {
    openSheetKind = projection.openSheetKind;
    if (!openSheetKind) sheetTrigger = null;
  }
  projectSheetRegion(
    $('navigation'),
    projection.navigationHidden,
    projection.navigationExpanded,
    projection.navigationInert,
  );
  projectSheetRegion(
    $('inspector'),
    projection.inspectorHidden,
    projection.inspectorExpanded,
    projection.inspectorInert,
  );
  $('conversation').inert = projection.conversationInert;
  $('navigation-toggle').setAttribute(
    'aria-expanded',
    String(projection.navigationExpanded),
  );
  $('inspector-toggle').setAttribute(
    'aria-expanded',
    String(projection.inspectorExpanded),
  );
  $('sheet-backdrop').hidden = !projection.backdropVisible;
  document.body.classList.toggle('sheet-open', projection.backdropVisible);
}

function openSheet(kind, trigger) {
  const nextKind = normalizeOpenSheet(window.innerWidth, kind);
  if (!nextKind) return;
  openSheetKind = nextKind;
  sheetTrigger = trigger || document.activeElement;
  renderSheets();
  const close = nextKind === 'navigation'
    ? $('navigation-close')
    : $('inspector-close');
  requestAnimationFrame(() => close.focus());
}

function closeSheet(returnFocus = true) {
  if (!openSheetKind) return false;
  const trigger = sheetTrigger;
  openSheetKind = null;
  sheetTrigger = null;
  renderSheets();
  if (returnFocus && trigger?.focus) {
    requestAnimationFrame(() => trigger.focus());
  }
  return true;
}

function handleViewportResize() {
  const previousKind = openSheetKind;
  const nextKind = normalizeOpenSheet(window.innerWidth, previousKind);
  if (nextKind === previousKind) {
    renderSheets();
    return;
  }
  openSheetKind = nextKind;
  sheetTrigger = null;
  renderSheets();
  const fallback = previousKind === 'navigation'
    ? $('new-session')
    : $('activity-tab');
  requestAnimationFrame(() => fallback.focus());
}

function render() {
  const current = activeSession(state);
  renderSheets();
  renderProviderSetup();
  renderSessionCreationControls();
  renderSessionList();
  renderActivity(current);
  renderReview(current);
  renderInteraction(current);
  renderLegacyImport(current);

  const loadEarlier = $('load-earlier');
  loadEarlier.hidden = !current?.transcriptHasMore;
  loadEarlier.disabled = !current?.transcriptHasMore ||
    transcriptLoads.has(current?.id);
  loadEarlier.textContent = transcriptLoads.has(current?.id)
    ? 'Loading…'
    : 'Load earlier';

  if (!current) {
    timeline.replaceChildren($('empty-state').content.cloneNode(true));
    renderEmptyState();
    $('session-title').textContent = 'New session';
    $('session-path').textContent = 'Choose a workspace';
    $('status').textContent = 'Offline';
    $('status').className = 'status';
    $('turn-state').textContent = 'Create a session to begin.';
    $('prompt').value = '';
    $('prompt').disabled = true;
    $('send').disabled = true;
    $('cancel').disabled = true;
    renderExecutionControls(null);
    return;
  }

  const busy = ['running', 'stopping'].includes(current.status);
  $('session-title').textContent = current.title || 'Untitled';
  $('session-path').textContent = current.workspace_label || 'Workspace unavailable';
  $('status').textContent = current.status || 'idle';
  $('status').className = `status ${busy ? 'busy' : ''}`;
  $('turn-state').textContent = current.notice ||
    (!current.live
      ? (current.resumable
        ? 'Select this saved session to resume it.'
        : 'This catalog entry cannot be resumed in Desktop.')
      : (busy ? 'Agent is working.' : 'Ready'));
  if ($('prompt').value !== (current.draft || '')) {
    $('prompt').value = current.draft || '';
  }
  $('prompt').disabled = !canEditDraft(current);
  $('send').disabled = !canSubmitTurn(current);
  $('cancel').disabled = !current.active_turn_id;
  renderExecutionControls(current);

  if (current.messages.length === 0) {
    timeline.replaceChildren(element(
      'div',
      'session-empty',
      current.live
        ? 'This session is ready. Describe the change you want to make.'
        : 'Select a resumable session or create a new workspace session.',
    ));
  } else {
    timeline.replaceChildren(...current.messages.map(renderMessage));
  }
}

function renderLegacyImport(session) {
  const panel = $('legacy-import-remediation');
  const button = $('legacy-import');
  const visible = Boolean(
    session?.import_required && !session.live &&
    !['offline', 'restoring'].includes(session.status),
  );
  const importing = session?.activation === 'importing';
  panel.hidden = !visible;
  button.disabled = !canImportDurableSession(session);
  button.textContent = importing ? 'Importing…' : 'Import and continue';
  $('legacy-import-copy').textContent = importing
    ? 'Copying the legacy history into canonical YHC storage.'
    : 'This legacy history is read-only until you confirm its previous producer is stopped.';
}

function renderSessionCreationControls() {
  const creating = sessionCreation.busy();
  const button = $('new-session');
  const label = button.querySelector('span');
  button.disabled = creating;
  button.setAttribute('aria-busy', String(creating));
  if (label) label.textContent = creating ? 'Creating session…' : 'New session';
}

function option(value, label, selected = false, disabled = false) {
  const item = document.createElement('option');
  item.value = value;
  item.textContent = label;
  item.selected = selected;
  item.disabled = disabled;
  return item;
}

function modelLabel(model) {
  const display = model?.display_name || model?.selector || 'Unnamed model';
  const details = [model?.provider, model?.api_model].filter(Boolean).join(' · ');
  return details ? `${display} — ${details}` : display;
}

function renderExecutionControls(session) {
  const model = $('model-select');
  const reasoning = $('reasoning-select');
  const permission = $('permission-mode-select');
  const block = $('model-block');
  const remediation = $('model-remediation');
  const rebind = $('model-rebind');
  const execution = executionForDisplay(session);
  $('execution-controls').hidden = !session;
  const controlled = Boolean(session?.live) && !session.active_turn_id &&
    !['running', 'stopping', 'offline', 'restoring', 'saved'].includes(session.status) &&
    !['loading', 'updating'].includes(execution?.status);
  model.disabled = !controlled;
  reasoning.disabled = !controlled || !execution?.reasoningEffortSupported;
  permission.disabled = !controlled;
  model.replaceChildren(...(execution?.models || []).map((entry) => option(
    entry.selector || '', modelLabel(entry), entry.selector === execution.model,
  )));
  reasoning.replaceChildren(...(execution?.reasoningEffortOptions || ['default']).map(
    (value) => option(value, value, value === execution?.reasoningEffort),
  ));
  const safeModes = execution?.permissionModeOptions || [];
  const permissionOptions = safeModes.map((value) => option(
    value, value, value === execution?.permissionMode,
  ));
  if (execution?.permissionMode && !safeModes.includes(execution.permissionMode)) {
    permissionOptions.unshift(option(
      execution.permissionMode,
      `Externally enabled: ${execution.permissionMode}`,
      true,
      true,
    ));
  }
  permission.replaceChildren(...permissionOptions);
  const remediationCopy = execution?.dispatchBlock?.remediation || execution?.error || '';
  const rebindSelector = modelRebindSelector(session);
  block.textContent = remediationCopy;
  block.hidden = !remediationCopy;
  rebind.hidden = !rebindSelector;
  rebind.disabled = !rebindSelector;
  remediation.hidden = !remediationCopy && !rebindSelector;
}

function providerDisplayName(provider) {
  return {
    anthropic: 'Anthropic',
    openai: 'OpenAI',
    google: 'Google Gemini',
    deepseek: 'DeepSeek',
    qwen: 'Qwen',
    ark: 'Volcengine Ark',
  }[provider] || provider || 'Not configured';
}

function renderProviderSetup() {
  const settings = $('provider-settings');
  const hostGuidance = $('provider-host-guidance');
  settings.hidden = !providerSetup.setupAvailable;
  settings.disabled = providerSetupBusy;
  const settingsLabel = providerSetup.configured
    ? 'Change provider'
    : 'Configure provider';
  settings.setAttribute('aria-label', settingsLabel);
  settings.title = settingsLabel;
  hostGuidance.hidden = !providerSetup.hostGuidance;
  $('provider-card').classList.toggle(
    'is-configured',
    Boolean(providerSetup.configured || providerSetup.launchReady),
  );

  if (providerSetup.hostGuidance) {
    $('provider-status').textContent = 'Host managed';
    $('provider-detail').textContent =
      'Use the Desktop App or host process to change credentials.';
  } else if (!surface) {
    $('provider-status').textContent = 'Starting…';
    $('provider-detail').textContent = 'Checking local provider setup.';
  } else if (providerSetup.configured) {
    $('provider-status').textContent = providerDisplayName(providerSetup.provider);
    $('provider-detail').textContent = [
      providerSetup.model || 'default',
      providerSetup.baseURL,
    ].filter(Boolean).join(' · ');
  } else if (providerSetup.errorCode === 'stored_profile_unavailable') {
    $('provider-status').textContent = 'Profile unavailable';
    $('provider-detail').textContent = 'Replace the saved provider setup to continue.';
  } else if (providerSetup.launchReady) {
    $('provider-status').textContent = 'Environment ready';
    $('provider-detail').textContent = 'Using the provider environment from launch.';
  } else {
    $('provider-status').textContent = 'Not configured';
    $('provider-detail').textContent = providerSetup.secureStorageAvailable
      ? 'Add an API key before your first prompt.'
      : 'Secure operating-system storage is unavailable.';
  }

  $('provider-save').disabled = providerSetupBusy || providerSetup.submitDisabled;
  $('provider-cancel').disabled = providerSetupBusy;
  for (const control of $('provider-form').elements) {
    if (!['provider-save', 'provider-cancel'].includes(control.id)) {
      control.disabled = providerSetupBusy || providerSetup.submitDisabled;
    }
  }
}

function renderEmptyState() {
  const providerButton = timeline.querySelector('[data-empty-action="provider"]');
  const workspaceButton = timeline.querySelector('[data-empty-action="workspace"]');
  const copy = timeline.querySelector('#empty-copy');
  if (!providerButton || !workspaceButton || !copy) return;

  providerButton.hidden = !providerSetup.setupAvailable;
  providerButton.textContent = providerSetup.configured
    ? 'Review provider setup'
    : 'Configure provider';
  providerButton.className = providerSetup.launchReady ? 'quiet' : 'primary';
  workspaceButton.hidden = surface === 'web';
  workspaceButton.disabled = sessionCreation.busy();
  workspaceButton.setAttribute('aria-busy', String(sessionCreation.busy()));
  workspaceButton.textContent = sessionCreation.busy()
    ? 'Creating session…'
    : 'Open workspace';
  workspaceButton.className = providerSetup.launchReady || providerSetup.hostGuidance
    ? 'primary'
    : 'quiet';
  copy.textContent = surface === 'web'
    ? 'Select a saved session to read its history or attach your next request.'
    : (providerSetup.hostGuidance
      ? 'Open a workspace on this host to start a coding session.'
    : (providerSetup.launchReady
      ? 'Your provider is ready. Open a folder to start coding.'
      : 'Connect a model provider, then open a folder to start coding.'));
  providerButton.onclick = () => openProviderDialog();
  workspaceButton.onclick = () => beginSessionCreation();
}

function sessionDotClass(session) {
  if (session.attention) return 'is-attention';
  if (['running', 'stopping'].includes(session.status)) return 'is-busy';
  if (session.live) return 'is-live';
  return 'is-saved';
}

function renderSessionList() {
  const query = $('session-filter').value;
  const matching = Object.values(state.sessions)
    .filter((session) => sessionMatchesQuery(
      session,
      query,
    ));
  const compact = prioritizeSessionRows(matching, { query, limit: 12 });
  const projection = prioritizeSessionRows(matching, {
    query,
    historyExpanded,
    limit: 12,
  });
  sessionList.replaceChildren(...projection.visible.map((session) => {
    const row = element(
      'div',
      `session-row ${session.id === state.activeID ? 'active' : ''}`,
    );
    const select = element('button', 'session-select');
    select.type = 'button';
    select.append(element('strong', '', session.title || 'Untitled'));
    select.append(element(
      'small',
      '',
      [session.status, session.workspace_label].filter(Boolean).join(' · ') || 'Unavailable',
    ));
    if (session.attention) {
      select.append(element('span', 'attention', 'Needs attention'));
    }
    select.onclick = () => {
      const selection = selectSession(session.id);
      if (openSheetKind === 'navigation') closeSheet();
      selection.catch(showError);
    };

    row.append(element('span', `session-dot ${sessionDotClass(session)}`));
    row.append(select);
    if (session.live) {
      const close = element('button', 'session-close', '×');
      close.type = 'button';
      close.title = 'Close session';
      close.setAttribute('aria-label', `Close ${session.title || 'session'}`);
      close.onclick = () => closeSession(session).catch(showError);
      row.append(close);
    }
    return row;
  }));

  const history = $('toggle-session-history');
  history.hidden = Boolean(query) || compact.hiddenCount === 0;
  history.textContent = historyExpanded
    ? 'Show recent sessions'
    : `Show ${compact.hiddenCount} older session${compact.hiddenCount === 1 ? '' : 's'}`;

  const catalogState = catalog?.snapshot();
  const loadMore = $('load-more-sessions');
  loadMore.hidden = !catalogState?.hasMore;
  loadMore.disabled = !catalogState?.hasMore || Boolean(catalogState?.loading);
  loadMore.textContent = catalogState?.loading ? 'Loading…' : 'Load more sessions';
  const catalogStatus = $('session-catalog-status');
  catalogStatus.textContent = catalogState?.error ||
    (catalogState?.loading ? 'Loading saved sessions…' : '');
}

function toolCallName(call) {
  return call.name || call.function?.name || 'Tool';
}

function renderMessage(message) {
  const card = element('article', `message ${message.role}`);
  card.append(element('span', 'label', message.role));
  if (message.toolCalls.length > 0) {
    card.append(element(
      'div',
      'tool-calls',
      message.toolCalls.map(toolCallName).join(', '),
    ));
  }
  if (message.reasoningContent) {
    const details = element('details', 'reasoning');
    details.append(element('summary', '', 'Reasoning'));
    details.append(element('div', '', message.reasoningContent));
    card.append(details);
  }
  const body = message.content ||
    (message.toolCalls.length > 0 ? 'Tool call requested.' : '');
  card.append(renderMessageContent(document, message.role, body));
  return card;
}

function renderActivity(current) {
  const items = current?.activity || [];
  if (inspectorView === 'activity') {
    $('activity-count').textContent = String(items.length);
  }
  const entries = items.slice(-25).reverse().flatMap((item) => {
    const presentation = activityPresentation(item);
    return presentation ? [{ item, presentation }] : [];
  });
  if (entries.length === 0) {
    const empty = element('div', 'activity-empty');
    empty.append(element('strong', '', 'No activity yet'));
    empty.append(element('span', '', 'Work progress will appear here.'));
    activity.replaceChildren(empty);
    return;
  }
  activity.replaceChildren(...entries.map(({ item, presentation }) => {
    const row = element('div', 'activity-item');
    row.dataset.kind = item.kind;
    row.dataset.state = item.state;
    row.append(element('span', 'activity-dot'));
    const body = element('div', 'activity-body');
    body.append(element('span', 'activity-type', presentation.title));
    body.append(element('span', 'activity-text', presentation.detail));
    row.append(body);
    return row;
  }));
}

function renderReview(current) {
  const review = current?.review || {};
  const inactive = !current?.live ||
    ['offline', 'restoring', 'saved'].includes(current.status);
  $('refresh-review').disabled = inactive || review.status === 'loading';
  $('ignore-whitespace').disabled = inactive || review.status === 'loading';
  if ($('ignore-whitespace').checked !== Boolean(review.ignoreWhitespace)) {
    $('ignore-whitespace').checked = Boolean(review.ignoreWhitespace);
  }
  if (inspectorView === 'review') {
    $('activity-count').textContent = String(review.sources?.length || 0);
  }

  const diff = $('review-diff');
  if (!current) {
    $('review-status').textContent =
      'Open a live session to inspect tracked worktree changes.';
    diff.textContent = '';
    return;
  }
  if (inactive) {
    $('review-status').textContent =
      'Resume this saved session before inspecting its worktree.';
    diff.textContent = '';
    return;
  }
  if (review.status === 'loading') {
    $('review-status').textContent = 'Reading tracked changes against HEAD…';
    diff.textContent = '';
    return;
  }
  if (review.status === 'error') {
    $('review-status').textContent = review.error || 'Review failed.';
    diff.textContent = '';
    return;
  }
  if (review.status !== 'ready') {
    $('review-status').textContent =
      'Inspect the current tracked worktree changes without running a shell.';
    diff.textContent = '';
    return;
  }
  const sources = Array.isArray(review.sources) ? review.sources : [];
  if (sources.length === 0) {
    $('review-status').textContent =
      'No Git worktree with a committed HEAD was found for this session.';
    diff.textContent = '';
    return;
  }
  const truncated = sources.some((source) => source.truncated);
  $('review-status').textContent = truncated
    ? 'Large diff: Git stopped at the output cap; the digest covers this prefix.'
    : `Tracked changes against HEAD · ${formatReviewTime(review.generatedAt)}`;
  diff.textContent = sources.map((source) => {
    const header = [
      `# ${current.workspace_label || 'Workspace'}`,
      `# ${source.base_ref || 'HEAD'} @ ${(source.head_ref || '').slice(0, 12)}`,
      `# ${source.diff_hash || ''}`,
    ].join('\n');
    return `${header}\n\n${source.diff || 'No tracked changes against HEAD.'}`;
  }).join('\n\n');
}

function formatReviewTime(value) {
  const timestamp = Date.parse(value || '');
  if (!Number.isFinite(timestamp)) return 'just now';
  return new Date(timestamp).toLocaleTimeString([], {
    hour: '2-digit',
    minute: '2-digit',
    second: '2-digit',
  });
}

function showInspectorView(view) {
  inspectorView = view === 'review' ? 'review' : 'activity';
  const reviewSelected = inspectorView === 'review';
  $('activity-view').hidden = reviewSelected;
  $('review-view').hidden = !reviewSelected;
  $('activity-tab').classList.toggle('active', !reviewSelected);
  $('activity-tab').setAttribute('aria-selected', String(!reviewSelected));
  $('review-tab').classList.toggle('active', reviewSelected);
  $('review-tab').setAttribute('aria-selected', String(reviewSelected));
  render();
  const current = activeSession(state);
  if (
    reviewSelected &&
    current?.live &&
    !['offline', 'restoring', 'saved'].includes(current.status)
  ) {
    void loadReview();
  }
}

async function loadReview() {
  const current = activeSession(state);
  if (
    !current?.live ||
    ['offline', 'restoring', 'saved'].includes(current.status)
  ) {
    return;
  }
  const requestID = ++reviewRequestID;
  const ignoreWhitespace = $('ignore-whitespace').checked;
  dispatch({
    type: 'SESSION_REVIEW_LOADING',
    id: current.id,
    requestID,
    ignoreWhitespace,
  });
  try {
    const response = await api('reviewDiff', {
      sessionID: current.id,
      ignoreWhitespace,
    });
    dispatch({
      type: 'SESSION_REVIEW_SUCCESS',
      id: current.id,
      requestID,
      response,
    });
  } catch (error) {
    dispatch({
      type: 'SESSION_REVIEW_FAILED',
      id: current.id,
      requestID,
      error: error.message,
    });
  }
}

const permissionScopeLabels = Object.freeze({
  allow_once: 'Allow once',
  allow_session: 'Allow for this session',
  allow_always: 'Always allow',
});

function interactionButton(label, className, onClick, disabled = false) {
  const button = element('button', className, label);
  button.type = 'button';
  button.disabled = disabled;
  button.onclick = onClick;
  return button;
}

function interactionDetails(interaction, review) {
  const details = element('details', 'interaction-details');
  details.append(element('summary', '', 'Technical details'));
  const rows = [['Request ID', interaction.requestID]];
  if (interaction.kind === 'plan_approval') {
    rows.push(['Plan revision', interaction.planApproval?.revision]);
    if (/^sha256:[a-f0-9]{64}$/.test(review?.digest || '')) {
      rows.push(['Reviewed digest', review.digest]);
    }
  }
  const list = element('dl');
  for (const [label, value] of rows) {
    if (value === '' || value === null || value === undefined) continue;
    list.append(element('dt', '', label), element('dd', '', String(value)));
  }
  details.append(list);
  return details;
}

function setInteractionDraft(sessionID, requestID, patch, rerender = false) {
  const action = {
    type: 'INTERACTION_DRAFT_UPDATE',
    id: sessionID,
    requestID,
    patch,
  };
  if (rerender) {
    dispatch(action);
    return;
  }
  state = reducer(state, action);
}

function renderInteraction(session) {
  const pending = activeInteraction(session);
  const panel = $('interaction-panel');
  panel.hidden = !pending;
  if (!pending) {
    $('interaction-body').replaceChildren();
    $('interaction-actions').replaceChildren();
    $('interaction-error').hidden = true;
    return;
  }

  const interaction = interactionViewModel(pending);
  const draft = interactionDraft(session, interaction.requestID);
  const queueSize = Math.max(0, (session.interactions?.length || 0) - 1);
  $('interaction-kind').textContent = interaction.kind.replaceAll('_', ' ');
  $('interaction-queue').hidden = queueSize === 0;
  $('interaction-queue').textContent = `${queueSize} more waiting`;
  $('interaction-body').replaceChildren();
  $('interaction-actions').replaceChildren();
  $('interaction-error').hidden = !draft.error;
  $('interaction-error').textContent = draft.error || '';

  if (!interaction.actionable) {
    renderUnknownInteraction(session, interaction);
    return;
  }
  switch (interaction.kind) {
    case 'permission':
      renderPermissionInteraction(session, interaction, draft, pending);
      break;
    case 'question':
      renderQuestionInteraction(session, interaction, draft, pending);
      break;
    case 'plan_approval':
      renderPlanInteraction(session, interaction, draft, pending);
      break;
    case 'repeated_tool':
      renderRepeatedToolInteraction(session, interaction, draft, pending);
      break;
    default:
      renderUnknownInteraction(session, interaction);
  }
}

function renderUnknownInteraction(session, interaction) {
  $('interaction-title').textContent = 'Interaction unavailable';
  $('interaction-body').replaceChildren(
    element('p', 'interaction-copy', 'This interaction type is not supported by this client. Reload session state before continuing.'),
    interactionDetails(interaction),
  );
  $('interaction-actions').replaceChildren(interactionButton(
    'Reload session',
    'primary',
    () => reloadInteractionSnapshot(session.id).catch(showError),
  ));
}

function renderPermissionInteraction(session, interaction, draft, pending) {
  const permission = interaction.permission;
  const resolving = draft.submitting;
  $('interaction-title').textContent = permission.toolLabel || 'Permission required';
  const body = [
    element(
      'p',
      'interaction-copy',
      permission.available
        ? permission.summary
        : 'Only a one-time approval is available because safe action details could not be projected.',
    ),
  ];
  if (permission.evidence.length > 0) {
    const evidence = element('dl', 'interaction-evidence');
    for (const item of permission.evidence) {
      evidence.append(
        element('dt', '', item.label),
        element('dd', '', item.value),
      );
    }
    body.push(evidence);
  }
  body.push(interactionDetails(interaction));
  $('interaction-body').replaceChildren(...body);

  const actions = [];
  for (const scope of permission.grantScopes) {
    const label = permissionScopeLabels[scope];
    if (!label) continue;
    actions.push(interactionButton(
      label,
      actions.length === 0 ? 'primary' : 'quiet',
      () => submitInteraction(
        session.id,
        interaction,
        buildPermissionResolution(pending, { decision: scope }),
      ).catch(showError),
      resolving,
    ));
  }
  actions.push(
    interactionButton(
      'Deny',
      'quiet',
      () => submitInteraction(
        session.id,
        interaction,
        buildPermissionResolution(pending, { decision: 'deny' }),
      ).catch(showError),
      resolving,
    ),
    interactionButton(
      'Cancel',
      'quiet',
      () => submitInteraction(
        session.id,
        interaction,
        buildPermissionResolution(pending, { decision: 'cancelled' }),
      ).catch(showError),
      resolving,
    ),
  );
  $('interaction-actions').replaceChildren(...actions);
}

function questionAnswer(draft, questionID) {
  const answer = draft.answers?.[questionID] || {};
  return {
    optionIDs: Array.isArray(answer.optionIDs) ? answer.optionIDs : [],
    text: String(answer.text || ''),
  };
}

function questionStepReady(question, answer) {
  const text = answer.text.trim();
  if (question.options.length === 0) return text.length > 0;
  if (question.multiSelect) return answer.optionIDs.length > 0 || text.length > 0;
  return (answer.optionIDs.length === 1) !== (text.length > 0);
}

function updateQuestionAnswer(session, interaction, question, answer) {
  const currentDraft = interactionDraft(
    state.sessions[session.id],
    interaction.requestID,
  );
  setInteractionDraft(session.id, interaction.requestID, {
    answers: { ...currentDraft.answers, [question.id]: answer },
    error: '',
  });
  const next = $('interaction-next');
  if (next) next.disabled = !questionStepReady(question, answer);
}

function renderQuestionInteraction(session, interaction, draft, pending) {
  const questions = interaction.question.questions;
  const step = Math.max(0, Math.min(Number(draft.step || 0), questions.length));
  const resolving = draft.submitting;
  $('interaction-title').textContent = step === questions.length
    ? 'Review answers'
    : (questions[step].header || 'Question');

  if (step === questions.length) {
    renderQuestionReview(session, interaction, draft);
  } else {
    renderQuestionStep(session, interaction, draft, questions[step], step);
  }

  const actions = [];
  if (step > 0) {
    actions.push(interactionButton('Back', 'quiet', () => {
      setInteractionDraft(session.id, interaction.requestID, { step: step - 1, error: '' }, true);
    }, resolving));
  }
  if (step < questions.length) {
    const answer = questionAnswer(draft, questions[step].id);
    const next = interactionButton(
      step === questions.length - 1 ? 'Review answers' : 'Next',
      'primary',
      () => setInteractionDraft(
        session.id,
        interaction.requestID,
        { step: step + 1, error: '' },
        true,
      ),
      resolving || !questionStepReady(questions[step], answer),
    );
    next.id = 'interaction-next';
    actions.push(next);
  } else {
    actions.push(interactionButton(
      'Submit answers',
      'primary',
      () => submitInteraction(
        session.id,
        interaction,
        buildQuestionResolution(pending, {
          outcome: 'submit',
          answers: questionResolutionAnswers(interaction, draft),
        }),
      ).catch(showError),
      resolving,
    ));
  }
  actions.push(
    interactionButton(
      'Discuss instead',
      'quiet',
      () => submitInteraction(
        session.id,
        interaction,
        buildQuestionResolution(pending, { outcome: 'discuss' }),
        true,
      ).catch(showError),
      resolving,
    ),
    interactionButton(
      'Cancel',
      'quiet',
      () => submitInteraction(
        session.id,
        interaction,
        buildQuestionResolution(pending, { outcome: 'cancel' }),
      ).catch(showError),
      resolving,
    ),
  );
  $('interaction-actions').replaceChildren(...actions);
}

function questionResolutionAnswers(interaction, draft) {
  return interaction.question.questions.map((question) => {
    const answer = questionAnswer(draft, question.id);
    return {
      question_id: question.id,
      ...(answer.optionIDs.length > 0 ? { option_ids: answer.optionIDs } : {}),
      ...(answer.text ? { text: answer.text } : {}),
    };
  });
}

function renderQuestionStep(session, interaction, draft, question, step) {
  const answer = questionAnswer(draft, question.id);
  const promptID = `interaction-question-${step}-prompt`;
  const helpID = `interaction-question-${step}-help`;
  const progress = element(
    'p',
    'question-progress',
    `Question ${step + 1} of ${interaction.question.questions.length}`,
  );
  const text = element('p', 'question-text', question.text);
  text.id = promptID;
  const help = element(
    'p',
    'question-help',
    question.options.length === 0
      ? 'Enter an answer to continue.'
      : (question.multiSelect
        ? 'Choose one or more options, type Other, or use both.'
        : 'Choose one option or type Other.'),
  );
  help.id = helpID;
  const options = element('div', 'question-options');
  options.setAttribute('role', 'group');
  options.setAttribute('aria-labelledby', promptID);
  options.setAttribute('aria-describedby', helpID);
  const optionControls = [];
  for (const optionModel of question.options) {
    const control = document.createElement('input');
    control.type = question.multiSelect ? 'checkbox' : 'radio';
    control.name = `question-${interaction.requestID}-${question.id}`;
    control.value = optionModel.id;
    control.checked = answer.optionIDs.includes(optionModel.id);
    control.onchange = () => {
      const latest = questionAnswer(
        interactionDraft(state.sessions[session.id], interaction.requestID),
        question.id,
      );
      let optionIDs;
      if (question.multiSelect) {
        optionIDs = control.checked
          ? [...new Set([...latest.optionIDs, optionModel.id])]
          : latest.optionIDs.filter((id) => id !== optionModel.id);
      } else {
        optionIDs = [optionModel.id];
        editor.value = '';
      }
      updateQuestionAnswer(session, interaction, question, {
        optionIDs,
        text: question.multiSelect ? latest.text : '',
      });
    };
    const copy = element('span', 'question-option-copy');
    copy.append(element('strong', '', optionModel.label));
    if (optionModel.description) {
      copy.append(element('span', 'question-option-description', optionModel.description));
    }
    const label = element('label', 'question-option');
    label.append(control, copy);
    options.append(label);
    optionControls.push(control);
  }

  const other = element('label', 'question-other');
  other.append(element(
    'span',
    '',
    question.options.length === 0 ? 'Your answer' : 'Other',
  ));
  const editor = document.createElement('textarea');
  editor.rows = 2;
  editor.value = answer.text;
  editor.setAttribute('aria-describedby', helpID);
  editor.placeholder = question.options.length === 0
    ? 'Type your answer…'
    : 'Type another answer…';
  editor.oninput = () => {
    const latest = questionAnswer(
      interactionDraft(state.sessions[session.id], interaction.requestID),
      question.id,
    );
    if (!question.multiSelect) {
      for (const control of optionControls) control.checked = false;
    }
    updateQuestionAnswer(session, interaction, question, {
      optionIDs: question.multiSelect ? latest.optionIDs : [],
      text: editor.value,
    });
  };
  other.append(editor);
  $('interaction-body').replaceChildren(
    progress,
    text,
    help,
    options,
    other,
    interactionDetails(interaction),
  );
}

function renderQuestionReview(session, interaction, draft) {
  const list = element('ol', 'question-review');
  interaction.question.questions.forEach((question, index) => {
    const answer = questionAnswer(draft, question.id);
    const selected = question.options
      .filter((optionModel) => answer.optionIDs.includes(optionModel.id))
      .map((optionModel) => optionModel.label);
    if (answer.text.trim()) selected.push(answer.text.trim());
    const copy = element('div');
    copy.append(
      element('strong', '', question.header || `Question ${index + 1}`),
      element('p', '', selected.join(', ') || 'No answer'),
    );
    const edit = interactionButton('Edit', 'quiet', () => {
      setInteractionDraft(session.id, interaction.requestID, { step: index, error: '' }, true);
    });
    const row = element('li');
    row.append(copy, edit);
    list.append(row);
  });
  $('interaction-body').replaceChildren(list, interactionDetails(interaction));
}

function renderPlanInteraction(session, interaction, draft, pending) {
  const planApproval = interaction.planApproval;
  const review = draft.planReview;
  const resolving = draft.submitting;
  const targetMode = planApproval.targetModes.includes(draft.targetMode)
    ? draft.targetMode
    : (planApproval.targetModes[0] || '');
  $('interaction-title').textContent = `Review plan revision ${planApproval.revision}`;
  const body = [];
  if (!planApproval.reviewAvailable) {
    body.push(element('p', 'plan-review-status', 'Plan review is unavailable. Reload the session before continuing.'));
  } else if (review.status === 'loading') {
    body.push(element('p', 'plan-review-status', 'Loading the exact plan revision…'));
  } else if (review.status === 'error') {
    body.push(element('p', 'plan-review-status', review.error || 'Plan review could not be loaded.'));
  } else if (review.status === 'ready') {
    const reviewPanel = element('div', 'plan-review');
    reviewPanel.append(renderMessageContent(document, 'assistant', review.content));
    body.push(reviewPanel);
    if (!targetMode) {
      body.push(element(
        'p',
        'plan-review-status',
        'No valid post-approval mode was provided. Reload the session before continuing.',
      ));
    } else {
      const target = element('label', 'plan-target');
      target.append(element('span', '', 'After approval'));
      const selector = document.createElement('select');
      selector.replaceChildren(...planApproval.targetModes.map((mode) => option(
        mode,
        mode === 'bypassPermissions' ? 'Continue without permission prompts' : `Return to ${mode} mode`,
        mode === targetMode,
      )));
      selector.onchange = () => setInteractionDraft(
        session.id,
        interaction.requestID,
        { targetMode: selector.value, error: '' },
        true,
      );
      target.append(selector);
      body.push(target);
      const feedback = element('label', 'plan-feedback');
      feedback.append(element('span', '', 'Revision feedback'));
      const editor = document.createElement('textarea');
      editor.rows = 3;
      editor.value = draft.feedback || '';
      editor.placeholder = 'Describe what should change in the plan…';
      editor.oninput = () => {
        setInteractionDraft(
          session.id,
          interaction.requestID,
          { feedback: editor.value, error: '' },
        );
        const revise = $('interaction-revise');
        if (revise) revise.disabled = !editor.value.trim();
      };
      feedback.append(editor);
      body.push(feedback);
    }
    body.push(interactionDetails(interaction, review));
  }
  $('interaction-body').replaceChildren(...body);

  const actions = [];
  if (!planApproval.reviewAvailable || (review.status === 'ready' && !targetMode)) {
    actions.push(interactionButton(
      'Reload session',
      'primary',
      () => reloadInteractionSnapshot(session.id).catch(showError),
      resolving,
    ));
  } else if (review.status === 'error') {
    actions.push(interactionButton(
      'Retry review',
      'primary',
      () => loadInteractionPlan(session.id, interaction.requestID).catch(showError),
      resolving,
    ));
  }
  if (review.status === 'ready' && targetMode) {
    const revise = interactionButton(
      'Request changes',
      'quiet',
      () => {
        const currentDraft = interactionDraft(
          state.sessions[session.id],
          interaction.requestID,
        );
        return submitInteraction(
          session.id,
          interaction,
          buildPlanResolution(pending, {
            outcome: 'revise',
            target_mode: targetMode,
            feedback: currentDraft.feedback,
            confirmed: false,
          }, currentDraft.planReview),
        ).catch(showError);
      },
      resolving || !String(draft.feedback || '').trim(),
    );
    revise.id = 'interaction-revise';
    actions.push(
      interactionButton(
        targetMode === 'bypassPermissions' ? 'Approve and bypass prompts' : 'Approve plan',
        'primary',
        () => approvePlanInteraction(
          session,
          interaction,
          draft,
          pending,
          targetMode,
        ).catch(showError),
        resolving,
      ),
      revise,
      interactionButton(
        'Cancel plan',
        'quiet',
        () => submitInteraction(
          session.id,
          interaction,
          buildPlanResolution(pending, {
            outcome: 'cancel',
            target_mode: targetMode,
            confirmed: false,
          }, draft.planReview),
        ).catch(showError),
        resolving,
      ),
    );
  }
  $('interaction-actions').replaceChildren(...actions);
  if (planApproval.reviewAvailable && review.status === 'idle') {
    void loadInteractionPlan(session.id, interaction.requestID).catch(showError);
  }
}

async function approvePlanInteraction(
  session,
  interaction,
  draft,
  pending,
  targetMode,
) {
  let confirmed = false;
  if (targetMode === 'bypassPermissions') {
    confirmed = await requestConfirmation(
      'Continue without permission prompts?',
      'This plan requests bypassPermissions mode. Future tool actions in this run may proceed without individual approval prompts.',
    );
    if (!confirmed) return;
  }
  await submitInteraction(
    session.id,
    interaction,
    buildPlanResolution(pending, {
      outcome: 'approve',
      target_mode: targetMode,
      confirmed,
    }, draft.planReview),
  );
}

function renderRepeatedToolInteraction(session, interaction, draft, pending) {
  const repeatedTool = interaction.repeatedTool;
  const resolving = draft.submitting;
  $('interaction-title').textContent = 'Repeated tool call detected';
  $('interaction-body').replaceChildren(
    element('p', 'repeated-warning', repeatedTool.explanation),
    element('p', 'interaction-copy', `Attempt ${repeatedTool.attempt}. Continue only if repeating this exact call is intentional.`),
    interactionDetails(interaction),
  );
  const actions = repeatedTool.outcomes.flatMap((outcome) => {
    const label = outcome === 'continue'
      ? 'Continue once'
      : (outcome === 'stop' ? 'Stop and change strategy' : '');
    if (!label) return [];
    return [interactionButton(
      label,
      outcome === 'continue' ? 'primary' : 'quiet',
      () => submitInteraction(
        session.id,
        interaction,
        buildRepeatedToolResolution(pending, { outcome }),
      ).catch(showError),
      resolving,
    )];
  });
  $('interaction-actions').replaceChildren(...actions);
}

function selectWorkspace() {
  if (surface === 'web') return Promise.resolve(null);
  return transport.selectWorkspace();
}

function providerSetupMessage(reason = '') {
  if (reason) return reason;
  if (!providerSetup.secureStorageAvailable) {
    return 'Secure operating-system credential storage is unavailable on this machine.';
  }
  if (providerSetup.errorCode === 'stored_profile_unavailable') {
    return 'The saved provider profile could not be opened. Replace it to continue.';
  }
  return providerSetup.configured
    ? 'Enter an API key to replace the current provider launch profile.'
    : 'Choose a provider and save an API key to start your first session.';
}

function openProviderDialog(reason = '') {
  if (!providerSetup.setupAvailable) {
    $('turn-state').textContent =
      'Provider setup is managed by the Desktop App or host process.';
    return;
  }
  const providers = new Set(['anthropic', 'openai', 'google', 'deepseek', 'qwen', 'ark']);
  $('provider-select').value = providers.has(providerSetup.provider)
    ? providerSetup.provider
    : 'openai';
  $('provider-model').value = providerSetup.model || 'default';
  $('provider-base-url').value = providerSetup.baseURL || '';
  $('provider-api-key').value = '';
  const status = $('provider-dialog-status');
  status.textContent = providerSetupMessage(reason);
  status.classList.toggle('error', Boolean(reason || providerSetup.errorCode));
  renderProviderSetup();
  if (!$('provider-dialog').open) $('provider-dialog').showModal();
  requestAnimationFrame(() => (
    providerSetup.configured ? $('provider-api-key') : $('provider-select')
  ).focus());
}

function closeProviderDialog() {
  if (providerSetupBusy) return;
  $('provider-api-key').value = '';
  if ($('provider-dialog').open) $('provider-dialog').close();
}

function providerConfigurationFailure(error) {
  const message = String(error?.message || 'Provider setup failed.');
  if (/close all live sessions/i.test(message)) {
    return 'Close every live session before changing the provider.';
  }
  if (/encryption|secure storage|safe storage/i.test(message)) {
    return 'Secure operating-system credential storage is unavailable.';
  }
  if (/valid base url/i.test(message)) return 'Enter a valid HTTP(S) base URL.';
  if (/valid model/i.test(message)) return 'Enter a valid model name.';
  if (/provider key/i.test(message)) return 'Enter a valid API key.';
  return message;
}

async function retryAfterProviderSetup() {
  if (pendingWorkspace.pending()) {
    await sessionCreation.begin(() => pendingWorkspace.retry());
  }
}

async function configureProvider(event) {
  event.preventDefault();
  if (providerSetupBusy || !providerSetup.setupAvailable) return;
  const apiKeyInput = $('provider-api-key');
  let apiKey = apiKeyInput.value;
  const submission = {
    provider: $('provider-select').value,
    model: $('provider-model').value,
    baseURL: $('provider-base-url').value,
    apiKey,
  };
  providerSetupBusy = true;
  $('provider-dialog-status').textContent = 'Encrypting provider profile and restarting backend…';
  $('provider-dialog-status').classList.remove('error');
  renderProviderSetup();
  let configured = false;
  try {
    const status = await transport.configureProvider(submission);
    providerSetup = providerSetupProjection(surface, status);
    configured = true;
  } catch (error) {
    const status = $('provider-dialog-status');
    status.textContent = providerConfigurationFailure(error);
    status.classList.add('error');
  } finally {
    submission.apiKey = '';
    apiKey = '';
    apiKeyInput.value = '';
    providerSetupBusy = false;
    render();
  }
  if (!configured) return;
  closeProviderDialog();
  try {
    await retryAfterProviderSetup();
  } catch (error) {
    openProviderDialog(`Provider saved, but the session could not start: ${error.message}`);
  }
}

async function createSessionForWorkspace(workspace) {
  if (!workspace?.workspace_handle || !workspace?.workspace_label) {
    throw new Error('Workspace selection is required.');
  }
  const summary = await api('createSession', {
    workspaceHandle: workspace.workspace_handle,
  });
  return activateCreatedSession(summary, {
    activate(created) {
      prepareSessionHydration(created, '', false);
      dispatch({ type: 'SESSION_SELECT', id: created.id }, 'bottom');
    },
    async hydrate(created) {
      await hydratePreparedSession(created);
      if (inspectorView === 'review' && state.activeID === created.id) {
        await loadReview();
      }
    },
  });
}

async function createSession() {
  const workspace = await selectWorkspace();
  if (!workspace) return;
  if (shouldDeferWorkspaceForProvider(surface, providerSetup)) {
    pendingWorkspace.defer(workspace);
    openProviderDialog();
    return null;
  }
  try {
    return await pendingWorkspace.attempt(workspace);
  } catch (error) {
    if (surface === 'desktop') {
      openProviderDialog(`The session could not start: ${error.message}`);
      return null;
    }
    throw error;
  }
}

async function loadExecutionSettings(sessionID, mutation = null) {
  const session = state.sessions[sessionID];
  if (!session?.live || session.status === 'restoring') return;
  const requestID = ++executionRequestID;
  dispatch({
    type: 'EXECUTION_SETTINGS_LOADING',
    id: sessionID,
    requestID,
    mutation: Boolean(mutation),
  });
  try {
    const response = mutation
      ? await api('updateExecutionSetting', { sessionID, ...mutation })
      : await api('getExecutionSettings', { sessionID });
    dispatch({
      type: 'EXECUTION_SETTINGS_SUCCESS',
      id: sessionID,
      requestID,
      response,
    });
  } catch (error) {
    dispatch({
      type: 'EXECUTION_SETTINGS_FAILED',
      id: sessionID,
      requestID,
      error: error.message,
    });
  }
}

async function selectSession(id) {
  dispatch({ type: 'SESSION_SELECT', id }, 'bottom');
  const session = state.sessions[id];
  if (!session) return;
  if (!session.live) {
    await loadTranscript(id, true);
    return;
  }
  if (session.status !== 'restoring') {
    startStream(id);
  }
  if (inspectorView === 'review') await loadReview();
}

async function closeSession(session) {
  const confirmed = await requestConfirmation(
    'Close session?',
    `Close "${session.title || 'this session'}"? ` +
      (session.resumable
        ? 'The durable transcript will remain available to resume later.'
        : (session.durable
          ? 'The durable transcript will remain read-only in Desktop.'
          : 'This empty session has no durable transcript yet.')),
    'Close',
  );
  if (!confirmed) return;
  if (session.live) {
    await api('closeSession', { sessionID: session.id });
  }
  attachAttempts.delete(session.id);
  stopStream(session.id);
  const retained = retainedClosedDescriptor(session);
  if (retained) {
    dispatch({
      type: 'SESSION_UPSERT',
      session: retained,
    });
    return;
  }
  dispatch({ type: 'SESSION_REMOVE', id: session.id }, 'bottom');
}

function updateDraft(value) {
  const current = activeSession(state);
  if (!current) return;
  attachAttempts.delete(current.id);
  state = reducer(state, {
    type: 'SESSION_DRAFT',
    id: current.id,
    draft: value,
  });
  if (persistenceEnabled) persist();
}

async function send(event) {
  event.preventDefault();
  const current = activeSession(state);
  const prompt = $('prompt').value.trim();
  if (!current || !prompt || !canSubmitTurn(current)) return;
  if (current.live) {
    try {
      await api('startTurn', {
        sessionID: current.id,
        prompt,
        clientTurnID: crypto.randomUUID(),
      });
    } catch (error) {
      throw sessionScopedError(error, current.id);
    }
    dispatch({ type: 'SESSION_DRAFT', id: current.id, draft: '' });
    return;
  }

  let attempt = attachAttempts.get(current.id);
  if (!attempt || attempt.prompt !== prompt) {
    attempt = { prompt, clientTurnID: crypto.randomUUID() };
    attachAttempts.set(current.id, attempt);
  }
  dispatch({ type: 'ATTACH_STARTED', id: current.id });
  try {
    const response = await api('attachTurn', {
      sessionID: current.id,
      prompt: attempt.prompt,
      clientTurnID: attempt.clientTurnID,
    });
    if (response?.status === 'turn_accepted') {
      dispatch({
        type: 'ATTACH_ACCEPTED',
        id: current.id,
        clientTurnID: attempt.clientTurnID,
        response,
      });
    } else if (response?.status === 'interaction_required') {
      dispatch({
        type: 'ATTACH_INTERACTION_REQUIRED',
        id: current.id,
        clientTurnID: attempt.clientTurnID,
        response,
      });
    } else {
      throw new Error('Attach turn returned an invalid response.');
    }
    const attached = state.sessions[current.id];
    if (!attached?.live || attached.activation === 'attaching') {
      throw new Error('Attach turn returned a mismatched response.');
    }
    attachAttempts.delete(current.id);
    await synchronizeSession(response.session, attached.draft, true);
  } catch (error) {
    dispatch({ type: 'ATTACH_FAILED', id: current.id, error: error.message });
    throw sessionScopedError(error, current.id);
  }
}

async function refreshImportedDurableSession(sessionID) {
  const catalogRefresh = catalog.reset(catalog.snapshot().search);
  const page = await api('listDurableSessions', {
    search: sessionID,
    limit: 100,
  });
  const rows = Array.isArray(page?.sessions)
    ? page.sessions.filter((row) => row?.id === sessionID)
    : [];
  if (rows.length !== 1 || rows[0].import_required || !rows[0].resumable) {
    throw new Error('Imported session did not pass canonical catalog admission.');
  }
  dispatch({ type: 'DURABLE_IMPORT_COMPLETED', id: sessionID });
  dispatch({ type: 'DURABLE_SESSION_PAGE', sessions: rows });
  await catalogRefresh;
}

async function importDurableSession() {
  const current = activeSession(state);
  if (!canImportDurableSession(current)) return;
  const sessionID = current.id;
  const confirmed = await requestConfirmation(
    'Import legacy session?',
    'Continue only after every older agent process that could write this session has stopped. Import copies the history into canonical YHC storage and leaves the legacy bytes unchanged.',
    'Import and continue',
  );
  if (!confirmed) return;

  dispatch({ type: 'DURABLE_IMPORT_STARTED', id: sessionID });
  try {
    try {
      await api('importDurableSession', {
        sessionID,
        confirmLegacyStopped: true,
      });
    } catch (error) {
      if (error.code !== 'legacy_import_not_required') throw error;
    }
    await refreshImportedDurableSession(sessionID);
    if (state.activeID === sessionID) await loadTranscript(sessionID, true);
  } catch (error) {
    const session = state.sessions[sessionID];
    if (session?.activation === 'importing') {
      dispatch({
        type: 'DURABLE_IMPORT_FAILED',
        id: sessionID,
        error: error.message,
      });
    } else {
      dispatch({
        type: 'SESSION_NOTICE',
        id: sessionID,
        status: 'archived',
        notice: `${error.message} Reload Desktop to refresh imported history.`,
      });
    }
    throw sessionScopedError(error, sessionID);
  }
}

async function cancel() {
  const current = activeSession(state);
  if (!current?.active_turn_id) return;
  await api('cancelTurn', {
    sessionID: current.id,
    turnID: current.active_turn_id,
    mode: 'immediate',
    reason: 'User cancelled from client',
  });
}

async function reloadInteractionSnapshot(sessionID) {
  const snapshot = await api('snapshot', { sessionID });
  dispatch({ type: 'SESSION_SNAPSHOT', id: sessionID, snapshot });
}

async function loadInteractionPlan(sessionID, requestID) {
  dispatch({ type: 'INTERACTION_PLAN_LOADING', id: sessionID, requestID });
  try {
    const review = await api('getInteractionPlan', { sessionID, requestID });
    const session = state.sessions[sessionID];
    if (!session?.interactions.some((item) => item.request_id === requestID)) return;
    dispatch({
      type: 'INTERACTION_PLAN_SUCCESS',
      id: sessionID,
      requestID,
      review,
    });
  } catch (error) {
    const session = state.sessions[sessionID];
    if (!session?.interactions.some((item) => item.request_id === requestID)) return;
    dispatch({
      type: 'INTERACTION_PLAN_FAILED',
      id: sessionID,
      requestID,
      error: error.message,
    });
    throw error;
  }
}

async function submitInteraction(
  sessionID,
  interaction,
  result,
  focusComposer = false,
) {
  const requestID = interaction.requestID;
  dispatch({ type: 'INTERACTION_SUBMITTING', id: sessionID, requestID });
  try {
    const response = await api('resolveInteraction', {
      sessionID,
      requestID,
      result,
    });
    if (!response?.accepted) throw new Error('Interaction was not accepted.');
    if (focusComposer) requestAnimationFrame(() => $('prompt').focus());
  } catch (error) {
    const session = state.sessions[sessionID];
    if (!session?.interactions.some((item) => item.request_id === requestID)) return;
    dispatch({
      type: 'INTERACTION_SUBMIT_FAILED',
      id: sessionID,
      requestID,
      error: error.message,
    });
    throw error;
  }
}

function readPersisted() {
  try {
    const saved = JSON.parse(
      localStorage.getItem(DESCRIPTOR_KEY) || '{"sessions":[]}',
    );
    const drafts = JSON.parse(localStorage.getItem(DRAFT_KEY) || '{}');
    return {
      activeID: typeof saved.activeID === 'string' ? saved.activeID : null,
      sessions: Array.isArray(saved.sessions)
        ? saved.sessions
          .filter((item) => item?.id && item?.workspace_label)
          .map((item) => ({
            ...item,
            draft: typeof drafts[item.id] === 'string' ? drafts[item.id] : '',
          }))
        : [],
    };
  } catch {
    return { activeID: null, sessions: [] };
  }
}

async function loadTranscript(sessionID, replace = false) {
  const current = state.sessions[sessionID];
  if (!current) return;
  if (!current.live) {
    if (durableHistoryLoader.pending(sessionID)) return;
    transcriptLoads.add(sessionID);
    dispatch({
      type: 'SESSION_NOTICE',
      id: sessionID,
      status: 'restoring',
      notice: 'Loading saved history…',
    });
    try {
      const result = await durableHistoryLoader.load({
        sessionID,
        cursor: replace ? '' : current.transcriptNextCursor,
        limit: TRANSCRIPT_LIMIT,
      });
      dispatch(
        {
          type: 'SESSION_TRANSCRIPT_PAGE',
          id: result.sessionID,
          page: result.page,
          replace,
        },
        result.sessionID === state.activeID
          ? (replace ? 'bottom' : 'prepend')
          : 'preserve',
      );
      const loaded = state.sessions[result.sessionID];
      if (loaded && !loaded.live) {
        dispatch({
          type: 'SESSION_NOTICE',
          id: result.sessionID,
          status: loaded.resumable ? 'saved' : 'archived',
          notice: loaded.resumable
            ? 'Saved session. Send a message to continue.'
            : 'This durable session is available as read-only history.',
        });
      }
    } catch (error) {
      const loaded = state.sessions[sessionID];
      if (loaded && !loaded.live) {
        dispatch({
          type: 'SESSION_NOTICE',
          id: sessionID,
          status: loaded.resumable ? 'saved' : 'archived',
          notice: `Saved history could not be loaded: ${error.message}`,
        });
      }
      return;
    } finally {
      transcriptLoads.delete(sessionID);
      if (state.activeID === sessionID) rerenderPreservingTimeline();
    }
    return;
  }
  if (transcriptLoads.has(sessionID)) return;
  transcriptLoads.add(sessionID);
  render();
  try {
    const page = await api('transcriptPage', {
      sessionID,
      cursor: replace ? '' : current?.transcriptNextCursor,
      limit: TRANSCRIPT_LIMIT,
    });
    dispatch(
      { type: 'SESSION_TRANSCRIPT_PAGE', id: sessionID, page, replace },
      sessionID === state.activeID
        ? (replace ? 'bottom' : 'prepend')
        : 'preserve',
    );
  } finally {
    transcriptLoads.delete(sessionID);
    if (state.activeID === sessionID) rerenderPreservingTimeline();
  }
}

function prepareSessionHydration(summary, draft = '', durable = true) {
  dispatch({
    type: 'SESSION_UPSERT',
    session: {
      ...summary,
      status: 'restoring',
      draft,
      durable,
      resumable: durable,
      live: true,
    },
  });
}

async function hydratePreparedSession(summary) {
  try {
    await loadTranscript(summary.id, true);
    const snapshot = await api('snapshot', { sessionID: summary.id });
    dispatch({ type: 'SESSION_SNAPSHOT', id: summary.id, snapshot });
  } catch (error) {
    const current = state.sessions[summary.id];
    dispatch({
      type: 'SESSION_NOTICE',
      id: summary.id,
      status: current?.status === 'restoring'
        ? (summary.status || 'idle')
        : '',
      notice: `Session restored without complete history: ${error.message}`,
    });
  }
  await loadExecutionSettings(summary.id);
  startStream(summary.id);
}

async function synchronizeSession(summary, draft = '', durable = true) {
  prepareSessionHydration(summary, draft, durable);
  await hydratePreparedSession(summary);
}

async function restore() {
  const saved = readPersisted();
  const descriptorsByID = new Map(
    saved.sessions.map((descriptor) => [descriptor.id, descriptor]),
  );
  let liveByID = new Map();
  try {
    const response = await api('listSessions');
    const liveSessions = Array.isArray(response?.sessions)
      ? response.sessions
      : [];
    liveByID = new Map(liveSessions.map((session) => [session.id, session]));
    for (const session of liveSessions) {
      if (!descriptorsByID.has(session.id)) {
        descriptorsByID.set(session.id, { ...session, draft: '' });
      }
    }
  } catch (error) {
    $('turn-state').textContent =
      `Could not inspect live sessions: ${error.message}`;
  }
  for (const session of liveByID.values()) {
    const existing = descriptorsByID.get(session.id) || {};
    descriptorsByID.set(session.id, liveDescriptor(existing, session));
  }
  const restored = [...descriptorsByID.values()];
  for (const descriptor of restored) {
    const liveSession = liveByID.get(descriptor.id);
    dispatch({
      type: 'SESSION_UPSERT',
      session: liveSession
        ? { ...descriptor, ...liveSession, status: 'restoring', live: true }
        : unverifiedPersistedDescriptor(descriptor),
    });
  }
  if (saved.activeID && state.sessions[saved.activeID]) {
    dispatch({ type: 'SESSION_SELECT', id: saved.activeID }, 'bottom');
  }
  await Promise.all(restored
    .filter((descriptor) => liveByID.has(descriptor.id))
    .map((descriptor) => synchronizeSession(
      liveByID.get(descriptor.id),
      descriptor.draft,
      Boolean(descriptor.durable || descriptor.resumable),
    )));
  const catalogReady = await catalog.reset('');
  const current = activeSession(state);
  if (catalogReady && current?.durable && !current.live) {
    await loadTranscript(current.id, true);
  }
}

function completeConfirmation(value) {
  const resolve = confirmationResolve;
  confirmationResolve = null;
  if ($('confirm-dialog').open) $('confirm-dialog').close();
  resolve?.(value);
}

function requestConfirmation(title, copy, actionLabel = 'Continue') {
  if (confirmationResolve) {
    return Promise.reject(new Error('A confirmation is already open.'));
  }
  $('confirm-title').textContent = title;
  $('confirm-copy').textContent = copy;
  $('confirm-accept').textContent = actionLabel;
  return new Promise((resolve) => {
    confirmationResolve = resolve;
    $('confirm-dialog').showModal();
    $('confirm-accept').focus();
  });
}

function startStream(sessionID) {
  if (streams.has(sessionID)) return;
  const stream = {
    opening: false,
    retries: 0,
    stopped: false,
    timer: null,
  };
  streams.set(sessionID, stream);
  void openStream(sessionID, stream);
}

async function openStream(sessionID, stream) {
  if (
    streams.get(sessionID) !== stream ||
    stream.stopped ||
    stream.opening
  ) {
    return;
  }
  stream.opening = true;
  try {
    const result = await transport.startEvents(
      sessionID,
      state.sessions[sessionID]?.cursor || 0,
    );
    if (result.status === 'gap') {
      const recovered = await recoverReplayGap(sessionID);
      if (!recovered) throw new Error('event replay recovery failed');
      scheduleReconnect(sessionID, stream, true);
      return;
    }
    stream.retries = 0;
  } catch (error) {
    dispatch({
      type: 'SESSION_NOTICE',
      id: sessionID,
      notice: `Event stream disconnected: ${error.message}`,
    });
    scheduleReconnect(sessionID, stream);
  } finally {
    stream.opening = false;
  }
}

function scheduleReconnect(sessionID, stream, immediate = false) {
  if (streams.get(sessionID) !== stream || stream.stopped || stream.timer) {
    return;
  }
  const delay = immediate ? 0 : Math.min(500 * (2 ** stream.retries), 10_000);
  stream.retries = Math.min(stream.retries + 1, 6);
  stream.timer = setTimeout(() => {
    stream.timer = null;
    void openStream(sessionID, stream);
  }, delay);
}

function stopStream(sessionID) {
  const stream = streams.get(sessionID);
  if (!stream) return;
  stream.stopped = true;
  clearTimeout(stream.timer);
  streams.delete(sessionID);
  void transport.stopEvents(sessionID).catch(() => {});
}

async function recoverReplayGap(sessionID) {
  dispatch({ type: 'SESSION_REPLAY_GAP', id: sessionID });
  try {
    await loadTranscript(sessionID, true);
    const snapshot = await api('snapshot', { sessionID });
    dispatch({ type: 'SESSION_SNAPSHOT', id: sessionID, snapshot });
    return true;
  } catch (error) {
    dispatch({
      type: 'SESSION_NOTICE',
      id: sessionID,
      status: 'error',
      notice: `Replay recovery failed: ${error.message}`,
    });
    return false;
  }
}

function handleEventStream(payload) {
  const stream = streams.get(payload.sessionID);
  if (!stream) return;
  if (payload.kind === 'event') {
    stream.retries = 0;
    dispatch({ type: 'EVENT', event: payload.event });
    if (['terminal', 'turn.finished'].includes(payload.event.type)) {
      void loadExecutionSettings(payload.sessionID);
    }
    return;
  }
  if (payload.kind === 'error') {
    dispatch({
      type: 'SESSION_NOTICE',
      id: payload.sessionID,
      notice: `Event stream error: ${payload.error || 'unknown error'}`,
    });
  }
  scheduleReconnect(payload.sessionID, stream);
}

function showError(error) {
  const scopedID = typeof error?.yhcSessionID === 'string'
    ? error.yhcSessionID
    : '';
  if (scopedID && !state.sessions[scopedID]) return;
  const current = scopedID
    ? state.sessions[scopedID]
    : activeSession(state);
  if (current) {
    dispatch({
      type: 'SESSION_NOTICE',
      id: current.id,
      notice: error.message,
    });
  } else {
    $('turn-state').textContent = error.message;
  }
}

function sessionScopedError(error, sessionID) {
  const scoped = error instanceof Error
    ? error
    : new Error(String(error || 'Session request failed.'));
  scoped.yhcSessionID = sessionID;
  return scoped;
}

async function bootstrapApp() {
  transport = createTransport();
  const info = await transport.getInfo();
  if (!info || info.protocolVersion !== 2) {
    throw new Error('Backend protocol mismatch');
  }
  surface = info.surface;
  try {
    providerSetup = providerSetupProjection(
      surface,
      await transport.getProviderStatus(),
    );
  } catch {
    providerSetup = providerSetupProjection(surface, {
      configured: false,
      errorCode: 'status_unavailable',
    });
  }
  const openWeb = $('open-web');
  openWeb.hidden = info.surface !== 'desktop' || !info.webAvailable;
  openWeb.onclick = () => transport.openWeb().catch(showError);
  transport.onEventStream(handleEventStream);
  transport.onBackendExit((payload) => {
    for (const id of [...streams.keys()]) stopStream(id);
    for (const session of Object.values(state.sessions)) {
      dispatch({
        type: 'SESSION_UPSERT',
        session: {
          ...session,
          live: false,
          status: 'offline',
          notice: payload.error || 'Backend stopped.',
        },
      });
    }
  });
  catalog = createDurableCatalog({
    fetchPage: (input) => api('listDurableSessions', input),
    applyPage: (sessions, replace) => dispatch({
      type: 'DURABLE_SESSION_PAGE', sessions, replace,
    }),
    reportState: () => render(),
  });
  await restore();
  persistenceEnabled = true;
  persist();
  render();
}

function beginSessionCreation() {
  if (surface === 'web') return;
  const creation = sessionCreation.begin();
  if (openSheetKind === 'navigation') closeSheet();
  creation.catch(showError);
}

$('new-session').onclick = () => beginSessionCreation();
$('provider-settings').onclick = () => openProviderDialog();
$('toggle-session-history').onclick = () => {
  historyExpanded = !historyExpanded;
  renderSessionList();
};
$('session-filter').oninput = () => {
  renderSessionList();
  clearTimeout(catalogSearchTimer);
  catalogSearchTimer = setTimeout(() => {
    void catalog?.reset($('session-filter').value);
  }, 200);
};
$('load-more-sessions').onclick = () => void catalog?.loadMore();
$('navigation-toggle').onclick = (event) => {
  openSheet('navigation', event.currentTarget);
};
$('inspector-toggle').onclick = (event) => {
  openSheet('inspector', event.currentTarget);
};
$('navigation-close').onclick = () => closeSheet();
$('inspector-close').onclick = () => closeSheet();
$('sheet-backdrop').onclick = () => closeSheet();
$('composer').onsubmit = (event) => send(event).catch(showError);
$('cancel').onclick = () => cancel().catch(showError);
$('load-earlier').onclick = () => {
  const current = activeSession(state);
  if (current?.transcriptHasMore) {
    loadTranscript(current.id).catch(showError);
  }
};
$('activity-tab').onclick = () => showInspectorView('activity');
$('review-tab').onclick = () => showInspectorView('review');
$('refresh-review').onclick = () => void loadReview();
$('ignore-whitespace').onchange = () => void loadReview();
$('prompt').oninput = () => updateDraft($('prompt').value);
$('prompt').onkeydown = (event) => {
  if (event.key === 'Enter' && (event.metaKey || event.ctrlKey)) {
    $('composer').requestSubmit();
  }
};
$('model-select').onchange = () => {
  const current = activeSession(state);
  if (current) {
    void loadExecutionSettings(current.id, {
      field: 'model', value: $('model-select').value,
    });
  }
};
$('model-rebind').onclick = () => {
  const current = activeSession(state);
  const selector = modelRebindSelector(current);
  if (!current || !selector) return;
  void loadExecutionSettings(current.id, {
    field: 'model', value: selector,
  });
};
$('legacy-import').onclick = () => importDurableSession().catch(showError);
$('reasoning-select').onchange = () => {
  const current = activeSession(state);
  if (current) {
    void loadExecutionSettings(current.id, {
      field: 'reasoning_effort', value: $('reasoning-select').value,
    });
  }
};
$('permission-mode-select').onchange = () => {
  const current = activeSession(state);
  if (current) {
    void loadExecutionSettings(current.id, {
      field: 'permission_mode', value: $('permission-mode-select').value,
    });
  }
};
$('provider-form').onsubmit = (event) => void configureProvider(event);
$('provider-cancel').onclick = () => closeProviderDialog();
$('provider-dialog').oncancel = (event) => {
  event.preventDefault();
  closeProviderDialog();
};
$('confirm-form').onsubmit = (event) => {
  event.preventDefault();
  completeConfirmation(true);
};
$('confirm-cancel').onclick = () => completeConfirmation(false);
$('confirm-dialog').oncancel = (event) => {
  event.preventDefault();
  completeConfirmation(false);
};
document.addEventListener('keydown', (event) => {
  const dialogOpen = $('provider-dialog').open ||
    $('confirm-dialog').open;
  if (
    shouldCloseSheetOnEscape(event.key, dialogOpen, openSheetKind) &&
    closeSheet()
  ) {
    event.preventDefault();
    event.stopPropagation();
  }
});
window.addEventListener('resize', handleViewportResize);

render();
bootstrapApp().catch((error) => {
  $('turn-state').textContent = error.message;
  $('status').textContent = 'Error';
});
