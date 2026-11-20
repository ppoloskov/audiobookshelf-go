---
id: TASK-20261029-sqlrows-libraryjson
title: "Audit single-connection SQL cursor nesting and library JSON null parity"
status: verified
risk: low
skill_pack_version: "1.1.0"
depends_on: []
approval: not_required
gates: passed
spec_review: passed
craft_review: passed
correction_attempts: 0
---

## Scope
Audit requested Go packages, excluding internal/userapi and explicitly excluded recent files. Fix actual open-cursor/nested-query hazards and omit nullable library lastScanVersion in legacy JSON.

## Contract
Preserve upstream behavior at pinned commit edb1f7efc92ecacdef51aafb4d9d5054ce10604f. Keep lastScan present as null, and omit lastScanVersion only when its DB value is NULL. No dependency or API additions.

## Acceptance criteria
- [x] Audit requested query sites and report SAFE/FIXED locations.
- [x] Regression test covers null and non-null lastScanVersion.
- [x] Required CGO-disabled build, vet and full test pass.

## Architecture review
No new abstractions/dependencies; a one-field serialization correction and audit only.

## Decisions
Upstream `Library.toOldJSON()` returns `lastScan: this.lastScan?.valueOf() || null` and `lastScanVersion: this.lastScanVersion`; required JSON shape is thus null for lastScan, undefined/omitted lastScanVersion.

## Evidence
- `CGO_ENABLED=0 go test ./internal/catalog` — passed.
- `CGO_ENABLED=0 go build ./...` — passed.
- `CGO_ENABLED=0 go vet ./...` — passed.
- `CGO_ENABLED=0 go test ./...` — passed.
- `gofmt -l internal/catalog/library_writes.go internal/catalog/handler_test.go` — no output.

## Review: contract
- No unintended behavior change found; only lastScanVersion omission is altered, while lastScan remains present-null as upstream requires.

## Review: craft
- No helper, dependency, or abstraction added. Tests exercise multiple library items with DTO expansion and both null/non-null library serialization.

## Blockers / next action
Audit verdicts and exact call sites are reported in the implementation response. No SQL cursor hazard requiring code changes was found in scope.

## History
- Initial scope recorded against installed go-scope-ledger version 1.1.0.
