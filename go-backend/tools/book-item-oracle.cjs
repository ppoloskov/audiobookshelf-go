#!/usr/bin/env node
'use strict'

// Execute the pinned Book and LibraryItem serializers against fixed records.
// Sequelize, filesystem, and event dependencies are stubbed; serializer code
// and the formatting helpers it calls are loaded from upstream source.
const fs = require('node:fs')
const path = require('node:path')
const vm = require('node:vm')

const ROOT = path.resolve(__dirname, '../..')
const GOLDEN = path.join(ROOT, 'go-backend/contracts/parity/book-item-golden.json')
const BASELINE = 'edb1f7efc92ecacdef51aafb4d9d5054ce10604f'
const SOURCES = [
  'server/models/Book.js',
  'server/models/LibraryItem.js',
  'server/utils/index.js',
  'server/utils/htmlSanitizer.js',
  'server/utils/parsers/parseNameString.js'
]

function loadCommonJS(relativePath, requireStub) {
  const filename = path.join(ROOT, relativePath)
  const module = { exports: {} }
  const sandbox = { module, exports: module.exports, require: requireStub, console, Date, Promise, Buffer, Intl, structuredClone }
  vm.runInNewContext(fs.readFileSync(filename, 'utf8'), sandbox, { filename })
  return module.exports
}

function sourceHashes() {
  return Object.fromEntries(SOURCES.map(relative => [relative, require('node:crypto').createHash('sha256').update(fs.readFileSync(path.join(ROOT, relative))).digest('hex')]))
}

const prefix = '/fixture/books'
const timestamp = 1704067200000
function audio(ino, filename, duration, size, exclude = false) {
  return {
    index: 0,
    ino,
    metadata: { filename, ext: '.mp3', path: `${prefix}/${filename}`, relPath: filename, size, mtimeMs: timestamp, ctimeMs: timestamp, birthtimeMs: timestamp },
    addedAt: timestamp,
    updatedAt: timestamp,
    trackNumFromMeta: 1,
    discNumFromMeta: 1,
    trackNumFromFilename: 1,
    discNumFromFilename: 1,
    manuallyVerified: false,
    format: 'MP3',
    duration,
    bitRate: 128000,
    language: 'eng',
    codec: 'mp3',
    timeBase: '1/44100',
    channels: 2,
    channelLayout: 'stereo',
    chapters: [],
    metaTags: {},
    mimeType: 'audio/mpeg',
    exclude
  }
}

function item(id, index) {
  return {
    id: `item-${id}`,
    ino: `item-ino-${id}`,
    extraData: {},
    libraryId: 'book-library',
    libraryFolderId: 'book-folder',
    path: `${prefix}/${id}`,
    relPath: id,
    isFile: false,
    mtime: timestamp + index,
    ctime: timestamp + index,
    birthtime: timestamp + index,
    createdAt: timestamp + index,
    updatedAt: timestamp + index,
    lastScan: timestamp + index,
    lastScanVersion: '2.37.2',
    isMissing: false,
    isInvalid: false,
    mediaType: 'book',
    libraryFiles: [],
    numFiles: 0,
    size: 0
  }
}

function book(id, title, overrides = {}) {
  return {
    id,
    title,
    subtitle: null,
    publishedYear: null,
    publishedDate: null,
    publisher: null,
    description: '<p>A story</p>',
    isbn: null,
    asin: null,
    language: 'en',
    explicit: false,
    abridged: false,
    coverPath: null,
    duration: 12.5,
    narrators: ['Reader'],
    audioFiles: [audio(`audio-${id}`, `${id}.mp3`, 12.5, 100)],
    ebookFile: null,
    chapters: [{ id: 0, start: 0, end: 12.5, title: 'Chapter 1' }],
    tags: ['tag'],
    genres: ['fiction'],
    authors: [{ id: `author-${id}`, name: 'Ada Author' }],
    series: [{ id: `series-${id}`, name: 'First Series', bookSeries: { sequence: '1' } }],
    ...overrides
  }
}

const ebookFile = {
  ino: 'ebook-ino',
  ebookFormat: 'epub',
  metadata: {
    filename: 'ebook.epub',
    ext: '.epub',
    path: `${prefix}/ebook-cover/ebook.epub`,
    relPath: 'ebook.epub',
    size: 1234,
    mtimeMs: timestamp,
    ctimeMs: timestamp,
    birthtimeMs: timestamp
  }
}

