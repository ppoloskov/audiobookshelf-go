# Go backend handover

Updated 2026-11-05. Canonical checkout:
`/Users/paul/projects/audiobookshelf-go`, branch `master`, remote
`https://github.com/ppoloskov/audiobookshelf-go`. Book/documentation commits
through `948f6a15` are integrated and pushed. **All further development is on
master; do not create development branches.** Live cutover remains separate.

Read [AGENTS.md](AGENTS.md), [ARCHITECTURE.md](ARCHITECTURE.md) and
[ROADMAP.md](ROADMAP.md). The [pre-feature handover](archive/handover-before-books-2026-11-04.md)
is history; generated STATUS is a dated sampled differential, not live acceptance.

## Next: code deletion before more migration

The prior waves added 194 production lines; moving old bodies did not deliver
net reduction. The concrete pruning plan existed but was wrongly left deferred.
Current next work is PR01–PR04 (six dead functions, three membership loops,
three nullable helpers and identity text wrapper; ~81 estimated net lines),
then PR07 (eight direct callers and deletion of playlistJSON; ~2 net lines).
Recheck current references and run named checks. The deletion wave must lower
net production source; no new layers or baseline increases. Mark each card
DONE only with its actual delta and validation in the implementation commit.

Current source recheck at f466e36e confirms all six PR01 names are definitions
only. playlistJSON is a pure three-line forwarder with an unused user parameter:
seven callers in playlist.go, one in library_lists.go. Direct lists.Playlist
calls preserve existing guards/orchestration, so PR07 does not depend on L05.
collectionDTO still performs real per-user ACL/result adaptation; retain it
until a scoped caller migration replaces that responsibility.

PR06 (~31 lines) then PR05 (~30) are conditional on preserved assertions,
equivalent behavior and measured net savings. groupKey and guarded routes are
not approved cuts. These estimates do not promise to recover all 194 lines.
L04/write/route migration follows the deletion wave; it remains planned.
This update changes documentation only: references/source reviewed and
git diff --check checked; no runtime removals or test runs claimed.

## Accepted L01–L03 read wave

L01's full-schema/live-catalog baseline ran before ownership moved. L02/L03
now have architect-reviewed source and behavior, with individual DONE markers
in this implementation commit. internal/lists/reader.go owns plain Playlist
and Collection functions; no stateless service, new interface/dependency,
constructor or extra Handler field. Production imports from lists into
userapi/catalog are prohibited by the existing dependency rule's new file scope.

Deleted: playlistJSON's GetPlaylist/ListPlaylistMediaItems/entry assembly,
mediaRef/refs copy, collectionDTO's SELECT/item-loop/assembly and redundant
visible counter. Collection returns three values; userapi's thin adapter
supplies the old successful hasVisible=true/error=false contract. It still
binds h.canAccessItem to the request user in the same order. ownedPlaylist's
pre-hydration owner/library SELECT and status ordering remain explicit.

Retained adapters and exact removal conditions:

- playlistJSON: global/library playlist lists, ownedPlaylist and its detail/
  mutation users, create/update/add/remove response/event builders and
  createPlaylistFromCollection. Keep only its single forwarding call. Delete
  now through PR07's direct calls; waiting for write migration/L05 is unnecessary.
- collectionDTO: global/library collection lists/detail/delete, create/update,
  batch/add/remove readback/events and collection-to-playlist conversion. Keep
  only the per-user visibility callback and old result contract. Delete after
  every such caller migrates in L05; no old SELECT/assembly body remains.
- recordToMap/loadItem: catalog page/detail/shelf consumers still need the
  map boundary. Their later mixed-media projection migration removes these;
  I06 is not permission to copy the old decoder/SQL back into catalog.

Contract tests cover mixed book/podcast projections/order, nulls/timestamps,
owner and partial/no-library access, explicit-item denial, all-filtered empty
collections, orphan playlist records and collection callback order/failure.
Craft review removed the redundant intermediate slice/counter and used one
small read file with shared imports. No outstanding scoped review findings.

Metrics versus 4d34d494: 44,889 -> 44,918 production lines (+29), maps 993 ->
995 (+2), complexity 10,894 unchanged; high functions 179 and []any 326 unchanged.
Architect explicitly accepts the two baseline edits for ownership/temporary
adapters, not source reduction. Combined I05–I06 + L01–L03 cost versus 7105382c
is +194 production lines / +23 complexity. No offset from unrelated pruning.

