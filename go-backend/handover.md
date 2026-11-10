# Go backend handover

## User API simplification — UA01 (2026-11-10)

Terra implemented clearer concern files inside `internal/userapi`, retaining
the public facade and all callers. The mixed `me_extra.go` is removed. Actual
collapses remove rounded intermediate maps, redundant primary-key bookkeeping,
nested stats-map assertions and repeated PATCH validation; series visibility
uses standard slice operations. Characterization tests preserve ordered
validation, legacy stats, duplicate progress rows and repeated series actions.
The architect accepted the ownership tradeoff: +34 production LOC, −17
complexity, −2 high-complexity functions, −6 dynamic maps and −1 `[]any`.
Full build/vet/tests (real FFmpeg and Socket.IO), lint and ratchet/dbnest pass
in an isolated HEAD-plus-UA01 source snapshot; focused race tests pass too.
See [the implementation/review record](docs/USERAPI-SIMPLIFICATION.md) for exact
snapshot boundaries, gotchas and checks. Shared master still has unrelated
in-flight edits whose metric reconciliation is separate. No commit/push or
deployment was performed for UA01; combined publication gates remain pending.

## Podcasts simplification PC01 (2026-11-10)

Implemented with the requested Terra worker and reviewed by the architect.
[The review/addendum](docs/PODCASTS-SIMPLIFICATION-REVIEW.md) records contracts,
actual file ownership, characterizations and checks. Typed quick matching,
direct update construction, shared completion/queue snapshots, single probing
and deferred creation cleanup replace redundant code. Creation/OPML,
scheduling, episode mutation and registration have focused files; podcasts.go
falls from 1,032 to 356 lines. All public interfaces and podcasts/read source
are unchanged. Net packet: -10 production LOC / -27 complexity / -1 function
over 15 / -9 dynamic maps, with no other metric growth.

Important quirks: PATCH matches exact key case and ignores some wrong types;
OPML's current genre conversion remains; search and quick-match differ;
existing directories and committed item directories survive creation failures.
New tests protect these semantics and file/DB rollback. Focused races pass
34 top-level tests (65 including subtests), zero skips. Isolated full build,
vet, suite, lint, ratchet/dbnest, SQLC freshness, dependency/source/contract
checks, both Linux builds and TS rollback pass. One unrelated HLS test skips
on its hardcoded /usr/bin/ffmpeg macOS check; optional Monstrobook inventory
remains skipped. Validation snapshot is HEAD bf72cb3c plus only PC01, excluding
concurrent PB04/userapi changes; do not attribute combined workspace metrics
to podcasts. Local acceptance is complete; isolate PC01 for publication without
staging unrelated work. No live deployment is authorized by this packet.

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

- **Base:** local `master` contains the accepted email-settings packets through `7754ed0a`, PB02a–d, PB03a at `bf72cb3c` and the intermediate `371e9abb` simplification, plus the accepted PB04a slice in this commit. The catalog read graph and people entity surface are complete.
- **Local acceptance:** the current worktree passes `go build ./...`, `go vet ./...`, `go test ./... -count=1` with the documented FFmpeg prefix, targeted `-race` tests, golangci-lint v2.15.0, dependency-boundary fixtures and `make ratchet`.
- **Metrics:** 38,563 handwritten LOC / 8,539 complexity / 111 functions over 15 / 436 `map[string]any` / 195 `[]any`. The combined baseline edit in `c7b7d1d9` carries PB04a (+96 LOC / +15 complexity, others unchanged) plus UA01 (+34 LOC / −17 complexity / −2 over-15 / −6 maps / −1 `[]any`); the PB04 worker's isolated numbers were correct, and the architect's earlier contrary note is retracted.
- **Catalog read graph (PB02) — DONE:** PB02a moved item permission inputs into `internal/access`. PB02b moved common item identity, the batched loader and common envelopes into `internal/items`. PB02c moved book identity, projections and `LoadMany` into `internal/books`. PB02d is ACCEPTED at `a8e1b111`: podcast models/projections/episode loading moved into `internal/podcasts/read`, shared file models into `internal/mediafiles`, and `libitem` is deleted. `items` composes `books` + `podcastread` + `mediafiles`; feature `podcasts` consumes the read subpackage and keeps writes. The graph is acyclic and shared chunked queries live in `store.QueryIn`. Architect rulings: `mediafiles` approved as the shared representation leaf (not `store`); `podcasts/read` approved as a foundation subpackage with fixtures both ways; the items row-decode and explicit-podcast `Access()` assertions were restored at acceptance. Each media owner now has direct tests at its own boundary.
- **People (PB03a) — ACCEPTED (2026-11-10):** author/series records, views, detail, update/rename, merge/delete and metadata matching moved from catalog into `internal/people` (1,149 production lines; the standalone package clears the fold-into-books measurement). Catalog keeps library-scoped author/series list routes and author image routes for the next slice; itemwrite remains the relation-cleanup/orphan owner. The dependency is `catalog` → `people`, and `people` does not import catalog. Author images use catalog callbacks supplied at composition; people writes metadata through the scanner callback. Architect rulings: +141 LOC / +26 complexity ratified (composition surface, shared `DecodeObjectFields`, removed `AuthorFinder` dependency); the image-callback seam has a named exit task in PB04; the nil-skip `WriteMetadata` tolerance must become a recording callback in the test rig; pre-dispatch budget recording is required for later growth packets.
- **PB04a — ACCEPTED (2026-11-11):** the people remainder (library-scoped author/series lists, author image routes/helpers, author/series test rig with a recording `WriteMetadata` callback) and the first `internal/library` slice (`InLibrary` request scope, `Scope`, `MediaTable`) landed. Both PB03a contract conditions are closed; the image-callback exit is complete (`PurgeImageCache`/`SaveAuthorImage` deleted from `Deps`). Shared file/stream serving moved to `httputil`, image resize to `mediatools`; the temporary `SeriesMatchesFilter` callback exits in PB04b. Owner ruling on series: series is not people — it is library-scoped (no image/matching/merge upstream, `libraryId` FK), its list routes are LibraryController operations upstream, and `books` cannot own the routes (`items → books` would cycle). Series routes move people → library in PB04b; `people` ends as authors-only.
- **UA01 — accepted by integration (2026-11-11):** the user API simplification (me_extra split into topical files, facade retained, six documented behavior-preserving simplifications) landed in the shared checkout mid-review and was published with PB04a in `c7b7d1d9`. Ratified retroactively: its isolated validation record is in [USERAPI-SIMPLIFICATION.md](docs/USERAPI-SIMPLIFICATION.md); architect reran `-race` on userapi/users and the full gates on the combined tree. Isolated delta: +34 LOC / −17 complexity / −2 over-15 / −6 maps / −1 `[]any`. Process rule recorded in ROADMAP: no `git add -A` on the shared checkout; workers publish or quarantine their changes before another packet's review.
- **QM01:** empty converted match lists preserve existing links; duplicate series updates run sequentially; missing sequence preserves the current position; absent, null, empty and numeric text stay distinct. The actual QuickMatch adapter stores raw zero as `"0"`; PATCH and scanner adapters retain their respective null/absence rules. Existing Go characterization preserves event ordering.
- **Git state:** accepted work is committed through `bf72cb3c` and `371e9abb` and pushed. The accepted PB04a slice, its baseline update and this documentation update are committed together and pushed to the fork's master. No deployment is claimed.

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
