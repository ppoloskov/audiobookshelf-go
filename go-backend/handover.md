# Go backend handover

Updated 2026-11-04. Current implementation is in
`/private/tmp/abs-books-domain-20261104`, branch `codex/books-domain-batching`,
based on `48c10a6a`, remote `https://github.com/ppoloskov/audiobookshelf-go`.
The original checkout remains on master with its unrelated dirty work intact.
Use this worktree/branch for continuation. No default-branch merge or deployment.

Read [AGENTS.md](AGENTS.md), [ARCHITECTURE.md](ARCHITECTURE.md) and
[ROADMAP.md](ROADMAP.md). The [pre-feature handover](archive/handover-before-books-2026-11-04.md)
is history; generated STATUS is a dated sampled differential, not live acceptance.

## Completed feature scope

B01–B09 book-read/projection outcomes and I01 query instrumentation are
implemented and architect-reviewed after the cheap workers finished:

- `internal/books/reader.go`: plain `books.Load(ctx, db, id)` owns book reads
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

Actual global metrics versus the base:

| Metric | Before | After | Delta |
|---|---:|---:|---:|
| Handwritten non-test Go lines | 44,586 | 44,585 | −1 |
| Cyclomatic complexity | 10,851 | 10,840 | −11 |
| Dynamic map uses | 993 | 991 | −2 |

No ratchet baseline was raised. This is a domain-boundary/duplication improvement
with nearly flat source size, not a large code deletion. No constant-query-count
or latency win is claimed yet; book loading still performs individual reads.

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

**I02:** implement `books.LoadMany(ctx, db, ids)` using bounded batched rows and
ordered association queries; preserve single Load not-found behavior. Then I03
prefix batching, I04 ordinary book pages, I05 shelves and I06 common item rows.
Use I01 instrumentation to prove actual query counts and meaningful values.
Migrate one consumer family at a time. Lists/people/progress remain later tasks;
no “finish catalog” assignment or universal DTO. Owner authorized implementation
and pushing reviewed ready features; route removals/new libraries/live cutover
retain their separate decisions.

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
