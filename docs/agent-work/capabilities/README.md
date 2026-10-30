# Capability parity records

Generated per-capability parity ledger for the TypeScript-to-Go rewrite.
One record per route candidate from `go-backend/contracts/parity/route-coverage.json`
(regenerate with `python3 go-backend/tools/parity/build_route_coverage.py`, then
`python3 go-backend/tools/parity/build_capabilities.py`; `--out DIR` targets another
directory — tests always regenerate into temporary directories, never this one),
plus non-HTTP subsystem
records listed in the generator. Regeneration preserves reviewed content: curated
evidence, exceptions, invariants, verified cases, state scenarios, stateful
classification, status, contract, tests, implementation and source references are
merged; a malformed record is refused, never overwritten
(`go-backend/tools/parity/test_build_capabilities.py` is the regression).

## Status vocabulary (go-scope-ledger check-ledger.go)

Capability `status` values are restricted by the ledger validator:
`unverified`, `characterized`, `implemented`, `verified`, `blocked`.

Route-coverage status `implemented_not_differentially_verified` maps to capability
status `implemented` (implemented but unverified). Route-coverage `not_implemented`
maps to `unverified`. `characterized` is used when source behaviour was analysed
but no Go implementation exists yet. `verified` additionally requires checked
normal/error/edge cases with real test and evidence references and is deliberately
not assigned automatically; the per-route differential runs recorded in
`contracts/parity/*.json` sample only a subset of variants.

## Historical provenance

Work before 2026-10-29 predates the go skill pack ledger format. Route-coverage
and differential reports under `go-backend/contracts/parity/` are the inherited
evidence; this directory is the active ledger. Task ledger records created
2026-10-29 pinned skill pack version 1.1.0. From 2026-10-30 the installed pack
is 1.3.0: new records (e.g. the upgraded `cap-sub-scanner-watcher.json`) carry
`invariants`/`stateful`/`state_scenarios` per the 1.3 format; historical
records keep their original provenance and are not relabelled.