# Q01 metadata embed queue review

On November 14, 2026, the tools diagnostic passed three shared scenarios on upstream v2.37.2 at `edb1f7efc92ecacdef51aafb4d9d5054ce10604f`. The full verifier stopped during plan discovery. Go assessment and rollback remain blocked. [Scenario proof records, measured inputs and full verdict](q01-embed-queue-run.json).

## Observed behavior

| Request and starting state | Required observation on each socket |
| --- | --- |
| Embed one book while the worker is idle | The book's task starts and finishes successfully. The queue emits no transition. |
| Submit books A, B and C in a single batch while idle | A starts immediately. Both admins receive B and C admissions with `queued: true`. Each authenticated socket receives each waiter's `queued: false` after its predecessor finishes and before its own task starts. B finishes before C starts. The anonymous socket receives none of these task or queue events. |
| Complete a batch, then submit the same books in a different order | Both runs finish in submission order. The new first book starts immediately and only its two successors produce queue events. The subtest establishes both rounds itself. |

Each scenario uses the public HTTP API, the fixture's audio files, FFmpeg, and six Socket.IO clients. The audiences are the submitting root, a second admin, an ordinary listener, a user limited to a different library, a user whose access to the book's library is removed while connected, and an anonymous connection. The setup verifies the revoked account's item access changes from 200 to 403; it also verifies 403 for the restricted account. A terminal task event and a later pong on each connection bound the transcript. Assertions check the spelling, type and value of the two consumed queue fields, the identity of each finished task, and the order and multiplicity of events on each socket.

The batch controller completes validation before admission. The metadata manager registers the first task as running before its first asynchronous filesystem operation. This makes the two waiting admissions reproducible with real media. The client reads `libraryItemId` and `queued` to add or remove a waiting item. [Admission and dequeue](../../../server/managers/AudioMetadataManager.js) · [Task events](../../../server/managers/TaskManager.js) · [Socket audiences](../../../server/SocketAuthority.js) · [Client event handler](../../../client/layouts/default.vue) · [Client state](../../../client/store/tasks.js).

## Reproduction and limits

Use Node 22.23.4, Go 1.27.0 and WebP-enabled FFmpeg as described in [TESTING.md](../../TESTING.md). From the Go module, run `go run ./testmachine/cmd/verify -node /absolute/path/to/node22 -packets tools` for the graded packet and `go run ./testmachine/cmd/verify -gaps -packets tools` for the unfilled scenario requirements. Use `make verify` from the repository root for the whole verdict.

The independent reviewer exercised the final per-socket comparator with disposable Go test overlays. The valid transcript and a transcript with an extra unused payload field passed. Moving one listener’s dequeue after its task start, omitting `queued` on a dequeue, and changing `libraryItemId` to `libraryitemid` each failed. The review accepts the three scoped tuples and leaves the tools family open. [Independent receipt](../contracts/verification-review/tools.json).

The graded packet exited 0 with all three proof records matched to passing subtests. The worker also ran three consecutive complete packets, then ran the self-contained repeat subtest twice after the final simplification. The final packet passed under the race detector. Scoped formatting, tagged vet and tagged lint passed. The tools gap diagnostic lists 144 overlapping scenario obligations. The V8 audit recorded unexecuted code in the file-access, cache-creation and embed-failure branches of AudioMetadataManager and the duplicate-admission and encode/cancel paths of ToolsController. Queue advancement on failure, persisted audio/backup changes, progress and task restoration remain open.

The current register has 345 requirements and 503 invariants. Whole-suite discovery, repository-wide Go tests and vet fail at six missing scenario functions in [the inherited playlists packet](../scenarios/playlists/packet.go). The full report's null coverage gaps mean discovery stopped before the calculation. A separate read-only check of the partial plan found 97 declaration/register mismatches in auth, author, browse and user_admin. The structural ledger check also rejects invariant classes in six unrelated route records (R205, R209, R210, R212, R213 and R216). The regenerated map and its checks pass. The [earlier integration](unified-verification-integration-20261114.json) used a different register and worktree.
