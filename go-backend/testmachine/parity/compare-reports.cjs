#!/usr/bin/env node
'use strict'

const fs = require('node:fs')

const UUID = /[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}/gi
const TIMESTAMP_FIELD = /(?:At|Seen|Scan|Update|Time)$/i
const ISO_TIMESTAMP = /^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(?:\.\d+)?(?:Z|[+-]\d{2}:\d{2})$/
const ISO_DATE = /^\d{4}-\d{2}-\d{2}$/
const MIN_EPOCH_MS = 100_000_000_000
const REPEATABILITY_POLICY = 'schema2: UUID relationships are canonicalized; JWTs are validated during the run and compared by verdict; only file-delivery ETag/Last-Modified, playback-session date/weekday, fixture-rooted state paths, and timestamp/port fields are normalized across reports.'

function collectUUIDs(value, uuids = new Map()) {
  if (typeof value === 'string') {
    for (const match of value.matchAll(UUID)) {
      const uuid = match[0].toLowerCase()
      if (!uuids.has(uuid)) uuids.set(uuid, `u${uuids.size + 1}`)
    }
  } else if (Array.isArray(value)) {
    value.forEach(child => collectUUIDs(child, uuids))
  } else if (value && typeof value === 'object') {
    for (const child of Object.values(value)) collectUUIDs(child, uuids)
  }
  return uuids
}

function isVolatileTimestamp(field, value) {
  if (!TIMESTAMP_FIELD.test(field)) return false
  if (typeof value === 'string') {
    if (ISO_DATE.test(value)) return new Date(value).toISOString().slice(0, 10) === value
    const parsed = ISO_TIMESTAMP.test(value)
      ? Date.parse(value)
      : /^\d{4}-\d{2}-\d{2} \d{2}:\d{2}:\d{2}(?:\.\d+)? [+-]\d{2}:\d{2}$/.test(value)
        ? Date.parse(value.replace(' ', 'T').replace(/ ([+-]\d{2}:\d{2})$/, '$1'))
        : Number.NaN
    return Number.isFinite(parsed)
  }
  return typeof value === 'number' && Number.isInteger(value) && (value === 0 || Math.abs(value) >= MIN_EPOCH_MS)
}

function normalize(value, field, uuids) {
  if (typeof value === 'string' && /^<fixture-jwt-sha256:[0-9a-f]{64}>$/.test(value)) return '<per-run-jwt-equality-checked>'
  if (isVolatileTimestamp(field, value)) return '<volatile-time>'
  if (field.toLowerCase() === 'port' && Number.isInteger(value)) return '<volatile-port>'
  if (typeof value === 'string') {
    return value.replace(UUID, match => uuids.get(match.toLowerCase()) || match)
  }
  if (Array.isArray(value)) return value.map(child => normalize(child, field, uuids))
  if (value && typeof value === 'object') {
    return Object.fromEntries(Object.entries(value).map(([key, child]) => [key, normalize(child, key, uuids)]))
  }
  return value
}

const FILE_DELIVERY_CASES = new Set(['direct-file-range', 'direct-file-head-range', 'direct-file-invalid-range', 'direct-file-head-invalid-range', 'item-file-download', 'item-file-download-range'])

