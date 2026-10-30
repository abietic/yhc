import assert from 'node:assert/strict';
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import { execFileSync } from 'node:child_process';
import { createRequire } from 'node:module';
import test from 'node:test';

const require = createRequire(import.meta.url);
const { writeChromaModule, chromaFixtureBinary } = require('./chroma_fixture.cjs');
const asar = require('@electron/asar');
const {
  REQUIRED_ARCHIVE_FILES,
  normalizeArchiveEntry,
  verifyPackagedArtifact,
} = require('../scripts/verify_packaged_artifact.cjs');
const {
  REQUIRED_LICENSE_FILES,
  resourcesDirectory,
} = require('../scripts/verify_packaged_notices.cjs');

async function createArchive(source, destination) {
  await asar.createPackage(source, destination);
  const archive = fs.statSync(destination);
  assert.ok(archive.isFile() && archive.size > 0, 'ASAR archive must be a non-empty file');
  asar.uncache(destination);
}

test('archive entries normalize native separators before exact allowlist comparison', () => {
  assert.equal(normalizeArchiveEntry('/bootstrap.cjs'), 'bootstrap.cjs');
  assert.equal(normalizeArchiveEntry('\\bootstrap.cjs'), 'bootstrap.cjs');
  assert.equal(normalizeArchiveEntry('\\nested\\entry.cjs'), 'nested/entry.cjs');
  assert.throws(() => normalizeArchiveEntry(null), /archive entry must be text/);
});

async function createArtifactFixture(platform, arch = 1) {
  const root = fs.mkdtempSync(path.join(os.tmpdir(), 'yhc-packaged-artifact-'));
  const repository = path.join(root, 'repository');
  const projectDir = path.join(repository, 'desktop');
  const appOutDir = path.join(root, 'app-out');
  const context = {
    appOutDir,
    electronPlatformName: platform,
    arch,
    packager: {
      projectDir,
      appInfo: { productFilename: 'YHC', version: '0.1.0' },
    },
  };
  const resources = resourcesDirectory(context);
  const archiveSource = path.join(root, 'archive-source');
  const sourceWebUI = path.join(repository, 'internal', 'webui', 'assets');
  const packagedWebUI = path.join(resources, 'webui');
  const backendName = platform === 'win32' ? 'yhc.exe' : 'yhc';
  const sourceBackend = path.join(projectDir, 'resources', 'bin', backendName);
  const packagedBackend = path.join(resources, 'bin', backendName);

  fs.mkdirSync(archiveSource, { recursive: true });
  fs.mkdirSync(sourceWebUI, { recursive: true });
  fs.mkdirSync(packagedWebUI, { recursive: true });
  fs.mkdirSync(path.dirname(sourceBackend), { recursive: true });
  fs.mkdirSync(path.dirname(packagedBackend), { recursive: true });
  fs.mkdirSync(path.join(resources, 'licenses'), { recursive: true });

  for (const filename of REQUIRED_ARCHIVE_FILES) {
    const content = filename === 'package.json'
      ? JSON.stringify({
        name: 'yhc-desktop',
        version: '0.1.0',
        private: true,
        main: 'main.cjs',
      })
      : `module.exports = ${JSON.stringify(filename)};\n`;
    fs.writeFileSync(path.join(archiveSource, filename), content);
  }
  await createArchive(archiveSource, path.join(resources, 'app.asar'));

  for (const [filename, content] of [
    ['index.html', '<main>YHC</main>\n'],
    ['app.mjs', 'export const app = true;\n'],
    ['vendor/marked.esm.js', 'export const marked = true;\n'],
  ]) {
    const source = path.join(sourceWebUI, filename);
    const packaged = path.join(packagedWebUI, filename);
    fs.mkdirSync(path.dirname(source), { recursive: true });
    fs.mkdirSync(path.dirname(packaged), { recursive: true });
    fs.writeFileSync(source, content);
    fs.writeFileSync(packaged, content);
  }

  writeChromaModule(repository);
  const backendBytes = chromaFixtureBinary(platform, 'v2.27.0', false, arch === 3 ? 'arm64' : 'amd64');
  fs.writeFileSync(sourceBackend, backendBytes, { mode: 0o755 });
  fs.writeFileSync(packagedBackend, backendBytes, { mode: 0o755 });
  for (const filename of REQUIRED_LICENSE_FILES) {
    fs.writeFileSync(path.join(resources, 'licenses', filename), 'retained\n');
  }
  const notices = path.join(repository, 'third_party', 'notices');
  fs.mkdirSync(notices, { recursive: true });
  for (const name of ['chroma.COPYING', 'chroma.PROVENANCE.json']) {
    fs.copyFileSync(new URL(`../../third_party/notices/${name}`, import.meta.url), path.join(notices, name));
    fs.copyFileSync(path.join(notices, name), path.join(resources, 'licenses', name));
  }

  return {
    archiveSource,
    context,
    packagedBackend,
    sourceBackend,
    packagedWebUI,
    resources,
    root,
  };
}

