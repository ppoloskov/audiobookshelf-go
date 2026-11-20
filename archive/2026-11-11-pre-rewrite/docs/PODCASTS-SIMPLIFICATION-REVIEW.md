# Podcasts simplification review — 2026-11-10

## Decision and scope

The package can become substantially easier to follow without changing its
callers. The strongest opportunities are typed internal matching, a smaller
episode-update path, one media probe per download, and separating concerns
within the existing package. A dramatic net line reduction is not yet proven;
file moves are organization, not savings. Keep all implemented functionality.

The assessment below preceded PC01 implementation; the implementation addendum
at the end records the actual changes and verification. Reviewed canonical master
at `bf72cb3c16efebadc60ed7a4f12d7f803a8c7326` with the inherited PB04 worktree
changes present. No production source, tests, dependencies, metrics or Git
staging were changed by this review. Root package: 2,165 physical production
lines across six files; `read`: 307 across two files. These include comments
and blank lines and are not ratchet measurements. `podcasts.go` alone has
1,032 lines; `downloads.go` has 530.

## Interfaces to retain

- `New(*sql.DB, config.Config, events.Emitter, NotificationFunc) *Handler`,
  `Handler.Routes(*http.ServeMux)` and `Handler.Close()`. `New` currently starts
  schedules; moving startup to a new API would change caller behavior.
- `EpisodeDownloads(string) ([]wire.EpisodeDownload, *wire.EpisodeDownload)`
  satisfies catalog's `DownloadQueue` interface. Runtime also supplies
  `QuickMatchEpisodes(context.Context, string, bool) (int, error)` and
  `Reschedule(context.Context, string)` as callbacks. See
  [runtime](../internal/server/runtime.go) and
  [consumer declarations](../internal/catalog/handler.go).
- Keep exported `Feed`, `FeedMetadata`, `Episode`, download DTOs and
  `NotificationFunc`. `FetchFeedEpisodes(context.Context, string)
  ([]map[string]any, error)` has only one current production caller, inside
  QuickMatchEpisodes. It can remain a compatibility wrapper while that internal
  caller uses typed feed data directly.
- Keep `podcasts/read` and its exported records, options, projections,
  `LoadMany`, `Podcast.Media`, `Episode.JSON` and `Episode.Expanded`. Direct
  production consumers outside podcasts are items, catalog, playback, lists,
  playlistview, sharing and userapi. Concrete type assertions make matching
  JSON alone insufficient. Keep the current acyclic dependency graph:
  podcasts → items → podcasts/read.

## Findings and smallest useful changes

### 1. Typed feed matching — simplify

Evidence: [podcasts.go:494](../internal/podcasts/podcasts.go) marshals typed
`[]Episode` and immediately unmarshals it into `[]map[string]any`.
[quickmatch.go:20–166](../internal/podcasts/quickmatch.go) then uses string keys,
an indexed array of nullable SQL values, nested-map accessors and generic date
conversion to recover values already represented by the feed type.

Have QuickMatchEpisodes call `getFeed`, and let its private matcher consume
`[]Episode`. Keep the exported map-returning wrapper. Replace the numbered
nullable-field array with named fields if the resulting code is clearer;
do not introduce a repository/interface hierarchy. This removes dynamic
reconstruction from the operational path even if the wrapper remains.

Before changing: characterize ties, no match, override on/off, SQL NULL versus
empty text, absent/zero publishedAt, enclosure length, default episode type,
partial failures and repeat matching. Existing quickmatch_test.go is one
successful example. Search and quick-match are different policies: search uses
title/subtitle >0.6 and weighted sorting; quick-match uses a best score >=0.9
and trims candidate text. Share primitives only; retain those distinctions.

### 2. Episode PATCH and writes — simplify / merge duplicate

Evidence: [podcasts.go:203–322](../internal/podcasts/podcasts.go) decodes a map,
walks a field-name map, switches back to struct fields, then sorts a second map
to construct SQL. [quickmatch.go:57–96](../internal/podcasts/quickmatch.go) builds
another map before producing the same SQL SET/argument lists.

