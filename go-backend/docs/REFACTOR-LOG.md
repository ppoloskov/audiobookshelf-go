# Refactor decision log

Goal: pieces that change together live together; everything depends on what changes less. Must not change: SQLite schema and stored values, HTTP/Socket.IO client contracts (JSON by value, ADR-002), TS v2.37.2 rollback.
Paths are relative to `go-backend/`. Dead/wrapper/unused-param claims were verified by repo-wide grep (incl. tests); symbols were re-located (lines corrected where they drifted); copy claims were checked at both sides for the headline cases, remaining line numbers come from the surveys and may drift by a few lines.
Read a fix section as the list of problems it removes; `Special cases` marks each special case with its fix or why it stays.

## Fixes

### F1 — One outbound-HTTP policy: httputil.SafeClient + one DownloadImage
- `internal/catalog/cover.go:284` and `internal/catalog/authors.go:722` — two image downloaders (same transport/UA/30s/svg check) already drifted: redirect cap `>=5` vs `>5`, EXP_PROXY_SUPPORT only in cover (copy)
- `internal/catalog/cover.go:244-282` — private-address rule (`publicUnicastAddress`, `coverURLTransport`) lives in catalog, not httputil (layout)
- `internal/podcasts/podcasts.go:1054` and `:1100` — two more private-IP lists (dial hook + `rejectPrivateHost`), missing CGNAT/0.0.0.0/8 that cover.go blocks (copy)
- `internal/podcasts/downloads.go:219` — SSRF pre-check plus dial check plus CheckRedirect for one request (copy)
- `internal/catalog/cover.go:291` — `os.Getenv("EXP_PROXY_SUPPORT")` inside download func; also read in `internal/httputil/ssrf.go:13` (global)
- `internal/catalog/cover.go:292`, `internal/catalog/authors.go:728` — SSRF exemption decided for first host, redirects inherit it (copy)
- `internal/httputil/ssrf.go:13-19` — three env vars read on every outbound request (global)
- `internal/podcasts/podcasts.go:1122` — `SetHTTPClientForTests` sets `allowPrivateNetwork`; production flag only tests set (speculative)
- `internal/catalog/cover.go:322-326`, `internal/catalog/authors.go:738` — failed copy leaves partial file (swallowed-error)
- `internal/catalog/cover.go:260-282` — resolver checks/dials only `ips[0]` (swallowed-error)
- `internal/catalog/quickmatch.go:192` — third caller of downloader; temp-path pattern repeated (param-one-caller)
- `internal/catalog/cover.go:173` — `supportedCoverTypes` string duplicates keys of `supportedImageTypes` at `:171` (copy)

### F2 — One owner of ffmpeg/ffprobe (internal/mediatools)
- `internal/scanner/files.go:676`, `internal/catalog/batch_chapters.go:327`, `internal/podcasts/downloads.go:308`, `internal/audiotools/tools.go:666`, `internal/playback/playback.go:1512` — FFPROBE_PATH/FFMPEG_PATH read via `os.Getenv` at call time in five places (global)
- `internal/scanner/files.go:1125` — `audioTagsFromFile` hardcodes `"ffprobe"` (ignores FFPROBE_PATH), uses `context.Background()`, swallows failure (copy/swallowed-error)
- `internal/scanner/files.go:1125`, `internal/scanner/import.go:246` — second ffprobe spawn on `g.files[0]` for tags `probeAudio` already read (copy)
- `internal/catalog/cover.go:147` — hardcodes `"ffmpeg"`, ignores FFMPEG_PATH; `cmd.Stdout/Stderr = nil` at `:148-149` is a no-op (copy/dead)
- `internal/podcasts/downloads.go:296-345`, `internal/scanner/files.go:675-805`, `internal/catalog/batch_chapters.go:331` — four ffprobe invocations/result structs; podcast and scanner build `audioFile` JSON separately (copy)
- `internal/audiotools/tools.go:498`, `:573` — ffmpeg stderr discarded/uncaptured; admin sees only `exit status 1` (swallowed-error)
- `internal/catalog/batch_chapters.go:334` — ffprobe failure returned as bare 500 without stderr (swallowed-error)
- `internal/audiotools/tools.go:62` — `task.cmd` written at `:499`,`:574`, never read (dead)

### F3 — serversettings is the only reader/writer of the server-settings row
- `internal/server/manager.go:84-98`, `internal/adminops/handler.go:121-148`, `internal/store/store.go:202`, `internal/identity/identity.go:165,810`, `internal/identity/oidc.go:345,388,907`, `internal/scanner/scanner.go:681`, `internal/scanner/eventshape.go:34`, `internal/scanner/import.go:395`, `internal/scanner/metadata.go:186`, `internal/bootstrap/bootstrap.go:188` — ~12 raw `SELECT/UPDATE settings WHERE key='server-settings'`; writers must remember `Invalidate` (copy)
- `internal/serversettings/serversettings.go:34`, `:89` — process-global cache keyed by `*sql.DB`; correctness depends on writers calling `Invalidate` and closers calling `Forget` (`internal/server/manager.go:307` `closeStore`) (global/convention)
- `internal/catalog/handler.go:995,1238` and `catalog/handler.go:143` — `Load(...).Doc()["sortingIgnorePrefix"]` map-key reads and per-call reload (internals)
- `internal/catalog/writes.go:120`, `quickmatch.go:242`, `authors.go:238,307`, `collapse_subseries.go:291`, `personalized.go:147` — each re-reads prefixes before BeginTx (convention)
- `internal/serversettings/serversettings.go:46` — `Bool` re-decodes the whole doc each call; `Doc()` clones (shallow/speculative)
- `internal/serversettings/serversettings.go:81-86`, `internal/identity/oidc.go:906-918` — read error returns defaults / disables login method silently (swallowed-error)
- `internal/serversettings/serversettings.go:13` — imports `libitem` only for `SortingPrefixes` (direction; also F4)
- `internal/server/runtime.go:202-210`, `:227-235` — `backup.Options` literal built twice with `stringSetting/intSetting` defaults (`:387-416`) (copy)
- `internal/backup/backup.go:78-80,121-124` — default `BackupsToKeep=2` and backup dir repeated in `New` and `UpdateOptions` (copy)
- `internal/server/runtime.go:73-78`, `cmd/audiobookshelf-go/main.go:69-73` — settings loaded and `ApplyLoggingSettings` run twice on startup (copy)
- `internal/identity/identity.go:713-757`, `internal/bootstrap/bootstrap.go:188` — default settings merge outside serversettings (copy)
- `internal/scanner/scanner.go:678-694` — default prefixes `the,a` and `sortingPrefixes` parse copied from `libitem.SortingPrefixes` (copy)
- `internal/identity/identity.go:119-180`, `:740,746,749` — identity reads BACKUP_PATH/ALLOW_IFRAME env itself; also `internal/server/runtime.go:470`, `internal/adminops/handler.go:173`, `internal/bootstrap/bootstrap.go:189-195` (global/copy)
- `internal/emailapi/handler.go:60-85`, `internal/notifications/handler.go:53-79`, `internal/userapi/me_extra.go:203-253`, `internal/identity/identity.go:708,786` — settings-row load/save and default email document copied (copy; the F3 settings-row helper covers email/notification rows)

### F4 — Text/name leaf (textsort): TitleIgnorePrefix, PrefixAtEnd, SortingPrefixes; nameparse.ToLastFirst everywhere
- `internal/itemwrite/relations.go:81`, `internal/libitem/json.go:163`, `internal/scanner/files.go:156`, `internal/adminops/handler.go:366` — four `TitleIgnorePrefix`-like functions, two opposite meanings (column vs display); itemwrite one slices by byte length of lower-cased string (copy)
- `internal/itemwrite/relations.go:244` calls libitem's while `:123` uses the local one in the same chain (copy)
- `internal/scanner/files.go:1170` — `lastFirst` simplified copy of `nameparse.ToLastFirst` (`nameparse/nameparse.go:316`); stored `authorNamesLastFirst` can differ from `authors.lastFirst` (copy; must keep stored value where upstream rule differs: check by test before switching)
- `internal/itemwrite/relations.go:17-24` — itemwrite imports libitem only for one call (direction)
- `internal/serversettings/serversettings.go:13`, `internal/libitem/json.go:145` — settings parser lives in libitem (direction)
- `internal/catalog/handler.go:147-156` — `titlePrefix`, `seriesPrefixFreeName` wrappers (wrapper)
- `internal/catalog/handler.go:163` — `lastFirst` wrapper, only test callers (`handler_test.go:483,674`) (dead)
- `internal/catalog/handler.go:756` `naturalLess` (just lower-cased compare, not natural), `internal/catalog/personalized_books.go:352` `naturalCompare` — comparators beside `textsort` (copy)
- `internal/itemwrite/relations.go:96` — `kind.prefixes` exists only for one event field (vocabulary)

### F5 — jsonvalue owns JS-semantics coercions
- `internal/catalog/authors.go:108` `jsTruthy`, `internal/catalog/batch_chapters.go:503` `truthy` (bool-only), `internal/catalog/quickmatch.go:43` `jsTruthyRaw`, `internal/identity/oidc.go:863`, `internal/realtime/hub.go:714`, `internal/emailapi/handler.go:131`, `internal/notifications/handler.go:169`, `internal/progressdto/apply.go:113` — copies of `jsonvalue.Truthy` with different semantics (copy)
- `internal/catalog/authors.go:849` `jsString`, `internal/catalog/quickmatch.go:52` `matchString`, `internal/identity/oidc.go:858` `getString` — copies of `jsonvalue.String` (copy)
- `internal/identity/identity.go:1050,1059,1065` `decodeMap/decodeArray/stringSlice`, `internal/userapi/userapi.go:1278` `jsonArray`, `internal/userapi/me_extra.go:79`, `internal/catalog/handler.go:113-121` `decodeObject/decodeArray/decodeStrings` (one-line wrappers over `store.DecodeJSON*`, 30+ callers) (copy/wrapper)
- `internal/userapi/userapi.go:1288,1302,1308`, `internal/userapi/listening.go:146`, `internal/catalog/batch_chapters.go:515`, `internal/progressdto/apply.go:53`, `internal/progressdto/progress.go:23` — numeric coercion repeated (copy)
- `internal/emailapi/handler.go:426` `anyArray`, `internal/notifications/handler.go:455` `array`, `internal/adminops/handler.go:828` `containsAny` — "any as []any" helpers (copy)
- `internal/catalog/quickmatch.go:163-178,439-449`, `internal/catalog/mutation.go:84-91` — overlapping `stored*` value readers (copy)
- `internal/jsonvalue/value.go:16` — `String` is `v.(string)`; one user (wrapper; keep if F5 routes all callers to it)
- `internal/progressdto/apply.go:45-52` — `Value` swallows marshal error (swallowed-error)

### F6 — Typed events: events.Emitter {ToUser, ToAdmins, ToAll}, no-op default; one Task type + builder
- `internal/realtime/hub.go:440-478` — `Emit(userID,...)` overloaded with `"*"` and `"@admins"`; callers `podcasts/podcasts.go:127`, `lists/collection_http.go:89`, `sharing/feeds.go:299`, `playback/playback.go:326`, `providers/handler.go:226` (vocabulary)
- `internal/lists/*.go` (11 sites), `internal/sharing/feeds.go` (2), `internal/sharing/shares.go` (3), `internal/providers/handler.go` (2), `internal/adminops/handler.go:467,513,548`, `internal/notifications/handler.go` — `if h.emit != nil` guards (speculative)
- `internal/podcasts/podcasts.go:65-67`, `internal/playback/playback.go:209-211` — no-op default emit done per package (copy)
- `internal/adminops/handler.go:34`, `internal/emailapi/handler.go:35`, `internal/notifications/handler.go:26`, `internal/playback/playback.go:208`, `internal/sharing/handler.go:55`, `internal/userapi/userapi.go:63`, `internal/audiotools/tools.go:85`, `internal/catalog/handler.go:182` (`emitFunc`, used only for the field) — inline `func(userID,event string,payload any)` beside `events.Emitter` (`internal/events/events.go:9`) (copy)
- `internal/podcasts/downloads.go:160,195`, `podcasts/podcasts.go:772,829`, `catalog/match_task.go:22`, `audiotools/tools.go:40-65,372,450`, `scanner/scanner.go:259`, `scanner/watcher.go:163-182`, `wire/match.go:85-102` — five Task definitions / seven `task_started/finished` literals (copy)
- `internal/audiotools/tools.go:416-424` — cancel detected by comparing error string `"Task canceled by user"`; every failure stored with `MessageTaskCanceledByUser` (convention)
- `internal/audiotools/tools.go:290-328` vs `:277-310` — `batch` and `embed` copy the task/queue/emit block; batch omits `queuedFlag = true` (copy); `queuedFlag` (`:64`) is never read anyway (dead)
- `internal/audiotools/tools.go:62-65` — `userID` field set at `:364`, never read (dead)
- `internal/audiotools/tools.go:358` — `newTask` 10 params, `queued` selects registry (param-one-caller)
- `internal/catalog/match_task.go:14-18` `background(fn)` — `go fn(ctx)` wrapper (wrapper); `finishTask`/`startTask` one caller each (param-one-caller)
- `internal/podcasts/podcasts.go:822` `emitFailedOPMLFeed` five strings (param-one-caller)
- `internal/scanner/watcher.go:253-269` vs `internal/audiotools/tools.go:177-185,419-439` — separate snapshot/finish helpers (copy)
- `internal/realtime/hub.go:76` — `Hub.cfg` stored, never read after `New` (`New` reads `cfg.RouterBasePath` at `:110`) (dead)

