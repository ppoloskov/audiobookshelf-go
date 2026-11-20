# B5 — DATETIME write-format characterization

**Scope:** read-only characterization of SQLite `DATETIME` serialization in the Go backend against pinned upstream `edb1f7efc92ecacdef51aafb4d9d5054ce10604f` (v2.37.2). No production/tooling code was changed. Fixture evidence is from `/private/var/folders/jt/2prvbq7965ggn35rz2vwnqrw0000gn/T/opencode/b5-datetime-fixture`.

## Findings in brief

- The source-created upstream database stores Sequelize `DATE` fields as text in the form `YYYY-MM-DD HH:MM:SS.mmm +00:00` (UTC offset with colon, exactly three fractional digits, no `T`/`Z`). SQL NULL is genuinely NULL, not an empty string.
- Go has at least three material write serializations, not merely two at call sites: canonical-like `...mmm +00:00`, RFC3339Nano `...T...Z`, and the `database/sql`/SQLite-driver serialization visible in the Go fixture as `...fraction +0000 UTC`. A Go time.Time parameter (driver serialization) is distinct from formatting with `time.RFC3339Nano`.
- The canonical-like calls use layout `"2006-01-02 15:04:05.000 -07:00"`; since the supplied time is UTC, the sample suffix is `+00:00`. This matches upstream structurally. However, several Go datetime writes use RFC3339Nano or bind `time.Time`, and the Go DB run proves observable divergence.
- The risk is real: upstream has SQL timestamp ordering and direct JS Date object comparisons; mixed strings can sort differently if SQLite compares text, and parsing tolerance does not repair lexical SQL ordering.
- Severity is consistent with B5's summary, but the inventory shows broader exposure than the cited examples: identity/session persistence also writes RFC3339Nano, and driver-bound `time.Time` values emerge as a third raw database form. No evidence here establishes a new failure beyond that broader scope.

## Upstream source and raw database evidence

Model sources (all under `server/models/`) declare Sequelize-managed timestamps by enabling the model timestamps option, which is Sequelize's standard `createdAt`/`updatedAt` behavior. Examples: `PlaybackSession.js:174-203`, `MediaProgress.js:57-89`, `Series.js:89-129`; their class comments identify those timestamp properties as `Date` (`PlaybackSession.js:49-52`, `MediaProgress.js:33-36`, `Series.js:19-22`). `MediaProgress.finishedAt` is explicitly `DataTypes.DATE` at `MediaProgress.js:73`. The schema created by upstream declares `playbackSessions.createdAt/updatedAt DATETIME NOT NULL` and `mediaProgresses.finishedAt DATETIME` nullable; raw DDL is in the fixture database and tracked schema artifact `go-backend/contracts/parity/schema.sql`.

Fixture method: ran the unmodified `go-backend/tools/parity/create-fixture.cjs` with installed Node 22.23.4 to make independent Node/Go config copies; source script creates the upstream DB and seeds progress and a closed playback session through upstream APIs (`create-fixture.cjs:285-353`, copies at `:401-425`). Node 26 failed before startup because an installed dependency uses unavailable `SlowBuffer`; Node 22 succeeded. `sqlite3` observations (timestamps are exact values read from the generated fixture; wall clock is fixture-run-specific):

```text
node playbackSessions (first rows):
52fd7550-633f-49fe-ab46-78ffca3708e0 | createdAt 2026-10-29 21:41:52.784 +00:00 | updatedAt 2026-10-29 21:42:15.022 +00:00
7d30d024-c171-4ec0-a0bd-d3ec83ef3a3d | createdAt 2026-10-29 21:42:15.675 +00:00 | updatedAt 2026-10-29 21:42:15.675 +00:00

node mediaProgresses:
52fd7550-633f-49fe-ab46-78ffca3708e0 | createdAt 2026-10-29 21:41:52.784 +00:00 | updatedAt 2026-10-29 21:43:35.883 +00:00
7d30d024-c171-4ec0-a0bd-d3ec83ef3a3d | createdAt 2026-10-29 21:42:15.675 +00:00 | updatedAt 2026-10-29 21:43:36.507 +00:00

node series:
16dca415-72b2-4d1a-b9a3-0394562445b4 | Series E | 2026-10-29 21:41:52.315 +00:00 | 2026-10-29 21:41:52.315 +00:00
```

This proves milliseconds are present and zero UTC offset is rendered `+00:00`; it does not prove all upstream values always have nonzero milliseconds (millisecond precision is the serializer contract). For nullable `finishedAt`, the fixture query returned an empty display for an unset field; `quote(finishedAt)`/`typeof` should be used to make NULL explicit. The source-created schema permits SQL NULL, and the `MediaProgress` model documents the Date as nullable; there was no empty-string timestamp in these fixture rows. Do not conflate sqlite3 CLI's blank rendering of NULL with `''`.

