# Roadmap: the rewrite

The greenfield rewrite is done. The result is described in
[ARCHITECTURE.md](ARCHITECTURE.md); the current state and open items are in
[handover.md](handover.md). Below, the waves as they were planned, with the old
package each task replaced. Old names are history: none of them exists in
`internal/` any more.

Per-area facts of the old code (routes, tables, hazards) are in
[docs/survey/](docs/survey/).

## Wave 1: foundation

- [x] **F1 `db`**: `DB` handle over sqlx with context transactions, `Time`,
  `JSON[T]`, `Bool`, a row struct for all 26 tables, a struct for every
  JSON-column document, `Insert`/`Update`. Tests: every struct against the
  pinned schema; every JSON cell of the fixture database round-trips by value.
- [x] **F2 `httpx`**: `Handler`, `Error`, `JSON`, `Decode[T]`, query helpers,
  file serving, the SSRF-safe client.
- [x] **F3 `auth` core**: `Principal` from a `db.User`, typed permissions and
  their decisions, `MediaClause`, context accessors, `Admin` wrapper.
- [x] **F4 slog bridge**: `log/slog` default handler feeding the existing log
  pipeline, so new code logs with slog from day one.

## Wave 2: keystones (parallel)

- [x] **K1 `item` read model**: one loader for items, books, podcasts and
  episodes; minified/full/expanded views as typed structs. Replaces `items`,
  `books`, `podcasts/read`, `mediafiles`.
- [x] **K2 `settings`**: typed server, email and notification documents with
  atomic `Update`. Replaces `serversettings`, `emailsettings`, `settingsdoc`.
- [x] **K3 `auth` flows**: login, refresh, logout, init, API keys, OIDC on
  `x/oauth2`, the authentication middleware. Replaces `identity`, `auth`
  (old), `access`, `users`.
- [x] **K4 `logging`**: slog handler with console, daily files, ring buffer and
  socket listeners. Replaces `logging`.

## Wave 3: domains (parallel, on the keystones)

- [x] **D1 `metadata`**: one typed result per search kind behind a small
  provider interface. Replaces `providers`, `wire` match types.
- [x] **D2 `item` writes**: media PATCH, batch, relations, deletion, covers,
  files, quick match, `metadata.json` writer. Replaces `catalog`,
  `itemwrite`, `sqlupdate`.
- [x] **D3 `library`** (as `libraries`, `browse` and `author`): libraries,
      browse, shelves, search, series, authors, narrators, stats, filter data.
      Replaces `library` (old), `people`, `filterdata`.
- [x] **D4 `list`**: collections and playlists. Replaces `lists`,
  `playlistview`.
- [x] **D5 `playback`**: sessions, progress, bookmarks, listening stats, HLS,
  devices. Replaces `playback` (old), `progressdto`, the
  progress half of `userapi`.
- [x] **D6 `share`**: shares and RSS feeds out with `encoding/xml`. Replaces
  `sharing`, `feeds`.
- [x] **D7 `podcast`**: feed, creation, downloads, schedule, episodes.
  Replaces `podcasts`.
- [x] **D8 `scan`**: walk, match, plan, apply; watcher; sidecar readers.
  Replaces `scanner`.
- [x] **D9 `user`**: account, users admin, API-key routes. Replaces the rest
  of `userapi`.
- [x] **D10 `admin`, `notify`, `mail`, `backup`, `tools`**: replaces `adminops`,
  `emailapi`, `notifications`, `backup` (old), `audiotools`.

## Wave 4: close

- [x] **C1 `app`** (kept the package name `server`): composition root and
      middleware chain; delete the hand-rolled dispatch, Express error pages and
      route aliases. Add the CORS middleware upstream has and Go lacks (mobile
      ebook and cover loads).
- [x] **C2 `realtime`**: typed socket actions (`realtime.Actions`). `events`
      stayed a separate leaf package: domains import it, not `realtime`.
- [x] **C3** delete the old shared packages and sqlc; merge the leaves into
  `text` and `media`; remove `db.DB.SQL()`.
- [x] **C4** tooling: drop the ratchet, dbnest and depguard layers; CI runs
  build, vet, tests, lint, cross-build and rollback.
- [x] **C5** README and docs reflect the result; rollback and journey checks
  pass; push.

## Next: Oracle Parity & Desloping Roadmap

