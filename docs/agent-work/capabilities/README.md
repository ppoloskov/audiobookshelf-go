# Canonical replacement requirement register

Acceptance now runs through `make verify`: one Go-first/.mjs machine, upstream
proof before Go replacement assessment. Shared assertion bindings never replace
this requirement register or automatically verify its broad invariants.

These JSON records are the active requirement register for the two fixed
v2.37.2 contracts in [CONTRACT.md](../CONTRACT.md), reconciled on 2026-11-13.
The pre-reconciliation records are preserved in
[archive](../archive/capabilities-pre-20261113-contract-map/).

Generated [requirement-to-test views](../../../go-backend/docs/verification/replacement-requirement-map.md)
and the [ordered owner queue](../../../go-backend/docs/verification/replacement-gap-queue.md)
come from these records through
`node go-backend/tools/replacement-contract-map.mjs` (repository root).
Use `--check` to detect stale views. Do not run the archived capability generator
over the current register.

Each source operation/event/format/lifecycle obligation has a stable ID, contract,
source and implementation references, tests, invariants and normal/error/edge
plus repeat-state proof status. A test reference is an assertion lead until its
exact coverage is reviewed and a current run is recorded. `implemented` does not
mean verified; historical evidence never upgrades a rewritten implementation.

The validator accepts `unverified`, `characterized`, `implemented`, `verified`
and `blocked`. Only reviewed, reproducible current evidence can set `verified`.
Refine sampled invariants and add newly discovered obligations within the fixed
contract; never freeze the denominator to obtain closure. Express representation,
byte order and internal algorithms are free unless a real client or stored value
requires them. Historical byte-parity details may remain in provenance exceptions.

One bounded owner packet per invocation, architect review between packets. Keep
current task and accepted evidence in [tasks](../tasks/); final closure also needs
an independent completeness audit and the strict invariant ledger check. The
mapping packet itself performs no product verification.
