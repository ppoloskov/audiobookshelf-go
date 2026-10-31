#!/usr/bin/env node
'use strict'

const fs = require('node:fs')
const os = require('node:os')
const path = require('node:path')
const { spawnSync } = require('node:child_process')
const { inputDigest } = require('./status.cjs')
const { prepareOracle } = require('./lib/oracle.cjs')

const ROOT = path.resolve(__dirname, '../../..')
const NODE = process.execPath
const workRoot = fs.realpathSync(fs.mkdtempSync(path.join(os.tmpdir(), 'abs-parity-')))
const goBinary = path.join(workRoot, 'audiobookshelf-go')
const env = { ...process.env, SSRF_REQUEST_FILTER_WHITELIST: '127.0.0.1' }

function run(command, args, options = {}) {
  const result = spawnSync(command, args, { cwd: ROOT, env, encoding: 'utf8', ...options })
  if (result.stdout) process.stdout.write(result.stdout)
  if (result.stderr) process.stderr.write(result.stderr)
  if (result.error) throw result.error
  if (result.status !== 0) throw new Error(`${command} ${args.join(' ')} exited ${result.status ?? result.signal}`)
}

function buildGo() {
  const digest = inputDigest()
  run('go', ['build', '-trimpath', '-o', goBinary, './cmd/audiobookshelf-go'], {
    cwd: path.join(ROOT, 'go-backend'),
    env: { ...env, CGO_ENABLED: '0' }
  })
  if (digest !== inputDigest()) throw new Error('executable inputs changed during build; rerun parity')
  return digest
}

function createFixture(name, sourceBackup = false) {
  const dir = path.join(workRoot, name)
  run(NODE, [path.join(__dirname, 'create-fixture.cjs'), dir, ...(sourceBackup ? ['--source-backup'] : [])])
  return path.join(dir, 'fixture-manifest.json')
}

function differential(fixture, name, cases) {
  const builtInputDigest = buildGo()
  const report = path.join(path.dirname(fixture), `${name}-report.json`)
  run(NODE, [path.join(__dirname, 'run-local.cjs'), '--fixture', fixture, '--go-bin', goBinary, '--built-input-digest', builtInputDigest,
    '--out', report, ...(cases ? ['--cases', cases] : [])])
  return report
}