Baseline from `make oracle` (2026-11-20, pinned upstream TS v2.37.2 @ `edb1f7ef`):
- **Oracle coverage:** **80.1%** TS V8 in-scope lines (**82.7%** functions), **71.5%** Go statement coverage, **218/218 (100%)** routes hit (**216/218 = 99.1%** happy-path, **214/218 = 98.2%** error-probed), **8/9** inbound & **59/62** outbound Socket.IO events, **171/171 (100%)** web `$axios` routes.
- **Current Go-vs-TS gap:** **768 unexpected findings across 205 groups** (`670` HTTP, `71` SQLite, `21` Socket.IO, `3` stubs, `3` test errors).
- **Codebase size:** **35,350 prod LOC** (+ 18,719 test LOC) across 32 packages in `internal/`.

### Desloping & Simplification Assessment

The Waves 1–4 rewrite already removed the major legacy slop (`TODO/FIXME` = 0, stray `*-E` files = 0, legacy `logging.*f` = 2 in `internal/`, `map[string]any` = 27 total). However, **~90% of the 768 oracle findings collapse into ~15 root causes**, and fixing them via targeted structural consolidation can cut **~1,500–2,500 prod LOC** while closing the parity gap:

| Opportunity | Target Package(s) | Est. LOC Reduction | Parity Findings Closed |
| --- | --- | ---: | ---: |
| Split historical vs live session wire DTO | `internal/playback` | ~100–150 | ~245 (32%) |
| Canonical sort tie-breaker & shared view enrichment | `internal/browse` (2,205 LOC) | ~300–400 | ~226 (29%) |
| Unify `AudioFile` ffprobe/metaTags builder | `internal/podcast` + `internal/scan` + `internal/media` | ~250–350 | ~65 (8%) |
| Shared ordered-container CRUD (`playlist` 662 + `collection` 521) | `internal/list` (1,408 LOC) | ~350–450 | ~2 |
| Centralize `saveAndBroadcastItem` / `titleIgnorePrefix` | `internal/item` (4,355 LOC) | ~300–400 | ~30 |
| Edge JSON-syntax middleware for POST/PATCH/PUT | `internal/server` + `internal/httpx` | ~50 (avoids per-handler checks) | ~55 |
| Replace remaining `map[string]any` payloads with structs | `internal/notify` (14), `podcast` (4), `backup` (4) | ~100–150 | — |
| Semantic XML / MIME / URL-query diffing + intentional divergences | `testmachine/oracle` (`diff.go`, `divergences.json`) | — | ~20 false positives |

---

### Parity Backlog by Module (Prioritized Waves to Zero Findings)

#### Wave P1 — Highest-Impact Parity + Desloping (~470 findings, ~61% of backlog)

- [ ] **`internal/playback` (~245 findings: 231 HTTP, 9 Socket.IO, 4 SQLite)**
  - **Refactor:** Split stored-session DTO (`PlaybackSessionView`) from active-stream DTO (`LivePlaybackSessionView`) in `history.go` / `session.go` so historical endpoints (`/api/me/listening-sessions`, `/api/me/listening-stats`, `/api/users/:id/listening-stats`, `/api/me/item/listening-sessions/*`) do not emit 7 extra fields (`audioTracks`, `coverAspectRatio`, `lastSave`, `mediaItemShareId`, `shareSessionId`, `stream`, `user`) — closes **231 findings**.
  - Fix `deviceDescription` formatting when device fields are empty/null (`"null null / null"` vs `"  / "`) on `user_item_progress_updated` (5 socket findings) or record in `divergences.json` if clients ignore it.
  - Emit `user_stream_update` on session close (`close-open-session`, `close-episode-playback-session`) and `user_updated` on progress delete (`me-delete-progress-saga2`).
  - Fix podcast playback session `displayTitle` (podcast title vs episode title) and `displayAuthor` SQLite `NULL` vs `""` (`delete-playback-session`, `sync-local-sessions-batch`).
  - Align `PATCH /api/me/progress/:libraryItemId/:episodeId?` missing-ID status (`404` vs `400`).

