# Handover

Updated 2026-10-30. Continue from `docs/agent-work/tasks/TASK-20261030.md`; do not restart the earlier scanner, ACL, or destructive-lifecycle packets.

## Current state

- Upstream is pinned to v2.37.2 at `edb1f7efc92ecacdef51aafb4d9d5054ce10604f`.
- W5 ACL differential passes 11/11; W6 destructive lifecycle passes nine scenarios with zero divergences. Their limits and normalizations are recorded in the task ledger and capability records.
- Structural ledger validation passes for 15 tasks and 237 capability records. Strict `-require-verified -require-invariants` fails with 1,758 diagnostics; full parity is not established.
- OIDC now has a pinned Node-versus-Go local-provider differential: auto-registration success, provider error, state replay, expired ID token, existing-user email linking, and disabled-auto-registration denial pass 6/6. Red/green fixes preserve linked-user `extraData` member order and emit admin-only `user_added` on auto-registration; the latter is verified on real Node and Go sockets. See `go-backend/contracts/parity/oidc-differential-{report,provenance}.json`. Mobile flow and detailed cookies remain unverified, so overall OIDC capability is still partial.
- Cover PATCH/DELETE and remote upload, download variants, email/notification ACL, and realistic browser playback/session recovery remain open.

## Gotchas

- Run the pinned Node server through `mise exec node@22 -- node ...`; the host Node 26 runtime is not compatible with its dependency tree.
- Use isolated copies of the fixture for each implementation. Rebuild the Go binary from the current source before differential runs; stale binaries previously produced misleading ACL failures.
- Re-stat copied fixture files because inode values change across copies. Normalize only fixture-dependent IDs, paths, timestamps, and inodes; keep null-versus-empty and response shape strict.
- Node and Go database copies must never share writable metadata, config, or backup paths. The fixture path rewriter must handle JSON-valued settings columns.
- Rapid backup creation can collide at minute precision; lifecycle/count behavior was checked, distinct-file retention rolloff remains unverified.
- Existing Go OIDC unit tests validate Go behavior only. Use the paired differential report for cross-implementation claims, and keep unexercised OIDC variants unverified.

## Next executable action

Add OIDC invalid signature/issuer/audience/nonce cases. Then add cover PATCH/DELETE and remote-URL upload cases, followed by download edge cases and email/notification ACL coverage.
