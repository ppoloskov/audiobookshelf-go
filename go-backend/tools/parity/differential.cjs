#!/usr/bin/env node
'use strict'

const fs = require('node:fs')
const path = require('node:path')
const { parseArgs } = require('./lib/args.cjs')
const { requestJSON } = require('./lib/http.cjs')
const { writeJSON } = require('./lib/artifacts.cjs')
const { executeCaseGroup, executeNamedSteps, runStoredCase: executeStoredCase, caseInventoryError } = require('./lib/case-data.cjs')
const { setTimeout: delay } = require('node:timers/promises')
const { startCoverSource, startSMTPFixture, startNotificationAppriseFixture, smtpMessageShape, readEmailDeviceState, readCoverDeleteState } = require('./lib/local-fixtures.cjs')
const { loadSocketIOClient, socketNotificationProbe, socketAuthentication, socketUserUpdatedProbe } = require('./lib/socket-probes.cjs')
const { configureComparison, compare, deepSort, normalizeCase } = require('./lib/compare.cjs')
const { REPEATABILITY_POLICY } = require('./compare-reports.cjs')
const { detectFFmpegProfile, webpExpectedStatus } = require('./lib/ffmpeg-profile.cjs')
const casesPath = path.resolve(__dirname, 'cases/core.json')
const PREFIX = '/audiobookshelf'