const INPUT = {
  books: [
    {
      name: 'synthetic fixture book',
      item: item('fixture', 0),
      book: book('book-fixture', 'Deterministic Book', {
        description: '<p>A deterministic <strong>story</strong>.</p>',
        duration: 2,
        audioFiles: [audio('audio-fixture', 'deterministic.mp3', 2, 32000)],
        chapters: [{ id: 0, start: 0, end: 2, title: 'Chapter 1' }],
        authors: [{ id: 'fixture-author', name: 'Fixture Author' }],
        series: []
      }),
      ebookColumn: 'null'
    },
    {
      name: 'multiple audio files and excluded track',
      item: item('multi', 1),
      book: book('book-multi', 'The Multiple Book', {
        description: '<p>Multi <strong>file</strong> book.</p>',
        duration: 20,
        audioFiles: [audio('audio-multi-1', '01-first.mp3', 8, 80), audio('audio-multi-2', '02-excluded.mp3', 12, 120, true), audio('audio-multi-3', '03-last.mp3', 12, 120)],
        chapters: [{ id: 0, start: 0, end: 8, title: 'First' }, { id: 1, start: 8, end: 20, title: 'Last' }],
        series: [{ id: 'series-multi', name: 'Sequence Series', bookSeries: { sequence: '2.5' } }]
      }),
      ebookColumn: 'null'
    },
    {
      name: 'null description',
      item: item('null-description', 2),
      book: book('book-null-description', 'No Description', { description: null, audioFiles: [], chapters: [], narrators: [], authors: [], series: [] }),
      ebookColumn: 'null'
    },
    {
      name: 'series without sequence',
      item: item('no-sequence', 3),
      book: book('book-no-sequence', 'No Sequence', { audioFiles: [], chapters: [], series: [{ id: 'series-no-sequence', name: 'Unnumbered Series', bookSeries: { sequence: null } }] }),
      ebookColumn: 'null'
    },
    {
      name: 'explicit and abridged',
      item: item('explicit-abridged', 4),
      book: book('book-explicit-abridged', 'Explicit Abridged', { explicit: true, abridged: true, audioFiles: [audio('audio-explicit', 'explicit.mp3', 12.5, 100)] }),
      ebookColumn: 'null'
    },
    {
      name: 'non-null ebook, cover and null duration',
      item: item('ebook-cover', 5),
      book: book('book-ebook-cover', 'Ebook Cover', {
        coverPath: `${prefix}/ebook-cover/cover.jpg`,
        duration: null,
        audioFiles: [],
        chapters: [],
        tags: [],
        ebookFile,
        series: []
      }),
      ebookColumn: JSON.stringify(ebookFile)
    }
  ]
}

function execute(input) {
  global.ServerSettings = { sortingPrefixes: ['the', 'a'] }
  const utils = require(path.join(ROOT, 'server/utils'))
  const parseNameString = require(path.join(ROOT, 'server/utils/parsers/parseNameString'))
  const htmlSanitizer = require(path.join(ROOT, 'server/utils/htmlSanitizer'))
  class Model { constructor(values = {}) { Object.assign(this, values) } static init() {} static addHook() {} }
  const DataTypes = new Proxy({}, { get: (_target, key) => String(key) })
  const sequelize = { Model, DataTypes }
  const noop = new Proxy({}, { get: () => () => {} })
  const Book = loadCommonJS('server/models/Book.js', name => {
    if (name === 'sequelize') return sequelize
    if (name === '../Logger') return noop
    if (name === '../utils') return utils
    if (name === '../utils/parsers/parseNameString') return parseNameString
    if (name === '../utils/htmlSanitizer') return htmlSanitizer
    if (name === '../utils/queries/libraryItemsBookFilters' || name === '../SocketAuthority') return noop
    throw new Error(`Unexpected Book dependency: ${name}`)
  })
  const LibraryItem = loadCommonJS('server/models/LibraryItem.js', name => {
    if (name === 'path') return require('node:path')
    if (name === 'sequelize') return sequelize
    if (name === '../libs/fsExtra' || name === '../Logger' || name === '../utils/queries/libraryFilters' || name === '../utils/fileUtils') return noop
    if (name === '../objects/files/LibraryFile') return class {}
    if (name === './Book') return Book
    if (name === './Podcast') return class {}
    throw new Error(`Unexpected LibraryItem dependency: ${name}`)
  })

  const roundTrip = value => JSON.parse(JSON.stringify(value))
  return Object.fromEntries(input.books.map(fixture => {
    const media = new Book({ ...fixture.book })
    const itemValues = { ...fixture.item, media }
    for (const key of ['mtime', 'ctime', 'birthtime', 'createdAt', 'updatedAt', 'lastScan']) {
      if (itemValues[key] !== null && itemValues[key] !== undefined) itemValues[key] = new Date(itemValues[key])
    }
    const libraryItem = new LibraryItem(itemValues)
    return [fixture.name, {
      itemJSON: roundTrip(libraryItem.toOldJSON()),
      itemMinifiedJSON: roundTrip(libraryItem.toOldJSONMinified()),
      itemExpandedJSON: roundTrip(libraryItem.toOldJSONExpanded())
    }]
  }))
}

async function main() {
  const fixture = fs.existsSync(GOLDEN) ? JSON.parse(fs.readFileSync(GOLDEN, 'utf8')) : {}
  if (process.argv[2] === '--update') {
    fixture.contract = {
      name: 'Book-backed LibraryItem legacy, minified, and expanded serializers',
      scope: 'Actual pinned Book.toOldJSON/toOldJSONMinified/toOldJSONExpanded and LibraryItem.toOldJSON/toOldJSONMinified/toOldJSONExpanded methods; Sequelize, filesystem, and event infrastructure are stubbed. Database loading, routes, podcasts, and Socket.IO are outside this source fixture.',
      source: SOURCES
    }
    fixture.upstream = { baseline: BASELINE, sourceHashes: sourceHashes() }
    fixture.input = INPUT
    fixture.expected = execute(INPUT)
    fs.writeFileSync(GOLDEN, `${JSON.stringify(fixture, null, 2)}\n`)
    return
  }
  throw new Error('Usage: book-item-oracle.cjs --update')
}

module.exports = { BASELINE, SOURCES, INPUT, execute, sourceHashes }
if (require.main === module) main().catch(error => {
  process.stderr.write(`${error.stack || error}\n`)
  process.exitCode = 1
})
