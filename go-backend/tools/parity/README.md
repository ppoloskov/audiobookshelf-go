> **Status (2026-11-04):** this differential harness is advisory since V1;
> contract/value/state tests and rollback are the required gates. Byte-level
> normalizations are diagnostic, not a spec. V2a must extract reusable fixture,
> route, shape, browser, benchmark and rollback dependencies before V2b removes
> unreferenced tooling; real-device acceptance remains open. See the current
> [ROADMAP](../../ROADMAP.md) and [North Star](../../ARCHITECTURE.md).
> HTTP cache/range/format semantics remain contracts even when representation differs.

# Upstream parity fixtures

The parity tools use a disposable database initialized by the actual pinned Node server. They do not install or migrate a Go schema.

## Generate the full fresh-init schema

From the repository root, after installing upstream dependencies and building the server:

```sh
npm ci
npm run build:server
node go-backend/tools/parity/create-fixture.cjs /tmp/abs-parity-fixture
```

`create-fixture.cjs` starts an isolated upstream server, waits for database initialization, exports the fresh schema and initial settings before creating the root user, then seeds one synthetic book, one podcast, a restricted reader, and book progress through the upstream APIs. It stops Node before copying the initialized config and metadata to independent `node/` and `go/` directories. Both databases retain identical user, library and item IDs. The only media is a two second generated silent audio file. The fixture password values are fixed test credentials in the script and output manifest; do not reuse them.

The committed schema artifacts are:

- `go-backend/contracts/parity/schema.sql`: complete DDL from upstream `sqlite_master` after first initialization.
- `default-settings.json`: persisted server defaults and source-executed email/notification defaults. The latter two setting rows are not persisted in a fresh DB until changed.
- `migration-state.json`: `migrationsMeta` version and maxVersion.
- `schema-provenance.json`: baseline, upstream source hashes, schema hash and companion artifact hashes.

The exporter removes secret-bearing settings and environment-specific path settings. The actual disposable database and test credentials are written only under the requested work directory.

## Differential API run

Compare saved reports with `node go-backend/tools/parity/compare-reports.cjs <baseline.json> <current.json>`. It requires matching case IDs and compares status codes, headers, and full reference/Go bodies. Normalization is limited to body UUIDs mapped by first appearance (also replaced in paths), ISO-8601 or epoch-ms values in fields ending in `At`, `Seen`, `Scan`, `Update`, or `Time`, and integer `port` fields. It also compares each report's `normalizationApplied` block and prints every differing path; missing/extra cases or any residual difference produce a non-zero exit. Run its self-tests with `mise exec node@22 -- node --test go-backend/tools/parity/test-compare-reports.cjs`.

From the repository root, `make parity` rebuilds the Go binary, creates a fresh
fixture, and runs the core differential. `make parity-full` runs the named
core, ACL, cascades, OIDC, socket-soak, existing-database, rollback, backup
restore, F07 roll-in, and operational-milestone suites. Each suite receives
disposable state under a printed temporary work directory; preserve that path
when inspecting logs or reports after a failure. Both targets require Node 22,
the built upstream `dist-server`, and loopback permission.

Build the upstream server, build the Go command with `CGO_ENABLED=0`, then run:

```sh
node go-backend/tools/parity/run-local.cjs \
  --fixture /tmp/abs-parity-fixture/fixture-manifest.json \
  --go-bin /path/to/audiobookshelf-go
```

The runner starts Node and Go against separate database copies and writes `parity-report.json` beside the fixture. The core suite compares authentication, authorization, catalog reads and mutations, byte ranges, progress, playback, bookmarks, notifications, cover handling, email devices, scans, downloads, and authenticated Socket.IO behavior over configured and legacy paths. The Socket.IO probe uses the JavaScript v4 client. A route mismatch remains a failure; unsupported responses are not counted as compatibility. Mutating requests consume their disposable databases, so create a fresh fixture for each clean run.

`differential.cjs` can also run against servers started by another harness:

```sh
node go-backend/tools/parity/differential.cjs \
  --node-url http://127.0.0.1:13378 --go-url http://127.0.0.1:23378 \
  --fixture /tmp/abs-parity-fixture/fixture-manifest.json \
  --out /tmp/abs-parity-fixture/report.json
```

The core suite has 193 cases (see `STATUS.md` for the latest recorded run).
`go-backend/tools/parity/cases/core.json` is the core case catalog: executable request descriptors and the former registry's coverage metadata share one record per case. The older `go-backend/contracts/parity/differential-cases.json` remains as a historical contract artifact, but the runner no longer treats it as a second case source. Stateful ordered flows are named in `go-backend/tools/parity/cases/steps.json` and implemented in `go-backend/tools/parity/cases/steps/`. The differential engine rejects unknown selected IDs, reports zero executions as failure, writes its report before returning failure, and compares status, selected headers and normalized bodies without relaxing case contracts.