function fullRunIsComplete(summary) {
  return summary.skipped === 0 && summary.executed === summary.total
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
  'playlist-from-collection-create': 200, 'playlist-from-collection-read': 200, 'playlist-from-collection-delete': 200,
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

function validatedArgs() {
  const out = parseArgs()
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
  const response = await requestJSON(server.url, route, {
    pathPrefix: PREFIX,
    method: options.method || 'GET',
    redirect: 'manual',
    signal: AbortSignal.timeout(15000),
    jsonContentType: false,
    bodyIsRaw: true,
    body: requestBody,
    headers: {
      ...(options.token ? { Authorization: `Bearer ${options.token}` } : {}),
      ...(options.refreshToken ? { 'X-Refresh-Token': options.refreshToken } : {}),
      ...(options.headers || {}),
      ...(options.body !== undefined && (options.method === 'POST' || options.method === 'PATCH') ? { 'Content-Type': 'application/json' } : {}),
      ...(options.method === 'POST' && route === '/login' ? { 'X-Return-Tokens': 'true' } : {})
    }
  })
  const raw = response.bytes
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

















// Zip responses cannot be compared byte-for-byte: local-file header fields
// (per-entry metadata ordering, filesystem block data) legitimately differ
// between implementations. Canonicalize both archives to the sorted set of
// name + size + CRC32 (unzip -lv); equal CRC entries prove byte-equal content
// because both zippers use STORE mode (upstream zlib level 0).














// Realtime event semantics per user: patching the READER's progress emits
// 'user_updated' only to the owning user (MeController.js:281-286
// clientEmitter(req.user.id, ...)); a root client must not receive it.


async function main() {
  const args = validatedArgs()
  const ffmpegProfile = detectFFmpegProfile()
  const expectedWebpStatus = webpExpectedStatus(ffmpegProfile)
  EXPECTED_STATUS['cover-cache-default'] = expectedWebpStatus
  EXPECTED_STATUS['cover-cache-webp-size'] = expectedWebpStatus
  const requestedIDs = (args.cases || '').split(',').map(value => value.trim()).filter(Boolean)
  const requestedCases = new Set(requestedIDs)
  if (requestedIDs.length !== requestedCases.size) throw new Error('duplicate requested case ID')
  const selectedRun = requestedCases.size > 0
  const caseFile = casesPath
  const executableCases = JSON.parse(fs.readFileSync(caseFile, 'utf8'))
  const inlineCases = new Map(executableCases.map(testCase => [testCase.id, testCase]))
  const knownCaseIds = new Set([
    ...Object.keys(EXPECTED_STATUS),
    ...inlineCases.keys()
  ])
  const unknownCases = [...requestedCases].filter(id => !knownCaseIds.has(id))
  if (unknownCases.length) throw new Error(`unknown case ID(s): ${unknownCases.join(', ')}`)
  const fixture = JSON.parse(fs.readFileSync(path.resolve(args.fixture), 'utf8'))
  const fixturePathRoots = {
    node: { media: fixture.directories.nodeMedia, metadata: path.join(fixture.directories.node, 'metadata') },
    go: { media: fixture.directories.goMedia, metadata: path.join(fixture.directories.go, 'metadata') }
  }
  // Per-copy inode values used inside serialized content URLs (e.g. track
  // contentUrl paths embed the audio file inode). Inode strings that appear as
  // full field values are normalized per case by checkAndReplaceInode, so this
  // replacement only needs to cover substring occurrences.
  const manifestInos = [fixture.items.audioFile, fixture.items.audioFileGo]
    .filter((value) => typeof value === 'string' && /^\d+$/.test(value))
  configureComparison({ expectedStatus: EXPECTED_STATUS, fixturePathRoots, manifestInos })
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
    const { nodeToken, goToken, nodeBody, goBody, anonymous, notes, ...requestOptions } = options
    const [reference, candidate] = await Promise.all([
      call(node, referenceRoute, { ...requestOptions, body: nodeBody ?? requestOptions.body, token: anonymous ? undefined : nodeToken || requestOptions.token || referenceAccessToken }),
      call(go, goRoute, { ...requestOptions, body: goBody ?? requestOptions.body, token: anonymous ? undefined : goToken || requestOptions.token || referenceAccessToken })
    ])
    results.push(compare(id, reference, candidate, notes || []))
    return [reference, candidate]
  }
  const runStoredCase = (id, context) => executeStoredCase(id, context, inlineCases, comparePair)

  const nodeLogin = await call(node, '/login', { method: 'POST', body: credentials })
  const goLogin = await call(go, '/login', { method: 'POST', body: credentials })
  if (!selectedRun || requestedCases.has('login')) results.push(compare('login', nodeLogin, goLogin))
  const referenceAccessToken = nodeLogin.body?.user?.accessToken
  const referenceRefreshToken = nodeLogin.body?.user?.refreshToken
  if (!referenceAccessToken || !referenceRefreshToken) {
    throw new Error('reference login did not return tokens; fixture/server auth setup is invalid')
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
  const caseDataContext = { bookLibraryId, bookItemId, audioFileId, audioFileIdGo, rootToken: referenceAccessToken, readerToken: readerAccessToken }

  let nodeAudioPath
  let goAudioPath
  await executeNamedSteps({ steps: path.join(__dirname, 'cases/steps.json'), stepIDs: ['local-session-lifecycle'], selectedRun, requestedCases, context: caseDataContext, runtime: { selectedRun, requestedCases, call, fixture, node, go, results, compare, referenceAccessToken, bookLibraryId }, comparePair })

  if (selectedRun) {
    for (const id of ['direct-file-range', 'direct-file-head-range', 'direct-file-invalid-range', 'direct-file-head-invalid-range']) {
      if (requestedCases.has(id)) await runStoredCase(id, caseDataContext)
    }
    const report = {
      schemaVersion: 2,
      repeatabilityPolicy: REPEATABILITY_POLICY,
      baseline: fixture.baseline,
      capabilities: { ffmpeg: ffmpegProfile },
      fixtureProvenance: { nodeMediaRoot: fixture.directories.nodeMedia, goMediaRoot: fixture.directories.goMedia, nodeMetadataRoot: path.join(fixture.directories.node, 'metadata'), goMetadataRoot: path.join(fixture.directories.go, 'metadata') },
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
    const inventoryError = caseInventoryError(requestedCases, report.cases)
    if (inventoryError) {
      report.executionError = inventoryError
      process.exitCode = 1
    }
    report.summary.executed = report.cases.filter(result => !result.skipped).length
    const output = path.resolve(args.out || 'parity-results.json')
    writeJSON(output, report)
    console.log(JSON.stringify({ output, summary: report.summary }, null, 2))
    if (report.summary.executed === 0) {
      console.error('0 cases executed')
      process.exitCode = 1
    }
    if (report.summary.failed || report.summary.skipped) process.exitCode = 1
    return
  }

  const [fullStatefulChecks] = await executeNamedSteps({ steps: path.join(__dirname, 'cases/steps.json'), stepIDs: ['full-core-workflows'], selectedRun, requestedCases, context: caseDataContext, runtime: { caseFile, selectedRun, requestedCases, comparePair, bookLibraryId, bookItemId, audioFileId, audioFileIdGo, nodeLogin, goLogin, referenceAccessToken, referenceRefreshToken, readerAccessToken, results, compare, call, runStoredCase, node, go, fixture, ioClient, emailDevicesState, startCoverSource, startSMTPFixture, startNotificationAppriseFixture, smtpMessageShape, readEmailDeviceState, readCoverDeleteState, PREFIX, socketNotificationProbe, socketAuthentication, socketUserUpdatedProbe, normalizeCase, deepSort }, comparePair })

  const reportCases = selectedRun ? results.filter(result => requestedCases.has(result.id)) : results
  const inventoryError = caseInventoryError(selectedRun ? requestedCases : knownCaseIds, reportCases)
  const report = {
    schemaVersion: 2,
    repeatabilityPolicy: REPEATABILITY_POLICY,
    baseline: fixture.baseline,
    fixtureProvenance: { nodeMediaRoot: fixture.directories.nodeMedia, goMediaRoot: fixture.directories.goMedia, nodeMetadataRoot: path.join(fixture.directories.node, 'metadata'), goMetadataRoot: path.join(fixture.directories.go, 'metadata') },
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
    capabilities: { ffmpeg: ffmpegProfile },
    note: 'Failed cases are compatibility gaps. Skipped cases are unmet prerequisites and are reported separately; neither is treated as equivalent behavior.',
    executionError: inventoryError,
    summary: {
      total: reportCases.length,
      passed: reportCases.filter((r) => r.passed).length,
      failed: reportCases.filter((r) => !r.passed && !r.skipped).length,
      skipped: reportCases.filter((r) => r.skipped).length
    },
    statefulChecks: fullStatefulChecks,
    cases: reportCases
  }
  report.summary.executed = report.cases.filter(result => !result.skipped).length
  const output = path.resolve(args.out || 'parity-results.json')
  writeJSON(output, report)
  console.log(JSON.stringify({ output, summary: report.summary }, null, 2))
  if (report.summary.executed === 0) {
    console.error('0 cases executed')
    process.exitCode = 1
  }
  if (inventoryError || report.summary.failed || (!selectedRun && !fullRunIsComplete(report.summary)) || (!selectedRun && (!report.statefulChecks.coverDelete.equal || !report.statefulChecks.coverUpload.equal || !report.statefulChecks.coverPatch.equal || (report.statefulChecks.emailDevices && !report.statefulChecks.emailDevices.equal)))) process.exitCode = 1
}

if (require.main === module) {
  main().catch((error) => {
    process.stderr.write(`${error.stack || error}\n`)
    process.exitCode = 1
  })
}

module.exports = { fullRunIsComplete }
