#!/usr/bin/env node
// Runs the Twin-Server Differential Oracle (go test -tags=oracle) with both
// NODE_V8_COVERAGE (TS v2.37.2) and GOCOVERDIR (Go rewrite), generates a
// categorized Go-parity backlog report, runs optional client/E2E hooks, and
// prints a unified Oracle Scorecard.
import { spawn, spawnSync } from 'node:child_process'
import fs from 'node:fs'
import os from 'node:os'
import path from 'node:path'
import { fileURLToPath } from 'node:url'

function parsePctFlag(flag, arg, argv, index) {
  let nextIndex = index
  const raw = arg.includes('=') ? arg.slice(arg.indexOf('=') + 1) : argv[++nextIndex]
  const val = Number(raw)
  if (raw === undefined || raw.trim() === '' || !Number.isFinite(val) || val < 0 || val > 100) {
    console.error(`[oracle] invalid ${flag} value: ${raw}`)
    process.exit(2)
  }
  return { value: val, nextIndex }
}

function parseCliArgs(argv) {
  let minV8Pct = 0
  let minRoutePct = 0
  let webE2E = false
  const extraArgs = []
  for (let i = 0; i < argv.length; i++) {
    const arg = argv[i]
    if (arg === '--web-e2e') {
      webE2E = true
      continue
    }
    if (arg === '--min-v8-pct' || arg.startsWith('--min-v8-pct=')) {
      const parsed = parsePctFlag('--min-v8-pct', arg, argv, i)
      minV8Pct = parsed.value
      i = parsed.nextIndex
      continue
    }
    if (arg === '--min-route-pct' || arg.startsWith('--min-route-pct=')) {
      const parsed = parsePctFlag('--min-route-pct', arg, argv, i)
      minRoutePct = parsed.value
      i = parsed.nextIndex
      continue
    }
    extraArgs.push(arg)
  }
  return { minV8Pct, minRoutePct, webE2E, extraArgs }
}

function runStreamingCommand(cmd, args, opts, logPath) {
  return new Promise((resolve) => {
    const logStream = fs.createWriteStream(logPath, { encoding: 'utf8' })
    const child = spawn(cmd, args, {
      cwd: opts.cwd,
      env: opts.env,
      stdio: ['ignore', 'pipe', 'pipe'],
    })
    let streamClosed = false
    let exitStatus = null
    const finishIfReady = () => {
      if (streamClosed && exitStatus !== null) {
        resolve(exitStatus)
      }
    }
    logStream.on('finish', () => {
      streamClosed = true
      finishIfReady()
    })
    child.stdout.on('data', (chunk) => {
      process.stdout.write(chunk)
      logStream.write(chunk)
    })
    child.stderr.on('data', (chunk) => {
      process.stderr.write(chunk)
      logStream.write(chunk)
    })
    child.on('error', (err) => {
      const msg = `[oracle] failed to start ${cmd}: ${err.message}\n`
      process.stderr.write(msg)
      logStream.write(msg)
      exitStatus = 1
      logStream.end()
    })
    child.on('close', (code) => {
      exitStatus = code ?? 1
      logStream.end()
    })
  })
}

function matchExclusionBucket(relPath, buckets) {
  for (const b of buckets) {
    if (Array.isArray(b.exact) && b.exact.includes(relPath)) return b.bucket
    if (Array.isArray(b.prefixes) && b.prefixes.some((p) => relPath.startsWith(p))) return b.bucket
  }
  return null
}

function summarizeTotals(loaded, neverLoaded) {
  const coveredLines = loaded.reduce((s, f) => s + f.covered_lines, 0)
  const loadedLines = loaded.reduce((s, f) => s + f.lines, 0)
  const neverLines = neverLoaded.reduce((s, f) => s + f.lines, 0)
  const totalLines = loadedLines + neverLines
  const execFns = loaded.reduce((s, f) => s + f.executed_functions, 0)
  const totalFns = loaded.reduce((s, f) => s + f.functions, 0)
  const linePct = totalLines > 0 ? (coveredLines / totalLines) * 100 : 0
  const fnPct = totalFns > 0 ? (execFns / totalFns) * 100 : 0
  return {
    coveredLines,
    totalLines,
    linePct,
    execFns,
    totalFns,
    fnPct,
    loadedCount: loaded.length,
    neverCount: neverLoaded.length,
  }
}

function formatLine(label, stats) {
  return `${label}: ${stats.coveredLines} / ${stats.totalLines} (${stats.linePct.toFixed(1)}%) | functions ${stats.execFns} / ${stats.totalFns} (${stats.fnPct.toFixed(1)}%)`
}

