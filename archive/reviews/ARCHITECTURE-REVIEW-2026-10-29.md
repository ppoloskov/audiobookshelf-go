# Independent Go Architecture Review — `go-backend`

**Date:** 2026-10-29 **Scope:** Read-only, greenfield architecture review of the
entire Go backend (82 files, ~37.8k LOC, 21 `internal/` packages, 2 `cmd/`
binaries). Upstream TypeScript (`server/` @
`edb1f7efc92ecacdef51aafb4d9d5054ce10604f`, v2.37.2) is treated strictly as a
behavioural contract, not an architecture to reproduce. **Method:** Full manual
read of core wiring plus exhaustive per-package sweeps; `go build` / `go vet`
(clean); `staticcheck@latest` and `deadcode@latest` executed in isolated caches
(Go 1.27.0 toolchain). Every load-bearing claim was re-verified against the
working tree; claims that did not survive verification were excluded or
downgraded. **Review type:** Read-only. No code was modified; no task ledger was
created or updated (expressly instructed). The two review passes
(contract/correctness and Go craft/simplicity) are independently reasoned but
executed by a single reviewer — not independent-reviewer execution.

---

## Pass 1 — Contract / correctness (adversarial)

### BLOCKING

#### B1. Swapped `(page, limit)` tuple in the series route — VERIFIED

`internal/catalog/handler.go:1956`

`pagination` returns `(page, limit)` (`handler.go:698-711`:
`return page, limit, nil`). The series handler assigns:

```go
limit, page, err := pagination(r)   // handler.go:1956 — swapped
```

`start := page*limit` is accidentally correct by commutativity, but
`end := start + limit` (`:1966`) slices with the *page* value as the limit, and
the payload (`:2006`) reports `limit`/`page` swapped. The sibling caller at
`:2175` (`page, limit, err := pagination(r)`) is correct. Series pagination
diverges from upstream whenever both query params are present.

- **Repair:** swap the two identifiers.
- **Verification:** extend `tools/parity` with
  `/api/libraries/{id}/series?page=&limit=` variants — the current 71-case suite
  never exercises paginated series.

#### B2. `scanner.QuickMatchPodcastEpisodes` is production-dead: the two halves are never wired — VERIFIED

`internal/server/runtime.go:98` wires one direction:

```go
rt.podcasts.SetQuickMatchHandler(sc.QuickMatchPodcastEpisodes)
```

But `sc.podcastFeedFetcher` is only set by `scanner.SetPodcastFeedFetcher`
(`scanner/scanner.go:169`), whose **only caller in the repo is a test**
(`scanner_test.go:1703`). Meanwhile `podcasts.FetchFeedEpisodes`
(`podcasts.go:571`) — the obvious intended fetcher — has
**zero production callers**. In production the route always returns
`"scanner: podcast feed fetcher is not configured"` (`podcast_match.go:23-25`),
and the whole `podcast_match.go` feature (~200 lines including the fuzzy
Levenshtein matcher `bestFeedEpisode`/`editSimilarity`, `:110-164`) is dead.

- **Repair:** one line in `newRuntime`:
  `sc.SetPodcastFeedFetcher(rt.podcasts.FetchFeedEpisodes)`.
- **Verification:** differential case for `/api/podcasts/match-episodes`; also
  resolves two dead-export findings (S8).

#### B3. `Runtime.Close` never closes `podcasts.Handler` — VERIFIED

`internal/server/runtime.go:211-227` closes backup, scanner, audiotools,
playback, realtime — **not podcasts**. `podcasts.New` starts a cron scheduler
inside the constructor (`podcasts.go:918-940`); its `Close`
(`podcasts.go:611-622`) is never invoked, so **every backup restore**
(`manager.go:98-180` rebuilds the Runtime) leaks a scheduler and download queue
pinned to the old, closed `*sql.DB`. Go's shutdown is here *less* complete than
upstream's `Server.stop()` (`Server.js:562-570`).

- **Repair:** add `rt.podcasts.Close()`; longer-term move cron start out of
  `New` (see S8/S3).
- **Verification:** restore test asserting cron entry count before/after;
  race-detector run of the restore suite.

#### B4. Data race on podcast download-task state — VERIFIED

`internal/podcasts/downloads.go:147-150` mutates `d.failed`, `d.finished`,
`d.finishedAt` with no lock:

```go
d.failed = true            // no queueMu held
...
d.finished = true
d.finishedAt = time.Now().UnixMilli()
```

while `downloadsFor` (`downloads.go:98-108`) reads the same fields under
`queueMu` via `d.clientJSON()`. The race-detector snapshot passed only because
this path wasn't exercised concurrently.

- **Repair:** guard the writes with `queueMu` (or confine mutation to the queue
  goroutine).
- **Verification:** concurrent download + status-poll test under `-race`.

#### B5. Two DATETIME write formats for the same columns

Most packages write `"2006-01-02 15:04:05.000 -07:00"` (12+ sites:
`adminops:114,663`, `notifications:72`, `emailapi:85`, `providers:344`,
`sharing:1289`, `catalog` and `scanner` throughout); playback writes
`time.RFC3339Nano` (`playback.go:832-833`) and podcasts likewise
(`podcasts.go:442`). Readers cope via multi-layout parsing (4–5 layouts,
re-implemented 7×), but the stored format is upstream-visible (Sequelize writes
one canonical format) and any consumer doing string comparison/sort on these
columns is exposed.

- **Repair:** one `dbtime` leaf package, one write format matching upstream's.
- **Verification:** DB-content differential on playback-session save and podcast
  update flows.

### SHOULD_FIX (correctness-adjacent)

