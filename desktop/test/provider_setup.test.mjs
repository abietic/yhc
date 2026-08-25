import assert from 'node:assert/strict';
import { mkdtemp, readFile, stat, writeFile } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { createRequire } from 'node:module';
import test from 'node:test';

const require = createRequire(import.meta.url);
const {
  ambientProviderConfigured,
  createProviderRestartCoordinator,
  encryptionUsable,
  providerLaunchEnvironment,
  providerProfileStatus, providerSetupStorageAvailable,
  readProviderLaunchProfile,
  validateProviderSubmission,
  writeProviderProfile,
} = require('../provider_setup.cjs');

const sentinel = 'TEST-ONLY-PROVIDER-KEY-SENTINEL';

function fakeSafeStorage({ available = true, backend = 'secure' } = {}) {
  return {
    isEncryptionAvailable: () => available,
    getSelectedStorageBackend: () => backend,
    encryptString: (value) => Buffer.from(`encrypted:${value}`, 'utf8'),
    decryptString: (value) => {
      const decoded = Buffer.from(value).toString('utf8');
      if (!decoded.startsWith('encrypted:')) throw new Error('invalid ciphertext');
      return decoded.slice('encrypted:'.length);
    },
  };
}

async function profilePath() {
  return join(await mkdtemp(join(tmpdir(), 'yhc-provider-')), 'provider-profile.json');
}

test('validates and normalizes only supported provider submissions', () => {
  assert.deepEqual(validateProviderSubmission({
    provider: '  DeepSeek ', model: '', baseURL: 'https://api.example.test/v1', apiKey: sentinel,
  }), {
    provider: 'deepseek', model: 'default', baseURL: 'https://api.example.test/v1', apiKey: sentinel,
  });
  for (const submission of [
    { provider: 'unknown', apiKey: sentinel },
    { provider: 'openai', model: '   ', apiKey: sentinel },
    { provider: 'openai', model: '\u0000', apiKey: sentinel },
    { provider: 'openai', baseURL: 'ftp://example.test', apiKey: sentinel },
    { provider: 'openai', baseURL: 'https://user:pass@example.test', apiKey: sentinel },
    { provider: 'openai', baseURL: 'https://example.test/?q=1', apiKey: sentinel },
    { provider: 'openai', baseURL: 'https://example.test/#part', apiKey: sentinel },
    { provider: 'openai', baseURL: `https://example.test/${'a'.repeat(2049)}`, apiKey: sentinel },
    { provider: 'openai', apiKey: '' },
    { provider: 'openai', apiKey: '   ' },
    { provider: 'openai', apiKey: 'contains\ncontrol' },
    { provider: 'openai', apiKey: 'x'.repeat(4097) },
  ]) {
    assert.throws(() => validateProviderSubmission(submission), TypeError);
  }
});

test('rejects unavailable and Linux basic-text encryption', () => {
  assert.equal(encryptionUsable(fakeSafeStorage({ available: false }), 'darwin'), false);
  assert.equal(encryptionUsable(fakeSafeStorage({ backend: 'basic_text' }), 'linux'), false);
  assert.equal(encryptionUsable({ isEncryptionAvailable: () => true }, 'linux'), false);
  assert.equal(encryptionUsable(fakeSafeStorage(), 'darwin'), true);
});

test('writes owner-only encrypted versioned profile with no plaintext secret', async () => {
  const path = await profilePath();
  const safeStorage = fakeSafeStorage();
  const status = await writeProviderProfile({
    profilePath: path, safeStorage, platform: 'darwin',
  }, {
    provider: 'openai', model: 'gpt-test', baseURL: 'https://api.example.test/v1', apiKey: sentinel,
  });
  const serialized = await readFile(path, 'utf8');
  const saved = JSON.parse(serialized);
  assert.deepEqual(status, {
    configured: true, provider: 'openai', model: 'gpt-test', baseURL: 'https://api.example.test/v1',
  });
  assert.deepEqual(Object.keys(saved).sort(), ['base_url', 'encrypted_key', 'model', 'provider', 'version']);
  assert.equal(saved.version, 1);
  assert.match(saved.encrypted_key, /^[A-Za-z0-9+/]+={0,2}$/);
  assert.equal(serialized.includes(sentinel), false);
  assert.equal((await stat(path)).mode & 0o777, 0o600);
  assert.deepEqual(await readProviderLaunchProfile({
    profilePath: path, safeStorage, platform: 'darwin',
  }), {
    provider: 'openai',
    model: 'gpt-test',
    baseURL: 'https://api.example.test/v1',
    apiKey: sentinel,
  });
});

