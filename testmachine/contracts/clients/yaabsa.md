# yaabsa inventory
- Repo: https://github.com/Vito0912/yaabsa  Commit: 6ff482e3febaaa1bffc5c6b757da241c9a1a3120  Platform: Flutter app (Android, iOS, macOS, Windows, Linux, web; Android Auto and Wear OS variants)  Language/stack: Dart (SDK ^3.13.0), Dio 5.11.0 (+ native_dio_adapter 1.8.0), socket_io_client 3.1.6, Riverpod, sembast/drift, background_downloader, just_audio (vendored under packages/)
- Permalink base: https://github.com/Vito0912/yaabsa/blob/6ff482e3febaaa1bffc5c6b757da241c9a1a3120/

## Requested confirmations
- **GET /api/me/progress: CONFIRMED.** Defined at `lib/api/routes/me_api.dart:203-222` (route literal line 209, no query params). Called at `lib/provider/common/media_progress_provider.dart:441` (inside `_requestAllRemoteProgress`, lines 428-443). The call is gated on server version: on servers below 2.36 the client calls `GET /api/me` and reads `mediaProgress` instead (`media_progress_provider.dart:432-438`). Second caller: `lib/screens/wear/wear_player_screen.dart:137` (`_fetchAllProgress`, lines 132-137, same version gate).
- **GET /api/playlists: CONFIRMED.** Defined at `lib/api/routes/list_api.dart:14-34` (route line 26, no query). Callers: `lib/provider/common/playlist_provider.dart:52` (`PlaylistsNotifier._fetchPlaylists`, filters by libraryId client-side), `lib/util/audio_handler/bg_audio_handler_auto_queue.dart:671` (Android Auto playlist queue), `lib/util/audio_handler/auto/bg_audio_handler_android_auto_data.dart:278` (sets `doNotCache`).
- **GET /api/collections: CONFIRMED.** Defined at `lib/api/routes/list_api.dart:53-73` (route line 65, no query). Callers: `lib/provider/common/collection_provider.dart:52` (`CollectionsNotifier._fetchCollections`), `lib/util/audio_handler/bg_audio_handler_auto_queue.dart:721`, `lib/util/audio_handler/auto/bg_audio_handler_android_auto_data.dart:251` (sets `doNotCache`).
- Single-item variants also exist and are called: `GET /api/playlists/:id` (`list_api.dart:43`, called from `lib/provider/player/queue_source_provider.dart:83` with `noCache`), `GET /api/collections/:id` (`list_api.dart:82`, called from `queue_source_provider.dart:93`).
- Socket client: `socket_io_client` ^3.1.6 (`pubspec.yaml:77`), locked at 3.1.6 (`pubspec.lock:1869-1876`). HTTP: Dio 5.11.0 (`pubspec.lock:456-463`). No other HTTP client package (no `http:`, retrofit, chopper, or dio_cache package in `pubspec.yaml`).

## HTTP calls
Route paths are literal strings in the source, prefixed with `/api` or an auth/status path, and resolved by Dio against the server base URL (see Auth and Base URL below). All rows are CONFIRMED from reading the call site unless marked INFERRED. "Request fields" lists body or query keys the client sends. "Response fields read" lists the main keys read through the typed model (`fromJson`), not every field of the model.

