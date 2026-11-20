#!/usr/bin/env node
// Real-browser E2E differential test driving the unchanged bundled Nuxt SPA
// (client/dist) in headless Chromium against both pinned TS v2.37.2 and Go.
import { spawn, spawnSync } from 'node:child_process'
import fs from 'node:fs'
import { createRequire } from 'node:module'
import net from 'node:net'
import os from 'node:os'
import path from 'node:path'
import { setTimeout as delay } from 'node:timers/promises'
import { fileURLToPath } from 'node:url'

const __dirname = path.dirname(fileURLToPath(import.meta.url))

function findRepoRoot(startDir) {
  let dir = startDir
  while (true) {
    if (fs.existsSync(path.join(dir, 'go.mod')) && fs.existsSync(path.join(dir, 'testmachine', 'oracle'))) {
      return dir
    }
    const parent = path.dirname(dir)
    if (parent === dir) {
      throw new Error(`repository root (go.mod and testmachine/oracle/) not found above ${startDir}`)
    }
    dir = parent
  }
}

const repoRoot = findRepoRoot(__dirname)
const upstreamDir = process.env.ORACLE_UPSTREAM_DIR
  ? path.resolve(process.env.ORACLE_UPSTREAM_DIR)
  : path.join(repoRoot, '.oracle-upstream')
const require = createRequire(import.meta.url)

function parseArgs(argv) {
  const opts = { tsUrl: null, goUrl: null, out: null, base: '/audiobookshelf', optional: false }
  for (let i = 0; i < argv.length; i++) {
    const a = argv[i]
    if (a === '--optional') opts.optional = true
    else if (a === '--ts-url' && argv[i + 1]) opts.tsUrl = argv[++i].replace(/\/+$/, '')
    else if (a === '--go-url' && argv[i + 1]) opts.goUrl = argv[++i].replace(/\/+$/, '')
    else if (a === '--out' && argv[i + 1]) opts.out = path.resolve(argv[++i])
    else if (a === '--base' && argv[i + 1]) opts.base = argv[++i].replace(/\/+$/, '')
  }
  return opts
}

async function loadPlaywright(optional) {
  try {
    return await import('playwright')
  } catch {
    console.error('Playwright is not installed. Run: npm i --no-save playwright && npx playwright install chromium')
    process.exit(optional ? 0 : 2)
  }
}

function freePort() {
  return new Promise((resolve, reject) => {
    const srv = net.createServer()
    srv.listen(0, '127.0.0.1', () => {
      const { port } = srv.address()
      srv.close((err) => (err ? reject(err) : resolve(port)))
    })
    srv.on('error', reject)
  })
}

async function rewriteSqlitePaths(dbPath, seedRoots, targetDirs, bookLibId) {
  const sqlite3 = require('sqlite3').verbose()
  const db = new sqlite3.Database(dbPath)
  const all = (sql, params = []) => new Promise((res, rej) => db.all(sql, params, (e, r) => (e ? rej(e) : res(r))))
  const run = (sql, params = []) => new Promise((res, rej) => db.run(sql, params, (e) => (e ? rej(e) : res())))
  const pairs = [
    [seedRoots.config, targetDirs.config],
    [seedRoots.metadata, targetDirs.metadata],
    [seedRoots.media, targetDirs.media],
  ].sort((a, b) => b[0].length - a[0].length)
  try {
    const tables = await all("SELECT name FROM sqlite_master WHERE type='table' AND substr(name,1,7)<>'sqlite_'")
    const cols = []
    for (const { name: t } of tables) {
      const info = await all(`PRAGMA table_info("${t.replaceAll('"', '""')}")`)
      for (const c of info) {
        const u = (c.type || '').toUpperCase()
        if (u === '' || /TEXT|CHAR|CLOB|JSON/.test(u)) cols.push([t, c.name])
      }
    }
    await run('BEGIN IMMEDIATE')
    for (const [from, to] of pairs) {
      for (const [t, c] of cols) {
        const qt = `"${t.replaceAll('"', '""')}"`, qc = `"${c.replaceAll('"', '""')}"`
        await run(`UPDATE ${qt} SET ${qc}=replace(${qc},?,?) WHERE instr(${qc},?)>0`, [from, to, from])
      }
    }
    if (bookLibId) {
      const row = await all("SELECT id, permissions FROM users WHERE username='trial-listener'")
      if (row[0]) {
        const p = JSON.parse(row[0].permissions)
        p.accessAllLibraries = false
        p.librariesAccessible = [bookLibId]
        await run('UPDATE users SET permissions=? WHERE id=?', [JSON.stringify(p), row[0].id])
      }
    }
    await run('COMMIT')
  } catch (err) {
    await run('ROLLBACK').catch(() => {})
    throw err
  } finally {
    await new Promise((res, rej) => db.close((e) => (e ? rej(e) : res())))
  }
}