### F7 — Users own their shape: one user DTO builder, one device upsert, access.Defaults(type) + permission encode/patch, one UA parser
- `internal/userapi/userapi.go:1251` `browserUser`, `:1169` `minimalBrowserUser`, `internal/identity/identity.go:274` `UserBrowser` — three user DTO builders, already differ on hasOpenIDLink/lastSeen (copy)
- `internal/identity/identity.go:274` — `hideRootToken,minimal` always false,false from both callers (`identity.go:679`, `oidc.go:569`); minimal branch unreachable (speculative/param-one-caller)
- `internal/realtime/hub.go:591-597`, `internal/identity/identity.go:295-302`, `internal/userapi/userapi.go:1190-1197`, `internal/playback/playback.go:1006` `publicUser` — lastSeen/createdAt normalisation and the public-user shape copied (copy)
- `internal/userapi/userapi.go:1131`, `internal/identity/identity.go:683-691` — DTO builder re-queries email to recover NULL vs "" (convention)
- `internal/userapi/userapi.go:1119` `BrowserUser` — no caller anywhere (dead); `:1113` `ProgressRows` only `userapi_test.go:288` plus wrapper `progressRows` `:1108` (wrapper/dead)
- `internal/identity/identity.go:549`, `internal/identity/oidc.go:671`, `internal/userapi/userapi.go:1200` — three default-permissions literals; key lists `{"librariesAccessible","itemTagsSelected"}` repeated at `userapi.go:478,742,748`, `oidc.go:731`, `access/access.go:27` (copy)
- `internal/identity/identity.go:550`, `internal/identity/oidc.go:672` — initial `extraData` literal copies (copy)
- `internal/access/access.go:114-117` — four identical `can(flag)` methods (if-one-value)
- `internal/access/access.go:120` `AdminUser`, `:51` `IsAdmin`, `internal/identity/identity.go:795` inline check, `internal/backup/backup.go:166-190` local admin check, `internal/scanner/scanner.go:351` `adminOnly` — admin predicate spelled several ways (copy)
- `internal/catalog/library_writes.go:30` `adminUser` adds `IsActive`, hidden difference from `access.AdminUser` (copy)
- `internal/userapi/sessions.go:85-131` `parseUA/uaVersion` vs `internal/deviceinfo/ua.go:7` `ParseUserAgent` — two UA parsers with different rules, userapi one untested (copy)
- `internal/sharing/sharedevice.go:25-65` vs `internal/playback/playback.go:1371-1434` — two `devices` upserts (copy)
- `internal/sharing/sharedevice.go:70-78`, `internal/identity/identity.go:985`, `internal/playback/playback.go:1411` — three client-IP extractions with different XFF handling (copy)
- `internal/identity/identity.go:785-806` `filterDevices`, `internal/userapi/me_extra.go:228,258`, `internal/emailapi/handler.go:415,449` — ereader availability rule three times (copy)
- `internal/userapi/userapi.go:997-1035` date parser beside `dbtime.Parse`; `internal/identity/identity.go:87-117` `nullableDate.Scan` (copy)
- `internal/userapi/userapi.go:1145` `scanUser` and hand 14-column SELECT vs sqlc `GetUserByID` vs `store/store.go` row mapping (copy)
- `internal/realtime/hub.go:351` — Hub writes `users.lastSeen` itself (internals)
- `internal/progressdto/apply.go` vs `internal/userapi/userapi.go:312-326` — double target resolution; library check only in one of the two callers (copy/convention)
- `internal/progressdto/progress.go` returns `[]any` of maps while `Apply` returns `wire.MediaProgress` (layout)
- `internal/userapi/userapi.go:277-281` — `batchUpdateProgress` drops every error (swallowed-error; upstream behaviour, keep behaviour, document)
- `internal/userapi/listening.go:158` `minInt` — builtin `min` available (go 1.27) (wrapper)
- `internal/userapi/userapi.go:33` `queries()` — no caller in userapi (dead; identity/lists/catalog copies of `queries()` are used)

### F8 — DTOs live with their single producer; store and logging stop importing wire
- `internal/store/device.go:9`, `internal/store/playbacksession.go:10`, `internal/logging` (`wire.LogEntry`) — storage and logging import the DTO package (direction)
- `internal/wire/log.go`, `wire/device.go`, `wire/match.go`, `wire/podcast.go`, `wire/shelves.go` — one-user DTOs grouped by TS origin (layout)
- `internal/wire/shelves.go:37` `AuthorShelfEntry` duplicates `wire.AuthorExpanded` with pointer timestamps (copy)
- `internal/wire/shelves.go:64` `MediaItemShareForClient` vs `wire/sharing.go:13` `MediaItemShare` — two DTOs of one row, converted by hand at `catalog/item_includes.go:63-95` (copy)
- `internal/wire/book.go:5,12` `BookSeries`/`BookSeriesRef`, `libitem.SeriesRef` — three series refs (copy)
- `internal/wire/shelves.go:9,29,32,98` — `any` holes inside typed DTOs (internals)
- `internal/wire/shelves.go:111,125` `LibraryCollectionsPage/LibraryPlaylistsPage` belong to `lists` (layout)
- `internal/wire/shelves.go:93` `CollapsedSubseriesPage` is the typed twin of the map built at `internal/catalog/handler.go:1318` (copy)
- `internal/store/playbacksession.go:63-82` `millisOf` copies `dbtime.Millis`; `:106` `nullFloat` copied at `internal/lists/collection_http.go:482` (copy)
- `internal/store/playbacksession.go:12-61` — `PlaybackSessionColumns` order must mirror sqlc model (shallow)
- `internal/providers/book_finder.go:81-100` — `convertJSON` round trip typed -> JSON -> `wire.BookMatch` (internals)
- `internal/playback/playback.go:751-756` — `OpenSessions` JSON round trip to `[]map[string]any` (internals)

### F9 — One request prologue for /api/libraries/:id/* and one listParams
- Library lookup + access + mediaType prologue pasted in `internal/catalog/handler.go:255,376,434,765,914,1219,1716`, `personalized.go:76`, `library_issues.go:17`, `library_extras.go:17,63,152`, `cover.go:355`, `download.go:68`, `batch_chapters.go:224,297`; intended owner `library_routes.go:36 libraryAccess` used by only 3 (copy/convention)
- `internal/lists/library_http.go:26-57`, `internal/podcasts/library.go:22-79` — `LibraryController.middleware` copied (403/404 and limit/page loop) (copy)
- `internal/lists/library_http.go:31-33` — `COUNT(*)` as existence check where siblings handle `sql.ErrNoRows` (swallowed-error)
- `internal/catalog/handler.go:97` `pagination`, `library_routes.go:52-63`, `handler.go:897` `authorPagination`, `personalized.go:88-97`, `handler.go:1723-1728`, `podcasts/library.go:59` — six limit/page parsers with different error rules (copy)
- `internal/catalog/series_features.go:15,24`, `catalog/handler.go:1369`, `catalog/series_routes.go:121-127`, `catalog/authors.go:251` (`sliceHas` = `slices.Contains`) — include= parsers with unmarked differences (copy)
- Library settings read five ways: `catalog/handler.go:920-931`, `collapse_subseries.go:235`, `personalized_books.go:27`, `library_routes.go:44`, `library_writes.go:197`; also `playback/playback.go:580-590`, `sharing/publicshare.go:182-188`, `progressdto/apply.go:175` (copy)
- `internal/catalog/query.go:194` `orderSQL(sortBy, filter, desc, ignorePrefix, podcast, user)` and `collapse_series.go:32` seven-param `collapseSeries`; `collapse_subseries.go:124,150` thread `ignorePrefix` (vocabulary/param-one-caller)
- `internal/catalog/query.go:151-181`, `collapse_series.go:42-48`, `handler.go:1264-1297` — clause/alias/arg-order contract between `filterSQL` and callers (convention)
- `internal/catalog/handler.go:93` `itemMaps(ctx, ids, _ string)` — ignored param, wrapper over `expandedItems` (wrapper/vocabulary; callers `batch_chapters.go:108`, `authors.go:196`)
- `internal/catalog/handler.go:1217-1224`, `:1721-1727` — books/podcasts table switch with 501 for others (if-one-value; also F10 media-table scope)
- `internal/catalog/library_writes.go:34-39` `defaultLibrarySettings(mediaType)` and create/update validators `:62-107` vs `:244-295` (copy; folder-path normalisation `:124-131` vs `:327-334`)
- `internal/catalog/library_writes.go:673-720` `libraryJSON`, `catalog/handler.go:273`, `:375` — three library serializers; lastScan==0 -> null only in two; folder list ORDER BY differs vs `handler.go:1196` (copy)
- `internal/catalog/handler.go:1196` `libraryFolders` (no ORDER BY) vs `library_writes.go:681-700`; `pathAllowed` `:1588` treats failed query as "no roots" (copy/swallowed-error)
- `internal/catalog/handler.go:532-596` `genreCounts/longestItems/largestItems` take SQL fragments, allow-list repeated (vocabulary)
- `internal/catalog/library_extras.go:79-84`, `query.go:238`, `collapse_subseries.go:89` — three base64 filter-value decoders (copy)
- `internal/catalog/personalized_books.go:36` `sixtyDaysAgo` vs `catalog/query.go:132` `$recent` (copy)

### F10 — Catalog entity views and mutation finish
- Series serialised five ways: `internal/catalog/handler.go:1055`, `:1155`, `:2005`, `series_routes.go:38`, `personalized_books.go:262-270`; each with own `nameIgnorePrefix` rule (copy)
- Authors serialised by hand at `internal/catalog/handler.go:877`, `:1936`, `:700`, `personalized_books.go:419-432` beside `wire.Author` (`authors.go:66-100`); `internal/itemwrite/relations.go:97-127,289` builds author payload map for events (copy)
- Three series-books queries with three orderings: `catalog/handler.go:1084`, `series_features.go:101`, `series_routes.go:60`, `personalized_books.go:139,297` (copy)
- Series progress computed twice: `catalog/handler.go:1170-1183` (error dropped via `_ =` at `:1177`) vs `series_routes.go:128-143` (copy/swallowed-error)
- rssFeed attach in four places; `feedMinified` (`series_features.go:35`) map vs `wire.FeedMinified` (copy)
- `internal/catalog/batch_chapters.go:41-107` `batchGet` re-implements `itemsExpandedWhere` (`batch.go:32-58`) and `userCanAccessItems` (`batch.go:63-84`) (copy)
- `internal/catalog/writes.go:64-72` vs `batch.go:246-256` — cron check and metadata parse in PATCH media and batchUpdate; apply+announce+ItemChanged pipeline duplicated (`writes.go:83-90` vs `batch.go:258-269`) (copy)
- Sweep/announce order differs: `writes.go:80-90`, `batch.go:168-169,263-273`, `quickmatch.go:355-356`, `itemwrite/sweep.go:41-44` (convention)
- `internal/catalog/batch_chapters.go:194-200` — calls `h.itemChanged` directly, bypassing `ItemChanged` (`mutation.go:24-46`) (convention)
- `internal/catalog/mutation.go:44-47` and ~10 sites (`writes.go:517,628,732`, `cover.go:457`, `batch_chapters.go:200,409,495`, `quickmatch.go:362`) — `ItemJSON` error dropped, no `item_updated` (swallowed-error); `ItemJSON(ctx,id,expanded bool)` returns `any` for two types (param-one-caller)
- `internal/catalog/handler.go:1454` `ItemMinifiedJSON` — only tests and `contracttest/libitem_equivalence_test.go:122` call it (dead in production)
- Cover pipeline in three places: `catalog/writes.go:561-623` `updateCover`, `catalog/cover.go:342-471` `uploadCover`, `catalog/quickmatch.go:183-204` `saveCoverFromURL`; `writes.go:515-520` `removeCover` shares the tail; `removeCover/updateCover` sit in writes.go apart from cover.go (copy/layout)
- Metadata-tree paths derived independently: `cover.go:33-36`, `authors.go:650-652`, `quickmatch.go:184`, `writes.go:~498,597` (global/copy)
- `mediaType -> books/podcasts` switch 8 times: `writes.go:104-106,504-507,565-568`, `cover.go:435-438,501`, `quickmatch.go:100-103,244,296`, `batch.go:69`, `batch_chapters.go:95` (copy)
- `internal/catalog/writes.go:28-38` `writableItem` three-value return, callers use only `allowed` (shallow)
- `internal/catalog/writes.go:239` `removeLibraryItem` nil-user mode only for `batch.go:164`; returns HTTP status from non-HTTP func (param-one-caller/vocabulary)
- `internal/catalog/library_writes.go:482-560` re-implements per-item cascade next to `writes.go:239`; `:604` `sqlMarks` dup of `batch_chapters.go:508 placeholders`; `:605` `stringArgs` vs `store.Texts` (copy)
- `internal/catalog/library_writes.go:343-355` `status, _ := h.deleteLibraryItem(...)` drops message (swallowed-error)
- `internal/catalog/writes.go:~293,~327-338`, `quickmatch.go:620,634`, `library_writes.go:333-346,375-383,591` — `rows.Scan(...) == nil` skips rows (swallowed-error)
- `internal/catalog/authors.go:486` — `h.authorMerge(...)` error discarded then `return nil` (swallowed-error: DB failure -> empty 200)
- `internal/catalog/authors.go:551-627`, `:655-685`, `itemwrite/sweep.go:55-58` — author removal event + filter pair in three places (copy)
- `internal/catalog/writes.go:~514,~625` — `_, _ = UPDATE libraryItems SET updatedAt` ignored (swallowed-error)
- `internal/itemwrite/relations.go:260-262` `authorBookCount` scan error -> 0, can sweep an author that still has books; `:239` `ms, _ := dbtime.OptionalMillis(now)` then `*ms` (swallowed-error)
- `internal/itemwrite/relations.go:28-34` `Rules` flags each used by one caller; `:92-98` `kind.author bool`; `:153` `seqSel` (vocabulary/if-one-value)
- `internal/itemwrite/relations.go:73-77`, `scanner/import.go:367-370`, `catalog/writes.go:84-86` — callers read `Changes.*` slices directly (internals)
- `internal/catalog/mutation.go:120-130` `columnSet.text` hardcodes `column == "description"`; `writes.go:117-170`, `quickmatch.go:320-328` write into `columns.set` directly; columnSet methods split across `mutation.go` and `quickmatch.go` (vocabulary/internals/layout)
- `internal/catalog/quickmatch.go:476-506` `matchOptionsFromBody(body, keys ...string)`; `:35-40` `quickMatchOptions` `*bool` tri-state collapsed at `:225` (vocabulary/speculative)
- `internal/catalog/quickmatch.go:432-437` `orEmpty` (only because `matchList` can return nil) (dead-ish)
- `internal/catalog/mutation.go:20-22` `env()` builds `itemwrite.Env` per call (wrapper); `itemwrite/sweep.go:43-46` `Env.Sweep` while callers still call individual sweeps (wrapper)
- `internal/catalog/writes.go:489` `dbNow()` wraps `dbtime.Now()` (12 uses; `batch_chapters.go:176` calls dbtime directly); `internal/providers/handler.go:307`, `internal/adminops/handler.go:616` same (wrapper)
- `internal/catalog/handler.go:122,128,129,135,1417` — `deref`, `stringValue` single-purpose; `nonEmpty` (`:129`), `valueOrNull` (`:135`), `stringValues` (`:1417`) have no callers (providers/sharing `nonEmpty` are their own) (dead)
- `internal/catalog/handler.go:164` `number` — only `handler_test.go:611,615` (dead in production)
- `internal/catalog/handler.go:1008-1018` — sort cases `lastBookUpdated` (`return false`) and `random` (`return i < j`) are no-ops/non-shuffle; `lastBookUpdated` equals default (dead; `random` returns client-visible order, keep stable order)
- `internal/catalog/handler.go:1368` `expandedItemIncludes` one caller, `mediaID` derivable (param-one-caller); `:1588` `pathAllowed(..., metadataOK)` true from one caller (param-one-caller)
- `internal/catalog/download.go:50-51` re-assign zero value; `:140-144` `_ = walkErr` no-op (dead)
- `internal/catalog/download.go:104-139` vs `library_routes.go:299-343` — two zip walkers (copy)
- `internal/catalog/download.go:23` `audioMIMEType` vs `handler.go:1645` table vs `playback/playback.go:1460`; `mediaFile` header overwritten by `servePath` (copy; see F12)
- `internal/catalog/handler.go:234-242` `resetIssues` vs `scanner/scanner.go:237` `resetIssuesFilterData` (copy; see F11)
- `internal/catalog/handler.go:187` `queries()` wraps `sqlc.New` (wrapper)
- `internal/catalog/collapse_subseries.go:283-290`, `personalized.go:36-39,214` — reach into libitem internals (internals)
- `internal/catalog/library_extras.go:131-134` — `err == nil` swallows load error before `items_updated` (swallowed-error)
- `internal/catalog/library_routes.go:155`, `:172-180` — bad `libraryFiles` JSON ignored; `RemoveAll` error only logged (swallowed-error)
- `internal/catalog/handler.go:1004-1017`, `:1188` — `dateSortKey` ignores `OptionalMillis` error; `personalized_books.go:253-255,435-436` same (swallowed-error)

