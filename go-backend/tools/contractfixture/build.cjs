#!/usr/bin/env node
'use strict'

// Builds the contract-test fixture: a realistic installation created and
// scanned by pinned upstream (TS v2.37.2, see UPSTREAM_COMMIT), copied into
// go-backend/internal/contracttest/testdata/install/. Run by hand once:
//
//   node go-backend/tools/contractfixture/build.cjs [--work DIR]
//
// Requires Node 22, FFmpeg and a built dist-server (npm run build:server).
// The seed reuses the replacement trial's seedInstallation. Absolute paths
// used at seed time are recorded in fixture.json so Boot can rewrite them.
// Only the database, metadata, media and ids.json are copied; the work dir
// is left behind for inspection.

const fs = require('node:fs')
const os = require('node:os')
const path = require('node:path')
const { DatabaseSync } = require('node:sqlite')
const trial = require('../parity/replacement-trial.cjs')
const { parseArgs } = require('../parity/lib/args.cjs')

const UPSTREAM_COMMIT = 'edb1f7efc92ecacdef51aafb4d9d5054ce10604f'
const REPO_ROOT = path.resolve(__dirname, '../../..')
const OUT = path.join(REPO_ROOT, 'go-backend/internal/contracttest/testdata/install')

function dirSize(dir) {
  let total = 0
  for (const entry of fs.readdirSync(dir, { withFileTypes: true, recursive: true })) {
    if (entry.isFile()) total += fs.statSync(path.join(entry.parentPath, entry.name)).size
  }
  return total
}

async function main() {
  const args = parseArgs()
  const work = path.resolve(args.work || fs.mkdtempSync(path.join(os.tmpdir(), 'abs-contractfixture-')))
  fs.mkdirSync(work, { recursive: true })
  console.log(`seed work dir: ${work}`)

  const { ids } = await trial.seedInstallation(work)
  const snapshot = path.join(work, 'snapshot')
  const snapshotDB = path.join(snapshot, 'config', 'absdatabase.sqlite')

  // Compact copy via VACUUM INTO: a single self-contained file, no -wal/-shm.
  fs.rmSync(OUT, { recursive: true, force: true })
  fs.mkdirSync(path.join(OUT, 'config'), { recursive: true })
  const vacuumed = path.join(OUT, 'config', 'absdatabase.sqlite')
  const db = new DatabaseSync(snapshotDB)
  try {
    db.exec(`VACUUM INTO '${vacuumed.replace(/'/g, "''")}'`)
  } finally {
    db.close()
  }
  for (const suffix of ['-wal', '-shm', '-journal']) {
    if (fs.existsSync(vacuumed + suffix)) throw new Error(`unexpected ${suffix} next to fixture database`)
  }

  fs.cpSync(path.join(snapshot, 'metadata'), path.join(OUT, 'metadata'), { recursive: true })
  fs.cpSync(path.join(work, 'media'), path.join(OUT, 'media'), { recursive: true })
  fs.copyFileSync(path.join(work, 'ids.json'), path.join(OUT, 'ids.json'))

  const fixture = {
    upstreamCommit: UPSTREAM_COMMIT,
    seedRoots: {
      config: path.join(work, 'install', 'config'),
      metadata: path.join(work, 'install', 'metadata'),
      media: path.join(work, 'media')
    },
    users: {
      root: trial.ROOT_USER,
      listener: trial.LISTENER,
      finisher: trial.FINISHER
    }
  }
  fs.writeFileSync(path.join(OUT, 'fixture.json'), JSON.stringify(fixture, null, 2) + '\n')

  const size = dirSize(OUT)
  console.log(`fixture written to ${OUT}`)
  console.log(`ids: ${JSON.stringify(ids)}`)
  console.log(`size: ${(size / 1024 / 1024).toFixed(2)} MiB`)
  if (size > 10 * 1024 * 1024) {
    throw new Error(`fixture is ${size} bytes, over the 10 MB budget; report rather than trimming journey content`)
  }
}

main().catch((error) => {
  console.error(error)
  process.exitCode = 1
})
