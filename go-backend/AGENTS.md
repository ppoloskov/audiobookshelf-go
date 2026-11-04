# Agent guide: Go backend

The root `agents.md` points here. This file owns workflow; permanent contracts
live in [ARCHITECTURE.md](ARCHITECTURE.md). Read those, then [ROADMAP.md](ROADMAP.md)
and [handover.md](handover.md), before selecting work.

## North Star and boundaries

Replace TypeScript Audiobookshelf v2.37.2 in an existing installation, with
clients, books and progress, using simple, clear, fast Go.

- Fixed: SQLite schema and stored meaning/formats, `/config` and `/metadata`
  compatibility, rollback to TS v2.37.2, and protocols real clients require.
- Internal structure, SQL, DTO construction and framing may change when those
  boundaries are preserved. HTTP caching/range semantics are observable.
- `server/` is read-only behavioral reference at
  `edb1f7efc92ecacdef51aafb4d9d5054ce10604f`; never copy its architecture by default.
- Never run TS and Go on the same writable database or metadata tree.

## Architect and worker policy (owner, 2026-11-04)

- Expensive models own architecture, planning, review and final acceptance.
  Delegate bounded source research, implementation and mechanical edits to
  cheap capable workers. Prefer `opencode2` with an available `*flash` model
  when the configured provider is permitted to receive the assigned material.
  Inspect `opencode2 models` and `opencode2 run --help`; do not guess CLI flags
  or a model name. Built-in cheap workers are the fallback.
- Book-read B01–B09 and query instrumentation I01 are implemented/reviewed.
  Next: I02 batched book reads, then I03–I06 and L01 onward. Dispatch one card, not
  the whole epic. Relevant compatibility checks accompany each slice; no
  requirement to complete unrelated client research first.
- Give each worker one task ID, exact read/write scope, fixed contracts,
  dependencies, named tests/commands and a short completion format. Workers
  do not change architecture, expand scope, commit, publish or deploy.
- Keep context small: relevant symbols and contract rows, no whole-repo dumps
  or all-skill loading. Parallelize only non-overlapping work with a clear gain.
- The architect reviews the diff and behavioral evidence before acceptance.
  Reuse worker validation logs; rerun checks when evidence or code changed.
- Current authorization (2026-11-04): implement the roadmap using cheap
  workers, review completed batches and push reviewed ready features to the
  fork. Use a feature branch; no default-branch merge/deployment implied.
  Route removals, new libraries and live acceptance/cutover retain separate
  owner decisions. Do not infer those approvals from implementation authority.
- Owner requested review after workers finish. Sequential cards may form one
  coherent feature batch; keep their individual acceptance checks, avoid
  temporary adapter churn, and review once the batch is ready. Do not push
  an intermediate tree with known compile/test or compatibility failures.

## One current execution record

Every implementation card in `ROADMAP.md` must have an explicit `- [ ]`
status checkbox. On acceptance, change it to `- [x] DONE` in the **same commit**
as the completed work, with actual check results and unresolved limitations.
A status paragraph or summary table alone does not mark a task done. Pending
and in-progress cards stay unchecked. Reconcile this before every commit/push.

`ROADMAP.md` is the active task queue; update status, actual validation,
remaining risks and next action there and in `handover.md` after each packet.
Update README and this guide when their statements change. Documentation is
part of completion, never a later cleanup task. Link history under `archive/`;
never use a dated review as live status. Generated `STATUS.md` reports only
its dated sampled differential run.

Use existing task IDs and records. Do not recreate archived JSON capability
ledgers, duplicate the roadmap or impose whole-upstream parity gates. See
[SKILL-REVIEW.md](SKILL-REVIEW.md) for the reviewed Go-skill overrides. Load
only the specialist needed by the current packet. Record contract and Go
simplicity review separately, proportionate to risk; two model calls are not
mandatory for every edit. After two failed worker repairs of one issue,
return its evidence to the architect; continue independent authorized work.

## Verification

Documentation-only packets: check links, task dependencies, contradictory
claims, generated-file boundaries and `git diff --check`. Runtime suites are
not required for prose-only edits; disclose what was not run.

For code packets, begin with the named regression and affected packages;
then run the required integration checks before marking the packet accepted:

| Check | Command / evidence |
|---|---|
| Build, vet, tests | `cd go-backend && go build ./... && go vet ./... && go test ./...` |
| Lint/dependency boundaries | from `go-backend`: `golangci-lint run --config=../.golangci.yml ./...` |
| Metrics and no nested pool queries | root: `make ratchet` |
| Generated typed queries, if inputs/callers changed | root: `make sqlc`; compare tracked **and untracked** generated outputs |
| Rollback after persistence changes; CI image gate | CI `rollback`: TS v2.37.2 reopens Go-written data |
| Protocol changes | named value/state tests; real Socket.IO library interop must execute, not skip |
| Concurrency/lifecycle changes | affected packages with `CGO_ENABLED=1 go test -race ...` |

CI currently gates images on `verify` and `rollback`. The Node `parity` job is
advisory since V1; do not make byte differences blocking. Keep diagnostic
inputs reproducible until V2 extraction and acceptance finish.

Metrics are review signals, not a product outcome; 30% fewer handwritten Go
lines is aspirational. **The existing ratchet still rejects increases.** Until
Q1 changes it, a justified increase needs an explicit baseline edit, measured
deltas and review in the same packet. Never silently weaken a correctness or
security guard, or claim a proposed policy is already implemented.

## Code rules

- No schema changes/migrations. DATETIME writes use `dbtime`; preserve JSON
  columns, IDs, enum strings and metadata meaning.
- Preserve client fields, types, meaningful values, nullability, auth, event
  recipients and media semantics. No removal based only on web-client absence.
- No new dependency without owner approval. Reuse approved libraries and
  standard Go facilities before inventing wrappers.
- No pool query while its `*sql.Rows` or `*sql.Tx` is open: the single writer
  can deadlock. `tools/dbnest` remains a correctness gate.
- Favor typed requests/responses, consumer-sized interfaces and cohesive
  ownership. Do not introduce a universal DTO or move packages for line-count optics.
- Remove replaced code when safe; retain a temporary bridge only with named
  consumers, a removal condition and a roadmap task.
- Preserve unrelated dirty work. One bounded packet per commit, only when
  committing is authorized. Never report a skipped/unrun check as passing.

## Operational commands

Run from root: `make ratchet`, `make bench`, `make client-usage` (writes the
inventory), `make parity-replacement`, `make parity-scan`, `make route-probe`.
The last three are advisory, need Node 22 and a built TS reference, and mutate
disposable fixtures. Run route-probe last. Without mise use `PARITY_NODE=node`.
Local image-source samples need `SSRF_REQUEST_FILTER_WHITELIST=127.0.0.1`.