| Method | Path | File:lines | Request fields | Response fields read | Status |
|---|---|---|---|---|---|
| GET | /status | lib/api/routes/me_api.dart:442-456 (route 448) | none | app, serverVersion, isInit, language, authMethods, authFormData (`lib/api/me/status.dart:9-16`) | CONFIRMED |
| GET | /ping | lib/api/routes/me_api.dart:426-440 (route 432) | none | none (`fromJson: (data) => null`) | CONFIRMED |
| POST | /login | lib/api/routes/me_api.dart:23-44 (route 36, body line 38) | body `{username, password}` (`lib/api/me/request/login_request.dart:6-8`); header `x-return-tokens: true` when `returnTokens` (me_api.dart:31-33) | Login: user, userDefaultLibraryId, serverSettings, Source (`lib/api/me/login.dart:11-14`); user: id, username, token, accessToken, refreshToken, apiKey, permissions (`lib/api/me/user.dart:17-42`) | CONFIRMED |
| POST | /api/authorize | lib/api/routes/me_api.dart:70-84 (route 76) | body `{}` | Login (as above) | CONFIRMED |
| POST | /auth/refresh | lib/api/routes/me_api.dart:86-109 (route 101) | header `x-refresh-token`; body `{}`; extra `skip_auth_refresh` | Login (as above) | CONFIRMED |
| POST | /logout | lib/api/routes/me_api.dart:111-133 (route 124) | header `x-refresh-token`; body `{}`; extra `skip_auth_refresh` | none (returns map, unused) | CONFIRMED |
| PATCH | /api/me/password | lib/api/routes/me_api.dart:46-68 (route 60) | body `{password, newPassword}`; header `x-refresh-token` (optional) | none | CONFIRMED |
| GET | /auth/openid | lib/provider/core/oidc_provider.dart:78 (in `initiateOidc`, lines 60-124) | query `code_challenge`, `code_challenge_method=S256`, `redirect_uri=yaabsa://oauth`, `client_id=Yaabsa`, `response_type=code`, `state`; `followRedirects: false`; accepts 200-399 | `location` header (line 95); `set-cookie` (line 98) | CONFIRMED |
| GET | /auth/openid/callback | lib/provider/core/oidc_provider.dart:181-183 (in `handleCallbackUri`, lines 129-260) | query `code`, `state`, `code_verifier`; header `Cookie` (from the saved set-cookie, line 167) | Login: user, userDefaultLibraryId, serverSettings (`oidc_provider.dart:186-191`) | CONFIRMED |
| GET | /auth/openid/config (admin) | lib/api/routes/admin_api.dart:290-291 | none | AdminOpenIdIssuerConfig (`lib/api/admin/admin_openid_issuer_config.dart`) | CONFIRMED |
| GET | /api/me | lib/api/routes/me_api.dart:187-201 (route 193) | none (query `{}`) | User incl. mediaProgress, bookmarks, permissions, apiKey, accessToken (`user.dart`); cached (see Caching) | CONFIRMED |
| GET | /api/me/progress | lib/api/routes/me_api.dart:203-222 (route 209) | none | mediaProgress[] (validated at lines 210-214) | CONFIRMED |
| GET | /api/me/progress/:itemId | lib/api/routes/me_api.dart:403-420 (route 410) | none | MediaProgress (`lib/api/me/media_progress.dart:10-27`) | CONFIRMED |
| GET | /api/me/progress/:itemId/:episodeId | lib/api/routes/me_api.dart:412-416 | none | MediaProgress | CONFIRMED |
| PATCH | /api/me/progress/:itemId | lib/api/routes/me_api.dart:458-480 (route 466-471, body 474) and 482-504 (route 490-495) and 536-555 (route 544-546) | three body shapes: full MediaProgress JSON (`createUpdateMediaProgress`, called at `lib/provider/player/session_provider.dart:858`); arbitrary map, e.g. `{isFinished}` (`patchMediaProgress`, callers `lib/components/app/item/item_progress_actions.dart:144` and `:195`); ebook `{ebookLocation, ebookProgress}` (`updateBookProgress`, callers `lib/util/reader/reader_progress_sync.dart:201`, `lib/screens/reader/reader.dart:201`) | none | CONFIRMED |
| PATCH | /api/me/progress/:itemId/:episodeId | lib/api/routes/me_api.dart:458-480 (route 468-471), 482-504 (route 492-495) | same as above | none | CONFIRMED |
| PATCH | /api/me/progress/batch/update | lib/api/routes/me_api.dart:506-534 (route 529, raw `_dio.request`, method PATCH at 517) | JSON array of `{libraryItemId, episodeId?, isFinished}` built at `lib/components/app/item/item_progress_actions.dart:250-258`, sent at :261 | none | CONFIRMED |
| GET | /api/me/items-in-progress | lib/api/routes/me_api.dart:224-239 (route 231) | query `limit` (default 25) | ItemsInProgress | CONFIRMED |
| GET | /api/me/bookmarks | lib/api/routes/me_api.dart:241-255 (route 247) | none | BookmarksResponse | CONFIRMED |
| GET | /api/me/bookmarks/:libraryItemId | lib/api/routes/me_api.dart:257-272 (route 264) | none | BookmarksResponse | CONFIRMED |
| POST | /api/me/item/:itemId/bookmark | lib/api/routes/me_api.dart:135-151 (route 143) | body `{time: int, title: string}` (`lib/api/me/request/create_bookmark_request.dart:9-10`) | Bookmark | CONFIRMED |
| PATCH | /api/me/item/:itemId/bookmark | lib/api/routes/me_api.dart:153-169 (route 161, body 163) | body `CreateBookmarkRequest` (same shape) | Bookmark | CONFIRMED |
| DELETE | /api/me/item/:itemId/bookmark/:time | lib/api/routes/me_api.dart:171-185 (route 179) | none | bool | CONFIRMED |
| GET | /api/users/:userId/listening-stats | lib/api/routes/me_api.dart:274-289 (route 281) | none | UserListeningStats | CONFIRMED |
| GET | /api/me/listening-stats | lib/api/routes/me_api.dart:291-305 (route 297) | none | UserListeningStats | CONFIRMED |
| GET | /api/me/listening-sessions | lib/api/routes/me_api.dart:307-322 (route 315) | query (see file) | ListeningSessionsPage | CONFIRMED |
| GET | /api/users/:userId/listening-sessions | lib/api/routes/me_api.dart:325-340 (route 334) | query (see file) | ListeningSessionsPage | CONFIRMED |
| GET | /api/me/item/listening-sessions/:libraryItemId[/:episodeId] | lib/api/routes/me_api.dart:344-363 (routes 353-359) | none | ListeningSessionsPage | CONFIRMED |
| GET | /api/me/stats/year/:year | lib/api/routes/me_api.dart:369-384 (route 376) | none | YearInReviewStats | CONFIRMED |
| GET | /api/stats/year/:year | lib/api/routes/me_api.dart:386-400 (route 393) | none | YearInReviewStats | CONFIRMED |
| GET | /api/libraries | lib/api/routes/library_api.dart:33 | none | libraries | CONFIRMED |
| POST | /api/libraries | lib/api/routes/library_api.dart:52 | create library payload | library | CONFIRMED |
| PATCH | /api/libraries/:libraryId | lib/api/routes/library_api.dart:70 | update library payload | library | CONFIRMED |
| POST | /api/libraries/order | lib/api/routes/library_api.dart:87 | reorder payload | none | CONFIRMED |
| POST | /api/libraries/:libraryId/scan?force=0\|1 | lib/api/routes/library_api.dart:105 | query `force` | bool | CONFIRMED |
| GET | /api/libraries/:libraryId/matchall | lib/api/routes/library_api.dart:125 | none | bool | CONFIRMED |
| POST | /api/libraries/:libraryId/remove-metadata?ext= | lib/api/routes/library_api.dart:145 | query `ext` | response | CONFIRMED |
| DELETE | /api/libraries/:libraryId | lib/api/routes/library_api.dart:162 | none | response | CONFIRMED |
| GET | /api/libraries/:libraryId/stats | lib/api/routes/library_api.dart:178 | none | LibraryStats | CONFIRMED |
| GET | /api/libraries/:libraryId | lib/api/routes/library_api.dart:201 | none | library details | CONFIRMED |
| GET | /api/libraries/:libraryId/items | lib/api/routes/library_api.dart:234 | library items query (see file) | library items | CONFIRMED |
| GET | /api/libraries/:libraryId/search | lib/api/routes/library_api.dart:253 | `q` and related (see file) | search results | CONFIRMED |
| GET | /api/libraries/:libraryId/series | lib/api/routes/library_api.dart:271 | query (see file) | series | CONFIRMED |
| GET | /api/libraries/:libraryId/authors | lib/api/routes/library_api.dart:289 | none | authors | CONFIRMED |
| GET | /api/authors/:authorId | lib/api/routes/library_api.dart:312 | none | author details | CONFIRMED |
| DELETE | /api/authors/:authorId | lib/api/routes/library_api.dart:329 | none | bool | CONFIRMED |
| GET | /api/series/:seriesId | lib/api/routes/library_api.dart:344 | none | series | CONFIRMED |
| GET | /api/libraries/:libraryId/personalized | lib/api/routes/library_api.dart:361 | none | personalized shelves | CONFIRMED |
| GET | /api/libraries/:libraryId/recent-episodes | lib/api/routes/library_api.dart:380 | none | RecentEpisodesResponse | CONFIRMED |
| GET | /api/items/:itemId | lib/api/routes/library_item_api.dart:57 (fn 50-65) | none | LibraryItem | CONFIRMED |
| POST | /api/items/:id/play | lib/api/routes/library_item_api.dart:77-91 (route 79, POST at 82-90, body 89) | body PlayLibraryItemRequest: `deviceInfo{deviceId, clientName, clientVersion, manufacturer, model, sdkVersion}`, `forceDirectPlay`, `forceTranscode`, `supportedMimeTypes[]`, `mediaPlayer` (`lib/api/library_items/request/play_library_item_request.dart:10-14`; built at `lib/provider/player/session_provider.dart:223-228`, and `lib/util/cast/cast_track_resolver.dart:66-72`) | PlaybackSession: id, libraryItemId, episodeId, playMethod, mediaPlayer, audioTracks[] (contentUrl, mimeType, startOffset, duration, index, title), currentTime, serverVersion, libraryItem (`lib/api/library_items/playback_session.dart:18-42`, `audio_track.dart:12-20`) | CONFIRMED |
| POST | /api/items/:id/play/:episodeId | lib/api/routes/library_item_api.dart:77-91 (route 77) | same as above | same as above | CONFIRMED |
| DELETE | /api/items/:itemId?hard=0\|1 | lib/api/routes/library_item_api.dart:101 | query `hard` | bool | CONFIRMED |
| POST | /api/items/batch/delete?hard=0\|1 | lib/api/routes/library_item_api.dart:121 (via `_postStatusOnly`, line 36, POST) | query `hard` | bool | CONFIRMED |
| POST | /api/items/batch/update | lib/api/routes/library_item_api.dart:142 | batch update request | BatchUpdateLibraryItemsResponse | CONFIRMED |
| POST | /api/items/batch/quickmatch | lib/api/routes/library_item_api.dart:165 (via `_postStatusOnly`) | batch quickmatch request | bool | CONFIRMED |
| POST | /api/items/:itemId/match | lib/api/routes/library_item_api.dart:181 | quick match options | QuickMatchLibraryItemResponse | CONFIRMED |
| PATCH | /api/items/:itemId/media | lib/api/routes/library_item_api.dart:201 | UpdateLibraryItemMediaRequest | UpdateLibraryItemMediaResponse | CONFIRMED |
| DELETE | /api/items/:itemId/file/:ino | lib/api/routes/library_item_api.dart:213 | none | bool | CONFIRMED |
| GET | /api/items/:itemId/ffprobe/:ino | lib/api/routes/library_item_api.dart:224 | none | Map (ffprobe output) | CONFIRMED |
| POST | /api/items/:itemId/chapters | lib/api/routes/library_item_api.dart:245 | chapters payload | Map | CONFIRMED |
| GET | /api/search/chapters | lib/api/routes/library_item_api.dart:264 | query (see file) | Map | CONFIRMED |
| GET | /api/items/:itemId/metadata-object | lib/api/routes/library_item_api.dart:286 | none | metadata object | CONFIRMED |
| POST | /api/tools/item/:itemId/embed-metadata | lib/api/routes/library_item_api.dart:305-321 (via `_postStatusOnly`, route 313) | none | bool | CONFIRMED |
| POST | /api/tools/item/:itemId/encode-m4b | lib/api/routes/library_item_api.dart:331-347 (via `_postStatusOnly`, route 340) | none | bool | CONFIRMED |
| DELETE | /api/tools/item/:itemId/encode-m4b | lib/api/routes/library_item_api.dart:355 | none | bool | CONFIRMED |
| GET | /api/items/:id/file/:ino/download | lib/util/handler/download_handler.dart:767 (URL), :794 (headers), :800 (`allowPause`), `lib/components/app/item/library_item_file_download_dialog.dart:73-78` | none (streaming download via background_downloader) | file bytes | CONFIRMED |
| GET | /api/items/:id/file/:ino | lib/util/subtitles/subtitle_loader.dart:285-295 (Dio `get`, responseType plain, bearer via `secure` extra) | none | text (subtitle file) | CONFIRMED |
| GET | /api/items/:id/file/:ino (URL with `?token=`) | lib/components/app/item/editor/chapters/chapter_editor_preview.dart:55-56 | query `token` (see Auth) | image/audio preview | CONFIRMED |
| GET | /api/items/:id/ebook | lib/screens/reader/reader_core_helpers.dart:39 (URL string) | none (headers not verified) | ebook bytes | CONFIRMED (URL), INFERRED (headers) |
| GET | /api/items/:id/cover | lib/api/routes/library_item_api.dart:399-429 (URL builder; params width 409, height 414, raw 419, ts 424) | query `width`, `height`, `raw=1`, `ts` (see Caching) | image | CONFIRMED |
| GET | /api/authors/:id/image | lib/components/common/author_card.dart:131-135 (URL), :116 (CachedNetworkImage) | query `width` | image | CONFIRMED |
| GET | /public/session/:sessionId/track/:index | lib/api/library_items/audio_track.dart:38 (URL) | none (no auth header for non-HLS, see Playback) | audio bytes | CONFIRMED |
| GET | <track.contentUrl> (server HLS playlist, path is server-supplied) | lib/api/library_items/audio_track.dart:30-38 (used when mimeType contains `mpegurl`) | none | m3u8 | CONFIRMED |
| GET | /api/playlists | lib/api/routes/list_api.dart:14-34 (route 26) | none (query `{}`; cached, see Caching) | PlaylistResponse.playlists[] (id, libraryId, userId, name, description, coverPath, items, lastUpdate, createdAt) | CONFIRMED |
| GET | /api/playlists/:playlistId | lib/api/routes/list_api.dart:36-51 (route 43) | none | Playlist | CONFIRMED |
| POST | /api/playlists | lib/api/routes/list_api.dart:92-115 (route 102) | `libraryId`, `name`, `description?`, `items[]` | Playlist | CONFIRMED |
| PATCH | /api/playlists/:playlistId | lib/api/routes/list_api.dart:117-135 (route 122) | update payload | Playlist | CONFIRMED |
| POST | /api/playlists/:playlistId/batch/add | lib/api/routes/list_api.dart:137-152 (route 140) | items payload | Playlist | CONFIRMED |
| POST | /api/playlists/:playlistId/batch/remove | lib/api/routes/list_api.dart:154-170 (route 158) | items payload | Playlist | CONFIRMED |
| DELETE | /api/playlists/:playlistId | lib/api/routes/list_api.dart:172-186 (route 175) | none | bool | CONFIRMED |
| GET | /api/collections | lib/api/routes/list_api.dart:53-73 (route 65) | none (query `{}`; cached, see Caching) | CollectionResponse.collections[] (id, libraryId, userId, name, description, coverPath, books, lastUpdate, createdAt) | CONFIRMED |
| GET | /api/collections/:collectionId | lib/api/routes/list_api.dart:75-90 (route 82) | none | Collection | CONFIRMED |
| POST | /api/collections | lib/api/routes/list_api.dart:188-210 (route 193) | create payload | Collection | CONFIRMED |
| PATCH | /api/collections/:collectionId | lib/api/routes/list_api.dart:211-228 (route 213) | update payload | Collection | CONFIRMED |
| POST | /api/collections/:collectionId/batch/add | lib/api/routes/list_api.dart:229-246 (route 231) | books payload | Collection | CONFIRMED |
| POST | /api/collections/:collectionId/batch/remove | lib/api/routes/list_api.dart:247-263 (route 249) | books payload | Collection | CONFIRMED |
| DELETE | /api/collections/:collectionId | lib/api/routes/list_api.dart:264-273 (route 266) | none | bool | CONFIRMED |
| GET | /api/sessions | lib/api/routes/session_api.dart:32 (fn 10-35) | page/limit query (see file) | AdminListeningSessionsPage | CONFIRMED |
| GET | /api/sessions/open | lib/api/routes/session_api.dart:48 | none | OpenSessionsResponse | CONFIRMED |
| DELETE | /api/sessions/:sessionId | lib/api/routes/session_api.dart:65 | none | bool | CONFIRMED |
| GET | /api/session/:id | lib/api/routes/session_api.dart:80 (defined; no caller traced) | none | PlaybackSession | CONFIRMED |
| POST | /api/session/:id/close | lib/api/routes/session_api.dart:97 (fn 82-100); callers `lib/provider/player/session_provider.dart:141,169`, `lib/util/cast/cast_track_resolver.dart:91` (8 s timeout) | body `{}` | bool | CONFIRMED |
| POST | /api/session/:sessionId/sync | lib/api/routes/session_api.dart:119 (fn 101-129); callers `lib/provider/player/session_provider.dart:491-500, 631-638`, `lib/util/audio_handler/bg_audio_handler_resume.dart:32` | body `{currentTime, timeListened, duration}` (`lib/api/session/request/sync_session_request.dart:9-11`) | bool | CONFIRMED |
| POST | /api/session/local | lib/api/routes/session_api.dart:144 (fn 133-155); callers `lib/components/settings/admin_server_sessions_view.dart:415`, `lib/components/sessions/library_item_listening_sessions_tab.dart:305`, `lib/components/sessions/current_user_listening_sessions_tab.dart:287` | full PlaybackSession JSON (editor save); preceded by `DELETE /api/sessions/:id` when `deleteFirst` | bool | CONFIRMED |
| GET | /api/search/podcast | lib/api/routes/podcast_api.dart:73 | search term, country (see file) | List<PodcastSearchResult> | CONFIRMED |
| POST | /api/podcasts/feed | lib/api/routes/podcast_api.dart:45 | `rssFeed` (see file) | PodcastFeed | CONFIRMED |
| GET | /api/libraries/:libraryId/podcast-titles | lib/api/routes/podcast_api.dart:98 | none | List<LibraryPodcastTitle> | CONFIRMED |
| POST | /api/podcasts | lib/api/routes/podcast_api.dart:122 | CreatePodcastRequest | CreatePodcastResponse | CONFIRMED |
| POST | /api/podcasts/:libraryItemId/download-episodes | lib/api/routes/podcast_api.dart:145 | episodes payload | bool | CONFIRMED |
| GET | /api/search/providers | lib/api/routes/upload_api.dart:22 | none | SearchProvidersResponse | CONFIRMED |
| GET | /api/filesystem | lib/api/routes/upload_api.dart:42 | query path (see file) | FilesystemDirectory | CONFIRMED |
| POST | /api/filesystem/pathexists | lib/api/routes/upload_api.dart:60 | `{path}` (see file) | PathExistsResponse | CONFIRMED |
| GET | /api/search/books or /api/search/podcasts | lib/api/routes/upload_api.dart:81-91 (endpoint chosen by mediaType, line 81) | search query (see file) | search results | CONFIRMED |
| POST | /api/upload | lib/api/routes/upload_api.dart:113-119 (multipart, method line 119); form built at `lib/components/app/upload/library_upload_panel.dart:962-978` | multipart fields `title`, `author`, `series`, `library`, `folder`, and file parts keyed by index "0", "1", ... | none | CONFIRMED |
| GET | /api/logger-data (admin) | lib/api/routes/admin_api.dart:160 | none | LoggerData | CONFIRMED |
| GET | /api/tasks (admin) | lib/api/routes/admin_api.dart:177 | none | AbsTaskList | CONFIRMED |
| GET | /api/users (admin) | lib/api/routes/admin_api.dart:193, 560 | none | AdminUsersResponse | CONFIRMED |
| GET | /api/users/:userId (admin) | lib/api/routes/admin_api.dart:577 | none | AdminUser | CONFIRMED |
| POST | /api/users (admin) | lib/api/routes/admin_api.dart:595 (fn `_upsertUser`, method 'POST' at 594) | AdminUserUpsertRequest | AdminUser | CONFIRMED |
| PATCH | /api/users/:userId (admin) | lib/api/routes/admin_api.dart:612 (method 'PATCH' at 611) | AdminUserUpsertRequest | AdminUser | CONFIRMED |
| DELETE | /api/users/:userId (admin) | lib/api/routes/admin_api.dart:627 | none | bool | CONFIRMED |
| PATCH | /api/users/:userId/openid-unlink (admin) | lib/api/routes/admin_api.dart:642 (via `_patchStatusOnly`, PATCH at 116-123) | none | bool | CONFIRMED |
| GET | /api/api-keys (admin) | lib/api/routes/admin_api.dart:209 | none | AdminApiKeysResponse | CONFIRMED |
| POST | /api/api-keys (admin) | lib/api/routes/admin_api.dart:511 | CreateAdminApiKeyRequest | AdminApiKeyResponse | CONFIRMED |
| PATCH | /api/api-keys/:apiKeyId (admin) | lib/api/routes/admin_api.dart:529 | UpdateAdminApiKeyRequest | AdminApiKey | CONFIRMED |
| DELETE | /api/api-keys/:apiKeyId (admin) | lib/api/routes/admin_api.dart:546 | none | bool | CONFIRMED |
| GET | /api/backups (admin) | lib/api/routes/admin_api.dart:225 | none | AdminBackupsResponse | CONFIRMED |
| POST | /api/backups (admin) | lib/api/routes/admin_api.dart:414 (`tmpDio` with 10-minute receive timeout, line 412) | none | AdminBackup | CONFIRMED |
| DELETE | /api/backups/:backupId (admin) | lib/api/routes/admin_api.dart:431 | none | bool | CONFIRMED |
| POST | /api/backups/upload (admin) | lib/api/routes/admin_api.dart:453-454 (multipart, method POST at 447) | multipart file | response | CONFIRMED |
| PATCH | /api/backups/path (admin) | lib/api/routes/admin_api.dart:485 (method PATCH at 479) | UpdateBackupPathRequest | none | CONFIRMED |
| GET | /api/backups/:backupId/apply (admin) | lib/api/routes/admin_api.dart:501 (method **GET** at 495, as coded) | none | none | CONFIRMED |
| GET | /api/backups/:backupId/download (admin, URL only) | lib/components/settings/admin_server_backups_view.dart:543-552, launched externally at :562 | query `token` (see Auth) | file | CONFIRMED |
| GET | /api/feeds (admin) | lib/api/routes/admin_api.dart:241 | none | AdminRssFeed list | CONFIRMED |
| POST | /api/feeds/:feedId/close (admin) | lib/api/routes/admin_api.dart:393 (POST at 385) | none | response | CONFIRMED |
| GET | /api/auth-settings (admin) | lib/api/routes/admin_api.dart:257 | none | AdminAuthenticationSettings | CONFIRMED |
| PATCH | /api/auth-settings (admin) | lib/api/routes/admin_api.dart:274 | UpdateAdminAuthenticationSettingsRequest | UpdateAdminAuthenticationSettingsResponse | CONFIRMED |
| GET | /api/emails/settings (admin) | lib/api/routes/admin_api.dart:307 | none | AdminEmailSettingsResponse | CONFIRMED |
| PATCH | /api/emails/settings (admin) | lib/api/routes/admin_api.dart:324 | UpdateAdminEmailSettingsRequest | response | CONFIRMED |
| POST | /api/emails/test (admin) | lib/api/routes/admin_api.dart:348 (`_dio.request`, POST at 340) | `{}` | response | CONFIRMED |
| POST | /api/emails/ereader-devices (admin) | lib/api/routes/admin_api.dart:368 | UpdateAdminEmailEreaderDevicesRequest | response | CONFIRMED |
| GET | /api/tags (admin) | lib/api/routes/admin_api.dart:655 | none | TagsResponse | CONFIRMED |
| GET | /api/genres (admin) | lib/api/routes/admin_api.dart:671 | none | GenresResponse | CONFIRMED |
| POST | /api/tags/rename (admin) | lib/api/routes/admin_api.dart:689 | rename payload | MetadataTermUpdateResponse | CONFIRMED |
| DELETE | /api/tags/:tag (admin) | lib/api/routes/admin_api.dart:707 (via `_deleteMetadataTerm`, DELETE at 144) | none | MetadataTermUpdateResponse | CONFIRMED |
| POST | /api/genres/rename (admin) | lib/api/routes/admin_api.dart:722 | rename payload | MetadataTermUpdateResponse | CONFIRMED |
| DELETE | /api/genres/:genre (admin) | lib/api/routes/admin_api.dart:740 (via `_deleteMetadataTerm`) | none | MetadataTermUpdateResponse | CONFIRMED |
| GET | /api/custom-metadata-providers (admin) | lib/api/routes/admin_api.dart:753 | none | CustomMetadataProvidersResponse | CONFIRMED |
| POST | /api/custom-metadata-providers (admin) | lib/api/routes/admin_api.dart:770 | CreateCustomMetadataProviderRequest | CreateCustomMetadataProviderResponse | CONFIRMED |
| DELETE | /api/custom-metadata-providers/:providerId (admin) | lib/api/routes/admin_api.dart:787 | none | bool | CONFIRMED |
| PATCH | /api/settings (admin) | lib/api/routes/admin_api.dart:802 | settings payload | response | CONFIRMED |
| PATCH | /api/sorting-prefixes (admin) | lib/api/routes/admin_api.dart:829 | sorting prefixes | SortingPrefixesUpdateResponse | CONFIRMED |

