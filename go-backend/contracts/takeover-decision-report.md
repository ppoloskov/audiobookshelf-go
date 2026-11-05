# Takeover decision report — 2026-11-04

Documentation and bounded audit delivered. Owner subsequently authorized
roadmap implementation and pushing reviewed ready features to the fork. [ROADMAP](../ROADMAP.md) is the active queue. The six-client audit
is explicitly partial, so the original brief's full A/B acceptance is not
claimed. The gap/test plan and task queue define how to close the remainder.

## Five findings

1. **Protect real callers.** yaabsa actively calls the three GET progress/list
   routes labeled unused by the current generator; Absorb also calls bulk
   progress. Keep those routes; external evidence must enter the generator.
   [Pinned callers](mobile-client-audit.md), [25 route decisions](mobile-route-decisions.md).
2. **Protect HTTP meaning.** Lissen's cache consumes ETag/If-None-Match/304.
   Express formatting can change; validator behavior cannot be discarded as
   formatting. [Source evidence](mobile-client-audit.md).
3. **Test the existing Socket.IO implementation.** Four source-checked clients
   explicitly use websocket-only. Three listen for `auth_failed`; yaabsa logs
   `invalid_token`. Existing real JS interop can skip; failure/recovery and
   resource assertions need work. No replacement proposed. [MC-01–03](mobile-compat-gaps.md).
4. **Do not delete shared verification inputs.** Fixture and client-usage
   tools import/read parity modules; V2 must extract and regenerate first.
   Current CI already makes parity advisory and retains rollback as an image
   gate. [Source paths and ordering](../handover.md).
5. **Measure structural simplification.** sqlc is typed-query safety, with
   modest reported line savings. Shared batched item loading/projections is
   the stronger hypothesis; prove query-count/latency benefit. The 30% goal
   lacks a proven deletion inventory. [S13/P2/P3 and metrics](../ROADMAP.md).

## Risks, ordered by impact and evidence

- **P0 confirmed inventory blind spot:** route deletion could break demonstrated
  callers; no deletion performed or approved.
- **P0 coverage gaps:** auth recovery/reconnect and bulk offline persistence
  have insufficient client-specific state assertions; no runtime bug reproduced.
- **P0 hardening investigation:** current WS defaults cap frames at 32 MiB;
  explicit client-compatible frame/total-message bounds and cleanup need proof.
  The reader is not literally unbounded; advertised Engine.IO maxPayload is
  not alone a WebSocket limit requirement.
- **P1 confirmed client cache requirement:** changing ETag/304 semantics can
  degrade client behavior. Full Go endpoint reproduction remains pending.
- **P1 tooling dependency fact:** premature V2 removal breaks regeneration.
  Device, media format/range callers and dynamic-route evidence remain partial.

## Decisions and recommended defaults

- Domain-migration follow-up: explicit books/items/podcasts ownership and
  small book-read/batch/list cards were followed by the owner cleanup checkpoint, accompanied by relevant
  compatibility tests. No full mobile-audit freeze, schema change or live deployment.
- Make 30% reduction aspirational; Q1's original soft-review proposal is
  superseded by the explicit budget/acceptance/checkpoint policy (2026-11-05),
  retaining correctness/dependency/dbnest gates. Ratchet code still strict today.
- Provision the existing `socket.io-client@4.8.1` JS probe in required CI.
  Current advisory CI already installs it; the older Go v3 client approval is
  separate. No new runtime library requested.
- Keep unknown routes/features pending evidence. Route removals need individual
  R2 decisions; O4 devices and any live cutover need separate owner involvement.
- Keep boot ANALYZE/deletion cleanup and new runtime libraries deferred (O1/2/5).
- External Flash delegation was automatically rejected for private-code export;
  built-in cheap workers were used. No external-provider permission assumed.

## Implementation follow-up

B01–B09 book read/projection outcomes and I01 query instrumentation are now
implemented/reviewed and integrated into `master` on 2026-11-05; individual card checks
and actual verification are recorded in ROADMAP/handover. The stateless reader
object was removed; use plain `books.Load(ctx, db, id)`.

Actual outcome: production lines −1, complexity −11, maps −2. No thousands-of-
lines claim. I02 LoadMany, I03 prefix snapshots and I04 ordinary book pages are now
checked DONE; I05 shelves and I06 item rows are now also reviewed DONE, with
old scanner/classifier/scalar+shelf SQL removed. Shelf helper reads 51/251 ->
6/6; this wave costs +165 production lines / +23 complexity, explicitly
reviewed. L01–L03 list baseline/read extraction are accepted: old read bodies,
entry copy and visibility counter removed; +29 production lines, complexity
unchanged, two thin adapters retained for existing route/write/event callers.
RO02/S9a/PR01–PR04/PR07 now delete 158 production lines and 46 complexity points;
the playlist forwarder is gone. Strict baselines were lowered, not raised.
An isolated signed-source FFmpeg/libwebp build makes the full local clean-source
suite pass. Restricted HTTP remains active and is retained; its prefix copies
are deleted. collectionDTO's per-user ACL adaptation still has real callers.
Conditional PR06/PR05 and validator-sensitive S9b remain pending before broader
cleanup; L04 still scopes future writes. Q1 now requires explicit growth review.
All device/route-removal decisions stay
open. Initial acceptance had five local WebP encoder failures; the isolated
native FFmpeg fix now makes the integrated full suite pass. Build/vet/lint/
ratchet/dbnest/arm64 and affected package tests also pass.

## Changes and verification

Updated root/backend README/agent/handover navigation, permanent architecture,
ROADMAP, shared compatibility contract, realtime/parity/contract READMEs;
added client evidence/audit, gap/test plan, route decisions and Go skill review.
Original input documents and prior roadmap are preserved verbatim under
`archive/reviews/`. Inherited TODO only gained a historical banner; unrelated
codemap/runtime route work was preserved. No code/CI/generated inventory changed.

`go test ./tools/clientusage` passed with local toolchain/task cache; provenance
hashes match current route/trial inputs. Source/test review and documentation
checks are recorded in [handover](../handover.md). Full runtime, rollback,
benchmark and device suites were not run for this prose-only change.

Remaining audit limitations: selected files, not exhaustive operation coverage;
Socket.IO package-version/lockfile reconciliation; AudioBooth actual media
range path; six-client HLS/download/cover formats; full auth-refresh/version
branches; unresolved dynamic registrations; all real-device acceptance.

2026-11-05 follow-up: each B01–B09 and I01–I04 card has an explicit checked
DONE status. The page regression proves 43/203 -> 7/7 reads for 10/50 actual
returned books; feature source cost is +139 lines / +31 complexity / +2 []any,
explicitly reviewed and baselined. No new runtime dependency or guard removal.
