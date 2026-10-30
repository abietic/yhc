import assert from 'node:assert/strict';
import test from 'node:test';

import { settingsGroups, settingsMatches, settingsCredentialsMatches } from '../../internal/webui/assets/settings_view.mjs';

const fields = Object.freeze({
  theme: Object.freeze({ group: 'appearance', label: 'Theme', description: 'Shared appearance' }),
  provider: Object.freeze({ group: 'model', label: 'Provider', description: 'Default route' }),
  model: Object.freeze({ group: 'model', label: 'Model', description: 'New session model' }),
  max_turns: Object.freeze({ group: 'execution', label: 'Maximum turns', description: 'Zero means unlimited' }),
});

test('settings categories prioritize model setup without mutating schema metadata', () => {
  const groups = settingsGroups(fields);
  assert.deepEqual(groups.map(({ id, label, entries }) => [id, label, entries.map(([key]) => key)]), [
    ['model', 'Model & provider', ['provider', 'model']],
    ['execution', 'Agent behavior', ['max_turns']],
    ['appearance', 'Appearance & input', ['theme']],
  ]);
  assert.strictEqual(groups[0].entries[0][1], fields.provider);
  assert.deepEqual(Object.keys(fields), ['theme', 'provider', 'model', 'max_turns']);
});

test('unknown schema groups and ungrouped fields remain discoverable', () => {
  const groups = settingsGroups({
    future: { group: 'extensions', label: 'Future setting' },
    legacy: { label: 'Ungrouped setting' },
    unusual: { group: '__proto__', label: 'Unusual group' },
  });
  assert.deepEqual(groups.map(group => [group.id, typeof group.label]), [
    ['__proto__', 'string'], ['extensions', 'string'], ['other', 'string'],
  ]);
  assert.equal(settingsMatches('future', { group: 'extensions' }, 'extensions', ''), true);
});

test('category selection filters fields but a search always crosses categories', () => {
  assert.equal(settingsMatches('theme', fields.theme, 'model', ''), false);
  assert.equal(settingsMatches('theme', fields.theme, 'appearance', ''), true);
  assert.equal(settingsMatches('theme', fields.theme, 'all', ''), true);
  assert.equal(settingsMatches('max_turns', fields.max_turns, 'appearance', '  UNLIMITED  '), true);
  assert.equal(settingsMatches('provider', fields.provider, 'appearance', 'model & provider'), true);
  assert.equal(settingsMatches('theme', fields.theme, 'all', 'missing setting'), false);
  assert.equal(settingsMatches('theme', fields.theme, 'missing', ''), false);
});

test('search uses only schema identity and descriptions, not setting values', () => {
  const field = { ...fields.provider, stored_value: 'fixture-stored-value', effective_value: 'fixture-effective-value' };
  assert.equal(settingsMatches('provider', field, 'all', 'provider'), true);
  assert.equal(settingsMatches('provider', field, 'all', 'fixture-stored-value'), false);
  assert.equal(settingsMatches('provider', field, 'all', 'fixture-effective-value'), false);
});

test('credentials are contextual help, discoverable by public metadata across categories', () => {
  for (const category of ['model', 'all']) assert.equal(settingsCredentialsMatches(category, ''), true);
  for (const category of ['execution', 'appearance', 'extensions']) assert.equal(settingsCredentialsMatches(category, ''), false);
  for (const query of ['API key', '  CREDENTIALS  ', 'secure storage', 'authentication']) {
    assert.equal(settingsCredentialsMatches('appearance', query), true);
  }
  for (const query of ['theme', 'maximum turns', 'fixture-key-value', 'missing setting']) {
    assert.equal(settingsCredentialsMatches('all', query), false);
  }
});
