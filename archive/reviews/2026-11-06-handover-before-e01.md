> Historical snapshot. Superseded next steps are not current instructions. See [live roadmap](../../ROADMAP.md).

# Go backend handover

Updated 2026-11-06. Canonical checkout:
`/Users/paul/projects/audiobookshelf-go`, branch `master`, remote
`https://github.com/ppoloskov/audiobookshelf-go`. Book/documentation commits
through `948f6a15` are integrated and pushed. **All further development is on
master; do not create development branches.** Live cutover remains separate.

Read [AGENTS.md](../../AGENTS.md), [ARCHITECTURE.md](../../ARCHITECTURE.md) and
[ROADMAP.md](../../ROADMAP.md). The [pre-feature handover](../../archive/handover-before-books-2026-11-04.md)
is history; generated STATUS is a dated sampled differential, not live acceptance.

## Current architect task — E01

Owner requested a major simplification/deletion audit on 2026-11-06, preserving
all functionality. E01a–c cheap workers are researching repeated mechanics,
data/model/persistence alternatives and protocol/lifecycle alternatives. No
source code changes or new extraction are authorized by this audit. Temporary
reports are under `/tmp/abs-e01-20261106`; root reviews leading evidence before
updating the durable deletion review and choosing proof packets. Runtime
baseline remains 44,674 lines / 10,809 complexity. D02 stays withdrawn.

## Current correction — preserve functionality

Runtime source is restored exactly to `82110311` (the last code commit).
The correction that follows is documentation-only. The owner clarified:
“We don't drop functionality.” Backup/restore and manual conversion/embedding
remain KEEP, including their active web UI flows. D02's local removal wave was
fully discarded before any commit/push: 30 tracked paths restored to HEAD and
only our new disabled-feature test removed. Inherited .codemap/TODO/identity WIP
and recovery stash were preserved. Do not reuse /tmp/abs-d02 removal snapshots.

Production baseline remains 44,674 lines / 10,809 complexity. No D02 savings or
feature losses were published. Restored backup/audiotools package tests and manager restore success/rollback
tests PASS (exit 0), with logs in `/tmp/abs-d02-rollback-verify-20261105`.
All eleven operation routes and fourteen shape records are present, and the
226-route inventory contains no product-disabled metadata. Source is byte-
identical to the already-tested published parent. Subsequent
changes are documentation-only and establish functionality preservation as a
firm constraint. D01's large feature footprints are KEEP, not deletion targets.
No 1,000-line behavior-preserving wave is currently established; next work must
prove real duplication/equivalent replacements before extraction. Do not ask
again to remove these features merely to meet a LOC quota.

## Previous accepted checkpoint

L04/L05 and D01 are accepted and ready for master delivery. The prior published
HEAD is `7c08c25b`. Lists owns HTTP/CRUD/events/feed/pagination; the old userapi
playlist/library-list/feed files and collection bodies/dispatch are deleted.
Required ACL composition remains; no obsolete read/bool-result bridge survives.
Shared request/coercion helpers each have one implementation, including the
string-array converter whose accidental duplication caused two extra `[]any` uses.

Final metrics: 44,674 production lines (−29), complexity 10,809 (−23), maps 990
(−2), high functions 178 (−1), `[]any` 326 unchanged; no metric increases.
The first +178-line move and +54-line repair were rejected. R2 removed routing
wrappers/alias registry, duplicate error pairs and array conversion; negative
POST regressions catch the accidental extra playlist actions. The baseline was
lowered with the existing tool in the exact shipping snapshot, then reviewed
and copied. The small reduction closes this in-flight stage, not a large-wave claim.

Clean snapshot `/tmp/abs-pr06-pr05-20261105/l04c-final` excludes inherited
`.codemap`, TODO and uncompilable `identity/routes.go`. Build/vet/full native-
codec suite count=1/lint2.15 zero issues/Linux arm64 CGO0/ratchet+dbnest PASS;
sqlc1.31.1 regeneration preserves all nine generated files, including untracked
output identity. Canonical source/artifact/baseline matches the tested snapshot
for 25 paths. The worker's temporary wire/shelves.go formatting experiment was
identified and returned to HEAD; no unrelated semantic edit was observed.

Route evidence: 226 ordered inventory records, 21 lists owners, 0 verified /
226 unverified; client usage 201 routes and 25 unused keys unchanged, 18 owner
paths refreshed. Both generated artifacts must update after route ownership
changes; the first full suite correctly rejected stale client usage. Another
failed retry omitted FFMPEG_PATH and hit five WebP failures. The corrected full
run passed with the documented native PATH **and** FFMPEG_PATH; no waiver.
Logs `/tmp/abs-pr06-pr05-20261105/l04c-full-test-ffmpeg.log`, `l04c-lint-final.log`,
`l04c-vet.log`, `l04c-linux-arm64.log`; failed attempts retained. No local TS
rollback, device acceptance, benchmark or deployment was run. SQL/schema and
stored meaning did not change.

