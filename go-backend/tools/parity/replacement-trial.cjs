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
const { startImageSource } = require('./lib/local-fixtures.cjs')

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

  // 5. A podcast folder with three locally generated episodes (no RSS, no
  // network). Upstream discovers episodes from the audio files in the folder
  // (PodcastScanner.scanNewPodcastLibraryItem, server/scanner/PodcastScanner.js)
  // and maps ID3 comment/subtitle/date/disc/track/title to
  // description/subtitle/pubDate/season/episode/title
  // (AudioFileScanner.setPodcastEpisodeMetadataFromAudioMetaTags).
  const cast = path.join(mediaRoot, 'podcasts', 'Fixture Cast')
  fs.mkdirSync(cast, { recursive: true })
  const episodes = [
    { n: 1, title: 'Pilot Episode', date: '2025-03-01', dur: 24, freq: 330, desc: 'The first episode of the fixture cast.' },
    { n: 2, title: 'Second Outing', date: '2025-03-08', dur: 18, freq: 440, desc: 'A <b>second</b> episode.' },
    { n: 3, title: 'Third Time', date: '2025-03-15', dur: 12, freq: 550, desc: 'Episode three closes the season.' }
  ]
  for (const e of episodes) {
    ffmpeg(['-f', 'lavfi', '-i', `sine=frequency=${e.freq}:duration=${e.dur}`, '-ac', '1', '-ar', '22050', '-b:a', '32k',
      '-metadata', `title=${e.title}`, '-metadata', 'artist=Fixture Host', '-metadata', 'album=Fixture Cast',
      '-metadata', `track=${e.n}`, '-metadata', 'disc=1', '-metadata', `date=${e.date}`,
      '-metadata', `comment=${e.desc}`, '-metadata', 'genre=Technology',
      '-id3v2_version', '3', path.join(cast, `episode-${e.n}.mp3`)])
  }
  ffmpeg(['-f', 'lavfi', '-i', 'color=c=teal:s=500x500', '-frames:v', '1', path.join(cast, 'cover.jpg')])
  return { books, podcasts: path.join(mediaRoot, 'podcasts') }
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

