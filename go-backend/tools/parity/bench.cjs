#!/usr/bin/env node
'use strict'

// Large-library benchmark: pinned upstream (Node) vs the Go server on the SAME
// installation, restored in place from one snapshot (same method as
// replacement-trial.cjs).
//
//  1. Generates a deterministic library (default 2000 books, ~300 authors,
//     ~200 series, ~15% multi-file books). Audio is a few 1-2 second mp3 base
//     files generated once with ffmpeg; every book file gets its own ID3v2.3 tag
//     (title, artist, album, narrator, genre, year, track) prepended in JS, so
//     there is one ffmpeg call per base file, not per book.
//  2. Lets upstream create the library and scan it (scan time is recorded),
//     adds several users and listening history through the client API
//     (play/sync/close and offline local-all: ~100 items with progress and
//     ~500 sessions), then stops upstream and snapshots config+metadata.
//  3. For upstream and then for Go, on the restored snapshot: cold start time
//     to /ping, RSS after one warmup pass, and p50/p95 latency over N runs of
//     the hot client calls. It records status codes and fails when Go answers
//     non-2xx where upstream answers 2xx. Bodies are not compared here
//     (replacement-trial.cjs does that).
//  4. Writes bench-report.json and bench-report.md to the work dir.
//
// Usage: node bench.cjs [--work DIR] [--go-bin PATH] [--books 2000] [--runs 20]
//                       [--reuse 1] [--only upstream|go]
// --reuse 1 skips generation/seeding when DIR already holds a seeded snapshot.
// A call is flagged when Go p95 > 2x upstream p95 or Go p95 > 500 ms.

const fs = require('node:fs')
const os = require('node:os')
const path = require('node:path')
const { spawnSync } = require('node:child_process')
const { setTimeout: delay } = require('node:timers/promises')
const { freePort, spawnLogged, stop } = require('./lib/process.cjs')
const { requestJSON } = require('./lib/http.cjs')
const { parseArgs } = require('./lib/args.cjs')

const ROOT = path.resolve(__dirname, '../../..')
const BASE = '/audiobookshelf'
const ROOT_USER = { username: 'bench-root', password: 'bench-root-password' }
const USERS = ['bench-u1', 'bench-u2', 'bench-u3', 'bench-u4', 'bench-u5'].map(username => ({ username, password: `${username}-password` }))
const DEVICE = { deviceId: 'bench-android-0001', manufacturer: 'Google', model: 'Pixel 8', sdkVersion: 34, clientName: 'Abs Android', clientVersion: '0.9.79-beta' }
const MIME = ['audio/flac', 'audio/mpeg', 'audio/mp4', 'audio/ogg', 'audio/aac', 'audio/webm']
const SLOW_RATIO = 2
const SLOW_MS = 500

function sh(cmd, args, opts = {}) {
  const r = spawnSync(cmd, args, { encoding: 'utf8', ...opts })
  if (r.status !== 0) throw new Error(`${cmd} ${args.join(' ')} failed: ${r.stderr || r.stdout}`)
  return r.stdout
}

// Deterministic PRNG so every run builds the same library.
function mulberry32(seed) {
  let a = seed >>> 0
  return () => {
    a = (a + 0x6D2B79F5) >>> 0
    let t = a
    t = Math.imul(t ^ (t >>> 15), t | 1)
    t ^= t + Math.imul(t ^ (t >>> 7), t | 61)
    return ((t ^ (t >>> 14)) >>> 0) / 4294967296
  }
}

// ---------------------------------------------------------------------------
// Library generation

