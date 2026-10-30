# Validation record

Baseline: Audiobookshelf v2.37.2, `edb1f7efc92ecacdef51aafb4d9d5054ce10604f`.

Local checks use Go 1.26.1 with CGO disabled, Python 3.12 and Node.js.

- Go package tests and `go vet ./...`.
- Five Python source-inventory fixture tests.
- Node source oracle: execute upstream Library/LibraryFolder serializers and LibraryController.findAll, then compare the reproducible golden and permission scenarios.
- Store tests compare the Go SQLite serializer with that source-executed golden, reject writes, reject missing schema, and verify database bytes remain unchanged.
- Linux amd64 and arm64 server compilation with `CGO_ENABLED=0`.
- Executable smoke on a disposable synthetic SQLite fixture: health/status, a prefixed library request with signed user token, rejection of an unauthenticated library request, empty folders encoded as an array, graceful process shutdown, and unchanged database bytes.

No real user database is used. `contracts/parity/schema.sql` is now exported from an actual fresh initialization of the pinned Node server, including the full schema and migration state. The live differential tools start upstream and Go on independent copies of a seeded book/podcast fixture.

The persisted core report covers 17 sampled scenarios: authentication, restricted library visibility, book DTOs, direct byte ranges, progress, playback/HLS and both Socket.IO paths. Further mutation scenarios are added as implementation proceeds; see the current case registry and sanitized reports for their exact scope. A passing sampled request does not verify every variant of that route or complete client compatibility. Multi-client recipient tests, schema reopen tests and generated-media tests run separately. Unresolved failures must remain visible in the suite.

CI definitions are included in `.github/workflows/go-backend.yml`. Remote CI has not run because this branch has not been published.
