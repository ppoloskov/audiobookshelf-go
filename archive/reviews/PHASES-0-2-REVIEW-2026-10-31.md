# Independent review of phases 0–2

Reviewed 2026-10-31, Australia/Brisbane. Result: **revise before extending the migration**.

Scope: the current worktree, including uncommitted and untracked Phase 0/0b harness changes, Phase 1 guardrails and CI, Phase 2 book DTOs, their consumers, and pinned upstream serializer/controller behavior. HEAD was `718cc0a0`. This is dated review evidence, not generated project status. The reviewer did not implement these changes. Contract and simplicity passes were performed separately by the same reviewer; no separate-agent review is claimed.

## Value and architecture assessment

The strongest improvements are a repeatable fresh-fixture comparison command, negative tests of the comparison tool, shared harness utilities, and removal of hidden `catalog.Handler` construction from feature packages. Manual composition and a small consumer-facing `items.Reader` interface are proportionate. The interface currently remains implemented by `catalog.Handler`; the serialization implementation has not moved to a concrete `items` reader. That is a reasonable transitional seam, but the architecture decision is only partly implemented.

The source-executed golden is also valuable: it checks three book shapes against actual pinned serializer methods, rather than treating another Go function as the entire oracle. Separate wire envelopes express omitted fields more clearly. Keeping nested objects opaque is consistent with D5.

The weakest part is the connection between these improvements. Typed books travel inside `map[string]any`, through an API returning `any`, and then become maps again in authorization, realtime, metadata writing, and audio-tag generation. Existing consumers can still compile while silently dropping data. Finding R1 demonstrates this failure. The measured map decrease alone does not establish that the architecture is safer, simpler, faster, or ready for clients.

## Findings

### R1 — P1 / blocking: collection-to-playlist conversion silently loses every book

Evidence: `internal/userapi/playlist.go:830–832` asserts `item["media"]` to `map[string]any` and continues on failure. `collectionDTO` obtains those values from `ItemJSON(..., true)`, which now supplies `wire.BookExpanded` through `internal/catalog/handler.go:277–284` and `book_wire.go:146–150`.

Observed counterexample: a database containing one accessible book and a one-book collection, using the real `catalog.Handler` injected into the real `userapi.Handler`, receives `POST /api/playlists/collection/c1`. The response is HTTP 200 with `"items":[]`; `playlistMediaItems` contains zero records. A temporary Go overlay test requiring one persisted item fails. Upstream `server/controllers/PlaylistController.js:543–551` persists each collection book's media ID.

Smallest repair: adapt this consumer to the actual book representation and add a handler-level regression that checks response items, persisted IDs/order, and the emitted playlist. Add the corresponding upstream differential case. Do not treat successful HTTP status as sufficient acceptance.

### R2 — P1 / blocking: dependency guard does not protect the intended packages

Evidence: `.golangci.yml:12–13`, `18–25`, `31–33`, and `45–60` select directories with patterns such as `internal/wire/**/*.go`. The actual packages put their Go files directly in those directories.

Observed counterexample: an isolated module with the same module name, the unchanged configuration, and deliberately forbidden `wire -> catalog` and `scanner -> catalog` imports exits 0 with `0 issues` under the pinned golangci-lint 2.15.0. A control rule using `$all` rejects both. A rule matching direct scanner files with `**/internal/scanner/*.go` rejects the scanner import, with or without a description. Thus the clean lint run is not evidence that these boundary rules are effective. The production-wide `httptest` prohibition is a separate rule and is not implicated by this counterexample.

