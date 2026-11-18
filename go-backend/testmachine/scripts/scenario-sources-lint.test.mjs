import assert from 'node:assert/strict'
import fs from 'node:fs'
import os from 'node:os'
import path from 'node:path'
import test from 'node:test'
import { classifyCitation, lintScenarioSources, runCli, summaryLine, tokenizeGo } from './scenario-sources-lint.mjs'

// A synthetic pinned tree plus scenario packages. The lint is pointed at it
// with --root/--scenarios, so nothing outside the temp directory is read.
const UPSTREAM = [
  ['server/controllers/UserController.js', 'line 1\nline 2\nline 3\nline 4\nline 5\n'],
  ['server/utils/fileUtils.js', 'a\nb\nc\nd\ne\nf\n'],
  ['client/components/App.vue', '<template>\n  <div />\n</template>\n'],
  ['index.js', 'require("./server")\nconst x = 1\n'],
  ['go-backend/testmachine/contracts/clients/official-app.md', '# contract\nclient notes\n']
]

function fixture(t) {
  const root = fs.mkdtempSync(path.join(os.tmpdir(), 'abs-scenario-sources-'))
  t.after(() => fs.rmSync(root, { recursive: true, force: true }))
  const write = (relative, source) => {
    const file = path.join(root, relative)
    fs.mkdirSync(path.dirname(file), { recursive: true })
    fs.writeFileSync(file, source)
  }
  for (const [relative, source] of UPSTREAM) write(relative, source)
  const scenarios = path.join(root, 'go-backend/testmachine/scenarios')
  return { root, scenarios, write, lint: (extra = {}) => lintScenarioSources({ root, scenariosDir: scenarios, ...extra }) }
}

const scenarioFile = (sources, extra = '') => `package demo

import "audiobookshelf-compat/testmachine/scenarios/harness"

var _ = harness.Scenario{}

${extra}
func scenarios() []harness.Scenario {
	return []harness.Scenario{
		{Sources: []string{${sources}}, Claim: "x"},
	}
}
`

test('accepts a single line, a range, a whole file, index.js and a contract document', (t) => {
  const { write, lint } = fixture(t)
  write(
    'go-backend/testmachine/scenarios/demo/good.go',
    scenarioFile(
      '"server/controllers/UserController.js:2", "server/controllers/UserController.js:2-4", ' +
        '"server/controllers/UserController.js", "index.js:1", ' +
        '"go-backend/testmachine/contracts/clients/official-app.md:99"'
    )
  )
  const report = lint()
  assert.deepEqual(report.findings, [])
  assert.equal(report.filesScanned, 1)
  assert.equal(report.packetCount, 1)
  assert.equal(report.citationsChecked, 5)
  assert.equal(report.citationsDistinct, 5)
  assert.equal(report.ok, true)
})

test('reports a missing file', (t) => {
  const { write, lint } = fixture(t)
  write('go-backend/testmachine/scenarios/demo/missing.go', scenarioFile('"server/controllers/Gone.js:1"'))
  const report = lint()
  assert.equal(report.findings.length, 1)
  assert.equal(report.findings[0].kind, 'missing-file')
  assert.equal(report.findings[0].line, 10)
  assert.deepEqual(report.findingsByKind, { 'missing-file': 1 })
})

test('reports a range that overruns the file', (t) => {
  const { write, lint } = fixture(t)
  write('go-backend/testmachine/scenarios/demo/overrun.go', scenarioFile('"server/controllers/UserController.js:4-9"'))
  const [finding] = lint().findings
  assert.equal(finding.kind, 'line-out-of-range')
  assert.match(finding.reason, /overruns end of file \(server\/controllers\/UserController\.js has 5 lines\)/)
})

test('reports a range that ends before it starts', (t) => {
  const { write, lint } = fixture(t)
  write('go-backend/testmachine/scenarios/demo/reversed.go', scenarioFile('"server/controllers/UserController.js:4-2"'))
  const [finding] = lint().findings
  assert.equal(finding.kind, 'reversed-range')
  assert.match(finding.reason, /range 4-2 ends before it starts/)
})

