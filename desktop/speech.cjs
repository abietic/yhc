const MAX_CAPTURE_BYTES = 44 + 60 * 16000 * 2;
const MAX_PLAYBACK_BYTES = 16 << 20;

function speechEndpoint(action, input) {
  switch (action) {
    case 'capabilities':
      return { path: '/v1/speech' };
    case 'transcribe':
      if (!(input instanceof Uint8Array) || input.byteLength < 44 || input.byteLength > MAX_CAPTURE_BYTES) {
        throw new Error('Invalid microphone recording.');
      }
      return { path: '/v1/speech/transcribe', body: input, type: 'audio/wav' };
    case 'readAloud': {
      if (typeof input !== 'string' || !input.isWellFormed() || !input.trim() || input.length > 20000) {
        throw new Error('Invalid text for read aloud.');
      }
      return { path: '/v1/speech/read-aloud', body: JSON.stringify({ text: input }), type: 'application/json' };
    }
    default:
      throw new Error('Unsupported Desktop speech action.');
  }
}

function createSpeechRequests(fetchRequest = fetch) {
  const pending = new Map();
  const actions = new Set(['transcribe', 'readAloud']);

  async function request(bootstrap, action, input, requestID) {
    if (!bootstrap) throw new Error('Desktop backend is unavailable.');
    const origin = new URL(bootstrap.url);
    if (origin.protocol !== 'http:' || !['127.0.0.1', '[::1]'].includes(origin.hostname) ||
        origin.username || origin.password || origin.href !== origin.origin + '/') {
      throw new Error('Desktop speech backend must use a loopback HTTP origin.');
    }
    const { path, body, type } = speechEndpoint(action, input);
    const tracked = actions.has(action);
    if (tracked && (!Number.isSafeInteger(requestID) || requestID < 1 || pending.has(action))) {
      throw new Error('A Desktop speech request is already running or invalid.');
    }
    const controller = new AbortController();
    if (tracked) pending.set(action, { requestID, controller });
    const timeout = setTimeout(() => controller.abort(), tracked ? 125_000 : 5_000);
    try {
      const response = await fetchRequest(origin.origin + path, {
        method: type ? 'POST' : 'GET',
        headers: {
          Authorization: 'Bearer ' + bootstrap.token,
          ...(type ? { 'Content-Type': type } : {}),
        },
        body,
        redirect: 'error',
        signal: controller.signal,
      });
      if (!response.ok) {
        const code = (await response.json().catch(() => ({})))?.error?.code;
        switch (code) {
          case 'speech_budget_exhausted':
            throw new Error('Desktop speech budget reached.');
          case 'speech_usage_unknown':
            throw new Error('Desktop speech paused because usage or pricing is uncertain.');
          default:
            throw new Error('Desktop speech request failed.');
        }
      }
      if (action !== 'readAloud') return await response.json();
      if (response.headers.get('content-type') !== 'audio/wav' || !response.body) {
        throw new Error('Invalid read aloud audio.');
      }
      const chunks = [];
      let bytes = 0;
      for await (const chunk of response.body) {
        bytes += chunk.byteLength;
        if (bytes > MAX_PLAYBACK_BYTES) throw new Error('Read aloud audio is too large.');
        chunks.push(chunk);
      }
      if (!bytes) throw new Error('Read aloud audio is empty.');
      return new Uint8Array(Buffer.concat(chunks, bytes));
    } finally {
      clearTimeout(timeout);
      if (pending.get(action)?.controller === controller) pending.delete(action);
    }
  }

  function cancel(action, requestID) {
    const active = pending.get(action);
    if (!active || active.requestID !== requestID) return;
    pending.delete(action);
    active.controller.abort();
  }

  function cancelAll() {
    for (const active of pending.values()) active.controller.abort();
    pending.clear();
  }

  return { request, cancel, cancelAll };
}

module.exports = { createSpeechRequests };
