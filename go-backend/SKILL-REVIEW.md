# Go skill review — 2026-11-04

Scope: reviewed the installed `~/.agents/skills/go-*/SKILL.md` entrypoints
(19 skills), `~/.codex/skills/go-development/SKILL.md`, and the architect loop's
worker profile. Supporting references were not exhaustively audited. Central
skill files were not edited. This repository's owner instructions govern
scope and override conflicting generic workflow guidance.

## Conflicts and proportional application

| Skill / exact rule | Assessment for this repository | Local resolution |
|---|---|---|
| `go-skill-router` §3/6: create version-pinned ledger, validate before done; `go-scope-ledger`: canonical task files and capability JSON | Too much duplicate tracking; this repo deliberately archived these records | Maintain the existing ROADMAP and handover; keep historical provenance. No parallel ledger migration or validator gate for this documentation packet. |
| `go-characterization-parity` steps 2–5: comprehensive capability JSON, differential loop, every in-scope variant verified | Useful methods for risky operations, but its full parity programme conflicts with client-contract scope and advisory byte differential | Apply behavior/state characterization to named client/persistence obligations; do not reinstate exhaustive upstream/byte parity or expand implementation beyond the owner-authorized roadmap slices. |
| `go-verification-gates` G3/G4 + final ledger gate | Good honesty/risk principles; whole-module checks and universal ledger closure are excessive for prose-only edits | Document-only: links/consistency/diff checks. Code: targeted tests then existing CI gates; reviews scaled to actual risk. No claims of skipped scans passing. |
| `go-security-supply-chain`: scanners for every substantive change; unavailable scanner blocks | Security checks are valuable, but “substantive” can sweep in unrelated documentation/refactors | Use focused security review and scans on affected exposure/dependency changes. Keep unrelated baseline findings explicit; no blanket new release gate for docs. |
| `go-development`: generated refreshes, `go mod tidy` require explicit approval | Conflicts if interpreted as asking again for the already-approved sqlc workflow | `make sqlc` is authorized within an approved query task; no repeated approval. New dependencies, unrelated module churn, schema changes remain out of scope. |
| `go-config-persistence`: sqlc is an exception; migration guidance | sqlc is already owner approved; migrations violate this project's fixed boundary | Keep sqlc for query safety, no repeated library debate; do not introduce schema migration work. |
| `go-public-api`: preserve exported Go APIs by default | Too broad if applied to repo-internal interfaces/package names | Fixed external protocols/data remain protected; internal APIs can be redesigned with callers, tests and coherent ownership. |
| `go-native-architecture`: mandatory review for new package/layer/dependency | Fits architect role, but should not create an ADR per small package | A short alternatives/consumer note in the task suffices for small changes; owner approval still applies to new libraries. |
| `go-naming-docs`: “Aggressively improve” adjacent names | Can encourage unrelated changes and token use | Fix misleading docs and touched names; no incidental rename campaign. |
| `go-observability-performance`: profile before algorithm changes | Useful evidence standard; not a reason to delay obvious measured N+1 repair | Establish query-count baseline and before/after benchmark for S13/P2/P3, without requiring a new profiling framework. |
| Router/ledger/work-packets: two unsuccessful repairs => block/escalate | Useful worker budget bound, not a limit on architect investigation or whole project | Worker returns evidence after two repairs; architect chooses a different approach; continue independent authorized tasks. |
| `architect-dev-loop/references/developer-agent.md`: default `gpt-5.3-codex` | Superseded by owner's explicit cheaper-worker instruction | Prefer permitted OpenCode Flash; built-in cheap worker fallback. Architect retains planning/review. |

## Keep

`go-scope-contract`, `go-boundaries-validation`, `go-behaviour-tests`,
`go-errors-context`, `go-concurrency-lifecycle`, `go-modernity-release`,
`go-stdlib-first` and `go-simplify-review` largely fit: concrete Go, meaningful
state/value tests, errors at boundaries, owned lifetimes, approved libraries,
and evidence before deletion. Load specialists on demand. Existing Go 1.27 /
toolchain 1.27.3 is recorded from go.mod, not a fresh claim about the world's
latest release. No toolchain upgrade is proposed.

No security safeguard is waived. A scanner required for a particular security
change must still run or leave that change unverified. Protocol codec risk
belongs in the Socket.IO work, not a speculative subsystem replacement.
