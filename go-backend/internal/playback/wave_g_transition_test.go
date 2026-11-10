package playback

import (
	"encoding/json"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"audiobookshelf-compat/internal/mediatools"
)

// TS syncLocalSession updates only currentTime, timeListening, updatedAt, date
// and dayOfWeek for an existing row (PlaybackSessionManager.js:207-220).
func TestLocalSessionRepeatUpdatesOnlyProgressFields(t *testing.T) {
	h, user, _, _ := fixture(t)
	first := `{"id":"play_local_repeat","libraryItemId":"item-one","displayTitle":"Original title","displayAuthor":"Original author","currentTime":7,"timeListening":2,"updatedAt":1000,"mediaMetadata":{"title":"Original metadata"}}`
	if w := request(h, http.MethodPost, "/api/session/local", user, first); w.Code != http.StatusOK {
		t.Fatalf("first local upload = %d %s", w.Code, w.Body.String())
	}
	var id string
	if err := h.db.QueryRow(`SELECT id FROM playbackSessions`).Scan(&id); err != nil {
		t.Fatal(err)
	}
	second := `{"id":"play_local_repeat","libraryItemId":"item-one","displayTitle":"Replacement title","displayAuthor":"Replacement author","currentTime":9,"timeListening":5,"updatedAt":2000,"mediaMetadata":{"title":"Replacement metadata"}}`
	if w := request(h, http.MethodPost, "/api/session/local", user, second); w.Code != http.StatusOK {
		t.Fatalf("repeat local upload = %d %s", w.Code, w.Body.String())
	}
	var title, author, metadata string
	var current, listening float64
	err := h.db.QueryRow(`SELECT displayTitle,displayAuthor,currentTime,timeListening,mediaMetadata FROM playbackSessions WHERE id=?`, id).
		Scan(&title, &author, &current, &listening, &metadata)
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]any
	if err := json.Unmarshal([]byte(metadata), &decoded); err != nil {
		t.Fatal(err)
	}
	if title != "Original title" || author != "Original author" || decoded["title"] != "Original metadata" {
		t.Fatalf("existing fields were replaced: title=%q author=%q metadata=%s", title, author, metadata)
	}
	if current != 9 || listening != 5 {
		t.Fatalf("progress fields = current %v listening %v; want 9 and 5", current, listening)
	}
}

