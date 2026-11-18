---
id: TASK-20261113-unified-verification
title: "Unify replacement verification and establish upstream-first coverage proof"
status: in_progress
risk: medium
skill_pack_version: "1.3.0"
depends_on: []
approval: approved
gates: blocked
spec_review: passed
craft_review: passed
correction_attempts: 0
---

## Scope
Owner: verification tooling. User requests one verification machine, primarily Go
with .mjs only where necessary, proving upstream coverage before using the same
scenarios to assess Go. This supersedes the immediate Q01 tools dispatch. Preserve
all existing evidence/tests until their required assertions are migrated; do not
call wrapping old commands behavioral unification. No application production edits.
The 2026-11-18 owner clarification limits this task to delivery of the ready test
machine. Certify upstream with `make verify VERIFY_ARGS="-target upstream"` and
deliver the runnable dual-target suite. Go comparison and rollback against
Go-written state are subsequent work.

## Contract
Pinned v2.37.2 source and existing replacement contract are authoritative. User
confirmed on 2026-11-13: retain ebook-engine and older-schema-migration exclusions.
All other discovered in-contract requirements remain in the denominator.
One canonical capability register; same backend-independent assertions for both
servers; one report/verdict. Distinguish requirement/invariant/state coverage,
source execution coverage and internal Go regression tests. Unknown, omitted,
skipped, zero-case, stale-source or missing-assertion proof fails closed. No 100%
claim until independent inventory and executable assertion evidence both justify it.
No Python in the unified active test/generation path; migrate the newly introduced
Python mapping and inherited scanner to .mjs with output identity checks.

## Acceptance criteria
- [x] A single documented command reports upstream proof and gates subsequent Go comparison.
- [ ] Shared scenarios use public HTTP/socket/files/database effects, independent of Go internals.
- [ ] Coverage names exact IDs and proof facets, preserving exclusions and recording all missing proof.
- [ ] Empty/stale/skipped/missing-source or unasserted execution cannot earn a PASS/100%.
- [ ] Existing assertions remain runnable; competing historical verdicts are not merged as proof.
- [ ] Active mapping/inventory tooling and tests use Go/.mjs, with Python removed from that path.
- [ ] Current measured coverage and remaining closure work are factual; full upstream 100% is not fabricated.

- [ ] Every defined upstream requirement/invariant is independently characterized and verified before the same suite can certify the Go replacement. **Not complete.**

## Architecture review
Existing suite-runner requires both servers and mostly compares two outputs; it
cannot independently certify upstream. Existing contracttest shape recordings do
not assert full values/state. Introduce a small stdlib Go coordinator and shared
black-box Go tests, with .mjs for running pinned Node/source-only functionality.
Reuse existing fixture and pinned-source checks; no added dependencies. Do not
port every legacy harness at once or build a second replacement feature register.
Exact tool/file boundaries and acceptance are recorded here before implementation.

## Decisions
Write set: go-backend/tools/{verify/,source-inventory.mjs,source-inventory.test.mjs,
replacement-contract-map.mjs,replacement-contract-map.test.mjs}; one shared Go
black-box scenario package; Makefile; Go CI workflow and Docker verification job (reuse the same machine; image/publish logic untouched); canonical task/continuity
and verification docs. Python map/scanner files may be removed after matching
Node replacements pass. Historical reports retain their original commands.
No Go feature-gap packet, dependency installation, deployment or upstream edits.

## Evidence
2026-11-18 follow-up dispatch. `store` remains the sole owner packet for this
invocation. The Terra owner adds fresh current-schema initialization, an
offline listening write and reopen, and committed and rejected settings writes.
The architect verifies concrete API and row values and runs the integrated
upstream-only verifier on the completed slice.

Store follow-up result. Four new scenarios cover fresh schema and root identity
through reopen, offline session history and precedence of newer progress, merged
settings across reopen, and validation rejection with unchanged persisted and
surfaced settings. Seven scenarios passed twice in the packet and in integration.
A temporary mutation confirmed partial changes after a 400 fail the settings
check. Separate probes observed both updates in 180 concurrent patch pairs and
repeatably preserved disk state across a forced SQLite write failure, despite
upstream exiting without an HTTP response. Fifteen store gaps remain.

Integrated upstream-only result. 974/975 passed across 18 packets with stable
inputs and 2,037 overlapping gaps. `library_data/issues-remove-twice-and-without-issues`
failed in scan setup. The failed installation persisted the missing flag and three
isolated reruns passed. Eight inherited tagged lint findings and 33 incomplete
packet reviews remain. The whole-contract inventory receipt is absent.