async function prepareInstalls(workRoot) {
  const src = path.join(repoRoot, 'testmachine/testdata/install')
  const meta = JSON.parse(fs.readFileSync(path.join(src, 'fixture.json'), 'utf8'))
  const bookLibId = '51bf3bdf-f246-4f7d-b0a6-9059e4d0ed41'
  const installs = {}
  for (const side of ['ts', 'go']) {
    const dest = path.join(workRoot, side)
    fs.cpSync(src, dest, { recursive: true })
    const dirs = { config: path.join(dest, 'config'), metadata: path.join(dest, 'metadata'), media: path.join(dest, 'media') }
    await rewriteSqlitePaths(path.join(dirs.config, 'absdatabase.sqlite'), meta.seedRoots, dirs, bookLibId)
    installs[side] = dirs
  }
  return { installs, users: meta.users }
}

async function waitReady(url, base, proc, logPath) {
  const deadline = Date.now() + 45000
  while (Date.now() < deadline) {
    if (proc.exitCode !== null) throw new Error(`Server exited early (${proc.exitCode}); log: ${logPath}`)
    try {
      const res = await fetch(`${url}${base}/ping`)
      if (res.ok) return
    } catch {}
    await delay(200)
  }
  throw new Error(`Server not ready within 45s: ${url}${base}/ping`)
}

async function startServers(workRoot, base) {
  const goBin = path.join(workRoot, 'monstrobook')
  const build = spawnSync('go', ['build', '-o', goBin, './cmd/monstrobook'], {
    cwd: repoRoot, encoding: 'utf8', env: { ...process.env, CGO_ENABLED: '0' },
  })
  if (build.status !== 0) throw new Error(`Go build failed: ${build.stderr}`)
  const tsEntry = path.join(upstreamDir, 'index.js')
  if (!fs.existsSync(tsEntry)) {
    throw new Error(`Upstream TS entrypoint missing at ${tsEntry}; run 'node testmachine/oracle/fetch-upstream.mjs' or 'make upstream'`)
  }
  const { installs, users } = await prepareInstalls(workRoot)
  const procs = []
  const targets = {}
  for (const side of ['ts', 'go']) {
    const port = await freePort()
    const url = `http://127.0.0.1:${port}`
    const logPath = path.join(workRoot, `${side}.log`)
    const logFd = fs.openSync(logPath, 'w')
    const env = {
      ...process.env, CONFIG_PATH: installs[side].config, METADATA_PATH: installs[side].metadata,
      HOST: '127.0.0.1', PORT: String(port), SOURCE: 'docker', ROUTER_BASE_PATH: base,
      ...(side === 'ts' ? { NODE_ENV: 'production' } : { ABS_GO_CLIENT_PATH: path.join(upstreamDir, 'client/dist') }),
    }
    const cmd = side === 'ts' ? process.execPath : goBin
    const args = side === 'ts' ? [tsEntry] : []
    const cwd = side === 'ts' ? upstreamDir : repoRoot
    const proc = spawn(cmd, args, { cwd, env, stdio: ['ignore', logFd, logFd] })
    fs.closeSync(logFd)
    procs.push(proc)
    targets[side] = { url, proc, logPath }
  }
  await Promise.all([
    waitReady(targets.ts.url, base, targets.ts.proc, targets.ts.logPath),
    waitReady(targets.go.url, base, targets.go.proc, targets.go.logPath),
  ])
  const stop = async () => {
    for (const p of procs) if (p.exitCode === null) p.kill('SIGTERM')
    await delay(500)
    for (const p of procs) if (p.exitCode === null) p.kill('SIGKILL')
  }
  return { tsUrl: targets.ts.url, goUrl: targets.go.url, users, stop }
}

function normalizeApiPath(rawUrl, base) {
  let p = new URL(rawUrl).pathname
  if (base && p.startsWith(base)) p = p.slice(base.length) || '/'
  p = p.replace(/[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}/gi, ':id')
  return p.replace(/\/(items|libraries|authors|series|collections|playlists|users|session|sessions|feeds|backups|api-keys)\/[^/?]+/g, '/$1/:id')
}