function printCoverageSummary(v8JsonPath, exclusionsPath) {
  const report = JSON.parse(fs.readFileSync(v8JsonPath, 'utf8'))
  const exclusions = JSON.parse(fs.readFileSync(exclusionsPath, 'utf8'))
  const buckets = exclusions.excluded_buckets ?? []

  const appLoaded = report.files.filter((f) => f.group === 'app')
  const appNever = report.never_loaded.filter((f) => f.group === 'app')
  const totalApp = summarizeTotals(appLoaded, appNever)

  const inScopeLoaded = []
  const inScopeNever = []
  const excludedCounts = new Map()
  for (const f of appLoaded) {
    const bucket = matchExclusionBucket(f.path, buckets)
    if (bucket) excludedCounts.set(bucket, (excludedCounts.get(bucket) ?? 0) + f.lines)
    else inScopeLoaded.push(f)
  }
  for (const f of appNever) {
    const bucket = matchExclusionBucket(f.path, buckets)
    if (bucket) excludedCounts.set(bucket, (excludedCounts.get(bucket) ?? 0) + f.lines)
    else inScopeNever.push(f)
  }
  const inScope = summarizeTotals(inScopeLoaded, inScopeNever)

  console.log('\n[oracle] === Machine-Measured Coverage Summary ===')
  console.log(formatLine('Total server/ app lines', totalApp))
  console.log(formatLine('In-scope server/ app lines (excl. approved migrations/exclusions)', inScope))
  if (excludedCounts.size > 0) {
    const parts = [...excludedCounts.entries()].map(([k, lines]) => `${k}=${lines}L`)
    console.log(`Excluded app buckets: ${parts.join(', ')}`)
  }

  const remaining = [
    ...inScopeLoaded.map((f) => ({
      path: f.path,
      uncovered: f.lines - f.covered_lines,
      total: f.lines,
      pct: f.lines > 0 ? (f.covered_lines / f.lines) * 100 : 100,
      fns: `${f.executed_functions}/${f.functions}`,
    })),
    ...inScopeNever.map((f) => ({
      path: f.path,
      uncovered: f.lines,
      total: f.lines,
      pct: 0,
      fns: '0/? (never loaded)',
    })),
  ]
    .filter((f) => f.uncovered > 0)
    .sort((a, b) => b.uncovered - a.uncovered || a.path.localeCompare(b.path))
    .slice(0, 15)

  console.log('\nTop 15 remaining uncovered in-scope files:')
  if (remaining.length === 0) {
    console.log('  (none)')
  } else {
    for (const f of remaining) {
      console.log(`  ${f.path.padEnd(50)} ${String(f.uncovered).padStart(5)} uncov / ${String(f.total).padStart(5)} (${f.pct.toFixed(1).padStart(5)}%)  fns ${f.fns}`)
    }
  }
  return inScope
}

function unquoteFindingValue(raw) {
  try {
    return String(JSON.parse(raw))
  } catch {
    return raw.replace(/^"|"$/g, '')
  }
}

function classifyRootCause(f) {
  const detail = (f.detail || '').trim()
  if (f.surface === 'http') {
    if (f.path === 'status') return `HTTP status mismatch (ts=${f.ts} vs go=${f.go})`
    if (f.path.startsWith('header.')) return `HTTP header mismatch (${f.path})`
    if (f.path.startsWith('cookie.')) return `HTTP cookie mismatch (${f.path})`
    if (detail) return detail
    return `HTTP body mismatch (${f.path})`
  }
  if (f.surface === 'db') {
    if (/typeof|storage class/i.test(detail)) return 'SQLite typeof() storage class mismatch'
    if (/missing row/i.test(detail)) return 'SQLite row missing in Go'
    if (/extra row/i.test(detail)) return 'SQLite extra row in Go'
    return detail ? `SQLite diff: ${detail}` : `SQLite diff (${f.path})`
  }
  if (f.surface === 'socket') {
    return detail ? `Socket.IO diff: ${detail}` : `Socket.IO event mismatch (${f.path})`
  }
  if (f.surface === 'fs') {
    if (f.path.includes('metadata.json')) return 'FS metadata.json mismatch'
    return detail ? `FS diff: ${detail}` : `FS mismatch (${f.path})`
  }
  if (f.surface === 'stub') {
    return detail ? `Outbound stub diff: ${detail}` : `Outbound stub mismatch (${f.path})`
  }
  return detail || `${f.surface} mismatch`
}