const FIRST = ['Ada', 'Bram', 'Cora', 'Dmitri', 'Elena', 'Felix', 'Greta', 'Hugo', 'Iris', 'Jonas', 'Kira', 'Leo', 'Mira', 'Noel', 'Olga', 'Pavel', 'Quinn', 'Rosa', 'Soren', 'Tessa', 'Ulric', 'Vera', 'Wren', 'Xavi', 'Yara', 'Zane']
const LAST = ['Archer', 'Blackwood', 'Castellan', 'Dunmore', 'Evergreen', 'Fairweather', 'Galloway', 'Hartley', 'Ingram', 'Jessup', 'Kingsley', 'Lockhart', 'Marlowe', 'Northcott', 'Oakes', 'Pemberton', 'Quill', 'Rothwell', 'Sinclair', 'Thorne', 'Underhill', 'Vance', 'Whitlock', 'Yardley']
const ADJ = ['Silent', 'Broken', 'Hidden', 'Golden', 'Crimson', 'Forgotten', 'Endless', 'Hollow', 'Burning', 'Frozen', 'Wandering', 'Secret', 'Distant', 'Final', 'Last', 'Ancient', 'Quiet', 'Bitter', 'Gentle', 'Wild']
const NOUN = ['Crown', 'River', 'Garden', 'Empire', 'Lantern', 'Harbor', 'Mirror', 'Citadel', 'Orchard', 'Compass', 'Archive', 'Horizon', 'Cathedral', 'Meridian', 'Tide', 'Ember', 'Labyrinth', 'Voyage', 'Oracle', 'Fortress']
const PLACE = ['of Ash', 'of Glass', 'of the North', 'of Thorns', 'of Salt', 'in Winter', 'at Midnight', 'of the Deep', 'of Stone', 'of Dawn']
const GENRES = ['Fantasy', 'Science Fiction', 'Mystery', 'Thriller', 'Romance', 'Historical Fiction', 'Biography', 'Horror', 'Self-Help', 'Adventure']

function id3Frame(id, text) {
  const body = Buffer.concat([Buffer.from([0]), Buffer.from(text, 'latin1')])
  const header = Buffer.alloc(10)
  header.write(id, 0, 'latin1')
  header.writeUInt32BE(body.length, 4)
  return Buffer.concat([header, body])
}

function id3Tag(fields) {
  const frames = Buffer.concat(Object.entries(fields).map(([id, text]) => id3Frame(id, text)))
  const header = Buffer.alloc(10)
  header.write('ID3', 0, 'latin1')
  header[3] = 3
  const size = frames.length
  header[6] = (size >> 21) & 0x7f
  header[7] = (size >> 14) & 0x7f
  header[8] = (size >> 7) & 0x7f
  header[9] = size & 0x7f
  return Buffer.concat([header, frames])
}

function generateLibrary(mediaRoot, bookCount) {
  const rand = mulberry32(20261102)
  const pick = arr => arr[Math.floor(rand() * arr.length)]
  const scratch = path.join(mediaRoot, '..', 'gen')
  fs.mkdirSync(scratch, { recursive: true })
  const base = []
  for (let i = 0; i < 3; i++) {
    const file = path.join(scratch, `base-${i}.mp3`)
    sh('ffmpeg', ['-hide_banner', '-loglevel', 'error', '-y', '-f', 'lavfi', '-i', `sine=frequency=${300 + i * 150}:duration=${1 + i * 0.5}`,
      '-ac', '1', '-ar', '22050', '-b:a', '32k', '-write_id3v2', '0', '-write_xing', '0', file])
    base.push(fs.readFileSync(file))
  }
  const authorCount = Math.min(300, Math.max(10, Math.round(bookCount * 0.15)))
  const seriesCount = Math.min(200, Math.max(5, Math.round(bookCount * 0.1)))
  const authors = []
  for (let i = 0; authors.length < authorCount; i++) {
    const name = `${FIRST[i % FIRST.length]} ${LAST[Math.floor(i / FIRST.length) % LAST.length]}${i >= FIRST.length * LAST.length ? ` ${i}` : ''}`
    authors.push(name)
  }
  const narrators = Array.from({ length: 80 }, (_, i) => `${FIRST[(i * 7) % FIRST.length]} ${LAST[(i * 5 + 3) % LAST.length]} Reader`)
  const series = []
  for (let i = 0; series.length < seriesCount; i++) {
    series.push({ name: `The ${ADJ[i % ADJ.length]} ${NOUN[Math.floor(i / ADJ.length) % NOUN.length]} Saga${i >= ADJ.length * NOUN.length ? ` ${i}` : ''}`, author: pick(authors), next: 1 })
  }
  const audiobooks = path.join(mediaRoot, 'audiobooks')
  let files = 0
  let multi = 0
  const seenTitles = new Set()
  for (let b = 0; b < bookCount; b++) {
    const inSeries = b % 2 === 0
    const ser = inSeries ? series[Math.floor(b / 2) % series.length] : null
    const author = ser ? ser.author : pick(authors)
    let title
    do { title = `${pick(ADJ)} ${pick(NOUN)} ${pick(PLACE)} ${b}` } while (seenTitles.has(title))
    seenTitles.add(title)
    const seq = ser ? ser.next++ : 0
    const folder = ser
      ? path.join(audiobooks, author, ser.name, `Book ${seq} - ${title}`)
      : path.join(audiobooks, author, title)
    fs.mkdirSync(folder, { recursive: true })
    const parts = rand() < 0.15 ? 2 + Math.floor(rand() * 3) : 1
    if (parts > 1) multi++
    const narrator = pick(narrators)
    const genre = pick(GENRES)
    const year = String(1990 + Math.floor(rand() * 35))
    for (let p = 1; p <= parts; p++) {
      const tag = id3Tag({ TIT2: parts > 1 ? `${title} Part ${p}` : title, TPE1: author, TALB: title, TCOM: narrator, TCON: genre, TYER: year, TRCK: `${p}/${parts}` })
      const name = parts > 1 ? `0${p} - Part ${p}.mp3` : `${title}.mp3`
      fs.writeFileSync(path.join(folder, name), Buffer.concat([tag, base[(b + p) % base.length]]))
      files++
    }
  }
  return { audiobooks, books: bookCount, files, multiFileBooks: multi, authors: authorCount, series: seriesCount }
}