- [ ] **`internal/browse` (~226 findings: 226 HTTP)**
  - **Refactor:** Consolidate ad-hoc sort/filter/pagination pipelines across `items.go`, `filter.go`, `series.go`, `subseries.go`, `collapse.go`, and `shelves.go` (2,205 LOC) onto one deterministic tie-breaker comparator and shared view-enrichment hook (~300–400 LOC savings).
  - Match upstream secondary sort tie-breakers for `books-sort-author-name`, `library-items-filter-author`, `books-filter-series-id`, `books-filter-feed-open`, `library-items-collapseseries-desc`, and `library-series-sort-numbooks-desc`.
  - Populate `rssFeed` on `filter=feed-open` responses (`books-filter-feed-open`, 2 findings) and `media.metadata.series` on `filter=series` responses (`books-filter-series-id`, 2 findings).

#### Wave P2 — Podcast, Scanner & Item Media Pipeline (~155 findings)

- [ ] **`internal/podcast` (~75 findings: 62 HTTP, 3 Socket.IO, 9 SQLite, 1 test_error)**
  - **Refactor:** Deduplicate `AudioFile` ffprobe/metaTags construction between `internal/podcast/episode.go` and `internal/scan/audio.go` into one shared helper in `internal/media` (~250–350 LOC savings) — closes all **45 `podcast-after-download` findings** (missing `metaTags`, `bitRate`/`duration`/`size` drift).
  - Fix `autoDownloadSchedule` default `"30 5 * * *"` overwriting explicit empty `""` (`podcast-clear-auto-download-cron`, `podcast-library-items-sort-title`, `podcast-library-recent-episodes`, and 9 SQLite/Socket rows).
  - Fix RSS feed parser nullability: empty strings `""` -> `null` and `nil` categories -> `[]` (`podcast-fetch-stub-feed`, `fetch-and-parse-podcast-feed`).
  - Fix `GET /api/podcasts/:id/clear-queue` permission check (`403` for non-admin listener, currently `200`).
  - Fix `create-podcast` extra `lastScan`/`scanVersion` fields and `podcasts.numEpisodes` SQLite `NULL` vs `0`.
  - Investigate & fix `podcast-search-episode-pilot` (0 vs 1), `podcast-set-feed-url` (6 vs 3 episodes), `POST /api/podcasts/:id/match-episodes [bad_json]` (500 vs 400), and `TestOracleGapsPodcastMaxKeepAndMatchAll`.

- [ ] **`internal/scan` (~45 findings: 5 HTTP, 39 SQLite, 1 test_error)**
  - Fix background scanner/watcher mutating `books`/`libraryItems` inodes/timestamps during read-only/rejected route-matrix requests (**31 SQLite findings** across `POST /api/me/ereader-devices`, `POST /api/libraries/order`, `GET /api/libraries/:id/stats`, `GET /api/libraries/:id/series`).
  - Extract embedded cover art during book scan (`gap-embedded-cover` 404 vs 200 JPEG, `gap-embedded-after-cleanup` `coverPath` null).
  - Stop persisting extra `fileType`/`isSupplementary` fields and update `ino` on `books.ebookFile` during book scan (`post-book-scan-db`, `rename-scan-db`, `delete-scanned-audio-file-db`, 9 SQLite findings).
  - Investigate & fix `TestOracleDeepScanAuthorSeriesCleanupAndCancel`.

- [ ] **`internal/item` (~35 findings: 15 HTTP, 7 Socket.IO, 12 SQLite)**
  - **Refactor:** Centralize item reload + `titleIgnorePrefix` recomputation + event broadcast across `patch.go`, `write.go`, `media.go`, `match.go`, `files.go`, `delete.go` (4,355 LOC; ~300–400 LOC savings).
  - Fix `patch-saga1-tracks-reorder` returning expanded fields (14 extra fields vs TS minified/raw media).
  - Fix `titleIgnorePrefix` recomputation on quick-match / batch-quick-match (`books` + `libraryItems` SQLite drift, 12 findings).
  - Stop emitting `task_started` on `batch-delete-disposable` and `disposable-delete-hard` (6 socket findings).
  - Refresh `numFiles` before broadcasting `playlist_updated` on `podcast-delete-ep3-soft` (1 socket finding).
  - Align `GET /api/items/:id/ffprobe/:fileid [listener]` status (`404` vs `403`).

#### Wave P3 — HTTP Edge Middleware & Remaining Domain Modules (~100 findings)

