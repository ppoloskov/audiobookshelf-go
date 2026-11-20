# Absorb inventory
- Repo: https://github.com/pounat/absorb  Commit: 8411ffc75cb3c179ceb697d4fad605a8098ca74b  Platform: Android + iOS (Flutter; also a CarPlay/Android Auto/Wear surface in the same code)  Language/stack: Dart/Flutter, package:http 1.6.0, socket_io_client 3.1.4 (socket_io_common 3.1.1), cached_network_image 3.4.1, flutter_cache_manager 3.4.1, background_downloader 9.5.5, vendored just_audio fork (packages/just_audio)
- Permalink base: https://github.com/pounat/absorb/blob/8411ffc75cb3c179ceb697d4fad605a8098ca74b/

Scope notes:
- The same codebase also ships a second backend, BookOrbit (lib/services/bookorbit_api_service.dart, 6327 lines, plus bookorbit_*.dart). It is selected by `ServerBackend` (lib/services/server_backend.dart:1-48). Its paths are NOT Audiobookshelf paths and are excluded from the table below. Where ApiService delegates to it, the ABS paths listed here are not used (lib/services/api_service.dart:326-362, 779, 1143-1176, 1219-1221).
- Several non-ABS endpoints are also called (Audnexus, Audible catalog, GitHub, RMAB, WebDAV settings sync, Wiktionary, Hugging Face). They are listed in the HTTP table under "non-ABS" rows so they are not confused with ABS.
- Paths in the table are the literal path appended to the user-entered server URL. `_cleanBaseUrl` only normalizes the URL (scheme added if missing, whitespace removed, trailing slashes stripped): lib/utils/server_url.dart:1-10, lib/services/api_service.dart:646. No `/api` prefix is added by the client; every ABS path is written with `/api/...` (or `/auth/...`, `/login`, `/logout`, `/status`, `/ping`, `/public/...`, `/socket.io`) literally. A router base path such as `/audiobookshelf` therefore has to be part of the user's server URL. The client does not special-case it, except in `buildTrackUrl` and the socket path (below).

## 1. API base
- Base URL is the user-entered server URL: `ApiService.baseUrl` (lib/services/api_service.dart:261, 308), normalized by `_cleanBaseUrl => normalizeServerUrl(baseUrl)` (lib/services/api_service.dart:646). Normalization in lib/utils/server_url.dart:1-10 (adds `http://` if no scheme, strips whitespace and trailing `/`). CONFIRMED.
- Login builds `<serverUrl>/login` by appending `login` to the trailing-slash-normalized URL (lib/services/api_service.dart:1083-1085); status/ping/refresh/logout use the same pattern (lib/services/api_service.dart:1147-1149, 1177, 1222-1224, 735, 1569). CONFIRMED.
- Router base path (`/audiobookshelf`): not special-cased for requests. Tracks: if a server `contentUrl` already starts with the base path, the origin is used instead of concatenating (lib/services/api_service.dart:2291-2305). Socket: path = `<basePath>/socket.io` on origin `scheme://authority` (lib/services/socket_service.dart:62-71). CONFIRMED.
- Paths are not prefixed with `/api` by the client; they are literal. CONFIRMED.

## HTTP calls
Status: all rows CONFIRMED unless marked INFERRED. Request fields = JSON body keys unless noted. Auth header on every `_auth*` call is `Authorization: Bearer <token>` (see Auth). Line ranges are the function, with the URL/body lines in the range.

