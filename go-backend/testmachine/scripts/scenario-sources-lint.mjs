#!/usr/bin/env node
// scenario-sources-lint.mjs — prove that every upstream citation a scenario
// declares in its `Sources: []string{...}` field resolves in the pinned upstream
// tree at the repository root (server/, client/, index.js, contract docs).
//
// The register validates the source refs of capabilities; the harness only
// checks that a scenario's Sources list is non-empty (harness/plan.go). This
// tool closes that hole: a citation must name a real file and real lines.
//
// Accepted citation forms (a citation may also omit the line part, which means
// "this whole file"):
//   server/controllers/UserController.js:116
//   server/controllers/UserController.js:116-194
//   server/scanner/BookScanner.js:225-232,484-494   (several ranges, comma-list)
//   server/controllers/UserController.js
//   server/Database.js (createTextSearchQuery)      (parenthetical note, ignored)
//   index.js:12
//   client/components/App.vue:343
//   ../client/components/App.vue:343-352   (upstream-relative spelling)
//   go-backend/testmachine/contracts/clients/official-app.md:139
//                                          (contract document: existence only,
//                                           the line part is accepted and ignored)
//
// Everything else is a finding, never a crash. The tool reads Go string
// literals (backquote or double-quote with standard escapes) through a small
// tokenizer, so comments and non-citation strings are not mistaken for sources.
// A Sources list is evaluated as a Go constant expression: string consts/vars,
// `+` concatenation and `append([]string{...}, x...)` are followed, so a
// citation split over a const and a literal suffix is checked as one value. A
// value that comes from a function parameter is reported as dynamic (not a
// finding); the literals at the call sites are still checked because every
// citation-shaped literal in a scenario file is validated.
//
// Usage:
//   node scenario-sources-lint.mjs [--root <repo>] [--report <json>]
//                                  [--scenarios <dir>]
// Exit 0 when every citation resolves, 1 on findings, 2 on a usage error.
// The scenarios/harness package is the toolkit, not scenario declarations, and
// is skipped (its `Sources []string` is the struct field type).

import fs from 'node:fs'
import path from 'node:path'
import { fileURLToPath } from 'node:url'
import { parseArgs } from 'node:util'

const SOURCE_EXTENSION = /\.(?:js|mjs|cjs|jsx|ts|tsx|vue|md)$/
const CONTRACT_PREFIX = 'go-backend/testmachine/contracts/'
const HARNESS_DIR = 'harness'
// Identifiers that appear inside a Sources expression but name types, builtins
// or literals rather than a citation variable.
const IGNORED_IDENTS = new Set(['string', 'append', 'make', 'nil', 'true', 'false', 'copy'])
// Tokens after which a newline does not end a Go statement.
const CONTINUATIONS = new Set(['(', '[', '{', ',', '=', ':=', '+', '|', '.', '...', '&', '*'])
const OPENERS = new Set(['(', '[', '{'])
const CLOSERS = new Set([')', ']', '}'])

const SCRIPT_ROOT = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '../../..')
const DEFAULT_SCENARIOS = 'go-backend/testmachine/scenarios'

// ---------------------------------------------------------------- tokenizer

function isIdentStart(ch) {
  return ch !== undefined && /[\p{L}_]/u.test(ch)
}

function isIdentPart(ch) {
  return ch !== undefined && /[\p{L}\p{N}_]/u.test(ch)
}

