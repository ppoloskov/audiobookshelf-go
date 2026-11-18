# Ordered replacement-contract gap queue

Generated with the requirement mapping; status lives in capability/task records.
One accountable owner and one bounded packet per invocation. Q00 is this mapping
packet. Architect acceptance is required after Q00 and between every later packet;
no worker may dispatch its successor. This queue authorizes preparation within the
existing contract, not deployment, dependencies or compatibility exceptions.

At dispatch the architect creates a canonical task, pins base+approved overlay,
names exact files (the directory below is only a maximum ownership boundary), and
selects a finishable subset of assigned invariant IDs. Budgets: 45 minutes for
characterization, then a 90-minute implementation/verification checkpoint; at most
two unsuccessful corrections of one failure. These are planning limits, not model
or token limits. If the owner's family cannot fit, split it into sequential packets
before dispatch, retaining every ID and dependency. No unreviewed partial pass.

All packets depend on acceptance of the previous packet in this order; Q03 onward
also depend on Q02's usable contract-aware harness. Multi-producer event records are accountable integration obligations: owners may
add contracttest assertions, but later producer requirements remain open until their
own packets are accepted. Cross-owner defects discovered
in a test-only integration packet get a new bounded owner task before resuming;
never silently expand production write scope. Architect alone updates register,
README, AGENTS, ROADMAP and handover. Workers may write only dispatched production
files, their tests and a named deterministic harness/evidence path. Shared media,
settings, server or event helpers require explicit ownership in the brief.

Every packet must: read its whole package + source/client references; derive
normal/error/edge and initial/repeat/changed/force/failure or justify N/A; add
failure-seeking source/client-derived tests first; demonstrate red where a bug is
identified; implement the smallest fix; run affected tests/race plus required repo
checks; provide current source/binary/corpus hashes and N compared/M mismatched/S
skipped; obtain separate contract and simplicity architect reviews. No production
behavior change is authorized solely because a generic map row suggests it.

## Q01 — Audio task progress and queue lifecycle

Owner: **tools**. Maximum production write boundary: `go-backend/internal/tools/`.

Acceptance: Prove track_started/progress/finished and task_progress with incremental values, exact task data and audiences; immediate work must not fake queue transitions; test waiting/repeat/cancel/failure/shutdown and real output. Reuse media process runner; any media API change needs a separately reviewed ownership exception.

Assigned requirement IDs (17): `cap-event-metadata-embed-queue`, `cap-event-task-lifecycle`, `cap-event-track-finished`, `cap-event-track-progress`, `cap-flow-tools-job-lifecycle`, `route-r183`, `route-r184`, `route-r185`, `route-r186`, `route-r204`, `socket-outgoing-metadata-embed-queue-update`, `socket-outgoing-task-finished`, `socket-outgoing-task-progress`, `socket-outgoing-task-started`, `socket-outgoing-track-finished`, `socket-outgoing-track-progress`, `socket-outgoing-track-started`

## Q02 — Contract-aware evidence harness

Owner: **harness**. Maximum production write boundary: `go-backend/testmachine/parity/`.

Acceptance: Reproduce and triage recorded core/replacement/scan/OIDC/boot differences; distinguish client/persistence defects from free representation. Correct fixture and comparator assumptions with rejection self-tests; never normalize away protected fields, statuses, IDs, time units or missing events.

Assigned requirement IDs (0): All preceding requirements; audit-only rollup, no production ownership.

## Q03 — Pinned database and row semantics

Owner: **db**. Maximum production write boundary: `go-backend/internal/db/`.

Acceptance: Open fresh/existing/refused installs; prove schema/row/time/JSON TEXT/null/trigger/link-order semantics and transactional rollback with TS reopening Go mutations. Compare refused-install trees and meaningful row state; explain harmless boot updates rather than demanding byte identity.

Assigned requirement IDs (2): `persisted-column-semantics`, `sub-database-schema`

