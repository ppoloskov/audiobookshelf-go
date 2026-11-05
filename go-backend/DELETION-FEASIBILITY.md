# Deletion feasibility and first-wave proposal

Architect-reviewed source audit, 2026-11-05, against `7c08c25b`. Candidate
files below are unchanged by the subsequent lists completion. Counts are
physical production Go lines (including declarations/comments), not test or
generated lines. Net estimates are unimplemented. ROADMAP D01/D02 owns status;
refresh affected counts and consumer evidence when these targets change.

The review establishes **4,607 nonoverlapping conditional candidate lines**,
not 4,607 safe deletions. These are active features. No proven-dead core code
or behavior-preserving 1,000-line consolidation wave was found. Product
narrowing is the only currently evidenced route to a wave of that size.

## Candidate inventory

Production paths are relative to `go-backend/`; `client/` paths are relative
to the repository root.

| ID | Exact deletion surface | Gross lines | Classification / evidence |
|---|---|---:|---|
| F1 | `internal/backup/backup.go:1–803`, all declarations/functions | 803 | Optional only by product decision. Active web backup settings/table calls: `client/pages/config/backups.vue:157–206`, `client/components/tables/BackupsTable.vue:89–183`. Native use unknown. |
| F2 | `internal/audiotools/tools.go:1–732`, all declarations/functions | 732 | Optional manual M4B/metadata-embedding tools; active `client/pages/audiobook/_id/manage.vue:340–390`, `Tools.vue:94–114`, batch embed `Appbar.vue:203`, task polling `layouts/default.vue:581`. Native use unknown. |
| F3 | `internal/providers/author_match.go:1–92`, `book_finder.go:1–445`, `builtin_search.go:1–802`, `cover_search.go:1–150`, `custom_search.go:1–215`, `handler.go:1–356`, `podcast_finder.go:1–21` | 2,081 | Conditional whole provider/search/custom-administration removal. Active match/search/custom-provider UI; HTTP and realtime consumers, detailed below. Not authorized. |
| F4 | `internal/catalog/quickmatch.go:1–916` | 916 | Conditional item/batch/library matching removal, not duplicate hydration. Direct search can remain if only this feature is removed. |
| F5 | `internal/catalog/authors.go:991–1065`, `authorMatch` | 75 | Conditional author matching removal. Active AuthorCard, author EditModal and BookShelfToolbar callers. Shared author/ACL helpers stay. |
| | **Unique conditional gross, F1–F5** | **4,607** | No overlap. No guaranteed net savings credited. |

Provider/matching web consumers include `client/components/modals/item/tabs/Match.vue:426`,
`client/pages/library/_library/podcast/search.vue:167`, and
`client/pages/config/item-metadata-utils/custom-metadata-providers.vue:55`.
`contracts/client-usage.json` records positive route callers; it does not prove
mobile absence. `contracts/mobile-client-audit.md` remains partial. Official
iOS/Android and third-party use of F1–F5 is **unknown**, not zero.

Excluded from that total: 224 source-region lines of backup-only manager
support, 138 ebook-operation lines classified keep/unknown, tests, generated
queries and protocol snapshots. Ebook readers/status UI are active; retain
path authorization and persisted ebook status. Manager restore helpers are
listed below as dependency cleanup, not precredited savings. Shared manual
metadata/ACL/image helpers outside F4/F5 are retained.

## Proposed first wave: F1 + F2, awaiting owner decision

The concrete gross target is **1,535 lines**, not a package relocation.
A proposed **250-line planning maximum** for replacement code plus retained lines from
those two target files would yield a **1,285-line net target**. This is a
planning budget, not a measured replacement estimate or guaranteed saving.
The disabled-route/task/settings design and build must prove that cap
before implementation is dispatched. To clear 1,000 net, total replacement
and retained target code must stay at or below 535 lines. Existing manager
support is outside the gross figure and is not subtracted again as a cost.
Keep authenticated `GET /api/tasks` returning `{"tasks":[]}` and, for
`include=queue`, `{"tasks":[],"queuedTaskData":{"embedMetadata":[]}}`.
The other four tool operations and seven backup operations become explicitly
unsupported behind authentication/admin checks if their removal is approved.

