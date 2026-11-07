# Architectural simplification: ADRs and ledger

Production lines are counted by `go run ./tools/ratchet` (handwritten,
non-test, non-generated Go). Moved lines are not counted as deleted. Test,
fixture, documentation and generated-code changes are excluded.

## ADR-001: one HTTP handler shape, routes on the mux (2026-11-06)

**Context.** Handlers write their own responses and return nothing. A
failure costs 3–4 lines (`logging.RequestError`, `httputil.WriteError`,
`return`), repeated ~450 times for 5xx and ~700 times for 4xx answers.
Eight feature packages are mounted as `Handle(w, r, user) bool` chains,
each a hand-written path router (`strings.Split` plus `len(parts)` checks);
the catalog one alone is ~300 lines. The runtime names every package and
answers an unmatched `/api` route with a non-TS 501 JSON body.

**Options compared.**
- *Ours:* keep the Handle chains and per-site error writes. Rejected: it
  is the bulk of the duplication.
- *Monstrobook:* handlers return `error`, `httpx.Error` maps errors, and
  routes are registered as method patterns on `http.ServeMux`. It gets
  there by dropping Express framing (no ETags, uniform JSON errors),
  which our clients observe.
- *Chosen (third):* Monstrobook's shape plus the framing contract.
  `httputil.Handler func(w, r) error` and one mapper, `httputil.Fail`.
  The mapper writes `Error{Status, Message}` as Express `send(message)`
  (text/html with an ETag) or `sendStatus` (text/plain with an ETag), and
  logs the cause on 5xx. `httputil.JSON` adds Express's weak ETag and a
  304 on `If-None-Match`, which was missing from Go before (Lissen
  revalidates cached GETs). Packages register `Routes(*http.ServeMux)`,
  which the runtime discovers through an interface, so packages convert
  independently. One trailing slash routes like Express non-strict
  routing. An unknown route is a 404, as in TS.

**Consequences.** The legacy chain adapter (`Runtime.legacyChain`) is
deleted once the last package stops exposing `Handle`. Response options
(`WithoutETag`, `Streamed`, `ForRequest`, …) are deleted when they are
unused.

## ADR-002: the JSON contract is semantic, not Express bytes (owner, 2026-11-07)

**Decision.** Clients and TS rollback read JSON by value. The contract is the
set of keys, their JSON types and values, array order, and `null` vs absent
where a reader branches on it. Not contract, and not to be preserved with
code: object key order (HTTP bodies and persisted JSON columns alike),
whitespace and trailing newlines, which framing an error body uses
(text/html vs text/plain), and Express's exact ETag bytes (an ETag only has to
change when the body changes).

**Consequences.** Delete code whose only job is key order or byte fidelity
(ordered marshal structs, order-preserving JSON rewriters, insertion-order
bookkeeping, superset-map-and-delete shapes kept for order) and turn byte or
order assertions into semantic comparisons (`internal/compat` already ignores
key order). New code uses plain typed structs. Persisted JSON keeps its values
and nulls, because TS reads those back.

## Ledger

| Step | Commit | Handwritten before → after | Net | Complexity | Notes |
|------|--------|----------------------------|-----|------------|-------|
| Baseline | 8e17183 | 44,674 | — | 10,809 | 990 `map[string]any`, 9 Handle routers |
| M1: handlers return error; Handle chains → mux | (this commit) | 44,674 → 43,348 | −1,326 (prod +2,560 / −4,036) | 10,809 → 10,406 | 8 hand routers and the legacy adapter deleted; JSON 200 GET/HEAD gains ETag/304; unknown routes 404 |
| M2: one error framing (httputil.Fail); per-package write helpers deleted; catalog round 2 | (this commit) | 43,348 → 42,743 | −605 | 10,406 → 10,380 | ~20 helpers (bare, text, reply, reject, plainError, dbFail, …) and catalog adapters gone; error bodies follow the TS send/sendStatus choice |
| M3: ADR-002 sweep (key-order/byte-fidelity code deleted) | (this commit) | 42,743 → 42,578 | −165 | 10,380 → 10,347 | httputil Streamed/WithoutContentLength/WithoutETag, catalog writeJSONWithETag, OIDC ordered rewriter, playback ordered metadata structs; byte/order test assertions now compare values |
| M4 (C1): one typed item model, internal/libitem | (this commit) | 42,578 → 41,812 | −766 (libitem +~1,050 included) | 10,347 → 10,155 | packages books/ and items/ deleted; catalog podcast/book map builders, podcasts/item.go serializer, playback and sharing schema readers, lists per-entry SELECTs, dead wire book types; map[string]any 990 → 900, []any 326 → 301 |
| M5 (C1d + C2): typed item decorators; one item-mutation write path (internal/itemwrite) | (this commit) | 41,812 → 41,475 | −337 (new itemwrite + mutation.go +661 included) | 10,155 → 10,015 | toMap round trip, realtime map item path, sync/quickMatch author+series copies, column builders, orphan check copies, finalizeScan loops, taxonomy bodies; map[string]any 900 → 852 |
| M6 (C3 + C4): filters/sort as tables; shelves as definitions; one permission clause | (this commit) | 41,475 → 41,204 | −271 | 10,015 → 9,874 | filterClause (55), podcastFilterClause, three order builders, podcastPermissionClause, userMediaClause and personalized hand loops (37) replaced by catalog/query.go tables and one shelf loop |

