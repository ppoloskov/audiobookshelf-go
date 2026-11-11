Scope sizes: non-test lines are catalog 3574, itemwrite 654, people 1101, sqlupdate 70, nameparse 324, wire 141, jsonvalue 109 (about 5970). Test lines are about 6150, of which catalog is 4895.

## 1. RESPONSIBILITIES
- **catalog**: library-item HTTP surface (read, media PATCH, batch, quickmatch, delete, cover, files, download), plus library delete, library issues cleanup and library zip download.
  - Item: `GET/DELETE /api/items/{id}`; `GET /api/items/{id}/download`; `GET /api/items/{id}/file/{ino}`; `GET /api/items/{id}/file/{ino}/download`; `DELETE /api/items/{id}/file/{ino}`.
  - Item media: `GET /api/items/{id}/ffprobe/{ino}`; `POST /api/items/{id}/chapters`; `PATCH /api/items/{id}/tracks`; `PATCH /api/items/{id}/ebook/{ino}/status`; `GET /api/items/{id}/metadata-object`; `PATCH /api/items/{id}/media`.
  - Ebook and match: `GET /api/items/{id}/ebook[/{fileId}]`; `POST /api/items/{id}/match`.
  - Cover: `GET/POST/PATCH/DELETE /api/items/{id}/cover`.
  - Batch: `POST /api/items/batch/{get,delete,update,quickmatch}`.
  - Library: `DELETE /api/libraries/{id}`; `DELETE /api/libraries/{id}/issues`; `GET /api/libraries/{id}/matchall`; `GET /api/libraries/{id}/download`.
- **itemwrite**: the one write path for book-author and book-series links (`Rules`, `Changes`, `LinkAuthors`, `LinkSeries`), the orphan author/series sweep, and item row deletion (`DeleteRows`, `Deleter`).
- **people**: author entity.
  - Routes: `GET/PATCH/DELETE /api/authors/{id}`; `POST /api/authors/{id}/match`; `GET/POST/DELETE /api/authors/{id}/image`.
  - It also exports `AuthorRecord`, `ScanAuthor` and `AuthorView` to the library package, which uses them for author list, search and shelves.
- **sqlupdate**: ordered changed-column list plus `UPDATE … updatedAt` (Sequelize `changed()`). Used by people (authors) and `library/series_routes.go`.
- **nameparse**: `ToLastFirst`, a port of `parseNameString.js`. 324 lines.
- **wire**: shared DTOs: `BookMatch`/`PodcastMatch` (all `json.RawMessage`), `AuthorMatch`, `SeriesCreated`, `MediaProgress`, `EpisodeDownload`. Also `AddPlaybackSessionSpreadFields(map)`.
- **jsonvalue**: JS coercions (`Truthy`, `Text`, `Number`, `Float`, `String`, `Strings`, `Array`). It has 36 `Truthy` call sites across about 16 packages.

## 2. TABLES
- **libraryItems**: read and written. Triggers copy `title`, `titleIgnorePrefix` and `authorNamesFirstLast/LastFirst` from books and authors, so catalog must not write those columns.
- **books, podcasts**: read and written (generic `SELECT *`, columns picked by name).
- **podcastEpisodes**: read and deleted.
- **authors**: read, inserted, updated, deleted.
- **series**: read, inserted, deleted.
- **bookAuthors, bookSeries**: read, inserted, deleted. `bookSeries.sequence` is updated.
- **libraries, libraryFolders**: read and deleted; `libraries.displayOrder` is renumbered on library delete.
- **mediaProgresses, playlists/playlistItems, mediaItemShares, feeds**: deleted on item delete.
- **collections, collectionBooks**: deleted on library delete.
- **playbackSessions**: `libraryId` set to NULL on library delete.
- **settings** (via serversettings): read only. Other tables are read through `items.Load` and `library.*`.

## 3. COUPLING
- **catalog.Deps**:
  - `Filters` (`*filterdata.Cache`) is shared with the scanner and library.
  - `ItemChanged` (supplied by the catalog `ItemChanged` method itself, which calls `scanner.WriteMetadata`) rewrites `metadata.json`. Only the test rig sets it; production wiring leaves it nil, so check whether the metadata file is actually rewritten.
  - `LibraryChanged` and `PodcastScheduleChanged` exist because catalog cannot import scanner or podcasts.
  - `Books` (`BookFinder`: `SearchBooks`, `SearchPodcasts`) is implemented by providers.
  - `PodcastEpisodes` is a scanner callback.
  - `Downloads` (`DownloadQueue`) is implemented by podcasts.
