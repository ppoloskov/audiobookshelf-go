# Wave G integration validation — historical status

> This document records an earlier pre-QM01 integration attempt. Its
> listener-blocked and unmerged-index conclusions are superseded by the
> [final canonical validation addendum](../refactor-final-validation-20261110.md).
> Keep the detailed earlier failures below as history; use the linked addendum
> for current local gate status.

## Status update — 2026-11-10

- SQLC v1.31.1 regeneration/hash comparison passed in isolated snapshot `/private/tmp/abs-wave-g-sqlc-20261110`: exact repository Makefile target `make sqlc SQLC=/Users/paul/go/bin/sqlc`, generator `/Users/paul/go/bin/sqlc` v1.31.1, 45-second timeout, exit 0 in under one second, no stderr. SHA inventory of all 11 generated-directory files had no added, removed, or changed files; the approved SQL source/generated clause hashes match canonical. This completed run supersedes the earlier interrupted attempt below.
- The final G4 playback test-only revision is now synchronized between canonical and validation copies; SHA-256: `d1bb6adff4d9988255c194df84607079a4019604ed2dd9e91fcd2ea7da621bb7`.
- G2 reviewed and verified the two approved feed-summary fixture schema updates in `internal/catalog/batch_test.go` and `internal/catalog/handler_test.go`. The named focused catalog tests pass in the final snapshot: `TestLibraryDeleteClearsLibraryRowsAndRefreshesWatcher|TestBatchDelete|TestDeleteItemEmitsPlaylistEvents`.
- A fresh listener probe on 2026-11-10 still fails with `operation not permitted`; full suite, Socket.IO, and rollback acceptance remain blocked. No integration acceptance, commit, or push is claimed.
- An ordinary Git resolution attempt on 2026-11-10, `git add -- go-backend/internal/catalog/quickmatch.go go-backend/ratchet-baseline.json`, failed with exit 128: Git could not create `.git/index.lock` (`Operation not permitted`). The working contents are resolved, but the index remains unmerged. Completing staging requires Git write permission in the execution environment.

Base snapshot: `/private/tmp/abs-wave-g-validation`, HEAD `b42969b9431c635ab9032f057c033bc429323d2f`. Validation tree excludes owner WIP `internal/identity/routes.go`, conflict files, and unrelated concurrent R1 work. Canonical checkout remains on master. No commits, pushes, or source edits by the integration runner. Full suite is not green, so this is not shipping acceptance.

## Final offline gates completed

- `GOTOOLCHAIN=local GOCACHE=/tmp/abs-wave-g-gocache go build ./...` — PASS.
- Same environment `go vet ./...` — PASS.
- `golangci-lint 2.15.0 run --config=../.golangci.yml --timeout=5m ./...` — PASS, 0 issues.
- `go run ./tools/dbnest` — PASS.
- `go run ./tools/ratchet` after root's approved baseline update — PASS, exact zeros: 38,034 handwritten lines, 8,520 complexity, 115 functions over complexity 15; all other tracked metrics unchanged.
- Linux arm64 cross-compile with `GOOS=linux GOARCH=arm64 CGO_ENABLED=0`, trimpath/stripped output — PASS (`/tmp/audiobookshelf-go-arm64-waveg`).
- `CGO_ENABLED=1 go test -race ./internal/scanner -count=1` — PASS after G1 fixed `wc -c` fixture whitespace. This run is recorded in `/tmp/wave-g-final-fulltests.log` companion evidence only if applicable; G1 independently reports full scanner package and race pass.
- `go test ./internal/sharing ./internal/identity -run 'TestWaveG' -count=1` — PASS.
- `CGO_ENABLED=1 go test -race ./internal/catalog ./internal/lists -run 'TestWaveG' -count=1` — PASS.
- G4 worker's final HLS flake repair was verified in isolated `/private/tmp/abs-wave-g-final-race`: targeted zero-start reset race with `-count=10` and full `go test -race ./internal/playback -count=1` — PASS. The shared validation snapshot has not yet received this final test-only `wave_g_transition_test.go` revision; my full playback race attempt before it failed at line 251 because the async reset flag was sampled before/after the restart transition. G4 reports the final test now waits for reset completion and asserts restart state. No new runner command was started after root's report-only direction.

## Full suite and network dependent checks

Final full `go test ./... -count=1` was run with `NODE_PATH=/Users/paul/projects/audiobookshelf-go/node_modules` and the native FFmpeg prefix. Log: `/tmp/wave-g-final-fulltests.log`. Scanner's former golden mismatch is gone. The remaining failed package tests are listener denials: catalog, contracttest, emailapi, httputil, identity, notifications, podcasts, providers, realtime and server report `listen ... bind: operation not permitted` on loopback (`127.0.0.1` or `[::1]`). The realtime JS Socket.IO client was installed and reached test server startup; it was not skipped for a missing module.

A final explicit actual-client probe was attempted:

```text
NODE_PATH=/Users/paul/projects/audiobookshelf-go/node_modules ABS_PARITY_REQUIRE_SOCKETIO=1 go test -v ./internal/realtime -run '^TestSocketIO4WebsocketAuthAndPrefixedPath$' -count=1
```

It failed before the socket exchange with `listen tcp6 [::1]:0: bind: operation not permitted`.

Rollback was previously attempted with Node 22.23.4, the canonical TS oracle, and a snapshot-built Go binary:

```text
node go-backend/tools/parity/replacement-trial.cjs --rollback-only --go-bin /private/tmp/abs-wave-g-validation/audiobookshelf-go --work /private/tmp/abs-wave-g-rollback
```

The run exited before seeding/rollback because Node failed `listen EPERM: operation not permitted 127.0.0.1`. This was before the last HLS/test-only repair; a post-repair rollback was not completed. The pinned TS oracle input diff was clean at the earlier check. No listener bypass was attempted.

## SQLC generation/hash status

The earlier invocation in `/private/tmp/abs-wave-g-validation` was interrupted by root after approximately 354 seconds, without final status or generated-output hashes; at that time SQLC was unverified. The completed 2026-11-10 run above supersedes that historical outcome.

## Required next gates

1. Synchronize G4's final test-only playback file revision into the validation snapshot and confirm source hash against the G4 final manifest.
2. Re-run full suite/Socket.IO/rollback where loopback access is available. The current sandbox denies listeners, so those acceptance gates remain blocked here.
3. Keep baseline unchanged from the architect-approved 38,034 / 8,520 / 115 values; do not commit or push until required CI and rollback gates pass.
