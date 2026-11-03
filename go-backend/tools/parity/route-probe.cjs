#!/usr/bin/env node
'use strict'

// Route probe: every route the pinned upstream registers must be handled by Go.
//
// The Go server answers a route it has no handler for with
//   501 {"error":"This route is not implemented in the experimental Go backend"}
// A client (the iOS app hit GET /api/authors/:id and /api/authors/:id/image)
// then breaks in production while every differential and trial stays green,
// because those only exercise routes somebody thought to write a case for.
//
// This probe removes the "somebody thought of it" step:
//  1. it parses `this.router.<method>('<path>'` out of the three upstream
//     router files (server/routers/ApiRouter.js mounted at /api,
//     PublicRouter.js at /public, HlsRouter.js at /hls; see Server.js);
//  2. it takes the seeded installation of replacement-trial.cjs (upstream
//     creates it; --work DIR reuses an existing seeded trial work dir,
//     otherwise a fresh one is seeded), restores the pristine snapshot and
//     boots Go on it;
//  3. it substitutes real ids (library, item, author, series) into the paths,
//     sends every route as root (GET first, destructive DELETEs of the real
//     entities last) and reports the routes whose response is the fallback.
//
// It checks reachability, not parity: any other status (400, 404, 500, ...)
// passes here and is the job of the differential and trial. It never needs
// the network: nothing it sends carries a URL, and the Go process is started
// with a dead provider proxy so an accidental outbound call fails fast.
//
// ALLOWLIST is the explicit list of routes known to be missing. The probe
// fails when a route outside the list hits the fallback AND when a route on
// the list no longer does (a stale entry), so the list can only shrink.
//
// Usage: node route-probe.cjs [--work DIR] [--go-bin PATH]
// Mutates DIR/install (restored from DIR/snapshot first) and, for
// POST /api/libraries/:id/remove-metadata and similar, DIR/media: use a work
// dir that is not reused for a trial afterwards (the CI step runs it last).

const fs = require('node:fs')
const os = require('node:os')
const path = require('node:path')
const { freePort, stop } = require('./lib/process.cjs')
const { parseArgs } = require('./lib/args.cjs')
const trial = require('./replacement-trial.cjs')

const ROOT = path.resolve(__dirname, '../../..')
const BASE = '/audiobookshelf'

const ROUTER_FILES = [
  ['server/routers/ApiRouter.js', '/api'],
  ['server/routers/PublicRouter.js', '/public'],
  ['server/routers/HlsRouter.js', '/hls']
]

// Routes known to reach the fallback. Format: 'METHOD /full/path' exactly as
// registered upstream (with the router mount prefix).
const ALLOWLIST = [
  'GET /api/authors/:id',
  'PATCH /api/authors/:id',
  'DELETE /api/authors/:id',
  'POST /api/authors/:id/match',
  'GET /api/authors/:id/image',
  'POST /api/authors/:id/image',
  'DELETE /api/authors/:id/image',
  'GET /api/series/:id',
  'PATCH /api/series/:id',
  'POST /api/items/batch/update',
  'POST /api/items/batch/delete',
  'POST /api/items/batch/quickmatch',
  'POST /api/items/:id/match',
  'GET /api/libraries/:id/matchall',
  'GET /api/search/podcast',
  'GET /api/libraries/:id/opml',
  'GET /api/libraries/:id/download',
  'POST /api/libraries/:id/remove-metadata'
]

function upstreamRoutes() {
  const routes = []
  for (const [file, prefix] of ROUTER_FILES) {
    const src = fs.readFileSync(path.join(ROOT, file), 'utf8')
    for (const m of src.matchAll(/this\.router\.(get|post|patch|put|delete)\(\s*'([^']+)'/g)) {
      routes.push({ method: m[1].toUpperCase(), path: prefix + m[2], file })
    }
  }
  return routes
}

function realIds(dbPath) {
  const { DatabaseSync } = require('node:sqlite')
  const db = new DatabaseSync(dbPath, { readOnly: true })
  try {
    const one = (sql) => db.prepare(sql).get()?.id
    return {
      library: one("SELECT id FROM libraries WHERE mediaType = 'book' ORDER BY createdAt LIMIT 1"),
      item: one("SELECT id FROM libraryItems WHERE mediaType = 'book' ORDER BY createdAt LIMIT 1"),
      // An author with books, so handlers get past the empty-author shortcuts.
      author: one('SELECT authorId AS id FROM bookAuthors GROUP BY authorId ORDER BY COUNT(*) DESC, authorId LIMIT 1') || one('SELECT id FROM authors LIMIT 1'),
      series: one('SELECT id FROM series LIMIT 1')
    }
  } finally {
    db.close()
  }
}

const ZERO = '00000000-0000-4000-8000-000000000000'

