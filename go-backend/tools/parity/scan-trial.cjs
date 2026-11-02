#!/usr/bin/env node
'use strict'

// Scan trial: after swapping an existing upstream installation to the Go
// server, do NEW CONTENT and LIBRARY MAINTENANCE behave like upstream?
//
// Method (same in-place method as replacement-trial.cjs):
//  1. pinned upstream seeds an install (book library with 3 books, a root user,
//     a listener with progress); config+metadata and the media tree are
//     snapshotted;
//  2. a scripted scenario mutates the media tree and drives scans, maintenance
//     and metadata edits while a connected root Socket.IO client records every
//     realtime event; it runs against upstream, then the config/metadata
//     snapshot and a pristine copy of the media tree are restored to the SAME
//     paths and the identical scenario runs against Go;
//  3. every step's HTTP responses and realtime events (names, payloads, order)
//     are normalized and diffed.
//
// Normalization (every rule has a reason, nothing else is normalized):
//  - UUIDs that do not exist in the seeded install are replaced by a role label
//    (item by relPath, author/series by name) so id consistency across
//    responses and events is still checked; unlabeled new ids (task ids)
//    become <new-uuid>. Reason: ids are generated at runtime.
//  - `ino` fields -> <ino>. Reason: the media tree is restored from a pristine
//    copy before each run, so inode numbers differ between runs by design.
//    Item matching by inode is still exercised by the rename step.
//  - birthtimeMs / ctimeMs -> <ts>. Reason: the kernel sets them at copy time;
//    mtimes are pinned by the fixture and compared exactly.
//  - addedAt/updatedAt/createdAt/lastScan/startedAt/finishedAt/... and any
//    epoch-ms or ISO value near the current time -> <ts>. Reason: wall clock.
//  - scanResults.elapsed / the "(N ms)" suffix of scanResults.text. Reason:
//    scan duration is wall clock.
//  - JWTs and the work directory prefix. Reason: per-run tokens and tmp dir.
//  - discover shelf order. Reason: ORDER BY RANDOM() upstream.
//  - library stats authorsWithCount: ties are ordered by name. Reason: the
//    order among authors with equal counts is whatever SQLite's GROUP BY plan
//    yields after the server's boot-time ANALYZE; it differed between fresh
//    upstream seeds and is not specified.
//  - the seeded listener progress is created in ascending media id order.
//    Reason: user.mediaProgress order also follows that plan; creation order
//    equal to index order makes both agree (a fixture choice, not a rewrite).
//
// The library watcher is disabled for the explicit-scan steps (so the watcher
// cannot scan mid-step at an arbitrary moment) and enabled again for the final
// watcher steps, which compare the final state after a bounded wait.
//
// Usage: node scan-trial.cjs [--work DIR] [--go-bin PATH] [--reuse 1]
// --reuse 1 skips media generation and seeding when DIR holds a seeded snapshot.

const fs = require('node:fs')
const os = require('node:os')
const path = require('node:path')
const { setTimeout: delay } = require('node:timers/promises')
const { freePort, spawnLogged, waitReady, stop } = require('./lib/process.cjs')
const { requestJSON } = require('./lib/http.cjs')
const { parseArgs } = require('./lib/args.cjs')
const { loadSocketIOClient } = require('./lib/socket-probes.cjs')
const media = require('./lib/trial-media.cjs')

const ROOT = path.resolve(__dirname, '../../..')
const BASE = '/audiobookshelf'
const ROOT_USER = { username: 'scan-root', password: 'scan-root-password' }
const LISTENER = { username: 'scan-listener', password: 'scan-listener-password' }
const ANDROID_DEVICE = {
  deviceId: 'scan-android-0001', manufacturer: 'Google', model: 'Pixel 8', sdkVersion: 34,
  clientName: 'Abs Android', clientVersion: '0.9.79-beta'
}
const MOBILE_MIME = ['audio/flac', 'audio/mpeg', 'audio/mp4', 'audio/ogg', 'audio/aac', 'audio/webm']
const SCAN_TIMEOUT_MS = 90000
const WATCHER_TIMEOUT_MS = 90000