No backup table holds DATETIME columns in this schema. Backup artifact metadata (`details`) is not a model `DATETIME` column and is outside this inventory.

## Go write inventory

“Reader tolerance” below names the concrete parser/layout implementation that can consume relevant strings; it does not imply every reader has the same list. Calls are limited to production `internal/` code. Values like feed `pubDate`, RFC/email dates, JSON/log timestamps, and date-only values are excluded unless they are written to a database DATETIME column.

| File:line | Flow / stored columns | Go representation | Reader tolerance |
|---|---|---|---|
| `internal/adminops/handler.go:114,663` | server settings and admin media updates (`updatedAt`) | layout `2006-01-02 15:04:05.000 -07:00` → `...mmm +00:00` | general readers include this layout: `catalog/handler.go:833-835`, `progressdto/progress.go:82`, `playback/playback.go:2010-12`, `identity/identity.go:95-103`; admin path itself does not parse these values |
| `internal/notifications/handler.go:72` | notification settings `updatedAt` | same canonical-like layout | general layout readers above; no package-specific setting timestamp reader |
| `internal/emailapi/handler.go:85` | email settings `updatedAt` | same canonical-like layout | general layout readers above |
| `internal/providers/handler.go:335` | provider `createdAt`/`updatedAt` writes via `dbNow` | same canonical-like layout | `providers/handler.go:328-330` parses canonical-like, RFC3339Nano and no-fraction space/offset; `store`/`catalog` general readers also tolerate it |
| `internal/catalog/writes.go:710` and callers (`writes.go:120,130,436,518,730,737,788,795,850`; `library_extras.go:140`; `cover.go:283`; `batch_chapters.go:428,517,521`; `library_writes.go:153,438,444,710`) | catalog/library/author/series/item writes | same canonical-like layout via `dbNow` | `catalog/handler.go:833-835`, `userapi/userapi.go:2017-19`, `progressdto/progress.go:82` and others parse it |
| `internal/catalog/batch_chapters.go:191` | book/library-item batch updates | same canonical-like layout inline | same general readers |
| `internal/catalog/personalized.go:65,121` and `catalog/handler.go:518` | SQL cutoff literals, not persisted writes | same layout as query parameter | not a persisted write; SQL compares stored timestamps against these strings |
| `internal/scanner/scanner.go:534` (`nowDB`) | library scans and related scanner writes (e.g. `:848,854`) | same canonical-like layout | `catalog/handler.go`, `store`, `userapi`, `progressdto` layouts |
| `internal/scanner/metadata.go:328`, `scanner/files.go:372,981`, `scanner/podcast_match.go:192,197` | scanner-created/updated model rows | same canonical-like layout | general readers; scanner event test parser is test-only (`scanner_events_test.go:89`) |
| `internal/realtime/hub.go:279` | user `lastSeen` | same canonical-like layout | `identity/identity.go:95-103`, `store/store.go:413-19`, userapi date coercion |
| `internal/sharing/handler.go:1289` | share/feed rows through `sqlDate` | same canonical-like layout | sharing parser `handler.go:1277-79`; standard parsers too |
| `internal/bootstrap/bootstrap.go:231` | root/default rows with timestamps | same canonical-like layout | general readers |
| `internal/playback/playback.go:831-833` | playback session `createdAt`,`updatedAt` | `time.RFC3339Nano` → UTC `...T...Z`; `now` declared at `:831`, created at `:832` | playback `parseTime` layouts at `:2010-12` include RFC3339Nano and multiple space layouts; catalog, progressdto, userapi and store also parse RFC3339Nano |
| `internal/playback/playback.go:901,920,924` | progress `finishedAt`,`createdAt`,`updatedAt`, including synced `lastUpdate` | RFC3339Nano formatted strings | `progressdto/progress.go:82`, `userapi/userapi.go:2017-19`, `playback/playback.go:2010-12`, `store/store.go:405-19` |
| `internal/playback/playback.go:1804` | playback-related persisted timestamp (inspect each SQL use in playback handler before altering) | RFC3339Nano | playback/store/general readers |
| `internal/podcasts/podcasts.go:442` | episode deletion cascade updates `libraryItems.updatedAt`, `podcasts.updatedAt` | RFC3339Nano | `podcasts/item.go:250-255`, catalog/store general readers |
| `internal/podcasts/podcasts.go:376,442,521,964,976,998` | episode/podcast model row timestamps (`createdAt`,`updatedAt`,`lastEpisodeCheck`) | RFC3339Nano | `podcasts/item.go:250-255` supports RFC3339Nano plus 3 space forms; catalog/store readers also accept it |
| `internal/identity/identity.go:909` | sessions/users timestamps; callers at `:520,543,564,1100`, plus `oidc.go:542,661,699,744` | RFC3339Nano | identity date layouts `identity.go:95-103`; store `:405-19`; userapi and catalog parsers |
| `database/sql` binding of Go `time.Time` values (various callers, including update paths; see fixture values below) | any DATETIME parameter passed as `time.Time` rather than formatted string | modernc driver serializes as `YYYY-MM-DD HH:MM:SS[.fraction] +0000 UTC` in this observed build | some readers include `...999999999 -0700 MST` (`podcasts/item.go:250-52`, `userapi/userapi.go:2408-10`); not all general parsers include this layout |

