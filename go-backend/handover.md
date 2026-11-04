# Go backend handover

Updated 2026-11-05. Current implementation is in
`/private/tmp/abs-books-domain-20261104`, branch `codex/books-domain-batching`,
based on `48c10a6a`, remote `https://github.com/ppoloskov/audiobookshelf-go`.
The original checkout remains on master with its unrelated dirty work intact.
Use this worktree/branch for continuation. No default-branch merge or deployment.

Read [AGENTS.md](AGENTS.md), [ARCHITECTURE.md](ARCHITECTURE.md) and
[ROADMAP.md](ROADMAP.md). The [pre-feature handover](archive/handover-before-books-2026-11-04.md)
is history; generated STATUS is a dated sampled differential, not live acceptance.

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

**I05:** reuse LoadMany for book-shelf hydration, preserving selected ID order,
per-user values/ACL, includes and row closure. I02/I03/I04 are checked DONE;
use I01 query instrumentation to prove shelf query counts and actual values.
Then I06 common item rows. Lists/people/progress remain later tasks; no
"finish catalog" assignment or universal DTO. Separate route removal, new
library and live-cutover decisions still apply.

## Gotchas and preserved work

- Original checkout's `.codemap/`, `go-backend/TODO.md` and untracked
  `internal/identity/routes.go` were excluded from this branch. Do not reset,
  stage or absorb them. TODO only gained a historical banner in the original.
- Never run TS and Go on the same writable database/metadata. Use isolated
  consistent SQLite/config/media snapshots. No migrations or boot deletions.
- One SQLite connection: close Rows/Tx before another pool query. Preserve
  dbtime writes, nullability and opaque stored JSON semantics.
- `items.Reader` is still the map-returning catalog interface. Typed book
  values inside outer maps are structs; existing access helpers/type assertions
  remain required. Full typed item/batched-consumer migration is not complete.
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
summary paragraphs alone never mark a task complete.

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

**Next: I05** book shelf hydration, then I06 item rows. I05/I06 and list cards
remain unchecked. I02–I04 do not complete all of S13/P2/P3 or all client/media
paths. Future API uses plain LoadMany, not the removed stateless Reader.