Owner decisions are separate: remove server-managed backup creation,
scheduling, upload/apply/restore, listing and deletion; remove manual M4B
conversion, metadata embedding, their queue/cancel/task reporting. Existing
web controls would report unsupported operations; editing the read-only
bundled client is not assumed. Native operation use remains unknown. This
changes the compatibility scope for those features and is **not authorized**.

Keep normal audiobook/podcast playback, required FFmpeg/HLS, scanner ingest,
metadata search, authentication and authorization, SQLite/persisted meaning,
existing backup/media files, and TS rollback validation. No boot cleanup,
archive deletion or database migration is proposed. Any retained file/archive
entrypoint must keep its path/symlink/SQLite validation; any retained provider
entrypoint must keep outbound URL/SSRF checks.

Small execution gates are in ROADMAP D02a–D02e. Provider removal is not selected
for the first wave because its UI, scanner matching, realtime and podcast
search impacts are wider. Under complete compatibility, only the small core
consolidation hypotheses below are currently supported; no 30% saving is proved.

## F1/F2 consumers and cleanup boundary

Backup routes are mounted in Runtime with settings reload/notification/close
hooks; audio tools have construction/mount/close hooks. Runtime references are
at the audit baseline `7c08c25b`; lists completion shifts those line numbers
without changing these consumers. Positive web evidence: `BackupsTable.vue`,
backup settings, item Manage/Tools, batch embed in Appbar, default-layout task
polling and NotificationWidget labels. The exact AST declaration manifest below
replaces approximate function-owned source regions.

Backup dependency cleanup, not included in the 1,535 gross: `manager.go`
`updateBackupPath` 76–98 (23), `restore` 99–185 (87), `resumeOld` 186–207 (22),
`rollbackRestore` 208–227 (20), `removeSQLiteSidecars` 228–232 (5),
`copyDirectoryStage` 233–295 (63), plus `restoredDir` 32–35 (4): 224 source-region
lines. Runtime backup callbacks and manager restore tests are the consumers;
verify zero retained consumers before deleting these helpers. Keep ordinary
Manager request/close ownership and the independent TS rollback harness.

## Retained-behavior verification and storage snapshots


- Backup tests: `TestCreateBackupUsesVacuumSnapshotAndPinnedArchiveLayout`, `TestApplyStagesSafeMetadataAndRejectsTraversalAndSymlinks`, `TestManagerRestoreSwapsRuntimeDatabaseAndMetadata`, `TestManagerRestoreFailureRollsBackDatabaseAndMetadata`, `TestUpstreamCreatedBackupImports`, `TestJourneyAdmin`. While backup ingestion is retained, preserve SQLite validation, metadata staging and archive traversal/symlink rejection. If the entire entrypoint is removed, delete its guards only after proving zero retained consumers; preserve independent TS rollback.
- Audio tests: `TestFFMetadataEscapingAndChapterOrder`, `TestMergeCreatesM4BAndKeepsSourceBackup`, `TestMergeFFmpegFailureDoesNotInstallOutput`, `TestCancelEncodeKillsConfiguredFFmpeg`. Add queue/lifecycle coverage before changing that behavior; preserve source backup.
- Provider tests: `TestCustomProviderCRUDPermissionsAndFallback`, `TestProviderCatalogAndCustomSearchHTTPTransform`, `TestCustomSearchMalformedAndRemoteFailureReturnEmpty`, `TestBuiltinProvidersUseInjectableHTTPAndSourceTransforms`, `TestSearchBooksFuzzyFallbackAndCustomProvider`, `TestSearchPodcastRoute`, `TestAuthorMatch`, `TestCoverSearchSocket`, `TestCoverSearchCancel`, and quick-match validation/application/batch tests. Any retained custom-provider path must keep auth and outbound-URL protections.
- Ebook tests include `TestTrackAndEbookStatusUpdates` and `TestJourneyItems`; preserve path authorization and stored status.
- `contracts/mobile-client-audit.md` is explicitly partial; its pinned official-client evidence does not cover these operation families. Lack of a selected call is **unknown**, not evidence of absence. No full mobile audit is claimed here.
- Generated protocol fixtures are separate from Go production: `contracts/shapes/*.json` = 7 files, 108,510 physical JSON lines / 3,414,049 bytes. Excluded from all candidate totals; retain pending V2 extraction/regeneration proof.


