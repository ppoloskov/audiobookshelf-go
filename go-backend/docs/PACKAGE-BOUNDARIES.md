# Package and object boundaries proposal

2026-11-11. Status: email-settings, catalog read graph (PB02a–d), people
entities (PB03a) and the people remainder/library scope (PB04a) are accepted;
PB04b1 (filter/sort compiler and series), PB04b2a (library metadata reads) and
PB04b2b (item browsing, collapse helpers and item includes) are accepted;
PB04b3 (search, shelves and library-scoped author lists) is implemented
pending review. Scope: current Go package ownership, catalog decomposition, settings,
shared objects and dependency rules. This is a response to the owner's request
for better package division, rather than another line-reduction exercise.

## Findings

Counts below are the physical-line snapshot recorded during the 2026-11-10
source review for top-level non-test `.go` files, including comments, blanks
and then-current untracked files. They are a dated size signal, not current
checkout counts, ratchet measurements or a validated shipping-source inventory.

| Priority | Evidence | Consequence and smallest architectural repair |
|---|---|---|
| High | `internal/catalog`: 41 production files / 8,744 lines; `handler.go:29-66` shares DB, whole config and callbacks across authors, libraries, items and matching | Unrelated operations have one dependency surface. Separate owners along actual operations, replacing the corresponding Handler methods and callers together. |
| High | `.golangci.yml:129-179` bans feature-to-feature imports; `playlistview/view.go:43-128` owns queries, response composition and event delivery | Domain behavior is pushed into foundation packages to make it shareable. Permit named acyclic domain dependencies; prohibit importing HTTP adapters instead. |
| High | `catalog/mutation.go:114-260` combines arbitrary stored columns, JSON request semantics and book/podcast mutation rules | Storage representation and HTTP coercion become the application model. Decode compatible input at the edge into typed commands; retain one transaction owner and explicit post-commit work. |
| Medium | `progressdto/apply.go:22-43` defines HTTP status errors and request presence; the package also applies persisted progress updates | A DTO package is actually a progress subsystem. Make progress a domain used by playback, users and login initialization; keep transport mapping at callers. |
| Medium | `settingsdoc/settingsdoc.go:20-27,59-122` owns email defaults and device authorization alongside generic row I/O | A persistence helper is an email-policy owner. Move email policy under email ownership; keep only shared row mechanics below it. |
| Medium | `store/store.go:30-47` owns a credential-bearing User; adjacent files own request context, playlists and playback/device records | Database connection ownership has accumulated unrelated domain responsibilities. Move records and operations to named owners while preserving credential-safe response builders. |
| Medium | `serversettings/serversettings.go:58-63,151` and `mediatools/mediatools.go:17-22` read environment outside config | Process config, mutable persisted settings and browser representations are entangled. Pass resolved process options at composition; keep persisted settings and their cache lifecycle separate. |

These are architecture findings, not claims that every identified behavior is
incorrect. Tiny packages alone are not evidence of a problem: `dbtime`,
`deviceinfo`, `textsort` and similar reusable mechanisms can be cohesive.

## Proposed ownership

Use domain packages with explicit operations. Keep HTTP handlers, SQL helpers
and domain logic in separate files initially (`http.go`, `queries.go`,
`service.go`, `model.go`, `response.go`), with different concrete receivers
where state is needed. Files alone do not enforce boundaries: application
operations must accept context and typed inputs, not HTTP requests/writers,
and must be callable by jobs without constructing an HTTP handler. Split an
adapter into a subpackage when consumers or enforcement justify the cost.
Do not create a service/repository/interface per entity by convention.

