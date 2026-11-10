> Archived completed research (2026-11-11). Candidate proofs remain open in [ROADMAP.md](../../ROADMAP.md); this report is evidence, not adoption approval.

# Whole-subsystem replacement audit — revised boundaries

2026-11-11. Runtime source snapshot: **accbd563**. All 57 internal Go package directories reclassified. The working tree already contains the owner's clarification in five documentation files; this audit does not modify them or runtime source. This report **supersedes the conservative recommendations** in the earlier module-by-module audit. Counts reuse its same unchanged runtime-source snapshot.

## Governing boundary and changed result

Only persisted-data/database/rollback and client contracts are fixed product boundaries. Current module names, row structs, SQL/query shape, helper behavior, controller organization, protocol engine, process model, queue implementation and internal TS emulation are all replaceable. A library does not need to match our current methods; a new architecture may delete several layers at once.

A source comment saying “like TypeScript/Sequelize/Express” is not enough to make behavior mandatory. Classify each proposed adapter:

1. **Proven data/client obligation:** retain the outcome, using any implementation.
2. **Incidental implementation/representation:** remove or redesign freely.
3. **Unresolved observability:** examine clients, stored data and request/state tests; do not freeze it automatically or silently assume it can be removed.

Actual field/type/meaning/nullability, authorization, valid accepted workflows, relevant errors, cache/range semantics and transaction effects remain contracts. Raw JSON order, classes, internal helpers, identical parsing algorithm and custom transport implementation are not contracts by themselves. Security properties such as safe path containment and authorization remain outcomes to prove, not reasons to retain a particular custom implementation.

**Architecture verdict: approve bounded replacement feasibility pilots; do not claim a dependency adoption or measured savings yet.** The strongest new directions are:

| Priority | Replacement design | Cross-module consolidation | Decision |
|---|---|---|---|
| 1 | Library-owned Socket.IO/Engine.IO transport | realtime framing, sockets/acks, connection bookkeeping and server mounts | Reopen; first decisive protocol prototype |
| 2 | Bob canonical persistence layer; Bun as alternative | sqlupdate, duplicate store/entity records, row scanners/loaders, settings CRUD and query binding | Largest architectural reduction hypothesis |
| 3 | Typed HTTP edge with oapi-codegen or Huma | repeated route binding/body decoding/request/response types across feature handlers | Reopen; separate bounded vertical slice, not auto-CRUD |
| 4 | One media-job engine using afmpeg/native backend | mediatools, audiotools media pipeline, playback transcode, scanner/podcast probing | Reopen whole-engine experiment; higher feasibility risk |
| 5 | go-mail delivery operation | SMTP/auth/TLS, MIME/base64, attachment staging and transport lifetime | Strongest small external adoption case |
| 6 | bluemonday content policy | textclean sanitizer algorithm and metadata sanitation callers | Retain facade only if useful; no byte-emulation wrapper by default |
| 7 | One archive service | backup and private/public download ZIP machinery | Compare mholt/archives against a narrow stdlib implementation |
| 8 | slog-based record pipeline | custom logger, multi-sink writes, file rotation; client adapter remains | Newly eligible redesign, medium confidence |

These opportunities overlap. A Bob pilot and HTTP generation cannot both count deletion of the same request/model plumbing. A media engine and archive/log pipeline similarly share lifecycle work. No total net LOC estimate or thousand-line reduction is established by this source audit.

## 1. Replace the transport, rather than wrap the existing transport