Row count note: the table above lists one row per distinct method and path. Where one path has several body variants (`/api/me/progress/:itemId`), the variants are noted in the same row.

Defined in code with no caller traced: `GET /api/session/:id` (listed in the table). 
## Auth
- **Base URL.** No hard-coded server host. `ABSApi.basePath` is `http://localhost:3000` (`lib/api/routes/abs_api.dart:18`), used only as a fallback when no `basePathOverride` is passed (`abs_api.dart:37-46`). The real base is the user-entered server URL: `buildServerApi(baseUrl)` in `lib/screens/auth/sign_in.dart:114-125` and `lib/provider/core/user_providers.dart:291-296` (`BaseOptions(baseUrl: basePathOverride ?? ABSApi.basePath)`). Routes are absolute paths (`/api/...`, `/login`, `/status`), so Dio resolves them against the base URL and keeps any sub-path in it. This is the sole mechanism for the `/audiobookshelf` router base path: the client does not add or strip it. Trailing slash is trimmed for socket URLs (`abs_socket_client.dart:728-733`). The `/api` prefix is in every route literal. INFERRED: Dio's `baseUrl + path` join preserves a base sub-path such as `/audiobookshelf`. The dio 5.11.0 source is not in this checkout, so this rests on Dio's documented behavior.
- **Base-path stripping in server-returned URLs.** `AudioTrack.processedContentUrl` removes a leading `/audiobookshelf` (15 chars) from `contentUrl` before resolving it (`lib/api/library_items/audio_track.dart:22-28`). This means the server can return contentUrl with the router base path and the client assumes it.
- **Token source.** `User.preferredAuthToken` = `apiKey ?? accessToken ?? token` (`lib/api/me/user.dart:42`). Fields come from `POST /login` and the refresh response (`user.dart:21-24`).
- **How the token is sent.** `Authorization: Bearer <token>` header. Set by `BearerAuthInterceptor.onRequest` (`lib/api/routes/interceptors/bearer_auth_interceptor.dart:8-16`) and `OAuthInterceptor` (`o_auth_interceptor.dart`) for requests carrying a `secure` extra entry with `type: http, scheme: bearer` (added at every `ABSApi.makeApi*` call, `abs_api.dart:119-122`, `:163-166`, and the other helpers). Interceptor order is set in `lib/provider/core/user_providers.dart:268-288`: Bearer, OAuth, Cache, AuthRefresh, ABS.
- **Query token (`?token=`).** Used in three places only: `chapter_editor_preview.dart:55-56` (`file/:ino` preview), `lib/components/settings/admin_server_backups_view.dart:543-552` (backup download, opened with `launchUrl` at :562), and `lib/util/audio_handler/auto/bg_audio_handler_android_auto_media.dart:435-440` (cover URI for Android Auto content provider). Fact: the backup download and Android Auto cover put the token in the URL.
- **Media player auth.** `BgAudioHandlerSource._currentRequestHeadersInternal` (`lib/util/audio_handler/bg_audio_handler_source.dart:211-219`) sends the Bearer token to the player only when the current media is HLS (`isHls`). Direct-play tracks get server custom headers only, so direct play is authorized by the session id in the public URL (`/public/session/:sessionId/track/:index`). Headers go into just_audio sources at `lib/models/internal_media.dart:114-160`.
- **Refresh flow.** `AuthRefreshInterceptor` (`lib/util/interceptors/auth_refresh_interceptor.dart`):
  - Pre-flight: if the Bearer JWT `exp` is within 5 s, refresh first (`onRequest`, lines 30-91; `isJwtExpired` at 350-361).
  - Reactive: on HTTP 401 or 403 (`onError`, lines 94-125), refresh, then retry once with the new token (`_retryRequest`, lines 291-331; retry flag `auth_retry_attempted`, line 19).
  - Refresh call: `POST /auth/refresh` with header `x-refresh-token` set from the stored `activeUser.refreshToken` (`auth_refresh_interceptor.dart:187`, call at 209-212; `me_api.dart:86-109`). The stored `refreshToken` is read from the local DB, not from a response header.
  - Persist: `db.addOrUpdateStoredUser(refreshedUser)` (line 226).
  - Concurrency: one refresh at a time via `_globalRefreshCompleter` (lines 27, 148-156, 169, 260).
  - Skipped paths (no refresh): suffixes `/login`, `/auth/refresh`, `/logout`, `/status`, `/ping` (lines 127-138), plus extra `skip_auth_refresh`.
  - Failure: 401/403 during refresh removes the session via `onAuthFailed` (lines 247-256).
