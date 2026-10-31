#!/usr/bin/env node
'use strict'

// Compare upstream and Go web OIDC callback behavior against one local provider.
// Each implementation gets an isolated copy of the same initialized fixture.
const fs = require('node:fs')
const path = require('node:path')
const crypto = require('node:crypto')
const { freePort, spawnLogged, waitReady, stop: stopProcess } = require('./lib/process.cjs')
const { setTimeout: delay } = require('node:timers/promises')
const { parseArgs } = require('./lib/args.cjs')
const { writeJSON } = require('./lib/artifacts.cjs')
const { login } = require('./lib/http.cjs')

const ROOT = path.resolve(__dirname, '../../..')
const PREFIX = '/audiobookshelf'
const CLIENT_ID = 'abs-oidc-differential'
const USER = { sub: 'oidc-differential-sub', email: 'oidc-differential@example.test', email_verified: true, name: 'OIDC Differential', preferred_username: 'oidc-differential' }

function validatedArgs() {
  const out = parseArgs()
  if (!out.fixture || !out['go-bin'] || !out['work-dir']) throw new Error('Usage: oidc-differential.cjs --fixture <fixture-manifest.json> --go-bin <binary> --work-dir <new-directory> [--out <report.json>] [--include-web-state-expiry true]')
  return out
}

function listen(server) { return new Promise((resolve, reject) => server.listen(0, '127.0.0.1', err => err ? reject(err) : resolve(server.address().port))) }
function b64url(value) { return Buffer.from(value).toString('base64url') }

function makeProvider() {
  const { privateKey, publicKey } = crypto.generateKeyPairSync('rsa', { modulusLength: 2048 })
  const { privateKey: untrustedPrivateKey } = crypto.generateKeyPairSync('rsa', { modulusLength: 2048 })
  const jwk = publicKey.export({ format: 'jwk' })
  const flows = new Map()
  let base
  const server = require('node:http').createServer(async (req, res) => {
    const u = new URL(req.url, base)
    if (u.pathname === '/jwks') {
      res.writeHead(200, { 'content-type': 'application/json' })
      return res.end(JSON.stringify({ keys: [{ ...jwk, kid: 'oidc-test-key', use: 'sig', alg: 'RS256' }] }))
    }
    if (u.pathname === '/token') {
      const body = new URLSearchParams(await new Promise(resolve => { let data = ''; req.setEncoding('utf8'); req.on('data', c => { data += c }); req.on('end', () => resolve(data)) }))
      const flow = flows.get(body.get('code'))
      const verifier = body.get('code_verifier') || ''
      const challenge = b64url(crypto.createHash('sha256').update(verifier).digest())
      if (!flow || challenge !== flow.challenge || body.get('redirect_uri') !== flow.redirectURI) {
        res.writeHead(400); return res.end('invalid test authorization code')
      }
      const now = Math.floor(Date.now() / 1000)
      const header = b64url(JSON.stringify({ alg: 'RS256', typ: 'JWT', kid: 'oidc-test-key' }))
      const code = body.get('code')
      const claims = b64url(JSON.stringify({
        iss: code.endsWith('-invalid-issuer') ? `${base}/wrong` : base,
        aud: code.endsWith('-invalid-audience') ? 'wrong-client' : CLIENT_ID,
        sub: USER.sub,
        nonce: code.endsWith('-invalid-nonce') ? `${flow.nonce}-wrong` : flow.nonce,
        iat: now,
        exp: code.includes('-expired') ? now - 60 : now + 3600
      }))
      const unsigned = `${header}.${claims}`
      const signer = code.endsWith('-invalid-signature') ? untrustedPrivateKey : privateKey
      const token = `${unsigned}.${crypto.sign('RSA-SHA256', Buffer.from(unsigned), signer).toString('base64url')}`
      res.writeHead(200, { 'content-type': 'application/json' })
      return res.end(JSON.stringify({ access_token: 'oidc-provider-access', token_type: 'Bearer', expires_in: 3600, id_token: token }))
    }
    if (u.pathname === '/userinfo') {
      if (req.headers.authorization !== 'Bearer oidc-provider-access') { res.writeHead(401); return res.end() }
      res.writeHead(200, { 'content-type': 'application/json' }); return res.end(JSON.stringify(USER))
    }
    res.writeHead(404); res.end()
  })
  return { server, flows, setBase(value) { base = value }, get base() { return base } }
}

