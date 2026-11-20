# Wave G validation report

Validation snapshot: `/private/tmp/abs-wave-g-validation`, based on commit `b42969b9431c635ab9032f057c033bc429323d2f`. Canonical checkout remains on `master`; no commits or changes to snapshot Git metadata were made by the runner. Root approved and applied a narrow baseline update to both canonical and validation working copies; it remains unmerged in the read-only Git index. All tests and exact approved packet files were overlaid into the disposable snapshot; inherited `identity/routes.go` and unrelated conflicted owner files were excluded; the approved ratchet baseline overlay is included. Native FFmpeg/ffprobe were provided with `PATH=/private/tmp/abs-cleanup-checkpoint-20261105/ffmpeg-9.0.2-webp/bin:$PATH` and `FFMPEG_PATH`.

## Passed

- `GOTOOLCHAIN=local GOCACHE=/tmp/abs-wave-g-gocache go build ./...` — pass.
- Same env, `go vet ./...` — pass.
- `/Users/paul/.local/share/mise/installs/golangci-lint/2.15.0/golangci-lint-2.15.0-darwin-arm64/golangci-lint run --config=../.golangci.yml --timeout=5m ./...` — pass, `0 issues`.
- `go run ./tools/dbnest` — pass.
- `go test ./internal/playback -count=1`, `CGO_ENABLED=1 go test -race ./internal/playback -count=1`, and the zero-reset-event focused test with `-count=10` — pass after stabilizing the restart assertion.
- `CGO_ENABLED=1 go test -race ./internal/server -run '^TestManagerStartupRemovesOrphanPlaybackStreams$' -count=1` — pass.
- `CGO_ENABLED=1 go test -race ./internal/catalog ./internal/lists -run 'TestWaveG' -count=1` — pass.
- G2 feed-summary fixture additions in `internal/catalog/batch_test.go` and `internal/catalog/handler_test.go` add only required summary columns; `TestLibraryDeleteClearsLibraryRowsAndRefreshesWatcher|TestBatchDelete|TestDeleteItemEmitsPlaylistEvents` pass in the final snapshot.
- G5 focused tests `go test ./internal/sharing ./internal/identity -run 'TestWaveG' -count=1` — pass. The share test exercises expiry on separate live sessions for cover, track, download, progress and share document; checks row cleanup, one close event and repeat idempotency. The API-key test checks authentication before and denial after deactivate/delete.
- `GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go build -trimpath -ldflags '-s -w' -o /tmp/audiobookshelf-go-arm64-waveg ./cmd/audiobookshelf-go` — pass.

## Fails / blocked

- Full `go test ./... -count=1` — remains blocked by sandbox loopback bind restrictions. The contracttest, emailapi, httputil, identity, notifications, podcasts, providers, realtime, catalog and server packages report attempts to bind `127.0.0.1:0` or `[::1]:0` (`operation not permitted`). No workaround was attempted. The earlier `TestScanGolden` portability mismatch is fixed in `internal/scanner/scan_golden_test.go` by trimming whitespace from macOS `wc -c` output; expected fixture values were not changed. Full `go test ./internal/scanner -count=1` and `CGO_ENABLED=1 go test -race ./internal/scanner -count=1` now pass. The earlier mismatch had titles like `T-       6` instead of `T-6` and downstream duration/chapter differences; see historical generated output `/private/tmp/abs-wave-g-validation/go-backend/internal/scanner/testdata/scan_golden.json.got`.
- Socket.IO real-client probe was explicitly attempted with installed client exposed: `NODE_PATH=/Users/paul/projects/audiobookshelf-go/node_modules ABS_PARITY_REQUIRE_SOCKETIO=1 go test -v ./internal/realtime -run '^TestSocketIO4WebsocketAuthAndPrefixedPath$' -count=1`. It reached test server startup then failed with `listen tcp6 [::1]:0: bind: operation not permitted`; the probe was not skipped for missing client.
- Rollback command was attempted with approved canonical harness and snapshot binary: `PATH=/Users/paul/.local/share/mise/installs/node/22.23.4/bin:/private/tmp/abs-cleanup-checkpoint-20261105/ffmpeg-9.0.2-webp/bin:$PATH NODE_PATH=/Users/paul/projects/audiobookshelf-go/node_modules GOTOOLCHAIN=local GOCACHE=/tmp/abs-wave-g-gocache FFMPEG_PATH=/private/tmp/abs-cleanup-checkpoint-20261105/ffmpeg-9.0.2-webp/bin/ffmpeg node go-backend/tools/parity/replacement-trial.cjs --rollback-only --go-bin /private/tmp/abs-wave-g-validation/audiobookshelf-go --work /private/tmp/abs-wave-g-rollback`. It exited before the journey: Node reported `listen EPERM: operation not permitted 127.0.0.1`. Pinned TS oracle input check was separately verified clean (`git diff --quiet edb1f7efc92ecacdef51aafb4d9d5054ce10604f -- server index.js tsconfig.server.json package.json package-lock.json`, exit 0). Node 22.23.4 is installed.
- SQLC v1.31.1 regeneration/hash comparison passed in `/private/tmp/abs-wave-g-sqlc-20261110`: `make sqlc SQLC=/Users/paul/go/bin/sqlc`, exit 0 in under one second; all 11 generated-directory file hashes were unchanged and the approved source/generated clause hashes match canonical. The earlier interrupted attempt is historical only.

## Ratchet (approved packet growth)