/** Decode the body of a Go string literal (quote already excluded). */
export function decodeGoString(raw, quote) {
  if (quote === '`') return raw
  let out = ''
  for (let i = 0; i < raw.length; i++) {
    const ch = raw[i]
    if (ch !== '\\') {
      out += ch
      continue
    }
    const esc = raw[++i]
    switch (esc) {
      case 'a': out += '\x07'; break
      case 'b': out += '\b'; break
      case 'f': out += '\f'; break
      case 'n': out += '\n'; break
      case 'r': out += '\r'; break
      case 't': out += '\t'; break
      case 'v': out += '\v'; break
      case '\\': out += '\\'; break
      case '"': out += '"'; break
      case "'": out += "'"; break
      case 'x': out += hexChar(raw.slice(i + 1, i + 3), 16, 2, raw.slice(i - 1, i + 3)); i += 2; break
      case 'u': out += hexChar(raw.slice(i + 1, i + 5), 16, 4, raw.slice(i - 1, i + 5)); i += 4; break
      case 'U': out += hexChar(raw.slice(i + 1, i + 9), 16, 8, raw.slice(i - 1, i + 9)); i += 8; break
      default:
        if (esc !== undefined && /[0-7]/.test(esc)) {
          out += hexChar(raw.slice(i, i + 3), 8, 3, raw.slice(i - 1, i + 3))
          i += 2
        } else {
          out += esc ?? ''
        }
    }
  }
  return out
}

/** Decode one escape; a malformed escape is kept verbatim, never guessed. */
function hexChar(digits, base, width, verbatim) {
  if (digits.length !== width || !/^[0-9a-fA-F]+$/.test(digits)) return verbatim
  const code = Number.parseInt(digits, base)
  if (!Number.isFinite(code) || code > 0x10ffff) return verbatim
  try {
    return String.fromCodePoint(code)
  } catch {
    return verbatim
  }
}

/**
 * Tokenize Go well enough to find identifiers and string literals: comments are
 * dropped, numbers and runes are skipped, and every token carries its 1-based
 * line and the number of newlines since the previous token.
 */
export function tokenizeGo(source) {
  const tokens = []
  const n = source.length
  let i = 0
  let line = 1
  let pendingNewlines = 0

  const push = (kind, text, value, startLine) => {
    tokens.push({ kind, text, value, line: startLine, newlinesBefore: pendingNewlines })
    pendingNewlines = 0
  }

  while (i < n) {
    const ch = source[i]
    if (ch === '\n') {
      line++
      pendingNewlines++
      i++
      continue
    }
    if (ch === ' ' || ch === '\t' || ch === '\r' || ch === '\v' || ch === '\f') {
      i++
      continue
    }
    if (ch === '/' && source[i + 1] === '/') {
      while (i < n && source[i] !== '\n') i++
      continue
    }
    if (ch === '/' && source[i + 1] === '*') {
      i += 2
      while (i < n && !(source[i] === '*' && source[i + 1] === '/')) {
        if (source[i] === '\n') {
          line++
          pendingNewlines++
        }
        i++
      }
      i += 2
      continue
    }
    if (ch === '"' || ch === '`') {
      const startLine = line
      const start = i
      let raw = ''
      i++
      while (i < n) {
        const c = source[i]
        if (ch === '"' && c === '\\') {
          raw += c
          i++
          if (i < n) {
            if (source[i] === '\n') line++
            raw += source[i]
            i++
          }
          continue
        }
        if (c === ch) {
          i++
          break
        }
        if (c === '\n') line++
        raw += c
        i++
      }
      push('string', source.slice(start, i), decodeGoString(raw, ch), startLine)
      continue
    }
    if (ch === "'") {
      const startLine = line
      const start = i
      i++
      while (i < n) {
        if (source[i] === '\\') {
          i += 2
          continue
        }
        if (source[i] === "'") {
          i++
          break
        }
        if (source[i] === '\n') {
          line++
          break
        }
        i++
      }
      push('rune', source.slice(start, i), null, startLine)
      continue
    }
    if (isIdentStart(ch)) {
      const start = i
      while (i < n && isIdentPart(source[i])) i++
      push('ident', source.slice(start, i), null, line)
      continue
    }
    if (/[0-9]/.test(ch) || (ch === '.' && /[0-9]/.test(source[i + 1] ?? ''))) {
      const start = i
      i++
      while (i < n && /[0-9A-Za-z_.]/.test(source[i])) {
        if (source[i] === '.' && !/[0-9]/.test(source[i + 1] ?? '')) break
        i++
      }
      push('number', source.slice(start, i), null, line)
      continue
    }
    if (source.startsWith('...', i)) {
      push('punct', '...', null, line)
      i += 3
      continue
    }
    if (source.startsWith(':=', i)) {
      push('punct', ':=', null, line)
      i += 2
      continue
    }
    push('punct', ch, null, line)
    i++
  }
  return tokens
}

