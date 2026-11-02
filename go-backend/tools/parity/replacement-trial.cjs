#!/usr/bin/env node
'use strict'

// Replacement trial: can the Go binary take over an EXISTING upstream
// installation in place, serve its clients, books and progress, and hand the
// installation back to upstream afterwards?
//
// Unlike the core differential (one synthetic 2-second book, path-rewritten
// copies), this trial:
//  1. generates a realistic media tree (multi-file mp3 book with ID3 tags and
//     cover.jpg, single-file m4b with chapters, flac book with desc.txt, an
//     epub-only book) and lets pinned upstream create and scan the
//     installation, then records listening history through the same calls
//     the mobile apps and web client make (play/sync/close, offline local
//     sessions, finished flags, ebook location, bookmarks, collections,
//     playlists);
//  2. snapshots config+metadata (media is never mutated, so paths and inodes
//     stay identical — this is the in-place swap a real user performs);
//  3. runs one scripted client journey against upstream, restores the
//     snapshot to the SAME paths, runs the identical journey against Go, and
//     diffs every step (status + normalized body; bytes for media);
//  4. stops Go and boots upstream on the Go-written state (rollback) and
//     checks that progress, sessions and bookmarks written by Go are read
//     back by upstream.
//
// Usage: node replacement-trial.cjs [--work DIR] [--go-bin PATH] [--reuse 1]
// --reuse 1 skips media generation and seeding when DIR already holds a seeded
// snapshot (faster iteration on Go fixes).
// Exit non-zero when any journey step differs or the rollback check fails.

const fs = require('node:fs')
const os = require('node:os')
const path = require('node:path')
const { spawnSync } = require('node:child_process')
const { setTimeout: delay } = require('node:timers/promises')
const { freePort, spawnLogged, waitReady, stop } = require('./lib/process.cjs')
const { requestJSON } = require('./lib/http.cjs')
const { parseArgs } = require('./lib/args.cjs')
const { loadSocketIOClient } = require('./lib/socket-probes.cjs')

const ROOT = path.resolve(__dirname, '../../..')
const BASE = '/audiobookshelf'
const ROOT_USER = { username: 'trial-root', password: 'trial-root-password' }
const LISTENER = { username: 'trial-listener', password: 'trial-listener-password' }
// A second listener who finished book 1 of the saga, so "Continue Series" applies.
const FINISHER = { username: 'trial-finisher', password: 'trial-finisher-password' }
const ANDROID_DEVICE = {
  deviceId: 'trial-android-0001', manufacturer: 'Google', model: 'Pixel 8', sdkVersion: 34,
  clientName: 'Abs Android', clientVersion: '0.9.79-beta'
}
const IOS_DEVICE = {
  deviceId: 'trial-ios-0001', manufacturer: 'Apple', model: 'iPhone15,2', sdkVersion: 17,
  clientName: 'Abs iOS', clientVersion: '0.9.79-beta'
}
const MOBILE_MIME = ['audio/flac', 'audio/mpeg', 'audio/mp4', 'audio/ogg', 'audio/aac', 'audio/webm']

function sh(cmd, args, opts = {}) {
  const r = spawnSync(cmd, args, { encoding: 'utf8', ...opts })
  if (r.status !== 0) throw new Error(`${cmd} ${args.join(' ')} failed: ${r.stderr || r.stdout}`)
  return r.stdout
}

function ffmpeg(args) { sh('ffmpeg', ['-hide_banner', '-loglevel', 'error', '-y', ...args]) }

// ---------------------------------------------------------------------------
// Media generation

