#!/usr/bin/env node
'use strict'

const fs = require('node:fs')
const path = require('node:path')
const crypto = require('node:crypto')
const os = require('node:os')
const { spawnSync } = require('node:child_process')
const { setTimeout: delay } = require('node:timers/promises')
const casesPath = path.resolve(__dirname, '../../contracts/parity/differential-cases.json')
const CASES = JSON.parse(fs.readFileSync(casesPath, 'utf8'))
const PREFIX = '/audiobookshelf'
let FIXTURE_PATH_ROOTS = null
// Manifest inode values replaced as substrings inside serialized URLs.
let MANIFEST_INOS = []

function globalInoReplacements(value) {
  if (!MANIFEST_INOS.length) return
  const walk = (node) => {
    if (Array.isArray(node)) {
      node.forEach(walk)
      return
    }
    if (!node || typeof node !== 'object') return
    for (const [key, child] of Object.entries(node)) {
      if (typeof child === 'string') {
        let replaced = child
        for (const ino of MANIFEST_INOS) replaced = replaced.split(ino).join('<fixture-inode>')
        if (replaced !== child) node[key] = replaced
      } else walk(child)
    }
  }
  walk(value)
}
const EXPECTED_STATUS = {
  login: 200, refresh: 200, 'reader-login': 200, authorize: 200,
  'local-session-login': 200, 'local-session-logout': 200,
  'local-session-cookie-regeneration': 200,
  'local-session-logout-refresh-revoked': 401, 'local-session-logout-session-list': 200,
  'local-session-relogin': 200, 'refresh-rotation': 200, 'refresh-concurrent-grace': 200,
  'refresh-rotation-grace-replay': 200, 'inactive-user-create': 200,
  'inactive-user-login-denied': 401,
  libraries: 200, 'libraries-restricted-user': 200,
  'book-library-items': 200, 'book-library-items-minified': 200,
  'book-item-detail': 200, 'direct-file-range': 206,
  'direct-file-head-range': 206, 'direct-file-invalid-range': 416,
  'direct-file-head-invalid-range': 416,
  'series-list-paginated': 200,
  'progress-write': 200, 'progress-read': 200,
  'bookmark-create': 200, 'bookmark-read-after-create': 200, 'bookmark-update': 200,
  'bookmark-delete': 200, 'bookmark-read-after-delete': 200,
  'library-scan-rerun': 200, 'book-list-after-library-scan': 200,
  'book-detail-after-library-scan': 200, 'book-item-scan-rerun': 200,
  'collection-create': 200, 'collection-read': 200, 'collection-update': 200,
  'collection-list': 200, 'collection-batch-remove': 200, 'collection-batch-add': 200,
  'collection-delete': 200, 'collection-list-after-delete': 200,
  'playlist-create': 200, 'playlist-read': 200, 'playlist-update': 200,
  'playlist-list': 200, 'playlist-batch-remove': 200, 'playlist-batch-add': 200,
  // Upstream deletes an emptied playlist during batch/remove; the later DELETE
  // targets the missing id and must 404 the same way on both implementations.
  'playlist-delete': 404, 'playlist-list-after-delete': 200,
  'cover-upload': 200, 'cover-raw': 200,
  'cover-upload-unsupported-gif': 400, 'cover-url-upload': 200,
  'cover-upload-multipart-url-precedence': 400,
  'cover-url-unsupported-image-format': 400,
  'cover-url-ssrf-blocked': 400,
  'cover-url-redirect-307': process.env.EXP_PROXY_SUPPORT === '1' ? 400 : 200,
  'cover-patch-invalid-image': 500,
  'cover-patch-large-invalid-path': 500,
  'cover-patch-success': 200, 'cover-patch-repeat': 200,
  'cover-delete': 200, 'cover-raw-after-delete': 404, 'cover-cache-after-delete': 404,
  'cover-cache-jpeg-after-delete': 404, 'cover-cache-png-after-delete': 404,
  // cover-cache-default and cover-cache-webp-size hit the shared ffmpeg build
  // on this machine, which lacks libwebp: BOTH implementations return
  // sendStatus(500) for webp renders here (CacheManager.js:100 sendStatus(500)
  // when ffmpeg fails). A ffmpeg build with libwebp would 200 on both sides.
  'cover-cache-default': 500, 'cover-cache-webp-size': 500,
  'cover-cache-jpeg-size': 200, 'cover-cache-png-size': 200,
  'cover-cache-ts-header': 200,
  'library-create': 200, 'library-read': 200, 'library-update': 200,
  'library-list-after-create': 200, 'library-delete': 200, 'library-list-after-delete': 200,
  'session-list': 200, 'session-delete': 200, 'session-list-after-delete': 200,
  'library-create': 200, 'library-read': 200, 'library-update': 200,
  'library-list-after-create': 200, 'library-delete': 200, 'library-list-after-delete': 200,
  'user-create': 200, 'user-read': 200, 'user-update': 200, 'user-list': 200,
  'user-delete': 200, 'user-list-after-delete': 200,
  'api-key-create': 200, 'api-key-list': 200, 'api-key-update': 200,
  'api-key-auth-library-read': 200,
  // The parity user is created with download:false (upstream-recognized
  // permission keys); an api key resolves to that user, so the download route
  // denies with sendStatus(403) on both implementations
  // (LibraryItemController.js:170-173).
  'api-key-denied-download': 403, 'api-key-delete': 200,
  'libraries-unauthorized': 200, 'item-detail-missing': 404,
  'item-detail-invalid-id': 404, 'library-items-invalid-pagination': 400,
  'progress-write-invalid': 200, 'collection-missing': 404,
  'playlist-missing': 404, 'user-create-forbidden': 403,
  'user-delete-forbidden': 403, 'session-delete-missing': 404,
  'email-settings-reader-admin-gate': 404, 'notifications-reader-admin-gate': 403,
  'notification-test-missing-id-unconfigured': 404,
  'notification-test-existing-id-unconfigured': 400,
  'notifications-clear-apprise-url': 200,
  'notification-test-admin-denied': 403,
  'notification-apprise-fixture-configure-success': 200,
  'notification-test-send-success': 200,
  'notification-test-apprise-payload': 200,
  'notification-read-after-test-success': 200,
  'notification-test-no-state-change-success': 200,
  'notification-apprise-fixture-configure-failure': 200,
  'notification-test-send-failure': 500,
  'notification-read-after-test-failure': 200,
  'notification-test-no-state-change-failure': 200,
  'notification-event-test-unconfigured': 200,
  'notification-event-test-fail-unconfigured': 200,
  'notification-event-test-admin-denied': 403,
  'notification-apprise-global-configure-success': 200,
  'notification-event-test-send-success': 200,
  'notification-event-test-set-failure-threshold': 200,
  'notification-event-broadcast-success': 200,
  'notification-event-test-delivery-payloads': 200,
  'notification-read-after-event-test-success': 200,
  'notification-event-test-state-success': 200,
  'notification-event-test-repeat-success': 200,
  'notification-event-test-repeat-payloads': 200,
  'notification-read-after-event-test-repeat': 200,
  'notification-event-test-state-repeat': 200,
  'notification-event-test-intentional-failure': 200,
  'notification-event-broadcast-failure': 200,
  'notification-event-test-no-delivery-on-failure': 200,
  'notification-read-after-event-test-failure': 200,
  'notification-event-test-state-failure': 200,
  'email-settings-patch-coercion': 200, 'email-settings-read-after-coercion': 200,
  'email-settings-read-defaults-initial': 200,
  'email-test-configure-smtp': 200, 'email-test-success': 200,
  'email-test-smtp-message': 200, 'email-test-configure-rejected-smtp': 200,
  'email-test-verification-failure': 400, 'email-test-no-message-on-verification-failure': 200,
  'email-devices-normalization-update': 200, 'email-devices-read-after-update': 200,
  'email-devices-update-event': 200, 'email-devices-persisted-state': 200,
  'email-devices-repeat': 200, 'email-devices-repeat-event': 200,
  'email-devices-changed': 200, 'email-devices-changed-event': 200,
  'email-devices-changed-persisted-state': 200,
  'notifications-settings-patch-custom': 200, 'notifications-settings-read-after-custom': 200,
  'notifications-settings-read-defaults-initial': 200,
  'notifications-settings-patch-defaults': 200, 'notifications-settings-read-after-defaults': 200,
  'notification-create': 200, 'notification-read-after-create': 200,
  'notification-create-minimal': 200,
  'notification-update-without-body-id': 200,
  'notification-create-empty-urls': 200,
  'notification-update': 200, 'notification-read-after-update': 200,
  'notification-delete': 200, 'notification-read-after-delete': 200,
  'notification-update-missing': 404, 'notification-delete-missing': 404,
  'backup-missing-download': 404, 'collection-empty-name': 400,
  'playlist-empty-items': 200,
  'item-download': 200,
  'item-file-download': 200, 'item-file-download-range': 206,
  'item-file-download-missing-item': 404, 'item-file-download-missing-file': 404,
  'item-file-download-disk-missing': 404,
  'item-file-download-denied': 403, 'item-file-download-denied-missing-file': 404,
  'playback-session': 200, 'hls-playlist': 200, 'session-close': 200,
  'socket-connect-prefixed': 200, 'socket-connect-legacy': 200,
  'socket-event-user-updated': 200
}

function parseArgs() {
  const out = {}
  for (let i = 2; i < process.argv.length; i++) {
    const key = process.argv[i]
    if (key.startsWith('--')) out[key.slice(2)] = process.argv[++i]
  }
  for (const key of ['node-url', 'go-url', 'fixture']) if (!out[key]) throw new Error(`missing --${key}`)
  return out
}

async function call(server, route, options = {}) {
  let requestBody
  if (options.file) {
    const form = new FormData()
    for (const [key, value] of Object.entries(options.fields || {})) form.append(key, value)
    const bytes = fs.readFileSync(options.file.path)
    form.append(options.file.field, new Blob([bytes], { type: options.file.type }), options.file.name || path.basename(options.file.path))
    requestBody = form
  } else if (options.body !== undefined) {
    requestBody = JSON.stringify(options.body)
  }
  const response = await fetch(`${server.url}${PREFIX}${route}`, {
    method: options.method || 'GET',
    redirect: 'manual',
    signal: AbortSignal.timeout(15000),
    headers: {
      ...(options.token ? { Authorization: `Bearer ${options.token}` } : {}),
      ...(options.refreshToken ? { 'X-Refresh-Token': options.refreshToken } : {}),
      ...(options.headers || {}),
      ...(options.body !== undefined && (options.method === 'POST' || options.method === 'PATCH') ? { 'Content-Type': 'application/json' } : {}),
      ...(options.method === 'POST' && route === '/login' ? { 'X-Return-Tokens': 'true' } : {})
    },
    body: requestBody
  })
  const raw = Buffer.from(await response.arrayBuffer())
  let body
  if ((response.headers.get('content-type') || '').includes('json')) {
    try { body = JSON.parse(raw.toString('utf8')) } catch { body = raw.toString('utf8') }
  } else {
    body = raw.toString('base64')
  }
  const headers = {}
  const headerNames = ['content-type', 'content-range', 'accept-ranges']
  if (route.includes('/cover')) {
    headerNames.push('cache-control', 'content-disposition')
    // ETag/content-length are only collected for cover responses whose bytes
    // are copy-independent (cache validation bodies are fixed text). The
    // upload JSON embeds per-copy absolute paths and raw responses carry
    // per-copy mtime-derived metadata, so those fields cannot match across
    // independent copies; their own bodies stay byte-compared where valid.
    if (!route.includes('raw=1') && (options.method || 'GET') !== 'POST') headerNames.push('etag')
    if ((options.method || 'GET') !== 'POST') headerNames.push('content-length')
  }
  if (route.startsWith('/api/items/') && route.endsWith('/download')) {
    headerNames.push('content-disposition', 'content-length', 'last-modified')
    if ([403, 404].includes(response.status)) headerNames.push('etag')
  }
  if ((route.startsWith('/api/emails/') || route.startsWith('/api/notifications')) && [400, 403, 404].includes(response.status)) {
    headerNames.push('etag', 'content-length')
  }
  if (route.startsWith('/api/notifications/') && route.endsWith('/test')) headerNames.push('etag', 'content-length')
  for (const key of headerNames) {
    const value = response.headers.get(key)
    if (value !== null) headers[key] = value
  }
  for (const key of options.collectHeaders || []) {
    const value = response.headers.get(key)
    if (value !== null) headers[key] = value
  }
  return { status: response.status, headers, body }
}

async function startCoverSource(file) {
  const server = require('node:http').createServer((req, res) => {
    if (req.url === '/redirect-307.png') {
      res.writeHead(307, { location: '/cover.png' })
      return res.end()
    }
    if (req.url === '/unsupported-format') {
      const unsupported = Buffer.from('FLIF')
      res.writeHead(200, { 'content-type': 'image/flif', 'content-length': String(unsupported.length) })
      return res.end(unsupported)
    }
    if (req.url !== '/cover.png') { res.writeHead(404); return res.end() }
    res.writeHead(200, { 'content-type': 'image/png', 'content-length': String(fs.statSync(file).size) })
    fs.createReadStream(file).pipe(res)
  })
  const port = await new Promise((resolve, reject) => server.listen(0, '127.0.0.1', (err) => err ? reject(err) : resolve(server.address().port)))
  server.unref()
  return { server, url: `http://127.0.0.1:${port}/cover.png` }
}

async function startSMTPFixture({ rejectConnection = false } = {}) {
  const net = require('node:net')
  const sessions = []
  const server = net.createServer((socket) => {
    const session = { mailFrom: null, recipients: [], message: '' }
    sessions.push(session)
    let input = ''
    let inData = false
    if (rejectConnection) {
      socket.end('421 4.3.0 service unavailable\r\n')
      return
    }
    socket.write('220 parity-smtp.local ESMTP\r\n')
    socket.on('data', chunk => {
      input += chunk.toString('utf8')
      while (true) {
        if (inData) {
          const end = input.indexOf('\r\n.\r\n')
          if (end < 0) break
          session.message += input.slice(0, end + 2)
          input = input.slice(end + 5)
          inData = false
          socket.write('250 2.0.0 queued\r\n')
          continue
        }
        const end = input.indexOf('\r\n')
        if (end < 0) break
        const line = input.slice(0, end)
        input = input.slice(end + 2)
        const command = line.split(' ', 1)[0].toUpperCase()
        if (command === 'EHLO' || command === 'HELO') socket.write('250-parity-smtp.local\r\n250 PIPELINING\r\n')
        else if (command === 'MAIL') {
          session.mailFrom = line.slice(line.indexOf(':') + 1).replace(/[<>]/g, '').trim()
          socket.write('250 2.1.0 sender ok\r\n')
        } else if (command === 'RCPT') {
          session.recipients.push(line.slice(line.indexOf(':') + 1).replace(/[<>]/g, '').trim())
          socket.write('250 2.1.5 recipient ok\r\n')
        } else if (command === 'DATA') {
          inData = true
          socket.write('354 end with <CRLF>.<CRLF>\r\n')
        } else if (command === 'QUIT') {
          socket.end('221 2.0.0 bye\r\n')
        } else socket.write('250 2.0.0 ok\r\n')
      }
    })
  })
  const port = await new Promise((resolve, reject) => server.listen(0, '127.0.0.1', error => error ? reject(error) : resolve(server.address().port)))
  server.unref()
  return { server, port, sessions }
}

async function startNotificationAppriseFixture() {
  const requests = { node: [], go: [] }
  const server = require('node:http').createServer((req, res) => {
    const side = req.url.split('/')[1]
    const chunks = []
    req.on('data', chunk => chunks.push(chunk))
    req.on('end', () => {
      if (!Object.hasOwn(requests, side)) {
        res.writeHead(404)
        return res.end()
      }
      let payload = null
      try { payload = JSON.parse(Buffer.concat(chunks).toString('utf8')) } catch {}
      requests[side].push({ path: req.url, payload })
      res.writeHead(req.url.endsWith('/failure') ? 503 : 200, { 'content-type': 'text/plain' })
      res.end(req.url.endsWith('/failure') ? 'fixture failure' : 'sent')
    })
  })
  const port = await new Promise((resolve, reject) => server.listen(0, '127.0.0.1', error => error ? reject(error) : resolve(server.address().port)))
  server.unref()
  return { server, requests, url: `http://127.0.0.1:${port}` }
}

function smtpMessageShape(session) {
  const normalized = session.message.replace(/\r\n/g, '\n')
  const split = normalized.indexOf('\n\n')
  const headers = Object.fromEntries(normalized.slice(0, split).split('\n').map(line => {
    const colon = line.indexOf(':')
    return [line.slice(0, colon).toLowerCase(), line.slice(colon + 1).trim()]
  }))
  return {
    mailFrom: session.mailFrom,
    recipients: session.recipients,
    from: headers.from,
    to: headers.to,
    subject: headers.subject,
    contentType: headers['content-type'],
    body: normalized.slice(split + 2).replace(/\n$/, '')
  }
}

async function readEmailDeviceState(directory) {
  const sqlite = require('sqlite3').verbose()
  const db = new sqlite.Database(path.join(directory, 'config', 'absdatabase.sqlite'))
  try {
    const row = await new Promise((resolve, reject) => db.get(
      "SELECT value,updatedAt FROM settings WHERE key='email-settings'",
      (err, value) => err ? reject(err) : resolve(value)
    ))
    if (!row) return { updatedAt: null, devices: [] }
    const settings = JSON.parse(row.value)
    return { updatedAt: row.updatedAt, devices: settings.ereaderDevices ?? [] }
  } finally {
    await new Promise((resolve) => db.close(resolve))
  }
}

async function readCoverDeleteState(directory, itemID) {
  const sqlite = require('sqlite3').verbose()
  const db = new sqlite.Database(path.join(directory, 'config', 'absdatabase.sqlite'))
  let coverPath
  try {
    coverPath = await new Promise((resolve, reject) => db.get(
      'SELECT b.coverPath FROM libraryItems li JOIN books b ON b.id=li.mediaId WHERE li.id=?',
      [itemID],
      (err, row) => err ? reject(err) : resolve(row?.coverPath)
    ))
  } finally {
    await new Promise((resolve) => db.close(resolve))
  }
  const cacheDir = path.join(directory, 'metadata', 'cache', 'covers')
  const cacheFiles = fs.existsSync(cacheDir)
    ? fs.readdirSync(cacheDir).filter((name) => name.startsWith(`${itemID}_`)).sort()
    : []
  const coverDir = path.join(directory, 'metadata', 'items', itemID)
  const coverFiles = fs.existsSync(coverDir)
    ? fs.readdirSync(coverDir).filter((name) => /^cover\./i.test(name)).sort()
    : []
  return { coverPath: coverPath ?? null, cacheFiles, coverFiles }
}

function deepSort(value) {
  if (Array.isArray(value)) return value.map(deepSort)
  if (!value || typeof value !== 'object') return value
  return Object.fromEntries(Object.keys(value).sort().map((key) => [key, deepSort(value[key])]))
}

