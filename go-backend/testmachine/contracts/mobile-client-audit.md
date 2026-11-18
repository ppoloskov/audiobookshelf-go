# Mobile client compatibility audit

2026-11-04. **Partial, commit-pinned source audit; MC-A remains open.**
Selected API/socket/cache files were read through the GitHub read-only
connector after public clone failed DNS. No client/device or protocol test
was run. Existing tests below were inspected, not a compatibility PASS.
This document replaces neither actual client acceptance nor the unresolved
operation census. Do not delete based on a feature not found in selected files.

## Pinned clients and review status

Default-branch heads observed during this audit (not a claim that all six are
actively maintained; maintenance/release status still needs checking):

| Client / repository | Platform | Branch / pinned SHA | Review status |
|---|---|---|---|
| [Official](https://github.com/advplyr/audiobookshelf-app) | iOS + Android | master `609f9d644c8390fc662869e08ace18ce4a519b00` | Selected Swift/Kotlin API and shared socket files; partial |
| [Lissen](https://github.com/GrakovNe/lissen-android) | Android | main `421288498f429f4c438528490807de86ec59f382` | API/cache/session DTO files; partial |
| [AudioBooth](https://github.com/AudioBooth/AudioBooth) | iOS/watchOS | main `3039270df3189435cb508797d6b5a0940a794640` | Auth/network helper; media-player path not investigated |
| [Absorb](https://github.com/pounat/absorb) | Flutter | main `8411ffc75cb3c179ceb697d4fad605a8098ca74b` | Selected API and socket service; partial |
| [Storii](https://github.com/likhithpraveenk/storii) | Flutter | main `b638fcd5f26deda17df7f22d04db90534b2c7f3b` | Selected progress/list/socket files; partial |
| [yaabsa](https://github.com/Vito0912/yaabsa) | Flutter | master `6ff482e3febaaa1bffc5c6b757da241c9a1a3120` | Bulk-progress/list caller + socket setup/listener; partial |

## Operation evidence

Each row uses the client's platform/repo/SHA above. “Confirmed” means source
usage, **not** compatible runtime behavior. DTO/constants/comments are labeled
as inferred evidence. Status for every Go/client interaction is **not tested**;
MC-A must expand these selected operations into a complete relevant census.
The author-maintained [evidence input](mobile-client-evidence.json) is separate
from generated `client-usage.json`; MC-B will add validation/merging to the
existing generator. No generated JSON was hand-edited.

| Client | Operation | SHA-pinned source | Behavior / source certainty | Go path / function | Test evidence | Status / gap | Priority / reason |
|---|---|---|---|---|---|---|---|
| audiobookshelf-app | PATCH /api/me/progress/:libraryItemId/:episodeId? | [source](https://github.com/advplyr/audiobookshelf-app/blob/609f9d644c8390fc662869e08ace18ce4a519b00/ios/App/Shared/util/ApiClient.swift#L593-L598) | PATCH with generic Encodable payload, path varies by optional episodeId; confirmed source; compatibility untested | internal/userapi/userapi.go:Handle → getAllProgress / handleProgress | TestProgressReadAndUpdate (existing, not run) | Not tested; MC-A/MC-D | P0: auth/progress/persistence risk |
| audiobookshelf-app | POST /api/session/local | [source](https://github.com/advplyr/audiobookshelf-app/blob/609f9d644c8390fc662869e08ace18ce4a519b00/ios/App/Shared/util/ApiClient.swift#L512-L528) | posts partial sessions, batch payload contains sessions and optional deviceInfo; confirmed source; compatibility untested | internal/playback/playback.go:Handle → handleLocalSync | TestOfflineLocalSyncAndAdminHistoryRoutes; TestGoWritesUseTSStorageFormats (local only; not run). Bulk state/replay: MISSING | Not tested; MC-06 | P0: auth/progress/persistence risk |
| audiobookshelf-app | POST /api/session/local-all | [source](https://github.com/advplyr/audiobookshelf-app/blob/609f9d644c8390fc662869e08ace18ce4a519b00/ios/App/Shared/util/ApiClient.swift#L512-L528) | posts partial sessions, batch payload contains sessions and optional deviceInfo; confirmed source; compatibility untested | internal/playback/playback.go:Handle → handleLocalSync | TestOfflineLocalSyncAndAdminHistoryRoutes; TestGoWritesUseTSStorageFormats (local only; not run). Bulk state/replay: MISSING | Not tested; MC-06 | P0: auth/progress/persistence risk |
| audiobookshelf-app | GET /api/me/progress/:id/:episodeId? | [source](https://github.com/advplyr/audiobookshelf-app/blob/609f9d644c8390fc662869e08ace18ce4a519b00/android/app/src/main/java/com/audiobookshelf/app/server/ApiHandler.kt#L662-L671) | expects decodable MediaProgress or null on error; confirmed source; compatibility untested | internal/userapi/userapi.go:Handle → getAllProgress / handleProgress | TestProgressReadAndUpdate (existing, not run) | Not tested; MC-A/MC-D | P0: auth/progress/persistence risk |
| lissen-android | POST /api/session/local-all | [source](https://github.com/GrakovNe/lissen-android/blob/421288498f429f4c438528490807de86ec59f382/app/src/main/kotlin/org/grakovne/lissen/channel/audiobookshelf/common/model/playback/LocalSessionSyncRequest.kt#L6-L34) | body deviceInfo plus sessions array; sessions carry id, libraryItemId, nullable episodeId and playback/time/device metadata; inferred request from DTO/comment; compatibility not tested | internal/playback/playback.go:Handle → handleLocalSync | TestOfflineLocalSyncAndAdminHistoryRoutes; TestGoWritesUseTSStorageFormats (local only; not run). Bulk state/replay: MISSING | Not tested; MC-06 | P0: auth/progress/persistence risk |
| AudioBooth | network/auth envelope | [source](https://github.com/AudioBooth/AudioBooth/blob/3039270df3189435cb508797d6b5a0940a794640/API/Sources/API/Services/AuthenticationService.swift#L61-L94) | POST /login with username/password and x-return-tokens:true; custom headers supported; confirmed login request; compatibility untested | internal/identity/identity.go:Handle/login | MISSING: AudioBooth x-return-tokens consumer assertion (other login tests exist) | Not tested; MC-A/MC-D | P0: auth/progress/persistence risk |
| Absorb | GET /api/me/progress | [source](https://github.com/pounat/absorb/blob/8411ffc75cb3c179ceb697d4fad605a8098ca74b/lib/services/api_service.dart#L1481-L1487) | authenticated GET; on 200 parses JSON map; confirmed source; compatibility untested | internal/userapi/userapi.go:Handle → getAllProgress / handleProgress | TestProgressReadAndUpdate (existing, not run) | Not tested; MC-07 | P0: auth/progress/persistence risk |
| Absorb | POST /api/session/local fallback | [source](https://github.com/pounat/absorb/blob/8411ffc75cb3c179ceb697d4fad605a8098ca74b/lib/services/api_service.dart#L35-L39) | comments specify fallback to legacy /play on 404/501; comment evidence; active fallback implementation not traced | internal/playback/playback.go:Handle → handleLocalSync | TestOfflineLocalSyncAndAdminHistoryRoutes; TestGoWritesUseTSStorageFormats (local only; not run). Bulk state/replay: MISSING | Not tested; MC-06 | P0: auth/progress/persistence risk |
| Storii | progress by item | [source](https://github.com/likhithpraveenk/storii/blob/b638fcd5f26deda17df7f22d04db90534b2c7f3b/lib/api/abs/endpoints/me_api.dart#L28-L77) | GET per-item progress, PATCH upsert request; confirmed source; compatibility untested | internal/userapi/userapi.go:Handle → getAllProgress / handleProgress | TestProgressReadAndUpdate (existing, not run) | Not tested; MC-A/MC-D | P0: auth/progress/persistence risk |
| Storii | playlist and collection paths | [source](https://github.com/likhithpraveenk/storii/blob/b638fcd5f26deda17df7f22d04db90534b2c7f3b/lib/api/abs/endpoints/api_routes.dart#L87-L108) | defines collection and playlist collection/batch/ID routes; list calls not confirmed; constants confirmed; active list invocation not established | internal/userapi/userapi.go:collectionPlaylist | TestPlaylistCreateListAndOwnerRestriction (playlist only; not run); client collection decoding: MISSING | Not tested; MC-A/MC-D | P1: client-visible functionality |
| audiobookshelf-app | Socket.IO transport and auth | [source](https://github.com/advplyr/audiobookshelf-app/blob/609f9d644c8390fc662869e08ace18ce4a519b00/plugins/server.js#L34-L67) | Active caller sets transports=['websocket'], upgrade=false, path <server path>/socket.io; listens auth_failed; emits auth with stored user token after connect; confirmed source; compatibility untested | internal/realtime/eio.go:serveEngine; hub.go:handleAuth | TestSocketIO4WebsocketAuthAndPrefixedPath (existing, conditional; not run). Client recovery: MISSING | Not tested; MC-03 | P0: auth/progress/persistence risk |
| absorb | Socket.IO transport/auth_failed | [source](https://github.com/pounat/absorb/blob/8411ffc75cb3c179ceb697d4fad605a8098ca74b/lib/services/socket_service.dart#L28-L48) | setTransports(['websocket']); listens auth_failed; on message Token expired asks token refresh callback, otherwise disconnects; confirmed active caller; compatibility untested | internal/realtime/eio.go:serveEngine; hub.go:handleAuth | TestSocketIO4WebsocketAuthAndPrefixedPath (existing, conditional; not run). Client recovery: MISSING | Not tested; MC-03 | P0: auth/progress/persistence risk |
| storii | Socket.IO transport/auth_failed | [source](https://github.com/likhithpraveenk/storii/blob/b638fcd5f26deda17df7f22d04db90534b2c7f3b/lib/api/abs/socket/socket_api.dart#L21-L25) | websocket-only; disables auto-connect; listener handles auth_failed; emits auth with token after connect; confirmed active caller; compatibility untested | internal/realtime/eio.go:serveEngine; hub.go:handleAuth | TestSocketIO4WebsocketAuthAndPrefixedPath (existing, conditional; not run). Client recovery: MISSING | Not tested; MC-03 | P0: auth/progress/persistence risk |
| yaabsa | GET /api/me/progress | [source](https://github.com/Vito0912/yaabsa/blob/6ff482e3febaaa1bffc5c6b757da241c9a1a3120/lib/api/routes/me_api.dart#L202-L221) | active getAllMediaProgress caller requires response.mediaProgress to be a list; confirmed active caller; compatibility untested | internal/userapi/userapi.go:Handle → getAllProgress / handleProgress | TestProgressReadAndUpdate (existing, not run) | Not tested; MC-07 | P0: auth/progress/persistence risk |
| yaabsa | GET /api/playlists | [source](https://github.com/Vito0912/yaabsa/blob/6ff482e3febaaa1bffc5c6b757da241c9a1a3120/lib/api/routes/list_api.dart#L14-L71) | active callers getUserPlaylist/getCollections use empty query params and decode playlist/collection response wrappers; confirmed active callers; compatibility untested | internal/userapi/userapi.go:collectionPlaylist | TestPlaylistCreateListAndOwnerRestriction (playlist only; not run); client collection decoding: MISSING | Not tested; MC-07 | P1: client-visible functionality |
| yaabsa | GET /api/collections | [source](https://github.com/Vito0912/yaabsa/blob/6ff482e3febaaa1bffc5c6b757da241c9a1a3120/lib/api/routes/list_api.dart#L14-L71) | active callers getUserPlaylist/getCollections use empty query params and decode playlist/collection response wrappers; confirmed active callers; compatibility untested | internal/userapi/userapi.go:collectionPlaylist | TestPlaylistCreateListAndOwnerRestriction (playlist only; not run); client collection decoding: MISSING | Not tested; MC-07 | P1: client-visible functionality |
| yaabsa | Socket.IO transport and invalid_token | [source](https://github.com/Vito0912/yaabsa/blob/6ff482e3febaaa1bffc5c6b757da241c9a1a3120/lib/api/socket/abs_socket_client.dart#L120-L190); [source](https://github.com/Vito0912/yaabsa/blob/6ff482e3febaaa1bffc5c6b757da241c9a1a3120/lib/api/socket/abs_socket_client.dart#L532-L540) | websocket-only; auto-connect and reconnection disabled; auth token is sent after connection; invalid_token listener logs warning; confirmed listener/setup; broader UI effect untested | internal/realtime/eio.go:serveEngine; hub.go:handleAuth | TestSocketIO4WebsocketAuthAndPrefixedPath (existing, conditional; not run). Client recovery: MISSING | Not tested; MC-03 | P0: auth/progress/persistence risk |
| lissen-android | ETag/304 conditional cache | [source](https://github.com/GrakovNe/lissen-android/blob/421288498f429f4c438528490807de86ec59f382/app/src/main/kotlin/org/grakovne/lissen/channel/audiobookshelf/common/api/SafeApiCall.kt#L13-L83) | 304 returns cached object; cache miss after sending validator retries without it; 200 body with ETag caches body; confirmed source; compatibility untested | internal/httputil/json.go:WriteJSON + endpoint/middleware investigation | MISSING: source-confirmed cacheable-endpoint regression | Not tested; MC-05 | P1: client cache semantics |

## Transport/event evidence and protocol matrix

Extra exact spans: official [socket setup](https://github.com/advplyr/audiobookshelf-app/blob/609f9d644c8390fc662869e08ace18ce4a519b00/plugins/server.js#L34-L67);
Absorb [options](https://github.com/pounat/absorb/blob/8411ffc75cb3c179ceb697d4fad605a8098ca74b/lib/services/socket_service.dart#L60-L91),
[auth failure](https://github.com/pounat/absorb/blob/8411ffc75cb3c179ceb697d4fad605a8098ca74b/lib/services/socket_service.dart#L28-L48),
[event registration](https://github.com/pounat/absorb/blob/8411ffc75cb3c179ceb697d4fad605a8098ca74b/lib/services/socket_service.dart#L413-L420);
Storii [auth listener](https://github.com/likhithpraveenk/storii/blob/b638fcd5f26deda17df7f22d04db90534b2c7f3b/lib/api/abs/socket/socket_api.dart#L64-L73),
[token emission](https://github.com/likhithpraveenk/storii/blob/b638fcd5f26deda17df7f22d04db90534b2c7f3b/lib/api/abs/socket/socket_api.dart#L153-L159).
Lissen [validator header](https://github.com/GrakovNe/lissen-android/blob/421288498f429f4c438528490807de86ec59f382/app/src/main/kotlin/org/grakovne/lissen/channel/audiobookshelf/common/api/ConditionalCacheInterceptor.kt#L7-L20)
and [304/200 handling](https://github.com/GrakovNe/lissen-android/blob/421288498f429f4c438528490807de86ec59f382/app/src/main/kotlin/org/grakovne/lissen/channel/audiobookshelf/common/api/SafeApiCall.kt#L13-L83).

C = confirmed selected source; I = inference; U = not investigated. These are
requirements found, not tests passed. Polling and binary usage are U for all
six, not “not needed”. Package Socket.IO/Engine.IO versions have not been
reconciled with lockfiles; no version compatibility claim is made.

| Client | WS-only / path / failure event | Polling / binary | REST cache | Byte ranges | HLS | Offline | Covers |
|---|---|---|---|---|---|---|---|
| Official | C websocket; server prefix + `/socket.io`; auth→init / `auth_failed` | U / U | U | U | U | C iOS local + local-all, Android local | U |
| Lissen | U | U / U | C ETag / If-None-Match / 304 cached object | U | U | I local-all DTO | U |
| AudioBooth | U | U / U | U | U: generic JSON helper not media proof | U | U | U |
| Absorb | C websocket; base-path `/socket.io`; `auth_failed`, expired message drives refresh | U / U | U | U | U | I local fallback comment | U |
| Storii | C websocket, auto-connect disabled; `auth_failed`; path U | U / U | U | U | U | U | U |
| yaabsa | C websocket, auto-connect/reconnect disabled; `invalid_token` logs; path U | U / U | U | U | U | U | U |

The current Go `auth_failed` event agrees with three confirmed listeners.
yaabsa's distinct `invalid_token` listener is an investigation, not a proven
broken login: its inspected handler only logs. Do not blindly rename an event
and break the other clients. Source-backed failure/recovery reproduction is
MC-03. [yaabsa listener](https://github.com/Vito0912/yaabsa/blob/6ff482e3febaaa1bffc5c6b757da241c9a1a3120/lib/api/socket/abs_socket_client.dart#L175-L190).

Absorb `getAllProgress` explicitly falls back from 404 on the bulk progress
route to `/api/me`; this is an endpoint-availability branch, not a numeric
server-version comparison. [Source](https://github.com/pounat/absorb/blob/8411ffc75cb3c179ceb697d4fad605a8098ca74b/lib/services/api_service.dart#L1478-L1500).
Numeric server-version compatibility branches remain not investigated.
AudioBooth range behavior remains unknown outside its generic helper;
AVPlayer/URLSession can issue requests elsewhere.
[Inspected helper](https://github.com/AudioBooth/AudioBooth/blob/3039270df3189435cb508797d6b5a0940a794640/API/Sources/API/NetworkService.swift#L331-L350).

## Remaining MC-A work before full audit acceptance

- Confirm installed client libraries/versions and each socket path/auth-refresh
  flow; audit connection recovery, acknowledgements and actual event payloads.
- Follow actual media/player/download call sites for AudioBooth and all other
  clients: direct URLs, Range/Content-Range, HLS, downloads, offline sync.
- Enumerate Lissen Cacheable endpoints and exact cover format/resize parameters
  for all clients; original artwork/format simplification remains unapproved.
- Reconcile full route/event caller census, dynamic call sites and current Go
  registrations through MC-B; selected rows above are not an exhaustive list.
- Add independent decoder/value/state expectations; current shape-only tests
  and named test existence do not prove mobile behavior. Device O4 stays open.

These are concrete deferred research scopes, not unsupported features. The
[gap/test plan](mobile-compat-gaps.md) and [route decisions](mobile-route-decisions.md)
protect the known requirements while investigation continues.
