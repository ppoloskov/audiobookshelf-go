# Storii inventory
- Repo: https://github.com/likhithpraveenk/storii  Commit: b638fcd5f26deda17df7f22d04db90534b2c7f3b  Platform: Flutter (app 0.13.1+527, pubspec.yaml:4); android/ and ios/ dirs present  Language/stack: Dart (sdk ^3.13.0), Flutter 3.47.0, Dio 5.11.1, socket_io_client 3.1.6, Riverpod, Hive CE
- Permalink base: https://github.com/likhithpraveenk/storii/blob/b638fcd5f26deda17df7f22d04db90534b2c7f3b/

Versions (pubspec.yaml / pubspec.lock):
- socket_io_client `^3.1.6` (pubspec.yaml:59); locked 3.1.6 (pubspec.lock:1206), socket_io_common 3.1.1 (:1214)
- dio `^5.11.1` (pubspec.yaml:22); locked 5.11.1 (pubspec.lock:348)
- dio_cache_interceptor `^4.0.7` (pubspec.yaml:23); locked 4.0.7 (:356); http_cache_core 1.1.4 (:672)
- http_cache_hive_store `^5.1.1` (pubspec.yaml:37); locked 5.1.1 (:680)
- cached_network_image_ce `^4.12.0` (pubspec.yaml:17); locked 4.12.0 (:188); flutter_cache_manager 3.4.5 (:465, transitive)
- http 1.6.0 (:664, transitive); web_socket_channel 3.0.3 (:1502, transitive)
- just_audio: git fork `likhithpraveenk/just_audio`, ref `minor` (pubspec.yaml:42-46, lock :776); not from pub.
- No Socket.IO, HTTP or cache code outside the packages above. No `packages/` dir in repo.
- Note: no pub cache exists in the sandbox, so library default behaviour below is from package knowledge and marked INFERRED or NOT VERIFIED.

## HTTP calls
Base URL and path construction:
- User enters a server URL; `normalizedUri` lowercases the whole string and keeps its path, trailing slash stripped (lib/shared/helpers/extensions.dart:39-53). Add-server hint is `https://abs.example.com` (lib/features/auth/ui/add_server_sheet.dart:107). No `/audiobookshelf` prefix is added by the client (`AbsUrls.webClient` at lib/app/config/abs_urls.dart:4 is only a relative resolve and is not called).
- Paths in `ApiRoutes` are absolute, start with `/` and include `/api/...` or `/auth/...` (lib/api/abs/endpoints/api_routes.dart:14-177). `ApiRoutes._build` replaces `:param`, and drops optional `/:episodeId?` when empty (api_routes.dart:2-12).
- Main client: `Dio(BaseOptions(baseUrl: ...))` (lib/api/abs/client/api_client.dart:32-39); relative paths go through Dio's base URL concatenation, so a sub-path base is kept (Dio behaviour; not verified, no pub source).
- Some URLs are built with `Uri.resolve(absolutePath)`, which replaces the whole base path. These are cover URLs (lib/features/library/logic/cover_url_provider.dart:23-24), item cover and track URLs (lib/app/repositories/item_repository.dart:81-86), and downloads (lib/features/downloads/logic/download_engine.dart:30-33). Sub-path installs would break for these, CONFIRMED by code, impact INFERRED.
- Base URL source differs by call: main API and socket and tracks use `activeServerUrlProvider` (LAN/remote switch: lib/app/providers/connection_providers.dart:83-117); covers and downloads use `user.serverUrl` (the remote URL) (cover_url_provider.dart:22-24; download_engine.dart:30).
- `/api/` prefix: yes, on all API paths. `/api/` is not added by a builder; it is literal in ApiRoutes.

