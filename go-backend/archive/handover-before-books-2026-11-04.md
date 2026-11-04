# Go backend handover

Updated 2026-11-04. This is current continuation state. Permanent contracts:
[ARCHITECTURE.md](ARCHITECTURE.md); process: [AGENTS.md](AGENTS.md); active
queue/approvals: [ROADMAP.md](ROADMAP.md). Dated reviews and old task detail
under `archive/` are evidence, not current instructions. Generated `STATUS.md`
is the last recorded sampled core run, not live mobile/deployment status.

## Current state and authorization

Takeover inspected HEAD `48c10a6a` (L7 merge). F foundations, L1–L7, S2,
C5/C24/C25/C26 and V1 are recorded complete; not independently reaccepted by a
new full-suite run. The fixed boundaries are SQLite/persisted meaning and
client-visible protocols, including rollback, cache/range/media semantics.
Upstream TS v2.37.2 is a behavioral reference, not a byte oracle.

This packet updates documentation and performs bounded source/test auditing.
No runtime code, schema, generated client inventory, ratchet baseline, CI or
dependencies were changed. No commit, publication, deployment, route deletion
or live database operation was performed. The revised execution queue is
**implementation authorized by subsequent owner instruction**; see current execution below.

The expensive architect owns architecture/planning/review. Cheap workers own
bounded execution. OpenCode has available Flash models, but automatic review
rejected the attempted `ollama-cloud/minimax-m3.1-flash` delegation because
private repository content could be exported to that provider without specific
approval. Those workers did not launch. Built-in `gpt-6-luna` workers performed
read-only research instead. Do not bypass that rejection or infer provider
permission from general delegation authorization.

## Findings that change the next work

- `client-usage.json`: 201 resolved web/log/trial routes, 25 labeled unused,
  one unmatched pattern, nine dynamic calls. This is not full mobile or
  meaningful-value coverage. External client source is not yet a generator
  input. Preserve GET `/api/me/progress`, `/api/playlists`, `/api/collections`
  while reconciling confirmed callers; no deletion authorized.
- [Mobile audit](contracts/mobile-client-audit.md) pins all six clients and
  records selected source evidence, not exhaustive coverage. Remaining
  unknowns are explicit. [Gap/test plan](contracts/mobile-compat-gaps.md)
  separates observed source facts, suspected gaps and proposed tests.
- C25 uses in-repo Engine.IO/Socket.IO v4 over WebSocket only, default namespace;
  polling and binary packets are unsupported. Real JS interop coverage exists
  but can skip. MC-C makes execution/failure/recovery evidence explicit.
- `eio.go` has no explicit `MaxPayloadBytes` setting, but the pinned x/net
  WebSocket library defaults to 32 MiB per frame. Do not repeat an “unbounded”
  memory finding; investigate total/fragmented input and suitable event limits.
- C5 cover search/cancel are wired in `server/runtime.go` through providers.
  `TestCoverSearchSocket` and `TestCoverSearchCancel` exist. Earlier unwired
  claims were stale and have been corrected in active docs.
- Both offline endpoints exist; stronger bulk `local-all` repeat/update/
  ownership/persistence assertions are planned, not an established data-loss bug.
- sqlc remains useful for typed checked queries. The supplied −58/−4 line
  deltas for L5/L6 were not remeasured here; no thousands-of-lines promise.
  S13 + P2/P3 target duplicate item assembly/N+1 queries before broad moves.
- V1 is complete: CI parity is advisory; image depends on verify + rollback.
  V2 cannot delete shared seeds: `contractfixture/build.cjs` imports
  `parity/replacement-trial.cjs` and `parity/lib/args.cjs`; clientusage reads
  the replacement trial and parity route inventory. Extract/retest all inbound
  fixture/shape/browser/benchmark/rollback dependencies before removal.
- Metrics remain strictly enforced by current tooling. Q1 proposes review
  signals; a documented policy is not an implemented ratchet change.

## Dirty work inherited — preserve it

At entry these were untracked: `.codemap/`, `go-backend/TODO.md`,
`go-backend/internal/identity/routes.go`. The codemap and runtime route file were left untouched. TODO.md received
only an explicit historical/superseded banner; its original text is preserved.
It is **not** the active work queue;
use ROADMAP. Reconcile ownership before adopting/archiving it or integrating
identity route work. No worker should reset or clean these paths.

## Operational gotchas

- Never run TS and Go against the same writable config/metadata. Use isolated
  installation copies; SQLite backup API or stop TS before taking a snapshot.
- Production uses one SQLite connection: pool query while Rows/Tx remains open
  deadlocks. Keep `tools/dbnest`; DATETIME writes go through `dbtime`.
- `items.Reader` is still a map-returning serialization boundary implemented by
  one catalog handler. Typed book values inside an outer map remain concrete
  structs; consumers must use DTO/media-access helpers, not blind map assertions.
  `tools/book-item-oracle.cjs` golden is semantic source evidence, not an output
  to regenerate just to make Go green.
- Node reference tooling requires 22.x, built dist-server and generated media/
  web client; fresh worktrees lack untracked outputs. Makefile selects mise if
  present; otherwise `PARITY_NODE=node`. Local image sources need
  `SSRF_REQUEST_FILTER_WHITELIST=127.0.0.1`.