Final shipping-source metrics are 38,034 handwritten Go lines, complexity 8,520, and 115 functions over complexity 15 versus stage-2 values 37,927 / 8,490 / 114 (+107 / +30 / +1); all other metrics are unchanged. Root approved the independent per-area budgets and applied only those three baseline values. `go run ./tools/ratchet` passes with that exact overlay. The baseline remains unmerged in the read-only Git index; no staging or commit was performed.

## Runner environment

`mise ls node` showed 22.23.4, 24.22.2, 26.7.0; explicit Node 22.23.4 was used where required. golangci-lint 2.15.0 was available by absolute path. `mise` emitted a non-fatal warning that its Neovim cache write was denied. The sandbox refused network listeners; no approval escalation or policy workaround was attempted.

## Exact approved overlay manifest

Production source overlays in the validation snapshot:

- `go-backend/internal/catalog/item_delete.go`
- `go-backend/internal/catalog/library_writes.go`
- `go-backend/internal/db/queries/playback.sql`
- `go-backend/internal/db/sqlc/playback.sql.go`
- `go-backend/internal/feeds/close.go`
- `go-backend/internal/lists/handler.go`
- `go-backend/internal/playback/hls.go`
- `go-backend/internal/playback/streams.go`
- `go-backend/internal/scanner/scanner.go`
- `go-backend/internal/server/manager.go`

New regression tests in the validation snapshot:

- `go-backend/internal/backup/wave_g_transition_test.go`
- `go-backend/internal/catalog/wave_g_transition_test.go`
- `go-backend/internal/identity/wave_g_security_test.go`
- `go-backend/internal/lists/wave_g_transition_test.go`
- `go-backend/internal/playback/wave_g_transition_test.go`
- `go-backend/internal/playback/lifecycle_test.go` (zero-time reset behavior and observable restart assertion)
- `go-backend/internal/podcasts/wave_g_transition_test.go`
- `go-backend/internal/scanner/wave_g_transition_test.go`
- `go-backend/internal/scanner/scan_golden_test.go` (portable `wc -c` whitespace handling; golden expectations unchanged)
- `go-backend/internal/server/wave_g_playback_startup_test.go`
- `go-backend/internal/server/wave_g_transition_test.go`
- `go-backend/ratchet-baseline.json` (root-approved 3-metric update)
- `go-backend/internal/sharing/wave_g_transition_test.go`
- `go-backend/internal/userapi/wave_g_transition_test.go`
- `go-backend/internal/catalog/batch_test.go` and `go-backend/internal/catalog/handler_test.go` (two minimal feed-summary fixture schema additions; no assertion changes)

The overlay excludes inherited owner WIP `go-backend/internal/identity/routes.go`, conflicted `ROADMAP.md`, `handover.md`, and `internal/catalog/quickmatch.go`; it also excludes unrelated `.codemap/`, `TODO.md`, architectural assessment, `quickmatch_relations_test.go`, and concurrent R1 online-users work (`internal/userapi/online_users_test.go` plus its associated source delta). Preserve that R1 work; it is outside the Wave G manifest and its metric/gate claims. The root-approved `ratchet-baseline.json` overlay is included. The snapshot was based on `b42969b9431c635ab9032f057c033bc429323d2f`; it is a disposable validation tree, not a branch or canonical checkout.

## Review and current disposition

Architect review accepts the necessary Wave G growth within the previously assigned per-area budgets: catalog/feed/list ≤100 production LOC and ≤25 complexity; playback ≤80 LOC and ≤20 complexity; scanner ≤15 LOC and ≤3 complexity; high-complexity function growth at most +1 per applicable area; no map, dependency or `[]any` growth. Final aggregate ratchet result is +107 LOC, +30 complexity, and +1 function over complexity 15 (38,034 / 8,520 / 115 versus 37,927 / 8,490 / 114); other tracked metrics are unchanged. The exact three baseline metrics were approved and applied by root; `make ratchet` passes.

The behavior audit and 15 transition exercises are complete: eight initially red transitions now have focused green evidence, and seven passed without production changes. The HLS playlist suffix and zero-reset-event regressions pass, including playback race tests; the final playback test file is synchronized in canonical and validation copies (SHA-256 `d1bb6adff4d9988255c194df84607079a4019604ed2dd9e91fcd2ea7da621bb7`). Full scanner/full-race pass after the `wc` portability fix. `make ratchet` and SQLC v1.31.1 regeneration/hash comparison pass with the root-approved baseline. G2's two feed-summary fixture additions and focused deletion tests also pass. Integration acceptance remains blocked: a fresh 2026-11-10 listener probe still returns `operation not permitted`; full suite and Socket.IO cannot complete, and the earlier TS rollback attempt exited before its data-reopen journey on `listen EPERM`. The prior rollback binary predates the final HLS change; any future rollback must explicitly build the snapshot binary first. This is **not** an accepted integration packet; no commit, push, deployment, or release is claimed.

Next steps are to rerun full tests, Socket.IO interop, and rollback where loopback binding is available. Before a future rollback attempt, rebuild the native snapshot binary explicitly from the validation snapshot with `go build -o /private/tmp/abs-wave-g-validation/audiobookshelf-go ./cmd/audiobookshelf-go`; `go build ./...` only cache-compiles and the previous binary predates the final HLS repair. Keep N2 unchecked until these gates pass. Current final evidence is collated in [the integration validation update](INTEGRATION-VALIDATION.md).
