> Pre-rewrite survey (2026-11-11). Describes the OLD code; kept for its HAZARDS sections.

**Sizes (production, non-test lines):** scanner 3,708 (11 files), audiotools 576, backup 729, mediatools 136, filekind 67, cronspec 20. Total about 5,240. Tests: scanner 3,749, audiotools 466, backup 777, cronspec 29. Go is 1.27 per go.mod, so `os.CopyFS` and `zip.Writer.AddFS` exist.

## 1. RESPONSIBILITIES
- scanner/scanner.go (922): Scanner lifecycle, `begin`/`beginWait` per-library run registry, scan task events, `scanLibrary`, `Start`/`arm`/`eventLoop` (fsnotify plus cron), `library()`/`folders()` loaders, `finalizeScan`.
- scanner/files.go (976): file/group types, `bookMetadata` and sources (abs, nfo, opf, txt, audio tags), `probeAudio`, track/disc parsing, smart ordering, `buildChapters`, path-derived title/author/series.
- scanner/import.go (431): gather, then pure `plan()`, then apply (raw INSERT/UPDATE for books, podcasts, episodes, libraryItems) and `afterImport`.
- scanner/watcher.go (613): debounced fs-event queue, `scanFilesChanged`, `scanFolderUpdates`, `rescanStoredItem`, `collectItemGroup`.
- scanner/walk.go (221): `walkFiles`, `claimGroups`/`groupFiles`/`updateGroup`.
- scanner/libraryfiles.go (175): `libraryFile` JSON and `mergeLibraryFiles` diff.
- scanner/matcher.go (104): path then inode matching of stored items to scanned groups.
- scanner/metadata.go (147): `WriteMetadata` sidecar writer.
- scanner/eventshape.go (62): emulates the JS instance shape for events.
- scanner/stat_*.go (57): ctime and birthtime (statx on Linux).
- mediatools: ffprobe/ffmpeg runner, `Info` structs, `ProbeRaw`, `ResizeImage`.
- filekind: extension to kind and MIME table.
- audiotools: encode-m4b, embed-metadata, task queue, `/api/tasks`.
- backup: `.audiobookshelf` zip create/list/upload/download/restore staging, cron schedule.
- cronspec: 5-field `robfig/cron` parser wrapper.
- **Routes** (all admin unless noted):
  - Scanner: `POST /api/items/batch/scan`, `POST /api/libraries/{id}/scan`, `POST /api/items/{id}/scan`.
  - Audiotools: `GET /api/tasks` (any user), `POST /api/tools/batch/embed-metadata`, `DELETE|POST /api/tools/item/{id}/encode-m4b`, `POST /api/tools/item/{id}/embed-metadata`.
  - Backup: `GET|POST /api/backups`, `POST /api/backups/upload`, `PATCH /api/backups/path`, `GET /api/backups/{id}/download`, `GET /api/backups/{id}/apply`, `DELETE /api/backups/{id}`.

## 2. TABLES / FILES
- **Scanner reads:** libraries (settings and extraData JSON), libraryFolders, libraryItems, books (audioFiles, coverPath, ebookFile), podcastEpisodes, bookAuthors, serverSettings (via serversettings).
- **Scanner writes:**
  - libraryItems, books, podcasts, podcastEpisodes.
  - libraries (lastScan, lastScanVersion, extraData.lastScanMetadataPrecedence).
  - authors, series and bookAuthors/bookSeries via `itemwrite`.
  - episode and playlist deletion via `store.DeleteEpisodes`.
