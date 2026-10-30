#!/usr/bin/env node
'use strict'

const fs = require('node:fs')
const path = require('node:path')
const os = require('node:os')
const net = require('node:net')
const { spawn, spawnSync } = require('node:child_process')
const { setTimeout: delay } = require('node:timers/promises')
const { io } = require('socket.io-client')

const ROOT = path.resolve(__dirname, '../../..')
const PREFIX = '/audiobookshelf'
const DEFAULT_FIXTURE = '/private/var/folders/jt/2prvbq7965ggn35rz2vwnqrw0000gn/T/opencode/abs-parity-fixture/fixture-manifest.json'

function args() {
  const out = {}
  for (let i = 2; i < process.argv.length; i++) if (process.argv[i].startsWith('--')) out[process.argv[i].slice(2)] = process.argv[++i]
  return { fixture: out.fixture || DEFAULT_FIXTURE, rounds: Number(out.rounds || 3), cycles: Number(out.cycles || 12), workers: Number(out.workers || 6), 'go-bin': out['go-bin'] || '/tmp/abs-soak-go' }
}

async function freePort() {
  const server = net.createServer()
  await new Promise((resolve, reject) => server.listen(0, '127.0.0.1', e => e ? reject(e) : resolve()))
  const port = server.address().port
  await new Promise((resolve, reject) => server.close(e => e ? reject(e) : resolve()))
  return port
}

function spawnLogged(command, argv, options, logPath) {
  const child = spawn(command, argv, { ...options, stdio: ['ignore', 'pipe', 'pipe'] })
  const log = fs.createWriteStream(logPath)
  child.stdout.pipe(log); child.stderr.pipe(log)
  return { child, log }
}
async function waitReady(proc, url, logPath) {
  for (let i = 0; i < 120; i++) {
    if (proc.child.exitCode !== null) throw new Error(`server exited; see ${logPath}`)
    try { if ((await fetch(`${url}${PREFIX}/ping`)).ok) return } catch {}
    await delay(250)
  }
  throw new Error(`server not ready at ${url}; see ${logPath}`)
}
async function stop(proc) {
  if (!proc) return
  if (proc.child.exitCode === null) {
    proc.child.kill('SIGINT')
    await Promise.race([new Promise(resolve => proc.child.once('exit', resolve)), delay(15000).then(() => proc.child.kill('SIGKILL'))])
  }
  proc.log.end()
}

function fixtureCopy(manifest, dir, side) {
  const sideDir = path.join(dir, side)
  fs.cpSync(manifest.directories[side], sideDir, { recursive: true })
  const mediaSource = manifest.directories[side === 'node' ? 'nodeMedia' : 'goMedia']
  const mediaTarget = path.join(sideDir, 'media')
  if (fs.existsSync(mediaSource) && path.resolve(mediaSource) !== path.resolve(mediaTarget)) fs.cpSync(mediaSource, mediaTarget, { recursive: true })
  const clone = structuredClone(manifest)
  clone.directories[side] = sideDir
  clone.directories[side === 'node' ? 'nodeMedia' : 'goMedia'] = mediaTarget
  clone.directories.media = mediaTarget
  const file = path.join(dir, 'fixture-manifest.json')
  fs.writeFileSync(file, JSON.stringify(clone, null, 2))
  return file
}

async function login(url, credentials) {
  const response = await fetch(`${url}${PREFIX}/login`, {
    method: 'POST', headers: { 'content-type': 'application/json', 'X-Return-Tokens': 'true' },
    body: JSON.stringify(credentials), signal: AbortSignal.timeout(15000)
  })
  const body = await response.json()
  if (!response.ok || !body.user?.accessToken) throw new Error(`login failed ${response.status}: ${JSON.stringify(body)}`)
  return body.user.accessToken
}

function connectAuth(url, socketPath, token) {
  return new Promise(resolve => {
    const started = performance.now()
    const socket = io(url, { path: socketPath, timeout: 8000, reconnection: false, transports: ['polling', 'websocket'] })
    let settled = false
    const done = result => {
      if (settled) return
      settled = true; clearTimeout(timer); socket.disconnect()
      resolve({ ...result, latency: performance.now() - started })
    }
    const timer = setTimeout(() => done({ status: 'timeout' }), 10000)
    socket.once('connect', () => socket.emit('auth', token))
    socket.once('init', () => done({ status: 'init' }))
    socket.once('auth_failed', payload => done({ status: 'auth_failed', detail: payload }))
    socket.once('connect_error', error => done({ status: 'connect_error', detail: error.message }))
  })
}

