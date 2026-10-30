#!/usr/bin/env node
'use strict'

// Rollback proof for the pinned v2.37.2 replacement: after a differential run
// has mutated the Go fixture copy, start the actual Node server against the
// Go-written database and verify it boots, authenticates, and serves the same
// catalog as the Go side did. The database must never be shared live: the
// check boots Node alone on a full copy (including WAL/SHM sidecars) of the
// Go fixture.

const fs = require('node:fs')
const path = require('node:path')
const crypto = require('node:crypto')
const { spawn } = require('node:child_process')

const ROOT = path.resolve(__dirname, '../../..')
const BASE = '/audiobookshelf'

// The runner mirrors create-fixture.cjs startServer/stopServer so both tools
// share lifecycle semantics.
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
    await new Promise((resolve) => setTimeout(resolve, 250))
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
      new Promise((resolve) => setTimeout(() => { server.child.kill('SIGKILL'); resolve() }, 15000))
    ])
  }
  server.log.end()
}

async function request(baseUrl, route, { method = 'GET', token, headers = {}, body } = {}) {
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
  return { status: response.status, value }
}

async function freePort() {
  const server = require('node:net').createServer()
  await new Promise((resolve, reject) => server.listen(0, '127.0.0.1', (error) => error ? reject(error) : resolve()))
  const port = server.address().port
  await new Promise((resolve) => server.close(resolve))
  return port
}

async function main() {
  const manifestPath = process.argv[2]
  if (!manifestPath) throw new Error('usage: rollback-check.cjs <fixture-manifest.json>')
  const manifest = JSON.parse(fs.readFileSync(manifestPath, 'utf8'))
  const goDir = manifest.directories.go
  // Copy the Go-written database tree so Node never shares live state.
  const workRoot = path.join(path.dirname(manifestPath), 'rollback')
  const dbTarget = path.join(workRoot, 'config')
  fs.rmSync(workRoot, { recursive: true, force: true })
  fs.mkdirSync(dbTarget, { recursive: true })
  fs.cpSync(path.join(goDir, 'config'), dbTarget, { recursive: true, force: true })
  // Metadata and media paths inside the Go database point into the go/ tree;
  // the rollback check reads catalog state only, so those paths are re-rooted
  // below (rewriteFixtureDatabasePaths equivalent, metadata/media only).
  const metadataTarget = path.join(workRoot, 'metadata')
  fs.cpSync(path.join(goDir, 'metadata'), metadataTarget, { recursive: true, force: true })
  const rewrite = (value) => {
    if (typeof value === 'string') {
      if (value.startsWith(manifest.directories.go)) return `${manifest.directories.node}${value.slice(manifest.directories.go.length)}`
      if (value.startsWith(manifest.directories.goMedia)) return `${manifest.directories.nodeMedia}${value.slice(manifest.directories.goMedia.length)}`
      if (value.startsWith(path.join(manifest.directories.go, 'metadata'))) return `${path.join(manifest.directories.node, 'metadata')}${value.slice((path.join(manifest.directories.go, 'metadata')).length)}`
      return value
    }
    if (Array.isArray(value)) return value.map(rewrite)
    if (value && typeof value === 'object') return Object.fromEntries(Object.entries(value).map(([key, entry]) => [key, rewrite(entry)]))
    return value
  }

  // Point the copied DB at the node media tree using the same column map as
  // create-fixture rewriteFixtureDatabasePaths.
  const sqlite3 = require('sqlite3').verbose()
  const db = new sqlite3.Database(path.join(dbTarget, 'absdatabase.sqlite'))
  const all = (sql) => new Promise((resolve, reject) => db.all(sql, (error, rows) => error ? reject(error) : resolve(rows)))
  const runWith = (sql, params) => new Promise((resolve, reject) => db.run(sql, params, (error) => error ? reject(error) : resolve()))
  const goAbs = manifest.directories.go
  const nodeAbs = manifest.directories.node
  const goMediaAbs = manifest.directories.goMedia
  const nodeMediaAbs = manifest.directories.nodeMedia
  // The rewrite() helper above handles JSON payloads; both are kept in sync.
  const rootPairs = [
    [goAbs, nodeAbs],
    [goMediaAbs, nodeMediaAbs]
  ]
  const renumber = (value) => {
    let next = value
    let touched = false
    for (const [from, to] of rootPairs) {
      if (next.includes(from)) {
        next = next.split(from).join(to)
        touched = true
      }
    }
    return touched ? next : null
  }
  for (const [table, columns] of Object.entries({
    libraryFolders: ['path'],
    libraryItems: ['path', 'libraryFiles', 'extraData'],
    books: ['coverPath', 'audioFiles', 'ebookFile'],
    podcasts: ['coverPath'],
    podcastEpisodes: ['audioFile', 'chapters', 'extraData'],
    authors: ['imagePath'],
    settings: ['value']
  })) {
    for (const column of columns) {
      const rows = await all(`SELECT rowid, \`${column}\` AS value FROM \`${table}\` WHERE \`${column}\` IS NOT NULL`)
      for (const row of rows) {
        const value = String(row.value)
        const next = renumber(value)
        if (next !== null) {
          await runWith(`UPDATE \`${table}\` SET \`${column}\`=? WHERE rowid=?`, [next, row.rowid])
        }
      }
    }
  }
  await new Promise((resolve, reject) => db.close((error) => error ? reject(error) : resolve()))

  const port = await freePort()
  const logPath = path.join(workRoot, 'rollback-node.log')
  const results = { started: false, ping: false, login: false, libraries: false, bookItem: false }
  let server = null
  try {
    server = await startServer(dbTarget, metadataTarget, port, logPath)
    results.started = true
    const ping = await request(server.baseUrl, '/ping')
    results.ping = ping.status === 200 && ping.value?.success === true

    const login = await request(server.baseUrl, '/login', { method: 'POST', body: manifest.rootCredentials })
    results.login = login.status === 200 && Boolean(login.value?.user?.accessToken)
    if (!results.login) throw new Error(`rollback login failed: ${login.status}`)
    const token = login.value.user.accessToken

    const libraries = await request(server.baseUrl, '/api/libraries', { token })
    const expectedLibraryIds = [manifest.libraries.book, manifest.libraries.podcast].sort()
    const observedLibraryIds = (Array.isArray(libraries.value?.libraries) ? libraries.value.libraries : [])
      .map((library) => library.id).sort()
    results.libraries = libraries.status === 200 && JSON.stringify(observedLibraryIds) === JSON.stringify(expectedLibraryIds)

    const itemPath = `/api/items/${manifest.items.book}`
    const item = await request(server.baseUrl, itemPath, { token })
    results.bookItem = item.status === 200 && Boolean(item.value?.id) &&
      item.value.media?.audioFiles?.length > 0 &&
      typeof item.value.media.metadata?.title === 'string'
  } finally {
    await stopServer(server)
  }

  const check = (name) => console.log(`${results[name] ? 'PASS' : 'FAIL'} rollback.${name}`)
  for (const name of Object.keys(results)) check(name)
  const passed = Object.values(results).every(Boolean)
  process.exitCode = passed ? 0 : 1
}

main().catch((error) => {
  process.stderr.write(`${error.stack || error}\n`)
  process.exitCode = 1
})