// ---------------------------------------------------------------------------
// Servers

async function startUpstream(dirs, port, logPath) {
  const server = spawnLogged(process.execPath, [
    path.join(ROOT, 'dist-server/index.js'), '--config', dirs.config, '--metadata', dirs.metadata,
    '--port', String(port), '--host', '127.0.0.1', '--source', 'docker'
  ], { cwd: ROOT, env: { ...process.env, NODE_ENV: 'production' } }, logPath)
  const baseUrl = `http://127.0.0.1:${port}`
  await waitReady(server, baseUrl, { pathPrefix: BASE, logPath, attempts: 240 })
  return { ...server, baseUrl, kind: 'upstream', logPath }
}

async function startGo(goBin, dirs, port, logPath) {
  const server = spawnLogged(goBin, [], {
    cwd: ROOT,
    env: {
      ...process.env, CONFIG_PATH: dirs.config, METADATA_PATH: dirs.metadata, HOST: '127.0.0.1',
      PORT: String(port), SOURCE: 'docker', ABS_GO_CLIENT_PATH: path.join(ROOT, 'client', 'dist')
    }
  }, logPath)
  const baseUrl = `http://127.0.0.1:${port}`
  await waitReady(server, baseUrl, { pathPrefix: BASE, logPath, attempts: 240 })
  return { ...server, baseUrl, kind: 'go', logPath }
}

function api(server) {
  return async (route, opts = {}) => requestJSON(server.baseUrl, route, { timeoutMs: 60000, ...opts })
}

async function must(promise, label) {
  const r = await promise
  if (!r.ok) throw new Error(`${label} -> ${r.status}: ${JSON.stringify(r.value).slice(0, 400)}`)
  return r.value
}

async function loginAs(call, creds) {
  const v = await must(call('/login', { method: 'POST', body: creds, headers: { 'X-Return-Tokens': 'true' } }), `login ${creds.username}`)
  return v.user
}

// ---------------------------------------------------------------------------
// Seeding: upstream creates the installation.