function generateMedia(mediaRoot) {
  const books = path.join(mediaRoot, 'audiobooks')
  const scratch = path.join(mediaRoot, '..', 'gen')
  fs.mkdirSync(scratch, { recursive: true })

  // 1. Multi-file mp3 book in Author/Series/Title layout with cover.jpg.
  const saga1 = path.join(books, 'Terry Fixture', 'Long Saga', 'Book 1 - The Beginning')
  fs.mkdirSync(saga1, { recursive: true })
  for (let i = 1; i <= 3; i++) {
    ffmpeg(['-f', 'lavfi', '-i', `sine=frequency=${300 + i * 100}:duration=20`, '-ac', '1', '-ar', '22050', '-b:a', '32k',
      '-metadata', `title=Part ${i}`, '-metadata', 'artist=Terry Fixture', '-metadata', 'album=The Beginning',
      '-metadata', `track=${i}/3`, '-id3v2_version', '3', path.join(saga1, `0${i} - Part ${i}.mp3`)])
  }
  ffmpeg(['-f', 'lavfi', '-i', 'color=c=navy:s=600x600', '-frames:v', '1', path.join(saga1, 'cover.jpg')])

  // 2. Second series entry, flac + desc.txt + reader.txt.
  const saga2 = path.join(books, 'Terry Fixture', 'Long Saga', 'Book 2 - The Middle')
  fs.mkdirSync(saga2, { recursive: true })
  ffmpeg(['-f', 'lavfi', '-i', 'sine=frequency=550:duration=30', '-ac', '1', '-ar', '22050',
    '-metadata', 'title=The Middle', '-metadata', 'artist=Terry Fixture', path.join(saga2, 'The Middle.flac')])
  fs.writeFileSync(path.join(saga2, 'desc.txt'), 'The second book of the saga, narrated slowly.')
  fs.writeFileSync(path.join(saga2, 'reader.txt'), 'Nora Narrator')

  // 3. Single m4b with chapters and an embedded cover.
  const novel = path.join(books, 'Ada Example', 'Single File Novel')
  fs.mkdirSync(novel, { recursive: true })
  const chapters = path.join(scratch, 'chapters.txt')
  fs.writeFileSync(chapters, [';FFMETADATA1', 'title=Single File Novel', 'artist=Ada Example', 'album=Single File Novel',
    'genre=Fiction', 'date=2021',
    '[CHAPTER]', 'TIMEBASE=1/1000', 'START=0', 'END=15000', 'title=Opening',
    '[CHAPTER]', 'TIMEBASE=1/1000', 'START=15000', 'END=30000', 'title=Middle',
    '[CHAPTER]', 'TIMEBASE=1/1000', 'START=30000', 'END=45000', 'title=Ending', ''].join('\n'))
  const embedCover = path.join(scratch, 'embed.jpg')
  ffmpeg(['-f', 'lavfi', '-i', 'color=c=maroon:s=400x400', '-frames:v', '1', embedCover])
  ffmpeg(['-f', 'lavfi', '-i', 'sine=frequency=440:duration=45', '-i', chapters, '-i', embedCover,
    '-map', '0:a', '-map', '2:v', '-map_metadata', '1', '-map_chapters', '1',
    '-c:a', 'aac', '-b:a', '32k', '-ac', '1', '-c:v', 'mjpeg', '-disposition:v', 'attached_pic',
    '-f', 'mp4', path.join(novel, 'Single File Novel.m4b')])

  // 4. Ebook-only book (minimal valid EPUB 3).
  const ebookDir = path.join(books, 'Ada Example', 'Ebook Only')
  fs.mkdirSync(ebookDir, { recursive: true })
  const epubSrc = path.join(scratch, 'epub')
  fs.mkdirSync(path.join(epubSrc, 'META-INF'), { recursive: true })
  fs.mkdirSync(path.join(epubSrc, 'OEBPS'), { recursive: true })
  fs.writeFileSync(path.join(epubSrc, 'mimetype'), 'application/epub+zip')
  fs.writeFileSync(path.join(epubSrc, 'META-INF', 'container.xml'), '<?xml version="1.0"?><container version="1.0" xmlns="urn:oasis:names:tc:opendocument:xmlns:container"><rootfiles><rootfile full-path="OEBPS/content.opf" media-type="application/oebps-package+xml"/></rootfiles></container>')
  fs.writeFileSync(path.join(epubSrc, 'OEBPS', 'content.opf'), '<?xml version="1.0" encoding="UTF-8"?><package xmlns="http://www.idpf.org/2007/opf" version="3.0" unique-identifier="uid"><metadata xmlns:dc="http://purl.org/dc/elements/1.1/"><dc:identifier id="uid">trial-ebook-1</dc:identifier><dc:title>Ebook Only</dc:title><dc:creator>Ada Example</dc:creator><dc:language>en</dc:language><meta property="dcterms:modified">2024-01-01T00:00:00Z</meta></metadata><manifest><item id="c1" href="c1.xhtml" media-type="application/xhtml+xml"/><item id="nav" href="nav.xhtml" media-type="application/xhtml+xml" properties="nav"/></manifest><spine><itemref idref="c1"/></spine></package>')
  fs.writeFileSync(path.join(epubSrc, 'OEBPS', 'c1.xhtml'), '<?xml version="1.0" encoding="UTF-8"?><html xmlns="http://www.w3.org/1999/xhtml"><head><title>One</title></head><body><p>Chapter one text.</p></body></html>')
  fs.writeFileSync(path.join(epubSrc, 'OEBPS', 'nav.xhtml'), '<?xml version="1.0" encoding="UTF-8"?><html xmlns="http://www.w3.org/1999/xhtml" xmlns:epub="http://www.idpf.org/2007/ops"><head><title>Nav</title></head><body><nav epub:type="toc"><ol><li><a href="c1.xhtml">One</a></li></ol></nav></body></html>')
  const epub = path.join(ebookDir, 'Ebook Only.epub')
  sh('zip', ['-X0q', epub, 'mimetype'], { cwd: epubSrc })
  sh('zip', ['-Xr9Dq', epub, 'META-INF', 'OEBPS'], { cwd: epubSrc })
  return books
}

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
  return async (route, opts = {}) => requestJSON(server.baseUrl, route, { timeoutMs: 30000, ...opts })
}

