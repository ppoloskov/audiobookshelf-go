'use strict'

const fs = require('node:fs')
const net = require('node:net')
const { spawn } = require('node:child_process')
const { setTimeout: delay } = require('node:timers/promises')

async function freePort(host = '127.0.0.1') {
  const server = net.createServer()
  await new Promise((resolve, reject) => server.listen(0, host, error => error ? reject(error) : resolve()))
  const { port } = server.address()
  await new Promise((resolve, reject) => server.close(error => error ? reject(error) : resolve()))
  return port
}

function spawnLogged(command, args, options, logPath) {
  const child = spawn(command, args, { ...options, stdio: ['ignore', 'pipe', 'pipe'] })
  const log = fs.createWriteStream(logPath)
  child.stdout.pipe(log)
  child.stderr.pipe(log)
  return { child, log }
}

async function waitReady(processInfo, url, { pathPrefix = '', logPath, attempts = 120, intervalMs = 250, exitMessage, timeoutMessage } = {}) {
  const child = processInfo.child || processInfo
  for (let i = 0; i < attempts; i++) {
    if (child.exitCode !== null) throw new Error(exitMessage || `server exited before readiness; inspect ${logPath}`)
    try {
      if ((await fetch(`${url}${pathPrefix}/ping`)).ok) return
    } catch {}
    await delay(intervalMs)
  }
  throw new Error(timeoutMessage || `server did not become ready at ${url}; inspect ${logPath}`)
}

async function stop(processInfo, timeoutMs = 15000) {
  if (!processInfo) return
  const child = processInfo.child || processInfo
  if (child.exitCode === null) {
    child.kill('SIGINT')
    await Promise.race([
      new Promise(resolve => child.once('exit', resolve)),
      delay(timeoutMs).then(() => child.kill('SIGKILL'))
    ])
  }
  processInfo.log?.end()
}

module.exports = { freePort, spawnLogged, waitReady, stop }
