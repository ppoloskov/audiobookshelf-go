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

- [ ] **Unified verification (foundation accepted; coverage open):** one Go-first/.mjs machine
  ([VERIFICATION.md](VERIFICATION.md)). Deliver a suite certified against pinned
  upstream v2.37.2 and ready for a separate Go comparison. Existing exclusions
  retained. Migrate useful assertions
  into the shared backend-independent suite without dropping older evidence.
  The November 18 plan found scenarios in 17 of 34 owner packets. Twelve
  filesystem-browse and path-existence scenarios passed upstream. Seven other
  admin-file requirements still need scenarios and packet review. The item-write
  repair resolved 50 missing bindings and passed 77 upstream scenarios.
  Playback sessions resolved 21 missing bindings and passed 31 upstream scenarios.
  Full plan validation succeeds and all 968 declared upstream scenarios passed
  in the integrated run. The new `store` packet adds three passing checks for
  repeat boot and bookmark, progress and catalog file persistence. The second
  slice adds fresh init, offline sessions and settings durability. The latest
  run passed 974 of 975 scenarios across 18 owner packets. Repair the intermittent
  issue-count setup in `library_data` next. Tagged lint, unbound invariants,
  incomplete owner packets and the whole-contract audit still block certification.
  [Current handover](handover.md)
  Q01 tools adds upstream scenarios for immediate, waiting and repeated metadata embeds. [Queue review and discovery blockers](docs/verification/q01-embed-queue-review.md).

- [x] **Q00 requirement mapping (architect accepted):** [complete register and test mapping](docs/verification/replacement-requirement-map.md),
  [source reconciliation](docs/verification/replacement-inventory-reconciliation.md),
  and [ordered gap queue](docs/verification/replacement-gap-queue.md). This is
  inventory, not whole-contract verification. Acceptance/review evidence is in
  [the map task](../docs/agent-work/tasks/TASK-20261113-replacement-contract-map.md).
- [ ] **Q01 tools — source-assertion family, implementation deferred:** audio task progress and
  metadata queue lifecycle. Failure advancing the next waiter, task progress and persisted media effects remain open. One owner per invocation; review
  between packets. Upstream assertion closure now precedes Go feature fixes. Q02–Q29 follow the queue, with packet splitting when needed.
  Accepted playback lifecycle evidence remains separately scoped.

### Verification delivery plan

1. **Done.** Full plan validation passes after reconciling the declared
   `item_write` and `playback_sessions` invariants with pinned source.
2. **Baseline captured.** All 968 existing scenarios passed against upstream.
   Resolve the inherited tagged lint findings in their owner packets. Check that the
   upstream-only verdict can be reproduced on a supported clean setup and
   reports discovery, assertion and review failures clearly. Address confirmed
   runner defects in their own tooling packet. Review how the whole-contract
   receipt depends on the local Node executable, installed dependencies and
   Go application tree when establishing a reproducible handoff.
3. Close the requirement gaps in `tests/replacement/packets.json`. Reuse working
   fixtures, shared scenarios and reviewed assertions first. Prioritise database
   state, lifecycle, real-client authentication and Socket.IO, scan and playback,
   then fill remaining owner requirements. Each packet must substantiate its
   errors, edge cases, transitions and durable and protocol effects as applicable.
   Source-execution data identifies missed paths for review. Use the existing
   author and library-data assertions when reconciling `items_updated` across
   its producers and add evidence for any remaining scan producers.
4. Give a Terra worker one bounded owner packet per invocation at medium effort.
   The architect reviews the source and client claims and reruns the acceptance
   checks before the next dispatch. Reuse current receipts. Use a single extra
   review for high-risk or failed packets as needed, and reserve an independent
   cross-packet audit for the complete source, client and persistence inventory.
5. Deliver when `make verify VERIFY_ARGS="-target upstream"` exits zero from a
   documented setup, with all applicable facets and active review receipts
   accepted, upstream scenarios passing and input digests stable. Include the
   report, setup and exact rerun command. The Go comparison and rollback trial
   against Go-written data belong to the subsequent replacement assessment.

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
