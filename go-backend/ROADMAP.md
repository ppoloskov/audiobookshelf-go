# Roadmap

### UA01 · User API simplification and file ownership (2026-11-10)

- [ ] LOCALLY VERIFIED, PENDING INTEGRATION — owner requested Terra implementation after the package
  review. Preserve `New`, `Handler.Routes`, `Credentials`, `Deps`, all HTTP
  routes/auth/status/JSON semantics, persisted formats and event recipients.
  Scope is `internal/userapi` only; no new package, dependency, schema or
  cross-domain move. Architect owns documentation and final acceptance.
- Test first: characterize changed account/PATCH, statistics and repeated
  update/event paths before replacing their implementations. Split the mixed
  `userapi.go`/`me_extra.go` by concern and collapse demonstrated duplicate work,
  map assertions and redundant branches. Do not count file moves as savings.
- Initial reduction budget returned for architect review: the clear concern
  split costs imports/headers (+49 total LOC in the first candidate) despite
  deleting intermediate rounding maps and redundant bookkeeping. Folding files
  back into miscellaneous buckets was rejected. Revised bound for acceptance:
  at most +60 production LOC for ownership, with reduced total complexity and
  dynamic maps, no growth in functions over 15, `[]any` or injectors. Further
  test-backed collapse of series-state bookkeeping and repeated PATCH type
  validation is required. No adapters/new packages/dependencies/schema changes.
- Checks: focused baseline/characterization, userapi and direct foundations,
  targeted race, full build/vet/tests with documented FFmpeg, lint and ratchet.
  Baselines remain architect-owned. Full CI/rollback is required before any
  publication; unrelated dirty work remains untouched.
- Result: concern files replace `me_extra.go`; shared payload construction,
  typed stats accumulators, direct rank rounding, slice-based series state and
  ordered PATCH validation remove redundant mechanisms. Architect contract
  and simplicity reviews accepted +34 LOC / −17 complexity / −2 functions over
  15 / −6 maps / −1 `[]any`, within the revised ownership budget. Full isolated
  build/vet/test, real Socket.IO and FFmpeg, lint, focused races, ratchet/dbnest
  and formatting pass. [Evidence and limitations](docs/USERAPI-SIMPLIFICATION.md).
  Commit/push and combined CI/rollback remain pending; mark DONE in that commit.

### PC01 · Podcasts simplification and ownership (2026-11-10)

