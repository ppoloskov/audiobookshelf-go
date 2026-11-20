//go:build oracle

package oracle

import (
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

const (
	complexTaskTimeout = 30 * time.Second
	complexHLSTimeout  = 20 * time.Second
)

func assertNoUnexpected(t *testing.T, tw *Twin, step string, findings []Finding) {
	t.Helper()
	unexpected, acknowledged := tw.Diff.Filter(findings)
	if len(acknowledged) > 0 {
		t.Logf("%s: acknowledged divergences (%d):\n%s", step, len(acknowledged), FormatFindings(acknowledged))
	}
	if len(unexpected) > 0 {
		t.Errorf("%s: unexpected divergences (%d):\n%s", step, len(unexpected), FormatFindings(unexpected))
	}
}

func assertDBEqual(t *testing.T, tw *Twin, step string, skipPaths ...string) {
	t.Helper()
	findings, err := tw.Diff.DiffSQLite(
		step,
		filepath.Join(tw.TS.Config, "absdatabase.sqlite"),
		filepath.Join(tw.Go.Config, "absdatabase.sqlite"),
	)
	if err != nil {
		t.Fatalf("%s: diff sqlite: %v", step, err)
	}
	if len(skipPaths) > 0 {
		filtered := findings[:0]
		for _, f := range findings {
			skip := false
			for _, p := range skipPaths {
				if f.Path == p {
					skip = true
					break
				}
			}
			if !skip {
				filtered = append(filtered, f)
			}
		}
		findings = filtered
	}
	assertNoUnexpected(t, tw, step, findings)
}

func waitTaskFinished(t *testing.T, sock *Socket, from int, action string, matchData func(map[string]any) bool) {
	t.Helper()
	sock.Wait(t, from, "task_finished", complexTaskTimeout, func(args []json.RawMessage) bool {
		if len(args) == 0 {
			return false
		}
		var task map[string]any
		if err := json.Unmarshal(args[0], &task); err != nil {
			return false
		}
		if task["action"] != action {
			return false
		}
		if failed, _ := task["isFailed"].(bool); failed {
			t.Fatalf("task %q failed: %s", action, string(args[0]))
		}
		if finished, _ := task["isFinished"].(bool); !finished {
			return false
		}
		if matchData != nil {
			data, _ := task["data"].(map[string]any)
			return matchData(data)
		}
		return true
	})
}

func generateSineMP3(t *testing.T, path, title, artist string) []byte {
	t.Helper()
	cmd := exec.Command(
		"ffmpeg", "-hide_banner", "-loglevel", "error",
		"-f", "lavfi", "-i", "sine=frequency=440:duration=2",
		"-c:a", "libmp3lame",
		"-metadata", "title="+title,
		"-metadata", "artist="+artist,
		"-y", path,
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("ffmpeg generate mp3: %v\n%s", err, string(out))
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read generated mp3: %v", err)
	}
	return data
}

func writeFileMkdir(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

// TestOracleComplexScannerLifecycle verifies media file discovery, metadata.json
// sidecar parsing, item rescan status transitions, podcast scanning, and full
// 26-table SQLite parity between TS v2.37.2 and Go.
func TestOracleComplexScannerLifecycle(t *testing.T) {
	mp3Bytes := generateSineMP3(t, filepath.Join(t.TempDir(), "Oracle Scanned Book.mp3"), "Oracle Scanned Book", "Oracle Scan Author")
	tw := StartTwin(t, Options{})

	tsRoot := tw.TS.Token(t, "root")
	goRoot := tw.Go.Token(t, "root")
	bookLib := tw.TS.IDs["library"]
	podLib := tw.TS.IDs["podcastLibrary"]

	// Write identical MP3 + metadata.json into both book library folders.
	initialMeta := []byte(`{"title":"Oracle Scanned Book","authors":["Oracle Scan Author"]}`)
	for _, mediaRoot := range []string{tw.TS.Media, tw.Go.Media} {
		dir := filepath.Join(mediaRoot, "audiobooks", "Oracle Scanned Book")
		writeFileMkdir(t, filepath.Join(dir, "Oracle Scanned Book.mp3"), mp3Bytes)
		writeFileMkdir(t, filepath.Join(dir, "metadata.json"), initialMeta)
	}
	tw.Diff.Corr.RegisterInodes(tw.TS.Media, tw.Go.Media, tw.TS.Metadata, tw.Go.Metadata)

	tsMark := tw.WatchTS.Socket("root").Mark()
	goMark := tw.WatchGo.Socket("root").Mark()
	assertNoUnexpected(t, tw, "scan-book-library", tw.Diff.DiffHTTP(
		"scan-book-library",
		tw.TS.Post(t, tsRoot, "/api/libraries/"+bookLib+"/scan", nil),
		tw.Go.Post(t, goRoot, "/api/libraries/"+bookLib+"/scan", nil),
	))
	matchBookLib := func(data map[string]any) bool { return data["libraryId"] == bookLib }
	waitTaskFinished(t, tw.WatchTS.Socket("root"), tsMark, "library-scan", matchBookLib)
	waitTaskFinished(t, tw.WatchGo.Socket("root"), goMark, "library-scan", matchBookLib)

	tsSearch := tw.TS.Get(t, tsRoot, "/api/libraries/"+bookLib+"/search?q=Oracle+Scanned+Book")
	goSearch := tw.Go.Get(t, goRoot, "/api/libraries/"+bookLib+"/search?q=Oracle+Scanned+Book")
	assertNoUnexpected(t, tw, "search-scanned-book", tw.Diff.DiffHTTP("search-scanned-book", tsSearch, goSearch))

	extractItemID := func(side string, resp Response) string {
		obj := resp.Object(t)
		books, _ := obj["book"].([]any)
		if len(books) == 0 {
			t.Fatalf("%s search returned no book matches: %s", side, string(resp.Body))
		}
		first, _ := books[0].(map[string]any)
		li, _ := first["libraryItem"].(map[string]any)
		id, _ := li["id"].(string)
		if id == "" {
			t.Fatalf("%s search missing libraryItem.id: %s", side, string(resp.Body))
		}
		return id
	}
	tsItemID := extractItemID("ts", tsSearch)
	goItemID := extractItemID("go", goSearch)
	tw.Diff.Corr.BindID(tsItemID, goItemID)
	tw.Diff.Corr.RegisterInodes(tw.TS.Media, tw.Go.Media, tw.TS.Metadata, tw.Go.Metadata)
	assertDBEqual(t, tw, "post-book-scan-db")

	// Edit metadata.json title -> rescan item (UPDATED) -> rescan again (UPTODATE).
	updatedMeta := []byte(`{"title":"Oracle Rescanned Title","authors":["Oracle Scan Author"]}`)
	future := time.Now().Add(2 * time.Second)
	for _, mediaRoot := range []string{tw.TS.Media, tw.Go.Media} {
		metaPath := filepath.Join(mediaRoot, "audiobooks", "Oracle Scanned Book", "metadata.json")
		writeFileMkdir(t, metaPath, updatedMeta)
		if err := os.Chtimes(metaPath, future, future); err != nil {
			t.Fatalf("chtimes %s: %v", metaPath, err)
		}
	}

	tsRescan1 := tw.TS.Post(t, tsRoot, "/api/items/"+tsItemID+"/scan", nil)
	goRescan1 := tw.Go.Post(t, goRoot, "/api/items/"+goItemID+"/scan", nil)
	assertNoUnexpected(t, tw, "item-rescan-updated", tw.Diff.DiffHTTP("item-rescan-updated", tsRescan1, goRescan1))
	if got := tsRescan1.Object(t)["result"]; got != "UPDATED" {
		t.Fatalf("first item rescan result = %v, want UPDATED", got)
	}

	tsRescan2 := tw.TS.Post(t, tsRoot, "/api/items/"+tsItemID+"/scan", nil)
	goRescan2 := tw.Go.Post(t, goRoot, "/api/items/"+goItemID+"/scan", nil)
	assertNoUnexpected(t, tw, "item-rescan-uptodate", tw.Diff.DiffHTTP("item-rescan-uptodate", tsRescan2, goRescan2))
	if got := tsRescan2.Object(t)["result"]; got != "UPTODATE" {
		t.Fatalf("second item rescan result = %v, want UPTODATE", got)
	}

	// Scan podcast library and wait for task_finished on both.
	tsPodMark := tw.WatchTS.Socket("root").Mark()
	goPodMark := tw.WatchGo.Socket("root").Mark()
	assertNoUnexpected(t, tw, "scan-podcast-library", tw.Diff.DiffHTTP(
		"scan-podcast-library",
		tw.TS.Post(t, tsRoot, "/api/libraries/"+podLib+"/scan", nil),
		tw.Go.Post(t, goRoot, "/api/libraries/"+podLib+"/scan", nil),
	))
	matchPodLib := func(data map[string]any) bool { return data["libraryId"] == podLib }
	waitTaskFinished(t, tw.WatchTS.Socket("root"), tsPodMark, "library-scan", matchPodLib)
	waitTaskFinished(t, tw.WatchGo.Socket("root"), goPodMark, "library-scan", matchPodLib)

	// Delete the generated MP3 file -> scan -> verify item marked isMissing via filter=issues -> force rescan.
	for _, mediaRoot := range []string{tw.TS.Media, tw.Go.Media} {
		mp3Path := filepath.Join(mediaRoot, "audiobooks", "Oracle Scanned Book", "Oracle Scanned Book.mp3")
		if err := os.Remove(mp3Path); err != nil {
			t.Fatalf("remove %s: %v", mp3Path, err)
		}
	}

	tsMissMark := tw.WatchTS.Socket("root").Mark()
	goMissMark := tw.WatchGo.Socket("root").Mark()
	assertNoUnexpected(t, tw, "scan-missing-mp3", tw.Diff.DiffHTTP(
		"scan-missing-mp3",
		tw.TS.Post(t, tsRoot, "/api/libraries/"+bookLib+"/scan", nil),
		tw.Go.Post(t, goRoot, "/api/libraries/"+bookLib+"/scan", nil),
	))
	waitTaskFinished(t, tw.WatchTS.Socket("root"), tsMissMark, "library-scan", matchBookLib)
	waitTaskFinished(t, tw.WatchGo.Socket("root"), goMissMark, "library-scan", matchBookLib)

	tsIssues := tw.TS.Get(t, tsRoot, "/api/libraries/"+bookLib+"/items?filter=issues")
	goIssues := tw.Go.Get(t, goRoot, "/api/libraries/"+bookLib+"/items?filter=issues")
	assertNoUnexpected(t, tw, "library-issues-after-delete", tw.Diff.DiffHTTP("library-issues-after-delete", tsIssues, goIssues))
	issueResults, _ := tsIssues.Object(t)["results"].([]any)
	foundMissing := false
	for _, entry := range issueResults {
		it, _ := entry.(map[string]any)
		if it["id"] == tsItemID {
			if missing, _ := it["isMissing"].(bool); !missing {
				t.Fatalf("expected item %s isMissing=true, got %v", tsItemID, it["isMissing"])
			}
			foundMissing = true
		}
	}
	if !foundMissing {
		t.Fatalf("expected item %s in ?filter=issues results: %s", tsItemID, excerpt(tsIssues.Body))
	}

	tsForceMark := tw.WatchTS.Socket("root").Mark()
	goForceMark := tw.WatchGo.Socket("root").Mark()
	assertNoUnexpected(t, tw, "scan-force-rescan", tw.Diff.DiffHTTP(
		"scan-force-rescan",
		tw.TS.Post(t, tsRoot, "/api/libraries/"+bookLib+"/scan?force=1", nil),
		tw.Go.Post(t, goRoot, "/api/libraries/"+bookLib+"/scan?force=1", nil),
	))
	waitTaskFinished(t, tw.WatchTS.Socket("root"), tsForceMark, "library-scan", matchBookLib)
	waitTaskFinished(t, tw.WatchGo.Socket("root"), goForceMark, "library-scan", matchBookLib)

	assertNoUnexpected(t, tw, "remove-metadata-after-scan", tw.Diff.DiffHTTP(
		"remove-metadata-after-scan",
		tw.TS.Post(t, tsRoot, "/api/libraries/"+bookLib+"/remove-metadata", nil),
		tw.Go.Post(t, goRoot, "/api/libraries/"+bookLib+"/remove-metadata", nil),
	))
}

// TestOracleComplexPodcastDownloadAndM4B exercises podcast episode downloading
// from a local stub MP3/RSS feed (PodcastManager) and audiobook M4B encoding
// and cancellation (AbMergeManager + ffmpegHelpers).
func TestOracleComplexPodcastDownloadAndM4B(t *testing.T) {
	mp3Bytes := generateSineMP3(t, filepath.Join(t.TempDir(), "stub-ep.mp3"), "Stub Podcast Episode", "Oracle Podcast")
	tw := StartTwin(t, Options{})

	tsRoot := tw.TS.Token(t, "root")
	goRoot := tw.Go.Token(t, "root")
	podcastID := tw.TS.IDs["podcast"]
	saga1 := tw.TS.IDs["saga1"]

	epURL := tw.Stubs.HTTPURL + "/ep.mp3"
	tw.Stubs.SetRoute("/ep.mp3", 200, "audio/mpeg", mp3Bytes)
	rssXML := `<?xml version="1.0" encoding="UTF-8"?><rss version="2.0" xmlns:itunes="http://www.itunes.com/dtds/podcast-1.0.dtd"><channel><title>Stub Pod</title><item><title>Oracle Stub Episode</title><description>Downloaded from local stub</description><pubDate>Wed, 03 Jan 2024 00:00:00 GMT</pubDate><guid isPermaLink="false">oracle-stub-ep-1</guid><enclosure url="` + epURL + `" length="4096" type="audio/mpeg"/><itunes:episode>99</itunes:episode><itunes:season>1</itunes:season></item></channel></rss>`
	tw.Stubs.SetRoute("/pod.xml", 200, "application/xml", []byte(rssXML))

	tsBefore := tw.TS.Get(t, tsRoot, "/api/items/"+podcastID+"?expanded=1")
	goBefore := tw.Go.Get(t, goRoot, "/api/items/"+podcastID+"?expanded=1")
	assertNoUnexpected(t, tw, "podcast-before-download", tw.Diff.DiffHTTP("podcast-before-download", tsBefore, goBefore))

	episodeIDs := func(resp Response) map[string]struct{} {
		media, _ := resp.Object(t)["media"].(map[string]any)
		eps, _ := media["episodes"].([]any)
		out := make(map[string]struct{}, len(eps))
		for _, e := range eps {
			m, _ := e.(map[string]any)
			if id, _ := m["id"].(string); id != "" {
				out[id] = struct{}{}
			}
		}
		return out
	}
	tsBeforeIDs := episodeIDs(tsBefore)
	goBeforeIDs := episodeIDs(goBefore)

	assertNoUnexpected(t, tw, "podcast-fetch-stub-feed", tw.Diff.DiffHTTP(
		"podcast-fetch-stub-feed",
		tw.TS.Post(t, tsRoot, "/api/podcasts/feed", map[string]any{"rssFeed": tw.Stubs.TSHTTPURL + "/pod.xml"}),
		tw.Go.Post(t, goRoot, "/api/podcasts/feed", map[string]any{"rssFeed": tw.Stubs.GoHTTPURL + "/pod.xml"}),
	))

	tsPodMark := tw.WatchTS.Socket("root").Mark()
	goPodMark := tw.WatchGo.Socket("root").Mark()
	dlPayload := []map[string]any{{
		"title":       "Oracle Stub Episode",
		"subtitle":    "Stub Subtitle",
		"description": "Downloaded from local stub",
		"pubDate":     "Wed, 03 Jan 2024 00:00:00 GMT",
		"episodeType": "full",
		"season":      "1",
		"episode":     "99",
		"publishedAt": 1704240000000,
		"guid":        "oracle-stub-ep-1",
		"enclosure": map[string]any{
			"url":    epURL,
			"type":   "audio/mpeg",
			"length": "4096",
		},
	}}
	assertNoUnexpected(t, tw, "podcast-download-episodes", tw.Diff.DiffHTTP(
		"podcast-download-episodes",
		tw.TS.Post(t, tsRoot, "/api/podcasts/"+podcastID+"/download-episodes", dlPayload),
		tw.Go.Post(t, goRoot, "/api/podcasts/"+podcastID+"/download-episodes", dlPayload),
	))

	waitEpisodeFinished := func(sock *Socket, from int) {
		sock.Wait(t, from, "episode_download_finished", complexTaskTimeout, func(args []json.RawMessage) bool {
			if len(args) == 0 {
				return false
			}
			var ev map[string]any
			if err := json.Unmarshal(args[0], &ev); err != nil {
				return false
			}
			if ev["libraryItemId"] != podcastID {
				return false
			}
			if failed, _ := ev["failed"].(bool); failed {
				t.Fatalf("episode download failed: %s", string(args[0]))
			}
			finished, _ := ev["isFinished"].(bool)
			return finished
		})
	}
	waitEpisodeFinished(tw.WatchTS.Socket("root"), tsPodMark)
	waitEpisodeFinished(tw.WatchGo.Socket("root"), goPodMark)

	tsAfter := tw.TS.Get(t, tsRoot, "/api/items/"+podcastID+"?expanded=1")
	goAfter := tw.Go.Get(t, goRoot, "/api/items/"+podcastID+"?expanded=1")
	findNewEpisode := func(side string, resp Response, old map[string]struct{}) string {
		for id := range episodeIDs(resp) {
			if _, ok := old[id]; !ok {
				return id
			}
		}
		t.Fatalf("%s did not add a new episode after download: %s", side, excerpt(resp.Body))
		return ""
	}
	tsNewEpID := findNewEpisode("ts", tsAfter, tsBeforeIDs)
	goNewEpID := findNewEpisode("go", goAfter, goBeforeIDs)
	tw.Diff.Corr.BindID(tsNewEpID, goNewEpID)
	tw.Diff.Corr.RegisterInodes(tw.TS.Media, tw.Go.Media, tw.TS.Metadata, tw.Go.Metadata)
	assertNoUnexpected(t, tw, "podcast-after-download", tw.Diff.DiffHTTP("podcast-after-download", tsAfter, goAfter))

	// Encode saga1 to M4B and wait for task completion on both servers.
	tsM4bMark := tw.WatchTS.Socket("root").Mark()
	goM4bMark := tw.WatchGo.Socket("root").Mark()
	assertNoUnexpected(t, tw, "encode-m4b-start", tw.Diff.DiffHTTP(
		"encode-m4b-start",
		tw.TS.Post(t, tsRoot, "/api/tools/item/"+saga1+"/encode-m4b?bitrate=64k", nil),
		tw.Go.Post(t, goRoot, "/api/tools/item/"+saga1+"/encode-m4b?bitrate=64k", nil),
	))
	waitTaskFinished(t, tw.WatchTS.Socket("root"), tsM4bMark, "encode-m4b", nil)
	waitTaskFinished(t, tw.WatchGo.Socket("root"), goM4bMark, "encode-m4b", nil)

	// Cancel path when no encode task is running returns 404 on both servers.
	assertNoUnexpected(t, tw, "encode-m4b-cancel-idle", tw.Diff.DiffHTTP(
		"encode-m4b-cancel-idle",
		tw.TS.Delete(t, tsRoot, "/api/tools/item/"+saga1+"/encode-m4b"),
		tw.Go.Delete(t, goRoot, "/api/tools/item/"+saga1+"/encode-m4b"),
	))
}

// TestOracleComplexHLSPlaybackAndTools exercises forced HLS transcoding,
// live M3U8 + MPEG-TS segment streaming, session close, and embed-metadata tool execution.
func TestOracleComplexHLSPlaybackAndTools(t *testing.T) {
	tw := StartTwin(t, Options{})
	tsRoot := tw.TS.Token(t, "root")
	goRoot := tw.Go.Token(t, "root")
	saga1 := tw.TS.IDs["saga1"]

	playBody := map[string]any{
		"forceTranscode":     true,
		"forceDirectPlay":    false,
		"supportedMimeTypes": []string{},
		"mediaPlayer":        "html5",
		"deviceInfo":         map[string]any{"clientName": "OracleHLS"},
	}
	tsPlay := tw.TS.Post(t, tsRoot, "/api/items/"+saga1+"/play", playBody)
	goPlay := tw.Go.Post(t, goRoot, "/api/items/"+saga1+"/play", playBody)

	extractHLS := func(side string, resp Response) (sessID, contentURL string) {
		if resp.Status != http.StatusOK {
			t.Fatalf("%s play status = %d: %s", side, resp.Status, excerpt(resp.Body))
		}
		obj := resp.Object(t)
		if pm, _ := obj["playMethod"].(float64); pm != 2 {
			t.Fatalf("%s playMethod = %v, want 2 (Transcode)", side, obj["playMethod"])
		}
		sessID, _ = obj["id"].(string)
		tracks, _ := obj["audioTracks"].([]any)
		if len(tracks) == 0 {
			t.Fatalf("%s missing audioTracks: %s", side, excerpt(resp.Body))
		}
		first, _ := tracks[0].(map[string]any)
		contentURL, _ = first["contentUrl"].(string)
		if !strings.HasPrefix(contentURL, "/hls/") || !strings.HasSuffix(contentURL, "/output.m3u8") {
			t.Fatalf("%s unexpected contentUrl %q", side, contentURL)
		}
		return sessID, contentURL
	}
	tsSessID, tsPlaylistPath := extractHLS("ts", tsPlay)
	goSessID, goPlaylistPath := extractHLS("go", goPlay)
	tw.Diff.Corr.BindID(tsSessID, goSessID)

	fetchHLS := func(tgt *Target, playlistPath string) {
		deadline := time.Now().Add(complexHLSTimeout)
		var playlist string
		for {
			resp := tgt.Get(t, "", playlistPath)
			if resp.Status == http.StatusOK && strings.Contains(string(resp.Body), ".ts") {
				playlist = string(resp.Body)
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("%s HLS playlist %s did not yield a .ts segment within %s (last status %d)", tgt.Name, playlistPath, complexHLSTimeout, resp.Status)
			}
			time.Sleep(100 * time.Millisecond)
		}
		if !strings.HasPrefix(strings.TrimSpace(playlist), "#EXTM3U") {
			t.Fatalf("%s invalid m3u8 playlist:\n%s", tgt.Name, playlist)
		}
		var segLine string
		for _, line := range strings.Split(playlist, "\n") {
			trimmed := strings.TrimSpace(line)
			if trimmed != "" && !strings.HasPrefix(trimmed, "#") && strings.Contains(trimmed, ".ts") {
				segLine = trimmed
				break
			}
		}
		baseURL, err := url.Parse(playlistPath)
		if err != nil {
			t.Fatalf("parse playlist path %q: %v", playlistPath, err)
		}
		segRef, err := url.Parse(segLine)
		if err != nil {
			t.Fatalf("parse segment %q: %v", segLine, err)
		}
		segPath := baseURL.ResolveReference(segRef).Path
		var segResp Response
		for {
			segResp = tgt.Get(t, "", segPath)
			if segResp.Status == http.StatusOK {
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("%s HLS segment %s status = %d within %s", tgt.Name, segPath, segResp.Status, complexHLSTimeout)
			}
			time.Sleep(100 * time.Millisecond)
		}
		if len(segResp.Body) == 0 || segResp.Body[0] != 0x47 {
			t.Fatalf("%s HLS segment missing MPEG-TS sync byte 0x47 (len=%d)", tgt.Name, len(segResp.Body))
		}
	}
	fetchHLS(tw.TS, tsPlaylistPath)
	fetchHLS(tw.Go, goPlaylistPath)

	assertNoUnexpected(t, tw, "close-hls-session", tw.Diff.DiffHTTP(
		"close-hls-session",
		tw.TS.Post(t, tsRoot, "/api/session/"+tsSessID+"/close", map[string]any{}),
		tw.Go.Post(t, goRoot, "/api/session/"+goSessID+"/close", map[string]any{}),
	))

	tsMark := tw.WatchTS.Socket("root").Mark()
	goMark := tw.WatchGo.Socket("root").Mark()
	assertNoUnexpected(t, tw, "embed-metadata", tw.Diff.DiffHTTP(
		"embed-metadata",
		tw.TS.Post(t, tsRoot, "/api/tools/item/"+saga1+"/embed-metadata", nil),
		tw.Go.Post(t, goRoot, "/api/tools/item/"+saga1+"/embed-metadata", nil),
	))
	waitTaskFinished(t, tw.WatchTS.Socket("root"), tsMark, "embed-metadata", nil)
	waitTaskFinished(t, tw.WatchGo.Socket("root"), goMark, "embed-metadata", nil)
}

// TestOracleComplexSocketIOActions verifies live inbound Socket.IO commands
// (ping/pong, admin broadcast, log listener toggle, and bad-token rejection).
func TestOracleComplexSocketIOActions(t *testing.T) {
	tw := StartTwin(t, Options{})

	// 1. ping -> pong
	tsRootSock := tw.WatchTS.Socket("root")
	goRootSock := tw.WatchGo.Socket("root")
	tsPingMark := tsRootSock.Mark()
	goPingMark := goRootSock.Mark()
	tsRootSock.Emit(t, "ping")
	goRootSock.Emit(t, "ping")
	tsPong := tsRootSock.Wait(t, tsPingMark, "pong", 5*time.Second, nil)
	goPong := goRootSock.Wait(t, goPingMark, "pong", 5*time.Second, nil)
	assertNoUnexpected(t, tw, "socket-ping-pong", tw.Diff.DiffSockets("socket-ping-pong", []SocketEvent{tsPong}, []SocketEvent{goPong}))

	// 2. message_all_users -> admin_message on listener + all authenticated sockets
	tsMark := tw.WatchTS.Mark()
	goMark := tw.WatchGo.Mark()
	payload := map[string]string{"message": "Oracle broadcast"}
	tsRootSock.Emit(t, "message_all_users", payload)
	goRootSock.Emit(t, "message_all_users", payload)
	tw.WatchTS.Socket("listener").Wait(t, tsMark["listener"], "admin_message", 5*time.Second, nil)
	tw.WatchGo.Socket("listener").Wait(t, goMark["listener"], "admin_message", 5*time.Second, nil)
	assertNoUnexpected(t, tw, "socket-message-all-users", tw.Diff.DiffSockets(
		"socket-message-all-users",
		tw.WatchTS.Since(tsMark, 50*time.Millisecond),
		tw.WatchGo.Since(goMark, 50*time.Millisecond),
	))

	// 3. set_log_listener & remove_log_listener keep root socket healthy
	tsRootSock.Emit(t, "set_log_listener", 2)
	goRootSock.Emit(t, "set_log_listener", 2)
	tsRootSock.Emit(t, "remove_log_listener")
	goRootSock.Emit(t, "remove_log_listener")
	tsPingMark2 := tsRootSock.Mark()
	goPingMark2 := goRootSock.Mark()
	tsRootSock.Emit(t, "ping")
	goRootSock.Emit(t, "ping")
	tsRootSock.Wait(t, tsPingMark2, "pong", 5*time.Second, nil)
	goRootSock.Wait(t, goPingMark2, "pong", 5*time.Second, nil)

	// 4. unauthenticated socket emitting auth with bad-token -> auth_failed
	tsUnauth := tw.TS.Socket(t, "unauth", "")
	goUnauth := tw.Go.Socket(t, "unauth", "")
	tsUnauth.Emit(t, "auth", "bad-token")
	goUnauth.Emit(t, "auth", "bad-token")
	tsFail := tsUnauth.Wait(t, 0, "auth_failed", 5*time.Second, nil)
	goFail := goUnauth.Wait(t, 0, "auth_failed", 5*time.Second, nil)
	assertNoUnexpected(t, tw, "socket-auth-failed", tw.Diff.DiffSockets("socket-auth-failed", []SocketEvent{tsFail}, []SocketEvent{goFail}))
}

// TestOracleComplexFreshInitAndOIDC verifies first-boot initialization (/status,
// /init, /login, full SQLite parity) and OpenID Connect discovery + authorize redirect.
func TestOracleComplexFreshInitAndOIDC(t *testing.T) {
	t.Run("fresh-init", func(t *testing.T) {
		tw := StartTwin(t, Options{Fresh: true, NoSockets: true})

		tsStatus0 := tw.TS.Get(t, "", "/status")
		goStatus0 := tw.Go.Get(t, "", "/status")
		assertNoUnexpected(t, tw, "fresh-status-before", tw.Diff.DiffHTTP("fresh-status-before", tsStatus0, goStatus0))
		if isInit, _ := tsStatus0.Object(t)["isInit"].(bool); isInit {
			t.Fatalf("expected fresh server isInit=false, got %s", string(tsStatus0.Body))
		}

		initBody := map[string]any{
			"newRoot": map[string]any{
				"username": "freshroot",
				"password": "freshpassword123",
			},
		}
		assertNoUnexpected(t, tw, "fresh-init", tw.Diff.DiffHTTP(
			"fresh-init",
			tw.TS.Post(t, "", "/init", initBody),
			tw.Go.Post(t, "", "/init", initBody),
		))

		tsStatus1 := tw.TS.Get(t, "", "/status")
		goStatus1 := tw.Go.Get(t, "", "/status")
		assertNoUnexpected(t, tw, "fresh-status-after", tw.Diff.DiffHTTP("fresh-status-after", tsStatus1, goStatus1))
		if isInit, _ := tsStatus1.Object(t)["isInit"].(bool); !isInit {
			t.Fatalf("expected initialized server isInit=true, got %s", string(tsStatus1.Body))
		}

		loginBody := map[string]string{"username": "freshroot", "password": "freshpassword123"}
		assertNoUnexpected(t, tw, "fresh-login", tw.Diff.DiffHTTP(
			"fresh-login",
			tw.TS.Post(t, "", "/login", loginBody),
			tw.Go.Post(t, "", "/login", loginBody),
		))

		assertDBEqual(t, tw, "fresh-init-db", "settings[0].value.tokenSecret")
	})

	t.Run("oidc-config-and-authorize", func(t *testing.T) {
		key, err := rsa.GenerateKey(rand.Reader, 2048)
		if err != nil {
			t.Fatalf("generate rsa key: %v", err)
		}
		var srv *httptest.Server
		mux := http.NewServeMux()
		mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"issuer":                                srv.URL,
				"authorization_endpoint":                srv.URL + "/authorize",
				"token_endpoint":                        srv.URL + "/token",
				"userinfo_endpoint":                     srv.URL + "/userinfo",
				"end_session_endpoint":                  srv.URL + "/logout",
				"jwks_uri":                              srv.URL + "/jwks",
				"id_token_signing_alg_values_supported": []string{"RS256"},
				"code_challenge_methods_supported":      []string{"S256"},
			})
		})
		mux.HandleFunc("/jwks", func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"keys": []map[string]any{{
					"kty": "RSA",
					"use": "sig",
					"alg": "RS256",
					"kid": "oracle-oidc-key",
					"n":   base64.RawURLEncoding.EncodeToString(key.PublicKey.N.Bytes()),
					"e":   base64.RawURLEncoding.EncodeToString(big.NewInt(int64(key.PublicKey.E)).Bytes()),
				}},
			})
		})
		srv = httptest.NewServer(mux)
		defer srv.Close()

		tw := StartTwin(t, Options{NoSockets: true})
		tsRoot := tw.TS.Token(t, "root")
		goRoot := tw.Go.Token(t, "root")

		authSettings := map[string]any{
			"authActiveAuthMethods":      []string{"local", "openid"},
			"authOpenIDIssuerURL":        srv.URL,
			"authOpenIDAuthorizationURL": srv.URL + "/authorize",
			"authOpenIDTokenURL":         srv.URL + "/token",
			"authOpenIDUserInfoURL":      srv.URL + "/userinfo",
			"authOpenIDJwksURL":          srv.URL + "/jwks",
			"authOpenIDClientID":         "oracle-oidc-client",
			"authOpenIDClientSecret":     "oracle-oidc-secret",
		}
		assertNoUnexpected(t, tw, "oidc-patch-auth-settings", tw.Diff.DiffHTTP(
			"oidc-patch-auth-settings",
			tw.TS.Patch(t, tsRoot, "/api/auth-settings", authSettings),
			tw.Go.Patch(t, goRoot, "/api/auth-settings", authSettings),
		))

		issuerQuery := url.Values{"issuer": {srv.URL}}
		assertNoUnexpected(t, tw, "oidc-config", tw.Diff.DiffHTTP(
			"oidc-config",
			tw.TS.Do(t, Request{Path: "/auth/openid/config", Token: tsRoot, Query: issuerQuery}),
			tw.Go.Do(t, Request{Path: "/auth/openid/config", Token: goRoot, Query: issuerQuery}),
		))

		tsAuth := tw.TS.Do(t, Request{Path: "/auth/openid", Query: url.Values{"callback": {"/audiobookshelf/"}}})
		goAuth := tw.Go.Do(t, Request{Path: "/auth/openid", Query: url.Values{"callback": {"/audiobookshelf/"}}})
		if tsAuth.Status != http.StatusFound || goAuth.Status != http.StatusFound {
			t.Fatalf("GET /auth/openid statuses: ts=%d go=%d, want 302", tsAuth.Status, goAuth.Status)
		}
		tsLoc, err := url.Parse(tsAuth.Header.Get("Location"))
		if err != nil {
			t.Fatalf("parse ts Location: %v", err)
		}
		goLoc, err := url.Parse(goAuth.Header.Get("Location"))
		if err != nil {
			t.Fatalf("parse go Location: %v", err)
		}
		if tsLoc.Scheme+"://"+tsLoc.Host+tsLoc.Path != srv.URL+"/authorize" ||
			goLoc.Scheme+"://"+goLoc.Host+goLoc.Path != srv.URL+"/authorize" {
			t.Fatalf("authorize redirect base mismatch: ts=%q go=%q", tsLoc.String(), goLoc.String())
		}
		tsQ, goQ := tsLoc.Query(), goLoc.Query()
		for _, param := range []string{"response_type", "client_id", "scope", "code_challenge_method"} {
			if tsQ.Get(param) == "" || tsQ.Get(param) != goQ.Get(param) {
				t.Fatalf("authorize query param %q mismatch: ts=%q go=%q", param, tsQ.Get(param), goQ.Get(param))
			}
		}
		if tsQ.Get("state") == "" || goQ.Get("state") == "" || tsQ.Get("code_challenge") == "" || goQ.Get("code_challenge") == "" {
			t.Fatalf("authorize missing state/code_challenge: ts=%v go=%v", tsQ, goQ)
		}
	})
}

