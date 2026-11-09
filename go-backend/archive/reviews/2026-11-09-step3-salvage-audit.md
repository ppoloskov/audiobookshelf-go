# Salvage test-quality pass: audit of removed tests

Baseline 2b61551 (old HEAD) vs the pass (now committed as d833484, tree clean). Method: Test/Benchmark func names per package at
baseline vs now (the `-func` list in salvage-tests-files.txt has diff artifacts: moved tests such as TestEnclosureLengthAcceptsNumberOrString,
and the `tsdiagnostic` files TestTSRepresentationR01..R07 / TestExpressETagMatchesExpress, which were deleted mid-pass but are present in the
committed tree, so they are NOT removed). 60 tests are really gone. I read every baseline body and the covering test.
`go vet ./...` and `go vet -tags tsdiagnostic ./...` are clean.
Legend: COVERED = same assertion elsewhere; WEAKER = covered, named assertion lost; LOST = no replacement.

| Removed test | File | Asserted behaviour | Class | Covering test / what is lost |
|---|---|---|---|---|
| TestContentAllowlistPartialOverlap | access/access_test.go | allow-list admits item sharing a tag, rejects none shared | COVERED | TestTagRule access_test.go:251 (rows "allow list, item shares a tag", "no shared tag") |
| TestCancelEncodeKillsConfiguredFFmpeg | audiotools/tools_test.go | cancel kills ffmpeg, task ends, no output left | COVERED | TestCancelEncodeEndsRunningTask routes_test.go:161 (stronger: 400 on duplicate, 404 on 2nd cancel, task_finished once) |
| TestFFMetadataEscapingAndChapterOrder | audiotools/tools_test.go | ffmetadata escaping of `; # = \`, chapter START/END ms, order | COVERED | TestEmbedQueueRunsOneAtATimeAndWritesMetadata routes_test.go:255-262 (checks the file ffmpeg actually received) |
| TestExportNodeFixtureDatabaseForNodeImport | backup/backup_test.go | Go archive importable (env-gated ABS_BACKUP_*, always skipped; checked with Go's own readBackup) | LOST (low) | nothing in `go test`. Never ran; manual harness tools/parity/test-backup-restore.cjs covers both implementations |
| TestUpstreamCreatedBackupImports | backup/backup_test.go | Node-created archive restores (env-gated, always skipped) | LOST | same; Node->Go archive compatibility has no Go test. REFACTOR-LOG said "vendor a small fixture or remove" |
| TestMissingBackupUsesExpressSendStatus | backup/backup_test.go | 404 on download/apply/delete for unknown id; body "Not Found", text/plain, ETag | WEAKER | TestMissingBackupIs404 backup_test.go:201 keeps the 404s; body/content-type/ETag dropped (framing, ADR-002; httputil.TestSendStatusIsExpressSendStatus covers the helper) |
| TestUploadInvalidExtensionUsesExpressSend | backup/backup_test.go | .zip upload rejected 500 "Invalid backup file" with exact framing | WEAKER | TestUploadInvalidExtensionIsRejected backup_test.go:225 (status + Contains + new "no file left behind"); exact bytes/ETag dropped (framing) |
| TestBookPageQueryCountIndependentOfPageSize | catalog/book_batch_test.go | page contents, ids, ACL, nullable titles, prefixes, AND SQL reads equal for 10/50 | WEAKER | TestBookPageContentsIndependentOfPageSize book_batch_test.go:18 keeps every content assertion; N+1 guard (read-count equality) dropped on purpose; nothing now fails if an N+1 returns |
| TestBookShelfQueryCountIndependentOfSize | catalog/shelf_batch_test.go | shelf ids/order/ACL/other-user progress, duplicate + missing selection errors, read count | WEAKER | TestBookShelfContentsIndependentOfSize shelf_batch_test.go:~20-105 (duplicates, sql.ErrNoRows, other-user progress all kept); read-count equality dropped |
| TestItemBatchQueryCounts | catalog/shelf_batch_test.go | libitem.Load: empty/one/250+dup/251 (chunk boundary)/missing; reads 0/4/4/4/1 | WEAKER | TestItemBatchLoadsAnyNumberOfItems shelf_batch_test.go:133 keeps rows and chunking, adds first-row order; read counts dropped |
| TestIncludePageQueryCountIndependentOfPageSize | catalog/query_count_test.go | `?include=numepisodesincomplete` page of 10/50 returns that many rows, reads equal and <= 8 | LOST | no test requests library items with `include=` other than RSSFEED (handler_test.go:795). The include=numepisodesincomplete path now has no Go test at all |
| TestQueryCounterIncludesPreparedReads | catalog/query_count_test.go | tests the test-only counting driver | COVERED (vacuous) | helper deleted with it; no other user |
| TestLibraryItemsMaterializeBeforeNestedDTOQueries | catalog/handler_test.go | 3 items listed with SetMaxOpenConns(1) (no nested query while rows open) | COVERED | TestLibraryItemsListReturnsEveryItemWithItsMedia handler_test.go (fixture uses 1 conn, :45; asserts total, ids, title/author). Rewritten to a stronger form |
| TestLibraryViewIncludesNullLastScanVersion | catalog/handler_test.go | lastScanVersion/lastScan present as null, then value | COVERED | TestLibraryResponseIncludesNullLastScanVersion, same checks through GET /api/libraries/l1 |
| TestRemoveOldCoversUsesPinnedExtensionAllowlist | catalog/cover_test.go | only old image types removed; svg/foo/other.jpg kept | COVERED | TestUploadCoverRemovesOnlyOlderCoverImages cover_test.go (same 7 files via the route, plus new cover bytes and svg 400) |
| TestShelfEncodesLikeTheMapItReplaced | catalog/shelf_types_test.go | shelf JSON equals old map (entities slice/empty/nil) | COVERED | TestShelfEncodesToTheClientShape (literal expected JSON, same four cases) |
| TestLibitemEquivalence | contracttest/item_columns_test.go | libitem vs old builder over NULL/zero columns | COVERED (mostly) | TestItemJSONReflectsStoredColumns with hand values. Dropped: books `mtime=NULL,size=0`, episode `enclosureURL/enclosureSize/"index"=NULL` (episode column NULLs no longer exercised) |
| TestRequestDecodesPermissionsOnce | contracttest/permission_revocation_test.go | list/item/personalized routes decode permissions once per request (access.Decodes hook) | WEAKER | TestRequestSnapshotIsReusedOnlyForTheSameUserRow access_test.go:305 (AllocsPerRun, stale-doc and other-user cases). The per-route "handlers use the snapshot" integration is gone; perf-only |
| TestNowUsesCanonicalLayout | dbtime/dbtime_test.go | Now() length/`+00:00` suffix/parse | COVERED | Now()=Format(time.Now()); TestFormatUsesUTCAndMillisecondPrecision dbtime_test.go:8 pins layout |
| TestSQLNullBindingStaysNullInDatabase | dbtime/dbtime_test.go | nil binds as SQL NULL; canonical string round-trips | COVERED (vacuous) | exercised only the sqlite driver, no project code |
| TestCanAccessContentTagMatrix | emailapi/handler_test.go | access.Content tag matrix (mirror of algorithm) | COVERED | TestTagRule access_test.go:251 (10 hand-derived rows from User.js:658-669; all/deny/allow/empty branches) |
| TestOpaqueETagSyntax | httputil/text_test.go | tested a helper defined inside the test file | COVERED (vacuous) | helper stays; nothing in production |
| TestNullableDateScanReadsEveryStoredSpelling | identity/nullabledate_test.go | nullableDate.Scan: 4 spellings, nil->invalid, "nope" errors, time.Time passthrough | WEAKER | spellings: dbtime TestParseAcceptsStoredSpellings (parse_test.go:8). LOST: nil->!Valid, error on garbage, time.Time and unsupported-type branches of identity.nullableDate (identity.go:80, used in refresh-token grace) |
| TestParseDateStringReadsEveryStoredSpelling | identity/parsedate_test.go | parseDate (apikeys.go:146) 4 spellings, ""/garbage -> zero | WEAKER | spellings via dbtime parse test. LOST: zone-less `2024-01-01T00:00:00.123` fallback (apikeys.go:150) and zero-time on garbage |
| TestConsoleLineFormat | logging/logging_test.go | exact `[ts] INFO: [Server] hello 42`, stdout/stderr split, ERROR regex | WEAKER | TestConsoleLineCarriesLevelAndMessage logging_test.go:33 keeps stream split and level/message via Contains; exact prefix/timestamp layout dropped (still in daily-file shape test) |
| TestParseLevel | logging/logging_test.go | ParseLevel(warn)/unknown | COVERED (vacuous) | production ParseLevel deleted (dead code) |
| TestApprisePayloadPreservesMissingAndNullTemplates | notifications/handler_test.go | missing template omits title/body; null title -> null; "" body -> "" | COVERED | TestAppriseRequestPreservesMissingAndNullTemplates handler_test.go (creates via API, reads the body a mock Apprise receives; stronger) |
| TestSessionGolden | playback/golden_test.go | self-recorded golden of 3 scenarios (book/podcast/local): responses, rows, events | WEAKER | lifecycle_test.go (open/sync/close, 400/403/404, accumulation, local once-per-id, stale progress, local-all, transcode close) + existing TestPodcastEpisodeTrackFallback..., contracttest play.* journeys. Not re-asserted: devices-table rows from deviceInfo (only TestDeviceExtraData... partial); episode session sync/close writing episode progress; `play/<missing episode>` error; local session keeping client mediaMetadata/coverPath/displayTitle/playMethod; local-all tolerating non-object entries ("junk") and string startedAt/currentTime; stream HLS for episodes |
| TestCloseStopsSchedulerAndCancelsCurrentDownloadIdempotently | podcasts/lifecycle_test.go | Close cancels current download, drops queue, idempotent | COVERED | TestCloseCancelsRunningDownloadAndDropsQueue lifecycle_test.go:306 (real request abandoned, reported failed, queued B never requested, double Close). Scheduler-stop on Close is asserted by neither (old test asserted the opposite) |
| TestDownloadCompletionStatusPollingIsSynchronized | podcasts/downloads_test.go | racy polling during failed download; final Failed/IsFinished/FinishedAt | COVERED | TestFailedDownloadIsReportedAndQueueContinues lifecycle_test.go:261 (4 pollers via EpisodeDownloads, -race, same final fields, plus next download runs). downloads.go gained queueMu in startDownload (real race fix) |
| TestDecodedBodyUnwrapsContentEncoding | podcasts/feed_encoding_test.go | gzip/deflate/raw-deflate/identity | COVERED | TestFetchedFeedIsDecodedByContentEncoding feed_encoding_test.go:21 (same four through a real server and getFeed) |
| TestEpisodeSelectionGolden | podcasts/selection_golden_test.go | self-recorded matchEpisodes/filterNew over feeds + OPML | COVERED | TestFilterNewSelectsUnseenEpisodesAfterCutoff, TestMatchEpisodesRanksByTitleSimilarity, TestParseOPMLSelectsRSSOutlines selection_test.go (hand-derived, cites PodcastManager.js:402-407). Real-feed breadth gone, but it was recorded from the code |
| TestParseOPMLMatchesNamesCaseInsensitively | podcasts/selection_golden_test.go | OPML attr names case-insensitive | COVERED | case "attribute names ignore case" selection_test.go:~35 (identical doc) |
| TestRegisterEpisodeRowGolden | podcasts/register_golden_test.go | self-recorded episode row + item bookkeeping | COVERED | TestRegisterEpisodeStoresRowAndUpdatesItem register_episode_test.go (hand-derived expected per column, audioFile, numEpisodes/size/libraryFiles) |
| TestParseFeedMatchesOldParserGolden | podcasts/rss_golden_test.go | parseFeed == recorded JSON | COVERED | renamed TestParseFeedMatchesPinnedFixtures, same body (still self-recorded; no new independence) |
| TestProviderOutputsGolden | providers/characterisation_test.go | JSON for 20 search routes + finders vs golden | COVERED | TestProviderOutputs (hand-written expected, one subtest per route) and TestFindersReturnProviderMatches. Production changed with it (see below) |
| TestIsoDateConvertsStoredDatetimeToRFC3339 | providers/isodate_test.go | isoDate spellings | COVERED | TestCustomProviderListReportsRFC3339Timestamps via GET /api/custom-metadata-providers; the `""` case dropped (trivial) |
| TestClientDisconnectAndTransportCloseReasons | realtime/sio_test.go | disconnect reasons client/transport | COVERED | TestDisconnectReasons sio_test.go:343 |
| TestEmitOnClosedConnectionIsNoOp | realtime/sio_test.go | Socket.Emit after close returns false, no panic | WEAKER | TestFullBufferClosesConnectionWithoutBlocking sio_test.go:473-497 emits "late" after close (no panic/block). `Emit` return value no longer asserted |
| TestEncodeArrayDoesNotEscapeHTML | realtime/sio_test.go | frame `{"a":"<b>&"}` unescaped; unencodable errors | COVERED | TestEmitEncodesLikeJSONStringify sio_test.go:456 (frame and next event delivered after unencodable payload) |
| TestNewRegistersLegacyAndPrefixedSocketPaths | realtime/hub_test.go | Paths() both, unknown path 404 | COVERED | TestSocketPathsLegacyAndPrefixed hub_test.go (+ connects on prefixed path) |
| TestParsePacket | realtime/sio_test.go | table of packet grammar incl. ack ids, namespaces, binary, malformed | WEAKER | TestPacketGrammar sio_test.go:248, TestAckRoundTrip :296 (ack id 12), TestInvalidNamespace, TestBinaryPacketClosesConnection. Not re-asserted: namespaced event with ack id (`2/x,5[..]`), `0/chat` without comma, `0{"token"}` connect auth payload, `37` client ack. mobile-compat-gaps.md:166 still cites TestParsePacket |
| TestPayloadAccessibleApplies...WhenPresent | realtime/hub_test.go | per-payload ACL by library/tag/explicit/collection | COVERED | TestBroadcastsRespectLibraryTagAndExplicitPermissions hub_test.go (same 10 payload kinds, 3 users, over live sockets) |
| TestPayloadForUserFiltersTypedItemSlices | realtime/hub_test.go | typed []Expanded list filtered | COVERED | same table ("typed item list", "untyped item list") |
| TestUsersOnlineCallsPlaybackLookupOutsideHubLock | realtime/hub_test.go | session lookup not under Hub.mu (no deadlock) | COVERED | TestUsersOnlineListsConnectionsAndToleratesReentrantLookups (lookup calls Hub.Refresh = write lock; 2s deadlock guard) |
| TestBenchLibraryOldAndNewAgree | scanner/bench_match_test.go | new matcher == copy of old quadratic matcher | COVERED | TestBenchLibraryPairsEachItemWithItsOwnGroup (hand expectation for 2000 items, deleted items unmatched, none unclaimed) |
| TestGroupMatcherWorkIsLinear | scanner/matcher_test.go | visit counter <= 4/item for 50/400 | WEAKER | TestGroupMatcherScalesLinearly matcher_test.go (wall-clock ratio < 40x for 10x, best of 5, skipped under -short). Deterministic count replaced by timing; flake risk, silently skipped in -short |
| TestWatcherDiscoversNewNestedFoldersAndCloseStopsIt | scanner/scanner_test.go | nested new folder imported; Close stops watcher | COVERED | TestWatcherImportsNewNestedFolders + TestCloseStopsWatcherBatchesAndGoroutines scanner/lifecycle_test.go (Close drops pending batch, goroutines exit); also adds Cancel tests. Caveat: goroutine check uses runtime.NumGoroutine() <= before (can flake if other tests leak) |
| TestShareRejectsExpiredAndDownloadDisabled | sharing/handler_test.go | share with past expiresAt -> public 404 | COVERED | TestExpiredShareIsRefusedRemovedAndItsSessionsClosed (expiry after open: refused, row removed, share_closed once, old cookie dead) + TestShareWithoutDownloadFlagRefusesDownload (403). Creating a share already expired (expiresAt:1) no longer tested |
| TestDecodeJSONObject | store/json_test.go | empty/null/ws-null/malformed -> empty non-nil mutable; valid object | COVERED | TestDecodeJSONSliceAndObject json_test.go (same inputs, plus mutability) |
| TestDecodeJSONSlice | store/json_test.go | same for slices AND partial decode `["one",2,"three"]` -> ["one","","three"] | WEAKER | partial-decode case lost. json.go documents "retaining any values decoded before a type error"; now untested |
| TestDecodeJSONSliceMaps | store/json_test.go | `[{..},4,{..}]` keeps both objects, nil for the number | LOST | same documented partial-decode contract, no replacement. Used for libraryFiles/audioFiles columns (catalog/writes.go:247,273) |
| TestNullIfBlank / TestNullInt64 / TestNullStringMapsInvalidToNil / TestNullIfEmptyMapsEmptyToNil | store/nullable*_test.go | NULL vs "" converters | COVERED | TestNullableColumnsKeepNullDistinctFromEmpty nullable_test.go (every branch; only `NullIfEmpty(" ")==" "` and NullString("x") dropped) |
| TestNullStringPtrAndStringPtrValue | store/json_test.go | NullStringPtr + StringPtrValue(nil)->nil, (&"x")->"x" | WEAKER | NullStringPtr kept; StringPtrValue (production: catalog/authors.go:352,357 and item_media.go:132, nil must reach SQL as NULL) has no test now |
| TestSanitizeDifferences / TestStripDifferences | textclean/golden_test.go | documented sanitize/strip deviations | COVERED | renamed TestSanitizeMatchesUpstream.. / TestStripMatchesUpstream.. with identical rows (old column dropped) |
| TestPlaylistNameOrder | textsort/textsort_test.go | 15-name order pinned from Node `localeCompare` | COVERED | TestMatchesNode (12,544 Node-recorded pairs incl. `_` vs `-`, digits, Éclair/eclair, case). Exact `á`/`Á` pair and the full sort sequence not repeated; the pair table is stronger |

Not removed, but rewritten with fewer assertions (checked): TestCreateNotifiesBackupLifecycle (18->12 assertions), TestBookItemSerializersMatchPinnedSourceGolden
(dual-run vs deleted legacy builders removed, TS golden kept), scanner tests that used real ffmpeg/ffprobe now use a fake ffprobe (they no longer skip;
no test now feeds a real ffprobe's output to the parser), TestNewReturnsVersion4UUIDs (1000 uniqueness check -> 50 samples, uniqueness dropped).

## Must restore (LOST/WEAKER and contractual or lifecycle)
1. backup `TestCreateNotifiesBackupLifecycle`: assert the full onBackupCompleted payload again: `completionTime`, `backupSize`, `backupCount`,
   `removedOldest` ("false", or true after rotation; backup.go:252). These are the Apprise template variables (notifications/handler.go:448); only
   backupPath/backupCount are checked now. onBackupFailed payload is still checked.
2. store: restore partial-decode assertions (`["one",2,"three"]`, `[{..},4,{..}]`) for DecodeJSONSlice, and `StringPtrValue(nil)==nil` / unwrap.
3. catalog: a test for library items `?include=numepisodesincomplete` (the only `include=` list path with no test now); optionally an N+1 guard that does
   not rely on a hook (e.g. count queries in a wrapper driver in the test file only, or bound the call count for 10 vs 50 rows).
4. identity: nullableDate.Scan (nil -> invalid, garbage -> error, time.Time) and parseDate zone-less fallback / garbage -> zero. Cheap table tests.
5. playback: add back local-all with a non-object entry and string-typed numbers, client-supplied metadata kept by `/api/session/local`, `play/<unknown
   episode>` error, and an episode session's sync/close writing episode progress (the golden was the only place for these).
6. backup Node-archive import: either vendor a small upstream-created archive and run it unconditionally, or record that tools/parity/test-backup-restore.cjs
   is the only coverage (currently not in CI).
7. realtime: namespaced packet with ack id and `0/chat` without comma (parser table) if the parser is touched; `Socket.Emit` returns false after close.
8. scanner TestGroupMatcherScalesLinearly is wall-clock based and skipped in -short; prefer a deterministic bound (e.g. benchmark with allocs, or an
   assertion on iterations through an exported-for-test counter in export_test.go).

Lower priority: exact HTTP framing assertions for backup 404/upload (ADR-002 says not contract, accepted).

## Production code changed alongside the tests (outside "tests only"; confirm intent)
- providers/builtin_search.go: removed `if m.Rating == "" { m.Rating = m.Region }`. TS Audible.js:85 is `rating: item.rating || null`, so this is a correct
  fix, and the new TestProviderOutputs expects no rating for the second match. It was previously baked into the golden.
- filterdata: empty-string publisher/language no longer listed (libraryFilters.js falsy rule); covered by new filterdata_test.go.
- playback/hls.go: `closed` flag so a pending seek restart cannot start ffmpeg after close (covered by lifecycle tests, not by a race test of the seek path).
- podcasts/downloads.go: startDownload now takes queueMu (race fix). access: Decodes() hook removed. realtime: connectFn removed.
  scanner matcher: visits removed. providers.SetEndpoint moved to export_test.go. Still present: podcasts.SetHTTPClientForTests (runtime_test.go:63).

## Dangling references
- `tsdiagnostic` lane: all 7 `*_representation_diagnostic_test.go` / `ts_representation_diagnostic_test.go` files are in the committed tree and compile. Running the
  Makefile `test-ts-diagnostics` target now: `TestTSRepresentationR03` FAILS (httputil/ts_representation_diagnostic_test.go:30, body lacks trailing
  "\n"). This is pre-existing (httputil production code and that test are unchanged vs 2b61551); workflow marks diagnostics advisory. Everything else in the
  lane passes. No test in that lane was removed by the pass.
- `node --test go-backend/tools/test-assertion-manifest.cjs` (the CI step in .github/workflows/go-backend.yml:41) passes (2 pass, 1 skip): contracts/test-assertions.json
  only names `textclean/golden_test.go#...` symbols, all still present.
