export function suggestionKey(snapshot) {
  if (!snapshot?.enabled || !snapshot.sessionID || snapshot.input !== '') return '';
  return JSON.stringify([snapshot.sessionID, snapshot.fence]);
}

function suggestionText(response, sessionID) {
  const text = response?.suggestion;
  if (response?.status !== undefined && response.status !== 'available') return '';
  if (response?.session_id !== sessionID || typeof text !== 'string' || text !== text.trim() ||
      new TextEncoder().encode(text).length >= 100 || /[\u0000-\u001f\u007f-\u009f\uD800-\uDFFF]/u.test(text)) return '';
  return text;
}

function suggestionFeedback(response) {
  if (response?.status === 'suppressed') {
    const reasons = {
      disabled: 'Next-step suggestions are disabled. Enable prompt_suggestions and start a new session.',
      no_context: 'No conversation context yet. Send a message first.',
      early_conversation: 'Next-step suggestions need at least two assistant messages.',
      plan_mode: 'Next-step suggestions are paused in Plan mode.',
      pending_permission: 'Answer the pending permission request before asking for a suggestion.',
      goal_active: 'Next-step suggestions are paused while a Goal is active.',
      busy: 'Wait for the current turn to finish before asking for a suggestion.',
      in_flight: 'A suggestion request is still finishing. Try again shortly.',
    };
    return Object.hasOwn(reasons, response.reason) ? reasons[response.reason] : 'Next-step suggestions are unavailable in this session.';
  }
  const statuses = {
    empty: 'No next step suggested. You can write your own.',
    filtered: 'No suitable next-step suggestion was returned.',
    timed_out: 'The suggestion request timed out. Try again.',
    cancelled: 'The suggestion request was cancelled.',
    stale: 'The suggestion was discarded because the model route changed. Try again.',
    failed: 'The suggestion request failed. Try again.',
  };
  return Object.hasOwn(statuses, response?.status) ? statuses[response.status] : 'No suggestion available. You can write your own next step.';
}

// Refresh never requests, writes a draft, or sends a turn. The caller accepts
// one text value as an ordinary undoable composer edit.
export function createPromptSuggestion({ snapshot, request, publish }) {
  let serial = 0;
  let key = '';
  let value = { text: '', busy: false, status: '' };
  const clear = () => { serial++; key = ''; value = { text: '', busy: false, status: '' }; publish(value); };
  const refresh = () => { if (key && key !== suggestionKey(snapshot())) clear(); };
  return {
    refresh,
    dismiss: clear,
    current() { refresh(); return value; },
    accept() {
      refresh();
      const text = value.text;
      clear();
      return text;
    },
    async request() {
      refresh();
      const current = snapshot();
      const capturedKey = suggestionKey(current);
      if (!capturedKey || value.busy) return;
      key = capturedKey;
      const generation = ++serial;
      value = { text: '', busy: true, status: 'Thinking of a next step…' };
      publish(value);
      try {
        const response = await request({ sessionID: current.sessionID });
        if (generation !== serial || capturedKey !== suggestionKey(snapshot())) { refresh(); return; }
        const text = suggestionText(response, current.sessionID);
        value = { text, busy: false, status: text ? 'Suggestion only. Review before sending.' : suggestionFeedback(response) };
      } catch {
        if (generation !== serial || capturedKey !== suggestionKey(snapshot())) { refresh(); return; }
        value = { text: '', busy: false, status: 'Suggestion unavailable. Your draft is unchanged.' };
      }
      publish(value);
    },
  };
}