- **x-return-tokens.** `POST /login` sends `x-return-tokens: true` when `returnTokens` is set (`me_api.dart:31-33`). Callers: `lib/screens/auth/sign_in.dart:429-433` and `:516-518`, `lib/screens/settings/server_connection_settings.dart:177-179`. Without it the response has no accessToken/refreshToken, so the client expects them.
- **Login body.** `{username, password}` (`lib/api/me/request/login_request.dart:6-8`).
- **Authorize / validate.** `POST /api/authorize` with `{}` (`me_api.dart:70-84`), used by `lib/provider/core/user_providers.dart:134` for session validation, `lib/provider/wear/wear_providers.dart:48`, and admin views.
- **Logout.** `POST /logout` with `x-refresh-token` (`me_api.dart:111-133`), called from `lib/screens/settings/settings_screen.dart:553`.
- **OIDC / SSO.** `lib/provider/core/oidc_provider.dart`:
  - Starts at `initiateOidc` (lines 60-124). Generates a PKCE verifier and `code_challenge = BASE64URL(SHA256(verifier))`, method `S256`. Random `state` (24 bytes).
  - `GET /auth/openid` with `redirect_uri=yaabsa://oauth` and `client_id=Yaabsa`, `response_type=code`, no redirect following. Reads the `Location` header and opens it in the external browser (`launchUrl`, line 113).
  - Session (state, codeVerifier, serverUrl, cookie, customHeaders) stored in FlutterSecureStorage key `oidc_session` (line 110).
  - Callback handled by `handleCallbackUri` (lines 129-260). Checks `state` (lines 134-164). Calls `GET /auth/openid/callback` with query `code`, `state`, `code_verifier` (lines 181-183), plus the `Cookie` header replayed from the saved set-cookie (line 167). Note: the PKCE verifier is sent as a query parameter. That is what the code does; whether the server accepts it this way is outside this checkout.
  - Response: `Login` (lines 186-191), then saves user with `Server.fromExternalAddress` (`activeConnection: external`).
  - Error text hints the expected mobile redirect URI `yaabsa://oauth` must be allowed on the server (`oidc_provider.dart:281`).
