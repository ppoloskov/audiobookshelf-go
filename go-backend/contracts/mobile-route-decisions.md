# Route and event decisions — 2026-11-04

Preliminary reconciliation; MC-B remains open for the complete Go registration
and caller census. Inputs: generated [client-usage.json](client-usage.json),
[external evidence](mobile-client-evidence.json) and [source audit](mobile-client-audit.md).
No runtime route was removed and the generated inventory was not hand-edited.

## Existing web/log/trial set

All 201 listed routes remain protected while their recorded callers are
reconciled. `sources` distinguishes web from owner mobile logs and inferred
trial steps; it is not third-party source coverage. The JSON preserves exact
methods, paths, caller locations and coarse Go handler mappings; these mappings
are not proof of actual runtime registration or behavioral tests. MC-B must
reconcile modern `Routes(mux)` registrations and legacy `Handle` branches.

One unmatched `/api/:param` pattern and nine dynamic call sites remain exactly
as recorded; inspect them before proposing any deletion. No absence inference
is safe until the caller scope is complete. No route is yet a REMOVE CANDIDATE
awaiting approval: insufficiently investigated routes are INVESTIGATE instead.

## All 25 entries currently called unused by the generator

Each has exactly one decision below. KEEP means demonstrated caller, not a
runtime PASS. INVESTIGATE means no caller established by this bounded review,
not absence across clients. Known external callers link to SHA-pinned rows in
the audit. Do not reinterpret these as owner removal approvals.

| Method/path | Go mapping (from inventory) | Decision | Justification / test or task |
|---|---|---|---|
| ALL * | go-backend/internal/server/runtime.go | INVESTIGATE | Caller census/dynamic-site review incomplete; MC-A/MC-B; tests not reconciled |
| GET /api/^\/libraries/ |  | INVESTIGATE | Caller census/dynamic-site review incomplete; MC-A/MC-B; tests not reconciled |
| GET /api/collections | go-backend/internal/userapi/userapi.go | KEEP | yaabsa active caller; see mobile audit; MC-07/MC-B; consumer test pending |
| GET /api/me/bookmarks | go-backend/internal/userapi/userapi.go | INVESTIGATE | Caller census/dynamic-site review incomplete; MC-A/MC-B; tests not reconciled |
| GET /api/me/bookmarks/:libraryItemId | go-backend/internal/userapi/userapi.go | INVESTIGATE | Caller census/dynamic-site review incomplete; MC-A/MC-B; tests not reconciled |
| GET /api/me/progress | go-backend/internal/userapi/userapi.go | KEEP | yaabsa active caller; Absorb also confirmed; see mobile audit; MC-07/MC-B; consumer test pending |
| GET /api/notificationdata | go-backend/internal/notifications/handler.go | INVESTIGATE | Caller census/dynamic-site review incomplete; MC-A/MC-B; tests not reconciled |
| GET /api/playlists | go-backend/internal/userapi/userapi.go | KEEP | yaabsa active caller; see mobile audit; MC-07/MC-B; consumer test pending |
| POST /api/playlists/:id/item | go-backend/internal/userapi/userapi.go | INVESTIGATE | Caller census/dynamic-site review incomplete; MC-A/MC-B; tests not reconciled |
| GET /api/podcasts/:id/downloads | go-backend/internal/podcasts/podcasts.go | INVESTIGATE | Caller census/dynamic-site review incomplete; MC-A/MC-B; tests not reconciled |
| GET /api/series/:id | go-backend/internal/catalog/handler.go | INVESTIGATE | Caller census/dynamic-site review incomplete; MC-A/MC-B; tests not reconciled |
| PATCH /api/series/:id | go-backend/internal/catalog/handler.go | INVESTIGATE | Caller census/dynamic-site review incomplete; MC-A/MC-B; tests not reconciled |
| GET /api/session/:id | go-backend/internal/playback/playback.go | INVESTIGATE | Caller census/dynamic-site review incomplete; MC-A/MC-B; tests not reconciled |
| POST /api/session/local-all | go-backend/internal/playback/playback.go | KEEP | Official iOS active caller; Lissen DTO corroborates shape; see mobile audit; MC-06 semantics still untested |
| GET /api/stats/server | go-backend/internal/adminops/handler.go | INVESTIGATE | Caller census/dynamic-site review incomplete; MC-A/MC-B; tests not reconciled |
| GET /api/users/online | go-backend/internal/userapi/userapi.go | INVESTIGATE | Caller census/dynamic-site review incomplete; MC-A/MC-B; tests not reconciled |
| POST /api/watcher/update | go-backend/internal/adminops/handler.go | INVESTIGATE | Caller census/dynamic-site review incomplete; MC-A/MC-B; tests not reconciled |
| GET /feed/:slug/cover* | go-backend/internal/sharing/handler.go | INVESTIGATE | Caller census/dynamic-site review incomplete; MC-A/MC-B; tests not reconciled |
| GET /feed/:slug/item/:episodeId/* | go-backend/internal/sharing/handler.go | INVESTIGATE | Caller census/dynamic-site review incomplete; MC-A/MC-B; tests not reconciled |
| GET /healthcheck | go-backend/internal/server/runtime.go | INVESTIGATE | Caller census/dynamic-site review incomplete; MC-A/MC-B; tests not reconciled |
| GET /hls/:stream/:file | go-backend/internal/playback/playback.go | INVESTIGATE | Caller census/dynamic-site review incomplete; MC-A/MC-B; tests not reconciled |
| POST /logout | go-backend/internal/identity/identity.go | INVESTIGATE | Caller census/dynamic-site review incomplete; MC-A/MC-B; tests not reconciled |
| GET /ping | go-backend/internal/server/runtime.go | INVESTIGATE | Caller census/dynamic-site review incomplete; MC-A/MC-B; tests not reconciled |
| GET /public/share/:slug/track/:index | go-backend/internal/sharing/handler.go | INVESTIGATE | Caller census/dynamic-site review incomplete; MC-A/MC-B; tests not reconciled |
| GET route | go-backend/internal/server/runtime.go | INVESTIGATE | Caller census/dynamic-site review incomplete; MC-A/MC-B; tests not reconciled |

## Socket events

The generated web event emit/listen arrays retain exact caller references.
C5 search/cancel cover events are implemented; current `search_covers` and
`cancel_cover_search` are KEEP, not missing dispatchers. Existing Socket.IO
packet/ack and JS happy-path tests are named in the gap/test plan.

External auth listener decisions: `auth_failed` KEEP (official/Absorb/Storii
source); `invalid_token` INVESTIGATE (yaabsa listener; user effect not proved,
no replacement of other clients' event approved). Existing Go emits the former.
Missing/incompatible event census beyond these selected paths is unresolved;
MC-A/MC-C must reconcile recipient, payload and ack semantics with pinned sources.

## Reproducible process to implement (MC-B)

Use the author-maintained external evidence input as a validated generator
input, not edits to generated JSON. Normalize only concrete method/path calls;
keep DTO/comment/constant inferences distinct and retain commit/file/line
provenance. Test KEEP protection of the three GET routes plus iOS bulk local-all, deterministic
merge, invalid evidence rejection and unchanged unresolved call sites.
Extend existing `tools/clientusage` tests; `TestOutputIsCurrent` already
provides a non-writing freshness check. Regenerate with root `make client-usage`
and verify repeat output identity. Full registration/route/event coverage must
be reported separately from route shapes and behavioral test coverage.