function parseOracleLog(logText) {
  const lines = logText.split(/\r?\n/)
  const stats = {
    flowSteps: { pass: 0, fail: 0, skip: 0 },
    complex: { pass: 0, fail: 0, skip: 0 },
    routeMatrix: { pass: 0, fail: 0, skip: 0, topStatus: null, summary: null },
  }
  const findings = []
  const failedTests = []
  const testsWithFindings = new Set()

  let currentTest = ''
  let inUnexpected = false

  const runRe = /^=== RUN\s+(\S.*)$/
  const resultRe = /^--- (PASS|FAIL|SKIP):\s+(.+?)\s+\([\d.]+s\)$/
  const unexpectedHeaderRe = /(?:^|\s)unexpected divergences \(\d+\):$/
  const acknowledgedHeaderRe = /(?:^|\s)acknowledged divergences \(\d+\):$/
  const findingRe = /^\[([^\]]+)\]\s+step=(.+?)\s+path=(.+?)\s+ts=("(?:[^"\\]|\\.)*")\s+go=("(?:[^"\\]|\\.)*")\s+\((.*)\)$/
  const matrixSummaryRe = /route matrix:\s+total=(\d+)\s+probed=(\d+)\s+checks=(\d+)\s+acknowledged=(\d+)\s+unexpected=(\d+)/

  for (const rawLine of lines) {
    const trimmed = rawLine.trim()
    const runMatch = trimmed.match(runRe)
    if (runMatch) {
      currentTest = runMatch[1].trim()
      inUnexpected = false
      continue
    }

    const resMatch = trimmed.match(resultRe)
    if (resMatch) {
      inUnexpected = false
      const outcome = resMatch[1].toLowerCase()
      const testName = resMatch[2]
      if (/^TestOracleFlows\/[^/]+\/[^/]+$/.test(testName)) {
        stats.flowSteps[outcome]++
      } else if (/^TestOracle(?:Complex|Deep)\w+$/.test(testName)) {
        stats.complex[outcome]++
      } else if (testName === 'TestOracleRouteMatrix') {
        stats.routeMatrix.topStatus = resMatch[1]
      } else if (testName.startsWith('TestOracleRouteMatrix/')) {
        stats.routeMatrix[outcome]++
      }
      if (outcome === 'fail') {
        failedTests.push(testName)
      }
      continue
    }

    const matrixMatch = trimmed.match(matrixSummaryRe)
    if (matrixMatch) {
      stats.routeMatrix.summary = {
        total: Number(matrixMatch[1]),
        probed: Number(matrixMatch[2]),
        checks: Number(matrixMatch[3]),
        acknowledged: Number(matrixMatch[4]),
        unexpected: Number(matrixMatch[5]),
      }
    }

    if (unexpectedHeaderRe.test(trimmed)) {
      inUnexpected = true
      continue
    }
    if (acknowledgedHeaderRe.test(trimmed) || trimmed.startsWith('=== ') || trimmed === 'FAIL') {
      inUnexpected = false
      continue
    }

    if (inUnexpected) {
      const fMatch = trimmed.match(findingRe)
      if (fMatch) {
        const finding = {
          surface: fMatch[1],
          step: fMatch[2],
          path: fMatch[3],
          ts: unquoteFindingValue(fMatch[4]),
          go: unquoteFindingValue(fMatch[5]),
          detail: fMatch[6],
          testName: currentTest,
        }
        findings.push(finding)
        if (currentTest) testsWithFindings.add(currentTest)
      } else if (trimmed === '') {
        inUnexpected = false
      }
    }
  }

  for (const failed of failedTests) {
    const hasNested = [...testsWithFindings].some((t) => t === failed || t.startsWith(`${failed}/`))
    const isParentWithChildFail = failedTests.some((other) => other !== failed && other.startsWith(`${failed}/`))
    if (!hasNested && !isParentWithChildFail) {
      findings.push({
        surface: 'test_error',
        step: failed,
        path: 'test',
        ts: '',
        go: 'FAIL',
        detail: 'test failed without structured divergence finding (see oracle-test.log)',
        testName: failed,
      })
    }
  }

  return { stats, findings }
}

