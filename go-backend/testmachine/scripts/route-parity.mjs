#!/usr/bin/env node
// route-parity.mjs — static route-inventory parity between pinned upstream
// TypeScript v2.37.2 and the Go rewrite.
//
// Input A: go-backend/testmachine/contracts/source-inventory.json — every
// route registration site in the pinned upstream sources (method + path +
// source location), machine-extracted by scripts/source-inventory.mjs.
// Input B: the Go route table, extracted statically from non-test .go files
// under go-backend/internal and go-backend/cmd: the first string argument of
// every `X.Handle(` call, plus local wrapper calls (`admin`, `onItem`, ...)
// defined as `name := func(pattern string, ...)` that forward to `.Handle(`.
// Loop-built patterns are expanded from their `[]string{...}` or
// `map[...]string{...}` range literals (taxonomy tags/genres, feeds open).
//
// Verdict: exit 0 when every upstream method+path site is covered by some Go
// pattern with the same method (a Go pattern without a method prefix covers
// any method). Go `{x}` wildcards may stand in for upstream `:x` params and
// upstream literals; a match whose pattern is strictly more general than the
// upstream route — a wildcard over a literal, or a `{x...}` catch-all over a
// literal — is flagged "wildcard-covered" so it can be verified by a test
// (see reports/coverage.md "Wildcard matches", the 8 known cases).
// Exit 1 with a per-route report when routes are missing.
//
// Usage:
//   node route-parity.mjs            # recompute and (re)write the report
//   node route-parity.mjs --check    # recompute and compare with the report

import fs from 'node:fs'
import path from 'node:path'
import process from 'node:process'
import { fileURLToPath, pathToFileURL } from 'node:url'

const SCRIPT_DIR = path.dirname(fileURLToPath(import.meta.url))
const TESTMACHINE = path.resolve(SCRIPT_DIR, '..')
const GO_BACKEND = path.resolve(TESTMACHINE, '..')
const INVENTORY = path.join(TESTMACHINE, 'contracts', 'source-inventory.json')
const REPORT = path.join(TESTMACHINE, 'reports', 'route-parity.json')
const SCAN_DIRS = ['internal', 'cmd'].map((dir) => path.join(GO_BACKEND, dir))

const METHODS = new Set(['GET', 'POST', 'PATCH', 'DELETE', 'PUT', 'HEAD', 'OPTIONS'])

// ---------------------------------------------------------------------------
// Go route extraction

// Returns the line with comments removed; a `//` inside a string literal
// (e.g. "http://") is kept. Handles only line comments, which is all this
// codebase uses for route lines.
function stripLineComment(line) {
  let inString = false
  let inRune = false
  for (let i = 0; i < line.length; i++) {
    const ch = line[i]
    if (inString) {
      if (ch === '\\') i++
      else if (ch === '"') inString = false
    } else if (inRune) {
      if (ch === '\\') i++
      else if (ch === "'") inRune = false
    } else if (ch === '"') inString = true
    else if (ch === "'") inRune = true
    else if (ch === '/' && line[i + 1] === '/') return line.slice(0, i)
  }
  return line
}

