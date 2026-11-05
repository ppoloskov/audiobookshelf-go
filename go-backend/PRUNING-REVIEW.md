# Dead code and duplication review

Reviewed 2026-11-05 against tracked master `948f6a15`. Cheap workers inspected
dead code and duplicates; architect independently checked the named bodies,
references, caller types and replacement semantics. This is an analysis, not
an implementation or a passing deletion test. Runtime source change: zero.

Scope: handwritten production Go backend. Generated sqlc, pinned upstream,
unchanged client, test-only serialization oracles and inherited untracked
work are excluded. A clean `git archive` isolated static analysis from WIP.
No broad route-removal or whole-repository reachability proof is claimed.

## Direct candidates

Paths below are relative to `go-backend`. Estimates count nonblank production
source, include necessary import overhead, exclude tests and are approximate;
actual ratchet deltas must be measured after implementation. These candidates
do not remove a route, permission check, persisted format or fallback.

| Class / packet | Location and evidence | Recommendation | Estimated saving |
|---|---|---|---:|
| proven-removable / PR01 | `internal/catalog/batch_chapters.go:586` anyStrings; `internal/providers/book_finder.go:252` levenshteinSimilarity; `internal/scanner/files.go:1718` sameBytes and `:1761` parseSequence; `internal/userapi/userapi.go:2346` nullNumber and `:2352` timestampString. Each name occurs only at its private definition across Go sources; unused analyzer independently agrees. | Delete these six definitions only. Keep active levenshteinDistance, extractSequence, finiteNumber and adjacent helpers/imports still used. | 41 |
| replace-with-standard / PR02 | `internal/playback/playback.go:2115`, `internal/adminops/handler.go:680`, `internal/realtime/hub.go:738`: identical eight-line contains loops, all callers accept strings. | Replace calls with stdlib slices.Contains; remove the three bodies. Preserve every ACL/tag/MIME caller, negation and operator. Three imports included. | 21 |
| merge-duplicate / PR03 | `internal/catalog/quickmatch.go:703` nullValue and `internal/scanner/metadata.go:196` nullDBString equal store.NullString: invalid -> nil, valid -> string, including empty string. `internal/catalog/authors.go:463` valueOrNil equals store.StringPtrValue. | Reuse existing store helpers; delete three bodies. Quickmatch/metadata need two imports; authors already imports store. | 16 |
| merge-duplicate / PR04 | `internal/identity/identity.go:897–899` text plus its comment duplicates store.Text. All callers bind non-NULL sql.NullString, even for empty text. | Replace calls with store.Text; delete local helper/comment. Retain TextOrNull's different semantics. | 3 |

**Direct estimate: 81 production lines**, not an observed metric reduction.
No new package or dependency is needed for PR01–PR04. This is a small verified
inventory; it does not establish a path to a 30% reduction.

## Conditional candidates

- **merge-duplicate / PR05:** playback `parseUA` + `after` at
  `internal/playback/playback.go:1897–1940` and sharing `parseUserAgent` at
  `internal/sharing/sharedevice.go:82–117` implement the same four-string
  parser. Browser/OS precedence, case sensitivity, markers, delimiters and
  underscore conversion match by inspection. A small shared leaf helper can
  remove roughly 30 additional lines after package/import/comment overhead.
  Both callers need it, so duplication removal is the reason for the package;
  do not introduce a parser interface or configurable framework. First freeze
  output for common browsers, overlapping OS markers, empty/unknown strings,
  missing Version markers and delimiters; then test both device consumers.
  `userapi/sessions.go:99` is deliberately excluded: richer output, different
  case/OS handling, tablet/mobile fields and nil behavior.
- **likely-removable / PR06:** `scanner/files.go:728` mergeBookMetadata is
  called only by two assertions in TestMetadataSourcesFollowConfiguredPrecedence.
  Its body matches the active mergeBookMetadataWithABS path with hasABS=false.
  Retarget those assertions to the real function with empty ABS metadata,
  preserve precedence coverage and delete the 27-line duplicate. Also
  `scanner/files.go:1392` trackNumbersFromFilename is a three-line wrapper used
  only in TestFilenameTrackDiscParsingMatchesUpstream; direct filenameNumbers
  calls must retain its empty arguments. Sharing handler.go:164 sqlDate is a
  one-line wrapper used only by tests; those may use dbtime.Format directly.
  Potential 31 production lines, conditional on retaining assertions and
  accounting for changed test setup. Do not move unused implementations into
  test files just to lower the production metric.
- **unknown / keep for now:** scanner/files.go:88 groupKey (21 lines) is
  test-only, while grouping runs through scan/watcher paths. Deletion requires
  redirecting the test to real grouping behavior and proving equivalent
  coverage. cdDirectory is still used by the watcher and must remain.

## Keep / exclusions

- countSeedPaths in internal/contracttest and countDynamicMapTypes in
  tools/ratchet have real test callers. Analyzer warnings with tests disabled
  do not make those test/tool helpers dead product code.
- Nullable JSON/container helpers differ on malformed/null inputs. Podcast
  time helpers preserve zero-epoch behavior. Stored JSON compaction and Go
  value marshaling are different operations. No blanket replacement.
- isAdmin one-liners and dbtime forwarding aliases offer little benefit
  after shared-layer/import cost. No new abstraction for those alone.
- User/library/tag ACL variants need the C12 permission matrix before S6.
  No route is removable solely because the bundled web client lacks a caller.
  Mobile evidence remains partial; V2 tooling still has active shared inputs.
- Untracked identity/routes.go is owner WIP, not dead shipped code. Preserve it
  and .codemap/TODO. Independent test oracles retain their value.

## Verification and execution

Observed: master SHA confirmed; clean archive; installed golangci-lint 2.15.0
`run --no-config --enable-only=unused --tests=false ./...` returned exit 1,
12 findings. Six are uncalled production functions, four are production
helpers with test callers, two are retained test/tool helpers. Direct rg
reference searches and source comparison refined the classifications above.
No dependency installation, code deletion, package test or runtime experiment
was performed for this review. Worker reports are under
`/tmp/abs-master-consolidation-20261105/{dead-code,duplicates}.md`.

Next: implementation-scope-control/minimal-implementation-ladder for the named
PR cards in ROADMAP; go-behaviour-tests for PR05/PR06. All work stays on master,
one bounded worker packet at a time. Each card names affected package checks;
the accepted wave then runs AGENTS integration gates and measures actual
ratchet changes. Existing five native libwebp journey failures remain a known
host limitation, not a waiver for additional failures.

net: -81 estimated production lines possible in direct candidates
