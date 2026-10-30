# Parity development status

Updated 2026-10-30 during continued parity work. This remains an experimental backend, not a drop-in replacement. The recorded default-mode Node-vs-Go HTTP differential passes 129/129, including admin email/notification settings and notification CRUD samples. A proxy-mode run adds nine email-device cases; the SMTP differential passes 144/144; and the latest default-mode run with loopback-only fixture whitelisting passes 147/147, adding selected notification test-route errors. Successful notification test delivery/state effects, SMTP TLS/auth, and send-to-device attachment delivery remain open. The isolated omitted-`urls` notification probe reproduces an upstream process-exit divergence and remains unresolved. OIDC passes 20/20 selected cases but remains partial. The strict ledger gate fails with 1,627 diagnostics and all 226 route compatibility statuses remain unverified. See root `handover.md` and `docs/agent-work/tasks/TASK-20261030.md` for precise evidence, limitations, and the next work packets. The active-work descriptions below record implementation scope, not running agents.

Target: upstream v2.37.2 (`edb1f7efc92ecacdef51aafb4d9d5054ce10604f`). User instruction is to continue until feature parity, using Luna implementation agents. Do not treat a passing sampled flow as complete route parity.

## Tested checkpoint

`8aac9cd` contains the core Go runtime and source-backed compatibility tooling. Its staged tree was tested in an isolated checkout: CGO-disabled package tests and vet passed. Race-detector checks also passed for identity, playback, realtime, scanner and server packages in that snapshot.

The real source-created differential flow grew from 17 passing core scenarios to 22 with bookmark lifecycle cases. See sanitized reports and their provenance under `contracts/parity`. HTTP/Socket.IO requests use actual Node and Go executables; no Node runtime fallback exists in the Go implementation.

## Active work

- Catalog: query variants, personalized shelves, library/item mutations, covers, media files, batch operations, metadata persistence.
- User API: full MeController surface, listening stats/history, credential-change session handling, collections/playlists and API keys.
- Scanner: local import, sidecars/OPF precedence, repeated scans, watcher scheduling and cancellation, source-compatible metadata writing.
- Identity: local and OIDC flows, shared progress DTOs, settings side effects, notifications/email; RSS and public shares are being added.
- Media: playback/direct/HLS and offline/history flows, FFmpeg merge/embed jobs, backup import/export/restore, podcast feeds and download scheduling.
- Providers: custom providers and built-in metadata/search provider contracts.
- Runtime: lifecycle, manager refresh callbacks, safe backup restore graph replacement, CLI/configuration and static frontend integration.

Implementation and tests for these areas are evolving. Some supported source routes and variants still return explicit failures or are absent; full parity is not reached.

## Verification requirements

Mutation fixtures must have independent database, metadata **and media** trees. Earlier exploratory scanner results against shared media are provisional and cannot establish compatibility. Controlled path roots may be normalized only while preserving relative suffixes and types.

One multi-client realtime probe timed out under the full suite; 30 subsequent repetitions and the race-detector snapshot passed. Keep the test and investigate any recurrence rather than masking it.

Bidirectional backup interoperability has been exercised using an actual upstream-created archive and a Go-created archive imported by upstream. Executable restore, scheduler, permission, rollback and mutation tests remain distinct acceptance gates.

## Working conventions

- Keep active packages compile-safe between edits. Assemble large multi-function changes outside the module or in staging, then publish the complete function set atomically.
- Agents own disjoint source paths. Coordinate interfaces directly; root owns dependency changes, integration review and commits.
- The optional `ABS_GO_READ_ONLY=1` uses a restricted development surface without background writers.
- Runtime tools and generated fixtures may live in the workspace sibling `.runtime` directory; keep generated media, binaries, dependency caches and credentials out of Git.
- Remote publishing previously failed: GitHub app write returned 403 and normal Git lacked push credentials. Continue local implementation independently of publishing.
