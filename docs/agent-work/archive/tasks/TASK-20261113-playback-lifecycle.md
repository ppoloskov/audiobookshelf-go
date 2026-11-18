---
id: TASK-20261113-playback-lifecycle
title: "Playback stream lifecycle parity packet"
status: done
risk: medium
skill_pack_version: "1.3.0"
depends_on: []
approval: approved
gates: passed
spec_review: passed
craft_review: passed
correction_attempts: 0
---

## Scope
One ownership change: playback stream lifecycle only, including supporting tests,
live differential evidence and documentation. Stop for architect review before
another packet. No audio-task events, client changes, dependencies or deployment.

Architect re-review on 2026-11-13 **accepts the packet and closes both P2 findings**.
See [the review](../../../../go-backend/docs/verification/playback-lifecycle-architect-review.md).
The architect independently reran the race suite, original cancellation probe
and all ten gate regressions; all passed. This completed record is archived.
No later ownership packet has started; its budget and scope must be set at dispatch.

P2 response before re-review (historical):
The ownership design is accepted. Both P2 findings are now addressed and ready
for architect re-review: media bounds inherited-pipe draining with WaitDelay, and
the live gate requires nonzero updates for each owner before close. Red/green
regressions, the original architect overlay, fresh strengthened live progress,
full suite/race/lint/build/cross-build and rollback checks pass. The recorded
architect review remains unchanged; this response is not self-acceptance.
No next ownership packet has been dispatched.

Implementation checkpoint before architect review: the authorized fix is implemented. The original four red
regressions and three added playback regressions fail against an isolated archive
of the base and pass against the fix. Full repository, race, lint, build,
cross-build and rollback checks pass. Live acceptance covers ten scenarios: nine
contract-equivalent lifecycles and one source shutdown defect where Go performs
required process/directory cleanup. Architect review subsequently found the two
blockers above. Initial checkpoint evidence below is retained as history, not current state.

## Contract
The two contracts in `go-backend/ARCHITECTURE.md` govern. Pinned upstream is
v2.37.2 (`edb1f7efc92ecacdef51aafb4d9d5054ce10604f`). Trace actual emitters and
unchanged consumers for stream_open/progress/ready/error/closed/reset. Preserve
owner-user isolation, event payload values, restart state, cleanup and process
ownership. Do not infer semantics from event names or reproduce JS architecture.

## Acceptance criteria
- [x] Confirm current source/client requirements and gaps.
- [x] Capture failing regressions before the smallest playback-owned fix.
- [x] Verify real-media TS/Go lifecycle, recipients and cleanup.
- [x] Run targeted/race and required repository checks with actual exit codes.
- [x] Return verified packet or coherent checkpoint for architect review.

## Architecture review
Reuse playback's existing stream supervisor and events. The user authorized the
narrow media-owned successful-start notification on continuation; no dependency,
package or general process framework was added. All lifecycle state stays in
playback. Current API is `media.RunStarted`; existing `Run` callers are unchanged.

**Initial checkpoint (superseded): stopped with no production edits.** `internal/media/probe.go:1-3`
explicitly owns tool resolution/output. Its `Run` (`:109-124`) exposes only
completion, whereas upstream `Stream.js:297-309,218-233` starts initial progress
only after successful FFmpeg start. Duplicating binary/environment selection and
execution in playback would violate existing ownership. Exact initial event
timing is requested observable parity; the bundled web client ignores the
zero-segment progress payload, so this is not evidence of a playback failure by
itself.

Smallest concrete decision for architect review: permit a media-owned start
notification (for example `RunStarted(ctx, onStarted, args...)`) invoked only
after `cmd.Start` succeeds, sharing existing tool/error/cancellation handling.
Keep polling, lifecycle payloads, reset state and cleanup in playback. Retain
`Run` callers unchanged. This proposed API was subsequently authorized and added.

