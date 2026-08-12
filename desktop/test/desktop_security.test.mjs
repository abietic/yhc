import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';
import test from 'node:test';

const mainPath = new URL('../main.cjs', import.meta.url);
const packagePath = new URL('../package.json', import.meta.url);
const packageLockPath = new URL('../package-lock.json', import.meta.url);
const preloadPath = new URL('../preload.cjs', import.meta.url);
const rendererPath = new URL('../../internal/webui/assets/index.html', import.meta.url);
const makefilePath = new URL('../../Makefile', import.meta.url);

test('packaged Electron stays above the known sandbox-bypass floor', async () => {
  const [manifest, lock] = await Promise.all([
    readFile(packagePath, 'utf8').then(JSON.parse),
    readFile(packageLockPath, 'utf8').then(JSON.parse),
  ]);
  const declared = manifest.devDependencies?.electron;
  assert.match(declared, /^\d+\.\d+\.\d+$/, 'Electron must remain exactly pinned');
  const version = declared.split('.').map(Number);
  assert.ok(
    version[0] > 41
      || (version[0] === 41 && (version[1] > 10 || (version[1] === 10 && version[2] >= 3))),
    `Electron ${declared} is below the 41.10.3 sandbox-bypass fix`,
  );
  assert.equal(lock.packages?.['node_modules/electron']?.version, declared);
});

test('platform package resources match each staged desktop backend name', async () => {
  const [manifest, makefile] = await Promise.all([
    readFile(packagePath, 'utf8').then(JSON.parse),
    readFile(makefilePath, 'utf8'),
  ]);
  const build = manifest.build;

  assert.deepEqual(build.extraResources, [
    { from: '../internal/webui/assets', to: 'webui' },
  ]);
  assert.deepEqual(build.mac.extraResources, [
    { from: 'resources/bin/yhc', to: 'bin/yhc' },
  ]);
  assert.deepEqual(build.linux.extraResources, [
    { from: 'resources/bin/yhc', to: 'bin/yhc' },
  ]);
  assert.deepEqual(build.win.extraResources, [
    { from: 'resources/bin/yhc.exe', to: 'bin/yhc.exe' },
  ]);
  assert.match(makefile, /desktop-stage-windows-amd64:[^\n]*yhc\.exe/);
  assert.match(makefile, /\$\(DESKTOP_STAGE_DIR\)\/yhc\.exe/);
});

test('preload exposes bounded provider setup without a secret read capability', async () => {
  const source = await readFile(preloadPath, 'utf8');
  assert.match(source, /getProviderStatus:\s*\(\)\s*=>\s*ipcRenderer\.invoke\('app:provider-status'\)/);
  assert.match(source, /configureProvider:\s*\(input\)\s*=>\s*ipcRenderer\.invoke\('app:configure-provider', input\)/);
  assert.doesNotMatch(source, /readProvider(?:Key|Secret|Credential)/);
  assert.doesNotMatch(source, /decryptString|safeStorage|PROV_API_KEY/);
});

test('only Electron main can turn a native directory choice into a workspace handle', async () => {
  const [main, preload] = await Promise.all([
    readFile(mainPath, 'utf8'),
    readFile(preloadPath, 'utf8'),
  ]);
  assert.match(preload, /selectWorkspace: \(\) => ipcRenderer\.invoke\('app:select-workspace'\)/);
  assert.doesNotMatch(preload, /selectDirectory/);
  assert.match(main, /ipcMain\.handle\('app:select-workspace'/);
  assert.match(main, /operationRequest\('createWorkspace', \{ cwd: result\.filePaths\[0\] \}\)/);
  assert.match(main, /workspace_handle/);
  assert.match(main, /workspace_label/);
  assert.match(main, /operation === 'createWorkspace'/);
});

test('main process owns encrypted launch profile and keeps backend argv secret-free', async () => {
  const source = await readFile(mainPath, 'utf8');
  assert.match(source, /safeStorage/);
  assert.match(source, /provider-profile\.v1\.json/);
  assert.match(source, /ipcMain\.handle\('app:provider-status'/);
  assert.match(source, /ipcMain\.handle\('app:configure-provider'/);
  assert.match(source, /spawn\(resolveBackend\(\), \['serve', 'app', '--web'\], \{/);
  assert.match(source, /env:\s*launchEnvironment/);
  assert.match(source, /const owned = backend === child/);
  assert.match(source, /owned && !stoppingBackends\.has\(child\)/);
  assert.doesNotMatch(source, /\['serve', 'app', '--web'[^\]]*(?:apiKey|PROV_API_KEY)/);
  assert.doesNotMatch(source, /notifyBackendExit\(\{[^}]*diagnostics/s);
  assert.doesNotMatch(source, /detail:\s*error\.message/);
});

test('renderer uses supported CSP while Electron denies external navigation', async () => {
  const [main, renderer] = await Promise.all([
    readFile(mainPath, 'utf8'),
    readFile(rendererPath, 'utf8'),
  ]);
  assert.doesNotMatch(renderer, /navigate-to/);
  assert.match(renderer, /form-action 'none'/);
  assert.match(main, /setWindowOpenHandler\(\(\) => \(\{ action: 'deny' \}\)\)/);
  assert.match(main, /on\('will-navigate', \(event\) => event\.preventDefault\(\)\)/);
});
