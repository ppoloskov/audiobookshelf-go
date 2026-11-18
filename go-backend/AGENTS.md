# Agent guide: Go backend

Read [ARCHITECTURE.md](ARCHITECTURE.md) first: the two contracts, the design
rules, the packages, the foundation APIs and the conventions. Then
[handover.md](handover.md) (current state and open follow-ups) and
[ROADMAP.md](ROADMAP.md) (what is next). The Go engineering rules and review
checklist are in the root [agents.md](../agents.md).

## Roles

Verification-tooling foundation is architect-accepted; the overall coverage task
TASK-20261113-unified-verification remains in progress. Packet names and directory names must match the keys in
`testmachine/scenarios/packets.json`. Keep the canonical invariant IDs, shared tests and
generated mapping in sync, and obtain both packet reviews and the cross-packet inventory audit.
User requires one Go-first/.mjs machine and upstream completeness, retaining the
defined contract exclusions. The active task delivers the test machine. Run
`make verify VERIFY_ARGS="-target upstream"` for its acceptance. Go assessment
is a separate task. See testmachine/VERIFICATION.md. Keep one bounded owner packet per
invocation after every scoped review. Q01 feature implementation is superseded: owner families
first bind source-derived assertions into testmachine/scenarios, with architect review.
Reopen scenarios stop the writer and use pinned upstream on the same disposable
install. Assert concrete row, file and client values on either side of reopen.
For configuration failures, distinguish validation rejection from a rejected
database write and verify the persisted state independently of process behavior.
For Q01, use the [queue review](testmachine/reports/q01-embed-queue-review.md) for the characterized transitions and remaining tools obligations. A successful packet diagnostic supplies scoped evidence. On handoff, check packet constructors and their canonical invariant bindings as well as files on disk. See [handover.md](handover.md) for the current discovery failure and gaps.

Current parity work is limited to one ownership packet per invocation. The
accepted playback lifecycle packet is recorded in
[TASK-20261113-playback-lifecycle](../docs/agent-work/archive/tasks/TASK-20261113-playback-lifecycle.md).
Both P2 findings are closed by architect re-review. No later application feature packet has been
dispatched; set its owner, write set, acceptance criteria and budget at dispatch.
`media` owns FFmpeg path resolution and execution. The user authorized the narrow
successful-process-start notification on 2026-11-13; lifecycle state stays in
playback.
The two P2 revisions are implemented: media uses `exec.Cmd.WaitDelay` to bound
post-cancellation pipe draining; live progress must include nonzero updates for
each owner before close. The packet is architect-accepted; whole-project parity
is still unproven.

Q00 inventory is architect-accepted; it changed no application production code. Canonical requirements are
`testmachine/register/capabilities/*.json`; the generated requirement-to-test mapping
and ordered queue are in `testmachine/reports/replacement-{requirement-map,gap-queue}.md`.
Run `node go-backend/testmachine/scripts/replacement-contract-map.mjs --check` from root.
Do not infer proof from old reports, a shape skeleton or a test name. Add newly
discovered in-contract obligations and refine sampled invariants before accepting
an owner packet. Q01 application fixes are deferred by the unified-machine direction; exact
write set and a finishable shared-assertion subset are required at dispatch. See ROADMAP and the
map task for acceptance state. Live deployment is separately authorized.

- The architect (an expensive model) designs, writes task briefs, reviews
  diffs and commits.
- Workers run on Sonnet or a cheaper model at medium effort and execute one
  brief each. A worker does not commit, push, add dependencies or change files
  outside its write set.

## Agent cost policy

Owner direction, 2026-11-15.

- Subagents run on Sonnet at medium effort. No larger model unless the owner
  asks.
- At most 3-4 subagents in parallel per wave.
- No owner/reviewer loop on every packet. At most one review pass, for
  failures, high-risk packets or a sampled subset; that reviewer fixes what it
  finds.
- Before launching more than 10 agent tasks, give a plan with an estimated
  cost and get the owner's confirmation.
- Highest-value work first, so a usage-limit stop leaves it done.
- Say at once when no subagents are running although delegated work is
  expected.
- Cluster test failures by root cause before fixing anything.

## Rules for a worker

1. Write only inside the files your brief names. Other workers may be editing
   other packages in the same checkout.
2. Build and test your own packages
   (`go build ./internal/x/... && go test ./internal/x/...`). A failure
   elsewhere may be another worker's half-finished edit; report it, do not fix
   it.
3. Respect the import order in ARCHITECTURE "Packages": import only packages
   below yours. For a call upward, add a func field to the package's `Deps`
   and let `internal/server` fill it.
4. Follow the quirk policy: keep a behaviour only for a persisted value or a
   real client. When unsure, look at `../client` and
   `testmachine/contracts/mobile-client-audit.md`, then choose the simple behaviour and
   list it in your report. Add it to [docs/CHANGES.md](docs/CHANGES.md) if a
   client can see it.
5. Test through the package's routes on `dbtest.New` or `dbtest.Fixture`.
   Assert decoded values, rows and events; compare JSON by value
   (`dbtest.EqualJSON`), never by bytes. Add a journey in
   `internal/contracttest` for a new client-visible flow.
6. Use `db.Tx` for every read-modify-write and pass the `ctx` it gives you.
   Emit events after the transaction returns.
7. A PATCH body is a struct of pointer fields; use a null-recording type where
   a real client sends `null` to clear a value (ARCHITECTURE "Conventions").
8. Log with `log/slog`. Do not add calls to the `internal/logging` printf
   helpers.
9. New client-visible routes, fields or events need evidence of a client that
   uses them.

## Report format

End with: files written; what was verified (commands and results); behaviour
differences, one line each; tests dropped or changed and why; open questions.
Never report a check you did not run.

## Checks before a commit (architect)

```sh
cd go-backend
gofmt -l . && go vet ./... && go test ./... -count=1      # FFmpeg prefix: TESTING.md
CGO_ENABLED=1 go test -race ./internal/<changed>/...
```

`make check` at the repository root is an alias for the upstream-first
`make verify` gate. Before a push: the
rollback check (ARCHITECTURE "Testing") and both Linux cross-builds
(`GOOS=linux GOARCH=amd64|arm64 CGO_ENABLED=0 go build ./cmd/audiobookshelf-go`).
Development is on `master` only. Deployment is separately authorised.
