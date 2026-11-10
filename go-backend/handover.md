# Go backend handover

## Package/object boundary request (2026-11-10)

The owner requested more cohesive packages and clearer object ownership.
[The source-backed proposal](docs/PACKAGE-BOUNDARIES.md) records findings,
owners, alternatives, migration hazards and required proof (ROADMAP PB01).
The first bounded ownership packet is ACCEPTED (2026-11-10): email
defaults and e-reader device policy moved from `settingsdoc` to
`internal/emailsettings`; `emailapi`, `identity` and `userapi`
consume that owner, while `settingsdoc` retains only generic `Load`/`Save` row
mechanics. Architect rerun verified build, vet, the full suite with the
documented FFmpeg prefix, lint, dependency fixtures and `make ratchet` at +0
on every metric; the identity nil→defaults loading change is unobservable.
Catalog decomposition and unrelated settings behavior were not changed.
Next packets, in order: (1) convert the email-settings document to a struct
with json tags per the new agents.md rule 1 — device entries stay
`map[string]any` for unknown-key round-trip parity — which also removes the
unchecked `device["email"].(string)` assertion in emailapi sendEbook; (2)
after explicit handover, the catalog books/podcasts/common-item read
dependency graph, migrating existing libitem models without a second
implementation. Earlier read-only review confirmed
progressdto, playlistview, settingsdoc and store ownership problems; libitem
and itemwrite already consolidate useful shared behavior and must not be
duplicated during extraction. The blanket feature import ban was not weakened:
`emailsettings` is classified as a foundation package.

Updated 2026-11-10. Canonical checkout: `/Users/paul/projects/audiobookshelf-go`, **master only**, fork
`https://github.com/ppoloskov/audiobookshelf-go`. No development branches or implementation worktrees. The
architect plans, reviews and integrates; cheap workers run bounded packets on disjoint packages. Every push is
gated: the full local CI mirror and the TS rollback check must pass first.

Read [AGENTS.md](AGENTS.md), [ARCHITECTURE.md](ARCHITECTURE.md), [ROADMAP.md](ROADMAP.md), then the refactor
[decision log](docs/REFACTOR-LOG.md) and the [simplification ledger](docs/SIMPLIFICATION.md). The previous handover
is archived at [archive/reviews/2026-11-09-handover-before-wave-g.md](archive/reviews/2026-11-09-handover-before-wave-g.md).

## Current state

- **Base:** local `master` now includes upstream `fc5aa760963c639f61ccfe9ceeb6a16c51b7b714`; the reviewed source changes are still uncommitted. The current working tree contains Wave G, R1, RT1 and QM01, plus the user's other in-scope WIP.
- **Local acceptance:** the canonical worktree passes `go test ./... -count=1` with real Socket.IO required, `go build ./...`, `go vet ./...`, golangci-lint v2.15.0, `make ratchet`, `make sqlc SQLC=/Users/paul/go/bin/sqlc`, `go run ./tools/dbnest`, required Node/Python source and contract checks, Linux amd64/arm64 builds, and the TS rollback journey. See the [final validation report](docs/refactor-final-validation-20261110.md) for exact environment, commands and results.
- **Metrics:** 38,119 handwritten LOC / 8,531 complexity / 115 functions over 15 / 461 `map[string]any` / 202 `[]any`. The canonical worktree baseline matches these measured values; no other baseline fields changed.
- **QM01:** empty converted match lists preserve existing links; duplicate series updates run sequentially; missing sequence preserves the current position; absent, null, empty and numeric text stay distinct. The actual QuickMatch adapter stores raw zero as `"0"`; PATCH and scanner adapters retain their respective null/absence rules. Existing Go characterization preserves event ordering.
- **Git state:** ROADMAP, handover, `internal/catalog/quickmatch.go` and `ratchet-baseline.json` still have index stages 1/2/3. The current worktree versions are the reviewed resolution candidates; stage only the explicit approved paths and preserve all other user WIP. Commit/push and remote CI are not yet complete. No deployment is claimed.

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
- Earlier Wave G leads and subsequent corrections are tracked in [the G1–G6 audit](docs/WAVE-G-AUDIT.md);
  do not reuse the superseded claim that the TS numeric-string behavior is confirmed.
