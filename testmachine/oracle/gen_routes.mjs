#!/usr/bin/env node
import fs from 'node:fs'
import path from 'node:path'
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
const routesOutputPath = path.join(__dirname, 'routes.json')
const socketsOutputPath = path.join(__dirname, 'sockets.json')

const ROUTE_FILES = [
  'server/routers/ApiRouter.js',
  'server/routers/PublicRouter.js',
  'server/routers/HlsRouter.js',
  'server/Server.js',
  'server/Auth.js'
]

const MOUNT_PREFIX = {
  'server/routers/ApiRouter.js': '/api',
  'server/routers/PublicRouter.js': '/public',
  'server/routers/HlsRouter.js': '/hls',
  'server/Server.js': '',
  'server/Auth.js': ''
}

const READONLY_POST_PATHS = new Set([
  '/api/authorize',
  '/api/items/batch/get',
  '/api/filesystem/pathexists',
  '/api/validate-cron',
  '/api/podcasts/feed',
  '/api/podcasts/opml/parse'
])

const SKIP_ROUTES = new Map([
  ['ALL /*', 'catch-all SPA/Next.js fallback, not an API route'],
  ['POST /init', 'server initialization route returns 500 or initializes fresh DB'],
  ['GET /auth/openid', 'initiates external OIDC redirect'],
  ['GET /auth/openid/mobile-redirect', 'initiates external OIDC mobile redirect'],
  ['GET /auth/openid/callback', 'requires live OIDC callback state'],
  ['GET /api/backups/:id/apply', 'applies backup archive and restarts server']
])

const SOCKET_WRAPPERS = {
  emitter: 0,
  clientEmitter: 1,
  adminEmitter: 0,
  libraryItemEmitter: 0,
  libraryItemsEmitter: 0
}

