# Agent guide: Monstrobook

Read [ARCHITECTURE.md](ARCHITECTURE.md) first: the two contracts, the design
rules, the packages, the foundation APIs, and the conventions. Then
[handover.md](handover.md) (current state and open follow-ups) and
[ROADMAP.md](ROADMAP.md) (what is next).

North Star: simple, clear, fast Go (`monstrobook`) replacing TypeScript
Audiobookshelf v2.37.2, with fixed persisted data/rollback and real-client
protocols. All development is on `master`; do not create development branches.
Keep [README.md](README.md), [AGENTS.md](AGENTS.md), [ROADMAP.md](ROADMAP.md),
and [handover.md](handover.md) current with each accepted packet.

## Verification & Roles

The authoritative verification machine is the **Twin-Server Differential Oracle**
under [testmachine/oracle/](testmachine/oracle/README.md), replacing the retired
hand-written scenario/register/parity/contracttest stack (~530k LOC removed on
2026-11-20).

- Run `make upstream` (once, to fetch pinned TS v2.37.2 into gitignored
  `.oracle-upstream/`), then `make verify` (alias `make check` / `make oracle`)
  from the repository root with Node 22 and the WebP FFmpeg prefix in
  [TESTING.md](TESTING.md). It boots pinned TS v2.37.2 (`NODE_V8_COVERAGE`)
  and Go (`GOCOVERDIR`) side by side, diffs HTTP, all 26 SQLite tables +
  `typeof()`, 3-audience Socket.IO, FS, and outbound stubs, verifies
  bidirectional TS↔Go rollback, checks real web/mobile client coverage, and
  writes `go-parity-backlog.md`.
- Run `make oracle-unit` for fast pure-Go `-race` differ/mutation tests.
- Intentional Go improvements over upstream bugs live in
  [testmachine/oracle/divergences.json](testmachine/oracle/divergences.json).
- Approved V8 coverage exclusions live in
  [testmachine/oracle/coverage-exclusions.json](testmachine/oracle/coverage-exclusions.json).
- Current active phase: **Go parity refactoring & fixes**, driven by the
  prioritized `go-parity-backlog.md` emitted by `make oracle`. Live deployment
  remains separately authorized.

- The architect (an expensive model) designs, writes task briefs, reviews
  diffs and commits.
- Workers run on a cheaper capable model and execute one bounded brief each. A
  worker does not commit, push, add dependencies or change files outside its
  write set.

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
   real client. When unsure, look at `.oracle-upstream/client` and
   `testmachine/contracts/mobile-client-audit.md`, then choose the simple
   behaviour and list it in your report. Add it to [docs/CHANGES.md](docs/CHANGES.md)
   if a client can see it.
