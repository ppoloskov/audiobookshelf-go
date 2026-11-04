# Roadmap

Updated 2026-11-05. **Owner authorized implementation with cheap workers,
review after completed batches, and pushing ready features to the fork.**
No default-branch merge, deployment, new library or route-removal approval
is implied. Record actual completed cards and tests; do not mark plans done. [ARCHITECTURE.md](ARCHITECTURE.md) owns permanent
contracts; [AGENTS.md](AGENTS.md) owns worker/check policy.

## Direction and current evidence

Preserve SQLite/persisted meaning and real client protocols. Simplify the Go
implementation between them. Prioritize client risk and measured cost, then
remove duplicate responsibilities. sqlc is query safety, not the principal
line-saving strategy. The former 30% target is aspirational, with no proven
13k-line deletion inventory. Package moves are not savings by themselves.

At takeover HEAD `48c10a6a`, recorded baseline: 44,586 handwritten Go lines,
10,851 complexity, 178 functions over 15, 993 map uses, 9 legacy routers,
18 setters. These are baseline-file values, not a new measurement. Supplied
review reports L5 −58 and L6 −4 lines; not independently remeasured here.

Completed: F1–F9 (including F5b/F8 extensions), L1–L7, S2, C5, C24–C26, V1.
These preserve historical completion claims, not a fresh all-tests-pass claim.
The 201-route figure means the recorded web/log/trial route set is reached,
not that mobile behavior is verified. Inventory still has one unmatched
pattern and nine unresolved dynamic call sites. Real-device O4 remains open.
V1 made parity advisory; image CI gates on `verify` and `rollback`.

Historical task detail/acceptance and earlier owner decisions are preserved
in [the pre-takeover roadmap](archive/reviews/2026-11-04-roadmap-before-takeover.md).
The [audit brief](archive/reviews/2026-11-04-mobile-audit-brief.md) specifies
A–G deliverables; the [review](archive/reviews/2026-11-04-simplification-review.md)
is input evidence, not an automatic statement of current runtime behavior.

## Execution conventions

- Follow dependencies, not old alphabetical phases. One bounded task per
  worker; split L tasks further if their write scopes become broad.
- Expensive architect plans/reviews. Cheap workers research/code/test within
  written scopes. Each accepted packet updates this queue and handover.
- State: planned / in progress / awaiting review / accepted / blocked. Record
  actual commands, exits/skips, evidence, measured benefit and next action.
  A written test plan is not a passing test.
- All tests listed below are **proposed unless marked existing**. Commands
  using new test names become meaningful only after those tests are added;
  verify they executed (no empty regex match or skipped integration).
- Keep fixed contracts, read-only upstream and unrelated dirty work in every
  packet. No dependency, removal, publication or live cutover approval implied.
- Finish with applicable AGENTS checks. Metrics are review signals; current
  tool enforcement stays strict until Q1. Node-tool deletion does not reduce
  the handwritten-Go metric.

## 1. Compatibility evidence — alongside domain migration

### MC-A · Commit-pinned client audit (P0, M; documentation)

Intent: establish actual obligations before changing protocols or deleting
routes. Scope: `contracts/mobile-client-audit.md`, gap/test plans, route/event
decision evidence; six clients named in the audit brief. No runtime edits.
Prerequisite: public source access; pin the default-branch SHA and retrieval
time for each. Distinguish confirmed/inferred/not found/not investigated.

Acceptance: each verified client × operation has a SHA permalink, headers/
payload/transport expectations, Go path, named test or MISSING, evidence
status and priority; all six review statuses and protocol matrix are explicit.
Unknowns produce investigation tasks, never unsupported-feature decisions.
Check every evidence link and source span; review auth, WS-only, progress,
range, cache, HLS/offline, cover format and version branches. Do not call a
partial inspection an exhaustive operation inventory. Done when A/C/D/G of
the brief are complete or precisely marked open with executable follow-ups.
Approval: audit authorized now; follow-up source research stays read-only.

### MC-B / R1 · Reproducible inventory and route decisions (P0, M)

Intent: prevent demonstrated mobile endpoints being removed as “unused”.
Scope: `tools/clientusage/main.go`, its tests, a checked external-evidence
manifest under `contracts/`, generated `contracts/client-usage.json` and a
route/event decision table. Prerequisite: the relevant confirmed MC-A evidence, with remaining audit
unknowns carried forward. No route deletion.
Smallest proposed change: merge pinned external operation records into the
existing generator with provenance; keep unresolved call sites visible and
classify actual Go registrations as well as upstream inventory entries.

Acceptance: `GET /api/me/progress`, `/api/playlists`, `/api/collections` are
protected, as is source-confirmed iOS `POST /api/session/local-all`;
every route gets exactly one KEEP /
SIMPLIFY / REMOVE CANDIDATE / INVESTIGATE with evidence and approval status.
Do not hand-edit generated JSON. Proposed `TestExternalClientEvidence` and
`TestInventoryDeterministic` in `tools/clientusage/main_test.go`; run
`cd go-backend && go test ./tools/clientusage`, then root `make client-usage`
twice and compare outputs. Use existing `TestOutputIsCurrent` as the non-writing
freshness check; no new check flag is needed. Done when external evidence is reproducible,
all dynamic unknowns remain visible, and no web-only absence implies removal.
Approval: queue approval to implement; per-route R2 approval remains separate.

