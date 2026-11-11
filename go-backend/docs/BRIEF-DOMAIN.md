# Worker brief: rewrite one domain (Wave 3)

You are a worker on a Go rewrite. Your task card names: the task id, the NEW
package to write, the OLD packages it replaces, and the survey file.

Repo: /Users/paul/projects/audiobookshelf-go/go-backend (module
`audiobookshelf-compat`, Go 1.27).

## Read first

1. ARCHITECTURE.md (whole) and AGENTS.md ("Rules for a worker").
2. Your survey file in docs/survey/. Its HAZARDS section lists behaviours that
   must survive; CLIENT DEPENDENCE tells you which quirks clients need.
3. The OLD packages, fully (code and tests).
4. The foundation you build on, and nothing else of the old shared code:
   - `internal/db`: `DB` (Get/Select/Exec/Insert/Update/Tx), `Time`,
     `JSON[T]`, `Bool`, row structs (models.go), JSON documents
     (documents.go), `dbtest.New`, `dbtest.Fixture`, `dbtest.EqualJSON`.
   - `internal/httpx`: Handler, errors, JSON, Decode[T], ServeFile, Download.
   - `internal/auth`: `auth.User(ctx)` principal and its permission methods,
     `MediaClause`, `ReadItem`, `auth.Admin`.
   - `internal/item`: `Load`/`Get` and the item/book/podcast/episode views.
   - `internal/settings`: `*settings.Store` (Server(), UpdateServer, Email…).
   - `internal/events`: `events.Emitter` (ToAll/ToAdmins/ToUser), `Task`.
   - logging: `log/slog` only.
   Leaf packages you may import: textsort, textclean, nameparse, textdist,
   filekind, mediatools, config, version, cronspec, ids.

## Write set

- Your new package directory (all files).
- `internal/server/runtime.go`: ONLY the lines that construct and mount the
  old packages you replace. Change them to construct and mount your package.
  Other workers edit other lines of this file at the same time: keep your
  edit to those lines, re-read the file right before editing, and never
  rewrite the file wholesale.
- Delete the old packages' directories, but ONLY those your card lists as
  "delete". An old package with other importers (card says "keep") stays.
- Do not edit go.mod, other packages, or docs. Do not commit.

## Design rules (from ARCHITECTURE.md, condensed)

- Constructor: `New(d *db.DB, settings *settings.Store, emit events.Emitter, ...deps)`
  plus `Routes(mux *http.ServeMux)`. Take concrete collaborators; an interface
  or callback only for a genuine upward call, named for what it does.
- Routes: identical method + path patterns to the old package. Wrap
  admin-only routes with `auth.Admin`. Read the user with `auth.User(r.Context())`.
- Rows: `SELECT *` into db row structs, or a small local struct with db tags
  for joins. No `rows.Scan`, no `sql.Null*`, no `map[string]any`.
- Requests: decode into typed structs with `httpx.Decode`. A PATCH body is a
  struct of pointer fields: nil = absent. Accept a number-or-string only where
  your survey says a real client sends strings.
- Responses: typed structs. Reuse `item` views; never rebuild an item, book or
  podcast projection.
- Read-modify-write inside `d.Tx(ctx, func(ctx) error {...})`, using the ctx
  it gives you. Emit events after the transaction returns.
- Events: same names, payload fields and recipients as the old code.
- Upstream-parity code kept only for a persisted value or a real client, with
  a one-line comment naming which.
- Files by concern (handlers, queries, domain logic). Doc comment on every
  exported identifier.
- **Size.** The new package's non-test lines must not exceed the old
  packages you delete; smaller is the aim. Do not add features, validations
  or mechanisms the old Go code lacked, even if upstream has them. Never
  change client-visible behaviour to save lines. If you cannot stay within
  the old size, stop and report which requirement prevents it.
- **Parity on the final code.** The parity test must pass on the version you
  hand in. If you restructure after it passed, run it again before deleting
  the old package.

## Proof

1. `parity_test.go`, header `// TEMPORARY: parity with the old package; delete
   with the old package or before commit if the old package is deleted now.`
   Write it FIRST, while the old package still exists. On `dbtest.Fixture(t)`,
   mount OLD and NEW handlers on separate muxes over separate copies of the
   fixture. For every route, send the same well-formed requests (several per
   route: typical, empty result, not found, forbidden for a restricted user)
   and compare: status, `dbtest.EqualJSON` of the bodies, the resulting rows
   of every table the route writes, and the events emitted (name, recipient,
   payload by value). Authenticate requests by putting a principal in the
   context yourself (`auth.WithUser`, plus whatever the old handler reads:
   `store.ContextWithUser` / `access.WithRequest` — look at
   internal/server/runtime.go serveRoute).
2. When it passes, port the old package's HTTP-level tests to the new
   package (they must not import old packages) and delete the parity test
   together with the old packages. Before deleting, copy any parity
   differences you accepted into the report.
3. Run: `gofmt -l internal`, `go build ./...`, `go vet ./internal/<new>/...`,
   `go test -count=1 ./internal/<new>/...` and `-race` on it, then the
   integration suite:
   `P=/private/tmp/abs-cleanup-checkpoint-20261105/ffmpeg-9.0.2-webp; PATH="$P/bin:$PATH" FFMPEG_PATH="$P/bin/ffmpeg" go test -count=1 ./internal/server/... ./internal/contracttest/...`.
   If a failure is in a package another worker is changing, report it and
   do not fix it.

## Report (your last message, at most 400 words)

Task id; files with line counts, old total vs new total; commands and
results; route list unchanged (yes/no, and any difference); every behaviour
difference found by the parity test or chosen deliberately, one line each,
with the reason (client evidence or quirk policy); old tests dropped and why;
anything you could not finish; open questions. Never claim a check you did
not run.
