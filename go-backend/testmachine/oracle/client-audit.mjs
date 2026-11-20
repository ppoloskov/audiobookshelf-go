#!/usr/bin/env node
// Audits real web-client (Nuxt SPA) HTTP + Socket.IO usage and mobile client
// contracts against go-backend/testmachine/oracle/routes.json, flows/*.json, and complex*.go.
import fs from 'node:fs'
import path from 'node:path'
import { fileURLToPath } from 'node:url'

const __dirname = path.dirname(fileURLToPath(import.meta.url))
const repoRoot = path.resolve(__dirname, '../../..')
const CLIENT_DIRS = ['components', 'layouts', 'middleware', 'mixins', 'pages', 'players', 'plugins', 'store']
const SOCKET_BUILTINS = new Set(['connect', 'connect_error', 'disconnect'])

function parseArgs(argv) {
  let jsonOut = null
  for (let i = 0; i < argv.length; i++) {
    if (argv[i] === '--json' && argv[i + 1]) jsonOut = path.resolve(argv[++i])
    else if (argv[i].startsWith('--json=')) jsonOut = path.resolve(argv[i].slice(7))
  }
  return { jsonOut }
}

function walkFiles(dir, out = []) {
  if (!fs.existsSync(dir)) return out
  for (const entry of fs.readdirSync(dir, { withFileTypes: true })) {
    const full = path.join(dir, entry.name)
    if (entry.isDirectory()) walkFiles(full, out)
    else if (entry.name.endsWith('.js') || entry.name.endsWith('.vue')) out.push(full)
  }
  return out
}

function normPath(raw) {
  let p = raw.replace(/\$\{[^}]+\}/g, ':param').replace(/\{\{[^}]+\}\}/g, ':param').split('?')[0].trim()
  p = p.replace(/:param(?::param)+/g, ':param')
  if (p.length > 1 && p.endsWith('/')) p = p.slice(0, -1)
  return p.startsWith('/') ? p : `/${p}`
}

function matchSegments(method, clientPath, serverRoute) {
  if (serverRoute.method !== method && serverRoute.method !== 'ALL') return false
  if (serverRoute.path === '/*') return false
  const cSegs = clientPath.split('/').filter(Boolean)
  const sSegs = serverRoute.path.split('/').filter(Boolean)
  const matchAt = (ci, si) => {
    if (ci === cSegs.length && si === sSegs.length) return true
    if (si < sSegs.length && sSegs[si] === '*') return true
    if (si < sSegs.length && sSegs[si].startsWith(':') && sSegs[si].endsWith('?') && matchAt(ci, si + 1)) return true
    if (ci >= cSegs.length || si >= sSegs.length) return false
    if (cSegs[ci] === sSegs[si] || sSegs[si].startsWith(':') || cSegs[ci] === ':param') return matchAt(ci + 1, si + 1)
    return false
  }
  return matchAt(0, 0)
}