| Method | Path | File:lines | Request fields | Response fields read | Status |
|---|---|---|---|---|---|
| GET | /healthcheck | lib/api/abs/endpoints/auth_api.dart:30-36; api_routes.dart:15 | none | none (2xx only) | CONFIRMED (callers: lib/features/auth/logic/servers_provider.dart:10-19 pingServer; lib/features/auth/logic/add_server_notifier.dart:37-39; connection_providers.dart:111-117 LAN probe) |
| GET | /ping | auth_api.dart:38-45; api_routes.dart:17 | none | `success` | CONFIRMED defined; no caller found (NOT called) |
| GET | /status | auth_api.dart:47-54; api_routes.dart:16 | none | ServerStatusResponse: serverVersion, authMethods, authFormData.authOpenIDAutoLaunch, authFormData.authOpenIDButtonText, authFormData.authLoginCustomMessage (models/server_status_response.dart:8-25) | CONFIRMED (serverStatusProvider: lib/features/auth/logic/servers_provider.dart:36-39; used by connection_status_icon.dart:102; add_user_sheet.dart:42-69; user_login_form.dart:60-68; bug_report_provider.dart:31) |
| POST | /login | auth_api.dart:13-28; api_routes.dart:20 | body `{username, password}`; header `x-return-tokens: true` (auth_api.dart:18-22) | LoginResponse: user.id, user.username, user.type, user.accessToken, user.refreshToken, serverSettings, userDefaultLibraryId, Source (models/login_response.dart:9-15) | CONFIRMED (add_user_notifier.dart:44-60; _finalize :129-146) |
| POST | /api/authorize | lib/api/abs/endpoints/server_api.dart:11-15; api_routes.dart:22 | none (no body) | LoginResponse: user.username, user.type, serverSettings (saved, authenticated_user_provider.dart:33-38) | CONFIRMED (lib/app/providers/authenticated_user_provider.dart:25-31; run on each authenticatedUser read, errors except network/timeout rethrown) |
| POST | /logout | server_api.dart:17-19; api_routes.dart:23 | none | none | CONFIRMED (lib/features/auth/logic/user_session_controller.dart:23-31) |
| POST | /auth/refresh | lib/api/abs/client/api_client.dart:83-110 (refreshDio :88-94); api_routes.dart:21 | header `x-refresh-token: <refresh>`; no body | `user.accessToken`, `user.refreshToken` (required, :96-104) | CONFIRMED (called from handleUnauthorized api_client.dart:58-58) |
| GET | /auth/openid | auth_api.dart:56-91; api_routes.dart:24 | query: code_challenge (S256 of verifier), code_challenge_method=S256, response_type=code, redirect_uri=`storii://oauth`, client_id=`Storii`, state (only if passed; caller passes none). followRedirects=false | response 302: `Location` header (authorize URL) and `set-cookie` (joined as Cookie for next call) (:81-88) | CONFIRMED (add_user_notifier.dart:86-101; redirect_uri :38) |
| GET | /auth/openid/callback | auth_api.dart:93-121; api_routes.dart:25 | query: code, state, code_verifier (the raw verifier, :100-106); header `cookie` (from set-cookie), `x-return-tokens: true` | LoginResponse (same as /login) | CONFIRMED (add_user_notifier.dart:103-126) |
| GET | /api/libraries | library_api.dart:9-12; api_routes.dart:28 | none | `libraries[]` (Library.fromJson) | CONFIRMED (LibraryRepository.getAll, library_repository.dart:15; used for library list) |
| GET | /api/libraries/:id | library_api.dart:14-21; api_routes.dart:29 | query `include=filterdata` | LibraryResponse (not enumerated) | CONFIRMED (lib/features/library/logic/active_library_provider.dart:18-19) |
| GET | /api/libraries/:id/items | library_api.dart:23-33; api_routes.dart:30 | query from LibraryItemsRequestParams: limit, page, sort, desc, filter, collapseseries | results, total, limit, page, sortBy, sortDesc, filterBy, mediaType, minified, include, collapseseries (library_items_response) | CONFIRMED (library_items_provider.dart:48; series_provider.dart:23) |
| GET | /api/libraries/:id/items (raw) | library_api.dart:35-45 | same as above | raw Map | CONFIRMED defined; no caller found (getItemsRaw) |
| GET | /api/libraries/:id/recent-episodes | library_api.dart:47-58; api_routes.dart:31 | query limit (default 50), page | RecentEpisodesResponse | CONFIRMED (lib/features/latest_episodes/logic/recent_episodes_provider.dart:18) |
| GET | /api/libraries/:id/personalized | library_api.dart:60-66; api_routes.dart:32 | none | Shelf list | CONFIRMED defined; no caller found |
| GET | /api/libraries/:id/series | library_api.dart:68-78; api_routes.dart:33 | query limit, page, sort, desc, filter | results, total, filterBy, sortBy, minified, include | CONFIRMED (lib/features/series/logic/series_list_provider.dart:50) |
| GET | /api/libraries/:id/series (raw) | library_api.dart:80-90 | same | raw Map | CONFIRMED defined; no caller found |
| GET | /api/libraries/:id/series/:seriesId | library_api.dart:92-99; api_routes.dart:34 | query `include=progress` | Series | CONFIRMED (lib/features/series/logic/series_provider.dart:27) |
| GET | /api/libraries/:id/authors | library_api.dart:101-111; api_routes.dart:35 | query sort, desc, filter | `authors[]` | CONFIRMED (lib/features/author/logic/authors_list_provider.dart:22) |
| GET | /api/authors/:id | library_api.dart:113-120; api_routes.dart:80 | query `include=items,series`; libraryId arg unused in path | Author | CONFIRMED (lib/features/author/logic/author_provider.dart:19) |
| GET | /api/libraries/:id/search | library_api.dart:122-133; api_routes.dart:36 | query q, limit (optional) | SearchResponse | CONFIRMED (lib/features/search/logic/search_provider.dart:39) |
| POST | /api/libraries/:id/scan | library_api.dart:135-141; api_routes.dart:37 | query force=1 or 0 | none | CONFIRMED (lib/features/admin/logic/library_actions_provider.dart:31-33) |
| GET | /api/libraries/:id/collections | library_api.dart:143-149; api_routes.dart:38 | none | `results[]` (Collection) | CONFIRMED (lib/features/collections/logic/collection_providers.dart:47) |
| GET | /api/libraries/:id/playlists | library_api.dart:151-157; api_routes.dart:39 | none | `results[]` (Playlist) | CONFIRMED (lib/features/playlists/logic/playlist_providers.dart:48) |
| GET | /api/items/:id | item_api.dart:22-36; api_routes.dart:59 | query `expanded=1` (default), `include=progress` (optional) | LibraryItem: id, libraryId, mediaType, media, userMediaProgress, addedAt, updatedAt, recentEpisode, isMissing, isFile, relPath, path, size | CONFIRMED (ItemRepository.getItem item_repository.dart:17-30; lib/features/item/logic/item_detail_provider.dart:48) |
| POST | /api/items/:id/play | item_api.dart:38-51; api_routes.dart:63 | body PlayItemRequestParams (play_item_request_params.dart:8-31, json omits defaults): deviceInfo{clientName "Storii", clientVersion, deviceId, deviceName, manufacturer, model, sdkVersion}, supportedMimeTypes[], mediaPlayer ("Storii just_audio", constants.dart:9); forceDirectPlay/forceTranscode omitted when false | PlaybackSession: id, audioTracks[], libraryItemId, episodeId, libraryId, currentTime, duration, playMethod, startedAt, updatedAt, timeListening, displayTitle, displayAuthor, mediaType, mediaPlayer, chapters, coverPath, libraryItem, user, date, dayOfWeek, startTime, deviceInfo (playback_session.dart). Track: index, contentUrl, mimeType, startOffset, duration, title, ino | CONFIRMED (session_notifier.dart:28-50; session_repository.dart:14-26; params from play_request_params.dart:13-57) |
| POST | /api/items/:id/play/:episodeId | item_api.dart:38-51; api_routes.dart:64 | same as above | same as above | CONFIRMED (same call; used when episodeId set, item_api.dart:43-46) |
| GET | /api/items/:id/cover?raw=1 | item_api.dart:53-65; api_routes.dart:62 | query raw=1; responseType bytes | image bytes | CONFIRMED (ItemRepository.getCover item_repository.dart:32-39; download cover download_engine.dart:206-218) |
| POST | /api/items/:id/cover | item_api.dart:67-87 | multipart field `cover` (file) or JSON `{url}` (data_builder) | UpdateCoverResponse | CONFIRMED (lib/features/item/logic/item_metadata_notifier.dart:86) |
| DELETE | /api/items/:id/cover | item_api.dart:89-91 | none | none | CONFIRMED (item_metadata_notifier.dart:111-115) |
| POST | /api/items/:id/scan | item_api.dart:93-100; api_routes.dart:65 | none | `result` (ScanItemResult) | CONFIRMED (more_options_widget.dart:237 via ItemRepository.scanItem item_repository.dart:41-46) |
| POST | /api/items/:id/match | item_api.dart:102-112; api_routes.dart:66 | body MatchItemRequestParams (title, author, overrideDefaults, isbn, asin, ...) | MatchItemResponse | CONFIRMED defined (ItemRepository.matchItem item_repository.dart:57-64); no feature caller found |
| PATCH | /api/items/:id/media | item_api.dart:114-125; api_routes.dart:61 | body UpdateItemMediaReqParams (metadata: title, subtitle, authors, narrators, series, genres, publishedYear, publishedDate, publisher, description, isbn, asin, language, tags, autoDownloadEpisodes, lastEpisodeCheck, ...) | `libraryItem` | CONFIRMED (item_metadata_notifier.dart:52-60) |
| GET | /api/me | me_api.dart:16-19; api_routes.dart:121 | none | User (accessToken/refreshToken fields exist in model) incl. mediaProgress[] | CONFIRMED (lib/app/providers/user_provider.dart:21; media_progress_map_provider.dart:19 uses serverUser.mediaProgress) |
| GET | /api/me/listening-sessions | me_api.dart:21-32; api_routes.dart:122 | query itemsPerPage, page | UserSessionsResponse | CONFIRMED defined; no caller found (MeRepository.getSessions me_repository.dart:110-116 unused) |
| GET | /api/me/listening-stats | me_api.dart:34-40; api_routes.dart:123 | none | UserStatsResponse | CONFIRMED (lib/features/stats/logic/stats_provider.dart:12) |
| GET | /api/me/progress/:id/remove-from-continue-listening | me_api.dart:42-50; api_routes.dart:125-126 | none (method GET) | User | CONFIRMED (lib/features/item/logic/user_progress_actions.dart:59-65) |
| GET | /api/me/progress/:libraryItemId[/:episodeId] | me_api.dart:52-61; api_routes.dart:127 | none | MediaProgress (id, libraryItemId, episodeId, currentTime, duration, progress, isFinished, lastUpdate, startedAt, finishedAt, hideFromContinueListening) | CONFIRMED defined; no caller found (MeRepository.getMediaProgress unused) |
| PATCH | /api/me/progress/:libraryItemId[/:episodeId] | me_api.dart:63-73; api_routes.dart:127 | body UpsertProgressRequestParams (keys: currentTime, duration, progress, isFinished, hideFromContinueListening, finishedAt, startedAt; nulls dropped). Callers send only isFinished | none | CONFIRMED (user_progress_actions.dart:17-32 markComplete; :43-57 removeEpisodeProgress sends isFinished:false) |
| DELETE | /api/me/progress/:id | me_api.dart:75-80; api_routes.dart:127 | none | none | CONFIRMED (user_progress_actions.dart:34-41) |
| POST | /api/me/item/:id/bookmark | me_api.dart:82-93; api_routes.dart:128 | body `{time: <int seconds>, title}` | AudioBookmark | CONFIRMED (lib/features/player/logic/bookmarks_provider.dart:20-36) |
| PATCH | /api/me/item/:id/bookmark | me_api.dart:95-106 | body `{time: <int seconds>, title}` | AudioBookmark | CONFIRMED (bookmarks_provider.dart:37-53) |
| DELETE | /api/me/item/:id/bookmark/:time | me_api.dart:108-116; api_routes.dart:129 | path time in int seconds | none | CONFIRMED (bookmarks_provider.dart:54-63) |
| GET | /api/me/items-in-progress | me_api.dart:118-125; api_routes.dart:124 | query limit (default 25) | `libraryItems[]` | CONFIRMED defined; MeRepository.getItemsInProgress (me_repository.dart:102-104) has no feature caller found |
| GET | /api/me/series/:id/remove-from-continue-listening | me_api.dart:127-135; api_routes.dart:131 | none (method GET) | User | CONFIRMED (user_progress_actions.dart:68-74; more_options_widget.dart:172) |
| GET | /api/me/series/:id/readd-to-continue-listening | me_api.dart:137-145; api_routes.dart:132-133 | none (method GET) | User | CONFIRMED (user_progress_actions.dart:77-83; series_options_widget.dart:103) |
| GET | /api/sessions | sessions_api.dart:14-25; api_routes.dart:153 | query user (optional), itemsPerPage=10, page=10 (default page is 10, likely a bug; expected 0) | SessionsResponse | CONFIRMED defined; no caller found |
| DELETE | /api/session/:id | sessions_api.dart:27-29; api_routes.dart:157 | none | none | CONFIRMED defined; no caller found |
| POST | /api/session/local | sessions_api.dart:31-37; api_routes.dart:155 | body: PlaybackSession JSON, "stripped" (user, audioTracks, libraryItem, chapters, mediaMetadata cleared; session_extensions.dart:167-175) | none | CONFIRMED (session_notifier.dart:129; sessions_cleanup.dart:28-30 and 47-49) |
| POST | /api/session/local-all | sessions_api.dart:39-52; api_routes.dart:156 | body `{sessions: [PlaybackSession stripped...]}` | `results[]`: id, success, progressSynced, error (sync_local_session_response.g.dart) | CONFIRMED defined (SessionRepository.syncLocalSessions session_repository.dart:54-60); no feature caller found |
| GET | /api/session/:id | sessions_api.dart:54-60; api_routes.dart:157 | none | PlaybackSession | CONFIRMED defined (SessionRepository.getSession session_repository.dart:46-48); no feature caller found |
| POST | /api/session/:id/sync | sessions_api.dart:62-71; api_routes.dart:158 | body `{currentTime, timeListened}` (Duration as precise seconds, json_converters.dart:43-60) | none | CONFIRMED (session_notifier.dart:150-164; sessions_cleanup.dart:34-41) |
| POST | /api/session/:id/close | sessions_api.dart:73-82; api_routes.dart:159 | body null (no params passed at session_notifier.dart:204) | none | CONFIRMED (session_notifier.dart:193-215) |
| GET | /api/search/providers | server_api.dart:21-29; api_routes.dart:171 | none | providers (MetadataProvidersResponse) | CONFIRMED defined; no caller found |
| GET | /api/search/books | search_api.dart:10-28; api_routes.dart:172 | query provider, id, title, author (optional) | SearchBook[] | CONFIRMED defined; no caller found |
| GET | /api/search/covers | search_api.dart:30-50; api_routes.dart:175 | query provider, id, title, author, isPodcast=1 | `results` | CONFIRMED defined; no caller found |
| GET | /api/podcasts/:id/search-episode | search_api.dart:65-77 (commented out); api_routes.dart:176 | query title | episodes | NOT CALLED (commented code) |
| GET | /api/search/podcasts, /api/search/chapters | api_routes.dart:173-174 | - | - | NOT CALLED (defined only, no method) |
| POST | /api/collections | collections_api.dart:9-18; api_routes.dart:89 | body CreateCollectionRequestParams {libraryId, name, books[]} | Collection | CONFIRMED (collection_providers.dart:124) |
| GET | /api/collections/:id | collections_api.dart:20-26; api_routes.dart:90 | none | Collection | CONFIRMED (collection_providers.dart:74) |
| PATCH | /api/collections/:id | collections_api.dart:28-51 | updateMetadata body {name, description}; reorder body {books[]} | Collection | CONFIRMED (collection_providers.dart:91, :142) |
| DELETE | /api/collections/:id | collections_api.dart:53-55 | none | none | CONFIRMED (collection_providers.dart:158) |
| POST | /api/collections/:id/batch/add | collections_api.dart:57-67; api_routes.dart:91 | body `{books: [libraryItemId...]}` | Collection | CONFIRMED (collection_providers.dart:171) |
| POST | /api/collections/:id/batch/remove | collections_api.dart:69-79; api_routes.dart:92 | body `{books: [...]}` | Collection | CONFIRMED (collection_providers.dart:187) |
| POST | /api/playlists | playlists_api.dart:9-16; api_routes.dart:104 | body CreatePlaylistRequestParams {libraryId, name, items[]} | Playlist | CONFIRMED (playlist_providers.dart:120) |
| GET | /api/playlists/:id | playlists_api.dart:18-24; api_routes.dart:105 | none | Playlist | CONFIRMED (playlist_providers.dart:75) |
| PATCH | /api/playlists/:id | playlists_api.dart:26-49 | updateMetadata body {name, description}; reorder body {items[]} (PlaylistItem: libraryItemId, episodeId?) | Playlist | CONFIRMED (playlist_providers.dart:92, :140) |
| DELETE | /api/playlists/:id | playlists_api.dart:51-53 | none | none | CONFIRMED (playlist_providers.dart:156) |
| POST | /api/playlists/:id/batch/add | playlists_api.dart:55-65; api_routes.dart:106 | body `{items: [{libraryItemId, episodeId?}]}` | Playlist | CONFIRMED (playlist_providers.dart:170) |
| POST | /api/playlists/:id/batch/remove | playlists_api.dart:67-77; api_routes.dart:107 | body `{items: [...]}` | Playlist | CONFIRMED (playlist_providers.dart:188) |
| POST | /api/playlists/collection/:collectionId | playlists_api.dart:79-85; api_routes.dart:108 | none | Playlist | CONFIRMED defined; no caller found |
| GET | /api/items/:id/file/:ino/download | api_routes.dart:60, 68-70; download_engine.dart:28-38; download_transport.dart:76-87 | headers: `Authorization: Bearer <token>`, `Range: bytes=<n>-` when resuming (download_transport.dart:80-82) | body stream; status 206 vs 200 checked (download_transport.dart:96; download_track_transfer.dart:78-94) | CONFIRMED |
| GET | /public/session/:id/track/:index | api_routes.dart:160, 165-166; item_repository.dart:85-86; session_extensions.dart:82-91 | none (no auth header; AppAudioSource headers empty, session_extensions.dart:124-129) | audio stream | CONFIRMED (played via just_audio ProgressiveAudioSource, just_audio_player.dart:145-149) |
| GET | /api/items/:id/cover (display) | cover_url_provider.dart:9-33; api_routes.dart:62; item_repository.dart:81-83 (playback art: `?raw=1`) | query ts=<updatedAt ms>, width (600 grid / 200 list defaults, user_settings.dart:83-84, 263-267), or raw=1 | image | CONFIRMED (rendered by CachedNetworkImage, image_widget.dart:110-117; no auth header) |
| GET | /api/authors/:id/image | api_routes.dart:81, 84; cover_url_provider.dart:19-21 | query ts, width or raw | image | CONFIRMED (callers author_card.dart:31, author_list_tile.dart:34, author_detail_screen.dart:49) |