test('reports an unknown citation form instead of crashing', (t) => {
  const { write, lint } = fixture(t)
  write(
    'go-backend/testmachine/scenarios/demo/unknown.go',
    scenarioFile('"server/controllers/UserController", "see the controller", "src/thing.js:1"')
  )
  const report = lint()
  assert.equal(report.findings.length, 3)
  assert.deepEqual(report.findings.map((finding) => finding.kind), ['unknown-form', 'unknown-form', 'unknown-form'])
  assert.equal(report.citationsChecked, 0)
})

test('reports a path whose case differs from the file on disk', (t) => {
  const { write, lint } = fixture(t)
  write('go-backend/testmachine/scenarios/demo/case.go', scenarioFile('"server/Controllers/UserController.js:1"'))
  const [finding] = lint().findings
  assert.equal(finding.kind, 'missing-file')
  assert.match(finding.reason, /the path segment Controllers is controllers on disk/)
})

test('resolves client citations, the ../client spelling and several ranges', (t) => {
  const { write, lint } = fixture(t)
  write(
    'go-backend/testmachine/scenarios/demo/client.go',
    scenarioFile(
      '"client/components/App.vue:2", "../client/components/App.vue:1-3", ' +
        '"server/utils/fileUtils.js:1-2,4,6"'
    )
  )
  const report = lint()
  assert.deepEqual(report.findings, [])
  assert.equal(report.citationsChecked, 3)
})

test('follows string constants, concatenation and append into Sources', (t) => {
  const { write, lint } = fixture(t)
  write(
    'go-backend/testmachine/scenarios/demo/consts.go',
    `package demo

import "audiobookshelf-compat/testmachine/scenarios/harness"

const controller = "server/controllers/UserController.js"
const single = controller + ":2"

var shared = []string{"server/utils/fileUtils.js:1-2"}

func scenarios() []harness.Scenario {
	return []harness.Scenario{
		{Sources: []string{shared[0], single}, Claim: "x"},
		{Sources: append([]string{controller + ":3"}, shared...), Claim: "y"},
	}
}
`
  )
  const report = lint()
  // `shared[0]` is a malformed index expression, not a citation: never a finding.
  assert.deepEqual(report.findings, [])
  assert.equal(report.citationsChecked, 3)
  assert.deepEqual(
    [...new Set(report.dynamicSources.map((item) => item.name))].sort(),
    ['shared']
  )
})

test('reports a Sources value that cannot be resolved statically as dynamic', (t) => {
  const { write, lint } = fixture(t)
  write(
    'go-backend/testmachine/scenarios/demo/dynamic.go',
    `package demo

import "audiobookshelf-compat/testmachine/scenarios/harness"

func scenario(sources []string) harness.Scenario {
	return harness.Scenario{Sources: sources, Claim: "x"}
}
`
  )
  const report = lint()
  assert.deepEqual(report.findings, [])
  assert.equal(report.citationsChecked, 0)
  assert.deepEqual(report.dynamicSources, [{ file: 'go-backend/testmachine/scenarios/demo/dynamic.go', line: 6, name: 'sources' }])
})

test('ignores _test.go files and the harness package', (t) => {
  const { write, lint } = fixture(t)
  write('go-backend/testmachine/scenarios/demo/good.go', scenarioFile('"server/controllers/UserController.js:1"'))
  write('go-backend/testmachine/scenarios/demo/bad_test.go', scenarioFile('"server/controllers/Gone.js:1"'))
  write('go-backend/testmachine/scenarios/harness/plan.go', scenarioFile('"server/controllers/Gone.js:1"'))
  const report = lint()
  assert.deepEqual(report.findings, [])
  assert.equal(report.filesScanned, 1)
  assert.equal(report.citationsChecked, 1)
})