- **API keys.** The server can return `apiKey` on the user, and it takes precedence over accessToken and token (`user.dart:42`). The client does not create API keys for itself; admin API-key management is in `admin_api.dart:209-546`.
- **Not used:** `x-access-token`, `?token=` on API calls (other than the three URLs above), basic auth.

## Socket.IO
- **Library.** `socket_io_client` 3.1.6 (`pubspec.yaml:77`, `pubspec.lock:1869-1876`), `socket_io_common` 3.1.1 transitive (`pubspec.lock:1877-1884`). Imported as `package:socket_io_client/socket_io_client.dart` (`lib/api/socket/abs_socket_client.dart:1`).
- **Connection URL and path.** The URL is the server URL with a trailing slash removed (`abs_socket_client.dart:728-733`), passed to `io.io(normalizedServerUrl, options.build())` at line 134. No `path`, `query`, or `auth` option is set. The default Socket.IO path applies (INFERRED: the library default `/socket.io` appended to the server URL; no `/socket.io` literal exists in `lib/`, and the library source is not in this checkout, so the exact path-with-sub-path behavior is not verified).
- **Transports.** WebSocket only: `setTransports(["websocket"])` (line 129). No polling fallback.
- **Connect control.** `disableAutoConnect()` and `disableReconnection()` (line 129). The client calls `socket.connect()` itself (line 148). Reconnection is not automatic; `lib/provider/core/socket_provider.dart` calls `ABSSocketClient.connect` again from `updateSocketForUser` (lines 300-336, connect at 336). The provider also disconnects when the app is backgrounded (around :346-351).
- **Handshake headers.** `extraHeaders` = server custom headers only (`buildRequestHeaders(serverHeaders: headers)` at line 118, `setExtraHeaders` at 130-132). No Bearer token in the handshake.
- **Auth.** Emits `"auth"` with the raw token string as the single argument (`socket.emit("auth", apiToken)`, line 539) in `_authenticateSocket` (lines 532-541). Called on the `connect` event (lines 166-169) and when `connect()` is called while already connected (lines 143-145). Token value comes from `user?.preferredAuthToken` (`socket_provider.dart:301`).
- **Auth result events.** Listens for `init` (lines 180-183, logs success, reapplies the log listener), `invalid_token` (lines 185-191, logs a warning). There is no listener for `auth_failed`: NOT FOUND (searched `auth_failed`, lines 170-200 of the file). A rejected token shows up only as `invalid_token`.
- **Acks.** None. `emit` calls at lines 539, 760, 764 pass no ack callback.
- **Lifecycle listeners.** `connect` (166), `disconnect` (171, logs reason), `connect_error` (176, logs warning).
- **Events listened to (27 total, file:line; the three lifecycle and two auth events are in the same list):** user_item_progress_updated (193), item_added (221), item_updated (231), item_removed (241), items_added (255), items_updated (269), batch_quickmatch_complete (283), task_started (313), task_finished (327), task_updated (341), metadata_embed_queue_update (355), track_started (380), track_progress (395), track_finished (412), task_progress (427), collection_added (442), collection_updated (454), collection_removed (466), playlist_added (478), playlist_updated (486), playlist_removed (498), log (510), plus init (180), invalid_token (185), connect (166), disconnect (171), connect_error (176). Collection and playlist handlers are wired in `lib/provider/core/socket_provider.dart:197-225`.
- **Events emitted (3):** `auth` (line 539, token), `set_log_listener` (line 764, level int; default `_serverLogListenerLevel = 2` at line 90), `remove_log_listener` (line 760). Log listener used only by `lib/components/settings/admin_server_logs_view.dart:135-180`.
- **Payload shape.** `user_item_progress_updated` carries `{id, data: MediaProgress}` (`lib/api/socket/events/user_item_progress_updated_event.dart:8-11`). Payload helpers accept a Map or a one-element List (`_payloadToJson`, line 542).

