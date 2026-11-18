// Package store exercises persisted state across server restarts.
package store

import (
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"
	"time"

	"audiobookshelf-compat/testmachine/scenarios/harness"
)

// Packet returns the store scenarios.
func Packet() harness.Packet {
	return harness.Packet{Name: "store", NoServer: true, Scenarios: []harness.Scenario{
		// Reads and boot checks first, then non-destructive writes, then the
		// scenarios that stop or kill their server last.
		{Requirement: "sub-database-schema", Invariant: "INV-SUB-DB-BOOT-001", Case: "normal", State: "repeat", Assertion: "existing-install-repeat-boot", Sources: []string{"server/Database.js:176-200", "server/Database.js:239-344"}, Claim: "Repeated boot preserves the existing schema and selected fixture records.", Run: repeatBoot},
		{Requirement: "sub-database-schema", Invariant: "INV-SUB-DB-FRESH-ROOT-001", Case: "normal", State: "initial", Assertion: "fresh-root-upstream-reopen", Sources: []string{"server/Database.js:176-200", "server/Database.js:239-344", "server/Server.js:377-405"}, Claim: "A fresh installation initializes its current schema and root and preserves their identities on pinned upstream reopen.", Run: freshRoot},
		{Requirement: "sub-database-schema", Invariant: "INV-SUB-DB-BOOT-001", Case: "edge", State: "initial", Assertion: "boot-missing-settings-row", Sources: []string{"server/Database.js:365-374", "server/auth/TokenManager.js:42-58", "server/models/Setting.js:36-42"}, Claim: "A boot without the server-settings row recreates it from the defaults with the generated token secret and keeps login working.", Run: bootMissingSettingsRow},
		{Requirement: "sub-database-schema", Invariant: "INV-SUB-DB-BOOT-001", Case: "error", State: "failure", Assertion: "boot-corrupt-refused", Sources: []string{"server/Database.js:186-206", "server/Database.js:236-275"}, Claim: "A boot that cannot open the database exits with status 1 and leaves the file and no write-ahead sidecars behind.", Run: bootCorruptRefused},
		{Requirement: "sub-database-schema", Invariant: "INV-F07-REOPEN-STATE-001", Case: "normal", State: "changed", Assertion: "bookmark-upstream-reopen", Sources: []string{"server/controllers/MeController.js:322-352", "server/models/User.js:841-912", "server/controllers/MeController.js:234-246", "server/controllers/MeController.js:269-287", "server/models/User.js:730-831"}, Claim: "Bookmark and progress mutations survive a stop and pinned upstream reopen with their identities and stored values intact.", Run: bookmarkReopen},
		{Requirement: "sub-database-schema", Invariant: "INV-SUB-DB-LOCAL-SESSION-001", Case: "normal", State: "changed", Assertion: "local-session-upstream-reopen", Sources: []string{"server/routers/ApiRouter.js:238", "server/managers/PlaybackSessionManager.js:127-250", "server/controllers/MeController.js:150-175"}, Claim: "A mobile local listening session remains in storage and history after pinned upstream reopens the installation.", Run: localSession},
		{Requirement: "persisted-column-semantics", Invariant: "INV-PERSISTED-CATALOG-TITLE-001", Case: "normal", State: "changed", Assertion: "catalog-title-upstream-reopen", Sources: []string{"server/routers/ApiRouter.js:108-111", "server/controllers/LibraryItemController.js:208-285", "server/models/Book.js:370-431", "server/models/LibraryItem.js:612-700", "server/Database.js:854-882"}, Claim: "A catalog title update persists in its book, derived item, and metadata file and remains readable after pinned upstream reopens the installation.", Run: catalogReopen},
		{Requirement: "persisted-column-semantics", Invariant: "INV-PERSISTED-COLUMN-SEMANTICS", Case: "normal", State: "initial", Assertion: "column-write-encodings", Sources: []string{"server/models/User.js:730-826", "server/models/User.js:853-876", "server/models/MediaProgress.js:155-176"}, Claim: "The first progress and bookmark writes persist pinned UTC date spellings, TEXT JSON columns with JSON milliseconds and nullable boolean integers, and read back as JSON milliseconds and nulls.", Run: columnWriteEncodings},
		{Requirement: "persisted-column-semantics", Invariant: "INV-PERSISTED-COLUMN-SEMANTICS", Case: "normal", State: "repeat", Assertion: "column-repeat-finish-unchanged", Sources: []string{"server/models/MediaProgress.js:190-245", "server/models/MediaProgress.js:155-176"}, Claim: "Repeating the finish request on an already finished progress keeps finishedAt and createdAt while the save moves updatedAt forward.", Run: columnRepeatFinish},
		{Requirement: "persisted-column-semantics", Invariant: "INV-PERSISTED-COLUMN-SEMANTICS", Case: "edge", State: "initial", Assertion: "column-link-author-order", Sources: []string{"server/models/Book.js:440-487", "server/Database.js:903-945", "server/models/Author.js:113-125"}, Claim: "The bookAuthors link rows persist in creation order, the trigger-maintained authorNames columns follow that order, and an unlinked author row is kept while other books still link it.", Run: columnLinkAuthorOrder},
		{Requirement: "persisted-column-semantics", Invariant: "INV-PERSISTED-COLUMN-SEMANTICS", Case: "edge", State: "changed", Assertion: "column-json-unknown-retained", Sources: []string{"server/models/User.js:920-930", "server/controllers/MeController.js:532-546", "server/models/User.js:602-622"}, Claim: "Unknown JSON fields in a users JSON column survive boot and are still present after a later write rewrites the column.", Run: columnJSONUnknownRetained},
		{Requirement: "persisted-column-semantics", Invariant: "INV-PERSISTED-COLUMN-SEMANTICS", Case: "error", State: "initial", Assertion: "column-rejected-write-unchanged", Sources: []string{"server/controllers/MeController.js:327-352", "server/models/User.js:853-876"}, Claim: "Rejected bookmark writes answer 400 and change no column of the users row.", Run: columnRejectedWrite},
		{Requirement: "persisted-column-semantics", Invariant: "INV-PERSISTED-COLUMN-SEMANTICS", Case: "error", State: "failure", Assertion: "column-failed-write-journal", Sources: []string{"server/models/User.js:853-876", "server/controllers/MeController.js:327-352", "server/Server.js:269-292"}, Claim: "A database write failure during bookmark creation exits the server with status 1, leaves the users row untouched, and the installation loads the original bookmarks after the failure trigger is removed and reopened.", Run: columnFailedWriteJournal},
		{Requirement: "cap-flow-settings-atomicity", Invariant: "INV-CAP-FLOW-SETTINGS-ATOMICITY", Case: "normal", State: "initial", Assertion: "settings-patch-initial", Sources: []string{"server/controllers/MiscController.js:139-175", "server/objects/settings/ServerSettings.js:365-381", "server/models/Setting.js:45-48"}, Claim: "The first settings PATCH merges into the document, moves updatedAt strictly forward, keeps createdAt and never surfaces the token secret.", Run: settingsPatchInitial},
		{Requirement: "cap-flow-settings-atomicity", Invariant: "INV-CAP-FLOW-SETTINGS-ATOMICITY", Case: "normal", State: "repeat", Assertion: "settings-patch-repeat", Sources: []string{"server/objects/settings/ServerSettings.js:365-381", "server/controllers/MiscController.js:152-171"}, Claim: "A repeated identical patch and a patch with only non-patchable keys answer 200 without writing the settings row.", Run: settingsPatchRepeat},
		{Requirement: "cap-flow-settings-atomicity", Invariant: "INV-CAP-FLOW-SETTINGS-ATOMICITY", Case: "edge", State: "changed", Assertion: "settings-concurrent-merge", Sources: []string{"server/controllers/MiscController.js:139-171", "server/objects/settings/ServerSettings.js:365-381"}, Claim: "Two writers racing disjoint settings patches both persist: the stored document and the served settings contain both changes.", Run: settingsConcurrentMerge},
		{Requirement: "cap-flow-settings-atomicity", Invariant: "INV-SETTINGS-SEQUENTIAL-REOPEN-001", Case: "normal", State: "changed", Assertion: "settings-merge-upstream-reopen", Sources: []string{"server/controllers/MiscController.js:139-171", "server/objects/settings/ServerSettings.js:1-200"}, Claim: "Disjoint settings writes merge and survive pinned upstream reopen.", Run: settingsReopen},
		{Requirement: "cap-flow-settings-atomicity", Invariant: "INV-SETTINGS-REJECTION-001", Case: "error", State: "failure", Assertion: "settings-invalid-unchanged", Sources: []string{"server/controllers/MiscController.js:139-165"}, Claim: "Invalid allowedOrigins and non-admin updates are rejected without changing persisted or surfaced settings.", Run: settingsRejection},
		{Requirement: "cap-flow-settings-atomicity", Invariant: "INV-CAP-FLOW-SETTINGS-ATOMICITY", Case: "error", State: "initial", Assertion: "settings-iframe-env-rejected", Sources: []string{"server/controllers/MiscController.js:148-151", "server/objects/settings/ServerSettings.js:365-381"}, Claim: "With ALLOW_IFRAME enabled in the environment, a patch disabling iframe embedding is rejected before any write.", Run: settingsIframeEnv},
		{Requirement: "cap-flow-settings-atomicity", Invariant: "INV-SETTINGS-FAILURE-001", Case: "error", State: "failure", Assertion: "settings-failure-trigger", Sources: []string{"server/controllers/MiscController.js:163-165", "server/models/Setting.js:36-42", "server/Server.js:269-292"}, Claim: "A settings upsert that fails in the database exits the server with status 1, leaves the persisted document and timestamp intact, and the installation loads the original settings and accepts writes after the failure trigger is removed and reopened.", Run: settingsFailureTrigger},
	}}
}