// --------------------------------------------------------------- extraction

/** End index (exclusive) of the expression that starts at `start`. */
function expressionEnd(tokens, start) {
  let depth = 0
  for (let i = start; i < tokens.length; i++) {
    const token = tokens[i]
    if (token.kind !== 'punct') continue
    if (OPENERS.has(token.text)) depth++
    else if (CLOSERS.has(token.text)) {
      if (depth === 0) return i
      depth--
    } else if (token.text === ',' && depth === 0) {
      return i
    }
  }
  return tokens.length
}

/** End index (exclusive) of the initializer that follows `=` at `eq`. */
function initializerEnd(tokens, eq) {
  let depth = 0
  let end = eq + 1
  for (let i = eq + 1; i < tokens.length; i++) {
    const token = tokens[i]
    const previous = tokens[i - 1]
    if (depth === 0 && token.newlinesBefore > 0 && !CONTINUATIONS.has(previous?.text)) break
    if (token.kind === 'punct' && OPENERS.has(token.text)) depth++
    else if (token.kind === 'punct' && CLOSERS.has(token.text)) {
      depth--
      if (depth < 0) break
    }
    end = i + 1
  }
  return end
}

/** The `Sources: <expression>` spans of one token stream. */
function sourcesSpans(tokens) {
  const spans = []
  for (let i = 0; i + 1 < tokens.length; i++) {
    if (tokens[i].kind === 'ident' && tokens[i].text === 'Sources' && tokens[i + 1].text === ':') {
      spans.push({ start: i + 2, end: expressionEnd(tokens, i + 2) })
    }
  }
  return spans
}

/** Split a token range on `separator` at nesting depth 0. */
function splitTopLevel(tokens, start, end, separator) {
  const parts = []
  let depth = 0
  let from = start
  for (let i = start; i < end; i++) {
    const token = tokens[i]
    if (token.kind !== 'punct') continue
    if (OPENERS.has(token.text)) depth++
    else if (CLOSERS.has(token.text)) depth--
    else if (token.text === separator && depth === 0) {
      parts.push({ start: from, end: i })
      from = i + 1
    }
  }
  if (from < end) parts.push({ start: from, end })
  return parts
}

/** Drop redundant parentheses around a token range. */
function unwrapParens(tokens, start, end) {
  let from = start
  let to = end
  while (from < to && tokens[from].text === '(' && matchingIndex(tokens, from, to) === to - 1) {
    from++
    to--
  }
  return { start: from, end: to }
}

/** Index of the bracket that closes the one at `open`, or -1. */
function matchingIndex(tokens, open, limit) {
  let depth = 0
  for (let i = open; i < limit; i++) {
    const token = tokens[i]
    if (token.kind !== 'punct') continue
    if (OPENERS.has(token.text)) depth++
    else if (CLOSERS.has(token.text)) {
      depth--
      if (depth === 0) return i
    }
  }
  return -1
}

const CONTROL_KEYWORDS = new Set(['for', 'if', 'switch', 'select', 'case', 'range'])

/** True when the declaration at `index` is a for/if/switch header, not a value. */
function isControlFlowDecl(tokens, index) {
  let start = index
  while (start > 0 && tokens[start].newlinesBefore === 0) start--
  for (let i = start; i < index; i++) {
    if (tokens[i].kind === 'ident' && CONTROL_KEYWORDS.has(tokens[i].text)) return true
  }
  return false
}

/**
 * The package's string consts and vars: name -> initializer span, or
 * `{ ambiguous: true }` when two declarations share the name. Only `=` and `:=`
 * declarations are recorded; typed `var x []string = ...` is not used in the
 * scenarios today. Control-flow headers (`for _, spec := range ...`) are
 * skipped so their body is never mistaken for an initializer.
 */