async function withArtifact(platform, check, arch = 1) {
  const fixture = await createArtifactFixture(platform, arch);
  try {
    await check(fixture);
  } finally {
    fs.rmSync(fixture.root, { recursive: true, force: true });
  }
}

for (const arch of [1, 3]) {
  test(`darwin architecture ${arch} rejects a real backend of the other architecture`, async () => {
    await withArtifact('darwin', ({ context, sourceBackend, packagedBackend }) => {
      assert.doesNotThrow(() => verifyPackagedArtifact(context));
      const wrong = chromaFixtureBinary('darwin', 'v2.27.0', false, arch === 3 ? 'amd64' : 'arm64');
      fs.writeFileSync(sourceBackend, wrong);
      fs.writeFileSync(packagedBackend, wrong);
      assert.throws(() => verifyPackagedArtifact(context), /architecture/);
    }, arch);
  });
}

test('missing, unknown and unsupported package architectures fail closed', async () => {
  await withArtifact('darwin', ({ context }) => {
    for (const arch of [undefined, null, 0, 2, 4, 99, 'arm64']) {
      context.arch = arch;
      assert.throws(() => verifyPackagedArtifact(context), /architecture/);
    }
  });
});

test('older launcher uses an existing build toolchain and missing caches cannot download one', async t => {
  if (process.platform === 'win32') return t.skip('requires executable aliases for the older-launcher integration');
  const info = JSON.parse(execFileSync('go', ['env', '-json', 'GOROOT', 'GOVERSION'], {
    cwd: os.tmpdir(), encoding: 'utf8', env: { ...process.env, GOWORK: 'off', GOTOOLCHAIN: 'local' }, timeout: 10000,
  }));
  const currentMinor = Number(info.GOVERSION.match(/^go1\.(\d+)/)?.[1]);
  let older;
  for (const dir of (process.env.PATH || '').split(path.delimiter)) {
    const candidate = path.resolve(dir || '.', 'go');
    try {
      const text = execFileSync(candidate, ['version'], { cwd: os.tmpdir(), encoding: 'utf8',
        env: { ...process.env, GOTOOLCHAIN: 'local', GOWORK: 'off' }, timeout: 5000 });
      const minor = Number(text.match(/go1\.(\d+)/)?.[1]);
      if (minor >= 26 && minor < currentMinor) { older = candidate; break; }
    } catch { /* PATH may contain absent or unrelated installations. */ }
  }
  if (!older) return t.skip('no older Go launcher is installed; explicit real-launcher evidence is environment-specific');
  await withArtifact('darwin', ({ context, root }) => {
    const aliases = path.join(root, 'launchers');
    fs.mkdirSync(aliases);
    fs.symlinkSync(older, path.join(aliases, 'go'));
    fs.symlinkSync(path.join(info.GOROOT, 'bin/go'), path.join(aliases, info.GOVERSION));
    const env = { ...process.env, PATH: aliases, GOTOOLCHAIN: 'auto', GOPROXY: 'off', GOWORK: 'off' };
    const selected = execFileSync('go', ['env', 'GOVERSION'], {
      cwd: path.resolve(context.packager.projectDir, '..'), env, encoding: 'utf8', timeout: 10000,
    }).trim();
    assert.equal(selected, info.GOVERSION, 'normal automatic selection must use the cached build compiler');
    const script = `const v=require(process.argv[1]);try{v.verifyPackagedArtifact(JSON.parse(process.argv[2]));process.stdout.write('accepted')}catch(e){process.stderr.write(String(e));process.exitCode=1}`;
    const args = ['-e', script, require.resolve('../scripts/verify_packaged_artifact.cjs'), JSON.stringify(context)];
    assert.equal(execFileSync(process.execPath, args, { env, encoding: 'utf8', timeout: 30000 }), 'accepted');
    fs.rmSync(path.join(aliases, info.GOVERSION));
    const missing = path.join(root, 'empty-module-cache');
    assert.throws(() => execFileSync(process.execPath, args, { env: { ...env, GOMODCACHE: missing }, encoding: 'utf8',
      stdio: ['ignore', 'pipe', 'pipe'], timeout: 30000 }), /existing cached build Go toolchain/);
    assert.equal(fs.existsSync(missing), false, 'inspection must not initiate a toolchain download');
  });
});