### MC-C · Socket.IO contract and resource tests (P0, M)

Intent: protect auth/live progress and bound the in-repo protocol maintenance
risk. Scope: `internal/realtime/{eio,sio,hub}.go`, tests,
`internal/server/socket_connect_test.go`, CI interop setup only as needed.
Prerequisite: relevant MC-A source rows; reproduce suspected differences first.
Keep Engine.IO v4, default namespace and both legacy/prefixed paths; do not
replace the subsystem or add polling/binary support without demonstrated need.

Acceptance: use an actual Socket.IO v4 library to test auth→init, invalid and
expired token expectations (including `invalid_token` vs `auth_failed`),
re-authentication, reconnect, recipients and acknowledgements on both paths.
Existing `TestSocketIO4WebsocketAuthAndPrefixedPath` skips without the JS
library: ensure it executes in required CI, not just in advisory parity.
Test malformed frames, the actual library frame cap and fragmented-message
limits against client payload needs, heartbeat, queue pressure and cleanup.
The advertised Engine.IO `maxPayload` alone does not specify a WS frame cap. Proposed `TestInboundPayloadLimit`,
`TestSocketReauthentication`, `TestSocketReconnectDelivery` in realtime.
Commands from `go-backend`: `go test -v ./internal/realtime ./internal/server
-run 'Socket|Packet|Ping|Buffer|Payload|HubClose'` with the pinned JS client
available, then `CGO_ENABLED=1 go test -race ./internal/realtime` and full
gates. Done when required cases execute and any reproduced gap has a narrow
regression/fix; no claim of unknown transports supported. Size excludes a
new transport implementation. Approval: queue; approve provisioning the existing JS `socket.io-client@4.8.1`
probe in required verify CI (currently installed only in advisory parity).
The historical Go v3 test-client approval is a different dependency.

### MC-D · REST/media/offline value regressions (P0, M)

Intent: test client-required behavior beyond structural shapes. Scope:
`internal/contracttest/`, focused tests beside `catalog`, `playback`,
`userapi`, `httputil`; runtime fixes only for reproduced failures.
Prerequisite: relevant MC-A rows. Preserve headers' semantics, playback and
persisted state; no Express byte formatting requirement or artwork conversion.

Acceptance: seeded-user tests assert ETag conditional GET→304 with empty body;
byte/suffix/invalid/unsatisfiable ranges with 206/416, exact bytes and
Content-Range; direct audio and HLS play/session URLs and MIME types;
`/api/session/local` AND `/api/session/local-all`, then query progress/session
values and reopen persistence; representative decoder values/ordering/ACLs;
actual cover parameters and decodable format while retaining original art.
Proposed `mobile_protocol_test.go`: `TestMobileConditionalGET`,
`TestMobileMediaRanges`, `TestMobilePlayback`, `TestMobileOfflinePersistence`,
`TestMobileCoverParameters`. Run `cd go-backend && go test -v
./internal/contracttest -run 'TestMobile'`, affected package tests and full
checks. Done when all source-confirmed P0/P1 behaviors have meaningful passing
assertions or a separately approved, explicit unresolved blocker.
Approval: queue; contract/persistence changes beyond repair remain prohibited.

### MC-E / O4 · Device acceptance (P0, S; owner/device time)

Prerequisites: MC-C/D passing for relevant cases; preserve copy-only isolation.
Scope: `contracts/mobile-client-audit.md` and handover evidence, no live swap
by a worker. Owner tests official iOS/Android plus affected third-party apps
on a disposable initialized install: existing token login/refresh, browse,
seek, download, offline sync, reconnect and progress on a second client.
Record app/server versions, platform, procedure, results and safe logs.
Done when explicit results support the declared client scope; source and
simulator tests cannot substitute. Approval: owner arranges device access and
any live cutover separately. Blocks R3 and final retirement of diagnostics.

## 2. Domain migration and measured simplification

### Dispatch policy: small cards, not whole epics

Owner follow-up, 2026-11-04: explicitly include the **books domain** and make
work concrete for cheap models. Implementation is now authorized; the preceding request only planned these cards. S13/P2/P3 and S11 below are epics,
not single worker assignments. Compatibility MC tasks remain necessary for
the operations they affect; do not wait for the entire six-client census
before a behavior-preserving book extraction with relevant passing tests.

Each card below has an explicit completion checkbox and status; pending cards are size S (one narrow result, normally at most
four production files plus tests). Paths are relative to go-backend. New paths
and tests are marked `new`. A dependency means implemented/tested predecessor code, not merely a plan.
Cross-worker reuse requires reviewed integration; an explicitly authorized
single-worker feature batch may validate sequential cards before its final
review. Do not dispatch blocked cards.

Worker gets only: common rules + its card + named source/test snippets. Do not
load the entire roadmap, all skills or previous chats. Preserve SQLite/JSON/
protocol meaning, row closure, owner work and immutable goldens. No dependencies,
schema/route/auth changes, unrelated SQL conversions, commits or baseline edits.
If scope expands beyond the card, return the blocker to the architect; do not
invent an abstraction. Report changed paths, exact test results/skips, measured
query/complexity change if applicable, and remaining issue in at most 12 lines.

