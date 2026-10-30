# Parity finalization work queue

Prepared 2026-10-30. Planning only: no implementation or capability promotion is authorized by this document alone.

## Governing outcome

Deliver the operational replacement defined by `docs/agent-work/CONTRACT.md`, pinned to upstream v2.37.2, with no Node runtime fallback. The user's request to split finalization supplies the planning authorization; the existing contract supplies the accepted outcome. Historical migration deferral and the approved ebook stub remain exactly as recorded there. Do not restart completed work merely because this queue names its area.

This queue replaces the open-ended dispatch instruction “continue remaining parity” with bounded assignments. It does not replace historical evidence or change any acceptance requirement. Existing reports are evidence candidates, not automatic passes against changed code. Independent review challenged F02–F07 acceptance; follow-up evidence is recorded in TASK-20261030. Treat those rows as complete only where the named review remedy has fresh evidence. F05's strengthened actual-cookie assertion still requires a new differential report; do not dispatch F08 before bounded closure is verified and reviewed.

## Dispatch rules for smaller models

- Dispatch ONE numbered row at a time. Rows after F03 are planned, not dispatch-ready until F03 maps their exact capability IDs and source symbols. A coordinator fills in the packet below before assigning work; workers must not infer an entire subsystem from a row title.
- Each assignment has one observable behavior family, one production package at most, one owned test/harness file, and explicitly named evidence files. If more are necessary, return a proposed split before editing. A verification-only task owns no production files.
- Existing `go-backend/AGENTS.md` selects Luna for implementation agents. Using another implementation model requires a user change to that instruction. Review/planning tasks need no model-specific assumption.
- Characterize pinned upstream first. If current Go already matches, deliver evidence and STOP; do not invent a repair. If it differs, capture the red result before the smallest repair. Tests alone do not authorize architectural cleanup.
- No new response framework, router registry, repository layer, shared item model, generic coercion package, wholesale DTO conversion, dependency upgrade, or file reorganization in this programme. A necessary exception must explain the observed blocker and smallest alternative.
- No agent resets, cleans, stages, commits, publishes, mutates upstream sources, or touches live user data. Preserve all pre-existing changes. Use fresh independent config/database/metadata/media copies and distinct ports per run.
- Reuse current harnesses. New harness code must exercise a missing observation rather than create a second implementation of an existing runner. Pin Node 22 and rebuild the Go binary; record binary/source identity, commands, exit codes, environment, fixture identity, compared/mismatched/skipped counts, and normalization rationale.
- A run may be resumed only if its documented state allows it. Do not rerun mutation sequences on a consumed fixture and call it a fresh comparison.
- After two unsuccessful repair attempts, return the reproducer, diff and blocker. Do not switch to a different feature, weaken assertions, add retries to hide differences, or spin up another redesign.
- Workers update only their assigned capability/evidence and packet result. The coordinator updates README/agents/handover once per accepted milestone. No repeated full-suite run after every documentation edit.

## Required packet (copy for every assignment)

```text
Task ID / state: Fnn or Fnn.nn / ready
Outcome: one externally observable behavior; exact requests/events/client actions
Requirement: CONTRACT clause + capability IDs + pinned source files/symbols
Already verified: evidence to reuse, source revision, why still applicable
Owned files: exact production file(s), test file, evidence and capability file(s)
Inputs: fixture manifest, configuration, identities, media and provider behavior
Expected outputs: status/headers/raw body or JSON types, event recipients/order
Expected state: DB rows, files, IDs, timestamps, process/job effects before/after
Scenarios: normal, relevant denial/error/edge, repeat/retry/restart as applicable
Non-goals: adjacent variants, refactors, other packages
Prerequisites: accepted task IDs; tool/runtime requirements
Commands: exact existing runner invocation and package checks after inspection
Completion: explicit comparisons and state assertions; N/M/S and exit codes
Risk/rollback: disposable fixture only; production risk; how to revert this diff
Return: files changed, source reason, red/green or no-change evidence, limitations
```

Do not claim a runnable invocation until the runner's current options have been inspected. Workers receive this filled packet and the relevant source excerpt, not the entire historical task transcript.

## Common acceptance and sequencing

Every row below inherits the packet fields and dispatch limits above. All rows start **planned**; execution status is tracked in `docs/agent-work/tasks/TASK-20261030.md` and per-milestone in this file's status footer (as of 2026-10-30: F01–F03 complete, Milestone 1 rows F04–F07 complete, Milestone 2 active). Dependencies are sequential within each milestone unless stated otherwise. F03 is required for every later row. Milestone acceptance is sequential; do not work on email/OIDC while playback is still the active milestone. A genuine external blocker may permit the next independent row, but it remains visible and does not count as completion.

