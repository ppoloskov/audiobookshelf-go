# Roadmap: the rewrite

The greenfield rewrite is done. The result is described in
[ARCHITECTURE.md](ARCHITECTURE.md); the current state and open items are in
[handover.md](handover.md). Below, the waves as they were planned, with the old
package each task replaced. Old names are history: none of them exists in
`internal/` any more.

Per-area facts of the old code (routes, tables, hazards) are in
[docs/survey/](docs/survey/).

## Next: Oracle Parity & Desloping Roadmap

Baseline from `make oracle` (2026-11-20, pinned upstream TS v2.37.2 @
`edb1f7ef`):

- **Oracle coverage:** **80.1%** TS V8 in-scope lines (**82.7%** functions),
  **71.5%** Go statement coverage, **218/218 (100%)** routes hit
  (**216/218 = 99.1%** happy-path, **214/218 = 98.2%** error-probed), **8/9**
  inbound & **59/62** outbound Socket.IO events, **171/171 (100%)** web `$axios`
  routes.
- **Current Go-vs-TS gap:** **768 unexpected findings across 205 groups** (`670`
  HTTP, `71` SQLite, `21` Socket.IO, `3` stubs, `3` test errors).
- **Codebase size:** **35,350 prod LOC** (+ 18,719 test LOC) across 32 packages
  in `internal/`.

### Desloping & Simplification Assessment

- **Soft architectural target:** **~20K prod LOC (excluding tests)**, down from
  ~35.8K today — achieved
  **strictly through simplification and better higher-order abstractions**
  (never golfing or sacrificing structure/readability).
- The Waves 1–4 rewrite already removed the major legacy slop (`TODO/FIXME` = 0,
  stray `*-E` files = 0, legacy `logging.*f` = 2 in `internal/`,
  `map[string]any` = 23 remaining after Wave P2).
- **Oracle parity achieved (2026-11-20):** Waves P1–P5 reduced the oracle parity
  backlog from **768 → 0 findings (0 groups)**! `make verify` exits **0**:
  - **Flow Steps:** `PASS (377 / 377)`
  - **Complex Tests:** `PASS (17 / 17)`
  - **Route Matrix:** `PASS (654 / 654, 0 unexpected)`
  - **SQLite (26 tables), Socket.IO (3 audiences), Outbound Stubs, Bidirectional
    TS↔Go Rollback:** `0 unexpected findings`
  - **TS V8 In-Scope Coverage:** `80.3%` lines (`32,730 / 40,752`), `82.8%`
    functions; **Go Statement Cov:** `71.4%`; **Routes Happy-Path:**
    `217 / 218 (99.5%)`.

| Opportunity                                                       | Target Package(s)                                       |              Est. LOC Reduction | Parity Findings Closed |
|-------------------------------------------------------------------|---------------------------------------------------------|--------------------------------:|-----------------------:|
| Split historical vs live session wire DTO                         | `internal/playback`                                     |                        ~100–150 |             ~245 (32%) |
| Canonical sort tie-breaker & shared view enrichment               | `internal/browse` (2,205 LOC)                           |                        ~300–400 |             ~226 (29%) |
| Unify `AudioFile` ffprobe/metaTags builder                        | `internal/podcast` + `internal/scan` + `internal/media` |                        ~250–350 |               ~65 (8%) |
| Shared ordered-container CRUD (`playlist` 662 + `collection` 521) | `internal/list` (1,408 LOC)                             |                        ~350–450 |                     ~2 |
| Centralize `saveAndBroadcastItem` / `titleIgnorePrefix`           | `internal/item` (4,355 LOC)                             |                        ~300–400 |                    ~30 |
| Edge JSON-syntax middleware for POST/PATCH/PUT                    | `internal/server` + `internal/httpx`                    | ~50 (avoids per-handler checks) |                    ~55 |
| Replace remaining `map[string]any` payloads with structs          | `internal/notify` (14), `podcast` (4), `backup` (4)     |                        ~100–150 |                      — |
| Semantic XML / MIME / URL-query diffing + intentional divergences | `testmachine/oracle` (`diff.go`, `divergences.json`)    |                               — |    ~20 false positives |

---

### Parity Backlog by Module (Prioritized Waves to Zero Findings)

#### Wave P1 — Highest-Impact Parity + Desloping (~470 findings, ~61% of backlog) — DONE (`768 -> 349` findings, `-54.6%`)

- [x] **`internal/playback` + `internal/user` progress/sessions (~245 findings
      closed)**
  - Split stored-session DTO (`HistorySession`) from admin-enriched DTO
    (`AdminHistorySession`, used on `GET /api/sessions` and
    `GET /api/users/{id}/listening-sessions`) — closed all **231** historical
    session extra-field findings.
  - Fixed `deviceDescription` formatting (`"null null / null"`), emitted
    `user_stream_update` on session close and `user_updated` on progress delete,
    fixed podcast session `displayTitle`/`displayAuthor` SQLite `NULL`, and
    aligned `PATCH /api/me/progress/:id` missing-ID `404`.