// ---------------------------------------------------------------------------
// Servers

async function startServer(kind, goBin, dirs, logPath) {
  const port = await freePort()
  const t0 = performance.now()
  const server = kind === 'go'
    ? spawnLogged(goBin, [], { cwd: ROOT, env: { ...process.env, CONFIG_PATH: dirs.config, METADATA_PATH: dirs.metadata, HOST: '127.0.0.1', PORT: String(port), SOURCE: 'docker', ABS_GO_CLIENT_PATH: path.join(ROOT, 'client', 'dist') } }, logPath)
    : spawnLogged(process.execPath, [path.join(ROOT, 'dist-server/index.js'), '--config', dirs.config, '--metadata', dirs.metadata, '--port', String(port), '--host', '127.0.0.1', '--source', 'docker'],
      { cwd: ROOT, env: { ...process.env, NODE_ENV: 'production' } }, logPath)
  const baseUrl = `http://127.0.0.1:${port}`
  for (let i = 0; i < 12000; i++) {
    if (server.child.exitCode !== null) throw new Error(`${kind} exited before readiness; see ${logPath}`)
    try {
      if ((await fetch(`${baseUrl}${BASE}/ping`)).ok) return { ...server, kind, baseUrl, startMs: performance.now() - t0, pid: server.child.pid }
    } catch {}
    await delay(5)
  }
  throw new Error(`${kind} did not become ready; see ${logPath}`)
}

function rssMB(pid) {
  try {
    const status = fs.readFileSync(`/proc/${pid}/status`, 'utf8')
    const rss = Number(/VmRSS:\s+(\d+) kB/.exec(status)?.[1])
    const hwm = Number(/VmHWM:\s+(\d+) kB/.exec(status)?.[1])
    return { rssMB: +(rss / 1024).toFixed(1), peakMB: +(hwm / 1024).toFixed(1) }
  } catch { return { rssMB: null, peakMB: null } }
}

const call = server => (route, opts = {}) => requestJSON(server.baseUrl, route, { timeoutMs: 120000, ...opts })

async function must(promise, label) {
  const r = await promise
  if (!r.ok) throw new Error(`${label} -> ${r.status}: ${JSON.stringify(r.value).slice(0, 400)}`)
  return r.value
}

async function loginAs(c, creds) {
  return (await must(c(`${BASE}/login`, { method: 'POST', body: creds, headers: { 'X-Return-Tokens': 'true' } }), `login ${creds.username}`)).user
}

// ---------------------------------------------------------------------------
// Seeding

async function allItems(c, token, libraryId) {
  const out = []
  for (let page = 0; ; page++) {
    const r = await must(c(`/api/libraries/${libraryId}/items?limit=200&page=${page}&minified=1&sort=media.metadata.title`, { token }), 'items page')
    out.push(...r.results.map(i => ({ id: i.id, title: i.media.metadata.title, author: i.media.metadata.authorName })))
    if (out.length >= r.total || !r.results.length) return out
  }
}

