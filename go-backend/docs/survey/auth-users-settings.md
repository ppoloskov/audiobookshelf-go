> Pre-rewrite survey (2026-11-11). Describes the OLD code; kept for its HAZARDS sections.

Scope: 11 packages, 6,421 non-test lines (+160 generated icu_zones_gen.go) and 5,519 test lines. Counts below are approximate.

## 1. RESPONSIBILITIES
- **identity** (2,056): local login, refresh-token sessions, OIDC, API keys, user insert, password hash/verify, rate limit, the Express cookie and response emulation, `Authenticate`. Also exports `CreateUser`, `ChangePassword`, `InvalidateUserSessions`, `UserSessions`, API-key CRUD.
- **auth** (189): HS256 JWT issue/validate with a golang-jwt `MapClaims` wrapper.
- **access** (244): `Permissions` decode, tag/library/explicit rules, SQL `MediaClause`, `RequireAdmin`, per-request snapshot.
- **users** (146): user JSON builders (`UserDTO`, `ListedUserDTO`, `OnlineUserDTO`) plus API-key and session DTOs.
- **userapi** (1,568): user CRUD, `/api/me` account, progress, bookmarks, listening stats, year stats, API-key HTTP layer.
- **emailsettings** (203): email settings document plus ereader-device policy.
- **settingsdoc** (58): generic JSON `settings`-row read/write.
- **serversettings** (251): cached `server-settings` document, defaults, browser view, time zone.
- **emailapi** (345): email routes plus hand-written SMTP and MIME.
- **notifications** (453): notification settings CRUD, Apprise delivery, event catalogue.
- **adminops** (908): a grab-bag of admin routes (settings, stats, taxonomy, upload, filesystem).

Routes:
- **Public**: `POST /login`, `/auth/refresh`, `/logout`, `/init`, `/api/authorize`; `GET /auth/openid`, `/auth/openid/mobile-redirect`, `/auth/openid/callback`. `GET /auth/openid/config` is admin, checked inside the handler. All are rate-limited and dispatched by `identity.Handle`.
- **User (`/api/me...`)**:
  - Account: `GET /api/me`, `PATCH /api/me/password`, `GET /api/me/sessions`, `DELETE /api/me/sessions/{id}`.
  - Listening and progress: listening-sessions and listening-stats, `item/listening-sessions/{id}[/{ep}]`, `items-in-progress`, `stats/year/{y}`, `series/{id}/{action}`, `progress` (GET, GET/DELETE/PATCH `{id}[/{ep}]`, `PATCH batch/update`).
  - Bookmarks and devices: `GET bookmarks[/{id}]`, `POST|PATCH item/{id}/bookmark`, `DELETE item/{id}/bookmark/{time}`, `POST ereader-devices`.
  - Other: `GET /api/users/{id}/{listening-sessions|listening-stats}` (self or admin), `POST /api/emails/send-ebook-to-device`, `POST /api/filesystem/pathexists` (needs the upload permission), `POST /api/upload`, `POST /api/validate-cron`.
- **Admin, 403**:
  - Users: `GET|POST /api/users`, `GET /api/users/online`, `GET|PATCH|DELETE /api/users/{id}`, `PATCH .../openid-unlink`.
  - API keys: `GET|POST /api/api-keys`, `PATCH|DELETE /api/api-keys/{id}`.
  - Settings: `PATCH /api/settings`, `PATCH /api/sorting-prefixes`, `GET|PATCH /api/auth-settings`.
  - Notifications: `GET|PATCH|POST /api/notifications`, `GET /api/notificationdata`, `GET /api/notifications/test`, `GET|DELETE|PATCH /api/notifications/{id}` (test is `.../{id}/test`).
  - Misc: `GET /api/logger-data`, `POST /api/cache/purge`, `POST /api/cache/items/purge`, `GET /api/stats/server`, `GET /api/stats/year/{y}`, `POST /api/watcher/update`, `GET /api/filesystem`, tags and genres (`GET`, `POST .../rename`, `DELETE /{name}`).
- **Admin, 404**: `GET|PATCH /api/emails/settings`, `POST /api/emails/test`, `POST /api/emails/ereader-devices`.