## Decisions
2026-11-13 continuation: user directed "Proceed with fixes using cheat agents
and minimal code changes" after reviewing the concrete dependency. Treat this as
authorization for the narrow media successful-start notification and the same
playback packet, using cheaper developer agents. No new ownership packet,
dependency, client change, commit, push or deployment is included.

Base `8990c2e61a1637a69effc4ca26e2df1748301889`, branch `master`.
Preserve pre-existing overlays: `go-backend/tools/parity/suite-runner.cjs`,
`go-backend/contracts/parity/latest-core-run.json`, untracked `go-backend/STATUS.md`.
Their initial hashes and diff are in `/private/tmp/abs-playback-lifecycle`.
No commit/push before requested architect review.

Initial/final overlay SHA-256 values (unchanged):

- suite-runner.cjs: `e158ee1ab8490d12c2491c7ac489ad83db0b48ae7830ac107ee7bd961150a504`
- latest-core-run.json: `8b852b33b4eb1cd55ba19106c63894801afe7dd80dca480424e5159375e297f1`
- STATUS.md: `2f47a134b3b24eea2b367d3cbc2dd8bcf47675cfe4f39277da854706929beff8`

Current source requirements:

| Contract | Pinned source / consumer |
| --- | --- |
| Owner-user events reach all its sockets, not other users | `server/objects/Stream.js:416-417`, `server/SocketAuthority.js:68-78` |
| Initial progress on process start, then 2s file polling | `Stream.js:218-233,297-309` |
| Progress counts cumulative unique segments, contiguous chunks with number singletons/string ranges, two-decimal percent | `Stream.js:151-214`; `client/components/app/MediaPlayerContainer.vue:419-430` |
| Open once after >6 segments, or on successful end before complete flag flips | `Stream.js:171-175,341-352`; web consumer `:439-441` only logs |
| Normal completion clears polling; do not invent ready on every end | `Stream.js:341-352`; ready consumer `MediaPlayerContainer.vue:449-455` |
| Fast reset can complete before its delayed startLoop, yielding later ready | `Stream.js:301-308,225-229,341-352`; observed in live fast-reset trace |
| Error payload is `{id,error}`, explicit close is scalar ID, remove files before event, manager detaches failed stream | `Stream.js:358-378`; `PlaybackSessionManager.js:353-356`; consumer closes matching stream `MediaPlayerContainer.vue:442-462` |
| Client destructures errorMessage although upstream emits error | `client/layouts/default.vue:171-173`; preserve source-derived payload, do not modify client |
| SIGKILL for cancel/reset is intentional, not error; copy failures may retry AAC | `Stream.js:317-337,381-414`; retry remains unverified |
| Reset keeps initialized flag and cumulative segments, global reset after requested missing segment | `Stream.js:135-149,398-414`; `server/routers/HlsRouter.js:75-92`; consumer `MediaPlayerContainer.vue:464-466` |

## Evidence
Initial checkpoint evidence follows; its red results and unchanged-production
statements describe the earlier checkpoint. Current continuation results appear
after it. Pinned upstream and clients remain unchanged (`git diff --stat edb1f7ef -- server client`, exit 0, empty).

New `go-backend/internal/playback/hls_lifecycle_test.go` contains four failing
regressions: real successful completion, real corrupt-media failure, incremental
segment filesystem observations and executable-start failure. Tests assert
open/progress/close/error values, owner audience and failed-stream cleanup.
Incremental coverage uses the existing sleeping process fixture, not real-media
acceptance or a TS/JS simulation. Actual media coverage is separate.

Commands from go-backend unless noted; environment uses
`GOCACHE=/private/tmp/abs-playback-lifecycle/go-cache GOTOOLCHAIN=local`.