### F11 — filterdata builds and patches its own data
- `internal/catalog/handler.go:613-748` `buildFilterData` builds a map mirroring `filterdata.Data` tags, then `filterdata.FromMap` JSON round trip (`filterdata/filterdata.go:72`); defaults literal `:615` copies `normalize` (`:44`) (copy)
- `internal/catalog/handler.go:599-607` — miss returns `built`, hit returns `cached.Map()`: first and later responses differ in shape; `FromMap` error dropped (copy/swallowed-error)
- `internal/catalog/handler.go:722-748` sorted sets vs `filterdata.appendUnique` (`:158`) append order (copy)
- `internal/filterdata/filterdata.go:95-101` `Provider` (one implementer, one consumer); `catalog/handler.go:223` `FilterCache()` and `:227` `EnsureFilterData` getters; scanner stores both `filterLoader` and `filters` (`scanner/scanner.go:52-53,63`) (speculative/direction/wrapper)
- `internal/filterdata/filterdata.go:167-230` — ten per-field mutators with single callers (`scanner/import.go:404-410`, `itemwrite/sweep.go`) (vocabulary)
- `internal/filterdata/filterdata.go:123` `Has`; `catalog/handler.go:235` and `scanner/scanner.go:238` guards duplicate `Cache.with` nil handling (dead/speculative)
- `internal/scanner/scanner.go:236-247` — count-query error ignored; `:230-235` `EnsureFilterData` error dropped (swallowed-error)
- `internal/scanner/scanner.go:230`, `watcher.go:438` — "EnsureFilterData before add" convention; `ScanItemForce` (`scanner.go:75`) does not (convention)
- `internal/server/runtime.go:139` — `FilterCache().Invalidate("")` from composition root (internals)
- `internal/catalog/library_issues.go:60` `StoreNumIssues(libraryID,0)` without the guard (convention)

### F12 — One file-kind table (ext -> kind, mime)
- `internal/catalog/handler.go:1645`, `catalog/download.go:23`, `playback/playback.go:1460-1484`, `sharing/handler.go:118-127`, `sharing/feeds.go:645-662`, `podcasts/downloads.go:549-563`, `scanner/files.go:812-838`, `audiotools/tools.go:612-664` — audio ext/mime tables with different membership and defaults (copy)
- `internal/scanner/watcher.go:360-366` `isScannableNonMedia` == `scanner/files.go:174-180` `scannableCompanion` (copy)
- `internal/scanner/watcher.go:350-358` `isMediaFileExt`, `scanner/walk.go:72-74` `mediaExt`, `watcher.go:674-678` `collectItemGroup` — three versions of the "kept file" rule; watcher-rescan of an ebook item builds a different file set than a full scan (copy)
- `internal/scanner/files.go:71` `cdDirectory` vs `files.go:872` inline `dirRE` compiled per call (copy)
- `internal/scanner/watcher.go:350` `isMediaFileExt` one caller, three flags (param-one-caller)

### F13 — Scanner run lifecycle in one place; sidecar built from libitem
- `internal/scanner/scanner.go:75-93` `ScanItemForce` vs `:193-203` `scan` — run registration copied (copy)
- `internal/scanner/scanner.go:800-812` vs `scanner/watcher.go:528-538` — two writers of "item went missing", different columns (copy)
- `internal/scanner/watcher.go:300-315` vs `scanner/scanner.go:296-312` `scanResultsText` (copy)
- `internal/scanner/watcher.go:653-656` `emitAuthorCounts` wraps `finalizeScan`; caller resets `tally.authorsChanged = nil` (`:559-562`) (wrapper/convention)
- `internal/scanner/scanner.go:66-71` `Scan`/`ScanForce` wrappers over `scan` (wrapper)
- `internal/scanner/scanner.go:155` `UpdateWatchedPath` — `oldPath` never read; implemented as full `ScanForce`; `"renamed"` handled as `"added"` (dead/if-one-value)
- `internal/scanner/metadata.go:88-191` `WriteMetadata` hand loader vs `import.go:390-401` `afterImport` via libitem (copy); `metadata.go:184-190` and `import.go:393-397` default settings map twice (copy)
- `internal/scanner/metadata.go:42-45` — `metadataFileFormat` honoured then forced to json (if-one-value)
- `internal/scanner/metadata.go:22-30` — `db == nil`/empty-id guards, two callers (if-one-value)
- `internal/scanner/metadata.go:222-266` `bookMetadataObject` accepts `[]any|[]map|[]string` for one field (internals)
- `internal/scanner/import.go:399` — `WriteItemMetadata` result ignored after commit (swallowed-error)
- `internal/scanner/files.go:772-778`, `:796-798`, `import.go:292` — `scanMetadata` filename numbers computed then overwritten whenever `filename != ""` (always true) so the `scanMetadata` smuggling has no effect (dead)
- `internal/scanner/files.go:857` `filenameNumbers` six strings, four always `""` at the surviving call (vocabulary)
- `internal/scanner/files.go:1177` `bookPathMetadata(..., parseSubtitle ...bool)` one caller (param-one-caller)
- `internal/scanner/matcher.go:14-15,87,91` — `visits` counter read only by `matcher_test.go:157-175` (dead in production)
- `internal/scanner/watcher.go:687` — `var _ = sql.ErrNoRows` keeps an unused import (dead)
- `internal/scanner/scanner.go:1003` `mustRel` drops error; `watcher.go:424` `fileIno`; `scanner.go:656` `nowDB`,`ms`; `libraryfiles.go:196` `nowMillis` (wrapper)
- `internal/scanner/files.go:1141-1146` `parseNameList` is `splitMetadataList` (wrapper)
- `internal/scanner/podcast_match.go:130-157` `editSimilarity` copy of `textdist.Levenshtein` (copy)
- `internal/scanner/files.go:325-456` — nine metadata fields listed in four-five places (copy)
- `internal/scanner/files.go:222-234` — empty `default:` and `"folderStructure"` arms (if-one-value)
- `internal/scanner/scanner.go:386-389,408-411,509-513` — scan results/errors discarded by route goroutines (swallowed-error)
- `internal/scanner/scanner.go:752-760` — unreadable folder `continue`s so all its items become Missing (swallowed-error; reconsider: could mark whole library errored)
- `internal/scanner/import.go:268-297` — failed probe drops the file silently, not logged (swallowed-error; behaviour pinned by `TestScanDropsProbeFailedAudio`, add a log only)
- `internal/scanner/watcher.go:98-127`, `:20-23` — poll-until-mtime-stable (see Special cases: keep)
- `internal/scanner/scanner.go:632-635,677-702`, `import.go:200-223`, `files.go:178-271`, `scanner.go:473-478,557-566`, `watcher.go:551-561` — dropped errors (swallowed-error)

### F14 — identity owns the token/session lifecycle
- `internal/identity/identity.go:642-654` `issueToken/issueLegacyToken`, `internal/userapi/userapi.go:1036` `signToken` (+ inline at `:508,699,833`) — three claim builders for `auth.Sign` (copy)
- `internal/userapi/sessions.go` (sessions list/delete) vs `identity.go:565-640`, `:465` — two owners of `sessions` rows (layout)
- API-key lifecycle split: `userapi/userapi.go:788-995` vs `identity/identity.go:231-247`, `:127-133` (layout)
- Password change via hooks: `userapi/userapi.go:153` -> `SetCredentialHooks`, `SetRefreshTokenCookie` (global/convention; also F20)
- `internal/identity/identity.go:182` `TokenSecret()` exists only to feed `userapi.New` (`server/runtime.go:115`) (wrapper)
- `internal/auth/token.go:55-59` — `ValidateToken`/`ValidateTokenIgnoringExpiration` pass `validateToken(...,bool)`; `identity.go:222,249` redoes exp check; `:403` refresh type check by caller (convention/wrapper)
- `internal/server/server.go:176-215` second Bearer path and `server/runtime.go:432` root-count query vs identity (copy; removed by F21)
- `internal/identity/identity.go:119-180`, `:997-1020` — env knobs read in `New` although `config.Config` passed (global)
- `internal/identity/identity.go:1079` `mustJSON` drops marshal error; `:1050-1056` `decodeMap` (swallowed-error)
- `internal/identity/identity.go:707` `emailSettings, _ :=`; `oidc.go:592` `user, _ = h.userByID` (swallowed-error)
- `internal/identity/identity.go:127-133` — identity validates the column sets of five tables (internals)
- `internal/identity/identity.go:995` `sqlDate` wraps `dbtime.Format` (wrapper)
- `internal/auth/token.go:20-30` — `Claims.NotBefore/IssuedAt/KeyID/Raw` have no consumer outside auth (verify before deleting; speculative)
- `internal/identity/identity.go:1089-1140` response-writer layers (`writePassportDenied`...) (wrapper; check `httputil` equivalents, keep Express-text ones)

### F15 — backup on ADR-001 handlers; one admin middleware (backup, scanner, audiotools, adminops, notifications, emailapi)
- `internal/backup/backup.go:166-190` local admin wrapper; each route wrapped by hand (convention)
- `internal/adminops/handler.go:162,205,302,377,398,427,449,476,510,545,619,745` — 11 inline `AdminUser` 403 checks; `validateCron` has none; `:67-83` `read`/`write` closures (copy/wrapper)
- `internal/notifications/handler.go:36-43`, `internal/emailapi/handler.go:45-52` — bespoke admin wrappers (403 vs 404) (copy)
- `internal/audiotools/tools.go:100-128` — `itemRoute` checks `!AdminUser` then repeats `!AdminUser && !Library` (second admin test can never be true); `:296` `batch` rechecks admin (copy/dead branch)
- `internal/backup/backup.go:491-496,560-562` — nine inline 500 `http.Error` blocks; size check repeated (`:495`,`:516`,`:760`) (copy)
- `internal/backup/backup.go:87-94` vs `:131-143` cron create/start; `:330-339` vs `:407-418` upsert (copy)
- `internal/backup/backup.go:200-203,740` `emit` -> `emitFunc` forward; `:216-225` `delete` one closure (wrapper)
- `internal/backup/backup.go:161-166,188-190` `Options.BackupPathEnvSet` only for a log line (vocabulary)
- `internal/backup/backup.go:34-44` nil-able hooks answer 501; production supplies all (speculative)
- `internal/backup/backup.go:272` `h.db` only nil-checked (dead); `:262` reads `absdatabase.sqlite` directly (global)
- `internal/backup/backup.go:218` — failed `os.Remove` still drops the backup from the list (swallowed-error)
- `internal/backup/backup.go:371,419` — `emit backup_applied` after `WriteHeader(200)` (convention)
- `internal/adminops/handler.go:166,215,308,380,458,517,784` — seven inline body decodes next to `httputil.DecodeObjectRequest` (copy)
- `internal/adminops/handler.go:339-341,482-484,581-583,602-604,629-638,664-666`, `upload.go:70-84` — query/exec/IO errors skipped, upload answers 200 on failed write (swallowed-error)
- `internal/adminops/handler.go:204` `authSettings(patch bool)`, `:397` `purgeCache(itemsOnly)`, `:475-544` taxonomy column strings (vocabulary/param-one-caller); `notifications/handler.go:81` `get(data bool)`, `emailapi/handler.go:86` `settings(patch bool)` (vocabulary)
- `internal/adminops/handler.go:56` `RouteDeps{AfterWrite}` one caller; `:90,116` AfterWrite fires for `validate-cron`/`pathexists` (param-one-caller/convention)
- `internal/adminops/handler.go:366-374` sorting-prefix rewrite owns columns `itemwrite` owns (layout; use F4)
- `internal/adminops/handler.go:158` `timeZone` forced `"UTC"` (if-one-value; client contract, keep)
- `internal/emailapi/handler.go:263,271` `from` assigned then `_ = from` (dead); `:469` `mustJSON` unused (dead); `:32-40` `cfg` stored never read; `internal/notifications/handler.go:29` `_ config.Config` (speculative)
- `internal/notifications/handler.go:487` `lastID` no callers (dead); `:498-505` handlers `onPodcastEpisodeDownloaded/onRSSFeedFailed/onRSSFeedDisabled` appear only in the catalog list (no trigger; keep catalog, client-visible)
- `internal/notifications/handler.go:309` `events[len-1]` must be `onTest` (convention)
- `internal/notifications/handler.go:310` `?fail=1` test knob on production endpoint (if-one-value)