## Playback, sessions, progress
- **Start playback.** `POST /api/items/:id/play` or `/api/items/:id/play/:episodeId` (`lib/api/routes/library_item_api.dart:77-91`). Called from `lib/provider/player/session_provider.dart:223-233` (`forceDirectPlay` set when a complete local download exists, `forceTranscode` passed through, `supportedMimeTypes` from `PlayerUtils.getSupportedMimeTypes`, `mediaPlayer = "<appName> just_audio"`, `deviceInfo` from `PlayerUtils.getDeviceInfo`, `lib/util/player_utils.dart:61-106`, which sets `clientName` to the app name and `clientVersion` to the package version). Cast path uses the same endpoint with `mediaPlayer = "<appName> cast"` (`lib/util/cast/cast_track_resolver.dart:66-74`).
- **Response fields read.** `audioTracks[].contentUrl`, `audioTracks[].mimeType`, `audioTracks[].startOffset`, `audioTracks[].duration`, `audioTracks[].index`, `playMethod`, `id`, `libraryItemId`, `episodeId`, `currentTime`, `serverVersion` (`lib/api/library_items/playback_session.dart:18-42`, `audio_track.dart:12-20`).
- **HLS vs direct.** Per track: `mimeType` containing `mpegurl` means HLS. The URL is `baseUrl + contentUrl` (`audio_track.dart:30-38`). Otherwise direct: `${baseUrl}public/session/${sessionId}/track/${index}` (`audio_track.dart:38`). `playMethod` integers 0 and 2 are compared in `bg_audio_handler_runtime.dart:8, 130, 167, 205`, `bg_audio_handler_source.dart:76, 105`, and `cast_track_resolver.dart:165`. The names (0 = direct, 2 = transcode) are INFERRED from the comparisons; the code uses only integers.
- **Sync (open session).** `POST /api/session/:sessionId/sync` with `{currentTime, timeListened, duration}` (`lib/api/session/request/sync_session_request.dart:9-11`, `session_api.dart:119`). Periodic timer `Timer.periodic` (`lib/util/audio_handler/playback_sync_service.dart:97-106`); interval from user setting `SettingKeys.syncInterval`, clamped to a minimum of 5 s (`playback_sync_service.dart:31`, `:81-88`). Final flush on pause or stop (per the file header comment, lines 17-18). Retry/queue in `session_provider.dart:491-500` and `:631-638`.
- **Close.** `POST /api/session/:id/close` with `{}` (`session_api.dart:97`), called at `session_provider.dart:141, 169` and `cast_track_resolver.dart:91`.
- **Local / offline sessions.** `POST /api/session/local` (`session_api.dart:144`) sends a full PlaybackSession JSON. Only called from editor dialogs (`admin_server_sessions_view.dart:415`, `library_item_listening_sessions_tab.dart:305`, `current_user_listening_sessions_tab.dart:287`), always after `DELETE /api/sessions/:id` when `deleteFirst`. Offline playback itself does not create server sessions; it uses downloaded files (`session_provider.dart:200-233`). `POST /api/session/local-all`: NOT FOUND.
- **Progress read.** `GET /api/me/progress` (see requested confirmations). Single item: `GET /api/me/progress/:itemId[/:episodeId]` (`me_api.dart:403-420`).
- **Progress write.** `PATCH /api/me/progress/:itemId[/:episodeId]` with either full MediaProgress JSON (`createUpdateMediaProgress`, `me_api.dart:458-480`), or a partial map (`patchMediaProgress`, `me_api.dart:482-504`). Batch: `PATCH /api/me/progress/batch/update` with an array (`me_api.dart:506-534`). Ebook: `PATCH /api/me/progress/:itemId` with `{ebookLocation, ebookProgress}` (`me_api.dart:536-555`). The client also applies progress from the socket event `user_item_progress_updated` (`socket_provider.dart`).
- **Bookmarks.** `GET /api/me/bookmarks`, `GET /api/me/bookmarks/:libraryItemId`, `POST`, `PATCH`, `DELETE /api/me/item/:itemId/bookmark[/:time]` (`me_api.dart:135-185`, `:241-272`). Body `{time: int, title: string}`. Used by `lib/provider/player/user_bookmarks_provider.dart:285-304`.
- **Items in progress.** `GET /api/me/items-in-progress?limit=25` (`me_api.dart:224-239`).
- **Listening stats and sessions.** `me_api.dart:274-400` (see table). Callers not traced one by one; session tabs live under `lib/components/sessions/`.
- **Admin sessions and sharing.** `GET /api/sessions`, `GET /api/sessions/open`, `DELETE /api/sessions/:id` (`session_api.dart:32-65`), used by admin session views.