| Command | Exit / result |
| --- | --- |
| Initial `go test ./internal/playback/... -count=1` without temporary GOCACHE | 1, environment: default cache write denied |
| `go test ./internal/playback -run TestStreamLifecycle -count=1` | 1, all four new regressions red; `/private/tmp/abs-playback-lifecycle/red.log` |
| `CGO_ENABLED=1 go test -race ./internal/playback -run TestStreamLifecycle -count=1` | 1, same four regressions; no race report; `red-race.log` |
| `go test ./... -count=1` inside sandbox | 1, environment: loopback binding denied; `repository-tests.log` |
| Same full test command with authorized loopback and native WebP FFmpeg prefix from TESTING.md | 1, only four new playback regressions failed; all other test packages passed; `repository-tests-loopback.log` |
| Same full command rerun on finalized tests | 1, only four new playback regressions failed; `repository-tests-final.log` |
| `go test -json ./... -count=1` with same authorized loopback/WebP environment | 1; 1,699 test/subtest run records, four new regressions fail, zero individual tests skipped. Four package skip records mean no test files (`cmd`, `db/dbtest`, `media`, `version`). Raw `repository-tests-json.log`; durable counts in playback-lifecycle-report.json |
| `go build ./...` | 0; `build.log` |
| `go vet ./...` | 0; `vet-final.log` |
| `golangci-lint run --config=../.golangci.yml --timeout=5m ./...` | 0, configured staticcheck/errcheck/unused/etc; `lint-final.log` |
| `gofmt -l .` | 0, no filenames |
| `git diff --check` (root) | 0 |
| `node --check go-backend/tools/parity/playback-lifecycle.cjs` (root) | 0 |
| `go run ~/.agents/skills/go-scope-ledger/scripts/check-ledger.go -tasks docs/agent-work/tasks -capabilities docs/agent-work/capabilities` (root) | 0, structure only; no parity acceptance |

No red-to-green result exists: production work stopped before the ownership
dependency. No assertions weakened, tests skipped or goldens refreshed. Rollback
and Linux cross-builds were not run (no production changes, commit or push).
Live baseline results and final checks are recorded below when available.

Live evidence is in
`go-backend/contracts/parity/playback-lifecycle-evidence/`, produced by
`go-backend/tools/parity/playback-lifecycle.cjs` using independent disposable
TS/Go installations, real generated AAC media and real FFmpeg. Two sockets for
the owner plus one unrelated user observe recipients. Reports preserve event
arguments, timestamps, play values, HLS delivery, files and row counts.
The summary is `go-backend/contracts/parity/playback-lifecycle-report.json`.
Durable check logs use `.txt` names in the evidence directory; original `.log`
paths above refer to `/private/tmp/abs-playback-lifecycle`.

- Short successful completion: both return play 200, equivalent play method,
  duration and HLS track values, and playable playlist/segments. TS emits
  initial progress, one open and one explicit close per owner socket. Go emits
  none. Neither emits ready on ordinary completion.
- Long throttled run: TS emits incremental chunk/percent progress and opens;
  Go emits none. Both remained incomplete when explicitly closed at 25 seconds;
  this is progress/active-close evidence, not successful long completion.
- Corrected runtime failure: TS emits error to both owner sockets, removes
  stream files and serves HLS 404. Go emits no error, retains files and serves
  its playlist 200. Corrected startup/preflight failure shows the same contrast.
- Error observation originally ended at 6 seconds and interrupted upstream's
  AAC retry wait; those two traces are superseded, not passing error cases.
  Corrected 18-second observations preserve the original runs' provenance.
  `/usr/bin/false` probes startup/preflight failure; the Go missing-interpreter
  regression separately probes executable-start failure.
- Fast reset: TS emits delayed initial progress and ready after reset; Go emits
  none. The requested segment was zero, so neither broadcasts reset (upstream
  suppresses a zero start). Nonzero reset broadcast is source/existing-test
  evidence, not newly demonstrated by these live runs.
