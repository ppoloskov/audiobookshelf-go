# Architectural simplification: ADRs and ledger

Production lines are counted by `go run ./tools/ratchet` (handwritten,
non-test, non-generated Go). Moved lines are not counted as deleted. Test,
fixture, documentation and generated-code changes are excluded.

## ADR-001: one HTTP handler shape, routes on the mux (2026-11-06)

**Context.** Handlers write their own responses and return nothing. A
failure costs 3–4 lines (`logging.RequestError`, `httputil.WriteError`,
`return`), repeated ~450 times for 5xx and ~700 times for 4xx answers.
Eight feature packages are mounted as `Handle(w, r, user) bool` chains,
each a hand-written path router (`strings.Split` plus `len(parts)` checks);
the catalog one alone is ~300 lines. The runtime names every package and
answers an unmatched `/api` route with a non-TS 501 JSON body.

**Options compared.**
- *Ours:* keep the Handle chains and per-site error writes. Rejected: it
  is the bulk of the duplication.
- *Monstrobook:* handlers return `error`, `httpx.Error` maps errors, and
  routes are registered as method patterns on `http.ServeMux`. It gets
  there by dropping Express framing (no ETags, uniform JSON errors),
  which our clients observe.
- *Chosen (third):* Monstrobook's shape plus the framing contract.
  `httputil.Handler func(w, r) error` and one mapper, `httputil.Fail`.
  The mapper writes `Error{Status, Message}` as Express `send(message)`
  (text/html with an ETag) or `sendStatus` (text/plain with an ETag), and
  logs the cause on 5xx. `httputil.JSON` adds Express's weak ETag and a
  304 on `If-None-Match`, which was missing from Go before (Lissen
  revalidates cached GETs). Packages register `Routes(*http.ServeMux)`,
  which the runtime discovers through an interface, so packages convert
  independently. One trailing slash routes like Express non-strict
  routing. An unknown route is a 404, as in TS.

**Consequences.** The legacy chain adapter (`Runtime.legacyChain`) is
deleted once the last package stops exposing `Handle`. Response options
(`WithoutETag`, `Streamed`, `ForRequest`, …) are deleted when they are
unused.

## Ledger

| Step | Commit | Handwritten before → after | Net | Complexity | Notes |
|------|--------|----------------------------|-----|------------|-------|
| Baseline | 8e17183 | 44,674 | — | 10,809 | 990 `map[string]any`, 9 Handle routers |
