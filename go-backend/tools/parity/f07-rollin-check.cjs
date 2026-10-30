#!/usr/bin/env node
'use strict'

// Self-contained F07 roll-in check. The snapshot manifest and every path in
// its copied DB must remain under the snapshot root; no source fixture path is
// consulted while this runner executes.
const fs = require('node:fs')
const path = require('node:path')
const net = require('node:net')
const crypto = require('node:crypto')
const { spawn, spawnSync } = require('node:child_process')
const { setTimeout: delay } = require('node:timers/promises')
const { createRequire } = require('node:module')

const ROOT = path.resolve(__dirname, '../../..')
const BASE = '/audiobookshelf'
const sqlite3 = createRequire(path.join(ROOT, 'package.json'))('sqlite3').verbose()

function inside(root, candidate) {
  const relative = path.relative(root, candidate)
  return relative === '' || (!relative.startsWith(`..${path.sep}`) && relative !== '..' && !path.isAbsolute(relative))
}
function all(dbPath, sql, params = []) {
  return new Promise((resolve, reject) => {
    const db = new sqlite3.Database(dbPath, sqlite3.OPEN_READONLY)
    db.all(sql, params, (error, rows) => db.close(closeError => error || closeError ? reject(error || closeError) : resolve(rows)))
  })
}
function run(dbPath, sql, params = []) {
  return new Promise((resolve, reject) => {
    const db = new sqlite3.Database(dbPath)
    db.run(sql, params, error => db.close(closeError => error || closeError ? reject(error || closeError) : resolve()))
  })
}
async function rewriteRoots(dbPath, pairs) {
  const replace = value => {
    let result = String(value)
    for (const [from, to] of pairs) result = result.split(from).join(to)
    return result === String(value) ? null : result
  }
  const columnsByTable = {
    libraryFolders: ['path'], libraryItems: ['path', 'libraryFiles', 'extraData'],
    books: ['coverPath', 'audioFiles', 'ebookFile'], podcasts: ['coverPath'],
    podcastEpisodes: ['audioFile', 'chapters', 'extraData'], authors: ['imagePath'], settings: ['value']
  }
  for (const [table, columns] of Object.entries(columnsByTable)) {
    for (const column of columns) {
      const rows = await all(dbPath, `SELECT rowid, "${column}" AS value FROM "${table}" WHERE "${column}" IS NOT NULL`)
      for (const row of rows) {
        const next = replace(row.value)
        if (next !== null) await run(dbPath, `UPDATE "${table}" SET "${column}"=? WHERE rowid=?`, [next, row.rowid])
      }
    }
  }
}
async function restrictReader(dbPath, username, libraryID) {
  const rows = await all(dbPath, 'SELECT id, permissions FROM users WHERE username=?', [username])
  if (rows.length !== 1) throw new Error(`expected one reader account ${username}, found ${rows.length}`)
  const permissions = JSON.parse(rows[0].permissions)
  permissions.accessAllLibraries = false
  permissions.librariesAccessible = [libraryID]
  await run(dbPath, 'UPDATE users SET permissions=? WHERE id=?', [JSON.stringify(permissions), rows[0].id])
}
async function logicalState(dbPath) {
  const schema = await all(dbPath, `SELECT name FROM sqlite_master WHERE type='table' AND name NOT LIKE 'sqlite_%' ORDER BY name`)
  const state = {}
  for (const { name } of schema) state[name] = await all(dbPath, `SELECT * FROM "${name.replaceAll('"', '""')}" ORDER BY rowid`)
  return state
}
function fileState(root, roots) {
  const files = {}
  const visit = dir => {
    for (const entry of fs.readdirSync(dir, { withFileTypes: true }).sort((a, b) => a.name.localeCompare(b.name))) {
      const full = path.join(dir, entry.name)
      const relative = path.relative(root, full)
      if (relative === 'logs' || relative.startsWith(`logs${path.sep}`)) continue
      if (entry.isDirectory()) visit(full)
      else {
        let bytes = fs.readFileSync(full)
        if (/\.(json|abs|opf|xml)$/.test(entry.name)) bytes = Buffer.from(normalize(bytes.toString('utf8'), roots))
        files[relative] = crypto.createHash('sha256').update(bytes).digest('hex')
      }
    }
  }
  visit(root)
  return files
}
async function freePort() {
  const server = net.createServer()
  await new Promise((resolve, reject) => server.listen(0, '127.0.0.1', error => error ? reject(error) : resolve()))
  const port = server.address().port
  await new Promise(resolve => server.close(resolve))
  return port
}
async function start(side, dirs, port, logPath, goBin) {
  const command = side === 'go' ? goBin : (process.env.NODE22 || 'mise')
  const args = side === 'go'
    ? []
    : ['exec', 'node@22', '--', 'node', path.join(ROOT, 'dist-server/index.js'), '--config', dirs.config, '--metadata', dirs.metadata, '--port', String(port), '--host', '127.0.0.1', '--source', 'docker']
  const env = side === 'go'
    ? { ...process.env, CONFIG_PATH: dirs.config, METADATA_PATH: dirs.metadata, HOST: '127.0.0.1', PORT: String(port), ROUTER_BASE_PATH: BASE, SOURCE: 'docker' }
    : { ...process.env, NODE_ENV: 'production', ROUTER_BASE_PATH: BASE }
  const child = spawn(command, args, { cwd: ROOT, env, stdio: ['ignore', 'pipe', 'pipe'] })
  const log = fs.createWriteStream(logPath)
  child.stdout.pipe(log); child.stderr.pipe(log)
  const base = `http://127.0.0.1:${port}`
  for (let i = 0; i < 120; i++) {
    if (child.exitCode !== null) throw new Error(`${side} exited before readiness; see ${logPath}`)
    try { if ((await fetch(`${base}${BASE}/ping`)).ok) return { child, log, base } } catch {}
    await delay(250)
  }
  child.kill('SIGINT')
  throw new Error(`${side} did not become ready; see ${logPath}`)
}
async function stop(server) {
  if (!server) return
  if (server.child.exitCode === null) {
    server.child.kill('SIGINT')
    await Promise.race([new Promise(resolve => server.child.once('exit', resolve)), delay(12000).then(() => server.child.kill('SIGKILL'))])
  }
  server.log.end()
}
async function request(base, route, options = {}) {
  const response = await fetch(`${base}${BASE}${route}`, {
    method: options.method || 'GET',
    headers: { ...(options.body ? { 'Content-Type': 'application/json' } : {}), ...(options.token ? { Authorization: `Bearer ${options.token}` } : {}) },
    body: options.body ? JSON.stringify(options.body) : undefined
  })
  const text = await response.text()
  let body
  try { body = JSON.parse(text) } catch { body = text }
  return { status: response.status, body }
}
function normalize(value, roots) {
  if (typeof value === 'string') {
    let result = value
    for (const [from, to] of roots) result = result.split(from).join(to)
    return result
  }
  if (Array.isArray(value)) return value.map(item => normalize(item, roots))
  if (value && typeof value === 'object') return Object.fromEntries(Object.entries(value).map(([key, item]) => [key, normalize(item, roots)]))
  return value
}