- **people.Deps**: `Filters`, `Authors` (`AuthorFinder`, implemented by providers), and `WriteMetadata`, which is `scanner.WriteMetadata`. It duplicates `catalog.ItemChanged` minus the event.
- **Package pairing**: the catalog and people handlers each carry `db`, `cfg`, `emit` (`events.Emitter`) and `filters`. The library package imports people, and itemwrite is imported by catalog, library, scanner and server.
- **Duplication caused by the no-import rule**: `minifiedItems`, `expandedItems`, `sortingPrefixes`, `pageOf`, `firstNonEmpty` and `samePointer` are each copied in 2 packages.

## 4. TESTS
- **people**: about 100% black-box (HTTP mux, 80 refs, 10 tests).
- **catalog**: about 90% black-box (`authorRig.do` through a mux). There are 4 white-box exceptions: `h.finishItemWrites`, `h.queryStrings`, `minifiedItems` and fixtures. Roughly 1,300 lines are `handler_test.go`.
- **catalog, misplaced tests**: about 1,000 lines (query_*, personalized_*, shelf_batch, query_count, item_golden) exercise library/items behaviour moved earlier. They are placement debt, not catalog logic.
- **itemwrite**: 100% white-box (`package itemwrite`, calls `LinkAuthors`, `Sweep…` directly). These are DB-level tests of the semantic contract and would be ported, not deleted.
- **nameparse**: golden file generated from Node (survives a rewrite).
- **jsonvalue**: unit tests, white-box.

## 5. COMPLEXITY SOURCES
1. `catalog/mutation.go:115-307` (~190 lines). `storedRow` (`SELECT *` into `[]any`) plus `columnSet` map-based diff, with `storedBool/String/JSONText`. This is a hand-rolled Sequelize `changed()`. `bookFields` and `podcastFields` (210-262) drive it from `map[string]json.RawMessage`.
2. `catalog/request.go` (159 lines). `requestString/Bool/StringArray`, `jsTruthyRaw`, `matchString`, `matchList` and `looselyOne` re-implement Express/JS coercion on raw JSON.
3. `catalog/quickmatch.go:161-326` (~165 lines), `329-436` (~110 lines), `58-106` (~50 lines). `quickMatchLibraryItem` is one function with nearly parallel book and podcast branches; `bookDetails/podcastDetails` compare via `jsonvalue.Truthy` on stored values; `loadQuickMatchItem` is manual Scan plumbing.
4. `catalog/writes.go:80-186` (~105 lines, `applyMedia`) plus `batch.go:124-224` (~100 lines, `batchUpdate`) plus `mutation.go:90-112`. The single-item, batch and quickmatch paths all run the same cycle: `columnSet`, then `Link*`, then `itemWrite`, then `finishItemWrites`. `batchUpdate` hand-parses `[]json.RawMessage` with `Truthy(id)` and `Text(id)`.
5. `itemwrite/relations.go:130-259` (~130 lines). `LinkAuthors` and `LinkSeries` are near-duplicates (remove, add, `Clock`). `authorPayload` (284-303) builds `map[string]any` events with a created/non-created key shape. `sweep.go:48-100` uses dynamic `fmt.Sprintf` SQL with `RETURNING` and manual rowid ordering.
6. `catalog/item_media.go:22-96`, `207-340` (~210 lines). `updateChapters`, `updateTracks` and `updateEbookStatus` use `[]map[string]any` and `mediafiles.File` maps, with `Truthy/Float` for index/exclude handling.
7. `people/authors.go:347-445` (~100 lines, `authorUpdate`) plus `sqlupdate` (70 lines) plus `AuthorColumnUpdate`. Per-key `*string` diffing mimics `changed()`; `authorMerge` is 449-519.
8. `people/authors.go:219-303` (~85 lines). `AuthorSeriesGroups` marshals `json.RawMessage` per entry, with `jsNullable` and `NaturalBaseCompare` to mimic JS sort.
9. Four delete paths: `deleteItem`, `batchDelete` (`batch.go:75-122`), `removeLibraryItemsWithIssues` and `deleteLibrary` (`library_delete.go:19-125`, ~105 lines). Each repeats the sweep, filters reset, feed-closed events and artifact removal. `deleteLibrary` has 14 `Internal("database…")` blocks.
10. `catalog/cover.go:132-170` and `217-340`. `sniffCoverType` is a hand-rolled magic-number table (~40 lines) that mirrors file-type to reproduce error text. `uploadCover` is ~125 lines of hand-built JSON body and ETag.
11. `wire/match.go:20-40` and `jsonvalue/value.go` (109 lines). `BookMatch` fields are all `RawMessage` because providers may return a string or a list.