function runSuite(name) {
  const suiteDir = path.join(workRoot, name)
  fs.mkdirSync(suiteDir, { recursive: true })
  switch (name) {
    case 'core':
    case 'equivalence': {
      const fixture = createFixture('core-fixture')
      const report = path.join(path.dirname(fixture), 'core-report.json')
      let gateError = null
      try {
      differential(fixture, 'core')
      const profile = JSON.parse(fs.readFileSync(report, 'utf8')).capabilities.ffmpeg.webp ? 'webp' : 'no-webp'
      let baseline = process.env.PARITY_BASELINE || path.join(ROOT, `go-backend/contracts/parity/baselines/core-schema2-${profile}.json`)
      if (name === 'equivalence' || !fs.existsSync(baseline)) {
        // A second independent fixture proves repeatability when bootstrapping
        // a new encoder profile; it does not overwrite an accepted baseline.
        baseline = differential(createFixture('equivalence-fixture'), 'equivalence')
      }
      run(NODE, [path.join(__dirname, 'compare-reports.cjs'), baseline, report])
      } catch (error) {
        gateError = error.message
        throw error
      } finally {
        if (fs.existsSync(report)) {
          const result = JSON.parse(fs.readFileSync(report, 'utf8'))
          result.gate = { equivalencePassed: gateError === null, error: gateError }
          fs.writeFileSync(report, `${JSON.stringify(result, null, 2)}\n`)
          run(NODE, [path.join(__dirname, 'status.cjs'), '--report', report])
        }
      }
      return
    }
    case 'acl':
      buildGo()
      run(NODE, [path.join(__dirname, 'acl-differential.cjs'), '--go-bin', goBinary, '--work-dir', suiteDir,
        '--out', path.join(suiteDir, 'report.json')])
      return
    case 'cascades': {
      const fixture = createFixture('cascades-fixture')
      buildGo()
      run(NODE, [path.join(__dirname, 'cascades-differential.cjs'), '--fixture', fixture, '--go-bin', goBinary,
        '--out', path.join(suiteDir, 'report.json')])
      return
    }
    case 'oidc': {
      const fixture = createFixture('oidc-fixture')
      buildGo()
      run(NODE, [path.join(__dirname, 'oidc-differential.cjs'), '--fixture', fixture, '--go-bin', goBinary,
        '--work-dir', suiteDir, '--out', path.join(suiteDir, 'report.json'), '--include-web-state-expiry', 'true'])
      return
    }
    case 'socket-soak': {
      const fixture = createFixture('socket-soak-fixture')
      buildGo()
      run(NODE, [path.join(__dirname, 'socket-soak.cjs'), '--fixture', fixture, '--go-bin', goBinary])
      return
    }
    case 'existing-db-matrix': {
      const fixture = createFixture('existing-db-fixture')
      buildGo()
      run(NODE, [path.join(__dirname, 'existing-db-matrix.cjs'), fixture], { env: { ...env, GO_BIN: goBinary } })
      return
    }
    case 'rollback-check': {
      const fixture = createFixture('rollback-fixture')
      differential(fixture, 'rollback')
      run(NODE, [path.join(__dirname, 'rollback-check.cjs'), fixture])
      return
    }
    case 'test-backup-restore': {
      for (const implementation of ['node', 'go']) {
        const fixture = createFixture(`backup-${implementation}-fixture`, true)
        if (implementation === 'go') buildGo()
        const manifest = JSON.parse(fs.readFileSync(fixture, 'utf8'))
        const args = [path.join(__dirname, 'test-backup-restore.cjs'), '--fixture', fixture,
          '--archive', manifest.sourceBackup.path, '--implementation', implementation,
          '--out', path.join(suiteDir, `${implementation}-report.json`)]
        if (implementation === 'go') args.push('--go-bin', goBinary)
        run(NODE, args)
      }
      return
    }
    case 'operational-milestone': {
      const fixture = createFixture('operational-fixture')
      run(NODE, [path.join(__dirname, 'operational-milestone.cjs'), '--fixture', fixture,
        '--out', path.join(suiteDir, 'operational-run'),
        '--report-out', path.join(suiteDir, 'report.json'),
        '--provenance-out', path.join(suiteDir, 'provenance.json')])
      return
    }
    case 'f07-rollin-check': {
      const fixturePath = createFixture('f07-fixture')
      const manifest = JSON.parse(fs.readFileSync(fixturePath, 'utf8'))
      const snapshot = path.dirname(fixturePath)
      for (const part of ['config', 'metadata', 'media']) {
        const source = part === 'media' ? manifest.directories.goMedia : path.join(manifest.directories.go, part)
        fs.cpSync(source, path.join(snapshot, part), { recursive: true })
      }
      fs.writeFileSync(path.join(snapshot, 'fixture-manifest.json'), JSON.stringify(manifest, null, 2))
      run(NODE, [path.join(__dirname, 'f07-rollin-check.cjs'), snapshot])
      return
    }
    default:
      throw new Error(`unknown parity suite: ${name}`)
  }
}

function main() {
  prepareOracle()
  const selection = process.argv[2]
  const full = ['acl', 'cascades', 'oidc', 'socket-soak', 'existing-db-matrix', 'rollback-check',
    'test-backup-restore', 'f07-rollin-check', 'operational-milestone']
  const suites = selection === 'full' ? ['core', ...full] : selection === 'core' ? ['core'] : [selection]
  const failures = []
  console.log(`Parity work directory: ${workRoot}`)
  for (const suite of suites) {
    console.log(`\n== parity suite: ${suite} ==`)
    try {
      runSuite(suite)
    } catch (error) {
      failures.push({ suite, error: error.message })
      process.stderr.write(`suite ${suite} failed: ${error.message}\n`)
    }
  }
  if (failures.length) {
    process.stderr.write(`\n${failures.length} parity suite(s) failed: ${failures.map(failure => failure.suite).join(', ')}\n`)
    process.exitCode = 1
  } else {
    console.log('\nAll requested parity suites passed.')
  }
}

try {
  main()
} catch (error) {
  process.stderr.write(`${error.stack || error}\n`)
  process.exitCode = 1
}
