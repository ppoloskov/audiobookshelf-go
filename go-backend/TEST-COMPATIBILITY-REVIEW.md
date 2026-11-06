# Test, oracle and corpus boundary review

TQ01 · 2026-11-06 · source reviewed at `68e302cf`.

**Historical review:** R01–R08 are now implemented under owner authorization.
See [implementation and actual results](TEST-COMPATIBILITY-IMPLEMENTATION.md)
and [machine-readable assertion mapping](contracts/test-assertions.json).
The text below preserves the original findings, source locations and decisions;
its “no changes/tests run” statements describe the review packet only.

## Decision

Preserve client protocols, functionality and persisted data/rollback. Upstream
TS v2.37.2 is evidence about those contracts, not a requirement to reproduce
Express, JavaScript, Sequelize or ICU implementation details.

This is an extracted review manifest, not a test deletion patch. **One standalone
test is solely an Express fingerprint; eight assertion families below are
candidates for separation.** Most affected tests also protect useful behavior.
No test, corpus input, runtime implementation, CI gate or schema was changed.
RELAX means the listed representation need not be a compatibility requirement;
it does not authorize deleting its surrounding test or changing production code.

Three built-in `gpt-6-luna` workers reviewed (A) source oracles/goldens,
(B) protocol tests, and (C) differential harness/corpus. The architect checked
candidate source, corrected classifications/counts, and added OPML/log/HTML and
persisted-name analysis. External OpenCode could not start due to its log path
being outside the writable sandbox; no source was sent to an external provider.

## Scope and denominators

| Reviewed artifact | Actual scope; counts are not additive coverage |
|---|---|
| `tools/{library,book-item}-oracle.cjs`, their self-tests and Go golden consumers | Library fixture has two records and unrestricted/restricted projections. Book/item fixture has six inputs × three item serialization shapes; typed-media and dual-builder assertions also use those inputs. |
| `internal/textsort/testdata/collation.json` and producer/tests | 112 strings, 12,544 ordered pairs, two expected comparator signs per row. |
| `internal/catalog/testdata/opml-golden.json` | Seven cases: `empty`, `nofeed`, `one`, `minimal`, `special`, `two`, `untitled`. |
| `internal/nameparse/testdata/lastfirst.json` | 52 input/output entries; persisted derivation, retained. |
| Targeted protocol test search | Worker searched 36 test files in httputil/identity/catalog/server/sharing/playback/realtime; selected findings were read directly. This is not a claim of exhaustive assertion review. |
| `tools/parity/cases/core.json` | 168 request descriptors. This is not the total executed suite size. |
| `tools/parity/cases/steps.json` | Four module descriptors, 68 explicit case references; references overlap other inputs and modules also execute flows. |
| `contracts/parity/differential-cases.json` | 122 historical case records under `.cases`; no longer the runner's case source. |
| Comparator/oracle, shape runner and rollback | Source/selected dependencies reviewed; no regeneration or rollback execution. |

## Extracted representation assertions

These eight families are a bounded candidate list, not eight removable tests.
Each replacement must retain an independent expected value and fail on a
plausible protocol, data or security regression.

