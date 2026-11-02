# Roadmap

Supersedes the historical M0-M6 plan and the sequencing in
`docs/specs/planned/parity-finalization-work-queue.md` where they conflict.
The old plan is kept in git history. Compatibility boundary, oracle rules, and
fixture rules are unchanged: pinned upstream v2.37.2, independent disposable
copies per server, no Node fallback.

Status is generated into `STATUS.md`. This file defines order and exit
criteria, not current numbers. Dated reviews are in `archive/reviews/` and are
evidence, not current state.

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

North Star: produce a Go server build that can replace the TypeScript server
in an existing installation, with clients, books and progress. "Done" means
the target clients work against a swapped-in install, not that every
long-tail variant is ledger-verified.

**Client scope (decided).** The stock web client (`client/`, driven in
Chromium) plus the Android and iOS apps' API usage. The apps themselves are not
driven; their call pattern is replayed. A customer-specific client is out of
scope until named.

**Tiers.** Tier 1: routes and Socket.IO events those clients hit. Tier 2: rare.
Tier 3: never observed; deferred explicitly rather than ledger-verified.

**Replay harness.** `tools/parity/replacement-trial.cjs` (`make
parity-replacement`, details in `tools/parity/README.md`) is the Tier 1 replay
harness. Upstream creates a realistic install (multi-file mp3 book with cover,
m4b with chapters, flac with desc/reader sidecars, epub-only item; Android, iOS
and web history; a second user), the install is snapshotted, the same
mobile/web journey runs against upstream and against Go in place, a Socket.IO
`user_updated` probe runs, and rollback reopens the Go-written database with
upstream. It is a required check for product-facing packets (`AGENTS.md`).
Its latest result is recorded by its own report, not here.

**Acceptance for the product track:** Tier 1 replay is clean (including the
workstreams below); web and mobile acceptance pass; upgrade and rollback trial
passes; realistic library load is measured; `CGO_ENABLED=0` amd64 and arm64
builds work.

### Product workstreams (done 2026-11-02)

All four are on master, each with a CI-gated trial (`.github/workflows/docker-go.yml`
parity job). Details are in the commit messages.

1. **Podcasts in the trial.** Local podcast library, episode play/sync/close,
   episode progress, offline sessions, playlists, shelves and search.
2. **Known gaps.** Collapsed sub-series, `localeCompare` playlist order
   (`internal/textsort`), `rssfeed`/`share` includes on shelves and items, ICU
   time zone canonicalization (`internal/identity/icu_zones_gen.go`).
3. **New content after the swap.** `tools/parity/scan-trial.cjs` (`make
   parity-scan`): add, extend, rename and delete folders, issues cleanup, item
   rescans, metadata and cover edits, watcher on and off, compared with
   realtime events.
4. **Operational.** `make bench` (large-library latency, memory, cold start),
   portable `make browser-check`, multi-arch `Dockerfile.go` image published
   to `ghcr.io/<owner>/audiobookshelf-go` by CI.

**Remaining for the product track:**
- Real Android and iOS apps against a swapped-in install (owner; only their
  API call pattern is replayed).
- Decide whether Go runs `ANALYZE` at boot like upstream does. Without it,
  planner-dependent orderings (`/api/me` `mediaProgress`, `authorsWithCount`
  ties) can differ on installs whose statistics differ from the fixtures. It
  is a database side effect, so it needs an owner decision.
- Known behavioral gaps: share expiry is lazy (upstream uses timers and emits
  `share_closed`); the podcast auto-download cron is not rescheduled after
  `PATCH` media; `POST /api/watcher/update` still runs a full scan; series RSS
  feeds are not closed when their series is removed; genre/tag/author edits
  invalidate the filter-data cache instead of patching it. Not exercised:
  ebook-only and non-media files in scans, and watcher folder renames.

## Simplification and performance (post-replacement)

Assessment of 2026-11-02, after the product workstreams. The code is correct
against the trials but grew route by route, so its structure lags
`ARCHITECTURE.md`. An estimated 15-25% of the roughly 39k non-test Go lines
can go without changing an observable byte. This section orders that work.
It refines Phases 3-7 above with measured evidence and does not replace them.

**Rules for every item:** behavior-preserving refactors. Required checks are
those in `AGENTS.md`, including `make parity-replacement` and `make
parity-scan`, with no fewer steps. Refactor packets must lower at least one
ratchet metric and raise none. One package or concern per packet.

### Evidence (dated snapshot; regenerate before relying on it)