- **Scanner reads:** item folder files (`metadata.json`, `*.opf`, `*.nfo`, `desc.txt`, `reader.txt`) and `/metadata/items/<id>/metadata.json` as a fallback.
- **Scanner writes:** `metadata.json` (MarshalIndent, two spaces), either in the item folder when `storeMetadataWithItem` is on or in `/metadata/items/<id>/`. With the item-folder variant it also updates libraryItems.libraryFiles, size and mtime.
- **Audiotools:** reads libraryItems and books; makes no DB writes. It rewrites audio files in place (`.abs-tmp` then rename) and writes the merged `<item>.m4b`. It also uses `/metadata/cache/items/<id>/` for `metadata.txt`, `inputs.txt` and backup copies of the originals.
- **Backup:** reads `config/absdatabase.sqlite`, `/metadata/items` and `/metadata/authors`.
  - Takes a snapshot with `VACUUM INTO config/absdatabase.<id>.sqlite`.
  - Archive `backups/<YYYY-MM-DDTHHMM>.audiobookshelf` is a zip: `absdatabase.sqlite`, `metadata-items/…`, `metadata-authors/…` (dir entries end in `/`), and `details`.
  - `details` is 4 lines: `id\nkey\ncreatedAtMs\nserverVersion` (no trailing newline).
  - Restore stages into `config/backup-restore-*` and hands off to `server/manager.go:122 restore`. That function swaps the DB (`.restore-previous`), swaps the metadata dirs and rebuilds the Runtime.
- **Persisted JSON shapes:** libraryItems.libraryFiles (`libraryFile`), books.audioFiles (`mediafiles.AudioFile`), books.chapters (`AudioChapter`), podcastEpisodes.audioFile.

## 3. COUPLING
- The only importer of scanner, audiotools and backup is `server/runtime.go:97-160`.
- Scanner exports:
  - `WriteMetadata`, injected as `catalog.Deps.ItemChanged` and `people.Deps.WriteMetadata`.
  - `Refresh`, wrapped as the `libraryChanged` closure for `catalog.Deps.LibraryChanged` and `library.Deps.LibraryChanged`. It is also called from `adminops.Deps.SettingsChanged`.
  - `UpdateWatchedPath`, as `adminops.Deps.WatcherUpdate`.
  - `Cancel`, called from `Dispatch("cancel_scan")`.
- Scanner imports about 20 internal packages: `itemwrite.Env{DB, Filters, Emit}`, `filterdata.Cache`, `events.Emitter`, `items`/`books` (for the event shape), `playlistview`, `serversettings`.
- Backup hooks (`Options.Restore`, `UpdatePath`, `NotificationFunc`):
  - `Restore` is `Manager.restore` and exists because restore must close and rebuild the whole Runtime.
  - `UpdatePath` persists the path setting.
  - `NotificationFunc` is `notifications.TriggerEvent`, passed as a callback to avoid an import.
  - `adminops.SettingsChanged` calls `backup.UpdateOptions`.
- mediatools is used by scanner, audiotools, catalog (cover resize, ffprobe route), people (author image), playback (HLS), and podcasts (downloads and episode_registration).
- cronspec is used by backup, adminops, catalog and podcasts. Scanner does not use it (see §5).

## 4. TESTS
- **Scanner:** all 13 files are `package scanner` (in-package).
  - About 70% drive the public flow through `Scan`, `scanFilesChanged` or `Start` on a temp dir, with fake ffprobe/ffmpeg. This includes `scan_golden_test.go`, which records DB rows and events and regenerates with `UPDATE_GOLDEN`.
  - About 30% are white-box: `plan_test` (18 unexported calls), `matcher_test`, `begin_test`, and about 10 in `scanner_test` (smartAudioOrder, probeAudio, buildChapters, parseOPF, extractors).
- **Backup:** about 90% black-box through `Routes` and httptest. The rest is `readBackup` and Create.
- **Audiotools:** about 85% black-box via routes (fake ffmpeg). `tools_test` calls `merge` directly.
- **Cronspec:** trivial.

## 5. COMPLEXITY SOURCES
1. `files.go:157-520` (~360 lines): metadata sources.
   - `parseAbsMetadata` uses `map[string]json.RawMessage` plus an `absPresent` map.
   - Also `scalars()` pointer lists, the NFO key map, `parseOPF` and merge helpers.
   - The same `metadata.json` shape is written separately in `metadata.go:65-116` via `map[string]any`, so there are two untyped codecs for one document.