For each behavior compare the public result and resulting state on both implementations. Include source-relevant normal/error/edge cases and repeat/state transitions; “not applicable” needs a source reason. Matching unit tests or a successful build is not operational acceptance. Matching failure on both servers is parity evidence for that case, not proof the intended user workflow succeeds.

Production edits require focused regressions and appropriate build/vet checks. Concurrency edits require targeted `-race` plus lifecycle tests. At milestone boundaries run the retained regression corpus, relevant persisted-state and upstream-reopen checks. Integration owns full-suite execution and shared harness edits; parallel agents must not share files or running mutable fixtures.

### Milestone 0 — preserve work and fix the execution boundary

| ID | Requirement/source | Owned area and bounded outcome | Required proof | Dependency |
|---|---|---|---|---|
| F01 | CONTRACT evidence retention; `build_capabilities.py`, current enriched capability JSON | Generator only: existing reviewed records survive regeneration; preserve route/subsystem evidence and stateful GET classification. Prefer create-missing/refuse-overwrite over a complex merge engine. | In temporary output directories, seed an enriched route and subsystem record, run twice, assert preservation and deterministic new-record creation. Never test overwrite protection on the live ledger. | None |
| F02 | CONTRACT source of truth; ROADMAP architecture and milestone instructions | Documentation only: identify current authoritative guidance, remove conflicting interface prerequisites, point dispatch to this queue, preserve historical decisions. | A cold reader finds one outcome, one active milestone and one next task. No claim that strict parity passed. | F01 |
| F03 | CONTRACT inventory reconciliation; source-inventory, route-coverage, capability records, ApiRouter/HlsRouter/PublicRouter and SocketAuthority | Read-only inventory plus planning output: create `docs/specs/planned/parity-finalization-traceability.json` mapping EVERY current capability, route candidate and event candidate to evidence and a task. Resolve aliases/runtime registrations; include conditional features, jobs, config and failure behavior. | No orphan IDs. Every gap has an F-task or F51 child, every existing verified observation is retained, every excluded item cites owner approval. Record exact current counts, not assumed 226/165 totals. Produce dispatch-ready packets for F04–F06. | F02 |

F03 is bookkeeping with a finite input set, not permission to implement missing routes. Divide its inspection into batches of at most ten candidates if needed; the coordinator merges the mapping. Unresolved semantics are explicit characterization tasks.

### Milestone 1 — existing installation, authentication and browsing

Primary sources: `index.js`, `server/Database.js`, `server/Auth.js`, `server/auth/`, retained client. Reuse `existing-db-matrix.cjs`, `client-check.cjs`, `rollback-check.cjs` and current local-auth evidence.

| ID | Bounded behavior / inputs and state | Go area / expected proof | Dependency |
|---|---|---|---|
| F04 | Start with representative pinned existing DB, including pinned partial-state cases; reject unsupported historical schemas without mutation. | bootstrap/store; compare startup outcomes, logical schema/rows and files before/after; no implicit migration or recreation. Reuse matrix before adding cases. | F03 |
| F05 | Local login, refresh, logout and inactive/restricted user observations needed by the client. | identity; reuse selected existing cases; fill only missing local-session cases, verify cookies/token usability and persisted revocation. Split by transition if uncovered. | F04 |
| F06 | Browser login → library → book detail through the configured prefix; fresh DB initialization is a separate fixture variant. | server/static client; actual retained browser, assets and request failures; root/default/prefixed launch behavior; compare restricted visibility. | F05 |
| F07 | Upstream opens and reads a Go-written fixture after the above transitions. | verification only; rollback runner, logical row/file comparison and actual upstream login/read. Preserve a restartable snapshot for milestone 2. | F06 |

### Milestone 2 — usable playback, before peripheral services

Sources: PlaybackSessionManager, playback controller, HlsRouter, SocketAuthority and client player. Reuse browser-check, operational-milestone and differential runners. Use multi-file audio longer than several HLS segments; the two-second sample cannot establish reset/seek behavior.

