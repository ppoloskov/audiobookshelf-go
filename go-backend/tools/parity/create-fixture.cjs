#!/usr/bin/env node
'use strict'

// Create deterministic test media, initialize a fresh upstream database,
// capture its pre-root schema/defaults, seed it through upstream APIs, then
// produce independent config/metadata copies for the Node and Go servers.
const fs = require('node:fs')
const fsp = require('node:fs/promises')
const crypto = require('node:crypto')
const os = require('node:os')
const path = require('node:path')
const net = require('node:net')
const { spawn, spawnSync } = require('node:child_process')
const { setTimeout: delay } = require('node:timers/promises')

const ROOT = path.resolve(__dirname, '../../..')
const BASE = '/audiobookshelf'
const USERNAME = 'oracle-root'
const PASSWORD = 'oracle-test-password'
const READER_USERNAME = 'oracle-reader'
const READER_PASSWORD = 'oracle-reader-password'
const createSourceBackup = process.argv.includes('--source-backup')

async function freePort() {
  const server = net.createServer()
  await new Promise((resolve, reject) => server.listen(0, '127.0.0.1', (error) => error ? reject(error) : resolve()))
  const port = server.address().port
  await new Promise((resolve, reject) => server.close((error) => error ? reject(error) : resolve()))
  return port
}

async function request(baseUrl, route, { method = 'GET', token, body, headers = {} } = {}) {
  const response = await fetch(`${baseUrl}${BASE}${route}`, {
    method,
    headers: {
      ...(body === undefined ? {} : { 'Content-Type': 'application/json' }),
      ...(token ? { Authorization: `Bearer ${token}` } : {}),
      ...headers
    },
    body: body === undefined ? undefined : JSON.stringify(body)
  })
  const bytes = Buffer.from(await response.arrayBuffer())
  let value = null
  try { value = JSON.parse(bytes.toString('utf8')) } catch { value = bytes.toString('utf8') || null }
  if (!response.ok) throw new Error(`${method} ${route} -> ${response.status}: ${JSON.stringify(value)}`)
  return { status: response.status, value }
}

async function startServer(config, metadata, port, logPath) {
  const log = fs.createWriteStream(logPath)
  const child = spawn(process.execPath, [
    path.join(ROOT, 'dist-server/index.js'), '--config', config, '--metadata', metadata,
    '--port', String(port), '--host', '127.0.0.1', '--source', 'docker'
  ], { cwd: ROOT, env: { ...process.env, NODE_ENV: 'production' }, stdio: ['ignore', 'pipe', 'pipe'] })
  child.stdout.pipe(log)
  child.stderr.pipe(log)
  const baseUrl = `http://127.0.0.1:${port}`
  let lastError = ''
  for (let i = 0; i < 120; i++) {
    if (child.exitCode !== null) throw new Error(`upstream server exited early; log: ${logPath}`)
    try {
      const response = await fetch(`${baseUrl}${BASE}/ping`)
      if (response.ok) return { child, log, baseUrl }
    } catch (error) { lastError = error.message }
    await delay(250)
  }
  child.kill('SIGINT')
  throw new Error(`upstream server did not become ready (${lastError}); log: ${logPath}`)
}

async function stopServer(server) {
  if (!server) return
  if (server.child.exitCode === null) {
    server.child.kill('SIGINT')
    await Promise.race([
      new Promise((resolve) => server.child.once('exit', resolve)),
      delay(15000).then(() => { server.child.kill('SIGKILL') })
    ])
  }
  server.log.end()
}

function copyTree(source, target) {
  fs.cpSync(source, target, { recursive: true, force: true, preserveTimestamps: true })
}

async function waitForBook(baseUrl, token, libraryId) {
  for (let i = 0; i < 90; i++) {
    const { value } = await request(baseUrl, `/api/libraries/${libraryId}/items?limit=10&page=0`, { token })
    const item = value.results?.find((result) => result.mediaType === 'book' && result.media?.metadata?.title === 'Deterministic Book')
    if (item) return item
    await delay(500)
  }
  throw new Error('upstream scan did not add the synthetic book within 45 seconds')
}

