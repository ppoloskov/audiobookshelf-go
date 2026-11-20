# Monstrobook

A fast, standalone Go server implementing the **Audiobookshelf v2.37.2** server
protocol and SQLite schema. It serves the official upstream v2.37.2 web UI
(pulled directly from `ghcr.io/advplyr/audiobookshelf:2.37.2` in Docker) and
supports official iOS, Android, and third-party mobile apps as a drop-in
replacement with bidirectional SQLite rollback compatibility.

## Quick Start

### Docker

Run with Docker Compose using [docker-compose.yml](docker-compose.yml):

```sh
docker compose up -d
```

Or build the multi-stage image locally (uses
`ghcr.io/advplyr/audiobookshelf:2.37.2` for `/app/client/dist` — no Node build
required):

```sh
docker build -t monstrobook .
docker run -d \
  -p 13378:80 \
  -v ./config:/config \
  -v ./metadata:/metadata \
  -v ./audiobooks:/audiobooks \
  -v ./podcasts:/podcasts \
  -e TZ=UTC \
  monstrobook
```

Existing Audiobookshelf v2.37.2 `/config` and `/metadata` volumes work
unchanged, and can be reopened by the upstream TypeScript server at any time.

### Local Build & Test

With Nix (recommended — provides Go, Node 22, WebP-enabled `ffmpeg`/`ffprobe`,
and `golangci-lint` without needing `mise` or custom FFmpeg builds):

```sh
nix develop        # or: make nix-shell (direnv: cp .envrc.example .envrc && direnv allow)
make build         # builds ./monstrobook from ./cmd/monstrobook
make test          # runs go test ./... -count=1
```

Or run single targets inside Nix from outside the shell (`make nix-test`,
`make nix-verify`).

Without Nix, building and testing requires Go 1.27+ (matching [go.mod](go.mod)),
Node 22 (for the differential oracle), and `ffmpeg`/`ffprobe` with WebP support
(see [TESTING.md](TESTING.md)).

### Twin-Server Differential Oracle & OpenAPI

Verification compares Monstrobook live against pinned TypeScript Audiobookshelf
v2.37.2 across HTTP, all 26 SQLite tables, 3-audience Socket.IO,
filesystem/`metadata.json`, outbound stubs, and bidirectional TS↔Go rollback:

```sh
make upstream    # fetch pinned TS v2.37.2 into .oracle-upstream/ (gitignored)
make verify      # run the Twin-Server Differential Oracle (alias: make check / make oracle)
make nix-verify  # run make verify inside nix develop
make openapi     # run the oracle and emit docs/openapi.json
```

## Repository Layout

- `cmd/monstrobook/` — server entrypoint, flags, logging, database startup, and
  shutdown
- `internal/` — domain packages (`auth`, `browse`, `db`, `item`, `library`,
  `playback`, `podcast`, `realtime`, `scan`, `server`, etc.)
- `testmachine/oracle/` — Twin-Server Differential Oracle, declarative flows
  (`flows/*.json`), route/socket manifests, and `cmd/abs-record` mobile capture
  proxy
- `docs/` — behaviour changes ([docs/CHANGES.md](docs/CHANGES.md)), domain
  surveys (`docs/survey/`), and generated OpenAPI spec (`docs/openapi.json` via
  `make openapi`)
- `archive/` — historical pre-rewrite notes, reports, and task logs

## Documentation

- [ARCHITECTURE.md](ARCHITECTURE.md) — contracts, design rules, package import
  order, foundation APIs, and request pipeline
- [TESTING.md](TESTING.md) — local acceptance environment and WebP FFmpeg setup
- [ROADMAP.md](ROADMAP.md) — completed rewrite waves, oracle cutover, and active
  parity work
- [handover.md](handover.md) — current oracle scorecard and open follow-ups
- [docs/CHANGES.md](docs/CHANGES.md) — deliberate behaviour differences from the
  pre-rewrite Go build
- [docs/openapi.json](docs/openapi.json) — OpenAPI specification (generated via
  `make openapi`)
