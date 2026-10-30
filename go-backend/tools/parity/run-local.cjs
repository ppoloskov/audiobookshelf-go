#!/usr/bin/env node
'use strict'

const fs = require('node:fs')
const path = require('node:path')
const net = require('node:net')
const { spawn, spawnSync } = require('node:child_process')
const { setTimeout: delay } = require('node:timers/promises')

const ROOT = path.resolve(__dirname, '../../..')
const PREFIX = '/audiobookshelf'

function parseArgs() {
  const out = {}
  for (let i = 2; i < process.argv.length; i++) {
    const arg = process.argv[i]
    if (arg.startsWith('--')) out[arg.slice(2)] = process.argv[++i]
  }
  if (!out.fixture || !out['go-bin']) throw new Error('Usage: run-local.cjs --fixture <fixture-manifest.json> --go-bin <audiobookshelf-go> [--out <report.json>] [--cases <comma-separated-case-ids>]')
  return out
}

async function freePort() {
  const server = net.createServer()
  await new Promise((resolve, reject) => server.listen(0, '127.0.0.1', (error) => error ? reject(error) : resolve()))
  const port = server.address().port
  await new Promise((resolve, reject) => server.close((error) => error ? reject(error) : resolve()))
  return port
}

function spawnLogged(command, args, options, logFile) {
  const child = spawn(command, args, { ...options, stdio: ['ignore', 'pipe', 'pipe'] })
  const log = fs.createWriteStream(logFile)
  child.stdout.pipe(log)
  child.stderr.pipe(log)
  return { child, log }
}

async function waitReady(processInfo, url, logFile) {
  for (let i = 0; i < 120; i++) {
    if (processInfo.child.exitCode !== null) throw new Error(`server exited before readiness; inspect ${logFile}`)
    try {
      const response = await fetch(`${url}${PREFIX}/ping`)
      if (response.ok) return
    } catch {}
    await delay(250)
  }
  throw new Error(`server did not become ready at ${url}; inspect ${logFile}`)
}

async function stop(processInfo) {
  if (!processInfo) return
  if (processInfo.child.exitCode === null) {
    processInfo.child.kill('SIGINT')
    await Promise.race([
      new Promise((resolve) => processInfo.child.once('exit', resolve)),
      delay(15000).then(() => processInfo.child.kill('SIGKILL'))
    ])
  }
  processInfo.log.end()
}

async function main() {
  const args = parseArgs()
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
      waitReady(nodeProcess, nodeUrl, path.join(runDir, 'reference-server.log')),
      waitReady(goProcess, goUrl, path.join(runDir, 'go-server.log'))
    ])
    const output = path.resolve(args.out || path.join(runDir, 'parity-report.json'))
    const run = spawnSync(process.execPath, [
      path.join(__dirname, 'differential.cjs'), '--node-url', nodeUrl, '--go-url', goUrl,
      '--fixture', fixturePath, '--out', output,
      ...(args.cases ? ['--cases', args.cases] : [])
    ], { cwd: ROOT, encoding: 'utf8' })
    process.stdout.write(run.stdout)
    process.stderr.write(run.stderr)
    if (run.error) throw run.error
    if (run.status !== 0) process.exitCode = run.status ?? 1
  } finally {
    await Promise.all([stop(nodeProcess), stop(goProcess)])
  }
}

main().catch((error) => {
  process.stderr.write(`${error.stack || error}\n`)
  process.exitCode = 1
})
