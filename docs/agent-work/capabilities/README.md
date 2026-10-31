# Capability parity records

Generated per-capability parity ledger for the TypeScript-to-Go rewrite.
One record per route candidate from `go-backend/contracts/parity/route-coverage.json`,
plus non-HTTP subsystem records. The route inventory remains regenerable with
`python3 go-backend/tools/parity/build_route_coverage.py`; the former capability
ledger generator and its regression test are archived under
`docs/agent-work/archive/parity-tools/` and are not part of the active gate.

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
evidence; this directory is retained as dated capability evidence, not a gate. Task ledger records created
2026-10-29 pinned skill pack version 1.1.0. From 2026-10-30 the installed pack
is 1.3.0: new records (e.g. the upgraded `cap-sub-scanner-watcher.json`) carry
`invariants`/`stateful`/`state_scenarios` per the 1.3 format; historical
records keep their original provenance and are not relabelled.
