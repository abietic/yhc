const fs = require('node:fs');
const path = require('node:path');
const os = require('node:os');
const crypto = require('node:crypto');
const { execFileSync } = require('node:child_process');

function goOutput(executable, args, cwd, deadline) {
  try {
    const remaining = deadline - Date.now();
    if (remaining <= 0) throw new Error('deadline exceeded');
    return execFileSync(executable, args, { cwd, encoding: 'utf8', timeout: remaining,
      env: { ...process.env, GOWORK: 'off', GOPROXY: 'off', GOTOOLCHAIN: 'local' },
      stdio: ['ignore', 'pipe', 'pipe'], maxBuffer: 4 * 1024 * 1024 });
  } catch { throw new Error('Chroma module or binary build information cannot be verified'); }
}

function goJSON(executable, args, cwd, deadline) {
  try { return JSON.parse(goOutput(executable, args, cwd, deadline)); }
  catch { throw new Error('Chroma module or binary build information cannot be verified'); }
}

// Bootstrap metadata with the launcher, then use the exact compiler recorded by
// the backend. Only existing PATH installations and Go's toolchain module cache
// are considered: selection never invokes automatic toolchain downloading.
function cachedBuildGo(binary, deadline) {
  const outsideModule = os.tmpdir();
  const firstLine = goOutput('go', ['version', '-m', binary], outsideModule, deadline).split('\n')[0];
  const version = firstLine.match(/: (go\d+\.\d+(?:\.\d+)?(?:(?:beta|rc)\d+)?)$/)?.[1];
  if (!version) throw new Error('Chroma backend build Go version is unsupported');
  const launcher = goJSON('go', ['env', '-json', 'GOROOT', 'GOVERSION', 'GOMODCACHE', 'GOHOSTOS', 'GOHOSTARCH'], outsideModule, deadline);
  const filename = process.platform === 'win32' ? 'go.exe' : 'go';
  const candidates = [];
  if (launcher.GOVERSION === version && path.isAbsolute(launcher.GOROOT || '')) {
    candidates.push(path.join(launcher.GOROOT, 'bin', filename));
  }
  for (const directory of (process.env.PATH || '').split(path.delimiter)) {
    if (directory) candidates.push(path.join(directory, process.platform === 'win32' ? `${version}.exe` : version));
  }
  if (path.isAbsolute(launcher.GOMODCACHE || '') && /^[a-z0-9]+$/.test(launcher.GOHOSTOS || '') && /^[a-z0-9]+$/.test(launcher.GOHOSTARCH || '')) {
    candidates.push(path.join(launcher.GOMODCACHE,
      `golang.org/toolchain@v0.0.1-${version}.${launcher.GOHOSTOS}-${launcher.GOHOSTARCH}`, 'bin', filename));
  }
  for (const candidate of new Set(candidates)) {
    try {
      if (!fs.statSync(candidate).isFile()) continue;
      if (goOutput(candidate, ['version'], outsideModule, deadline).startsWith(`go version ${version} `)) return candidate;
    } catch { /* An unavailable installation cannot satisfy the build version. */ }
  }
  throw new Error('Chroma backend requires an existing cached build Go toolchain');
}

function backendArchitecture(platform, arch) {
  // electron-builder Arch: ia32=0, x64=1, armv7l=2, arm64=3, universal=4.
  const goarch = new Map([[0, '386'], [1, 'amd64'], [2, 'arm'], [3, 'arm64']]).get(arch);
  const goos = platform === 'win32' ? 'windows' : platform;
  if (!goarch || !new Set(['darwin-amd64', 'darwin-arm64', 'linux-amd64', 'windows-amd64']).has(`${goos}-${goarch}`)) {
    throw new Error('Chroma backend package architecture is missing or unsupported');
  }
  return { goos, goarch };
}

