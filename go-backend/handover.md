# Go backend handover

## Independent review checkpoint

See [PHASES-0-2-REVIEW-2026-10-31.md](PHASES-0-2-REVIEW-2026-10-31.md) for the original findings, and [the remediation task](../docs/agent-work/tasks/TASK-20261031-phase-review-remediation.md) for subsequent fixes and verification. Keep the dated review as evidence; generated [STATUS.md](STATUS.md) owns the latest sampled core result.

The shared worktree has pre-existing uncommitted work. No release/freeze tag was created from only HEAD, because that would omit the implementation being reviewed; run provenance instead fingerprints the tested worktree. Tagging belongs with a deliberate commit of that work. No new F-packets were started during remediation.

The active engineering sequence is in `ROADMAP.md`. Phase 0b consolidation and Phase 1 ratchet, dependency checks, and CI wiring are implemented in the current worktree. `AGENTS.md` and `ARCHITECTURE.md` remain the source of working and structure rules. Do not copy current case totals into durable docs; use generated `STATUS.md` for status.

## Harness work in progress

- Shared Node utilities are under `tools/parity/lib/` for process/port handling, HTTP and login, argument parsing, artifact writing, comparison/normalization, fixture helpers, socket probes, and case data. The remaining fixture/state/rollback `request` wrappers now use the common HTTP adapter.
- `tools/parity/cases/core.json` combines executable core descriptors with the prior route/category registry metadata, so the engine has one core case catalog. The older `contracts/parity/differential-cases.json` is retained as a historical artifact and is no longer loaded by the runner. Local-session, bookmark, notification, and full-core workflow modules are indexed by `tools/parity/cases/steps.json`; OIDC web/mobile flows and compared results are indexed by `tools/parity/cases/oidc-steps.json` and `tools/parity/cases/oidc.json`, with step modules under `tools/parity/cases/steps/`.
- The root `Makefile` exposes `make parity` (core) and `make parity-full` (all named API, persistence, rollback, restore, load, and operational suites). `tools/parity/suite-runner.cjs` rebuilds the Go binary before comparisons and keeps each suite's fixture and output under one temporary work directory. The F07 snapshot target passed its standalone runner check.
- `differential.cjs` is now a short orchestrator below the roadmap's approximate size target. Specialized OIDC provider/persistence checks remain a separate suite entrypoint; the flow sequence and compared-case catalog are data-indexed.
- `tools/ratchet/` measures Go architecture counts with `go/ast` and parity helper/call counts with regex. `ratchet-baseline.json` is the active floor; `make ratchet-update` refuses to raise an existing count. `.golangci.yml` enforces package dependency direction and production-only import restrictions through depguard.
- The catalog library-folder cleanup now calls the underlying item deletion operation directly. This removed production `httptest` self-dispatch while retaining the same HTTP handler response path.
- `internal/items.Reader` is the shared item-serialization boundary. `server` creates one `catalog.Handler` and injects it into `scanner` and `userapi`; those feature packages no longer import `catalog` directly.
- The retired Python ledger tools are retained under `../docs/agent-work/archive/parity-tools/`; `build_route_coverage.py --check` remains active. The checked-in route inventory was refreshed from current source and the check passes.
- Preserve the pre-existing macOS fixture path canonicalization in `tools/parity/create-fixture.cjs` and its corresponding environment guidance changes in `AGENTS.md`.

## Verification gotchas

- Use Node 22 with `mise exec node@22 -- node ...`; Node 26 is not the supported upstream harness runtime.
- Differential tests bind local loopback servers. Restricted runs may fail with `listen EPERM`; run with loopback permission rather than interpreting that as a harness failure.
- Set `SSRF_REQUEST_FILTER_WHITELIST=127.0.0.1` for fixture and local differential runs.
- Rebuild the Go executable from current source before every differential run, and create a new fixture for each run because cases mutate state.
- In restricted environments, Go's default cache may be unwritable; use a task-specific `GOCACHE` under `/private/tmp`.
- Compare case IDs, outcomes, and normalized body digests. Cross-run bodies carry fixture UUIDs, paths, timestamps, SMTP ports, and dates that need fixture-specific normalization for a digest comparison; do not widen runtime comparison rules to make the Node/Go pair pass.
- Historical full-suite runs saw a rollback Socket.IO timeout; a separate fresh rollback run passed. The phase-review backup mismatch was traced to comparing a Go-rewritten media copy against a Node-authored archive with different real paths/inodes. The restore test now starts both independent targets from copies matching that source archive, retains exact API-body equality, changes the title and deletes metadata before applying the archive, and requires restoration. This changes the fixture, not the backup runtime. `f07-rollin-check` requires a self-contained snapshot root, which the runner builds from a fresh fixture.
- `make status` now regenerates `STATUS.md` from the sanitized latest-core-run evidence. `make parity` records fresh source digests and the encoder profile; `make parity-equivalence` compares independent fixtures. Schema-1 reports remain historical: they lack the provenance and verdict fields required by the schema-2 equivalence gate. Do not overwrite them to make the new comparator pass.
- The OIDC suite's provider HTTP server must close idle and active keep-alive connections after the flow; otherwise the suite writes its report but leaves Node alive.

## Book wire pilot

- `internal/catalog/book_wire.go` owns the typed SQL row loader and builders; `internal/wire/book.go` defines the three book media envelopes. The nested metadata, files, chapters, ebook, and track JSON remains opaque by design.
- A typed envelope stored inside an existing `map[string]any` item is still a concrete Go struct. Consumers must either use that DTO or deliberately decode it at a map-oriented boundary. Access-control filtering, realtime filtering, scanner metadata writing, playlist removal, and the batch chapter handler account for this distinction.
- Source serializer outputs and edge fixtures are maintained by `tools/book-item-oracle.cjs`; verify them with `mise exec node@22 -- node tools/test-book-item-oracle.cjs` and the catalog golden/dual-run test. Do not regenerate the golden to make a Go mismatch pass; update it only when the pinned source fixture or source commit changes.