test('status never decrypts or reveals the secret', async () => {
  const path = await profilePath();
  const safeStorage = fakeSafeStorage();
  await writeProviderProfile({ profilePath: path, safeStorage, platform: 'darwin' }, {
    provider: 'qwen', apiKey: sentinel,
  });
  const status = await providerProfileStatus({
    profilePath: path,
    safeStorage: { decryptString: () => { throw new Error('must not decrypt'); } },
  });
  assert.deepEqual(status, { configured: true, provider: 'qwen', model: 'default' });
  assert.equal(JSON.stringify(status).includes(sentinel), false);
});

test('malformed and undecryptable profiles fail closed', async () => {
  const path = await profilePath();
  await assert.rejects(
    readProviderLaunchProfile({
      profilePath: path, safeStorage: fakeSafeStorage(), platform: 'darwin',
    }),
    /provider profile unavailable/,
  );
  await writeFile(path, '{not-json}', { mode: 0o600 });
  assert.deepEqual(await providerProfileStatus({ profilePath: path }), {
    configured: false,
    errorCode: 'stored_profile_unavailable',
  });
  await assert.rejects(
    readProviderLaunchProfile({
      profilePath: path, safeStorage: fakeSafeStorage(), platform: 'darwin',
    }),
    /provider profile invalid/,
  );
  await writeFile(path, JSON.stringify({
    version: 1, provider: 'openai', model: 'default', encrypted_key: Buffer.from('bad').toString('base64'),
  }), { mode: 0o600 });
  await assert.rejects(
    readProviderLaunchProfile({
      profilePath: path, safeStorage: fakeSafeStorage(), platform: 'darwin',
    }),
    /provider profile decrypt failed/,
  );
  await assert.rejects(
    readProviderLaunchProfile({
      profilePath: path,
      safeStorage: fakeSafeStorage({ backend: 'basic_text' }),
      platform: 'linux',
    }),
    /provider profile encryption unavailable/,
  );
  await writeFile(path, JSON.stringify({
    version: 1,
    provider: 'openai',
    model: 'default',
    encrypted_key: Buffer.from('encrypted:test').toString('base64'),
    plaintext_key: sentinel,
  }), { mode: 0o600 });
  assert.deepEqual(await providerProfileStatus({ profilePath: path }), {
    configured: false,
    errorCode: 'stored_profile_unavailable',
  });
});

test('profile status distinguishes an absent profile from unreadable storage', async () => {
  const path = await profilePath();
  assert.deepEqual(await providerProfileStatus({ profilePath: path }), {
    configured: false,
  });

  const unavailable = new Error('permission denied');
  unavailable.code = 'EACCES';
  assert.deepEqual(await providerProfileStatus({
    profilePath: path,
    fs: { readFile: async () => { throw unavailable; } },
  }), {
    configured: false,
    errorCode: 'stored_profile_unavailable',
  });
});

test('projects a cloned launch environment and removes stale optional base URL', () => {
  const base = { PATH: '/bin', PROV_BASE_URL: 'https://stale.example.test', OTHER: 'keep' };
  const withURL = providerLaunchEnvironment(base, {
    provider: 'ark', model: 'model-a', apiKey: sentinel, baseURL: 'https://api.example.test',
  });
  assert.deepEqual(withURL, {
    PATH: '/bin', OTHER: 'keep', PROV: 'ark', PROV_MODEL: 'model-a',
    PROV_API_KEY: sentinel, PROV_BASE_URL: 'https://api.example.test/',
  });
  const withoutURL = providerLaunchEnvironment(base, {
    provider: 'google', model: 'default', apiKey: sentinel,
  });
  assert.equal(withoutURL.PROV_BASE_URL, undefined);
  assert.deepEqual(base, { PATH: '/bin', PROV_BASE_URL: 'https://stale.example.test', OTHER: 'keep' });
});

test('detects only credible ambient provider environment without reading secrets', () => {
  assert.equal(ambientProviderConfigured({}), false);
  assert.equal(ambientProviderConfigured({ PROV: 'openai' }), false);
  assert.equal(ambientProviderConfigured({ PROV_API_KEY: sentinel }), false);
  assert.equal(ambientProviderConfigured({ PROV: 'openai', PROV_API_KEY: sentinel }), true);
  assert.equal(ambientProviderConfigured({ OPENAI_API_KEY: sentinel }), true);
  assert.equal(ambientProviderConfigured({ PROV: 'openai', PROV_API_KEY: '   ' }), false);
  assert.equal(ambientProviderConfigured(null), false);
});

