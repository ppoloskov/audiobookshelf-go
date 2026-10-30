# Handover

Updated 2026-10-30. Continue from `docs/agent-work/tasks/TASK-20261030.md`; do not restart the earlier scanner, ACL, or destructive-lifecycle packets.

## Current state

- Upstream is pinned to v2.37.2 at `edb1f7efc92ecacdef51aafb4d9d5054ce10604f`.
- W5 ACL differential passes 11/11; W6 destructive lifecycle passes nine scenarios with zero divergences; the latest fresh sampled HTTP suite passes 103/103. It includes cover upload/PATCH/delete samples plus real reader-token denials for user create/delete (403), email settings GET (404), and notifications GET (403), with Express body/header matching. A token-precedence bug in the differential harness was fixed so per-case reader tokens are no longer replaced by the root token. The new email/notification evidence covers only those denial branches. A separate `EXP_PROXY_SUPPORT=1` probe passes for 307 rejection with unchanged state; that mode's full run intentionally skips private-address testing because upstream disables its SSRF filter. Evidence limits are recorded in the task ledger and capabilities.
- Structural ledger validation passes for 15 tasks and 237 capability records. The refreshed route-coverage artifact maps 99 passing variants from the 103-case report across 56 route candidates; all 226 route compatibility statuses remain unverified. Strict `-require-verified -require-invariants` fails with 1,681 diagnostics across 236 capability records. Full parity is not established.
- OIDC now has a pinned Node-versus-Go local-provider differential with 10/10 matched cases: auto-registration success, provider error, state replay, expired ID token, existing-user email linking, disabled-auto-registration denial, and invalid nonce/issuer/audience/signature. The selected admin-only `user_added` event is verified on real sockets. Red/green fixes preserve linked-user `extraData` member order and emit admin-only `user_added` on auto-registration; the latter is verified on real Node and Go sockets. See `go-backend/contracts/parity/oidc-differential-{report,provenance}.json`. Mobile flow, flow-state expiry, and detailed cookies remain unverified, so overall OIDC capability is still partial.
- Cover URL success, one default 307 redirect, one blocked RFC1918 target, and one proxy-mode 307 rejection are verified. Alternate-path PATCH, event/updatedAt effects, exact 10,000,000-byte parser boundary, 301/302 redirect chains, and broader SSRF/DNS behavior remain open. Download variants and normal/admin email/notification routes, mutations, and state effects remain open. OIDC mobile flow, flow-state expiry, detailed cookie attributes, and realistic browser playback/seek/transcode/session recovery remain open. Route-wide normal/error/edge coverage and invariants remain the primary parity gate.

## Gotchas

- Run the pinned Node server with Node v22.23.4 (`mise exec node@22 -- node ...` or the installed version's direct path); the host Node 26 runtime is not compatible with its dependency tree. Run local differential/package tests with the established loopback permission and `SSRF_REQUEST_FILTER_WHITELIST=127.0.0.1` for local image-source samples.
- Use isolated copies of the fixture for each implementation. Rebuild the Go binary from the current source before differential runs; stale binaries previously produced misleading ACL failures.
- Re-stat copied fixture files because inode values change across copies. Normalize only fixture-dependent IDs, paths, timestamps, and inodes; keep null-versus-empty and response shape strict.
- Node and Go database copies must never share writable metadata, config, or backup paths. The fixture path rewriter must handle JSON-valued settings columns.
- Rapid backup creation can collide at minute precision; lifecycle/count behavior was checked, distinct-file retention rolloff remains unverified.
- Existing Go OIDC unit tests validate Go behavior only. Use the paired differential report for cross-implementation claims, and keep unexercised OIDC variants unverified.

## Next executable action

Continue email/notification normal/admin response and mutation/state cases, download errors/edge cases, OIDC mobile/cookie and flow-state-expiry, and realistic browser playback/seek/transcode/session recovery. First reconcile route coverage against the latest 103-case report and rerun structural plus strict ledger gates. Keep cover alternate-path PATCH, event, updatedAt, exact-size boundary, redirects and broad SSRF/DNS variants open.