Smallest repair: use tested path patterns that match direct files and nested files in the actual CI working directory. Include negative fixtures for wire, feature, leaf and production-test imports, plus positive fixtures for allowed dependencies. See the [official depguard configuration](https://golangci-lint.run/docs/linters/configuration/#depguard).

### R3 — P1 / blocking: a requested case can disappear while the run passes

Evidence: `tools/parity/differential.cjs:344–372` returns from focused mode after only the authentication workflows and four direct-file cases. It calculates totals from the results it happened to produce, without reconciling them against requested IDs. Full mode similarly calculates `total` from `reportCases.length` at line 396; `fullRunIsComplete` compares those self-derived totals, not an expected case inventory.

Observed counterexample with identical local stub servers: requesting `--cases login,playback-session` exits 0, reporting one executed/passed case, with only `login` in the report. Requesting playback alone fails the zero-case check, so the existing self-test misses partial omissions. Missing dispatch code in full mode is likewise not detected by its count comparison alone.

Smallest repair: reconcile expected/requested IDs with executed IDs, reject omissions and duplicates, and route supported focused cases through their named workflows. Keep a negative test for partial execution, not just zero execution. Enforce the no-fewer-cases rule from an independent expected set.

### R4 — P2 / should fix: report equivalence ignores verdicts and state checks

Evidence: `tools/parity/compare-reports.cjs:114–135` compares side statuses, headers, bodies and the normalization declaration, but omits case `passed`, `skipped`, errors and report `statefulChecks`.

Observed counterexample: change a case from passed to failed/skipped and a state check from equal to unequal, preserving its recorded bodies/statuses. `compareReports` still returns `passed:true`. This does not fulfill Phase 0b's requirement to preserve pass/fail results.

The documented comparison command also fails on the fresh passing core report against `baselines/baseline-88db747.json`: both have the same case IDs and outcomes, but JWT fingerprints, HTTP date/ETag values, and playback calendar fields differ across fixture runs. These are observed differences in both implementations, not proof of a Go regression. No automated baseline comparison is called by `make parity` or CI.

Smallest repair: compare outcomes and every authoritative state check; validate report schemas and nonzero execution. Make repeatability explicit through deterministic inputs or narrowly documented fixture provenance. Do not expand generic normalization to erase semantic differences. Test the comparator on independently generated real reports, and exercise it where the equivalence gate is required.

### R5 — P2 / should fix: map aliases bypass the ratchet

Evidence: `tools/ratchet/main.go:144–170` discovers alias names but increments the metric only when visiting an `ast.MapType`. It never counts alias type uses or resolves imported selectors. Alias names are also accumulated across packages without package identity.

Observed counterexample: `type Object = map[string]any; var First Object; var Second Object` measures one map, rather than the declaration plus the two dynamic-map type uses. Replacing repeated explicit maps with alias uses therefore lowers the metric while retaining the same representation. The current `items.Object` API already uses a map alias.

Smallest repair: define precisely what the metric measures, count aliases consistently with package/type identity, and add direct/chained/imported-alias negative fixtures. Re-establish the baseline with the corrected measurement. Keep ratchet counts as a regression aid, not architectural acceptance.

### R6 — P2 / should fix: Phase 0's single current status source does not exist

Observed: `make status` exits 2 with `No rule to make target 'status'`; `go-backend/STATUS.md` is absent. Root guidance and README refer to that nonexistent generated source. `go-backend/README.md:7–9,22` still identifies the superseded finalization queue/F09 as active, although `ROADMAP.md` supersedes that sequencing and freezes new F-packets during Phase 0. No local tag exists for the planned freeze.

Consequence: a cold-start maintainer receives conflicting next actions and cannot obtain the promised current status. The handover already acknowledges the missing generator; this review confirms the gap rather than claiming it is newly introduced.

Smallest repair: implement one generator/target from authoritative harness artifacts, link to it, remove competing active-status prose, and explicitly resolve remaining freeze/hygiene tasks. Preserve historical reports and ledgers as evidence.

### R7 — P2 / should fix: the required generated-inventory check currently fails

Observed: `python3 go-backend/tools/parity/build_route_coverage.py --check` exits 1: `route coverage ledger is stale; regenerate with build_route_coverage.py`. Generating to a temporary path identifies the stale `internal/catalog/handler.go` hash. CI runs this check before the Go checks, so the current worktree is not green despite the Phase 2 completion claim.

The generator also still reads `contracts/parity/differential-cases.json` at line 35, while the runner reads `tools/parity/cases/core.json` and the handover calls the former historical. This leaves a second active case-metadata source after consolidation.

Smallest repair: refresh generated output through its generator after final code changes. Make inventory metadata consume the executable catalog, or explicitly define and verify the relationship. Do not hand-edit the generated hash or promote coverage claims.

### R8 — P2 / should fix: CI parity assumes this host's broken WebP capability

Evidence: `tools/parity/differential.cjs:59–64` requires HTTP 500 for both WebP cover cases specifically because this host's FFmpeg lacks libwebp. Its own comment states that a capable build returns 200 on both sides. `lib/compare.cjs:547` requires the hardcoded expected status as well as agreement. CI installs its own FFmpeg with apt and does not pin the host capability.

Consequence, inferred from the comparison logic: an environment in which both servers successfully produce WebP still fails the parity gate. The current local core pass does not qualify the Ubuntu CI behavior; Ubuntu CI was not executed in this review.

Smallest repair: declare and verify the FFmpeg capability profile, then assert the appropriate success or failure contract for that profile. Retain a successful cover-rendering test so matching failures cannot stand in for a working client workflow.

## Phase 2 follow-up before expanding DTO conversion

- Prefer concrete shape-returning builders over `typedBookEnvelope(..., shape string) (any, error)`. Load the shared row/relations once; construct only the requested shape.
- The current builder calls `titlePrefix` twice for every shape (`book_wire.go:71,88`); each call queries settings (`handler.go:732–745`). It also builds and marshals unused metadata shapes. This adds work to the pilot; no performance improvement was measured. Remove the avoidable repeated work without adding a cache or a new framework.
- Choose one documented transitional representation for each shared API. Avoid distributing map/struct switches and JSON round trips to every consumer. Keep wire encoding at a deliberate boundary and migrate consumers in complete slices. No generic repository layer or DI container is needed.
- Add non-null ebook/`ebookFormat`, non-null cover, nullable duration, and typed-payload ACL/event fixtures. All five source-golden books currently have a literal `null` ebook column, so they cannot validate a present ebook format.
- The dual-run test compares the old book helpers, not the entire old `ItemMinifiedJSON` entrypoint. That entrypoint previously returned extra `libraryItemId`/`tracks` fields; the new source-backed minified shape omits them. Preserve the upstream-correct result, but describe this as a characterized behavior correction rather than unchanged Go output.

## Verification and compatibility matrix

All checks below were executed against the reviewed worktree, before this documentation was added. Go was 1.27.0 locally; the workflow's Go 1.26.1 environment was not independently executed.

| Boundary/check | Observed result | Practical limit |
| --- | --- | --- |
| `CGO_ENABLED=0 go build ./...` and `go vet ./...` | Exit 0 | No deployment or architecture-specific build claim |
| `CGO_ENABLED=0 go test ./...` | Exit 0 with local listener permission | Existing tests miss R1 |
| Harness integrity self-tests | All eight pass | Miss partial omissions, R3 |
| Saved-report comparator self-tests | All eight pass | Miss verdict/state regressions, R4 |
| Source-executed book/item golden | Exit 0; three shapes for five fixtures | Synthetic book corpus; no non-null ebook |
| Fresh `make parity` | Exit 0; all 190 cases execute and pass | Sampled core behavior only |
| `make ratchet` | Exit 0 | Alias bypass, R5 |
| Pinned depguard on current source | Exit 0 | Forbidden-import fixture also passes, R2 |
| Route inventory `--check` | Exit 1 | Stale generated hash, R7 |
| `make status` | Exit 2 | Missing target/file, R6 |
| Fresh named backup-restore suite | Exit 1 | Node checks pass; Go library/item equality checks fail |
| Independent collection-to-playlist overlay regression | Fails | Confirms R1 with real handlers and persisted rows |
| Independent alias-count overlay regression | Fails | Confirms R5 |
| Independent partial-selection stub probe | Incorrectly exits 0 | Confirms R3 |
| Independent verdict/state-change comparator probe | Incorrectly reports equivalent | Confirms R4 |

The backup test reproduces the already documented operational gap: Node passes every recorded check; Go accepts/applies the archive and restores metadata files, but `librariesUnchanged` and `itemDetailUnchanged` are false. This establishes response mismatch, not physical database data loss. The cause was not isolated here and is not attributed to Phase 2. Treat restore acceptance as open. Full `make parity-full`, browser/mobile acceptance, race suites and deployed behavior were not rerun.

Fresh reports are local disposable evidence:

- Core: `/private/var/folders/jt/2prvbq7965ggn35rz2vwnqrw0000gn/T/abs-parity-l4ZbKG/core-fixture/core-report.json`.
- Backup pair: `/private/var/folders/jt/2prvbq7965ggn35rz2vwnqrw0000gn/T/abs-parity-BQScgz/test-backup-restore/{node,go}-report.json`.
- Review probes and Go overlays were created only under `/private/tmp/abs-review-*`; no production code or tracked tests were changed. Temporary artifacts are not durable completion gates.

## Decisions and repair order

Upstream v2.37.2 remains the behavioral oracle. `ARCHITECTURE.md` owns intended structure; `ROADMAP.md` owns sequencing. Historical ledgers and a passing metric do not override those sources. No compatibility exception, new dependency, schema change, or replacement architecture is recommended.

1. Repair R1 and add a differential witness for the real workflow.
2. Repair R2–R4 with negative tests that demonstrate the guardrails actually fail.
3. Correct R5/R7 and close Phase 0's status/source-of-truth gaps. Resolve the FFmpeg profile before relying on CI portability.
4. Simplify the pilot's representation boundary and unused construction before using it as the template for Phase 6.
5. Re-run the relevant gates; separately investigate the existing restore mismatch. Resume the next roadmap slice only with an honest account of remaining acceptance gaps.

Next workflow: bounded implementation-scope control with behavior regression tests for these findings, followed by verification. This review does not authorize or perform publishing, deployment, or broad refactoring.
