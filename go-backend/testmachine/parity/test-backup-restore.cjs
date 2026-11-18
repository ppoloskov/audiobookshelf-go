#!/usr/bin/env node
'use strict'

const fs = require('node:fs')
const path = require('node:path')
const crypto = require('node:crypto')
const { spawn } = require('node:child_process')
const { setTimeout: delay } = require('node:timers/promises')
const { parseArgs } = require('./lib/args.cjs')
const { freePort, waitReady, stop } = require('./lib/process.cjs')
const { requestJSON } = require('./lib/http.cjs')
const { writeJSON } = require('./lib/artifacts.cjs')

const ROOT = path.resolve(__dirname, '../../..')
const PREFIX = '/audiobookshelf'

function validatedArgs() {
  const out = parseArgs()
  for (const key of ['fixture', 'archive', 'out']) if (!out[key]) throw new Error(`missing --${key}`)
  out.implementation = out.implementation || 'node'
  if (out.implementation === 'go' && !out['go-bin']) throw new Error('--go-bin is required for --implementation go')
  return out
}

async function api(url, route, { method = 'GET', token, body } = {}) {
  const response = await requestJSON(url, route, {
    pathPrefix: PREFIX, method, token, body, timeoutMs: 30000,
    headers: method === 'POST' && route === '/login' ? { 'X-Return-Tokens': 'true' } : {},
    throwOnError: false
  })
  if (!response.ok) throw new Error(`${method} ${route} returned ${response.status}: ${JSON.stringify(response.value)}`)
  return { status: response.status, body: response.value }
}

const waitNodeReady = (child, url, logPath) => waitReady(child, url, { pathPrefix: PREFIX, logPath, timeoutMessage: `Node server did not become ready at ${url}; inspect ${logPath}` })

async function main() {
  const args = validatedArgs()
  const fixturePath = path.resolve(args.fixture)
  const fixture = JSON.parse(fs.readFileSync(fixturePath, 'utf8'))
  const runDir = path.dirname(fixturePath)
  const nodeDir = path.join(runDir, `${args.implementation}-restore`)
  const config = path.join(nodeDir, 'config')
  const metadata = path.join(nodeDir, 'metadata')
  fs.rmSync(nodeDir, { recursive: true, force: true })
  // The imported archive was authored from the Node fixture. Start both
  // targets from that same copy so strict before/after API equality compares
  // identical media paths and filesystem inode values across the restore.
  fs.cpSync(fixture.directories.node, nodeDir, { recursive: true })
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
    await waitNodeReady(child, url, logPath)
    const login = await api(url, '/login', { method: 'POST', body: fixture.rootCredentials })
    const token = login.body?.user?.accessToken
    if (!token) throw new Error('Node login did not return an access token')
    const beforeLibraries = await api(url, '/api/libraries', { token })
    const beforeItem = await api(url, `/api/items/${fixture.items.book}`, { token })
    const itemMetadata = path.join(metadata, 'items', fixture.items.book, 'metadata.json')
    const authorId = beforeItem.body.media?.metadata?.authors?.[0]?.id
    const authorMetadata = authorId ? path.join(metadata, 'authors', `${authorId}.png`) : null
    if (!authorMetadata) throw new Error('book detail did not identify its author metadata asset')
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
    const changedTitle = `${beforeItem.body.media.metadata.title} before restore`
    await api(url, `/api/items/${fixture.items.book}/media`, {
      method: 'PATCH', token, body: { metadata: { title: changedTitle } }
    })
    const changedItem = await api(url, `/api/items/${fixture.items.book}`, { token })
    if (changedItem.body.media?.metadata?.title !== changedTitle) {
      throw new Error('pre-restore item title change was not visible')
    }
    fs.rmSync(itemMetadata, { force: true })
    fs.rmSync(authorMetadata, { force: true })
    const targetMetadataRemovedBeforeApply = !fs.existsSync(itemMetadata) && !fs.existsSync(authorMetadata)
    if (!targetMetadataRemovedBeforeApply) {
      throw new Error('could not remove target metadata files before restore')
    }
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
        preRestoreMutationApplied: changedItem.body.media?.metadata?.title === changedTitle,
        targetMetadataRemovedBeforeApply,
        librariesUnchanged: sameLibraries,
        itemDetailUnchanged: sameItem,
        itemMetadataRestored: itemMetadataExists,
        authorMetadataRestored: authorMetadataExists
      },
      malformedUpload: { status: malformedResponse.status, body: malformedBody },
      apiDigests: { librariesBefore: bodyHash(beforeLibraries.body), librariesAfter: bodyHash(afterLibraries.body), itemBefore: bodyHash(beforeItem.body), itemAfter: bodyHash(afterItem.body) }
    }
    writeJSON(path.resolve(args.out), result)
    console.log(JSON.stringify({ out: path.resolve(args.out), checks: result.checks }, null, 2))
    if (Object.values(result.checks).some(value => value !== true)) process.exitCode = 1
  } finally {
    await stop({ child, log })
  }
}

main().catch(error => {
  process.stderr.write(`${error.stack || error}\n`)
  process.exitCode = 1
})
