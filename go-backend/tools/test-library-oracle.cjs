#!/usr/bin/env node
'use strict'

const assert = require('node:assert/strict')
const fs = require('node:fs')
const path = require('node:path')
const { BASELINE, ROOT, SOURCES, execute, hashFile } = require('./library-oracle.cjs')

async function main() {
  const fixturePath = path.join(ROOT, 'go-backend/contracts/library-golden.json')
  const fixture = JSON.parse(fs.readFileSync(fixturePath, 'utf8'))
  assert.equal(fixture.upstream.baseline, BASELINE)
  assert.deepEqual(fixture.upstream.sourceHashes, Object.fromEntries(SOURCES.map((source) => [source, hashFile(source)])),
    'upstream contract source changed; review and regenerate the golden deliberately')

  const actual = await execute(fixture.input)
  assert.deepEqual(actual, fixture.expected, 'actual upstream serializers/controller output differs from golden')
  assert.deepEqual(await execute(fixture.input), actual, 'repeated execution must produce reproducible output')

  const altered = structuredClone(fixture.input)
  altered.libraries[0].name = 'Changed by oracle test'
  altered.libraries[0].libraryFolders[0].path = '/changed/path'
  altered.libraries[0].settings.skipMatchingMediaWithIsbn = true
  const alteredOutput = await execute(altered)
  const book = alteredOutput.unrestricted.libraries[0]
  assert.equal(book.name, 'Changed by oracle test', 'library name is serialized from actual upstream model')
  assert.equal(book.folders[0].fullPath, '/changed/path', 'folder path is serialized by actual LibraryFolder.toOldJSON')
  assert.equal(book.settings.skipMatchingMediaWithIsbn, true, 'settings object is copied by actual Library.toOldJSON')
  assert.notDeepEqual(alteredOutput, actual, 'input field changes must be observable in oracle output')

  assert.deepEqual(actual.unrestricted.libraries.map((library) => library.id), ['book-library', 'podcast-library'],
    'empty librariesAccessible preserves the upstream no-filter behavior')
  assert.deepEqual(actual.restricted.libraries.map((library) => library.id), ['podcast-library'],
    'nonempty librariesAccessible filters through actual LibraryController.findAll')
  console.log('library oracle: upstream serializers, controller filtering, and golden output verified')
}

main().catch((error) => {
  process.stderr.write(`${error.stack || error}\n`)
  process.exitCode = 1
})
