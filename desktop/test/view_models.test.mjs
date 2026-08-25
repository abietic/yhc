import assert from 'node:assert/strict';
import test from 'node:test';

import {
  buildIdentityViewModel,
  interactionViewModel,
} from '../../internal/webui/assets/view_models.mjs';

test('Desktop build identity is bounded text and browser surfaces hide it', () => {
  assert.deepEqual(buildIdentityViewModel({
    surface: 'desktop',
    build: { version: '0.1.0', commit: '0123456789ab', modified: true },
  }), {
    visible: true,
    text: 'v0.1.0 · 0123456789ab · dirty',
  });
  assert.deepEqual(buildIdentityViewModel({
    surface: 'desktop',
    build: { version: '0.1.0', commit: 'unknown', modified: false },
  }), {
    visible: true,
    text: 'v0.1.0 · unknown',
  });
  for (const info of [
    { surface: 'web', build: { version: '0.1.0', commit: '0123456789ab', modified: false } },
    { surface: 'desktop' },
    { surface: 'desktop', build: { version: '<img src=x>', commit: '0123456789ab', modified: false } },
  ]) {
    assert.deepEqual(buildIdentityViewModel(info), { visible: false, text: '' });
  }
});

test('interaction view models expose only the tagged v2 renderer fields', () => {
  const view = interactionViewModel({ request_id: 'q1', turn_id: 'turn-1', kind: 'question', input: { command: 'secret' }, message: 'raw',
    question: { questions: [{ id: 'q-1', header: 'Pick', text: 'One?', multi_select: false, free_text: true,
      options: [] }] } });
  assert.deepEqual(view, { requestID: 'q1', turnID: 'turn-1', kind: 'question', actionable: true, question: { questions: [{ id: 'q-1', header: 'Pick', text: 'One?', multiSelect: false, freeText: true, options: [] }] } });
  assert.equal(JSON.stringify(view).includes('secret'), false);
  assert.equal(JSON.stringify(view).includes('raw'), false);
});

test('permission, plan, repeated-tool, and unknown views are safe and tagged', () => {
  assert.deepEqual(interactionViewModel({ request_id: 'p', turn_id: 'turn-1', kind: 'permission', permission: { available: true, tool_label: 'Write', summary: 'Allow this tool action?', evidence: [{ label: 'Access', value: 'May change data' }], grant_scopes: ['allow_once', 'allow_session', 'allow_always'] } }),
    { requestID: 'p', turnID: 'turn-1', kind: 'permission', actionable: true, permission: { available: true, toolLabel: 'Write', summary: 'Allow this tool action?', evidence: [{ label: 'Access', value: 'May change data' }], grantScopes: ['allow_once', 'allow_session', 'allow_always'] } });
  assert.deepEqual(interactionViewModel({ request_id: 'p-once', turn_id: 'turn-1', kind: 'permission', permission: { available: true, tool_label: 'Bash', summary: 'Allow this tool action?', evidence: [{ label: 'Access', value: 'May make destructive changes' }], grant_scopes: ['allow_once'] } }),
    { requestID: 'p-once', turnID: 'turn-1', kind: 'permission', actionable: true, permission: { available: true, toolLabel: 'Bash', summary: 'Allow this tool action?', evidence: [{ label: 'Access', value: 'May make destructive changes' }], grantScopes: ['allow_once'] } });
  assert.deepEqual(interactionViewModel({ request_id: 'r', turn_id: 'turn-1', kind: 'repeated_tool', repeated_tool: { attempt: 2, explanation: 'This repeated tool call needs your decision.', outcomes: ['continue', 'stop'] } }),
    { requestID: 'r', turnID: 'turn-1', kind: 'repeated_tool', actionable: true, repeatedTool: { attempt: 2, explanation: 'This repeated tool call needs your decision.', outcomes: ['continue', 'stop'] } });
  assert.equal(interactionViewModel({ request_id: 'fallback', turn_id: 'turn-1', kind: 'permission', permission: {
    available: false, evidence: [], grant_scopes: ['allow_once'],
  } }).actionable, true);
  assert.equal(interactionViewModel({ request_id: 'plan', turn_id: 'turn-1', kind: 'plan_approval', plan_approval: {
    revision: 2, target_modes: ['default', 'acceptEdits', 'bypassPermissions'], review_available: false,
  } }).actionable, true);
  assert.deepEqual(interactionViewModel({ request_id: 'x', kind: 'other', source: 'private' }),
    { requestID: 'x', turnID: '', kind: 'unknown', actionable: false });
});

test('known interaction kinds fail closed when their v2 variant is malformed', () => {
  const malformed = [
    { request_id: 'question', turn_id: 'turn-1', kind: 'question' },
    { request_id: 'question', turn_id: 'turn-1', kind: 'question', question: { questions: [] } },
    { request_id: 'question', turn_id: 'turn-1', kind: 'question', question: { questions: [{
      id: 'wrong', header: '', text: 'Choose', options: [], multi_select: false, free_text: true,
    }] } },
    { request_id: 'repeated', turn_id: 'turn-1', kind: 'repeated_tool', repeated_tool: {
      attempt: 1, explanation: 'again', outcomes: [],
    } },
    { request_id: 'repeated', turn_id: 'turn-1', kind: 'repeated_tool', repeated_tool: {
      attempt: 1, explanation: 'again', outcomes: ['continue', 'unexpected'],
    } },
    { request_id: 'permission', turn_id: 'turn-1', kind: 'permission', permission: {
      available: true, tool_label: 'Write', summary: 'raw prompt text',
      evidence: [{ label: 'Access', value: 'secret' }],
      grant_scopes: ['allow_once', 'allow_session', 'allow_always'],
    } },
    { request_id: 'permission', turn_id: 'turn-1', kind: 'permission', permission: {
      available: true, tool_label: 'Write', summary: 'Allow this tool action?',
      evidence: [{ label: 'Access', value: 'May change data' }],
      grant_scopes: ['allow_session'],
    } },
    { request_id: 'plan', turn_id: 'turn-1', kind: 'plan_approval', plan_approval: {
      revision: 0, target_modes: ['default'], review_available: true,
    } },
    { request_id: 'permission', turn_id: '', kind: 'permission', permission: {
      available: false, evidence: [], grant_scopes: ['allow_once'],
    } },
    { request_id: 'permission', turn_id: 'turn-1', kind: 'permission', permission: {
      available: false, evidence: [], grant_scopes: ['allow_once'],
    }, question: { questions: [] } },
  ];

  for (const interaction of malformed) {
    assert.equal(interactionViewModel(interaction).actionable, false, JSON.stringify(interaction));
  }
});
