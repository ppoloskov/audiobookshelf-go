# Go backend handover: current gotchas

Process and checks are in [AGENTS.md](AGENTS.md), order of work in
[ROADMAP.md](ROADMAP.md), status in generated [STATUS.md](STATUS.md). This file
holds only what trips people up now. Dated reviews are in
[archive/reviews/](archive/reviews/) and are evidence, not current state.
Earlier handover and status documents are in [archive/](archive/).

## Linux and worktrees

- The Makefile defaults to `mise exec node@22 -- node`. There is no `mise` on
  the Linux box: run `make PARITY_NODE=node parity` or call `node` directly.
  Use Node 22.x; Node 26 breaks the upstream dependency tree.
- A fresh git worktree has none of the untracked build outputs. Before any
  harness run it needs `node_modules` (`npm ci`), `dist-server`
  (`npm run build:server`) and `client/dist` (build in `client/`), or symlinks
  to a checkout that has them.
- `tools/parity/browser-check.cjs` (Chromium web-client acceptance) is not
  portable yet: it hardcodes macOS paths and spawns `mise`. On Linux set
  `BROWSER_CHECK_OUT` and put a `mise` shim on `PATH` that execs `node`. Tracked
  as part of the operational workstream in the roadmap.

## Running the harness

- Differential suites bind loopback servers. `listen EPERM` means the sandbox
  blocked loopback, not a harness failure.
- Set `SSRF_REQUEST_FILTER_WHITELIST=127.0.0.1` (the Makefile does).
- Rebuild the Go binary before every run and use a fresh fixture each time;
  cases mutate state. Copied fixture files get new inodes, so re-stat before
  comparing.
- If Go's cache is unwritable, set a task-specific `GOCACHE`.
- Compare case IDs, outcomes and normalized body digests. Do not widen
  normalization to turn a pair green.
- Schema-1 reports under `contracts/parity/` are historical; they lack the
  provenance fields the schema-2 equivalence gate needs. Do not overwrite them.
- The OIDC suite's provider server must close keep-alive connections after the
  flow, otherwise Node stays alive after the report is written.
- The backup restore test starts both targets from copies matching the source
  archive; comparing a Go-rewritten media copy to a Node-authored archive
  differs in paths and inodes by construction.
- Historical full-suite runs saw a rollback Socket.IO timeout once; a fresh run
  passed. Re-run before treating it as real.

## Code gotchas

- A typed `internal/wire` book envelope stored in a `map[string]any` item is
  still a concrete struct. Consumers must use the DTO or deliberately decode it
  at a map boundary. Access-control filtering, realtime filtering, scanner
  metadata writing, playlist removal and the batch chapter handler already do.
- Source book serializer outputs come from `tools/book-item-oracle.cjs`; check
  with `node tools/test-book-item-oracle.cjs`. Do not regenerate the golden to
  make Go pass; only when the pinned source fixture changes.
- `internal/items.Reader` is the shared item-serialization boundary; `server`
  injects one `catalog.Handler` into `scanner` and `userapi`.
- `tools/parity/create-fixture.cjs` canonicalizes macOS `/private` fixture
  paths; keep that when editing it.
