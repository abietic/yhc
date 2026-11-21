import assert from 'node:assert/strict';
import test from 'node:test';

import {
  createNativeSpeech, encodeDraftWAV, readAloudMessages,
} from '../../internal/webui/assets/native_speech.mjs';
import speech from '../speech.cjs';

function deferred() {
  let resolve;
  const promise = new Promise((accept) => { resolve = accept; });
  return { promise, resolve };
}

function controls() {
  return Object.fromEntries([
    'recordGroup', 'record', 'cancelRecord', 'recordStatus',
    'playbackGroup', 'stop', 'playbackStatus',
  ].map((name) => [name, {}]));
}

test('recording updates only the owning draft and does not call playback', async () => {
  const calls = [];
  const drafts = [];
  const ui = controls();
  const awaitingTranscription = deferred();
  const client = createNativeSpeech({
    controls: ui,
    bridge: {
      speech(action) {
        calls.push(action);
        return Promise.resolve({ text: '  draft text  ' });
      },
      cancelSpeech: async (action) => { calls.push(action + '-cancel'); },
    },
    appendDraft: (...args) => drafts.push(args),
    capture: async (_signal, ready) => {
      ready(() => {});
      return awaitingTranscription.promise;
    },
    play: async () => { assert.fail('unexpected playback'); },
  });
  client.sync({ id: 'a', record: true, read: false });
  assert.equal(ui.playbackGroup.hidden, true);
  await client.readAloud('a', 'not enabled');
  const first = ui.record.onclick();
  awaitingTranscription.resolve(new Uint8Array(44));
  await first;
  assert.deepEqual(calls, ['transcribe']);
  assert.deepEqual(drafts, [['a', 'draft text']]);
  assert.match(ui.recordStatus.textContent, /Review before sending/);
});

test('session changes and cancel discard a late transcription', async () => {
  const response = deferred();
  const started = deferred();
  const canceled = [];
  const ui = controls();
  const client = createNativeSpeech({
    controls: ui,
    bridge: {
      speech: () => { started.resolve(); return response.promise; },
      cancelSpeech: async (...args) => { canceled.push(args); },
    },
    appendDraft: () => assert.fail('late text reached a draft'),
    capture: async () => new Uint8Array(44),
  });
  client.sync({ id: 'a', record: true, read: false });
  const request = ui.record.onclick();
  await started.promise;
  client.sync({ id: 'b', record: true, read: false });
  response.resolve({ text: 'late result' });
  await request;
  assert.deepEqual(canceled, [['transcribe', 1]]);
  assert.equal(ui.record.textContent, 'Record draft');
});

test('projection and state changes do not trigger playback, and stop cancels the click', async () => {
  const response = deferred();
  const actions = [];
  const ui = controls();
  const first = { role: 'assistant', completed: true, turnID: 'ok', content: 'progress', toolCalls: [] };
  const final = { ...first, content: 'final answer' };
  const stopped = { ...first, turnID: 'cancelled' };
  const pending = { ...first, turnID: 'pending', completed: false };
  const tool = { ...first, turnID: 'tool', toolCalls: [{name: 'test tool'}] };
  const activity = [
    { kind: 'turn', state: 'completed', turnID: 'ok' },
    { kind: 'turn', state: 'completed', turnID: 'tool' },
    { kind: 'turn', state: 'stopped', turnID: 'cancelled' },
  ];
  const selected = readAloudMessages([first, final, stopped, pending, tool], activity, true);
  assert.deepEqual(selected.filter(message => message.readAloud).map(message => message.content), ['final answer']);
  const client = createNativeSpeech({
    controls: ui,
    bridge: {
      speech: async (...args) => { actions.push(args); await response.promise; },
      cancelSpeech: async (...args) => { actions.push(args); },
    },
    appendDraft: () => assert.fail('playback touched a draft'),
    play: async (load, signal) => {
      await load();
      assert.equal(signal.aborted, true);
    },
  });
  client.sync({ id: 'a', record: false, read: true });
  assert.equal(ui.recordGroup.hidden, true);
  assert.deepEqual(actions, []);
  const request = client.readAloud('a', selected[1].content);
  await client.readAloud('a', selected[1].content);
  assert.deepEqual(actions, [['readAloud', 'final answer', 1]]);
  ui.stop.onclick();
  response.resolve();
  await request;
  assert.deepEqual(actions, [['readAloud', 'final answer', 1], ['readAloud', 1]]);
  assert.equal(ui.stop.hidden, true);
});

test('encodes bounded 16 kHz mono PCM', () => {
  const samples = new Float32Array(1600);
  samples[0] = -1;
  samples[1] = 1;
  const wav = encodeDraftWAV(samples);
  const view = new DataView(wav.buffer);
  assert.equal(view.getUint32(24, true), 16000);
  assert.equal(view.getUint16(22, true), 1);
  assert.equal(view.getUint16(34, true), 16);
  assert.equal(view.getUint32(40, true), 3200);
  assert.equal(view.getInt16(44, true), -32768);
  assert.equal(view.getInt16(46, true), 32767);
  assert.throws(() => encodeDraftWAV(samples.subarray(100)), /too short/);
});

test('native transport restricts operations and aborts by exact operation and request', async () => {
  const calls = [];
  const client = speech.createSpeechRequests(async (url, options) => {
    calls.push({ url, options });
    if (url.endsWith('/read-aloud')) return new Response('audio', { headers: { 'content-type': 'audio/wav' } });
    return new Promise((_resolve, reject) =>
      options.signal.addEventListener('abort', () => reject(options.signal.reason), { once: true }),
    );
  });
  const bootstrap = { url: 'http://127.0.0.1:1234', token: 'fixture' };
  const capture = client.request(bootstrap, 'transcribe', new Uint8Array(44), 1);
  assert.deepEqual(await client.request(bootstrap, 'readAloud', 'answer', 2), new TextEncoder().encode('audio'));
  await assert.rejects(client.request({ ...bootstrap, url: 'https://api-docs.deepseek.com/' }, 'capabilities'), /must use a loopback/);
  await assert.rejects(client.request(bootstrap, 'createSession'), /Unsupported/);
  await assert.rejects(client.request(bootstrap, 'transcribe', new Uint8Array(2), 2), /Invalid microphone/);
  client.cancel('transcribe', 2);
  assert.equal(calls[0].options.signal.aborted, false);
  assert.equal(calls[0].options.headers.Authorization, 'Bearer fixture');
  client.cancel('transcribe', 1);
  await assert.rejects(capture, { name: 'AbortError' });
  assert.equal(calls.length, 2);
});