Focused and full lists/userapi PASS; build/vet/lint (0), dbnest, Linux/arm64
CGO=0 build and final ratchet PASS on the tracked-source validation snapshot.
Pinned sqlc 1.31.1 regeneration and recursive generated-directory comparison
PASS: identical files, including schema copy, no new output. Full suite exits
1 only for the same five native WebP TestJourney cases; no new failure.
Logs: `/tmp/abs-i05-i06-20261105/l01-userapi.log`, l03-full.log and
lists-full-test.log. No schema, dependency, write, event or route changes;
rollback/device/latency suites not rerun. No live deployment acceptance.

After the deletion wave, L04 must turn the source-mapped write-removal inventory into exact small
cards. Its inventory is planning input, not dispatch permission; L04/L05 and
list writes/routes remain unchecked. Current request's I01–I06 completion and
start of L work are fulfilled; standalone PR cards are now next, not executed.

## Accepted I05–I06 wave

I05/I06 are architect-reviewed, checked DONE and pushed as `4d34d494`.
personalizedItems no-includes book hydration is batched, with
original selection/order/duplicates/error behavior retained. Other media and
includes retain fallback. Corrected fixture tests actual 10/50 returned books,
explicit ACL exclusion and other-user-only progress among top-ranked rows,
nullable titles, prefix values and missing/duplicate IDs. Helper-path reads:
51/251 before, 6/6 after (not full personalized endpoint totals). Focused and
full catalog tests passed; logs under `/tmp/abs-i05-i06-20261105/`.

I06 deleted the temporary I05 envelope SQL, stringsToArgs, catalog scanItemRow,
scalar item SELECT and fileType switch. items.Record/ScanRecord/LoadMany,
RowColumns and FileType are the single owners. A wire-map adapter recordToMap
remains for existing catalog page/detail/shelf consumers. It must not duplicate
decoding. Thin loadItem remains for current catalog detail/shelf consumers;
remove it only when those callers use the typed common record directly.
Extra shelf lookup maps and the one-caller scan wrapper were rejected/deleted.

Contract review: IDs/media IDs, order/duplicates, nullable/sparse times/size,
strict malformed-date errors, file classification and missing/canceled loads
preserved; includes fallback and book/podcast goldens remain passing.
Craft review: concrete DB dependency, no service/interface/dependency added;
single decoder/query/classifier, explicit cursor closure and no nested reads.

Measured versus 7105382c: production lines 44,724 -> 44,889 (+165), complexity
10,871 -> 10,894 (+23), maps 991 -> 993, []any 325 -> 326, high-complexity
functions 178 -> 179. Architect accepts these five explicit baseline edits for
new typed/bounded loading and measured query reduction; no source reduction
claimed or strict guard relaxed. Final ratchet passes with reviewed baseline.

Build, vet, lint (0 issues), dbnest, Linux/arm64 CGO=0 build PASS. Full suite
exits 1 for only the same five native libwebp TestJourney cases; no new failures.
Focused I01–I06 and full items/catalog packages PASS. Production source, schema,
generated queries and dependencies outside the named files are unchanged.
No rollback/device/latency tests run; no writes/schema/protocols changed.

Validation snapshot `/tmp/abs-i05-i06-20261105/validation` uses tracked master
plus the exact changed catalog/items files; inherited untracked identity routes
are excluded from shipping metrics. Raw ratchet in the dirty owner checkout
will also count that WIP; do not delete it or absorb its metrics into baseline.
Logs: full-test.log and i05-*.log in that temporary directory's parent. One
initial ratchet invocation from repo root failed (wrong module directory); the
correct snapshot go-backend invocation passed after explicit baseline update.
The following L01–L03 read wave is now accepted above. L04 write-seam planning
follows; no list write/route migration yet.

## Master consolidation and recovery

Owner changed workflow to master-only on 2026-11-05. All four feature commits
were fast-forwarded to master and pushed; no conflicts. Local and remote
branch inventories now contain only master. The feature branch/worktree and
already-integrated replacement-trial remote were removed after ancestry checks.
The latter had zero unique commits and was 177 commits behind the old master.

