# TQ01 implementation and acceptance

2026-11-06 · baseline `fda317d1` · production Go behavior unchanged.

R02/R03/R05 were implemented and tested first, followed by individually scoped
R01/R04/R06/R07/R08 changes. Three cheap workers implemented bounded test/tool
packets; architect integration and separate contract/simplicity reviews retained
security, persistence, HTTP and client-visible behavior. No production Go file,
schema, dependency, source golden, existing installation or Monstrobook file
was changed. Ratchet/dbnest confirms every production metric is unchanged.

## Delivered assertions

| Family | Default compatibility evidence | Separate representation evidence |
|---|---|---|
| R02 | Independent decoded fields/types/values, exactly one JSON document and actual emitted body length. | Sorted key/escape spelling and fixed byte count. |
| R03 | Streamed JSON meaning, EOF, no-length option, HEAD-empty and GET-equivalent representation length. | Encoder newline spelling. |
| R05 | Parsed log timestamps/source/message/level, empty-array shape, fatal record ordering and retained destinations/filtering. | JSON member order, escape bytes and adjacent fatal fields. |
| R01 | Opaque entity-tag grammar, unchanged replay, weak/strong/list/wildcard/nonmatch rules, changed data invalidates the old validator, updated-validator replay and HEAD/range behavior. Error body/status/MIME are retained. | Express SHA-1 vector and body/stat tag algorithms. |
| R04 | Error information in parsed visible HTML, generic public panic information, no secret/stack/query leakage, CSP/nosniff, hostile-path escaping, range headers, media/path protections and empty HEAD. | Exact finalhandler templates and panic fragment. |
| R06 | Strict parsed OPML structure, decoded attributes/title/URLs, order, ACL, MIME and original negative cases. Formatting variation passes; hidden/duplicate/namespaced content and trailing data fail. | Original seven XML byte goldens and empty-document indentation. |
| R07 | Four manually expected text/br token sequences, entity/br equivalence and markup/text mutation negatives. The entire 59-row original table is preserved; the other 55 rows keep exact expectations. | The four original entity/void-tag spellings. |
| R08 | PATCH status, success true, required fields, fixture-rooted paths, captured response length when advertised, opaque ETag syntax and parsed JSON equality. Repeat response and persisted path must match the independently expected metadata destination; cache/file checks remain. | Original JSON.stringify/length/SHA-1 reconstruction on explicitly captured sidecar inputs. |

Original named test functions remain, except the pure
`TestExpressETagMatchesExpress` function moves intact to the tagged diagnostic
lane. Adjacent HEAD/no-length and pagination ETag assertions are mapped too.
No I01–I08 semantic quirk was fixed: notably OPML double escaping/literal null,
OIDC settings behavior, stored name derivations and ICU sorting retain evidence.

## Machine-readable traceability

- [test-assertions.json](contracts/test-assertions.json): 22 original assertion
  groups with disposition, original file/symbol/hash, replacement/diagnostic
  targets, and 12 explicit retained controls. Units are named per group; this
  is not an exhaustive count of every assertion in the repository.
- [monstrobook-test-reconciliation.json](contracts/monstrobook-test-reconciliation.json):
  read-only hashes, source revision and indexed references for 19 top-level Go
  contracts, 491 journey steps and 209 pinned routes. Twenty review references
  are 19 unique labels, including 17 Go function names and two parity case IDs.
  None directly appears in the 19-entry Go inventory; related journey/route
  evidence is qualified rather than promoted to assertion coverage.
- [test-assertion-results.json](contracts/test-assertion-results.json): actual
  command/exit records, recovered failures, final results, exclusions and raw
  artifact paths/hashes. Retained source/schema/oracle/rollback hashes and the
  original sanitizer table span are checked by
  `tools/test-assertion-manifest.cjs`.

Monstrobook's imports/assessment remain pinned to `68e302cf`. No import refresh,
external score change or Monstrobook implementation test run is claimed.
The current packet changes this repository's gates, not that frozen assessment.

## Lanes and reproduction

Default `go test ./...` excludes `//go:build tsdiagnostic` files. Run historical
Go fingerprints explicitly with `make test-ts-diagnostics`. The separate
`representation-diagnostics` CI job is advisory. The previously inconsistent
`upstream-parity` job is now advisory too; required semantic verification,
source-oracle integrity and image rollback gates remain.