async function seed(work, expectedBooks, sizes) {
  const dirs = { config: path.join(work, 'install', 'config'), metadata: path.join(work, 'install', 'metadata') }
  fs.mkdirSync(dirs.config, { recursive: true })
  fs.mkdirSync(dirs.metadata, { recursive: true })
  const server = await startServer('upstream', null, dirs, path.join(work, 'seed-upstream.log'))
  const c = call(server)
  const ids = { sizes }
  try {
    await must(c(`${BASE}/init`, { method: 'POST', body: { newRoot: ROOT_USER } }), 'init')
    const rt = (await loginAs(c, ROOT_USER)).accessToken
    const library = await must(c('/api/libraries', { method: 'POST', token: rt, body: { name: 'Bench', mediaType: 'book', provider: 'google', folders: [{ fullPath: path.join(work, 'media', 'audiobooks') }], settings: { skipMatchingMediaWithAsin: true, skipMatchingMediaWithIsbn: true } } }), 'create library')
    ids.library = library.id
    const scanStart = performance.now()
    await must(c(`/api/libraries/${library.id}/scan`, { method: 'POST', token: rt, body: {} }), 'scan')
    let total = 0
    let lastLog = 0
    for (let stable = 0; stable < 3;) {
      await delay(1000)
      const r = await must(c(`/api/libraries/${library.id}/items?limit=1`, { token: rt }), 'items')
      if (r.total === expectedBooks) stable++
      else stable = 0
      total = r.total
      if (performance.now() - lastLog > 15000) { lastLog = performance.now(); console.log(`  scan: ${total}/${expectedBooks} after ${((performance.now() - scanStart) / 1000).toFixed(0)}s`) }
      if ((performance.now() - scanStart) > 45 * 60 * 1000) throw new Error(`scan did not finish: ${total}/${expectedBooks}`)
    }
    // The count stayed at the target for 3 polls; the first poll that reached it ended the scan.
    ids.upstreamScanSeconds = +((performance.now() - scanStart - 3000) / 1000).toFixed(1)
    console.log(`  upstream scan: ${total} items in ${ids.upstreamScanSeconds}s`)

    const items = await allItems(c, rt, library.id)
    const users = []
    for (const u of USERS) {
      await must(c('/api/users', { method: 'POST', token: rt, body: { ...u, type: 'user', isActive: true, permissions: { download: true, update: false, delete: false, upload: false, accessAllLibraries: true, accessAllTags: true, accessExplicitContent: true } } }), `create ${u.username}`)
      const user = await loginAs(c, u)
      users.push({ ...u, id: user.id, token: user.accessToken })
    }
    ids.users = users.map(({ username, password, id }) => ({ username, password, id }))

    // ~500 sessions over ~100 items: half through play/sync/close, half as
    // offline local-all uploads backdated over two months.
    const rand = mulberry32(7)
    const pool = items.slice(0, Math.min(items.length, 110))
    const weights = [200, 120, 80, 60, 40]
    const sessionPlan = []
    users.forEach((u, ui) => { for (let i = 0; i < weights[ui]; i++) sessionPlan.push({ user: u, item: pool[Math.floor(rand() * pool.length)], local: i % 2 === 1 }) })
    let apiSessions = 0
    const localByUser = new Map()
    for (const s of sessionPlan) {
      if (s.local) {
        const list = localByUser.get(s.user.id) || []
        list.push(s)
        localByUser.set(s.user.id, list)
        continue
      }
      const play = await must(c(`/api/items/${s.item.id}/play`, { method: 'POST', token: s.user.token, body: { deviceInfo: DEVICE, mediaPlayer: 'exo-player', forceDirectPlay: true, forceTranscode: false, supportedMimeTypes: MIME } }), 'seed play')
      const t = Math.floor(rand() * Math.max(1, play.duration - 1))
      await must(c(`/api/session/${play.id}/sync`, { method: 'POST', token: s.user.token, body: { currentTime: t, timeListened: 1, duration: play.duration } }), 'seed sync')
      await must(c(`/api/session/${play.id}/close`, { method: 'POST', token: s.user.token, body: { currentTime: t, timeListened: 0, duration: play.duration } }), 'seed close')
      apiSessions++
    }
    let localSessions = 0
    const now = Date.now()
    for (const [userId, list] of localByUser) {
      const token = users.find(u => u.id === userId).token
      for (let off = 0; off < list.length; off += 50) {
        const sessions = list.slice(off, off + 50).map((s, i) => {
          const startedAt = now - Math.floor(rand() * 60 * 86400000) - 3600000
          const date = new Date(startedAt)
          return { id: `play_local_bench_${userId.slice(0, 8)}_${off + i}`, userId, libraryId: library.id, libraryItemId: s.item.id, mediaType: 'book', mediaMetadata: {}, chapters: [],
            displayTitle: s.item.title, displayAuthor: s.item.author || '', coverPath: null, duration: 1.5, playMethod: 3, mediaPlayer: 'AVPlayer', deviceInfo: { ...DEVICE, deviceId: 'bench-ios-0001' },
            serverVersion: '2.37.2', date: date.toISOString().slice(0, 10), dayOfWeek: date.toLocaleDateString('en-US', { weekday: 'long', timeZone: 'UTC' }), timeListening: 1, startTime: 0, currentTime: 1, startedAt, updatedAt: startedAt + 1000 }
        })
        await must(c('/api/session/local-all', { method: 'POST', token, body: { deviceInfo: DEVICE, sessions } }), 'local-all')
        localSessions += sessions.length
      }
    }
    // Finished flags on some of user 1's items.
    for (const item of pool.slice(0, 20)) await must(c(`/api/me/progress/${item.id}`, { method: 'PATCH', token: users[0].token, body: { isFinished: true } }), 'finish')
    ids.apiSessions = apiSessions
    ids.localSessions = localSessions

    const me = await must(c('/api/me', { token: users[0].token }), 'me')
    ids.progressItems = new Set(me.mediaProgress.map(p => p.libraryItemId)).size
    const fd = await must(c(`/api/libraries/${library.id}?include=filterdata`, { token: users[0].token }), 'filterdata')
    ids.authorId = fd.filterdata.authors[0].id
    ids.seriesId = fd.filterdata.series[0].id
    ids.genre = fd.filterdata.genres[0]
    ids.searchTerm = fd.filterdata.series[0].name.split(' ')[1]
    ids.items = { hot: pool[0].id, other: pool[1].id, play: items.find(i => i.id !== pool[0].id).id }
    ids.counts = { items: items.length, authors: fd.filterdata.authors.length, series: fd.filterdata.series.length, genres: fd.filterdata.genres.length }
    console.log(`  seeded: ${JSON.stringify(ids.counts)} ${apiSessions} api + ${localSessions} local sessions, ${ids.progressItems} items with progress (user1)`)
    return { dirs, ids }
  } finally {
    await stop(server)
  }
}

