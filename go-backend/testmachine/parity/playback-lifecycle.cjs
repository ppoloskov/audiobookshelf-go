#!/usr/bin/env node
'use strict'

// Observe actual pinned upstream and Go processes over independent disposable
// fixture copies. No event simulation, source patching or golden updates.
const fs = require('node:fs')
const path = require('node:path')
const { spawnSync } = require('node:child_process')
const sqlite3 = require('sqlite3')
const { io } = require('socket.io-client')
const { freePort, spawnLogged, waitReady, stop } = require('./lib/process.cjs')
const { requestJSON, login } = require('./lib/http.cjs')
const { parseArgs } = require('./lib/args.cjs')
const ROOT = path.resolve(__dirname, '../../..'), BASE = '/audiobookshelf'
const delay = ms => new Promise(resolve => setTimeout(resolve, ms))
const names = ['stream_open', 'stream_progress', 'stream_ready', 'stream_error', 'stream_closed', 'stream_reset']
function check(ok, message) { if (!ok) throw new Error(message) }
function pidAlive(pid) { try { process.kill(pid, 0); return true } catch (error) { return error.code !== 'ESRCH' } }
function eventPayload(event) { return event.args?.[0] }
function eventsFor(events, recipient) { return events.filter(event => event.recipient === recipient) }
function assertEvents(side, mode, evidence) {
  const { events } = evidence
  for (const event of events.filter(event => event.name === 'stream_progress')) {
    const payload = eventPayload(event)
    check(payload?.stream === evidence.play.id && Array.isArray(payload.chunks) && Number.isFinite(payload.numSegments) && typeof payload.percent === 'string', `${side}/${mode}: malformed progress payload`)
    const segments = new Set()
    for (const chunk of payload.chunks) {
      const match = typeof chunk === 'number' && Number.isInteger(chunk) ? [String(chunk), String(chunk)] : typeof chunk === 'string' && chunk.includes('-') ? chunk.match(/^(\d+)-(\d+)$/) : null
      check(match, `${side}/${mode}: malformed chunk range ${JSON.stringify(chunk)}`)
      const first = Number(match[1]), last = Number(match[2] || match[1])
      check(Number.isInteger(first) && Number.isInteger(last) && first >= 0 && last >= first, `${side}/${mode}: invalid chunk range ${JSON.stringify(chunk)}`)
      for (let number = first; number <= last; number++) segments.add(number)
    }
    check(payload.numSegments === 0 ? payload.percent === '0%' : payload.numSegments === Math.ceil(evidence.play.duration / 6) && payload.percent === `${((segments.size * 100) / payload.numSegments).toFixed(2)}%`, `${side}/${mode}: percent/total does not match unique segments (${payload.percent}, ${segments.size}/${payload.numSegments})`)
    check(payload.numSegments >= segments.size, `${side}/${mode}: observed segment count exceeds total`)
  }
  for (const event of events.filter(event => event.name === 'stream_open')) {
    const payload = eventPayload(event)
    check(payload?.id === evidence.play.id && payload.userId && payload.libraryItem?.id && payload.episode === null && payload.segmentLength === 6 && Number.isFinite(payload.startTime) && Number.isFinite(payload.segmentStartNumber) && payload.clientPlaylistUri === `/hls/${evidence.play.id}/output.m3u8` && payload.playlistPath.endsWith(`/streams/${evidence.play.id}/output.m3u8`), `${side}/${mode}: malformed open lifecycle fields`)
  }
  for (const recipient of ['owner-1', 'owner-2']) {
    const names = eventsFor(events, recipient).map(event => event.name)
    check(!events.filter(event => event.recipient === 'other-user' && event.name !== 'stream_reset').length, `${side}/${mode}: owner-only lifecycle event reached unrelated user`)
    const closeEvents = eventsFor(events, recipient).filter(event => event.name === 'stream_closed')
    check(mode === 'shutdown' ? closeEvents.length <= 1 : closeEvents.length === (mode.includes('error') ? 0 : 1), `${side}/${mode}/${recipient}: unexpected stream_closed count ${closeEvents.length}; got ${names}`)
    for (const close of closeEvents) check(close.args[0] === evidence.play.id, `${side}/${mode}/${recipient}: close payload ID mismatch`)
    if (mode === 'complete' || mode === 'aac-retry') {
      check(names.indexOf('stream_progress') >= 0 && names.indexOf('stream_progress') < names.indexOf('stream_open') && names.indexOf('stream_open') < names.indexOf('stream_closed'), `${side}/${mode}/${recipient}: expected progress, open, close ordering; got ${names}`)
      check(!names.includes('stream_ready'), `${side}/${mode}/${recipient}: normal completion must not invent ready`)
    } else if (mode === 'progress') {
      const ownerEvents = eventsFor(events, recipient)
      const closeIndex = ownerEvents.findIndex(event => event.name === 'stream_closed')
      check(ownerEvents.some((event, index) => event.name === 'stream_progress' && isIncrementalProgress(eventPayload(event)) && index < closeIndex), `${side}/${mode}/${recipient}: missing nonzero incremental progress before close`)
      check(names.includes('stream_progress') && names.includes('stream_open') && names.at(-1) === 'stream_closed', `${side}/${mode}/${recipient}: missing active progress/open/close lifecycle; got ${names}`)
    } else if (mode === 'shutdown') {
      check(names.includes('stream_progress') && !names.includes('stream_error'), `${side}/${mode}/${recipient}: active shutdown lifecycle mismatch; got ${names}`)
    } else if (mode === 'runtime-error' || mode === 'start-error' || mode === 'midstream-error') {
      const error = eventsFor(events, recipient).find(event => event.name === 'stream_error')
      check(error, `${side}/${mode}/${recipient}: expected stream_error; got ${names}`)
      check(eventPayload(error)?.id === evidence.play.id && typeof eventPayload(error)?.error === 'string' && eventPayload(error).error.length > 0, `${side}/${mode}/${recipient}: invalid error payload`)
      check(names.indexOf('stream_error') >= 0 && names.indexOf('stream_error') === names.lastIndexOf('stream_error'), `${side}/${mode}/${recipient}: terminal stream_error missing or duplicated`)
    } else if (mode === 'fast-reset') {
      check(names.includes('stream_ready') && names.indexOf('stream_ready') > names.indexOf('stream_progress'), `${side}/${mode}/${recipient}: delayed reset ready missing or out of order; got ${names}`)
    } else if (mode === 'seek-reset') {
      const reset = events.filter(event => event.name === 'stream_reset')
      check(reset.length === 3 && ['owner-1', 'owner-2', 'other-user'].every(label => reset.some(event => event.recipient === label)), `${side}/${mode}: reset must reach each global socket exactly once`)
      const payloads = reset.map(eventPayload)
      check(payloads.every(payload => payload.streamId === evidence.play.id && payload.startTime === 60), `${side}/${mode}: nonzero reset payload mismatch`)
    }
    if (['complete', 'aac-retry', 'progress', 'runtime-error', 'midstream-error', 'fast-reset', 'seek-reset', 'seek-close'].includes(mode)) {
      const first = eventsFor(events, recipient)[0]
      check(first?.name === 'stream_progress' && eventPayload(first)?.chunks.length === 0 && eventPayload(first)?.numSegments === 0 && eventPayload(first)?.percent === '0%', `${side}/${mode}/${recipient}: missing initial zero progress`)
    }
    const opens = eventsFor(events, recipient).filter(event => event.name === 'stream_open')
    check(opens.length <= 1, `${side}/${mode}/${recipient}: stream_open emitted more than once`)
    for (const event of opens) check(eventPayload(event).isTranscodeComplete === false, `${side}/${mode}/${recipient}: stream_open completion flag must remain false at opening`)
  }
  check(JSON.stringify(stableEvents(evidence, 'owner-1')) === JSON.stringify(stableEvents(evidence, 'owner-2')), `${side}/${mode}: duplicate owner sockets received different lifecycle events or payloads`)
}
function isIncrementalProgress(payload) {
  return Array.isArray(payload?.chunks) && payload.chunks.length > 0 && Number.isFinite(payload.numSegments) && payload.numSegments > 0
}
function stableEvents(evidence, recipient, omitProgress = false) {
  return eventsFor(evidence.events, recipient).filter(event => !(omitProgress && event.name === 'stream_progress')).map(event => {
    const payload = eventPayload(event)
    if (event.name === 'stream_progress') return { name: event.name, chunks: payload.chunks, numSegments: payload.numSegments, percent: payload.percent }
    if (event.name === 'stream_open') return { name: event.name, userId: payload.userId, libraryItemId: payload.libraryItem.id, episode: payload.episode, segmentLength: payload.segmentLength, startTime: payload.startTime, segmentStartNumber: payload.segmentStartNumber, isTranscodeComplete: payload.isTranscodeComplete, clientPlaylistUri: payload.clientPlaylistUri.replace(evidence.play.id, '<stream-id>'), playlistSuffix: payload.playlistPath.replace(evidence.play.id, '<stream-id>').slice(payload.playlistPath.indexOf('/streams/')) }
    if (event.name === 'stream_reset') return { name: event.name, streamId: payload.streamId === evidence.play.id, startTime: payload.startTime }
    if (event.name === 'stream_error') return { name: event.name, id: payload.id === evidence.play.id, hasSafeError: typeof payload.error === 'string' && payload.error.length > 0 }
    return { name: event.name }
  })
}
async function database(file, fn) {
  const db = new sqlite3.Database(file)
  const get = (sql, args = []) => new Promise((resolve, reject) => db.get(sql, args, (err, row) => err ? reject(err) : resolve(row)))
  const run = (sql, args = []) => new Promise((resolve, reject) => db.run(sql, args, err => err ? reject(err) : resolve()))
  try { return await fn({ get, run }) } finally { await new Promise((resolve, reject) => db.close(err => err ? reject(err) : resolve())) }
}
async function connect(url, token, recipient, events) {
  const socket = io(url, { path: BASE + '/socket.io', transports: ['websocket'], reconnection: false })
  socket.onAny((name, ...args) => { if (names.includes(name)) events.push({ recipient, name, args, at: Date.now() }) })
  await new Promise((resolve, reject) => {
    const timer = setTimeout(() => reject(new Error(`socket auth timeout ${recipient}`)), 10000)
    socket.on('connect', () => socket.emit('auth', token))
    socket.once('init', () => { clearTimeout(timer); resolve() })
    socket.once('connect_error', err => { clearTimeout(timer); reject(err) })
  })
  return socket
}
async function until(fn, timeout = 15000) {
  const end = Date.now() + timeout
  while (!fn() && Date.now() < end) await delay(50)
  return fn()
}
async function scenario(side, mode, args, manifest, out) {
  const dir = path.join(out, side + '-' + mode)
  fs.mkdirSync(dir)
  for (const part of ['config', 'metadata']) fs.cpSync(path.join(manifest.directories[side], part), path.join(dir, part), { recursive: true })
  const media = path.join(dir, 'audio.m4b'), native = process.env.FFMPEG_PATH || 'ffmpeg'
  const duration = ['progress', 'seek-close', 'fast-reset', 'seek-reset', 'shutdown', 'midstream-error'].includes(mode) ? 240 : 12
  const encode = spawnSync(native, ['-v', 'error', '-f', 'lavfi', '-i', 'sine=frequency=440:sample_rate=44100', '-t', String(duration), '-c:a', 'aac', '-y', media], { encoding: 'utf8' })
  check(encode.status === 0, `fixture encode failed (${encode.status}): ${encode.stderr}`)
    if (mode === 'runtime-error') fs.writeFileSync(media, 'deliberately corrupt disposable audio')
  const dbfile = path.join(dir, 'config/absdatabase.sqlite')
  await database(dbfile, async ({ get, run }) => {
    const { mediaId } = await get('SELECT mediaId FROM libraryItems WHERE id=?', [manifest.items.book])
    const book = await get('SELECT audioFiles FROM books WHERE id=?', [mediaId])
    const files = JSON.parse(book.audioFiles)
    files[0].metadata.path = media
    files[0].metadata.filename = 'audio.m4b'
    files[0].metadata.size = fs.statSync(media).size
    files[0].duration = duration
    await run('UPDATE books SET audioFiles=?,duration=? WHERE id=?', [JSON.stringify(files), duration, mediaId])
    await run('DELETE FROM mediaProgresses')
  })
  let ffmpeg = native
  let evidenceCopyMarker = ''
  let ffmpegPIDPath = ''
  if (mode === 'progress' || mode === 'seek-close' || mode === 'shutdown') {
    // Throttle real FFmpeg concat input; capability probes pass unchanged.
    ffmpeg = path.join(dir, 'ffmpeg-rate')
    if (mode === 'shutdown') {
      ffmpegPIDPath = path.join(dir, 'ffmpeg-running.pid')
      fs.writeFileSync(ffmpeg, `#!/bin/sh\nfor arg do\n if [ "$arg" = concat ]; then echo $$ > "${ffmpegPIDPath}"; exec "${native}" -readrate 20 "$@"; fi\ndone\nexec "${native}" "$@"\n`, { mode: 0o700 })
    } else fs.writeFileSync(ffmpeg, '#!/bin/sh\nfor arg do\n if [ "$arg" = concat ]; then exec "' + native + '" -readrate 20 "$@"; fi\ndone\nexec "' + native + '" "$@"\n', { mode: 0o700 })
  }
  if (mode === 'midstream-error') {
    ffmpegPIDPath = path.join(dir, 'ffmpeg-running.pid')
    ffmpeg = path.join(dir, 'ffmpeg-midstream-stop')
    fs.writeFileSync(ffmpeg, `#!/bin/sh\nfor arg do\n if [ "$arg" = concat ]; then echo $$ > "${ffmpegPIDPath}"; exec "${native}" -readrate 20 "$@"; fi\ndone\nexec "${native}" "$@"\n`, { mode: 0o700 })
  }
  if (mode === 'aac-retry') {
    const marker = path.join(dir, 'copy-failure-injected')
    ffmpeg = path.join(dir, 'ffmpeg-inject-copy-failure')
    fs.writeFileSync(ffmpeg, `#!/bin/sh\nfor arg do\n if [ "$arg" = copy ]; then touch "${marker}"; exit 1; fi\ndone\nexec "${native}" "$@"\n`, { mode: 0o700 })
    evidenceCopyMarker = marker
  }
  if (mode === 'start-error') {
    ffmpeg = path.join(dir, 'ffmpeg-no-interpreter')
    fs.writeFileSync(ffmpeg, '#!/path/that/does/not/exist\n', { mode: 0o700 })
  }
  const port = await freePort(), url = `http://127.0.0.1:${port}`, log = path.join(dir, 'server.log')
  const env = { ...process.env, NODE_ENV: 'production', CONFIG_PATH: path.join(dir, 'config'), METADATA_PATH: path.join(dir, 'metadata'), HOST: '127.0.0.1', PORT: String(port), ROUTER_BASE_PATH: BASE, SOURCE: 'docker', FFMPEG_PATH: ffmpeg }
  let proc
  const sockets = [], events = []
  const evidence = { side, mode, directory: dir, fixtureEncodeExitCode: encode.status, duration, events }
  try {
    proc = side === 'node'
      ? spawnLogged('mise', ['exec', 'node@22', '--', 'node', path.join(ROOT, 'dist-server/index.js'), '--config', env.CONFIG_PATH, '--metadata', env.METADATA_PATH, '--port', String(port), '--host', '127.0.0.1', '--source', 'docker'], { cwd: ROOT, env }, log)
      : spawnLogged(path.resolve(args['go-bin']), [], { cwd: ROOT, env }, log)
    await waitReady(proc, url, { pathPrefix: BASE, logPath: log })
    const root = await login(url, manifest.rootCredentials, { pathPrefix: BASE })
    const reader = await login(url, manifest.readerCredentials, { pathPrefix: BASE })
    for (const [label, token] of [['owner-1', root.token], ['owner-2', root.token], ['other-user', reader.token]]) sockets.push(await connect(url, token, label, events))
    const call = (route, options = {}) => requestJSON(url, route, { pathPrefix: BASE, token: root.token, timeoutMs: 15000, ...options })
    if (mode === 'seek-close' || mode === 'fast-reset' || mode === 'seek-reset') await call('/api/me/progress/' + manifest.items.book, { method: 'PATCH', body: { duration, currentTime: 120, progress: .5, isFinished: false } })
    const play = await call('/api/items/' + manifest.items.book + '/play', { method: 'POST', body: { forceTranscode: true, mediaPlayer: 'playback-lifecycle-acceptance' } })
    const session = play.value?.session || play.value
    evidence.play = { status: play.status, id: session.id, playMethod: session.playMethod, duration: session.duration, startTime: session.startTime, currentTime: session.currentTime, tracks: session.audioTracks }
    check(play.status === 200 && session.id, `play failed: ${JSON.stringify(evidence.play)}`)
    check(Array.isArray(evidence.play.tracks) && evidence.play.tracks.every(track => track.contentUrl === `/hls/${session.id}/output.m3u8`), `${side}/${mode}: play response HLS URL does not reference its stream`)
    const streamDir = path.join(dir, 'metadata/streams', session.id)
    if (mode === 'shutdown') {
      evidence.activeBeforeShutdown = await until(() => fs.existsSync(path.join(streamDir, 'output-1.ts')), 30000)
      evidence.transcodePIDObserved = await until(() => fs.existsSync(ffmpegPIDPath), 5000)
      check(evidence.activeBeforeShutdown && evidence.transcodePIDObserved, `${side}/${mode}: real transcode did not become active before shutdown`)
      evidence.transcodePID = Number(fs.readFileSync(ffmpegPIDPath, 'utf8').trim())
      evidence.filesBeforeShutdown = fs.readdirSync(streamDir).sort().map(name => [name, fs.statSync(path.join(streamDir, name)).size])
      await stop(proc)
      await delay(1500)
      evidence.serverExitCode = proc.child.exitCode
      evidence.serverSignal = proc.child.signalCode
      evidence.filesAfterShutdown = fs.existsSync(streamDir) ? fs.readdirSync(streamDir).sort().map(name => [name, fs.statSync(path.join(streamDir, name)).size]) : []
      evidence.transcodeAliveAfterShutdown = pidAlive(evidence.transcodePID)
      check(evidence.serverExitCode === 0, `${side}/${mode}: server did not stop cleanly (${evidence.serverExitCode}, ${evidence.serverSignal})`)
      if (side === 'go') check(!evidence.transcodeAliveAfterShutdown && evidence.filesAfterShutdown.length === 0, `${side}/${mode}: server did not reap FFmpeg and remove the stream directory`)
      check(evidence.filesAfterShutdown.length === 0 || JSON.stringify(evidence.filesAfterShutdown) === JSON.stringify(evidence.filesBeforeShutdown), `${side}/${mode}: stream output continued changing after server shutdown`)
      if (side === 'node' && evidence.transcodeAliveAfterShutdown) {
        evidence.sourceLeavesFFmpegAfterShutdown = true
        process.kill(evidence.transcodePID, 'SIGTERM')
        evidence.harnessReapedSourceFFmpeg = await until(() => !pidAlive(evidence.transcodePID), 5000)
      }
    }
    else if (mode === 'seek-close' || mode === 'fast-reset' || mode === 'seek-reset') {
      if (mode === 'fast-reset' || mode === 'seek-reset') {
        evidence.initialOpenObserved = await until(() => events.some(event => event.name === 'stream_open'), 15000)
        check(evidence.initialOpenObserved, `${side}/${mode}: initial stream_open was not observed before reset`)
      }
      evidence.segmentBeforeSeek = await until(() => fs.existsSync(path.join(streamDir, 'output-16.ts')))
      const segment = mode === 'seek-reset' ? 10 : 0
      evidence.seekRequestedAt = Date.now()
      evidence.seek = { segment, status: (await call('/hls/' + session.id + `/output-${segment}.ts`)).status }
      await delay(mode === 'fast-reset' || mode === 'seek-reset' ? 4000 : 1200)
    } else if (mode === 'midstream-error') {
      evidence.transcodeStarted = await until(() => fs.existsSync(ffmpegPIDPath) && fs.existsSync(path.join(streamDir, 'output-1.ts')), 30000)
      check(evidence.transcodeStarted, `${side}/${mode}: real FFmpeg did not start and produce output before termination`)
      evidence.transcodePID = Number(fs.readFileSync(ffmpegPIDPath, 'utf8').trim())
      process.kill(evidence.transcodePID, 'SIGTERM')
      evidence.errorObserved = await until(() => events.some(event => event.name === 'stream_error'), 18000)
      evidence.directoryAfterError = fs.existsSync(streamDir)
      evidence.hlsAfterError = (await call('/hls/' + session.id + '/output.m3u8')).status
    } else if (mode.includes('error')) {
      evidence.errorObserved = await until(() => events.some(e => e.name === 'stream_error'), 18000)
      evidence.directoryAfterError = fs.existsSync(streamDir)
      evidence.hlsAfterError = (await call('/hls/' + session.id + '/output.m3u8')).status
    } else if (mode !== 'aac-retry') {
      evidence.completed = await until(() => {
        const file = path.join(streamDir, 'final-output.m3u8')
        return fs.existsSync(file) && fs.readFileSync(file, 'utf8').includes('#EXT-X-ENDLIST')
      }, 25000)
      await delay(2300)
      const playlist = await call('/hls/' + session.id + '/output.m3u8'), segment = await call('/hls/' + session.id + '/output-0.ts')
      evidence.media = { playlistStatus: playlist.status, segmentStatus: segment.status, segmentBytes: segment.bytes.length, files: fs.existsSync(streamDir) ? fs.readdirSync(streamDir).sort() : [] }
    }
    if (mode === 'aac-retry') {
      evidence.copyFailureInjected = await until(() => fs.existsSync(evidenceCopyMarker), 18000)
      check(evidence.copyFailureInjected, `${side}/${mode}: injected copy failure did not reach real FFmpeg transcode`)
      evidence.completed = await until(() => {
        const file = path.join(streamDir, 'final-output.m3u8')
        return fs.existsSync(file) && fs.readFileSync(file, 'utf8').includes('#EXT-X-ENDLIST')
      }, 25000)
      const playlist = await call('/hls/' + session.id + '/output.m3u8'), segment = await call('/hls/' + session.id + '/output-0.ts')
      evidence.media = { playlistStatus: playlist.status, segmentStatus: segment.status, segmentBytes: segment.bytes.length, files: fs.existsSync(streamDir) ? fs.readdirSync(streamDir).sort() : [] }
      check(evidence.completed && evidence.media.playlistStatus === 200 && evidence.media.segmentStatus === 200 && evidence.media.segmentBytes > 0, `${side}/${mode}: AAC retry did not finish and serve real media`)
    } else if (mode.includes('error')) {
      check(evidence.errorObserved, `${side}/${mode}: stream_error was not observed within 18 seconds`)
      check(!evidence.directoryAfterError && evidence.hlsAfterError === 404, `${side}/${mode}: failed stream files/HLS route were not cleaned up`)
    }
    if (mode === 'complete') check(evidence.completed && evidence.media.playlistStatus === 200 && evidence.media.segmentStatus === 200 && evidence.media.segmentBytes > 0, `${side}/${mode}: real transcode did not complete and serve media`)
    if (mode === 'seek-reset') check(evidence.seek.status === 404, `${side}/${mode}: reset probe should request missing segment`)
    if (mode !== 'shutdown') {
      const close = await call('/api/session/' + session.id + '/close', { method: 'POST', body: {} })
      await delay(500)
      evidence.close = { status: close.status, body: close.value, directoryExists: fs.existsSync(streamDir), hlsStatus: (await call('/hls/' + session.id + '/output.m3u8')).status }
      check(evidence.close.status === 200 && evidence.close.body === 'OK' && !evidence.close.directoryExists && evidence.close.hlsStatus === 404, `${side}/${mode}: explicit close did not return OK and clean files/HLS`)
    }
    evidence.persistence = await database(dbfile, async ({ get }) => ({ sessions: await get('SELECT count(*) AS count FROM playbackSessions WHERE id=?', [session.id]), progress: await get('SELECT count(*) AS count FROM mediaProgresses') }))
    check(evidence.persistence.sessions.count === 0, `${side}/${mode}: unlistened session was unexpectedly persisted`)
  } catch (error) { evidence.failure = error.stack || String(error); evidence.failureCategory = /fixture encode failed|spawn .*ENOENT/.test(String(error)) ? 'environment' : /socket auth|did not become ready|server exited before readiness/.test(String(error)) ? 'harness-or-environment' : 'product-or-assertion' }
  finally { sockets.forEach(socket => socket.disconnect()); await stop(proc); evidence.serverExitCode = proc?.child.exitCode; evidence.serverSignal = proc?.child.signalCode }
  return evidence
}
async function main() {
  const args = parseArgs()
  check(args.fixture && args['go-bin'] && args.out, 'Usage: --fixture manifest --go-bin binary --out new-directory [--modes complete,progress,runtime-error,startup-error,seek-close]')
  const out = path.resolve(args.out)
  check(!fs.existsSync(out), 'output directory must be new')
  fs.mkdirSync(out, { recursive: true })
  const manifest = JSON.parse(fs.readFileSync(args.fixture))
  const report = { baseline: manifest.baseline, fixture: args.fixture, scenarios: [], skips: [], result: 'NOT_ACCEPTED' }
  for (const mode of (args.modes || 'complete,progress,runtime-error,start-error,aac-retry,fast-reset,seek-reset,seek-close,shutdown,midstream-error').split(',')) {
    const node = await scenario('node', mode, args, manifest, out), go = await scenario('go', mode, args, manifest, out)
    if (!node.failure) { try { assertEvents('node', mode, node) } catch (error) { node.failure = String(error); node.failureCategory = 'product-or-assertion' } }
    if (!go.failure) { try { assertEvents('go', mode, go) } catch (error) { go.failure = String(error); go.failureCategory = 'product-or-assertion' } }
    if (!node.failure && !go.failure) {
      try {
        const normalizeTracks = side => side.play.tracks.map(track => ({ ...track, contentUrl: track.contentUrl.replace(side.play.id, '<stream-id>') }))
        check(node.play.status === go.play.status && node.play.playMethod === go.play.playMethod && node.play.duration === go.play.duration && node.play.startTime === go.play.startTime && node.play.currentTime === go.play.currentTime && JSON.stringify(normalizeTracks(node)) === JSON.stringify(normalizeTracks(go)), `${mode}: TS/Go play response contract differs`)
        if (mode !== 'shutdown') check(node.close.status === go.close.status && node.close.body === go.close.body, `${mode}: TS/Go explicit close result differs`)
        check(node.persistence.sessions.count === go.persistence.sessions.count && node.persistence.progress.count === go.persistence.progress.count, `${mode}: TS/Go persistence result differs`)
        for (const recipient of ['owner-1', 'owner-2']) {
          const expected = stableEvents(node, recipient, mode === 'fast-reset'), actual = stableEvents(go, recipient, mode === 'fast-reset')
          // Long-running progress cadence is scheduler-dependent; completion and fast reset have stable fixture states.
          if (mode === 'complete' || mode === 'aac-retry' || mode === 'fast-reset' || mode === 'start-error') check(JSON.stringify(actual) === JSON.stringify(expected), `${mode}/${recipient}: TS/Go lifecycle payload/order differ: ${JSON.stringify(expected)} != ${JSON.stringify(actual)}`)
        }
        if (mode === 'seek-reset') {
          const resetEvents = side => side.events.filter(event => event.name === 'stream_reset').map(event => ({ recipient: event.recipient, startTime: event.args[0].startTime, streamId: event.args[0].streamId === side.play.id })).sort((a, b) => a.recipient.localeCompare(b.recipient))
          check(JSON.stringify(resetEvents(node)) === JSON.stringify(resetEvents(go)), `${mode}: TS/Go global reset recipients or payload differ`)
          const terminalOrder = side => ['owner-1', 'owner-2', 'other-user'].map(recipient => [recipient, side.events.filter(event => event.recipient === recipient && ['stream_open', 'stream_reset', 'stream_ready', 'stream_error', 'stream_closed'].includes(event.name)).map(event => event.name)])
          check(JSON.stringify(terminalOrder(node)) === JSON.stringify(terminalOrder(go)), `${mode}: TS/Go per-recipient lifecycle ordering differs`)
        }
      } catch (error) { go.failure = String(error); go.failureCategory = 'product-or-assertion' }
    }
    report.scenarios.push({ mode, node, go })
    fs.writeFileSync(path.join(out, 'report.json'), JSON.stringify(report, null, 2) + '\n')
    console.log(`${mode}: node=${node.failure || node.events.map(e => e.name).join(',')} go=${go.failure || go.events.map(e => e.name).join(',')}`)
  }
  const failures = report.scenarios.flatMap(s => [s.node, s.go]).filter(side => side.failure)
  report.result = failures.length ? 'FAILED' : 'PASSED'
  report.failures = failures.map(({ side, mode, failureCategory, failure }) => ({ side, mode, failureCategory, failure }))
  fs.writeFileSync(path.join(out, 'report.json'), JSON.stringify(report, null, 2) + '\n')
  if (failures.length) process.exitCode = 1
}
if (require.main === module) main().catch(err => { console.error(err); process.exitCode = 1 })
module.exports = { assertEvents }
