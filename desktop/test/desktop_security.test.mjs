import assert from 'node:assert/strict';
import { execFile } from 'node:child_process';
import fs from 'node:fs';
import { readFile } from 'node:fs/promises';
import os from 'node:os';
import path from 'node:path';
import { createRequire } from 'node:module';
import test from 'node:test';
import { promisify } from 'node:util';

const require = createRequire(import.meta.url);
const execFileAsync = promisify(execFile);
const {
  REQUIRED_LICENSE_FILES,
  resourcesDirectory,
  verifyPackagedNotices,
} = require('../scripts/verify_packaged_notices.cjs');
const {
  REQUIRED_ARCHIVE_FILES,
} = require('../scripts/verify_packaged_artifact.cjs');

const mainPath = new URL('../main.cjs', import.meta.url);
const packagePath = new URL('../package.json', import.meta.url);
const packageLockPath = new URL('../package-lock.json', import.meta.url);
const preloadPath = new URL('../preload.cjs', import.meta.url);
const rendererPath = new URL('../../internal/webui/assets/index.html', import.meta.url);
const makefilePath = new URL('../../Makefile', import.meta.url);
const repositoryPath = new URL('../..', import.meta.url);

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

  assert.equal(manifest.devDependencies?.['@electron/asar'], '3.4.1');
  assert.match(manifest.version, /^[0-9][0-9A-Za-z.+-]{0,63}$/);

  assert.deepEqual(build.files, [
    'main.cjs',
    'bootstrap.cjs',
    'lifecycle.cjs',
    'provider_setup.cjs',
    'request.cjs',
    'preload.cjs',
    'package.json',
  ]);
  assert.deepEqual([...build.files].sort(), REQUIRED_ARCHIVE_FILES);

  assert.deepEqual(build.extraResources, [
    { from: '../internal/webui/assets', to: 'webui' },
    { from: '../LICENSE', to: 'licenses/YHC.LICENSE' },
    { from: '../NOTICE', to: 'licenses/YHC.NOTICE' },
    { from: '../internal/webui/assets/vendor/marked.LICENSE.txt', to: 'licenses/marked.LICENSE.txt' },
    { from: '../internal/webui/assets/vendor/marked.NOTICE.txt', to: 'licenses/marked.NOTICE.txt' },
    { from: 'node_modules/electron/dist/LICENSE', to: 'licenses/electron.LICENSE' },
    { from: 'node_modules/electron/dist/LICENSES.chromium.html', to: 'licenses/electron-third-party.LICENSES.html' },
  ]);
  assert.equal(build.afterPack, 'scripts/verify_packaged_artifact.cjs');
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
  for (const target of [
    'build/desktop/darwin-amd64/yhc',
    'build/desktop/darwin-arm64/yhc',
    'build/desktop/linux-amd64/yhc',
    'build/desktop/windows-amd64/yhc\\.exe',
  ]) {
    assert.match(
      makefile,
      new RegExp(`${target}:[^\\n]*desktop/package\\.json`),
    );
  }
});

