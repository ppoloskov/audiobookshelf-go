# Independent review of phases 0–2: value and architecture

Reviewed 2026-10-31, Australia/Brisbane. Result: **revise before extending the migration**.

Scope: the current worktree at HEAD `718cc0a0`, including uncommitted Phase 0/0b harness work, Phase 1 guardrails and CI, Phase 2 book DTOs and their consumers, and pinned upstream serializer behavior. This is dated review evidence, not generated project status. I re-derived the findings independently; where they coincide with `PHASES-0-2-REVIEW-2026-10-31.md`, the table below records the currently observed evidence rather than relying on that review. No production code, baseline, or ledger was edited; `make parity` and `make status` generated the status/record artifacts described below, and the Go test run added scanner fixture directories.

Environment: Go 1.27.0 darwin/arm64, Node 22.23.4 via mise, golangci-lint 2.15.0, FFmpeg without `libwebp`. The Ubuntu CI and the capable-FFmpeg success path were not executed.

## Value assessment

The verification infrastructure is the real achievement of these phases:

- `make parity` builds the Go binary from current source, creates a fresh fixture, runs Node and Go independently, and refuses a zero-case result.
- The differential self-tests now cover zero execution, partial omission, duplicate requested IDs, unknown IDs, and a deliberately wrong response.
- The report comparator checks verdicts, skipped/error state, expected status, stateful checks, and input provenance, not only bodies and headers.
- The harness has one case catalog (`tools/parity/cases/core.json`), shared helpers under `tools/parity/lib/`, and a runner under about 500 lines.
- `tools/ratchet` now counts dynamic-map aliases and has negative fixtures; depguard has fifteen positive/negative fixtures proving the intended rules actually reject forbidden imports.
- The pinned-source book golden executes the real `Book.js`/`LibraryItem.js` serializers under stubs and pins the source hashes. That is materially stronger than comparing two Go implementations.
- `scanner` and `userapi` receive one shared `catalog.Handler`; neither feature package imports `catalog` in production code.

The DTO layer is much weaker. The pilot changed the Go type of the media value but left the shared API and all consumers on dynamic maps. Three wire structs exist, but `typedBookEnvelope` returns `any`, `ItemJSON`/`ItemMinifiedJSON` still return `map[string]any`, and the new `items.Reader` returns a map alias and keeps an `expanded bool`. Every consumer that needs media data either type-switches or JSON-marshals it back into a map. This does not deliver "types at the boundary"; it distributes a union type across the feature packages. The measured map decrease is not evidence that the DTO replaced dynamic maps: the old map serializer is retained in `handler_test.go`, which the ratchet excludes, while production still builds three metadata maps per book.

## Findings

### V1 — P1: the shared boundary is type-erased, so the pilot is not yet an architecture

Evidence:

- `internal/catalog/book_wire.go:29` takes a `shape string` and returns `any`.
- `internal/catalog/handler.go:2300` and `:2335` still return `map[string]any`; media is stored as a struct inside that map.
- `internal/items/reader.go:6–12` defines `Object = map[string]any` and keeps `ItemJSON(..., expanded bool)`.
- Consumers recover media by type switch (`internal/userapi/playlist.go:716–723`, `internal/catalog/personalized.go:87–96`, `internal/catalog/handler.go:2008–2014`) or by JSON round-trip (`internal/catalog/handler.go:2231–2233`, `internal/scanner/metadata.go:27–34`, `internal/realtime/hub.go:582–591`, `internal/catalog/batch_chapters.go:253–255`).

This is the same failure mode as the collection-to-playlist loss: a consumer that assumes a map can silently skip a typed value. The repair added another type switch at the one known site; it did not remove the class. It also explains the ratchet regression in V3: the new "shared" alias is still a dynamic map.

Smallest direction: three concrete builders returning `wire.BookFull`, `wire.BookMinified`, and `wire.BookExpanded`; no `shape string` or `expanded bool`. Load the book row and relations once. Give consumers one media identity/access helper (for example `ID()` and the access-relevant fields) instead of per-package switches. Migrate the whole consumer slice in one packet. No repository framework or DI container is needed.

### V2 — P1: the core parity gate is red, including the new R1 witness

