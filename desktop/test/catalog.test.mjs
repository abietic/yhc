import test from 'node:test';
import assert from 'node:assert/strict';

import {
  createDurableCatalog,
  createDurableHistoryLoader,
} from '../../internal/webui/assets/catalog.mjs';

function deferred() {
  let resolve;
  let reject;
  const promise = new Promise((resolvePromise, rejectPromise) => {
    resolve = resolvePromise;
    reject = rejectPromise;
  });
  return { promise, reject, resolve };
}

test('catalog continues one search with the returned cursor', async () => {
  const calls = [];
  const applied = [];
  const catalog = createDurableCatalog({
    limit: 100,
    fetchPage: async (input) => {
      calls.push(input);
      return calls.length === 1
        ? { sessions: [{ id: 'newer' }], next_cursor: 'page-2', has_more: true }
        : { sessions: [{ id: 'older' }], next_cursor: '', has_more: false };
    },
    applyPage: (sessions, replace) => applied.push({ sessions, replace }),
    reportState: () => {},
  });

  await catalog.reset('desktop');
  await catalog.loadMore();

  assert.deepEqual(calls, [
    { cursor: '', limit: 100, search: 'desktop' },
    { cursor: 'page-2', limit: 100, search: 'desktop' },
  ]);
  assert.deepEqual(applied.map((item) => item.replace), [true, false]);
  assert.deepEqual(catalog.snapshot(), {
    generation: 1,
    search: 'desktop',
    cursor: '',
    hasMore: false,
    loading: false,
    error: '',
  });
});

test('a newer reset owns the catalog generation', async () => {
  const first = deferred();
  const second = deferred();
  const applied = [];
  const states = [];
  const catalog = createDurableCatalog({
    fetchPage: ({ search }) => search === 'first' ? first.promise : second.promise,
    applyPage: (sessions, replace) => applied.push({ sessions, replace }),
    reportState: (state) => states.push(state),
  });

  const staleRequest = catalog.reset('first');
  const currentRequest = catalog.reset('second');
  second.resolve({
    sessions: [{ id: 'current' }],
    next_cursor: 'second-page',
    has_more: true,
  });
  assert.equal(await currentRequest, true);
  first.resolve({
    sessions: [{ id: 'stale' }],
    next_cursor: 'stale-page',
    has_more: true,
  });
  assert.equal(await staleRequest, false);

  assert.deepEqual(applied, [{ sessions: [{ id: 'current' }], replace: true }]);
  assert.equal(catalog.snapshot().search, 'second');
  assert.equal(catalog.snapshot().cursor, 'second-page');
  assert.equal(states.at(-1).generation, 2);
});

test('catalog reports an error and a later reset can retry', async () => {
  let attempts = 0;
  const states = [];
  const applied = [];
  const catalog = createDurableCatalog({
    fetchPage: async () => {
      attempts += 1;
      if (attempts === 1) throw new Error('catalog unavailable');
      return { sessions: [{ id: 'recovered' }], has_more: false };
    },
    applyPage: (sessions) => applied.push(sessions),
    reportState: (state) => states.push(state),
  });

  assert.equal(await catalog.reset('retry'), false);
  assert.equal(catalog.snapshot().error, 'catalog unavailable');
  assert.equal(catalog.snapshot().loading, false);

  assert.equal(await catalog.reset('retry'), true);
  assert.deepEqual(applied, [[{ id: 'recovered' }]]);
  assert.equal(catalog.snapshot().error, '');
  assert.equal(states.at(-1).loading, false);
});

test('catalog allows only one in-flight load for a generation', async () => {
  const page = deferred();
  let calls = 0;
  const catalog = createDurableCatalog({
    fetchPage: async () => {
      calls += 1;
      if (calls === 1) {
        return { sessions: [], next_cursor: 'more', has_more: true };
      }
      return page.promise;
    },
    applyPage: () => {},
    reportState: () => {},
  });

  await catalog.reset('');
  const owned = catalog.loadMore();
  const duplicate = catalog.loadMore();
  assert.equal(await duplicate, false);
  assert.equal(calls, 2);

  page.resolve({ sessions: [], next_cursor: '', has_more: false });
  assert.equal(await owned, true);
  assert.equal(catalog.snapshot().loading, false);
});

test('durable history load is single-flight per session and cursor', async () => {
  const gate = deferred();
  let calls = 0;
  const loader = createDurableHistoryLoader(async (input) => {
    calls += 1;
    await gate.promise;
    return { messages: [{ id: input.sessionID }] };
  });

  const first = loader.load({ sessionID: 'session-a', cursor: '', limit: 100 });
  const duplicate = loader.load({ sessionID: 'session-a', cursor: '', limit: 100 });
  assert.equal(first, duplicate);
  await Promise.resolve();
  assert.equal(calls, 1);
  assert.equal(loader.pending('session-a'), true);

  gate.resolve();
  assert.deepEqual(await first, {
    sessionID: 'session-a', page: { messages: [{ id: 'session-a' }] },
  });
  assert.equal(loader.pending('session-a'), false);
});

test('durable history load releases a failed flight for explicit retry', async () => {
  let calls = 0;
  const loader = createDurableHistoryLoader(async () => {
    calls += 1;
    if (calls === 1) throw new Error('history unavailable');
    return { messages: [] };
  });

  await assert.rejects(loader.load({ sessionID: 'session-a' }), /history unavailable/);
  assert.equal(loader.pending('session-a'), false);
  assert.deepEqual(await loader.load({ sessionID: 'session-a' }), {
    sessionID: 'session-a', page: { messages: [] },
  });
  assert.equal(calls, 2);
});

test('durable history keeps different sessions independent and response-owned', async () => {
  const gates = new Map([
    ['session-a', deferred()],
    ['session-b', deferred()],
  ]);
  const calls = [];
  const loader = createDurableHistoryLoader(async (input) => {
    calls.push(input.sessionID);
    await gates.get(input.sessionID).promise;
    return { messages: [{ id: input.sessionID }] };
  });

  const first = loader.load({ sessionID: 'session-a' });
  const second = loader.load({ sessionID: 'session-b' });
  await Promise.resolve();
  assert.deepEqual(calls, ['session-a', 'session-b']);

  gates.get('session-b').resolve();
  assert.equal((await second).sessionID, 'session-b');
  assert.equal(loader.pending('session-a'), true);
  gates.get('session-a').resolve();
  assert.equal((await first).sessionID, 'session-a');
});