- **B6. Divergent tag-permission semantics:** playback `canAccess` allows when
  *any* item tag is selected (`playback.go:1749-1754`); emailapi requires
  *every* tag (`handler.go:480-493`). Seven independent ACL implementations
  exist (playback `canAccess` :1717-1755, audiotools :660-676, podcasts
  `canAccessPodcast` item.go:182-230, sharing `libraryAccess` :1167-1182,
  adminops `canAccessLibrary` :871-889, emailapi
  `libraryAllowed`+`canAccessContent` :447-495, providers/admin checks). At
  least one diverges from upstream. Characterize each upstream rule before
  unifying — do not assume they should be identical.
- **B7. Three JWT signers, one of them broken:** `identity.sign`
  (`identity.go:608-618`), `userapi.signToken` (`userapi.go:2027-2058`, re-reads
  the secret from settings per call and contains a **dead branch** —
  `settings["tokenSecret"].([]byte)` at `:2038` can never succeed because
  `json.Unmarshal` into `map[string]any` yields `string`, never `[]byte`), while
  `auth` can verify but not issue. Consolidate issue+verify in `auth`.
- **B8. Unbounded `rateBuckets` growth** (`identity.go:52`, `936-963`) — one map
  entry per client IP forever, never pruned; no session reaper exists at all
  (expired sessions accumulate between opportunistic `DELETE`s at
  `identity.go:386,409,465-468`). Upstream `TokenManager` does periodic cleanup;
  parity debt. OIDC flow map is lazily pruned only on OIDC endpoint hits
  (`oidc.go:748-759`).
- **B9. Swallowed errors / panic-asymmetry:** 17+ `_ = json.Unmarshal` in
  userapi/identity/listening (`userapi.go:438,2159,2253,2270`, …); unchecked
  `payload["user"].(map[string]any)` assertions (`identity.go:357,434`;
  `oidc.go:274`) panic on shape drift; `newUUID` panics (`userapi.go:2525-2527`)
  while `randomUUID` returns an error (`identity.go:1009-1018`) — same
  operation, opposite contracts; `emailapi` silently truncates attachments at
  512 MiB (`io.LimitReader(f, 512<<20)`, `handler.go:247`);
  `getItemListeningSessions` maps *any* DB error to 404 (`me_extra.go:37-40`);
  batch progress update silently drops per-item failures (`userapi.go:311-314`);
  `handleUser` returns empty 200s on unrecognized subpaths
  (`userapi.go:717-719,781-783`) after `Handle` already claimed the request.
- **B10. Dead backup→notification pipeline:** `backup.SetNotificationHandler`
  (`backup.go:112`) has no production caller, so `notifyEvent`
  (`backup.go:118-125`) is inert and `notifications.TriggerEvent`
  (`notifications/handler.go:227`) is test-reachable only (grep-verified).
  `onBackupCompleted`/`onBackupFailed` payloads (`backup.go:257,261`) are
  computed and discarded. Upstream *does* emit these notifications, so wiring
  (`backup.SetNotificationHandler(notifications.TriggerEvent)`) is the
  parity-correct choice; deletion is the alternative.

### Correctness NOTES (verified non-blocking)

- `batch_chapters.go:86-90` builds its visibility query via map lookup before
  the media-type existence check — verified the 403 return
  **precedes execution**, so invalid SQL never reaches SQLite. Style only:
  validate before building.
- `err == sql.ErrNoRows` instead of `errors.Is` at 7 sites (`cover.go:201`,
  `series_features.go:37`, `download.go:63`, `files.go:121,178,369,387`,
  `adminops/upload.go:38,51`); inconsistent cursor-close patterns (manual
  `rows.Err(); rows.Close()` ×36 vs `defer rows.Close()` ×7 in
  `catalog/handler.go`); nondeterministic UPDATE SQL text from map iteration
  (`writes.go:113-121`, `library_writes.go:426-438`) — harmless to SQLite,
  hostile to tests/logging.
- N+1 by design: per-item media queries in `libraryItems`
  (`handler.go:2253-2290` ≈ 3+ queries/item plus a settings re-read per
  `titlePrefix` call), per-author COUNT (`:1715-1728`), per-series-per-book
  `seriesBooks`→`ItemMinifiedJSON` (`:1990-2005`), per-key lookups in `apiKeys`
  (`userapi.go:1923-1955`), per-item `canAccessItem`+`ItemJSON` in
  `collectionDTO` (`userapi.go:1467-1481`), per-client
  `json.Unmarshal(user.Permissions)` per emitted event (`hub.go:470-565`). The
  `SetMaxOpenConns(1)` pool (`store.go:69`, `open_write.go:35`) forces
  materialization workarounds (documented at `userapi.go:1247-1248`).
  Performance debt, not correctness.
- `expandedBookItem` re-implements `bookJSON` expansion and runs a **second**
  `SELECT duration FROM books` per item (`handler.go:407-422` vs `:82-85`);
  `expandedPodcastItem` double-queries episodes (`:230` then `:337`);
  `bookMinifiedJSON` builds tracks via `bookJSON(expanded=true)` then deletes
  them (`:145-169`).
- realtime `Emit` holds `RLock` across blocking socket writes (`hub.go:361-382`)
  while `EmitSocket` already uses the correct snapshot-then-emit pattern
  (`:385-396`) — contention smell, not a correctness bug at current client
  counts.

---

## Pass 2 — Go craft / simplicity

### Where the design mirrors TypeScript instead of being Go-native

The upstream is a behavioural contract, but four TS structures were reproduced
that are **not** contractually required.

#### S1. Express routing reproduced as a bool-chain — the single largest structural finding

`Runtime.ServeHTTP` (`runtime.go:229-338`) linearly probes **13**
`Handle(w, r, user) bool` switches; `withPath` (`runtime.go:378-384`) calls
`r.Clone()` — a deep header copy — **up to 14 times per request**. Each package
re-implements path splitting with *inconsistent normalization*:

