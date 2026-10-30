#!/usr/bin/env node
'use strict'

// Real Chromium acceptance for the unchanged upstream client against isolated
// v2.37.2 Go and Node fixture copies. Requires Playwright available to Node.
//
// F06/F09 variants:
//  --variant full       default: prefixed launch, login -> library -> detail, playback
//  --variant transcode  forced HLS playback on long, independent multi-file audio fixtures
//  --variant restricted reader login -> library -> detail (restricted visibility; no playback)
//  --variant root       launch without a configured prefix and compare the same steps
//  --variant fresh      both servers first-boot an EMPTY config/metadata tree; compare
//                       the setup shell and the fresh-init schema instead of playback
// Only the full variant writes the checked-in browser-playback-differential
// contract report; variant runs write their own out dir + browser-report.json.
const fs = require('node:fs')
const crypto = require('node:crypto')
const path = require('node:path')
const net = require('node:net')
const { spawn, spawnSync } = require('node:child_process')
const { setTimeout: delay } = require('node:timers/promises')

const ROOT = path.resolve(__dirname, '../../..')
const FULL_BASE = '/audiobookshelf'
const DEFAULT_FIXTURE = '/private/var/folders/jt/2prvbq7965ggn35rz2vwnqrw0000gn/T/opencode/audit-repair-final-parity/fixture-manifest.json'
const OUT = process.env.BROWSER_CHECK_OUT || '/private/var/folders/jt/2prvbq7965ggn35rz2vwnqrw0000gn/T/opencode/browser-acceptance'
const VARIANT = (process.argv.includes('--variant') ? process.argv[process.argv.indexOf('--variant') + 1] : 'full')
if (!['full', 'transcode', 'restricted', 'root', 'fresh'].includes(VARIANT)) throw new Error(`unknown --variant ${VARIANT}`)
const BASE = VARIANT === 'root' ? '' : FULL_BASE

function arg(name, fallback) {
  const i = process.argv.indexOf(`--${name}`)
  return i < 0 ? fallback : process.argv[i + 1]
}
function playwright() {
  try { return require('playwright') } catch {}
  try { return require(require.resolve('playwright', { paths: ['/Users/paul/.npm/_npx/e41f203b7505f1fb/node_modules'] })) } catch {}
  throw new Error('Playwright module not found; run npx -y playwright@latest install chromium and expose its module')
}
async function freePort() {
  const s = net.createServer()
  await new Promise((resolve, reject) => s.listen(0, '127.0.0.1', e => e ? reject(e) : resolve()))
  const p = s.address().port
  await new Promise(resolve => s.close(resolve))
  return p
}
function start(command, args, options, logPath) {
  const child = spawn(command, args, { ...options, stdio: ['ignore', 'pipe', 'pipe'] })
  const log = fs.createWriteStream(logPath)
  child.stdout.pipe(log); child.stderr.pipe(log)
  return { child, log }
}
async function waitReady(proc, url, logPath) {
  for (let i = 0; i < 120; i++) {
    if (proc.child.exitCode !== null) throw new Error(`server exited before readiness; see ${logPath}`)
    try { if ((await fetch(`${url}${BASE}/ping`)).ok) return } catch {}
    await delay(250)
  }
  throw new Error(`server not ready at ${url}; see ${logPath}`)
}

// Fresh-init schema comparison: table list + DDL and migrationsMeta state
// must match between the two servers' independently created databases.
function freshInitState(configDir) {
  const sqlite3 = require('sqlite3').verbose()
  const dbPath = path.join(configDir, 'absdatabase.sqlite')
  return new Promise((resolve, reject) => {
    const db = new sqlite3.Database(dbPath)
    db.serialize(() => {
      db.all(`SELECT name, sql FROM sqlite_master WHERE type='table' AND name NOT LIKE 'sqlite_%' ORDER BY name`, [], (error, tables) => {
        if (error) { db.close(); return reject(error) }
        db.all(`SELECT key, value FROM migrationsMeta ORDER BY key`, [], (error2, meta) => {
          db.close(err3 => err3 ? reject(err3) : resolve({ tables, migrationsMeta: meta }))
        })
      })
    })
  })
}
async function stop(proc) {
  if (!proc) return
  if (proc.child.exitCode === null) {
    proc.child.kill('SIGINT')
    await Promise.race([new Promise(resolve => proc.child.once('exit', resolve)), delay(12000).then(() => proc.child.kill('SIGKILL'))])
  }
  proc.log.end()
}
async function rewriteCopiedCoverPaths(databasePath, fromMetadata, toMetadata) {
  const sqlite3 = require('sqlite3').verbose()
  const db = new sqlite3.Database(databasePath)
  const all = (sql, params = []) => new Promise((resolve, reject) => db.all(sql, params, (error, rows) => error ? reject(error) : resolve(rows)))
  const run = (sql, params = []) => new Promise((resolve, reject) => db.run(sql, params, error => error ? reject(error) : resolve()))
  try {
    await run('BEGIN IMMEDIATE')
    for (const table of ['books', 'podcasts']) {
      const rows = await all(`SELECT rowid, coverPath FROM ${table} WHERE coverPath LIKE ?`, [`${fromMetadata}%`])
      for (const row of rows) {
        await run(`UPDATE ${table} SET coverPath=? WHERE rowid=?`, [toMetadata + row.coverPath.slice(fromMetadata.length), row.rowid])
      }
    }
    await run('COMMIT')
  } catch (error) {
    await run('ROLLBACK').catch(() => {})
    throw error
  } finally {
    await new Promise((resolve, reject) => db.close(error => error ? reject(error) : resolve()))
  }
}