Use a typed, presence-aware private PATCH document and explicit comparisons.
A small package-private episode-update accumulator can own ordered SET clauses,
arguments and updatedAt for both callers if it actually removes code. Keep
validation and overwrite policy in their respective callers. No generic SQL
framework, reflection or new dependency is needed.

Before changing: protect absent/null/empty/wrong-type inputs, sanitization,
enclosure clearing, chapters, publishedAt, no-op writes and event emission.
Plain pointers alone cannot distinguish absent enclosure from explicit null.
A strict decoder would also change the current behavior of ignoring some
wrong-type fields. Current chapter handling preserves open-ended keys and
rejects numeric start=0; do not silently normalize those during cleanup.
Keep dynamic chapter data where the existing contract permits it.

### 3. Download orchestration — merge duplicate, preserve lifecycle

Evidence: [downloads.go:184–229](../internal/podcasts/downloads.go) repeats the
finished/time/lock transition in success and failure branches. EpisodeDownloads
collects queue and current state under two separate lock acquisitions
(113–136); libraryDownloadQueue repeats filtered snapshot logic in library.go.
`fetchEpisode` probes the final file at 287; `registerEpisode` immediately
probes it again at 308. This is two ffprobe processes on each successful
ordinary download.

Centralize the completion transition and collect item queue/current status
under one lock. Keep queue snapshot helpers small; three callers do not justify
a generic filtering abstraction. Validate media once in the fetch/register
flow, retaining validation for direct registration and invalid-file cleanup.
Separating file transfer from registration is useful; a new worker framework
or channel topology has no demonstrated benefit.

Before changing: preserve serial FIFO, duplicate suppression, queued/current
distinctions, event names/audiences/order, failures advancing the queue, clear
affecting only waiting tasks, cancellation, file collision names, temp-file
cleanup, size limits, retention, transaction rollback and auto-only
notifications. Add an invalid-audio download case and a registration-write
failure case that verify no leftover media or partial DB state. Do not remove
the atomic write transaction or media validation to save lines.

### 4. File responsibilities — simplify organization

Evidence: [podcasts.go](../internal/podcasts/podcasts.go) contains routing,
auth/loading, PATCH/delete, creation, OPML import, cron state, selection,
network fetch and compression. downloads.go combines queue, transfer,
registration, retention and notification delivery.

Keep the public Handler as the facade and initially keep its state concrete.
Suggested files within the existing package (not new subpackages/layers):

| File | Responsibility |
|---|---|
| handler.go | Handler, constructor, public facade and shutdown |
| routes.go | Route registration, authorization and thin HTTP handlers |
| episodes.go | Episode mutation, deletion and registration transactions |
| create.go | Typed creation input and creation operation |
| opml.go | OPML parsing, import orchestration and task messages |
| schedule.go | Cron entries, failure counts and scheduled checks |
| feed.go | Safe HTTP fetch, decoding and feed URL validation |
| rss.go, quickmatch.go | Existing parser and matching responsibilities |
| downloads.go, library.go, dto.go | Queue/transfer, library endpoints, DTOs |
| read/ | Existing shared data/projections/loaders, retained |

Move the roughly 270-line creation/import block and roughly 130-line scheduler
block out of podcasts.go, with feed transport separated from parsing. The
creation operation should receive concrete data from HTTP and OPML rather
than having OPML construct nested maps. Preserve its validation order, path
checks, directory cleanup, defaults and item_added timing. Typed decoding must
retain existing numeric/string coercion where observable.

### 5. Small cleanup candidates and KEEP decisions

- `libraryRoute(recent bool)` (library.go:19) selects unrelated endpoints.
  Two named handlers sharing the existing library opener are clearer; preserve
  auth and the recent-only podcast-media-type check. This is clarity work, not
  a promised net reduction.
- The `filepath.Clean` calls immediately before `filepath.Abs` in creation
  (podcasts.go:576/580) are redundant with Abs's cleaning. Keep containment
  checks and cleanup. Small local deletion only.
- Keep gofeed and the adapter in rss.go. It already replaces raw RSS mapping;
  the remaining logic expresses tested precedence, sanitization, enclosures,
  iTunes/media/chapter extensions and Windows-1252 handling.
- Do not collapse manual and scheduled episode checks into one policy: their
  cutoff, failure response, counter/disable behavior and write/enqueue ordering
  differ. Keep filterNew as the already-shared selection implementation.