- `catalog.Handle` (`handler.go:1017-1193`): `path.Clean` + ~35 route arms of
  manual `len(parts)==N && parts[K]=="literal" && r.Method==X` chains;
  `r.Method != GET && != HEAD` repeated 7×; dead trailing dispatch block at
  `:1189-1192`.
- `userapi.Handle` (`userapi.go:60-116`): strips `/api` prefix itself; tests
  call it with and without the prefix and both "work" (`userapi_test.go:97` vs
  `:153`).
- `identity.Handle` (`identity.go:177-181`): strips the router base a
  **second time** after `ServeHTTP` already stripped it (`runtime.go:239`).
- `scanner.Handle` (`scanner.go:232-343`): re-splits the path 3 times
  (`:280,285,294,318`); `url.PathUnescape` applied to library IDs (`:296`) but
  not item IDs (`:320`); responds with `http.Error` (newline-terminated) vs
  catalog's Express-style bodies.
- `adminops`, `notifications`, `sharing` use raw `r.URL.Path`.

Route order is load-bearing (sharing before userapi; `/api/tasks` special-cased
after providers at `runtime.go:323-331`); unmatched → catch-all 501
(`runtime.go:337`). This is Express `router.use` fallthrough transliterated.

**Go-native alternative:** Go 1.22+ `http.ServeMux` with method patterns
(`mux.HandleFunc("GET /api/libraries/{id}", h.library)`) and per-package
`RegisterRoutes(mux)` deletes ~13 hand-rolled switches (~1,500 LOC of routing
boilerplate), the request cloning, and the ordering fragility, with
most-specific-wins precedence covering overlapping-route cases. Wire-visible
behaviour (status codes, bodies, 404/405) is upstream's contract, not Express's
mount mechanics.

- **Benefit:** high. **Risk:** medium-high — precedence semantics differ
  (first-match vs most-specific), trailing-slash and `path.Clean` subtleties,
  public-route bypasses (`isPublicPlayback`, cover/author-image exceptions) must
  be re-expressed as explicit registrations.
- **Required coverage:** smoke every one of the 226 route candidates before and
  after. Currently **0/226 verified**, 50 routes have differential variants, 71
  scenarios pass — this refactor is a hard blocker until route coverage is
  near-complete.

#### S2. Sequelize `toJSON()` reproduced as `map[string]any` end-to-end — ~1,070 occurrences

Per-package counts (grep, all files): catalog 245, userapi 149, scanner 143,
podcasts 123, playback 105, providers 89, sharing 82, identity 59, realtime 40,
notifications 39, adminops 36, server 33, audiotools 25, auth 18, emailapi 18,
store 11, backup 8.

The entire response model is untyped, with per-package coercion-helper
ecosystems (`decodeObject/decodeArray/decodeStrings` `handler.go:777-806` vs
`decodeJSONValue` `metadata.go:192-201`; `nullString` vs `nullDBString`;
`dateMillis` 5-way type switch `handler.go:807-828`;
`millis`/`number`/`floating`/`floatValue` variants), post-hoc key deletion to
emulate ORM field dropping (`handler.go:146-148,162-169,2339-2340`,
`userapi.go:1098-1101,1191`, `identity.go:301-306`), and assertion-driven
request handling — worst case `updateProgress` (`userapi.go:347-488`) with ~30
runtime type assertions on a known request DTO. Timestamps scanned into bare
`any` (`handler.go:32-34`, `userapi.go:1439`, `playback.go:339-343`,
`sessions.go:44`, `listening.go:20`). `backup.Backup` (`backup.go:59-70`) is the
**only typed JSON DTO** in nine media/ops packages.

This forfeits compile-time checking exactly where parity drift is most dangerous
(wrong key, wrong nesting level, wrong unit). Greenfield Go would type the DTOs.
The constraint is real: upstream emits explicit `null`s and byte-compared
responses leave no room for `omitempty` accidents — struct fields need
`*T`/sentinel handling.

**Recommended path:** type **request** DTOs first (input validation improves,
wire risk ~zero), then response DTOs package-by-package starting with catalog's
serializers (`bookJSON`/`podcastJSON`/episode maps at `handler.go:42-428`).

- **Benefit:** highest of any item (est. −3,000 LOC of coercion/assembly across
  phases). **Risk:** high for responses, low for requests.
- **Required coverage:** per-endpoint differential variants for every typed DTO
  — the existing harness already does byte-comparison, which is exactly the
  safety net this needs.

#### S3. Manager-singleton pattern reproduced as constructor + 12 setter injections

`New` + `SetCredentialHooks` / `SetOnlineData` / `SetSessionLookup` /
`SetDispatcher` / `SetItemChanged`×2 / `SetLibraryChanged` /
`SetSettingsChanged` / `SetWatcherUpdate` / `SetLoggerData` /
`SetNotificationHandler` / `SetHTTPClient`, all wired from one composition root
(`runtime.go:98-154`). Post-construction unsynchronized mutation, safe only by
wiring order. Nil-emit guards (`if h.emit != nil`, e.g. `writes.go:139,151,326`)
exist only because **three `catalog.Handler` instances are constructed**:

- `runtime.go:86` — `catalog.New(db, cfg, hub.Emit)` (the real one)
- `userapi.go:47` — `catalog.New(db, cfg, nil)` (hidden second instance, used
  for `ItemJSON`/`ItemMinifiedJSON`)
- `scanner.go:53` — `catalog.New(db, cfg, nil)` (third instance, used for
  `ItemJSON`)

Constructors with side effects: `podcasts.New` starts the cron scheduler and
queries the DB (`podcasts.go:59,918-940`); `backup.New` starts cron
(`backup.go:89-95`).