## Provider/matching boundaries: retain shared consumers

The F3–F5 total is a conditional whole-feature ceiling, not a safe pruning
claim. Removing only quickmatch must leave direct search and cover/realtime
routes. Removing all provider behavior would also lose automatic matching;
it needs its own decisions and replacement accounting.


| Source | References / classification |
|---|---|
| `catalog/quickmatch.go:29–33, 223–401, 412–745, 749–915` | `BookFinder` is consumed by three matching routes and scanner-style matching. `quickMatchAuthors`, `quickMatchSeries`, `storedValue`, `dbTimeMillis`, `orEmpty`, `storedText`, `storedNumber`, and `match*` helpers have references only inside this feature file. They are removable only with the quick-match workflows. |
| `catalog/writes.go:180` `rowColumns` | Used by retained `applyMedia` at `writes.go:230` and by quickmatch at `quickmatch.go:111`; keep. |
| `catalog/writes.go:808` `storedString` | Used by retained manual metadata at `writes.go:832,846` and quickmatch at `quickmatch.go:166,439,524`; keep. |
| `catalog/writes.go:971` `jsonValue` | Used by retained manual metadata/file writes at `writes.go:869,874,898,901,1223,1230,1262` and quickmatch at `quickmatch.go:429,432`; keep. |
| `catalog/authors.go:106` `jsTruthy` | Used by retained ACL/image-update paths (`authors.go:142,886`) and `catalog/batch.go:211`; quickmatch’s `jsTruthyRaw` delegates to it. Keep. |
| `catalog/authors.go:394` `jsonBodyObject` | Used by retained author update and image-upload handlers (`authors.go:497,874`) plus authorMatch (`:997`); keep. `firstNonEmpty` (`:1067–1074`) and `asinPattern` (`:989`) are authorMatch-only. |
| `providers/book_finder.go:26` `SearchBooks`, `podcast_finder.go:12` `SearchPodcasts` | Only called as catalog quickmatch collaborators. `/api/search/books` and `/api/search/podcast` instead use `searchBuiltinBooks`/`itunesPodcasts` in `builtin_search.go`; those routes remain if only quick-match is removed. |
| `providers/author_match.go:12–92` | `FindAuthorByASIN/Name` satisfies catalog’s `AuthorFinder` for `authors.authorMatch`; `/api/search/authors` uses a separate `findAuthorByName` path in `builtin_search.go`. |
| `providers/cover_search.go:35–150` | HTTP covers route plus websocket cover-search caller in `server/runtime.go:184–186`; cannot delete with only catalog quick-match. |
| `providers/handler.go:61–80, 106–255` | Search provider list and custom-provider CRUD; custom-provider route callers remain active. Full package removal requires explicit removal of these persisted features and runtime registration too. |


# Core implementation audit

Read-only audit. Reference baseline: published `7c08c25b`; inspected current catalog/scanner/playback/books/items sources and existing tests. In-flight list files were not evaluated as deletion opportunities. No source, docs, metrics, tests, or generated repository artifacts were changed.

## Finding

A safe 1,000-line core deletion wave is **not credible from confirmed duplication/dead code in this scope**. The selected packages contain about 19,507 current production lines (catalog 11,999; scanner 4,686; playback 2,120; books 465; items 237); those footprints are not savings. Current web inventory records 201 routes, one unmatched and nine unresolved call sites. The pinned mobile audit is explicitly partial and does not prove route absence. Any feature loss requires an operation-specific consumer review and owner REMOVE decision; no global client-audit freeze is imposed on behavior-preserving consolidation.

