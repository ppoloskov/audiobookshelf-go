'use strict'

const fs = require('node:fs')
const path = require('node:path')
const crypto = require('node:crypto')
const os = require('node:os')
const { spawnSync } = require('node:child_process')

let EXPECTED_STATUS = {}
let FIXTURE_PATH_ROOTS = null
let MANIFEST_INOS = []

function configureComparison({ expectedStatus, fixturePathRoots, manifestInos }) {
  EXPECTED_STATUS = expectedStatus
  FIXTURE_PATH_ROOTS = fixturePathRoots
  MANIFEST_INOS = manifestInos
}

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
    'playlist-from-collection-create', 'playlist-from-collection-read',
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
  if (['playlist-create', 'playlist-read', 'playlist-update', 'playlist-batch-add', 'playlist-batch-remove', 'playlist-empty-items', 'playlist-from-collection-create', 'playlist-from-collection-read'].includes(id)) {
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
      expectedStatus: EXPECTED_STATUS[id],
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
    expectedStatus,
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

module.exports = { configureComparison, compare, deepSort, normalizeCase }
