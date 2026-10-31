'use strict'

const assert = require('node:assert/strict')
const fs = require('node:fs')
const os = require('node:os')
const path = require('node:path')
const { spawnSync } = require('node:child_process')
const test = require('node:test')
const { REPEATABILITY_POLICY } = require('./compare-reports.cjs')

const tool = path.join(__dirname, 'compare-reports.cjs')
const uuidA = '11111111-1111-4111-8111-111111111111'
const uuidB = '22222222-2222-4222-8222-222222222222'
const uuidC = '33333333-3333-4333-8333-333333333333'

function sampleReport(body = { id: uuidA, ownerId: uuidA, title: 'Example' }) {
  return {
    schemaVersion: 2,
    repeatabilityPolicy: REPEATABILITY_POLICY,
    baseline: 'edb1f7efc92ecacdef51aafb4d9d5054ce10604f',
    fixtureProvenance: { nodeMediaRoot: '/fixtures/a/media', goMediaRoot: '/fixtures/b/media', nodeMetadataRoot: '/fixtures/a/metadata', goMetadataRoot: '/fixtures/b/metadata' },
    verification: { inputDigest: 'same-source', completedInputDigest: 'same-source' },
    summary: { total: 1, executed: 1, passed: 1, failed: 0, skipped: 0 },
    normalizationApplied: { identifiers: ['UUIDs in bodies and paths'] },
    statefulChecks: { sample: { equal: true } },
    capabilities: { ffmpeg: { webp: true } },
    cases: [{
      id: 'sample', passed: true, skipped: false, expectedStatus: 200,
      referenceStatus: 200, goStatus: 200,
      referenceHeaders: { 'content-type': 'application/json', location: `/items/${body.id}` },
      goHeaders: { 'content-type': 'application/json', location: `/items/${body.id}` },
      referenceBody: body, goBody: body
    }]
  }
}

function run(baseline, current) {
  const directory = fs.mkdtempSync(path.join(os.tmpdir(), 'compare-reports-test-'))
  try {
    const before = path.join(directory, 'baseline.json')
    const after = path.join(directory, 'current.json')
    fs.writeFileSync(before, JSON.stringify(baseline))
    fs.writeFileSync(after, JSON.stringify(current))
    return spawnSync(process.execPath, [tool, before, after], { encoding: 'utf8' })
  } finally {
    fs.rmSync(directory, { recursive: true, force: true })
  }
}

test('identical reports pass', () => {
  const report = sampleReport()
  const result = run(report, structuredClone(report))
  assert.equal(result.status, 0, result.stdout + result.stderr)
  assert.match(result.stdout, /PASS reports are equivalent/)
})

test('a changed non-volatile body field fails', () => {
  const result = run(sampleReport(), sampleReport({ id: uuidA, ownerId: uuidA, title: 'Changed' }))
  assert.equal(result.status, 1)
  assert.match(result.stdout, /\$\.referenceBody\.title/)
  assert.match(result.stdout, /\$\.goBody\.title/)
})

test('a consistently changed UUID passes', () => {
  const result = run(sampleReport(), sampleReport({ id: uuidB, ownerId: uuidB, title: 'Example' }))
  assert.equal(result.status, 0, result.stdout + result.stderr)
})

test('volatile timestamps and ports pass while ordinary fields remain comparable', () => {
  const before = sampleReport({ id: uuidA, ownerId: uuidA, title: 'Example', updatedAt: 1793361611008, lastSeen: '2026-10-30', port: 43001 })
  const after = sampleReport({ id: uuidB, ownerId: uuidB, title: 'Example', updatedAt: 1793395811675, lastSeen: '2026-10-31', port: 51002 })
  const result = run(before, after)
  assert.equal(result.status, 0, result.stdout + result.stderr)
})

test('SQL DATETIME values in state checks normalize while values stay typed and parseable', () => {
  const before = sampleReport()
  before.statefulChecks.sample.changedUpdatedAt = '2026-10-30 12:01:01.285 +00:00'
  const after = sampleReport()
  after.statefulChecks.sample.changedUpdatedAt = '2026-10-31 09:01:01.285 +00:00'
  assert.equal(run(before, after).status, 0)
})

test('a UUID change that breaks an identity relationship fails', () => {
  const result = run(sampleReport(), sampleReport({ id: uuidB, ownerId: uuidC, title: 'Example' }))
  assert.equal(result.status, 1)
  assert.match(result.stdout, /\$\.referenceBody\.ownerId/)
})