## Q04 — Atomic settings and secrets

Owner: **settings**. Maximum production write boundary: `go-backend/internal/settings/`.

Acceptance: Prove partial/null/default handling, concurrent disjoint patches, failed update rollback, deep snapshots, reload callbacks, secure defaults, secret redaction and nonpersisted environment overrides against pinned rows.

Assigned requirement IDs (1): `cap-flow-settings-atomicity`

## Q05 — Local authentication and permissions

Owner: **auth**. Maximum production write boundary: `go-backend/internal/auth/`.

Acceptance: Characterize login/init/logout/refresh/API authorization, token modes, rotation/replay/grace, disabled/inactive users and immediate revocation; check denied reads/events expose no data and existing client fallback statuses.

Assigned requirement IDs (8): `flow-client-auth-token-lifecycle`, `flow-client-permissions-ownership`, `route-r000`, `route-r001`, `route-r006`, `route-r010`, `route-r207`, `sub-authorization-ownership-w5`

## Q06 — OIDC web and mobile PKCE

Owner: **oidc**. Maximum production write boundary: `go-backend/internal/auth/oidc*.go`.

Acceptance: Prove web state/cookie and native PKCE flows, expiry, duplicate/empty/wrong verifier, one-time replay, redirect validation, linking/unlinking and safe errors; triage known 400-vs-302 candidate with actual client decisions.

Assigned requirement IDs (6): `flow-client-oidc-mobile-pkce`, `route-r002`, `route-r003`, `route-r004`, `route-r005`, `sub-oidc-auth`

## Q07 — Socket transport, auth and dispatch

Owner: **realtime**. Maximum production write boundary: `go-backend/internal/realtime/`.

Acceptance: Use a real Socket.IO v4 client with legacy/prefixed websocket paths; prove auth/init/action errors, per-user sockets, reconnect/heartbeat/overflow/shutdown, revocation and filtered audiences. Polling/binary requirements require client evidence and explicit disposition, never a hidden skip.

Assigned requirement IDs (17): `socket-incoming-auth`, `socket-incoming-cancel-cover-search`, `socket-incoming-cancel-scan`, `socket-incoming-connection`, `socket-incoming-disconnect`, `socket-incoming-message-all-users`, `socket-incoming-ping`, `socket-incoming-remove-log-listener`, `socket-incoming-search-covers`, `socket-incoming-set-log-listener`, `socket-outgoing-admin-message`, `socket-outgoing-auth-failed`, `socket-outgoing-init`, `socket-outgoing-pong`, `socket-outgoing-user-offline`, `socket-outgoing-user-online`, `sub-realtime-socketio`

## Q08 — Session sync, offline replay and media access

Owner: **playback**. Maximum production write boundary: `go-backend/internal/playback/`.

Acceptance: Prove local/local-all with iOS/Android payloads, duplicate and out-of-order replay, seconds/milliseconds, stable IDs, progress conflict rules, session ownership/history/delete, public track ranges and post-close access. Link accepted stream packet scenarios instead of reimplementing its accepted differences.

Assigned requirement IDs (22): `flow-client-playback-session-sync`, `route-r059`, `route-r060`, `route-r140`, `route-r141`, `route-r142`, `route-r143`, `route-r144`, `route-r145`, `route-r146`, `route-r147`, `route-r148`, `route-r219`, `route-r225`, `socket-outgoing-stream-closed`, `socket-outgoing-stream-error`, `socket-outgoing-stream-open`, `socket-outgoing-stream-progress`, `socket-outgoing-stream-ready`, `socket-outgoing-stream-reset`, `socket-outgoing-user-session-closed`, `socket-outgoing-user-stream-update`

## Q09 — User, progress, bookmarks and API keys

Owner: **user**. Maximum production write boundary: `go-backend/internal/user/`.

