# Agent guide: Go backend

This is the only agent instruction file (root `agents.md` points here). Rules
here point at a check; design guidance is in `ARCHITECTURE.md`.

## Goal

Replace the TypeScript Audiobookshelf server (v2.37.2) in an existing
installation, with clients, books and progress, using simple, clear, fast Go.

- **Fixed contracts:** the SQLite database (schema, value formats, meaning;
  TS v2.37.2 must still open it) and the client protocols (HTTP fields and
  types, Socket.IO events, auth flows that clients use).
- **Everything else is replaceable:** structure, SQL, framing, key order,
  error text no client parses, logs.
- Upstream `server/` (pinned `edb1f7efc92ecacdef51aafb4d9d5054ce10604f`) is a
  reference for meaning. Never edit it. Do not copy its structure.

## Read first

1. This file.
2. `ARCHITECTURE.md`: principles, contracts, target layout, libraries.
3. `ROADMAP.md`: pick one unchecked task. Its "Done when" is the acceptance
   check.

## A task is done when

| # | Check | Command |
|---|-------|---------|
| 1 | Builds, vets, tests pass | `cd go-backend && go build ./... && go vet ./... && go test ./...` |
| 2 | Lint and dependency rules | `golangci-lint run --config=../.golangci.yml ./...` |
| 3 | Ratchet: no metric rises; lower the baseline when one falls | `make ratchet` |
| 4 | Contract, persistence and functional tests pass (part of check 1 once ROADMAP F4-F7 land) | `go test ./...` |
| 5 | Rollback: TS v2.37.2 reopens a database Go wrote | the CI rollback job |
| 6 | Until ROADMAP V1: the replacement and scan trials pass | `make parity-replacement`, `make parity-scan` |
| 7 | Generated code is current | `make sqlc` leaves no diff (after ROADMAP L5) |

Rules:

- Refactors must lower handwritten lines or complexity and raise no metric.
  Say the deltas in the commit message.
- A trial difference (check 6) that is only in a "free" area of
  `ARCHITECTURE.md` (bytes, key order, headers, error text no client parses)
  may be normalized in the harness, with the reason in the commit message.
  A difference in a field a client reads, a status a client branches on, a
  stored value, or an event payload must be fixed.
- Never claim a check you did not run. Say what you did not run.
- Commit message: what changed, which contract it touches (if any), deltas,
  what is unverified.

## Rules

### Contracts

- Never change the schema or migrate an existing database. Write DATETIME
  values only through `dbtime`; keep JSON columns, ID formats and enum strings
  as TS writes them.
- Keep a client-visible field, type or `null` unless ROADMAP R2 approved its
  removal.
- Never run TS and Go against the same writable database or metadata tree.

### Code

- Follow `ARCHITECTURE.md`: typed request/response structs, routes on
  `ServeMux`, constructor injection, one owner per concern.
- No new dependency without owner approval (see the library table in
  `ARCHITECTURE.md`). Prefer an approved library over handwritten code.
- No query on the pool while a `*sql.Rows` or `*sql.Tx` is open (`tools/dbnest`
  fails the build).
- Delete code you replace in the same commit. Do not leave compatibility shims.
- One task per commit. List unrelated findings in the commit message instead of
  fixing them.

## Commands

From the repository root unless noted.

```sh
cd go-backend && go build ./... && go vet ./... && go test ./...
make ratchet                 # metrics and dbnest
make parity-replacement      # until V1; needs Node 22 and `npm ci && npm run build:server`
make parity-scan             # until V1
make route-probe             # until V1; run last, it mutates its work dir
make bench                   # performance snapshot
```

Node 22.x is needed only for the TS reference server (trials and the rollback
check). Without `mise`, pass `PARITY_NODE=node` to `make`. Local image-source
samples need `SSRF_REQUEST_FILTER_WHITELIST=127.0.0.1`.

## When blocked

Stop and report the command, its output and the smallest fix you would
propose. Do not weaken a test of a fixed contract to make it pass.