function writeParityBacklog(backlogPath, parsed) {
  const { findings } = parsed
  const surfacePriority = ['http', 'db', 'socket', 'fs', 'stub', 'test_error']
  const groups = new Map()

  for (const f of findings) {
    const rootCause = classifyRootCause(f)
    const key = `${f.surface}\0${f.step}\0${rootCause}`
    let grp = groups.get(key)
    if (!grp) {
      grp = { surface: f.surface, step: f.step, rootCause, items: [] }
      groups.set(key, grp)
    }
    grp.items.push(f)
  }

  const bySurface = new Map()
  for (const grp of groups.values()) {
    const arr = bySurface.get(grp.surface) ?? []
    arr.push(grp)
    bySurface.set(grp.surface, arr)
  }
  for (const arr of bySurface.values()) {
    arr.sort((a, b) => b.items.length - a.items.length || a.step.localeCompare(b.step) || a.rootCause.localeCompare(b.rootCause))
  }

  const lines = [
    '# Go Parity Backlog (Unexpected Oracle Divergences)',
    '',
    `Generated: ${new Date().toISOString()}`,
    `Total unexpected findings: **${findings.length}** across **${groups.size}** (surface, step/route, root cause) groups`,
    '',
  ]

  if (findings.length === 0) {
    lines.push('No remaining unexpected Go-vs-TS parity findings.')
  } else {
    lines.push('## Summary by Surface', '', '| Surface | Groups | Findings |', '| --- | ---: | ---: |')
    const orderedSurfaces = [...bySurface.keys()].sort((a, b) => {
      const ia = surfacePriority.indexOf(a)
      const ib = surfacePriority.indexOf(b)
      return (ia === -1 ? 99 : ia) - (ib === -1 ? 99 : ib) || a.localeCompare(b)
    })
    for (const surface of orderedSurfaces) {
      const arr = bySurface.get(surface)
      const count = arr.reduce((s, g) => s + g.items.length, 0)
      lines.push(`| ${surface} | ${arr.length} | ${count} |`)
    }
    for (const surface of orderedSurfaces) {
      lines.push('', `## ${surface.toUpperCase()}`, '')
      for (const grp of bySurface.get(surface)) {
        lines.push(`### \`${grp.step}\` — ${grp.rootCause} (${grp.items.length})`)
        const sample = grp.items.slice(0, 8)
        for (const item of sample) {
          lines.push(`- \`${item.path}\`: ts=\`${item.ts}\` vs go=\`${item.go}\` (${item.detail})`)
        }
        if (grp.items.length > sample.length) {
          lines.push(`- _...and ${grp.items.length - sample.length} more in this group_`)
        }
        lines.push('')
      }
    }
  }

  fs.writeFileSync(backlogPath, `${lines.join('\n')}\n`, 'utf8')
  return { totalFindings: findings.length, totalGroups: groups.size }
}

function measureGoCoverage(goBackend, goCoverDir) {
  const entries = fs.existsSync(goCoverDir) ? fs.readdirSync(goCoverDir) : []
  if (entries.length === 0) {
    return { pct: null, rawLine: 'N/A (no covdata emitted)' }
  }
  console.log('\n[oracle] === Go Server Coverage ===')
  spawnSync('go', ['tool', 'covdata', 'percent', `-i=${goCoverDir}`], {
    cwd: goBackend,
    stdio: 'inherit',
  })
  const funcRes = spawnSync('go', ['tool', 'covdata', 'func', `-i=${goCoverDir}`], {
    cwd: goBackend,
    encoding: 'utf8',
    maxBuffer: 32 * 1024 * 1024,
  })
  if (funcRes.status !== 0 || !funcRes.stdout) {
    return { pct: null, rawLine: 'N/A (go tool covdata func failed)' }
  }
  const lines = funcRes.stdout.trim().split(/\r?\n/)
  for (let i = lines.length - 1; i >= 0; i--) {
    const m = lines[i].match(/^total:?\s+\(statements\)\s+([\d.]+)%\s*$/)
    if (m) {
      return { pct: Number(m[1]), rawLine: `${m[1]}%` }
    }
  }
  return { pct: null, rawLine: 'N/A (total line missing)' }
}

function firstFinite(...vals) {
  for (const v of vals) {
    if (typeof v === 'number' && Number.isFinite(v)) return v
  }
  return null
}

