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

## ADR-002: the JSON contract is semantic, not Express bytes (owner, 2026-11-07)

**Decision.** Clients and TS rollback read JSON by value. The contract is the
set of keys, their JSON types and values, array order, and `null` vs absent
where a reader branches on it. Not contract, and not to be preserved with
code: object key order (HTTP bodies and persisted JSON columns alike),
whitespace and trailing newlines, which framing an error body uses
(text/html vs text/plain), and Express's exact ETag bytes (an ETag only has to
change when the body changes).

**Consequences.** Delete code whose only job is key order or byte fidelity
(ordered marshal structs, order-preserving JSON rewriters, insertion-order
bookkeeping, superset-map-and-delete shapes kept for order) and turn byte or
order assertions into semantic comparisons (`internal/compat` already ignores
key order). New code uses plain typed structs. Persisted JSON keeps its values
and nulls, because TS reads those back.

## Ledger

| Step | Commit | Handwritten before → after | Net | Complexity | Notes |
|------|--------|----------------------------|-----|------------|-------|
| Baseline | 8e17183 | 44,674 | — | 10,809 | 990 `map[string]any`, 9 Handle routers |
| M1: handlers return error; Handle chains → mux | (this commit) | 44,674 → 43,348 | −1,326 (prod +2,560 / −4,036) | 10,809 → 10,406 | 8 hand routers and the legacy adapter deleted; JSON 200 GET/HEAD gains ETag/304; unknown routes 404 |
| M2: one error framing (httputil.Fail); per-package write helpers deleted; catalog round 2 | (this commit) | 43,348 → 42,743 | −605 | 10,406 → 10,380 | ~20 helpers (bare, text, reply, reject, plainError, dbFail, …) and catalog adapters gone; error bodies follow the TS send/sendStatus choice |
| M3: ADR-002 sweep (key-order/byte-fidelity code deleted) | (this commit) | 42,743 → 42,578 | −165 | 10,380 → 10,347 | httputil Streamed/WithoutContentLength/WithoutETag, catalog writeJSONWithETag, OIDC ordered rewriter, playback ordered metadata structs; byte/order test assertions now compare values |

### M1 behaviour changes (all toward TS/Express)

- An unknown `/api` route answers 404 `Not Found` (text/plain) instead of a
  501 JSON body.
- One trailing slash routes on every mux route (Express non-strict routing).
  `//` inside a path no longer matches (it did through `path.Clean`).
- A method that a route does not declare answers 404 instead of being served
  (catalog's any-method branches are now GET only) or answering 405 (playback
  API routes). Public `/hls` and `/public/session` keep 405.
- GET routes answer HEAD (sharing `/feed` and `/public/share`, playback).
- A non-admin sending an undeclared method to `/api/notifications*` gets 404,
  not 403 (TS per-route middleware).
- Three userapi/lists database-failure 500s (`targetUser`,
  `requireItemAccess`, `libraryListPagination`) are now Express-framed
  (text/html) instead of `http.Error` text/plain.
- playback's `GET /api/items/{id}/file/{ino}` handler was unreachable
  (catalog matched first) and is deleted.

### Mechanisms removed in M1

Hand-written path routers in catalog, userapi, sharing, playback, scanner,
podcasts, notifications and emailapi; `Runtime.legacyChain`, `features()`,
`legacyRouter`, `isPublicPlayback`, the sharing/playback public
pre-dispatch, the lists `notImplemented` 404 shim, the 501 fallback, and the
`PathUnescape` 400 branches that the mux's decoded `PathValue` makes dead.