function rewriteFixturePaths(value, rootPairs) {
  if (typeof value === 'string') {
    for (const [fromRoot, toRoot] of rootPairs) {
      if (value.startsWith(fromRoot)) return `${toRoot}${value.slice(fromRoot.length)}`
    }
    return value
  }
  if (Array.isArray(value)) return value.map((entry) => rewriteFixturePaths(entry, rootPairs))
  if (value && typeof value === 'object') return Object.fromEntries(Object.entries(value).map(([key, entry]) => [key, rewriteFixturePaths(entry, rootPairs)]))
  return value
}

async function rewriteFixtureDatabasePaths(databasePath, rootPairs) {
  const sqlite3 = require('sqlite3').verbose()
  const db = new sqlite3.Database(databasePath)
  const run = (sql, params = []) => new Promise((resolve, reject) => db.run(sql, params, (error) => error ? reject(error) : resolve()))
  const all = (sql, params = []) => new Promise((resolve, reject) => db.all(sql, params, (error, rows) => error ? reject(error) : resolve(rows)))
  try {
    await run('BEGIN IMMEDIATE')
    const columnsByTable = {
      libraryFolders: ['path'],
      libraryItems: ['path', 'libraryFiles', 'extraData'],
      books: ['coverPath', 'audioFiles', 'ebookFile'],
      podcasts: ['coverPath'],
      podcastEpisodes: ['audioFile', 'chapters', 'extraData'],
      authors: ['imagePath'],
      settings: ['value']
    }
    for (const [table, columns] of Object.entries(columnsByTable)) {
      for (const column of columns) {
        const rows = await all(`SELECT rowid, \`${column}\` AS value FROM \`${table}\` WHERE \`${column}\` IS NOT NULL`)
        for (const row of rows) {
          let next
          if (['libraryFiles', 'extraData', 'audioFiles', 'ebookFile', 'chapters', 'value'].includes(column)) {
            try { next = JSON.stringify(rewriteFixturePaths(JSON.parse(row.value), rootPairs)) } catch { continue }
          } else {
            next = rewriteFixturePaths(row.value, rootPairs)
          }
          if (next !== row.value) await run(`UPDATE \`${table}\` SET \`${column}\`=? WHERE rowid=?`, [next, row.rowid])
        }
      }
    }
    await run('COMMIT')
  } catch (error) {
    await run('ROLLBACK').catch(() => {})
    throw error
  } finally {
    await new Promise((resolve, reject) => db.close((error) => error ? reject(error) : resolve()))
  }
}

// Read the inode of the first JSON file entry of a table column (null when absent).
async function firstAudioIno(databasePath, table, column) {
  const sqlite3 = require('sqlite3').verbose()
  const db = new sqlite3.Database(databasePath)
  const all = (sql, params = []) => new Promise((resolve, reject) => db.all(sql, params, (error, rows) => error ? reject(error) : resolve(rows)))
  try {
    const rows = await all(`SELECT ${column} AS files FROM ${table} WHERE ${column} IS NOT NULL`)
    for (const row of rows) {
      try {
        const files = JSON.parse(row.files)
        if (Array.isArray(files) && files[0]?.ino !== undefined) return String(files[0].ino)
      } catch {}
    }
    return null
  } finally {
    await new Promise((resolve, reject) => db.close((error) => error ? reject(error) : resolve()))
  }
}

function statInoOf(path) {
  try { return String(fs.statSync(path).ino) } catch { return null }
}

