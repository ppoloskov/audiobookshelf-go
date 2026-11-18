# Independent packet review brief

You review one packet of [packets.json](packets.json). You did not write it.
Your result is the receipt
`go-backend/contracts/verification-review/<packet>.json`, the only file you
write. You do not edit scenarios, requirement records or the harness.

The question: do the packet's scenarios prove every in-contract behaviour of
its requirements on pinned upstream v2.37.2, with meaningful assertions?
Contract and exclusions: "What is contract" in [OWNERS.md](OWNERS.md).

## Order of work (the order makes the review independent)

1. **Obligations first, without opening the scenarios.** For each requirement
   id of the packet read its record (`docs/agent-work/capabilities/cap-<id>.json`)
   only for the route/event it names, then read the upstream source path
   (router → controller → managers/models) and the client callers (`client/`,
   `go-backend/contracts/clients/*.md`, `go-backend/contracts/client-usage.json`).
   Write down the obligations: each authorisation rule, deliberate status,
   response field a client reads, persisted effect, file effect, event and
   audience, repeat/changed/force/failure behaviour, and edge variant.
2. **Then read the packet**: `tests/replacement/<packet>/*.go` and the
   records' invariants and facets. For each obligation find the scenario that
   proves it. For each scenario check that its code asserts its whole `Claim`
   (values, rows, events, files; not only a status or key presence), that its
   `Sources` are right, that it cannot pass vacuously (empty loops, unchecked
   errors, conditions that never run), that it has no sleeps, skips or
   target-dependent branches, and that it asserts nothing outside the
   contract (error text, key order, accidental 500s).
3. **Facets marked not applicable**: confirm each reason against the source.
4. **Run it**: `make verify-packet PACKET=<packet>` must pass (environment in
   OWNERS.md "Running"). Then run it with `REPLACEMENT_V8` and
   `go-backend/tools/verify-coverage.mjs` filtered to the packet's handlers
   (OWNERS.md "Completeness check"): every unexecuted function or branch in
   those handlers is either an obligation without a scenario (a finding) or
   justified (unreachable, exclusion).
5. **Probe one or two scenarios for strength**: temporarily change an
   expected value in a copy or reason about what wrong server behaviour would
   still pass. A scenario that would pass against a wrong implementation is a
   finding. Leave the tree unchanged.

## Receipt

```json
{
  "packet": "<packet>",
  "reviewer": "independent review, <date>, <model>",
  "scenario_digest": "...",
  "requirements_digest": "...",
  "required_tuples": [
    {"requirement": "route-r000", "invariant": "INV-…", "case": "error", "state": "initial",
     "assertion": "<scenario assertion that proves it>",
     "sources": ["server/Auth.js:322"], "rationale": "the obligation, in one sentence"}
  ],
  "not_applicable": [{"requirement": "route-r000", "facet": "force", "reason": "confirmed: …"}],
  "open_findings": ["<requirement>: <what is missing or wrong, precise enough to fix>"]
}
```

- Digests: `cd go-backend && go run ./tools/verify -node "$(mise exec node@22 -- node -p process.execPath)" -digest <packet>`,
  taken after your last look at the files. The scenario digest binds packet code, shared harness, fixture, module files, client audits, owner contracts and the upstream pin.
- `required_tuples` lists every obligation that a scenario proves, one tuple
  per scenario (a tuple names exactly one scenario; every scenario you
  accept appears once). `sources` are upstream or client paths.
- An obligation without an adequate scenario, a weak or vacuous scenario, a
  wrong not-applicable reason, or an out-of-contract assertion goes to
  `open_findings`. Do not list a scenario you do not accept in
  `required_tuples`.
- The gate accepts the packet only with an empty `open_findings`, matching
  digests, and every declared scenario listed. Never empty the findings to
  make the gate pass.

When the owner has fixed findings you are asked again: re-check the changed
files, update tuples, findings and digests.

## Report

Under 300 words: obligations counted, scenarios accepted, findings (numbered,
one line each), not-applicable facets confirmed or rejected, commands run.
