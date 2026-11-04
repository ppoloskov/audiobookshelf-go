# Audiobookshelf Go backend

A Go server that replaces the TypeScript Audiobookshelf server (v2.37.2) in an
existing installation, with clients, books and progress, in simple, clear and
fast Go.

- **Fixed contracts:** the SQLite database (schema, value formats, meaning; the
  TS v2.37.2 server can still open it) and the client protocols (HTTP,
  Socket.IO, auth) that the web client, the mobile apps and documented-API
  clients use. Everything between them is replaceable.
- Upstream `advplyr/audiobookshelf` v2.37.2 (`edb1f7efc92ecacdef51aafb4d9d5054ce10604f`,
  source in `server/`) is a reference for meaning, never modified, and not a
  byte-level oracle or a structure template.
- The bundled web client in `client/` is served unchanged.
- Pure Go (`CGO_ENABLED=0`), no Node at runtime; FFmpeg/ffprobe as
  subprocesses. A short, owner-approved list of libraries
  ([ARCHITECTURE.md](ARCHITECTURE.md#libraries)).
- Fork license is upstream's GPL-3.0. Do not push this work upstream.

## Where things live

| Concern | File |
|---------|------|
| Agent rules and the required checks per packet | [AGENTS.md](AGENTS.md) |
| North Star, contracts, target structure, libraries, metrics | [ARCHITECTURE.md](ARCHITECTURE.md) |
| Ordered list of atomic tasks, with done checks | [ROADMAP.md](ROADMAP.md) |
| Last recorded differential run (generated; advisory since V1) | [STATUS.md](STATUS.md) |
| Current gotchas for whoever works here next | [handover.md](handover.md) |
| Node differential harness (advisory since V1) | [tools/parity/README.md](tools/parity/README.md) |
| Upstream source inventory (historical) | [contracts/README.md](contracts/README.md) |
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

```sh
cd go-backend && go build ./... && go vet ./... && go test ./...
make ratchet              # handwritten-code/complexity metrics and tools/dbnest
```

Contract shapes, meaningful value/state assertions, persistence/rollback and
functional journeys are complementary evidence. Foundations F4–F9 and V1 are
recorded complete. The generated 201-route web/log/trial inventory is not
complete mobile compatibility proof: one unmatched pattern and nine dynamic
calls remain, and six-client source/device coverage is incomplete.
See the [mobile audit](contracts/mobile-client-audit.md),
[gap/test plan](contracts/mobile-compat-gaps.md) and revised
[execution queue](ROADMAP.md). The compact [decision report](contracts/takeover-decision-report.md)
records findings, risks and choices. Owner has authorized roadmap implementation and feature-branch pushes after review.

Node differential commands remain advisory diagnostics (`make parity`,
`make parity-replacement`, `make parity-scan`, `make route-probe`; Node 22 and
`npm ci && npm run build:server`). V2 cannot remove their shared seed/inventory
inputs before regeneration is independent. The TS rollback check remains required.

`cmd/abs-compat` compares GET/HEAD responses of two running servers ad hoc.

## Docker image

`Dockerfile.go` (repo root) builds the stock web client and the Go server into
an alpine image with ffmpeg, tzdata and tini. The runtime contract matches the
upstream image: `PORT=80`, `CONFIG_PATH=/config`, `METADATA_PATH=/metadata`,
`SOURCE=docker`; the client lives in `/app/client/dist` (`ABS_GO_CLIENT_PATH`).
The image has a `HEALTHCHECK` on `/healthcheck`. CI
(`.github/workflows/docker-go.yml`) has these gates:

1. `verify`: build, vet, test, sqlc freshness, ratchet, dependency boundaries,
   and an arm64 build.
2. After verify, `rollback` proves TS reopens Go-written data; `parity` runs
   independently as an advisory diagnostic (`continue-on-error: true`).
3. `image` depends on verify + rollback, publishes amd64/arm64 tags for pushes;
   pull requests build without pushing. Parity does not block publication.

Build locally with `docker build -f Dockerfile.go -t audiobookshelf-go .`.

## Logs

Log lines use upstream's console format, `[YYYY-MM-DD HH:mm:ss.SSS] LEVEL:
message`, and the daily log files and crash log follow upstream's layout under
`METADATA_PATH/logs`. The level comes from the `logLevel` server setting, as
upstream reads it (the default is 2, INFO). The access line for each request is
logged at DEBUG, so the default output has no per-request lines.
`/healthcheck` and `/ping` are logged at TRACE because liveness probes call
them often.

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

**Which build is running.** The startup log names it:
`[Server] Init v2.37.2 (Go backend build <git sha>)`. With the `latest` tag
and Kubernetes' default `imagePullPolicy: IfNotPresent`, a node keeps the
`latest` it pulled first. Use `imagePullPolicy: Always`, or pin the
`sha-<short sha>` tag CI also publishes, so a restart picks up new builds.

## Known limits

Open work, including known gaps (share expiry timer, podcast schedule after a
`PATCH`, mobile verification), is in [ROADMAP.md](ROADMAP.md).

## Development ownership

The architect owns design, task scopes and review. Cheap capable workers own
bounded execution; prefer permitted OpenCode Flash models, with built-in cheap
workers as fallback. Keep README, AGENTS, ROADMAP and handover current with
each accepted packet. The [skill review](SKILL-REVIEW.md) records overrides to
excessive generic parity/ledger processes. Metrics guide review; the current
ratchet still enforces limits pending the proposed Q1 policy change.

Domain migration now has explicit `books`, `items` and `podcasts` ownership.
Book reads and projections are implemented in `internal/books`; B01–B09 and
test-only query instrumentation I01 are reviewed. That initial extraction kept source nearly
flat (−1 line), complexity −11, dynamic maps −2. The subsequent I02–I04 wave is also complete (see below). I05/I06, list migration,
book writes and mixed-media migration remain open. The [roadmap](ROADMAP.md)
gives exact scopes/dependencies/tests; this feature does not claim all domains
complete or constant query counts yet.

Local test prerequisite: FFmpeg must support WebP encoding (`libwebp`). The
installed Homebrew build here lacks it: five pre-existing journey image tests
fail on both base and feature. The final suite has no additional failures;
this is not a clean full-suite or deployment acceptance claim.

## Batched book pages (I02–I04)

`books.LoadMany` replaces singleton SQL with three reads per 250 unique IDs;
empty input performs none. Ordinary noncollapsed/no-include book pages use
batch hydration and reuse their existing settings snapshot. The regression
fixture returns actual 10/50 filtered books in both envelope variants: prior
43/203 reads, now 7/7. Order, ACL, pagination, prefixes and nullable values pass.
Collapsed/include/podcast paths, shelves and other consumers remain open.

This performance feature adds 139 production lines and 31 complexity points;
the explicit baseline exception is recorded in ROADMAP/handover, with strict
guards retained. Query counts improved; latency/RSS were not benchmarked.