- contracts/test-assertion-results.json lines 33, 37, 105, 109, 235, 239, 325, 329: list TestUpstreamCreatedBackupImports and
  TestExportNodeFixtureDatabaseForNodeImport as "unexecuted"; these tests no longer exist. Historical record, not validated by any check, but stale.
- go-backend/TEST-COMPATIBILITY-IMPLEMENTATION.md:104-105 (same two tests), TEST-COMPATIBILITY-REVIEW.md:77 (TestPlaylistNameOrder), contracts/mobile-compat-gaps.md:166
  (TestParsePacket cited as "existing" coverage; now removed), docs/SIMPLIFICATION.md:271 (TestRequestDecodesPermissionsOnce) cite removed tests.
- docs/agent-work/capabilities/cap-route-r165.json:32 (and copies under .claude/worktrees) cite notifications TestApprisePayloadPreservesMissingAndNullTemplates (renamed to
  TestAppriseRequestPreservesMissingAndNullTemplates). Not machine-checked by CI, but stale.
- No references in Makefile, tools/, or other .github workflows to any deleted file (golden_test.go for playback/podcasts, session_golden.json, register/selection goldens,
  provider_matches.golden.json, nullable2_test.go, identity/*date_test.go, query_count_test.go). podcasts testdata/feeds/many.xml (used only by the deleted selection golden) is probably an unused fixture now.