| Area / exact source | Evidence and classification | Deletion feasibility |
|---|---|---|
| Catalog item hydration: `catalog/handler.go:38–53, 2191–2219, 2536–2549` | `items.RowColumns`/`ScanRecord` are reused by listing; `loadItem` calls `items.LoadMany`; book media reads use `books.Load/LoadMany`. `recordToMap` is the remaining legacy wire-map adapter, not a second row decoder. **keep**; not a deletion. | No unique safe gross deletion. Future mixed-media projection work must name callers and preserve `TestItemGolden`, `TestBookProjectionValues`, `TestBookPageQueryCountIndependentOfPageSize`, item includes and shelf tests. |
| Playback book hydration: `playback/playback.go:413–503` (`loadBook`, 91 physical lines) | Independently queries `books`, ordered author/series joins, decodes tags/audio/chapters and assembles metadata while `books.Load/LoadMany` (`books/batch.go:18–147`) + `Book.Full/Minified/Expanded` (`books/projection.go:10–94`) already own those persisted fields/projections. This is a real second implementation; **merge-duplicate**, medium evidence. Semantics differ: playback strictly errors on malformed audio JSON, ignores some other unmarshal errors, filters excluded files and maps to private `audioFile`; `books` uses shared lenient decoders and wire types. | Candidate only, not yet removable as-is. Gross 91; replacement estimate 30–45 lines for `books.Load`/projection-to-playback conversion; estimated net 46–61. Exact output/failure behavior must be frozen before migration. |
| Podcast episode hydration: `catalog/handler.go:152–226`; `playback/playback.go:505–595`; `podcasts/item.go:38–60, 127–180`; `catalog/personalized.go:194–292` | Multiple SQL row scans and episode maps exist. The shapes genuinely differ: catalog item DTO vs personalized shelf vs playback session `audioTrack`; playback skips malformed episode audio and uses session audio types; `podcasts` item projection includes feed/download/list fields. **likely merge-duplicate row decoding, unknown projection savings**—not proof these bodies are interchangeable. | Second consolidation option: consider a narrowly scoped shared persisted-episode row loader in existing `podcasts` ownership, keeping each response projection. Rough 50–90 lines of duplicated scan plumbing, likely 35–70 replacement lines; net −20–55, low confidence; it could grow code. Do not count toward a deletion target before source/value/order/null/error tests show the row contract is identical. |
| Scanner pipeline: `scanner/scanner.go:794–1099` full scan, `scanner/watcher.go:502–720` incremental changes, `scanner/files.go:137–598` `importGroup` | Both full and watcher paths converge on `importGroup`; watcher also calls it from `rescanStoredItem`. File grouping, inode identity matching, metadata precedence, unchanged-file preservation, and event chunks are distinct pipeline obligations, not duplicate whole importers. `probeAudio`, `buildChapters`, `mergeBookMetadataWithABS` feed the same path. **keep**; no duplicated end-to-end body found. | No candidate deletion. Existing preservation tests include `TestScanRenameCompanionChangeAndUnchangedBookkeeping`, `TestAudioEntryIdentityPreservedOnCompanionUpdateAndRemoval`, precedence/fallback tests, watcher discovery/close and event chunk tests. Full-filesystem/device parity remains unverified. |
| Catalog writes and media routes: `catalog/writes.go:1–1282`, `library_writes.go:1–858`, plus `authors.go`, `series_routes.go`, `batch_chapters.go` | Large volumes implement active metadata, cover, file, library, author/series, batch, download and permission operations. Web usage confirms callers including `LazyBookCard.vue`, `Details.vue`, `AudioTracksTableRow.vue`, library edit/tools and authors modals. Mobile audit does not close Android/iOS operation census. **keep/unknown** absent a specific client proof and owner decision. | Route removal gross/net credited: 0. Do not count size or web-only absence as savings. |
| Playback state/media: `playback/playback.go:145–361, 617–1425, 1426–1726` | Direct playback, range serving, sessions/progress/local sync, history, HLS/transcoding and session serialization. Pinned mobile evidence confirms iOS/Android progress PATCH and official local/local-all sync; Lissen source also specifies local-all DTO. **keep**. | No safe route/body removal identified. |

