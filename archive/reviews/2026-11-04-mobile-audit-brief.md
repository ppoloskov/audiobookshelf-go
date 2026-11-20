# Architect Agent Task: Mobile Client Compatibility Audit and Executable Roadmap

Review Audiobookshelf-Go against the current source code of actively maintained Audiobookshelf mobile clients. Translate findings into **specific, prioritized engineering tasks with reproducible tests and unambiguous completion criteria**.

Repository: https://github.com/ppoloskov/audiobookshelf-go

Read `go-backend/ARCHITECTURE.md`, `AGENTS.md`, `ROADMAP.md`, and the current client contracts before starting.

## Architectural constraints

Two contracts are fixed:

1. **SQLite and persisted data:** No schema changes, migrations, or incompatible changes to stored values, metadata files or rollback behaviour.
2. **Client-facing protocols:** Preserve HTTP routes, authentication, JSON semantics, Socket.IO events, streaming, downloads and playback behaviour required by supported clients.

Everything between those boundaries can be simplified or redesigned. Upstream TypeScript is a behavioural reference, not a byte-level oracle.

Do not retain compatibility machinery without evidence, but do not delete functionality merely because the bundled web UI does not use it.

## 1. Audit contemporary client source code

Review these repositories at their latest available commits:

- Official app: https://github.com/advplyr/audiobookshelf-app
- Lissen: https://github.com/GrakovNe/lissen-android
- AudioBooth: https://github.com/AudioBooth/AudioBooth
- Absorb: https://github.com/pounat/absorb
- Storii: https://github.com/likhithpraveenk/storii
- yaabsa: https://github.com/Vito0912/yaabsa

Record for each client:

- Platform and commit SHA.
- HTTP endpoints and methods actually invoked.
- Authentication and token-refresh mechanisms.
- Socket.IO version, transport configuration, path, authentication and events.
- Playback initiation, direct streaming, HLS, downloads and offline-session syncing.
- HTTP caching, range requests and cover-format assumptions.
- Explicit server-version compatibility branches.

Use source file and line references. Distinguish **confirmed usage**, **inferred usage**, **not found in source**, and **not investigated**. An unsuccessful code search is not proof that a feature is unused.

## 2. Validate findings against Audiobookshelf-Go

Pay particular attention to:

**Socket.IO:** The official app, Absorb, Storii and yaabsa explicitly configure WebSocket-only transport. Inspect `internal/realtime/eio.go`, `sio.go` and `hub.go` for interoperability, reconnection, authentication, acknowledgements, heartbeat, resource limits and malformed packets. Investigate the `invalid_token` versus `auth_failed` event difference.

**REST:** Lissen uses conditional caching with `ETag`, `If-None-Match` and `304`. AudioBooth uses HTTP byte ranges with `206` and `Content-Range`. Preserve the HTTP semantics clients need, but not Express-specific formatting.

**Playback:** Verify direct audio URLs, HLS, content types, stream seeking, offline sessions and both `/api/session/local` and `/api/session/local-all`.

**Routes:** The existing `contracts/client-usage.json` classifies some demonstrably used third-party endpoints as unused:

- `GET /api/me/progress` — yaabsa
- `GET /api/playlists` — yaabsa
- `GET /api/collections` — yaabsa

Audit other proposed deletions before R2/R3. Current web-client usage alone is insufficient to authorize removal.

**Covers:** Assess whether WebP cache standardisation changes any observable output requirement. Preserve original artwork and establish explicit client-format requirements before simplifying format negotiation.

## 3. Reassess simplification priorities

Measured sqlc results are L5 −58 and L6 −4 handwritten lines. The earlier estimate of thousands of removable lines was incorrect.

Keep sqlc for typed, checked queries, but stop treating it as the main code-reduction lever.

Investigate higher-value work: shared typed and batched item loading, repeated response construction, dynamic-map transformations, unnecessary compatibility layers, duplicated authorization logic and entire unused behaviours.

Treat the 30% code-reduction goal as aspirational unless backed by measured deletion opportunities. Do not introduce abstractions just to move or shorten code.

# Required operational deliverables

## Deliverable A — Commit-pinned compatibility inventory

Create `go-backend/contracts/mobile-client-audit.md`.

Include one row per verified **client × protocol operation**:

| Field | Required content |
|---|---|
| Client | Name, platform, source repository and commit SHA |
| Operation | Exact HTTP method/path, Socket.IO event, or media operation |
| Source | Permalink pinned to SHA, with file and line range |
| Behaviour | Relevant headers, payload fields, response expectations or transport settings |
| Go implementation | File/function that handles it |
| Test evidence | Existing Go test name, or `MISSING` |
| Status | Confirmed compatible / gap / uncertain / not tested |
| Priority | P0 / P1 / P2, with reason |

Do not conflate *implemented* with *tested*. Link each gap to a proposed task ID.

Also provide a concise protocol matrix showing which clients need WebSocket-only Socket.IO, polling, binary packets, REST caching, range requests, HLS and offline sessions.

**Acceptance:** Every claim links to source evidence. All six clients have an explicit review status. Unknowns are recorded rather than silently treated as unsupported.

## Deliverable B — Reconciled route and event inventory

Compare the new audit with `contracts/client-usage.json` and the actual Go route registrations.

Produce:

- Routes previously marked unused but found in real clients.
- Routes used by the bundled UI or official mobile app.
- Routes used by third-party clients.
- Routes with no located caller.
- Routes with unresolved dynamic call sites.
- Missing or incompatible Socket.IO events.

For every candidate removal, assign exactly one decision:

