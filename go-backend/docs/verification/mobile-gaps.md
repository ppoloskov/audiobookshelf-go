# Part C: real-client evidence (mobile)

## What contracts/mobile-client-audit.md says (2026-11-04)

- Status line: "Partial, commit-pinned source audit; MC-A remains open. ... No client/device or protocol test was run. Existing tests below were inspected, not a compatibility PASS."
- Six clients pinned by SHA: official app (iOS+Android), Lissen, AudioBooth, Absorb, Storii, yaabsa. Evidence is source reading only; each row's status is "Not tested".
- 22 operation rows, all P0/P1, all "Not tested": PATCH /api/me/progress/:libraryItemId/:episodeId?, POST /api/session/local and /local-all (official, Lissen, Absorb fallback), GET /api/me/progress/:id/:episodeId?, GET /api/me/progress (Absorb, yaabsa), POST /login with x-return-tokens (AudioBooth), playlists/collections (Storii, yaabsa), Socket.IO websocket-only transport and auth_failed / invalid_token (official, Absorb, Storii, yaabsa), ETag/If-None-Match 304 cache (Lissen).
- Verified vs assumed: "Confirmed" means the source line was read, not that Go behaves compatibly. Polling and binary Socket.IO, byte ranges, HLS and covers are "U = not investigated" for most clients. Client socket-recovery, bulk offline persistence (MC-06), range edge cases (MC-04) and cache semantics (MC-05) are listed as MISSING tests.
- Staleness: Go paths and test names in the audit point at packages that no longer exist (`internal/userapi`, `internal/identity`, `internal/httputil`); the audit has not been re-run since the 2026-11-09/10 package rewrite. `mobile-client-evidence.json` is "not yet consumed by tools/clientusage" (its own status field).

## Mobile-used routes (176 upstream routes appear in the six clients' raw evidence, contracts/clients/*.md; 54 for the official app)

File: mobile_routes.json. Per route: which clients use it and the strongest harness class.
Class counts for all 176: live TS-vs-Go differential 107, recorded-TS-shape only 56, path-text differential 7, Go tests only 4, none 2.
Official app (54 routes): 47 differential, 5 recorded-shape only, 2 Go tests only.

Mobile-shaped payloads that ARE compared live TS vs Go (replacement trial): login with `X-Return-Tokens`, `/auth/refresh`, `POST items/:id/play` with Android deviceInfo/exo-player/mime list (book and podcast episode), session sync/close, `POST /api/session/local` with an Android/exo-player session (book and episode), `PATCH /api/me/progress/batch/update`, bookmarks create/update/delete, ebook progress PATCH, and TS-reopens-Go rollback. Socket.IO auth + `user_updated` event compared live (socket probe: ok).

Mobile-used routes with NO live differential using a mobile-shaped payload:
1. `POST /api/session/local-all` (iOS bulk upload; official, Lissen, Absorb, AudioBooth, Storii). iOS_DEVICE `local-all` is sent only to the TS server while SEEDING (replacement-trial.cjs:272, 311); Go never receives it. Only Go package tests cover it (no TS comparison, MC-06 "bulk replay MISSING").
2. `GET /api/me/progress` (all progress; Absorb, yaabsa) and `GET /api/me/bookmarks`: no harness and no Go test mentions the path.
3. `GET /api/session/:id` (official, Storii, yaabsa): Go tests only.
4. `GET /public/session/:id/track/:index`: recorded-shape only (no value comparison); direct-play byte-range content is compared live only through `/api/items/:id/file/:ino` ranges.
5. `DELETE /api/me/progress/:id`, `PATCH /api/items/:id/ebook/:fileid/status`, `POST /api/emails/send-ebook-to-device`: recorded-shape only.
6. `POST /api/podcasts`, `POST /api/podcasts/feed`, `POST /api/podcasts/:id/download-episodes` (official, Absorb, AudioBooth, yaabsa): requested by the differential, but no step reaches a success status in TS or in contracttest (error path only).
7. OIDC mobile flow (`/auth/openid`, `/callback`, `mobile-redirect`; all clients): live differential exists only in `make parity-full` (oidc suite, not in CI) and it currently reports 23 of 24 cases different (headers; one status: duplicate-empty-verifier 400 vs 302). contracttest has recorded shapes only.
8. Conditional GET (304/ETag, Lissen) and mobile cover parameters: only Go-side `TestMobileConditionalGET` / `TestMobileCoverParameters`; no TS-vs-Go comparison (MC-05 "MISSING").
9. Socket.IO: websocket-only transport, `auth_failed` recovery, `invalid_token` listener: `TestSocketIO4WebsocketAuthAndPrefixedPath` is conditional (can skip, MC-01); no real mobile client has ever been run (device acceptance O4 open).