| Method | Path | File:lines | Request fields | Response fields read | Status |
|---|---|---|---|---|---|
| GET | /ping | lib/services/api_service.dart:1143-1157 (pingServer), 1165-1187 (pingServerDetailed, URL 1177) | none (customHeaders only) | HTTP status 200 only | CONFIRMED |
| GET | /status | lib/services/api_service.dart:1219-1236 (getServerVersion); lib/services/oidc_service.dart:418-446 (auth config) | none | `serverVersion`; `authMethods` (contains 'openid'/'local'), `authFormData` (oidc_service.dart:438-460) | CONFIRMED |
| POST | /login | lib/services/api_service.dart:1067-1111 (URL 1083-1085, headers 1090-1095, body 1096) | username, password; header `x-return-tokens: true` | `user.accessToken`, `user.refreshToken`, `user.token` (legacy), top-level `accessToken`/`refreshToken`, `serverVersion`, `serverSettings`, `ereaderDevices`, `user` (lib/services/auth_tokens.dart:14-27; lib/providers/auth_provider.dart:813-870) | CONFIRMED |
| POST | /auth/refresh | lib/services/api_service.dart:734-741 (`_sendRefresh`); used by 743-770, 810-899 | header `x-refresh-token`; `User-Agent` (no body) | same as login: `user.accessToken`/`user.refreshToken` or top-level (auth_tokens.dart:14-27) | CONFIRMED |
| POST | /logout[?allDevices=1] | lib/services/api_service.dart:1563-1580 | header `x-refresh-token` (no body) | 2xx = ok | CONFIRMED |
| POST | /api/authorize | lib/services/api_service.dart:1447-1460 (`authorize`); called from lib/providers/auth_provider.dart ~618-625 (cold-start restore) and ~714 | none | `user`, `userDefaultLibraryId`, `serverSettings`, `ereaderDevices`, `Source`, `serverVersion` (per comment 1443-1446, auth_provider.dart:179) | CONFIRMED |
| GET | /api/me | lib/services/api_service.dart:1463-1477 (`getMe`); 786 (`_checkRevokedApiKey`); 1125-1131 (`loginWithApiKey`, Bearer = API key); fallbacks 1492, 1515, 3164 | none | whole user object: `mediaProgress`, `bookmarks`, `ereaderDevices` (dropped by /me per comment 1446), `type` (auth_provider.dart:247), `id` | CONFIRMED |
| GET | /api/me/sessions?page=&itemsPerPage= | lib/services/api_service.dart:1525-1548; delete 1550-1561 | header `x-refresh-token` (sendRefreshTokenHeader) | `sessions`, `total`, `numPages` (AuthSessionsResult, parse at 1525-1548) | CONFIRMED |
| DELETE | /api/me/sessions/:sessionId | lib/services/api_service.dart:1550-1561 | none | 2xx | CONFIRMED |
| PATCH | /api/me/password | lib/services/api_service.dart:1582-1648 (body 1596) | `password` (current), `newPassword`; header `x-refresh-token` | 200 empty (older) or JSON with `error` (400 message) or tokens (`accessToken`/`refreshToken`) | CONFIRMED |
| GET | /api/me/listening-stats | lib/services/api_service.dart:1651-1662 | none | whole object | CONFIRMED |
| GET | /api/me/progress | lib/services/api_service.dart:1480-1500 (`getAllProgress`); fallback GET /api/me on 404 | none | `mediaProgress[]` (also from /api/me fallback) | CONFIRMED |
| GET | /api/me/progress/:itemId | lib/services/api_service.dart:2635-2650 (`getItemProgress`); callers in lib/providers/_lp_core.dart:1483, lib/services/audio_player_service.dart:2017, 4377, 4899 | none (path: podcast compound id split at 36 chars, 2639-2641) | `id`, `lastUpdate`, `currentTime`, `progress`, `isFinished`, `startedAt`, `ebookLocation`, `ebookProgress`, `ebookUpdatedAt` (per comment 2670-2672) | CONFIRMED |
| GET | /api/me/progress/:itemId/:episodeId | lib/services/api_service.dart:3015-3026 (`getEpisodeProgress`); caller lib/providers/_lp_absorbing.dart:834 | none | progress object | CONFIRMED |
| PATCH | /api/me/progress/:itemId | lib/services/api_service.dart:2789-2824 (`updateProgress`); unfinish path 2800-2805 | `currentTime`, `duration`, `progress` (= currentTime/duration clamp 0..1), `isFinished` (true only, or `{isFinished:false}` alone for unfinish) | status code only (`< 500` = ok) | CONFIRMED |
| PATCH | /api/me/progress/:itemId | lib/services/api_service.dart:2833-2865 (`updateEbookProgress`); raw `http.patch`, NO 401 retry (2853) | `ebookLocation`, `ebookProgress`, `isFinished: true` only when progress >= 1 (2847) | status (`lastEbookProgressStatus`, 2859) | CONFIRMED |
| PATCH | /api/me/progress/:itemId | lib/services/api_service.dart:2764-2776 (`updateProgressFinishedDate`) | `finishedAt`, `isFinished: true` | 2xx | CONFIRMED |
| PATCH | /api/me/progress/:itemId | lib/services/api_service.dart:2704-2761 (`updateProgressStartDate`, recreate step 2733-2749 and restore 2745-2749) | `createdAt`, `isFinished`, `currentTime`, `duration`, `progress`, `hideFromContinueListening`, `ebookLocation`, `ebookProgress`, `finishedAt` (from `_progressRecreationBody`, 2676-2699) | 2xx | CONFIRMED |
| PATCH | /api/me/progress/:itemId | lib/services/api_service.dart:2918-2929 (`resetProgress`, last step) | `currentTime:0`, `progress:0`, `ebookLocation:null`, `ebookProgress:0`, `isFinished:false`, `hideFromContinueListening:true`, `lastUpdate` | 2xx not checked | CONFIRMED |
| PATCH | /api/me/progress/:itemId/:episodeId | lib/services/api_service.dart:3035-3067 (`updateEpisodeProgress`); `zeroEpisodeProgress` 3089-3112 (two PATCHes) | `currentTime`, `duration`, `progress`, `isFinished` (true or false alone) | status (`< 500` ok) | CONFIRMED |
| PATCH | /api/me/progress/batch/update | lib/services/api_service.dart:4945-4963 (`updateLibraryItemsFinished`) | JSON array `[{libraryItemId, isFinished}]` | 200 | CONFIRMED |
| DELETE | /api/me/progress/:progressId | lib/services/api_service.dart:3072-3083 (`deleteMediaProgress`); also 2722-2723 and 2902 (`resetProgress`) | none; `progressId` = media progress record id | 2xx | CONFIRMED |
| GET | /api/me/progress/:progressId/remove-from-continue-listening | lib/services/api_service.dart:2955-2965 | none (GET with side effect) | 200 | CONFIRMED |
| GET | /api/me/series/:seriesId/remove-from-continue-listening | lib/services/api_service.dart:2940-2950 | none (GET with side effect) | 200 | CONFIRMED |
| GET | /api/me/bookmarks | lib/services/api_service.dart:1503-1523 (`getAllBookmarks`); fallback /api/me on 404 (1515) | none | `bookmarks[]` | CONFIRMED |
| GET | /api/me/bookmarks/:itemId | lib/services/api_service.dart:3151-3178 (`getServerBookmarks`); on 404 falls back to GET /api/me filtering `bookmarks[].libraryItemId` (3164-3173) | none | `bookmarks[]` | CONFIRMED |
| POST | /api/me/item/:itemId/bookmark | lib/services/api_service.dart:3182-3191 (`createBookmark`) | `time`, `title` | 200 | CONFIRMED |
| PATCH | /api/me/item/:itemId/bookmark | lib/services/api_service.dart:3195-3204 (`updateBookmark`) | `time`, `title` | 200 | CONFIRMED |
| DELETE | /api/me/item/:itemId/bookmark/:time | lib/services/api_service.dart:3208-3216 (`deleteBookmark`) | none; `time` in path (double, e.g. `12.5`) | 200 | CONFIRMED |
| GET | /api/me/item/listening-sessions/:itemId[/:episodeId]?itemsPerPage=1000 | lib/services/api_service.dart:1666-1686 (`getMyItemListeningSessions`) | query `itemsPerPage` | `sessions[]` (paged variant 1710-1750 uses `itemsPerPage`, `page`) | CONFIRMED |
| GET | /api/me/listening-sessions?itemsPerPage=&page= | lib/services/api_service.dart:1689-1702 | query | `sessions[]`, paging fields | CONFIRMED |
| GET | /api/users/:userId/listening-sessions?itemsPerPage=&page= | lib/services/api_service.dart:1753-1764 | query | sessions | CONFIRMED |
| GET | /api/me/stats/year/:year | lib/services/api_service.dart:4310-4320 | none | year-in-review object | CONFIRMED |
| POST | /api/session/:sessionId/sync | lib/services/api_service.dart:2370-2392 (`syncPlaybackSessionStatus`) | `currentTime`, `timeListened`, `duration` (duration kept for older servers, comment 2382-2384) | HTTP status (200 ok; 404 = session gone, comment 2368-2369) | CONFIRMED |
| POST | /api/session/:sessionId/close | lib/services/api_service.dart:2396-2402 (`closePlaybackSession`) | none | not read | CONFIRMED |
| POST | /api/session/local | lib/services/api_service.dart:2409-2432 (`syncLocalSession`, body 2415-2420); re-POST edit 2572-2582 (`updateListeningSession`) | full session map (`id`, `libraryItemId`, `episodeId`, `mediaType`, `displayTitle`, `displayAuthor`, `duration`, `startTime`, `currentTime`, `timeListening`, `startedAt`, `updatedAt`, `date`, `dayOfWeek`) + `deviceInfo`, `mediaPlayer` ('exo-player' or 'ebook-reader'), `playMethod` (default 3) | 200 = ok; 404/501 = server too old (2427) | CONFIRMED |
| POST | /api/session/local-all | lib/services/api_service.dart:2436-2468 (`syncLocalSessionsAll`, body 2451-2456) | `sessions[]` (each + `mediaPlayer`, `playMethod`), `deviceInfo` | 200; 404/501 = too old | CONFIRMED |
| DELETE | /api/sessions/:sessionId | lib/services/api_service.dart:2622-2631 (`deleteListeningSession`) | none | 200 | CONFIRMED |
| POST | /api/items/:itemId/play | lib/services/api_service.dart:2224-2276 (`startPlaybackSession`; URL 2227, body 2229-2251, startOffset 2252) | `deviceInfo` {clientName 'Absorb', clientVersion, deviceId, deviceName, manufacturer, model}, `forceDirectPlay` (= !forceTranscode), `forceTranscode`, `mediaPlayer:'exo-player'`, `supportedMimeTypes` (13 audio/* values), optional `startOffset` | `id` (session id), `audioTracks[]` (`contentUrl`, `mimeType`, `codec`, `metadata.filename`, `duration`), `playMethod`, `chapters`, `duration`, `currentTime`, `libraryItemId`, `displayTitle` (read by lib/services/audio_player_service.dart:5041-5098, lib/services/download_service.dart:2142-2184) | CONFIRMED |
| POST | /api/items/:itemId/play/:episodeId | lib/services/api_service.dart:2971-3011 (`startEpisodePlaybackSession`) | same body as /play (no startOffset) | same as /play | CONFIRMED |
| GET | /api/items/:itemId?expanded=1&include=progress | lib/services/api_service.dart:3133-3146 (`getLibraryItem`) | none | `media`, `media.metadata`, `media.duration`, `media.audioFiles`, `media.ebookFile`, `media.ebookFormat`, `userMediaProgress`, `libraryFiles` (see api_service.dart:2592-2607, lib/services/download_service.dart:794) | CONFIRMED |
| POST | /api/items/batch/get | lib/services/api_service.dart:1891-1915 (`getLibraryItemsBatch`) | `libraryItemIds[]` | `libraryItems[]` | CONFIRMED |
| GET | /api/libraries | lib/services/api_service.dart:1304-1322 (`getLibraries`) | none | `libraries[]` (`folders[].fullPath`, `folders[].id` used at 4596-4602) | CONFIRMED |
| GET | /api/libraries/:libraryId | lib/services/api_service.dart:1801-1815 (`getLibraryFilterData`) | query `include=filterdata` | filter data (`filterdata` object) | CONFIRMED |
| GET | /api/libraries/:libraryId/personalized | lib/services/api_service.dart:1328-1363 (`getPersonalizedView`) | query `minified=1`, `include` (default `numEpisodesIncomplete`; lib/providers/_lp_core.dart:2248 also `rssfeed`), `shelves`, `limit` | `shelves[]` (or top-level array, or `results`) | CONFIRMED |
| GET | /api/libraries/:libraryId/items | lib/services/api_service.dart:1366-1399 (`getLibraryItems`; query built 1377-1385: page, limit, sort, desc, include=numEpisodesIncomplete, filter (raw, unencoded), minified=0, collapseseries=1) | query as listed | `results[]`, `total` | CONFIRMED |
| GET | /api/libraries/:libraryId/items?filter=series.<b64>&sort=media.metadata.series.sequence&limit=&collapseseries=0 | lib/services/api_service.dart:2139-2182 (`getBooksBySeries`, `getAllBooksBySeries` limit=0), 3346-3348 (`getSeries` page of 100) | query: `filter`, `sort`, `limit`, `page` (3347), `collapseseries` | `results[]`, `total` | CONFIRMED |
| GET | /api/libraries/:libraryId/items?filter=series.<b64>&sort=addedAt&limit=100&page=&collapseseries=1 | lib/services/api_service.dart:3398-3424 (`getSeriesCollapsed`) | query | `results[]`, `total` | CONFIRMED |
| GET | /api/libraries/:libraryId/items?filter=authors.<b64>&sort=media.metadata.title&limit= | lib/services/api_service.dart:1819-1839 (`getBooksByAuthor`) | query | `results[]` | CONFIRMED |
| GET | /api/libraries/:libraryId/items?filter=narrators.<b64>&sort=media.metadata.title&limit= | lib/services/api_service.dart:1963-1985 (`getBooksByNarrator`) | query | `results[]` | CONFIRMED |
| GET | /api/libraries/:libraryId/items?filter=issues&page&limit&sort=media.metadata.title&desc=0 | lib/services/api_service.dart:1405-1431 (`getIssueItems`, `getIssueItemCount`) | query | `results[]`, `total` | CONFIRMED |
| GET | /api/libraries/:libraryId/series | lib/services/api_service.dart:1766-1790 (`getLibrarySeries`) | query `page`, `limit`, `sort`, `desc` | `results[]`, `total` | CONFIRMED |
| GET | /api/libraries/:libraryId/series/:seriesId | lib/services/api_service.dart:3331-3333 (`getSeries` meta, 30s timeout), 3305-3313 (`getSeriesInfo` fallback) | none | `name`, `totalDuration` | CONFIRMED |
| GET | /api/series/:seriesId | lib/services/api_service.dart:3293-3304 (`getSeriesInfo`) | none | series object | CONFIRMED |
| GET | /api/libraries/:libraryId/authors | lib/services/api_service.dart:1843-1862 (`getLibraryAuthors`), 1864-1886 (`getLibraryAuthorsPage`, query limit/page/sort/desc) | query | `authors[]` | CONFIRMED |
| GET | /api/authors/:authorId?include=items,series[&library=] | lib/services/api_service.dart:1987-2007 (`getAuthorById`) | query | `id`, `name`, `description`, `imagePath`, `libraryItems`, `series` | CONFIRMED |
| PATCH | /api/authors/:authorId | lib/services/api_service.dart:2009-2042 (`updateAuthor`) | `name`, `description`, `asin`, `imagePath` (only if set) | 200 object | CONFIRMED |
| POST | /api/authors/:authorId/match | lib/services/api_service.dart:2046-2055 (`matchAuthor`, delegates to quickMatchAuthor) and 2057-2110 (`quickMatchAuthor`, URL 2080; body 2063-2093) | `region` (default 'us'), `asin` (when set), `q` (when set) | `author` (2099-2110) | CONFIRMED |
| POST | /api/authors/:authorId/image | lib/services/api_service.dart:2111-2122 (`updateAuthorImageFromUrl`) | `url` | 200 | CONFIRMED |
| DELETE | /api/authors/:authorId/image | lib/services/api_service.dart:2126-2136 | none | 200 | CONFIRMED |
| GET | /api/authors/:authorId/image?width=&token=[&ts=] | lib/services/api_service.dart:1793-1800 (URL built, no request in this file; fetched by image widgets, lib/widgets/library_grid_tiles.dart:900) | query `width`, `token`, `ts` | image bytes | CONFIRMED |
| GET | /api/libraries/:libraryId/narrators | lib/services/api_service.dart:1935-1960 (`getLibraryNarratorCounts`); `getLibraryNarrators` 1917-1933 uses filter data | none | narrator counts map | CONFIRMED |
| GET | /api/libraries/:libraryId/search?q=&limit= | lib/services/api_service.dart:3427-3448 (`searchLibrary`) | query `q` (encodeQueryComponent), `limit`; timeout 60s | `book[]`, `series[]`, `authors[]` (per comment 3426) | CONFIRMED |
| GET | /api/search/books?title=&provider=&region=[&author=] | lib/services/api_service.dart:3453-3507 (`searchBooks`) | query `title`, `provider` (default 'audible'), `region`, `author` | array (or map with results/items/books/matches) | CONFIRMED |
| GET | /api/search/covers?title=&author=&provider= | lib/services/api_service.dart:4523-4552 (`searchCovers`, default provider 'google') | query | `results[]` (string urls or objects with `cover`/`url`/`image`) | CONFIRMED |
| GET | /api/search/covers?title=&author=&provider=audible&region= | lib/services/api_service.dart:3609-3637 (`searchAudibleRating`) | query | array; reads `asin`/`key` | CONFIRMED |
| GET | /api/search/chapters?asin=&region=[&id=] | lib/services/api_service.dart:4784-4799 (`searchChapters`) | query `asin`, `region`, `id` (BookOrbit only per comment 4782-4783 — sent for ABS too when non-null, 4787) | `chapters`, `runtime`, `brandIntroDurationMs`, `brandOutroDurationMs`, or `{error, stringKey}` | CONFIRMED |
| GET | /api/search/podcast?term= | lib/services/api_service.dart:4557-4574 (`searchPodcasts`) | query `term` | array or `podcasts[]` | CONFIRMED |
| POST | /api/podcasts | lib/services/api_service.dart:4577-4640 (`createPodcast`, body 4607-4629, post 4631-4634) | `libraryId`, `folderId`, `path`, `media.metadata` {title, author, description, releaseDate, genres, feedUrl, imageUrl, itunesPageUrl, itunesId, itunesArtistId, explicit, language}, `media.autoDownloadEpisodes`, `media.autoDownloadSchedule` (default '0 0 * * 1') | library item JSON | CONFIRMED |
| POST | /api/podcasts/feed | lib/services/api_service.dart:4642-4654 (`getPodcastFeed`) | `rssFeed` | `podcast`/`episodes` map (returned as-is) | CONFIRMED |
| GET | /api/libraries/:libraryId/episode-downloads | lib/services/api_service.dart:4657-4665 (`getEpisodeDownloads`) | none | download queue map | CONFIRMED |
| POST | /api/podcasts/:libraryItemId/download-episodes | lib/services/api_service.dart:4668-4678 (`downloadPodcastEpisodes`) | JSON array of episode objects | 200 | CONFIRMED |
| GET | /api/podcasts/:id/checknew | lib/services/api_service.dart:4682-4705 (`checkNewEpisodes`, per podcast from `getLibraryItems`), 4710-4722 (`checkNewPodcastEpisodes`, query `limit`) | query `limit` (optional) | `episodes[]` | CONFIRMED |
| DELETE | /api/podcasts/:podcastId/episode/:episodeId?hard=0|1 | lib/services/api_service.dart:4857-4865 (`deletePodcastEpisode`) | query `hard` | status code (403 = no delete permission) | CONFIRMED |
| POST | /api/items/:itemId/match | lib/services/api_service.dart:4729-4753 (`matchLibraryItem`) | `title`, `author`, `provider`, `overrideCover`, `overrideDetails` (each only when set) | match result map | CONFIRMED |
| POST | /api/items/:itemId/chapters | lib/services/api_service.dart:4758-4776 (`updateChapters`) | `chapters[]` ({id,start,end,title}) | 200 | CONFIRMED |
| PATCH | /api/items/:itemId/media | lib/services/api_service.dart:4462-4478 (`updateItemMedia`, body 4468-4472) | `metadata` (object), `tags` (optional) | 200 | CONFIRMED |
| PATCH | /api/items/:itemId/media | lib/services/api_service.dart:4839-4849 (`updatePodcastMedia`) | `mediaUpdates` map at media level (e.g. `autoDownloadEpisodes`, `autoDownloadSchedule`) | 200 | CONFIRMED |
| POST | /api/items/:itemId/cover | lib/services/api_service.dart:4481-4491 (`updateItemCoverUrl`) | `url` | 200 | CONFIRMED |
| POST | /api/items/:itemId/cover (multipart) | lib/services/api_service.dart:4507-4519 (`uploadItemCover`) | multipart part `cover` (file) | 200 | CONFIRMED |
| DELETE | /api/items/:itemId/cover | lib/services/api_service.dart:4494-4504 (`removeItemCover`) | none | 200 | CONFIRMED |
| GET | /api/items/:itemId/cover?token=&width=&ts= | lib/services/api_service.dart:1436-1441 (`getCoverUrl`, URL only); download fetch lib/services/download_service.dart:716-719, 1694-1698, 1725-1727, 2763-2765 | query `token`, `width` (widths used in lib: 120, 200, 400 (default), 800, 1200), `ts` | image bytes (200 + non-empty body required) | CONFIRMED |
| DELETE | /api/items/:itemId?hard=0|1 | lib/services/api_service.dart:4871-4879 (`deleteLibraryItem`) | query `hard` | status code | CONFIRMED |
| DELETE | /api/items/:itemId/file/:fileId | lib/services/api_service.dart:4884-4892 (`deleteLibraryFile`) | none; `fileId` URL-encoded | status code | CONFIRMED |
| POST | /api/items/batch/delete?hard=0|1 | lib/services/api_service.dart:4898-4914 (`deleteLibraryItems`) | `libraryItemIds[]` | status code (403 surfaced) | CONFIRMED |
| POST | /api/items/batch/quickmatch | lib/services/api_service.dart:4917-4942 (`quickMatchLibraryItems`) | `options` {provider, overrideCover, overrideDetails}, `libraryItemIds[]` | 200 | CONFIRMED |
| POST | /api/tools/item/:itemId/embed-metadata?backup=0|1 | lib/services/api_service.dart:4803-4813 (`embedMetadata`) | query `backup` | 200 (admin) | CONFIRMED |
| POST | /api/tools/item/:itemId/encode-m4b?codec=&bitrate=&channels= | lib/services/api_service.dart:4817-4836 (`startM4bEncode`) | query `codec`, `bitrate`, `channels` | 200 (admin); completion via socket `task_finished` action `encode-m4b` (socket_service.dart:266) | CONFIRMED |
| GET | /api/libraries/:libraryId/recent-episodes?limit=&page= | lib/services/api_service.dart:3116-3130 (`getRecentEpisodes`) | query `limit`, `page` | `episodes[]` or array | CONFIRMED |
| GET | /api/libraries/:libraryId/playlists (paged: limit, page) | lib/services/api_service.dart:4967-4990 (`getLibraryPlaylists`; `_getAllPaged` 4975-4995, page size 25, max 80 pages) | query `limit`, `page` | paged list | CONFIRMED |
| GET | /api/playlists/:playlistId | lib/services/api_service.dart:5005-5015 (`getPlaylist`) | none | playlist object | CONFIRMED |
| POST | /api/playlists | lib/services/api_service.dart:5018-5036 (`createPlaylist`) | `libraryId`, `name`, `items[]` | playlist object | CONFIRMED |
| PATCH | /api/playlists/:playlistId | lib/services/api_service.dart:5040-5058 | `name`, `items[]` (each optional) | playlist | CONFIRMED |
| DELETE | /api/playlists/:playlistId | lib/services/api_service.dart:5061-5070 | none | 200 | CONFIRMED |
| POST | /api/playlists/:playlistId/item | lib/services/api_service.dart:5072-5090 | `libraryItemId`, `episodeId` (optional) | playlist | CONFIRMED |
| DELETE | /api/playlists/:playlistId/item/:libraryItemId[/:episodeId] | lib/services/api_service.dart:5092-5110 | none | playlist | CONFIRMED |
| GET | /api/libraries/:libraryId/collections (paged) | lib/services/api_service.dart:5113-5117 (`_getAllPaged` 4975-4995) | query `limit`, `page` | paged list | CONFIRMED |
| GET | /api/collections/:collectionId | lib/services/api_service.dart:5119-5130 | none | collection (`books[]`) | CONFIRMED |
| POST | /api/collections | lib/services/api_service.dart:5132-5155 | `libraryId`, `name`, `books[]`, `description` (optional) | collection | CONFIRMED |
| PATCH | /api/collections/:collectionId | lib/services/api_service.dart:5157-5178 | `name`, `description`, `books` (each optional) | collection | CONFIRMED |
| DELETE | /api/collections/:collectionId | lib/services/api_service.dart:5180-5190 | none | HTTP status returned | CONFIRMED |
| POST | /api/collections/:collectionId/book | lib/services/api_service.dart:5193-5208 (`addBookToCollection`) | `id` (libraryItemId) | collection | CONFIRMED |
| DELETE | /api/collections/:collectionId/book/:libraryItemId | lib/services/api_service.dart:5210-5220 | none | collection | CONFIRMED |
| GET | /api/users | lib/services/api_service.dart:3856-3873 (`getUsers`) | none (admin) | `users[]` | CONFIRMED |
| POST | /api/users | lib/services/api_service.dart:4330-4345 (body 4330-4337) | `username`, `password`, `type`, `isActive`, `permissions` (optional), `librariesAccessible` (optional) | user | CONFIRMED |
| GET | /api/users/:userId | lib/services/api_service.dart:4347-4358 (`getUser`) | none | user object | CONFIRMED |
| PATCH | /api/users/:userId | lib/services/api_service.dart:4360-4370 (`updateUser`) | arbitrary `updates` map | 200 | CONFIRMED |
| DELETE | /api/users/:userId | lib/services/api_service.dart:4372-4382 (`deleteUser`) | none | 200 | CONFIRMED |
| PATCH | /api/users/:userId/openid-unlink | lib/services/api_service.dart:4385-4395 (`unlinkOpenID`) | none | 200 | CONFIRMED |
| GET | /api/users/online | lib/services/api_service.dart:3876-3887 (`getOnlineUsers`) | none | `usersOnline[]` or `openSessions[]` | CONFIRMED |
| GET | /api/sessions?itemsPerPage= | lib/services/api_service.dart:3890-3901 (`getAllSessions`) | query `itemsPerPage` | `sessions[]` | CONFIRMED |
| GET | /api/sessions?itemsPerPage=&page=&sort=&desc=[&user=] | lib/services/api_service.dart:3906-3931 (`getAllSessionsPaged`) | query | `total`, `numPages`, `page`, `sessions[]` | CONFIRMED |
| GET | /api/backups | lib/services/api_service.dart:3934-3943 (`getBackups`) | none | `backups[]` | CONFIRMED |
| POST | /api/backups | lib/services/api_service.dart:4089-4095 (`createBackup`) | none | 200 | CONFIRMED |
| GET | /api/tasks | lib/services/api_service.dart:3946-3964 (`getServerTasks`) | none | `tasks[]` (or array) | CONFIRMED |
| POST | /api/filesystem/pathexists | lib/services/api_service.dart:3969-3999 (`checkUploadPathExists`) | `directory`, `folderPath` | `exists`, `libraryItemTitle` | CONFIRMED |
| POST | /api/upload (multipart) | lib/services/api_service.dart:4003-4086 (`uploadMedia`, form 4013-4021, file parts 4029-4068) | form fields `title`, `library`, `folder`, `author`, `series` (not for podcasts); file parts named "0", "1", ... (4039) | 200 = ok; else body as error text | CONFIRMED |
| GET | /api/filesystem?path= | lib/services/api_service.dart:4139-4150 | query `path` | directory listing map | CONFIRMED |
| POST | /api/libraries | lib/services/api_service.dart:4152-4163 (`createLibrary`) | caller body map (`name`, `folders[{fullPath}]`, `mediaType`, `icon`, ...) | library | CONFIRMED |
| PATCH | /api/libraries/:id | lib/services/api_service.dart:4165-4175 (`updateLibrary`) | caller body map (folders keep `{id, fullPath}`) | 200 | CONFIRMED |
| DELETE | /api/libraries/:id | lib/services/api_service.dart:4177-4185 (`deleteLibrary`) | none | 200 | CONFIRMED |
| POST | /api/libraries/order | lib/services/api_service.dart:4187-4196 (`reorderLibraries`) | raw JSON array `[{id, newOrder}]` | 200 | CONFIRMED |
| POST | /api/libraries/:id/scan | lib/services/api_service.dart:4098-4106 (`scanLibrary`) | none | 200 | CONFIRMED |
| POST | /api/libraries/:id/match | lib/services/api_service.dart:4109-4117 (`matchLibrary`) | none | 200 | CONFIRMED |
| GET | /api/libraries/:id/stats | lib/services/api_service.dart:4120-4128 (`getLibraryStats`) | none | stats map | CONFIRMED |
| POST | /api/libraries/:id/remove-metadata | lib/services/api_service.dart:159-184 (`removeLibraryMetadataFiles`, 30 min timeout) | per call body (not inspected beyond URL/timeout, 167-180) | result map | CONFIRMED (URL), body INFERRED |
| POST | /api/validate-cron | lib/services/api_service.dart:186-200 (`validateCronExpression`) | `expression` (body name per call) | error text | CONFIRMED (URL), body INFERRED |
| GET | /api/logger-data | lib/services/api_service.dart:203-225 (`getServerLogs`) | none | `currentDailyLogs[]` | CONFIRMED |
| GET | /api/custom-metadata-providers | lib/services/api_service.dart:4200-4215 | none | `providers[]` (ids `custom-<id>`) | CONFIRMED |
| PATCH | /api/settings | lib/services/api_service.dart:4223-4235 | caller `patch` map | `serverSettings` | CONFIRMED |
| PATCH | /api/sorting-prefixes | lib/services/api_service.dart:4238-4249 | `sortingPrefixes[]` | `serverSettings` | CONFIRMED |
| GET | /api/auth-settings | lib/services/api_service.dart:4252-4259 | none | password and OIDC settings map | CONFIRMED |
| PATCH | /api/auth-settings | lib/services/api_service.dart:4262-4270 | caller `patch` map | `updated`, `serverSettings` | CONFIRMED |
| GET | /auth/openid/config?issuer= | lib/services/api_service.dart:4273-4288 | query `issuer` | OIDC discovery JSON, or error text | CONFIRMED |
| POST | /api/cache/purge | lib/services/api_service.dart:4131-4137 (`purgeCache`) | none | 200 | CONFIRMED |
| GET | /api/stats/server | lib/services/api_service.dart:4291-4298 | none | `books`, `podcasts`, `total` | CONFIRMED |
| GET | /api/stats/year/:year | lib/services/api_service.dart:4300-4308 | none | year-in-review map | CONFIRMED |
| GET | /api/emails/settings | lib/services/api_service.dart:3236-3251 (`getEmailSettings`) | none | `settings` (unwrapped) | CONFIRMED |
| PATCH | /api/emails/settings | lib/services/api_service.dart:3253-3263 | caller `patch` map | 200 | CONFIRMED |
| POST | /api/emails/test | lib/services/api_service.dart:3265-3273 | none | 200 | CONFIRMED |
| POST | /api/emails/ereader-devices | lib/services/api_service.dart:3275-3286 | `ereaderDevices[]` ({name, email, availabilityOption, users}) | 200 | CONFIRMED |
| POST | /api/emails/send-ebook-to-device | lib/services/api_service.dart:3221-3234 (`sendEBookToDevice`) | `libraryItemId`, `deviceName` | 200 | CONFIRMED |
| GET | /api/api-keys | lib/services/api_service.dart:4399-4413 (`getApiKeys`) | none | `apiKeys[]` | CONFIRMED |
| POST | /api/api-keys | lib/services/api_service.dart:4415-4433 (`createApiKey`, body 4415-4424) | `name`, `userId`, `isActive`, `expiresIn` (optional) | created key (contains token value) | CONFIRMED |
| PATCH | /api/api-keys/:keyId | lib/services/api_service.dart:4435-4448 (`updateApiKey`) | `isActive`, `userId` (optional) | 200 | CONFIRMED |
| DELETE | /api/api-keys/:keyId | lib/services/api_service.dart:4450-4456 (`deleteApiKey`) | none | 200 | CONFIRMED |
| POST | /api/items/:itemId/... (play session start in downloads) | lib/services/download_service.dart:2138-2139, lib/services/local_session_service.dart:400-401, lib/services/audio_player_service.dart:3930-3931, 4904-4905, 5006-5011, 5081-5086 | see POST /api/items/:itemId/play above | see above | CONFIRMED (call sites) |
| POST | /api/session/:id/close | lib/services/download_service.dart:2255, 3046; lib/services/local_session_service.dart:406; lib/services/audio_player_service.dart:4302, 4715, 5073 | none | not read | CONFIRMED (call sites) |
| POST | /api/session/:id/sync (playback) | lib/services/audio_player_service.dart:4273-4280 (hot-swap), 6508-6617 (20 s cadence), 7832-7942 (`_progressSync.syncToServer`); lib/services/local_session_service.dart:404; lib/services/progress_sync_service.dart:221-224, 724-728 | see above | see above | CONFIRMED |
| PATCH | /api/me/progress/:itemId (queued sync) | lib/services/progress_sync_service.dart:227-230 (updateProgress), 371-381 (flushPendingSync, episode variant 371-375), 538 (ebook flush) | see above | see above | CONFIRMED |

### Non-ABS endpoints also called (not Audiobookshelf)
| Method | URL | File:lines | Request fields | Response fields read | Status |
|---|---|---|---|---|---|
| GET | https://api.audnex.us/books/:asin?region=&update=1 | lib/services/api_service.dart:3514-3537 (`getAudibleRating` fallback), 3671-3695 (`getAudnexusBook`, `_getAudnexusBookRaw` 3682-3695 with `region=us` fallback) | none | `rating`, series data | CONFIRMED |
| GET | https://api.audible<tld>/1.0/catalog/products/:asin?response_groups=rating | lib/services/api_service.dart:3539-3566 (`_audibleCatalogRating`) | none | `product.rating.overall_distribution.average_rating`, `num_ratings` | CONFIRMED |
| GET | https://api.audible<tld>/1.0/catalog/products/:seriesAsin?response_groups=relationships | lib/services/api_service.dart:3699-3731 | none | `product.relationships[].asin/sequence/sort` | CONFIRMED |
| GET | https://api.audible<tld>/1.0/catalog/products/:asin?response_groups=product_attrs,product_desc,product_details,series,rating,media | lib/services/api_service.dart:3735-3751 | none | `product` | CONFIRMED |
| GET | https://api.github.com/repos/advplyr/audiobookshelf/releases | lib/services/audiobookshelf_update_service.dart:20 (server update check) | Accept header | release list | CONFIRMED (URL) |
| GET | https://api.github.com/repos/<owner>/<repo>/... | lib/services/update_checker_service.dart:95-105 (app update check) | Accept header | release JSON | CONFIRMED (URL) |
| GET/POST/PATCH (RMAB) /api/auth/me, /api/audiobooks/search, /api/requests, /api/requests/:id | lib/services/rmab_service.dart:46, 68, 113, 144, 163 (ReadMeABook server, user-supplied base URL, `rmab_` bearer token; separate server) | per-call | query `q`, `limit`, etc. | per-call | CONFIRMED (URL) |
| PUT / GET `<user WebDAV base>/absorb-settings-<hash16>.json` | lib/services/settings_sync_service.dart:682-696 (URI), 747-766 (PUT), 810-819 (GET), 909 | body: settings JSON with `syncedAt` (read at 842) | `syncedAt` | CONFIRMED |
| GET https://en.wiktionary.org/api/rest_v1/page/definition/:word | lib/services/dictionary_service.dart:75 | none | definitions | CONFIRMED (URL) |
| GET https://huggingface.co/ggerganov/whisper.cpp/resolve/main/ggml-*.bin | lib/services/transcription_service.dart:63-81 (model download) | none | binary | CONFIRMED (URL) |
| POST/GET (DeepL web) https://www.deepl.com/translator | lib/services/translate_service.dart:100 | not an API; string only | — | INFERRED (web URL, not an API call) |

## Auth
- Login: `POST <server>/login` with `x-return-tokens: true`, body `{username, password}` (api_service.dart:1083-1099). Pre-login reachability: `GET /ping` (api_service.dart:1143-1157, auth_provider.dart:768-773). CONFIRMED.
- Token parse: `AuthTokens.fromResponse` reads `user.accessToken` and `user.refreshToken`, else top-level `accessToken`/`refreshToken`; legacy = `user.token` (lib/services/auth_tokens.dart:14-27; lib/providers/auth_provider.dart:830-838). `isLegacy` = no accessToken (auth_tokens.dart:12). Legacy sessions are flagged `_isLegacyToken` (auth_provider.dart:836, 1097; restored from prefs when no refresh token, 573). CONFIRMED.
- Send: `Authorization: Bearer <accessToken|legacy token|API key>` on every authenticated call (`_headers` 628-632, `mediaHeaders` 635-638), plus `customHeaders` (proxy headers such as Cloudflare Access) on every request (626-638, 1091, 1571). CONFIRMED.
- Query-string token (`?token=`) used on: cover URL (api_service.dart:1437), author image URL (1794), durable file download URL (2349, `buildFileUrl`), tokened track URL fallback (2337-2339, `buildTrackUrl`, used when session not direct play or server < 2.22), ebook download (lib/services/ebook_cache.dart:318-321: header AND `?token=`, comment says some reverse proxies strip the header). CONFIRMED. Consequence: the bearer token sits in URLs that the image cache keys and logs may see (cache key stripping `token` is at lib/widgets/stable_cached_network_image.dart:1-40 — CONFIRMED).
- Refresh (`POST /auth/refresh`, header `x-refresh-token`, api_service.dart:734-741). Triggers:
  1. Pre-flight: before a request, if the access JWT `exp` is within 60 s (`_accessTokenSkew`, 904; expiry parse 908-922; `_ensureFreshAccessToken` 953-975). Suppressed for 2 min after a transient failure (`_preflightCooldown`, 926-927, 968).
  2. Reactive: on 401 in `_authGet/_authPost/_authPatch/_authDelete` (982-1065), except legacy tokens (which do not refresh; on 401 they call `_checkRevokedApiKey`, 1004-1006, 1023-1025, 1042-1044, 1061-1063).
  Single-flight within an instance via `_refreshCompleter` (816-820); cross-instance lock `_acquireTokenMutationLock` (718-732, 827); before refreshing it re-reads persisted tokens written by another isolate (`_adoptPersistedTokens` 674-700, 829). Up to 2 attempts, 250 ms apart on transient failure (`_refreshRetryDelay` default, 316). 401/403 = rejected -> `onAuthExpired` (803, 1003, 1022, 1041, 1060, 966). Other status or network error = transient (893-894). CONFIRMED.
  Response parsing for rotation: 847-853 (`accessToken` and optional new `refreshToken`; the spent-token case is logged at 870-874). CONFIRMED.
- API keys: the key is placed in the Bearer slot and validated with `GET /api/me` (api_service.dart:1117-1140; auth_provider.dart:962-1040, `_accessToken = apiKey` at 1006, `_refreshToken = null` at 1007). No refresh (`_isLegacyToken = true`, auth_provider.dart:1008). A 401 triggers at most one `GET /api/me` per minute to confirm revocation (`_checkRevokedApiKey` 775-797). CONFIRMED.
- Logout: `POST /logout[?allDevices=1]` with `x-refresh-token` (api_service.dart:1563-1580). Local tokens are cleared in auth_provider. CONFIRMED.
- OIDC / SSO (lib/services/oidc_service.dart):
  - Constants: `redirect_uri = absorb://oauth` (oidc_service.dart:48), `client_id = Audiobookshelf-App` (49). The server must allow the redirect URI under Mobile redirect URIs (comment 45-47; error text 127-130). CONFIRMED.
  - PKCE: verifier = 32 random bytes base64url (62-63 region in `_generateRandom`, 59-66); challenge = base64url(SHA-256(verifier)), `code_challenge_method=S256` (62-66, 80-86). CONFIRMED.
  - State: 16 random bytes base64url (74; checked 249-257, 341-352). CONFIRMED.
  - Step 1: `GET <server>/auth/openid?code_challenge=&code_challenge_method=S256&redirect_uri=absorb%3A%2F%2Foauth&client_id=Audiobookshelf-App&response_type=code&state=` with `followRedirects=false`; custom headers; header `x-return-tokens: true` (oidc_service.dart:80-101). Captures `Set-Cookie` cookies (104-116) and expects a 302 `Location` (oidc_service.dart ~140-160). CONFIRMED.
  - Step 2: Custom Tab / ASWebAuthenticationSession with `callbackUrlScheme: absorb` (oidc_service.dart:161, 282-290). CONFIRMED.
  - Step 3: `GET <server>/auth/openid/callback?state=&code=&code_verifier=` (oidc_service.dart:356-359; note: verifier sent as a query parameter, not a POST body) with `Cookie:` = captured cookies (371-376), `x-return-tokens: true`, `followRedirects=false` (370-372). Response parsed as login JSON (`loginWithOidc` auth_provider.dart:1067-1140). CONFIRMED.
  - Status probe before SSO: `GET /status` for `authMethods` / `authFormData` (oidc_service.dart:418-446). CONFIRMED.
  - BookOrbit SSO (different redirect `bookorbit://oauth2-callback`) excluded (oidc_service.dart:167).
- Token-expiry signal from the socket: `auth_failed` with `{message: 'Token expired'}` triggers a fresh token request (socket_service.dart:28-46). CONFIRMED.
- Password change and token rotation: 1582-1648 (see table).
- Transport note (not protocol): a process-wide `HttpOverrides` sets `badCertificateCallback` to `trustAllCerts` (lib/main.dart:124-135), so all Dart HTTP (including the socket and image cache) accepts any certificate when the user enables it. CONFIRMED.

## Socket.IO
- Library and version: `socket_io_client` 3.1.4 (pubspec.yaml:18 `^3.0.2`; pubspec.lock `socket_io_client` 3.1.4; transitive `socket_io_common` 3.1.1). CONFIRMED. Protocol version the server must speak: not verifiable from this repo (INFERRED: socket_io_client 3.x targets Socket.IO v4 servers; ABS uses v4).
- Import: lib/services/socket_service.dart:2. Singleton: `SocketService` (socket_service.dart:17-22).
- URL: origin = `<scheme>://<authority>` (port included), path = `<basePath>/socket.io` (socket_service.dart:62-71, 94-101). CONFIRMED.
- Options (`buildSocketOptions`, socket_service.dart:73-92): `setTransports(['websocket'])` (WebSocket only, no polling fallback), `setPath(<path>)`, `enableForceNew()`, `enableReconnection()`, `setReconnectionDelay(1000)`, `setReconnectionDelayMax(30000)`, `setReconnectionAttempts(5)`, `setExtraHeaders(customHeaders)` when set. CONFIRMED.
- Connect: `connect(serverUrl, token, customHeaders)` (socket_service.dart:380-); not started for BookOrbit (385) or e-ink mode (395-399). Started from library_provider.dart:254-277 with `auth.token` and `customHeaders`. CONFIRMED.
- Auth: on every `connect` event, client emits `'auth'` with the raw token string (`_socket!.emit('auth', _token)`, socket_service.dart:404-406, 619-621). Token refresh: `updateToken` re-emits `auth` if connected (socket_service.dart:371-377; called from auth_provider.dart:368). No query/extraHeaders token. CONFIRMED.
- Auth outcome:
  - `init` = authenticated (socket_service.dart:409-416 and 624-631); calls `onAuthenticated` (catch-up), resets `_askedForToken`, sends `set_log_listener` (409-416).
  - `auth_failed` with optional `{message}` (socket_service.dart:28-46, listener 416): `message == 'Token expired'` -> one retry via `onTokenExpired` (30-45, once per sign-in); otherwise `disconnect()`. CONFIRMED.
- No acks: no `emitWithAck` / ack callback is used (grep of lib: none). CONFIRMED.
- Events emitted by client: `auth` (string token) (socket_service.dart:376, 406, 621); `set_log_listener` (int level, 221); `remove_log_listener` (no payload, 219). CONFIRMED.
- Events listened to (CONFIRMED, socket_service.dart):
  - Session: `connect` (onConnect 402-407), `disconnect` (onDisconnect 530-541), `connect_error` (onConnectError 538-540, 705-707; log only), `reconnect_failed` (542-548, 709-716).
  - Auth: `init` (409), `auth_failed` (416).
  - Progress: `user_item_progress_updated` (419-428) with `data.data` patch and `data.sessionId` (`_withSession` 561-562).
  - Library items: `item_added` (429), `item_updated` (435), `item_removed` (441), `items_updated` (450), `items_added` (455).
  - Series: `series_added`, `series_updated`, `series_removed` (462-474; 666-668 in softReconnect).
  - Collections: `collection_added`, `collection_updated`, `collection_removed` (476-487; 670-672).
  - Playlists: `playlist_added`, `playlist_updated`, `playlist_removed` (490-502; 674-676).
  - Authors: `author_added`, `author_updated`, `author_removed`, `authors_num_books_updated` (socket_service.dart:8-14, registered 360-364 and called from 504, 678).
  - User: `user_updated` (507-512; 680-683).
  - Tasks: `task_started` (514; handler 250-258), `task_progress` (515; 260-262), `task_finished` (516; 264-268; `encode-m4b` action -> `onEncodeFinished`, 266).
  - Logs: `log` (238; handler 216-236 uses level 0-6).
  - E-reader: `ereader-devices-updated` with `ereaderDevices` (522-528; 690-696).
- Reconnect: after the first successful `init`, all listeners are re-bound in `softReconnect` (socket_service.dart:605-720) which duplicates the list above. CONFIRMED.
- Socket is not used for BookOrbit (385, 608). CONFIRMED.
- Progress is pulled, not replayed, after reconnect: `onAuthenticated` -> `_catchUpRemoteProgress` (library_provider.dart:262; lib/providers/_lp_core.dart:1696-1712 calls `GET /api/me/progress`). CONFIRMED.

## Playback, sessions, progress
- Start: `POST /api/items/:id/play` (or `/play/:episodeId`) with `forceDirectPlay`, `forceTranscode`, `mediaPlayer: 'exo-player'`, `supportedMimeTypes`, `deviceInfo` (api_service.dart:2200-2207, 2224-2276, 2971-3011). CONFIRMED.
- Dolby/EAC-3/AC-3/AC-4 on Android (not Samsung): if the session's tracks have those codecs, the client closes the session and re-POSTs `/play` with `forceTranscode: true` (audio_player_service.dart:5052-5098). CONFIRMED.
- Track URL: `buildTrackUrl` (api_service.dart:2285-2340):
  - if `contentUrl` is absolute: used as-is, with `token` removed from `/public/session/` URLs and NOT added otherwise (2297-2321);
  - if the session is direct play (`playMethod == 0`) and server version >= 2.22 (`_hasPublicSessionTracks`, 1297-1301): `<base>/public/session/<sessionId>/track/<index>` with no token (2326-2331);
  - otherwise: `<base><contentUrl>?token=<token>` (2337-2339), which is how HLS/transcode `contentUrl`s get the token. HLS (`.m3u8`) is never checked for or parsed in the client (grep for `m3u8`/`hls`: no matches outside the comment at 2323). INFERRED that transcode contentUrls are HLS playlists served by the server; the client just appends `token`.
- Playback player: `ConcatenatingAudioSource` of `AudioSource.uri(<track URL>, headers: playbackSessionHeaders)` (audio_player_service.dart:4752-4772, 5212, 5473, 5999; `playbackSessionHeaders` 644 = `customHeaders` only, so the bearer token is NOT sent to the player, comment 640-643). CONFIRMED.
- Session sync: `POST /api/session/:id/sync` body `{currentTime, timeListened, duration}` (api_service.dart:2370-2392). The playback loop syncs every 20 s (audio_player_service.dart:6508-6512); background save every 300 s (6238, 7638). Sync is skipped and falls back to `updateProgress` when no session (progress_sync_service.dart:213-230). CONFIRMED.
- Close: `POST /api/session/:id/close` (api_service.dart:2396-2402), called on stop/hot swap (audio_player_service.dart:4302, 4715, 5073; download_service.dart:2255, 3046). CONFIRMED.
- Offline / local sessions: `POST /api/session/local` (one) and `POST /api/session/local-all` (batch) with `playMethod` default 3 (LOCAL) and `mediaPlayer: 'exo-player'` (api_service.dart:2409-2468). Reading time uses `mediaPlayer: 'ebook-reader'` and deviceId `<id>-ebook`, clientName 'Absorb (ebook)' (api_service.dart:2212-2219). Queued sessions replayed by lib/services/local_session_service.dart:300-375. CONFIRMED.
- Progress updates:
  - Audio book: `PATCH /api/me/progress/:itemId` with `currentTime`, `duration`, `progress`, optional `isFinished` (api_service.dart:2789-2824).
  - Podcast episode: `PATCH /api/me/progress/:showId/:episodeId` (3035-3067).
  - Ebook position: `PATCH /api/me/progress/:itemId` with `ebookLocation`, `ebookProgress` (2833-2865); no 401 retry, raw `http.patch` (2853).
  - Batch finished/unfinished: `PATCH /api/me/progress/batch/update` (4945-4963). CONFIRMED.
  - Queued sync when offline: progress_sync_service.dart:253-470 (flushPendingSync, 5 per run, retry with backoff ~462-465), ebook queue 476-555.
- Remote progress from socket: `user_item_progress_updated` (socket_service.dart:419-428) -> `_onRemoteProgressUpdated` (library_provider.dart:260-263). CONFIRMED.
- Bookmarks: GET/POST/PATCH/DELETE under `/api/me/bookmarks` and `/api/me/item/:id/bookmark` (see table). CONFIRMED.
- Listening-time records for stats: `POST /api/session/local` with `timeListening` and `updatedAt`, reading-time as listening sessions (api_service.dart:2511-2565). CONFIRMED.
- Podcast auto-download schedule and episode queue: `/api/libraries/:id/episode-downloads`, `/api/podcasts/:id/download-episodes`, `/api/podcasts/:id/checknew` (see table; scheduling in lib/providers/_lp_core.dart and lib/services/download_service.dart). CONFIRMED.
- Playlists and collections: see table (api_service.dart:4967-5220). CONFIRMED.

## Downloads, ranges, media
- Download URL for a book file: `GET <base>/api/items/:id/file/:ino?token=<token>` (api_service.dart:2348-2350; `buildDownloadFileUrl` 456). Built by `_resolveDurableFiles` from play-session `contentUrl`s; `ino` extracted from the `/api/items/<id>/file/<ino>` path (download_service.dart:2651-2705). Fallback: a `/public/session/<sid>/track/<i>` link when the track has no ino (download_service.dart:2694-2697). CONFIRMED.
- Headers for the download task: `api.downloadHeadersFor(url)` = `mediaHeaders` (`Authorization: Bearer` + customHeaders) (download_service.dart:2317-2344; api_service.dart:459, 635-638). CONFIRMED.
- Download engine: `background_downloader` 9.5.5 `DownloadTask` with `retries: 3`, `allowPause: true`, `requiresWiFi` per setting, group `absorb_downloads` (download_service.dart:2317-2344, 2492-2519, 472-475). CONFIRMED. Range/resume behavior of the plugin is not in this repo; `FileDownloader().resume` is called (download_service.dart:2847). INFERRED: resume uses an HTTP `Range` request in the plugin (not verified).
- On 401 mid-download for narrated EPUBs: download renews headers via `renewedMediaHeaders` (api_service.dart:463-466) and resumes or restarts (download_service.dart:2831-2868, 2837 reads `responseStatusCode`). CONFIRMED.
- Expected statuses for downloads: the background_downloader path does not check status in app code beyond the plugin's own `responseStatusCode` (2822, 2837); `403` maps to a permission message (download_service.dart:3379). CONFIRMED.
- Ebook (EPUB/PDF/CBZ) download: `GET /api/items/:id/file/:ino?token=` with `followRedirects=false`, manual redirect loop up to 5 hops, re-attaching headers to each hop (lib/services/ebook_cache.dart:300-330). Requires 200 (`if (response.statusCode != 200) throw`, 331-333). Rejects `text/html` (334-335). Stall timeout 60 s (340-345). CONFIRMED.
- Covers for download: `GET <cover URL with width=1200>` via `http.get` with `mediaHeaders`, requires 200 and non-empty body (download_service.dart:716-719, 1694-1698, 1725-1727, 2763-2765; `_hiResCoverUrl` regex `width=\d+` -> `width=1200`, 2740-2741). CONFIRMED.
- Narrated EPUB (EPUB that reads itself aloud): audio is pulled out of the EPUB with HTTP Range requests against the file endpoint:
  - `RemoteZip` requires `206` for every range request and reads `Content-Range` total (lib/services/remote_zip.dart:53-70, 205-213). Errors on any other status (63-66, 209-210). CONFIRMED.
  - Used with `buildFileUrl` + `mediaHeaders` (lib/services/narration_service.dart:263). CONFIRMED.
  - `RemoteAudioSlice` requires `206` on ranged fetch; accepts `200` only if the server ignores Range (lib/services/remote_audio_slice.dart:166-182). CONFIRMED.
  - Loopback proxy `NarrationStream` (127.0.0.1) passes ranges upstream and requires 206 (lib/services/narration_stream.dart:49-50, 257-275, 449-475). CONFIRMED.
- Streaming playback: the platform player fetches the track URL with Range requests (native ExoPlayer/AVPlayer; not in this repo). The vendored just_audio fork's own range path (`_HttpRangeRequest`, packages/just_audio/lib/just_audio.dart:2221-2240) and origin fetch requires `206` for ranged requests (3182-3186) and returns `416` only through its own server path (4219-4224). `LockCachingAudioSource` exists in the fork (2952) but is not instantiated in lib/ (grep: no `LockCachingAudioSource(` call outside the fork). CONFIRMED (code paths), behavior of native player INFERRED.
- 416 handling: `'Response code: 416'` in the player error clears the streaming cache (lib/services/audio_player_service.dart:6140-6146). CONFIRMED.
- Content types: the app assumes `audio/*` (supportedMimeTypes, api_service.dart:2236-2250) and `text/html` = error page for ebooks (ebook_cache.dart:334-335). Mime types for tracks come from the session `mimeType` (download_service.dart:2702-2705 uses name only). CONFIRMED.
- Chunked/whole-file: `RemoteZip` and `RemoteAudioSlice` both fall back to hanging up when the server answers `200` to a range (remote_zip.dart:63-66; remote_audio_slice.dart:176-180). CONFIRMED.
- Upload: `POST /api/upload` multipart, file parts "0", "1", ... with `Content-Length` from `file.size` (api_service.dart:4029-4068). CONFIRMED.

## Caching and covers
- Cover URL: `<base>/api/items/:id/cover?token=<token>[&width=N][&ts=<updatedAt>]` (api_service.dart:1436-1441). Default width 400; widths used in the app: 120, 200, 400, 800, 1200 (grep of `getCoverUrl(...width:`). `ts` = cache-buster from item `updatedAt` (`registerHasCover`, lib/providers/_lp_core.dart:1870-1881, 2020-2024). CONFIRMED.
- Author image URL: `<base>/api/authors/:id/image?width=N&token=<token>[&ts=]` (api_service.dart:1793-1800). CONFIRMED.
- Image cache: `cached_network_image` 3.4.1 (`StableCachedNetworkImage` with `cacheKey` from `stableCoverCacheKey`, lib/widgets/stable_cached_network_image.dart:1-40) and `flutter_cache_manager` 3.4.1 (transitive). No custom `CacheManager` instance and no `Cache-Control`/`ETag`/`If-None-Match`/`304` handling in the app (grep: no hits for If-None-Match, Cache-Control, 304 outside comments). The default flutter_cache_manager disk cache (HTTP cache headers handled by the plugin) is INFERRED. The cache key strips `token` and replaces `ts` with `updatedAt` (stable_cached_network_image.dart:1-22), so a token change does not bust the cover cache. CONFIRMED.
- Cover override: a local metadata override wins over the server cover (lib/providers/_lp_state.dart:399-402). CONFIRMED.
- HTTP client caching: `package:http` default client; no `dio` or cache interceptor (grep: none). CONFIRMED.
- Downloaded covers stored on disk and read locally (download_service.dart:716-722, 1694-1699). CONFIRMED.
- Server status cache: server version persisted per origin in SharedPreferences `server_version_<origin>` (api_service.dart:1257-1282). CONFIRMED.
- Audible rating cache: `audible_rating_<itemId>` in SharedPreferences (api_service.dart:3572-3604). CONFIRMED.
- ETag not used (grep: 5 hits, all BookOrbit asset manifests or proxy header list). CONFIRMED.

## Server version branches
- Version sources: `serverVersion` in login response, else `serverSettings.version` (auth_provider.dart:864-867), OIDC callback response (1119-1125), `/api/authorize` (179-181), `GET /status` (api_service.dart:1219-1236; auth_provider.dart:948-956, 1366-1372). CONFIRMED.
- Feature branch 2.22: `serverVersionAtLeast(version, 2, 22)` (api_service.dart:1286-1292) gates tokenless `/public/session/<sid>/track/<i>` URLs for direct-play tracks (1297-1301, 2326-2331). Before 2.22 or unknown version: tokened URL. CONFIRMED.
- Fallbacks by 404 (not by version): `GET /api/me/progress` -> `GET /api/me` (1480-1500), `GET /api/me/bookmarks` -> `GET /api/me` (1503-1523), `GET /api/me/bookmarks/:itemId` -> `GET /api/me` filter (3151-3178), `/api/session/local` & `/local-all` 404/501 = too old (2427, 2463), `GET /api/series/:id` -> `GET /api/libraries/:id/series/:id` (3293-3316), `PATCH /api/me/password` 404 = unsupported (1607-1608). CONFIRMED. Comments say "compact v2.36 endpoint" (1479, 1502) but the code does not branch on the version for these. CONFIRMED.
- Socket 2.37 branch: `auth_failed` message 'Token expired' -> refresh and retry once (socket_service.dart:28-46; comment says "Audiobookshelf 2.37"). CONFIRMED.
- Minimum supported version: NOT FOUND. No constant or check (grep for minVersion/minimumServer/MIN_SERVER/supportedServer/requiredServer: no hits in lib/ outside `adminVersion` labels).
- Other version display: settings_screen.dart:1751-1755 (server update check via GitHub releases, `audiobookshelf_update_service.dart:20`).

## Not found (searched terms)
- `If-None-Match`, `Cache-Control`, `304`, `ETag` (for ABS responses), `Last-Modified`: NOT FOUND in the ABS path (only BookOrbit manifest etags and a proxy header list in lib/services/bookorbit_media_proxy.dart:175).
- `m3u8`, `/hls`, `hls` (as an API path): NOT FOUND (only a comment at lib/services/api_service.dart:2323).
- `/s/` (share links), `/share`, `shareLink`, `share_link`: NOT FOUND.
- `/init`, `/api/init`, `/api/ping`, `/api/status`: NOT FOUND (ping and status are root `/ping` and `/status` only).
- `Dio(`, `dio`, `Retrofit`, `axios`, `fetch(`, `URLSession` (only a comment at download_service.dart:1272), `XMLHttpRequest`: NOT FOUND (client is package:http plus dart:io HttpClient).
- `x-api-key`, `X-Api-Key`, `api_key` as header: NOT FOUND (API keys are sent as Bearer).
- `Accept-Encoding`, `gzip`, `Retry-After`, `X-Request`: NOT FOUND.
- `minServerVersion`, `minimumServer`, `minVersion`, `MIN_SERVER`, `supportedServer`, `requiredServer`: NOT FOUND.
- `emitWithAck`, `ack` on sockets: NOT FOUND.
- `/api/items/:id/ebook` (ebook-only endpoint): NOT FOUND (ebook downloaded from `/api/items/:id/file/:ino`).
- `/api/me/items-in-progress`, `/api/me/share`, `/api/share`, `/api/ereader` (non-`emails` path): NOT FOUND.
- `/feed` (podcast RSS as ABS feed route): NOT FOUND (only `/api/podcasts/feed` POST, api_service.dart:4641-4645).
- Cookie session for the ABS API (non-OIDC): NOT FOUND (cookies only in the OIDC pre-flight/callback, oidc_service.dart:104-116, 371-376).
- `LockCachingAudioSource(` instantiated in `lib/`: NOT FOUND (defined in packages/just_audio/lib/just_audio.dart:2952 only).
- Explicit `206`/`416` checks on the native track player path inside `lib/`: NOT FOUND (only `'Response code: 416'` string match, audio_player_service.dart:6142).

## Dynamic call sites (paths built at runtime you could not resolve)
- `getItemProgress`, `updateProgress`, `updateEbookProgress`, `resetProgress`, `zeroEpisodeProgress`: progress path is `itemId` or `<36-char show id>/<episode id>` split by index (api_service.dart:2639-2641, 2796-2798, 2849-2851, 2895-2900). Episode id is taken as the substring after 37 characters — resolved to `/api/me/progress/<show>/<episode>`.
- `getMyItemListeningSessions(itemId, episodeId)` and `getItemListeningSessions(...)`: path `/api/me/item/listening-sessions/<itemId>[/<episodeId>]`, the first form is NOT URL-encoded (api_service.dart:1671-1673 raw; 1715-1718 encoded).
- `getEpisodeProgress`, `updateEpisodeProgress`, `zeroEpisodeProgress`, `deleteBookmark(time)`, `deleteLibraryFile(fileId)` (encoded at 4887), `removeItemFromContinueListening(mediaProgressId)`: path segments interpolated without `Uri.encodeComponent` (3019, 3043, 3095, 3211, 2958).
- `_getAllPaged(path)` and `getLibraryPlaylists/Collections`: path passed as string (api_service.dart:4970, 5117).
- Query strings assembled by string concatenation with raw values (not `Uri.replace`): `getLibraryItems` `filter` and `sort` (api_service.dart:1377-1385), `getSeries` / `getAllBooksBySeries` / `getBooksByAuthor` / `getBooksByNarrator` `filter=<kind>.<base64>` (1826, 1970, 2146-2180, 3346-3348, 3405). `base64Encode` uses the standard alphabet (`+`, `/`, `=` possible), and these values are not percent-encoded. INFERRED risk: a series or author id whose base64 contains `+` could be decoded as a space by the server. Not verified against server behavior.
- `getCoverUrl` / `getAuthorImageUrl` put `token` and `itemId` into the URL unencoded (api_service.dart:1437, 1794). JWT tokens are URL-safe in practice; a legacy/API key with other characters would break (INFERRED).
- `buildTrackUrl(contentUrl)`: the server `contentUrl` is used verbatim as a path; absolute contentUrls are used as-is (no token added) (api_service.dart:2297-2321). Behavior depends on the server.
- Podcast `createPodcast` path: built from library folder `fullPath` + title with `[\\/:*?"<>|]` stripped (api_service.dart:4591-4605). Sent as body `path`, not URL.
- `_hiResCoverUrl`: regex replace of `width=\d+` in the cover URL (download_service.dart:2740-2741). CONFIRMED.
- `getCoverUrl(itemId, width: width)` (dynamic width, multiple callers) — widths not enumerated beyond the set above.
- BookOrbit dispatch: all `ApiService` methods delegate to BookOrbit when `ServerBackend.active` / backend is BookOrbit (api_service.dart:326-362, 779, 1143-1176, 1219-1221, 2482, 2661). Their paths are out of this inventory.
