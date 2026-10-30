# Agent guidance

## North star

Deliver a verified operational drop-in replacement for pinned Audiobookshelf v2.37.2. Match externally observable HTTP, realtime, persistence, filesystem, and failure behavior. Keep full parity unclaimed until source inventory is reconciled and strict verification gates pass.

## Current verified slice (2026-10-30)

The recorded default-mode sampled HTTP differential passes 129/129. A proxy-mode run adds nine passing email-device response/readback/event/state cases. The latest default-mode run, with only loopback whitelisted for fixtures, passes 157/157 and includes SMTP test behavior plus 12 notification test-route cases for ACL, error/success delivery, response shapes, and unchanged firing state. Structural ledger validation passes for 15 tasks and 237 capability records. Strict verification still fails with 1,627 diagnostics across 236 records; all 226 route compatibility statuses remain unverified and full parity is not established. OIDC remains partial despite a fresh 20/20 local-provider differential that includes successful mobile PKCE token exchange and persisted user/session effects. Global notification event lifecycle/queue behavior, SMTP TLS/auth, send-to-device attachment delivery, and broader ACL remain open; notification CRUD samples pass, with one malformed-request process-crash divergence retained as unresolved. See `handover.md` and the active task ledger for scope limits and the next action.

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
