export function suggestionKey(snapshot) {
  if (!snapshot?.enabled || !snapshot.sessionID || snapshot.input !== '') return '';
  return JSON.stringify([snapshot.sessionID, snapshot.fence]);
}

function suggestionText(response, sessionID) {
  const text = response?.suggestion;
  if (response?.session_id !== sessionID || typeof text !== 'string' || text !== text.trim() ||
      new TextEncoder().encode(text).length >= 100 || /[\u0000-\u001f\u007f-\u009f\uD800-\uDFFF]/u.test(text)) return '';
  return text;
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
        value = { text, busy: false, status: text ? 'Suggestion only. Review before sending.' : 'No suggestion available. You can write your own next step.' };
      } catch {
        if (generation !== serial || capturedKey !== suggestionKey(snapshot())) { refresh(); return; }
        value = { text: '', busy: false, status: 'Suggestion unavailable. Your draft is unchanged.' };
      }
      publish(value);
    },
  };
}
