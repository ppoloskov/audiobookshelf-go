# Roadmap

Updated 2026-11-09. Open work only. Finished and obsolete cards are listed in
[archive/reviews/2026-11-09-roadmap-pruned.md](archive/reviews/2026-11-09-roadmap-pruned.md);
older detail in [the pre-takeover roadmap](archive/reviews/2026-11-04-roadmap-before-takeover.md).
Current state: [handover.md](handover.md). Permanent contracts:
[ARCHITECTURE.md](ARCHITECTURE.md). Worker/check policy: [AGENTS.md](AGENTS.md).
Decision log and ledger: [docs/REFACTOR-LOG.md](docs/REFACTOR-LOG.md),
[docs/SIMPLIFICATION.md](docs/SIMPLIFICATION.md).

All development is on `master`; no branches. Every push is gated: full local
CI mirror plus the TS rollback check, chained (`ci-local.sh && rollback && git
commit && git push`). Manual deployment, new libraries and route removals need
separate owner approval. Every open card has a `- [ ]` checkbox; mark it
`- [x] DONE` in the commit that completes it, with actual check results.
Preserve functionality; package moves and feature removal are not savings.

## 1. Refactor work, in order (handover.md "Next, in order")

### N1 · Salvage Step 3 (behavioural test-quality pass)

- [ ] IN PROGRESS. T3 (podcasts, providers, notifications, emailapi) is
  finished but uncommitted. T1 (playback, sharing, realtime), T2 (scanner,
  audiotools, backup, server), T4 (catalog, items, lists), T5 (identity,
  access, users, store, infra, contracttest) were stopped by the owner on
  2026-11-09 with partial edits in the working tree (also saved as a patch).
- **Next:** per package keep what builds and passes, revert the rest to
  master, gated commit; then relaunch the unfinished packets (ask the owner
  first: a stopped agent cannot be resumed). Spec `TESTS-SPEC`: classify each
  "Tests to change" candidate in the log as replace / rewrite / delete /
  retain; keep every contractual assertion; remove test-only production hooks
  only via existing injection or package-private fixtures; lifecycle
  invariants first (playback, HLS, share expiry, watcher shutdown, podcast
  downloads, restore).
- **Follow-up inside N1:** replace `podcasts.SetHTTPClientForTests` (used by
  `server/runtime_test.go:63`) with `t.Setenv("SSRF_REQUEST_FILTER_WHITELIST",
  "127.0.0.1")` before `NewManager`, then delete the method.
- **Check:** CI mirror and rollback pass; no assertion in the log's contractual
  list lost; affected packages with `-count=1`.

### N2 · Wave G, behavioural transition and side-effect parity audit

- [ ] PLANNED (owner brief 2026-11-09). Six read-only investigations: scanner/
  filesystem, catalog lifecycle, podcasts/lists/progress, playback/background,
  security/identity/sharing, persistence/runtime. Then a transition matrix,
  reproduce the 10-15 highest-risk suspects, minimal fixes with a failing test
  first. Bug class: missing downstream lifecycle transitions (example fixed in
  wave F: scanner removal left podcast episodes behind).
- **Check:** each fix has a test that failed before; TS evidence cited
  (`server/` at the pinned commit); CI mirror and rollback.

### N3 · Step 4, final check and report

- [ ] PLANNED. CI mirror, rollback, GitHub CI, race runs
  (`CGO_ENABLED=1 go test -race`, affected packages at minimum; no workflow
  runs `-race` today, verify). Final report: behavioural evidence, test
  decisions, remaining exceptions, compatibility, CI, rollback, measured
  complexity. Stop when exit criteria hold; do not chase 35,000 lines or zero
  special cases.

## 2. Compatibility evidence and device acceptance

Cards stay open until their named assertions execute in required CI.

### MC-A / MC-B · Client audit and route decisions (documentation/tooling)

- [ ] OPEN, verify. Raw evidence and census exist (d796d9c):
  `contracts/mobile-client-audit.md`, `mobile-client-evidence.json`,
  `mobile-route-decisions.md` (still marked preliminary), `client-usage.json`.
  The census has one unmatched `/api/:param` pattern and nine unresolved
  dynamic call sites. Router code changed (ADR-001), so re-run
  `make client-usage` and reconcile the decision table against the mux
  registrations; every route gets one KEEP / SIMPLIFY / REMOVE CANDIDATE /
  INVESTIGATE with evidence. Unknowns become investigations, never
  unsupported-feature decisions. Do not hand-edit generated JSON.
- **Check:** `go test ./tools/clientusage`; `make client-usage` twice with
  identical output; `GET /api/me/progress`, `/api/playlists`,
  `/api/collections`, `POST /api/session/local-all` protected.

### MC-C · Socket.IO contract and resource tests

