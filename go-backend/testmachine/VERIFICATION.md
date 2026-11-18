# Unified replacement verification

`make verify` (or `make check`) from the repository root is the one verdict. Go
coordinates; the shared black-box scenarios are Go tests under
`testmachine/scenarios/`; native `.mjs` provides source inventory, mapping and Node
self-tests. Python is not in this path. Use the configured golangci-lint release, Node 22 and the FFmpeg/WebP
environment in [TESTING.md](../TESTING.md). `VERIFY_ARGS="-out /abs/report.json"`
selects the report path and `VERIFY_NODE=/abs/node` the Node 22 executable.
Logs, disposable installs and raw V8 files stay under `abs-verify-*` in the OS
temporary directory. No live installation or golden is ever touched.

Use `make verify VERIFY_ARGS="-target upstream"` to certify the test machine
against pinned v2.37.2. This mode stops after the upstream verdict and returns
zero only when all upstream assertions, coverage and reviews pass. The default
command additionally gates the Go comparison and rollback trial on that verdict.

## Structure

- `testmachine/scenarios/packets.json`: packet name to requirement ids (34 owner packets).
- `testmachine/scenarios/harness/`: the toolkit (no build tag, no `internal/` imports).
  It starts the server under test, speaks HTTP and Socket.IO, reads the database,
  and logs `REPLACEMENT_DECLARE` and `REPLACEMENT_PROOF` lines. API: [testmachine/scenarios/README.md](scenarios/README.md).
- `testmachine/scenarios/<packet>/`: one Go package per packet. `packet.go` returns the
  packet's scenarios; `run_test.go` (build tag `replacement`) calls `harness.Run`.
  Examples: `server`, `auth`. A compile error stays inside its own packet.
- `testmachine/cmd/verify`: the coordinator. It starts no servers; the harness does.

## What `make verify` does, in order

1. **Plan:** `go test -tags=replacement ./testmachine/scenarios/...` with
   `REPLACEMENT_PLAN_ONLY=1` (no servers) collects every declared scenario. The plan
   is validated against the capability records (`testmachine/register/capabilities`) and
   `packets.json` (known packet, requirement owned by that packet, known invariant,
   applicable case and state, unique assertion per packet).
2. **Coverage gaps** (`coverageGaps`, `reviewGaps`):
   every in-scope invariant has a declared scenario; every case (`normal`, `error`,
   `edge`) and, for stateful requirements, state (`initial`, `repeat`, `changed`,
   `force`, `failure`) has a scenario or is `not_applicable` in the record **and**
   the record's `exceptions` has a string starting `"<facet> is not applicable:"`.
   Status flags inside the records are outputs, never inputs.
   Each packet needs `testmachine/contracts/verification-review/<packet>.json` by an independent
   reviewer: `scenario_digest` and `requirements_digest` current (print them with
   `go run ./testmachine/cmd/verify -digest <packet>`), no `open_findings`, a `required_tuples`
   entry (sources, rationale, exact scenario) for every declared scenario, and
   every `not_applicable` facet recorded with a reason. A passing run never writes it.
   Whole-contract discovery needs a separate independent audit of the pinned source, client protocols,
   formats and lifecycle across packet boundaries. Record it in
   `testmachine/contracts/verification-inventory-review.json` with `reviewer`, `source_pin`,
   the full report `inputs`, a source-backed `reconciliation` summary and an explicit empty
   `open_findings` array after every finding closes. The final audit is tied to that measured
   installation, including its Node binary and installed dependencies.
3. **Machine health:** mapping check, `gofmt`, Go build/tests/vet (also
   `vet -tags=replacement`), configured tagged lint, targeted verifier/harness race tests, Node self-tests, the upstream unit suite and Linux amd64/arm64 builds.
4. **Upstream run:** the same `go test` command with `REPLACEMENT_TARGET=upstream`
   against pinned v2.37.2 (`index.js` directly), `-p <parallel>` packages at once.
   Every declared scenario is graded: it counts only if subtest
   `TestReplacement/<assertion>` of package `testmachine/scenarios/<packet>` ended `pass`
   and emitted exactly one `REPLACEMENT_PROOF` equal to the declaration. Failures
   (also build failures, panics, timeouts) keep the scenario's last 60 output lines
   and never abort the run. The report lists them per packet.
5. **Verified upstream** only if health passed, there are zero gaps and every
   declared scenario passed. Only then the same scenarios run with
   `REPLACEMENT_TARGET=go` (CGO=0 binary), followed by the rollback check (upstream
   reopens what Go wrote). Input digests must be unchanged at the end.

The default command exits nonzero unless the whole replacement is verified.
The upstream-only command exits nonzero unless upstream coverage is verified.
Both write `report.json` (`upstream.packets`, `go.packets`: declared, passed,
failed and failures).

## Developing one packet

```sh
make verify-packet PACKET=auth [RUN=assertion-regex]     # against pinned upstream, verbose
cd go-backend && go run ./testmachine/cmd/verify -packets auth,server   # summary with failure output
go run ./testmachine/cmd/verify -gaps          # coverage and review gaps of the declared plan (no servers)
go run ./testmachine/cmd/verify -digest auth   # digests for the review receipt
```

`-packets`, `-gaps` and `-digest` give no verdict, run no health checks and apply no gate.
Scenarios of one packet run in declaration order on one shared server; packets run
in parallel on separate servers.

## Scope and limits

Two approved exclusions stay: older-schema migration and ebook-engine internals.
The canonical requirement and invariant register is the feature source of truth; scenarios
are not a second inventory. Raw V8 data is kept per upstream launch; whole-source
line/branch percentages are unavailable (`null`, never 0 or 100). Only the complete
machine result supports “All requirements of the defined v2.37.2 replacement contract
are verified.” This is separate from live deployment authorization.