function packageSymbols(entries) {
  const symbols = new Map()
  for (const entry of entries) {
    const { tokens } = entry
    for (let i = 0; i < tokens.length - 1; i++) {
      const token = tokens[i]
      if (token.kind !== 'ident' || token.text === '_') continue
      const next = tokens[i + 1]
      if (next.text !== '=' && next.text !== ':=') continue
      if (isControlFlowDecl(tokens, i)) continue
      const symbol = { file: entry.file, start: i + 2, end: initializerEnd(tokens, i + 1) }
      if (symbol.end <= symbol.start) continue
      const existing = symbols.get(token.text)
      if (existing && (existing.start !== symbol.start || existing.file !== symbol.file)) {
        symbols.set(token.text, { ambiguous: true })
      } else {
        symbols.set(token.text, symbol)
      }
    }
  }
  return symbols
}

/** Resolution context of one package: symbol table, token streams, used spans. */
function packageContext(entries) {
  const byFile = new Map(entries.map((entry) => [entry.file, entry]))
  const symbols = packageSymbols(entries)
  const consumedSpans = new Map(entries.map((entry) => [entry.file, []]))
  return {
    byFile,
    symbols,
    /** Remember a token range that a Sources expression already accounted for. */
    consume(file, start, end) {
      consumedSpans.get(file).push([start, end])
    },
    isConsumed(file, index) {
      return consumedSpans.get(file).some(([start, end]) => index >= start && index < end)
    }
  }
}

/** Evaluate a token range as one Go string constant (concatenation allowed). */
function evalStringExpr(ctx, entry, start, end, depth, seen) {
  const tokens = entry.tokens
  ;({ start, end } = unwrapParens(tokens, start, end))
  ctx.consume(entry.file, start, end)
  if (start >= end) return { dynamic: true, name: '', line: tokens[Math.min(start, tokens.length - 1)]?.line ?? 1 }
  const parts = splitTopLevel(tokens, start, end, '+')
  if (parts.length > 1) {
    let value = ''
    for (const part of parts) {
      const result = evalStringExpr(ctx, entry, part.start, part.end, depth, seen)
      if (result.dynamic) return result
      value += result.value
    }
    return { value, line: tokens[start].line }
  }
  const token = tokens[start]
  if (start + 1 === end && token.kind === 'string') return { value: token.value, line: token.line }
  if (start + 1 === end && token.kind === 'ident' && !IGNORED_IDENTS.has(token.text)) {
    const symbol = ctx.symbols.get(token.text)
    if (!symbol || symbol.ambiguous || seen.has(token.text) || depth > 6) {
      return { dynamic: true, name: token.text, line: token.line }
    }
    const next = new Set(seen)
    next.add(token.text)
    return evalStringExpr(ctx, ctx.byFile.get(symbol.file), symbol.start, symbol.end, depth + 1, next)
  }
  return { dynamic: true, name: token.text ?? '', line: token.line }
}

/**
 * Evaluate a `Sources:` value into its citation strings: `[]string{...}`,
 * `append([]string{...}, x...)`, or a `[]string` variable/const.
 */
function evalSourcesExpr(ctx, entry, start, end, depth, seen) {
  const tokens = entry.tokens
  ;({ start, end } = unwrapParens(tokens, start, end))
  ctx.consume(entry.file, start, end)
  if (start >= end) return []
  const first = tokens[start]
  if (first.kind === 'ident' && first.text === 'append' && tokens[start + 1]?.text === '(') {
    const close = matchingIndex(tokens, start + 1, end)
    if (close === -1) return [{ dynamic: true, name: 'append', line: first.line }]
    const items = []
    for (const arg of splitTopLevel(tokens, start + 2, close, ',')) {
      const spread = tokens[arg.end - 1]?.text === '...'
      items.push(...evalSourcesExpr(ctx, entry, arg.start, spread ? arg.end - 1 : arg.end, depth + 1, seen))
    }
    return items
  }
  if (first.kind === 'punct' && first.text === '[') {
    let open = -1
    for (let i = start; i < end; i++) {
      if (tokens[i].text === '{') {
        open = i
        break
      }
    }
    const close = open === -1 ? -1 : matchingIndex(tokens, open, end)
    if (close === -1) return [{ dynamic: true, name: 'literal', line: first.line }]
    return splitTopLevel(tokens, open + 1, close, ',').map((part) => evalStringExpr(ctx, entry, part.start, part.end, depth, seen))
  }
  if (start + 1 === end && first.kind === 'ident') {
    const symbol = ctx.symbols.get(first.text)
    if (!symbol || symbol.ambiguous || seen.has(first.text) || depth > 6) {
      return [{ dynamic: true, name: first.text, line: first.line }]
    }
    const next = new Set(seen)
    next.add(first.text)
    return evalSourcesExpr(ctx, ctx.byFile.get(symbol.file), symbol.start, symbol.end, depth + 1, next)
  }
  return [evalStringExpr(ctx, entry, start, end, depth, seen)]
}

