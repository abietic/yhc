const fs = require('node:fs');
const path = require('node:path');

const asar = require('@electron/asar');
const {
  resourcesDirectory,
  verifyPackagedNotices,
} = require('./verify_packaged_notices.cjs');

const REQUIRED_ARCHIVE_FILES = Object.freeze([
  'lifecycle.cjs',
  'main.cjs',
  'package.json',
  'preload.cjs',
  'provider_setup.cjs',
  'request.cjs',
]);

function requireDirectory(candidate, label) {
  let info;
  try {
    info = fs.lstatSync(candidate);
  } catch {
    throw new Error(`${label} is invalid`);
  }
  if (!info.isDirectory() || info.isSymbolicLink()) {
    throw new Error(`${label} is invalid`);
  }
}

function requireRegularFile(candidate, label) {
  let info;
  try {
    info = fs.lstatSync(candidate);
  } catch {
    throw new Error(`${label} is invalid`);
  }
  if (!info.isFile() || info.isSymbolicLink() || info.size === 0) {
    throw new Error(`${label} is invalid`);
  }
  return info;
}

function regularFileList(root, label) {
  requireDirectory(root, label);
  const files = [];
  const visit = (directory, prefix = '') => {
    for (const entry of fs.readdirSync(directory, { withFileTypes: true })) {
      const relative = prefix ? `${prefix}/${entry.name}` : entry.name;
      const candidate = path.join(directory, entry.name);
      const info = fs.lstatSync(candidate);
      if (info.isSymbolicLink()) {
        throw new Error(`${label} contains an unsafe entry: ${relative}`);
      }
      if (info.isDirectory()) {
        visit(candidate, relative);
        continue;
      }
      if (!info.isFile()) {
        throw new Error(`${label} contains an unsafe entry: ${relative}`);
      }
      files.push(relative);
    }
  };
  visit(root);
  return files.sort();
}

function sameList(left, right) {
  return left.length === right.length &&
    left.every((value, index) => value === right[index]);
}

function verifyArchive(resources, context) {
  const archive = path.join(resources, 'app.asar');
  requireRegularFile(archive, 'packaged application archive');
  const entries = asar.listPackage(archive)
    .map((entry) => entry.replace(/^\/+/, ''))
    .sort();
  if (!sameList(entries, REQUIRED_ARCHIVE_FILES)) {
    throw new Error('packaged application archive entries do not match');
  }
  for (const filename of REQUIRED_ARCHIVE_FILES) {
    const info = asar.statFile(archive, filename);
    if (!Number.isSafeInteger(info?.size) || info.size <= 0 || info.link || info.files) {
      throw new Error(`packaged application archive file is invalid: ${filename}`);
    }
  }

  let manifest;
  try {
    manifest = JSON.parse(asar.extractFile(archive, 'package.json').toString('utf8'));
  } catch {
    throw new Error('packaged application metadata is invalid');
  }
  if (manifest.name !== 'yhc-desktop') {
    throw new Error('packaged application name does not match');
  }
  if (manifest.main !== 'main.cjs') {
    throw new Error('packaged application entrypoint does not match');
  }
  if (manifest.version !== context.packager?.appInfo?.version) {
    throw new Error('packaged application version does not match');
  }
}

function verifyWebUI(resources, projectDir) {
  const source = path.resolve(projectDir, '..', 'internal', 'webui', 'assets');
  const packaged = path.join(resources, 'webui');
  const sourceFiles = regularFileList(source, 'source WebUI');
  const packagedFiles = regularFileList(packaged, 'packaged WebUI');
  if (!sameList(sourceFiles, packagedFiles)) {
    throw new Error('packaged WebUI file list does not match');
  }
  for (const filename of sourceFiles) {
    const sourceBytes = fs.readFileSync(path.join(source, filename));
    const packagedBytes = fs.readFileSync(path.join(packaged, filename));
    if (!sourceBytes.equals(packagedBytes)) {
      throw new Error(`packaged WebUI file differs: ${filename}`);
    }
  }
}

function backendName(platform) {
  if (platform === 'darwin' || platform === 'linux') return 'yhc';
  if (platform === 'win32') return 'yhc.exe';
  throw new Error(`unsupported packaged application platform: ${platform}`);
}

function verifyBackend(resources, projectDir, platform) {
  const filename = backendName(platform);
  const source = path.join(projectDir, 'resources', 'bin', filename);
  const packaged = path.join(resources, 'bin', filename);
  requireRegularFile(source, 'staged backend');
  const packagedInfo = requireRegularFile(packaged, 'packaged backend');
  if (platform !== 'win32' && (packagedInfo.mode & 0o111) === 0) {
    throw new Error('packaged backend is not executable');
  }
  if (!fs.readFileSync(source).equals(fs.readFileSync(packaged))) {
    throw new Error('packaged backend differs from the staged build');
  }
}

function verifyPackagedArtifact(context) {
  const projectDir = context.packager?.projectDir;
  if (typeof projectDir !== 'string' || projectDir.length === 0) {
    throw new Error('packaged application project directory is invalid');
  }
  const resources = resourcesDirectory(context);
  requireDirectory(resources, 'packaged application resources');
  verifyArchive(resources, context);
  verifyWebUI(resources, projectDir);
  verifyBackend(resources, projectDir, context.electronPlatformName);
  verifyPackagedNotices(context);
}

module.exports = async (context) => verifyPackagedArtifact(context);
module.exports.REQUIRED_ARCHIVE_FILES = REQUIRED_ARCHIVE_FILES;
module.exports.backendName = backendName;
module.exports.regularFileList = regularFileList;
module.exports.verifyPackagedArtifact = verifyPackagedArtifact;
