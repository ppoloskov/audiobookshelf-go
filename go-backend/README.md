# Audiobookshelf Go backend

An experimental Go rewrite of the Audiobookshelf server. North Star: a Go
server build that can replace the TypeScript server in an existing
installation, with clients, books and progress.

- Behavior oracle: upstream `advplyr/audiobookshelf` v2.37.2, commit
  `edb1f7efc92ecacdef51aafb4d9d5054ce10604f`, source under `server/`. It is
  never modified and never the architecture template.
- Same HTTP, Socket.IO, SQLite and metadata layout as upstream; the original
  web client in `client/` is served unchanged.
- Runtime is Go built with `CGO_ENABLED=0`. No Node process at runtime. FFmpeg
  and ffprobe run as subprocesses.
- Fork license is upstream's GPL-3.0. Do not push this work to the upstream
  repository.

## Where things live

| Concern | File |
|---------|------|
| Agent rules and the required checks per packet | [AGENTS.md](AGENTS.md) |
| Target code structure | [ARCHITECTURE.md](ARCHITECTURE.md) |
| Order of work, exit criteria, product track | [ROADMAP.md](ROADMAP.md) |
| Current verification status (generated, never hand-edited) | [STATUS.md](STATUS.md) |
| Current gotchas for whoever works here next | [handover.md](handover.md) |
| Harness usage (fixtures, differential, suites, replacement trial) | [tools/parity/README.md](tools/parity/README.md) |
| Source inventory and parity contracts | [contracts/README.md](contracts/README.md) |
| Dated reviews and superseded status documents (evidence only) | [archive/](archive/) |

## Run

Use a consistent, disposable snapshot of an initialized v2.37.2 installation.
For a live SQLite database use SQLite's backup API, or stop upstream before
copying, so WAL contents are included. Never run Node and Go against the same
writable tree.

```sh
cd go-backend
CGO_ENABLED=0 go build -o audiobookshelf-go ./cmd/audiobookshelf-go
CONFIG_PATH=/path/to/snapshot/config \
METADATA_PATH=/path/to/snapshot/metadata \
HOST=127.0.0.1 PORT=23378 \
./audiobookshelf-go
```

A new database is created only when the configured file is absent. An existing
database must match the pinned migration version; it is never recreated or
migrated implicitly. Build the web client with `npm ci && npm run generate` in
`client/` and set `ABS_GO_CLIENT_PATH` to its `dist` if the default lookup
fails. `ABS_GO_READ_ONLY=1` is a development option for a restricted read-only
surface, not a full read-only mode.

## Verify

From the repository root (Node 22.x; upstream built once with `npm ci && npm
run build:server`):

```sh
cd go-backend && CGO_ENABLED=0 go build ./... && CGO_ENABLED=0 go vet ./... && CGO_ENABLED=0 go test ./...
make parity               # core differential, Node vs Go on fresh fixtures
make parity-full          # all named API, persistence, rollback and operational suites
make parity-equivalence   # two independent fixtures must agree
make parity-replacement   # product check: swap Go into a realistic install, replay the mobile/web journey, roll back
make ratchet              # architecture guardrails
make status               # regenerate STATUS.md from the latest recorded run
```

Counts and verdicts are in [STATUS.md](STATUS.md) and in the reports the
harness writes; do not copy them elsewhere. Without `mise`, pass
`PARITY_NODE=node` to `make`.

`cmd/abs-compat` is a small standard-library differential client for ad-hoc
GET/HEAD comparison of two running servers (`-left`, `-right`, `-cases`; tokens
from `ABS_COMPAT_LEFT_TOKEN` and `ABS_COMPAT_RIGHT_TOKEN`). It can trigger work
on upstream, so use disposable copies. Source inventory tooling:
`python3 go-backend/tools/source_inventory.py` (see
[contracts/README.md](contracts/README.md)).

## Docker image

`Dockerfile.go` (repo root) builds the stock web client and the Go server into
an alpine image with ffmpeg, tzdata and tini. The runtime contract matches the
upstream image: `PORT=80`, `CONFIG_PATH=/config`, `METADATA_PATH=/metadata`,
`SOURCE=docker`; the client lives in `/app/client/dist` (`ABS_GO_CLIENT_PATH`).
The image has a `HEALTHCHECK` on `/healthcheck`. CI
(`.github/workflows/docker-go.yml`) runs the Go checks and the upstream parity
suites, then publishes `ghcr.io/<owner>/audiobookshelf-go` for `linux/amd64`
and `linux/arm64` (`latest` from `master`, `sha-<short>`, version tags; pull
requests build without pushing). Build locally with
`docker build -f Dockerfile.go -t audiobookshelf-go .`.

To switch an existing docker-compose install, back up `/config` (and ideally
`/metadata`) first, stop the old container, then change only the image:

```yaml
services:
  audiobookshelf:
    image: ghcr.io/<owner>/audiobookshelf-go:latest   # was ghcr.io/advplyr/audiobookshelf
    # ports and volumes unchanged: 13378:80, /config, /metadata, media folders
```

The existing database must already be at the pinned v2.37.2 migration level
(run the TypeScript v2.37.2 image once first); the Go server refuses to start
otherwise and never migrates it. To roll back,
stop the container and set the image back to the TypeScript one, keeping the
same volumes (restore the backup if you wrote data you want to discard).
`ROUTER_BASE_PATH` defaults to `/audiobookshelf`, as upstream does; the root
paths such as `/ping` and `/healthcheck` keep working, and the variable can be
set or emptied exactly as for the TypeScript image.

## Known limits

The remaining gaps and the next workstreams are listed in the product track of
[ROADMAP.md](ROADMAP.md). No overall parity claim is made here.
