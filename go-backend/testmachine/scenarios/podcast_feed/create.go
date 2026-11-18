package podcast_feed

import (
	"bytes"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"audiobookshelf-compat/testmachine/scenarios/harness"
)

const (
	createReq       = "route-r149"
	createBodyInv   = "INV-R149-BODY-001"
	createPersInv   = "INV-R149-PERSIST-001"
	createAuthInv   = "INV-R149-SAMPLED"
	createDuplicate = "INV-R149-DUPLICATE-001"
	createSrc       = "server/controllers/PodcastController.js:40-190"
	createModelSrc  = "server/models/Podcast.js:79-125"
	createItemSrc   = "server/models/LibraryItem.js:643-700"
)

// createBody builds a create payload from the parts the scenarios vary.
func createBody(libraryID, folderID, path string, media map[string]any) map[string]any {
	return map[string]any{"libraryId": libraryID, "folderId": folderID, "path": path, "media": media}
}

// metadataOnly wraps metadata in a media object.
func metadataOnly(metadata map[string]any) map[string]any {
	return map[string]any{"metadata": metadata}
}

// itemEventMatcher matches an item event of the named title without failing on
// an unrelated event.
func itemEventMatcher(title string) func([]json.RawMessage) bool {
	return func(args []json.RawMessage) bool {
		if len(args) == 0 {
			return false
		}
		var item struct {
			Media struct {
				Metadata struct {
					Title string `json:"title"`
				} `json:"metadata"`
			} `json:"media"`
		}
		if err := json.Unmarshal(args[0], &item); err != nil {
			return false
		}
		return item.Media.Metadata.Title == title
	}
}

