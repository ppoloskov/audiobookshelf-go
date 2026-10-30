# Roadmap

Supersedes the historical M0-M6 plan and the sequencing in
`docs/specs/planned/parity-finalization-work-queue.md` where they conflict.
The old plan is kept in git history. Compatibility boundary, oracle rules, and
fixture rules are unchanged: pinned upstream v2.37.2, independent disposable
copies per server, no Node fallback.

Status is generated into `STATUS.md`. This file defines order and exit
criteria, not current numbers.

## Why the plan changed

- Architecture work was gated on route coverage that the process could not
  reach, and the queue forbade the refactors that reduce the cost of reaching
  it.
- Verification was tracked in hand-written ledger records and status prose
  copied into several files. That measured paperwork and drifted.
- A differential run with 0 cases could exit 0.

## Phase 0: freeze and integrity

Pause new F-packets (F09 onward) while this phase runs.

Work:
1. Tag the current state.
2. Harness: a run with 0 executed cases exits non-zero. Add a self-test that
   feeds a deliberately wrong response and requires the harness to fail.
3. `make parity`: rebuild the Go binary, create a fresh fixture, run the
   differential, print executed/passed counts, fail on zero.
4. Generate `STATUS.md` from harness output (`make status`). Remove hand-written
   counts from `README.md`, `STATUS.md`, `handover.md`, `HANDOFF.md`, and root
   `agents.md`. Root `agents.md` becomes a pointer to `go-backend/AGENTS.md`.
5. Archive hand-curated capability records and ledger diagnostics under
   `docs/agent-work/archive/`. They are evidence, not a gate. Keep
   `build_route_coverage.py --check` as the generated route inventory.
6. Repository hygiene: find out why `--help/`, `.log`, `.json` and
   `.parity-work/` exist at `go-backend/` root and stop it; keep binaries out of
   the tree; run `go mod tidy`; fix stale package comments.
7. Confirm the review's bug list against current source. Verified fixed on the
   2026-10-30 snapshot: B1 (series pagination), B2 (feed fetcher wiring), B3
   (`podcasts.Close`), B10 (backup notifications wiring). Not yet re-checked:
   B4 (download task race), B5 (two DATETIME formats; partly addressed by
   `dbtime`), B6-B9.

Exit: `make parity` is trustworthy, one generated status file exists, docs
agree with each other.

## Phase 0b: harness consolidation

The Node harness is about 5.9k lines of `.cjs` with no shared module:
`freePort` is copied into 10 files, `login` into 10, `request` into 5, and
`waitReady` and `parseArgs` into 4 each. `differential.cjs` (2,493 lines)
holds 91 hardcoded `comparePair(...)` calls while `differential-cases.json`
holds 122 declarative cases, so there are two case formats. Do not port it to
Go now: the harness needs `socket.io-client`, `playwright`, `sqlite3` and
upstream's own code, and a rewrite would change the tool that catches
regressions (see D4).

Each item is its own packet. Do not combine items 1 and 2.

1. **Shared library.** Create `tools/parity/lib/` with modules for process and
   port handling (`freePort`, spawn, `waitReady`), HTTP and login, argument
   parsing, and fixture/report/provenance writing. Replace the duplicated
   copies.
2. **One case format.** Move the 91 inline `comparePair` calls into data files
   next to the existing 122 cases. Stateful sequences (bookmark lifecycle, OIDC
   flows) become named step modules referenced from the data. Target: the
   engine in `differential.cjs` is under about 500 lines and cases live in
   `cases/*.json`.
3. **Suites under one runner.** Turn the one-off scripts into named suites
   (`acl`, `cascades`, `oidc`, `socket-soak`, `existing-db-matrix`,
   `rollback-check`, `test-backup-restore`). `make parity` runs the fast core
   suite and `make parity-full` runs all suites. Fold `f07-rollin-check` and
   `operational-milestone` in as suites or archive them.
4. **Keep as separate tools:** `create-fixture.cjs`, `run-local.cjs`,
   `browser-check.cjs`, `library-oracle.cjs`, `export-schema.cjs`.
5. **Archive the Python ledger tooling** (`build_capabilities.py`,
   `classify-ledger-findings.py`, `test_build_capabilities.py`). Keep
   `build_route_coverage.py --check` and `source_inventory.py`.

**Equivalence gate for items 1-3.** Existing reports under
`contracts/parity/` are the golden files. After each item, regenerate them with
the consolidated harness on a fresh fixture. The new harness must reproduce the
same case IDs, the same pass/fail results, and the same normalized body
digests. Any difference is investigated before the item merges. This gate
depends on the Phase 0 zero-case failure and the red-response self-test.

Exit: no duplicated harness helpers, one case format, suites runnable from
`make parity` and `make parity-full`, and the equivalence gate passes.

## Phase 1: guardrails

1. `tools/ratchet` (Go, `go/ast` for Go code, plus a regex counter for the two
   harness metrics) and `ratchet-baseline.json`; `make ratchet` and
   `make ratchet-update`. The harness metrics are inline `comparePair` calls
   (91 to 0) and duplicated harness helper definitions (`freePort` has 10
   copies, target 1).