## Downloads, ranges, media
- **Downloads.** `GET ${server.url}/api/items/:id/file/:ino/download` (`lib/util/handler/download_handler.dart:767`) via `background_downloader` (`DownloadTask`/`UriDownloadTask`, lines 769-800), with headers `buildRequestHeaders(serverHeaders, bearerToken: authToken)` (line 702, from `user.preferredAuthToken` at 698), `retries: 3`, `allowPause: true` (line 800), `requiresWiFi` optional. Also `lib/components/app/item/library_item_file_download_dialog.dart:73-78` (`doNotCache`).
- **Range requests.** NOT FOUND. No `Range`, `Content-Range`, `Accept-Ranges`, `206`, or `416` in `lib/` (searched these terms). Seeking in playback is done by the audio player (just_audio / media_kit) on the progressive source URL; the client sets no Range header itself. Resume of paused downloads is delegated to `background_downloader` (INFERRED: its resume behavior uses Range internally; the package source is not in this checkout).
- **Status codes.** No explicit status-code handling for downloads or media beyond the generic `isConnectivityFailure` / `401/403` handling in interceptors (`lib/util/interceptors/auth_refresh_interceptor.dart:279-289`, `abs_interceptor.dart:30-53`). 2xx is checked for `_postStatusOnly` (`library_item_api.dart:25-26`) and cache writes (`cache_interceptor.dart`).
- **Content-type assumptions.** Extension-to-MIME map for downloads (`lib/util/handler/download_handler.dart:2076-`): epub `application/epub+zip`, pdf `application/pdf`, m4b and m4a `audio/mp4`, aac `audio/aac`, ogg/oga `audio/ogg`, opus `audio/opus`, wav `audio/wav`, flac `audio/flac`, mp3 `audio/mpeg` (remaining entries not listed here). Track MIME type comes from the server (`AudioTrack.mimeType`, `audio_track.dart:18`), defaulting to `audio/mpeg` in cast (`cast_track_resolver.dart:50`).
- **Downloaded cover names.** `cover.jpg`, `cover.jpeg`, `cover.png`, `cover.webp` (`download_handler.dart:1500`).
- **Subtitles.** `GET /api/items/:id/file/:ino` as text (`subtitle_loader.dart:285-295`).

## Caching and covers
- **Dio cache interceptor (custom, sembast-backed).** `CacheInterceptor` (`lib/util/interceptors/cache_interceptor.dart:137-281`), in the chain at `lib/provider/core/user_providers.dart:274-278`. Stores GET 2xx responses in the `StoreRef.main()` store under key `<userId><uri>` (`_getCacheKey`, lines 305-308), with timestamp, data and headers. Serves a fresh hit or, when offline, a stale hit (`onRequest`, lines 151-215). Bypassed by `extra.noCache` (read bypass, still writes) and `extra.doNotCache` (both bypassed) (lines 152-156, 217-222).
- **Routes cached (`cacheRouteDefinitions`, lines 13-80), 7 days unless noted:** `/api/libraries` (pattern line 17), `/api/libraries/{id}` (23), `/api/libraries/{id}/items` (29), `/api/items/{id}` (35), `/api/items/{id}/{x}` (41, pattern `/api/items/{uuidv4}/{uuidv4}`), `/api/libraries/{id}/filterdata` (47), `/api/libraries/{id}/series` (53), **`/api/playlists` (pattern line 59)**, **`/api/collections` (pattern line 65)**, and `/api/me` (1 day, `aggressiveCache: true`, pattern line 71, rule lines 69-74).
- **Matcher detail.** `CacheRouteDefinition.matches` (lines 326-342) compares segment count and literal segments only, and treats any `{...}` pattern segment as a wildcard. It does not check UUID format. Effect: any 5-segment GET under `/api/items/<id>/...`, such as `/api/items/<id>/metadata-object`, matches the 7-day "Item nested resource" rule. `/api/me/progress` has 4 segments and is not matched by `/api/me` (3 segments), so progress is not cached.
- **Playlists and collections cache.** `getUserPlaylist()` and `getCollections()` with default args can return a cached copy for 7 days (`playlist_provider.dart:52`, `collection_provider.dart:52`). `forceServer: true` sets `noCache` (`list_api.dart:21-23, 60-62`), so a server read is forced on refresh. Auto/Android Auto callers pass `doNotCache` (`bg_audio_handler_android_auto_data.dart:251, 278`) or call without options (`bg_audio_handler_auto_queue.dart:671, 721`, which can read cache). Cache entries for these two routes are invalidated when an item changes (`invalidateCachedLibraryItemEntries`, fragments `/api/playlists`, `/api/collections`, lines 91-92).
- **HTTP-level cache headers.** NOT FOUND: `ETag`, `If-None-Match`, `If-Match`, `304`, `Cache-Control` (searched all of `lib/`). No `dio_cache_interceptor`, `http_cache`, or Dio cache package in `pubspec.yaml`. Platform HTTP stack may cache on its own: native adapter (`lib/util/network/dio_factory_native.dart:34`, `NativeAdapter`, Cronet on Android when available). Caching behavior of the platform stack is INFERRED, not verified.
- **Images.** `cached_network_image_ce` ^4.10.0 (`pubspec.yaml:49`, locked 4.10.1 at `pubspec.lock:215`). Cover URL built by `LibraryItemApi._buildCoverUri` (`library_item_api.dart:399-429`): `/api/items/:id/cover` with `width` (line 409) and `height` (414) when given, `raw=1` (419, asserted to have no width/height), `ts=<updatedAt>` (424) as cache-buster. Caller that passes width and height: `lib/provider/player/session_provider.dart:296-301` (`playerCoverRequestDimension`). `raw: true`: `lib/screens/item/library_item_book_view.dart:64`. Other callers pass no size: `widget_bridge.dart:177`, `cast_track_resolver.dart:158`, `reader_speech.dart:137`, `reader_narration.dart:138`, `bg_audio_handler_android_auto_media.dart:427`. No `format=` param anywhere (searched `format=`, `webp`, `jpeg`; only upload extension allow-list at `library_upload_panel.dart:43-45`).
- **Author images.** `GET /api/authors/:id/image?width=` (`author_card.dart:131-135`).
- **Image request headers (INFERRED concern).** Covers and author images are fetched with `httpHeaders` from `dio.options.headers` (`library_item_api.dart:384`, `author_card.dart:116`, `library_item_book_view.dart:51`, `player_cover_component.dart:24`). The Bearer token is added only by interceptors per Dio request, so it is not in `dio.options.headers`. Cover requests therefore carry only server custom headers (`user_providers.dart:291-296`). Whether `/api/items/:id/cover` needs auth on the server is not verifiable from this checkout. The Android Auto path appends `?token=` for this reason (`bg_audio_handler_android_auto_media.dart:435-440`).
- **Local covers.** `_resolveLocalCoverUri` returns file URIs for downloaded items (`library_item_api.dart:431-`).

