const SAMPLE_RATE = 16000;
const MAX_RECORDING_SECONDS = 60;
const RECORDER_TYPE = 'audio/webm;codecs=opus';

export function encodeDraftWAV(samples) {
  const frames = Math.min(samples.length, SAMPLE_RATE * MAX_RECORDING_SECONDS);
  if (frames < SAMPLE_RATE / 10) throw new Error('The recording is too short.');
  const wav = new Uint8Array(44 + frames * 2);
  const header = new DataView(wav.buffer);
  wav.set([82, 73, 70, 70], 0); // RIFF
  header.setUint32(4, wav.length - 8, true);
  wav.set([87, 65, 86, 69, 102, 109, 116, 32], 8); // WAVEfmt
  header.setUint32(16, 16, true);
  header.setUint16(20, 1, true);
  header.setUint16(22, 1, true);
  header.setUint32(24, SAMPLE_RATE, true);
  header.setUint32(28, SAMPLE_RATE * 2, true);
  header.setUint16(32, 2, true);
  header.setUint16(34, 16, true);
  wav.set([100, 97, 116, 97], 36); // data
  header.setUint32(40, frames * 2, true);
  for (let index = 0; index < frames; index++) {
    const sample = Math.max(-1, Math.min(1, samples[index]));
    header.setInt16(44 + index * 2, sample * (sample < 0 ? 32768 : 32767), true);
  }
  return wav;
}

async function recordDraftAudio(signal, ready) {
  const stream = await navigator.mediaDevices.getUserMedia({
    audio: { channelCount: 1 },
    video: false,
  });
  let compressed;
  try {
    if (signal.aborted) return null;
    compressed = await new Promise((resolve, reject) => {
      const recorder = new MediaRecorder(stream, {
        mimeType: RECORDER_TYPE,
        audioBitsPerSecond: 64000,
      });
      const chunks = [];
      let bytes = 0;
      const finish = () => {
        if (recorder.state !== 'inactive') recorder.stop();
      };
      const abort = () => {
        finish();
        resolve(null);
      };
      recorder.ondataavailable = ({ data }) => {
        if (signal.aborted || data.size === 0) return;
        bytes += data.size;
        if (bytes > 2 << 20) {
          finish();
          reject(new Error('The recording is too large.'));
          return;
        }
        chunks.push(data);
      };
      recorder.start(250);
      signal.addEventListener('abort', abort, { once: true });
      const timeout = setTimeout(finish, MAX_RECORDING_SECONDS * 1000);
      // Clear the timer and remove the abort listener before media decoding.
      const cleanup = () => {
        clearTimeout(timeout);
        signal.removeEventListener('abort', abort);
      };
      recorder.onstop = () => {
        cleanup();
        resolve(new Blob(chunks, { type: RECORDER_TYPE }));
      };
      recorder.onerror = () => {
        cleanup();
        reject(new Error('Microphone recording failed.'));
      };
      ready(finish);
    });
  } finally {
    for (const track of stream.getTracks()) track.stop();
  }
  if (signal.aborted || !compressed) return null;
  const audio = new OfflineAudioContext(1, 1, SAMPLE_RATE);
  const decoded = await audio.decodeAudioData(await compressed.arrayBuffer());
  return signal.aborted ? null : encodeDraftWAV(decoded.getChannelData(0));
}

async function playAnswerAudio(load, signal, onPlaying) {
  const context = new AudioContext();
  const close = () => { if (context.state !== 'closed') void context.close().catch(() => {}); };
  signal.addEventListener('abort', close, { once: true });
  try {
    const resumed = context.resume();
    // resume can reject on cancellation before the synthesis promise returns.
    void resumed.catch(() => {});
    const audio = await load();
    if (signal.aborted) return;
    await resumed;
    const decoded = await context.decodeAudioData(audio.buffer);
    if (signal.aborted) return;
    const source = context.createBufferSource();
    source.buffer = decoded;
    source.connect(context.destination);
    await new Promise((resolve) => {
      const finish = () => {
        signal.removeEventListener('abort', finish);
        resolve();
      };
      signal.addEventListener('abort', finish, { once: true });
      source.onended = finish;
      source.start();
      onPlaying();
    });
  } finally {
    signal.removeEventListener('abort', close);
    close();
  }
}

/** Select the final assistant message of turns with a successful terminal event. */
export function readAloudMessages(messages, activity, enabled) {
  if (!enabled) return messages;
  const successful = new Set(activity
    .filter(({ kind, state }) => kind === 'turn' && state === 'completed')
    .map(({ turnID }) => turnID));
  const lastAssistant = new Map();
  for (const message of messages) {
    if (message.role === 'assistant' && successful.has(message.turnID)) {
      lastAssistant.set(message.turnID, message);
    }
  }
  return messages.map((message) => (
    message.completed && lastAssistant.get(message.turnID) === message &&
    !message.toolCalls.length && message.content.trim()
      ? { ...message, readAloud: true }
      : message
  ));
}