This is a format inventory, not an exhaustive list of SQL column assignments; each fix should inspect the SQL parameter at the listed call sites. Other `Format` matches for RSS/pubDate, API JSON milliseconds/ISO strings, date-only fields, and logs are not database DATETIME writes.

## Upstream consumers and risk

There are concrete consumers of temporal order/comparison:

- `server/Database.js:789-799` removes duplicate progress rows by comparing `mp2.updatedAt > mp1.updatedAt`, then equality and id tie-break. This SQL comparison is on stored column text under SQLite.
- `server/migrations/v2.15.0-series-column-unique.js:129` orders by `updatedAt DESC`.
- `server/managers/PlaybackSessionManager.js:232` compares `userProgressForItem.updatedAt.valueOf()` with session timestamp; `:491` filters stale sessions using timestamps. Other upstream in-memory object comparisons include `server/models/Feed.js:120,205-206,294-295` and `server/models/FeedEpisode.js:222`.
- Go uses temporal SQL comparisons: e.g. `internal/catalog` cutoff filters (`handler.go:518`, `personalized.go:65,121`) compare stored timestamps against a formatted text cutoff; progress selection/update logic converts timestamps to milliseconds (`playback.go:885-90`, `progressdto/progress.go:60-85`).

Mixed offsets/representations are not safely orderable as plain text in general. For example a space-separated date string sorts differently from a `T`-separated string for otherwise equal instants, and fractional widths can affect lexical order. Reader multi-layout parsing only helps after selecting a row; it cannot repair SQLite lexical `ORDER BY`, `<`, `>` or equality behavior.

## DB-content differential evidence

The standard `run-local.cjs` was run on the fixture with existing `go-backend/audiobookshelf-go`; it reported 73 total, 60 passed, 13 failed. It was run a second time on the already-mutated fixture and reported 41 passed/32 failed. This is not a clean or passing API parity result; the paired DBs nevertheless supply raw storage evidence for the write flows. Run-local invokes upstream and Go on distinct DB copies (`run-local.cjs:67-88`) and the differential includes progress writes, playback session creation/update and close (README lines 31-37).

The seeded session has id `52fd7550-633f-49fe-ab46-78ffca3708e0` in node-config. After `run-local`, node-config still has its seeded playback row; Go-config has **no** row in `playbackSessions` (the playback API differential did not create/persist a matching row in this execution). Therefore this fixture run cannot supply the requested same-logical-playback-row side-by-side write comparison. This is an evidence gap, not a MATCHES-UPSTREAM result. The separate raw test query previously observed a Go-formatted timestamp in playback-related progress state, but it must not be mislabeled as a matching playbackSessions record. A passing, targeted playback-session DB write case is required.

For the shared seeded progress id `52fd7550-633f-49fe-ab46-78ffca3708e0`, raw `createdAt` stays the upstream seed value, while the progress case updates `updatedAt`:

| DB | createdAt | updatedAt |
|---|---|---|
| node-config (upstream) | `2026-10-29 21:41:52.784 +00:00` | `2026-10-29 21:43:35.883 +00:00` |
| go-config (Go) | `2026-10-29 21:41:52.784 +00:00` | `2026-10-29 21:43:35.881075 +0000 UTC` |

Thus pre-existing matching canonical text is preserved, but a new Go update diverges in spelling and fractional precision. The Go row's raw suffix/precision is from Go's actual DB; not inferred from the source format string.

Progress write case: seeded progress id `52fd7550-633f-49fe-ab46-78ffca3708e0` was updated on both sides. Node stored `updatedAt=2026-10-29 21:43:35.883 +00:00`; Go stored `updatedAt=2026-10-29 21:43:35.881075 +0000 UTC`. Go also created a second progress row, id `dce48574-01b3-4103-8921-be6846478a6b`, with `createdAt=2026-10-29 21:42:15.67233 +0000 UTC` and `updatedAt=2026-10-29 21:43:36.50301 +0000 UTC`; no corresponding new upstream row was present in this run. That additional logical-row mismatch is a separate progress behavior discrepancy, so compare timestamps on the shared seeded row only when isolating serialization.

