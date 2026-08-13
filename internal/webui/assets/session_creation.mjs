export async function activateCreatedSession(summary, { activate, hydrate }) {
  activate(summary);
  await hydrate(summary);
  return summary;
}

export function createSessionCreationGate(run, onBusyChange = () => {}) {
  let busy = false;
  let inFlight = null;

  function begin(nextRun = run) {
    if (inFlight) return inFlight;

    busy = true;
    try {
      onBusyChange(true);
    } catch (error) {
      busy = false;
      throw error;
    }

    let result;
    try {
      result = nextRun();
    } catch (error) {
      result = Promise.reject(error);
    }

    const pending = Promise.resolve(result).finally(() => {
      if (inFlight !== pending) return;
      inFlight = null;
      busy = false;
      onBusyChange(false);
    });
    inFlight = pending;
    return pending;
  }

  return {
    begin,
    busy: () => busy,
  };
}