async function runRound(server, round, options, manifest) {
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), `abs-socket-soak-${server}-${round}-`))
  const copiedManifest = fixtureCopy(manifest, dir, server)
  const fixture = JSON.parse(fs.readFileSync(copiedManifest, 'utf8'))
  const port = await freePort()
  const url = `http://127.0.0.1:${port}`
  const logPath = path.join(dir, `${server}.log`)
  let proc
  try {
    if (server === 'node') {
      const root = fixture.directories.node
      proc = spawnLogged('mise', ['exec', 'node@22', '--', 'node', path.join(ROOT, 'dist-server/index.js'), '--config', path.join(root, 'config'), '--metadata', path.join(root, 'metadata'), '--port', String(port), '--host', '127.0.0.1', '--source', 'docker'], { cwd: ROOT, env: { ...process.env, NODE_ENV: 'production' } }, logPath)
    } else {
      const goRoot = fixture.directories.go
      proc = spawnLogged(options['go-bin'], [], { cwd: ROOT, env: { ...process.env, CONFIG_PATH: path.join(goRoot, 'config'), METADATA_PATH: path.join(goRoot, 'metadata'), HOST: '127.0.0.1', PORT: String(port), ROUTER_BASE_PATH: PREFIX, SOURCE: 'docker' } }, logPath)
    }
    await waitReady(proc, url, logPath)
    const token = await login(url, fixture.rootCredentials)
    const patchUrl = `${url}${PREFIX}/api/me/progress/${fixture.items.book}`
    let patchErrors = 0
    let pressure = true
    const patchers = Array.from({ length: options.workers }, async () => {
      while (pressure) {
        try {
          const response = await fetch(patchUrl, { method: 'PATCH', headers: { authorization: `Bearer ${token}`, 'content-type': 'application/json' }, body: JSON.stringify({ duration: 2, currentTime: 0.5, progress: 0.25 }), signal: AbortSignal.timeout(10000) })
          if (!response.ok) patchErrors++
        } catch { patchErrors++ }
      }
    })
    const latencies = []; let timeouts = 0; let failures = 0
    for (let i = 0; i < options.cycles; i++) {
      const socketPath = i % 2 === 0 ? `${PREFIX}/socket.io` : '/socket.io'
      const result = await connectAuth(url, socketPath, token)
      latencies.push(result.latency)
      if (result.status === 'timeout') timeouts++
      else if (result.status !== 'init') failures++
    }
    pressure = false
    await Promise.all(patchers)
    latencies.sort((a, b) => a - b)
    const percentile = p => latencies[Math.min(latencies.length - 1, Math.ceil(p * latencies.length) - 1)]
    return { round, server, min: latencies[0], max: latencies.at(-1), p50: percentile(.5), p95: percentile(.95), timeouts, authFailures: failures, patchErrors, dir }
  } finally { await stop(proc) }
}

async function main() {
  const options = args()
  const manifest = JSON.parse(fs.readFileSync(path.resolve(options.fixture), 'utf8'))
  if (!fs.existsSync(options['go-bin'])) {
    const build = spawnSync('go', ['build', '-o', options['go-bin'], './cmd/audiobookshelf-go'], { cwd: path.join(ROOT, 'go-backend'), encoding: 'utf8', env: { ...process.env, CGO_ENABLED: '0' } })
    if (build.status !== 0) throw new Error(`Go build failed (${build.status}): ${build.stderr}`)
  }
  const results = []
  for (const server of ['node', 'go']) for (let round = 1; round <= options.rounds; round++) results.push(await runRound(server, round, options, manifest))
  console.log(`load: ${options.workers} progress PATCH workers; ${options.cycles} sequential socket cycles/round; alternating prefixed and legacy paths`)
  console.log('| Round | Server | min ms | max ms | p50 ms | p95 ms | auth timeouts | auth failures | patch errors |')
  console.log('|---:|---|---:|---:|---:|---:|---:|---:|---:|')
  for (const r of results) console.log(`| ${r.round} | ${r.server} | ${r.min.toFixed(1)} | ${r.max.toFixed(1)} | ${r.p50.toFixed(1)} | ${r.p95.toFixed(1)} | ${r.timeouts} | ${r.authFailures} | ${r.patchErrors} |`)
  console.log(`\nRaw results (including disposable workspace paths):\n${JSON.stringify(results, null, 2)}`)
}

main().catch(error => { console.error(error.stack || error); process.exitCode = 1 })