Observed: `make parity` executed 193 cases: 191 passed, 2 failed, 0 skipped. The failures are `playlist-from-collection-create` and `playlist-from-collection-read`. Their bodies contain the expected playlist items; every difference is an unnormalized fixture path:

- `items[].libraryItem.path`
- `items[].libraryItem.media.coverPath`
- nested `libraryFiles[].metadata.path`, `audioFiles[].metadata.path`, `tracks[].metadata.path`

Cause: `tools/parity/lib/compare.cjs:88–95` lists the cases whose paths are normalized, and the new `playlist-from-collection-*` IDs are absent. The R1 behavior repair itself is credible: the userapi regression test passes, the response items are present and ordered, and `playlist-from-collection-delete` passes. The witness that should prove R1 cannot pass as written.

Smallest repair: add the two case IDs to `mediaPathCases` and rerun. Do not widen path normalization globally; the existing root checks must still run.

### V3 — P1: the ratchet gate is red and the baseline cannot be corrected under the current policy

Observed: `make ratchet` reports `map[string]any and aliases: 1129 (baseline 1126)`; every other metric is equal. The alias-counting repair for R5 is correct, but `ratchet-baseline.json` was generated before that repair. The new `items.Object` alias accounts for the difference: one alias declaration, two interface result uses, and one scanner slice replacing a direct map.

`make ratchet-update` refuses to raise an existing count, so the worktree cannot satisfy the project's own completion gate. Decide explicitly: either remove the alias and reduce the count elsewhere, or approve a one-time documented reset because the metric definition changed. Do not hand-edit the baseline.

### V4 — P1: the route-inventory gate is red and a durable document claims it passes

Observed: `python3 go-backend/tools/parity/build_route_coverage.py --check` exits 1 with `route coverage ledger is stale`. Generating to a temporary path succeeds and shows the executable catalog now declares 164 variants across 66 routes, versus 118/58 in the checked-in ledger. The source hashes also changed.

`go-backend/handover.md:20` says the check passes. `docs/agent-work/tasks/TASK-20261031.md` says Phase 2 is complete with gates passed. Both are contradicted by the current commands. Regenerate the inventory after the code is final and correct the claims; do not promote coverage status from the stale file.

### V5 — P2: the pilot adds JSON round-trips and repeated work on hot paths

Observed conversions introduced by the typed value:

- Realtime access filtering calls `objectMap` on the media of every `*` payload, per connected client (`internal/realtime/hub.go:520–530`, `:582–591`). A scan that emits `items_added`/`items_updated` therefore serializes and decodes the same media once per item per client. `objectMap` returning nil is treated as unrestricted at `:545–546`, while the catalog access check fails closed at `handler.go:2231–2233`; the failure modes are inconsistent.
- Scanner metadata writing marshals and unmarshals the expanded item for every imported item (`internal/scanner/metadata.go:27–34`).
- Item detail access control marshals the media to read `explicit` and tags (`internal/catalog/handler.go:2228–2240`).
- The audio-tag endpoint does the same and ignores both errors (`internal/catalog/batch_chapters.go:253–256`).

The builder also constructs `metadata`, `minMetadata`, and `expandedMetadata` for every shape, encodes `metadataJSON` before the minified branch, and calls `titlePrefix` twice per book (`internal/catalog/book_wire.go:62–103`). Each `titlePrefix` call queries `settings` (`handler.go:732–745`). A book list therefore pays two settings queries per item and marshals unused shapes. The prior review identified this; the worktree still has it. Load the settings value once, build only the requested shape, and compute access metadata once before fan-out.

### V6 — P2: the source golden is real, but its corpus cannot validate the claimed edges

Observed: `node go-backend/tools/test-book-item-oracle.cjs` passes for five fixtures and three shapes. The fixtures cover null description, literal `"null"` ebook column, series without sequence, multiple audio files with an excluded track, explicit, and abridged.

All five fixtures use `ebookColumn: "null"` and `coverPath: null`; none has a non-null `ebookFile`/`ebookFormat`, a non-null cover path, or a null duration. Consequently the tests cannot distinguish a correct `EbookFormat` omission/encoding from a regression, cannot exercise the non-null `CoverPath *string` path, and cannot exercise `Duration any` as null. The source golden and the Go golden/dual-run test also decode before comparing, so they do not check upstream key order; nested metadata is marshaled from a Go map and therefore appears alphabetically.

