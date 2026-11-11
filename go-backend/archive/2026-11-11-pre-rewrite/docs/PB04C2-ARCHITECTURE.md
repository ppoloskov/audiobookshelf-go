# PB04c2 gate decision and completed-work review

2026-11-11. Reviewed source: `cf3990d0` on master, initially clean checkout.
Decision owner: architect. No runtime implementation or worker dispatch in
this review. This record governs the existing PB04 roadmap, not a new queue.

## Decision

**Deletion moves first to the existing `internal/itemwrite` mutation owner.**
Do not extend the c1 callback pattern to whole-library deletion. PB04c2 stays
gated until the corrective packet and deletion prerequisite below are accepted.
Quick-match may use the typed per-item operation seam specified here; it does
not require relocating the complete matcher before c2.

Why:

- `catalog/library_delete.go:42-96` deletes item references and library-owned
  rows in one transaction. The library operation must retain that transaction.
- `catalog/item_delete.go:61-89` starts/commits its own transaction and then
  announces playlists/feeds, purges cache/metadata and emits item removal.
  Calling that operation while a library transaction is open either splits
  atomicity or waits for the single DB connection it already holds.
- `library/writes.go:341-357` calls RemoveFolderItems before its transaction.
  That seam is not evidence that a transactionless callback is safe inside a
  library deletion transaction.
- `itemwrite` already owns relation/orphan sweeps and can depend on
  `playlistview -> items`. Putting deletion completion directly in root
  `items` would introduce `items -> playlistview -> items`.
- A new removal package or a set of callbacks for SQL, files and events would
  add ownership surfaces without removing these dependencies. Reuse the
  mutation owner; do not put this business workflow into store.

## Review of completed work

The PB02/PB03/PB04 sequence has established real read ownership: items composes
books/podcast reads, people owns authors, and library owns browsing, series,
search/shelves and the c1 write operations. The latest c1 diff preserves the
moved operation bodies apart from ReadView wiring and the explicit callback.
No new extraction regression was found in the inspected c1 paths. Earlier
acceptance evidence remains attributed to its original packets; this review
is not a fresh exhaustive audit of every earlier change.

Two actionable findings:

1. **High: removal-only folder PATCH leaves the folder row behind.**
   `library/writes.go:349-355` excludes removedFolders from hasUpdates. The
   callback can commit item deletions, after which the handler returns 200
   before deleting the folder and refreshing watchers/emitting library_updated.
   The same condition exists in pre-c1 catalog/library_writes.go:355, so this
   is inherited, not introduced by extraction. Upstream explicitly destroys
   the folder, sets hasFolderUpdates and refreshes/emits for that case
   (`server/controllers/LibraryController.js:475-511`).
2. **Medium: claimed callback coverage is absent.**
   `library/writes_test.go:41-43` supplies a no-op callback, not the recording
   callback claimed in the card. Its only test retains the old folder and
   adds a new one; it never exercises removal, callback failure or missing
   wiring. Restore meaningful boundary coverage before moving ownership.

A temporary Go test overlay reproduced finding 1: create two folders, PATCH
only the retained folder ID, expect the other row removed. Result: FAIL,
removed row count 1, response still lists both folders. The overlay changed no
repository source. Reproducer files are under `/private/tmp/abs-pb04-review-*`;
the enduring test must be added by the corrective packet.

## Corrective dispatch: PB04c1-R

One concern: restore removal-only update behavior before moving its seam.
Write scope: library/writes.go, library/writes_test.go and the existing
catalog/server integration test rig only if needed for real deletion wiring;
owning status docs. No unrelated settings rewrite.

Pre-dispatch ceiling: **+5 handwritten LOC / +2 complexity**, zero increases
in high-complexity functions, maps, []any or any other ratchet metric. Test and
documentation lines are outside production LOC. This is a budget, not a
baseline edit or an implementation acceptance.

Start with failing endpoint tests. Cover folder-row removal and response,
actual item removal, retained-folder/items survival, one watcher refresh and
the established events. Use recording callbacks to assert library/folder IDs
and pre-transaction call timing. Cover callback error and missing wiring:
no subsequent library writes, refresh or library_updated event. Repeated
identical PATCH must not delete/announce again. Preserve established partial
commit behavior if a later folder operation fails; do not redesign atomicity
of folder updates under a one-line bug fix.

## Prerequisite dispatch: PB04c2-D

One ownership change: transfer item deletion mechanics from catalog to
itemwrite. Start only after PB04c1-R review; stop for review before c2.