- [ ] OPEN, verify. Custom Engine.IO v4/Socket.IO framing is retained (owner).
  Existing: realtime tests for binary packets, full buffer, hub close,
  disconnect, event-before-connect; `internal/server/socket_connect_test.go`.
  The real `socket.io-client@4.8.1` probe is installed only in the CI
  `upstream-parity` job (advisory). Gaps to confirm or add: auth then init,
  `invalid_token` vs `auth_failed`, re-authentication, reconnect delivery,
  inbound payload/frame limit, heartbeat; both legacy and prefixed paths.
- **Decision needed:** provision the JS client in the required verify job so
  `TestSocketIO4WebsocketAuthAndPrefixedPath` executes there (it skips
  without the library).
- **Check:** `go test -v ./internal/realtime ./internal/server -run
  'Socket|Packet|Ping|Buffer|Payload|HubClose'` with the client present; no
  skips; `CGO_ENABLED=1 go test -race ./internal/realtime`.

### MC-D · REST/media/offline value regressions

- [ ] PARTIAL. Done: `TestMobileConditionalGET`, `TestMobileCoverParameters`
  in `internal/contracttest/mobile_protocol_test.go`; range and direct-play
  coverage in `playback_test.go` and `catalog/handler_test.go`.
- **Remaining, verify each exists before adding:** 206/416 suffix, invalid and
  unsatisfiable ranges with exact bytes and Content-Range; HLS play/session
  URLs and MIME; `/api/session/local` and `/api/session/local-all` followed by
  progress/session reopen persistence; decoder values/ordering/ACLs.
- **Check:** `go test -v ./internal/contracttest -run TestMobile` plus affected
  packages. Persisted state and contracts are not changed to pass tests.

### MC-E / O4 · Real-device acceptance (owner)

- [ ] OPEN, owner/device time. Owner tests official iOS/Android and affected
  third-party apps on a disposable initialized install: token login/refresh,
  browse, seek, download, offline sync, reconnect, progress on a second
  client. Record app/server versions, platform, procedure, results, safe logs
  in `contracts/mobile-client-audit.md` and handover. Source and simulator
  tests cannot substitute. Blocks R2/R3 and V2b. Live cutover is a separate
  owner decision.

### R2 / R3 · Owner-approved route/behaviour removal

- [ ] BLOCKED on MC-B and MC-E. R2 records owner KEEP/REMOVE per operation;
  R3 changes only approved handlers with exact file/test lists filled from
  MC-B before dispatch. No removal is approved.

## 3. Tooling retirement

### V2a / V2b · Extract reusable tooling, then retire TS diagnostics

- [ ] OPEN, verify scope (`tools/` still holds clientusage, contractfixture,
  parity, oracles; TS diagnostics are advisory CI jobs). V2a: make fixture
  generation and `make client-usage` independent of removable parity modules.
  V2b (needs V2a, MC-E and owner acceptance): delete only unreferenced
  differential infrastructure; keep rollback.
- Known drift: `go test -tags tsdiagnostic ./internal/httputil/` fails
  `TestTSRepresentationR03` (expects a trailing newline WriteJSON no longer
  writes) on master too; the diagnostic lane is not required CI. Fix or retire it
  with V2b. The `TestTSRepresentation*` files back `contracts/test-assertions.json`
  diagnostic targets, so remove them only together with their manifest entries.
- **Check:** fixture regeneration, `make client-usage`, Go contract and
  persistence tests, and `node go-backend/tools/parity/replacement-trial.cjs
  --rollback-only --work <disposable-dir>` from a clean checkout. Node lines
  are not part of the Go metric.

## 4. Deferred investigations (not dispatched; each needs file scope, regression and benefit first)

All are "verify against wave G / current code before starting"; several may
already be fixed in waves D-F (REFACTOR-LOG.md).

| IDs | Item |
|---|---|
| C1-C4, C7/C8/C11 | Share, podcast schedule, watcher, cancel, sort/name/feed behaviours. Podcast reschedule and watcher cancel were fixed in waves D-E; confirm the rest in wave G with named regressions. |
| C13/C19, C23 | Download/session lifecycle ownership and race CI (podcast download race fixed in T3; no `-race` in CI). |
| C15/C16 | Request JSON error handling; attachment truncation. Reproduce first. |
| C18 | `final-go-tests.txt` is a tracked test log; remove or ignore (housekeeping). |
| P4 | Read pool; benchmark first; rollback-journal invariant stays. |
| P6 | Client-critical benchmark baseline (`make bench`); no new framework. |
| Covers | Cover format/cache parameters per client; keep original artwork and persisted layout; no WebP-only approval. |
| Polling/binary Socket.IO | Only where client evidence requires; no subsystem replacement. |

## 5. Parked owner decisions (none block current work; the default applies until decided)

- O1 `ANALYZE` at boot, O2 destructive TS cleanup, O5 new libraries: parked;
  default is no new boot writes, deletes or dependencies.
- Provisioning the JS Socket.IO client in required CI (MC-C).
- Supported-client scope after MC-A; per-route R2 approvals; O4 device access.
- External provider use for cheap workers requires repository-material
  permission; built-in workers are the default.
