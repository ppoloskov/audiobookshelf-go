# Handover

Updated 2026-10-30. Continue from `docs/agent-work/tasks/TASK-20261030.md`; do not restart the earlier scanner, ACL, or destructive-lifecycle packets.

## Current state

- Upstream is pinned to v2.37.2 at `edb1f7efc92ecacdef51aafb4d9d5054ce10604f`.
- W5 ACL differential passes 11/11; W6 destructive lifecycle passes nine scenarios with zero divergences; the fresh sampled HTTP suite passes 101/101. Cover samples include multipart upload and URL-over-file precedence, local URL upload and redirect, unsupported image rejection, private-network URL rejection with unchanged state, successful/repeated PATCH with exact Express JSON headers, stale-cache purge, unrelated `cover.svg` retention, cover reads/cache, and delete cleanup. A separate `EXP_PROXY_SUPPORT=1` probe passes for 307 rejection with unchanged state; that mode's full run intentionally skips private-address testing because upstream disables its SSRF filter. Evidence limits are recorded in the task ledger and cover capability.
- Structural ledger validation passes for 15 tasks and 237 capability records. Route coverage attaches 101 passing full-suite variants across 54 route candidates; all 226 route compatibility statuses remain unverified. Strict `-require-verified -require-invariants` fails with 1,681 diagnostics; full parity is not established.
- OIDC now has a pinned Node-versus-Go local-provider differential with 10/10 matched cases: auto-registration success, provider error, state replay, expired ID token, existing-user email linking, disabled-auto-registration denial, and invalid nonce/issuer/audience/signature. The selected admin-only `user_added` event is verified on real sockets. Red/green fixes preserve linked-user `extraData` member order and emit admin-only `user_added` on auto-registration; the latter is verified on real Node and Go sockets. See `go-backend/contracts/parity/oidc-differential-{report,provenance}.json`. Mobile flow, flow-state expiry, and detailed cookies remain unverified, so overall OIDC capability is still partial.
- Cover URL success, one default 307 redirect, one blocked RFC1918 target, and one proxy-mode 307 rejection are verified. Alternate-path PATCH, event/updatedAt effects, 10 MiB request boundary, 301/302 redirect chains, and broader SSRF/DNS behavior remain open. Download variants, email/notification ACL, and realistic browser playback/session recovery remain open. OIDC mobile flow, flow-state expiry, detailed cookie attributes, and general Socket.IO coverage remain open.

## Gotchas

- Run the pinned Node server through `mise exec node@22 -- node ...`; the host Node 26 runtime is not compatible with its dependency tree.
- Use isolated copies of the fixture for each implementation. Rebuild the Go binary from the current source before differential runs; stale binaries previously produced misleading ACL failures.
- Re-stat copied fixture files because inode values change across copies. Normalize only fixture-dependent IDs, paths, timestamps, and inodes; keep null-versus-empty and response shape strict.
- Node and Go database copies must never share writable metadata, config, or backup paths. The fixture path rewriter must handle JSON-valued settings columns.
- Rapid backup creation can collide at minute precision; lifecycle/count behavior was checked, distinct-file retention rolloff remains unverified.
- Existing Go OIDC unit tests validate Go behavior only. Use the paired differential report for cross-implementation claims, and keep unexercised OIDC variants unverified.

## Next executable action

Continue with email/notification ACL and download edge cases, then OIDC mobile/cookie and flow-state-expiry, plus realistic browser playback/session recovery. Keep the PATCH alternate-path, event, updatedAt and request-size variants open.
