# Mobile compatibility gaps and test plan

2026-11-04; planning evidence, **no runtime fixes or device acceptance**.
Source-backed client requirements live in [mobile-client-audit.md](mobile-client-audit.md).
The following distinguish coverage gaps/code facts from reproduced bugs.
P0 = auth/playback/progress/persistence/security risk; P1 = other client-visible
behavior/cache/live updates; P2 = optional cleanup. Priority is impact, not a
claim that a suspected failure has occurred. Dispatch via ROADMAP MC-C/MC-D.

## Executable gap register

### MC-01 · Real Socket.IO interop can skip (P0, S)

- Clients: all Socket.IO users. Current path:
  `internal/realtime/hub_test.go:TestSocketIO4WebsocketAuthAndPrefixedPath`.
  It uses genuine JS Socket.IO v4 but `t.Skip`s when Node cannot require it.
  Required verify CI does not provision it; advisory parity does so later.
- Reproduce: from `go-backend`, `go test -v ./internal/realtime -run
  '^TestSocketIO4WebsocketAuthAndPrefixedPath$' -count=1` without that dependency.
  Expected assurance: test executes. Current source allows SKIP; not run here.
- Fix: provision pinned `socket.io-client@4.8.1` in the required test job and
  require its availability there, preserving optional local behavior if useful.
  Do not build another test framework or runtime transport.
- Regression/acceptance: same existing test must execute and assert both
  paths, auth/init, ping, fanout/ACL/ack and timestamp DB readback. CI logs
  must show PASS rather than SKIP. Dependency: Node/client tooling, MC-A.
  Risk: false-green CI. Owner: queue approval; this JS probe version is already
  used in advisory CI; approve provisioning it in verify. The historical
  Go v3 client approval is separate. No runtime dependency proposed. Evidence: test +
  `.github/workflows/docker-go.yml` verify/parity jobs.

### MC-02 · Explicit WebSocket resource bound needs evidence (P0, M)

- Clients: every connection. `internal/realtime/eio.go:serveConn/readLoop`
  advertises Engine.IO `maxPayload=1000000` and does not set
  `ws.MaxPayloadBytes`. **Not unbounded:** pinned x/net/websocket v0.58.0
  defaults to a 32 MiB frame cap (`websocket.go:DefaultMaxPayloadBytes`,
  `Conn.MaxPayloadBytes`, `Codec.Receive`). Engine.IO's advertised field is
  not alone proof of a required WebSocket cap.
- Reproduce: connect/auth normally, send near/over library frame bound and
  fragmented input while observing dispatch, close and resource cleanup.
  Expected: bounded memory and prompt cleanup, valid client events accepted.
  Current: handler limit/fragmented-message behavior not exercised here.
- Action: inspect library behavior, measure real event sizes, use its native
  size control where sufficient; no bespoke protocol layer. Proposed tests
  `internal/realtime/sio_test.go:TestInboundPayloadLimit` and
  `TestFragmentedPayloadLimit` assert no dispatch over chosen bound, normal
  events accepted and goroutines close. Run `go test -v ./internal/realtime
  -run 'PayloadLimit'`, then race/lifecycle checks per MC-C.
- Dependency: MC-A event payload evidence; architect selects limit after
  measurements. Acceptance: frame and total-message cases execute and bound
  is documented. Risk: resource exhaustion vs rejecting valid messages.
  No runtime vulnerability reproduced. No owner cap decision fabricated.

### MC-03 · Auth failure and recovery semantics (P0, M)

- Current: `internal/realtime/hub.go:handleAuth` emits `auth_failed` and leaves
  failure-state behavior to that path. Existing JS test only exercises valid
  auth; listening for a failure is not asserting its contract. Check client
  `invalid_token` listeners against current upstream meaning before a fix.
- Minimal reproduction: JS client connects, sends invalid/expired token,
  records event/payload, then valid `auth`; disconnect/reconnect and reauth;
  repeat as an already authenticated user with a bad token. Expected event
  semantics must come from pinned client/upstream evidence. Current runtime
  outcomes not reproduced; an event-name mismatch alone is not a confirmed bug.
- Proposed `internal/realtime/hub_test.go:TestSocketReauthentication` and
  `TestSocketReconnectDelivery`: assert failure payload, no unauthorized
  protected dispatch, correct user association after failures/switches, one
  init on intended auth cycle and correct recipient delivery after reconnect.
  Run `go test -v ./internal/realtime -run 'SocketReauthentication|SocketReconnectDelivery'`
  with actual JS client, plus MC-C gates.
- Dependency: MC-A failure-event evidence, MC-01 required interop provisioning.
  Done when explicit failure/recovery matrix passes; preserve working clients
  rather than blindly rename events. Medium change risk to auth state; owner
  approves any deliberate protocol change, queue covers regression work.

