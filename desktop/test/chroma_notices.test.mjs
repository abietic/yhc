import assert from 'node:assert/strict';
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import { createRequire } from 'node:module';
import test from 'node:test';
import { fileURLToPath } from 'node:url';

const require = createRequire(import.meta.url);
const { writeChromaModule, chromaFixtureBinary } = require('./chroma_fixture.cjs');
const { REQUIRED_LICENSE_FILES, resourcesDirectory, verifyPackagedNotices } = require('../scripts/verify_packaged_notices.cjs');
const repository = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '../..');
const noticeNames = ['chroma.COPYING', 'chroma.PROVENANCE.json'];

test('packaging requires Chroma source license and provenance', () => {
  const manifest = JSON.parse(fs.readFileSync(path.join(repository, 'desktop/package.json')));
  for (const name of noticeNames) {
    assert.ok(REQUIRED_LICENSE_FILES.includes(name));
    assert.ok(manifest.build.extraResources.some(item => item.from === `../third_party/notices/${name}` && item.to === `licenses/${name}`));
  }
});

for (const platform of ['linux', 'darwin', 'win32']) {
  test(`${platform} package rejects changed font license or reviewed provenance`, () => {
    const dir = fs.mkdtempSync(path.join(os.tmpdir(), 'yhc-chroma-notices-'));
    try {
      const fixture = path.join(dir, 'repository');
      writeChromaModule(fixture);
      const context = { electronPlatformName: platform, arch: 1, appOutDir: dir,
        packager: { projectDir: path.join(fixture, 'desktop'), appInfo: { productFilename: 'YHC' } } };
      const resources = resourcesDirectory(context);
      const licenses = path.join(resources, 'licenses');
      fs.mkdirSync(licenses, { recursive: true });
      fs.mkdirSync(path.join(resources, 'bin'), { recursive: true });
      fs.writeFileSync(path.join(resources, 'bin', platform === 'win32' ? 'yhc.exe' : 'yhc'), chromaFixtureBinary(platform));
      for (const name of REQUIRED_LICENSE_FILES) fs.writeFileSync(path.join(licenses, name), 'retained');
      fs.mkdirSync(path.join(fixture, 'third_party/notices'), { recursive: true });
      for (const name of noticeNames) {
        fs.copyFileSync(path.join(repository, 'third_party/notices', name), path.join(licenses, name));
        fs.copyFileSync(path.join(repository, 'third_party/notices', name), path.join(fixture, 'third_party/notices', name));
      }
      assert.doesNotThrow(() => verifyPackagedNotices(context));
      writeChromaModule(fixture, 'v2.28.0');
      assert.throws(() => verifyPackagedNotices(context), /Chroma/);
      writeChromaModule(fixture, 'v2.27.0', true);
      assert.throws(() => verifyPackagedNotices(context), /Chroma/);
      writeChromaModule(fixture);
      const backend = path.join(resources, 'bin', platform === 'win32' ? 'yhc.exe' : 'yhc');
      for (const bytes of [Buffer.from('not a Go executable'), chromaFixtureBinary(platform, 'v2.20.0'), chromaFixtureBinary(platform, 'v2.27.0', true)]) {
        fs.writeFileSync(backend, bytes);
        assert.throws(() => verifyPackagedNotices(context), /Chroma/);
      }
      fs.writeFileSync(backend, chromaFixtureBinary(platform));
      const copying = path.join(licenses, noticeNames[0]);
      const valid = fs.readFileSync(copying);
      fs.writeFileSync(copying, 'MIT only; font notices removed');
      assert.throws(() => verifyPackagedNotices(context), /Chroma|chroma/);
      fs.writeFileSync(copying, valid);
      const provenance = path.join(licenses, noticeNames[1]);
      const record = JSON.parse(fs.readFileSync(provenance));
      record.reviewed_licenses = ['MIT'];
      fs.writeFileSync(provenance, JSON.stringify(record));
      assert.throws(() => verifyPackagedNotices(context), /Chroma|chroma/);
    } finally { fs.rmSync(dir, { recursive: true, force: true }); }
  });
}
