# Architecture: target state

Upstream TypeScript is a **behavioral contract**, not an architecture. This
document describes the Go structure that new and migrated code follows.
`ARCHITECTURE-REVIEW-2026-10-29.md` is dated evidence of the earlier state;
where its "anti-recommendations" conflict with this file, this file wins.

Tags: **[enforced]** a tool fails the build; **[planned]** a roadmap phase adds
the check; **[guidance]** reviewed by a human. **DECISION** marks something the
project owner must confirm; the stated default applies until then.

## Principles

1. **Compatibility decides observable behavior; Go idioms decide structure.**
   Wire shapes, status codes, quirks, and persistence are fixed by upstream.
   Everything behind the HTTP boundary is ours to design.
2. **One owner per concern.** Each cross-cutting helper (JSON writing, time
   formatting, IDs, events, authz, ffmpeg) has exactly one package. Copies are
   defects.
3. **Types at the boundary.** Requests and responses are typed structs. Dynamic
   maps are allowed only for genuinely opaque pass-through JSON.
4. **Explicit wiring.** No setters, no globals, no constructors that start
   goroutines.
5. **Change structure only under a differential gate.** Refactors keep the
   differential case count and result unchanged.

## Package layout

```
internal/
  server/         composition root; Runtime/Manager; assembles the mux
  config/         typed env/flag parsing (the only place env is read)
  bootstrap/      fresh-init and schema/migration verification
  store/          open/pragma/schema boundary; thin, not a repository framework
  auth/           token verify AND issue (one JWT implementation)

  wire/           typed request/response DTOs, grouped by domain
  httputil/       Express-exact response writers, body decoding, error mapping
  dbtime/         the single SQLite DATETIME write format and parse
  events/         one Emitter interface/func type; Bus for fan-out
  authz/          permission evaluation (after characterization, see below)
  ids/            UUID generation (one implementation, returns error)
  media/          ffmpeg/ffprobe runner: path resolution, timeouts, stderr

  catalog/ userapi/ identity/ playback/ scanner/ podcasts/ providers/
  sharing/ adminops/ notifications/ emailapi/ backup/ audiotools/ realtime/
                  feature packages, one per upstream controller area;
                  each exposes Routes(mux, deps)
```

Feature packages keep their 1:1 mapping to upstream controllers. That mapping
is navigable and lets code cite upstream source lines. The problem was copied
helpers, not package boundaries.

**Dependency rules** [planned: `depguard` in `.golangci.yml`, Phase 1]:

- `wire` imports nothing from `internal/`.
- Leaf packages (`httputil`, `dbtime`, `events`, `ids`, `media`, `authz`) import
  only the standard library and `wire`.
- Feature packages may import leaf packages, `store`, `auth`, `config`.
- Feature packages must not import each other. Shared read-side serialization
  moves to a shared package (see DECISION D1).
- Nothing outside `*_test.go` imports `net/http/httptest`.

**DECISION D1** (default: extract): today `scanner` and `userapi` each construct
a hidden `catalog.Handler` just to call `ItemJSON`/`ItemMinifiedJSON`. Extract
that read side into `internal/items` (`items.Reader`), used by `catalog`,
`userapi`, `scanner`, and the emit hook. Until extracted, exactly one
`catalog.Handler` exists and is shared by injection.

## Request flow

```
net/http -> Runtime.ServeHTTP
  -> realtime (Engine.IO paths, matched on the original URL)
  -> strip router base once
  -> ServeMux (method+path patterns, per-package Routes)
       -> auth wrappers: public | authenticated | admin
       -> handler: decode request DTO -> do work -> build response DTO
       -> httputil writer
```

Rules:

- **Routing** [planned, Phase 5]: Go 1.22+ `ServeMux` patterns
  (`mux.HandleFunc("GET /api/libraries/{id}", h.library)`), registered by each
  package's `Routes(mux, deps)`. Strip the router base exactly once, in
  `Runtime`. Packages never strip prefixes themselves.