**Go-native:** pass collaborators as constructor parameters (a small funcs
struct, or narrow consumer-side interfaces), share the one `catalog.Handler`,
make `New` side-effect-free with explicit `Start`/`Close`.

- **Benefit:** removes a whole failure class (nil-emit, unwired fetchers — cf.
  B2, B10). **Risk:** low-medium. **Coverage:** existing package tests + 71-case
  suite.

#### S4. Per-controller packages copied their helpers instead of sharing them

The 1:1 controller→package mapping made each package self-sufficient, producing
the module's worst duplication (all verified):

| Duplicated concern | Copies | Evidence |
|---|---|---|
| `EmitFunc` type (identical signature `func(userID, event string, payload any)`) | 8 named + 4 anonymous | `scanner.go:30`, `playback.go:30`, `userapi.go:32`, `podcasts.go:32`, `tools.go:28`, `backup.go:30`, `providers/handler.go:24`, `catalog/handler.go:988` (unexported); anonymous at `notifications/handler.go:22,25`, `emailapi/handler.go:33`, `sharing/handler.go:32,55`, `adminops/handler.go:30` |
| `writeJSON` (3 different signatures) | ~8 | `server.go:245`, `catalog:714`, `userapi:2462`, `identity:1025`, `playback:2127`, `backup:770`, `providers:357`, `adminops:890`; plus `write`/`jsonResponse` variants in sharing/notifications/emailapi/podcasts |
| `admin`/`isAdmin` | 9 | `providers:122`, `adminops:97`, `hub.go:585`, `podcasts:1210`, `playback:1756`, `emailapi:69`, `notifications:75`, `sharing:90`, `userapi:2145` |
| UUID generation (panics / returns error / silent / `google/uuid`) | 4 | `userapi:2523`, `identity:1009`, `playback:2097` & `podcasts:1084` (hand-rolled), `sharing:1157`, `notifications:381`, `providers/handler.go:194` (library); catalog `makeUUID` returns `""` on failure (`writes.go:391-400`) |
| timestamp layout list + `millis` coercion | 7 | `progress.go:82`, `userapi:2386,2409`, `identity:94-102`, `store:413-419`, `playback:2015`, `sharing:1290`, `podcasts/item.go:252` |
| cron parser construction (`cron.NewParser(Minute|Hour|Dom|Month|Dow)`) | 5 | `podcasts.go:922,1048`, `backup.go:90,154`, `adminops:366` |
| ffmpeg/ffprobe path resolution + exec | 4–5 | `playback:1555-1565,2145`, `tools.go:489-498,531-563,653`, `downloads.go:270-279`, `scanner/files.go:1053`, `catalog/batch_chapters.go:341` — no shared runner, no shared timeout policy, inconsistent stderr handling |
| permission/ACL evaluation | 7 (two divergent — B6) | passim |
| settings-row load/save | 3 | `adminops:109-126`, `notifications:67-74`, `emailapi:80-87`; a 4th hand-rolled in `manager.updateBackupPath` (`manager.go:75-96`) |
| HTML sanitize / strip | 2 + 2 | hand-rolled tokenizer+allowlist `rss.go:265-396` vs `auth.SanitizeHTML`; `stripTags` `playback:1991` vs `stripHTML` `builtin_search.go:792` |
| Levenshtein | 2 | `podcasts.go:590-609` vs `builtin_search.go:810-829` (differ only in lowercasing) |
| `lastFirst` | 2 | `catalog/handler.go:895-901` vs `scanner/files.go:1542-1548` (identical bodies) |
| SQL placeholder helpers | 3 | `placeholders` `batch_chapters.go:543`, `sqlMarks` `library_writes.go:668`, `stringArgs` `:669-675` |
| JSON body decoding | 2 (divergent) | `identity.decodeBody` (typed dest, 1 MiB limit, trailing-data check, `identity.go:864-875`) vs `userapi.decodeBody` (`map[string]any`, `UseNumber`, no size limit, `userapi.go:2434-2446`) |
| Browser-user DTO | 3 | `identity.UserBrowser` (`identity.go:264-308`, confusing `minimal` flag that sets then deletes `mediaProgress`), `userapi.browserUser` (`:2251-2276`), `userapi.minimalBrowserUser` (`:2147-2176`); plus `nullableUserEmail` byte-identical in both packages |
| Progress row DTO | 2 | `progressdto.Read` (`progress.go:13-47`) vs `userapi.scanProgress` (`:2221-2249`) — same SELECT, same map shape, subtly different helpers |
| User row scanning | 3 | `scanUser` (`userapi.go:2122-2136`), `identity.userQuery` (`:821-838`), `store.user` (`store.go:190-237`) — same 14-column SELECT |
| Feed new-episode detection | 2 in one package | `checkNew` (`podcasts.go:484-520`) vs `filterNew` (`:1012-1045`) |
| MIME tables | 3 | `audioMIMEType` (`download.go:24-47`), inline map in `servePath` (`handler.go:2595`), `audioMimeType` (`scanner/files.go:1188-1215`); plus `mimeForFormat`/`mimeForPath`/`mimeForExt` in playback/sharing/podcasts |
| fileType classifiers | 2, **divergent** extension sets | `fileType` (`handler.go:965-980`) vs `fileTypeForExtension` (`files.go:999-1014`) — scanner knows `.oga/.mp4/.bmp/.tif`, catalog doesn't |
| `truthy`/`truth`/`jsTruthy` | 3 names, one function | `batch_chapters.go:533`, `metadata.go:266`, `userapi.go:1205`, `oidc.go:850` |
| Express text writers | 4 variants | `sendExpressText` (`handler.go:736-745`), `writeError` (`:723-732`), `sendTextStatus` (`cover.go:30-32`), `sendDownloadText` (`download.go:152-157`) |
| Date/now writers | 4 | `dbNow` (`writes.go:710`), `nowDB` (`scanner.go:534`), `timeString` (`files.go:977-982`), inline literal (`metadata.go:328`) |
| `writePlain` | 2 | `runtime.go:552`, `identity.go:1037` |
| ereader device filtering | 2, divergent | `filterDevices` (`identity.go:744-769`) vs `updateEReaderDevices` (`me_extra.go:235-249,268-282`) |
| extraData full-table scans | 3 | `identity.userByIDOrOldID` (`:799-819`), `oidc.userByOIDCSub` (`:608-629`), `store.UserByIDOrOldID` (`store.go:148-184`) |
| `/status` payload | 2, **divergent behaviour** | `server.go:94-146` (refuses `authLoginCustomMessage`) vs `runtime.go:386-424` (sanitizes it) |
| authenticate | 3, divergent | `identity.Authenticate` (`:214-250`), `server.go:203-229` (rejects api-key types identity accepts), realtime unverified payload sniff (`hub.go:664-682`) |
| Pagination parsers | 2, different tuple orders | `pagination` `(page, limit)` (`handler.go:698-712`) vs `authorPagination` `(limit, page, bool)` (`:1812-1826`) — cf. B1 |
| Scanner grouping/discovery algorithm | 3 | production (`scanner.go:700-770`), simplified test-only `groupKey` (`files.go:65-85`), test clone `discoverGroupsForTest` (`scanner_test.go:1495-1576`) |
| `mergeBookMetadata` vs `mergeBookMetadataWithABS` | 2, former test-only | `files.go:564-590` vs `:592-618` |