### F16 — Dead code, wrappers, speculative options: delete
- `internal/server/runtime.go:68` `NewRuntime` — no callers (dead)
- `internal/httputil/handler.go:141` `Decode` — no callers (dead); `internal/httputil/text.go:40` `WriteError` and `internal/httputil/options.go:26` `ContentType` — tests only (dead)
- `internal/logging/level.go:50` `ParseLevel`, `:47` `Level.Valid` — tests only (dead); `internal/logging/logger.go:213-226` package `Tracef`, `Notef`, `Fatal` — no callers outside logging; instance `Trace..Notef` (`:195-209`) tests only (dead)
- `internal/store/store.go:333` `NullIfZero` — tests only (dead)
- `internal/dbtime/millis.go:85` `ParseUnixMilli` — tests only (dead)
- `internal/access/access.go:122-125` `decodes`/`Decodes()` — production counter read only by `contracttest/permission_revocation_test.go:39` (dead in production; assert through behaviour)
- `internal/server/manager.go:205-206` `_ = oldStore; _ = oldRuntime`; `:298` `dbOrNil` (caller checks err first); `:307` `closeStore` two lines (dead/wrapper)
- `internal/cronspec/cronspec.go:20` `Valid` wraps `Validate` (5 vs 1 callers) (wrapper)
- `internal/realtime/hub.go:141` `UsersOnline()` forwards to `usersOnline` (wrapper); `:763` `int64Pointer` single use (wrapper); `:128-138` `SetDatabase` nil-arg error never produced (speculative); `:289-291` stale comment block (dead)
- `internal/providers/handler.go:92` `stream` wraps `httputil.WriteJSON` (22 uses, wrapper); `internal/sharing/handler.go:95` `write` (wrapper)
- `internal/providers/podcast_finder.go:12` `SearchPodcasts` wraps `itunesPodcasts`+`convertJSON` (needed by `catalog` interface; keep if F20 keeps the interface) (wrapper)
- `internal/providers/book_finder.go:240` `levenshteinDistance`, `internal/podcasts/podcasts.go:530` `similarity`, `internal/textdist/textdist.go:24` `LevenshteinFold` — three wrappers over textdist (wrapper)
- `internal/playback/playback.go:1506` `sendOK` and `internal/audiotools/tools.go:687` `sendOK` duplicate `httputil.OK` (copy/wrapper); `playback.go:1496` `nowMS`, `:1305` `hlsPlaylist` (wrapper)
- `internal/playback/playback.go:536-541` `saveSession` rule wrapper (wrapper)
- `internal/sharing/handler.go:55-58` `New` assigns then returns (wrapper); `sharing/handler.go:32`, `lists/handler.go:33` `queries()` (wrapper)
- `internal/lists/handler.go:26-28` comment describes a parameter that no longer exists (dead comment)
- requestUser copied: `internal/podcasts/podcasts.go:1115`, `lists/handler.go:35`, `catalog/authors.go:103`, `userapi/userapi.go:1220`, `audiotools/tools.go:142` (copy)
- `internal/podcasts/podcasts.go:977` `anyString`, `:481` `str` (wrapper)
- `internal/logging/files.go:357` `containsString` = `slices.Contains`; same at `internal/sharing/feedsrc.go:465` (copy)
- `internal/httputil/handler.go:134` `OK` wraps `SendStatus(200)` (37 callers; keep as name, not delete), `json.go:85` `JSON` wraps `JSONStatus` (wrapper, keep)
- `internal/audiotools/tools.go:150-176` `Tasks` always returns nil error (unreachable branch at `:131-135`), hand insertion sort instead of `slices.SortFunc`, local `copy` shadows builtin `:187` (dead/wrapper)
- `internal/audiotools/tools.go:99` `itemRoute` action takes `store.User` but only `u.ID` read (speculative)
- `internal/audiotools/tools.go:143-151` `include=queue` embeds single key map (speculative)
- `internal/scanner/scanner.go:106-113` `SetPodcastFeedFetcher` mutex-guarded injection (speculative; folds into F20)
- `internal/realtime/hub.go:56-72,87,114`, `internal/podcasts/podcasts.go:1122`, `internal/providers/handler.go:44-49`, `internal/scanner/watcher.go:20-23` — test seams in production code (`AddTestClient`, `SetTestEmitter`, `connectFn`, `SetHTTPClientForTests`, `SetEndpoint`, `watcherPendingDelay`): keep only where a behaviour test needs them; list as speculative
- `internal/logging/logger.go:24-30` `Options{Level,Stdout,Stderr,Now}`/`New` — one production logger (speculative; tests use it, keep unless tests are rewritten)
- `internal/logging/logger.go:63-66` — zero `Level` rewritten Trace->Info (if-one-value)
- `internal/emailapi/handler.go:318-320` — `port != 465 -> secure=false` is nodemailer rule (keep, see Special cases)
- `internal/httputil/options.go:203-227` `Option` variadic for `ForRequest` (35 uses) (speculative; shrink after `ContentType` removal)
- `internal/store/store.go:298-345` — 15-function null vocabulary (vocabulary; `NullInt64`/`StringPtrValue` have many callers, not removable)
- `internal/logging/http.go:16-56` prefix block pasted three times (+ `server/recover.go:36`) (copy)
- `internal/logging/files.go:19-21` `RecentLimit`/`DefaultDailyLogsToKeep` exported, used inside only (speculative)
- `internal/config/config.go:84-90` `ABS_GO_CLIENT_PATH` probing (keep, deployment convenience) 
- `cmd/abs-compat/main.go`, `internal/compat/compat.go` — referenced only by `README.md:95`; no CI/Makefile use, no tests for the command; duplicate of `tools/parity` (dead; delete or move under `tools/` — needs owner nod, see Owner decisions)
- `../.golangci.yml:29-34,99-100,122-123` — rules for nonexistent `internal/media`, `internal/authz`, `internal/books` (dead)
- `tools/ratchet/main.go:116-122` `isHandleBoolRouter` and baseline metrics fixed at 0 (`../ratchet-baseline.json`) (dead)
- `tools/ratchet/main.go:124` and `tools/clientusage/main.go:909` both define `repositoryRoot()` (copy)
- `tools/__pycache__`, `tools/parity/__pycache__` — bytecode in tree (dead; check `git ls-files` before removing)
- `internal/db/schema.sql`, `internal/db/sqlc/schema.sql`, `contracts/parity/schema.sql` — three generations guarded by drift tests (copy; keep, drift tests are the guard)

### F17 — One version constant
- `"2.37.2"` in `internal/scanner/scanner.go:804,860`, `scanner/import.go:378,384`, `backup/backup.go:31`, `playback/playback.go:55`, `sharing/sharedevice.go:17`, `sharing/publicshare.go:216`, `server/server.go:22`, `bootstrap/bootstrap.go:26`, `notifications/handler.go:504`, `db/sqlc/playback.sql.go:186` (copy; generated sqlc file keeps its own literal in SQL)

### F18 — Layout: split mixed files by feature
- `internal/catalog/handler.go` (2008 lines): library read/stats `:273-597`, filterdata `:599-762`, authors list `:764-911`, series list `:913-1194`, item list `:1218-1340`, item detail `:1342-1460`, file serving `:1462-1713`, search `:1715-2008`; series split across six files, authors across four; file names `library_extras`, `series_features` say nothing (layout)
- `internal/catalog/handler.go:1588` `pathAllowed` used by writes from a read file (`writes.go:393,671`, `batch_chapters.go:324`) (layout)
- `internal/catalog/routes.go:27-98` registry vs handlers in `library_extras.go`, `library_issues.go`, `library_routes.go`; dangling TS-route comments `routes.go:48-50` (layout)
- `internal/catalog/writes.go:240-405` item deletion DB cleanup in the HTTP write file (layout)
- `internal/catalog/authors.go:1-1068` generic helpers (`requestUser`, `isUUID`, `sliceHas`, `firstNonEmpty`, `axiosMessage`) used by other catalog files (layout)
- Body-parsing helpers in four files: `writes.go:~442`, `authors.go:353-380`, `batch.go:86-130`, `quickmatch.go:42-76` (layout)
- `internal/catalog/match_task.go` + `quickmatch.go:593-663` — matchAll split from its task envelope (layout)
- `internal/playback/playback.go` (1517 lines): sessions, device upsert, HLS transcode `:1039-1304`, media serving, local sync (layout); `:476` `handleSync` serves `/sync` and `/close` by path suffix, `:881` by `"-all"` (convention); `:1273` 6s segment constant in five places (convention); `:1272-1273` `time.Sleep(100ms)` + `t.startTime/segmentStart` written outside `t.mu` (race; see Special cases)
- `internal/realtime/hub.go`: framing (`eio.go`, `sio.go`) with domain logic (auth messages, item payload filtering `:607-682`, cover/log/scan handlers `:386-438`); imports libitem/access/auth/sqlc/store (layout/direction)
- `internal/realtime/hub.go:97-116,128-138` hub rebind via `SetDatabase`; `server/manager.go:126-127,173,198` passes the old hub to the next runtime (global)
- `internal/podcasts/podcasts.go` (1138 lines): routes, episode CRUD, OPML, scheduler, feed fetching; `podcasts/downloads.go` also ffprobe+DB registration (layout)
- `internal/podcasts/podcasts.go:376-380`, `downloads.go:509-515`, `catalog/writes.go:414,719`, `catalog/library_writes.go:529` — episode delete written three times, `numEpisodes` rule differs (`len-1` `podcasts.go:384` vs `COUNT(*)` `downloads.go:518`) (copy)
- `internal/podcasts/podcasts.go:403-433` `checkNew` vs `:887-950` `runScheduled`; `UPDATE podcasts SET lastEpisodeCheck` four times (`:426,931,937,947`) (copy)
- `internal/podcasts/downloads.go:329-420` `registerEpisode` hand-builds file records `scanner/files.go` also builds (copy)
- `internal/sharing/feeds.go` (746 lines): rows, handlers, XML (`:612-746`), refresh (layout); `lists/collection_http.go:462-480` rebuilds feed JSON that `sharing/feeds.go:133 feedRow.old()` produces (copy)
- `internal/sharing/feeds.go:28-120`, `shares.go:23-65` — row structs mirror sqlc rows column by column (shallow)
- `internal/sharing/publicshare.go:71-87` vs `playback/playback.go:1012-1028` UUID shape check twice (copy; use `ids`)
- `internal/sharing/publicshare.go:25-52,215-222` vs `playback/playback.go:60-90,1310` parallel session/track models (copy)
- `internal/sharing/feedsrc.go:228`, `playback/playback.go:383`, `catalog/handler.go:122` `deref` copies; `userapi.go:1161` and `realtime/hub.go:763` `int64Pointer` (copy)
- `internal/lists/handler.go:40-44` `cleanName/stripMarkup` third tag-stripper beside `textclean`; `podcasts/podcasts.go:245,692`, `providers/match.go:53` (copy)
- `internal/providers/handler.go:104-130` `getCustomProviders` vs `:284-304` `customProviders` (copy)
- `internal/providers/builtin_search.go:61`, `cover_search.go:104`, `handler.go:236` — audible-region list three times; `book_finder.go:109-112` vs `builtin_search.go:61-64` region parse; 10s timeout literal in five places (`book_finder.go:30`, `builtin_search.go:41,469`, `custom_search.go:18`, `cover_search.go:13`) (copy)
- `internal/providers/builtin_search.go` (751 lines) mixes routes and six upstream clients; `providers/handler.go:137-224` CRUD beside search (layout)
- `internal/providers/custom_search.go:17` `searchCustom(..., mediaType, ..., isbn)` `mediaType` always `"book"` (param-one-caller); `book_finder.go:107` `runBookSearch` max distances passed unchanged (param-one-caller); `custom_search.go:18-20,48-55` unreachable `timeout<=0`/nil-client branches (speculative)
- `internal/adminops` mixes seven features (layout)
- `internal/server/manager.go:235-296` `copyDirectoryStage` belongs with `backup` (layout)
- `internal/notifications/handler.go:498-505` event catalog as `[]any` of maps (layout)
- `internal/audiotools/tools.go` (690 lines) routes, registry, queue, args, loader; `:197-247` `loadItem` is a fifth item loader (layout)
- `internal/scanner/scanner.go:352-437` routes in the same file as orchestration and watcher arm (layout); `scanner/metadata.go` sidecar writer triggered from catalog (layout); `scanner/podcast_match.go:16-110` podcast matching inside scanner wired into catalog and podcasts (layout)
- `internal/server/runtime.go:168-197` socket dispatcher decodes cover/log/scan payloads; `:149-164` playback lookup and watcher mapping in the root (layout; F20)

### F19 — store owns the database: one open (pragmas) and one path helper
- Five hand-built DSNs / `SetMaxOpenConns(1)`: `internal/store/store.go:51-91`, `store/open_write.go:14-55`, `bootstrap/bootstrap.go:137-149,215-236`, `backup/backup.go:286-289` (copy; `bootstrap.openReadOnly` is also used by `verifyRacedExisting` at `bootstrap.go:119`, so it stays until bootstrap uses the shared open)
- `config/absdatabase.sqlite` joined in `cmd/audiobookshelf-go/main.go:56`, `server/manager.go:116,189`, `contracttest/fixture.go:48`, `backup/backup.go:262,286` (copy)
- `busy_timeout(5000)` in DSN and again by `PRAGMA` (`bootstrap/bootstrap.go:138,149`, `store/open_write.go:31`, `store.go:71`) (copy)
- `internal/store/device.go`, `store/playbacksession.go` hold legacy JSON builders in the DB package (layout)
- `internal/contracttest/fixture.go:74,111-136` re-types the production open sequence (`main.go:53-93`) (copy)
- `internal/bootstrap/bootstrap.go:21` imports `contracts/parity` assets from production (direction; keep, schema embed)
- `internal/store/store.go:27` — "Store is a read-only handle" comment is false (stale); package doc `:1-3` stale; `internal/config/config.go:1-3,14-16` stale (both go with F21)
- `internal/store/store.go:196-198` `lenientJSON` maps invalid JSON to null (keep: documented contract)