### M1 behaviour changes (all toward TS/Express)

- An unknown `/api` route answers 404 `Not Found` (text/plain) instead of a
  501 JSON body.
- One trailing slash routes on every mux route (Express non-strict routing).
  `//` inside a path no longer matches (it did through `path.Clean`).
- A method that a route does not declare answers 404 instead of being served
  (catalog's any-method branches are now GET only) or answering 405 (playback
  API routes). Public `/hls` and `/public/session` keep 405.
- GET routes answer HEAD (sharing `/feed` and `/public/share`, playback).
- A non-admin sending an undeclared method to `/api/notifications*` gets 404,
  not 403 (TS per-route middleware).
- Three userapi/lists database-failure 500s (`targetUser`,
  `requireItemAccess`, `libraryListPagination`) are now Express-framed
  (text/html) instead of `http.Error` text/plain.
- playback's `GET /api/items/{id}/file/{ino}` handler was unreachable
  (catalog matched first) and is deleted.

### Mechanisms removed in M1

Hand-written path routers in catalog, userapi, sharing, playback, scanner,
podcasts, notifications and emailapi; `Runtime.legacyChain`, `features()`,
`legacyRouter`, `isPublicPlayback`, the sharing/playback public
pre-dispatch, the lists `notImplemented` 404 shim, the 501 fallback, and the
`PathUnescape` 400 branches that the mux's decoded `PathValue` makes dead.

### M4 (C1) notes

- Queries: a library page with `include=` drops from 83 to 8 queries for 20
  rows (constant in page size); collapsed-series pages from 104 to 29; shelves,
  series books, author items, subseries, search, batch/get, playlists,
  collections, items-in-progress and recent episodes load once per list
  instead of once per item. Known regression: the single-item access check in
  userapi now loads the item (about 4 queries instead of 1).
- Values that moved toward TS: a NULL series sequence in a persisted play
  session's mediaMetadata is `null` (TS `Book.oldMetadataToJSON`), not `""`;
  podcast title prefixes use the server's sortingPrefixes; an item whose
  media row is missing is a 404 instead of a 500.
- Remaining: catalog still renders the typed libitem structs into
  `map[string]any` through a JSON round trip (`toMap`) so its decorators can
  keep writing map keys. That costs one extra marshal per item and keeps
  realtime's map access path alive. Typing the catalog decorators deletes both.

### M5 notes

- Characterisation tests recorded against the old code before the refactor:
  catalog/relations_characterization_test.go (PATCH, batch, quick match),
  scanner TestScanRelationTable, adminops taxonomy golden table.
- Values moved toward TS: a series created by PATCH stores `nameIgnorePrefix`
  as the stripped name (TS `Series.js:80` `getTitleIgnorePrefix`); the scanner
  uses the shared last-first author form, stores a new series sequence as
  NULL, adds newly linked authors/series to filter data and closes the feeds
  of series it removes. Event order on single-item writes: series sweep,
  item announcement, author sweep.
- Single-item access check is one query again (`libitem.LoadAccess`).
- Realtime filters only typed item payloads; a map payload with `media` is
  never delivered (every item emitter now sends libitem values).
- Left: shared image fetch (`cover.go`/`authors.go`), and catalog still has a
  second SetItemChanged wiring.

### M6 notes

- Recorded before the refactor and unchanged after: catalog/testdata/query_golden.json
  (2,004 cases: every filter group and sort key, both media types, three
  users) and catalog/testdata/personalized_golden.json (5 users, both library
  types, settings and query variants).
- Shelves load all their items with one libitem.Load; episode shelves load only
  the episodes they show (`libitem.Options.EpisodeIDs`).
- Podcast filter values decode like book values; book genre/tag filters guard
  with json_valid like podcasts. A book sort on a key with no column
  (`media.metadata.author`, `media.numTracks`) still answers 500, as before.
- Permission code outside catalog (podcasts, emailapi, playback, sharing) still
  has its own copies; next packet.