- Explicit closes return 200/OK, remove stream directories and serve subsequent
  HLS 404 on both. Unlistened sessions create no playbackSessions rows. The seek
  setup retains one progress row on both. These checks do not prove all process
  descendants exited; deeper shutdown/cancel races remain acceptance work.

Observation-run exit 0 means capture succeeded, **not differential parity**.
There is no passing TS-versus-Go lifecycle comparison. Report skip arrays are
empty; unexercised requirements above remain explicitly unverified rather than
being counted as success. See the evidence README for commands and provenance.

### Authorized continuation verification

Current durable machine-check evidence:
`go-backend/contracts/parity/playback-lifecycle-evidence/architect-verification.json`.
Its command list records actual exit codes and source hashes; accompanying
`fixed-package-tests.txt`, `fixed-race-tests.txt`, `base-red-final-tests.txt`,
`lint-accepted.txt` and `rollback-fixed.txt` retain the key logs.

- Isolated `git archive 8990c2e... go-backend` plus the new playback lifecycle
  test file: all seven lifecycle tests fail, exit 1. This reproduces the three
  added regressions without mutating the shared working tree.
- Fixed playback/media packages and CGO race run: exit 0, all tests pass.
- Full `go test -json ./... -count=1` with the documented native WebP FFmpeg:
  exit 0, 1,704 test/subtest run records, zero individual skips. Three no-test-file
  package records are `cmd/audiobookshelf-go`, `internal/db/dbtest`,
  `internal/version` (media now has tests).
- Build, vet, configured golangci-lint (including staticcheck/errcheck), gofmt,
  diff whitespace, Linux amd64 and arm64 CGO-disabled builds: exit 0.
- Rollback-only trial with a fresh disposable installation: exit 0. Pinned
  TypeScript reopens Go-written data and matches progress, bookmarks and eight
  session rows. No real installation was used.
- Initial lint attempts exited 1: first errcheck rejected an unchecked error
  channel receive; then staticcheck rejected a redundant blank assignment.
  Final code documents the intentional cancellation discard with one errcheck
  directive and passes both linters. Earlier failed logs are retained.

Production behavior differences from the base: restored owner-user lifecycle
messages, incremental chunks, asynchronous missing-input failure, terminal
failure detachment/removal, one normal close after reaping, cumulative seek
segments and a single eligible AAC retry. No schema, clients or dependencies
changed. Differences from pinned TS deliberately retained by the simpler design:
fixed safe error text; immediate reaping rather than a ten-second stale-process
wait; numeric exit-code 1 rather than JS string-prefix matching (which also
matches 183); shutdown reaps FFmpeg and removes ephemeral streams rather than
leaving an orphan. The web client ignores zero-segment progress and does not
consume the `error` text; it closes using the stream ID. Server shutdown has
already disconnected its clients. None of these differences requires duplicating
TS classes, coercion or a source process leak.

### P2 revision evidence and scope

Current commands, actual exit codes, source/binary hashes and logs:
`go-backend/contracts/parity/playback-lifecycle-evidence/p2-revision/verification.json`.
The pre-P2 report is archived as `playback-lifecycle-report-before-p2.json`.

- Inherited-pipe regression without WaitDelay: exit 1, close exceeds four seconds.
  Fixed media/playback tests and race checks pass. The architect's unchanged
  `TestArchitectCloseWithInheritedPipe` overlay also passes with `-race`.
- Production adds a named one-second `cmd.WaitDelay` in media. Its bound starts
  after cancellation or direct-child exit; healthy commands have no runtime cap.
  Playback retains supervisor/event/filesystem ownership and waits for media.
  This does not process-group-kill arbitrary wrapper descendants; tests explicitly
  clean their own inherited-pipe child after proving close returns.
- The public `assertEvents` mutation regression exits 1 against the pre-fix gate
  (six failures) and 0 against the fix (10/10, zero skips). Missing updates for
  either owner or both and updates moved after close are rejected. No fixed
  cadence/count is required. No recorded traces or goldens were refreshed.
