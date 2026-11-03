# Roadmap

An ordered list of atomic tasks for the Go backend. Each task is one commit by
one agent. Required checks are in [AGENTS.md](AGENTS.md); the architecture
target is [ARCHITECTURE.md](ARCHITECTURE.md); current verification is in the
generated [STATUS.md](STATUS.md). Dated reviews in `archive/reviews/` are
evidence, not a task source.

Compatibility boundary is unchanged: pinned upstream v2.37.2, the TypeScript
server is the behavior oracle, no Node fallback at runtime.

Conventions:

- Tick the box (`- [x]`) in the commit that completes the task.
- Metric values below are from the ratchet and `grep` at the time this list
  was written (`map[string]any` 1020, hand-rolled `Handle` routers 13,
  `writeJSON`-style copies 12, `EmitFunc` declarations 7, `Set*` injectors
  18). Re-measure before relying on a number. Where a task says "from N to
  <= M", N is the value before that task, so run a group's tasks in the order
  listed.
- Paths are relative to the repository root unless they start with `go-backend/`.
- Test names in "Done when" are the tests to add; they do not exist yet unless
  the task says otherwise.

## Correctness gaps

- [ ] C1: Close a share when it expires, without waiting for a request
  - Files: `go-backend/internal/sharing/shares.go` (expiry checks near lines
    165 and 232; `share_closed` emit near line 201)
  - Change: On share create and at startup, schedule a timer per share with a
    non-zero `expiresAt` that closes it and emits `share_closed` to `@admins`
    (upstream `server/managers/ShareManager.js:165`). Keep the request-time
    check as a backstop.
  - Done when: `go test ./internal/sharing -run TestShareClosesAtExpiryWithoutRequest`
    passes; the test sees `share_closed` with no HTTP request.

- [ ] C2: Reschedule the podcast auto-download cron after `PATCH` media
  - Files: `go-backend/internal/catalog/writes.go` (`autoDownloadSchedule` and
    `autoDownloadEpisodes` writes, lines 74 and 939-942);
    `go-backend/internal/podcasts/podcasts.go` (`addSchedule` line 981,
    `scheduleIDs` lines 992 and 1024); `go-backend/internal/server/runtime.go`
  - Change: Give `catalog` an injected callback, set in `runtime.go` like
    `SetItemChanged`, that calls a podcasts method which removes the old cron
    entry and adds the new one whenever either field changes.
  - Done when: `go test ./internal/server -run TestPatchAutoDownloadScheduleReschedules`
    passes (the scheduler entry changes after `PATCH`).

- [ ] C3: Make `POST /api/watcher/update` touch only the changed path
  - Files: `go-backend/internal/scanner/scanner.go` (`UpdateWatchedPath`,
    lines 196-229, ends in `ScanForce` on the whole library); upstream
    `server/Watcher.js` (`onFileAdded`, `onFileRemoved`, `onFileRename`) and
    `server/controllers/MiscController.js:579-612`
  - Change: Replace the final library-wide `ScanForce` with the per-file add,
    remove or rename handling that `Watcher.js` performs.
  - Done when: `go test ./internal/scanner -run TestUpdateWatchedPathDoesNotScanLibrary`
    passes and `make parity-scan` reports every step matching.

- [ ] C4: Make match-all honor a library cancel
  - Files: `go-backend/internal/catalog/quickmatch.go` (`libraryMatchAll` line
    852, `matchLibraryItems`); `go-backend/internal/scanner/scanner.go` (`Cancel`
    line 175, `runs` map line 47); `go-backend/internal/server/runtime.go`
    (`cancel_scan` dispatch, around line 158)
  - Change: Register the match-all run in the same cancel registry the scanner
    uses and check it between items, as upstream `matchLibraryItemsChunk` does
    (`server/scanner/Scanner.js:457`). Today the run is started on
    `h.background` and nothing can stop it.
  - Done when: `go test ./internal/catalog -run TestMatchAllStopsOnCancel`
    passes (a cancel after the first item leaves the rest unmatched).

- [ ] C5: Wire Socket.IO `search_covers` and `cancel_cover_search`
  - Files: `go-backend/internal/server/runtime.go` (`SetDispatcher` switch,
    lines 156-175, has no case for these events); `go-backend/internal/realtime/hub.go`
    (`handleSearchCovers` around line 395, `dispatchAction` line 422);
    `go-backend/internal/providers/handler.go` (`GET /api/search/covers`, line 68);
    upstream `server/SocketAuthority.js:370-430`
  - Change: Add dispatcher cases that run the provider cover search and emit
    `cover_search_result` / `cover_search_error` with the request ID, as
    `SocketAuthority.handleCoverSearch` does. Today a socket search returns
    nothing and no error.
  - Done when: `grep -n 'search_covers' go-backend/internal/server/runtime.go`
    is non-empty and a socket test (`go-backend/tools/parity/socket-soak.cjs`
    or a `realtime` test) receives `cover_search_result`.

- [ ] C6: Stop the sanitizer writing `'` as `&#39;`
  - Files: `go-backend/internal/auth/sanitize.go` (`html.EscapeString` at
    lines 40, 52 and 74); upstream `server/libs/sanitizeHtml/index.js`
    (`escapeHtml`, lines 629-637)
  - Change: Replace `html.EscapeString` with an escaper that encodes only
    `&`, `<`, `>` in text and also `"` in attributes, as upstream does.
  - Done when: `go test ./internal/auth -run TestSanitizeHTMLKeepsApostrophe`
    passes (`<p>it's</p>` is returned unchanged) and
    `grep -n 'html.EscapeString' go-backend/internal/auth/sanitize.go` is empty.