### MC-04 · Range value/edge assertions (P0, S)

- Existing: `internal/catalog/handler_test.go:TestMediaFileUsesRangeAndRejectsPathsOutsideLibrary`
  checks prefix 206/bytes/Content-Range/ETag and unsatisfiable GET/HEAD 416;
  playback tests check direct seeking; podcast journey includes suffix request.
  No client range failure reproduced. Do not call seeking unimplemented.
- Reproduce with known N-byte fixture: GET `Range: bytes=2-4`, `bytes=-3`,
  `bytes=N-`, malformed and reversed ranges. Assert bytes 2..4 / final 3,
  status 206 and exact Content-Range, 416 with `bytes */N` when unsatisfiable.
  Define malformed/multiple-range expectations from HTTP/client requirements;
  stdlib `http.ServeContent` already owns parsing.
- Extend existing catalog test or proposed contract
  `internal/contracttest/mobile_protocol_test.go:TestMobileMediaRanges`.
  Run `go test -v ./internal/catalog -run TestMediaFileUsesRangeAndRejectsPathsOutsideLibrary`
  and `go test -v ./internal/contracttest -run TestMobileMediaRanges` after addition.
- Dependency: MC-A media callers. Done when value and boundary cases execute
  without asserting incidental multipart byte formatting. Risk low, no new
  parser/dependency, queue approval sufficient.

### MC-05 · Cache and artwork format requirements (P1, M)

- Current: JSON writer `internal/httputil/json.go:WriteJSON` has no request
  argument/conditional-cache logic; complete middleware/route behavior still
  needs a targeted reproduction. Do not infer broken caching from one helper.
  Existing `catalog/handler_test.go:TestCoverRawServesOriginalAndHonorsTimestampCacheHeader`
  protects original bytes/timestamp behavior; author journey exercises formats.
- Reproduction: on each source-confirmed Lissen Cacheable GET, capture 200
  body + ETag, repeat with If-None-Match; expected 304 + empty body/cache reuse.
  For actual client cover parameters, decode returned image and check MIME,
  dimensions, refresh after source change, original artwork retention.
  Current: no live request executed in this audit; format matrix incomplete.
- Proposed `internal/contracttest/mobile_protocol_test.go:TestMobileConditionalGET`
  and `TestMobileCoverParameters`; use existing TS-created seeded install and
  ordinary HTTP client, no second framework. Run `go test -v
  ./internal/contracttest -run 'TestMobileConditionalGET|TestMobileCoverParameters'`.
- Dependencies: MC-A exact cacheable endpoints/parameters; S9 waits for these.
  Done when required validator semantics and image decoding pass (ETag string
  style is free). Risk invalidating cache or format support. No WebP-only cache
  or stored artwork change approved; any such design needs separate review.

### MC-06 · Bulk offline persistence coverage (P0, M)

- Current: both `POST /api/session/local` and `POST /api/session/local-all`
  route through `internal/playback/playback.go:handleLocalSync` with different
  flags. Existing `contracttest/persistence_test.go:TestGoWritesUseTSStorageFormats`
  covers local + online writes, not the full bulk offline state matrix.
  This is a coverage gap, not proof of data loss.
- Reproduce: upload source-shaped `{deviceInfo,sessions:[...]}` with two
  different session IDs and book/episode records, then repeat unchanged and
  advance one record. Expected: correct IDs/user ownership, units, progress,
  listening times and storage formats, no unintended duplicate rows on replay;
  derive merge/replay semantics from pinned source before asserting policy.
- Proposed `internal/contracttest/mobile_protocol_test.go:TestMobileOfflinePersistence`:
  exercise both endpoints, verify rows/values after each operation and DB
  reopen, reject cross-user writes, test a failing input leaves contractually
  correct state. Run `go test -v ./internal/contracttest -run
  'TestMobileOfflinePersistence|TestGoWritesUseTSStorageFormats'`, then rollback.
- Dependency: MC-A iOS/Lissen payload evidence. Done when normal/repeat/changed/
  failure state meets independent expectations and TS can read Go writes.
  Persistence risk; fixed schema/metadata stay unchanged, queue approval.

### MC-07 · Route deletion evidence is incomplete (P0, M)

- Current: generated `contracts/client-usage.json` calls 25 upstream routes
  unused, including GET progress/playlists/collections and POST local-all. These are absence in
  the generator's web/log/trial corpus, not proven absence across clients.
  External source is not an input to `tools/clientusage/main.go` today.