async function seed(work, media) {
  const booksRoot = media.books
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

    // Podcast library: the folder already exists on disk, so the scan creates
    // the podcast item and its three episodes without any RSS fetch.
    const plib = await must(call('/api/libraries', {
      method: 'POST', token: rt,
      body: { name: 'Podcasts', mediaType: 'podcast', provider: 'itunes', folders: [{ fullPath: media.podcasts }] }
    }), 'create podcast library')
    await must(call(`/api/libraries/${plib.id}/scan`, { method: 'POST', token: rt, body: {} }), 'scan podcasts')
    let pitems = []
    for (let i = 0; i < 120 && pitems.length < 1; i++) {
      await delay(500)
      pitems = (await must(call(`/api/libraries/${plib.id}/items?limit=50`, { token: rt }), 'podcast items')).results
    }
    if (pitems.length !== 1) throw new Error(`upstream scan produced ${pitems.length} podcasts, expected 1`)
    ids.podcastLibrary = plib.id
    ids.podcast = pitems[0].id
    const pfull = await must(call(`/api/items/${ids.podcast}?expanded=1`, { token: rt }), 'podcast item')
    const epByTitle = Object.fromEntries(pfull.media.episodes.map(e => [e.title, e.id]))
    ids.ep1 = epByTitle['Pilot Episode']
    ids.ep2 = epByTitle['Second Outing']
    ids.ep3 = epByTitle['Third Time']
    for (const k of ['ep1', 'ep2', 'ep3']) if (!ids[k]) throw new Error(`seed: no episode ${k}; titles=${Object.keys(epByTitle)}`)

    // Android listens to episode 1 (streaming), episode 2 is marked finished,
    // an iOS offline session is uploaded for episode 3, plus a bookmark and a
    // playlist holding episodes.
    const ps = await must(call(`/api/items/${ids.podcast}/play/${ids.ep1}`, { method: 'POST', token: lt, body: {
      deviceInfo: ANDROID_DEVICE, mediaPlayer: 'exo-player', forceDirectPlay: true, forceTranscode: false, supportedMimeTypes: MOBILE_MIME } }), 'seed podcast play')
    await must(call(`/api/session/${ps.id}/sync`, { method: 'POST', token: lt, body: { currentTime: 8, timeListened: 8, duration: ps.duration } }), 'seed podcast sync')
    await must(call(`/api/session/${ps.id}/close`, { method: 'POST', token: lt, body: { currentTime: 10.5, timeListened: 2.5, duration: ps.duration } }), 'seed podcast close')
    await must(call(`/api/me/progress/${ids.podcast}/${ids.ep2}`, { method: 'PATCH', token: lt, body: { isFinished: true } }), 'finish episode 2')
    const plocal = {
      id: 'play_local_trial0003', userId: ids.listener, libraryId: ids.podcastLibrary, libraryItemId: ids.podcast, episodeId: ids.ep3,
      mediaType: 'podcast', mediaMetadata: {}, chapters: [], displayTitle: 'Third Time', displayAuthor: 'Fixture Host',
      coverPath: null, duration: 12, playMethod: 3, mediaPlayer: 'AVPlayer', deviceInfo: IOS_DEVICE,
      serverVersion: '2.37.2', date: new Date(now - 43200000).toISOString().slice(0, 10), dayOfWeek: 'Sunday',
      timeListening: 6, startTime: 0, currentTime: 6, startedAt: now - 43200000, updatedAt: now - 43200000 + 6000
    }
    await must(call('/api/session/local-all', { method: 'POST', token: lt, body: { deviceInfo: IOS_DEVICE, sessions: [plocal] } }), 'local-all podcast')
    await must(call(`/api/me/item/${ids.podcast}/bookmark`, { method: 'POST', token: lt, body: { time: 5, title: 'Cast bookmark' } }), 'podcast bookmark')
    const ppl = await must(call('/api/playlists', { method: 'POST', token: lt, body: { libraryId: ids.podcastLibrary, name: 'Cast queue',
      items: [{ libraryItemId: ids.podcast, episodeId: ids.ep3 }, { libraryItemId: ids.podcast, episodeId: ids.ep1 }] } }), 'podcast playlist')
    ids.podcastPlaylist = ppl.id
    // An open RSS feed on the podcast, so include=rssfeed has something to report.
    await must(call(`/api/feeds/item/${ids.podcast}/open`, { method: 'POST', token: rt, body: { serverAddress: 'http://trial.invalid', slug: 'fixture-cast-feed', metadataDetails: { preventIndexing: true } } }), 'open podcast feed')
    // Open RSS feeds (saga1, novel) and media item shares (saga2, novel), for
    // the include=rssfeed / include=share steps at the end of the journey.
    for (const k of ['saga1', 'novel']) {
      await must(call(`/api/feeds/item/${ids[k]}/open`, { method: 'POST', token: rt, body: {
        serverAddress: 'http://trial.invalid', slug: `trial-feed-${k}`, metadataDetails: { preventIndexing: true, ownerName: 'Trial Owner', ownerEmail: 'owner@example.com' } } }), `open feed ${k}`)
    }
    for (const k of ['saga2', 'novel']) {
      const item = await must(call(`/api/items/${ids[k]}?expanded=1`, { token: rt }), `item ${k}`)
      await must(call('/api/share/mediaitem', { method: 'POST', token: rt, body: {
        slug: `trial-share-${k}`, mediaItemType: 'book', mediaItemId: item.media.id, expiresAt: 0, isDownloadable: k === 'novel' } }), `share ${k}`)
    }
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

  // ---- Podcasts: what the mobile apps and web client call for a podcast
  // library (local folder; no RSS/iTunes network access).
  const PL = ids.podcastLibrary
  const b64 = (v) => Buffer.from(v).toString('base64')
  add('plib.get', L, `/api/libraries/${PL}`)
  add('plib.filterdata', L, `/api/libraries/${PL}?include=filterdata`)
  add('plib.personalized', L, `/api/libraries/${PL}/personalized?minified=1&include=rssfeed,numEpisodesIncomplete,share`)
  add('plib.personalized.full', L, `/api/libraries/${PL}/personalized?limit=5`)
  add('plib.items.title', L, `/api/libraries/${PL}/items?limit=20&page=0&minified=1&sort=media.metadata.title&desc=0&include=rssfeed,numEpisodesIncomplete`)
  add('plib.items.full', L, `/api/libraries/${PL}/items?limit=20&page=0`)
  add('plib.items.added', L, `/api/libraries/${PL}/items?limit=20&page=0&minified=1&sort=addedAt&desc=1&include=numEpisodesIncomplete`)
  add('plib.items.numtracks', L, `/api/libraries/${PL}/items?limit=20&minified=1&sort=media.numTracks&desc=1`)
  add('plib.items.author', L, `/api/libraries/${PL}/items?limit=20&minified=1&sort=media.metadata.author`)
  add('plib.items.filter.inprogress', L, `/api/libraries/${PL}/items?limit=20&minified=1&filter=progress.${b64('in-progress')}&include=numEpisodesIncomplete`)
  add('plib.items.filter.finished', L, `/api/libraries/${PL}/items?limit=20&minified=1&filter=progress.${b64('finished')}`)
  add('plib.items.filter.notstarted', L, `/api/libraries/${PL}/items?limit=20&minified=1&filter=progress.${b64('not-started')}`)
  add('plib.items.filter.genre', L, `/api/libraries/${PL}/items?limit=20&minified=1&filter=genres.${b64('Technology')}`)
  add('plib.items.filter.explicit', L, `/api/libraries/${PL}/items?limit=20&minified=1&filter=explicit.${b64('explicit')}`)
  add('plib.items.filter.explicit.bare', L, `/api/libraries/${PL}/items?limit=20&minified=1&filter=explicit`)
  add('plib.items.filter.language', L, `/api/libraries/${PL}/items?limit=20&minified=1&filter=languages.${b64('en')}`)
  add('plib.items.filter.recent', L, `/api/libraries/${PL}/items?limit=20&minified=1&filter=recent&sort=media.duration`)
  add('plib.recent-episodes', L, `/api/libraries/${PL}/recent-episodes?limit=25&page=0`)
  add('plib.recent-episodes.page2', L, `/api/libraries/${PL}/recent-episodes?limit=1&page=1`)
  add('plib.podcast-titles', L, `/api/libraries/${PL}/podcast-titles`)
  add('plib.search', L, `/api/libraries/${PL}/search?q=fixture&limit=12`)
  add('plib.search.episode', L, `/api/libraries/${PL}/search?q=pilot&limit=12`)
  add('plib.search.genre', L, `/api/libraries/${PL}/search?q=technology&limit=12`)
  add('plib.search.none', L, `/api/libraries/${PL}/search?q=zzzznothing&limit=12`)
  add('plib.playlists', L, `/api/libraries/${PL}/playlists?minified=1`)
  add('plib.playlist', L, `/api/playlists/${ids.podcastPlaylist}`)
  add('plib.series', L, `/api/libraries/${PL}/series?limit=20&page=0`)
  add('plib.authors', L, `/api/libraries/${PL}/authors`)
  add('plib.collections', L, `/api/libraries/${PL}/collections`)
  add('plib.episode-downloads', L, `/api/libraries/${PL}/episode-downloads`)
  add('podcast.item', L, `/api/items/${ids.podcast}`)
  add('podcast.item.expanded', L, `/api/items/${ids.podcast}?expanded=1&include=progress,rssfeed,downloads`)
  add('podcast.item.expanded.ep1', L, `/api/items/${ids.podcast}?expanded=1&include=progress,rssfeed,downloads&episode=${ids.ep1}`)
  add('podcast.item.expanded.ep2', L, `/api/items/${ids.podcast}?expanded=1&include=progress&episode=${ids.ep2}`)
  add('podcast.item.expanded.ep3', L, `/api/items/${ids.podcast}?expanded=1&include=progress&episode=${ids.ep3}`)
  add('podcast.item.expanded.epmissing', L, `/api/items/${ids.podcast}?expanded=1&include=progress&episode=00000000-0000-4000-8000-000000000000`)
  add('podcast.item.minified', L, `/api/items/${ids.podcast}?minified=1`)
  add('podcast.cover', L, `/api/items/${ids.podcast}/cover`, { binary: 'meta' })
  add('podcast.cover.thumb', L, `/api/items/${ids.podcast}/cover?width=400&format=jpeg`, { binary: 'meta' })
  add('podcast.cover.raw', L, `/api/items/${ids.podcast}/cover?raw=1`, { binary: 'bytes' })
  add('podcast.batch', L, '/api/items/batch/get', { method: 'POST', body: { libraryItemIds: [ids.podcast, ids.saga1] } })
  add('podcast.downloads', L, `/api/podcasts/${ids.podcast}/downloads`)
  add('podcast.episode.get', L, `/api/podcasts/${ids.podcast}/episode/${ids.ep1}`)
  add('podcast.episode.missing', L, `/api/podcasts/${ids.podcast}/episode/00000000-0000-4000-8000-000000000000`)
  add('podcast.checknew.forbidden', L, `/api/podcasts/${ids.podcast}/checknew`)
  add('podcast.searchepisode.nofeed', L, `/api/podcasts/${ids.podcast}/search-episode?title=pilot`)
  add('podcast.notpodcast', L, `/api/podcasts/${ids.saga1}/downloads`)
  add('podcast.progress.ep1', L, `/api/me/progress/${ids.podcast}/${ids.ep1}`)
  add('podcast.progress.ep2', L, `/api/me/progress/${ids.podcast}/${ids.ep2}`)
  add('podcast.progress.ep3', L, `/api/me/progress/${ids.podcast}/${ids.ep3}`)
  add('podcast.progress.noepisode', L, `/api/me/progress/${ids.podcast}`)
  add('podcast.itemsessions.ep1', L, `/api/me/item/listening-sessions/${ids.podcast}/${ids.ep1}`)
  add('podcast.itemsessions.item', L, `/api/me/item/listening-sessions/${ids.podcast}`)

  // Episode playback: Android direct play resumes at 10.5s, then a web
  // player direct play of episode 3, then forced HLS transcode of episode 2.
  add('play.ep.android', L, `/api/items/${ids.podcast}/play/${ids.ep1}`, { method: 'POST', capture: 'epSession', body: {
    deviceInfo: ANDROID_DEVICE, mediaPlayer: 'exo-player', forceDirectPlay: true, forceTranscode: false, supportedMimeTypes: MOBILE_MIME } })
  add('stream.ep.range', L, ctx => ctx.epSession.audioTracks[0].contentUrl, { headers: { Range: 'bytes=0-4095' }, binary: 'bytes', tokenQuery: true })
  add('stream.ep.suffix', L, ctx => ctx.epSession.audioTracks[0].contentUrl, { headers: { Range: 'bytes=-1024' }, binary: 'bytes', tokenQuery: true })
  add('stream.ep.full', L, ctx => ctx.epSession.audioTracks[0].contentUrl, { binary: 'bytes', tokenQuery: true })
  add('sync.ep.1', L, ctx => `/api/session/${ctx.epSession.id}/sync`, { method: 'POST', body: ctx => ({ currentTime: 15, timeListened: 4.5, duration: ctx.epSession.duration }) })
  add('podcast.progress.ep1.after.sync', L, `/api/me/progress/${ids.podcast}/${ids.ep1}`)
  add('close.ep', L, ctx => `/api/session/${ctx.epSession.id}/close`, { method: 'POST', body: ctx => ({ currentTime: 20, timeListened: 5, duration: ctx.epSession.duration }) })
  add('podcast.progress.ep1.after.close', L, `/api/me/progress/${ids.podcast}/${ids.ep1}`)
  add('play.ep.web', L, `/api/items/${ids.podcast}/play/${ids.ep3}`, { method: 'POST', capture: 'epWebSession', body: {
    deviceInfo: { clientName: 'Abs Web', clientVersion: '2.37.2' }, mediaPlayer: 'html5', forceDirectPlay: false, forceTranscode: false,
    supportedMimeTypes: ['audio/flac', 'audio/mpeg', 'audio/mp4', 'audio/ogg', 'audio/aac', 'audio/webm'] } })
  add('stream.epweb.range', L, ctx => ctx.epWebSession.audioTracks[0].contentUrl, { headers: { Range: 'bytes=100-2047' }, binary: 'bytes', tokenQuery: true })
  add('close.epweb', L, ctx => `/api/session/${ctx.epWebSession.id}/close`, { method: 'POST', body: { currentTime: 7, timeListened: 1 } })
  add('play.ep.hls', L, `/api/items/${ids.podcast}/play/${ids.ep2}`, { method: 'POST', capture: 'epHlsSession', body: {
    deviceInfo: { clientName: 'Abs Web', clientVersion: '2.37.2' }, mediaPlayer: 'html5', forceDirectPlay: false, forceTranscode: true, supportedMimeTypes: ['audio/mpeg'] } })
  add('hls.ep.playlist', L, ctx => ctx.epHlsSession.audioTracks[0].contentUrl, { binary: 'text', tokenQuery: true, retry: 20 })
  add('close.ep.hls', L, ctx => `/api/session/${ctx.epHlsSession.id}/close`, { method: 'POST', body: { currentTime: 3, timeListened: 3 } })
  add('play.ep.missing', L, `/api/items/${ids.podcast}/play/00000000-0000-4000-8000-000000000000`, { method: 'POST', body: { deviceInfo: ANDROID_DEVICE, supportedMimeTypes: MOBILE_MIME } })
  // POST /play without an episodeId on a podcast makes upstream open a bogus HLS stream (no client sends it), so it is not exercised.

  // Offline mobile listening for an episode, batch progress with episodeId,
  // per-episode progress writes, bookmark on the podcast item.
  add('session.local.ep', L, '/api/session/local', { method: 'POST', body: ctx => ({
    id: 'play_local_trial0004', userId: ctx.listenerId, libraryId: ids.podcastLibrary, libraryItemId: ids.podcast, episodeId: ids.ep1,
    mediaType: 'podcast', mediaMetadata: {}, chapters: [], displayTitle: 'Pilot Episode', displayAuthor: 'Fixture Host', coverPath: null,
    duration: 24, playMethod: 3, mediaPlayer: 'exo-player', deviceInfo: ANDROID_DEVICE, serverVersion: '2.37.2',
    date: '2026-01-06', dayOfWeek: 'Tuesday', timeListening: 8, startTime: 20, currentTime: 22,
    startedAt: 1767686400000, updatedAt: 1767686408000 }) })
  add('podcast.progress.ep1.after.local', L, `/api/me/progress/${ids.podcast}/${ids.ep1}`)
  add('progress.batch.ep', L, '/api/me/progress/batch/update', { method: 'PATCH', body: [
    { libraryItemId: ids.podcast, episodeId: ids.ep3, currentTime: 9, duration: 12, progress: 0.75 },
    { libraryItemId: ids.podcast, episodeId: ids.ep2, isFinished: false, currentTime: 2, duration: 18 }] })
  add('podcast.progress.ep2.after.batch', L, `/api/me/progress/${ids.podcast}/${ids.ep2}`)
  add('progress.ep3.finish', L, `/api/me/progress/${ids.podcast}/${ids.ep3}`, { method: 'PATCH', body: { isFinished: true } })
  add('progress.ep3.after.finish', L, `/api/me/progress/${ids.podcast}/${ids.ep3}`)
  add('progress.ep3.unfinish', L, `/api/me/progress/${ids.podcast}/${ids.ep3}`, { method: 'PATCH', body: { isFinished: false, currentTime: 4 } })
  add('progress.ep.missing', L, `/api/me/progress/${ids.podcast}/00000000-0000-4000-8000-000000000000`, { method: 'PATCH', body: { currentTime: 1 } })
  add('podcast.bookmark.create', L, `/api/me/item/${ids.podcast}/bookmark`, { method: 'POST', body: { time: 9, title: 'Cast second' } })
  add('podcast.bookmark.delete', L, `/api/me/item/${ids.podcast}/bookmark/9`, { method: 'DELETE' })
  add('podcast.playlist.add', L, `/api/playlists/${ids.podcastPlaylist}/item`, { method: 'POST', body: { libraryItemId: ids.podcast, episodeId: ids.ep2 } })
  add('podcast.playlist.remove', L, `/api/playlists/${ids.podcastPlaylist}/item/${ids.podcast}/${ids.ep3}`, { method: 'DELETE' })
  add('podcast.playlist.after', L, `/api/playlists/${ids.podcastPlaylist}`)
  add('plib.personalized.after', L, `/api/libraries/${PL}/personalized?minified=1&include=rssfeed,numEpisodesIncomplete,share`)
  add('plib.items.after', L, `/api/libraries/${PL}/items?limit=20&page=0&minified=1&include=numEpisodesIncomplete`)
  add('podcast.items-in-progress.after', L, '/api/me/items-in-progress')
  add('podcast.me.after', L, '/api/me')
  add('podcast.listening-sessions.after', L, '/api/me/listening-sessions?itemsPerPage=10&page=0')
  add('podcast.listening-stats.after', L, '/api/me/listening-stats')

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
  add('plib.stats', R, `/api/libraries/${ids.podcastLibrary}/stats`)
  add('plib.get.root', R, `/api/libraries/${ids.podcastLibrary}?include=filterdata`)
  add('plib.items.root', R, `/api/libraries/${ids.podcastLibrary}/items?limit=20&page=0&minified=1&include=rssfeed,numEpisodesIncomplete`)
  add('plib.recent-episodes.root', R, `/api/libraries/${ids.podcastLibrary}/recent-episodes?limit=25&page=0`)
  add('podcast.item.root', R, `/api/items/${ids.podcast}?expanded=1&include=progress,rssfeed,downloads`)
  add('podcast.checknew.nofeed', R, `/api/podcasts/${ids.podcast}/checknew`)
  add('podcast.searchepisode.nofeed.root', R, `/api/podcasts/${ids.podcast}/search-episode?title=pilot`)
  add('podcast.searchepisode.notitle', R, `/api/podcasts/${ids.podcast}/search-episode`)
  add('podcast.clearqueue', R, `/api/podcasts/${ids.podcast}/clear-queue`)
  add('podcast.downloadepisodes.empty', R, `/api/podcasts/${ids.podcast}/download-episodes`, { method: 'POST', body: [] })
  // match-episodes without a feedURL crashes upstream (podcastUtils.getPodcastFeed reads null.startsWith, unhandled rejection), so it is not exercised.
  add('podcast.opml.parse', R, '/api/podcasts/opml/parse', { method: 'POST', body: { opmlText: '<?xml version="1.0"?><opml version="2.0"><body><outline text="One" type="rss" xmlUrl="https://example.invalid/one.xml"/><outline text="Two" type="rss" xmlUrl="https://example.invalid/two.xml"/></body></opml>' } })
  add('podcast.opml.parse.empty', R, '/api/podcasts/opml/parse', { method: 'POST', body: {} })
  add('podcast.feed.invalid', R, '/api/podcasts/feed', { method: 'POST', body: { rssFeed: 'not a url' } })
  add('podcast.create.invalid', R, '/api/podcasts', { method: 'POST', body: { libraryId: ids.podcastLibrary } })
  add('podcast.episode.update', R, `/api/podcasts/${ids.podcast}/episode/${ids.ep1}`, { method: 'PATCH', body: {
    title: 'Pilot Episode (edited)', subtitle: 'Sub <b>line</b>', description: '<p>Edited <script>alert(1)</script>text</p>', season: '2', episode: '5', episodeType: 'bonus',
    pubDate: 'Sat, 01 Mar 2025 12:00:00 GMT', publishedAt: 1740830400000, ignored: 'x' } })
  add('podcast.episode.after', R, `/api/podcasts/${ids.podcast}/episode/${ids.ep1}`)
  add('podcast.episode.update.missing', R, `/api/podcasts/${ids.podcast}/episode/00000000-0000-4000-8000-000000000000`, { method: 'PATCH', body: { title: 'x' } })
  add('podcast.media.update', R, `/api/items/${ids.podcast}/media`, { method: 'PATCH', body: {
    metadata: { title: 'Fixture Cast Renamed', author: 'Fixture Host', description: 'Edited podcast', language: 'en', explicit: true, genres: ['Technology', 'News'] },
    tags: ['cast', 'trial'], autoDownloadEpisodes: false, autoDownloadSchedule: '30 * * * *', maxEpisodesToKeep: 3, maxNewEpisodesToDownload: 2 } })
  add('podcast.media.update.badcron', R, `/api/items/${ids.podcast}/media`, { method: 'PATCH', body: { autoDownloadSchedule: 'nope' } })
  add('podcast.item.after.update', R, `/api/items/${ids.podcast}?expanded=1&include=progress,rssfeed,downloads`)
  add('plib.items.after.update', R, `/api/libraries/${ids.podcastLibrary}/items?limit=20&page=0&minified=1&include=numEpisodesIncomplete`)
  add('plib.search.renamed', R, `/api/libraries/${ids.podcastLibrary}/search?q=renamed&limit=12`)
  add('podcast.episode.delete', R, `/api/podcasts/${ids.podcast}/episode/${ids.ep3}`, { method: 'DELETE' })
  add('podcast.episode.delete.again', R, `/api/podcasts/${ids.podcast}/episode/${ids.ep3}`, { method: 'DELETE' })
  add('podcast.item.after.delete', R, `/api/items/${ids.podcast}?expanded=1&include=progress`)
  add('plib.recent-episodes.after.delete', R, `/api/libraries/${ids.podcastLibrary}/recent-episodes?limit=25&page=0`)
  add('podcast.playlist.after.delete', R, `/api/playlists/${ids.podcastPlaylist}`)
  add('user.listener.after.podcast', R, ctx => `/api/users/${ctx.listenerId}`)
  add('auth.refresh', null, '/auth/refresh', { method: 'POST', headers: ctx => ({ 'x-refresh-token': ctx.listenerRefresh }) })

  // Playlist ordering: Playlist.getOldPlaylistsForUserAndLibrary sorts by
  // name.localeCompare (server/models/Playlist.js:85), so mixed case, accents,
  // digits and punctuation must interleave the way ICU orders them.
  const playlistNames = ['b', 'B', 'a', 'A', 'Á', 'á', 'Éclair', 'eclair', '10', '9', '_x', 'Zoe', 'zoe']
  playlistNames.forEach((name, i) => add(`playlist.order.create.${i}`, L, '/api/playlists', { method: 'POST', body: { libraryId: ids.library, name } }))
  add('playlist.order.all', L, '/api/playlists')
  add('playlist.order.library', L, `/api/libraries/${ids.library}/playlists?minified=1`)
  add('playlist.order.library.page', L, `/api/libraries/${ids.library}/playlists?limit=5&page=1`)

  // Collapsed sub-series: a series-filtered page with collapseseries=1 goes
  // through libraryHelpers.handleCollapseSubseries
  // (server/controllers/LibraryController.js:625-633). Build "Mega Cycle"
  // (saga1=1, saga2=2, novel=3, ebook without sequence) around the existing
  // "Long Saga" (saga1=1, saga2=2), so Long Saga collapses inside it.
  const setSeries = (id, key, series) => add(id, R, `/api/items/${ids[key]}/media`, { method: 'PATCH', body: { metadata: { series } } })
  setSeries('subseries.setup.saga1', 'saga1', [{ name: 'Long Saga', sequence: '1' }, { name: 'Mega Cycle', sequence: '1' }])
  setSeries('subseries.setup.saga2', 'saga2', [{ name: 'Long Saga', sequence: '2' }, { name: 'Mega Cycle', sequence: '2' }])
  setSeries('subseries.setup.novel', 'novel', [{ name: 'Mega Cycle', sequence: '3' }])
  setSeries('subseries.setup.ebook', 'ebook', [{ name: 'Mega Cycle' }])
  add('subseries.setup.series', R, `/api/libraries/${ids.library}/series?limit=20&page=0&sort=name&desc=0&minified=1`, { capture: 'seriesList' })
  const seriesFilter = (ctx, name) => `series.${Buffer.from(ctx.seriesList.results.find(x => x.name === name).id).toString('base64')}`
  const sub = (id, who, query, name = 'Mega Cycle') => add(id, who, ctx => `/api/libraries/${ids.library}/items?minified=1&collapseseries=1&filter=${encodeURIComponent(seriesFilter(ctx, name))}${query}`)
  sub('subseries.default', L, '')
  sub('subseries.root', R, '&include=rssfeed')
  sub('subseries.sort.title.desc', L, '&sort=media.metadata.title&desc=1')
  sub('subseries.sort.sequence.desc', L, '&sort=sequence&desc=1')
  sub('subseries.sort.duration', L, '&sort=media.duration&desc=1')
  sub('subseries.sort.size', L, '&sort=size')
  sub('subseries.sort.author', L, '&sort=media.metadata.authorName')
  sub('subseries.sort.addedAt', L, '&sort=addedAt&desc=1')
  sub('subseries.sort.invalid', L, '&sort=bogus')
  sub('subseries.sort.empty', L, '&sort=')
  sub('subseries.page0', L, '&limit=2&page=0')
  sub('subseries.page1', L, '&limit=2&page=1')
  sub('subseries.alone', L, '', 'Long Saga')
  sub('subseries.finisher', 'finisher', '&sort=media.metadata.title')
  add('subseries.missing', L, `/api/libraries/${ids.library}/items?minified=1&collapseseries=1&filter=series.${Buffer.from('00000000-0000-4000-8000-000000000000').toString('base64')}`)
  add('subseries.no-series', L, `/api/libraries/${ids.library}/items?minified=1&collapseseries=1&filter=series.${Buffer.from('no-series').toString('base64')}`)

  // include=rssfeed / include=share on shelves and pages: library items carry
  // their open RSS feed (libraryItemsBookFilters.js:407-408, 670-677 and
  // libraryFilters.js) and, for admins only, their open media item share.
  // The feeds and shares themselves are created by upstream in seed().
  add('includes.root.finish', R, `/api/me/progress/${ids.saga1}`, { method: 'PATCH', body: { isFinished: true } })
  add('includes.root.progress', R, `/api/me/progress/${ids.novel}`, { method: 'PATCH', body: { currentTime: 10, duration: 45 } })
  const shelfQuery = `/personalized?minified=1&include=rssfeed,numEpisodesIncomplete,share`
  add('includes.personalized.root', R, `/api/libraries/${ids.library}${shelfQuery}`)
  add('includes.personalized.listener', L, `/api/libraries/${ids.library}${shelfQuery}`)
  add('includes.personalized.finisher', 'finisher', `/api/libraries/${ids.library}${shelfQuery}`)
  add('includes.personalized.none', R, `/api/libraries/${ids.library}/personalized?minified=1`)
  add('includes.items.root', R, `/api/libraries/${ids.library}/items?limit=20&minified=1&sort=media.metadata.title&include=rssfeed,share`)
  add('includes.items.listener', L, `/api/libraries/${ids.library}/items?limit=20&minified=1&sort=media.metadata.title&include=rssfeed,share`)
  for (const k of ['saga1', 'saga2', 'novel', 'ebook']) {
    add(`includes.item.root.${k}`, R, `/api/items/${ids[k]}?expanded=1&include=rssfeed,share`)
  }
  add('includes.item.listener.novel', L, `/api/items/${ids.novel}?expanded=1&include=rssfeed,share`)

  // Feeds and shares created through the server under test (not seeded by
  // upstream): the HTTP responses, the rss_feed_* / share_* Socket.IO events a
  // root client receives, the public routes, and (after the journey) the
  // persisted rows, which are compared in main() via dumpRows().
  // RssFeedManager.openFeedForItem/Collection/Series, Feed.getFeedObjFor*,
  // FeedEpisode.getFeedEpisodeObjFrom*, ShareController.js:263-345, ShareManager.js.
  const FA = 'http://trial.invalid'
  const feedBody = (slug, metadataDetails) => ({ serverAddress: FA, slug, ...(metadataDetails ? { metadataDetails } : {}) })
  const seededFeedId = (ctx, key) => ctx.feedsSeeded.feeds.find(f => f.entityId === ids[key]).id
  const seriesId = (ctx, name) => ctx.seriesList.results.find(x => x.name === name).id
  add('life.feeds.seeded', R, '/api/feeds', { capture: 'feedsSeeded' })
  add('life.feed.open.invalid', R, `/api/feeds/item/${ids.saga2}/open`, { method: 'POST', body: { slug: 'life-x' } })
  add('life.feed.open.missing', R, '/api/feeds/item/00000000-0000-4000-8000-000000000000/open', { method: 'POST', body: feedBody('life-x') })
  add('life.feed.open.noaudio', R, `/api/feeds/item/${ids.ebook}/open`, { method: 'POST', body: feedBody('life-x') })
  add('life.feed.open.listener', L, `/api/feeds/item/${ids.saga2}/open`, { method: 'POST', body: feedBody('life-x') })
  add('life.feed.open.slugused', R, `/api/feeds/item/${ids.saga2}/open`, { method: 'POST', body: feedBody('trial-feed-saga1') })
  add('life.feed.open.saga2', R, `/api/feeds/item/${ids.saga2}/open`, { method: 'POST', capture: 'feedSaga2', events: 1, isoShape: true,
    body: feedBody('life-saga2', { preventIndexing: false, ownerName: 'Life Owner', ownerEmail: 'life@example.com' }) })
  add('life.feed.close.missing', R, '/api/feeds/00000000-0000-4000-8000-000000000000/close', { method: 'POST' })
  add('life.feed.close.listener', L, ctx => `/api/feeds/${ctx.feedSaga2.feed.id}/close`, { method: 'POST' })
  // Close the seeded feeds and reopen them without owner details / with odd values, so
  // columns that upstream leaves NULL are exercised.
  add('life.feed.close.saga1', R, ctx => `/api/feeds/${seededFeedId(ctx, 'saga1')}/close`, { method: 'POST', events: 1 })
  add('life.feed.open.saga1.default', R, `/api/feeds/item/${ids.saga1}/open`, { method: 'POST', body: feedBody('life-saga1'), capture: 'feedSaga1', events: 1 })
  add('life.feed.close.novel', R, ctx => `/api/feeds/${seededFeedId(ctx, 'novel')}/close`, { method: 'POST', events: 1 })
  add('life.feed.open.novel.odd', R, `/api/feeds/item/${ids.novel}/open`, { method: 'POST', events: 1,
    body: feedBody('life-novel', { preventIndexing: 'no', ownerName: '', ownerEmail: 42 }) })
  add('life.feed.close.podcast', R, ctx => `/api/feeds/${seededFeedId(ctx, 'podcast')}/close`, { method: 'POST', events: 1 })
  add('life.feed.open.podcast', R, `/api/feeds/item/${ids.podcast}/open`, { method: 'POST', events: 1,
    body: feedBody('life-cast', { preventIndexing: false, ownerName: 'Cast Owner' }) })
  add('life.feed.open.collection', R, `/api/feeds/collection/${ids.collection}/open`, { method: 'POST', events: 1, body: feedBody('life-collection', { ownerEmail: 'col@example.com' }) })
  add('life.feed.open.collection.missing', R, '/api/feeds/collection/00000000-0000-4000-8000-000000000000/open', { method: 'POST', body: feedBody('life-y') })
  add('life.feed.open.series.mega', R, ctx => `/api/feeds/series/${seriesId(ctx, 'Mega Cycle')}/open`, { method: 'POST', events: 1, body: feedBody('life-series-mega') })
  add('life.feed.open.series.long', R, ctx => `/api/feeds/series/${seriesId(ctx, 'Long Saga')}/open`, { method: 'POST', events: 1, capture: 'feedLong', body: feedBody('life-series-long', { preventIndexing: false }) })
  add('life.feed.open.series.missing', R, '/api/feeds/series/00000000-0000-4000-8000-000000000000/open', { method: 'POST', body: feedBody('life-z') })
  // Entities edited after their feed was opened: the next GET /feed/:slug
  // refreshes the feed (RssFeedManager.checkFeedRequiresUpdate, Feed.updateFeedForEntity).
  add('life.feed.stale.saga2.patch', R, `/api/items/${ids.saga2}/media`, { method: 'PATCH', body: { metadata: { title: 'The Middle Revised', description: 'A <b>revised</b> description.' } } })
  add('life.feed.stale.collection.patch', R, `/api/collections/${ids.collection}`, { method: 'PATCH', body: { name: 'Favourites Renamed', description: 'Renamed collection' } })
  add('life.feeds.list', R, '/api/feeds')
  add('life.feeds.list.listener', L, '/api/feeds')
  add('life.feed.xml.saga2', null, '/feed/life-saga2', { binary: 'xml' })
  add('life.feed.xml.saga1', null, '/feed/life-saga1', { binary: 'xml' })
  add('life.feed.xml.novel', null, '/feed/life-novel', { binary: 'xml' })
  add('life.feed.xml.podcast', null, '/feed/life-cast', { binary: 'xml' })
  add('life.feed.xml.collection', null, '/feed/life-collection', { binary: 'xml' })
  add('life.feed.xml.series', null, '/feed/life-series-mega', { binary: 'xml' })
  add('life.feed.xml.missing', null, '/feed/life-nope', { binary: 'xml' })
  add('life.feed.cover.saga1', null, '/feed/life-saga1/cover.jpg', { binary: 'bytes' })
  add('life.items.rssfeed', R, `/api/libraries/${ids.library}/items?limit=20&minified=1&sort=media.metadata.title&include=rssfeed,share`)
  add('life.feed.close.series.long', R, ctx => `/api/feeds/${ctx.feedLong.feed.id}/close`, { method: 'POST', events: 1 })
  add('life.feeds.list.after', R, '/api/feeds')

  const bookId = (key) => ctx => ctx[`item_${key}`].media.id
  for (const k of ['saga1', 'saga2', 'ebook']) add(`life.share.item.${k}`, R, `/api/items/${ids[k]}?expanded=1`, { capture: `item_${k}` })
  const shareBody = (key, extra) => ctx => ({ slug: `life-share-${key}`, mediaItemType: 'book', mediaItemId: bookId(key)(ctx), expiresAt: 0, ...extra })
  add('life.share.create.listener', L, '/api/share/mediaitem', { method: 'POST', body: shareBody('saga1') })
  add('life.share.create.invalid', R, '/api/share/mediaitem', { method: 'POST', body: { slug: ' ', mediaItemType: 'book', mediaItemId: 'x', expiresAt: 0 } })
  add('life.share.create.badtype', R, '/api/share/mediaitem', { method: 'POST', body: shareBody('saga1', { mediaItemType: 'nope' }) })
  add('life.share.create.badexpiry', R, '/api/share/mediaitem', { method: 'POST', body: shareBody('saga1', { expiresAt: -5 }) })
  add('life.share.create.missingitem', R, '/api/share/mediaitem', { method: 'POST', body: { slug: 'life-share-none', mediaItemType: 'book', mediaItemId: '00000000-0000-4000-8000-000000000000', expiresAt: 0 } })
  add('life.share.create.saga1', R, '/api/share/mediaitem', { method: 'POST', capture: 'shareSaga1', events: 1, isoShape: true, body: shareBody('saga1', { isDownloadable: true }) })
  add('life.share.create.dup.item', R, '/api/share/mediaitem', { method: 'POST', body: shareBody('saga1', { slug: 'life-share-other' }) })
  add('life.share.create.dup.slug', R, '/api/share/mediaitem', { method: 'POST', body: { slug: 'life-share-saga1', mediaItemType: 'podcastEpisode', mediaItemId: ids.ep2, expiresAt: 0 } })
  // Fixed far-future expiry (2100-01-01): the exact ISO value is compared.
  add('life.share.create.ebook', R, '/api/share/mediaitem', { method: 'POST', capture: 'shareEbook', events: 1, isoShape: true, body: shareBody('ebook', { expiresAt: 4102444800000, isDownloadable: false }) })
  // isDownloadable absent: omitted from the response and event, NULL in the row.
  add('life.share.create.episode', R, '/api/share/mediaitem', { method: 'POST', events: 1, isoShape: true, body: { slug: 'life-share-ep3', mediaItemType: 'podcastEpisode', mediaItemId: ids.ep3, expiresAt: 4102444800000 } })
  // Expiry already past: upstream answers 201 and emits share_open, but drops the row at once.
  add('life.share.create.pastexpiry', R, '/api/share/mediaitem', { method: 'POST', events: 1, isoShape: true, body: { slug: 'life-share-past', mediaItemType: 'podcastEpisode', mediaItemId: ids.ep1, expiresAt: 1000 } })
  add('life.share.public.saga1', null, '/public/share/life-share-saga1', { capture: 'publicShare', isoShape: true })
  add('life.share.public.saga1.t', null, '/public/share/life-share-saga1?t=7', { isoShape: true })
  add('life.share.public.ebook', null, '/public/share/life-share-ebook', { isoShape: true })
  add('life.share.public.episode', null, '/public/share/life-share-ep3', { isoShape: true })
  add('life.share.public.pastexpiry', null, '/public/share/life-share-past')
  add('life.share.public.missing', null, '/public/share/life-share-nope')
  add('life.share.delete.listener', L, ctx => `/api/share/mediaitem/${ctx.shareSaga1.id}`, { method: 'DELETE' })
  add('life.share.delete.missing', R, '/api/share/mediaitem/00000000-0000-4000-8000-000000000000', { method: 'DELETE' })
  add('life.items.share', R, `/api/libraries/${ids.library}/items?limit=20&minified=1&sort=media.metadata.title&include=rssfeed,share`)
  add('life.share.delete.ebook', R, ctx => `/api/share/mediaitem/${ctx.shareEbook.id}`, { method: 'DELETE', events: 1, isoShape: true })
  add('life.share.public.ebook.deleted', null, '/public/share/life-share-ebook')
  // Year in review over the legacy rows (NULL and REAL columns).
  const year = new Date().getUTCFullYear()
  add('legacy.year.me', L, `/api/me/stats/year/${year}`)
  add('legacy.year.admin', R, `/api/stats/year/${year}`)
  add('legacy.listening-stats', L, '/api/me/listening-stats')
  add('legacy.listening-sessions', L, '/api/me/listening-sessions?itemsPerPage=50&page=0')
  add('legacy.sessions.all', R, '/api/sessions?itemsPerPage=50&page=0')

  // ---- Authors: AuthorController.js (findOne, update/merge, delete, image
  // read/upload/delete, match). The author page and cover grid of the mobile
  // apps and the web client call these. Images come from a local source
  // (ctx.fixtureUrl); the Audnexus match needs the network, so only its
  // no-result paths are compared (an Audnexus lookup cannot be redirected to a
  // fixture on the upstream side, which has no endpoint override).
  const ZERO_ID = '00000000-0000-4000-8000-000000000000'
  const authorId = (ctx, name) => ctx.authorList.authors.find(a => a.name === name).id
  const AUTH = (name, suffix = '') => ctx => `/api/authors/${authorId(ctx, name)}${suffix}`
  const TERRY = 'Terry Fixture'
  const imageBody = (file) => ctx => ({ url: `${ctx.fixtureUrl}/${file}` })
  add('author.list', R, `/api/libraries/${ids.library}/authors`, { capture: 'authorList' })
  add('author.get.terry', R, AUTH(TERRY))
  add('author.get.terry.items', R, AUTH(TERRY, '?include=items'))
  add('author.get.terry.series', R, AUTH(TERRY, '?include=items,series'))
  add('author.get.terry.seriesonly', R, AUTH(TERRY, '?include=series'))
  add('author.get.ada.series', R, AUTH('Ada Example', '?include=items,series'))
  add('author.get.listener', L, AUTH(TERRY, '?include=items,series'))
  add('author.get.finisher', 'finisher', AUTH('Ada Example', '?include=items'))
  add('author.get.anon', null, AUTH(TERRY))
  add('author.get.missing', R, `/api/authors/${ZERO_ID}`)
  add('author.get.badid', R, '/api/authors/not-a-uuid')
  add('author.image.none', null, AUTH(TERRY, '/image'), { binary: 'bytes' })
  add('author.image.none.raw', null, AUTH(TERRY, '/image?raw=1'), { binary: 'bytes' })
  add('author.image.badformat', null, AUTH(TERRY, '/image?format=gif'), { binary: 'bytes' })
  add('author.image.badid', null, '/api/authors/not-a-uuid/image', { binary: 'bytes' })
  add('author.image.badid.raw', null, '/api/authors/not-a-uuid/image?raw=1', { binary: 'bytes' })
  add('author.image.missing', null, `/api/authors/${ZERO_ID}/image`, { binary: 'bytes' })
  add('author.image.upload.listener', L, AUTH(TERRY, '/image'), { method: 'POST', body: imageBody('author.jpg') })
  add('author.image.upload.nourl', R, AUTH(TERRY, '/image'), { method: 'POST', body: {} })
  add('author.image.upload.badscheme', R, AUTH(TERRY, '/image'), { method: 'POST', body: { url: 'ftp://example.invalid/a.jpg' } })
  add('author.image.upload.numurl', R, AUTH(TERRY, '/image'), { method: 'POST', body: { url: 5 } })
  add('author.image.upload.notimage', R, AUTH(TERRY, '/image'), { method: 'POST', body: imageBody('page.html') })
  add('author.image.upload.svg', R, AUTH(TERRY, '/image'), { method: 'POST', body: imageBody('icon.svg') })
  add('author.image.upload.404', R, AUTH(TERRY, '/image'), { method: 'POST', body: imageBody('missing.jpg') })
  add('author.image.upload.refused', R, AUTH(TERRY, '/image'), { method: 'POST', body: ctx => ({ url: `${ctx.deadUrl}/a.jpg` }) })
  add('author.image.upload.nochange', R, AUTH(TERRY), { capture: 'terryBefore' })
  add('author.image.upload.ok', R, AUTH(TERRY, '/image'), { method: 'POST', body: imageBody('author.jpg'), events: 1 })
  add('author.image.raw', null, AUTH(TERRY, '/image?raw=1'), { binary: 'bytes' })
  add('author.image.default', null, AUTH(TERRY, '/image'), { binary: 'bytes' })
  add('author.image.jpeg', null, AUTH(TERRY, '/image?format=jpeg'), { binary: 'bytes' })
  add('author.image.w200', null, AUTH(TERRY, '/image?width=200&format=jpeg'), { binary: 'bytes' })
  add('author.image.w100h50', null, AUTH(TERRY, '/image?width=100&height=50&format=png'), { binary: 'bytes' })
  add('author.image.h120', null, AUTH(TERRY, '/image?height=120&format=jpeg'), { binary: 'bytes' })
  add('author.image.w0', null, AUTH(TERRY, '/image?width=0&format=jpeg'), { binary: 'bytes' })
  add('author.image.wbad', null, AUTH(TERRY, '/image?width=abc&format=jpeg'), { binary: 'bytes' })
  add('author.image.clamp', null, AUTH(TERRY, '/image?width=100&height=999999&format=jpeg'), { binary: 'bytes' })
  add('author.image.webp', null, AUTH(TERRY, '/image?width=150&format=webp'), { binary: 'bytes' })
  add('author.image.w200.again', null, AUTH(TERRY, '/image?width=200&format=jpeg'), { binary: 'bytes' })
  add('author.get.terry.afterimage', R, AUTH(TERRY))
  add('author.update.listener', L, AUTH(TERRY), { method: 'PATCH', body: { description: 'x' } })
  add('author.update.empty', R, AUTH(TERRY), { method: 'PATCH', body: {} })
  add('author.update.invalid', R, AUTH(TERRY), { method: 'PATCH', body: { name: 5, description: {}, other: 'x' } })
  add('author.update.description', R, AUTH(TERRY), { method: 'PATCH', body: { description: 'A prolific fixture author.', asin: 'B000TERRY1' }, events: 1 })
  add('author.update.nochange', R, AUTH(TERRY), { method: 'PATCH', body: { description: 'A prolific fixture author.', asin: 'B000TERRY1' } })
  add('author.update.null', R, AUTH(TERRY), { method: 'PATCH', body: { asin: null }, events: 1 })
  add('author.match.listener', L, AUTH(TERRY, '/match'), { method: 'POST', body: {} })
  add('author.match.empty', R, AUTH(TERRY, '/match'), { method: 'POST', body: {} })
  add('author.match.badasin', R, AUTH(TERRY, '/match'), { method: 'POST', body: { asin: 'nope', region: 'uk' } })
  add('author.rename.ada', R, AUTH('Ada Example'), { method: 'PATCH', body: { name: 'Ada Renamed' }, events: 2 })
  add('author.after.rename.list', R, `/api/libraries/${ids.library}/authors`)
  add('author.after.rename.item', R, `/api/items/${ids.novel}?expanded=1`)
  add('author.rename.lowercase', R, AUTH('Ada Example'), { method: 'PATCH', body: { name: 'ada renamed again' }, events: 2 })
  add('author.merge', R, AUTH('Ada Example'), { method: 'PATCH', body: { name: TERRY }, events: 3 })
  add('author.after.merge.ada', R, AUTH('Ada Example'))
  add('author.after.merge.terry', R, AUTH(TERRY, '?include=items,series'))
  add('author.after.merge.list', R, `/api/libraries/${ids.library}/authors`)
  add('author.after.merge.item', R, `/api/items/${ids.novel}?expanded=1`)
  add('author.temp.create', R, `/api/items/${ids.saga2}/media`, { method: 'PATCH', body: { metadata: { authors: [{ name: TERRY }, { name: 'Temp Author' }] } } })
  add('author.temp.list', R, `/api/libraries/${ids.library}/authors`, { capture: 'authorList' })
  add('author.temp.image', R, AUTH('Temp Author', '/image'), { method: 'POST', body: imageBody('author.png'), events: 1 })
  add('author.temp.image.get', null, AUTH('Temp Author', '/image?width=64&format=jpeg'), { binary: 'bytes' })
  add('author.temp.delete.listener', L, AUTH('Temp Author'), { method: 'DELETE' })
  add('author.temp.delete', R, AUTH('Temp Author'), { method: 'DELETE', events: 1 })
  add('author.temp.delete.again', R, AUTH('Temp Author'), { method: 'DELETE' })
  add('author.temp.image.after', null, AUTH('Temp Author', '/image?width=64&format=jpeg'), { binary: 'bytes' })
  add('author.temp.item', R, `/api/items/${ids.saga2}?expanded=1`)
  add('author.image.delete.listener', L, AUTH(TERRY, '/image'), { method: 'DELETE' })
  add('author.image.delete', R, AUTH(TERRY, '/image'), { method: 'DELETE', events: 1 })
  add('author.image.delete.again', R, AUTH(TERRY, '/image'), { method: 'DELETE' })
  add('author.image.after.delete', null, AUTH(TERRY, '/image?width=200&format=jpeg'), { binary: 'bytes' })
  add('author.image.after.delete.raw', null, AUTH(TERRY, '/image?raw=1'), { binary: 'bytes' })
  add('author.final.list', R, `/api/libraries/${ids.library}/authors`)

  // ---- Series: SeriesController.js findOne and update (the series page of the
  // mobile apps). seriesList and the open series feed come from the sub-series
  // and feed lifecycle steps above.
  const SER = (name, suffix = '') => ctx => `/api/series/${seriesId(ctx, name)}${suffix}`
  add('series.get.long', R, SER('Long Saga'))
  add('series.get.long.progress', R, SER('Long Saga', '?include=progress'))
  add('series.get.mega.progress.rss', R, SER('Mega Cycle', '?include=progress,rssfeed'))
  add('series.get.mega.spaced', R, SER('Mega Cycle', '?include=%20progress%20,%20rssfeed%20,,'))
  add('series.get.long.rss', R, SER('Long Saga', '?include=rssfeed'))
  add('series.get.mega.listener', L, SER('Mega Cycle', '?include=progress'))
  add('series.get.mega.finisher', 'finisher', SER('Mega Cycle', '?include=progress'))
  add('series.get.anon', null, SER('Long Saga'))
  add('series.get.missing', R, `/api/series/${ZERO_ID}`)
  add('series.get.badid', R, '/api/series/not-a-uuid')
  add('series.update.listener', L, SER('Long Saga'), { method: 'PATCH', body: { name: 'x' } })
  add('series.update.empty', R, SER('Long Saga'), { method: 'PATCH', body: {} })
  add('series.update.invalid', R, SER('Long Saga'), { method: 'PATCH', body: { name: 5, description: null, other: 'x' } })
  add('series.update.missing', R, `/api/series/${ZERO_ID}`, { method: 'PATCH', body: { name: 'x' } })
  add('series.update.rename', R, SER('Long Saga'), { method: 'PATCH', body: { name: 'The Long Saga Renamed', description: 'A saga in two parts.' }, events: 1 })
  add('series.update.nochange', R, SER('Long Saga'), { method: 'PATCH', body: { name: 'The Long Saga Renamed', description: 'A saga in two parts.' } })
  add('series.update.description', R, SER('Long Saga'), { method: 'PATCH', body: { description: '' }, events: 1 })
  add('series.after.get', R, SER('Long Saga', '?include=progress,rssfeed'))
  add('series.after.list', R, `/api/libraries/${ids.library}/series?limit=20&page=0&sort=name&desc=0&minified=1`)
  return s
}