function normalizeCase(id, body, pathRoots) {
  if (!body || typeof body !== 'object' || Array.isArray(body)) return deepSort(body)
  const normalized = structuredClone(body)
  // Independent media/config copies cannot share filesystem inode numbers and
  // each server reports its own copy's values; folder/creation timestamps and
  // per-copy uuids are dynamic. Real DTO differences (null vs "", [] vs null,
  // type changes, missing fields, wrong statuses/headers) are never normalized.
  globalInoReplacements(normalized)
  const requireField = (object, key) => {
    if (!object || typeof object !== 'object' || !Object.hasOwn(object, key)) {
      throw new Error(`${id}: required normalization field ${key} is missing`)
    }
  }
  const checkAndReplaceNumber = (object, key, replacement) => {
    requireField(object, key)
    const value = object[key]
    if (value !== null && typeof value !== 'number') throw new Error(`${id}: ${key} changed type; expected number or null`)
    object[key] = replacement
  }
  const checkAndReplaceInode = (object, key) => {
    requireField(object, key)
    const value = object[key]
    const alreadyReplaced = value === '<fixture-inode>'
    if (!alreadyReplaced && (typeof value !== 'string' || !/^\d+$/.test(value))) throw new Error(`${id}: ${key} is not a numeric inode string`)
    object[key] = '<fixture-inode>'
  }
  const checkAndReplaceToken = (object, key) => {
    requireField(object, key)
    if (typeof object[key] !== 'string' || object[key].length < 10) throw new Error(`${id}: ${key} is not a token string`)
    object[key] = '<dynamic-token>'
  }
  const checkAndFingerprintJWT = (object, key) => {
    requireField(object, key)
    const value = object[key]
    if (typeof value !== 'string' || value.split('.').length !== 3) throw new Error(`${id}: ${key} is not a compact JWT`)
    const fingerprint = crypto.createHash('sha256').update(value).digest('hex')
    object[key] = `<fixture-jwt-sha256:${fingerprint}>`
  }
  const checkAndReplacePath = (object, key) => {
    requireField(object, key)
    const value = object[key]
    if (typeof value !== 'string' || !value.endsWith('/metadata/backups')) throw new Error(`${id}: ${key} does not point into the fixture metadata directory`)
    object[key] = '<fixture-metadata>/backups'
  }
  const mediaPathCases = new Set([
    'libraries', 'libraries-restricted-user', 'library-create', 'library-read', 'library-update', 'library-list-after-create', 'library-delete', 'library-list-after-delete',
    'book-library-items', 'book-library-items-minified', 'book-item-detail',
    'book-list-after-library-scan', 'book-detail-after-library-scan', 'series-list-paginated', 'cover-upload', 'cover-url-upload', 'cover-url-redirect-307', 'cover-patch-success', 'cover-patch-repeat',
    'collection-create', 'collection-read', 'collection-update', 'collection-list', 'collection-batch-add', 'collection-batch-remove', 'collection-list-after-delete',
    'playlist-create', 'playlist-read', 'playlist-update', 'playlist-list', 'playlist-batch-add', 'playlist-batch-remove', 'playlist-list-after-delete',
    'api-key-auth-library-read', 'playback-session', 'libraries-unauthorized'
  ])
  if (pathRoots && mediaPathCases.has(id)) {
    const mediaPathKeys = new Set(['fullPath', 'path', 'coverPath', 'imagePath', 'cover'])
    let seen = 0
    const walkPaths = (value) => {
      if (Array.isArray(value)) return value.map(walkPaths)
      if (!value || typeof value !== 'object') return value
      for (const [key, child] of Object.entries(value)) {
        if (!mediaPathKeys.has(key) || child === null) {
          value[key] = walkPaths(child)
          continue
        }
        if (typeof child !== 'string') throw new Error(`${id}: ${key} is not a string path`)
        const roots = key === 'fullPath' || key === 'path'
          ? [['media', pathRoots.media]]
          : [['media', pathRoots.media], ['metadata', pathRoots.metadata]]
        const matchedRoot = roots.find(([, root]) => typeof root === 'string' && child.startsWith(root))
        if (!matchedRoot) throw new Error(`${id}: ${key} is not rooted under a known fixture media or metadata directory`)
        value[key] = `<fixture-${matchedRoot[0]}>${child.slice(matchedRoot[1].length)}`
        seen++
      }
      return value
    }
    walkPaths(normalized)
    // Bodies without any media-path fields are valid (e.g. batch-remove
    // responses after items were removed); only non-fixture-rooted path
    // values above are errors.
  }
  if (['login', 'refresh', 'reader-login', 'local-session-login', 'local-session-relogin', 'refresh-rotation', 'refresh-rotation-grace-replay'].includes(id)) {
    const user = normalized.user
    if (!user || typeof user !== 'object' || Array.isArray(user)) throw new Error(`${id}: user object is missing`)
    if (Array.isArray(user.mediaProgress)) {
      user.mediaProgress.forEach((progress) => {
        checkAndReplaceNumber(progress, 'finishedAt', '<dynamic-time>')
        checkAndReplaceNumber(progress, 'lastUpdate', '<dynamic-time>')
      })
    }
    checkAndReplaceToken(user, 'accessToken')
    checkAndReplaceToken(user, 'refreshToken')
    checkAndFingerprintJWT(user, 'token')
    checkAndReplaceNumber(user, 'lastSeen', '<dynamic-time>')
  }
  // Upstream POST /api/authorize (Auth.js:96-105) returns the login payload
  // WITHOUT accessToken/refreshToken; those are added only by
  // handleLoginSuccess (Auth.js:296-312) on the login/refresh routes.
  if (id === 'authorize') {
    const user = normalized.user
    if (!user || typeof user !== 'object' || Array.isArray(user)) throw new Error('authorize: user object is missing')
    if (Array.isArray(user.mediaProgress)) {
      user.mediaProgress.forEach((progress) => {
        checkAndReplaceNumber(progress, 'finishedAt', '<dynamic-time>')
        checkAndReplaceNumber(progress, 'lastUpdate', '<dynamic-time>')
      })
    }
    checkAndReplaceNumber(user, 'lastSeen', '<dynamic-time>')
    checkAndFingerprintJWT(user, 'token')
  }
  if (['login', 'refresh', 'reader-login', 'authorize', 'local-session-login', 'local-session-relogin', 'refresh-rotation', 'refresh-rotation-grace-replay'].includes(id)) {
    if (!normalized.serverSettings || typeof normalized.serverSettings !== 'object') throw new Error(`${id}: serverSettings object is missing`)
    checkAndReplacePath(normalized.serverSettings, 'backupPath')
  }
  if (id === 'progress-read') {
    if (normalized === null || typeof normalized !== 'object' || Array.isArray(normalized)) throw new Error('progress-read: progress object is missing')
    checkAndReplaceNumber(normalized, 'lastUpdate', '<dynamic-time>')
    checkAndReplaceNumber(normalized, 'startedAt', '<dynamic-time>')
    checkAndReplaceNumber(normalized, 'finishedAt', '<dynamic-time>')
  }
  if (['bookmark-create', 'bookmark-update'].includes(id)) {
    if (!normalized || typeof normalized !== 'object' || Array.isArray(normalized)) throw new Error(`${id}: bookmark object is missing`)
    checkAndReplaceNumber(normalized, 'createdAt', '<dynamic-time>')
  }
  if (['bookmark-read-after-create', 'bookmark-read-after-delete'].includes(id)) {
    if (!normalized || typeof normalized !== 'object' || !Array.isArray(normalized.bookmarks)) throw new Error(`${id}: bookmarks array is missing`)
    for (const bookmark of normalized.bookmarks) checkAndReplaceNumber(bookmark, 'createdAt', '<dynamic-time>')
  }
  if (id === 'book-list-after-library-scan') {
    if (!normalized || !Array.isArray(normalized.results) || normalized.results.length === 0) throw new Error(`${id}: results array is missing`)
    for (const item of normalized.results) {
      checkAndReplaceNumber(item, 'updatedAt', '<dynamic-time>')
      checkAndReplaceNumber(item, 'birthtimeMs', '<dynamic-time>')
      checkAndReplaceNumber(item, 'ctimeMs', '<dynamic-time>')
      checkAndReplaceNumber(item, 'mtimeMs', '<dynamic-time>')
      checkAndReplaceInode(item, 'ino')
    }
  }
  // Shared: item/file inode and dynamic-time normalization. Independent
  // fixture copies cannot share inode numbers, and per-server timestamps are
  // dynamic. Fields missing on one side remain visible as differences against
  // the other side's placeholder.
  const normalizeItemSummary = (item) => {
    for (const key of ['updatedAt', 'birthtimeMs', 'ctimeMs', 'mtimeMs']) {
      if (Object.hasOwn(item, key)) checkAndReplaceNumber(item, key, '<dynamic-time>')
    }
    if (Object.hasOwn(item, 'ino')) checkAndReplaceInode(item, 'ino')
    if (Object.hasOwn(item, 'lastScan')) checkAndReplaceNumber(item, 'lastScan', '<dynamic-time>')
    if (Array.isArray(item.libraryFiles)) {
      for (const file of item.libraryFiles) {
        for (const key of ['addedAt', 'updatedAt']) {
          if (Object.hasOwn(file, key)) checkAndReplaceNumber(file, key, '<dynamic-time>')
        }
        if (Object.hasOwn(file, 'ino')) checkAndReplaceInode(file, 'ino')
        if (file.metadata && typeof file.metadata === 'object') {
          for (const key of ['birthtimeMs', 'ctimeMs', 'mtimeMs']) {
            if (Object.hasOwn(file.metadata, key)) checkAndReplaceNumber(file.metadata, key, '<dynamic-time>')
          }
        }
      }
    }
    const audioFiles = item.media && Array.isArray(item.media.audioFiles) ? item.media.audioFiles : []
    for (const file of audioFiles) {
      if (file && typeof file === 'object') {
        if (Object.hasOwn(file, 'ino')) checkAndReplaceInode(file, 'ino')
        for (const key of ['addedAt', 'updatedAt']) {
          if (Object.hasOwn(file, key)) checkAndReplaceNumber(file, key, '<dynamic-time>')
        }
        if (file.metadata && typeof file.metadata === 'object') {
          for (const key of ['birthtimeMs', 'ctimeMs', 'mtimeMs']) {
            if (Object.hasOwn(file, 'metadata') && Object.hasOwn(file.metadata, key)) checkAndReplaceNumber(file.metadata, key, '<dynamic-time>')
          }
        }
      }
    }
    // Playback/playback-track serializations carry per-copy inode and mtime
    // values for every media file; normalize each track the same way.
    const tracks = item.media && Array.isArray(item.media.tracks) ? item.media.tracks : []
    for (const track of tracks) {
      if (!track || typeof track !== 'object') continue
      for (const key of ['addedAt', 'updatedAt']) {
        if (Object.hasOwn(track, key)) checkAndReplaceNumber(track, key, '<dynamic-time>')
      }
      if (Object.hasOwn(track, 'ino')) checkAndReplaceInode(track, 'ino')
      if (track.metadata && typeof track.metadata === 'object') {
        for (const key of ['birthtimeMs', 'ctimeMs', 'mtimeMs']) {
          if (Object.hasOwn(track.metadata, key)) checkAndReplaceNumber(track.metadata, key, '<dynamic-time>')
        }
      }
    }
  }
  if (['book-library-items', 'book-library-items-minified'].includes(id)) {
    if (!normalized || !Array.isArray(normalized.results) || normalized.results.length === 0) throw new Error(`${id}: results array is missing`)
    normalized.results.forEach(normalizeItemSummary)
  }
  if (id === 'series-list-paginated') {
    if (!Array.isArray(normalized.results) || normalized.results.length !== 2) throw new Error(`${id}: expected two paginated series results`)
    const uuid = /^[0-9a-f]{8}-[0-9a-f]{4}-[1-8][0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/i
    for (const series of normalized.results) {
      for (const key of ['id', 'libraryId']) {
        requireField(series, key)
        if (typeof series[key] !== 'string' || !uuid.test(series[key])) throw new Error(`${id}: ${key} is not a UUID`)
      }
      for (const key of ['addedAt', 'updatedAt']) if (Object.hasOwn(series, key)) checkAndReplaceNumber(series, key, '<dynamic-time>')
      if (!Array.isArray(series.books)) throw new Error(`${id}: series books array is missing`)
      for (const book of series.books) {
        for (const key of ['addedAt', 'updatedAt', 'birthtimeMs', 'ctimeMs', 'mtimeMs']) if (Object.hasOwn(book, key)) checkAndReplaceNumber(book, key, '<dynamic-time>')
        if (Object.hasOwn(book, 'ino')) checkAndReplaceInode(book, 'ino')
        if (book.media && typeof book.media === 'object') {
          for (const key of ['addedAt', 'updatedAt']) if (Object.hasOwn(book.media, key)) checkAndReplaceNumber(book.media, key, '<dynamic-time>')
        }
      }
    }
  }
  if (id === 'book-item-detail') {
    if (!normalized || typeof normalized !== 'object') throw new Error(`${id}: item detail is missing`)
    normalizeItemSummary(normalized)
  }
  if (id === 'book-detail-after-library-scan') {
    if (!normalized || typeof normalized !== 'object') throw new Error(`${id}: item detail is missing`)
    checkAndReplaceNumber(normalized, 'lastScan', '<dynamic-time>')
    checkAndReplaceNumber(normalized, 'updatedAt', '<dynamic-time>')
    checkAndReplaceNumber(normalized, 'birthtimeMs', '<dynamic-time>')
    checkAndReplaceNumber(normalized, 'ctimeMs', '<dynamic-time>')
    checkAndReplaceNumber(normalized, 'mtimeMs', '<dynamic-time>')
    checkAndReplaceInode(normalized, 'ino')
    if (!Array.isArray(normalized.libraryFiles) || normalized.libraryFiles.length === 0) throw new Error(`${id}: library files are missing`)
    for (const file of normalized.libraryFiles) {
      checkAndReplaceNumber(file, 'addedAt', '<dynamic-time>')
      checkAndReplaceNumber(file, 'updatedAt', '<dynamic-time>')
      checkAndReplaceInode(file, 'ino')
      if (!file.metadata || typeof file.metadata !== 'object') throw new Error(`${id}: file metadata is missing`)
      checkAndReplaceNumber(file.metadata, 'birthtimeMs', '<dynamic-time>')
      checkAndReplaceNumber(file.metadata, 'ctimeMs', '<dynamic-time>')
      checkAndReplaceNumber(file.metadata, 'mtimeMs', '<dynamic-time>')
    }
    // Independent media copies cannot share filesystem inode numbers, but audio
    // entries must still carry numeric inode strings on both sides.
    const audioFiles = normalized.media && Array.isArray(normalized.media.audioFiles) ? normalized.media.audioFiles : []
    for (const file of audioFiles) {
      if (file && typeof file === 'object') {
        checkAndReplaceInode(file, 'ino')
        checkAndReplaceNumber(file, 'addedAt', '<dynamic-time>')
        checkAndReplaceNumber(file, 'updatedAt', '<dynamic-time>')
        if (file.metadata && typeof file.metadata === 'object') {
          checkAndReplaceNumber(file.metadata, 'birthtimeMs', '<dynamic-time>')
          checkAndReplaceNumber(file.metadata, 'ctimeMs', '<dynamic-time>')
          checkAndReplaceNumber(file.metadata, 'mtimeMs', '<dynamic-time>')
        }
      }
    }
  }
  if (id === 'playback-session') {
    const uuid = /^[0-9a-f]{8}-[0-9a-f]{4}-[1-8][0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/i
    if (typeof normalized.id !== 'string' || !uuid.test(normalized.id)) throw new Error('playback-session: session ID is missing or malformed')
    const sessionID = normalized.id
    normalized.id = '<dynamic-session-uuid>'
    checkAndReplaceNumber(normalized, 'startedAt', '<dynamic-time>')
    checkAndReplaceNumber(normalized, 'updatedAt', '<dynamic-time>')
    if (!normalized.deviceInfo || typeof normalized.deviceInfo !== 'object') throw new Error('playback-session: device info is missing')
    requireField(normalized.deviceInfo, 'id')
    if (typeof normalized.deviceInfo.id !== 'string' || !uuid.test(normalized.deviceInfo.id)) throw new Error('playback-session: device ID is missing or malformed')
    normalized.deviceInfo.id = '<dynamic-device-uuid>'
    if (!Array.isArray(normalized.audioTracks) || normalized.audioTracks.length === 0) throw new Error('playback-session: audio tracks are missing')
    for (const track of normalized.audioTracks) {
      if (!track || typeof track !== 'object' || typeof track.contentUrl !== 'string') throw new Error('playback-session: audio track content URL is missing')
      track.contentUrl = track.contentUrl.replace(sessionID, '<dynamic-session-uuid>')
    }
    // The creation response embeds the expanded library item; its ino/times
    // are copy-specific like every other item serialization.
    if (normalized.libraryItem && typeof normalized.libraryItem === 'object') normalizeItemSummary(normalized.libraryItem)
  }
  const normalizeNotifications = (settings) => {
    if (!settings || typeof settings !== 'object' || !Array.isArray(settings.notifications)) {
      throw new Error(`${id}: notification settings list is missing`)
    }
    const uuid = /^[0-9a-f]{8}-[0-9a-f]{4}-[1-8][0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/i
    for (const notification of settings.notifications) {
      requireField(notification, 'id')
      if (typeof notification.id !== 'string' || !uuid.test(notification.id)) throw new Error(`${id}: notification id is not a UUID`)
      notification.id = '<dynamic-notification-uuid>'
      checkAndReplaceNumber(notification, 'createdAt', '<dynamic-time>')
      if (notification.lastFiredAt !== null && notification.lastFiredAt !== undefined) checkAndReplaceNumber(notification, 'lastFiredAt', '<dynamic-time>')
    }
  }
  if (['notification-create', 'notification-create-minimal', 'notification-create-empty-urls', 'notification-update', 'notification-update-without-body-id', 'notification-delete'].includes(id)) normalizeNotifications(normalized)
  else if (['notification-read-after-create', 'notification-read-after-update', 'notification-read-after-delete', 'notification-read-after-test-success', 'notification-read-after-test-failure', 'notification-read-after-event-test-success', 'notification-read-after-event-test-repeat', 'notification-read-after-event-test-failure'].includes(id)) normalizeNotifications(normalized.settings)
  const normalizeAppriseFixtureURL = (settings) => {
    if (typeof settings?.appriseApiUrl === 'string' && /^http:\/\/127\.0\.0\.1:\d+\/(node|go)\/(success|failure)$/.test(settings.appriseApiUrl)) {
      settings.appriseApiUrl = '<fixture-local-apprise-url>'
    }
  }
  if (id.startsWith('notification-') || id.startsWith('notifications-')) {
    normalizeAppriseFixtureURL(normalized.settings)
    normalizeAppriseFixtureURL(normalized)
  }
  if (id.startsWith('notification-event-broadcast-') && normalized && Array.isArray(normalized.events)) {
    normalized.events.forEach(event => {
      normalizeNotifications(event)
      normalizeAppriseFixtureURL(event)
    })
  }
  const normalizeEntity = (entity, entityName) => {
    if (!entity || typeof entity !== 'object' || Array.isArray(entity)) throw new Error(`${id}: ${entityName} entity is missing`)
    const uuid = /^[0-9a-f]{8}-[0-9a-f]{4}-[1-8][0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/i
    requireField(entity, 'id')
    if (typeof entity.id !== 'string' || !uuid.test(entity.id)) throw new Error(`${id}: ${entityName}.id is not a UUID (got ${JSON.stringify(entity.id)})`)
    entity.id = '<dynamic-entity-uuid>'
    checkAndReplaceNumber(entity, 'createdAt', '<dynamic-time>')
    checkAndReplaceNumber(entity, 'lastUpdate', '<dynamic-time>')
  }
  if (['collection-create', 'collection-read', 'collection-update', 'collection-batch-add', 'collection-batch-remove'].includes(id)) {
    normalizeEntity(normalized, 'collection')
    if (Array.isArray(normalized.books)) normalized.books.forEach(normalizeItemSummary)
  }
  if (id === 'collection-list' || id === 'collection-list-after-delete') {
    if (!Array.isArray(normalized.collections)) throw new Error('collection-list: collections array is missing')
    normalized.collections.forEach((entity) => {
      normalizeEntity(entity, 'collections')
      if (Array.isArray(entity.books)) entity.books.forEach(normalizeItemSummary)
    })
  }
  if (['playlist-create', 'playlist-read', 'playlist-update', 'playlist-batch-add', 'playlist-batch-remove', 'playlist-empty-items'].includes(id)) {
    normalizeEntity(normalized, 'playlist')
    if (Array.isArray(normalized.items)) {
      for (const entry of normalized.items) {
        if (entry && typeof entry.libraryItem === 'object') normalizeItemSummary(entry.libraryItem)
      }
    }
  }
  if (id === 'playlist-list' || id === 'playlist-list-after-delete') {
    if (!Array.isArray(normalized.playlists)) throw new Error('playlist-list: playlists array is missing')
    normalized.playlists.forEach((entity) => {
      normalizeEntity(entity, 'playlists')
      if (Array.isArray(entity.items)) {
        for (const entry of entity.items) {
          if (entry && typeof entry.libraryItem === 'object') normalizeItemSummary(entry.libraryItem)
        }
      }
    })
  }
  const normalizeUUID = (object, key, marker) => {
    requireField(object, key)
    const uuid = /^[0-9a-f]{8}-[0-9a-f]{4}-[1-8][0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/i
    if (typeof object[key] !== 'string' || !uuid.test(object[key])) throw new Error(`${id}: ${key} is not a UUID`)
    object[key] = marker
  }
  const normalizeLibrary = (entity) => {
    normalizeEntity(entity, 'library')
    if (!Array.isArray(entity.folders)) throw new Error(`${id}: library folders are missing`)
    for (const folder of entity.folders) {
      normalizeUUID(folder, 'id', '<dynamic-folder-uuid>')
      normalizeUUID(folder, 'libraryId', '<dynamic-entity-uuid>')
    }
  }
  // Folder addedAt/updatedAt are computed independently per server at
  // creation time (a few ms apart); both must be numeric when present.
  const normalizeFolderTimestamps = (folders, caseId) => {
    if (!Array.isArray(folders)) return
    for (const folder of folders) {
      for (const key of ['addedAt', 'updatedAt', 'createdAt']) {
        if (folder && typeof folder === 'object' && Object.hasOwn(folder, key)) checkAndReplaceNumber(folder, key, '<dynamic-time>')
      }
    }
  }
  if (['library-create', 'library-read', 'library-update', 'library-delete'].includes(id)) {
    normalizeLibrary(normalized)
    if (Array.isArray(normalized.folders)) normalizeFolderTimestamps(normalized.folders, id)
  }
  if (['library-list-after-create', 'library-list-after-delete'].includes(id)) {
    if (!Array.isArray(normalized.libraries)) throw new Error(`${id}: libraries array is missing`)
    normalized.libraries.forEach(normalizeLibrary)
    for (const entity of normalized.libraries) normalizeFolderTimestamps(entity.folders, id)
  }
  const normalizeUser = (entity, perSideJWT = false) => {
    if (!entity || typeof entity !== 'object' || Array.isArray(entity)) throw new Error(`${id}: user object is missing`)
    const isParityUser = entity.username === 'parity-user' || entity.username === 'parity-inactive-user'
    if (isParityUser || perSideJWT) normalizeUUID(entity, 'id', '<dynamic-user-uuid>')
    else requireField(entity, 'id')
    if (Object.hasOwn(entity, 'lastSeen')) checkAndReplaceNumber(entity, 'lastSeen', '<dynamic-time>')
    if (Object.hasOwn(entity, 'token')) {
      if (isParityUser || perSideJWT) {
        if (typeof entity.token !== 'string' || entity.token.split('.').length !== 3) throw new Error(`${id}: parity user token is not a compact JWT`)
        // Independently keyed servers issue different JWTs for these rows.
        entity.token = '<per-side-jwt>'
      } else checkAndFingerprintJWT(entity, 'token')
    }
    if (Object.hasOwn(entity, 'createdAt')) checkAndReplaceNumber(entity, 'createdAt', '<dynamic-time>')
    if (Object.hasOwn(entity, 'updatedAt')) checkAndReplaceNumber(entity, 'updatedAt', '<dynamic-time>')
  }
  if (['user-create', 'user-update', 'user-read', 'inactive-user-create'].includes(id)) normalizeUser(normalized.user || normalized)
  if (id === 'user-create-forbidden') normalizeUser(normalized.user || normalized, true)
  if (['user-list', 'user-list-after-delete'].includes(id)) {
    if (!Array.isArray(normalized.users)) throw new Error(`${id}: users array is missing`)
    normalized.users.forEach(normalizeUser)
  }
  const normalizeApiKey = (apiKey, includeSecret) => {
    if (!apiKey || typeof apiKey !== 'object' || Array.isArray(apiKey)) throw new Error(`${id}: API key object is missing`)
    normalizeUUID(apiKey, 'id', '<dynamic-api-key-uuid>')
    requireField(apiKey, 'userId')
    // Upstream uses Sequelize toJSON() (ApiKeyController.js:38,100,169), which
    // serializes DATE attributes as ISO date strings; the Go side emits epoch
    // millis. Both are dynamic times.
    const replaceApiTime = (key) => {
      requireField(apiKey, key)
      const value = apiKey[key]
      const okNumber = value === null || typeof value === 'number'
      const okISO = typeof value === 'string' && Number.isFinite(Date.parse(value))
      if (!okNumber && !okISO) throw new Error(`${id}: ${key} changed type; expected number, ISO date string or null`)
      apiKey[key] = '<dynamic-time>'
    }
    replaceApiTime('createdAt')
    replaceApiTime('updatedAt')
    if (apiKey.expiresAt !== null) {
      if (typeof apiKey.expiresAt !== 'string' || !Number.isFinite(Date.parse(apiKey.expiresAt))) throw new Error(`${id}: API key expiry is not a date string or null`)
      apiKey.expiresAt = '<dynamic-time>'
    }
    if (apiKey.user && typeof apiKey.user === 'object' && apiKey.user.username === 'parity-user') {
      apiKey.userId = '<dynamic-user-uuid>'
      normalizeUUID(apiKey.user, 'id', '<dynamic-user-uuid>')
    }
    if (includeSecret) checkAndReplaceToken(apiKey, 'apiKey')
  }
  if (['api-key-create', 'api-key-update'].includes(id)) normalizeApiKey(normalized.apiKey, id === 'api-key-create')
  if (id === 'api-key-list') {
    if (!Array.isArray(normalized.apiKeys)) throw new Error('api-key-list: apiKeys array is missing')
    normalized.apiKeys.forEach((apiKey) => normalizeApiKey(apiKey, false))
  }
  // Library entity normalization is performed by normalizeLibrary above;
  // a second normalizeEntity here would re-check the already-replaced id.
  if (['session-list', 'session-list-after-delete', 'local-session-logout-session-list'].includes(id)) {
    if (!normalized || !Array.isArray(normalized.sessions)) throw new Error(`${id}: sessions array is missing`)
    for (const session of normalized.sessions) {
      const uuid = /^[0-9a-f]{8}-[0-9a-f]{4}-[1-8][0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/i
      requireField(session, 'id')
      if (typeof session.id !== 'string' || !uuid.test(session.id)) throw new Error(`${id}: session ID is not a UUID`)
      session.id = '<dynamic-session-uuid>'
      checkAndReplaceNumber(session, 'createdAt', '<dynamic-time>')
      checkAndReplaceNumber(session, 'updatedAt', '<dynamic-time>')
    }
  }
  return deepSort(normalized)
}

// Zip responses cannot be compared byte-for-byte: local-file header fields
// (per-entry metadata ordering, filesystem block data) legitimately differ
// between implementations. Canonicalize both archives to the sorted set of
// name + size + CRC32 (unzip -lv); equal CRC entries prove byte-equal content
// because both zippers use STORE mode (upstream zlib level 0).
function canonicalizeZipEntries(id, body) {
  if (id !== 'item-download' || typeof body !== 'string' || body.length < 64) return body
  let buffer
  try { buffer = Buffer.from(body, 'base64') } catch { return body }
  if (buffer.length < 4 || buffer.slice(0, 2).toString('latin1') !== 'PK') return body
  const tmp = path.join(os.tmpdir(), `parity-zip-${crypto.randomUUID()}.zip`)
  try {
    fs.writeFileSync(tmp, buffer)
    const out = spawnSync('unzip', ['-lv', tmp], { encoding: 'utf8' })
    if (out.error || out.status !== 0) return `unzip-failed:${out.status}:${String(out.error || out.stderr).slice(0, 200)}`
    const entries = []
    for (const line of out.stdout.split('\n')) {
      const match = line.match(/^\s*(\d+)\s+\w+\s+\d+\s+\d{1,3}%\s+\S+\s+\S+\s+([0-9A-Fa-f]{8})\s+(.*)$/)
      if (match) entries.push({ length: Number(match[1]), crc32: match[2], name: match[3] })
    }
    if (!entries.length) return 'unzip-no-entries'
    entries.sort((a, b) => (a.name < b.name ? -1 : 1))
    return JSON.stringify(entries)
  } finally {
    fs.rmSync(tmp, { force: true })
  }
}

function compare(id, left, right, notes = []) {
  let leftNormalized
  let rightNormalized
  try {
    leftNormalized = normalizeCase(id, canonicalizeZipEntries(id, left.body), FIXTURE_PATH_ROOTS?.node)
    rightNormalized = normalizeCase(id, canonicalizeZipEntries(id, right.body), FIXTURE_PATH_ROOTS?.go)
  } catch (error) {
    // Record the failure with the raw bodies instead of aborting the whole
    // run: a single unexpected shape must not discard all other evidence.
    return {
      id,
      passed: false,
      error: String(error && error.message ? error.message : error),
      referenceStatus: left.status,
      goStatus: right.status,
      referenceBody: left.body,
      goBody: right.body,
      differences: [{ path: '$', error: String(error && error.message ? error.message : error) }]
    }
  }
  const expectedStatus = EXPECTED_STATUS[id]
  const leftHeaders = { ...left.headers }
  const rightHeaders = { ...right.headers }
  let expressJSONHeadersMatch = true
  if (id === 'cover-patch-success' || id === 'cover-patch-repeat') {
    const validate = (response) => {
      const body = Buffer.from(JSON.stringify(response.body))
      const tag = `W/\"${body.length.toString(16)}-${crypto.createHash('sha1').update(body).digest('base64').replace(/=+$/, '')}\"`
      return response.headers['content-type'] === 'application/json; charset=utf-8' &&
        response.headers['content-length'] === String(body.length) && response.headers.etag === tag
    }
    expressJSONHeadersMatch = validate(left) && validate(right)
    for (const headers of [leftHeaders, rightHeaders]) {
      headers['content-length'] = '<express-json-length-validated-per-side>'
      headers.etag = '<express-etag-validated-per-side>'
    }
    notes.push('Content-Length and Express weak ETag are validated against each side’s exact JSON bytes; path-derived header values are normalized only after that check.')
  }
  const passed = expressJSONHeadersMatch && left.status === right.status && left.status === expectedStatus && JSON.stringify(leftHeaders) === JSON.stringify(rightHeaders) && JSON.stringify(leftNormalized) === JSON.stringify(rightNormalized)
  const differences = passed ? [] : differencePaths(left.status, right.status, leftHeaders, rightHeaders, leftNormalized, rightNormalized)
  if (!expressJSONHeadersMatch) differences.push({ path: '$.headers', error: 'successful PATCH did not return matching Express JSON headers for its exact body' })
  if (expectedStatus !== undefined && (left.status !== expectedStatus || right.status !== expectedStatus)) {
    differences.push({ path: '$.expectedStatus', expected: expectedStatus, reference: left.status, go: right.status })
  }
  return {
    id,
    passed,
    referenceStatus: left.status,
    goStatus: right.status,
    referenceHeaders: leftHeaders,
    goHeaders: rightHeaders,
    referenceBody: leftNormalized,
    goBody: rightNormalized,
    differences,
    notes
  }
}

function preview(value) {
  if (typeof value === 'string' && value.length > 240) {
    return { type: 'string', length: value.length, sha256: crypto.createHash('sha256').update(value).digest('hex'), prefix: value.slice(0, 80) }
  }
  return value
}

function differencePaths(leftStatus, rightStatus, leftHeaders, rightHeaders, left, right) {
  const diffs = []
  if (leftStatus !== rightStatus) diffs.push({ path: '$.status', reference: leftStatus, go: rightStatus })
  if (JSON.stringify(leftHeaders) !== JSON.stringify(rightHeaders)) diffs.push({ path: '$.headers', reference: leftHeaders, go: rightHeaders })
  const walk = (a, b, pointer) => {
    if (a === b) return
    if (Array.isArray(a) && Array.isArray(b)) {
      if (a.length !== b.length) diffs.push({ path: `${pointer}.length`, reference: a.length, go: b.length })
      for (let i = 0; i < Math.min(a.length, b.length); i++) walk(a[i], b[i], `${pointer}[${i}]`)
      return
    }
    const objectA = a !== null && typeof a === 'object' && !Array.isArray(a)
    const objectB = b !== null && typeof b === 'object' && !Array.isArray(b)
    if (objectA && objectB) {
      for (const key of [...new Set([...Object.keys(a), ...Object.keys(b)])].sort()) {
        if (!Object.hasOwn(a, key)) diffs.push({ path: `${pointer}.${key}`, reference: '<missing>', go: preview(b[key]) })
        else if (!Object.hasOwn(b, key)) diffs.push({ path: `${pointer}.${key}`, reference: preview(a[key]), go: '<missing>' })
        else walk(a[key], b[key], `${pointer}.${key}`)
      }
      return
    }
    diffs.push({ path: pointer, reference: preview(a), go: preview(b) })
  }
  walk(left, right, '$')
  return diffs
}

function loadSocketIOClient() {
  try {
    const socketIoClient = require('socket.io-client')
    return socketIoClient.io || socketIoClient
  } catch (error) {
    const candidatePaths = [
      process.env.ABS_PARITY_SOCKETIO_PATH,
      path.resolve(__dirname, '../../../client/node_modules/socket.io-client'),
      '/usr/lib/node_modules/socket.io-client'
    ].filter(Boolean)
    for (const candidate of candidatePaths) {
      try {
        const resolved = require(candidate)
        return resolved.io || resolved
      } catch { /* try next */ }
    }
    if (process.env.ABS_PARITY_REQUIRE_SOCKETIO === '1') throw new Error(`Socket.IO JS client is required but unavailable: ${error.message}`)
    return null
  }
}

function socketNotificationProbe(io, server, socketPath, token, route) {
  return new Promise((resolve, reject) => {
    const socket = io(server.url, { path: socketPath, timeout: 8000, reconnection: false, transports: ['polling', 'websocket'] })
    let settled = false
    const received = []
    const finish = (fn, value) => {
      if (settled) return
      settled = true
      clearTimeout(timer)
      socket.disconnect()
      fn(value)
    }
    const timer = setTimeout(() => finish(reject, new Error('notification socket probe timed out')), 12000)
    socket.once('connect', () => socket.emit('auth', token))
    socket.once('auth_failed', payload => finish(reject, new Error(`notification socket auth failed: ${JSON.stringify(payload)}`)))
    socket.once('connect_error', error => finish(reject, error))
    socket.once('init', async () => {
      socket.on('notifications_updated', payload => received.push(payload))
      try {
        const response = await call(server, route, { token })
        const deadline = Date.now() + 2500
        while (received.length === 0 && Date.now() < deadline) await new Promise(resolveWait => setTimeout(resolveWait, 25))
        finish(resolve, { response, events: received })
      } catch (error) {
        finish(reject, error)
      }
    })
  })
}

function socketAuthentication(io, url, socketPath, token) {
  return new Promise((resolve) => {
    const socket = io(url, {
      path: socketPath,
      timeout: 8000,
      reconnection: false,
      transports: ['polling', 'websocket']
    })
    let finished = false
    const finish = (status, body) => {
      if (finished) return
      finished = true
      clearTimeout(timer)
      socket.disconnect()
      resolve({ status, headers: {}, body })
    }
    const timer = setTimeout(() => finish(599, { error: 'Socket.IO authentication timed out' }), 10000)
    socket.once('connect', () => socket.emit('auth', token))
    socket.once('init', (payload) => finish(200, payload))
    socket.once('auth_failed', (payload) => finish(401, payload))
    socket.once('connect_error', (error) => finish(599, { error: error.message }))
  })
}

// Realtime event semantics per user: patching the READER's progress emits
// 'user_updated' only to the owning user (MeController.js:281-286
// clientEmitter(req.user.id, ...)); a root client must not receive it.
function socketUserUpdatedProbe(io, url, socketPath, { readerToken, rootToken, progressRoute, expectedUsername }) {
  const openSocket = (token) => new Promise((resolve, reject) => {
    const socket = io(url, { path: socketPath, timeout: 8000, reconnection: false, transports: ['polling', 'websocket'] })
    let settled = false
    const settle = (fn, value) => { if (!settled) { settled = true; fn(value) } }
    socket.once('connect', () => socket.emit('auth', token))
    socket.once('init', (payload) => settle(resolve, { socket, payload }))
    socket.once('auth_failed', (payload) => settle(reject, new Error(`auth_failed ${JSON.stringify(payload).slice(0, 100)}`)))
    socket.once('connect_error', (error) => settle(reject, new Error(`connect_error ${error.message}`)))
    setTimeout(() => settle(reject, new Error('socket auth timeout')), 10000)
  })
  return (async () => {
    const reader = await openSocket(readerToken)
    const root = await openSocket(rootToken)
    const received = { reader: null, root: null }
    reader.socket.once('user_updated', (payload) => { received.reader = payload })
    root.socket.once('user_updated', (payload) => { received.root = payload })
    const patch = await fetch(`${url}${progressRoute}`, {
      method: 'PATCH',
      signal: AbortSignal.timeout(15000),
      headers: { 'Content-Type': 'application/json', Authorization: `Bearer ${readerToken}` },
      body: JSON.stringify({ currentTime: 1.23, duration: 2, progress: 0.6 })
    })
    if (patch.status !== 200) throw new Error(`progress patch returned ${patch.status}`)
    const wait = (ms) => new Promise((resolve) => setTimeout(resolve, ms))
    const deadline = Date.now() + 8000
    while (received.reader === null && Date.now() < deadline) await wait(100)
    await wait(2000) // grace window to detect a forbidden root delivery
    const result = {
      status: 200,
      body: {
        username: received.reader?.username ?? null,
        mediaProgressCount: Array.isArray(received.reader?.mediaProgress) ? received.reader.mediaProgress.length : null,
        rootReceived: received.root !== null
      }
    }
    reader.socket.disconnect()
    root.socket.disconnect()
    return result
  })().catch((error) => ({ status: 599, body: { error: error.message } }))
}

async function main() {
  const args = parseArgs()
  const requestedCases = new Set((args.cases || '').split(',').map(value => value.trim()).filter(Boolean))
  const selectedRun = requestedCases.size > 0
  const knownCaseIds = new Set([...Object.keys(EXPECTED_STATUS), ...CASES.cases.map(testCase => testCase.id)])
  const unknownCases = [...requestedCases].filter(id => !knownCaseIds.has(id))
  if (unknownCases.length) throw new Error(`unknown case ID(s): ${unknownCases.join(', ')}`)
  const fixture = JSON.parse(fs.readFileSync(path.resolve(args.fixture), 'utf8'))
  FIXTURE_PATH_ROOTS = {
    node: { media: fixture.directories.nodeMedia, metadata: path.join(fixture.directories.node, 'metadata') },
    go: { media: fixture.directories.goMedia, metadata: path.join(fixture.directories.go, 'metadata') }
  }
  // Per-copy inode values used inside serialized content URLs (e.g. track
  // contentUrl paths embed the audio file inode). Inode strings that appear as
  // full field values are normalized per case by checkAndReplaceInode, so this
  // replacement only needs to cover substring occurrences.
  MANIFEST_INOS = [fixture.items.audioFile, fixture.items.audioFileGo]
    .filter((value) => typeof value === 'string' && /^\d+$/.test(value))
  const node = { url: args['node-url'].replace(/\/$/, '') }
  const go = { url: args['go-url'].replace(/\/$/, '') }
  const credentials = fixture.rootCredentials
  const bookLibraryId = fixture.libraries.book
  const bookItemId = fixture.items.book
  const audioFileId = fixture.items.audioFile
  const audioFileIdGo = fixture.items.audioFileGo || audioFileId
  const results = []
  const ioClient = loadSocketIOClient()
  let emailDevicesState = null

  const comparePair = async (id, referenceRoute, goRoute, options = {}) => {
    const { nodeToken, goToken, nodeBody, goBody, ...requestOptions } = options
    const [reference, candidate] = await Promise.all([
      call(node, referenceRoute, { ...requestOptions, body: nodeBody ?? requestOptions.body, token: nodeToken || requestOptions.token || referenceAccessToken }),
      call(go, goRoute, { ...requestOptions, body: goBody ?? requestOptions.body, token: goToken || requestOptions.token || referenceAccessToken })
    ])
    results.push(compare(id, reference, candidate))
    return [reference, candidate]
  }

  const nodeLogin = await call(node, '/login', { method: 'POST', body: credentials })
  const goLogin = await call(go, '/login', { method: 'POST', body: credentials })
  if (!selectedRun || requestedCases.has('login')) results.push(compare('login', nodeLogin, goLogin))
  const referenceAccessToken = nodeLogin.body?.user?.accessToken
  const referenceRefreshToken = nodeLogin.body?.user?.refreshToken
  if (!referenceAccessToken || !referenceRefreshToken) {
    throw new Error('reference login did not return tokens; fixture/server auth setup is invalid')
  }

  const directRangeCases = [
    { id: 'direct-file-range', method: 'GET', range: 'bytes=0-127' },
    { id: 'direct-file-head-range', method: 'HEAD', range: 'bytes=0-127' },
    { id: 'direct-file-invalid-range', method: 'GET', range: 'bytes=999999999-' },
    { id: 'direct-file-head-invalid-range', method: 'HEAD', range: 'bytes=999999999-' }
  ]
  const compareDirectRange = async ({ id, method, range }) => {
    const options = { method, token: referenceAccessToken, headers: { Range: range }, collectHeaders: ['content-length', 'last-modified', 'etag'] }
    const [reference, candidate] = await Promise.all([
      call(node, `/api/items/${bookItemId}/file/${audioFileId}`, options),
      call(go, `/api/items/${bookItemId}/file/${audioFileIdGo}`, options)
    ])
    results.push(compare(id, reference, candidate))
  }

  const nodeRefresh = await call(node, '/auth/refresh', { method: 'POST', refreshToken: referenceRefreshToken })
  const goRefreshToken = goLogin.body?.user?.refreshToken
  if (goRefreshToken) {
    const goRefresh = await call(go, '/auth/refresh', { method: 'POST', refreshToken: goRefreshToken })
    if (!selectedRun || requestedCases.has('refresh')) results.push(compare('refresh', nodeRefresh, goRefresh))
  } else {
    results.push({
      id: 'refresh', passed: false, skipped: true,
      prerequisite: 'Go login did not return an independent Go refresh token',
      referenceStatus: nodeRefresh.status, goStatus: null,
      referenceBody: normalizeCase('refresh', nodeRefresh.body), goBody: null
    })
  }

  const [nodeReaderLogin, goReaderLogin] = await Promise.all([
    call(node, '/login', { method: 'POST', body: fixture.readerCredentials }),
    call(go, '/login', { method: 'POST', body: fixture.readerCredentials })
  ])
  if (!selectedRun || requestedCases.has('reader-login') || [...requestedCases].some(id => id.startsWith('local-session-') || id.startsWith('refresh-') || id.startsWith('inactive-user-'))) results.push(compare('reader-login', nodeReaderLogin, goReaderLogin))
  const readerAccessToken = nodeReaderLogin.body?.user?.accessToken
  if (!readerAccessToken) throw new Error('reference reader login did not return an access token')

  let nodeLocal = null
  let goLocal = null
  let nodeAudioPath
  let goAudioPath
  // F05 local-session lifecycle. Dedicated reader sessions keep the suite's
  // reference sessions intact; session-row and cookie-relationship assertions
  // run WITHIN each side against its own sqlite copy before any normalization.
  // Characterized upstream behavior: successful local login regenerates the
  // express-session and responds with a fresh signed connect.sid
  // (passport sessionManager.logIn) plus the refresh cookie; a valid refresh
  // ROTATES the tokens (Auth.js:331-358, TokenManager.rotateTokensForSession),
  // the rotated (old) token replays within the 10-minute REFRESH_TOKEN_GRACE
  // period returning the stored rotated refresh token (TokenManager.js:408-420),
  // a CONCURRENT second refresh with the same original token returns the same
  // rotated token via the race/grace branch (TokenManager.js:246), and
  // POST /logout clears the epoch cookies and REGENERATES a new connect.sid on
  // every call (Auth.js:486-524, TokenManager.invalidateRefreshToken:531).
  {
    const normalizeExpressJSONHeaders = (result) => {
      if (result.headers?.etag && result.headers?.['content-length']) {
        const match = /^W\/"([0-9a-f]+)-([A-Za-z0-9+\/=]+)"$/.exec(result.headers.etag)
        if (!match) throw new Error(`unexpected Express ETag shape: ${result.headers.etag}`)
        if (parseInt(match[1], 16) !== Number(result.headers['content-length'])) {
          throw new Error(`etag length prefix ${match[1]} != content-length ${result.headers['content-length']}`)
        }
        result.headers['content-length'] = '<dynamic-len>'
        result.headers.etag = `W/"<dynamic-len>-<body-digest>"`
      }
      return result
    }

    if (!selectedRun || [...requestedCases].some(id => id.startsWith('local-session-') || id.startsWith('refresh-') || id.startsWith('inactive-user-'))) {
    ;[nodeLocal, goLocal] = await Promise.all([
      call(node, '/login', { method: 'POST', body: fixture.readerCredentials, collectHeaders: ['set-cookie', 'etag', 'content-length'] }),
      call(go, '/login', { method: 'POST', body: fixture.readerCredentials, collectHeaders: ['set-cookie', 'etag', 'content-length'] })
    ])
    const assertLoginCookies = (side, result) => {
      const cookie = result.headers?.['set-cookie'] || ''
      const sessionPart = cookie.split(', ').find(part => part.startsWith('connect.sid='))
      if (!sessionPart) throw new Error(`${side}: login must set a connect.sid cookie; observed ${JSON.stringify(cookie)}`)
      if (!sessionPart.includes('Path=/; HttpOnly')) {
        throw new Error(`${side}: connect.sid attributes must include Path=/, HttpOnly; observed ${sessionPart}`)
      }
      // The refresh token travels in the refresh_token cookie for browser
      // flows; mobile-style X-Return-Tokens logins carry it in the body
      // instead (Auth.js handleLoginSuccess). Both must be present somewhere.
      const refreshPart = cookie.split(', ').find(part => part.startsWith('refresh_token='))
      const cookieToken = refreshPart ? refreshPart.split('=')[1].split(';')[0] : null
      const bodyToken = result.body?.user?.refreshToken
      if (!cookieToken && (!bodyToken || typeof bodyToken !== 'string' || bodyToken.length < 10)) {
        throw new Error(`${side}: login must deliver the refresh token by cookie or body`)
      }
      return cookieToken || bodyToken
    }
    let nodeLoginRefresh
    let goLoginRefresh
    try {
      nodeLoginRefresh = assertLoginCookies('node', nodeLocal)
      goLoginRefresh = assertLoginCookies('go', goLocal)
      const normalizeLoginPair = (result) => {
        const cookie = result.headers?.['set-cookie']
        if (typeof cookie === 'string') {
          // The signed session identifier and refresh token values are
          // per-side random; names, order and attributes stay exact.
          result.headers['set-cookie'] = cookie
            .replace(/refresh_token=[^;]+/, 'refresh_token=<dynamic-cookie>')
            .replace(/connect\.sid=[^;]+/, 'connect\.sid=<dynamic-session-cookie>')
        }
        return result
      }
      results.push(compare('local-session-login',
        normalizeExpressJSONHeaders(normalizeLoginPair(structuredClone(nodeLocal))),
        normalizeExpressJSONHeaders(normalizeLoginPair(structuredClone(goLocal)))))
    } catch (error) {
      results.push({ id: 'local-session-login', passed: false, error: String(error && error.message ? error.message : error) })
    }
    const nodeLocalTokens = nodeLocal.body?.user
    const goLocalTokens = goLocal.body?.user
    if (nodeLoginRefresh && goLoginRefresh && nodeLocalTokens?.accessToken && goLocalTokens?.accessToken) {
      // Rotation relationship: returned tokens differ from the login tokens.
      const [nodeRotated, goRotated] = await Promise.all([
        call(node, '/auth/refresh', { method: 'POST', refreshToken: nodeLoginRefresh, collectHeaders: ['set-cookie', 'etag', 'content-length'] }),
        call(go, '/auth/refresh', { method: 'POST', refreshToken: goLoginRefresh, collectHeaders: ['set-cookie', 'etag', 'content-length'] })
      ])
      const assertRotation = (side, result, loginToken) => {
        const token = result.body?.user?.refreshToken
        if (!token || typeof token !== 'string' || token.length < 10) throw new Error(`${side}: rotation must return a refresh token`)
        if (token === loginToken) throw new Error(`${side}: rotation must CHANGE the refresh token`)
        const cookie = (result.headers?.['set-cookie'] || '').split(', ').find(part => part.startsWith('refresh_token='))
        const cookieToken = (cookie || '').split('=')[1]?.split(';')[0]
        if (cookieToken !== token) throw new Error(`${side}: rotation Set-Cookie refresh_token must equal the returned token; observed ${JSON.stringify(cookieToken)}`)
        return token
      }
      let nodeRotatedToken = null
      let goRotatedToken = null
      try {
        nodeRotatedToken = assertRotation('node', nodeRotated, nodeLoginRefresh)
        goRotatedToken = assertRotation('go', goRotated, goLoginRefresh)
        // Grace replay: the OLD (rotated) token returns the SAME rotated token.
        const [nodeReplay, goReplay] = await Promise.all([
          call(node, '/auth/refresh', { method: 'POST', refreshToken: nodeLoginRefresh, collectHeaders: ['set-cookie', 'etag', 'content-length'] }),
          call(go, '/auth/refresh', { method: 'POST', refreshToken: goLoginRefresh, collectHeaders: ['set-cookie', 'etag', 'content-length'] })
        ])
        const assertReplay = (side, result, rotatedToken) => {
          const token = result.body?.user?.refreshToken
          if (token !== rotatedToken) throw new Error(`${side}: grace replay must return the stored rotated token; observed ${JSON.stringify(token)}`)
          const cookie = (result.headers?.['set-cookie'] || '').split(', ').find(part => part.startsWith('refresh_token='))
          const cookieToken = (cookie || '').split('=')[1]?.split(';')[0]
          if (cookieToken !== rotatedToken) throw new Error(`${side}: replay Set-Cookie must re-cookie the rotated token`)
        }
        assertReplay('node', nodeReplay, nodeRotatedToken)
        assertReplay('go', goReplay, goRotatedToken)
        // Start both requests with a CURRENT token: this reaches rotation
        // contention as well as the old-token grace branch.
        const [nodeConcurrentLogin, goConcurrentLogin] = await Promise.all([
          call(node, '/login', { method: 'POST', body: fixture.readerCredentials }),
          call(go, '/login', { method: 'POST', body: fixture.readerCredentials })
        ])
        const nodeConcurrentOriginal = nodeConcurrentLogin.body?.user?.refreshToken
        const goConcurrentOriginal = goConcurrentLogin.body?.user?.refreshToken
        if (!nodeConcurrentOriginal || !goConcurrentOriginal) throw new Error('concurrent refresh requires fresh login tokens')
        const [nodeConcurrent1, nodeConcurrent2] = await Promise.all([
          call(node, '/auth/refresh', { method: 'POST', refreshToken: nodeConcurrentOriginal, collectHeaders: ['set-cookie'] }),
          call(node, '/auth/refresh', { method: 'POST', refreshToken: nodeConcurrentOriginal, collectHeaders: ['set-cookie'] })
        ])
        const [goConcurrent1, goConcurrent2] = await Promise.all([
          call(go, '/auth/refresh', { method: 'POST', refreshToken: goConcurrentOriginal, collectHeaders: ['set-cookie'] }),
          call(go, '/auth/refresh', { method: 'POST', refreshToken: goConcurrentOriginal, collectHeaders: ['set-cookie'] })
        ])
        const assertConcurrent = (side, a, b, originalToken) => {
          const rotatedToken = assertRotation(side, a, originalToken)
          assertReplay(side, b, rotatedToken)
          for (const result of [a, b]) {
            if (result.status !== 200) throw new Error(`${side}: concurrent refresh must match the grace behavior; observed status ${result.status} body ${JSON.stringify(result.body).slice(0, 200)}`)
            if (result.body?.user?.refreshToken !== rotatedToken) throw new Error(`${side}: concurrent refresh must return the stored rotated token; observed ${JSON.stringify(result.body?.user?.refreshToken)}`)
          }
        }
        assertConcurrent('node', nodeConcurrent1, nodeConcurrent2, nodeConcurrentOriginal)
        assertConcurrent('go', goConcurrent1, goConcurrent2, goConcurrentOriginal)
        // The session row must carry exactly the rotated token.
        const sqlite3 = require('sqlite3').verbose()
        const assertSessionRow = (side, dbPath, rotatedToken) => new Promise((resolve, reject) => {
          const db = new sqlite3.Database(dbPath, sqlite3.OPEN_READONLY)
          db.all(`SELECT COUNT(*) AS n FROM sessions WHERE refreshToken = ? OR lastRefreshToken = ?`, [rotatedToken, rotatedToken], (error, rows) => {
            db.close()
            error ? reject(error) : resolve(rows?.[0]?.n)
          })
        })
        const nodeRow = await assertSessionRow('node', path.join(fixture.directories.node, 'config', 'absdatabase.sqlite'), nodeRotatedToken)
        const goRow = await assertSessionRow('go', path.join(fixture.directories.go, 'config', 'absdatabase.sqlite'), goRotatedToken)
        if (nodeRow !== 1 || goRow !== 1) throw new Error(`session row must reference the rotated token exactly once; node=${nodeRow} go=${goRow}`)
        // Paired response comparison with the recorded normalizations.
        const normalizeCookieForPair = (result) => {
          const cookie = result.headers?.['set-cookie']
          if (typeof cookie === 'string') {
            result.headers['set-cookie'] = cookie
              .replace(/refresh_token=[^;]+/, 'refresh_token=<dynamic-cookie>')
              .replace(/Expires=[^;]+/g, 'Expires=<dynamic-date>')
          }
          return result
        }
        results.push(compare('refresh-rotation',
          normalizeExpressJSONHeaders(normalizeCookieForPair(structuredClone(nodeRotated))),
          normalizeExpressJSONHeaders(normalizeCookieForPair(structuredClone(goRotated)))))
        results.push(compare('refresh-rotation-grace-replay',
          normalizeExpressJSONHeaders(normalizeCookieForPair(structuredClone(nodeReplay))),
          normalizeExpressJSONHeaders(normalizeCookieForPair(structuredClone(goReplay)))))
        results.push(compare('refresh-concurrent-grace',
          { status: 200, headers: {}, body: { bothRotated: true } },
          { status: 200, headers: {}, body: { bothRotated: true } }))
      } catch (error) {
        results.push({ id: 'refresh-rotation', passed: false, error: String(error && error.message ? error.message : error) })
      }
      // Logout on the dedicated sessions: clearing + REGENERATED connect.sid;
      // a repeated logout on the same session must REPLACE the identifier.
      // The CURRENT (rotated) token is presented: upstream's logout destroys
      // the exact refreshToken match, so a rotated/stale token does not revoke
      // a live session (observed; recorded as characterization).
      const logoutAndAssert = async (side, server, accessToken, currentRefreshToken) => {
        const refreshToken = currentRefreshToken
        const loginResponse = side === 'node' ? nodeLocal : goLocal
        const loginCookie = loginResponse.headers?.['set-cookie'] || ''
        const initialSID = loginCookie.split(', ').find(part => part.startsWith('connect.sid='))?.split(';')[0]
        if (!initialSID) throw new Error(`${side}: successful login did not provide connect.sid for the cookie lifecycle sequence`)
        const first = await call(server, '/logout', { method: 'POST', token: accessToken, refreshToken, headers: { Cookie: initialSID }, collectHeaders: ['set-cookie', 'etag', 'content-length'] })
        const firstSID = (first.headers?.['set-cookie'] || '').split(', ').find(part => part.startsWith('connect.sid='))
        if (first.status !== 200 || !firstSID || firstSID.split('=')[1]?.split(';')[0] === initialSID.split('=')[1]) {
          throw new Error(`${side}: logout must replace the presented connect.sid; status=${first.status} initial=${initialSID} replacement=${firstSID}`)
        }
        const sid = firstSID
        if (!sid) throw new Error(`${side}: first logout did not issue replacement connect.sid`)
        const second = await call(server, '/logout', { method: 'POST', token: accessToken, headers: { Cookie: sid.split(';')[0] }, collectHeaders: ['set-cookie', 'etag', 'content-length'] })
        const sid2 = (second.headers?.['set-cookie'] || '').split(', ').find(part => part.startsWith('connect.sid='))
        if (!sid || !sid2 || sid === sid2) throw new Error(`${side}: repeated logout must replace the connect.sid session identifier; observed ${JSON.stringify([sid, sid2])}`)
        const revoked = await call(server, '/auth/refresh', { method: 'POST', refreshToken, collectHeaders: ['etag', 'content-length'] })
        if (revoked.status !== 401) throw new Error(`${side}: revoked refresh token must 401; observed ${revoked.status} ${JSON.stringify(revoked.body).slice(0, 160)}`)
        return { first, second, revoked, cookieRelationship: { initialSIDPresent: true, firstLogoutReplacedSID: true, repeatedLogoutReplacedSID: true } }
      }
      try {
        const nodeLogoutRun = await logoutAndAssert('node', node, nodeLocalTokens.accessToken, nodeRotatedToken)
        const goLogoutRun = await logoutAndAssert('go', go, goLocalTokens.accessToken, goRotatedToken)
        // The revoked-session row must be gone from both sqlite copies.
        const sqlite3 = require('sqlite3').verbose()
        const assertRevokedRow = (side, dbPath, refreshToken) => new Promise((resolve, reject) => {
          const db = new sqlite3.Database(dbPath, sqlite3.OPEN_READONLY)
          db.all(`SELECT COUNT(*) AS n FROM sessions WHERE refreshToken = ? OR lastRefreshToken = ?`, [refreshToken, refreshToken], (error, rows) => {
            db.close()
            error ? reject(error) : resolve(rows?.[0]?.n)
          })
        })
        const nodeGone = await assertRevokedRow('node', path.join(fixture.directories.node, 'config', 'absdatabase.sqlite'), nodeRotatedToken)
        const goGone = await assertRevokedRow('go', path.join(fixture.directories.go, 'config', 'absdatabase.sqlite'), goRotatedToken)
        if (nodeGone !== 0 || goGone !== 0) throw new Error(`revoked session row must be deleted; node=${nodeGone} go=${goGone}`)
        // Access token remains usable; session list reflects the removal.
        const [nodeAfterLogout, goAfterLogout] = await Promise.all([
          call(node, '/api/me/sessions', { token: nodeLocalTokens.accessToken, refreshToken: nodeLoginRefresh }),
          call(go, '/api/me/sessions', { token: goLocalTokens.accessToken, refreshToken: goLoginRefresh })
        ])
        results.push(compare('local-session-logout-session-list', nodeAfterLogout, goAfterLogout))
        const normalizeLogoutPair = (result) => {
          const cookie = result.headers?.['set-cookie']
          if (typeof cookie === 'string') {
            result.headers['set-cookie'] = cookie
              .replace(/connect\.sid=[^;]+;/, 'connect\.sid=<dynamic-session-cookie>;')
          }
          return result
        }
        results.push(compare('local-session-cookie-regeneration',
          { status: 200, headers: {}, body: nodeLogoutRun.cookieRelationship },
          { status: 200, headers: {}, body: goLogoutRun.cookieRelationship }))
        results.push(compare('local-session-logout',
          normalizeExpressJSONHeaders(normalizeLogoutPair(structuredClone(nodeLogoutRun.first))),
          normalizeExpressJSONHeaders(normalizeLogoutPair(structuredClone(goLogoutRun.first)))))
        results.push(compare('local-session-logout-refresh-revoked', nodeLogoutRun.revoked, goLogoutRun.revoked))
      } catch (error) {
        results.push({ id: 'local-session-logout', passed: false, error: String(error && error.message ? error.message : error) })
      }
    } else {
      for (const id of ['local-session-login', 'refresh-rotation', 'refresh-rotation-grace-replay', 'refresh-concurrent-grace', 'local-session-logout', 'local-session-logout-refresh-revoked', 'local-session-logout-session-list']) {
        results.push({ id, passed: false, skipped: true, prerequisite: 'both dedicated local logins must return usable tokens' })
      }
    }
      if (!selectedRun || requestedCases.has('local-session-relogin')) {
    const [nodeRelogin, goRelogin] = await Promise.all([
      call(node, '/login', { method: 'POST', body: fixture.readerCredentials }),
      call(go, '/login', { method: 'POST', body: fixture.readerCredentials })
    ])
    results.push(compare('local-session-relogin', nodeRelogin, goRelogin))
    }

    // Creation-time inactive user denies login on both implementations.
  if (!selectedRun || [...requestedCases].some(id => id.startsWith('inactive-user-'))) {
  const [nodeInactive, goInactive] = await Promise.all([
      call(node, '/api/users', { method: 'POST', token: referenceAccessToken, body: {
        username: 'parity-inactive-user', password: 'parity-inactive-password', type: 'user',
        isActive: false, librariesAccessible: [bookLibraryId],
        permissions: { download: false, update: false, delete: false }
      } }),
      call(go, '/api/users', { method: 'POST', token: referenceAccessToken, body: {
        username: 'parity-inactive-user', password: 'parity-inactive-password', type: 'user',
        isActive: false, librariesAccessible: [bookLibraryId],
        permissions: { download: false, update: false, delete: false }
      } })
    ])
    results.push(compare('inactive-user-create', nodeInactive, goInactive))
    const nodeInactiveId = nodeInactive.body?.user?.id
    const goInactiveId = goInactive.body?.user?.id
    if (nodeInactiveId && goInactiveId) {
      // Denied login wire shape (reviewer-verified): passport's default
      // failure ends with 401, 'Unauthorized', Content-Length:12 and NO
      // Content-Type. Compare all three without a header exception.
      const [nodeDenied, goDenied] = await Promise.all([
        call(node, '/login', { method: 'POST', body: { username: 'parity-inactive-user', password: 'parity-inactive-password' }, collectHeaders: ['content-length'] }),
        call(go, '/login', { method: 'POST', body: { username: 'parity-inactive-user', password: 'parity-inactive-password' }, collectHeaders: ['content-length'] })
      ])
      results.push(compare('inactive-user-login-denied', nodeDenied, goDenied))
    } else {
      results.push({ id: 'inactive-user-login-denied', passed: false, skipped: true, prerequisite: 'both inactive-user creates must return IDs' })
    }
  }
  }

  }
  if (selectedRun) {
    for (const rangeCase of directRangeCases) {
      if (requestedCases.has(rangeCase.id)) await compareDirectRange(rangeCase)
    }
    const report = {
      schemaVersion: 1,
      baseline: fixture.baseline,
      casesFile: path.relative(path.resolve(__dirname, '../../..'), casesPath),
      fixtureUserId: fixture.userId,
      generatedAt: new Date().toISOString(),
      note: 'Focused differential: only requested case IDs are included; this is not a full-suite result.',
      summary: {
        total: results.filter(result => requestedCases.has(result.id)).length,
        passed: results.filter(result => requestedCases.has(result.id) && result.passed).length,
        failed: results.filter(result => requestedCases.has(result.id) && !result.passed && !result.skipped).length,
        skipped: results.filter(result => requestedCases.has(result.id) && result.skipped).length
      },
      cases: results.filter(result => requestedCases.has(result.id))
    }
    report.summary.executed = report.cases.filter(result => !result.skipped).length
    const output = path.resolve(args.out || 'parity-results.json')
    fs.writeFileSync(output, `${JSON.stringify(report, null, 2)}\n`)
    console.log(JSON.stringify({ output, summary: report.summary }, null, 2))
    if (report.summary.executed === 0) {
      console.error('0 cases executed')
      process.exitCode = 1
    }
    if (report.summary.failed || report.summary.skipped) process.exitCode = 1
    return
  }

  // Use the valid upstream access token on both independent database copies.
  // This keeps read comparisons useful before Go login/token issuance lands.
  const commonCalls = [
    ['authorize', '/api/authorize', { method: 'POST', token: referenceAccessToken }],
    ['libraries', '/api/libraries', { method: 'GET', token: referenceAccessToken }],
    ['libraries-restricted-user', '/api/libraries', { method: 'GET', token: readerAccessToken }],
    ['book-library-items', `/api/libraries/${bookLibraryId}/items?limit=10&page=0&sort=media.metadata.title`, { method: 'GET', token: referenceAccessToken }],
    ['book-library-items-minified', `/api/libraries/${bookLibraryId}/items?limit=10&page=0&minified=1&sort=media.metadata.title`, { method: 'GET', token: referenceAccessToken }],
    ['book-item-detail', `/api/items/${bookItemId}`, { method: 'GET', token: referenceAccessToken }],
    ['progress-write', `/api/me/progress/${bookItemId}`, {
      method: 'PATCH', token: referenceAccessToken,
      body: { duration: 2, currentTime: 1.5, progress: 0.75, isFinished: false }
    }],
    ['progress-read', `/api/me/progress/${bookItemId}`, { method: 'GET', token: referenceAccessToken }]
  ]
  for (const [id, route, options] of commonCalls) {
    if (selectedRun && !requestedCases.has(id)) continue
    const [reference, candidate] = await Promise.all([call(node, route, options), call(go, route, options)])
    results.push(compare(id, reference, candidate, id === 'progress-write'
      ? ['This case mutates only independent fixture copies; rerun fixture creation before another clean run.']
      : []))
  }

  // Each media copy carries its own filesystem inode for the same audio file,
  // so the file-id route is compared per side.
  for (const rangeCase of directRangeCases) await compareDirectRange(rangeCase)

  const [nodeDownload, goDownload] = await Promise.all([
    call(node, `/api/items/${bookItemId}/download`, { token: referenceAccessToken }),
    call(go, `/api/items/${bookItemId}/download`, { token: referenceAccessToken })
  ])
  results.push(compare('item-download', nodeDownload, goDownload))

  const fileDownloadRoutes = {
    node: `/api/items/${bookItemId}/file/${audioFileId}/download`,
    go: `/api/items/${bookItemId}/file/${audioFileIdGo}/download`
  }
  const [nodeFileDownload, goFileDownload] = await Promise.all([
    call(node, fileDownloadRoutes.node, { token: referenceAccessToken }),
    call(go, fileDownloadRoutes.go, { token: referenceAccessToken })
  ])
  results.push(compare('item-file-download', nodeFileDownload, goFileDownload))

  const [nodeFileDownloadRange, goFileDownloadRange] = await Promise.all([
    call(node, fileDownloadRoutes.node, { token: referenceAccessToken, headers: { Range: 'bytes=0-127' } }),
    call(go, fileDownloadRoutes.go, { token: referenceAccessToken, headers: { Range: 'bytes=0-127' } })
  ])
  results.push(compare('item-file-download-range', nodeFileDownloadRange, goFileDownloadRange))

  const missingFileRoute = `/api/items/${bookItemId}/file/absent-file-ino/download`
  const [nodeMissingFile, goMissingFile] = await Promise.all([
    call(node, missingFileRoute, { token: referenceAccessToken }),
    call(go, missingFileRoute, { token: referenceAccessToken })
  ])
  results.push(compare('item-file-download-missing-file', nodeMissingFile, goMissingFile))

  const missingItemFileRoute = '/api/items/00000000-0000-0000-0000-000000000000/file/absent-file-ino/download'
  const [nodeMissingItemFile, goMissingItemFile] = await Promise.all([
    call(node, missingItemFileRoute, { token: referenceAccessToken }),
    call(go, missingItemFileRoute, { token: referenceAccessToken })
  ])
  results.push(compare('item-file-download-missing-item', nodeMissingItemFile, goMissingItemFile))

  // Exercise the bookmark lifecycle against the same independently seeded user copies.
  const bookmarkTime = 1.25
  const bookmarkCreateRoute = `/api/me/item/${bookItemId}/bookmark`
  const bookmarkReadRoute = `/api/me/bookmarks/${bookItemId}`
  const bookmarkSteps = [
    ['bookmark-create', bookmarkCreateRoute, { method: 'POST', token: referenceAccessToken, body: { time: bookmarkTime, title: 'Parity bookmark' } }],
    ['bookmark-read-after-create', bookmarkReadRoute, { method: 'GET', token: referenceAccessToken }],
    ['bookmark-update', bookmarkCreateRoute, { method: 'PATCH', token: referenceAccessToken, body: { time: bookmarkTime, title: 'Updated parity bookmark' } }],
    ['bookmark-delete', `/api/me/item/${bookItemId}/bookmark/${bookmarkTime}`, { method: 'DELETE', token: referenceAccessToken }],
    ['bookmark-read-after-delete', bookmarkReadRoute, { method: 'GET', token: referenceAccessToken }]
  ]
  for (const [id, route, options] of bookmarkSteps) {
    if (selectedRun && !requestedCases.has(id)) continue
    const [reference, candidate] = await Promise.all([call(node, route, options), call(go, route, options)])
    results.push(compare(id, reference, candidate))
  }

  // Static errors and malformed/empty inputs are compared without body normalization.
  const errorCalls = [
    ['libraries-unauthorized', '/api/libraries', { method: 'GET' }],
    ['item-detail-missing', '/api/items/00000000-0000-0000-0000-000000000000', { method: 'GET', token: referenceAccessToken }],
    ['item-detail-invalid-id', '/api/items/not-a-uuid', { method: 'GET', token: referenceAccessToken }],
    ['library-items-invalid-pagination', `/api/libraries/${bookLibraryId}/items?page=-1`, { method: 'GET', token: referenceAccessToken }],
    ['progress-write-invalid', `/api/me/progress/${bookItemId}`, { method: 'PATCH', token: referenceAccessToken, body: { currentTime: 'abc' } }],
    ['collection-missing', '/api/collections/00000000-0000-0000-0000-000000000000', { method: 'GET', token: referenceAccessToken }],
    ['playlist-missing', '/api/playlists/00000000-0000-0000-0000-000000000000', { method: 'GET', token: referenceAccessToken }],
    ['user-create-forbidden', '/api/users', { method: 'POST', token: readerAccessToken, body: { username: 'forbidden-user', password: 'forbidden-password', type: 'user' } }],
    ['user-delete-forbidden', '/api/users/00000000-0000-0000-0000-000000000000', { method: 'DELETE', token: readerAccessToken }],
    ['email-settings-reader-admin-gate', '/api/emails/settings', { method: 'GET', token: readerAccessToken }],
    ['notifications-reader-admin-gate', '/api/notifications', { method: 'GET', token: readerAccessToken }],
    ['notification-test-missing-id-unconfigured', '/api/notifications/00000000-0000-4000-8000-000000000000/test', { method: 'GET', token: referenceAccessToken }],
    ['notification-test-admin-denied', '/api/notifications/00000000-0000-4000-8000-000000000000/test', { method: 'GET', token: readerAccessToken }],
    ['notification-event-test-unconfigured', '/api/notifications/test', { method: 'GET', token: referenceAccessToken }],
    ['notification-event-test-fail-unconfigured', '/api/notifications/test?fail=1', { method: 'GET', token: referenceAccessToken }],
    ['notification-event-test-admin-denied', '/api/notifications/test', { method: 'GET', token: readerAccessToken }],
    ['session-delete-missing', '/api/me/sessions/00000000-0000-0000-0000-000000000000', { method: 'DELETE', token: referenceAccessToken }],
    ['backup-missing-download', '/api/backups/00000000-0000-0000-0000-000000000000/download', { method: 'GET', token: referenceAccessToken }],
    ['collection-empty-name', '/api/collections', { method: 'POST', token: referenceAccessToken, body: { name: '', libraryId: bookLibraryId, books: [] } }]
  ]
  for (const [id, route, options] of errorCalls) {
    if (selectedRun && !requestedCases.has(id)) continue
    await comparePair(id, route, route, options)
  }
  if (!selectedRun || [...requestedCases].some(id => id.startsWith('email-'))) {
  await comparePair('email-settings-read-defaults-initial', '/api/emails/settings', '/api/emails/settings', {
    method: 'GET', token: referenceAccessToken
  })
  await comparePair('email-settings-patch-coercion', '/api/emails/settings', '/api/emails/settings', {
    method: 'PATCH', token: referenceAccessToken,
    body: { host: 'smtp.example.invalid', port: '587', secure: 'false' }
  })
  await comparePair('email-settings-read-after-coercion', '/api/emails/settings', '/api/emails/settings', {
    method: 'GET', token: referenceAccessToken
  })
  }
  if (!selectedRun || [...requestedCases].some(id => id.startsWith('notification-'))) {
  await comparePair('notifications-settings-read-defaults-initial', '/api/notifications', '/api/notifications', {
    method: 'GET', token: referenceAccessToken
  })
  await comparePair('notifications-settings-patch-custom', '/api/notifications', '/api/notifications', {
    method: 'PATCH', token: referenceAccessToken,
    body: { appriseApiUrl: 'http://apprise.example.invalid', maxFailedAttempts: 3, maxNotificationQueue: 8 }
  })
  await comparePair('notifications-settings-read-after-custom', '/api/notifications', '/api/notifications', {
    method: 'GET', token: referenceAccessToken
  })
  await comparePair('notifications-settings-patch-defaults', '/api/notifications', '/api/notifications', {
    method: 'PATCH', token: referenceAccessToken, body: {}
  })
  await comparePair('notifications-settings-read-after-defaults', '/api/notifications', '/api/notifications', {
    method: 'GET', token: referenceAccessToken
  })
  await comparePair('notifications-clear-apprise-url', '/api/notifications', '/api/notifications', {
    method: 'PATCH', token: referenceAccessToken, body: { appriseApiUrl: null }
  })
  }
  const notificationCreateBody = {
    libraryId: bookLibraryId,
    eventName: 'onTest',
    urls: ['discord://parity-test'],
    titleTemplate: 'Parity notification',
    bodyTemplate: 'Test body',
    enabled: false,
    type: 'info'
  }
  let nodeNotification = { body: { notifications: [] } }
  let goNotification = { body: { notifications: [] } }
  let nodeNotificationID = null
  let goNotificationID = null
  ;[nodeNotification, goNotification] = await comparePair('notification-create', '/api/notifications', '/api/notifications', {
    method: 'POST', token: referenceAccessToken, body: notificationCreateBody
  })
  nodeNotificationID = nodeNotification.body?.notifications?.at(-1)?.id
  goNotificationID = goNotification.body?.notifications?.at(-1)?.id
  if (!selectedRun || [...requestedCases].some(id => id.startsWith('notification-'))) if (nodeNotificationID && goNotificationID) {
    await comparePair('notification-test-existing-id-unconfigured', `/api/notifications/${nodeNotificationID}/test`, `/api/notifications/${goNotificationID}/test`, {
      method: 'GET', token: referenceAccessToken
    })
    const apprise = await startNotificationAppriseFixture()
    try {
      await comparePair('notification-apprise-fixture-configure-success', '/api/notifications', '/api/notifications', {
        method: 'PATCH', token: referenceAccessToken,
        nodeBody: { appriseApiUrl: `${apprise.url}/node/success` },
        goBody: { appriseApiUrl: `${apprise.url}/go/success` }
      })
      await comparePair('notification-test-send-success', `/api/notifications/${nodeNotificationID}/test`, `/api/notifications/${goNotificationID}/test`, {
        method: 'GET', token: referenceAccessToken
      })
      if (apprise.requests.node.length !== 1 || apprise.requests.go.length !== 1) throw new Error('notification test success must deliver exactly once on each side')
      results.push(compare('notification-test-apprise-payload',
        { status: 200, headers: {}, body: apprise.requests.node[0].payload },
        { status: 200, headers: {}, body: apprise.requests.go[0].payload }))
      const [nodeAfterSuccess, goAfterSuccess] = await comparePair('notification-read-after-test-success', '/api/notifications', '/api/notifications', {
        method: 'GET', token: referenceAccessToken
      })
      const testState = (response, notificationID) => {
        const notification = response.body?.settings?.notifications?.find(entry => entry.id === notificationID)
        if (!notification) throw new Error('notification disappeared after test delivery')
        return {
          lastFiredAt: notification.lastFiredAt,
          lastAttemptFailed: notification.lastAttemptFailed,
          numConsecutiveFailedAttempts: notification.numConsecutiveFailedAttempts,
          numTimesFired: notification.numTimesFired
        }
      }
      results.push(compare('notification-test-no-state-change-success',
        { status: 200, headers: {}, body: testState(nodeAfterSuccess, nodeNotificationID) },
        { status: 200, headers: {}, body: testState(goAfterSuccess, goNotificationID) }))
      if (JSON.stringify(testState(nodeAfterSuccess, nodeNotificationID)) !== JSON.stringify({ lastFiredAt: null, lastAttemptFailed: false, numConsecutiveFailedAttempts: 0, numTimesFired: 0 }) ||
          JSON.stringify(testState(goAfterSuccess, goNotificationID)) !== JSON.stringify({ lastFiredAt: null, lastAttemptFailed: false, numConsecutiveFailedAttempts: 0, numTimesFired: 0 })) {
        throw new Error('GET /notifications/:id/test must not mutate firing counters')
      }
      await comparePair('notification-apprise-fixture-configure-failure', '/api/notifications', '/api/notifications', {
        method: 'PATCH', token: referenceAccessToken,
        nodeBody: { appriseApiUrl: `${apprise.url}/node/failure` },
        goBody: { appriseApiUrl: `${apprise.url}/go/failure` }
      })
      await comparePair('notification-test-send-failure', `/api/notifications/${nodeNotificationID}/test`, `/api/notifications/${goNotificationID}/test`, {
        method: 'GET', token: referenceAccessToken
      })
      if (apprise.requests.node.length !== 2 || apprise.requests.go.length !== 2) throw new Error('notification test failure must attempt exactly one delivery on each side')
      const [nodeAfterFailure, goAfterFailure] = await comparePair('notification-read-after-test-failure', '/api/notifications', '/api/notifications', {
        method: 'GET', token: referenceAccessToken
      })
      results.push(compare('notification-test-no-state-change-failure',
        { status: 200, headers: {}, body: testState(nodeAfterFailure, nodeNotificationID) },
        { status: 200, headers: {}, body: testState(goAfterFailure, goNotificationID) }))
      if (JSON.stringify(testState(nodeAfterFailure, nodeNotificationID)) !== JSON.stringify({ lastFiredAt: null, lastAttemptFailed: false, numConsecutiveFailedAttempts: 0, numTimesFired: 0 }) ||
          JSON.stringify(testState(goAfterFailure, goNotificationID)) !== JSON.stringify({ lastFiredAt: null, lastAttemptFailed: false, numConsecutiveFailedAttempts: 0, numTimesFired: 0 })) {
        throw new Error('failed GET /notifications/:id/test must not mutate firing counters')
      }
    } finally {
      await new Promise(resolve => apprise.server.close(resolve))
    }
  } else {
    for (const id of ['notification-test-existing-id-unconfigured', 'notification-apprise-fixture-configure-success', 'notification-test-send-success', 'notification-test-apprise-payload', 'notification-read-after-test-success', 'notification-test-no-state-change-success', 'notification-apprise-fixture-configure-failure', 'notification-test-send-failure', 'notification-read-after-test-failure', 'notification-test-no-state-change-failure']) {
      results.push({ id, passed: false, skipped: true, prerequisite: 'both notification create responses must return UUIDs' })
    }
  }
  await comparePair('notification-create-empty-urls', '/api/notifications', '/api/notifications', {
    method: 'POST', token: referenceAccessToken,
    body: { eventName: 'onTest', urls: [], titleTemplate: 'Ignored', bodyTemplate: 'Ignored' }
  })
  await comparePair('notification-create-minimal', '/api/notifications', '/api/notifications', {
    method: 'POST', token: referenceAccessToken,
    body: { eventName: 'onTest', urls: ['discord://minimal'], enabled: 'false', libraryId: '', type: '' }
  })
  await comparePair('notification-read-after-create', '/api/notifications', '/api/notifications', {
    method: 'GET', token: referenceAccessToken
  })
  if (nodeNotificationID && goNotificationID) {
    await comparePair('notification-update-without-body-id', `/api/notifications/${nodeNotificationID}`, `/api/notifications/${goNotificationID}`, {
      method: 'PATCH', token: referenceAccessToken,
      nodeBody: { titleTemplate: 'Must remain unchanged' },
      goBody: { titleTemplate: 'Must remain unchanged' }
    })
    await comparePair('notification-update', `/api/notifications/${nodeNotificationID}`, `/api/notifications/${goNotificationID}`, {
      method: 'PATCH', token: referenceAccessToken,
      nodeBody: { id: nodeNotificationID, enabled: true, titleTemplate: 'Updated parity notification', urls: ['discord://parity-updated'] },
      goBody: { id: goNotificationID, enabled: true, titleTemplate: 'Updated parity notification', urls: ['discord://parity-updated'] }
    })
    const [nodeReadAfterUpdate, goReadAfterUpdate] = await comparePair('notification-read-after-update', '/api/notifications', '/api/notifications', {
      method: 'GET', token: referenceAccessToken
    })
    if (!selectedRun || [...requestedCases].some(id => id.startsWith('notification-event-'))) {
    const activeOnTest = response => response.body.settings.notifications.filter(entry => entry.eventName === 'onTest' && entry.enabled)
    const nodeActiveOnTest = activeOnTest(nodeReadAfterUpdate)
    const goActiveOnTest = activeOnTest(goReadAfterUpdate)
    if (nodeActiveOnTest.length !== 2 || goActiveOnTest.length !== 2) throw new Error('global test event fixture must contain two enabled onTest notifications')
    const globalApprise = await startNotificationAppriseFixture()
    try {
      await comparePair('notification-apprise-global-configure-success', '/api/notifications', '/api/notifications', {
        method: 'PATCH', token: referenceAccessToken,
        nodeBody: { appriseApiUrl: `${globalApprise.url}/node/success` },
        goBody: { appriseApiUrl: `${globalApprise.url}/go/success` }
      })
      if (ioClient) {
        const eventProbes = await Promise.all([
          socketNotificationProbe(ioClient, node, `${PREFIX}/socket.io`, referenceAccessToken, '/api/notifications/test'),
          socketNotificationProbe(ioClient, go, `${PREFIX}/socket.io`, referenceAccessToken, '/api/notifications/test')
        ])
        results.push(compare('notification-event-test-send-success', eventProbes[0].response, eventProbes[1].response))
        const broadcast = compare('notification-event-broadcast-success',
          { status: 200, headers: {}, body: { count: eventProbes[0].events.length, events: eventProbes[0].events } },
          { status: 200, headers: {}, body: { count: eventProbes[1].events.length, events: eventProbes[1].events } })
        if (eventProbes[0].events.length !== 1 || eventProbes[1].events.length !== 1) {
          broadcast.passed = false
          broadcast.differences = [...(broadcast.differences || []), { path: '$.count', reference: eventProbes[0].events.length, go: eventProbes[1].events.length, expected: 1 }]
        }
        results.push(broadcast)
      } else {
        await comparePair('notification-event-test-send-success', '/api/notifications/test', '/api/notifications/test', {
          method: 'GET', token: referenceAccessToken
        })
        results.push({ id: 'notification-event-broadcast-success', passed: false, skipped: true, prerequisite: 'socket.io-client v4 is required to observe notifications_updated' })
      }
      if (globalApprise.requests.node.length !== 2 || globalApprise.requests.go.length !== 2) throw new Error('global notification test must deliver once per enabled notification on each side')
      results.push(compare('notification-event-test-delivery-payloads',
        { status: 200, headers: {}, body: globalApprise.requests.node.map(request => request.payload) },
        { status: 200, headers: {}, body: globalApprise.requests.go.map(request => request.payload) }))
      const stateShape = response => response.body.settings.notifications.filter(entry => entry.eventName === 'onTest').map(entry => ({
        urls: entry.urls,
        enabled: entry.enabled,
        lastFiredAtPresent: entry.lastFiredAt !== null,
        lastAttemptFailed: entry.lastAttemptFailed,
        numConsecutiveFailedAttempts: entry.numConsecutiveFailedAttempts,
        numTimesFired: entry.numTimesFired
      }))
      const [nodeAfterGlobalSuccess, goAfterGlobalSuccess] = await comparePair('notification-read-after-event-test-success', '/api/notifications', '/api/notifications', {
        method: 'GET', token: referenceAccessToken
      })
      const nodeSuccessState = stateShape(nodeAfterGlobalSuccess)
      const goSuccessState = stateShape(goAfterGlobalSuccess)
      for (const notification of [...nodeSuccessState, ...goSuccessState]) {
        if (!notification.enabled || !notification.lastFiredAtPresent || notification.lastAttemptFailed || notification.numConsecutiveFailedAttempts !== 0 || notification.numTimesFired !== 1) {
          throw new Error(`global notification success state is unexpected: ${JSON.stringify(notification)}`)
        }
      }
      results.push(compare('notification-event-test-state-success',
        { status: 200, headers: {}, body: nodeSuccessState },
        { status: 200, headers: {}, body: goSuccessState }))
      await delay(1100)
      await comparePair('notification-event-test-repeat-success', '/api/notifications/test', '/api/notifications/test', {
        method: 'GET', token: referenceAccessToken
      })
      if (globalApprise.requests.node.length !== 4 || globalApprise.requests.go.length !== 4) throw new Error('repeated notification test must redeliver each enabled notification on both sides')
      results.push(compare('notification-event-test-repeat-payloads',
        { status: 200, headers: {}, body: globalApprise.requests.node.slice(2).map(request => request.payload) },
        { status: 200, headers: {}, body: globalApprise.requests.go.slice(2).map(request => request.payload) }))
      const [nodeAfterGlobalRepeat, goAfterGlobalRepeat] = await comparePair('notification-read-after-event-test-repeat', '/api/notifications', '/api/notifications', {
        method: 'GET', token: referenceAccessToken
      })
      const nodeRepeatState = stateShape(nodeAfterGlobalRepeat)
      const goRepeatState = stateShape(goAfterGlobalRepeat)
      for (const notification of [...nodeRepeatState, ...goRepeatState]) {
        if (!notification.enabled || !notification.lastFiredAtPresent || notification.lastAttemptFailed || notification.numConsecutiveFailedAttempts !== 0 || notification.numTimesFired !== 2) {
          throw new Error(`repeated global notification state is unexpected: ${JSON.stringify(notification)}`)
        }
      }
      results.push(compare('notification-event-test-state-repeat',
        { status: 200, headers: {}, body: nodeRepeatState },
        { status: 200, headers: {}, body: goRepeatState }))
      await delay(1100)
      await comparePair('notification-event-test-set-failure-threshold', '/api/notifications', '/api/notifications', {
        method: 'PATCH', token: referenceAccessToken,
        nodeBody: { maxFailedAttempts: 1, appriseApiUrl: `${globalApprise.url}/node/success` },
        goBody: { maxFailedAttempts: 1, appriseApiUrl: `${globalApprise.url}/go/success` }
      })
      if (ioClient) {
        const eventProbes = await Promise.all([
          socketNotificationProbe(ioClient, node, `${PREFIX}/socket.io`, referenceAccessToken, '/api/notifications/test?fail=1'),
          socketNotificationProbe(ioClient, go, `${PREFIX}/socket.io`, referenceAccessToken, '/api/notifications/test?fail=1')
        ])
        results.push(compare('notification-event-test-intentional-failure', eventProbes[0].response, eventProbes[1].response))
        const broadcast = compare('notification-event-broadcast-failure',
          { status: 200, headers: {}, body: { count: eventProbes[0].events.length, events: eventProbes[0].events } },
          { status: 200, headers: {}, body: { count: eventProbes[1].events.length, events: eventProbes[1].events } })
        if (eventProbes[0].events.length !== 1 || eventProbes[1].events.length !== 1) {
          broadcast.passed = false
          broadcast.differences = [...(broadcast.differences || []), { path: '$.count', reference: eventProbes[0].events.length, go: eventProbes[1].events.length, expected: 1 }]
        }
        results.push(broadcast)
      } else {
        await comparePair('notification-event-test-intentional-failure', '/api/notifications/test?fail=1', '/api/notifications/test?fail=1', {
          method: 'GET', token: referenceAccessToken
        })
        results.push({ id: 'notification-event-broadcast-failure', passed: false, skipped: true, prerequisite: 'socket.io-client v4 is required to observe notifications_updated' })
      }
      if (globalApprise.requests.node.length !== 4 || globalApprise.requests.go.length !== 4) throw new Error('intentional test-event failure must not deliver to Apprise')
      results.push(compare('notification-event-test-no-delivery-on-failure',
        { status: 200, headers: {}, body: globalApprise.requests.node.length },
        { status: 200, headers: {}, body: globalApprise.requests.go.length }))
      const [nodeAfterGlobalFailure, goAfterGlobalFailure] = await comparePair('notification-read-after-event-test-failure', '/api/notifications', '/api/notifications', {
        method: 'GET', token: referenceAccessToken
      })
      const nodeFailureState = stateShape(nodeAfterGlobalFailure)
      const goFailureState = stateShape(goAfterGlobalFailure)
      for (const notification of [...nodeFailureState, ...goFailureState]) {
        if (notification.enabled || !notification.lastFiredAtPresent || !notification.lastAttemptFailed || notification.numConsecutiveFailedAttempts !== 1 || notification.numTimesFired !== 3) {
          throw new Error(`global notification failure state is unexpected: ${JSON.stringify(notification)}`)
        }
      }
      results.push(compare('notification-event-test-state-failure',
        { status: 200, headers: {}, body: nodeFailureState },
        { status: 200, headers: {}, body: goFailureState }))
    } finally {
      await new Promise(resolve => globalApprise.server.close(resolve))
    }
    }
    if (!selectedRun || [...requestedCases].some(id => id.startsWith('notification-'))) await comparePair('notification-delete', `/api/notifications/${nodeNotificationID}`, `/api/notifications/${goNotificationID}`, {
      method: 'DELETE', token: referenceAccessToken
    })
    if (!selectedRun || [...requestedCases].some(id => id.startsWith('notification-'))) await comparePair('notification-read-after-delete', '/api/notifications', '/api/notifications', {
      method: 'GET', token: referenceAccessToken
    })
  } else {
    for (const id of ['notification-update', 'notification-read-after-update', 'notification-apprise-global-configure-success', 'notification-event-test-send-success', 'notification-event-test-delivery-payloads', 'notification-read-after-event-test-success', 'notification-event-test-state-success', 'notification-event-test-repeat-success', 'notification-event-test-repeat-payloads', 'notification-read-after-event-test-repeat', 'notification-event-test-state-repeat', 'notification-event-test-intentional-failure', 'notification-event-test-no-delivery-on-failure', 'notification-read-after-event-test-failure', 'notification-event-test-state-failure', 'notification-delete', 'notification-read-after-delete']) {
      results.push({ id, passed: false, skipped: true, prerequisite: 'both notification create responses must return UUIDs' })
    }
  }
  if (!selectedRun || [...requestedCases].some(id => id.startsWith('notification-'))) await comparePair('notification-update-missing', '/api/notifications/00000000-0000-4000-8000-000000000000', '/api/notifications/00000000-0000-4000-8000-000000000000', {
    method: 'PATCH', token: referenceAccessToken, body: { id: '00000000-0000-4000-8000-000000000000', enabled: true }
  })
  if (!selectedRun || [...requestedCases].some(id => id.startsWith('notification-'))) await comparePair('notification-delete-missing', '/api/notifications/00000000-0000-4000-8000-000000000000', '/api/notifications/00000000-0000-4000-8000-000000000000', {
    method: 'DELETE', token: referenceAccessToken
  })
  if (!selectedRun || requestedCases.has('playlist-empty-items')) await comparePair('playlist-empty-items', '/api/playlists', '/api/playlists', {
    method: 'POST', token: referenceAccessToken,
    body: { libraryId: bookLibraryId, name: 'Empty parity playlist', items: [] }
  })
  if (!selectedRun || requestedCases.has('series-list-paginated')) await comparePair('series-list-paginated', `/api/libraries/${bookLibraryId}/series?sort=name&desc=0&limit=2&page=1`, `/api/libraries/${bookLibraryId}/series?sort=name&desc=0&limit=2&page=1`, { method: 'GET' })
  const [nodeSessions, goSessions] = await Promise.all([
    call(node, '/api/me/sessions', { token: nodeLogin.body.user.accessToken, refreshToken: nodeLogin.body.user.refreshToken }),
    call(go, '/api/me/sessions', { token: goLogin.body.user.accessToken, refreshToken: goLogin.body.user.refreshToken })
  ])
  results.push(compare('session-list', nodeSessions, goSessions))
  const nodeCurrentSession = nodeSessions.body?.sessions?.find((session) => session.current) || nodeSessions.body?.sessions?.[0]
  const goCurrentSession = goSessions.body?.sessions?.find((session) => session.current) || goSessions.body?.sessions?.[0]
  if (nodeCurrentSession?.id && goCurrentSession?.id) {
    const [nodeSessionDelete, goSessionDelete] = await Promise.all([
      call(node, `/api/me/sessions/${nodeCurrentSession.id}`, { method: 'DELETE', token: nodeLogin.body.user.accessToken }),
      call(go, `/api/me/sessions/${goCurrentSession.id}`, { method: 'DELETE', token: goLogin.body.user.accessToken })
    ])
    results.push(compare('session-delete', nodeSessionDelete, goSessionDelete))
    const [nodeAfterDelete, goAfterDelete] = await Promise.all([
      call(node, '/api/me/sessions', { token: nodeLogin.body.user.accessToken, refreshToken: nodeLogin.body.user.refreshToken }),
      call(go, '/api/me/sessions', { token: goLogin.body.user.accessToken, refreshToken: goLogin.body.user.refreshToken })
    ])
    results.push(compare('session-list-after-delete', nodeAfterDelete, goAfterDelete))
  } else {
    for (const id of ['session-delete', 'session-list-after-delete']) results.push({ id, passed: false, skipped: true, prerequisite: 'both logins must create a current session' })
  }
  const libraryPayload = (folderPath) => ({
    name: 'Parity CRUD Library', mediaType: 'book', provider: 'google',
    folders: [{ fullPath: folderPath }]
  })
  const [nodeLibrary, goLibrary] = await Promise.all([
    call(node, '/api/libraries', { method: 'POST', token: referenceAccessToken, body: libraryPayload(fixture.libraryCrudFolders.node) }),
    call(go, '/api/libraries', { method: 'POST', token: referenceAccessToken, body: libraryPayload(fixture.libraryCrudFolders.go) })
  ])
  results.push(compare('library-create', nodeLibrary, goLibrary))
  const nodeLibraryId = nodeLibrary.body?.id
  const goLibraryId = goLibrary.body?.id
  if (nodeLibraryId && goLibraryId) {
    await comparePair('library-read', `/api/libraries/${nodeLibraryId}`, `/api/libraries/${goLibraryId}`, { method: 'GET' })
    await comparePair('library-update', `/api/libraries/${nodeLibraryId}`, `/api/libraries/${goLibraryId}`, { method: 'PATCH', body: { name: 'Parity CRUD Library Updated' } })
    await comparePair('library-list-after-create', '/api/libraries', '/api/libraries', { method: 'GET' })
    await comparePair('library-delete', `/api/libraries/${nodeLibraryId}`, `/api/libraries/${goLibraryId}`, { method: 'DELETE' })
    await comparePair('library-list-after-delete', '/api/libraries', '/api/libraries', { method: 'GET' })
  } else {
    for (const id of ['library-read', 'library-update', 'library-list-after-create', 'library-delete', 'library-list-after-delete']) {
      results.push({ id, passed: false, skipped: true, prerequisite: 'both library create calls must return independent IDs' })
    }
  }
  const userPayload = {
    username: 'parity-user', password: 'parity-user-test-password', type: 'user', isActive: true,
    librariesAccessible: [bookLibraryId],
    permissions: { download: false, update: false, delete: false }
  }
  const [nodeUser, goUser] = await comparePair('user-create', '/api/users', '/api/users', { method: 'POST', body: userPayload })
  const nodeUserId = nodeUser.body?.user?.id
  const goUserId = goUser.body?.user?.id
  if (nodeUserId && goUserId) {
    await comparePair('user-read', `/api/users/${nodeUserId}`, `/api/users/${goUserId}`, { method: 'GET' })
    await comparePair('user-update', `/api/users/${nodeUserId}`, `/api/users/${goUserId}`, { method: 'PATCH', body: { permissions: { canUpdate: true, canDelete: false, canDownload: false } } })
    await comparePair('user-list', '/api/users', '/api/users', { method: 'GET' })
    const [nodeApiKey, goApiKey] = await Promise.all([
      call(node, '/api/api-keys', { method: 'POST', token: referenceAccessToken, body: { name: 'Parity API Key', userId: nodeUserId, isActive: true, expiresIn: 3600 } }),
      call(go, '/api/api-keys', { method: 'POST', token: referenceAccessToken, body: { name: 'Parity API Key', userId: goUserId, isActive: true, expiresIn: 3600 } })
    ])
    results.push(compare('api-key-create', nodeApiKey, goApiKey))
    const nodeKey = nodeApiKey.body?.apiKey
    const goKey = goApiKey.body?.apiKey
    const nodeApiKeyId = nodeKey?.id
    const goApiKeyId = goKey?.id
    if (nodeKey?.apiKey && goKey?.apiKey && nodeApiKeyId && goApiKeyId) {
      await comparePair('api-key-list', '/api/api-keys', '/api/api-keys', { method: 'GET' })
      await comparePair('api-key-auth-library-read', '/api/libraries', '/api/libraries', { method: 'GET', nodeToken: nodeKey.apiKey, goToken: goKey.apiKey })
      await comparePair('api-key-denied-download', `/api/items/${bookItemId}/download`, `/api/items/${bookItemId}/download`, { method: 'GET', nodeToken: nodeKey.apiKey, goToken: goKey.apiKey })
      await comparePair('item-file-download-denied', fileDownloadRoutes.node, fileDownloadRoutes.go, { method: 'GET', nodeToken: nodeKey.apiKey, goToken: goKey.apiKey })
      const missingNodeFileRoute = `/api/items/${bookItemId}/file/absent-file-ino/download`
      const missingGoFileRoute = `/api/items/${bookItemId}/file/absent-file-ino/download`
      await comparePair('item-file-download-denied-missing-file', missingNodeFileRoute, missingGoFileRoute, { method: 'GET', nodeToken: nodeKey.apiKey, goToken: goKey.apiKey })
      await comparePair('api-key-update', `/api/api-keys/${nodeApiKeyId}`, `/api/api-keys/${goApiKeyId}`, { method: 'PATCH', body: { isActive: false } })
      await comparePair('api-key-delete', `/api/api-keys/${nodeApiKeyId}`, `/api/api-keys/${goApiKeyId}`, { method: 'DELETE' })
    } else {
      results.push({ id: 'api-key-list', passed: false, skipped: true, prerequisite: 'both API key create calls must return active key tokens' })
      for (const id of ['api-key-auth-library-read', 'api-key-denied-download', 'item-file-download-denied', 'item-file-download-denied-missing-file', 'api-key-update', 'api-key-delete']) results.push({ id, passed: false, skipped: true, prerequisite: 'both API key create calls must return active key tokens' })
    }
    await comparePair('user-delete', `/api/users/${nodeUserId}`, `/api/users/${goUserId}`, { method: 'DELETE' })
    await comparePair('user-list-after-delete', '/api/users', '/api/users', { method: 'GET' })
  } else {
    for (const id of ['user-read', 'user-update', 'user-list', 'api-key-create', 'api-key-list', 'api-key-auth-library-read', 'api-key-denied-download', 'api-key-update', 'api-key-delete', 'user-delete', 'user-list-after-delete']) {
      results.push({ id, passed: false, skipped: true, prerequisite: 'both user create calls must return independent IDs' })
    }
  }
  const [nodeCollection, goCollection] = await comparePair('collection-create', '/api/collections', '/api/collections', {
    method: 'POST', body: { libraryId: bookLibraryId, name: 'Parity Collection', description: 'Source parity', books: [bookItemId] }
  })
  const nodeCollectionId = nodeCollection.body?.id
  const goCollectionId = goCollection.body?.id
  if (nodeCollectionId && goCollectionId) {
    await comparePair('collection-read', `/api/collections/${nodeCollectionId}?include=books`, `/api/collections/${goCollectionId}?include=books`, { method: 'GET' })
    await comparePair('collection-update', `/api/collections/${nodeCollectionId}`, `/api/collections/${goCollectionId}`, { method: 'PATCH', body: { name: 'Parity Collection Updated', description: 'Updated source parity' } })
    await comparePair('collection-list', '/api/collections', '/api/collections', { method: 'GET' })
    await comparePair('collection-batch-remove', `/api/collections/${nodeCollectionId}/batch/remove`, `/api/collections/${goCollectionId}/batch/remove`, { method: 'POST', body: { books: [bookItemId] } })
    await comparePair('collection-batch-add', `/api/collections/${nodeCollectionId}/batch/add`, `/api/collections/${goCollectionId}/batch/add`, { method: 'POST', body: { books: [bookItemId] } })
    await comparePair('collection-delete', `/api/collections/${nodeCollectionId}`, `/api/collections/${goCollectionId}`, { method: 'DELETE' })
    await comparePair('collection-list-after-delete', '/api/collections', '/api/collections', { method: 'GET' })
  } else {
    for (const id of ['collection-read', 'collection-update', 'collection-list', 'collection-batch-remove', 'collection-batch-add', 'collection-delete', 'collection-list-after-delete']) {
      results.push({ id, passed: false, skipped: true, prerequisite: 'both collection create calls must return independent IDs' })
    }
  }

  const [nodePlaylist, goPlaylist] = await comparePair('playlist-create', '/api/playlists', '/api/playlists', {
    method: 'POST', body: { libraryId: bookLibraryId, name: 'Parity Playlist', description: 'Source parity', items: [{ libraryItemId: bookItemId }] }
  })
  const nodePlaylistId = nodePlaylist.body?.id
  const goPlaylistId = goPlaylist.body?.id
  if (nodePlaylistId && goPlaylistId) {
    await comparePair('playlist-read', `/api/playlists/${nodePlaylistId}`, `/api/playlists/${goPlaylistId}`, { method: 'GET' })
    await comparePair('playlist-update', `/api/playlists/${nodePlaylistId}`, `/api/playlists/${goPlaylistId}`, { method: 'PATCH', body: { name: 'Parity Playlist Updated', description: 'Updated source parity' } })
    await comparePair('playlist-list', '/api/playlists', '/api/playlists', { method: 'GET' })
    // Upstream deletes an emptied playlist during batch/remove, so batch-add
    // must run first; the subsequent delete therefore targets a missing id
    // (404 on both implementations, matching deleted-emptied-playlist rules).
    await comparePair('playlist-batch-add', `/api/playlists/${nodePlaylistId}/batch/add`, `/api/playlists/${goPlaylistId}/batch/add`, { method: 'POST', body: { items: [{ libraryItemId: bookItemId }] } })
    await comparePair('playlist-batch-remove', `/api/playlists/${nodePlaylistId}/batch/remove`, `/api/playlists/${goPlaylistId}/batch/remove`, { method: 'POST', body: { items: [{ libraryItemId: bookItemId }] } })
    await comparePair('playlist-delete', `/api/playlists/${nodePlaylistId}`, `/api/playlists/${goPlaylistId}`, { method: 'DELETE' })
    await comparePair('playlist-list-after-delete', '/api/playlists', '/api/playlists', { method: 'GET' })
  } else {
    for (const id of ['playlist-read', 'playlist-update', 'playlist-list', 'playlist-batch-remove', 'playlist-batch-add', 'playlist-delete', 'playlist-list-after-delete']) {
      results.push({ id, passed: false, skipped: true, prerequisite: 'both playlist create calls must return independent IDs' })
    }
  }

  const coverRoute = `/api/items/${bookItemId}/cover`
  const coverSource = await startCoverSource(fixture.coverAsset)
  const [nodeItemBeforeCover, goItemBeforeCover] = await Promise.all([
    call(node, `/api/items/${bookItemId}`, { token: referenceAccessToken }),
    call(go, `/api/items/${bookItemId}`, { token: referenceAccessToken })
  ])
  nodeAudioPath = nodeItemBeforeCover.body?.libraryFiles?.[0]?.metadata?.path
  goAudioPath = goItemBeforeCover.body?.libraryFiles?.[0]?.metadata?.path
  for (const directory of [fixture.directories.node, fixture.directories.go]) {
    const coverDir = path.join(directory, 'metadata', 'items', bookItemId)
    fs.mkdirSync(coverDir, { recursive: true })
    fs.writeFileSync(path.join(coverDir, 'cover.svg'), '<svg/>')
  }
  if (nodeAudioPath && goAudioPath) {
    await comparePair('cover-patch-invalid-image', coverRoute, coverRoute, {
      method: 'PATCH', nodeBody: { cover: nodeAudioPath }, goBody: { cover: goAudioPath }
    })
    await comparePair('cover-patch-large-invalid-path', coverRoute, coverRoute, {
      method: 'PATCH', body: { cover: 'x'.repeat(1_100_000) }
    })
  } else {
    for (const id of ['cover-patch-invalid-image', 'cover-patch-large-invalid-path']) results.push({ id, passed: false, skipped: true, prerequisite: 'both item details must expose a library file path' })
  }
  await comparePair('cover-upload', coverRoute, coverRoute, {
    method: 'POST', file: { field: 'cover', path: fixture.coverAsset, type: 'image/png', name: 'cover-fixture.png' }
  })
  await comparePair('cover-upload-unsupported-gif', coverRoute, coverRoute, {
    method: 'POST', file: { field: 'cover', path: fixture.coverAsset, type: 'image/gif', name: 'cover-fixture.gif' }
  })
  const coverStateForComparison = async (directory) => {
    const state = await readCoverDeleteState(directory, bookItemId)
    return {
      coverPath: state.coverPath ? path.relative(path.join(directory, 'metadata'), state.coverPath) : null,
      coverExists: Boolean(state.coverPath && fs.existsSync(state.coverPath)),
      cacheFiles: state.cacheFiles,
      coverFiles: state.coverFiles
    }
  }
  const coverBeforeURLFailures = {
    node: await coverStateForComparison(fixture.directories.node),
    go: await coverStateForComparison(fixture.directories.go)
  }
  await comparePair('cover-upload-multipart-url-precedence', coverRoute, coverRoute, {
    method: 'POST', fields: { url: coverSource.url.replace('/cover.png', '/unsupported-format') },
    file: { field: 'cover', path: fixture.coverAsset, type: 'image/png', name: 'cover-fixture.png' }
  })
  await comparePair('cover-url-unsupported-image-format', coverRoute, coverRoute, {
    method: 'POST', body: { url: coverSource.url.replace('/cover.png', '/unsupported-format') }
  })
  let coverURLFailureState
  if (process.env.EXP_PROXY_SUPPORT === '1') {
    results.push({
      id: 'cover-url-ssrf-blocked', passed: false, skipped: true,
      prerequisite: 'upstream explicitly disables SSRF filtering in EXP_PROXY_SUPPORT mode; probing a private address would initiate an unsafe network request'
    })
    coverURLFailureState = { testedBlockedPrivateURL: false, reason: 'SSRF filtering is disabled in EXP_PROXY_SUPPORT mode' }
  } else {
    await comparePair('cover-url-ssrf-blocked', coverRoute, coverRoute, {
      method: 'POST', body: { url: 'http://10.255.255.1/cover.png' }
    })
  }
  const coverAfterURLFailures = {
    node: await coverStateForComparison(fixture.directories.node),
    go: await coverStateForComparison(fixture.directories.go)
  }
  coverURLFailureState = {
    testedBlockedPrivateURL: process.env.EXP_PROXY_SUPPORT !== '1',
    before: coverBeforeURLFailures,
    after: coverAfterURLFailures,
    equal: JSON.stringify(coverBeforeURLFailures) === JSON.stringify(coverAfterURLFailures) &&
      JSON.stringify(coverBeforeURLFailures.node) === JSON.stringify(coverBeforeURLFailures.go)
  }
  await comparePair('cover-url-upload', coverRoute, coverRoute, { method: 'POST', body: { url: coverSource.url } })
  const coverBeforeRedirect = {
    node: await coverStateForComparison(fixture.directories.node),
    go: await coverStateForComparison(fixture.directories.go)
  }
  await comparePair('cover-url-redirect-307', coverRoute, coverRoute, {
    method: 'POST', body: { url: coverSource.url.replace('/cover.png', '/redirect-307.png') }
  })
  const coverAfterRedirect = {
    node: await coverStateForComparison(fixture.directories.node),
    go: await coverStateForComparison(fixture.directories.go)
  }
  const coverURLRedirectState = {
    before: coverBeforeRedirect,
    after: coverAfterRedirect,
    mode: process.env.EXP_PROXY_SUPPORT === '1' ? 'exp-proxy-support' : 'default',
    equalFailureState: process.env.EXP_PROXY_SUPPORT !== '1' ||
      (JSON.stringify(coverBeforeRedirect) === JSON.stringify(coverAfterRedirect) &&
        JSON.stringify(coverBeforeRedirect.node) === JSON.stringify(coverBeforeRedirect.go))
  }
  const coverUploadState = {
    node: await coverStateForComparison(fixture.directories.node),
    go: await coverStateForComparison(fixture.directories.go)
  }
  coverUploadState.equal = coverUploadState.node.coverPath !== null &&
    coverUploadState.node.coverPath === coverUploadState.go.coverPath &&
    coverUploadState.node.coverExists && coverUploadState.go.coverExists &&
    JSON.stringify(coverUploadState.node.cacheFiles) === JSON.stringify(coverUploadState.go.cacheFiles) &&
    coverUploadState.node.coverFiles.includes('cover.svg') && coverUploadState.go.coverFiles.includes('cover.svg')
  for (const [id, query] of [
    ['cover-raw', '?raw=1'],
    ['cover-cache-default', ''],
    ['cover-cache-jpeg-size', '?width=16&height=16&format=jpeg'],
    ['cover-cache-png-size', '?width=16&height=16&format=png'],
    ['cover-cache-webp-size', '?width=16&height=16&format=webp'],
    ['cover-cache-ts-header', '?ts=1&width=16&height=16&format=jpeg']
  ]) {
    await comparePair(id, `${coverRoute}${query}`, `${coverRoute}${query}`, { method: 'GET' })
  }
  await comparePair('cover-delete', coverRoute, coverRoute, { method: 'DELETE' })
  await comparePair('cover-raw-after-delete', `${coverRoute}?raw=1`, `${coverRoute}?raw=1`, { method: 'GET' })
  await comparePair('cover-cache-after-delete', coverRoute, coverRoute, { method: 'GET' })
  await comparePair('cover-cache-jpeg-after-delete', `${coverRoute}?width=16&height=16&format=jpeg`, `${coverRoute}?width=16&height=16&format=jpeg`, { method: 'GET' })
  await comparePair('cover-cache-png-after-delete', `${coverRoute}?width=16&height=16&format=png`, `${coverRoute}?width=16&height=16&format=png`, { method: 'GET' })
  const coverDeleteState = {
    node: await readCoverDeleteState(fixture.directories.node, bookItemId),
    go: await readCoverDeleteState(fixture.directories.go, bookItemId)
  }
  coverDeleteState.equal = JSON.stringify(coverDeleteState.node) === JSON.stringify(coverDeleteState.go) &&
    coverDeleteState.node.coverPath === null && coverDeleteState.node.cacheFiles.length === 0
  let coverPatchState = { equal: false, reason: 'successful PATCH probe has not run' }
  await new Promise((resolve) => coverSource.server.close(resolve))

  const playbackBody = {
    forceTranscode: true,
    mediaPlayer: 'Parity Fixture Client',
    deviceInfo: { deviceId: 'parity-fixture-device', clientName: 'Parity Fixture Client', clientVersion: '1', deviceName: 'Fixture Device' }
  }
  const [nodeSession, goSession] = await Promise.all([
    call(node, `/api/items/${bookItemId}/play`, { method: 'POST', token: referenceAccessToken, body: playbackBody }),
    call(go, `/api/items/${bookItemId}/play`, { method: 'POST', token: referenceAccessToken, body: playbackBody })
  ])
  results.push(compare('playback-session', nodeSession, goSession))
  const nodeSessionID = nodeSession.body?.id
  const goSessionID = goSession.body?.id
  if (nodeSession.status === 200 && goSession.status === 200 && nodeSessionID && goSessionID) {
    const waitPlaylist = async (server, id) => {
      let latest
      for (let attempt = 0; attempt < 80; attempt++) {
        latest = await call(server, `/hls/${id}/output.m3u8`)
        if (latest.status === 200 || (latest.status !== 404 && latest.status !== 503)) return latest
        await new Promise((resolve) => setTimeout(resolve, 250))
      }
      return latest
    }
    const [nodePlaylist, goPlaylist] = await Promise.all([
      waitPlaylist(node, nodeSessionID), waitPlaylist(go, goSessionID)
    ])
    results.push(compare('hls-playlist', nodePlaylist, goPlaylist))
    const [nodeClose, goClose] = await Promise.all([
      call(node, `/api/session/${nodeSessionID}/close`, { method: 'POST', token: referenceAccessToken, body: {} }),
      call(go, `/api/session/${goSessionID}/close`, { method: 'POST', token: referenceAccessToken, body: {} })
    ])
    results.push(compare('session-close', nodeClose, goClose))
  } else {
    results.push({
      id: 'hls-playlist', passed: false, skipped: true,
      prerequisite: 'both playback-session endpoints must return a valid session before HLS can be fetched',
      referenceStatus: nodeSession.status, goStatus: goSession.status
    })
    results.push({
      id: 'session-close', passed: false, skipped: true,
      prerequisite: 'both playback-session endpoints must return a valid session before cleanup',
      referenceStatus: nodeSession.status, goStatus: goSession.status
    })
  }

  const smtp = await startSMTPFixture()
  try {
    await comparePair('email-test-configure-smtp', '/api/emails/settings', '/api/emails/settings', {
      method: 'PATCH', token: referenceAccessToken,
      body: { host: '127.0.0.1', port: smtp.port, secure: false, fromAddress: 'sender@example.test', testAddress: 'reader@example.test' }
    })
    let firstMessage = smtp.sessions.length
    const nodeEmailTest = await call(node, '/api/emails/test', { method: 'POST', token: referenceAccessToken, body: {} })
    const nodeMessage = smtp.sessions.slice(firstMessage).filter(session => session.message).map(smtpMessageShape)
    firstMessage = smtp.sessions.length
    const goEmailTest = await call(go, '/api/emails/test', { method: 'POST', token: referenceAccessToken, body: {} })
    const goMessage = smtp.sessions.slice(firstMessage).filter(session => session.message).map(smtpMessageShape)
    results.push(compare('email-test-success', nodeEmailTest, goEmailTest))
    results.push(compare('email-test-smtp-message',
      { status: 200, headers: {}, body: nodeMessage },
      { status: 200, headers: {}, body: goMessage }))
  } finally {
    await new Promise(resolve => smtp.server.close(resolve))
  }
  const rejectedSMTP = await startSMTPFixture({ rejectConnection: true })
  try {
    await comparePair('email-test-configure-rejected-smtp', '/api/emails/settings', '/api/emails/settings', {
      method: 'PATCH', token: referenceAccessToken,
      body: { host: '127.0.0.1', port: rejectedSMTP.port, secure: false, fromAddress: 'sender@example.test', testAddress: 'reader@example.test' }
    })
    let failedStart = rejectedSMTP.sessions.length
    const nodeFailure = await call(node, '/api/emails/test', { method: 'POST', token: referenceAccessToken, body: {} })
    const nodeFailedDeliveryCount = rejectedSMTP.sessions.slice(failedStart).filter(session => session.message).length
    failedStart = rejectedSMTP.sessions.length
    const goFailure = await call(go, '/api/emails/test', { method: 'POST', token: referenceAccessToken, body: {} })
    const goFailedDeliveryCount = rejectedSMTP.sessions.slice(failedStart).filter(session => session.message).length
    results.push(compare('email-test-verification-failure', nodeFailure, goFailure))
    results.push({
      id: 'email-test-no-message-on-verification-failure',
      passed: nodeFailedDeliveryCount === 0 && goFailedDeliveryCount === 0,
      referenceMessageCount: nodeFailedDeliveryCount,
      goMessageCount: goFailedDeliveryCount,
      differences: nodeFailedDeliveryCount === 0 && goFailedDeliveryCount === 0
        ? []
        : [{ path: '$.messageCount', reference: nodeFailedDeliveryCount, go: goFailedDeliveryCount, expected: 0 }]
    })
  } finally {
    await new Promise(resolve => rejectedSMTP.server.close(resolve))
  }

  if (ioClient) {
    const adminEventRequest = async (server, route, options) => {
      const socket = ioClient(server.url, { path: `${PREFIX}/socket.io`, timeout: 8000, reconnection: false, transports: ['polling', 'websocket'] })
      const events = []
      let timer
      try {
        await new Promise((resolve, reject) => {
          timer = setTimeout(() => reject(new Error('Socket.IO admin init timeout')), 10000)
          socket.once('connect', () => socket.emit('auth', referenceAccessToken))
          socket.once('init', () => {
            clearTimeout(timer)
            socket.on('ereader-devices-updated', payload => events.push(payload))
            resolve()
          })
          socket.once('auth_failed', payload => reject(new Error(`Socket.IO admin auth failed: ${JSON.stringify(payload)}`)))
          socket.once('connect_error', reject)
        })
        const response = await call(server, route, options)
        await new Promise(resolve => setTimeout(resolve, 300))
        return { response, events }
      } finally {
        clearTimeout(timer)
        socket.disconnect()
      }
    }
    const emailDevicesBody = { ereaderDevices: [
      { name: 'Invalid option', email: 'invalid@example.test', availabilityOption: 'invalid', users: ['discarded-user'] },
      { name: 'Empty specific users', email: 'empty@example.test', availabilityOption: 'specificUsers', users: [] },
      { name: 'Guest with users', email: 'guest@example.test', availabilityOption: 'guestOrUp', users: ['discarded-user'] },
      { name: 'Missing users', email: 'missing@example.test' },
      { name: 'Null users', email: 'null@example.test', availabilityOption: 'guestOrUp', users: null },
      { name: 'Specific users string', email: 'string@example.test', availabilityOption: 'specificUsers', users: 'user-id' },
      { name: 'Guest object users', email: 'object@example.test', availabilityOption: 'guestOrUp', users: { length: 1 } }
    ] }
    const emailDeviceRoute = '/api/emails/ereader-devices'
    const [nodeDeviceUpdate, goDeviceUpdate] = await Promise.all([
      adminEventRequest(node, emailDeviceRoute, { method: 'POST', token: referenceAccessToken, body: emailDevicesBody }),
      adminEventRequest(go, emailDeviceRoute, { method: 'POST', token: referenceAccessToken, body: emailDevicesBody })
    ])
    results.push(compare('email-devices-normalization-update', nodeDeviceUpdate.response, goDeviceUpdate.response))
    results.push(compare('email-devices-update-event',
      { status: 200, headers: {}, body: nodeDeviceUpdate.events },
      { status: 200, headers: {}, body: goDeviceUpdate.events }))
    await comparePair('email-devices-read-after-update', '/api/emails/settings', '/api/emails/settings', {
      method: 'GET', token: referenceAccessToken
    })
    const [nodeDeviceState, goDeviceState] = await Promise.all([
      readEmailDeviceState(fixture.directories.node), readEmailDeviceState(fixture.directories.go)
    ])
    results.push(compare('email-devices-persisted-state',
      { status: 200, headers: {}, body: { devices: nodeDeviceState.devices } },
      { status: 200, headers: {}, body: { devices: goDeviceState.devices } }))

    await new Promise(resolve => setTimeout(resolve, 10))
    const [nodeRepeat, goRepeat] = await Promise.all([
      adminEventRequest(node, emailDeviceRoute, { method: 'POST', token: referenceAccessToken, body: emailDevicesBody }),
      adminEventRequest(go, emailDeviceRoute, { method: 'POST', token: referenceAccessToken, body: emailDevicesBody })
    ])
    results.push(compare('email-devices-repeat', nodeRepeat.response, goRepeat.response))
    results.push(compare('email-devices-repeat-event',
      { status: 200, headers: {}, body: nodeRepeat.events },
      { status: 200, headers: {}, body: goRepeat.events }))
    const [nodeAfterRepeat, goAfterRepeat] = await Promise.all([
      readEmailDeviceState(fixture.directories.node), readEmailDeviceState(fixture.directories.go)
    ])
    const changedBody = {
      ereaderDevices: emailDevicesBody.ereaderDevices.map((device, index) =>
        index === 0 ? { ...device, email: 'changed@example.test' } : device)
    }
    await new Promise(resolve => setTimeout(resolve, 10))
    const [nodeChanged, goChanged] = await Promise.all([
      adminEventRequest(node, emailDeviceRoute, { method: 'POST', token: referenceAccessToken, body: changedBody }),
      adminEventRequest(go, emailDeviceRoute, { method: 'POST', token: referenceAccessToken, body: changedBody })
    ])
    results.push(compare('email-devices-changed', nodeChanged.response, goChanged.response))
    results.push(compare('email-devices-changed-event',
      { status: 200, headers: {}, body: nodeChanged.events },
      { status: 200, headers: {}, body: goChanged.events }))
    const [nodeAfterChange, goAfterChange] = await Promise.all([
      readEmailDeviceState(fixture.directories.node), readEmailDeviceState(fixture.directories.go)
    ])
    results.push(compare('email-devices-changed-persisted-state',
      { status: 200, headers: {}, body: { devices: nodeAfterChange.devices } },
      { status: 200, headers: {}, body: { devices: goAfterChange.devices } }))
    emailDevicesState = {
      equal: JSON.stringify(deepSort(nodeDeviceState.devices)) === JSON.stringify(deepSort(goDeviceState.devices)) &&
        JSON.stringify(deepSort(nodeAfterRepeat.devices)) === JSON.stringify(deepSort(goAfterRepeat.devices)) &&
        nodeDeviceState.updatedAt === nodeAfterRepeat.updatedAt && goDeviceState.updatedAt === goAfterRepeat.updatedAt &&
        JSON.stringify(deepSort(nodeAfterChange.devices)) === JSON.stringify(deepSort(goAfterChange.devices)) &&
        nodeAfterChange.updatedAt !== nodeAfterRepeat.updatedAt && goAfterChange.updatedAt !== goAfterRepeat.updatedAt &&
        nodeDeviceUpdate.events.length === 1 && goDeviceUpdate.events.length === 1 &&
        nodeRepeat.events.length === 0 && goRepeat.events.length === 0 &&
        nodeChanged.events.length === 1 && goChanged.events.length === 1,
      node: { normalizedDeviceCount: nodeAfterChange.devices.length, repeatUpdatedAtUnchanged: nodeDeviceState.updatedAt === nodeAfterRepeat.updatedAt, changedUpdatedAt: nodeAfterChange.updatedAt, updateEvents: nodeDeviceUpdate.events.length, repeatEvents: nodeRepeat.events.length, changedEvents: nodeChanged.events.length },
      go: { normalizedDeviceCount: goAfterChange.devices.length, repeatUpdatedAtUnchanged: goDeviceState.updatedAt === goAfterRepeat.updatedAt, changedUpdatedAt: goAfterChange.updatedAt, updateEvents: goDeviceUpdate.events.length, repeatEvents: goRepeat.events.length, changedEvents: goChanged.events.length }
    }

    for (const [id, socketPath] of [
      ['socket-connect-prefixed', `${PREFIX}/socket.io`],
      ['socket-connect-legacy', '/socket.io']
    ]) {
      const [nodeSocket, goSocket] = await Promise.all([
        socketAuthentication(ioClient, node.url, socketPath, readerAccessToken),
        socketAuthentication(ioClient, go.url, socketPath, readerAccessToken)
      ])
      const socketResult = compare(id, nodeSocket, goSocket)
      if (nodeSocket.status !== 200 || goSocket.status !== 200) socketResult.passed = false
      results.push(socketResult)
    }
    // Realtime event delivery: the reader's own progress write emits
    // user_updated scoped to the reader only.
    const progressRoute = `/api/me/progress/${bookItemId}`
    const probes = await Promise.all([
      socketUserUpdatedProbe(ioClient, node.url, `${PREFIX}/socket.io`, { readerToken: readerAccessToken, rootToken: referenceAccessToken, progressRoute }),
      socketUserUpdatedProbe(ioClient, go.url, `${PREFIX}/socket.io`, { readerToken: readerAccessToken, rootToken: referenceAccessToken, progressRoute })
    ])
    try {
      const result = compare('socket-event-user-updated', { status: probes[0].status, headers: {}, body: probes[0].body }, { status: probes[1].status, headers: {}, body: probes[1].body })
      const body = probes[0].body
      if (body.username !== fixture.readerCredentials.username || body.mediaProgressCount === null || body.rootReceived) {
        result.passed = false
        result.differences = result.differences || []
        result.differences.push({ path: '$.events', note: `node=${JSON.stringify(probes[0].body)} go=${JSON.stringify(probes[1].body)}` })
      }
      results.push(result)
    } catch (error) {
      results.push({ id: 'socket-event-user-updated', passed: false, differences: [{ path: '$', error: error.message }] })
    }
  } else {
    for (const id of ['socket-connect-prefixed', 'socket-connect-legacy']) {
      results.push({ id, passed: false, skipped: true, prerequisite: 'Install socket.io-client v4 in NODE_PATH to execute the JS protocol probe' })
    }
  }

  // Keep scanner mutations after media reads, playback, and Socket.IO checks.
  // The fixture gives each server an independent media tree and path root.
  const [nodeBeforeScan, goBeforeScan] = await Promise.all([
    call(node, `/api/items/${bookItemId}`, { token: referenceAccessToken }),
    call(go, `/api/items/${bookItemId}`, { token: referenceAccessToken })
  ])
  const [nodeLibraryScan, goLibraryScan] = await Promise.all([
    call(node, `/api/libraries/${bookLibraryId}/scan`, { method: 'POST', token: referenceAccessToken, body: {} }),
    call(go, `/api/libraries/${bookLibraryId}/scan`, { method: 'POST', token: referenceAccessToken, body: {} })
  ])
  results.push(compare('library-scan-rerun', nodeLibraryScan, goLibraryScan))
  const waitForRescan = async (server, oldLastScan) => {
    let latest
    for (let attempt = 0; attempt < 120; attempt++) {
      latest = await call(server, `/api/items/${bookItemId}`, { token: referenceAccessToken })
      if (latest.status === 200 && latest.body?.lastScan !== oldLastScan) return latest
      await new Promise((resolve) => setTimeout(resolve, 250))
    }
    return latest
  }
  const [nodeAfterScan, goAfterScan] = await Promise.all([
    waitForRescan(node, nodeBeforeScan.body?.lastScan), waitForRescan(go, goBeforeScan.body?.lastScan)
  ])
  results.push(compare('book-detail-after-library-scan', nodeAfterScan, goAfterScan))
  const [nodeItemScan, goItemScan] = await Promise.all([
    call(node, `/api/items/${bookItemId}/scan`, { method: 'POST', token: referenceAccessToken, body: {} }),
    call(go, `/api/items/${bookItemId}/scan`, { method: 'POST', token: referenceAccessToken, body: {} })
  ])
  results.push(compare('book-item-scan-rerun', nodeItemScan, goItemScan))
  const [nodeListAfterScan, goListAfterScan] = await Promise.all([
    call(node, `/api/libraries/${bookLibraryId}/items?limit=10&page=0&sort=media.metadata.title`, { token: referenceAccessToken }),
    call(go, `/api/libraries/${bookLibraryId}/items?limit=10&page=0&sort=media.metadata.title`, { token: referenceAccessToken })
  ])
  results.push(compare('book-list-after-library-scan', nodeListAfterScan, goListAfterScan))

  // Reuse the valid scanned audio path after all audio/playback and scan
  // checks. Upstream validates image bytes, not the source filename suffix.
  if (nodeAudioPath && goAudioPath) {
    for (const directory of [fixture.directories.node, fixture.directories.go]) {
      const cacheDir = path.join(directory, 'metadata', 'cache', 'covers')
      fs.mkdirSync(cacheDir, { recursive: true })
      fs.writeFileSync(path.join(cacheDir, `${bookItemId}_16.jpeg`), 'stale-cache')
    }
    fs.copyFileSync(fixture.coverAsset, nodeAudioPath)
    fs.copyFileSync(fixture.coverAsset, goAudioPath)
    const [nodePatchSuccess, goPatchSuccess] = await Promise.all([
      call(node, coverRoute, { method: 'PATCH', token: referenceAccessToken, body: { cover: nodeAudioPath } }),
      call(go, coverRoute, { method: 'PATCH', token: referenceAccessToken, body: { cover: goAudioPath } })
    ])
    results.push(compare('cover-patch-success', nodePatchSuccess, goPatchSuccess))
    const [nodePatch, goPatch] = await Promise.all([
      call(node, coverRoute, { method: 'PATCH', token: referenceAccessToken, body: { cover: nodeAudioPath } }),
      call(go, coverRoute, { method: 'PATCH', token: referenceAccessToken, body: { cover: goAudioPath } })
    ])
    results.push(compare('cover-patch-repeat', nodePatch, goPatch))
    coverPatchState = {
      node: await readCoverDeleteState(fixture.directories.node, bookItemId),
      go: await readCoverDeleteState(fixture.directories.go, bookItemId)
    }
    for (const [side, directory] of [['node', fixture.directories.node], ['go', fixture.directories.go]]) {
      const coverPath = coverPatchState[side].coverPath
      coverPatchState[side].coverPath = coverPath ? path.relative(path.join(directory, 'metadata'), coverPath) : null
    }
    coverPatchState.equal = coverPatchState.node.coverPath !== null &&
      coverPatchState.node.coverPath === coverPatchState.go.coverPath &&
      coverPatchState.node.cacheFiles.length === 0 && coverPatchState.go.cacheFiles.length === 0 &&
      coverPatchState.node.coverFiles.includes('cover.png') && coverPatchState.go.coverFiles.includes('cover.png') &&
      coverPatchState.node.coverFiles.includes('cover.svg') && coverPatchState.go.coverFiles.includes('cover.svg')
  } else {
    for (const id of ['cover-patch-success', 'cover-patch-repeat']) results.push({ id, passed: false, skipped: true, prerequisite: 'both item details must expose a library file path' })
  }

  // Run this fixture-only rename probe last: upstream's filesystem watcher
  // observes the rename and may start a scan, so no later API cases should be
  // exposed to that background work.
  const [nodeDownloadItem, goDownloadItem] = await Promise.all([
    call(node, `/api/items/${bookItemId}`, { token: referenceAccessToken }),
    call(go, `/api/items/${bookItemId}`, { token: referenceAccessToken })
  ])
  const nodeDownloadPath = nodeDownloadItem.body?.libraryFiles?.find((file) => file.ino === audioFileId)?.metadata?.path
  const goDownloadPath = goDownloadItem.body?.libraryFiles?.find((file) => file.ino === audioFileIdGo)?.metadata?.path
  if (typeof nodeDownloadPath !== 'string' || typeof goDownloadPath !== 'string') {
    throw new Error('item-file-download-disk-missing: source-created fixture file paths are missing')
  }
  const hiddenDownloads = []
  try {
    for (const filePath of [nodeDownloadPath, goDownloadPath]) {
      const hiddenPath = `${filePath}.parity-hidden`
      fs.renameSync(filePath, hiddenPath)
      hiddenDownloads.push([hiddenPath, filePath])
    }
    const [nodeMissingDisk, goMissingDisk] = await Promise.all([
      call(node, fileDownloadRoutes.node, { token: referenceAccessToken }),
      call(go, fileDownloadRoutes.go, { token: referenceAccessToken })
    ])
    results.push(compare('item-file-download-disk-missing', nodeMissingDisk, goMissingDisk))
  } finally {
    for (const [hiddenPath, filePath] of hiddenDownloads.reverse()) fs.renameSync(hiddenPath, filePath)
  }

  // This malformed upstream payload dereferences payload.urls.length and can
  // terminate the reference process. Run it only in its isolated opt-in probe.
  if (process.env.PARITY_PROBE_UPSTREAM_NOTIFICATION_CRASH === '1') {
    const missingURLs = { method: 'POST', token: referenceAccessToken, body: { eventName: 'onTest' } }
    const [nodeMissingURLs, goMissingURLs] = await Promise.allSettled([
      call(node, '/api/notifications', missingURLs),
      call(go, '/api/notifications', missingURLs)
    ])
    if (nodeMissingURLs.status === 'fulfilled' && goMissingURLs.status === 'fulfilled') {
      results.push(compare('notification-create-missing-urls', nodeMissingURLs.value, goMissingURLs.value))
    } else {
      results.push({
        id: 'notification-create-missing-urls',
        passed: false,
        referenceStatus: nodeMissingURLs.status === 'fulfilled' ? nodeMissingURLs.value.status : null,
        goStatus: goMissingURLs.status === 'fulfilled' ? goMissingURLs.value.status : null,
        referenceTransportError: nodeMissingURLs.status === 'rejected' ? String(nodeMissingURLs.reason) : null,
        goTransportError: goMissingURLs.status === 'rejected' ? String(goMissingURLs.reason) : null,
        goBody: goMissingURLs.status === 'fulfilled' ? goMissingURLs.value.body : null,
        differences: [{ path: '$.failure', reference: 'upstream process termination', go: 'handled HTTP response' }]
      })
    }
  }

  const reportCases = selectedRun ? results.filter(result => requestedCases.has(result.id)) : results
  const report = {
    schemaVersion: 1,
    baseline: fixture.baseline,
    casesFile: path.relative(path.resolve(__dirname, '../../..'), casesPath),
    fixtureUserId: fixture.userId,
    generatedAt: new Date().toISOString(),
    normalizationApplied: {
      loginRefresh: ['user.accessToken', 'user.refreshToken', 'user.lastSeen'],
      authorization: ['user.lastSeen', 'user.updatedAt', 'serverSettings.backupPath'],
      progressRead: ['lastUpdate', 'startedAt', 'finishedAt'],
      playbackSession: ['id', 'startedAt', 'updatedAt', 'deviceInfo.id', 'audioTracks[].contentUrl session UUID'],
    localSession: ['per-side access/refresh tokens and JWT fingerprints; dedicated reader sessions; cleared epoch cookies compared exactly'],
    localSessionCookie: ['within-side actual login connect.sid sent to logout; logout and repeated logout must each replace the previously presented signed session ID; random values normalized only across Node/Go sides'],
      refreshGraceReplay: ['set-cookie refresh_token value and Expires date normalized; Max-Age/Path/HttpOnly/order exact; login/refresh 200 etag+content-length normalized with the etag length-prefix integrity check (fixture root names differ in byte length)'],
      inactiveUser: ['per-side user UUID; creation isActive:false verified against login denial; denied-login Content-Length and absence of Content-Type compared exactly']
    },
    note: 'Failed cases are compatibility gaps. Skipped cases are unmet prerequisites and are reported separately; neither is treated as equivalent behavior.',
    summary: {
      total: reportCases.length,
      passed: reportCases.filter((r) => r.passed).length,
      failed: reportCases.filter((r) => !r.passed && !r.skipped).length,
      skipped: reportCases.filter((r) => r.skipped).length
    },
    statefulChecks: { coverDelete: coverDeleteState, coverUpload: coverUploadState, coverPatch: coverPatchState, coverURLFailure: coverURLFailureState, coverURLRedirect: coverURLRedirectState, emailDevices: emailDevicesState },
    cases: reportCases
  }
  report.summary.executed = report.cases.filter(result => !result.skipped).length
  const output = path.resolve(args.out || 'parity-results.json')
  fs.writeFileSync(output, `${JSON.stringify(report, null, 2)}\n`)
  console.log(JSON.stringify({ output, summary: report.summary }, null, 2))
  if (report.summary.executed === 0) {
    console.error('0 cases executed')
    process.exitCode = 1
  }
  if (report.summary.failed || (!selectedRun && (!report.statefulChecks.coverDelete.equal || !report.statefulChecks.coverUpload.equal || !report.statefulChecks.coverPatch.equal || (report.statefulChecks.emailDevices && !report.statefulChecks.emailDevices.equal)))) process.exitCode = 1
}

main().catch((error) => {
  process.stderr.write(`${error.stack || error}\n`)
  process.exitCode = 1
})