- **Auth is a wrapper, not a preamble.** Public endpoints (capability URLs,
  item covers, author images) are registered explicitly as public. Do not
  repeat access checks as per-route boilerplate.
- **404/405 are upstream's, not `ServeMux`'s.** Register explicit fallbacks
  that reproduce Express behavior, and give them smoke cases.
- **Migration is a strangler.** During Phase 5 a catch-all `/` pattern delegates
  to the legacy `Handle` chain. Each package moves to `Routes` only when its
  routes have smoke cases (happy path, 401, 403, 404, wrong method).

## Wire DTOs

Applies to `internal/wire` [guidance; the ratchet counts `map[string]any`].

- One struct per wire shape, **named for the shape on the wire**, not for TS
  methods or flags. Do not use names like `oldJSON`, `legacyJSON`, or an
  `expanded bool` that switches between different shapes. Two shapes are two
  types.
- Field order in the struct follows upstream's key order.
- A nullable scalar is `*T` **without** `omitempty`, so it marshals as `null`.
- A key that upstream omits conditionally uses a pointer **with** `omitempty`.
- Slices must be non-nil (`make([]T, 0)`) so they marshal as `[]`, never `null`.
  Use constructors in `wire` where it is easy to forget.
- Opaque pass-through JSON stored in a column stays `json.RawMessage` until a
  later phase types it.
- Every DTO has a golden or dual-run test that covers null, empty, and absent
  variants. Compare after decoding and also check null-vs-absent explicitly.
- Requests: decode into typed structs with one shared decoder (size limit,
  trailing-data check, `UseNumber` only where upstream semantics need it).
  Unknown shapes are rejected the way upstream rejects them, not by panic.

**DECISION D2** (default: pointers): if a field can be null **and** absent as
distinct states, add `wire.Optional[T]` with custom marshalling. Do not
introduce it speculatively.

## Dependencies and lifecycle

- Collaborators arrive through a deps struct on `New`. No `Set*` after
  construction. Nil-emit guards disappear because a nil emitter is a
  construction error.
- `New` allocates; it does not start cron, goroutines, or DB work. `Start`
  begins background work; `Close` stops it and **waits** for in-flight work.
- Background goroutines take a context from `Start`, not
  `context.Background()`.
- `Manager`/`Runtime` hot-restore with rollback stays. It has no upstream
  counterpart and is the reason the two types exist.
- Handlers call plain functions, never each other through fabricated requests.
  `httptest` recorders and forged users in production code are removed.

## Events

- One `events.Emitter` type. Packages receive it in deps; there are no per-package
  `EmitFunc` types.
- Handlers and services emit domain events by name and payload DTO. Only
  `realtime` knows about sockets, rooms, and recipient filtering.
- Fan-out snapshots clients under the lock, then emits outside it.

## Persistence

- `store` stays a thin open/pragma/schema boundary. No generic repository
  framework and no DI container.
- Transactions are owned by the calling handler or service, explicitly.
- All DATETIME columns are written through `dbtime` in one format and parsed
  through one `dbtime.Parse` [planned: Phase 3; needs a DB-content differential
  first because the stored format is visible to upstream].
- Existing N+1 query patterns are allowed during the parity phases. Do not
  change query behavior in a structure-only packet. Record the hot spots for
  later.
- No SQL assembled with `map` iteration order. Build update statements from an
  ordered slice so text is deterministic.

## Authorization

- Seven independent ACL implementations exist and at least two disagree on tag
  semantics (any-tag vs every-tag). **Do not unify by assumption.** First
  characterize each against upstream with a generated permission matrix, then
  consolidate into `authz` where behavior is identical and keep named variants
  where upstream differs.
- Every `authz` rule cites the upstream file and line it reproduces.

## Errors

- Errors are values. No `_ = json.Unmarshal(...)`, no unchecked type assertions
  on request-derived data, no panics in request paths.