Acceptance: Prove admin/root/owner CRUD, progress and bookmark list/detail/null/delete, stats/session projections, API-key permissions and ereader edits including durable rows and recipient scopes; include GET all-progress/all-bookmarks success.

Assigned requirement IDs (41): `route-r071`, `route-r072`, `route-r073`, `route-r074`, `route-r075`, `route-r076`, `route-r077`, `route-r078`, `route-r079`, `route-r099`, `route-r100`, `route-r101`, `route-r102`, `route-r103`, `route-r104`, `route-r105`, `route-r106`, `route-r107`, `route-r108`, `route-r109`, `route-r110`, `route-r111`, `route-r112`, `route-r113`, `route-r114`, `route-r115`, `route-r116`, `route-r117`, `route-r118`, `route-r119`, `route-r120`, `route-r121`, `route-r199`, `route-r200`, `route-r201`, `route-r202`, `socket-outgoing-ereader-devices-updated`, `socket-outgoing-user-added`, `socket-outgoing-user-item-progress-updated`, `socket-outgoing-user-removed`, `socket-outgoing-user-updated`

## Q10 — Scan precedence, identity and lifecycle

Owner: **scan**. Maximum production write boundary: `go-backend/internal/scan/`.

Acceptance: Prove configured metadata precedence including absent/empty/locked, full multi-file chapter offsets/order, repeat/force/move/collision/removal/partial failure/cancel, watchers/cron, derived entities and cache invalidation. TS rereads written metadata.json for books and podcasts.

Assigned requirement IDs (6): `format-metadata-json`, `route-r038`, `route-r049`, `route-r062`, `scan-precedence-timeline`, `sub-scanner-watcher`

## Q11 — Podcast feed, download and schedule

Owner: **podcast**. Maximum production write boundary: `go-backend/internal/podcast/`.

Acceptance: Reach successful feed/create/search/check/download paths with local RSS/audio, not denial-only shapes. Prove stable episode IDs, queued/running/clear/failure/stop, retry/auto-disable/scheduling, OPML/match and file/row/event effects.

Assigned requirement IDs (23): `route-r024`, `route-r039`, `route-r040`, `route-r043`, `route-r149`, `route-r150`, `route-r151`, `route-r152`, `route-r153`, `route-r154`, `route-r155`, `route-r156`, `route-r157`, `route-r158`, `route-r159`, `route-r160`, `route-r161`, `socket-outgoing-episode-added`, `socket-outgoing-episode-download-finished`, `socket-outgoing-episode-download-queue-cleared`, `socket-outgoing-episode-download-queued`, `socket-outgoing-episode-download-started`, `sub-podcast-download-queue`

## Q12 — Item metadata, files and deletion

Owner: **item**. Maximum production write boundary: `go-backend/internal/item/`.

Acceptance: Prove projections and PATCH absent/null/empty/locked fields, batch atomicity, chapters/tracks, cover/file/ebook serving and mutation, quickmatch and destructive cascades; preserve TS-readable metadata/rows and no effects/events on rollback.

Assigned requirement IDs (31): `route-r045`, `route-r046`, `route-r047`, `route-r048`, `route-r050`, `route-r051`, `route-r052`, `route-r053`, `route-r054`, `route-r055`, `route-r056`, `route-r057`, `route-r058`, `route-r061`, `route-r063`, `route-r064`, `route-r065`, `route-r066`, `route-r067`, `route-r068`, `route-r069`, `route-r070`, `socket-outgoing-batch-quickmatch-complete`, `socket-outgoing-item-added`, `socket-outgoing-item-removed`, `socket-outgoing-item-updated`, `socket-outgoing-items-added`, `socket-outgoing-items-updated`, `sub-covers`, `sub-destructive-cascades-w6`, `sub-downloads`

## Q13 — Library management and aggregates

Owner: **library**. Maximum production write boundary: `go-backend/internal/library/`.

Acceptance: Prove library CRUD/folder removal/order/settings, access-filtered aggregates/filterdata invalidation, downloads, scan/match dispatch and metadata removal with rollback and watcher effects after commit.

