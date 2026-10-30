import assert from 'node:assert/strict';
import test from 'node:test';
import * as settings from '../../internal/webui/assets/settings_view.mjs';

const snapshot = { fields: {
  model_profile: { conflicts_with: ['provider', 'model', 'api_base_url'], stored_value: null, blocked_by: [] },
  provider: { conflicts_with: ['model_profile'], stored_value: 'openai', effective_value: 'project-provider' },
  model: { conflicts_with: ['model_profile'], stored_value: null, effective_value: 'inherited-model' },
  api_base_url: { conflicts_with: ['model_profile'], stored_value: '' },
} };

test('selection conflicts follow Go metadata and selected-scope stored values, not inherited values', () => {
  assert.deepEqual(settings.settingsSelectionConflicts('model_profile', snapshot, { model_profile: 'primary' }), {
    conflicts: ['provider'], blockers: [],
  });
  assert.deepEqual(settings.settingsSelectionConflicts('model_profile', snapshot, {
    model_profile: 'primary', provider: null, model: 'new-model',
  }), { conflicts: ['model'], blockers: [] });
});

test('explicit null removes a conflict; legacy schema remains compatible and unknown fields cannot be reset', () => {
  const fields = { ...snapshot.fields, model_profile: { ...snapshot.fields.model_profile, conflicts_with: ['provider', 'api_key'] } };
  assert.deepEqual(settings.settingsSelectionConflicts('model_profile', { fields }, { provider: null }), { conflicts: [], blockers: [] });
  assert.deepEqual(settings.settingsSelectionConflicts('model', { fields: { model: {} } }, {}), { conflicts: [], blockers: [] });
});

test('noneditable blockers are separate from editable conflicts', () => {
  const fields = { ...snapshot.fields, model_profile: { ...snapshot.fields.model_profile, blocked_by: ['fallback_model'] } };
  assert.deepEqual(settings.settingsSelectionConflicts('model_profile', { fields }, {}), {
    conflicts: ['provider'], blockers: ['fallback_model'],
  });
});