Each card's command below is the targeted worker check, not the whole release
gate. Before accepting an integration wave run applicable AGENTS build/vet/full
tests/lint/ratchet checks once on the integrated tree; record card results and
wave validation separately. Never call a card fully verified before its gates.
The architect owns architecture/documentation integration. Per the latest
owner instruction, a worker may complete sequential cards as one coherent
feature batch (B02–B07), running card checks as it goes; final code review is
once the batch is ready. Avoid creating and deleting temporary APIs merely to
publish intermediate cards. Worker completion means awaiting review, not push.

#### Current execution status — book reads DONE; batching in progress

Feature worktree `/private/tmp/abs-books-domain-20261104`, branch
`codex/books-domain-batching`, base `48c10a6a`; delivery is to this fork branch,
not default/master. Documentation baseline is `d64aa599`.

| Cards | State / actual implementation |
|---|---|
| B01 | DONE (`d2ec8d9`): independent catalog value assertions and unchanged source goldens |
| B02–B07 | DONE (`d2ec8d9`) as one coherent wave: `books.Load(ctx, db, id)` and `Book.Full/Minified/Expanded`; no transitional metadata exports or production catalog builders |
| B08/B09 | DONE (`d2ec8d9`): shared persisted JSON normalization; deleted stateless DB wrapper and redundant numeric conversion |
| I01 | DONE (`d2ec8d9`): test-only SQL query counter, direct + prepared execution verified |
| I02 | IN PROGRESS: batched book reads; no caller migration yet |
| I03–I06, lists | PLANNED; no item row or list migration claimed complete |

Actual metrics versus 48c10a6a: **44,585 production Go lines (−1)**,
**complexity 10,840 (−11)**, **991 dynamic maps (−2)**. This is a boundary and
duplication improvement with essentially flat source size, not a large deletion.
Ratchet baselines were only lowered; no guard was weakened. No performance or
constant-query-count improvement is claimed before I02–I05.

Build, vet, dependency lint (0 issues), dbnest, ratchet and Linux/arm64
CGO-disabled build PASS. Final full `go test ./...` exits 1 for the exact five
pre-existing native FFmpeg/libwebp TestJourney cases, with no new failures;
affected package tests and persisted JSON tables pass. Feature scope reviewed;
**full release acceptance remains incomplete** on this host. No encoder shim,
test deletion or blanket baseline raise was used. Details: handover.

Next implementation card: **I02**, now that I01 is available. Use plain
`books.LoadMany(ctx, db, ids)`; do not reintroduce a Reader object. Full mobile
research continues separately. Route removal/device/cutover decisions unchanged.

#### First milestone: explicit book reads + book-page batching (S13/P2/P3)

Completed read spine: `B01–B09` plus independent `I01`. Next dependency spine:
`I02 -> I03 -> I04 -> I05 -> I06`. Only I02 is currently dispatchable there.
Book metadata/chapter writes and complete mixed-media migration are not claimed
finished by this milestone. All route removals and real-device gates stay intact.

##### B01 · Pin book projection values

- [x] **DONE** — implemented, architect-reviewed and pushed in `d2ec8d9` (2026-11-04). Scoped checks passed; the five pre-existing WebP full-suite failures remain documented.

- **Depends:** none. **Write only:** Tests only: `internal/catalog/handler_test.go`, `item_golden_test.go`.
- **Do:** Add `TestBookProjectionValues` using existing `catalogFixture`/golden fixture. Assert missing vs empty descriptions, [] relation arrays, null series sequence, track/item IDs, excluded-file counts and source audio order for all three book views. Existing `TestBookItemSerializersMatchPinnedSourceGolden` and `TestItemGolden` stay independent; do not regenerate goldens.
- **Check (from go-backend):** `go test ./internal/catalog -run 'TestBookProjectionValues|TestBookItemSerializersMatchPinnedSourceGolden|TestItemGolden' -count=1`.
- **Done:** Named assertions execute on current code; record observed baseline. No production edits; a discovered failure becomes a separate reproducer/fix card.

##### B02 · Move single-book reads into books

- [x] **DONE** — implemented, architect-reviewed and pushed in `d2ec8d9` (2026-11-04). Scoped checks passed; the five pre-existing WebP full-suite failures remain documented.

- **Depends:** B01. **Write only:** `internal/books/reader.go`; reader integration tests in `internal/catalog/book_reader_test.go`; `internal/catalog/book_wire.go`, `handler.go`, related `handler_test.go`.
- **Do:** Implement plain `books.Load(ctx, db, bookID)` returning typed `books.Book` data; B09 removed the initially proposed stateless Reader wrapper. Move loadBookRow/loadBookSource and bookRelations SQL/decoding into the books.Load function. Return typed author/series slices ordered by existing association createdAt ASC, preserving nullable sequence. Catalog calls Load; it no longer performs these book reads or converts relation maps back into wire types. Keep projection implementation and existing wire declarations in place. Do not introduce a callback from books to catalog.
- **Check (from go-backend):** `go test ./internal/books ./internal/catalog -run 'TestLoadBookNullsAndRelationOrder|TestBookProjectionValues|TestBookItemSerializersMatchPinnedSourceGolden|TestItemGolden' -count=1`.
- **Done:** New TestLoadBookNullsAndRelationOrder exercises real SQLite nulls/order/not-found. Remove old scans and bookRelations implementation, adapt its test without making the expected result call the new implementation. All Rows close before the next query.

##### B03 · Give book metadata projection one owner

