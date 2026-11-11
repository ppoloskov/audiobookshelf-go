# Architecture

A Go server that replaces the TypeScript Audiobookshelf server (v2.37.2) in an
existing installation. It must be simple, compact, idiomatic Go that a person
or an agent can read top to bottom.

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
   imports the domains below it directly. A callback or interface exists only
   for a real upward call (for example "library changed, refresh watchers").
7. **Standard library first, then a mature library** when it removes real
   code. See [Libraries](#libraries).
8. **Handlers are thin**: decode, authorise, call, encode. They return an
   error; `httpx` maps it to a status once.

## Packages (target)

An import may only point down this list. The compiler rejects cycles; there is
no other layering tool.

```
cmd/audiobookshelf-go   main

app        composition root: wiring, middleware chain, static client, restore swap, lifecycle
admin      settings routes, server stats, tags/genres, upload, filesystem, cache, logs, email
backup     backup archive create/list/restore, schedule
tools      audio tools (encode m4b, embed metadata), task list
user       /api/me, users admin, API-key routes

library    libraries, browse/filter/sort, shelves, search, series, authors, narrators, stats
scan       scanner, watcher, metadata sidecar readers
podcast    feed parsing, creation/OPML, download queue, schedule, episodes
playback   sessions, progress, bookmarks, listening stats, HLS, local sync
list       collections, playlists
share      media-item shares, RSS feeds out
notify     notification settings and delivery

item       item/book/podcast loader and views, item routes, media edits, author/series links,
           deletion, covers, files, metadata.json writer, quick match
metadata   metadata providers (books, podcasts, authors, covers), custom providers

auth       principal and permissions, JWT, sessions, API keys, login/refresh/OIDC, middleware
settings   typed server/email/notification settings documents with atomic Update
realtime   Socket.IO transport, hub, audiences, the Emitter and Task types

db         open/validate/bootstrap, DB handle, column types, row and document structs
media      ffprobe/ffmpeg, file kinds, image resize
httpx      Handler, JSON in/out, errors, ETag, file serving, SSRF-safe client
text       sort keys and collation, name parsing, HTML sanitising, edit distance
logging    slog handler: console, daily files, ring buffer, socket listeners
config     flags and environment, directory layout
```

About 24 packages replace 57. The expected size is roughly half of today's
39,700 production lines.

## Foundation APIs

### db

```go
d, err := db.Open(path)                       // pragmas, one connection, schema + version check

d.Get(ctx, &row, `SELECT * FROM books WHERE id = ?`, id)        // sql.ErrNoRows when missing
d.Select(ctx, &rows, `SELECT * FROM books WHERE id IN (?)`, ids) // slice arguments expand
d.Exec(ctx, `DELETE FROM feeds WHERE id = ?`, id)
d.Insert(ctx, "books", &book)                 // every column, from the struct's db tags
d.Update(ctx, "books", &book, "title", "updatedAt") // named columns by id; all when none named
d.Tx(ctx, func(ctx context.Context) error { ... })  // calls with this ctx join the transaction
```

- `SELECT *` into a row struct is the norm. A struct that misses a column
  fails the scan, so the structs are checked against the pinned schema by a
  test.
- `db.Time` reads every stored spelling and writes
  `YYYY-MM-DD HH:MM:SS.SSS +00:00` in UTC; the zero value is NULL. It marshals
  to JSON as epoch milliseconds, or `null` when zero.
- `db.JSON[T]` reads NULL and malformed text as the zero `T`, and writes TEXT,
  never a BLOB.
- Large `IN` lists are chunked by the caller with `slices.Chunk(ids, 500)`.
- `libraryItems.title`, `titleIgnorePrefix` and `authorNames*` are set when
  the item is inserted; afterwards triggers copy them from books and authors
  and `Update` refuses to write them.
- Tests get a database from `dbtest.New(t)` (empty pinned schema) or
  `dbtest.Fixture(t)` (a copy of the upstream-created fixture install).

### httpx

```go
mux.Handle("PATCH /api/authors/{id}", httpx.Handler(h.update))

func (h *Handler) update(w http.ResponseWriter, r *http.Request) error {
    user := auth.User(r.Context())
    req, err := httpx.Decode[updateRequest](w, r)   // size-limited; 400 on malformed JSON
    if err != nil { return err }
    ...
    return httpx.JSON(w, r, view)                   // ETag + 304 on GET
}
```

Errors: `httpx.Error(status, message)`; anything else is a logged 500 with a
fixed body. Internal detail never reaches the client.

### auth

Middleware authenticates once and stores the principal in the context.
`auth.User(ctx)` returns it; `auth.Admin(h)` wraps admin-only routes.
Permission questions (`CanAccessLibrary`, `CanAccessItem`, `MediaClause` for
SQL) are methods on the principal.

### events

Domains publish through `events.Emitter` (`ToAll`, `ToAdmins`, `ToUser`).
Event names, payload fields and recipients are client contract.

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

1. **Contract and journey tests** (`internal/contracttest`): boot the real
   server on an upstream-created install and drive it over HTTP and Socket.IO.
   They survive any internal rewrite and must stay green at every commit.
2. **Domain tests**: each package tests its routes through its mux, on a
   temporary database, asserting decoded values, state and events.
3. **Rollback**: TypeScript v2.37.2 reopens a database Go wrote
   (`tools/parity/replacement-trial.cjs --rollback-only`), before every push.

Run before declaring done: `gofmt -l .`, `go vet ./...`, `go test ./...`
(see [TESTING.md](TESTING.md) for the FFmpeg prefix), `-race` for anything
with locks or goroutines.

## Libraries

| Library | Use |
|---|---|
| `modernc.org/sqlite` | pure-Go SQLite driver (no CGO) |
| `github.com/jmoiron/sqlx` | struct scanning, `IN` expansion |
| `github.com/golang-jwt/jwt/v5` | JWT |
| `github.com/coreos/go-oidc/v3`, `golang.org/x/oauth2` | OIDC |
| `golang.org/x/crypto`, `golang.org/x/time/rate` | bcrypt, rate limiting |
| `golang.org/x/net` | HTML tokenizer, WebSocket |
| `github.com/mmcdole/gofeed` | podcast feed parsing |
| `github.com/fsnotify/fsnotify` | library watcher |
| `github.com/robfig/cron/v3` | schedules |
| `github.com/google/uuid` | UUIDv4 |

Adding one needs a stated reason: what it removes and who maintains it.

## Transition

The rewrite is a strangler, on `master`, always green.
[ROADMAP.md](ROADMAP.md) has the order.

- New packages are built on the foundation beside the old ones. The old
  package is deleted in the same change that switches its routes over.
- Old shared packages (`store`, `dbtime`, `db/sqlc`, `jsonvalue`, `wire`,
  `access`, `serversettings`, ...) stay until their last importer is gone.
  New code never imports them.
- `db.DB.SQL()` exposes the `*sql.DB` for old packages only; it is removed
  with the last of them.
