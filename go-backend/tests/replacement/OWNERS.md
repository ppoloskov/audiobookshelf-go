# Owner packet brief: upstream scenarios

One worker owns one packet (a key of [packets.json](packets.json)) per
invocation. The packet's package is `tests/replacement/<packet>/`. The harness
API is in [README.md](README.md). The architect reviews every packet; an
independent reviewer audits it against the upstream source
([REVIEWERS.md](REVIEWERS.md)).

## Mission

For every requirement of your packet, write black-box scenarios that prove what
pinned upstream v2.37.2 does, and make all of them pass against the real
upstream server. The same scenarios later judge the Go rewrite, so they must
contain only contract behaviour and must not know which server they talk to.

You never run, read or cite the Go server (`go-backend/internal`,
`go-backend/cmd`). Expected values come from the upstream source (`server/`,
`index.js`), from what clients use (`client/`, `go-backend/contracts/clients/*.md`,
`go-backend/contracts/client-usage.json`, `go-backend/contracts/mobile-client-audit.md`)
and from the live upstream server. When the live server and your reading of
the source disagree, the live server wins: fix the expectation and cite the
source line that explains it.

## What is contract (assert it) and what is not (do not assert it)

Read "The two contracts" in `go-backend/ARCHITECTURE.md` and
`docs/agent-work/CONTRACT.md`. In short:

Assert:

- the route exists and who may call it (unauthenticated, wrong user, missing
  permission, admin-only);
- statuses the handler sets deliberately (`res.sendStatus(404)`,
  `res.status(400)`, redirects) and every status a client branches on;
- response fields and their JSON types, nullability and values that a client
  reads. Use exact values when the fixture makes them deterministic; otherwise
  type, format and relations (`updatedAt` not before the request started);
- persisted data: rows, column values and value formats in
  `config/absdatabase.sqlite` (`e.Rows`), and files under `metadata/` and the
  library folders. Persisted data is always contract, also when no client
  reads it back;
- Socket.IO events: name, audience (who receives it and who must not) and the
  payload fields clients read;
- conditional requests, byte ranges, MIME types and auth flows;
- that a rejected request changes nothing (rows, files, events).

Do not assert:

- JSON key order, whitespace, header order, compression, the ETag algorithm;
- error text and error-page HTML (assert the status; a body only where a
  client parses it);
- accidents of JavaScript: when upstream fails with an unhandled exception
  (500) on input no real client sends, assert only `status >= 400` and that
  nothing changed. Do not pin truthiness or `Number()` coercion for inputs no
  client sends;
- log text;
- ebook engine internals and migration of older database schemas (the two
  agreed exclusions). Ebook file grouping and serving are in scope.

`go-backend/docs/CHANGES.md` lists upstream behaviours the owner already ruled
non-contract. You may read it for that purpose only, never to derive an
expected value. List in your report every behaviour you saw and deliberately
did not assert, one line each with the reason.

## Requirement records

Your requirement records are `docs/agent-work/capabilities/cap-<id>.json`.
Many have a placeholder invariant (`INV-…-SAMPLED`) whose text only names the
route. For each requirement:

1. Read the whole upstream path: router line → controller → managers, models,
   utils it calls. Find the callers in `client/` and the client audits.
2. Rewrite the invariants so that each is one concrete, checkable statement
   derived from the source. Keep existing invariant ids; add invariants
   (`INV-<REQ>-<TOPIC>-001`) when a requirement has several distinct
   obligations (response shape, authorisation, persistence, event). Fill
   `source` with upstream references (`server/...js:LINE`). Leave `status`
   fields as they are.