Add at least one fixture with a full ebook (`ebookFormat: "epub"`, metadata size), a non-null cover, null duration, and empty tags; regenerate the golden deliberately from the pinned source. If exact key order is part of the contract, compare raw bytes or type the metadata in upstream order rather than comparing decoded maps.

### V7 — P2: tests write thousands of directories into the source tree

Observed: `go-backend/internal/scanner/items/` contains about 4,634 UUID directories and 18 MB of `metadata.json` files; 3,995 of them were tracked in HEAD and the rest were untracked run output. The cause is `internal/scanner/metadata.go:52` joining an empty `cfg.MetadataPath` with `"items"`, while many scanner tests pass `config.Config{}` (for example `internal/scanner/scanner_test.go:204, 260, 343, 432, 501`). Go test runs each package with its source directory as cwd, so the relative path lands inside the package.

This is not only test hygiene. `WriteItemMetadata` is exported and will write into the process cwd if a caller supplies an empty metadata path. Reject an empty path at that boundary and give test fixtures a `t.TempDir()` metadata path. Do not hide the symptom with a new `.gitignore` entry.

### V8 — P2: the equivalence and status gates are not durable

Observed:

- `tools/parity/suite-runner.cjs:61–66` looks for `contracts/parity/baselines/core-schema2-${profile}.json`; that file does not exist, so `make parity` runs a second fresh differential as its baseline. This proves repeatability of the current catalog, not equivalence to an accepted prior result. A case removed from `core.json` and `EXPECTED_STATUS` is invisible because the expected set is derived from the same current catalog.
- The only baseline file in the worktree is `contracts/parity/baselines/baseline-88db747.json`; it is untracked, schema 1, and cannot be used by the schema-2 comparator.
- `go-backend/STATUS.md` is absent in a clean checkout. `make status` fails until a local parity run has written a record. After the run above it exists and honestly says `Current result: FAIL` with 193/191/2, but the README and handover link it as the authoritative status.
- `go-backend/README.md:3` still says generated status belongs in `STATUS.md` "when that generator is available"; the generator is available now.

Commit a reviewed schema-2 baseline (or a baseline/inventory manifest that enforces no fewer cases) and a generated status artifact, and document the reset process. Otherwise the equivalence gate is self-referential and the status source is not present for a cold-start maintainer.

### V9 — P3: residual guardrail and robustness gaps

- The ratchet does not walk aliases used as generic type arguments (`IndexExpr`/`IndexListExpr`), so that spelling can still evade the metric.
- `internal/userapi/userapi.go:48–50` accepts a nil `items.Reader`; `scanner.New` rejects one (`internal/scanner/scanner.go:55–57`). A nil reader makes playlist routes panic instead of failing at construction.
- `internal/catalog/batch_chapters.go:253–255` ignores marshal/unmarshal errors, violating the "no `_ = json.Unmarshal`" rule.
- `docs/agent-work/capabilities/` still describes the capability directory as the active ledger, while `ROADMAP.md` Phase 0 item 5 says hand-curated capability records are archived evidence, not a gate. The generator is archived, but the competing "active ledger" heading remains.

## Prior-review status, independently checked

| Prior finding | Current evidence | Status |
|---|---|---|
| R1 collection-to-playlist loss | Userapi regression test passes; parity response items present and ordered; only fixture paths differ | Behavior fixed; witness/gate not closed (V2) |
| R2 depguard ineffectiveness | Pinned lint exits 0; 15 boundary fixtures reject/allow correctly | Closed |
| R3 partial case omission | 11 harness self-tests pass; 193-case inventory reconciles; partial-omission stub case fails correctly | Closed |
| R4 verdict/state-blind comparator | 21 report/status tests pass; comparator checks verdicts, skipped/error, expected status, stateful checks, provenance | Mostly closed; durable-baseline gap remains (V8) |
| R5 alias-count bypass | Alias fixtures pass; production count is 1129 against a 1126 baseline | Metric fixed; gate/baseline not closed (V3) |
| R6 status source absent | `make status` target and generator exist; no clean-checkout status; generated result is FAIL | Partial |
| R7 route inventory stale | `--check` exits 1; generator to temp succeeds; handover claims pass | Open (V4) |
| R8 FFmpeg capability assumption | Profile detection and encode probe exist; expected WebP status follows the profile; 11 self-tests pass | Design closed; capable path not exercised here |

