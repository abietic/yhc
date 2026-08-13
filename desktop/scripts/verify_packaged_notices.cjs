const fs = require('node:fs');
const path = require('node:path');

const REQUIRED_LICENSE_FILES = Object.freeze([
  'YHC.LICENSE',
  'YHC.NOTICE',
  'marked.LICENSE.txt',
  'marked.NOTICE.txt',
  'electron.LICENSE',
  'electron-third-party.LICENSES.html',
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
}

module.exports = async (context) => verifyPackagedNotices(context);
module.exports.REQUIRED_LICENSE_FILES = REQUIRED_LICENSE_FILES;
module.exports.resourcesDirectory = resourcesDirectory;
module.exports.verifyPackagedNotices = verifyPackagedNotices;