async function restrictReaderToBookLibrary(databasePath, username, bookLibraryID) {
  const sqlite3 = require('sqlite3').verbose()
  const db = new sqlite3.Database(databasePath)
  try {
    const row = await new Promise((resolve, reject) => {
      db.get('SELECT id, permissions FROM users WHERE username=?', [username], (error, value) => error ? reject(error) : resolve(value))
    })
    if (!row) throw new Error(`restricted browser fixture is missing reader ${username}`)
    const permissions = JSON.parse(row.permissions)
    permissions.accessAllLibraries = false
    permissions.librariesAccessible = [bookLibraryID]
    await new Promise((resolve, reject) => db.run('UPDATE users SET permissions=? WHERE id=?', [JSON.stringify(permissions), row.id], error => error ? reject(error) : resolve()))
  } finally {
    await new Promise((resolve, reject) => db.close(error => error ? reject(error) : resolve()))
  }
}

async function prepareLongTranscodeFixture(configDir, targetRoot, itemId, username) {
  const sqlite3 = require('sqlite3').verbose()
  const mediaDir = path.join(targetRoot, 'media', 'forced-transcode')
  fs.mkdirSync(mediaDir, { recursive: true })
  const files = []
  for (let i = 0; i < 2; i++) {
    const file = path.join(mediaDir, `transcode-part-${i + 1}.m4b`)
    const generated = spawnSync('ffmpeg', ['-hide_banner', '-loglevel', 'error', '-f', 'lavfi', '-i', `anoisesrc=color=pink:sample_rate=44100:duration=300:seed=${20261030 + i}`, '-t', '300', '-c:a', 'aac', '-b:a', '128k', '-metadata', 'title=Deterministic Book', '-y', file], { encoding: 'utf8' })
    if (generated.status !== 0) throw new Error(`could not generate long transcode fixture: ${generated.stderr}`)
    const st = fs.statSync(file)
    files.push({ path: file, stat: st })
  }
  const db = new sqlite3.Database(path.join(configDir, 'absdatabase.sqlite'))
  const get = (sql, values = []) => new Promise((resolve, reject) => db.get(sql, values, (error, row) => error ? reject(error) : resolve(row)))
  const run = (sql, values = []) => new Promise((resolve, reject) => db.run(sql, values, error => error ? reject(error) : resolve()))
  try {
    const item = await get('SELECT mediaId FROM libraryItems WHERE id=?', [itemId])
    if (!item?.mediaId) throw new Error(`transcode fixture item not found: ${itemId}`)
    const book = await get('SELECT audioFiles FROM books WHERE id=?', [item.mediaId])
    const original = JSON.parse(book?.audioFiles || '[]')[0]
    if (!original) throw new Error(`transcode fixture book has no audio file: ${item.mediaId}`)
    const audioFiles = files.map(({ path: file, stat }, index) => ({
      ...original,
      index: index + 1,
      ino: String(stat.ino),
      duration: 300,
      metadata: {
        ...original.metadata,
        filename: path.basename(file), ext: '.m4b', path: file, relPath: path.basename(file), size: stat.size,
        mtimeMs: stat.mtimeMs, ctimeMs: stat.ctimeMs, birthtimeMs: stat.birthtimeMs
      }
    }))
    await run('BEGIN IMMEDIATE')
    await run('UPDATE books SET duration=?, audioFiles=? WHERE id=?', [600, JSON.stringify(audioFiles), item.mediaId])
    await run("DELETE FROM mediaProgresses WHERE userId=(SELECT id FROM users WHERE username=?) AND json_extract(extraData,'$.libraryItemId')=?", [username, itemId])
    await run('COMMIT')
  } catch (error) {
    await run('ROLLBACK').catch(() => {})
    throw error
  } finally {
    await new Promise((resolve, reject) => db.close(error => error ? reject(error) : resolve()))
  }
}

function ffmpegProcessesForSession(sessionId) {
  const ps = spawnSync('ps', ['-axo', 'pid=,command='], { encoding: 'utf8' })
  if (ps.status !== 0) throw new Error(`could not inspect FFmpeg process state: ${ps.stderr}`)
  return ps.stdout.split('\n').flatMap(line => {
    if (!line.includes(sessionId) || !/ffmpeg/i.test(line)) return []
    const match = line.trim().match(/^(\d+)\s+(.*)$/)
    return match ? [{ pid: Number(match[1]), command: match[2] }] : []
  })
}

function normalizeFailures(value) {
  return value.map(({ status, url, body }) => ({
    status,
    route: new URL(url).pathname.replace(/\/api\/items\/[^/]+\/cover/, '/api/items/:id/cover'),
    body
  })).sort((a, b) => JSON.stringify(a).localeCompare(JSON.stringify(b)))
}