## Verification matrix

All commands below were run against this worktree before this document was added.

| Check | Observed result | Limit |
|---|---|---|
| `CGO_ENABLED=0 go build ./...` | Exit 0 | No deployment claim |
| `CGO_ENABLED=0 go vet ./...` | Exit 0 | — |
| `CGO_ENABLED=0 go test ./...` | Exit 0 | Tests leave scanner metadata artifacts (V7) |
| `make ratchet` | Nonzero: 1129 > 1126 | V3 |
| Pinned golangci-lint 2.15.0 | Exit 0, `0 issues` | Rules proved by the fixtures below |
| `node go-backend/tools/test-dependency-boundaries.cjs` | 15 passed | Requires golangci-lint 2.15.0 |
| `node --test .../test-differential-selftest.cjs` | 11 passed | Stub servers; partial omission covered |
| `node --test .../test-compare-reports.cjs .../test-status.cjs` | 21 passed | Schema-2 reports only |
| `node go-backend/tools/test-book-item-oracle.cjs` | 5 fixtures, 3 shapes | Narrow corpus (V6) |
| `python3 .../build_route_coverage.py --check` | Exit 1, stale | V4 |
| `make parity` | Nonzero: 193 executed, 191 passed, 2 failed | V2 |
| `make status` | Exit 0; generated status says FAIL | Requires a prior parity record |
| `make parity-full` | Not run | Operational suites remain separate |
| Backup-restore suite | Not run | Prior review's restore mismatch remains unverified here |
| Browser/mobile/race/deployed checks | Not run | Outside phases 0–2 |

## Recommended order

1. Make the core gate green: add the two `playlist-from-collection-*` IDs to path normalization, rerun `make parity`, and require 193 executed and 193 passed.
2. Resolve the ratchet: remove or replace `items.Object`, or approve a documented measurement-corrected baseline, and require `make ratchet` to pass.
3. Regenerate the route inventory after the code is final and make `handover.md`/task claims match the command outputs.
4. Before Phase 6, replace the shape/`any` boundary with three concrete builders and one media accessor; remove the per-client and per-item JSON round-trips. Do not use the current pilot as the template for wider DTO conversion.
5. Regenerate the book golden with non-null ebook, non-null cover, and null duration; add the missing cases and error checks.
6. Give scanner tests a temporary metadata path and guard `WriteItemMetadata` against an empty path; remove the untracked test artifacts.
7. Commit a durable schema-2 baseline or an inventory manifest that enforces the no-fewer-cases rule, and provide a generated status artifact for a clean checkout.

The migration's value at this point is in the verification infrastructure, not in the DTO layer. The infrastructure is worth keeping. The DTO layer needs one representation, not one type plus four conversion patterns, before it is worth extending.

## Remediation applied after this review

- V2: added `playlist-from-collection-create`/`-read` to the fixture-path normalization set. Fresh core parity now executes 193/193 and the committed schema-2 baseline compares cleanly.
- V3: replaced the three per-shape metadata maps with one shape-specific map and removed the `items.Object` alias. `make ratchet` now reports 1126 against the unchanged baseline; no baseline reset was needed.
- V4: regenerated `contracts/parity/route-coverage.json`; `--check` and the route-catalog tests pass.
- V6: added a sixth source fixture with a non-null ebook/`ebookFormat`, non-null cover, null duration and empty tags; the Go golden/dual-run test passes.
- V7: scanner tests now use a temporary metadata root and `WriteItemMetadata` rejects an empty metadata path. The 3,995 tracked test sidecars were removed from the tree.
- V8: added `contracts/parity/baselines/core-schema2-no-webp.json` (193/193) so `make parity` compares against an accepted result instead of bootstrapping every run.
- V9: `userapi.New` now returns a construction error for a nil database or reader; the touched batch-chapter decode path returns an error instead of silently continuing.

Remaining: V1/V5 (one typed representation, no per-consumer JSON round-trips) is deferred to a pre-Phase-6 packet. `make parity-full`, backup restore, browser/mobile/race checks and Ubuntu CI were not rerun. One `make parity` run flaked on `socket-connect-legacy` (Go 599) and the immediately repeated run passed; that flake deserves separate attention.
