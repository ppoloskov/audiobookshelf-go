# Go backend handover

Updated 2026-11-12.

## Where things stand

- The greenfield rewrite between the two contracts (SQLite compatibility,
  client protocol) is complete in code. Packages, APIs and the request
  pipeline are in [ARCHITECTURE.md](ARCHITECTURE.md). The wave list, all done,
  is in [ROADMAP.md](ROADMAP.md).
- Behaviour differences from the pre-rewrite Go build (baseline `7051b3e5`) are
  in [docs/CHANGES.md](docs/CHANGES.md).
- The tree is `cmd/audiobookshelf-go` plus 32 packages under `internal/` and
  `db/dbtest`. No old shared package, sqlc or ratchet/depguard tooling remains.
- Development is on `master`. Live deployment is separately authorised.

## Size, measured

- Before the rewrite: 39,740 hand-written production lines plus 3,243
  generated, in 57 packages; 52 test packages green.
- After cleanup pass 3: 35,863 hand-written, none generated.
- Counted on 2026-11-12 (`internal/` and `cmd/`, without contracttest and
  dbtest): 34,918 production lines, 24,053 test lines.

## Verified

- `go build ./...` and `go vet ./...` are clean (run 2026-11-12).
- On 2026-11-12 `go test ./...` passed in every package except
  `internal/contracttest`: its cover and author-image journeys
  (`TestJourney/item.*.cover`, `podcast.cover`, `author.image.*`) returned 500.
  This host's ffmpeg has no libwebp; use the prefix in [TESTING.md](TESTING.md).
  I did not rerun with it.
- The rollback check (`replacement-trial.cjs --rollback-only`, see
  ARCHITECTURE "Testing") is required before every push. This update did not
  run it; the last result is whatever the latest push recorded. Run it again
  before claiming rollback.

## Open follow-ups

- `internal/logging` still has the printf helpers (`Infof`, `Errorf`, `Fatalf`,
  ...). Its doc comment says "Transitional ... delete when unused". They are
  used by `cmd/audiobookshelf-go`, `internal/server` and
  `internal/server/middleware.go`; move those to `slog` and delete them.
- `internal/library` has the package doc "Package libraries ..."; rename to
  match the package name.
- `internal/text` and `internal/media` have 12 stray `*-E` files (sed backups, tracked in git);
  delete them.
- `Makefile` target `test-ts-diagnostics` names packages that no longer exist
  (`httputil`, `catalog`, `textclean`); `docs/survey` and `contracts/README.md`
  still describe the old code.
- Open decisions, default no: run `ANALYZE` at boot (upstream does); port
  upstream's boot-time `cleanDatabase`.
- Not proven: device acceptance of the mobile apps; mobile evidence is in
  `contracts/mobile-client-audit.md` and `contracts/mobile-compat-gaps.md`.

No `TODO`, `FIXME` or `TEMPORARY` markers remain in `internal/` or `cmd/`
(`grep -rn "TODO\|TEMPORARY\|FIXME" internal cmd`).

## Closed since the earlier handover

- CORS middleware exists (`internal/server/middleware.go`).
- The scanner uses `cronspec.New()`; a bad library cron is logged and skipped.
- Filter cache is invalidated on series rename and podcast creation.
- `(*db.DB).SQL()` is gone.

## How to continue

1. Read [AGENTS.md](AGENTS.md), then ARCHITECTURE "Conventions".
2. Change code on `master` with a test beside it. Keep a behaviour only for a
   persisted value or a real client; record any change in `docs/CHANGES.md`.
3. Run `make check` (root), with the FFmpeg prefix from TESTING.md, and `-race`
   for locks and goroutines.
4. Before a push: the rollback check and both Linux cross-builds
   (`GOOS=linux GOARCH=amd64|arm64 CGO_ENABLED=0 go build ./cmd/audiobookshelf-go`).
5. Keep README, AGENTS, ROADMAP and this file current.