async function must(promise, label) {
  const r = await promise
  if (!r.ok) throw new Error(`${label} -> ${r.status}: ${JSON.stringify(r.value).slice(0, 400)}`)
  return r.value
}

async function loginAs(call, creds, extraHeaders = {}) {
  const v = await must(call('/login', { method: 'POST', body: creds, headers: { 'X-Return-Tokens': 'true', ...extraHeaders } }), `login ${creds.username}`)
  return v.user
}

// ---------------------------------------------------------------------------
// Seeding: upstream creates the installation and its history.

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
    for (let i = 0; i < 120 && items.length < 4; i++) {
      await delay(500)
      items = (await must(call(`/api/libraries/${library.id}/items?limit=50`, { token: rt }), 'items')).results
    }
    if (items.length !== 4) throw new Error(`upstream scan produced ${items.length} items, expected 4`)
    const byTitle = Object.fromEntries(items.map(i => [i.media.metadata.title, i.id]))
    const ids = {
      library: library.id,
      saga1: byTitle['The Beginning'] || byTitle['Book 1 - The Beginning'],
      saga2: byTitle['The Middle'] || byTitle['Book 2 - The Middle'],
      novel: byTitle['Single File Novel'],
      ebook: byTitle['Ebook Only']
    }
    for (const [k, v] of Object.entries(ids)) if (!v) throw new Error(`seed: no item for ${k}; titles=${Object.keys(byTitle)}`)

    await must(call('/api/users', { method: 'POST', token: rt, body: { ...LISTENER, type: 'user', isActive: true,
      permissions: { download: true, update: false, delete: false, upload: false, accessAllLibraries: true, accessAllTags: true, accessExplicitContent: true } } }), 'create listener')
    const listener = await loginAs(call, LISTENER)
    const lt = listener.accessToken
    ids.listener = listener.id

    // Android streaming session on the multi-file book.
    const s = await must(call(`/api/items/${ids.saga1}/play`, { method: 'POST', token: lt, body: {
      deviceInfo: ANDROID_DEVICE, mediaPlayer: 'exo-player', forceDirectPlay: true, forceTranscode: false, supportedMimeTypes: MOBILE_MIME } }), 'seed play')
    await must(call(`/api/session/${s.id}/sync`, { method: 'POST', token: lt, body: { currentTime: 12, timeListened: 12, duration: s.duration } }), 'seed sync')
    await must(call(`/api/session/${s.id}/sync`, { method: 'POST', token: lt, body: { currentTime: 27.5, timeListened: 15.5, duration: s.duration } }), 'seed sync 2')
    await must(call(`/api/session/${s.id}/close`, { method: 'POST', token: lt, body: { currentTime: 27.5, timeListened: 0, duration: s.duration } }), 'seed close')

    // Finished novel; ebook location; bookmark; collection; playlist.
    await must(call(`/api/me/progress/${ids.novel}`, { method: 'PATCH', token: lt, body: { isFinished: true } }), 'finish novel')
    await must(call(`/api/me/progress/${ids.ebook}`, { method: 'PATCH', token: lt, body: { ebookLocation: 'epubcfi(/6/2!/4/2/1:0)', ebookProgress: 0.25 } }), 'ebook progress')
    await must(call(`/api/me/item/${ids.saga1}/bookmark`, { method: 'POST', token: lt, body: { time: 21, title: 'Good bit' } }), 'bookmark')
    const col = await must(call('/api/collections', { method: 'POST', token: rt, body: { libraryId: ids.library, name: 'Favourites', books: [ids.saga1, ids.novel] } }), 'collection')
    ids.collection = col.id
    const pl = await must(call('/api/playlists', { method: 'POST', token: lt, body: { libraryId: ids.library, name: 'Commute', items: [{ libraryItemId: ids.saga2 }, { libraryItemId: ids.novel }] } }), 'playlist')
    ids.playlist = pl.id

    await must(call('/api/users', { method: 'POST', token: rt, body: { ...FINISHER, type: 'user', isActive: true } }), 'create finisher')
    const finisher = await loginAs(call, FINISHER)
    await must(call(`/api/me/progress/${ids.saga1}`, { method: 'PATCH', token: finisher.accessToken, body: { isFinished: true } }), 'finisher finishes book 1')

    // Offline listening on iOS synced later (mobile local session upload).
    const now = Date.now()
    const local = {
      id: 'play_local_trial0001', userId: ids.listener, libraryId: ids.library, libraryItemId: ids.saga2,
      mediaType: 'book', mediaMetadata: {}, chapters: [], displayTitle: 'The Middle', displayAuthor: 'Terry Fixture',
      coverPath: null, duration: 30, playMethod: 3, mediaPlayer: 'AVPlayer', deviceInfo: IOS_DEVICE,
      serverVersion: '2.37.2', date: new Date(now - 86400000).toISOString().slice(0, 10), dayOfWeek: 'Sunday',
      timeListening: 9, startTime: 0, currentTime: 9, startedAt: now - 86400000, updatedAt: now - 86400000 + 9000
    }
    await must(call('/api/session/local-all', { method: 'POST', token: lt, body: { deviceInfo: IOS_DEVICE, sessions: [local] } }), 'local-all')
    return { dirs, ids }
  } finally {
    await stop(server)
  }
}

