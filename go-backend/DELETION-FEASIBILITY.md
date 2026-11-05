# Behavior-preserving simplification review

Reviewed 2026-11-06. E01 source baseline: runtime `82110311`, documentation
`d1b67033`, canonical `master`. Handwritten Go baseline: **44,674 lines,
10,809 complexity**. Three cheap workers researched mechanics, data paths and
protocol/lifecycle alternatives; architect reviewed leading source bodies,
replacement costs, overlaps and contract limits. This is source research,
not implemented or experimentally verified savings. No runtime tests were run.

**Result: no credible 1,000-line wave or thousands-line deletion portfolio is
established.** A–D total 174–184 estimated net lines. F–H add 70–126
conditionally, for 244–310 across those candidates if each proof succeeds.
These estimates are not delivered reductions. The owner retains custom
Engine.IO/Socket.IO framing and cancelled library replacement. Behavior-preserving
transport fixes and contract tests remain in scope.

All implemented functionality remains: backups/restore, manual audio tools,
providers/matching, ebooks, read-only mode and existing client operations.
D02 was withdrawn and its implementation discarded before publication.
The [historical D01 inventory](archive/reviews/2026-11-05-deletion-feasibility.md)
records 4,607 active-feature footprint lines, **zero approved feature cuts**.
It is not the live queue. [ROADMAP.md](ROADMAP.md) owns task status.

## Counting and evidence

Counts are physical handwritten production Go lines, including comments and
blank lines within a replaced span. Gross means old lines removed; replacement
includes new helpers, callers, imports, adapters and retained target bodies.
A smaller handwritten metric achieved through SQL/templates/generation still
needs a full source-cost review; moving complexity is not a saving. Tests and
JSON snapshots are separate and never credited to production Go reduction.

Exact mechanics occurrences are preserved in the
[148-pair edit manifest](archive/reviews/2026-11-06-e01-error-http-candidates.md) and
[17-guard edit manifest](archive/reviews/2026-11-06-e01-emitter-deletions.md).
The broader census manifests include excluded sites and are not edit scopes.
Source paths below are relative to `go-backend/`. Refresh counts after edits.

## Ranked portfolio

| ID / classification | Exact old surface and smaller design | Gross / replacement / estimated net | Evidence and retained behavior |
|---|---|---|---|
| A — merge duplicate mechanics | 148 adjacent `logging.RequestError` / `http.Error` pairs across the manifest; one shared logging+response function, without moving handler ownership. Existing lists helper and scanner `err.Error()` response are excluded: the latter would evaluate before logging in a combined call. | **296 old** → 148 call lines + conservative 10–20 helper/refactor lines; **128–138 net**. Original worker helper allowance was increased during review. | Same status, plain HTTP error response/MIME/newline/nosniff, separate log/response strings, query-token redaction and useful handler source attribution. Returns and transaction order stay at callers. Not yet characterized for the new helper. |
| B — simplify existing event boundary | Only the **17 standalone** three-line nil guards whose payload/recipient/event are precomputed identifiers/literals; add a nil-safe method on existing `events.Emitter`. | **51 old** → 17 method calls + about 9 method/import/type cost; **~25 net**. | Manifest lists 37 eligible-looking sites; 15 mixed-condition blocks and five nested lazy-projection guards are **not** in this saving or edit scope. Keep all lazy payload work and scanner conditional buffer resets. No default no-op constructor or event framework. |
| C — proven duplicate implementation | `catalog/handler.go:2451–2464`, `stringValues`, equals existing `jsonvalue.Strings`; replace its one caller and delete local body. | **14 old**, caller line-neutral + 1 import; **13 net**. | Preserve typed-slice aliasing, nil/empty, mixed `[]any` filtering and empty strings. Source equivalence verified; replacement not executed. |
| D — merge duplicate loops | `userapi/me_extra.go:437–451`, `roundTimes`/`roundMonths`, combined function bodies 14 lines (15-line inclusive span); one map-key-generic rounding function. | **15 old span** → ~7 including separator; **~8 net**. | Same fresh result maps, key types and `math.Round`; input maps unchanged; no route change. Low priority. |
| E — KEEP by owner decision | Retain custom Engine.IO/Socket.IO framing in `internal/realtime`; the library-replacement initiative is cancelled. | **0 savings credited.** | Ordinary protocol fixes and contract tests remain in scope. C25's prior auth-before-ACK race is retained as brief historical rationale below; no replacement proof, dependency trial or framing deletion is planned. |
| F — reuse existing book data | `playback/playback.go:413–503`, `loadBook` (91 lines); reuse `books.Load` plus explicit playback conversion. | **91 old** → 30–45 estimated conversion/caller cost; **46–61 net**, conditional. | Excluded audio, duration/track order, malformed stored JSON behavior and caller accumulators differ from ordinary book projections. Not an exact clone; add projection/error/order proof before replacement. Repeated from D01, counted only once here. |
| G — batch association algorithm | `catalog/authors.go:314–389`, `authorSeriesGroups` (76); load ordered memberships and item data in batches. | **76 old** → 48–64 estimated; **12–28 net**, conditional. | Also eliminates per-item query orchestration: preserve duplicate input IDs, createdAt/rowid order, first-seen groups and stable sequence sorting. `TestAuthorFindOneItemsAndSeries` exists; query-count/duplicate/order proof is missing. |
| H — reuse sidecar hydration | `scanner/metadata.go:89–195`, `WriteMetadata` (107); reuse book loader and direct sidecar projection. | **107 old** → 70–95 estimated; **12–37 net**, conditional. | Generic `WriteItemMetadata` has another live scanner caller and stays. Preserve podcast path, NULL/default/malformed values, author/series order, sidecar settings/path bookkeeping and metadata.json meaning. |
| I — small HTTP mechanisms | Status assembly: `server/server.go:82–134`, `runtime.go:458–496`. Local-file serving: playback `serveMedia:1906–1926`, sharing `serveMedia:181–191`. | Status **35–45 target / 25–35 replacement / 0–20 net**. Media **29–35 target / 20–28 replacement / ~5–15 net**. Planning envelopes; no combined wave credit. | Read-only initialization/auth/HTML policy differs; never instantiate full Runtime in read-only mode. ServeFile/ServeContent differ in redirects, stat/MIME/error/cache/HEAD/range handling. Tests missing for shared replacement. |