func repeatBoot(t *testing.T, _ *harness.Env) {
	e := harness.Start(t, harness.Options{})
	schema := e.Rows(t, "SELECT type, name, tbl_name, sql FROM sqlite_master WHERE name NOT LIKE 'sqlite_%' ORDER BY type, name")
	assertSchemaNames(t, e)
	file := filepath.Join(e.Media, "audiobooks/Terry Fixture/Long Saga/Book 2 - The Middle/reader.txt")
	contents := readFile(t, file)
	if string(contents) != "Nora Narrator" {
		t.Fatalf("fixture reader: %q", contents)
	}
	metadataFile := filepath.Join(e.Metadata, "items", e.IDs["saga1"], "metadata.json")
	metadata := readFile(t, metadataFile)
	if len(metadata) == 0 {
		t.Fatal("fixture metadata is empty")
	}
	rows := e.Rows(t, "SELECT id, username, bookmarks FROM users WHERE id = ?", e.IDs["listener"])
	if len(schema) == 0 || len(rows) != 1 || rows[0]["username"] != "trial-listener" {
		t.Fatalf("fixture schema or listener missing: %v", rows)
	}
	e.Stop(t)
	reopened := harness.Start(t, harness.Options{Install: e.Install, Upstream: true})
	if data := readFile(t, file); string(data) != "Nora Narrator" {
		t.Fatalf("reader after boot: %q", data)
	}
	if data := readFile(t, metadataFile); !reflect.DeepEqual(data, metadata) {
		t.Fatal("metadata changed on boot")
	}
	if got := reopened.Rows(t, "SELECT type, name, tbl_name, sql FROM sqlite_master WHERE name NOT LIKE 'sqlite_%' ORDER BY type, name"); !reflect.DeepEqual(got, schema) {
		t.Fatal("schema changed on repeat boot")
	}
	if got := reopened.Rows(t, "SELECT id, username, bookmarks FROM users WHERE id = ?", e.IDs["listener"]); !reflect.DeepEqual(got, rows) {
		t.Fatalf("listener changed: %v != %v", got, rows)
	}
}