const UUID = /[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}/g
const JWT = /eyJ[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+/g
let WORK_DIR = ''
let DEAD_PORT = 0
const TIME_KEY = /(At|Time|Update|Seen|Scan|^date|^dayOfWeek|Ms|token|Token)$/

function normalize(value, known, fresh, key = '') {
  if (typeof value === 'string' && key === 'filterBy') {
    // Library filters carry ids as base64: "series.<b64 uuid>". Ids created
    // during the journey differ per server, so normalize the decoded id.
    const m = /^([a-z]+)\.([A-Za-z0-9+/=]+)$/.exec(value)
    const decoded = m && Buffer.from(m[2], 'base64').toString()
    if (decoded && /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/.test(decoded) && !known.has(decoded)) return `${m[1]}.<b64:new-uuid>`
  }
  if (typeof value === 'string') {
    // Feed cover URLs carry the entity's updatedAt in ms (Feed.getFeedImageURL),
    // which is the wall-clock time of an earlier journey mutation.
    let v = value.replace(JWT, '<jwt>').replace(/(\/cover\.\w+)\?ts=\d+$/, '$1?ts=<ms>')
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
  // booksWithCovers / finishedBooksWithCovers / booksAddedWithCovers are
  // selected with ORDER BY RANDOM() upstream (userStats.js, adminStats.js):
  // compare them as sets.
  if (Array.isArray(value) && /WithCovers$/.test(key)) return value.map(v => normalize(v, known, fresh, key)).sort()
  if (Array.isArray(value)) return value.map(v => normalize(v, known, fresh, key))
  if (value && typeof value === 'object') {
    const out = {}
    for (const k of Object.keys(value).sort()) out[k] = normalize(value[k], known, fresh, k)
    return out
  }
  return value
}

// ISO date strings are normalized to <iso> by normalize(), which would hide a
// wrong format (epoch millis vs ISO, missing milliseconds). With isoShape the
// digits are kept as a shape ("9999-99-99T99:99:99.999Z"), except expiresAt,
// whose value is fixed by the step and compared exactly.
function isoShape(value, key = '') {
  if (typeof value === 'string' && key !== 'expiresAt' && /^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}/.test(value)) return `iso-shape:${value.replace(/\d/g, '9')}`
  if (Array.isArray(value)) return value.map(v => isoShape(v, key))
  if (value && typeof value === 'object') return Object.fromEntries(Object.entries(value).map(([k, v]) => [k, isoShape(v, k)]))
  return value
}

// The generated RSS has the request host (random port) and wall-clock dates.
function normalizeFeedXML(text) {
  return text.replace(UUID, '<uuid>').replace(/http:\/\/127\.0\.0\.1:\d+/g, '<host>')
    .replace(/<lastBuildDate>[^<]*<\/lastBuildDate>/g, '<lastBuildDate>').replace(/\?ts=\d+/g, '?ts=<ms>')
}

// A root Socket.IO client that records the feed and share events emitted during
// the journey; take(n) waits (up to 3s) for n events and drains the buffer.
async function connectEventSocket(server, token) {
  const io = loadSocketIOClient()
  if (!io) return null
  const socket = io(server.baseUrl, { path: `${BASE}/socket.io`, transports: ['websocket'], reconnection: false, timeout: 8000 })
  const buffer = []
  for (const name of ['rss_feed_open', 'rss_feed_closed', 'share_open', 'share_closed', 'author_updated', 'author_removed', 'author_added', 'items_updated', 'item_updated', 'item_removed', 'series_updated', 'series_removed', 'batch_quickmatch_complete']) socket.on(name, payload => buffer.push({ event: name, payload }))
  await new Promise((resolve, reject) => {
    const t = setTimeout(() => reject(new Error('event socket: no init')), 10000)
    socket.on('connect', () => socket.emit('auth', token))
    socket.on('init', () => { clearTimeout(t); resolve() })
    socket.on('connect_error', reject)
  })
  buffer.length = 0
  return {
    // Forget what earlier steps emitted, so a step that checks events sees only its own.
    drain() { buffer.length = 0 },
    async take(n) {
      const deadline = Date.now() + 3000
      while (buffer.length < n && Date.now() < deadline) await delay(25)
      await delay(150)
      return buffer.splice(0)
    },
    close() { socket.disconnect() }
  }
}

async function runJourney(server, ids, logDir) {
  const call = api(server)
  const ctx = {}
  const tokens = {}
  const results = {}
  const known = snapshotUUIDs(ids)
  const fresh = new Map()
  const resolve = (v) => typeof v === 'function' ? v(ctx) : v
  let rootSocket = null
  // A local image source for the routes that download an image from a URL, and
  // a port nothing listens on (the same for both servers, because the connect
  // error names it).
  const imageDir = path.join(logDir, 'image-fixture')
  fs.mkdirSync(imageDir, { recursive: true })
  const jpeg = path.join(imageDir, 'author.jpg')
  const png = path.join(imageDir, 'author.png')
  if (!fs.existsSync(jpeg)) ffmpeg(['-f', 'lavfi', '-i', 'color=c=orange:s=300x500', '-frames:v', '1', jpeg])
  if (!fs.existsSync(png)) ffmpeg(['-f', 'lavfi', '-i', 'color=c=purple:s=200x200', '-frames:v', '1', png])
  const imageSource = await startImageSource(jpeg, png)
  ctx.fixtureUrl = imageSource.url
  DEAD_PORT ||= await freePort()
  ctx.deadUrl = `http://127.0.0.1:${DEAD_PORT}`
  try {
    for (const step of journeySteps(ids)) {
      if (step.skip) continue
      let route
      try { route = resolve(step.route) } catch (e) { results[step.id] = { error: `route unresolved: ${e.message}` }; continue }
      const token = step.who ? tokens[step.who] : undefined
      if (step.tokenQuery && token && !/[?&]token=/.test(route)) route += (route.includes('?') ? '&' : '?') + `token=${token}`
      let body, headers
      try { body = resolve(step.body); headers = resolve(step.headers) || {} } catch (e) { results[step.id] = { error: `body unresolved: ${e.message}` }; continue }
      if (step.events) {
        await delay(100)
        rootSocket?.drain()
      }
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
      if (step.id === 'login.root' && r.ok) {
        tokens.root = r.value.user.accessToken
        rootSocket = await connectEventSocket(server, tokens.root)
      }
      if (step.id === 'login.finisher' && r.ok) tokens.finisher = r.value.user.accessToken
      let recorded
      if (step.binary === 'bytes') recorded = { len: r.bytes.length, sha: require('node:crypto').createHash('sha256').update(r.bytes).digest('hex'), type: r.headers.get('content-type'), range: r.headers.get('content-range') }
      else if (step.binary === 'meta') recorded = { type: r.headers.get('content-type'), nonEmpty: r.bytes.length > 0 }
      else if (step.binary === 'xml') recorded = { status: r.status, type: r.headers.get('content-type'), text: typeof r.value === 'string' ? normalizeFeedXML(r.value) : r.value }
      else if (step.binary === 'text') recorded = { type: r.headers.get('content-type'), text: typeof r.value === 'string' ? r.value.replace(UUID, '<uuid>').replace(/[?&]token=[^\s&]+/g, '') : r.value }
      else recorded = normalize(step.isoShape ? isoShape(r.value) : r.value, known, fresh)
      results[step.id] = { status: r.status, body: recorded }
      if (step.events) {
        const events = await rootSocket?.take(step.events)
        results[step.id].events = normalize(step.isoShape ? isoShape(events) : events, known, fresh)
      }
    }
  } finally {
    rootSocket?.close()
    await imageSource.close()
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

// Real installs carry playbackSessions rows written by older upstream
// versions and mobile uploads: NULL playMethod, timeListening, display
// fields, date and mediaMetadata, and fractional seconds. The current
// upstream never writes such rows through its API, so the seed inserts them
// directly (both servers then read the same rows). A Go reader that scanned
// these into non-nullable types returned 500 on /api/me/listening-stats in a
// real deployment. No row has 0 < timeListening <= 3 or 0: upstream's boot
// cleanup (Database.cleanDatabase, server/Database.js:777-786) deletes those,
// so they cannot exist in an install upstream has booted.
function addLegacySessions(dbPath, ids) {
  const { DatabaseSync } = require('node:sqlite')
  const db = new DatabaseSync(dbPath)
  try {
    const book = db.prepare(`SELECT mediaId FROM libraryItems WHERE id = ?`).get(ids.saga1).mediaId
    const ins = db.prepare(`INSERT INTO playbackSessions (id,mediaItemId,mediaItemType,displayTitle,displayAuthor,duration,playMethod,mediaPlayer,startTime,currentTime,serverVersion,coverPath,timeListening,mediaMetadata,date,dayOfWeek,extraData,userId,deviceId,libraryId,createdAt,updatedAt) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`)
    const extra = JSON.stringify({ libraryItemId: ids.saga1 })
    const y = new Date().getUTCFullYear()
    ins.run('legacy-null-0001', book, 'book', null, null, null, null, null, null, null, null, null, null, null, null, null, extra, ids.listener, null, ids.library, `${y}-01-15 10:00:00.000 +00:00`, `${y}-01-15 10:05:00.000 +00:00`)
    ins.run('legacy-null-0002', book, 'book', 'The Beginning', null, 60.186123, 3, null, 0, 12.4, '2.17.0', null, 12.4, '{"title":"The Beginning","authors":[{"name":"Terry Fixture"}],"genres":["Fiction"]}', `${y}-02-02`, 'Monday', extra, ids.listener, null, ids.library, `${y}-02-02 10:00:00.000 +00:00`, `${y}-02-02 10:01:00.000 +00:00`)
    ins.run('legacy-null-0003', book, 'book', 'The Beginning', 'Terry Fixture', 60.186123, null, 'AVPlayer', 1.25, 33.75, null, null, null, '{}', null, null, JSON.stringify({ libraryItemId: 'li_legacy' }), ids.listener, null, ids.library, `${y}-03-02 10:00:00.000 +00:00`, `${y}-03-02 10:01:00.000 +00:00`)
  } finally {
    db.close()
  }
}

// Persisted rows written by the journey (feeds, feed episodes, shares), read
// from the stopped server's database. IDs created during the journey and
// timestamps are normalized; everything else (NULL vs '', booleans, dates,
// URLs) is compared exactly. node:sqlite is experimental in Node 22.
function dumpRows(dbPath, ids) {
  const { DatabaseSync } = require('node:sqlite')
  const db = new DatabaseSync(dbPath, { readOnly: true })
  const known = snapshotUUIDs(ids)
  const shape = (v, key) => {
    if (typeof v === 'string') {
      if (/(^|[a-z])(createdAt|updatedAt)$/.test(key)) return `ts-shape:${v.replace(/\d/g, '9')}`
      return v.replace(UUID, m => known.has(m) ? m : '<new-uuid>')
    }
    return v
  }
  const rows = (sql) => db.prepare(sql).all().map(r => {
    // imageURL's ?ts= must equal entityUpdatedAt in ms; the instant itself is journey wall-clock.
    if (typeof r.imageURL === 'string' && typeof r.entityUpdatedAt === 'string') {
      const ms = Date.parse(r.entityUpdatedAt.replace(' +00:00', 'Z').replace(' ', 'T'))
      r.imageURL = r.imageURL.replace(/\?ts=(\d+)$/, (m, ts) => Number(ts) === ms ? '?ts=<entityUpdatedAt>' : m)
      r.entityUpdatedAt = 'ts-shape:' + r.entityUpdatedAt.replace(/\d/g, '9')
    }
    return Object.fromEntries(Object.keys(r).sort().map(k => [k, shape(r[k], k)]))
  })
  // Author rows, the book/author links in order, the denormalized author
  // names of library items, and the files author routes write.
  const metadataDir = path.join(path.dirname(path.dirname(dbPath)), 'metadata')
  const listDir = (dir) => fs.existsSync(dir)
    ? fs.readdirSync(dir).sort().map(name => ({ name: name.replace(UUID, m => known.has(m) ? m : '<new-uuid>'), size: fs.statSync(path.join(dir, name)).size }))
    : []
  const itemMetadata = {}
  const itemsDir = path.join(metadataDir, 'items')
  for (const id of fs.existsSync(itemsDir) ? fs.readdirSync(itemsDir).sort() : []) {
    const file = path.join(itemsDir, id, 'metadata.json')
    // Key order of the file is not part of the contract (readers parse it).
    if (fs.existsSync(file)) itemMetadata[id.replace(UUID, m => known.has(m) ? m : '<new-uuid>')] = sortKeys(JSON.parse(fs.readFileSync(file, 'utf8')))
  }
  const out = {
    'db.authors': rows('SELECT * FROM authors ORDER BY name, createdAt'),
    'db.series': rows('SELECT * FROM series ORDER BY name, createdAt'),
    'db.bookAuthors': rows('SELECT li.title AS title, a.name AS author FROM bookAuthors ba JOIN authors a ON a.id = ba.authorId JOIN libraryItems li ON li.mediaId = ba.bookId ORDER BY li.title, ba.createdAt, a.name'),
    'db.libraryItems.authorNames': rows('SELECT title, authorNamesFirstLast, authorNamesLastFirst FROM libraryItems ORDER BY title'),
    'fs.metadata.authors': listDir(path.join(metadataDir, 'authors')),
    'fs.cache.images': listDir(path.join(metadataDir, 'cache', 'images')),
    'fs.items.metadata.json': itemMetadata,
    'db.feeds': rows('SELECT * FROM feeds ORDER BY slug'),
    'db.feedEpisodes': rows('SELECT fe.*, f.slug AS feedSlug FROM feedEpisodes fe LEFT JOIN feeds f ON f.id = fe.feedId ORDER BY f.slug, fe.pubDate, fe.filePath'),
    'db.mediaItemShares': rows('SELECT * FROM mediaItemShares ORDER BY slug'),
    // Devices created by public share visits (no user): PlaybackSessionManager.getDeviceInfo.
    'db.devices.public': rows('SELECT * FROM devices WHERE userId IS NULL ORDER BY clientName, deviceName')
  }
  db.close()
  return Object.fromEntries(Object.entries(out).map(([k, v]) => [k, { status: 200, body: v }]))
}

function sortKeys(value) {
  if (Array.isArray(value)) return value.map(sortKeys)
  if (value && typeof value === 'object') return Object.fromEntries(Object.keys(value).sort().map(k => [k, sortKeys(value[k])]))
  return value
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

// Generates the media tree, lets pinned upstream create and scan the
// installation, then snapshots it (work/snapshot) and records the ids
// (work/ids.json). Shared with route-probe.cjs.
async function seedInstallation(work) {
  const media = generateMedia(path.join(work, 'media'))
  const { dirs, ids } = await seed(work, media)
  addLegacySessions(path.join(dirs.config, 'absdatabase.sqlite'), ids)
  copyTree(path.join(work, 'install'), path.join(work, 'snapshot'))
  fs.writeFileSync(path.join(work, 'ids.json'), JSON.stringify(ids, null, 2))
  return { dirs, ids }
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
    ;({ dirs, ids } = await seedInstallation(work))
  }
  SNAPSHOT_DB = path.join(snapshot, 'config', 'absdatabase.sqlite')

  // Upstream journey (reference), in place.
  let up = await startUpstream(dirs, await freePort(), path.join(work, 'journey-upstream.log'))
  const upstream = await runJourney(up, ids, work)
  const upLogin = await loginAs(api(up), LISTENER)
  const upSocket = await socketProgressProbe(up, ids, upLogin.accessToken)
  await stop(up)
  Object.assign(upstream, dumpRows(path.join(dirs.config, 'absdatabase.sqlite'), ids))

  // Swap: restore the pre-journey installation to the same paths, boot Go.
  copyTree(snapshot, path.join(work, 'install'))
  const go = await startGo(goBin, dirs, await freePort(), path.join(work, 'journey-go.log'))
  const goResults = await runJourney(go, ids, work)
  let goSocket
  try { goSocket = await socketProgressProbe(go, ids, (await loginAs(api(go), LISTENER)).accessToken) } catch (e) { goSocket = { status: 'error', error: e.message } }
  const goProgress = await requestJSON(go.baseUrl, '/api/me', { token: (await loginAs(api(go), LISTENER)).accessToken })
  await stop(go)
  Object.assign(goResults, dumpRows(path.join(dirs.config, 'absdatabase.sqlite'), ids))

  // Rollback: upstream reopens the Go-written installation.
  let rollback
  try {
    up = await startUpstream(dirs, await freePort(), path.join(work, 'rollback-upstream.log'))
    const lt = (await loginAs(api(up), LISTENER)).accessToken
    const me = await requestJSON(up.baseUrl, '/api/me', { token: lt })
    const sessions = await requestJSON(up.baseUrl, '/api/me/listening-sessions?itemsPerPage=50&page=0', { token: lt })
    const pick = (u) => (u?.mediaProgress || []).map(p => ({ id: p.libraryItemId, ep: p.episodeId || '', t: p.currentTime, f: p.isFinished, e: p.ebookLocation, h: p.hideFromContinueListening })).sort((a, b) => (a.id + a.ep).localeCompare(b.id + b.ep))
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

  const unresolved = Object.entries(upstream).filter(([, v]) => v?.error).map(([k, v]) => `${k}: ${v.error}`)
  if (unresolved.length) throw new Error(`upstream journey steps could not run:\n${unresolved.join('\n')}`)
  for (const [kind, results] of [['upstream', upstream], ['go', goResults]]) fs.writeFileSync(path.join(work, `journey-${kind}.json`), JSON.stringify(results, null, 2))
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

module.exports = { ROOT_USER, startGo, api, loginAs, seedInstallation, copyTree, sh }

if (require.main === module) main().catch((error) => { console.error(error); process.exitCode = 1 })