- Reproduce: inspect `unusedUpstreamRoutes` alongside pinned external calls
  in [audit](mobile-client-audit.md). Expected safe inventory: demonstrated
  consumers protected, unknown/dynamic sites remain open. Current generated
  categories are insufficient for removal; no route was deleted here.
- Fix/test in MC-B: merge a small checked provenance manifest, retain current
  generator; proposed `tools/clientusage/main_test.go:TestExternalClientEvidence`
  plus existing `TestOutputIsCurrent`; include source-confirmed local-all. Run `go test ./tools/clientusage` and
  root `make client-usage`; prove repeated generation identical.
- Done when decisions are traceable and no removal is inferred from web-only
  absence. Dependency MC-A; risk removing real client functionality. Owner
  approves each actual removal via R2, none approved in this audit.

## Test matrix and completion commands

All existing tests below were **inspected, not run** in this audit. Proposed
names do not execute until implemented. Commands below run from `go-backend`;
proposed files use the existing `contracttest.Boot` seeded fixture.

| Scenario | Existing / proposed file and test | Required assertion / fixture | Command / verification |
|---|---|---|---|
| Both socket paths + v4 JS | Existing `realtime/hub_test.go:TestSocketIO4WebsocketAuthAndPrefixedPath`; server `TestSocketConnectAuthLoop` | Real JS dependency; legacy + configured prefix; auth/init and recipient/ack values; loop is native protocol evidence only | `go test -v ./internal/realtime ./internal/server -run 'Socket'`; no interop SKIP |
| Invalid/expired, reauth, reconnect | Proposed realtime tests MC-03 | Isolated users + expired/current tokens; event names/payload, auth state, fresh init/recipient delivery | `go test -v ./internal/realtime -run 'SocketReauthentication|SocketReconnectDelivery'` |
| Malformed/binary/namespace | Existing `sio_test.go:TestParsePacket`, `TestEventBeforeConnectClosesConnection`, `TestBinaryPacketClosesConnection`, `TestInvalidNamespace` | Invalid frames close/reject without protected dispatch; binary unsupported is recorded, not generic compatibility | `go test -v ./internal/realtime -run 'Packet|Namespace'` |
| Heartbeat/queue/cleanup/oversize | Existing `TestPingTimeout`, `TestAnsweredPingsKeepConnectionOpen`, `TestFullBufferClosesConnectionWithoutBlocking`, `TestHubCloseClosesConnections`; proposed MC-02 limits | Controlled ping/queue fixture, bounded completion/no late dispatch | `go test -v ./internal/realtime -run 'Ping|Buffer|HubClose|PayloadLimit'`; targeted race |
| Cache validators | Proposed `mobile_protocol_test.go:TestMobileConditionalGET` | Exact Lissen Cacheable endpoint; ETag then If-None-Match ->304, zero body, same cached values | `go test -v ./internal/contracttest -run TestMobileConditionalGET` |
| Byte/suffix/invalid ranges | Existing catalog media test; proposed `TestMobileMediaRanges` | Deterministic N bytes; 206 bytes/Content-Range; 416 `*/N` | MC-04 commands |
| Direct/HLS | Existing `playback_test.go:TestDirectPlaybackRangeOwnershipAndProgressPersistence`, `TestTranscodedSessionServesHLSWithoutBearerAndRejectsBadFiles`; proposed `TestMobilePlayback` in contracttest | Fixture audio + ffmpeg; actual media URLs/MIME, segment/session ownership, seek and progress meaning | `go test -v ./internal/playback -run 'DirectPlayback|TranscodedSession'`; then `go test -v ./internal/contracttest -run TestMobilePlayback` |
| Local + bulk sync/persistence | Existing `TestGoWritesUseTSStorageFormats`; proposed `TestMobileOfflinePersistence` | Source-shaped sessions/units/IDs, repeat and changed inputs, ownership, rows after reopen, TS rollback | MC-06 command + CI rollback |
| Covers with actual parameters | Existing raw-cover test + author journey; proposed `TestMobileCoverParameters` | Original fixture art; actual client width/format/query combinations decode, MIME/dimensions/cache refresh | MC-05 command |
| Client JSON meaning | Existing shape tests allow extras and inferred Any; proposed `TestMobileJSONValues` in contracttest | Source-derived decoder expectations; exact IDs/URLs/progress/order/ACL/null-vs-empty values, not fields alone | `go test -v ./internal/contracttest -run TestMobileJSONValues` |
| Real devices | Owner O4 / MC-E, no Go test substitute | Official iOS/Android + declared third-party versions, existing auth, reconnect/seek/offline sync/second-client progress | Record versioned device procedure/result; pending |

After implementation run applicable AGENTS checks; do not mark a regex-only
command passing without showing named tests ran. No device, runtime bug or
full mobile compatibility claim follows from this document.