| ID / decision | Exact test or policy and source location | Extract only this constraint | Retained/replacement evidence |
|---|---|---|---|
| R01 · RELAX/SPLIT | `TestExpressETagMatchesExpress`, `internal/httputil/text_test.go:9`; same-algorithm checks in `TestSendStatusIsExpressSendStatus` (`:28`), `TestSendTextIsExpressSend` (`internal/httputil/sendtext_test.go:21`), `TestWriteJSONWithETagSetsExpressHeaders` (`internal/catalog/cover_test.go:94`), missing/denied item tests (`internal/catalog/handler_test.go:873,892`), and range test (`:1649`) | Exact `W/"length-SHA1"` or `W/"size-mtime"` value. Only `TestExpressETagMatchesExpress` is entirely this fingerprint. | Treat validators as opaque. Replay the observed validator on an unchanged resource and preserve matching/nonmatching conditional behavior, empty 304, auth before 304, HEAD and ranges. Keep body/status/value assertions. A unit vector for a deliberately retained Express helper can remain an implementation test without making its algorithm an API requirement. |
| R02 · SPLIT | `TestWriteJSONFramesLikeExpress`, `internal/httputil/json_test.go:11` | Exact sorted `a,b` member order, `\u003c\u0026\u003e` spelling and hard-coded length `32`. | Decode and assert both keys/types/values (`a == "<&>"`, `b == 1`), status and JSON MIME. If Content-Length is present, compare it to actual encoded bytes. Preserve contextual HTML safety if a caller embeds JSON in HTML. Do not remove length/body consistency checks. |
| R03 · SPLIT | `TestWriteJSONStreamedKeepsEncoderFraming`, `internal/httputil/json_test.go:48` | Exact `{"a":1}\n` serialization, including trailing newline. | Assert one complete JSON document with the right value; reject extra non-whitespace data. Retain HEAD's empty body and the existing Streamed option contract while callers use it. This does not approve S9b option deletion. |
| R04 · SPLIT | `TestMissingClientAssetMatchesExpressFinalhandler`, `internal/server/runtime_client_test.go:62`; range error HTML in `TestMediaFileUsesRangeAndRejectsPathsOutsideLibrary`, `internal/catalog/handler_test.go:1655`; panic envelope in `TestGuardPanicsAnswersLikeExpressFinalhandlerAndLogsStack`, `internal/server/recover_test.go:12` | Exact finalhandler document layout, `<pre>` wrapper, whitespace and Express page wording. The panic test checks a fragment, not a complete byte snapshot. | Keep 404/500/416, empty HEAD, unsatisfied Content-Range, permitted path checks, generic public errors, diagnostic stack logging and token redaction. Preserve CSP/nosniff protections; MIME changes need endpoint evidence. Do not classify security headers as quirks. |
| R05 · SPLIT | `TestDailyFileJSONLineShapeAndName`, `internal/logging/logging_test.go:136,152`; `TestLoggerDataShape` (`:319`); adjacency substring in `TestCrashLogReceivesFatalOnly` (`:287`) | Exact log JSON member order, escape spelling, or adjacent `levelName,level` fields. | Parse JSON lines and assert fields/types/values, level numbers, timestamp format, source attribution and readable files under the existing metadata layout. Keep event delivery/filtering and crash/daily destinations. Console punctuation/debug message text are internal-policy candidates, not permission to change log retention or stored format. |
| R06 · SPLIT | `TestGenerateOPMLMatchesUpstream`, `internal/catalog/library_routes_test.go:20,51`; empty body indentation in `TestLibraryOPMLRoute` (`:82`) | Four-space indentation, attribute order, self-closing style and trailing-newline policy across the seven-case corpus. | Preserve compatible XML Content-Type (the exact current type if consumers require it). Parse XML; assert OPML version, document structure, outlines, decoded attributes, ordering where meaningful, no-feed exclusion and ACL filtering. Preserve original corpus inputs. Decoded-value bugs in `special`/`untitled` belong to I01, not formatting normalization. |
| R07 · SPLIT | `sanitizeGolden` entries at `internal/textclean/golden_test.go:28,31,32,51`, consumed by `TestSanitizeGolden` (`:129`) | Lexically different but equivalent HTML, e.g. `<br />` versus `<br>`, or `&#34;` versus equivalent quote entities. | Compare safe rendered text/element/attribute meaning for these entries. Keep every script/event-handler/unsafe-URL rejection case, escaping of literal `<script>` text, malformed-input safety and allowed markup. Different text, whitespace meaning or unsafe DOM is not equivalent. These tables came from earlier Go implementations too; they are not all TS quirks. |
| R08 · SPLIT | `tools/parity/lib/compare.cjs:535-550`, cases `cover-patch-success` and `cover-patch-repeat` at `tools/parity/cases/core.json:2529,2540` | Reconstructing serialized bytes using `JSON.stringify(parsedBody)` and requiring Express's SHA1 weak ETag and that reconstruction's length. | Keep PATCH status, JSON fields/types/values, image/path and repeat-state behavior. Validate actual wire length if present, rather than reconstructed JSON length. Assert opaque validator semantics where advertised. Capture original wire bytes if a framing check needs them; do not introduce new general tooling merely for this review. |

R01/R08 overlap on the ETag rule but identify different owners (Go tests versus
Node comparator). They must not be counted as separate production-code savings.
R02/R03/R05 are representation constraints; some encode Go choices rather than
literal TS output. The reason to separate them is the external contract boundary.

## Quirks requiring a semantic decision

These are real source quirks or suspicious expectations, but relaxing them can
change values, ordering, settings, authorization or public workflows. No safe
removal is established by this review.