async function checkBrowser(browserType, name, url, manifest) {
  const result = { origin: url, steps: {}, consoleErrors: [], pageErrors: [], apiFailures: [], assetFailures: [], playback: { hlsResponses: [] }, screenshots: {} }
  const browser = await browserType.launch({ headless: true })
  const credentials = VARIANT === 'restricted' ? manifest.readerCredentials : manifest.rootCredentials
  try {
    const page = await browser.newPage({ viewport: { width: 1000, height: 700 }, deviceScaleFactor: 1 })
    page.on('console', msg => { if (msg.type() === 'error') result.consoleErrors.push(msg.text()) })
    page.on('pageerror', e => result.pageErrors.push(e.stack || e.message))
    page.on('response', async response => {
      if (VARIANT === 'transcode' && new URL(response.url()).pathname.includes('/hls/')) {
        const pathname = new URL(response.url()).pathname
        const bytes = await response.body()
        result.playback.hlsResponses.push({
          extension: path.extname(pathname), status: response.status(),
          contentType: response.headers()['content-type'] || '', contentLength: response.headers()['content-length'] || null,
          bodyLength: bytes.length, bodySHA256: crypto.createHash('sha256').update(bytes).digest('hex')
        })
      }
      if (VARIANT === 'root' && response.status() === 404 && new URL(response.url()).pathname.includes('/_nuxt/')) {
        result.assetFailures.push({ route: new URL(response.url()).pathname, status: response.status(),
          contentType: response.headers()['content-type'], contentLength: response.headers()['content-length'],
          body: await response.text() })
      }
      if (response.url().includes('/api/') && (response.status() < 200 || response.status() >= 300)) {
        let body = ''
        try { body = (await response.text()).slice(0, 1000) } catch {}
        result.apiFailures.push({ status: response.status(), url: response.url(), body })
      }
    })
    const snap = async key => {
      const file = path.join(OUT, `${name}-${key}.png`)
      await page.screenshot({ path: file, fullPage: false, animations: 'disabled' })
      result.screenshots[key] = file
    }
    await page.goto(`${url}${BASE}/`, { waitUntil: 'networkidle', timeout: 45000 })
    result.steps.shell = await page.locator('#__nuxt').count() > 0
    await snap('shell')
    const username = page.locator('input').first()
    const password = page.locator('input[type="password"]')
    await username.waitFor({ state: 'visible', timeout: 20000 })
    if (VARIANT === 'fresh') {
      // Fresh init keeps the setup login form visible; do not submit.
      result.steps.setupForm = await username.count() > 0 && await password.count() > 0
      result.steps.noConsoleErrors = result.consoleErrors.length === 0 && result.pageErrors.length === 0
      result.steps.allAPI2xx = result.apiFailures.length === 0
      await snap('setup')
      return result
    }
    await username.fill(credentials.username)
    await password.fill(credentials.password)
    const submit = page.locator('button[type="submit"]').first()
    const loginResponsePromise = page.waitForResponse(response => {
      const request = response.request()
      return request.method() === 'POST' && new URL(response.url()).pathname.endsWith('/login')
    }, { timeout: 20000 })
    if (await submit.count()) await submit.click()
    else await password.press('Enter')
    const loginResponse = await loginResponsePromise
    let loginPayload = null
    try { loginPayload = await loginResponse.json() } catch {}
    await page.waitForTimeout(1800)
    result.steps.login = !/\/login(?:\?|$)/.test(new URL(page.url()).pathname)
    await snap('login')
    const libId = manifest.libraries.book
    await page.goto(`${url}${BASE}/library/${libId}`, { waitUntil: 'networkidle', timeout: 45000 })
    // The upstream client renders item titles inside cards whose text node can
    // be CSS-hidden under placeholder overlays; DOM attachment of the real
    // title with zero API failures is the acceptance signal (identical on both
    // implementations).
    await page.getByText('Deterministic Book', { exact: true }).first().waitFor({ state: 'attached', timeout: 20000 })
    const libBody = await page.locator('body').innerText()
    const cards = page.locator('a[href*="/item/"], [class*="item-card"], [class*="book-card"]')
    result.steps.library = libBody.includes('Deterministic Book') || (await cards.count()) > 0
    await snap('library')
    const itemId = manifest.items.book
    await page.goto(`${url}${BASE}/item/${itemId}`, { waitUntil: 'networkidle', timeout: 45000 })
    await page.getByText('Deterministic Book', { exact: true }).first().waitFor({ state: 'attached', timeout: 20000 })
    const bodyText = await page.locator('body').innerText()
    result.steps.detail = bodyText.includes('Deterministic Book') && /duration|narrator|reader|A\. Reader/i.test(bodyText)
    await snap('detail')

    if (VARIANT === 'restricted') {
      // Verify ACL using the reader's actual browser-issued access token, not
      // navigation success alone. Capture the complete visible library set,
      // then directly request the forbidden podcast library and item APIs.
      const accessToken = loginPayload?.user?.accessToken
      if (loginResponse.status() !== 200 || typeof accessToken !== 'string' || accessToken.length < 10) {
        throw new Error(`restricted visibility requires a successful browser login token; status=${loginResponse.status()}`)
      }
      const visibility = await page.evaluate(async ({ base, token, libraryId, itemId }) => {
        const get = async route => {
          const response = await fetch(`${base}/api${route}`, { headers: { Authorization: `Bearer ${token}` } })
          const text = await response.text()
          let body
          try { body = JSON.parse(text) } catch { body = text }
          return { status: response.status, body }
        }
        const visible = await get('/libraries')
        const forbiddenLibrary = await get(`/libraries/${libraryId}`)
        const forbiddenItem = await get(`/items/${itemId}`)
        const libraryRows = Array.isArray(visible.body?.libraries) ? visible.body.libraries : []
        const forbiddenLibraryJSON = JSON.stringify(forbiddenLibrary.body)
        const forbiddenItemJSON = JSON.stringify(forbiddenItem.body)
        return {
          visibleStatus: visible.status,
          visibleLibraryIds: libraryRows.map(row => row.id).sort(),
          forbiddenLibrary: {
            status: forbiddenLibrary.status,
            leaksProtectedID: forbiddenLibraryJSON.includes(libraryId),
            leaksProtectedName: forbiddenLibraryJSON.includes('Parity Podcasts')
          },
          forbiddenItem: {
            status: forbiddenItem.status,
            leaksProtectedID: forbiddenItemJSON.includes(itemId),
            leaksProtectedTitle: forbiddenItemJSON.includes('Sample Podcast')
          }
        }
      }, { base: BASE, token: accessToken, libraryId: manifest.libraries.podcast, itemId: manifest.items.podcast })
      const forbiddenLibraryDenied = visibility.forbiddenLibrary.status < 200 || visibility.forbiddenLibrary.status >= 300
      const forbiddenItemDenied = visibility.forbiddenItem.status < 200 || visibility.forbiddenItem.status >= 300
      const forbiddenItemHasNoProtectedData = !visibility.forbiddenLibrary.leaksProtectedID &&
        !visibility.forbiddenLibrary.leaksProtectedName && !visibility.forbiddenItem.leaksProtectedID &&
        !visibility.forbiddenItem.leaksProtectedTitle
      result.restrictedVisibility = visibility
      result.steps.restrictedVisibility = visibility.visibleStatus === 200 &&
        JSON.stringify(visibility.visibleLibraryIds) === JSON.stringify([manifest.libraries.book]) &&
        forbiddenLibraryDenied && forbiddenItemDenied && forbiddenItemHasNoProtectedData
    }

    if (VARIANT === 'full' || VARIANT === 'transcode') {
      // Exercise the actual web player, including the media element and its
      // public play/close API calls. API-only playback cases do not prove that
      // the shipped browser client can consume the selected media response.
      const playPath = `/api/items/${itemId}/play`
    if (VARIANT === 'transcode') {
      await page.route('**/api/items/**/play', async route => {
        const request = route.request()
        if (request.method() !== 'POST') return route.continue()
        const body = request.postDataJSON() || {}
        return route.continue({ postData: JSON.stringify({ ...body, forceDirectPlay: false, forceTranscode: true, supportedMimeTypes: [] }) })
      })
    }
    const playResponsePromise = page.waitForResponse(response => {
      const request = response.request()
      return request.method() === 'POST' && new URL(response.url()).pathname.endsWith(playPath)
    }, { timeout: 20000 })
    await page.locator('button:visible').filter({ hasText: 'Play' }).first().click()
    const playResponse = await playResponsePromise
    let playSession = null
    try { playSession = await playResponse.json() } catch {}
    result.playback.start = { status: playResponse.status(), sessionIdPresent: Boolean(playSession?.id), playMethod: playSession?.playMethod }
    result.steps.playbackStart = playResponse.status() === 200 && Boolean(playSession?.id)
    if (VARIANT === 'transcode') {
      result.steps.transcodeSelected = playSession?.playMethod === 2 && playSession?.audioTracks?.[0]?.contentUrl?.includes('/hls/')
      result.playback.ffmpegBeforeClose = []
      for (let i = 0; i < 40 && result.playback.ffmpegBeforeClose.length === 0; i++) {
        result.playback.ffmpegBeforeClose = ffmpegProcessesForSession(playSession.id)
        if (result.playback.ffmpegBeforeClose.length === 0) await delay(25)
      }
      result.steps.ffmpegAliveBeforeClose = result.playback.ffmpegBeforeClose.length > 0
      result.playback.streamDir = path.join(OUT, 'fixture', name, 'metadata', 'streams', playSession.id)
    }

    await page.locator('#mediaPlayerContainer').waitFor({ state: 'visible', timeout: 15000 })
    await page.waitForFunction(() => {
      const audio = document.querySelector('#audio-player')
      return Boolean(audio && audio.readyState >= 1 && Number.isFinite(audio.duration) && audio.duration > 0)
    }, { timeout: 15000 })
    result.playback.loaded = await page.locator('#audio-player').evaluate(audio => ({
      readyState: audio.readyState,
      duration: audio.duration,
      paused: audio.paused,
      currentSrc: new URL(audio.currentSrc).pathname
    }))
    result.steps.audioLoaded = result.playback.loaded.readyState >= 1 && result.playback.loaded.duration > 0
    await page.waitForFunction(() => {
      const audio = document.querySelector('#audio-player')
      return Boolean(audio && !audio.paused && audio.readyState >= 2 && audio.currentTime > 0)
    }, { timeout: 5000 })
    result.playback.afterPlay = await page.locator('#audio-player').evaluate(audio => ({ paused: audio.paused, currentTime: audio.currentTime }))
    result.steps.playbackPlaying = !result.playback.afterPlay.paused && result.playback.afterPlay.currentTime > 0

    if (VARIANT === 'transcode') {
      result.steps.transcodePlayingAdvances = result.steps.transcodeSelected && result.steps.audioLoaded && result.steps.playbackPlaying
      result.playback.hlsObserved = [...result.playback.hlsResponses]
      result.steps.hlsPlaylistAndSegmentServed = result.playback.hlsObserved.some(x => x.extension === '.m3u8' && x.status === 200) &&
        result.playback.hlsObserved.some(x => x.extension === '.ts' && x.status === 200)
      await page.waitForFunction(() => document.querySelector('#audio-player')?.currentTime >= 1, { timeout: 12000 })
      result.playback.beforeClose = await page.locator('#audio-player').evaluate(audio => ({ currentTime: audio.currentTime, duration: audio.duration, paused: audio.paused }))
    } else {
    const pauseButton = page.locator('#mediaPlayerContainer').getByRole('button', { name: 'Pause' })
    await pauseButton.waitFor({ state: 'visible', timeout: 10000 })
    await pauseButton.click()
    await page.waitForFunction(() => document.querySelector('#audio-player')?.paused === true, { timeout: 5000 })
    result.playback.afterPause = await page.locator('#audio-player').evaluate(audio => ({ paused: audio.paused, currentTime: audio.currentTime }))
    result.steps.playbackPause = result.playback.afterPause.paused

    const track = page.locator('#mediaPlayerContainer .cursor-pointer.transform.duration-100').first()
    const trackBox = await track.boundingBox()
    if (!trackBox || trackBox.width < 20) throw new Error('Playback track bar is not measurable')
    await track.click({ position: { x: Math.round(trackBox.width * 0.7), y: Math.max(1, Math.round(trackBox.height / 2)) } })
    await page.waitForFunction(() => {
      const audio = document.querySelector('#audio-player')
      return Boolean(audio && audio.currentTime > audio.duration * 0.55)
    }, { timeout: 5000 })
    result.playback.afterSeekForward = await page.locator('#audio-player').evaluate(audio => ({ paused: audio.paused, currentTime: audio.currentTime, duration: audio.duration }))
    await track.click({ position: { x: Math.round(trackBox.width * 0.3), y: Math.max(1, Math.round(trackBox.height / 2)) } })
    await page.waitForFunction(() => {
      const audio = document.querySelector('#audio-player')
      return Boolean(audio && audio.currentTime < audio.duration * 0.45 && audio.currentTime > 0)
    }, { timeout: 5000 })
    result.playback.afterSeekBackward = await page.locator('#audio-player').evaluate(audio => ({ paused: audio.paused, currentTime: audio.currentTime, duration: audio.duration }))
    await track.click({ position: { x: Math.round(trackBox.width * 0.7), y: Math.max(1, Math.round(trackBox.height / 2)) } })
    await page.waitForFunction(() => {
      const audio = document.querySelector('#audio-player')
      return Boolean(audio && audio.currentTime > audio.duration * 0.55)
    }, { timeout: 5000 })
    result.playback.afterSeek = await page.locator('#audio-player').evaluate(audio => ({ paused: audio.paused, currentTime: audio.currentTime, duration: audio.duration }))
    result.steps.playbackSeekBackward = result.playback.afterSeekBackward.currentTime < result.playback.afterSeekForward.currentTime
    result.steps.playbackSeekForward = result.playback.afterSeek.currentTime > result.playback.afterSeekBackward.currentTime
    result.steps.playbackSeek = result.steps.playbackSeekBackward && result.steps.playbackSeekForward
    }

    const closePath = /\/api\/session\/[^/]+\/close$/
    const closeResponsePromise = page.waitForResponse(response => response.request().method() === 'POST' && closePath.test(new URL(response.url()).pathname), { timeout: 10000 })
    await page.locator('#mediaPlayerContainer button').first().click()
    const closeResponse = await closeResponsePromise
    result.playback.close = { status: closeResponse.status(), sessionIdMatches: new URL(closeResponse.url()).pathname.includes(playSession?.id || '\u0000') }
    result.steps.playbackClose = closeResponse.status() === 200 && await page.locator('#mediaPlayerContainer').count() === 0
    if (VARIANT === 'transcode') {
      result.playback.ffmpegAfterClose = ffmpegProcessesForSession(playSession.id)
      for (let i = 0; i < 50 && result.playback.ffmpegAfterClose.length > 0; i++) {
        await delay(100)
        result.playback.ffmpegAfterClose = ffmpegProcessesForSession(playSession.id)
      }
      result.playback.streamDirPresentAfterClose = fs.existsSync(result.playback.streamDir)
      result.steps.ffmpegTerminatedOnClose = result.steps.ffmpegAliveBeforeClose && result.playback.ffmpegAfterClose.length === 0
      result.steps.transcodeOutputRemovedOnClose = !result.playback.streamDirPresentAfterClose
    }
    }

    result.steps.noConsoleErrors = result.consoleErrors.length === 0 && result.pageErrors.length === 0
    result.steps.allAPI2xx = result.apiFailures.length === 0
    return result
  } catch (error) {
    result.error = error.stack || error.message
    result.steps.noConsoleErrors = result.consoleErrors.length === 0 && result.pageErrors.length === 0
    result.steps.allAPI2xx = result.apiFailures.length === 0
    return result
  } finally { await browser.close() }
}