**Candidate:** `github.com/zishang520/socket.io/servers/socket/v3`, initially test v3.0.6. [Project](https://github.com/zishang520/socket.io), [release](https://github.com/zishang520/socket.io/releases/tag/v3.0.6), [module dependencies](https://raw.githubusercontent.com/zishang520/socket.io/main/servers/socket/go.mod).

The release explicitly addresses early CONNECT/listener-registration races, handler initialization and polling response ownership. Local historical C25 evidence records an early-auth failure against v2.3.8 (51/200 immediate-auth connections); that is grounds for testing the newer version, not for permanently excluding a library or assuming the defect is solved.

**Delete candidates:** realtime/eio.go **239 lines**, sio.go **228 lines**, plus transport-specific parts of hub.go and mount glue. The **467 protocol lines are gross footprint**, not net savings. Use native library sockets/acks/rooms; do not rebuild our Socket/conn engine underneath a library wrapper.

**Small application shell:** post-CONNECT auth event; token/active-user checks; auth_failed/init shapes and order; lastSeen/updatedAt persisted before init; online/offline/session responses; action ACLs; per-user item/library/tag/explicit filtering; log listener cleanup; backend lookup through restore; ack argument shapes. Rooms can replace simple routing but not individualized item filtering.

**Discard incidental restrictions:** custom packet encoder/parser, 256-message queue, five-second writer deadline and WebSocket-only/polling-400 behavior need not survive merely because tests assert them. Preserve compatible flows and bounded resources; the implementation may support additional standard transports.

**Decisive proof:** existing 200 sequential plus 10×20 concurrent immediate-auth connections on both legacy/prefixed paths, then reauth/reconnect, invalid/expired/API-key auth, init/persistence ordering, acks/action ACLs, filtered recipients, payload/slow-consumer limits and close/restore. Inspect latest queue/listener ordering: library release notes are not proof of the exact ABS case. Reject if it needs a fork or recreates the deleted engine. Old googollee library's documented client-1.4 protocol is unsuitable; a generic WebSocket library alone does not provide Socket.IO.

## 2. One generated persistence model, not several similar row models

**Preferred prototype: Bob**, with **Bun as a competing design**, not additional layers on top. [Bob generator](https://bob.stephenafamo.com/docs/code-generation/intro/), [optional setter/table API](https://bob.stephenafamo.com/docs/models/table/), [Bun updates](https://bun.uptrace.dev/guide/query-update.html), [Bun transactions](https://bun.uptrace.dev/guide/transactions.html).

Generate canonical records/setters/query APIs from a disposable copy of the pinned SQLite schema. Use records directly in application operations and attach relations for aggregates. Keep codecs for exact stored DATETIME/JSON/nullable meaning and genuinely different HTTP/event views. Authenticated user data must never be serialized from a credential-bearing DB record as an endpoint response.

**Can disappear or consolidate:**

- sqlupdate's entire **70-line** generic changed-column package;
- catalog storedRow/rowColumns/columnSet SQL emulation and string-key update construction;
- duplicate store.User/UserFromRow, AuthorRecord/ScanAuthor, SeriesRecord/LoadSeries and session-row storage layers where equivalent generated records suffice;
- bespoke row-scanning frameworks in items/load.go (**118 lines**), books/load.go (**81**) and podcasts/read/load.go (**about 98**), keeping only necessary relation/order/projection logic;
- settingsdoc's separate persistence wrapper into ordinary generated row access plus codecs;
- playlistview/store playlist row plumbing into one list operation/projection owner;
- empty DB marker and compatibility forwarding layers that no longer have consumers.

These are target implementation envelopes, not promised deletions. Compare complete replacement code including authored SQL, model tags, generator config, codecs, normalization and adapters. Generated sqlc output is not handwritten savings. Existing sqlc is also eligible to win the same slice: it is not intrinsically protected or intrinsically obsolete.

Bob optional setters can distinguish deliberately present zero values from omission. Normalize a request once into a typed command/setter; determine observable no-op/timestamp/event outcomes once. There is no requirement to reproduce Sequelize's changed-field classes or retain Change/Force methods. All participating operations receive the same transaction; ORM hooks do not get to publish effects before its commit.

**Decisive prototype:** author + series read/update/storage, covering both existing sqlupdate consumers, followed by a batched book read to show the library replaces a framework rather than one 70-line helper. Prove absent/null/empty/zero/false, equal/forced updates, ordering/query count, transaction rollback/no effects and TS reopening Go-written rows. Compare Bob against Bun or a sqlc-only implementation on that same workload; retain one selected implementation in the shipping tree.

## 3. Generate or framework-own the HTTP edge

My earlier blanket rejection of HTTP CRUD frameworks was too broad. The useful design is **typed endpoint adapters around application operations**, not a universal service exposing database tables.

- [oapi-codegen](https://github.com/oapi-codegen/oapi-codegen) can generate request/response types, parameters and strict server adapters using net/http. Strict server generation does not by itself provide all input validation; count required validation/middleware too.
- [Huma validation](https://huma.rocks/features/request-validation/) and [custom response errors](https://huma.rocks/features/response-errors/) provide typed parsing/validation/serialization with customizable error model/content type. Defaults such as validation statuses/problem JSON are not automatically ABS-compatible.

This can replace many repeated decoding/validation/response loops and handwritten transport DTOs across library, people, userapi, adminops, lists and settings endpoints. Domain operations can shrink as boundary coercion stops leaking through their core. Packages may merge or disappear; a global wire package is not required.

**Actual current blocker:** docs/openapi.json is OpenAPI 3.0.0 with **31 paths and 91 schemas**, not a complete characterization of the server. Do not generate from it and declare compatibility, and do not spend months rebuilding the entire spec before demonstrating value.

**Decisive prototype:** one read endpoint, one nullable PATCH and representative unauthorized/invalid/not-found errors. Verify route/auth/error precedence, response status/content type/body shape, field/null/absence and size handling. Measure authored schema/config/custom error adapters as well as Go deleted. If framework configuration becomes a parallel decoder, use generated types/server glue only or retain a small direct endpoint; no library is entitled to win.

## 4. One media engine across five current owners

**Candidate:** [afmpeg](https://afmpeg.phpboyscout.uk/) with its ffmpeg-wasi engine and optional native backend; [go-astiav](https://github.com/asticode/go-astiav) is a lower-level native alternative. Pure-Go/subprocess preferences are not product-contract reasons to reject a better replacement. This is an architecture experiment, not an automatic build-contract change.

Afmpeg documents typed commands/jobs/probing, metadata/chapters, stream copy, progress and cancellation, WASM runtime and a native subprocess backend. The common engine could own probing, cover conversion, embedding/merge and HLS creation, so these stop being scattered process-specific implementations.

**Candidate source envelope, approximately 500 gross lines:** mediatools 136; audiotools embed/merge/metadata block about 158; playback transcode setup/playlist/process about 121; scanner probe mapping about 71; podcast probe bridge about 13. These slices contain some necessary policy and are not all deletable. No throughput/net-code claim has been tested.

**Replace:** process-specific command/concat/FFmetadata/escaping/error/cancellation plumbing; duplicated probe execution and some polling/progress mechanics. **Retain outcomes:** input/output codecs/defaults, stored AudioFile/chapter/tag meaning, exposed full ffprobe JSON, safe backup/install behavior, client task/events and HLS URL/segment readiness/seek/reset behavior.

**Decisive proof:** full required-format probe including AMR/AMR-WB; complete exposed probe response; long-audiobook AAC throughput and memory; native amd64/arm64 delivery; one metadata/chapter/cover remux; backward seek/reset with concurrent segment requests. Published codec/platform capability is evidence to investigate, not compatibility. Default WASM module acquisition is a separate runtime artifact; native mode is a subprocess backend, not static libav. Count packaging/code/codec gaps. A simple ffmpeg-go command DSL remains a weaker option because changing argument syntax alone does not remove lifecycle/processing mechanisms.

## 5. Mail, sanitization and archive engines remain worthwhile, with smaller shells

**go-mail:** replace the complete mail operation, not just smtpConnect. [Capabilities](https://github.com/wneessen/go-mail), [context sending](https://pkg.go.dev/github.com/wneessen/go-mail#Client.DialAndSendWithContext). Delete the 111 gross lines in emailapi/handler.go:229-339 and replace whole-file/base64 staging with attachment readers/files. Keep supported saved settings, e-reader permission/selection, endpoint results and SMTP behavior. Its operation can be called by other owners; translating arbitrary persisted Apprise targets into SMTP is not an automatic notifications replacement.

**bluemonday:** replace the 236-line textclean security algorithm with configured library policy. [Policy API](https://github.com/microcosm-cc/bluemonday). Preserve safe rendered/stored content and meaningful links/markup, not exact token-stack/escaping quirks. Do not surround the library with another sanitizer solely to reproduce formatting. Test genuine metadata/client/security counterexamples.

**One archive manifest/stream service:** backup + public/private downloads can discard their separate tree walkers/readers/writers. Identified helper spans total roughly **190 gross lines**; outer operation mechanics may also shrink. [mholt/archives](https://github.com/mholt/archives) versus stdlib archive/zip. Fixed exported backup ZIP layout and safe snapshot/restore installation are real data contracts, so Restic/Kopia is not a turnkey replacement. Preserve that format through whichever engine is simpler. Compression/entry naming/path guards become explicit service policies rather than copied handlers. Count codecs and extraction safety adapters before selecting the external package.

## 6. Reopen logging as a standard pipeline

Current logging is **724 gross lines**: logger.go 240, files.go 361 and 123 lines of levels/DTOs/HTTP adapters. The prior audit incorrectly treated the existing logger/file-store classes as reasons not to replace them.

A fresh design can use **slog records + one sink pipeline**, with a rotating file writer and a thin ABS history/socket projection. [slog handler model](https://github.com/golang/example/blob/master/slog-handler-guide/guide.md), [slog-multi fanout](https://github.com/samber/slog-multi), [Timberjack time/scheduled rotation](https://github.com/DeRuina/timberjack). slog itself may need less code than a new multi-sink dependency for three fixed destinations; measure both. Archived file-rotatelogs is not the recommended new dependency.

The web client reads timestamp/message/source/levelName/numeric level and GET /api/logger-data history; preserve those and per-socket filtering. Console byte formatting, Logger wrappers, its locking strategy, pending/tail arrays and malformed-file rewrite algorithm are not intrinsically fixed. A single canonical record and retained-history owner can replace several independently maintained representations.

Before changing disk layout, establish which logs/files are persisted rollback or client-history contracts. Maintain a legacy reader if needed; don't silently discard existing history. Timberjack's default rotated naming/retention is not automatically the current date-file behavior. A custom compatibility sink may erase savings, so this is medium confidence rather than a promised 724-line deletion.

## 7. Smaller modules and remaining exclusions, reconsidered

Names/IDs/coercions need not stay as literal TS ports. Existing google/uuid can replace ids plus the notification duplicate; a UA parser can own common device recognition; JSON conversions can live once at the edge; name parsing can use a shorter/library implementation if stored name interpretation and client display/search/order remain correct. Broader recognition and non-observable formatting differences are not automatically compatibility breaks.

A Goja runtime is now eligible in principle, but embedding the same handwritten JS algorithms does not delete them and adds runtime/concurrency/assets. No smaller total implementation has been demonstrated. Likewise, imported cache/job/DI frameworks are not prohibited, but no external library currently demonstrates a better complete implementation than the bounded designs above.

Notifications already delegates multi-provider delivery to Apprise; Shoutrrr requires compatibility for saved Apprise targets/server behavior and replaces only a small current HTTP delivery block unless more of the subsystem is proven replaceable. Existing gofeed/cron/fsnotify are not protected from replacement; they simply already own their underlying mechanisms and no superior reduction case was found in this pass.

## Reclassified package map

All counts are unchanged-snapshot physical non-test lines including comments/blanks, not ratchet or net-savings counts. db/sqlc is generated; contracttest packages are verification support. Labels describe replaceable designs and required outcomes, not protected current code.

| Package | Gross LOC | Replacement family | Revised disposition |
|---|---:|---|---|
| [`access`](../../internal/access) | 244 | Application boundary | Replace its representation freely; retain authorization/visibility and required SQL filtering. A generated predicate/typed permission model can replace the current helper package. |
| [`adminops`](../../internal/adminops) | 908 | Persistence + typed HTTP | Rewrite settings/taxonomy/statistics handlers around canonical records and typed operations; generated decode/encode may replace edge loops. Keep supported admin workflows and authorization. |
| [`audiotools`](../../internal/audiotools) | 576 | Unified media engine | Replace embed/merge/metadata encoding and process/job mechanics with one engine used by playback/scanning too. Keep request/result and media-install/backup contracts. |
| [`auth`](../../internal/auth) | 189 | Identity consolidation | JWT is already externalized. Merge adapters into a coherent identity boundary if that deletes duplication; claims/validation semantics, not this package API, are fixed. |
| [`backup`](../../internal/backup) | 729 | Archive service + persistence | Use one manifest-based ZIP engine also serving downloads. Redesign scheduling/staging mechanics; exported backup format and safe successful restore/rollback remain. |
| [`books`](../../internal/books) | 336 | Canonical persistence + projection | Generated rows/relations can replace book row copies and bespoke loaders. Keep only aggregates and client projections that differ semantically from records. |
| [`bootstrap`](../../internal/bootstrap) | 185 | Minimal DB boundary | Existing-database compatibility checks remain; implementation/startup organization is replaceable. No new library has demonstrated a smaller pinned-schema bootstrap. |
| [`catalog`](../../internal/catalog) | 3,574 | Persistence + HTTP + media | Treat this catch-all as dissolvable. Replace row emulation with setters, HTTP glue with typed adapters, file/media mechanics with shared engines; allocate operations by their contracts. |
| [`config`](../../internal/config) | 198 | One typed configuration owner | caarlos0/env is eligible; consolidate environment reads across modules and CLI parsing. Preserve externally supported settings/precedence, not current parsing helpers or flags implementation. |
| [`contracttest`](../../internal/contracttest) | 438 | Verification support | Reorganizable test fixture code; not a production-library reduction target. Keep proof of actual client/data contracts, not implementation fingerprints. |
| [`contracttest/shape`](../../internal/contracttest/shape) | 308 | Verification support | Response assertions can change form; retained client-field/type/security evidence remains necessary. Do not count tooling relocation as production deletion. |
| [`cronspec`](../../internal/cronspec) | 20 | Existing cron library | Can disappear as a package if common scheduler configuration is clearer. Retain accepted schedule dialect and observable schedules, not this 20-line wrapper. |
| [`db`](../../internal/db) | 4 | Persistence consolidation | Package marker may disappear into the canonical persistence owner. Four lines is incidental; no separate adoption project. |
| [`db/sqlc`](../../internal/db/sqlc) | 3,243 | Alternative, not protected | Keep sqlc if it wins the measured comparison; Bob may replace it. Generated output is not handwritten savings and must not be double-counted. |
| [`dbtime`](../../internal/dbtime) | 138 | Persistence codec | Absorb exact stored DATETIME/NULL conversion into a shared Scanner/Valuer/codec if the persistence design benefits. Current functions/package are replaceable. |
| [`deviceinfo`](../../internal/deviceinfo) | 54 | UA parsing alternative | A library such as uap-go may own recognition across session/playback/sharing. Preserve client-consumed field meaning; narrow current recognition/quirks are not automatically fixed. Low expected LOC return. |
| [`emailapi`](../../internal/emailapi) | 345 | Mail engine + typed HTTP | Replace the complete SMTP/MIME/attachment pipeline with go-mail and a typed application operation; discard whole-buffer staging and custom connection/message types. |
| [`emailsettings`](../../internal/emailsettings) | 203 | Typed config/persistence | Keep supported settings values/device access, not Port/Truthy implementations. Normalize compatibility at the edge; generated settings persistence can absorb this ownership. |
| [`events`](../../internal/events) | 86 | Application protocol projection | May merge with realtime/application event contracts. Keep names/payloads/recipients; current Emitter wrapper, task builder chain and audiences implementation need not survive. |
| [`feeds`](../../internal/feeds) | 207 | Feed application owner | Can collapse into one feed/query/projection owner with sharing/list functionality. Preserve row/GUID identity and URLs; no need for current split or helper signatures. |
| [`filekind`](../../internal/filekind) | 67 | Media policy/codec registry | May merge into media engine or a library-backed resolver. Retain interpretations needed for stored media and client MIME behavior; not every old table entry is independently immutable. |
| [`filterdata`](../../internal/filterdata) | 346 | Library read model | Can be replaced with generated aggregate queries and a simpler cache/state owner. Preserve results, read-after-write behavior and page performance; incremental patch implementation is not fixed. |
| [`httputil`](../../internal/httputil) | 584 | Typed HTTP + guarded client | oapi-codegen/Huma can absorb parameter/body/response mechanics; safeurl can own network protection. Keep actual status/content/auth/range/cache contracts, not Express-like writer helpers. |
| [`identity`](../../internal/identity) | 2,056 | OAuth/OIDC + persistence | Use installed oauth2/go-oidc for protocol exchange and generated session/user persistence. Rewrite flow state/cookie helpers if needed, preserving tokens, DB records and clients. |
| [`ids`](../../internal/ids) | 42 | Library direct use | google/uuid can replace this facade plus notifications UUID duplicate. Keep stored ID format and required validation/security, not exact current regex merely because TS used it. |
| [`items`](../../internal/items) | 352 | Canonical persistence aggregate | Replace row copies and manual loading with generated records/relationships; retain generic item response dispatch and required ordering/null/permission behavior. |
| [`itemwrite`](../../internal/itemwrite) | 654 | Transactional operations on persistence | Can be redesigned/merged with item application operations. Replace generic dynamic mutation mechanics with typed generated updates; commit boundaries and external effects remain provable. |
| [`jsonvalue`](../../internal/jsonvalue) | 109 | Edge normalization, not core model | Eliminate broad coercion use from typed core; retain only required wire/opaque-data conversions. A JS interpreter is eligible in principle but no smaller total design is established. |
| [`library`](../../internal/library) | 4,086 | Persistence + HTTP + queries | Rewrite around generated records/setters and typed endpoints. Query predicates/filter vocabulary can be declarative. Keep browse/search/lifecycle outcomes, not controller layout or shared Handler. |
| [`lists`](../../internal/lists) | 1,485 | One list operation/projection owner | Generated membership queries plus list operations can absorb playlistview and store playlist helpers. Keep ordered membership, ACLs, no-op timestamps and recipient outcomes. |
| [`logging`](../../internal/logging) | 724 | Reopened: slog pipeline | Replace bespoke logger/file-store classes with one standard record pipeline, rolling sink and thin client-history/socket adapter. Current console formatting and internal repair loops are not fixed. |
| [`mediafiles`](../../internal/mediafiles) | 175 | Unified media representation | May fold into canonical stored media codecs and engine/application boundary. Preserve unknown stored metadata and path security; current map wrapper and helper owner are not fixed. |
| [`mediatools`](../../internal/mediatools) | 136 | Unified media engine | Eligible for full replacement by afmpeg/native engine rather than an argument-builder wrapper. Build/process model is open; codecs, probe API and real throughput must be proven. |
| [`nameparse`](../../internal/nameparse) | 324 | Reopened name-normalization design | Current literal TS port is not sacred. A library or shorter algorithm is eligible if persisted name interpretation and client display/search/sort requirements hold; evaluate examples, not implementation identity. |
| [`notifications`](../../internal/notifications) | 453 | Notification operation owner | Can rewrite around a delivery library if saved Apprise targets/server semantics remain. Current 22-line Apprise HTTP delivery makes a Shoutrrr swap unlikely to delete much without translation cost. |
| [`people`](../../internal/people) | 1,101 | Persistence + typed author API | Generated canonical Author record/setter and operation-level merge replace row/scanner/column-update classes. Keep author behavior and contract-safe DTOs, not AuthorRecord layer. |
| [`playback`](../../internal/playback) | 1,541 | Unified media + session operation | Replace transcode/process/playlist machinery and storage copies. Preserve playable URLs, seek/reset/segment behavior and progress/session effects. Exact queue/worker/temporary-file algorithms are open. |
| [`playlistview`](../../internal/playlistview) | 130 | Collapse under lists | Generated query/relations + one list projection/event adapter can eliminate this independent package. No need to retain its map builder or separate read/announce layer. |
| [`podcasts`](../../internal/podcasts) | 2,155 | Media + feed parser + jobs | Keep/replace gofeed as evidence dictates; unified media/job operations can remove bespoke probing/registration plumbing. Preserve supported feed/download/schedule outputs and persisted metadata. |
| [`podcasts/read`](../../internal/podcasts/read) | 307 | Canonical persistence relations | Generated podcast/episode records and relations can replace this loader layer. Projections may remain under a cohesive podcast API, not necessarily this subpackage. |
| [`progressdto`](../../internal/progressdto) | 620 | Typed progress operation | Replace DTO-named persistence/coercion subsystem with normalized commands + generated records + one update operation. Preserve finish/unfinish/offline reconciliation and observable timestamps/events. |
| [`providers`](../../internal/providers) | 1,962 | One outbound/search adapter design | HTTP libraries and common typed DTO conversion may consolidate clients. Preserve search/provider results and custom-provider configuration. No verified umbrella provider SDK replaces all adapters yet. |
| [`realtime`](../../internal/realtime) | 1,124 | Reopened: Socket.IO library | zishang520 Socket.IO v3 is a leading prototype: remove eio.go/sio.go and replace transport tracking with library sockets/rooms. Keep deferred auth/init, ACLs, filtering and action contracts. |
| [`scanner`](../../internal/scanner) | 3,708 | Media engine + persistence + fsnotify | Rebuild reconciliation around shared engine/records; drop copied probe/import machinery. Metadata precedence/stable identity/sidecar outputs are contracts; current scan classes/pipeline are open. |
| [`server`](../../internal/server) | 1,024 | Composition over replacement engines | Composition/root/restore APIs can shrink with canonical persistence, library sockets and explicit app operations. Preserve service lifecycle/client recovery/data rollback, not current Runtime/Manager split. |
| [`serversettings`](../../internal/serversettings) | 411 | One typed settings owner | Replace global pointer registry with owned typed snapshots/atomic updates and canonical settings persistence. Keep values/deep-copy isolation and visible change behavior, not registry/cache design. |
| [`settingsdoc`](../../internal/settingsdoc) | 58 | Potential package elimination | Generated settings-row CRUD + one JSON codec can absorb this wrapper. Consolidate it as part of persistence/settings, not a separate 58-line adoption exercise. |
| [`sharing`](../../internal/sharing) | 1,960 | Feed + archive + typed HTTP | One feed DTO/XML engine and common archive engine can replace manual rendering/ZIP duplication. Public shares/auth and row/URL identity still need app operations, but existing module structure is open. |
| [`sqlupdate`](../../internal/sqlupdate) | 70 | Target entire package deletion | Bob optional setters or Bun selected-column updates replace this mechanism. Observable changed/no-op/forced-write outcomes move into one typed operation decision. |
| [`store`](../../internal/store) | 862 | Canonical persistence boundary | Retain or replace open/schema guard; eliminate duplicate user/session/device row layers and repeated scanners. Keep only genuinely distinct domain/HTTP projections and DB codecs. |
| [`textclean`](../../internal/textclean) | 236 | Library policy facade | bluemonday can replace sanitizer algorithm and strip pipeline. Retain safe rendered content/required metadata meaning, not stack-repair/escaping byte quirks copied from TS. |
| [`textdist`](../../internal/textdist) | 31 | Shared matching mechanism | May disappear into a justified shared matching library. Keep matching results that clients depend on; 31 lines alone still does not establish worthwhile dependency adoption. |
| [`textsort`](../../internal/textsort) | 131 | Library-backed sorting policy | Existing x/text already replaces collation engine. Consolidate prefix/sort policy as useful; preserve actual stored sort interpretation and client ordering, not current mutex/helpers as implementations. |
| [`userapi`](../../internal/userapi) | 1,568 | Typed API + generated persistence | Rewrite user/progress/listening endpoints around canonical records/commands/projections. Remove duplicative request parsing/state machinery; protect credentials and established account behavior. |
| [`users`](../../internal/users) | 146 | Protocol projection owner | Can merge with identity/account API types or generated transport DTOs. Distinct safe API views remain necessary; current type/function/package layout is not fixed. |
| [`version`](../../internal/version) | 7 | Build metadata | May use standard buildinfo/ldflags if simpler. Current seven lines are no meaningful library opportunity. |
| [`wire`](../../internal/wire) | 141 | Typed HTTP/event generation | Known shared JSON types can be generated or owned by protocol operations; this global package may dissolve. Preserve required shapes, not manual DTO definitions. |

## Concrete next action

**First:** a library-backed realtime prototype, because it targets a bounded 467-line protocol implementation, removes protocol maintenance and has a specific historical failure test. **Second:** a coherent Bob persistence vertical slice, because it offers the largest cross-module reduction. **Third:** typed HTTP generation on a small verified endpoint contract. Mail/sanitizer are smaller independent candidates; media/log/archive experiments are eligible but need capability/adapter-cost proof.

Each prototype must be one bounded ownership change with an exact old-code deletion list. Count total authored Go + SQL + configuration/codecs/adapters, separately from generated output and upstream dependency size. Preserve data/client invariants, measure realistic performance, run required affected/full/race/rollback gates and do not leave parallel implementations in a shipping result. No prototype or dependency install was executed by this audit; research claims are based on inspected source and official capabilities, not passing integration evidence.

Source control state: no runtime source or new repository edits in this audit. The five inherited documentation changes recording the owner's clarification are preserved. No new memory note, commit, push, deployment or task dispatch was performed. This report changes architectural recommendations; it does not silently start/replace the current PB04 execution packet.
