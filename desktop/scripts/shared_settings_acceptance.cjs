const { spawnSync } = require('node:child_process');
const { createHash } = require('node:crypto');
const fs = require('node:fs');
const path = require('node:path');
const assert = require('node:assert/strict');

const INITIAL_SETTINGS = Object.freeze({
  provider: 'glm', model: 'glm-5.3-flash', theme: 'daybreak',
  max_turns: 2, reduced_motion: true,
});
const FINAL_SETTINGS = Object.freeze({ ...INITIAL_SETTINGS, max_turns: 0, reduced_motion: false });
const SETTINGS_TIMEOUT_MS = 10_000;

function settingsPersistenceMatches(document, snapshot, expected) {
  if (snapshot?.scope !== 'user' || !document || !snapshot.fields) return false;
  return Object.entries(expected).every(([key, value]) => {
    const field = snapshot.fields[key];
    return Object.hasOwn(document, key) && document[key] === value &&
      field?.stored_value === value && field.effective_value === value &&
      field.source === 'user' && field.overridden === false;
  });
}

function settingsProjectionMatches(probe, expected, statusPrefix) {
  return probe?.open === true && probe.scope === 'user' && probe.projectDisabled === true &&
    probe.saveDisabled === true && probe.providerOption === true &&
    probe.profileControl === 'SELECT' && probe.profileDisabled === true &&
    probe.profilePlaceholder === 'No saved choices available' &&
    typeof probe.status === 'string' && probe.status.startsWith(statusPrefix) && probe.theme === expected.theme &&
    probe.reducedMotion === String(expected.reduced_motion) &&
    Object.entries(expected).every(([key, value]) => probe.values?.[key] === String(value) &&
      probe.sources?.[key]?.includes('From user'));
}

function verifySettingsPersistence({ backendPath, environment, workspace, expected, profileChoices = [], absentKeys = [], runCLI = spawnSync }) {
  const settingsPath = path.join(environment.HOME, '.claude', 'settings.json');
  const info = fs.lstatSync(settingsPath);
  if (!info.isFile() || info.isSymbolicLink() || (info.mode & 0o777) !== 0o600) {
    throw new Error('shared settings are not an owner-only regular file');
  }
  const raw = fs.readFileSync(settingsPath);
  const document = JSON.parse(raw);
  const child = runCLI(backendPath, ['config', 'settings', '--scope', 'user'], {
    cwd: workspace, env: environment, encoding: 'utf8',
    timeout: SETTINGS_TIMEOUT_MS, maxBuffer: 1 << 20,
  });
  if (child.error || child.status !== 0 || child.signal) {
    throw new Error(`packaged settings CLI failed (${child.error?.code || child.status || child.signal})`);
  }
  const snapshot = JSON.parse(child.stdout);
  if (!settingsPersistenceMatches(document, snapshot, expected) ||
      snapshot.revision !== createHash('sha256').update(raw).digest('hex')) {
    throw new Error('saved settings and fresh packaged CLI disagree');
  }
  if ((!profileChoices.length && Object.hasOwn(document, 'model_profile')) || snapshot.fields.model_profile?.choice_only !== true ||
      JSON.stringify(snapshot.fields.model_profile.options || []) !== JSON.stringify(profileChoices)) {
    throw new Error('model profile catalog or choice contract changed');
  }
  for (const key of absentKeys) {
    if (Object.hasOwn(document, key) || snapshot.fields[key]?.stored_value !== null) {
      throw new Error('conflicting override was not removed');
    }
  }
  for (const name of ['settings.json', 'settings.local.json']) {
    if (fs.existsSync(path.join(workspace, '.claude', name))) {
      throw new Error('user settings unexpectedly wrote workspace configuration');
    }
  }
  return snapshot;
}

