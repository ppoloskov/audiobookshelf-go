# Agent guide: Go backend

Read [ARCHITECTURE.md](ARCHITECTURE.md) first: the two contracts, the design
rules, the packages, the foundation APIs and the conventions. Then
[handover.md](handover.md) (current state and open follow-ups) and
[ROADMAP.md](ROADMAP.md) (what is next). The Go engineering rules and review
checklist are in the root [agents.md](../agents.md).

## Roles

- The architect (an expensive model) designs, writes task briefs, reviews
  diffs and commits.
- Workers run on Sonnet or a cheaper model at medium effort and execute one
  brief each. A worker does not commit, push, add dependencies or change files
  outside its write set.

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
   `contracts/mobile-client-audit.md`, then choose the simple behaviour and
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

`make check` at the repository root runs the first line. Before a push: the
rollback check (ARCHITECTURE "Testing") and both Linux cross-builds
(`GOOS=linux GOARCH=amd64|arm64 CGO_ENABLED=0 go build ./cmd/audiobookshelf-go`).
Development is on `master` only. Deployment is separately authorised.