- [x] **DONE** — implemented, architect-reviewed and pushed in `d2ec8d9` (2026-11-04). Scoped checks passed; the five pre-existing WebP full-suite failures remain documented.

- **Depends:** B02. **Write only:** `internal/books/metadata.go`; shared catalog value tests; `internal/catalog/book_wire.go`.
- **Do:** Move fullMetadata/minifiedMetadata/expandedMetadata, authorNames/seriesName/descriptionPlain to books using Book data. Title-ignore-prefix is passed as a precomputed nullable value; retain catalog titlePrefix/settings ownership. The completed wave calls private metadata helpers from books projections directly; no intermediate exported adapters were published. No new settings cache, prefix policy or people-domain dependency.
- **Check (from go-backend):** `go test ./internal/books ./internal/catalog -run 'TestLoadBookNullsAndRelationOrder|TestBookProjectionValues|TestBookItemSerializersMatchPinnedSourceGolden|TestItemGolden' -count=1`.
- **Done:** Shared TestBookProjectionValues checks nil/blank descriptions, author/series order and sequence, title prefix and plain description. Delete duplicate helpers in catalog; all metadata helpers are private after B07.

##### B04 · Move full book media projection

- [x] **DONE** — implemented, architect-reviewed and pushed in `d2ec8d9` (2026-11-04). Scoped checks passed; the five pre-existing WebP full-suite failures remain documented.

- **Depends:** B03. **Write only:** `internal/books/projection.go`; shared catalog value tests; `internal/catalog/book_wire.go`.
- **Do:** Move bookFull assembly to Book.Full(itemID), returning existing wire.BookFull. Move bookEbook/encodeJSON only with their consumers; catalog can forward to the single owner until B07. Keep opaque nested source JSON behavior and concrete wire value inside outer item map.
- **Check (from go-backend):** `go test ./internal/books ./internal/catalog -run 'TestLoadBookNullsAndRelationOrder|TestBookProjectionValues|TestBookItemSerializersMatchPinnedSourceGolden|TestItemGolden' -count=1`.
- **Done:** TestBookProjectionValues checks decoded fields/null/arrays and libraryItemId. Old Handler.bookFull may be forwarding-only until B07, with no duplicate assembly.

##### B05 · Move minified book media projection

- [x] **DONE** — implemented, architect-reviewed and pushed in `d2ec8d9` (2026-11-04). Scoped checks passed; the five pre-existing WebP full-suite failures remain documented.

- **Depends:** B04. **Write only:** `internal/books/projection.go`; shared catalog value tests; `internal/catalog/book_wire.go`, `handler.go` only for book-specific count/size helpers.
- **Do:** Move bookMinified assembly to Book.Minified(titlePrefix). Move includedAudioCount/audioSize into books when their production callers are book-only; keep test oracle calculations independent. Catalog computes title prefix as before and delegates. Do not change settings queries yet.
- **Check (from go-backend):** `go test ./internal/books ./internal/catalog -run 'TestLoadBookNullsAndRelationOrder|TestBookProjectionValues|TestBookItemSerializersMatchPinnedSourceGolden|TestItemGolden' -count=1`.
- **Done:** TestBookProjectionValues covers track count vs files, chapters, ebook size/format, null vs zero duration. No duplicate minified implementation; forwarding adapter ends in B07.

##### B06 · Move expanded book media projection

- [x] **DONE** — implemented, architect-reviewed and pushed in `d2ec8d9` (2026-11-04). Scoped checks passed; the five pre-existing WebP full-suite failures remain documented.

- **Depends:** B05. **Write only:** `internal/books/projection.go`; shared catalog value tests; `internal/catalog/book_wire.go`, `handler.go` for makeTracks only.
- **Do:** Move bookExpanded to Book.Expanded(itemID, titlePrefix), including ordered track URL/offset construction. Move makeTracks if book-only; leave number/fileType helpers shared by podcast paths alone. Keep wire.BookExpanded, existing route URLs, units and RawMessage behavior.
- **Check (from go-backend):** `go test ./internal/books ./internal/catalog -run 'TestLoadBookNullsAndRelationOrder|TestBookProjectionValues|TestBookItemSerializersMatchPinnedSourceGolden|TestItemGolden' -count=1`.
- **Done:** TestBookProjectionValues uses hand-calculated offsets/order/item IDs and skipped-file case. Catalog has no expanded assembly beyond forwarding; no playback/session changes.

##### B07 · Remove book migration adapters

- [x] **DONE** — implemented, architect-reviewed and pushed in `d2ec8d9` (2026-11-04). Scoped checks passed; the five pre-existing WebP full-suite failures remain documented.

- **Depends:** B06. **Write only:** `internal/catalog/book_wire.go`, `handler.go`, `batch_chapters.go`; `internal/books/metadata.go` visibility only.
- **Do:** Call the new Book projection methods directly from ItemJSON/ItemMinifiedJSON/expandedBookItem. Delete obsolete forwarding builders/types/helpers; retain stringPointerValue in its actual remaining owner (batch_chapters) if still needed. Make metadata helpers private when catalog no longer calls them. Keep HTTP orchestration in catalog for now.
- **Check (from go-backend):** `go test ./internal/books ./internal/catalog ./internal/items -count=1`.
- **Done:** rg confirms no catalog loadBookRow/loadBookSource/bookRelations or duplicate bookFull/bookMinified/bookExpanded assembly; books imports no catalog/items/server. Book read/projection extraction complete, book writes explicitly still unmigrated.

