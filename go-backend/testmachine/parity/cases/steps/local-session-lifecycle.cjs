'use strict'

const path = require('node:path')

module.exports = async function runLocalSessionLifecycle({ runtime }) {
  const { selectedRun, requestedCases, call, fixture, node, go, results, compare, referenceAccessToken, bookLibraryId } = runtime
  let nodeLocal = null
  let goLocal = null
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
}