2. `import.go:296-413` + `62-109` (~170 lines): two 20-column hand-written SQL strings with `mustJSON`/`NullIfBlank`, a `planInput`/`itemPlan` pair, and separate book and podcast apply paths.
3. `watcher.go:372-586` (~215 lines): `scanFolderUpdates` and `rescanStoredItem`. They hold SQL `LIKE` path lookups, inode `json_each` fallbacks, a first-pass remap, and an item-handling flow that duplicates `scanLibrary`.
4. Five separate "match by path, then inode" implementations: `matcher.go` (104), `libraryfiles.go:81-122`, `files.go:133`, `watcher.go:355-460`, `import.go:329-357`.
5. `scanner.go:656-809` (150 lines): `scanLibrary`. It interleaves ordering, claiming, 10-item event chunking, extraData read-modify-write via `map[string]any`, and finalize.
6. `walk.go:83-161` (~80 lines): `updateGroup` and `claimGroups` emulate JS object key order and a "nil list means single file" convention. `groupFiles` also re-sorts afterwards.
7. `files.go:542-613`, `798-890`, `615-730` (~230 lines): `probeAudio`, an O(n·m) `goto`-based tag alias scan, `applyAudioMetadata`, and `smartAudioOrder` with closures.
   - `podcasts/episode_registration.go:30-80` repeats the probe to `AudioFile` mapping.
   - `podcasts/downloads.go:276` calls `Probe` separately.
8. `metadata.go:16-63` + `eventshape.go` (~110 lines): `WriteMetadata` marshals the Expanded item, decodes it back into `map[string]any`, then re-selects fields. `instanceShape` mutates `map[string]any` entries to imitate structuredClone of a JS instance (`wasModified`, null metaTag keys).
9. `audiotools/tools.go:46-160` + `164-205` (~150 lines): `task.data map[string]any` cloned through a JSON roundtrip, `chapterDTOs` as maps, `loadItem` with 11 `sql.NullString`, and the `ffmetadata` writer.
10. `backup.go:430-515` + `596-729` (~230 lines): hand-rolled `addDir`/`addFile`, zip entry extraction with path checks, `treeBytes`, `details` text parsing. `Options` carries three function hooks.
11. `scanner.go:405-522`: `arm`/`eventLoop`, with a linear `libraryOf` root scan and a best-effort watch.
12. **Bug and inconsistency:** the scanner uses `cron.New()` (default parser, accepts `@hourly` and `@every`) instead of `cronspec.New()`. A single bad library cron makes `Start` fail, which fails server start.

## 6. CLIENT DEPENDENCE
- **Likely depended on:**
  - Task shape: `scanResults.{added,updated,missing,elapsed}` is read in `ItemTaskRunningCard.vue`.
  - Task actions: `library-scan`, `embed-metadata`, `encode-m4b`.
  - Socket events: `items_added`, `items_updated`, `metadata_embed_queue_update`, `track_finished`, `queuedTaskData.embedMetadata`.
  - Backups page fields: `backupLocation`, `backupPathEnvSet`.
  - Audio file fields: `trackNumFromMeta` and `embeddedCoverArt` (edit page).
- **No hit in client:** `wasModified` and `authors_num_books_updated`. A `watcher-scan` hit was not found either. Whether the mobile app reads them is unknown.
- **Item 8, null `metaTags` keys:** unknown.
- **Item 12:** upstream bad-cron behaviour is unknown.
- **Scan tasks in `GET /api/tasks`:** they do not appear there (only audiotools tasks do). Whether the client or mobile relies on that is unknown.

## 7. GREENFIELD PROPOSAL
- **Packages:**
  - `media` (filekind + mediatools + typed `Probe` → `AudioFile` in one place, used by scanner and podcasts).
  - `scan` (discover, group, match, plan, apply, watch).
  - `sidecar` (typed `MetadataJSON` struct: read and write, plus opf/nfo readers).
  - `backup` (create/restore as a core with the HTTP layer separate).
  - `tasks` (typed task registry, shared by scan, tools and /api/tasks).
