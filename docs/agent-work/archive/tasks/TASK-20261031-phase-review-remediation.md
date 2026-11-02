---
id: TASK-20261031-phase-review-remediation
title: "Repair independently reproduced phase 0–2 findings"
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
Repair R1–R8 in `go-backend/PHASES-0-2-REVIEW-2026-10-31.md`, preserving the existing worktree and pinned upstream behavior. The owner explicitly requested agents using GPT-6 Luna at Medium effort. Root integrates their exclusive packets and owns shared documentation, CI, status, inventory and final verification. No publishing, deployment, upstream edits, or unrelated backend rewrite.

Independent-review follow-up (2026-10-31): also close the reproduced blocking findings V2–V4, V7 and V9 in `go-backend/PHASES-0-2-INDEPENDENT-REVIEW-2026-10-31.md`, and add the missing V6 source-golden edges. The broad V1/V5 representation rewrite is explicitly deferred until after the gates are green and before Phase 6; this task may make only behavior-preserving simplifications to the pilot builder and shared reader.

## Contract
Upstream v2.37.2 remains the oracle. Preserve collection book identities/order through playlist persistence and emitted output. Verification must reject omitted/duplicate cases, changed verdicts/state checks, forbidden imports and dynamic-map alias bypasses. Generated status must reflect actual harness output with provenance; the executable case catalog owns route metadata. FFmpeg capability differences must be explicit and must not turn matching failures into successful rendering proof.

## Acceptance criteria
- [x] R1: real-handler persistence/event regression and upstream differential witness pass.
- [x] R2: dependency guard rejects direct/nested forbidden imports and accepts allowed imports.
- [x] R3: missing/duplicate requested and full-suite IDs fail; supported focused cases run correctly.
- [x] R4: equivalence checks outcomes/state and reports repeatably with explicit provenance.
- [x] R5: direct/chained/imported dynamic-map alias metrics cannot be reduced by alias spelling.
- [x] R6: `make status` generates one current source; competing active prose is removed.
- [x] R7: route inventory consumes executable metadata and passes generated check.
- [x] R8: WebP expectations follow a validated capability profile, including success proof where available.
- [x] V2: the fresh core parity run is green, including the playlist-from-collection witness.
- [x] V3: the ratchet passes without hiding a regression; the measurement correction or code reduction is recorded.
- [x] V4: the route inventory is regenerated from final source and passes `--check`.
- [x] V6: the source golden covers a non-null ebook/ebookFormat, non-null cover, null duration and empty tags.
- [x] V7: scanner tests do not write into the source tree and `WriteItemMetadata` rejects an empty metadata path.
- [x] V9: new dependencies are validated at construction and the touched error paths do not silently discard decode failures.
- [x] Build/vet/Go tests, harness regressions, fresh parity, ratchet, dependency checks, source inventory and ledger validation pass; remaining operational gaps are explicit.

## Architecture review
Use existing consumer DTOs, Go/Node standard libraries and the existing harness. No new runtime package, dependency, repository framework or DI container is needed. A small status generator and focused guardrail fixtures are tooling, with actual callers in Make/CI. Preserve the historical review; record remediation as later evidence.

## Decisions
User authorization: "Use agents on GPT-6 Luna with Medium effort to fix the finding." Treat all numbered findings as the requested repair scope. Keep broad DTO expansion and the previously unisolated backup mismatch separate unless a finding's repair reveals a direct dependency.

| Packet | Owner | Exclusive paths | Dependency |
| --- | --- | --- | --- |
| R1 runtime regression | Luna / Medium `runtime_fix` | userapi playlist and focused Go tests | none |
| R2/R5 guardrails | Luna / Medium `guardrail_fixes` | .golangci.yml, tools/ratchet, dependency selftests | none |
| R3/R4/R8 harness | Luna / Medium `harness_fixes` | differential/comparator, their helpers/tests and case dispatch as needed | none |
| R6/R7 integration | root | Makefile, runner integration, status, route inventory, CI, baseline and shared docs | worker interface coordination |

## Evidence
- `CGO_ENABLED=0 go build ./...`, `go vet ./...`, and `go test ./...` passed.
- `make parity` executed 193/193, compared cleanly with `contracts/parity/baselines/core-schema2-no-webp.json`, and passed. `make status` generated `PASS (sampled core suite only)`.
- `make ratchet` passed at `map[string]any and aliases: 1126 (baseline 1126)`; all other metrics equal.
- Pinned golangci-lint 2.15.0: `0 issues`. `tools/test-dependency-boundaries.cjs`: 15 fixtures passed.
- Harness/report/status self-tests: 32 passed. Book source golden: 6 fixtures, 3 shapes. Library oracle and source-inventory tests passed.
- `build_route_coverage.py --check` passed for 226 routes; `test_route_coverage.py` passed 4 tests.
- `check-ledger.go` passed with 17 tasks and 237 capability records.
- V2 normalization fix, V3 builder-map reduction and alias removal, V4 inventory regeneration, V6 edge fixture, V7 temp metadata root and empty-path guard, V8 schema-2 baseline, V9 constructor/decode validation are in the worktree.
- Not rerun: `make parity-full`, backup restore, browser/mobile/race checks, Ubuntu CI. Local FFmpeg lacks `libwebp`, so the capable success path is not locally exercised. One `make parity` run flaked on `socket-connect-legacy`; the immediately repeated run passed and the committed baseline comparison then passed.

## Review: contract
- Passed by the implementing agent against the pinned upstream oracle and the independent review findings. No separate-agent review is claimed. The remaining V1/V5 representation issue is explicitly recorded as a separate pre-Phase-6 packet.

## Review: craft
- Passed by the implementing agent. The changes reuse existing DTOs and harness helpers, remove a map alias and two metadata-map copies, add construction and decode validation, and add no dependency or runtime package.

## Blockers / next action
- No blocker for this remediation packet. Next: plan the bounded pre-Phase-6 V1/V5 representation packet (three concrete book builders, one media accessor, no per-consumer JSON round-trips) and separately investigate the one-off Socket.IO timeout.

## History
- Created against installed skill-pack 1.3.0. Existing phase work and review are preserved; no historical task status was relabeled.
- 2026-10-31: Resumed after the independent review. Added V2–V4/V7/V9 as blocking acceptance and V6 as the source-golden edge gap. The V1/V5 representation rewrite remains a separate pre-Phase-6 packet.
- 2026-10-31: Closed the packet. Core parity, ratchet, inventory, depguard, harness tests, book/library goldens and ledger validation pass. Added the schema-2 no-WebP baseline and removed the tracked scanner test sidecars. Full operational suites and the capable-WebP path remain unrun.
