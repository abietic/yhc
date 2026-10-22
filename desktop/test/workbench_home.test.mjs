import assert from 'node:assert/strict';
import test from 'node:test';
import { homeConfigurationSummary, recentWorkbenchSessions } from '../../internal/webui/assets/workbench_home.mjs';

test('shared defaults are an observed selection, not a credential or connectivity claim', () => {
  const fields = {
    provider: { effective_value: 'glm', source: 'user' },
    model: { effective_value: 'glm-fixture', source: 'default' },
    api_key: { effective_value: 'must-not-escape' },
    api_base_url: { effective_value: 'https://secret@example.test' },
  };
  const summary = homeConfigurationSummary('desktop', fields, { configured: true, provider: 'openai', model: 'fallback' });
  assert.equal(summary.title, 'Shared defaults detected');
  assert.deepEqual(summary.entries, [
    { label: 'Provider', value: 'glm', source: 'User defaults' },
    { label: 'Model', value: 'glm-fixture', source: 'Built-in default' },
  ]);
  assert.match(summary.detail, /project overrides/);
  assert.match(summary.detail, /credentials/);
  assert.doesNotMatch(JSON.stringify(summary), /must-not-escape|secret@example|fallback|Connected/);
});

test('a model profile remains the canonical selection instead of mixing legacy fallbacks', () => {
  const summary = homeConfigurationSummary('desktop', {
    model_profile: { effective_value: 'primary', source: 'project' },
    provider: { effective_value: 'anthropic', source: 'user' },
    model: { effective_value: 'legacy', source: 'user' },
  }, { configured: true, provider: 'openai', model: 'fallback' });
  assert.deepEqual(summary.entries, [{ label: 'Model profile', value: 'primary', source: 'Project override' }]);
});

test('a model-only shared selection does not invent a provider', () => {
  const summary = homeConfigurationSummary('desktop', { model: { effective_value: 'chosen', source: 'user' } });
  assert.equal(summary.title, 'Shared defaults detected');
  assert.deepEqual(summary.entries, [{ label: 'Model', value: 'chosen', source: 'User defaults' }]);
});

test('Desktop vault metadata is explicitly a fallback, never a shared setting', () => {
  const summary = homeConfigurationSummary('desktop', {}, { configured: true, provider: 'openai', model: 'fixture', apiKey: 'must-not-escape' });
  assert.equal(summary.title, 'Desktop fallback saved');
  assert.deepEqual(summary.entries, [
    { label: 'Provider', value: 'openai', source: 'Desktop fallback' },
    { label: 'Model', value: 'fixture', source: 'Desktop fallback' },
  ]);
  assert.match(summary.detail, /take priority/);
  assert.doesNotMatch(JSON.stringify(summary), /must-not-escape|apiKey/);
});

test('launch environment and empty setup stay honest about runtime resolution', () => {
  const ambient = homeConfigurationSummary('desktop', {}, { launchReady: true });
  assert.equal(ambient.title, 'Launch configuration detected');
  assert.deepEqual(ambient.entries, []);
  assert.match(ambient.detail, /runtime/);
  const missing = homeConfigurationSummary('desktop', { model: { effective_value: 'default', source: 'default' } });
  assert.equal(missing.title, 'Choose your model setup');
  assert.match(missing.detail, /workspace may already/);
});

test('Web never projects Desktop credential capability or fallback metadata', () => {
  const summary = homeConfigurationSummary('web', {}, { configured: true, provider: 'private-provider', model: 'private-model' });
  assert.equal(summary.title, 'Host-managed configuration');
  assert.deepEqual(summary.entries, []);
  assert.match(summary.detail, /Desktop App or host process/);
  assert.doesNotMatch(JSON.stringify(summary), /private-provider|private-model/);
});

test('recent sessions are a bounded newest-first read-only projection', () => {
  const sessions = Array.from({ length: 7 }, (_, index) => ({
    id: String(index), title: 'Session ' + index, updated_at: `2026-10-0${index + 1}T00:00:00Z`,
  }));
  assert.deepEqual(recentWorkbenchSessions(sessions).map(item => item.id), ['6', '5', '4', '3', '2']);
  assert.deepEqual(sessions.map(item => item.id), ['0', '1', '2', '3', '4', '5', '6']);
  assert.deepEqual(recentWorkbenchSessions(null), []);
});