Four leaf packages — `internal/httputil` (Express-exact writers),
`internal/dbtime` (one layout list + one write format), `internal/events`
(`EmitFunc`), `internal/authz` (permission evaluation, after B6
characterization) — conservatively delete **1,500–2,000 lines**. `progressdto`
is the in-repo precedent that this pattern works (extracted so `identity`
needn't import `catalog`, `progress.go:1-2`).

- **Risk:** low for mechanical moves (byte-identical outputs preserved); authz
  and dbtime consolidations are behaviour-critical and gate on B5/B6
  characterization.

#### S5. HTTP self-dispatch through `httptest` in production code

- `library_writes.go:399-408` fabricates an `httptest` recorder + request +
  **forged user with injected permissions**
  (`cleanupUser.Permissions = json.RawMessage(`{"accessAllLibraries":true,"delete":true}`)`)
  to reuse `deleteItem` from `updateLibrary`.
- `podcasts.go:862-867` invokes `h.createPodcast` via a fake request/recorder
  during OPML import, string-sniffing the response body for "already exists"
  (`:874`).

Extract the inner logic into plain functions both the HTTP handler and the
caller invoke. Also removes `net/http/httptest` from production imports (2
packages: `library_writes.go:9`, `podcasts.go:16`).

- **Risk:** low. **Coverage:** existing mutation tests + 2 differential cases.

#### S6. Read-only `server.Handler` vs `Runtime` duplication

Two `/status` implementations with **divergent behaviour** (`server.go:94-146`
refuses `authLoginCustomMessage`; `runtime.go:386-424` sanitizes it), duplicated
`/ping` (`server.go:63` vs `runtime.go:249`) and `/healthcheck`
(`server.go:65-71` vs `runtime.go:253-261`), and a second `authenticate` with
*different* API-key semantics (`server.go:203-229` rejects keys
`identity.Authenticate` accepts; `auth.ValidateBearer`'s sole non-test consumer
is this legacy path). Schema validation is done three times at startup
(`bootstrap.go:117-125` migration check, `store.validateSchema`
`store.go:97-118`, `identity.New` column checks `identity.go:118-128`) —
different granularities, defensible but overlapping.

Either fold the read-only surface into `Runtime` behind the existing
`ABS_GO_READ_ONLY` flag, or delete the divergent copies deliberately.

- **Risk:** medium. **Coverage:** RO-mode smoke.

#### S7. Concurrency / lifecycle smells

- **Scanner:** one `sync.Mutex` (`scanner.go:42`) guarding three unrelated
  concerns (`runs`, `watchCancel`, `podcastFeedFetcher`); lock/unlock sequences
  copy-pasted 7×
  (`scanner.go:76-84,158-165,170-173,215-224,351-358,464-474,479-484`); cron
  callback spawns a **nested** goroutine (`scanner.go:402` — robfig/cron already
  runs each job in its own goroutine); fire-and-forget scans on
  `context.Background()` (`:266-273,309-315`) with discarded results; `Close`
  doesn't wait for in-flight scans; `Refresh` is blunt cancel+full-`Start`
  (`:478-486`); `UpdateWatchedPath` validates `kind`/`oldPath` then ignores
  them, always running full `ScanForce` (`:179-212`).
- **Realtime:** `Emit` holds `RLock` across blocking socket writes
  (`hub.go:361-382`) — see Pass 1 notes; `Close` doesn't drain `h.clients`
  (leaked per-Runtime on restore until GC).
- **Playback:** `transcode.checkSegment` spawns a goroutine that sleeps 100ms
  then mutates `t.startTime`/`t.segmentStart`/calls `t.start` outside the mutex
  (`playback.go:1629-1641`); `Close` doesn't wait for ffmpeg children despite
  the "waits briefly" comment; sessions map is in-memory only.
- **Audiotools:** one goroutine per task, `Close` cancels contexts but doesn't
  wait (`tools.go:171-180`); queue recursion via goroutine tail-call (`:457`).
- **Notifications/emailapi/adminops:** settings read-modify-write with
  **no locking** (`notifications:56-118,268-302`; `emailapi:70-130`;
  `adminops:98-181`) — concurrent admin requests lose updates; notifications
  creates a fresh `http.Client` per delivery (`:315`).
- **Runtime:** per-request settings SELECT for the CSP `frame-ancestors` header
  on **every** HTTP request (`runtime.go:243 → 425-431`, on
  `context.Background()`), despite a `settingsChanged` hook existing that could
  invalidate a cache; `Manager.ServeHTTP` uses `Mutex` where `RWMutex` would do
  (`manager.go:24,50`, minor).
- **Podcasts:** lock/unlock/relock dance in `enqueue` (`downloads.go:84-94`);
  `runScheduled` issues `db.Exec` with no context (`:974,987,1009`);
  DNS-rebinding TOCTOU — resolve+dial then re-resolve on redirect
  (`podcasts.go:1142-1167` vs `:1179-1201`; `downloads.go:184-206`).

#### S8. Dead and inflated surface (deadcode + staticcheck verified)

`deadcode@latest ./...` — 19 unreachable functions (note: deadcode flags
test-only reachability; removals need one grep per symbol — already performed
for the load-bearing ones):

| Symbol | Location | Note |
|---|---|---|
| `queryStringOrNil` | `catalog/handler.go:981` | zero callers |
| `Load` | `config/config.go:29` | `LoadArgs` is the used entry |
| `mediaItemID`, `mediaItemType`, `progressRatio`, `writeJSONStatus` | `playback/playback.go:1429,1435,1441,2144` | |
| `nullableStringValue` | `podcasts/item.go:246` | |
| `FindEpisodeMatches` | `podcasts/podcasts.go:564` | exported "for scanner integration"; no caller exists |
| `decodeJSON` | `podcasts/rss.go:424` | |
| `numberString` | `providers/handler.go:366` | |
| `groupKey`, `mergeBookMetadata`, `trackNumbersFromFilename`, `parseSequence` | `scanner/files.go:65,564,1224,1586` | test-only |
| `NewRuntime` | `server/runtime.go:59` | exported constructor, zero callers (`NewManager`→`newRuntime` used) |
| `titleSafe` | `sharing/handler.go:1385` | |
| `BrowserUser`, `Handler.unsupported`, `nullableString` | `userapi/userapi.go:2096,2204,2427` | |

Additional dead/inflated surface:

- `store.UserByUsername` (`store.go:186`) — production-dead, test-only;
  `store.UserByID` exported but only self-used.
- `const BuildNumber = 1` (`server.go:21`) — no references.
- `transcode.furthest` field (`playback.go:106`) never assigned — the "client
  skipped >10 segments" reset path at `:1617` is half-dead.
- `auth.ErrNotYetValid` (`token.go:26`) — produced but no production consumer
  inspects it.
- `userapi.ProgressRows` (`:2090`) — exported, only test-reachable; production
  uses unexported `progressRows`.
- `identity.UserBrowser`/`Authorize` — exported but only consumed inside the
  package; should be unexported.
- `podcasts.SetHTTPClientForTests` (`:1229`) — test-only export that also flips
  `allowPrivateNetwork`: a production footgun.
- `providers.SetHTTPClient` (`handler.go:43`) — no callers.
- `dbOrNil` nil-branch unreachable (`manager.go:288-293`); `resumeOld` discards
  its parameters (`manager.go:195-196`).
- staticcheck SA4006 unused values: `personalized.go:99` (key, label),
  `personalized.go:140` (queryArgs), `writes.go:84` (columns), `scanner.go:723`
  (keyRel), `files.go:195` (chapters), `scanner_test.go:274` (prior).
- Dead branches/locals: `if body == "" { body = "" }` (`userapi.go:2476-2478`);
  `_ = oldDuration` (`:414`); `_ = permissions` (`identity.go:763`); dead
  `[]byte` assertion (`userapi.go:2038`, see B7); `_ = from`
  (`emailapi:256-261`); `if meta, ok := …; ok { _ = meta }`
  (`playback.go:668-671`); `_ = mediaType` (`handler.go:694`); `_ = year`
  (`files.go:1579`); `var _ = filepath.Separator` (`files.go:1650`);
  `var _ = strconv.IntSize` (`custom_search.go:215`).
- test-only dead code in scanner: `groupKey`, `trackNumbersFromFilename`,
  `mergeBookMetadata` (production uses `…WithABS`), plus the third copy of the
  discovery algorithm in `scanner_test.go`.

#### S9. Hygiene

- `go.mod` marks **every** dependency `// indirect` despite direct imports
  (`fsnotify`, `robfig/cron`, `gorilla/websocket`, `coreos/go-oidc`,
  `zishang520/socket.io`, `golang.org/x/crypto`, …) — `go mod tidy` never run.
- Stale package comment: `catalog` claims "read-only catalog API endpoints"
  (`handler.go:1-2`) and `New`'s doc repeats "read-only catalog handler"
  (`:999-1000`) — false since
  `writes.go`/`library_writes.go`/`cover.go`/`batch_chapters.go`.
- Compiled Mach-O binaries `audiobookshelf-go` and `abs-compat` in the repo root
  (gitignored via `.gitignore:3-4`, but present in the working tree).
- Two same-named `titleIgnorePrefix` functions with **different semantics**:
  `catalog/handler.go:877-888` appends `", The"` (upstream
  `getTitlePrefixAtEnd`); `scanner/files.go:492-499` strips (upstream
  `getTitleIgnorePrefix`). Upstream deliberately has both; the Go pair preserves
  both behaviours under one ambiguous name — a latent divergence hazard. Rename
  both distinctly.
- TS-style patterns beyond routing: `bookPathMetadata`'s variadic-optional bool
  (`files.go:1549,1580`); `map[bool]string{…}[x]` inline branch tables
  (`personalized.go:159`, `playback.go:1312`); manual JSON string assembly with
  `escapeJSONString` (`cover.go:297,341-343`); `sameBytes` via string conversion
  instead of `bytes.Equal` (`files.go:1541`); TS naming kept in DTOs
  (`oldJSON`/`legacyJSON`/`expandedJSON`, `minifiedFeed`/`oldFeed`/`metaFeed`,
  `clientJSON`/`publicJSON`, i18n `TitleKey`/`MessageTask…` keys).
- `providers` error swallowing: every upstream HTTP failure becomes
  `return []map[string]any{}, nil`
  (`builtin_search.go:89-91,135-137,169-171,228-230,444-446`;
  `custom_search.go:58-69`) — callers cannot distinguish "no results" from
  "provider down".
- 25-placeholder INSERT with stray space `?, ?,?)` (`podcasts.go:773`); manual
  insertion-slice hack `args[:len(args)-1]` (`tools.go:557`).
- Env parsing scattered past `config`: `BACKUP_PATH` (`runtime.go:159,193,201`,
  `bootstrap.go:181`), `ALLOW_IFRAME` (`runtime.go:430`, `bootstrap.go:187`),
  token TTLs and rate-limit envs (`identity.go:131-136`); `ABS_GO_READ_ONLY`
  parsed twice (`config.go:103`, `bootstrap.go:57`). Also time-unit ambiguity
  across envs: `ACCESS_TOKEN_EXPIRY` parsed as **seconds**
  (`identity.go:131,913-919`) while `RATE_LIMIT_AUTH_WINDOW` is parsed as
  **milliseconds** (`identity.go:135`).

### Boilerplate estimates (measured)

- `catalog/handler.go` (2,803 lines): routing ~180; coercion/response helpers
  ~290; media serializers ~350; SQL filter/sort builders ~270; route handlers
  ~1,600 — within which `writeError` ×113, the literal `"database read failed"`
  ×66, `rows.Close()` ×36, `dateMillis(` ×25, per-route `userCanAccessLibrary`
  preamble ×12. **~60% mechanical boilerplate.**
- `userapi/userapi.go` (2,532 lines): route switch ~50; response helpers ~70;
  coercion helpers ~230; duplicate JWT signer ~30; DTO builders ~180; ~131
  `http.Error` + 76 `sendStatus` call sites. **~35–40% boilerplate.**

### What's already right

- Single composition root (`runtime.go`) with **no import cycles**; feature
  packages never import each other except `scanner→catalog` and
  `userapi→catalog`.
- Exactly **one** interface in the whole module (`server.Repository`,
  `server.go:23-28`) — no interface proliferation. (It is implementer-shaped: 4
  methods, `Runtime` consumes only `ServerSettings` — narrow it, don't add
  more.)
- Typed config with boundary validation (port range, positional rejection,
  absolute paths, base-path normalization matching `Server.js:55-63`).
- Fail-closed schema verification posture (per AGENTS.md rule 10): fresh-init
  creation is exclusive-create race-safe (`bootstrap.go:75-91`), existing DBs
  must match the pinned migration version.
- `CGO_ENABLED=0` with `modernc.org/sqlite`; FFmpeg/ffprobe as subprocesses per
  the contract.
- `store` is a thin open/pragma/schema boundary, not a repository framework —
  keep it that way. Transaction ownership is per-request in handlers (24
  `BeginTx` sites, all in feature packages).
- `Manager` hot-restore with rollback (`manager.go:98-218`) is genuinely
  well-designed and has **no TS counterpart** (upstream restarts the process) —
  the sole justification for `Manager`/`Runtime` existing as separate types, and
  a good one.
- The parity harness with provenance tracking
  (`contracts/parity/*-provenance.json`) is exactly the safety net the
  higher-risk items need.
- Express wire quirks (weak ETags `cover.go:41-52`, no-newline text bodies
  `handler.go:723-745`, English `sendStatus`, chunked streaming) are
  contractually required and correctly isolated in the writers.
- `progressdto` demonstrates the right extraction pattern; `auth`'s boundary
  (stateless verify + sanitize, no DB) is the cleanest in the module — modulo
  `SanitizeHTML`'s placement (a text utility consumed by 6 packages, not an auth
  concern) and the missing issuing half (B7).