- Keep retention's existing one-candidate behavior and playlist/progress
  cleanup. Keep feed compression handling, network policy, size limits and
  distinct feed-versus-stored episode representations.
- Do not blindly merge podcast/episode filename sanitizers: podcast names
  collapse whitespace and guard dot names; episode names currently differ.

## Verification and follow-through

Contract pass: traced producer/caller data, queue/current distinctions,
matching policies, PATCH presence semantics, persistence and events. Concrete
counterexamples above explain why a passing suite alone would not justify
strict decoding, one universal episode type or identical matching policies.
The separate bounded worker review confirmed consumers and existing contract
tests; that worker ran no tests. The primary reviewer performed the Go
craft/deletion assessment and the actual focused test run.

Executed against the current worktree from go-backend:

```sh
GOTOOLCHAIN=local CGO_ENABLED=1 \
PATH="/private/tmp/abs-cleanup-checkpoint-20261105/ffmpeg-9.0.2-webp/bin:$PATH" \
FFMPEG_PATH=/private/tmp/abs-cleanup-checkpoint-20261105/ffmpeg-9.0.2-webp/bin/ffmpeg \
go test -race ./internal/podcasts/... -count=1 -json
```

Exit 0: both packages passed; 29 top-level tests (27 feature, 2 read), 52
including subtests, zero skips. Real ffprobe fixtures executed. Local JSON log:
`/private/tmp/podcasts-review-tests-20261110.json` (temporary evidence).
The initial sandbox attempt could not access the existing Go build cache;
the successful rerun had the required cache/loopback access.

No code changes were made, so full-module build/vet/lint, downstream suites,
ratchet, TS diagnostics and rollback were not rerun. These results are a
focused baseline, not proof of the proposed refactor or whole-repo parity.

Recommended implementation order: fill the named behavioral gaps, simplify
typed matching and update construction, remove duplicate probe/completion work,
then organize files. Preserve all public signatures, read models, routes and
features. Run affected downstream suites and existing repository acceptance
gates before accepting code. Measure net ratchet changes after implementation;
do not set a drastic deletion target without replacement-cost evidence.

## PC01 implementation and acceptance — 2026-11-10

Implemented by the owner's requested `gpt-6.1-terra` agent and reviewed by the
parent architect. No public signatures, `podcasts/read` source, dependencies,
schema, routes or features changed. Separate contract and Go-simplicity review
found no blocking issues in the final source.

### Actual ownership and reductions

- `podcasts.go` now has 356 lines (previously 1,032), retaining construction,
  routing, request orchestration and the public facade.
- `create.go` owns podcast creation and OPML import; `schedule.go` owns cron
  entries and scheduled checks; `episode_mutations.go` owns edits/deletion;
  `episode_registration.go` owns downloaded-episode persistence, retention and
  notification data. `downloads.go` retains queue/transfer state. `rss.go`
  owns feed retrieval, decoding and parsing. Existing library/DTO/read owners
  remain. The final split uses four additional production files; separate
  feed/opml/deletion files were rejected because their repeated import blocks
  outweighed their benefit.
- QuickMatchEpisodes consumes typed feed episodes and named nullable database
  fields directly. Deleted its intermediate update map, redundant iteration,
  generic nested-map accessors and date converter. The public map-returning
  FetchFeedEpisodes wrapper remains unchanged for interface compatibility.
- PATCH uses explicit old-field pairs instead of a name map plus field-name
  switch. Creation removes a metadata remapping map and uses one deferred
  rollback/directory-cleanup path. A committed item's directory is retained
  even if the subsequent reload fails; existing directories are never removed.
- Download completion has one state transition, item queue/current values are
  captured under one lock, and media is probed once during registration rather
  than twice per successful download. Direct registration still validates
  media, and failed registration removes downloaded files.

Isolated HEAD-plus-PC01 ratchet deltas, including all new source files:

| Metric | Before | After | Delta |
|---|---:|---:|---:|
| Handwritten production lines | 38,443 | 38,433 | -10 |
| Cyclomatic complexity | 8,568 | 8,541 | -27 |
| Functions over complexity 15 | 114 | 113 | -1 |
| Dynamic maps | 451 | 442 | -9 |

