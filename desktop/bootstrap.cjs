const BUILD_FIELDS = new Set(['commit', 'modified', 'version']);
const VERSION_PATTERN = /^[0-9][0-9A-Za-z.+-]{0,63}$/;
const COMMIT_PATTERN = /^(?:unknown|[0-9a-f]{12})$/;

function invalidBootstrap() {
  return new Error('invalid backend bootstrap');
}

function record(value) {
  return Boolean(value) && typeof value === 'object' && !Array.isArray(value);
}

function parseBuildIdentity(value, shellVersion) {
  if (!record(value) || Object.keys(value).length !== BUILD_FIELDS.size) {
    throw invalidBootstrap();
  }
  for (const field of Object.keys(value)) {
    if (!BUILD_FIELDS.has(field)) throw invalidBootstrap();
  }
  if (
    typeof shellVersion !== 'string' ||
    !VERSION_PATTERN.test(shellVersion) ||
    typeof value.version !== 'string' ||
    !VERSION_PATTERN.test(value.version) ||
    value.version !== shellVersion ||
    typeof value.commit !== 'string' ||
    !COMMIT_PATTERN.test(value.commit) ||
    typeof value.modified !== 'boolean'
  ) {
    throw invalidBootstrap();
  }
  return Object.freeze({
    version: value.version,
    commit: value.commit,
    modified: value.modified,
  });
}

function parseBackendBootstrap(value, shellVersion) {
  if (
    !record(value) ||
    typeof value.url !== 'string' ||
    value.url.length === 0 ||
    typeof value.token !== 'string' ||
    value.token.length === 0 ||
    value.protocol_version !== 2
  ) {
    throw invalidBootstrap();
  }
  return Object.freeze({
    url: value.url,
    token: value.token,
    webURL: typeof value.web_url === 'string'
      ? value.web_url
      : (typeof value.webURL === 'string' ? value.webURL : ''),
    protocolVersion: value.protocol_version,
    pid: value.pid,
    build: parseBuildIdentity(value.build, shellVersion),
  });
}

function rejectBackendBootstrap(child, stoppingBackends, settle, error) {
  stoppingBackends.add(child);
  try {
    child.kill('SIGKILL');
  } catch {
    // The exit/error handlers remain the process-lifecycle authority.
  } finally {
    settle(error);
  }
}

module.exports = {
  parseBackendBootstrap,
  rejectBackendBootstrap,
};
