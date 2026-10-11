import assert from 'node:assert/strict';
import test from 'node:test';
import * as settings from '../../internal/webui/assets/settings_view.mjs';

test('choice-only schema metadata remains authoritative with an empty catalog', () => {
  for (const options of [undefined, []]) {
    assert.equal(settings.usesSettingChoices({ type: 'string', choice_only: true, options }), true);
  }
});

test('legacy options and booleans remain choices; unbounded values remain text or numbers', () => {
  for (const field of [
    { type: 'string', options: ['existing'] }, { type: 'boolean' },
  ]) assert.equal(settings.usesSettingChoices(field), true);
  for (const field of [
    { type: 'string' }, { type: 'integer' }, { type: 'string', options: [] },
    { type: 'string', choice_only: false }, { type: 'string', choice_only: 'true' },
  ]) assert.equal(settings.usesSettingChoices(field), false);
});
