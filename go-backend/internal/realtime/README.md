# Realtime adapter

`Hub` implements the Socket.IO v4 / Engine.IO transport through
`github.com/zishang520/socket.io/v2` and reproduces the pinned
`server/SocketAuthority.js` connection boundary. It serves `/socket.io` and,
when `config.Config.RouterBasePath` is set, the additional prefixed path.
Clients authenticate after connection by sending the existing access token in
the `auth` event. `New` delegates token validation and active-user lookup to
the supplied HTTP authentication callback.

`Hub.Emit` uses an exact recipient selector: a user ID, `@admins` (admin/root),
or `*` (all authenticated clients). Broadcast item payloads are filtered by
library, tag, and explicit-content permissions when the payload contains those
fields. Arrays of item-shaped payloads are reduced per recipient. Events with
no identifying metadata remain global.

`SetDispatcher` lets scanning, cover search, and log management own their
domain work without giving the transport package permission to invent those
services. The dispatcher receives authenticated action events, an optional
Socket.IO acknowledgement callback, and a streaming `Reply` callback. The
transport enforces admin checks for `cancel_scan`, `set_log_listener`, and
`message_all_users`; it checks authentication for cover search and cancellation.
Unwired dispatch actions return no result (cover search reports an explicit
unavailable error). A full replacement must install the real domain dispatcher.

On successful auth the adapter updates `users.lastSeen` and `users.updatedAt`
using Sequelize-compatible SQLite date text before emitting `init`. The
integration test uses `identity.Handler.Authenticate`, then reopens the file
through the read-only store to verify those timestamps remain readable.
Playback-session details in the public online-user payload are currently
`null`; playback-session serialization and socket-scoped logger/search cleanup
need service integrations before parity is complete.

The Node integration test skips when `socket.io-client` is absent. To exercise
polling, WebSocket upgrade, auth, acknowledgements, recipient permissions, and
the prefixed mount, install Socket.IO client 4.x and set `NODE_PATH`:

```sh
npm install --prefix /tmp/realtime-node-probe --no-audit --no-fund socket.io-client@4.8.1
NODE_PATH=/tmp/realtime-node-probe/node_modules CGO_ENABLED=0 go test -v ./internal/realtime
```