### F20 — Composition root passes collaborators through constructors
- `internal/server/runtime.go:136-163,199,217` — `SetQuickMatchHandler`, `SetPodcastFeedFetcher`, `SetCredentialHooks`, `SetOnlineData`, `SetSessionLookup`, `SetItemChanged`, `SetWatcherUpdate`, `SetLibraryChanged`, `SetSettingsChanged`, `SetNotificationHandler`, `SetDispatcher`, `SetUserAddedEmitter` (ratchet metric `Set* injector methods: 18`) (convention/global)
- `internal/catalog/routes.go:20-22` `Routes` mutates `h.authorFinder/finder/episodes`; handlers misbehave before it; `catalog/handler_test.go:1841 Handle` calls `Routes` per request (global)
- `internal/catalog/handler.go:246,250`, `catalog/mutation.go:24-46` — `itemChanged` nil makes `saveMetadataFiles` silently skip the sidecar (global)
- `internal/catalog/handler.go:178-181`, `:207` `DownloadQueue` + `WithDownloadQueue` single implementation, nil check `:1392` (speculative)
- `internal/catalog/routes.go:12-22` `RouteDeps` three fields set once (speculative); `Handler` struct ten collaborators through three routes (shallow)
- `internal/catalog/quickmatch.go:27-32` `BookFinder`, `authors.go:39-44` `AuthorFinder` single implementation (providers); used for fakes (speculative; keep if tests need the seam)
- `internal/hub/provider cover-search`: `internal/realtime/hub.go:386-418`, `server/runtime.go:183-195`, `providers/cover_search.go:39-98` — validation, decode and events split three ways; `cover_search_error` vs `cover_search_provider_error` vocabulary (layout)
- `internal/realtime/hub.go:33-44`, `:419-438` — generic `Action` carries cover-search vocabulary (vocabulary)
- `internal/server/runtime.go:72` `newRuntime(db,cfg,repo,manager,preservedHub...)`; `:198` `if manager != nil` (production always passes one); `manager.go:25-31` each field touched by `restore`; `Manager` and `Runtime` mutual dependence (`manager.go:128,173,195`, `runtime.go:210,235`) (internals/shallow)
- `internal/server/manager.go:210` `rollbackRestore` eight params, `resumeOld` discards two (vocabulary)
- `internal/server/manager.go:101-186` — restore protocol order known only to reader (convention)
- `internal/server/runtime.go:97-98,115,149` call order identity -> realtime -> emitters (convention)
- `internal/server/runtime.go:140-144,242` — routes must register before `backup.Routes`; `serveRoute` (`:357`) probes mux, missing route silently 404s (convention)
- `internal/scanner/scanner.go:71-72,596-612` `Refresh` restarts watcher and every cron job on any library/settings write (internals)
- `internal/playback/playback.go:731-745` `openSessions` one `GetUserByID` per session with `context.Background()` (internals)
- `internal/lists/handler.go:21-31` `canView` closure from `userapi.CanReadItem` (`server/runtime.go:122`) couples lists to userapi; `CanReadItem` exists only for it (wrapper)
- `internal/config/config.go:17-27` — `Config` passed by value to ~15 constructors, most use one field (shallow)
- `internal/scanner/scanner_test.go:22,42` and `server/runtime.go:102` — scanner takes `catalog.Handler` as its Provider (direction; resolved by F11)
- `internal/backup/backup.go:46-58` `Options.Restore/UpdatePath` closures from `server/manager.go`; backup knows the stage-dir layout (direction/internals)
- `internal/audiotools/tools.go:5-24` imports `access` for one boolean (direction)
- `internal/podcasts/podcasts.go:69`, `:79-83`, `:88-95` — env read in constructor, `SetQuickMatchHandler` queueMu, `adminOnly` closure (global/speculative)
- `internal/podcasts/podcasts.go:843-866`, `catalog/writes.go:66`, `catalog/mutation.go:168-174`, `catalog/batch.go:248` — schedule edits do not call `addSchedule`; they take effect after restart (global; behaviour change: needs owner decision before wiring, see Owner decisions)
- `internal/sharing/handler.go:22-30,34` in-memory `sessions` map shared with `dropShareSessions` (`sharing/shares.go:183`); `findShare` (no expiry) vs `loadShare`; `publicshare.go:230,260,315` use `findShare` and skip expiry (convention)
- `internal/sharing/feeds.go:247-304` `openFeed` slug-check order per entity (if-one-value/convention)
- `internal/lists/routes.go:11-47` HEAD wrapped by hand per GET (convention)
- `internal/providers/handler.go:62-70` custom-provider admin closure (convention)
- `internal/podcasts/podcasts.go:88-123`, `:404`, `:180` admin checks split between route and handler (convention)
- `internal/emailapi/handler.go:239`, `adminops/handler.go:431-444` raw SQL on `libraryItems/books/playbackSessions` (internals)
- `internal/server/runtime.go:546-600`, `:473-480` client route table; public cover/author-image path tests re-parse patterns (internals)
- `internal/server/runtime.go:535` vs `server/recover.go:56` Express error HTML built twice; `runtime.go:602` `writePlain` vs `httputil.SendStatus` (copy; server-side only, bytes unchanged)
- `internal/server/logging.go:11` `ApplyLoggingSettings` repeated by `adminops/handler.go:188-191` (copy)
- `internal/logging/logger.go:73-76` process-wide logger rebound on every runtime rebuild (global; keep, single logger)
- `internal/httputil/handler.go:28-33` `Error{Status,Message,Cause,Empty}` flag changes branches (shallow)
- `internal/bootstrap/bootstrap.go:77` `if cfg.MetadataPath != ""` always true (`config.go:75`) (if-one-value)
- `../Makefile:11`, `../Dockerfile.go:33`, `../.github/workflows/docker-go.yml:97` — hand-maintained lists/ldflags (convention; arm64 build omits `buildCommit`)

### F21 — Remove read-only mode (owner decision 2026-11-07)
Owner decided to remove `ABS_GO_READ_ONLY` entirely; the server always runs read-write. Verified code to delete:
- `cmd/audiobookshelf-go/main.go:58-62` (`store.OpenReadOnly` branch), `:74` (`!cfg.ReadOnly` guard), `:80-81` (`server.NewHandler` branch), `:110-113` (`mode` string) — collapse to the read-write path
- `internal/server/server.go:22-27` `Repository` interface, `:31-60` `Handler`/`NewHandler`/`ServeHTTP`, `:82` `status`, `:136` `api`, `:181` `authenticate`, `:209` `unsupported` (whole read-only surface; `_ = claims` at `:144`, duplicate `/ping` `/status` `/healthcheck` and hand-written 401). Keep `isGet` (`:213`, 8 uses in `runtime.go`) and `Version` (`:22`, moved by F17) — relocate to `runtime.go`
- `internal/server/runtime.go:45,68,72,75,124,222` — `Runtime.repo` / `Repository` use is only `ServerSettings`; replace with `serversettings` (F3), then drop `repo` param from `newRuntime` and the dead `NewRuntime` (`:68`)
- `internal/store/store.go:49-91` `OpenReadOnly`, `:99-108` `UserByID`, `:157-169` `UserByIDOrOldID`, `:223-296` `Libraries`/`libraryFolders` (`:366`), `:202` `ServerSettings` — only the read-only handler (and tests) call them; `internal/store/store.go:1-3`, `:27` stale doc
- `internal/bootstrap/bootstrap.go:59` env read `ABS_GO_READ_ONLY`, `:54` doc, `:28-30,72` `ErrReadOnlyDatabaseMissing`; keep `openReadOnly` (`:215`) only while `verifyRacedExisting` (`:119`) uses it, else move to F19 shared open
- `internal/config/config.go:26` `ReadOnly` field, `:99` env read, `:1-3,14-16` stale "read-only slice" docs
- Tests: `internal/server/server_test.go` (all 6 tests on the read-only `Handler`, `fakeRepo`), `internal/store/store_test.go:83 TestReadOnlyReadsUpstreamModels`, `:148 TestOpenReadOnlyDoesNotCreateAndRequiresSchema`, `TestLibrariesMatchPinnedUpstreamGolden`, `:206` OpenReadOnly use, `internal/bootstrap/bootstrap_test.go:78 TestEnsureReadOnlyMissingDatabaseDoesNotCreate`, `internal/realtime/hub_test.go:279-285` (uses `store.OpenReadOnly` + `UserByID` to re-read after socket auth: rewrite to read the row with `sql`/sqlc), `internal/contracttest/journey_record_test.go:52` comment
- Docs: `README.md:60`, `ROADMAP.md:258,266-267`, `PRUNING-REVIEW.md:24`, `docs/SIMPLIFICATION.md` ledger entries (RO01/RO02 say "retain it"): mark superseded by the owner decision
- After removal: `if manager != nil` (`internal/server/runtime.go:198`) stays only for tests (F20); `cmd` line count and ratchet baseline change (`make ratchet-update`)

### Unassigned
- `internal/server/runtime.go:298-308`, `server/server.go:66-76` duplicated `/ping` (second goes with F21); `http.Error` 503 strings `runtime.go:428,434` (copy; server-local, can follow F21)
- `internal/realtime/hub.go:269-278,350-358` `dispatch` error ignored in `disconnected`; DB write failure leaves socket unauthenticated without `auth_failed` (swallowed-error)
- `internal/realtime/eio.go:176,189` `time.After` per heartbeat tick (retry-sleep, minor; use ticker)
- `internal/playback/playback.go:955,733-742,752-755` ignored errors (`CountPlaybackSessionByID`, `GetUserByID`, marshal) (swallowed-error); `:581-586` `libraryFinishSettings` ignores Scan/Unmarshal (swallowed-error)
- `internal/playback/playback.go:303` vs `:962-995` two session constructors compute Date/DayOfWeek/BookID; `:545-548,597-600,991` mediaID rule three times (copy)
- `internal/playback/playback.go:596` `syncProgress(duration, lastUpdate)` two workflows' flags (vocabulary); `:1436` `serveMedia(mime)` always `f.MimeType` (param-one-caller)
- `internal/playback/playback.go:456,1311` podcast "track 0" fallback (if-one-value, upstream behaviour: keep)
- `internal/lists/playlist_http.go:331` `addPlaylistItem(..., batch bool)`; `:495` `deletePlaylistMediaIDs` map param (vocabulary)
- `internal/podcasts/podcasts.go:576` `createPodcastFromBody` returns HTTP status/message (vocabulary); `:778` `runOPMLImport` mutates `task` (param-one-caller)
- `internal/podcasts/podcasts.go:476-478,292-293,370,697-698`, `downloads.go:381-383,410,412,459,502`, `:172,275`, `podcasts.go:845-861` — dropped errors (swallowed-error)
- `internal/providers/builtin_search.go:109-127`, `custom_search.go:56-68` — upstream failure becomes empty list (swallowed-error; upstream parity, keep)
- `internal/sharing/*` dropped errors: `publicshare.go:183-188,196`, `shares.go:209`, `feeds.go:253,487`, `handler.go:96` (swallowed-error)
- `internal/sharing/feeds.go:404` `createFeed` (8 params), `:355` `newFeedRow` (9 params) (param-one-caller)
- `internal/identity/identity.go:53-72` Handler exposes limiter/flow maps; tests poke `h.oidcFlows` (internals)
- `internal/identity/oidc.go` (918 lines) separate feature in `identity` package (layout)
- `internal/userapi/userapi.go` 1322 lines mixing progress, bookmarks, admin, API keys, tokens, DTOs (layout)
- `internal/userapi/userapi.go:547,1141` raw SQL bypassing store/sqlc; `:512` raw INSERT (internals)
- `internal/access/access.go:11-14` imports `store`+`libitem` (direction); `:127-149` `Of` vs `For` vs `WithRequest` convention; `realtime/hub.go` and `identity/identity.go:694` use `Of` and miss the snapshot (convention)
- `internal/access/access.go:98` `MediaClause(alias)` alias convention (vocabulary); `:80-82` Go `Content` and SQL `MediaClause` must agree (convention; guarded by `TestMediaClauseMatchesContent`)
- `internal/access/access.go:66-68` `Library` vs `ListLibrary` (one caller `server/server.go:159`, goes with F21 -> then `ListLibrary` is unused: delete) 
- `internal/access/access.go:19-30` `Permissions` exports flag fields beside methods (shallow)
- `internal/auth/token.go:77-93` userId rule split between auth and identity (convention)
- `internal/deviceinfo/ua.go:7` four-string return (vocabulary)
- `internal/dbtime`, `internal/store/sqlvalue.go` `Text/TextOrNull/...` null vocabulary (vocabulary)
- `internal/textsort/textsort.go:27-30` global mutex around collators (speculative; needed, `collate.Collator` not goroutine-safe: keep)
- `internal/libitem` imported by nearly everything (direction; not a defect on its own)
- `internal/itemwrite/relations.go:106-115` comments name `SetAuthors/SetSeries` (stale; goes to Comments)
- `internal/itemwrite/relations.go:203-214` insertion order carries book author order (convention; read contract)
- `internal/itemwrite/sweep.go:28-35` `Env` nil-field convention (shallow)
- `internal/itemwrite/sweep.go:99-122` sweep returns nil on error (swallowed-error; by design, add comment only)
- `internal/store/json.go:11-30` lenient decode (documented; keep)
- `internal/logging/logger.go:115` `write(level, source, msg, depth)` caller must pass depth 2 (convention)
- `internal/logging/logger.go:60-70` zero `Options.Level` rewritten to INFO (convention)
- `internal/emailapi/handler.go:322,336`, `notifications/handler.go:401` magic timeouts 8s/6s (retry-sleep category, not a retry; keep)
- `internal/compat/compat.go:282-330` vs `tools/parity/lib/compare.cjs` (see F16 compat)
- `internal/contracttest/*_test.go` helper repetition (`login`, `request`) pasted per file (copy; see Tests)
- `internal/db/schema_test.go:35` re-implements the awk filter of `Makefile:62` (copy; keep)
- `../Makefile:11` `test-ts-diagnostics` hand-lists packages (convention)
- `../ratchet-baseline.json` lags the ledger (convention)

## Special cases