Before consolidation, the dirty original checkout was saved as stash
`aeedc216bc2cee564d4c48b9470bf9e3139a744d`. Recovery files live in
`/tmp/abs-master-consolidation-20261105/`: `MANIFEST.md`, full original status,
tracked patch and verified `original-stash.bundle`. The stash and bundle are
retained; no stash pop/drop or blanket clean/reset occurred. The newer merged
documentation was kept; only unique inherited `.codemap/` (33 files), TODO
and identity/routes.go were restored as untracked, unstaged work.

The old `/private/tmp/audiobookshelf-packet1-88db` directory remains intact.
Its recorded detached commit `88db747b` was already in master, but `.git` is
missing, so Git could not verify file status. Only orphaned Git metadata was
pruned; do not delete its physical contents without inspecting/recovering them.
Temporary recovery files are local safeguards, not remote backups; keep the
retained stash until inherited work is explicitly reconciled.

## Completed book-domain feature — d2ec8d9 snapshot

B01–B09 book-read/projection outcomes and I01 query instrumentation are
implemented and architect-reviewed after the cheap workers finished:

- `internal/books/batch.go`: plain `books.Load(ctx, db, id)` delegates to
  bounded LoadMany, owning book reads
  and ordered author/series links. No stateless Reader object or duplicate DB
  handle. Nullable title is exposed narrowly; source fields remain private.
- `metadata.go` / `projection.go`: full, minified and expanded media remain
  existing concrete `wire.Book*` values. Opaque files decode once per load;
  expanded output reuses full/minified results. Counts/offsets/URLs/nulls and
  relation ordering preserve existing behavior. No metadata/chapter writes moved.
- Old production `catalog/book_wire.go`, book row/relation scans and duplicate
  book builders are removed. Catalog remains HTTP/item orchestration; subseries
  has a necessary direct book projection call. `stringPointerValue` moved to
  its remaining batch-chapters consumer.
- `store/json.go`: one lenient persisted JSON slice decoder and object decoder
  replace duplicate bodies in catalog/books. Empty/null/malformed and partial
  type-error cases are tested; these are not request-validation helpers.
- Original map serializer/read oracle remains test-only and independent.
  Goldens were not regenerated. New tests assert null/empty values, relations,
  media identity, excluded files and reader not-found behavior.
- Test-only SQL instrumentation sees direct and prepared reads. No production
  tracing hook, new library, schema change, route change or write change.

Initial book-domain metrics versus the base (historical snapshot):

| Metric | Before | After | Delta |
|---|---:|---:|---:|
| Handwritten non-test Go lines | 44,586 | 44,585 | −1 |
| Cyclomatic complexity | 10,851 | 10,840 | −11 |
| Dynamic map uses | 993 | 991 | −2 |

The initial d2ec8d9 baseline only fell. That was a boundary/duplication
improvement with nearly flat source size. Current I02–I04 query results and
reviewed source-cost adjustment are recorded below; wall-clock latency and
RSS remain unmeasured.

## Review findings and resolution

Contract pass found a real draft regression: stored ebook `{}` had become `{}`
on the wire, whereas old bookEbook returned null. Restored normalization and
added explicit full/expanded assertions. The worker added test+fix together;
**no pre-fix red test run was observed**, so do not claim one.

Craft pass rejected broad import bans and an initial source/complexity increase.
Books-only dependency guards now leave valid catalog->items imports alone.
Shared decoding, reuse of expanded metadata, removal of the stateless Reader
and numeric-helper duplication address the substantive complexity. Readable
projection field layout retained; no test weakening, line-packing campaign,
new framework or guard relaxation used for acceptance. No outstanding scoped
code finding; full runtime release acceptance has the environment gap below.

## Verification actually executed

Commands from go-backend used installed Go 1.27.0 with `GOTOOLCHAIN=local` and
`GOCACHE=/tmp/abs-takeover-20261104/go-cache`; go.mod's suggested toolchain is
1.27.3. This does not change/pin a new toolchain or certify a production image.

- Focused book/golden/reader/JSON tests: PASS (exit 0), including
  TestBookProjectionValues, TestLoadBookNullsAndRelationOrder, both existing
  source goldens and the store JSON table tests.
