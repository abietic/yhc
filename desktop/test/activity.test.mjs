import test from 'node:test';
import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';

import { activityPresentation } from '../../internal/webui/assets/activity.mjs';

test('Activity presentation uses fixed semantic copy for every admitted family', () => {
  const cases = [
    [{ kind: 'turn', state: 'started', category: '' }, ['Turn', 'Started']],
    [{ kind: 'turn', state: 'waiting', category: '' }, ['Turn', 'Waiting for input']],
    [{ kind: 'turn', state: 'failed', category: '' }, ['Turn', 'Failed']],
    [{ kind: 'tool', state: 'running', category: 'command' }, ['Command', 'Running']],
    [{ kind: 'tool', state: 'completed', category: 'file_read' }, ['File read', 'Completed']],
    [{ kind: 'task', state: 'paused', category: 'task' }, ['Task', 'Paused']],
    [{ kind: 'agent', state: 'stopped', category: 'agent' }, ['Agent', 'Stopped']],
    [{ kind: 'interaction', state: 'waiting', category: 'question' }, ['Question', 'Waiting for your response']],
    [{ kind: 'interaction', state: 'resolved', category: 'plan_approval' }, ['Plan approval', 'Resolved']],
  ];
  for (const [entry, expected] of cases) {
    const presentation = activityPresentation(entry);
    assert.deepEqual([presentation.title, presentation.detail], expected);
  }
  assert.equal(activityPresentation({ kind: 'assistant', state: 'completed' }), null);
  assert.equal(activityPresentation({ kind: 'tool', state: 'running', category: 'raw-command' }), null);
});

test('Activity renderer consumes only semantic projection fields', async () => {
  const [app, activity, css] = await Promise.all([
    readFile(new URL('../../internal/webui/assets/app.mjs', import.meta.url), 'utf8'),
    readFile(new URL('../../internal/webui/assets/activity.mjs', import.meta.url), 'utf8'),
    readFile(new URL('../../internal/webui/assets/styles.css', import.meta.url), 'utf8'),
  ]);

  assert.match(app, /from '\.\/activity\.mjs'/);
  assert.match(app, /activityPresentation\(item\)/);
  assert.doesNotMatch(app, /function describeActivity\(/);
  assert.doesNotMatch(app, /function activityKind\(/);
  for (const forbidden of ['message', 'content', 'reasoning', 'error', 'input', 'command', 'path']) {
    assert.doesNotMatch(activity, new RegExp(`entry\\.${forbidden}\\b`));
  }
  assert.match(css, /\.activity-empty/);
  assert.match(css, /activity-item\[data-state="failed"\]/);
});
