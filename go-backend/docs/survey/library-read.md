All sizes are non-test lines at the current checkout; nothing was modified.

**1. RESPONSIBILITIES**
- library (4999 lines): library CRUD and scope, filter/sort SQL compiler, item list, personalized shelves, search, series, authors, narrators, stats, OPML.
- filterdata (346): in-memory filter-menu cache with 30 min TTL and in-place patch methods.
- items (352): library-item loader (5-6 queries) plus Full/Expanded/Minified envelopes.
- books (336): book row loader plus three projections.
- podcasts/read (307): same for podcasts and episodes.
- mediafiles (226): `File` (a `map[string]any`), typed `AudioFile`/`FileMetadata` (used only by the scanner), path-in-library check, cover-cache purge.
- textsort (131): ICU-collation emulation plus sorting-prefix logic.
- feeds (207): RSS feed JSON (minified and full) and `CloseEntity`.
- playlistview (130): expanded playlist JSON plus `Announce` events.
- Only library and the series PATCH register routes:
  - `GET,POST /api/libraries`; `POST /api/libraries/order`; `GET,PATCH /api/libraries/{id}`.
  - `GET /api/libraries/{id}/` followed by `stats|filterdata|opml|narrators|podcast-titles|authors|items|personalized|search|series|series/{seriesId}`.
  - `POST …/remove-metadata`; `PATCH,DELETE …/narrators/{narratorId}`; `GET,PATCH /api/series/{id}`.
  - `DELETE /api/libraries/{id}`, matchall, download and issues are in catalog, not here.

**2. TABLES**
- Read: libraries, libraryFolders, libraryItems, books, podcasts, podcastEpisodes, authors, bookAuthors, series, bookSeries, mediaProgresses, feeds/feedEpisodes, mediaItemShares, playlists/playlistMediaItems, customMetadataProviders, settings (via serversettings).
- Written: libraries, libraryFolders, books.narrators, series (name, description), feeds (delete in `CloseEntity`). Folder removal goes through `itemwrite.Deleter.DeleteFolderSoft`.

**3. COUPLING**
- `library.Deps` has three members:
  - `Filters *filterdata.Cache` is a pointer shared with scanner, itemwrite, people and catalog. They patch it in place; library invalidates it after narrator edits.
  - `LibraryChanged func(string)` calls `scanner.Refresh`. It is a callback only because library must not import scanner.
  - `ItemDeleter itemwrite.Deleter` is a concrete struct.
- `events.Emitter` is used for `library_added`, `library_updated`, `series_updated` and `items_updated`, all via `ToAll`.
- `playlistview.Announce` takes an `Emitter` and sends `playlist_*` events with `ToUser`.
- `catalog` imports `library.InLibrary`, `Scope`, `ReadView`, `OpenMediaItemShare` and `MediaTableOf`.
- `items` is imported by 9 packages, `textsort` by 11 and `mediafiles` by 8, so they are shared leaves.
- There is no interface indirection. The coupling is direct imports plus the shared `*filterdata.Cache`.

**4. TESTS**
- Library tests total 913 lines.
  - The route tests are black-box through a mux: series_test, writes_test, the collapse page test, and the 2 route tests in read_test.
  - White-box tests are `seriesSequenceList`, the Node base64 decode, shelf encoding and `ReadView` (about 20%).
- Most of the item-list, personalized, search and filter behaviour is tested outside the package, via `library.New(...).Routes(mux)`.
  - Catalog: personalized_golden (byte-recorded), personalized_independent, query_test, query_independent, item_includes (about 830 lines).
  - contracttest/journey_library_test (266 lines).
  - About 80% of that coverage is black-box and survives a rewrite. The goldens compare bytes, so key order matters.
- items, books and podcasts/read tests go through `Load` and the projections, so they are semi-black-box. `items` asserts a constant query count.
- filterdata is white-box on the cache (a public API). textsort is Node-fixture driven (`testdata/collation.json`, 279 KB). mediafiles has 51 lines. feeds and playlistview have no tests.

**5. COMPLEXITY SOURCES**
1. `library/writes.go:28-475` (about 450 lines):
   - Settings are a `map[string]any` with a per-key type switch, `reflect.DeepEqual` change detection and dynamic `SET` building.
   - `requestString` coerces a number to a string.
   - `Settings` in scope.go (4 fields) and `View.Settings map[string]any` duplicate it.
2. `collapse_subseries.go` plus `collapse_types.go` (about 510): in-memory re-implementation of `libraryHelpers`.
   - It uses natural sort, fast-sort tie semantics and a `seriesSequenceList` range merger.
   - Unknown sorts deliberately evaluate to `''`.
3. `personalized.go` plus `personalized_books.go` (about 630): a shelf engine built on closures, a query-text memo and `[]any` entities.
   - It contains `jsParseFloat` and a continue-series per-series loop.
   - Each `*Series*` scan is duplicated.
4. `query.go:1-258`: SQL fragments in maps with `$v`/`$u` markers bound by regex.
   - It contains `decodeFilterValue` (`query.go:230-258`), which emulates Node's base64 decoding.
5. `collapse_series.go` plus `items_list.go` (about 270): Sequelize ordering emulation, and a bool-heavy handler of about 110 lines that builds its payload as a map.
6. `read.go:86-227` (140): `StatsValue` as `map[string]any`, with 4 hand-written scan loops and the table whitelist repeated 3 times.
7. The loader plumbing is the same across the three entity packages:
   - `books/load.go:12-80`, `podcasts/read/load.go:14-95` and `items/load.go:60-118` (about 200 lines of `sql.Null*` and `NullStringPtr`).
   - `json.go` files (699 lines) hold three projections per entity. `Media` is typed `any`, and type-switches at `items/json.go:150-205` recover id and access.
   - `PodcastMedia(.., full, expanded bool)` switches behaviour on two bool parameters.
