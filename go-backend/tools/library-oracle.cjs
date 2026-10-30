#!/usr/bin/env node
'use strict'

// Execute the pinned upstream serializers and list handler in an isolated VM.
// Only infrastructure dependencies are stubbed; the behavior under contract
// comes from server/models/{Library,LibraryFolder}.js and LibraryController.js.
const crypto = require('node:crypto')
const fs = require('node:fs')
const path = require('node:path')
const vm = require('node:vm')

const ROOT = path.resolve(__dirname, '../..')
const BASELINE = 'edb1f7efc92ecacdef51aafb4d9d5054ce10604f'
const SOURCES = [
  'server/models/Library.js',
  'server/models/LibraryFolder.js',
  'server/controllers/LibraryController.js'
]

function loadCommonJS(relativePath, requireStub) {
  const filename = path.join(ROOT, relativePath)
  const module = { exports: {} }
  const sandbox = {
    module,
    exports: module.exports,
    require: requireStub,
    console,
    Intl,
    Date,
    Promise,
    Buffer,
    setTimeout,
    clearTimeout
  }
  vm.runInNewContext(fs.readFileSync(filename, 'utf8'), sandbox, { filename })
  return module.exports
}

function hashFile(relativePath) {
  return crypto.createHash('sha256').update(fs.readFileSync(path.join(ROOT, relativePath))).digest('hex')
}

async function execute(input) {
  const Model = class {
    constructor(values = {}) { Object.assign(this, values) }
    static init() {}
  }
  const DataTypes = new Proxy({ UUIDV4: 'UUIDV4' }, { get: (target, key) => target[key] || String(key) })
  const sequelize = { DataTypes, Model }
  const LibraryFolder = loadCommonJS('server/models/LibraryFolder.js', (name) => {
    if (name === 'sequelize') return sequelize
    throw new Error(`Unexpected LibraryFolder dependency: ${name}`)
  })
  const Library = loadCommonJS('server/models/Library.js', (name) => {
    if (name === 'sequelize') return sequelize
    if (name === '../Logger') return { debug() {}, error() {} }
    throw new Error(`Unexpected Library dependency: ${name}`)
  })

  const libraries = input.libraries.map((record) => {
    const folders = record.libraryFolders.map((folder) => new LibraryFolder({
      ...folder,
      createdAt: new Date(folder.createdAt),
      updatedAt: new Date(folder.updatedAt)
    }))
    return new Library({
      ...record,
      lastScan: record.lastScan === null ? null : new Date(record.lastScan),
      createdAt: new Date(record.createdAt),
      updatedAt: new Date(record.updatedAt),
      libraryFolders: folders
    })
  })

  // Minimal external dependencies permit evaluation of the real controller
  // module. The exercised findAll method only reads Database and, when asked
  // for stats, the two filter stubs below.
  const db = { libraryModel: { async getAllWithFolders() { return libraries } } }
  const noOp = new Proxy({}, { get: () => () => {} })
  const controller = loadCommonJS('server/controllers/LibraryController.js', (name) => {
    if (name === 'express' || name === 'sequelize') return {}
    if (name === '../libs/fastSort') return { createNewSortInstance: () => ({ sort: (items) => items }) }
    if (name === '../Database') return db
    if (name === '../utils/queries/libraryItemsBookFilters') return { getBookLibraryStats: async () => ({}) }
    if (name === '../utils/queries/libraryItemsPodcastFilters') return { getPodcastLibraryStats: async () => ({}) }
    return noOp
  })

  const responses = {}
  for (const scenario of input.scenarios) {
    let body
    await controller.findAll({
      user: { permissions: { librariesAccessible: scenario.librariesAccessible } },
      query: {}
    }, { json(value) { body = value } })
    responses[scenario.name] = body
  }
  return JSON.parse(JSON.stringify(responses))
}

module.exports = { BASELINE, ROOT, SOURCES, execute, hashFile }

if (require.main === module) {
  const fixturePath = path.join(ROOT, 'go-backend/contracts/library-golden.json')
  if (process.argv[2] !== '--update') {
    process.stderr.write('Usage: node go-backend/tools/library-oracle.cjs --update\n')
    process.exitCode = 2
  } else {
    const fixture = JSON.parse(fs.readFileSync(fixturePath, 'utf8'))
    fixture.upstream = {
      baseline: BASELINE,
      sourceHashes: Object.fromEntries(SOURCES.map((source) => [source, hashFile(source)]))
    }
    execute(fixture.input).then((expected) => {
      fixture.expected = expected
      fs.writeFileSync(fixturePath, `${JSON.stringify(fixture, null, 2)}\n`)
    }).catch((error) => {
      process.stderr.write(`${error.stack || error}\n`)
      process.exitCode = 1
    })
  }
}
