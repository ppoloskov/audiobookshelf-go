# Architecture

A Go server that replaces the TypeScript Audiobookshelf server (v2.37.2) in an
existing installation. It is simple, compact, idiomatic Go that a person or an
agent can read top to bottom.

## The two contracts

Everything between these two is ours to design.

1. **The SQLite database** and the `/config` + `/metadata` trees it points at.
   An existing v2.37.2 install opens unchanged, and the TypeScript server can
   reopen whatever Go wrote (rollback). Same tables, columns, value formats
   and meaning. No schema change, no migration.
2. **The client protocol**: the HTTP routes, JSON fields and types, auth flows
   and Socket.IO events that the bundled web client (`../client`), the mobile
   apps and documented third-party clients actually use.

`../server` (upstream, pinned at `edb1f7ef`) is a reference for what the
contracts mean. It is not a template. We do not reproduce JavaScript,
Express or Sequelize behaviour for its own sake.

### What "client contract" means in practice

Keep a behaviour only when one of these holds:

- a persisted value depends on it;
- a real client reads the field or branches on the status. Evidence is the
  web client source, `contracts/mobile-client-audit.md`, or a contract or
  journey test.

Otherwise choose the simplest sane behaviour:

- Request bodies decode into typed structs. A wrong type is a 400. Accept a
  number-or-numeric-string only where a real client sends strings.
- Responses are typed structs. Key order, whitespace, exact ETag algorithm,
  error-page HTML and error text nobody parses are free.
- No truthiness, `Number()`, `changed()` tracking or key-order emulation.

The deliberate differences from the pre-rewrite Go build are listed in
[docs/CHANGES.md](docs/CHANGES.md).

## Design rules

1. **One data mechanism.** Rows scan into typed structs (`internal/db`) with
   `sqlx`. SQL is written as SQL, next to the code that needs it. No code
   generation, no hand-written `rows.Scan`, no `sql.Null*` in domain code.
2. **`db` owns every persisted shape**: one struct per table and one struct
   per JSON-column document. Column types do the format work once:
   `db.Time` (the Sequelize DATETIME text), `db.JSON[T]` (JSON as TEXT),
   `db.Bool` (nullable 0/1). Nullable text and numbers are pointers.
3. **Transactions travel in the context.** `db.Tx(ctx, fn)` puts the
   transaction in `ctx`; every `db` call made with that context joins it.
   There is one write connection, so this makes "query the pool inside a
   transaction" (a deadlock) impossible to write.
4. **Typed in, typed out.** No `map[string]any` documents. `any` only for
   genuinely open data, with a comment.
5. **One projection per entity and shape.** An item, book, podcast or user has
   one loader and its minified/full/expanded views are methods on it.
6. **Packages are domains.** A domain owns its routes, queries and types, and
   imports the domains below it directly. A callback exists only for a real
   upward call (for example "library changed, refresh watchers").
