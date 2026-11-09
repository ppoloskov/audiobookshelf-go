# Go backend handover

Updated 2026-11-09. Canonical checkout: `/Users/paul/projects/audiobookshelf-go`, **master only**, fork
`https://github.com/ppoloskov/audiobookshelf-go`. No development branches or implementation worktrees. The
architect plans, reviews and integrates; cheap workers run bounded packets on disjoint packages. Every push is
gated: the full local CI mirror and the TS rollback check must pass first.

Read [AGENTS.md](AGENTS.md), [ARCHITECTURE.md](ARCHITECTURE.md), [ROADMAP.md](ROADMAP.md), then the refactor
[decision log](docs/REFACTOR-LOG.md) and the [simplification ledger](docs/SIMPLIFICATION.md). The previous handover
is archived at [archive/reviews/2026-11-09-handover-before-wave-g.md](archive/reviews/2026-11-09-handover-before-wave-g.md).

## Current state

- **Master:** `d833484` (Step 3 salvage). CI-green locally (CI mirror + rollback).
- **Structural refactor (owner goal, comments + tests chosen): structurally complete.** Waves A–F plus three verifier
  rounds; round 3 found nothing worth fixing beyond small leftovers, all fixed. Clean DAG, no unreasonable imports,
  remaining special cases all have a TS or client-contract reason. Details and every bug fixed with TS evidence:
  [docs/REFACTOR-LOG.md](docs/REFACTOR-LOG.md).
- **Owner-approved features landed:** podcast notification events; playlist events on deletes.
- **Metrics at master** (`go run ./tools/ratchet`): 37,972 handwritten lines (from 44,674 at the original baseline,
  41,020 at the start of the refactor); cyclomatic complexity 8,499; functions above 15: 114; `map[string]any` 464;
  `[]any` 202; Set* injectors 4.

## In progress: Step 3, behavioural test-quality pass

Owner-approved spec: `TESTS-SPEC` (classify every candidate in the log's "Tests to change" as replace / rewrite /
delete / retain; preserve every contractual assertion; remove test-only hooks only via existing injection or
package-private fixtures; lifecycle invariants first: playback, HLS, share expiry, watcher shutdown, podcast
downloads, restore).

- **Salvaged and committed (`d833484`):** T3 finished; partial T1/T2/T4/T5 work kept where it built and passed
  under `-race`. Behaviour fixes with TS evidence are in the commit message (podcast download race, Audible
  rating, empty filter values, HLS close vs seek restart, share expiry on session routes). TS-diagnostic files
  restored (they back `contracts/test-assertions.json`).
- **Audit done** ([archive/reviews/2026-11-09-step3-salvage-audit.md](archive/reviews/2026-11-09-step3-salvage-audit.md)):
  60 tests really gone; most covered. **In progress: restore packet R** for the WEAKER/LOST contractual items:
  backup notification payload fields; store DecodeJSONSlice partial decode + StringPtrValue; catalog
  `include=numepisodesincomplete` + an N+1 guard; identity nullableDate/parseDate; playback devices row, episode
  progress on sync/close, unknown-episode error, local session metadata, local-all tolerance; realtime packet
  namespaced-ack cases and Emit-after-close; stale doc references. Node-to-Go backup import tests were always
  skipped (env-gated), so no loss; Node archive import stays covered only by the manual harness.
- Then relaunch the unfinished packets from the current tree: T1 playback/sharing/realtime, T2 scanner/backup/
  server, T4 catalog/items/lists, T5 identity/infra/contracttest. Remaining T3 item: podcasts
  `SetHTTPClientForTests` (replace in `server/runtime_test.go` with `SSRF_REQUEST_FILTER_WHITELIST=127.0.0.1`).

## Next, in order

1. **Finish Step 3**: restore audit findings, relaunch unfinished test packets (above).
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

None blocking. Parked decisions with defaults (O1, O2, O5, MC-C JS client in CI): ROADMAP.md section 5.
