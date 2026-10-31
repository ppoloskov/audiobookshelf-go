'use strict'

const fs = require('node:fs')
const path = require('node:path')
const { setTimeout: delay } = require('node:timers/promises')
const { executeCaseGroup, executeNamedSteps } = require('../../lib/case-data.cjs')

module.exports = async function runFullCoreWorkflows({ context, runtime }) {
  const { caseFile, selectedRun, requestedCases, comparePair, bookLibraryId, bookItemId, audioFileId, audioFileIdGo, nodeLogin, goLogin, referenceAccessToken, referenceRefreshToken, readerAccessToken, results, compare, call, runStoredCase, node, go, fixture, ioClient, emailDevicesState: initialEmailDevicesState, startCoverSource, startSMTPFixture, startNotificationAppriseFixture, smtpMessageShape, readEmailDeviceState, readCoverDeleteState, PREFIX, socketNotificationProbe, socketAuthentication, socketUserUpdatedProbe, normalizeCase, deepSort } = runtime
  let emailDevicesState = initialEmailDevicesState
  let nodeAudioPath
  let goAudioPath
  // Use the valid upstream access token on both independent database copies.
  // This keeps read comparisons useful before Go login/token issuance lands.
  await executeCaseGroup({ cases: caseFile, group: 'common', context, selectedRun, requestedCases, comparePair })
  // The media copies carry different inodes, so case data supplies each path.
  await executeCaseGroup({ cases: caseFile, group: 'direct-files', context, selectedRun, requestedCases, comparePair })

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
  await executeNamedSteps({ steps: path.join(__dirname, '..', 'steps.json'), stepIDs: ['bookmark-lifecycle'], selectedRun, requestedCases, context, comparePair })

  // Static errors and malformed/empty inputs are compared without body normalization.
  await executeCaseGroup({ cases: caseFile, group: 'errors', context, selectedRun, requestedCases, comparePair })
  await executeNamedSteps({ steps: path.join(__dirname, '..', 'steps.json'), stepIDs: ['notification-lifecycle'], selectedRun, requestedCases, context, runtime: { selectedRun, requestedCases, bookLibraryId, referenceAccessToken, results, runStoredCase, compare, startNotificationAppriseFixture, ioClient, node, go, PREFIX, socketNotificationProbe, call, delay }, comparePair })

  if (!selectedRun || requestedCases.has('playlist-empty-items')) await runStoredCase("playlist-empty-items", { "bookLibraryId": bookLibraryId, "referenceAccessToken": referenceAccessToken })
  if (!selectedRun || requestedCases.has('series-list-paginated')) await runStoredCase("series-list-paginated", { "bookLibraryId": bookLibraryId })
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
    await runStoredCase("library-read", { "goLibraryId": goLibraryId, "nodeLibraryId": nodeLibraryId })
    await runStoredCase("library-update", { "goLibraryId": goLibraryId, "nodeLibraryId": nodeLibraryId })
    await runStoredCase("library-list-after-create", {  })
    await runStoredCase("library-delete", { "goLibraryId": goLibraryId, "nodeLibraryId": nodeLibraryId })
    await runStoredCase("library-list-after-delete", {  })
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
  const [nodeUser, goUser] = await runStoredCase("user-create", { "userPayload": userPayload })
  const nodeUserId = nodeUser.body?.user?.id
  const goUserId = goUser.body?.user?.id
  if (nodeUserId && goUserId) {
    await runStoredCase("user-read", { "goUserId": goUserId, "nodeUserId": nodeUserId })
    await runStoredCase("user-update", { "goUserId": goUserId, "nodeUserId": nodeUserId })
    await runStoredCase("user-list", {  })
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
      await runStoredCase("api-key-list", {  })
      await runStoredCase("api-key-auth-library-read", { "goKey.apiKey": goKey.apiKey, "nodeKey.apiKey": nodeKey.apiKey })
      await runStoredCase("api-key-denied-download", { "bookItemId": bookItemId, "goKey.apiKey": goKey.apiKey, "nodeKey.apiKey": nodeKey.apiKey })
      await runStoredCase("item-file-download-denied", { "fileDownloadRoutes.go": fileDownloadRoutes.go, "fileDownloadRoutes.node": fileDownloadRoutes.node, "goKey.apiKey": goKey.apiKey, "nodeKey.apiKey": nodeKey.apiKey })
      const missingNodeFileRoute = `/api/items/${bookItemId}/file/absent-file-ino/download`
      const missingGoFileRoute = `/api/items/${bookItemId}/file/absent-file-ino/download`
      await runStoredCase("item-file-download-denied-missing-file", { "goKey.apiKey": goKey.apiKey, "missingGoFileRoute": missingGoFileRoute, "missingNodeFileRoute": missingNodeFileRoute, "nodeKey.apiKey": nodeKey.apiKey })
      await runStoredCase("api-key-update", { "goApiKeyId": goApiKeyId, "nodeApiKeyId": nodeApiKeyId })
      await runStoredCase("api-key-delete", { "goApiKeyId": goApiKeyId, "nodeApiKeyId": nodeApiKeyId })
    } else {
      results.push({ id: 'api-key-list', passed: false, skipped: true, prerequisite: 'both API key create calls must return active key tokens' })
      for (const id of ['api-key-auth-library-read', 'api-key-denied-download', 'item-file-download-denied', 'item-file-download-denied-missing-file', 'api-key-update', 'api-key-delete']) results.push({ id, passed: false, skipped: true, prerequisite: 'both API key create calls must return active key tokens' })
    }
    await runStoredCase("user-delete", { "goUserId": goUserId, "nodeUserId": nodeUserId })
    await runStoredCase("user-list-after-delete", {  })
  } else {
    for (const id of ['user-read', 'user-update', 'user-list', 'api-key-create', 'api-key-list', 'api-key-auth-library-read', 'api-key-denied-download', 'api-key-update', 'api-key-delete', 'user-delete', 'user-list-after-delete']) {
      results.push({ id, passed: false, skipped: true, prerequisite: 'both user create calls must return independent IDs' })
    }
  }
  const [nodeCollection, goCollection] = await runStoredCase("collection-create", { "bookItemId": bookItemId, "bookLibraryId": bookLibraryId })
  const nodeCollectionId = nodeCollection.body?.id
  const goCollectionId = goCollection.body?.id
  if (nodeCollectionId && goCollectionId) {
    await runStoredCase("collection-read", { "goCollectionId": goCollectionId, "nodeCollectionId": nodeCollectionId })
    // Source PlaylistController.createFromCollection persists each book's media
    // ID. Reading the new playlist independently witnesses the saved relation.
    const [nodeConverted, goConverted] = await runStoredCase('playlist-from-collection-create', { nodeCollectionId, goCollectionId })
    if (nodeConverted.body?.id && goConverted.body?.id) {
      const converted = { nodePlaylistId: nodeConverted.body.id, goPlaylistId: goConverted.body.id }
      await runStoredCase('playlist-from-collection-read', converted)
      await runStoredCase('playlist-from-collection-delete', converted)
    } else {
      for (const id of ['playlist-from-collection-read', 'playlist-from-collection-delete']) results.push({ id, passed: false, skipped: true, prerequisite: 'both collection conversions must create a playlist' })
    }
    await runStoredCase("collection-update", { "goCollectionId": goCollectionId, "nodeCollectionId": nodeCollectionId })
    await runStoredCase("collection-list", {  })
    await runStoredCase("collection-batch-remove", { "bookItemId": bookItemId, "goCollectionId": goCollectionId, "nodeCollectionId": nodeCollectionId })
    await runStoredCase("collection-batch-add", { "bookItemId": bookItemId, "goCollectionId": goCollectionId, "nodeCollectionId": nodeCollectionId })
    await runStoredCase("collection-delete", { "goCollectionId": goCollectionId, "nodeCollectionId": nodeCollectionId })
    await runStoredCase("collection-list-after-delete", {  })
  } else {
    for (const id of ['collection-read', 'playlist-from-collection-create', 'playlist-from-collection-read', 'playlist-from-collection-delete', 'collection-update', 'collection-list', 'collection-batch-remove', 'collection-batch-add', 'collection-delete', 'collection-list-after-delete']) {
      results.push({ id, passed: false, skipped: true, prerequisite: 'both collection create calls must return independent IDs' })
    }
  }

  const [nodePlaylist, goPlaylist] = await runStoredCase("playlist-create", { "bookItemId": bookItemId, "bookLibraryId": bookLibraryId })
  const nodePlaylistId = nodePlaylist.body?.id
  const goPlaylistId = goPlaylist.body?.id
  if (nodePlaylistId && goPlaylistId) {
    await runStoredCase("playlist-read", { "goPlaylistId": goPlaylistId, "nodePlaylistId": nodePlaylistId })
    await runStoredCase("playlist-update", { "goPlaylistId": goPlaylistId, "nodePlaylistId": nodePlaylistId })
    await runStoredCase("playlist-list", {  })
    // Upstream deletes an emptied playlist during batch/remove, so batch-add
    // must run first; the subsequent delete therefore targets a missing id
    // (404 on both implementations, matching deleted-emptied-playlist rules).
    await runStoredCase("playlist-batch-add", { "bookItemId": bookItemId, "goPlaylistId": goPlaylistId, "nodePlaylistId": nodePlaylistId })
    await runStoredCase("playlist-batch-remove", { "bookItemId": bookItemId, "goPlaylistId": goPlaylistId, "nodePlaylistId": nodePlaylistId })
    await runStoredCase("playlist-delete", { "goPlaylistId": goPlaylistId, "nodePlaylistId": nodePlaylistId })
    await runStoredCase("playlist-list-after-delete", {  })
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
    await runStoredCase("cover-patch-invalid-image", { "coverRoute": coverRoute, "goAudioPath": goAudioPath, "nodeAudioPath": nodeAudioPath })
    await runStoredCase("cover-patch-large-invalid-path", { "coverRoute": coverRoute })
  } else {
    for (const id of ['cover-patch-invalid-image', 'cover-patch-large-invalid-path']) results.push({ id, passed: false, skipped: true, prerequisite: 'both item details must expose a library file path' })
  }
  await runStoredCase("cover-upload", { "coverRoute": coverRoute, "fixture.coverAsset": fixture.coverAsset })
  await runStoredCase("cover-upload-unsupported-gif", { "coverRoute": coverRoute, "fixture.coverAsset": fixture.coverAsset })
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
  await runStoredCase("cover-upload-multipart-url-precedence", { "coverRoute": coverRoute, "coverSource.url": coverSource.url, "fixture.coverAsset": fixture.coverAsset })
  await runStoredCase("cover-url-unsupported-image-format", { "coverRoute": coverRoute, "coverSource.url": coverSource.url })
  let coverURLFailureState
  if (process.env.EXP_PROXY_SUPPORT === '1') {
    results.push({
      id: 'cover-url-ssrf-blocked', passed: false, skipped: true,
      prerequisite: 'upstream explicitly disables SSRF filtering in EXP_PROXY_SUPPORT mode; probing a private address would initiate an unsafe network request'
    })
    coverURLFailureState = { testedBlockedPrivateURL: false, reason: 'SSRF filtering is disabled in EXP_PROXY_SUPPORT mode' }
  } else {
    await runStoredCase("cover-url-ssrf-blocked", { "coverRoute": coverRoute })
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
  await runStoredCase("cover-url-upload", { "coverRoute": coverRoute, "coverSource.url": coverSource.url })
  const coverBeforeRedirect = {
    node: await coverStateForComparison(fixture.directories.node),
    go: await coverStateForComparison(fixture.directories.go)
  }
  await runStoredCase("cover-url-redirect-307", { "coverRoute": coverRoute, "coverSource.url": coverSource.url })
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
  await executeCaseGroup({ cases: caseFile, group: 'cover-cache', context: { coverRoute }, selectedRun, requestedCases, comparePair })
  await runStoredCase("cover-delete", { "coverRoute": coverRoute })
  await runStoredCase("cover-raw-after-delete", { "coverRoute": coverRoute })
  await runStoredCase("cover-cache-after-delete", { "coverRoute": coverRoute })
  await runStoredCase("cover-cache-jpeg-after-delete", { "coverRoute": coverRoute })
  await runStoredCase("cover-cache-png-after-delete", { "coverRoute": coverRoute })
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
    await runStoredCase("email-test-configure-smtp", { "referenceAccessToken": referenceAccessToken, "smtp.port": smtp.port })
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
    await runStoredCase("email-test-configure-rejected-smtp", { "referenceAccessToken": referenceAccessToken, "rejectedSMTP.port": rejectedSMTP.port })
    let failedStart = rejectedSMTP.sessions.length
    const nodeFailure = await call(node, '/api/emails/test', { method: 'POST', token: referenceAccessToken, body: {} })
    const nodeFailedDeliveryCount = rejectedSMTP.sessions.slice(failedStart).filter(session => session.message).length
    failedStart = rejectedSMTP.sessions.length
    const goFailure = await call(go, '/api/emails/test', { method: 'POST', token: referenceAccessToken, body: {} })
    const goFailedDeliveryCount = rejectedSMTP.sessions.slice(failedStart).filter(session => session.message).length
    results.push(compare('email-test-verification-failure', nodeFailure, goFailure))
    results.push({
      id: 'email-test-no-message-on-verification-failure',
      passed: nodeFailedDeliveryCount === 0 && goFailedDeliveryCount === 0 && nodeFailure.status === 400 && goFailure.status === 400,
      expectedStatus: 400,
      referenceStatus: nodeFailure.status,
      goStatus: goFailure.status,
      referenceMessageCount: nodeFailedDeliveryCount,
      goMessageCount: goFailedDeliveryCount,
      differences: nodeFailedDeliveryCount === 0 && goFailedDeliveryCount === 0 && nodeFailure.status === 400 && goFailure.status === 400
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
    await runStoredCase("email-devices-read-after-update", { "referenceAccessToken": referenceAccessToken })
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

  return { coverDelete: coverDeleteState, coverUpload: coverUploadState, coverPatch: coverPatchState, coverURLFailure: coverURLFailureState, coverURLRedirect: coverURLRedirectState, emailDevices: emailDevicesState }
}