/**
 * Classify one citation string. Returns a citation with a resolved
 * root-relative `path` and a `ranges` list of {start, end} lines (empty means
 * "whole file"), or `{ kind: 'unknown', reason }`.
 *
 * Liberal on shape, strict on target: a trailing parenthetical note
 * (`server/Database.js (createTextSearchQuery)`) and comma-separated multiple
 * ranges (`server/scanner/BookScanner.js:225-232,484-494`) are accepted, but
 * every file and line range is still checked.
 */
export function classifyCitation(raw) {
  let text = String(raw).trim()
  if (text === '') return { kind: 'unknown', reason: 'empty citation' }
  if (/\s/.test(text)) {
    const noted = /^(.*?)\s+\(([^()]*)\)$/.exec(text)
    if (!noted) return { kind: 'unknown', reason: 'citation contains whitespace' }
    text = noted[1]
  }
  const match = /^(.*?):(\d+(?:-\d+)?(?:,\d+(?:-\d+)?)*)$/.exec(text)
  const citedPath = match ? match[1] : text
  const ranges = []
  if (match) {
    for (const segment of match[2].split(',')) {
      const range = /^(\d+)(?:-(\d+))?$/.exec(segment)
      if (!range) return { kind: 'unknown', reason: `unparsable line range: ${segment}` }
      ranges.push({ start: Number(range[1]), end: range[2] === undefined ? null : Number(range[2]) })
    }
  }
  if (!SOURCE_EXTENSION.test(citedPath)) {
    if (/:\d+(?:-\d+)?(?:,\d+(?:-\d+)?)*$/.test(citedPath)) {
      return {
        kind: 'unknown',
        reason: `citation has two line parts: the path ${citedPath} already ends with a line range (a const that carries a range is concatenated with another)`
      }
    }
    return { kind: 'unknown', reason: `citation path has no source extension: ${citedPath}` }
  }
  if (citedPath.startsWith(CONTRACT_PREFIX)) {
    return { kind: 'contract', path: citedPath, ranges: [] }
  }
  let repoPath = null
  if (citedPath === 'index.js' || citedPath.startsWith('server/') || citedPath.startsWith('client/')) {
    repoPath = citedPath
  } else if (citedPath.startsWith('../client/')) {
    repoPath = citedPath.slice('../'.length)
  }
  if (!repoPath) {
    return {
      kind: 'unknown',
      reason: `citation path is not rooted at server/, client/, ../client/, index.js or the contract tree: ${citedPath}`
    }
  }
  if (repoPath.split('/').includes('..') || repoPath.startsWith('/')) {
    return { kind: 'unknown', reason: `citation path escapes its tree: ${citedPath}` }
  }
  return { kind: 'line-checked', path: repoPath, ranges }
}

function fileLineCount(absolute, cache) {
  const cached = cache.get(absolute)
  if (cached !== undefined) return cached
  const text = fs.readFileSync(absolute, 'utf8')
  if (text === '') return cache.set(absolute, 0).get(absolute)
  const count = text.split('\n').length - (text.endsWith('\n') ? 1 : 0)
  cache.set(absolute, count)
  return count
}

/**
 * The first path segment whose case differs from the entry on disk, or null.
 * macOS checkouts are case-insensitive, so a citation with the wrong case would
 * pass statSync here and fail on a case-sensitive CI checkout.
 */