- TestQueryCounterIncludesPreparedReads: PASS, both direct/prepared count once.
- `go build ./...`: PASS; `go vet ./...`: PASS.
- golangci-lint 2.15.0 with `--config=../.golangci.yml ./...`: PASS, 0 issues.
- `go run ./tools/dbnest`: PASS. `go run ./tools/ratchet -update`: PASS,
  lower-only changes; final `go run ./tools/ratchet`: PASS.
- `CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build ... ./cmd/audiobookshelf-go`:
  PASS, output under /tmp, not a deployed/published binary.
- Final escalated `go test ./...`: **FAIL**, exit 1, exact same baseline five
  TestJourney cases: item.saga1.cover, item.novel.cover, podcast.cover,
  author.image.default, author.image.webp. **No additional failing cases.**
  Catalog/store/userapi/playback/scanner and inventory-tool package tests pass.
- Local links, gofmt of changed Go files, source scope and `git diff --check`
  are verified before delivery. The generated inventory/module/schema files
  remain unchanged. No SQL generation input changed; no sqlc regeneration needed.

The full-suite failures are pre-existing on clean 48c10a6a: Homebrew FFmpeg
9.0.2 lacks libwebp, reproduced by its native encoder probe (exit 8). No cached
compatible FFmpeg exists. No cwebp masquerading shim or test skips are used to
claim green. Logs: `/tmp/abs-takeover-20261104/baseline-full-test.log` and
`books-final-full-test.log`. Branch publication is not full release acceptance.

Not run: Linux runtime tests, image publication, real-device acceptance, Node
rollback/differential or performance benchmarks. No persistence writes changed;
rollback remains required in CI and for future write changes. Current CI gates
images on verify+rollback; parity is advisory since V1.

## Next executable work

**Next PR01–PR04:** remove six uncalled functions and substitute
equivalent existing helpers (~81 production lines). See the
[pruning review](PRUNING-REVIEW.md) and exact ROADMAP cards. G02 is completed
analysis only: no runtime removal or deletion tests. PR05/PR06 are conditional
follow-ups; userapi UA and permission semantics stay separate. unused analysis
on a clean master archive reported 12 findings (exit 1), including genuine
test/tool callers; warnings were classified rather than blindly deleted.
This documentation packet ran consistency/link review and git diff --check;
runtime tests were not rerun because production source was unchanged.
That requested I05 -> I06 -> L01 start is complete. Do the actual deletion
wave next before further migration; see ROADMAP's removal contract.
I01–I06 and L01–L03 stay completed, not reimplemented.

**After pruning, L04:** turn the named write-removal inventory into exact bounded
packets. I01–I06 and L01–L03 are checked DONE with named removal evidence;
no need to reimplement them. People/progress remain later tasks; no
"finish catalog" assignment or universal DTO. Separate route removal, new
library and live-cutover decisions still apply.

## Gotchas and preserved work

- `.codemap/`, `go-backend/TODO.md` and `internal/identity/routes.go` are
  preserved inherited untracked work in the canonical master checkout. Do not
  reset, stage or absorb them. TODO only gained a historical banner earlier.
- Never run TS and Go on the same writable database/metadata. Use isolated
  consistent SQLite/config/media snapshots. No migrations or boot deletions.
- One SQLite connection: close Rows/Tx before another pool query. Preserve
  dbtime writes, nullability and opaque stored JSON semantics.
- `items.Reader` is still the map-returning catalog interface. Typed book
  values inside outer maps are structs; existing access helpers/type assertions
  remain required. Full typed item/batched-consumer migration is not complete.
- List extraction gotcha: before L03, collectionDTO incremented visible only
  when appending books, then returned `visible > 0 || len(books) == 0`. Thus successful
  reads retain a same-library collection even when every member is filtered
  out; library ACL still applies. Preserve observed behavior in L01/L03 rather
  than silently changing it to hide the collection. L03 removes the counter
  and preserves that result in its adapter. Playlist ownership and
  library-denial statuses differ and must remain separate.
- Native FFMpeg must have libwebp for all journey image tests. Node reference
  tools need Node 22, built dist-server and disposable fixtures; run route-probe
  last. Preserve macOS /private path normalization and restat copied media inodes.