// ---------------------------------------------------------------------------
// Benchmark

const b64 = s => encodeURIComponent(Buffer.from(s).toString('base64'))

function callList(ids) {
  const L = `/api/libraries/${ids.library}`
  const calls = []
  const get = (id, who, route) => calls.push({ id, who, route })
  get('me', 'u1', '/api/me')
  get('libraries', 'u1', '/api/libraries')
  get('library.filterdata', 'u1', `${L}?include=filterdata`)
  get('personalized', 'u1', `${L}/personalized?minified=1&include=rssfeed,numEpisodesIncomplete,share`)
  get('items.title', 'u1', `${L}/items?limit=50&page=0&minified=1&sort=media.metadata.title&desc=0&include=rssfeed,numEpisodesIncomplete`)
  get('items.title.full', 'u1', `${L}/items?limit=50&page=0&sort=media.metadata.title&desc=0`)
  get('items.addedAt', 'u1', `${L}/items?limit=50&page=0&minified=1&sort=addedAt&desc=1`)
  get('items.duration', 'u1', `${L}/items?limit=50&page=0&minified=1&sort=media.duration&desc=1`)
  get('items.progress', 'u1', `${L}/items?limit=50&page=0&minified=1&sort=progress&desc=1`)
  get('items.collapseseries', 'u1', `${L}/items?limit=50&page=0&minified=1&sort=media.metadata.title&collapseseries=1`)
  get('items.collapseseries.p5', 'u1', `${L}/items?limit=50&page=5&minified=1&sort=media.metadata.title&collapseseries=1`)
  get('items.filter.inprogress', 'u1', `${L}/items?limit=50&minified=1&filter=progress.${b64('in-progress')}`)
  get('items.filter.finished', 'u1', `${L}/items?limit=50&minified=1&filter=progress.${b64('finished')}`)
  get('items.filter.notstarted', 'u1', `${L}/items?limit=50&minified=1&filter=progress.${b64('not-started')}`)
  get('items.filter.author', 'u1', `${L}/items?limit=50&minified=1&filter=authors.${b64(ids.authorId)}`)
  get('items.filter.series', 'u1', `${L}/items?limit=50&minified=1&filter=series.${b64(ids.seriesId)}&sort=sequence`)
  get('items.filter.genre', 'u1', `${L}/items?limit=50&minified=1&filter=genres.${b64(ids.genre)}`)
  get('series', 'u1', `${L}/series?limit=50&page=0&sort=name&desc=0&minified=1`)
  get('series.size', 'u1', `${L}/series?limit=50&page=0&sort=numBooks&desc=1&minified=1`)
  get('authors', 'u1', `${L}/authors`)
  get('narrators', 'u1', `${L}/narrators`)
  get('search', 'u1', `${L}/search?q=${encodeURIComponent(ids.searchTerm)}&limit=12`)
  get('item.expanded', 'u1', `/api/items/${ids.items.hot}?expanded=1&include=progress,rssfeed,authors,downloads,share`)
  get('items-in-progress', 'u1', '/api/me/items-in-progress')
  get('listening-stats', 'u1', '/api/me/listening-stats')
  get('listening-sessions', 'u1', '/api/me/listening-sessions?itemsPerPage=10&page=0')
  get('sessions.admin', 'root', '/api/sessions?itemsPerPage=10&page=0&sort=updatedAt&desc=1')
  return calls
}

