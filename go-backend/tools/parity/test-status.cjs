'use strict'
const assert = require('node:assert/strict')
const test = require('node:test')
const { snapshot, render } = require('./status.cjs')

function report() {
  return {
    baseline: 'pinned-upstream', generatedAt: '2026-10-31T00:00:00Z',
    verification: { inputDigest: 'source-a', completedInputDigest: 'source-a' },
    summary: { total: 1, executed: 1, passed: 1, failed: 0, skipped: 0 },
    cases: [{ id: 'workflow', passed: true, referenceStatus: 200, goStatus: 200 }],
    statefulChecks: { coverDelete: { equal: true }, coverUpload: { equal: true }, coverPatch: { equal: true }, emailDevices: { equal: true }, coverURLFailure: { equal: true }, coverURLRedirect: { equalFailureState: true } },
    gate: { equivalencePassed: true, error: null }
  }
}

test('status reflects recorded cases and identifies changed source', () => {
  const record = snapshot(report(), 'recorded-report.json')
  const current = render(record, 'source-a')
  assert.match(current, /PASS/)
  assert.match(current, /\| Executed \| 1 \|/)
  assert.match(render(record, 'source-b'), /STALE/)
  assert.doesNotMatch(render(record, 'source-b'), /Current result: PASS/)
})

test('failed state checks cannot produce passing status', () => {
  const input = report()
  input.statefulChecks.coverPatch.equal = false
  assert.match(render(snapshot(input, 'report.json'), 'source-a'), /FAIL/)
})

test('missing state checks and failed equivalence cannot produce passing status', () => {
  const missing = report(); delete missing.statefulChecks.emailDevices
  assert.match(render(snapshot(missing, 'report.json'), 'source-a'), /FAIL/)
  const failed = report(); failed.gate = { equivalencePassed: false, error: 'reports differ' }
  assert.match(render(snapshot(failed, 'report.json'), 'source-a'), /FAIL/)
  const unverified = report(); delete unverified.gate
  assert.match(render(snapshot(unverified, 'report.json'), 'source-a'), /FAIL/)
})

test('zero, duplicate, inconsistent and unproven reports are rejected', () => {
  const empty = report(); empty.cases = []
  assert.throws(() => snapshot(empty, 'report.json'), /cases/)
  const duplicate = report(); duplicate.cases.push(duplicate.cases[0])
  assert.throws(() => snapshot(duplicate, 'report.json'), /duplicate/)
  const inconsistent = report(); inconsistent.summary.passed = 20
  assert.throws(() => snapshot(inconsistent, 'report.json'), /summary/)
  const unproven = report(); delete unproven.verification
  assert.throws(() => snapshot(unproven, 'report.json'), /provenance/)
  const changed = report(); changed.verification.completedInputDigest = 'source-b'
  assert.throws(() => snapshot(changed, 'report.json'), /changed/)
})

test('saved status omits response bodies and credentials', () => {
  const input = report(); input.cases[0].referenceBody = { token: 'do-not-copy' }
  const serialized = JSON.stringify(snapshot(input, 'report.json'))
  assert.doesNotMatch(serialized, /do-not-copy|referenceBody/)
})
