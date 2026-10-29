import assert from 'node:assert/strict';
import { createRequire } from 'node:module';
import { readFile } from 'node:fs/promises';
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import { createHash } from 'node:crypto';
import test from 'node:test';

const require = createRequire(import.meta.url);
const { ambientProviderConfigured, validateProviderSubmission } = require('../provider_setup.cjs');
const { parseArguments, validatePlatformOptions } = require('../scripts/unpacked_lifecycle_smoke.cjs');
const {
  settingsPersistenceMatches, settingsProjectionMatches, verifySettingsPersistence,
} = require('../scripts/shared_settings_acceptance.cjs');

const expected = { provider: 'glm', model: 'glm-5.3-flash', theme: 'daybreak', max_turns: 0, reduced_motion: false };
function snapshotFor(values = expected) {
  return {
    scope: 'user',
    fields: { model_profile: { choice_only: true }, ...Object.fromEntries(Object.entries(values).map(([key, value]) => [key, {
      stored_value: value, effective_value: value, source: 'user', overridden: false,
    }])) },
  };
}

test('Desktop credentials admit runtime-supported GLM', () => {
  const input = { provider: 'glm', model: 'glm-5.3-flash', apiKey: 'fixture-only-key' };
  assert.deepEqual(validateProviderSubmission(input), input);
});

test('runtime GLM key names are recognized by Desktop onboarding', () => {
  for (const name of ['ZAI_API_KEY', 'ZHIPUAI_API_KEY']) {
    assert.equal(ambientProviderConfigured({ [name]: 'fixture-only-key' }), true, name);
    assert.equal(ambientProviderConfigured({ [name]: '   ' }), false, name);
  }
});

test('Desktop onboarding offers GLM', async () => {
  const html = await readFile(new URL('../../internal/webui/assets/index.html', import.meta.url), 'utf8');
  assert.equal(html.includes('<option value="glm">'), true);
});

test('native shared-settings mode is explicit, macOS-only, and exclusive', () => {
  const input = parseArguments(['--app', '/tmp/fixture/YHC.app/Contents/MacOS/YHC', '--shared-settings']);
  assert.equal(input.sharedSettings, true);
  assert.equal(validatePlatformOptions('darwin', input).sharedSettings, true);
  for (const platform of ['linux', 'win32', 'unknown']) {
    assert.throws(() => validatePlatformOptions(platform, { sharedSettings: true }), /macOS-only/);
  }
  for (const extra of ['--crash-backend', '--crash-active-turn', '--reopen-window', '--shared-settings']) {
    assert.throws(() => parseArguments(['--app', '/tmp/fixture/YHC.app/Contents/MacOS/YHC', '--shared-settings', extra]));
  }
});

test('persistence oracle rejects omitted zero/false, changed types, wrong scope, and inherited defaults', () => {
  assert.equal(settingsPersistenceMatches(expected, snapshotFor(), expected), true);
  for (const key of Object.keys(expected)) {
    const missing = { ...expected };
    delete missing[key];
    assert.equal(settingsPersistenceMatches(missing, snapshotFor(), expected), false, key);
    for (const patch of [
      { stored_value: null }, { effective_value: null }, { source: 'default' },
      { source: 'project' }, { overridden: true },
    ]) {
      const snapshot = snapshotFor();
      Object.assign(snapshot.fields[key], patch);
      assert.equal(settingsPersistenceMatches(expected, snapshot, expected), false, key);
    }
  }
  const stringZero = { ...expected, max_turns: '0' };
  assert.equal(settingsPersistenceMatches(stringZero, snapshotFor(stringZero), expected), false);
  assert.equal(settingsPersistenceMatches(expected, { ...snapshotFor(), scope: 'project' }, expected), false);
  assert.equal(settingsPersistenceMatches(null, snapshotFor(), expected), false);
});

test('renderer oracle requires saved typed values, provenance, appearance, and disabled project scope', () => {
  const probe = {
    open: true, scope: 'user', projectDisabled: true, saveDisabled: true, providerOption: true,
    profileControl: 'SELECT', profileDisabled: true, profilePlaceholder: 'No saved choices available',
    status: 'Saved. Agent defaults apply to new sessions.', theme: 'daybreak', reducedMotion: 'false',
    values: Object.fromEntries(Object.entries(expected).map(([key, value]) => [key, String(value)])),
    sources: Object.fromEntries(Object.keys(expected).map((key) => [key, 'Effective · From user'])),
  };
  assert.equal(settingsProjectionMatches(probe, expected, 'Saved.'), true);
  for (const patch of [
    { open: false }, { scope: 'project' }, { projectDisabled: false }, { saveDisabled: false },
    { providerOption: false }, { status: null }, { status: 'Unsaved changes' },
    { profileControl: 'INPUT' }, { profileDisabled: false }, { profilePlaceholder: '' },
    { theme: 'polar-night' }, { reducedMotion: 'true' }, { values: {} }, { sources: {} },
  ]) {
    assert.equal(settingsProjectionMatches({ ...probe, ...patch }, expected, 'Saved.'), false);
  }
});