// ---------------------------------------------------------------------------
// Journey: what the mobile apps and the web client call after the swap.

function journeySteps(ids) {
  const L = 'listener'
  const R = 'root'
  const s = []
  const add = (id, who, route, opts = {}) => s.push({ id, who, route, ...opts })

  add('ping', null, '/ping')
  add('status', null, '/status')
  add('ping.prefixed', null, `${BASE}/ping`)
  add('login.listener.mobile', null, '/login', { method: 'POST', body: LISTENER, headers: { 'X-Return-Tokens': 'true' }, capture: 'listenerLogin' })
  add('login.listener.web', null, `${BASE}/login`, { method: 'POST', body: LISTENER, capture: 'listenerWebLogin' })
  add('authorize', L, '/api/authorize', { method: 'POST' })
  add('me', L, '/api/me')
  add('libraries', L, '/api/libraries')
  add('library.filterdata', L, `/api/libraries/${ids.library}?include=filterdata`)
  add('personalized', L, `/api/libraries/${ids.library}/personalized?minified=1&include=rssfeed,numEpisodesIncomplete,share`)
  add('items.title', L, `/api/libraries/${ids.library}/items?limit=20&page=0&minified=1&sort=media.metadata.title&desc=0&include=rssfeed,numEpisodesIncomplete`)
  add('items.added', L, `/api/libraries/${ids.library}/items?limit=20&page=0&minified=1&sort=addedAt&desc=1`)
  add('items.collapseseries', L, `/api/libraries/${ids.library}/items?limit=20&page=0&minified=1&sort=media.metadata.title&collapseseries=1`)
  add('items.filter.inprogress', L, `/api/libraries/${ids.library}/items?limit=20&minified=1&filter=progress.${Buffer.from('in-progress').toString('base64')}`)
  add('items.filter.finished', L, `/api/libraries/${ids.library}/items?limit=20&minified=1&filter=progress.${Buffer.from('finished').toString('base64')}`)
  add('items.filter.notstarted', L, `/api/libraries/${ids.library}/items?limit=20&minified=1&filter=progress.${Buffer.from('not-started').toString('base64')}`)
  add('items.filter.ebooks', L, `/api/libraries/${ids.library}/items?limit=20&minified=1&filter=ebooks.${Buffer.from('ebook').toString('base64')}`)
  add('items.duration', L, `/api/libraries/${ids.library}/items?limit=20&minified=1&sort=media.duration&desc=1`)
  add('series', L, `/api/libraries/${ids.library}/series?limit=20&page=0&sort=name&desc=0&minified=1`)
  add('authors', L, `/api/libraries/${ids.library}/authors`)
  add('narrators', L, `/api/libraries/${ids.library}/narrators`)
  add('collections', L, `/api/libraries/${ids.library}/collections?minified=1`)
  add('playlists', L, `/api/libraries/${ids.library}/playlists?minified=1`)
  add('playlist', L, `/api/playlists/${ids.playlist}`)
  add('collection', L, `/api/collections/${ids.collection}`)
  add('search', L, `/api/libraries/${ids.library}/search?q=saga&limit=12`)
  add('search.author', L, `/api/libraries/${ids.library}/search?q=ada&limit=12`)
  add('items-in-progress', L, '/api/me/items-in-progress')
  add('listening-stats', L, '/api/me/listening-stats')
  add('listening-sessions', L, '/api/me/listening-sessions?itemsPerPage=10&page=0')
  add('progress.saga1', L, `/api/me/progress/${ids.saga1}`)
  add('progress.ebook', L, `/api/me/progress/${ids.ebook}`)
  for (const k of ['saga1', 'saga2', 'novel', 'ebook']) {
    add(`item.${k}.expanded`, L, `/api/items/${ids[k]}?expanded=1&include=progress,rssfeed,authors,downloads,share`)
    add(`item.${k}.cover`, L, `/api/items/${ids[k]}/cover`, { binary: 'meta' })
    add(`item.${k}.cover.thumb`, L, `/api/items/${ids[k]}/cover?width=400&format=jpeg`, { binary: 'meta' })
  }
  add('item.batch', L, '/api/items/batch/get', { method: 'POST', body: { libraryItemIds: [ids.saga1, ids.novel] } })
  add('ebook.download', L, `/api/items/${ids.ebook}/ebook`, { binary: 'bytes' })

  // Android direct play resumes from seeded progress; stream a range; sync; close.
  add('play.android', L, `/api/items/${ids.saga1}/play`, { method: 'POST', capture: 'androidSession', body: {
    deviceInfo: ANDROID_DEVICE, mediaPlayer: 'exo-player', forceDirectPlay: true, forceTranscode: false, supportedMimeTypes: MOBILE_MIME } })
  add('stream.track1.range', L, ctx => ctx.androidSession.audioTracks[0].contentUrl, { headers: { Range: 'bytes=0-4095' }, binary: 'bytes', tokenQuery: true })
  add('stream.track2.full', L, ctx => ctx.androidSession.audioTracks[1].contentUrl, { binary: 'bytes', tokenQuery: true })
  add('sync.android.1', L, ctx => `/api/session/${ctx.androidSession.id}/sync`, { method: 'POST', body: ctx => ({ currentTime: 35, timeListened: 7.5, duration: ctx.androidSession.duration }) })
  add('progress.after.sync', L, `/api/me/progress/${ids.saga1}`)
  add('sync.android.2', L, ctx => `/api/session/${ctx.androidSession.id}/sync`, { method: 'POST', body: ctx => ({ currentTime: 44, timeListened: 9, duration: ctx.androidSession.duration }) })
  add('close.android', L, ctx => `/api/session/${ctx.androidSession.id}/close`, { method: 'POST', body: ctx => ({ currentTime: 46, timeListened: 2, duration: ctx.androidSession.duration }) })
  add('progress.after.close', L, `/api/me/progress/${ids.saga1}`)

  // Web player on the m4b: direct then forced transcode (HLS).
  add('play.web.direct', L, `/api/items/${ids.novel}/play`, { method: 'POST', capture: 'webSession', body: {
    deviceInfo: { clientName: 'Abs Web', clientVersion: '2.37.2' }, mediaPlayer: 'html5', forceDirectPlay: false, forceTranscode: false,
    supportedMimeTypes: ['audio/flac', 'audio/mpeg', 'audio/mp4', 'audio/ogg', 'audio/aac', 'audio/webm'] } })
  add('stream.m4b.range', L, ctx => ctx.webSession.audioTracks[0].contentUrl, { headers: { Range: 'bytes=0-8191' }, binary: 'bytes', tokenQuery: true })
  add('close.web.direct', L, ctx => `/api/session/${ctx.webSession.id}/close`, { method: 'POST', body: { currentTime: 16, timeListened: 16 } })
  add('play.web.hls', L, `/api/items/${ids.saga2}/play`, { method: 'POST', capture: 'hlsSession', body: {
    deviceInfo: { clientName: 'Abs Web', clientVersion: '2.37.2' }, mediaPlayer: 'html5', forceDirectPlay: false, forceTranscode: true, supportedMimeTypes: ['audio/mpeg'] } })
  add('hls.playlist', L, ctx => ctx.hlsSession.audioTracks[0].contentUrl, { binary: 'text', tokenQuery: true, retry: 20 })
  add('close.web.hls', L, ctx => `/api/session/${ctx.hlsSession.id}/close`, { method: 'POST', body: { currentTime: 5, timeListened: 5 } })

  // Mobile offline session + batch progress + bookmarks lifecycle.
  add('session.local', L, '/api/session/local', { method: 'POST', body: ctx => ({
    id: 'play_local_trial0002', userId: ctx.listenerId, libraryId: ids.library, libraryItemId: ids.novel, mediaType: 'book',
    mediaMetadata: {}, chapters: [], displayTitle: 'Single File Novel', displayAuthor: 'Ada Example', coverPath: null,
    duration: 45, playMethod: 3, mediaPlayer: 'exo-player', deviceInfo: ANDROID_DEVICE, serverVersion: '2.37.2',
    date: '2026-01-05', dayOfWeek: 'Monday', timeListening: 10, startTime: 0, currentTime: 10,
    startedAt: 1767600000000, updatedAt: 1767600010000 }) })
  add('progress.batch', L, '/api/me/progress/batch/update', { method: 'PATCH', body: [
    { libraryItemId: ids.saga2, currentTime: 12, duration: 30, progress: 0.4 },
    { libraryItemId: ids.novel, isFinished: false, currentTime: 3 }] })
  add('progress.saga2.after.batch', L, `/api/me/progress/${ids.saga2}`)
  add('bookmark.create', L, `/api/me/item/${ids.saga1}/bookmark`, { method: 'POST', body: { time: 40, title: 'Second' } })
  add('bookmark.update', L, `/api/me/item/${ids.saga1}/bookmark`, { method: 'PATCH', body: { time: 21, title: 'Renamed' } })
  add('bookmark.delete', L, `/api/me/item/${ids.saga1}/bookmark/40`, { method: 'DELETE' })
  add('ebook.progress', L, `/api/me/progress/${ids.ebook}`, { method: 'PATCH', body: { ebookLocation: 'epubcfi(/6/2!/4/2/1:9)', ebookProgress: 0.6 } })
  add('progress.hide', L, `/api/me/progress/${ids.novel}/remove-from-continue-listening`, { method: 'GET' })
  add('me.after', L, '/api/me')
  add('items-in-progress.after', L, '/api/me/items-in-progress')
  add('listening-sessions.after', L, '/api/me/listening-sessions?itemsPerPage=10&page=0')

  // Second user: Continue Series shelf and collapsed series with filters.
  add('login.finisher', null, '/login', { method: 'POST', body: FINISHER, headers: { 'X-Return-Tokens': 'true' } })
  add('personalized.finisher', 'finisher', `/api/libraries/${ids.library}/personalized?minified=1&include=rssfeed,numEpisodesIncomplete,share`)
  add('items.collapse.title.desc', 'finisher', `/api/libraries/${ids.library}/items?limit=20&page=0&minified=1&sort=media.metadata.title&desc=1&collapseseries=1`)
  add('items.collapse.notfinished', 'finisher', `/api/libraries/${ids.library}/items?limit=20&minified=1&collapseseries=1&filter=progress.${Buffer.from('not-finished').toString('base64')}`)
  add('items.collapse.page2', 'finisher', `/api/libraries/${ids.library}/items?limit=2&page=1&minified=1&sort=media.metadata.title&collapseseries=1`)

  // Root/admin views of the same history.
  add('login.root', null, '/login', { method: 'POST', body: ROOT_USER, headers: { 'X-Return-Tokens': 'true' }, capture: 'rootLogin' })
  add('users', R, '/api/users')
  add('user.listener', R, ctx => `/api/users/${ctx.listenerId}`)
  add('user.listener.sessions', R, ctx => `/api/users/${ctx.listenerId}/listening-sessions?itemsPerPage=10&page=0`)
  add('sessions.all', R, '/api/sessions?itemsPerPage=10&page=0')
  add('library.stats', R, `/api/libraries/${ids.library}/stats`)
  add('users.online', R, '/api/users/online')
  add('auth.refresh', null, '/auth/refresh', { method: 'POST', headers: ctx => ({ 'x-refresh-token': ctx.listenerRefresh }) })
  return s
}

