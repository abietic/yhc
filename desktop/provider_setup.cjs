const nodeFS = require('node:fs/promises');
const { randomUUID } = require('node:crypto');
const nodePath = require('node:path');

const PROVIDERS = new Set(['anthropic', 'openai', 'google', 'deepseek', 'qwen', 'ark']);
const CONTROL_CHARACTER = /[\u0000-\u001F\u007F]/;
const MAX_MODEL_LENGTH = 256;
const MAX_KEY_LENGTH = 4096;
const MAX_BASE_URL_LENGTH = 2048;
const MAX_PROFILE_BYTES = 16 << 10;
const PROFILE_VERSION = 1;
const PROFILE_MISSING_CODE = 'provider_profile_missing';
const PROFILE_UNAVAILABLE_CODE = 'provider_profile_unavailable';
const PROVIDER_SPECIFIC_KEY_ENVIRONMENTS = [
  'ANTHROPIC_API_KEY',
  'OPENAI_API_KEY',
  'GOOGLE_API_KEY',
  'GEMINI_API_KEY',
  'DEEPSEEK_API_KEY',
  'DASHSCOPE_API_KEY',
  'QWEN_API_KEY',
  'ARK_API_KEY',
];

function requireText(value, label, maxLength) {
  if (typeof value !== 'string' || value.length === 0 || value.length > maxLength ||
      CONTROL_CHARACTER.test(value)) {
    throw new TypeError(`valid ${label} required`);
  }
  return value;
}

function normalizeProvider(value) {
  if (typeof value !== 'string') throw new TypeError('supported provider required');
  const provider = value.trim().toLowerCase();
  if (!PROVIDERS.has(provider)) throw new TypeError('supported provider required');
  return provider;
}

function normalizeModel(value) {
  if (value === undefined || value === null || value === '') return 'default';
  if (typeof value !== 'string') throw new TypeError('valid model required');
  return requireText(value.trim(), 'model', MAX_MODEL_LENGTH);
}

function normalizeBaseURL(value) {
  if (value === undefined || value === null || value === '') return undefined;
  if (typeof value !== 'string') throw new TypeError('valid base URL required');
  const normalized = value.trim();
  if (normalized.length === 0 || normalized.length > MAX_BASE_URL_LENGTH ||
      CONTROL_CHARACTER.test(normalized)) {
    throw new TypeError('valid base URL required');
  }
  let url;
  try {
    url = new URL(normalized);
  } catch {
    throw new TypeError('valid base URL required');
  }
  if ((url.protocol !== 'http:' && url.protocol !== 'https:') || !url.hostname ||
      url.username || url.password || url.search || url.hash) {
    throw new TypeError('valid base URL required');
  }
  return url.toString();
}

function validateProviderSubmission(input) {
  if (!input || typeof input !== 'object' || Array.isArray(input)) {
    throw new TypeError('provider submission required');
  }
  const apiKey = requireText(input.apiKey, 'provider key', MAX_KEY_LENGTH);
  if (apiKey.trim().length === 0) throw new TypeError('valid provider key required');
  const submission = {
    provider: normalizeProvider(input.provider),
    model: normalizeModel(input.model),
    apiKey,
  };
  const baseURL = normalizeBaseURL(input.baseURL);
  if (baseURL !== undefined) submission.baseURL = baseURL;
  return submission;
}

function encryptionUsable(safeStorage, platform) {
  try {
    if (!safeStorage || safeStorage.isEncryptionAvailable() !== true) return false;
    if (platform === 'linux') {
      return safeStorage.getSelectedStorageBackend() !== 'basic_text';
    }
    return true;
  } catch {
    return false;
  }
}

function ambientProviderConfigured(environment) {
  if (!environment || typeof environment !== 'object' || Array.isArray(environment)) {
    return false;
  }
  const present = (name) => (
    typeof environment[name] === 'string' && environment[name].trim().length > 0
  );
  if (present('PROV') && present('PROV_API_KEY')) return true;
  return PROVIDER_SPECIFIC_KEY_ENVIRONMENTS.some(present);
}

