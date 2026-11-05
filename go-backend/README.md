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
| Clean-source validation and native FFmpeg/WebP setup | [TESTING.md](TESTING.md) |
| Earlier pruning review (PR01–PR07 implemented) | [PRUNING-REVIEW.md](PRUNING-REVIEW.md) |
| Reviewed large deletion candidates and first-wave decisions | [DELETION-FEASIBILITY.md](DELETION-FEASIBILITY.md) |
| Lissen conditional JSON and pending framing evidence | [MC05-S9B-EVIDENCE.md](MC05-S9B-EVIDENCE.md) |
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
records findings, risks and choices. Owner has authorized roadmap implementation
and reviewed pushes to `master`. All development stays on `master`; no new
development branches. CI publication and live cutover have distinct acceptance.

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
ratchet still enforces limits. Q1 requires explicit growth budgets, actual
architect review and a cleanup checkpoint before another expansion; its former
soft-metrics proposal has been superseded.

Current priority: D02 owner decisions and replacement-cost proof for the first
substantive reduction wave; lists completion and the D01 audit are accepted.
No new domain extraction starts without its end-to-end deletion case. The audit
separates proven consolidation from optional product cuts, which need owner
approval; generated shape snapshots do not count as production Go savings.

Domain migration now has explicit `books`, `items` and `podcasts` ownership.
All stages carry an explicit old-code removal plan in ROADMAP. Current owner
sequence has completed I01–I06 and lists L01–L03. The cleanup checkpoint also
completed RO02, S9a, PR01–PR04 and PR07: **158 fewer production lines, 46 fewer
complexity points**, no metric growth. Latest sequence is PR06 -> PR05 ->
MC-05/F1 -> S9b-R1 -> L04 list work. PR06/PR05 are now DONE: 74 more production
lines and 25 complexity points removed; integrated full suite and guards PASS.
MC-05/F1 and the S9b-R1 evidence review are DONE. Whole S9b framing deletion
remains pending under KEEP decisions. Lissen conditional-cache evidence and
gate results are in [MC05-S9B-EVIDENCE.md](MC05-S9B-EVIDENCE.md). Full mobile
audit and device acceptance remain open. L04/L05 are DONE: lists now owns its
HTTP/writes/feed/pagination; old userapi bodies are deleted. Full gates pass;
production lines fall 29, complexity 23, maps two, with no metric increases.
D01 identifies 4,607 conditional candidate lines, not safe deletions. The first
proposed wave targets backup management and manual audio tools (1,535 gross);
feature removal needs owner approval. See the deletion feasibility report.

Book reads and projections are implemented in `internal/books`; B01–B09 and
test-only query instrumentation I01 are reviewed. That initial extraction kept source nearly
flat (−1 line), complexity −11, dynamic maps −2. I02–I06 and L01–L03 list reads
are also complete (see below). List writes/routes,
book writes and mixed-media migration remain open. The [roadmap](ROADMAP.md)
gives exact scopes/dependencies/tests; this feature does not claim all domains
complete or constant query counts yet.

Local tests require native WebP encoding (`libwebp`). An isolated verified
FFmpeg build fixes the host's five image-journey failures; baseline and final
integrated clean-source suites now pass with count=1. The global Homebrew
binary remains unchanged and lacks that encoder. Follow [TESTING.md](TESTING.md)
for PATH/FFMPEG_PATH and preserved untracked-work caveats. This is local Go
acceptance, not device, Node rollback or live-deployment acceptance.

## Batched book pages (I02–I04)

`books.LoadMany` replaces singleton SQL with three reads per 250 unique IDs;
empty input performs none. Ordinary noncollapsed/no-include book pages use
batch hydration and reuse their existing settings snapshot. The regression
fixture returns actual 10/50 filtered books in both envelope variants: prior
43/203 reads, now 7/7. Order, ACL, pagination, prefixes and nullable values pass.
Collapsed/include/podcast paths and other consumers remain open.

This performance feature adds 139 production lines and 31 complexity points;
the explicit baseline exception is recorded in ROADMAP/handover, with strict
guards retained. Query counts improved; latency/RSS were not benchmarked.

I05/I06 add bounded common-item loading and typed items.Record. No-include
book shelf hydration drops from 51/251 to 6/6 reads for 10/50 actual IDs.
The old catalog scanner, scalar item SQL, local classifier and temporary shelf
query are removed. This wave still adds 165 production lines / 23 complexity;
the reviewed source-cost exception is explicit, not a reduction claim. Its
original WebP environment gap is now fixed for the documented test invocation;
the integrated full suite/build/vet/lint/dbnest/arm64 pass. All I cards are DONE.

L01–L03 are also checked DONE: full-schema list/permission baseline and plain
lists.Playlist/Collection read functions. Old userapi read bodies, redundant
entry copy and visibility counter were removed. Thin adapters remained at that
read milestone; L04/L05 have now removed the obsolete userapi list ownership
and bool-result adapter. That initial extraction cost 29 production lines,
complexity unchanged; explicit baseline exception, no reduction/speed claim.
The following deletion wave removes 158 lines overall, leaving a net 36-line
increase since pre-I05 and 23 fewer complexity points. Baselines were lowered
with the existing tool; strict guards remain. S9a drops only ordered-key JSON
scaffolding after real mutation-response tests; cache/range/MIME options remain.
The restricted read-only HTTP mode was audited and is active, so its behavior
is retained while duplicate prefix methods are gone. L04/L05 now complete list
HTTP ownership with another 29-line reduction; none of this proves a large
consolidation wave. D01/D02 gates apply before further extraction.