- [x] **`internal/browse` (~175 findings closed)**
  - Consolidated sort/pagination/series/book-media helpers (`sortDir`,
    `pagedQuery`, `updateBookMedia`, `seriesRefOf`), fixed secondary sort
    tie-breakers (`authorName`/`authorNameLF`, `series.<id>` sequence, `series`
    `numBooks` desc), and populated `rssFeed` on `filter=feed-open` and
    `media.metadata.series` on `filter=series.<id>`.
  - Remaining in `browse`: `library-items-filter-author` &
    `library-items-collapseseries-desc` tie order (tracked for quick follow-up).

#### Wave P2 — Podcast, Scanner & Item Media Pipeline — DONE (`349 -> 220` findings, `-71.4%` cumulative)

- [x] **`internal/media` + `internal/podcast`**
  - Extracted shared `media.ProbeAudio` / `AudioMetaTags` (all 37 tags) into
    `internal/media/audio.go` (ready for `internal/scan/audio.go` to reuse and
    drop ~180 LOC of duplication).
  - Replaced `map[string]any` notification payloads in `internal/podcast` with
    typed structs; fixed `podcast-after-download` (45 findings), RSS feed
    nullability (14 findings), `clear-queue`/`match-episodes` auth & bad-JSON
    handling, `search-episode` substring similarity, and `create-podcast`
    response/SQLite shape.
- [x] **`internal/scan`**
  - Fixed `POST /api/libraries/:id/scan [bad_json]` launching background scans
    (eliminating all 31 SQLite mutation findings), implemented embedded cover
    art extraction on book scan, fixed `books.ebookFile` Shape 1/Shape 2 + `ino`
    + `isSupplementary` parity, and removed redundant `stat`/time helpers.
- [x] **`internal/item`**
  - Consolidated `applyTitleAndCover`,
    `announceItem`/`announceHTTP`/`finishWith`, and `setTextIfMissing`; fixed
    `patch-saga1-tracks-reorder` non-expanded `Full` response (14 findings),
    `quick-match` `titleIgnorePrefix` (12 SQLite findings),
    `autoDownloadSchedule: ""` clearing (12+ findings), `Expanded.LastScan`
    `omitempty`, and `ffprobe` middleware check order.

#### Wave P3 — HTTP Edge Middleware & Domain Parity — PARTIALLY DONE (`220 -> 131` findings, `-82.9%` cumulative; Route Matrix `97 -> 1`)

- [x] **`internal/server` & `internal/scan/audio.go`**
  - Added global `validateJSONBody` middleware (`10 MiB` limit, matching
    `express.json()`) — collapsed Route Matrix unexpected findings from
    **55 → 1** (653/654 passing).
  - Deduplicated `internal/scan/audio.go` onto `media.ProbeAudio` /
    `media.LeadingNumber` (**-71 prod LOC**, raised `internal/media` coverage
    `78.0% → 87.1%`).

- [x] **`internal/library`, `internal/backup`, `internal/auth`,
      `internal/user`**
  - `GET /api/libraries?include=stats` returns compact stats (`totalSize`,
    `totalDuration`, `numAudioFiles`, `totalItems`); `libraries.extraData`
    persists as SQLite `NULL`.
  - `PATCH /api/backups/path` returns `text/plain` `"OK"`;
    `POST /api/backups/upload` returns `400` on invalid multipart body.
  - `POST /api/api-keys` / `PATCH /api/api-keys/{id}` match upstream omitted
    fields; `PATCH /api/me/password` sets rotated `refresh_token` cookie and
    nulls `sessions.lastRefreshToken*`; `PATCH /api/users/{id}` returns `404` on
    missing user ID before body decode.

- [ ] **Remaining in Wave P3 (`internal/share`, `internal/list`,
      `internal/mail`, `internal/notify`)**
  - `internal/share`: fix `get-public-share-by-slug` / `get-public-share-init`
    `playbackSession.id` UUIDs and `get-feed-item-range`.
  - `internal/list`: unify `playlist.go` (662 LOC) + `collection.go` (521 LOC)
    ordered-container helpers (~350–450 LOC savings) and fix
    `playlist-batch-remove` `order` re-indexing.
  - `internal/mail` & `internal/notify`: set `application/epub+zip` attachment
    MIME in `mail`; replace the 14 `map[string]any` usages in `notify` with
    typed structs.

#### Wave P4 — Remaining Parity, List Desloping & Oracle Calibration — DONE (`131 -> 47` findings, `-93.9%` cumulative; Route Matrix `654/654 PASS`)