Assigned requirement IDs (19): `route-r017`, `route-r018`, `route-r019`, `route-r020`, `route-r021`, `route-r023`, `route-r030`, `route-r032`, `route-r033`, `route-r034`, `route-r035`, `route-r036`, `route-r037`, `route-r041`, `route-r042`, `route-r044`, `socket-outgoing-library-added`, `socket-outgoing-library-removed`, `socket-outgoing-library-updated`

## Q14 — Browse, search and series

Owner: **browse**. Maximum production write boundary: `go-backend/internal/browse/`.

Acceptance: Prove pagination/filter/sort/search/collapse/personalized/series projections across restricted/tag/explicit users, renamed series, empty/invalid queries and stored ordering.

Assigned requirement IDs (10): `route-r022`, `route-r025`, `route-r026`, `route-r029`, `route-r031`, `route-r138`, `route-r139`, `socket-outgoing-series-added`, `socket-outgoing-series-removed`, `socket-outgoing-series-updated`

## Q15 — Authors and image lifecycle

Owner: **author**. Maximum production write boundary: `go-backend/internal/author/`.

Acceptance: Prove author update/match/delete and image upload/fetch/cache/remove, linked item updates, permissions and durable metadata; assert affected audiences and rollback rather than just response shape.

Assigned requirement IDs (11): `route-r131`, `route-r132`, `route-r133`, `route-r134`, `route-r135`, `route-r136`, `route-r137`, `socket-outgoing-author-added`, `socket-outgoing-author-removed`, `socket-outgoing-author-updated`, `socket-outgoing-authors-num-books-updated`

## Q16 — Collections and playlists

Owner: **list**. Maximum production write boundary: `go-backend/internal/list/`.

Acceptance: Prove owner/library/permission gates, empty/null values, ordered add/remove/reorder/batch and collection-to-playlist conversion; check empty-list deletion, shared-item cascades, feed closing and rollback silence.

Assigned requirement IDs (28): `flow-client-list-ownership`, `route-r027`, `route-r028`, `route-r080`, `route-r081`, `route-r082`, `route-r083`, `route-r084`, `route-r085`, `route-r086`, `route-r087`, `route-r088`, `route-r089`, `route-r090`, `route-r091`, `route-r092`, `route-r093`, `route-r094`, `route-r095`, `route-r096`, `route-r097`, `route-r098`, `socket-outgoing-collection-added`, `socket-outgoing-collection-removed`, `socket-outgoing-collection-updated`, `socket-outgoing-playlist-added`, `socket-outgoing-playlist-removed`, `socket-outgoing-playlist-updated`

## Q17 — Feed and public share consumers

Owner: **share**. Maximum production write boundary: `go-backend/internal/share/`.

Acceptance: Prove feed item/collection/series open/refresh/XML/cover/enclosure, timezone/pubDate meaning, closing/deletion, share expiry/session/progress/download and capability access; byte ranges and cache headers must support real clients.

Assigned requirement IDs (20): `flow-client-feed-share`, `route-r007`, `route-r008`, `route-r009`, `route-r187`, `route-r188`, `route-r189`, `route-r190`, `route-r191`, `route-r195`, `route-r196`, `route-r220`, `route-r221`, `route-r222`, `route-r223`, `route-r224`, `socket-outgoing-rss-feed-closed`, `socket-outgoing-rss-feed-open`, `socket-outgoing-share-closed`, `socket-outgoing-share-open`

## Q18 — Backup archive, retention and apply

Owner: **backup**. Maximum production write boundary: `go-backend/internal/backup/`.

Acceptance: TS reads Go archive layout and database; prove upload/path/settings/schedule/retention, malformed/traversal/symlink/wrong-version rejection before install, and no corruption on failure. Runtime swap itself belongs to server packet.