function profileDependencies(deps) {
  if (!deps || typeof deps.profilePath !== 'string' || deps.profilePath.length === 0) {
    throw new TypeError('profile path required');
  }
  return { fs: deps.fs || nodeFS, profilePath: deps.profilePath };
}

function publicStatus(metadata) {
  const status = { configured: true, provider: metadata.provider, model: metadata.model };
  if (metadata.baseURL !== undefined) status.baseURL = metadata.baseURL;
  return status;
}

function profileMetadata(profile) {
  if (!profile || typeof profile !== 'object' || Array.isArray(profile) || profile.version !== PROFILE_VERSION) {
    throw new TypeError('provider profile invalid');
  }
  const metadata = {
    provider: normalizeProvider(profile.provider),
    model: requireText(profile.model, 'model', MAX_MODEL_LENGTH),
  };
  if (Object.hasOwn(profile, 'base_url')) {
    const baseURL = normalizeBaseURL(profile.base_url);
    if (baseURL === undefined) throw new TypeError('provider profile invalid');
    metadata.baseURL = baseURL;
  }
  if (typeof profile.encrypted_key !== 'string' || profile.encrypted_key.length === 0 ||
      !/^[A-Za-z0-9+/]+={0,2}$/.test(profile.encrypted_key) ||
      Buffer.from(profile.encrypted_key, 'base64').toString('base64') !== profile.encrypted_key) {
    throw new TypeError('provider profile invalid');
  }
  const keys = Object.keys(profile).sort();
  const expected = [
    ...(Object.hasOwn(profile, 'base_url') ? ['base_url'] : []),
    'encrypted_key', 'model', 'provider', 'version',
  ].sort();
  if (keys.length !== expected.length || keys.some((key, index) => key !== expected[index])) {
    throw new TypeError('provider profile invalid');
  }
  return { ...metadata, encryptedKey: profile.encrypted_key };
}

async function loadProfile(deps) {
  const { fs, profilePath } = profileDependencies(deps);
  let content;
  try {
    content = await fs.readFile(profilePath, 'utf8');
  } catch (error) {
    const unavailable = new Error('provider profile unavailable');
    unavailable.code = error?.code === 'ENOENT'
      ? PROFILE_MISSING_CODE
      : PROFILE_UNAVAILABLE_CODE;
    throw unavailable;
  }
  try {
    if (Buffer.byteLength(content, 'utf8') > MAX_PROFILE_BYTES) {
      throw new TypeError('provider profile invalid');
    }
    return profileMetadata(JSON.parse(content));
  } catch (error) {
    if (error instanceof TypeError) throw error;
    throw new TypeError('provider profile invalid');
  }
}

async function writeProviderProfile(deps, input) {
  const { fs, profilePath } = profileDependencies(deps);
  if (!encryptionUsable(deps.safeStorage, deps.platform)) {
    throw new Error('provider profile encryption unavailable');
  }
  const submission = validateProviderSubmission(input);
  let encryptedKey;
  try {
    encryptedKey = deps.safeStorage.encryptString(submission.apiKey);
  } catch {
    throw new Error('provider profile encryption failed');
  }
  if (!Buffer.isBuffer(encryptedKey)) throw new Error('provider profile encryption failed');
  const profile = {
    version: PROFILE_VERSION,
    provider: submission.provider,
    model: submission.model,
    ...(submission.baseURL === undefined ? {} : { base_url: submission.baseURL }),
    encrypted_key: encryptedKey.toString('base64'),
  };
  const temporaryPath = `${profilePath}.${process.pid}.${randomUUID()}.tmp`;
  try {
    await fs.mkdir(nodePath.dirname(profilePath), { recursive: true });
    await fs.writeFile(temporaryPath, JSON.stringify(profile), {
      encoding: 'utf8',
      flag: 'wx',
      mode: 0o600,
    });
    await fs.chmod(temporaryPath, 0o600);
    await fs.rename(temporaryPath, profilePath);
    await fs.chmod(profilePath, 0o600);
  } catch {
    try { await fs.unlink(temporaryPath); } catch {}
    throw new Error('provider profile write failed');
  }
  return publicStatus(submission);
}

