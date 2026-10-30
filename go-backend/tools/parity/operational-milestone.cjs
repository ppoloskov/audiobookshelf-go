#!/usr/bin/env node
'use strict'

// Operational restart/resume and database rollback check on one disposable
// existing-installation fixture. The same Go-written database is opened by
// pinned upstream only after the Go process has stopped.
const fs = require('node:fs')
const path = require('node:path')
const { spawn, spawnSync } = require('node:child_process')
const net = require('node:net')
const sqlite3 = require('sqlite3').verbose()

const ROOT = path.resolve(__dirname, '../../..')
const BASE = '/audiobookshelf'
const sleep = ms => new Promise(resolve => setTimeout(resolve, ms))
function arg(name) { const i = process.argv.indexOf(`--${name}`); return i < 0 ? null : process.argv[i + 1] }
async function freePort() {
  const server = net.createServer()
  await new Promise((resolve, reject) => server.listen(0, '127.0.0.1', e => e ? reject(e) : resolve()))
  const port = server.address().port
  await new Promise(resolve => server.close(resolve))
  return port
}
function start(command, args, env, logFile) {
  const log = fs.createWriteStream(logFile)
  const child = spawn(command, args, { cwd: ROOT, env, stdio: ['ignore', 'pipe', 'pipe'] })
  child.stdout.pipe(log); child.stderr.pipe(log)
  return { child, log }
}
async function stop(server) {
  if (!server) return
  if (server.child.exitCode === null) {
    server.child.kill('SIGINT')
    await Promise.race([new Promise(resolve => server.child.once('exit', resolve)), sleep(15000).then(() => server.child.kill('SIGKILL'))])
  }
  server.log.end()
}
async function ready(server, url, logFile) {
  for (let i = 0; i < 120; i++) {
    if (server.child.exitCode !== null) throw new Error(`server exited; inspect ${logFile}`)
    try { if ((await fetch(`${url}${BASE}/ping`)).ok) return } catch {}
    await sleep(250)
  }
  throw new Error(`server did not become ready; inspect ${logFile}`)
}
async function call(url, route, { method = 'GET', token, body } = {}) {
  const response = await fetch(`${url}${BASE}${route}`, {
    method,
    headers: { ...(token ? { authorization: `Bearer ${token}` } : {}), ...(body === undefined ? {} : { 'content-type': 'application/json' }) },
    body: body === undefined ? undefined : JSON.stringify(body)
  })
  const bytes = Buffer.from(await response.arrayBuffer())
  let value
  try { value = JSON.parse(bytes.toString()) } catch { value = bytes.toString() }
  return { status: response.status, value, bytes }
}
function assert(ok, message) { if (!ok) throw new Error(message) }
async function lengthenFixtureAudio(dbPath, itemId, username, mediaDir) {
  // The stock fixture's two-second clip triggers upstream's short-item
  // auto-finish rule on every seek. A longer equivalent clip lets this
  // acceptance exercise a genuine unfinished resume point.
  fs.mkdirSync(mediaDir, { recursive: true })
  const audioPath = path.join(mediaDir, 'resume-test.m4b')
  const ffmpeg = spawnSync('ffmpeg', ['-hide_banner', '-loglevel', 'error', '-f', 'lavfi', '-i', 'anullsrc=r=44100:cl=mono', '-t', '60', '-c:a', 'aac', '-b:a', '96k', '-metadata', 'title=Deterministic Book', '-metadata', 'artist=A. Reader', '-y', audioPath], { encoding: 'utf8' })
  assert(ffmpeg.status === 0, `could not generate long resume fixture: ${ffmpeg.stderr}`)
  const size = fs.statSync(audioPath).size
  const db = new sqlite3.Database(dbPath)
  const get = (sql, values) => new Promise((resolve, reject) => db.get(sql, values, (error, row) => error ? reject(error) : resolve(row)))
  const run = (sql, values) => new Promise((resolve, reject) => db.run(sql, values, error => error ? reject(error) : resolve()))
  try {
    const item = await get('SELECT mediaId FROM libraryItems WHERE id=?', [itemId])
    assert(item?.mediaId, `fixture item not found: ${itemId}`)
    const book = await get('SELECT audioFiles FROM books WHERE id=?', [item.mediaId])
    const audioFiles = JSON.parse(book.audioFiles)
    assert(audioFiles.length === 1, `expected one fixture audio file, found ${audioFiles.length}`)
    audioFiles[0].duration = 60
    audioFiles[0].bitRate = 96000
    audioFiles[0].metadata.path = audioPath
    audioFiles[0].metadata.size = size
    audioFiles[0].metadata.filename = path.basename(audioPath)
    audioFiles[0].metadata.ext = '.m4b'
    await run('UPDATE books SET duration=?, audioFiles=? WHERE id=?', [60, JSON.stringify(audioFiles), item.mediaId])
    // The stock fixture pre-seeds a completed short-track progress row. Clear
    // only this test user's row so the scenario starts with no resume state.
    await run(`DELETE FROM mediaProgresses WHERE userId=(SELECT id FROM users WHERE username=?) AND json_extract(extraData,'$.libraryItemId')=?`, [username, itemId])
  } finally { await new Promise((resolve, reject) => db.close(error => error ? reject(error) : resolve())) }
  return { path: audioPath, duration: 60, bytes: size }
}