- [ ] **`internal/server` & `internal/httpx` (~55 findings: 55 HTTP)**
  - **Refactor:** Add a lightweight JSON-syntax pre-check middleware for requests carrying `Content-Type: application/json` (matching Express `express.json()` returning `400` before route/auth dispatch) — closes **~35 `[bad_json]` route-matrix groups** (across `auth`, `admin`, `backup`, `item`, `library`, `list`, `playback`, `share`, `tools`, `user`, `author`) without per-handler boilerplate.
  - Record Express's `<!DOCTYPE html><pre>Bad Request</pre>` HTML error body on malformed JSON in `testmachine/oracle/divergences.json` (intentional Go improvement per `ARCHITECTURE.md`).

- [ ] **`internal/library` (~17 findings: 14 HTTP, 3 SQLite)**
  - Match `GET /api/libraries?include=stats` compact `stats` shape: add `numAudioFiles` and drop 12 extra full-stats fields (`authorsWithCount`, `genresWithCount`, `largestItems`, `longestItems`, `numAudioTracks`, `totalAuthors`, `totalGenres`).
  - Store `libraries.extraData` as SQLite `NULL` instead of `"{}"` text on create/patch/reorder (3 SQLite findings).

- [ ] **`internal/auth` & `internal/user` (~12 findings: 10 HTTP, 2 SQLite)**
  - `me-patch-password`: set rotated `refresh_token` cookie and clear `sessions.lastRefreshToken` / `lastRefreshTokenExpiresAt` to SQLite `NULL` (3 findings).
  - `api-keys-create` & `api-keys-deactivate`: omit `createdByUser` (and unset `description`/`lastUsedAt`/`permissions`) to match TS response (5 findings).
  - Align `PATCH /api/users/:id [missing_id]` (`404` vs `400`) and `PATCH /api/notifications/:id [missing_id]` (`404` vs `400`).

- [ ] **`internal/backup` (~6 findings: 6 HTTP)**
  - `backups-download`: `Content-Type: application/octet-stream` (or record `application/zip` in `divergences.json`).
  - `backups-patch-path-*`: return plain text `"OK"` instead of JSON `"\"OK\""` (5 findings).
  - `POST /api/backups/upload [bad_json]`: return `400` instead of `500`.

- [ ] **`internal/share` (~3 findings: 2 HTTP, 1 test_error)**
  - Fix `get-public-share-by-slug` / `get-public-share-init` `playbackSession.id` (replace hardcoded/placeholder UUIDs with proper generated UUIDs).
  - Investigate & fix `TestOracleFlows/coverage_remaining_routes/get-feed-item-range`.

- [ ] **`internal/list` (1 finding + high-value desloping)**
  - **Refactor:** Extract shared ordered-container CRUD/batch/hydration helpers across `playlist.go` (662 LOC) and `collection.go` (521 LOC) (~350–450 LOC savings).
  - Fix `playlist-batch-remove` `playlistMediaItems.order` re-indexing (1 SQLite finding).

- [ ] **`internal/mail` & `internal/notify` (desloping + 2 stub findings)**
  - Set `application/epub+zip` attachment MIME type in `internal/mail` when sending `.epub`.
  - **Desloping:** Replace the 14 `map[string]any` usages in `internal/notify` with typed structs.

#### Wave P4 — Oracle Differ Calibration & Final Polish (~20 false positives + housekeeping)

- [ ] **`testmachine/oracle` (`diff.go` & `divergences.json`)**
  - Canonicalize XML whitespace after `<?xml ...?>` in `diff.go` (fixes 4 RSS feed XML false positives).
  - Compare SMTP stub bodies by parsed MIME headers/parts rather than raw header order/boundary strings (fixes 2 email stub false positives).
  - Canonicalize outbound HTTP stub query-param order (fixes 1 `quick-match-saga1` stub false positive).
  - Record `Accept-Ranges: bytes` from `http.ServeFile` on cover/author images & 416 responses in `divergences.json` (7 findings).
- [ ] **Housekeeping**
  - Replace the last 2 `logging.*f` calls in `internal/` (and `cmd/monstrobook/main.go`) with `slog` and delete the legacy printf helpers.
  - [x] ~~Delete the 12 stray `*-E` files in `internal/text` and `internal/media`.~~ (already removed)
- [ ] **Device acceptance of the mobile apps** (using `cmd/abs-record` against real iOS/Android apps).

## Open decisions

| Question                                  | Default until decided |
|-------------------------------------------|-----------------------|
| Run `ANALYZE` at boot (upstream does)     | no                    |
| Port upstream's boot-time `cleanDatabase` | no                    |