function normalizedSide(record, side, caseID) {
  const bodyKey = `${side}Body`
  const uuids = collectUUIDs(record[bodyKey])
  const result = {}
  for (const key of [`${side}Status`, `${side}Headers`, bodyKey]) {
    if (Object.hasOwn(record, key)) result[key] = normalize(record[key], key, uuids)
  }
  if (FILE_DELIVERY_CASES.has(caseID) && result[`${side}Headers`]) {
    const headers = result[`${side}Headers`]
    for (const key of Object.keys(headers)) {
      const normalizedKey = key.toLowerCase()
      if (normalizedKey === 'etag') {
        if (typeof headers[key] !== 'string' || !/^(?:W\/)?"[0-9a-f]+-[0-9a-f]+"$/i.test(headers[key])) throw new Error(`${caseID}: ${side} ETag is not a valid file validator`)
        headers[key] = '<fixture-file-metadata>'
      }
      if (normalizedKey === 'last-modified') {
        if (typeof headers[key] !== 'string' || !Number.isFinite(Date.parse(headers[key])) || !/^[A-Z][a-z]{2}, \d{2} [A-Z][a-z]{2} \d{4} \d{2}:\d{2}:\d{2} GMT$/.test(headers[key]) || new Date(Date.parse(headers[key])).toUTCString() !== headers[key]) throw new Error(`${caseID}: ${side} Last-Modified is not a canonical HTTP date`)
        headers[key] = '<fixture-file-metadata>'
      }
    }
  }
  if (caseID === 'playback-session' && result[bodyKey] && typeof result[bodyKey] === 'object') {
    result[bodyKey] = structuredClone(result[bodyKey])
    if (Object.hasOwn(result[bodyKey], 'date') || Object.hasOwn(result[bodyKey], 'dayOfWeek')) {
      const date = result[bodyKey].date
      const weekday = result[bodyKey].dayOfWeek
      const parsedDate = typeof date === 'string' && ISO_DATE.test(date) ? new Date(`${date}T00:00:00.000Z`) : null
      if (!parsedDate || parsedDate.toISOString().slice(0, 10) !== date) throw new Error(`${caseID}: ${side} date is not a valid ISO calendar date`)
      const weekdays = ['Sunday', 'Monday', 'Tuesday', 'Wednesday', 'Thursday', 'Friday', 'Saturday']
      if (typeof weekday !== 'string' || weekday !== weekdays[parsedDate.getUTCDay()]) throw new Error(`${caseID}: ${side} dayOfWeek does not match date`)
      result[bodyKey].date = '<run-date>'
      result[bodyKey].dayOfWeek = '<run-weekday>'
    }
  }
  return result
}

function normalizeFixturePaths(value, report) {
  const roots = Object.values(report?.fixtureProvenance || {}).filter(root => typeof root === 'string' && root.length > 0).sort((a, b) => b.length - a.length)
  if (!roots.length) return value
  const visit = node => {
    if (typeof node === 'string') {
      for (const root of roots) if (node === root || node.startsWith(`${root}/`)) return `<fixture-root>${node.slice(root.length)}`
      return node
    }
    if (Array.isArray(node)) return node.map(visit)
    if (node && typeof node === 'object') return Object.fromEntries(Object.entries(node).map(([key, child]) => [key, visit(child)]))
    return node
  }
  return visit(value)
}

function differencePaths(left, right, path = '$') {
  if (Object.is(left, right)) return []
  if (Array.isArray(left) || Array.isArray(right)) {
    if (!Array.isArray(left) || !Array.isArray(right)) return [path]
    const differences = []
    if (left.length !== right.length) differences.push(`${path}.length`)
    for (let index = 0; index < Math.min(left.length, right.length); index++) {
      differences.push(...differencePaths(left[index], right[index], `${path}[${index}]`))
    }
    return differences
  }
  const leftObject = left !== null && typeof left === 'object'
  const rightObject = right !== null && typeof right === 'object'
  if (leftObject || rightObject) {
    if (!leftObject || !rightObject) return [path]
    const differences = []
    const keys = [...new Set([...Object.keys(left), ...Object.keys(right)])].sort()
    for (const key of keys) {
      const childPath = path === '$' ? `$.${key}` : `${path}.${key}`
      if (!Object.hasOwn(left, key) || !Object.hasOwn(right, key)) differences.push(childPath)
      else differences.push(...differencePaths(left[key], right[key], childPath))
    }
    return differences
  }
  return [path]
}