// Wrapper names defined per file: `admin := func(pattern string, ...) {...}`
// or `onItem := func(pattern string, ...) {...}`. Their bodies forward the
// pattern to a `.Handle(` call unchanged.
const WRAPPER_DEF = /^\s*(\w+)\s*:?=\s*func\(\s*pattern\s+string/gm

// Reads the first argument of a call starting at `open` (index of `(`),
// respecting string literals and nesting. Returns the text up to the first
// top-level comma, or null at the end of the call.
function firstArgument(text, open) {
  let depth = 0
  let i = open + 1
  for (; i < text.length; i++) {
    const ch = text[i]
    if (ch === '"') {
      i++
      while (i < text.length && text[i] !== '"') {
        if (text[i] === '\\') i++
        i++
      }
      continue
    }
    if (ch === '(' || ch === '[') depth++
    else if (ch === ')' || ch === ']') {
      if (depth === 0) return null // end of the call itself, no comma seen
      depth--
    } else if (ch === ',' && depth === 0) break
  }
  if (i >= text.length) return null
  return text.slice(open + 1, i).trim()
}

// Splits a top-level expression on `+` operators, keeping string literals
// whole. Returns { parts, literal } where every part is a string literal
// (with quotes) or an identifier, or null when the expression is not a
// concatenation of those two forms.
function splitConcat(expr) {
  const parts = []
  let current = ''
  let inString = false
  for (let i = 0; i < expr.length; i++) {
    const ch = expr[i]
    if (inString) {
      current += ch
      if (ch === '\\') current += expr[++i] ?? ''
      else if (ch === '"') inString = false
      continue
    }
    if (ch === '"') {
      inString = true
      current += ch
    } else if (ch === '+') {
      parts.push(current.trim())
      current = ''
    } else {
      current += ch
    }
  }
  parts.push(current.trim())
  if (parts.some((p) => p === '')) return null
  for (const part of parts) {
    if (!(/^"(?:[^"\\]|\\.)*"$/.test(part) || /^\w+$/.test(part))) return null
  }
  return parts
}

function unquote(literal) {
  return JSON.parse(literal.replace(/^"(.*)"$/s, '"$1"'))
}

// Loop variables bound to string values, used to expand dynamically built
// patterns:
//   for _, kind := range []string{"tags", "genres"} {...}
//   for _, pattern := range []string{"/socket.io", "/socket.io/"} {...}
//   for kind, path := range map[entityKind]string{itemFeed: "item", ...} {...}
function collectLoopBindings(text) {
  const bindings = new Map() // name -> string[]
  const add = (name, values) => {
    const existing = bindings.get(name) ?? []
    bindings.set(name, [...new Set([...existing, ...values])])
  }
  const sliceRe = /for\s+_,\s*(\w+)\s*:?=\s*range\s+\[\]string\{([^}]*)\}/g
  for (const match of text.matchAll(sliceRe)) {
    const values = [...match[2].matchAll(/"((?:[^"\\]|\\.)*)"/g)].map((m) => m[1])
    add(match[1], values)
  }
  const mapRe = /for\s+(?:\w+,\s*(\w+)|_,\s*(\w+))\s*:?=\s*range\s+map\[[^\]]*\]string\{([^}]*)\}/g
  for (const match of text.matchAll(mapRe)) {
    const name = match[1] ?? match[2]
    const values = [...match[3].matchAll(/:\s*"((?:[^"\\]|\\.)*)"/g)].map((m) => m[1])
    add(name, values)
  }
  return bindings
}