// Set inode columns/fields to the REAL inodes of the copied tree so the
// database describes this copy's files (copies cannot share inode numbers).
async function rewriteFixtureInodes(databasePath) {
  const sqlite3 = require('sqlite3').verbose()
  const db = new sqlite3.Database(databasePath)
  const run = (sql, params = []) => new Promise((resolve, reject) => db.run(sql, params, (error) => error ? reject(error) : resolve()))
  const all = (sql, params = []) => new Promise((resolve, reject) => db.all(sql, params, (error, rows) => error ? reject(error) : resolve(rows)))
  const restatFiles = (value) => {
    let parsed
    try { parsed = JSON.parse(value) } catch { return null }
    const rows = Array.isArray(parsed) ? parsed : []
    let changed = false
    for (const entry of rows) {
      const filePath = entry?.metadata?.path
      if (typeof filePath !== 'string') continue
      const ino = statInoOf(filePath)
      if (ino !== null && entry.ino !== ino) {
        entry.ino = ino
        changed = true
      }
    }
    return changed ? JSON.stringify(rows) : null
  }
  try {
    await run('BEGIN IMMEDIATE')
    for (const row of await all(`SELECT rowid, path, libraryFiles FROM libraryItems`)) {
      if (typeof row.path === 'string') {
        const ino = statInoOf(row.path)
        if (ino !== null) await run(`UPDATE libraryItems SET ino=? WHERE rowid=?`, [ino, row.rowid])
      }
      if (typeof row.libraryFiles === 'string') {
        const next = restatFiles(row.libraryFiles)
        if (next !== null) await run(`UPDATE libraryItems SET libraryFiles=? WHERE rowid=?`, [next, row.rowid])
      }
    }
    for (const table of ['books', 'podcastEpisodes']) {
      for (const row of await all(`SELECT rowid, ${table === 'books' ? 'audioFiles' : 'audioFile'} AS files FROM ${table}`)) {
        if (typeof row.files !== 'string') continue
        const next = restatFiles(row.files)
        if (next !== null) await run(`UPDATE ${table} SET ${table === 'books' ? 'audioFiles' : 'audioFile'}=? WHERE rowid=?`, [next, row.rowid])
      }
    }
    await run('COMMIT')
  } catch (error) {
    await run('ROLLBACK').catch(() => {})
    throw error
  } finally {
    await new Promise((resolve, reject) => db.close((error) => error ? reject(error) : resolve()))
  }
}

function rewriteMetadataPaths(dir, rootPairs) {
  if (!fs.existsSync(dir)) return
  for (const entry of fs.readdirSync(dir, { withFileTypes: true })) {
    const fullPath = path.join(dir, entry.name)
    if (entry.isDirectory()) rewriteMetadataPaths(fullPath, rootPairs)
    else if (entry.isFile() && entry.name.endsWith('.json')) {
      try {
        const parsed = JSON.parse(fs.readFileSync(fullPath, 'utf8'))
        fs.writeFileSync(fullPath, `${JSON.stringify(rewriteFixturePaths(parsed, rootPairs), null, 2)}\n`)
      } catch {}
    }
  }
}