- [ ] C7: Add the title tie-break to the `authorNameLF` sort
  - Files: `go-backend/internal/catalog/handler.go` (`orderClause`, the
    `fields` map with `media.metadata.authorNameLF`); upstream
    `server/utils/queries/libraryItemsBookFilters.js:279` (`getTitleOrder()`)
  - Change: Append the title tie-break upstream adds. Check `authorName` in the
    same upstream function and apply the same rule if it differs.
  - Done when: `go test ./internal/catalog -run TestAuthorNameLFSortTieBreaksByTitle`
    passes and `make parity` has no new differences.

- [ ] C8: Parse last-first names with `nameparse` in the scanner
  - Files: `go-backend/internal/scanner/files.go` (`lastFirst`, line 1725, and
    its callers in that file); `go-backend/internal/nameparse/nameparse.go`
    (`ToLastFirst`, line 316)
  - Change: Replace the naive split (first word moved to the end) with
    `nameparse.ToLastFirst`. Leave the copy in `catalog/book_wire.go` for
    task K11 to review.
  - Done when: a table test in `internal/scanner` covering the names in
    `nameparse`'s tests passes, and `grep -n 'func lastFirst' go-backend/internal/scanner/files.go`
    is empty.

- [ ] C9: Include directory entries in single-item download zips
  - Files: `go-backend/internal/catalog/download.go` (the `filepath.WalkDir`
    callback, about lines 121-150, returns early on `entry.IsDir()`)
  - Change: Write a zip directory entry (name ends in `/`, no data) for each
    directory, before its files, as the upstream zip helper does
    (`server/utils/zipHelpers.js`).
  - Done when: `go test ./internal/catalog -run TestSingleItemDownloadListsDirectories`
    passes and `make parity` passes.

