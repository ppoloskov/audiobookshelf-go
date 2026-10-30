# Audiobookshelf Go backend rewrite

Status: experimental Go backend under active parity development. This is **not** a drop-in replacement yet. The latest default-mode differential passes 176/176 selected cases, including the 18-case global notification event slice; separate reports cover selected OIDC flows (now 22/22, including web logout/session invalidation), covers/downloads, SMTP behavior, and email-device proxy-mode scenarios. Strict ledger verification still fails; route-wide and overall compatibility are not established. The pinned Node server remains the behavior reference, and the original frontend is retained. See the root `handover.md` and `docs/agent-work/tasks/TASK-20261030.md` for verified scope and remaining work.

## Target

- Upstream: `advplyr/audiobookshelf`, release `v2.37.2`.
- Baseline commit: `edb1f7efc92ecacdef51aafb4d9d5054ce10604f`.
- Preserve existing web and mobile clients, API behavior, SQLite database and metadata layout.
- Backend runtime implemented in Go, built with `CGO_ENABLED=0`. Go dependencies are allowed. FFmpeg and ffprobe may run as external processes.
- Keep the existing frontend. Its build toolchain is separate from the backend runtime requirement.
- Keep upstream license and attribution in this fork; the upstream license is GPL-3.0.

## Included now

1. `tools/source_inventory.py`: reproducible source evidence for route registrations, Socket.IO candidates and model files, with source hashes and locations.
2. `contracts/source-inventory.json`: the generated baseline. This is a source index, not a complete compatibility contract.
3. `cmd/abs-compat`: a standard-library Go HTTP differential harness. It compares explicitly selected GET/HEAD requests against two isolated servers. It does not test write flows, authentication lifecycles, realtime protocols or database equivalence yet.
4. `ROADMAP.md`: implementation sequence, ownership and release gates.
5. `cmd/audiobookshelf-go`: a Go HTTP server with fresh-database initialization from the source-executed baseline schema, local authentication, token lifecycle, library/item browsing, progress/bookmarks, playback/HLS, and Socket.IO.
6. Domain packages for catalog/user changes, scanning/watchers, media tools, backups, settings and OIDC are being implemented and tested. Individual handlers and sampled scenarios do not establish complete route parity.
7. `tools/parity`: starts the actual upstream and Go executables on independent source-created fixtures and compares HTTP, media bytes, mutations and Socket.IO. Sanitized reports and source provenance are in `contracts/parity`.

## Run the experimental server

Use a consistent, disposable snapshot of an initialized v2.37.2 installation. For a live SQLite database, use SQLite's backup API or stop upstream before copying so WAL contents are included correctly. Do not copy only the main database file while upstream is writing.

```sh
cd go-backend
CGO_ENABLED=0 go build -o audiobookshelf-go ./cmd/audiobookshelf-go
CONFIG_PATH=/path/to/snapshot/config \
METADATA_PATH=/path/to/snapshot/metadata \
HOST=127.0.0.1 PORT=23378 \
./audiobookshelf-go
```

The default runtime reads and writes the database and metadata. Use disposable copies while compatibility work is incomplete. A new database is created only when the configured file is absent; existing databases must match the pinned migration version and are never silently recreated or migrated.

Build the original frontend with `npm ci` and `npm run generate` in `client/`, then set `ABS_GO_CLIENT_PATH` to its `dist` directory if the default path cannot locate it. No Node process is used by the Go runtime; Node is used for building frontend assets and running the reference tests.

`ABS_GO_READ_ONLY=1` is a development option for the restricted read-only surface. It does not provide a full read-only equivalent of every source endpoint. Unimplemented routes and variants remain explicit failures. Existing mobile/web workflows, all filters, background tasks, providers and mutations have not been exhaustively verified.

## Development

From the upstream repository root:

```sh
python3 -m unittest -v go-backend/tools/test_source_inventory.py
python3 go-backend/tools/source_inventory.py --output go-backend/contracts/source-inventory.json
cd go-backend
CGO_ENABLED=0 go test ./...
CGO_ENABLED=0 go build ./cmd/abs-compat
node tools/test-library-oracle.cjs
```

Example comparison, using two disposable servers with equivalent fixture data:

```sh
go run ./cmd/abs-compat \
  -left http://127.0.0.1:13378 \
  -right http://127.0.0.1:23378 \
  -cases contracts/smoke-cases.json
```

Tokens, if needed, come from `ABS_COMPAT_LEFT_TOKEN` and `ABS_COMPAT_RIGHT_TOKEN`. Case paths include the complete deployment prefix (for example `/audiobookshelf/api/libraries`). Select cases deliberately: upstream includes GET routes that trigger work, so GET alone does not guarantee absence of side effects. Use disposable, isolated fixture copies.

## Fork setup

The fork is `https://github.com/ppoloskov/audiobookshelf-go`. The local implementation branch is `go-backend`; `origin` points to that fork and `upstream` to `advplyr/audiobookshelf`. Remote publishing depends on GitHub write access. Do not push this branch to the upstream repository.
