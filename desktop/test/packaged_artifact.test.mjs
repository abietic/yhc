import assert from 'node:assert/strict';
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import { once } from 'node:events';
import { createRequire } from 'node:module';
import test from 'node:test';

const require = createRequire(import.meta.url);
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
  const output = await asar.createPackage(source, destination);
  if (!output.writableFinished) await once(output, 'finish');
  asar.uncache(destination);
}

test('archive entries normalize native separators before exact allowlist comparison', () => {
  assert.equal(normalizeArchiveEntry('/bootstrap.cjs'), 'bootstrap.cjs');
  assert.equal(normalizeArchiveEntry('\\bootstrap.cjs'), 'bootstrap.cjs');
  assert.equal(normalizeArchiveEntry('\\nested\\entry.cjs'), 'nested/entry.cjs');
  assert.throws(() => normalizeArchiveEntry(null), /archive entry must be text/);
});

async function createArtifactFixture(platform) {
  const root = fs.mkdtempSync(path.join(os.tmpdir(), 'yhc-packaged-artifact-'));
  const repository = path.join(root, 'repository');
  const projectDir = path.join(repository, 'desktop');
  const appOutDir = path.join(root, 'app-out');
  const context = {
    appOutDir,
    electronPlatformName: platform,
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

  const backendBytes = Buffer.from(`backend-${platform}\n`);
  fs.writeFileSync(sourceBackend, backendBytes, { mode: 0o755 });
  fs.writeFileSync(packagedBackend, backendBytes, { mode: 0o755 });
  for (const filename of REQUIRED_LICENSE_FILES) {
    fs.writeFileSync(path.join(resources, 'licenses', filename), 'retained\n');
  }

  return {
    archiveSource,
    context,
    packagedBackend,
    packagedWebUI,
    resources,
    root,
  };
}

async function withArtifact(platform, check) {
  const fixture = await createArtifactFixture(platform);
  try {
    await check(fixture);
  } finally {
    fs.rmSync(fixture.root, { recursive: true, force: true });
  }
}

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
