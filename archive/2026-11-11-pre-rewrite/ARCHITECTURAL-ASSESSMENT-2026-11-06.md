# Architectural simplification assessment — 2026-11-06

## Candidates reviewed

| Area | Evidence and comparison | Estimated net deletion / decision |
|---|---|---|
| Catalog author/series matching | Go `quickmatch.go` repeated the relation walk, insert/link, sequence update and unlink for authors and series. Monstrobook has a combined relation mutation routine, but retains separate scanner mutation and does not prove the same event/cache behavior. | Selected bounded consolidation: remove 35 production lines, complexity −8. Preserve author/series SQL and event branches. |
| Cross-entrypoint author/series writes | Catalog PATCH uses trimmed names and SQLite NOCASE; quickmatch uses Unicode `EqualFold` without trimming; scanner uses exact lookup. Their event, cache, timestamp and ordering rules also differ. | No safe unified owner identified; do not consolidate these paths. |
| Item projections and persistence | Item maps span catalog, items, books, wire and playback. Monstrobook's typed browse projection still duplicates playback loading. Persisted JSON decoding is already centralized and tested for malformed/null/heterogeneous values. | No demonstrated net deletion. Typed rewrite risks nullability, omitted fields, podcast shapes and rollback meaning. |
| Browse, collapse and personalized shelves | About 1,873 lines across distinct paging, ACL, collapse and per-user shelf policies. Monstrobook's smaller implementation omits or differs from several policies. | No redundant ≥1,000-line subset established. Keep policies; investigate batched eligible-ID loading separately if query measurements justify it. |
| Read-only and full runtime setup | Runtime shell differs by about 1,177 lines, but full runtime owns service lifecycle and restore reconstruction absent from read-only mode and Monstrobook. | No large removal case. Share only pure probes if a separately measured benefit appears. |

No source-backed candidate supports a ≥1,000-line net reduction; the 3,000-line first-wave objective is not established by this assessment. The selected change is a bounded simplification, not completion of the reinvention objective. No performance gain is claimed or measured.

## Implemented slice and evidence

`quickMatchAuthors` and `quickMatchSeries` now share one relation loop with explicit author/series persistence and event branches. Route-specific PATCH and scanner behavior remains separate. Tests cover empty and duplicate relations, existing-entity linking, and null/empty/numeric series sequences.

On the clean tracked-source baseline, metrics changed from 44,674 lines / 10,809 complexity / 178 high-complexity functions to 44,639 / 10,801 / 177. Maps (990) and `[]any` (326) were unchanged. New regression tests pass against both the original implementation and the changed implementation. The focused match tests pass. The full catalog suite could not bind `[::1]:0` in the sandbox during `TestAuthorImageUploadAndDelete`; this is an environment limitation, not a passing suite. No benchmark was run.