- **T1/T2/T4/T5 relaunch done (committed with this update):** 3 tests removed, each replaced by a stronger
  named test; hooks removed (realtime AddTestClient/SetTestEmitter, podcasts SetHTTPClientForTests, access
  decisions golden); lifecycle tests added (catalog delete cascades, lists lifecycle, identity revocation,
  audiotools batch). Step 3 is complete.
- Original Wave G feed and stream-cleanup leads reproduced and fixed in the bounded packet; current evidence is
  recorded in the transition audit and validation report, including initial RED and final green outcomes.

## R1 reviewed locally (2026-11-10)

- `OpenSessions` now passes its typed session DTO slice through as an opaque
  JSON value; the userapi endpoint preserves array shape and nullable fields.
  The implementation worker reports the focused endpoint, full playback and
  userapi package, and targeted race checks passing.
- R1-only `make ratchet-update && make ratchet` passed in
  `/private/tmp/abs-ref-r1-metrics-20261110`, based on HEAD
  `b42969b9431c635ab9032f057c033bc429323d2f` plus only the three R1 source and
  test files. Metrics changed by −2 production LOC and −3 maps; all other
  metrics were unchanged. The shared Wave G baseline was not changed by this run.
- Earlier recovered Wave G logs remain historical: their catalog fixture
  failures predated the approved fixture-schema updates, and realtime's
  full-buffer failure predated RT1. The final combined snapshot supersedes
  those observations; its full suite and rollback passed. The full evidence
  and exact environment corrections are in the [final validation report](docs/refactor-final-validation-20261110.md).

## Next, in order

1. Re-read and stage the current worktree versions of the four unmerged paths
   after reviewing their staged diff; retain all original stage blobs in the
   recovery snapshot.
2. Fetch `origin/master`, inspect the remote `agents.md` addition, then
   fast-forward only if it is the reviewed documentation-only change. Stage and
   review the exact full authorized worktree manifest, commit on `master`, and
   push without force.
3. Verify required GitHub CI on the pushed commit. Keep N3 open until it passes;
   deployment remains separately authorized.

## Working rules learned this session

- Snapshot the working tree to a patch after any interruption; workers have been stopped by API limits six times.
- The 5-hour API limit pauses all workers at once; resume them with a message, not a new agent.
- An agent stopped by the owner cannot be resumed; ask before relaunching.
- Run the required local workflow commands and TS rollback before commit/push; then verify remote GitHub CI on the pushed commit. The checked-in command mirror and evidence are in [the final validation report](docs/refactor-final-validation-20261110.md).
- depguard layers: leaf (stdlib only) / foundation / feature; features never import features. dbnest flags any
  identifier named `db`; name transaction parameters `tx`.
- sqlc: `make sqlc SQLC="go run github.com/sqlc-dev/sqlc/cmd/sqlc@v1.31.1"` (regenerates byte-identically).

## Open owner decisions

Single-executable media feasibility (2026-11-09, analysis only): see
[the review](docs/FFMPEG-LIBRARY-FEASIBILITY.md). Static libav/CGO is technically
feasible but changes the build contract; embedded WASM needs codec/performance
proof (published AMR gap). No dependency or runtime migration was made.
Full probe API, HLS and manual audio tools must remain; client/dist also remains
an external runtime asset. Current catalog covers honor FFMPEG_PATH; TESTING.md
was corrected. No candidate build, benchmark or runtime tests were run.

Parked decisions with defaults (O1, O2, O5, MC-C JS client in CI) remain in ROADMAP section 5. No schema migrations, startup DB cleanup or broad filesystem cleanup is authorized. Preserve the TS-proven UUID playback stream cleanup at process startup only; do not run it during restore/runtime replacement. Preserve backup/restore and manual audio-tool flows. Full prior decisions and recovery instructions remain in the dated archive linked above.