async function main() {
  const manifestPath = path.resolve(arg('fixture') || '')
  const out = path.resolve(arg('out') || path.join(path.dirname(manifestPath), 'operational-milestone'))
  const manifest = JSON.parse(fs.readFileSync(manifestPath, 'utf8'))
  const target = path.join(out, 'installation')
  assert(!fs.existsSync(out), `refusing to reuse existing output directory: ${out}`)
  fs.mkdirSync(out, { recursive: true })
  fs.mkdirSync(target, { recursive: true })
  for (const part of ['config', 'metadata']) fs.cpSync(path.join(manifest.directories.go, part), path.join(target, part), { recursive: true })
  const config = path.join(target, 'config'), metadata = path.join(target, 'metadata')
  const audioFixture = await lengthenFixtureAudio(path.join(config, 'absdatabase.sqlite'), manifest.items.book, manifest.rootCredentials.username, path.join(target, 'media'))
  const bin = path.join(out, 'audiobookshelf-go')
  const build = spawnSync('go', ['build', '-o', bin, './cmd/audiobookshelf-go'], { cwd: path.join(ROOT, 'go-backend'), env: { ...process.env, CGO_ENABLED: '0' }, encoding: 'utf8' })
  assert(build.status === 0, `Go build failed: ${build.stderr}`)
  const evidence = { baseline: manifest.baseline, fixture: manifestPath, database: path.join(config, 'absdatabase.sqlite'), fixtureAdjustment: audioFixture, stages: [] }
  const goEnv = port => ({ ...process.env, CONFIG_PATH: config, METADATA_PATH: metadata, HOST: '127.0.0.1', PORT: String(port), ROUTER_BASE_PATH: BASE, SOURCE: 'docker' })
  let server
  let port = await freePort(), url = `http://127.0.0.1:${port}`
  try {
    server = start(bin, [], goEnv(port), path.join(out, 'go-first.log')); await ready(server, url, path.join(out, 'go-first.log'))
    let login = await call(url, '/login', { method: 'POST', body: manifest.rootCredentials })
    assert(login.status === 200, `Go login failed: ${login.status}`)
    let token = login.value?.user?.accessToken
    assert(token, 'Go login did not return an access token')
    const libraries = await call(url, '/api/libraries', { token })
    assert(libraries.status === 200 && libraries.value?.libraries?.some(x => x.id === manifest.libraries.book), 'Go browse did not show the existing book library')
    const detail = await call(url, `/api/items/${manifest.items.book}`, { token })
    assert(detail.status === 200 && detail.value?.media?.audioFiles?.length, 'Go browse did not show the existing book/audio')
    const detailDuration = detail.value.media.audioFiles[0].duration
    assert(detailDuration === 60, `extended fixture duration not visible to Go: ${JSON.stringify(detail.value.media)}`)
    evidence.stages.push({ name: 'go-login-browse', pass: true, libraryCount: libraries.value.libraries.length, title: detail.value.media.metadata.title, duration: detailDuration })

    const direct = await call(url, `/api/items/${manifest.items.book}/play`, { method: 'POST', token, body: { mediaPlayer: 'operational-milestone', forceDirectPlay: true } })
    assert(direct.status === 200, `direct play failed: ${direct.status}`)
    const directSession = direct.value.session || direct.value
    assert(directSession.id && directSession.audioTracks?.[0]?.contentUrl && directSession.duration === 60, 'direct play did not return a playable 60-second track')
    const directAudio = await fetch(`${url}${BASE}${directSession.audioTracks[0].contentUrl}`, { headers: { authorization: `Bearer ${token}` } })
    assert(directAudio.ok && Number(directAudio.headers.get('content-length') || 0) > 0, `direct audio fetch failed: ${directAudio.status}`)
    await call(url, `/api/session/${directSession.id}/close`, { method: 'POST', token, body: { duration: directSession.duration, currentTime: 1, timeListened: 1 } })
    evidence.stages.push({ name: 'go-direct-audio', pass: true, status: directAudio.status, duration: directSession.duration, contentLength: Number(directAudio.headers.get('content-length')) || null, bytes: directAudio.bytes?.length })

    const transcoded = await call(url, `/api/items/${manifest.items.book}/play`, { method: 'POST', token, body: { mediaPlayer: 'operational-milestone', forceTranscode: true } })
    assert(transcoded.status === 200, `transcoded play start failed: ${transcoded.status}`)
    const transcodeSession = transcoded.value.session || transcoded.value
    assert(transcodeSession.id && transcodeSession.audioTracks?.[0]?.contentUrl, 'transcode start did not return an HLS track')
    const playlistURL = `${url}${BASE}${transcodeSession.audioTracks[0].contentUrl}`
    let playlist
    for (let i = 0; i < 30; i++) { playlist = await fetch(playlistURL, { headers: { authorization: `Bearer ${token}` } }); if (playlist.ok) break; await sleep(300) }
    assert(playlist?.ok, `HLS playlist failed: ${playlist?.status}`)
    const playlistText = await playlist.text()
    const segment = playlistText.split(/\r?\n/).find(line => line && !line.startsWith('#'))
    assert(segment, 'HLS playlist contained no segment')
    let segmentResponse, segmentBytes
    for (let i = 0; i < 40; i++) {
      segmentResponse = await fetch(new URL(segment, playlistURL), { headers: { authorization: `Bearer ${token}` } })
      segmentBytes = Buffer.from(await segmentResponse.arrayBuffer())
      if (segmentResponse.ok && segmentBytes.length > 0) break
      await sleep(250)
    }
    assert(segmentResponse.ok && segmentBytes.length > 0, `transcoded segment failed: ${segmentResponse.status}; playlist=${JSON.stringify(playlistText)}; uri=${segment}`)
    evidence.stages.push({ name: 'go-transcoded-audio', pass: true, playMethod: transcodeSession.playMethod, playlistStatus: playlist.status, segmentStatus: segmentResponse.status, segmentBytes: segmentBytes.length })
    await call(url, `/api/session/${transcodeSession.id}/close`, { method: 'POST', token, body: { duration: transcodeSession.duration, currentTime: 1, timeListened: 1 } })

    // Persist a backward and forward seek as progress updates, then confirm the
    // same row survives a process restart and is offered as the resume point.
    for (const currentTime of [30, 15, 30]) {
      const patch = await call(url, `/api/me/progress/${manifest.items.book}`, { method: 'PATCH', token, body: { duration: 60, currentTime, progress: currentTime / 60, isFinished: false } })
      assert(patch.status === 200, `progress seek to ${currentTime} failed: ${patch.status}`)
    }
    const progress = await call(url, `/api/me/progress/${manifest.items.book}`, { token })
    assert(progress.status === 200 && Math.abs(progress.value.currentTime - 30) < 0.01, `forward seek did not persist: ${JSON.stringify(progress.value)}`)
    evidence.stages.push({ name: 'go-back-forward-progress', pass: true, initialForwardSeekSeconds: 30, backwardSeekSeconds: 15, forwardSeekSeconds: 30, persistedCurrentTime: progress.value.currentTime })
  } finally { await stop(server); server = null }

  port = await freePort(); url = `http://127.0.0.1:${port}`
  try {
    server = start(bin, [], goEnv(port), path.join(out, 'go-restart.log')); await ready(server, url, path.join(out, 'go-restart.log'))
    const login = await call(url, '/login', { method: 'POST', body: manifest.rootCredentials })
    assert(login.status === 200, `Go restart login failed: ${login.status}`)
    const token = login.value?.user?.accessToken
    const progress = await call(url, `/api/me/progress/${manifest.items.book}`, { token })
    assert(progress.status === 200 && Math.abs(progress.value.currentTime - 30) < 0.01, `progress missing after Go restart: ${JSON.stringify(progress.value)}`)
    const resume = await call(url, `/api/items/${manifest.items.book}/play`, { method: 'POST', token, body: { mediaPlayer: 'operational-milestone', forceDirectPlay: true } })
    const session = resume.value?.session || resume.value
    assert(resume.status === 200 && Math.abs(session.currentTime - 30) < 0.01, `resume point not returned after restart: ${JSON.stringify(session)}`)
    await call(url, `/api/session/${session.id}/close`, { method: 'POST', token, body: { duration: 60, currentTime: 30, timeListened: 1 } })
    evidence.stages.push({ name: 'go-restart-resume', pass: true, resumedCurrentTime: session.currentTime })
  } finally { await stop(server); server = null }

  // Pinned Node opens the exact disposable Go-written database, after Go has
  // stopped. Verify login, browse and persisted progress from the rollback.
  port = await freePort(); url = `http://127.0.0.1:${port}`
  const node = process.env.NODE22 || 'mise'
  try {
    server = start(node, ['exec', 'node@22', '--', 'node', path.join(ROOT, 'dist-server/index.js'), '--config', config, '--metadata', metadata, '--port', String(port), '--host', '127.0.0.1', '--source', 'docker'], { ...process.env, NODE_ENV: 'production' }, path.join(out, 'node-reopen.log'))
    await ready(server, url, path.join(out, 'node-reopen.log'))
    const login = await call(url, '/login', { method: 'POST', body: manifest.rootCredentials })
    assert(login.status === 200, `pinned upstream could not login to Go-written DB: ${login.status}`)
    const token = login.value?.user?.accessToken
    const detail = await call(url, `/api/items/${manifest.items.book}`, { token })
    const progress = await call(url, `/api/me/progress/${manifest.items.book}`, { token })
    assert(detail.status === 200 && detail.value?.media?.audioFiles?.length, 'pinned upstream could not browse Go-written book')
    assert(progress.status === 200 && Math.abs(progress.value.currentTime - 30) < 0.01, 'pinned upstream did not read Go-written progress')
    evidence.stages.push({ name: 'pinned-upstream-reopens-go-database', pass: true, title: detail.value.media.metadata.title, currentTime: progress.value.currentTime })
  } finally { await stop(server) }

  evidence.result = evidence.stages.every(stage => stage.pass) ? 'PASS' : 'FAIL'
  fs.writeFileSync(path.join(out, 'operational-milestone-report.json'), `${JSON.stringify(evidence, null, 2)}\n`)
  if (evidence.result === 'PASS') {
    const reportPath = path.join(ROOT, 'go-backend/contracts/parity/operational-playback-milestone-report.json')
    const provenancePath = path.join(ROOT, 'go-backend/contracts/parity/operational-playback-milestone-provenance.json')
    const report = {
      schemaVersion: 1,
      baseline: manifest.baseline,
      result: 'PASS',
      scope: 'On one disposable copy of a representative existing v2.37.2 installation: Go login/browse, direct audio, transcoded HLS segment, backward/forward progress, Go restart/resume, and pinned-upstream reopen of the Go-written database.',
      fixtureAdjustment: 'Replaced only the disposable copy’s two-second AAC clip with a 60-second equivalent and cleared the seeded root-user progress row so the upstream short-item auto-finish rule would not erase the resume point.',
      stages: evidence.stages
    }
    const provenance = {
      schemaVersion: 1,
      baseline: manifest.baseline,
      command: 'node go-backend/tools/parity/operational-milestone.cjs --fixture <existing-fixture-manifest> --out <new-disposable-output-directory>',
      upstream: 'Node v22.23.4; pinned dist-server/index.js',
      go: 'CGO_ENABLED=0 binary built from current go-backend source',
      fixture: 'Independent output copy of the fixture’s Go config and metadata; modified track and database remain under the output directory and are opened serially by Go and Node.',
      limits: ['Direct-play browser controls were separately checked by browser-playback-differential-report.json.', 'Transcoded playback was verified at the HTTP/HLS layer by fetching a generated segment; Chromium HLS decoding was not exercised.', 'Socket reconnect/session recovery and longer-duration periodic browser progress synchronization remain unverified.', 'The fixture’s seeded progress row was cleared; this does not verify resume behavior from a pre-existing completed row.']
    }
    fs.writeFileSync(reportPath, `${JSON.stringify(report, null, 2)}\n`)
    fs.writeFileSync(provenancePath, `${JSON.stringify(provenance, null, 2)}\n`)
  }
  console.log(JSON.stringify(evidence, null, 2))
}
main().catch(error => { process.stderr.write(`${error.stack || error}\n`); process.exitCode = 1 })
