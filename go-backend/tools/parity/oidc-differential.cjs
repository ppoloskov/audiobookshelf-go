#!/usr/bin/env node
'use strict'

// Compare upstream and Go web OIDC callback behavior against one local provider.
// Each implementation gets an isolated copy of the same initialized fixture.
const fs = require('node:fs')
const path = require('node:path')
const net = require('node:net')
const crypto = require('node:crypto')
const { spawn } = require('node:child_process')
const { setTimeout: delay } = require('node:timers/promises')

const ROOT = path.resolve(__dirname, '../../..')
const PREFIX = '/audiobookshelf'
const CLIENT_ID = 'abs-oidc-differential'
const USER = { sub: 'oidc-differential-sub', email: 'oidc-differential@example.test', email_verified: true, name: 'OIDC Differential', preferred_username: 'oidc-differential' }

function parseArgs() {
  const out = {}
  for (let i = 2; i < process.argv.length; i++) if (process.argv[i].startsWith('--')) out[process.argv[i].slice(2)] = process.argv[++i]
  if (!out.fixture || !out['go-bin'] || !out['work-dir']) throw new Error('Usage: oidc-differential.cjs --fixture <fixture-manifest.json> --go-bin <binary> --work-dir <new-directory> [--out <report.json>]')
  return out
}

async function freePort() {
  const server = net.createServer()
  await new Promise((resolve, reject) => server.listen(0, '127.0.0.1', err => err ? reject(err) : resolve()))
  const value = server.address().port
  await new Promise((resolve, reject) => server.close(err => err ? reject(err) : resolve()))
  return value
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
  const child = kind === 'node'
    ? spawn('mise', ['exec', 'node@22', '--', 'node', path.join(ROOT, 'dist-server/index.js'), '--config', path.join(directory, 'config'), '--metadata', path.join(directory, 'metadata'), '--port', String(port), '--host', '127.0.0.1', '--source', 'docker'], { cwd: ROOT, env: { ...process.env, NODE_ENV: 'production' }, stdio: ['ignore', 'pipe', 'pipe'] })
    : spawn(path.resolve(goBin), [], { cwd: ROOT, env: { ...process.env, CONFIG_PATH: path.join(directory, 'config'), METADATA_PATH: path.join(directory, 'metadata'), HOST: '127.0.0.1', PORT: String(port), ROUTER_BASE_PATH: PREFIX, SOURCE: 'docker' }, stdio: ['ignore', 'pipe', 'pipe'] })
  const log = fs.createWriteStream(logPath); child.stdout.pipe(log); child.stderr.pipe(log)
  return { child, log }
}

async function ready(proc, base, logPath) {
  for (let i = 0; i < 100; i++) {
    if (proc.child.exitCode !== null) throw new Error(`server exited; inspect ${logPath}`)
    try { if ((await fetch(`${base}${PREFIX}/ping`)).ok) return } catch {}
    await delay(250)
  }
  throw new Error(`readiness timeout; inspect ${logPath}`)
}

async function stop(proc) {
  if (!proc) return
  if (proc.child.exitCode === null) {
    proc.child.kill('SIGINT')
    await Promise.race([new Promise(resolve => proc.child.once('exit', resolve)), delay(10000).then(() => proc.child.kill('SIGKILL'))])
  }
  proc.log.end()
}

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
      return { key, value: key === 'expires' ? '<dynamic-date>' : attrValue }
    })
    let valueSummary
    if (stableValues.has(name)) valueSummary = cookieValue
    else if (name === 'connect.sid') valueSummary = '<fixture-session-id>'
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

async function loginToken(base, credentials) {
  const response = await fetch(`${base}${PREFIX}/login`, {
    method: 'POST',
    headers: { 'content-type': 'application/json', 'x-return-tokens': 'true' },
    body: JSON.stringify({ username: credentials.username, password: credentials.password }),
    signal: AbortSignal.timeout(15000)
  })
  const body = await response.json()
  const token = body.user?.accessToken || body.accessToken
  if (!response.ok || !token) throw new Error(`fixture user login failed (${response.status})`)
  return token
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
    return { success: summary, providerError: providerErrorSummary, replay: replaySummary, expiredToken: expiredSummary, invalidNonce, invalidIssuer, invalidAudience, invalidSignature }
  } finally { await stop(proc) }
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