| ID | Bounded behavior / state | Go area / required proof | Dependency |
|---|---|---|---|
| F08 | Direct stream play/pause and valid/invalid byte ranges including HEAD. | playback; HTTP bytes/headers/status plus actual browser media time advancement. | F07 |
| F09 | Forced transcode starts, produces playable segments, and closes. | playback; actual FFmpeg and retained browser; playlist/segment responses, progress and process termination. | F08 |
| F10 | Backward seek resets HLS while concurrent segment requests run. | playback transcode lifecycle; source comparison, red regression if divergent, targeted race test; stale processes cannot publish current output. | F09 |
| F11 | Close/shutdown during a pending reset. | playback; deterministic lifecycle regression showing no reset can start work after close; public cancellation result and no leftover FFmpeg/output writers. | F10 |
| F12 | Progress sync and completed-state effects after play/pause/seek/close. | playback/userapi assigned separately if repair spans both; DB values, units, timestamps and per-user events match; repeat sync does not duplicate state. | F11 |
| F13 | Restart server, log back in and resume the same item at persisted progress. | verification first; real browser and database checks, including a multi-file chapter boundary. | F12 |
| F14 | Disconnect/reconnect active client and second user's observer. | realtime first; compare session recovery, event recipients/order, stale connection cleanup and progress after reconnect. | F13 |
| F15 | Playback milestone acceptance. | verification only: replay F08–F14 on independent fixtures, upstream reopens resulting DB; browser success is required alongside differential equality. | F14 |

### Milestone 3 — library state survives real changes

Sources: `server/scanner/`, LibraryScanner, LibraryItem/Book models, item controllers. Reuse scanner evidence and differential cases; do not repeat already closed scanner repairs.

| ID | Bounded behavior / state | Go area / required proof | Dependency |
|---|---|---|---|
| F16 | Initial → unchanged repeat import of a representative multi-file book. | scanner; stable IDs, tracks/chapters, relationship rows, metadata files and events; no duplicates. | F15 |
| F17 | Rename/move/change audio, then rescan and forced scan. | scanner; explicit identity and file-reconciliation cases from source; split rename and changed-input repairs. | F16 |
| F18 | Conflicting/empty sidecars and configured metadata precedence. | scanner; reuse prior precedence goldens, compare DB and stored metadata through public scan. | F17 |
| F19 | One probe/write failure followed by retry. | scanner; compare source partial-state behavior, job status, files and event timing; do not assume transactional rollback is automatically more compatible. | F18 |
| F20 | Watcher add/rename/delete and shutdown with queued work. | scanner; public watcher workflow and repeat/restart observations; targeted lifecycle/race checks. | F19 |
| F21 | Catalog filters, ordering, pagination, search and collapsed series gaps. | catalog; F03 generates separate children per query family, maximum three related variants per child; compare restrictive users and empty results. Search-extension equivalent semantics remain required. | F20 |
| F22 | Item edit → metadata sidecar → reread/rescan. | catalog then scanner only as separate repairs; preserve null/absent/empty values and author/series relationships, compare files and events. | F21 |

### Milestone 4 — access, destructive operations and recovery

Sources: User model predicates plus each endpoint's middleware/controller, cover/download handlers, backup managers. Do not substitute one global ACL rule for endpoint composition.

| ID | Bounded behavior / state | Go area / required proof | Dependency |
|---|---|---|---|
| F23 | Route-specific library/tag/explicit permissions, including admin with restricted permissions. | F03 children per endpoint family, one package each; source-derived truth table and isolated differential. Go `[]string` versus `[]any` alone is not a public input case. | F22 |
| F24 | User/credential/API-key changes and active-session effects. | userapi/identity separate children; repeat/no-op changes, denial and revocation of existing HTTP/socket access. | F23 |
| F25 | Collection/playlist ownership and mutation lifecycle. | userapi; reuse existing create/update/delete evidence, fill missing ordering/empty/deleted-member cases, compare persisted state. | F24 |
| F26 | Cover alternate-path replacement and store-with-item behavior. | catalog; source-compatible paths, removal effects, updatedAt and socket recipients. | F25 |
| F27 | Cover size boundary, redirect chain and remote fetch failures. | catalog; split size, redirects and network-policy cases into children; local controlled servers, no arbitrary internet targets; retain existing SSRF policy evidence. | F26 |
| F28 | Download Apple-mobile MIME, X-Accel and filesystem failures. | catalog; separate packet per variant, exact headers/body/ranges and access checks. | F27 |
| F29 | Destructive item/library/user cascades with repeat deletion. | one owning package per child; reuse W6, compare relationships, durable media/metadata deletion and events; no live files. | F28 |
| F30 | Backup create → restore → continued use and upstream import. | backup/server separate repairs; reuse interoperability evidence, compare DB/metadata and runtime rebinding, not only successful HTTP. | F29 |
| F31 | Restore while requests/jobs are active, then injected restore failure. | server manager; source characterization, isolated filesystem failure, race/lifecycle tests and documented resulting service/state. | F30 |
| F32 | Distinct-file retention rolloff and scheduler boundary. | backup; controlled time/separate archive names, keep/count/size policy, restart and cancellation. Minute-collision behavior is a separate characterization if relevant. | F31 |

