// go-overfit-audit.test.mjs — self-test for go-overfit-audit.mjs on a tiny
// fixture corpus in a temp directory: an install with ids.json + media names,
// a Go corpus with a deliberate overfitting hit, a test file that must be
// skipped, and an allow-listed upstream-defined hit.
//
// Run: node --test go-overfit-audit.test.mjs

import assert from 'node:assert/strict'
import fs from 'node:fs'
import os from 'node:os'
import path from 'node:path'
import test from 'node:test'

import { audit, fixtureIdLiterals, fixtureLiterals, scanCorpus } from './go-overfit-audit.mjs'

// Builds a temp install with ids.json (uuid values become fixture literals)
// and a media tree, plus a Go corpus. Returns { root, dirs, install }.
function makeCorpus() {
  const root = fs.mkdtempSync(path.join(os.tmpdir(), 'overfit-audit-test-'))
  const install = path.join(root, 'install')
  const go = path.join(root, 'go')
  fs.mkdirSync(path.join(install, 'media', 'audiobooks', 'Ada Example'), { recursive: true })
  fs.mkdirSync(path.join(go, 'internal', 'x'), { recursive: true })
  fs.writeFileSync(
    path.join(install, 'ids.json'),
    JSON.stringify({ library: '51bf3bdf-f246-4f7d-b0a6-9059e4d0ed41', saga1: 'b3c61dfc-dd4b-47ee-8998-0340c1228f52' })
  )
  fs.writeFileSync(path.join(install, 'media', 'audiobooks', 'Ada Example', 'Single File Novel.m4b'), 'x')
  fs.writeFileSync(
    path.join(go, 'internal', 'x', 'overfit.go'),
    `package x

// overfitting: the fixture title and a seeded id appear in application code
func coverPath(id string) string {
	if id == "b3c61dfc-dd4b-47ee-8998-0340c1228f52" {
		return "Single File Novel.m4b"
	}
	return defaultCover
}
`
  )
  fs.writeFileSync(
    path.join(go, 'internal', 'x', 'overfit_test.go'),
    `package x

// must be skipped: fixture literals are expected in test code
func testCover() string { return "Single File Novel.m4b" }
`
  )
  fs.writeFileSync(
    path.join(go, 'internal', 'x', 'upstream.go'),
    `package y

// upstream-defined companion file, adjudicated via the allow list
func isCompanion(name string) bool { return name == "desc.txt" || name == "reader.txt" }
`
  )
  return { root, dirs: [go], install }
}

test('fixtureIdLiterals reads the seeded uuid values from ids.json', () => {
  const { install } = makeCorpus()
  try {
    const ids = fixtureIdLiterals(install)
    assert.deepEqual(ids.sort(), [
      '51bf3bdf-f246-4f7d-b0a6-9059e4d0ed41',
      'b3c61dfc-dd4b-47ee-8998-0340c1228f52',
    ])
  } finally {
    fs.rmSync(install, { recursive: true, force: true })
  }
})

test('fixtureIdLiterals returns [] when the install is absent', () => {
  const absent = path.join(os.tmpdir(), 'overfit-audit-test-absent-install')
  assert.deepEqual(fixtureIdLiterals(absent), [])
})

test('fixtureLiterals includes the curated titles, users and passwords', () => {
  const { install } = makeCorpus()
  try {
    const literals = fixtureLiterals(install)
    for (const expected of [
      'Single File Novel',
      'trial-listener-password',
      'Single File Novel.m4b',
      'reader.txt',
      'desc.txt',
      'saga1',
      '51bf3bdf-f246-4f7d-b0a6-9059e4d0ed41',
    ]) {
      assert.ok(literals.includes(expected), `missing fixture literal ${expected}`)
    }
  } finally {
    fs.rmSync(install, { recursive: true, force: true })
  }
})

test('scanCorpus finds literal hits with file and line, skipping _test.go', () => {
  const { root, dirs, install } = makeCorpus()
  try {
    const literals = fixtureLiterals(install)
    const { hits, filesScanned } = scanCorpus(dirs, literals, root)
    assert.equal(filesScanned, 2) // overfit.go and upstream.go; overfit_test.go skipped
    const seeded = hits.find((h) => h.literal === 'b3c61dfc-dd4b-47ee-8998-0340c1228f52')
    assert.ok(seeded, 'seeded id not found')
    assert.equal(seeded.file, path.join('go', 'internal', 'x', 'overfit.go'))
    assert.equal(seeded.line, 5)
    const title = hits.find((h) => h.literal === 'Single File Novel.m4b')
    assert.ok(title, 'fixture title not found')
    assert.equal(title.file, path.join('go', 'internal', 'x', 'overfit.go'))
    // the test file mention must not be scanned
    assert.ok(!hits.some((h) => h.file.endsWith('_test.go')))
  } finally {
    fs.rmSync(root, { recursive: true, force: true })
  }
})

test('audit reports un-allow-listed hits and exits-clean state comes from the allow list', () => {
  const { root, dirs, install } = makeCorpus()
  try {
    const overfit = audit({ dirs, installDir: install, allowList: [], goBackend: root })
    // seeded id, "Single File Novel" and "Single File Novel.m4b" (substring
    // overlap counts both), desc.txt and reader.txt
    assert.equal(overfit.report.counts.unallowlisted_findings, 5)
    assert.ok(overfit.findings.some((h) => h.literal === 'Single File Novel.m4b'))

    const adjudicated = audit({
      dirs,
      installDir: install,
      goBackend: root,
      allowList: [
        { literal: 'desc.txt', file: path.join('go', 'internal', 'x', 'upstream.go'), reason: 'upstream-defined' },
        { literal: 'reader.txt', file: path.join('go', 'internal', 'x', 'upstream.go'), reason: 'upstream-defined' },
      ],
    })
    // the upstream-defined hits drop out; the fixture title and seeded id remain
    assert.equal(adjudicated.report.counts.hits, 5)
    assert.equal(adjudicated.report.counts.allow_listed, 2)
    assert.equal(adjudicated.report.counts.unallowlisted_findings, 3)
    assert.ok(adjudicated.findings.every((h) => h.literal !== 'desc.txt' && h.literal !== 'reader.txt'))
    // allow-list matching is per file: the same literal elsewhere still counts
    const otherFile = audit({
      dirs,
      installDir: install,
      goBackend: root,
      allowList: [{ literal: 'desc.txt', file: 'somewhere/else.go', reason: 'not this file' }],
    })
    assert.ok(otherFile.findings.some((h) => h.literal === 'desc.txt'))
  } finally {
    fs.rmSync(root, { recursive: true, force: true })
  }
})

test('audit on the real repo scans the full internal/cmd tree', () => {
  // Guards against a silent regression to an empty scan. The committed
  // report holds the adjudications, so this must stay findings-free; if it
  // is not, the CLI run below this test file would also fail.
  const result = audit()
  assert.ok(result.report.counts.go_files_scanned > 200, 'expected the real corpus')
  assert.ok(result.report.counts.fixture_literals > 40, 'expected the full literal list')
  assert.equal(result.report.counts.unallowlisted_findings, 0)
})
