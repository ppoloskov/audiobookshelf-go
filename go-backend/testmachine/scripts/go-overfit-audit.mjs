#!/usr/bin/env node
// go-overfit-audit.mjs — prove the Go implementation does not hardcode to the
// test machine's fixture.
//
// The fixture facts (usernames, passwords, seeded ids, item/author titles,
// media file names) come from the fixture install that the test machine
// copies: internal/contracttest/testdata/install (fixture.json, ids.json,
// media/). The harness reads them from there at run time — the only places
// those literals may legitimately appear in Go code are that harness and its
// helpers. This tool scans every non-test .go file under go-backend/internal
// and go-backend/cmd for those literals.
//
// A hit is either overfitting (reported, exit 1) or a deliberate mention
// recorded in the committed report's allow_list with a reason. Two fixture
// file names are known upstream-defined behaviour, not fixture-specific:
// `desc.txt` and `reader.txt` are companion files upstream's scanner reads
// (server/scanner/BookScanner.js:745-754, LibraryItemScanData.js:148-153),
// so any hit is adjudicated, not treated as overfitting, and must be
// allow-listed explicitly with that reason.
//
// Deliberately NOT scanned (upstream defines them, so a hit would not prove
// fixture dependence): the bare words `root`, `listener`, `finisher`
// (upstream has a root user type; `listener`/`finisher` are scenario-side
// aliases, the seeded usernames are `trial-*`), the media folder names
// `audiobooks`/`podcasts`, and `absdatabase.sqlite` (upstream's database
// file name). Generic ids.json keys (`library`, `collection`, `playlist`,
// `podcast`, `novel`, `ebook`, `disposable`) are also excluded; the
// distinctive scenario keys are scanned.
//
// Usage:
//   node go-overfit-audit.mjs   # scan, update reports/go-overfit-audit.json
//                               # and exit 0 (no findings) or 1
//
// Exit codes: 0 = no un-allow-listed fixture literal in non-test Go code,
// 1 = findings listed on stdout (they are reported, never auto-fixed).

import fs from 'node:fs'
import path from 'node:path'
import process from 'node:process'
import { fileURLToPath, pathToFileURL } from 'node:url'

const SCRIPT_DIR = path.dirname(fileURLToPath(import.meta.url))
const TESTMACHINE = path.resolve(SCRIPT_DIR, '..')
const GO_BACKEND = path.resolve(TESTMACHINE, '..')
const INSTALL = path.join(GO_BACKEND, 'internal', 'contracttest', 'testdata', 'install')
const SCAN_DIRS = ['internal', 'cmd'].map((dir) => path.join(GO_BACKEND, dir))
const REPORT = path.join(TESTMACHINE, 'reports', 'go-overfit-audit.json')

// Fixture titles, author and media file names from the seeded install. These
// exist only because the fixture provides them; a hit in application code
// would mean the code expects the fixture instead of user data.
// Filenames that upstream itself defines are scanned too and adjudicated
// (see allow_list): desc.txt / reader.txt.
const CURATED_LITERALS = [
  // users (fixture.json), plus their passwords
  'trial-root',
  'trial-listener',
  'trial-finisher',
  'trial-root-password',
  'trial-listener-password',
  'trial-finisher-password',
  // fixture install pin (fixture.json upstreamCommit)
  'edb1f7efc92ecacdef51aafb4d9d5054ce10604f',
  // library folder / author / item titles (media/)
  'Ada Example',
  'Single File Novel',
  'Ebook Only',
  'Zed Disposable',
  'Hard Delete Me',
  'Terry Fixture',
  'Long Saga',
  'Book 1 - The Beginning',
  'Book 2 - The Middle',
  'Fixture Cast',
  // media file names (media/)
  'Single File Novel.m4b',
  'Ebook Only.epub',
  'Hard Delete Me.mp3',
  '01 - Part 1.mp3',
  '02 - Part 2.mp3',
  '03 - Part 3.mp3',
  'The Middle.flac',
  'reader.txt',
  'desc.txt',
  'episode-1.mp3',
  'episode-2.mp3',
  'episode-3.mp3',
  // distinctive scenario-side keys of ids.json (the values are the seeded
  // uuids, read below; generic keys like `library` or `novel` are skipped)
  'saga1',
  'saga2',
  'ep1',
  'ep2',
  'ep3',
  'podcastLibrary',
  'podcastPlaylist',
]

