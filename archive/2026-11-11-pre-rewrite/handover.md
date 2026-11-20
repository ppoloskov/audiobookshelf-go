# Go backend handover

## Owner clarification — internal freedom, 2026-11-11

The owner grants complete freedom to reimplement internal subsystems. Fixed
product boundaries are persisted-data/rollback compatibility and client
contracts. Do not retain modules, JS emulation or upstream mechanisms merely
because they exist today. Historical retention decisions, including custom
transport framing, do not permanently exclude library alternatives. Review
whole-workflow replacement cost and prove any retained compatibility adapter
against an actual data/client obligation. The earlier library audit was too
conservative where it treated internal mechanisms as immutable; its exclusions
need reassessment under this rule. This clarification starts no runtime packet.

## Revised library replacement recommendations — 2026-11-11

The completed re-audit covers all 57 internal Go package directories at
`accbd563`. It supersedes the earlier helper-only shortlist: evaluate complete
subsystem replacements and count the adapters that actually remain. Internal
APIs, record layers, SQL, framing, process/queue models and TS emulation are
replaceable; persisted-data/rollback and client contracts govern acceptance.

Recommended prototype order (separate from the active PB04 execution queue):

1. **Socket.IO/Engine.IO replacement:** evaluate
   [zishang520 Socket.IO v3](https://github.com/zishang520/socket.io/releases/tag/v3.0.6),
   initially v3.0.6. Delete candidates are `realtime/eio.go` (239 physical
   lines), `sio.go` (228), and transport-specific hub/mount plumbing. Use
   library sockets/acks/rooms directly. Preserve post-connect `auth`,
   `auth_failed`/`init` ordering, persisted lastSeen, action ACLs and per-user
   payload filtering. The release reports early-CONNECT race fixes; rerun the
   historical 200 sequential and 10×20 concurrent immediate-auth tests on both
   paths before claiming resolution. Also verify reconnect, limits, shutdown
   and restore. Historical cancellation is no longer an exclusion.
2. **Canonical persistence:** prototype
   [Bob records and optional setters](https://bob.stephenafamo.com/docs/models/table/)
   across author/series read-update operations, then a batched book read.
   [Bun](https://bun.uptrace.dev/guide/) and existing sqlc are comparison
   alternatives, not additional overlapping persistence layers. Target
   `sqlupdate`, catalog row/update emulation, duplicate store/entity records,
   scanners/loaders and settings-row glue. Preserve stored formats, safe API
   projections, null/zero/presence, observable no-op behavior, query ordering
   and outer transaction ownership. Do not serialize credential-bearing rows.
3. **Typed HTTP edge:** evaluate
   [oapi-codegen](https://github.com/oapi-codegen/oapi-codegen) or
   [Huma](https://huma.rocks/features/request-validation/) on one read, one
   nullable PATCH and representative auth/error responses. Replace repeated
   binding/decoding/response types rather than wrap old handlers. Current
   `docs/openapi.json` has only 31 paths/91 schemas; verify the selected
   contract first. Generator/framework default errors and validation order
   must not silently replace client contracts.

Other worthwhile candidates:

- **One media-job engine:** [afmpeg](https://afmpeg.phpboyscout.uk/) could
  consolidate mediatools, audiotools processing, playback transcode and
  scanner/podcast probe plumbing. Rough candidate envelope is 500 physical
  lines, not net savings. Prove full probe output, required codecs, long-audio
  throughput/memory, native platform coverage and HLS seek/reset first.
- **Mail:** [go-mail](https://github.com/wneessen/go-mail) replaces SMTP/TLS,
  MIME/base64 and attachment staging; 111 gross transport/encoding lines are
  identified, plus the opportunity to stream attachments. Retain settings,
  e-reader access and endpoint results; notifications already uses Apprise.
- **Sanitization:** [bluemonday](https://github.com/microcosm-cc/bluemonday)
  can replace the 236-line textclean algorithm with configured policy.
  Preserve safe rendered/stored content; avoid recreating token/formatting
  quirks merely for TS resemblance.
- **Archive service:** compare
  [mholt/archives](https://github.com/mholt/archives) with one shared
  `archive/zip` implementation across backup and public/private downloads.
  About 190 gross helper lines are identified. Preserve exported backup
  layout, snapshot/restore safety and download behavior; count codec/dependency
  and manifest-adapter costs.
- **Logging:** reconsider a `slog` record/sink pipeline with a rotating writer
  such as [Timberjack](https://github.com/DeRuina/timberjack), plus a thin
  client-history/socket adapter. The current 724-line logger is replaceable.
  Establish persisted log/history obligations before changing file layout;
  console formatting and existing classes are not intrinsically contracts.

All figures are gross physical source footprints, not measured net deletion;
the opportunities overlap and must not be summed. No library was installed,
prototype implemented, runtime test/benchmark run or adoption approved by the
audit. Before dispatch, name the exact deletion/replacement scope and budget;
measure total authored Go, SQL, configuration, codecs and adapters separately
from generated output. Require client/data, race, performance and rollback
proof appropriate to the change. These recommendations do not auto-start a
packet or replace PB04c2's gate/progress-store sequence.

## Current architect decision — 2026-11-11, cf3990d0

[PB04c2 gate review and dispatch constraints](docs/PB04C2-ARCHITECTURE.md):
deletion moves first to existing itemwrite; library keeps its whole-library
transaction and receives typed deletion changes for post-commit effects.
Do not reuse the c1 transactionless callback inside that transaction. Root
items would cycle through playlistview. A narrow per-item quick-match seam is
accepted; match-all selection/task orchestration belongs to library.

Before extraction, PB04c1-R repairs a reproduced inherited defect:
removal-only folder PATCH removes items but returns before deleting the
folder row or refreshing/emitting. Its write test was a no-op callback, despite
the earlier recording-callback claim. Review ran five affected package suites
(all pass) and a temporary overlay counterexample (fails as expected).
No runtime implementation, baseline edit, commit or push in this review.
R budget +5 LOC/+2 complexity; deletion prerequisite D budget +120/+25;
all other metric growth zero. C2 remains gated with budget unset until its
post-D dispatch. One packet per invocation/review; progress/store records
follow c2. Historical acceptance summaries below are not a fresh full audit.

## User API simplification — UA01 (2026-11-10)

Terra implemented clearer concern files inside `internal/userapi`, retaining
the public facade and all callers. The mixed `me_extra.go` is removed. Actual
collapses remove rounded intermediate maps, redundant primary-key bookkeeping,
nested stats-map assertions and repeated PATCH validation; series visibility
uses standard slice operations. Characterization tests preserve ordered
validation, legacy stats, duplicate progress rows and repeated series actions.
The architect accepted the ownership tradeoff: +34 production LOC, −17
complexity, −2 high-complexity functions, −6 dynamic maps and −1 `[]any`.
Full build/vet/tests (real FFmpeg and Socket.IO), lint and ratchet/dbnest pass
in an isolated HEAD-plus-UA01 source snapshot; focused race tests pass too.
See [the implementation/review record](docs/USERAPI-SIMPLIFICATION.md) for exact
snapshot boundaries, gotchas and checks. Shared master still has unrelated
in-flight edits whose metric reconciliation is separate. No commit/push or
deployment was performed for UA01; combined publication gates remain pending.

## Podcasts simplification PC01 (2026-11-10)

Implemented with the requested Terra worker and reviewed by the architect.
[The review/addendum](docs/PODCASTS-SIMPLIFICATION-REVIEW.md) records contracts,
actual file ownership, characterizations and checks. Typed quick matching,
direct update construction, shared completion/queue snapshots, single probing
and deferred creation cleanup replace redundant code. Creation/OPML,
scheduling, episode mutation and registration have focused files; podcasts.go
falls from 1,032 to 356 lines. All public interfaces and podcasts/read source
are unchanged. Net packet: -10 production LOC / -27 complexity / -1 function
over 15 / -9 dynamic maps, with no other metric growth.

Important quirks: PATCH matches exact key case and ignores some wrong types;
OPML's current genre conversion remains; search and quick-match differ;
existing directories and committed item directories survive creation failures.
New tests protect these semantics and file/DB rollback. Focused races pass
34 top-level tests (65 including subtests), zero skips. Isolated full build,
vet, suite, lint, ratchet/dbnest, SQLC freshness, dependency/source/contract
checks, both Linux builds and TS rollback pass. One unrelated HLS test skips
on its hardcoded /usr/bin/ffmpeg macOS check; optional Monstrobook inventory
remains skipped. Validation snapshot is HEAD bf72cb3c plus only PC01, excluding
concurrent PB04/userapi changes; do not attribute combined workspace metrics
to podcasts. Local acceptance is complete; isolate PC01 for publication without
staging unrelated work. No live deployment is authorized by this packet.

## Package/object boundary request (2026-11-10)

The owner requested more cohesive packages and clearer object ownership.
[The source-backed proposal](docs/PACKAGE-BOUNDARIES.md) records findings,
owners, alternatives, migration hazards and required proof (ROADMAP PB01).
The first bounded ownership packet is ACCEPTED (2026-11-10): email
defaults and e-reader device policy moved from `settingsdoc` to
`internal/emailsettings`; `emailapi`, `identity` and `userapi`
consume that owner, while `settingsdoc` retains generic row mechanics.
The second packet is ACCEPTED (2026-11-10): `emailsettings.Settings` is a
typed json-tagged document, PATCH
decodes onto the loaded struct, device maps preserve unknown fields, the SMTP
password is redacted by `String`, `settingsdoc.LoadInto` supports typed
documents, and sendEbook no longer asserts the device email type. Revised
accepted growth after the port-parity fix is +115 handwritten LOC and +13
complexity, offset by −10 `map[string]any`, −6 `[]any` and −1 function over 15;
only those measured baseline fields changed. Stored ports now round-trip
verbatim and PATCH applies upstream `Number()` coercion rather than the old Go
clamp; the clamp removal is a parity restoration. Residual risks: a malformed
stored `ereaderDevices` non-object element now returns 500 instead of being
ignored (unreachable through validated TS/Go writes), and the pre-existing
`name`/`email != nil` versus JS truthiness mismatch in device visibility stays
a separate follow-up parity nit. Next after explicit handover: the catalog
books/podcasts/common-item read dependency graph, migrating existing libitem
models without a second implementation. Earlier read-only review confirmed
progressdto, playlistview, settingsdoc and store ownership problems; libitem
and itemwrite already consolidate useful shared behavior and must not be
duplicated during extraction. The blanket feature import ban was not weakened:
`emailsettings` is classified as a foundation package.

Updated 2026-11-10. Canonical checkout: `/Users/paul/projects/audiobookshelf-go`, **master only**, fork
`https://github.com/ppoloskov/audiobookshelf-go`. No development branches or implementation worktrees. The
architect plans, reviews and integrates; cheap workers run bounded packets on disjoint packages. Every push is
gated: the full local CI mirror and the TS rollback check must pass first.

Read [AGENTS.md](AGENTS.md), [ARCHITECTURE.md](ARCHITECTURE.md), [ROADMAP.md](ROADMAP.md), then the refactor
[decision log](docs/REFACTOR-LOG.md) and the [simplification ledger](docs/SIMPLIFICATION.md). The previous handover
is archived at [archive/reviews/2026-11-09-handover-before-wave-g.md](archive/reviews/2026-11-09-handover-before-wave-g.md).

## Current state

- **Base:** local `master` contains the accepted email-settings packets through `7754ed0a`, PB02a–d, PB03a at `bf72cb3c`, the intermediate `371e9abb` simplification, PB04a/UA01 through `a90d1dd5`, PB04b1 at `c244f6a5`, PB04b2a at `dcf1b94a`, PB04b2b at `88e71f91`, PB04b3 at `2b422604`, PB04c1 at `cf3990d0`, PB04c1-R at `36b0c30a`, plus the accepted PB04c2-D slice in this commit.
- **Local acceptance:** the current worktree passes `go build ./...`, `go vet ./...`, `go test ./... -count=1` with the documented FFmpeg prefix, targeted `-race` tests, golangci-lint v2.15.0, dependency-boundary fixtures and `make ratchet`.
- **Metrics:** 38,834 handwritten LOC / 8,570 complexity / 111 functions over 15 / 435 `map[string]any` / 194 `[]any`. PB04c2-D has an in-review delta of +109 LOC / +14 complexity / −1 map with every other ratchet metric unchanged, within the +120/+25 ceiling.
- **Catalog read graph (PB02) — DONE:** PB02a moved item permission inputs into `internal/access`. PB02b moved common item identity, the batched loader and common envelopes into `internal/items`. PB02c moved book identity, projections and `LoadMany` into `internal/books`. PB02d is ACCEPTED at `a8e1b111`: podcast models/projections/episode loading moved into `internal/podcasts/read`, shared file models into `internal/mediafiles`, and `libitem` is deleted. `items` composes `books` + `podcastread` + `mediafiles`; feature `podcasts` consumes the read subpackage and keeps writes. The graph is acyclic and shared chunked queries live in `store.QueryIn`. Architect rulings: `mediafiles` approved as the shared representation leaf (not `store`); `podcasts/read` approved as a foundation subpackage with fixtures both ways; the items row-decode and explicit-podcast `Access()` assertions were restored at acceptance. Each media owner now has direct tests at its own boundary.
- **People (PB03a) — ACCEPTED (2026-11-10):** author/series records, views, detail, update/rename, merge/delete and metadata matching moved from catalog into `internal/people` (1,149 production lines; the standalone package clears the fold-into-books measurement). Catalog keeps library-scoped author/series list routes and author image routes for the next slice; itemwrite remains the relation-cleanup/orphan owner. The dependency is `catalog` → `people`, and `people` does not import catalog. Author images use catalog callbacks supplied at composition; people writes metadata through the scanner callback. Architect rulings: +141 LOC / +26 complexity ratified (composition surface, shared `DecodeObjectFields`, removed `AuthorFinder` dependency); the image-callback seam has a named exit task in PB04; the nil-skip `WriteMetadata` tolerance must become a recording callback in the test rig; pre-dispatch budget recording is required for later growth packets.
- **PB04a — ACCEPTED (2026-11-11):** the people remainder (library-scoped author/series lists, author image routes/helpers, author/series test rig with a recording `WriteMetadata` callback) and the first `internal/library` slice (`InLibrary` request scope, `Scope`, `MediaTable`) landed. Both PB03a contract conditions are closed; the image-callback exit is complete (`PurgeImageCache`/`SaveAuthorImage` deleted from `Deps`). Shared file/stream serving moved to `httputil`, image resize to `mediatools`; the temporary `SeriesMatchesFilter` callback exits in PB04b. Owner ruling on series: series is not people — it is library-scoped (no image/matching/merge upstream, `libraryId` FK), its list routes are LibraryController operations upstream, and `books` cannot own the routes (`items → books` would cycle). Series routes moved people → library in PB04b1; `people` is authors-only.
- **UA01 — accepted by integration (2026-11-11):** the user API simplification (me_extra split into topical files, facade retained, six documented behavior-preserving simplifications) landed in the shared checkout mid-review and was published with PB04a in `c7b7d1d9`. Ratified retroactively: its isolated validation record is in [USERAPI-SIMPLIFICATION.md](docs/USERAPI-SIMPLIFICATION.md); architect reran `-race` on userapi/users and the full gates on the combined tree. Isolated delta: +34 LOC / −17 complexity / −2 over-15 / −6 maps / −1 `[]any`. Process rule recorded in ROADMAP: no `git add -A` on the shared checkout; workers publish or quarantine their changes before another packet's review.
- **QM01:** empty converted match lists preserve existing links; duplicate series updates run sequentially; missing sequence preserves the current position; absent, null, empty and numeric text stay distinct. The actual QuickMatch adapter stores raw zero as `"0"`; PATCH and scanner adapters retain their respective null/absence rules. Existing Go characterization preserves event ordering.
- **PB04b1 — ACCEPTED (2026-11-11):** the filter/sort compiler and series entity/list/detail/update routes moved from catalog/people into `internal/library`; `people` is authors-only and `SeriesMatchesFilter` is gone. Support code: `feeds.LoadMinified`, `httputil.NormalizedInclude`/`IncludeHas`, and `sqlupdate.Update` for author/series column edits (new package ratified: two consumers, one Sequelize-`changed()` concept; `store` was the acceptable alternative). Catalog browsing/search still calls `library.FilterSQL`/`OrderSQL` until PB04b2/b3. Series tests moved to library, including list/filter/include/pagination coverage. The changed-only `series_updated` event and the pure-rename compiler were verified by the architect; the PB04b decomposition was recorded in the card before any move, with the budget pre-accepted. No route, JSON, permission, persistence or event behavior changed.
- **PB04b2a — ACCEPTED (2026-11-11):** library list/detail/view, stats, filterdata and narrator/podcast-title reads moved to `internal/library`; catalog keeps narrator updates and every library/admin write. `View`/`FolderView` are public for catalog write responses, while `ReadView` and `StatsValue` hold the read implementation. Library tests now own the view/stats/filterdata coverage; catalog mutation and invalidation tests mount the library handler where they assert write side effects. Route registrations moved exactly; the same `filterdata.Cache` instance is shared by both handlers so write invalidation still clears reads. The 16-line catalog alias file has a named exit task: it dissolves when library writes/admin move. Actual +52 LOC / +5 complexity, within the pre-recorded +100/+20 budget. No route, JSON, permission, persistence or event behavior changed.
- **PB04b2b — ACCEPTED (2026-11-11):** the item-list route, filter/sort/count/page rendering, collapse-series/subseries helpers and the item rssFeed/mediaItemShare include seam moved to `internal/library`; `books.SeriesRef` replaced `people.BookSeries` (identical json tags). Catalog keeps shelves/search, item detail, cover/file routes and mutations, plus library writes/admin. The admin-only share gate and feed/expiry/null behavior were verified verbatim; actual +3 LOC / +2 complexity. Recorded notes: the missing b2b pre-budget was self-disclosed and ratified this once (every packet records its budget before dispatch from now on), and the catalog-side item-list fixtures move to library once catalog's read surface is empty.
- **PB04b3 — ACCEPTED (2026-11-11):** library search, personalized shelves, shelf/search DTOs and the library-scoped author list moved to `internal/library`; the author list came from people so the old people → library edge inverted to library → people (verified acyclic; the allow-fixture flipped to match). People is now author entities/CRUD/images/matching only. Catalog production holds only library writes/admin, item detail/cover/file routes and item mutations. Actual +3 LOC / +2 complexity, all other metrics unchanged. This was the third self-disclosed missing pre-dispatch budget; from the writes/admin packet onward a missing pre-recorded budget returns the packet regardless of actual growth.
- **PB04c1 — ACCEPTED (2026-11-11):** library create, update, reorder, narrator update, OPML and remove-metadata moved to `internal/library`; the catalog `libraryView` alias file is deleted (exit task completed). The `RemoveFolderItems` composition callback (catalog implements, library calls on folder removal) fails loudly when unwired; its test callback is a no-op, with removal coverage to be repaired under PB04c1-R. Its exit task is item-deletion ownership. Catalog production now retains only library delete/issues, download, match-all (PB04c2, gated on the item-deletion/quick-match dependency), item detail/cover/file routes and item mutations. The c-split and +120/+25 budget were recorded before moving; actual +51/+9. Note: the packet report arrived truncated; the review was performed from the card and diff.
- **PB04c1-R — ACCEPTED (2026-11-11):** the removal-only folder PATCH repair. The one-line `hasUpdates` fix (removed folders now count as a real update) restores the inherited upstream behavior — folder row deleted inside the library transaction, one watcher refresh, one `library_updated`, updated library returned — with five tests covering removal, retained folders, callback arguments, callback error, missing wiring and idempotence. Verified against LibraryController.js:475-511 and the pre-move `catalog/library_writes.go:355` lineage. The PB04c2 gate decision ([PB04C2-ARCHITECTURE.md](docs/PB04C2-ARCHITECTURE.md)) is ratified: deletion mechanics to `itemwrite` with caller-owned transactions and post-commit effects, the `QuickMatchItem` typed seam, download ownership inventoried at dispatch. PB04c2-D is authorized under its recorded +120/+25 ceiling; stop for review after that one ownership change.
- **PB04c2-D — IN REVIEW (2026-11-11):** item deletion rows/results moved to `itemwrite.DeleteRows(ctx, tx, targets)` under caller-owned transactions; concrete soft/hard/folder operations own post-commit effects. Catalog single/batch/issues and whole-library callers use the owner, the `RemoveFolderItems` bridge is deleted, and library folder removal calls `itemwrite.Deleter.DeleteFolderSoft` before its own transaction. Path safety/cover purge moved to `mediafiles`. Whole-library transaction and effect order remain; the new rollback test fails a library-owned statement after item-row deletion and proves rows/files/cache/events survive. No route, JSON, permission, persistence or event behavior changed.
- **PB04c2-D — ACCEPTED (2026-11-11):** deletion mechanics moved to `internal/itemwrite`: `DeleteRows(ctx, tx, targets)` is transaction-only with typed changes; `Deleter` provides named soft/hard operations and separately callable post-commit steps (playlist/feed announcements, cover/metadata purge, hard-delete file removal, `item_removed`); `DeleteFolderSoft` preserves the per-item sweep and issues-reset schedule; batch keeps series-then-authors per item and issues accumulate-then-sweep-once. The `RemoveFolderItems` bridge is deleted — library calls `DeleteFolderSoft` directly; path safety and cover purge moved once to `mediafiles` with direct tests; the whole-library transaction and its effect order stayed with catalog until c2. The required rollback proof landed (trigger-failed library delete after item rows → 500, everything intact). Actual +109 LOC / +14 complexity / −1 map, within the +120/+25 ceiling. Residual risk: unknown-media-type rows in folder deletion skip media cleanup instead of 400ing (corrupt rows only). PB04c2 stays gated: set its budget against this post-D source at dispatch.
- **Git state:** accepted work is committed through `36b0c30a` and pushed. The accepted PB04c2-D slice, its baseline update and this documentation update are committed together and pushed to the fork's master. No deployment is claimed.

## Step 3, behavioural test-quality pass (done)

Owner-approved spec: `TESTS-SPEC` (classify every candidate in the log's "Tests to change" as replace / rewrite /
delete / retain; preserve every contractual assertion; remove test-only hooks only via existing injection or
package-private fixtures; lifecycle invariants first: playback, HLS, share expiry, watcher shutdown, podcast
downloads, restore).

- **Salvaged and committed (`d833484`):** T3 finished; partial T1/T2/T4/T5 work kept where it built and passed
  under `-race`. Behaviour fixes with TS evidence are in the commit message (podcast download race, Audible
  rating, empty filter values, HLS close vs seek restart, share expiry on session routes). TS-diagnostic files
  restored (they back `contracts/test-assertions.json`).
- **Audit done and restored** ([archive/reviews/2026-11-09-step3-salvage-audit.md](archive/reviews/2026-11-09-step3-salvage-audit.md)):
  60 tests really gone; the contractual/lifecycle losses were restored (backup notification fields, store partial
  decode, catalog numEpisodesIncomplete + N+1 guard via a counting driver in the test, identity dates, playback
  journeys, realtime parser/Emit-after-close, stale doc references). Node-to-Go backup import tests were always
  skipped (env-gated); Node archive import stays covered only by the manual harness.
- Earlier Wave G leads and subsequent corrections are tracked in [the G1–G6 audit](docs/WAVE-G-AUDIT.md);
  do not reuse the superseded claim that the TS numeric-string behavior is confirmed.
- **T1/T2/T4/T5 relaunch done (committed with this update):** 3 tests removed, each replaced by a stronger
  named test; hooks removed (realtime AddTestClient/SetTestEmitter, podcasts SetHTTPClientForTests, access
  decisions golden); lifecycle tests added (catalog delete cascades, lists lifecycle, identity revocation,
  audiotools batch). Step 3 is complete.
- Original Wave G feed and stream-cleanup leads reproduced and fixed in the bounded packet; current evidence is
  recorded in the transition audit and validation report, including initial RED and final green outcomes.

## R1 reviewed locally (2026-11-10)

- `OpenSessions` now passes its typed session DTO slice through as an opaque
  JSON value; the userapi endpoint preserves array shape and nullable fields.
  The implementation worker reports the focused endpoint, full playback and
  userapi package, and targeted race checks passing.
- R1-only `make ratchet-update && make ratchet` passed in
  `/private/tmp/abs-ref-r1-metrics-20261110`, based on HEAD
  `b42969b9431c635ab9032f057c033bc429323d2f` plus only the three R1 source and
  test files. Metrics changed by −2 production LOC and −3 maps; all other
  metrics were unchanged. The shared Wave G baseline was not changed by this run.
- Earlier recovered Wave G logs remain historical: their catalog fixture
  failures predated the approved fixture-schema updates, and realtime's
  full-buffer failure predated RT1. The final combined snapshot supersedes
  those observations; its full suite and rollback passed. The full evidence
  and exact environment corrections are in the [final validation report](docs/refactor-final-validation-20261110.md).

## Next, in order

1. Review PB04c2-D in the shared worktree; do not stage unrelated changes.
2. After acceptance, set PB04c2's budget against the post-D source and dispatch
   the remaining library delete/issues/download/match-all routes.
2. Dispatch/review PB04c2-D, deletion ownership in existing itemwrite.
3. Only then dispatch PB04c2 with exact download/matching seams and its own
   pre-recorded budget: library delete/issues, download and match-all.
4. Then move the progress/store records per the boundary sequence.
5. Publication and remote-CI verification remain the existing N3 gate; live
   deployment stays separately authorized.

## Working rules learned this session

- Snapshot the working tree to a patch after any interruption; workers have been stopped by API limits six times.
- The 5-hour API limit pauses all workers at once; resume them with a message, not a new agent.
- An agent stopped by the owner cannot be resumed; ask before relaunching.
- Run the required local workflow commands and TS rollback before commit/push; then verify remote GitHub CI on the pushed commit. The checked-in command mirror and evidence are in [the final validation report](docs/refactor-final-validation-20261110.md).
- depguard layers: leaf (stdlib only) / foundation / feature; features never import features. dbnest flags any
  identifier named `db`; name transaction parameters `tx`.
- sqlc: `make sqlc SQLC="go run github.com/sqlc-dev/sqlc/cmd/sqlc@v1.31.1"` (regenerates byte-identically).

## Open owner decisions

Single-executable media feasibility (2026-11-09, analysis only): see
[the review](docs/FFMPEG-LIBRARY-FEASIBILITY.md). Static libav/CGO is technically
feasible but changes the build contract; embedded WASM needs codec/performance
proof (published AMR gap). No dependency or runtime migration was made.
Full probe API, HLS and manual audio tools must remain; client/dist also remains
an external runtime asset. Current catalog covers honor FFMPEG_PATH; TESTING.md
was corrected. No candidate build, benchmark or runtime tests were run.

Parked decisions with defaults (O1, O2, O5, MC-C JS client in CI) remain in ROADMAP section 5. No schema migrations, startup DB cleanup or broad filesystem cleanup is authorized. Preserve the TS-proven UUID playback stream cleanup at process startup only; do not run it during restore/runtime replacement. Preserve backup/restore and manual audio-tool flows. Full prior decisions and recovery instructions remain in the dated archive linked above.
