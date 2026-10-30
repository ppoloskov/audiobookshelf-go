# Agent guidance

## North star

Deliver a verified operational drop-in replacement for pinned Audiobookshelf v2.37.2. Match externally observable HTTP, realtime, persistence, filesystem, and failure behavior. Keep full parity unclaimed until source inventory is reconciled and strict verification gates pass.

## Current verified slice (2026-10-30)

The latest fresh sampled HTTP differential is 129/129, including cover upload/PATCH/delete behavior, real reader-token ACL responses for user writes/email settings/notifications, admin email/notification settings defaults and mutations, and seven per-file download cases covering ranges, missing records and disk files, permission order, and Express error responses. The ACL cases use reader credentials after correcting `comparePair` token precedence and compare Express denial bodies/headers. A separate `EXP_PROXY_SUPPORT=1` probe confirms one 307 rejection with unchanged state; that run intentionally skips private-address probing because upstream disables SSRF filtering in that mode. Structural ledger validation passes for 15 tasks and 237 capability records. Strict verification still fails; all 226 route compatibility statuses remain unverified and full parity is not established. OIDC and email/notification route behavior remain partial; notification create/update/delete samples now pass, with one malformed-request process-crash divergence retained as unresolved. See `handover.md` and the active task ledger for scope limits and the next action.

## Source of truth

- Compatibility contract: `docs/agent-work/CONTRACT.md`.
- Active work, evidence, blockers, and next action: `docs/agent-work/tasks/TASK-20261030.md`.
- Per-operation obligations and evidence: `docs/agent-work/capabilities/`.
- Pinned TypeScript implementation is the oracle. Do not edit it as part of the rewrite.

## Working rules

- Compare the public result and resulting state against pinned upstream; fix only source-backed, reproducible differences.
- Preserve meaningful distinctions such as `null` versus empty values. Normalize only documented fixture-dependent IDs, paths, timestamps, and inode values.
- Keep each server on its own disposable fixture copy. Test state transitions and failure effects, not only successful responses.
- Add evidence and limitations to the owning capability and task ledger. Never promote unexercised routes or infer parity from unit tests/builds.
- Keep changes narrow; protect existing user changes and do not reset or clean the worktree.
- Run tests and verification appropriate to the compatibility slice, record actual results, and update this file, the README, and `handover.md` when project guidance or current state changes.
