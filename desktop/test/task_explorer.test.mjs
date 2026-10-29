import test from 'node:test';
import assert from 'node:assert/strict';
import { normalizeTaskExplorerSnapshot, taskExplorerRows } from '../../internal/webui/assets/task_explorer.mjs';

const execution = (generation) => ({ agent_id: 'same-agent', generation, observation_ordinal: '0',
  name: 'Worker', phase: 'waiting_input', activity: 'looks running', attention: ['permission'],
  transcript_path: '/private/synthetic', allowed_actions: ['pause'] });
const raw = () => ({ session_id: 's', available: false, unavailable_reason: 'legacy board',
  revision: { board: '18446744073709551615', runtime: '9007199254740993' },
  executions: [execution('9007199254740992'), execution('9007199254740993')],
  work_items: [{ board_id: 'first', id: 'same', revision: '1', title: 'Bounded work' },
    { board_id: 'second', id: 'same', revision: '2', title: 'Another work' }],
  links: [{ board_id: 'first', work_item_id: 'same', agent_id: 'same-agent', generation: '9007199254740993', state: 'valid' }],
  attention: [], diagnostics: [],
  hidden: { runtime_events_dropped: '18446744073709551615', executions: { other: 3 } },
  transcript_path: '/private/synthetic', raw_payload: { secret: true },
});

test('task projection preserves exact identities, ordered kinds and canonical phases without private metadata', () => {
  const projected = normalizeTaskExplorerSnapshot(raw(), 's');
  const ordered = taskExplorerRows(projected);
  assert.deepEqual(ordered.map(row => row.kind), ['work', 'work', 'execution', 'execution']);
  assert.equal(new Set(ordered.map(row => row.key)).size, 4);
  assert.equal(ordered[2].status, 'waiting_input', 'activity text cannot override phase');
  assert.equal(projected.available, false, 'partial execution snapshots remain useful');
  assert.equal(projected.hidden.runtime_events_dropped, '18446744073709551615');
  assert.equal(JSON.stringify(projected).includes('/private/synthetic'), false);
  assert.equal(JSON.stringify(projected).includes('allowed_actions'), false);
  assert.deepEqual(taskExplorerRows(projected, 'worker', 'execution').map(row => row.data.generation), ['9007199254740992', '9007199254740993']);
  assert.equal(taskExplorerRows(projected, 'bounded', 'execution').length, 0);
});

test('malformed and duplicate identities fail closed rather than rebinding rows', () => {
  for (const generation of [9007199254740993, '01', '-1', '', '18446744073709551616']) {
    const value = raw(); value.executions[0].generation = generation;
    assert.throws(() => normalizeTaskExplorerSnapshot(value, 's'));
  }
  const duplicate = raw(); duplicate.executions[1].generation = duplicate.executions[0].generation;
  assert.throws(() => normalizeTaskExplorerSnapshot(duplicate, 's'));
  assert.throws(() => normalizeTaskExplorerSnapshot(raw(), 'other'));
  const malformed = raw(); malformed.work_items = 'not a list';
  assert.throws(() => normalizeTaskExplorerSnapshot(malformed, 's'));
});

test('presentation copy is bounded, Unicode-safe and independent of the wire object', () => {
  const value = raw();
  value.executions = Array.from({ length: 150 }, (_, index) => ({ ...execution(String(index)), name: '界'.repeat(600) }));
  value.work_items[0].blocks = Array.from({ length: 150 }, () => '界'.repeat(600));
  value.hidden.executions = Object.fromEntries(Array.from({ length: 50 }, (_, i) => [`hidden${i}`, i]));
  const projected = normalizeTaskExplorerSnapshot(value, 's');
  assert.equal(projected.executions.length, 128);
  assert.equal([...projected.executions[0].name].length, 512);
  assert.equal(projected.work_items[0].blocks.length, 128);
  assert.equal(Object.keys(projected.hidden.executions).length, 32);
  value.executions[0].name = 'mutated';
  assert.equal([...projected.executions[0].name].length, 512);
});

test('over-budget exact lineage is unavailable, never a truncated new identity', () => {
  const value = raw();
  value.executions[0].execution_session_id = '界'.repeat(513);
  value.executions[0].thread_id = 'exact-thread';
  const projected = normalizeTaskExplorerSnapshot(value, 's');
  assert.equal(projected.executions[0].execution_session_id, '');
  assert.equal(projected.executions[0].thread_id, 'exact-thread');
});