function loadSurfaceHits(surfaceOutPath, routesManifestPath) {
  if (!fs.existsSync(surfaceOutPath)) return null
  const raw = JSON.parse(fs.readFileSync(surfaceOutPath, 'utf8'))
  const routesObj = raw.routes && !Array.isArray(raw.routes) && typeof raw.routes === 'object' ? raw.routes : {}
  const sockObj = raw.sockets && typeof raw.sockets === 'object' ? raw.sockets : {}
  const inObj = sockObj.inbound && typeof sockObj.inbound === 'object' ? sockObj.inbound : {}
  const outObj = sockObj.outbound && typeof sockObj.outbound === 'object' ? sockObj.outbound : {}

  let total = firstFinite(routesObj.in_scope, routesObj.total, routesObj.probed, raw.routes_in_scope, raw.routes_total, raw.total_routes)
  let happy = firstFinite(routesObj.happy, routesObj.happy_path, routesObj.happy_hit, routesObj.happy_count, raw.routes_happy_hit, raw.routes_happy, raw.happy_routes_count)
  let error = firstFinite(routesObj.error, routesObj.error_probed, routesObj.error_hit, routesObj.error_count, raw.routes_error_hit, raw.routes_error, raw.error_routes_count)
  let any = firstFinite(routesObj.any, routesObj.any_hit, routesObj.any_count, raw.routes_any_hit, raw.routes_any, raw.any_routes_count)

  let unhitHappyRoutes = Array.isArray(raw.unhit_happy_routes) ? raw.unhit_happy_routes.slice() : null
  let modified = false

  // Derive missing route counts or unhit_happy_routes from per-route hit records + routes.json if needed.
  const rawHits = Array.isArray(raw.route_hits)
    ? raw.route_hits
    : Array.isArray(raw.routes)
      ? raw.routes
      : (raw.route_hits && typeof raw.route_hits === 'object')
        ? Object.entries(raw.route_hits).map(([key, val]) => ({ key, ...(val && typeof val === 'object' ? val : {}) }))
        : (raw.hits && typeof raw.hits === 'object' && !Array.isArray(raw.hits))
          ? Object.entries(raw.hits).map(([key, val]) => ({ key, ...(val && typeof val === 'object' ? val : {}) }))
          : null

  if ((unhitHappyRoutes === null || total === null || happy === null || error === null || any === null) && rawHits) {
    const hitMap = new Map()
    for (const entry of rawHits) {
      const key = entry.key || `${String(entry.method || '').toUpperCase()} ${entry.path || ''}`.trim()
      if (!key) continue
      const statuses = Array.isArray(entry.statuses) ? entry.statuses : Array.isArray(entry.status_codes) ? entry.status_codes : []
      const isHappy = Boolean(entry.happy || entry.happy_hit || (entry.happy_hits ?? 0) > 0 || statuses.some((s) => s >= 200 && s < 400))
      const isError = Boolean(entry.error || entry.error_hit || (entry.error_hits ?? 0) > 0 || statuses.some((s) => s >= 400 && s < 500))
      const isAny = Boolean(entry.any || entry.any_hit || (entry.total_hits ?? entry.hits ?? 0) > 0 || isHappy || isError || statuses.length > 0)
      hitMap.set(key, { happy: isHappy, error: isError, any: isAny })
    }
    if (fs.existsSync(routesManifestPath)) {
      const manifest = JSON.parse(fs.readFileSync(routesManifestPath, 'utf8'))
      const probed = (manifest.routes || []).filter((r) => !r.skip)
      const computedUnhit = []
      let hCount = 0
      let eCount = 0
      let aCount = 0
      for (const r of probed) {
        const key = `${r.method} ${r.path}`
        const rec = hitMap.get(key)
        if (rec?.happy) hCount++
        else computedUnhit.push(key)
        if (rec?.error) eCount++
        if (rec?.any) aCount++
      }
      if (total === null) total = probed.length
      if (happy === null) happy = hCount
      if (error === null) error = eCount
      if (any === null) any = aCount
      if (unhitHappyRoutes === null) {
        unhitHappyRoutes = computedUnhit
        raw.unhit_happy_routes = unhitHappyRoutes
        modified = true
      }
    }
  }

  if (unhitHappyRoutes === null) {
    unhitHappyRoutes = []
    raw.unhit_happy_routes = unhitHappyRoutes
    modified = true
  }
  if (modified) {
    fs.writeFileSync(surfaceOutPath, `${JSON.stringify(raw, null, 2)}\n`, 'utf8')
  }

  const totalN = total ?? 0
  const happyN = happy ?? 0
  const errorN = error ?? 0
  const anyN = any ?? 0
  const pct = (n, d) => (d > 0 ? (n / d) * 100 : 0)

  const countOf = (v) => (Array.isArray(v) ? v.length : (typeof v === 'number' && Number.isFinite(v) ? v : null))
  const inboundHit = firstFinite(countOf(inObj.hit), countOf(inObj.live), countOf(sockObj.inbound_hit), countOf(sockObj.inbound_live), countOf(raw.socket_inbound_hit), countOf(raw.inbound_hit)) ?? 0
  const inboundTotal = firstFinite(inObj.total, sockObj.inbound_total, raw.socket_inbound_total, raw.inbound_total) ?? 0
  const outboundHit = firstFinite(countOf(outObj.hit), countOf(outObj.live), countOf(sockObj.outbound_hit), countOf(sockObj.outbound_live), countOf(raw.socket_outbound_hit), countOf(raw.outbound_hit)) ?? 0
  const outboundTotal = firstFinite(outObj.total, sockObj.outbound_total, raw.socket_outbound_total, raw.outbound_total) ?? 0

  return {
    total: totalN,
    happy: happyN,
    happyPct: totalN > 0 ? pct(happyN, totalN) : (firstFinite(routesObj.happy_pct, raw.happy_pct) ?? 0),
    error: errorN,
    errorPct: totalN > 0 ? pct(errorN, totalN) : (firstFinite(routesObj.error_pct, raw.error_pct) ?? 0),
    any: anyN,
    anyPct: totalN > 0 ? pct(anyN, totalN) : (firstFinite(routesObj.any_pct, raw.any_pct) ?? 0),
    unhitHappyRoutes,
    inboundHit,
    inboundTotal,
    outboundHit,
    outboundTotal,
  }
}

