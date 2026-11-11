# MC-05 and S9b evidence

Updated 2026-11-05. MC-05, MC-05/F1 and S9b-R1 are DONE after full gates.
This records bounded evidence; it does not complete the mobile audit or
authorize framing-option deletion.

## MC-05: pinned Lissen behavior

Pinned Lissen [SafeApiCall](https://github.com/GrakovNe/lissen-android/blob/421288498f429f4c438528490807de86ec59f382/app/src/main/kotlin/org/grakovne/lissen/channel/audiobookshelf/common/api/SafeApiCall.kt#L64-L89)
accepts any nonnull successful typed response body. It inserts a response in
its cache only when an ETag exists, and sends a stored validator only when one
exists via the [conditional cache interceptor](https://github.com/GrakovNe/lissen-android/blob/421288498f429f4c438528490807de86ec59f382/app/src/main/kotlin/org/grakovne/lissen/channel/audiobookshelf/common/api/ConditionalCacheInterceptor.kt#L11-L20).
When a cache entry exists, a matching 304 returns the typed cached object; if
it was evicted, Lissen retries without the validator. Thus a fresh JSON 200
without ETag is accepted and does not establish a missing server cache layer
or required 304 behavior.

The pinned [API client](https://github.com/GrakovNe/lissen-android/blob/421288498f429f4c438528490807de86ec59f382/app/src/main/kotlin/org/grakovne/lissen/channel/audiobookshelf/common/client/AudiobookshelfApiClient.kt#L46-L169)
has 12 Cacheable typed-JSON declarations. The regression
`TestMobileConditionalGET` covers these corresponding route families:

| Route case | Contract checked |
|---|---|
| libraries | fresh typed JSON; validator behavior |
| library with filterdata | fresh typed JSON; validator behavior |
| current user (`/api/me`) | fresh typed JSON; validator behavior |
| book library items | typed results and current item |
| podcast library items | typed results and current item |
| library authors | typed results and current author |
| book stats | current total item count |
| narrators | typed narrator array |
| playlists | typed results |
| book item | current typed item |
| podcast item | current typed item |
| author with included items | author response and validator behavior |

For each route the test requires authenticated JSON 200 and current seeded
typed values. If the first response advertises ETag, replay that exact value
and require an empty 304; without ETag, send a stale client validator and
require a fresh 200 body. Authentication is checked before conditional
handling. The author-items endpoint exposed a specific mismatch: its 200
response advertised an ETag but ignored matching If-None-Match. F1 replaces
the old buffer/encoder/newline-trim producer with a request-aware replacement
using the existing `writeJSONWithETag`, at 14 callsites. The old producer is deleted
in the same fix; one producer remains, with no bridge, duplicate or extra
helper consumer. Measured cost is +17 production lines and +9 complexity.
The JSON-writer-style metric 1→2 is a detector artifact from renaming that
writer; it is explicitly accepted as +1 and was not evaded by changing the
name. No additional safe same-scope cuts were found. PR05 savings are not an
offset for this correction. No schema, route or dependency change, and no
global cache layer.

The separate `TestMobileCoverParameters` contract covers original and resized
book covers and a width-only author image request, validating image MIME,
decode format and requested width. Separate characterization against a disposable TS-created fixture
observed `/api/libraries` returning a fresh 200 with no ETag, and direct media
`ServeContent` returning 304 for a matching validator and a changed 200 after
file mtime/content changed. These observations support conditional semantics;
they are not a complete per-endpoint TS or real-device audit.

F1 passed source, contract, craft and full integration review. Build, vet,
tests, arm64 build, ratchet (including dbnest) and golangci-lint 2.15.0 (built
with Go 1.27.0, zero issues) all PASS. Logs:
`/tmp/abs-pr06-pr05-20261105/mc05-f1/verification`. The root updated three
baseline values in the same code commit. No SQL query input, route wiring,
schema or dependency changed; the 14 writer callsites were updated as
described above. Rollback, benchmark and real-device suites were not rerun;
no full Lissen app/device integration was performed. MC-A/B/C/D/E and the
broader six-client audit remain open.

## S9b: retained framing options

The five option families are `ContentType`, `WithoutETag`,
`WithoutContentLength`, `Streamed`, and `ForRequest`. `ContentType` appears at
four provider error callers; the others have multiple callers. These counts
show scope only. KEEP means no
equivalence-based safe-removal proof exists, not that every option or header
has been proven mandatory. Successful typed-JSON responses do not prove
provider error body/MIME behavior, stream framing, or HEAD equivalence.

S9b-R1 is DONE as a bounded evidence review: record KEEP for all five option
families until a specific family has a safe-equivalence proof. R1 deletes no
code and does not block independent L04/list work. The broader S9b deletion
card stays pending until one caller family has an exact old-code deletion
scope, behavior evidence, a net source reduction and the focused plus
integration checks. Do not remove an option based only on call count or
Lissen's JSON cache behavior. Preserve demonstrated ETag/304, range, freshness,
MIME and HEAD semantics; evaluate each independently. No new growth budget is
approved for S9b.
