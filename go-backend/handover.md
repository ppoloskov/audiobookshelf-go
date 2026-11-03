# Go backend handover: current gotchas

Process and checks are in [AGENTS.md](AGENTS.md), order of work in
[ROADMAP.md](ROADMAP.md), status in generated [STATUS.md](STATUS.md). This file
holds only what trips people up now. Dated reviews are in
[archive/reviews/](archive/reviews/) and are evidence, not current state.
Earlier handover and status documents are in [archive/](archive/).

## Linux and worktrees

- The Makefile `PARITY_NODE` defaults to `mise exec node@22 -- node` when
  `mise` is installed and to plain `node` otherwise (override with
  `make PARITY_NODE=... parity`). Use Node 22.x; Node 26 breaks the upstream
  dependency tree.
- A fresh git worktree has none of the untracked build outputs. Before any
  harness run it needs `node_modules` (`npm ci`), `dist-server`
  (`npm run build:server`) and `client/dist` (build in `client/`), or symlinks
  to a checkout that has them.
- `make browser-check` (Chromium web-client acceptance) is portable: it uses a
  fresh temp output dir (`BROWSER_CHECK_OUT` to override), creates its own
  fixture unless `--fixture` is given, and starts upstream with the running Node
  (`ABS_PARITY_NODE` to override). It needs `playwright` + Chromium. It never
  rewrites the checked-in browser contract unless `--write-contract` is passed.
  Other harness tools (`acl-differential`, `f07-rollin-check`,
  `operational-milestone`, `cascades-differential`) still spawn `mise`; on Linux
  put a `mise` shim on `PATH` that execs `node` when running them.

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
- `make route-probe` mutates its work dir (`install` and `media`), including
  deletes of the seeded entities. Run it last, on a work dir that is not reused
  for a trial. CI runs it last for that reason. A failure names each route that
  reached the 501 fallback.

## Code gotchas

- A typed `internal/wire` book envelope stored in a `map[string]any` item is
  still a concrete struct. Consumers must use the DTO or deliberately decode it
  at a map boundary. Access-control filtering, realtime filtering, scanner
  metadata writing, playlist removal and the batch chapter handler already do.
- Source book serializer outputs come from `tools/book-item-oracle.cjs`; check
  with `node tools/test-book-item-oracle.cjs`. Do not regenerate the golden to
  make Go pass; only when the pinned source fixture changes.
- `internal/items.Reader` is the shared item-serialization interface. `server`
  builds one `catalog.Handler` and passes it to `scanner`, `userapi` and
  `playback` as that interface.
- Socket.IO `search_covers` and `cancel_cover_search` have no dispatcher case
  yet (`internal/server/runtime.go`), so a socket cover search returns nothing,
  without an error. The HTTP `/api/search/covers` works. ROADMAP C5.
- `tools/parity/create-fixture.cjs` canonicalizes macOS `/private` fixture
  paths; keep that when editing it.