A–D form a nonoverlapping **174–184 estimated net**, not a substantive
thousand-line wave. F–H add **70–126 conditional net**; A–D plus F–H total
**244–310 conditional net**. This is an investigation ceiling, not an accepted
wave or lower-bound guarantee. Helper overlap must be reconciled if runtime
implementations proceed. E is KEEP and contributes zero; I is excluded.
No measured complexity reduction is claimed before implementation.

## Larger designs examined without a deletion case

- **Metadata patching:** `catalog/writes.go:216–281,848–971` spans 190 lines.
  A typed presence-aware patch still needs absent/null/coercion semantics,
  sanitization, derived titles, association sync and transaction/error/event
  handling. Shared `storedRow`, `rowColumns` and `requestString` have other
  live consumers. Replacement plus adapters is roughly 165–220 lines; not
  a major saving. No reflection/descriptor framework recommended.
- **Series collapse:** three algorithm regions total 609 lines, but encode
  different ACL/filter/order/count/pagination policies. An ordered membership
  snapshot can target `subseriesBooks:247–319` (73), not all 609. Replacement
  55–90 gives net −17 to +18. Query-count improvements may be worthwhile as
  performance work, with measurements; they are not thousand-line deletions.
- **Wire/projection sharing:** common fields might eliminate only 15–25
  declarations/assignments before adapters. JSON **key order is free** under
  ARCHITECTURE; old byte goldens are not a reason to retain redundant code.
  Field presence/types/nullability and collision-free flattening remain fixed.
  No large simplification has been demonstrated; do not add a universal DTO.
- **SQL JSON projection/generation:** query clauses, null/order rules, models,
  adapters and maintenance remain real code. No verified smaller replacement.
- **Transactions:** 32 production BeginTx / 30 Commit sites (excluding fixture
  support) have endpoint-specific early returns, file cleanup and post-commit
  work. A callback adds about 8–15 lines/site against 7–12 boilerplate lines;
  no net deletion established. Keep transaction ownership local.
- **Whole ServeMux conversion:** roughly 240–320 matcher lines replaced by
  260–380 registrations/wrappers. No net case, and method/HEAD/auth precedence
  differs. Earlier lists migration's −29 illustrates why moves are insufficient.
- **Read-only server elimination:** unsafe; identity initialization may write a
  missing JWT secret and podcast construction starts schedules. Retain the
  read-only runtime and its explicit unsupported-operation behavior.
- **Lifecycle/restore unification:** service close ownership, process cancellation
  and backup rollback are different obligations. No generic supervisor justified.
- **Microhelpers:** filter-cache duplicates ~5 net and sorting prefix ~4–6 net
  excluded from the wave. Truthiness and permission snippets have different
  caller policies; no blanket unification. Metadata source precedence differs
  for explicit empty values; no generic merge. Keep OS-specific stat semantics.

## Retained custom framing decision

C25 commit `1ddb115a` records why the previous `github.com/zishang520/socket.io/v2`
integration was removed: CONNECT was acknowledged before application auth
listeners were attached, and 51/200 sequential connections failed. The current
in-repo Engine.IO/Socket.IO framing fixed that ordering and is retained by owner
decision. E02a, E02b and E02c are **CANCELLED**, not completed or failed research.
No library replacement, adapter-costing exercise, dependency trial or framing
deletion is planned. This decision does not bar ordinary transport fixes or
protocol contract tests that preserve current behavior.

## Next dispatch and acceptance

The next priority is E03's bounded behavior-preserving cleanup. Do not fill time
with tiny extractions or announce a thousand-line implementation wave. A–D
remain candidates subject to their named proof and growth gates; F–H are
conditional hypotheses and must be independently measured.

For any selected implementation: exact old blocks deleted in the same packet,
new helper/caller costs measured, no baseline increase, affected behavior tests
then AGENTS integration gates, cold-start docs and DONE cards in the same
commit. Preserve inherited owner WIP. This audit changed documentation only;
`git diff --check`, local links, source/manifest counts and contract review are
the checks, with runtime suites explicitly unrun.
