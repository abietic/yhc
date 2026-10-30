const fs = require('node:fs');
const os = require('node:os');
const path = require('node:path');
const { execFileSync } = require('node:child_process');

const modulePath = 'github.com/alecthomas/chroma/v2';
const cache = new Map();

function writeChromaModule(root, version = 'v2.27.0', replacement = false) {
  fs.mkdirSync(root, { recursive: true });
  fs.writeFileSync(path.join(root, 'go.mod'), `module example.com/chromanoticefixture\n\ngo 1.27.0\nrequire ${modulePath} ${version}\n${replacement ? `replace ${modulePath} => ./replacement\n` : ''}`);
  fs.copyFileSync(path.resolve(__dirname, '../../go.sum'), path.join(root, 'go.sum'));
  if (replacement) {
    fs.mkdirSync(path.join(root, 'replacement'), { recursive: true });
    fs.writeFileSync(path.join(root, 'replacement/go.mod'), `module ${modulePath}\n\ngo 1.27.0\n`);
    fs.writeFileSync(path.join(root, 'replacement/chroma.go'), 'package chroma\n');
  }
}

function chromaFixtureBinary(platform, version = 'v2.27.0', replacement = false, goarch = 'amd64') {
  const key = JSON.stringify([platform, version, replacement, goarch]);
  if (cache.has(key)) return cache.get(key);
  const root = fs.mkdtempSync(path.join(os.tmpdir(), 'yhc-chroma-go-fixture-'));
  try {
    writeChromaModule(root, version, replacement);
    fs.writeFileSync(path.join(root, 'main.go'), `package main\nimport _ "${modulePath}"\nfunc main() {}\n`);
    const binary = path.join(root, 'fixture');
    execFileSync('go', ['build', '-mod=mod', '-o', binary, '.'], { cwd: root,
      env: { ...process.env, GOWORK: 'off', GOTOOLCHAIN: 'local', CGO_ENABLED: '0',
        GOOS: platform === 'win32' ? 'windows' : platform, GOARCH: goarch },
      stdio: ['ignore', 'pipe', 'pipe'], timeout: 60000 });
    const bytes = fs.readFileSync(binary);
    cache.set(key, bytes);
    return bytes;
  } finally { fs.rmSync(root, { recursive: true, force: true }); }
}

module.exports = { writeChromaModule, chromaFixtureBinary };