### Milestone 5 — finish remaining services, one transition at a time

F03 must cite exact source symbols and capability IDs for every child below. A parent is a scheduling bucket, NEVER an implementation assignment. Existing selected OIDC/email/notification evidence remains valid within its recorded limits.

| ID | Bounded behavior / state | Go area / required proof | Dependency |
|---|---|---|---|
| F33 | Podcast RSS ingestion and repeat refresh. | podcasts; malformed/feed-change/dedup cases, episodes/metadata and events. | F32 |
| F34 | Podcast download queue: enqueue → complete/fail/cancel and restart. | podcasts; separate transition children, local media server, files/rows/events, limits and cancellation. | F33 |
| F35 | Public RSS/share access, expiry and revocation. | sharing; separate children per authentication/capability URL transition; bytes and unauthorized exposure checks. | F34 |
| F36 | Built-in/custom provider response mapping and failure. | providers; child per provider operation; controlled captured responses, config and error/empty mapping. No wholesale live-provider dependency. | F35 |
| F37 | OIDC session expiry/renewal and failure-cookie clearing. | identity; child per missing transition; local provider and controlled time, compare cookies and session replay. | F36 |
| F38 | OIDC logout errors/mobile/allDevices and remaining callback/provider branches. | identity; source-inventoried children, one branch each; reuse 24 selected cases rather than rewriting them. | F37 |
| F39 | SMTP TLS/auth and DATA-stage failure. | emailapi; child per transport/auth/failure variant; local SMTP, envelope/body and state/event observations. | F38 |
| F40 | Send-to-device attachment and recipient/error variants. | emailapi; child per variant, actual MIME attachment bytes plus ACL and repeat effects; approved ebook stub boundary remains explicit. | F39 |
| F41 | Notification queue capacity, delay, ordering and concurrent triggers. | notifications; children per queue invariant, controlled Apprise receiver, persisted attempts and socket broadcasts. | F40 |
| F42 | Notification lifecycle event coverage and malformed omitted-urls behavior. | notifications; child per event family; malformed request runs in an isolated oracle process and records exit behavior. No unapproved compatibility exception or automatic deliberate server crash. | F41 |
| F43 | Audio merge/embed job lifecycle and filesystem effects. | audiotools; child per operation, actual ffprobe of output, cancellation/failure/retry, stable task IDs in the appropriate source format. | F42 |

### Milestone 6 — exhaustive closure and cutover evidence

| ID | Bounded behavior / state | Owned area / required proof | Dependency |
|---|---|---|---|
| F44 | Unknown path/method and response fidelity on owned prefixes. | child per package; source-derived status, raw text/JSON and header assertions including HEAD/empty bodies; fix silent empty-200 paths without replacing routing. | F43 |
| F45 | Conditional startup/config and runtime assets. | child per option family from index.js/Server.js; defaults, CLI/env precedence, base path, client assets, FFmpeg failure, restricted read-only mode and source-conditional features. No silent exclusion of a conditional upstream feature. | F44 |
| F46 | Socket.IO inventory closure beyond connect. | child per maximum three related events; both paths/transports, acknowledgements, payload/order/recipient and auth lifecycle. Reuse F14 evidence. | F45 |
| F47 | Cross-feature concurrency and recovery. | child per source-backed conflict: refresh/progress, scan/playback, restore/jobs, shutdown; bounded load with explicit DB/files/process assertions. | F46 |
| F48 | Packaging and platform acceptance. | build/package files only if needed; CGO-disabled amd64/arm64 builds, supported-platform runtime smoke where available, frontend/runtime assets and FFmpeg dependency. A cross-build is not a runtime pass. | F47 |
| F49 | Retained real web and selected mobile-client workflows. | verification only; identify exact clients/versions, perform install/login/browse/play/seek/resume/progress flows; unavailable mobile environment is an explicit blocker. | F48 |
| F50 | Final integration and upstream rollback trial. | verification only; fixed full corpus, logical DB/files comparison, actual upstream reopen/use after all mutation groups; build/test/vet/race, strict ledger gates. Record failures without weakening gates. | F49 and F51 closed |
| F51 | Residual inventory closure. | F03 creates finite numbered children for every mapped requirement not covered above, including every model/job/config branch and route/event variant. One operation/transition per child; no catch-all implementation task. | Children scheduled in their owning milestone before F50 |