async function seed(work, booksRoot) {
  const dirs = { config: path.join(work, 'install', 'config'), metadata: path.join(work, 'install', 'metadata') }
  fs.mkdirSync(dirs.config, { recursive: true })
  fs.mkdirSync(dirs.metadata, { recursive: true })
  const server = await startUpstream(dirs, await freePort(), path.join(work, 'seed-upstream.log'))
  const call = api(server)
  try {
    await must(call(`${BASE}/init`, { method: 'POST', body: { newRoot: ROOT_USER } }), 'init')
    const root = await loginAs(call, ROOT_USER)
    const rt = root.accessToken
    const library = await must(call('/api/libraries', {
      method: 'POST', token: rt,
      body: { name: 'Audiobooks', mediaType: 'book', provider: 'google', folders: [{ fullPath: booksRoot }],
        settings: { skipMatchingMediaWithAsin: true, skipMatchingMediaWithIsbn: true } }
    }), 'create library')
    await must(call(`/api/libraries/${library.id}/scan`, { method: 'POST', token: rt, body: {} }), 'scan')
    let items = []
    for (let i = 0; i < 120 && items.length < 3; i++) {
      await delay(500)
      items = (await must(call(`/api/libraries/${library.id}/items?limit=50`, { token: rt }), 'items')).results
    }
    if (items.length !== 3) throw new Error(`upstream scan produced ${items.length} items, expected 3`)
    const byTitle = Object.fromEntries(items.map(i => [i.media.metadata.title, i.id]))
    const ids = {
      library: library.id,
      saga1: byTitle['The Beginning'] || byTitle['Book 1 - The Beginning'],
      saga2: byTitle['The Middle'] || byTitle['Book 2 - The Middle'],
      novel: byTitle['Single File Novel']
    }
    for (const [k, v] of Object.entries(ids)) if (!v) throw new Error(`seed: no item for ${k}; titles=${Object.keys(byTitle)}`)

    await must(call('/api/users', { method: 'POST', token: rt, body: { ...LISTENER, type: 'user', isActive: true,
      permissions: { download: true, update: false, delete: false, upload: false, accessAllLibraries: true, accessAllTags: true, accessExplicitContent: true } } }), 'create listener')
    const listener = await loginAs(call, LISTENER)
    const lt = listener.accessToken
    ids.listener = listener.id

    // user.mediaProgress is listed in an order that depends on SQLite's plan
    // for the pinned build (the server runs ANALYZE at boot), which differs from
    // Go's engine for some row shapes. Progress rows are therefore created in
    // ascending media id order, where row-insertion order and index order agree.
    const mediaId = Object.fromEntries(items.map(i => [i.id, i.media.id]))
    const seedProgress = {
      [ids.saga1]: async () => {
        const s = await must(call(`/api/items/${ids.saga1}/play`, { method: 'POST', token: lt, body: {
          deviceInfo: ANDROID_DEVICE, mediaPlayer: 'exo-player', forceDirectPlay: true, forceTranscode: false, supportedMimeTypes: MOBILE_MIME } }), 'seed play')
        await must(call(`/api/session/${s.id}/sync`, { method: 'POST', token: lt, body: { currentTime: 12, timeListened: 12, duration: s.duration } }), 'seed sync')
        await must(call(`/api/session/${s.id}/close`, { method: 'POST', token: lt, body: { currentTime: 27.5, timeListened: 15.5, duration: s.duration } }), 'seed close')
      },
      // Progress on the book that step c renames.
      [ids.saga2]: () => must(call(`/api/me/progress/${ids.saga2}`, { method: 'PATCH', token: lt, body: { currentTime: 14, duration: 30, progress: 14 / 30 } }), 'saga2 progress'),
      [ids.novel]: () => must(call(`/api/me/progress/${ids.novel}`, { method: 'PATCH', token: lt, body: { isFinished: true } }), 'finish novel')
    }
    for (const itemId of Object.keys(seedProgress).sort((x, y) => mediaId[x].localeCompare(mediaId[y]))) await seedProgress[itemId]()
    return { dirs, ids }
  } finally {
    await stop(server)
  }
}

// ---------------------------------------------------------------------------
// Normalization

const UUID = /[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}/g
const JWT = /eyJ[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+/g
const WALL_CLOCK_KEY = /^(addedAt|updatedAt|createdAt|lastScan|lastUpdate|startedAt|finishedAt|lastSeen|birthtimeMs|ctimeMs|lastScanVersionAt|timestamp|time_stamp)$/
let WORK_DIR = ''

function makeNormalizer(known, registry) {
  const normalize = (value, key = '') => {
    if (typeof value === 'string') {
      if (key === 'ino') return '<ino>'
      let v = value.replace(JWT, '<jwt>').replace(/\/file\/\d+/g, '/file/<ino>')
      if (WORK_DIR) v = v.split(WORK_DIR).join('<work>')
      v = v.replace(UUID, m => known.has(m) ? m : (registry.get(m) || '<new-uuid>'))
      if (/^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}/.test(v)) return '<iso>'
      v = v.replace(/\(\d+ (ms|sec|min)\)$/, '(<elapsed>)')
      return v
    }
    if (typeof value === 'number') {
      if (key === 'ino') return '<ino>'
      if (key === 'elapsed') return '<ms>'
      if (WALL_CLOCK_KEY.test(key)) return '<ts>'
      if (Math.abs(value - Date.now()) < 7 * 86400000) return '<now-ms>'
      return value
    }
    if (Array.isArray(value)) return value.map(v => normalize(v, key))
    if (value && typeof value === 'object') {
      const out = {}
      for (const k of Object.keys(value).sort()) out[k] = normalize(value[k], k)
      return out
    }
    return value
  }
  return normalize
}