function runClientAuditHook(here, repoRoot, env) {
  const scriptPath = path.join(here, 'client-audit.mjs')
  if (!fs.existsSync(scriptPath)) return null
  console.log('\n[oracle] === Web Client Audit (client-audit.mjs) ===')
  const res = spawnSync(process.execPath, [scriptPath], {
    cwd: repoRoot,
    env,
    encoding: 'utf8',
    maxBuffer: 16 * 1024 * 1024,
  })
  if (res.stdout) process.stdout.write(res.stdout)
  if (res.stderr) process.stderr.write(res.stderr)
  const combined = `${res.stdout || ''}\n${res.stderr || ''}`
  const allLines = combined.split(/\r?\n/).map((l) => l.trim())
  const httpRaw = allLines.find((l) => /^Web HTTP routes\s*:/i.test(l))
  const socketRaw = allLines.find((l) => /^Web Socket\.IO\s*:/i.test(l))
  const httpSummary = httpRaw
    ? httpRaw
      .replace(/^Web HTTP routes\s*:\s*/i, '')
      .replace(/\s*matched probed matrix\s*/i, ' matrix ')
      .replace(/\s*exercised by flows\/complex\*\.go/i, ' flows')
    : null
  const socketSummary = socketRaw ? socketRaw.replace(/^Web Socket\.IO\s*:\s*/i, '') : null
  const summaryLines = allLines
    .filter((l) => /^(?:Web HTTP routes|Web Socket\.IO)\s*:|\$axios|socket\.io|coverage|scorecard|summary/i.test(l))
    .slice(0, 3)
  return {
    passed: res.status === 0,
    httpSummary,
    socketSummary,
    summary: summaryLines.length > 0 ? summaryLines.join(' | ') : (res.status === 0 ? 'PASS' : `FAIL (exit ${res.status})`),
  }
}

function runWebE2EHook(here, repoRoot, env) {
  const scriptPath = path.join(here, 'web-e2e.mjs')
  if (!fs.existsSync(scriptPath)) {
    return { ran: false, passed: true, summary: 'SKIPPED (web-e2e.mjs not present)' }
  }
  console.log('\n[oracle] === Web E2E (--optional) ===')
  const res = spawnSync(process.execPath, [scriptPath, '--optional'], {
    cwd: repoRoot,
    env,
    encoding: 'utf8',
    maxBuffer: 16 * 1024 * 1024,
  })
  if (res.stdout) process.stdout.write(res.stdout)
  if (res.stderr) process.stderr.write(res.stderr)
  return {
    ran: true,
    passed: res.status === 0,
    summary: res.status === 0 ? 'PASS' : `FAIL (exit ${res.status})`,
  }
}

function formatPassFail(counts) {
  const total = counts.pass + counts.fail
  if (total === 0 && counts.skip === 0) return 'NOT RUN'
  const status = counts.fail === 0 ? 'PASS' : 'FAIL'
  const skipPart = counts.skip > 0 ? `, SKIP ${counts.skip}` : ''
  return `${status} (PASS ${counts.pass} / FAIL ${counts.fail}${skipPart})`
}