const UUID = /[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}/g
const JWT = /eyJ[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+/g
let WORK_DIR = ''
const TIME_KEY = /(At|Time|Update|Seen|Scan|^date|^dayOfWeek|Ms|token|Token)$/

function normalize(value, known, fresh, key = '') {
  if (typeof value === 'string') {
    let v = value.replace(JWT, '<jwt>')
    if (WORK_DIR) v = v.split(WORK_DIR).join('<work>')
    v = v.replace(UUID, m => known.has(m) ? m : '<new-uuid>')
    v = v.replace(/play_local_[a-z0-9]+/g, m => m)
    if (/^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}/.test(v)) return '<iso>'
    if (/(At|Time|Update|Seen|Scan)$/.test(key) && /^\d{10,}$/.test(v)) return '<ts>'
    return v
  }
  if (typeof value === 'number') {
    const now = Date.now()
    if (Math.abs(value - now) < 7 * 86400000) return '<now-ms>'
    if (/(lastUpdate|startedAt|updatedAt|createdAt|addedAt|finishedAt|lastSeen|lastScan|birthtimeMs|mtimeMs|ctimeMs)$/.test(key)) return '<ts>'
    return value
  }
  if (Array.isArray(value)) return value.map(v => normalize(v, known, fresh, key))
  if (value && typeof value === 'object') {
    const out = {}
    for (const k of Object.keys(value).sort()) out[k] = normalize(value[k], known, fresh, k)
    return out
  }
  return value
}