test('a missing case ID fails', () => {
  const current = sampleReport()
  current.cases = []
  const result = run(sampleReport(), current)
  assert.equal(result.status, 1)
  assert.match(result.stdout, /case IDs missing from current: sample/)
})

test('an extra case ID fails', () => {
  const current = sampleReport()
  current.cases.push({ ...current.cases[0], id: 'unexpected' })
  const result = run(sampleReport(), current)
  assert.equal(result.status, 1)
  assert.match(result.stdout, /extra case IDs in current: unexpected/)
})

test('status, headers, and normalization declarations are checked', () => {
  const before = sampleReport()
  const after = structuredClone(before)
  after.cases[0].referenceStatus = 201
  after.cases[0].goHeaders['content-type'] = 'text/plain'
  after.normalizationApplied.identifiers = []
  const result = run(before, after)
  assert.equal(result.status, 1)
  assert.match(result.stdout, /\$\.referenceStatus/)
  assert.match(result.stdout, /\$\.goHeaders\.content-type/)
  assert.match(result.stdout, /normalizationApplied differs/)
})

test('case verdict, error and authoritative stateful checks are compared', () => {
  for (const mutate of [
    report => { report.cases[0].passed = false },
    report => { report.cases[0].skipped = true },
    report => { report.cases[0].error = 'execution failed' },
    report => { report.statefulChecks.sample.equal = false },
    report => { report.capabilities.ffmpeg.webp = false }
  ]) {
    const current = sampleReport()
    mutate(current)
    assert.equal(run(sampleReport(), current).status, 1)
  }
})

test('reports with zero executed cases are rejected', () => {
  const current = sampleReport()
  current.summary.executed = 0
  assert.equal(run(sampleReport(), current).status, 1)
})

test('different verified source inputs can be compared when behavior is unchanged', () => {
  const baseline = sampleReport()
  const current = structuredClone(baseline)
  current.verification = { inputDigest: 'new-source', completedInputDigest: 'new-source' }
  assert.equal(run(baseline, current).status, 0)
})

test('report execution errors, baseline source pins, and every summary count are enforced', () => {
  for (const mutate of [
    report => { report.executionError = 'missing case' },
    report => { report.baseline = 'another-upstream-commit' },
    report => { report.summary.total = 2 },
    report => { report.summary.failed = 1 }
  ]) {
    const current = sampleReport()
    mutate(current)
    assert.notEqual(run(sampleReport(), current).status, 0)
  }
})

test('expected status changes fail even when both reports otherwise agree', () => {
  const current = sampleReport()
  current.cases[0].expectedStatus = 201
  assert.notEqual(run(sampleReport(), current).status, 0)
})

test('fixture provenance makes stateful fixture paths repeatable', () => {
  const before = sampleReport()
  before.statefulChecks.cover = { path: '/fixtures/a/media/book/cover.jpg' }
  const after = sampleReport()
  after.fixtureProvenance = { nodeMediaRoot: '/fresh/c/media', goMediaRoot: '/fresh/d/media', nodeMetadataRoot: '/fresh/c/metadata', goMetadataRoot: '/fresh/d/metadata' }
  after.statefulChecks.cover = { path: '/fresh/c/media/book/cover.jpg' }
  assert.equal(run(before, after).status, 0)
})

test('invalid normalized HTTP dates, ETags, dates, and weekdays fail closed', () => {
  const fileBase = sampleReport()
  fileBase.cases[0].id = 'direct-file-range'
  fileBase.cases[0].referenceHeaders.etag = 'W/"640-1a18cae84e1"'
  fileBase.cases[0].goHeaders.etag = 'W/"640-1a18cae84e1"'
  fileBase.cases[0].referenceHeaders['last-modified'] = 'Fri, 30 Oct 2026 12:00:09 GMT'
  fileBase.cases[0].goHeaders['last-modified'] = 'Fri, 30 Oct 2026 12:00:09 GMT'
  for (const key of ['etag', 'last-modified']) {
    const candidate = structuredClone(fileBase)
    candidate.cases[0].referenceHeaders[key] = key === 'etag' ? 'not-an-etag' : 'yesterday'
    assert.notEqual(run(fileBase, candidate).status, 0)
  }
  const playbackBase = sampleReport({ date: '2026-10-30', dayOfWeek: 'Friday' })
  playbackBase.cases[0].id = 'playback-session'
  for (const body of [{ date: '2026-02-30', dayOfWeek: 'Monday' }, { date: '2026-10-30', dayOfWeek: 'Monday' }]) {
    const candidate = structuredClone(playbackBase)
    candidate.cases[0].referenceBody = body
    assert.notEqual(run(playbackBase, candidate).status, 0)
  }
})
