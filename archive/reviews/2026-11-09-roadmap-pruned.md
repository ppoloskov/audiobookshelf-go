# Roadmap cards removed on 2026-11-09

The 1,368-line ROADMAP.md was reduced to open work only. Full text is in git
history (parent of the pruning commit, 2b61551) and the older
[pre-takeover roadmap](2026-11-04-roadmap-before-takeover.md). Evidence below is
a commit or a doc; not re-verified by running suites.

## DONE

- TQ01, TQ01-I (R01-R08, manifest, final suites): fda317d, 8e17183; [TEST-COMPATIBILITY-IMPLEMENTATION.md](../../TEST-COMPATIBILITY-IMPLEMENTATION.md).
- D01 deletion inventory; E01, E01a-d audit: [2026-11-05-deletion-feasibility.md](2026-11-05-deletion-feasibility.md), DELETION-FEASIBILITY.md, 2fb4cde (no 1,000-line wave found).
- RO02 (base-prefix methods), FF01 (native WebP ffmpeg, see TESTING.md): c4cf90a era, 24d199e.
- G01 master consolidation; G02 pruning review ([PRUNING-REVIEW.md](../../PRUNING-REVIEW.md)).
- PR01-PR07 and the cleanup checkpoint (-158, then -74 lines): 24d199e, e6a39e8.
- B01-B09 books projection/reads, I01-I06 batching/query counter, L01-L05 and L04a-d lists ownership: d2ec8d9, 4d34d49, f466e36, 8211031; books/items packages later deleted in a7f4e13.
- S9a ordered-key scaffolding: c4cf90a era, then ADR-002 sweep 9d29248.
- MC-05, MC-05/F1, S9b-R1 Lissen typed-JSON evidence: [MC05-S9B-EVIDENCE.md](../../MC05-S9B-EVIDENCE.md); `TestMobileConditionalGET` / `TestMobileCoverParameters` in internal/contracttest/mobile_protocol_test.go.
- Q1 growth-review policy: adopted, now in AGENTS.md ("Verification").
- V1 (parity advisory): AGENTS.md. F1-F9, L1-L7, S2 (cf7135d), C5, C24-C26: completed per old roadmap.
- E03a/b/c (error pair helper, coercion, nil emitter): superseded by 05018a9 (ADR-001 M2: httputil.Fail, ~20 write helpers deleted) and 60e7eb7 (events.Emitter nil-safe, wave C4).
- S1/S3/S4 routing migration: 8512b18 (ADR-001 M1, hand routers deleted).
- C12 -> S6 permission equivalence: 111fef8 (internal/access), c729d1d (PERM2).
- S5/S7/S8 constructors and UA parser: 60e7eb7 (Set* 17 -> 4), PR05.
- P1 settings cache: internal/serversettings (111fef8). P5 filter-data updates: F11 (internal/filterdata).
- S9 framing options: httputil Streamed/WithoutContentLength/WithoutETag removed in 9d29248; ContentType/ForRequest replaced by httputil.Fail and conditional JSON (022dffc).
- C1 typed item model: a7f4e13 (internal/libitem) with itemwrite 45e099e.

## OBSOLETE

- D02 / D02a-e feature-removal wave: withdrawn by owner (functionality preserved).
- E02a-c Socket.IO library replacement: cancelled by owner; custom framing retained.
- RO01 read-only HTTP audit: read-only mode removed (F21, d8ab294).
- S9b deletion of framing options: dissolved by ADR-002 (9d29248); nothing left to decide.
- S9 / S9b KEEP rows, "Scope still open" (S13/P2/P3 books/items epic), S10/S12/S14/S15 domain migration order: replaced by libitem, itemwrite and the refactor waves A-F (docs/REFACTOR-LOG.md).
- L8a-c selective sqlc per domain: replaced by opportunistic sqlc use (identity, users, sessions, playback, sharing, progress, lists); no conversion quota.
- S16 map reduction: no quota; `map[string]any` 990 -> 464 via waves; tracked by ratchet only.
- MC-B/R1 `cmd/abs-compat`-era route census wording: abs-compat and internal/compat deleted (F16).
- 2026-11-05 priority table, "Execution conventions", dispatch policy for B/I/L cards, takeover validation paragraph, decisions 1, 2 and 6 (answered or stale).