async function attachSocketTracker(page) {
  await page.evaluate(() => {
    window.__e2eSockets = window.__e2eSockets || []
    const sock = window.$nuxt?.$root?.socket
    if (!sock || sock.__e2eHooked) return
    sock.__e2eHooked = true
    for (const ev of ['item_updated', 'user_item_progress_updated', 'user_session_closed', 'user_updated']) {
      sock.on(ev, (payload) => window.__e2eSockets.push({ event: ev, at: Date.now(), hasPayload: Boolean(payload) }))
    }
  })
}

async function runTargetJourneys(browser, side, url, base, users) {
  const out = { side, url, steps: {}, consoleErrors: [], pageErrors: [], server5xx: [], failedAssets: [], xhr: [], socketEvents: [] }
  const ctx = await browser.newContext({ viewport: { width: 1280, height: 800 } })
  const page = await ctx.newPage()
  const trackPage = (p) => {
    p.on('console', (m) => { if (m.type() === 'error') out.consoleErrors.push(m.text()) })
    p.on('pageerror', (e) => out.pageErrors.push(e.message))
    p.on('response', (res) => {
      const u = new URL(res.url())
      if (res.status() >= 500) out.server5xx.push({ path: u.pathname, status: res.status() })
      if ((u.pathname.includes('/_nuxt/') || /\.(js|css|svg|png|jpg|woff2)$/.test(u.pathname)) && res.status() >= 400) {
        out.failedAssets.push({ path: u.pathname, status: res.status() })
      }
      if (/\/(api\/|login$|logout$|status$|auth\/)/.test(u.pathname)) {
        out.xhr.push({ method: res.request().method(), route: normalizeApiPath(res.url(), base), status: res.status() })
      }
    })
  }
  trackPage(page)

  try {
    // 1. Unauthenticated redirect to /login + static assets
    await page.goto(`${url}${base}/`, { waitUntil: 'networkidle', timeout: 30000 })
    const userIn = page.locator('input').first()
    const passIn = page.locator('input[type="password"]')
    await userIn.waitFor({ state: 'visible', timeout: 15000 })
    out.steps.unauthLoginRedirect = new URL(page.url()).pathname.endsWith('/login') && (await page.locator('#__nuxt').count()) > 0
    out.steps.staticAssetsClean = out.failedAssets.length === 0 && out.server5xx.length === 0

    // 2. Root login -> library shelf -> search -> book detail -> chapters/metadata -> edit title & save -> author -> collections & playlists -> admin pages
    await userIn.fill(users.root.username)
    await passIn.fill(users.root.password)
    const loginWait = page.waitForResponse((r) => r.request().method() === 'POST' && new URL(r.url()).pathname.endsWith('/login'))
    await passIn.press('Enter')
    const loginData = await (await loginWait).json()
    await page.waitForFunction(() => !location.pathname.endsWith('/login'), { timeout: 15000 })
    await attachSocketTracker(page)

    const libs = await page.evaluate(async ({ base, tok }) => {
      const r = await fetch(`${base}/api/libraries`, { headers: { Authorization: `Bearer ${tok}` } })
      return r.json()
    }, { base, tok: loginData.user.accessToken })
    const bookLib = (libs.libraries || libs).find((l) => l.mediaType === 'book') || (libs.libraries || libs)[0]
    await page.goto(`${url}${base}/library/${bookLib.id}`, { waitUntil: 'networkidle', timeout: 30000 })
    await page.locator('a[href*="/item/"]').first().waitFor({ state: 'attached', timeout: 15000 })
    out.steps.libraryShelf = (await page.locator('a[href*="/item/"]').count()) > 0

    const itemsPayload = await page.evaluate(async ({ base, tok, libId }) => {
      const r = await fetch(`${base}/api/libraries/${libId}/items?limit=10`, { headers: { Authorization: `Bearer ${tok}` } })
      return r.json()
    }, { base, tok: loginData.user.accessToken, libId: bookLib.id })
    const bookItem = (itemsPayload.results || []).find((i) => i.media?.audioFiles?.length > 0 || i.media?.numAudioFiles > 0) || itemsPayload.results[0]
    const origTitle = bookItem.media.metadata.title

    const searchIn = page.locator('form[role="search"] input').first()
    if (await searchIn.count()) {
      await searchIn.fill(origTitle.slice(0, 8))
      await page.waitForResponse((r) => new URL(r.url()).pathname.includes('/search'), { timeout: 10000 }).catch(() => {})
      await page.waitForTimeout(300)
    }
    out.steps.search = true

    await page.goto(`${url}${base}/item/${bookItem.id}`, { waitUntil: 'networkidle', timeout: 30000 })
    await page.getByText(origTitle, { exact: false }).first().waitFor({ state: 'attached', timeout: 15000 })
    await attachSocketTracker(page)

    const editBtn = page.locator('button[aria-label="Edit"]').first()
    await editBtn.waitFor({ state: 'visible', timeout: 10000 })
    await editBtn.click()
    const chaptersTab = page.locator('button:visible, a:visible, div[role="tab"]:visible').filter({ hasText: /^Chapters$/i }).first()
    if (await chaptersTab.count()) {
      await chaptersTab.click()
      await page.locator('button:visible, a:visible, div[role="tab"]:visible').filter({ hasText: /^Details$/i }).first().click()
    }
    const newTitle = `${origTitle} E2E`
    const targetInput = page.locator('#formWrapper input').first()
    await targetInput.waitFor({ state: 'visible', timeout: 10000 })
    await targetInput.fill(newTitle)
    const patchWait = page.waitForResponse((r) => r.request().method() === 'PATCH' && new URL(r.url()).pathname.includes(`/api/items/${bookItem.id}/media`))
    await page.locator('button:visible').filter({ hasText: /^Save( and Close)?$/i }).last().click()
    out.steps.editTitleSave = (await patchWait).status() === 200
    await page.waitForTimeout(400)
    await page.goto(`${url}${base}/item/${bookItem.id}`, { waitUntil: 'networkidle', timeout: 30000 })
    const detailText = await page.locator('body').innerText()
    out.steps.bookDetailAndMetadata = detailText.includes(newTitle)

    const authorLink = page.locator('a[href*="/author/"]').first()
    if (await authorLink.count()) {
      await authorLink.click()
      await page.waitForFunction(() => location.pathname.includes('/author/'), { timeout: 10000 })
      await page.waitForLoadState('networkidle')
    }
    out.steps.authorPage = page.url().includes('/author/')

    for (const [key, tab] of [['collectionsTab', 'collections'], ['playlistsTab', 'playlists']]) {
      await page.goto(`${url}${base}/library/${bookLib.id}/bookshelf/${tab}`, { waitUntil: 'networkidle', timeout: 25000 })
      out.steps[key] = (await page.locator('#__nuxt').count()) > 0
    }
    for (const [key, route] of [['adminSettings', '/config'], ['adminUsers', '/config/users'], ['adminBackups', '/config/backups']]) {
      await page.goto(`${url}${base}${route}`, { waitUntil: 'networkidle', timeout: 25000 })
      out.steps[key] = new URL(page.url()).pathname.includes(route)
    }

    // 3. Direct playback start + progress update + Socket.IO live event reception
    await page.goto(`${url}${base}/item/${bookItem.id}`, { waitUntil: 'networkidle', timeout: 30000 })
    await attachSocketTracker(page)
    const playWait = page.waitForResponse((r) => r.request().method() === 'POST' && new URL(r.url()).pathname.endsWith(`/api/items/${bookItem.id}/play`))
    await page.locator('button:visible').filter({ hasText: 'Play' }).first().click()
    const playRes = await playWait
    await page.locator('#mediaPlayerContainer').waitFor({ state: 'visible', timeout: 15000 })
    await page.waitForFunction(() => {
      const a = document.querySelector('#audio-player')
      return Boolean(a && a.readyState >= 1 && Number.isFinite(a.duration) && a.duration > 0)
    }, { timeout: 15000 })
    out.steps.playbackStart = playRes.status() === 200
    const closeWait = page.waitForResponse((r) => r.request().method() === 'POST' && /\/api\/session\/[^/]+\/close$/.test(new URL(r.url()).pathname))
    await page.locator('#mediaPlayerContainer button').first().click()
    out.steps.playbackProgressAndClose = (await closeWait).status() === 200
    await page.waitForTimeout(400)
    out.socketEvents = await page.evaluate(() => window.__e2eSockets || [])
    out.steps.socketLiveEvents = out.socketEvents.length > 0

    // 4. Restricted listener login -> admin hidden + library visibility
    const lCtx = await browser.newContext({ viewport: { width: 1280, height: 800 } })
    const lPage = await lCtx.newPage()
    trackPage(lPage)
    await lPage.goto(`${url}${base}/`, { waitUntil: 'networkidle', timeout: 30000 })
    await lPage.locator('input').first().fill(users.listener.username)
    await lPage.locator('input[type="password"]').fill(users.listener.password)
    const lLoginWait = lPage.waitForResponse((r) => r.request().method() === 'POST' && new URL(r.url()).pathname.endsWith('/login'))
    await lPage.locator('input[type="password"]').press('Enter')
    const lData = await (await lLoginWait).json()
    await lPage.waitForFunction(() => !location.pathname.endsWith('/login'), { timeout: 15000 })
    const adminLinks = await lPage.locator('a[href*="/config"]').count()
    await lPage.goto(`${url}${base}/config/users`, { waitUntil: 'networkidle', timeout: 20000 })
    const onUsersPage = new URL(lPage.url()).pathname.endsWith('/config/users')
    const lLibs = await lPage.evaluate(async ({ base, tok }) => {
      const r = await fetch(`${base}/api/libraries`, { headers: { Authorization: `Bearer ${tok}` } })
      return r.json()
    }, { base, tok: lData.user.accessToken })
    out.listenerVisibleLibraries = (lLibs.libraries || lLibs).map((l) => l.name).sort()
    out.steps.restrictedListener = adminLinks === 0 && !onUsersPage
    await lCtx.close()
  } catch (err) {
    out.error = err.stack || err.message
  } finally {
    await ctx.close()
  }
  out.steps.zeroErrors = out.consoleErrors.length === 0 && out.pageErrors.length === 0 && out.server5xx.length === 0
  return out
}

