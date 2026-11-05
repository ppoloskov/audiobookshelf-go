# Go backend handover

Updated 2026-11-06. Canonical checkout:
`/Users/paul/projects/audiobookshelf-go`, **master only**, fork
`https://github.com/ppoloskov/audiobookshelf-go`. No development branches or
implementation worktrees. Expensive architect plans/reviews; cheap workers
research/execute bounded packets. Reviewed ready work may be pushed; deployment,
route removal and new dependencies retain separate owner decisions.

Read [AGENTS.md](AGENTS.md), [ARCHITECTURE.md](ARCHITECTURE.md), then
[ROADMAP.md](ROADMAP.md). This file is live handover; superseded checkpoints,
logs and recovery chronology are in the
[historical handover](archive/reviews/2026-11-06-handover-before-e01.md).

## Current state and next action

Last runtime commit **82110311**, preceding audit documentation commit **2fb4cde9**.
B01–B09, I01–I06, L01–L05, PR01–PR07, MC-05/F1, S9b-R1 and D01 are accepted.
Lists HTTP/write/feed ownership is complete; do not restart L04. Wider
book writes/mixed-media/people/progress work remains. S9b deletion and full
mobile/device acceptance remain open.

**Preserve all functionality.** D02 backup/restore and manual audio-tool removal
was discarded before publication. Those features, providers/matching and ebooks
remain supported. Do not reuse `/tmp/abs-d02` removal snapshots or request cuts
to reach a line target. Restored backup/audiotools and manager restore/rollback
tests passed at `/tmp/abs-d02-rollback-verify-20261105`.

E01 source research is completed in [DELETION-FEASIBILITY.md](DELETION-FEASIBILITY.md).
It did **not** establish a thousand-line wave. A–D total 174–184 estimated net;
F–H add 70–126 conditionally. No E01 runtime code/dependency changes or runtime
tests. The owner retains current custom Engine.IO/Socket.IO framing and cancelled
the library-replacement initiative (E02a–c). C25 documents the earlier
auth-before-ACK race. Ordinary transport fixes and contract tests remain
in scope. Next is E03, the bounded behavior-preserving cleanup. No new domain
extraction is dispatched. Roadmap cards own status; estimates are not delivery.

## Last runtime acceptance

Production baseline **44,674 handwritten Go lines / 10,809 complexity / 990 maps /
326 []any / 178 high-complexity functions**. L04/L05 reduced lines by 29 and
complexity by 23, with no metric growth. Build, vet, full native-codec tests,
lint (zero), Linux arm64, strict ratchet/dbnest and sqlc nine-file identity passed
in clean validation snapshot `/tmp/abs-pr06-pr05-20261105/l04c-final`.
No local TS rollback, device or benchmark run was claimed. E01 is docs only;
these runtime results are historical acceptance, not newly rerun evidence.

226 ordered route records / 21 lists owners, 0 verified / 226 unverified;
201 recorded client routes. Inventories are coverage evidence, not mobile
completeness. Refresh both route ownership and client-usage outputs after
ownership edits. Keep V2 fixture/shape/browser/benchmark/rollback inputs until
standalone extraction and reproducible regeneration are proven.

## Gotchas and preserved owner work

- Preserve untracked `.codemap/`, `go-backend/TODO.md` and
  `internal/identity/routes.go`. The last currently fails compilation because
  `httputil.NotFound` is absent. Do not stage/fix/reset owner WIP; use a clean
  tracked-source validation archive plus exact approved overlays. This is a
  validation snapshot, not a development branch.
- Recovery stash `aeedc216bc2cee564d4c48b9470bf9e3139a744d` and
  `/tmp/abs-master-consolidation-20261105/original-stash.bundle` remain.
  Do not pop/drop or delete recovery data. Old `/private/tmp/audiobookshelf-packet1-88db`
  contents were not verified clean; retain them.
- Never run TS and Go on the same writable SQLite/config/metadata tree. No
  schema migrations or boot cleanup. Close Rows/Tx before a new pool query;
  preserve dbtime and opaque stored JSON meaning.
- Native FFmpeg requires libwebp. Use **both PATH and FFMPEG_PATH** from
  [TESTING.md](TESTING.md); default global FFmpeg causes five image failures.
  Node reference tools need Node 22, built dist-server and isolated fixtures.
  Use physical `/private/tmp` paths on macOS or scanner fixtures can seed zero items.
- Socket.IO JS interop can silently skip without Node modules; set
  `NODE_PATH=/Users/paul/projects/audiobookshelf-go/node_modules` in isolated
  validation. Require actual execution. Current transport is WebSocket-only
  Engine.IO4/default namespace; frame cap is 32 MiB, not unbounded.
- `items.Reader` still returns outer maps containing typed book structs;
  mixed-media migration is incomplete. Collections may remain visible with
  all members filtered; playlist ownership and library-denial semantics differ.
- Emitter guards often protect lazy DB/projection/payload work. Removing a nil
  check merely because the final emit arguments are variables is unsafe.
  Shared HTTP error helpers must preserve token redaction and source attribution.
- OpenCode Flash source transfer was rejected by automatic approval review;
  built-in cheap workers are the fallback. No bypass or external provider
  permission is assumed. [SKILL-REVIEW.md](SKILL-REVIEW.md) documents proportionate
  Go skill overrides; do not reintroduce a parallel ledger or byte-parity gates.

## Documentation acceptance

E01 records source/manifest/arithmetic/contract review, local-link checks and
`git diff --check`; no Go suite is needed or claimed for prose-only changes.
All completed cards need individual `[x] DONE` markers with actual validation
in the same accepted commit. Keep README, AGENTS, roadmap and this handover
current; archive historical status instead of leaving contradictory next steps.
