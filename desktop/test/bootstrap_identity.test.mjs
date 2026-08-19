import assert from 'node:assert/strict';
import { createRequire } from 'node:module';
import test from 'node:test';

const require = createRequire(import.meta.url);
const {
  parseBackendBootstrap,
  rejectBackendBootstrap,
} = require('../bootstrap.cjs');

function validBootstrap(overrides = {}) {
  return {
    protocol_version: 2,
    url: 'http://127.0.0.1:31337',
    token: 'process-token',
    pid: 42,
    web_url: 'http://127.0.0.1:31337/#pair=token',
    build: {
      version: '0.1.0',
      commit: '0123456789ab',
      modified: false,
    },
    ...overrides,
  };
}

test('backend bootstrap accepts one matching bounded build identity', () => {
  const input = validBootstrap();
  const { token, ...parsed } = parseBackendBootstrap(input, '0.1.0');
  assert.equal(token, input.token);
  assert.deepEqual(parsed, {
    url: input.url,
    webURL: input.web_url,
    protocolVersion: 2,
    pid: 42,
    build: {
      version: '0.1.0',
      commit: '0123456789ab',
      modified: false,
    },
  });

  const unknown = validBootstrap({
    build: { version: '0.1.0', commit: 'unknown', modified: true },
  });
  assert.deepEqual(parseBackendBootstrap(unknown, '0.1.0').build, {
    version: '0.1.0', commit: 'unknown', modified: true,
  });
});

test('backend bootstrap rejects missing, malformed, or mismatched build identity', () => {
  const invalid = [
    [validBootstrap({ build: undefined }), '0.1.0'],
    [validBootstrap({ build: { version: '0.2.0', commit: '0123456789ab', modified: false } }), '0.1.0'],
    [validBootstrap({ build: { version: 'v0.1.0', commit: '0123456789ab', modified: false } }), 'v0.1.0'],
    [validBootstrap({ build: { version: '', commit: '0123456789ab', modified: false } }), '0.1.0'],
    [validBootstrap({ build: { version: `0.1.0\nspoof`, commit: '0123456789ab', modified: false } }), '0.1.0'],
    [validBootstrap({ build: { version: 'x'.repeat(65), commit: '0123456789ab', modified: false } }), '0.1.0'],
    [validBootstrap({ build: { version: '0.1.0', commit: 'short', modified: false } }), '0.1.0'],
    [validBootstrap({ build: { version: '0.1.0', commit: '0123456789aG', modified: false } }), '0.1.0'],
    [validBootstrap({ build: { version: '0.1.0', commit: '0123456789ab', modified: 'false' } }), '0.1.0'],
    [validBootstrap({ build: { version: '0.1.0', commit: '0123456789ab', modified: false, dependencies: [] } }), '0.1.0'],
    [validBootstrap({ protocol_version: 1 }), '0.1.0'],
  ];
  for (const [value, shellVersion] of invalid) {
    assert.throws(
      () => parseBackendBootstrap(value, shellVersion),
      /invalid backend bootstrap/,
    );
  }
});

test('intentional bootstrap rejection suppresses crash projection before kill', () => {
  const order = [];
  const child = {
    kill(signal) {
      order.push(`kill:${signal}`);
      return true;
    },
  };
  const stopping = new WeakSet();
  const failure = new Error('invalid backend bootstrap');
  let settled;

  rejectBackendBootstrap(child, stopping, (error) => {
    order.push('settle');
    settled = error;
  }, failure);

  assert.equal(stopping.has(child), true);
  assert.strictEqual(settled, failure);
  assert.deepEqual(order, ['kill:SIGKILL', 'settle']);
});