// Extracts `{ "METHOD /path" | "/path", ... }` registrations from one file.
// Returns { routes: [{ pattern, line }], unresolved: [{ expr, line }] }.
function extractRoutesFromFile(file) {
  const raw = fs.readFileSync(file, 'utf8')
  const lines = raw.split('\n')
  const text = lines.map(stripLineComment).join('\n')
  const wrappers = [...text.matchAll(WRAPPER_DEF)].map((m) => m[1])
  const loopBindings = collectLoopBindings(text)

  const callRe = new RegExp(
    `(?:\\b[\\w.]+\\.Handle\\b${wrappers.length ? `|\\b(?:${wrappers.join('|')})\\b` : ''})\\s*\\(`,
    'g'
  )
  const routes = []
  const unresolved = []
  for (const match of text.matchAll(callRe)) {
    const open = match.index + match[0].length - 1
    const line = text.slice(0, match.index).split('\n').length
    const arg = firstArgument(text, open)
    if (!arg) continue
    const parts = splitConcat(arg)
    if (!parts) continue // not a string-expression first argument
    let values = ['']
    let ok = true
    for (const part of parts) {
      if (part.startsWith('"')) {
        values = values.map((v) => v + unquote(part))
      } else if (part === 'pattern') {
        // The forwarding parameter inside a wrapper definition, not a route.
        ok = false
        break
      } else if (loopBindings.has(part)) {
        values = values.flatMap((v) => loopBindings.get(part).map((w) => v + w))
      } else {
        unresolved.push({ expr: arg, line })
        ok = false
        break
      }
    }
    if (!ok) continue
    for (const value of values) {
      if (/^(?:[A-Z]+ )?\//.test(value)) routes.push({ pattern: value, line })
    }
  }
  return { routes, unresolved }
}

function walkGoFiles(dir) {
  const files = []
  for (const entry of fs.readdirSync(dir, { withFileTypes: true })) {
    const full = path.join(dir, entry.name)
    if (entry.isDirectory()) files.push(...walkGoFiles(full))
    else if (entry.isFile() && entry.name.endsWith('.go') && !entry.name.endsWith('_test.go')) {
      files.push(full)
    }
  }
  return files
}

// Extracts and dedupes the Go route table. A pattern is stored with its
// method ('' = any) and path; duplicate patterns keep the first location.
function extractGoRoutes(dirs = SCAN_DIRS) {
  const byPattern = new Map()
  const unresolved = []
  for (const dir of dirs) {
    if (!fs.existsSync(dir)) continue
    for (const file of walkGoFiles(dir)) {
      const { routes, unresolved: bad } = extractRoutesFromFile(file)
      const rel = path.relative(GO_BACKEND, file)
      for (const { pattern, line } of routes) {
        const methodMatch = /^([A-Z]+) (\/.*)$/.exec(pattern)
        const method = methodMatch && METHODS.has(methodMatch[1]) ? methodMatch[1] : ''
        const routePath = methodMatch && METHODS.has(methodMatch[1]) ? methodMatch[2] : pattern
        if (!byPattern.has(pattern)) {
          byPattern.set(pattern, { pattern, method, path: routePath, file: rel, line })
        }
      }
      for (const { expr, line } of bad) unresolved.push({ file: rel, expr, line })
    }
  }
  return { routes: [...byPattern.values()], unresolved }
}

// ---------------------------------------------------------------------------
// Upstream inventory and matching

// Upstream route sites that this static check does not compare against the Go
// table: the regex cache middleware, the runtime-built SPA fallback, and the
// `router.all('*')` catch-all (reports/coverage.md "Method and limits").
function classifySite(route) {
  if (route.method === 'ALL') return 'catch-all'
  if (route.route_kind === 'regex_literal') return 'regex'
  if (route.route_kind === 'dynamic_expression') return 'dynamic'
  if (route.route === '*') return 'catch-all'
  return null
}

// Full upstream path: the router mount (only when the extractor resolved it
// to a concrete prefix) plus the router-relative route. The runtime
// ROUTER_BASE_PATH prefix applies to both sides and is ignored here.
function upstreamFullPath(route) {
  const mount = route.prefix_context?.known_mount
  const prefix = typeof mount === 'string' && mount.startsWith('/') ? mount : ''
  return prefix + route.route
}

// Parses an Express route path into segments:
//   literal  — exact text that must match
//   param    — `:name`, one non-empty segment
//   star     — trailing wildcard, `*` or `cover*`; matches any remaining
//              characters (Express `*` spans segments), optionally after a
//              literal prefix
function parseUpstreamPath(routePath) {
  const segments = routePath.split('/').filter((s) => s !== '')
  return segments.map((segment, index) => {
    if (segment === '*') return { kind: 'star', prefix: '', text: segment }
    if (segment.endsWith('*')) return { kind: 'star', prefix: segment.slice(0, -1), text: segment }
    if (/^:\w+\?$/.test(segment)) return { kind: 'param', optional: true, text: segment }
    if (/^:\w+$/.test(segment)) return { kind: 'param', text: segment }
    if (index !== segments.length - 1 && segment.endsWith('*')) {
      // A star mid-path is not expected in this inventory; treat conservatively.
      return { kind: 'unmatchable', text: segment }
    }
    return { kind: 'literal', text: segment }
  })
}

// Expands `:name?` optional params into concrete variants; the route is
// covered only when every variant is covered.
function expandOptional(segments) {
  let variants = [[]]
  for (const segment of segments) {
    if (segment.kind === 'param' && segment.optional) {
      variants = variants.flatMap((v) => [[...v, { ...segment, optional: false }], [...v]])
    } else {
      variants = variants.map((v) => [...v, segment])
    }
  }
  return variants
}

// Tests one upstream variant against one Go pattern. Returns flags or null.
// Go segments: `{name}` = one non-empty segment, `{name...}` = trailing
// catch-all (zero or more segments, verified against net/http ServeMux).
function matchVariant(upSegments, goSegments) {
  const flags = {
    wildcardOverLiteral: [],
    catchAllOverLiteral: false,
    catchAllOverParams: false,
    methodAny: false,
  }
  let i = 0
  for (let k = 0; k < goSegments.length; k++) {
    const go = goSegments[k]
    const catchAll = /^\{(\w+)\.\.\.\}$/.exec(go)
    const wild = !catchAll && /^\{(\w+)\}$/.test(go)
    if (catchAll) {
      if (k !== goSegments.length - 1) return null // Go requires it trailing
      const consumed = upSegments.slice(i)
      i = upSegments.length
      for (const segment of consumed) {
        if (segment.kind === 'literal' || segment.kind === 'star') catchAllOverLiteralFlag(flags, segment)
        else if (segment.kind === 'param') flags.catchAllOverParams = true
      }
      continue
    }
    if (wild) {
      const segment = upSegments[i++]
      if (!segment) return null
      if (segment.kind === 'literal') flags.wildcardOverLiteral.push(segment.text)
      else if (segment.kind === 'unmatchable') return null
      // param matches are the normal case, no flag
      continue
    }
    // Go literal segment: it only covers an upstream literal of the same
    // text. A Go literal can never cover an upstream `:param` route — that
    // would be false coverage (a request with any other param value would
    // not match) — and it cannot cover a suffix wildcard either.
    const segment = upSegments[i++]
    if (!segment) return null
    if (segment.kind === 'literal' && segment.text === go) continue
    return null
  }
  if (i !== upSegments.length) return null
  return flags
}

function catchAllOverLiteralFlag(flags, segment) {
  // A star segment (`cover*`) carries its own literal prefix; a bare `*` or a
  // literal segment consumed by the catch-all counts.
  if (segment.kind === 'literal' || segment.prefix !== '') flags.catchAllOverLiteral = true
}

// Renders a parsed variant back to its path form, for reports.
function variantPath(variant) {
  return '/' + variant.map((s) => s.text).join('/')
}

function goSegmentsOf(patternPath) {
  return patternPath.split('/').filter((s) => s !== '')
}

function specificity(flags) {
  return (
    flags.wildcardOverLiteral.length * 4 +
    (flags.catchAllOverLiteral ? 2 : 0) +
    flags.catchAllOverParams ? 1 : 0
  )
}

function checkParity() {
  const inventory = JSON.parse(fs.readFileSync(INVENTORY, 'utf8'))
  const go = extractGoRoutes()
  const goRoutes = go.routes

  const results = []
  const counts = {
    total_upstream_sites: inventory.routes.length,
    excluded: 0,
    matched_exact: 0,
    wildcard_covered: 0,
    missing: 0,
  }
  const wildcardCoveredRoutes = []

  for (const route of inventory.routes) {
    const base = {
      method: route.method,
      route: route.route,
      source: route.source,
      line: route.line,
      route_kind: route.route_kind,
    }
    const excluded = classifySite(route)
    if (excluded) {
      counts.excluded++
      results.push({ ...base, status: 'excluded', reason: excluded })
      continue
    }
    const fullPath = upstreamFullPath(route)
    const variants = expandOptional(parseUpstreamPath(fullPath))
    // Each variant (an upstream route with all optional params either present
    // or absent) is matched on its own: Go often registers two literal
    // patterns where upstream has one `:param?` route, so a single pattern
    // does not have to cover both.
    const variantMatches = []
    for (const variant of variants) {
      let best = null
      let bestFlags = null
      for (const pattern of goRoutes) {
        if (pattern.method && pattern.method !== route.method) continue
        const flags = matchVariant(variant, goSegmentsOf(pattern.path))
        if (!flags) continue
        if (!best || specificity(flags) < specificity(bestFlags)) {
          best = pattern
          bestFlags = flags
        }
      }
      variantMatches.push({ variant, best, flags: bestFlags })
    }
    const unmatched = variantMatches.filter((m) => !m.best)
    if (unmatched.length > 0) {
      counts.missing++
      results.push({
        ...base,
        status: 'missing',
        upstream_path: fullPath,
        unmatched_variants: unmatched.map((m) => variantPath(m.variant)),
      })
      continue
    }
    const merged = {
      wildcardOverLiteral: [],
      catchAllOverLiteral: false,
      catchAllOverParams: false,
      methodAny: false,
    }
    for (const m of variantMatches) {
      merged.wildcardOverLiteral.push(...m.flags.wildcardOverLiteral)
      merged.catchAllOverLiteral = merged.catchAllOverLiteral || m.flags.catchAllOverLiteral
      merged.catchAllOverParams = merged.catchAllOverParams || m.flags.catchAllOverParams
      merged.methodAny = merged.methodAny || m.flags.methodAny
    }
    const isWildcard = merged.wildcardOverLiteral.length > 0 || merged.catchAllOverLiteral
    const entry = {
      ...base,
      status: isWildcard ? 'wildcard-covered' : 'matched',
      upstream_path: fullPath,
      matched_by_variants: variantMatches.map((m) => ({
        variant: variantPath(m.variant),
        pattern: m.best.pattern,
        file: `${m.best.file}:${m.best.line}`,
        flags: {
          wildcard_over_literals: [...new Set(m.flags.wildcardOverLiteral)],
          catch_all_over_literal: m.flags.catchAllOverLiteral,
          catch_all_over_params: m.flags.catchAllOverParams,
          go_pattern_without_method: m.flags.methodAny,
        },
      })),
    }
    if (isWildcard) {
      counts.wildcard_covered++
      wildcardCoveredRoutes.push(entry)
    } else {
      counts.matched_exact++
    }
    results.push(entry)
  }

  const summary = {
    source_revision: inventory.source_revision ?? null,
    counts: {
      total_upstream_sites: counts.total_upstream_sites,
      matched_exact: counts.matched_exact,
      wildcard_covered: counts.wildcard_covered,
      excluded: counts.excluded,
      missing: counts.missing,
      go_route_patterns: goRoutes.length,
      go_unresolved_patterns: go.unresolved.length,
    },
    excluded_sites: results
      .filter((r) => r.status === 'excluded')
      .map((r) => ({ method: r.method, route: r.route, source: r.source, line: r.line, reason: r.reason })),
    wildcard_covered_routes: wildcardCoveredRoutes.map((r) => ({
      method: r.method,
      path: r.upstream_path,
      source: `${r.source}:${r.line}`,
      matched_by: r.matched_by_variants[0].pattern + ' (' + r.matched_by_variants[0].file + ')',
      wildcard_over_literals: [...new Set(r.matched_by_variants.flatMap((v) => v.flags.wildcard_over_literals))],
    })),
    routes: results,
  }
  if (go.unresolved.length > 0) summary.unresolved_go_patterns = go.unresolved
  return summary
}

function compareReports(computed, committed) {
  const diffs = []
  const keys = new Set([...Object.keys(computed), ...Object.keys(committed)])
  for (const key of keys) {
    const a = JSON.stringify(computed[key])
    const b = JSON.stringify(committed[key])
    if (a !== b) diffs.push(key)
  }
  return diffs
}

function printSummary(summary) {
  const c = summary.counts
  console.log(
    `route-parity: upstream sites ${c.total_upstream_sites}, matched ${c.matched_exact}, ` +
      `wildcard-covered ${c.wildcard_covered}, excluded ${c.excluded}, missing ${c.missing}; ` +
      `Go patterns ${c.go_route_patterns}${c.go_unresolved_patterns ? `, unresolved ${c.go_unresolved_patterns}` : ''}`
  )
  for (const site of summary.excluded_sites) {
    console.log(`  excluded ${site.method} ${site.route} (${site.source}:${site.line}, ${site.reason})`)
  }
  for (const route of summary.wildcard_covered_routes) {
    console.log(`  wildcard ${route.method} ${route.path} <- ${route.matched_by}`)
  }
  for (const route of summary.routes) {
    if (route.status === 'missing') {
      console.log(`  MISSING ${route.method} ${route.upstream_path} (${route.source}:${route.line})`)
    }
  }
  if (summary.unresolved_go_patterns) {
    for (const item of summary.unresolved_go_patterns) {
      console.log(`  UNRESOLVED Go pattern ${item.expr} at ${item.file}:${item.line}`)
    }
  }
}

// Exports for route-parity.test.mjs. The CLI below only runs when this file
// is the entry point.
export {
  stripLineComment,
  firstArgument,
  splitConcat,
  collectLoopBindings,
  extractRoutesFromFile,
  extractGoRoutes,
  classifySite,
  upstreamFullPath,
  parseUpstreamPath,
  expandOptional,
  matchVariant,
  checkParity,
}

// ---------------------------------------------------------------------------
// CLI

const invokedDirectly = process.argv[1] && import.meta.url === pathToFileURL(process.argv[1]).href
if (invokedDirectly) {
  const check = process.argv.includes('--check')
  const summary = checkParity()
  printSummary(summary)

  const failed =
    summary.counts.missing > 0 ||
    summary.counts.go_unresolved_patterns > 0 ||
    summary.counts.total_upstream_sites !==
      summary.counts.matched_exact + summary.counts.wildcard_covered + summary.counts.excluded + summary.counts.missing

  if (check) {
    if (!fs.existsSync(REPORT)) {
      console.error(`route-parity --check: committed report not found at ${path.relative(process.cwd(), REPORT)}`)
      process.exit(1)
    }
    const committed = JSON.parse(fs.readFileSync(REPORT, 'utf8'))
    const diffs = compareReports(summary, committed)
    if (diffs.length > 0) {
      console.error(`route-parity --check: report drift in: ${diffs.join(', ')}`)
      process.exit(1)
    }
    if (failed) {
      console.error('route-parity --check: committed report matches but the parity verdict is failing')
      process.exit(1)
    }
    console.log('route-parity --check: committed report matches the recomputed result')
    process.exit(0)
  }

  fs.mkdirSync(path.dirname(REPORT), { recursive: true })
  fs.writeFileSync(REPORT, JSON.stringify(summary, null, 2) + '\n')
  console.log(`route-parity: report written to ${path.relative(process.cwd(), REPORT)}`)
  process.exit(failed ? 1 : 0)
}