## Candidate totals and proof boundary

- Confirmed dead/removable production code in audited core: **0 lines**.
- Unique consolidation gross estimates (not approved savings): **141–181 lines** (91-line playback book loader + 50–90 lines of podcast scan plumbing; no overlap counted).
- Replacement estimate: **65–115 lines**; theoretical net range **26–116 lines**, with the podcast portion low confidence. The playback candidate alone has the clearer bounded case (46–61 net estimate).
- These are source-review estimates, not ratchet results. Neither candidate is a 1,000-line first wave; **3,000 candidate production lines are not supported** by this evidence. Reaching that number would require client-confirmed feature/route removals or materially broader proven duplication, neither currently established.

## Client evidence and required checks

- Web inventory: `contracts/client-usage.json` has active callers for `GET /api/items/:id` (`MediaPlayerContainer.vue:513`), `POST /api/items/:id/play` (`PlayerHandler.js:196`), item batch reads/updates, library scans, and author/series/list routes. These are positive usage signals, not a complete call census.
- Pinned mobile evidence: [`contracts/mobile-client-audit.md`](contracts/mobile-client-audit.md) identifies official iOS/Android progress PATCH and `/session/local` + `/session/local-all` callers (pinned SHA `609f9d644c8390fc662869e08ace18ce4a519b00`), plus Lissen `/session/local-all` DTO evidence. Audit status remains partial; media playback/ranges and several mobile callers remain unknown. Lack of a selected caller is not absence proof.
- Playback consolidation gate: `TestPlaybackSessionDeviceAndEmptyMediaArraysMatchOldJSON`, `TestDirectPlaybackRangeOwnershipAndProgressPersistence`, `TestTranscodedSessionServesHLSWithoutBearerAndRejectsBadFiles`, `TestPodcastEpisodeTrackFallbackAndFileRoute`, `TestOfflineLocalSyncAndAdminHistoryRoutes`, plus book projection/null tests. Add cases only for changed failure/null/order semantics.
- Podcast row-loader gate: catalog item/personalized episode field sets, order, empty/null JSON, malformed stored audio, and direct playback track URLs/offsets need independent expected-value assertions. Existing relevant tests include `TestPodcastItemDetailSerializesEpisodeAndTrack`, `TestPodcastEpisodeTrackFallbackAndFileRoute`, and personalized shelf tests; full shared-row coverage is not established.
- Scanner retention gate: existing import, precedence, changed/unchanged file, watcher, cancellation, author cleanup, and event-order tests above. No deletion or metadata-format rewrite authorized.

## Decisions required before any deletion

- Scoped web/iOS/Android/third-party consumer evidence for each proposed removal, with remaining uncertainty presented to the owner; the partial mobile audit does not prove absence.
- Owner KEEP/REMOVE decision per operation before deleting routes/features; no route removal is authorized by this audit.
- For playback book reuse, decide whether malformed persisted JSON keeps current playback-specific failure behavior or adopts books' decoder semantics; preserve database and event contracts.
- For podcast consolidation, approve shared row ownership only after proving identical selected columns, order, null handling, malformed JSON handling and SQL row closure. No new package, dependency, schema, or migration is recommended.

## Exact top-level Go function manifest

Ranges start at the `func` keyword and end at the AST declaration end, inclusive. Function line counts include every physical line in that range. Whole-file gross counts include imports, types, comments, blanks and declarations; each is listed separately from function ranges.

### `go-backend/internal/backup/backup.go` — 803 whole-file lines

