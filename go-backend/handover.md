# Go backend handover

Updated 2026-11-18.

## Current direction: one upstream-first verification machine

The active goal is delivery of the test machine certified against pinned upstream
v2.37.2. Run `make verify VERIFY_ARGS="-target upstream"` for acceptance. A
separate replacement assessment owns the Go comparison and rollback against
Go-written data. [Delivery plan](ROADMAP.md#verification-delivery-plan)

The second `store` slice added four upstream scenarios. Fresh init creates the
26 named tables and seven triggers and preserves the new root identity and
login across reopen. A mobile-shaped offline upload persists a 10-second
session and its history while preserving the newer 27.5-second position.
Disjoint settings patches persist as one merged document across reopen.
An invalid `allowedOrigins` patch leaves the row, timestamp and surfaced
settings unchanged. A simulated partial write failed the rejection test on
that row. The seven store scenarios passed two packet runs and integration.
Tagged store vet and lint passed. Fifteen store coverage gaps remain.

The current integrated upstream-only run passed 974 of 975 declarations across
18 of 34 packets with stable inputs. `library_data/issues-remove-twice-and-without-issues`
timed out waiting for the missing-item flag and issue count. The failed run
stored `isMissing=1` and logged scan completion. Three isolated reruns with
V8 instrumentation passed. Take `library_data` next to reproduce the failure,
observe row and filter-count state separately, and make the DELETE issues
fixture deterministic while retaining scan coverage for its owner. Its three
tagged lint findings can be fixed in the same packet.

The full report records 2,037 overlapping coverage and review gaps, including
255 unbound invariants across 197 of 345 requirements and only one current
review out of 34. Eight tagged lint findings remain. The independent inventory
receipt is absent. These counts measure separate obligations and should be
reported separately from scenario pass counts.

Two disposable settings probes guide the remaining store work. Upstream kept
both changes in 180 pairs of concurrent disjoint patches. A temporary SQLite
trigger that aborted the next settings upsert closed the HTTP connection and
exited the server with status 1 in two runs. Both kept the persisted document
and timestamp intact and loaded the original language after removing the
trigger and reopening. Add reviewed shared scenarios for concurrent merges and
durable failure recovery. Keep the process failure and API contract decisions
explicit when characterizing the failure.

The first `store` packet adds three upstream scenarios. Repeat boot checks the
26 table and seven trigger names, unchanged DDL, a listener row, the fixture
reader file and selected metadata. Two write-and-reopen scenarios check exact
bookmark and progress values and identities, SQLite TEXT/UTC/null encodings,
and a catalog title in the book row, trigger-maintained library item title and
written `metadata.json`. Pinned upstream reopens the stopped writer's install
and reads the new values through the API. An isolated counterexample changed
the stored resume position from 12 to 1 second between the stop and reopen.
The test failed on that row. Three scenarios passed two worker runs and an
independent architect run. Tagged packet lint and vet passed.

The first store slice's plan discovered 971 scenarios across 18 packets and reported 2,041
overlapping coverage and review gaps. The register contains 345 requirements
and 647 invariants at that point. The `store` packet had 21 declared gaps and no accepted
review. Fresh current-schema initialization, offline session rows after reopen,
settings persistence and concurrent patches, rejected/failed writes and broader
column semantics remain open. The 968-scenario integrated result below predates
the `store` addition.

The November 18 item-write packet reconciled 50 unknown declaration bindings and
added 13 source-backed invariants. All 77 declarations now validate. A scoped
architect run passed all 77 against pinned upstream. A temporary negative test
changed the expected surviving shared-playlist count in `delete-soft-cascade`
from one to zero and failed on that stored result. Review corrected overstated
claims about orphan cleanup on soft delete and the recipients of podcast updates.
There are 32 remaining declaration gaps, including `items_updated`, restore
socket behavior and missing edge and lifecycle evidence. Soft-delete orphan
cleanup needs a timing-aware assertion. Existing `items_updated` evidence in
`author` and `library_data` is a lead for the cross-packet audit.

The November 18 playback-sessions packet resolved 21 unknown declaration
bindings across R059, R060 and R146–R148. The 31 scoped upstream scenarios
passed twice after cleanup, and the architect's scoped run passed all 31.
The shared fixture changes listener progress during playback and close. GET now
checks the session created by the play response, and sync requires the original
start position to survive the update. A temporary negative test made same-device
resume expect 32 seconds after saving 33 and failed on that exact difference.
Tagged vet and packet lint passed. The discovery check still reports 77 packet
gaps, including offline upload, admin session routes, and separately owned socket
and client-flow evidence.

Full plan validation now succeeds with 968 declarations across 17 of 34 packets
and a register of 345 requirements and 646 invariants. The plan reports 2,048
overlapping coverage and review gaps. There are 257 invariants without a binding
across 198 requirements. Only `auth` has a current accepted review, and the
independent whole-contract inventory receipt is absent.

The integrated upstream-only baseline completed with stable input digests.
All 968 declared scenarios passed across 17 owner packets, with no runner
anomalies. The report says upstream `partial` and counts 2,050 overlapping
gaps, including the missing independent inventory receipt and machine health.
Health failed solely on eight tagged lint findings in `auth`, `browse`,
`library_data`, `user_admin` and `user_progress`. Mapping, Go build/test/vet,
verifier and harness race tests, both Linux builds, 77 Node self-tests and 363
upstream unit tests passed. The standalone v1.3.0 ledger validator also rejects
60 inherited invariant classifications across `browse`, `author`, `user_admin`
and `admin_settings`. Clean those up within their respective owner packets.

Use mobile session payloads with more than three seconds of listening for
reopen checks because upstream boot prunes short rows. The same shared scenario
can later use Go for the writes and pinned upstream for reopen.

The November 18 `admin_files` repair registers the filesystem browse and path-existence checks. The worker and architect each ran all 12 scenarios twice against upstream. These cover the JSON values and types used by the folder chooser and uploader, permission and validation order, repeat and changed-disk behavior, and unchanged SQLite rows and media/metadata after rejections. Take the snapshot after fixture logins and exclude routine server logs. The architect removed unused watcher and upload setup helpers flagged by packet lint. Windows drive enumeration is outside the published Linux platform coverage. The app-root directory filter still needs an assertion. The two route IDs have no declaration-facet gaps. The rest of `admin_files` has 52 gaps across cache purge, item-cache purge, year stats, server stats, upload, watcher update and logger data. The packet has no acceptance receipt.

Run `make verify` with Node 22 and the FFmpeg prefix in [TESTING.md](TESTING.md). The November 18 run exited during plan validation on `item_write/book-all-metadata-fields` because `route-r053` has no `INV-R053-BOOK-META-001`. The raw discovery output contained 968 declarations across 17 of 34 packets. Fifty `item_write` and 21 `playback_sessions` declarations referenced missing invariants. The 345 requirements and 620 recorded invariants still need coverage reconciliation, including web and mobile protocols and the independent whole-contract audit. Machine health, the full upstream scenario run, the shared Go comparison and rollback were not reached by `make verify`. Its displayed zero gaps at this stage means gap calculation did not run. The separate capability ledger validator also rejects inherited invariant class names in several other packets.

The item-write packet resolved that first discovery blocker in the next invocation.
Keep the approved ebook-engine and older-schema-migration exclusions.

The earlier November 14 integration ran the 216 scenarios then declared on pinned upstream v2.37.2. They passed in nine packets and touched 41 of 345 requirements and 88 of 478 invariants. The verifier recorded 2,530 overlapping coverage and review gaps, zero accepted packet reviews, and no whole-contract discovery review. Go assessment and rollback remain blocked. The two exclusions are ebook engine internals and older-schema migrations. [Integration report](docs/verification/unified-verification-integration-20261114.json)

`make verify` ran with Node 22.23.4, the WebP FFmpeg prefix from TESTING.md and Go 1.27.0. Machine health passed, including lint, verifier/harness race tests, tagged vet, Linux builds, 69 Node self-tests and 363 upstream unit tests. Measured inputs matched before and after. The earlier eight-assertion report documents the November 13 foundation only.

Use packet names exactly as declared in `tests/replacement/packets.json`. A symlink alias or a hyphen in place of a canonical underscore disrupts discovery. Discovery checks every declaration against the canonical register, so update records and regenerate the mapping when introducing an invariant. The integration corrected the shared item-read expectation for an administrator requesting an unshared book: upstream returns an explicit `mediaItemShare: null`. Progress-list and repeated-hide checks now assert complete row values and unchanged timestamps. The verifier requires the test process, package, parent test and scenario evidence to agree before it can certify a run.

The November 15 partial comparison covered 12 of 34 packets and reported 96 Go failures. It ran outside the upstream-first gate. [Partial comparison](docs/verification/partial-run-20261115/README.md).

On November 17 the playlists package compiled and ran. Added checks cover persisted order and ownership across restart, rollback and retry after a failed SQLite link insert, and expanded media fields the client consumes. That upstream run stopped at the empty admin-files constructor repaired on November 18.

## Latest inventory packet: Q00 (architect accepted)

See [mapping](docs/verification/replacement-requirement-map.md),
[ordered owner queue](docs/verification/replacement-gap-queue.md),
[source/client reconciliation](docs/verification/replacement-inventory-reconciliation.md),
and [task ledger](../docs/agent-work/tasks/TASK-20261113-replacement-contract-map.md).
The register has 345 requirements/425 invariants, all 226 route sites, 77 socket
name/direction records and 25 persisted model files, plus non-HTTP formats,
configuration and lifecycle obligations. Every row has an assigned owner packet.
Named tests are assertion leads; historical verification was reopened for the
rewritten code. Accepted playback evidence below remains valid within its scope.

New discovery: immediate metadata embed emits queue true/false in Go although
source emits only waiting/dequeue transitions. Q01 tools must characterize and
fix the consumer-visible trace; track/task progress is also missing proof.
Notification event payload is the entire settings object; backup_applied omits
its source argument. Preserve these source-derived requirements. The inherited
core STATUS report remains FAIL and was not regenerated by this inventory.

Q00 originally handed off a bounded Q01 tools
packet with exact file ownership, source/client invariants and the queue budget.
Do not start Q02 or another domain in the same invocation. Reproduce generator:
`node go-backend/tools/replacement-contract-map.mjs --check`; product tests and
strict all-verified ledger are required later, not claimed by mapping success.
Independent contract/craft reviews accept Q00 after source-semantic and renderer-test fixes. Mapping/ledger, 13 Python tests, gofmt/vet/lint and all 1,706 Go test/subtest records pass on the current checkout (zero individual skips); these checks do not verify the open requirement matrix. Review/check results are in the task ledger. No production code changed in Q00;
existing playback/harness/status overlays are preserved and must be reviewed
separately before a combined commit/push. No live deployment was performed.

## Latest accepted packet: playback lifecycle

Architect re-review on 2026-11-13 **accepts this packet and closes both P2 findings**. The
[review](docs/verification/playback-lifecycle-architect-review.md) accepts the
ownership split and documented TS differences. The original unbounded close
wait and vacuous progress gate are fixed. Media now sets a one-second
`exec.Cmd.WaitDelay` for cancellation/post-exit
pipe draining, without limiting healthy transcode duration. Playback cleanup
ownership is unchanged. The gate now requires incremental progress for each
owner before close. Both regressions fail without their fixes and pass after.
The original architect overlay passes; fresh live progress, full 1,706-test suite
(zero individual skips), race/vet/lint/build/cross-build and rollback checks pass.
Current commands/hashes/logs are in
[P2 verification](contracts/parity/playback-lifecycle-evidence/p2-revision/verification.json).
The architect independently reran the media/playback race suite, original
cancellation probe and 10/10 gate tests; all passed. The next candidate is
audio-task progress, with owner/write set/budget to be set at dispatch. It has
not started. Acceptance of this packet does not establish whole-project parity.

See [TASK-20261113-playback-lifecycle](../docs/agent-work/archive/tasks/TASK-20261113-playback-lifecycle.md)
for source references, evidence and exact continuation. The user authorized the
narrow media start notification and playback fix using cheaper agents. Those
changes are implemented; original red evidence is preserved. Architect acceptance
and whole-project parity are separate claims.

`media.RunStarted` reports successful process startup and shares the existing
tool resolver/execution code. Playback owns timers, events, retries and cleanup.
Sessions are registered before startup, failed streams detach and remove output,
and explicit close/shutdown waits for process reaping before emitting one close.
Missing inputs now produce an asynchronous error after play returns 200. Eligible
copy failures retry AAC once. No dependency, schema or client change was added.

Gotchas from current source: lifecycle events target every socket of the owning
user, while `stream_reset` broadcasts globally. Normal completion clears the
timer and does not normally emit `stream_ready`; a fast reset can finish before
its delayed startLoop and later emit ready. The web client reads `errorMessage`
but upstream emits `error`; do not change clients or infer new payload semantics.
The former silent-error/retained-playlist defect is fixed. Error payloads retain
`id` and `error`, using fixed safe text rather than exposing internal paths.
Go reaps an exited process immediately rather than reproducing upstream's
ten-second retry wait. It tests numeric exit code 1, without the upstream string
prefix bug that also matches 183. Failed-stream timers stop instead of surviving
cleanup. These differences do not change the client decisions documented in the
packet.

Initial implementation checks (historical): full JSON test run passes (1,704 test/subtest run records,
zero individual test skips); playback/media race, vet, configured lint, native and
Linux amd64/arm64 builds pass. Rollback passes: pinned TypeScript reopens the
Go-written database and matches progress, bookmarks and all eight session rows.
Live acceptance results and exact commands are maintained in the packet report.
Ten real-media lifecycle scenarios are demonstrated: nine match the required
upstream contract; shutdown verifies Go reaps FFmpeg/removes streams where
upstream leaves its child alive. `server/Server.js:558-570` stops sockets and
HTTP without stopping playback. The harness records and then reaps that source
orphan. This is a documented cleanup improvement, not an identical trace or a
skipped failure. Architect acceptance is recorded above; no other packet has started.

Keep existing overlays untouched: suite-runner.cjs, latest-core-run.json and
untracked STATUS.md. Base is `8990c2e61a1637a69effc4ca26e2df1748301889` on master.
Default Go cache writes and sandbox loopback binding were denied; temporary
GOCACHE plus an authorized loopback test rerun resolved those environment issues.

## Where things stand

- The greenfield rewrite between the two contracts (SQLite compatibility,
  client protocol) is complete in code. Packages, APIs and the request
  pipeline are in [ARCHITECTURE.md](ARCHITECTURE.md). The wave list, all done,
  is in [ROADMAP.md](ROADMAP.md).
- Behaviour differences from the pre-rewrite Go build (baseline `7051b3e5`) are
  in [docs/CHANGES.md](docs/CHANGES.md).
- The tree is `cmd/audiobookshelf-go` plus 32 packages under `internal/` and
  `db/dbtest`. No old shared package, sqlc or ratchet/depguard tooling remains.
- Development is on `master`. Live deployment is separately authorised.

## Size, measured

- Before the rewrite: 39,740 hand-written production lines plus 3,243
  generated, in 57 packages; 52 test packages green.
- After cleanup pass 3: 35,863 hand-written, none generated.
- Counted on 2026-11-12 (`internal/` and `cmd/`, without contracttest and
  dbtest): 34,918 production lines, 24,053 test lines.

## Verified

- `go build ./...` and `go vet ./...` are clean (run 2026-11-12).
- On 2026-11-12 `go test ./...` passed in every package except
  `internal/contracttest`: its cover and author-image journeys
  (`TestJourney/item.*.cover`, `podcast.cover`, `author.image.*`) returned 500.
  This host's ffmpeg has no libwebp; use the prefix in [TESTING.md](TESTING.md).
  I did not rerun with it.
- The rollback check (`replacement-trial.cjs --rollback-only`, see
  ARCHITECTURE "Testing") is required before every push. This update did not
  run it; the last result is whatever the latest push recorded. Run it again
  before claiming rollback.

## Open follow-ups

- `internal/logging` still has the printf helpers (`Infof`, `Errorf`, `Fatalf`,
  ...). Its doc comment says "Transitional ... delete when unused". They are
  used by `cmd/audiobookshelf-go`, `internal/server` and
  `internal/server/middleware.go`; move those to `slog` and delete them.
- `internal/library` has the package doc "Package libraries ..."; rename to
  match the package name.
- `internal/text` and `internal/media` have 12 stray `*-E` files (sed backups, tracked in git);
  delete them.
- `Makefile` target `test-ts-diagnostics` names packages that no longer exist
  (`httputil`, `catalog`, `textclean`); `docs/survey` and `contracts/README.md`
  still describe the old code.
- Open decisions, default no: run `ANALYZE` at boot (upstream does); port
  upstream's boot-time `cleanDatabase`.
- Not proven: device acceptance of the mobile apps; mobile evidence is in
  `contracts/mobile-client-audit.md` and `contracts/mobile-compat-gaps.md`.

No `TODO`, `FIXME` or `TEMPORARY` markers remain in `internal/` or `cmd/`
(`grep -rn "TODO\|TEMPORARY\|FIXME" internal cmd`).

## Closed since the earlier handover

- CORS middleware exists (`internal/server/middleware.go`).
- The scanner uses `cronspec.New()`; a bad library cron is logged and skipped.
- Filter cache is invalidated on series rename and podcast creation.
- `(*db.DB).SQL()` is gone.

## How to continue

1. Read [AGENTS.md](AGENTS.md), then ARCHITECTURE "Conventions".
2. Change code on `master` with a test beside it. Keep a behaviour only for a
   persisted value or a real client; record any change in `docs/CHANGES.md`.
3. Run `make check` (root), with the FFmpeg prefix from TESTING.md, and `-race`
   for locks and goroutines.
4. Before a push: the rollback check and both Linux cross-builds
   (`GOOS=linux GOARCH=amd64|arm64 CGO_ENABLED=0 go build ./cmd/audiobookshelf-go`).
5. Keep README, AGENTS, ROADMAP and this file current.