The OIDC suite keeps its specialized provider and persistence checks in `oidc-differential.cjs`. Its stateful flow order is data-indexed in `cases/oidc-steps.json`, with named modules in `cases/steps/`; the compared result paths and case IDs are in `cases/oidc.json`. The full-suite runner enables the expiry pair so the committed OIDC golden's case set is exercised.

## Operational playback milestone

`operational-milestone.cjs` accepts an existing fixture manifest and creates a new disposable copy of its Go config/metadata under `--out`. The output directory must be new; the runner refuses to replace an existing `installation/`. It lengthens only the copied AAC track and clears only the copied root user's seeded progress row, then verifies login/browse, direct audio, HLS transcode output, backward/forward progress, restart/resume, and pinned Node reopening the Go-written database. It writes a sanitized report and provenance under `contracts/parity/`. This validates HLS playlist/segment delivery, not Chromium HLS decoding. The separate `browser-check.cjs` verifies browser direct-play and backward/forward track-bar seeks.

The former Python capability-ledger generator, classifier, and regression test are archived in `docs/agent-work/archive/parity-tools/`. The active route inventory remains `build_route_coverage.py --check`; the archive is historical tooling, not an active parity gate.

`core-flow-report.json` is a historical sanitized evidence summary from a narrower source-created run. It records response status/header parity and SHA-256 digests of normalized bodies without retaining fixture identifiers, credentials, tokens, or environment-specific paths. `core-flow-provenance.json` records its baseline, tool versions, source-script hashes, fixture method, normalization contract, and limits. It does not describe the current full core catalog.

`mutation-flow-report.json` and `mutation-flow-provenance.json` are historical artifacts from a later fresh-fixture run of that narrower core set followed by bookmark create, read, update, delete, and post-delete read. Their `createdAt` field is required and type-checked before normalization.

## Browser check

`make browser-check` (or `node go-backend/tools/parity/browser-check.cjs [--fixture MANIFEST] [--variant full|transcode|restricted|root|fresh] [--write-contract]`) drives the unchanged web client in headless Chromium (Playwright) against isolated copies of the same fixture, once on upstream and once on Go, and compares login, library, detail and direct-play (or HLS, restricted-reader, root-prefix or first-boot) behavior. It needs `playwright` resolvable from Node (`require('playwright')`) with Chromium installed.

Without `--fixture` it creates a fresh one with `create-fixture.cjs` (needs the built `dist-server/`). Output goes to `BROWSER_CHECK_OUT`, default a fresh `os.tmpdir()` directory; `browser-report.json` there is the result. Upstream runs under the Node that runs the script (`process.execPath`), or `ABS_PARITY_NODE` when set; use Node 22.x. The checked-in `contracts/parity/browser-playback-differential-*.json` files are rewritten only by the `full` variant with `--write-contract`. Browser timing values (the first-loaded `readyState`, the post-play/pause `currentTime`) are compared as loaded/advanced booleans; seek positions stay exact.

## Large-library benchmark

`make bench` (or `node go-backend/tools/parity/bench.cjs [--work DIR] [--go-bin PATH] [--books 2000] [--runs 20] [--reuse 1] [--only upstream|go]`) compares upstream and Go on a generated library of 2000 books (about 300 authors, 200 series, 15% multi-file; tiny mp3 files with per-book ID3v2.3 tags written by the script, so only three ffmpeg calls). Upstream creates and scans the library (the scan time is recorded) and records five users' history through the client API: about 500 sessions (half play/sync/close, half offline `local-all`) over about 100 items, plus finished flags. Config and metadata are snapshotted with media untouched, then each server runs on the snapshot restored to the same paths (as in the replacement trial).

Per server it records the time from process start to the first successful `/ping`, RSS after start, after one warmup pass and after the load, and p50/p95 over N runs (default 20, interleaved round-robin) of: login, `/api/me`, libraries, filterdata, personalized, items pages (limit 50) with several sorts and filters including `collapseseries`, series, authors, narrators, search, expanded item, play start plus close, listening stats and sessions, and admin `/api/sessions`. It writes `bench-report.json` and `bench-report.md` to the work dir. Response bodies are not compared (the replacement trial does that); the run fails when Go answers non-2xx where upstream answers 2xx. A call is marked SLOW when Go p95 exceeds twice the upstream p95 or 500 ms. Numbers depend on the host; run both servers on the same machine and compare ratios, not absolute values. The work dir with 2000 books is about 100 MB; delete it afterwards.

## Route inventory ledger

Regenerate the source-inspected ledger with:

```sh
python3 go-backend/tools/parity/build_route_coverage.py
python3 go-backend/tools/parity/build_route_coverage.py --check
```

All inventory routes begin with `compatibilityStatus: "unverified"`. A handler candidate status records only the Go code location and whether the main dispatcher imports that package; it never means differential parity passed. Only a passing entry with a real executable case may be marked verified after review.

