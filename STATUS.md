# Monstrobook historical pre-oracle verification status

Historical snapshot from 2026-11-12 (the retired pre-oracle `run-local.cjs` runner).
Superseded by the **Twin-Server Differential Oracle** (`make verify`; see
[testmachine/oracle/README.md](testmachine/oracle/README.md) and
[handover.md](handover.md)).

Current recorded snapshot result (2026-11-12): FAIL

Recorded run: 2026-11-12T21:47:18.872Z

Pinned upstream: `edb1f7efc92ecacdef51aafb4d9d5054ce10604f`

| Measure          | Count |
|------------------|-------|
| Declared/results | 193   |
| Executed         | 193   |
| Passed           | 192   |
| Failed           | 1     |
| Skipped          | 0     |

Failed state checks: none recorded.
Report equivalence: not passed.

Broader operational suites, browser/mobile acceptance, restore acceptance and
unexercised variants are not implied by this historical snapshot. See
[ROADMAP.md](ROADMAP.md) for current verification status and
[handover.md](handover.md) for the latest oracle scorecard and remaining gaps.