##### B08 · Share persisted JSON normalization (review follow-up)

- [x] **DONE** — implemented, architect-reviewed and pushed in `d2ec8d9` (2026-11-04). Scoped checks passed; the five pre-existing WebP full-suite failures remain documented.

- **Depends:** B07. **Write only:** `internal/store/json.go`, `json_test.go`;
  catalog decoder definitions in `handler.go`; book reader decoder calls.
- **Do:** one lenient persisted-JSON slice decoder (strings + object arrays)
  and one object decoder, reused across catalog and books. Preserve non-null
  empty containers, whitespace/null and partial type-error semantics. These
  helpers are not HTTP request validators; book empty-ebook normalization stays.
- **Check:** `go test ./internal/store ./internal/catalog ./internal/books -run 'JSON|BookProjection|LoadBook|ItemGolden|BookItemSerializers' -count=1`.
- **Done:** duplicate decode bodies removed, behavior tables pass, measured
  code/complexity recorded. Completed and reviewed with the book feature.

##### B09 · Remove stateless read wrapper (review follow-up)

- [x] **DONE** — implemented, architect-reviewed and pushed in `d2ec8d9` (2026-11-04). Scoped checks passed; the five pre-existing WebP full-suite failures remain documented.

- **Depends:** B08. **Write only:** books reader/metadata/projection files;
  their catalog call sites and tests.
- **Do:** use `books.Load(ctx, db, id)` rather than a Reader object duplicating
  the DB handle already in catalog. Delete the wrapper/constructor/extra field.
  Reuse JSON numeric conversion and keep name projection helpers in metadata.
- **Check:** the same focused tests as B08, dependency lint and `make ratchet`.
- **Done:** one explicit DB dependency, no wrapper or duplicate numeric helper;
  no source packing/baseline raises to claim reduction. Completed.

##### I01 · Add test-only SQL query counter

- [x] **DONE** — implemented, architect-reviewed and pushed in `d2ec8d9` (2026-11-04). Scoped checks passed; the five pre-existing WebP full-suite failures remain documented.

- **Depends:** B07. **Write only:** `internal/catalog/query_count_test.go` (new), fixture setup in `handler_test.go` only if needed.
- **Do:** Use a test-only database/sql/driver wrapper around the installed modernc SQLite driver, forwarding supported context/prepare/transaction interfaces. There is no Store query hook today: do not add a production observer or repository interface. Reset count after seeding; count direct/prepared reads consistently. Add TestQueryCounterIncludesPreparedReads to prove the counter sees both.
- **Check (from go-backend):** `go test ./internal/catalog -run 'TestQueryCounterIncludesPreparedReads|TestBookProjectionValues' -count=1`.
- **Done:** Actual SQL reads are counted and instrumentation does not change results/connection behavior. Record current 10/50-book page counts as baseline, not a fixed-count PASS. No runtime instrumentation.

##### I02 · Batch books and their relations

- [ ] **IN PROGRESS** — cheap worker implementing batched book reads (2026-11-05).

- **Depends:** I01. **Write only:** `internal/books/batch.go`, `batch_test.go` (new); `reader.go` shares scans only.
- **Do:** Add books.LoadMany(ctx, db, bookIDs) -> map[string]Book using book rows + author links + series links, one query per kind per bounded chunk (250 unique IDs maximum per chunk). Reuse Load row decoding; implement books.Load via books.LoadMany if that removes duplication while retaining sql.ErrNoRows. Deduplicate lookup IDs, retain per-book createdAt ordering, close each Rows before the next query; no SQL per book.
- **Check (from go-backend):** `go test ./internal/books -run 'TestLoadManyBookValues|TestLoadManyBookChunkBoundary|TestLoadBookNullsAndRelationOrder' -count=1`.
- **Done:** Tests compare independent known multi-book/null/order values and 0/1/250/251 IDs, missing IDs absent from map, canceled context propagated. No table/schema changes; query-count assertion comes through I04 integration counter.

##### I03 · Read title-prefix settings once per book batch

- [ ] **PLANNED** — not completed; follow dependencies and acceptance below.

- **Depends:** I02. **Write only:** `internal/catalog/book_batch.go`, `book_batch_test.go` (new); `handler.go` titlePrefix helper extraction only.
- **Do:** Add a catalog-local batch assembly helper for already-loaded book item envelopes/media IDs. Load sortingPrefixes once, call books.LoadMany with bounded chunks, and project full/minified/expanded values in the caller input order. Share existing catalogTitlePrefixAtEnd logic; preserve default the/a and empty/null title behavior. No cross-request cache or podcast changes.
- **Check (from go-backend):** `go test ./internal/catalog -run 'TestBatchBookProjectionValues|TestBookProjectionValues|TestItemGolden' -count=1`.
- **Done:** TestBatchBookProjectionValues proves order/duplicates, each media view, custom/default prefixes and missing-book error behavior matching single reads. The helper only assembles; it does not add an HTTP route or change item selection/ACL.

##### I04 · Batch the ordinary book library page

- [ ] **PLANNED** — not completed; follow dependencies and acceptance below.