func createScenarios() []harness.Scenario {
	return []harness.Scenario{
		{
			Requirement: createReq, Invariant: createBodyInv, Case: "normal", State: "initial", Assertion: "create-podcast-stores-metadata-cover-and-schedule",
			Sources: []string{createSrc, createModelSrc, "server/managers/CoverManager.js:300-345"},
			Claim:   "POST /api/podcasts creates the podcast folder and a podcast library item with the posted metadata, sanitized description, tags, genres, auto-download flag and schedule, downloads the cover image into the item folder, emits item_added to the admin, the listener and the user without explicit access (the podcast is not explicit), and answers the expanded library item.",
			Run: func(t *testing.T, e *harness.Env) {
				stub := newFeedStub(t)
				image := jpegBytes(t)
				stub.setBytes("/cover.jpg", "image/jpeg", image)
				folderID, folderPath := libraryFolder(t, e, e.IDs["podcastLibrary"])
				root := e.Socket(t, e.Token(t, "root"))
				listener := e.Socket(t, e.Token(t, "listener"))
				finisher := e.Socket(t, e.Token(t, "finisher"))
				rootMark, listenerMark, finisherMark := root.Mark(), listener.Mark(), finisher.Mark()
				started := time.Now().UnixMilli()
				resp, item := createPodcast(t, e, e.Token(t, "root"), signalPodcastBody(e, folderID, folderPath, stub.url("/signal.xml"), stub.url("/cover.jpg")))
				wantStatus(t, "create", resp, http.StatusOK)
				wantPath := filepath.Join(folderPath, "Signal Show")
				if item.ID == "" || item.Media.ID == "" {
					t.Fatalf("ids missing: %+v", item)
				}
				if item.LibraryID != e.IDs["podcastLibrary"] || item.FolderID != folderID || item.MediaType != "podcast" || item.IsFile {
					t.Fatalf("item identity wrong: %+v", item)
				}
				if item.Path != wantPath || item.RelPath != "Signal Show" {
					t.Fatalf("path = %q relPath = %q, want %q", item.Path, item.RelPath, wantPath)
				}
				if item.AddedAt < started || item.UpdatedAt < item.AddedAt {
					t.Fatalf("addedAt %d updatedAt %d, start %d", item.AddedAt, item.UpdatedAt, started)
				}
				metadata := item.Media.Metadata
				requireValue(t, "title", metadata.Title, "Signal Show")
				requireValue(t, "author", metadata.Author, "Signal Author")
				requireValue(t, "description", metadata.Description, "<p>Signal <b>description</b></p>")
				requireValue(t, "releaseDate", metadata.ReleaseDate, "2025-01-01")
				requireValue(t, "feedUrl", metadata.FeedURL, stub.url("/signal.xml"))
				requireValue(t, "imageUrl", metadata.ImageURL, stub.url("/cover.jpg"))
				requireValue(t, "itunesPageUrl", metadata.ItunesPageURL, "https://example.invalid/signal")
				requireValue(t, "itunesId", metadata.ItunesID, "123456789")
				requireValue(t, "itunesArtistId", metadata.ItunesArtistID, "987654321")
				requireValue(t, "language", metadata.Language, "en-us")
				requireValue(t, "type", metadata.Type, "episodic")
				if !reflect.DeepEqual(metadata.Genres, []string{"Technology", "Science"}) {
					t.Fatalf("genres = %v", metadata.Genres)
				}
				if !reflect.DeepEqual(item.Media.Tags, []string{"public"}) {
					t.Fatalf("tags = %v", item.Media.Tags)
				}
				if !item.Media.AutoDownloadEpisodes || strPtr(item.Media.AutoDownloadSchedule) != "0 0 1 1 *" {
					t.Fatalf("auto download = %v %v", item.Media.AutoDownloadEpisodes, item.Media.AutoDownloadSchedule)
				}
				if item.Media.LastEpisodeCheck == nil || *item.Media.LastEpisodeCheck < started {
					t.Fatalf("lastEpisodeCheck = %v, start %d", item.Media.LastEpisodeCheck, started)
				}
				if item.Media.MaxEpisodesToKeep != 0 || item.Media.MaxNewEpisodesToDownload != 3 || item.Media.NumEpisodes != 0 || len(item.Media.Episodes) != 0 {
					t.Fatalf("episode defaults wrong: %+v", item.Media)
				}
				wantCover := filepath.Join(wantPath, "cover.jpg")
				requireValue(t, "coverPath", item.Media.CoverPath, wantCover)
				if len(item.LibraryFiles) != 1 || item.LibraryFiles[0].Metadata.Filename != "cover.jpg" ||
					item.LibraryFiles[0].Metadata.Ext != "jpg" || item.LibraryFiles[0].Metadata.RelPath != "cover.jpg" ||
					item.LibraryFiles[0].Metadata.Path != wantCover || item.LibraryFiles[0].Metadata.Size != int64(len(image)) {
					t.Fatalf("libraryFiles = %+v", item.LibraryFiles)
				}
				if data, err := os.ReadFile(wantCover); err != nil || !bytes.Equal(data, image) {
					t.Fatalf("cover file: %v (%d bytes, want %d)", err, len(data), len(image))
				}
				if info, err := os.Stat(wantPath); err != nil || !info.IsDir() {
					t.Fatalf("podcast folder: %v", err)
				}
				row := podcastRow(t, e, item.Media.ID)
				requireString(t, row, "title", "Signal Show")
				requireString(t, row, "author", "Signal Author")
				requireString(t, row, "description", "<p>Signal <b>description</b></p>")
				requireString(t, row, "feedURL", stub.url("/signal.xml"))
				requireString(t, row, "imageURL", stub.url("/cover.jpg"))
				requireString(t, row, "itunesId", "123456789")
				requireString(t, row, "itunesArtistId", "987654321")
				requireString(t, row, "language", "en-us")
				requireString(t, row, "podcastType", "episodic")
				requireString(t, row, "autoDownloadSchedule", "0 0 1 1 *")
				requireString(t, row, "coverPath", wantCover)
				requireString(t, row, "genres", `["Technology","Science"]`)
				requireString(t, row, "tags", `["public"]`)
				if got := row["autoDownloadEpisodes"]; got != int64(1) {
					t.Fatalf("autoDownloadEpisodes = %v", got)
				}
				if got := row["explicit"]; got != int64(0) {
					t.Fatalf("explicit = %v", got)
				}
				items := e.Rows(t, "SELECT path, relPath, mediaType, isFile, isMissing, isInvalid, size, title, libraryFolderId FROM libraryItems WHERE id = ?", item.ID)
				if len(items) != 1 {
					t.Fatalf("%d library item rows", len(items))
				}
				stored := items[0]
				requireString(t, stored, "path", wantPath)
				requireString(t, stored, "relPath", "Signal Show")
				requireString(t, stored, "mediaType", "podcast")
				requireString(t, stored, "title", "Signal Show")
				requireString(t, stored, "libraryFolderId", folderID)
				if stored["isFile"] != int64(0) || stored["isMissing"] != int64(0) || stored["isInvalid"] != int64(0) || stored["size"] != int64(0) {
					t.Fatalf("stored flags = %+v", stored)
				}
				waitItemEvent(t, root, rootMark, "item_added", "Signal Show")
				waitItemEvent(t, listener, listenerMark, "item_added", "Signal Show")
				waitItemEvent(t, finisher, finisherMark, "item_added", "Signal Show")
			},
		},
		{
			Requirement: createReq, Invariant: createPersInv, Case: "edge", State: "changed", Assertion: "create-podcast-minimal-body-defaults",
			Sources: []string{createSrc, createModelSrc},
			Claim:   "A create body with only a title in media.metadata stores the server's podcast episode schedule, no auto-download, empty genres and tags, null optional metadata and converts numeric itunes ids to strings; a cover URL that cannot be fetched is kept as image metadata without a cover file, and the create still succeeds.",
			Run: func(t *testing.T, e *harness.Env) {
				folderID, folderPath := libraryFolder(t, e, e.IDs["podcastLibrary"])
				unreachableCover := deadURL(t)
				body := createBody(e.IDs["podcastLibrary"], folderID, filepath.Join(folderPath, "Minimal Show"), map[string]any{
					"metadata": map[string]any{
						"title":          "Minimal Show",
						"imageUrl":       unreachableCover,
						"itunesId":       12345,
						"itunesArtistId": 678,
						"genres":         []string{"Technology", ""},
					},
					"tags": []string{"", "ok"},
				})
				_, item := createPodcast(t, e, e.Token(t, "root"), body)
				metadata := item.Media.Metadata
				requireValue(t, "title", metadata.Title, "Minimal Show")
				requireValue(t, "itunesId", metadata.ItunesID, "12345")
				requireValue(t, "itunesArtistId", metadata.ItunesArtistID, "678")
				requireValue(t, "imageUrl", metadata.ImageURL, unreachableCover)
				for name, value := range map[string]*string{
					"author": metadata.Author, "description": metadata.Description, "releaseDate": metadata.ReleaseDate,
					"feedUrl": metadata.FeedURL, "itunesPageUrl": metadata.ItunesPageURL,
					"language": metadata.Language, "type": metadata.Type,
				} {
					if value != nil {
						t.Fatalf("%s = %q, want null", name, *value)
					}
				}
				if len(metadata.Genres) != 0 || len(item.Media.Tags) != 0 {
					t.Fatalf("genres = %v tags = %v, want empty", metadata.Genres, item.Media.Tags)
				}
				if item.Media.AutoDownloadEpisodes || strPtr(item.Media.AutoDownloadSchedule) != "0 * * * *" {
					t.Fatalf("auto download = %v %v, want false and the server schedule", item.Media.AutoDownloadEpisodes, item.Media.AutoDownloadSchedule)
				}
				if item.Media.CoverPath != nil || len(item.LibraryFiles) != 0 {
					t.Fatalf("cover = %v files = %+v", item.Media.CoverPath, item.LibraryFiles)
				}
				if metadata.Explicit {
					t.Fatal("explicit defaults to true")
				}
				row := podcastRow(t, e, item.Media.ID)
				requireString(t, row, "genres", "[]")
				requireString(t, row, "tags", "[]")
				requireString(t, row, "autoDownloadSchedule", "0 * * * *")
				requireString(t, row, "itunesId", "12345")
			},
		},
		{
			Requirement: createReq, Invariant: createPersInv, Case: "normal", State: "changed", Assertion: "create-podcast-explicit-and-tag-permissions",
			Sources: []string{createSrc, createModelSrc, "server/SocketAuthority.js:104-110", "server/models/User.js:646-683"},
			Claim:   "Two further creates in the same library folder store independent items with their own explicit flag, tags and feed URL, and the item_added event of an explicit podcast reaches an admin and a user who may access explicit content but not a user who may not.",
			Run: func(t *testing.T, e *harness.Env) {
				folderID, folderPath := libraryFolder(t, e, e.IDs["podcastLibrary"])
				root := e.Socket(t, e.Token(t, "root"))
				listener := e.Socket(t, e.Token(t, "listener"))
				finisher := e.Socket(t, e.Token(t, "finisher"))
				rootMark, listenerMark, finisherMark := root.Mark(), listener.Mark(), finisher.Mark()
				_, explicit := createPodcast(t, e, e.Token(t, "root"), createBody(e.IDs["podcastLibrary"], folderID, filepath.Join(folderPath, "Explicit Show"), map[string]any{
					"metadata": map[string]any{"title": "Explicit Show", "explicit": true, "feedUrl": "http://127.0.0.1:9/explicit.xml"},
				}))
				if !explicit.Media.Metadata.Explicit {
					t.Fatal("explicit was not stored")
				}
				_, tagged := createPodcast(t, e, e.Token(t, "root"), createBody(e.IDs["podcastLibrary"], folderID, filepath.Join(folderPath, "Tagged Show"), map[string]any{
					"metadata": map[string]any{"title": "Tagged Show", "feedUrl": "http://127.0.0.1:9/tagged.xml"},
					"tags":     []string{"secret"},
				}))
				if !reflect.DeepEqual(tagged.Media.Tags, []string{"secret"}) {
					t.Fatalf("tagged tags = %v", tagged.Media.Tags)
				}
				row := podcastRow(t, e, explicit.Media.ID)
				if row["explicit"] != int64(1) {
					t.Fatalf("stored explicit = %v", row["explicit"])
				}
				requireString(t, podcastRow(t, e, tagged.Media.ID), "tags", `["secret"]`)
				waitItemEvent(t, root, rootMark, "item_added", "Explicit Show")
				waitItemEvent(t, listener, listenerMark, "item_added", "Explicit Show")
				finisher.None(t, finisherMark, "item_added", quietWindow, itemEventMatcher("Explicit Show"))
				waitItemEvent(t, root, rootMark, "item_added", "Tagged Show")
			},
		},
		{
			Requirement: createReq, Invariant: createDuplicate, Case: "edge", State: "repeat", Assertion: "create-podcast-existing-path-is-400",
			Sources: []string{createSrc},
			Claim:   "Posting the same create body again answers 400 for the existing path, adds no row, emits no item_added and leaves the stored item unchanged.",
			Run: func(t *testing.T, e *harness.Env) {
				folderID, folderPath := libraryFolder(t, e, e.IDs["podcastLibrary"])
				body := createBody(e.IDs["podcastLibrary"], folderID, filepath.Join(folderPath, "Minimal Show"), metadataOnly(map[string]any{"title": "Minimal Show"}))
				before := takeSnapshot(t, e)
				rowBefore := podcastRow(t, e, itemOfByPath(t, e, filepath.Join(folderPath, "Minimal Show")).Media.ID)
				socket := e.Socket(t, e.Token(t, "root"))
				mark := socket.Mark()
				wantStatus(t, "duplicate path", e.Post(t, e.Token(t, "root"), "/api/podcasts", body), http.StatusBadRequest)
				before.requireUnchanged(t, e, "a duplicate create")
				socket.None(t, mark, "item_added", quietWindow, itemEventMatcher("Minimal Show"))
				if rowAfter := podcastRow(t, e, itemOfByPath(t, e, filepath.Join(folderPath, "Minimal Show")).Media.ID); !reflect.DeepEqual(rowBefore, rowAfter) {
					t.Fatalf("the stored podcast changed:\n%+v\n%+v", rowBefore, rowAfter)
				}
			},
		},
		{
			Requirement: createReq, Invariant: createAuthInv, Case: "error", State: "failure", Assertion: "create-podcast-rejections-change-nothing",
			Sources: []string{createSrc, "server/routers/ApiRouter.js:248"},
			Claim:   "POST /api/podcasts answers 401 without a token, 403 for a non-admin, 400 for a missing media or metadata, an invalid auto-download schedule (checked before the library lookup), an empty or non-string path, a path outside the library folder or a path that is an existing file or item, and 404 for an unknown library or a folder of another library; no rejected request creates a row, a folder or an event.",
			Run: func(t *testing.T, e *harness.Env) {
				folderID, folderPath := libraryFolder(t, e, e.IDs["podcastLibrary"])
				bookFolderID, bookFolderPath := libraryFolder(t, e, e.IDs["library"])
				existing := filepath.Join(folderPath, "Fixture Cast")
				outside := filepath.Join(bookFolderPath, "Outside Show")
				if _, err := os.Stat(outside); err == nil {
					t.Fatalf("the reject fixture already exists: %s", outside)
				}
				rejectedMedia := func() map[string]any { return metadataOnly(map[string]any{"title": "Rejected Show"}) }
				base := func() map[string]any {
					return createBody(e.IDs["podcastLibrary"], folderID, filepath.Join(folderPath, "Rejected Show"), rejectedMedia())
				}
				before := takeSnapshot(t, e)
				socket := e.Socket(t, e.Token(t, "root"))
				mark := socket.Mark()
				wantStatus(t, "anonymous", e.Do(t, harness.Request{Method: http.MethodPost, Path: "/api/podcasts", JSON: base()}), http.StatusUnauthorized)
				wantStatus(t, "listener", e.Post(t, e.Token(t, "listener"), "/api/podcasts", base()), http.StatusForbidden)
				wantStatus(t, "no media", e.Post(t, e.Token(t, "root"), "/api/podcasts", map[string]any{"libraryId": e.IDs["podcastLibrary"], "folderId": folderID, "path": filepath.Join(folderPath, "Rejected Show")}), http.StatusBadRequest)
				wantStatus(t, "no metadata", e.Post(t, e.Token(t, "root"), "/api/podcasts", createBody(e.IDs["podcastLibrary"], folderID, filepath.Join(folderPath, "Rejected Show"), map[string]any{})), http.StatusBadRequest)
				badSchedule := base()
				badSchedule["media"].(map[string]any)["autoDownloadSchedule"] = "every hour"
				wantStatus(t, "invalid schedule", e.Post(t, e.Token(t, "root"), "/api/podcasts", badSchedule), http.StatusBadRequest)
				both := base()
				both["libraryId"] = "not-a-library"
				both["media"].(map[string]any)["autoDownloadSchedule"] = "every hour"
				wantStatus(t, "invalid schedule before the library lookup", e.Post(t, e.Token(t, "root"), "/api/podcasts", both), http.StatusBadRequest)
				unknownLibrary := base()
				unknownLibrary["libraryId"] = "not-a-library"
				wantStatus(t, "unknown library", e.Post(t, e.Token(t, "root"), "/api/podcasts", unknownLibrary), http.StatusNotFound)
				wrongFolder := base()
				wrongFolder["folderId"] = bookFolderID
				wantStatus(t, "folder of another library", e.Post(t, e.Token(t, "root"), "/api/podcasts", wrongFolder), http.StatusNotFound)
				unknownFolder := base()
				unknownFolder["folderId"] = "not-a-folder"
				wantStatus(t, "unknown folder", e.Post(t, e.Token(t, "root"), "/api/podcasts", unknownFolder), http.StatusNotFound)
				wantStatus(t, "empty path", e.Post(t, e.Token(t, "root"), "/api/podcasts", createBody(e.IDs["podcastLibrary"], folderID, "", rejectedMedia())), http.StatusBadRequest)
				wantStatus(t, "non-string path", e.Post(t, e.Token(t, "root"), "/api/podcasts", map[string]any{"libraryId": e.IDs["podcastLibrary"], "folderId": folderID, "path": 7, "media": rejectedMedia()}), http.StatusBadRequest)
				wantStatus(t, "path outside the folder", e.Post(t, e.Token(t, "root"), "/api/podcasts", createBody(e.IDs["podcastLibrary"], folderID, outside, rejectedMedia())), http.StatusBadRequest)
				wantStatus(t, "existing item path", e.Post(t, e.Token(t, "root"), "/api/podcasts", createBody(e.IDs["podcastLibrary"], folderID, existing, rejectedMedia())), http.StatusBadRequest)
				filePath := filepath.Join(existing, "episode-1.mp3")
				wantStatus(t, "path is a file", e.Post(t, e.Token(t, "root"), "/api/podcasts", createBody(e.IDs["podcastLibrary"], folderID, filePath, rejectedMedia())), http.StatusBadRequest)
				if _, err := os.Stat(outside); err == nil {
					t.Fatalf("a rejected create made the folder %s", outside)
				}
				before.requireUnchanged(t, e, "rejected creates")
				socket.None(t, mark, "item_added", quietWindow, nil)
			},
		},
	}
}

// waitItemEvent waits for an item event carrying the title.
func waitItemEvent(t *testing.T, socket *harness.Socket, mark int, name, title string) harness.Event {
	t.Helper()
	return socket.Wait(t, mark, name, eventTimeout, itemEventMatcher(title))
}

// itemOfByPath returns the library item stored at a path through the API.
func itemOfByPath(t *testing.T, e *harness.Env, path string) libraryItemJSON {
	t.Helper()
	rows := e.Rows(t, "SELECT id FROM libraryItems WHERE path = ?", path)
	if len(rows) != 1 {
		t.Fatalf("%d library items at %q", len(rows), path)
	}
	id, _ := rows[0]["id"].(string)
	return itemOf(t, e, id)
}