- Map errors to HTTP in one place (`httputil`). A database error is not a 404.
- Upstream HTTP failures from providers are not silently converted to empty
  results; callers can tell "no results" from "provider down" (unless upstream
  does exactly that, in which case cite it).

## Shared utilities: single owners

| Concern | Owner | Replaces |
|---------|-------|----------|
| JSON/text response writers, Express quirks | `httputil` | ~12 `writeJSON` copies, 4 Express text writers |
| SQLite DATETIME format and parse | `dbtime` | 13 layouts, parsers repeated 7x |
| Event emission type | `events` | 7 named `EmitFunc` types plus anonymous ones |
| UUID generation | `ids` | 4-6 variants (panic, error, silent, empty) |
| ffmpeg/ffprobe execution | `media` | 4-5 resolution and exec sites |
| JWT sign and verify | `auth` | 3 signers, one with a dead branch |
| Permission evaluation | `authz` | 7 copies, after characterization |
| HTML sanitize/strip, Levenshtein, MIME tables, cron parser | a single shared owner each | duplicates across packages |

## Naming

- No TS-shaped names (`oldJSON`, `legacyJSON`, `minifiedFeed`, `clientJSON`).
  Name by wire shape or purpose.
- The two `titleIgnorePrefix` functions have different upstream semantics
  (`getTitlePrefixAtEnd` vs `getTitleIgnorePrefix`). Rename both distinctly;
  never merge them.

## Concurrency

- One mutex per concern; do not guard unrelated state with one lock.
- Do not spawn a goroutine inside a cron callback (cron already does).
- Shared mutable task state is guarded or confined to one goroutine, and has a
  `-race` test.
- Settings read-modify-write is serialized or transactional.
- Cache read-mostly settings (such as the CSP `frame-ancestors` flag) and
  invalidate through the settings-changed event, not a query per request.

## Ratchet metrics

`make ratchet` [planned: Phase 1] counts these and compares to
`ratchet-baseline.json`. Counts may not rise. After a drop, the
baseline must be lowered in the same commit.

| Metric | Baseline (2026-10-30 snapshot) | Phase target |
|--------|-------------------------------|--------------|
| `map[string]any` and aliases of it | 1057 | set after pilot |
| Hand-rolled `Handle(w, r, ...) bool` routers | 13 | 0 (Phase 5) |
| `writeJSON`-style copies | 12 | 1 in `httputil` (Phase 3) |
| `EmitFunc`-style type declarations | 7 | 0 (Phase 3) |
| `Set*` injector methods | 20 | 0 (Phase 4) |
| `catalog.New` call sites | 3 | 1 or 0 with `items` (Phase 4) |
| `httptest` imports in non-test files | 1 | 0 (Phase 4) |
| Inline `comparePair(...)` calls in `tools/parity/differential.cjs` | 91 | 0 (Phase 0b) |
| Duplicated harness helper definitions (`freePort`, `login`, `request`, `waitReady`, `parseArgs`) across `tools/parity/*.cjs` | 10, 10, 5, 4, 4 copies | 1 each (Phase 0b) |

The Go metrics use `go/ast`; the two harness metrics use a regex counter over
`tools/parity/`. Aliases such as `type JSON = map[string]any` count as the thing they alias.
The ratchet is a floor, not proof of quality; review still checks intent.

## Open decisions

| ID | Question | Default |
|----|----------|---------|
| D1 | Extract `internal/items` read side, or share one `catalog.Handler` | Extract |
| D2 | `*T` pointers vs `wire.Optional[T]` for null-and-absent | Pointers |
| D3 | `net/http.ServeMux` vs a router library | Stdlib `ServeMux` |
| D4 | Keep the Node-based differential harness or port it to Go | Keep; consolidate into one runner first |
| D5 | Type inner objects (`AudioFile`, `Chapter`) in the pilot or later | Later |
