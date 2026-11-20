//go:build oracle

package oracle

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"testing"
	"time"
)

func createGapCustomProvider(t *testing.T, tw *Twin, step, tsRoot, goRoot, name, tsURL, goURL string) (string, string) {
	t.Helper()
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

func extractBookSearchID(t *testing.T, side string, resp Response) string {
	t.Helper()
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
	return id
}

func waitSocketEventAny(t *testing.T, sock *Socket, from int, timeout time.Duration, names ...string) SocketEvent {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		events := sock.Events()
		var seen []string
		for i := max(from, 0); i < len(events); i++ {
			seen = append(seen, events[i].Name)
			if slices.Contains(names, events[i].Name) {
				return events[i]
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("none of %v arrived on %s socket within %s; events since %d: %v", names, sock.role, timeout, from, seen)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func generateMediaWithFFmpeg(t *testing.T, path string, args ...string) []byte {
	t.Helper()
	fullArgs := append([]string{"-hide_banner", "-loglevel", "error"}, args...)
	fullArgs = append(fullArgs, "-y", path)
	if out, err := exec.Command("ffmpeg", fullArgs...).CombinedOutput(); err != nil {
		t.Fatalf("ffmpeg %s: %v\n%s", filepath.Base(path), err, string(out))
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read generated media %s: %v", path, err)
	}
	return data
}

func matchLibItemEv(itemID string, wantFinished bool) func([]json.RawMessage) bool {
	return func(args []json.RawMessage) bool {
		var ev map[string]any
		if len(args) == 0 || json.Unmarshal(args[0], &ev) != nil || ev["libraryItemId"] != itemID {
			return false
		}
		return !wantFinished || ev["isFinished"] == true
	}
}

// TestOracleGapsSocketEventsAndFeedStream covers user_offline, log listener
// streaming, cover_search_provider_error / cover_search_complete, backup_applied
// over active sockets, and GET /feed/:slug/item/:episodeId/* byte-range streaming.
func TestOracleGapsSocketEventsAndFeedStream(t *testing.T) {
	tw := StartTwin(t, Options{})
	tsRoot, goRoot := tw.TS.Token(t, "root"), tw.Go.Token(t, "root")
	tsSock, goSock := tw.WatchTS.Socket("root"), tw.WatchGo.Socket("root")
	saga1 := tw.TS.IDs["saga1"]

	// 1. user_offline: close the finisher sockets (their only open connection) and
	// observe user_offline on root sockets.
	tsOffMark, goOffMark := tsSock.Mark(), goSock.Mark()
	tw.WatchTS.Socket("finisher").Close()
	tw.WatchGo.Socket("finisher").Close()
	matchFinisher := func(args []json.RawMessage) bool {
		var u map[string]any
		return len(args) > 0 && json.Unmarshal(args[0], &u) == nil && u["username"] == "trial-finisher"
	}
	tsOff := tsSock.Wait(t, tsOffMark, "user_offline", 5*time.Second, matchFinisher)
	goOff := goSock.Wait(t, goOffMark, "user_offline", 5*time.Second, matchFinisher)
	assertNoUnexpected(t, tw, "gap-user-offline", tw.Diff.DiffSockets("gap-user-offline", []SocketEvent{tsOff}, []SocketEvent{goOff}))

	// 2. set_log_listener / log / remove_log_listener.
	tsSock.Emit(t, "set_log_listener", 2)
	goSock.Emit(t, "set_log_listener", 2)
	time.Sleep(50 * time.Millisecond)
	tsLogMark, goLogMark := tsSock.Mark(), goSock.Mark()
	assertNoUnexpected(t, tw, "gap-log-ping", tw.Diff.DiffHTTP("gap-log-ping", tw.TS.Get(t, "", "/ping"), tw.Go.Get(t, "", "/ping")))
	patchSettings := map[string]any{"scannerParseSubtitle": true}
	assertNoUnexpected(t, tw, "gap-log-settings", tw.Diff.DiffHTTP("gap-log-settings", tw.TS.Patch(t, tsRoot, "/api/settings", patchSettings), tw.Go.Patch(t, goRoot, "/api/settings", patchSettings)))
	tsSock.Wait(t, tsLogMark, "log", 5*time.Second, nil)
	goSock.Wait(t, goLogMark, "log", 5*time.Second, nil)
	tsSock.Emit(t, "remove_log_listener")
	goSock.Emit(t, "remove_log_listener")

	// 3. cover_search_provider_error / cover_search_complete on custom providers.
	tw.Stubs.SetRoute("/err-prov/search", http.StatusInternalServerError, "text/plain", []byte("boom"))
	tsErrID, goErrID := createGapCustomProvider(t, tw, "gap-err-provider", tsRoot, goRoot, "Oracle Err Cover", tw.Stubs.TSHTTPURL+"/err-prov", tw.Stubs.GoHTTPURL+"/err-prov")
	tsErrMark, goErrMark := tsSock.Mark(), goSock.Mark()
	tsSock.Emit(t, "search_covers", map[string]any{"requestId": "gap-err-1", "title": "Err Book", "author": "", "provider": "custom-" + tsErrID, "podcast": false})
	goSock.Emit(t, "search_covers", map[string]any{"requestId": "gap-err-1", "title": "Err Book", "author": "", "provider": "custom-" + goErrID, "podcast": false})
	waitSocketEventAny(t, tsSock, tsErrMark, 10*time.Second, "cover_search_provider_error", "cover_search_complete")
	waitSocketEventAny(t, goSock, goErrMark, 10*time.Second, "cover_search_provider_error", "cover_search_complete")

	// Malformed 200 payload: wait for provider error or search complete so steps 4-5 still run.
	tw.Stubs.SetRoute("/bad-prov/search", http.StatusOK, "application/json", []byte(`{"bad":true}`))
	tsBadID, goBadID := createGapCustomProvider(t, tw, "gap-bad-provider", tsRoot, goRoot, "Oracle Malformed Cover", tw.Stubs.TSHTTPURL+"/bad-prov", tw.Stubs.GoHTTPURL+"/bad-prov")
	tsBadMark, goBadMark := tsSock.Mark(), goSock.Mark()
	tsSock.Emit(t, "search_covers", map[string]any{"requestId": "gap-bad-1", "title": "Bad Book", "author": "", "provider": "custom-" + tsBadID, "podcast": false})
	goSock.Emit(t, "search_covers", map[string]any{"requestId": "gap-bad-1", "title": "Bad Book", "author": "", "provider": "custom-" + goBadID, "podcast": false})
	tsProvDone := tsSock.Wait(t, tsBadMark, "cover_search_complete", 10*time.Second, nil)
	goProvDone := goSock.Wait(t, goBadMark, "cover_search_complete", 10*time.Second, nil)
	assertNoUnexpected(t, tw, "gap-cover-provider-error", tw.Diff.DiffSockets("gap-cover-provider-error", []SocketEvent{tsProvDone}, []SocketEvent{goProvDone}))
	for _, pair := range [][2]string{{tsErrID, goErrID}, {tsBadID, goBadID}} {
		assertNoUnexpected(t, tw, "gap-del-provider", tw.Diff.DiffHTTP("gap-del-provider", tw.TS.Delete(t, tsRoot, "/api/custom-metadata-providers/"+pair[0]), tw.Go.Delete(t, goRoot, "/api/custom-metadata-providers/"+pair[1])))
	}

	// 4. backup_applied observed on active root sockets.
	tsBak := tw.TS.Post(t, tsRoot, "/api/backups", nil)
	goBak := tw.Go.Post(t, goRoot, "/api/backups", nil)
	extractBackupID := func(side string, resp Response) string {
		arr, _ := resp.Object(t)["backups"].([]any)
		if len(arr) == 0 {
			t.Fatalf("%s backups empty: %s", side, excerpt(resp.Body))
		}
		first, _ := arr[0].(map[string]any)
		id, _ := first["id"].(string)
		if id == "" {
			t.Fatalf("%s backup missing id: %s", side, excerpt(resp.Body))
		}
		return id
	}
	tsBakID, goBakID := extractBackupID("ts", tsBak), extractBackupID("go", goBak)
	tw.Diff.Corr.BindID(tsBakID, goBakID)
	assertNoUnexpected(t, tw, "gap-backup-create", tw.Diff.DiffHTTP("gap-backup-create", tsBak, goBak))

	tsApplyMark, goApplyMark := tsSock.Mark(), goSock.Mark()
	assertNoUnexpected(t, tw, "gap-backup-apply", tw.Diff.DiffHTTP("gap-backup-apply", tw.TS.Get(t, tsRoot, "/api/backups/"+tsBakID+"/apply"), tw.Go.Get(t, goRoot, "/api/backups/"+goBakID+"/apply")))
	tsSock.Wait(t, tsApplyMark, "backup_applied", 10*time.Second, nil)
	goSock.Wait(t, goApplyMark, "backup_applied", 10*time.Second, nil)
	for _, tgt := range []*Target{tw.TS, tw.Go} {
		if err := tgt.WaitReady(readyTimeout); err != nil {
			t.Fatalf("%s not ready after backup apply: %v", tgt.Name, err)
		}
	}

	// 5. GET /feed/:slug/item/:episodeId/* byte-range stream.
	feedReq := map[string]any{"serverAddress": "http://127.0.0.1:8080", "slug": "oracle-gap-feed"}
	assertNoUnexpected(t, tw, "gap-feed-open", tw.Diff.DiffHTTP("gap-feed-open", tw.TS.Post(t, tsRoot, "/api/feeds/item/"+saga1+"/open", feedReq), tw.Go.Post(t, goRoot, "/api/feeds/item/"+saga1+"/open", feedReq)))
	findFeedAndEp := func(side string, resp Response) (string, string) {
		feeds, _ := resp.Object(t)["feeds"].([]any)
		for _, entry := range feeds {
			if f, _ := entry.(map[string]any); f["slug"] == "oracle-gap-feed" {
				fid, _ := f["id"].(string)
				eps, _ := f["episodes"].([]any)
				if len(eps) > 0 {
					ep0, _ := eps[0].(map[string]any)
					if epID, _ := ep0["id"].(string); fid != "" && epID != "" {
						return fid, epID
					}
				}
			}
		}
		t.Fatalf("%s feed oracle-gap-feed missing episode: %s", side, excerpt(resp.Body))
		return "", ""
	}
	tsFeedID, tsEpID := findFeedAndEp("ts", tw.TS.Get(t, tsRoot, "/api/feeds"))
	goFeedID, goEpID := findFeedAndEp("go", tw.Go.Get(t, goRoot, "/api/feeds"))
	tw.Diff.Corr.BindID(tsFeedID, goFeedID)
	tw.Diff.Corr.BindID(tsEpID, goEpID)

	rangeReq := func(epID string) Request {
		return Request{Method: http.MethodGet, Path: "/feed/oracle-gap-feed/item/" + epID + "/media.mp3", Header: map[string]string{"Range": "bytes=0-63"}}
	}
	tsStream, goStream := tw.TS.Do(t, rangeReq(tsEpID)), tw.Go.Do(t, rangeReq(goEpID))
	if tsStream.Status != http.StatusPartialContent || goStream.Status != http.StatusPartialContent {
		t.Fatalf("feed stream range statuses: ts=%d go=%d, want 206", tsStream.Status, goStream.Status)
	}
	assertNoUnexpected(t, tw, "gap-feed-stream-range", tw.Diff.DiffHTTP("gap-feed-stream-range", tsStream, goStream))
	assertNoUnexpected(t, tw, "gap-feed-close", tw.Diff.DiffHTTP("gap-feed-close", tw.TS.Post(t, tsRoot, "/api/feeds/"+tsFeedID+"/close", nil), tw.Go.Post(t, goRoot, "/api/feeds/"+goFeedID+"/close", nil)))
}

// TestOracleGapsQueuesAndM4BCancel exercises podcast download queue/clear,
// metadata embed queuing, and mid-encode M4B cancellation.
func TestOracleGapsQueuesAndM4BCancel(t *testing.T) {
	mp3Bytes := generateSineMP3(t, filepath.Join(t.TempDir(), "slow-ep.mp3"), "Slow Ep", "Oracle Pod")
	longMP3 := generateMediaWithFFmpeg(t, filepath.Join(t.TempDir(), "long-noise.mp3"),
		"-f", "lavfi", "-i", "anoisesrc=color=pink:duration=90", "-c:a", "libmp3lame", "-b:a", "64k",
		"-metadata", "title=Long Pink Noise Book", "-metadata", "artist=Oracle Noise",
	)
	tw := StartTwin(t, Options{})
	tsRoot, goRoot := tw.TS.Token(t, "root"), tw.Go.Token(t, "root")
	tsSock, goSock := tw.WatchTS.Socket("root"), tw.WatchGo.Socket("root")
	podcastID, bookLib, saga1, saga2 := tw.TS.IDs["podcast"], tw.TS.IDs["library"], tw.TS.IDs["saga1"], tw.TS.IDs["saga2"]

	// 1. episode_download_queued + episode_download_queue_cleared.
	slowSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-time.After(2500 * time.Millisecond):
		case <-r.Context().Done():
			return
		}
		w.Header().Set("Content-Type", "audio/mpeg")
		_, _ = w.Write(mp3Bytes) // client disconnect during cleanup is ignored
	}))
	t.Cleanup(slowSrv.Close)

	makeEp := func(n, title, guid string, pubAt int64) map[string]any {
		return map[string]any{
			"title": title, "description": "Queued slow download", "pubDate": "Thu, 01 Jan 2026 00:00:00 GMT",
			"episodeType": "full", "publishedAt": pubAt, "guid": guid,
			"enclosure": map[string]any{"url": slowSrv.URL + "/slow-ep.mp3?n=" + n, "type": "audio/mpeg", "length": "4096"},
		}
	}
	dlPayload := []map[string]any{makeEp("1", "Slow Ep 1", "slow-ep-1", 1767225600000), makeEp("2", "Slow Ep 2", "slow-ep-2", 1767312000000)}
	tsPodMark, goPodMark := tsSock.Mark(), goSock.Mark()
	assertNoUnexpected(t, tw, "gap-pod-dl-queue", tw.Diff.DiffHTTP("gap-pod-dl-queue", tw.TS.Post(t, tsRoot, "/api/podcasts/"+podcastID+"/download-episodes", dlPayload), tw.Go.Post(t, goRoot, "/api/podcasts/"+podcastID+"/download-episodes", dlPayload)))
	tsSock.Wait(t, tsPodMark, "episode_download_queued", 10*time.Second, matchLibItemEv(podcastID, false))
	goSock.Wait(t, goPodMark, "episode_download_queued", 10*time.Second, matchLibItemEv(podcastID, false))

	assertNoUnexpected(t, tw, "gap-pod-clear-queue", tw.Diff.DiffHTTP("gap-pod-clear-queue", tw.TS.Get(t, tsRoot, "/api/podcasts/"+podcastID+"/clear-queue"), tw.Go.Get(t, goRoot, "/api/podcasts/"+podcastID+"/clear-queue")))
	matchCleared := func(args []json.RawMessage) bool {
		var id string
		return len(args) > 0 && json.Unmarshal(args[0], &id) == nil && id == podcastID
	}
	tsSock.Wait(t, tsPodMark, "episode_download_queue_cleared", 5*time.Second, matchCleared)
	goSock.Wait(t, goPodMark, "episode_download_queue_cleared", 5*time.Second, matchCleared)
	tsSock.Wait(t, tsPodMark, "episode_download_finished", complexTaskTimeout, matchLibItemEv(podcastID, true))
	goSock.Wait(t, goPodMark, "episode_download_finished", complexTaskTimeout, matchLibItemEv(podcastID, true))

	// 2. metadata_embed_queue_update via back-to-back embed-metadata calls.
	tsEmbedMark, goEmbedMark := tsSock.Mark(), goSock.Mark()
	assertNoUnexpected(t, tw, "gap-embed-saga1", tw.Diff.DiffHTTP("gap-embed-saga1", tw.TS.Post(t, tsRoot, "/api/tools/item/"+saga1+"/embed-metadata", nil), tw.Go.Post(t, goRoot, "/api/tools/item/"+saga1+"/embed-metadata", nil)))
	assertNoUnexpected(t, tw, "gap-embed-saga2", tw.Diff.DiffHTTP("gap-embed-saga2", tw.TS.Post(t, tsRoot, "/api/tools/item/"+saga2+"/embed-metadata", nil), tw.Go.Post(t, goRoot, "/api/tools/item/"+saga2+"/embed-metadata", nil)))
	matchEmbedQueue := func(args []json.RawMessage) bool {
		var ev map[string]any
		return len(args) > 0 && json.Unmarshal(args[0], &ev) == nil && ev["libraryItemId"] == saga2 && ev["queued"] == true
	}
	tsSock.Wait(t, tsEmbedMark, "metadata_embed_queue_update", 10*time.Second, matchEmbedQueue)
	goSock.Wait(t, goEmbedMark, "metadata_embed_queue_update", 10*time.Second, matchEmbedQueue)
	for _, id := range []string{saga1, saga2} {
		waitTaskFinished(t, tsSock, tsEmbedMark, "embed-metadata", func(d map[string]any) bool { return d["libraryItemId"] == id })
		waitTaskFinished(t, goSock, goEmbedMark, "embed-metadata", func(d map[string]any) bool { return d["libraryItemId"] == id })
	}

	// 3. DELETE /api/tools/item/:id/encode-m4b mid-encode cancel.
	for _, mediaRoot := range []string{tw.TS.Media, tw.Go.Media} {
		writeFileMkdir(t, filepath.Join(mediaRoot, "audiobooks", "Long Pink Noise Book", "Long Pink Noise Book.mp3"), longMP3)
	}
	tw.Diff.Corr.RegisterInodes(tw.TS.Media, tw.Go.Media, tw.TS.Metadata, tw.Go.Metadata)
	tsScanMark, goScanMark := tsSock.Mark(), goSock.Mark()
	assertNoUnexpected(t, tw, "gap-scan-long-book", tw.Diff.DiffHTTP("gap-scan-long-book", tw.TS.Post(t, tsRoot, "/api/libraries/"+bookLib+"/scan", nil), tw.Go.Post(t, goRoot, "/api/libraries/"+bookLib+"/scan", nil)))
	waitTaskFinished(t, tsSock, tsScanMark, "library-scan", func(d map[string]any) bool { return d["libraryId"] == bookLib })
	waitTaskFinished(t, goSock, goScanMark, "library-scan", func(d map[string]any) bool { return d["libraryId"] == bookLib })

	tsLongID := extractBookSearchID(t, "ts", tw.TS.Get(t, tsRoot, "/api/libraries/"+bookLib+"/search?q=Long+Pink+Noise+Book"))
	goLongID := extractBookSearchID(t, "go", tw.Go.Get(t, goRoot, "/api/libraries/"+bookLib+"/search?q=Long+Pink+Noise+Book"))
	tw.Diff.Corr.BindID(tsLongID, goLongID)

	tsM4bMark, goM4bMark := tsSock.Mark(), goSock.Mark()
	assertNoUnexpected(t, tw, "gap-encode-m4b-start", tw.Diff.DiffHTTP("gap-encode-m4b-start", tw.TS.Post(t, tsRoot, "/api/tools/item/"+tsLongID+"/encode-m4b", nil), tw.Go.Post(t, goRoot, "/api/tools/item/"+goLongID+"/encode-m4b", nil)))
	matchEncodeTask := func(itemID string) func([]json.RawMessage) bool {
		return func(args []json.RawMessage) bool {
			var task map[string]any
			if len(args) == 0 || json.Unmarshal(args[0], &task) != nil || task["action"] != "encode-m4b" {
				return false
			}
			data, _ := task["data"].(map[string]any)
			return data["libraryItemId"] == itemID
		}
	}
	tsSock.Wait(t, tsM4bMark, "task_started", 10*time.Second, matchEncodeTask(tsLongID))
	goSock.Wait(t, goM4bMark, "task_started", 10*time.Second, matchEncodeTask(goLongID))
	time.Sleep(150 * time.Millisecond) // allow TS AbMergeManager to register pendingTask before cancel
	assertNoUnexpected(t, tw, "gap-encode-m4b-cancel", tw.Diff.DiffHTTP("gap-encode-m4b-cancel", tw.TS.Delete(t, tsRoot, "/api/tools/item/"+tsLongID+"/encode-m4b"), tw.Go.Delete(t, goRoot, "/api/tools/item/"+goLongID+"/encode-m4b")))
	tsSock.Wait(t, tsM4bMark, "task_finished", complexTaskTimeout, matchEncodeTask(tsLongID))
	goSock.Wait(t, goM4bMark, "task_finished", complexTaskTimeout, matchEncodeTask(goLongID))
}

