# Verification harness inventory (2026-11-13, HEAD c89429ed, frozen copy)

Historical discovery evidence. The active authority is now the Go-first unified
machine described in ../../VERIFICATION.md. Legacy scripts remain runnable while
assertions migrate; their separate verdicts do not establish full coverage.

All runs: Go binary built from `git archive HEAD`; Node 22.23.4 (mise); upstream oracle = dist-server rebuilt from the pinned source (oracle check `git diff --quiet edb1f7ef -- server ...` clean); FFmpeg 9.0.2 with WebP first on PATH. Logs: `*.log` and `*.result` in this directory.
Last-touched = `git log -1 --format=%cs` of the path.

| # | Harness | Compares | Inputs | Last touched | Runs today? | Result (wall) | CI |
|---|---|---|---|---|---|---|---|
| 1 | `internal/contracttest` (`go test`) | Go vs values RECORDED once from TS (contracts/shapes/*.json): status, content-type, JSON type skeleton, Socket.IO event names/shape, 15 DB dumps. Not values. Plus hand-written Go assertions (persistence, mobile protocol, socket.io client, permissions) | upstream-created install `testdata/install` (built by tools/contractfixture/build.cjs on 2026-11-03 from the replacement-trial seed); 23 Test funcs, 868 subtests; 7 journeys = 763 HTTP steps + 15 DB dumps = 778 recorded entries | code 2026-11-12; shapes + fixture 2026-11-03 | yes | PASS, 25.8 s (full `go test ./...` also PASS, 28.6 s) | gating (`verify` job, go-backend.yml build job) |
| 2 | `replacement-trial.cjs` (`make parity-replacement`) | LIVE TS vs Go on the same seeded install, restored in place; full normalized value diff per step + Socket.IO events + TS reopens Go-written DB (rollback) | 491 HTTP steps (+15 DB dump checks = 506) from `journeySteps`, web + Android/iOS-shaped payloads; step list in `rt_steps.json` | 2026-11-03 | yes | 478/506 match; socket ok; rollback ok; 164 s. Exit 1 | advisory (docker-go.yml `parity`); `--rollback-only` part is GATING (`rollback` job gates the image) |
| 3a | `scan-trial.cjs` (`make parity-scan`) | LIVE TS vs Go: new content, rename, delete, metadata edit, responses + events | 12 scripted steps on a 3-book library | 2026-11-02 | yes | 7/12 match; 137 s. Exit 1 | advisory |
| 3b | `route-probe.cjs` (`make route-probe`) | Every upstream route (209 from the 3 router files) must not hit Go's 501 "not implemented" fallback | seeded work dir of the replacement trial | 2026-11-03 | runs, but VACUOUS | "209 upstream routes sent to Go; 0 hit the not-implemented fallback", 15 s | advisory |
| 3c | `browser-check.cjs` (`make browser-check`) | Playwright/Chromium on the unchanged web client, Node vs Go | fresh fixture | 2026-11-02 | NO: Playwright not installed on this host ("Playwright module not found") | cannot run | not in CI |
| 3d | `suite-runner.cjs core` (`make parity`) / `equivalence` | LIVE TS vs Go, 168 stored cases + stateful step modules, vs baseline `baselines/core-schema2-no-webp.json` (equivalence: second independent fixture) | `cases/core.json` + `cases/steps/*.cjs` | 2026-11-06 (cases) | crashes mid-run | exit 1 after 47 s: `Error: global test event fixture must contain two enabled onTest notifications` (notification-lifecycle.cjs:92). TS fixture has 2 enabled onTest notifications, Go has 1 | `equivalence` advisory in go-backend.yml; `core` advisory in docker-go.yml |
| 3e | `suite-runner.cjs full` (`make parity-full`) | core + acl, cascades, oidc, socket-soak, existing-db-matrix, rollback-check, test-backup-restore, f07-rollin-check, operational-milestone | per suite | 2026-10-31..11-03 | yes | 994 s. 6 of 10 suites fail: core, acl, oidc, existing-db-matrix, rollback-check, f07-rollin-check. Pass: cascades (2 results, 0 divergences), socket-soak (Go p95 auth 6-8 ms, 0 failures), test-backup-restore, operational-milestone | not in CI |
| 3f | `bench.cjs` (`make bench`, 300 books, 5 runs) | upstream vs Go latency | generated library | 2026-11-02 | yes | exit 0, 38 s; Go cold start 97 ms vs 524 ms; no Go non-2xx; flagged: listening-sessions p95; RSS columns null on macOS (stale measurement) | not in CI |
| 4a | `tools/library-oracle.cjs` + `test-library-oracle.cjs` | executes pinned upstream Library serializers in a VM; checks `contracts/library-golden.json` | upstream source | 2026-10-30 | yes | PASS 0.04 s | gating (go-backend.yml) |
| 4b | `tools/book-item-oracle.cjs` + `test-book-item-oracle.cjs` | same for book/item serializers; `contracts/parity/book-item-golden.json` | upstream source | 2026-10-31 | yes | PASS ("6 source fixtures, three shapes") | gating |
| 4c | `tools/source_inventory.py` + test; `contracts/source-inventory.json` | route/socket syntax inventory of server/ at the pinned commit | server/ | 2026-10-30 | yes | 5 tests OK | gating |
| 4d | node --test of compare-reports, status, cover-patch-contract, differential-selftest | harness self-tests | - | 2026-10-31..11-06 | yes | 38/38 pass | gating (first three); selftest advisory |
| 4e | `tools/clientusage` (`make client-usage`) + `contracts/client-usage.json` | maps web-client calls and mobile-log/trial routes to upstream routes and Go handlers | client/ source | 2026-11-10 | yes (`TestOutputIsCurrent` passes in `go test ./...`) | PASS | gating (go test) |
| 5 | `docs/openapi.json` (repo root) | upstream API description, lint only (lint-openapi.yml) | - | 2024-08-18 | n/a | 31 paths, 46 operations, version 0.1.0. Hand-written, covers a small subset of the 223 routes. Not a test input | lint only |

## Status of the contracts/ files (who consumes what)

- `contracts/shapes/*.json` (778 entries, recorded from TS 2026-11-03): consumed by contracttest. Live.
- `contracts/client-usage.json` (201 routes, 70 events): generated; guarded by `TestOutputIsCurrent`. Live. Input of `clientaudit/build.py`.
- `contracts/parity/baselines/core-schema2-no-webp.json`: consumed by `suite-runner equivalence`. Only a no-webp baseline exists; the webp profile bootstraps a second fixture instead.
- `contracts/parity/route-coverage.json` (2026-11-10): read by clientusage/main.go and clientaudit.
- `contracts/parity/latest-core-run.json`: read by `status.cjs` only.
- `contracts/parity/book-item-golden.json`, `contracts/library-golden.json`: consumed ONLY by the oracle tests (they prove the golden still equals what the upstream source produces). No Go test compares Go output to them (grep of internal/: none).
- `contracts/parity/*-report.json`, `*-provenance.json`, browser-screens/, evidence/ (about 60 files, 2026-10-29..31): historical run records, not read by any code except status/README references.
- `contracts/smoke-cases.json` (2 cases: healthcheck, ping): referenced by nothing. Orphan.
- `contracts/source-inventory.json`: referenced only by contracts/README.md; regenerated by the python tool and checked by its unit test. Pinned commit edb1f7ef (2026-10-30).
- `contracts/mobile-client-evidence.json` (14 K) and `contracts/clients/*.md` (6 clients, raw evidence 2026-11-04/09): input to `tools/clientaudit/build.py` (prints Markdown, edits nothing). `mobile-client-evidence.json` says itself "not yet consumed by tools/clientusage".
- `contracts/mobile-client-audit.md`, `mobile-compat-gaps.md`, `mobile-route-decisions.md`, `takeover-decision-report.md` (2026-11-04): documents. They cite Go paths that no longer exist (`internal/userapi`, `internal/identity`, `internal/httputil`) and "TEST-COMPATIBILITY-IMPLEMENTATION.md", `test-assertions.json`, `monstrobook-test-reconciliation.json` (all absent). `tools/parity/README.md` cites `representation-diagnostic.cjs` and `make test-ts-diagnostics` (absent).

## Why the failing differentials fail (evidence)

Replacement trial, 28 differing steps (journey-upstream.json vs journey-go.json), groups:
- listening-stats, listening-sessions (x6 steps): Go adds `audioTracks: []` and `coverAspectRatio: null` to session objects that TS omits.
- life.feeds.list / life.feed.xml.* / db.feedEpisodes: TS `pubDate` "Fri, 13 Nov 2026 07:08:15 GMT" vs Go "Thu, 12 Nov 2026 21:08:15 GMT" (10 h offset: a time-zone difference in the feed episode pubDate); XML text differs (TS single line, Go newline after the declaration).
- life.feed.open.noaudio: message "Item has no audio tracks" vs "No audio tracks"; life.share.public.*: "Not Found" vs "Media item share not found"; series.update.invalid: "No valid fields to update" vs `Invalid value for "name"`. These are text bodies of error replies (quirk-policy changes).
- life.share.create.pastexpiry: Go adds `isDownloadable: false`.
- batch.update.apply / revert / after.filterdata: event order and author id differences (`item_updated` vs `author_updated` first); filterdata authors differ.
- db.books.titles: `bookIgnore`/`itemIgnore` "Single File Novel" vs "Matched Single File Novel".
Scan trial: filterdata `authorCount` TS 2 vs Go 3 after adding a book (TS keeps a stale author "Terry Fixture"; Go lists the new one); item payload `metaTags.tag*` null in TS vs absent in Go.
Core (3d): the TS fixture has two enabled onTest notifications because upstream stores `"enabled":"false"` (a string) as enabled; Go has one. Harness cannot proceed. Partial core run restricted to 18 cases: 7 pass, 4 fail (headers `content-type: text/plain` present only in Go; local-session-login), 7 skipped.
ACL: 1 divergence of 11 cases (10 pass): `malformed-permissions-create` TS 200 (creates user) vs Go 400 "Invalid request".
OIDC: 24 compared, 1 equal; 23 differ almost only in headers (content-type text/html vs text/plain, ETag, Content-Length). One status difference: `oidc-mobile-callback-no-session-duplicate-empty-verifier` TS 400 "No session" vs Go 302 to /login. Needs 10 minutes of real time for the state-expiry case.
existing-db-matrix: all 4 "refuses" scenarios fail only because Go's message ("migration state is ...") no longer contains the harness's expected text ("existing database migration state is ..."); Go exits 1 and does not serve. The two baseline "boot-unmutated" checks fail because Go boot rewrites the DB/trees.
f07-rollin-check / rollback-check: f07 `node-go-reopen-and-restricted-state` fails because Go rewrites the `server-settings` row at boot (updatedAt changes and JSON is re-serialised with different key order); rollback-check dies in the same notification step as core. The CI `rollback` job (replacement-trial --rollback-only) passed here ("rollback ok", 13 sessions, progress and bookmarks match).
So: the harness fails are a mix of deliberate behaviour changes the harness cannot express (no allow-list) and a few that were never triaged. Nothing here shows a missing route.