2. `.golangci.yml` with `depguard` rules from `ARCHITECTURE.md` (dependency
   directions, no `httptest` outside tests).
3. CI runs build, vet, test, ratchet, depguard. Parity runs in CI if Node and
   FFmpeg are available there; otherwise it is a required pre-merge local run
   recorded in the commit message.

Exit: a change that raises a metric or breaks a dependency rule fails CI.

## Phase 2: pilot (`bookJSON` to `wire`)

Scope: `catalog.bookJSON` (5 direct callers) and everything reached through
`ItemJSON`/`ItemMinifiedJSON` (`userapi`, `scanner` emits, `runtime` emit hook).

1. Capture golden outputs of `ItemJSON` and `ItemMinifiedJSON` for every fixture
   item before changing code.
2. Add `internal/wire` book types (envelope only; inner objects stay opaque,
   D5).
3. Add `loadBookRow` plus typed builders; keep the old function during the
   pilot.
4. Dual-run test: old vs new for every fixture book plus edge cases (null
   description, `"null"` ebook column, series without sequence, multi-file
   audio, explicit/abridged).
5. Switch callers one at a time; run `catalog`, `scanner`, `userapi` package
   tests after each.
6. `make parity`: same case count, same result. Goldens unchanged.
7. Delete the old function and helpers that lost their last caller; lower the
   baseline.

Exit: the pilot merges with an unchanged differential result, and the metric
drop is recorded. The measured drop sets the `map[string]any` target for later
phases.

## Phase 3: leaf packages

Each item is its own packet.

1. `events`: one emitter type; remove per-package `EmitFunc` types.
2. `httputil`: one set of Express-exact writers and one request decoder.
3. `ids`: one UUID implementation that returns errors.
4. `media`: one ffmpeg/ffprobe runner with a shared timeout and stderr policy.
5. `auth`: one JWT issue-and-verify implementation (replaces three signers).
6. `dbtime`: finish the single write format and one parser. Needs a DB-content
   differential for playback-session save and podcast update first.

Exit: ratchet metrics for writers and emit types reach their Phase 3 targets.

## Phase 4: injection and lifecycle

1. Deps structs; remove all `Set*` injectors and nil-emit guards.
2. One shared `items` reader (D1) or one shared `catalog.Handler`.
3. `New` side-effect free; `Start`/`Close` that waits.
4. Remove `httptest` self-dispatch in `library_writes.go`; extract plain
   functions.
5. Concurrency fixes from the review (scanner mutex split, realtime
   snapshot-then-emit, playback transcode timing, settings read-modify-write),
   each with a `-race` test.

Exit: `Set*` and `httptest`-in-prod metrics are 0.

## Phase 5: routing

Smallest packages first: `sharing`, `notifications`, `emailapi`, `adminops`,
`audiotools`, `providers`, `backup`; then `podcasts`, `playback`, `scanner`,
`identity`, `userapi`; `catalog` last.

For each package:
1. Add smoke cases: happy path, 401, 403, 404, wrong method.
2. Add `Routes(mux, deps)`; remove that package's `Handle` chain; keep the
   legacy catch-all for the rest.
3. Verify 404/405 behavior matches upstream (explicit fallbacks).
4. `make parity` unchanged.

Exit: `Handle`-chain metric is 0; the legacy catch-all is deleted.

## Phase 6: DTO expansion

Order: request DTOs first (starting with `updateProgress`), then response DTOs
by package (catalog first, then `userapi`, `playback`, others). Each slice
follows the Phase 2 loop. Inner pass-through objects are typed last.

## Phase 7: authorization

Generate a permission matrix per affected route against upstream. Consolidate
into `authz` only where behavior is identical; keep named variants where
upstream differs. Resolve B6 (any-tag vs every-tag) explicitly.

## Product track (parallel; owned by the project owner)

"Done" means the target clients work, not that every long-tail variant is
ledger-verified.

1. **Define the client scope.** Which clients: stock web, Android, iOS, a
   customer's custom client? Record the answer in this file.
2. **Capture real traffic** from the TypeScript server for those clients:
   routes, methods, Socket.IO events.
3. **Tier the routes** by observed use: Tier 1 (clients hit them), Tier 2
   (rare), Tier 3 (never observed). Defer Tier 3 explicitly instead of
   ledger-verifying it.
4. **Replay harness** runs captured traffic against Node and Go on fresh
   fixtures and diffs responses, persisted state, and emitted events.
5. **Acceptance:** Tier 1 replay is clean; web and mobile acceptance pass;
   upgrade and rollback trial passes (pinned upstream reopens a Go-written
   database copy); realistic library load is measured; `CGO_ENABLED=0` amd64
   and arm64 builds work.

## How parity work and refactors coexist

F-packets resume after Phase 1, ordered by client tier. They may run alongside
Phases 2-7 only when their owned paths are disjoint. A refactor packet and a
parity packet never own the same package at the same time. Each packet names
its owned paths up front.

## What we stop doing

- Updating several files with the same status paragraph.
- Hand-editing ledger records as a completion gate.
- Packets whose acceptance is "update the documents".
- Treating 0 executed cases as a pass.