| Function | Start–end | Function lines |
|---|---:|---:|
| `New` | 75–103 | 29 |
| `(*Handler).Close` | 105–114 | 10 |
| `(*Handler).SetNotificationHandler` | 118–122 | 5 |
| `(*Handler).notifyEvent` | 124–131 | 8 |
| `(*Handler).UpdateOptions` | 135–169 | 35 |
| `(*Handler).Reload` | 171–201 | 31 |
| `(*Handler).Routes` | 206–239 | 34 |
| `(*Handler).listRoute` | 241–244 | 4 |
| `(*Handler).createRoute` | 246–253 | 8 |
| `(*Handler).Create` | 255–268 | 14 |
| `(*Handler).create` | 270–346 | 77 |
| `(*Handler).upload` | 348–419 | 72 |
| `(*Handler).updatePath` | 421–463 | 43 |
| `(*Handler).delete` | 465–477 | 13 |
| `(*Handler).apply` | 479–587 | 109 |
| `(*Handler).list` | 589–596 | 8 |
| `(*Handler).options` | 597–597 | 1 |
| `(*Handler).find` | 598–605 | 8 |
| `readBackup` | 607–658 | 52 |
| `details` | 660–666 | 7 |
| `writeArchive` | 668–700 | 33 |
| `addFile` | 701–713 | 13 |
| `addDir` | 714–739 | 26 |
| `treeBytes` | 741–767 | 27 |
| `extractEntry` | 769–801 | 33 |
| `(*Handler).emit` | 803–803 | 1 |

### `go-backend/internal/audiotools/tools.go` — 732 whole-file lines

| Function | Start–end | Function lines |
|---|---:|---:|
| `New` | 84–89 | 6 |
| `(*Handler).Routes` | 93–107 | 15 |
| `(*Handler).itemRoute` | 111–134 | 24 |
| `(*Handler).tasksRoute` | 136–144 | 9 |
| `requestUser` | 146–149 | 4 |
| `isAdmin` | 151–151 | 1 |
| `(*Handler).Tasks` | 153–175 | 23 |
| `(*Handler).Close` | 177–186 | 10 |
| `snapshotTask` | 188–195 | 8 |
| `cloneTime` | 197–203 | 7 |
| `cloneData` | 205–215 | 11 |
| `chapterDTOs` | 217–223 | 7 |
| `(*Handler).loadItem` | 225–275 | 51 |
| `(*Handler).encode` | 277–300 | 24 |
| `(*Handler).embed` | 301–325 | 25 |
| `(*Handler).batch` | 327–379 | 53 |
| `(*Handler).newTask` | 381–396 | 16 |
| `(*Handler).has` | 397–411 | 15 |
| `(*Handler).cancelEncode` | 412–429 | 18 |
| `(*Handler).finish` | 430–451 | 22 |
| `(*Handler).runNextEmbed` | 453–474 | 22 |
| `(*Handler).runEmbed` | 476–536 | 61 |
| `(*Handler).runEncode` | 537–543 | 7 |
| `(*Handler).merge` | 544–619 | 76 |
| `ffmetadata` | 620–640 | 21 |
| `escape` | 641–643 | 3 |
| `muxer` | 644–657 | 14 |
| `qvalue` | 658–663 | 6 |
| `copyFile` | 664–687 | 24 |
| `ffmpegPath` | 688–693 | 6 |
| `canAccess` | 695–711 | 17 |
| `randomID` | 712–712 | 1 |
| `str` | 713–713 | 1 |
| `num` | 714–725 | 12 |
| `status` | 726–726 | 1 |
| `text` | 727–731 | 5 |
| `sendOK` | 732–732 | 1 |

### `go-backend/internal/providers/author_match.go` — 92 whole-file lines

| Function | Start–end | Function lines |
|---|---:|---:|
| `(*Handler).authorRequest` | 24–41 | 18 |
| `authorMatch` | 43–48 | 6 |
| `(*Handler).FindAuthorByASIN` | 51–56 | 6 |
| `(*Handler).FindAuthorByName` | 62–92 | 31 |