function caseMismatch(root, relative) {
  let current = root
  for (const segment of relative.split('/')) {
    let names
    try {
      names = fs.readdirSync(current)
    } catch {
      return null
    }
    if (!names.includes(segment)) {
      const actual = names.find((name) => name.toLowerCase() === segment.toLowerCase())
      return actual === undefined ? null : { segment, actual }
    }
    current = path.join(current, segment)
  }
  return null
}

/** Check one classified citation against the tree at `root`. */
export function validateCitation(root, citation, lineCache = new Map()) {
  if (citation.kind === 'unknown') return { ok: false, kind: 'unknown-form', reason: citation.reason }
  const absolute = path.resolve(root, citation.path)
  const relative = path.relative(root, absolute).split(path.sep).join('/')
  if (relative === '' || relative === '..' || relative.startsWith('../') || path.isAbsolute(relative)) {
    return { ok: false, kind: 'path-escapes-root', reason: `resolves outside the repository root: ${relative}` }
  }
  let stat
  try {
    stat = fs.statSync(absolute)
  } catch {
    return { ok: false, kind: 'missing-file', reason: `file not found: ${citation.path}` }
  }
  if (!stat.isFile()) return { ok: false, kind: 'missing-file', reason: `not a regular file: ${citation.path}` }
  const mismatched = caseMismatch(root, relative)
  if (mismatched) {
    return {
      ok: false,
      kind: 'missing-file',
      reason: `file not found: ${citation.path} (the path segment ${mismatched.segment} is ${mismatched.actual} on disk)`
    }
  }
  const lineCount = fileLineCount(absolute, lineCache)
  if (citation.kind === 'contract' || citation.ranges.length === 0) return { ok: true, kind: 'ok', lineCount }
  for (const { start, end } of citation.ranges) {
    if (start < 1) {
      return { ok: false, kind: 'line-out-of-range', reason: `line ${start} is not a line number of ${citation.path}` }
    }
    if (start > lineCount) {
      return {
        ok: false,
        kind: 'line-out-of-range',
        reason: `line ${start} beyond end of file (${citation.path} has ${lineCount} lines)`
      }
    }
    if (end === null) continue
    if (end < start) {
      return { ok: false, kind: 'reversed-range', reason: `range ${start}-${end} ends before it starts` }
    }
    if (end > lineCount) {
      return {
        ok: false,
        kind: 'line-out-of-range',
        reason: `range ${start}-${end} overruns end of file (${citation.path} has ${lineCount} lines)`
      }
    }
  }
  return { ok: true, kind: 'ok', lineCount }
}

// -------------------------------------------------------------------- lint

function goFilesUnder(dir) {
  const found = []
  const walk = (current) => {
    for (const entry of fs.readdirSync(current, { withFileTypes: true })) {
      const full = path.join(current, entry.name)
      if (entry.isDirectory()) walk(full)
      else if (entry.isFile() && entry.name.endsWith('.go')) found.push(full)
    }
  }
  walk(dir)
  return found.sort()
}

const rootRelative = (root, file) => path.relative(root, file).split(path.sep).join('/')

/**
 * Lint every non-test .go file below the scenarios directory.
 *
 * @param {{root?: string, scenariosDir?: string}} options
 */