type bookmark struct {
	LibraryItemID string  `json:"libraryItemId"`
	Time          float64 `json:"time"`
	Title         string  `json:"title"`
	CreatedAt     int64   `json:"createdAt"`
}

func bookmarkReopen(t *testing.T, _ *harness.Env) {
	e := harness.Start(t, harness.Options{})
	token := e.Token(t, "listener")
	start := time.Now().UnixMilli()
	resp := e.Post(t, token, "/api/me/item/"+e.IDs["saga1"]+"/bookmark", map[string]any{"time": 42.5, "title": "Reopen Ω"})
	if resp.Status != http.StatusOK {
		t.Fatalf("bookmark status %d", resp.Status)
	}
	var b bookmark
	resp.JSON(t, &b)
	if b.LibraryItemID != e.IDs["saga1"] || b.Time != 42.5 || b.Title != "Reopen Ω" || b.CreatedAt < start || b.CreatedAt > time.Now().UnixMilli() {
		t.Fatalf("bookmark: %+v", b)
	}
	progress := e.Patch(t, token, "/api/me/progress/"+e.IDs["saga1"], map[string]any{"currentTime": 12, "duration": 60, "progress": 0.2})
	if progress.Status != http.StatusOK {
		t.Fatalf("progress status %d", progress.Status)
	}
	progressQuery := "SELECT id, userId, mediaItemId, mediaItemType, duration, currentTime, isFinished, finishedAt, createdAt, updatedAt FROM mediaProgresses WHERE userId = ? AND mediaItemId = ?"
	progressArgs := []any{e.IDs["listener"], "5e33de11-3e94-4c51-87d8-d06d2928ac38"}
	progressRows := e.Rows(t, progressQuery, progressArgs...)
	if len(progressRows) != 1 || progressRows[0]["currentTime"] != float64(12) || progressRows[0]["duration"] != float64(60) || progressRows[0]["mediaItemType"] != "book" || progressRows[0]["id"] != "b95b3028-3461-4306-803b-6e310140cd22" || progressRows[0]["finishedAt"] != nil || progressRows[0]["isFinished"] != int64(0) {
		t.Fatalf("progress row: %v", progressRows)
	}
	for _, key := range []string{"createdAt", "updatedAt"} {
		value, ok := progressRows[0][key].(string)
		if !ok || !regexp.MustCompile(`^\d{4}-\d{2}-\d{2} \d{2}:\d{2}:\d{2}\.\d{3} \+00:00$`).MatchString(value) {
			t.Fatalf("%s format: %v", key, progressRows[0][key])
		}
	}
	before := e.Rows(t, "SELECT bookmarks, typeof(bookmarks) AS kind FROM users WHERE id = ?", e.IDs["listener"])
	assertStored(t, before, b)
	e.Stop(t)
	reopened := harness.Start(t, harness.Options{Install: e.Install, Upstream: true})
	if got := reopened.Rows(t, progressQuery, progressArgs...); !reflect.DeepEqual(got, progressRows) {
		t.Fatalf("progress changed: %v != %v", got, progressRows)
	}
	after := reopened.Rows(t, "SELECT bookmarks, typeof(bookmarks) AS kind FROM users WHERE id = ?", e.IDs["listener"])
	if !reflect.DeepEqual(after, before) {
		t.Fatalf("bookmark storage changed: %v != %v", after, before)
	}
	token = reopened.Token(t, "listener")
	progressResponse := reopened.Get(t, token, "/api/me/progress/"+e.IDs["saga1"])
	if progressResponse.Status != http.StatusOK {
		t.Fatalf("progress GET status %d", progressResponse.Status)
	}
	var progressBody struct {
		ID            string  `json:"id"`
		LibraryItemID string  `json:"libraryItemId"`
		CurrentTime   float64 `json:"currentTime"`
		Duration      float64 `json:"duration"`
		FinishedAt    *int64  `json:"finishedAt"`
		IsFinished    bool    `json:"isFinished"`
	}
	progressResponse.JSON(t, &progressBody)
	if progressBody.ID != "b95b3028-3461-4306-803b-6e310140cd22" || progressBody.LibraryItemID != e.IDs["saga1"] || progressBody.CurrentTime != 12 || progressBody.Duration != 60 || progressBody.IsFinished || progressBody.FinishedAt != nil {
		t.Fatalf("progress GET: %+v", progressBody)
	}
	got := reopened.Get(t, token, "/api/me/bookmarks/"+e.IDs["saga1"])
	if got.Status != http.StatusOK {
		t.Fatalf("bookmarks status %d", got.Status)
	}
	var body struct {
		Bookmarks []bookmark `json:"bookmarks"`
	}
	got.JSON(t, &body)
	found := false
	for _, v := range body.Bookmarks {
		if v == b {
			found = true
		}
	}
	if !found {
		t.Fatalf("bookmark absent after reopen: %+v", body)
	}
	if r := reopened.Get(t, "", "/api/me/bookmarks/"+e.IDs["saga1"]); r.Status != http.StatusUnauthorized {
		t.Fatalf("unauthenticated status %d", r.Status)
	}
}
func assertStored(t *testing.T, rows []map[string]any, want bookmark) {
	t.Helper()
	if len(rows) != 1 || rows[0]["kind"] != "text" {
		t.Fatalf("bookmark column: %v", rows)
	}
	raw, ok := rows[0]["bookmarks"].(string)
	if !ok {
		t.Fatalf("bookmark text: %v", rows)
	}
	var values []bookmark
	if err := json.Unmarshal([]byte(raw), &values); err != nil {
		t.Fatal(err)
	}
	for _, v := range values {
		if v == want {
			return
		}
	}
	t.Fatalf("stored bookmark absent: %+v", values)
}

