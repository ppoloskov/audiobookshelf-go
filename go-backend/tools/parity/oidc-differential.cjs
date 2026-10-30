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
      const expired = body.get('code').includes('-expired')
      const claims = b64url(JSON.stringify({ iss: base, aud: CLIENT_ID, sub: USER.sub, nonce: flow.nonce, iat: now, exp: expired ? now - 60 : now + 3600 }))
      const unsigned = `${header}.${claims}`
      const token = `${unsigned}.${crypto.sign('RSA-SHA256', Buffer.from(unsigned), privateKey).toString('base64url')}`
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

async function configureDB(file, provider) {
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
      authOpenIDAutoRegister: true,
      authOpenIDMatchExistingBy: null,
      authOpenIDGroupClaim: '',
      authOpenIDAdvancedPermsClaim: ''
    })
    await run("UPDATE settings SET value=? WHERE key='server-settings'", [JSON.stringify(settings)])
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

async function get(url, jar) {
  const response = await fetch(url, { redirect: 'manual', headers: jar.size ? { cookie: [...jar].map(([k, v]) => `${k}=${v}`).join('; ') } : {}, signal: AbortSignal.timeout(20000) })
  cookieJar(response.headers, jar)
  return response
}

function redirectSummary(response, base) {
  const location = response.headers.get('location') || ''
  const target = location ? new URL(location, base) : null
  return { status: response.status, redirectPath: target?.pathname || null, redirectError: target?.searchParams.get('error') || null }
}

async function snapshotDB(file) {
  const sqlite = require('sqlite3').verbose(); const db = new sqlite.Database(file)
  const all = (sql, args = []) => new Promise((resolve, reject) => db.all(sql, args, (err, rows) => err ? reject(err) : resolve(rows)))
  try {
    const users = await all('SELECT username,email,type,isActive,extraData FROM users WHERE username=? ORDER BY username', [USER.preferred_username])
    const sessions = await all('SELECT COUNT(*) AS count FROM sessions WHERE userId IN (SELECT id FROM users WHERE username=?)', [USER.preferred_username])
    return { users, sessionCount: sessions[0].count }
  } finally { await new Promise(resolve => db.close(resolve)) }
}

async function runFlow(kind, dir, goBin, provider, work) {
  const port = await freePort(), base = `http://127.0.0.1:${port}`, logPath = path.join(work, `${kind}.log`)
  await configureDB(path.join(dir, 'config', 'absdatabase.sqlite'), provider.base)
  const proc = startServer(kind, dir, port, logPath, goBin)
  try {
    await ready(proc, base, logPath)
    const jar = new Map()
    const start = await get(`${base}${PREFIX}/auth/openid?callback=${encodeURIComponent(`${PREFIX}/login`)}`, jar)
    if (start.status !== 302) throw new Error(`${kind} OIDC start returned ${start.status}: ${await start.text()}`)
    const authorization = new URL(start.headers.get('location'))
    const code = `code-${kind}`
    provider.flows.set(code, { nonce: authorization.searchParams.get('nonce'), challenge: authorization.searchParams.get('code_challenge'), redirectURI: authorization.searchParams.get('redirect_uri') })
    const callback = new URL(`${base}${PREFIX}/auth/openid/callback`)
    callback.searchParams.set('code', code); callback.searchParams.set('state', authorization.searchParams.get('state'))
    const result = await get(callback, jar)
    const location = result.headers.get('location') || ''
    const parsedLocation = location ? new URL(location, base) : null
    const summary = {
      status: result.status,
      redirectPath: parsedLocation?.pathname || null,
      redirectError: parsedLocation?.searchParams.get('error') || null,
      hasSetToken: Boolean(parsedLocation?.searchParams.get('setToken')),
      hasAccessToken: Boolean(parsedLocation?.searchParams.get('accessToken')),
      userState: parsedLocation?.searchParams.get('state') || null,
      persisted: await snapshotDB(path.join(dir, 'config', 'absdatabase.sqlite'))
    }
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
    return { success: summary, providerError: providerErrorSummary, replay: replaySummary, expiredToken: expiredSummary }
  } finally { await stop(proc) }
}

async function main() {
  const args = parseArgs(), fixture = JSON.parse(fs.readFileSync(args.fixture, 'utf8'))
  const work = path.resolve(args['work-dir'])
  if (fs.existsSync(work) && fs.readdirSync(work).length) throw new Error(`work directory must be empty: ${work}`)
  fs.mkdirSync(work, { recursive: true })
  const dirs = {}
  for (const kind of ['node', 'go']) {
    dirs[kind] = path.join(work, kind)
    fs.cpSync(fixture.directories[kind], dirs[kind], { recursive: true, preserveTimestamps: true })
  }
  const provider = makeProvider(), providerPort = await listen(provider.server)
  provider.setBase(`http://127.0.0.1:${providerPort}`)
  let result
  try {
    const node = await runFlow('node', dirs.node, args['go-bin'], provider, work)
    const go = await runFlow('go', dirs.go, args['go-bin'], provider, work)
    const cases = [{ name: 'oidc-web-auto-register-success', node: node.success, go: go.success }, { name: 'oidc-provider-error', node: node.providerError, go: go.providerError }, { name: 'oidc-state-replay-after-provider-error', node: node.replay, go: go.replay }, { name: 'oidc-expired-id-token', node: node.expiredToken, go: go.expiredToken }].map(test => ({ ...test, equal: JSON.stringify(test.node) === JSON.stringify(test.go) }))
    const matched = cases.filter(test => test.equal).length
    result = { baseline: fixture.baseline, provider: 'local ephemeral RS256 provider', compared: cases.length, matched, mismatched: cases.length - matched, cases, limits: ['Web auto-registration success, provider-error redirect, replay after provider error, and expired ID-token rejection are exercised.', 'Denial, linking, and Socket.IO user_added observations remain uncovered.', 'UUIDs, timestamps, and tokens are excluded from persisted-state comparison; semantic user fields and session count are compared.'] }
  } finally { await new Promise(resolve => provider.server.close(resolve)) }
  const output = args.out ? path.resolve(args.out) : path.join(work, 'oidc-report.json')
  fs.mkdirSync(path.dirname(output), { recursive: true }); fs.writeFileSync(output, `${JSON.stringify(result, null, 2)}\n`)
  process.stdout.write(`${JSON.stringify({ ...result, report: output }, null, 2)}\n`)
  if (result.mismatched) process.exitCode = 1
}

main().catch(error => { console.error(error.stack || error); process.exitCode = 1 })
