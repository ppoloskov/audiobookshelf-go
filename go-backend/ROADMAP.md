# Roadmap

### PB01 · Package and object ownership review (2026-11-10)

- [ ] IN REVIEW — owner requested better package division and object
  boundaries. [Source-backed review](docs/PACKAGE-BOUNDARIES.md) records
  catalog scope, misleading helper ownership, dependency-rule pressure,
  proposed owners and migration sequence. First runtime packet ACCEPTED
  2026-11-10: email defaults and e-reader device policy moved from
  `settingsdoc` to `internal/emailsettings`; `emailapi`, `identity` and
  `userapi` consume it, and generic settings-row `Load`/`Save` remain in
  `settingsdoc`. Catalog decomposition and unrelated settings behavior were
  not changed. Architect rerun verified: build, vet, full suite with the
  documented FFmpeg prefix, lint (0 issues), 20 dependency fixtures,
  `make ratchet` +0 on every metric (no baseline edit; the 38,119 baseline
  belongs to 74734477), and that the identity nil→defaults change is
  unobservable (Load substitutes only for missing/null rows; VisibleTo is
  empty either way). Follow-ups recorded in handover: email-settings
  document to struct with json tags (new agents.md rule 1; also fixes the
  unchecked `device["email"].(string)` in emailapi sendEbook), then catalog
  decomposition after explicit handover.
- Documentation validation only: source/test/lint inspection and targeted
  whitespace/link checks. Existing implementation cards retain their state.

Updated 2026-11-09. Open work only. Finished and obsolete cards are listed in
[archive/reviews/2026-11-09-roadmap-pruned.md](archive/reviews/2026-11-09-roadmap-pruned.md);
older detail in [the pre-takeover roadmap](archive/reviews/2026-11-04-roadmap-before-takeover.md).
Current state: [handover.md](handover.md). Permanent contracts:
[ARCHITECTURE.md](ARCHITECTURE.md). Worker/check policy: [AGENTS.md](AGENTS.md).
Decision log and ledger: [docs/REFACTOR-LOG.md](docs/REFACTOR-LOG.md),
[docs/SIMPLIFICATION.md](docs/SIMPLIFICATION.md).

All development is on `master`; no branches. Every push is gated: full local
CI mirror plus the TS rollback check, chained (`ci-local.sh && rollback && git
commit && git push`). Manual deployment, new libraries and route removals need
separate owner approval. Every open card has a `- [ ]` checkbox; mark it
`- [x] DONE` in the commit that completes it, with actual check results.
Preserve functionality; package moves and feature removal are not savings.

## 1. Refactor work, in order (handover.md "Next, in order")

### N1 · Step 3 behavioural test-quality pass

- [x] DONE — Step 3 complete at `b42969b9`; T1–T5 salvage and assertion audit
  are recorded in `handover.md` and archived review. Existing accepted tests,
  fixtures, and compatibility assertions remain part of the contract.

### N2 · Wave G transition and side-effect audit

- [x] DONE — all 15 G1–G6 transitions have named tests. Eight initially red
  transitions pass with bounded fixes; seven passed without production changes.
  The HLS playlist suffix and zero-time reset-event assertions pass with real
  media and playback race coverage.
- **Final canonical validation (2026-11-10):** the full Go suite passes with
  real Socket.IO required, along with build, vet, golangci-lint v2.15.0,
  ratchet/dbnest, SQLC v1.31.1, Linux amd64/arm64 builds, required
  source/contract checks and TS v2.37.2 rollback. See the final validation
  addendum for exact commands, hashes and environment limits.
- The measured canonical metrics are 38,119 handwritten LOC / 8,531
  complexity / 115 functions over 15 / 461 `map[string]any` / 202 `[]any`.
  N2's completion is recorded in the accepted source commit. Remote CI is
  verified after push under N3; deployment remains separately authorized.

### R1 · Remove the OpenSessions JSON round trip

- [x] DONE — pass the typed session DTO slice through the userapi callback as
  opaque JSON and remove the marshal/unmarshal map conversion and ignored
  errors. Preserve the nonnil array shape, explicit nullable fields and admin
  authorization gate. The typed endpoint regression, full userapi/playback
  package tests and targeted races pass. R1 independently reduces production
  LOC by 2 and maps by 3; all other metrics are unchanged.
- Comment cleanup and test cleanup were not selected.

### RT1 · Abort full realtime transport queues promptly

- [x] DONE — abort promptly when a bounded outbound queue fills, keeping normal
  close graceful, preserving the first close reason and keeping sends
  nonblocking. The unchanged full-buffer regression, realtime package/race
  tests, and final canonical full suite pass. Actual growth is +16 LOC / +2
  complexity within the assigned +30/+8 budget; all other metrics are
  unchanged.

### QM01 · Preserve quickmatch and relation metadata semantics

- [x] DONE — preserve existing relations for empty converted match lists and
  apply duplicate series updates sequentially against the current relation
  cache. Missing sequence metadata preserves the stored position; explicitly
  missing, null, empty text and numeric text remain distinct. The real adapter
  stores raw numeric zero as `"0"`; the prior zero test used a fake finder and
  bypassed that adapter, so it was corrected. PATCH still maps missing and
  non-string values to explicit NULL while string values, including empty,
  remain explicit text; scanner blanks remain absent.
- The shared Series input carries presence as `*sql.NullString`; KeepSequence
  and done-map branches were removed after the symbol check. The existing
  Go characterization retains event ordering, with no second SQL engine or
  provider normalization change.
- **Budget:** assigned +50 LOC/+12 complexity, zero growth in high-complexity
  functions, maps, `[]any`, dependencies and schema. Actual was +26 LOC/+5
  complexity with every other ratchet metric unchanged.
- **Check:** focused empty-list, duplicate (2 then 1), missing-sequence,
  rollback, raw-value and PATCH regressions pass; catalog/itemwrite/scanner
  package tests and races pass, as do the final canonical full suite and TS
  rollback.

### N3 · Step 4, final check and report

- [ ] PENDING REMOTE CI — canonical local verification and the final validation
  report are complete. Commit and push the accepted source tree to `master`,
  then verify the required GitHub CI jobs on that pushed commit. Keep N3 open
  until remote CI passes; deployment remains separate.