## 2. TABLES / SETTINGS
- **users** (R/W): all columns. JSON columns `permissions`, `bookmarks`, `extraData` are kept as raw bytes. `extraData` holds `seriesHideFromContinueListening`, `authOpenIDSub`, `oldUserId`.
- **sessions** (R/W): `refreshToken`, `expiresAt`, `lastRefreshToken`, `lastRefreshTokenExpiresAt`, `ipAddress`, `userAgent`, `userId`.
- **apiKeys** (R/W).
- **settings** (R/W): described below.
- **Reads**: libraries, libraryFolders, libraryItems, books, podcasts, podcastEpisodes, series, authors, mediaProgresses (via `progressdto`), playbackSessions, devices (via `store.LoadDeviceInfo`).
- **Other writes**:
  - Taxonomy edits write `books|podcasts.tags|genres`.
  - Sorting-prefix changes write `titleIgnorePrefix` and `nameIgnorePrefix` on books, podcasts and series.
  - User delete removes playlists and nulls `playbackSessions.userId`.
  - Progress writes come from `progressdto`, and a socket auth writes `users.lastSeen`.

Settings rows:
- **`server-settings`**: a flat JSON object.
  - Scanner flags: `scanner*`, `storeCoverWithItem`, `storeMetadataWithItem`.
  - Backup: `backupSchedule`, `backupsToKeep`, `maxBackupSize`, `backupPath`.
  - Logging: `logLevel`, `loggerDailyLogsToKeep`, `loggerScannerLogsToKeep`.
  - Rate limiting: `rateLimitLoginRequests`, `rateLimitLoginWindow`.
  - UI and display: `homeBookshelfView`, `bookshelfView`, `dateFormat`, `timeFormat`, `language`, `chromecastEnabled`, `allowIframe`, `allowedOrigins`.
  - Sorting: `sortingIgnorePrefix`, `sortingPrefixes`.
  - Other: `podcastEpisodeSchedule`, `metadataFileFormat`, `version`, `buildNumber`.
  - Auth: `authLoginCustomMessage`, `authActiveAuthMethods`, 19 `authOpenID*` keys, and `tokenSecret`.
  - `tokenSecret` and the OIDC secrets are omitted from the browser view.
  - Unknown keys must round-trip.
- **`email-settings`**: `{id, host, port, secure, rejectUnauthorized, user, pass, testAddress, fromAddress, ereaderDevices[{name, email, availabilityOption, users[]}]}`.
- **`notification-settings`**: `{id, appriseType, appriseApiUrl, notifications[{id, libraryId, eventName, urls, enabled, type, lastFiredAt, lastAttemptFailed, numConsecutiveFailedAttempts, numTimesFired, createdAt, titleTemplate?, bodyTemplate?}], maxFailedAttempts, maxNotificationQueue, notificationDelay}`.

## 3. COUPLING
- **Authentication path**:
  - The `server.Runtime` calls `identity.Handle`, which is a hand-written `switch` over method + path, before the mux.
  - Next it calls `identity.Authenticate`, which gives a `store.User`.
  - It puts the user into the context with `store.ContextWithUser`, plus an `access.WithRequest` snapshot, then calls `routes.ServeHTTP`.
  - Handlers read it with `store.RequestUser(r)` or `withUser` wrappers. The one public exception is GET item cover and author image, which gets a zero user.
  - `realtime` also calls `Runtime.Authenticate` and `auth.*` for the socket `auth` event.
- **`identity.RegisterRoutes`** (`routes.go`, 45 lines) is unused.
- **`userapi.Credentials`** is a 13-method interface that identity satisfies, because userapi cannot import identity (cycle via users).
- **`userapi.Deps`**: `UserChanged` (is `hub.Refresh`), `UsersOnline` (hub), `OpenSessions` (playback). They exist because realtime imports users, access and auth.
- **`adminops.Deps`**:
  - `AfterWrite` invalidates the filter cache.
  - `SettingsChanged` updates backup options and refreshes the scanner.
  - `ItemChanged` is `catalog.ItemChanged`.
  - `WatcherUpdate` is the scanner watcher hook.
- **`notifications.TriggerEvent`** is injected into podcasts and backup.
- **`events.Emitter`** is passed to every handler.
- **`serversettings`** has 14 importer packages and keeps its cache in a `sync.Map` keyed by `*sql.DB`, which needs `Forget`.
- **Consumers**: `access` is imported by 20 packages. `emailsettings` is imported by emailapi, identity and userapi.