export function lintScenarioSources(options = {}) {
  const root = path.resolve(options.root ?? SCRIPT_ROOT)
  const scenariosDir = path.resolve(options.scenariosDir ?? path.join(root, DEFAULT_SCENARIOS))
  if (!fs.existsSync(scenariosDir)) throw new Error(`scenarios directory not found: ${scenariosDir}`)

  const harnessDir = path.join(scenariosDir, HARNESS_DIR)
  const files = goFilesUnder(scenariosDir).filter(
    (file) => !file.endsWith('_test.go') && !file.startsWith(harnessDir + path.sep)
  )

  const entries = files.map((file) => ({
    file,
    packet: rootRelative(scenariosDir, path.dirname(file)) || '.',
    tokens: tokenizeGo(fs.readFileSync(file, 'utf8'))
  }))
  if (entries.length === 0) throw new Error(`no non-test scenario .go files under ${scenariosDir}`)
  const byPackage = new Map()
  for (const entry of entries) {
    const dir = path.dirname(entry.file)
    if (!byPackage.has(dir)) byPackage.set(dir, [])
    byPackage.get(dir).push(entry)
  }

  const findings = []
  const citations = []
  const dynamic = []
  const lineCache = new Map()

  for (const packageEntries of byPackage.values()) {
    const ctx = packageContext(packageEntries)
    // Primary: evaluate every Sources expression, following string variables
    // and concatenations back to their literals.
    for (const entry of packageEntries) {
      const file = rootRelative(root, entry.file)
      for (const span of sourcesSpans(entry.tokens)) {
        for (const element of evalSourcesExpr(ctx, entry, span.start, span.end, 0, new Set())) {
          if (element.dynamic) {
            dynamic.push({ file, line: element.line, name: element.name || element.reason || 'expression' })
            continue
          }
          const classified = classifyCitation(element.value)
          if (classified.kind === 'unknown') {
            findings.push({ packet: entry.packet, file, line: element.line, citation: element.value, kind: 'unknown-form', reason: classified.reason })
            continue
          }
          citations.push({ packet: entry.packet, file, line: element.line, citation: element.value, classified })
        }
      }
    }
    // Secondary: a citation passed as a plain argument to a helper (`srcs(...)`,
    // `scenario(..., sources, ...)`) is not inside a Sources expression, but it
    // is still a citation. Tokens the evaluator consumed are already counted.
    for (const entry of packageEntries) {
      const file = rootRelative(root, entry.file)
      for (let i = 0; i < entry.tokens.length; i++) {
        const token = entry.tokens[i]
        if (token.kind !== 'string' || ctx.isConsumed(entry.file, i)) continue
        const classified = classifyCitation(token.value)
        if (classified.kind === 'unknown') continue
        citations.push({ packet: entry.packet, file, line: token.line, citation: token.value, classified })
      }
    }
  }

  const checked = []
  for (const item of citations) {
    const result = validateCitation(root, item.classified, lineCache)
    if (result.ok) {
      checked.push(item)
      continue
    }
    findings.push({ packet: item.packet, file: item.file, line: item.line, citation: item.citation, kind: result.kind, reason: result.reason })
  }

  // The same literal can be reached through several Sources lists (one const
  // used by many scenarios); report it once, at its own line.
  const uniqueFindings = [...new Map(findings.map((finding) => [`${finding.file}:${finding.line}:${finding.kind}:${finding.citation}`, finding])).values()]
  uniqueFindings.sort((a, b) => (a.file === b.file ? a.line - b.line || a.citation.localeCompare(b.citation) : a.file.localeCompare(b.file)))

  const findingsByKind = {}
  for (const finding of uniqueFindings) findingsByKind[finding.kind] = (findingsByKind[finding.kind] ?? 0) + 1
  const citationsByPacket = {}
  for (const item of citations) citationsByPacket[item.packet] = (citationsByPacket[item.packet] ?? 0) + 1
  for (const item of entries) citationsByPacket[item.packet] ??= 0

  const distinct = new Set(citations.map((item) => item.citation))
  const uniqueDynamic = [...new Map(dynamic.map((item) => [`${item.file}:${item.line}:${item.name}`, item])).values()].sort(
    (a, b) => (a.file === b.file ? a.line - b.line : a.file.localeCompare(b.file))
  )
  return {
    tool: 'scenario-sources-lint',
    root,
    scenariosDir: rootRelative(root, scenariosDir),
    filesScanned: files.length,
    packetCount: new Set(entries.map((entry) => entry.packet)).size,
    packets: citationsByPacket,
    citationsChecked: citations.length,
    citationsDistinct: distinct.size,
    citationsOk: checked.length,
    findingCount: uniqueFindings.length,
    findingsByKind,
    findings: uniqueFindings,
    dynamicSourcesCount: uniqueDynamic.length,
    dynamicSources: uniqueDynamic,
    ok: uniqueFindings.length === 0
  }
}