test('tokenizer decodes Go escapes and skips comments', () => {
  const tokens = tokenizeGo('a := "x\\ty" // "server/gone.js:1"\n`raw`')
  assert.deepEqual(tokens.filter((token) => token.kind === 'string').map((token) => token.value), ['x\ty', 'raw'])
})

test('classifyCitation keeps the citation forms explicit', () => {
  assert.deepEqual(classifyCitation('server/a.js:1-2,4'), {
    kind: 'line-checked',
    path: 'server/a.js',
    ranges: [
      { start: 1, end: 2 },
      { start: 4, end: null }
    ]
  })
  assert.equal(classifyCitation('server/a.js:1-2,4').kind, 'line-checked')
  assert.equal(classifyCitation('../client/x.vue:7').path, 'client/x.vue')
  assert.equal(classifyCitation('index.js:12').path, 'index.js')
  assert.equal(classifyCitation('go-backend/testmachine/contracts/x.md:12').kind, 'contract')
  assert.equal(classifyCitation('server/Database.js (createTextSearchQuery)').kind, 'line-checked')
  assert.equal(classifyCitation('README.md').kind, 'unknown')
  assert.equal(classifyCitation('../../etc/passwd.ts:1').kind, 'unknown')
})

test('CLI exits 1 on findings and writes the JSON report', (t) => {
  const { root, write } = fixture(t)
  write('go-backend/testmachine/scenarios/demo/bad.go', scenarioFile('"server/controllers/Gone.js:1"'))
  const reportPath = path.join(root, 'out', 'report.json')
  const output = { write: () => {} }
  const stderr = { write: () => {} }
  const code = runCli(['--root', root, '--report', reportPath], { stdout: output, stderr })
  assert.equal(code, 1)
  const report = JSON.parse(fs.readFileSync(reportPath, 'utf8'))
  assert.equal(report.finding_count, 1)
  assert.equal(report.findings[0].file, 'go-backend/testmachine/scenarios/demo/bad.go')
  assert.equal(report.findings[0].packet, 'demo')
  assert.equal(report.ok, false)
})

test('CLI exits 0 with a clean corpus and the summary is one line', (t) => {
  const { root, write } = fixture(t)
  write('go-backend/testmachine/scenarios/demo/good.go', scenarioFile('"server/controllers/UserController.js:1-5"'))
  const lines = []
  const code = runCli(['--root', root], {
    stdout: { write: (chunk) => lines.push(chunk) },
    stderr: { write: () => {} }
  })
  assert.equal(code, 0)
  const report = lintScenarioSources({ root })
  assert.equal(lines.at(-1).trim(), summaryLine(report))
  assert.match(lines.at(-1), /1 files in 1 packets, 1 citations checked \(1 distinct\), 0 findings — OK/)
})

test('CLI rejects an unknown flag', () => {
  const stderr = []
  const code = runCli(['--nope'], { stdout: { write: () => {} }, stderr: { write: (chunk) => stderr.push(chunk) } })
  assert.equal(code, 2)
  assert.match(stderr.join(''), /usage: scenario-sources-lint\.mjs/)
})

test('CLI refuses a scenarios directory with no scenario files', (t) => {
  const { root, scenarios } = fixture(t)
  fs.mkdirSync(scenarios, { recursive: true })
  const stderr = []
  const code = runCli(['--root', root], {
    stdout: { write: () => {} },
    stderr: { write: (chunk) => stderr.push(chunk) }
  })
  assert.equal(code, 2)
  assert.match(stderr.join(''), /no non-test scenario \.go files under/)
})

test('CLI notes a packet that declares no citation', (t) => {
  const { root, write } = fixture(t)
  write('go-backend/testmachine/scenarios/silent/empty.go', 'package silent\n')
  const stderr = []
  const code = runCli(['--root', root], {
    stdout: { write: () => {} },
    stderr: { write: (chunk) => stderr.push(chunk) }
  })
  assert.equal(code, 0)
  assert.match(stderr.join(''), /note: packet silent declares no resolvable Sources citations/)
})
