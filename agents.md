# Agent guidance

Agent instructions live in [go-backend/AGENTS.md](go-backend/AGENTS.md).
Read it, then [ARCHITECTURE.md](go-backend/ARCHITECTURE.md),
[ROADMAP.md](go-backend/ROADMAP.md), and [handover.md](go-backend/handover.md).

North Star: simple, clear, fast Go replacing TS v2.37.2, with fixed persisted
data/rollback and real-client protocols. Expensive architect plans/reviews;
cheap capable workers execute bounded tasks. All development is on `master`;
do not create development branches. Review and push ready work to the fork's
`master`. Live deployment remains separately authorized. Keep README,
AGENTS, ROADMAP and handover current with each accepted packet.

# Go engineering rules

Two contracts are fixed: the SQLite database stays compatible with upstream
v2.37.2, and existing clients keep working (routes, the JSON fields and types
they read, the statuses they branch on, Socket.IO events). Everything else is
ours to design; see go-backend/ARCHITECTURE.md for the quirk policy. Where a
rule below mentions matching upstream or JavaScript behaviour, it applies only
when one of the two contracts needs it. State every behaviour change in your
report.

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
- Every route must state its authorisation. Admin-only routes use the existing
  `access.RequireAdmin` wrapper. Permission checks keep upstream's order.
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
- Use context-aware APIs (`tls.Dialer.DialContext`, `QueryRowContext`).
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
- Code that mimics JS semantics (`Truthy`, `Text`, number formatting) must be
  proven against real Node output, not guesses. Write a table test whose expected
  values come from `node -e`. Always include the edge cases: `0`, `-0`, `""`,
  `"0"`, `[]`, `{}`, `1e20`, `1e21`, `0.000001`, `0.0000001`, nested arrays with
  null.
- `fmt` verbs are NOT JS formatting. Use `strconv.FormatFloat` and apply the JS
  exponent rules.
- Number handling goes through one function (`jsonvalue.Number`) so `json.Number`,
  ints and float64 agree everywhere (`Truthy`, `Text`, `Float`).
- Comment every deliberate deviation or quirk with the upstream source location
  (`// like nodemailer: other ports use STARTTLS`).

## 8. Code organisation
- One file, one concern. Do not mix HTTP handlers, persistence, transport (SMTP),
  encoding (MIME) and config in one file. Transport and encoding code takes plain
  arguments, so it is testable without a DB or HTTP.
- Handlers stay thin: decode, validate, call, encode. Long handlers (60+ lines)
  extract a loader (`loadEbook`) that returns data or an HTTP error.
- Raw SQL lives in the store/domain package, not in HTTP handlers.
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
  (`-race` for anything with locks, caches or goroutines), and `staticcheck` or
  `golangci-lint` if configured.
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