// Stream.js resets a missing segment more than ten places beyond the furthest
// generated segment (Stream.js:125-144; HlsRouter.js:74-92).
func TestFarForwardMissingSegmentResetsTranscode(t *testing.T) {
	ffmpeg := filepath.Join(t.TempDir(), "ffmpeg")
	if err := os.WriteFile(ffmpeg, []byte("#!/bin/sh\nexec sleep 30\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("FFMPEG_PATH", ffmpeg)
	if !mediatools.FFmpegAvailable() {
		t.Fatal("test ffmpeg executable is unavailable")
	}
	h, user, _, _ := fixture(t)
	defer h.Close()
	if _, err := h.db.Exec(`UPDATE books SET duration=120 WHERE id='book-one'`); err != nil {
		t.Fatal(err)
	}
	var resetPayload map[string]any
	h.emit = func(userID, event string, payload any) {
		h.record(userID, event, payload)
		if event == "stream_reset" {
			resetPayload, _ = payload.(map[string]any)
		}
	}
	id, dir := startTranscoded(t, h, user)
	if err := os.WriteFile(filepath.Join(dir, "output-1.ts"), []byte("segment"), 0600); err != nil {
		t.Fatal(err)
	}
	if w := hls(h, id, "output-11.ts"); w.Code != http.StatusNotFound {
		t.Fatalf("boundary missing segment status = %d, want 404", w.Code)
	}
	for _, e := range h.emissions() {
		if e.event == "stream_reset" {
			t.Fatal("segment exactly 10 ahead of frontier reset the stream")
		}
	}
	w := hls(h, id, "output-12.ts")
	if w.Code != http.StatusNotFound {
		t.Fatalf("missing segment status = %d, want 404", w.Code)
	}
	_ = hls(h, id, "output-12.ts") // Reset already in flight; do not announce it twice.
	resets := 0
	for _, e := range h.emissions() {
		if e.event == "stream_reset" {
			resets++
		}
	}
	if resets != 1 {
		t.Fatalf("stream_reset events = %d, want one for segment 11 beyond frontier 1", resets)
	}
	if resetPayload["startTime"] != float64(72) || resetPayload["streamId"] != id {
		t.Fatalf("stream_reset payload = %#v, want startTime 72 and streamId %q", resetPayload, id)
	}
	deadline := time.Now().Add(2 * time.Second)
	for {
		h.mu.Lock()
		stream := h.sessions[id].stream
		h.mu.Unlock()
		stream.mu.Lock()
		resetting, segmentStart := stream.resetting, stream.segmentStart
		stream.mu.Unlock()
		if !resetting {
			if segmentStart != 2 {
				t.Fatalf("reset segment start = %d, want 2 after reset seek at 42s", segmentStart)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("transcode reset did not restart")
		}
		time.Sleep(time.Millisecond)
	}
	playlist, err := os.ReadFile(filepath.Join(dir, "output.m3u8"))
	if err != nil || !strings.Contains(string(playlist), "output-19.ts") {
		t.Fatalf("reset truncated the original full VOD playlist: err=%v playlist=%s", err, playlist)
	}
}

// A seek-start transcode writes its actual suffix playlist to
// final-output.m3u8. TS keeps the full output.m3u8 generated for the whole
// duration and serves it even after completion and a backward reset.
func TestCompletedSeekKeepsFullPlaylistAcrossBackwardReset(t *testing.T) {
	h, user, _, mediaPath := fixture(t)
	defer h.Close()
	ffmpeg := os.Getenv("FFMPEG_PATH")
	if ffmpeg == "" {
		var err error
		ffmpeg, err = exec.LookPath("ffmpeg")
		if err != nil {
			t.Skip("ffmpeg is unavailable")
		}
	}
	if !mediatools.FFmpegAvailable() {
		t.Skip("ffmpeg is unavailable")
	}
	mediaPath = filepath.Join(filepath.Dir(mediaPath), "test.m4a")
	if out, err := exec.Command(ffmpeg, "-hide_banner", "-loglevel", "error", "-f", "lavfi", "-i", "sine=frequency=440:sample_rate=8000", "-t", "60", "-c:a", "aac", "-b:a", "32k", "-y", mediaPath).CombinedOutput(); err != nil {
		t.Fatalf("create seek fixture: %v (%s)", err, out)
	}
	info, err := os.Stat(mediaPath)
	if err != nil {
		t.Fatal(err)
	}
	var audioFiles []map[string]any
	var encodedAudio string
	if err := h.db.QueryRow(`SELECT audioFiles FROM books WHERE id='book-one'`).Scan(&encodedAudio); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(encodedAudio), &audioFiles); err != nil {
		t.Fatal(err)
	}
	metadata := audioFiles[0]["metadata"].(map[string]any)
	metadata["filename"] = "test.m4a"
	metadata["ext"] = ".m4a"
	metadata["path"] = mediaPath
	metadata["size"] = float64(info.Size())
	audioFiles[0]["duration"] = float64(60)
	audioFiles[0]["codec"] = "aac"
	audioFiles[0]["format"] = "m4a"
	audioFiles[0]["bitRate"] = float64(32000)
	encodedAudioBytes, err := json.Marshal(audioFiles)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.db.Exec(`UPDATE books SET duration=60,audioFiles=? WHERE id='book-one'`, string(encodedAudioBytes)); err != nil {
		t.Fatal(err)
	}
	if _, err := h.db.Exec(`INSERT INTO mediaProgresses (id,mediaItemId,mediaItemType,duration,currentTime,isFinished,hideFromContinueListening,extraData,userId) VALUES ('seek-progress','book-one','book',60,42,0,0,'{}','user-one')`); err != nil {
		t.Fatal(err)
	}
	id, dir := startTranscoded(t, h, user)
	h.mu.Lock()
	stream := h.sessions[id].stream
	done := stream.done
	h.mu.Unlock()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("seek-start ffmpeg did not complete")
	}
	finalPlaylist, err := os.ReadFile(filepath.Join(dir, "final-output.m3u8"))
	if err != nil || !strings.Contains(string(finalPlaylist), "output-2.ts") {
		t.Fatalf("ffmpeg did not produce a suffix playlist: err=%v playlist=%s", err, finalPlaylist)
	}
	readPlaylist := func() string {
		t.Helper()
		w := hls(h, id, "output.m3u8")
		if w.Code != http.StatusOK {
			t.Fatalf("public playlist = %d %s", w.Code, w.Body.String())
		}
		return w.Body.String()
	}
	afterCompletion := readPlaylist()
	if w := hls(h, id, "output-1.ts"); w.Code != http.StatusNotFound {
		t.Fatalf("backward segment status = %d, want 404", w.Code)
	}
	deadline := time.Now().Add(2 * time.Second)
	for {
		stream.mu.Lock()
		resetting, segmentStart := stream.resetting, stream.segmentStart
		stream.mu.Unlock()
		if !resetting && segmentStart == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("backward transcode reset did not restart")
		}
		time.Sleep(time.Millisecond)
	}
	afterReset := readPlaylist()
	checkFull := func(stage, playlist string) {
		t.Helper()
		if !strings.Contains(playlist, "output-0.ts") || !strings.Contains(playlist, "output-9.ts") {
			t.Errorf("%s public playlist omitted full-duration segments: %s", stage, playlist)
		}
	}
	checkFull("after completion", afterCompletion)
	checkFull("after backward reset", afterReset)
}

func TestZeroStartBackwardResetDoesNotEmitStreamReset(t *testing.T) {
	ffmpeg := filepath.Join(t.TempDir(), "ffmpeg")
	if err := os.WriteFile(ffmpeg, []byte("#!/bin/sh\nexec sleep 30\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("FFMPEG_PATH", ffmpeg)
	h, _, _, _ := fixture(t)
	defer h.Close()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "files.txt"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	stream := &transcode{root: root, startTime: 42, segmentStart: 2, complete: true}
	h.sessions["zero-reset"] = &session{ID: "zero-reset", stream: stream}
	if w := hls(h, "zero-reset", "output-0.ts"); w.Code != http.StatusNotFound {
		t.Fatalf("missing segment zero status = %d, want 404", w.Code)
	}
	stream.mu.Lock()
	resetting, segmentStart, startTime, done := stream.resetting, stream.segmentStart, stream.startTime, stream.done
	stream.mu.Unlock()
	deadline := time.Now().Add(2 * time.Second)
	for resetting || segmentStart != 0 || startTime != 0 || done == nil {
		if time.Now().After(deadline) {
			t.Fatalf("segment zero did not restart the completed transcode: resetting=%v segmentStart=%d startTime=%v done=%v", resetting, segmentStart, startTime, done != nil)
		}
		time.Sleep(time.Millisecond)
		stream.mu.Lock()
		resetting, segmentStart, startTime, done = stream.resetting, stream.segmentStart, stream.startTime, stream.done
		stream.mu.Unlock()
	}
	for _, e := range h.emissions() {
		if e.event == "stream_reset" {
			t.Fatal("TS suppresses stream_reset when reset startTime is zero")
		}
	}
}