## 4. TESTS
All tests are in-package, but they are mostly black-box through httptest, calling `New(db, cfg, emit)` and then `Handle` or `Routes(mux)`.
- identity, userapi, emailapi, notifications, adminops: about 85% HTTP-level. These would survive a rewrite if the constructors are kept or adapted. `identity/oidc_test.go` has 42 httptest uses.
- access, auth, emailsettings, serversettings, users, settingsdoc: about 100% white-box unit tests (`auth/token_test.go`, `access_test.go`), roughly 1,000 lines. These would be rewritten.
- Overall about 75% survives with only a harness change.

## 5. COMPLEXITY SOURCES
1. `oidc.go` 1–887, about 890 lines:
   - Authorize flow: `oidc.go:69-184` (116).
   - Callback: `oidc.go:218-305` (88).
   - User match, link and register: `oidc.go:528-605` (78).
   - Groups and permissions mapping: `oidc.go:682-757` (76).
   - Hand-rolled code exchange, userinfo fetch and nonce check: `oidc.go:469-526` (58). go-oidc is used only for ID-token verification.
   - Two in-memory flow maps with pruning: `oidc.go:759-770`.
   - `oidcConfig` quirk where an unset subfolder becomes `"undefined"` or `"null"`: `oidc.go:421-430`.
2. Express and passport emulation, about 200 lines:
   - Fake `connect.sid` HMAC cookies: `identity.go:683-719`, `270-277`, `391-397`. Nothing stores or reads this session.
   - Cookie encoder: `identity.go:651-681`.
   - `writeExpressJSON/Body` with a weak ETag, `writePassportDenied` and `redirectExpress`: `identity.go:763-831`.
   - Custom 404-on-method-mismatch wrappers: `routes.go`.
3. `map[string]any` login payload: `loginPayload` at `identity.go:553-599` (47), mutated with `payload["user"].(map[string]any)` (unchecked assert) at `identity.go:264` and `353`. `UserDTO` at `users/dto.go:15-55`, `ListedUserDTO`, `OnlineUserDTO` (the same user JSON built three times). `getUser` and `updateUser` also mutate the maps.
4. Permissions as maps: `access.go:52-77` (`Defaults` and `Patch` over `map[string]any`) and `access.go:23-37` (`any` action flags that are JS-truthy-tested). `userapi/account.go:245-348` (`updateUser`, 104 lines of ordered, hand-validated PATCH) and `99-148` (`createUser`).
5. Typeless server settings:
   - `serversettings.go:24-60` and `129-143`.
   - A JSON round-trip deep copy in `Doc()`.
   - The `sync.Map` cache with a generation counter: `serversettings.go:190-251` (62).
   - Time-zone emulation: `HostTimeZone` (about 25) plus the generated `icu_zones_gen.go` (160).
   - `adminops/handler.go:138-263` (126 lines of `patchSettings` and `authSettings(…, patch bool)`) with `equalJSON` marshal compare.
   - `sortingPrefixes` at `264-331` does a full-table rescan of books, podcasts and series.
   - Related: the `adminops/handler.go:751` `validMobileURI` regexp is compiled per call.
6. `emailsettings.go:44-113` (70): JS `Number()` and truthiness for port, secure and rejectUnauthorized. `Device map[string]any` and `CleanDevices` (32). `emailapi/handler.go:59-84` uses `reflect.DeepEqual` plus a RawMessage shadow struct.
7. `emailapi/handler.go:226-339` (115): hand-rolled SMTP dial/TLS/STARTTLS/AUTH and MIME multipart with a fixed boundary. It does `io.ReadAll` up to 512MB, with a blanket 8s deadline. `urlQuery` has a misleading name.
8. `notifications/handler.go` (453): the whole document is `map[string]any`, with `update` at `71-123`, `updateNotification` at `212-260`, `strictEqual`, `number` and `array` helpers, and `id()` hand-rolled UUID v4 (use `ids.New`). The event catalogue is 8 very long lines (`446-453`). No concurrency protection (read-modify-write races), and `deliverActive` saves the full doc.
9. `auth/token.go` (189): `MapClaims`, manual `optionalString` checks, `Claims` unpack. Typed claims structs would cut it to about 60 lines.
10. Stats duplication: `userapi/year_stats.go` (185), `listening.go:56-111` (56) and `adminops/handler.go:541-673` (130, with `orderedTimes` emulating JS object key order). Two different top-N implementations.
11. Misplaced code: `adminops` taxonomy `411-539` (130, raw SQL with a callback `rewriteTaxonomy`), upload (128), filesystem and pathexists (about 75). `userapi/progress.go` + `bookmarks.go` + `listening.go` + `year_stats.go` (about 800) are playback/progress concerns, not user admin.
12. Per-request rate limiter: `identity.go:733-761` (29) plus fields.

