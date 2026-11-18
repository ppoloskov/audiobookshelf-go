'use strict'

const assert = require('node:assert/strict')
const { once } = require('node:events')
const http = require('node:http')
const path = require('node:path')
const test = require('node:test')
const { configureComparison, compare } = require('./lib/compare.cjs')
const { call } = require('./differential.cjs')
const { coverPatchRepeatStateMatches } = require('./cases/steps/full-core-workflows.cjs')

const nodeRoots = { media: '/tmp/r08/node/media', metadata: '/tmp/r08/node/metadata' }
const goRoots = { media: '/tmp/r08/go/media', metadata: '/tmp/r08/go/metadata' }
configureComparison({
  expectedStatus: { 'cover-patch-success': 200, 'cover-patch-repeat': 200 },
  fixturePathRoots: { node: nodeRoots, go: goRoots },
  manifestInos: []
})

function response(root, overrides = {}) {
  const body = { success: true, cover: path.join(root.media, 'book', 'cover.png') }
  const wire = Buffer.from(`${JSON.stringify(body)}\n`)
  return {
    status: 200,
    body,
    wireLength: wire.length,
    ...overrides,
    headers: { 'content-type': 'application/json; charset=utf-8', 'content-length': String(wire.length), etag: 'W/"opaque-tag"', ...overrides.headers }
  }
}

function comparePatch(left = response(nodeRoots), right = response(goRoots), id = 'cover-patch-success') {
  return compare(id, left, right)
}

test('default PATCH comparison accepts equivalent parsed JSON with different wire framing and opaque ETags', () => {
  const left = response(nodeRoots, {
    headers: { 'content-type': 'Application/JSON ; charset = UTF-8', etag: 'W/"node-validator"' }
  })
  const right = response(goRoots, {
    headers: { 'content-type': 'application/json;charset="utf-8"', etag: '"go-validator"' }
  })
  left.wireLength = Buffer.byteLength(`{ "success" : true, "cover" : ${JSON.stringify(left.body.cover)} }\n`)
  left.headers['content-length'] = String(left.wireLength)
  assert.notEqual(left.wireLength, right.wireLength)
  assert.equal(comparePatch(left, right).passed, true)
})

test('PATCH does not require Content-Length or ETag when either is absent', () => {
  const left = response(nodeRoots)
  const right = response(goRoots)
  delete left.headers['content-length']
  delete left.headers.etag
  delete right.headers.etag
  assert.equal(comparePatch(left, right).passed, true)
})

test('PATCH captures actual response wire byte length', async (t) => {
  const wire = Buffer.from('{ "success" : true, "cover" : "/tmp/r08/node/media/book/cover.png" }\n')
  const server = http.createServer((_request, response) => {
    response.writeHead(200, {
      'content-type': 'Application/JSON ; charset = UTF-8',
      'content-length': String(wire.length),
      etag: '"opaque"'
    })
    response.end(wire)
  })
  server.listen(0, '127.0.0.1')
  await once(server, 'listening')
  t.after(() => new Promise(resolve => server.close(resolve)))
  const result = await call({ url: `http://127.0.0.1:${server.address().port}` }, '/api/items/book/cover', {
    method: 'PATCH', body: { cover: '/tmp/cover.png' }
  })
  assert.equal(result.wireLength, wire.length)
  assert.equal(result.headers['content-length'], String(wire.length))
  assert.deepEqual(result.body, JSON.parse(wire.toString('utf8')))
})

test('PATCH comparison rejects incorrect success, status, MIME, length, ETag, and fixture path', () => {
  const invalidCases = [
    ['wrong success value', response(nodeRoots, { body: { success: false, cover: path.join(nodeRoots.media, 'cover.png') } })],
    ['wrong success type', response(nodeRoots, { body: { success: 'true', cover: path.join(nodeRoots.media, 'cover.png') } })],
    ['missing cover', response(nodeRoots, { body: { success: true } })],
    ['non-JSON MIME', response(nodeRoots, { headers: { 'content-type': 'text/plain' } })],
    ['empty JSON charset', response(nodeRoots, { headers: { 'content-type': 'application/json; charset=""' } })],
    ['wrong actual wire length', response(nodeRoots, { headers: { 'content-length': '1' } })],
    ['invalid opaque ETag', response(nodeRoots, { headers: { etag: 'not-an-entity-tag' } })],
    ['outside fixture path sharing the fixture root prefix', response(nodeRoots, { body: { success: true, cover: '/tmp/r08/node/media-sibling/cover.png' } })],
    ['wrong status', response(nodeRoots, { status: 500 })]
  ]
  for (const [name, invalidLeft] of invalidCases) {
    const result = comparePatch(invalidLeft, response(goRoots))
    assert.equal(result.passed, false, name)
    assert.ok(result.differences.length > 0, name)
  }
  const sharedWrongStatus = comparePatch(response(nodeRoots, { status: 500 }), response(goRoots, { status: 500 }))
  assert.equal(sharedWrongStatus.passed, false, 'matching status still must equal expected PATCH status')
})

test('PATCH comparison rejects a missing required field even when both sides omit it', () => {
  const left = response(nodeRoots, { body: { success: true } })
  const right = response(goRoots, { body: { success: true } })
  assert.equal(comparePatch(left, right).passed, false)
})

test('repeat patch state requires stable response path and matching persisted path', () => {
  // Cover validation copies a library-file image into the fixture metadata tree.
  const requested = path.join(nodeRoots.metadata, 'items', 'book', 'cover.png')
  const first = { success: true, cover: requested }
  const repeat = { success: true, cover: requested }
  assert.equal(coverPatchRepeatStateMatches(first, repeat, requested, requested), true)
  assert.equal(coverPatchRepeatStateMatches(first, { success: true, cover: `${requested}.other` }, requested, requested), false)
  assert.equal(coverPatchRepeatStateMatches(first, repeat, `${requested}.other`, requested), false)
  assert.equal(coverPatchRepeatStateMatches(first, repeat, null, requested), false)
  assert.equal(coverPatchRepeatStateMatches(first, repeat, undefined, requested), false)
  assert.equal(coverPatchRepeatStateMatches(first, repeat, requested, null), false)
  assert.equal(coverPatchRepeatStateMatches(first, { success: false, cover: requested }, requested, requested), false)
})