### `go-backend/internal/providers/book_finder.go` — 445 whole-file lines

| Function | Start–end | Function lines |
|---|---:|---:|
| `(*Handler).SearchBooks` | 26–81 | 56 |
| `truncateRunes` | 83–88 | 6 |
| `convertJSON` | 91–104 | 14 |
| `(*Handler).runBookSearch` | 108–144 | 37 |
| `splitTitleParts` | 156–156 | 1 |
| `stripRedundantSpaces` | 158–160 | 3 |
| `replaceAccentedChars` | 162–164 | 3 |
| `stripSubtitle` | 166–174 | 9 |
| `cleanTitleForCompares` | 177–185 | 9 |
| `isWordChar` | 187–189 | 3 |
| `cleanAuthorForCompares` | 192–200 | 9 |
| `removeMiddleInitials` | 204–248 | 45 |
| `levenshteinDistance` | 250–250 | 1 |
| `filterSearchResults` | 254–298 | 45 |
| `titleCandidates` | 318–363 | 46 |
| `removeAuthorFromTitle` | 367–378 | 12 |
| `(*Handler).authorCandidates` | 383–413 | 31 |
| `(*Handler).validateAuthor` | 416–445 | 30 |

### `go-backend/internal/providers/builtin_search.go` — 802 whole-file lines

| Function | Start–end | Function lines |
|---|---:|---:|
| `(*Handler).searchBooks` | 23–58 | 36 |
| `(*Handler).searchBuiltinBooks` | 60–78 | 19 |
| `(*Handler).searchGoogleBooks` | 80–140 | 61 |
| `(*Handler).searchITunesBooks` | 142–168 | 27 |
| `(*Handler).searchOpenLibrary` | 170–225 | 56 |
| `(*Handler).searchAudiobookCovers` | 227–254 | 28 |
| `queryParamAsString` | 259–274 | 16 |
| `(*Handler).searchPodcasts` | 278–301 | 24 |
| `(*Handler).itunesPodcasts` | 305–322 | 18 |
| `(*Handler).searchCovers` | 324–350 | 27 |
| `(*Handler).searchAuthor` | 352–369 | 18 |
| `(*Handler).searchChapters` | 370–386 | 17 |
| `(*Handler).getJSON` | 388–415 | 28 |
| `(*Handler).findAuthorByName` | 417–452 | 36 |
| `(*Handler).searchFantLab` | 454–542 | 89 |
| `(*Handler).searchAudible` | 544–604 | 61 |
| `cleanAudibleBook` | 606–668 | 63 |
| `cleanSequence` | 670–679 | 10 |
| `intValue` | 680–691 | 12 |
| `joinOrNil` | 692–697 | 6 |
| `nilIfEmpty` | 698–703 | 6 |
| `largestImage` | 705–716 | 12 |
| `itunesArtwork` | 717–751 | 35 |
| `stringField` | 752–752 | 1 |
| `nullAny` | 753–758 | 6 |
| `valueArray` | 759–764 | 6 |
| `firstYear` | 765–775 | 11 |
| `stringOrNull` | 776–787 | 12 |
| `pathTail` | 788–791 | 4 |
| `validASIN` | 792–802 | 11 |

### `go-backend/internal/providers/cover_search.go` — 150 whole-file lines

| Function | Start–end | Function lines |
|---|---:|---:|
| `(*Handler).StartCoverSearch` | 39–62 | 24 |
| `(*Handler).CancelCoverSearch` | 66–77 | 12 |
| `(*Handler).searchCoversFrom` | 81–95 | 15 |
| `coverProviders` | 100–110 | 11 |
| `(*Handler).providerCovers` | 113–150 | 38 |

### `go-backend/internal/providers/custom_search.go` — 215 whole-file lines

| Function | Start–end | Function lines |
|---|---:|---:|
| `(*Handler).searchCustom` | 19–87 | 69 |
| `normalizeCustomMatch` | 89–121 | 33 |
| `stringValue` | 123–148 | 26 |
| `numberValue` | 149–166 | 18 |
| `normalizeTags` | 167–190 | 24 |
| `normalizeSeries` | 191–213 | 23 |