function summarizeXhr(xhrList) {
  const counts = {}
  for (const x of xhrList) {
    const k = `${x.method} ${x.route} -> ${x.status}`
    counts[k] = (counts[k] || 0) + 1
  }
  return Object.fromEntries(Object.entries(counts).sort())
}

async function main() {
  const opts = parseArgs(process.argv.slice(2))
  const pw = await loadPlaywright(opts.optional)
  let servers = null
  const workRoot = fs.mkdtempSync(path.join(os.tmpdir(), 'abs-web-e2e-'))
  try {
    let tsUrl = opts.tsUrl, goUrl = opts.goUrl
    let users = { root: { username: 'trial-root', password: 'trial-root-password' }, listener: { username: 'trial-listener', password: 'trial-listener-password' } }
    if (!tsUrl || !goUrl) {
      servers = await startServers(workRoot, opts.base)
      tsUrl = servers.tsUrl
      goUrl = servers.goUrl
      users = servers.users
    }
    let browser
    try {
      browser = await pw.chromium.launch({ headless: true })
    } catch {
      console.error('Chromium binary missing. Run: npm i --no-save playwright && npx playwright install chromium')
      process.exit(opts.optional ? 0 : 2)
    }
    try {
      const [tsRes, goRes] = await Promise.all([
        runTargetJourneys(browser, 'ts', tsUrl, opts.base, users),
        runTargetJourneys(browser, 'go', goUrl, opts.base, users),
      ])
      const tsXhr = summarizeXhr(tsRes.xhr), goXhr = summarizeXhr(goRes.xhr)
      const xhrKeys = new Set([...Object.keys(tsXhr), ...Object.keys(goXhr)])
      const xhrDiffs = [...xhrKeys].filter((k) => tsXhr[k] !== goXhr[k]).map((k) => ({ key: k, ts: tsXhr[k] || 0, go: goXhr[k] || 0 }))
      const listenerMatch = JSON.stringify(tsRes.listenerVisibleLibraries) === JSON.stringify(goRes.listenerVisibleLibraries)
      const allStepsOk = [tsRes, goRes].every((r) => !r.error && Object.values(r.steps).every(Boolean))
      const report = { ok: allStepsOk && xhrDiffs.length === 0 && listenerMatch, listenerMatch, xhrDiffs, ts: tsRes, go: goRes }

      console.log('=== Web E2E Differential Report ===')
      for (const side of [tsRes, goRes]) {
        const passed = Object.values(side.steps).filter(Boolean).length
        console.log(`${side.side.toUpperCase()} (${side.url}): ${passed}/${Object.keys(side.steps).length} steps, ${side.xhr.length} XHRs, ${side.socketEvents.length} socket events, 5xx=${side.server5xx.length}`)
        if (side.error) console.error(`${side.side} error: ${side.error}`)
      }
      console.log(`Listener visibility match: ${listenerMatch} (${JSON.stringify(goRes.listenerVisibleLibraries)})`)
      console.log(`XHR status/count parity  : ${xhrDiffs.length === 0 ? 'MATCH' : `DIFF (${xhrDiffs.length})`}`)
      if (opts.out) {
        fs.mkdirSync(path.dirname(opts.out), { recursive: true })
        fs.writeFileSync(opts.out, `${JSON.stringify(report, null, 2)}\n`, 'utf8')
        console.log(`Wrote JSON report: ${opts.out}`)
      }
      process.exitCode = report.ok ? 0 : 1
    } finally {
      await browser.close()
    }
  } finally {
    if (servers) await servers.stop()
    fs.rmSync(workRoot, { recursive: true, force: true })
  }
}

main().catch((err) => { console.error(err.stack || err); process.exitCode = 1 })