function snapshotUUIDs(ids, snapshotDb) {
  const set = new Set(Object.values(ids))
  if (snapshotDb) for (const m of fs.readFileSync(snapshotDb, 'latin1').match(UUID) || []) set.add(m)
  return set
}

// ---------------------------------------------------------------------------
// Scenario

async function connectRootSocket(server, token, buffer) {
  const io = loadSocketIOClient()
  if (!io) throw new Error('socket.io-client unavailable (set ABS_PARITY_SOCKETIO_PATH or install client deps)')
  const socket = io(server.baseUrl, { path: `${BASE}/socket.io`, transports: ['websocket'], reconnection: false, timeout: 10000 })
  socket.onAny((name, ...args) => buffer.push({ event: name, payload: args.length > 1 ? args : args[0] }))
  await new Promise((resolve, reject) => {
    const t = setTimeout(() => reject(new Error('socket init timeout')), 15000)
    socket.on('connect', () => socket.emit('auth', token))
    socket.on('init', () => { clearTimeout(t); resolve() })
    socket.on('connect_error', e => { clearTimeout(t); reject(e) })
  })
  return socket
}

async function runScenario(server, ids, work, paths) {
  const call = api(server)
  const buffer = []
  const rootUser = await loginAs(call, ROOT_USER)
  const listener = await loginAs(call, LISTENER)
  const tokens = { root: rootUser.accessToken, listener: listener.accessToken }
  const socket = await connectRootSocket(server, tokens.root, buffer)
  buffer.length = 0
  const known = snapshotUUIDs(ids, path.join(work, 'snapshot', 'config', 'absdatabase.sqlite'))
  const registry = new Map()
  const normalize = makeNormalizer(known, registry)
  const lib = ids.library
  const books = paths.books
  const results = {}
  const order = []

  const reg = (id, label) => { if (id && !known.has(id) && !registry.has(id)) registry.set(id, label) }
  async function refreshRegistry() {
    const items = await call(`/api/libraries/${lib}/items?limit=200&minified=1`, { token: tokens.root })
    for (const it of items.value?.results || []) reg(it.id, `<item:${it.relPath}>`)
    const authors = await call(`/api/libraries/${lib}/authors`, { token: tokens.root })
    for (const a of authors.value?.authors || []) reg(a.id, `<author:${a.name}>`)
    const series = await call(`/api/libraries/${lib}/series?limit=200`, { token: tokens.root })
    for (const s of series.value?.results || []) reg(s.id, `<series:${s.name}>`)
  }

  const itemIdByRel = async (rel) => {
    const items = await call(`/api/libraries/${lib}/items?limit=200&minified=1`, { token: tokens.root })
    const it = (items.value?.results || []).find(i => i.relPath === rel)
    if (!it) throw new Error(`no library item with relPath ${rel}`)
    return it.id
  }

  async function step(id, fn) {
    buffer.length = 0
    const res = {}
    const h = {
      // Record a request/response under `label` for the current step.
      async http(label, who, route, opts = {}) {
        const { binary, ...rest } = opts
        const r = await call(route, { ...rest, token: who ? tokens[who] : undefined })
        let body = r.value
        if (binary === 'meta') body = { type: r.headers.get('content-type'), nonEmpty: r.bytes.length > 0 }
        if (Array.isArray(body) && route.includes('/personalized')) {
          for (const shelf of body) if (shelf?.id === 'discover' && Array.isArray(shelf.entities)) shelf.entities.sort((a, b) => String(a.id).localeCompare(String(b.id)))
        }
        if (Array.isArray(body?.authorsWithCount)) {
          body.authorsWithCount.sort((a, b) => b.count - a.count || String(a.name).localeCompare(String(b.name)))
        }
        res[label] = { status: r.status, body }
        return r
      },
      // Wait for the library-scan task to finish (task_finished event), which is
      // the completion signal both servers emit; also covers "count changes".
      async waitScan(timeoutMs = SCAN_TIMEOUT_MS) {
        const start = Date.now()
        while (Date.now() - start < timeoutMs) {
          if (buffer.some(e => e.event === 'task_finished' && e.payload?.action === 'library-scan')) { await delay(500); return true }
          await delay(200)
        }
        res._scanTimeout = { timedOut: true }
        return false
      },
      // Wait until the number of library items differs from `count` (polling).
      async waitCount(count, timeoutMs) {
        const start = Date.now()
        while (Date.now() - start < timeoutMs) {
          const r = await call(`/api/libraries/${lib}/items?limit=1`, { token: tokens.root })
          if (r.value?.total !== count) return true
          await delay(1000)
        }
        return false
      },
      note(label, value) { res[label] = value },
      libItems: async () => (await call(`/api/libraries/${lib}/items?limit=200&minified=1`, { token: tokens.root })).value
    }
    try { await fn(h) } catch (e) { res._error = { message: String(e.message || e).replace(WORK_DIR, '<work>') } }
    await delay(500)
    const events = buffer.splice(0)
    await refreshRegistry()
    results[id] = { responses: normalize(res), events: normalize(events) }
    order.push(id)
  }

  const itemChecks = async (h, prefix, itemId, listenerToo = true) => {
    await h.http(`${prefix}.item`, 'root', `/api/items/${itemId}?expanded=1&include=progress,authors`)
    await h.http(`${prefix}.cover`, 'root', `/api/items/${itemId}/cover`, { binary: 'meta' })
    if (listenerToo) await h.http(`${prefix}.progress`, 'listener', `/api/me/progress/${itemId}`)
  }
  const listChecks = async (h, prefix) => {
    await h.http(`${prefix}.items`, 'root', `/api/libraries/${lib}/items?limit=50&sort=media.metadata.title&desc=0`)
    await h.http(`${prefix}.items.minified`, 'root', `/api/libraries/${lib}/items?limit=50&minified=1&sort=addedAt&desc=1`)
    await h.http(`${prefix}.series`, 'root', `/api/libraries/${lib}/series?limit=50&sort=name&desc=0`)
    await h.http(`${prefix}.authors`, 'root', `/api/libraries/${lib}/authors`)
  }
  const scanLibrary = async (h, label = 'scan') => {
    await h.http(label, 'root', `/api/libraries/${lib}/scan`, { method: 'POST', body: {} })
    await h.waitScan()
  }

  const saga1Dir = path.join(books, 'Terry Fixture', 'Long Saga', 'Book 1 - The Beginning')
  const saga2Dir = path.join(books, 'Terry Fixture', 'Long Saga', 'Book 2 - The Middle')
  const saga2Renamed = path.join(books, 'Terry Fixture', 'Long Saga', 'Book 2 - The Middle Revised')
  const dawnDir = path.join(books, 'Nora Writer', 'Fresh Arrivals', 'Vol 1 - Dawn Chorus')
  const DAWN_REL = 'Nora Writer/Fresh Arrivals/Vol 1 - Dawn Chorus'
  const WATCHED_REL = 'Wanda Watcher/Watched Folder'

  await refreshRegistry()

  // 0. Quiet the watcher for the explicit-scan steps (library update path).
  await step('0.watcher.disable', async (h) => {
    await h.http('update', 'root', `/api/libraries/${lib}`, { method: 'PATCH', body: { settings: { disableWatcher: true } } })
  })

  // 0b. Baseline rescan of the restored (identical content, new inodes) tree.
  await step('0.baseline.rescan', async (h) => {
    await scanLibrary(h)
    await listChecks(h, 'after')
  })

  await step('a.add-book', async (h) => {
    const before = (await h.libItems()).total
    media.copyInto(path.join(paths.stage, 'newbook', 'Nora Writer'), books)
    await h.http('scan', 'root', `/api/libraries/${lib}/scan`, { method: 'POST', body: {} })
    await h.waitScan()
    const changed = await h.waitCount(before, 20000)
    if (!changed) throw new Error('library item count did not change after scan')
    const id = await itemIdByRel(DAWN_REL)
    await itemChecks(h, 'dawn', id, false)
    await h.http('dawn.cover.thumb', 'root', `/api/items/${id}/cover?width=200&format=jpeg`, { binary: 'meta' })
    await listChecks(h, 'list')
    await h.http('filterdata', 'root', `/api/libraries/${lib}?include=filterdata`)
    await h.http('personalized.listener', 'listener', `/api/libraries/${lib}/personalized?minified=1&include=rssfeed,numEpisodesIncomplete,share`)
    await h.http('library.stats', 'root', `/api/libraries/${lib}/stats`)
    await h.http('search', 'root', `/api/libraries/${lib}/search?q=dawn&limit=12`)
  })

  await step('b.modify-book', async (h) => {
    media.copyInto(path.join(paths.stage, 'extra', '04 - Part 4.mp3'), saga1Dir)
    media.copyInto(path.join(paths.stage, 'extra', 'desc.txt'), saga1Dir)
    media.setMtime(saga1Dir)
    await scanLibrary(h)
    await itemChecks(h, 'saga1', ids.saga1)
    await listChecks(h, 'list')
  })

  await step('c.rename-book', async (h) => {
    fs.renameSync(saga2Dir, saga2Renamed)
    await scanLibrary(h)
    await itemChecks(h, 'saga2', ids.saga2)
    await h.http('me.listener', 'listener', '/api/me')
    await listChecks(h, 'list')
    await h.http('personalized.listener', 'listener', `/api/libraries/${lib}/personalized?minified=1&include=rssfeed,numEpisodesIncomplete,share`)
  })

  await step('d.delete-book', async (h) => {
    fs.rmSync(saga2Renamed, { recursive: true, force: true })
    await scanLibrary(h)
    await itemChecks(h, 'gone', ids.saga2)
    await listChecks(h, 'list')
    await h.http('items.issues', 'root', `/api/libraries/${lib}/items?limit=50&minified=1&filter=issues`)
    await h.http('filterdata', 'root', `/api/libraries/${lib}?include=filterdata`)
    await h.http('library.stats', 'root', `/api/libraries/${lib}/stats`)
    await h.http('personalized.listener', 'listener', `/api/libraries/${lib}/personalized?minified=1&include=rssfeed,numEpisodesIncomplete,share`)
  })

  await step('d2.remove-issues', async (h) => {
    await h.http('delete.issues', 'root', `/api/libraries/${lib}/issues`, { method: 'DELETE' })
    await itemChecks(h, 'gone', ids.saga2)
    await listChecks(h, 'list')
    await h.http('items.issues', 'root', `/api/libraries/${lib}/items?limit=50&minified=1&filter=issues`)
    await h.http('filterdata', 'root', `/api/libraries/${lib}?include=filterdata`)
    await h.http('me.listener', 'listener', '/api/me')
    await h.http('library.stats', 'root', `/api/libraries/${lib}/stats`)
  })

  await step('e.item-scan', async (h) => {
    await h.http('scan.unchanged', 'root', `/api/items/${ids.saga1}/scan`, { method: 'POST' })
    const dawnId = await itemIdByRel(DAWN_REL)
    // Replace the cover and remove a track, then rescan just that item.
    media.copyInto(path.join(paths.stage, 'replacement-cover.jpg'), path.join(dawnDir, 'cover.jpg'))
    media.setMtime(path.join(dawnDir, 'cover.jpg'), media.FILE_MTIME_S + 500)
    fs.rmSync(path.join(dawnDir, '03 - Chorus 3.mp3'))
    media.setMtime(dawnDir)
    await h.http('scan.changed', 'root', `/api/items/${dawnId}/scan`, { method: 'POST' })
    await itemChecks(h, 'dawn', dawnId, false)
    await h.http('scan.again', 'root', `/api/items/${dawnId}/scan`, { method: 'POST' })
    await h.http('scan.listener', 'listener', `/api/items/${dawnId}/scan`, { method: 'POST' })
    await listChecks(h, 'list')
  })

  await step('f.metadata-edit', async (h) => {
    const dawnId = await itemIdByRel(DAWN_REL)
    await h.http('patch.media', 'root', `/api/items/${dawnId}/media`, { method: 'PATCH', body: { metadata: {
      title: 'Dawn Chorus Revised', subtitle: 'A Second Look',
      authors: [{ name: 'Nora Writer' }, { name: 'Guest Author' }],
      narrators: ['Reader Two'],
      series: [{ name: 'Fresh Arrivals', sequence: '2' }, { name: 'Side Stories', sequence: '1' }],
      genres: ['Fantasy', 'Adventure'], publishedYear: '2023'
    } } })
    await itemChecks(h, 'dawn', dawnId, false)
    await h.http('series.after', 'root', `/api/libraries/${lib}/series?limit=50&sort=name&desc=0`)
    await h.http('authors.after', 'root', `/api/libraries/${lib}/authors`)
    await h.http('narrators.after', 'root', `/api/libraries/${lib}/narrators`)
    await h.http('filterdata', 'root', `/api/libraries/${lib}?include=filterdata`)
    const form = new FormData()
    form.append('cover', new Blob([fs.readFileSync(path.join(paths.stage, 'upload-cover.jpg'))], { type: 'image/jpeg' }), 'upload-cover.jpg')
    await h.http('cover.upload', 'root', `/api/items/${dawnId}/cover`, { method: 'POST', body: form })
    await h.http('cover.get', 'root', `/api/items/${dawnId}/cover`, { binary: 'meta' })
    await h.http('item.after.cover', 'root', `/api/items/${dawnId}?expanded=1`)
    await h.http('series.final', 'root', `/api/libraries/${lib}/series?limit=50&sort=name&desc=0`)
    await h.http('authors.final', 'root', `/api/libraries/${lib}/authors`)
    await h.http('upload.listener', 'listener', `/api/items/${dawnId}/cover`, { method: 'POST', body: form })
  })

  // Watcher steps: enable, add a folder without calling scan, wait (bounded)
  // for the watcher to pick it up, compare the final state.
  await step('g.watcher.enable', async (h) => {
    await h.http('update', 'root', `/api/libraries/${lib}`, { method: 'PATCH', body: { settings: { disableWatcher: false } } })
    await delay(3000)
  })
  await step('g.watcher.add', async (h) => {
    const before = (await h.libItems()).total
    media.copyInto(path.join(paths.stage, 'watched', 'Wanda Watcher'), books)
    const found = await h.waitCount(before, WATCHER_TIMEOUT_MS)
    h.note('detected', { found })
    await delay(3000)
    const id = await itemIdByRel(WATCHED_REL).catch(() => null)
    if (id) await itemChecks(h, 'watched', id, false)
    await listChecks(h, 'list')
  })
  await step('g.watcher.remove', async (h) => {
    fs.rmSync(path.join(books, 'Wanda Watcher', 'Watched Folder'), { recursive: true, force: true })
    // Upstream marks the item missing after the 10s pending delay; wait a fixed time.
    await delay(30000)
    await listChecks(h, 'list')
    await h.http('items.issues', 'root', `/api/libraries/${lib}/items?limit=50&minified=1&filter=issues`)
  })

  socket.disconnect()
  return { results, order }
}