8. `mediafiles.File` and `Chapters` are `map[string]any`. `books.tracks()` copies the maps and `File.Excluded` uses JS truthiness. The typed `AudioFile` struct exists but reads don't use it.
9. `search.go` (282), `series_list.go` (214) and `authors_list.go` (101):
   - Each reads rows, sorts in memory, then queries once per row (series, authors, `collapsedSeriesJSON`, `AttachItemIncludes`).
   - The "books of a series with the permission clause" SQL is written about 6 times.
   - The `mediaProgresses` semantics appear in 4 places (`query.go`, `series_filter.go`, `personalized*.go`). `series_filter.go` has its own variants.
10. `filterdata.go:200-346`: patch API (`AddAuthor`, `AddBook` and others) that reproduces upstream's stale, unsorted menus.
11. `feeds/feeds.go` (178) and `playlistview` (130): sqlc rows mapped by hand into two feed shapes, plus `map[string]any` playlist views.

**6. CLIENT DEPENDENCE** (grepped components/pages/store)
- Used by the web client:
  - `collapsedSeries` fields `id`, `name`, `nameIgnorePrefix`, `numBooks`, `libraryItemIds` and `seriesSequenceList`.
  - `numEpisodesIncomplete`, `recentEpisode`, `mediaItemShare`, `labelStringKey`, `numIssues` and `numUserPlaylists`.
  - `descriptionPlain`, `ebookFormat`, `authorsWithCount`, `longestItems`, and `titleIgnorePrefix` in `LazyBookCard.vue`.
- Not used by the web client: `loadedAt`, `lastScanVersion`, `oldLibraryItemId`, `numFiles`, `libraryItemIdsFinished`, JSON key order, ETags.
- Mobile clients: unknown.
- The sorts that evaluate to `''` in collapse_subseries are not plausibly relied on. The stale, unsorted filterdata is not either.

**7. GREENFIELD PROPOSAL** (about 7450 now, about 3000 after)
- `internal/catalogdb` (about 600): a `DateTime` column type (millis, NULL to `nil`), `JSONCol[T]`, `StringArray` and `NullStr`. Typed `LibraryItem`, `Book`, `Podcast`, `Episode` and `Series` structs, and a batched `Load` of about 150 lines.
- `internal/media` (about 500): `Book` and `Podcast` with `View()`, `Minified()` and `Expanded()` returning real structs. Use a `Media` interface or generic `Item[M]` instead of `any`. `AudioFile` is typed, with unknown keys kept in `Extra map[string]json.RawMessage` for round-trip. A `Query` builder owns the permission clause.
- `internal/library` (about 1500): `Settings` struct plus PATCH decoding onto it. One filter/sort table. A shelf list of `func(ctx, Env) ([]Entity, error)`. A series module. A stats query as a typed `Stats` struct.
- `filterdata`: recompute on invalidation, about 120 lines.
- `textsort`: keep as is.

**8. HAZARDS**
- Persisted formats:
  - DATETIME is `"2006-01-02 15:04:05.000 -07:00"`. Parsing must accept the four other layouts. A zero `lastScan`, `publishedAt` or `lastEpisodeCheck` becomes `null`.
  - The `ebookFile` column can be SQL NULL or the string `'null'`.
  - The `LIKE '%"isSupplementary":true%'` filter needs compact `libraryFiles` JSON.
  - `libraryItems.title`, `titleIgnorePrefix` and `authorNames*` are denormalised columns that sorts read.
  - `series.nameIgnorePrefix` is not recomputed on PATCH.
  - `extraData.oldLibraryItemId`, `oldEpisodeId` and `guid`.
  - The library `lastScanVersion` is omitted on create only.
- Sort semantics:
  - `COLLATE NOCASE` is ASCII-only.
  - `CAST(sequence AS FLOAT) ... NULLS LAST` and the `NULLS FIRST/LAST` flips.
  - Series sequence order uses the `textsort` natural order with empty sequences last.
  - Node ICU parity depends on `collation.json`.
  - SQLite row order for ties (`ORDER BY rowid`).
- Events and recipients:
  - `library_added`, `library_updated`, `series_updated` and `items_updated` go to everyone.
  - `playlist_*` go to the owner only.
  - `LibraryChanged` fires only after commit and only on folder or watcher changes.
- Permissions: `MediaClause` on every book and podcast query; `adminUser` also requires `IsActive`; `podcast-titles` is admin-only; series routes return 404 when no accessible book exists, and PATCH needs the update permission.
- Query count: `items.Load` is constant at 5-6 queries (asserted by `TestItemsLoadQueryCountIsConstant`). Library list endpoints are N+1 today and carry no guarantee.
- Response details:
  - `minified podcast numEpisodes` is 0, and list rows take the item size.
  - A search podcast has its episodes stripped.
  - `rssFeed` and `mediaItemShare` appear only when requested, and the share only for admins.
  - `include=filterdata` returns `{filterdata, issues, numUserPlaylists, library}`.
  - Narrator ids are `QueryEscape(base64(name))`.
- Goldens: the catalog goldens are byte-recorded, so any change to key order needs a semantic compare.
