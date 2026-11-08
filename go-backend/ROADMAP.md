# Roadmap

Updated 2026-11-06. **Owner authorized implementation with cheap workers,
review after completed batches, and pushing ready features to the fork.**
All development is on `master`; do not create development branches. Existing
branch commits are integrated and reviewed ready work is pushed to master.
Manual deployment, new libraries and route removals retain separate approval.
Record actual completed cards and tests; do not mark plans done. [ARCHITECTURE.md](ARCHITECTURE.md) owns permanent
contracts; [AGENTS.md](AGENTS.md) owns worker/check policy.

Current acceptance: L04/L05 and D01 DONE; D02 is withdrawn. Preserve all
implemented functionality; future reductions require proven equivalent code. Full
native-codec suite and all gates PASS. Completed cards preserve delivery-time
evidence; earlier five-WebP failures were resolved by FF01 and are not current
failures. Historical intermediate growth is not the final accepted delta.

## Direction and current evidence

Preserve SQLite/persisted meaning and real client protocols. Simplify the Go
implementation between them. Prioritize client risk and measured cost, then
remove duplicate responsibilities. sqlc is query safety, not the principal
line-saving strategy. The former 30% target is aspirational, with no proven
13k-line deletion inventory. Package moves are not savings by themselves.

At takeover HEAD `48c10a6a`, recorded baseline: 44,586 handwritten Go lines,
10,851 complexity, 178 functions over 15, 993 map uses, 9 legacy routers,
18 setters. These are baseline-file values, not a new measurement. Supplied
review reports L5 −58 and L6 −4 lines; not independently remeasured here.

Completed: F1–F9 (including F5b/F8 extensions), L1–L7, S2, C5, C24–C26, V1.
These preserve historical completion claims, not a fresh all-tests-pass claim.
The 201-route figure means the recorded web/log/trial route set is reached,
not that mobile behavior is verified. Inventory still has one unmatched
pattern and nine unresolved dynamic call sites. Real-device O4 remains open.
V1 made parity advisory; image CI gates on `verify` and `rollback`.

Historical task detail/acceptance and earlier owner decisions are preserved
in [the pre-takeover roadmap](archive/reviews/2026-11-04-roadmap-before-takeover.md).
The [audit brief](archive/reviews/2026-11-04-mobile-audit-brief.md) specifies
A–G deliverables; the [review](archive/reviews/2026-11-04-simplification-review.md)
is input evidence, not an automatic statement of current runtime behavior.

## Current owner direction — preserve functionality while reducing code

### TQ01 · Review tests, oracle and corpus for TS-only constraints

- [x] DONE — 2026-11-06 review with three cheap built-in workers and architect
  source reconciliation; [extraction manifest](TEST-COMPATIBILITY-REVIEW.md).
  One standalone Express fingerprint test and eight mixed assertion families
  identified; no test/production/CI changes. Book/library goldens and persisted
  derivations retained; semantic quirks remain explicitly unapproved to change.
- **Checks:** direct source/client evidence, JSON denominators, documentation
  links/anchors and `git diff --check`. No runtime suites or regeneration run.
  Production/test code delta zero; this is not V2a/V2b retirement acceptance.
- **Follow-through:** R01–R08 were implemented in TQ01-I below after explicit
  owner authorization, preserving length correctness/security. I01–I08 still
  require semantic decisions. This review found a second ordinary equivalence
  CI job despite the advisory policy; TQ01-I resolved its diagnostic handling
  without changing required semantic verification, source or rollback gates.

#### TQ01-I · Implement semantic assertions and diagnostic separation

- [x] DONE — owner authorized implementation, 2026-11-06. Started with
  R02/R03/R05; then handle R01/R04/R06/R07/R08 individually. Replace only
  representation constraints with independent semantic assertions; retain
  useful upstream fingerprints in opt-in, non-blocking diagnostics. No
  production behavior, dependency, schema, persisted fixture or oracle-source
  changes. Production metric budget: zero change in every metric.
- [x] DONE R02/R03 — JSON values/types, single-document, actual length/HEAD and
  framing checks; original encoding checks tagged separately. Focused suites PASS.
- [x] DONE R05 — decoded ordered log records/client data and retained log
  destinations/filtering. Default and diagnostic suites PASS.
- [x] DONE R01 — opaque validators, obs-text grammar, unchanged/changed
  representation replay and conditional/cache/range semantics. Focused tests PASS.
- [x] DONE R04 — semantic error pages with security, hostile-path, public-error
  redaction and HEAD/range protections. Focused/default/diagnostic checks PASS.
- [x] DONE R06 — strict parsed OPML, retained decoded values and ACL/MIME;
  structure/hidden-content negatives and formatting equivalence PASS.
- [x] DONE R07 — four independent safe HTML token vectors; all original 59
  source rows preserved, 55 other exact expectations retained. Both lanes PASS.
- [x] DONE R08 — semantic PATCH comparison, captured response length and separate Node
  representation diagnostics; preserve state/corpus/rollback source evidence.
- [x] DONE Machine-readable assertion manifest + source-pinned reconciliation with
  Monstrobook external inventories; never alter their score or test inputs.
- [x] DONE Final affected/default/diagnostic suites and required integration gates,
  exact failures/skips/unexecuted evidence, contract + simplicity review,
  README/AGENTS/handover and reviewed publication. Validate in clean tracked
  snapshot plus approved overlays; preserve inherited identity/routes.go WIP.
  [Implementation/results](TEST-COMPATIBILITY-IMPLEMENTATION.md): build/vet/lint0,
  454 top-level Go passes / 1,536 pass events / three documented skips, Node
  semantic/harness 38/38, manifest+external snapshot 3/3, ten Go and two Node
  diagnostic tests, live core 193/193 plus all state checks and standalone
  pinned-TS rollback PASS. Every production metric delta zero. No Monstrobook
  score/import change; no device/browser/race/benchmark/parity-full acceptance.

L04/L05 and D01 remain accepted. D02 feature cuts are WITHDRAWN: the owner
requires preservation of functionality, including backup/restore and manual
audio tools used by the web UI. The uncommitted removal wave was discarded;
published source remains `82110311`. Do not count feature removal, package
moves, generated JSON or test deletion as behavior-preserving Go savings.

### D01 · Deletion feasibility inventory

- [x] DONE — architect-reviewed [historical inventory](archive/reviews/2026-11-05-deletion-feasibility.md): 4,607
  unique conditional production lines, exact AST manifest of 174 functions,
  positive web consumers and explicit unknown mobile use. Zero proven-dead
  core code; no credible behavior-preserving 1,000-line wave. Source/count/
  test-name/nonoverlap checks and diff check pass; no audit runtime changes.
- **Output:** one `DELETION-FEASIBILITY.md` with nonoverlapping exact files,
  functions, line bounds/counts, actual bundled/iOS/Android client evidence
  (unknowns explicit), category, gross deletion, replacement cost, estimated
  net savings, retained-behavior tests and owner decisions for every target.
- Investigate at least 3,000 unique candidate production lines. This is an
  inventory target, never permission to invent savings or classify essential
  code as removable. Report any shortfall and distinguish proven redundancy,
  replacement candidates, optional features and essential implementations.
- Cover catalog hydration/writes, user/list orchestration, playback, scanner,
  server-managed backups, conversion tools and metadata providers. Preserve
  playback FFmpeg, auth/path/SSRF guards, storage meaning and rollback assets.
  Measure shape-snapshot storage separately from production Go.
- **Check:** source/count/consumer citations, nonoverlap, net-cost arithmetic,
  explicit uncertainty and `git diff --check`. No runtime deletion in this card.

### D02 · Withdrawn feature-removal proposal — do not dispatch

- [ ] WITHDRAWN — backup/restore and manual audio conversion/embedding are
  required functionality. The audit already showed active web UI consumers;
  those footprints cannot be credited as safe code reductions.
- The local D02 implementation, disabled-route responses, removed tests/shapes,
  harness exceptions, generated annotations and lowered baseline were restored
  to `82110311` before any commit/push. No functionality loss or Go savings
  from that wave remain. D02a–e execution cards are superseded by this decision.
- Keep all existing operations, settings, events, paths and persisted meaning.
  Do not reopen a product-cut approval merely to hit a numerical line target.

### E01 · Major behavior-preserving simplification audit

- [x] DONE — bounded source review, three cheap-worker streams, architect
  review and [durable portfolio](DELETION-FEASIBILITY.md), 2026-11-06.
  **Quantitative target unmet:** no credible 1,000-line wave/thousands-line
  portfolio. No runtime changes, new dependency, feature cut or tests run.
  Local source/count/manifest/arithmetic/contract/link checks and diff check.
- Preserve all functionality; exact gross vs replacement/net, uncertainty and
  finite proof per opportunity. Count adapters/callers/SQL/templates too.
  Relocation, source packing, tests and generated snapshots are not Go savings.
- Do not manufacture a wave by adding microhelpers or extracting domains.
  The owner retains custom Socket.IO framing; E03 is the next small cleanup.

#### E01a · Repeated-mechanics census

- [x] DONE — 148 eligible error/log pairs (150 found, two excluded), exact duplicate string conversion, rounding
  loops and strict emitter subset inventoried. Architect increased HTTP helper
  cost allowance and rejected nested emitter guards that protect lazy DB work.
  No permission/truthiness/OS-stat consolidation or constructor no-op emitters.

#### E01b · Data/model/algorithm alternatives

- [x] DONE — reviewed author grouping, scanner sidecars, metadata patching,
  series datasets, DTO sharing, SQL JSON and transaction boilerplate. Small
  conditional reuse exists; no major equivalent deletion is established.
  JSON key order is free; stored meaning/field/null/order contracts remain.

#### E01c · HTTP/protocol/lifecycle alternatives

- [x] DONE — full Runtime cannot replace restricted read-only initialization.
  Shared status/media mechanics are small. Whole routing conversion has no
  positive deletion case. The owner retains custom Socket.IO framing and
  cancelled library replacement. Preserve protocol and lifecycle guarantees.

#### E01d · Architect portfolio and execution gate

- [x] DONE — ranked nonoverlapping candidates, reviewed leading source and C25
  history; conservative helper costs and finite proof tasks recorded. No thousand-line
  wave selected. The research card's completion does not meet the numerical
  target or authorize a dependency/feature cut. Runtime baseline unchanged.

### E02 · Socket library replacement — cancelled by owner

- [ ] CANCELLED E02a — exact Socket.IO ordering source proof; owner retains
  current custom Engine.IO/Socket.IO framing.
- [ ] CANCELLED E02b — adapter and dependency costing for library replacement.
- [ ] CANCELLED E02c — conditional dependency trial and framing replacement.
- These are cancelled by owner decision, not completed or failed research. No
  replacement, framing deletion or new dependency is planned. Ordinary
  transport fixes and protocol contract tests remain allowed.
- C25 recorded the prior library's auth-before-CONNECT-ACK race (51/200
  sequential connections failed); current framing fixed that ordering. See
  [DELETION-FEASIBILITY.md](DELETION-FEASIBILITY.md) for the retained rationale.

### E03 · Small consolidation cohort, separately measured

#### E03a · HTTP error response proof and common operation

- [ ] READY FOR BOUNDED IMPLEMENTATION — E03 is the next priority; no
  dependency. Scope `internal/logging/http.go`, existing logging tests and
  existing lists error helper only. One shared operation may call http.Error;
  preserve log source depth, query redaction, nil request/error, distinct
  log/response strings and response status/MIME/nosniff/newline. No universal
  error type or middleware. Tests compare recorded behavior with current calls.