// ---------------------------------------------------------------------------

function diffPaths(x, y, p = '', out = []) {
  if (out.length >= 60 || JSON.stringify(x) === JSON.stringify(y)) return out
  if (x && y && typeof x === 'object' && typeof y === 'object' && Array.isArray(x) === Array.isArray(y)) {
    for (const k of new Set([...Object.keys(x), ...Object.keys(y)])) diffPaths(x[k], y[k], `${p}.${k}`, out)
    return out
  }
  out.push(`${p}: upstream=${JSON.stringify(x)?.slice(0, 200)} go=${JSON.stringify(y)?.slice(0, 200)}`)
  return out
}

async function main() {
  const args = parseArgs()
  const work = path.resolve(args.work || fs.mkdtempSync(path.join(os.tmpdir(), 'abs-scan-')))
  const goBin = path.resolve(args['go-bin'] || path.join(ROOT, 'go-backend', 'audiobookshelf-go'))
  fs.mkdirSync(work, { recursive: true })
  console.log(`work dir: ${work}`)
  if (!args['go-bin']) media.sh('go', ['build', '-o', goBin, './cmd/audiobookshelf-go'], { cwd: path.join(ROOT, 'go-backend'), env: { ...process.env, CGO_ENABLED: '0' } })

  WORK_DIR = work
  const snapshot = path.join(work, 'snapshot')
  const paths = {
    books: path.join(work, 'media', 'audiobooks'),
    pristine: path.join(work, 'media-pristine'),
    stage: path.join(work, 'stage')
  }
  let dirs, ids
  if (args.reuse && fs.existsSync(path.join(work, 'ids.json'))) {
    ids = JSON.parse(fs.readFileSync(path.join(work, 'ids.json'), 'utf8'))
    dirs = { config: path.join(work, 'install', 'config'), metadata: path.join(work, 'install', 'metadata') }
  } else {
    fs.rmSync(path.join(work, 'media'), { recursive: true, force: true })
    media.generateSeedMedia(path.join(work, 'media'))
    media.generateStage(paths.stage)
    media.pinMtimes(paths.books)
    media.pinMtimes(paths.stage)
    ;({ dirs, ids } = await seed(work, paths.books))
    media.copyTree(path.join(work, 'install'), snapshot)
    media.copyTree(path.join(work, 'media'), paths.pristine)
    fs.writeFileSync(path.join(work, 'ids.json'), JSON.stringify(ids, null, 2))
  }
  const restore = () => {
    media.copyTree(snapshot, path.join(work, 'install'))
    media.copyTree(paths.pristine, path.join(work, 'media'))
  }

  restore()
  const up = await startUpstream(dirs, await freePort(), path.join(work, 'scan-upstream.log'))
  let upstream
  try { upstream = await runScenario(up, ids, work, paths) } finally { await stop(up) }
  fs.writeFileSync(path.join(work, 'scan-upstream.json'), JSON.stringify(upstream.results, null, 2))

  restore()
  const go = await startGo(goBin, dirs, await freePort(), path.join(work, 'scan-go.log'))
  let goRun
  try { goRun = await runScenario(go, ids, work, paths) } finally { await stop(go) }
  fs.writeFileSync(path.join(work, 'scan-go.json'), JSON.stringify(goRun.results, null, 2))

  // Per step: responses and events are diffed separately.
  const differing = []
  let total = 0
  for (const id of upstream.order) {
    total++
    const u = upstream.results[id]; const g = goRun.results[id]
    const respDiffs = diffPaths(u.responses, g?.responses)
    const eventDiffs = diffPaths(u.events, g?.events)
    if (respDiffs.length || eventDiffs.length) differing.push({ id, responses: respDiffs, events: eventDiffs, upstreamEvents: u.events.map(e => e.event), goEvents: g?.events.map(e => e.event) })
  }
  const report = { steps: total, matched: total - differing.length, differing }
  fs.writeFileSync(path.join(work, 'scan-report.json'), JSON.stringify(report, null, 2))
  console.log(JSON.stringify(report, null, 2))
  console.log(`\nscan trial: ${report.matched}/${report.steps} steps match (responses and realtime events)`)
  if (differing.length) process.exitCode = 1
}

main().catch((error) => { console.error(error); process.exitCode = 1 })
