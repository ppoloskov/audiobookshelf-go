> **Historical assessment — not current status.** This inherited note is preserved
> as evidence. Its counts, blockers and sequencing are superseded by
> [ROADMAP.md](ROADMAP.md) and [handover.md](handover.md), updated 2026-11-04.
> Do not use it as an execution queue or infer approvals from it.

I read STATUS.md, ROADMAP.md, ARCHITECTURE.md, handover.md, both 2026-10-31
phase reviews, AGENTS.md, READMEs, the work-queue spec, CONTRACT.md, all four
audits, and the task ledgers via a subagent — then reconciled against the live
tree (make ratchet, route-coverage check, git log/diff).

Reconciliation: docs vs. current gates

| Claim in docs                                                                | Live evidence now                                                                                                                                                                                                                       |
|------------------------------------------------------------------------------|-----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------|
| STATUS.md "core parity PASS 193/193"                                         | Consistent with remediation task; generated 2026-10-31                                                                                                                                                                                  |
| make ratchet green (map: 1126)                                               | Passes at 1115 — an in-flight uncommitted packet lowered the baseline again (33 files uncommitted, ~391 insertions)                                                                                                                     |
| handover.md: "route inventory --check passes"                                | Currently exits 1 (stale) — code has moved past the last regeneration                                                                                                                                                                   |
| Roadmap says phases 3+ are future                                            | Git log shows Phase 3 leaf packets already committed: httputil writers (12→1), ids, media, auth JWT, dbtime parse. Ratchet: EmitFunc 0, Set* 0, writeJSON copies 1, httptest-in-prod 0 — Phase 3 effectively done, most of Phase 4 done |
| docs/specs/planned/parity-finalization-work-queue.md footer: "active at F09" | Superseded by ROADMAP.md (stale doc, kept for history)                                                                                                                                                                                  |
| capabilities/README: ledger is "the active ledger"                           | Contradicts ROADMAP Phase 0 (archived evidence only) — stale (review finding V9 residual)                                                                                                                                               |

Net: code is ahead of the docs. The 33 uncommitted files (runtime, realtime,
scanner, podcasts, providers, playback…) are an unfinished packet that re-staled
the route inventory.

1) How far from the goal?

Your goal: deployable image, upstream web UI + existing mobile clients, dropped
onto an existing installation (books, DB, progress, tokens). The good news — the
risky parts for your goal are mostly done or partly done:

- Existing DB: works — refuses non-pinned migration state (migrationsMeta =
  2.37.2), never migrates implicitly. Reading a real installation is a core case
  (existing-db suite, rollback-check pass).
- Tokens: one JWT signer/verifier now (auth packet), token lifecycle + OIDC
  flows differentially verified (OIDC 24/24 at last count).
- Progress/playback: sessions, progress, bookmarks differentially verified;
  DATETIME write format now byte-matches upstream Sequelize (dbtime packet —
  this was a real corruption/reordering risk, now closed).
- Clients: Chromium browser acceptance passed on both sides (2026-10-30); the
  official mobile clients were in scope of captured differential cases. The
  stock Vue frontend is kept as-is and served by the Go binary.

The honest gap (why you can't switch yet): coverage is sampled, not total.
Upstream has 226 route registrations; the executable core catalog covers 66
routes / 164 variants, and the per-route capability ledger still marks
essentially everything below "verified." Plus the open behavioral divergences
below. Rough distance: verification infrastructure ~done, architecture 60%
(ratchet floor reached for phases 0–4), wire/DTO depth 10% (1,115 map[string]any
left), full route parity maybe 35–40%, deployment packaging 0%. I'd call it "one
hard month of focused packets" from a trial swap on your own install, not from a
claimable parity certificate — the product track explicitly says "done means the
target clients work, not that every long-tail variant is ledger-verified," which
is the right yardstick for your goal.

2) What remains — trivial vs. hard

Trivial (mechanical, no design risk):

- Commit the in-flight packet, regenerate route inventory, rerun make parity +
  make status, keep docs from claiming stale facts.
