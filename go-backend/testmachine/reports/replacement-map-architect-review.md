# Architect review: replacement requirement mapping Q00

2026-11-13. **Accepted for inventory/mapping only.** This does not verify the
replacement product and does not dispatch Q01 or authorize deployment.

Independent reviewer inspected the canonical capability records, generated
mapping and queue, current source/test anchors, renderer/tests and continuity
docs. Root architect integrated the changes and ran mapping/ledger checks.

## Contract review — accepted

The retained 226 source route sites, literal/dynamic socket wrappers, persisted
models and explicit filesystem/runtime/client obligations are reconciled. Every
record has an owner packet, invariants and state classification. Missing or
sampled assertions remain explicit proof gaps, not passes. Final closure requires
owner-level behavioral decomposition and a second independent inventory audit;
static entrypoint completeness does not imply complete behavioral proof.

Findings corrected before acceptance:

- Historical invariants still required exact Express HTML/header/body length,
  ETag algorithm, transfer encoding, opaque session-cookie implementation and
  JSON member order. Active requirements now state client/persisted semantics;
  old observations remain only historical provenance. Follow-up review caught
  four remaining OIDC/cover invariants; those were corrected and re-reviewed.
- Item-download requirement sub-downloads was routed to library because of a
  stale parent. Q12/item now owns it. Multi-producer events remain accountable
  integration obligations; their producer packets must close their own proofs.
- Capability README declared the register historical/inactive. It now describes
  the active canonical register and archive provenance.
- Source-contract errors discovered during inventory were corrected: actual
  queue wait/dequeue transitions; whole notification settings payload; missing
  backup argument; cache middleware rather than authorization. Q01 must prove
  and repair the queue behavior mismatch rather than codifying current Go output.

Final independent re-review: all reported contract findings closed; Q00 accepted.
Accepted playback lifecycle evidence/differences remain separately scoped.

## Craft review — accepted

One canonical register supplies deterministic CSV/JSON/Markdown views and an
ordered queue; README/AGENTS/ROADMAP/handover link them. Source-site/reference
checks fail closed on new unmatched routes/events, deleted tests and stale views.

The initial renderer test incorrectly required every invariant to remain open.
It now checks faithful canonical status projection, and a synthetic accepted
invariant proves verified evidence survives rendering without granting a global
product claim. Eight mapper regressions pass. No application production code,
dependency or client asset was changed by Q00; no product test was weakened.

## Release boundary

No combined commit/push was performed: this checkout includes pre-existing
uncommitted playback production/tests/evidence and separate harness/status
overlays. The map references current playback tests, so publishing the map alone
before its accepted prerequisite packet would leave broken test references in a
fresh checkout. Review/publish those prerequisite overlays in their own commits
with required rollback/build gates before publishing this mapping packet. Do not
bundle unreviewed harness changes. Current Go sanity checks do not substitute
for that release verification or the remaining requirement proof.
