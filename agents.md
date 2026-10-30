# Agent guidance

## North star

Deliver a verified operational drop-in replacement for pinned Audiobookshelf v2.37.2. Match externally observable HTTP, realtime, persistence, filesystem, and failure behavior. Keep full parity unclaimed until source inventory is reconciled and strict verification gates pass.

## Current verified slice (2026-10-30)

The latest fresh default-mode differential passes 176/176, including 18 global notification event cases for configured/unconfigured behavior, admin denial, repeat delivery, persisted state, intentional failure threshold, and `notifications_updated` Socket.IO broadcasts. OIDC now has 24/24 selected cases, including a red/green invalid mobile PKCE verifier failure/replay and web logout cookie/session effects, and a delayed web callback after ten minutes; OIDC remains partial because exact session expiry/renewal, failure-cookie clearing, logout error/mobile/allDevices behavior, and other callback/provider cases remain open. Earlier separate runs cover SMTP and notification per-ID delivery; proxy-mode email-device samples also have evidence. Covers, downloads, SMTP TLS/auth, send-to-device attachments, notification queue/concurrency and other lifecycle events, broader ACL, remaining routes, and realistic browser playback/session recovery remain open. One malformed notification request still has an unresolved upstream process-exit divergence. Strict verification remains failing; full parity is not established. See `handover.md` and the active task ledger for exact counts, scope limits, and next actions.

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
