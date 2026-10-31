#!/usr/bin/env node
'use strict'

// Isolated persisted-state comparison for playback-session and progress writes.
// The reference and Go servers are never live against the same writable state.
const fs = require('node:fs')
const os = require('node:os')
const path = require('node:path')
const { spawnSync } = require('node:child_process')
const { setTimeout: delay } = require('node:timers/promises')
const { freePort, spawnLogged, waitReady, stop: stopProcess } = require('./lib/process.cjs')
const { createRequester } = require('./lib/http.cjs')
const { writeJSON } = require('./lib/artifacts.cjs')
const { parseArgs } = require('./lib/args.cjs')

const ROOT = path.resolve(__dirname, '../../..')
const PREFIX = '/audiobookshelf'
const USERNAME = 'persisted-check-root'
const PASSWORD = 'persisted-check-password'

function args() {
  const out = parseArgs()
  if (!out['go-bin']) throw new Error('Usage: node tools/parity/persisted-state-check.cjs --go-bin <binary> [--out <json>] [--work-dir <dir>]')
  return out
}
const port = freePort
const logged = spawnLogged
const stop = server => stopProcess(server, 12000)
const request = createRequester({ pathPrefix: PREFIX, throwOnError: true, valueOnly: true })
const ready = (p, url, logPath) => waitReady(p, url, { pathPrefix: PREFIX, logPath })
function sqlite(dbPath, sql) {
  const r = spawnSync('sqlite3', ['-json', dbPath, sql], { encoding: 'utf8' })
  if (r.status !== 0) throw new Error(`sqlite3 failed: ${r.stderr}`)
  return JSON.parse(r.stdout || '[]')
}
const queries = {
  // createdAt ASC aligns logically identical rows across independently
  // initialized fixtures (ids and timestamps differ per side, but the
  // creation ORDER is a lifecycle fact both sides must agree on).
  playbackSessions: 'SELECT * FROM playbackSessions ORDER BY createdAt, id',
  mediaProgresses: 'SELECT * FROM mediaProgresses ORDER BY userId,mediaItemId,id',
  devices: 'SELECT * FROM devices ORDER BY createdAt, id'
}
function snapshot(db) { return Object.fromEntries(Object.entries(queries).map(([table, sql]) => [table, sqlite(db, sql)])) }
const UUID_RE = /^[0-9a-f]{8}-[0-9a-f-]{27}$/i
// JSON-encoded TEXT columns (extraData, mediaMetadata, deviceConfig) carry
// per-side UUIDs as strings; decode strings that parse as a JSON object/array
// and normalize their contents so independently initialized databases compare
// on shape, not on fixture-derived identifiers.
function normalizedString(value, ids) {
  const trimmed = value.trim()
  if (!trimmed.startsWith('{') && !trimmed.startsWith('[')) return value
  try {
    const parsed = JSON.parse(trimmed)
    if (typeof parsed !== 'object' || parsed === null) return value
    return JSON.stringify(normalized(parsed, ids))
  } catch {
    return value
  }
}
function normalized(value, ids = new Map()) {
  if (Array.isArray(value)) return value.map(v => normalized(v, ids))
  if (typeof value === 'string') {
    if (UUID_RE.test(value)) {
      if (!ids.has(value)) ids.set(value, `uuid-${ids.size + 1}`)
      return ids.get(value)
    }
    return normalizedString(value, ids)
  }
  if (!value || typeof value !== 'object') return value
  return Object.fromEntries(Object.entries(value).map(([k, v]) => {
    if (/^(?:createdAt|updatedAt|finishedAt)$/.test(k)) return [k, '<dynamic-time>']
    if (k === 'date' || k === 'dayOfWeek') return [k, '<dynamic-time>']
    return [k, normalized(v, ids)]
  }))
}
function equal(a, b) {
  // Fresh per-side id maps: independently initialized fixtures have different
  // UUIDs, and assignment order (not value) defines the placeholder index.
  return JSON.stringify(normalized(a)) === JSON.stringify(normalized(b))
}

