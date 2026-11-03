#!/usr/bin/env node
'use strict'

// Boots the pinned TS server (TS v2.37.2, see build.cjs) on a fresh copy of the
// contract-test fixture, for the Go recorder:
//
//   node go-backend/tools/contractfixture/serve-upstream.cjs
//   cd go-backend && go test ./internal/contracttest -run TestJourney -record \
//       -upstream=<baseURL> -upstream-root=<root>
//
// With --empty the server starts on empty config and metadata directories: an
// uninitialized install with no root user (the TestJourneyInit recording).
//
// The copy lives in a temp directory. Its seed paths in the database are
// rewritten to the copy, with the same rule as fixture.go (rewriteSeedPaths):
// every text-like column, longest seed path first. Prints
//   ROOT <temp root>
//   READY <baseURL>
// then stays up until SIGINT or SIGTERM, which stops the server and removes
// the copy. Needs the repository's node_modules (sqlite3) and dist-server.

const fs = require('node:fs')
const os = require('node:os')
const path = require('node:path')
const sqlite3 = require('sqlite3')
const { freePort, spawnLogged, waitReady, stop } = require('../parity/lib/process.cjs')

const REPO_ROOT = path.resolve(__dirname, '../../..')
const FIXTURE = path.join(REPO_ROOT, 'go-backend/internal/contracttest/testdata/install')
const BASE = '/audiobookshelf'

function open(file) {
  return new Promise((resolve, reject) => {
    const db = new sqlite3.Database(file, sqlite3.OPEN_READWRITE, error => error ? reject(error) : resolve(db))
  })
}

function all(db, sql, params = []) {
  return new Promise((resolve, reject) => db.all(sql, params, (error, rows) => error ? reject(error) : resolve(rows)))
}

function run(db, sql, params = []) {
  return new Promise((resolve, reject) => db.run(sql, params, error => error ? reject(error) : resolve()))
}

function close(db) {
  return new Promise((resolve, reject) => db.close(error => error ? reject(error) : resolve()))
}

// textColumns mirrors textColumns in internal/contracttest/fixture.go: every
// column of every user table whose declared type is TEXT-like or empty.
async function textColumns(db) {
  const tables = await all(db, `SELECT name FROM sqlite_master WHERE type = 'table' AND substr(name, 1, 7) <> 'sqlite_'`)
  const out = []
  for (const { name } of tables) {
    const quoted = `"${name.replace(/"/g, '""')}"`
    for (const col of await all(db, `PRAGMA table_info(${quoted})`)) {
      const upper = String(col.type).toUpperCase()
      const textLike = upper === '' || upper.includes('TEXT') || upper.includes('CHAR') || upper.includes('CLOB') || upper.includes('JSON')
      if (textLike) out.push({ table: quoted, column: `"${col.name.replace(/"/g, '""')}"` })
    }
  }
  return out
}

// rewriteSeedPaths mirrors rewriteSeedPaths in internal/contracttest/fixture.go.
async function rewriteSeedPaths(file, pairs) {
  const db = await open(file)
  try {
    const cols = await textColumns(db)
    pairs.sort((a, b) => b.from.length - a.from.length)
    await run(db, 'BEGIN')
    try {
      for (const { from, to } of pairs) {
        if (!from) throw new Error('empty seed path')
        for (const c of cols) {
          await run(db, `UPDATE ${c.table} SET ${c.column} = replace(${c.column}, ?, ?) WHERE instr(${c.column}, ?) > 0`, [from, to, from])
        }
      }
      await run(db, 'COMMIT')
    } catch (error) {
      await run(db, 'ROLLBACK').catch(() => {})
      throw error
    }
  } finally {
    await close(db)
  }
}

const EMPTY = process.argv.includes('--empty')

async function main() {
  const root = fs.mkdtempSync(path.join(os.tmpdir(), 'abs-contract-upstream-'))
  const dirs = { config: path.join(root, 'config'), metadata: path.join(root, 'metadata'), media: path.join(root, 'media') }
  if (EMPTY) {
    for (const dir of Object.values(dirs)) fs.mkdirSync(dir)
  } else {
    const meta = JSON.parse(fs.readFileSync(path.join(FIXTURE, 'fixture.json'), 'utf8'))
    fs.cpSync(FIXTURE, root, { recursive: true })
    await rewriteSeedPaths(path.join(dirs.config, 'absdatabase.sqlite'), [
      { from: meta.seedRoots.media, to: dirs.media },
      { from: meta.seedRoots.metadata, to: dirs.metadata },
      { from: meta.seedRoots.config, to: dirs.config }
    ])
  }
  console.log(`ROOT ${root}`)

  const port = await freePort()
  const baseUrl = `http://127.0.0.1:${port}`
  const logPath = path.join(root, 'upstream.log')
  // Same launch as startUpstream in tools/parity/replacement-trial.cjs, plus the
  // SSRF whitelist the image-source samples need (AGENTS.md).
  const server = spawnLogged(process.execPath, [
    path.join(REPO_ROOT, 'dist-server/index.js'), '--config', dirs.config, '--metadata', dirs.metadata,
    '--port', String(port), '--host', '127.0.0.1', '--source', 'docker'
  ], {
    cwd: REPO_ROOT,
    env: { ...process.env, NODE_ENV: 'production', SSRF_REQUEST_FILTER_WHITELIST: '127.0.0.1' }
  }, logPath)

  let stopping = false
  const shutdown = async () => {
    if (stopping) return
    stopping = true
    await stop(server).catch(() => {})
    fs.rmSync(root, { recursive: true, force: true })
    process.exit(0)
  }
  process.on('SIGINT', shutdown)
  process.on('SIGTERM', shutdown)
  server.child.on('exit', code => {
    if (!stopping) {
      console.error(`upstream exited with ${code}; inspect ${logPath}`)
      fs.rmSync(root, { recursive: true, force: true })
      process.exit(1)
    }
  })

  await waitReady(server, baseUrl, { pathPrefix: BASE, logPath, attempts: 240 })
  console.log(`READY ${baseUrl}`)
}

main().catch(error => {
  console.error(error)
  process.exitCode = 1
})