## Server version branches
- **Helper.** `serverVersionAtLeast(version, major, minor, patch)` parses `^v?(\d+)\.(\d+)(\.(\d+))?` (`lib/util/server_version.dart:1-18`).
- **Only gate found.** `serverSupportsMediaProgressAndBookmarkRoutes(version)` = `>= 2.36.0` (`server_version.dart:20-22`). Used at:
  - `lib/provider/common/media_progress_provider.dart:432` (GET /api/me/progress vs GET /api/me fallback, see requested confirmations).
  - `lib/provider/player/user_bookmarks_provider.dart:229` (remote bookmarks).
  - `lib/screens/reader/reader_core_helpers.dart:155` (reader progress).
  - `lib/screens/wear/wear_player_screen.dart:133` (wear progress).
- **Version source.** `serverVersionProvider` = `currentUser.setting.version` (`lib/provider/core/user_providers.dart:311-314`), i.e., `ServerSettings.version` from login (`lib/api/me/server_settings.dart:39`, set from `Login.serverSettings` at `oidc_provider.dart:191` and `auth_refresh_interceptor.dart:223`). `/status` `serverVersion` (`lib/api/me/status.dart:10`) is shown in `lib/screens/auth/widgets/sign_in_server_status.dart:37` but not used for gating.
- **Minimum supported version.** No explicit minimum found. Only the 2.36 gate above.
- **App-level version check.** Latest server release is fetched from GitHub (`https://api.github.com/repos/advplyr/audiobookshelf/releases/latest`, `lib/provider/core/server_update_provider.dart:139-148`). Not an ABS API call.

## Not found (searched terms)
- `/api/session/local-all`, `local-all`: NOT FOUND.
- `/init` (server init route): NOT FOUND (searched `/init`).
- `/socket.io` literal path: NOT FOUND (searched `/socket.io` and `socket.io`; only the package import).
- Socket `auth_failed` event: NOT FOUND (searched `auth_failed`).
- Socket acknowledgements: NOT FOUND (no ack-callback `emit` calls; the three `emit` calls at `abs_socket_client.dart:539, 760, 764` have no callback).
- Bearer token in socket handshake (query, `extraHeaders`, `auth`): NOT FOUND. Only server custom headers in `extraHeaders`.
- `ETag`, `If-None-Match`, `If-Match`, `304`, `Cache-Control`: NOT FOUND in `lib/`.
- `Range`, `Content-Range`, `Accept-Ranges`, `206`, `416`: NOT FOUND in `lib/`.
- `format=` cover param (`webp`, `jpeg`): NOT FOUND (only upload extension allow-list).
- `/hls` literal path: NOT FOUND (HLS paths come from server `contentUrl`; `playback_diagnostics.dart:53-57` classifies `.m3u8` by suffix).
- `/s/` share routes: NOT FOUND.
- `/public/` literal other than `public/session/.../track/...`: NOT FOUND (`audio_track.dart:38`, `playback_diagnostics.dart:53`).
- `/api/items/:id/download` (no `/file/:ino`): NOT FOUND.
- `/api/me/listening-sessions` and other stats routes: FOUND (table).
- OIDC `redirect_uri` variants other than `yaabsa://oauth`: NOT FOUND in client (`oidc_provider.dart:82`; admin side uses `audiobookshelf://oauth` as a server setting placeholder, `lib/components/settings/authentication/admin_server_authentication_view.dart:50`).
- Refresh via response header (e.g. `x-refresh-token` in a response): NOT FOUND. Refresh token is read from local storage (`auth_refresh_interceptor.dart:187`).
- HTTP client packages other than Dio (`http:`, `retrofit`, `chopper`, `dio_cache_interceptor`, `http_cache`): NOT FOUND in `pubspec.yaml`.
- `magic` config: `lib/api/magic/magic_config.dart` and `lib/provider/core/magic_config_provider.dart` (17 lines) contain no network call. NOT FOUND for magic-login endpoints.
- `socket_io_client` library source: NOT in this checkout (no `.dart_tool` or pub cache). Library-default behaviors (path, reconnection defaults) are INFERRED only where stated.

## Dynamic call sites (paths built at runtime you could not resolve)
- All of the following resolve to a literal path at runtime; listed here because the path is assembled in code:
  - `ABSApi.makeApiGetRequest` replaces `{param}` placeholders from `queryParams` (`lib/api/routes/abs_api.dart:172-179`); `ABSApi.makeApiDeleteRequest` and others take the route as a parameter.
  - `AdminApi._upsertUser(method, route)` (`lib/api/routes/admin_api.dart:~80-100`) and `_patchStatusOnly` / `_deleteMetadataTerm` (`admin_api.dart:116-150`): method and route are supplied by callers (resolved above).
  - `LibraryItemApi._postStatusOnly(route)` (`library_item_api.dart:29-46`), with routes resolved in the table (`batch/delete`, `batch/quickmatch`, `embed-metadata`, `encode-m4b`).
  - `PodcastApi._postStatusOnly` (`podcast_api.dart:14-22`) is a generic POST wrapper; callers pass `/api/podcasts` and `/api/podcasts/:id/download-episodes`.
  - `UploadApi` search endpoint chosen by `mediaType` (`upload_api.dart:81`).
  - `playLibraryItem` episode vs non-episode branch (`library_item_api.dart:76-80`).
  - `MeApi` progress routes and episode suffix (`me_api.dart:409-413`, `465-469`, `489-493`).
  - Cover, author image, backup download and track URLs are built by string concatenation with `Uri` (`library_item_api.dart:427`, `author_card.dart:131`, `admin_server_backups_view.dart:544-549`, `audio_track.dart:33-38`). The `${server.url}` prefix in `download_handler.dart:767` and the ebook URL (`reader_core_helpers.dart:39`) are string concatenations too.
  - HLS track URLs and their paths are server-supplied (`audio_track.dart:34`). Shape not verifiable from the client.
  - `download_playback_tracks.dart:20` builds a download URL by string concatenation (`/api/items/<id>/file/<ino>/download`). Not traced to a network call.
  - Socket URL is the trimmed server URL (`abs_socket_client.dart:728-733`), so its path depends on the user-entered server URL.

## Summary
Counts: 143 rows in the HTTP table, one per method+path (39 are admin routes; the rest cover auth, user, progress, sessions, library, items, lists, podcasts and upload; the table also holds media URL forms such as covers, track URLs, ebook, subtitle and download URLs, marked as URL in their rows), 27 socket events listened to and 3 emitted. Confirmed: GET /api/me/progress (me_api.dart:203-222; called at media_progress_provider.dart:441 behind a 2.36 version gate, and wear_player_screen.dart:137), GET /api/playlists (list_api.dart:14-34; called at playlist_provider.dart:52 and two Android Auto sites), GET /api/collections (list_api.dart:53-73; called at collection_provider.dart:52 and two Android Auto sites). Socket.IO 3.1.6 with `auth` event and WebSocket-only transport. Auth is Bearer with refresh via `/auth/refresh` and `x-refresh-token`. Notable findings: the cache matcher ignores UUID format; `?token=` appears in three URLs; media players receive the Bearer only for HLS; cover fetches do not add the Bearer header.