test('provider restart rejects malformed or non-empty live session inspection', async () => {
  for (const sessions of [undefined, null, {}, { sessions: null }]) {
    let persisted = false;
    const coordinator = createProviderRestartCoordinator({
      inspectSessions: async () => sessions,
      persistProfile: async () => { persisted = true; },
      stopEventStreams: () => {},
      stopBackend: async () => {},
      startBackend: async () => {},
    });
    await assert.rejects(coordinator.configure({}), /live sessions could not be verified/);
    assert.equal(persisted, false);
  }

  let persisted = false;
  const coordinator = createProviderRestartCoordinator({
    inspectSessions: async () => ({ sessions: [{ id: 'live-session' }] }),
    persistProfile: async () => { persisted = true; },
    stopEventStreams: () => {},
    stopBackend: async () => {},
    startBackend: async () => {},
  });
  await assert.rejects(coordinator.configure({}), /close all live sessions/);
  assert.equal(persisted, false);
});

test('provider restart persists then stops streams and backend before starting', async () => {
  const order = [];
  const coordinator = createProviderRestartCoordinator({
    inspectSessions: async () => { order.push('inspect'); return { sessions: [] }; },
    persistProfile: async () => {
      order.push('persist');
      return {
        configured: true,
        provider: 'openai',
        model: 'default',
        baseURL: 'https://api.example.test/v1',
        apiKey: sentinel,
      };
    },
    stopEventStreams: async () => { order.push('stop-streams'); },
    stopBackend: async () => { order.push('stop'); },
    startBackend: async () => { order.push('start'); },
  });
  const status = await coordinator.configure({ provider: 'openai', apiKey: sentinel });
  assert.deepEqual(order, ['inspect', 'persist', 'stop-streams', 'stop', 'start']);
  assert.deepEqual(status, {
    configured: true,
    provider: 'openai',
    model: 'default',
    baseURL: 'https://api.example.test/v1',
  });
  assert.equal(JSON.stringify(status).includes(sentinel), false);
});

test('provider restart is single-flight and stops before a failed start', async () => {
  let releaseInspection;
  let inspections = 0;
  const coordinator = createProviderRestartCoordinator({
    inspectSessions: () => {
      inspections += 1;
      return new Promise((resolve) => { releaseInspection = () => resolve({ sessions: [] }); });
    },
    persistProfile: async () => ({ configured: true, provider: 'qwen', model: 'default' }),
    stopEventStreams: () => {},
    stopBackend: async () => {},
    startBackend: async () => {},
  });
  const first = coordinator.configure({ provider: 'qwen', apiKey: sentinel });
  const second = coordinator.configure({ provider: 'ark', apiKey: 'ignored-test-value' });
  assert.equal(first, second);
  releaseInspection();
  await first;
  assert.equal(inspections, 1);

  const order = [];
  const failing = createProviderRestartCoordinator({
    inspectSessions: async () => ({ sessions: [] }),
    persistProfile: async () => ({ configured: true, provider: 'ark', model: 'default' }),
    stopEventStreams: () => { order.push('stop-streams'); },
    stopBackend: async () => { order.push('stop'); throw new Error('bounded stop failed'); },
    startBackend: async () => { order.push('start'); },
  });
  await assert.rejects(
    failing.configure({ provider: 'ark', apiKey: sentinel }),
    /bounded stop failed/,
  );
  assert.deepEqual(order, ['stop-streams', 'stop']);
});

test('macOS setup admission does not synchronously probe Keychain availability', () => {
  let availabilityProbes = 0;
  const safeStorage = fakeSafeStorage();
  safeStorage.isEncryptionAvailable = () => {
    availabilityProbes += 1;
    throw new Error('Keychain interaction must stay user initiated');
  };

  assert.equal(providerSetupStorageAvailable(safeStorage, 'darwin'), true);
  assert.equal(availabilityProbes, 0);
  assert.equal(providerSetupStorageAvailable(null, 'darwin'), false);
  assert.equal(providerSetupStorageAvailable({ encryptString() {} }, 'darwin'), false);
  assert.equal(providerSetupStorageAvailable(
    fakeSafeStorage({ available: false }),
    'win32',
  ), false);
});
