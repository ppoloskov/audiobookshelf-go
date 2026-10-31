#!/usr/bin/env node
// Behavioral self-test for the repository's pinned golangci-lint depguard rules.
// Run from any directory; fixture modules are isolated under the OS temp dir.
const assert = require('node:assert/strict');
const { spawnSync } = require('node:child_process');
const fs = require('node:fs');
const os = require('node:os');
const path = require('node:path');

const root = path.resolve(__dirname, '../..');
const config = path.join(root, '.golangci.yml');
const version = '2.15.0';
const candidates = [
  process.env.GOLANGCI_LINT,
  path.join(os.homedir(), '.local/share/mise/installs/golangci-lint', version,
    `golangci-lint-${version}-${process.platform}-${process.arch}`, 'golangci-lint'),
  'golangci-lint',
].filter(Boolean);
let lint;
for (const candidate of candidates) {
  const result = spawnSync(candidate, ['--version'], { encoding: 'utf8' });
  if (result.status === 0 && result.stdout.includes(version)) { lint = candidate; break; }
}
assert.ok(lint, `golangci-lint ${version} is required (set GOLANGCI_LINT to its executable)`);

const tmp = fs.mkdtempSync(path.join(os.tmpdir(), 'abs-depguard-'));
const goCache = path.join(tmp, 'gocache');
let passed = 0;
function fixture(name, sourcePath, source, extra = {}) {
  const dir = path.join(tmp, name);
  fs.mkdirSync(dir, { recursive: true });
  fs.writeFileSync(path.join(dir, 'go.mod'), 'module audiobookshelf-compat\n\ngo 1.24.0\n\nrequire golang.org/x/net v0.43.0\n');
  fs.writeFileSync(path.join(dir, 'go.sum'), 'golang.org/x/net v0.43.0 h1:lat02VYK2j4aLzMzecihNvTlJNQUq316m2Mr9rnM6YE=\ngolang.org/x/net v0.43.0/go.mod h1:vhO1fvI4dGsIjh73sWfUVjj3N7CA9WkKJNQm2svM6Jg=\n');
  const files = { ...extra, [sourcePath]: source };
  for (const [rel, body] of Object.entries(files)) {
    const target = path.join(dir, rel);
    fs.mkdirSync(path.dirname(target), { recursive: true });
    fs.writeFileSync(target, body);
  }
  const result = spawnSync(lint, ['run', `--config=${config}`, '--timeout=2m', './...'], {
    cwd: dir, encoding: 'utf8', env: { ...process.env, GOTOOLCHAIN: 'local', GOCACHE: goCache },
  });
  return { ...result, output: `${result.stdout || ''}${result.stderr || ''}` };
}
function rejects(name, sourcePath, importPath) {
  const extra = importPath.startsWith('audiobookshelf-compat/internal/')
    ? { 'internal/catalog/catalog.go': 'package catalog\ntype Stub struct{}\n' } : {};
  const result = fixture(name, sourcePath, `package fixture\nimport _ "${importPath}"\n`, extra);
  assert.notEqual(result.status, 0, `${name}: forbidden import unexpectedly passed\n${result.output}`);
  assert.match(result.output, /\(depguard\)/, `${name}: missing explicit depguard diagnostic\n${result.output}`);
  assert.match(result.output, new RegExp(importPath.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')), `${name}: diagnostic did not name forbidden import\n${result.output}`);
  passed++;
}
function allows(name, sourcePath, importPath, extra = {}) {
  const result = fixture(name, sourcePath, `package fixture\nimport _ "${importPath}"\n`, extra);
  assert.equal(result.status, 0, `${name}: allowed import failed\n${result.output}`);
  passed++;
}
try {
  // Illegal imports in both direct package files and deeper production paths.
  rejects('wire-direct', 'internal/wire/fixture.go', 'audiobookshelf-compat/internal/catalog');
  rejects('wire-nested', 'internal/wire/nested/fixture.go', 'audiobookshelf-compat/internal/catalog');
  rejects('feature-direct', 'internal/scanner/fixture.go', 'audiobookshelf-compat/internal/catalog');
  rejects('feature-nested', 'internal/scanner/nested/fixture.go', 'audiobookshelf-compat/internal/catalog');
  rejects('leaf-direct', 'internal/httputil/fixture.go', 'audiobookshelf-compat/internal/catalog');
  rejects('leaf-nested', 'internal/httputil/nested/fixture.go', 'audiobookshelf-compat/internal/catalog');
  rejects('production-httptest', 'internal/scanner/fixture.go', 'net/http/httptest');
  // The rules must permit intended dependencies, not merely reject everything.
  allows('wire-standard', 'internal/wire/fixture.go', 'encoding/json');
  allows('wire-nested-standard', 'internal/wire/nested/fixture.go', 'encoding/json');
  allows('leaf-wire', 'internal/httputil/fixture.go', 'audiobookshelf-compat/internal/wire', {
    'internal/wire/wire.go': 'package wire\ntype DTO struct{}\n',
  });
  allows('leaf-nested-wire', 'internal/httputil/nested/fixture.go', 'audiobookshelf-compat/internal/wire', {
    'internal/wire/wire.go': 'package wire\ntype DTO struct{}\n',
  });
  allows('feature-leaf', 'internal/scanner/fixture.go', 'audiobookshelf-compat/internal/httputil', {
    'internal/httputil/leaf.go': 'package httputil\ntype Helper struct{}\n',
  });
  allows('feature-nested-leaf', 'internal/scanner/nested/fixture.go', 'audiobookshelf-compat/internal/httputil', {
    'internal/httputil/leaf.go': 'package httputil\ntype Helper struct{}\n',
  });
  allows('auth-html-exception', 'internal/auth/fixture.go', 'golang.org/x/net/html');
  allows('test-httptest', 'internal/scanner/fixture_test.go', 'net/http/httptest');
} finally {
  fs.rmSync(tmp, { recursive: true, force: true });
}
console.log(`dependency boundary fixtures: ${passed} passed (golangci-lint ${version})`);