async function configureDB(file, provider, mode) {
  const sqlite = require('sqlite3').verbose()
  const db = new sqlite.Database(file)
  const run = (sql, args) => new Promise((resolve, reject) => db.run(sql, args, err => err ? reject(err) : resolve()))
  const get = sql => new Promise((resolve, reject) => db.get(sql, (err, row) => err ? reject(err) : resolve(row)))
  try {
    const row = await get("SELECT value FROM settings WHERE key='server-settings'")
    if (!row) throw new Error(`server-settings row missing in ${file}`)
    const settings = JSON.parse(row.value)
    Object.assign(settings, {
      authActiveAuthMethods: ['local', 'openid'],
      authOpenIDIssuerURL: provider,
      authOpenIDAuthorizationURL: `${provider}/authorize`,
      authOpenIDTokenURL: `${provider}/token`,
      authOpenIDUserInfoURL: `${provider}/userinfo`,
      authOpenIDJwksURL: `${provider}/jwks`,
      authOpenIDLogoutURL: null,
      authOpenIDClientID: CLIENT_ID,
      authOpenIDClientSecret: 'oidc-test-secret',
      authOpenIDTokenSigningAlgorithm: 'RS256',
      authOpenIDSubfolderForRedirectURLs: PREFIX,
      authOpenIDMobileRedirectURIs: ['audiobookshelf://oauth'],
      authOpenIDAutoRegister: mode !== 'denied',
      authOpenIDMatchExistingBy: mode === 'link' ? 'email' : null,
      authOpenIDGroupClaim: '',
      authOpenIDAdvancedPermsClaim: ''
    })
    await run("UPDATE settings SET value=? WHERE key='server-settings'", [JSON.stringify(settings)])
    if (mode === 'link') await run('UPDATE users SET email=? WHERE username=?', [USER.email, 'oracle-reader'])
  } finally { await new Promise(resolve => db.close(resolve)) }
}

function startServer(kind, directory, port, logPath, goBin) {
  const command = kind === 'node'
    ? 'mise'
    : path.resolve(goBin)
  const args = kind === 'node'
    ? ['exec', 'node@22', '--', 'node', path.join(ROOT, 'dist-server/index.js'), '--config', path.join(directory, 'config'), '--metadata', path.join(directory, 'metadata'), '--port', String(port), '--host', '127.0.0.1', '--source', 'docker']
    : []
  const options = kind === 'node'
    ? { cwd: ROOT, env: { ...process.env, NODE_ENV: 'production' } }
    : { cwd: ROOT, env: { ...process.env, CONFIG_PATH: path.join(directory, 'config'), METADATA_PATH: path.join(directory, 'metadata'), HOST: '127.0.0.1', PORT: String(port), ROUTER_BASE_PATH: PREFIX, SOURCE: 'docker' } }
  return spawnLogged(command, args, options, logPath)
}

const ready = (proc, base, logPath) => waitReady(proc, base, {
  pathPrefix: PREFIX,
  logPath,
  attempts: 100,
  exitMessage: `server exited; inspect ${logPath}`,
  timeoutMessage: `readiness timeout; inspect ${logPath}`
})
const stop = proc => stopProcess(proc, 10000)

function cookieJar(headers, jar) {
  const values = headers.getSetCookie ? headers.getSetCookie() : [headers.get('set-cookie') || '']
  for (const value of values) {
    const first = value.split(';', 1)[0]
    const at = first.indexOf('=')
    if (at > 0) jar.set(first.slice(0, at), first.slice(at + 1))
  }
}