async function runSide(kind, work, goBin, outDir) {
  const base = path.join(work, kind)
  const config = path.join(base, 'config'), metadata = path.join(base, 'metadata'), media = path.join(base, 'media')
  const books = path.join(media, 'books')
  for (const d of [config, metadata, books]) fs.mkdirSync(d, { recursive: true })
  const bookDir = path.join(books, 'Persisted Author', 'Persisted Book')
  fs.mkdirSync(bookDir, { recursive: true })
  const mediaFile = path.join(bookDir, 'Persisted Book.m4b')
  const ff = spawnSync('ffmpeg', ['-hide_banner', '-loglevel', 'error', '-f', 'lavfi', '-i', 'anullsrc=r=44100:cl=mono', '-t', '8', '-c:a', 'aac', '-metadata', 'title=Persisted Book', '-metadata', 'artist=Persisted Author', '-y', mediaFile], { encoding: 'utf8' })
  if (ff.status !== 0) throw new Error(`ffmpeg fixture generation failed: ${ff.stderr}`)
  const p = await port(), url = `http://127.0.0.1:${p}`
  const server = kind === 'node'
    ? logged('mise', ['exec', 'node@22', '--', 'node', path.join(ROOT, 'dist-server/index.js'), '--config', config, '--metadata', metadata, '--port', String(p), '--host', '127.0.0.1', '--source', 'docker'], { cwd: ROOT, env: { ...process.env, NODE_ENV: 'production' } }, path.join(outDir, `${kind}.log`))
    : logged(path.resolve(goBin), [], { cwd: ROOT, env: { ...process.env, CONFIG_PATH: config, METADATA_PATH: metadata, HOST: '127.0.0.1', PORT: String(p), ROUTER_BASE_PATH: PREFIX, SOURCE: 'docker' } }, path.join(outDir, `${kind}.log`))
  try {
    await ready(server, url, path.join(outDir, `${kind}.log`))
    await request(url, '/init', { method: 'POST', body: { newRoot: { username: USERNAME, password: PASSWORD } } })
    const login = await request(url, '/login', { method: 'POST', body: { username: USERNAME, password: PASSWORD }, headers: { 'X-Return-Tokens': 'true' } })
    const token = login.user?.accessToken || login.accessToken
    if (!token) throw new Error(`login response lacked access token: ${JSON.stringify(login)}`)
    const library = await request(url, '/api/libraries', { method: 'POST', token, body: { name: 'Persisted Books', mediaType: 'book', provider: 'google', settings: { skipMatchingMediaWithAsin: true, skipMatchingMediaWithIsbn: true }, folders: [{ fullPath: books }] } })
    await request(url, `/api/libraries/${library.id}/scan`, { method: 'POST', token, body: {} })
    let item
    for (let i = 0; i < 90; i++) {
      const listing = await request(url, `/api/libraries/${library.id}/items?limit=20&page=0`, { token })
      item = listing.results?.find(x => x.media?.metadata?.title === 'Persisted Book')
      if (item) break
      await delay(500)
    }
    if (!item) throw new Error(`${kind}: scan did not discover the synthetic book`)
    const db = path.join(config, 'absdatabase.sqlite')
    const steps = []
    const capture = label => steps.push({ label, rows: snapshot(db) })
    capture('initialized-and-item-created')
    const play = await request(url, `/api/items/${item.id}/play`, { method: 'POST', token, body: { mediaPlayer: 'persisted-state-check', forceDirectPlay: true } })
    const sessionId = play.session?.id || play.id
    if (!sessionId) throw new Error(`play response missing session id: ${JSON.stringify(play).slice(0, 400)}`)
    capture('play')
    for (const [i, time] of [1.25, 2.5, 3.75].entries()) {
      await request(url, `/api/me/progress/${item.id}`, { method: 'PATCH', token, body: { duration: 8, currentTime: time, progress: time / 8, isFinished: false } })
      capture(`progress-patch-${i + 1}`)
    }
    await request(url, `/api/session/${sessionId}/sync`, { method: 'POST', token, body: { duration: 8, currentTime: 4.5, timeListened: 5 } })
    capture('session-sync')
    await request(url, `/api/session/${sessionId}/close`, { method: 'POST', token, body: { duration: 8, currentTime: 5, timeListened: 1 } })
    capture('close')
    const reopened = await request(url, `/api/items/${item.id}/play`, { method: 'POST', token, body: { mediaPlayer: 'persisted-state-check', forceDirectPlay: true } })
    const reopenedId = reopened.session?.id || reopened.id
    capture('reopen')
    await request(url, `/api/session/${reopenedId}/close`, { method: 'POST', token, body: { duration: 8, currentTime: 5.5, timeListened: 1 } })
    capture('reclose')
    return { kind, itemId: item.id, steps }
  } finally { await stop(server) }
}