| ID / decision | Exact evidence | Why this is not a formatting-only removal; next proof |
|---|---|---|
| I01 · INVESTIGATE | OPML `special`, `untitled`, `one`; `internal/catalog/testdata/opml-golden.json`; `internal/catalog/library_routes.go:76-108` | Upstream escapes twice, so the special feed URL decodes to a string containing `&amp;` rather than the original `&`; a null title becomes literal `"null"`. `itunesPageUrl`/`itunesPageURL` mismatch suppresses `htmlUrl`, also asserted by `TestLibraryOPMLRoute`. These preserve generator bugs. Propose a correct import/export contract, then test decoded title/URL roundtrip including ampersands and optional links. Web export is real (`client/components/app/BookShelfToolbar.vue:424`), import forwards XML to the server (`client/pages/library/_library/podcast/search.vue:95`). Fixing values must be explicit, not silently normalized in a comparator. |
| I02 · INVESTIGATE | `TestOIDCRedirectURIUsesSubfolderSettingVerbatim`, `internal/identity/oidc_test.go:635`, rows `null` and `unset`; `internal/identity/oidc.go:458` | JavaScript interpolation gives `/null/` and `/undefined/` in provider callback URLs. Valid root/subfolder behavior stays. Define invalid/missing setting handling and check OIDC provider callback registration plus old settings before changing these rows; preserve PKCE/state/session/redirect allowlist defenses. |
| I03 · INVESTIGATE | `TestICUZoneName`, `internal/identity/tz_test.go:16`, e.g. `Asia/Kolkata -> Asia/Calcutta`, `GMT -> +00:00`; producer `tools/icuzones/generate.cjs` | Pins Node/ICU alias spelling. Equivalent IANA names may be acceptable, but the value is client-visible and used with scheduling/settings. Trace those consumers; test correct instant/offset and usable zones before removing the alias table. Keep `TestHostTimeZone`'s nonempty/non-Local requirement. |
| I04 · INVESTIGATE/SPLIT | `TestMatchesNode`, `internal/textsort/textsort_test.go:16`; `TestPlaylistNameOrder` (`:52`); `tools/gen-collation-fixture.cjs` | Exact signs for 12,544 pairs tie the implementation to Node/ICU. Rare punctuation/case ordering may be incidental, but array order, natural sequence order and stable pagination are observable. Define intended title/name/natural-sort/tie rules and test endpoint pagination before isolating version-specific rows. No safe subset of pair rows is identified yet. |
| I05 · INVESTIGATE | `TestNodeBase64DecodeMatchesBufferFrom`, `internal/catalog/collapse_subseries_test.go:52`, especially `Y`, `YW-_`, whitespace and missing-padding rows | Valid encoded filters are client protocol (`client/plugins/init.client.js:170`, `client/components/controls/LibraryFilterSelect.vue`). Malformed Buffer.from permissiveness may be incidental. Trace Unicode/base64url/padding callers and define rejection behavior before pruning malformed rows. Keep valid filter decoding and ACL behavior. |
| I06 · INVESTIGATE | `TestDeniedLoginMatchesPassportWireHeaders`, `internal/identity/identity_test.go:172`; Express text/error tests in identity, emailapi, backup, notifications and userapi | Absent Content-Type and forced `Content-Length: 12`/no chunking reproduce Passport framing. Status/auth failures are required; error payloads can be displayed or parsed. Check each client/error consumer before relaxing MIME, wording or header presence. Byte length must match the emitted body whenever sent. Cookie attributes/clearing and redirect Location remain contracts. |
| I07 · KEEP pending separate boundary work | `TestTruthy/json number zero`, `internal/jsonvalue/value_test.go:44`; settings/device coercion tests in `internal/emailapi/handler_test.go:22,93` | `json.Number("0")` truthiness is a Go representation artifact, not JavaScript number truthiness. Other coercions reach stored settings/device data. Review valid input types and persistence/readback independently before changing shared coercion tests; no blanket truthiness cleanup. |
| I08 · INVESTIGATE | `TestParseUserAgent`, `internal/deviceinfo/ua_test.go:21`, ten rows in `testdata/ua.json`, including synthetic precedence/case cases | Diagnostic parser precedence may be incidental, but device records are stored and displayed. Preserve shape, ordinary device identity and old records; establish intended parsing for malformed/synthetic UAs before changing rows. Reuse from PR05 is not evidence these values are free. |

## Evidence to keep

- **Source oracle integrity.** `tools/test-library-oracle.cjs:12-18`,
  `tools/test-book-item-oracle.cjs:12-17` and `tools/parity/lib/oracle.cjs:7`
  pin source hashes, fixture inputs and reproducibility. They test provenance,
  not Go compatibility, but must stay while the oracle claims pinned evidence.
  Removing the hashes would weaken the diagnostic, not simplify Go contracts.
