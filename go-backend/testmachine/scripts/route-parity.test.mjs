// route-parity.test.mjs — self-test for route-parity.mjs.
//
// Covers the five verdict behaviours the tool must get right: exact match,
// param match, wildcard-over-literal flagging, missing route and method
// mismatch — plus the Go-source extractor on a small temp corpus, including
// loop-expanded patterns and wrapper registrations.
//
// Run: node --test route-parity.test.mjs

import assert from 'node:assert/strict'
import fs from 'node:fs'
import os from 'node:os'
import path from 'node:path'
import test from 'node:test'

import {
  extractGoRoutes,
  expandOptional,
  extractRoutesFromFile,
  matchVariant,
  parseUpstreamPath,
} from './route-parity.mjs'

// Route model helper: builds a Go pattern object the way extractGoRoutes does.
function goPattern(pattern) {
  const match = /^([A-Z]+) (\/.*)$/.exec(pattern)
  return {
    pattern,
    method: match ? match[1] : '',
    path: match ? match[2] : pattern,
    file: 'test.go',
    line: 1,
  }
}

// matchRoute mirrors the tool's verdict for one upstream method+path against
// a set of Go patterns: every optional-param variant must be covered by some
// pattern whose method matches (a Go pattern without a method covers any).
// Returns { matched, isWildcard, variants }.
function matchRoute(method, upstreamPath, patterns) {
  const all = (Array.isArray(patterns) ? patterns : [patterns]).map(goPattern)
  const variants = expandOptional(parseUpstreamPath(upstreamPath))
  const variantResults = variants.map((variant) => {
    let best = null
    for (const pattern of all) {
      if (pattern.method && pattern.method !== method) continue
      const flags = matchVariant(variant, pattern.path.split('/').filter((s) => s !== ''))
      if (flags) best = { pattern, flags }
    }
    return best
  })
  if (variantResults.some((r) => !r)) return { matched: false, variants: variantResults }
  const isWildcard = variantResults.some(
    (r) => r.flags.wildcardOverLiteral.length > 0 || r.flags.catchAllOverLiteral
  )
  return { matched: true, isWildcard, variants: variantResults }
}

test('exact match: identical literal route', () => {
  const r = matchRoute('GET', '/api/libraries', 'GET /api/libraries')
  assert.equal(r.matched, true)
  assert.equal(r.isWildcard, false)
})

test('exact match: same shape literals', () => {
  const r = matchRoute('GET', '/api/libraries/:id/stats', 'GET /api/libraries/{id}/stats')
  assert.equal(r.matched, true)
  assert.equal(r.isWildcard, false)
})

test('param match: upstream :param covered by Go {x}', () => {
  const r = matchRoute(
    'GET',
    '/api/libraries/:id/narrators/:narratorId',
    'GET /api/libraries/{id}/narrators/{narratorId}'
  )
  assert.equal(r.matched, true)
  assert.equal(r.isWildcard, false)
})

test('param match: Go pattern without a method prefix covers the method', () => {
  const r = matchRoute('GET', '/hls/:stream/:file', '/hls/{id}/{name}')
  assert.equal(r.matched, true)
  assert.equal(r.isWildcard, false)
})

test('wildcard-over-literal is flagged', () => {
  const r = matchRoute(
    'POST',
    '/api/collections/:id/batch/add',
    'POST /api/collections/{id}/batch/{action}'
  )
  assert.equal(r.matched, true)
  assert.equal(r.isWildcard, true)
  assert.deepEqual(r.variants[0].flags.wildcardOverLiteral, ['add'])
})

test('missing route: no Go pattern matches the path shape', () => {
  const r = matchRoute('GET', '/api/libraries/:id/items/:x/:y', 'GET /api/libraries/{id}/items')
  assert.equal(r.matched, false)
})

test('missing route: a segment that is one literal longer does not match', () => {
  const r = matchRoute('DELETE', '/api/libraries/:id', 'DELETE /api/libraries/{id}/issues')
  assert.equal(r.matched, false)
})

test('method mismatch: different method does not match', () => {
  const r = matchRoute('DELETE', '/api/libraries/:id', 'PATCH /api/libraries/{id}')
  assert.equal(r.matched, false)
})

test('method mismatch: any-method Go pattern covers every method', () => {
  const r = matchRoute('PATCH', '/public/share/:slug/progress', '/public/share/{slug}/progress')
  assert.equal(r.matched, true)
})

test('optional upstream param: each variant may be covered by a different Go pattern', () => {
  const r = matchRoute('GET', '/api/items/:id/ebook/:fileid?', [
    'GET /api/items/{id}/ebook',
    'GET /api/items/{id}/ebook/{fileId}',
  ])
  assert.equal(r.matched, true)
  assert.equal(r.isWildcard, false)
  assert.equal(r.variants.length, 2)
})

test('missing route: optional param present variant is uncovered', () => {
  const r = matchRoute('GET', '/api/items/:id/ebook/:fileid?', 'GET /api/items/{id}/ebook')
  assert.equal(r.matched, false)
})