function printScorecard(data) {
  const { parsed, backlog, v8Stats, surfaceStats, surfaceOutPath, goCov, clientAudit, webE2E, logPath, backlogPath, tmpRoot } = data
  const { flowSteps, complex, routeMatrix } = parsed.stats
  const matrixVerdict = routeMatrix.topStatus ?? (routeMatrix.fail === 0 ? (routeMatrix.pass > 0 ? 'PASS' : 'NOT RUN') : 'FAIL')
  const matrixDetail = routeMatrix.summary
    ? `probed=${routeMatrix.summary.probed}/${routeMatrix.summary.total}, checks=${routeMatrix.summary.checks}, unexpected=${routeMatrix.summary.unexpected}`
    : `PASS ${routeMatrix.pass} / FAIL ${routeMatrix.fail}`

  console.log('\n=== ORACLE SCORECARD ===')
  console.log(`Flow Steps:                  ${formatPassFail(flowSteps)}`)
  console.log(`Complex Tests:               ${formatPassFail(complex)}`)
  console.log(`Route Matrix:                ${matrixVerdict} (${matrixDetail})`)
  if (v8Stats) {
    console.log(`TS V8 In-Scope Coverage:     ${v8Stats.linePct.toFixed(1)}% (${v8Stats.coveredLines}/${v8Stats.totalLines} lines, fns ${v8Stats.fnPct.toFixed(1)}%)`)
  } else {
    console.log('TS V8 In-Scope Coverage:     N/A')
  }
  console.log(`Go Server Statement Cov:     ${goCov.rawLine}`)
  if (surfaceStats) {
    console.log('Runtime Surface-Area Completeness:')
    console.log(`  Routes Happy-Path (2xx/3xx): ${surfaceStats.happy} / ${surfaceStats.total} (${surfaceStats.happyPct.toFixed(1)}%)`)
    console.log(`  Routes Error-Probed (4xx):   ${surfaceStats.error} / ${surfaceStats.total} (${surfaceStats.errorPct.toFixed(1)}%)`)
    console.log(`  Routes Any Hit:              ${surfaceStats.any} / ${surfaceStats.total} (${surfaceStats.anyPct.toFixed(1)}%)`)
    console.log(`  Socket Events Live:          inbound ${surfaceStats.inboundHit}/${surfaceStats.inboundTotal}, outbound ${surfaceStats.outboundHit}/${surfaceStats.outboundTotal}`)
    if (surfaceStats.unhitHappyRoutes.length > 0) {
      const shown = surfaceStats.unhitHappyRoutes.slice(0, 25)
      const extra = surfaceStats.unhitHappyRoutes.length - shown.length
      console.log(`  Unhit Happy-Path Routes (${surfaceStats.unhitHappyRoutes.length} total${extra > 0 ? `, showing first ${shown.length}` : ''}):`)
      for (const route of shown) {
        console.log(`    - ${route}`)
      }
      if (extra > 0) {
        console.log(`    ... and ${extra} more (full list in ${surfaceOutPath})`)
      }
    }
  }
  if (clientAudit) {
    if (clientAudit.httpSummary || clientAudit.socketSummary) {
      console.log(`Web Client Audit ($axios/IO): ${clientAudit.passed ? 'PASS' : 'FAIL'}`)
      if (clientAudit.httpSummary) {
        console.log(`  HTTP:   ${clientAudit.httpSummary}`)
      }
      if (clientAudit.socketSummary) {
        console.log(`  Socket: ${clientAudit.socketSummary}`)
      }
    } else {
      console.log(`Web Client Audit ($axios/IO): ${clientAudit.passed ? 'PASS' : 'FAIL'} — ${clientAudit.summary}`)
    }
  }
  if (webE2E) {
    console.log(`Web E2E (--web-e2e):         ${webE2E.summary}`)
  }
  console.log(`Go Parity Backlog:           ${backlog.totalFindings} findings (${backlog.totalGroups} groups) -> ${backlogPath}`)
  if (surfaceStats) {
    console.log(`Surface Hits JSON:           ${surfaceOutPath}`)
  }
  console.log(`Oracle Test Log:             ${logPath}`)
  console.log(`Artifacts Dir:               ${tmpRoot}`)
}

const here = path.dirname(fileURLToPath(import.meta.url))
const goBackend = path.resolve(here, '../..')
const repoRoot = path.resolve(goBackend, '..')
const tmpRoot = fs.mkdtempSync(path.join(os.tmpdir(), 'abs-oracle-run-'))
const v8Dir = path.join(tmpRoot, 'v8')
const goCoverDir = path.join(tmpRoot, 'gocover')
const logPath = path.join(tmpRoot, 'oracle-test.log')
const backlogPath = path.join(tmpRoot, 'go-parity-backlog.md')
const surfaceOutPath = path.join(tmpRoot, 'surface-hits.json')
fs.mkdirSync(v8Dir, { recursive: true })
fs.mkdirSync(goCoverDir, { recursive: true })