- Loopback `EPERM` is sandbox access, not a protocol failure. Trials mutate
  disposable fixtures; route-probe runs last. Copied media inodes can differ.
  Preserve fixture macOS `/private` canonicalization.
- OIDC test provider keep-alive connections can leave Node running. Close them.
  Backup tests must start from matching archive copies, not differently rewritten
  Go and TS media trees. Historical schema-1 reports lack newer provenance.
- Go module suggests toolchain 1.27.3; this host's `go version` reported
  go1.27.0 darwin/arm64. Targeted tooling test below used `GOTOOLCHAIN=local`;
  this is not a toolchain upgrade or release-build certification.

## Takeover verification

Actually executed:

- `git status --short`, source/doc/test inspection and pinned public source
  reads; public git clone failed DNS, GitHub read-only connector worked.
- `opencode2 --help`, `opencode2 models`, `opencode2 run --help`: successful
  with sandbox escalation for its log directory. Worker launches rejected by
  automatic review as described above; no external worker execution claimed.
- From go-backend: `GOTOOLCHAIN=local
  GOCACHE=/tmp/abs-takeover-20261104/go-cache go test ./tools/clientusage`:
  **PASS**, exit 0. Includes `TestOutputIsCurrent`. Mise emitted a cache-write
  warning under sandbox; the test result was successful.
- SHA-256 verification of the inventory's routeCoverage/replacementTrial inputs:
  **PASS**, both match recorded provenance. No generated JSON hand-edited.
- Local Markdown links: **PASS**, 18 active changed/new documents checked.
- External evidence structure: **PASS**, 18 records have 40-character commit
  SHAs and pinned line references; this validates provenance shape, not source
  census completeness or all linked claims independently.
- Route decision check: **PASS**, each of the 25 generator-unused operations
  has exactly one KEEP/INVESTIGATE decision. No REMOVE CANDIDATE invented.
- Archive identity: **PASS**, both supplied documents match original bytes;
  previous roadmap matches HEAD verbatim. Generated inventory/baseline/CI/module
  files have no diff. `git diff --check`: **PASS**, exit 0.
- Cheap-worker documentation review found stale handover, ambiguous dependency
  approval/cap wording and a route decision mismatch for source-confirmed
  `POST /api/session/local-all`; corrected. It is KEEP, with bulk persistence
  semantics still unverified. No other final blocking contradictions reported.

Not run: full build/vet/tests, lint, ratchet, sqlc generation, Node differential,
fixture regeneration, rollback, browser/device acceptance, protocol reproducers
or benchmarks. This documentation-only packet does not certify runtime behavior.

## Domain planning follow-up — 2026-11-04

Owner asked for an explicit book domain and concrete small tasks for simple
models. ARCHITECTURE now separates `books` data/metadata/chapters/projections
from common `items` identity/files/covers and `podcasts` data/episodes/feeds.
Book reads move first; writes/chapter mutation are not claimed migrated.
No new HTTP routes, database changes or runtime edits in this follow-up.

ROADMAP B01–B07 extract book data/projections one concern at a time;
I01–I06 add test-only query counting, batched book/association reads,
one-per-batch title settings, book-page/shelf hydration and common item rows.
L01–L03 pin list contracts and extract reads; L04 is a bounded architect
planning checkpoint for separate write cards. L05 is explicitly a blocked
placeholder, never a worker implementation instruction. All cards are planned.

A cheap built-in worker inspected the relevant symbols and proposed slices;
the architect set final boundaries and checked actual helper callers/test
names. Important discoveries: bookRelations also has direct test callers;
stringPointerValue remains used by batch_chapters; titlePrefix reads settings
per call; personalizedItems must explicitly close Rows before further queries.
Do not export transient SQL cursors, introduce a books->catalog/items cycle,
move all wire types, or turn the expected test oracle into the new implementation.

## Current execution — 2026-11-04

Owner authorized implementation using cheap workers, code review after completed
batches, and pushing ready features. Worktree:
`/private/tmp/abs-books-domain-20261104`, branch `codex/books-domain-batching`,
base `48c10a6a`, fork remote `https://github.com/ppoloskov/audiobookshelf-go`.
Do not merge default/master or deploy. The original checkout and inherited
`.codemap/`, `TODO.md`, `internal/identity/routes.go` remain outside the branch.
Authored documentation from takeover is copied to this worktree; inherited
TODO is intentionally not published. Review after code workers complete.

B01 book contract assertions and independent I01 test-only SQL instrumentation
are in progress in disjoint files. I01's former B07 sequencing is relaxed for
this test-only helper; runtime batching still follows completed book extraction.
B02–B07 can form one coherent worker batch to avoid temporary adapter/export
churn; individual acceptance checks stay required. No card is accepted yet.

Measured clean-worktree baseline: 44,586 handwritten Go lines, complexity
10,851, 178 functions >15, 993 dynamic maps; ratchet PASS. First full suite
attempt was blocked by sandbox module-cache writes; escalation rerun started.
The architect records actual final test exits, code review, deltas and delivery
before accepting or pushing. Next after B01: B02–B07 book-domain read/projection
extraction. Keep fixed data/protocol contracts and original independent goldens.