- **Parsed book/library goldens.** `TestLibrariesMatchPinnedUpstreamGolden`
  (`internal/store/store_test.go:178`) and
  `TestBookItemSerializersMatchPinnedSourceGolden`
  (`internal/catalog/handler_test.go:69`) decode before deep equality; JSON
  member order and whitespace are already ignored. No concrete unnecessary
  response field was proved. Preserve IDs, nullability, relationships,
  settings, paths, durations, offsets, included/excluded audio, ebook data and
  permission filtering. The six inputs are `synthetic fixture book`, `multiple
  audio files and excluded track`, `null description`, `series without sequence`,
  `explicit and abridged`, and `non-null ebook, cover and null duration`. No
  field/value in these cases was proved safe to relax. Whole-object equality
  alone is not evidence of a quirk.
- **Persisted derivations, even quirky ones.** The 52 `nameparse` cases include
  surprising suffix/case/spacing outcomes. `ToLastFirst` writes stored
  `authors.lastFirst` (`internal/catalog/authors.go:485`,
  `internal/catalog/writes.go:653`) and feeds sorting. Do not rewrite those
  expectations as cosmetic name formatting. Preserve DATETIME/JSON/UUID/enum
  formats, metadata meaning and TS reopening Go-written state.
- **Actual protocol semantics.** Keep Lissen's typed JSON contracts in
  `internal/contracttest/mobile_protocol_test.go`: 200 without ETag is allowed;
  advertised validator handling, empty 304 and auth ordering remain. Keep
  status branches, Range/Content-Range/206/416, MIME clients decode, HEAD,
  auth/cookies, redirect targets, Socket.IO event payloads/recipients/order,
  media bytes/format, progress and state transitions. See
  [MC05-S9B-EVIDENCE.md](MC05-S9B-EVIDENCE.md).
- **Independent functional/corpus inputs.** Keep the seeded installation,
  route descriptors, ordered workflows, shape inputs, browser/benchmark inputs
  and rollback dependencies. Shape checks protect required/optional/nullable
  keys and types, not all values/ACL/order/state. RSS parser goldens represent
  previous Go parser behavior as well; they are not automatically TS-only.
- **Already relaxed diagnostics.** `compare.cjs:38,484` sorts object keys;
  `:487-507` reduces ZIPs to sorted name/length/CRC records. Neither is a new
  byte-parity cleanup opportunity. Keep archive entry inventory/content proof;
  other non-JSON bodies are captured as base64 in `differential.cjs:182-188`,
  so error-page bytes can still overconstrain while actual media bytes matter.

## CI and retirement boundary

Observed inconsistency: image workflow `.github/workflows/docker-go.yml:102-105`
sets parity `continue-on-error: true`, and images depend on verify + rollback.
However `.github/workflows/go-backend.yml:78,105-111` still has an ordinary
`upstream-parity` job running equivalence, without `continue-on-error`.
Oracle self-tests also run under `source-and-contracts`. Thus “parity is advisory”
describes image publication, not every workflow result. Repository branch
protection requirements were not inspected. No CI change was made here.

Keep provenance strict in the diagnostic lane. Acceptance should be independent
client/value/state tests plus persistence/rollback. Reconcile the second CI job
explicitly before describing every workflow as advisory; do not remove rollback
or diagnostic inputs to make a gate pass.

[ROADMAP V2a/V2b](ROADMAP.md#v2av2b--extract-reusable-tooling-then-retire-diagnostics-p2-ms)
still owns tooling retirement. Inventory inbound imports and prove standalone
fixture generation, client-usage generation and rollback from a clean checkout
before deleting harness modules. This audit completes neither V2a nor V2b and
does not unblock the pending mobile/device acceptance by itself.

## Follow-through and verification

First useful implementation packet: R02 + R03 + R05, assertion changes only in
their named test files, independently expected decoded values, no runtime or
CI edits. Then R01/R04/R06/R07/R08 as separate caller-aware decisions. Preserve
fixed length correctness, security protections and semantic negatives. Run
affected tests in the documented clean-source snapshot (owner's untracked
`internal/identity/routes.go` remains uncompilable WIP). Changing runtime code,
options or CI needs its own bounded packet; a review candidate is not a DONE fix.

Contract review: worker + architect source/consumer checks; final worker review
of R01–R08's defenses resulted in explicitly retaining XML MIME and clarifying
observed-validator replay. Simplicity review: no runtime/test edits or new
abstraction; one manifest in the existing roadmap, no new framework or ledger.

Verification for this review: direct source/consumer checks, JSON counts via
`jq`, documentation links/anchors and `git diff --check`. No Go/Node runtime
suite, corpus regeneration, rollback, device acceptance or live deployment was
run. Three workers plus architect review cover the named areas, not every
assertion in every test or historical report. No new framework or dependency;
production and test code deltas are zero. Documentation is the only output.