test('fresh CLI oracle is bounded, uses the same isolated environment, and fails closed', (t) => {
  const root = fs.mkdtempSync(path.join(os.tmpdir(), 'yhc-settings-oracle-'));
  t.after(() => fs.rmSync(root, { recursive: true, force: true }));
  const configDirectory = path.join(root, 'home', '.claude');
  const workspace = path.join(root, 'workspace');
  fs.mkdirSync(configDirectory, { recursive: true, mode: 0o700 });
  fs.mkdirSync(workspace, { mode: 0o700 });
  const settingsPath = path.join(configDirectory, 'settings.json');
  const raw = JSON.stringify(expected);
  fs.writeFileSync(settingsPath, raw, { mode: 0o600 });
  const snapshot = { ...snapshotFor(), revision: createHash('sha256').update(raw).digest('hex') };
  const environment = { HOME: path.join(root, 'home'), PATH: '/fixture' };
  const options = { backendPath: '/fixture/packaged/yhc', environment, workspace, expected };
  let calls = 0;
  assert.deepEqual(verifySettingsPersistence({ ...options, runCLI: (command, args, config) => {
    calls++;
    assert.equal(command, options.backendPath);
    assert.deepEqual(args, ['config', 'settings', '--scope', 'user']);
    assert.strictEqual(config.env, environment);
    assert.equal(config.cwd, workspace);
    assert.equal(config.timeout, 10_000);
    assert.equal(config.maxBuffer, 1 << 20);
    return { status: 0, signal: null, stdout: JSON.stringify(snapshot) };
  } }), snapshot);
  assert.equal(calls, 1);
  for (const result of [
    { status: 1 }, { status: null, error: { code: 'ETIMEDOUT' } },
    { status: 0, signal: 'SIGTERM' },
    { status: 0, stdout: JSON.stringify({ ...snapshot, revision: 'wrong' }) },
  ]) {
    assert.throws(() => verifySettingsPersistence({ ...options, runCLI: () => result }));
  }
  for (const profile of [{}, { choice_only: false }, { choice_only: true, options: ['invented'] }]) {
    const wrong = { ...snapshot, fields: { ...snapshot.fields, model_profile: profile } };
    assert.throws(() => verifySettingsPersistence({ ...options, runCLI: () => ({ status: 0, stdout: JSON.stringify(wrong) }) }), /profile/);
  }
  fs.chmodSync(settingsPath, 0o644);
  assert.throws(() => verifySettingsPersistence({ ...options, runCLI: () => assert.fail('unsafe file must fail before CLI') }), /owner-only/);
});

test('native selection oracle requires the exact catalog and deleted overrides in both file and CLI', (t) => {
  const root = fs.mkdtempSync(path.join(os.tmpdir(), 'yhc-selection-oracle-'));
  t.after(() => fs.rmSync(root, { recursive: true, force: true }));
  const directory = path.join(root, 'home', '.claude');
  const workspace = path.join(root, 'workspace');
  fs.mkdirSync(directory, { recursive: true, mode: 0o700 }); fs.mkdirSync(workspace);
  const settingsPath = path.join(directory, 'settings.json');
  const selected = { theme: 'daybreak', model_profile: 'smoke-primary' };
  const raw = JSON.stringify(selected);
  fs.writeFileSync(settingsPath, raw, { mode: 0o600 });
  const snapshot = snapshotFor(selected);
  Object.assign(snapshot.fields.model_profile, { choice_only: true, options: ['smoke-primary'] });
  for (const key of ['provider', 'model', 'api_base_url']) snapshot.fields[key] = { stored_value: null };
  snapshot.revision = createHash('sha256').update(raw).digest('hex');
  const options = { backendPath: '/fixture/yhc', environment: { HOME: path.join(root, 'home') }, workspace,
    expected: selected, profileChoices: ['smoke-primary'], absentKeys: ['provider', 'model', 'api_base_url'] };
  const run = (value) => () => ({ status: 0, signal: null, stdout: JSON.stringify(value) });
  assert.deepEqual(verifySettingsPersistence({ ...options, runCLI: run(snapshot) }), snapshot);
  for (const patch of [{ options: [] }, { options: ['other'] }, { choice_only: false }]) {
    const wrong = structuredClone(snapshot); Object.assign(wrong.fields.model_profile, patch);
    assert.throws(() => verifySettingsPersistence({ ...options, runCLI: run(wrong) }), /profile/);
  }
  const wrong = structuredClone(snapshot); wrong.fields.model.stored_value = 'legacy-model';
  assert.throws(() => verifySettingsPersistence({ ...options, runCLI: run(wrong) }), /override/);
  fs.writeFileSync(settingsPath, JSON.stringify({ ...selected, model: 'legacy-model' }));
  assert.throws(() => verifySettingsPersistence({ ...options, runCLI: run(snapshot) }));
});
