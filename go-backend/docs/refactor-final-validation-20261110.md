# Final local refactor validation — 2026-11-10

> The disposable-snapshot evidence below is retained as historical provenance.
> The canonical worktree results after QM01, identity/routes, and the approved
> combined baseline are recorded in the final integration addendum at the end.

## Result and limits

The final disposable snapshot for R1 + Wave G + RT1 passed the required local
Go, source/contract, build, lint, generation, cross-build and TS rollback gates.
This is local validation evidence; it is not a commit, remote CI result,
publication or deployment. The roadmap cards remain unchecked until the owner
reconciles the canonical baseline and unmerged index state and accepts a
completion commit. The separate quickmatch ownership conflict remains in its
other chat.

## Snapshot provenance and exact source

- Canonical base: `b42969b9431c635ab9032f057c033bc429323d2f` (`master`).
- Validation tree: `/private/tmp/abs-goal-final-validate-20261110`, created
  from `git archive HEAD`, with only the approved Wave G manifest, the latest
  R1 and fixture files, and reviewed RT1 `eio.go` overlaid. The Wave G
  production/test list is preserved in
  [the approved manifest](wave-g/VALIDATION.md#exact-approved-overlay-manifest).
  R1 additions were `internal/playback/playback.go`,
  `internal/userapi/userapi.go` and `internal/userapi/online_users_test.go`;
  RT1 added only `internal/realtime/eio.go`.
- Base archive SHA-256:
  `1e081e3790740a5d009c4899ffe7d179e0bf300b4cbbf8279e4e0dc44777f3d2`.
  Wave G + R1 overlay archive SHA-256:
  `2e2b915e73e36aa02a49e02ee4a325e824bef0fb948438a4a3330dc320664fb0`.
  RT1 was overlaid separately at
  `go-backend/internal/realtime/eio.go`, SHA-256
  `b810994969e969b1bd8c6b1acfd514fe976b84ebcac3e1597982021e9160ecc7`.
- The overlay hash inventory is
  `/private/tmp/abs-goal-final-validate-20261110/overlay-source-hashes.txt`
  (SHA-256 `816910e1dcb4ce8583343d6a32e4f427c8e7c9398bbefd19708264090d81c2fd`).
  Before writing this report, all 28 listed overlay paths and RT1 `eio.go`
  were rehashed in the canonical checkout and matched the final snapshot.
  The final G4 transition test hash is
  `d1bb6adff4d9988255c194df84607079a4019604ed2dd9e91fcd2ea7da621bb7`.
- The snapshot excludes the conflicted **worktree versions** of
  `internal/catalog/quickmatch.go`, `ratchet-baseline.json`, `ROADMAP.md`, and
  `handover.md`; its starting content for those paths is from HEAD. The
  snapshot-only ratchet baseline was seeded from the reviewed Wave G values,
  then set to the measured approved R1/RT1 values. Other excluded owner WIP
  includes `internal/identity/routes.go`, `.codemap/`, the optional
  quickmatch test and unrelated FFmpeg feasibility work. The quickmatch source
  from HEAD remains in the build and test tree.

The approved snapshot ratchet baseline was reviewed against the measured tree
before use. Final values were 38,048 handwritten Go LOC, 8,522 total
complexity, 115 functions over complexity 15, 461 `map[string]any` uses and
202 `[]any` uses; other measured fields were unchanged. RT1's assigned budget
was +30 LOC / +8 complexity with no other growth; actual RT1 growth was +16 /
+2. R1 independently reduced production code by 2 LOC and maps by 3. These
effects are reported separately; the snapshot-only baseline is not the
canonical conflicted baseline.

The existing route coverage generator refreshed the single authorized
`contracts/parity/route-coverage.json` artifact. All 226 routes, semantic
counts and route records were identical; only SHA pins for `userapi.go`,
`playback.go` (R1) and `scanner.go` (Wave G) changed. The canonical result
matches the reviewed snapshot candidate, and
`python3 go-backend/tools/parity/build_route_coverage.py --check` passes.

## Required Go workflow gates

All commands below ran in the final snapshot unless identified as the RT1
worker's isolated package check.

| Gate | Result | Evidence |
|---|---|---|
| `go test ./... -count=1` with `CGO_ENABLED=0`, `ABS_PARITY_REQUIRE_SOCKETIO=1`, `NODE_PATH` set to the installed JS client, and documented native FFmpeg/WebP prefix | PASS, exit 0 | [full suite log](/private/tmp/abs-goal-final-validate-20261110/full-go-test-rt1-webp.log) |
| R1 endpoint, `go test ./internal/userapi ./internal/playback`, and targeted races | PASS per implementation worker; combined full suite also passed | R1 implementation evidence; see R1 decision log |
| Existing realtime buffer regression, realtime package and `go test -race ./internal/realtime` | PASS in RT1's isolated HEAD + exact `eio.go` snapshot; the regression was unchanged. Combined full suite also passed. | RT1 worker snapshot `/private/tmp/rt1-validate-20261110`; final source hash above |
| `go build ./...` | PASS, exit 0 | [build log](/private/tmp/abs-goal-final-validate-20261110/build-rt1.log) |
| `go vet ./...` | PASS, exit 0 | [vet log](/private/tmp/abs-goal-final-validate-20261110/vet-rt1.log) |
| golangci-lint v2.15.0, `--config=../.golangci.yml --timeout=5m ./...` | PASS, exit 0 | [lint log](/private/tmp/abs-goal-final-validate-20261110/lint-v2.15.0-rt1.log) |
| SQLC v1.31.1: `make sqlc SQLC=/Users/paul/go/bin/sqlc`; compare the full generated directory before/after | PASS, exit 0; no added, removed or changed generated files | `sqlc-before-rt1/` and the final `internal/db/sqlc/` tree in the snapshot |
| `go run ./tools/ratchet` and `go run ./tools/dbnest` | PASS, exit 0 against the reviewed temporary snapshot baseline | Ratchet output: 38,048 / 8,522 / 115 / 461 maps / 202 arrays |
| Linux/amd64 and Linux/arm64 server cross-builds | PASS, exit 0 | `/private/tmp/abs-goal-final-validate-20261110/audiobookshelf-go-amd64` and `...-arm64` |

The full suite used the native WebP-enabled FFmpeg at
`/private/tmp/abs-cleanup-checkpoint-20261105/ffmpeg-9.0.2-webp/bin`, with
both `PATH` and `FFMPEG_PATH` set. An initial invocation with Homebrew FFmpeg
9.0.2 (which lacks libwebp) failed image journeys; after switching to the
documented binary, `TestJourney` passed and the final full suite passed. No
assertion or source changes were made to address that environment mismatch.
The required Socket.IO test was configured with
`ABS_PARITY_REQUIRE_SOCKETIO=1`; missing JS client support could not silently
skip that test.

## Required source and contract workflow gates

The Node commands used Node v22.23.4. The host only had Python 3.14.7
available; the workflow provisions Python 3.12, so the Python checks passed
locally on 3.14.7 rather than that exact CI version. Local Go was 1.27.0 with
`GOTOOLCHAIN=local`; the workflow selects the Go version in `go.mod` (1.27.3).

| Gate | Result |
|---|---|
| `python3 -m unittest -v go-backend/tools/test_source_inventory.py` | PASS, 5/5 |
| `node go-backend/tools/test-library-oracle.cjs` | PASS |
| `node go-backend/tools/test-book-item-oracle.cjs` | PASS |
| `python3 go-backend/tools/parity/build_route_coverage.py --check` | PASS, 226 unverified route records |
| `python3 -m unittest -v go-backend/tools/parity/test_route_coverage.py` | PASS, 4/4 |
| `node --test go-backend/tools/parity/test-compare-reports.cjs go-backend/tools/parity/test-status.cjs` | PASS, 21/21 |
| `node --test go-backend/tools/test-assertion-manifest.cjs go-backend/tools/parity/test-cover-patch-contract.cjs` | PASS on Node 22: 8 passed, 1 optional Monstrobook inventory test skipped |
| `node go-backend/tools/test-dependency-boundaries.cjs` | PASS, 19 fixtures |

Node v26 was accidentally selected in the first assertion-contract attempt;
its loopback fixture was denied by the sandbox. The prescribed Node 22
invocation with the narrow authorized local-listener test run passed. The
optional Monstrobook snapshot test remained skipped as designed. The workflow's
representation diagnostics and upstream differential parity jobs are
`continue-on-error` advisory and were not run. This repository has no
`ci-local.sh`; the commands above mirror the required jobs in
`.github/workflows/docker-go.yml` and `.github/workflows/go-backend.yml`.

## Built binary and rollback

The final binary was rebuilt from this snapshot at
`/private/tmp/abs-goal-final-validate-20261110/audiobookshelf-go` (SHA-256
`c7d33f60fcdafe70a731ca8edab9b1869e33cc5b984bd726a0b3b3bbbf1ad622`). The
Node v22.23.4 server build passed. The pinned upstream oracle inputs
(`server`, `index.js`, `tsconfig.server.json`, `package.json`,
`package-lock.json`) had no diff from `edb1f7efc92ecacdef51aafb4d9d5054ce10604f`
and no untracked input files.

```sh
mise exec node@22 -- node go-backend/tools/parity/replacement-trial.cjs \
  --rollback-only \
  --go-bin /private/tmp/abs-goal-final-validate-20261110/audiobookshelf-go \
  --work /private/tmp/abs-goal-final-rollback-rt1-20261110
```

Result: exit 0; the TypeScript server reopened the Go-written database,
`progressMatches=true`, `bookmarksMatch=true`, `sessionCount=8`, and
`sessionsMatch=true`. Logs and report are under
`/private/tmp/abs-goal-final-rollback-rt1-20261110/`.

## Remaining owner work

The canonical ratchet baseline and Git index remain unmerged; the unmerged
paths are `ROADMAP.md`, `handover.md`, `internal/catalog/quickmatch.go`, and
`ratchet-baseline.json`. No index entry was changed by this validation. The
owner must reconcile the combined baseline and index, review the coherent
snapshot, and accept its completion commit before roadmap boxes change to
`[x]`. No commit, push, remote GitHub CI or deployment is claimed. The pinned
TS representation and differential diagnostics remain advisory, not blockers
for this local gate report.

## Final canonical integration addendum — 2026-11-10

This addendum supersedes earlier status and metric statements above. It covers
the canonical worktree after QM01 and `internal/identity/routes.go` were
reviewed, and after the fetched `origin/master` documentation-only fast-forward
to `fc5aa760963c639f61ccfe9ceeb6a16c51b7b714`. The current source tree includes
the prior Wave G, R1 and RT1 work, QM01, identity routes, and the user's other
in-scope work. The local completion commit and remote CI are still pending.

### QM01 behavior and metrics

The actual QuickMatch provider adapter now stores raw numeric zero as `"0"`; the
old zero regression used a fake finder and bypassed that adapter, so the test
input was corrected. Empty converted author/series lists preserve existing
relations; duplicate series updates run sequentially against the current cache;
missing sequence metadata preserves the stored position. Missing, explicit
null, empty text and numeric text remain distinct. Scanner blanks remain absent,
while PATCH missing/non-string inputs remain explicit NULL and strings
(including empty) remain explicit text. Existing Go characterization retains
event ordering. Focused regressions, catalog/itemwrite/scanner packages and
their race runs pass.

The assigned QM01 budget was +50 LOC/+12 complexity with no other metric
growth; actual growth was +26 LOC/+5 complexity. Final measured metrics are
38,119 handwritten LOC / 8,531 cyclomatic complexity / 115 functions over 15 /
461 `map[string]any` / 202 `[]any`; all other ratchet metrics are unchanged.
The worktree baseline matches these values. RT1's +16/+2 and R1's -2 LOC/-3
maps remain independent packet deltas.

### Canonical command results

| Command/check | Result |
|---|---|
| `ABS_PARITY_REQUIRE_SOCKETIO=1 NODE_PATH=/Users/paul/projects/audiobookshelf-go/node_modules GOTOOLCHAIN=local go test ./... -count=1` with Node 22 and native FFmpeg 9.0.2/libwebp | PASS, exit 0; no skipped Go packages |
| `go build ./...`; `go vet ./...` | PASS, exit 0 |
| golangci-lint v2.15.0 `--config=../.golangci.yml --timeout=5m ./...` | PASS, 0 issues |
| `make ratchet` (`tools/ratchet` and `tools/dbnest`) | PASS, exit 0 at the measured values above |
| SQLC v1.31.1 `make sqlc SQLC=/Users/paul/go/bin/sqlc` | PASS after generating the `playback.sql.go` clause to match its current query; repeat generation was byte-identical and produced no untracked outputs |
| Linux amd64 and arm64 CGO-disabled cross-builds | PASS, exit 0 |
| Required source/contract checks | PASS: source inventory 5/5; route ledger 226 records and route tests 4/4; both source oracles; report/status tests 21/21; assertion checks 8 passed with one optional Monstrobook test skipped; dependency-boundary fixtures 19; `tools/clientusage` package test |
| TS v2.37.2 rollback-only journey with rebuilt canonical Go binary | PASS, exit 0; progress/bookmarks/sessions match, 8 sessions |

Node checks used v22.23.4. Local Go was 1.27.0 (`GOTOOLCHAIN=local`) versus
workflow Go 1.27.3; Python checks used 3.14.7 versus workflow 3.12. The optional
Monstrobook inventory test remains skipped by design. Advisory representation
diagnostics and upstream differential parity were not run. The first full test
run exposed a stale generated `client-usage.json` route-coverage hash; the
documented generator changed only that hash (201 routes / 1 unmatched / 25
unused remained unchanged), and the final full suite passed.

The rebuilt binary is
`/private/tmp/abs-final-canonical-20261110/audiobookshelf-go`, SHA-256
`7b82055bfd8e913feb190971d7429ecc0b4af6023d82cca17692450bf6e174f0`. The
rollback run is under
`/private/tmp/abs-final-canonical-rollback-20261110/`. The previous Wave G
integration report contains earlier listener-blocked observations and is
historical; this addendum records the final authorized local verification.