## 6. CLIENT DEPENDENCE
The client sources are in `../client`; I grepped `store`, `pages` and `plugins`.
- **Used by the web client**:
  - `POST /auth/refresh` (`store/user.js:152` reads `response.user.accessToken`).
  - `/login` returns `{user, userDefaultLibraryId, serverSettings, Source, ereaderDevices}` (`login.vue:181`).
  - `/logout` returns `redirect_url` (`account.vue:214`).
  - `/status` returns `isInit`, `authMethods`, `authFormData`.
  - `/auth/openid?callback=...` and `?accessToken=` on the callback (`login.vue:135`, `315`).
  - Sessions list with `numPages` (`account.vue:315`).
- **Nothing found**: `connect.sid`, ETag, the bare `Unauthorized` body quirks and `setToken`. `setToken` and `x-refresh-token`/`x-return-tokens` are likely mobile-app headers (unknown, not in the web client).
- **Ordered PATCH validation and the "undefined" subfolder quirk**: unknown.
- **Port/Number coercion, `Truthy`, `strictEqual`, `orderedTimes`, per-entry rounding in stats**: unknown. Only the UI displays them, so the exact values probably don't matter.
- **Plausibly used by real clients**:
  - Mobile OIDC with PKCE and `redirect_uri` (`mobile-redirect` flow).
  - `x-return-tokens`/`x-refresh-token` headers.
  - Bearer `?token=` query.
  - `user_updated`, `user_added`, `user_removed` events.

## 7. GREENFIELD PROPOSAL
Packages:
- **`internal/auth`** (about 400): `Authenticator` as `func(next http.Handler) http.Handler` middleware that sets `ctx` user and permissions via `auth.UserFrom(ctx)`. Typed `Claims` over `jwt/v5` `RegisteredClaims`. API-key lookup. Login, refresh, logout, init as mounted handlers (`routes.go`). Bcrypt via `x/crypto`. Rate limit via `x/time/rate` per IP.
- **`internal/auth/oidc`** (about 250): use `golang.org/x/oauth2` (PKCE helpers) with go-oidc. A flow store as a small struct with a TTL. Drop `connect.sid` and the Express writers unless a client proves otherwise. Plain `http.Redirect` and `json.NewEncoder`.
- **`internal/users`** (about 450): `User` and `Permissions` structs (typed bools, `[]string`) with `UnmarshalJSON` tolerating legacy types; one `Public()` view struct with variants; `Store` (typed sqlc rows, `Update(ctx, fn)`); handlers for CRUD, me and sessions, api-keys.
- **`internal/settings`** (about 250): generic `Doc[T]` loader with `Update(ctx, func(*T))` in a transaction; typed `Server`, `Email` and `Notification` structs. `Server` should embed `Extra map[string]json.RawMessage` to preserve unknown keys (the secret fields excluded from JSON views by a separate `Public()`). Remove the `sync.Map` cache in favour of `atomic.Pointer`.
- **`internal/mail`**: use `wneessen/go-mail` or `net/mail` + `mime/multipart`, and stream the attachment.
- **`internal/notify`** (about 250), **`internal/admin`** (about 450: stats, taxonomy, upload, filesystem), and a **`progress`** package for progress, bookmarks and stats, folded into playback.