```sh
# From repository root, using Node 22:
node --test go-backend/tools/test-assertion-manifest.cjs \
  go-backend/tools/parity/test-cover-patch-contract.cjs
MONSTROBOOK_ROOT=/path/to/monstrobook \
  node --test go-backend/tools/test-assertion-manifest.cjs
node --test go-backend/tools/parity/test-representation-diagnostic.cjs
```

For actual Node fingerprints, add `--representation-diagnostics-out <sidecar>`
to a **full** `tools/parity/run-local.cjs` invocation with a fresh fixture and
built Go binary, then run `node tools/parity/representation-diagnostic.cjs
<sidecar>` from `go-backend`. Normal reports omit the raw diagnostic inputs.
Combining the new sidecar flag with `--cases` is explicitly rejected; focused
runs do not execute the full cover workflow. An empty sidecar fails, never passes.

Go acceptance used the clean archive plus approved overlays at
`/private/tmp/tq01-implementation-20261106/shipping`, Node 22.23.4, Go 1.27.0
with `GOTOOLCHAIN=local`, `CGO_ENABLED=0`, temporary caches and the native codec
prefix from [TESTING.md](TESTING.md). The inherited untracked
`internal/identity/routes.go`, `.codemap/` and `TODO.md` were preserved/excluded.
Installed dependencies were copied unchanged for reference compilation; no
dependency was installed or edited. Git oracle reads used the canonical object
database with the snapshot as work tree, without a branch or Git mutation there.

## Actual results and limitations

| Check | Final result |
|---|---|
| Clean baseline affected five Go packages | PASS |
| Build / vet / lint | PASS; lint zero issues |
| Full Go suite | PASS; 454 top-level tests, 1,536 test/subtest pass events, 40 packages with tests; three tests skipped below |
| Semantic/harness Node tests | 38/38 PASS |
| Manifest plus actual external snapshot reconciliation | 3/3 PASS; without external env, 2 PASS and one deliberate external-only skip |
| Go representation diagnostics | Ten tests across five packages PASS |
| Node diagnostic self-tests | 2/2 PASS |
| Fresh live Node/Go core | 193/193 executed/pass, zero failures/skips; all state checks pass |
| Live PATCH representation diagnostic | Both cases × both servers PASS, separate from compatibility verdict |
| Pinned TS reopening Go-written data | PASS: started, ping, login, libraries and book item |
| Ratchet / nested-query gate | PASS; 44,674 production lines / 10,809 complexity, all metric deltas zero |

Unexecuted tests in the final Go run:

- `TestUpstreamCreatedBackupImports` and `TestExportNodeFixtureDatabaseForNodeImport`
  (historical): both were env-gated (`ABS_BACKUP_*`) and always skipped in
  `go test`; they were removed in the step 3 test-quality pass. Node/Go backup
  archive compatibility is covered only by the manual harness
  `tools/parity/test-backup-restore.cjs`; the independent rollback check above
  did run.
- `TestTranscodedSessionServesHLSWithoutBearerAndRejectsBadFiles`: the existing
  test checks `/usr/bin/ffmpeg`, absent on this Mac, despite the verified native
  codec prefix. No skip or production code was added to accommodate this.

Socket.IO logging initially skipped for its client-relative module lookup; the
snapshot exposed the already installed client, and the named test and final full
suite executed it successfully. Browser/device acceptance, race, benchmarks,
Monstrobook scoring and broader `parity-full` suites were not run.

Recovered failures are recorded, not counted as passes: initial cache/loopback
sandbox denials; the lint shim lacking a configured version; a reference compiler
crash with symlinked dependencies (fixed with an unchanged physical copy);
test-authoring assumptions about OPML visibility, text-token counts and HTTP
status wording; and one manifest hash typo/field-name error. The first real core
run passed all 193 API cases but failed our new repeat-state assertion because
it expected the submitted source path. Pinned CoverManager validation copies
that image to metadata; correcting the independent expected destination and
rerunning on fresh data passed the entire workflow. Production behavior was
never changed to satisfy a test.

Final contract/craft review fixes also cover opaque-tag obs-text bytes, changed
content receiving a fresh validator, active attributes in error pages, strict
OPML structure and hidden-content negatives. No new general test framework or
production abstraction was added. Diagnostic fingerprints remain useful
evidence, not requirements that Go emulate a framework's encoding algorithm.