Every other metric is unchanged. The podcast feature package itself falls
from 2,165 to 2,155 physical production lines. These are net reductions,
including import/header costs; the 676 lines relocated out of podcasts.go are
not additional savings. The reduction is modest in size, with clearer ownership
and fewer dynamic transformations. No speed benchmark was run.

### Compatibility cases and deliberate limits

Characterization covers empty versus NULL matching fields, override/repeat/first
tie behavior, exact PATCH key case, missing/null/wrong-type/empty inputs,
chapter extension keys and zero-start rejection, invalid audio, failed
registration after episode insertion, both creation-insert failures, existing
versus newly created directory ownership, successful creation, numeric metadata
coercion and sanitized empty descriptions. Original assertions remain intact.

PATCH's permissive exact-key map boundary remains: ordinary struct decoding
would introduce case-insensitive keys and reject inputs currently ignored.
Creation/OPML map inputs also remain; OPML currently supplies `[]string`
categories to a helper that accepts `[]any`, so importing genres yields `[]`.
This pre-existing behavior was preserved rather than changed during cleanup.
Manual/scheduled checks, the two matching policies and filename rules retain
their existing distinctions. The proposed generic update accumulator and typed
creation/PATCH document rewrites were not needed for the smaller accepted
implementation.

### Verification evidence

Acceptance snapshot: `/private/tmp/abs-podcasts-pc01-shipping`, archived from
`bf72cb3c16efebadc60ed7a4f12d7f803a8c7326` and overlaid with only PC01 podcast
sources/tests, refreshed evidence hashes and lowered metric baseline. Concurrent
PB04 and userapi work is excluded. Canonical worktree metrics therefore must
not be used to attribute this packet's changes.

- Worker final `go test -race ./internal/podcasts/... -count=1 -json`: exit 0,
  34 top-level tests, 65 including subtests, zero skips, real ffprobe executed.
  Log: `/private/tmp/pc01-podcasts-race-final.json`.
- Exported declaration/field/signature comparison against archived original
  source: identical; `podcasts/read` directory: unchanged. Function-body
  comparison separated moves from actual behavioral-path edits.
- Architect independently overlaid all new characterization tests onto the
  original production source: all six selected top-level tests passed under
  `-race`, exit 0, zero skips. This confirms their expectations preserve the
  existing behavior. Log: `/private/tmp/pc01-original-characterization.json`.
- Isolated `go build ./...`, `go vet ./...`, `go test ./... -count=1 -json`:
  exit 0. All 49 packages with tests passed. One pre-existing playback HLS test
  skips because it checks the Linux-only `/usr/bin/ffmpeg` path on macOS;
  podcast tests have no skips. Socket.IO interop executed with the installed
  Node client. Log: `/private/tmp/pc01-shipping-tests.json`.
- golangci-lint v2.15.0: exit 0, zero issues. `make ratchet` and dbnest:
  exit 0 after the snapshot's lower-only baseline update. Dependency rules:
  26 fixtures passed. Both Linux amd64/arm64 CGO-disabled builds passed.
- Pinned SQLC v1.31.1 refresh: exit 0; all 11 generated files byte-identical,
  including absence of new outputs.
- Required source/contract checks passed: source inventory 5 tests; route
  catalog 4; report/status 21; both source oracles; assertion/PATCH checks 8
  passed with one optional external Monstrobook snapshot check skipped.
- Route coverage regeneration preserves all 226 route records and all counts;
  only source-hash freshness pins changed, including older stale pins. Client
  usage regeneration changes only the route-coverage input hash, retaining
  201 routes / 1 unmatched / 25 unused.
- TS v2.37.2 rollback: exit 0; upstream booted and progress/bookmarks/sessions
  matched, with eight sessions. Rebuilt binary and rollback evidence:
  `/private/tmp/abs-podcasts-pc01-shipping/audiobookshelf-go`,
  `/private/tmp/abs-podcasts-pc01-rollback/rollback-report.json`.

Local Go was 1.27.0, Node 22.23.4, with native FFmpeg/WebP from TESTING.md.
Advisory TS diagnostics and broad differential scoring were not rerun. No live
deployment is part of PC01.