- Ratchet: `map[string]any` 1036 (catalog 218, userapi 147, podcasts 124,
  scanner 108, playback 89); hand-rolled `Handle` routers 13; `Set*`
  injectors 20; `writeJSON`-style copies 12; `EmitFunc` declarations 7.
- Duplicated helpers across packages: `writeJSON` x8; `number` x7;
  `sendStatus` x6; `nullableString`/`nullString` x9; `isAdmin`/
  `canAccessLibrary` x7; time helpers (`parseTime`, `millis`, `dateMillis`,
  `parseDate`) x10; plus `newUUID`, `parseUA`, `stringValue`, `writeError`.
  There are 27 `time.Parse` call sites, and 11 files carry their own
  DATETIME layouts, although `dbtime.Parse` exists. This session found real
  drift bugs from such copies (two progress-update copies, two device
  serializers).
- Largest files: `catalog/handler.go` 3.2k lines, `userapi/userapi.go` 2.5k,
  `playback/playback.go` 2.2k, `scanner/files.go` 1.8k.
- Unit-test gaps: `progressdto` (665 lines, the single progress-update
  implementation) has no tests; `sharing` and `userapi` are thinly tested.
  CI builds with `CGO_ENABLED=0`, so `-race` never runs.
- Performance (`make bench`, 2000 books, p95, upstream vs Go):
  - Go is ahead on cold start (996 vs 63 ms), RSS (328 vs 29 MB),
    collapse-series pages, item detail and play.
  - Go is behind on personalized (170 vs 201 ms), series lists (165 vs
    176 ms), `/api/me` (4 vs 8 ms), `items.duration` (40 vs 55 ms) and
    listening-stats (11 vs 15 ms).
- Structural performance smells:
  - The store uses `SetMaxOpenConns(1)`, so every request, reads included,
    queues on one connection.
  - About 40 call sites build item JSON one item at a time, at several
    queries per item.
  - 25 places read `server-settings` from the database per call, including
    `titlePrefix` inside loops.

### Work items, in order

1. **Leaf packages for the copies** (Phase 3). Create `httputil` (writers,
   `sendStatus`, `sendText`, error bodies), `ids` (UUID), `authz` (admin and
   library/tag/explicit access checks, characterized per Phase 7) and finish
   `dbtime` (one parser; replace the 27 `time.Parse` sites and the per-package
   millis/date helpers). Move the user-agent parser out of
   playback/sharing into one leaf.
   Exit: each helper listed above has one definition; `writeJSON`-style
   copies at 1 or fewer; the dependency rules add the new leaves.
2. **Tests for `progressdto`.** Table tests for `Apply` covering finished to
   `progress=1`, hide-from-continue-listening, `lastUpdate`, no-op saves,
   episodes and batch, each case cited to `User.js`/`MediaProgress.js`.
   Exit: every branch of `Apply` is covered.
3. **Settings cache and batched item loading** (catalog first).
   - Load server and library settings once, invalidated on settings writes.
   - Add a page-level loader that hydrates N items (books, authors, series,
     files, progress) in a constant number of queries, and use it for the
     library page, shelves, collections, playlists and series books.
   Exit: `make bench` shows no Tier 1 call slower than upstream at p95;
   query count per page is independent of page size (add a test).
4. **Typed responses** (Phase 6), in order catalog, userapi, podcasts,
   playback, scanner events. Inner opaque objects last.
   Exit: `map[string]any` at least halved from the evidence snapshot;
   remaining uses are documented opaque pass-through.
5. **One router and deps structs** (Phases 4-5). Smallest packages first,
   following the Phase 5 recipe.
   Exit: hand-rolled `Handle` routers 0, `Set*` injectors 0 (test seams
   become constructor options), `runtime.go` dispatch replaced by mux
   registration.
6. **Split the three largest files** along routes/serializers/queries. Do it
   as a side effect of items 4-5, not as a separate packet.
   Exit: no non-generated Go file over about 1000 lines.
7. **Database read pool** (Phase 4.5).
   - Keep one write connection and add a pool of 4-8 read connections.
   - Stay in rollback-journal mode: WAL changes the on-disk format that
     upstream reopens on rollback, so it is out of scope.
   - Audit read-modify-write paths first, each with a `-race` test.
   Exit: a concurrent-client benchmark (add to `make bench`) shows reads no
   longer serialize behind writes; replacement and scan trials unchanged.
8. **Race detector in CI.** Add a CGO-enabled `go test -race ./...` job
   (tests only; the shipped binary stays `CGO_ENABLED=0`).
   Exit: green `-race` job on master.

Items 1-3 are an estimated 2-3 days of work; items 4-8 are an estimated 1-2
weeks. Items 1 and 2 have no prerequisites.

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