function resolvePath(routePath, ids) {
  return routePath
    .replace(/^\/api\/libraries\/:id/, `/api/libraries/${ids.library}`)
    .replace(/^\/api\/items\/:id/, `/api/items/${ids.item}`)
    .replace(/^\/api\/authors\/:id/, `/api/authors/${ids.author}`)
    .replace(/^\/api\/series\/:id/, `/api/series/${ids.series}`)
    .replace(/:[A-Za-z]+\??/g, ZERO)
}

// Requests that remove the real entities must run after everything else, or
// the later requests would probe a 404 instead of the handler.
function order(routes) {
  const rank = (r) => {
    if (r.method === 'GET') return 0
    if (r.method !== 'DELETE') return 1
    return /^\/api\/(libraries|items|authors|series)\/:id$/.test(r.path) ? 3 : 2
  }
  return routes.map((r, i) => ({ r, i })).sort((a, b) => rank(a.r) - rank(b.r) || a.i - b.i).map(x => x.r)
}

function isFallback(status, text) {
  if (status !== 501) return false
  try { return /not implemented/i.test(JSON.parse(text)?.error || '') } catch { return /not implemented/i.test(text) }
}

async function main() {
  const args = parseArgs()
  const work = path.resolve(args.work || fs.mkdtempSync(path.join(os.tmpdir(), 'abs-route-probe-')))
  fs.mkdirSync(work, { recursive: true })
  const goBin = path.resolve(args['go-bin'] || path.join(ROOT, 'go-backend', 'audiobookshelf-go'))
  if (!args['go-bin']) trial.sh('go', ['build', '-o', goBin, './cmd/audiobookshelf-go'], { cwd: path.join(ROOT, 'go-backend'), env: { ...process.env, CGO_ENABLED: '0' } })

  const dirs = { config: path.join(work, 'install', 'config'), metadata: path.join(work, 'install', 'metadata') }
  if (fs.existsSync(path.join(work, 'ids.json')) && fs.existsSync(path.join(work, 'snapshot'))) {
    console.log(`route probe: reusing seeded installation in ${work}`)
    trial.copyTree(path.join(work, 'snapshot'), path.join(work, 'install'))
  } else {
    console.log(`route probe: seeding an installation with upstream in ${work} (slow; pass --work of a trial run to reuse)`)
    await trial.seedInstallation(work)
    trial.copyTree(path.join(work, 'snapshot'), path.join(work, 'install'))
  }
  const ids = realIds(path.join(dirs.config, 'absdatabase.sqlite'))
  for (const [k, v] of Object.entries(ids)) if (!v) throw new Error(`seeded installation has no ${k}`)

  const routes = upstreamRoutes()
  if (routes.length < 200) throw new Error(`parsed only ${routes.length} upstream routes; the router files changed shape`)

  // A dead proxy: if a handler ever reaches for the network the request fails
  // at once instead of leaving the machine.
  const deadProxy = 'http://127.0.0.1:9'
  process.env.HTTP_PROXY = process.env.HTTPS_PROXY = deadProxy
  process.env.http_proxy = process.env.https_proxy = deadProxy
  process.env.NO_PROXY = process.env.no_proxy = '127.0.0.1,localhost'
  const server = await trial.startGo(goBin, dirs, await freePort(), path.join(work, 'route-probe-go.log'))
  const missing = []
  const errors = []
  try {
    const token = (await trial.loginAs(trial.api(server), trial.ROOT_USER)).accessToken
    for (const route of order(routes)) {
      const target = resolvePath(route.path, ids)
      const label = `${route.method} ${route.path}`
      try {
        const response = await fetch(`${server.baseUrl}${BASE}${target}`, {
          method: route.method,
          headers: { authorization: `Bearer ${token}`, 'content-type': 'application/json' },
          body: route.method === 'GET' ? undefined : '{}',
          signal: AbortSignal.timeout(15000)
        })
        const text = await response.text()
        if (isFallback(response.status, text)) missing.push(label)
      } catch (error) {
        errors.push(`${label}: ${error.message}`)
      }
    }
  } finally {
    await stop(server)
  }

  const unique = (list) => [...new Set(list)]
  const allow = new Set(ALLOWLIST)
  const registered = new Set(routes.map(r => `${r.method} ${r.path}`))
  const unexpected = unique(missing).filter(l => !allow.has(l))
  const stale = ALLOWLIST.filter(l => !unique(missing).includes(l))
  const unknown = ALLOWLIST.filter(l => !registered.has(l))
  console.log(`route probe: ${routes.length} upstream routes sent to Go; ${unique(missing).length} hit the not-implemented fallback; allowlist ${ALLOWLIST.length}`)
  for (const e of errors) console.log(`  request error (not a fallback): ${e}`)
  for (const l of unexpected) console.log(`  NOT IMPLEMENTED: ${l}`)
  for (const l of stale) console.log(`  stale allowlist entry (route is handled now; remove it): ${l}`)
  for (const l of unknown) console.log(`  allowlist entry names no upstream route: ${l}`)
  if (unexpected.length || stale.length || unknown.length) process.exitCode = 1
}

main().catch((error) => { console.error(error); process.exitCode = 1 })
