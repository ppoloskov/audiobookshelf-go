---
id: TASK-20261113-replacement-contract-map
title: "Reconcile replacement requirements, tests and ordered owner packets"
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
Produce the complete source-derived requirement-to-test mapping and ordered gap
queue for the defined v2.37.2 replacement contract. This invocation owns the
inventory/planning packet only. No production behavior changes or later owner
dispatch before architect review. Cheap bounded agents reconcile disjoint source
families; the architect owns integration, inventory reconciliation and sequencing.

## Contract
Use `docs/agent-work/CONTRACT.md` and `go-backend/ARCHITECTURE.md`, pinned upstream
`edb1f7efc92ecacdef51aafb4d9d5054ce10604f`, real clients and persisted formats.
Keep the existing capability ledger as the canonical requirement register; render
the test matrix from it and use ROADMAP as the queue entrypoint. No new competing
status system. Preserve existing exclusions and accepted playback differences.

## Acceptance criteria
- [x] Reconcile every source entrypoint and required non-HTTP lifecycle/format.
- [x] Map concrete requirements to actual tests and required missing proof.
- [x] Separate implementation defects, coverage gaps, harness drift and exclusions.
- [x] Publish bounded, ordered, one-owner packets with review gates and budgets.
- [x] Validate the mapping and state the exact gate for the final contract claim.

## Architecture review
Documentation/inventory changes only. Retain existing capability IDs, archive
superseded records with provenance, and do not infer verification from a route
string, shape skeleton, passing Go suite or historical report.

## Decisions
Base `8990c2e61a1637a69effc4ca26e2df1748301889`, master, with the accepted playback
overlay and pre-existing suite-runner/latest-core/STATUS overlays preserved.
Playback acceptance is in `go-backend/docs/verification/playback-lifecycle-architect-review.md`.
Do not reopen its accepted scope or extrapolate it to other playback requirements.

## Evidence
Existing coverage tables are discovery leads; many paths and event statuses are
stale. Pre-reconciliation capability records are archived under
`docs/agent-work/archive/capabilities-pre-20261113-contract-map/`.

Current result: 345 requirement records / 425 invariants, 226 routes, 77 socket name/direction records, 165 syntax event sites, 25 models; 47 invariant rows and 30 requirements lack test anchors. All proof gaps are assigned in Q01–Q29.

Checks: mapping `--check`, 8 mapper tests, 5 inventory tests and pinned normal ledger pass. Current checkout `gofmt -l .` (no file paths), vet, configured golangci-lint (0 issues) and all 1,706 Go test/subtest records pass with native WebP FFmpeg; zero individual test skips. Three packages have no test files. No new Go production changes, so no new concurrency race run. Strict all-verified/real-client/differential/rollback/cross-build acceptance not claimed. Exact commands/exit statuses/log hash: [checks](../../../go-backend/docs/verification/replacement-map-checks.json).

## Review: contract
Accepted after independent failure-seeking review and re-review: [architect review](../../../go-backend/docs/verification/replacement-map-architect-review.md). Superseded byte/header/cookie-implementation requirements were removed from active contracts, old details retained as history; queue ownership, source payloads and cache middleware corrected. Completeness is source-surface reconciliation with explicit behavioral proof gaps; no product verification claimed.

## Review: craft
Accepted: one canonical register; generated CSV/JSON/Markdown; bounded owner queue. Renderer tests now preserve canonical verified statuses and prove no global claim is inferred. Eight renderer regressions pass.

## Blockers / next action
Q00 inventory/mapping accepted. Next invocation dispatch Q01 tools with exact files, selected invariants and budget from [queue](../../../go-backend/docs/verification/replacement-gap-queue.md). No second production owner was dispatched. Review/publish existing playback prerequisite overlays separately before publishing the map; no commit/push/deployment performed. Whole replacement claim remains open.

## History
- 2026-11-13: User requested a complete mapping and ordered closure queue, with
  one owner per invocation and architect review between packets. Installed
  skill pack 1.3.0. No whole-contract verification claim is currently supported.

- 2026-11-13: Q00 accepted after independent review. One incomplete representation-cleanup revision was found on re-review and corrected; final contract/craft reviews accept. No active correction failure remains. The map does not upgrade any product requirement to verified. Existing playback acceptance stays separately scoped.
