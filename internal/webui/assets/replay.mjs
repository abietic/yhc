export async function synchronizeReplayGap({
  loadTranscript,
  loadSnapshot,
  applySnapshot,
  reportTranscriptFailure = () => {},
}) {
  const snapshot = await loadSnapshot();
  applySnapshot(snapshot);
  const historyRefresh = Promise.resolve()
    .then(() => loadTranscript(snapshot))
    .then((loaded) => ({ historyComplete: loaded !== false }))
    .catch((error) => {
      reportTranscriptFailure(error);
      return { historyComplete: false };
    });
  return { historyRefresh };
}

export function shouldReconnectReplayGapImmediately(retries) {
  return retries === 0;
}

export function replayTranscriptFenceMatches(expectedCursor, currentCursor) {
  return Number.isSafeInteger(expectedCursor) &&
    expectedCursor >= 0 &&
    currentCursor === expectedCursor;
}