2026-11-18 dispatch. `store` is the sole owner packet for this invocation.
The packet is listed in `packets.json` and has no scenario directory. The Terra
owner covers the three assigned capabilities, starting with current-schema boot
and a public API write followed by a stopped-install reopen on pinned upstream.
The architect reviews rejected and changed state, verifies the packet and owns
the generated map. Older-schema migration and Go comparison remain outside this
run.

Store boot/reopen result. The new packet has three upstream scenarios. Two
worker runs and an independent architect diagnostic passed. A temporary overlay
changed a saved progress row between shutting down the writer and reopening on
pinned upstream. The verification failed on the altered row. Tagged lint and
vet, mapping generation and drift check passed. The full plan validates with
971 declarations in 18 owner packets and 2,041 overlapping gaps. The `store`
packet still has 21 gaps and no acceptance receipt. Full integrated validation
of the expanded suite has yet to run.

2026-11-18 dispatch. `playback_sessions` is the sole owner packet for this
invocation. Current scoped discovery fails on `play-method-direct-or-transcode`.
The Terra worker owns its shared scenarios and manifest-assigned records. The
worker reconciles inherited bindings against pinned source, runs the packet
upstream and reports untested session behavior. The architect owns the map,
independent review and the integrated upstream-only gate.

Playback-sessions result. Reconciled 21 missing declaration bindings, adding 13
source-backed invariants. All 31 scenarios passed two consecutive upstream runs
after cleanup, and the architect's scoped diagnostic passed. The worker corrected
two checks that assumed seed progress after earlier scenarios had changed it.
The architect verified an intentionally wrong same-device resume position fails.
Tagged vet, lint and mapping drift checks pass. There are 77 open packet gaps.
Full plan validation succeeds with 968 declarations, 2,048 overlapping gaps and
one current packet review out of 34. The independent inventory review is absent.

Integrated upstream-only run. `make verify VERIFY_ARGS="-target upstream"`
completed with stable input digests, 968 proof records across 17 packets and no
runner anomalies. The upstream verdict was `partial`; 2,050 overlapping gaps
include the missing whole-contract receipt and machine health. Full tagged lint
failed with eight inherited findings. Other integrated health checks passed.
The ledger validator fails on 60 inherited class values in other packets.

2026-11-18 dispatch. Item-write is the sole owner packet for this invocation.
The Terra worker owns its scenario directory and the 12 records assigned to it
by `packets.json`. Reconcile 50 unknown invariant references against pinned
source and run all existing assertions upstream. The architect owns generated
views, integrated upstream-only verification, and scope and continuity review.
The delivery sequence is in [ROADMAP.md](../../../go-backend/ROADMAP.md).

Item-write result. The 50 missing declaration bindings now resolve. The current
register has 345 requirements and 633 invariants. Scoped discovery reports 77
declarations and 32 gaps. Three worker runs and an architect diagnostic passed
all 77 upstream scenarios. Scoped tagged vet and lint passed. The generated
map was rebuilt and its drift check passed.
A temporary Go overlay made the surviving shared-playlist count wrong in
`delete-soft-cascade`; the focused upstream check failed on the persisted count.
Overstated soft-delete, podcast audience and batch-delete claims were corrected
after source and client review. Item-write has open requirements and no
acceptance receipt.

2026-11-18 dispatch. Resume with the `admin_files` discovery repair. The Terra worker owns its directory and capability records `route-r129` and `route-r130`. This invocation covers the existing filesystem-browse and path-existence scenario families. The architect runs the complete verifier, checks the source and web consumers, and records the still-open obligations. Upstream completeness remains the prerequisite for the Go comparison.

2026-11-18 result. `make verify-packet PACKET=admin_files` passed twice. The architect's `go run ./tools/verify -packets admin_files` independently accepted all 12 upstream assertion results. The folder-chooser and uploader consumer contracts are asserted in the two route families. Rejection snapshots compare SQLite rows and media and metadata contents after login setup and allow routine log writes. The architect removed unused setup helpers reported by packet lint. V8 reported only Windows-specific unexecuted ranges in the controller on this POSIX run. The app-root filtering convention still needs an assertion. `-gaps -packets admin_files` reports 52 gaps for the remaining seven routes. The independent packet receipt remains open. Generated mapping and 27 focused Node self-tests pass. Separate Go regressions, verifier and harness race tests, and tagged vet pass.

