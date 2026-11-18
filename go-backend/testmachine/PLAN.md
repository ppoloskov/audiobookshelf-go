# Test machine delivery plan — 2026-11-18

Status through commit `7fcd129f`: waves C, H, G and the oracle-hardening
series (route parity, overfit audit, citation lint — all machine-health gates)
are DONE; all 18 built packets have zero scenario coverage gaps. Workers:
DeepSeek V4.1 Flash `opencode-go/deepseek-v4.1-flash` and GLM 5.3 Flash
`opencode-go/glm-5.3-flash`, one bounded owner packet per invocation, at most
3–4 in parallel, no overlapping write sets; the architect reviews, verifies
and commits every landing. Audit basis: [AUDIT.md](AUDIT.md). Paths below are
the post-consolidation layout under `go-backend/testmachine/`.

Owner confirmation required before waves G–R are dispatched in full
(≈ 30 worker invocations; flash-model effort dominated, highest-value first).

## Wave C — consolidation (running)

- **C1 — Physical consolidation** (GLM 5.3 Flash; in flight). Move the whole
  machine into `go-backend/testmachine/` (`scenarios/`, `cmd/verify/`,
  `cmd/clientusage/`, `scripts/`, `parity/`, `register/`, `reports/`,
  `contracts/`, `VERIFICATION.md`), update every reference, keep the machine's
  self-checks green. Acceptance: plan-only declares 975 scenarios; `-gaps`
  prints the same totals (975 / 2035 / 1 of 34); contract-map `--check` passes;
  `make verify-packet PACKET=auth RUN=<one assertion>` passes upstream.

## Wave H — machine hygiene (after C1; parallel)

- **H1 library_data determinism** (DeepSeek): make
  `issues-remove-twice-and-without-issues` deterministic (fixture + timing),
  keep scan coverage for its owner, clear the packet's 3 tagged lint findings.
- **H2 lint backlog** (GLM): the remaining tagged findings in `auth`, `browse`,
  `user_admin`, `user_progress`; behaviour-neutral.
- **H3 register repair** (DeepSeek): regenerate the stale `user_me` receipt
  bindings; fix the 60 invariant classifications the ledger validator rejects
  (`browse`, `author`, `user_admin`, `admin_settings`). Records only, no
  scenarios.

## Wave G — close gaps inside existing packets (ordered by risk)

Priority per ROADMAP: database state, lifecycle, real-client auth and
Socket.IO, scan, playback, then the rest. Owner ≠ reviewer model per packet.

- **G1 store** (GLM): remaining ~15 gaps — concurrent disjoint settings
  patches, failed/durable-write recovery (trigger probe), broader column
  semantics.
- **G2 tools slice A** (DeepSeek, Q01): metadata embed queue
  (immediate/waiting/repeat), consumer-visible trace per
  `reports/q01-embed-queue-review.md`.
- **G3 tools slice B** (GLM, Q01): audio task lifecycle and progress events;
  persisted media effects.
- **G4 author slice A** (DeepSeek): 150 gap lines — split by requirement
  family (r131–r134 + linked-item updates).
- **G5 author slice B** (GLM): r135–r137 + author sockets/events.
- **G6 browse** (GLM): 25 gaps.
- **G7 collections** (DeepSeek): 11.
- **G8 playlists** (GLM): 9.
- **G9 admin_settings** (DeepSeek): 9.
- **G10 library_manage** (GLM): 8.
- **G11 user_admin** (DeepSeek): 7 + not-applicable force reasons.
- **G12 item_read + store tail** (GLM): 2 + leftovers.
- **G13 items_updated cross-packet reconciliation** (DeepSeek): bind the
  shared `items_updated` evidence from `author`/`library_data` to remaining
  scan producers.

## Wave N — build the 16 zero-scenario packets (queue order)

- **N1 realtime A** (GLM): transport/auth/dispatch — real Socket.IO v4 client,
  `socket-incoming-*` + `init/auth-failed/pong/user-online/offline` (17 reqs →
  split A/B).
- **N2 realtime B** (DeepSeek): admin message, log listeners, cover search
  cancel, disconnect, filtered audiences.
- **N3 oidc** (DeepSeek): web state/cookie + mobile PKCE, replay, expiry,
  linking; triage the 400-vs-302 candidate.
- **N4 scan** (GLM): metadata precedence, chapter offsets, repeat/force/move/
  collision/removal/cancel, watcher/cron.
- **N5 playback_streams** (DeepSeek): stream open/progress/ready/reset/closed/
  error + public track ranges; link accepted lifecycle evidence.
- **N6 metadata A** (GLM): built-in providers query/result/type/fallback.
- **N7 metadata B** (DeepSeek): custom providers CRUD, SSRF, websocket cover
  search results/cancel/provider errors.
- **N8 share** (GLM): feeds, RSS open/close, share expiry/session/progress.
- **N9 podcast_feed** (DeepSeek): local RSS feed parse/create/search/download
  queue with a stubbed feed host.
- **N10 podcast_episodes A** (GLM): episode CRUD, OPML/match.
- **N11 podcast_episodes B** (DeepSeek): download queue lifecycle, scheduling,
  auto-disable, events.
- **N12 feeds** (GLM): feed item/collection/series XML, enclosures, timezone.
- **N13 mail** (DeepSeek): local SMTP/TLS/auth, MIME attachments, ereader
  delivery, redaction.
- **N14 notify** (GLM): subscription CRUD, Apprise payload (stubbed),
  notifications_updated full-settings payload.
- **N15 backup** (DeepSeek): archive layout TS↔Go, retention, apply, rejection
  before install.
- **N16 protocol** (GLM): ranges/206/416, ETag/304, MIME, conditional caching.
- **N17 launch** (DeepSeek): flags/env precedence, refused/fresh/existing
  boot (CGO=0, no Node).
- **N18 item_cover** (GLM): cover upload/fetch/cache/remove across items.
- **N19 user_misc** (DeepSeek): me/settings/ereader edges, API keys tail.

## Wave R — reviews and certification

- **R1 packet review receipts** (alternating the two models, never the owner's
  model): 33 packets need accepted receipts; run per REVIEWERS.md as each
  packet closes, not as one batch.
- **R2 whole-contract inventory audit** (both models as independent auditors,
  architect adjudicates): the missing
  `contracts/verification-inventory-review.json`; re-scan pinned source,
  client protocols, formats and lifecycle across packet boundaries.
- **R3 certification**: `make verify VERIFY_ARGS="-target upstream"` exits
  zero from a documented setup; input digests stable. Publish the setup and
  rerun command.
- **R4 (separate assessment, after R3)**: Go comparison + rollback trial
  (`REPLACEMENT_TARGET=go`), the Nov 15 partial run's 96 Go failures as the
  starting backlog. Not part of machine delivery.
- **R5 client device acceptance** (owner-assisted): assemble the Q28 evidence
  pack (builds, scripts, instructions) so the owner can run unchanged iOS and
  Android clients; record results. Blocking for the final claim, not for
  upstream certification.

## Explicitly out of scope

Older-schema migration and ebook-engine internals (approved exclusions);
live deployment (separately authorized); no new dependencies.