5. Test through the package's routes on `dbtest.New` or `dbtest.Fixture`.
   Assert decoded values, rows and events; compare JSON by value
   (`dbtest.EqualJSON`), never by bytes. Add declarative flow steps under
   `testmachine/oracle/flows/` for new client-visible flows.
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
gofmt -l . && go vet ./... && go test ./... -count=1      # FFmpeg prefix: TESTING.md
CGO_ENABLED=1 go test -race ./internal/<changed>/...
```

`make check` at the repository root is an alias for the upstream-first
`make verify` gate (which includes bidirectional TS↔Go rollback). Before a push:
both Linux cross-builds (`GOOS=linux GOARCH=amd64|arm64 CGO_ENABLED=0 go build ./cmd/monstrobook`).
Development is on `master` only. Deployment is separately authorised.

---

# Go engineering rules

Two contracts are fixed: the SQLite database stays compatible with upstream
v2.37.2, and existing clients keep working (routes, the JSON fields and types
they read, the statuses they branch on, Socket.IO events). Everything else is
ours to design; see [ARCHITECTURE.md](ARCHITECTURE.md) for the quirk policy.
Where a rule below mentions matching upstream or JavaScript behaviour, it
applies only when one of the two contracts needs it. State every behaviour
change in your report.

## 1. Types over maps
- Never model a document as `map[string]any` plus string-key loops. Use a struct
  with json tags. Decode a PATCH body onto the loaded struct (only keys present
  are overwritten), then validate.
- A struct's zero value must be safe. Set non-zero defaults (for example
  `RejectUnauthorized: true`) BEFORE decoding, so an absent key never becomes an
  insecure default.
- Keep `any` only for genuinely open-ended data, and comment why.
- Never use a `bool` parameter to switch behaviour (`settings(w, r, patch bool)`).
  Write two functions.

## 2. Validate at the edge, once
- Wrap every request body: `http.MaxBytesReader(w, r.Body, limit)`.
- Validate each input once, after parsing: ranges, emails, hosts, enums. Reject
  with a 4xx. Do not silently coerce bad input (a bad port must not become 465)
  unless upstream does, and then comment that it does.
- Never assert types on untrusted data without the comma-ok form. `x.(string)`
  panics.
- Reject negative or zero values for durations, sizes and limits explicitly.

## 3. Errors
- Handle or deliberately ignore every error. An ignored error needs a comment
  (`_ = json.Unmarshal(...) // malformed tags are treated as none`).
- Wrap with context: `fmt.Errorf("rcpt to: %w", err)`. Use `err`, not `e`.
- Plain `if err != nil { return err }` chains are idiomatic; do not build
  try/catch emulations, and never use panic/recover for control flow.
- `panic` only for programmer errors (corrupt embedded asset) and prefer
  `sync.OnceValue` so it fires at first use, not at import.
- Do not leak internal errors (SMTP, DB, paths) to non-admin users. Log the
  detail, return a fixed message.
- Check preconditions that need no I/O BEFORE doing I/O.

## 4. Security
- Anything that touches secrets (`pass`, `tokenSecret`, JWT keys) must be
  excluded from responses and logs. Add `String()` redaction to structs that
  hold secrets so `%+v` cannot leak them.
- Every route must state its authorisation. Admin-only routes use the
  `auth.Admin` wrapper. Permission checks keep upstream's order.
- Do not trust values read from the DB for headers or paths without validation
  (CR/LF in header values, `filepath.Base` for names).
- `InsecureSkipVerify` only behind an explicit setting, default secure, with a
  warning logged when disabled.

## 5. Resources and performance
- Never read an unbounded file into memory. Check `Stat().Size()` against a
  named limit and return an error (never truncate silently with `LimitReader`).
  Prefer streaming (`io.Copy`, `base64.NewEncoder`) over `ReadAll`.
- Deadlines: separate dial/handshake timeouts from whole-session limits. Do not
  put a short blanket deadline on a connection that carries a large payload.
- Use context-aware APIs (`tls.Dialer.DialContext`, `db.DB` methods with the request `ctx`).
- Anything computed once per process (time zone, defaults) is cached with
  `sync.OnceValue`, not recomputed per request.
- Defaults and other shared package-level data are returned as DEEP copies.
  `maps.Clone` and `slices.Clone` are shallow.

## 6. Concurrency and state
- A read-modify-write on stored settings must be atomic. Use one
  `Update(ctx, func(*T))` that holds a lock or transaction, not `Get` then `Save`.
- Prefer state owned by a struct (`Store`) over global registries keyed by
  pointer (`sync.Map` + `Forget`). Whoever creates it owns its lifetime.
- When a cache can race with invalidation, hold the lock across the read, or use
  a generation counter. Write the invariant in the doc comment.
- Do not write to the DB when nothing changed unless you have decided it is
  harmless; if you keep change detection, use one mechanism (struct `==` or one
  `reflect.DeepEqual`), not three.

