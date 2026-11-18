#!/usr/bin/env node
// Conservative syntax inventory, not behavioral or runtime coverage proof.
import fs from 'node:fs'
import path from 'node:path'
import { createHash } from 'node:crypto'
import { spawnSync } from 'node:child_process'
import { fileURLToPath } from 'node:url'

export const ROOT = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '../..')
const wrappers = {
  emitter: [0, 'all_authorized_clients'],
  clientEmitter: [1, 'specific_user_clients'],
  adminEmitter: [0, 'admin_clients'],
  libraryItemEmitter: [0, 'clients_with_library_item_access'],
  libraryItemsEmitter: [0, 'clients_with_access_to_any_library_item']
}
const routePattern = /(?<receiver>(?:this\.)?router)\s*\.\s*(?<method>get|post|put|patch|delete|options|head|all)\s*\(/gi
const socketPattern = /(?<receiver>[\w$.]+)\s*\.\s*(?<method>on|emit)\s*\(/g
const wrapperPattern = /(?<receiver>SocketAuthority|this)\s*\.\s*(?<method>emitter|clientEmitter|adminEmitter|libraryItemEmitter|libraryItemsEmitter)\s*\(/g
export const sha256 = data => createHash('sha256').update(data).digest('hex')
const lineNumber = (source, offset) => source.slice(0, offset).split('\n').length
const relative = (root, file) => path.relative(root, file).split(path.sep).join('/')

function filesUnder(dir, recursive = true) {
  if (!fs.existsSync(dir)) return []
  return fs.readdirSync(dir, { withFileTypes: true }).flatMap(entry => {
    const file = path.join(dir, entry.name)
    return entry.isDirectory() ? (recursive ? filesUnder(file) : []) : (entry.name.endsWith('.js') ? [file] : [])
  }).sort()
}

// Match the inherited conservative scanner: quotes and nested delimiters are
// recognized; comments, regular-expression syntax and template expressions are
// retained as syntax rather than interpreted as JavaScript.
function callArguments(source, opening) {
  const args = []
  let start = opening + 1, depth = 0, quote = null, escaped = false
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

function classify(expression = '') {
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

function prefixContext(source) {
  for (const [file, mount] of [['ApiRouter.js', '/api'], ['PublicRouter.js', '/public'], ['HlsRouter.js', '/hls']]) {
    if (source.endsWith(`server/routers/${file}`)) return { known_mount: mount, additional_prefix: 'global.RouterBasePath (runtime configured)', fully_resolved: false }
  }
  if (source.endsWith('server/Server.js')) return { known_mount: 'router mounted at global.RouterBasePath', fully_resolved: false }
  return { known_mount: null, reason: 'mount point not resolved by this source extractor', fully_resolved: false }
}

function sourceRecord(root, file, source) {
  return { path: relative(root, file), sha256: sha256(source) }
}

export function extractRoutes(root, sourceRoots = ['server']) {
  const routes = [], sources = {}
  for (const sourceRoot of sourceRoots) for (const file of filesUnder(path.join(root, sourceRoot))) {
    const rel = relative(root, file)
    if (rel.includes('/libs/')) continue
    const source = fs.readFileSync(file, 'utf8')
    const matches = [...source.matchAll(routePattern)]
    if (!matches.length) continue
    sources[rel] = sourceRecord(root, file, source)
    for (const match of matches) {
      const args = callArguments(source, match.index + match[0].length - 1)
      const [route, routeKind] = classify(args[0])
      routes.push({ method: match.groups.method.toUpperCase(), route, route_kind: routeKind, receiver: match.groups.receiver,
        source: rel, line: lineNumber(source, match.index), argument_count_syntax_hint: args.length,
        middleware_behavior_resolved: false, prefix_context: prefixContext(rel) })
    }
  }
  return [routes, sources]
}

export function extractSocketEvents(root, sourceRoots = ['server']) {
  const events = [], sources = {}
  for (const sourceRoot of sourceRoots) for (const file of filesUnder(path.join(root, sourceRoot))) {
    const rel = relative(root, file)
    if (rel.includes('/libs/')) continue
    const source = fs.readFileSync(file, 'utf8')
    const direct = [...source.matchAll(socketPattern)].filter(match => /socket|io/i.test(match.groups.receiver))
    const wrapped = [...source.matchAll(wrapperPattern)]
    if (!direct.length && !wrapped.length) continue
    sources[rel] = sourceRecord(root, file, source)
    for (const match of direct) {
      const [event, eventKind] = classify(callArguments(source, match.index + match[0].length - 1)[0])
      events.push({ direction: match.groups.method === 'on' ? 'incoming_registration' : 'emission', operation: match.groups.method,
        receiver: match.groups.receiver, event, event_kind: eventKind, source: rel, line: lineNumber(source, match.index), scope_resolved: false })
    }
    for (const match of wrapped) {
      const wrapper = match.groups.method
      const [index, scope] = wrappers[wrapper]
      const [event, eventKind] = classify(callArguments(source, match.index + match[0].length - 1)[index])
      events.push({ direction: 'emission_via_wrapper', operation: 'emit', receiver: match.groups.receiver, wrapper,
        wrapper_event_argument_index: index, event, event_kind: eventKind, source: rel,
        line: lineNumber(source, match.index), scope_classification: scope, scope_resolved: false })
    }
  }
  return [events, sources]
}

export function extractModels(root) {
  const models = [], sources = {}
  for (const file of filesUnder(path.join(root, 'server/models'), false)) {
    const rel = relative(root, file), source = fs.readFileSync(file, 'utf8')
    models.push({ filename: path.basename(file), source: rel, line: 1 })
    sources[rel] = sourceRecord(root, file, source)
  }
  return [models, sources]
}

export function buildInventory(root = ROOT) {
  const [routes, routeSources] = extractRoutes(root)
  const [socketEvents, socketSources] = extractSocketEvents(root)
  const [models, modelSources] = extractModels(root)
  const sources = Object.fromEntries(Object.entries({ ...routeSources, ...socketSources, ...modelSources }).sort(([a], [b]) => a < b ? -1 : a > b ? 1 : 0))
  const revision = spawnSync('git', ['rev-parse', 'HEAD'], { cwd: root, encoding: 'utf8' })
  return {
    schema_version: 1, inventory_kind: 'conservative_source_syntax_inventory', source_revision: revision.status === 0 ? revision.stdout.trim() : null, source_tree: 'server/',
    limitations: [
      'This is source extraction, not a complete API or behavior specification.',
      'Express mount prefixes are partially annotated and remain runtime/configuration dependent.',
      'Middleware, authorization, conditional registration, request/response schemas, and handler behavior are not interpreted.',
      'Only syntax forms represented by this extractor are inventoried; dynamic expressions are retained as unresolved text.',
      'Socket.IO wrapper emissions are inventoried from known wrapper signatures, but recipient scope and runtime event flow are not resolved.',
      'The syntax scanner reads JavaScript .js files only and may count registrations in comments or strings; TypeScript sources are not scanned.',
      'Model inventory lists filenames only; it does not describe database schema, migrations, or serialized metadata formats.'
    ],
    counts: { routes: routes.length, socket_events: socketEvents.length, model_files: models.length, source_files: Object.keys(sources).length },
    routes, socket_events: socketEvents, model_files: models, sources
  }
}

if (process.argv[1] && path.resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
  const args = process.argv.slice(2)
  let root = ROOT, output
  for (let i = 0; i < args.length; i++) {
    if (args[i] === '--root' && args[i + 1]) root = path.resolve(args[++i])
    else if (args[i] === '--output' && args[i + 1]) output = path.resolve(args[++i])
    else throw new Error(`unknown or incomplete argument: ${args[i]}`)
  }
  const text = JSON.stringify(buildInventory(root), null, 2) + '\n'
  if (output) { fs.mkdirSync(path.dirname(output), { recursive: true }); fs.writeFileSync(output, text) }
  else process.stdout.write(text)
}