/** One-line verdict; always the last line of CLI output. */
export function summaryLine(report) {
  const verdict = report.ok ? 'OK' : 'FAIL'
  return (
    `scenario-sources-lint: ${report.filesScanned} files in ${report.packetCount} packets, ` +
    `${report.citationsChecked} citations checked (${report.citationsDistinct} distinct), ` +
    `${report.findingCount} finding${report.findingCount === 1 ? '' : 's'} — ${verdict}`
  )
}

/** JSON report body (counts + findings), stable key order. */
export function reportJson(report) {
  return {
    tool: report.tool,
    scenarios_dir: report.scenariosDir,
    files_scanned: report.filesScanned,
    packets: report.packetCount,
    citations_by_packet: report.packets,
    citations_checked: report.citationsChecked,
    citations_distinct: report.citationsDistinct,
    citations_ok: report.citationsOk,
    finding_count: report.findingCount,
    findings_by_kind: report.findingsByKind,
    findings: report.findings.map((finding) => ({
      packet: finding.packet,
      file: finding.file,
      line: finding.line,
      citation: finding.citation,
      kind: finding.kind,
      reason: finding.reason
    })),
    dynamic_sources_count: report.dynamicSourcesCount,
    dynamic_sources: report.dynamicSources,
    notes: [
      'citations_checked counts Sources values that parsed to a file path; an unparsable value is an unknown-form finding and is not counted as a citation.',
      'scenarios/harness is the scenario toolkit (its Sources []string is the struct field type), so it is not scanned; packets counts directories that hold scenario files.'
    ],
    ok: report.ok
  }
}

const USAGE = `usage: scenario-sources-lint.mjs [--root <repo>] [--scenarios <dir>] [--report <json>]

Validates the upstream citations of every scenario Sources list against the
pinned upstream tree at the repository root. Exit 0 when every citation
resolves, 1 on findings, 2 on a usage error.`

/** Run the CLI and return its exit code (testable without process.exit). */
export function runCli(argv, io = {}) {
  const stdout = io.stdout ?? process.stdout
  const stderr = io.stderr ?? process.stderr
  let values
  try {
    ;({ values } = parseArgs({
      args: argv,
      options: {
        root: { type: 'string' },
        scenarios: { type: 'string' },
        report: { type: 'string' },
        help: { type: 'boolean', short: 'h' }
      },
      strict: true,
      allowPositionals: false
    }))
  } catch (err) {
    stderr.write(`scenario-sources-lint: ${err.message}\n${USAGE}\n`)
    return 2
  }
  if (values.help) {
    stdout.write(`${USAGE}\n`)
    return 0
  }
  let report
  try {
    report = lintScenarioSources({ root: values.root, scenariosDir: values.scenarios })
  } catch (err) {
    stderr.write(`scenario-sources-lint: ${err.message}\n`)
    return 2
  }
  for (const finding of report.findings) {
    stdout.write(`${finding.file}:${finding.line}: [${finding.packet}] ${JSON.stringify(finding.citation)} — ${finding.reason}\n`)
  }
  // A packet with scenario files but no citation at all is an extractor
  // regression, not a clean packet: say so without failing the run.
  for (const [packet, count] of Object.entries(report.packets)) {
    if (count === 0) stderr.write(`scenario-sources-lint: note: packet ${packet} declares no resolvable Sources citations\n`)
  }
  stdout.write(`${summaryLine(report)}\n`)
  if (values.report) {
    try {
      const target = path.resolve(values.report)
      fs.mkdirSync(path.dirname(target), { recursive: true })
      fs.writeFileSync(target, `${JSON.stringify(reportJson(report), null, 2)}\n`)
    } catch (err) {
      stderr.write(`scenario-sources-lint: cannot write report: ${err.message}\n`)
      return 2
    }
  }
  return report.ok ? 0 : 1
}

if (process.argv[1] && fs.realpathSync(process.argv[1]) === fileURLToPath(import.meta.url)) {
  process.exit(runCli(process.argv.slice(2)))
}
