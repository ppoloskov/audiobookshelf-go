# Twin-Server Differential Oracle

Differential verification harness comparing pinned TypeScript **v2.37.2** and **Go** side-by-side in real time across HTTP, SQLite, Socket.IO, filesystem, outbound stubs, and bidirectional rollback.

## Architecture

```
                        +-----------------------------+
                        |  Test / Flow / Route Matrix |
                        +--------------+--------------+
                                       |
                 +---------------------+---------------------+
                 v                                           v
   +---------------------------+               +---------------------------+
   |   TS v2.37.2 (Node 22)    |               |    Go (monstrobook)       |
   |   NODE_V8_COVERAGE=v8/    |               |    GOCOVERDIR=gocover/    |
   +------+------+------+------+               +------+------+------+------+
          |      |      |                             |      |      |
       HTTP   3-Role  SQLite/FS                    HTTP   3-Role  SQLite/FS
       Stubs  Sock.IO  (26 tbls)                   Stubs  Sock.IO  (26 tbls)
          |      |      |                             |      |      |
          +------+------+--------------+--------------+------+------+
                                       v
        +--------------------------------------------------------------+
        |  Differential Oracle (Correspondence + Bijective UUID/Inode) |
        |  - HTTP: status, headers, cookies, strict JSON types/JWTs    |
        |  - SQLite: all 26 tables + typeof() storage classes          |
        |  - Socket.IO: 3 audiences (root, listener, finisher)         |
        |  - FS: tree & normalized metadata.json                       |
        |  - Outbound HTTP/SMTP stubs + bidirectional DB/FS rollback   |
        +------------------------------+-------------------------------+
                                       v
            divergences.json filter -> Scorecard + go-parity-backlog.md
```

## Running the Oracle

From the repository root:

```sh
# Full twin-server oracle + TS V8 & Go coverage + scorecard + parity backlog
make oracle

# Run a single declarative flow or test filter
make oracle ORACLE_ARGS="-run TestOracleFlows/auth"

# Enforce minimum in-scope TS V8 line coverage, happy-path route coverage, and optional Playwright E2E
make oracle ORACLE_ARGS="--min-v8-pct 75 --min-route-pct 80 --web-e2e"
```

From the repository root (unit self-tests without booting twin servers, or full race check):

```sh
go test -race ./testmachine/oracle
```

Pinned upstream TypeScript v2.37.2 is fetched on demand into `.oracle-upstream/` (or `$ORACLE_UPSTREAM_DIR`) by `testmachine/oracle/fetch-upstream.mjs` using `testmachine/oracle/upstream.json`. `run-oracle.mjs` invokes `fetch-upstream.mjs` automatically if `.oracle-upstream/index.js` is missing. Set `ORACLE_OPENAPI_OUT` (e.g. `ORACLE_OPENAPI_OUT=docs/openapi.json`) to emit an OpenAPI 3.0.3 spec merged from live TS traffic across all routes in `routes.json`.

Each `make oracle` run writes `oracle-test.log`, `go-parity-backlog.md`, `ts-v8-coverage.json`, `surface-hits.json`, and raw V8/Go coverage profiles to `/tmp/abs-oracle-run-*` and prints a unified `=== ORACLE SCORECARD ===`. If `client-audit.mjs` exists in this directory, it is invoked automatically for `$axios` / Socket.IO client coverage.

### CLI Flags & Surface Artifacts

- **`--min-v8-pct <N>`**: Optional threshold (default `0`, report-only). Fails the oracle run if in-scope TS V8 line coverage is below `N%`.
- **`--min-route-pct <N>`**: Optional threshold (default `0`, report-only). Fails the oracle run if runtime happy-path (2xx/3xx) route coverage in `surface-hits.json` is below `N%`.
- **`sockets.json`**: Socket.IO inbound/outbound event manifest generated alongside `routes.json` by `gen_routes.mjs` directly from `server/`.
- **`surface-hits.json`**: Runtime surface-area completeness report emitted via `ORACLE_SURFACE_OUT` during `go test -tags=oracle`. Tracks happy-path (`2xx/3xx`), error-probed (`4xx`), and any-hit route coverage against `routes.json`, live inbound/outbound Socket.IO event hits against `sockets.json`, and the full `unhit_happy_routes` list (top 25 printed in the scorecard).

## Adding a Declarative Flow (`flows/*.json`)

Any `*.json` file placed in `flows/` is automatically discovered by `TestOracleFlows`.