for (const platform of ['darwin', 'linux', 'win32']) {
  test(`verifies the complete ${platform} unpacked application payload`, async () => {
    await withArtifact(platform, ({ context }) => {
      assert.doesNotThrow(() => verifyPackagedArtifact(context));
    });
  });
}

test('rejects unexpected application archive files and metadata drift', async () => {
  await withArtifact('linux', async ({ archiveSource, context, resources }) => {
    const archive = path.join(resources, 'app.asar');
    fs.writeFileSync(path.join(archiveSource, 'debug.cjs'), 'module.exports = true;\n');
    fs.rmSync(archive);
    await createArchive(archiveSource, archive);
    assert.throws(
      () => verifyPackagedArtifact(context),
      /packaged application archive entries do not match/,
    );

    fs.rmSync(path.join(archiveSource, 'debug.cjs'));
    fs.writeFileSync(
      path.join(archiveSource, 'package.json'),
      JSON.stringify({ name: 'yhc-desktop', version: '0.2.0', main: 'main.cjs' }),
    );
    fs.rmSync(archive);
    await createArchive(archiveSource, archive);
    assert.throws(
      () => verifyPackagedArtifact(context),
      /packaged application version does not match/,
    );
  });
});

test('rejects missing or byte-divergent WebUI resources', async () => {
  await withArtifact('linux', ({ context, packagedWebUI }) => {
    fs.writeFileSync(path.join(packagedWebUI, 'app.mjs'), 'changed\n');
    assert.throws(
      () => verifyPackagedArtifact(context),
      /packaged WebUI file differs: app\.mjs/,
    );

    fs.rmSync(path.join(packagedWebUI, 'vendor', 'marked.esm.js'));
    assert.throws(
      () => verifyPackagedArtifact(context),
      /packaged WebUI file list does not match/,
    );
  });
});

test('rejects unsafe or non-executable packaged backends', async () => {
  await withArtifact('linux', ({ context, packagedBackend }) => {
    fs.chmodSync(packagedBackend, 0o644);
    assert.throws(
      () => verifyPackagedArtifact(context),
      /packaged backend is not executable/,
    );

    fs.rmSync(packagedBackend);
    fs.symlinkSync('missing-yhc', packagedBackend);
    assert.throws(
      () => verifyPackagedArtifact(context),
      /packaged backend is invalid/,
    );
  });
});

test('rejects packaged backend bytes that differ from the staged build', async () => {
  await withArtifact('win32', ({ context, packagedBackend }) => {
    fs.writeFileSync(packagedBackend, 'different backend\n');
    assert.throws(
      () => verifyPackagedArtifact(context),
      /packaged backend differs from the staged build/,
    );
  });
});