- [x] DONE — implemented by the requested Terra worker; architect contract and
  simplicity reviews accepted. Public interfaces and shared read models are
  unchanged. Isolated full build/vet/suite/lint, dependency/source checks,
  ratchet/dbnest, SQLC freshness, both Linux builds and TS rollback passed.
  Focused races: 34 top-level tests, 65 including subtests, zero skips.
  Net reduction: 10 production lines, 27 complexity points, one function over
  15 and nine dynamic maps, with every other ratchet metric unchanged.
  [Implementation evidence and baseline skips](docs/PODCASTS-SIMPLIFICATION-REVIEW.md#pc01-implementation-and-acceptance--2026-11-10).
- Original authorization: owner requested implementation with a Terra worker.
  Assessment and retained contracts are in
  [the review](docs/PODCASTS-SIMPLIFICATION-REVIEW.md). Keep public interfaces
  and shared read models. Implementation sequence:
  characterize PATCH/matching/error gaps, use typed matching and simpler update
  construction, remove duplicate probing/completion work, then separate file
  responsibilities. No new dependencies/features/removals or net savings are
  claimed before measurement. Both podcast packages passed the focused race
  baseline (29 top-level tests; zero skips). PC01 owns only internal/podcasts
  implementation/tests and its documentation; preserve unrelated PB04 work.
- Pre-dispatch metric budget: no growth in any ratchet metric; net production
  LOC/complexity reduction required, excluding relocation credit. Benefit:
  concrete typed matching/update flow, fewer duplicate operations and clearer
  ownership without new packages/dependencies. Delete replaced map/accessor,
  SQL construction and probe/completion duplication; retain the existing public
  map-returning feed wrapper. No new adapter or deferred exit task is planned.
  Architect acceptance included focused race/contract tests, full build/vet/
  suite/lint, dependency checks, ratchet/dbnest and persistence rollback proof.

### PB01 · Package and object ownership review (2026-11-10)

- [ ] IN PROGRESS — owner requested better package division and object
  boundaries. [Source-backed review](docs/PACKAGE-BOUNDARIES.md) records
  catalog scope, misleading helper ownership, dependency-rule pressure,
  proposed owners and migration sequence. First runtime packet ACCEPTED
  2026-11-10: email defaults and e-reader device policy moved from
  `settingsdoc` to `internal/emailsettings`; `emailapi`, `identity` and
  `userapi` consume it, and generic settings-row `Load`/`Save` remain in
  `settingsdoc`. Second runtime packet ACCEPTED
  2026-11-10: the email-settings document is now typed `emailsettings.Settings`
  with json tags; PATCH decodes onto the loaded struct, device maps retain
  unknown fields, the SMTP password is redacted by `String`, `settingsdoc`
  adds `LoadInto` and saves any JSON value, and the unchecked
  `device["email"].(string)` assertion is removed. Architect rerun verified:
  gofmt, build, vet, the full suite with the documented FFmpeg prefix,
  targeted `-race` on all five affected packages, golangci-lint v2.15.0
  (0 issues), 20 dependency fixtures, `make ratchet` +0 and dbnest.
- Accepted growth for the typed packet after the port-parity fix:
  +115 handwritten LOC and +13 complexity, with `map[string]any` −10,
  `[]any` −6 and functions over 15 −1; the baseline is updated only for those
  measured fields. Catalog
  decomposition remains unstarted and requires explicit handover.
- Port parity restored: stored numeric ports load and save verbatim
  (`99999`, `-1`, `12.5`); PATCH now applies upstream `Number()` semantics
  instead of the old Go clamp/truncation (`""`→0, `"1e2"`→100, `"0x10"`→16,
  `true`→1, `[]`→0, `[5]`→5, invalid/null/`{}`→465). The clamp removal is a
  parity restoration, not a new behaviour choice.
- Residual risks: a malformed stored `ereaderDevices` non-object element now
  returns 500 instead of being ignored; TS and Go write paths validate before
  saving, so this is reachable only through manually corrupted rows. Separate
  parity nit: `CleanDevices`/`VisibleTo` filter `name`/`email` with `!= nil`,
  while TS uses truthiness (`""` is filtered by TS but kept by Go); this is
  pre-existing from the accepted policy packet and is not changed here.
- Documentation validation only: source/test/lint inspection and targeted
  whitespace/link checks. Existing implementation cards retain their state.

### PB02 · Catalog read graph (2026-11-10)

- [x] DONE 2026-11-10 — first bounded slice ACCEPTED at `7c2a1c0e`: `Access`,
  its permission-only `LoadAccess` query and `ItemAccess`/`CanReadItem` now
  live in `internal/access`; `libitem` item envelopes expose
  `access.Access`. This removed the `access` → `libitem` dependency;
  `libitem` now depends on `access`. DONE with the fourth slice below: the
  catalog read graph is complete (`access` permissions, `items`
  composition, `books` and `podcasts/read` media owners, `mediafiles`
  shared types, `libitem` deleted) and every architect-verified check
  passed on each slice.
- [ ] IN PROGRESS — second bounded slice ACCEPTED 2026-11-10: common
  library-item identity, the
  batched loader and the common response envelopes (`Item`, `Options`,
  `Load`/`LoadOne`, `Head`, `Full`, `Expanded`, `Minified`, `Extras`,
  `JSONNull`, `ListRow`) moved from `libitem` into `internal/items`.
  `libitem` now owns book/podcast media models and exported media
  projections (`Book.Full/Minified/Expanded`, `Podcast.Media`), which
  `items` composes without a second loader or model. No route, JSON,
  permission, persistence, query-count or event behaviour changed.
  Architect rerun verified: gofmt, build, vet, the full suite with the
  documented FFmpeg prefix, `-race` across all eleven affected packages,
  golangci-lint v2.15.0 (0 issues), 21 dependency fixtures,
  `make ratchet` +0 (LOC baseline 38,258) and dbnest.
- Accepted growth for PB02b: +24 handwritten LOC with complexity,
  high-complexity functions, maps and `[]any` unchanged; only the LOC
  baseline field was updated.
- [ ] IN PROGRESS — third bounded slice ACCEPTED 2026-11-10: `Book`, book author/series
  associations, book projections and `LoadMany` moved from
  `libitem`/`items` into `internal/books`. `items` now depends on concrete
  book reads; `libitem` retains only podcast media/projections. The shared
  `store.QueryIn` chunked-query helper owns batching for both item and book
  reads. No route, JSON, permission, persistence, query-count or event
  behaviour changed. Architect rerun verified: gofmt, build, vet, the full
  suite with the documented FFmpeg prefix, `-race` across all twelve
  affected packages, golangci-lint v2.15.0 (0 issues), 22 dependency
  fixtures, `make ratchet` +0 and dbnest. Accepted deviation: `books` has
  no package-local tests yet; the items integration tests cover `LoadMany`
  and every book projection.
- Accepted PB02c delta: +29 handwritten LOC and −2 complexity, with
  high-complexity functions, maps and `[]any` unchanged. Only the LOC and
  complexity baseline fields were updated.
- [ ] IN PROGRESS — fourth bounded slice ACCEPTED at `a8e1b111`: podcast media ownership moved into
  the foundation read subpackage `internal/podcasts/read`; the existing
  `internal/podcasts` feature keeps HTTP/write paths and consumes the read
  package, so `items` imports a foundation rather than a feature. Shared
  `File`/`AudioFile`/`FileMetadata`/`AudioChapter` moved to the
  `internal/mediafiles` leaf. `libitem` is deleted. `items` composes
  `books` + `podcastread` + `mediafiles`; no feature-to-feature import or
  cycle remains. Architect rerun verified: gofmt, build, vet, the full
  suite with the documented FFmpeg prefix (49 packages), `-race` across
  all thirteen affected packages, golangci-lint v2.15.0 (0 issues), 24
  dependency fixtures, `make ratchet` +0 and dbnest. Review rulings:
  `mediafiles` approved as the shared file/audio representation leaf
  (consumed by books, podcasts/read, items, scanner and audiotools)
  rather than `store`, which stays connection/lifecycle-only;
  `podcasts/read` approved as a foundation read subpackage with
  fixture rules both ways (`items` → `podcasts/read` allowed,
  `podcasts/read` → feature rejected). Architect acceptance restored the
  items row-decode/access wiring assertions dropped by the test split
  (`TestItemsRowDecodingAndAccess`: extraData `oldLibraryItemId`,
  libraryFiles fileType derivation, `Item.Access()` book wiring); no
  metric change, as test lines are outside the ratchet's production LOC.
- Test placement closed with the split: `books` has direct `LoadMany` and
  projection tests, `podcasts/read` has direct loader/projection/episode
  tests, and `items` retains common item composition/list-row/query
  tests plus the restored row-decode/access wiring test. No mixed-media
  test package remains.
- Accepted PB02d delta: +15 handwritten LOC with complexity,
  high-complexity functions, maps and `[]any` unchanged. Only the LOC
  baseline field was updated. Checks: gofmt, build, vet, affected package
  tests, full suite with the documented FFmpeg prefix, targeted races,
  golangci-lint (0 issues), 24 dependency fixtures, `make ratchet` and
  dbnest.
- The catalog read graph is complete. Next after explicit handover:
  people (author/series) ownership, then library browsing/search/shelves,
  per the package-boundary sequence.

### PB03 · People — author/series entity ownership (2026-11-10)

- [ ] IN PROGRESS — first people slice ACCEPTED 2026-11-10: author/series
  records and views, detail, update/rename, merge/delete and
  metadata-provider matching moved from catalog into `internal/people`.
  `catalog` keeps library-scoped author/series list routes and author image
  routes until the next slice; `itemwrite` remains the owner of relation
  cleanup/orphan sweeps. No route, JSON, permission, persistence or event
  behaviour changed for the moved endpoints. Architect rerun verified:
  gofmt, build, vet, the full suite with the documented FFmpeg prefix,
  `-race` across the nine affected packages, golangci-lint v2.15.0
  (0 issues), 26 dependency fixtures, `make ratchet` +0 and dbnest.
  Review rulings: the standalone `people` package is justified by
  measurement (1,149 production lines, well over the agreed ~400-line
  fold bar; its consumers are catalog and the runtime, not books), and
  the test coverage is the catalog rig, which constructs `people.New`
  with `Deps` and asserts people event payloads directly.
- Accepted PB03a delta: +141 handwritten LOC and +26 complexity, with maps,
  `[]any`, high-complexity functions and injector metrics unchanged. The
  growth buys the 111-line `people.Deps` composition surface, the shared
  bounded `httputil.DecodeObjectFields` body decoder and the runtime
  wiring that removes catalog's `AuthorFinder` dependency; no smaller
  alternative preserves the ownership split. Adapter exit task: the
  `SaveAuthorImage`/`PurgeImageCache` callbacks dissolve when the author
  image routes move into `people`; `WriteMetadata`/`Filters` stay as
  scanner/filterdata collaborators. Process note for later packets: the
  per-metric growth budget must be recorded in the card before dispatch,
  not reconciled after.
- Next slice after explicit handover (PB04): move the library-scoped
  author/series list routes and author image routes to their proper
  owners, then continue with library browsing/search/shelves. PB04's
  acceptance conditions: (a) the author/series test rig and tests move to
  the final owner when catalog's author surface fully dissolves, and
  (b) the people rig supplies a recording `WriteMetadata` callback
  instead of relying on its nil-skip, so a runtime wiring omission
  cannot silently skip metadata writes.

### PB04 · Library browsing/search/shelves + people remainder (2026-11-10)

- [ ] PROPOSED SPLIT — source inventory shows the requested surface is too
  coupled for one bounded ownership move: people lists/images still depend on
  catalog library-scope and cover helpers, while browsing/filter/search/shelves
  share `query.go`, item loaders, collapsed-series and shelf projection code.
  Proposed coherent packets, in order:
  1. **PB04a — people remainder.** Move `/api/libraries/:id/authors` and
     `/series` list handlers, series filter helpers, and author image routes
     into `internal/people`; move the author/series test rig and tests to
     people; replace the people `WriteMetadata` nil-skip with a recording test
     callback. No library browse/search code moves.
  2. **PB04b — library scope and browsing.** Move library scope/`OpenLibrary`
     middleware, library read/detail/filterdata/stats, item-list browsing and
     the filter/sort SQL compiler into `internal/library`, preserving query
     counts and endpoint shapes.
  3. **PB04c — shelves and search.** Move personalized shelves, collapsed
     series/subseries and library search into `internal/library`; keep library
     writes/admin (create/update/delete/reorder, OPML, download, match-all)
     in catalog for a later packet rather than turning `library` into a
     dumping ground.
- [ ] IN PROGRESS — PB04a ACCEPTED 2026-11-11: library-scoped author/series list
  handlers, series filter seam, author image routes, author image helpers and
  the author/series test rig moved into `internal/people`; the recording
  `WriteMetadata` callback is now supplied by the people test rig. Shared
  image file/stream helpers moved to `httputil`/`mediatools`. The first
  `internal/library` slice (request scope/`InLibrary`, `MediaTable`) is in
  place for the browsing packet. Catalog routes for the moved endpoints are
  removed. No route, JSON, permission, persistence or event behavior changed.
  Architect rerun verified: gofmt, build, vet, the full suite with the
  documented FFmpeg prefix (50 packages), `-race` across the nine affected
  packages, golangci-lint v2.15.0 (0 issues), 28 dependency fixtures,
  `make ratchet` +0 and dbnest. Both PB03a contract conditions are closed
  (rig move with a recording `WriteMetadata` callback; image-callback exit
  completed — `PurgeImageCache`/`SaveAuthorImage` deleted from `Deps`).
- Actual deltas (reconciled, 2026-11-11): PB04a alone is +130 handwritten
  LOC combined-baseline edit = +96 LOC / +15 complexity / all other metrics
  unchanged for PB04a, plus the concurrently integrated UA01 userapi
  simplification (+34 LOC / −17 complexity / −2 functions over 15 /
  −6 `map[string]any` / −1 `[]any`). The PB04 worker's reported numbers
  (+96 / +15, others unchanged) were correct in isolation; the baseline
  file correctly carries the combined total. An earlier architect note
  attributing the gap to stale-prior arithmetic was wrong and is
  retracted.
- Owner ruling (2026-11-11): series is not people. Upstream `Series` has no
  person-like operations (no image/matching/merge) and is library-scoped
  (`libraryId` FK, `UNIQUE(name, libraryId)`); the series list routes are
  LibraryController operations upstream. Series routes move people → library
  in PB04b (the `SeriesMatchesFilter` exit completes there with the
  filter/sort compiler); series data stays in `books` (`SeriesRef`,
  sequence, seriesName). Books cannot own the routes: the series view
  composes `items.Minified` and `items` → `books` would cycle. `people`
  ends as authors-only. Owner may veto before PB04b dispatch.
- Next: PB04b library browsing/search plus the series move per the ruling,
  keeping library writes/admin in catalog until a later packet.
- PB04b decomposition (proposed before any PB04b move, 2026-11-11): the
  browsing/search/read surface does not fit one coherent ownership packet. It
  splits into:
  1. **PB04b1 — query compiler and series**: `library` owns the filter/sort
     SQL compiler and the series entity/list/detail/update routes; `people`
     becomes authors-only; `SeriesMatchesFilter` is deleted. Catalog item
     browsing/search keeps calling the compiler until its own move.
  2. **PB04b2 — browse and library reads**: item-list browsing, library
     read/detail/filterdata/stats and the narrator/podcast-title read routes.
     Library view types become public for catalog write responses; narrator
     updates and all library/admin writes stay in catalog.
  3. **PB04b3 — discovery**: library search and personalized shelves. Item
     detail, cover/file routes and item mutations stay in catalog.
  Each slice is independently verifiable and keeps writes/admin out. PB04b1
  budget was **+120 handwritten LOC / +25 complexity**, with zero growth in
  functions over 15, maps, `[]any` and injector metrics.
- [ ] IN PROGRESS — PB04b1 ACCEPTED 2026-11-11: `internal/library` owns the filter/sort
  SQL compiler and the series entity/list/detail/update routes; `people` is
  authors-only and the `SeriesMatchesFilter` callback is deleted. Catalog
  browsing/search still calls the library compiler until PB04b2/b3. Support
  extractions: `feeds.LoadMinified`, `httputil.NormalizedInclude`/`IncludeHas`
  and the generic `sqlupdate.Update` used by author and series edits. Series
  tests moved to `internal/library`, including list/filter/include/pagination
  coverage. Actual delta: **+53 LOC / −2 complexity / −1 `[]any`**,
  with functions over 15, maps and injector metrics unchanged — under the
  pre-recorded +120/+25 budget on both fields. Architect rerun verified:
  gofmt, build, vet, the full suite with the documented FFmpeg prefix
  (51 packages), `-race` across the affected packages, golangci-lint
  v2.15.0 (0 issues), 32 dependency fixtures, `make ratchet` +0 and
  dbnest. Review rulings: the compiler move is a pure rename-plus-export
  (`filterSQL`/`orderSQL` → `FilterSQL`/`OrderSQL`); the changed-only
  `series_updated` event contract is preserved (`Update.Change` gates the
  emit); `sqlupdate` passes the new-package rule (two consumers, one
  Sequelize-`changed()` concept; folding it into `store` was the
  acceptable alternative and is not worth churn). No route, JSON,
  permission, persistence or event behavior changed. Next after acceptance:
  PB04b2 browse/library reads, then PB04b3 search/shelves.
- PB04b2 split before moving (2026-11-11): item browsing is coupled to the
  collapse-series/subseries queries and the item `rssFeed`/`mediaItemShare`
  include seam, while library metadata reads stand alone. It splits into
  **b2a** (libraries/library detail, stats, filterdata, narrator and podcast
  title reads, plus public library view types for catalog write responses) and
  **b2b** (item-list browsing with collapse helpers and item includes). Writes
  and admin remain in catalog. b2a budget: **+100 LOC / +20 complexity**, zero
  growth in functions over 15, maps, `[]any` and injector metrics.
- [ ] IN PROGRESS — PB04b2a ACCEPTED 2026-11-11: `internal/library` owns `GET
  /api/libraries`, library detail/view, stats, filterdata and the narrator and
  podcast-title read routes; the public `View`/`FolderView` shapes serve
  catalog write responses through two type aliases and one `ReadView`
  delegation. Catalog keeps narrator PATCH/DELETE and all library/admin
  writes. Read tests moved to `internal/library`; catalog's
  mutation/invalidation tests mount the library handler where they verify
  write side effects. Actual delta: **+52 LOC / +5 complexity**, with
  functions over 15, maps, `[]any` and injectors unchanged — within the
  pre-recorded +100/+20 budget. Architect rerun verified: gofmt, build,
  vet, the full suite with the documented FFmpeg prefix (51 packages),
  `-race` on library/catalog/server/people, golangci-lint v2.15.0
  (0 issues), 32 dependency fixtures, `make ratchet` +0 and dbnest.
  Route registrations moved exactly; the runtime passes the same
  `filterdata.Cache` instance to both handlers, so catalog write
  invalidation still clears library reads. Exit task recorded: the
  catalog alias file (`library_view.go`, 16 lines) dissolves when library
  writes/admin move. Next after acceptance: PB04b2b item-list browsing,
  then PB04b3 search/shelves.
- [ ] IN PROGRESS — PB04b2b ACCEPTED 2026-11-11: `internal/library` owns the item-list
  route, collapse-series/subseries helpers and the item rssFeed/mediaItemShare
  include seam. `books.SeriesRef` replaces the duplicate `people.BookSeries`
  representation (identical json tags, shape-safe dedup); `catalog` keeps
  personalized shelves, search, item detail, cover/file routes and item
  mutations. Catalog's item-list/query integration tests mount the library
  handler; library owns the collapse/subseries tests. Actual delta: **+3 LOC
  / +2 complexity**, with functions over 15, maps, `[]any` and injectors
  unchanged. Architect rerun verified: gofmt, build, vet, the full suite
  with the documented FFmpeg prefix (51 packages), `-race` on
  library/catalog/server/people/items/sharing, golangci-lint v2.15.0
  (0 issues), 32 dependency fixtures, `make ratchet` +0 and dbnest.
  The admin-only mediaItemShare gate, feed lookup and expiry/null behavior
  were verified as verbatim. Notes: (1) no separate b2b budget was recorded
  before dispatch — self-disclosed; the trivial actual growth is ratified
  this once, and every packet records its budget before dispatch from now
  on, small ones included; (2) exit task: the catalog-side item-list/query
  fixtures move to library (or dissolve) when catalog's read surface is
  empty after PB04b3 and the writes/admin packet. Next after acceptance:
  PB04b3 search/shelves, then library writes/admin.
- [ ] IN PROGRESS — PB04b3 ACCEPTED 2026-11-11: `internal/library` owns library
  search, personalized shelves, the shelf/search DTOs and the library-scoped
  author list. The author list moved from people so library can consume
  `people.AuthorRecord`/`AuthorView` without the old people → library edge;
  people is now author entities/CRUD/images/matching only, and library
  imports people (`go list -deps` verified acyclic; the allow-fixture was
  flipped to match). Catalog production now contains only library
  writes/admin, item detail/cover/file routes and item mutations. Actual
  delta: **+3 LOC / +2 complexity**, with functions over 15, maps, `[]any`
  and injectors unchanged. Architect rerun verified: gofmt, build, vet, the
  full suite with the documented FFmpeg prefix (51 packages), `-race` on
  library/catalog/server/people/items, golangci-lint v2.15.0 (0 issues), 32
  dependency fixtures, `make ratchet` +0 and dbnest. Search limits/defaults,
  shelf ordering, admin-only share gates, progress filtering and author
  pagination were spot-verified as verbatim. Standing rule from the next
  packet: this is the third self-disclosed missing pre-dispatch budget
  (PB03a, b2b, b3), each ratified on trivial actuals — from the writes/admin
  packet onward, a missing pre-recorded budget returns the packet for
  revision regardless of actual growth. Next after acceptance: library
  writes/admin (dissolving the catalog `libraryView` alias and moving the
  remaining item-list fixtures), then the progress/store records.
- PB04c split before moving (2026-11-11): library delete/issues are coupled to
  catalog item deletion rows, sweeps and playlist/feed/cover events; library
  download needs catalog item-scope/access helpers; match-all starts the item
  quick-match task. These do not fit one coherent write packet. Split:
  1. **PB04c1 — library writes/admin reads**: create, update, reorder, narrator
     update, OPML and remove-metadata; library view aliases dissolve. Update's
     folder-removal branch keeps one explicit `RemoveFolderItems` composition
     callback until item deletion ownership moves.
  2. **PB04c2 — library delete/issues/download/match-all**: move only after the
     item-deletion/quick-match dependency is resolved or a transaction-safe
     seam is accepted. Catalog keeps those routes until then.
  c1 budget: **+120 LOC / +25 complexity**, zero growth in functions over 15,
  maps, `[]any` and injector metrics. c2 budget is set at dispatch.
- [x] DONE — PB04c1 accepted at `cf3990d0` (2026-11-11): `internal/library` owns library
  create, update, reorder, narrator update, OPML and remove-metadata. The
  catalog `libraryView`/`libraryFolderView` alias file is deleted (its exit
  task completed); catalog delete uses `library.ReadView` directly. Update's
  folder-removal branch calls the composition `RemoveFolderItems` callback
  implemented by catalog (`library_delete.go`), with the exit task when item
  deletion moves; an unwired callback fails loudly with a 500 instead of
  silently skipping. Review correction: the library write test supplies a
  no-op callback and never exercises removal; PB04c1-R below closes that gap.
  Catalog retains delete/issues, download and match-all as PB04c2. Actual
  delta: **+51 LOC / +9 complexity**, with functions over 15, maps, `[]any`
  and injectors unchanged — within the pre-recorded +120/+25 budget. Architect
  rerun verified: gofmt, build, vet, the full suite with the documented FFmpeg
  prefix (51 packages), `-race` on library/catalog/server/items/scanner,
  golangci-lint v2.15.0 (0 issues), 32 dependency fixtures, `make ratchet`
  +0 and dbnest. The c-split and budget were recorded before moving, as the
  standing rule requires. The accepted extraction preserved an inherited
  removal-only update defect; it is not evidence that that behavior is correct.
- [ ] IN PROGRESS — PB04c1-R ACCEPTED 2026-11-11: removal-only folder PATCH now
  counts removed folders as a real update, so it deletes the folder row inside
  the library transaction, refreshes watchers and emits `library_updated`
  after commit. Tests cover removal, kept-folder retention, callback
  arguments, callback error, missing wiring and repeat idempotence. Actual
  delta: **+0 LOC / +1 complexity**, with every other ratchet metric
  unchanged — within the pre-recorded +5/+2 ceiling. The production fix is
  one line (`removedFolders` joins `hasUpdates`); everything else it restores
  already existed downstream in c1. Architect rerun verified: gofmt, build,
  vet, the full suite with the documented FFmpeg prefix (51 packages),
  `-race` on library/catalog/server, golangci-lint v2.15.0 (0 issues), 32
  dependency fixtures, `make ratchet` +0, dbnest and `git diff --check`;
  the fix was cross-checked against upstream
  (LibraryController.js:475-511: folder removal sets hasFolderUpdates,
  destroys the folder row, refreshes and emits once). The `RemoveFolderItems`
  bridge remains until PB04c2-D. The c1 review correction in this card is
  accurate: the gate review found both the inherited defect the PB04c1
  architect review missed and the no-op callback mislabeled as recording
  coverage; both are now closed.
- [ ] IN PROGRESS — PB04c2-D ACCEPTED 2026-11-11: deletion rows/results moved to
  `internal/itemwrite.DeleteRows(ctx, tx, targets)` with caller-owned
  transactions and no pool/files/cache/event access. Concrete soft/hard
  operations and folder deletion run post-commit phases; catalog single/batch/
  issues and whole-library callers were mapped to the owner with their
  distinct schedules preserved (single/folder: sweep+issues-reset per item;
  batch: series then authors per item; issues: accumulate, sweep once).
  The `RemoveFolderItems` bridge is deleted; library folder update calls
  `itemwrite.Deleter.DeleteFolderSoft` directly before its own transaction.
  Shared path safety and cover-cache purge moved once to `mediafiles` with
  direct tests. Whole-library deletion keeps its enclosing transaction and
  effect order. The rollback proof landed exactly as the decision required:
  a trigger fails `DELETE FROM libraries` after item rows were removed → 500,
  every row survives, no watcher call, events, cache purge or metadata-file
  removal. Actual delta: **+109 LOC / +14 complexity / −1 map**, with
  functions over 15, `[]any` and other metrics unchanged — within the
  pre-recorded +120/+25 ceiling. Architect rerun verified: gofmt, build,
  vet, the full suite with the documented FFmpeg prefix (52 packages),
  `-race` on itemwrite/library/catalog/server/mediafiles/scanner,
  golangci-lint v2.15.0 (0 issues), 35 dependency fixtures, `make ratchet`
  +0 and dbnest. Residual risk recorded: `DeleteRows` skips media-specific
  reference cleanup for an unknown media type where the old single-item path
  returned 400 (the single route still checks `mediaTable()`; only a corrupt
  row in folder deletion could reach it — unreachable through validated
  writes). Stop for review before c2.
- [ ] PB04c2 — **GATED**, not merely queued. Architect selects deletion-owner
  first, not another independently committing deletion callback. A typed
  per-item quick-match operation seam is accepted in principle; library owns
  selection/task orchestration. Resolve download selector/access ownership
  with exact callers at dispatch. No active c2 budget: set it against the
  accepted post-D source when dispatching; prerequisites do not auto-start c2.
  After c2 acceptance, close the sequence with progress/store records.
  Architect ratification 2026-11-11: the
  [gate decision](docs/PB04C2-ARCHITECTURE.md) is accepted as the c2 gate
  finding — deletion mechanics to `itemwrite` with caller-owned transactions
  and post-commit effects (no independently committing deletion inside a
  library transaction), the `QuickMatchItem` typed seam, and the download
  ownership inventory at dispatch. PB04c2-D is authorized to dispatch under
  its recorded +120/+25 ceiling with the exact write scope in the decision;
  stop for review after that one ownership change.

### UA01 · User API simplification (2026-11-10)

- [x] DONE — accepted by integration 2026-11-11 in `c7b7d1d9` (the PB04a
  acceptance commit). UA01 was implemented locally by the owner-requested
  Terra worker against `bf72cb3c`, validated in an isolated clean source
  per its [record](docs/USERAPI-SIMPLIFICATION.md), and ratified by the
  architect after the fact: full suite, lint, ratchet, dbnest, fixtures and
  a rerun `-race` on userapi/users all pass on the combined tree. Its own
  isolated delta (+34 LOC / −17 complexity / −2 over-15 / −6 maps /
  −1 `[]any`) is included in the `c7b7d1d9` baseline update. The file split
  (`me_extra.go` → topical files, `userapi.go` facade retained) and the
  six listed simplifications are behavior-preserving per the documented
  counterexample review; no route, JSON, schema, token or event change.
- Process record: two packets landed in one commit because the shared
  checkout gained UA01 mid-review of PB04a. Integration mistakes are
  recorded, not rewritten. Going forward the architect stages explicit
  paths only (no `git add -A` on the shared checkout), and workers must
  publish or quarantine their tree changes before another packet's review
  begins.

Updated 2026-11-09. Open work only. Finished and obsolete cards are listed in
[archive/reviews/2026-11-09-roadmap-pruned.md](archive/reviews/2026-11-09-roadmap-pruned.md);
older detail in [the pre-takeover roadmap](archive/reviews/2026-11-04-roadmap-before-takeover.md).
Current state: [handover.md](handover.md). Permanent contracts:
[ARCHITECTURE.md](ARCHITECTURE.md). Worker/check policy: [AGENTS.md](AGENTS.md).
Decision log and ledger: [docs/REFACTOR-LOG.md](docs/REFACTOR-LOG.md),
[docs/SIMPLIFICATION.md](docs/SIMPLIFICATION.md).

All development is on `master`; no branches. Every push is gated: full local
CI mirror plus the TS rollback check, chained (`ci-local.sh && rollback && git
commit && git push`). Manual deployment, new libraries and route removals need
separate owner approval. Every open card has a `- [ ]` checkbox; mark it
`- [x] DONE` in the commit that completes it, with actual check results.
Preserve functionality; package moves and feature removal are not savings.

## 1. Refactor work, in order (handover.md "Next, in order")

### N1 · Step 3 behavioural test-quality pass

- [x] DONE — Step 3 complete at `b42969b9`; T1–T5 salvage and assertion audit
  are recorded in `handover.md` and archived review. Existing accepted tests,
  fixtures, and compatibility assertions remain part of the contract.

### N2 · Wave G transition and side-effect audit

- [x] DONE — all 15 G1–G6 transitions have named tests. Eight initially red
  transitions pass with bounded fixes; seven passed without production changes.
  The HLS playlist suffix and zero-time reset-event assertions pass with real
  media and playback race coverage.
- **Final canonical validation (2026-11-10):** the full Go suite passes with
  real Socket.IO required, along with build, vet, golangci-lint v2.15.0,
  ratchet/dbnest, SQLC v1.31.1, Linux amd64/arm64 builds, required
  source/contract checks and TS v2.37.2 rollback. See the final validation
  addendum for exact commands, hashes and environment limits.
- The measured canonical metrics are 38,119 handwritten LOC / 8,531
  complexity / 115 functions over 15 / 461 `map[string]any` / 202 `[]any`.
  N2's completion is recorded in the accepted source commit. Remote CI is
  verified after push under N3; deployment remains separately authorized.

### R1 · Remove the OpenSessions JSON round trip

- [x] DONE — pass the typed session DTO slice through the userapi callback as
  opaque JSON and remove the marshal/unmarshal map conversion and ignored
  errors. Preserve the nonnil array shape, explicit nullable fields and admin
  authorization gate. The typed endpoint regression, full userapi/playback
  package tests and targeted races pass. R1 independently reduces production
  LOC by 2 and maps by 3; all other metrics are unchanged.
- Comment cleanup and test cleanup were not selected.

### RT1 · Abort full realtime transport queues promptly

- [x] DONE — abort promptly when a bounded outbound queue fills, keeping normal
  close graceful, preserving the first close reason and keeping sends
  nonblocking. The unchanged full-buffer regression, realtime package/race
  tests, and final canonical full suite pass. Actual growth is +16 LOC / +2
  complexity within the assigned +30/+8 budget; all other metrics are
  unchanged.

### QM01 · Preserve quickmatch and relation metadata semantics

- [x] DONE — preserve existing relations for empty converted match lists and
  apply duplicate series updates sequentially against the current relation
  cache. Missing sequence metadata preserves the stored position; explicitly
  missing, null, empty text and numeric text remain distinct. The real adapter
  stores raw numeric zero as `"0"`; the prior zero test used a fake finder and
  bypassed that adapter, so it was corrected. PATCH still maps missing and
  non-string values to explicit NULL while string values, including empty,
  remain explicit text; scanner blanks remain absent.
- The shared Series input carries presence as `*sql.NullString`; KeepSequence
  and done-map branches were removed after the symbol check. The existing
  Go characterization retains event ordering, with no second SQL engine or
  provider normalization change.
- **Budget:** assigned +50 LOC/+12 complexity, zero growth in high-complexity
  functions, maps, `[]any`, dependencies and schema. Actual was +26 LOC/+5
  complexity with every other ratchet metric unchanged.
- **Check:** focused empty-list, duplicate (2 then 1), missing-sequence,
  rollback, raw-value and PATCH regressions pass; catalog/itemwrite/scanner
  package tests and races pass, as do the final canonical full suite and TS
  rollback.

### N3 · Step 4, final check and report

- [ ] PENDING REMOTE CI — canonical local verification and the final validation
  report are complete. Commit and push the accepted source tree to `master`,
  then verify the required GitHub CI jobs on that pushed commit. Keep N3 open
  until remote CI passes; deployment remains separate.
