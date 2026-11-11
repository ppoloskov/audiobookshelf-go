# User API simplification — UA01

Implemented locally on 2026-11-10 by the owner-requested Terra worker,
reviewed by the architect. Publication is pending integration with the
unrelated work in the shared master checkout. No deployment is claimed.

## Ownership

All files remain in `internal/userapi`; no new package or dependency was added.

| File | Responsibility |
| --- | --- |
| `userapi.go` | Public Handler/Credentials/Deps facade, constructor, route/auth registration and common dispatch helpers |
| `account.go` | Account reads/admin writes, password change and account projections |
| `api_keys.go` | API-key routes and their response projections |
| `sessions.go` | Refresh-token session listing/deletion and session device description |
| `progress.go` | Progress reads/writes, continue-listening visibility, in-progress items and admin progress enrichment |
| `bookmarks.go` | Bookmark reads/mutations and item-access guard |
| `listening.go` | Own/admin/item listening routes, session projection and listening totals |
| `year_stats.go` | Annual totals, cover selection and rankings |
| `ereader_devices.go` | Current-user e-reader device policy and mutation |

The mixed `me_extra.go` was removed. Runtime still constructs one Handler;
`New`, `Handler.Routes`, `Credentials`, `Deps`, all routes and authorization
wrappers retain their signatures/registration. Server, identity, realtime and
playback require no caller edits.

## Actual simplifications

- `/api/me` reuses the existing user-payload constructor.
- In-progress items no longer maintain a seen set for unique progress primary
  keys. Distinct progress rows for one item remain distinct response entries.
- Listening totals use concrete float accumulators and date/day maps, removing
  nested dynamic-map assertions. Legacy session construction remains the
  source for date, null, prefix and metadata semantics.
- Annual ranking rounds directly into sortable entries, deleting the two
  intermediate rounded-map builders and duplicate output-key branches.
- Series visibility uses `slices.Contains`/`slices.DeleteFunc` and one length
  comparison. Hiding preserves existing duplicates; readding removes all
  matches. Repeated actions do not write or emit.
- Account PATCH string-type validation uses one ordered field/error table;
  the existing missing/null/empty handling and validation precedence remain.

Emitting `user_updated` and subsequently reloading its response were retained:
collapsing them can change behavior when an emitter callback changes state.
Opaque persisted extraData/bookmark properties remain intact. No HTTP behavior,
schema, persisted format, token policy or event-recipient change is intended.

## Measured tradeoff and review

| Package metric | Before | After | Delta |
| --- | ---: | ---: | ---: |
| Handwritten production lines | 1,534 | 1,568 | +34 |
| Cyclomatic complexity | 396 | 379 | −17 |
| Functions with complexity above 15 | 6 | 4 | −2 |
| Dynamic map uses | 59 | 53 | −6 |
| `[]any` uses | 15 | 14 | −1 |

The initial net-LOC reduction target was not achieved. The architect accepted
the +34 LOC ownership cost within the revised +60 budget, with measured
complexity/map reductions. Imports and headers across cohesive files account
for the size tradeoff; file relocation is not claimed as a deletion.
Only UA01's measured delta was applied to the shared ratchet baseline, retaining
the pre-existing PB04 baseline changes.

Contract review compared parsed declarations before/after to distinguish moves
from implementation edits. Counterexamples checked: invalid-field precedence
and root denial before body validation; null/empty PATCH values; repeated series
actions, duplicate IDs and preserved unknown extraData; distinct progress rows
for one item; zero/negative listening seconds, legacy ID normalization and first
metadata retention; rounded ties, blank ranking names and empty rankings.
New tests were run before the selected refactors and afterward. No existing
test assertion was weakened. No blocking contract finding remains.

Go simplicity review accepts concern files and direct standard-library slice
operations without new services, repositories, interfaces or adapters. The
existing per-entry database reads and broader persistence/concurrency behavior
remain outside this bounded refactor; no batching/performance gain is claimed.

## Validation and cold-start notes

Clean validation source: `/private/tmp/abs-userapi-ua01-shipping`, from
`bf72cb3c16efebadc60ed7a4f12d7f803a8c7326` plus exactly `internal/userapi/`
(including removal of `me_extra.go`) and the UA01-only metric baseline change.
It excludes the unrelated PB04 working changes.

- Full `CGO_ENABLED=0 go build ./...`, `go vet ./...`, and `go test ./...
  -count=1`: passed with the native FFmpeg prefix in TESTING.md.
- Final full suite used `NODE_PATH` pointing to the checkout's installed
  Socket.IO client. The named real-library interop test also executed and passed.
- Focused userapi/users/progressdto/deviceinfo tests and userapi race tests:
  passed; package gofmt check is empty.
- golangci-lint v2.15.0: passed, zero issues. Use the actual mise installation
  executable: the shim has no configured version in an archive, while the
  older `~/go/bin` binary was built with an incompatible Go version.
- Isolated ratchet/dbnest: passed after the reviewed UA01 baseline change.
  Shared-worktree metrics also include other active edits; reconcile those
  separately rather than attributing their decreases to UA01.
- Source-inventory and route-catalog unit tests, `git diff --check`: passed.

Go's default build cache was sandbox-restricted; checks used
`GOCACHE=/private/tmp/abs-userapi-go-cache`. Full listener-based tests ran with
the necessary loopback access. No schema/query input changed, so sqlc output
was not regenerated. Full publication CI mirror, existing stale route-source
provenance hashes, TS rollback and commit/push remain the combined integration
step. No upstream inventory, recorded expectations or assertion manifest was
rewritten to improve a score.
