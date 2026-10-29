import assert from 'node:assert/strict';
import test from 'node:test';
import { createGoalController, normalizeGoalState } from '../../internal/webui/assets/goal.mjs';

const sessionID = 'f47ac10b-58cc-4372-a567-0e02b2c3d479';
const empty = { session_id: sessionID, available: true, diagnostic: '', default_token_budget: null, goal: null };
const active = { ...empty, goal: { goal_id: 'goal-1', objective: 'x', status: 'active', revision: '1', token_budget: null, tokens_used: '0', root_active_time_ms: '0', usage_coverage: '', status_reason_code: '', status_reason: '' } };
const ready = (response = active, started = () => {}) => {
  const controller = createGoalController({ api: async () => response, onStarted: started });
  controller.updateContext({ sessionID, live: true, backendReady: true, activeTurnID: '' }); return controller.load().then(() => controller);
};

test('goal state keeps decimal values as strings and rejects malformed state', () => {
  assert.equal(normalizeGoalState({ ...empty, default_token_budget: '9007199254740993' }, sessionID).default_token_budget, '9007199254740993');
  assert.throws(() => normalizeGoalState({ ...empty, extra: true }, sessionID), /Invalid Goal state/);
  assert.throws(() => normalizeGoalState({ ...empty, goal: { goal_id: 'g', objective: 'x', status: 'active', revision: 1 } }, sessionID), /Invalid Goal state/);
});

test('goal controller sends exact CAS create and preserves draft on conflicts', async () => {
  const calls = [];
  const controller = createGoalController({ api: async (operation, payload) => {
    calls.push([operation, payload]);
    if (operation === 'getGoal') return empty;
    const error = new Error('changed'); error.code = 'goal_conflict'; error.status = 409; throw error;
  } });
  controller.updateContext({ sessionID, live: true, backendReady: true, activeTurnID: '' });
  await controller.load();
  assert.equal(controller.stage({ objective: '  目标  ', tokenBudget: '42' }), true);
  await controller.control('create');
  assert.deepEqual(calls.at(-1), ['controlGoal', { sessionID, operation: 'create', expected_goal_id: '', expected_revision: '0', objective: '目标', token_budget: '42' }]);
  assert.equal(controller.state().draft.objective, '  目标  ');
  assert.equal(controller.state().status, 'conflict');
});

test('goal controller drops late reads and refuses mutations while a turn is active', async () => {
  let resolve;
  const controller = createGoalController({ api: () => new Promise(done => { resolve = done; }) });
  controller.updateContext({ sessionID, live: true, backendReady: true, activeTurnID: '' });
  const pending = controller.load();
  controller.updateContext({ sessionID: 'other-session', live: true, backendReady: true, activeTurnID: '' });
  resolve(empty); await pending;
  assert.equal(controller.state().snapshot, null);
  controller.updateContext({ sessionID, live: true, backendReady: true, activeTurnID: 'turn-1' });
  assert.equal(await controller.control('clear'), false);
});

test('Goal refuses stage and control before a successful read', async () => {
  const controller = createGoalController({ api: async () => empty });
  controller.updateContext({ sessionID, live: true, backendReady: true, activeTurnID: '' });
  assert.equal(controller.stage({ objective: 'x', tokenBudget: '' }), false);
  assert.equal(await controller.control('create'), false);
});

test('Goal read error cannot authorize creation', async () => {
  const controller = createGoalController({ api: async () => { throw new Error('down'); } });
  controller.updateContext({ sessionID, live: true, backendReady: true, activeTurnID: '' }); await controller.load();
  assert.equal(await controller.control('create'), false);
});

test('Goal session switch retains but cannot send old draft', async () => {
  const controller = await ready(empty); controller.stage({ objective: 'x', tokenBudget: '' });
  controller.updateContext({ sessionID: 'other', live: true, backendReady: true, activeTurnID: '' });
  assert.equal(await controller.control('create'), false);
});

test('Goal rejects overflowing U64 state', () => {
  assert.throws(() => normalizeGoalState({ ...empty, default_token_budget: '18446744073709551616' }, sessionID));
});

test('Goal refresh never silently rebases a draft', async () => {
  let snapshot = active, writes = 0;
  const controller = createGoalController({ api: async op => { if (op === 'getGoal') return snapshot; writes++; } });
  controller.updateContext({ sessionID, live: true, backendReady: true }); await controller.load();
  controller.stage({ objective: 'draft', tokenBudget: '42' });
  snapshot = { ...active, goal: { ...active.goal, revision: '2' } }; await controller.load();
  controller.stage({ objective: 'more draft', tokenBudget: '43' });
  assert.equal(controller.state().draft.revision, '1');
  assert.equal(await controller.control('edit'), false); assert.equal(writes, 0);
  assert.match(controller.state().notice, /草稿/);
});

test('Goal successful field save retains the other unsaved field', async () => {
  let snapshot = active;
  const controller = createGoalController({ api: async (op, payload) => {
    if (op === 'getGoal') return snapshot;
    snapshot = { ...active, goal: { ...active.goal, objective: payload.objective ?? 'draft', token_budget: payload.token_budget ?? null, revision: payload.operation === 'edit' ? '2' : '3' } };
    return { state: snapshot, saved: true, started: false };
  } });
  controller.updateContext({ sessionID, live: true, backendReady: true }); await controller.load();
  controller.stage({ objective: 'draft', tokenBudget: '42' }); await controller.control('edit');
  assert.equal(controller.state().draft.tokenBudget, '42'); assert.equal(controller.state().draft.revision, '2');
  await controller.control('budget'); assert.equal(controller.state().draft, null);
});

test('Goal foreign draft cannot be adopted by typing after a session switch', async () => {
  const controller = createGoalController({ api: async (_op, payload) => ({ ...empty, session_id: payload.sessionID }) });
  controller.updateContext({ sessionID, live: true, backendReady: true }); await controller.load();
  controller.stage({ objective: 'old', tokenBudget: '' });
  controller.updateContext({ sessionID: 'other', live: true, backendReady: true }); await controller.load();
  assert.equal(controller.stage({ objective: 'new', tokenBudget: '' }), false);
  assert.equal(await controller.control('create'), false);
  assert.equal(controller.state().draft.objective, 'old');
});

test('Goal invalid creation budget never reaches transport', async () => {
  let writes = 0;
  const controller = createGoalController({ api: async op => { if (op === 'getGoal') return empty; writes++; } });
  controller.updateContext({ sessionID, live: true, backendReady: true }); await controller.load();
  for (const tokenBudget of ['0', '00', '-1', '18446744073709551616']) {
    controller.stage({ objective: 'x', tokenBudget }); assert.equal(await controller.control('create'), false);
  }
  assert.equal(writes, 0);
});