Write scope: itemwrite deletion code/tests; catalog/item_delete.go,
library_delete.go, library_issues.go, batch.go and their deletion tests;
library/handler.go and writes.go for the callback exit; server/runtime.go;
directly used catalog cover/path helpers and callers only where needed to
move their single implementation; lint fixtures if dependency edges change;
owning docs. Do not migrate media PATCH or quickmatch implementations.

Pre-dispatch ceiling: **+120 handwritten LOC / +25 complexity**, zero
increases in high-complexity functions, maps, []any or other ratchet metrics.
Reuse the existing item row projection and permission loaders where applicable;
a minimal deletion target/change result is legitimate, a second aggregate is
not. List exact moved bodies and caller mappings in the worker report.

Required operation boundaries:

- `DeleteRows(ctx, tx, targets) (changes, error)` takes the caller's *sql.Tx.
  It never opens/commits a transaction, queries the pool, deletes files,
  invalidates caches or emits events. It returns typed playlist/feed/link
  changes and the deleted targets. No arbitrary table-name API.
- A concrete mutation owner provides explicitly named soft/hard deletion
  operations for standalone callers and separately callable post-commit
  steps. No bool mode switch or HTTP request/writer in this API.
- Whole-library deletion still owns its enclosing transaction. Its existing
  order remains: commit, watcher refresh, playlist announcements, each item's
  feed-close/cache/metadata/item_removed, then library_removed. No pool access
  while tx/rows remain open, and no effects before successful commit.
- Preserve distinct sweep schedules: single/folder removal sweeps after each
  item; batch uses series then authors per item; issues accumulates links and
  sweeps once; whole-library deletion removes its library-owned entities in
  the enclosing transaction. Do not flatten these into one delete-many policy.
- Delete catalog's DeleteRows/removal implementations and RemoveFolderItems
  bridge. Library's folder update calls the concrete mutation owner directly
  before its own transaction, preserving the characterized c1 order. Catalog
  HTTP handlers remain adapters with their existing authorization order.
- Move shared cover purge/path safety implementations once and update their
  actual consumers; do not clone them into the new owner. Retain hard-delete
  symlink/root checks and current filesystem-error semantics, documenting
  deliberate ignores. No broader filesystem cleanup or new dependency.

Retain existing cascade, playlist audience, batch, Wave G feed/cache/event and
single-item rollback tests. Add a whole-library rollback test that fails a
library-owned statement after item-row deletion, proving all rows survive and
there are no watcher calls, files/cache changes or events. Use the one-connection
pool and bounded context so nested-transaction/pool deadlocks fail decisively.

## PB04c2 after prerequisite acceptance

Library owns the remaining library delete/issues/download/match-all routes and
their orchestration. No callback to another HTTP handler is accepted.

For match-all accept a consumer-defined typed operation equivalent to:
`QuickMatchItem(ctx, itemID, provider) (MatchOutcome, error)`, exposing only
updated/warning data. Catalog adapts its existing matcher at composition.
Library owns target selection, skip rules, task start/results/finish and the
existing asynchronous response behavior. Close rows before invoking it;
never pass an open transaction, request, Scope or generic map. The matcher
owns each item's transaction and side effects. Missing wiring must fail before
launch/200. This is a stable operation boundary with catalog as the current
item-matching owner, not a whole-library forwarding bridge or a second matcher.
Do not change job cancellation/restore semantics implicitly in this packet.

Download retains ordered item selection, full item ACL checks, path checks,
STORE ZIP streaming and post-header error behavior. At c2 dispatch inventory
the current itemsExpandedWhere/userCanAccessItems consumers and assign shared
read mechanics to items with explicit callers, rather than copying the SQL or
substituting a differently ordered items.Load result. This work must be in the
exact c2 file scope and budget before dispatch.

**No c2 budget is active:** set it at actual dispatch against the accepted
post-D source/metrics, with exact files, helper replacements and tests. The
deletion prerequisite does not authorize c2 automatically. After c2 acceptance,
continue with progress/store record ownership, the final boundary-sequence
stage. No further library extraction wave is silently added.

## Verification and limits

This review reran library/catalog/server/items/scanner suites using the
documented FFmpeg prefix: all five passed. The overlay counterexample failed
as expected. The focused green suites therefore do not close the missing
removal behavior. Independent read-only architecture tracing corroborated the
transaction/graph decision. No full-suite, race, lint, ratchet or TS rollback
rerun is claimed in this review. Runtime packets require their existing full
gates, affected race checks and persistence rollback before acceptance/push.
No source edits, baseline changes, commit, push or deployment were performed.