7. **Standard library first, then a mature library** when it removes real
   code. See [Libraries](#libraries).
8. **Handlers are thin**: decode, authorise, call, encode. They return an
   error; `httpx` maps it to a status once.

## Packages

`cmd/audiobookshelf-go` plus 32 packages under `internal/` and the test helper
`db/dbtest` (the module is `audiobookshelf-compat`). The list is in import order: a package may import
only packages below it. The compiler rejects cycles; there is no other
layering tool. This order was derived from the real import graph
(`go list -f '{{.ImportPath}}: {{join .Imports " "}}' ./internal/... ./cmd/...`).
Packages in the same group do not import each other.

```
cmd/audiobookshelf-go  main: flags, logging, database, Manager, HTTP server, shutdown

server     composition root: builds every domain over one database, middleware chain,
           static client, backup-restore swap (Manager), lifecycle
browse     item list with filter/sort, shelves, search, series list/detail/PATCH
library    library CRUD, order, stats, filterdata and people routes, OPML, zip, cleanup
user       /api/me, users admin, API keys, per-user progress/bookmark/stats routes

author     author routes: detail, rename, merge, delete, match, image
list       collections and playlists
mail       email settings, e-reader devices, test mail, send ebook (SMTP + MIME)
playback   sessions, progress sync, offline sync, direct play, HLS
podcast    feed parsing, creation/OPML, download queue, schedule, episodes
realtime   Socket.IO v4 / Engine.IO transport, hub, audiences, per-user event filtering
scan       library scan, item rescan, watcher, cron schedules, sidecar readers, metadata.json
share      media-item shares, RSS feeds out

item       item/book/podcast/episode loader and views, item routes and writes, covers,
           files, deletion, quick match

admin      settings routes, stats, tags/genres, upload, filesystem, cache, logs
backup     backup archive create/list/restore, schedule
metadata   metadata providers (books, podcasts, authors, chapters, covers), custom providers
notify     notification settings, event catalogue, Apprise delivery
tools      audio tools (encode m4b, embed metadata) and their task list

auth       Principal and permissions, JWT, sessions, API keys, login/refresh/OIDC, middleware

filterdata library filter menus and their cache
settings   typed server/email/notification documents, Store with atomic Update

db         open/validate/create, DB handle, column types, row and document structs
           db/dbtest: throwaway databases and JSON diff helpers for tests

httpx      Handler, JSON in/out, errors, file serving, SSRF-guarded client
events     Emitter and Task types
config     flags and environment, directory layout
cronspec   the one cron dialect (five fields)
deviceinfo user-agent parsing
ids        id generation
logging    slog handler: console, daily files, ring buffer, socket listeners
media      ffprobe/ffmpeg, file kinds
text       collation and sort keys, title prefixes, name parsing, HTML cleaning, edit distance
version    the upstream version string
```

Test-only packages: `internal/contracttest` (imports `server`) and
`internal/contracttest/shape`.

Edges you might not expect:

- `realtime` imports `item`: it filters item events per user with the typed
  item views. `user` imports `realtime` (online users) and `playback`.
- `item` imports `metadata` for the result types of its `BookFinder` interface;
  `metadata` therefore sits below `item`.
- `library` imports `author`; `browse` imports `library`, `author` and `item`.
- `auth` imports `settings`, `events` and `httpx`; it does not import `item`.
- `admin` imports `logging` for the log ring buffer.
- `backup` does not import `db`: it snapshots the database file through its
  own read-only SQLite connection (the driver is imported there).
- `cmd/audiobookshelf-go` imports `db`, `settings` and `logging` as well as
  `server`, to open the database and set up logging before the Manager starts.

## Foundation APIs

### db

```go
func Open(path string) (*DB, error)                          // existing file: pinned migration state + every row-struct column; never writes
func Create(path string, defaults []Setting) error           // new file: pinned schema, migration rows, settings rows
func OpenOrCreate(path string, defaults []Setting) (*DB, error)
func FilePath(configDir string) string

func (d *DB) Get(ctx context.Context, dest any, query string, args ...any) error     // sql.ErrNoRows when missing
func (d *DB) Select(ctx context.Context, dest any, query string, args ...any) error  // dest is *[]T; slice args expand for IN (?)
func (d *DB) Exec(ctx context.Context, query string, args ...any) (sql.Result, error)
func (d *DB) Insert(ctx context.Context, table string, row any) error                // every db-tagged column
func (d *DB) Update(ctx context.Context, table string, row any, columns ...string) error // by id; all columns when none named
func (d *DB) Tx(ctx context.Context, fn func(ctx context.Context) error) error      // calls with this ctx join the transaction
func (d *DB) Close() error
```

- `SELECT *` into a row struct is the norm. A struct that misses a column
  fails the scan; `Open` checks every column of every row struct.
- `Connect` and `ConnectWrapped` exist for tests (`ConnectWrapped` counts or
  fails queries). `Open` refuses any database whose migration state is not the
  pinned release (`ErrSchema`).
- `db.Time` reads every stored spelling and writes
  `YYYY-MM-DD HH:MM:SS.SSS +00:00` in UTC; the zero value is NULL. It marshals
  to JSON as epoch milliseconds, or `null` when zero. `db.Now()`,
  `db.FromMillis(ms)`.
- `db.JSON[T]` (field `V`) reads NULL and malformed text as the zero `T`,
  keeps text it cannot decode and writes it back unchanged until `V` is
  assigned, and writes TEXT, never a BLOB.
- `db.Bool` is a nullable TINYINT(1); NULL reads as false.
- `db.Ptr(v)` and `db.Val(p)` make and read nullable-column pointers.
- A slice argument expands to a placeholder list; an empty slice binds NULL
  (`x IN (?)` matches nothing). Callers chunk long lists with
  `slices.Chunk(ids, 500)`.
- `libraryItems.title`, `titleIgnorePrefix` and `authorNames*` are set when the
  item is inserted; afterwards triggers copy them from books and authors and
  `Update` refuses to write columns tagged `dbwrite:"insert"`.
- `dbtest.New(t)` is an empty pinned schema. `dbtest.Fixture(t)` is a copy of
  the upstream-created fixture install. `dbtest.Counting(t)` counts queries
  that return rows. `dbtest.OpenRaw(t, path)` is a second `*sql.DB` for
  assertions. `dbtest.RoundTrip`, `DiffJSON` and `EqualJSON` compare JSON by
  value.

### httpx

```go
type Handler func(w http.ResponseWriter, r *http.Request) error   // ServeHTTP maps the error once

func Decode[T any](w http.ResponseWriter, r *http.Request) (T, error)  // 1 MiB limit, unknown fields ignored; 400 malformed/mistyped, 413 oversized
func DecodeLimit[T any](w http.ResponseWriter, r *http.Request, limit int64) (T, error)
func JSON(w http.ResponseWriter, r *http.Request, value any) error     // ETag + 304 on GET
func JSONStatus(w http.ResponseWriter, r *http.Request, status int, value any) error
func Text(w http.ResponseWriter, status int, body string) error
func Status(w http.ResponseWriter, status int) error
func ServeFile(w http.ResponseWriter, r *http.Request, path string) error
func Download(w http.ResponseWriter, r *http.Request, path, filename string) error
func Error(status int, message string) error
func BadRequest(message string) error
func NotFound() error
func Forbidden() error
func Internal(message string, cause error) error   // fixed message to the client, cause logged
func QueryInt(r *http.Request, key string, fallback int) int
func QueryBool(r *http.Request, key string) bool
func ClientIP(r *http.Request) string
func NewSafeClient() *http.Client                  // SSRF-guarded outbound client
```

```go
mux.Handle("PATCH /api/authors/{id}", httpx.Handler(h.update))

func (h *Handler) update(w http.ResponseWriter, r *http.Request) error {
    user := auth.User(r.Context())
    req, err := httpx.Decode[updateRequest](w, r)
    if err != nil { return err }
    ...
    return httpx.JSON(w, r, view)
}
```

Any error that is not an `httpx` error is a logged 500 with a fixed body.
Internal detail never reaches the client. Error bodies are plain text.

### auth

```go
func New(d *db.DB, st *settings.Store, emit events.Emitter, cfg config.Config) (*Service, error)
func (s *Service) Routes(mux *http.ServeMux)                       // login, refresh, logout, init, OIDC (public)
func (s *Service) Middleware(next http.Handler) http.Handler       // authenticates, 401 or principal in ctx
func (s *Service) Authenticate(r *http.Request) (*Principal, error)

func User(ctx context.Context) *Principal                          // nil when absent; a nil Principal denies everything
func WithUser(ctx context.Context, p *Principal) context.Context
func NewPrincipal(u db.User) *Principal
func Admin(next http.Handler) http.Handler                         // root/admin and active only; else 403

func (p *Principal) IsAdmin() bool
func (p *Principal) IsRoot() bool
func (p *Principal) CanDownload() bool
func (p *Principal) CanUpdate() bool
func (p *Principal) CanDelete() bool
func (p *Principal) CanUpload() bool
func (p *Principal) CanAccessLibrary(libraryID string) bool
func (p *Principal) CanListLibrary(libraryID string) bool
func (p *Principal) CanAccessItem(libraryID string, explicit bool, tags []string) bool
func (p *Principal) CanAccessContent(explicit bool, tags []string) bool
func (p *Principal) MediaClause(alias string) (clause string, args []any)   // SQL fragment for list queries
func (p *Principal) ReadItem(ctx context.Context, d *db.DB, itemID string) (access ItemAccess, exists, allowed bool, err error)
```

The middleware authenticates once and stores the principal in the context. A
principal is a snapshot of the `users` row and is never mutated. `Service` also
owns users, sessions and API keys (`CreateUser`, `ChangePassword`,
`CreateAPIKey`, `Sessions`, ...).

### settings

```go
func NewStore(ctx context.Context, d *db.DB, getenv func(string) string, backupsDir string) (*Store, error)
func Fresh(backupsDir string) ([]db.Setting, error)                // defaults for a new database

func (s *Store) Server() Server                                     // cached snapshot
func (s *Store) Email(ctx context.Context) (Email, error)
func (s *Store) Notifications(ctx context.Context) (Notifications, error)
func (s *Store) UpdateServer(ctx context.Context, fn func(*Server) error) (Server, error)
func (s *Store) UpdateEmail(ctx context.Context, fn func(*Email) error) (Email, error)
func (s *Store) UpdateNotifications(ctx context.Context, fn func(*Notifications) error) (Notifications, error)
func (s *Store) OnChange(fn func(Server))
func (s *Store) Reload(ctx context.Context) error
```

Each `Update*` is one atomic read-modify-write. Types that hold secrets
(`Secret`, `Server`, `Email`, `AuthSettings`) redact in `String()`.

### events

```go
type Emitter func(audience, event string, payload any)    // nil Emitter discards
func (e Emitter) ToAll(event string, payload any)
func (e Emitter) ToAdmins(event string, payload any)
func (e Emitter) ToUser(userID, event string, payload any)
func (e Emitter) TaskStarted(t Task)
func (e Emitter) TaskFinished(t Task)
```

`events` is a leaf. The composition root passes `hub.Emit` as the Emitter, so
domains never import `realtime`. Event names, payload fields and recipients are
client contract.

### filterdata

```go
func NewCache() *Cache
func (c *Cache) Get(ctx context.Context, d *db.DB, libraryID, mediaType string) (Data, error)
func (c *Cache) Invalidate(libraryID string)       // "" invalidates every library
```

One cache is created per Runtime and handed to the domains that write items,
authors, series or libraries. It computes on demand; a document also expires
after `filterdata.TTL` (30 minutes). An `Invalidate` that races
a `Get` wins: a document read before the invalidation is not stored.

### realtime

```go
func New(cfg config.Config, backend func() Backend) *Hub
func (h *Hub) Emit(userID, event string, payload any)   // the Emitter delivery function
func (h *Hub) EmitSocket(socketID, event string, payload any) bool
func (h *Hub) Refresh(ctx context.Context, userID string) error    // permissions changed
func (h *Hub) UsersOnline() []OnlineUser
func (h *Hub) ServeHTTP(w http.ResponseWriter, r *http.Request)    // /socket.io
func (h *Hub) Close()
```

`Backend` is what the hub needs from the running server: `Authenticate`, `DB`,
`Session` and the `Actions` clients can trigger (`CancelScan`,
`SetLogListener`, `RemoveLogListener`, `SearchCovers`, `CancelCoverSearch`).
The hub is asked for the current `Backend` on every use, because it outlives a
backup restore. Item payloads are filtered per recipient with the typed views'
`Access()`.

## Request pipeline

`internal/server` builds one `Runtime` per database (`newRuntime`,
`runtime.go`). `build` constructs the domains in dependency order and mounts
each one with `Routes(*http.ServeMux)` on a single route table. `newHandler`
(`handler.go`) wraps it:

```
recover -> security headers -> CORS -> base path -> trailing slash -> public mux
```

- `recoverPanics`: logs a panic with its stack, answers 500 if nothing was
  written; also writes the DEBUG access line (TRACE for `/ping` and
  `/healthcheck`).
- `securityHeaders`: `Referrer-Policy`, and `frame-ancestors` unless the
  `allowIframe` setting is on.
- `corsPolicy`: upstream's rule (`server/Server.js:270-295`): cover and ebook
  routes for the mobile origins, everything for `allowedOrigins`, everything
  with `ALLOW_CORS=1`. A preflight answers 200 `OK`.
- `basePath`: strips `ROUTER_BASE_PATH` when present; requests without it are
  served too. The bare base path redirects to its root.
- `cleanPath`: `/x/` is routed as `/x`; any other non-canonical path is a 404.

The public mux decides authentication:

- Answered without a user: the `auth` flows, `/ping`, `/healthcheck`,
  `/status`, `/socket.io`, `GET /api/items/{id}/cover`,
  `GET /api/authors/{id}/image`.
- `/api` and `/api/`: behind `auth.Middleware`. A path that matches a domain
  route runs it; any other is a 404, which clients fall back on. Admin-only
  routes wrap themselves with `auth.Admin`.
- Everything else: if a domain registered the pattern (feeds, shares, HLS and
  session track URLs, which are capability URLs outside `/api`) that domain
  serves it and checks its own capability; otherwise the web client (an SPA:
  any extension-less GET gets `index.html`).

`Manager` (`manager.go`) sits in front of the Runtime. It owns the database and
the realtime hub and swaps the whole Runtime on a backup restore (move the old
files aside, put the uploaded ones in, reopen, rebuild; undo on any failure).
Requests get 503 while no Runtime is installed. Background work stops in
`Runtime.Close`, last built first. The scanner's watchers and schedules begin
in `Runtime.start`; the podcast and backup schedules begin in their `New`.

## Conventions

How a domain package is written. The existing packages are the examples.

- **Shape.** `New(d *db.DB, ...)` takes the database, the collaborators it
  needs, and an `events.Emitter`; `Routes(mux *http.ServeMux)` registers
  method + path patterns. Concrete collaborators, not interfaces. Doc comment
  on every exported identifier.
- **Typed in, typed out.** Decode the body into a struct with `httpx.Decode`.
  Respond with a struct. Rows are `SELECT *` into `db` row structs, or a small
  local struct with `db` tags for a join.
- **PATCH.** A field the client may omit is a pointer: nil is absent. JSON
  `null` also decodes to nil, so where a real client sends `null` to clear a
  value (the web client does for an emptied text field) use a small type with
  `UnmarshalJSON` that records null (see `item.Text`, `playback.Field`,
  `library` and `author` `optional`). Test null, absent and `""` through the
  route.
- **Wrong types are a 400.** Accept a number-or-string only where a real client
  sends strings, and say which client in a comment.
- **Transactions.** A read-modify-write runs in `d.Tx(ctx, func(ctx) error)`
  and passes the `ctx` it receives to every call inside.
- **Events after commit.** Emit when `Tx` has returned, never inside it.
  Names, payloads and recipients are client contract.
- **Upward calls are funcs.** A domain never imports a package above it. When
  it must call one (for example "the item changed, rewrite metadata.json"),
  the constructor takes a `Deps` struct with a func field named for what it
  does; the composition root fills it. Examples: `item.Deps`, `library.Deps`,
  `author.Deps`, `backup.Restore`.
- **One projection per entity.** Reuse `item` views for items, books, podcasts
  and episodes; do not rebuild a projection in another package.
- **Errors.** Return `httpx.Error`/`BadRequest`/`NotFound`/`Forbidden` for
  client errors. Wrap a server fault with `httpx.Internal(message, cause)`:
  fixed message out, cause logged. Never leak SMTP, DB or path detail to a
  non-admin.
- **Parity comments.** Code that keeps an upstream behaviour for a persisted
  value or a real client carries a one-line comment naming which (and the
  upstream file and line when it has one).
- **Files.** One concern per file; handlers, queries and domain logic apart.

## Persistence details that must not change

- Rollback-journal mode (no WAL), `foreign_keys(1)`, `busy_timeout(5000)`, one
  connection.
- A database whose migration version is not the pinned one is refused.
- Fresh install: the pinned schema, migration rows and default settings are
  created exactly as upstream would.
- JSON columns hold compact JSON (some upstream filters use `LIKE` on them).
- Link rows (`bookAuthors`, `bookSeries`) get `createdAt` one millisecond
  apart so read order equals insert order.
- `metadata.json`, the backup archive layout and the daily log files stay
  readable by v2.37.2.

## Testing

Three gates. None compares bytes with upstream.

1. **Contract and journey tests** (`internal/contracttest`): the fixture is an
   installation that upstream v2.37.2 created and scanned
   (`testdata/install`, rebuilt by `tools/contractfixture/build.cjs`). The
   tests copy it, serve the real server in-process and drive it over HTTP and
   Socket.IO: journeys (items, library, lists, playback, podcasts, feeds,
   admin, events), mobile protocol, persistence, permission revocation, and
   response shapes (`contracttest/shape`).
2. **Package tests**: each domain tests its routes through its own mux on a
   `dbtest` database (empty schema or the fixture), asserting decoded values,
   rows and events. `internal/server` tests the pipeline and the restore swap.
   `internal/text` compares collation and name parsing with fixtures that came
   from Node (`text/testdata`, generated by `tools/gen-collation-fixture.cjs`).
3. **Rollback**: TypeScript v2.37.2 reopens a database Go wrote, before every
   push. From the repository root, with Node 22:
   `SSRF_REQUEST_FILTER_WHITELIST=127.0.0.1 node go-backend/tools/parity/replacement-trial.cjs --rollback-only`
   (`make parity-replacement` runs the whole trial). See [handover.md](handover.md)
   for the last recorded result.

Run before declaring done (from the repository root: `make check`):

```sh
cd go-backend
gofmt -l .          # must print nothing
go vet ./...
go test ./...       # FFmpeg prefix: TESTING.md
go test -race ./internal/<changed>/...   # for anything with locks or goroutines
```

Tests that need ffmpeg with libwebp are described in [TESTING.md](TESTING.md).
`tools/parity` (Node differential harness) is advisory; it compares two running
servers and is not part of `go test`.

## Libraries

Direct requirements in `go.mod`:

| Library | Use |
|---|---|
| `modernc.org/sqlite` | pure-Go SQLite driver (no CGO); also the backup package's database copy |
| `github.com/jmoiron/sqlx` | struct scanning, `IN` expansion |
| `github.com/golang-jwt/jwt/v5` | JWT |
| `github.com/coreos/go-oidc/v3`, `golang.org/x/oauth2` | OIDC |
| `golang.org/x/crypto` | bcrypt |
| `golang.org/x/time` | login rate limit |
| `golang.org/x/net` | HTML tokenizer (`text`), charset detection (`podcast`), WebSocket (`realtime`) |
| `golang.org/x/text` | collation and normalisation (`text`, `metadata`) |
| `golang.org/x/sys` | `unix` stat calls in `scan` (Linux) |
| `github.com/mmcdole/gofeed` | podcast feed parsing |
| `github.com/fsnotify/fsnotify` | library watcher |
| `github.com/robfig/cron/v3` | schedules (through `cronspec`) |

Adding one needs a stated reason: what it removes and who maintains it.