function readCases(report, label, errors) {
  if (!report || !Array.isArray(report.cases)) {
    errors.push(`${label} report has no cases array`)
    return new Map()
  }
  const cases = new Map()
  for (const record of report.cases) {
    if (!record || typeof record.id !== 'string' || record.id.length === 0) {
      errors.push(`${label} report contains a case without a valid ID`)
      continue
    }
    if (cases.has(record.id)) errors.push(`${label} report contains duplicate case ID ${record.id}`)
    else cases.set(record.id, record)
  }
  return cases
}

function compareReports(baseline, current) {
  const errors = []
  const validateReport = (report, label) => {
    if (!report || typeof report !== 'object' || !Array.isArray(report.cases)) {
      errors.push(`${label} report is invalid or has no cases array`)
      return
    }
    if (!report.summary || typeof report.summary !== 'object' || !Number.isInteger(report.summary.executed) || report.summary.executed < 1) errors.push(`${label} report has no valid nonzero executed summary`)
    if (report.cases.some(record => !record || typeof record.passed !== 'boolean' || (record.skipped !== undefined && typeof record.skipped !== 'boolean'))) errors.push(`${label} report contains a case without valid verdicts`)
    for (const record of report.cases) {
      if (!Number.isInteger(record?.expectedStatus)) errors.push(`${label} case ${record?.id || '<unknown>'} has no integer expectedStatus`)
      else if (record.referenceStatus !== record.expectedStatus || record.goStatus !== record.expectedStatus) errors.push(`${label} case ${record.id} does not satisfy expectedStatus`)
    }
    if (report.summary && Array.isArray(report.cases)) {
      const passed = report.cases.filter(record => record?.passed === true).length
      const failed = report.cases.filter(record => record?.passed === false && record?.skipped !== true).length
      const skipped = report.cases.filter(record => record?.skipped === true).length
      const executed = report.cases.length - skipped
      if (report.summary.total !== report.cases.length || report.summary.passed !== passed || report.summary.failed !== failed || report.summary.skipped !== skipped || report.summary.executed !== executed) errors.push(`${label} report summary does not match its case verdicts`)
      if (failed || skipped) errors.push(`${label} report contains failed or skipped cases`)
    }
    if (report.executionError) errors.push(`${label} report has an executionError: ${report.executionError}`)
    if (typeof report.baseline !== 'string' || report.baseline.length === 0) errors.push(`${label} report has no pinned baseline source`)
  }
  validateReport(baseline, 'baseline')
  validateReport(current, 'current')
  if (baseline?.schemaVersion !== 2 || current?.schemaVersion !== 2) errors.push('report schemaVersion 2 with fixture provenance is required; regenerate the baseline from a new run')
  if (baseline?.repeatabilityPolicy !== REPEATABILITY_POLICY || current?.repeatabilityPolicy !== REPEATABILITY_POLICY) errors.push('reports do not declare the supported cross-run repeatability policy')
  if (!baseline?.fixtureProvenance || !current?.fixtureProvenance) errors.push('fixtureProvenance is required for repeatable report comparison')
  const verificationFor = (report, label) => {
    const verification = report?.verification
    if (!verification || typeof verification.inputDigest !== 'string' || verification.inputDigest.length === 0 || verification.completedInputDigest !== verification.inputDigest) {
      errors.push(`${label} report does not prove its inputs stayed unchanged during execution`)
      return null
    }
    return verification.inputDigest
  }
  const baselineDigest = verificationFor(baseline, 'baseline')
  const currentDigest = verificationFor(current, 'current')
  if (baseline?.baseline && current?.baseline && baseline.baseline !== current.baseline) errors.push('reports use different pinned behavioral sources')
  const baselineCases = readCases(baseline, 'baseline', errors)
  const currentCases = readCases(current, 'current', errors)
  const baselineIDs = new Set(baselineCases.keys())
  const currentIDs = new Set(currentCases.keys())
  const baselineOnly = [...baselineIDs].filter(id => !currentIDs.has(id)).sort()
  const currentOnly = [...currentIDs].filter(id => !baselineIDs.has(id)).sort()
  if (baselineOnly.length) errors.push(`case IDs missing from current: ${baselineOnly.join(', ')}`)
  if (currentOnly.length) errors.push(`extra case IDs in current: ${currentOnly.join(', ')}`)

  const caseDifferences = []
  for (const id of [...baselineIDs].filter(value => currentIDs.has(value)).sort()) {
    const before = baselineCases.get(id)
    const after = currentCases.get(id)
    const paths = []
    for (const key of ['passed', 'skipped', 'error', 'prerequisite', 'expectedStatus']) {
      paths.push(...differencePaths(before[key], after[key], `$.${key}`))
    }
    for (const side of ['reference', 'go']) {
      let beforeSide
      let afterSide
      try {
        beforeSide = normalizedSide(before, side, id)
        afterSide = normalizedSide(after, side, id)
      } catch (error) {
        errors.push(error.message)
        continue
      }
      paths.push(...differencePaths(beforeSide[`${side}Status`], afterSide[`${side}Status`], `$.${side}Status`))
      paths.push(...differencePaths(beforeSide[`${side}Headers`], afterSide[`${side}Headers`], `$.${side}Headers`))
      paths.push(...differencePaths(beforeSide[`${side}Body`], afterSide[`${side}Body`], `$.${side}Body`))
    }
    if (paths.length) caseDifferences.push({ id, paths: [...new Set(paths)].sort() })
  }

  const normalizationPaths = differencePaths(
    baseline?.normalizationApplied,
    current?.normalizationApplied,
    '$.normalizationApplied'
  )
  if (normalizationPaths.length) errors.push(...normalizationPaths.map(path => `normalizationApplied differs at ${path}`))
  const normalizedBaselineState = normalizeFixturePaths(normalize(baseline?.statefulChecks, 'statefulChecks', collectUUIDs(baseline?.statefulChecks)), baseline)
  const normalizedCurrentState = normalizeFixturePaths(normalize(current?.statefulChecks, 'statefulChecks', collectUUIDs(current?.statefulChecks)), current)
  const statefulPaths = differencePaths(normalizedBaselineState, normalizedCurrentState, '$.statefulChecks')
  if (statefulPaths.length) errors.push(...statefulPaths.map(path => `statefulChecks differ at ${path}`))
  const capabilityPaths = differencePaths(baseline?.capabilities, current?.capabilities, '$.capabilities')
  if (capabilityPaths.length) errors.push(...capabilityPaths.map(path => `capabilities differ at ${path}`))

  return { passed: errors.length === 0 && caseDifferences.length === 0, errors, caseDifferences, caseCount: { baseline: baselineCases.size, current: currentCases.size } }
}