async function main() {
  const a = args()
  const work = path.resolve(a['work-dir'] || fs.mkdtempSync(path.join('/private/var/folders/jt/2prvbq7965ggn35rz2vwnqrw0000gn/T/opencode', 'persisted-state-')))
  const out = path.resolve(a.out || path.join(work, 'persisted-state-evidence.json'))
  fs.mkdirSync(work, { recursive: true })
  const node = await runSide('node', work, a['go-bin'], work)
  const go = await runSide('go', work, a['go-bin'], work)
  const checks = []
  for (let i = 0; i < node.steps.length; i++) {
    const n = node.steps[i], g = go.steps[i]
    for (const table of ['playbackSessions', 'mediaProgresses', 'devices']) {
      checks.push({ name: `${n.label}.${table}.count`, pass: n.rows[table].length === g.rows[table].length, node: n.rows[table].length, go: g.rows[table].length })
      checks.push({ name: `${n.label}.${table}.columns-and-values`, pass: equal(n.rows[table], g.rows[table]), nodeColumns: n.rows[table][0] ? Object.keys(n.rows[table][0]) : null, goColumns: g.rows[table][0] ? Object.keys(g.rows[table][0]) : null })
    }
  }
  for (const side of [node, go]) {
    const patchRows = side.steps.find(s => s.label === 'progress-patch-3').rows.mediaProgresses
    checks.push({ name: `${side.kind}.repeated-progress-upsert-single-row`, pass: patchRows.length === 1, count: patchRows.length })
    checks.push({ name: `${side.kind}.close-retains-persisted-session`, pass: side.steps.find(s => s.label === 'close').rows.playbackSessions.length === 1, count: side.steps.find(s => s.label === 'close').rows.playbackSessions.length })
  }
  const evidence = { baseline: 'edb1f7efc92ecacdef51aafb4d9d5054ce10604f', workDir: work, sourceSemantics: {
    playback: 'PlaybackSessionManager.js:313-363 starts sessions in memory only; :421-431 close saves only if no syncData or syncs then removes from the in-memory sessions list, not the DB row. saveSession at :433-441 skips zero-listening sessions and otherwise creates/updates rows. PlaybackSession.js:209-236 initializes session timestamps and listening time.',
    progress: 'User.js:730-831 locates progress by media item/user, calls applyProgressUpdate for an existing row and creates only if absent; MediaProgress.js:190-249 applies changed values and finish rules.',
    transactions: 'CollectionController.js:69-97 wraps collection plus collectionBooks bulkCreate in a transaction with rollback on failure. Feed.js:171-187, :266-278 and :355-367 similarly wrap feed plus batch episodes; FeedEpisode.js:109 and :206/:243 use bulkCreate updateOnDuplicate.'
  }, node, go, checks, result: checks.every(c => c.pass) ? 'PASS' : 'FAIL', reportedGoDefects: checks.filter(c => !c.pass).map(c => ({ check: c.name, detail: c })) }
  writeJSON(out, evidence)
  console.log(JSON.stringify({ result: evidence.result, evidence: out, checks: checks.map(({ name, pass }) => ({ name, pass })) }, null, 2))
  if (evidence.result !== 'PASS') process.exitCode = 1
}
main().catch(error => { process.stderr.write(`${error.stack || error}\n`); process.exitCode = 1 })