Assigned requirement IDs (9): `format-backup-archive`, `route-r122`, `route-r123`, `route-r124`, `route-r125`, `route-r126`, `route-r127`, `route-r128`, `sub-backup-scheduler`

## Q19 — Metadata providers and cover search

Owner: **metadata**. Maximum production write boundary: `go-backend/internal/metadata/`.

Acceptance: Prove built-in/custom provider query/result/type/order/fallback, CRUD secrets and SSRF protection, websocket cover search result/error/cancel/provider error and no late result after cancellation; use deterministic local providers.

Assigned requirement IDs (16): `route-r175`, `route-r176`, `route-r177`, `route-r178`, `route-r179`, `route-r180`, `route-r192`, `route-r193`, `route-r194`, `socket-outgoing-cover-search-cancelled`, `socket-outgoing-cover-search-complete`, `socket-outgoing-cover-search-error`, `socket-outgoing-cover-search-provider-error`, `socket-outgoing-cover-search-result`, `socket-outgoing-custom-metadata-provider-added`, `socket-outgoing-custom-metadata-provider-removed`

## Q20 — SMTP, MIME and ereader delivery

Owner: **mail**. Maximum production write boundary: `go-backend/internal/mail/`.

Acceptance: Prove settings/defaults/redaction/admin gates, actual local SMTP/TLS/auth/envelope/MIME attachments/filenames, reader device persistence, timeout/cancel and safe error paths; TS/configured clients interpret the message.

Assigned requirement IDs (6): `cap-flow-mail-smtp-mime`, `route-r170`, `route-r171`, `route-r172`, `route-r173`, `route-r174`

## Q21 — Notification delivery and configuration

Owner: **notify**. Maximum production write boundary: `go-backend/internal/notify/`.

Acceptance: Prove subscription/settings CRUD, admitted triggers and no-subscriber edges, local Apprise payload, failure/retry/disable state and notifications_updated full settings; characterize the historical string-enabled fixture without retaining unrelated JS coercion.

Assigned requirement IDs (11): `cap-event-notifications-updated`, `cap-flow-notify-delivery`, `route-r162`, `route-r163`, `route-r164`, `route-r165`, `route-r166`, `route-r167`, `route-r168`, `route-r169`, `socket-outgoing-notifications-updated`

## Q22 — Admin operations and operational settings

Owner: **admin**. Maximum production write boundary: `go-backend/internal/admin/`.

Acceptance: Prove upload/filesystem/path/cache/tag/genre/cron/stats/settings routes including authorization, validation, traversal, atomic affected-row updates and downstream invalidation; no silent coercion or protected details.

Assigned requirement IDs (20): `route-r129`, `route-r130`, `route-r181`, `route-r182`, `route-r197`, `route-r198`, `route-r203`, `route-r205`, `route-r206`, `route-r208`, `route-r209`, `route-r210`, `route-r211`, `route-r212`, `route-r213`, `route-r214`, `route-r215`, `route-r216`, `route-r217`, `route-r218`

## Q23 — Ranges, MIME and conditional caching

Owner: **httpx**. Maximum production write boundary: `go-backend/internal/httpx/`.

Acceptance: Characterize shared file/JSON behavior plus actual public/item/playback/download entrypoints: suffix/open/multiple/unsatisfiable 206/416, correct bytes/Content-Range/MIME/disposition, ETag/304 after writes and denied/revoked access. Do not freeze representation algorithms.

Assigned requirement IDs (2): `flow-client-mobile-protocol`, `protocol-ranges-cache`

## Q24 — Observable text and ordering

Owner: **text**. Maximum production write boundary: `go-backend/internal/text/`.

Acceptance: Check Node-derived natural/locale/name/prefix fixtures and representative real caller ordering; retain only client-visible/persisted semantics, include Unicode/numeric edge cases and safe rich-text handling.

Assigned requirement IDs (1): `text-client-ordering`

## Q25 — Persisted logs and subscribers