function percentile(sorted, p) {
  if (!sorted.length) return null
  const i = Math.min(sorted.length - 1, Math.ceil(p * sorted.length) - 1)
  return sorted[Math.max(0, i)]
}

function stats(samples) {
  const sorted = [...samples].sort((a, b) => a - b)
  return { p50: +percentile(sorted, 0.5).toFixed(2), p95: +percentile(sorted, 0.95).toFixed(2), min: +sorted[0].toFixed(2), max: +sorted[sorted.length - 1].toFixed(2) }
}

async function timed(fn) {
  const t0 = performance.now()
  const r = await fn()
  return { ms: performance.now() - t0, r }
}

async function bench(kind, goBin, work, dirs, ids, runs) {
  fs.rmSync(path.join(work, 'install'), { recursive: true, force: true })
  fs.cpSync(path.join(work, 'snapshot'), path.join(work, 'install'), { recursive: true, preserveTimestamps: true })
  const server = await startServer(kind, goBin, dirs, path.join(work, `bench-${kind}.log`))
  const c = call(server)
  const out = { kind, coldStartMs: +server.startMs.toFixed(1), rssAfterStart: rssMB(server.pid), calls: {} }
  try {
    const tokens = {}
    const first = await timed(() => c(`${BASE}/login`, { method: 'POST', body: ids.users[0], headers: { 'X-Return-Tokens': 'true' } }))
    out.firstLoginMs = +first.ms.toFixed(1)
    tokens.u1 = first.r.value.user.accessToken
    tokens.root = (await loginAs(c, ROOT_USER)).accessToken
    const calls = callList(ids)
    // Warmup pass (also the cold-ish first call per route).
    for (const spec of calls) {
      const { ms, r } = await timed(() => c(spec.route, { token: tokens[spec.who] }))
      out.calls[spec.id] = { route: spec.route, firstMs: +ms.toFixed(1), status: r.status, bytes: r.bytes.length }
    }
    const playBody = { deviceInfo: DEVICE, mediaPlayer: 'exo-player', forceDirectPlay: true, forceTranscode: false, supportedMimeTypes: MIME }
    const loginSpec = async () => c(`${BASE}/login`, { method: 'POST', body: ids.users[0], headers: { 'X-Return-Tokens': 'true' } })
    const playClose = async () => {
      const p = await c(`/api/items/${ids.items.play}/play`, { method: 'POST', token: tokens.u1, body: playBody })
      if (!p.ok) return p
      const cl = await c(`/api/session/${p.value.id}/close`, { method: 'POST', token: tokens.u1, body: { currentTime: 0.5, timeListened: 0.5, duration: p.value.duration } })
      return cl
    }
    const composite = { login: loginSpec, 'play+close': playClose }
    for (const [id, fn] of Object.entries(composite)) {
      const { ms, r } = await timed(fn)
      out.calls[id] = { route: id === 'login' ? 'POST /login' : 'POST /api/items/:id/play + POST /api/session/:id/close', firstMs: +ms.toFixed(1), status: r.status }
    }
    out.rssAfterWarmup = rssMB(server.pid)
    // Measured runs, interleaved round-robin so drift hits every call equally.
    const samples = Object.fromEntries([...calls.map(x => x.id), ...Object.keys(composite)].map(id => [id, []]))
    const statuses = Object.fromEntries(Object.keys(samples).map(id => [id, new Set([out.calls[id].status])]))
    for (let run = 0; run < runs; run++) {
      for (const spec of calls) {
        const { ms, r } = await timed(() => c(spec.route, { token: tokens[spec.who] }))
        samples[spec.id].push(ms)
        statuses[spec.id].add(r.status)
      }
      for (const [id, fn] of Object.entries(composite)) {
        const { ms, r } = await timed(fn)
        samples[id].push(ms)
        statuses[id].add(r.status)
      }
    }
    for (const id of Object.keys(samples)) Object.assign(out.calls[id], stats(samples[id]), { statuses: [...statuses[id]].sort() })
    out.rssAfterLoad = rssMB(server.pid)
  } finally {
    await stop(server)
  }
  return out
}

