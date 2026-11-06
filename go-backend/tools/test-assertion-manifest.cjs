#!/usr/bin/env node
'use strict'

const assert = require('node:assert/strict')
const crypto = require('node:crypto')
const fs = require('node:fs')
const path = require('node:path')
const test = require('node:test')

const root = path.resolve(__dirname, '..')
const manifest = JSON.parse(fs.readFileSync(path.join(root, 'contracts/test-assertions.json'), 'utf8'))
const reconciliation = JSON.parse(fs.readFileSync(path.join(root, manifest.externalReconciliation.path), 'utf8'))
const hashPattern = /^[a-f0-9]{64}$/

function fileText(relative) {
  return fs.readFileSync(path.join(root, relative), 'utf8')
}

function digest(relative) {
  return crypto.createHash('sha256').update(fs.readFileSync(path.join(root, relative))).digest('hex')
}

function targetExists(reference, lane) {
  const split = reference.indexOf('#')
  assert.notEqual(split, -1, `target must include file#symbol: ${reference}`)
  const file = reference.slice(0, split)
  const symbol = reference.slice(split + 1)
  const source = fileText(file)
  if (file.endsWith('_test.go')) {
    assert.match(source, new RegExp(`\\bfunc\\s+${symbol}\\s*\\(`), `missing Go target ${reference}`)
    const tagged = source.split(/\r?\n/).slice(0, 2).includes('//go:build tsdiagnostic')
    assert.equal(tagged, lane === 'diagnostic', `Go test lane mismatch for ${reference}`)
  } else if (symbol.startsWith('Test')) {
    assert.ok(source.includes(symbol), `missing Node test target ${reference}`)
    assert.match(source, /\btest\s*\(/, `no node:test registration in ${file}`)
    if (lane === 'diagnostic') assert.equal(file, 'tools/parity/test-representation-diagnostic.cjs')
  } else {
    assert.ok(source.includes(symbol), `missing source symbol ${reference}`)
    if (lane === 'diagnostic' && file.endsWith('.cjs')) {
      assert.ok(file.includes('representation-diagnostic') || file === 'tools/parity/differential.cjs', `unexpected Node diagnostic target ${reference}`)
    }
  }
}

test('manifest shape, IDs, origin references, target symbols, and lanes', () => {
  assert.equal(manifest.schemaVersion, 1)
  assert.equal(manifest.baseCommit, 'fda317d123885d7a8f2accc20ca65d3cdc8210d8')
  assert.deepEqual(manifest.scope.families, ['R01', 'R02', 'R03', 'R04', 'R05', 'R06', 'R07', 'R08'])
  assert.equal(manifest.scope.candidateReferenceCount, 20)
  assert.equal(manifest.scope.uniqueCandidateLabels, 19)
  assert.match(manifest.scope.coverageBoundary, /does not claim all repository assertions/)
  assert.equal(manifest.candidateReferences.length, manifest.scope.candidateReferenceCount)
  assert.equal(new Set(manifest.candidateReferences.map((ref) => ref.label)).size, manifest.scope.uniqueCandidateLabels)
  assert.equal(new Set(manifest.candidateReferences.filter((ref) => ref.kind === 'goTest').map((ref) => ref.label)).size, 17)
  assert.ok(manifest.candidateReferences.filter((ref) => ref.family === 'R08').every((ref) => ref.kind === 'parityCaseId'))
  for (const [family, count] of Object.entries({ R01: 7, R02: 1, R03: 1, R04: 3, R05: 3, R06: 2, R07: 1, R08: 2 })) {
    assert.equal(manifest.candidateReferences.filter((ref) => ref.family === family).length, count, `${family} reference count`)
  }

  const originFiles = new Map()
  for (const origin of manifest.originFiles) {
    assert.ok(!originFiles.has(origin.path), `duplicate origin file ${origin.path}`)
    fileText(origin.path)
    assert.match(origin.sha256, hashPattern, `invalid base hash for ${origin.path}`)
    assert.ok(origin.symbols.length > 0, `origin file has no symbols: ${origin.path}`)
    origin.symbols.forEach((symbol) => assert.ok(symbol))
    originFiles.set(origin.path, new Set(origin.symbols))
  }

  const ids = new Set()
  for (const assertion of manifest.originalAssertions) {
    assert.ok(!ids.has(assertion.id), `duplicate assertion ID ${assertion.id}`)
    ids.add(assertion.id)
    assert.ok(manifest.scope.families.includes(assertion.family), `unknown family ${assertion.family}`)
    assert.ok(['retained', 'replaced', 'diagnostic'].includes(assertion.disposition), `invalid status ${assertion.disposition}`)
    assert.ok(Number.isInteger(assertion.assertionCount) && assertion.assertionCount > 0, `missing count: ${assertion.id}`)
    assert.ok(assertion.countUnit && assertion.expectation, `missing count unit/expectation: ${assertion.id}`)
    assert.ok(originFiles.get(assertion.originFile)?.has(assertion.originSymbol), `unknown original symbol ${assertion.originFile}#${assertion.originSymbol}`)
    for (const target of assertion.replacementTargets) targetExists(target, 'default')
    for (const target of assertion.diagnosticTargets) targetExists(target, 'diagnostic')
  }
  for (const family of manifest.scope.families) assert.ok(manifest.originalAssertions.some((row) => row.family === family), `missing family ${family}`)
  for (const control of manifest.retainedControls) {
    assert.ok(!ids.has(control.id), `duplicate retained-control ID ${control.id}`)
    ids.add(control.id)
    assert.equal(control.disposition, 'retained', `invalid retained-control status ${control.id}`)
    assert.ok(Number.isInteger(control.assertionCount) && control.assertionCount > 0 && control.countUnit && control.expectation)
    fileText(control.path)
    if (control.symbol) targetExists(`${control.path}#${control.symbol}`, 'default')
  }
  assert.ok(!fileText('tools/parity/test-cover-patch-contract.cjs').includes('opt-in diagnostic retains the old exact Express JSON fingerprint check'), 'diagnostic fingerprint test leaked into default Node lane')
  assert.ok(!fileText('tools/parity/test-cover-patch-contract.cjs').includes('test-representation-diagnostic.cjs'), 'Node diagnostic test imported by default test lane')
  assert.equal(reconciliation.summary.candidate_reference_count, manifest.scope.candidateReferenceCount)
  assert.equal(reconciliation.summary.unique_candidate_labels, manifest.scope.uniqueCandidateLabels)
  assert.deepEqual([reconciliation.localVerification.inventories.goTests.count, reconciliation.localVerification.inventories.journeys.count, reconciliation.localVerification.inventories.pinnedRoutes.count], [manifest.externalReconciliation.inventoryCounts.goTests, manifest.externalReconciliation.inventoryCounts.journeySteps, manifest.externalReconciliation.inventoryCounts.pinnedRoutes])
  assert.deepEqual([reconciliation.localVerification.externalFilesModified, reconciliation.localVerification.assessment.scoreChanged, reconciliation.localVerification.assessment.scoringRun], [false, false, false])
})
test('retained source provenance hashes and sanitizer corpus span', () => {
  for (const source of manifest.sourceProvenance) {
    assert.match(source.sha256, hashPattern, `invalid source hash ${source.path}`)
    assert.equal(digest(source.path), source.sha256, `source provenance changed: ${source.path}`)
  }
  for (const span of manifest.retainedCorpusSpans) {
    const source = fileText(span.path)
    const start = source.indexOf(span.startMarker)
    const end = source.indexOf(span.endMarker, start)
    assert.ok(start >= 0 && end > start, `missing corpus markers in ${span.path}`)
    const originalSpan = source.slice(start, end)
    assert.equal(crypto.createHash('sha256').update(originalSpan).digest('hex'), span.baseSpanSha256)
    const rows = originalSpan.split(/\r?\n/).filter((line) => /^\s*\{"/.test(line)).length
    assert.equal(rows, span.rowCount)
    assert.equal(span.unchangedRows + span.representationRowsReplaced, span.rowCount)
  }
})

test('optional Monstrobook inventory snapshot hashes, counts, and indexed references', { skip: !process.env.MONSTROBOOK_ROOT }, () => {
  const externalRoot = path.resolve(process.env.MONSTROBOOK_ROOT)
  const inventories = new Map()
  for (const item of manifest.externalReconciliation.inventoryFiles) {
    const file = path.join(externalRoot, item.path)
    const bytes = fs.readFileSync(file)
    assert.equal(crypto.createHash('sha256').update(bytes).digest('hex'), item.sha256, `external hash changed: ${item.path}`)
    inventories.set(path.basename(item.path), JSON.parse(bytes.toString('utf8')))
  }
  const go = inventories.get('go-test-inventory.json')
  const journeys = inventories.get('journey-inventory.json')
  const routes = inventories.get('pinned-route-inventory.json')
  const imported = inventories.get('import-manifest.json')
  const assessment = inventories.get('assessment.json')
  assert.equal(go.length, manifest.externalReconciliation.inventoryCounts.goTests)
  for (const [got, want] of [[journeys.steps.length, 491], [journeys.total, 491], [routes.length, 209], [imported.revision, manifest.externalReconciliation.expectedRevision], [imported.files.length, 115], [assessment.sourceRevision, manifest.externalReconciliation.expectedRevision], [assessment.catalogTotal, 193]]) assert.equal(got, want)
  const candidates = new Set(manifest.candidateReferences.filter((ref) => ref.kind === 'goTest').map((ref) => ref.label))
  assert.equal(go.filter((entry) => candidates.has(entry.test)).length, 0)
  for (const family of reconciliation.reconciliation) {
    for (const ref of family.represented_evidence.go_contract_tests) assert.deepEqual(go[ref.index], { file: ref.file, test: ref.test })
    for (const ref of family.represented_evidence.journey_steps) assert.deepEqual([journeys.steps[ref.index].id, journeys.steps[ref.index].method, journeys.steps[ref.index].route], [ref.id, ref.method, ref.route])
    for (const ref of family.represented_evidence.pinned_routes) assert.deepEqual([routes[ref.index].method, routes[ref.index].route, routes[ref.index].source], [ref.method, ref.route, ref.source])
  }
})
