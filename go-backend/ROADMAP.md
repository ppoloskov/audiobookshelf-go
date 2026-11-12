# Roadmap: the rewrite

Goal: the target in [ARCHITECTURE.md](ARCHITECTURE.md). About 24 packages and
roughly half of today's 39,700 production lines, with typed rows, typed
requests and responses, and no JavaScript emulation.

Method: strangler on `master`. Each task builds a new package on the
foundation, ports the HTTP-level tests, switches the routes over in the
composition root and deletes the old package. `internal/contracttest` stays
green at every commit.

Per-area facts (routes, tables, hazards, client dependence) are in
[docs/survey/](docs/survey/). Every brief points its worker at the matching
file; the HAZARDS section there is the list of behaviours that must survive.

Mark a task `[x]` in the commit that completes it.

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
- [ ] **D3 `library`** (`author` done as its own package; library itself open): libraries, browse, shelves, search, series, authors,
  narrators, stats, filter data. Replaces `library` (old), `people`,
  `filterdata`.
- [x] **D4 `list`**: collections and playlists. Replaces `lists`,
  `playlistview`.
- [x] **D5 `playback`**: sessions, progress, bookmarks, listening stats, HLS,
  devices. Replaces `playback` (old), `progressdto`, `deviceinfo`, the
  progress half of `userapi`.
- [x] **D6 `share`**: shares and RSS feeds out with `encoding/xml`. Replaces
  `sharing`, `feeds`.
- [x] **D7 `podcast`**: feed, creation, downloads, schedule, episodes.
  Replaces `podcasts`.
- [ ] **D8 `scan`**: walk, match, plan, apply; watcher; sidecar readers.
  Replaces `scanner`.
- [x] **D9 `user`**: account, users admin, API-key routes. Replaces the rest
  of `userapi`.
- [x] **D10 `admin`, `notify`, `mail`, `backup`, `tools`**: replaces `adminops`,
  `emailapi`, `notifications`, `backup` (old), `audiotools`.

## Wave 4: close

- [ ] **C1 `app`**: composition root and middleware chain; delete the
  hand-rolled dispatch, Express error pages and route aliases. Add the CORS
  middleware upstream has and Go lacks (mobile ebook and cover loads).
- [ ] **C2 `realtime`**: fold `events` in; typed socket actions.
- [ ] **C3** delete the old shared packages and sqlc; merge the leaves into
  `text` and `media`; remove `db.DB.SQL()`.
- [ ] **C4** tooling: drop the ratchet, dbnest and depguard layers; CI runs
  build, vet, tests, lint, cross-build and rollback.
- [ ] **C5** README and docs reflect the result; rollback and journey checks
  pass; push.

## Open decisions

| Question | Default until decided |
|---|---|
| Run `ANALYZE` at boot (upstream does) | no |
| Port upstream's boot-time `cleanDatabase` | no |