- **Deletion:** replace existing lists helper's duplicated log+response body;
  do not delete remaining 148 pairs until E03b. Shared helper/refactor expected
  10–20 lines including comments/imports; temporary cohort growth bounded at
  +20 lines/+5 complexity, maps/[]any/high-function counts nonincreasing.
  Do not accept/push this intermediate alone. **Check:** logging/lists tests,
  source attribution and recorder status/headers/body; E03b pays complete cost.

#### E03b · Replace only manifested error pairs and duplicate coercion

- [ ] PLANNED — after E03a tests. Cheap worker owns only 148 exact call-site
  pairs in the strict archived edit manifest, `catalog.stringValues`/its caller/import and
  `userapi.roundTimes`/`roundMonths` plus their callers. No handler ownership,
  permission, transaction, status or early-return changes. Preserve evaluation
  order; exclude any unsafe argument evaluation discovered during execution.
- **Deletion:** 296 old pair lines replaced by 148 calls; delete the 14-line local
  coercion using existing jsonvalue.Strings; replace two rounding bodies with
  one concrete-key-generic function. Include E03a/helper/import/caller costs.
  Estimated cohort net ~150–160 before emitter work, not a thousand-line wave.
- **Check:** targeted logging/catalog/userapi/lists behavior tests plus AGENTS
  integrated gates once E03a–c are ready. Every metric nonincreasing against
  44,674/10,809/990 maps/326 []any/178 high functions; actual source net negative.
  Architect owns lower-only baseline update. No baseline editing by worker.

#### E03c · Strict nil-emitter subset, then cohort review

- [ ] PLANNED — cheap worker uses only the 17 architect-reviewed standalone direct
  guard sites in the strict deletion review edit manifest. Scope events.Emitter method,
  necessary field-type/import edits and these exact guards. No nested guards,
  payload/DB work, mixed conditions, loops or scanner buffer-reset changes.
- **Deletion:** replace each eligible three-line nil-only direct call with one
  nil-safe method call; count method/import/type costs. Verify nil no-op and
  nonnil recipients/payload/order. Do not create a generic event bus/default
  no-op constructor. Abort any site where argument evaluation changes.
- Review whole E03 diff after workers finish, then integration gates; report
  actual cohort net/complexity, lower baseline, mark individual cards DONE
  and update docs in the same ready commit. No new domain extraction follows
  automatically; the substantive deletion shortfall remains explicit.

## Earlier owner cleanup priorities — 2026-11-05

Latest approved sequence: **PR06 -> PR05 -> MC-05/F1 -> S9b-R1 -> L04 list work**.
PR06/PR05 execute sequentially as one coherent deletion wave; review and full
integration gates run once before both DONE markers land in the code commit.
Source/client research for later stages may proceed read-only in parallel.
MC-05 and F1 are accepted after source, contract, craft and full integration
gates. The source review found no requirement for a global cache layer. S9b-R1
records KEEP for the five framing-option families; S9b deletion remains
pending. L04/L05 are accepted after R2 and full gates. This dated sequence is
superseded by the current E01 result and E03 priority above.

I05/I06 and L01–L03 already landed before this priority update. Do not restart
them or expand into more domains. **This bounded cleanup checkpoint is CLOSED**
after RO02/S9a/PR01–PR04/PR07 and FF01 validation. Remaining S9b cleanup is pending; D02 cuts are withdrawn. No new
domain migration was started.

| Priority | Current action and exit |
|---|---|
| 1 | I05/I06 DONE (+165 production lines/+23 complexity); L reads DONE (+29/flat). Cleanup now removes 158 lines/46 complexity: 44,760 lines/10,848 complexity. All required checks pass; remaining adapters have named consumers/exit tasks. Net since pre-I05 is still +36 lines, complexity −23; no full-growth recovery claim. |
| 2 | RO01 audited the read-only HTTP path (ABS_GO_READ_ONLY=1). Superseded: the owner removed the mode entirely (F21, 2026-11-07); the server always runs read-write. |
| 3 | S9a DONE: ordered-key serializer deleted after both real mutation-response field/value/header contracts pass, plus cache/range/HEAD regressions. MC-05/F1 and S9b-R1 DONE; whole S9b deletion remains pending with KEEP decisions. |
| 4 | L02 already landed. Strengthened final-deletion criteria now apply before more writers; PR07 has deleted the pure playlist forwarder. |
| 5 | Branch docs point to canonical master. FF01 DONE: real isolated native libwebp FFmpeg, baseline and integrated full suites PASS with count=1; no codec shim/test weakening. TESTING.md records the environment and rebuild details. |
| 6 | Q1 now requires explicit growth review and strict ratchet. No baseline increase during cleanup. |

### RO01 · Audit restricted read-only HTTP reachability

- [x] DONE — source audit at c4cf90a9. Config.ReadOnly selects OpenReadOnly,
  skips file-log initialization and constructs NewHandler in main. Its auth,
  supported routes and 501 behavior differ from Runtime. All private helpers
  are referenced; no whole unused HTTP implementation is proven removable.
  Existing server/store/bootstrap tests protect parts of this path; no main
  startup integration execution is claimed by this read-only audit.

### RO02 · Remove duplicate base-prefix methods with stdlib

- [x] DONE — source review, focused before/after tests and integrated gates PASS.
  Two methods and the impossible branch deleted, net 25 production lines
  removed (final physical-line measurement replaces the worker estimate).
  Write only internal/server/server.go and runtime.go plus
  affected existing server tests. Replace Handler.routePath and Runtime.stripBase
  calls with strings.TrimPrefix using their existing configured base; delete
  both methods and routePath's unreachable false-result branch. The bool was
  always true. Preserve prefix matching, default paths and 501 behavior;
  do not alter socket dispatch/auth/startup or introduce segment matching.
- Budget: net lines/complexity fall; maps/[]any/high functions may not rise.
  No new helper/layer/dependency/baseline increase. Two direct stdlib calls
  suffice; replacing these methods with another wrapper is unnecessary.
- Verify full internal/server tests and existing prefixed ping, health GET/HEAD
  and runtime route regressions. Add a behavioral case only if current endpoint
  coverage misses a preserved edge. Final wave uses full AGENTS gates.

### FF01 · Restore real native WebP test capability

- [x] DONE — isolated native FFmpeg 9.0.2 built from signature-verified official
  source with libwebp; real encode/probe PASS. Full clean c4cf90a9 suite with
  `-count=1` PASS, exit 0; verbose TestJourney confirms all five former failures
  PASS. Final integrated cleanup suite also PASS, exit 0, with count=1.
  Installed FFmpeg 9.0.2 and Cypress 4.4 both fail real libwebp
  encoding; installed Homebrew WebP headers/libs are available. Build official
  FFmpeg source into an isolated temporary prefix; no global brew replacement,
  Go dependency, source-format change, test skip or cwebp masquerading shim.
- Record source version/URL/archive hash/configure flags, actual encode exit,
  deterministic PATH/FFMPEG_PATH test invocation and full-suite exit. Catalog
  resolves ffmpeg by PATH; FFMPEG_PATH alone cannot fix its image tests.
  Mark DONE only after a real encode and the required full suite execute.

## Execution conventions

- Follow dependencies, not old alphabetical phases. One bounded task per
  worker; split L tasks further if their write scopes become broad.
- Expensive architect plans/reviews. Cheap workers research/code/test within
  written scopes. Each accepted packet updates this queue and handover.
- State: planned / in progress / awaiting review / accepted / blocked. Record
  actual commands, exits/skips, evidence, measured benefit and next action.
  A written test plan is not a passing test.
- All tests listed below are **proposed unless marked existing**. Commands
  using new test names become meaningful only after those tests are added;
  verify they executed (no empty regex match or skipped integration).
- Keep fixed contracts, read-only upstream and unrelated dirty work in every
  packet. Existing reviewed-push authorization applies; new dependencies,
  route removals and live cutover retain separate decisions.
- Finish with applicable AGENTS checks. Q1 requires explicit growth review;
  strict tool enforcement stays in place. Node-tool deletion does not reduce
  the handwritten-Go metric.

## Master consolidation and pruning review — 2026-11-05

### G01 · Consolidate development on master