Not found as client calls: /api/podcasts RSS or feed endpoints; podcast episode download endpoints; /hls/*; /s/* (share); /api/me/ereader; any other /public/* route besides /public/session; /api/sessions/open (defined api_routes.dart:154, no caller found); /api/me/progress GET list. Note: on refresh failure the client only clears local state (forceLogout) and does not call POST /logout.

## Auth
- Login: POST /login with `{username, password}` and header `x-return-tokens: true` (auth_api.dart:13-28). Response read: `user.accessToken`, `user.refreshToken`, `user.id`, `user.username`, `user.type` (add_user_notifier.dart:129-143). Tokens stored in flutter_secure_storage per user (token_provider.dart:7-12; token storage saveTokens/getAccessToken, lib/storage/secure_storage/token_storage.dart:16-40).
- Access token sent as `Authorization: Bearer <access>` on every main-client request (interceptors.dart:11-21; api_client.dart:79). Also on download requests (download_transport.dart:80). NOT on covers (CachedNetworkImage, image_widget.dart:113-117), NOT on `/public/session/.../track` (session_extensions.dart:124-129), NOT as `?token=` query anywhere (grep: no `token=` or `'token'` query keys). Socket uses emit, see below. No cookie except the OIDC callback (auth_api.dart:82-88, 109).
- Refresh: triggered by a 401 on any request except paths containing `/auth/refresh` or `/login` (interceptors.dart:28-34). `handleUnauthorized` (api_client.dart:44-76): if the stored access token differs from the one that failed, retry with it (:49-56); otherwise single-flight refresh via `_refreshFuture` (:58-75). Refresh = POST `/auth/refresh` with `x-refresh-token` (api_client.dart:91-94); success needs body `user.accessToken` and `user.refreshToken` (:96-104); on failure calls `onTokensFailure` → `forceLogout` (api_providers.dart:59-63; user_session_controller.dart:42-45) and throws 401.
- CONFIRMED finding: `refreshAccessToken` catches every exception and returns false (api_client.dart:106-109). A network error during refresh therefore forces logout.
- CONFIRMED finding: refresh Dio is built with only `baseUrl` and interceptors (api_client.dart:88-89); per-server custom headers (`headers`, api_providers.dart:34-39) are not sent on the refresh request.
- API keys: NOT FOUND.
- Custom per-server headers (serversStore headers): applied to main ApiClient (api_providers.dart:34-39), AuthApi (:75-80), socket handshake (socket_api.dart:25; api_providers.dart:109-116). NOT applied to refresh (above), downloads (download_transport.dart:52-57 Dio has no headers), covers (image_widget.dart:113-117), or just_audio playback (session_extensions.dart:124-129). CONFIRMED.
- OIDC / SSO flow:
  1. Client makes `code_verifier` (43 chars, generateRandom, oauth_helpers.dart:7-14) and S256 `code_challenge` (oauth_helpers.dart:17-21).
  2. GET `/auth/openid` with `code_challenge`, `code_challenge_method=S256`, `response_type=code`, `redirect_uri=storii://oauth`, `client_id=Storii` (add_user_notifier.dart:38, 86-101; auth_api.dart:56-91). Redirects not followed; takes 302 `Location` and `set-cookie`.
  3. Opens `Location` in external browser (add_user_notifier.dart:103-104; url_launcher).
  4. Deep link `storii://oauth?code&state` handled by deep_link_controller.dart:15-17 and the completer in add_user_notifier.dart:62, 106-115.
  5. GET `/auth/openid/callback` with `code`, `state`, and `code_verifier` in the query, header `cookie` from step 2 and `x-return-tokens: true` (auth_api.dart:93-121; add_user_notifier.dart:117-126). Timeout 5 min (:106-109).
  - Observation: no client-generated `state` is sent in step 2 (add_user_notifier.dart:91-96), and the client only checks that a state is present in the callback (:111-115). `code_verifier` is sent as a query parameter in step 5.
- `x-return-tokens: true` on login and OIDC callback (auth_api.dart:21, 109).
- TLS: `HttpOverrides.global` = CustomHttpOverrides trusting all bad certificates when `trustAllCertificatesProvider` is on (lib/app/init.dart:65-68; lib/app/security/custom_http_overrides.dart:3-11). Applies to all dart:io HTTP.
- Connection to a server uses `/healthcheck` before saving (add_server_notifier.dart:37-39).

## Socket.IO
- Library: socket_io_client 3.1.6 (pubspec.yaml:59; pubspec.lock:1206), socket_io_common 3.1.1 (:1214). Import `socket_io_client` as `io` (socket_api.dart:5).
- Connection URL: `baseUrl = activeServerUrl.toString()` (api_providers.dart:105-116), passed to `io.io(baseUrl, options)` (socket_api.dart:21-27). No explicit `path` option, so the library default `/socket.io` applies; with a sub-path server URL the path is passed through as given, behaviour NOT VERIFIED (no pub source in sandbox).
- Transports: `setTransports(['websocket'])` only (socket_api.dart:22-24). No polling fallback. CONFIRMED.
- Auth: after `connect`, client emits `auth` with the access token as the sole argument (socket_api.dart:153-156 `_socket.emit('auth', token)`). Custom headers go as `extraHeaders` on the handshake (socket_api.dart:25). No query params. No ack used anywhere.
- Reconnection: `disableAutoConnect()` (socket_api.dart:24), manual `connect()` (socket_api.dart:78, 95). No reconnection options set, so library defaults apply (NOT VERIFIED).
- Lifecycle handlers: `connect` → `_handleConnect` (socket_api.dart:63, 106-129: reads token via getToken, skips if same as last sent or previously rejected, emits auth); `disconnect` → `_handleDisconnect` (:64, 131-137); `connect_error` → `_handleDisconnect` (:65-68).
- Events listened (SocketEvents.on, socket/events/socket_events.dart:57-70):
  - `init` (socket_api.dart:75, 139-151): payload InitEvent `userId`, `username`, `usersOnline` (init_event.g.dart). Sets status ready.
  - `auth_failed` (socket_api.dart:70-73, 158-167): sets status authFailed, records failed token, disconnects, calls onAuthFailure which only logs (api_providers.dart:118-120).
  - `user_updated` (lib/api/abs/socket/events/user_events.dart:100-101).
  - `user_item_progress_updated` (user_events.dart:103-106; consumed media_progress_map_provider.dart:28-34).
  - `item_updated` (item_events.dart:29; consumed item_detail_provider.dart:66).
  - `item_added` (item_events.dart:27) commented out, NOT listened.
  - `task_started`, `task_updated`, `task_finished` (task_events.dart:85-92; consumed admin task_stream_provider.dart:15-31).
  - `collection_added`, `collection_updated`, `collection_removed` (collection_events.dart:7-16; consumed collection_events_provider.dart:15-31).
  - `playlist_added`, `playlist_updated`, `playlist_removed` (playlist_events.dart:37-44; consumed playlist_events_provider.dart:15-31).
- Events emitted: only `auth` (socket_api.dart:155). CONFIRMED.
- Auth error handling: `auth_failed` handled; `invalid_token` and any other auth error event NOT handled. A token read failure also sets authFailed (socket_api.dart:110-116). No refresh is triggered on `auth_failed` (socket_api.dart:70-73, 158-167); `_failedToken` blocks retry with the same token until changed or `refresh(force: true)` (:90-93, 121-124; connection_status_icon.dart:168).
- Re-auth triggers: app foreground (api_providers.dart:123-126), connectivity back online (:127-129), token stream change (:131-133, token_storage.dart:88-94).
- Reconnect sync: session_sync_watcher.dart:109-125 runs sessionsCleanup on socket connect.

## Playback, sessions, progress
- Start: POST `/api/items/:id/play` (or `/play/:episodeId`) with PlayItemRequestParams (item_api.dart:38-51; session_notifier.dart:28-50). Body fields: deviceInfo{clientName "Storii", clientVersion, deviceId, deviceName, manufacturer, model, sdkVersion}, supportedMimeTypes (platform list, audio_mime_helper.dart:118-130+), mediaPlayer "Storii just_audio" (constants.dart:9). `forceDirectPlay`, `forceTranscode`, `mediaPlayer` default values are stripped from JSON (play_item_request_params.dart:9-13, 29-30). No playMethod or forceTranscode set by the client.
- Session read: `id`, `audioTracks[]` (index, contentUrl, mimeType, startOffset, duration, title, ino), `playMethod`, `currentTime`, `duration`, `libraryItemId`, `episodeId`, `displayTitle`, `displayAuthor`, `chapters`, `mediaMetadata`, `mediaType`.
- Audio URL: per track, `serverUrl.resolve("/public/session/<id>/track/<index>")` (item_repository.dart:85-86; session_extensions.dart:82-91). Used as ProgressiveAudioSource (just_audio_player.dart:145-149). HLS and DASH branches exist but are chosen only by client-side mime (`AudioMimeHelper.resolve`, audio_mime_helper.dart:17-40, 60-61; just_audio_player.dart:123-141). No `/hls` URL is requested by the client.
- Track contentUrl is used only as MediaItem id (session_extensions.dart:95-98). Offline: local file paths replace URLs for downloaded tracks (session_extensions.dart:73-91).
- Sync: POST `/api/session/:id/sync` with `{currentTime, timeListened}` (session_notifier.dart:150-164). Interval default 20 s, metered 60 s (settings_provider.dart:58-59; app_settings.dart:32-34; playback_sync_service.dart:43-52). Also sync on play, pause, seek, stop, complete (playback_sync_service.dart:60-80).
- Local sessions (playMethod local, created offline): POST `/api/session/local` with stripped session (session_notifier.dart:113-138; sessions_cleanup.dart:28-30). Batch `local-all` is defined but not called.
- Close: POST `/api/session/:id/close`, no body (session_notifier.dart:193-215).
- Session sweep: POST `/api/session/local` or `/api/session/:id/sync` per saved session (sessions_cleanup.dart:20-60).
- Progress: no PATCH of progress from the playback path. Progress comes from `/api/session/:id/sync` and server socket event `user_item_progress_updated`. Explicit progress writes: PATCH `/api/me/progress/...` with `isFinished` only (user_progress_actions.dart:17-57). Progress batch endpoint: NOT FOUND.
- Bookmarks: POST/PATCH `/api/me/item/:id/bookmark` `{time (int seconds), title}`; DELETE `/api/me/item/:id/bookmark/:time` (bookmarks_provider.dart:20-63).
- Continue-listening remove/re-add: GET `/api/me/progress/:id/remove-from-continue-listening`, GET `/api/me/series/:id/remove-from-continue-listening` and `/readd-to-continue-listening` (user_progress_actions.dart:59-83). Note these use GET (me_api.dart:42-50, 127-145).
- Deep link `storii://play?id=` starts playback via queueProvider (deep_link_controller.dart:19-26).
- Android Auto cover art is served through a content:// provider wrapping the raw cover URL (android_auto_extensions.dart:17-24, 30).

## Downloads, ranges, media
- Audio download URL: `user.serverUrl.resolve("/api/items/:id/file/:ino/download")` (download_engine.dart:28-33). Uses stored remote URL, not the LAN URL.
- Auth: Bearer token from tokenProvider (download_engine.dart:55; download_transport.dart:80). Downloads use a separate Dio with no interceptors (download_transport.dart:52-57), so a 401 is not refreshed; track retries repeat the same token (download_track_transfer.dart:53-144).
- Range: resume sends `Range: bytes=<bytesOnDisk>-` when partial data exists (download_track_transfer.dart:59-62, 71-76; download_transport.dart:81-82). Expected responses:
  - if range requested and status 200 (not 206): partial file deleted and restarted from 0 (download_track_transfer.dart:78-88);
  - if no range requested and status 206: error (:90-94);
  - 206 treated as partial success (download_transport.dart:96).
  - 416 is not handled specially; it becomes a DioException and goes to retry (download_track_transfer.dart:112-143).
- Content-type: not checked for downloads; file is written with extension from mimeType (download_track_transfer.dart:64-68).
- Retries: 3 retries with 500 ms, 1000 ms, 2000 ms delays (download_track_transfer.dart:36-40, 53, 142).
- Timeouts: connect 30 s, receive 10 min (download_transport.dart:52-55).
- Cover download for offline: GET `/api/items/:id/cover?raw=1` as bytes (item_api.dart:53-65; download_engine.dart:206-218), saved to documents dir (cover_helper.dart:17-49).
- Playback streaming range: handled inside the just_audio fork (pubspec.yaml:42-46); whether it sends Range is NOT VERIFIED (no source in repo).
- HLS: client has HlsAudioSource path (just_audio_player.dart:130-136) but no HLS URL is built by the app. NOT FOUND for `/hls`.

## Caching and covers
- Main API client: `DioCacheInterceptor` with `CacheOptions(store: networkCacheStore, policy: refreshForceCache, hitCacheOnNetworkFailure: true, maxStale: 7 days)` (api_providers.dart:27-32, 46). Store: HiveCacheStore at `<docs>/dio_cache` (boxes.dart:43-44, 110-112). Interceptor order: AuthInterceptor (api_client.dart:41), LogsInterceptor, OfflineCacheInterceptor, DioCacheInterceptor (api_providers.dart:40-47). Only GET is cached by default (INFERRED from package defaults, not verified).
- Offline mode: `OfflineCacheInterceptor` forces cache policy and sets 1 s connect/receive timeouts when the server is offline (offline_cache_interceptor.dart:10-21; isServerOffline from socket state, connection_providers.dart:76-81).
- Cache clear: `networkCacheStore.clean()` and dir delete (app_cache.dart:28-33).
- ETag / If-None-Match / 304 / Cache-Control: NOT FOUND in app code. Any conditional request behaviour comes from dio_cache_interceptor with refresh policies (INFERRED, not verified).
- Download transport and media streams: no cache layer (download_transport.dart; just_audio sources).
- Images: `CachedNetworkImage` (cached_network_image_ce 4.12.0) with `AppImageCacheManager extends DefaultCacheManager(maxNrOfCacheObjects: 5000)` (image_widget.dart:113-117; app_cache.dart:36-41). Header-based caching from flutter_cache_manager 3.4.5 (INFERRED). Same manager for player background and dominant color (themed_background.dart:42-44; dominant_color.dart:12).
- Cover URL: `{serverUrl}/api/items/:id/cover` or `/api/authors/:id/image`, query `ts=<updatedAt ms>` (cache busting; cover_url_provider.dart:27-28), `width=<n>` (600 grid, 200 list defaults; user_settings.dart:83-84) when not raw, or `raw=1`. No `format=` (webp/jpeg) parameter anywhere (grep: NOT FOUND).
- Playback artUri: `/api/items/:id/cover?raw=1` (item_repository.dart:81-83; session_extensions.dart:28).

## Server version branches
- No version comparison found. `serverVersion` is only displayed: connection_status_icon.dart:145-146 and bug_report_provider.dart:25-33, 71.
- `isInit` in ServerStatusResponse (models/server_status_response.dart:12-13) is not read by any feature.
- Minimum supported server version: NOT FOUND.
- Client version sent to server: `clientVersion` in deviceInfo (play_request_params.dart:49).

## Not found (searched terms)
- "hls", "m3u8", "/hls": only client mime-type handling (audio_mime_helper.dart), no URLs.
- "/s/", "share", "shareUrl": NOT FOUND as HTTP paths.
- "ereader", "ebook" endpoints: NOT FOUND (ebook_file model only).
- "rss", "/feed", "podcasts/" paths, "download-episodes", "search-episode" (commented out): NOT FOUND as active calls.
- "apiKey", "api_key", "X-API-Key": NOT FOUND.
- "token=", "?token", "'token'" as query key: NOT FOUND.
- "ETag", "If-None-Match", "Last-Modified", "Cache-Control", "304": NOT FOUND in app code.
- "format=" / "webp" / "jpeg" in URLs: NOT FOUND.
- "Range" header: only download_transport.dart:81-82.
- "serverVersion" compares, "semver", "version >=": NOT FOUND.
- "invalid_token", "unauthorized" socket events: NOT FOUND (only auth_failed).
- socket acks (`emit(` with ack, `emitWithAck`): NOT FOUND (only one emit).
- "x-refresh-token" other than refresh: only api_client.dart:93.
- "/api/sessions/open" (sessionOpen, api_routes.dart:154): defined, no caller found.
- "/api/me/progress" GET list, "items-in-progress" in UI: MeRepository only.

## Dynamic call sites (paths built at runtime you could not resolve)
- Host and path of `user.serverUrl.resolve(...)` for cover and download: resolved at runtime from stored server URL (cover_url_provider.dart:23-24; download_engine.dart:30-33).
- `ApiRoutes._build` template substitution with user-provided ids (api_routes.dart:2-12). Ids come from server responses (library item id, ino, session id, track index, episode id, time seconds, collection/playlist ids).
- `AbsUrls.webClient` and `authConfig` (abs_urls.dart:4-6): `serverUri.resolve("audiobookshelf/...")`, used only for authConfig (user_login_form.dart:142). Opens a browser URL; not an API call.
- Track URLs: `/public/session/<sessionId>/track/<track.index>`; sessionId comes from server response, index from track.index (session_extensions.dart:82-91).
- Local session ids: `const Uuid().v4()` (session_extensions.dart:226).
- Deep-link values (`storii://play?id=`, `episodeId`) feed the playback call (deep_link_controller.dart:19-26).
- Socket URL: activeServerUrl at connect time, which may switch between LAN and remote (connection_providers.dart:83-117).