// TestOracleGapsPodcastMaxKeepAndMatchAll exercises podcast checknew with
// maxEpisodesToKeep/maxNewEpisodesToDownload settings and GET /api/libraries/:id/matchall.
func TestOracleGapsPodcastMaxKeepAndMatchAll(t *testing.T) {
	epMP3 := generateSineMP3(t, filepath.Join(t.TempDir(), "keep-ep.mp3"), "Keep Episode", "Keep Pod")
	bookMP3 := generateSineMP3(t, filepath.Join(t.TempDir(), "matchall.mp3"), "MatchAll Gap Book", "MatchAll Gap Author")
	tw := StartTwin(t, Options{})
	tsRoot, goRoot := tw.TS.Token(t, "root"), tw.Go.Token(t, "root")
	tsSock, goSock := tw.WatchTS.Socket("root"), tw.WatchGo.Socket("root")
	podcastID := tw.TS.IDs["podcast"]

	// 1. Podcast checknew with maxEpisodesToKeep=2, maxNewEpisodesToDownload=1, autoDownloadEpisodes=true.
	epURL := tw.Stubs.HTTPURL + "/keep-ep.mp3"
	tw.Stubs.SetRoute("/keep-ep.mp3", http.StatusOK, "audio/mpeg", epMP3)
	keepRSS := `<?xml version="1.0" encoding="UTF-8"?><rss version="2.0"><channel><title>Keep Pod</title>` +
		`<item><title>Keep Ep 1</title><pubDate>Thu, 01 Jan 2026 00:00:00 GMT</pubDate><guid isPermaLink="false">keep-1</guid><enclosure url="` + epURL + `" length="4096" type="audio/mpeg"/></item>` +
		`<item><title>Keep Ep 2</title><pubDate>Fri, 02 Jan 2026 00:00:00 GMT</pubDate><guid isPermaLink="false">keep-2</guid><enclosure url="` + epURL + `" length="4096" type="audio/mpeg"/></item>` +
		`<item><title>Keep Ep 3</title><pubDate>Sat, 03 Jan 2026 00:00:00 GMT</pubDate><guid isPermaLink="false">keep-3</guid><enclosure url="` + epURL + `" length="4096" type="audio/mpeg"/></item>` +
		`</channel></rss>`
	tw.Stubs.SetRoute("/keep-pod.xml", http.StatusOK, "application/xml", []byte(keepRSS))

	podPatch := map[string]any{
		"metadata":                 map[string]any{"feedUrl": tw.Stubs.HTTPURL + "/keep-pod.xml"},
		"maxEpisodesToKeep":        2,
		"maxNewEpisodesToDownload": 1,
		"autoDownloadEpisodes":     true,
	}
	assertNoUnexpected(t, tw, "gap-pod-patch-keep", tw.Diff.DiffHTTP("gap-pod-patch-keep", tw.TS.Patch(t, tsRoot, "/api/items/"+podcastID+"/media", podPatch), tw.Go.Patch(t, goRoot, "/api/items/"+podcastID+"/media", podPatch)))

	tsCheckMark, goCheckMark := tsSock.Mark(), goSock.Mark()
	tsCheck, goCheck := tw.TS.Get(t, tsRoot, "/api/podcasts/"+podcastID+"/checknew"), tw.Go.Get(t, goRoot, "/api/podcasts/"+podcastID+"/checknew")
	assertNoUnexpected(t, tw, "gap-pod-checknew", tw.Diff.DiffHTTP("gap-pod-checknew", tsCheck, goCheck))
	// maxNewEpisodesToDownload is 1, so wait for 1 downloaded episode on each server.
	wantDownloads := 1
	waitPodDoneCount := func(sock *Socket, from, want int) {
		deadline := time.Now().Add(complexTaskTimeout)
		for seen := 0; seen < want; {
			matched := false
			for i, ev := range sock.Events() {
				if i >= from && ev.Name == "episode_download_finished" && matchLibItemEv(podcastID, true)(ev.Args) {
					from = i + 1
					seen++
					matched = true
					break
				}
			}
			if !matched {
				if time.Now().After(deadline) {
					t.Fatalf("only %d/%d episode_download_finished on %s socket within %s", seen, want, sock.role, complexTaskTimeout)
				}
				time.Sleep(10 * time.Millisecond)
			}
		}
	}
	waitPodDoneCount(tsSock, tsCheckMark, wantDownloads)
	waitPodDoneCount(goSock, goCheckMark, wantDownloads)

	// 2. GET /api/libraries/:id/matchall on a temp library using a custom provider.
	tw.Stubs.SetRoute("/matchall/search", http.StatusOK, "application/json", []byte(`{"matches":[{"title":"MatchAll Gap Book","author":"MatchAll Gap Author","description":"Matched via matchall","publishedYear":"2024"}]}`))
	tsProvID, goProvID := createGapCustomProvider(t, tw, "gap-matchall-provider", tsRoot, goRoot, "Oracle MatchAll", tw.Stubs.TSHTTPURL+"/matchall", tw.Stubs.GoHTTPURL+"/matchall")

	tsLibFolder, goLibFolder := filepath.Join(tw.TS.Media, "matchall-books"), filepath.Join(tw.Go.Media, "matchall-books")
	writeFileMkdir(t, filepath.Join(tsLibFolder, "MatchAll Gap Book", "MatchAll Gap Book.mp3"), bookMP3)
	writeFileMkdir(t, filepath.Join(goLibFolder, "MatchAll Gap Book", "MatchAll Gap Book.mp3"), bookMP3)
	tw.Diff.Corr.RegisterInodes(tw.TS.Media, tw.Go.Media, tw.TS.Metadata, tw.Go.Metadata)

	createLibBody := func(provID, folderPath string) map[string]any {
		return map[string]any{"name": "Oracle MatchAll Library", "mediaType": "book", "icon": "database", "provider": "custom-" + provID, "folders": []map[string]any{{"fullPath": folderPath}}}
	}
	tsLibResp, goLibResp := tw.TS.Post(t, tsRoot, "/api/libraries", createLibBody(tsProvID, tsLibFolder)), tw.Go.Post(t, goRoot, "/api/libraries", createLibBody(goProvID, goLibFolder))
	extractLib := func(side string, resp Response) (string, string) {
		obj := resp.Object(t)
		libID, _ := obj["id"].(string)
		folders, _ := obj["folders"].([]any)
		if libID == "" || len(folders) == 0 {
			t.Fatalf("%s create library missing id/folders: %s", side, excerpt(resp.Body))
		}
		f0, _ := folders[0].(map[string]any)
		folderID, _ := f0["id"].(string)
		return libID, folderID
	}
	tsLibID, tsFolderID := extractLib("ts", tsLibResp)
	goLibID, goFolderID := extractLib("go", goLibResp)
	tw.Diff.Corr.BindID(tsLibID, goLibID)
	tw.Diff.Corr.BindID(tsFolderID, goFolderID)
	assertNoUnexpected(t, tw, "gap-matchall-create-lib", tw.Diff.DiffHTTP("gap-matchall-create-lib", tsLibResp, goLibResp))

	tsScanMark, goScanMark := tsSock.Mark(), goSock.Mark()
	assertNoUnexpected(t, tw, "gap-matchall-scan", tw.Diff.DiffHTTP("gap-matchall-scan", tw.TS.Post(t, tsRoot, "/api/libraries/"+tsLibID+"/scan", nil), tw.Go.Post(t, goRoot, "/api/libraries/"+goLibID+"/scan", nil)))
	waitTaskFinished(t, tsSock, tsScanMark, "library-scan", func(d map[string]any) bool { return d["libraryId"] == tsLibID })
	waitTaskFinished(t, goSock, goScanMark, "library-scan", func(d map[string]any) bool { return d["libraryId"] == goLibID })

	tsMatchMark, goMatchMark := tsSock.Mark(), goSock.Mark()
	assertNoUnexpected(t, tw, "gap-matchall-trigger", tw.Diff.DiffHTTP("gap-matchall-trigger", tw.TS.Get(t, tsRoot, "/api/libraries/"+tsLibID+"/matchall"), tw.Go.Get(t, goRoot, "/api/libraries/"+goLibID+"/matchall")))
	waitTaskFinished(t, tsSock, tsMatchMark, "library-match-all", func(d map[string]any) bool { return d["libraryId"] == tsLibID })
	waitTaskFinished(t, goSock, goMatchMark, "library-match-all", func(d map[string]any) bool { return d["libraryId"] == goLibID })
}