Owner: **logging**. Maximum production write boundary: `go-backend/internal/logging/`.

Acceptance: TS reads Go daily/crash logs; verify line shape/timezone, retention/midnight/reopen, level filtering and per-socket subscribe/unsubscribe without secret leakage.

Assigned requirement IDs (2): `format-daily-logs`, `socket-outgoing-log`

## Q26 — Runtime configuration and launch

Owner: **config**. Maximum production write boundary: `go-backend/internal/config/`.

Acceptance: Prove flag/environment precedence, supported config/metadata/tool/client paths and invalid input; operate a CGO=0 build without Node, retaining external FFmpeg and assets.

Assigned requirement IDs (2): `runtime-binary`, `runtime-configuration`

## Q27 — Runtime graph, restore and HTTP pipeline

Owner: **server**. Maximum production write boundary: `go-backend/internal/server/`.

Acceptance: Prove cold boot/shutdown/restart/restore success and rollback under requests/jobs, same-socket backup_applied argument semantics, CORS/basepath/public exceptions/cache isolation/static SPA; verify healthy media has no short blanket deadline.

Assigned requirement IDs (12): `cap-event-backup-applied`, `cap-event-log`, `cap-flow-backup-restore-swap`, `protocol-cors-basepath`, `route-r011`, `route-r012`, `route-r013`, `route-r014`, `route-r015`, `route-r016`, `runtime-lifecycle`, `socket-outgoing-backup-applied`

## Q28 — Configured web and native acceptance

Owner: **client**. Maximum production write boundary: `go-backend/internal/contracttest/`.

Acceptance: Run unchanged bundled web login/catalog/detail/playback with valid root/prefixed builds; exercise every source-audited native protocol workflow and retain device evidence for iOS/Android plus documented client profiles. Matching shared failures or source-audit confirmation are not successful workflows.

Assigned requirement IDs (2): `client-device-acceptance`, `sub-static-client`

## Q29 — Independent contract closure audit

Owner: **closure**. Maximum production write boundary: `go-backend/testmachine/reports/`.

Acceptance: Re-scan source, client consumers, DB/models/config/files/jobs and requirement register; all cases/invariants/state variants must have current positive and failure-seeking evidence. Run all machine/rollback/client gates and strict ledger. An unresolved required skip, missing assertion or stale evidence blocks the final claim.

Assigned requirement IDs (0): All preceding requirements; audit-only rollup, no production ownership.

## Final claim gate

Only after the independent final inventory audit and all packets are accepted:

1. Every in-scope operation, event, model/format and lifecycle obligation maps to
   passing source/client-derived tests for each required field/status/audience,
   error/edge and repeat-state invariant. No generic sampled invariant substitutes
   for missing branches. All exclusions/differences have explicit owner authority.
2. Evidence names the shipping commit and approved overlay, source pin, corpus,
   binary/input hashes, exact commands/exit statuses and durable observations;
   zero unresolved mismatches or required skips. Different representations may be
   compared by meaning only with the actual contract rationale recorded.
3. Go formatting, vet, complete tests, configured lint, affected race suites,
   CGO=0 native/Linux amd64/arm64 builds, real media, fresh/existing/refused boot,
   TS reopens Go-written database/files/backup, restore failure, runtime lifecycle
   and required web/native protocol/device acceptance all pass on that source.
4. Renderer `--check`, pinned ledger normal validation, then
   `go run /Users/paul/.agents/skills/go-scope-ledger/scripts/check-ledger.go
   -tasks docs/agent-work/tasks -capabilities go-backend/testmachine/register/capabilities
   -require-verified -require-invariants` pass. Structural success alone proves
   neither inventory completeness nor test behavior; architect checks both.

Then, and only then, publish: **“All requirements of the defined v2.37.2 replacement
contract are verified.”** Until then report individual accepted packet evidence
and the remaining queue. This is not authorization for live deployment.
