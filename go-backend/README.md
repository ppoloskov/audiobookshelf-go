# Audiobookshelf Go backend

A Go server that replaces the TypeScript Audiobookshelf server (v2.37.2) in an
existing installation. Pure Go (`CGO_ENABLED=0`), no Node at runtime, FFmpeg and
ffprobe as subprocesses. The bundled web client in `../client` is served
unchanged. Licence: upstream's GPL-3.0; this work is not pushed upstream.

Two contracts are fixed; everything between them is ours:

1. The SQLite database and the `/config` and `/metadata` trees. A v2.37.2
   install opens unchanged, and the TypeScript server can reopen what Go wrote.
2. The client protocol: routes, JSON fields, auth flows and Socket.IO events
   that the web client, the mobile apps and documented clients use.

Upstream (`advplyr/audiobookshelf` v2.37.2, `edb1f7ef`, source in `../server`) is
a reference for what the contracts mean, not a template.

## Status

The rewrite is finished in code. The packages are listed in
[ARCHITECTURE.md](ARCHITECTURE.md#packages); the old shared packages, sqlc and
the ratchet/depguard tooling are gone. Open follow-ups and known gaps are in
[handover.md](handover.md). Deliberate behaviour differences from the
pre-rewrite Go build are in [docs/CHANGES.md](docs/CHANGES.md).

What is recorded:

- Before the rewrite (commit `7051b3e5`): 39,740 production lines in 57
  packages, all 52 test packages green.
- The contract and journey tests (`internal/contracttest`) are the gate that
  stayed green at each rewrite commit, per [ROADMAP.md](ROADMAP.md).
- The rollback check (TypeScript reopens a database Go wrote): see
  [handover.md](handover.md) for the last recorded result.
- Not covered: device acceptance of the mobile apps and live deployment, which
  is authorised separately.

Size, measured: 39,740 hand-written production lines plus 3,243 generated
before the rewrite; 35,863 hand-written and none generated after cleanup pass
3. After the final pass the tree has 34,918 production lines and 24,053 test
lines (`internal/` and `cmd/`, contracttest and dbtest excluded from the
production count).

## Build and run

Use a disposable snapshot of an initialised v2.37.2 installation. Stop upstream
before copying (or use SQLite's backup API). Never run Node and Go on the same
writable tree.

```sh
cd go-backend
CGO_ENABLED=0 go build -o audiobookshelf-go ./cmd/audiobookshelf-go
CONFIG_PATH=/path/to/config METADATA_PATH=/path/to/metadata \
HOST=127.0.0.1 PORT=3333 ./audiobookshelf-go
```

A missing database file is created as a fresh install. An existing database
must be at the v2.37.2 migration level; it is never migrated or recreated, and
any other state is refused at start.

Flags (`internal/config`): `-c/--config`, `-m/--metadata`, `-p/--port`,
`-h/--host`, `-s/--source`; each overrides its environment variable.

| Variable | Meaning | Default |
|---|---|---|
| `CONFIG_PATH` | directory with `absdatabase.sqlite` | `./config` |
| `METADATA_PATH` | items, authors, cache, backups, logs, streams | `./metadata` |
| `PORT`, `HOST` | listen address | `3333`, all interfaces |
| `SOURCE` | runtime label | `debian` |
| `ROUTER_BASE_PATH` | URL prefix (requests without it also work) | `/audiobookshelf` |
| `ABS_GO_CLIENT_PATH` | built web client (`npm ci && npm run generate` in `../client`) | `client/dist` or `../client/dist` |
| `JWT_SECRET_KEY` | token secret | `tokenSecret` in the server settings |
| `ACCESS_TOKEN_EXPIRY`, `REFRESH_TOKEN_EXPIRY`, `REFRESH_TOKEN_GRACE_PERIOD` | seconds | 3600, 30 days, 600 |
| `RATE_LIMIT_AUTH_MAX`, `_WINDOW` (ms), `_MESSAGE` | login rate limit | 40, 600000 |
| `MAX_FAILED_EPISODE_CHECKS` | podcast auto-download off after N failures; 0 never | 24 |
| `ALLOW_CORS`, `ALLOW_IFRAME`, `BACKUP_PATH` | as upstream | off |
| `DISABLE_SSRF_REQUEST_FILTER`, `SSRF_REQUEST_FILTER_WHITELIST`, `EXP_PROXY_SUPPORT` | outbound request guard, as upstream | guard on |

`../Dockerfile.go` builds an alpine image with ffmpeg, tzdata and tini
(`PORT=80`, `/config`, `/metadata`, health check on `/healthcheck`). To switch
a docker-compose install, back up `/config` and `/metadata`, stop the old
container, change only the image, keep the volumes. Roll back by setting the
TypeScript image again. The start-up log names the build:
`[Server] Init v2.37.2 (Go backend build <sha>)`.

## Test

```sh
make check          # repository root: gofmt -l (must be empty), go vet, go test
```

Some tests encode WebP covers and need an FFmpeg with libwebp on `PATH` and in
`FFMPEG_PATH`. Without it the cover and author-image journeys in
`internal/contracttest` fail with a 500. See [TESTING.md](TESTING.md). The test
layers and the rollback check are described in
[ARCHITECTURE.md](ARCHITECTURE.md#testing).

## Layout

```
cmd/audiobookshelf-go   main
internal/               32 packages + db/dbtest; see ARCHITECTURE.md "Packages"
contracts/              client-usage evidence and mobile audit
docs/                   CHANGES.md, survey/ (pre-rewrite)
tools/                  Node harness (parity, rollback), fixture builders
archive/                superseded documents, evidence only
```

## Document map

| Document | What it is |
|---|---|
| [ARCHITECTURE.md](ARCHITECTURE.md) | contracts, design rules, packages, foundation APIs, request pipeline, conventions, testing |
| [AGENTS.md](AGENTS.md) | how architect and workers work on this code |
| [../agents.md](../agents.md) | Go engineering rules and review checklist |
| [TESTING.md](TESTING.md) | local acceptance environment, FFmpeg with WebP |
| [handover.md](handover.md) | current state, follow-ups, how to continue |
| [ROADMAP.md](ROADMAP.md) | the rewrite waves (done) and what is next |
| [docs/CHANGES.md](docs/CHANGES.md) | behaviour changes for operators |
| [docs/survey/](docs/survey/) | pre-rewrite area surveys; the HAZARDS sections still list behaviours to keep |
| [contracts/README.md](contracts/README.md) | upstream source inventory and client evidence |
| [tools/parity/README.md](tools/parity/README.md) | Node differential and rollback harness (advisory) |
| [archive/](archive/) | history |