async function runJourney(server, ids, logDir) {
  const call = api(server)
  const ctx = {}
  const tokens = {}
  const results = {}
  const known = snapshotUUIDs(ids)
  const fresh = new Map()
  const resolve = (v) => typeof v === 'function' ? v(ctx) : v
  for (const step of journeySteps(ids)) {
    if (step.skip) continue
    let route
    try { route = resolve(step.route) } catch (e) { results[step.id] = { error: `route unresolved: ${e.message}` }; continue }
    const token = step.who ? tokens[step.who] : undefined
    if (step.tokenQuery && token && !/[?&]token=/.test(route)) route += (route.includes('?') ? '&' : '?') + `token=${token}`
    let body, headers
    try { body = resolve(step.body); headers = resolve(step.headers) || {} } catch (e) { results[step.id] = { error: `body unresolved: ${e.message}` }; continue }
    let r
    for (let attempt = 0; attempt <= (step.retry || 0); attempt++) {
      try {
        r = await requestJSON(server.baseUrl, route, { method: step.method || 'GET', token: step.tokenQuery ? undefined : token, body, headers, timeoutMs: 30000 })
      } catch (e) { r = { status: 0, value: e.message, bytes: Buffer.alloc(0), headers: new Headers() } }
      if (r.status !== 404 && r.status !== 0) break
      await delay(500)
    }
    // Normalization: the "discover" shelf is ORDER BY RANDOM() upstream
    // (libraryItemsBookFilters.getDiscoverLibraryItems), so compare its
    // entity set, not its order.
    if (Array.isArray(r.value) && step.route.toString().includes('/personalized')) {
      for (const shelf of r.value) if (shelf?.id === 'discover' && Array.isArray(shelf.entities)) shelf.entities.sort((a, b) => String(a.id).localeCompare(String(b.id)))
    }
    if (step.capture && r.ok) ctx[step.capture] = r.value
    if (step.id === 'login.listener.mobile' && r.ok) {
      tokens.listener = r.value.user.accessToken
      ctx.listenerRefresh = r.value.user.refreshToken
      ctx.listenerId = r.value.user.id
      known.add(ctx.listenerId)
    }
    if (step.id === 'login.root' && r.ok) tokens.root = r.value.user.accessToken
    if (step.id === 'login.finisher' && r.ok) tokens.finisher = r.value.user.accessToken
    let recorded
    if (step.binary === 'bytes') recorded = { len: r.bytes.length, sha: require('node:crypto').createHash('sha256').update(r.bytes).digest('hex'), type: r.headers.get('content-type'), range: r.headers.get('content-range') }
    else if (step.binary === 'meta') recorded = { type: r.headers.get('content-type'), nonEmpty: r.bytes.length > 0 }
    else if (step.binary === 'text') recorded = { type: r.headers.get('content-type'), text: typeof r.value === 'string' ? r.value.replace(UUID, '<uuid>').replace(/[?&]token=[^\s&]+/g, '') : r.value }
    else recorded = normalize(r.value, known, fresh)
    results[step.id] = { status: r.status, body: recorded }
  }
  fs.writeFileSync(path.join(logDir, `journey-${server.kind}.json`), JSON.stringify(results, null, 2))
  return results
}