```json
{
  "name": "my-feature-flow",
  "fresh": false,
  "steps": [
    {
      "name": "update-progress",
      "method": "PATCH",
      "path": "/api/me/progress/{{novel}}",
      "role": "listener",
      "body": { "currentTime": 42, "duration": 120, "progress": 0.35 },
      "expect_status": 200,
      "check_db": true,
      "check_sockets": true,
      "capture": { "prog_id": "id" }
    }
  ]
}
```

- **Step `kind`**: `http` (default), `multipart`, `socket_emit`, `fs_write`, `fs_delete`, `db_exec`, `restart`, `rollback_go_to_ts`, `rollback_ts_to_go`, `stub_route`.
- **Check flags**: `check_db`, `check_fs`, `check_sockets` (with optional `settle_ms`), `check_stubs`.
- **Fixture `{{vars}}`**:
  - Users & entities: `{{root}}`, `{{listener}}`, `{{finisher}}`, `{{library}}`, `{{podcastLibrary}}`, `{{saga1}}`, `{{saga2}}`, `{{novel}}`, `{{ebook}}`, `{{disposable}}`, `{{podcast}}`, `{{ep1}}`, `{{ep2}}`, `{{ep3}}`, `{{collection}}`, `{{playlist}}`, `{{podcastPlaylist}}`, `{{author1}}`, `{{author2}}`, `{{series1}}`, `{{folder1}}`, `{{podcastFolder1}}`, `{{book1}}`, `{{progress1}}`, `{{bookmark1}}`
  - Paths & stubs: `{{install}}`, `{{config}}`, `{{metadata}}`, `{{media}}`, `{{stub_http}}`, `{{stub_http_shared}}`, `{{stub_smtp}}`, `{{stub_smtp_host}}`, `{{stub_smtp_port}}`
  - Captured vars: any key extracted via `"capture": {"var": "dot.path"}`.

## Recording a Mobile Session (`cmd/abs-record`)

Capture real iOS/Android Audiobookshelf client traffic against pinned TS v2.37.2 as a sanitized, replayable `Flow` JSON cassette:

```sh
go run ./testmachine/oracle/cmd/abs-record \
  -listen 0.0.0.0:13379 \
  -upstream http://127.0.0.1:13378 \
  -name mobile-ios-session \
  -out testmachine/oracle/flows/mobile-ios-session.json
```

Point the mobile app at `-listen`, exercise the workflows, and press `Ctrl+C`. Passwords/secrets, raw `Authorization`/`Cookie` headers, and `-install` paths are automatically redacted, and `role` is inferred from JWT claims.

## `divergences.json` and `coverage-exclusions.json`

- **`divergences.json`**: Ledger of intentionally accepted Go-vs-TS differences (architecture quirks or security hardenings such as SSRF blocking, SMTP redaction, and socket permission filtering). Rules with `go_expect` only suppress a finding when Go matches the expected value—regressions still fail.
- **`coverage-exclusions.json`**: Explicit TS `server/` buckets excluded from the in-scope V8 denominator (`vendored` under `server/libs/`, `migrations`, `ebook_internals`, `external_providers` under `server/providers/`, and `external_and_bootstrap`: `BinaryManager.js`, `BookFinder.js`, `AuthorFinder.js`, `PodcastFinder.js`). Both unfiltered `Total server/ app lines` and `In-scope server/ app lines` are always printed.

## Upgrading to a Newer TS Upstream Release

Follow this 5-step mechanical runbook whenever advancing the pinned TypeScript upstream release:

1. **Bump the TS commit & `sourcePin`**: Update `testmachine/oracle/upstream.json` and set `sourcePin` in `testmachine/oracle/fixture.go` to the new commit hash, then run `node testmachine/oracle/fetch-upstream.mjs`.
2. **Rebuild the seed fixture**: Regenerate the pinned TS seed fixture under `testmachine/testdata/install` so `fixture.json`'s `upstreamCommit` matches `sourcePin`.
3. **Diff `routes.json` and `sockets.json`**: Run `node testmachine/oracle/gen_routes.mjs` standalone and inspect `git diff` on `routes.json` and `sockets.json` to see added, removed, or changed HTTP routes and Socket.IO events.
4. **Run `make oracle` for the parity backlog**: Run `make oracle` to automatically generate the updated `go-parity-backlog.md`, `surface-hits.json` (with `unhit_happy_routes`), and the top uncovered V8 file list.
5. **Add JSON steps only for new features**: Add declarative `flows/*.json` steps (and update Go handlers or `divergences.json` as needed) only for newly introduced or modified upstream features.
