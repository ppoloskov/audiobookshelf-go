#!/usr/bin/env node
'use strict'

const fs = require('node:fs')
const path = require('node:path')
const net = require('node:net')
const crypto = require('node:crypto')
const { spawn } = require('node:child_process')
const { setTimeout: delay } = require('node:timers/promises')

const ROOT = path.resolve(__dirname, '../../..')
const PREFIX = '/audiobookshelf'

function parseArgs() {
  const out = {}
  for (let i = 2; i < process.argv.length; i++) {
    if (process.argv[i].startsWith('--')) out[process.argv[i].slice(2)] = process.argv[++i]
  }
  for (const key of ['fixture', 'archive', 'out']) if (!out[key]) throw new Error(`missing --${key}`)
  out.implementation = out.implementation || 'node'
  if (out.implementation === 'go' && !out['go-bin']) throw new Error('--go-bin is required for --implementation go')
  return out
}

async function freePort() {
  const server = net.createServer()
  await new Promise((resolve, reject) => server.listen(0, '127.0.0.1', error => error ? reject(error) : resolve()))
  const port = server.address().port
  await new Promise((resolve, reject) => server.close(error => error ? reject(error) : resolve()))
  return port
}

async function api(url, route, { method = 'GET', token, body } = {}) {
  const response = await fetch(`${url}${PREFIX}${route}`, {
    method,
    headers: {
      ...(token ? { Authorization: `Bearer ${token}` } : {}),
      ...(method === 'POST' && route === '/login' ? { 'X-Return-Tokens': 'true' } : {}),
      ...(body === undefined ? {} : { 'Content-Type': 'application/json' })
    },
    body: body === undefined ? undefined : JSON.stringify(body),
    signal: AbortSignal.timeout(30000)
  })
  const bytes = Buffer.from(await response.arrayBuffer())
  let parsed
  try { parsed = JSON.parse(bytes.toString('utf8')) } catch { parsed = bytes.toString('utf8') }
  if (!response.ok) throw new Error(`${method} ${route} returned ${response.status}: ${JSON.stringify(parsed)}`)
  return { status: response.status, body: parsed }
}

async function waitReady(child, url, logPath) {
  for (let i = 0; i < 120; i++) {
    if (child.exitCode !== null) throw new Error(`Node server exited before readiness; inspect ${logPath}`)
    try { if ((await fetch(`${url}${PREFIX}/ping`)).ok) return } catch {}
    await delay(250)
  }
  throw new Error(`Node server did not become ready at ${url}; inspect ${logPath}`)
}

async function stop(child, log, logPath) {
  if (!child) return
  if (child.exitCode === null) {
    child.kill('SIGINT')
    await Promise.race([
      new Promise(resolve => child.once('exit', resolve)),
      delay(15000).then(() => child.kill('SIGKILL'))
    ])
  }
  log.end()
}