const is2xx = s => s >= 200 && s < 300

function buildReport(work, ids, results, runs) {
  const up = results.upstream
  const go = results.go
  const rows = []
  const failures = []
  const flags = []
  const idsAll = Object.keys((up || go).calls)
  for (const id of idsAll) {
    const u = up?.calls[id]
    const g = go?.calls[id]
    const row = { id, route: (u || g).route, upstream: u && { status: u.statuses, p50: u.p50, p95: u.p95, first: u.firstMs, bytes: u.bytes }, go: g && { status: g.statuses, p50: g.p50, p95: g.p95, first: g.firstMs, bytes: g.bytes } }
    if (u && g) {
      row.ratioP95 = +(g.p95 / Math.max(u.p95, 0.01)).toFixed(2)
      if (u.statuses.every(is2xx) && !g.statuses.every(is2xx)) failures.push(`${id}: upstream ${u.statuses} but Go ${g.statuses}`)
      if (g.p95 > SLOW_MS || g.p95 > SLOW_RATIO * u.p95) { row.flag = true; flags.push(id) }
    } else if (g && !g.statuses.every(is2xx)) failures.push(`${id}: Go ${g.statuses}`)
    rows.push(row)
  }
  const report = {
    createdAt: new Date().toISOString(), host: { cpus: os.cpus().length, node: process.version, platform: `${os.platform()} ${os.release()}` }, runs,
    library: ids.sizes, counts: ids.counts, history: { apiSessions: ids.apiSessions, localSessions: ids.localSessions, itemsWithProgressUser1: ids.progressItems },
    upstreamScanSeconds: ids.upstreamScanSeconds,
    startup: Object.fromEntries(Object.entries(results).map(([k, r]) => [k, { coldStartMs: r.coldStartMs, rssAfterStart: r.rssAfterStart, rssAfterWarmup: r.rssAfterWarmup, rssAfterLoad: r.rssAfterLoad, firstLoginMs: r.firstLoginMs }])),
    rows, flagged: flags, failures
  }
  const f = v => v == null ? '-' : v < 10 ? v.toFixed(1) : Math.round(v)
  const md = []
  md.push('# Large-library benchmark', '')
  md.push(`Library: ${ids.sizes.books} books (${ids.sizes.files} files, ${ids.sizes.multiFileBooks} multi-file), ${ids.counts.authors} authors, ${ids.counts.series} series. History: ${ids.apiSessions + ids.localSessions} sessions, ${ids.progressItems} items with progress for user 1. Upstream scan: ${ids.upstreamScanSeconds}s. ${runs} runs per call, ms.`, '')
  md.push('| | upstream | Go |', '|---|---|---|')
  const sv = (k, fn) => `| ${k} | ${up ? fn(up) : '-'} | ${go ? fn(go) : '-'} |`
  md.push(sv('cold start to /ping (ms)', r => Math.round(r.coldStartMs)))
  md.push(sv('RSS after start (MB)', r => r.rssAfterStart.rssMB))
  md.push(sv('RSS after warmup (MB)', r => r.rssAfterWarmup.rssMB))
  md.push(sv('RSS after load (MB)', r => r.rssAfterLoad.rssMB))
  md.push('', '| call | status (up/go) | upstream p50 | upstream p95 | Go p50 | Go p95 | p95 ratio | |', '|---|---|---|---|---|---|---|---|')
  for (const r of rows) {
    md.push(`| ${r.id} | ${r.upstream?.status.join(',') ?? '-'} / ${r.go?.status.join(',') ?? '-'} | ${f(r.upstream?.p50)} | ${f(r.upstream?.p95)} | ${f(r.go?.p50)} | ${f(r.go?.p95)} | ${r.ratioP95 ?? '-'} | ${r.flag ? 'SLOW' : ''} |`)
  }
  md.push('', failures.length ? `Failures: ${failures.join('; ')}` : 'No Go non-2xx where upstream is 2xx.', `Flagged (Go p95 > ${SLOW_RATIO}x upstream or > ${SLOW_MS} ms): ${flags.length ? flags.join(', ') : 'none'}`, '')
  fs.writeFileSync(path.join(work, 'bench-report.json'), JSON.stringify(report, null, 2))
  fs.writeFileSync(path.join(work, 'bench-report.md'), md.join('\n'))
  return { report, markdown: md.join('\n') }
}

