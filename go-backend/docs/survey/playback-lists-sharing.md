Production Go in scope is about 6,100 lines (playback 1,541, progressdto 620, lists 1,485, sharing 1,960, deviceinfo 54, store files 419). Tests are about 2,650 lines. No files were modified.

**1. RESPONSIBILITIES**
- playback: session lifecycle (in-memory map plus persisted rows), local/offline sync, direct media, ffmpeg HLS, admin history, device upsert.
- progressdto: `mediaProgresses` read, and the create/merge/apply rules for progress updates (port of TS `createUpdateMediaProgressFromPayload`).
- lists: playlist and collection CRUD and the per-library listings.
- sharing: RSS feeds (open/close/list, public XML), media-item shares, public share playback sessions.
- deviceinfo: 54-line narrow User-Agent parser.
- store files: `playbacksession.go` (row type, `LegacyJSON` map), `device.go` and `devices.go` (device DTO and upsert), `playlists.go` (`RemoveFromPlaylists`/`DeleteEpisodes`, used by delete paths elsewhere).
- Public routes (no auth):
  - GET `/feed/{slug}` and `/feed/{slug}/{tail...}` (this one also serves item media and cover).
  - GET `/public/share/{slug}` plus `/cover`, `/track/{index}`, `/download`; PATCH `/public/share/{slug}/progress` (this one is cookie-guarded).
  - ANY `/public/session/{id}/track/{index}` and `/hls/{id}/{name}`.
- Authenticated routes:
  - playback: POST `/api/items/{id}/play[/{episodeId}]`; GET `/api/sessions`, `/api/sessions/open`; POST `/api/sessions/batch/delete`; DELETE `/api/sessions/{id}`; POST `/api/session/local[-all]`; GET `/api/session/{id}`; POST `/api/session/{id}/sync|close`.
  - lists: `/api/collections` (GET, POST); `/{id}` (GET, PATCH, DELETE); POST `/{id}/book`; DELETE `/{id}/book/{itemID}`; POST `/{id}/batch/{action}`.
  - lists: `/api/playlists` (GET, POST); `/{id}` (GET, PATCH, DELETE); POST `/{id}/{action}` (id `collection` creates from a collection); POST `/{id}/batch/add|remove`; DELETE `/{id}/item/{path...}`.
  - lists: GET `/api/libraries/{id}/collections` and `/playlists`.
  - sharing: GET `/api/feeds`; POST `/api/feeds/{item|collection|series}/{id}/open`; POST `/api/feeds/{id}/close`; POST `/api/share/mediaitem`; DELETE `/api/share/mediaitem/{id}`.

**2. TABLES**
- playbackSessions: R/W/D. devices: R/W. mediaProgresses: R/W (and D via `store.DeleteEpisodes`).
- libraries: R (`settings` JSON, read with raw SQL per sync). libraryItems, books, podcasts, podcastEpisodes, authors: R (via `items.Load`).
- users: R (username). collections, collectionBooks: R/W/D. playlists, playlistMediaItems: R/W/D.
- feeds, feedEpisodes: R/W/D. mediaItemShares: R/W/D. series, bookSeries: R.

**3. COUPLING**
- `playback.Handler` takes `(db, cfg, emit)`.
  - It exports `OpenSessions() any`, wired into `userapi.Deps.OpenSessions`.
  - It exports `SessionOf(userID) any`, wired into the realtime hub as `Runtime.Session`.
  - It exports `CleanupOrphanStreams`, called from `server/manager.go`.
  - Both `any` returns exist to avoid a package cycle and typed-nil problems.
- progressdto is called by identity, userapi, catalog and playback.
  - `ApplyChecked(..., allow func(libraryID) bool)` is a callback only because progressdto cannot import the access package.
- `lists.Collection(..., canView func)` is the same kind of legacy callback.
- `store.RemoveFromPlaylists` returns `[]PlaylistChange` so catalog, podcasts, scanner and itemwrite can emit after commit via `playlistview.Announce`.
- `events.Emitter` is a func type, used everywhere.
- The `feeds` package (207 lines, outside my scope) duplicates the `feedRow` types that sharing keeps: `sqlc.Feed`, `feedRow` and `feeds.Feed` all describe one table.

**4. TESTS**
- playback (about 1,400 lines): roughly 85% black-box through `request(h, method, path)` on the mux.
  - White-box parts: `transcodeAudioCodec` table test; four HLS-reset tests reaching `h.sessions[id].stream`/`checkSegment`; one test injecting `&session{}`.