### `go-backend/internal/providers/handler.go` — 356 whole-file lines

| Function | Start–end | Function lines |
|---|---:|---:|
| `New` | 37–42 | 6 |
| `(*Handler).SetEndpoint` | 46–50 | 5 |
| `(*Handler).endpoint` | 51–55 | 5 |
| `(*Handler).Routes` | 61–80 | 20 |
| `(*Handler).deleteCustomProviderRoute` | 84–91 | 8 |
| `isAdmin` | 93–93 | 1 |
| `(*Handler).getCustomProviders` | 106–140 | 35 |
| `(*Handler).createCustomProvider` | 142–203 | 62 |
| `(*Handler).deleteCustomProvider` | 205–253 | 49 |
| `(*Handler).getProviders` | 258–290 | 33 |
| `providerObject` | 292–298 | 7 |
| `(*Handler).customProviders` | 300–321 | 22 |
| `isoDate` | 323–328 | 6 |
| `dbNow` | 329–329 | 1 |
| `queryValue` | 331–336 | 6 |
| `uniqueStrings` | 337–348 | 12 |
| `sortedKeys` | 349–356 | 8 |

### `go-backend/internal/providers/podcast_finder.go` — 21 whole-file lines

| Function | Start–end | Function lines |
|---|---:|---:|
| `(*Handler).SearchPodcasts` | 12–21 | 10 |

### `go-backend/internal/catalog/quickmatch.go` — 916 whole-file lines

| Function | Start–end | Function lines |
|---|---:|---:|
| `optionalBool` | 42–42 | 1 |
| `jsTruthyRaw` | 45–51 | 7 |
| `matchString` | 54–63 | 10 |
| `matchList` | 66–78 | 13 |
| `(*Handler).loadQuickMatchItem` | 95–144 | 50 |
| `(quickMatchItem).authorName` | 146–152 | 7 |
| `(quickMatchItem).seriesName` | 154–163 | 10 |
| `storedText` | 165–170 | 6 |
| `storedNumber` | 172–180 | 9 |
| `(*Handler).serverSettings` | 183–190 | 8 |
| `settingTrue` | 192–194 | 3 |
| `(*Handler).saveCoverFromURL` | 199–220 | 22 |
| `(*Handler).quickMatchLibraryItem` | 223–405 | 183 |
| `(*quickMatchColumns).put` | 419–421 | 3 |
| `(*quickMatchColumns).changed` | 423–446 | 24 |
| `(*quickMatchColumns).changedNames` | 448–454 | 7 |
| `(*quickMatchColumns).bookDetails` | 459–490 | 32 |
| `(*quickMatchColumns).podcastDetails` | 493–513 | 21 |
| `orEmpty` | 515–520 | 6 |
| `storedValue` | 523–532 | 10 |
| `(*Handler).quickMatchAuthors` | 537–602 | 66 |
| `matchSeries` | 611–624 | 14 |
| `(*Handler).quickMatchSeries` | 628–702 | 75 |
| `dbTimeMillis` | 704–710 | 7 |
| `matchOptionsFromBody` | 714–745 | 32 |
| `(*Handler).itemMatch` | 749–786 | 38 |
| `(*Handler).batchQuickMatch` | 791–843 | 53 |
| `(*Handler).libraryMatchAll` | 848–861 | 14 |
| `(*Handler).matchLibraryItems` | 863–916 | 54 |

### `go-backend/internal/catalog/authors.go` — 1166 whole-file lines

| Function | Start–end | Function lines |
|---|---:|---:|
| `(*Handler).authorMatch` | 991–1065 | 75 |

**Target totals:** 174 listed functions / 3,954 inclusive function lines.
The deletion surfaces total 4,607 lines, including their non-function source.
The 5,698 full-file sum would include 1,091 unselected lines from authors.go;
those lines are excluded from all candidate/savings totals.
