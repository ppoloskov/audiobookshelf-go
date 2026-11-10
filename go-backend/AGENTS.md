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
- Owner clarification (2026-11-11): there is complete freedom to reimplement
  internal subsystems, consolidate modules and adopt smaller library-backed
  designs. Retain behavior only for a persisted-data or client contract, not
  because it mimics TS, Sequelize, Express or upstream structure. Existing
  implementation choices and historical retention decisions are not permanent
  architectural constraints. Evaluate end-to-end replacements and their actual
  adapters; do not assume the current internal mechanisms must survive.
- `server/` is read-only behavioral reference at
  `edb1f7efc92ecacdef51aafb4d9d5054ce10604f`; never copy its architecture by default.
- Never run TS and Go on the same writable database or metadata tree.

## Architect and worker policy (owner, updated 2026-11-06)

- Expensive models own architecture, planning, review and final acceptance.
  Delegate bounded source research, implementation and mechanical edits to
  cheap capable workers. Prefer `opencode2` with an available `*flash` model
  when the configured provider is permitted to receive the assigned material.
  Inspect `opencode2 models` and `opencode2 run --help`; do not guess CLI flags
  or a model name. Built-in cheap workers are the fallback.
- E01 source audit is complete; no thousand-line wave is established.
  Historical cancellation of the Socket.IO replacement initiative does not
  prohibit reassessment under the 2026-11-11 internal-reimplementation
  clarification. The client protocol is fixed; the framing implementation is
  replaceable. Cheap workers own bounded work; architect reviews alternatives,
  counts and contracts. No feature footprint/relocation credit.
- **Preserve functionality.** The owner clarified that backup/restore and
  manual conversion/embedding remain supported, including their web UI flows.
  D02's feature-removal proposal was withdrawn; its uncommitted implementation
  was fully discarded before publication. Do not reuse that discarded work.
- Reduction means deleting proven dead/redundant internals or replacing actual
  implementations with smaller equivalent code. Package moves and feature
  removal are not behavior-preserving savings. Absence from selected client
  calls or CI does not authorize dropping an implemented feature.
- B01–B09, I01–I06, L01–L05, PR01–PR07, MC-05/F1, S9b-R1 and D01 remain
  accepted at published `82110311`. Historical quickmatch consolidation was
  reported at 44,639 lines / 10,801 complexity / 177 high-complexity functions;
  these are not current ratchet values. Use `ratchet-baseline.json` and
  `make ratchet` for the active metric gate. Wave G evidence and its current
  blocked validation state are in `docs/WAVE-G-AUDIT.md` and
  `docs/wave-g/VALIDATION.md`; no untracked assessment document is required to
  interpret this policy. D01's large feature footprints are KEEP under the clarified
  requirement, not an available deletion wave. No safe 1,000-line consolidation
  is currently established; do not force that number by removing functionality.
- R1's OpenSessions pass-through is reviewed locally: preserve its typed JSON
  payload and array/null behavior. Its isolated HEAD-plus-R1 ratchet decreases
  production LOC by 2 and `map[string]any` by 3, with no other metric growth;
  that disposable baseline update is not the combined Wave G baseline.
- N2, R1, RT1 and QM01 now pass on the canonical current worktree, including
  real Socket.IO, SQLC v1.31.1, required local workflow checks and TS rollback.
  The measured baseline is 38,119 handwritten LOC / 8,531 complexity / 115
  functions over 15 / 461 `map[string]any` / 202 `[]any`; only those three
  changing fields were updated. See the final integration addendum for hashes,
  commands, versions and limitations. Mark those four cards DONE in the same
  accepted source commit; N3 stays open until remote CI passes after push.
- No new domain extraction until its end-to-end behavior-preserving deletion
  case is established: exact duplicate bodies, retained consumers/contracts,
  replacement cost, net savings and tests. No provider/ebook/admin feature cut
  or route retirement is authorized. Keep the existing strict growth review.
- Give each worker one task ID, exact read/write scope, fixed contracts,
  dependencies, named tests/commands and a short completion format. Workers
  do not change architecture, expand scope, commit, publish or deploy.
- Keep context small: relevant symbols and contract rows, no whole-repo dumps
  or all-skill loading. Parallelize only non-overlapping work with a clear gain.
- The architect reviews the diff and behavioral evidence before acceptance.
  Reuse worker validation logs; rerun checks when evidence or code changed.
