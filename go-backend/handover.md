# Go backend handover

Updated 2026-11-09. Canonical checkout: `/Users/paul/projects/audiobookshelf-go`, **master only**, fork
`https://github.com/ppoloskov/audiobookshelf-go`. No development branches or implementation worktrees. The
architect plans, reviews and integrates; cheap workers run bounded packets on disjoint packages. Every push is
gated: the full local CI mirror and the TS rollback check must pass first.

Read [AGENTS.md](AGENTS.md), [ARCHITECTURE.md](ARCHITECTURE.md), [ROADMAP.md](ROADMAP.md), then the refactor
[decision log](docs/REFACTOR-LOG.md) and the [simplification ledger](docs/SIMPLIFICATION.md). The previous handover
is archived at [archive/reviews/2026-11-09-handover-before-wave-g.md](archive/reviews/2026-11-09-handover-before-wave-g.md).

## Current state

- **Master:** `51011d9` (refactor wave F). CI-green locally (CI mirror + rollback).
- **Structural refactor (owner goal, comments + tests chosen): structurally complete.** Waves A–F plus three verifier
  rounds; round 3 found nothing worth fixing beyond small leftovers, all fixed. Clean DAG, no unreasonable imports,
  remaining special cases all have a TS or client-contract reason. Details and every bug fixed with TS evidence:
  [docs/REFACTOR-LOG.md](docs/REFACTOR-LOG.md).
- **Owner-approved features landed:** podcast notification events; playlist events on deletes.
- **Metrics at master** (`go run ./tools/ratchet`): 37,972 handwritten lines (from 44,674 at the original baseline,
  41,020 at the start of the refactor); cyclomatic complexity 8,499; functions above 15: 114; `map[string]any` 464;
  `[]any` 202; Set* injectors 4.

## In progress: Step 3, behavioural test-quality pass (uncommitted)

Owner-approved spec: `TESTS-SPEC` (classify every candidate in the log's "Tests to change" as replace / rewrite /
delete / retain; preserve every contractual assertion; remove test-only production hooks only via existing injection
or package-private fixtures; lifecycle invariants first: playback, HLS, share expiry, watcher shutdown, podcast
downloads, restore).

- **T3 (podcasts, providers, notifications, emailapi): finished.** Bugs found: data race in podcast `startDownload`
  (fixed under the queue lock); Audible `rating` fell back to region (TS Audible.js:85 → null). Four self-recorded
  goldens replaced by hand-derived expectations; providers `SetEndpoint` moved to export_test.go; podcasts
  `SetHTTPClientForTests` retained only for `server/runtime_test.go:63` (replace with
  `t.Setenv("SSRF_REQUEST_FILTER_WHITELIST","127.0.0.1")` before `NewManager`, then delete the method).
- **T1 (playback, sharing, realtime), T2 (scanner, audiotools, backup, server), T4 (catalog, items, lists),
  T5 (identity, access, users, store, infra, contracttest): stopped by the owner mid-work on 2026-11-09 ~02:09 UTC.**
  Their partial edits are in the working tree and saved as a patch. Next action: **salvage** — keep what builds and
  passes per package, revert the rest to master, gated commit, then relaunch the unfinished packages.

## Next, in order

1. **Salvage Step 3** (above), gated commit; relaunch unfinished test packets.
2. **Wave G — behavioural transition and side-effect parity audit** (owner brief 2026-11-09): six read-only
   investigations (scanner/filesystem, catalog lifecycle, podcasts/lists/progress, playback/background, security/
   identity/sharing, persistence/runtime) → transition matrix → reproduce the 10–15 highest-risk suspects → minimal
   fixes with a failing test first. Bug class: missing downstream lifecycle transitions (example: scanner removal left
   podcast episodes behind, fixed in wave F).
3. **Step 4 — final check and report:** CI mirror, rollback, GitHub CI, race runs; final report (behavioural
   evidence, test decisions, remaining exceptions, compatibility, CI, rollback, measured complexity).
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

None pending.