The complete `make verify` run exited nonzero at `item_write/book-all-metadata-fields`, which declares `INV-R053-BOOK-META-001` outside the canonical register. The 968 raw declarations span 17 of 34 packets and include 50 missing invariant bindings in `item_write` and 21 in `playback_sessions`. The v1.3.0 ledger validator reports inherited unsupported invariant classes in other packets. Upstream coverage and cross-packet review remain open. The full upstream run, Go comparison and rollback did not start.

Q01 tools dispatch, 2026-11-14. The worker owns only `go-backend/tests/replacement/tools/` on master at `8990c2e6` with the inherited verification overlay. The selected invariants are `INV-CAP-EVENT-METADATA-EMBED-QUEUE` and `INV-SOCKET-OUTGOING-METADATA-EMBED-QUEUE-UPDATE`. Exercise an immediate embed and two waiting embeds on real disposable media, including repeat, FIFO payloads, audience and per-socket task ordering. The independent reviewer derives expected behavior from the pinned producer and bundled client. The architect owns the two capability records, their generated mapping and continuity/evidence files. The queue's 45-minute characterization and 90-minute implementation checkpoints apply. Error recovery, other tools operations and application changes are outside this dispatch.

Q01 outcome. The [queue review](../../../go-backend/docs/verification/q01-embed-queue-review.md) and [machine results](../../../go-backend/docs/verification/q01-embed-queue-run.json) record three passing upstream scenarios, transcript counterexamples and remaining obligations. Whole-suite discovery failed at the incomplete playlists package before health, Go assessment or rollback. The current canonical register contains 345 requirements and 503 invariants.

2026-11-14 earlier integration: [report for that checkout](../../../go-backend/docs/verification/unified-verification-integration-20261114.json). All 216 declared upstream scenarios passed. Nine packets reached 41 requirements and 88 invariants. The register has 345 requirements and 478 invariants. Machine health passed; 34 packet reviews and the independent inventory audit remain outstanding. The 2,530 gaps overlap. Input digests were stable, and the gated Go and rollback phases did not start. The integrated command exited nonzero as required.

2026-11-13 foundation baseline. [Durable report](../../../go-backend/docs/verification/unified-verification-report.json) records machine-health PASS, 8 upstream assertions, 5,618 missing proof/binding/facet/review reasons, upstream PARTIAL, Go BLOCKED, equal before/after inputs and nonzero exit. Counts of gaps overlap and are not a requirement percentage. [Exact checks](../../../go-backend/docs/verification/unified-verification-checks.json) include 36 Go packages, 65 Node self-tests, 363 upstream unit tests, native/Linux builds, targeted race and lint. The older weak facet and login validators fail counterexample overlays. Node ports preserve the old inventory outputs before intentional subsequent contract/queue changes.

## Review: contract
Accepted for the foundation after correcting missing-facet credit, null/identity checks and application-input provenance. Exact required tuples are independently specified with sources/rationale; broad source invariants remain open. [Review](../../../go-backend/docs/verification/unified-verification-architect-review.md).

## Review: craft
Foundation accepted. No dependencies or application code changed. One coordinator/shared suite/report; old Go and Node diagnostics are machine health, not behavioral proof. Process groups, reaping and refused-signal/live-member checks pass repeated/race/full runs. Python is removed from the active map/inventory path.

## Blockers / next action
Dispatch `library_data` next. Reproduce the integrated issue-count timeout and
check the row state and filter cache separately. Stabilize the DELETE issues
setup and retain the source-backed scan/cache obligation in the owning packet.
Fix tagged lint findings in `library_data` as part of that review. Store still
needs durable failure and concurrency scenarios, other column semantics and a
packet review. The whole suite needs the remaining packets, current receipts
and an independent inventory audit. See go-backend/handover.md for continuation.

## History
- 2026-11-13: User redirected from independent oracles/test layers to one unified
  upstream-first machine; Go preferred, .mjs where necessary. Earlier interrupted
  turn performed reads only; no partial Python-to-Node implementation to recover.

- 2026-11-13: The first integrated run caught the unconditional Secure-cookie assumption; corrected to actual HTTPS/HTTP behavior. Review rejected the initial one-binding-per-invariant gate and missing/null identity checks; failure-seeking overlays reproduce both defects and current code rejects them. A non-race descendant cleanup refusal was resolved using live-member verification; 10 repeated cleanup checks, race checks and the final documented command pass machine health. Full upstream coverage remains unproved.
