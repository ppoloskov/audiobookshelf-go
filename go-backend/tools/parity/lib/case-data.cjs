'use strict'

const fs = require('node:fs')
const path = require('node:path')

function loadCaseGroup(file, group) {
  return JSON.parse(fs.readFileSync(file, 'utf8')).filter(testCase => testCase.group === group)
}

function caseInventoryError(expectedIDs, records) {
  const expected = new Set(expectedIDs)
  const counts = new Map()
  for (const record of records) counts.set(record.id, (counts.get(record.id) || 0) + 1)
  const missing = [...expected].filter(id => !counts.has(id)).sort()
  const duplicates = [...counts].filter(([, count]) => count > 1).map(([id]) => id).sort()
  const extra = [...counts.keys()].filter(id => !expected.has(id)).sort()
  if (!missing.length && !duplicates.length && !extra.length) return null
  return `case inventory mismatch${missing.length ? `; missing: ${missing.join(', ')}` : ''}${duplicates.length ? `; duplicate: ${duplicates.join(', ')}` : ''}${extra.length ? `; unexpected: ${extra.join(', ')}` : ''}`
}

function resolveTemplate(value, context) {
  if (Array.isArray(value)) return value.map(item => resolveTemplate(item, context))
  if (value && typeof value === 'object') {
    if (value.$replace) {
      const source = resolveTemplate({ $ref: value.$replace.ref }, context)
      return source.replace(value.$replace.from, value.$replace.to)
    }
    if (value.$repeat) return value.$repeat.value.repeat(value.$repeat.count)
    if (typeof value.$ref === 'string') {
      if (!Object.hasOwn(context, value.$ref)) throw new Error(`case reference is missing: ${value.$ref}`)
      return context[value.$ref]
    }
    return Object.fromEntries(Object.entries(value).map(([key, item]) => [key, resolveTemplate(item, context)]))
  }
  if (typeof value !== 'string') return value
  return value.replace(/\{\{([A-Za-z_$][A-Za-z0-9_$.]*)\}\}/g, (_match, name) => {
    if (!Object.hasOwn(context, name)) throw new Error(`case template value is missing: ${name}`)
    return String(context[name])
  })
}

async function runStoredCase(id, context, cases, comparePair) {
  const testCase = cases.get(id)
  if (!testCase) throw new Error(`inline case data is missing: ${id}`)
  return comparePair(
    id,
    resolveTemplate(testCase.route, context),
    resolveTemplate(testCase.goRoute ?? testCase.route, context),
    resolveTemplate(testCase.options || {}, context)
  )
}

async function executeCaseGroup({ cases, group, context, selectedRun, requestedCases, comparePair }) {
  for (const testCase of loadCaseGroup(cases, group)) {
    if (selectedRun && !requestedCases.has(testCase.id)) continue
    const route = resolveTemplate(testCase.route, context)
    const goRoute = resolveTemplate(testCase.goRoute ?? testCase.route, context)
    const options = resolveTemplate(testCase.options || {}, context)
    if (testCase.method) options.method = testCase.method
    if (testCase.body !== undefined) options.body = resolveTemplate(testCase.body, context)
    if (testCase.auth === 'none') options.anonymous = true
    else if (testCase.auth) options.token = context[`${testCase.auth}Token`]
    if (testCase.notes) options.notes = testCase.notes
    await comparePair(testCase.id, route, goRoute, options)
  }
}

async function executeNamedSteps({ steps, stepIDs, selectedRun, requestedCases, context, runtime, comparePair }) {
  const definitions = JSON.parse(fs.readFileSync(steps, 'utf8'))
  const results = []
  for (const step of definitions) {
    if (stepIDs && !stepIDs.includes(step.id)) continue
    if (selectedRun && !step.cases.some(id => requestedCases.has(id))) continue
    const run = require(path.resolve(path.dirname(steps), step.module))
    results.push(await run({
      caseFile: step.caseFile ? path.resolve(path.dirname(steps), step.caseFile) : undefined,
      group: step.group,
      context,
      runtime,
      selectedRun,
      requestedCases,
      comparePair
    }))
  }
  return results
}

module.exports = { loadCaseGroup, resolveTemplate, executeCaseGroup, executeNamedSteps, runStoredCase, caseInventoryError }