- `internal/bootstrap/bootstrap.go:103-116` retry-sleep: 300 x `time.Sleep(10ms)` polling for `migrationsMeta` — keep: second process racing first-boot `initializeNew` needs it (`TestEnsureConcurrentFirstBoot`)
- `internal/serversettings/serversettings.go:55-77` retry loop on generation mismatch — keep: lost-update guard for concurrent `Invalidate`; becomes simpler under F3 (`Save` bumps generation)
- `internal/scanner/watcher.go:98-127` per-file 3s poll up to 10 min — keep: mirrors Watcher.js mtime-settle (upstream behaviour)
- `internal/playback/playback.go:1272-1273` `time.Sleep(100ms)` waits for killed ffmpeg before wiping dir — keep sleep until process-exit wait replaces it; fix `startTime/segmentStart` writes outside `t.mu` (`:1280-1281`) (F18)
- `internal/playback/playback.go:1298-1300` `close()` waits up to 3s — keep: bounded ffmpeg shutdown
- `internal/logging/logger.go:63-66` Trace->Info rewrite — keep: TRACE only via ChangeLevel (comment `:51-52`)
- `internal/server/runtime.go:198` `if manager != nil` — F20 (tests are the only nil case); then keep or make unconditional
- `internal/bootstrap/bootstrap.go:77` `MetadataPath != ""` — F21/F19 (`LoadArgs` always sets it)
- `cmd/audiobookshelf-go/main.go:58,74,80,110` four `cfg.ReadOnly` switches — F21
- `internal/server/server.go:144` `_ = claims`; `:157-161` `/api/authorize` branch identical to fallthrough — F21
- `internal/identity/identity.go:274` `hideRootToken,minimal` always false — F7
- `internal/catalog/handler.go:1217-1224,1721-1727` books/podcasts 501 switch — F9/F10
- `internal/catalog/handler.go:615` `numIssues:0,bookCount:0` defaults at every branch — F11
- `internal/catalog/personalized.go:80-83` `ignorePrefix` read as bool from `Doc()` map — F3
- `internal/catalog/quickmatch.go:213-216` default provider `"google"` also needed at `:651` — F10 (one constant)
- `internal/catalog/cover.go:105-113` `width:=400,height:=-1`, 4096 clamp twice — F10 (client-visible defaults, keep values)
- `internal/catalog/quickmatch.go:239,244,296` `table := "books"` switched later — F10
- `internal/itemwrite/relations.go:153` `seqSel` — F10
- `internal/catalog/handler.go:93` ignored string param; `:1008-1018` no-op sort cases — F9, F16
- `internal/catalog/handler.go:1435` `ItemJSON(expanded bool)`; `:1588` `metadataOK`; `:1368` `expandedItemIncludes` — F10
- `internal/catalog/query.go:194` `podcast bool` switching unknown-filter semantics; `personalized.go:152,167` booleans selecting query shapes — keep: upstream differs between book and podcast, fold into F9 `listParams` (mediaType instead of bool)
- `internal/catalog/collapse_subseries.go:124,150` `ignorePrefix` threaded — F9
- `internal/catalog/writes.go:230,239` nil-user mode — F10
- `internal/audiotools/tools.go:358` `queued` — F6
- `internal/audiotools/tools.go:100-128` unreachable second admin test — F15
- `internal/scanner/files.go:222-234` empty arms; `scanner/metadata.go:42-45` forced json; `scanner/metadata.go:22-30` nil guards — F13
- `internal/scanner/watcher.go:380` `"renamed"` same as `"added"` — keep: watcher event kinds from the client/fs contract; `scanner.go:155` `oldPath` — F13
- `internal/scanner/scanner.go:675` default prefixes duplicate — F3/F4
- `internal/backup/backup.go:278,455` `Key:"sqlite"` typed any, `details()` re-derives (`:605-609`); `readBackup` maps `"1"` to nil (`:593-596`) — keep: backup JSON contract with upstream; no fix
- `internal/podcasts/downloads.go:527-547`, `podcasts.go:1017-1019` User-Agent appends `https://www.cbc.ca` — keep: upstream quirk needed by feed hosts
- `internal/podcasts/downloads.go:549-563` unknown extension -> `audio/mpeg` — F12
- `internal/playback/playback.go:456,1311` podcast "track 0" — keep: upstream/client contract
- `internal/realtime/hub.go:419-436` `dispatchAction` special-cases `search_covers` — F20
- `internal/realtime/hub.go:241-246` `remove_log_listener` with empty `store.User{}` — keep: upstream allows without check
- `internal/sharing/feeds.go:247-292` `openFeed` branches on `libraryItem` — keep: each entity type loads a different source; share steps (F18)
- `internal/emailapi/handler.go:318-320` `port != 465 -> secure=false` — keep: nodemailer rule
- `internal/adminops/handler.go:158` `timeZone` forced `"UTC"` — keep: client contract
- `internal/notifications/handler.go:309-310` `events[len-1]`, `?fail=1` — F15
- `internal/access/access.go:114-117` flags — F7
- `internal/identity/identity.go:790-801` `availability==""` -> `adminOrUp` — keep: upstream default (single copy after F7)
- `internal/userapi/userapi.go:1200` role matrix map literal — F7
- `internal/userapi/userapi.go:277-281` swallowed batch progress errors — keep: upstream behaviour
- `serversettings.Load` vs `Get` — F3
- swallowed-error clusters: scanner/catalog/adminops/server/manager/playback/podcasts/sharing — F10, F13, F15, Unassigned; keep as best-effort: `internal/server/manager.go:113,132,149,154,165,181-184,217-228` rollback `os.Remove/Rename` (log them), `internal/audiotools/tools.go:627-628` temp cleanup, `internal/bootstrap/bootstrap.go:92,96,145,159` rollbacks, `cmd/audiobookshelf-go/main.go:107` shutdown, `internal/logging/files.go:255-270,337`
- disabled tests: see Tests

## Owner decisions (resolved 2026-11-07)

- `ABS_GO_READ_ONLY`: owner: remove the mode (F21).
- `cmd/abs-compat` + `internal/compat`: owner: delete; `tools/parity` is the one comparison harness (F16).
- Podcast schedule edits through catalog media writes do not reschedule until restart: upstream reschedules
  (`server/controllers/LibraryItemController.js:276` → `CronManager.checkUpdatePodcastCron`). Bug; fixed by F10's
  write-finish step.
- `internal/scanner/files.go:1170` `lastFirst`: upstream uses `Author.getLastFirst` → `parseNameString.nameToLastFirst`
  (`server/scanner/BookScanner.js:237,539`), which `nameparse.ToLastFirst` ports. The scanner copy is a bug; F4 unifies.
- `internal/scanner/scanner.go:752-760` unreadable folder marks items Missing: keep (upstream parity).
- Production test seams (`realtime` `AddTestClient/SetTestEmitter/connectFn`, `providers.SetEndpoint`,
  `podcasts.SetHTTPClientForTests`): decided in the Tests pass — replace with constructor-injected collaborators
  where that removes the seam, otherwise keep.
- `tools/**/__pycache__`: untracked (not in git); nothing to do.

## Comments to remove

Verified rule: restates the code; TS citations (`file.js:NN`), client/OS/SQLite/upstream facts kept. Ranges are from the surveys; line numbers may be off by a few lines, delete by content.
- `internal/catalog/handler.go:41-54`, `:59-60`, `:75-76`, `:90-92` (history of `itemMaps`), `:143`, `:176-177`, `:184-186` (misplaced `queries`), `:206-212`, `:223-224`, `:232-233`, `:246-250`, `:597-598`, `:1433-1434` (first sentence only), `:1453`, `:1868-1869`, `:1873-1875`
- `internal/catalog/query.go:14-29`, `:123-124`, `:151-153`, `:178-180`
- `internal/catalog/routes.go:15-19` (first half), `:48-50` (dangling TS-route comments)
- `internal/catalog/library_routes.go:28-31`, `:34-36`, `:99-101`, `:116-118`
- `internal/catalog/library_issues.go:12-14`
- `internal/catalog/personalized.go:15-20`, `:25-27`, `:35-37`, `:121-122`, `:136-139`, `:146`, `:149-151`, `:166-169`
- `internal/catalog/personalized_books.go:14-16`, `:38-39`, `:63-66`
- `internal/catalog/series_routes.go:21-22,32-33`; `catalog/collapse_series.go:7-8,12-13,16-23`; `catalog/collapse_subseries.go:26-27,30-31,44-47,49-50`
- `internal/catalog/download.go:43-50` (first two sentences)
- `internal/catalog/mutation.go:19`, `:23`, `:64`, `:69`, `:73`, `:249-257`
- `internal/catalog/match_task.go:11-13`, `:22-24`
- `internal/catalog/quickmatch.go:78`, `:343-345` (second sentence), `:351-353`, `:431`
- `internal/catalog/batch.go:28-31,60-62,86,103-105` (prose; keep JS cites)
- `internal/catalog/authors.go:102,148-150,171-173,190-191,389-390,409-410`
- `internal/filterdata/filterdata.go:16-17,20,26,71,85,103,111,122,129,136`
- `internal/wire/log.go:7-10`; `internal/wire/session.go:3-8` (partly)
- `internal/itemwrite/relations.go:1-8,36,39-46,64-65,92,105-106 (stale names),116-118,271-272`; `itemwrite/sweep.go:23-26,37-43` (keep `Clock` Sequelize note `:63-68`)
- `internal/textdist/textdist.go:1-3,6,23-24`
- `internal/access/access.go:46-47` (second half), `:118`, `:123-124`, `:127-131`
- `internal/identity/identity.go:72-73,119-121,181`; `internal/userapi/userapi.go:29-33,1019-1020,1044,1112,1117-1118,1229`
- `internal/serversettings/serversettings.go:15,17-19,43,52-54 (keep invariant at 53-55),81-82,90,96`
- `internal/auth/token.go:1-8,35-37,151-165`; `internal/ids/ids.go:10-12`; `internal/progressdto/apply.go:60-62`
- `internal/bootstrap/bootstrap.go:28-30,84-86`; `internal/store/store.go:93-97,134,221-222`; `store.go:27` and `:1-3` (false/stale; rewrite, not delete)
- `internal/config/config.go:1-3,14-16` (stale; goes with F21)
- `internal/server/server.go:1-2,33-35` (goes with F21); `server/runtime.go:61-63` (Handle chain gone), `:66-67,247-251,344-348`; `server/manager.go:22-24,108,305-306`; `server/recover.go:72-75,112-113`
- `internal/httputil/handler.go:13-24,26-28,38-39,44-45,47-50,82-84,89`; `httputil/text.go:286-288` (with `WriteError`); `httputil/options.go:202-203,218,223-224`
- `internal/logging/logger.go:20-23,34,44-46,76,79,90-91,98-99`; `logging/files.go:52-56,239-241,287`
- `internal/dbtime/dbtime.go:13-17`; `dbtime/millis.go:53-55,69-72,96-99,126-127,136-137`; `internal/cronspec/cronspec.go:19,22,28,31`; `internal/events/events.go:6-8`
- `internal/adminops/handler.go:55,63-65` (keep `:333-334` deadlock fact and `:668-670` TS fact); `notifications/handler.go:33-34,314-317`; `emailapi/handler.go:42-43`
- `internal/contracttest/fixture.go:56-58,63-66,68-76,87-90`; `cmd/audiobookshelf-go/main.go:39-40`
- `internal/providers/handler.go:44-46,91`; `providers/cover_search.go:35-38,64-65,98-99,112`; `providers/builtin_search.go:337-339` (prose; keep cite)
- `internal/sharing/handler.go:31-32,101` (keep `Server.js:324` cite); `internal/lists/routes.go:21`; `lists/collection_http.go:481`; `lists/handler.go:26-28` (stale, delete)
- `internal/realtime/hub.go:50-54,119-121,140-141,163-164,166,289-291 (stale),517,605-606`
- `internal/playback/playback.go:206-207,214,596-601 (prose only),1335`
- `internal/podcasts/podcasts.go:77-78,574-575,1120-1121,1124`; `podcasts/library.go:56-58`
- `internal/scanner/scanner.go:34-35,53-55,74-76,151-154,228-229 (first clause),466-467,517-518,531-532,603-605,921-925 (first sentence; keep SQLite note),960-964 (keep TS cite)`
- `internal/scanner/import.go:15-18,34-36,70-72,83-86,189-190,234-236,262-265 (keep BookScanner.js cite),352-353,364-366,388-389`
- `internal/scanner/walk.go:12-17 (stale "only directory walk"),63-66,69-71,77-79,203-204`; `scanner/matcher.go:3-6,57-63,82-83 (keep LibraryScanner.js cite)`; `scanner/libraryfiles.go:34-35,56-58,129-131,144-146`
- `internal/scanner/files.go:152-155 (names nonexistent function),166-168 (orphaned),181-183,667-674,1159-1161`
- `internal/scanner/watcher.go:25-29,85-87,130-131,251-252,438-439,470-471,646-647,667-669`; `scanner/metadata.go:17-19,85-87`
- `internal/audiotools/tools.go:1-3,95-97,150,154`
- `internal/backup/backup.go:36-38,46-48,95-97,100-102,153-158,162-165,166-169,171-173,334`

## Tests to change