function tokenClaimsSummary(value, expectedNonce) {
  if (!value) return null
  const parts = value.split('.')
  if (parts.length !== 3) return { validJWTShape: false }
  try {
    const header = JSON.parse(Buffer.from(parts[0], 'base64url').toString('utf8'))
    const claims = JSON.parse(Buffer.from(parts[1], 'base64url').toString('utf8'))
    for (const key of ['iat', 'exp', 'nbf', 'auth_time']) {
      if (typeof claims[key] === 'number') claims[key] = '<dynamic-timestamp>'
    }
    const uuid = /^[0-9a-f]{8}-[0-9a-f]{4}-[1-8][0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/i
    for (const key of ['id', 'userId', 'sessionId', 'sid', 'jti']) {
      if (typeof claims[key] === 'string' && uuid.test(claims[key])) claims[key] = '<fixture-id>'
    }
    if (expectedNonce !== undefined && Object.hasOwn(claims, 'nonce')) {
      claims.nonceMatchesAuthorization = claims.nonce === expectedNonce
      delete claims.nonce
    }
    return { validJWTShape: true, header, claims }
  } catch {
    return { validJWTShape: false }
  }
}

function cookieSummary(headers, expectedOIDCNonce) {
  const values = headers.getSetCookie ? headers.getSetCookie() : [headers.get('set-cookie') || '']
  const stableValues = new Set(['auth_cb', 'auth_method', 'auth_state'])
  return values.filter(Boolean).map(value => {
    const [pair, ...parts] = value.split(';')
    const separator = pair.indexOf('=')
    const name = separator < 0 ? pair.trim() : pair.slice(0, separator).trim()
    const cookieValue = separator < 0 ? '' : pair.slice(separator + 1).trim()
    const attributes = parts.map(part => {
      const attr = part.trim()
      const equals = attr.indexOf('=')
      const key = (equals < 0 ? attr : attr.slice(0, equals)).trim().toLowerCase()
      const attrValue = equals < 0 ? null : attr.slice(equals + 1).trim()
      const isDynamicExpiry = key === 'expires' && Date.parse(attrValue) > Date.UTC(2000, 0, 1)
      return { key, value: isDynamicExpiry ? '<dynamic-date>' : attrValue }
    })
    let valueSummary
    if (stableValues.has(name)) valueSummary = cookieValue
    else if (name === 'connect.sid') valueSummary = cookieValue ? '<fixture-session-id>' : ''
    else if (name === 'openid_id_token') valueSummary = tokenClaimsSummary(decodeURIComponent(cookieValue), expectedOIDCNonce)
    else if (name === 'refresh_token') valueSummary = tokenClaimsSummary(decodeURIComponent(cookieValue))
    else valueSummary = cookieValue
    return { name, value: valueSummary, attributes }
  })
}

async function get(url, jar) {
  const response = await fetch(url, { redirect: 'manual', headers: jar.size ? { cookie: [...jar].map(([k, v]) => `${k}=${v}`).join('; ') } : {}, signal: AbortSignal.timeout(20000) })
  cookieJar(response.headers, jar)
  return response
}

async function post(url, jar) {
  const response = await fetch(url, {
    method: 'POST', redirect: 'manual',
    headers: jar.size ? { cookie: [...jar].map(([k, v]) => `${k}=${v}`).join('; ') } : {},
    signal: AbortSignal.timeout(20000)
  })
  cookieJar(response.headers, jar)
  return response
}

async function loginToken(base, credentials) {
  const response = await login(base, { username: credentials.username, password: credentials.password }, {
    pathPrefix: PREFIX,
    timeoutMs: 15000,
    tokenSelector: body => body?.user?.accessToken || body?.accessToken,
    errorMessage: status => `fixture user login failed (${status})`
  })
  return response.token
}

async function openSocket(base, token) {
  const { io } = require('socket.io-client')
  const socket = io(base, { path: `${PREFIX}/socket.io`, timeout: 8000, reconnection: false, transports: ['polling', 'websocket'] })
  const connected = new Promise((resolve, reject) => {
    const timer = setTimeout(() => reject(new Error('Socket.IO init timeout')), 10000)
    socket.once('connect', () => socket.emit('auth', token))
    socket.once('init', () => { clearTimeout(timer); resolve() })
    socket.once('connect_error', error => { clearTimeout(timer); reject(error) })
  })
  try { await connected } catch (error) { socket.disconnect(); throw error }
  return socket
}

function normalizeAddedUser(value) {
  if (Array.isArray(value)) return value.map(normalizeAddedUser)
  if (!value || typeof value !== 'object') return value
  return Object.fromEntries(Object.entries(value).map(([key, item]) => {
    if (key === 'id') return [key, '<new-user-id>']
    if (key === 'token') return [key, '<new-user-token>']
    if (key === 'createdAt') return [key, '<dynamic-time>']
    return [key, normalizeAddedUser(item)]
  }))
}

function canonicalJSON(value) {
  if (Array.isArray(value)) return value.map(canonicalJSON)
  if (!value || typeof value !== 'object') return value
  return Object.fromEntries(Object.entries(value).sort(([a], [b]) => a.localeCompare(b)).map(([key, item]) => [key, canonicalJSON(item)]))
}

function resolvePath(source, expression) {
  return expression.split('.').reduce((value, key) => value?.[key], source)
}

async function watchUserAdded(base, fixture) {
  const rootToken = await loginToken(base, fixture.rootCredentials)
  const readerToken = await loginToken(base, fixture.readerCredentials)
  const [admin, reader] = await Promise.all([openSocket(base, rootToken), openSocket(base, readerToken)])
  const adminEvents = [], readerEvents = []
  admin.on('user_added', payload => adminEvents.push(normalizeAddedUser(payload)))
  reader.on('user_added', payload => readerEvents.push(normalizeAddedUser(payload)))
  return {
    result: async () => { await delay(500); return { adminEvents, readerEventCount: readerEvents.length } },
    close: () => { admin.disconnect(); reader.disconnect() }
  }
}

function redirectSummary(response, base) {
  const location = response.headers.get('location') || ''
  const target = location ? new URL(location, base) : null
  return { status: response.status, location, redirectPath: target?.pathname || null, redirectError: target?.searchParams.get('error') || null }
}

async function rejectedTokenFlow(kind, base, dir, jar, provider, flavor) {
  const start = await get(`${base}${PREFIX}/auth/openid?callback=${encodeURIComponent(`${PREFIX}/login`)}`, jar)
  if (start.status !== 302) throw new Error(`${kind} ${flavor} OIDC start returned ${start.status}`)
  const authorization = new URL(start.headers.get('location'))
  const code = `code-${kind}-${flavor}`
  provider.flows.set(code, { nonce: authorization.searchParams.get('nonce'), challenge: authorization.searchParams.get('code_challenge'), redirectURI: authorization.searchParams.get('redirect_uri') })
  const callback = new URL(`${base}${PREFIX}/auth/openid/callback`)
  callback.searchParams.set('code', code); callback.searchParams.set('state', authorization.searchParams.get('state'))
  const response = await get(callback, jar)
  return { ...redirectSummary(response, base), persisted: await snapshotDB(path.join(dir, 'config', 'absdatabase.sqlite')) }
}

async function snapshotDB(file) {
  const sqlite = require('sqlite3').verbose(); const db = new sqlite.Database(file)
  const all = (sql, args = []) => new Promise((resolve, reject) => db.all(sql, args, (err, rows) => err ? reject(err) : resolve(rows)))
  try {
    const subject = `%${USER.sub}%`
    const users = await all('SELECT username,email,type,isActive,extraData FROM users WHERE extraData LIKE ? ORDER BY username', [subject])
    const sessions = await all('SELECT COUNT(*) AS count FROM sessions WHERE userId IN (SELECT id FROM users WHERE extraData LIKE ?)', [subject])
    return { users, sessionCount: sessions[0].count }
  } finally { await new Promise(resolve => db.close(resolve)) }
}

async function runFlow(kind, dir, goBin, provider, work, fixture, mode = 'success') {
  const port = await freePort(), base = `http://127.0.0.1:${port}`, logPath = path.join(work, `${kind}.log`)
  await configureDB(path.join(dir, 'config', 'absdatabase.sqlite'), provider.base, mode)
  const proc = startServer(kind, dir, port, logPath, goBin)
  try {
    await ready(proc, base, logPath)
    const jar = new Map()
    const missingWebSession = redirectSummary(await get(`${base}${PREFIX}/auth/openid/callback?state=missing-state&code_verifier=`, new Map()), base)
    const duplicateEmptyVerifierQuery = new URLSearchParams([['state', 'missing-state'], ['code_verifier', ''], ['code_verifier', '']])
    const missingMobileSession = await get(`${base}${PREFIX}/auth/openid/callback?${duplicateEmptyVerifierQuery}`, new Map())
    const missingMobileSessionSummary = {
      status: missingMobileSession.status,
      contentType: missingMobileSession.headers.get('content-type'),
      contentLength: missingMobileSession.headers.get('content-length'),
      etag: missingMobileSession.headers.get('etag'),
      body: await missingMobileSession.text()
    }
    const socketWatch = mode === 'success' ? await watchUserAdded(base, fixture) : null
    const start = await get(`${base}${PREFIX}/auth/openid?callback=${encodeURIComponent(`${PREFIX}/login`)}`, jar)
    if (start.status !== 302) throw new Error(`${kind} OIDC start returned ${start.status}: ${await start.text()}`)
    const startCookies = cookieSummary(start.headers)
    const authorization = new URL(start.headers.get('location'))
    const code = `code-${kind}`
    provider.flows.set(code, { nonce: authorization.searchParams.get('nonce'), challenge: authorization.searchParams.get('code_challenge'), redirectURI: authorization.searchParams.get('redirect_uri') })
    const callback = new URL(`${base}${PREFIX}/auth/openid/callback`)
    callback.searchParams.set('code', code); callback.searchParams.set('state', authorization.searchParams.get('state'))
    const result = await get(callback, jar)
    const location = result.headers.get('location') || ''
    const parsedLocation = location ? new URL(location, base) : null
    const summary = {
      missingWebSession,
      missingMobileSession: missingMobileSessionSummary,
      status: result.status,
      redirectPath: parsedLocation?.pathname || null,
      redirectError: parsedLocation?.searchParams.get('error') || null,
      hasSetToken: Boolean(parsedLocation?.searchParams.get('setToken')),
      hasAccessToken: Boolean(parsedLocation?.searchParams.get('accessToken')),
      setTokenClaims: tokenClaimsSummary(parsedLocation?.searchParams.get('setToken')),
      accessTokenClaims: tokenClaimsSummary(parsedLocation?.searchParams.get('accessToken')),
      userState: parsedLocation?.searchParams.get('state') || null,
      startCookies,
      callbackCookies: cookieSummary(result.headers, provider.flows.get(code)?.nonce),
      persisted: await snapshotDB(path.join(dir, 'config', 'absdatabase.sqlite')),
      realtime: socketWatch ? await socketWatch.result() : undefined
    }
    if (socketWatch) socketWatch.close()
    if (mode === 'denied') return { denied: summary }
    if (mode === 'link') return { linked: summary }
    const startErrorFlow = await get(`${base}${PREFIX}/auth/openid?callback=${encodeURIComponent(`${PREFIX}/login`)}`, jar)
    if (startErrorFlow.status !== 302) throw new Error(`${kind} error-flow OIDC start returned ${startErrorFlow.status}`)
    const errorAuthorization = new URL(startErrorFlow.headers.get('location'))
    const errorCallback = new URL(`${base}${PREFIX}/auth/openid/callback`)
    errorCallback.searchParams.set('error', 'access_denied')
    errorCallback.searchParams.set('state', errorAuthorization.searchParams.get('state'))
    const providerError = await get(errorCallback, jar)
    const providerErrorSummary = { ...redirectSummary(providerError, base), persisted: await snapshotDB(path.join(dir, 'config', 'absdatabase.sqlite')) }
    const replay = await get(errorCallback, jar)
    const replaySummary = { ...redirectSummary(replay, base), persisted: await snapshotDB(path.join(dir, 'config', 'absdatabase.sqlite')) }
    const expiredStart = await get(`${base}${PREFIX}/auth/openid?callback=${encodeURIComponent(`${PREFIX}/login`)}`, jar)
    const expiredAuthorization = new URL(expiredStart.headers.get('location'))
    const expiredCode = `code-${kind}-expired`
    provider.flows.set(expiredCode, { nonce: expiredAuthorization.searchParams.get('nonce'), challenge: expiredAuthorization.searchParams.get('code_challenge'), redirectURI: expiredAuthorization.searchParams.get('redirect_uri') })
    const expiredCallback = new URL(`${base}${PREFIX}/auth/openid/callback`)
    expiredCallback.searchParams.set('code', expiredCode); expiredCallback.searchParams.set('state', expiredAuthorization.searchParams.get('state'))
    const expiredResponse = await get(expiredCallback, jar)
    const expiredSummary = { ...redirectSummary(expiredResponse, base), persisted: await snapshotDB(path.join(dir, 'config', 'absdatabase.sqlite')) }
    const invalidNonce = await rejectedTokenFlow(kind, base, dir, jar, provider, 'invalid-nonce')
    const invalidIssuer = await rejectedTokenFlow(kind, base, dir, jar, provider, 'invalid-issuer')
    const invalidAudience = await rejectedTokenFlow(kind, base, dir, jar, provider, 'invalid-audience')
    const invalidSignature = await rejectedTokenFlow(kind, base, dir, jar, provider, 'invalid-signature')
    const logoutBefore = await snapshotDB(path.join(dir, 'config', 'absdatabase.sqlite'))
    const logoutResponse = await post(`${base}${PREFIX}/logout`, jar)
    const logoutSummary = {
      status: logoutResponse.status,
      contentType: logoutResponse.headers.get('content-type'),
      contentLength: logoutResponse.headers.get('content-length'),
      etag: logoutResponse.headers.get('etag'),
      body: await logoutResponse.text(),
      cookies: cookieSummary(logoutResponse.headers),
      persistedBefore: logoutBefore,
      persistedAfter: await snapshotDB(path.join(dir, 'config', 'absdatabase.sqlite'))
    }
    return { success: summary, providerError: providerErrorSummary, replay: replaySummary, expiredToken: expiredSummary, invalidNonce, invalidIssuer, invalidAudience, invalidSignature, logout: logoutSummary }
  } finally { await stop(proc) }
}

async function runWebStateExpiry(kind, dir, goBin, provider, work) {
  const port = await freePort(), base = `http://127.0.0.1:${port}`, logPath = path.join(work, `web-state-expiry-${kind}.log`)
  await configureDB(path.join(dir, 'config', 'absdatabase.sqlite'), provider.base, 'success')
  const proc = startServer(kind, dir, port, logPath, goBin)
  try {
    await ready(proc, base, logPath)
    const jar = new Map()
    const start = await get(`${base}${PREFIX}/auth/openid?callback=${encodeURIComponent(`${PREFIX}/login`)}`, jar)
    if (start.status !== 302) throw new Error(`${kind} OIDC expiry start returned ${start.status}`)
    const authorization = new URL(start.headers.get('location'))
    const state = authorization.searchParams.get('state')
    const code = `code-${kind}-web-state-expiry`
    provider.flows.set(code, { nonce: authorization.searchParams.get('nonce'), challenge: authorization.searchParams.get('code_challenge'), redirectURI: authorization.searchParams.get('redirect_uri') })
    const mobileJar = new Map()
    const mobileState = `mobile-expiry-${kind}`
    const mobileVerifier = `mobile-expiry-verifier-${kind}`
    const mobileQuery = new URLSearchParams({
      response_type: 'code', redirect_uri: 'audiobookshelf://oauth', state: mobileState,
      code_challenge: b64url(crypto.createHash('sha256').update(mobileVerifier).digest()),
      code_challenge_method: 'S256'
    })
    const mobileStart = await get(`${base}${PREFIX}/auth/openid?${mobileQuery}`, mobileJar)
    if (mobileStart.status !== 302) throw new Error(`${kind} mobile OIDC expiry start returned ${mobileStart.status}`)
    return {
      finish: async () => {
        const callback = new URL(`${base}${PREFIX}/auth/openid/callback`)
        callback.searchParams.set('code', code)
        callback.searchParams.set('state', state)
        const response = await get(callback, jar)
        const mobileCallback = await get(`${base}${PREFIX}/auth/openid/mobile-redirect?${new URLSearchParams({ code: 'expired-mobile-code', state: mobileState })}`, mobileJar)
        const location = response.headers.get('location') || ''
        const target = location ? new URL(location, base) : null
        return {
          web: {
            startStatus: start.status,
            callback: {
              status: response.status,
              redirectPath: target?.pathname || null,
              redirectError: target?.searchParams.get('error') || null,
              hasSetToken: Boolean(target?.searchParams.get('setToken')),
              hasAccessToken: Boolean(target?.searchParams.get('accessToken')),
              setTokenClaims: tokenClaimsSummary(target?.searchParams.get('setToken')),
              accessTokenClaims: tokenClaimsSummary(target?.searchParams.get('accessToken'))
            },
            callbackCookies: cookieSummary(response.headers, provider.flows.get(code)?.nonce),
            persisted: await snapshotDB(path.join(dir, 'config', 'absdatabase.sqlite'))
          },
          mobile: {
            startStatus: mobileStart.status,
            redirect: {
              status: mobileCallback.status,
              contentType: mobileCallback.headers.get('content-type'),
              contentLength: mobileCallback.headers.get('content-length'),
              etag: mobileCallback.headers.get('etag'),
              body: await mobileCallback.text()
            }
          }
        }
      },
      stop: () => stop(proc)
    }
  } catch (error) {
    await stop(proc)
    throw error
  }
}

async function runWebStateExpiryPair(nodeDir, goDir, goBin, provider, work) {
  const results = await Promise.allSettled([
    runWebStateExpiry('node', nodeDir, goBin, provider, work),
    runWebStateExpiry('go', goDir, goBin, provider, work)
  ])
  const sides = results.filter(result => result.status === 'fulfilled').map(result => result.value)
  const failure = results.find(result => result.status === 'rejected')
  if (failure) {
    await Promise.all(sides.map(side => side.stop()))
    throw failure.reason
  }
  try {
    // Upstream Express MemoryStore retains this session for 24 hours. Waiting
    // past the Go flow-map TTL makes the public callback comparison decisive.
    await delay(10 * 60 * 1000 + 1500)
    const [node, go] = await Promise.all(sides.map(side => side.finish()))
    return { node, go }
  } finally {
    await Promise.all(sides.map(side => side.stop()))
  }
}

async function runMobileRedirect(kind, dir, goBin, provider, work) {
  const port = await freePort(), base = `http://127.0.0.1:${port}`, logPath = path.join(work, `mobile-${kind}.log`)
  await configureDB(path.join(dir, 'config', 'absdatabase.sqlite'), provider.base, 'mobile')
  const proc = startServer(kind, dir, port, logPath, goBin)
  try {
    await ready(proc, base, logPath)
    const jar = new Map()
    const state = 'oidc-mobile-state-fixed'
    const code = 'oidc-mobile-code-fixed'
    const verifier = 'mobile-test-verifier'
    const challenge = b64url(crypto.createHash('sha256').update(verifier).digest())
    const appURI = 'audiobookshelf://oauth'
    async function errorSummary(query) {
      const response = await get(`${base}${PREFIX}/auth/openid?${query}`, jar)
      return {
        status: response.status,
        contentType: response.headers.get('content-type'),
        contentLength: response.headers.get('content-length'),
        etag: response.headers.get('etag'),
        body: await response.text()
      }
    }
    const badRedirect = await errorSummary(new URLSearchParams({ response_type: 'code', redirect_uri: 'audiobookshelf://evil', code_challenge: 'test-challenge' }))
    const missingChallenge = await errorSummary(new URLSearchParams({ response_type: 'code', redirect_uri: appURI }))
    const badChallengeMethod = await errorSummary(new URLSearchParams({ response_type: 'code', redirect_uri: appURI, code_challenge: 'test-challenge', code_challenge_method: 'plain' }))
    const query = new URLSearchParams({
      response_type: 'code', redirect_uri: appURI, state,
      code_challenge: challenge, code_challenge_method: 'S256'
    })
    const start = await get(`${base}${PREFIX}/auth/openid?${query}`, jar)
    const providerLocation = start.headers.get('location') || ''
    const startBody = await start.text()
    const providerURL = providerLocation ? new URL(providerLocation) : null
    provider.flows.set(code, {
      nonce: providerURL?.searchParams.get('nonce'),
      challenge: providerURL?.searchParams.get('code_challenge'),
      redirectURI: providerURL?.searchParams.get('redirect_uri')
    })
    const startSummary = {
      status: start.status,
      contentType: start.headers.get('content-type'),
      etag: start.headers.get('etag'),
      normalizedContentLength: Number(start.headers.get('content-length')) - Buffer.byteLength(providerLocation) + Buffer.byteLength('<provider-url>'),
      redirectBody: startBody.replace(providerLocation, '<provider-url>'),
      cookies: cookieSummary(start.headers),
      providerPath: providerURL?.pathname || null,
      providerRedirectPath: providerURL ? new URL(providerURL.searchParams.get('redirect_uri')).pathname : null,
      state: providerURL?.searchParams.get('state') || null,
      responseType: providerURL?.searchParams.get('response_type') || null,
      challenge: providerURL?.searchParams.get('code_challenge') || null,
      challengeMethod: providerURL?.searchParams.get('code_challenge_method') || null
    }
    const callbackURL = `${base}${PREFIX}/auth/openid/mobile-redirect?${new URLSearchParams({ code, state })}`
    const callback = await get(callbackURL, jar)
    const callbackSummary = {
      status: callback.status,
      location: callback.headers.get('location') || null,
      contentType: callback.headers.get('content-type'),
      contentLength: callback.headers.get('content-length'),
      etag: callback.headers.get('etag'),
      body: await callback.text()
    }
    const replay = await get(callbackURL, jar)
    const replaySummary = {
      status: replay.status,
      contentType: replay.headers.get('content-type'),
      contentLength: replay.headers.get('content-length'),
      etag: replay.headers.get('etag'),
      body: await replay.text()
    }
    const exchangeURL = new URL(`${base}${PREFIX}/auth/openid/callback`)
    exchangeURL.searchParams.set('code', code)
    exchangeURL.searchParams.set('state', state)
    exchangeURL.searchParams.set('code_verifier', verifier)
    const exchange = await get(exchangeURL, jar)
    const exchangeBody = await exchange.json().catch(() => null)
    const exchangePayload = exchangeBody && typeof exchangeBody === 'object' ? structuredClone(exchangeBody) : exchangeBody
    if (exchangePayload?.user) {
      const tokenSummaries = Object.fromEntries(['token', 'accessToken', 'refreshToken']
        .filter(key => typeof exchangePayload.user[key] === 'string')
        .map(key => [key, tokenClaimsSummary(exchangePayload.user[key])]))
      Object.assign(exchangePayload.user, normalizeAddedUser(exchangePayload.user), tokenSummaries)
    }
    if (exchangePayload?.serverSettings?.backupPath) exchangePayload.serverSettings.backupPath = '<fixture-path>'
    const exchangeSummary = {
      status: exchange.status,
      contentType: exchange.headers.get('content-type'),
      payload: exchangePayload,
      persisted: await snapshotDB(path.join(dir, 'config', 'absdatabase.sqlite'))
    }
    const exchangeReplay = await get(exchangeURL, jar)
    const exchangeReplaySummary = {
      status: exchangeReplay.status,
      contentType: exchangeReplay.headers.get('content-type'),
      location: exchangeReplay.headers.get('location') || null,
      body: await exchangeReplay.text()
    }

    return { badRedirect, missingChallenge, badChallengeMethod, start: startSummary, callback: callbackSummary, replay: replaySummary, exchange: exchangeSummary, exchangeReplay: exchangeReplaySummary }
  } finally { await stop(proc) }
}

async function runMobileInvalidVerifier(kind, dir, goBin, provider, work) {
  const port = await freePort(), base = `http://127.0.0.1:${port}`, logPath = path.join(work, `mobile-invalid-verifier-${kind}.log`)
  await configureDB(path.join(dir, 'config', 'absdatabase.sqlite'), provider.base, 'mobile')
  const proc = startServer(kind, dir, port, logPath, goBin)
  try {
    await ready(proc, base, logPath)
    const jar = new Map()
    const state = 'oidc-mobile-invalid-verifier-state'
    const code = 'oidc-mobile-invalid-verifier-code'
    const expectedVerifier = 'mobile-invalid-verifier-expected'
    const suppliedVerifier = 'mobile-invalid-verifier-supplied'
    const query = new URLSearchParams({
      response_type: 'code', redirect_uri: 'audiobookshelf://oauth', state,
      code_challenge: b64url(crypto.createHash('sha256').update(expectedVerifier).digest()),
      code_challenge_method: 'S256'
    })
    const start = await get(`${base}${PREFIX}/auth/openid?${query}`, jar)
    if (start.status !== 302) throw new Error(`${kind} invalid-verifier OIDC start returned ${start.status}`)
    const authorization = new URL(start.headers.get('location'))
    provider.flows.set(code, {
      nonce: authorization.searchParams.get('nonce'),
      challenge: authorization.searchParams.get('code_challenge'),
      redirectURI: authorization.searchParams.get('redirect_uri')
    })
    const callback = await get(`${base}${PREFIX}/auth/openid/mobile-redirect?${new URLSearchParams({ code, state })}`, jar)
    const callbackSummary = {
      status: callback.status,
      location: callback.headers.get('location') || null,
      contentType: callback.headers.get('content-type'),
      body: await callback.text()
    }
    const exchangeURL = new URL(`${base}${PREFIX}/auth/openid/callback`)
    exchangeURL.searchParams.set('code', code)
    exchangeURL.searchParams.set('state', state)
    exchangeURL.searchParams.set('code_verifier', suppliedVerifier)
    const persistedBefore = await snapshotDB(path.join(dir, 'config', 'absdatabase.sqlite'))
    const exchange = await get(exchangeURL, jar)
    const exchangeSummary = {
      status: exchange.status,
      contentType: exchange.headers.get('content-type'),
      contentLength: exchange.headers.get('content-length'),
      etag: exchange.headers.get('etag'),
      body: await exchange.text(),
      persistedBefore,
      persistedAfter: await snapshotDB(path.join(dir, 'config', 'absdatabase.sqlite'))
    }
    const replay = await get(exchangeURL, jar)
    const replaySummary = {
      status: replay.status,
      contentType: replay.headers.get('content-type'),
      contentLength: replay.headers.get('content-length'),
      etag: replay.headers.get('etag'),
      location: replay.headers.get('location') || null,
      body: await replay.text(),
      persisted: await snapshotDB(path.join(dir, 'config', 'absdatabase.sqlite'))
    }
    return { startStatus: start.status, callback: callbackSummary, exchange: exchangeSummary, replay: replaySummary }
  } finally { await stop(proc) }
}

async function main() {
  const args = validatedArgs(), fixture = JSON.parse(fs.readFileSync(args.fixture, 'utf8'))
  const work = path.resolve(args['work-dir'])
  if (fs.existsSync(work) && fs.readdirSync(work).length) throw new Error(`work directory must be empty: ${work}`)
  fs.mkdirSync(work, { recursive: true })
  const scenarios = {}
  for (const scenario of ['success', 'link', 'denied', 'mobile', 'mobile-invalid-verifier']) {
    scenarios[scenario] = {}
    for (const kind of ['node', 'go']) {
      const dir = path.join(work, scenario, kind)
      fs.mkdirSync(path.dirname(dir), { recursive: true })
      fs.cpSync(fixture.directories[kind], dir, { recursive: true, preserveTimestamps: true })
      scenarios[scenario][kind] = dir
    }
  }
  if (args['include-web-state-expiry'] === 'true') {
    const scenario = 'web-state-expiry'
    scenarios[scenario] = {}
    for (const kind of ['node', 'go']) {
      const dir = path.join(work, scenario, kind)
      fs.mkdirSync(path.dirname(dir), { recursive: true })
      fs.cpSync(fixture.directories[kind], dir, { recursive: true, preserveTimestamps: true })
      scenarios[scenario][kind] = dir
    }
  }
  const provider = makeProvider(), providerPort = await listen(provider.server)
  provider.setBase(`http://127.0.0.1:${providerPort}`)
  let result
  try {
    const stepContext = {}
    const stepRuntime = { scenarios, args, fixture, provider, work, runFlow, runMobileRedirect, runMobileInvalidVerifier, runWebStateExpiryPair }
    const oidcSteps = JSON.parse(fs.readFileSync(path.join(__dirname, 'cases/oidc-steps.json'), 'utf8'))
    for (const step of oidcSteps) {
      if (step.when && args['include-web-state-expiry'] !== 'true') continue
      const runStep = require(path.resolve(__dirname, 'cases', step.module))
      const output = await runStep({ step, runtime: stepRuntime })
      for (const [source, target] of Object.entries(step.as)) stepContext[target] = output[source]
    }
    const caseContext = stepContext
    const cases = JSON.parse(fs.readFileSync(path.join(__dirname, 'cases/oidc.json'), 'utf8'))
      .filter(test => !test.when || (test.when === 'web-state-expiry' && caseContext.webStateExpiry))
      .map(test => ({ name: test.id, node: resolvePath(caseContext, test.node), go: resolvePath(caseContext, test.go) }))
    cases.forEach(test => { test.equal = JSON.stringify(canonicalJSON(test.node)) === JSON.stringify(canonicalJSON(test.go)) })
    const matched = cases.filter(test => test.equal).length
    result = { baseline: fixture.baseline, provider: 'local ephemeral RS256 provider', compared: cases.length, matched, mismatched: cases.length - matched, cases, limits: ['Web callback without session and empty code_verifier, duplicate empty code_verifier mobile classification, auto-registration success, provider error, callback state replay, expired ID token, email linking, auto-registration denial, invalid nonce/issuer/audience/signature, selected admin-only user_added event, mobile redirect allowlist/PKCE errors, valid mobile token exchange, consumed-flow callback replay, mobile redirect state replay and expiry after 10 minutes, invalid mobile PKCE verifier failure plus replay, web callback after 10 minutes, and selected web OIDC logout cookie/session effects are exercised.', 'Selected web/mobile start, successful callback and web logout cookies are compared by name/stable value/Max-Age/Path/Expires/HttpOnly/Secure/SameSite and signed connect.sid. JWT algorithms/claims are compared, normalizing only UUID identifiers and timestamps; each ID-token nonce is checked against its own authorization request. The exact 24-hour session-expiry boundary and rolling session-touch renewal remain unverified. Logout errors, mobile logout behavior, other callback variants, and general Socket.IO inventory remain unverified.', 'Persisted user fields and session counts are compared; only fixture-specific backup paths, UUID identifiers, and timestamps are normalized. JSON object member order is canonicalized only for Socket.IO event payloads; arrays, scalar values, null/empty distinctions, and raw persisted extraData text remain strict.'] }
  } finally {
    provider.server.closeIdleConnections?.()
    provider.server.closeAllConnections?.()
    await new Promise(resolve => provider.server.close(resolve))
  }
  const output = args.out ? path.resolve(args.out) : path.join(work, 'oidc-report.json')
  writeJSON(output, result, { ensureDirectory: true })
  process.stdout.write(`${JSON.stringify({ ...result, report: output }, null, 2)}\n`)
  if (result.mismatched) process.exitCode = 1
}

main().catch(error => { console.error(error.stack || error); process.exitCode = 1 })