function extractWebClient() {
  const httpMap = new Map()
  const listeners = new Map()
  const emitters = new Map()
  const add = (map, key, loc) => {
    if (!map.has(key)) map.set(key, [])
    map.get(key).push(loc)
  }
  for (const sub of CLIENT_DIRS) {
    for (const file of walkFiles(path.join(repoRoot, 'client', sub))) {
      const rel = path.relative(repoRoot, file)
      const src = fs.readFileSync(file, 'utf8')
      for (const m of src.matchAll(/\$axios\s*\??\.\s*(\$?(?:get|post|patch|delete|put))\s*\(\s*(['"`])((?:\\.|(?!\2)[\s\S])*?)\2/g)) {
        const method = m[1].replace(/^\$/, '').toUpperCase()
        add(httpMap, `${method} ${normPath(m[3])}`, rel)
      }
      for (const m of src.matchAll(/(?:\$root\s*\??\.)?socket\s*\??\.\s*(on|emit)\s*\(\s*(['"`])([^'"`]+)\2/g)) {
        add(m[1] === 'on' ? listeners : emitters, m[3], rel)
      }
    }
  }
  return { httpMap, listeners, emitters }
}

function parseGoConcatExpr(expr) {
  let out = ''
  let i = 0
  while (i < expr.length) {
    if (expr[i] === '"') {
      let j = i + 1
      while (j < expr.length && (expr[j] !== '"' || expr[j - 1] === '\\')) j++
      out += expr.slice(i + 1, j)
      i = j + 1
    } else if (/[\s+]/.test(expr[i])) {
      i++
    } else {
      let j = i
      while (j < expr.length && expr[j] !== '+' && expr[j] !== '"') j++
      out += ':param'
      i = j
    }
  }
  return normPath(out)
}

function loadOracleCorpus() {
  const flowRoutes = new Set()
  const socketObserved = new Set(['init'])
  const socketEmitted = new Set(['auth'])
  let checkSocketSteps = 0

  const flowsDir = path.join(__dirname, 'flows')
  for (const f of fs.readdirSync(flowsDir).sort()) {
    if (!f.endsWith('.json')) continue
    const flow = JSON.parse(fs.readFileSync(path.join(flowsDir, f), 'utf8'))
    for (const s of flow.steps || []) {
      if ((!s.kind || s.kind === 'http' || s.kind === 'multipart') && s.path) {
        flowRoutes.add(`${(s.method || 'GET').toUpperCase()} ${normPath(s.path)}`)
      }
      if (s.kind === 'socket_emit' && s.socket_event) socketEmitted.add(s.socket_event)
      if (s.check_sockets) checkSocketSteps++
    }
  }
  for (const f of fs.readdirSync(__dirname).sort()) {
    if (!/^complex.*\.go$/.test(f)) continue
    const src = fs.readFileSync(path.join(__dirname, f), 'utf8')
    for (const m of src.matchAll(/tw\.(?:TS|Go)\.(Get|Post|Patch|Put|Delete)\s*\([^,]+,[^,]+,\s*("[^"]*"(?:\s*\+\s*(?:"[^"]*"|[A-Za-z0-9_.()]+))*)/g)) {
      flowRoutes.add(`${m[1].toUpperCase()} ${parseGoConcatExpr(m[2])}`)
    }
    for (const m of src.matchAll(/Request\s*\{([^}]+)\}/g)) {
      const mm = m[1].match(/Method:\s*(?:http\.Method(Get|Post|Patch|Put|Delete)|"([A-Z]+)")/)
      const pm = m[1].match(/Path:\s*("[^"]*"(?:\s*\+\s*(?:"[^"]*"|[A-Za-z0-9_.()]+))*)/)
      if (pm) flowRoutes.add(`${(mm ? (mm[1] || mm[2]) : 'GET').toUpperCase()} ${parseGoConcatExpr(pm[1])}`)
    }
    for (const m of src.matchAll(/\.Wait\s*\([^,]+,[^,]+,\s*"([^"]+)"/g)) socketObserved.add(m[1])
    for (const m of src.matchAll(/\.Emit\s*\([^,]+,\s*"([^"]+)"/g)) socketEmitted.add(m[1])
    if (src.includes('/hls/')) flowRoutes.add('GET /hls/:param/:param')
  }
  const flowSpecs = [...flowRoutes].map((key) => {
    const [method, p] = key.split(' ')
    return { method, path: p }
  })
  return { flowRoutes, flowSpecs, socketObserved, socketEmitted, checkSocketSteps }
}

function summarizeMobileClients(serverRoutes, flowSpecs) {
  const clientsPath = path.join(repoRoot, 'go-backend/testmachine/contracts/clients/clients.json')
  if (!fs.existsSync(clientsPath)) return []
  const meta = JSON.parse(fs.readFileSync(clientsPath, 'utf8'))
  return (meta.clients || []).map((c) => {
    const mdPath = path.join(path.dirname(clientsPath), `${c.key}.md`)
    const uniqueRoutes = new Set()
    let rawOps = 0
    if (fs.existsSync(mdPath)) {
      for (const line of fs.readFileSync(mdPath, 'utf8').split('\n')) {
        const m = line.match(/^\|\s*(GET|POST|PATCH|PUT|DELETE|HEAD|OPTIONS)\s*\|\s*([^|]+)\|/i)
        if (!m) continue
        rawOps++
        let p = m[2].trim().replace(/`/g, '').replace(/\s+\([^)]*\)$/, '').replace(/^\{[^}]+\}/, '')
        p = normPath(p.replace(/\{[^}]+\}/g, ':param').replace(/:[A-Za-z0-9_]+/g, ':param'))
        uniqueRoutes.add(`${m[1].toUpperCase()} ${p}`)
      }
    }
    let inMatrix = 0, inFlows = 0
    for (const key of uniqueRoutes) {
      const [method, p] = key.split(' ')
      if (serverRoutes.some((r) => !r.skip && matchSegments(method, p, r))) inMatrix++
      if (flowSpecs.some((f) => matchSegments(method, p, f))) inFlows++
    }
    return { key: c.key, name: c.name, platform: c.platform, sha: c.sha, rawOps, uniqueRoutes: uniqueRoutes.size, inMatrix, inFlows }
  })
}

function main() {
  const { jsonOut } = parseArgs(process.argv.slice(2))
  const manifest = JSON.parse(fs.readFileSync(path.join(__dirname, 'routes.json'), 'utf8'))
  const serverRoutes = manifest.routes || []
  const web = extractWebClient()
  const corpus = loadOracleCorpus()

  const webHttp = [...web.httpMap.keys()].sort()
  const matrixMatched = [], matrixSkipped = [], matrixUnmatched = [], flowMatched = []
  for (const key of webHttp) {
    const [method, p] = key.split(' ')
    const hits = serverRoutes.filter((r) => matchSegments(method, p, r))
    if (hits.some((r) => !r.skip)) matrixMatched.push(key)
    else if (hits.length > 0) matrixSkipped.push(key)
    else matrixUnmatched.push(key)
    if (corpus.flowSpecs.some((f) => matchSegments(method, p, f))) flowMatched.push(key)
  }

  const appListeners = [...web.listeners.keys()].filter((e) => !SOCKET_BUILTINS.has(e)).sort()
  const allListeners = [...web.listeners.keys()].sort()
  const allEmitters = [...web.emitters.keys()].sort()
  const listenersObserved = appListeners.filter((e) => corpus.socketObserved.has(e))
  const emittersExercised = allEmitters.filter((e) => corpus.socketEmitted.has(e))
  const mobile = summarizeMobileClients(serverRoutes, corpus.flowSpecs)

  const report = {
    routesManifest: { total: serverRoutes.length, probed: serverRoutes.filter((r) => !r.skip).length },
    webClient: {
      uniqueHttpRoutes: webHttp.length,
      matchedMatrixProbed: matrixMatched.length,
      matchedMatrixSkipped: matrixSkipped.length,
      unmatchedMatrix: matrixUnmatched,
      exercisedByFlows: flowMatched.length,
      notInFlows: webHttp.filter((k) => !flowMatched.includes(k)),
      socketListenersTotal: allListeners.length,
      socketAppListeners: appListeners.length,
      socketListenersNamedInOracle: listenersObserved,
      socketEmittersTotal: allEmitters.length,
      socketEmittersExercised: emittersExercised,
      flowCheckSocketSteps: corpus.checkSocketSteps,
    },
    mobileClients: mobile,
  }

  console.log('=== Real Client Compatibility Audit (Web Nuxt SPA + 6 Mobile Clients) ===')
  console.log(`Oracle routes.json: ${report.routesManifest.probed} probed / ${report.routesManifest.total} total`)
  console.log(`Web HTTP routes   : ${webHttp.length} unique | ${matrixMatched.length} matched probed matrix (${matrixSkipped.length} skipped-only, ${matrixUnmatched.length} unmatched) | ${flowMatched.length} exercised by flows/complex*.go`)
  console.log(`Web Socket.IO     : ${allListeners.length} listeners (${appListeners.length} app + ${allListeners.length - appListeners.length} built-ins), ${listenersObserved.length} explicitly asserted in oracle (${corpus.checkSocketSteps} flow check_sockets steps capture broadcasts); ${emittersExercised.length}/${allEmitters.length} emitters exercised`)
  if (mobile.length > 0) {
    console.log('\nMobile Client      | Raw Ops | Unique | Matrix | Flows')
    console.log('-------------------+---------+--------+--------+------')
    for (const c of mobile) {
      console.log(`${c.key.padEnd(18)} | ${String(c.rawOps).padStart(7)} | ${String(c.uniqueRoutes).padStart(6)} | ${String(c.inMatrix).padStart(6)} | ${String(c.inFlows).padStart(5)}`)
    }
  }
  if (jsonOut) {
    fs.mkdirSync(path.dirname(jsonOut), { recursive: true })
    fs.writeFileSync(jsonOut, `${JSON.stringify(report, null, 2)}\n`, 'utf8')
    console.log(`\nWrote JSON report: ${jsonOut}`)
  }
}

main()