async function main() {
  const args = parseArgs()
  const fixturePath = path.resolve(args.fixture)
  const fixture = JSON.parse(fs.readFileSync(fixturePath, 'utf8'))
  const runDir = path.dirname(fixturePath)
  const nodeDir = path.join(runDir, `${args.implementation}-restore`)
  const config = path.join(nodeDir, 'config')
  const metadata = path.join(nodeDir, 'metadata')
  fs.rmSync(nodeDir, { recursive: true, force: true })
  fs.cpSync(args.implementation === 'go' ? fixture.directories.go : fixture.directories.node, nodeDir, { recursive: true })
  fs.mkdirSync(path.join(metadata, 'backups'), { recursive: true })

  const port = await freePort()
  const url = `http://127.0.0.1:${port}`
  const logPath = path.join(runDir, `${args.implementation}-restore.log`)
  const log = fs.createWriteStream(logPath)
  const child = args.implementation === 'go'
    ? spawn(path.resolve(args['go-bin']), [], { cwd: ROOT, env: {
      ...process.env, CONFIG_PATH: config, METADATA_PATH: metadata,
      HOST: '127.0.0.1', PORT: String(port), ROUTER_BASE_PATH: PREFIX, SOURCE: 'docker'
    }, stdio: ['ignore', 'pipe', 'pipe'] })
    : spawn(process.execPath, [
      path.join(ROOT, 'dist-server/index.js'), '--config', config, '--metadata', metadata,
      '--port', String(port), '--host', '127.0.0.1', '--source', 'docker'
    ], { cwd: ROOT, env: { ...process.env, NODE_ENV: 'production' }, stdio: ['ignore', 'pipe', 'pipe'] })
  child.stdout.pipe(log)
  child.stderr.pipe(log)

  let result
  try {
    await waitReady(child, url, logPath)
    const login = await api(url, '/login', { method: 'POST', body: fixture.rootCredentials })
    const token = login.body?.user?.accessToken
    if (!token) throw new Error('Node login did not return an access token')
    const beforeLibraries = await api(url, '/api/libraries', { token })
    const beforeItem = await api(url, `/api/items/${fixture.items.book}`, { token })
    // Upstream BackupController.upload delegates malformed archives to
    // BackupManager.uploadBackup; retain the observed status/body without
    // treating the expected rejection as a harness failure.
    const malformed = new FormData()
    malformed.append('file', new Blob([Buffer.from('not a zip')], { type: 'application/zip' }), 'malformed.zip')
    const malformedResponse = await fetch(`${url}${PREFIX}/api/backups/upload`, {
      method: 'POST', headers: { Authorization: `Bearer ${token}` }, body: malformed,
      signal: AbortSignal.timeout(30000)
    })
    const malformedText = await malformedResponse.text()
    let malformedBody
    try { malformedBody = JSON.parse(malformedText) } catch { malformedBody = malformedText }
    const fileBytes = fs.readFileSync(path.resolve(args.archive))
    const form = new FormData()
    form.append('file', new Blob([fileBytes], { type: 'application/zip' }), path.basename(args.archive))
    const uploadResponse = await fetch(`${url}${PREFIX}/api/backups/upload`, {
      method: 'POST', headers: { Authorization: `Bearer ${token}` }, body: form,
      signal: AbortSignal.timeout(30000)
    })
    const uploadBody = await uploadResponse.json()
    if (!uploadResponse.ok) throw new Error(`backup upload returned ${uploadResponse.status}: ${JSON.stringify(uploadBody)}`)
    const backup = uploadBody.backups?.find(candidate => candidate.filename === path.basename(args.archive))
    if (!backup?.id) throw new Error('backup upload response did not identify the uploaded archive')
    await api(url, `/api/backups/${encodeURIComponent(backup.id)}/apply`, { token })

    const relogin = await api(url, '/login', { method: 'POST', body: fixture.rootCredentials })
    const restoredToken = relogin.body?.user?.accessToken
    if (!restoredToken) throw new Error('Node relogin after backup apply failed')
    const afterLibraries = await api(url, '/api/libraries', { token: restoredToken })
    const afterItem = await api(url, `/api/items/${fixture.items.book}`, { token: restoredToken })
    const itemMetadata = path.join(metadata, 'items', fixture.items.book, 'metadata.json')
    const authorId = beforeItem.body.media?.metadata?.authors?.[0]?.id
    const authorMetadata = authorId ? path.join(metadata, 'authors', `${authorId}.png`) : null
    const itemMetadataExists = fs.existsSync(itemMetadata)
    const authorMetadataExists = !!authorMetadata && fs.existsSync(authorMetadata)
    const bodyHash = value => crypto.createHash('sha256').update(JSON.stringify(value)).digest('hex')
    const sameLibraries = bodyHash(beforeLibraries.body) === bodyHash(afterLibraries.body)
    const sameItem = bodyHash(beforeItem.body) === bodyHash(afterItem.body)
    result = {
      schemaVersion: 1,
      evidenceKind: `${args.implementation === 'go' ? 'upstream Node' : 'Go'} archive imported and applied by ${args.implementation === 'go' ? 'Go' : 'upstream Node'} server`,
      archive: { name: path.basename(args.archive), bytes: fileBytes.length, sha256: crypto.createHash('sha256').update(fileBytes).digest('hex') },
      checks: {
        malformedUploadRejected: !malformedResponse.ok,
        uploadAccepted: uploadResponse.ok,
        applyReturned200: true,
        librariesUnchanged: sameLibraries,
        itemDetailUnchanged: sameItem,
        itemMetadataRestored: itemMetadataExists,
        authorMetadataRestored: authorMetadataExists
      },
      malformedUpload: { status: malformedResponse.status, body: malformedBody },
      apiDigests: { librariesBefore: bodyHash(beforeLibraries.body), librariesAfter: bodyHash(afterLibraries.body), itemBefore: bodyHash(beforeItem.body), itemAfter: bodyHash(afterItem.body) }
    }
    fs.writeFileSync(path.resolve(args.out), `${JSON.stringify(result, null, 2)}\n`)
    console.log(JSON.stringify({ out: path.resolve(args.out), checks: result.checks }, null, 2))
    if (Object.values(result.checks).some(value => value !== true)) process.exitCode = 1
  } finally {
    await stop(child, log, logPath)
  }
}

main().catch(error => {
  process.stderr.write(`${error.stack || error}\n`)
  process.exitCode = 1
})