// Realtime: a second device must receive progress updates made by the first.
async function socketProgressProbe(server, ids, listenerToken) {
  const io = loadSocketIOClient()
  if (!io) return { skipped: 'socket.io-client unavailable' }
  return new Promise((resolve) => {
    const socket = io(server.baseUrl, { path: `${BASE}/socket.io`, transports: ['websocket'], reconnection: false, timeout: 8000 })
    const got = []
    const done = (r) => { clearTimeout(t); socket.disconnect(); resolve(r) }
    const t = setTimeout(() => done({ status: 'timeout', got }), 10000)
    socket.on('connect', () => socket.emit('auth', listenerToken))
    socket.on('init', async () => {
      await requestJSON(server.baseUrl, `/api/me/progress/${ids.saga2}`, { method: 'PATCH', token: listenerToken, body: { currentTime: 20, duration: 30, progress: 0.66 } })
    })
    socket.on('user_updated', (u) => {
      const mp = (u?.mediaProgress || []).find(p => p.libraryItemId === ids.saga2)
      got.push(u)
      done({ status: 'ok', event: 'user_updated', currentTime: mp?.currentTime, progress: mp?.progress })
    })
  })
}

// Every UUID that already exists in the installation is stable across the
// swap; only UUIDs created during the journey are normalized.
let SNAPSHOT_DB = ''
function snapshotUUIDs(ids) {
  const set = new Set(Object.values(ids))
  if (SNAPSHOT_DB) for (const m of fs.readFileSync(SNAPSHOT_DB, 'latin1').match(UUID) || []) set.add(m)
  return set
}

function diffResults(a, b) {
  const diffs = []
  for (const id of new Set([...Object.keys(a), ...Object.keys(b)])) {
    const x = JSON.stringify(a[id]); const y = JSON.stringify(b[id])
    if (x !== y) diffs.push(id)
  }
  return diffs
}

function diffPaths(x, y, p = '', out = []) {
  if (out.length >= 12 || JSON.stringify(x) === JSON.stringify(y)) return out
  if (x && y && typeof x === 'object' && typeof y === 'object' && Array.isArray(x) === Array.isArray(y)) {
    for (const k of new Set([...Object.keys(x), ...Object.keys(y)])) diffPaths(x[k], y[k], `${p}.${k}`, out)
    return out
  }
  out.push(`${p}: upstream=${JSON.stringify(x)?.slice(0, 160)} go=${JSON.stringify(y)?.slice(0, 160)}`)
  return out
}

function copyTree(from, to) {
  fs.rmSync(to, { recursive: true, force: true })
  fs.cpSync(from, to, { recursive: true, preserveTimestamps: true })
}