async function main() {
  const snapshotRoot = path.resolve(process.argv[2] || '')
  if (!process.argv[2]) throw new Error('usage: f07-rollin-check.cjs <self-contained-snapshot-dir>')
  const manifestPath = path.join(snapshotRoot, 'fixture-manifest.json')
  const manifest = JSON.parse(fs.readFileSync(manifestPath, 'utf8'))
  for (const key of ['config', 'metadata', 'media']) {
    const dir = path.join(snapshotRoot, key)
    if (!inside(snapshotRoot, dir) || !fs.statSync(dir).isDirectory()) throw new Error(`snapshot is missing local ${key} directory`)
  }
  for (const dir of Object.values(manifest.directories || {})) {
    if (!inside(snapshotRoot, path.resolve(dir))) throw new Error(`manifest references path outside snapshot: ${dir}`)
  }
  const workRoot = path.join(snapshotRoot, `rollin-check-${process.pid}`)
  fs.mkdirSync(workRoot, { recursive: true })
  const oldGo = manifest.directories.go
  const oldGoMedia = manifest.directories.goMedia
  const oldMetadata = path.join(oldGo, 'metadata')
  const sideDirs = {}
  const beforeStates = {}
  for (const side of ['go', 'node']) {
    const dir = path.join(workRoot, side)
    sideDirs[side] = { root: dir, config: path.join(dir, 'config'), metadata: path.join(dir, 'metadata'), media: path.join(dir, 'media') }
    for (const key of ['config', 'metadata', 'media']) fs.cpSync(path.join(snapshotRoot, key), sideDirs[side][key], { recursive: true, force: true })
    const dbPath = path.join(sideDirs[side].config, 'absdatabase.sqlite')
    await rewriteRoots(dbPath, [
      [oldGo, dir], [oldGoMedia, sideDirs[side].media], [oldMetadata, sideDirs[side].metadata],
      [path.join(snapshotRoot, 'media'), sideDirs[side].media], [path.join(snapshotRoot, 'metadata'), sideDirs[side].metadata]
    ])
    beforeStates[side] = await logicalState(dbPath)
    // Apply the same explicit restricted-reader variant as F06 to each
    // disposable copy after capturing the Go-written snapshot state.
    await restrictReader(dbPath, manifest.readerCredentials.username, manifest.libraries.book)
  }
  const beforeEqual = JSON.stringify(normalize(beforeStates.go, [[sideDirs.go.root, '<SNAPSHOT>']])) ===
    JSON.stringify(normalize(beforeStates.node, [[sideDirs.node.root, '<SNAPSHOT>']]))
  const buildPath = path.join(workRoot, 'audiobookshelf-go')
  const build = spawnSync('go', ['build', '-o', buildPath, './cmd/audiobookshelf-go'], { cwd: path.join(ROOT, 'go-backend'), encoding: 'utf8', env: { ...process.env, CGO_ENABLED: '0' } })
  if (build.status !== 0) throw new Error(`CGO-disabled Go build failed: ${build.stderr}`)
  const report = { snapshotRoot, selfContained: true, preBootLogicalStateEqual: beforeEqual, sides: {} }
  const servers = {}
  try {
    for (const side of ['go', 'node']) {
      servers[side] = await start(side, sideDirs[side], await freePort(), path.join(workRoot, `${side}.log`), buildPath)
      const login = await request(servers[side].base, '/login', { method: 'POST', body: manifest.rootCredentials })
      const token = login.body?.user?.accessToken
      const libs = await request(servers[side].base, '/api/libraries', { token })
      const readerLogin = await request(servers[side].base, '/login', { method: 'POST', body: manifest.readerCredentials })
      const readerToken = readerLogin.body?.user?.accessToken
      const visible = await request(servers[side].base, '/api/libraries', { token: readerToken })
      const deniedLibrary = await request(servers[side].base, `/api/libraries/${manifest.libraries.podcast}`, { token: readerToken })
      const deniedItem = await request(servers[side].base, `/api/items/${manifest.items.podcast}`, { token: readerToken })
      const item = await request(servers[side].base, `/api/items/${manifest.items.book}`, { token })
      const afterRuntimeState = await logicalState(path.join(sideDirs[side].config, 'absdatabase.sqlite'))
      delete afterRuntimeState.sessions
      for (const user of afterRuntimeState.users || []) {
        delete user.lastSeen
        delete user.updatedAt
      }
      report.sides[side] = {
        loginStatus: login.status, readerLoginStatus: readerLogin.status,
        libraryIDs: (libs.body?.libraries || []).map(row => row.id).sort(),
        restrictedLibraryIDs: (visible.body?.libraries || []).map(row => row.id).sort(),
        forbiddenLibrary: { status: deniedLibrary.status, leaksID: JSON.stringify(deniedLibrary.body).includes(manifest.libraries.podcast), leaksName: JSON.stringify(deniedLibrary.body).includes('Parity Podcasts') },
        forbiddenItem: { status: deniedItem.status, leaksID: JSON.stringify(deniedItem.body).includes(manifest.items.podcast), leaksTitle: JSON.stringify(deniedItem.body).includes('Sample Podcast') },
        bookItem: { status: item.status, id: item.body?.id, title: item.body?.media?.metadata?.title, audioFiles: item.body?.media?.audioFiles?.length || 0 },
        persistentState: normalize(afterRuntimeState, [[sideDirs[side].root, '<SNAPSHOT>']])
      }
    }
  } finally { await Promise.all([stop(servers.go), stop(servers.node)]) }
  for (const side of ['go', 'node']) {
    report.sides[side].files = Object.fromEntries(['metadata', 'media'].map(key => [key,
      fileState(sideDirs[side][key], [[sideDirs[side].root, '<SNAPSHOT>']])]))
  }
  report.filesMatched = JSON.stringify(report.sides.go.files) === JSON.stringify(report.sides.node.files)
  const expectedLibs = [manifest.libraries.book, manifest.libraries.podcast].sort()
  const paired = (report.sides.go.loginStatus === report.sides.node.loginStatus && report.sides.go.loginStatus === 200) &&
    report.sides.go.readerLoginStatus === 200 && report.sides.node.readerLoginStatus === 200 &&
    JSON.stringify(report.sides.go.libraryIDs) === JSON.stringify(expectedLibs) && JSON.stringify(report.sides.node.libraryIDs) === JSON.stringify(expectedLibs) &&
    JSON.stringify(report.sides.go.restrictedLibraryIDs) === JSON.stringify([manifest.libraries.book]) &&
    JSON.stringify(report.sides.node.restrictedLibraryIDs) === JSON.stringify([manifest.libraries.book]) &&
    report.sides.go.forbiddenLibrary.status === 403 && report.sides.node.forbiddenLibrary.status === 403 &&
    report.sides.go.forbiddenItem.status === 403 && report.sides.node.forbiddenItem.status === 403 &&
    !report.sides.go.forbiddenLibrary.leaksID && !report.sides.go.forbiddenLibrary.leaksName &&
    !report.sides.node.forbiddenLibrary.leaksID && !report.sides.node.forbiddenLibrary.leaksName &&
    !report.sides.go.forbiddenItem.leaksID && !report.sides.go.forbiddenItem.leaksTitle &&
    !report.sides.node.forbiddenItem.leaksID && !report.sides.node.forbiddenItem.leaksTitle &&
    JSON.stringify(report.sides.go.bookItem) === JSON.stringify(report.sides.node.bookItem) &&
    JSON.stringify(report.sides.go.persistentState) === JSON.stringify(report.sides.node.persistentState) &&
    report.filesMatched
  report.reopenContractMatched = paired
  report.stateTablesCompared = Object.keys(beforeStates.go)
  report.limits = ['Sessions are excluded from post-request state equality because each server independently creates random login sessions; users.lastSeen and users.updatedAt are volatile login fields. All other tables and columns are compared after both servers reopen the snapshot and execute the same root/reader requests.', 'The restricted-reader permission is applied identically to both disposable reopen copies after capturing the preserved snapshot state.', 'This verifies this snapshot and the selected F04-F06 state; it does not prove the complete mutation corpus or all restart behaviors.']
  fs.writeFileSync(path.join(workRoot, 'report.json'), JSON.stringify(report, null, 2) + '\n')
  console.log(`${beforeEqual ? 'PASS' : 'FAIL'} f07.preboot-logical-state-equal`)
  console.log(`${paired ? 'PASS' : 'FAIL'} f07.node-go-reopen-and-restricted-state`)
  console.log(`${report.filesMatched ? 'PASS' : 'FAIL'} f07.metadata-and-media-files`)
  console.log(`Report: ${path.join(workRoot, 'report.json')}`)
  process.exitCode = beforeEqual && paired ? 0 : 1
}
main().catch(error => { console.error(error.stack || error); process.exitCode = 1 })