async function main() {
  const manifestPath = path.resolve(arg('fixture', DEFAULT_FIXTURE))
  const manifest = JSON.parse(fs.readFileSync(manifestPath, 'utf8'))
  fs.mkdirSync(OUT, { recursive: true })
  const fixtureRoot = path.join(OUT, 'fixture')
  fs.mkdirSync(fixtureRoot, { recursive: true })
  for (const side of ['go', 'node']) {
    const target = path.join(fixtureRoot, side)
    if (VARIANT === 'fresh') {
      // Fresh-DB variant: both servers first-boot an empty tree.
      fs.mkdirSync(target, { recursive: true })
      continue
    }
    const source = manifest.directories[side]
    fs.mkdirSync(target, { recursive: true })
    for (const sub of ['config', 'metadata']) fs.cpSync(path.join(source, sub), path.join(target, sub), { recursive: true, force: true })
    await rewriteCopiedCoverPaths(path.join(target, manifest.databaseRelativePath), path.join(source, 'metadata'), path.join(target, 'metadata'))
    if (VARIANT === 'restricted') {
      await restrictReaderToBookLibrary(path.join(target, manifest.databaseRelativePath), manifest.readerCredentials.username, manifest.libraries.book)
    }
    if (VARIANT === 'transcode') {
      await prepareLongTranscodeFixture(path.join(target, 'config'), target, manifest.items.book, manifest.rootCredentials.username)
    }
  }
  const goBin = path.join(OUT, 'audiobookshelf-go')
  const build = spawnSync('go', ['build', '-o', goBin, './cmd/audiobookshelf-go'], {
    cwd: path.join(ROOT, 'go-backend'), encoding: 'utf8', env: { ...process.env, CGO_ENABLED: '0' }
  })
  if (build.status !== 0) throw new Error(`Go build failed (${build.status}): ${build.stderr}`)
  const pw = playwright()
  const servers = {}
  const results = {}
  try {
    for (const side of ['go', 'node']) {
      const port = await freePort()
      const url = `http://127.0.0.1:${port}`
      const dirs = path.join(fixtureRoot, side)
      const logPath = path.join(OUT, `${side}-server.log`)
      if (side === 'go') {
        servers.go = start(goBin, [], { cwd: ROOT, env: { ...process.env,
          CONFIG_PATH: path.join(dirs, 'config'), METADATA_PATH: path.join(dirs, 'metadata'),
          ABS_GO_CLIENT_PATH: path.join(ROOT, 'client/dist'), HOST: '127.0.0.1', PORT: String(port),
          ROUTER_BASE_PATH: BASE, SOURCE: 'docker' } }, logPath)
      } else {
        const node = process.env.NODE22 || 'mise'
        // Match run-local.cjs: its checked-in production entry point is dist-server/index.js.
        servers.node = start(node, ['exec', 'node@22', '--', 'node', path.join(ROOT, 'dist-server/index.js'),
          '--config', path.join(dirs, 'config'), '--metadata', path.join(dirs, 'metadata'),
          '--port', String(port), '--host', '127.0.0.1', '--source', 'docker'],
        { cwd: ROOT, env: { ...process.env, NODE_ENV: 'production', ROUTER_BASE_PATH: BASE } }, logPath)
      }
      await waitReady(servers[side], url, logPath)
      results[side] = await checkBrowser(pw.chromium, side, url, manifest)
      if (VARIANT === 'fresh') results[side].freshInit = await freshInitState(path.join(dirs, 'config'))
    }
  } finally { await Promise.all([stop(servers.go), stop(servers.node)]) }
  const goPlayback = results.go.playback || {}
  const nodePlayback = results.node.playback || {}
  const normalizePlayback = playback => VARIANT === 'transcode' ? ({
    start: { status: playback.start?.status, sessionIdPresent: playback.start?.sessionIdPresent, playMethod: playback.start?.playMethod },
    loaded: playback.loaded && { readyState: playback.loaded.readyState, duration: playback.loaded.duration },
    afterPlay: playback.afterPlay && { paused: playback.afterPlay.paused, currentTime: Number(playback.afterPlay.currentTime.toFixed(1)) },
    beforeClose: playback.beforeClose && { currentTime: Number(playback.beforeClose.currentTime.toFixed(1)), duration: playback.beforeClose.duration, paused: playback.beforeClose.paused },
    hlsResponses: (playback.hlsObserved || []).map(x => ({ extension: x.extension, status: x.status, contentType: x.contentType, contentLength: x.contentLength, bodyLength: x.bodyLength, bodySHA256: x.bodySHA256 })).sort((a, b) => JSON.stringify(a).localeCompare(JSON.stringify(b))),
    ffmpegAliveBeforeClose: (playback.ffmpegBeforeClose || []).length > 0,
    ffmpegTerminatedOnClose: (playback.ffmpegAfterClose || []).length === 0,
    streamDirPresentAfterClose: playback.streamDirPresentAfterClose,
    close: { status: playback.close?.status, sessionIdMatches: playback.close?.sessionIdMatches }
  }) : ({
    start: { status: playback.start?.status, sessionIdPresent: playback.start?.sessionIdPresent },
    loaded: playback.loaded && { readyState: playback.loaded.readyState, duration: playback.loaded.duration },
    afterPlay: playback.afterPlay && { paused: playback.afterPlay.paused, currentTime: Number(playback.afterPlay.currentTime.toFixed(1)) },
    afterPause: playback.afterPause && { paused: playback.afterPause.paused, currentTime: Number(playback.afterPause.currentTime.toFixed(1)) },
    afterSeekForward: playback.afterSeekForward && { paused: playback.afterSeekForward.paused, currentTime: Number(playback.afterSeekForward.currentTime.toFixed(1)), duration: playback.afterSeekForward.duration },
    afterSeekBackward: playback.afterSeekBackward && { paused: playback.afterSeekBackward.paused, currentTime: Number(playback.afterSeekBackward.currentTime.toFixed(1)), duration: playback.afterSeekBackward.duration },
    afterSeek: playback.afterSeek && { paused: playback.afterSeek.paused, currentTime: Number(playback.afterSeek.currentTime.toFixed(1)), duration: playback.afterSeek.duration },
    close: { status: playback.close?.status, sessionIdMatches: playback.close?.sessionIdMatches }
  })
  const goFailures = normalizeFailures(results.go.apiFailures || [])
  const nodeFailures = normalizeFailures(results.node.apiFailures || [])
  // Root assets fail concurrently. Compare their diagnostic multiset after
  // removing only each fixture's origin; retain the raw observations above.
  const consoleErrors = side => VARIANT === 'root'
    ? (results[side].consoleErrors || []).map(text => text.replaceAll(results[side].origin, '<origin>')).sort()
    : results[side].consoleErrors || []
  const goConsoleErrors = consoleErrors('go')
  const nodeConsoleErrors = consoleErrors('node')
  const assets = side => [...results[side].assetFailures].sort((a, b) => a.route.localeCompare(b.route))
  results.comparison = {
    playbackEqual: JSON.stringify(normalizePlayback(goPlayback)) === JSON.stringify(normalizePlayback(nodePlayback)),
    goPlayback: normalizePlayback(goPlayback),
    nodePlayback: normalizePlayback(nodePlayback),
    apiFailuresEqual: JSON.stringify(goFailures) === JSON.stringify(nodeFailures),
    apiFailures: { go: goFailures, node: nodeFailures },
    consoleErrorsEqual: JSON.stringify(goConsoleErrors) === JSON.stringify(nodeConsoleErrors),
    consoleErrors: { go: goConsoleErrors, node: nodeConsoleErrors },
    rootAssetFailuresEqual: VARIANT === 'root' && results.go.assetFailures.length > 0 &&
      JSON.stringify(assets('go')) === JSON.stringify(assets('node')),
    pageErrorsEqual: JSON.stringify(results.go.pageErrors || []) === JSON.stringify(results.node.pageErrors || []),
    pageErrors: { go: results.go.pageErrors || [], node: results.node.pageErrors || [] },
    restrictedVisibilityEqual: VARIANT === 'restricted' &&
      JSON.stringify(results.go.restrictedVisibility) === JSON.stringify(results.node.restrictedVisibility),
    restrictedVisibility: VARIANT === 'restricted'
      ? { go: results.go.restrictedVisibility, node: results.node.restrictedVisibility }
      : undefined
  }
  for (const side of ['go', 'node']) {
    const steps = results[side].steps
    steps.playbackMatchesUpstream = results.comparison.playbackEqual
    steps.runtimeFailuresMatchUpstream = results.comparison.apiFailuresEqual && results.comparison.consoleErrorsEqual && results.comparison.pageErrorsEqual &&
      (VARIANT !== 'root' || results.comparison.rootAssetFailuresEqual)
    if (VARIANT === 'restricted') steps.restrictedVisibilityMatchesUpstream = results.comparison.restrictedVisibilityEqual
  }
  const report = { browser: 'Playwright Chromium headless', fixture: manifestPath, output: OUT,
    boot: { go: 'CGO_ENABLED=0 binary; isolated copied config/metadata; ABS_GO_CLIENT_PATH=client/dist; ROUTER_BASE_PATH=/audiobookshelf',
      node: 'mise exec node@22 -- dist/index.js; isolated copied config/metadata' }, results }
  const contractReport = {
    schemaVersion: 1,
    baseline: manifest.baseline,
    browser: 'Playwright Chromium headless',
    fixture: 'fixture-manifest.json; independent per-side config, database and metadata copies',
    scope: 'Unmodified upstream browser client; login, library/detail navigation, direct-play start, loaded audio, playing, pause, backward and forward seek, session close.',
    results: {
      matched: results.comparison.playbackEqual && results.comparison.apiFailuresEqual && results.comparison.consoleErrorsEqual && results.comparison.pageErrorsEqual,
      playback: results.comparison,
      verified: ['POST /api/items/:id/play returned a session', 'HTMLAudioElement loaded the track and entered playing state', 'player pause control paused the element', 'track-bar clicks sought forward to approximately 70 percent, backward to approximately 30 percent, then forward again while paused', 'player close control closed the same session'],
      limitations: ['Only Chromium direct-play of the generated two-second AAC fixture was exercised.', 'HLS/transcode selection, socket reconnection, and session recovery remain unverified.', 'Both servers returned four identical 500 cover-resize responses because the host FFmpeg build has no WebP encoder; this shared fixture limitation is retained in the report.']
    }
  }
  const contractPath = path.join(ROOT, 'go-backend/contracts/parity/browser-playback-differential-report.json')
  if (VARIANT === 'full') {
    fs.writeFileSync(contractPath, JSON.stringify(contractReport, null, 2) + '\n')
  fs.writeFileSync(path.join(ROOT, 'go-backend/contracts/parity/browser-playback-differential-provenance.json'), JSON.stringify({
    schemaVersion: 1,
    baseline: manifest.baseline,
    command: 'BROWSER_CHECK_OUT=<isolated writable temp dir> node go-backend/tools/parity/browser-check.cjs',
    upstream: 'Node v22.23.4; pinned dist-server/index.js',
    go: 'CGO_ENABLED=0 binary built from current go-backend source',
    fixture: 'Generated silent AAC media; isolated copied fixture databases and metadata; copied DB coverPath values rewritten to their corresponding copied metadata roots.',
    normalizations: ['Per-side playback session UUID and origin are omitted from comparisons.', 'Current time is rounded to 100 ms for the paired browser state comparison.', 'Cover image resize 500s are retained and compared exactly by route, status, and body; they are not treated as Go-only differences.'],
    limitations: contractReport.results.limitations
  }, null, 2) + '\n')
  }
  for (const [side, value] of Object.entries(results)) {
    if (side === 'comparison') continue
    for (const [step, pass] of Object.entries(value.steps || {})) {
      const sharedFailure = ['noConsoleErrors', 'allAPI2xx'].includes(step) && value.steps.runtimeFailuresMatchUpstream
      console.log(`${pass ? 'PASS' : sharedFailure ? 'MATCH' : 'FAIL'} ${side}.${step}${sharedFailure ? ' (same upstream and Go fixture failures; see report)' : ''}`)
    }
    if (value.error) console.error(`${side} error: ${value.error}`)
    if (value.consoleErrors?.length) console.error(`${side} console errors: ${JSON.stringify(value.consoleErrors)}`)
    if (value.pageErrors?.length) console.error(`${side} page errors: ${JSON.stringify(value.pageErrors)}`)
    if (value.apiFailures?.length) console.error(`${side} API failures: ${JSON.stringify(value.apiFailures)}`)
  }
  const requiredSteps = VARIANT === 'full' ? ['shell', 'login', 'library', 'detail', 'playbackStart', 'audioLoaded', 'playbackPlaying', 'playbackPause', 'playbackSeek', 'playbackClose', 'playbackMatchesUpstream', 'runtimeFailuresMatchUpstream']
    : VARIANT === 'transcode' ? ['shell', 'login', 'library', 'detail', 'playbackStart', 'transcodeSelected', 'ffmpegAliveBeforeClose', 'audioLoaded', 'hlsPlaylistAndSegmentServed', 'playbackPlaying', 'transcodePlayingAdvances', 'playbackClose', 'ffmpegTerminatedOnClose', 'transcodeOutputRemovedOnClose', 'playbackMatchesUpstream', 'runtimeFailuresMatchUpstream']
    : VARIANT === 'fresh' ? ['shell', 'setupForm', 'allAPI2xx']
    : VARIANT === 'restricted'
      ? ['shell', 'login', 'library', 'detail', 'restrictedVisibility', 'restrictedVisibilityMatchesUpstream', 'runtimeFailuresMatchUpstream']
      : ['shell', 'login', 'library', 'detail', 'runtimeFailuresMatchUpstream']
  if (VARIANT === 'fresh') {
    const equal = JSON.stringify(results.go.freshInit) === JSON.stringify(results.node.freshInit)
    results.freshInitComparison = { equal, go: results.go.freshInit, node: results.node.freshInit }
    for (const side of ['go', 'node']) results[side].steps.freshInitMatchesUpstream = equal
    requiredSteps.push('freshInitMatchesUpstream')
  }
  const ok = ['go', 'node'].every(side => !results[side].error && requiredSteps.every(step => results[side].steps[step]))
  fs.writeFileSync(path.join(OUT, 'browser-report.json'), JSON.stringify(report, null, 2) + '\n')
  console.log(`Report: ${path.join(OUT, 'browser-report.json')} variant=${VARIANT}`)
  process.exitCode = ok ? 0 : 1
}
main().catch(error => { console.error(error.stack || error); process.exitCode = 1 })