- [x] **`internal/item/patch.go` & `internal/browse/filter.go`**
  - Preserved `autoDownloadSchedule: ""` as SQLite `text` empty string
    (eliminated 16 findings across SQLite/HTTP/Socket.IO).
  - Added deterministic unsorted tie-breakers (`ORDER BY b.id` for
    `filter=authors.<id>`, `ORDER BY li.rowid` for other unsorted book queries,
    matching upstream post-`ANALYZE` SQLite plans).
- [x] **`internal/notify`, `internal/list`, `internal/share`**
  - `PATCH /api/notifications/{id}` checks existence (`404`) before body decode
    (**Route Matrix hits 654/654 = 100% PASS**); replaced `map[string]any` in
    `notify` responses with typed structs.
  - Extracted shared ordered-container helpers into `internal/list/list.go`
    (`-90 LOC` across `collection.go` + `playlist.go`) and fixed
    `playlist-batch-remove` `order` parity (**0 SQLite findings**).
  - Separated `share` `playbackSession.id` (fresh random v4 UUID) from the
    `share_session_id` cookie.
- [x] **`testmachine/oracle` (`diff.go` & `divergences.json`)**
  - Added semantic XML inter-tag whitespace normalization, parsed SMTP MIME part
    comparison, canonical HTTP stub query-param sorting, and recorded
    `Accept-Ranges: bytes` + `backups` Content-Type intentional divergences.
- [x] **Wave P5 — Final 47 Findings → Zero-Divergence CI (`47 -> 0` findings,
      `make verify` exit 0!)**
  - `internal/library/write.go`: fixed real production bug querying
    `customMetadataProviders WHERE id = ?` (was nonexistent `slug` column).
  - `internal/db/documents.go` + `internal/scan/import.go`: added
    `db.AudioIndex` (marshals `0` as `null` for scanned podcast episodes) and
    paired podcast episodes by path/inode on rescan (fixing episode
    duplication).
  - `internal/scan/watcher.go` + `scan.go` + `tasks.go`: added `2s`
    `watcherRenameTimeout` matching `Watcher.js` (eliminating spurious
    `task_started` on hard-delete) and fixed canceled library scan emitting
    `task_failed`.
  - `internal/podcast/episode.go`: announced `playlist_updated` before trimming
    `libraryFiles` on episode delete.
  - `testmachine/oracle`: normalized ephemeral server base URLs, self-closing
    XML tags, `<lastBuildDate>`/`<pubDate>` drift, `discover` random-shelf
    entity order, `416` `Accept-Ranges`, and `coverage_remaining_routes.json`
    feed slug capture.
- [x] **Wave S1 — Structural Compression & Empirical LOC Check (`54997df5`)**
  - Compressed `internal/metadata` (`-154 LOC`, 18 → 11 files via
    `fetchJSON[T]`/`queryURL`/`searchBookOnce`), `internal/item` (`-72 LOC` via
    Book/Podcast wire struct embedding & shared chunked SQL), `internal/list`
    (`container[R, V]` across `collection.go`/`playlist.go`), and
    `cmd/monstrobook` + `internal/logging` (migrated `main.go` to `slog`,
    removed 9 unused printf helpers, `-35 LOC`).
  - Verified `make verify` exit `0` (**0 findings**, Go coverage
    `71.4% → 71.7%`). Confirmed empirically that the Waves 1–4 rewrite (~36.1K
    prod LOC vs 92.7K upstream TS = 2.6x smaller) is already tight without
    golfing.
- [x] **Gate 1 — Pre-Release Security & State-Machine Hardening** (see
      [docs/RELEASE_CHECKLIST.md](docs/RELEASE_CHECKLIST.md)):
  - Fixed High/Medium Security findings (`GET /api/users/{id}` root token
    redaction, `syncLocal` session IDOR & item access, `item` explicit/tag
    checks, OIDC `email_verified == true`, backup ZIP-bomb `io.LimitReader`,
    `encode-m4b` allowlist, `config.Config.String()` redaction).
  - Fixed High/Medium Open State-Machine & Concurrency findings (idle playback/HLS
    reaper + `localIDs` cap, `share` session TTL/cap, `podcast/queue` download
    timeout, `X-Forwarded-For` proxy trust + OIDC flow cap, `library-match-all`
    `defer TaskFinished`, `Scanner`/`tools`/`Hub` `WaitGroup` drain,
    `http.Server` `IdleTimeout`/`MaxHeaderBytes`).
- [ ] **Gates 2–4 — First Public Release (`v0.1.0`)**: CI green, container
      upgrade/rollback & SIGTERM soak, Web SPA E2E + real mobile app acceptance,
      tag `v0.1.0` ([docs/RELEASE_CHECKLIST.md](docs/RELEASE_CHECKLIST.md)).

## Open decisions

| Question                                  | Default until decided |
|-------------------------------------------|-----------------------|
| Run `ANALYZE` at boot (upstream does)     | no                    |
| Port upstream's boot-time `cleanDatabase` | no                    |
