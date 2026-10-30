#!/usr/bin/env node
'use strict'

// Web-client acceptance probe for the Go backend: boots the Go server with the
// retained original client build (client/dist, Nuxt generate output) against
// an existing v2.37.2 fixture database copy, then verifies the SPA shell,
// hashed assets, enumerated dynamic routes, unknown-path 404 semantics, and
// the client's boot-time API calls. This is scripted serving/flow acceptance —
// full manual or Playwright browser qualification is a separate gate.

const fs = require('node:fs')
const path = require('node:path')
const { spawn } = require('node:child_process')

const ROOT = path.resolve(__dirname, '../../..')
const BASE = '/audiobookshelf'

const GO_BIN = process.env.GO_BIN || path.join(ROOT, 'go-backend', 'audiobookshelf-go')

async function freePort() {
  const server = require('node:net').createServer()
  await new Promise((resolve, reject) => server.listen(0, '127.0.0.1', (error) => error ? reject(error) : resolve()))
  const port = server.address().port
  await new Promise((resolve) => server.close(resolve))
  return port
}

function startGo(config, metadata, port, logPath) {
  const log = fs.createWriteStream(logPath)
  const child = spawn(GO_BIN, [], {
    cwd: ROOT,
    env: {
      ...process.env,
      CONFIG_PATH: config,
      METADATA_PATH: metadata,
      ABS_GO_CLIENT_PATH: path.join(ROOT, 'client', 'dist'),
      HOST: '127.0.0.1',
      PORT: String(port),
      ROUTER_BASE_PATH: BASE,
      SOURCE: 'docker'
    },
    stdio: ['ignore', 'pipe', 'pipe']
  })
  child.stdout.pipe(log)
  child.stderr.pipe(log)
  const baseUrl = `http://127.0.0.1:${port}`
  let lastError = ''
  const ready = (async () => {
    for (let i = 0; i < 120; i++) {
      if (child.exitCode !== null) throw new Error(`go server exited early; log: ${logPath}`)
      try {
        const response = await fetch(`${baseUrl}${BASE}/ping`)
        if (response.ok) return
      } catch (error) { lastError = error.message }
      await new Promise((resolve) => setTimeout(resolve, 250))
    }
    child.kill('SIGABRT')
    throw new Error(`go server did not become ready (${lastError}); log: ${logPath}`)
  })()
  return { child, log, baseUrl, ready }
}

async function stopServer(server) {
  if (!server) return
  if (server.child.exitCode === null) {
    server.child.kill('SIGINT')
    await Promise.race([
      new Promise((resolve) => server.child.once('exit', resolve)),
      new Promise((resolve) => setTimeout(() => { server.child.kill('SIGKILL'); resolve() }, 5000))
    ])
  }
  server.log.end()
}

async function main() {
  const manifestPath = process.argv[2]
  if (!manifestPath) throw new Error('usage: client-check.cjs <fixture-manifest.json> [go-config-dir] [go-metadata-dir]')
  const manifest = JSON.parse(fs.readFileSync(manifestPath, 'utf8'))
  const goConfig = process.argv[3] || path.join(manifest.directories.go, 'config')
  const goMetadata = process.argv[4] || path.join(manifest.directories.go, 'metadata')

  const port = await freePort()
  const logPath = path.join(path.dirname(manifestPath), 'client-check-go.log')
  const server = startGo(goConfig, goMetadata, port, logPath)
  await server.ready
  const results = {}
  try {
    // 1. SPA shell at the router base path.
    const shell = await fetch(`${server.baseUrl}${BASE}/`)
    const html = await shell.text()
    results.shell = shell.status === 200 && html.includes('id="__nuxt"')

    // 2. Hashed Nuxt assets referenced by the shell all load.
    const assetUrls = [...html.matchAll(/(?:src|href)="(\/[^"]+\.(?:js|css))"/g)]
      .map((match) => match[1])
      .filter((url) => !url.startsWith(`${BASE}/api`))
    const assetResults = await Promise.all(assetUrls.slice(0, 20).map(async (url) => {
      const response = await fetch(`${server.baseUrl}${url}`)
      return response.status === 200
    }))
    results.assets = assetUrls.length > 0 && assetResults.every(Boolean)

    // 3. Enumerated dynamic client routes serve the shell (index.html).
    const dynamic = await fetch(`${server.baseUrl}${BASE}/library/${manifest.libraries.book}`)
    results.dynamicRoutes = dynamic.status === 200 && (await dynamic.text()).includes('id="__nuxt"')

    // 4. Unknown paths 404 (TASK-0010 upstream default-client semantics).
    const unknown = await fetch(`${server.baseUrl}${BASE}/definitely/not/a/route`)
    results.unknownPath404 = unknown.status === 404

    // 5. Login through the served client path (the client's auth flow origin).
    const login = await fetch(`${server.baseUrl}${BASE}/login`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(manifest.rootCredentials)
    })
    const loginBody = await login.json()
    results.login = login.status === 200 && Boolean(loginBody?.user?.accessToken)

    // 6. Client boot API: libraries list with the login token.
    const list = await fetch(`${server.baseUrl}${BASE}/api/libraries`, {
      headers: { Authorization: `Bearer ${loginBody.user.accessToken}` }
    })
    const listBody = await list.json()
    results.bootApi = list.status === 200 && Array.isArray(listBody?.libraries) && listBody.libraries.length >= 2

    for (const name of Object.keys(results)) console.log(`${results[name] ? 'PASS' : 'FAIL'} client.${name}`)
    process.exitCode = Object.values(results).every(Boolean) ? 0 : 1
  } finally {
    await stopServer(server)
  }
}

main().catch((error) => {
  process.stderr.write(`${error.stack || error}\n`)
  process.exitCode = 1
})