- OIDC providers must close keep-alives; backup comparisons need matching
  archive copies. Historical schema-1 reports lack current provenance.
- Full six-client audit remains partial; inventory's 201 web/log/trial routes,
  25 formerly-unused routes, one unmatched pattern and nine dynamic call sites
  are not mobile completeness. Confirmed external routes must remain protected.
- C5 cover search/cancel are implemented. Socket.IO is in-repo WebSocket-only
  v4, default namespace; JS interop can skip without dependency. 32 MiB is the
  pinned WS library's default frame cap; don't claim it is literally unbounded.
- V2 cannot remove shared fixture/route/shape/browser/benchmark/rollback inputs
  before extraction and regeneration proof. 30% reduction is aspirational.
- Expensive architect plans/reviews; cheap built-in workers implemented this
  feature. OpenCode Flash source export remains rejected for its external
  provider; no bypass or external-provider permission assumed.

## Explicit completion tracking

The owner correction was pushed as ef4d14e: B01–B09 and I01 have individual
[x] DONE markers. I02–I04 now carry the same explicit checked status in their
implementation commit. AGENTS requires this for every accepted task, alongside
actual test results and unresolved limitations. I05 onward stay unchecked;
summary paragraphs alone never mark a task complete. I05/I06 now follow the
same rule in their implementation commit.

## I02–I04 delivery — 2026-11-05

All three cards are explicitly **[x] DONE in the same code commit**. Completion
tracking correction B01–B09/I01 was already pushed as ef4d14e. Future completion
commits must check the individual card, not rely on summary prose.

LoadMany deduplicates, chunks by 250 and closes rows between book/author/series
reads. No missing book is synthesized from orphan links. Errors/cancellation
return no partial successful result; scalar Load retains sql.ErrNoRows. The
old single-book SQL/relations are deleted. Uses stdlib slices.Chunk, bound SQL
arguments and concrete types; no new service/DB interface/dependency.

Ordinary noncollapsed/no-includes book pages batch selected media IDs after
closing item rows, project in row order and reuse the settings snapshot for
prefixes. Other branches retain their prior behavior. Schema, writes, auth,
selection joins, pagination and client response contracts are unchanged.

Evidence: tests use real SQLite and query instrumentation. LoadMany reads:
0 input -> 0, 1/10/50/250 unique -> 3, 251 -> 6. Endpoint test was run red
against read-only git archive ef4d14e with the same corrected test copied in:
43/203 reads for 10/50 actual returned filtered rows (not just requested limits).
Green is 7/7 for ordinary/minified envelopes. Tests assert every ID/order,
genre and explicit ACL filtering, pagination, totals, null titles/descriptions
and fresh default/custom/mixed/empty prefix settings through real responses.
Initial draft fixture returned 5/25 rows; that evidence was corrected before
acceptance. No old working-tree code was reverted to manufacture red results.

Source-cost decision versus ef4d14e: lines 44,585 -> 44,724 (+139), complexity
10,840 -> 10,871 (+31), []any 323 -> 325 (+2); maps 991 and functions >15 at
178 unchanged. Architect accepts bounded loading and cursor/error handling
for this measured query reduction, with one read implementation and no source
packing. The three baseline values were explicitly adjusted, not mislabeled
as reductions or a green ratchet before adjustment. Strict ratchet policy
remains unchanged; Q1 is still separate. This does not waive the simplification
and deletion goals on future tasks.

Final checks: focused query/value/golden tests PASS; build, vet, lint (0 issues),
dbnest and Linux/arm64 CGO=0 build PASS. Full native go test ./... exits 1 with
only the same five pre-existing WebP TestJourney failures; no new failing
cases. FFmpeg probe still lacks libwebp. Final ratchet and documentation/scope
checks run after explicit baseline and completion-marker integration.
Logs: /tmp/abs-takeover-20261104/i02-i04-full-test.log and i02-i04-metrics.log.
No Node rollback, device acceptance, latency/RSS benchmark or deployment.

**Current next: PR01–PR04**, then PR07 and proven PR06/PR05, before L04.
These deletion cards and L04 onward remain unchecked. I01–I06 do not complete all of
S13/P2/P3 or all client/media
paths. Future API uses plain LoadMany, not the removed stateless Reader.
