# Replacement scenarios and harness

Black-box scenarios that run unchanged against pinned upstream (v2.37.2) and
later against the Go rewrite. One Go package per owner packet
(`packets.json`: packet name to requirement ids). Process and gate:
[../../VERIFICATION.md](../../VERIFICATION.md). Brief for owners: [OWNERS.md](OWNERS.md);
for reviewers: [REVIEWERS.md](REVIEWERS.md).

## Packet package

```
tests/replacement/<packet>/
  packet.go     package <packet>; func Packet() harness.Packet      (no build tag)
  run_test.go   //go:build replacement
                func TestReplacement(t *testing.T) { harness.Run(t, Packet()) }
```

`Packet.Name` must equal the directory name and be a key of `packets.json`.
Imports: `audiobookshelf-compat/tests/replacement/harness`. Import nothing from `internal/`.

## Run one packet

```sh
make verify-packet PACKET=auth [RUN=assertion-regex]         # upstream, go test -v
cd go-backend && go run ./tools/verify -packets auth          # upstream summary and failure output
cd go-backend && go test ./tests/replacement/harness          # harness self-tests, no server
REPLACEMENT_PLAN_ONLY=1 go test -tags=replacement ./tests/replacement/auth -v   # only the declarations
```

Needs Node 22 (`make` finds it; else `REPLACEMENT_NODE`) and ffmpeg/ffprobe on `PATH`
or in `FFMPEG_PATH`/`FFPROBE_PATH`. Environment read by the harness:
`REPLACEMENT_TARGET` (`upstream`|`go`, required), `REPLACEMENT_NODE`, `REPLACEMENT_GO_BIN`,
`REPLACEMENT_ROOT`, `REPLACEMENT_WORK` (keep work dirs there), `REPLACEMENT_V8` (coverage dir), `REPLACEMENT_PLAN_ONLY`.

## Scenario and Packet

```go
type Scenario struct {
    Requirement, Invariant string // capability id and invariant id inside it
    Case  string   // normal | error | edge
    State string   // stateless | initial | repeat | changed | force | failure
    Assertion string   // unique in the packet, [a-z0-9-]+; the subtest name
    Sources []string   // >=1 upstream refs "server/...js:LINE"
    Claim   string     // one sentence: the observable behaviour asserted
    Run func(t *testing.T, e *harness.Env)
}
type Packet struct {
    Name string; Options Options; NoServer bool; Scenarios []Scenario
}
```

`Run` validates the packet, logs `REPLACEMENT_DECLARE {json}` per scenario, then (unless
`REPLACEMENT_PLAN_ONLY=1`) starts one shared server with `Options` (none when `NoServer`:
`e` is nil and each scenario calls `harness.Start`) and runs the scenarios in order as
subtests `TestReplacement/<assertion>`. A scenario that returns without failing logs
`REPLACEMENT_PROOF`; a failed or skipped one never does (a skip is turned into a failure).
Scenarios of a packet share server state on purpose (initial, repeat, changed flows).

## Server: Options, Env

```go
type Options struct {
    Env map[string]string; Args []string
    Fresh bool     // empty config/metadata (uninitialised server); media still copied
    Install string // reuse an existing install directory (reopen checks)
    Upstream bool  // always launch pinned upstream, whatever the target
    NoWait bool    // do not wait for /ping (launch-failure scenarios)
}
func Start(t, Options) *Env      // fails the test on error; cleanup registered
func (e *Env) Stop(t)            // SIGINT to the process group, 10s, then SIGKILL; idempotent; closes sockets
func (e *Env) Restart(t)         // same install and options, new port; URL/Base updated; token cache cleared
func (e *Env) WaitReady(time.Duration) error
func (e *Env) WaitExit(time.Duration) (code int, exited bool)
func (e *Env) Log(t) string      // server stdout+stderr (appended across restarts)
```

`Env` fields: `URL`, `Base` (URL + `/audiobookshelf`), `Install`, `Config`, `Metadata`, `Media`,
`IDs` (fixture `ids.json`: library, saga1, novel, listener, ...; empty when Fresh), `LogPath`.
Both targets get the same configuration by environment only (`CONFIG_PATH`, `METADATA_PATH`,
`HOST`, `PORT`, `SOURCE=docker`, `ROUTER_BASE_PATH`, isolated HOME/TMPDIR, `TZ=UTC`, ...); the parent
environment is not inherited. `Options.Env` overrides. Fixture users: `root`, `listener`, `finisher`.

## HTTP

```go
e.Do(t, Request{Method, Path, Raw, Token, Header, Query, JSON, Body}) Response
e.Get(t, token, path) / e.Post(t, token, path, body) / e.Patch(...) / e.Delete(t, token, path)
Response{Status, Header, Body, Cookies}; r.JSON(t, &v); r.Object(t); r.Array(t)
e.Login(t, user, password) (accessToken, user map[string]any)
e.Token(t, "root"|"listener"|"finisher") string       // cached login of the fixture user
Multipart(fields, File{Field, Name, Content}...) (*bytes.Buffer, contentType)
Dig(decoded, "a", 0, "b") any      // nil when absent
Eventually(t, timeout, what, func() bool)
```

Paths are relative to `Env.Base` (`Raw: true`: relative to `Env.URL`). `JSON` marshals the value;
`json.RawMessage`, `[]byte` and `string` are sent verbatim. No redirects are followed, no cookie jar,
60s timeout, 64 MiB response limit.

## Socket.IO

```go
s := e.Socket(t, token)              // token != "": emits "auth", waits for "init"
s.Emit(t, "name", args...)
mark := s.Mark()                     // index after the last event so far
ev := s.Wait(t, mark, "name", 5*time.Second, match)   // match: func([]json.RawMessage) bool, nil = any
s.None(t, mark, "name", 500*time.Millisecond, match)  // no such event now or within the window
s.Events() []Event{Name, Args, At}; s.Close()
```

## Database (read-only)

```go
e.Rows(t, "SELECT ... WHERE id = ?", id) []map[string]any   // []byte columns as string
e.Count(t, "SELECT COUNT(*) FROM ...") int
```

Opens `Config/absdatabase.sqlite` per call with `mode=ro` and a 5s busy timeout.

```go
e.Stop(t); n := e.Exec(t, "UPDATE sessions SET expiresAt = ? WHERE id = ?", past, id); e.Restart(t)
```

`Exec` writes to the database directly and only while the server is stopped. Use it only to
prepare persisted state that no API can produce (an expired session, a legacy row, a linked
OpenID subject). Never use it in place of the request under test, and never to make an
assertion pass. Do not depend on the wall clock against fixture dates: set the dates you need.