| Owner | Responsibilities | Current code to migrate or absorb |
|---|---|---|
| `library` | Libraries/folders, browsing, filter/sort compiler, series, search, shelves and aggregate statistics | catalog library/query/search/personalized/series files; filterdata cache |
| `items` | Library-item identity, common files/artwork, item endpoint dispatch, batch orchestration and deletion workflow | catalog item/cover/file/batch code; common libitem data |
| `books` | Book metadata, chapters, book projections and author/series association updates | book-specific catalog mutation code; libitem.Book; relevant itemwrite operations |
| `people` | Author identity, CRUD, matching, images and orphan policy | catalog author files; relevant itemwrite sweeps |
| `podcasts` | Podcast/episode model and projections, metadata updates, feed/download/schedule lifecycle | existing podcasts; podcast-specific libitem and catalog code |
| `lists` | Playlist/collection membership, projections and change notifications | existing lists; playlistview and store playlist operations |
| `progress` | Durable media progress, bookmarks and offline reconciliation | progressdto; relevant userapi/playback operations, after caller inventory |
| `playback` | Active sessions, stream/HLS lifecycle and playback device operations | existing playback; relevant store session/device code |
| `users` | User records, permissions data and safe account projections | store.User, users and userapi account operations; auth/identity retain token/login flows |
| `settings` | Persisted server settings, immutable snapshots, invalidation and generic settings-row mechanics | serversettings; generic part of settingsdoc |
| `emailapi` / notifications | Feature settings defaults, validation, permissions and operations | existing feature code; feature-specific settingsdoc policy |
| `server` | Dependency assembly, route mounting and runtime replacement/shutdown | existing server; no business-operation callback relays through handlers |

First packet decision: email settings defaults and e-reader device policy live
in `internal/emailsettings`, a foundation package, because `identity` and
`userapi` also consume them. `emailapi` keeps the HTTP operations; `settingsdoc`
keeps only generic row loading/saving. This avoids a feature-to-feature import
from identity/userapi to emailapi while preserving the reviewed dependency
direction. The second packet makes `emailsettings.Settings` the typed document;
device entries remain maps so unknown device fields round-trip, while unknown
top-level fields are not persisted because upstream `EmailSettings.toJSON`
does not emit them.

First catalog read-graph slice: the permission projection of an item
(`Access`, `LoadAccess`, `ItemAccess`, `CanReadItem`) now belongs to
`internal/access`, removing the old `access` → `libitem` edge. The second
slice moves common item identity, the batched loader and common response
envelopes into `internal/items`. The third slice moves book identity,
author/series associations, book projections and batched book loading into
`internal/books`. The fourth slice moves podcast models/projections/episode
loading into the foundation read subpackage `internal/podcasts/read`, puts
shared file models in `internal/mediafiles`, and deletes `libitem`. `items`
composes `books` + `podcastread` + `mediafiles`; feature `podcasts` keeps its
HTTP/write paths and consumes the read package. `store.QueryIn` owns the
shared chunked-query mechanics.

People slice: author entity records/views, detail, update/rename, merge/delete,
metadata matching, library-scoped author lists and author images now live in
`internal/people`; series routes and projections moved to `internal/library` in
PB04b1. `internal/library` owns request scope/`InLibrary`, media-table
selection, the filter/sort SQL compiler, series entity/list/detail/update
routes, library metadata reads (list/detail/view, stats, filterdata,
narrators and podcast titles), item-list browsing with collapse/include
handling, library-scoped author lists, search and personalized shelves.
`itemwrite` keeps relation cleanup and orphan sweeps. Dependencies are acyclic:
`catalog` → `library`/`people`; `library` → `people`; people imports neither
catalog nor library. Generic changed-column
saving is the foundation leaf `internal/sqlupdate`.

Keep config for process input; keep store for opening/validating/closing the
database, sqlc for generated storage queries, and mediatools for subprocess
execution/probe formats. Keep backup, scanner, sharing, providers, audiotools
and realtime as existing owners unless a concrete operation demonstrates a
different home. Avoid collecting all of them into a generic admin package.

## Objects and dependencies

- A domain owns the authoritative Go meaning of its objects. SQL rows, input
  commands and API projections may be different types with explicit mapping.
  Do not turn sqlc rows or the global wire package into a universal model.
- `mediafiles.AudioFile` already shares scanner-produced and persisted file
  data. Reuse that representation when semantics match; inventory its callers
  before relocating. A probe Chapter and a persisted AudioChapter have
  different contracts and are not proven duplicate implementations.