- [ ] C10: Write `metadata.json` with the bytes `JSON.stringify` produces
  - Files: `go-backend/internal/scanner/metadata.go` (`json.Marshal` near line
    27; `json.MarshalIndent` and the appended `'\n'` at lines 71-75); upstream
    `server/models/LibraryItem.js:674` (`JSON.stringify(jsonObject, null, 2)`,
    no trailing newline)
  - Change: Drop the trailing newline, and stop HTML-escaping `<`, `>` and `&`
    (Go's encoder escapes them; `JSON.stringify` does not).
  - Done when: `go test ./internal/scanner -run TestMetadataJSONMatchesStringify`
    passes against a golden generated by upstream for a title containing `<&>`
    and `'`, and `make parity-scan` passes.

- [ ] C11: Close a series' feeds when the scanner removes the series
  - Files: `go-backend/internal/scanner/scanner.go` (series removal, `DELETE FROM
    series`, line 1220); `go-backend/internal/catalog/library_issues.go`
    (`closeFeeds`, line 97); upstream `server/scanner/BookScanner.js:1002`
  - Change: Close the series' feeds before the delete, emitting
    `rss_feed_closed` as `closeFeeds` does. Feature packages must not import each
    other, so move `closeFeeds` into a package both can import first (for
    example a new `internal/feeds`).
  - Done when: `go test ./internal/scanner -run TestRemovingSeriesClosesItsFeeds`
    passes.

- [ ] C12: Characterize tag permission rules (review B6) before unifying
  - Files: `go-backend/internal/playback/playback.go` (`canAccess`, line 1744);
    `go-backend/internal/emailapi/handler.go` (`libraryAllowed` line 527,
    `canAccessContent` line 543); `go-backend/internal/userapi/userapi.go`
    (`canAccessTags`, line 2354)
  - Change: For each rule, write a test with a comment giving the upstream
    `file:line` that defines it (any-tag or every-tag). Fix a Go path only where
    it differs from its upstream rule. Do not unify yet.
  - Done when: `go test ./internal/playback ./internal/emailapi ./internal/userapi -run TagACL`
    passes and every rule has a cited upstream line.

- [ ] C13: Add a `-race` regression test for podcast download task state (review B4)
  - Files: `go-backend/internal/podcasts/downloads.go` (`runDownload`, lines
    172-200, writes under `queueMu`; `clearQueue`); `go-backend/internal/podcasts/` (new `_test.go`)
  - Change: The writes at lines 182-191 already hold `queueMu`. Add a test
    that runs concurrent download and poll paths so the lock stays covered.
  - Done when: `CGO_ENABLED=1 go test -race -run TestDownloadTaskState ./internal/podcasts`
    passes.

- [ ] C14: Prune rate-limit buckets and expired sessions (review B8)
  - Files: `go-backend/internal/identity/identity.go` (`rateBuckets` field line
    58, updates near lines 1093-1098, session `DELETE`s at lines 426-518);
    `go-backend/internal/identity/oidc.go` (flow map pruning near 748-759)
  - Change: Drop expired `rateBuckets` entries on access, and run a session
    reaper started by the composition root and stopped by `Close`.
  - Done when: `go test ./internal/identity -run TestRateBucketsPruned` passes
    (entries for expired windows are removed).

- [ ] C15: Stop discarding JSON errors in request paths (review B9)
  - Files: `go-backend/internal/userapi/userapi.go` (`_ = json.Unmarshal`, for
    example lines 673, 1997, 2112, 2237, 2300); `go-backend/internal/identity/identity.go`
    (unchecked `payload["user"].(map[string]any)`, lines 384 and 481, and
    `_ = json.Unmarshal` at 1132 and 1141); `go-backend/internal/identity/oidc.go`
    (line 311)
  - Change: For each site, first confirm the upstream outcome for a malformed
    body, then return the matching status or use a comma-ok assertion.
  - Done when: `grep -rn '_ = json.Unmarshal' go-backend/internal/userapi go-backend/internal/identity --include=*.go`
    is empty and `make parity` passes.

- [ ] C16: Find where `emailapi` truncates attachments (review B9)
  - Files: `go-backend/internal/emailapi/handler.go` (attachment handling;
    review B9 in `archive/reviews/ARCHITECTURE-REVIEW-2026-10-29.md` names it)
  - Change: Locate the silent truncation and make it match upstream: the
    upstream outcome is recorded in its code, so confirm it there first.
  - Done when: a test covers an oversized attachment and `make parity` passes.

- [ ] C17: Add scan-trial steps for ebook-only items and watcher folder renames
  - Files: `go-backend/tools/parity/scan-trial.cjs`
  - Change: Add steps for an ebook-only item and a folder rename seen by the
    watcher, both compared with upstream. These paths are not exercised today
    (no trial exercises them today).
  - Done when: `make parity-scan` passes with the new steps (the step count
    goes up).

- [ ] C18: Stop tracking `go-backend/final-go-tests.txt`
  - Files: `go-backend/final-go-tests.txt` (tracked), `go-backend/.gitignore`
  - Change: `git rm` the file and ignore the path if it is regenerated.
  - Done when: `git ls-files go-backend | grep -c final-go-tests` prints 0.

- [ ] C19: Run the test suite under the race detector in CI
  - Files: `.github/workflows/docker-go.yml` (`verify` job)
  - Change: Add a step `CGO_ENABLED=1 go test -race ./...`. The shipped binary
    stays `CGO_ENABLED=0`. Install a C compiler on the runner if needed.
  - Done when: the step is green on master.

- [ ] C20: Fix the SQLITE_BUSY flake in `TestCreatePodcastValidatesFolderAndPersistsSourceShape`
  - Files: `go-backend/internal/podcasts/*_test.go`
  - Change: Find the competing writer under a parallel `go test ./...` run and serialize it, or give the test its own database.
  - Done when: `go test -count=20 ./internal/podcasts` passes inside a full parallel `go test ./...` run.

## Code consolidation (copy/paste removal)

Helper families are listed with file and line. Each task removes one family
or one package's copies. Leaf packages named here do not exist yet; the task
creates them.

- [x] K1: Create `internal/httputil` with the Express-exact JSON writer; move the server's three writers into it
  - Files: `go-backend/internal/server/server.go` (`writeJSON` line 250,
    `writeJSONForRequest` line 254, `writeJSONBody` line 258); new
    `go-backend/internal/httputil/json.go`
  - Change: One exported JSON writer (HEAD handling included) lives in
    `httputil`. Callers in `server` use it.
  - Done when: ratchet `writeJSON-style copies` goes from 12 to <= 10.

- [x] K2: Move the `writeJSON` copies in `catalog` and `identity` to `httputil`
  - Files: `go-backend/internal/catalog/handler.go` (line 735, takes `r`);
    `go-backend/internal/identity/identity.go` (line 1175)
  - Change: Delete both copies and call `httputil`.
  - Done when: ratchet `writeJSON-style copies` goes from 10 to <= 8.

- [x] K3: Move the `writeJSON` copies in `providers`, `playback` and `backup` to `httputil`
  - Files: `go-backend/internal/providers/handler.go` (line 366);
    `go-backend/internal/playback/playback.go` (line 2166);
    `go-backend/internal/backup/backup.go` (line 810)
  - Change: Delete the copies and call `httputil`.
  - Done when: ratchet `writeJSON-style copies` goes from 8 to <= 5.

- [x] K4: Move the last `writeJSON` copies in `userapi` and `adminops` to `httputil`
  - Files: `go-backend/internal/userapi/userapi.go` (`writeJSON` line 2511,
    `writeJSONKeys` line 2524); `go-backend/internal/adminops/handler.go`
    (`writeJSON` line 938, `writeJSONStatus` line 942)
  - Change: Delete the copies. Keep the key-ordered variant as an `httputil`
    option, not a separate function name that contains `writeJSON`.
  - Done when: ratchet `writeJSON-style copies` goes from 5 to <= 1.

- [x] K5: Move `sendStatus` into `httputil`
  - Files: `go-backend/internal/providers/handler.go` (line 354),
    `internal/playback/playback.go` (line 2180),
    `internal/podcasts/podcasts.go` (line 1275), `internal/userapi/userapi.go`
    (line 2554), `internal/backup/backup.go` (line 823),
    `internal/sharing/shares.go` (line 20)
  - Change: One `httputil` function reproduces the English status text
    (the express quirk noted in AGENTS.md).
  - Done when: `grep -rln '^func sendStatus(' go-backend/internal --include=*.go`
    is empty.

- [x] K6: Move `sendText` and `sendTextStatus` into `httputil`
  - Files: `go-backend/internal/providers/handler.go` (line 361),
    `internal/userapi/userapi.go` (line 2593), `internal/sharing/shares.go`
    (line 29), `internal/catalog/cover.go` (`sendTextStatus`)
  - Change: One text writer, with Express's charset behavior.
  - Done when: `grep -rln '^func sendText' go-backend/internal --include=*.go`
    is empty.

- [x] K7: Move `writeStatus` into `httputil`
  - Files: `go-backend/internal/catalog/handler.go` (line 756),
    `internal/emailapi/handler.go`, `internal/notifications/handler.go`
  - Change: Replace the three copies with `httputil`.
  - Done when: `grep -rln '^func writeStatus(' go-backend/internal --include=*.go`
    is empty.

- [x] K8: Move `writeError` into `httputil`, keeping the 5xx log line
  - Files: `go-backend/internal/catalog/handler.go` (line 744),
    `internal/providers/handler.go` (line 371), `internal/userapi/userapi.go`
    (line 2501)
  - Change: Errors map to status in one place. Callers log the cause with
    `logging.RequestError` before writing a 5xx.
  - Done when: `grep -rln '^func writeError(' go-backend/internal --include=*.go`
    is empty.

- [x] K9: Keep one `nullString` helper, in `store`
  - Files: `go-backend/internal/catalog/handler.go` (line 874),
    `internal/providers/builtin_search.go` (line 779),
    `internal/store/store.go` (line 331, the one to keep),
    `internal/sharing/sharedevice.go` (line 65)
  - Change: Export the store helper and call it from the other three.
  - Done when: `grep -rln '^func nullString(' go-backend/internal --include=*.go`
    lists only `internal/store/store.go`.

- [x] K10: Keep one nullable-string helper family, in `store`
  - Files: `internal/progressdto/progress.go` (`nullable`, line 83),
    `internal/playback/playback.go` (`nullable` line 2048, `nullableString` line
    1399), `internal/sharing/handler.go` (`nullable` line 170,
    `nullableString` line 176), `internal/podcasts/rss.go` (line 230),
    `internal/userapi/playlist.go` (`nullableStringForDB`, line 339),
    `internal/userapi/userapi.go` (map variant, line 2474),
    `internal/scanner/files.go` (line 697)
  - Change: The variants return different types (`any`, `*string`, `string`).
    Characterize each caller's expected type, then keep one helper per type in
    `store`.
  - Done when: `grep -rln 'func nullable' go-backend/internal --include=*.go`
    lists only `internal/store`.

- [ ] K11: Move time helpers to `dbtime`
  - Files: `parseTime` (`internal/catalog/handler.go:860`,
    `internal/userapi/me_extra.go:473`, `internal/sharing/handler.go:182`);
    `millis` (`internal/progressdto/progress.go:89`,
    `internal/playback/playback.go:2054`, `internal/userapi/userapi.go:2433`);
    `dateMillis` (`internal/catalog/handler.go:838`,
    `internal/progressdto/apply.go:542`); `parseDate` (`internal/progressdto/progress.go:107`,
    `internal/store/store.go:400`)
  - Change: One set in `dbtime`, used by all callers.
  - Done when: `grep -rn '^func \(parseTime\|millis\|dateMillis\|parseDate\)(' go-backend/internal --include=*.go | grep -v internal/dbtime`
    is empty.

- [ ] K12: Route DATETIME `time.Parse` calls through `dbtime.Parse` in `userapi` and `progressdto`
  - Files: `go-backend/internal/userapi/userapi.go` (4 sites),
    `internal/userapi/me_extra.go` (2), `internal/progressdto/apply.go` (4),
    `internal/progressdto/progress.go` (1)
  - Change: Replace the DATETIME layouts with `dbtime.Parse`. Run the DB-content
    differential first, because the stored format is visible to upstream
    (review B5).
  - Done when: `grep -n 'time.Parse(' go-backend/internal/userapi/*.go go-backend/internal/progressdto/*.go`
    returns only non-DATETIME uses, and `make parity` passes.

- [ ] K13: Route the remaining DATETIME `time.Parse` calls through `dbtime.Parse`
  - Files: `go-backend/internal/playback/playback.go` (2), `internal/scanner/files.go`
    (2), `internal/podcasts/rss.go`, `internal/podcasts/item.go`, `internal/sharing/feedsrc.go`
    (2), `internal/sharing/handler.go`, `internal/providers/handler.go`,
    `internal/identity/identity.go`, `internal/store/store.go` (2),
    `internal/store/playbacksession.go` (2), `internal/catalog/writes.go`,
    `internal/catalog/handler.go`
  - Change: As K12.
  - Done when: `grep -rn 'time.Parse(' go-backend/internal --include=*.go | grep -v _test | grep -v internal/dbtime`
    reports only non-DATETIME uses, and `make parity` passes.

- [ ] K14: Create `internal/ids` returning errors; replace the five UUID helpers
  - Files: `newUUID` (`internal/playback/playback.go:2136`,
    `internal/podcasts/podcasts.go:1138`, `internal/userapi/userapi.go:2615`);
    `uuid` (`internal/sharing/handler.go:144`); `randomUUID`
    (`internal/identity/identity.go:1159`)
  - Change: One function that returns an error. The panicking `newUUID` is
    removed, so callers handle the error (review B9).
  - Done when: `grep -rn '^func \(newUUID\|uuid\|randomUUID\)(' go-backend/internal --include=*.go`
    is empty and `go test ./internal/ids` passes.

- [ ] K15: Create `internal/useragent` for the user-agent parser
  - Files: `go-backend/internal/playback/playback.go` (`parseUA`, line 1881),
    `go-backend/internal/userapi/sessions.go` (`parseUA`, line 98)
  - Change: One leaf package with a table test; both callers use it.
  - Done when: `grep -rn '^func parseUA(' go-backend/internal --include=*.go` is
    empty and `go test ./internal/useragent` passes.

- [x] K16: Keep one `levenshtein`
  - Files: `go-backend/internal/providers/builtin_search.go` (line 853, keep),
    `go-backend/internal/podcasts/podcasts.go` (line 628), and the wrappers in
    `go-backend/internal/providers/book_finder.go` (lines 249-251)
  - Change: `podcasts` imports the shared function from a leaf package
    (create `internal/textdist` if the providers package cannot be imported).
  - Done when: `grep -rn '^func levenshtein(' go-backend/internal --include=*.go`
    lists one definition.

- [x] K17: Create `internal/events` and replace the `EmitFunc` types in `providers`, `playback` and `podcasts`
  - Files: `go-backend/internal/providers/handler.go` (line 25),
    `internal/playback/playback.go` (line 36), `internal/podcasts/podcasts.go` (line 37)
  - Change: One `events.Emitter` type. Packages receive it in their deps.
  - Done when: ratchet `EmitFunc-style declarations` goes from 7 to <= 4.

- [x] K18: Replace the `EmitFunc` types in `userapi`, `scanner`, `backup` and `audiotools`
  - Files: `go-backend/internal/userapi/userapi.go` (line 36),
    `internal/scanner/scanner.go` (line 34), `internal/backup/backup.go` (line 33),
    `internal/audiotools/tools.go` (line 29)
  - Change: As K17.
  - Done when: ratchet `EmitFunc-style declarations` goes from 4 to 0.

- [ ] K19: Replace `Set*` injectors in `adminops`, `backup` and `scanner`
  - Files: `go-backend/internal/adminops/handler.go` (3 `Set*` methods, for
    example `SetWatcherUpdate` line 45), `internal/backup/backup.go` (1),
    `internal/scanner/scanner.go` (1)
  - Change: Pass the collaborators through the deps struct on `New`. Test seams
    become constructor options.
  - Done when: ratchet `Set* injector methods` goes from 18 to <= 13.

- [ ] K20: Replace `Set*` injectors in `catalog`, `identity` and `providers`
  - Files: `go-backend/internal/catalog/handler.go` (`SetLibraryChanged` line
    1081, `SetItemChanged` line 1085), `internal/identity/identity.go` (2),
    `internal/providers/handler.go` (1)
  - Change: As K19.
  - Done when: ratchet `Set* injector methods` goes from 13 to <= 8.

- [ ] K21: Replace `Set*` injectors in `podcasts` and `userapi`
  - Files: `go-backend/internal/podcasts/podcasts.go` (`SetQuickMatchHandler` line
    72, `SetHTTPClientForTests` line 1304), `internal/userapi/userapi.go` (2)
  - Change: As K19. The test-only setter becomes a constructor option used by tests.
  - Done when: ratchet `Set* injector methods` goes from 8 to <= 4.

- [ ] K22: Replace `Set*` injectors in `realtime`
  - Files: `go-backend/internal/realtime/hub.go` (4 `Set*` methods, including
    `SetDispatcher` line 137 and `SetSessionLookup`)
  - Change: Pass the dispatcher and lookups through `realtime.New`'s deps.
    `runtime.go` builds them before the hub.
  - Done when: ratchet `Set* injector methods` goes from 4 to 0.

- [ ] K23: Create `internal/authz`, characterize `isAdmin` and `canAccessLibrary`, and consolidate
  - Files: `isAdmin` (`internal/providers/handler.go:115`,
    `internal/realtime/hub.go:677`, `internal/playback/playback.go:1783`,
    `internal/userapi/userapi.go:2223`); `canAccessLibrary`
    (`internal/podcasts/library.go:99`, `internal/userapi/userapi.go:2343`,
    `internal/adminops/handler.go:919`)
  - Change: Write a permission matrix per route against upstream first (task
    C12 covers tags). Consolidate only where the rules are identical.
  - Done when: `grep -rn '^func isAdmin(\|^func canAccessLibrary(' go-backend/internal --include=*.go`
    lists only `internal/authz`, and the matrix test passes.

- [ ] K24: Move JWT issuing into `internal/auth` and delete the `tokenSecret` dead branch (review B7)
  - Files: `go-backend/internal/identity/identity.go` (`sign`, line 682);
    `go-backend/internal/userapi/userapi.go` (`signToken`, line 2056; the
    `settings["tokenSecret"].([]byte)` branch near line 2069 is dead);
    `go-backend/internal/auth/` (verify and issue)
  - Change: `auth` owns signing and verifying. `identity` and `userapi` call it.
    Keep the secret read from settings in one place.
  - Done when: `grep -rln 'SignedString' go-backend/internal --include=*.go` lists
    only `internal/auth`, and `go test ./internal/auth ./internal/identity ./internal/userapi` passes.

- [ ] K25: Use one cron parser (six copies today)
  - Files: `go-backend/internal/catalog/writes.go` (line 134),
    `internal/podcasts/podcasts.go` (lines 961 and 1102), `internal/backup/backup.go`
    (lines 93 and 160), `internal/adminops/handler.go` (line 375)
  - Change: One leaf package (`internal/cronspec`) exposes the parser and
    `Validate`. Callers use it.
  - Done when: `grep -rn 'cron.NewParser(' go-backend/internal --include=*.go | grep -v _test`
    lists one line.

- [ ] K26: Route ffmpeg and ffprobe execution through `internal/media`
  - Files: `go-backend/internal/catalog/batch_chapters.go`,
    `internal/catalog/cover.go`, `internal/playback/playback.go`,
    `internal/podcasts/downloads.go`, `internal/scanner/files.go`,
    `internal/audiotools/tools.go`
  - Change: One runner (path resolution, timeout, stderr policy) in
    `internal/media`. Callers pass arguments only.
  - Done when: `grep -rn 'exec.Command' go-backend/internal --include=*.go | grep -v _test | grep -v internal/media`
    lists no ffmpeg or ffprobe call.

- [ ] K27: Split `catalog/handler.go` (3,317 lines) by route, serializer and query
  - Files: `go-backend/internal/catalog/handler.go`
  - Change: Move code into files named for their concern. No behavior change.
  - Done when: `wc -l go-backend/internal/catalog/handler.go` is under 1000 and
    `make parity` passes.

- [ ] K28: Split `userapi/userapi.go` (2,624 lines) by route family
  - Files: `go-backend/internal/userapi/userapi.go`
  - Change: As K27.
  - Done when: `wc -l go-backend/internal/userapi/userapi.go` is under 1000 and
    `make parity` passes.

- [ ] K29: Split `playback/playback.go` (2,220 lines) into session, stream and serializer files
  - Files: `go-backend/internal/playback/playback.go`
  - Change: As K27.
  - Done when: `wc -l go-backend/internal/playback/playback.go` is under 1000 and
    `make parity` passes.

- [ ] K30: Split `scanner/files.go` (1,829 lines) into metadata, files and matching
  - Files: `go-backend/internal/scanner/files.go`
  - Change: As K27.
  - Done when: `wc -l go-backend/internal/scanner/files.go` is under 1000 and
    `make parity-scan` passes.

- [ ] K31: Register the `sharing`, `notifications` and `emailapi` routes on the mux
  - Files: `go-backend/internal/sharing/handler.go` (`Handle` chain),
    `internal/notifications/handler.go`, `internal/emailapi/handler.go`,
    `internal/server/runtime.go` (legacy chain order)
  - Change: Add `Routes(mux, deps)` to each package and delete its `Handle`
    method. First add smoke cases for each route family: happy path, 401, 403,
    404 and wrong method (ARCHITECTURE.md, "Migration is a strangler").
  - Done when: ratchet `hand-rolled Handle bool routers` goes from 13 to <= 10
    and `make parity` passes.

- [ ] K32: Register the `adminops`, `audiotools`, `backup` and `providers` routes on the mux
  - Files: `go-backend/internal/adminops/handler.go`, `internal/audiotools/tools.go`,
    `internal/backup/backup.go`, `internal/providers/handler.go`
  - Change: As K31.
  - Done when: ratchet `hand-rolled Handle bool routers` goes from 10 to <= 6.

- [ ] K33: Register the `podcasts`, `playback` and `scanner` routes on the mux
  - Files: `go-backend/internal/podcasts/podcasts.go`, `internal/playback/playback.go`,
    `internal/scanner/scanner.go`
  - Change: As K31.
  - Done when: ratchet `hand-rolled Handle bool routers` goes from 6 to <= 3.

- [ ] K34: Register the `identity` and `userapi` routes on the mux
  - Files: `go-backend/internal/identity/identity.go`, `internal/userapi/userapi.go`
  - Change: As K31. Public routes (capability URLs, covers, author images) are
    registered as public, not checked inside the handler.
  - Done when: ratchet `hand-rolled Handle bool routers` goes from 3 to <= 1.

- [ ] K35: Remove the `catalog` `Handle` chain and the legacy catch-all in `runtime.go`
  - Files: `go-backend/internal/catalog/handler.go` (`Handle`),
    `internal/server/runtime.go` (the fallback that dispatches to `Handle`
    chains, around lines 340-390)
  - Change: Once K31-K34 are merged, nothing reaches the legacy chain. Delete it.
    Keep the final 501 fallback for unknown routes: `make route-probe` relies on
    it to detect a route that has no handler. Keep the explicit 404/405
    fallbacks that reproduce Express.
  - Done when: ratchet `hand-rolled Handle bool routers` goes from 1 to 0 and
    `make route-probe` passes.

## Typing (replace `map[string]any` and `[]any` with typed structs in `internal/wire`)

The ratchet counts `map[string]any` (1020 today; its count equals
`grep -ro 'map\[string\]any'` over non-test files). It does not count `[]any`
(313 today); task T0 adds that metric. A pass-through object that is genuinely
opaque stays `json.RawMessage` and is documented as opaque.

- [x] T0: Make the ratchet count `[]any` as well
  - Files: `go-backend/tools/ratchet/main.go`, `go-backend/ratchet-baseline.json`
  - Change: Add a `[]any` metric next to `map[string]any`, and update the
    baseline in the same commit.
  - Done when: `cd go-backend && go run ./tools/ratchet` prints a `[]any` line
    and the baseline has the new key.

- [ ] T1: Capture golden outputs for `ItemJSON` and `ItemMinifiedJSON` for every fixture item
  - Files: `go-backend/internal/catalog/handler.go` (`ItemJSON` line 2662,
    `ItemMinifiedJSON` line 2697); new `go-backend/internal/catalog/item_golden_test.go`
    and a `testdata/` golden
  - Change: Golden output for each fixture item and the edge cases (null
    description, `"null"` ebook column, series without sequence, multi-file
    audio, explicit and abridged). Captured before any type change.
  - Done when: `go test ./internal/catalog -run TestItemGolden` passes on the
    unchanged code.

- [ ] T2: Type `ItemJSON` and `ItemMinifiedJSON` in `wire/book.go`
  - Files: `go-backend/internal/catalog/handler.go` (`ItemJSON`, `ItemMinifiedJSON`),
    `go-backend/internal/catalog/book_wire.go`, `go-backend/internal/wire/book.go`,
    `go-backend/internal/items/reader.go` (interface return types)
  - Change: Return `wire.Book` and `wire.BookMinified`. Keep `audioFiles`,
    `chapters` and `metadata` as typed structs. Use `json.RawMessage` only where
    the column is opaque.
  - Done when: T1 passes unchanged and `grep -n 'map\[string\]any' go-backend/internal/catalog/book_wire.go`
    is empty.

- [ ] T3: Type personalized shelves in `wire/shelves.go`
  - Files: `go-backend/internal/catalog/personalized.go` (28 `map[string]any`),
    `go-backend/internal/catalog/personalized_books.go`, `go-backend/internal/wire/shelves.go`
  - Change: One struct per shelf shape (`continue-listening`, `recently-added`,
    and so on), named for the wire shape.
  - Done when: `grep -c 'map\[string\]any' go-backend/internal/catalog/personalized.go`
    prints 0, and `make parity` and `make parity-replacement` pass.

- [ ] T4: Type the rest of `catalog` (`writes.go`, `batch_chapters.go`, `library_writes.go`, `authors.go`, `handler.go`)
  - Files: `go-backend/internal/catalog/writes.go` (17), `batch_chapters.go` (16),
    `library_writes.go` (14), `authors.go`, `handler.go` (the rest after T2)
  - Change: Request DTOs in `wire/catalog_requests.go`, response DTOs in
    `wire/catalog.go`. Decode with one shared decoder.
  - Done when: `grep -rn 'map\[string\]any' go-backend/internal/catalog --include=*.go | grep -v _test`
    lists only documented opaque uses, and the ratchet total drops.

- [ ] T5: Type the `userapi` profile and sessions responses in `wire/me.go`
  - Files: `go-backend/internal/userapi/userapi.go` (68 `map[string]any`),
    `go-backend/internal/userapi/me_extra.go` (26)
  - Change: Name structs for the wire shapes (`/api/me`, `/api/users`,
    sessions). One request DTO per body.
  - Done when: `grep -c 'map\[string\]any' go-backend/internal/userapi/me_extra.go`
    prints 0 and the ratchet total drops.

- [ ] T6: Type `userapi` playlists and listening in `wire/playlist.go` and `wire/listening.go`
  - Files: `go-backend/internal/userapi/playlist.go` (21 `map[string]any`, 11
    `[]any`), `go-backend/internal/userapi/listening.go` (13)
  - Change: As T5.
  - Done when: `grep -c 'map\[string\]any\|\[\]any' go-backend/internal/userapi/playlist.go go-backend/internal/userapi/listening.go`
    prints 0 (and any opaque use is documented).

- [ ] T7: Type `playback` session payloads in `wire/playback.go`
  - Files: `go-backend/internal/playback/playback.go` (85 `map[string]any`, 15 `[]any`)
  - Change: Name the session, progress and playlist shapes. `mediaMetadata`
    stays `json.RawMessage` (opaque).
  - Done when: `grep -c 'map\[string\]any' go-backend/internal/playback/playback.go`
    prints 0, or lists only documented opaque lines.

- [ ] T8: Type provider search results in `wire/search.go`
  - Files: `go-backend/internal/providers/builtin_search.go` (66 `map[string]any`,
    12 `[]any`), `go-backend/internal/providers/custom_search.go` (12)
  - Change: One struct per provider result shape. The custom provider's arbitrary
    JSON is decoded into a typed struct with the fields upstream reads.
  - Done when: `grep -c 'map\[string\]any' go-backend/internal/providers/builtin_search.go`
    prints 0, and `make parity` passes.

- [ ] T9: Type podcast responses in `wire/podcast.go`
  - Files: `go-backend/internal/podcasts/podcasts.go` (58), `internal/podcasts/item.go`
    (29), `internal/podcasts/downloads.go` (20), `internal/podcasts/rss.go` (12)
  - Change: Name the podcast, episode and download task shapes; the OPML task
    payload gets its own struct.
  - Done when: `grep -rc 'map\[string\]any' go-backend/internal/podcasts --include=*.go | grep -v _test`
    totals 0.

- [ ] T10: Type scanner events and `metadata.json` in `wire/scanner.go`
  - Files: `go-backend/internal/scanner/files.go` (53), `internal/scanner/metadata.go`
    (30), `internal/scanner/scanner.go` (13)
  - Change: The `metadata.json` shape becomes a struct that also fixes C10's
    key order. Scan events use typed payloads.
  - Done when: `grep -rc 'map\[string\]any' go-backend/internal/scanner --include=*.go | grep -v _test`
    totals 0, and `make parity-scan` passes.

- [ ] T11: Type notifications and email in `wire/notifications.go` and `wire/email.go`
  - Files: `go-backend/internal/notifications/handler.go` (42),
    `internal/emailapi/handler.go` (20)
  - Change: As T5.
  - Done when: `grep -c 'map\[string\]any' go-backend/internal/notifications/handler.go go-backend/internal/emailapi/handler.go`
    prints 0 for both.

- [ ] T12: Type admin settings and identity responses in `wire/admin.go` and `wire/identity.go`
  - Files: `go-backend/internal/adminops/handler.go` (35),
    `internal/identity/identity.go` (25), `internal/identity/oidc.go` (15)
  - Change: Settings keys are typed where upstream validates them; unknown keys
    stay in a documented opaque map only if upstream passes them through.
  - Done when: `grep -c 'map\[string\]any' go-backend/internal/adminops/handler.go go-backend/internal/identity/identity.go go-backend/internal/identity/oidc.go`
    prints 0, or lists only documented opaque lines.

- [ ] T13: Type task and realtime payloads in `wire/tasks.go` and `wire/realtime.go`
  - Files: `go-backend/internal/audiotools/tools.go` (25), `go-backend/internal/realtime/hub.go` (24)
  - Change: Task objects get a struct (`isFailed`, `titleSubs` and so on);
    realtime payloads get typed envelopes.
  - Done when: `grep -c 'map\[string\]any' go-backend/internal/audiotools/tools.go go-backend/internal/realtime/hub.go`
    prints 0.

- [ ] T14: Clear the remaining `map[string]any` in `sharing`, `store`, `backup`, `items`, `auth`, `progressdto`, `filterdata`
  - Files: `go-backend/internal/sharing/` (14), `internal/store/store.go` (9),
    `internal/backup/backup.go` (8), `internal/items/` (6), `internal/auth/` (6),
    `internal/progressdto/` (4), `internal/filterdata/` (3)
  - Change: Type what has a shape; document the rest as opaque in one line each.
  - Done when: `grep -rc 'map\[string\]any' go-backend/internal/sharing go-backend/internal/store go-backend/internal/backup go-backend/internal/items go-backend/internal/auth go-backend/internal/progressdto go-backend/internal/filterdata --include=*.go | grep -v _test`
    totals 0, or each remaining line has an `opaque:` comment.

## Performance

- [ ] P1: Read server settings once per request
  - Files: `go-backend/internal/store/store.go` (add a cached read), `internal/adminops/handler.go`
    (settings writes invalidate the cache), and the call sites found by
    `grep -rn 'server-settings\|serverSettings(' go-backend/internal --include=*.go | grep -v _test`
    (about 35 today; `titlePrefix` in loops)
  - Change: One cached load per request; invalidated on any settings write.
  - Done when: `go test ./internal/store -run TestSettingsReadOncePerRequest` passes.

- [ ] P2: Load item pages in a constant number of queries
  - Files: `go-backend/internal/catalog/handler.go` (`ItemJSON` line 2662,
    `ItemMinifiedJSON` line 2697, `orderClause`), `internal/catalog/personalized.go`
  - Change: A page-level loader hydrates N items (book, authors, series, files,
    progress) in a fixed number of queries, used by the library page, shelves,
    collections, playlists and series books.
  - Done when: `go test ./internal/catalog -run TestItemPageQueryCountIndependentOfPageSize`
    passes (the count for 10 and 50 items is equal).

- [ ] P3: Use the batch loader in personalized shelves
  - Files: `go-backend/internal/catalog/personalized.go`, `internal/catalog/personalized_books.go`
  - Change: Replace per-item `ItemJSON` calls with the P2 loader.
  - Done when: `grep -c 'ItemJSON(' go-backend/internal/catalog/personalized.go go-backend/internal/catalog/personalized_books.go`
    prints 0.

- [ ] P4: Allow concurrent reads with one write connection
  - Files: `go-backend/internal/store/store.go` (`SetMaxOpenConns(1)`, line 69),
    `internal/store/open_write.go` (line 35)
  - Change: One write connection and a pool of 4-8 read connections. Stay in
    rollback-journal mode (WAL changes the on-disk format upstream reopens on
    rollback). Audit read-modify-write paths first, each with a `-race` test.
  - Done when: `CGO_ENABLED=1 go test -race ./internal/store` passes and a
    concurrent-read test shows reads do not wait for a write; the replacement
    trial is unchanged.

- [ ] P5: Patch the filter-data cache on tag, genre and author edits
  - Files: `go-backend/internal/filterdata/` (cache), `internal/catalog/writes.go`,
    `internal/catalog/batch.go`
  - Change: Apply the edit to the cached filter data instead of dropping it.
  - Done when: `go test ./internal/catalog -run TestTagEditPatchesFilterData`
    passes and the test counts zero full rebuilds.

- [ ] P6: Re-measure `make bench` and list the calls slower than upstream
  - Files: `go-backend/tools/parity/bench.cjs`; the report it writes
  - Change: Run `make bench` against upstream and Go. The 2026-11-02 snapshot
    had Go ahead on cold start, memory, collapsed series, item detail and play,
    and listed five calls as behind, but its column order is ambiguous. Re-run
    before acting.
  - Done when: this task's entry lists each Tier 1 call that is slower than
    upstream at p95, with the bench report it came from.

## Owner decisions (need the project owner)

- [ ] O1: Decide whether Go runs `ANALYZE` at boot
  - Files: `go-backend/internal/bootstrap/bootstrap.go` (today: no `ANALYZE`);
    `go-backend/cmd/audiobookshelf-go/main.go` (boot order)
  - Change: Decide, then implement if yes. Upstream runs it at boot. Without it, planner-dependent orderings
    (`/api/me` `mediaProgress`, `authorsWithCount` ties) can differ on installs
    whose statistics differ from the fixtures. It is a database write at boot.
  - Done when: the owner adds a decision row (D6) to `ARCHITECTURE.md`'s open
    decisions; if yes, a follow-up task implements it with a test.

- [ ] O2: Decide whether Go ports upstream's boot cleanup (`Database.cleanDatabase`)
  - Files: `go-backend/internal/bootstrap/bootstrap.go` (today: no cleanup);
    upstream `server/Database.js` around lines 680-830
  - Change: Decide, then port if yes. Upstream removes sessions with `timeListening <= 3`, orphaned
    playlist and collection entries, empty series, duplicate media progress and
    invalid library items at every boot. Go skips it, so such rows created while
    Go runs persist until upstream boots again.
  - Done when: the owner adds a decision row (D7) to `ARCHITECTURE.md`; if yes,
    a follow-up task ports it with parity cases for each rule.

- [ ] O3: Confirm D2 (pointers, not `wire.Optional[T]`) before typing null-and-absent fields
  - Files: `go-backend/internal/wire/` (once T2 starts); `go-backend/ARCHITECTURE.md` (D2)
  - Change: Record the decision. Typing tasks T2-T14 assume pointers with and without `omitempty`.
  - Done when: D2 in `ARCHITECTURE.md` is marked confirmed or replaced.

- [ ] O4: Real Android and iOS apps against a swapped-in install
  - Files: none in this repo; result recorded here
  - Change: Run both apps against a swapped-in install. The replacement trial
    replays their API call pattern; the apps themselves are not driven.
  - Done when: the owner records the outcome of a real-device run under this
    task.
