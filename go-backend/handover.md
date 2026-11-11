# Go backend handover

Updated 2026-11-11.

## State

- The owner asked for a greenfield rewrite of everything between the two
  contracts (SQLite compatibility, client protocol). The design is in
  [ARCHITECTURE.md](ARCHITECTURE.md), the plan in [ROADMAP.md](ROADMAP.md).
- Baseline before the rewrite: commit `7051b3e5`, 39,740 production lines in
  57 packages, all 52 test packages green.
- Seven area surveys are in [docs/survey/](docs/survey/). They list routes,
  tables, coupling, client dependence and the hazards each rewrite must keep.
- The previous process documents (roadmap, handover, decision logs) are in
  [archive/2026-11-11-pre-rewrite/](archive/2026-11-11-pre-rewrite/). They are
  history, not instructions.

## Decisions taken

- Data access is `sqlx` struct scanning over typed row structs; sqlc and
  hand-written scans go away. A spike against the fixture database confirmed
  `SELECT *` into structs with custom DATETIME and JSON column types, `IN`
  expansion and named updates on `modernc.org/sqlite`.
- Transactions travel in the context (`db.Tx`).
- The ratchet, dbnest and the depguard layering are suspended for the rewrite
  and removed at the end (ROADMAP C4). Line counts rise while old and new
  packages coexist.
- The in-repo Socket.IO transport stays (websocket only, about 470 lines).

## Accepted behaviour changes

- `item` views emit every key of the typed audio/library/ebook file
  documents; a stored file that lacked a key now shows it with its zero value.
- A JSON column that does not decode into its Go type reads as empty and is
  logged; it is written back unchanged until code assigns a new value.
- The admin wrapper also requires an active user; a non-boolean stored
  permission flag grants nothing.

- Wave 3 so far (`list`, `share`, `metadata`, `notify`, `mail`): wrong JSON
  types in a request body are a 400; error bodies are plain text; 5xx bodies
  are fixed text. `share` fixes two old bugs (empty 200 for unknown feed
  items; book episode pubDate in local time labelled GMT). Custom metadata
  providers go through the SSRF guard.

## Size, measured

Typed rewrites of dense old code come out about the same size (Wave 3 so
far: 6,205 old lines, 6,034 new). Expect real reductions only where the old
code duplicates or emulates JavaScript, and from deleting the old shared
packages and sqlc at the end.

## Known gaps to close

- Upstream's CORS middleware has no Go equivalent (ROADMAP C1).
- The scanner uses `cron.New()` rather than the 5-field parser, and one bad
  library cron fails server start (fix in D8).

## Next

Wave 1 of the roadmap.