const routePattern = /(?<receiver>(?:this\.)?router)\s*\.\s*(?<method>get|post|put|patch|delete|all)\s*\(/gi
const socketPattern = /(?<receiver>[\w$.]+)\s*\.\s*(?<method>on|emit)\s*\(/g
const wrapperPattern = /(?<receiver>SocketAuthority|this)\s*\.\s*(?<method>emitter|clientEmitter|adminEmitter|libraryItemEmitter|libraryItemsEmitter)\s*\(/g

function lineNumber(source, offset) {
  return source.slice(0, offset).split('\n').length
}

function callArguments(source, opening) {
  const args = []
  let start = opening + 1
  let depth = 0
  let quote = null
  let escaped = false
  for (let i = start; i < source.length; i++) {
    const ch = source[i]
    if (quote) {
      if (escaped) escaped = false
      else if (ch === '\\') escaped = true
      else if (ch === quote) quote = null
      continue
    }
    if ('\'"`'.includes(ch)) quote = ch
    else if ('([{'.includes(ch)) depth++
    else if (')]}'.includes(ch)) {
      if (ch === ')' && depth === 0) {
        const last = source.slice(start, i).trim()
        if (last) args.push(last)
        return args
      }
      depth = Math.max(0, depth - 1)
    } else if (ch === ',' && depth === 0) {
      args.push(source.slice(start, i).trim())
      start = i + 1
    }
  }
  return args
}

function classifyLiteral(expression = '') {
  expression = expression.trim()
  if (!expression) return ['', 'missing']
  const quote = expression[0]
  if ('\'"`'.includes(quote) && expression.length >= 2 && expression.at(-1) === quote) {
    const value = expression.slice(1, -1)
    if (quote === '`' && value.includes('${')) return [expression, 'template_expression']
    return [value, quote === '`' ? 'template_literal' : 'string_literal']
  }
  if (expression.startsWith('/') && expression.lastIndexOf('/') > 0) return [expression, 'regex_literal']
  return [expression, 'dynamic_expression']
}

function loadControllerSources() {
  const ctrlDir = path.join(upstreamDir, 'server/controllers')
  const texts = {}
  for (const file of fs.readdirSync(ctrlDir)) {
    if (file.endsWith('.js')) {
      texts[file.slice(0, -3)] = fs.readFileSync(path.join(ctrlDir, file), 'utf8')
    }
  }
  return texts
}

function getMethodBody(ctrlTexts, ctrlName, methodName) {
  const src = ctrlTexts[ctrlName] || ''
  const re = new RegExp(`(?:async\\s+)?${methodName}\\s*\\([^)]*\\)\\s*\\{`)
  const match = re.exec(src)
  if (!match) return ''
  let depth = 1
  let i = match.index + match[0].length
  while (i < src.length && depth > 0) {
    if (src[i] === '{') depth++
    else if (src[i] === '}') depth--
    i++
  }
  return src.slice(match.index, i)
}

function isPublicRoute(method, fullPath) {
  if (
    fullPath === '/login' ||
    fullPath === '/logout' ||
    fullPath === '/ping' ||
    fullPath === '/healthcheck' ||
    fullPath === '/status' ||
    fullPath === '/init'
  ) {
    return true
  }
  if (
    fullPath.startsWith('/auth/') ||
    fullPath.startsWith('/public/') ||
    fullPath.startsWith('/hls/') ||
    fullPath.startsWith('/feed/')
  ) {
    return true
  }
  if (method === 'GET' && (fullPath === '/api/items/:id/cover' || fullPath === '/api/authors/:id/image')) {
    return true
  }
  return false
}

function isAdminApiRoute(method, apiLine, ctrlTexts) {
  const calls = [...apiLine.matchAll(/([A-Za-z]+Controller)\.([A-Za-z0-9_]+)/g)]
  for (const [, ctrl, fn] of calls) {
    if (fn === 'adminMiddleware') return true
    if (ctrl === 'UserController' && fn === 'middleware') {
      if (method === 'POST' || method === 'PATCH' || method === 'DELETE') return true
      continue
    }
    const body = getMethodBody(ctrlTexts, ctrl, fn)
    if (fn === 'middleware') {
      if (/if\s*\(\s*!req\.user\.isAdminOrUp\s*\)/.test(body) && !/req\.method/.test(body) && !/req\.params/.test(body)) {
        return true
      }
      continue
    }
    if (/if\s*\(\s*!req\.user\.isAdminOrUp\s*\)/.test(body.slice(0, 700))) {
      return true
    }
  }
  return false
}

function normalizeFullPath(source, routePath) {
  const prefix = MOUNT_PREFIX[source] ?? ''
  const combined = `${prefix}${routePath.startsWith('/') ? '' : '/'}${routePath}`
  if (combined.length > 1 && combined.endsWith('/')) {
    return combined.slice(0, -1)
  }
  return combined
}

function extractRoutesDirect(ctrlTexts) {
  const seen = new Map()
  for (const rel of ROUTE_FILES) {
    const source = fs.readFileSync(path.join(upstreamDir, rel), 'utf8')
    const lines = source.split('\n')
    for (const match of source.matchAll(routePattern)) {
      const args = callArguments(source, match.index + match[0].length - 1)
      const [route, routeKind] = classifyLiteral(args[0])
      if (routeKind !== 'string_literal') continue

      const method = match.groups.method.toUpperCase()
      const fullPath = normalizeFullPath(rel, route)
      const key = `${method} ${fullPath}`
      if (seen.has(key)) continue

      let auth = 'user'
      if (isPublicRoute(method, fullPath)) {
        auth = 'public'
      } else if (rel === 'server/routers/ApiRouter.js') {
        const line = lines[lineNumber(source, match.index) - 1] || ''
        if (isAdminApiRoute(method, line, ctrlTexts)) {
          auth = 'admin'
        }
      }

      const mutates =
        (method === 'POST' || method === 'PATCH' || method === 'PUT' || method === 'DELETE') &&
        !READONLY_POST_PATHS.has(fullPath)

      const spec = { method, path: fullPath, auth }
      if (mutates) spec.mutates = true
      if (SKIP_ROUTES.has(key)) {
        spec.skip = true
        spec.skip_reason = SKIP_ROUTES.get(key)
      }
      seen.set(key, spec)
    }
  }

  return [...seen.values()].sort((a, b) => {
    if (a.path !== b.path) return a.path.localeCompare(b.path)
    return a.method.localeCompare(b.method)
  })
}

function walkServerFiles(dir) {
  const results = []
  for (const entry of fs.readdirSync(dir, { withFileTypes: true })) {
    const full = path.join(dir, entry.name)
    if (entry.isDirectory()) {
      if (entry.name === 'libs' || entry.name === 'migrations') continue
      results.push(...walkServerFiles(full))
    } else if (entry.name.endsWith('.js')) {
      results.push(full)
    }
  }
  return results.sort()
}

function extractSocketsDirect() {
  const inbound = new Set()
  const outbound = new Set()

  for (const file of walkServerFiles(path.join(upstreamDir, 'server'))) {
    const source = fs.readFileSync(file, 'utf8')
    for (const match of source.matchAll(socketPattern)) {
      const receiver = match.groups.receiver
      if (!/socket|io/i.test(receiver)) continue
      const args = callArguments(source, match.index + match[0].length - 1)
      const [event, kind] = classifyLiteral(args[0])
      if (kind !== 'string_literal') continue
      if (match.groups.method === 'on') {
        if (/socket/i.test(receiver)) inbound.add(event)
      } else {
        outbound.add(event)
      }
    }
    for (const match of source.matchAll(wrapperPattern)) {
      const idx = SOCKET_WRAPPERS[match.groups.method]
      const args = callArguments(source, match.index + match[0].length - 1)
      const [event, kind] = classifyLiteral(args[idx])
      if (kind === 'string_literal') {
        outbound.add(event)
      }
    }
  }

  return {
    inbound: [...inbound].sort(),
    outbound: [...outbound].sort()
  }
}

function main() {
  if (!fs.existsSync(path.join(upstreamDir, 'server'))) {
    console.error(`upstream server/ missing at ${upstreamDir}; run 'node testmachine/oracle/fetch-upstream.mjs' or 'make upstream'`)
    process.exit(1)
  }
  const ctrlTexts = loadControllerSources()
  const routes = extractRoutesDirect(ctrlTexts)
  const routeManifest = {
    generated_from: 'server/',
    routes
  }
  fs.writeFileSync(routesOutputPath, `${JSON.stringify(routeManifest, null, 2)}\n`, 'utf8')

  const sockets = extractSocketsDirect()
  const socketManifest = {
    generated_from: 'server/',
    inbound: sockets.inbound,
    outbound: sockets.outbound
  }
  fs.writeFileSync(socketsOutputPath, `${JSON.stringify(socketManifest, null, 2)}\n`, 'utf8')

  console.log(
    `Extracted ${routes.length} unique routes to ${path.relative(repoRoot, routesOutputPath)} ` +
    `and ${sockets.inbound.length} inbound / ${sockets.outbound.length} outbound socket events to ${path.relative(repoRoot, socketsOutputPath)}`
  )
}

main()
