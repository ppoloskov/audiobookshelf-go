package podcast_feed

import (
	"bytes"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"audiobookshelf-compat/testmachine/scenarios/harness"
)

const (
	opmlCreateReq     = "route-r152"
	opmlCreateInv     = "INV-R152-CREATE-001"
	opmlCreateRepeat  = "INV-R152-REPEAT-001"
	opmlCreateAuthInv = "INV-R152-SAMPLED"
	opmlCreateSrc     = "server/controllers/PodcastController.js:249-274"
	opmlManagerSrc    = "server/managers/PodcastManager.js:526-743"
)

// importFeedA is the first feed of the import scenarios.
func importFeedA(stub *feedStub) feed {
	image := stub.url("/a-cover.jpg")
	return feed{
		Title:       "Wire Signal",
		Author:      "Wire Author",
		Description: "<p>Wire <b>import</b></p>",
		Language:    "de",
		Explicit:    "yes",
		Type:        "serial",
		Image:       image,
		Categories:  []string{"Technology/Software"},
		Items: []feedItem{
			{Title: "Wire One", EnclosureURL: stub.url("/wire-1.mp3"), EnclosureType: "audio/mpeg", GUID: "wire-1"},
			{Title: "Wire Two", EnclosureURL: stub.url("/wire-2.mp3"), EnclosureType: "audio/mpeg", GUID: "wire-2"},
		},
	}
}

// importFeedB is the second feed of the import scenarios.
func importFeedB(stub *feedStub) feed {
	return feed{
		Title:      "Night Wire",
		Author:     "Night Author",
		Categories: []string{"Society"},
		Items:      []feedItem{{Title: "Night One", EnclosureURL: stub.url("/night-1.mp3"), EnclosureType: "audio/mpeg"}},
	}
}

// waitOPMLImport waits for the import task to finish, the signal that the
// asynchronous import loop has processed every feed URL.
func waitOPMLImport(t *testing.T, socket *harness.Socket, mark int) {
	t.Helper()
	socket.Wait(t, mark, "task_finished", eventTimeout, func(args []json.RawMessage) bool {
		if len(args) == 0 {
			return false
		}
		var task struct {
			Action string `json:"action"`
		}
		if err := json.Unmarshal(args[0], &task); err != nil {
			return false
		}
		return task.Action == "opml-import"
	})
}

// importBodies builds the import request body.
func importBodies(libraryID, folderID string, feeds []string, autoDownload bool) map[string]any {
	return map[string]any{"feeds": feeds, "libraryId": libraryID, "folderId": folderID, "autoDownloadEpisodes": autoDownload}
}