3. Facets. Every requirement has cases `normal`, `error`, `edge`; a `stateful`
   one also has states `initial`, `repeat`, `changed`, `force`, `failure`:
   - normal: intended use by an authorised caller;
   - error: each deliberate rejection (unauthenticated, forbidden, not found,
     invalid input, conflict), without side effects;
   - edge: boundaries and valid variants (empty results, optional query
     parameters, limits, null against absent, Unicode);
   - initial: first use on the fixture state;
   - repeat: the same request again (idempotency, duplicates);
   - changed: behaviour after related state changed (reads reflect a write,
     another user's view, a revoked permission);
   - force: an explicit force/hard/override mode;
   - failure: a failed or rejected operation leaves persisted state, files and
     events unchanged, or in the partial state upstream defines.
   A facet that cannot exist for the requirement is set to `"not_applicable"`
   in `cases` / `state_scenarios`, with a line in `exceptions`:
   `"<facet> is not applicable: <reason>"`. The reviewer checks every such
   line. Do not write filler scenarios to tick a facet, and do not mark a
   facet not applicable to avoid work.
   Scenarios of a stateless requirement use state `"stateless"`.
4. Add each scenario to the invariant's `tests` list as
   `go-backend/tests/replacement/<packet>/run_test.go#TestReplacement/<assertion>`
   (the mapping check needs exactly this form). Remove a reference to a file
   that no longer exists; keep the other existing references.

Edit only your packet's records. The gate needs: every invariant has a
scenario, and every applicable facet of every requirement has a scenario.

## Scenarios

A scenario is one `harness.Scenario`: one requirement, invariant, case, state,
one sentence of `Claim`, upstream `Sources`, and a `Run` that asserts the claim
completely. Depth rules:

- A success scenario asserts status, decoded values, and every effect the
  claim names: the row (`e.Rows`), a follow-up read through the API, the
  event on a socket of each audience, the file on disk.
- An error scenario asserts the status and that nothing changed.
- Assert values, not just key presence. Decode into structs with the fields
  the client reads, or compare decoded values.
- An event scenario connects the sockets first, takes `Mark()`, acts, then
  `Wait`s; it uses `None` for an audience that must not receive the event.
- No `time.Sleep` to wait for an effect: use `harness.Eventually` or
  `Socket.Wait`. No `t.Skip`. No branching on the target.
- No real internet. Stub remote services with `httptest` servers on
  `127.0.0.1` (RSS feeds, SMTP, OIDC provider, Apprise, custom metadata
  providers, cover URLs). If upstream can only reach a fixed external host,
  assert the offline-deterministic behaviour and report the rest as an open
  question.
- Scenarios of a packet run in declaration order on one server and share its
  state. Order them deliberately (reads first, destructive last). Use
  `harness.Start` inside a scenario when it needs its own server, a restart,
  a fresh uninitialised install, special environment, or wants upstream to
  reopen what the target wrote (`Options.Upstream`).
- Keep the code plain: table-driven where cases repeat, small helpers in the
  packet, one file per requirement group (`packet.go` assembles them), no file
  over about 500 lines. Follow the Go rules in `agents.md` (errors, naming,
  gofmt).

## Findings reviewers raise most (avoid them)

- A field asserted only as empty or false: prove a non-empty value too
  (create the state), otherwise a server that always returns the default
  passes.
- "Not before" timestamp checks on a write: a changing write must move
  `updatedAt` strictly forward from the value read before; a no-op must leave
  it unchanged.
- Dependence on the wall clock against fixture dates (seeded sessions expire
  2026-12-03): set the dates you need with `e.Exec` (README "Database").
- Pagination proven on a single page: create enough rows for two pages.
- Only one of the access rules exercised: library access, item tags (allow
  and deny mode) and explicit content are separate rules.
- Check order: where the handler rejects in a defined order (404 before 403,
  400 before 403), pin it with a request that violates both.
- An upstream authorisation gap (a missing permission check) is contract only
  when a client offers that action to such a user; cite the client file, or
  do not assert it.
- Session, device and metadata sub-objects compared by one or two keys: compare
  every deterministic field a client reads.

- The fixture libraries have the file watcher on (a rescan about 10 s after
  a file change). A scenario that changes library files must not depend on
  the watcher running or not running in time: trigger the scan through the
  API and wait for its result, or disable the watcher for that library.
- Expectations derived only from the database at request time: also pin
  literal fixture facts (ids, paths, durations, titles) for the main items.

## Completeness check with upstream execution data

Run your packet with coverage and look at the handlers you own:

```sh
export REPLACEMENT_V8=$(mktemp -d)
make verify-packet PACKET=<packet>
node go-backend/tools/verify-coverage.mjs --v8 "$REPLACEMENT_V8" \
  --md "$REPLACEMENT_V8/coverage.md" --filter server/controllers/<Your>Controller.js
```

Every function and branch of the handlers behind your routes should be
executed by a scenario that asserts its outcome. For each range that stays
unexecuted, add a scenario or list it in the report with the reason
(unreachable, exclusion, needs the real internet). Execution is not proof: a
branch counts only when a scenario asserts what it does.

## Acceptance (no review loop)

Since 2026-11-15 most packets get no independent review, so your own checks
are the acceptance. A packet is done only when all three hold, and you report
each with its command and result:

1. every scenario passes against upstream in two consecutive full runs;
2. `-gaps -packets <packet>` prints no gap line;
3. the coverage report for your handlers (previous section) has no unexecuted
   function or branch behind your routes, except ranges you list with a
   reason (unreachable through the API, exclusion, external host).

Before reporting, go through "Findings reviewers raise most" line by line
against your scenarios and fix what applies. High-risk packets get one review
pass afterwards; that reviewer edits the packet directly.

A success path that only runs against a fixed third-party internet host is
marked not applicable with the reason `external host: <host>`; everything
deterministic offline (authorisation, validation, not found, the unreachable
provider answer, no side effects) still needs scenarios.

## Running

From the repository root, with the FFmpeg prefix of `go-backend/TESTING.md`:

```sh
export FFMPEG_TEST_PREFIX=/private/tmp/abs-cleanup-checkpoint-20261105/ffmpeg-9.0.2-webp
export PATH="$FFMPEG_TEST_PREFIX/bin:$PATH" FFMPEG_PATH="$FFMPEG_TEST_PREFIX/bin/ffmpeg" FFPROBE_PATH="$FFMPEG_TEST_PREFIX/bin/ffprobe"
make verify-packet PACKET=<packet>            # all scenarios
make verify-packet PACKET=<packet> RUN=<regex> # some
```

Before reporting: `cd go-backend && gofmt -l tests/replacement/<packet>`,
`go vet -tags=replacement ./tests/replacement/<packet>`, the full packet run
twice in a row (both green: scenarios must not be flaky), and
`go run ./tools/verify -node "$(mise exec node@22 -- node -p process.execPath)" -gaps -packets <packet>`
must print no gap line (it checks only your packet).

## Write set

Only `go-backend/tests/replacement/<packet>/**` and your packet's
`docs/agent-work/capabilities/cap-<id>.json` files. Not the harness: when you
miss a harness feature, write a local helper and say so in the report. No new
dependencies. No commit, push or branch.

Finish whole requirements. If the packet does not fit, complete as many
requirements as you can with all their facets, and list the unfinished ids.

## Report

Under 500 words: files written; requirements finished and unfinished;
commands run and results (scenario count, both run results); facets marked not
applicable; behaviours deliberately not asserted; unexecuted handler ranges
with reasons; open questions. Never report a check you did not run.
