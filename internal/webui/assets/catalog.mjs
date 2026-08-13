export function createDurableCatalog({
  fetchPage,
  applyPage,
  reportState,
  limit = 100,
}) {
  let generation = 0;
  let search = '';
  let cursor = '';
  let hasMore = false;
  let loading = false;
  let error = '';

  function snapshot() {
    return { generation, search, cursor, hasMore, loading, error };
  }

  function report() {
    reportState(snapshot());
  }

  async function request(replace) {
    if (loading) return false;
    const ownedGeneration = generation;
    loading = true;
    error = '';
    report();
    try {
      const page = await fetchPage({
        cursor: replace ? '' : cursor,
        limit,
        search,
      });
      if (ownedGeneration !== generation) return false;
      applyPage(Array.isArray(page.sessions) ? page.sessions : [], replace);
      cursor = typeof page.next_cursor === 'string' ? page.next_cursor : '';
      hasMore = Boolean(page.has_more && cursor);
      return true;
    } catch (cause) {
      if (ownedGeneration === generation) {
        error = String(cause?.message || cause);
      }
      return false;
    } finally {
      if (ownedGeneration === generation) {
        loading = false;
        report();
      }
    }
  }

  function reset(nextSearch = '') {
    generation += 1;
    search = String(nextSearch).trim();
    cursor = '';
    hasMore = false;
    loading = false;
    error = '';
    return request(true);
  }

  function loadMore() {
    return hasMore ? request(false) : Promise.resolve(false);
  }

  return { reset, loadMore, snapshot };
}

export function createDurableHistoryLoader(fetchPage) {
  if (typeof fetchPage !== 'function') {
    throw new TypeError('durable transcript fetch must be a function');
  }
  const flights = new Map();

  function load(input = {}) {
    const sessionID = typeof input.sessionID === 'string'
      ? input.sessionID.trim()
      : '';
    if (!sessionID) {
      return Promise.reject(new TypeError('durable session id required'));
    }
    const cursor = typeof input.cursor === 'string' ? input.cursor : '';
    const limit = Number.isInteger(input.limit) && input.limit > 0
      ? input.limit
      : 100;
    const key = JSON.stringify([sessionID, cursor, limit]);
    const existing = flights.get(key);
    if (existing) return existing;

    const requested = Promise.resolve()
      .then(() => fetchPage({ sessionID, cursor, limit }))
      .then((page) => ({ sessionID, page }));
    let flight;
    flight = requested.finally(() => {
      if (flights.get(key) === flight) flights.delete(key);
    });
    flights.set(key, flight);
    return flight;
  }

  return {
    load,
    pending: (sessionID) => {
      const normalized = String(sessionID || '').trim();
      return [...flights.keys()].some((key) => JSON.parse(key)[0] === normalized);
    },
  };
}