- **Depends:** I03. **Write only:** `internal/catalog/handler.go:libraryItems`, `query_count_test.go`, `handler_test.go`.
- **Do:** After collecting and closing libraryItems Rows, assemble book media through I03 instead of calling each book serializer. Scope ordinary noncollapsed book page, no includes first; leave podcast/collapsed/include branches unchanged and name their follow-up. Keep SQL filters, total, pagination, selected IDs, ACL and ordering unchanged.
- **Check (from go-backend):** `go test ./internal/catalog -run 'TestBookPageQueryCountIndependentOfPageSize|TestBookProgressFiltersAndSortUseCurrentUser|TestBookProjectionValues|TestItemGolden' -count=1`.
- **Done:** New TestBookPageQueryCountIndependentOfPageSize checks 10/50 books have equal read count with identical options, and correct IDs/order/values. Full vs minified query variants each tested. Counter validates actual endpoint path, not just loader.

##### I05 · Batch book shelf hydration

- [ ] **PLANNED** — not completed; follow dependencies and acceptance below.

- **Depends:** I04. **Write only:** `internal/catalog/personalized.go:personalizedItems`, `query_count_test.go`, `handler_test.go`.
- **Do:** Collect all IDs, check Rows.Err and explicitly close Rows before hydration. For book shelves load common libraryItems envelopes in one query per chunk, then use I03. Preserve shelf order, per-user progress/ACL and error behavior. Podcast hydration and per-item includes stay explicit follow-ups, never label all shelves batched.
- **Check (from go-backend):** `go test ./internal/catalog -run 'TestBookShelfQueryCountIndependentOfSize|TestPersonalizedBookShelvesUsePerUserProgress|TestItemGolden' -count=1`.
- **Done:** New TestBookShelfQueryCountIndependentOfSize compares 10/50 book IDs without includes and asserts same counts/correct user progress/order; no per-ID ItemMinifiedJSON on the migrated path. No nested pool reads with open Rows.

##### I06 · Extract common item row decoding

- [ ] **PLANNED** — not completed; follow dependencies and acceptance below.

- **Depends:** I05. **Write only:** `internal/items/row.go`, `row_test.go` (new); `internal/catalog/handler.go:scanItemRow/loadItem`.
- **Do:** Define typed items.Record and common row decode/ID batch load; keep books/podcast-specific projection outside the row type. Move generic field/time/file normalization from scanItemRow using existing dbtime parser, and have catalog adapt the record to the established outer JSON map. Reuse I05 envelope batch SQL. Do not expand items.Reader or migrate every consumer in this card.
- **Check (from go-backend):** `go test ./internal/items ./internal/catalog -run 'TestItemRowValues|TestBookProjectionValues|TestPodcastItemDetailSerializesEpisodeAndTrack|TestItemGolden' -count=1`.
- **Done:** New TestItemRowValues asserts ID/mediaID separation, dbtime milliseconds, optional scan/size fields, file classification and malformed date failure. One row decoder; wire map adapters remain explicitly until mixed-media contracts are migrated.

#### Next domain milestone: lists (S11), not one mass move

These cards start after the book-read milestone and relevant list contract
baselines. They preserve current podcast list behavior; book-only batching
never justifies filtering out podcast entries. S11 completes only after the
final wiring/removal card and integration gates. Later domains get equally
small cards only when this milestone is reached; do not prepay an exhaustive
implementation plan for untouched packages.

##### L01 · Pin list reads/permissions

- [ ] **PLANNED** — not completed; follow dependencies and acceptance below.

- **Depends:** I06. **Write only:** Tests only: `internal/userapi/userapi_test.go`, `playlist_collection_regression_test.go`, `playlist_order_test.go`.
- **Do:** Add TestListReadValuesAndPermissions for GET /api/collections, /api/playlists and detail using owner/other-user/no-library cases, real IDs/order/null description, book + podcast episode entries. Reuse existing fixture and keep existing playlist/order/regression tests.
- **Check:** `go test ./internal/userapi -run 'TestListReadValuesAndPermissions|TestPlaylistCreateListAndOwnerRestriction|TestPlaylistListsSortByLocaleCompare|TestCreatePlaylistFromCollectionPersistsExpandedBookItems' -count=1`.
- **Done:** Independent semantic/ACL baseline exists before ownership changes; no production edits.

##### L02 · Extract playlist read data

- [ ] **PLANNED** — not completed; follow dependencies and acceptance below.

- **Depends:** L01. **Write only:** `internal/lists/reader.go`, `reader_test.go` (new); `internal/userapi/playlist.go`.
- **Do:** Move GetPlaylist/ListPlaylistMediaItems use and playlist metadata/entry read assembly from ownedPlaylist/playlistJSON into a concrete lists.Reader. Keep existing item-reader dependency passed to the constructor and existing response map as a temporary boundary; do not move writes/routes or change orphan/error handling in this card. Lists never imports userapi/catalog.
- **Check:** `go test ./internal/lists ./internal/userapi -run 'TestPlaylistReadValues|TestListReadValuesAndPermissions|TestPlaylistListsSortByLocaleCompare|TestCreatePlaylistFromCollectionPersistsExpandedBookItems' -count=1`.
- **Done:** New TestPlaylistReadValues covers book/episode entries/order/nulls; no duplicate read implementation. Userapi retains a forwarding adapter until L05.

##### L03 · Extract collection read data

- [ ] **PLANNED** — not completed; follow dependencies and acceptance below.

