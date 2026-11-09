# Go backend handover

Updated 2026-11-09. Canonical checkout: `/Users/paul/projects/audiobookshelf-go`, **master only**, fork
`https://github.com/ppoloskov/audiobookshelf-go`. No development branches or implementation worktrees. The
architect plans, reviews and integrates; cheap workers run bounded packets on disjoint packages. Every push is
gated: the full local CI mirror and the TS rollback check must pass first.

Read [AGENTS.md](AGENTS.md), [ARCHITECTURE.md](ARCHITECTURE.md), [ROADMAP.md](ROADMAP.md), then the refactor
[decision log](docs/REFACTOR-LOG.md) and the [simplification ledger](docs/SIMPLIFICATION.md). The previous handover
is archived at [archive/reviews/2026-11-09-handover-before-wave-g.md](archive/reviews/2026-11-09-handover-before-wave-g.md).

## Current state

- **Master:** `df2c4a0` (Step 3 complete). CI-green locally (CI mirror + rollback).
- **Structural refactor (owner goal, comments + tests chosen): structurally complete.** Waves A–F plus three verifier
  rounds; round 3 found nothing worth fixing beyond small leftovers, all fixed. Clean DAG, no unreasonable imports,
  remaining special cases all have a TS or client-contract reason. Details and every bug fixed with TS evidence:
  [docs/REFACTOR-LOG.md](docs/REFACTOR-LOG.md).
- **Owner-approved features landed:** podcast notification events; playlist events on deletes.
- **Metrics at master** (`go run ./tools/ratchet`): 37,972 handwritten lines (from 44,674 at the original baseline,
  41,020 at the start of the refactor); cyclomatic complexity 8,499; functions above 15: 114; `map[string]any` 464;
  `[]any` 202; Set* injectors 4.

## Step 3, behavioural test-quality pass (done)

Owner-approved spec: `TESTS-SPEC` (classify every candidate in the log's "Tests to change" as replace / rewrite /
delete / retain; preserve every contractual assertion; remove test-only hooks only via existing injection or
package-private fixtures; lifecycle invariants first: playback, HLS, share expiry, watcher shutdown, podcast
downloads, restore).

- **Salvaged and committed (`d833484`):** T3 finished; partial T1/T2/T4/T5 work kept where it built and passed
  under `-race`. Behaviour fixes with TS evidence are in the commit message (podcast download race, Audible
  rating, empty filter values, HLS close vs seek restart, share expiry on session routes). TS-diagnostic files
  restored (they back `contracts/test-assertions.json`).
- **Audit done and restored** ([archive/reviews/2026-11-09-step3-salvage-audit.md](archive/reviews/2026-11-09-step3-salvage-audit.md)):
  60 tests really gone; the contractual/lifecycle losses were restored (backup notification fields, store partial
  decode, catalog numEpisodesIncomplete + N+1 guard via a counting driver in the test, identity dates, playback
  journeys, realtime parser/Emit-after-close, stale doc references). Node-to-Go backup import tests were always
  skipped (env-gated); Node archive import stays covered only by the manual harness.
- **Wave G seeds found by the restore (TS-confirmed, not yet fixed):** (a) `POST /api/items/<book>/play/<ep>`
  answers 404, TS 400 for a non-podcast item (LibraryItemController.js:461-464); (b) `/api/session/local-all`
  stores 0 for a string `currentTime` ("7"), TS stores 7 (SQLite numeric affinity); (c) re-uploading an
  existing local session overwrites stored mediaMetadata (`playback.sql:27` ON CONFLICT), TS updates only
  currentTime/timeListening/updatedAt (PlaybackSessionManager.js:207-210).
- **T1/T2/T4/T5 relaunch done (committed with this update):** 3 tests removed, each replaced by a stronger
  named test; hooks removed (realtime AddTestClient/SetTestEmitter, podcasts SetHTTPClientForTests, access
  decisions golden); lifecycle tests added (catalog delete cascades, lists lifecycle, identity revocation,
  audiotools batch). Step 3 is complete.
- **More Wave G seeds (TS-confirmed, not fixed):** (d) collection delete leaves its RSS feed and emits no
  `rss_feed_closed` (CollectionController.js:218, RssFeedManager.js:355-361); (e) item delete removes feed rows
  without `rss_feed_closed` (ApiRouter.js:387; Go item_delete.go:140); (f) TS removes orphan stream dirs
  (PlaybackSessionManager.js:465-483), no Go equivalent found in internal/playback.

## Next, in order

1. **IN PROGRESS: Wave G — behavioural transition and side-effect parity audit** (owner brief 2026-11-09): six read-only
   investigations (scanner/filesystem, catalog lifecycle, podcasts/lists/progress, playback/background, security/
   identity/sharing, persistence/runtime) → transition matrix → reproduce the 10–15 highest-risk suspects → minimal
   fixes with a failing test first. Bug class: missing downstream lifecycle transitions (example: scanner removal left
   podcast episodes behind, fixed in wave F).
2. **Step 4 — final check and report:** CI mirror, rollback, GitHub CI, race runs; final report (behavioural
   evidence, test decisions, remaining exceptions, compatibility, CI, rollback, measured complexity).
Wave G phase 1 outputs: `scratchpad/refactor/wave-g/G1..G6.md` (scanner, catalog, podcasts/lists/progress,
   playback/background, security/identity/sharing, persistence/runtime); seeds (a)-(f) above go to the matching
   investigator. If interrupted, finished outputs survive; relaunch only missing ones.
Stop when the exit criteria hold; do not chase 35,000 lines or zero special cases.

## Working rules learned this session

- Snapshot the working tree to a patch after any interruption; workers have been stopped by API limits six times.
- The 5-hour API limit pauses all workers at once; resume them with a message, not a new agent.
- An agent stopped by the owner cannot be resumed; ask before relaunching.
- Commit only with `ci-local.sh && rollback && git commit && git push` chained (one ungated push once broke master).
- depguard layers: leaf (stdlib only) / foundation / feature; features never import features. dbnest flags any
  identifier named `db`; name transaction parameters `tx`.
- sqlc: `make sqlc SQLC="go run github.com/sqlc-dev/sqlc/cmd/sqlc@v1.31.1"` (regenerates byte-identically).

## Open owner decisions

None blocking. Parked decisions with defaults (O1, O2, O5, MC-C JS client in CI): ROADMAP.md section 5.
