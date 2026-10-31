#!/usr/bin/env node
'use strict'

const assert = require('node:assert/strict')
const fs = require('node:fs')
const path = require('node:path')
const { BASELINE, INPUT, SOURCES, execute, sourceHashes } = require('./book-item-oracle.cjs')

const ROOT = path.resolve(__dirname, '../..')
const golden = JSON.parse(fs.readFileSync(path.join(ROOT, 'go-backend/contracts/parity/book-item-golden.json'), 'utf8'))

assert.equal(golden.upstream.baseline, BASELINE)
assert.deepEqual(golden.contract.source, SOURCES)
assert.deepEqual(golden.upstream.sourceHashes, sourceHashes(), 'pinned upstream serializer source changed; review and regenerate deliberately')
assert.deepEqual(golden.input, INPUT, 'source fixture input changed; regenerate and review the golden deliberately')
assert.deepEqual(execute(golden.input), golden.expected, 'actual upstream book/item serializers differ from the committed golden')
assert.deepEqual(execute(golden.input), golden.expected, 'repeated source execution must be reproducible')

for (const [name, outputs] of Object.entries(golden.expected)) {
  assert.ok(outputs.itemJSON.media, `${name}: full ItemJSON book media missing`)
  assert.ok(outputs.itemMinifiedJSON.media, `${name}: ItemMinifiedJSON book media missing`)
  assert.ok(outputs.itemExpandedJSON.media, `${name}: expanded ItemJSON book media missing`)
}

console.log(`book/item oracle: ${Object.keys(golden.expected).length} source fixtures, three serialization shapes verified`)