- Fresh real TS/Go `--modes progress`: exit 0, each owner receives 12 nonzero
  updates per implementation before close. Payload, recipient, HTTP, persistence
  and cleanup assertions pass. Twelve is observed, not required by the gate.
- Fresh full suite: exit 0, 1,706 test/subtest runs, zero individual skips; only
  the three no-test-file packages are skipped. Vet, configured lint, native/Linux
  builds, formatting and diff checks pass.
- Fresh disposable rollback: exit 0; TS reopens Go-written data, matching progress,
  bookmarks and eight session rows. The older ten-scenario live suite remains
  historical; the affected progress mode, not all ten modes, was rerun for P2.

## Review: contract
Both P2 counterexamples are prevented, with independent red/green proof, the
original architect overlay and a fresh live comparison. Known whole-packet
differences previously accepted by the architect are unchanged. Await re-review.

## Review: craft
The revision uses the standard exec lifecycle bound and one owner-local gate
predicate. No supervisor escape timeout, process framework, dependency, client
change or unrelated refactor was added. Mutation tests call the public gate;
pre-fix failures cannot be hidden by testing a disconnected helper.

## Prior worker review: contract (superseded by architect findings above)
Independent current-source review passes: registration precedes asynchronous
startup; retry publication is atomic relative to seek/close; timers are owned and
stopped; close waits for process reaping before its terminal event. Root review
also corrected initial forced-codec AAC reset timing (500ms; only retry uses 3s).
Live acceptance now verifies completion, incremental progress, runtime and exec
startup errors, injected copy failure with real AAC recovery, fast reset, nonzero
global reset, close/cancellation, shutdown and actual midstream SIGTERM failure.
Nine scenarios preserve the source contract; shutdown deliberately reaps/removes
what TS leaves running. Source `server/Server.js:558-570` stops sockets/HTTP but
not playback. Raw source failure and harness cleanup are retained, not counted
as an identical shutdown trace. No production contract finding remains open.

## Prior worker review: craft (superseded by architect findings above)
Independent craft review passes. Media reuses one command implementation;
playback uses typed known payloads, one file-progress reporter and explicit
process/timer ownership. No dependencies, TS/JS emulation, client changes or
unrelated refactors were introduced. The existing reset unit assertion now reads
the typed payload; live tests independently assert JSON keys/types/values.

## Blockers / next action
Architect re-review accepts this packet. No blocking finding remains in scope.
No commit, push or deployment was performed as part of review.
Representative live media are generated AAC book fixtures; device acceptance
and exhaustive media-format/multitrack/podcast variants are not implied.
Suggested subsequent packet is audio-task lifecycle; define its owner, write
set, budget and acceptance criteria at dispatch. It has not started.

## History
- 2026-11-13: Architect re-review accepted both P2 fixes, independently reran
  targeted race/original-probe/gate tests, checked current evidence hashes and
  raw full-suite counts, and archived this completed packet. Whole-project
  parity remains unproven; no next packet was dispatched.
- 2026-11-13: Completed both P2 revisions with red/green, original overlay and
  fresh race/live/repository/rollback evidence. Awaiting re-review; no acceptance,
  commit, push, deployment or next ownership packet is implied.
- 2026-11-13: Resumed the same packet for the architect's two P2 revisions.
  Bounded agents own media cancellation/pipe-drain regression and progress-gate
  counterexample regression. No redesign or subsequent owner is authorized;
  existing review and prior evidence remain unchanged.
- 2026-11-13: Resumed implementation after user authorized the proposed fix;
  prior red/checkpoint evidence retained unchanged for provenance. Two bounded
  cheaper agents own playback/media implementation and live acceptance harness.
- 2026-11-13: Started single playback packet under installed skill pack 1.3.0;
  existing rewrite tracking retained. This task is the packet record, not a new
  project parity inventory.
