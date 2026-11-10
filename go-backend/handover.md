# Go backend handover

## Package/object boundary request (2026-11-10)

The owner requested more cohesive packages and clearer object ownership.
[The source-backed proposal](docs/PACKAGE-BOUNDARIES.md) records findings,
owners, alternatives, migration hazards and required proof (ROADMAP PB01).
The first bounded ownership packet is ACCEPTED (2026-11-10): email
defaults and e-reader device policy moved from `settingsdoc` to
`internal/emailsettings`; `emailapi`, `identity` and `userapi`
consume that owner, while `settingsdoc` retains generic row mechanics.
The second packet is ACCEPTED (2026-11-10): `emailsettings.Settings` is a
typed json-tagged document, PATCH
decodes onto the loaded struct, device maps preserve unknown fields, the SMTP
password is redacted by `String`, `settingsdoc.LoadInto` supports typed
documents, and sendEbook no longer asserts the device email type. Revised
accepted growth after the port-parity fix is +115 handwritten LOC and +13
complexity, offset by −10 `map[string]any`, −6 `[]any` and −1 function over 15;
only those measured baseline fields changed. Stored ports now round-trip
verbatim and PATCH applies upstream `Number()` coercion rather than the old Go
clamp; the clamp removal is a parity restoration. Residual risks: a malformed
stored `ereaderDevices` non-object element now returns 500 instead of being
ignored (unreachable through validated TS/Go writes), and the pre-existing
`name`/`email != nil` versus JS truthiness mismatch in device visibility stays
a separate follow-up parity nit. Next after explicit handover: the catalog
books/podcasts/common-item read dependency graph, migrating existing libitem
models without a second implementation. Earlier read-only review confirmed
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

- **Base:** local `master` contains the accepted email-settings packets through `7754ed0a` and the accepted PB02a slice at `7c2a1c0e`, plus the accepted PB02b slice in this commit.
- **Local acceptance:** the current worktree passes `go build ./...`, `go vet ./...`, `go test ./... -count=1` with the documented FFmpeg prefix, targeted `-race` tests, golangci-lint v2.15.0, dependency-boundary fixtures and `make ratchet`.
- **Metrics:** 38,258 handwritten LOC / 8,544 complexity / 114 functions over 15 / 451 `map[string]any` / 196 `[]any`. PB02b has an accepted +24 LOC budget with every other metric unchanged.
- **Catalog read graph (PB02):** PB02a moved item permission inputs (`Access`, `LoadAccess`, `ItemAccess`, `CanReadItem`) into `internal/access`. PB02b is ACCEPTED (2026-11-10): common item identity, the batched loader and common response envelopes (`Item`, `Options`, `Load`/`LoadOne`, `Head`, `Full`, `Expanded`, `Minified`, `Extras`, `JSONNull`, `ListRow`) moved into `internal/items`; `libitem` now owns book/podcast media models and exported media projections, composed by `items` without a second loader or model. Both directions are acyclic: `items` → `libitem`/`access`; `libitem` → `access`.
- **QM01:** empty converted match lists preserve existing links; duplicate series updates run sequentially; missing sequence preserves the current position; absent, null, empty and numeric text stay distinct. The actual QuickMatch adapter stores raw zero as `"0"`; PATCH and scanner adapters retain their respective null/absence rules. Existing Go characterization preserves event ordering.
- **Git state:** accepted work is committed through `7c2a1c0e` and pushed. The accepted PB02b slice, its baseline update and this documentation update are committed together and pushed to the fork's master. No deployment is claimed.

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