async function main() {
  const workDir = path.resolve(process.argv[2] || fs.mkdtempSync(path.join(os.tmpdir(), 'abs-parity-')))
  const config = path.join(workDir, 'node', 'config')
  const metadata = path.join(workDir, 'node', 'metadata')
  const media = path.join(workDir, 'node', 'media')
  const books = path.join(media, 'books')
  const podcasts = path.join(media, 'podcasts')
  const libraryCrudFolder = path.join(media, 'library-crud')
  for (const dir of [config, metadata, books, podcasts, libraryCrudFolder]) fs.mkdirSync(dir, { recursive: true })

  const bookDir = path.join(books, 'A. Reader', 'Deterministic Book')
  fs.mkdirSync(bookDir, { recursive: true })
  const audioFile = path.join(bookDir, 'Deterministic Book.m4b')
  const ffmpeg = spawnSync('ffmpeg', [
    '-hide_banner', '-loglevel', 'error', '-f', 'lavfi', '-i', 'anullsrc=r=44100:cl=mono',
    '-t', '2', '-c:a', 'aac', '-metadata', 'title=Deterministic Book', '-metadata', 'artist=A. Reader',
    '-y', audioFile
  ], { encoding: 'utf8' })
  if (ffmpeg.status !== 0) throw new Error(`ffmpeg could not create fixture media: ${ffmpeg.stderr}`)

  // Distinct folder-derived series, discovered by the real upstream scan.
  // Keep these after the original book so its media path and fixture ID remain stable.
  for (const seriesName of ['Series A', 'Series B', 'Series C', 'Series D', 'Series E']) {
    const title = `${seriesName} Book`
    const seriesBookDir = path.join(books, 'Parity Author', seriesName, title)
    fs.mkdirSync(seriesBookDir, { recursive: true })
    const seriesAudio = path.join(seriesBookDir, `${title}.m4b`)
    const generated = spawnSync('ffmpeg', [
      '-hide_banner', '-loglevel', 'error', '-f', 'lavfi', '-i', 'anullsrc=r=44100:cl=mono',
      '-t', '2', '-c:a', 'aac', '-metadata', `title=${title}`, '-metadata', 'artist=Parity Author',
      '-metadata', `album=${seriesName}`,
      '-y', seriesAudio
    ], { encoding: 'utf8' })
    if (generated.status !== 0) throw new Error(`ffmpeg could not create ${title}: ${generated.stderr}`)
  }

  const port = await freePort()
  let manifestExtras = null
  let upstream
  try {
    upstream = await startServer(config, metadata, port, path.join(workDir, 'upstream.log'))
    const baseUrl = upstream.baseUrl
    // Snapshot after Database.init/MigrationManager, before root account creation.
    const dbPath = path.join(config, 'absdatabase.sqlite')
    const exporter = spawnSync(process.execPath, [
      path.join(ROOT, 'go-backend/tools/parity/export-schema.cjs'), '--db', dbPath,
      '--out', path.join(ROOT, 'go-backend/contracts/parity')
    ], { cwd: ROOT, encoding: 'utf8' })
    if (exporter.status !== 0) throw new Error(`schema export failed: ${exporter.stderr || exporter.stdout}`)

    await request(baseUrl, '/init', { method: 'POST', body: { newRoot: { username: USERNAME, password: PASSWORD } } })
    const login = await request(baseUrl, '/login', {
      method: 'POST', body: { username: USERNAME, password: PASSWORD }, headers: { 'X-Return-Tokens': 'true' }
    })
    const token = login.value.user.accessToken
    const userId = login.value.user.id
    if (!token || !userId) throw new Error('upstream login did not return access token and user ID')

    const bookLibrary = await request(baseUrl, '/api/libraries', {
      method: 'POST', token, body: {
        name: 'Parity Books', mediaType: 'book', provider: 'google',
        settings: { skipMatchingMediaWithAsin: true, skipMatchingMediaWithIsbn: true },
        folders: [{ fullPath: books }]
      }
    })
    const podcastLibrary = await request(baseUrl, '/api/libraries', {
      method: 'POST', token, body: { name: 'Parity Podcasts', mediaType: 'podcast', provider: 'podcastIndex', folders: [{ fullPath: podcasts }] }
    })
    const bookLibraryId = bookLibrary.value.id
    const podcastLibraryId = podcastLibrary.value.id
    const podcastFolderId = podcastLibrary.value.folders[0].id
    const podcast = await request(baseUrl, '/api/podcasts', {
      method: 'POST', token, body: {
        libraryId: podcastLibraryId, folderId: podcastFolderId,
        path: path.join(podcasts, 'Sample Podcast'),
        media: { metadata: { title: 'Sample Podcast', author: 'Fixture Author', description: 'Offline parity fixture' } }
      }
    })

    await request(baseUrl, '/api/users', {
      method: 'POST', token,
      body: {
        username: READER_USERNAME, password: READER_PASSWORD, type: 'user', isActive: true,
        librariesAccessible: [bookLibraryId]
      }
    })

    await request(baseUrl, `/api/libraries/${bookLibraryId}/scan`, { method: 'POST', token, body: {} })
    const bookItem = await waitForBook(baseUrl, token, bookLibraryId)
    const bookDetail = await request(baseUrl, `/api/items/${bookItem.id}`, { token })
    const audioFileId = bookDetail.value.libraryFiles?.find((file) => file.fileType === 'audio')?.ino
    if (!audioFileId) throw new Error('upstream expanded book did not expose an audio file ID')
    const coverAsset = path.join(workDir, 'cover-fixture.png')
    const coverGeneration = spawnSync('ffmpeg', [
      '-hide_banner', '-loglevel', 'error', '-f', 'lavfi', '-i', 'color=c=red:s=32x32',
      '-frames:v', '1', '-y', coverAsset
    ], { encoding: 'utf8' })
    if (coverGeneration.status !== 0) throw new Error(`ffmpeg could not create fixture cover: ${coverGeneration.stderr}`)
    const coverForm = new FormData()
    coverForm.append('cover', new Blob([fs.readFileSync(coverAsset)], { type: 'image/png' }), 'cover-fixture.png')
    const coverUpload = await fetch(`${baseUrl}${BASE}/api/items/${bookItem.id}/cover`, {
      method: 'POST', headers: { Authorization: `Bearer ${token}` }, body: coverForm,
      signal: AbortSignal.timeout(30000)
    })
    const coverUploadText = await coverUpload.text()
    if (!coverUpload.ok) throw new Error(`upstream cover upload returned ${coverUpload.status}: ${coverUploadText}`)
    await request(baseUrl, `/api/me/progress/${bookItem.id}`, {
      method: 'PATCH', token, body: { duration: 2, currentTime: 1, progress: 0.5, isFinished: false }
    })

    // Seed richer existing-database state so the fixtures exercise a real
    // v2.37.2 database with sessions, collections and playlists present at
    // boot rather than a bare fresh init.
    let playbackSessionId = null
    const play = await request(baseUrl, `/api/items/${bookItem.id}/play`, { method: 'POST', token, body: {} })
    playbackSessionId = play.value?.session?.id || play.value?.id || null
    if (!playbackSessionId) throw new Error(`upstream play did not open a session: ${play.status} ${JSON.stringify(play.value).slice(0, 200)}`)
    const closed = await request(baseUrl, `/api/session/${playbackSessionId}/close`, { method: 'POST', token, body: {} })
    if (closed.status !== 200) throw new Error(`session close returned ${closed.status}`)
    const collectionSeed = await request(baseUrl, '/api/collections', {
      method: 'POST', token, body: { libraryId: bookLibraryId, name: 'Fixture Collection', description: 'Seeded at fixture time', books: [bookItem.id] }
    })
    if (!collectionSeed.value?.id) throw new Error(`collection seed failed: ${collectionSeed.status} ${JSON.stringify(collectionSeed.value).slice(0, 200)}`)
    const playlistSeed = await request(baseUrl, '/api/playlists', {
      method: 'POST', token, body: { libraryId: bookLibraryId, name: 'Fixture Playlist', description: 'Seeded at fixture time', items: [{ libraryItemId: bookItem.id }] }
    })
    if (!playlistSeed.value?.id) throw new Error(`playlist seed failed: ${playlistSeed.status} ${JSON.stringify(playlistSeed.value).slice(0, 200)}`)
    manifestExtras = { playbackSession: playbackSessionId, collection: collectionSeed.value.id, playlist: playlistSeed.value.id }

    let sourceBackup = null
    if (createSourceBackup) {
      const authorId = bookDetail.value.media?.metadata?.authors?.[0]?.id
      if (!authorId) throw new Error('upstream scanned book did not expose its synthetic author ID')
      const imageBytes = Buffer.from('iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO+/p0sAAAAASUVORK5CYII=', 'base64')
      // Keep one synthetic author metadata asset so the upstream backup exercises both
      // metadata directories without relying on a remote image provider.
      fs.mkdirSync(path.join(metadata, 'authors'), { recursive: true })
      fs.writeFileSync(path.join(metadata, 'authors', `${authorId}.png`), imageBytes)
      const backups = await request(baseUrl, '/api/backups', { method: 'POST', token })
      const backup = backups.value.backups?.[0]
      if (!backup?.id) throw new Error('upstream backup request did not return a backup ID')
      const download = await fetch(`${baseUrl}${BASE}/api/backups/${encodeURIComponent(backup.id)}/download`, {
        headers: { Authorization: `Bearer ${token}` }, signal: AbortSignal.timeout(30000)
      })
      if (!download.ok) throw new Error(`upstream backup download returned ${download.status}`)
      const archivePath = path.join(workDir, 'upstream-source.audiobookshelf')
      const archiveBytes = Buffer.from(await download.arrayBuffer())
      await fsp.writeFile(archivePath, archiveBytes)
      const entries = spawnSync('unzip', ['-Z1', archivePath], { encoding: 'utf8' })
      if (entries.status !== 0) throw new Error(`could not inspect upstream backup archive: ${entries.stderr}`)
      const entryNames = entries.stdout.trim().split('\n').filter(Boolean)
      for (const required of ['absdatabase.sqlite', 'details']) {
        if (!entryNames.includes(required)) throw new Error(`upstream backup is missing required root entry ${required}`)
      }
      if (!entryNames.some((entry) => entry.startsWith('metadata-items/'))) throw new Error('upstream backup is missing metadata-items entries')
      if (!entryNames.some((entry) => entry.startsWith('metadata-authors/'))) throw new Error('upstream backup is missing metadata-authors entries')
      const details = spawnSync('unzip', ['-p', archivePath, 'details'], { encoding: 'utf8' })
      if (details.status !== 0 || details.stdout.trimEnd().split('\n').length !== 4) throw new Error('upstream backup details must contain four newline-delimited fields')
      sourceBackup = {
        path: archivePath,
        sha256: crypto.createHash('sha256').update(archiveBytes).digest('hex'),
        bytes: archiveBytes.length,
        entries: entryNames
      }
    }

    // Stop Node before copying SQLite and metadata so neither side can share
    // WAL files or mutate the other's fixture state. --dirty-shutdown instead
    // kills the process hard, leaving the SQLite WAL on disk for the
    // existing-database recovery variation.
    if (process.argv.includes('--dirty-shutdown')) {
      upstream.child.kill('SIGKILL')
      await new Promise((resolve) => upstream.child.once('exit', resolve))
      upstream.log.end()
      upstream = null
    } else {
      await stopServer(upstream)
      upstream = null
    }
    const goDir = path.join(workDir, 'go')
    copyTree(path.join(workDir, 'node'), goDir)
    const goMedia = path.join(goDir, 'media')
    const goMetadata = path.join(goDir, 'metadata')
    const pathRootPairs = [[media, goMedia], [metadata, goMetadata]]
    const goDatabase = path.join(goDir, 'config', 'absdatabase.sqlite')
    await rewriteFixtureDatabasePaths(goDatabase, pathRootPairs)
    // Copied files have new filesystem inodes; point the copied database's
    // inode columns at the actual copied files so neither server's rescan
    // sees spurious library-file changes.
    await rewriteFixtureInodes(goDatabase)
    const goAudioFileIno = await firstAudioIno(goDatabase, 'books', 'audioFiles')
    rewriteMetadataPaths(path.join(goDir, 'metadata', 'items'), pathRootPairs)
    const manifest = {
      baseline: 'edb1f7efc92ecacdef51aafb4d9d5054ce10604f',
      rootCredentials: { username: USERNAME, password: PASSWORD },
      readerCredentials: { username: READER_USERNAME, password: READER_PASSWORD },
      userId,
      libraries: { book: bookLibraryId, podcast: podcastLibraryId },
      items: { book: bookItem.id, podcast: podcast.value.id, audioFile: audioFileId, audioFileGo: goAudioFileIno },
      extras: manifestExtras,
      endpoints: { basePath: BASE, apiPrefix: `${BASE}/api` },
      directories: { node: path.join(workDir, 'node'), go: goDir, media, nodeMedia: media, goMedia },
      databaseRelativePath: 'config/absdatabase.sqlite',
      coverAsset,
      libraryCrudFolders: { node: libraryCrudFolder, go: path.join(goDir, 'media', 'library-crud') },
      ...(sourceBackup ? { sourceBackup } : {}),
      notes: [
        'Generated through the actual pinned Node server and APIs.',
        'Node and Go receive independent config and metadata copies of the exact same database, preserving user and object IDs.',
        'Node and Go receive independent media tree copies; only explicit path columns and nested path-bearing JSON values in the Go fixture are rewritten from the Node media root to the Go media root.',
        'Seeded existing-database state: a closed playback session, a collection and a playlist are present before the copies are made.',
        'Synthetic media contains generated silent audio only; no user data or external credentials are used.'
      ]
    }
    fs.writeFileSync(path.join(workDir, 'fixture-manifest.json'), `${JSON.stringify(manifest, null, 2)}\n`)
    console.log(JSON.stringify({ workDir, userId, libraryIds: manifest.libraries, itemIds: manifest.items, fixtureManifest: path.join(workDir, 'fixture-manifest.json') }, null, 2))
  } finally {
    await stopServer(upstream)
  }
}

main().catch((error) => {
  process.stderr.write(`${error.stack || error}\n`)
  process.exitCode = 1
})