D01 source audit is in [DELETION-FEASIBILITY.md](../../DELETION-FEASIBILITY.md): 4,607
nonoverlapping conditional candidate lines with 174 exact AST function ranges.
They are active product features, not proven-dead code. Native operation use is
unknown. Core audit found no credible behavior-preserving 1,000-line wave.
Generated shapes are separate: 108,510 JSON lines / 3,414,049 bytes, not Go savings.

Next: D02a owner KEEP/REMOVE decisions for server-managed backups/restore and
manual audio conversion/embedding, then D02b replacement-cost proof. Proposed
first wave is 1,535 gross lines, ≤250 replacement/retained-target budget and a
1,285 net target; this is a budget, not measured savings. Manager helpers are
outside gross and are not subtracted twice. Runtime callbacks, empty task DTO,
auth/admin checks, existing files, playback FFmpeg and TS rollback must remain
accounted for. No feature removal is authorized. No S10–S15/domain extraction
until its end-to-end deletion case is established. Do not restart completed
list/pruning work or turn unknown client usage into absence evidence.

## Earlier accepted milestones

PR06 -> PR05 -> MC-05/F1 -> S9b-R1 -> L04 list work is the accepted sequence.
PR06/PR05 are now
accepted in one code commit with individual DONE markers. PR06 removes 35
production lines; same metadata/filename/date assertions now hit active code.
PR05 removes 39 production lines after new package/import overhead. Both old
parsers and the new leaf passed the same 10 expected cases, including case,
priority, delimiters, missing Version and the preserved iPhone/Mac quirk.
Old implementations/token helper are deleted; userapi's richer parser stays.
No stateless service/interface/dependency was added. Owner WIP remains untouched.

Final metrics: lines 44,760 -> 44,686 (−74), complexity 10,848 -> 10,823 (−25),
maps 993 -> 992; []any 326 and high functions 179 unchanged. Build/vet/lint0,
dbnest/Linux arm64 build/full native-codec suite count=1 all PASS. Baselines
lowered by existing tool; final ratchet PASS. Source: 24d199e6 archive plus
exact scanner/sharing/playback/deviceinfo and dependency-rule overlays. Logs
under /tmp/abs-pr06-pr05-20261105; no Node rollback/device/latency run or SQL
generation change. MC-05/F1 and S9b-R1 are now accepted; current next work is
D02 owner decisions and replacement-cost proof; lists and D01 are accepted.

MC-05, MC-05/F1 and S9b-R1 are DONE after source, contract, craft and full
integration gates. Pinned
Lissen accepts a typed JSON 200 without ETag and caches only when one exists.
Twelve typed-JSON route cases are covered by `TestMobileConditionalGET`; the
separate `TestMobileCoverParameters` covers width-only image cases.
`author_items` had the concrete mismatch: 200 with ETag but no matching-
validator 304. F1 replaces the previous producer with request-aware writing
at 14 callsites. Cost is +17 lines/+9 complexity;
the style metric +1 is an accepted detector artifact from renaming the same
writer. This correction is not netted against PR05. The root updated three
baseline values in the same code commit; no dependency/schema/route wiring or
SQL-input change; the 14 writer callsites changed as described. No global
cache layer. Build, vet, tests, arm64 build, ratchet
(including dbnest) and golangci-lint 2.15.0 (built with Go 1.27.0, zero issues)
all PASS. Logs: `/tmp/abs-pr06-pr05-20261105/mc05-f1/verification`. Rollback,
real-device and benchmark suites were not rerun. See
[MC05-S9B-EVIDENCE.md](../../MC05-S9B-EVIDENCE.md).

S9b-R1 records KEEP for ContentType, WithoutETag, WithoutContentLength,
Streamed and ForRequest. Call counts define scope but do not prove necessity;
no option was deleted. Whole S9b deletion remains pending until a family has
safe-equivalence evidence and net source reduction. This bounded review closes
the immediate MC-05/S9b evidence milestone and does not block independent
L04/list work. Physical-device/full mobile acceptance and MC-A/B/C/D/E remain
open. L04/L05 and D01 are DONE; D02 product decisions remain open.

## Prior cleanup checkpoint closed — acceptance at 24d199e6