Series `16dca415-72b2-4d1a-b9a3-0394562445b4` (“Series E”) remained `createdAt=2026-10-29 21:41:52.315 +00:00` in both DB copies because the paired run did not exercise a Go series-creation flow. This is fixture-preservation evidence, **not** evidence that Go series creation writes the upstream representation. A targeted Go series-creation mutation is required for the later packet.

The fixture had no backup timestamp-bearing table; `sqlite_master` showed `migrationsMeta` but no `backups` table. Backup metadata is file-based and not an applicable SQLite DATETIME column.

## Repair plan for later packet

1. Add a leaf `internal/dbtime` package with a single write function returning UTC `t.UTC().Format("2006-01-02 15:04:05.000 -07:00")`. At UTC, this yields the Sequelize-shaped `...mmm +00:00`. Document that this layout is for database DATETIME values only; do not change API JSON, RSS dates, milliseconds, dates used for display, or timestamps passed to APIs.
2. Route database timestamp writes through that one helper. First change the proven divergences: `internal/playback/playback.go:831-833,901,920,924,1804`; `internal/podcasts/podcasts.go:376,442,521,964,976,998`; and `internal/identity/identity.go:909` callers (including `oidc.go` timestamp writes). Audit each SQL use at `podcasts/podcasts.go:361,713` before changing since a value may be JSON/provider data rather than a DATETIME column. Then replace duplicated canonical-like formatters in adminops, notifications, emailapi, providers, catalog, scanner, sharing, realtime and bootstrap with `dbtime`.
3. Audit every SQL-bound `time.Time` parameter in `internal/` and convert only those bound as DATETIME values; otherwise the driver may produce its own layout (`... +0000 UTC`). Preserve actual SQL NULL by binding `nil`; never substitute `""`.
4. Simplify only readers whose layouts exist solely to accept these in-repository database forms. Keep legacy compatibility layouts if existing user databases may contain them; do not blindly narrow parsing to one layout without evidence of existing stored values. Candidate duplicates: `progressdto/progress.go:78-87`, `catalog/handler.go:833-835`, `identity/identity.go:95-103`, `userapi/userapi.go:2008-19,2408-13`, `playback/playback.go:2010-12`, `podcasts/item.go:250-55`, `sharing/handler.go:1277-79`, `providers/handler.go:328-30`, `store/store.go:405-19`.
5. Verify with a fresh isolated fixture for each mutation run: (a) playback-session update/save and close; (b) progress write including shared seeded record and a newly inserted progress row; (c) Go series creation; (d) podcast episode/podcast update and scanner/catalog write if changed. Compare raw `quote(column)`, `typeof(column)`, exact `createdAt/updatedAt/finishedAt` and NULL-vs-empty semantics across node/go logical rows. Keep API parity cases and database equivalence assertions distinct.
6. Run focused tests: `go test ./internal/dbtime ./internal/playback ./internal/progressdto ./internal/podcasts ./internal/identity ./internal/catalog ./internal/scanner ./internal/providers ./internal/adminops ./internal/notifications ./internal/emailapi ./internal/sharing ./internal/store`; add deterministic unit coverage for UTC layout, milliseconds, parsing canonical + legacy forms, SQL NULL handling. Rerun targeted `tools/parity/run-local.cjs` on a new fixture and inspect report failures independently; the characterization run's 13 failures prevent claiming overall differential success.

## Evidence paths

- Pinned review summary: `go-backend/ARCHITECTURE-REVIEW-2026-10-29.md:102-115`.
- Upstream Sequelize models: `server/models/PlaybackSession.js:174-203`; `server/models/MediaProgress.js:57-89,190-263`; `server/models/Series.js:89-129`.
- Upstream timestamp comparison consumers: `server/Database.js:789-799`; `server/migrations/v2.15.0-series-column-unique.js:129`; `server/managers/PlaybackSessionManager.js:232,491`.
- Fixture generation / isolated copy provenance: `go-backend/tools/parity/create-fixture.cjs:277-353,401-449`; run-local separate processes/DBs: `go-backend/tools/parity/run-local.cjs:63-103`; suite scope: `go-backend/tools/parity/README.md:26-37`.
- Go examples: playback `internal/playback/playback.go:824-834,879-933`; podcasts `internal/podcasts/podcasts.go:442-454,521`; canonical-like catalog formatter `internal/catalog/writes.go:708-710`; identity formatter `internal/identity/identity.go:909`.