// TestOracleComplexConcurrency verifies concurrent disjoint PATCH /api/settings
// atomic merging and refresh token rotation grace-period replay semantics.
func TestOracleComplexConcurrency(t *testing.T) {
	tw := StartTwin(t, Options{NoSockets: true})
	tsRoot := tw.TS.Token(t, "root")
	goRoot := tw.Go.Token(t, "root")

	var wg sync.WaitGroup
	var tsResp1, tsResp2, goResp1, goResp2 Response
	wg.Add(4)
	go func() {
		defer wg.Done()
		tsResp1 = tw.TS.Patch(t, tsRoot, "/api/settings", map[string]any{"scannerParseSubtitle": true})
	}()
	go func() {
		defer wg.Done()
		tsResp2 = tw.TS.Patch(t, tsRoot, "/api/settings", map[string]any{"sortingIgnorePrefix": true})
	}()
	go func() {
		defer wg.Done()
		goResp1 = tw.Go.Patch(t, goRoot, "/api/settings", map[string]any{"scannerParseSubtitle": true})
	}()
	go func() {
		defer wg.Done()
		goResp2 = tw.Go.Patch(t, goRoot, "/api/settings", map[string]any{"sortingIgnorePrefix": true})
	}()
	wg.Wait()

	for name, r := range map[string]Response{"ts-1": tsResp1, "ts-2": tsResp2, "go-1": goResp1, "go-2": goResp2} {
		if r.Status != http.StatusOK {
			t.Fatalf("concurrent PATCH /api/settings %s status = %d: %s", name, r.Status, excerpt(r.Body))
		}
	}

	assertNoUnexpected(t, tw, "concurrent-settings-authorize", tw.Diff.DiffHTTP(
		"concurrent-settings-authorize",
		tw.TS.Post(t, tsRoot, "/api/authorize", nil),
		tw.Go.Post(t, goRoot, "/api/authorize", nil),
	))
	assertDBEqual(t, tw, "concurrent-settings-db")

	// Refresh token rotation + immediate grace-period replay.
	loginReq := func(tgt *Target) Response {
		u := tgt.users["listener"]
		return tgt.Do(t, Request{
			Method: http.MethodPost,
			Path:   "/login",
			Header: map[string]string{"x-return-tokens": "true"},
			JSON:   map[string]string{"username": u.Username, "password": u.Password},
		})
	}
	tsLogin := loginReq(tw.TS)
	goLogin := loginReq(tw.Go)
	assertNoUnexpected(t, tw, "grace-login", tw.Diff.DiffHTTP("grace-login", tsLogin, goLogin))

	tsUser, _ := tsLogin.Object(t)["user"].(map[string]any)
	goUser, _ := goLogin.Object(t)["user"].(map[string]any)
	tsRefresh, _ := tsUser["refreshToken"].(string)
	goRefresh, _ := goUser["refreshToken"].(string)
	if tsRefresh == "" || goRefresh == "" {
		t.Fatalf("missing refreshToken in x-return-tokens login: ts=%q go=%q", tsRefresh, goRefresh)
	}

	tsRot1 := tw.TS.Do(t, Request{Method: http.MethodPost, Path: "/auth/refresh", Header: map[string]string{"x-refresh-token": tsRefresh}})
	goRot1 := tw.Go.Do(t, Request{Method: http.MethodPost, Path: "/auth/refresh", Header: map[string]string{"x-refresh-token": goRefresh}})
	assertNoUnexpected(t, tw, "refresh-rotate-1", tw.Diff.DiffHTTP("refresh-rotate-1", tsRot1, goRot1))

	tsGrace := tw.TS.Do(t, Request{Method: http.MethodPost, Path: "/auth/refresh", Header: map[string]string{"x-refresh-token": tsRefresh}})
	goGrace := tw.Go.Do(t, Request{Method: http.MethodPost, Path: "/auth/refresh", Header: map[string]string{"x-refresh-token": goRefresh}})
	assertNoUnexpected(t, tw, "refresh-grace-replay", tw.Diff.DiffHTTP("refresh-grace-replay", tsGrace, goGrace))
}
