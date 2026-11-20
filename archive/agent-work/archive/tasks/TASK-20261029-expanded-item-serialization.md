---
id: TASK-20261029-expanded-item-serialization
title: "Restore expanded library item serialization parity"
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
Correct only expanded book and podcast item serialization in `go-backend/internal/catalog/handler.go`, with regression coverage in `handler_test.go`. Preserve non-expanded item detail and minified list/shelf shapes. Do not touch internal/userapi.

## Contract
Match upstream Audiobookshelf v2.37.2 commit `edb1f7efc92ecacdef51aafb4d9d5054ce10604f`: expanded LibraryItem keeps `lastScan`, `scanVersion`, and `libraryFiles`; expanded book includes full `audioFiles`, `chapters`, `ebookFile`, expanded author metadata, and tracks; expanded podcast includes full expanded episodes and track information. Podcast episode expanded form keeps `libraryItemId`.

## Acceptance criteria
- [x] Expanded book regression asserts retained item fields and full media keys; minified route stays minified.
- [x] Expanded podcast serialization follows upstream model flags without altering non-expanded detail.
- [x] CGO-disabled build, vet, and full tests pass; ledger validator exits successfully.

## Architecture review
No new dependencies or abstractions; use existing catalog serializers and fixture tests.

## Decisions
Only expanded serialization changes. Non-expanded detail and minified behavior are explicit non-goals. Expanded media remains a strict superset of minified output as required by each model's source comments. A missing database lastScan becomes explicit JSON null in expanded output, matching optional date serialization.

## Evidence
- `CGO_ENABLED=0 go test ./internal/catalog` (from `go-backend/`) — passed after correcting expanded null and strict-superset expectations.
- `CGO_ENABLED=0 go build ./...` (from `go-backend/`) — passed.
- `CGO_ENABLED=0 go vet ./...` (from `go-backend/`) — passed.
- `CGO_ENABLED=0 go test ./...` (from `go-backend/`) — passed.
- `git diff --check` and `gofmt -l internal/catalog/handler.go internal/catalog/handler_test.go` — passed; no output.
- `go run ~/.agents/skills/go-scope-ledger/scripts/check-ledger.go -tasks docs/agent-work/tasks -capabilities docs/agent-work/capabilities` — exit 0; 14 tasks and 233 capability records, structure valid.

## Review: contract
- Pinned upstream `LibraryItem.toOldJSONExpanded()` spreads minified then adds lastScan, scanVersion, expanded media, and libraryFiles; helpers no longer delete those item keys.
- `Book.toOldJSONExpanded(libraryItemId)` spreads minified book fields, then adds expanded metadata, audioFiles, chapters, ebookFile, and tracks; implementation preserves minified counters while returning full arrays/objects.
- `Podcast.toOldJSONExpanded(libraryItemId)` maps episodes through `PodcastEpisode.toOldJSONExpanded(libraryItemId)`; episode shape includes libraryItemId, audioTrack, size, and duration.
- Existing non-expanded detail path remains unchanged; tests cover full detail output and minified list keys.

## Review: craft
- No new dependency or abstraction. Expanded book data reuses existing full book serializer and derives legacy expanded metadata fields locally. Formatting, vet, and full suite passed.

## Blockers / next action
No blockers. Scope is expanded serialization only; no upstream variants were intentionally changed outside the task.

## History
- Initial scope pinned to the installed go-scope-ledger version 1.1.0.
- 2026-10-29: Expanded parity implementation and required checks completed. An initial focused test failed because it incorrectly expected omitted null lastScan and removal of strict-superset counters; both expectations were corrected from source and the focused test passed.