async function main() {
  const args = parseArgs(), fixture = JSON.parse(fs.readFileSync(args.fixture, 'utf8'))
  const work = path.resolve(args['work-dir'])
  if (fs.existsSync(work) && fs.readdirSync(work).length) throw new Error(`work directory must be empty: ${work}`)
  fs.mkdirSync(work, { recursive: true })
  const scenarios = {}
  for (const scenario of ['success', 'link', 'denied', 'mobile']) {
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
    const node = await runFlow('node', scenarios.success.node, args['go-bin'], provider, work, fixture)
    const go = await runFlow('go', scenarios.success.go, args['go-bin'], provider, work, fixture)
    const nodeLink = await runFlow('node', scenarios.link.node, args['go-bin'], provider, work, fixture, 'link')
    const goLink = await runFlow('go', scenarios.link.go, args['go-bin'], provider, work, fixture, 'link')
    const nodeDenied = await runFlow('node', scenarios.denied.node, args['go-bin'], provider, work, fixture, 'denied')
    const goDenied = await runFlow('go', scenarios.denied.go, args['go-bin'], provider, work, fixture, 'denied')
    const nodeMobile = await runMobileRedirect('node', scenarios.mobile.node, args['go-bin'], provider, work)
    const goMobile = await runMobileRedirect('go', scenarios.mobile.go, args['go-bin'], provider, work)
    const mobileStart = { name: 'oidc-mobile-start', node: nodeMobile.start, go: goMobile.start }
    const mobileCallback = { name: 'oidc-mobile-redirect', node: nodeMobile.callback, go: goMobile.callback }
    const mobileReplay = { name: 'oidc-mobile-state-replay', node: nodeMobile.replay, go: goMobile.replay }
    const mobileExchange = { name: 'oidc-mobile-token-exchange', node: nodeMobile.exchange, go: goMobile.exchange }
    const mobileExchangeReplay = { name: 'oidc-mobile-token-exchange-replay', node: nodeMobile.exchangeReplay, go: goMobile.exchangeReplay }
    const mobileCases = [
      { name: 'oidc-mobile-invalid-redirect', node: nodeMobile.badRedirect, go: goMobile.badRedirect },
      { name: 'oidc-mobile-missing-pkce-challenge', node: nodeMobile.missingChallenge, go: goMobile.missingChallenge },
      { name: 'oidc-mobile-unsupported-pkce-method', node: nodeMobile.badChallengeMethod, go: goMobile.badChallengeMethod },
      mobileStart, mobileCallback, mobileReplay, mobileExchange, mobileExchangeReplay
    ]
    const cases = [{ name: 'oidc-web-callback-no-session-empty-verifier', node: node.success.missingWebSession, go: go.success.missingWebSession }, { name: 'oidc-mobile-callback-no-session-duplicate-empty-verifier', node: node.success.missingMobileSession, go: go.success.missingMobileSession }, { name: 'oidc-web-auto-register-success', node: node.success, go: go.success }, { name: 'oidc-provider-error', node: node.providerError, go: go.providerError }, { name: 'oidc-state-replay-after-provider-error', node: node.replay, go: go.replay }, { name: 'oidc-expired-id-token', node: node.expiredToken, go: go.expiredToken }, { name: 'oidc-existing-user-email-link', node: nodeLink.linked, go: goLink.linked }, { name: 'oidc-auto-registration-denied', node: nodeDenied.denied, go: goDenied.denied }, { name: 'oidc-invalid-nonce', node: node.invalidNonce, go: go.invalidNonce }, { name: 'oidc-invalid-issuer', node: node.invalidIssuer, go: go.invalidIssuer }, { name: 'oidc-invalid-audience', node: node.invalidAudience, go: go.invalidAudience }, { name: 'oidc-invalid-signature', node: node.invalidSignature, go: go.invalidSignature }, ...mobileCases].map(test => ({ ...test, equal: JSON.stringify(canonicalJSON(test.node)) === JSON.stringify(canonicalJSON(test.go)) }))
    const matched = cases.filter(test => test.equal).length
    result = { baseline: fixture.baseline, provider: 'local ephemeral RS256 provider', compared: cases.length, matched, mismatched: cases.length - matched, cases, limits: ['Web callback without session and empty code_verifier, duplicate empty code_verifier mobile classification, auto-registration success, provider error, callback state replay, expired ID token, email linking, auto-registration denial, invalid nonce/issuer/audience/signature, selected admin-only user_added event, mobile redirect allowlist/PKCE errors, valid mobile token exchange, consumed-flow callback replay, mobile app redirect, and mobile redirect state replay are exercised.', 'Selected web/mobile start and successful callback cookies are compared by name/stable value/Max-Age/Path/Expires/HttpOnly/Secure/SameSite and signed connect.sid. JWT algorithms/claims are compared, normalizing only UUID identifiers and timestamps; each ID-token nonce is checked against its own authorization request. Mobile invalid-verifier/error exchange, in-memory flow-state expiry, failure/logout cookie clearing, other callback variants, and general Socket.IO inventory remain unverified.', 'Persisted user fields and session counts are compared; only fixture-specific backup paths, UUID identifiers, and timestamps are normalized. JSON object member order is canonicalized only for Socket.IO event payloads; arrays, scalar values, null/empty distinctions, and raw persisted extraData text remain strict.'] }
  } finally { await new Promise(resolve => provider.server.close(resolve)) }
  const output = args.out ? path.resolve(args.out) : path.join(work, 'oidc-report.json')
  fs.mkdirSync(path.dirname(output), { recursive: true }); fs.writeFileSync(output, `${JSON.stringify(result, null, 2)}\n`)
  process.stdout.write(`${JSON.stringify({ ...result, report: output }, null, 2)}\n`)
  if (result.mismatched) process.exitCode = 1
}

main().catch(error => { console.error(error.stack || error); process.exitCode = 1 })
