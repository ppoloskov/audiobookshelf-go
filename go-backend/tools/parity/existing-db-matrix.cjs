#!/usr/bin/env node
'use strict'

// Existing-database boot matrix for the pinned v2.37.2 replacement. Each
// scenario copies the fixture database tree and boots the Go binary alone:
//
// 1. exact baseline (migrationsMeta 2.37.2) must boot;
// 2. lower maxVersion (schema predates the pinned release) must refuse with
//    the bootstrap error — upstream would run migrations, the Go replacement
//    cannot, and it must fail clearly instead (CONTRACT/AGENTS);
// 3. a newer version must refuse the same way;
// 4. a missing migrationsMeta row must refuse rather than guess;
// 5. an in-flight partial upgrade state (version<maxVersion within the pinned
//    release) must refuse like upstream-completing migrations is out of scope;
// 6. a refused boot must leave database bytes and directory trees unchanged;
// 7. a healthy boot must not mutate an existing pinned database.
//
// The rollback direction (Node reopens a Go-written database) is covered by
// rollback-check.cjs and invoked by the differential pipeline.

const fs = require('node:fs')
const path = require('node:path')
const { spawn } = require('node:child_process')
const { setTimeout: delay } = require('node:timers/promises')

const GO_BIN = process.env.GO_BIN || path.resolve(__dirname, '../../audiobookshelf-go')

// A healthy boot must be observed SERVING: the check polls /audiobookshelf/ping
// for an actual OK response (a hung process never becomes ready and fails the
// scenario). After the readiness answer the process is SIGINT-stopped and the
// check awaits its exit before any snapshot comparison.
const BOOT_PORT = 58999
const BOOT_URL = `http://127.0.0.1:${BOOT_PORT}`
const PING = `${BOOT_URL}/audiobookshelf/ping`
function bootEnv(cfg, metadata) {
  return {
    ...process.env,
    CONFIG_PATH: cfg,
    METADATA_PATH: metadata,
    HOST: '127.0.0.1',
    PORT: String(BOOT_PORT),
    ROUTER_BASE_PATH: '/audiobookshelf',
    SOURCE: 'docker'
  }
}

async function bootUntilReady(cfg, metadata, { readinessTimeoutMs = 30000, command = GO_BIN, args = [], env = bootEnv(cfg, metadata) } = {}) {
  const child = spawn(command, args, { env, stdio: ['ignore', 'pipe', 'pipe'] })
  const output = []
  child.stdout.on('data', d => output.push(d))
  child.stderr.on('data', d => output.push(d))
  let ready = false
  const deadline = Date.now() + readinessTimeoutMs
  let exit = null
  while (Date.now() < deadline) {
    if (child.exitCode !== null) { exit = child.exitCode; break }
    try {
      const response = await fetch(PING, { signal: AbortSignal.timeout(2000) })
      if (response.ok) { ready = true; break }
    } catch {}
    await delay(250)
  }
  let stopped = false
  if (child.exitCode === null) {
    child.kill('SIGINT')
    stopped = await Promise.race([
      new Promise(resolve => child.once('exit', () => resolve(true))),
      delay(12000).then(() => { child.kill('SIGKILL'); return new Promise(resolve => child.once('exit', () => resolve(true))) })
    ])
  } else {
    // The child already exited (e.g. a refusal); do not wait for another
    // 'exit' event: an await on a fired event drains the event loop and node
    // exits mid-main with the wrong code.
    stopped = true
  }
  return { ready, exitCode: exit, stopped, output: output.join('').slice(-4000) }
}

function mutateMeta(dbPath, patch) {
  const sqlite3 = require('sqlite3').verbose()
  return new Promise((resolve, reject) => {
    const db = new sqlite3.Database(dbPath)
    db.serialize(() => {
      for (const [key, value] of Object.entries(patch)) {
        db.run('UPDATE migrationsMeta SET value=? WHERE key=?', [value, key])
      }
      db.close((error) => error ? reject(error) : resolve())
    })
  })
}

function deleteMetaRows(dbPath) {
  const sqlite3 = require('sqlite3').verbose()
  return new Promise((resolve, reject) => {
    const db = new sqlite3.Database(dbPath)
    db.serialize(() => {
      db.run('DELETE FROM migrationsMeta')
      db.close((error) => error ? reject(error) : resolve())
    })
  })
}

const crypto = require('node:crypto')

// Deterministic logical dump of the config database: schema (sqlite_master
// minus internal tables) plus every table's rows, sorted for comparison.
function dumpDatabase(dbPath) {
  const sqlite3 = require('sqlite3').verbose()
  return new Promise((resolve, reject) => {
    const db = new sqlite3.Database(dbPath)
    db.serialize(async () => {
      db.all(`SELECT type, name, tbl_name, sql FROM sqlite_master WHERE name NOT LIKE 'sqlite_%' ORDER BY name`,
        [], async (error, schemaRows) => {
          if (error) { db.close(); return reject(error) }
          const tables = schemaRows.filter((row) => row.type === 'table').map((row) => row.name)
          const payload = { schema: schemaRows, rows: [] }
          try {
            for (const table of tables) {
              payload.rows.push({ table, rows: await new Promise((res, rej) => {
                db.all(`SELECT * FROM "${table}" ORDER BY rowid`, [], (e, rs) => e ? rej(e) : res(rs))
              }) })
            }
          } catch (e) { db.close(); return reject(e) }
          db.close((closeError) => closeError ? reject(closeError) : resolve(JSON.stringify(payload)))
        })
    })
  })
}

