# Agent guide: Go backend for Audiobookshelf v2.37.2

This is the only agent instruction file. Root `agents.md` is a pointer to it.
Keep it short; rules here must point at a check. If a rule cannot be checked
by a command, it belongs in `ARCHITECTURE.md` as guidance, not here.

## Goal

A drop-in replacement for pinned upstream **v2.37.2**
(`edb1f7efc92ecacdef51aafb4d9d5054ce10604f`): same observable HTTP,
Socket.IO, SQLite, and filesystem behavior, with idiomatic Go inside.

The pinned TypeScript server is the **behavior oracle**. It is never the
architecture template. Do not edit it.

## Read first, in this order

1. This file.
2. `ARCHITECTURE.md`: the target structure and the rules new code follows.
3. `ROADMAP.md`: the current phase. Your packet comes from there.

Status lives in `STATUS.md`. It is generated from harness output. Never edit
it by hand, never copy status numbers into other files, and do not report
status from memory of an older document.

## A packet is done when all of these hold

| # | Check | Command | State |
|---|-------|---------|-------|
| 1 | Builds, vets, unit tests pass | `cd go-backend && CGO_ENABLED=0 go build ./... && CGO_ENABLED=0 go vet ./... && CGO_ENABLED=0 go test ./...` | exists |
| 2 | Differential passes, **with at least one case executed** and no fewer cases than before | `make parity` | planned (Phase 0) |
| 3 | Ratchet passes, baseline updated in the same commit | `make ratchet` | planned (Phase 1) |
| 4 | New or changed behavior has a differential case or a golden test | n/a | review |
| 5 | Commit message states: what changed, which upstream `file:line` it matches, what is still unverified | n/a | review |

Rules for the table:

- A differential run that executes 0 cases is a **failure**, not a pass.
- Until `make parity` exists, run the steps in "Commands" by hand and paste the
  case counts into the commit message.
- Refactor packets must **lower** at least one ratchet metric and raise none.
  Parity-fix packets may not raise any.
- Never claim parity for a route or variant you did not exercise. Say what you
  did not run.

## Rules

### Behavior (contract)

- Fix only differences you can reproduce against upstream and trace to source.
- Express quirks are contract, not bugs: weak ETags, text bodies without a
  trailing newline, English `sendStatus` texts, passwordless root, `null` vs
  empty vs absent. Do not "fix" them. They live in `internal/httputil` only.
- Never run Node and Go against the same writable database, config, metadata,
  or media tree. Mutating cases consume their fixture; create a fresh fixture
  for each clean run.
- An existing database must match the pinned migration version. Never create or
  migrate a schema implicitly on an existing database.
- No Node fallback in the Go runtime. FFmpeg and ffprobe run as subprocesses
  through the `media` runner only.
- Normalize only documented fixture-dependent IDs, paths, timestamps, and
  inodes. Every normalization needs a written reason.

### Code (structure)

- New code follows `ARCHITECTURE.md`. In particular:
  - Typed request and response DTOs live in `internal/wire`. Do not add
    `map[string]any` to request or response paths.
  - Register routes on `http.ServeMux` with method patterns. Do not add
    hand-rolled `parts[N] == "x"` matching or new `Handle(...) bool` chains.
  - Pass collaborators to `New` through a deps struct. Do not add `Set*`
    injectors. `New` has no side effects; use `Start` and `Close`.
  - If a helper exists in a leaf package, use it. If it does not, add it to the
    leaf package, not to your feature package.
- One behavior per packet. Edit only the paths your packet names.
- Do not fix unrelated things on the way past. List them in the commit message
  or packet report.
- Do not rewrite ledgers, status files, or generated reports by hand.

## Commands

Run from the upstream repository root unless noted.

```sh
# Upstream (reference server), once:
npm ci && npm run build:server           # use Node v22.x; Node 26 breaks the dependency tree

# Go build (from go-backend/):
CGO_ENABLED=0 go build -trimpath -o audiobookshelf-go ./cmd/audiobookshelf-go

# Fresh fixture (one per clean run; mutations consume it):
node go-backend/tools/parity/create-fixture.cjs /tmp/abs-fixture-$(date +%s)

# Differential (rebuild the Go binary first; stale binaries gave false failures):
node go-backend/tools/parity/run-local.cjs \
  --fixture /tmp/abs-fixture-XXXX/fixture-manifest.json \
  --go-bin ./go-backend/audiobookshelf-go \
  [--cases id1,id2]
```

Planned targets (the roadmap creates them): `make parity`, `make ratchet`,
`make ratchet-update`, `make status`.

Environment notes:

- Local image-source samples need `SSRF_REQUEST_FILTER_WHITELIST=127.0.0.1` and
  loopback permission.
- Copied fixture files get new inodes; re-stat before comparing.
- The fixture path rewriter must handle JSON-valued settings columns.

## When blocked

Stop. Report the exact command, its output, and the smallest repair you would
propose. Do not work around a failing check by changing expected values,
widening normalization, or narrowing the case list.

## Environment

On macOS, `/tmp` is a symlink to `/private/tmp`. The upstream file walker resolves
media paths to `/private/tmp`, so running `create-fixture.cjs` with a `/tmp/...`
fixture directory produces incorrect relative media paths and the book scan adds
zero items. Use a canonical fixture path, for example
`/private/tmp/abs-fixture-$(date +%s)`, for fixture creation and differential runs.