## Traceability and change control

| Source requirement | Source location | Queue coverage | Required test | Status |
|---|---|---|---|---|
| Pinned runtime, existing DB and rollback | CONTRACT; index.js; Database/models/migrations | F04–F07, F30–F32, F45, F48, F50 | real startup/reopen + state comparison | Planned, existing evidence reusable |
| HTTP, auth, JSON/header/error distinctions | CONTRACT; routers/controllers/auth | F05–F06, F21–F29, F33–F45, F51 | public differential + resulting state | Planned |
| Playback/progress/client interoperability | CONTRACT; playback manager/HlsRouter/client | F08–F15, F49 | browser/media + persisted restart/resume | Planned |
| Filesystem, import and background effects | CONTRACT; scanner/managers | F16–F20, F22, F29–F34, F41–F43, F47 | repeat/change/failure/restart | Planned |
| Realtime exchanges and recipients | CONTRACT; SocketAuthority/emitter inventory | F14, each mutation task, F46 | multi-client transcripts | Planned |
| All candidate routes/events and production configuration/assets | source-inventory + actual source reconciliation | F03 mapping, F45–F46, F51 | per-entry executable witness or explicit blocker | F03 required before exhaustive claim |
| Ebook stub/historical migration exclusions | CONTRACT owner decisions | F04, F16, F40, F51 | safe boundary checks, no expanded exception | Preserved |

The table is programme-level coverage, not a claim that every behavior has already been decomposed. F03 must materialize the per-entry mapping before exhaustive execution. Newly discovered missing behavior receives a numbered child and source justification; no open-ended “improvements” bucket. A blocked child stays in the denominator. Do not equate the number of packets, test cases or validator diagnostics with percentage parity.

## Completion record and milestone stops

Per child record: status (`planned`, `ready`, `in_progress`, `verified`, `blocked`), source/capability IDs, evidence, actual commands/exit codes, compared/mismatched/skipped counts, changed state, review outcome and remaining variants. Never mark a parent verified while required children are blocked or unverified.

At each milestone the coordinator reports: user-visible workflow now proven, remaining blockers, regression result and next ONE packet. Do not produce a fresh architecture review or unrelated refactor queue. General cleanup remains deferred until parity is established or an observed blocker requires it.

Final acceptance requires reconciled inventory with no unassigned in-scope behavior; all required scenarios and strict gates passing; no unresolved mismatch or unavailable required client/platform test; operational workflows and upstream rollback proven; only owner-approved exclusions. This is parity to the pinned contract, not a claim that finite tests prove every conceivable input.

## Mock PR Annotations

- reviewer-summary: each implementation diff closes one source-backed behavior, with retained red/green or no-change differential evidence.
- reviewer-question: does the fixture actually reach the transition (long HLS seek, distinct backup names, active socket), or merely return a successful response? Missing reachability proof blocks verification.
- reviewer-risk: normalizing IDs/times/paths must not hide identity instability, wrong timestamp units, changed roots, missing fields or body/header inconsistency. Broader normalization blocks approval without characterization.
- reviewer-test: public result, durable state and repeat/failure observations must agree on independent fixture copies. Unit-only evidence is inconclusive.
- reviewer-blocker: capability regeneration loses evidence, a worker edits beyond ownership, or a requirement disappears from the denominator. Resolve before accepting the affected packet.

Status: decomposition approved by the user 2026-10-30. F01–F08 are complete for their recorded variants. F08 passed focused direct range checks (4/4), Chromium direct-play actions, and a fresh full differential (190/190); see its task-ledger entry and `go-backend/contracts/parity/f08-direct-stream-{report,provenance}.json`. Milestone 2 is active at F09 (forced-transcode browser playback and process closure). Root hydration still fails on both servers with the retained prefixed client build; this matching failure is not successful root operation. Full parity remains unverified. See `TASK-20261030.md` for commands, limits, and preserved failed attempts. F44's other unknown-path/method variants remain open. F01's historical live-regeneration incident and recovery remain recorded in the task ledger.
