package item_read

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"audiobookshelf-compat/tests/replacement/harness"
)

// slowEnclosure serves episode URLs that never answer until released, so a
// podcast download stays in progress. The returned release function must be
// called before the test ends.
func slowEnclosure(t *testing.T) (baseURL string, release func()) {
	t.Helper()
	done := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		select {
		case <-done:
		case <-r.Context().Done():
		}
	}))
	var once bool
	release = func() {
		if !once {
			once = true
			close(done)
			server.CloseClientConnections()
			server.Close()
		}
	}
	t.Cleanup(release)
	return server.URL, release
}

func reviewScenarios(s *setup) []harness.Scenario {
	return []harness.Scenario{
		{
			Requirement: "route-r063", Invariant: "INV-ROUTE-R063-CONTRACT", Case: "error", State: "stateless", Assertion: "metadata-object-missing-item",
			Sources: []string{metadataObjectSource},
			Claim:   "A book flagged missing answers 400 on the metadata-object route, while the same user reads its item document, which shows isMissing true.",
			Run: func(t *testing.T, _ *harness.Env) {
				e := startQuiet(t)
				e.Stop(t)
				// no API marks a healthy item missing without deleting its files
				if n := e.Exec(t, "UPDATE libraryItems SET isMissing = 1 WHERE id = ?", e.IDs["saga1"]); n != 1 {
					t.Fatalf("marked %d rows missing", n)
				}
				e.Restart(t)
				root := e.Token(t, "root")
				wantStatus(t, "missing item", e.Get(t, root, "/api/items/"+e.IDs["saga1"]+"/metadata-object"), http.StatusBadRequest)
				wantStatus(t, "healthy item", e.Get(t, root, "/api/items/"+e.IDs["saga2"]+"/metadata-object"), http.StatusOK)
				valueAt(t, e.Get(t, root, "/api/items/"+e.IDs["saga1"]).Object(t), true, "isMissing")
			},
		},
		{
			Requirement: "route-r063", Invariant: "INV-ROUTE-R063-CONTRACT", Case: "edge", State: "stateless", Assertion: "metadata-object-excluded-audio-files",
			Sources: []string{metadataObjectSource, ffmetadataSource, "server/models/Book.js:260-262", "server/controllers/LibraryItemController.js:481-518"},
			Claim:   "TRACKTOTAL counts only included audio files (2 of 3 after one is excluded) and a book whose audio files are all excluded answers 400.",
			Run: func(t *testing.T, _ *harness.Env) {
				e := startQuiet(t)
				root := e.Token(t, "root")
				id := e.IDs["saga1"]
				files := filesOfItem(t, e, id)[:3]
				order := func(excluded string) map[string]any {
					var list []map[string]any
					for _, f := range files {
						list = append(list, map[string]any{"ino": f.Ino, "exclude": f.Ino == excluded || excluded == "*"})
					}
					return map[string]any{"orderedFileData": list}
				}
				wantStatus(t, "exclude one", e.Patch(t, root, "/api/items/"+id+"/tracks", order(files[1].Ino)), http.StatusOK)
				resp := e.Get(t, root, "/api/items/"+id+"/metadata-object")
				wantStatus(t, "one excluded", resp, http.StatusOK)
				requireSame(t, "metadata object", ffMetadata(t, e, id), resp.Object(t))
				valueAt(t, resp.Object(t), "2", "TRACKTOTAL")
				wantStatus(t, "exclude all", e.Patch(t, root, "/api/items/"+id+"/tracks", order("*")), http.StatusOK)
				wantStatus(t, "all excluded", e.Get(t, root, "/api/items/"+id+"/metadata-object"), http.StatusBadRequest)
			},
		},
		{
			Requirement: "route-r050", Invariant: "INV-R050-INCLUDE-001", Case: "normal", State: "stateless", Assertion: "include-downloads-with-active-download",
			Sources: []string{includeSource, "server/objects/PodcastEpisodeDownload.js:30-51", "server/managers/PodcastManager.js:73-95"},
			Claim:   "While one episode download is in progress and another is queued, include=downloads lists the active one in episodesDownloading and the waiting one in episodeDownloadsQueued, with their url, title, item, library and podcast fields.",
			Run: func(t *testing.T, _ *harness.Env) {
				e := startQuiet(t)
				base, release := slowEnclosure(t)
				urls := []string{base + "/slow-a.mp3", base + "/slow-b.mp3"}
				var episodes []map[string]any
				for i, title := range []string{"Slow A", "Slow B"} {
					episodes = append(episodes, map[string]any{"title": title, "enclosure": map[string]any{"url": urls[i], "type": "audio/mpeg", "length": "1000"}})
				}
				id := e.IDs["podcast"]
				wantStatus(t, "start downloads", e.Post(t, e.Token(t, "root"), "/api/podcasts/"+id+"/download-episodes", episodes), http.StatusOK)
				defer release()
				var doc map[string]any
				harness.Eventually(t, 10*time.Second, "the item to report the active download", func() bool {
					doc = expandedWith(t, e, e.Token(t, "listener"), id, map[string][]string{"include": {"downloads"}})
					active, _ := doc["episodesDownloading"].([]any)
					return len(active) == 1
				})
				queued, _ := doc["episodeDownloadsQueued"].([]any)
				if len(queued) != 1 {
					t.Fatalf("episodeDownloadsQueued = %v, want one entry", doc["episodeDownloadsQueued"])
				}
				for i, path := range [][]any{{"episodesDownloading", 0}, {"episodeDownloadsQueued", 0}} {
					entry := harness.Dig(doc, path...)
					valueAt(t, entry, urls[i], "url")
					valueAt(t, entry, []string{"Slow A", "Slow B"}[i], "episodeDisplayTitle")
					valueAt(t, entry, id, "libraryItemId")
					valueAt(t, entry, e.IDs["podcastLibrary"], "libraryId")
					valueAt(t, entry, "Fixture Cast", "podcastTitle")
					valueAt(t, entry, false, "podcastExplicit")
					valueAt(t, entry, false, "isFinished")
					valueAt(t, entry, false, "failed")
					valueAt(t, entry, "full", "episodeType")
					if text, _ := harness.Dig(entry, "id").(string); text == "" {
						t.Fatalf("download entry %d has no id", i)
					}
				}
			},
		},
		{
			Requirement: "route-r052", Invariant: "INV-R052-SINGLE-001", Case: "error", State: "stateless", Assertion: "download-item-files-gone-from-disk",
			Sources: []string{downloadItemSource, "server/controllers/LibraryItemController.js:152-160"},
			Claim:   "When the file of a single-file item is gone from disk its download answers 404, and when the folder of a folder item is gone the download is either refused or an empty zip; the stored rows are not changed by either request.",
			Run: func(t *testing.T, _ *harness.Env) {
				e := startQuiet(t)
				id, file := (&rootFile{}).create(t, e)
				removeIfExists(t, file.Path)
				rows := snapshotItems(t, e)
				wantStatus(t, "single file gone", e.Get(t, e.Token(t, "root"), "/api/items/"+id+"/download"), http.StatusNotFound)
				folder := e.IDs["novel"]
				if err := os.RemoveAll(itemPath(t, e, folder)); err != nil {
					t.Fatalf("remove folder: %v", err)
				}
				// upstream streams an empty zip for a vanished folder; only "no item content" is asserted
				if resp := e.Get(t, e.Token(t, "root"), "/api/items/"+folder+"/download"); resp.Status < 400 && len(zipEntries(t, resp.Body)) != 0 {
					t.Fatalf("download of a vanished folder answered %d with %d entries", resp.Status, len(zipEntries(t, resp.Body)))
				}
				if snapshotItems(t, e) != rows {
					t.Fatal("failed downloads changed stored rows")
				}
			},
		},
		{
			Requirement: "route-r052", Invariant: "INV-R052-SAMPLED", Case: "error", State: "stateless", Assertion: "download-unknown-item-before-permission",
			Sources: []string{downloadItemSource, middlewareSource, "server/controllers/LibraryItemController.js:163-173"},
			Claim:   "For a user without download permission an unknown item is 404 and an existing one is 403, on the item download, file download and stream routes (the stream route needs no permission, so it is 404 for the unknown item and 200 for a real one).",
			Run: func(t *testing.T, e *harness.Env) {
				token := s.token(t, e, "nodownload")
				file := fileNamed(t, e, e.IDs["saga1"], "01 - Part 1.mp3")
				wantStatus(t, "item download, unknown", e.Get(t, token, "/api/items/"+missingID+"/download"), http.StatusNotFound)
				wantStatus(t, "item download, real", e.Get(t, token, "/api/items/"+e.IDs["saga1"]+"/download"), http.StatusForbidden)
				wantStatus(t, "file download, unknown item", e.Get(t, token, "/api/items/"+missingID+"/file/"+file.Ino+"/download"), http.StatusNotFound)
				wantStatus(t, "file stream, unknown item", e.Get(t, token, "/api/items/"+missingID+"/file/"+file.Ino), http.StatusNotFound)
				wantStatus(t, "file stream, real", e.Get(t, token, fileRoute(e.IDs["saga1"], file)), http.StatusOK)
			},
		},
		{
			Requirement: "route-r066", Invariant: "INV-R066-SAMPLED", Case: "edge", State: "stateless", Assertion: "file-stream-apple-m4b-keeps-audio-mp4",
			Sources: []string{getFileSource},
			Claim:   "The stream route does not apply the iPhone m4b workaround of the download route: an iPhone WebKit agent still receives audio/mp4 for an m4b file.",
			Run: func(t *testing.T, e *harness.Env) {
				file := fileNamed(t, e, e.IDs["novel"], "Single File Novel.m4b")
				wantFileResponse(t, get(t, e, e.Token(t, "listener"), fileRoute(e.IDs["novel"], file), map[string]string{"User-Agent": appleSafari}), file, "audio/mp4")
			},
		},
		{
			Requirement: "route-r069", Invariant: "INV-ROUTE-R069-CONTRACT", Case: "normal", State: "stateless", Assertion: "ebook-supplementary-by-file-id",
			Sources: []string{ebookSource, "server/controllers/LibraryItemController.js:1165-1212"},
			Claim:   "A second ebook file added to the item folder is served by its inode with its own media type while the id-less request still returns the primary epub; making the primary supplementary leaves the id-less request at 404 while both files stay available by id.",
			Run: func(t *testing.T, _ *harness.Env) {
				e := startQuiet(t)
				root := e.Token(t, "root")
				id := e.IDs["ebook"]
				primary := fileNamed(t, e, id, "Ebook Only.epub")
				extra := filepath.Join(filepath.Dir(primary.Path), "Extra Notes.pdf")
				content := []byte("%PDF-1.4\n% supplementary ebook of the item_read packet\n")
				if err := os.WriteFile(extra, content, 0o644); err != nil {
					t.Fatalf("write %s: %v", extra, err)
				}
				wantStatus(t, "scan", e.Post(t, root, "/api/libraries/"+e.IDs["library"]+"/scan", nil), http.StatusOK)
				var supplement fileInfo
				harness.Eventually(t, 30*time.Second, "the scan to record the second ebook", func() bool {
					for _, f := range filesOfItem(t, e, id) {
						if f.Name == "Extra Notes.pdf" {
							supplement = f
							return true
						}
					}
					return false
				})
				primary = fileNamed(t, e, id, "Ebook Only.epub") // the scan refreshed the stored inodes
				wantFileResponse(t, e.Get(t, root, "/api/items/"+id+"/ebook/"+supplement.Ino), supplement, "application/pdf")
				wantFileResponse(t, e.Get(t, root, "/api/items/"+id+"/ebook"), primary, "application/epub+zip")
				wantStatus(t, "make primary supplementary", e.Patch(t, root, "/api/items/"+id+"/ebook/"+primary.Ino+"/status", nil), http.StatusOK)
				wantStatus(t, "no primary ebook", e.Get(t, root, "/api/items/"+id+"/ebook"), http.StatusNotFound)
				wantFileResponse(t, e.Get(t, root, "/api/items/"+id+"/ebook/"+primary.Ino), primary, "application/epub+zip")
				wantFileResponse(t, e.Get(t, root, "/api/items/"+id+"/ebook/"+supplement.Ino), supplement, "application/pdf")
			},
		},
	}
}