// Uses the normal form -> preload IPC -> real app-server path. The independent
// file and fresh-process oracles do not use the renderer's returned snapshot.
async function acceptSharedSettings({ evaluate, poll, reload, backendPath, environment, workspace }) {
  const keys = Object.keys(FINAL_SETTINGS);
  const readProjection = () => evaluate(`(() => {
    const keys = ${JSON.stringify(keys)};
    const values = {}, sources = {};
    for (const key of keys) {
      values[key] = document.getElementById('setting-' + key)?.value;
      sources[key] = document.querySelector('[data-setting="' + key + '"] .setting-source')?.textContent || '';
    }
    return {
      open: document.querySelector('#settings-dialog')?.open === true,
      scope: document.querySelector('#settings-scope')?.value,
      projectDisabled: document.querySelector('#settings-scope [value="project"]')?.disabled === true,
      saveDisabled: document.querySelector('#settings-save')?.disabled === true,
      providerOption: Boolean(document.querySelector('#setting-provider [value="glm"]')),
      profileControl: document.querySelector('#setting-model_profile')?.tagName,
      profileDisabled: document.querySelector('#setting-model_profile')?.disabled === true,
      profilePlaceholder: document.querySelector('#setting-model_profile option:checked')?.textContent,
      status: document.querySelector('#settings-status')?.textContent || '',
      theme: document.documentElement.dataset.theme,
      reducedMotion: document.documentElement.dataset.reducedMotion,
      values, sources,
    };
  })()`);
  const emptySessions = async () => {
    const result = await evaluate(`(async () => {
      const response = await globalThis.yhcDesktop.api('listSessions');
      return { valid: Array.isArray(response?.sessions), count: response?.sessions?.length };
    })()`);
    if (result?.valid !== true || result.count !== 0) {
      throw new Error('settings-only acceptance unexpectedly has live sessions');
    }
  };
  const open = async () => {
    const clicked = await evaluate(`(() => {
      const button = document.querySelector('#settings-open');
      if (!button || button.disabled) return false;
      button.click(); return true;
    })()`);
    if (!clicked) throw new Error('shared settings launcher is unavailable');
    await poll(async () => {
      const probe = await readProjection();
      return probe?.open && probe.providerOption && probe.values.max_turns !== undefined &&
        probe.status === 'Up to date' ? probe : null;
    }, SETTINGS_TIMEOUT_MS, 'shared settings load');
  };
  await emptySessions();
  await open();
  for (const expected of [INITIAL_SETTINGS, FINAL_SETTINGS]) {
    const submitted = await evaluate(`(() => {
      for (const [key, value] of Object.entries(${JSON.stringify(expected)})) {
        const field = document.getElementById('setting-' + key);
        if (!field || field.disabled) return false;
        field.value = String(value);
        field.dispatchEvent(new Event('input', { bubbles: true }));
      }
      const save = document.querySelector('#settings-save');
      if (!save || save.disabled || !document.querySelector('#settings-form').checkValidity()) return false;
      save.click(); return true;
    })()`);
    if (!submitted) throw new Error('shared settings form could not submit typed changes');
    await poll(async () => {
      const probe = await readProjection();
      return settingsProjectionMatches(probe, expected, 'Saved.') ? probe : null;
    }, SETTINGS_TIMEOUT_MS, 'shared settings save');
    verifySettingsPersistence({ backendPath, environment, workspace, expected });
  }
  await evaluate('globalThis.__yhcSettingsBeforeReload = true; true');
  await reload();
  await poll(
    () => evaluate('globalThis.__yhcSettingsBeforeReload === undefined'),
    SETTINGS_TIMEOUT_MS, 'fresh settings renderer',
  );
  await open();
  const reloaded = await readProjection();
  if (!settingsProjectionMatches(reloaded, FINAL_SETTINGS, 'Up to date')) {
    throw new Error('reloaded renderer lost saved settings or provenance');
  }
  verifySettingsPersistence({ backendPath, environment, workspace, expected: FINAL_SETTINGS });
  await evaluate('document.querySelector("#settings-close").click(); true');
  await poll(
    () => evaluate('document.querySelector("#settings-dialog").open === false'),
    SETTINGS_TIMEOUT_MS, 'settings dialog close',
  );
  await emptySessions();
  await acceptModelSelection({ evaluate, poll, reload, backendPath, environment, workspace });
  await emptySessions();
  return { status: 'pass', typed_zero_false: true, cli_file_agreement: true, renderer_reload: true, empty_profile_select_only: true, model_selection_confirmation: true };
}

