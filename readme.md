# Audiobookshelf Go

**Audiobookshelf, powered by a Go server.** A drop-in server replacement for [Audiobookshelf](https://github.com/advplyr/audiobookshelf) **v2.37.2**, retaining the familiar web interface and existing library data.

[![Container](https://img.shields.io/badge/GHCR-audiobookshelf--go-blue)](https://github.com/ppoloskov/audiobookshelf-go/pkgs/container/audiobookshelf-go)

> **Independent, experimental rewrite.** Not affiliated with or endorsed by the upstream Audiobookshelf project. This project targets v2.37.2 compatibility; mobile-client and edge-case verification is still in progress. Back up your installation before switching.

## Why this exists

[Audiobookshelf](https://github.com/advplyr/audiobookshelf) has always been my favourite self-hosted app. I've consistently voted for it in the [selfh.st](https://selfh.st/) surveys. It is an excellent piece of software, and I'm grateful to its maintainers and contributors for building something I genuinely enjoy using.

I've never been particularly enthusiastic about TypeScript, the complexity and potential dependency headaches of the npm ecosystem, or the resource overhead that can accompany a Node.js server. That's a personal preference, not a criticism of the people who built Audiobookshelf or a claim that Go is automatically faster.

This rewrite is an experiment: **how far can we simplify and optimize the Audiobookshelf server without sacrificing what makes the original great?** Can it use less memory, start faster, do less work per request, and ship in a smaller, simpler runtime? Those are questions to measure, not improvements to assume.

All credit for Audiobookshelf's product, interface, and original functionality belongs to the upstream project and its contributors.

## An AI-assisted engineering experiment

**This project is heavily AI-assisted.** AI coding agents contribute substantial work: exploring upstream behavior, implementing and refactoring Go code, writing tests, and investigating regressions. I set the direction and compatibility requirements, make architecture and scope decisions, and review the results. It is also an experiment in how far agent-assisted development can go on a real application with years of accumulated behavior.

AI-generated code is not treated as correct merely because it compiles or looks plausible. The project is built around a reference implementation, explicit compatibility contracts, reproducible tests, and rollback checks. These provide evidence, not a guarantee that every bug has been found.

### The upstream server is the oracle

Audiobookshelf **v2.37.2**, pinned to upstream commit [`edb1f7e`](https://github.com/advplyr/audiobookshelf/commit/edb1f7efc92ecacdef51aafb4d9d5054ce10604f), is the behavioral reference. When an API response, database value, or lifecycle transition is unclear, we inspect the original source and, where useful, run the original TypeScript server against disposable fixtures to find out what actually happens.

The oracle defines the **meaning of externally observable behavior**, not how the Go code must be structured. We do not aim for byte-for-byte copies of incidental JSON formatting, internal abstractions, or unconsumed error wording. We do preserve the semantics that clients and stored data depend on.

### Tests protect the contract

The test strategy uses several complementary layers:

- **Go tests** exercise API behavior, authentication, data operations, playback and progress, scanning and deletion, podcasts, real-time events, and other functional and lifecycle cases.
- **Persistence tests** protect the SQLite schema and the meaning of stored values; the Go server must read existing v2.37.2 data without an implicit migration.
- **Rollback integration tests** start the pinned TypeScript server on state written by Go, checking that progress, bookmarks, and sessions remain readable. These are required to publish an image.
- **Differential and browser tests** run comparable journeys against the original and rewritten servers, including the unchanged web client. They provide useful diagnostic evidence, but the legacy differential suite is advisory rather than a release gate.
- **CI checks** build, vet, test, enforce selected architectural constraints, and cross-compile for arm64 before publishing multi-platform images.

Coverage is still incomplete, particularly for real mobile devices and some edge cases. Passing CI means the defined checks passed; it does not mean every Audiobookshelf behavior has been proven equivalent.

### Architecture: preserve the edges, simplify the middle

Two boundaries are deliberately fixed:

1. **Stored data:** the v2.37.2 SQLite schema, value formats, and `/config` and `/metadata` layouts must remain compatible, including the ability to roll back to upstream.
2. **Client behavior:** HTTP APIs, authentication, Socket.IO events, streaming and range semantics, and the fields and state transitions clients actually use must keep working.

Inside those boundaries, the implementation is free to be idiomatic Go rather than a translation of TypeScript or Express. The goals are fewer moving parts, clear ownership of behavior, well-supported libraries where they remove complexity, a `CGO_ENABLED=0` server, no Node.js at runtime, and measurable performance improvements. We preserve features—including backup/restore and audio tooling—rather than cutting functionality to produce attractive code-size numbers.

See the [architecture](https://github.com/ppoloskov/audiobookshelf-go/blob/master/go-backend/ARCHITECTURE.md), [test approach](https://github.com/ppoloskov/audiobookshelf-go/blob/master/go-backend/TEST-COMPATIBILITY-IMPLEMENTATION.md), and [roadmap](https://github.com/ppoloskov/audiobookshelf-go/blob/master/go-backend/ROADMAP.md) for the detailed contracts, evidence, and outstanding work.

## What changes?

- **Go instead of Node.js** for the server; the shipped runtime doesn't require Node.
- **Existing interface:** bundles the unmodified Audiobookshelf v2.37.2 web client.
- **Existing data:** preserves the v2.37.2 SQLite schema, `/config`, `/metadata`, libraries, user data, and playback-progress formats.
- **Existing protocols:** implements the HTTP API, authentication and Socket.IO behavior used by clients.
- **Container-friendly:** Alpine runtime with FFmpeg, ffprobe, timezone data and an HTTP health check.
- **Multi-platform:** Linux `amd64` and `arm64` images.

This is a backend rewrite, **not** a separate application with a new database or frontend. Compatibility is an ongoing engineering goal, not a claim that every third-party mobile client is fully validated.

## Quick start with Docker Compose

Create `compose.yaml`:

```yaml
services:
  audiobookshelf:
    image: ghcr.io/ppoloskov/audiobookshelf-go:latest
    ports:
      - "13378:80"
    volumes:
      - ./config:/config
      - ./metadata:/metadata
      - ./audiobooks:/audiobooks
      - ./podcasts:/podcasts
    restart: unless-stopped
```

Start it:

```bash
docker compose up -d
```

Open **http://localhost:13378** or `http://<your-server>:13378`.

For reproducible deployments, prefer a published version or `sha-...` tag over `latest`. The container publishes Linux `amd64` and `arm64` variants.

## Switching from an existing Audiobookshelf installation

The intended migration is to **replace the container image while keeping the same ports and volume mounts**.

1. Make sure the original installation has been started successfully on **upstream Audiobookshelf v2.37.2**. The Go server does **not** automatically migrate an older or newer SQLite database schema.
2. Stop the original container. Do **not** run the TypeScript and Go servers against the same writable `/config` or `/metadata` trees.
3. Back up **both `/config` and `/metadata`**. If backing up a live SQLite database, use SQLite's backup API or stop the server before copying so WAL data is not missed.
4. Change only the image in your existing Compose file to `ghcr.io/ppoloskov/audiobookshelf-go:latest` (or a pinned tag), and start it.
5. Check `/healthcheck`, sign in, verify libraries and progress, and test the clients you depend on before retiring your backup.

An incompatible existing database migration level causes the Go server to refuse startup rather than silently change the database.

### Rollback

Stop the Go container and restore the upstream v2.37.2 image, keeping the same mounts. Automated CI includes a rollback test verifying that the upstream server can reopen Go-written state. Restore your backup if you need to discard changes made after the switch.

## Configuration

The image uses the same core runtime paths and port as the original Docker deployment:

| Environment variable | Default | Purpose |
| --- | --- | --- |
| `PORT` | `80` | HTTP listener port inside the container |
| `CONFIG_PATH` | `/config` | Database and configuration directory |
| `METADATA_PATH` | `/metadata` | Covers, cache, logs, backups and related metadata |
| `SOURCE` | `docker` | Deployment source identifier |
| `ABS_GO_CLIENT_PATH` | `/app/client/dist` | Bundled web client location |
| `ROUTER_BASE_PATH` | `/audiobookshelf` | Optional base-path routing, as in upstream |

The image includes a health check at `GET /healthcheck`. Logs are written to the console and to the upstream-compatible log directory under `/metadata/logs`.

If you're using a reverse proxy, preserve WebSocket forwarding for client realtime connections.

## Build from source

To build the published-style image from this repository's current source layout:

```bash
git clone https://github.com/ppoloskov/audiobookshelf-go.git
cd audiobookshelf-go
docker build -f Dockerfile.go -t audiobookshelf-go:local .
```

To build the Go server without Docker (Go version from `go-backend/go.mod` and FFmpeg/ffprobe are required):

```bash
cd go-backend
CGO_ENABLED=0 go build -o audiobookshelf-go ./cmd/audiobookshelf-go
```

A standalone Go binary must be pointed at a generated web-client `dist` directory using `ABS_GO_CLIENT_PATH` if the default client lookup is unavailable.

## Status and limitations

The project preserves the v2.37.2 persistence contract and supports rollback to that version. Automated verification includes Go tests, static checks, and an upstream database rollback check. Differential testing against the TypeScript server is advisory rather than a publication gate.

**Not all clients and behaviors are fully certified.** Remaining work includes mobile-client and Socket.IO acceptance, some lifecycle edge cases, and compatibility audits. Before using this as your primary audiobook server, check the [current roadmap](https://github.com/ppoloskov/audiobookshelf-go/blob/master/go-backend/ROADMAP.md) and test your own critical flows.

## Project links

- [Source repository](https://github.com/ppoloskov/audiobookshelf-go)
- [Container on GitHub Packages](https://github.com/ppoloskov/audiobookshelf-go/pkgs/container/audiobookshelf-go)
- [Technical architecture](https://github.com/ppoloskov/audiobookshelf-go/blob/master/go-backend/ARCHITECTURE.md)
- [Roadmap and known gaps](https://github.com/ppoloskov/audiobookshelf-go/blob/master/go-backend/ROADMAP.md)
- [Original Audiobookshelf project](https://github.com/advplyr/audiobookshelf)

## License

GPL-3.0, consistent with the upstream Audiobookshelf project. See the repository's [LICENSE](https://github.com/ppoloskov/audiobookshelf-go/blob/master/LICENSE).