function snapshotTree(root) {
  const snapshot = {}
  if (!fs.existsSync(root)) return snapshot
  for (const entry of fs.readdirSync(root, { withFileTypes: true })) {
    const full = path.join(root, entry.name)
    if (entry.isDirectory()) {
      snapshot[entry.name + '/'] = snapshotTree(full)
    } else {
      if (entry.name.endsWith('.log')) continue // transient logs are not database state
      snapshot[entry.name] = crypto.createHash('sha256').update(fs.readFileSync(full)).digest('hex')
    }
  }
  return snapshot
}

async function main() {
  const manifestPath = process.argv[2]
  if (!manifestPath) throw new Error('usage: existing-db-matrix.cjs <fixture-manifest.json>')
  const manifest = JSON.parse(fs.readFileSync(manifestPath, 'utf8'))
  const workRoot = path.join(path.dirname(manifestPath), `existing-db-matrix-${process.pid}`)
  fs.mkdirSync(workRoot, { recursive: true })

  const copyTree = (name) => {
    const dir = path.join(workRoot, name)
    fs.mkdirSync(dir, { recursive: true })
    fs.cpSync(path.join(manifest.directories.go, 'config'), path.join(dir, 'config'), { recursive: true, force: true })
    fs.cpSync(path.join(manifest.directories.go, 'metadata'), path.join(dir, 'metadata'), { recursive: true, force: true })
    return dir
  }

  const scenarios = [
    {
      // Self-check for the boot gate itself: a process that never serves must
      // FAIL readiness, not pass by timeout.
      name: 'hang-detection-selfcheck',
      expect: 'not-ready',
      prepare: (configDir, _metadataDir) => ({ configDir, metadataDir: _metadataDir })
    },
    {
      // Serving boot gate, exercised twice with both transitions snapshotted
      // (pristine, after boot #1, after boot #2) so the "initial and repeat
      // mutation-free boots" invariant has before/after evidence.
      name: 'baseline-boots-serving',
      expect: 'dual-boot'
    },
    {
      name: 'stale-maxVersion-refuses',
      expect: 'refuse',
      prepare: async (configDir, metadataDir) => {
        const dir = copyTree('stale')
        await mutateMeta(path.join(dir, 'config', 'absdatabase.sqlite'), { maxVersion: '2.36.4' })
        return { configDir: path.join(dir, 'config'), metadataDir: path.join(dir, 'metadata') }
      },
      message: 'existing database migration state is'
    },
    {
      name: 'in-flight-upgrade-refuses',
      expect: 'refuse',
      prepare: async () => {
        const dir = copyTree('inflight')
        await mutateMeta(path.join(dir, 'config', 'absdatabase.sqlite'),
          { version: '2.37.1', maxVersion: '2.37.2' })
        return { configDir: path.join(dir, 'config'), metadataDir: path.join(dir, 'metadata') }
      },
      message: 'existing database migration state is',
      checkUnmutated: true
    },
    {
      name: 'refused-boot-leaves-db-unmutated',
      expect: 'refuse',
      prepare: async () => {
        const dir = copyTree('nomut')
        await mutateMeta(path.join(dir, 'config', 'absdatabase.sqlite'), { maxVersion: '2.36.4' })
        return { configDir: path.join(dir, 'config'), metadataDir: path.join(dir, 'metadata') }
      },
      message: 'existing database migration state is',
      checkUnmutated: true
    },
    {
      name: 'newer-version-refuses',
      expect: 'refuse',
      prepare: async () => {
        const dir = copyTree('newer')
        await mutateMeta(path.join(dir, 'config', 'absdatabase.sqlite'), { version: '2.38.0', maxVersion: '2.38.0' })
        return { configDir: path.join(dir, 'config'), metadataDir: path.join(dir, 'metadata') }
      },
      message: 'existing database migration state is'
    },
    {
      name: 'missing-migrationsMeta-refuses',
      expect: 'refuse',
      prepare: async () => {
        const dir = copyTree('nometa')
        await deleteMetaRows(path.join(dir, 'config', 'absdatabase.sqlite'))
        return { configDir: path.join(dir, 'config'), metadataDir: path.join(dir, 'metadata') }
      },
      message: 'bootstrap: read migrationsMeta'
    }
  ]

  let failures = 0
  for (const scenario of scenarios) {
    if (scenario.expect === 'dual-boot') {
      // Serving-boot scenario: readiness observed twice, both transition
      // snapshots compared against the pristine copy.
      const dir = copyTree('dual')
      const cfg = path.join(dir, 'config')
      const meta = path.join(dir, 'metadata')
      const dbPath = path.join(cfg, 'absdatabase.sqlite')
      const s0 = { dump: await dumpDatabase(dbPath), tree: JSON.stringify({ config: snapshotTree(cfg), metadata: snapshotTree(meta) }) }
      const run1 = await bootUntilReady(cfg, meta)
      const s1 = { dump: await dumpDatabase(dbPath), tree: JSON.stringify({ config: snapshotTree(cfg), metadata: snapshotTree(meta) }) }
      const run2 = await bootUntilReady(cfg, meta)
      const s2 = { dump: await dumpDatabase(dbPath), tree: JSON.stringify({ config: snapshotTree(cfg), metadata: snapshotTree(meta) }) }
      const boot1Pass = run1.ready && run1.stopped
      const boot2Pass = run2.ready && run2.stopped
      const unchanged1 = s1.dump === s0.dump && s1.tree === s0.tree
      const unchanged2 = s2.dump === s1.dump && s2.tree === s1.tree
      console.log(`${boot1Pass ? 'PASS' : 'FAIL'} existing-db.baseline-boots-serving.boot1-ready-then-stopped`)
      console.log(`${boot2Pass ? 'PASS' : 'FAIL'} existing-db.baseline-boots-serving.boot2-ready-then-stopped`)
      console.log(`${unchanged1 ? 'PASS' : 'FAIL'} existing-db.baseline-boots-serving.boot1-transition-unmutated`)
      console.log(`${unchanged2 ? 'PASS' : 'FAIL'} existing-db.baseline-boots-serving.boot2-transition-unmutated`)
      if (!boot1Pass || !boot2Pass || !unchanged1 || !unchanged2) {
        failures++
        console.log(`  boot1=${JSON.stringify({ ready: run1.ready, stopped: run1.stopped, exit: run1.exitCode })} boot2=${JSON.stringify({ ready: run2.ready, stopped: run2.stopped, exit: run2.exitCode })}`)
        console.log(`  out1=${run1.output.slice(0, 300)} out2=${run2.output.slice(0, 300)}`)
      }
      continue
    }

    const { configDir, metadataDir } = await scenario.prepare(
      path.join(manifest.directories.go, 'config'),
      path.join(manifest.directories.go, 'metadata')
    )
    const dbPath = path.join(configDir, 'absdatabase.sqlite')
    const beforeDump = scenario.checkUnmutated ? await dumpDatabase(dbPath) : null
    const beforeTree = scenario.checkUnmutated
      ? { config: snapshotTree(configDir), metadata: snapshotTree(metadataDir) }
      : null
    let attempt
    if (scenario.expect === 'not-ready') {
      // Self-check: launch a process that consumes the boot cycle forever
      // without serving; the readiness window must expire and the scenario
      // records a detected hang (this proves timeout cannot masquerade as a
      // successful boot).
      attempt = await bootUntilReady(configDir, metadataDir, {
        command: process.execPath,
        args: ['-e', 'setInterval(() => {}, 1000)'],
        readinessTimeoutMs: 12000
      })
    } else {
      attempt = await bootUntilReady(path.join(configDir), path.join(metadataDir))
    }
    let pass
    if (scenario.expect === 'refuse') {
      pass = !attempt.ready && attempt.exitCode !== null && attempt.exitCode !== 0 &&
        attempt.output.includes(scenario.message) && !attempt.output.includes('listen=')
    } else if (scenario.expect === 'not-ready') {
      pass = !attempt.ready && attempt.exitCode === null && attempt.stopped
    } else {
      pass = attempt.ready && attempt.stopped
    }
    console.log(`${pass ? 'PASS' : 'FAIL'} existing-db.${scenario.name}`)
    if (!pass) {
      failures++
      console.log(`  exit=${attempt.exitCode} ready=${attempt.ready} output=${attempt.output.slice(0, 300)}`)
    }
    if (scenario.checkUnmutated) {
      const afterDump = await dumpDatabase(dbPath)
      const afterTree = { config: snapshotTree(configDir), metadata: snapshotTree(metadataDir) }
      const dumpSame = beforeDump === afterDump
      const treeSame = JSON.stringify(beforeTree) === JSON.stringify(afterTree)
      console.log(`${dumpSame ? 'PASS' : 'FAIL'} existing-db.${scenario.name}.db-unmutated`)
      console.log(`${treeSame ? 'PASS' : 'FAIL'} existing-db.${scenario.name}.trees-unmutated`)
      if (!dumpSame || !treeSame) {
        failures++
        console.log(`  dumpSame=${dumpSame} treeSame=${treeSame}`)
      }
    }
  }
  process.exitCode = failures === 0 ? 0 : 1
}

main().catch((error) => {
  process.stderr.write(`${error.stack || error}\n`)
  process.exitCode = 1
})