- `books.Author` is a book association reference, not a complete author
  entity. Likewise provider BookMatch is not a stored Book. Consolidate
  duplicated policy, not merely similarly named structures.
- Preserve unknown persisted JSON fields and nil/empty/missing distinctions.
  Typed patches need explicit presence; a plain pointer is insufficient where
  absent and explicit null differ. Translate required JS coercions at input
  boundaries, without a general-purpose TS object emulation layer.
- Allow consumers to call another domain's stable data/operation API along
  reviewed acyclic edges. Never call another domain's HTTP handler. Use a
  consumer-sized interface only for a real cycle/inversion or substitution;
  direct concrete dependencies are otherwise preferable.
- Proposed direction: lists/playback/library consume item reads; item reads
  compose books/podcast reads; books consume people association operations.
  Podcast downloads must not depend back on the items orchestrator: supply
  the needed read/update capability at composition, or isolate podcast data
  from the downloader if a proven cycle warrants it. Confirm the full graph
  for each packet before changing lint rules.
- A write operation owns its transaction, using the same tx throughout.
  Return committed change information for metadata writes, invalidation,
  scheduling and events in their established order. Do not replace existing
  ordering with asynchronous events merely to remove a dependency.
- Settings have separate process, persisted and response representations.
  Preserve current cache invalidation and Forget-on-restore semantics; no new
  global settings registry or per-request SQL settings reads.

## Alternatives and review result

Result: **revise the existing target before implementation**. Its domain
direction is useful, but "a domain owns handlers, queries and types" does not
define object/operation boundaries. Conversely blanket horizontal packages
(`handlers`, `services`, `repositories`, `models`) would scatter one workflow.
Renaming catalog files would leave its shared Handler and coupling intact.
Extracting every helper would increase adapters and public surface.

The proposed approach gives operations a real owner while retaining local
SQL and HTTP implementation details. Costs include coordinated caller moves,
test fixture migration and lint changes. Count these honestly; package moves
are not deletions. Existing strict ratchet/growth review remains until a
specific packet records its approved budget. This review neither approves a
baseline change nor implements the proposal.

## Sequence and proof

1. Reconcile inherited unmerged index/source state and establish a known
   passing source snapshot. Preserve Wave G, quickmatch and identity WIP.
2. First bounded ownership packet: separate email-specific settings policy
   from generic settings-row I/O. Inventory identity/userapi consumers too;
   do not break the existing import guard with a hidden exception. Preserve
   device authorization/defaults and Load/Save behavior. This tests the
   ownership/dependency rule with less risk than book mutation extraction.
3. Establish the books/podcasts/common-item read dependency graph together;
   migrate existing libitem models/loaders without a second implementation.
   Implemented as PB02a–d: access, items, books, podcastread and mediafiles
   now own the graph. Then move library browsing and people operations out of
   catalog, one coherent operation/caller set at a time. Preserve
   batching/query counts.
4. Move write workflows with transaction, cleanup, sidecars, events and
   schedules together. Close each adapter before starting another ownership
   expansion. Then migrate progress and remaining store domain records.

| Fixed boundary | Required proof per affected packet |
|---|---|
| SQLite schema, timestamps, JSON, IDs and rollback | persistence tests and TS rollback; no migrations |
| HTTP routes, auth, fields/types/nullability and errors | existing endpoint tests plus request-to-state regressions |
| Files, artwork, download and range semantics | existing file/path/security/range tests; real media where affected |
| Events and write side effects | ordering/recipient tests; failure, retry, batch and rollback cases |
| Runtime/settings lifecycle | invalidation/concurrency/restore tests; affected race checks |
| Performance and structure | fixed-query-count tests; no nested pool queries; reviewed import graph |

Completion means HTTP adapters translate, operations own policy/transactions,
SQL helpers own storage mapping, objects have named owners, and replaced code
and adapters are removed. Package count and LOC alone cannot prove this.
Runtime implementation still needs bounded cards with exact ownership,
deletions/replacements, metric budget and tests under the existing roadmap.

Review validation: inspected source/imports, existing tests, lint rules and
Git status. No runtime suites executed for this documentation-only proposal.
