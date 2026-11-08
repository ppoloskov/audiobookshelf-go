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
order assertions into semantic comparisons (`tools/parity` already ignores
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
| M7 (P + S): one permission owner (internal/access), cached server settings (internal/serversettings), scanner walk/group pipeline | (this commit) | 41,204 → 40,817 | −387 | 9,874 → 9,703 | ~10 per-package permission helpers and ~12 admin checks, 4 per-request settings SELECTs, 5 directory walks, 2 grouping algorithms, the per-item stored-row lookup in importGroup |
| M8: review fixes (authorization, settings lifecycle) + M-b providers + M-c playback/podcasts typing | (this commit) | 40,817 → 40,725 | −92 | 9,703 → 9,558 | provider map building and helpers; playback openJSON/clientJSON maps and payload helpers; podcasts xmlNode parser and checkNew copy; map[string]any 823 → 675 |
| M9 (S2): scanner inode index and importGroup gather/plan/apply | (this commit) | 40,725 → 40,899 | **+174 (approved growth exception)** | 9,558 → 9,569 (+11 total; max function 105 → 34) | quadratic matchGroupByInode replaced by a per-scan index; importGroup (291 lines, complexity 105) split into gather / pure plan / apply / afterImport |
| M10: itemwrite orphan-sweep safety fix | (this commit) | 40,899 → 40,928 | +29 (correctness fix) | 9,569 → 9,570 | check-then-delete replaced by one DELETE … NOT EXISTS … RETURNING |
| M11 (PERM2): request-scoped permissions; realtime revocation | (this commit) | 40,928 → 41,020 | +92 (approved priority; security) | 9,570 → 9,593 | ~100 per-helper access.Of decodes replaced by one per request |
| M12 (refactor waves A+B, docs/REFACTOR-LOG.md) | (this commit) | 41,020 → 39,324 | −1,696 | 9,593 → 8,944 | read-only mode, abs-compat, duplicate settings/DB/text/coercion helpers, private-IP checks, filter-data round trips, catalog-only wire types |
| M13 (refactor wave C) | (this commit) | 39,324 → 38,906 | −418 | 8,944 → 8,805 | duplicate image downloaders, library prologues, ffprobe/MIME tables, device upserts, admin checks, settings-row readers; userapi raw SQL |
| M14 (refactor wave C4) | (this commit) | 38,906 → 38,622 | −284 | 8,805 → 8,706 | emitter nil guards and audience strings, five task types, Set* wiring (17 → 4), podcasts↔scanner cycle |
| M15 (verifier round 1 + wave D) | (this commit) | 38,361 → 37,831 | −530 | 8,706 → 8,487 | deletion cascade copy, item lookup copies, feed/user/settings builders, untyped audio entries, wire under store/logging |

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

### M7 notes

- Recorded before the refactor and unchanged after: access/testdata/decisions.json
  (old helpers' decisions over 128 users × 30 items) and
  scanner/testdata/scan_golden.json (rows, sidecars and events for full scan,
  rename/delete/add, a watcher batch and a forced item scan).
- Toward TS `User.js:557-571`: the update/delete/download/upload flags are
  `isActive && !!flag` everywhere (inactive non-admins can no longer update or
  delete podcasts or collections; non-boolean truthy flags count); explicit
  podcasts are hidden from inactive users as catalog already did.
- Server settings are cached per database handle and invalidated by all three
  writers (PATCH /api/settings and sorting prefixes, backup path, token
  secret); a restore opens a new handle. Identity and the scanner still read
  the row directly.
- The scanner's single-item scan and watcher now follow the library scan's
  ignore rules (.ignore directories, temporary extensions, dot directories).
- Left in the scanner: typed audio files and sidecar (arch-scanner §6.4/6.7),
  one ffprobe implementation (§6.5).

### M8: review fixes (owner review of 111fef8)

- **List and item authorization agree** (P1). `access.MediaClause` selects
  exactly what `Content` allows; `TestMediaClauseMatchesContent` checks every
  user × item of the matrix. Two mismatches fixed: an empty allow list
  (accessAllTags false, nothing selected) now hides every item from list
  queries, failing closed where TS's list SQL skips the filter
  (`libraryItemsBookFilters.js:27`) although its item check denies; and
  explicit media needs an active user at item level too
  (`User.canAccessExplicitContent`). A user row without `accessAllTags` is an
  empty allow list (TS defaults always include the key). End-to-end:
  contracttest `TestEmptyTagAllowListSeesNothing`.
- **Malformed permissions grant nothing** (P1): a document that does not
  decode completely, including a later field of the wrong type, is discarded.
- `access/testdata/decisions.json` re-recorded; every changed cell falls under
  the rules above or the M7 notes, and `podSQL`, `catalogUpdate`, `podDelete`
  are now checked (recorded before but never asserted).
- **Settings cache** (P2): immutable snapshots (`Doc()` decodes a copy,
  prefixes cloned), a read overlapping an invalidation is repeated instead of
  returning an older document, and `Forget` drops a handle's entry on every
  close in the restore manager. Tests run under -race.
- Providers: a supplied `false`/`0` keeps its key (the web match modal
  branches on `explicit != null`, `Match.vue:213`); FantLab year stays a
  number (`FantLab.js:113`).
- Playback/podcasts: JSON bodies up to 10 MiB as TS (`Server.js:352`); OPML
  attribute names case-insensitive (htmlparser2 lowercases); feed episode
  `guid` null and `chapters` [] when missing (`podcastUtils.js:278-288`);
  `enclosure.length` accepts a number or a string.
- S2 was held back here for review and landed in M9.

### M9 (S2) growth review

- **Approval:** the owner approved the exception (2026-11-07). +60 was a review
  threshold; the narrow baseline raise is +174 handwritten lines, +11 total
  complexity (each new function adds a base point) and +3 `map[string]any`
  (audio and chapter maps carried in planInput/itemPlan).
- **Benefit, measured:**
  - Largest scanner function 105 → 34 (`plan`); `scanLibrary` 47 → 42;
    functions above complexity 15 unchanged at 145.
  - Matching is O(items + groups + files) instead of O(items × groups) with an
    os.Stat per candidate. BenchmarkMatch (old algorithm kept test-only for
    comparison), 4 vCPU:

    | stored items | old | new | speedup |
    |---|---|---|---|
    | 1,000 | 24.0 ms / 264k allocs | 1.85 ms / 12k allocs | 13× |
    | 10,000 | 2.54 s / 25.7M allocs | 24.4 ms / 123k allocs | 104× |
    | 50,000 | 57.4 s (worker run, BENCH_OLD_LARGE=1) | 125 ms / 616k allocs | ~460× |

    New grows ~67× from 1k to 50k items (linear); old ~2,300× (quadratic).
- **Semantics preserved:**
  - The matcher returns the same group as the old function: path first, then
    the earliest unclaimed group in scan order holding the item's inode or a
    stored file inode. An agreement test runs both algorithms on the same
    inputs.
  - Tests: matching rules and linear work (matcher_test.go), 13 pure plan cases
    (plan_test.go), scan_golden unchanged, partial failure
    (import_failure_test.go), watcher add/rename/delete ordering with identity
    preserved (watcher_order_test.go), plus the existing cancellation, rename,
    sidecar, `.ignore` and identity tests.
  - Checks: race tests for the scanner, the full CI mirror, and the TS rollback
    check (TS reopens the Go database: progress, bookmarks and 8 sessions
    match).
- **Remaining risks (they predate S2 and are pinned by tests, not changed):**
  - A failed item import aborts the whole scan. Items already committed stay
    committed, but their buffered items_added/items_updated events are
    dropped, and lastScan is not stamped. A retry succeeds.
  - The watcher matches a directory created after a delete by inode, so an
    inode reused by the filesystem can attach the new directory to the deleted
    item's id.
  - Typed audio files (arch-scanner §6.4) and a single ffprobe implementation
    (§6.5) are still open.

### M10: orphan cleanup safety review (itemwrite/sweep.go)

Defects found and fixed:
- **Lost links under concurrency (data loss).** The sweep selected orphan
  authors/series and then deleted them by id without re-checking. A request
  that linked a book in between had that new link removed by
  `bookAuthors`/`bookSeries` `ON DELETE CASCADE`. Now one
  `DELETE … WHERE … NOT EXISTS(…) RETURNING`. TestSweepNeverDropsAConcurrentLink
  (1,500 races, one-connection pool as in production) fails 5/5 on the old code
  and passes on the new.
- **Double announcements.** Overlapping sweeps could both emit `author_removed`
  / `series_removed` and edit filter data for one removal. Only rows this
  statement deleted are announced now (TestOverlappingSweepsAnnounceOnce).
- **Silent failures.** Query, scan, `rows.Err` and feed-close errors were
  dropped. They are logged now. A NULL `libraryId` no longer drops the row from
  the sweep.
- **Cancellation.** The sweep runs after the edit committed, so it now ignores
  the request's cancellation. A disconnecting client no longer leaves orphans
  (TestSweepIgnoresRequestCancellation).

### M11 (PERM2) notes

- Permissions are decoded once per request. The runtime calls
  `access.WithRequest` after authentication, and handlers call `access.For`,
  which reuses the snapshot only for the request's own user (same id, type,
  isActive and permission bytes) and decodes any other user's row. Nothing is
  cached across requests. Decodes per request: library items 3 → 1, item read
  2 → 1, personalized page 3 → 1 (`TestRequestDecodesPermissionsOnce`).
- **Realtime revocation, a deliberate deviation from TS (fail closed).** TS
  keeps the user object a socket authenticated with (`SocketAuthority.js:104-125`).
  `user_updated` goes only to the acting admin (`UserController.js:347`), and
  `user_removed` goes only to admins (`:402`), so an established socket keeps
  receiving item events after its access is removed. Go refreshes a
  connection's user and grants after an admin PATCH of the user, so access is
  removed and restored. A deactivated or deleted user's sockets are closed.
  Tests: `TestSocketFollowsUserUpdates` (library and tag removal/restoration,
  deactivation) and `TestSocketClosesWhenUserDeleted`. Both fail with the
  refresh disabled.
- The refresh callback is a `userapi.New` argument (no new Set* injector).