- **Pipeline:** `walk` → `[]Candidate{path, ino, files}` → one `Match(stored []Item, found []Candidate)` (single path-then-inode function) → `Resolve(metadata precedence)` → `Plan` → `Apply(tx)`, with watcher batches feeding the same stages after re-walking only affected item dirs.
- **Typed DB:** `ItemRow`/`BookRow` structs. The repo already has sqlc, so use generated queries.
- **Libraries:** fsnotify (keep); `robfig/cron` (keep); stdlib `zip.Writer.AddFS(os.DirFS)` and `os.CopyFS(stage, zipReader)` (zip.Reader is an fs.FS, so path safety comes from `fs.ValidPath`; keep a size cap); a small debounce on `time.AfterFunc`; `golang.org/x/sync/errgroup` only if parallel probing is wanted.
- **Estimated lines:** scanner 3,708 → about 1,800; audiotools 576 → about 350; backup 729 → about 380; mediatools+filekind+cronspec 223 → about 220 (more if probe mapping is absorbed). Net about 5,240 → about 2,750.

## 8. HAZARDS
- **Item identity:**
  - Path first, then inode, matching `ex.ino` or any stored `libraryFiles[].ino`. The first free group in scan order wins, and groups are claimed once.
  - A podcast folder that still exists on disk is never marked missing.
  - Existing items are processed in `ORDER BY rowid`, then new groups in path order.
  - The stat/inode/path comparisons in `plan()` decide the `updated` flag; sameDBTime compares at millisecond precision.
- **Library files diff:** stored entries keep their order, `addedAt` and `fileType`. Ebooks added to a book get `isSupplementary:true`. Numbers (sizes, `*Ms`) are float64.
- **Audio entries:** an unchanged file keeps its stored entry. A rescanned file keeps `addedAt`, `updatedAt` and `manuallyVerified` from the prior entry. Files with no audio stream are dropped. `smartAudioOrder` renumbers `index` from 1.
- **Metadata precedence:** library `settings.metadataPrecedence` order. `absMetadata` overwrites a scalar even with `""`, but arrays and chapters only when non-empty. If `lastScanMetadataPrecedence` differs, the next scan is forced (books only).
- **Chapters:** sidecar chapters override built chapters. The built-chapter rules: identical chapters across files collapse; otherwise offsets accumulate and chapters shorter than 0.1 s are skipped; title falls back to the filename or unique `tagTitle`.
- **Scan events:**
  - `items_added` and `items_updated` go out in chunks of 10.
  - `authors_num_books_updated` is emitted once at the end, then orphan authors and series are swept.
  - Task events: `library-scan` has `ShowSuccess`, `scanResults` has `missing`, `watcher-scan` has `removed`.
  - One scan per library at a time. Watcher batches wait for the running scan; only one batch runs at a time and the rest queue.
  - `cancel_scan` cancels the run context.
- **Watcher:** the debounce is 10 s from the last event. A file that is still being written is polled every 3 s (up to 200 polls) until its mtime is stable, and the timer re-arms while any file is still in flight. `change` events are ignored. `.ignore` files, dot-prefixed names and `@eaDir` are skipped.
- **Backup restore:**
  - Zip entries that are symlinks or escape the staging dir are rejected, with a 20 GiB cap.
  - A DB opened from the backup must validate before the swap.
  - The previous DB and metadata dirs are kept as `.restore-previous` and rolled back on failure.
  - `backup_applied` is emitted only after a 200.
  - Backup IDs and filenames derive from the `details` file, and duplicate IDs are ignored at load.
- **Sidecar write:** `metadata.json` must stay readable by TS (series as `"Name #seq"` strings, `explicit` and `abridged` booleans).
