# Audiobookshelf Go backend — handoff snapshot

Prepared 2026-10-29. Target upstream v2.37.2, commit `edb1f7efc92ecacdef51aafb4d9d5054ce10604f`.

This is an experimental implementation snapshot, not a completed drop-in replacement. Development has been stopped and packaged at the user's request. Existing database and media should be tested using independent disposable copies.

## Archive contents

- Original upstream source and retained GPL-3.0 license/attribution.
- Go implementation, tests, source inventories and compatibility tooling in `go-backend/`.
- Built original frontend in `client/dist/`.
- Statically linked Linux amd64 executable in `bin/audiobookshelf-go` (Go 1.26.1, CGO disabled).
- Final package-test output in `go-backend/final-go-tests.txt`.
- `SNAPSHOT.json` identifies the exact source commit and binary hash.

Dependency caches, node_modules, live data, generated test databases/media, credentials and Git history are excluded. Go module dependencies are pinned in go.mod/go.sum; building source requires downloading dependencies if they are not cached. The executable does not require Go or Node to run. FFmpeg and ffprobe must be installed separately for media operations.

## Run

From the extracted archive root, on Linux amd64:

```sh
CONFIG_PATH=/absolute/path/to/disposable/config \
METADATA_PATH=/absolute/path/to/disposable/metadata \
ABS_GO_CLIENT_PATH="$PWD/client/dist" \
HOST=127.0.0.1 PORT=23378 \
./bin/audiobookshelf-go
```

Open `http://127.0.0.1:23378/audiobookshelf/`. The default runtime writes data. An absent database can be initialized; existing databases must match the verified pinned schema. Historical schema migration compatibility is incomplete. Never run the reference Node server and this implementation against the same writable database, metadata or media tree.

To build on another supported platform:

```sh
cd go-backend
CGO_ENABLED=0 go build -trimpath -o audiobookshelf-go ./cmd/audiobookshelf-go
```

## Final checks

- Fresh `CGO_ENABLED=0 go test -count=1 -timeout=180s ./...`: passed.
- `CGO_ENABLED=0 go vet ./...`: passed.
- `CGO_ENABLED=0 go build -trimpath ./cmd/audiobookshelf-go`: passed.
- Python source-inventory tests: 5 passed.
- `git diff --check`: passed before packaging.

Existing reports cover sampled source-backed comparisons, not complete feature parity. A new full differential run was not performed for this final snapshot. The provisional scanner report explicitly excluded from compatibility counts remains marked invalid. Earlier race checks apply to checkpoint 8aac9cd, not every change in this archive.

## Remaining work and limits

- Full API, query-variant, permission, event and background-job parity remains unverified; source routes may be absent or explicitly fail.
- Provider and podcast packages contain implementations and unit tests but are not yet connected to the server runtime dispatch/lifecycle.
- Metadata matching and notification lifecycle hooks need further integration.
- Existing-schema upgrades and exhaustive mobile/web acceptance remain incomplete.
- Narrator updates can partially apply if a later write fails; response serialization errors can omit updated items from emitted results.
- Audiotools task-data publication/concurrency needs follow-up review and current-snapshot race tests.

See README.md, STATUS.md, ROADMAP.md, VALIDATION.md and contracts/parity for scope and evidence. A passing package test is not a claim of drop-in compatibility.

GitHub publishing was previously blocked by missing Git push credentials and GitHub integration write permission (403). This archive is the local handoff; no successful remote push is claimed.
