'use strict'

const assert = require('node:assert/strict')
const { spawnSync } = require('node:child_process')
const crypto = require('node:crypto')
const fs = require('node:fs')
const os = require('node:os')
const path = require('node:path')
const test = require('node:test')
const { configureComparison, compare } = require('./lib/compare.cjs')
const { writeRepresentationDiagnostics } = require('./differential.cjs')
const { validateExpressJSONRepresentation } = require('./representation-diagnostic.cjs')

const nodeRoots = { media: '/tmp/r08-diagnostic/node/media', metadata: '/tmp/r08-diagnostic/node/metadata' }
const goRoots = { media: '/tmp/r08-diagnostic/go/media', metadata: '/tmp/r08-diagnostic/go/metadata' }
configureComparison({
  expectedStatus: { 'cover-patch-success': 200 },
  fixturePathRoots: { node: nodeRoots, go: goRoots },
  manifestInos: []
})

function expressResponse(root) {
  const body = { success: true, cover: path.join(root.media, 'book', 'cover.png') }
  const bytes = Buffer.from(JSON.stringify(body))
  const etag = `W/"${bytes.length.toString(16)}-${crypto.createHash('sha1').update(bytes).digest('base64').replace(/=+$/, '')}"`
  return {
    status: 200,
    body,
    wireLength: bytes.length,
    headers: { 'content-type': 'application/json; charset=utf-8', 'content-length': String(bytes.length), etag }
  }
}

test('opt-in diagnostic retains the old exact Express JSON fingerprint check', () => {
  assert.equal(validateExpressJSONRepresentation(expressResponse(nodeRoots)), true)
})

test('diagnostic runner accepts sidecar inputs captured by compare only when explicitly written', (t) => {
  const result = compare('cover-patch-success', expressResponse(nodeRoots), expressResponse(goRoots))
  assert.equal(result.passed, true)
  assert.equal(Object.keys(result).includes('representationInputs'), false)

  const directory = fs.mkdtempSync(path.join(os.tmpdir(), 'abs-cover-patch-diagnostic-'))
  t.after(() => fs.rmSync(directory, { recursive: true, force: true }))
  const sidecar = path.join(directory, 'inputs.json')
  writeRepresentationDiagnostics(sidecar, [result])
  const runner = path.join(__dirname, 'representation-diagnostic.cjs')
  const diagnostic = spawnSync(process.execPath, [runner, sidecar], { encoding: 'utf8' })
  assert.equal(diagnostic.status, 0, diagnostic.stderr)
  assert.deepEqual(JSON.parse(diagnostic.stdout)['cover-patch-success'], {
    referenceExpressRepresentation: true,
    goExpressRepresentation: true
  })
  fs.writeFileSync(sidecar, JSON.stringify({ cases: {} }))
  const empty = spawnSync(process.execPath, [runner, sidecar], { encoding: 'utf8' })
  assert.equal(empty.status, 1)
  assert.match(empty.stderr, /no representation diagnostic cases supplied/)
  const focused = spawnSync(process.execPath, [path.join(__dirname, 'differential.cjs'),
    '--node-url', 'http://127.0.0.1:1', '--go-url', 'http://127.0.0.1:1',
    '--fixture', 'unused.json', '--cases', 'login', '--representation-diagnostics-out', sidecar], { encoding: 'utf8' })
  assert.equal(focused.status, 1)
  assert.match(focused.stderr, /sidecars require a full run/)
})