## 7. Matching JavaScript behaviour
No JS value emulation (truthiness, `Number()`) remains in the code. The rule
now applies to the few places that keep JS-defined logic: `internal/text`
(collation and natural sort, name parsing), whose tests compare against
fixtures generated by Node (`internal/text/testdata`,
`testmachine/scripts/gen-collation-fixture.cjs`).
- Code that mimics JS semantics (collation, name parsing, number formatting) must be
  proven against real Node output, not guesses. Write a table test whose expected
  values come from `node -e`. Always include the edge cases: `0`, `-0`, `""`,
  `"0"`, `[]`, `{}`, `1e20`, `1e21`, `0.000001`, `0.0000001`, nested arrays with
  null.
- `fmt` verbs are NOT JS formatting. Use `strconv.FormatFloat` and apply the JS
  exponent rules.
- Comment every deliberate deviation or quirk with the upstream source location
  (`// like nodemailer: other ports use STARTTLS`).

## 8. Code organisation
- One file, one concern. Do not mix HTTP handlers, persistence, transport (SMTP),
  encoding (MIME) and config in one file. Transport and encoding code takes plain
  arguments, so it is testable without a DB or HTTP.
- Handlers stay thin: decode, validate, call, encode. Long handlers (60+ lines)
  extract a loader (`loadEbook`) that returns data or an HTTP error.
- Raw SQL lives in the domain package that owns the data (or `internal/db`), not in HTTP handlers.
- Prefer the standard library over hand-rolling: `mime/multipart`,
  `mime.FormatMediaType`, `mime.QEncoding`, `net/mail`, `strconv`.
- Delete dead code: no no-op `TrimSuffix`, no redundant `filepath.Clean` before
  `Abs`, no `SetOutput(os.Stderr)` when it is already the default.
- Name things plainly: `err`, `settings`, not `e`, `m`. Do not shadow functions
  with variables (`device := device(...)`). Names describe what the function does
  (`urlQuery` that path-escapes is misnamed).
- Group imports: stdlib, blank line, module imports (`goimports -local`).
- Every exported identifier has a doc comment. Say any surprising behaviour
  (aliasing, "same slice returned", "zero value means not loaded").

## 9. Configuration
- Read the environment in one place. Inject it (`getenv func(string) string`) so
  tests do not need global state.
- Parse env values with one policy: either strict (error) or lenient (default +
  logged warning). Do not mix them. Validate once, after flags are parsed.
- Declare short and long flags through a helper so they cannot drift.
- Config structs that carry secrets implement redacting `String()`.

## 10. Tests and verification
- Behaviour-preserving refactors need tests FIRST (characterisation tests for
  status codes, messages and JSON shapes), then the refactor.
- Add tests only for what you introduce or fix: table-driven, one case per edge.
- Run before declaring done: `gofmt -l .`, `go vet ./...`, `go test ./...`
  (`-race` for anything with locks, caches or goroutines), and `golangci-lint run ./...`
  if installed.
- Say what you verified and what you could not.

## Agent workflow: improving existing code
1. Read the whole package first; list public routes, exported symbols and
   behaviours that must not change.
2. Write or confirm characterisation tests for those behaviours.
3. Fix bugs first (security, data loss, JS mismatches), then simplify, then
   reorganise files. One concern per commit.
4. Report: behaviour differences (explicit list), assumptions about code you
   could not see, and what you did NOT change.
5. Do not touch unrelated code. Do not add dependencies without asking.
6. If a rule here conflicts with upstream parity, parity wins; leave a comment
   with the upstream reference.

## Review checklist (for humans and agents)
- [ ] Bodies size-limited and validated; no unchecked type assertions
- [ ] No ignored errors without a comment; errors wrapped; no internal detail
      leaked to non-admins
- [ ] Secrets absent from responses, logs and `%+v`
- [ ] No unbounded reads; deadlines fit the payload; context honoured
- [ ] Read-modify-write is atomic; no global state without an owner
- [ ] JS-mimicking code has Node-derived tests
- [ ] One file, one concern; no dead code; names and imports tidy
- [ ] `gofmt`, `go vet`, `go test -race` clean