- `internal/catalog/query_count_test.go` `TestQueryCounterIncludesPreparedReads` — tests the test-only counter driver: remove
- `internal/catalog/query_count_test.go` `TestIncludePageQueryCountIndependentOfPageSize`, `internal/catalog/book_batch_test.go` `TestBookPageQueryCountIndependentOfPageSize`, `internal/libitem/libitem_test.go` `TestLibitemLoadQueryCountIsConstant` — pin SQL call counts: rewrite to assert no per-row queries with a bounded count (keep one guard; remove the other two)
- `internal/catalog/handler_test.go` `TestLibraryJSONIncludesNullLastScanVersion` — calls private `libraryJSON`: rewrite to the library HTTP response (F9)
- `internal/catalog/handler_test.go` `TestLibraryItemsMaterializeBeforeNestedDTOQueries` — asserts only `len==3`, name states implementation: rewrite to assert the response body or remove
- `internal/catalog/handler_test.go:68,308` `TestBookItemSerializersMatchPinnedSourceGolden`, `TestBookProjectionValues` — re-derive via test-local `lastFirst`/`number` (`:483,611,674`): rewrite with literal expected values; drop helpers when `catalog.lastFirst/number` go
- `internal/catalog/handler_test.go` series tests (`:1459,1498,1554`) vs `internal/catalog/series_routes_test.go` `TestSeriesFindOneProgressAndFeed` — two sets for two implementations: merge into one set over HTTP (F10)
- `internal/catalog/handler_test.go:1841` `Handle` helper calls `Routes(mux, RouteDeps{})` per request: replace with a constructed handler (F20)
- `internal/catalog/query_test.go` `TestLibraryQueryTable`, `internal/catalog/personalized_golden_test.go`, `internal/catalog/item_golden_test.go` — goldens regenerated by the code (`UPDATE_QUERY_GOLDEN=1`): keep with a pinned TS-sourced fixture, otherwise rewrite to behaviour cases
- `internal/catalog/etag_representation_diagnostic_test.go`, `opml_representation_diagnostic_test.go`, `internal/textclean/html_representation_diagnostic_test.go`, `internal/server/error_representation_diagnostic_test.go`, `internal/httputil/etag_representation_diagnostic_test.go`, `ts_representation_diagnostic_test.go`, `internal/logging/ts_representation_diagnostic_test.go` — `//go:build tsdiagnostic`, never run by `go test ./...`; R01 asserts `ExpressETag(body)==ETag` (implementation echo): remove all or fold into the default suite
- `internal/catalog/library_routes_test.go` `TestOPMLSemanticComparison`, `TestGenerateOPMLMatchesUpstream` — three tests of one generator: keep one
- `internal/wire/shelf_test.go` `TestShelfEncodesLikeTheMapItReplaced` — compares to a map that no longer exists: rewrite to a literal expected JSON
- `internal/filterdata/` — no tests: add behaviour tests (TTL, Patch) with F11
- `internal/catalog/relations_characterization_test.go:49-52,104` — reads private `rig.h.filters`: rewrite via the filter-data endpoint; `TestItemMutationRelationsAcrossRoutes` order-insensitive: assert sweep order once F10 fixes it
- `internal/catalog/cover_test.go` `TestRemoveOldCoversUsesPinnedExtensionAllowlist`, `TestSniffCoverTypeMatchesPinnedImageTypeHeaders` — unexported helpers: rewrite through the cover routes (F10)
- `internal/catalog/book_batch_test.go:164-178` — tests `titlePrefix` wrapper via raw settings rows: rewrite via the API
- `internal/itemwrite/sweep_test.go` `openDB` — hand-written schema copy: use the real schema; add `LinkAuthors/LinkSeries/Rules` tests (F10)
- `internal/textdist/textdist_test.go` `TestLevenshteinFoldIgnoresCase` — restates `ToLower`: remove; `internal/textsort/textsort_test.go` `TestPlaylistNameOrder` belongs in `lists`: move
- `internal/nameparse/nameparse_test.go` `len(golden) < 40` guard — weak: remove guard
- `internal/textclean/golden_test.go` `TestSanitizeDifferences`, `TestStripDifferences` — pin documented deviations: keep only if each deviation is a contract; else remove
- `internal/userapi/userapi_test.go` playlist/collection tests (`TestPlaylist*`, `TestCollection*`, `TestEmptyCollectionName...`), `list_read_test.go` `TestListReadValuesAndPermissions`, `playlist_order_test.go`, `playlist_collection_regression_test.go`, `framing_contract_test.go` `TestListWritesPersistAndEmit` — test `lists` from the wrong package: move to `internal/lists`
- `internal/userapi/userapi_test.go` `TestRouteMissRemainsUnhandled` (tests harness `Handle`), `TestUserDTOUsesUpstreamNullAndArrayShapes` (private builder; asserts absence of `isOldToken` which no code sets), `TestProgressRowsParsesSequelizeTimestamp` (exported-for-test wrapper): remove / rewrite via HTTP; delete `userAPIForTest` router
- `internal/userapi/parsedate_test.go` `TestParseDateStringReadsEveryStoredSpelling` and `internal/identity/nullabledate_test.go` `TestNullableDateScanReadsEveryStoredSpelling` — duplicate input tables, re-test `dbtime.Parse`: keep one in `dbtime`, remove both
- `internal/access/access_test.go` `TestDecisionsMatchRecorded` — golden rewritten by `ACCESS_RECORD=1`: replace with explicit cases; `TestExplicitNeedsActiveUser`, `TestEmptyAllowListHidesEverything`, `TestContentAllowlistPartialOverlap` overlap it: keep the explicit ones; keep `TestMediaClauseMatchesContent`
- `internal/auth/token_test.go` `TestSignMatchesPreviousBytes` — pins library JSON key order: rewrite to claims round trip; `TestGoIssuedTokenRoundTrip` fine
- `internal/serversettings/serversettings_test.go` `TestForgetDropsTheEntry` — private `entries`: remove with F3; `TestCacheServesUntilInvalidated` — rewrite to `Save` invalidates
- `internal/identity/identity_test.go` `TestLegacyAccessTokenIncludesJWTIssuedAt` — private `issueLegacyToken`: rewrite via login response
- `internal/identity/tz_test.go` `TestHostTimeZone` (cannot fail), `TestICUZoneName` (repeats generated table): remove
- `internal/identity/ratelimit_test.go` four limiter tests via private maps: rewrite through the auth route; `oidc_test.go` `TestPruneOIDCStateMatchesSessionAndMobileRedirectLifetimes`: rewrite via callback behaviour
- `internal/ids/ids_test.go` `TestNewReturnsVersion4UUIDs` — 1000-draw collision check cannot fail: drop `seen` check
- `internal/jsonvalue/value_test.go` — trivial type switches: keep, extend to all coercions (F5); add `internal/progressdto` tests
- `internal/store/nullable_test.go`, `nullable2_test.go` (`TestNullStringMapsInvalidToNil`, `TestNullIfEmptyMapsEmptyToNil`, `TestNullInt64AndNullIfZero`, `TestNullIfBlank`, `TestNullStringPtrAndStringPtrValue`) — restate one-liners: remove (`NullIfZero` test goes with the function)
- `internal/store/json_test.go` `TestDecodeJSONSlice`, `TestDecodeJSONSliceMaps`, `TestDecodeJSONObject` — assert `json.Unmarshal` leniency: keep one malformed-input case
- `internal/store/store_test.go` `TestNullableMillisTreatsNumericDatesAsMilliseconds` — tests `dbtime`: remove; `TestReadOnlyReadsUpstreamModels`, `TestLibrariesMatchPinnedUpstreamGolden`, `TestOpenReadOnlyDoesNotCreateAndRequiresSchema`: remove with F21
- `internal/server/server_test.go` (6 tests) — read-only `Handler`: remove with F21
- `internal/server/runtime_test.go` `TestRuntimeMatchEpisodesFetchesFeedAndMatches` — podcast behaviour through `newRuntime`: move to `podcasts`/`scanner`; `internal/server/logging_test.go` `TestSetLogListenerDeliversLogEvents` skips without node socket.io-client (disabled): run in CI or remove; `TestLoggerDataEndpointThroughRuntime` fine
- `internal/server/recover_test.go` `TestAccessLineIsDebugOnly`, `TestLivenessProbesAreTraceOnly` — assert exact log prefix: assert level only
- `internal/server/runtime_client_test.go` `TestServeClientDynamicRoutesAndUnknownPath404`, `TestMissingClientAssetMatchesExpressFinalhandler` — full Express HTML bytes (ADR-002: framing not contract): assert status and content type
- `internal/httputil/error_test.go` `TestWriteErrorSendsMessageAsExpressText`, `TestWriteErrorFallsBackToStatusText`, `TestErrorJSONWritesErrorProperty` (first two cover dead `WriteError`), `sendtext_test.go` `TestSendTextContentType` (dead `ContentType`): remove; `text_test.go` `TestOpaqueETagSyntax` (test-local helper): remove; `json_test.go` `TestWriteJSONFramesLikeExpress` (byte framing): relax to status/header/value
- `internal/logging/logging_test.go` `TestParseLevel` (dead `ParseLevel`): remove; `TestConsoleLineFormat`, `TestConsoleStreamPerLevel` (exact bytes): assert level/stream; `TestConcurrentLoggingIsRaceFree` (no assertion): keep only under `-race` with a count assertion
- `internal/dbtime/dbtime_test.go` `TestSQLNullBindingStaysNullInDatabase` (exercises the driver): move to `store`/remove; `TestNowUsesCanonicalLayout` repeats layout: remove; `dbtime/millis_test.go` `TestTimeAndParseUnixMilli`: remove with `ParseUnixMilli`
- `internal/config/config_test.go` `TestNormalizeBasePath`, `TestLoadRouterBasePathAndPort` — pin odd outputs by repeating function; use `t.Setenv` instead of manual unset (`:153-163`): rewrite
- `internal/adminops/handler_test.go` `TestSortingPrefixesRewritesAllTablesOnOneConnection` — pool-size regression tied to convention: keep as behaviour test (rows rewritten)
- `internal/notifications/handler_test.go` `TestApprisePayloadPreservesMissingAndNullTemplates`, `TestNotificationTestPostsRenderedPayloadToLocalApprise` — payload map internals: rewrite via the received Apprise request
- `internal/emailapi/handler_test.go` `TestCanAccessContentTagMatrix` — re-tests `access.Content`: remove
- `internal/compat/compat_test.go` (9 tests) — with `cmd/abs-compat` decision
- `internal/db/schema_test.go` `TestSchemaIsPinnedContract`, `TestSQLCSchemaIsDerived` — keep (drift guards for a manual copy)
- `tools/ratchet/main_test.go`, `simplification_test.go` — keep; update metric names with the ratchet cleanup
- `internal/contracttest/access_invariant_test.go`, `permission_revocation_test.go` (uses `access.Decodes()`), `libitem_equivalence_test.go` (calls `ItemMinifiedJSON`) — assert implementation shape: rewrite to HTTP values; `journey_record_test.go:52` comment (F21)
- `internal/podcasts/lifecycle_test.go` `TestCloseStopsSchedulerAndCancelsCurrentDownloadIdempotently` — builds private fields, asserts the cron entry was "unexpectedly removed" (inverted): rewrite to assert no scheduled run after Close
- `internal/podcasts/downloads_test.go` `TestDownloadCompletionStatusPollingIsSynchronized` — private fields, no assertion beyond `-race`: rewrite to assert final status via the route
- `internal/podcasts/register_adapter_test.go` — helper-only file used by `register_golden_test.go:68`: merge into that file
- `internal/podcasts/rss_differences_test.go`, `rss_golden_test.go`, `selection_golden_test.go`, `register_golden_test.go` — private `parseFeed`/`filterNew`, goldens regenerated with `-update`: keep with pinned fixtures, relabel; `feed_encoding_test.go` `TestDecodedBodyUnwrapsContentEncoding`: rewrite via fetch
- `internal/providers/isodate_test.go`, `book_finder_test.go` `TestCleanForCompares`, `TestTitleCandidatesOrdering`, `playback_test.go` `TestTranscodeAudioCodecMatchesPinnedStreamPolicy` — private helpers: rewrite to behaviour or remove; `providers/characterisation_test.go` `TestProviderOutputsGolden` (`UPDATE_GOLDEN`): split per provider
- `internal/playback/golden_test.go` `TestSessionGolden` (`-update-golden`), `persisted_state_test.go` tests (private `h.db`) — keep behaviour; golden pinned to TS fixtures
- `internal/realtime/hub_test.go` `TestNewRegistersLegacyAndPrefixedSocketPaths`, `TestUsersOnlineCallsPlaybackLookupOutsideHubLock`, `TestPayloadAccessibleApplies...`, `TestPayloadForUserFilters...`; `sio_test.go` `TestParsePacket`, `TestEncodeArrayDoesNotEscapeHTML` (replaces `connectFn`) — private seams: rewrite over a socket client or remove with F18; `hub_test.go:279` read-only store (F21)
- `internal/lists/collections_test.go` `TestCollectionReadValues`, `lists/reader_test.go` `TestPlaylistReadValues` — call package-internal readers: keep, but unexport `Playlist`/`Collection`
- `internal/sharing/handler_test.go` `TestShareDatesAreISOStrings`, `TestOpenFeedKeepsNullColumnsNull` — read DB rows after handler calls: schema-level, keep
- `internal/scanner/bench_match_test.go` `TestBenchLibraryOldAndNewAgree` — oracle is a verbatim copy of the deleted quadratic matcher: remove (keep `BenchmarkMatch`)
- `internal/scanner/matcher_test.go` `TestGroupMatcherWorkIsLinear` (counter `visits` only for test): replace with a benchmark; `TestGroupMatcherRules`, `TestGroupMatcherPathBeatsInodeAndClaimedGroupsAreSkipped` (private types): rewrite through scan
- `internal/scanner/plan_test.go` six `TestPlan*` — assert private flags restating `plan()`: rewrite through scan results
- `internal/scanner/scanner_test.go` — `TestAudioMetadataSourceMapping`, `TestReaderAndDescriptionSourceRules`, `TestFolderExtractorsSourcePatterns`, `TestFolderStructureExtractorOrderAndOptionalSubtitle`, `TestFilenameTrackDiscParsingMatchesUpstream`, `TestFilenameTrackParsingRemovesOnlyFirstBookTitleOccurrence` (passes a `title` production never passes), `TestSmartAudioOrder...`, `TestBuildChaptersMetaTitleAndSingleFileRules`, `TestParseOPFMetadata`: unexported helpers with hand-built maps; keep those with upstream-sourced cases, rewrite the rest via scan; `TestScanCancellationAndRouteOwnership`, `TestScannerDiscoveryRepairCharacterization` (internals of walker): rewrite to behaviour; `TestScanImportsFFmpegMediaAndSidecarsIdempotently` overlaps `scan_golden_test.go:TestScanGolden`: remove one; the six `t.Skip("ffmpeg/ffprobe unavailable")` tests (`:839-1208`): switch to `fakeFFprobe` so they run without ffmpeg (disabled-test); `TestWatcherDiscoversNewNestedFoldersAndCloseStopsIt`, `TestCronScheduleRunsLibraryScan` mutate package vars (`:1559-1561`): inject timings via constructor (F20)
- `internal/scanner/import_failure_test.go` — failure injected via DB triggers named after current columns: keep, document coupling
- `internal/scanner/scanner_events_test.go` `TestScanSequentialAuthorTimestampsAndCounts` sleeps for ordering: use fake clock
- `internal/audiotools/tools_test.go` `TestFFMetadataEscapingAndChapterOrder` (implementation echo), `TestCancelEncodeKillsConfiguredFFmpeg` (private `task`, skips the route): rewrite through `cancelEncode`; `TestMergeCreatesM4BAndKeepsSourceBackup` skips without ffmpeg; no tests for `embed`, `batch`, queue, `Tasks`, `loadItem`: add
- `internal/backup/backup_test.go` `TestMissingBackupUsesExpressSendStatus`, `TestUploadInvalidExtensionUsesExpressSend` (exact framing, ADR-002): relax; `TestUpstreamCreatedBackupImports`, `TestExportNodeFixtureDatabaseForNodeImport` (skip unless `ABS_BACKUP_*`; never run): vendor a small fixture or remove; `TestCreateNotifiesBackupLifecycle` (exact payload map): assert event names; add tests for `UpdatePath`, `delete`, duplicate-ID `Reload`
- `internal/bootstrap/bootstrap_test.go:78 TestEnsureReadOnlyMissingDatabaseDoesNotCreate` — F21 removal
- `internal/server/manager_test.go:123` `t.Skipf("symlink unsupported")`, `internal/contracttest/journey_test.go:185` step skips — keep (environment conditions)

## Rejected survey claims

- media/dead `internal/podcasts/rss.go:70` `numText` "check callers": used by `enclosure.Length` (`rss.go:66`, `:176`) and the download path — not dead.
- media/dead `internal/lists/reader.go:39,106` `Playlist`/`Collection` "no caller outside lists": used in-package at `lists/handler.go:54`, `library_http.go:146`, `playlist_http.go:31,62,157,291`; only the exports are unnecessary (unexport).
- infra/speculative `internal/store/store.go:324` `NullInt64` "1 caller": many callers (`lists/collection_http.go:476`, catalog, others); `StringPtrValue` also has 7 prod uses. Only `NullIfZero` is dead.
- infra/dead `internal/server/server.go` "`isGet` used by runtime only": true, but it is not dead; it moves with F21 (kept).
- catalogA/vocabulary `catalog/handler.go:164` `number` "exists only for tests" is right but `lastFirst/number` "move to test" conflicts with F4 (use nameparse directly in tests).
- catalogA/dead `catalog/handler.go:1058-1064` rssFeed attach and `:1157` `payload["rssFeed"]=nil` — the survey itself says fine; not a problem.
- catalogA/speculative `emitFunc` alias "next to `New(..., emit func)`" — only the field uses it; folded into F6, not a separate defect.
- catalogA/dead `filterdata.Cache.Has` as unused — used by `catalog/handler.go:235` and `scanner/scanner.go:238`; a duplicated guard, not dead.
- catalogB/wrapper `itemwrite` `LinkAuthors/LinkSeries` "thin builders" — the survey itself says keep; not a problem.
- catalogB/dead `Changes.Merge`, `AuthorsAdded/SeriesAdded` — used (`catalog/batch.go:262`, `Announce`); not dead.
- catalogB/shallow `textsort` global mutex — `collate.Collator` is not goroutine-safe; required.
- catalogB `catalog/quickmatch.go:27-32` `BookFinder`/`AuthorFinder` "used only for tests" — low confidence per surveyor; fakes exist (`quickmatch_test.go:25`), seam is intentional.
- catalogB/disabled-test `catalog/etag_representation_diagnostic_test.go` "if tag-gated; confirm" — confirmed tagged; recorded in Tests.
- identity/dead `userapi.go:1161` `int64Pointer` — used at `userapi.go:1156`; not dead (the `realtime/hub.go:763` one is a separate single-use wrapper).
- identity/dead `userapi.go:1145` `scanUser` parts / `itemAccess.exists` — `scanUser` and `exists` are both read; not dead.
- identity/dead `userapi/listening.go:158` `minInt` — real (one use), builtin `min` exists with go 1.27: kept as wrapper (not rejected); survey wrote "Go 1.27 (go.mod:3)" correctly.
- identity/dead `server/server.go:149-160` "`unsupported(w)` after every branch" — mixed: `/api/libraries` branch is live; `_ = claims` is the dead part. Handled by F21.
- identity/copy "`jsonvalue.Truthy` used only by access/userapi" — true but irrelevant; F5 stands.
- infra/dead `internal/emailapi/handler.go:32-40` field `cfg` "never read" — unverified beyond `_ config.Config` in notifications; kept under F15 as speculative only.
- infra/layout `internal/store/store.go:99-296` "`Libraries`, `libraryFolders` serve only the read-only handler" — true except tests; goes with F21 (note `store.libraryFolders` at `:366` is separate from `catalog.libraryFolders`).
- infra/convention `ratchet-baseline.json` "41020 handwritten lines" — a metrics file, not a code problem; kept only as convention note.
- scanner/dead `scanner/files.go:1141` and `:848-855` `audioFilename` — surveyor marked "fine"; not a problem.
- scanner/dead `scanner/files.go:45-56` `bookMetadata.Chapters` "only one producer" — a producer count, not dead; rejected.
- scanner/copy `backup/backup.go:262-264,352` "VACUUM INTO path assumed in each" — a SQLite fact, not a copy; `absdatabase.sqlite` joins are covered in F19.
- media/dead `realtime/hub.go:76` `cfg` — verified true (zero `h.cfg` reads), kept in F6; media survey's own "speculative: `Hub.cfg` stored" duplicates it (merged).
- media/copy "`sharing/handler.go:55-58` `New` assigns then returns" — cosmetic, kept as wrapper not a defect.
- media/disabled-test `podcasts/register_adapter_test.go` "no test" — helper-only file confirmed; recorded in Tests, not an error.
- Cross-survey duplicates merged: image downloader (scanner+catalogB+media), ffmpeg/ffprobe env readers (scanner+media+catalogB), settings-row readers (identity+infra+scanner+catalogA), `TitleIgnorePrefix` copies (catalogA/B+infra+scanner), JS truthiness (catalogB+identity+infra), Task definitions (media+scanner), version literal (media+scanner+infra), `dbNow`/`sqlDate` wrappers, `requestUser` copies.

