# Playback lifecycle architect review — 2026-11-13

Verdict after re-review: **approve the playback lifecycle packet**. Both P2
findings are closed; no blocking finding remains in the reviewed scope.
Whole-project parity and deployment acceptance are separate. No later ownership
packet has been dispatched.

## Re-review acceptance — 2026-11-13

Media now uses a named one-second `exec.Cmd.WaitDelay` to bound pipe draining
after cancellation/direct-child exit. This does not impose a runtime limit on a
healthy command. Execution remains media-owned; playback still owns its
supervisor, events and stream-directory cleanup. The live gate now requires a
nonzero incremental update for each owner before close, with no fixed cadence.

Independently rerun during re-review:

- `go test -race ./internal/media ./internal/playback -count=1`: exit 0.
- Original `TestArchitectCloseWithInheritedPipe` overlay with `-race`: exit 0.
- `node --test go-backend/tools/parity/test-playback-lifecycle.cjs` using Node 22:
  10/10 pass, zero skips. Missing either/both owners' updates and updates after
  close are rejected; recorded real traces pass.
- `git diff --check`: exit 0.

The five revised source/test/harness hashes match
`../../contracts/parity/playback-lifecycle-evidence/p2-revision/verification.json`.
Playback production files and the prior reset test are unchanged from the first
review. The fresh full-suite JSON log independently reconciles to 1,706
test/subtest runs, no failures and zero individual skips; its three package skips
are packages without tests. Fresh live progress evidence passes for TS and Go
and records 12 nonzero updates per owner on each side. Broader vet/lint/build,
cross-build and rollback evidence was inspected, not rerun in this re-review.
The prior ten-scenario suite was not rerun; the affected progress mode was.

Independent re-review logs are under
`/private/tmp/abs-playback-rereview.9KM6SJ`. `WaitDelay` bounds inherited-pipe
draining rather than terminating arbitrary wrapper descendants; fixture tests
clean up their own background children. This does not change the existing
process-ownership contract.

The original findings and failed probes below are retained as historical
evidence. The completed packet is archived at
[TASK-20261113-playback-lifecycle](../../../docs/agent-work/archive/tasks/TASK-20261113-playback-lifecycle.md).

## Scope and source of truth

Reviewed the pending playback/media production changes, lifecycle tests, live
observer and evidence against base `8990c2e61a1637a69effc4ca26e2df1748301889`.
The pre-existing suite-runner, latest-core-run and STATUS overlays are excluded.
At the initial review, all eight source/test hashes in
`playback-lifecycle-evidence/architect-verification.json` matched the files.
Current revision hashes are checked above. Pinned upstream/client inputs remain unchanged against
`edb1f7efc92ecacdef51aafb4d9d5054ce10604f`.

Playback should continue owning stream state, polling, events, retries and
cleanup. The narrow `media.RunStarted` hook correctly leaves process execution
and tool resolution in media. No new package, dependency or process framework
is needed. Fixed safe error text, numeric retry classification and cleaning up
the upstream shutdown leak are acceptable differences under ARCHITECTURE.md.

## Original findings — both closed by re-review

### P2 — cancellation can block close and shutdown indefinitely

`internal/playback/hls.go:490-491` now waits unconditionally for the supervisor.
That supervisor waits for `media.RunStarted`, whose `exec.Cmd.Wait` has no
post-cancellation pipe-draining bound (`internal/media/probe.go:125-135`).
CommandContext kills its direct child; an FFmpeg wrapper's surviving child can
keep stdout/stderr open and prevent Wait from returning. This stalls session
close, runtime shutdown and backup-restore shutdown. The old playback stop had a
three-second bound.

An isolated Go overlay test configured FFMPEG_PATH to a shell wrapper that
starts a background child inheriting its output pipes. After cancelling the
stream, Handler.Close still had not returned after four seconds. Killing the
fixture child released the wait; the probe cleaned up its own processes.

Required: bound cancellation/draining through media's standard exec lifecycle
controls, retain correct reaping/cleanup ownership, and add this regression.
Do not solve it only by abandoning a still-blocked supervisor in playback.

### P2 — live progress acceptance permits no incremental updates

`tools/parity/playback-lifecycle.cjs:50-51` checks only that a stream_progress
event exists. Initial zero-segment progress satisfies it. Payload validation
checks the events present but never requires a nonzero progress event.

Review probe: removed all 24 nonzero progress events from each side of the
recorded progress scenario, retaining the initial event, open and close.
`assertEvents` still passed for both TS and Go. This demonstrates a hole in the
live acceptance gate; the current recorded traces do contain incremental events
and the Go incremental-files unit test passes.

Required: for each owner socket, require a nonempty/nonzero incremental update
in the active-progress scenario, validate its ordering before close, and add a
harness regression proving that deleting those updates fails. Keep allowances
for scheduler-dependent snapshot counts.

## Compatibility and verification

| Contract | Review result |
| --- | --- |
| SQLite/schema and persistent formats | No production DB change in this packet; recorded rollback evidence matches the reviewed source hashes. Not independently rerun here. |
| Client event names, audiences and payloads | Accepted; restoration is cohesive and the strengthened progress gate passes. |
| Runtime cancellation and cleanup | Accepted; ordinary cases and the inherited-output-pipe regression pass. |
| Dependencies and package ownership | Accepted: existing media execution helper plus playback-owned lifecycle. |

Independent checks during the initial review (historical):

- `go test -race ./internal/media ./internal/playback -count=1`: exit 0.
- `git diff --check`: exit 0.
- Pinned source/client diff check: exit 0, unchanged.
- Isolated `TestArchitectCloseWithInheritedPipe` overlay: exit 1, cancellation
  regression reproduced. No test or production file was added to the checkout.
- Progress assertion counterexample: both assertions passed after every nonzero
  progress update was removed.

Review logs, the temporary overlay test and the counterexample result are under
`/private/tmp/abs-playback-architect-review.AYJ8Pz`. Full-suite, lint,
cross-build and rollback artifacts were inspected rather than rerun. The full
live suite was not rerun; its original shutdown difference and corrected targeted
reports remain accurately distinguished.

Next candidate: audio-task progress parity. Set its owner, write set, acceptance
criteria and budget at dispatch; require one ownership change and subsequent
architect review. It has not started. No production code was changed by either
architect review.