// Test-owned non-secret portfolio. No credential is supplied or model called.
async function acceptModelSelection({ evaluate, poll, reload, backendPath, environment, workspace }) {
  const settingsPath = path.join(environment.HOME, '.claude', 'settings.json');
  const definitions = {
    provider_accounts: { 'smoke-account': { provider: 'openai', base_url: 'https://api.example/v1', auth: { kind: 'env', name: 'YHC_SETTINGS_FIXTURE_KEY' } } },
    model_profiles: { 'smoke-primary': { account: 'smoke-account', api_model: 'gpt-4o', project_selectable: true } },
    private_extension: { sentinel: 'preserve-fixture' },
  };
  const seeded = { ...JSON.parse(fs.readFileSync(settingsPath)), ...definitions, api_base_url: 'https://api.example/v1' };
  let original = JSON.stringify(seeded);
  fs.writeFileSync(settingsPath, original, { mode: 0o600 });
  const readProjection = () => evaluate(`(() => ({
    status: document.querySelector('#settings-status')?.textContent,
    confirmation: document.querySelector('#confirm-dialog')?.open,
    copy: document.querySelector('#confirm-copy')?.textContent,
    closeDisabled: document.querySelector('#settings-close')?.disabled,
    saveDisabled: document.querySelector('#settings-save')?.disabled,
    selected: document.querySelector('#setting-model_profile')?.value,
    provider: document.querySelector('#setting-provider')?.value,
    model: document.querySelector('#setting-model')?.value,
    endpoint: document.querySelector('#setting-api_base_url')?.value,
    focused: document.activeElement?.id,
  }))()`);
  const open = async () => {
    await evaluate('document.querySelector("#settings-open").click(); true');
    await poll(async () => {
      const probe = await readProjection();
      return probe?.status === 'Up to date' && await evaluate('Boolean(document.querySelector("#setting-model_profile [value=smoke-primary]"))') ? probe : null;
    }, SETTINGS_TIMEOUT_MS, 'model selection catalog');
  };
  const selectAndSave = async () => {
    const submitted = await evaluate(`(() => {
      const profile = document.querySelector('#setting-model_profile');
      if (!profile || profile.disabled) return false;
      profile.value = 'smoke-primary'; profile.dispatchEvent(new Event('input', { bubbles: true }));
      const save = document.querySelector('#settings-save');
      if (save.disabled) return false;
      save.click(); return true;
    })()`);
    if (!submitted) throw new Error('profile selection could not submit');
    await poll(async () => {
      const probe = await readProjection();
      return probe?.confirmation && probe.closeDisabled ? probe : null;
    }, SETTINGS_TIMEOUT_MS, 'model selection confirmation');
    const probe = await readProjection();
    assert.equal(probe.copy, 'Save Model profile and remove Provider, Model, API endpoint overrides from User defaults? Other scopes and current sessions will stay unchanged.');
    assert.equal(fs.readFileSync(settingsPath, 'utf8'), original, 'confirmation must precede any write');
  };
  await open();
  await selectAndSave();
  await evaluate('document.querySelector("#confirm-cancel").click(); true');
  await poll(async () => {
    const probe = await readProjection();
    return !probe?.confirmation && probe.selected === 'smoke-primary' && probe.model === FINAL_SETTINGS.model &&
      !probe.saveDisabled && probe.focused === 'setting-model_profile' ? probe : null;
  }, SETTINGS_TIMEOUT_MS, 'cancelled selection draft');
  assert.equal(fs.readFileSync(settingsPath, 'utf8'), original, 'cancel changed configuration');
  await selectAndSave();
  // An actual second writer changes the revision while the confirmation is open.
  definitions.private_extension.revision_fixture = true;
  original = JSON.stringify({ ...seeded, ...definitions });
  fs.writeFileSync(settingsPath, original, { mode: 0o600 });
  await evaluate('document.querySelector("#confirm-accept").click(); true');
  await poll(async () => {
    const probe = await readProjection();
    return !probe?.confirmation && probe.status?.includes('changed elsewhere') && probe.selected === 'smoke-primary' &&
      probe.provider === FINAL_SETTINGS.provider && probe.model === FINAL_SETTINGS.model &&
      probe.endpoint === seeded.api_base_url && probe.saveDisabled ? probe : null;
  }, SETTINGS_TIMEOUT_MS, 'real revision conflict retains original selection draft');
  assert.equal(fs.readFileSync(settingsPath, 'utf8'), original, 'conflicting save changed the second writer document');
  // Discard is also explicit, then reload the fresh revision before retrying.
  await evaluate('document.querySelector("#settings-reload").click(); true');
  await poll(() => evaluate('document.querySelector("#confirm-dialog").open === true && document.querySelector("#confirm-title").textContent === "Discard unsaved settings?"'), SETTINGS_TIMEOUT_MS, 'conflict draft discard confirmation');
  await evaluate('document.querySelector("#confirm-accept").click(); true');
  await poll(async () => {
    const probe = await readProjection();
    return probe?.status === 'Up to date' && probe.selected === '' && probe.model === FINAL_SETTINGS.model ? probe : null;
  }, SETTINGS_TIMEOUT_MS, 'fresh conflict revision');
  await selectAndSave();
  await evaluate('document.querySelector("#confirm-accept").click(); true');
  await poll(async () => {
    const probe = await readProjection();
    return probe?.status?.startsWith('Saved.') && probe.selected === 'smoke-primary' && probe.saveDisabled ? probe : null;
  }, SETTINGS_TIMEOUT_MS, 'confirmed model selection');
  const { provider, model, ...unchanged } = FINAL_SETTINGS;
  const expected = { ...unchanged, model_profile: 'smoke-primary' };
  const verify = () => {
    verifySettingsPersistence({ backendPath, environment, workspace, expected,
      profileChoices: ['smoke-primary'], absentKeys: ['provider', 'model', 'api_base_url'] });
    const saved = JSON.parse(fs.readFileSync(settingsPath));
    for (const [key, value] of Object.entries(definitions)) assert.deepEqual(saved[key], value, 'profile definitions or unknown fields were lost');
  };
  verify();
  await evaluate('document.querySelector("#settings-close").click(); true');
  await evaluate('globalThis.__yhcSettingsBeforeReload = true; true');
  await reload();
  await open();
  const reloaded = await readProjection();
  assert.equal(reloaded.selected, 'smoke-primary');
  assert.equal(reloaded.saveDisabled, true);
  verify();
  await evaluate('document.querySelector("#settings-close").click(); true');
  await poll(() => evaluate('document.querySelector("#settings-dialog").open === false'), SETTINGS_TIMEOUT_MS, 'model settings close');
}

module.exports = {
  acceptSharedSettings, settingsPersistenceMatches, settingsProjectionMatches, verifySettingsPersistence,
};