- **Depends:** L02. **Write only:** `internal/lists/collections.go`, `reader_test.go` (new/existing); collection read functions in `internal/userapi/userapi.go`, `library_lists.go`.
- **Do:** Move collection SELECT and response assembly only, from collectionRoute/collectionDTO (userapi.go) and libraryCollections (library_lists.go); keep HTTP/writes where they are. Pass a small constructor callback `func(context.Context, store.User, string) (bool, error)` for existing per-item visibility; userapi adapts canAccessItem without importing userapi from lists. Keep filtering semantics in the same call order. Reuse list Reader and existing item serialization. No giant userapi rewrite or import from lists back to userapi.
- **Check:** `go test ./internal/lists ./internal/userapi -run 'TestCollectionReadValues|TestListReadValuesAndPermissions|TestCollectionDescriptionDBValueMatchesLegacyNullCoercion|TestCreatePlaylistFromCollectionPersistsExpandedBookItems' -count=1`.
- **Done:** New TestCollectionReadValues asserts list/detail/order/library visibility and values; old duplicate reads removed, forwarding endpoints preserved. If extraction needs >4 production files, return exact dependency list for split.

##### L04 · Plan list write seams from actual callers

- [ ] **PLANNED** — not completed; follow dependencies and acceptance below.

- **Depends:** L03. **Write only:** Documentation only: update this ROADMAP card into individually scoped write/route cards; read `userapi/playlist.go`, collection paths in `userapi.go`, `collection_feed.go` and runtime wiring.
- **Do:** Produce separate cards for playlist create/update, membership mutation, collection create/update, collection membership, delete/cleanup/feed, route mounting. Each must name actual functions/deps and a persisted-state/event regression. This is bounded deferred decomposition, not authorization for a worker to improvise all writes.
- **Check:** `git diff --check`.
- **Done:** All newly dispatchable write cards have exact files/commands/deps and no domain cycles. Architect review required; no claim S11 is complete.

##### L05 · Lists route wiring/removal — blocked template

- [ ] **PLANNED** — not completed; follow dependencies and acceptance below.

- **Depends:** all concrete write/route cards created by L04. **Write only:** BLOCKED until L04 fills exact lists handler/constructor and `internal/server/runtime.go` scopes.
- **Do:** Architect must replace this template with small route-family cards and a final removal card after read/write ownership is complete. Do not dispatch this heading to a simple model.
- **Check:** `not runnable until replacement cards are reviewed`.
- **Done:** Every list route uses lists owner, userapi forwarding/duplicate code removed, mixed-media values/ACL/persistence/events pass and route generator is refreshed. No route deletion implied.

#### Scope still open after these milestones

S13/P2/P3 are not marked complete until podcast projections, mixed-media
batching, includes/collapsed paths and each remaining playlist/progress/playback/
scanner consumer are migrated with their own tests. Book metadata/chapter
writes need separate state/event contracts. No worker receives “finish items”
or “move all catalog” as a task. Architect decomposes only the next slice.
Domain order after books/items reads and lists: people (S10), progress (S12),
then library/items HTTP and remaining users/auth/admin (S14/S15). Relevant
compatibility tests are prerequisites per slice, not a global audit freeze.

### L8a–c · Selective domain-owned sqlc (P2, S each)

Replaces L8's arbitrary “under 100 raw call sites” gate. Prerequisite: choose
queries actually touched by the accepted loader/domain packet; MC-D for its
outputs. L8a catalog static reads (`queries/catalog.sql`, catalog); L8b scanner
static writes/reads (new `queries/scanner.sql`, scanner); L8c podcast static
queries (new `queries/podcasts.sql`, podcasts). Dynamic filter/sort SQL stays
handwritten. No schema, DTO or lifecycle redesign in these tasks.
Acceptance per slice: named old query/scans removed, typed generated query
used, equivalent NULL/time/order/error semantics tested. Root `make sqlc`,
check tracked + untracked generated files, affected package/persistence tests
and full gates. Done when selected queries and measured delta are recorded;
no conversion quota or invented savings. Defer slices with no clear benefit.
Approval: queue; sqlc itself already approved.

### R2/R3 · Owner-approved behavior removal (P2, S per approved group)

Prerequisites: MC-B complete decision table, MC-E and relevant regressions;
no unresolved dynamic callers for the candidate. R2 records owner KEEP/REMOVE
per operation. R3 changes only explicitly approved handlers/registrations and
obsolete tests, then proves 404 where approved and remaining clients green.
Exact files/tests must be filled from MC-B **before dispatch**; do not give a
worker the whole inventory to delete. Commands: affected package tests,
`make client-usage`, contract suite, full gates. Done when each deletion has
approval, evidence and measured removed implementation. Approval: individual
removal required; no candidates approved by this takeover.

### S9 · Simplify framing, keep HTTP semantics (P2, S)

Prerequisites: MC-B/D, affected cache/range/cover tests. Scope:
`internal/httputil/` Express-only ordering/escaping/framing options and callers.
No removal of ETag/If-None-Match/304, Range/206/416, cache behavior or required
MIME negotiation. First name each unnecessary option and prove no client
requirement; remove it and tests of byte-only details. Run
`cd go-backend && go test ./internal/httputil ./internal/contracttest` and full
gates. Done when supported HTTP semantics pass and obsolete paths disappear.
Approval: queue; any externally observable semantic removal goes through R2.