const { minV8Pct, minRoutePct, webE2E: runWebE2E, extraArgs } = parseCliArgs(process.argv.slice(2))
const webpBin = '/private/tmp/abs-cleanup-checkpoint-20261105/ffmpeg-9.0.2-webp/bin'
const hasWebp = fs.existsSync(path.join(webpBin, 'ffmpeg'))
const env = {
  ...process.env,
  ORACLE_V8_DIR: v8Dir,
  ORACLE_GOCOVER_DIR: goCoverDir,
  ORACLE_SURFACE_OUT: surfaceOutPath,
  ...(process.version.startsWith('v22.') && !process.env.ORACLE_NODE ? { ORACLE_NODE: process.execPath } : {}),
  ...(hasWebp && !process.env.FFMPEG_PATH ? {
    PATH: `${webpBin}:${process.env.PATH || ''}`,
    FFMPEG_PATH: path.join(webpBin, 'ffmpeg'),
    FFPROBE_PATH: path.join(webpBin, 'ffprobe'),
  } : {}),
}

console.log(`[oracle] running twin-server differential suite (v8=${v8Dir}, gocover=${goCoverDir})`)
const goTestStatus = await runStreamingCommand(
  'go',
  ['test', '-tags=oracle', '-v', '-count=1', '-timeout=20m', './testmachine/oracle', ...extraArgs],
  { cwd: goBackend, env },
  logPath,
)

const logText = fs.existsSync(logPath) ? fs.readFileSync(logPath, 'utf8') : ''
const parsed = parseOracleLog(logText)
const backlog = writeParityBacklog(backlogPath, parsed)

const v8JsonPath = path.join(tmpRoot, 'ts-v8-coverage.json')
console.log('\n[oracle] === TS v2.37.2 V8 Coverage ===')
const v8Report = spawnSync(process.execPath, [
  path.join(repoRoot, 'go-backend/testmachine/scripts/verify-coverage.mjs'),
  '--v8', v8Dir,
  '--root', repoRoot,
  '--out', v8JsonPath,
  '--md', path.join(tmpRoot, 'ts-v8-coverage.md'),
  '--top', '20',
], { cwd: repoRoot, stdio: 'inherit' })

let gateFailed = false
let v8Stats = null
if (fs.existsSync(v8JsonPath)) {
  v8Stats = printCoverageSummary(v8JsonPath, path.join(here, 'coverage-exclusions.json'))
  if (minV8Pct > 0 && v8Stats.linePct < minV8Pct) {
    console.error(`\n[oracle] FAIL: in-scope V8 line coverage ${v8Stats.linePct.toFixed(2)}% is below --min-v8-pct ${minV8Pct}%`)
    gateFailed = true
  }
} else if (minV8Pct > 0) {
  console.error(`\n[oracle] FAIL: V8 coverage JSON not generated at ${v8JsonPath}`)
  gateFailed = true
}

const surfaceStats = loadSurfaceHits(surfaceOutPath, path.join(here, 'routes.json'))
if (minRoutePct > 0) {
  if (!surfaceStats) {
    console.error(`\n[oracle] FAIL: surface-hits.json not generated at ${surfaceOutPath}`)
    gateFailed = true
  } else if (surfaceStats.happyPct < minRoutePct) {
    console.error(`\n[oracle] FAIL: happy-path route coverage ${surfaceStats.happyPct.toFixed(1)}% is below --min-route-pct ${minRoutePct}%`)
    gateFailed = true
  }
}

const goCov = measureGoCoverage(goBackend, goCoverDir)
const clientAudit = runClientAuditHook(here, repoRoot, env)
const webE2E = runWebE2E ? runWebE2EHook(here, repoRoot, env) : null

printScorecard({ parsed, backlog, v8Stats, surfaceStats, surfaceOutPath, goCov, clientAudit, webE2E, logPath, backlogPath, tmpRoot })

let exitCode = 0
if (goTestStatus !== 0) exitCode = goTestStatus
else if (v8Report.status !== 0) exitCode = v8Report.status ?? 1
else if (gateFailed) exitCode = 1
else if (clientAudit && !clientAudit.passed) exitCode = 1
else if (webE2E && !webE2E.passed) exitCode = 1
process.exit(exitCode)