---

## Ranked proposals (benefit × risk × required parity coverage)

| # | Change | Benefit | Risk | Parity coverage required first |
|---|---|---|---|---|
| 1 | Fix series pagination swap (B1) | Correctness | trivial | 1 new differential case (paginated series) |
| 2 | Wire `scanner.SetPodcastFeedFetcher(podcasts.FetchFeedEpisodes)` (B2) | Revives a dead upstream feature; deletes 2 dead exports | trivial | 1 differential case (match-episodes) |
| 3 | Add `podcasts.Close()` to `Runtime.Close` (B3) | Stops cron/queue leak on every restore | trivial | restore test asserting scheduler stop |
| 4 | Fix downloads task-state race (B4) | Correctness under `-race` | low | concurrent download+poll `-race` test |
| 5 | Delete verified dead code (S8, ~19 funcs + dead branches) | −400–600 LOC, honest API surface | very low | test-only reachability re-check; existing suite |
| 6 | `go mod tidy`; fix stale comments; disambiguate `titleIgnorePrefix` names (S9) | Hygiene | trivial | none |
| 7 | Leaf packages: `events` (EmitFunc), `httputil`, `dbtime` (after B5), one UUID impl (S4, B7) | −1,500–2,000 LOC; kills B5/B7 classes | low | existing 71-case suite + package tests per move; dbtime needs DB-content differential |
| 8 | Share one `catalog.Handler`; constructor-inject collaborators, drop 12 setters + nil-guards (S3) | −100 LOC, removes a whole failure class | low-medium | catalog/userapi/scanner tests + 71-case suite |
| 9 | Extract plain functions from the 2 `httptest` self-dispatch sites (S5) | Removes forged-user control flow | low | 2 mutation differential cases |
| 10 | Wire backup→notifications or delete (B10); rate-bucket pruning + session reap (B8) | Upstream-visible behaviour | medium | backup-event differential cases |
| 11 | `realtime.Emit` snapshot pattern; settings cache with hook invalidation (S7) | Contention + per-request query removal | low-medium | realtime suite + `-race` |
| 12 | Resolve `/status` + RO-auth duplication (S6) | One definition of "valid credential" | medium | RO-mode differential smoke |
| 13 | Unify DATETIME write format (B5) — after item 7's dbtime exists | DB parity | medium | DB-content differential on affected flows |
| 14 | Characterize + consolidate authz (B6, S4) | Kills the divergent-permission class | **high** | permission-matrix differential per affected route — largest test investment here |
| 15 | Type **request** DTOs, starting with `updateProgress` (S2 phase 1) | Input validation, kills assertion-panic class | low-medium | mutation differential cases per typed endpoint |
| 16 | Type **response** DTOs package-by-package, catalog first (S2 phase 2) | Largest single simplification (−~3,000 LOC of coercion/assembly) | **high** | per-endpoint byte-exact differential variants; the 50-route variant set must grow substantially |
| 17 | Replace the 13-probe `Handle` bool-chain with `http.ServeMux` + `RegisterRoutes` (S1) | −~1,500 LOC routing boilerplate, no request cloning, no order fragility | medium-high | **near-complete route smoke — 0/226 verified today is a hard blocker** + method/404/405 variants |