- sharing (474 lines): about 95% black-box via `httptest`.
- lists (about 480 lines): about 55% black-box via the mux. `reader_test` and `collections_test` call `playlistview.Load`/`Collection` directly and assert on `map[string]any` shapes, so they die with a typed rewrite.
- progressdto (255 lines): black-box through exported `Apply` with JSON-decoded `Payload`. It survives only if the `Apply(Payload)` entry point survives.
- deviceinfo (48 lines): `Parse` unit tests.

**5. COMPLEXITY SOURCES**
1. `progressdto/apply.go:31-146` (115 lines) emulates JS `Number()`, truthiness and `new Date()` through `Optional{Set,Raw}`. `:298-539` (240 lines) holds the create/merge/apply rules, of which about 120 lines are essential.
2. `sharing/feeds.go:565-701` (137 lines) is hand-built RSS XML, with a JS-Date helper `feedsrc.go:467-490` (24 lines).
3. `sharing/feedsrc.go:20-465` (445 lines) builds feed sources separately for item, collection and series, using `remarshal` (JSON round trips) to retype data. `feeds.go:26-130` (105 lines) is `sql.Null*` row plumbing.
4. `sharing/feeds.go:462-537` (75 lines) `refreshFeedIfStale` plus `feedColumnsChanged`, which emulates Sequelize skipping a no-op save.
5. `playback/hls.go:94-300` (205 lines) is the ffmpeg supervisor (mutex, `closed`/`resetting` flags, restart goroutine). `generatePlaylist` is only 14 lines.
6. Session JSON exists in three forms:
   - `playback.go:53-156,639-656` (about 110 lines): struct, `sessionJSON` embed, `nullID`, and `tracks()` building `[]map[string]any` from `audioFile.Fields`.
   - `store/playbacksession.go:88-172` (85 lines): `LegacyJSON`, a `map[string]any`.
   - `sharing/publicshare.go:215-222`: a third `map[string]any` projection.
7. Device exists in five places (about 345 lines): `playback/device.go`, `store/device.go`, `store/devices.go`, `sharing/sharedevice.go` and `deviceinfo`. It has three DTO shapes, one of them a map.
8. `lists/playlist_http.go:69-554` (about 480 lines) decodes request bodies into `map[string]any` plus `jsonvalue.Truthy`. Reorder loops are duplicated five times: `playlist_http.go:271-290,524-537`, `collection_http.go:152-160,303-318` and `store/playlists.go:93-98`. `collection_http.go:183-323` is the 140-line batch handler.
9. List DTOs are `map[string]any` (`reader.go`, `playlistview/view.go`), so sorting reads `jsonvalue.String(item["name"])`.
10. `playback/history.go:21-106`: dynamic `ORDER BY` with manual `rows.Scan`, and an N+1 device and user lookup per row.
11. `playback.go:518-565` and `localsync.go:136-201`: progress payloads go through `Value()` (marshal then re-unmarshal into `Optional`).
12. `sharing/publicshare.go:51-212`: a 160-line handler that mixes cookies, session creation, raw SQL and `remarshal`.

**6. CLIENT DEPENDENCE**
- The web client sends only these progress and session payloads:
  - `{isFinished: bool}`, single or batch with `libraryItemId`.
  - Readers send `{ebookLocation, ebookProgress}` (PDF: a number; others unchecked).
  - Session sync/close sends `{timeListened: int, currentTime: number}`.
  - Share progress sends `{currentTime}`.
- It never sends string or null numerics, so the JS `Number()` coercion is not needed for the web.
- Absent versus null must still be distinguished for `duration`, `ebookProgress` and `finishedAt`. Absent means untouched, null clears.
- Mobile app payload types (strings, null) are unknown; the app is not in this repo. Tests such as `TestLocalAllToleratesNonObjectEntriesAndStringTimestamps` suggest an observed need, but I could not confirm it.
- Web events consumed: `user_item_progress_updated`, `user_stream_update`, `user_session_closed`, `stream_reset`, `share_open`/`share_closed`, `rss_feed_open`/`rss_feed_closed`, `playlist_*`, `collection_*`.
- Podcast-app dependence on RSS quirks (self-closing empty elements, the `"false"` MIME fallback) is unknown. I found nothing in the client.