function failureMessage(error, fallback) {
  if (error?.name === 'NotAllowedError') return 'Microphone access was denied.';
  const message = String(error?.message || '');
  for (const known of [
    'Desktop speech budget reached.',
    'Desktop speech paused because usage or pricing is uncertain.',
    'The recording is too short.',
  ]) {
    if (message.endsWith(known)) return known;
  }
  return fallback;
}

export function createNativeSpeech({
  bridge, controls, appendDraft, capture = recordDraftAudio, play = playAnswerAudio,
}) {
  let sessionID;
  let canRecord = false;
  let canRead = false;
  let recording;
  let playback;
  let nextID = 0;

  function cancelRecording() {
    if (!recording) return;
    recording.controller.abort();
    void bridge.cancelSpeech('transcribe', recording.id).catch(() => {});
    recording = null;
    controls.record.textContent = 'Record draft';
    controls.cancelRecord.hidden = true;
    controls.recordStatus.textContent = '';
  }

  function cancelPlayback() {
    if (!playback) return;
    playback.controller.abort();
    void bridge.cancelSpeech('readAloud', playback.id).catch(() => {});
    playback = null;
    controls.stop.hidden = true;
    controls.playbackStatus.textContent = '';
  }

  function sync({ id, record, read }) {
    if (id !== sessionID || !record) cancelRecording();
    if (id !== sessionID || !read) cancelPlayback();
    if (id !== sessionID || !record) controls.recordStatus.textContent = '';
    if (id !== sessionID || !read) controls.playbackStatus.textContent = '';
    sessionID = id;
    canRecord = Boolean(id && record);
    canRead = Boolean(id && read);
    controls.recordGroup.hidden = !canRecord;
    controls.playbackGroup.hidden = !canRead;
  }

  async function record() {
    if (!canRecord) return;
    if (recording) {
      recording.finish?.();
      return;
    }
    const job = { id: ++nextID, controller: new AbortController() };
    const draftSession = sessionID;
    recording = job;
    controls.record.textContent = 'Starting microphone…';
    controls.cancelRecord.hidden = false;
    controls.recordStatus.textContent = '';
    try {
      const wav = await capture(job.controller.signal, (finish) => {
        if (recording !== job) return;
        job.finish = finish;
        controls.record.textContent = 'Stop and transcribe';
        controls.recordStatus.textContent = 'Recording up to 60 seconds.';
      });
      if (recording !== job || !wav) return;
      job.finish = null;
      controls.record.textContent = 'Transcribing…';
      controls.recordStatus.textContent = '';
      const result = await bridge.speech('transcribe', wav, job.id);
      if (recording !== job || !canRecord || sessionID !== draftSession) return;
      const text = typeof result?.text === 'string' ? result.text.trim() : '';
      if (!text) {
        controls.recordStatus.textContent = 'No speech was recognized.';
        return;
      }
      appendDraft(draftSession, text);
      controls.recordStatus.textContent = 'Transcript added to draft. Review before sending.';
    } catch (error) {
      if (recording === job) {
        controls.recordStatus.textContent = failureMessage(error, 'Could not transcribe the recording.');
      }
    } finally {
      if (recording === job) {
        recording = null;
        controls.record.textContent = 'Record draft';
        controls.cancelRecord.hidden = true;
      }
    }
  }

  async function readAloud(id, text) {
    if (!canRead || id !== sessionID || playback?.text === text) return;
    cancelPlayback();
    let characters = 0;
    for (const character of text) characters += /\p{Script=Han}/u.test(character) ? 2 : 1;
    if (!characters || characters > 10000) {
      controls.playbackStatus.textContent = 'This answer is empty or too long to read aloud.';
      return;
    }
    const job = { id: ++nextID, text, controller: new AbortController() };
    playback = job;
    controls.stop.hidden = false;
    controls.playbackStatus.textContent = 'Preparing audio…';
    try {
      await play(
        () => bridge.speech('readAloud', text, job.id),
        job.controller.signal,
        () => { if (playback === job) controls.playbackStatus.textContent = 'Reading aloud…'; },
      );
      if (playback === job) controls.playbackStatus.textContent = '';
    } catch (error) {
      if (playback === job) {
        controls.playbackStatus.textContent = failureMessage(error, 'Could not read the answer aloud.');
      }
    } finally {
      if (playback === job) {
        playback = null;
        controls.stop.hidden = true;
      }
    }
  }

  controls.record.onclick = record;
  controls.cancelRecord.onclick = cancelRecording;
  controls.stop.onclick = cancelPlayback;
  const stop = () => {
    cancelRecording();
    cancelPlayback();
  };
  return { sync, cancelRecording, stop, readAloud, project: readAloudMessages };
}