func opmlCreateScenarios() []harness.Scenario {
	return []harness.Scenario{
		{
			Requirement: opmlCreateReq, Invariant: opmlCreateInv, Case: "normal", State: "initial", Assertion: "opml-create-imports-feeds-and-covers",
			Sources: []string{opmlCreateSrc, opmlManagerSrc, "server/models/Podcast.js:79-125"},
			Claim:   "POST /api/podcasts/opml/create answers 200 and imports each feed as a library item under the folder named after the sanitized feed title, with the feed metadata, categories as genres, the feed URL and cover, the auto-download flag and the server episode schedule, and emits item_added per created podcast.",
			Run: func(t *testing.T, e *harness.Env) {
				stub := newFeedStub(t)
				stub.setFeed("/a.xml", importFeedA(stub).document())
				stub.setFeed("/b.xml", importFeedB(stub).document())
				cover := jpegBytes(t)
				stub.setBytes("/a-cover.jpg", "image/jpeg", cover)
				folderID, folderPath := libraryFolder(t, e, e.IDs["podcastLibrary"])
				socket := e.Socket(t, e.Token(t, "root"))
				mark := socket.Mark()
				body := importBodies(e.IDs["podcastLibrary"], folderID, []string{stub.url("/a.xml"), stub.url("/b.xml")}, true)
				wantStatus(t, "opml create", e.Post(t, e.Token(t, "root"), "/api/podcasts/opml/create", body), http.StatusOK)
				waitItemEvent(t, socket, mark, "item_added", "Wire Signal")
				waitItemEvent(t, socket, mark, "item_added", "Night Wire")
				waitOPMLImport(t, socket, mark)

				wire := itemOfByPath(t, e, filepath.Join(folderPath, "Wire Signal"))
				if wire.MediaType != "podcast" || wire.LibraryID != e.IDs["podcastLibrary"] || wire.FolderID != folderID || wire.RelPath != "Wire Signal" {
					t.Fatalf("imported item identity wrong: %+v", wire)
				}
				metadata := wire.Media.Metadata
				requireValue(t, "title", metadata.Title, "Wire Signal")
				requireValue(t, "author", metadata.Author, "Wire Author")
				requireValue(t, "description", metadata.Description, "<p>Wire <b>import</b></p>")
				requireValue(t, "feedUrl", metadata.FeedURL, stub.url("/a.xml"))
				requireValue(t, "imageUrl", metadata.ImageURL, stub.url("/a-cover.jpg"))
				requireValue(t, "releaseDate", metadata.ReleaseDate, "")
				requireValue(t, "language", metadata.Language, "")
				requireValue(t, "itunesPageUrl", metadata.ItunesPageURL, "")
				requireValue(t, "itunesId", metadata.ItunesID, "")
				requireValue(t, "itunesArtistId", metadata.ItunesArtistID, "")
				requireValue(t, "type", metadata.Type, "")
				if !reflect.DeepEqual(metadata.Genres, []string{"Technology:Software"}) || metadata.Explicit {
					t.Fatalf("genres = %v explicit = %v", metadata.Genres, metadata.Explicit)
				}
				if !wire.Media.AutoDownloadEpisodes || strPtr(wire.Media.AutoDownloadSchedule) != "0 * * * *" || wire.Media.NumEpisodes != 0 || len(wire.Media.Episodes) != 0 {
					t.Fatalf("imported media = %+v", wire.Media)
				}
				requireValue(t, "coverPath", wire.Media.CoverPath, filepath.Join(wire.Path, "cover.jpg"))
				if data, err := os.ReadFile(filepath.Join(wire.Path, "cover.jpg")); err != nil || !bytes.Equal(data, cover) {
					t.Fatalf("imported cover: %v (%d bytes, want %d)", err, len(data), len(cover))
				}
				row := podcastRow(t, e, wire.Media.ID)
				requireString(t, row, "feedURL", stub.url("/a.xml"))
				requireString(t, row, "language", "")
				requireString(t, row, "genres", `["Technology:Software"]`)
				requireString(t, row, "autoDownloadSchedule", "0 * * * *")
				if got := row["explicit"]; got != int64(0) {
					t.Fatalf("explicit = %v", got)
				}
				if got := row["autoDownloadEpisodes"]; got != int64(1) {
					t.Fatalf("autoDownloadEpisodes = %v", got)
				}

				night := itemOfByPath(t, e, filepath.Join(folderPath, "Night Wire"))
				requireValue(t, "night title", night.Media.Metadata.Title, "Night Wire")
				requireValue(t, "night feedUrl", night.Media.Metadata.FeedURL, stub.url("/b.xml"))
				if night.Media.Metadata.ImageURL != nil || night.Media.CoverPath != nil || len(night.LibraryFiles) != 0 {
					t.Fatalf("the feed without an image has a cover: %+v", night.Media.Metadata.ImageURL)
				}
			},
		},
		{
			Requirement: opmlCreateReq, Invariant: opmlCreateRepeat, Case: "normal", State: "repeat", Assertion: "opml-create-repeat-adds-no-item",
			Sources: []string{opmlManagerSrc},
			Claim:   "Importing the same feed URLs again answers 200 but creates no second library item, keeps the stored podcast row unchanged and emits no item_added.",
			Run: func(t *testing.T, e *harness.Env) {
				stub := newFeedStub(t)
				stub.setFeed("/a.xml", importFeedA(stub).document())
				stub.setFeed("/b.xml", importFeedB(stub).document())
				folderID, folderPath := libraryFolder(t, e, e.IDs["podcastLibrary"])
				wire := itemOfByPath(t, e, filepath.Join(folderPath, "Wire Signal"))
				rowBefore := podcastRow(t, e, wire.Media.ID)
				before := takeSnapshot(t, e)
				socket := e.Socket(t, e.Token(t, "root"))
				mark := socket.Mark()
				body := importBodies(e.IDs["podcastLibrary"], folderID, []string{stub.url("/a.xml"), stub.url("/b.xml")}, true)
				wantStatus(t, "opml create repeat", e.Post(t, e.Token(t, "root"), "/api/podcasts/opml/create", body), http.StatusOK)
				waitOPMLImport(t, socket, mark)
				before.requireUnchanged(t, e, "a repeated import")
				socket.None(t, mark, "item_added", quietWindow, nil)
				if rowAfter := podcastRow(t, e, wire.Media.ID); !reflect.DeepEqual(rowBefore, rowAfter) {
					t.Fatalf("the imported podcast changed:\n%+v\n%+v", rowBefore, rowAfter)
				}
			},
		},
		{
			Requirement: opmlCreateReq, Invariant: opmlCreateRepeat, Case: "edge", State: "changed", Assertion: "opml-create-skips-existing-and-imports-new",
			Sources: []string{opmlManagerSrc},
			Claim:   "An import list that mixes an unreachable feed, an already imported feed and a new one creates only the new podcast, without auto-download and without a cover when the feed image cannot be fetched, and keeps the earlier import untouched.",
			Run: func(t *testing.T, e *harness.Env) {
				stub := newFeedStub(t)
				stub.setFeed("/a.xml", importFeedA(stub).document())
				stub.setFeed("/c.xml", feed{
					Title:  "Fresh Wire",
					Author: "Fresh Author",
					Image:  stub.url("/missing-cover.jpg"),
					Items:  []feedItem{{Title: "Fresh One", EnclosureURL: stub.url("/fresh.mp3"), EnclosureType: "audio/mpeg"}},
				}.document())
				folderID, folderPath := libraryFolder(t, e, e.IDs["podcastLibrary"])
				wire := itemOfByPath(t, e, filepath.Join(folderPath, "Wire Signal"))
				rowBefore := podcastRow(t, e, wire.Media.ID)
				before := takeSnapshot(t, e)
				body := importBodies(e.IDs["podcastLibrary"], folderID, []string{deadURL(t), stub.url("/a.xml"), stub.url("/c.xml")}, false)
				socket := e.Socket(t, e.Token(t, "root"))
				mark := socket.Mark()
				wantStatus(t, "partial import", e.Post(t, e.Token(t, "root"), "/api/podcasts/opml/create", body), http.StatusOK)
				waitItemEvent(t, socket, mark, "item_added", "Fresh Wire")
				waitOPMLImport(t, socket, mark)
				fresh := itemOfByPath(t, e, filepath.Join(folderPath, "Fresh Wire"))
				if fresh.Media.AutoDownloadEpisodes {
					t.Fatal("the create body without autoDownloadEpisodes enabled it")
				}
				requireValue(t, "fresh schedule", fresh.Media.AutoDownloadSchedule, "0 * * * *")
				if fresh.Media.CoverPath != nil || len(fresh.LibraryFiles) != 0 {
					t.Fatalf("a feed cover that cannot be fetched was stored: %+v", fresh.Media.CoverPath)
				}
				after := takeSnapshot(t, e)
				if after.libraryItems != before.libraryItems+1 || after.podcasts != before.podcasts+1 {
					t.Fatalf("import added %+v, want exactly one item and podcast on top of %+v", after, before)
				}
				if rowAfter := podcastRow(t, e, wire.Media.ID); !reflect.DeepEqual(rowBefore, rowAfter) {
					t.Fatalf("the earlier import changed:\n%+v\n%+v", rowBefore, rowAfter)
				}
			},
		},
		{
			Requirement: opmlCreateReq, Invariant: opmlCreateAuthInv, Case: "error", State: "failure", Assertion: "opml-create-rejections-change-nothing",
			Sources: []string{opmlCreateSrc, "server/utils/index.js:233-241"},
			Claim:   "POST /api/podcasts/opml/create answers 401 without a token, 403 for a non-admin, 400 when feeds is not a non-empty array of URLs (checked before the ids) or libraryId or folderId is missing, and 404 when the folder is unknown or belongs to another library; a rejected request imports nothing.",
			Run: func(t *testing.T, e *harness.Env) {
				folderID, _ := libraryFolder(t, e, e.IDs["podcastLibrary"])
				bookFolderID, _ := libraryFolder(t, e, e.IDs["library"])
				before := takeSnapshot(t, e)
				feedURL := deadURL(t)
				wantStatus(t, "anonymous", e.Do(t, harness.Request{Method: http.MethodPost, Path: "/api/podcasts/opml/create", JSON: importBodies(e.IDs["podcastLibrary"], folderID, []string{feedURL}, false)}), http.StatusUnauthorized)
				wantStatus(t, "listener", e.Post(t, e.Token(t, "listener"), "/api/podcasts/opml/create", importBodies(e.IDs["podcastLibrary"], folderID, []string{feedURL}, false)), http.StatusForbidden)
				for name, body := range map[string]map[string]any{
					"no feeds":        {"libraryId": e.IDs["podcastLibrary"], "folderId": folderID},
					"empty feeds":     importBodies(e.IDs["podcastLibrary"], folderID, []string{}, false),
					"feed not a url":  importBodies(e.IDs["podcastLibrary"], folderID, []string{"nope"}, false),
					"feed not string": importBodies(e.IDs["podcastLibrary"], folderID, []string{}, false),
					"no library":      {"feeds": []string{feedURL}, "folderId": folderID},
					"no folder":       {"feeds": []string{feedURL}, "libraryId": e.IDs["podcastLibrary"]},
					"folder mismatch": importBodies(e.IDs["podcastLibrary"], bookFolderID, []string{feedURL}, false),
					"unknown folder":  importBodies(e.IDs["podcastLibrary"], "not-a-folder", []string{feedURL}, false),
				} {
					if name == "feed not string" {
						body = map[string]any{"feeds": []any{42}, "libraryId": e.IDs["podcastLibrary"], "folderId": folderID}
					}
					status := http.StatusBadRequest
					if name == "folder mismatch" || name == "unknown folder" {
						status = http.StatusNotFound
					}
					wantStatus(t, name, e.Post(t, e.Token(t, "root"), "/api/podcasts/opml/create", body), status)
				}
				invalidAndMissing := map[string]any{"feeds": []string{"nope"}}
				wantStatus(t, "invalid feeds before the missing ids", e.Post(t, e.Token(t, "root"), "/api/podcasts/opml/create", invalidAndMissing), http.StatusBadRequest)
				before.requireUnchanged(t, e, "rejected imports")
			},
		},
	}
}