// Reads the seeded ids.json and returns its values (uuids) as fixture
// literals. Returns [] when the install is absent so the tool still runs.
export function fixtureIdLiterals(installDir = INSTALL) {
  const file = path.join(installDir, 'ids.json')
  if (!fs.existsSync(file)) return []
  let ids
  try {
    ids = JSON.parse(fs.readFileSync(file, 'utf8'))
  } catch (err) {
    throw new Error(`read ${path.relative(process.cwd(), file)}: ${err.message}`)
  }
  return [...new Set(Object.values(ids).filter((v) => typeof v === 'string'))]
}

export function fixtureLiterals(installDir = INSTALL) {
  return [...CURATED_LITERALS, ...fixtureIdLiterals(installDir)]
}

function walkGoFiles(dir) {
  const files = []
  for (const entry of fs.readdirSync(dir, { withFileTypes: true })) {
    const full = path.join(dir, entry.name)
    if (entry.isDirectory()) files.push(...walkGoFiles(full))
    else if (entry.isFile() && entry.name.endsWith('.go') && !entry.name.endsWith('_test.go')) {
      files.push(full)
    }
  }
  return files
}

// Scans the given non-test .go files for the literals. Returns hits as
// { literal, file (relative to goBackend), line }.
export function scanCorpus(dirs, literals, goBackend = GO_BACKEND) {
  const hits = []
  let filesScanned = 0
  for (const dir of dirs) {
    if (!fs.existsSync(dir)) continue
    for (const file of walkGoFiles(dir)) {
      filesScanned++
      const rel = path.relative(goBackend, file)
      const lines = fs.readFileSync(file, 'utf8').split('\n')
      for (let index = 0; index < lines.length; index++) {
        for (const literal of literals) {
          if (lines[index].includes(literal)) {
            hits.push({ literal, file: rel, line: index + 1 })
          }
        }
      }
    }
  }
  return { hits, filesScanned }
}

// allowList entries: { literal, file, reason, upstream_ref? }. A hit is
// allow-listed when both the literal and the file match.
function allowListed(hit, allowList) {
  return allowList.some((entry) => entry.literal === hit.literal && entry.file === hit.file)
}

// Loads the committed adjudications (allow_list) from the report file.
export function loadAllowList(reportFile = REPORT) {
  if (!fs.existsSync(reportFile)) return []
  try {
    const previous = JSON.parse(fs.readFileSync(reportFile, 'utf8'))
    return Array.isArray(previous.allow_list) ? previous.allow_list : []
  } catch {
    return [] // a corrupted report is replaced, not silently kept
  }
}

export function audit({ dirs = SCAN_DIRS, installDir = INSTALL, allowList, goBackend = GO_BACKEND } = {}) {
  const literals = fixtureLiterals(installDir)
  const { hits, filesScanned } = scanCorpus(dirs, literals, goBackend)

  // Keep the committed adjudications: allow_list entries survive re-runs.
  const list = allowList ?? loadAllowList()
  const findings = hits.filter((hit) => !allowListed(hit, list))
  const adjudicated = hits.filter((hit) => allowListed(hit, list))
  return {
    literals,
    hits,
    allowList: list,
    findings,
    adjudicated,
    filesScanned,
    report: {
      counts: {
        go_files_scanned: filesScanned,
        fixture_literals: literals.length,
        hits: hits.length,
        allow_listed: adjudicated.length,
        unallowlisted_findings: findings.length,
      },
      findings,
      allow_list: list,
    },
  }
}

const invokedDirectly = process.argv[1] && import.meta.url === pathToFileURL(process.argv[1]).href
if (invokedDirectly) {
  let result
  try {
    result = audit()
  } catch (err) {
    console.error(`go-overfit-audit: ${err.message}`)
    process.exit(1)
  }
  const relReport = path.relative(process.cwd(), REPORT)
  console.log(
    `go-overfit-audit: scanned ${result.report.counts.go_files_scanned} non-test .go files for ` +
      `${result.report.counts.fixture_literals} fixture literals: ` +
      `${result.report.counts.hits} hit(s), ${result.report.counts.allow_listed} allow-listed, ` +
      `${result.report.counts.unallowlisted_findings} finding(s)`
  )
  for (const hit of result.findings) {
    console.log(`  FINDING ${hit.literal} at ${hit.file}:${hit.line}`)
  }
  for (const hit of result.adjudicated) {
    const entry = result.allowList.find((e) => e.literal === hit.literal && e.file === hit.file)
    console.log(`  allow-listed ${hit.literal} at ${hit.file}:${hit.line} — ${entry?.reason ?? ''}`)
  }
  fs.mkdirSync(path.dirname(REPORT), { recursive: true })
  fs.writeFileSync(REPORT, JSON.stringify(result.report, null, 2) + '\n')
  console.log(`go-overfit-audit: report written to ${relReport}`)
  process.exit(result.findings.length > 0 ? 1 : 0)
}
