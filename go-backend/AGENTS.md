# Agent guide: Go backend

Read [ARCHITECTURE.md](ARCHITECTURE.md) first. It defines the two contracts,
the design rules and the target packages. [ROADMAP.md](ROADMAP.md) is the task
list; [handover.md](handover.md) is the current state. The Go engineering
rules are in the root [agents.md](../agents.md).

## Roles

- The architect (an expensive model) designs, writes task briefs, reviews
  diffs and commits.
- Workers (cheap models) execute one brief each. A worker does not commit,
  push, add dependencies or change files outside its write set.

## Rules for a worker

1. Write only inside the directories your brief names. Other packages are
   being rewritten by other workers at the same time, in the same checkout.
2. Build and test only your own packages
   (`go build ./internal/x/... && go test ./internal/x/...`). A failure
   elsewhere may be another worker's half-finished edit; report it, do not
   fix it.
3. New code imports the foundation (`db`, `httpx`, `auth`, `settings`,
   `events`, `log/slog`) and never the old shared packages listed under
   "Transition" in ARCHITECTURE.md, unless the brief allows one by name.
4. Follow the quirk policy: keep a behaviour only for a persisted value or a
   real client. When unsure, look at `../client` and
   `contracts/mobile-client-audit.md`, then choose the simple behaviour and
   list it in your report.
5. Port the old package's HTTP-level tests to the new package. Drop a test
   that only pins JavaScript emulation or an unexported helper, and list
   every dropped test with the reason.
6. Compare JSON by value, never by bytes.
7. Use `db.Tx` for every read-modify-write. Inside it, pass the `ctx` the
   function received.

## Report format

End with: files written; what was verified (commands and results); behaviour
differences from the old code, one line each; tests dropped and why; open
questions. Never report a check you did not run.

## Checks before a commit (architect)

```sh
cd go-backend
gofmt -l . && go vet ./... && go test ./... -count=1      # FFmpeg prefix: TESTING.md
CGO_ENABLED=1 go test -race ./internal/<changed>/...
```

Before a push: the rollback check (TESTING.md) and both Linux cross-builds.
Development is on `master` only. Deployment is separately authorised.
