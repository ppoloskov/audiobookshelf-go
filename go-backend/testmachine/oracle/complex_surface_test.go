//go:build oracle

package oracle

import (
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestOracleSurfaceSocketCoverSearchAndToolsBatch exercises Socket.IO streaming
// cover search (error, custom-provider results/completion, and cancellation)
// plus batch metadata embedding across both TS v2.37.2 and Go.
func TestOracleSurfaceSocketCoverSearchAndToolsBatch(t *testing.T) {
	tw := StartTwin(t, Options{})
	tsRoot, goRoot := tw.TS.Token(t, "root"), tw.Go.Token(t, "root")
	tsSock, goSock := tw.WatchTS.Socket("root"), tw.WatchGo.Socket("root")
	saga1 := tw.TS.IDs["saga1"]

	tw.Stubs.SetRoute("/cs-fast/search", http.StatusOK, "application/json", []byte(`{"matches":[{"title":"Cover Match","author":"Cover Author","cover":"http://example.invalid/cover.jpg"}]}`))

	createProvider := func(step, name, tsURL, goURL string) (string, string) {
		tsResp := tw.TS.Post(t, tsRoot, "/api/custom-metadata-providers", map[string]any{"name": name, "url": tsURL, "mediaType": "book"})
		goResp := tw.Go.Post(t, goRoot, "/api/custom-metadata-providers", map[string]any{"name": name, "url": goURL, "mediaType": "book"})
		extract := func(side string, r Response) string {
			prov, _ := r.Object(t)["provider"].(map[string]any)
			id, _ := prov["id"].(string)
			if id == "" {
				t.Fatalf("%s %s missing provider.id: %s", side, step, excerpt(r.Body))
			}
			return id
		}
		tsID, goID := extract("ts", tsResp), extract("go", goResp)
		tw.Diff.Corr.BindID(tsID, goID)
		assertNoUnexpected(t, tw, step, tw.Diff.DiffHTTP(step, tsResp, goResp))
		return tsID, goID
	}

	tsFastID, goFastID := createProvider("cs-fast-provider", "Oracle Fast Cover", tw.Stubs.TSHTTPURL+"/cs-fast", tw.Stubs.GoHTTPURL+"/cs-fast")

	release := make(chan struct{})
	slowSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	t.Cleanup(func() {
		close(release)
		slowSrv.Close()
	})
	tsSlowID, goSlowID := createProvider("cs-slow-provider", "Oracle Slow Cover", slowSrv.URL+"/cs-slow", slowSrv.URL+"/cs-slow")

	// 1. Invalid search_covers (missing title) -> cover_search_error on both.
	tsMark, goMark := tsSock.Mark(), goSock.Mark()
	tsSock.Emit(t, "search_covers", map[string]any{"requestId": "cover-err-1"})
	goSock.Emit(t, "search_covers", map[string]any{"requestId": "cover-err-1"})
	tsErr := tsSock.Wait(t, tsMark, "cover_search_error", 5*time.Second, nil)
	goErr := goSock.Wait(t, goMark, "cover_search_error", 5*time.Second, nil)
	assertNoUnexpected(t, tw, "socket-cover-search-error", tw.Diff.DiffSockets("socket-cover-search-error", []SocketEvent{tsErr}, []SocketEvent{goErr}))

	// 2. Custom-provider search_covers -> cover_search_result + cover_search_complete.
	tsFastMark, goFastMark := tsSock.Mark(), goSock.Mark()
	tsSock.Emit(t, "search_covers", map[string]any{"requestId": "cover-fast-1", "title": "Cover Match", "author": "Cover Author", "provider": "custom-" + tsFastID, "podcast": false})
	goSock.Emit(t, "search_covers", map[string]any{"requestId": "cover-fast-1", "title": "Cover Match", "author": "Cover Author", "provider": "custom-" + goFastID, "podcast": false})
	tsRes := tsSock.Wait(t, tsFastMark, "cover_search_result", 10*time.Second, nil)
	goRes := goSock.Wait(t, goFastMark, "cover_search_result", 10*time.Second, nil)
	tsDone := tsSock.Wait(t, tsFastMark, "cover_search_complete", 10*time.Second, nil)
	goDone := goSock.Wait(t, goFastMark, "cover_search_complete", 10*time.Second, nil)
	assertNoUnexpected(t, tw, "socket-cover-search-fast", tw.Diff.DiffSockets("socket-cover-search-fast", []SocketEvent{tsRes, tsDone}, []SocketEvent{goRes, goDone}))

	// 3. Slow custom-provider search_covers + cancel_cover_search -> cover_search_cancelled.
	tsCanMark, goCanMark := tsSock.Mark(), goSock.Mark()
	tsSock.Emit(t, "search_covers", map[string]any{"requestId": "cover-cancel-1", "title": "Slow Book", "author": "", "provider": "custom-" + tsSlowID, "podcast": false})
	goSock.Emit(t, "search_covers", map[string]any{"requestId": "cover-cancel-1", "title": "Slow Book", "author": "", "provider": "custom-" + goSlowID, "podcast": false})
	tsSock.Emit(t, "cancel_cover_search", "cover-cancel-1")
	goSock.Emit(t, "cancel_cover_search", "cover-cancel-1")
	tsCan := tsSock.Wait(t, tsCanMark, "cover_search_cancelled", 10*time.Second, nil)
	goCan := goSock.Wait(t, goCanMark, "cover_search_cancelled", 10*time.Second, nil)
	assertNoUnexpected(t, tw, "socket-cover-search-cancel", tw.Diff.DiffSockets("socket-cover-search-cancel", []SocketEvent{tsCan}, []SocketEvent{goCan}))

	for _, pair := range [][2]string{{tsFastID, goFastID}, {tsSlowID, goSlowID}} {
		assertNoUnexpected(t, tw, "cs-del-provider", tw.Diff.DiffHTTP(
			"cs-del-provider",
			tw.TS.Delete(t, tsRoot, "/api/custom-metadata-providers/"+pair[0]),
			tw.Go.Delete(t, goRoot, "/api/custom-metadata-providers/"+pair[1]),
		))
	}

	// 4. Batch embed metadata on saga1 and wait for task_finished.
	tsBatchMark, goBatchMark := tsSock.Mark(), goSock.Mark()
	batchBody := map[string]any{"libraryItemIds": []string{saga1}}
	assertNoUnexpected(t, tw, "tools-batch-embed-metadata", tw.Diff.DiffHTTP(
		"tools-batch-embed-metadata",
		tw.TS.Post(t, tsRoot, "/api/tools/batch/embed-metadata", batchBody),
		tw.Go.Post(t, goRoot, "/api/tools/batch/embed-metadata", batchBody),
	))
	waitTaskFinished(t, tsSock, tsBatchMark, "embed-metadata", nil)
	waitTaskFinished(t, goSock, goBatchMark, "embed-metadata", nil)
}

// TestOracleSurfaceScannerRenameAndAudioFileDelete verifies that renaming a
// scanned book folder on disk preserves its item ID via inode matching and
// exercises DELETE /api/items/:id/file/:ino on the temporary scanned item.
func TestOracleSurfaceScannerRenameAndAudioFileDelete(t *testing.T) {
	mp3Bytes := generateSineMP3(t, filepath.Join(t.TempDir(), "Rename Book.mp3"), "Rename Book", "Rename Author")
	tw := StartTwin(t, Options{})
	tsRoot, goRoot := tw.TS.Token(t, "root"), tw.Go.Token(t, "root")
	bookLib := tw.TS.IDs["library"]
	matchBookLib := func(data map[string]any) bool { return data["libraryId"] == bookLib }

	for _, mediaRoot := range []string{tw.TS.Media, tw.Go.Media} {
		writeFileMkdir(t, filepath.Join(mediaRoot, "audiobooks", "Rename Before", "Rename Book.mp3"), mp3Bytes)
	}
	tw.Diff.Corr.RegisterInodes(tw.TS.Media, tw.Go.Media, tw.TS.Metadata, tw.Go.Metadata)

	tsMark, goMark := tw.WatchTS.Socket("root").Mark(), tw.WatchGo.Socket("root").Mark()
	assertNoUnexpected(t, tw, "scan-before-rename", tw.Diff.DiffHTTP(
		"scan-before-rename",
		tw.TS.Post(t, tsRoot, "/api/libraries/"+bookLib+"/scan", nil),
		tw.Go.Post(t, goRoot, "/api/libraries/"+bookLib+"/scan", nil),
	))
	waitTaskFinished(t, tw.WatchTS.Socket("root"), tsMark, "library-scan", matchBookLib)
	waitTaskFinished(t, tw.WatchGo.Socket("root"), goMark, "library-scan", matchBookLib)

	extractFirstBookID := func(side string, resp Response) (string, int) {
		books, _ := resp.Object(t)["book"].([]any)
		if len(books) == 0 {
			t.Fatalf("%s search returned no book matches: %s", side, excerpt(resp.Body))
		}
		first, _ := books[0].(map[string]any)
		li, _ := first["libraryItem"].(map[string]any)
		id, _ := li["id"].(string)
		if id == "" {
			t.Fatalf("%s search missing libraryItem.id: %s", side, excerpt(resp.Body))
		}
		return id, len(books)
	}
	tsSearch1 := tw.TS.Get(t, tsRoot, "/api/libraries/"+bookLib+"/search?q=Rename+Book")
	goSearch1 := tw.Go.Get(t, goRoot, "/api/libraries/"+bookLib+"/search?q=Rename+Book")
	tsItemID, _ := extractFirstBookID("ts", tsSearch1)
	goItemID, _ := extractFirstBookID("go", goSearch1)
	tw.Diff.Corr.BindID(tsItemID, goItemID)
	tw.Diff.Corr.RegisterInodes(tw.TS.Media, tw.Go.Media, tw.TS.Metadata, tw.Go.Metadata)
	assertNoUnexpected(t, tw, "search-before-rename", tw.Diff.DiffHTTP("search-before-rename", tsSearch1, goSearch1))

	extractAudioIno := func(side string, resp Response) string {
		media, _ := resp.Object(t)["media"].(map[string]any)
		afs, _ := media["audioFiles"].([]any)
		if len(afs) == 0 {
			t.Fatalf("%s item missing audioFiles: %s", side, excerpt(resp.Body))
		}
		first, _ := afs[0].(map[string]any)
		ino, _ := first["ino"].(string)
		if ino == "" {
			t.Fatalf("%s audioFile missing ino: %s", side, excerpt(resp.Body))
		}
		return ino
	}
	tsIno := extractAudioIno("ts", tw.TS.Get(t, tsRoot, "/api/items/"+tsItemID))
	goIno := extractAudioIno("go", tw.Go.Get(t, goRoot, "/api/items/"+goItemID))

	// Rename directory on disk (preserves both folder and file inodes).
	for _, mediaRoot := range []string{tw.TS.Media, tw.Go.Media} {
		oldDir := filepath.Join(mediaRoot, "audiobooks", "Rename Before")
		newDir := filepath.Join(mediaRoot, "audiobooks", "Rename After")
		if err := os.Rename(oldDir, newDir); err != nil {
			t.Fatalf("rename %s -> %s: %v", oldDir, newDir, err)
		}
	}
	tw.Diff.Corr.RegisterInodes(tw.TS.Media, tw.Go.Media, tw.TS.Metadata, tw.Go.Metadata)

	tsMark2, goMark2 := tw.WatchTS.Socket("root").Mark(), tw.WatchGo.Socket("root").Mark()
	assertNoUnexpected(t, tw, "scan-after-rename", tw.Diff.DiffHTTP(
		"scan-after-rename",
		tw.TS.Post(t, tsRoot, "/api/libraries/"+bookLib+"/scan", nil),
		tw.Go.Post(t, goRoot, "/api/libraries/"+bookLib+"/scan", nil),
	))
	waitTaskFinished(t, tw.WatchTS.Socket("root"), tsMark2, "library-scan", matchBookLib)
	waitTaskFinished(t, tw.WatchGo.Socket("root"), goMark2, "library-scan", matchBookLib)

	tsItemAfter := tw.TS.Get(t, tsRoot, "/api/items/"+tsItemID)
	goItemAfter := tw.Go.Get(t, goRoot, "/api/items/"+goItemID)
	if tsItemAfter.Status != http.StatusOK || goItemAfter.Status != http.StatusOK {
		t.Fatalf("item lost after folder rename: ts=%d go=%d", tsItemAfter.Status, goItemAfter.Status)
	}
	if rel, _ := tsItemAfter.Object(t)["relPath"].(string); !strings.Contains(rel, "Rename After") {
		t.Fatalf("ts relPath = %q, want Rename After", rel)
	}
	assertNoUnexpected(t, tw, "item-after-rename", tw.Diff.DiffHTTP("item-after-rename", tsItemAfter, goItemAfter))

	_, tsCount := extractFirstBookID("ts-after", tw.TS.Get(t, tsRoot, "/api/libraries/"+bookLib+"/search?q=Rename+Book"))
	_, goCount := extractFirstBookID("go-after", tw.Go.Get(t, goRoot, "/api/libraries/"+bookLib+"/search?q=Rename+Book"))
	if tsCount != 1 || goCount != 1 {
		t.Fatalf("expected 1 item after rename (no duplicate), got ts=%d go=%d", tsCount, goCount)
	}
	assertDBEqual(t, tw, "rename-scan-db")

	assertNoUnexpected(t, tw, "delete-scanned-audio-file", tw.Diff.DiffHTTP(
		"delete-scanned-audio-file",
		tw.TS.Delete(t, tsRoot, "/api/items/"+tsItemID+"/file/"+tsIno),
		tw.Go.Delete(t, goRoot, "/api/items/"+goItemID+"/file/"+goIno),
	))
	assertDBEqual(t, tw, "delete-scanned-audio-file-db")
}

// TestOracleSurfaceLibraryFiltersDeep exercises remaining book/podcast sort &
// filter combinations and multi-item library zip download.
func TestOracleSurfaceLibraryFiltersDeep(t *testing.T) {
	tw := StartTwin(t, Options{NoSockets: true})
	tsRoot, goRoot := tw.TS.Token(t, "root"), tw.Go.Token(t, "root")
	tsListener, goListener := tw.TS.Token(t, "listener"), tw.Go.Token(t, "listener")
	bookLib, podLib := tw.TS.IDs["library"], tw.TS.IDs["podcastLibrary"]
	saga1, saga2 := tw.TS.IDs["saga1"], tw.TS.IDs["saga2"]
	novel, ebook, disposable := tw.TS.IDs["novel"], tw.TS.IDs["ebook"], tw.TS.IDs["disposable"]

	seeds := []struct {
		id   string
		meta map[string]any
	}{
		{saga1, map[string]any{"publishedYear": "2018", "publisher": "Filter Press", "language": "en"}},
		{saga2, map[string]any{"publishedYear": "2019", "publisher": "Other Press", "language": "fr"}},
		{ebook, map[string]any{"publishedYear": "2020", "publisher": "Filter Press"}},
		{disposable, map[string]any{"publishedYear": "2022"}},
	}
	for _, s := range seeds {
		payload := map[string]any{"metadata": s.meta}
		assertNoUnexpected(t, tw, "seed-"+s.id, tw.Diff.DiffHTTP(
			"seed-"+s.id,
			tw.TS.Patch(t, tsRoot, "/api/items/"+s.id+"/media", payload),
			tw.Go.Patch(t, goRoot, "/api/items/"+s.id+"/media", payload),
		))
	}

	b64 := func(v string) string { return base64.StdEncoding.EncodeToString([]byte(v)) }
	queries := []struct {
		name  string
		path  string
		role  string
		query url.Values
	}{
		{"books-sort-published-year", "/api/libraries/" + bookLib + "/items", "root", url.Values{"sort": {"media.metadata.publishedYear"}, "limit": {"10"}, "page": {"0"}}},
		{"books-sort-published-year-desc", "/api/libraries/" + bookLib + "/items", "root", url.Values{"sort": {"media.metadata.publishedYear"}, "desc": {"1"}, "limit": {"10"}, "page": {"0"}}},
		{"books-sort-author-lf-single", "/api/libraries/" + bookLib + "/items", "root", url.Values{"sort": {"media.metadata.authorNameLF"}, "filter": {"tracks." + b64("single")}, "limit": {"10"}, "page": {"0"}}},
		{"books-sort-birthtime", "/api/libraries/" + bookLib + "/items", "root", url.Values{"sort": {"birthtimeMs"}, "limit": {"10"}, "page": {"0"}}},
		{"books-sort-mtime-desc", "/api/libraries/" + bookLib + "/items", "root", url.Values{"sort": {"mtimeMs"}, "desc": {"1"}, "limit": {"10"}, "page": {"0"}}},
		{"books-sort-progress-desc", "/api/libraries/" + bookLib + "/items", "listener", url.Values{"sort": {"progress"}, "desc": {"1"}, "limit": {"10"}, "page": {"0"}}},
		{"books-filter-languages-en", "/api/libraries/" + bookLib + "/items", "root", url.Values{"filter": {"languages." + b64("en")}, "limit": {"10"}, "page": {"0"}}},
		{"books-filter-publishers", "/api/libraries/" + bookLib + "/items", "root", url.Values{"filter": {"publishers." + b64("Filter Press")}, "limit": {"10"}, "page": {"0"}}},
		{"books-filter-tracks-multi", "/api/libraries/" + bookLib + "/items", "root", url.Values{"filter": {"tracks." + b64("multi")}, "limit": {"10"}, "page": {"0"}}},
		{"books-filter-tracks-none", "/api/libraries/" + bookLib + "/items", "root", url.Values{"filter": {"tracks." + b64("none")}, "limit": {"10"}, "page": {"0"}}},
		{"books-filter-ebooks-ebook", "/api/libraries/" + bookLib + "/items", "root", url.Values{"filter": {"ebooks." + b64("ebook")}, "limit": {"10"}, "page": {"0"}}},
		{"books-filter-ebooks-no-ebook", "/api/libraries/" + bookLib + "/items", "root", url.Values{"filter": {"ebooks." + b64("no-ebook")}, "limit": {"10"}, "page": {"0"}}},
		{"books-filter-ebooks-supplementary", "/api/libraries/" + bookLib + "/items", "root", url.Values{"filter": {"ebooks." + b64("supplementary")}, "limit": {"10"}, "page": {"0"}}},
		{"books-filter-published-decades", "/api/libraries/" + bookLib + "/items", "root", url.Values{"filter": {"publishedDecades." + b64("2020")}, "limit": {"10"}, "page": {"0"}}},
		{"podcast-filter-genres-sort-numtracks", "/api/libraries/" + podLib + "/items", "root", url.Values{"filter": {"genres." + b64("Technology")}, "sort": {"media.numTracks"}, "desc": {"1"}, "limit": {"10"}, "page": {"0"}}},
		{"podcast-sort-author", "/api/libraries/" + podLib + "/items", "root", url.Values{"sort": {"media.metadata.author"}, "limit": {"10"}, "page": {"0"}}},
		{"podcast-sort-added-at", "/api/libraries/" + podLib + "/items", "root", url.Values{"sort": {"addedAt"}, "desc": {"1"}, "limit": {"10"}, "page": {"0"}}},
	}
	for _, tc := range queries {
		t.Run(tc.name, func(t *testing.T) {
			tsTok, goTok := tsRoot, goRoot
			if tc.role == "listener" {
				tsTok, goTok = tsListener, goListener
			}
			tsResp := tw.TS.Do(t, Request{Method: http.MethodGet, Path: tc.path, Token: tsTok, Query: tc.query})
			goResp := tw.Go.Do(t, Request{Method: http.MethodGet, Path: tc.path, Token: goTok, Query: tc.query})
			assertNoUnexpected(t, tw, tc.name, tw.Diff.DiffHTTP(tc.name, tsResp, goResp))
		})
	}

	tsDL := tw.TS.Get(t, tsRoot, "/api/libraries/"+bookLib+"/download?ids="+saga1+","+novel)
	goDL := tw.Go.Get(t, goRoot, "/api/libraries/"+bookLib+"/download?ids="+saga1+","+novel)
	findings := tw.Diff.DiffHTTP("library-download-multiple", tsDL, goDL)
	filtered := findings[:0]
	for _, f := range findings {
		// LibraryItems-<epochMs>.zip carries a per-request millisecond timestamp.
		if f.Path == "header.Content-Disposition" && strings.Contains(f.TSValue, "LibraryItems-") && strings.Contains(f.GoValue, "LibraryItems-") {
			continue
		}
		filtered = append(filtered, f)
	}
	assertNoUnexpected(t, tw, "library-download-multiple", filtered)
}
