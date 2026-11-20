# Realtime adapter

`Hub` implements a minimal Socket.IO v4 / Engine.IO v4 server (`eio.go`,
`sio.go`; websocket transport and the default namespace only, built on
`golang.org/x/net/websocket`) and reproduces the pinned
`server/SocketAuthority.js` connection boundary. A connection is registered
and its event handlers attached before the CONNECT acknowledgement is sent,
so an event sent right after the acknowledgement always finds its handler.
Polling requests get Engine.IO's 400 "Transport unknown" answer. It serves `/socket.io` and,
when `config.Config.RouterBasePath` is set, the additional prefixed path.
Clients authenticate after connection by sending the existing access token in
the `auth` event. `New` delegates token validation and active-user lookup to
the supplied HTTP authentication callback.

`Hub.Emit` uses an exact recipient selector: a user ID, `@admins` (admin/root),
or `*` (all authenticated clients). Broadcast item payloads are filtered by
library, tag, and explicit-content permissions when the payload contains those
fields. Arrays of item-shaped payloads are reduced per recipient. Events with
no identifying metadata remain global.

`SetDispatcher` lets scanning and log management own their domain work without
giving the transport package permission to invent those services. The
dispatcher receives authenticated action events, an optional Socket.IO
acknowledgement callback, and a streaming `Reply` callback. The transport
enforces admin checks for `cancel_scan`, `set_log_listener`, and
`message_all_users`; it checks authentication for cover search and cancellation.
The runtime's dispatcher handles `cancel_scan`, `set_log_listener`,
`remove_log_listener`, `disconnect`, `search_covers` and `cancel_cover_search`.
C5 wired cover search through providers; the contract suite has
`TestCoverSearchSocket` and `TestCoverSearchCancel`. Without a dispatcher,
cover search reports `cover_search_error`.

On successful auth the adapter updates `users.lastSeen` and `users.updatedAt`
using Sequelize-compatible SQLite date text before emitting `init`. The
integration test uses `identity.Handler.Authenticate`, then reopens the file
through the read-only store to verify those timestamps remain readable.
The runtime supplies each user's open playback session to the online-user
payload through `SetSessionLookup`. That field is `null` when the user has no
open session.

The Node integration test skips when `socket.io-client` is absent. To exercise
WebSocket connect, auth, acknowledgements, recipient permissions, and
the prefixed mount, install Socket.IO client 4.x and set `NODE_PATH`:

```sh
npm install --prefix /tmp/realtime-node-probe --no-audit --no-fund socket.io-client@4.8.1
NODE_PATH=/tmp/realtime-node-probe/node_modules CGO_ENABLED=0 go test -v ./internal/realtime
```

Compatibility audit status (2026-11-04): the existing real JS integration tests
successful auth, acknowledgements and recipient filtering, but can skip when
its dependency is absent. MC-C plans mandatory execution plus invalid/expired
token, re-authentication, reconnect and resource-limit assertions. Go currently
emits `auth_failed`; mobile `invalid_token` handling needs source comparison
before a change. `eio.go` does not configure `MaxPayloadBytes`: the pinned
x/net/websocket library supplies a 32 MiB default frame cap, so the reader is
not literally unbounded. Test the appropriate client-compatible cap and
fragmented messages; the Engine.IO `maxPayload` field alone is not proof of
a WebSocket-frame requirement. No hardening change has been made by this audit.