**Sequencing:** 1–6 are immediate (days; no coverage prerequisites beyond a
handful of new differential cases). 7–12 are safe once the current 71-case suite
is re-run per step. 13–16 are gated on targeted differential growth. 17 is gated
on route-coverage completeness — doing it at 0/226 verified would be
unverifiable by the harness's own admission.

---

## Anti-recommendations

- **Do not** add repositories/services/DI frameworks or a "domain layer" —
  `store` as a thin boundary is correct for this system; transaction ownership
  in handlers is explicit and works.
- **Do not** merge or split the feature packages: the per-controller boundaries
  are navigable and parity-citable (file headers reference upstream line
  numbers) — the *copied helpers* are the problem, not the boundaries.
- **Do not** bulk-convert `map[string]any` without per-route differential
  evidence (S2's phasing exists precisely because the harness reports 0/226
  verified).
- **Do not** "fix" Express quirks — weak ETags, text bodies without trailing
  newlines, English `sendStatus` texts, passwordless root
  (`identity.go:338,1072-1073`), `oldUserId` full-table scans, and email-NULL
  semantics are contract, not taste.
- **Do not** unify the two `titleIgnorePrefix` semantics into one function —
  upstream deliberately has two; rename them distinctly instead (S9).
- **Do not** assume the 7 ACL implementations should be identical — characterize
  each against upstream first (B6); they may legitimately differ per endpoint.