async function main() {
  const args = parseArgs()
  const work = path.resolve(args.work || fs.mkdtempSync(path.join(os.tmpdir(), 'abs-replacement-')))
  const goBin = path.resolve(args['go-bin'] || path.join(ROOT, 'go-backend', 'audiobookshelf-go'))
  fs.mkdirSync(work, { recursive: true })
  console.log(`work dir: ${work}`)
  if (!args['go-bin']) sh('go', ['build', '-o', goBin, './cmd/audiobookshelf-go'], { cwd: path.join(ROOT, 'go-backend'), env: { ...process.env, CGO_ENABLED: '0' } })

  WORK_DIR = work
  const snapshot = path.join(work, 'snapshot')
  let dirs, ids
  if (args.reuse && fs.existsSync(path.join(work, 'ids.json'))) {
    ids = JSON.parse(fs.readFileSync(path.join(work, 'ids.json'), 'utf8'))
    dirs = { config: path.join(work, 'install', 'config'), metadata: path.join(work, 'install', 'metadata') }
    copyTree(snapshot, path.join(work, 'install'))
  } else {
    const booksRoot = generateMedia(path.join(work, 'media'))
    ;({ dirs, ids } = await seed(work, booksRoot))
    copyTree(path.join(work, 'install'), snapshot)
    fs.writeFileSync(path.join(work, 'ids.json'), JSON.stringify(ids, null, 2))
  }
  SNAPSHOT_DB = path.join(snapshot, 'config', 'absdatabase.sqlite')

  // Upstream journey (reference), in place.
  let up = await startUpstream(dirs, await freePort(), path.join(work, 'journey-upstream.log'))
  const upstream = await runJourney(up, ids, work)
  const upLogin = await loginAs(api(up), LISTENER)
  const upSocket = await socketProgressProbe(up, ids, upLogin.accessToken)
  await stop(up)

  // Swap: restore the pre-journey installation to the same paths, boot Go.
  copyTree(snapshot, path.join(work, 'install'))
  const go = await startGo(goBin, dirs, await freePort(), path.join(work, 'journey-go.log'))
  const goResults = await runJourney(go, ids, work)
  let goSocket
  try { goSocket = await socketProgressProbe(go, ids, (await loginAs(api(go), LISTENER)).accessToken) } catch (e) { goSocket = { status: 'error', error: e.message } }
  const goProgress = await requestJSON(go.baseUrl, '/api/me', { token: (await loginAs(api(go), LISTENER)).accessToken })
  await stop(go)

  // Rollback: upstream reopens the Go-written installation.
  let rollback
  try {
    up = await startUpstream(dirs, await freePort(), path.join(work, 'rollback-upstream.log'))
    const lt = (await loginAs(api(up), LISTENER)).accessToken
    const me = await requestJSON(up.baseUrl, '/api/me', { token: lt })
    const sessions = await requestJSON(up.baseUrl, '/api/me/listening-sessions?itemsPerPage=50&page=0', { token: lt })
    const pick = (u) => (u?.mediaProgress || []).map(p => ({ id: p.libraryItemId, t: p.currentTime, f: p.isFinished, e: p.ebookLocation, h: p.hideFromContinueListening })).sort((a, b) => a.id.localeCompare(b.id))
    const goView = pick(goProgress.value)
    const upView = pick(me.value)
    rollback = {
      booted: true,
      progressMatches: JSON.stringify(goView) === JSON.stringify(upView),
      bookmarksMatch: JSON.stringify(goProgress.value?.bookmarks?.map(b => [b.libraryItemId, b.time, b.title])) === JSON.stringify(me.value?.bookmarks?.map(b => [b.libraryItemId, b.time, b.title])),
      sessionCount: sessions.value?.total, goView, upView
    }
  } catch (e) {
    rollback = { booted: false, error: e.message }
  } finally {
    await stop(up)
  }

  const diffs = diffResults(upstream, goResults)
  const report = {
    steps: Object.keys(upstream).length,
    matched: Object.keys(upstream).length - diffs.length,
    differing: diffs.map(id => ({ id, upstreamStatus: upstream[id]?.status, goStatus: goResults[id]?.status, diffs: diffPaths(upstream[id], goResults[id]) })),
    socket: { upstream: upSocket, go: goSocket },
    rollback
  }
  fs.writeFileSync(path.join(work, 'replacement-report.json'), JSON.stringify(report, null, 2))
  console.log(JSON.stringify(report, null, 2))
  const socketOK = JSON.stringify(upSocket) === JSON.stringify(goSocket)
  const ok = diffs.length === 0 && rollback.booted && rollback.progressMatches && rollback.bookmarksMatch && socketOK
  console.log(`\nreplacement trial: ${report.matched}/${report.steps} journey steps match; socket ${socketOK ? 'ok' : 'DIFFERS'}; rollback ${rollback.booted && rollback.progressMatches && rollback.bookmarksMatch ? 'ok' : 'FAILED'}`)
  if (!ok) process.exitCode = 1
}

main().catch((error) => { console.error(error); process.exitCode = 1 })
