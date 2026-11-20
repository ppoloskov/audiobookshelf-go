# Monstrobook First Public Release Checklist (`v0.1.0`)

Prepared from the 2026-11-20 pre-release Security and Open State-Machine / Concurrency audits (baseline: `54997df5`, `make verify` exit `0`, **0 unexpected oracle findings**, `golangci-lint` `0 issues`).

## Gate 1 — Pre-Release Security & State-Machine Hardening (COMPLETE)

### Security P0 / P1
- [x] **Admin → Root privilege escalation (`internal/user/admin.go`)**: redact `root`'s permanent JWT `Token` in `GET /api/users/{id}` when caller is not `root` (matching `listUsers`).
- [x] **Cross-user playback session IDOR (`internal/playback/local.go`, `session.go`)**: enforce `user.CanAccessItem` in `syncLocal` and scope session updates to `WHERE id = ? AND userId = ?`; scope `DELETE /api/sessions/{id}` (`history.go`) to owner or admin.
- [x] **Explicit/tag permission bypass (`internal/item/handler.go`, `delete.go`, `files.go`)**: enforce `user.CanAccessContent` on `readable`, `writable`, `delete`, and `downloadItem`.
- [x] **OIDC email-linking (`internal/auth/oidc_user.go`)**: require `email_verified == true` (not merely not-`false`) when `OpenIDMatchExistingBy == "email"`.
- [x] **Backup ZIP-bomb & temp-file safety (`internal/backup/apply.go`, `archive.go`, `routes.go`)**: bound decompressed streams with `io.LimitReader` + overflow-safe counter; add `defer r.MultipartForm.RemoveAll()`; serialize `Create()` and use unique temp file in `apply()`.
- [x] **Hardening**: allowlist `encode-m4b` `codec`/`channels`/`bitrate` (`internal/tools/encode.go`), add `inLibrary`/`within` checks on single-file `downloadItem`, resized `cover.go`, and podcast `?hard=1` delete; add `config.Config.String()` redaction for `JWTSecret`; align `SameSite=Lax` & `Secure: requestSecure(r)` on cookies.

### Open State Machines & Lifecycle P0 / P1
- [x] **Playback / HLS idle reaper (`internal/playback/`)**: add background idle-session reaper (evicting abandoned sessions, `/streams/<id>` dirs, and running `ffmpeg` processes) and cap `h.localIDs`.
- [x] **Public share session cap (`internal/share/session.go`)**: add TTL and max-capacity eviction on `sessions.byID` to prevent unauthenticated OOM growth.
- [x] **Podcast download queue stall timeout (`internal/podcast/queue.go`)**: bound per-episode downloads with a `context.WithTimeout` so one stalled upstream server cannot permanently wedge all podcast downloads.
- [x] **Rate-limit IP trust & OIDC flow cap (`internal/httpx/request.go`, `internal/auth/oidcflows.go`)**: trust `X-Forwarded-For` only from loopback/private proxy peers; enforce max capacity + eviction on `flows[T]`.
- [x] **Stuck `library-match-all` task (`internal/library/match.go`)**: `defer h.emit.TaskFinished(task)`, track goroutine with `sync.WaitGroup` + `Close()`, and guard against duplicate runs (wired into `internal/server/runtime.go`).
- [x] **Graceful shutdown & restore drain (`cmd/monstrobook/main.go`, `internal/scan/`, `internal/tools/`, `internal/realtime/`)**: set `IdleTimeout` & `MaxHeaderBytes` on `http.Server`; wait on `sync.WaitGroup` in `Scanner.Close`, `tools.Handler.Close`, and `Hub.Close`; emit `TaskFinished` on watcher cancel with pending task; fix `cancelEncode` temp-file race and `Refresh()` vs `Close()` lock race.

## Gate 2 — Automated CI & Differential Verification

- [ ] `nix develop --command make verify` exits `0` (**0 unexpected findings**, Flow Steps `377/377`, Complex `17/17`, Route Matrix `654/654`, bidirectional TS↔Go rollback pass).
- [ ] `nix develop --command go test -race ./... -count=1` passes across all packages.
- [ ] `nix develop --command golangci-lint run ./...` reports `0 issues.` and `gofmt -l .` is empty.
- [ ] Linux `amd64` and `arm64` `CGO_ENABLED=0` cross-builds of `./cmd/monstrobook` succeed.
- [ ] GitHub Actions `Unified replacement verification` and `Build and Push Go Docker Image` are green on `master` (multi-arch `ghcr.io/<owner>/monstrobook` image published).
- [ ] Regenerate `docs/openapi.json` via `make openapi` if any response/status changed.

## Gate 3 — Container, Migration & Rollback Smoke Tests

- [ ] **Fresh container boot**: `docker compose up` from empty `/config` & `/metadata`, complete `/init`, create library, scan real audiobook + ebook + podcast, verify Web UI (`/app/client/dist` from `ghcr.io/advplyr/audiobookshelf:2.37.2`) loads and plays audio + HLS + WebP covers.
- [ ] **Drop-in upgrade + rollback**: Back up a real Audiobookshelf v2.37.2 `/config` + `/metadata`, switch image to `monstrobook`, verify login, progress, shelves, covers, RSS feeds, and backups; switch back to `ghcr.io/advplyr/audiobookshelf:2.37.2` and verify zero DB corruption.
- [ ] **Graceful shutdown & soak**: Run active HLS transcode + library scan + Socket.IO clients, send `SIGTERM`, confirm clean exit in `<5s`, zero orphaned `ffmpeg` processes, zero leaked `/streams/*` dirs, and clean SQLite WAL checkpoint.

## Gate 4 — Real Client Acceptance & Release Packaging

- [ ] **Web SPA**: Run `make web-e2e` (Playwright against bundled Nuxt SPA) + manual smoke of login, OIDC, search, player, bookmarks, playlists, admin settings.
- [ ] **Mobile apps**: Smoke test official Audiobookshelf iOS/Android app (+ at least one third-party client such as Lissen / Plappa / ShelfPlayer): login, browse, stream, offline download, local session sync, progress sync, bookmarks.
- [ ] **Release notes & tag**: Verify `LICENSE` (GPL-3.0), `README.md` quick start, `docs/CHANGES.md` intentional divergences, and tag `v0.1.0` to publish the release image.