## Route probe

`make route-probe` (or `node go-backend/tools/parity/route-probe.cjs [--work DIR] [--go-bin PATH]`) checks that Go handles every route upstream registers. It reads `server/routers/ApiRouter.js` (mounted at `/api`), `PublicRouter.js` (`/public`) and `HlsRouter.js` (`/hls`), which give 209 routes, substitutes real IDs into the parameters, and sends each one to Go as root. It fails if a response is Go's `501 "not implemented in the experimental Go backend"` fallback and prints `NOT IMPLEMENTED: <route>` for each. Other statuses pass: behavior is checked by the differential and the trials. `ALLOWLIST` is empty; an entry passes only while its route is missing, and a stale entry fails the probe. It reuses the seeded replacement-trial installation (`--work DIR`), restores the snapshot first, and CI runs it last because it deletes the seeded entities.

## Replacement trial

`make parity-replacement` (or `node go-backend/tools/parity/replacement-trial.cjs [--work DIR] [--go-bin PATH] [--reuse 1]`) answers the product question directly: can the Go binary take over an existing upstream installation in place? It generates a realistic media tree (multi-file mp3 book with ID3 tags and `cover.jpg`, an m4b with chapters and an embedded cover, a flac book with `desc.txt`/`reader.txt`, an epub-only book), lets pinned upstream create and scan the installation, and records history through client calls: Android play/sync/close, iOS offline `local-all`, finished and ebook progress, bookmarks, a collection, a playlist, and a second user who finished book 1 of a series.

The installation also has a podcast library: a local folder with three generated mp3 episodes (ID3 title/date/track/comment, `cover.jpg`) that upstream discovers by scanning, so no RSS or iTunes network access is needed. History covers an Android episode play/sync/close, a finished episode, an iOS offline episode session, an item bookmark, an episode playlist and an open RSS feed. The podcast half of the journey exercises list filters and sorts, personalized shelves, `recent-episodes`, item detail with per-episode progress, episode play (direct and forced HLS), ranges, sync/close, offline and batch progress, playlists, search (items and episodes), and root-only episode/media updates and episode deletion. `match-episodes` without a feed and `POST /play` without an episode id are not exercised because upstream crashes or misbehaves on them.

Config and metadata are snapshotted while media stays untouched, so paths and inodes are identical for both servers. The same scripted mobile/web journey (login, authorize, libraries, personalized shelves, filters, collapsed series, search, item detail with progress, covers, ebook, direct and HLS playback, byte ranges, sync/close, offline sessions, batch progress, bookmarks, listening stats and sessions, admin views, token refresh) runs against upstream, then against Go after restoring the snapshot to the same paths. It also checks a Socket.IO `user_updated` progress event, then boots upstream on the Go-written state and compares progress and bookmarks (rollback).

The journey is about 506 steps. The last recorded run matched all of them with rollback ok (commit messages record the counts).

Normalization covers only UUIDs created during the journey (pre-existing ones must match exactly), JWTs, the work directory prefix, recent timestamps, and the order of the `discover` shelf, which is `ORDER BY RANDOM()` upstream. Media responses compare SHA-256 and `Content-Range`; covers compare content type only. `--reuse 1` skips seeding when DIR already holds a seeded snapshot.

## Scan trial

`make parity-scan` (or `node go-backend/tools/parity/scan-trial.cjs [--work DIR] [--go-bin PATH] [--reuse 1]`) proves that new content and library maintenance behave like upstream after an in-place swap. Pinned upstream seeds a three-book library with a root user and a listener who has progress; config+metadata and a pristine copy of the media tree are snapshotted. One scenario then runs against upstream, and again against Go after the snapshot and the media tree are restored to the same paths with pinned mtimes: add a multi-file mp3 book (ID3 tags, `cover.jpg`) and scan, add a track and `desc.txt` to an existing book, rename a book folder (listener progress must still apply), delete a folder (missing items, `DELETE /api/libraries/:id/issues`), single-item rescans, `PATCH /api/items/:id/media` plus a cover upload, and finally the library watcher (a folder added and removed with no scan request, bounded wait, final state compared). A connected root Socket.IO client records every realtime event; each step diffs the responses and the events (names, payloads, order).

The scan trial has 12 steps, and the last recorded run matched all of them.

Normalization is limited to ids created at runtime (replaced by a role label such as the item path or author name), `ino` values and `/file/<ino>` URLs (the media tree is restored with new inodes), `birthtimeMs`/`ctimeMs`, wall-clock timestamps, scan durations, JWTs, the work directory prefix and the `discover` shelf order; the full list with reasons is in the header of the script. The library watcher is switched off for the explicit-scan steps so it cannot scan at an arbitrary moment, and switched on for the last two steps. Known upstream non-determinism is avoided rather than normalized: `DELETE /issues` removes an emptied author only some of the time, so the scenario deletes a book whose author keeps other books.
