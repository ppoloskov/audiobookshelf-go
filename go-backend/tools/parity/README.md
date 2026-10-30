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

Build the upstream server, build the Go command with `CGO_ENABLED=0`, then run:

```sh
node go-backend/tools/parity/run-local.cjs \
  --fixture /tmp/abs-parity-fixture/fixture-manifest.json \
  --go-bin /path/to/audiobookshelf-go
```

The runner starts Node and Go against the separate database copies and writes `parity-report.json` beside the fixture. The core 17 cases compare login, refresh, authorization, root and restricted library visibility, expanded and minified item lists, item detail, direct file byte ranges, progress writes and reads, playback session creation, HLS playlist bytes, session close responses, and authenticated Socket.IO connections over both the configured and legacy paths. The runner also executes bookmark create, read, update, delete, and post-delete read as ordered mutations. The Socket.IO probe uses the JavaScript v4 client. A route mismatch remains a failure; unsupported responses are not counted as compatibility. Progress, bookmark, and playback requests mutate their disposable databases, so generate a new fixture for each clean run.

`differential.cjs` can also run against servers started by another harness:

```sh
node go-backend/tools/parity/differential.cjs \
  --node-url http://127.0.0.1:13378 --go-url http://127.0.0.1:23378 \
  --fixture /tmp/abs-parity-fixture/fixture-manifest.json \
  --out /tmp/abs-parity-fixture/report.json
```

`differential-cases.json` records the executable cases and their methods, routes, authentication, category, and case-specific normalization contract. Every field replaced during normalization is required to be present and is checked for its expected type or format. IDs, output types, collection order, status codes, headers, media fields and byte bodies remain part of the comparison.

## Operational playback milestone

`operational-milestone.cjs` accepts an existing fixture manifest and creates a new disposable copy of its Go config/metadata under `--out`. The output directory must be new; the runner refuses to replace an existing `installation/`. It lengthens only the copied AAC track and clears only the copied root user's seeded progress row, then verifies login/browse, direct audio, HLS transcode output, backward/forward progress, restart/resume, and pinned Node reopening the Go-written database. It writes a sanitized report and provenance under `contracts/parity/`. This validates HLS playlist/segment delivery, not Chromium HLS decoding. The separate `browser-check.cjs` verifies browser direct-play and backward/forward track-bar seeks.

The capability ledger generator can refresh generated route fields but cannot derive curated state scenarios or review notes. It merges old evidence, verified cases, invariants, state scenarios, exceptions and unknown fields into regenerated output so a routine run does not erase them, keeps reviewed `status` and `contract` values instead of overwriting them with re-derived text, unions reviewed `tests`, `implementation` and `source` references, preserves manually classified `stateful` records, and refuses to overwrite an existing record it cannot parse (exit status 2). `--out DIR` generates into another directory — tests use temporary directories, never the live ledger; see `test_build_capabilities.py`.

`core-flow-report.json` is a sanitized evidence summary from one source-created 17-case run. It records response status/header parity and SHA-256 digests of normalized bodies without retaining fixture identifiers, credentials, tokens, or environment-specific paths. `core-flow-provenance.json` records the baseline, tool versions, source-script hashes, fixture method, normalization contract, and the run's limits. This run verifies only the listed request shapes and seeded fixture state; it does not establish parity for every route or permission branch.

`mutation-flow-report.json` and `mutation-flow-provenance.json` record a later fresh-fixture run of those same 17 core cases followed by bookmark create, read, update, delete, and post-delete read. The five bookmark cases pass; their `createdAt` field is required and type-checked before normalization.

## Route inventory ledger

Regenerate the source-inspected ledger with:

```sh
python3 go-backend/tools/parity/build_route_coverage.py
python3 go-backend/tools/parity/build_route_coverage.py --check
```

All inventory routes begin with `compatibilityStatus: "unverified"`. A handler candidate status records only the Go code location and whether the main dispatcher imports that package; it never means differential parity passed. Only a passing entry with a real executable case may be marked verified after review.