## Progress

### Waves A + B (2026-11-08)
Done: F3, F4, F5, F17 (foundations); F21 (read-only mode removed), compat deleted, F19 (store owns the database);
F1 (httputil.SafeClient + DownloadImage; podcasts switched), F8 (wire types moved to their single users in sharing,
lists, podcasts, playback, catalog), F9 (catalog `inLibrary` prologue; `httputil.OpenLibrary` for lists/podcasts),
F10 (typed library/series/author views, `finishItemWrites`, `itemUpdated`, covers), F11 (`filterdata.Load`),
F18 (catalog handler.go, playback.go, realtime hub split by feature), F2 (`internal/mediatools`, scanner and
audiotools switched), F12 (`internal/filekind`), F13 (scan lifecycle), F7 part (`internal/users` DTO builders,
`access.Defaults`/`Patch`, `deviceinfo.Parse`, `store.UpsertDevice`), F14 part (`auth.Issue*`, `ValidateBearer`/
`ValidateRefresh`; userapi takes identity as `Credentials`, no secret or hooks), F16 items in those packages.
Dependency fixes made at integration: user DTOs in `internal/users` (no feature cross-import), device upsert in
`store` (not in the `deviceinfo` leaf), `auth` may import the stdlib-only `ids` leaf.
Bugs fixed (TS proof in each worker's notes): refresh tokens accepted as bearer tokens (TokenManager.js:81-84);
podcast schedule edits not rescheduling (LibraryItemController.js:276; `podcasts.Reschedule`); admins exempt from
podcast library checks (User.js:647); episode downloads cut off after 20 s; SSRF check of the first resolved
address and first host only; scanner audio tags read with a second ffprobe that ignored FFPROBE_PATH (tags now
from the first probe: AudioFileScanner.js:245,277); scanner last-first author names (BookScanner.js:237,539);
empty sortingPrefixes means none; missing-marking writes only isMissing (LibraryScanner.js:195,590); watcher rescan
keeps ebooks like a full scan.
Ratchet: 41,020 → 39,324 handwritten lines; complexity 9,593 → 8,944; map[string]any 678 → 600; []any 259 → 222;
functions above 15 145 → 130; Set* injectors 18 → 17.
Carried to Wave C: see the list below the next heading when it lands.

### Wave C (2026-11-08)
Done: catalog image downloads on `httputil.DownloadImage` (catalog's private-address checks deleted); one library-route
prologue (`httputil.OpenLibrary`, catalog `inLibrary` wraps it); ffprobe/ffmpeg exec sites and MIME tables in catalog,
podcasts, playback and sharing on `mediatools` / `filekind`; scanner on the filterdata API (`AddBook`, `Get`,
`ResetIssues`) with the old mutators deleted; device upserts in playback/sharing on `store.UpsertDevice`; F15
`access.RequireAdmin` in adminops, notifications, emailapi, audiotools, scanner, userapi; backup on `httputil.Handler`
with one options builder, schedule helper and upsert; F14 rest (userapi raw SQL → sqlc; API-key and session lifecycle
in identity); `internal/settingsdoc` owns the email/notification settings rows; `store.User.Email` distinguishes NULL
from ''; progressdto returns typed progress.
Bugs fixed: cover resize ignored FFMPEG_PATH; podcast `.wav` stored as audio/wav (TS AudioFile.mimeType →
audio/mpeg); PATCH user with null/empty email cleared it (UserController.js:312); create user with email "" stores ""
(:174); isActive on create uses JS truthiness (:176); a backup that can't be removed stays listed
(BackupManager.js:393-401); backup_applied after the 200 (:263-266); legacy '' email renders '' (User.toOldJSONForBrowser).
EXP_PROXY_SUPPORT=1: only 301/302 are followed (Server.js:89-104), in the shared SafeClient.
Ratchet: 39,324 → 38,906 handwritten lines; complexity 8,944 → 8,805; map[string]any 600 → 582; []any 222 → 212.
Still open: F6 typed events, F20 composition root (constructor wiring; runtime notes below), mediatools availability
check (playback.ffmpegExecutable), podcasts/providers admin wrappers → access.RequireAdmin, clientIP copies
(sharing/playback), backup.New's unused db, comments pass, tests pass, verifier loop.

### Wave C4 (2026-11-08)
F6: `events.Emitter` with explicit audiences (`ToUser`, `ToAdmins`, `ToAll`) and task helpers; a nil emitter discards,
so features never nil-check; only the hub spells "*"/"@admins". One `events.Task` builder for scanner, audiotools,
catalog match tasks and podcast downloads/OPML (payloads unchanged by value); audiotools cancel via a sentinel error.
F20: collaborators are constructor arguments (`Deps` structs for catalog, adminops, userapi; direct arguments for
identity, backup, scanner); realtime gets a `Backend` interface and the `Manager` owns the hub; the podcasts↔scanner
cycle is gone (`podcasts.QuickMatchEpisodes`); `newRuntime` builds in dependency order. Set* injectors 17 → 4 (the
remaining four are a cookie write and three test seams, left to the Tests pass).
Leftovers done: `mediatools.FFmpegAvailable`, podcasts/providers on `access.RequireAdmin`, `httputil.ClientIP`/`PeerIP`.
Bug fixed: `user_added`/`user_removed` went to the audience "admin", which no socket joins; they now go to admins
(UserController.js:187,402 adminEmitter).
Risk noted: `server` TestSocketConnectAuthLoop timed out once under heavy load (22/200 sockets); 0/4 failures under load
on both this tree and the previous commit when re-run.

### Comments pass (2026-11-08)
About 300 comment lines removed or rewritten across the codebase: comments that restated the implementation, doc
comments that repeated the declaration, and stale notes naming removed workarounds, packages or roadmap packets.
Kept: TS/upstream source citations, client/OS/SQLite/ffmpeg/Express facts, contract notes, owner decisions, package
docs. Checked mechanically: every changed Go file parses to the same syntax tree with comments stripped (only
blank-line/alignment differences where comments were removed). Handwritten lines 38,622 → 38,361 (comment lines
count in the ratchet).

### Verifier round 1 and fix wave D (2026-11-08)
Six read-only verifiers (catalog, scanner, identity, media, infra, cross-cutting) reported ~40 issues worth fixing;
the graph was a clean DAG. Fixed: one item-deletion cascade (`deleteItemRows`) with the nil-user / forged-user
permission modes removed (routes authorize first); one item lookup (`loadItem`/`itemFor`) for nine handlers;
`libitem.FileType` follows TS `LibraryFile.js:42-49`; one `textsort.SequenceLess`; one feed view (`internal/feeds`,
replacing `feedjson`, `sharing.FeedMinified` and the lists/sharing copies); `serversettings` builds the browser settings
document (one secret-key list, one env overlay); one user-creation path in identity; one progress decoder; one e-reader
visibility rule (`settingsdoc`); `users.OnlineUserDTO` for realtime and playback; one episode delete in podcasts;
indexed open-session lookup; one session media reference; UUIDs on `ids` (TS uuid regex); one typed audio file
(`libitem.AudioFile`) for fresh and stored entries; `store.Open` validates schema and migration version for startup and
restore; `store`/`logging` no longer import `wire`; one JSON writer; notification delivery short-circuits; adminops
errors returned; one Express error page; depguard layers leaf/foundation/feature.
Bugs fixed (TS proof in worker notes): forced rescan lost embedded chapters and new files were not re-ordered
(regression tests added first); GET settings sent "UTC" instead of the host zone (ServerSettings.js:307); e-reader
visibility narrower than MeController.js:609-610; series sequences "10" before "2" (seriesFilters.js:200-206);
collection rssFeed shape (Collection.js:44-47, Feed.js:645-676); feed entityType/preventIndexing null for NULL
columns (Feed.js:677); UUID validation (uuid regex.js:7); double authors_num_books_updated on forced item scan
(LibraryItemScanner.js:55-58).
Rejected with evidence: catalog library-file delete updating numEpisodes (TS doesn't, LibraryItemController.js:1035-1053);
unifying batch/single delete sweep order (both match TS); auth.Claims fields and access.Decodes (tests rely on them).
Ratchet: 38,361 → 37,831 handwritten lines; complexity 8,706 → 8,487; map[string]any 562 → 460; functions >15 124 → 115.
Open for the owner (not refactor): podcast notification events are never fired.

### Verifier round 2: OIDC fix (2026-11-08)
`identity/oidc.go`: a failed user re-read after applying OIDC groups/permissions returned an empty user with a nil error,
so the login could issue a session for a blank user; it now fails the login. Ratchet raised by the fix itself (+4 lines,
+2 complexity: the two error checks).

### Fix wave E1 + E2 (verifier round 2, 2026-11-08)
Fixed: watcher batches go through the scan lifecycle (`beginWait`: queue behind a running scan; Cancel/Close stop them);
library settings decoded once; one default metadata precedence (`serversettings.DefaultMetadataPrecedence`);
UpdateWatchedPath queues to the watcher like TS (MiscController.js:593-612) instead of a forced full scan; backup
defaults in one place; `config.MaxFailedEpisodeChecks` (Server.js:119); one filter-value decoder (port of
libraryFilters.decode; all clients checked base64-encode values: official app, Absorb; the plain-word fallback only
worked in Go); `Changes.Announce` (no nil-DB Env); `store.Placeholders`, `store.RequestUser` (five copies deleted);
`store.RemoveFromPlaylists` used by item delete, library-file episode delete and podcast episode delete.
Bugs fixed (TS proof): maxBackupSize 0 is unlimited (BackupManager.js:46 `|| Infinity`), not a 100 GB cap; episode
deletes leave no empty playlists or order gaps (Playlist.js:172-208); a filter value with a stray character decodes as
Node does (libraryFilters.js:13-20); watcher batches could race a library scan and ignored Cancel.
Ratchet raised by the bug fixes: +30 handwritten lines, +1 []any (scan-run queueing and playlist renumbering);
complexity −9.
Open for the owner: TS emits playlist_updated/playlist_removed when deletes change playlists; Go does not.

### Fix wave E3 (2026-11-08)
`config.Config.Dirs()` owns the on-disk metadata layout (items, authors, cache/covers|images|items, streams, backups,
logs); ~25 literal sites in catalog, scanner, backup, restore, playback, audiotools, adminops and logging switched; paths
unchanged. Error-returning handlers answer JSON through `httputil.JSON` (ETag/304 on GET 200 per ADR-001/002);
`WriteJSON` keeps four non-handler callers. `store.Placeholders` in access, libitem, scanner and adminops.
adminops `WatcherUpdate` is the scanner's queueing function directly (no 500 branch).

### Owner-approved features (2026-11-08)
1. Podcast notifications: onPodcastEpisodeDownloaded (auto-downloads; PodcastManager.js:257-261, NotificationManager.js:21-46),
   onRSSFeedFailed (PodcastManager.js:351, NotificationManager.js:81-98) and onRSSFeedDisabled (PodcastManager.js:346,
   NotificationManager.js:100-117) through `notifications.TriggerEvent`. Bug fixed: onBackupCompleted sent
   removedOldest as a bool the template skips; now "false" as NotificationManager.js:62.
2. Playlist events on deletes: `store.RemoveFromPlaylists` returns what changed; `internal/playlistview` (moved out of
   lists, shared without feature cross-imports) announces playlist_removed / playlist_updated to the playlist's user
   after commit (Playlist.js:172-208) from item delete, library delete, library-file episode delete and podcast episode
   delete/trim. Pending: scanner episode removal (PodcastScanner.js:80).
Ratchet raised for the new behaviour: +121 handwritten lines, +15 complexity, +5 map[string]any, +2 []any.

### Verifier round 3 and fix wave F (2026-11-08)
Round 3 (six read-only verifiers): media, scanner and cross-cutting had nothing worth fixing; catalog, identity and
infra had small leftovers; clean DAG, no unreasonable imports, no special case without a contract reason.
Fixed: one `store.DeleteEpisodes` (progress, playlist cleanup, episode rows) for catalog library-file delete, podcasts
deleteEpisode and the scanner; `serversettings.AuthKeys` is the one list of auth setting keys; adminops `sqlDate`,
httputil `Option`/`ForRequest`/`ErrorJSON` and five dead sqlc queries deleted; `/status` uses `CountRootUsers`.
Bug fixed (TS proof): the scanner never removed podcast episodes whose audio file was deleted (orphan episode rows,
progress and playlist entries); it now matches removed files by path then inode and deletes them with playlist
events after commit (PodcastScanner.js:66-102, :80). Ratchet raised by that behaviour: +13 lines, +5 complexity,
+1 []any.