## 6. CLIENT DEPENDENCE
- **Batch update**: `pages/batch/index.vue:450` sends `[{id, mediaPayload}]` with real string ids and only reads `data.updates`. The weird `id` coercion is unneeded.
- **Author PATCH**: `EditModal` reads `result.updated/merged`. The wrong-type body keys the author PATCH silently drops are not exercised.
- **Media PATCH**: the item Details form sends only changed keys: `metadata.{title,…,authors[{name}],series[{name,sequence}],narrators,genres}` and `tags`. Pointer-field structs fit this.
- **Quick match**: provider results are `BookMatch` with mixed types, so the truthiness and join/split logic is real but confined to the provider boundary.
- **Event bytes, ETag/Express weak-ETag, `Content-Length` quirks, and exact 400/403/500 status for malformed input**: unknown. Mobile clients are untested here.

## 7. GREENFIELD PROPOSAL
- **`itemwrite` becomes `store`-level typed repositories**: `Book`, `Podcast`, `Author`, `Series` structs with `db:` tags and custom column types `JSONList[T]`, `Time` and `NullBool`. A generic `Patch` mechanism replaces `columnSet`: decode the body into `BookPatch{Metadata *struct{Title *string …}; Tags *[]string}`, compare against the loaded struct and run one `UPDATE` generated from tags (about 150 lines instead of about 500).
- **`catalog` is split into small files**: `item.go` (GET), `media.go` (PATCH and batch share `applyBookPatch`), `files.go` (serve, download, delete file, ebook, tracks, chapters), `cover.go` (cover plus `imagekit`: use `net/http.DetectContentType` or `image.DecodeConfig` instead of the sniff table), and `match.go` (`Matcher` interface with `BookResult{Title string, Authors []string …}` typed by the provider adapter, so truthiness code disappears).
- **`people`** holds `author.go` (store plus merge), `handler.go` and `image.go`.
- **`wire`** and **`jsonvalue`**: keep `wire` for shared DTOs. `jsonvalue` shrinks to `Text/Number` on the provider adapter edge only; request bodies use typed structs.
- **Estimate**: catalog+itemwrite+people from about 5,330 lines to about 2,600; nameparse stays about 324; sqlupdate (70 lines), `request.go` (159 lines) and the `wire.BookMatch` raw layer disappear. Tests move to black-box through the mux, plus the Node golden for `nameparse`.

## 8. HAZARDS
- **Persisted formats**: author `lastFirst` via `ToLastFirst` (backfilled on PATCH when empty); series `nameIgnorePrefix` via `TitleIgnorePrefix`; `titleIgnorePrefix` set whenever the title changes (the libraryItems copy follows by trigger); `description` runs `textclean.Sanitize` and `""` stores NULL; JSON columns stored compact; `updatedAt` via `dbtime`.
- **Link timestamps**: `Clock` gives each link row a createdAt 1 ms apart so read-back order equals insertion order. Authors are read `ORDER BY createdAt, rowid`.
- **Name matching**: case-insensitive for PATCH and quickmatch, exact for the scanner.
- **Series sequence**: nil means keep the existing value, non-nil `NullString` means write that value.
- **Cleanup order**: remove old links, then sweep series, then announce, then sweep authors, then metadata+`item_updated` (`finishItemWrites`). Batch sweeps once for the whole batch so a series another item relinks is not swept. Sweep is one `DELETE … NOT EXISTS … RETURNING`, run outside the request context. Authors with asin, description or image are kept. Deleting a series also closes its feeds and emits `rss_feed_closed`. Item deletion: series then authors, then `filters.ResetIssues`.
- **Events**:
  - `item_updated`, `items_updated`, `item_removed`, `library_removed`.
  - `author_added`, `author_updated`, `author_removed`.
  - `series_added`, `series_removed`.
  - `rss_feed_closed`, `batch_quickmatch_complete` (to the requesting user only), and the task started/finished events for `library-match-all`.
  - Author payloads: `author_added` for a newly created author omits asin/description/imagePath/numBooks; the `series_added` payload (`SeriesCreated`) has no `description`.
- **Files**: `metadata.json` is rewritten for every affected item on author rename, merge and delete; cover write/remove purges the cover cache and `cover.*` old extensions; `storeCoverWithItem` picks the item directory; quickmatch cover failure is logged and does not fail the request; item hard delete only inside the library root (`PathAllowed`).
- **Permissions**: item routes check library access then update permission (403); the batch routes check delete/update permission first, then `userCanAccessItems` including explicit and tag rules; quickmatch and matchall are admin-only; `metadata-object` and `ffprobe` are admin-only; author routes return 404 for a library the user cannot see.
- **Responses**: `PATCH /api/items/{id}/media` answers `{updated, libraryItem}`; `batch/update` answers `{success, updates}`; the author PATCH answers `{author, updated}` or `{author, merged}`; the batch quickmatch route answers 200 immediately and finishes in a goroutine.