Libraries that remove code: oauth2 (about 80 lines), go-mail (about 100), jwt/v5 typed claims (about 100), `net/http` ServeMux 1.22 patterns (identity's switch and `routes.go`), `slices` and `maps` for the helpers, and optionally `ulule/limiter`.

Estimate: now about 6.4k non-test lines, after about 3.0–3.4k (roughly -50%). Tests about 5.5k now; the HTTP-level ones would be kept.

## 8. HAZARDS
- **Tokens**:
  - HS256 JWTs signed with the secret, which is `JWT_SECRET_KEY` or `settings.tokenSecret` (base64 of 256 random bytes), persisted in the `server-settings` row if absent. Rotating the secret kills every user token, session and API key.
  - Legacy `users.token` has `{userId, username, iat}`, no exp or type, and is returned to clients as `user.token`.
  - Access tokens have `type: "access"`, `jti`, `iat`, `exp`. Refresh tokens have `type: "refresh"`. API keys carry `{keyId, name, type: "api"|"apiKey", iat, exp?}`. An API key is validated by signature plus the `apiKeys` row (`isActive`, `expiresAt`, owner `isActive`). Refresh tokens are rejected as bearer.
  - Lookup falls back from the JWT `userId` to `extraData.oldUserId`. Access TTL 1h, refresh 30d, grace 10min (env `ACCESS_TOKEN_EXPIRY`, `REFRESH_TOKEN_EXPIRY`, `REFRESH_TOKEN_GRACE_PERIOD`).
- **Passwords**: bcrypt cost 8 in `users.pash`. Login lowercases the username (`lower(username)=?`). Root with an empty `pash` logs in with an empty password. Passwordless non-root (OIDC) users cannot log in locally. Only root may set an empty password.
- **Sessions** (a compare-and-swap rotation):
  - `UPDATE ... WHERE refreshToken = current`.
  - The old token becomes `lastRefreshToken`, valid for the grace period.
  - Both tokens match on lookup.
  - An expired token deletes its row.
  - Rotation by the grace token does not rotate.
  - Password change or credential change calls `InvalidateUserSessions`: it keeps and rotates the current session and returns new tokens (cookie, or body when `x-refresh-token` is set).
  - `/logout?allDevices=1` deletes all sessions of the user.
  - The web client uses the `refresh_token` cookie (Lax, HttpOnly, `Secure` when proxied HTTPS). Mobile uses `x-refresh-token` / `x-return-tokens`.
- **OIDC**:
  - Web flow: `callback` must be same-origin and under the base path; PKCE is done server-side with cookies `auth_cb`, `auth_state`, `auth_method`.
  - Mobile flow: needs a client `code_challenge` (S256 only) and an allowed `redirect_uri` (or `*`); `/auth/openid/mobile-redirect` re-redirects with `code` and `state`; the callback then returns JSON with `accessToken` and `refreshToken`.
  - Provider checks: `nonce`, ID-token `sub` must equal the userinfo `sub`, `email_verified` is required to match by email, and the signing algorithm comes from settings.
  - Linking: `extraData.authOpenIDSub`; an already-linked user cannot be re-linked.
  - Registration: auto-register only if enabled; the group claim is mapped to role admin/user/guest; the root user cannot be downgraded; the advanced-permissions claim maps through the fixed key table and is applied only to non-admins; failures delete a just-created user.
  - Logout URL: `id_token_hint` from the cookie, `post_logout_redirect_uri` only for the web method.
- **Permissions**:
  - Admin = root or admin. Action flags (`update`, `delete`, `download`, `upload`) are tested for JS truthiness AND an active user.
  - `Library()` is `accessAllLibraries` or membership. `ListLibrary` ignores `accessAllLibraries` when the list is non-empty.
  - Explicit content needs `accessExplicitContent` and an active user.
  - Tags: `itemTagsSelected` with `selectedTagsNotAccessible` acting as a denylist; an empty allow-list denies everything (fail closed in SQL `MediaClause`).
  - Malformed permissions JSON grants nothing.
  - `permissions.librariesAccessible` and `itemTagsSelected` are always arrays in the user DTO.
  - Admin PATCH accepts only bool values for known keys and string arrays; the root type cannot be changed.
- **Settings written back**: `tokenSecret` stays in `server-settings` and must never leak to browsers. `authOpenIDClientID`, `authOpenIDClientSecret`, `authOpenIDMobileRedirectURIs`, `authOpenIDGroupClaim` and `authOpenIDAdvancedPermsClaim` are hidden from the browser view but returned by `GET /api/auth-settings`. `authActiveAuthMethods` must keep only `local` and `openid`. Env `ALLOW_IFRAME` and `BACKUP_PATH` override the stored values. Unknown keys round-trip. `email-settings.pass` is stored plain. The `notification-settings` row is created on first write only.
- **Events**:
  - `user_added` and `user_removed`: admins.
  - `user_updated`: to the user themselves for progress, bookmark and series changes, and to the acting admin (not the edited user) for a user PATCH or openid-unlink.
  - `ereader-devices-updated`: to the user, or admins from the admin route.
  - `notifications_updated`: all clients.
  - After user update or delete, the hub must `Refresh` connected sockets (`UserChanged`).
  - Socket `auth` writes `users.lastSeen` and `updatedAt`.
- **Other**: `/init` fails when a root exists (500). Deleting a user: must not delete self or root, deletes playlists, nulls `playbackSessions.userId`.