// readFile bounds fixture and metadata reads, including growth after Stat.
func readFile(t *testing.T, path string) []byte {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close() // Read-only fixture handle. All file reads are checked below.
	info, err := f.Stat()
	if err != nil {
		t.Fatal(err)
	}
	const limit = 1 << 20
	if info.Size() > limit {
		t.Fatalf("file exceeds %d bytes: %s", limit, path)
	}
	data, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil {
		t.Fatal(err)
	}
	if len(data) > limit {
		t.Fatalf("file grew beyond limit: %s", path)
	}
	return data
}

func assertSchemaNames(t *testing.T, e *harness.Env) {
	t.Helper()
	names := e.Rows(t, "SELECT type || ':' || name AS entry FROM sqlite_master WHERE type IN ('table','trigger') AND name NOT LIKE 'sqlite_%' ORDER BY type,name")
	want := strings.Fields(`
		table:apiKeys table:authors table:bookAuthors table:bookSeries table:books
		table:collectionBooks table:collections table:customMetadataProviders table:devices
		table:feedEpisodes table:feeds table:libraries table:libraryFolders table:libraryItems
		table:mediaItemShares table:mediaProgresses table:migrationsMeta table:playbackSessions
		table:playlistMediaItems table:playlists table:podcastEpisodes table:podcasts
		table:series table:sessions table:settings table:users
		trigger:update_library_items_author_names_on_authors_update
		trigger:update_library_items_author_names_on_book_authors_delete
		trigger:update_library_items_author_names_on_book_authors_insert
		trigger:update_library_items_title
		trigger:update_library_items_title_from_podcasts_title
		trigger:update_library_items_title_ignore_prefix
		trigger:update_library_items_title_ignore_prefix_from_podcasts_title_ignore_prefix
	`)
	var gotNames []string
	for _, row := range names {
		name, ok := row["entry"].(string)
		if !ok {
			t.Fatalf("schema entry is not text: %v", row)
		}
		gotNames = append(gotNames, name)
	}
	if !reflect.DeepEqual(gotNames, want) {
		t.Fatalf("schema identities: %v", gotNames)
	}
}