test('desktop backend builds inject reproducible source identity', async () => {
  const [makefile, manifest] = await Promise.all([
    readFile(makefilePath, 'utf8'),
    readFile(packagePath, 'utf8').then(JSON.parse),
  ]);
  assert.match(makefile, /ifneq \(\$\(wildcard \.git\),\)/);
  assert.match(makefile, /BUILD_COMMIT \?= \$\(shell git rev-parse --verify HEAD/);
  assert.match(makefile, /BUILD_TIME \?= \$\(shell git show -s --format=%cI HEAD/);
  assert.match(makefile, /BUILD_MODIFIED \?= \$\(if \$\(shell git status --porcelain/);
  assert.doesNotMatch(makefile, /BUILD_MODIFIED[^\n]*--untracked-files=no/);
  assert.match(makefile, /else\nBUILD_COMMIT \?= unknown\nBUILD_TIME \?= unknown\nBUILD_MODIFIED \?= false\nendif/);
  for (const [symbol, variable] of [
    ['Commit', 'BUILD_COMMIT'],
    ['BuildTime', 'BUILD_TIME'],
    ['Modified', 'BUILD_MODIFIED'],
  ]) {
    assert.match(
      makefile,
      new RegExp(`-X github\\.com/abietic/yhc/internal/buildinfo\\.${symbol}=\\$\\(${variable}\\)`),
    );
  }

  const { stdout } = await execFileAsync('make', [
    '-B', '-n', 'build/desktop/linux-amd64/yhc', 'VERSION=9.9.9',
  ], { cwd: repositoryPath });
  assert.match(
    stdout,
    new RegExp(`buildinfo\\.Version=${manifest.version.replaceAll('.', '\\.')}(?:\\s|$)`),
  );
  assert.doesNotMatch(stdout, /buildinfo\.Version=9\.9\.9(?:\s|$)/);
});

test('packaged artifact retains project, Marked, Electron, and Chromium license material', () => {
  const root = fs.mkdtempSync(path.join(os.tmpdir(), 'yhc-package-notices-'));
  try {
    const linuxContext = { electronPlatformName: 'linux', appOutDir: root };
    const licenses = path.join(resourcesDirectory(linuxContext), 'licenses');
    fs.mkdirSync(licenses, { recursive: true });
    for (const filename of REQUIRED_LICENSE_FILES) {
      fs.writeFileSync(path.join(licenses, filename), 'retained');
    }
    assert.doesNotThrow(() => verifyPackagedNotices(linuxContext));

    fs.writeFileSync(path.join(licenses, 'electron.LICENSE'), '');
    assert.throws(() => verifyPackagedNotices(linuxContext), /invalid: electron\.LICENSE/);

    const macContext = {
      electronPlatformName: 'darwin',
      appOutDir: root,
      packager: { appInfo: { productFilename: 'YHC' } },
    };
    assert.equal(
      resourcesDirectory(macContext),
      path.join(root, 'YHC.app', 'Contents', 'Resources'),
    );
  } finally {
    fs.rmSync(root, { recursive: true, force: true });
  }
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

test('main process waits for observed backend exit before quitting or restarting', async () => {
  const source = await readFile(mainPath, 'utf8');
  const startup = source.slice(
    source.indexOf('app.whenReady()'),
    source.indexOf("app.on('activate'"),
  );
  assert.match(source, /createBackendStopCoordinator\(\{/);
  assert.match(source, /createQuitRequestScheduler\(\{ requestQuit \}\)/);
  assert.match(source, /unmarkStopping:\s*\(child\)\s*=>\s*stoppingBackends\.delete\(child\)/);
  assert.match(source, /stopBackend:\s*\(\)\s*=>\s*stopBackend\(\)/);
  assert.match(
    source,
    /try \{\s*await stopBackend\(\);\s*\} catch \{[\s\S]*?backendStopFailurePrompt\(\)[\s\S]*?return;[\s\S]*?quitAllowed = true;/,
  );
  assert.match(startup, /startDesktopHost\(\{[\s\S]*?loadRenderer,[\s\S]*?stopBackend,/);
  assert.match(
    startup,
    /\.catch\(async \(\) => \{[\s\S]*?if \(backend\) \{[\s\S]*?backendStopFailurePrompt\(\)[\s\S]*?return;[\s\S]*?quitAllowed = true;/,
  );
  assert.doesNotMatch(source, /if \(!backend \|\| !bootstrap\)/);
  assert.doesNotMatch(source, /STOP_TIMEOUT_MS/);
  assert.match(
    source,
    /app\.on\('before-quit', \(event\) => \{[\s\S]*?event\.preventDefault\(\);[\s\S]*?quitRequestScheduler\.request\(\);/,
  );
});

test('macOS window restoration has one target-owned composition path', async () => {
  const source = await readFile(mainPath, 'utf8');
  assert.match(source, /createWindowRestoreCoordinator/);
  assert.match(
    source,
    /const targetWindow = new BrowserWindow\([\s\S]*?mainWindow = targetWindow;[\s\S]*?targetWindow\.once\('closed', \(\) => \{\s*if \(mainWindow === targetWindow\) mainWindow = null;/,
  );
  assert.match(source, /function loadRenderer\(targetWindow\)/);
  assert.match(
    source,
    /if \(mainWindow === targetWindow && !targetWindow\.isDestroyed\(\)\) targetWindow\.show\(\);/,
  );
  assert.match(
    source,
    /createWindowRestoreCoordinator\(\{\s*currentWindow: \(\) => mainWindow,\s*backendAvailable: \(\) => Boolean\(bootstrap\),\s*createWindow,\s*loadRenderer,/,
  );
  assert.match(
    source,
    /app\.on\('second-instance', \(\) => \{\s*void restoreMainWindow\(\)\.catch\(\(\) => \{\}\);\s*\}\);/,
  );
  assert.match(
    source,
    /app\.on\('activate', \(\) => \{\s*void restoreMainWindow\(\)\.catch\(\(\) => \{\}\);\s*\}\);/,
  );
  assert.doesNotMatch(source, /BrowserWindow\.getAllWindows\(\)/);
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