### V2a/V2b · Extract reusable tooling, then retire diagnostics (P2, M/S)

V2a scope: `tools/contractfixture/build.cjs`, `tools/clientusage/`, route
inventory producer, shared seed/args modules, shape/benchmark/browser tooling,
Makefile and CI rollback imports. Prerequisite: MC-B design; not L8 completion.
Keep rollback operational. Inventory all inbound dependencies before deletion.
Extract fixture seed and route evidence into standalone owners and prove
fixture generation and `make client-usage` work without removable modules.
Use a disposable checkout for destructive regeneration comparisons; compare
logical fixture data and repeat generator output, not nondeterministic DB bytes.

V2b depends on V2a, MC-E and owner acceptance of retained evidence. Delete
only unreferenced differential infrastructure, preserve rollback and required
browser/benchmark/shape inputs. Run post-removal fixture regeneration,
`make client-usage`, Go contract/persistence tests and CI rollback equivalent
`node go-backend/tools/parity/replacement-trial.cjs --rollback-only --work
<disposable-dir>` (update this command if extraction moves its entrypoint).
Done when generators and rollback work from a clean checkout after removal;
record Node lines separately from handwritten-Go metrics. Approval: revised
retirement boundary before V2b; prior approval never authorizes broken generators.

### Q1 · Make size metrics review signals (P2, S)

Prerequisite: owner approves proposed policy. Scope: `tools/ratchet/`, tests,
CI/Makefile only as needed. Report line/complexity/map deltas without failing
solely on justified growth; retain hard correctness/dependency/nested-query
checks. Proposed `TestMetricGrowthReported` and existing guard tests; run
`cd go-backend && go test ./tools/ratchet ./tools/dbnest`, `make ratchet`, full
gates. Done when useful regression signals survive and documentation matches
tool enforcement. Approval: explicit policy approval; no tool changed here.

## 3. Deferred work and investigations — do not dispatch vague tasks

Preserve these IDs and their historical acceptance in the archived roadmap.
Before execution, architect supplies current file scope, regression, benefit
and prerequisites; this table deliberately is not authorization to code.

| IDs | Priority / reason / release condition |
|---|---|
| C1–C4, C7/C8/C11 | P1 suspected/known share, podcast schedule, watcher, cancel, sort/name/feed behaviors; reproduce with named historical regression before fix; do not prescribe timer architecture without review. |
| C12 → S6 | P0 permission-equivalence investigation before consolidation; matrix tests for library/tag/explicit/admin rules, then one definition. |
| C13/C19, C23 | P1 download/session lifecycle and race CI; focused ownership/race evidence first. |
| C15/C16 | P1 request JSON error handling and attachment truncation; targeted failure reproduction, security review if exposure changes. |
| C18 | P2 tracked test-log cleanup; small documentation/housekeeping packet only. |
| S1/S3/S4 | P2 routing migration after protocol coverage, package by package; preserve route precedence/method/base-path semantics. Untracked identity routes must be reconciled first. |
| S5/S7/S8 | P2 constructor/process-runner/UA duplication; do only alongside touched consumers with measurable ownership benefit. |
| S10/S12/S14/S15 | Domain migration follows the books/items/lists milestones above; decompose per handler family before dispatch. S11 is active planning above, not deferred. |
| S16 | P2 maps where known shape causes risk; drop arbitrary under-100 quota as acceptance. |
| P1/P4/P5 | P2 settings cache, read pool, filter-data updates; benchmark first, audit transaction/lifecycle correctness; rollback-journal invariant stays. |
| P6 | P1 baseline client-critical benchmark accompanies S13/P2/P3; detach tool dependency in V2a, no new benchmark framework. |
| Cover cache/format standardization | P2 investigate six-client format parameters/decoders first; preserve original artwork and persisted layout. No WebP-only approval. |
| Polling/binary Socket.IO support | INVESTIGATE only where client evidence requires it; no whole-subsystem replacement proposed. |

## Decisions needed

1. Books/items reads and lists are now explicit migration milestones. Implement
   them alongside affected compatibility tests; no full audit freeze or blanket
   L8 conversion prerequisite. Runtime dispatch approval is not inferred from
   a planning document; the latest owner message authorizes implementation.
2. Approve aspirational reduction target and Q1 review-signal policy; strict
   ratchet tooling remains unchanged until Q1 is implemented.
3. Accept the supported-client scope after MC-A; unknown evidence stays open.
4. R2 approves removals individually; O4 requires real-device evidence.
5. O1 (`ANALYZE` at boot), O2 (destructive TS cleanup) and O5 (new libraries)
   remain undecided. Defaults: no new boot writes/deletes/dependencies.
6. External Flash provider use must be allowed to receive repository material.
   This session's automatic review rejected that transfer; built-in cheap
   workers were used instead. No provider-access approval assumed.

## Takeover validation and next action

Documentation/audit evidence and commands actually executed are recorded in
[handover.md](handover.md). Implementation and device tests are not implied.
Next implementation card: I02 (batched book reads), after reviewed B01–B09/I01. In parallel,
continue MC-A/MC-B and source-confirmed MC-C/MC-D cases in disjoint files.
Full mobile research is not a prerequisite for I02 book-read batching. Architect dispatches
one card at a time to a cheap worker and reviews it before dependent work.