- Current authorization: implement the roadmap using cheap
  workers, review completed batches and push reviewed ready features to the
  fork. **All development is on `master`; do not create development branches
  or branch-based implementation worktrees.** Coordinate non-overlapping file
  ownership in the shared checkout. Reviewed ready commits are pushed to master;
  manual deployment/cutover remains separately authorized.
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

Inherited untracked identity/routes.go currently does not compile (it refers
to absent httputil.NotFound). Preserve it as owner WIP. Acceptance uses a clean
tracked-source archive plus the exact approved changed/new files; never hide
the issue by staging/fixing that file or resetting the checkout. Development
still occurs only on canonical master; a validation snapshot is not a branch.
Use [TESTING.md](TESTING.md) for the verified native FFmpeg PATH/FFMPEG_PATH;
the default global binary still lacks libwebp. Ratchet requires lowered
baselines after cuts too: run the existing lower-only update in the exact
shipping-source snapshot, review decreases, then rerun the gate.

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

TQ01's [implementation](TEST-COMPATIBILITY-IMPLEMENTATION.md) keeps decoded
value/type/state/security/HTTP contracts in default tests and moves historical
fingerprints to `//go:build tsdiagnostic` or the explicit Node sidecar. Run
`make test-ts-diagnostics` separately; both diagnostic CI jobs are advisory.
Preserve whole mixed tests' useful assertions and all oracle/rollback evidence.
Keep [the assertion map](contracts/test-assertions.json), retained corpus hashes
and external inventory reconciliation current when intentionally changing them;
never edit external inventories or assertions to improve an implementation's
score. Monstrobook's imports remain a separate pinned snapshot, not a claim
that its full Go suites or scoring ran here. I01–I08 changes remain separate.

The 30% reduction target is aspirational; **the strict ratchet still rejects
increases**. Q1's soft-metrics proposal is replaced by explicit growth review:
before dispatch, record a bounded per-metric budget, required benefit, smaller
alternatives, exact old-code deletions, adapter exit tasks and checks in the
existing card. Workers never edit baselines. Actual growth needs architect
acceptance against that budget and a narrow baseline edit in the same commit;
budget overruns return for review. No cross-packet offsets or blanket allowance.
Close the cleanup checkpoint after a growth wave before another ownership
expansion. Deletion packets must reduce net production code. Never weaken a
correctness/security guard or call an unimplemented policy machine-enforced.

## Code rules

- PB04c2 is architecturally gated by the
  [2026-11-11 decision](docs/PB04C2-ARCHITECTURE.md). First review the
  removal-only folder PATCH repair (PB04c1-R), then deletion ownership in
  itemwrite (PB04c2-D). Never call independently committing item deletion
  inside the library transaction. Dispatch one packet at a time with its
  recorded budget; c2 needs a fresh dispatch/budget after those reviews.

- UA01's [userapi ownership and validation record](docs/USERAPI-SIMPLIFICATION.md)
  describes the concern files behind its unchanged public facade. Preserve
  ordered PATCH validation, legacy listening null/date semantics, unknown
  persisted properties, repeated-action no-op behavior and emit/reload order.

- The [podcasts simplification review](docs/PODCASTS-SIMPLIFICATION-REVIEW.md)
  records PC01's implementation, caller contracts and verification. Retain the
  public facade and shared `podcasts/read` interfaces. Creation/OPML,
  scheduling, episode mutation and downloaded-episode registration have named
  file owners; preserve the characterized exact-key PATCH and coercion quirks.

- The owner's 2026-11-10 package/object-boundary request is recorded in
  [the boundary proposal](docs/PACKAGE-BOUNDARIES.md). Treat its layout as
  proposed, not current implementation. Inventory actual consumers before
  extraction; do not duplicate existing models or claim moves as deletions.

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

FFmpeg single-executable research is recorded in
[the feasibility review](docs/FFMPEG-LIBRARY-FEASIBILITY.md). It adds no approved
dependency or implementation work: retain current media functionality and
CGO-free/subprocess defaults until a separate build/dependency decision.

Run from root: `make ratchet`, `make bench`, `make client-usage` (writes the
inventory), `make parity-replacement`, `make parity-scan`, `make route-probe`.
The last three are advisory, need Node 22 and a built TS reference, and mutate
disposable fixtures. Run route-probe last. Without mise use `PARITY_NODE=node`.
Local image-source samples need `SSRF_REQUEST_FILTER_WHITELIST=127.0.0.1`.