function verifyChromaVersion(projectDir, resources, platform, arch, proof) {
  const expected = backendArchitecture(platform, arch);
  const deadline = Date.now() + 30000;
  const root = path.resolve(projectDir, '..');
  const mod = fs.lstatSync(path.join(root, 'go.mod'));
  if (!mod.isFile() || mod.isSymbolicLink()) throw new Error('Chroma source go.mod is unsafe');
  const binary = path.join(resources, 'bin', platform === 'win32' ? 'yhc.exe' : 'yhc');
  const stat = fs.lstatSync(binary);
  if (!stat.isFile() || stat.isSymbolicLink()) throw new Error('Chroma backend is unsafe');
  const go = cachedBuildGo(binary, deadline);
  const module = goJSON(go, ['list', '-mod=readonly', '-m', '-json', proof.module], root, deadline);
  if (module.Path !== proof.module || module.Version !== proof.version || module.Replace) {
    throw new Error('Chroma source version or replacement differs from reviewed provenance');
  }
  const build = goJSON(go, ['version', '-m', '-json', binary], root, deadline);
  const modules = build.Deps?.filter(dep => dep.Path === proof.module) || [];
  const goos = build.Settings?.find(setting => setting.Key === 'GOOS')?.Value;
  const goarch = build.Settings?.find(setting => setting.Key === 'GOARCH')?.Value;
  if (modules.length !== 1 || modules[0].Version !== proof.version || modules[0].Replace ||
      goos !== expected.goos || goarch !== expected.goarch) {
    throw new Error('Chroma backend dependency, replacement or architecture differs from reviewed provenance');
  }
}

const REQUIRED_LICENSE_FILES = Object.freeze([
  'YHC.LICENSE',
  'YHC.NOTICE',
  'marked.LICENSE.txt',
  'marked.NOTICE.txt',
  'electron.LICENSE',
  'electron-third-party.LICENSES.html',
  'chroma.COPYING',
  'chroma.PROVENANCE.json',
]);

function resourcesDirectory(context) {
  if (context.electronPlatformName === 'darwin') {
    return path.join(
      context.appOutDir,
      `${context.packager.appInfo.productFilename}.app`,
      'Contents',
      'Resources',
    );
  }
  return path.join(context.appOutDir, 'resources');
}

function verifyPackagedNotices(context) {
  const licenses = path.join(resourcesDirectory(context), 'licenses');
  for (const filename of REQUIRED_LICENSE_FILES) {
    const candidate = path.join(licenses, filename);
    let info;
    try {
      info = fs.lstatSync(candidate);
    } catch {
      throw new Error(`packaged license material is missing: ${filename}`);
    }
    if (!info.isFile() || info.isSymbolicLink() || info.size === 0) {
      throw new Error(`packaged license material is invalid: ${filename}`);
    }
  }
  const projectDir = context.packager?.projectDir;
  if (typeof projectDir !== 'string' || projectDir.length === 0) throw new Error('Chroma source project directory is missing');
  const source = path.resolve(projectDir, '..', 'third_party', 'notices');
  for (const name of ['chroma.COPYING', 'chroma.PROVENANCE.json']) {
    const sourcePath = path.join(source, name);
    const info = fs.lstatSync(sourcePath);
    if (!info.isFile() || info.isSymbolicLink() || !fs.readFileSync(sourcePath).equals(fs.readFileSync(path.join(licenses, name)))) {
      throw new Error(`packaged Chroma license differs from reviewed source: ${name}`);
    }
  }
  const proof = JSON.parse(fs.readFileSync(path.join(licenses, 'chroma.PROVENANCE.json'), 'utf8'));
  const digest = crypto.createHash('sha256').update(fs.readFileSync(path.join(licenses, 'chroma.COPYING'))).digest('hex');
  if (proof.schema_version !== 1 || proof.module !== 'github.com/alecthomas/chroma/v2' || proof.version !== 'v2.27.0' ||
      proof.source_sha256 !== digest || digest !== '13654b052f885cb3fa47d7343e13975d9c8a64c3bcd84993c35d5eb01f22b23f' ||
      JSON.stringify(proof.reviewed_licenses) !== JSON.stringify(['MIT', 'OFL-1.1-RFN']) ||
      JSON.stringify(proof.detector?.observed_licenses) !== JSON.stringify(['OFL-1.1-no-RFN']) ||
      JSON.stringify(proof.reserved_font_names) !== JSON.stringify(['Arimo', 'Tinos', 'Cousine', 'Liberation'])) {
    throw new Error('Chroma license digest or reviewed scopes are invalid');
  }
  verifyChromaVersion(projectDir, resourcesDirectory(context), context.electronPlatformName, context.arch, proof);
}

module.exports = async (context) => verifyPackagedNotices(context);
module.exports.REQUIRED_LICENSE_FILES = REQUIRED_LICENSE_FILES;
module.exports.resourcesDirectory = resourcesDirectory;
module.exports.verifyPackagedNotices = verifyPackagedNotices;