// TestOracleGapsEmbeddedCoverAndScannerCleanup verifies embedded MJPEG cover
// extraction during scan and author/series cleanup on force item rescan.
func TestOracleGapsEmbeddedCoverAndScannerCleanup(t *testing.T) {
	coverMP3 := generateMediaWithFFmpeg(t, filepath.Join(t.TempDir(), "book.mp3"),
		"-f", "lavfi", "-i", "sine=frequency=440:duration=2", "-f", "lavfi", "-i", "color=c=blue:s=64x64:d=0.1",
		"-map", "0:a", "-map", "1:v", "-frames:v", "1", "-c:a", "libmp3lame", "-c:v", "mjpeg",
		"-disposition:v:0", "attached_pic", "-id3v2_version", "3",
		"-metadata", "title=Embedded Cover Book", "-metadata", "artist=Dr. Jane Q. Doe Jr.",
		"-metadata", "album=Embedded Series", "-metadata", "series=Embedded Series",
	)
	tw := StartTwin(t, Options{})
	tsRoot, goRoot := tw.TS.Token(t, "root"), tw.Go.Token(t, "root")
	tsSock, goSock := tw.WatchTS.Socket("root"), tw.WatchGo.Socket("root")
	bookLib := tw.TS.IDs["library"]

	for _, mediaRoot := range []string{tw.TS.Media, tw.Go.Media} {
		writeFileMkdir(t, filepath.Join(mediaRoot, "audiobooks", "Embedded Cover Book", "book.mp3"), coverMP3)
	}
	tw.Diff.Corr.RegisterInodes(tw.TS.Media, tw.Go.Media, tw.TS.Metadata, tw.Go.Metadata)

	tsScanMark, goScanMark := tsSock.Mark(), goSock.Mark()
	assertNoUnexpected(t, tw, "gap-embedded-scan", tw.Diff.DiffHTTP("gap-embedded-scan", tw.TS.Post(t, tsRoot, "/api/libraries/"+bookLib+"/scan", nil), tw.Go.Post(t, goRoot, "/api/libraries/"+bookLib+"/scan", nil)))
	waitTaskFinished(t, tsSock, tsScanMark, "library-scan", func(d map[string]any) bool { return d["libraryId"] == bookLib })
	waitTaskFinished(t, goSock, goScanMark, "library-scan", func(d map[string]any) bool { return d["libraryId"] == bookLib })

	tsItemID := extractBookSearchID(t, "ts", tw.TS.Get(t, tsRoot, "/api/libraries/"+bookLib+"/search?q=Embedded"))
	goItemID := extractBookSearchID(t, "go", tw.Go.Get(t, goRoot, "/api/libraries/"+bookLib+"/search?q=Embedded"))
	tw.Diff.Corr.BindID(tsItemID, goItemID)
	tw.Diff.Corr.RegisterInodes(tw.TS.Media, tw.Go.Media, tw.TS.Metadata, tw.Go.Metadata)

	tsCover, goCover := tw.TS.Get(t, tsRoot, "/api/items/"+tsItemID+"/cover"), tw.Go.Get(t, goRoot, "/api/items/"+goItemID+"/cover")
	assertNoUnexpected(t, tw, "gap-embedded-cover", tw.Diff.DiffHTTP("gap-embedded-cover", tsCover, goCover))

	// Replace author & series in metadata.json so orphan cleanup removes Dr. Jane Q. Doe Jr. & Embedded Series.
	future := time.Now().Add(2 * time.Second)
	metaJSON := []byte(`{"title":"Embedded Cover Book","authors":["Replacement Gap Author"],"series":["Replacement Gap Series"]}`)
	for _, mediaRoot := range []string{tw.TS.Media, tw.Go.Media} {
		metaPath := filepath.Join(mediaRoot, "audiobooks", "Embedded Cover Book", "metadata.json")
		writeFileMkdir(t, metaPath, metaJSON)
		if err := os.Chtimes(metaPath, future, future); err != nil {
			t.Fatalf("chtimes %s: %v", metaPath, err)
		}
	}
	assertNoUnexpected(t, tw, "gap-force-rescan-cleanup", tw.Diff.DiffHTTP("gap-force-rescan-cleanup", tw.TS.Post(t, tsRoot, "/api/items/"+tsItemID+"/scan?force=1", nil), tw.Go.Post(t, goRoot, "/api/items/"+goItemID+"/scan?force=1", nil)))

	tsAfter, goAfter := tw.TS.Get(t, tsRoot, "/api/items/"+tsItemID+"?expanded=1"), tw.Go.Get(t, goRoot, "/api/items/"+goItemID+"?expanded=1")
	bindNestedID := func(resp Response, key string) string {
		media, _ := resp.Object(t)["media"].(map[string]any)
		meta, _ := media["metadata"].(map[string]any)
		arr, _ := meta[key].([]any)
		if len(arr) == 0 {
			return ""
		}
		first, _ := arr[0].(map[string]any)
		id, _ := first["id"].(string)
		return id
	}
	if tsA, goA := bindNestedID(tsAfter, "authors"), bindNestedID(goAfter, "authors"); tsA != "" && goA != "" {
		tw.Diff.Corr.BindID(tsA, goA)
	}
	if tsS, goS := bindNestedID(tsAfter, "series"), bindNestedID(goAfter, "series"); tsS != "" && goS != "" {
		tw.Diff.Corr.BindID(tsS, goS)
	}
	assertNoUnexpected(t, tw, "gap-embedded-after-cleanup", tw.Diff.DiffHTTP("gap-embedded-after-cleanup", tsAfter, goAfter))
}
