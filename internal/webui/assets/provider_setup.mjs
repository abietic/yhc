function publicText(value) {
  if (typeof value !== 'string') return '';
  return value.replace(/[\u0000-\u001f\u007f]/g, '').trim();
}

function publicBaseURL(value) {
  const raw = publicText(value);
  if (!raw) return '';
  try {
    const parsed = new URL(raw);
    if (parsed.protocol !== 'http:' && parsed.protocol !== 'https:') return '';
    return `${parsed.protocol}//${parsed.host}${parsed.pathname}`;
  } catch {
    return '';
  }
}

export function createPendingWorkspaceRetry(createSession) {
  if (typeof createSession !== 'function') {
    throw new TypeError('createSession must be a function');
  }

  let pendingWorkspace = null;
  let inFlight = null;

  function defer(workspace) {
    if (inFlight || !workspace?.workspace_handle || !workspace?.workspace_label) return pendingWorkspace;
    pendingWorkspace = Object.freeze({
      workspace_handle: workspace.workspace_handle,
      workspace_label: workspace.workspace_label,
    });
    return pendingWorkspace;
  }

  function attempt(workspace) {
    if (inFlight) return inFlight;
    defer(workspace);
    const attemptedWorkspace = pendingWorkspace;
    if (!attemptedWorkspace) return Promise.resolve(null);
    let creation;
    try {
      creation = createSession(attemptedWorkspace);
    } catch (error) {
      creation = Promise.reject(error);
    }
    inFlight = Promise.resolve(creation)
      .then((result) => {
        if (pendingWorkspace === attemptedWorkspace) pendingWorkspace = null;
        return result;
      })
      .finally(() => {
        inFlight = null;
      });
    return inFlight;
  }

  return {
    attempt,
    defer,
    retry: () => attempt(pendingWorkspace),
    pending: () => pendingWorkspace,
    clear: () => { pendingWorkspace = null; },
  };
}

export function providerSetupProjection(surface, status = {}) {
  const desktop = surface === 'desktop';
  const web = surface === 'web';
  const secureStorageAvailable = status?.secureStorageAvailable !== false;
  if (!desktop) {
    return {
      setupAvailable: false,
      hostGuidance: web,
      configured: false,
      launchReady: false,
      secureStorageAvailable,
      submitDisabled: true,
      provider: '',
      model: '',
      baseURL: '',
      errorCode: '',
    };
  }
  return {
    setupAvailable: true,
    hostGuidance: false,
    configured: status?.configured === true,
    launchReady: status?.configured === true || status?.launchReady === true,
    secureStorageAvailable,
    submitDisabled: !secureStorageAvailable,
    provider: publicText(status?.provider),
    model: publicText(status?.model),
    baseURL: publicBaseURL(status?.baseURL),
    errorCode: publicText(status?.errorCode),
  };
}

export function shouldDeferWorkspaceForProvider(surface, setup) {
  return surface === 'desktop' && setup?.setupAvailable === true &&
    setup?.launchReady !== true;
}

export function executionForDisplay(session) {
  return session?.live === true ? session.execution : null;
}

function sessionTime(session) {
  const timestamp = Date.parse(session?.updated_at || session?.created_at || '');
  return Number.isNaN(timestamp) ? 0 : timestamp;
}

export function prioritizeSessionRows(sessions, {
  query = '', historyExpanded = false, limit = 12,
} = {}) {
  const ordered = (Array.isArray(sessions) ? sessions : [])
    .map((session, index) => ({ session, index }))
    .sort((left, right) => sessionTime(right.session) - sessionTime(left.session) || left.index - right.index)
    .map(({ session }) => session);
  if (query || historyExpanded) return { visible: ordered, hiddenCount: 0 };

  const selected = new Set();
  for (const session of ordered) {
    if (session?.live === true) selected.add(session);
  }
  const maximum = Number.isFinite(limit) && limit > 0 ? Math.floor(limit) : 12;
  for (const session of ordered) {
    if (session?.live === true || !session?.resumable || selected.size >= maximum) continue;
    selected.add(session);
  }
  const visible = ordered.filter((session) => selected.has(session));
  return { visible, hiddenCount: ordered.length - visible.length };
}