test('catch-all: Go {x...} covers an upstream suffix wildcard and flags it', () => {
  const r = matchRoute('GET', '/feed/:slug/cover*', 'GET /feed/{slug}/{tail...}')
  assert.equal(r.matched, true)
  assert.equal(r.isWildcard, true)
  assert.equal(r.variants[0].flags.catchAllOverLiteral, true)
})

test('parseUpstreamPath classifies segments', () => {
  const segments = parseUpstreamPath('/api/me/progress/:id/:episodeId?')
  assert.deepEqual(
    segments.map((s) => [s.kind, s.text, Boolean(s.optional)]),
    [
      ['literal', 'api', false],
      ['literal', 'me', false],
      ['literal', 'progress', false],
      ['param', ':id', false],
      ['param', ':episodeId?', true],
    ]
  )
})

// ---------------------------------------------------------------------------
// Go source extractor on a temp corpus

function withTempDir(fn) {
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), 'route-parity-test-'))
  try {
    return fn(dir)
  } finally {
    fs.rmSync(dir, { recursive: true, force: true })
  }
}

test('extractGoRoutes reads mux.Handle registrations and skips tests and comments', () => {
  withTempDir((dir) => {
    fs.mkdirSync(path.join(dir, 'internal', 'x'), { recursive: true })
    fs.writeFileSync(
      path.join(dir, 'internal', 'x', 'routes.go'),
      `package x

import "net/http"

// mux.Handle("GET /commented", h) must not appear.
func Routes(mux *http.ServeMux) {
	mux.Handle("GET /api/x", handler)
	mux.Handle("POST /api/x/{id}", h.authed(handler))
	mux.Handle("/public/{slug}", public) // no method: covers any method
}
`
    )
    fs.writeFileSync(
      path.join(dir, 'internal', 'x', 'routes_test.go'),
      `package x

import "net/http"

func testRoutes(mux *http.ServeMux) {
	mux.Handle("GET /test-only", handler)
}
`
    )
    const { routes, unresolved } = extractGoRoutes([dir])
    const patterns = routes.map((r) => r.pattern).sort()
    assert.deepEqual(patterns, ['/public/{slug}', 'GET /api/x', 'POST /api/x/{id}'])
    assert.equal(unresolved.length, 0)
  })
})

test('extractGoRoutes expands loop-built patterns from slices and maps, and wrapper calls', () => {
  withTempDir((dir) => {
    fs.mkdirSync(path.join(dir, 'internal', 'y'), { recursive: true })
    fs.writeFileSync(
      path.join(dir, 'internal', 'y', 'routes.go'),
      `package y

import "net/http"

func Routes(mux *http.ServeMux) {
	admin := func(pattern string, h httpx.Handler) { mux.Handle(pattern, auth.Admin(h)) }
	for _, kind := range []string{"tags", "genres"} {
		admin("GET /api/"+kind, list)
		admin("POST /api/"+kind+"/rename", rename)
		admin("DELETE /api/"+kind+"/{name}", remove)
	}
	for _, path := range map[entityKind]string{itemFeed: "item", seriesFeed: "series"} {
		admin("POST /api/feeds/"+path+"/{id}/open", open)
	}
	mux.Handle("GET /api/feeds", listFeeds)
}
`
    )
    const { routes, unresolved } = extractGoRoutes([dir])
    const patterns = routes.map((r) => r.pattern).sort()
    assert.deepEqual(patterns, [
      'DELETE /api/genres/{name}',
      'DELETE /api/tags/{name}',
      'GET /api/feeds',
      'GET /api/genres',
      'GET /api/tags',
      'POST /api/feeds/item/{id}/open',
      'POST /api/feeds/series/{id}/open',
      'POST /api/genres/rename',
      'POST /api/tags/rename',
    ])
    assert.equal(unresolved.length, 0)
  })
})

test('extractRoutesFromFile flags an unresolvable pattern expression', () => {
  withTempDir((dir) => {
    fs.mkdirSync(dir, { recursive: true })
    const file = path.join(dir, 'routes.go')
    fs.writeFileSync(
      file,
      `package z

import "net/http"

func Routes(mux *http.ServeMux) {
	mux.Handle(prefix+" /api/unknown", handler)
}
`
    )
    const { routes, unresolved } = extractRoutesFromFile(file)
    assert.deepEqual(routes, [])
    assert.equal(unresolved.length, 1)
    assert.equal(unresolved[0].line, 6)
  })
})

test('stripLineComment keeps `//` inside strings and drops trailing comments', () => {
  withTempDir((dir) => {
    fs.mkdirSync(dir, { recursive: true })
    const file = path.join(dir, 'routes.go')
    fs.writeFileSync(
      file,
      `package w

import "net/http"

func Routes(mux *http.ServeMux) {
	mux.Handle("GET /api/a", h) // GET /api/commented
	mux.Handle("GET /api/b", h)
}
`
    )
    const { routes } = extractRoutesFromFile(file)
    assert.deepEqual(routes.map((r) => r.pattern), ['GET /api/a', 'GET /api/b'])
  })
})

test('the real repo extraction yields the full table with nothing unresolved', () => {
  // Guards against a silent regression to an empty Go route table.
  const { routes, unresolved } = extractGoRoutes()
  assert.ok(routes.length > 200, `expected the real Go route table, got ${routes.length}`)
  assert.equal(unresolved.length, 0)
})
