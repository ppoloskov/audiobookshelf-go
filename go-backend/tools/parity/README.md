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

`go-backend/tools/parity/cases/core.json` is the core case catalog: executable request descriptors and the former registry's coverage metadata share one record per case. The older `go-backend/contracts/parity/differential-cases.json` remains as a historical contract artifact, but the runner no longer treats it as a second case source. Stateful ordered flows are named in `go-backend/tools/parity/cases/steps.json` and implemented in `go-backend/tools/parity/cases/steps/`. The differential engine rejects unknown selected IDs, reports zero executions as failure, writes its report before returning failure, and compares status, selected headers and normalized bodies without relaxing case contracts.

The OIDC suite keeps its specialized provider and persistence checks in `oidc-differential.cjs`. Its stateful flow order is data-indexed in `cases/oidc-steps.json`, with named modules in `cases/steps/`; the compared result paths and case IDs are in `cases/oidc.json`. The full-suite runner enables the expiry pair so the committed OIDC golden's case set is exercised.

## Operational playback milestone

`operational-milestone.cjs` accepts an existing fixture manifest and creates a new disposable copy of its Go config/metadata under `--out`. The output directory must be new; the runner refuses to replace an existing `installation/`. It lengthens only the copied AAC track and clears only the copied root user's seeded progress row, then verifies login/browse, direct audio, HLS transcode output, backward/forward progress, restart/resume, and pinned Node reopening the Go-written database. It writes a sanitized report and provenance under `contracts/parity/`. This validates HLS playlist/segment delivery, not Chromium HLS decoding. The separate `browser-check.cjs` verifies browser direct-play and backward/forward track-bar seeks.

The former Python capability-ledger generator, classifier, and regression test are archived in `docs/agent-work/archive/parity-tools/`. The active route inventory remains `build_route_coverage.py --check`; the archive is historical tooling, not an active parity gate.

`core-flow-report.json` is a historical sanitized evidence summary from a narrower source-created run. It records response status/header parity and SHA-256 digests of normalized bodies without retaining fixture identifiers, credentials, tokens, or environment-specific paths. `core-flow-provenance.json` records its baseline, tool versions, source-script hashes, fixture method, normalization contract, and limits. It does not describe the current full core catalog.

`mutation-flow-report.json` and `mutation-flow-provenance.json` are historical artifacts from a later fresh-fixture run of that narrower core set followed by bookmark create, read, update, delete, and post-delete read. Their `createdAt` field is required and type-checked before normalization.

## Route inventory ledger

Regenerate the source-inspected ledger with:

```sh
python3 go-backend/tools/parity/build_route_coverage.py
python3 go-backend/tools/parity/build_route_coverage.py --check
```

All inventory routes begin with `compatibilityStatus: "unverified"`. A handler candidate status records only the Go code location and whether the main dispatcher imports that package; it never means differential parity passed. Only a passing entry with a real executable case may be marked verified after review.