- [x] DONE. Fast-forwarded and pushed master to `948f6a15`; no conflicts.
  Local and remote branch inventories contain only master. Deleted integrated
  feature refs and the remote replacement-trial ref (zero unique commits).
  Preserved inherited untracked work and a verified stash/bundle; removed only
  the clean feature worktree and orphaned packet-worktree metadata. The packet
  directory itself remains preserved because Git could not establish status.
  Recovery details: [historical recovery](archive/reviews/2026-11-06-handover-before-e01.md#master-consolidation-and-recovery).

### G02 · Review tracked dead code and duplication

- [x] DONE. Cheap-worker audits plus independent architect source/reference
  review at `948f6a15`; [findings](PRUNING-REVIEW.md). Six uncalled private
  functions and equivalent helper replacements offer approximately 81 direct
  production lines. Analyzer exit 1 reflects its 12 findings; no code removed
  or deletion tests executed. Conditional candidates are excluded from 81.

### Small pruning packets — PR01–PR07, MC-05 and S9b-R1 accepted

I01–I06, L01–L03 and PR01–PR07 are complete. PR06/PR05 passed their conditional
equivalence/net-savings proof. MC-05/F1 and S9b-R1 passed source/HTTP review
and final integration gates. Whole S9b deletion remains pending; L04
characterization/planning is current.
No new domain
expansion occurred during the completed cleanup checkpoint. PR01–PR04 were independent;
execute sequentially on master because some files overlap. Preserve contracts,
owner WIP, test assertions and neighboring live helpers. No new dependencies,
generic framework, route deletion or baseline increases. Run targeted commands
below, then the AGENTS integration gates once on the reviewed wave. Measure
actual production/complexity deltas; check each card DONE in its code commit.
Acceptance for this deletion wave: net handwritten production lines must fall;
do not raise its baseline, add a new layer, or count moved code as a saving.
Record old definitions removed, replacement overhead and net delta per card.
Conditional work that cannot prove equivalent behavior and net reduction stays
pending with its evidence; it does not justify another speculative rewrite.

#### PR01 · Delete six uncalled private functions

- [x] DONE — six definitions deleted, no replacement, net 44 production lines;
  scoped packages and final integrated checks PASS. Write only catalog/batch_chapters.go (anyStrings),
  providers/book_finder.go (levenshteinSimilarity), scanner/files.go (sameBytes,
  parseSequence), userapi/userapi.go (nullNumber, timestampString).
- Recheck whole-module references; delete definitions only, remove imports
  only if compiler confirms unused. Estimated −41 lines. Verify from go-backend:
  `go test ./internal/catalog ./internal/providers ./internal/scanner ./internal/userapi`.
  Repeat unused analysis and classify remaining warnings; do not expand scope.

#### PR02 · Replace three membership loops with slices.Contains

- [x] DONE — three loops/helpers deleted, net 22 production lines; ACL/MIME
  conditions unchanged. Scoped and integrated checks PASS. Write only playback/playback.go, adminops/handler.go,
  realtime/hub.go; replace contains calls and delete local loops. Keep all
  permission conditions/negations and MIME logic. Estimated net −21 lines.
- Verify: `go test ./internal/playback ./internal/adminops ./internal/realtime`;
  ensure TestPayloadAccessibleAppliesLibraryAndItemPermissionsWhenPresent and
  TestDirectPlaybackRangeOwnershipAndProgressPersistence execute.

#### PR03 · Reuse existing nullable-value helpers

- [x] DONE — three helpers deleted, net 18 production lines;
  catalog/scanner/store and integrated checks PASS. Write only catalog/quickmatch.go (nullValue -> store.NullString),
  scanner/metadata.go (nullDBString -> store.NullString), catalog/authors.go
  (valueOrNil -> store.StringPtrValue). Delete local bodies; add required store
  imports. Invalid/nil stays nil; valid empty string stays a string. Net ~−16.
- Verify: `go test ./internal/catalog ./internal/scanner ./internal/store`.
  Inspect series sequence writes, author updates and metadata nullability.

#### PR04 · Reuse store.Text for identity SQL parameters

- [x] DONE — full identity/store and integrated checks PASS; 13 callers converted,
  old helper deleted, net 4 lines. Necessary scope includes identity/oidc.go's
  seven package-level callers, not just identity.go. Write only those two
  files: text -> store.Text; delete
  helper/comment. Empty text must remain Valid=true; never use TextOrNull.
  Estimated −3 lines. Verify: `go test ./internal/identity ./internal/store`.
  Preserve inherited untracked identity/routes.go without staging it.

#### PR05 · Share the equivalent playback/sharing UA parser (S8 subset)

- [x] DONE in this code commit after PR06 scoped verification: both old parsers
  and playback's token helper deleted; one stdlib-only deviceinfo.ParseUserAgent
  serves both callers. Ten identical expected cases pass both original parsers
  in immutable 24d199e6 source and the new leaf; device-row assertions retained.
  Net production −39 lines; full paired-wave checks PASS. Write only playback/playback.go,
  sharing/sharedevice.go and new deviceinfo/ua.go + ua_test.go. Characterize
  both tuple parsers first, including precedence/case/marker/delimiter cases
  in the review. Extract one leaf function, delete both replaced copies;
  leave userapi's different parser intact. Target ~−30 net lines; measure
  package/import overhead before accepting a new package.
- Verify: `go test ./internal/deviceinfo ./internal/playback ./internal/sharing ./internal/userapi`.
  Retain device-row and absent-field assertions; no UA recognition expansion.

#### PR06 · Point legacy-helper tests at production implementations

- [x] DONE in this code commit — all original assertions retained and redirected
  to active functions; three obsolete production helpers deleted. Before/after
  scanner/sharing and full paired-wave checks PASS; net 35 production lines
  removed. Write only scanner/files.go + scanner_test.go and
  sharing/handler.go + handler_test.go. Redirect mergeBookMetadata tests to
  mergeBookMetadataWithABS with hasABS=false; filename wrapper test to
  filenameNumbers with identical empty arguments; sharing sqlDate setup to
  dbtime.Format. Delete only these replaced wrappers/bodies (~31 production
  lines). Keep all assertions; no test-only copy of removed production logic.
- Verify: `go test ./internal/scanner ./internal/sharing`, including metadata
  precedence and filename track/disc tests. groupKey/cdDirectory are excluded
  until real grouping-path coverage is established.

Paired PR06 -> PR05 acceptance at 24d199e6: production **44,760 -> 44,686
(−74)**; complexity **10,848 -> 10,823 (−25)**; maps **993 -> 992 (−1)**.
[]any 326, high functions 179 and other metrics unchanged. Build/vet/lint (0),
dbnest, Linux/arm64 CGO=0 build and full native-codec Go suite count=1 PASS.
Lower-only baseline update and final make ratchet PASS; no growth exception.
No SQL generation input/caller, schema, protocol or persistence behavior changed.
Original parser characterization used temporary package-local tests against the
same retained fixture; no legacy parser oracle/framework is shipped. Owner
untracked identity routes excluded from exact tracked-source validation.

#### PR07 · Remove the now-unnecessary playlistJSON forwarder

- [x] DONE — eight direct callers, forwarder deleted, net 3 production lines;
  list/userapi and integrated checks PASS. Depends on completed L02, not L05.
  Write only internal/userapi/playlist.go and internal/userapi/library_lists.go.
- Replace each h.playlistJSON(r,id,user) call with
  lists.Playlist(r.Context(),h.db,h.items,id); its user parameter is unused.
  Add the lists import where needed and delete the forwarding method entirely.
  Keep ownedPlaylist authorization before its read, all status/error branches,
  list sorting and mutation/event payload sequencing. No writer/route move.
- Verify: `go test ./internal/lists ./internal/userapi`; existing L01 mixed-media/
  ACL, playlist sorting and collection-conversion tests must execute. Reference
  search must find no playlistJSON definition or caller. Expect only a small
  net saving of about two lines after the one new import; measure it.
- collectionDTO remains for its real per-user ACL adaptation and old error/
  visibility contract. Its removal needs the scoped caller migration in L04/L05;
  do not duplicate its callback at every callsite merely to delete a function.

#### Cleanup acceptance evidence

All changes above share one reviewed code commit. Per-packet net physical
production lines: RO02 −25, S9a −42, PR01 −44, PR02 −22, PR03 −18, PR04 −4,
PR07 −3; total **−158**. Complexity 10,894 -> **10,848 (−46)**; maps 995 ->
**993 (−2)**, []any 326 and high functions 179 unchanged. No metric increased.
The existing lower-only make ratchet-update lowered the three baseline values;
final make ratchet PASS. Before lowering, exit 1 requested matching the lower
counts, not a growth exception. No ratchet implementation/gate was weakened.

Exact tracked-source snapshot at c4cf90a9 plus approved source overlays:
build/vet/lint (0 issues)/dbnest/Linux arm64 build PASS; pinned sqlc generation
and generated-directory equality PASS. Full go test ./... -count=1 with the
verified native FFmpeg PASS, exit 0. Scope, formatting, deleted-symbol checks
and git diff --check PASS. Owner untracked identity routes remain untouched
and excluded. Node rollback/device acceptance/latency were not run locally.
Logs and the actual toolchain/environment are recorded in TESTING.md/handover.

## 1. Compatibility evidence — alongside domain migration

### MC-A · Commit-pinned client audit (P0, M; documentation)

Intent: establish actual obligations before changing protocols or deleting
routes. Scope: `contracts/mobile-client-audit.md`, gap/test plans, route/event
decision evidence; six clients named in the audit brief. No runtime edits.
Prerequisite: public source access; pin the default-branch SHA and retrieval
time for each. Distinguish confirmed/inferred/not found/not investigated.

Acceptance: each verified client × operation has a SHA permalink, headers/
payload/transport expectations, Go path, named test or MISSING, evidence
status and priority; all six review statuses and protocol matrix are explicit.
Unknowns produce investigation tasks, never unsupported-feature decisions.
Check every evidence link and source span; review auth, WS-only, progress,
range, cache, HLS/offline, cover format and version branches. Do not call a
partial inspection an exhaustive operation inventory. Done when A/C/D/G of
the brief are complete or precisely marked open with executable follow-ups.
Approval: audit authorized now; follow-up source research stays read-only.

### MC-B / R1 · Reproducible inventory and route decisions (P0, M)

Intent: prevent demonstrated mobile endpoints being removed as “unused”.
Scope: `tools/clientusage/main.go`, its tests, a checked external-evidence
manifest under `contracts/`, generated `contracts/client-usage.json` and a
route/event decision table. Prerequisite: the relevant confirmed MC-A evidence, with remaining audit
unknowns carried forward. No route deletion.
Smallest proposed change: merge pinned external operation records into the
existing generator with provenance; keep unresolved call sites visible and
classify actual Go registrations as well as upstream inventory entries.

Acceptance: `GET /api/me/progress`, `/api/playlists`, `/api/collections` are
protected, as is source-confirmed iOS `POST /api/session/local-all`;
every route gets exactly one KEEP /
SIMPLIFY / REMOVE CANDIDATE / INVESTIGATE with evidence and approval status.
Do not hand-edit generated JSON. Proposed `TestExternalClientEvidence` and
`TestInventoryDeterministic` in `tools/clientusage/main_test.go`; run
`cd go-backend && go test ./tools/clientusage`, then root `make client-usage`
twice and compare outputs. Use existing `TestOutputIsCurrent` as the non-writing
freshness check; no new check flag is needed. Done when external evidence is reproducible,
all dynamic unknowns remain visible, and no web-only absence implies removal.
Approval: queue approval to implement; per-route R2 approval remains separate.

### MC-C · Socket.IO contract and resource tests (P0, M)

Intent: protect auth/live progress and bound the in-repo protocol maintenance
risk. Scope: `internal/realtime/{eio,sio,hub}.go`, tests,
`internal/server/socket_connect_test.go`, CI interop setup only as needed.
Prerequisite: relevant MC-A source rows; reproduce suspected differences first.
Keep Engine.IO v4, default namespace and both legacy/prefixed paths; do not
replace the subsystem or add polling/binary support without demonstrated need.

Acceptance: use an actual Socket.IO v4 library to test auth→init, invalid and
expired token expectations (including `invalid_token` vs `auth_failed`),
re-authentication, reconnect, recipients and acknowledgements on both paths.
Existing `TestSocketIO4WebsocketAuthAndPrefixedPath` skips without the JS
library: ensure it executes in required CI, not just in advisory parity.
Test malformed frames, the actual library frame cap and fragmented-message
limits against client payload needs, heartbeat, queue pressure and cleanup.
The advertised Engine.IO `maxPayload` alone does not specify a WS frame cap. Proposed `TestInboundPayloadLimit`,
`TestSocketReauthentication`, `TestSocketReconnectDelivery` in realtime.
Commands from `go-backend`: `go test -v ./internal/realtime ./internal/server
-run 'Socket|Packet|Ping|Buffer|Payload|HubClose'` with the pinned JS client
available, then `CGO_ENABLED=1 go test -race ./internal/realtime` and full
gates. Done when required cases execute and any reproduced gap has a narrow
regression/fix; no claim of unknown transports supported. Size excludes a
new transport implementation. Approval: queue; approve provisioning the existing JS `socket.io-client@4.8.1`
probe in required verify CI (currently installed only in advisory parity).
The historical Go v3 test-client approval is a different dependency.

### MC-D · REST/media/offline value regressions (P0, M)

Intent: test client-required behavior beyond structural shapes. Scope:
`internal/contracttest/`, focused tests beside `catalog`, `playback`,
`userapi`, `httputil`; runtime fixes only for reproduced failures.
Prerequisite: relevant MC-A rows. Preserve headers' semantics, playback and
persisted state; no Express byte formatting requirement or artwork conversion.

Acceptance: seeded-user tests assert ETag conditional GET→304 with empty body;
byte/suffix/invalid/unsatisfiable ranges with 206/416, exact bytes and
Content-Range; direct audio and HLS play/session URLs and MIME types;
`/api/session/local` AND `/api/session/local-all`, then query progress/session
values and reopen persistence; representative decoder values/ordering/ACLs;
actual cover parameters and decodable format while retaining original art.
Proposed `mobile_protocol_test.go`: `TestMobileConditionalGET`,
`TestMobileMediaRanges`, `TestMobilePlayback`, `TestMobileOfflinePersistence`,
`TestMobileCoverParameters`. Run `cd go-backend && go test -v
./internal/contracttest -run 'TestMobile'`, affected package tests and full
checks. Done when all source-confirmed P0/P1 behaviors have meaningful passing
assertions or a separately approved, explicit unresolved blocker.
Approval: queue; contract/persistence changes beyond repair remain prohibited.

### MC-E / O4 · Device acceptance (P0, S; owner/device time)

Prerequisites: MC-C/D passing for relevant cases; preserve copy-only isolation.
Scope: `contracts/mobile-client-audit.md` and handover evidence, no live swap
by a worker. Owner tests official iOS/Android plus affected third-party apps
on a disposable initialized install: existing token login/refresh, browse,
seek, download, offline sync, reconnect and progress on a second client.
Record app/server versions, platform, procedure, results and safe logs.
Done when explicit results support the declared client scope; source and
simulator tests cannot substitute. Approval: owner arranges device access and
any live cutover separately. Blocks R3 and final retirement of diagnostics.

## 2. Domain migration and measured simplification

### Dispatch policy: small cards, not whole epics

Owner follow-up, 2026-11-04: explicitly include the **books domain** and make
work concrete for cheap models. Implementation is now authorized; the preceding request only planned these cards. S13/P2/P3 and S11 below are epics,
not single worker assignments. Compatibility MC tasks remain necessary for
the operations they affect; do not wait for the entire six-client census
before a behavior-preserving book extraction with relevant passing tests.

Each card below has an explicit completion checkbox and status; pending cards are size S (one narrow result, normally at most
four production files plus tests). Paths are relative to go-backend. New paths
and tests are marked `new`. A dependency means implemented/tested predecessor code, not merely a plan.
Cross-worker reuse requires reviewed integration; an explicitly authorized
single-worker feature batch may validate sequential cards before its final
review. Do not dispatch blocked cards.

Worker gets only: common rules + its card + named source/test snippets. Do not
load the entire roadmap, all skills or previous chats. Preserve SQLite/JSON/
protocol meaning, row closure, owner work and immutable goldens. No dependencies,
schema/route/auth changes, unrelated SQL conversions, commits or baseline edits.
If scope expands beyond the card, return the blocker to the architect; do not
invent an abstraction. Report changed paths, exact test results/skips, measured
query/complexity change if applicable, and remaining issue in at most 12 lines.

Each card's command below is the targeted worker check, not the whole release
gate. Before accepting an integration wave run applicable AGENTS build/vet/full
tests/lint/ratchet checks once on the integrated tree; record card results and
wave validation separately. Never call a card fully verified before its gates.
The architect owns architecture/documentation integration. Per the latest
owner instruction, a worker may complete sequential cards as one coherent
feature batch (B02–B07), running card checks as it goes; final code review is
once the batch is ready. Avoid creating and deleting temporary APIs merely to
publish intermediate cards. Worker completion means awaiting review, not push.

#### Current execution status — B01–B09 and I01–I06 DONE

Canonical checkout: `/Users/paul/projects/audiobookshelf-go`, `master`.
All four book/documentation commits from base `48c10a6a` through `948f6a15`
were fast-forwarded and pushed to master on 2026-11-05. The merged feature
branch/worktree and already-integrated `replacement-trial` remote were retired.
Documentation baseline is `d64aa599`.

| Cards | State / actual implementation |
|---|---|
| B01 | DONE (`d2ec8d9`): independent catalog value assertions and unchanged source goldens |
| B02–B07 | DONE (`d2ec8d9`) as one coherent wave: `books.Load(ctx, db, id)` and `Book.Full/Minified/Expanded`; no transitional metadata exports or production catalog builders |
| B08/B09 | DONE (`d2ec8d9`): shared persisted JSON normalization; deleted stateless DB wrapper and redundant numeric conversion |
| I01 | DONE (`d2ec8d9`): test-only SQL query counter, direct + prepared execution verified |
| I02 | DONE: bounded LoadMany; scalar Load delegates; 0/3/6 read boundary proofs |
| I03/I04 | DONE: one settings snapshot + ordinary page batch; actual 10/50 returned books use 7/7 reads |
| I05/I06 | DONE in this code commit: book shelf batch + shared item rows; 10/50 shelf hydration reads 6/6; replaced query/decoder/classifier bodies removed |
| Lists | L01–L03 DONE in this code commit: baseline + lists.Playlist/Collection; old userapi read bodies deleted, forwarding adapters retained for named consumers |

Book feature d2ec8d9 snapshot metrics versus 48c10a6a: **44,585 production Go lines (−1)**,
**complexity 10,840 (−11)**, **991 dynamic maps (−2)**. This is a boundary and
duplication improvement with essentially flat source size, not a large deletion.
Those d2ec8d9 baselines were only lowered. The following performance wave has
a separate, explicitly reviewed source-cost adjustment; enforcement remains strict.

Build, vet, dependency lint (0 issues), dbnest, ratchet and Linux/arm64
CGO-disabled build PASS. Final full `go test ./...` exits 1 for the exact five
pre-existing native FFmpeg/libwebp TestJourney cases, with no new failures;
affected package tests and persisted JSON tables pass. Feature scope reviewed;
**full release acceptance remains incomplete** on this host. No encoder shim,
test deletion or blanket baseline raise was used. Details: handover.

I02–I04 performance-wave metrics versus ef4d14e: **44,724 production Go lines
(+139)**, **complexity 10,871 (+31)**, `[]any` **325 (+2)**; dynamic maps 991
and high-complexity function count 178 unchanged. This is new bounded batch
capability, not source-code reduction. Architect accepted the measured source
cost for 43/203 -> 7/7 endpoint reads, one read owner, and deletion of replaced
singleton SQL; no generic repository/framework/dependency or source packing.
The three baseline values were explicitly adjusted in this feature commit;
Q1 was not implemented and no guard was removed. Future growth still needs
measured benefit and review; reduction remains a project goal.

I05/I06 accepted wave: tracked-source metrics **44,889 lines (+165)**,
**10,894 complexity (+23)**, maps **993 (+2)**, []any **326 (+1)**,
high-complexity functions **179 (+1)** versus `7105382c`. Architect explicitly
accepts the new bounded common-item loader and typed row boundary for measured
51/251 -> 6/6 shelf hydration reads; this is a source-cost exception, not a
reduction. The five baseline values are adjusted in this code commit, without
weakening the strict ratchet or adding a dependency. Decoder/SQL/classifier
duplication and extra shelf lookup maps are removed, not left as alternatives.

Build/vet/lint (0 issues)/dbnest/arm64 build and affected packages PASS.
Full suite exits 1 only for the same five pre-existing libwebp journey cases;
no additional failures. Tracked-source validation snapshot excludes inherited
untracked WIP; see handover. Node rollback/device/latency tests not run.

L01–L03 accepted wave: **44,918 production lines (+29)** and maps **995 (+2)**
versus `4d34d494`; complexity **10,894 unchanged**, high functions 179 and []any
326 unchanged. Explicit architect-reviewed baseline exception for list read
ownership and temporary forwarding boundaries, with no stateless Reader or
new dependency. Old read bodies, mediaRef/refs and visible counter are deleted;
this package extraction is not a net source reduction or a speed claim.
Focused/full lists+userapi, build/vet/lint (0), dbnest/arm64 and final ratchet
PASS. The following is historical I05/I06 delivery evidence, superseded by
FF01 and the final successful L05 gates: sqlc v1.31.1 generation was identical;
no new generated output. That full suite exited 1 only for five native WebP
journey failures. No schema/write/event/route changes; rollback/device/bench
not rerun. See handover for evidence and retained adapters.

Next work: **E03 bounded consolidation**. E01 source review is DONE; D02 is
withdrawn and E02 is cancelled. L04 planning, MC-05/F1, S9b-R1 and PR01–PR07 are DONE. Whole S9b deletion
remains pending. Each stage names the old
code it deletes, any necessary
adapter consumers and the exact later removal condition.
Podcasts, collapsed/include paths and other consumers remain open; no whole
P2/P3/S13 completion claim. Full mobile research and owner decisions continue.

#### First milestone: explicit book reads + book-page batching (S13/P2/P3)

Completed spine: `B01–B09`, `I01–I06`, `L01–L05`. D01 is accepted and D02
is withdrawn. E01 research is complete; E02 library replacement is cancelled.
E03 owns the next small cleanup. Mixed-media consumer migration remains separate.
Book metadata/chapter writes and complete mixed-media migration are not claimed
finished by this milestone. All route removals and real-device gates stay intact.

##### Stage-by-stage removal contract

| Stage | Old code to remove / deliberate retention |
|---|---|
| I01 (DONE) | Test-only query instrumentation; no production code replaced or claimed deleted. |
| I02 (DONE) | Old scalar book SELECT/relation reads deleted; Load delegates to one LoadMany implementation. |
| I03 (DONE) | Repeated prefix-settings reads removed from batched book-page hydration; titlePrefix remains for named nonbatched callers. |
| I04 (DONE) | Per-book scalar hydration removed from ordinary no-include/noncollapsed pages; podcast/includes/collapsed paths retain explicit fallback. |
| I05 (DONE) | Per-ID book/no-includes hydration replaced; podcast/includes fallback retained. Temporary common-envelope SQL removed by I06 in the same wave. |
| I06 (DONE) | catalog scanItemRow deleted entirely; scalar loadItem SQL, I05 envelope query/stringsToArgs and local fileType switch deleted. items owns shared row projection/scanner/loader/classifier; recordToMap and thin loadItem remain for page/detail/shelf consumers until mixed-media projection migration. |
| L01 (DONE) | Tests only, no production deletion. Independent full-schema/live-reader assertions and existing regressions retained. |
| L02 (DONE) | playlistJSON SELECT/media-entry body and redundant mediaRef/refs copy deleted. ownedPlaylist retains pre-hydration authorization; PR07 has now deleted the forwarder and all eight callers use lists.Playlist directly. |
| L03 (DONE) | collectionDTO SELECT/item-loop/assembly and redundant visible counter deleted. HTTP/error/feed/pagination orchestration stays. Thin per-user adapter preserves old four-value contract until all consumers migrate in L05. |
| L04 | Documentation only: each write/route packet names original functions to delete; L05 removes all now-unreferenced forwarding adapters after final wiring. |

Measure actual net lines/complexity at review; moving code alone is not a size
reduction. No legacy implementation may coexist with its accepted replacement.

##### B01 · Pin book projection values

- [x] **DONE** — implemented, architect-reviewed and pushed in `d2ec8d9` (2026-11-04). Scoped checks passed; the five pre-existing WebP full-suite failures remain documented.

- **Depends:** none. **Write only:** Tests only: `internal/catalog/handler_test.go`, `item_golden_test.go`.
- **Do:** Add `TestBookProjectionValues` using existing `catalogFixture`/golden fixture. Assert missing vs empty descriptions, [] relation arrays, null series sequence, track/item IDs, excluded-file counts and source audio order for all three book views. Existing `TestBookItemSerializersMatchPinnedSourceGolden` and `TestItemGolden` stay independent; do not regenerate goldens.
- **Check (from go-backend):** `go test ./internal/catalog -run 'TestBookProjectionValues|TestBookItemSerializersMatchPinnedSourceGolden|TestItemGolden' -count=1`.
- **Done:** Named assertions execute on current code; record observed baseline. No production edits; a discovered failure becomes a separate reproducer/fix card.

##### B02 · Move single-book reads into books

- [x] **DONE** — implemented, architect-reviewed and pushed in `d2ec8d9` (2026-11-04). Scoped checks passed; the five pre-existing WebP full-suite failures remain documented.

- **Depends:** B01. **Write only:** `internal/books/reader.go`; reader integration tests in `internal/catalog/book_reader_test.go`; `internal/catalog/book_wire.go`, `handler.go`, related `handler_test.go`.
- **Do:** Implement plain `books.Load(ctx, db, bookID)` returning typed `books.Book` data; B09 removed the initially proposed stateless Reader wrapper. Move loadBookRow/loadBookSource and bookRelations SQL/decoding into the books.Load function. Return typed author/series slices ordered by existing association createdAt ASC, preserving nullable sequence. Catalog calls Load; it no longer performs these book reads or converts relation maps back into wire types. Keep projection implementation and existing wire declarations in place. Do not introduce a callback from books to catalog.
- **Check (from go-backend):** `go test ./internal/books ./internal/catalog -run 'TestLoadBookNullsAndRelationOrder|TestBookProjectionValues|TestBookItemSerializersMatchPinnedSourceGolden|TestItemGolden' -count=1`.
- **Done:** New TestLoadBookNullsAndRelationOrder exercises real SQLite nulls/order/not-found. Remove old scans and bookRelations implementation, adapt its test without making the expected result call the new implementation. All Rows close before the next query.

##### B03 · Give book metadata projection one owner

- [x] **DONE** — implemented, architect-reviewed and pushed in `d2ec8d9` (2026-11-04). Scoped checks passed; the five pre-existing WebP full-suite failures remain documented.

- **Depends:** B02. **Write only:** `internal/books/metadata.go`; shared catalog value tests; `internal/catalog/book_wire.go`.
- **Do:** Move fullMetadata/minifiedMetadata/expandedMetadata, authorNames/seriesName/descriptionPlain to books using Book data. Title-ignore-prefix is passed as a precomputed nullable value; retain catalog titlePrefix/settings ownership. The completed wave calls private metadata helpers from books projections directly; no intermediate exported adapters were published. No new settings cache, prefix policy or people-domain dependency.
- **Check (from go-backend):** `go test ./internal/books ./internal/catalog -run 'TestLoadBookNullsAndRelationOrder|TestBookProjectionValues|TestBookItemSerializersMatchPinnedSourceGolden|TestItemGolden' -count=1`.
- **Done:** Shared TestBookProjectionValues checks nil/blank descriptions, author/series order and sequence, title prefix and plain description. Delete duplicate helpers in catalog; all metadata helpers are private after B07.

##### B04 · Move full book media projection

- [x] **DONE** — implemented, architect-reviewed and pushed in `d2ec8d9` (2026-11-04). Scoped checks passed; the five pre-existing WebP full-suite failures remain documented.

- **Depends:** B03. **Write only:** `internal/books/projection.go`; shared catalog value tests; `internal/catalog/book_wire.go`.
- **Do:** Move bookFull assembly to Book.Full(itemID), returning existing wire.BookFull. Move bookEbook/encodeJSON only with their consumers; catalog can forward to the single owner until B07. Keep opaque nested source JSON behavior and concrete wire value inside outer item map.
- **Check (from go-backend):** `go test ./internal/books ./internal/catalog -run 'TestLoadBookNullsAndRelationOrder|TestBookProjectionValues|TestBookItemSerializersMatchPinnedSourceGolden|TestItemGolden' -count=1`.
- **Done:** TestBookProjectionValues checks decoded fields/null/arrays and libraryItemId. Old Handler.bookFull may be forwarding-only until B07, with no duplicate assembly.

##### B05 · Move minified book media projection

- [x] **DONE** — implemented, architect-reviewed and pushed in `d2ec8d9` (2026-11-04). Scoped checks passed; the five pre-existing WebP full-suite failures remain documented.

- **Depends:** B04. **Write only:** `internal/books/projection.go`; shared catalog value tests; `internal/catalog/book_wire.go`, `handler.go` only for book-specific count/size helpers.
- **Do:** Move bookMinified assembly to Book.Minified(titlePrefix). Move includedAudioCount/audioSize into books when their production callers are book-only; keep test oracle calculations independent. Catalog computes title prefix as before and delegates. Do not change settings queries yet.
- **Check (from go-backend):** `go test ./internal/books ./internal/catalog -run 'TestLoadBookNullsAndRelationOrder|TestBookProjectionValues|TestBookItemSerializersMatchPinnedSourceGolden|TestItemGolden' -count=1`.
- **Done:** TestBookProjectionValues covers track count vs files, chapters, ebook size/format, null vs zero duration. No duplicate minified implementation; forwarding adapter ends in B07.

##### B06 · Move expanded book media projection

- [x] **DONE** — implemented, architect-reviewed and pushed in `d2ec8d9` (2026-11-04). Scoped checks passed; the five pre-existing WebP full-suite failures remain documented.

- **Depends:** B05. **Write only:** `internal/books/projection.go`; shared catalog value tests; `internal/catalog/book_wire.go`, `handler.go` for makeTracks only.
- **Do:** Move bookExpanded to Book.Expanded(itemID, titlePrefix), including ordered track URL/offset construction. Move makeTracks if book-only; leave number/fileType helpers shared by podcast paths alone. Keep wire.BookExpanded, existing route URLs, units and RawMessage behavior.
- **Check (from go-backend):** `go test ./internal/books ./internal/catalog -run 'TestLoadBookNullsAndRelationOrder|TestBookProjectionValues|TestBookItemSerializersMatchPinnedSourceGolden|TestItemGolden' -count=1`.
- **Done:** TestBookProjectionValues uses hand-calculated offsets/order/item IDs and skipped-file case. Catalog has no expanded assembly beyond forwarding; no playback/session changes.

##### B07 · Remove book migration adapters

- [x] **DONE** — implemented, architect-reviewed and pushed in `d2ec8d9` (2026-11-04). Scoped checks passed; the five pre-existing WebP full-suite failures remain documented.

- **Depends:** B06. **Write only:** `internal/catalog/book_wire.go`, `handler.go`, `batch_chapters.go`; `internal/books/metadata.go` visibility only.
- **Do:** Call the new Book projection methods directly from ItemJSON/ItemMinifiedJSON/expandedBookItem. Delete obsolete forwarding builders/types/helpers; retain stringPointerValue in its actual remaining owner (batch_chapters) if still needed. Make metadata helpers private when catalog no longer calls them. Keep HTTP orchestration in catalog for now.
- **Check (from go-backend):** `go test ./internal/books ./internal/catalog ./internal/items -count=1`.
- **Done:** rg confirms no catalog loadBookRow/loadBookSource/bookRelations or duplicate bookFull/bookMinified/bookExpanded assembly; books imports no catalog/items/server. Book read/projection extraction complete, book writes explicitly still unmigrated.

##### B08 · Share persisted JSON normalization (review follow-up)

- [x] **DONE** — implemented, architect-reviewed and pushed in `d2ec8d9` (2026-11-04). Scoped checks passed; the five pre-existing WebP full-suite failures remain documented.

- **Depends:** B07. **Write only:** `internal/store/json.go`, `json_test.go`;
  catalog decoder definitions in `handler.go`; book reader decoder calls.
- **Do:** one lenient persisted-JSON slice decoder (strings + object arrays)
  and one object decoder, reused across catalog and books. Preserve non-null
  empty containers, whitespace/null and partial type-error semantics. These
  helpers are not HTTP request validators; book empty-ebook normalization stays.
- **Check:** `go test ./internal/store ./internal/catalog ./internal/books -run 'JSON|BookProjection|LoadBook|ItemGolden|BookItemSerializers' -count=1`.
- **Done:** duplicate decode bodies removed, behavior tables pass, measured
  code/complexity recorded. Completed and reviewed with the book feature.

##### B09 · Remove stateless read wrapper (review follow-up)

- [x] **DONE** — implemented, architect-reviewed and pushed in `d2ec8d9` (2026-11-04). Scoped checks passed; the five pre-existing WebP full-suite failures remain documented.

- **Depends:** B08. **Write only:** books reader/metadata/projection files;
  their catalog call sites and tests.
- **Do:** use `books.Load(ctx, db, id)` rather than a Reader object duplicating
  the DB handle already in catalog. Delete the wrapper/constructor/extra field.
  Reuse JSON numeric conversion and keep name projection helpers in metadata.
- **Check:** the same focused tests as B08, dependency lint and `make ratchet`.
- **Done:** one explicit DB dependency, no wrapper or duplicate numeric helper;
  no source packing/baseline raises to claim reduction. Completed.

##### I01 · Add test-only SQL query counter

- [x] **DONE** — implemented, architect-reviewed and pushed in `d2ec8d9` (2026-11-04). Scoped checks passed; the five pre-existing WebP full-suite failures remain documented.

- **Depends:** B07. **Write only:** `internal/catalog/query_count_test.go` (new), fixture setup in `handler_test.go` only if needed.
- **Do:** Use a test-only database/sql/driver wrapper around the installed modernc SQLite driver, forwarding supported context/prepare/transaction interfaces. There is no Store query hook today: do not add a production observer or repository interface. Reset count after seeding; count direct/prepared reads consistently. Add TestQueryCounterIncludesPreparedReads to prove the counter sees both.
- **Check (from go-backend):** `go test ./internal/catalog -run 'TestQueryCounterIncludesPreparedReads|TestBookProjectionValues' -count=1`.
- **Done:** Actual SQL reads are counted and instrumentation does not change results/connection behavior. Record current 10/50-book page counts as baseline, not a fixed-count PASS. No runtime instrumentation.

##### I02 · Batch books and their relations

- [x] **DONE** — implemented and architect-reviewed 2026-11-05; delivered with this code commit. Scoped checks and measured query counts below; global WebP baseline remains unresolved.

- **Depends:** I01. **Write only:** `internal/books/batch.go`, `reader.go`; `internal/catalog/book_reader_test.go` reuses the real SQLite fixture/counter.
- **Do:** Add books.LoadMany(ctx, db, bookIDs) -> map[string]Book using book rows + author links + series links, one query per kind per bounded chunk (250 unique IDs maximum per chunk). Reuse Load row decoding; implement books.Load via books.LoadMany if that removes duplication while retaining sql.ErrNoRows. Deduplicate lookup IDs, retain per-book createdAt ordering, close each Rows before the next query; no SQL per book.
- **Check (from go-backend):** `go test ./internal/books ./internal/catalog -run 'LoadMany|LoadBook|BookProjection|BookItemSerializers|ItemGolden|QueryCounter' -count=1`.
- **Done:** Tests compare independent known multi-book/null/order values and 0/1/250/251 IDs, missing IDs absent from map, canceled context propagated. No table/schema changes; query-count assertion comes through I04 integration counter.


Actual: 0 IDs -> 0 reads; 1/10/50/250 unique IDs -> 3; 251 -> 6. Missing IDs (including orphan associations) omitted; scalar ErrNoRows, cancellation and no partial error result pass. Old scalar SQL/relation implementation deleted.

##### I03 · Read title-prefix settings once per book batch

- [x] **DONE** — implemented and architect-reviewed 2026-11-05; delivered with this code commit. Scoped checks and measured query counts below; global WebP baseline remains unresolved.

- **Depends:** I02. **Write only:** `internal/catalog/book_batch_test.go` (new); `handler.go` titlePrefix/settings helper extraction; a separate helper file only if needed.
- **Do:** Extract the existing sorting-prefix read into a small helper reused by titlePrefix and the batch page. Load it once per batch, apply existing pure catalogTitlePrefixAtEnd and books projections to selected rows. Preserve default the/a, configured empty prefixes, nullable/blank title behavior and existing wire views. Do not add a batch service or universal response DTO. No cross-request cache or podcast changes.
- **Check (from go-backend):** `go test ./internal/catalog -run 'BookPageQueryCount|BookProjectionValues|BookItemSerializersMatchPinnedSourceGolden|ItemGolden' -count=1`.
- **Done:** TestBookPageQueryCountIndependentOfPageSize covers actual batched responses across default/custom/mixed/empty prefix updates and nullable titles; original full/minified/expanded goldens remain green. Reuses the page settings snapshot, no added settings query or batch service.

##### I04 · Batch the ordinary book library page

- [x] **DONE** — implemented and architect-reviewed 2026-11-05; delivered with this code commit. Scoped checks and measured query counts below; global WebP baseline remains unresolved.

- **Depends:** I03. **Write only:** `internal/catalog/handler.go:libraryItems`, `query_count_test.go`, `handler_test.go`.
- **Do:** After collecting and closing libraryItems Rows, assemble book media through I03 instead of calling each book serializer. Scope ordinary noncollapsed book page, no includes first; leave podcast/collapsed/include branches unchanged and name their follow-up. Keep SQL filters, total, pagination, selected IDs, ACL and ordering unchanged.
- **Check (from go-backend):** `go test ./internal/catalog -run 'TestBookPageQueryCountIndependentOfPageSize|TestBookProgressFiltersAndSortUseCurrentUser|TestBookProjectionValues|TestItemGolden' -count=1`.
- **Done:** New TestBookPageQueryCountIndependentOfPageSize checks 10/50 books have equal read count with identical options, and correct IDs/order/values. Full vs minified query variants each tested. Counter validates actual endpoint path, not just loader.


Actual endpoint proof: read-only archived ef4d14e + the same test gave 43/203 reads for 10/50 returned genre/ACL-filtered books; current path gives 7/7, ordinary and minified envelopes. Every returned ID/order, pagination, explicit ACL exclusion, total/flags and null/prefix values asserted. Above 250 book IDs, hydration adds 3 reads per chunk; no claim of unbounded constant work or measured wall-clock/RSS improvement.

##### I05 · Batch book shelf hydration

- [x] **DONE** — implemented and architect-reviewed in this code commit.
  Focused/full catalog and wave checks recorded above. 10/50 actual selected
  books: 51/251 -> 6/6 reads through personalizedItems, not endpoint totals.
  I06 removed the temporary envelope SQL before acceptance.

- **Depends:** I04. **Write only:** `internal/catalog/personalized.go:personalizedItems`, new `shelf_batch_test.go`; reuse existing query counter and handler fixtures.
- **Do:** Collect all IDs, check Rows.Err and explicitly close Rows before hydration. For book shelves load common libraryItems envelopes in one query per chunk, then use I03. Preserve shelf order, per-user progress/ACL and error behavior. Podcast hydration and per-item includes stay explicit follow-ups, never label all shelves batched.
- **Check (from go-backend):** `go test ./internal/catalog -run 'TestBookShelfQueryCountIndependentOfSize|TestPersonalizedBookShelvesUsePerUserProgress|TestItemGolden' -count=1`.
- **Done:** New TestBookShelfQueryCountIndependentOfSize compares 10/50 book IDs without includes and asserts same counts/correct user progress/order; no per-ID ItemMinifiedJSON on the migrated path. No nested pool reads with open Rows.

##### I06 · Extract common item row decoding

- [x] **DONE** — implemented and architect-reviewed in this code commit.
  One items.Record/ScanRecord/LoadMany/RowColumns/FileType owner; old catalog
  scanner/classifier/scalar SQL and I05 envelope SQL/argument helper deleted.
  Boundary/value/date/cancellation/query-count tests and wave gates recorded above.

- **Depends:** I05. **Write only:** `internal/items/row.go`, `row_test.go` (new); `internal/catalog/handler.go:scanItemRow/loadItem/fileType`; I05 envelope helper in `personalized.go` and tests in `shelf_batch_test.go`; `batch_chapters.go` only mechanical shared-classifier calls.
- **Do:** Define typed items.Record and common row decode/ID batch load; keep books/podcast-specific projection outside the row type. Move generic field/time/file normalization from scanItemRow using existing dbtime parser, and have catalog adapt the record to the established outer JSON map. Absorb/delete I05 envelope batch SQL. Do not expand items.Reader or migrate every consumer in this card.
- **Check (from go-backend):** `go test ./internal/items ./internal/catalog -run 'TestItemRowValues|TestBookProjectionValues|TestPodcastItemDetailSerializesEpisodeAndTrack|TestItemGolden' -count=1`.
- **Done:** New TestItemRowValues asserts ID/mediaID separation, dbtime milliseconds, optional scan/size fields, file classification and malformed date failure. One row decoder; wire map adapters remain explicitly until mixed-media contracts are migrated.

#### Next domain milestone: lists (S11), not one mass move

These cards start after the book-read milestone and relevant list contract
baselines. They preserve current podcast list behavior; book-only batching
never justifies filtering out podcast entries. S11 completes only after the
final wiring/removal card and integration gates. Later domains get equally
small cards only when this milestone is reached; do not prepay an exhaustive
implementation plan for untouched packages.

**Final-deletion checkpoint (applied now; L02 already landed):** every later
list packet must name the old functions/branches and remove their replaced
bodies in that same commit. No second SELECT/DTO/transaction implementation
may remain behind a forwarder. A bridge needs its exact consumers and a
  concrete removal card; PR07 has removed playlistJSON. collectionDTO is retained
only for request-user ACL/error adaptation until that responsibility moves.
L05 acceptance requires no obsolete handlers/adapters or production references
to them, all original routes mounted once by the new owner, mixed book/episode
field/value/order/NULL and ACL/status regressions, unchanged DB write formats,
transaction/order/cleanup and event recipient/payload tests, plus fresh route
inventory and integrated metrics. A package move or unchecked removal template
cannot complete S11. No more list write migration while cleanup is open.

##### L01 · Pin list reads/permissions

- [x] **DONE** — architect-reviewed test baseline in this implementation wave.
  Focused named command and full userapi package PASS. Full-schema/live catalog
  fixture covers ordered mixed media, null descriptions, owner/library/item
  ACL, partial-library list filtering and all-filtered empty collections.
  Tests only: no production deletion claimed. I05/I06 pushed as `4d34d494`.

- **Depends:** I06. **Write only:** Tests only: new `internal/userapi/list_read_test.go`; existing `userapi_test.go`, `playlist_collection_regression_test.go`, `playlist_order_test.go` only if shared fixture changes are necessary.
- **Do:** Add TestListReadValuesAndPermissions for GET /api/collections, /api/playlists and detail using owner/other-user/no-library cases, real IDs/order/null description, book + podcast episode entries. Reuse existing fixture and keep existing playlist/order/regression tests.
- **Check:** `go test ./internal/userapi -run 'TestListReadValuesAndPermissions|TestPlaylistCreateListAndOwnerRestriction|TestPlaylistListsSortByLocaleCompare|TestCreatePlaylistFromCollectionPersistsExpandedBookItems' -count=1`.
- **Done:** Independent semantic/ACL baseline exists before ownership changes; no production edits.

##### L02 · Extract playlist read data

- [x] **DONE** — architect-reviewed in this code commit; focused/full lists+
  userapi and wave gates above. Old playlistJSON read body and mediaRef/refs
  copy deleted; thin adapter and pre-hydration authorization retained.

- **Depends:** L01. **Write only:** `internal/lists/reader.go`, `reader_test.go` (new); `internal/userapi/playlist.go`.
- **Do:** Move playlistJSON's GetPlaylist/ListPlaylistMediaItems and metadata/entry assembly into plain lists.Playlist(ctx, db, itemReader, id). Explicit dependencies match books; no stateless Reader object/constructor/extra Handler field. Keep existing response map as a temporary boundary. ownedPlaylist retains its authorization SELECT before hydration; preserve status/order and orphan/error handling. Do not move writes/routes. Lists never imports userapi/catalog.
- **Check:** `go test ./internal/lists ./internal/userapi -run 'TestPlaylistReadValues|TestListReadValuesAndPermissions|TestPlaylistListsSortByLocaleCompare|TestCreatePlaylistFromCollectionPersistsExpandedBookItems' -count=1`.
- **Done:** New TestPlaylistReadValues covers book/episode entries/order/nulls; no duplicate read implementation. Userapi retained the thin forwarding adapter at delivery; PR07 has since deleted it without waiting for L05.

##### L03 · Extract collection read data

- [x] **DONE** — architect-reviewed in this code commit; ordered values,
  nulls/times, callback order/failure, all-denied empty books and missing row
  tests pass, alongside L01 HTTP baseline and wave gates. Old read body and
  redundant visible counter deleted; per-user callback adapter retained.

- **Depends:** L02. **Write only:** append Collection to `internal/lists/reader.go` (same imports, small shared read file), `collections_test.go` (new); collectionDTO in `internal/userapi/userapi.go`. Library list/HTTP orchestration remains unchanged.
- **Do:** Move collectionDTO SELECT/assembly into plain lists.Collection(ctx, db, itemReader, id, canView), returning map/libraryID/error. Pass a per-call callback `func(context.Context, string) (bool, error)` that binds the request user and adapts canAccessItem. Remove the redundant visible counter: its old predicate is always true on successful reads. The existing collectionDTO adapter returns hasVisible=(err==nil), preserving its error/status behavior and successful empty visibility. No stateless Reader/constructor/extra Handler field. Keep callback order, map boundary and item serialization. collectionRoute and internal/userapi/library_lists.go keep HTTP/list-ID selection/pagination/feed/ACL orchestration; no write/route move or import back to userapi/catalog.
- **Check:** `go test ./internal/lists ./internal/userapi -run 'TestCollectionReadValues|TestListReadValuesAndPermissions|TestCollectionDescriptionDBValueMatchesLegacyNullCoercion|TestCreatePlaylistFromCollectionPersistsExpandedBookItems' -count=1`.
- **Done:** New TestCollectionReadValues asserts list/detail/order/library visibility and values; old duplicate reads removed, forwarding endpoints preserved. If extraction needs >4 production files, return exact dependency list for split.

##### L04 · Plan and characterize final list ownership

- [x] DONE — architect-reviewed four-card plan below is based on actual
  callers and exact old owners. It closes planning only; it does not mark S11
  complete or authorize changes outside these cards. MC-05/F1 is published.

The implementation cards below form one final feature batch on `master`.
Execute them in dependency order and keep intermediate work buildable; do not
push an intermediate state that leaves duplicate/removed routes or a broken
tree. Review and publish the accepted final cohort once the cards and L05 gates
pass. No branches, production bridges, schema changes, or new dependencies.

##### L04a · Characterize existing list routes and write outcomes

- [x] DONE — test-only baseline accepted on clean `7c08c25b` plus the two
  test overlays. Worker owns
  `internal/contracttest/lists_routes_test.go` and may add only the specific
  existing userapi state/event assertions needed to close a discovered gap.
- **Depends:** L03 and existing accepted read/write fixtures. Freeze behavior
  through `Runtime.ServeHTTP` before changing routing. Cover default and
  `/audiobookshelf` router base paths; root, ID and nested collection/playlist paths;
  supported GET/POST/PATCH/DELETE, plus OPTIONS, HEAD and unsupported methods or
  subpaths. Assert exact legacy status/body/Content-Type/auth behavior, no
  redirects, and one- versus two-terminal-slash cases. In particular, measure
  whether `//` at the root is accepted; do not assume it is rejected. Keep
  the current 501 fallback for unsupported methods/paths.
- **Writes to characterize:** persisted values/order, cleanup, ACL/status
  precedence, and event names/recipients/payloads for existing playlist and
  collection mutations, using current userapi tests and fixtures. Add only
  missing state/event assertions. Do not change production code in L04a.
- **Check:** focused contract and existing userapi tests. **Done:** the matrix
  and state/event outcomes pass against the current implementation; no
  production modifications. `TestListsRouteBaseline` and full `userapi` PASS,
  format/diff checks clean. Roots accept `//`; IDs/nested routes reject it.
  Single trailing slash remains accepted; unsupported verbs stay 501.

##### L04b · Consolidate shared request/value helpers

- [x] DONE — helper moves accepted as preparation within the final cohort;
  +1 production line, −2 complexity, −3 maps; all other metrics flat.
  Affected tests/build/lint0/dbnest PASS on clean `7c08c25b` plus eight overlays
  (`/tmp/abs-pr06-pr05-20261105/l04b`). Exact old definitions removed, all current
  callers updated; part of the final cohort, not an independently published
  behavior change.
- **Write only:** `internal/httputil/json.go` and its tests;
  new `internal/jsonvalue/value.go` and tests;
  `internal/userapi/userapi.go`, `internal/userapi/me_extra.go`, and exact
  existing callsites found by repository-wide reference search.
- Move the object request decoder to `httputil.DecodeObjectRequest`, preserving
  `UseNumber`, body close and object-only behavior; update all callers and
  delete old `decodeBody`. Move the exact existing `jsTruthy` and
  `stringValue` bodies to tiny `internal/jsonvalue` `Truthy` and `String`
  functions; update every current caller and delete the old definitions. Keep
  the `json.Number` conversion quirk. Replace the userapi permissions map
  decoder with `store.DecodeJSONObject` at all current userapi references and
  delete its wrapper.
- **Pre-dispatch budget:** at most +20 physical production lines; all other
  ratchet metrics must not increase. Benefit: one existing request decoder and
  shared coercions with exact legacy semantics, without duplicates or domain
  cycles. The smaller alternative of keeping duplicate helpers is rejected.
  This preparation is not separately publishable; L04c must recover its cost
  and deliver a net decrease in every ratchet metric before L05. Namespace
  overhead counts. No baseline increase or cross-packet offset.
- **Check:** decoder and jsonvalue focused tables plus affected userapi tests.
  Preparatory budget: at most +20 production lines; all other metrics must
  not increase. Shared primitives avoid duplicated semantics and domain cycles;
  the four old helper bodies disappear. This card is not separately published
  or accepted against a raised baseline. L04c must recover this preparation
  cost and reduce the final cohort's net lines, with all metrics nonincreasing.

##### L04c · Move list HTTP ownership as one cohort

- [x] DONE — atomic production ownership move after L04a evidence and L04b
  helper availability. No temporary production bridge: old and new list HTTP
  owners must not coexist.
- **Write only:** new `internal/lists/routes.go`,
  `internal/lists/playlist_http.go`, `internal/lists/collection_http.go`;
  `internal/lists/handler.go` only if the established composition requires
  its single constructor; `internal/server/runtime.go`;
  `internal/userapi/userapi.go`; delete `internal/userapi/playlist.go`,
  `internal/userapi/library_lists.go`, and `internal/userapi/collection_feed.go`.
- Compose one `lists.Handler` from `db`, `items.Reader`, `emit`,
  `canView(ctx, store.User, itemID)` and
  `canAccessLibrary(store.User, libraryID)`, with `Routes(*http.ServeMux)`.
  Runtime constructs and mounts it. `userapi` exports `CanReadItem` and
  `AllowsLibrary` delegates to existing `canAccessItem` and
  `canAccessLibrary`; do not consolidate/change policy. Resolve the principal
  with `store.UserFromContext` after runtime authentication. Use
  `store.DecodeJSONObject` for permission JSON.
- Move list CRUD/read, library list and collection feed HTTP bodies once into
  `lists`. Move list-only `stripMarkup`, `cleanName`,
  `sendEntityNotFound`, `sendEntityNotFoundStatus` and
  `respondPlaylistError` after reference checks; keep their behavior. Do not
  substitute `textclean.Strip`. Delete the old collection dispatch/branches
  and userapi list methods with the move. Remove the always-success boolean
  adapter and protect an empty-but-visible collection as HTTP 200 in tests.
  Keep the actual ACL-aware collection helper if still used; it is distinct
  from the redundant boolean adapter.
- Preserve exact path/method dispatch, including 501 behavior. Use method
  ServeMux registrations with one HEAD rejection guard, so GET's implicit
  HEAD matching retains legacy 501. The first methodless-wrapper design was
  rejected for +178 lines/+12 complexity/+2 `[]any`; no baseline was raised.
  Change `serveRoute` only as
  needed to accept the exact measured trailing-slash cases for lists; no
  redirect or global path normalization. Preserve observed root `//` behavior.
- **Exact old ownership to remove:** `userapi.Handle` list dispatch;
  `userapi/playlist.go` playlist read/write/feed-to-playlist bodies;
  `userapi/library_lists.go` library list handlers;
  `userapi/collection_feed.go` collection RSS handler; collection handlers
  in `userapi.go` including `collectionRoute`, CRUD/membership branches and
  list helper methods. Move each body once. Retain no redundant bool/error
  adapter or duplicate transaction/query implementation.
- **Check:** `go test ./internal/lists ./internal/userapi ./internal/server`,
  build, vet, full suite with the documented native FFmpeg, lint, ratchet
  (all metrics nonincreasing, including dbnest), sqlc regeneration with
  generated-directory identity, Linux/arm64 CGO-disabled build, and the route
  coverage generator. No metric baseline increase or cross-packet offset.

##### L04c-R1 · Remove the first routing implementation's overhead

- [x] DONE — same L04c files and original nonincrease budget. Replace
  per-route verb wrappers and the playlist tail parser with direct method
  registrations and one HEAD guard. A single two-segment POST handles the
  overlapping collection-to-playlist/item paths without a second router.
- Delete alias pattern registries, fake probe requests, duplicate request
  cloning and the unused runtime lists field; reuse `withPath`. Preserve the
  measured slash matrix using only scoped list path normalization.
- The temporary typed-map slice experiment was discarded; R2 instead removes
  duplicated string-array conversion and identical internal-error pairs. No
  aliases, counter changes, source packing or SQL/transaction change.
- Check affected packages, L04a routes, build and metrics on the exact clean
  shipping tree before full gates. Rejected growth is not an accepted stage.

##### L04c-R2 · Delete duplicate internal-error pairs and root adapters

- [x] DONE — architect-directed repair after R1 remained +54 lines
  and +2 `[]any`; complexity/maps fell. The original final budget is unchanged.
- In the same lists files, parameterize the existing read-error writer with
  its constant message; replace only identical log/error pairs (same message,
  error value, status 500). Delete replaced pairs and unused logging imports.
  Keep transaction/return sequencing and client error bodies unchanged.
- Bind collection/playlist root GET/POST directly to native HTTP entry methods;
  remove the second root-method dispatcher. No extra handler framework.
- Correct playlist action routing: two-segment conversion/item paths and
  three-segment batch paths must remain distinct. Add negative POST cases for
  `/{id}/add` and `/{id}/batch/item`; both must remain 501 without mutation.
- Diagnose the extra slice uses from actual source evidence; no aliases,
  counter changes, arbitrary source packing or baseline increases. Repeat
  affected tests, original route baseline, build and metrics before acceptance.
- Confirmed cause: create/update collection copied the old shared string-array
  conversion. Extend this repair only to move userapi's exact `stringSlice`
  body once into `jsonvalue.Strings`, update existing userapi callers and use
  it for the two list callers; delete the duplicated loops/old helper. Preserve
  empty strings and typed-slice behavior; batch empty-ID filtering stays intact.

##### L04d · Refresh route ownership evidence

- [x] DONE — prepare the producer mapping alongside L04c, then generate
  evidence after the ownership move. Update `tools/parity/build_route_coverage.py` ownership mapping
  for collection/playlist routes and `GO_SOURCES` to `internal/lists/routes.go`;
  regenerate/review `contracts/parity/route-coverage.json`. Preserve route
  provenance; do not alter source inventory unless router source changed.
- **Check:** rerun generator and verify the checked-in evidence exactly matches
  output. **Done:** all existing list route cases are assigned once to lists,
  no stale userapi route ownership remains.

##### L05 · Final list-cohort acceptance

Accepted source: clean `7c08c25b` plus exact approved overlays/deletions.
Actual final metrics: **44,674 lines (−29), complexity 10,809 (−23), maps 990
(−2), high-complexity functions 178 (−1), `[]any` 326 (flat)**; other metrics
flat. Build/vet/full native-codec suite/lint0/arm64/ratchet+dbnest PASS; sqlc
1.31.1 nine-file generated identity PASS. Lower-only baseline accepted.
226 ordered upstream routes/21 lists owners and 201 client routes preserved;
coverage remains 0 verified/226 unverified. Both generated inventories refreshed.
Logs: `/tmp/abs-pr06-pr05-20261105/l04c-*`; final source/artifact/baseline identity
checked for 25 paths. Earlier stale-inventory and wrong-FFmpeg runs failed;
both corrected before the passing run. No local TS rollback/device/bench run;
SQL/storage semantics unchanged. See handover for environment/WIP boundaries.

- [x] DONE — final review card, executed only when L04a–L04d are ready.
- **Check:** all L04a state/event/route tests, L04c focused and full checks,
  L04d route evidence, and final `git diff --check`; review no stale userapi
  handlers/adapters/references, one route owner per path, and net source
  reduction. Confirm every ratchet metric is nonincreasing against the
  recorded baseline: 44,703 lines, 10,832 complexity, 992 maps, 326 `[]any`,
  179 high-complexity functions. Shared-helper growth is not accepted until
  the complete cohort meets the budget. **Done:** architect accepts the
  tested final tree; no route removal is implied. L04/L05 do not complete all
  of S11 or the broader mobile audit.

#### Scope still open after these milestones

S13/P2/P3 are not marked complete until podcast projections, mixed-media
batching, includes/collapsed paths and each remaining playlist/progress/playback/
scanner consumer are migrated with their own tests. Book metadata/chapter
writes need separate state/event contracts. No worker receives “finish items”
or “move all catalog” as a task. Architect decomposes only the next slice.
Tentative domain order after books/items reads and lists: people (S10), progress (S12),
then library/items HTTP and remaining users/auth/admin (S14/S15), gated by E01/E03 findings
and an end-to-end deletion case for each extraction. Relevant
compatibility tests are prerequisites per slice, not a global audit freeze.

### L8a–c · Selective domain-owned sqlc (P2, S each)

Replaces L8's arbitrary “under 100 raw call sites” gate. Prerequisite: choose
queries actually touched by the accepted loader/domain packet; MC-D for its
outputs. L8a catalog static reads (`queries/catalog.sql`, catalog); L8b scanner
static writes/reads (new `queries/scanner.sql`, scanner); L8c podcast static
queries (new `queries/podcasts.sql`, podcasts). Dynamic filter/sort SQL stays
handwritten. No schema, DTO or lifecycle redesign in these tasks.
Acceptance per slice: named old query/scans removed, typed generated query
used, equivalent NULL/time/order/error semantics tested. Root `make sqlc`,
check tracked + untracked generated files, affected package/persistence tests
and full gates. Done when selected queries and measured delta are recorded;
no conversion quota or invented savings. Defer slices with no clear benefit.
Approval: queue; sqlc itself already approved.

### R2/R3 · Owner-approved behavior removal (P2, S per approved group)

Prerequisites: MC-B complete decision table, MC-E and relevant regressions;
no unresolved dynamic callers for the candidate. R2 records owner KEEP/REMOVE
per operation. R3 changes only explicitly approved handlers/registrations and
obsolete tests, then proves 404 where approved and remaining clients green.
Exact files/tests must be filled from MC-B **before dispatch**; do not give a
worker the whole inventory to delete. Commands: affected package tests,
`make client-usage`, contract suite, full gates. Done when each deletion has
approval, evidence and measured removed implementation. Approval: individual
removal required; no candidates approved by this takeover.

### S9 · Simplify framing, keep HTTP semantics (P2, S)

- [ ] PARTIAL — no blanket option removal. Execute the bounded subcards below;
  validators/ranges/MIME and remaining client evidence keep their own gates.

#### S9a · Delete ordered-key JSON scaffolding

- [x] DONE — net 42 production lines removed. Both actual POST-response tests
  passed before and after; scoped cache/range/HEAD/ACL tests and integrated
  native-codec full suite PASS. OrderedKeys and marshalOrdered have no Go refs.
  Baseline tracked production source: 44,918 lines, 10,894
  complexity, maps 995, []any 326, high functions 179. Budget: every metric
  nonincreasing, net source reduction required, no new abstraction/dependency.
- Write only httputil/json.go/options.go and their existing tests; the two
  OrderedKeys arguments in userapi/playlist.go:addPlaylistItem and
  userapi/userapi.go:writeCollectionAfterBatch; new focused mutation-response
  test using existing full-schema fixture. No mutation implementation changes.
- Before deletion, freeze complete response field sets, IDs/order/null values,
  status, Content-Type and Content-Length versus body length for both POST
  responses. lists.Playlist/Collection construct complete top-level maps;
  their caller key lists omit no actual fields. Web client consumes parsed
  JSON via Axios; architecture permits key-order changes. No GET cache or
  byte-range implementation is touched. Existing field/ACL, 206/416, cache
  and HEAD regressions accompany this slice; no device acceptance claim.
- Delete OrderedKeys, options.keys, marshalOrdered and its WriteJSON branch
  plus both caller options. Use encoding/json once. Remove the now-obsolete
  byte-order/drop-unlisted-keys helper test only after actual response fields
  are independently asserted. Keep all other framing options and safeguards.
- Verify httputil/userapi plus named catalog cache/range regressions; final
  AGENTS gates. Expected tens of production lines removed; report actual net.

#### S9b · Validator-sensitive framing — deletion pending

- [ ] DELETION PENDING — no framing option deletion authorized. `ContentType`
  has four provider error callers; `WithoutETag`, `WithoutContentLength`,
  `Streamed`, and `ForRequest` are also used. Counts establish scope only.
  KEEP means safe equivalence has not been proven; it does not mean every
  option/header is mandatory. See bounded S9b-R1 review and
  [evidence](MC05-S9B-EVIDENCE.md). No growth budget is approved; any later
  deletion card must name exact old code/tests and reduce net production lines.
- First protect affected endpoint status/body/MIME/auth/cache/HEAD and actual
  client usage. Then scope one caller family, with measured replacement cost;
  do not globally delete ForRequest, Streamed, WithoutETag or length handling.

Prerequisites: MC-05 evidence review and affected cache/range/cover tests. Scope:
`internal/httputil/` Express-only ordering/escaping/framing options and callers.
No removal of ETag/If-None-Match/304, Range/206/416, cache behavior or required
MIME negotiation. First name each unnecessary option and prove no client
requirement; remove it and tests of byte-only details. Run
`cd go-backend && go test ./internal/httputil ./internal/contracttest` and full
gates. Done when supported HTTP semantics pass and obsolete paths disappear.
Approval: queue; any externally observable semantic removal goes through R2.

#### MC-05 · Lissen typed-JSON behavior characterization

- [x] DONE — pinned Lissen source and 12 typed-JSON route cases are
  characterized. A successful 200 body without ETag is accepted; the client
  caches only when ETag exists, so the server does not need a global cache
  layer or mandatory ETag generation. The per-route contract also requires
  304 only when the response advertises a validator, with auth before 304.
  Bounded scope only: MC-A/B/C/D/E and full mobile/device audit remain open.
  Evidence: [MC05-S9B-EVIDENCE.md](MC05-S9B-EVIDENCE.md).

#### MC-05/F1 · Lissen conditional typed-JSON correction

- [x] DONE — preserve the 12 typed-JSON routes in
  `TestMobileConditionalGET` and the separate original/resized cover and
  width-only author image cases in `TestMobileCoverParameters`, both in
  `internal/contracttest/mobile_protocol_test.go`. Pinned
  Lissen accepts a successful 200 body without ETag and caches only when ETag
  exists; absent ETag therefore does not require a server cache layer or
  conditional 304. When a validator is advertised, matching If-None-Match
  must yield an empty 304; auth must precede 304. `author_items` previously
  returned 200 despite an advertised ETag; F1 replaces the old producer with
  a request-aware replacement at 14 callsites. Budget: at most +20 physical production lines and
  +12 complexity, with maps/`[]any`/high-complexity-function counts not
  increasing. Required benefit: the replacement handles conditional
  semantics; leaving it untouched retains the author_items mismatch. F1
  measured +17 lines/+9 complexity; JSON-writer style count 1→2 is a rename
  of the same writer, accepted as +1 after review, not a second implementation.
  No schema, dependency, route or global cache layer. Verify focused mobile
  contract rows, freshness/auth/raw and width-only image cases,
  catalog/userapi/httputil packages, and all integration gates PASS.
  Acceptance log: `/tmp/abs-pr06-pr05-20261105/mc05-f1/verification`.
  No SQL inputs or callers changed; rollback, real-device and benchmark suites
  were not rerun. Root adjusted three baseline values in the same code commit.

#### S9b-R1 · Review evidence and decide whether to retain option families

- [x] DONE — bounded evidence review only. Review current
  caller evidence and MC-05 findings, then record KEEP for ContentType,
  WithoutETag, WithoutContentLength, Streamed, and ForRequest until a specific
  family has a safe-equivalence proof. Call counts do not establish necessity.
  Focused `httputil` and contract tests plus MC-05/F1 final integration gates
  PASS. R1 deletes no caller or option. It closes this review milestone while
  S9b deletion remains pending; unproven optional cleanup does not block
  independent L04 characterization/planning or list implementation. MC-A/B/C/D/E
  and full mobile/device acceptance remain open.

### V2a/V2b · Extract reusable tooling, then retire diagnostics (P2, M/S)

V2a scope: `tools/contractfixture/build.cjs`, `tools/clientusage/`, route
inventory producer, shared seed/args modules, shape/benchmark/browser tooling,
Makefile and CI rollback imports. Prerequisite: MC-B design; not L8 completion.
Keep rollback operational. Inventory all inbound dependencies before deletion.
Extract fixture seed and route evidence into standalone owners and prove
fixture generation and `make client-usage` work without removable modules.
Use a disposable checkout for destructive regeneration comparisons; compare
logical fixture data and repeat generator output, not nondeterministic DB bytes.

V2b depends on V2a, MC-E and owner acceptance of retained evidence. Delete
only unreferenced differential infrastructure, preserve rollback and required
browser/benchmark/shape inputs. Run post-removal fixture regeneration,
`make client-usage`, Go contract/persistence tests and CI rollback equivalent
`node go-backend/tools/parity/replacement-trial.cjs --rollback-only --work
<disposable-dir>` (update this command if extraction moves its entrypoint).
Done when generators and rollback work from a clean checkout after removal;
record Node lines separately from handwritten-Go metrics. Approval: revised
retirement boundary before V2b; prior approval never authorizes broken generators.

### Q1 · Explicit growth review; retain strict ratchet

- [x] DONE — policy adopted in this documentation commit at owner direction.
  The proposed report-only/soft-metrics change is superseded. No ratchet,
  correctness gate, baseline, CI or runtime code changes in this packet.
- Before worker dispatch, the existing task card must record: tracked-source
  baseline; maximum proposed line/complexity/map/[]any/high-function deltas;
  concrete required behavior or measured benefit; smaller alternatives;
  exact old symbols/files removed; any retained adapter's callers and removal
  card; validation and cleanup-checkpoint condition. No separate ledger.
- Architect reviews the actual diff and evidence against that budget. A
  worker cannot edit baselines; exceeding budget returns to architecture
  review instead of quietly raising it. Necessary growth needs a written
  acceptance reason and actual per-metric deltas in the same code commit as
  its narrowly scoped baseline edit. Existing dependency/route approval
  boundaries still apply; this does not authorize either.
- Ratchet remains failing on increases. No global allowance, warning-only
  mode, cross-packet offset, source packing or Node-as-Go savings. A deletion
  packet must be net negative and cannot raise its baseline.
- After a growth-bearing migration wave, close a cleanup checkpoint before
  dispatching another ownership expansion: measure integrated net cost,
  identify/remove safe duplicates and dead code, give every retained bridge a
  concrete removal task, record tests/limitations. A list of future cleanup
  ideas alone does not close that checkpoint.
- Checks for this prose-only policy: source/tool-rule consistency and
  `git diff --check`; no runtime tests required or claimed. Tool automation
  beyond the existing strict ratchet is not implied.

## 3. Deferred work and investigations — do not dispatch vague tasks

Preserve these IDs and their historical acceptance in the archived roadmap.
Before execution, architect supplies current file scope, regression, benefit
and prerequisites; this table deliberately is not authorization to code.

| IDs | Priority / reason / release condition |
|---|---|
| C1–C4, C7/C8/C11 | P1 suspected/known share, podcast schedule, watcher, cancel, sort/name/feed behaviors; reproduce with named historical regression before fix; do not prescribe timer architecture without review. |
| C12 → S6 | P0 permission-equivalence investigation before consolidation; matrix tests for library/tag/explicit/admin rules, then one definition. |
| C13/C19, C23 | P1 download/session lifecycle and race CI; focused ownership/race evidence first. |
| C15/C16 | P1 request JSON error handling and attachment truncation; targeted failure reproduction, security review if exposure changes. |
| C18 | P2 tracked test-log cleanup; small documentation/housekeeping packet only. |
| S1/S3/S4 | P2 routing migration after protocol coverage, package by package; preserve route precedence/method/base-path semantics. Untracked identity routes must be reconciled first. |
| S5/S7/S8 | P2 constructor/process-runner duplication remains deferred; the equivalent playback/sharing UA subset is scoped as PR05. userapi UA semantics remain separate. |
| S10/S12/S14/S15 | Domain migration follows the books/items/lists milestones above; decompose per handler family before dispatch. S11 is active planning above, not deferred. |
| S16 | P2 maps where known shape causes risk; drop arbitrary under-100 quota as acceptance. |
| P1/P4/P5 | P2 settings cache, read pool, filter-data updates; benchmark first, audit transaction/lifecycle correctness; rollback-journal invariant stays. |
| P6 | P1 baseline client-critical benchmark accompanies S13/P2/P3; detach tool dependency in V2a, no new benchmark framework. |
| Cover cache/format standardization | P2 investigate six-client format parameters/decoders first; preserve original artwork and persisted layout. No WebP-only approval. |
| Polling/binary Socket.IO support | INVESTIGATE only where client evidence requires it; no whole-subsystem replacement proposed. |

## Decisions needed

1. Books/items reads and lists are now explicit migration milestones. Implement
   them alongside affected compatibility tests; no full audit freeze or blanket
   L8 conversion prerequisite. Runtime dispatch approval is not inferred from
   a planning document; the latest owner message authorizes implementation.
2. The 30% target remains aspirational. Q1 now requires explicit budgeted growth
   review and cleanup checkpoints; strict ratchet tooling remains unchanged.
3. Accept the supported-client scope after MC-A; unknown evidence stays open.
4. R2 approves removals individually; O4 requires real-device evidence.
5. O1 (`ANALYZE` at boot), O2 (destructive TS cleanup) and O5 (new libraries)
   remain undecided. Defaults: no new boot writes/deletes/dependencies.
6. External Flash provider use must be allowed to receive repository material.
   This session's automatic review rejected that transfer; built-in cheap
   workers were used instead. No provider-access approval assumed.

## Takeover validation and next action

Documentation/audit evidence and commands actually executed are recorded in
[handover.md](handover.md). Implementation and device tests are not implied.
G01/G02 were consolidation/analysis only and executed no pruning. Later,
separate PR01–PR07 cards completed and were accepted; those are not attributed
to G01/G02.
Current sequence: I01–I06/L01–L03 and the bounded cleanup are accepted;
PR01–PR07, MC-05, MC-05/F1 and S9b-R1 are DONE. Whole S9b deletion remains
pending under KEEP decisions. L04/L05, D01 and E01 research are DONE; D02 is
withdrawn. E02a–c replacement work is cancelled by owner decision; custom
framing is retained. Next: E03 bounded consolidation. No thousand-line wave is established and no new
domain extraction is dispatched. In parallel,
continue MC-A/MC-B and source-confirmed MC-C/MC-D cases in disjoint files.
Full mobile research is not a global prerequisite for an unrelated behavior-preserving slice. Architect dispatches
one card at a time to a cheap worker and reviews it before dependent work.
