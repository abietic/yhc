import assert from 'node:assert/strict';
import { spawnSync } from 'node:child_process';
import { EventEmitter } from 'node:events';
import { mkdtemp, readFile, rm } from 'node:fs/promises';
import { createRequire } from 'node:module';
import os from 'node:os';
import path from 'node:path';
import test from 'node:test';
import { fileURLToPath } from 'node:url';

const runner = fileURLToPath(new URL('../scripts/workbench_ui_acceptance.cjs', import.meta.url));
const require = createRequire(import.meta.url);
const { listenFixtureServer } = require('../scripts/fixture_server_startup.cjs');

test('fixture startup resolves only after the loopback listen callback and releases its listener', async () => {
  const server = new EventEmitter();
  server.listen = (port, host, callback) => {
    assert.equal(port, 0);
    assert.equal(host, '127.0.0.1');
    queueMicrotask(callback);
  };
  await listenFixtureServer(server);
  assert.equal(server.listenerCount('error'), 0);
});

test('fixture startup propagates both emitted and synchronous listen failures', async () => {
  for (const emitted of [true, false]) {
    const server = new EventEmitter();
    const failure = new Error('fixture-owned-listen-failure');
    server.listen = () => {
      if (emitted) queueMicrotask(() => server.emit('error', failure));
      else throw failure;
    };
    await assert.rejects(listenFixtureServer(server), error => error === failure);
    assert.equal(server.listenerCount('error'), 0);
  }
});

test('a fixture with no callback or error has a bounded startup failure', async () => {
  const server = new EventEmitter();
  let callback;
  server.listen = (_port, _host, onListen) => { callback = onListen; };
  await assert.rejects(listenFixtureServer(server, 10), /Fixture server did not start/);
  assert.equal(server.listenerCount('error'), 0);
  callback(); // A late callback cannot turn the rejected startup into success.
});

test('UI acceptance fails and reports a fixture server that never starts', async t => {
  const outputDir = await mkdtemp(path.join(os.tmpdir(), 'yhc-ui-startup-test-'));
  t.after(() => rm(outputDir, { recursive: true, force: true }));
  const bootstrap = `
    const { EventEmitter } = require('node:events');
    const Module = require('node:module');
    const originalLoad = Module._load;
    Module._load = function (request, ...args) {
      if (request === 'playwright') return { chromium: {
        launch: async () => { throw new Error('Browser must not start before the fixture'); },
      } };
      if (request === 'node:http') return { createServer: () => {
        const server = new EventEmitter();
        server.listen = () => server;
        server.closeAllConnections = () => {};
        server.close = callback => { callback?.(); return server; };
        return server;
      } };
      return originalLoad.call(this, request, ...args);
    };
    require(process.argv[1]);
  `;
  const child = spawnSync(process.execPath, ['-e', bootstrap, runner, '--output-dir', outputDir], {
    encoding: 'utf8',
    env: { ...process.env, PLAYWRIGHT_MODULE_PATH: 'playwright' },
    timeout: 12000,
  });
  assert.equal(child.error, undefined, 'The runner must settle without being killed by the test');
  assert.equal(child.signal, null);
  assert.equal(child.status, 1, `Unstarted fixture must not be a green run: ${child.stdout} ${child.stderr}`);
  const report = JSON.parse(await readFile(path.join(outputDir, 'results.json'), 'utf8'));
  assert.deepEqual(report.errors, []);
  assert.deepEqual(report.results.map(({ name, status }) => ({ name, status })), [
    { name: 'Acceptance runner', status: 'fail' },
  ]);
  assert.match(report.results[0].error, /fixture server.*start/i);
  assert.doesNotMatch(child.stdout + child.stderr, /Browser must not start/);
});
