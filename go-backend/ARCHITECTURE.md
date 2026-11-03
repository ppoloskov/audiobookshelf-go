# Architecture

## North Star

A Go server that replaces the TypeScript Audiobookshelf server in an existing
installation, with clients, books and progress. It must be **simple, clear Go**
and **faster** than the server it replaces.

Two things are fixed contracts. Everything between them is replaceable.

1. **The SQLite database** (and the `/config` + `/metadata` trees it points
   at). An existing v2.37.2 install opens unchanged; data Go writes keeps the
   same structure and meaning, so the TypeScript v2.37.2 server can reopen it
   (rollback).
2. **The client-facing protocols**: the HTTP API, Socket.IO events and auth
   flows that real clients use. Those clients are the bundled web client
   (`client/`), the iOS and Android apps, and third-party clients that follow
   the documented API (`docs/openapi.json`).

The upstream TypeScript source (`server/`, pinned v2.37.2,
`edb1f7efc92ecacdef51aafb4d9d5054ce10604f`) is a **reference** for what the
contracts mean. It is not a template for structure and not a byte-level oracle.

## Principles

| Was | Now |
|-----|-----|
| Replicate upstream Sequelize behavior | Preserve the SQLite structure and data semantics |
| Replicate Express behavior | Preserve what clients actually require of the protocol |
| Exact upstream representations (bytes, key order, headers) | Compatible fields and types; no byte equality |
| Upstream controller-oriented packages | Cohesive Go domains, redesigned freely |
| Consolidate custom implementations | Prefer a mature library when it removes complexity |
| Upstream differential parity as the gate | Contract, persistence and functional tests as the gate |
| Implement every upstream route | Keep client-required behavior; remove unneeded internals |

In practice:

1. **Simplicity is a requirement.** Each change should leave less handwritten
   code or less complexity, or justify why not. Measured by the ratchet (see
   [Metrics](#metrics)).
2. **Compatible, not identical.** A response is correct when every field a
   client reads is present with the right JSON type and meaning. Key order,
   whitespace, ETag style, `Content-Length`, error wording that no client
   parses, and log wording are free.
3. **The database is sacred.** Same tables, columns, column formats (DATETIME
   text, JSON columns, ID formats), and the same meaning of every value. No
   schema change, no implicit migration. A row written by Go must read
   correctly in TypeScript v2.37.2 and the reverse.
4. **Libraries over hand-rolled code**, from a short, owner-approved list
   ([Libraries](#libraries)). A library is adopted when it replaces a
   meaningful amount of code or risk, not for one helper.
5. **Fast by default.** Constant query counts per page, no per-request settings
   reads, concurrent reads. Performance is measured, not assumed.
6. **Remove what no client needs.** A route or behavior with no client caller
   is a removal candidate. The owner approves each removal.

## Contracts in detail

### Persistence (fixed)

- Schema: exactly the pinned v2.37.2 schema (`SequelizeMeta` at the pinned
  migration). Go never alters it. An existing database at another migration
  level is refused at boot.
- Value formats Go must write: DATETIME as upstream stores it
  (`YYYY-MM-DD HH:MM:SS.SSS +00:00`, written only through `dbtime`), JSON
  columns as JSON text, UUIDv4 IDs, booleans as 0/1, the same enum strings.
- Semantics: the same row means the same thing (progress, sessions, ownership,
  permissions, extraData keys, `lastFirst`/`titleIgnorePrefix` derivations,
  feed and share records).
- Files: the `/metadata` layout (items' covers and `metadata.json`, author
  images, cache, backups, logs) stays readable by v2.37.2. `metadata.json`
  needs the same keys and value types, not the same bytes.
- Proof: persistence tests (Go writes, then the pinned TS server reads, and the
  reverse) and the rollback check.

### Client protocol (fixed where a client depends on it)

- HTTP: paths, methods, auth (JWT access/refresh, API tokens, OIDC, cookies),
  status codes that clients branch on, and response fields with their JSON
  types, including `null` where a client checks for it.
- Socket.IO v4 on the legacy and `/audiobookshelf` Engine.IO paths: event
  names, payload fields, and which users receive each event.
- `ROUTER_BASE_PATH` behavior, `/healthcheck`, `/ping`, `/status`, the served
  web client and the public share/feed/HLS URLs.
- Proof: contract tests (request/response field and type checks per route a
  client calls, Socket.IO event tests) and functional journeys.

### Free

Package structure, SQL text and query plans, HTTP framing (ETag style,
compression, header order, `Content-Length`), JSON key order and whitespace,
error message text that no client parses, log text and levels, internal
caches, goroutines, and anything a client cannot observe.

## Package layout (target)

Organize by domain, not by upstream controller. A domain owns its HTTP
handlers, its queries and its types.

```
cmd/audiobookshelf-go   main: config, open DB, build the server, run
internal/
  server      composition root: builds domains, mounts routes, lifecycle
  config      env/flags (the only place env is read)
  db          open/pragmas/schema check; sqlc-generated queries (internal/db/sqlc)
  dbtime      the one DATETIME format and parser
  httpx       JSON/text writers, request decoding, error->status mapping
  auth        JWT issue/verify (golang-jwt), API tokens, OIDC, permissions
  events      domain event type; realtime subscribes
  media       ffmpeg/ffprobe runner
  textclean   HTML sanitize/strip (x/net/html tokenizer)

  library     libraries, folders, filter data, search, stats
  items       library items, books, podcasts, files, covers, chapters
  people      authors, series, narrators
  lists       collections, playlists
  progress    media progress, bookmarks, listening sessions, playback, HLS
  podcasts    feeds (gofeed), episode downloads, auto-download schedule
  scanner     scan, watcher, metadata extraction
  users       users, settings, API keys, tasks visible to clients
  sharing     shares, RSS feeds out
  admin       backups, notifications, email, logs, tools, custom providers
  providers   metadata providers (Audible, Google, iTunes, ...)
  realtime    Socket.IO transport, rooms, per-user filtering
```

This is a direction, not a big-bang rename. Domains are carved out as the
roadmap touches them; current packages (`catalog`, `userapi`, `identity`,
`adminops`, ...) shrink until they disappear.

Rules:

- `httpx`, `dbtime`, `events`, `media`, `textclean` import only the standard
  library and approved libraries. Domains may import them, `db`, `auth`,
  `config`.
- Domains do not import each other's internals. Cross-domain reads go through a
  small interface or a shared query in `db`.
- Routes are registered on `net/http.ServeMux` with method patterns by each
  domain (`Routes(mux)`). The router base path is stripped once in `server`.
- Collaborators arrive through constructor arguments. No `Set*` injectors, no
  globals except the logger.
- `New` allocates; `Start` begins background work; `Close` stops it and waits.

## Data access

- Queries are SQL in `.sql` files, compiled by **sqlc** into typed Go
  (`internal/db/sqlc`). The schema sqlc reads is the pinned v2.37.2 schema
  dump; it is generated from upstream's fresh-init database, never edited by
  hand.
- Dynamic SQL (filter/sort builders for library pages) stays handwritten where
  sqlc cannot express it, in one place per domain, built from ordered slices.
- Hot reads load a page in a fixed number of queries (batch by ID list), never
  one query per item.
- One write connection. Reads may use a separate read pool once the
  read-modify-write paths are audited (ROADMAP P4). Rollback-journal mode stays,
  because TS v2.37.2 must reopen the file.
- Never issue a query on the pool while a `*sql.Rows` or `*sql.Tx` from the
  same pool is open (`tools/dbnest` fails the build). With one connection that
  deadlocks.
- Transactions are owned by the domain function that needs them.

## HTTP and JSON

- Requests and responses are typed structs. `map[string]any` is allowed only
  for opaque JSON stored in a column and passed through.
- `null` vs absent matters only where a client checks it; use `*T` for nullable
  and `omitempty` for optional, and stop there.
- Slices that clients iterate marshal as `[]`, not `null`.
- Errors map to status in `httpx`. A database error is a 500, not a 404.
- No Express emulation for its own sake. Keep a quirk only when a client
  depends on it, and name the client in a comment.

## Events and realtime

- Domains publish typed events (`events.Event`). `realtime` alone knows about
  sockets, rooms and per-user visibility.
- Event names and payload fields are client contract; delivery mechanics are
  not.

## Libraries

Approved by the owner (2026-11-03; the libraries already in use are confirmed). Adding a library requires owner approval first; propose
it with what it replaces (lines, risk) and its maintenance status.

| Library | Use | Replaces |
|---------|-----|----------|
| `modernc.org/sqlite` | pure-Go SQLite driver | (in use) |
| `github.com/sqlc-dev/sqlc` (build-time tool) | typed queries from SQL | ~590 hand-written `Query/Exec` call sites and their row scanning |
| `github.com/golang-jwt/jwt/v5` | JWT sign/verify | hand-rolled HMAC JWT in `auth`, `identity`, `userapi` |
| `golang.org/x/time/rate` | login/API rate limiting | custom buckets in `identity` |
| `golang.org/x/net/html` (tokenizer) | clean text and sanitize metadata HTML | `auth/sanitize.go`, three `stripHTML`/`sanitizeHTML` copies |
| `github.com/mmcdole/gofeed` | podcast RSS/Atom parsing | `podcasts/rss.go` XML mapping |
| `github.com/coreos/go-oidc/v3` | OIDC | (in use) |
| `github.com/zishang520/socket.io/v2` | Socket.IO v4 server | (in use) |
| `github.com/fsnotify/fsnotify` | library watcher | (in use) |
| `github.com/robfig/cron/v3` | schedules | (in use) |
| `github.com/google/uuid` | UUIDv4 | (in use) |
| `golang.org/x/crypto`, `golang.org/x/text` | bcrypt; text normalization | (in use) |

The Go toolchain follows the latest stable release (`go` directive in
`go.mod`), so libraries can use current language features.

## Metrics

`make ratchet` (`tools/ratchet`, baseline `ratchet-baseline.json`) fails when a
metric rises. When a metric falls, lower the baseline in the same commit.

Primary (simplification):

- Handwritten Go lines, non-test, excluding generated files (`// Code
  generated`). Baseline 44,326 at `768f131`.
- Total and per-function cyclomatic complexity; functions above 15.
- `map[string]any` and `[]any` uses in non-test code (1,003 and 313).

Secondary (structure): hand-rolled `Handle(...) bool` routers (13), `Set*`
injectors (18), duplicate helpers (1 each), `httptest` in non-test code (0),
nested queries on the pool (`tools/dbnest`, 0).

Commit messages report the change in handwritten lines and complexity.
Performance is tracked by `make bench` (p50/p95 per client-critical call,
memory, cold start); the roadmap names the calls to beat.

## Testing

Three kinds of test gate a change. None compares bytes with upstream.

1. **Contract tests**: for each route and Socket.IO event a client uses, the
   response has the fields the client reads, with the right types and
   nullability. Source of truth: the client code (`client/`), the mobile apps'
   observed calls, and `docs/openapi.json`. Written as Go tests against a
   seeded database.
2. **Persistence tests**: Go reads databases created by TS v2.37.2; TS v2.37.2
   reads databases Go wrote (rollback check); `metadata.json` and image files
   are readable by both.
3. **Functional tests**: end-to-end journeys a user performs (log in, browse,
   play and sync progress, download, scan new books, edit metadata, podcasts,
   collections, OIDC) against a realistic install.

The Node differential harness (`tools/parity/`) is kept as an optional,
non-blocking diagnostic until the three suites above cover the same journeys;
then it is retired (ROADMAP V-tasks). Its byte-level normalizations are not a
spec.

## Open decisions

| ID | Question | Status |
|----|----------|--------|
| D1 | Run `ANALYZE` at boot (upstream does) | Owner (ROADMAP O1) |
| D2 | Port upstream's boot cleanup `Database.cleanDatabase` | Owner (O2) |
| D3 | Which routes no client calls are removed | Owner, per route list (R2) |
| D4 | Libraries beyond the approved list | Owner, per proposal |