function main(args) {
  if (args.length !== 2) {
    process.stderr.write('Usage: node compare-reports.cjs <baseline.json> <current.json>\n')
    return 2
  }
  try {
    const baseline = JSON.parse(fs.readFileSync(args[0], 'utf8'))
    const current = JSON.parse(fs.readFileSync(args[1], 'utf8'))
    const result = compareReports(baseline, current)
    process.stdout.write(`Repeatability policy: ${REPEATABILITY_POLICY}\n`)
    process.stdout.write(`Case counts: baseline=${result.caseCount.baseline}, current=${result.caseCount.current}\n`)
    for (const error of result.errors) process.stdout.write(`ERROR ${error}\n`)
    for (const difference of result.caseDifferences) {
      process.stdout.write(`DIFF ${difference.id}\n`)
      for (const path of difference.paths) process.stdout.write(`  ${path}\n`)
    }
    process.stdout.write(result.passed ? 'PASS reports are equivalent\n' : 'FAIL reports differ\n')
    return result.passed ? 0 : 1
  } catch (error) {
    process.stderr.write(`ERROR ${error.message}\n`)
    return 2
  }
}

if (require.main === module) process.exitCode = main(process.argv.slice(2))

module.exports = { compareReports, differencePaths, normalize, REPEATABILITY_POLICY }
