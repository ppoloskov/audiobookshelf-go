# Agent guidance

## North star

Deliver a verified operational drop-in replacement for pinned Audiobookshelf v2.37.2. Match externally observable HTTP, realtime, persistence, filesystem, and failure behavior. Keep full parity unclaimed until source inventory is reconciled and strict verification gates pass.

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
