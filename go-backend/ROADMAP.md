# Roadmap: the rewrite

The greenfield rewrite is done. The result is described in
[ARCHITECTURE.md](ARCHITECTURE.md); the current state and open items are in
[handover.md](handover.md). Below, the waves as they were planned, with the old
package each task replaced. Old names are history: none of them exists in
`internal/` any more.

Per-area facts of the old code (routes, tables, hazards) are in
[docs/survey/](docs/survey/).

## Wave 1: foundation

- [x] **F1 `db`**: `DB` handle over sqlx with context transactions, `Time`,
  `JSON[T]`, `Bool`, a row struct for all 26 tables, a struct for every
  JSON-column document, `Insert`/`Update`. Tests: every struct against the
  pinned schema; every JSON cell of the fixture database round-trips by value.
- [x] **F2 `httpx`**: `Handler`, `Error`, `JSON`, `Decode[T]`, query helpers,
  file serving, the SSRF-safe client.
- [x] **F3 `auth` core**: `Principal` from a `db.User`, typed permissions and
  their decisions, `MediaClause`, context accessors, `Admin` wrapper.
- [x] **F4 slog bridge**: `log/slog` default handler feeding the existing log
  pipeline, so new code logs with slog from day one.

## Wave 2: keystones (parallel)

- [x] **K1 `item` read model**: one loader for items, books, podcasts and
  episodes; minified/full/expanded views as typed structs. Replaces `items`,
  `books`, `podcasts/read`, `mediafiles`.
- [x] **K2 `settings`**: typed server, email and notification documents with
  atomic `Update`. Replaces `serversettings`, `emailsettings`, `settingsdoc`.
- [x] **K3 `auth` flows**: login, refresh, logout, init, API keys, OIDC on
  `x/oauth2`, the authentication middleware. Replaces `identity`, `auth`
  (old), `access`, `users`.
- [x] **K4 `logging`**: slog handler with console, daily files, ring buffer and
  socket listeners. Replaces `logging`.

## Wave 3: domains (parallel, on the keystones)

- [x] **D1 `metadata`**: one typed result per search kind behind a small
  provider interface. Replaces `providers`, `wire` match types.
- [x] **D2 `item` writes**: media PATCH, batch, relations, deletion, covers,
  files, quick match, `metadata.json` writer. Replaces `catalog`,
  `itemwrite`, `sqlupdate`.
- [x] **D3 `library`** (as `libraries`, `browse` and `author`): libraries, browse, shelves, search, series, authors,
  narrators, stats, filter data. Replaces `library` (old), `people`,
  `filterdata`.
- [x] **D4 `list`**: collections and playlists. Replaces `lists`,
  `playlistview`.
- [x] **D5 `playback`**: sessions, progress, bookmarks, listening stats, HLS,
  devices. Replaces `playback` (old), `progressdto`, the
  progress half of `userapi`.
- [x] **D6 `share`**: shares and RSS feeds out with `encoding/xml`. Replaces
  `sharing`, `feeds`.
- [x] **D7 `podcast`**: feed, creation, downloads, schedule, episodes.
  Replaces `podcasts`.
- [x] **D8 `scan`**: walk, match, plan, apply; watcher; sidecar readers.
  Replaces `scanner`.
- [x] **D9 `user`**: account, users admin, API-key routes. Replaces the rest
  of `userapi`.
- [x] **D10 `admin`, `notify`, `mail`, `backup`, `tools`**: replaces `adminops`,
  `emailapi`, `notifications`, `backup` (old), `audiotools`.

## Wave 4: close

- [x] **C1 `app`** (kept the package name `server`): composition root and middleware chain; delete the
  hand-rolled dispatch, Express error pages and route aliases. Add the CORS
  middleware upstream has and Go lacks (mobile ebook and cover loads).
- [x] **C2 `realtime`**: typed socket actions (`realtime.Actions`). `events` stayed a
  separate leaf package: domains import it, not `realtime`.
- [x] **C3** delete the old shared packages and sqlc; merge the leaves into
  `text` and `media`; remove `db.DB.SQL()`.
- [x] **C4** tooling: drop the ratchet, dbnest and depguard layers; CI runs
  build, vet, tests, lint, cross-build and rollback.
- [x] **C5** README and docs reflect the result; rollback and journey checks
  pass; push.

## Next

- [x] **Twin-Server Differential Oracle (`testmachine/oracle/`):** Replaced the
  532k-LOC legacy verification stack (`scenarios/`, `parity/`, `register/`,
  `contracts/{parity,shapes,verification-review}/`, `reports/`, `cmd/verify`,
  and `internal/contracttest/`) with a 12.5k-LOC live TS-vs-Go differential
  oracle ([testmachine/oracle/README.md](testmachine/oracle/README.md)):
  - **218/218 (100%)** in-scope upstream routes exercised live (**213/218 = 97.7%** happy-path, **214/218 = 98.2%** error-probed); **8/9** inbound & **51/62** outbound Socket.IO events observed live.
  - **78.1%** TS v2.37.2 V8 in-scope lines (**80.9%** functions) and **69.5%** weighted Go server statement coverage in ~90s via `make verify`.
  - **169/169 (100%)** non-skipped web `$axios` routes exercised in flows, 6/6 web Socket.IO emitters exercised, Playwright `web-e2e.mjs` for `client/dist`, and `cmd/abs-record` mobile capture proxy CLI.
  - Standalone `gen_routes.mjs` scans `server/` directly for clean upstream version bumps.
- [ ] **Go parity refactoring & fixes (driven by `go-parity-backlog.md`):** Close
  the unexpected Go-vs-TS findings surfaced by `make oracle` (or record
  client-safe intentional improvements in `testmachine/oracle/divergences.json`)
  until `make verify` exits zero.
- [ ] **Device acceptance of the mobile apps** (using `cmd/abs-record` against
  real iOS/Android apps).

Open follow-ups (details in [handover.md](handover.md)):

- Replace the `logging` printf helpers with `slog` and delete them.
- Delete the 12 stray `*-E` files in `internal/text` and `internal/media`.
- Fix the stale `test-ts-diagnostics` Makefile target.
- Device acceptance of the mobile apps.

## Open decisions

| Question | Default until decided |
|---|---|
| Run `ANALYZE` at boot (upstream does) | no |
| Port upstream's boot-time `cleanDatabase` | no |
