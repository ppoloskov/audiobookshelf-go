---
id: TASK-20261031-pilot-wire-repair
title: "Pilot wire repair: typed book metadata and shared media accessor"
status: in_progress
risk: medium
skill_pack_version: "1.3.0"
depends_on: ["TASK-20261031-phase-review-remediation"]
approval: approved
gates: not_run
spec_review: not_run
craft_review: not_run
correction_attempts: 0
---

## Scope
Close the pre-Phase-6 representation gap in the book wire pilot before any Phase 6 slice. Replace the map-and-delete metadata construction with typed full, minified and expanded metadata structs; use `[]string` for tags and `*float64` for duration; replace `typedBookEnvelope(..., shape string) (any, error)` with concrete per-shape builders returning their own wire type; add one small media accessor for id, explicit and tags used by catalog ACL and realtime fan-out, with realtime failing closed on missing or unrecognized media. Preserve pinned upstream JSON behavior and nested opaque objects.

## Contract
Pinned upstream v2.37.2 remains the oracle. Metadata key order, null-vs-absent behavior, author/series typing, empty-slice semantics and the three distinct book shapes must stay source-compatible. Realtime must not deliver an item payload when its library is known but its media cannot be interpreted. No new runtime package, dependency, repository framework or DI container. Keep the existing source golden and differential as the behavioral gates.

## Acceptance criteria
- [ ] Typed `BookMetadataFull`, `BookMetadataMinified` and `BookMetadataExpanded` wire structs replace the map-and-delete metadata builders.
- [ ] `Tags` is `[]string` and `Duration` is `*float64` on every shape that carries them; null/empty/absent behavior is preserved.
- [ ] `bookFull`, `bookMinified` and `bookExpanded` are concrete builders returning their respective wire type; no `shape string`/`any` builder remains.
- [ ] One shared media accessor returns id, explicit and tags for typed books and legacy podcast maps; catalog ACL and realtime both use it.
- [ ] Realtime denies a library-scoped payload whose media is missing or unrecognized.
- [ ] Source golden (six fixtures, three shapes) and dual-run tests pass.
- [ ] Fresh core parity executes 193/193 and compares cleanly with the committed schema-2 baseline.
- [ ] `make ratchet` passes with a baseline lower than the current 1126.
- [ ] Backup-restore before/after reports are recorded and both sides pass the same checks.
- [ ] Socket flake rate is measured over repeated runs and reported.
- [ ] Build, vet, full Go tests, depguard, harness/status tests, route inventory and ledger validation pass.

## Architecture review
The pilot currently stores a concrete wire struct inside `map[string]any`, returns `any`, and forces consumers to type-switch or JSON round-trip. This packet keeps the item envelope map for now but makes the book media value and its metadata concrete, and removes the per-consumer conversion of media identity/ACL fields. It does not introduce a repository layer or generic serializer.

## Decisions
Use value-returning builders with a shared source loader so each shape builds only its own metadata. Keep `EbookFile`, audio files, chapters and tracks opaque per D5; keep `EbookFormat` representation-compatible unless the golden exposes a mismatch. Do not widen normalization to make the parity run pass.

## Evidence
- Before backup-restore reports: `contracts/parity/evidence/2026-10-31-pilot-wire/backup-restore-before-{node,go}.json` (both sides: all nine checks true).

## Review: contract
- NOT RUN.

## Review: craft
- NOT RUN.

## Blockers / next action
No external blocker. Implement the wire structs, builders and accessor, then run the golden/parity/ratchet/backup/socket gates.

## History
- Created against installed skill-pack 1.3.0 from the owner's pilot repair instructions. Depends on the completed phase-review remediation.
