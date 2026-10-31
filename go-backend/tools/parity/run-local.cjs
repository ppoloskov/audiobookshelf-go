#!/usr/bin/env node
'use strict'

const fs = require('node:fs')
const path = require('node:path')
const { spawnSync } = require('node:child_process')
const { freePort, spawnLogged, waitReady, stop } = require('./lib/process.cjs')
const { parseArgs } = require('./lib/args.cjs')
const { inputDigest } = require('./status.cjs')
const { prepareOracle } = require('./lib/oracle.cjs')

const ROOT = path.resolve(__dirname, '../../..')
const PREFIX = '/audiobookshelf'

function validatedArgs() {
  const out = parseArgs()
  if (!out.fixture || !out['go-bin']) throw new Error('Usage: run-local.cjs --fixture <fixture-manifest.json> --go-bin <audiobookshelf-go> [--out <report.json>] [--cases <comma-separated-case-ids>]')
  return out
}

async function main() {
  const args = validatedArgs()
  prepareOracle()
  const startedInputDigest = inputDigest()
  if (args['built-input-digest'] && args['built-input-digest'] !== startedInputDigest) throw new Error('source inputs no longer match the freshly built binary; rerun parity')
  const fixturePath = path.resolve(args.fixture)
  const fixture = JSON.parse(fs.readFileSync(fixturePath, 'utf8'))
  const nodeConfig = path.join(fixture.directories.node, 'config')
  const nodeMetadata = path.join(fixture.directories.node, 'metadata')
  const goConfig = path.join(fixture.directories.go, 'config')
  const goMetadata = path.join(fixture.directories.go, 'metadata')
  const nodePort = await freePort()
  const goPort = await freePort()
  const runDir = path.dirname(fixturePath)
  let nodeProcess
  let goProcess
  try {
    nodeProcess = spawnLogged(process.execPath, [
      path.join(ROOT, 'dist-server/index.js'), '--config', nodeConfig, '--metadata', nodeMetadata,
      '--port', String(nodePort), '--host', '127.0.0.1', '--source', 'docker'
    ], { cwd: ROOT, env: { ...process.env, NODE_ENV: 'production' } }, path.join(runDir, 'reference-server.log'))
    goProcess = spawnLogged(path.resolve(args['go-bin']), [], {
      cwd: ROOT,
      env: {
        ...process.env, CONFIG_PATH: goConfig, METADATA_PATH: goMetadata,
        HOST: '127.0.0.1', PORT: String(goPort), ROUTER_BASE_PATH: PREFIX, SOURCE: 'docker'
      }
    }, path.join(runDir, 'go-server.log'))
    const nodeUrl = `http://127.0.0.1:${nodePort}`
    const goUrl = `http://127.0.0.1:${goPort}`
    await Promise.all([
      waitReady(nodeProcess, nodeUrl, { pathPrefix: PREFIX, logPath: path.join(runDir, 'reference-server.log') }),
      waitReady(goProcess, goUrl, { pathPrefix: PREFIX, logPath: path.join(runDir, 'go-server.log') })
    ])
    const output = path.resolve(args.out || path.join(runDir, 'parity-report.json'))
    fs.rmSync(output, { force: true })
    const run = spawnSync(process.execPath, [
      path.join(__dirname, 'differential.cjs'), '--node-url', nodeUrl, '--go-url', goUrl,
      '--fixture', fixturePath, '--out', output,
      ...(args.cases ? ['--cases', args.cases] : [])
    ], { cwd: ROOT, encoding: 'utf8' })
    process.stdout.write(run.stdout)
    process.stderr.write(run.stderr)
    if (run.error) throw run.error
    if (fs.existsSync(output)) {
      const report = JSON.parse(fs.readFileSync(output, 'utf8'))
      report.verification = { inputDigest: startedInputDigest, completedInputDigest: inputDigest() }
      fs.writeFileSync(output, `${JSON.stringify(report, null, 2)}\n`)
      if (report.verification.inputDigest !== report.verification.completedInputDigest) throw new Error('executable inputs changed during parity; rerun on stable source')
    }
    if (run.status !== 0) process.exitCode = run.status ?? 1
  } finally {
    await Promise.all([stop(nodeProcess), stop(goProcess)])
  }
}

main().catch((error) => {
  process.stderr.write(`${error.stack || error}\n`)
  process.exitCode = 1
})
