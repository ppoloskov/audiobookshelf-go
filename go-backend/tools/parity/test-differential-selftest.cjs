'use strict'

const assert = require('node:assert/strict')
const { once } = require('node:events')
const fs = require('node:fs')
const http = require('node:http')
const os = require('node:os')
const path = require('node:path')
const { spawn } = require('node:child_process')
const test = require('node:test')

const differential = path.join(__dirname, 'differential.cjs')
const jwt = 'eyJhbGciOiJub25lIn0.eyJzdWIiOiJyb290In0.signature'

async function startStub(change = () => {}) {
  const server = http.createServer((request, response) => {
    request.resume()
    let status = 200
    let contentType = 'application/json'
    const body = {
      user: { accessToken: 'access-token-long', refreshToken: 'refresh-token-long', token: jwt, lastSeen: 1 },
      serverSettings: { backupPath: '/fixture/metadata/backups' }
    }
    change({ request, body, setStatus: value => { status = value }, setContentType: value => { contentType = value } })
    response.writeHead(status, { 'content-type': contentType })
    response.end(JSON.stringify(body))
  })
  server.listen(0, '127.0.0.1')
  await once(server, 'listening')
  return { server, url: `http://127.0.0.1:${server.address().port}` }
}

async function runPair(changeRight, cases = 'login') {
  const left = await startStub()
  const right = await startStub(changeRight)
  const directory = fs.mkdtempSync(path.join(os.tmpdir(), 'abs-differential-selftest-'))
  const fixturePath = path.join(directory, 'fixture.json')
  fs.writeFileSync(fixturePath, JSON.stringify({
    baseline: 'selftest', userId: 'root', rootCredentials: { username: 'root', password: 'secret' },
    readerCredentials: { username: 'reader', password: 'secret' },
    directories: { node: directory, go: directory, nodeMedia: directory, goMedia: directory },
    libraries: { book: 'library' }, items: { book: 'book', audioFile: '1' }
  }))
  const output = path.join(directory, 'report.json')
  const child = spawn(process.execPath, [differential, '--node-url', left.url, '--go-url', right.url,
    '--fixture', fixturePath, '--out', output, '--cases', cases], { stdio: ['ignore', 'pipe', 'pipe'] })
  let stdout = ''
  let stderr = ''
  child.stdout.setEncoding('utf8').on('data', chunk => { stdout += chunk })
  child.stderr.setEncoding('utf8').on('data', chunk => { stderr += chunk })
  const [status, signal] = await once(child, 'close')
  const result = { status: signal ? null : status, signal, stdout, stderr }
  const report = fs.existsSync(output) ? JSON.parse(fs.readFileSync(output, 'utf8')) : null
  await Promise.all([new Promise(resolve => left.server.close(resolve)), new Promise(resolve => right.server.close(resolve))])
  fs.rmSync(directory, { recursive: true, force: true })
  return { ...result, report }
}

test('identical HTTP stubs execute and pass at least one case', async () => {
  const result = await runPair(() => {})
  assert.equal(result.status, 0, result.stderr)
  assert.ok(result.report.summary.executed >= 1)
  assert.equal(result.report.summary.failed, 0)
})

test('different response body fails', async () => {
  const result = await runPair(({ body }) => { body.serverSettings.flag = true })
  assert.notEqual(result.status, 0)
  assert.equal(result.report.summary.failed, 1)
})

test('different status fails', async () => {
  const result = await runPair(({ setStatus }) => setStatus(500))
  assert.notEqual(result.status, 0)
  assert.equal(result.report.summary.failed, 1)
})

test('different compared header fails', async () => {
  const result = await runPair(({ setContentType }) => setContentType('application/json; charset=utf-8'))
  assert.notEqual(result.status, 0)
  assert.equal(result.report.summary.failed, 1)
})

test('a selected case that never reaches an execution point fails with zero cases', async () => {
  const result = await runPair(() => {}, 'playback-session')
  assert.notEqual(result.status, 0)
  assert.match(result.stderr, /0 cases executed/)
  assert.equal(result.report.summary.executed, 0)
})

test('an unknown selected case ID is rejected', async () => {
  const result = await runPair(() => {}, 'does-not-exist')
  assert.notEqual(result.status, 0)
  assert.match(result.stderr, /unknown case ID.*does-not-exist/)
  assert.equal(result.report, null)
})