async function providerProfileStatus(deps) {
  try {
    return publicStatus(await loadProfile(deps));
  } catch (error) {
    return error?.code === PROFILE_MISSING_CODE
      ? { configured: false }
      : { configured: false, errorCode: 'stored_profile_unavailable' };
  }
}

async function readProviderLaunchProfile(deps) {
  if (!encryptionUsable(deps?.safeStorage, deps?.platform)) {
    throw new Error('provider profile encryption unavailable');
  }
  const profile = await loadProfile(deps);
  let apiKey;
  try {
    if (!deps.safeStorage || typeof deps.safeStorage.decryptString !== 'function') throw new Error('missing');
    apiKey = deps.safeStorage.decryptString(Buffer.from(profile.encryptedKey, 'base64'));
    requireText(apiKey, 'provider key', MAX_KEY_LENGTH);
    if (apiKey.trim().length === 0) throw new Error('empty');
  } catch {
    throw new Error('provider profile decrypt failed');
  }
  return {
    provider: profile.provider,
    model: profile.model,
    ...(profile.baseURL === undefined ? {} : { baseURL: profile.baseURL }),
    apiKey,
  };
}

function providerLaunchEnvironment(baseEnv, launchProfile) {
  if (!baseEnv || typeof baseEnv !== 'object' || Array.isArray(baseEnv)) {
    throw new TypeError('base environment required');
  }
  const profile = validateProviderSubmission(launchProfile);
  const environment = { ...baseEnv };
  delete environment.PROV_BASE_URL;
  environment.PROV = profile.provider;
  environment.PROV_MODEL = profile.model;
  environment.PROV_API_KEY = profile.apiKey;
  if (profile.baseURL !== undefined) environment.PROV_BASE_URL = profile.baseURL;
  return environment;
}

function sanitizedProviderStatus(input) {
  if (!input || input.configured !== true) {
    throw new TypeError('configured provider status required');
  }
  const status = {
    configured: true,
    provider: normalizeProvider(input.provider),
    model: normalizeModel(input.model),
  };
  const baseURL = normalizeBaseURL(input.baseURL);
  if (baseURL !== undefined) status.baseURL = baseURL;
  return Object.freeze(status);
}

function requireNoLiveSessions(response) {
  if (!response || !Array.isArray(response.sessions)) {
    throw new Error('live sessions could not be verified');
  }
  if (response.sessions.length > 0) {
    throw new Error('close all live sessions before changing provider setup');
  }
}

function createProviderRestartCoordinator(deps) {
  const required = [
    'inspectSessions',
    'persistProfile',
    'stopEventStreams',
    'stopBackend',
    'startBackend',
  ];
  if (!deps || required.some((name) => typeof deps[name] !== 'function')) {
    throw new TypeError('provider restart dependencies required');
  }
  let inFlight = null;
  return Object.freeze({
    configure(submission) {
      if (inFlight) return inFlight;
      const run = (async () => {
        requireNoLiveSessions(await deps.inspectSessions());
        const status = sanitizedProviderStatus(await deps.persistProfile(submission));
        await deps.stopEventStreams();
        await deps.stopBackend();
        await deps.startBackend();
        return status;
      })();
      const current = run.finally(() => {
        if (inFlight === current) inFlight = null;
      });
      inFlight = current;
      return current;
    },
  });
}

function providerSetupStorageAvailable(safeStorage, platform) {
  if (platform !== 'darwin') return encryptionUsable(safeStorage, platform);
  return Boolean(
    safeStorage &&
    typeof safeStorage.encryptString === 'function' &&
    typeof safeStorage.decryptString === 'function',
  );
}

module.exports = {
  ambientProviderConfigured,
  createProviderRestartCoordinator,
  encryptionUsable,
  providerLaunchEnvironment,
  providerProfileStatus,
  providerSetupStorageAvailable,
  readProviderLaunchProfile,
  validateProviderSubmission,
  writeProviderProfile,
};