---

## Evidence appendix

### Tooling

- `CGO_ENABLED=0 go build ./...` — exit 0. `CGO_ENABLED=0 go vet ./...` — exit
  0.
- Last recorded full test run (`final-go-tests.txt`): all 21 test packages `ok`
  (incl. race-detector snapshot for identity, playback, realtime, scanner,
  server per `STATUS.md`).
- `staticcheck@latest ./...` (isolated GOMODCACHE/GOCACHE under the session temp
  dir): ~90 findings, dominated by intentional Express-parity idioms (numeric
  status codes ST1013, capitalized error strings ST1005 — deliberate for wire
  parity); signal items are the U1000/SA4006 findings listed in S8, plus
  `S1017`/`S1011` simplifications.
- `deadcode@latest ./...`: 19 unreachable functions (S8 table). Caveat: flags
  test-only reachability; one grep per symbol was performed for the load-bearing
  ones (B2, `NewRuntime`, `config.Load`, `FindEpisodeMatches`).
- `staticcheck@2025.1` failed to run against the Go 1.27 toolchain (export-data
  version mismatch) — `@latest` was used instead.

### Parity posture (`contracts/parity/`)

- `full-suite-report.json`: 71/71 differential scenarios passing against the
  real upstream executable (isolated per-server media/config/metadata trees).
- `route-coverage.json` counts: 226 source-inventory route candidates;
  **0 compatibility-verified**; 225 implemented-not-differentially-verified; 1
  not implemented; 50 routes with differential variants; 68 declared variants,
  22 sampled passing.
- Harness self-declared limitations: source-syntax candidates, not resolved
  Express routes; a passing case samples only its listed request shape and
  fixture state; a generic Go 501 is not evidence of upstream behaviour.

### Module facts

- Module `audiobookshelf-compat`, `go 1.24.0` directive, built with Go 1.27.0
  darwin/arm64.
- 82 `.go` files, 37,836 lines (incl. tests). Largest files:
  `catalog/handler.go` 2,803; `userapi/userapi.go` 2,532; `playback/playback.go`
  2,154; `scanner/scanner_test.go` 1,721; `scanner/files.go` 1,650.
- Internal dependency graph (non-test): `server` → 17 packages (sole composition
  root); `scanner → catalog`; `userapi → catalog`;
  `identity → auth, progressdto, contracts/parity`; leaf packages `store`,
  `config`, `auth`, `progressdto`, `compat` have no internal deps. No cycles.
- Exactly one interface type in the module (`server.Repository`); everything
  else is concrete structs + func-value injection.
- `map[string]any` occurrences: ≈1,070 across 17 packages.