- Dockerfile/image + amd64/arm64 CGO_ENABLED=0 cross-builds (product track has
  this; unverified but low risk).
- Phase 5 routing conversion per package (ServeMux + smoke cases — templated,
  listed order exists: sharing → notifications → emailapi → … → catalog last).
Medium (well-understood, bounded):
- V1/V5 pre-Phase-6 packet: three concrete book builders, one media accessor,
  remove per-consumer JSON round-trips (the collection→playlist bug class, R1,
  came from exactly this).
- Phase 7 authz consolidation — characterization showed only 2 real divergences
  (email any-tag fix landed; podcast explicit fix landed), rest match.
- Long-tail route edges: PATCH/DELETE cover 301/302 chains, SSRF matrix, SMTP
  TLS/auth, notification queue, OIDC mobile token exchange.
Hard (acceptance blockers, need investigation or owner decisions):
- Backup/restore mismatch: Go applies the archive and restores metadata files,
  but librariesUnchanged/itemDetailUnchanged checks fail — never isolated;
  restore acceptance is an explicit open gate.
- Restore disconnects realtime clients where upstream keeps them connected
  (Socket.IO lifecycle on restore is a Go-vs-Express structural difference —
  genuinely hard).
- Realtime parity residue: playback-session details in the online-user payload
  are null; the domain dispatcher is still partially placeholder (see
  internal/realtime/README.md).
- Malformed-notification POST: upstream process.exit(1) vs Go 200 — needs an
  owner decision (matching a crash is questionable).
- Scanner import semantics: 9 characterized divergences with the
  transactional-import decision pending.
- Socket flake (socket-connect-legacy, intermittent Go 599) — unexplained.
- Playback-sessions persisted-row literal comparison still lacks a fixture row
  (B5's write-format was verified elsewhere, but this column class isn't closed
  by evidence).

3) Steps to reach the goal
In dependency order, with the goal-oriented shortcut at the end:
1. Land and green the tree — commit the in-flight packet, regenerate
   route-coverage inventory, rerun make parity (require 193/193 vs the committed
   core-schema2-no-webp.json baseline), make status, make ratchet. One commit;
   this restores "docs agree with each other."
2. make parity-full — it hasn't been rerun post-remediation. Expect to surface
   the backup-restore mismatch; investigate it then (it's the single most
   deployment-relevant open bug).
3. V1/V5 typed-representation packet — before any DTO expansion; the review is
   explicit: don't use the current pilot as the Phase 6 template.
4. Realtime packet — playback-session payload serialization + real dispatcher;
   this is what mobile clients listen to for progress sync. Plus a
   rollback-check-style regression for restore-keeps-sockets if feasible.
5. Tiered route coverage, not exhaustive coverage (ROADMAP product track, and
   the correct shortcut for your goal): define your client set, capture real
   traffic from the Node server in your installation, mark routes Tier 1/2/3,
   and make make parity-full green over Tier 1 + mobile-critical flows. Defer
   Tier 3 explicitly — this is the documented escape hatch from the 226-route
   mountain.
6. Acceptance trial: upgrade/rollback trial (pinned upstream must reopen a copy
   of a Go-written DB — rollback-check exists, make it stand), realistic-load
   run, amd64/arm64 CGO_ENABLED=0 build verification.
7. Only then: image + swap on a copy of the real installation (config, metadata,
   DB) — run the stock web client and your actual mobile apps with existing
   tokens for a week; watch the malformed-notification and restore paths in
   particular.
8. Phase 6 DTO expansion and Phase 7 authz continue in the background on Tier
   2/3 packets — they raise confidence but aren't on the critical path to your
   working swap.
One structural caution from everything read: the two 2026-10-31 reviews agree
the verification infrastructure is solid and worth trusting, while the DTO layer
is the weak part — so don't equate "green make parity" with "architecturally
done"; the remaining risk concentrates in the untyped map surfaces (silent-drop
bug class) and the restore/realtime lifecycle, not in JSON formatting.