**7. GREENFIELD PROPOSAL**
- `progress`: typed `Update` with `*float64`/`*bool` and a tolerant decoder, plus `Store.Apply`. About 250 lines against 620.
- `session`: one `Session` struct serving as in-memory state, response and DB row. A `Manager` owns the map and mutex. Files `lifecycle.go`, `local.go`, `history.go`, `direct.go`. About 650 against 1,000.
- `session/hls`: an ffmpeg `Stream` type with `Seek` and `Close` methods. About 220.
- `device`: one `Info` type, one upsert, and the UA parser replaced by `mileusna/useragent` (verify its output against the narrow parser). About 120 against 345.
- `feeds`: one `Source` builder, typed episode generation, and RSS through stdlib `encoding/xml` with `,cdata` structs, which gives correct escaping. `eduncan911/podcast` is an alternative. About 700 against 1,300 (including the existing `feeds` package).
- `shares`: a `Manager` using `time.AfterFunc`. About 250.
- `playlists` and `collections`: typed request and response structs and one shared `reorder` helper. About 800 against 1,485.
- sqlc config with `emit_pointers_for_null_types` removes most `sql.Null*` plumbing (about 300 lines) and `store.ScanPlaybackSession`.
- `google/uuid` (or a stdlib equivalent) replaces `ids`. Hand-rolling the HLS playlist writer and keeping the exec-based ffmpeg are fine.
- Total: about 6,100 down to roughly 3,000–3,400 lines. Tests should be rewritten as black-box mux tests. The 255-line progress suite is the model.

**8. HAZARDS**
- Progress merge rules:
  - `isFinished` true→false resets `currentTime=0`, `progress=0`, `finishedAt=nil`.
  - Auto-finish uses the library's `markAsFinishedPercentComplete` when it is greater than 0, else `markAsFinishedTimeRemaining` (default 10s).
  - `currentTime` changing clears `hide` unless the payload sets `hide`.
  - `extraData` holds `libraryItemId` and `progress`.
  - `updatedAt` is only bumped when something changed.
  - `lastUpdate` overrides `updatedAt` after save.
  - Local sync skips progress only when the stored `updatedAt` is strictly newer than the session's.
- Session lifecycle:
  - Sessions are in memory and persisted only when `timeListening > 0` (on sync or close), or on any local upload.
  - Close with no sync data saves only listened sessions.
  - The upsert conflict updates only `currentTime`, `timeListening`, `updatedAt`, `date` and `dayOfWeek`, so `deviceId` and metadata stay as first written.
  - `serverVersion` is hard-coded to 2.37.2.
  - `play_local_*` ids map to server uuids through an in-memory map, as in TS. A deterministic uuid v5 would survive restarts.
  - A local session overwrites `duration` with the item's duration, and new local sessions keep the client's `date` and `dayOfWeek`.
- Events:
  - Start sends `user_stream_update` to admins with `libraryItem` null.
  - Sync sends `user_item_progress_updated` to the user, carrying `sessionId` and `deviceDescription`.
  - Close sends `user_session_closed` to the user.
  - `stream_reset` goes to all clients.
  - `share_open`/`share_closed` go to admins.
  - Playlist events go to the owner. Collection events go to everyone.
  - `rss_feed_*` events go to everyone.
- HLS:
  - Segments are 6s with a pre-generated full VOD playlist. ffmpeg starts at `start-30s`.
  - A missing segment resets the transcode when it is before `segmentStart`, or more than 10 segments beyond the furthest generated one.
  - A reset emits `stream_reset{startTime, streamId}` and wipes the segments but keeps the playlist.
  - Podcast track index 0 falls back to the first file.
- Shares:
  - Expiry is lazy in Go: there are no timers, whereas TS removes expired shares at startup and on a timer. Expired shares are removed, with `share_closed` emitted, only on access.
  - An already-expired share is created (201), emitted, then deleted.
  - The `share_session_id` cookie is `HttpOnly` with a 30-day max-age.
  - Dates are ISO strings, not epoch milliseconds.
  - Only books are playable through a share.
- Feeds:
  - Slugs are checked for uniqueness. Feed URL is `/feed/{slug}`.
  - Enclosure URL is `/feed/{slug}/item/{epId}/media{ext}`, with cover at `/feed/{slug}/cover{ext}?ts=`.
  - The XML namespaces and tags (`itunes:*`, `googleplay:block`, `podcast`, `dc:creator`) must be preserved.
  - Staleness refresh keeps episode ids by file path.
  - Dates are `toGMTString`; duration is rounded.
- Session and sync endpoints are not thread-safe: `handleSync` mutates a `*session` without a lock.
