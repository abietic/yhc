import assert from 'node:assert/strict';
import test from 'node:test';

import {
  activateCreatedSession,
  createSessionCreationGate,
} from '../../internal/webui/assets/session_creation.mjs';

function deferred() {
  let resolve;
  let reject;
  const promise = new Promise((resolvePromise, rejectPromise) => {
    resolve = resolvePromise;
    reject = rejectPromise;
  });
  return { promise, reject, resolve };
}

test('created session activates before hydration and hydration never reclaims focus', async () => {
  const hydration = deferred();
  let activeID = 'old';
  const order = [];

  const creation = activateCreatedSession(
    { id: 'new', workspace_label: 'yhc' },
    {
      activate(summary) {
        order.push(`activate:${summary.id}`);
        activeID = summary.id;
      },
      async hydrate(summary) {
        order.push(`hydrate:${summary.id}`);
        await hydration.promise;
      },
    },
  );

  assert.equal(activeID, 'new');
  assert.deepEqual(order, ['activate:new', 'hydrate:new']);

  activeID = 'old';
  hydration.resolve();
  assert.deepEqual(await creation, { id: 'new', workspace_label: 'yhc' });
  assert.equal(activeID, 'old');
});

test('hydration failure preserves the activated server-created session', async () => {
  const summary = { id: 'new', workspace_label: 'yhc' };
  let active = null;

  await assert.rejects(
    activateCreatedSession(summary, {
      activate(value) {
        active = value;
      },
      async hydrate() {
        throw new Error('snapshot unavailable');
      },
    }),
    /snapshot unavailable/,
  );
  assert.equal(active, summary);
});

test('creation gate starts before work and coalesces overlapping attempts', async () => {
  const completion = deferred();
  const busy = [];
  let attempts = 0;
  let gate;
  gate = createSessionCreationGate(
    () => {
      attempts += 1;
      assert.equal(gate.busy(), true);
      return completion.promise;
    },
    (value) => busy.push(value),
  );

  const first = gate.begin();
  const second = gate.begin();
  assert.equal(first, second);
  assert.equal(attempts, 1);
  assert.equal(gate.busy(), true);
  assert.deepEqual(busy, [true]);

  completion.resolve('created');
  assert.equal(await first, 'created');
  assert.equal(gate.busy(), false);
  assert.deepEqual(busy, [true, false]);
});

test('creation gate clears after cancellation and failure', async () => {
  const busy = [];
  const outcomes = [null, new Error('create failed'), 'recovered'];
  const gate = createSessionCreationGate(
    async () => {
      const outcome = outcomes.shift();
      if (outcome instanceof Error) throw outcome;
      return outcome;
    },
    (value) => busy.push(value),
  );

  assert.equal(await gate.begin(), null);
  assert.equal(gate.busy(), false);
  await assert.rejects(gate.begin(), /create failed/);
  assert.equal(gate.busy(), false);
  assert.equal(await gate.begin(), 'recovered');
  assert.equal(gate.busy(), false);
  assert.deepEqual(busy, [true, false, true, false, true, false]);
});