async function main() {
  const args = parseArgs()
  const work = path.resolve(args.work || fs.mkdtempSync(path.join(os.tmpdir(), 'abs-bench-')))
  const bookCount = Number(args.books || 2000)
  const runs = Number(args.runs || 20)
  const goBin = path.resolve(args['go-bin'] || path.join(ROOT, 'go-backend', 'audiobookshelf-go'))
  fs.mkdirSync(work, { recursive: true })
  console.log(`work dir: ${work}`)
  if (args.only !== 'upstream' && !args['go-bin']) sh('go', ['build', '-o', goBin, './cmd/audiobookshelf-go'], { cwd: path.join(ROOT, 'go-backend'), env: { ...process.env, CGO_ENABLED: '0' } })

  let dirs, ids
  const idsFile = path.join(work, 'ids.json')
  if (args.reuse && fs.existsSync(idsFile)) {
    ids = JSON.parse(fs.readFileSync(idsFile, 'utf8'))
    dirs = { config: path.join(work, 'install', 'config'), metadata: path.join(work, 'install', 'metadata') }
  } else {
    fs.rmSync(path.join(work, 'media'), { recursive: true, force: true })
    fs.rmSync(path.join(work, 'install'), { recursive: true, force: true })
    console.log(`generating ${bookCount} books`)
    const sizes = generateLibrary(path.join(work, 'media'), bookCount)
    console.log(`  ${JSON.stringify(sizes)}`)
    ;({ dirs, ids } = await seed(work, bookCount, sizes))
    fs.rmSync(path.join(work, 'snapshot'), { recursive: true, force: true })
    fs.cpSync(path.join(work, 'install'), path.join(work, 'snapshot'), { recursive: true, preserveTimestamps: true })
    fs.writeFileSync(idsFile, JSON.stringify(ids, null, 2))
  }

  const results = {}
  for (const kind of ['upstream', 'go']) {
    if (args.only && args.only !== kind) continue
    console.log(`benchmarking ${kind}`)
    results[kind] = await bench(kind, goBin, work, dirs, ids, runs)
    console.log(`  cold start ${results[kind].coldStartMs} ms, RSS after warmup ${results[kind].rssAfterWarmup.rssMB} MB`)
  }
  const { report, markdown } = buildReport(work, ids, results, runs)
  console.log(markdown)
  console.log(`report: ${path.join(work, 'bench-report.json')} and bench-report.md`)
  if (report.failures.length) {
    console.error(`FAIL: ${report.failures.join('; ')}`)
    process.exitCode = 1
  }
}

main().catch(error => { console.error(error.stack || error); process.exitCode = 1 })
