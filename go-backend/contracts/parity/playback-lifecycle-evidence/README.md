# Playback lifecycle evidence, 2026-11-13

The packet is architect-accepted after P2 re-review. This is not whole-project
parity or deployment acceptance. See [the current report](../playback-lifecycle-report.json),
[the task](../../../../docs/agent-work/archive/tasks/TASK-20261113-playback-lifecycle.md)
and [machine checks](architect-verification.json).

Current P2 follow-up: both architect findings are fixed and verified; re-review
is next. See [P2 commands, source hashes and results](p2-revision/verification.json).
Media uses the standard one-second WaitDelay to bound cancellation/post-exit pipe
draining, without a healthy-command runtime deadline or a playback timeout escape.
The public progress gate now requires nonzero updates for each owner before close.

- Inherited-pipe regression: red without the fix, green with it. The architect's
  original overlay also passes under race detection.
- Harness mutation regression: six failures against the old gate; 10/10 pass
  after, with zero skips. All mutation tests call the public gate.
- Fresh TS/Go progress: passes; 12 nonzero updates observed per owner on each side.
  No exact update count is required. `p2-revision/live-progress.json` is the raw run.
- Fresh full suite: 1,706 test/subtest runs, zero individual skips. Race, vet,
  configured lint, builds/cross-builds and disposable rollback pass.

The prior report is [archived](../playback-lifecycle-report-before-p2.json); its
source hashes and the original architect review remain historical, unchanged.
The ten-scenario matrix below predates P2, and was not rerun wholesale. The
following machine-check link is also historical; P2 verification above is current.

Ten real-media scenarios were exercised. Nine preserve the required pinned
upstream lifecycle behavior. The shutdown scenario records a source defect:
TypeScript leaves FFmpeg running after exiting; Go reaps it and removes its
stream directory. The harness reaps its own source orphan. The final independent
[process audit](process-cleanup-audit.json) found no disposable servers or FFmpeg
processes left running.

Current evidence:

- `final-live-observations.json`: the full ten-scenario run. Its exit 1 is retained:
  the only failed assertion wrongly demanded that the source reap FFmpeg on
  shutdown. All Go assertions passed. Do not relabel this raw report as green.
- `final-shutdown-observations.json`: corrected source-derived shutdown
  comparison, exit 0. Source leak recorded; Go cleanup required and verified.
- `final-seek-reset-observations.json`: corrected equivalent-state seek setup,
  exit 0. Both streams are open before seeking; nonzero reset reaches all three
  recipients once with matching payload and per-recipient ordering.
- `architect-verification.json`: actual commands, exit codes, source hashes,
  full-suite counts/skips, cross-build and rollback results.
- `base-red-final-tests.txt`: all seven lifecycle regressions fail on an isolated
  base archive. `fixed-package-tests.txt` and `fixed-race-tests.txt` pass.
- `lint-initial.txt`, `lint-second.txt`, `lint-accepted.txt`: both failed lint
  iterations and the final clean result. `rollback-fixed.txt` records successful
  pinned TypeScript reopening of Go-written progress, bookmarks and eight sessions.

The [observer](../../../tools/parity/playback-lifecycle.cjs) uses actual pinned
TS and Go servers, independent disposable SQLite/config/metadata copies, Socket.IO
clients and native FFmpeg. Three sockets test two owner connections and an
unrelated user. Generated AAC is real media. Copy failure is explicitly injected
at the subprocess boundary; its AAC retry executes real FFmpeg. Midstream failure
uses SIGTERM after the real process has produced segments. No TS/JS objects,
events or goldens are mocked or refreshed. Only random stream IDs, local stream
paths and irrelevant cross-socket scheduling are normalized; required payload
keys/types/values and per-recipient order remain asserted.

Historical provenance:

- [The initial summary](../playback-lifecycle-report-checkpoint.json) is archived.
- `baseline-observations.json`, `runtime-error-observations.json`,
  `startup-error-observations.json`, `fast-reset-observations.json`, `red.txt` and
  `red-race.txt` describe unchanged production before implementation.
- Initial six-second error windows interrupted upstream's AAC retry wait; the
  eighteen-second observations superseded them. `/usr/bin/false` tested different
  capability-preflight stages, so final startup acceptance uses a genuine failed
  executable start instead.
- Early harness failures involved UUID path comparison, async startup markers,
  readiness before seek, cross-socket delivery order and shutdown assumptions.
  Their provenance is recorded in the current summary; no product assertions were
  weakened to hide a Go defect.
- `repository-tests-final.txt`, `vet-final.txt`, `lint-final.txt` are historical
  checkpoint logs, not the accepted continuation results.

Source inputs remain pinned and unchanged at
`edb1f7efc92ecacdef51aafb4d9d5054ce10604f`; Go base is
`8990c2e61a1637a69effc4ca26e2df1748301889`. Existing suite-runner/latest-core/STATUS
overlays are untouched. Temporary media, server logs and original attempts stay
under `/private/tmp/abs-playback-lifecycle`; durable reports omit fixture credentials.

No commit, push, deployment or later ownership packet was performed. Both P2
findings are closed in [the architect review](../../../docs/verification/playback-lifecycle-architect-review.md).
Mobile-device acceptance and exhaustive media-format variants
remain outside the demonstrated representative cases.