Current cleanup checkpoint (owner's six-priority update) is CLOSED. I05/I06 and
L01–L03 are already complete; no new domain migration is underway. RO01 proved
the restricted HTTP mode is active, so its flag/auth/routes/501 behavior stay.
RO02 deletes its duplicated prefix methods via stdlib (−25 source lines), and
PR02 deletes three membership helpers (−22). PR01, PR03, PR04, PR07 and S9a
are also DONE with integrated acceptance. S9a removes only OrderedKeys
after protecting the two actual mutation DTO field/value/header contracts;
cache/range/MIME/streaming options remain. Other S9 work stays gated on evidence.

Q1 now retains the strict ratchet and requires pre-dispatch growth budgets,
actual architect acceptance, named deletions/adapter exits and a cleanup
checkpoint before another expansion. The warning-only proposal is superseded.
List final-deletion criteria now require old bodies removed with replacement,
zero obsolete references at final wiring, original routes mounted once and
value/ACL/persistence/event proof. This is applied after L02, which already
landed; do not pretend the tighter gate preceded that work.

FF01 DONE: isolated official FFmpeg 9.0.2/libwebp built from verified signed
source; real encode/probe and full c4cf90a9 baseline suite PASS. Verbose journey
log confirms all five formerly failing image cases pass. The final integrated
cleanup suite with count=1 also PASS, exit 0. No global Homebrew changes or fake
codec shim; default binary still lacks libwebp. TESTING.md records PATH plus
FFMPEG_PATH, source hash/flags and rebuild notes. Branch docs remain master.

New verification gotcha: untracked inherited internal/identity/routes.go refers
to missing httputil.NotFound and prevents some canonical-worktree builds, not
just metrics. Keep it untouched/unstaged. Test a clean tracked archive with
exact approved source overlays; source edits still occur only on master.

Reviewed net physical production lines by card: RO02 −25, S9a −42, PR01 −44,
PR02 −22, PR03 −18, PR04 −4, PR07 −3. Total 44,918 -> **44,760 (−158)**;
complexity 10,894 -> **10,848 (−46)**; maps 995 -> **993 (−2)**. []any 326,
high functions 179 and all other metrics unchanged. Since pre-I05, lines are
still +36 but complexity −23. No claim of complete growth recovery or 30% cuts.

Contract review: complete actual POST playlist/collection fields, IDs/order/
nulls/timestamps/status/type/length/ETag asserted before and after OrderedKeys
removal; cache/range/HEAD and ACL regressions PASS. Nullable SQL bindings and
OIDC callsites remain identical store.Text/NullString values. No schema, route,
auth, event recipient or stored-format changes. Restricted HTTP behavior stays.
Craft review: no new production abstraction/dependency; obsolete bodies and
forwarder deleted. PR04 needed identity/oidc.go's seven additional callers;
all 13 package calls now use store.Text. New tests protect real endpoints;
two obsolete order-only helper tests were removed, not replaced with mirrors.

Build/vet/lint (0), dbnest, pinned sqlc generation+directory equality, Linux
arm64 CGO=0 build and full Go suite count=1 PASS on the shipping snapshot.
Lower-only make ratchet-update lowered three values; final make ratchet PASS.
The pre-update exit 1 meant counts were below baseline, not growth or a test
failure. A worker's earlier raw counts included incomplete overlays; only the
final tracked snapshot numbers above are acceptance evidence. Node rollback,
real-device acceptance and latency were not run locally; no live deployment.

Validation source: c4cf90a9 archive plus the exact approved changed packages
adminops/catalog/httputil/playback/providers/realtime/scanner/server/userapi
and identity.go/oidc.go, excluding owner routes. Logs under
`/private/tmp/abs-cleanup-checkpoint-20261105`: integrated-full-test.log,
metrics-before-lowering.log, final-ratchet.log, baseline-full-test.log,
journey-verbose.log and BUILD_NOTES.md. Docs/format/scope checks accompany push.
At this historical checkpoint, S9b needed the mobile validator matrix and
PR06/PR05 still required equivalence/net-savings proof. Current status above
supersedes that note. At that checkpoint L04/L05 and list writes were pending.

## Historical pruning plan at c4cf90a9

The prior waves added 194 production lines; moving old bodies did not deliver
net reduction. The concrete pruning plan existed but was wrongly left deferred.
The then-next work was PR01–PR04 (six dead functions, three membership loops,
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
That c4cf90a9 update was documentation only. The accepted cleanup above now
supersedes its pending execution status.

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

- playlistJSON (now deleted by PR07): global/library playlist lists, ownedPlaylist and its detail/
  mutation users, create/update/add/remove response/event builders and
  createPlaylistFromCollection. Keep only its single forwarding call. Delete
  through PR07's direct calls; all eight now use lists.Playlist. No adapter remains.
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

**PR01–PR04 now DONE:** six uncalled functions removed and existing helpers reused.
The original scope was to remove six uncalled functions and substitute
equivalent existing helpers (~81 production lines). See the
[pruning review](../../PRUNING-REVIEW.md) and exact ROADMAP cards. G02 is completed
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

**Current next:** behavior-preserving deletion feasibility; D02 is withdrawn.
L04 planning, PR01–PR07, MC-05, MC-05/F1 and S9b-R1 are DONE;
whole S9b deletion remains pending. I01–I06 do not complete all of
S13/P2/P3 or all client/media
paths. Future API uses plain LoadMany, not the removed stateless Reader.
