import assert from 'node:assert/strict';
import test from 'node:test';

import {
  replayTranscriptFenceMatches,
  shouldReconnectReplayGapImmediately,
  synchronizeReplayGap,
} from '../../internal/webui/assets/replay.mjs';
import { initialState, reducer } from '../../internal/webui/assets/state.mjs';

function deferred() {
  let resolve;
  const promise = new Promise((resolvePromise) => {
    resolve = resolvePromise;
  });
  return { promise, resolve };
}

test('replay gap applies the live snapshot when transcript refresh is temporarily unavailable', async () => {
  const calls = [];
  let transcriptError = '';
  let state = reducer(initialState(), {
    type: 'SESSION_UPSERT',
    session: {
      id: 'session-1',
      workspace_label: 'project',
      cursor: 3,
      draft: 'keep this draft',
      replaying: true,
    },
  });

  const result = await synchronizeReplayGap({
    async loadSnapshot() {
      calls.push('snapshot');
      return {
        session: {
          id: 'session-1',
          workspace_label: 'project',
          status: 'waiting',
        },
        event_cursor: 41,
        messages: [],
        interactions: [{ request_id: 'question-1', kind: 'question' }],
        activity: [],
      };
    },
    applySnapshot(snapshot) {
      calls.push('apply-snapshot');
      state = reducer(state, {
        type: 'SESSION_SNAPSHOT',
        id: 'session-1',
        snapshot,
      });
    },
    async loadTranscript(snapshot) {
      calls.push('transcript');
      assert.equal(snapshot.event_cursor, 41);
      throw new Error('temporary transcript failure');
    },
    reportTranscriptFailure(error) {
      transcriptError = error.message;
    },
  });

  assert.deepEqual(calls, ['snapshot', 'apply-snapshot', 'transcript']);
  assert.deepEqual(await result.historyRefresh, { historyComplete: false });
  assert.equal(state.sessions['session-1'].cursor, 41);
  assert.equal(state.sessions['session-1'].draft, 'keep this draft');
  assert.equal(state.sessions['session-1'].interactions[0].request_id, 'question-1');
  assert.equal(state.sessions['session-1'].replaying, false);
  assert.equal(transcriptError, 'temporary transcript failure');
});

test('replay gap keeps snapshot failure fatal without reading stale history first', async () => {
  let transcriptLoads = 0;
  let reportedFailures = 0;

  await assert.rejects(synchronizeReplayGap({
    async loadSnapshot() {
      throw new Error('snapshot unavailable');
    },
    applySnapshot() {
      assert.fail('failed snapshot must not be applied');
    },
    async loadTranscript() {
      transcriptLoads += 1;
    },
    reportTranscriptFailure() {
      reportedFailures += 1;
    },
  }), /snapshot unavailable/);

  assert.equal(transcriptLoads, 0);
  assert.equal(reportedFailures, 0);
});

test('replay gap releases stream recovery before the transcript refresh completes', async () => {
  const transcript = deferred();
  const transcriptStarted = deferred();
  let cursor = 0;

  const synchronization = synchronizeReplayGap({
    async loadSnapshot() {
      return { event_cursor: 73 };
    },
    applySnapshot(snapshot) {
      cursor = snapshot.event_cursor;
    },
    loadTranscript() {
      transcriptStarted.resolve();
      return transcript.promise;
    },
  });

  await transcriptStarted.promise;
  let settled = false;
  void synchronization.then(() => {
    settled = true;
  });
  await Promise.resolve();
  try {
    assert.equal(settled, true);
    assert.equal(cursor, 73);
  } finally {
    transcript.resolve();
  }

  const recovery = await synchronization;
  assert.deepEqual(await recovery.historyRefresh, { historyComplete: true });
});

test('replay gap reports an intentionally discarded transcript as incomplete', async () => {
  let failures = 0;
  const recovery = await synchronizeReplayGap({
    async loadSnapshot() {
      return { event_cursor: 41 };
    },
    applySnapshot() {},
    async loadTranscript(snapshot) {
      assert.equal(snapshot.event_cursor, 41);
      return false;
    },
    reportTranscriptFailure() {
      failures += 1;
    },
  });

  assert.deepEqual(await recovery.historyRefresh, { historyComplete: false });
  assert.equal(failures, 0);
});

test('only the first consecutive replay gap reconnects without backoff', () => {
  assert.equal(shouldReconnectReplayGapImmediately(0), true);
  assert.equal(shouldReconnectReplayGapImmediately(1), false);
  assert.equal(shouldReconnectReplayGapImmediately(6), false);
});

test('replay transcript replacement only matches its snapshot cursor', () => {
  assert.equal(replayTranscriptFenceMatches(41, 41), true);
  assert.equal(replayTranscriptFenceMatches(41, 42), false);
  assert.equal(replayTranscriptFenceMatches(41, 40), false);
  assert.equal(replayTranscriptFenceMatches(-1, -1), false);
  assert.equal(replayTranscriptFenceMatches(41.5, 41.5), false);
});