- **KEEP:** demonstrated client requirement.
- **SIMPLIFY:** behaviour required, existing implementation unnecessarily complex.
- **REMOVE CANDIDATE:** no demonstrated caller; requires owner approval.
- **INVESTIGATE:** insufficient evidence.

Include the justification and source/test references.

Update the inventory generation process or propose the smallest concrete change needed to make the external-client evidence reproducible. Do not overwrite automatically generated files manually unless the workflow explicitly supports it.

**Acceptance:** No route is proposed for deletion solely on bundled-web-client absence. Every removal candidate is traceable to a decision awaiting approval.

## Deliverable C — Executable gap register

Create `go-backend/contracts/mobile-compat-gaps.md`.

For each identified gap, document:

1. Unique ID, e.g. `MC-01`.
2. Affected clients and exact behaviour.
3. Current Go code path.
4. Reproduction steps or a minimal protocol request.
5. Expected result and current result, distinguishing observed failures from suspected gaps.
6. Proposed fix, explicitly limited in scope.
7. Required regression test, including test file and test function name.
8. Acceptance criteria and relevant commands.
9. Dependencies, risks and estimate (S/M/L).
10. Evidence links and owner decision if needed.

Prioritize as follows:

- **P0:** Authentication, playback, progress, persistence or security breakage.
- **P1:** Missing client-visible functionality, incorrect live updates or broken caching.
- **P2:** Optional compatibility, cleanup and noncritical simplification.

A suspected gap must not be described as a confirmed bug until reproduced.

**Acceptance:** Every P0/P1 item can be handed to an implementation agent without needing a separate architecture investigation.

## Deliverable D — Test implementation plan

Produce a test matrix covering at least:

- Socket.IO connection to each supported path.
- `auth` → `init`, invalid/expired token handling and re-authentication.
- Reconnect and event delivery, recipients and acknowledgements.
- WebSocket-only interoperability using a real Socket.IO client library.
- Malformed packets, oversized messages and connection cleanup.
- Conditional GET with `ETag` → `If-None-Match` → `304`.
- HTTP byte-range requests, including suffix ranges, invalid ranges and `416`.
- Direct audio playback and HLS session creation.
- Offline/local-session ingestion and subsequent persistence.
- Cover requests using actual client parameters.
- Representative client JSON decoding and meaningful value assertions.

For each test specify the intended package/file, fixture, test function, exact expected assertions and command to run it.

Prefer extending the existing Go contract suite. Avoid building a second large testing framework.

**Acceptance:** The plan clearly distinguishes existing passing tests, tests requiring additions and scenarios requiring real-device verification. Commands must refer to actual repository packages or be clearly labelled as proposed after implementation.

## Deliverable E — Roadmap changes ready for execution

Update `go-backend/ROADMAP.md` with the findings.

Create a prioritized dependency-ordered task queue, not a generic list of ideas.

Each new or revised task must include:

- Task ID and concise title.
- Priority and rationale.
- Exact target files/packages.
- Change boundaries: what to modify and what must remain unchanged.
- Prerequisites.
- Expected outcome and measurable benefit.
- Explicit acceptance tests and commands.
- Definition of done.
- Whether owner approval is required.
- Size estimate (S/M/L).

Separate tasks into three groups:

1. **Compatibility blockers:** must be completed before route removals or major refactoring.
2. **Simplification opportunities:** removal or consolidation justified by evidence.
3. **Deferred investigations:** insufficient evidence or low expected return.

Revisit the ordering of R1–R3, S9, S13, P2/P3 and V2. Preserve sqlc tasks where they improve query safety, but remove unsupported line-saving projections.

**Acceptance:** A coding agent can select the next task, implement it, run stated tests and determine completion without interpreting vague architectural intentions.

## Deliverable F — Documentation consistency patch

Review and update, where necessary:

- `go-backend/ARCHITECTURE.md`
- `go-backend/AGENTS.md`
- `go-backend/ROADMAP.md`
- `go-backend/README.md`
- `go-backend/handover.md`

Correct stale statements about completed work, Socket.IO implementation, client coverage, parity gates and sqlc savings.

Keep permanent architecture rules separate from temporary implementation instructions and historical findings.

**Acceptance:** No conflicting instructions remain in the active documentation. Historical review material is preserved rather than rewritten.

## Deliverable G — Final decision report

Finish with a compact report containing:

**1. Executive findings:** The five most important verified conclusions and why they matter.

**2. Risks:** Top compatibility and security risks, ordered by impact and evidence strength.

**3. Decisions needed:** Every owner approval required, presented as a specific choice with a recommended default and consequences.

**4. Recommended next five engineering tasks:** In execution order, with task IDs, dependencies, size estimates and measurable completion criteria.

**5. Changes made:** Files changed and why, plus a summary of documentation and roadmap diffs.

**6. Verification:** Commands actually executed, pass/fail results and anything not tested. Do not claim tests were run if they were only proposed.

**7. Remaining uncertainties:** Missing client evidence, unresolved dynamic routes and real-device tests still required.

## Execution rules

Perform the audit and produce the documentation, inventories, gap register and roadmap revisions in this task.

Do **not** implement broad runtime changes, replace working subsystems, change SQLite, delete routes, or rewrite the architecture without a separately approved implementation task.

Use source evidence rather than assumptions. Prefer concrete, bounded changes with testable results over speculative code-reduction estimates.

**Success means the repository contains an evidence-backed, dependency-ordered work queue that an implementation agent can execute, plus clear compatibility boundaries that prevent unnecessary work and accidental client regressions.**