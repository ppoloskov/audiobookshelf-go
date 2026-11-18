# Test machine audit — 2026-11-18

Audit of the "test machine" (the unified replacement verification machine:
Go scenarios + coordinator + mapping tools + requirement register + review
receipts). Owner question: *what exists, is it complete against upstream TS
v2.37.2 and the clients, and what is missing?* Companion: [PLAN.md](PLAN.md).

State audited: commits `e447312b` + `a3df799a` (all previously uncommitted
machine work was committed first; base `8990c2e6`). Baseline commands run this
session: `go build ./... && go vet ./...` clean; `go test ./internal/playback/...
./internal/media/...` pass with the FFmpeg prefix; `go run ./tools/verify -gaps`
printed `975 declared scenarios in 18 packets; 2035 coverage gaps; 1 of 34
packet reviews accepted`.

## 1. What the machine is and where it lived (pre-consolidation)

Before consolidation the machine was spread over seven locations:

| Piece | Old location | Role |
|---|---|---|
| Scenarios + harness | `go-backend/tests/replacement/` | 19 packet packages, `packets.json` (34 packets), owner/reviewer briefs |
| Coordinator | `go-backend/tools/verify/` | plan validation, gap calc, upstream/Go runs, report |
| Mapping/coverage tools | `go-backend/tools/*.mjs` + oracle `*.cjs` | requirement map, gap queue, V8 coverage, upstream-serializer oracles |
| Old advisory harness | `go-backend/tools/parity/` | 31 `.cjs` runners incl. the rollback check (still required before pushes) |
| Requirement register | `docs/agent-work/capabilities/` (346 JSON) + `CONTRACT.md` | 345 requirements, invariants, facets, test bindings |
| Generated views + reports | `go-backend/docs/verification/` | requirement map, gap queue, run records, architect reviews |
| Contract inputs + receipts | `go-backend/contracts/` | client audits, golden files, shapes, per-packet review receipts |

Plus: root `Makefile` (`make verify`, `verify-packet`, legacy parity targets),
`.github/workflows/go-backend.yml` (CI runs `make verify`), `go-backend/VERIFICATION.md`
(gate spec), and wiring through `agents.md`, `AGENTS.md`, `ROADMAP.md`,
`handover.md`, `TESTING.md`.

## 2. Coverage audit: what the machine covers of upstream TS v2.37.2

**The inventory is complete; the evidence is not.** The Q00 inventory
(architect accepted) registered every in-contract obligation: 345 requirements,
all 226 route sites, 77 socket name/direction records, 25 persisted model
files, plus non-HTTP formats, configuration and lifecycle obligations. Every
requirement has an assigned owner packet. Two approved exclusions stand:
older-schema migration and ebook-engine internals.

What is actually *proven* against pinned upstream:

- **975 declared scenarios across 18 of 34 packets**; the latest integrated
  run passed 974/975 (`library_data/issues-remove-twice-and-without-issues`
  timed out once, passed in three isolated reruns — a fixture-determinism
  bug, not a coverage hole).
- **2,035 coverage gaps** remain (facet/state/invariant obligations without a
  declared scenario), including ~255 unbound invariants across ~197 requirements.
- **16 packets have zero scenarios**: backup, feeds, item_cover, launch, mail,
  metadata, notify, oidc, playback_streams, podcast_episodes, podcast_feed,
  protocol, realtime, scan, share, user_misc.
- **Gap hotspots inside existing packets** (gap lines from `-gaps`): author 150,
  tools 146 (audio-task progress + embed queue, Q01), browse 25, collections 11,
  playlists 9, admin_settings 9, library_manage 8, user_admin 7, store ~15
  (fresh-init/settings failure scenarios are recent additions; concurrent-patch
  and failed-write scenarios are the known remainder), item_read 2.
- **Reviews: 1 of 34 accepted** (`auth`). Receipts absent for 23 packets;
  `user_me`'s receipt is stale (digest mismatch); receipts exist but have open
  findings for the rest.
- **The independent whole-contract inventory receipt
  (`contracts/verification-inventory-review.json`) is missing.** The gate
  requires it; nothing currently cross-checks the register against the pinned
  source across packet boundaries.
- **Machine health blockers**: 8 tagged lint findings (auth, browse,
  library_data, user_admin, user_progress); the standalone ledger validator
  rejects 60 inherited invariant classifications in browse, author,
  user_admin, admin_settings; one intermittent library_data fixture.

**Client compatibility**: client evidence is an input layer, not yet a proof.
The machine knows what clients read (`contracts/clients/*.md` — official app,
absorb, audiobooth, lissen, storii, yaabsa — plus `client-usage.json` and the
mobile audits), and every scenario must cite a client consumer. Not covered:

- Q28 device acceptance: no iOS/Android device has run against Go. The
  mobile audits are source-level; the plan includes assembling the
  device-acceptance evidence pack, but the runs themselves need the owner.
- Socket polling/binary transports have no disposition (websocket-only is
  proven).
- Success paths fixed to third-party internet hosts (some metadata providers,
  cover URLs) are policy-marked not applicable; only offline-deterministic
  behaviour is proven.
- The Go comparison itself (scenarios run with `REPLACEMENT_TARGET=go` plus
  rollback) is deliberately a separate assessment after upstream
  certification; the only Go-side data is the Nov 15 partial run (96 failures
  across 12 packets, not a verdict).

## 3. Stale/duplicated work found (not part of the live machine)

1. Two harness generations: `tools/parity/` (advisory since 2026-11-04, but it
   still owns the pre-push rollback check) vs `tests/replacement` (authoritative).
2. Two requirement registers: the canonical 346-file register vs
   `docs/agent-work/archive/capabilities-pre-20261113-contract-map/` (239 stale files).
3. Orphaned coverage family: `docs/verification/coverage.{md,csv}` + `coverage_events.csv`
   whose Python generators were deleted.
4. Three overlapping mobile-gap documents (`contracts/mobile-compat-gaps.md`,
   `contracts/mobile-client-audit.md`, `docs/verification/mobile-gaps.md`) plus
   route decisions and evidence JSON.
5. Two status reports: old `go-backend/STATUS.md` (FAIL, 2026-11-12) vs the
   unified verification report.
6. Strict-ledger findings duplicated in `docs/agent-work/audits/` and
   `contracts/parity/`.
7. Deleted Python tools surviving as `tools/__pycache__` (removed this session)
   and as archive copies.
8. Run debris: `go-backend/.parity-work/` (9 disposable dirs), a 26 MB ignored
   `go-backend/audiobookshelf-go` binary in the tree.
9. `contracts/README.md` points at two nonexistent targets
   (`../TEST-COMPATIBILITY-IMPLEMENTATION.md`, `test/oracle/corpus`).

Nothing above was deleted in this audit beyond `tools/__pycache__`; stale
items move into the machine folder and get an explicit disposition in the
consolidation task rather than being silently dropped.

## 4. Verdict

The machine's *design* is sound and its inventory is complete, but today it
certifies only ~18% of the obligation surface end-to-end (975 scenarios, one
accepted review, no cross-contract audit receipt). "Covers 100% of upstream"
is not yet true and cannot be claimed until the gaps in section 2 close and
`make verify VERIFY_ARGS="-target upstream"` exits zero.
