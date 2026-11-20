package playback

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"monstrobook/internal/db"
)

// The oracle is server/objects/Stream.js:219,343-376: short successful
// transcodes open once, do not emit ready, and close only when requested.
func TestStreamLifecycleRealCompletion(t *testing.T) {
	binary, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Fatal(err)
	}
	e := newEnv(t)
	if output, err := exec.Command(binary, "-hide_banner", "-loglevel", "error", "-f", "lavfi", "-i", "sine=duration=1", "-c:a", "aac", "-f", "adts", "-y", e.media).CombinedOutput(); err != nil {
		t.Fatalf("generate media: %v: %s", err, output)
	}
	t.Setenv("FFMPEG_PATH", binary)
	id, dir := e.startTranscoded(t)
	st := e.streamOf(id)
	select {
	case <-st.done:
	case <-time.After(10 * time.Second):
		t.Fatal("ffmpeg did not exit")
	}
	if n := e.events("stream_open"); n != 1 {
		t.Errorf("stream_open = %d, want 1", n)
	}
	if n := e.events("stream_progress"); n != 1 {
		t.Errorf("initial progress count = %d, want 1", n)
	}
	e.mu.Lock()
	openJSON, openErr := json.Marshal(e.payload["stream_open"])
	progressJSON, progressErr := json.Marshal(e.payload["stream_progress"])
	e.mu.Unlock()
	if openErr != nil || progressErr != nil {
		t.Fatalf("encode event: %v, %v", openErr, progressErr)
	}
	var opened struct {
		ID                 string  `json:"id"`
		UserID             string  `json:"userId"`
		SegmentLength      int     `json:"segmentLength"`
		PlaylistPath       string  `json:"playlistPath"`
		ClientPlaylistURI  string  `json:"clientPlaylistUri"`
		StartTime          float64 `json:"startTime"`
		SegmentStartNumber int     `json:"segmentStartNumber"`
		Complete           bool    `json:"isTranscodeComplete"`
		LibraryItem        struct {
			ID string `json:"id"`
		} `json:"libraryItem"`
		Episode *json.RawMessage `json:"episode"`
	}
	if err := json.Unmarshal(openJSON, &opened); err != nil {
		t.Fatal(err)
	}
	if opened.ID != id || opened.UserID != listenerID || opened.SegmentLength != 6 || opened.ClientPlaylistURI != "/hls/"+id+"/output.m3u8" || opened.PlaylistPath != filepath.Join(dir, "output.m3u8") || opened.StartTime != 27.5 || opened.SegmentStartNumber != 0 || opened.Complete || opened.LibraryItem.ID != sagaItem || opened.Episode != nil {
		t.Errorf("open payload = %s", openJSON)
	}
	var initial struct {
		Stream      string `json:"stream"`
		Percent     string `json:"percent"`
		Chunks      []any  `json:"chunks"`
		NumSegments int    `json:"numSegments"`
	}
	if err := json.Unmarshal(progressJSON, &initial); err != nil {
		t.Fatal(err)
	}
	if initial.Stream != id || initial.Percent != "0%" || initial.Chunks == nil || len(initial.Chunks) != 0 || initial.NumSegments != 0 {
		t.Errorf("initial progress = %s", progressJSON)
	}
	time.Sleep(2200 * time.Millisecond) // allow a wrongly retained completion timer to fire
	if n := e.events("stream_ready"); n != 0 {
		t.Errorf("stream_ready = %d; upstream clears completion timer", n)
	}
	if _, err := os.Stat(filepath.Join(dir, "output-0.ts")); err != nil {
		t.Error(err)
	}
	e.expect(t, 200, listenerID, "POST", "/api/session/"+id+"/close", "")
	if n := e.events("stream_closed"); n != 1 {
		t.Errorf("stream_closed = %d, want 1", n)
	}
	e.mu.Lock()
	closedID, _ := e.payload["stream_closed"].(string)
	e.mu.Unlock()
	if closedID != id {
		t.Errorf("closed payload = %q, want %q", closedID, id)
	}
	e.Close()
	if n := e.events("stream_closed"); n != 1 {
		t.Errorf("duplicate stream_closed = %d", n)
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	for _, event := range e.sent {
		if event.Event == "stream_open" || event.Event == "stream_progress" || event.Event == "stream_closed" {
			if event.Audience != listenerID {
				t.Errorf("event %s leaked to %q", event.Event, event.Audience)
			}
		}
	}
}

func TestStreamLifecycleRealFailure(t *testing.T) {
	binary, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Fatal(err)
	}
	e := newEnv(t)
	t.Setenv("FFMPEG_PATH", binary)
	id, dir := lifecycleStart(t, e)
	waitLifecycleEvent(e, "stream_error")
	assertStreamError(t, e, id)
	if n := e.events("stream_error"); n != 1 {
		t.Errorf("stream_error = %d, want 1", n)
	}
	if n := e.events("stream_open"); n != 0 {
		t.Errorf("failed stream opened %d times", n)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Errorf("failed stream directory survived: %v", err)
	}
	if w := e.serve(t, "", "GET", "/hls/"+id+"/output.m3u8", ""); w.Code != 404 {
		t.Errorf("failed stream HLS status = %d, want 404", w.Code)
	}
	e.expect(t, 200, listenerID, "POST", "/api/session/"+id+"/close", "")
	if n := e.events("stream_closed"); n != 0 {
		t.Errorf("failed stream also closed %d times", n)
	}
}

func TestStreamLifecycleMissingFileFailsAsynchronously(t *testing.T) {
	binary, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Fatal(err)
	}
	e := newEnv(t)
	t.Setenv("FFMPEG_PATH", binary)
	if err := os.Remove(e.media); err != nil {
		t.Fatal(err)
	}
	id, dir := lifecycleStart(t, e)
	waitLifecycleEvent(e, "stream_error")
	assertStreamError(t, e, id)
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Errorf("failed stream directory survived: %v", err)
	}
	if w := e.serve(t, "", "GET", "/hls/"+id+"/output.m3u8", ""); w.Code != 404 {
		t.Errorf("failed stream HLS status = %d, want 404", w.Code)
	}
}

// Files are the externally visible HLS output, not emulated JS/TS objects.
// Stream.js:151-214 polls this output and reports contiguous segment ranges.
func TestStreamLifecycleIncrementalFiles(t *testing.T) {
	e := newEnv(t)
	id, dir := e.startTranscoded(t)
	for _, name := range []string{"output-1.ts", "output-3.ts", "output-4.ts"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("segment"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	deadline := time.Now().Add(3 * time.Second)
	for e.events("stream_progress") < 2 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	e.mu.Lock()
	raw, err := json.Marshal(e.payload["stream_progress"])
	e.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		Stream      string `json:"stream"`
		Percent     string `json:"percent"`
		Chunks      []any  `json:"chunks"`
		NumSegments int    `json:"numSegments"`
	}
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	if got.Stream != id || got.Percent != "27.27%" || got.NumSegments != 11 || !reflect.DeepEqual(got.Chunks, []any{float64(1), "3-4"}) {
		t.Errorf("incremental progress = %s", raw)
	}
	if n := e.events("stream_open"); n != 0 {
		t.Errorf("opened with only three segments: %d", n)
	}
	for _, name := range []string{"output-0.ts", "output-2.ts", "output-5.ts", "output-6.ts"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("segment"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	deadline = time.Now().Add(3 * time.Second)
	for e.events("stream_open") == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if n := e.events("stream_open"); n != 1 {
		t.Errorf("opened with seven segments = %d, want 1", n)
	}
}

func TestStreamLifecycleExecStartupFailure(t *testing.T) {
	e := newEnv(t)
	binary := filepath.Join(t.TempDir(), "ffmpeg")
	if err := os.WriteFile(binary, []byte("#!/nonexistent/abs-ffmpeg-interpreter\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("FFMPEG_PATH", binary)
	id, dir := lifecycleStart(t, e)
	waitLifecycleEvent(e, "stream_error")
	assertStreamError(t, e, id)
	if n := e.events("stream_error"); n != 1 {
		t.Errorf("stream_error = %d, want 1", n)
	}
	if n := e.events("stream_progress"); n != 0 {
		t.Errorf("exec failure emitted %d progress events before process start", n)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Errorf("failed exec directory survived: %v", err)
	}
}

func TestStreamLifecycleHandlerCloseReapsAndClosesOnce(t *testing.T) {
	e := newEnv(t)
	bin := filepath.Join(t.TempDir(), "ffmpeg")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\nwhile :; do :; done\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("FFMPEG_PATH", bin)
	id, _ := lifecycleStart(t, e)
	waitLifecycleEvent(e, "stream_progress")
	st := e.streamOf(id)
	e.Handler.Close()
	select {
	case <-st.done:
	case <-time.After(3 * time.Second):
		t.Fatal("handler close returned before the FFmpeg supervisor was reaped")
	}
	if n := e.events("stream_closed"); n != 1 {
		t.Fatalf("stream_closed = %d, want 1", n)
	}
	time.Sleep(100 * time.Millisecond)
	if n := e.events("stream_closed"); n != 1 || e.events("stream_error") != 0 {
		t.Fatalf("events after close: stream_closed=%d stream_error=%d", n, e.events("stream_error"))
	}
}

func TestStreamLifecycleHandlerCloseBoundsInheritedPipeDrain(t *testing.T) {
	e := newEnv(t)
	marker := filepath.Join(t.TempDir(), "child.pid")
	bin := filepath.Join(t.TempDir(), "ffmpeg")
	script := "#!/bin/sh\nsleep 30 &\necho $! > '" + marker + "'\nwhile :; do :; done\n"
	if err := os.WriteFile(bin, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("FFMPEG_PATH", bin)
	id, _ := lifecycleStart(t, e)
	waitLifecycleEvent(e, "stream_progress")
	var pid int
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		data, err := os.ReadFile(marker)
		if err == nil {
			pid, _ = strconv.Atoi(strings.TrimSpace(string(data))) // retry while the shell is still writing the marker
			if pid > 0 {
				break
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	if pid == 0 {
		t.Fatal("fixture child did not start")
	}
	closed := make(chan struct{})
	t.Cleanup(func() {
		process, err := os.FindProcess(pid)
		if err != nil {
			t.Errorf("find inherited-pipe fixture child: %v", err)
		} else if err := process.Kill(); err != nil {
			// The child may have exited on its own before cleanup.
			t.Logf("kill inherited-pipe fixture child: %v", err)
		}
		<-closed
	})
	go func() {
		e.Handler.Close()
		close(closed)
	}()
	select {
	case <-closed:
	case <-time.After(4 * time.Second):
		t.Errorf("Close blocked beyond four seconds after cancellation with inherited output pipe, stream=%s", id)
		process, err := os.FindProcess(pid)
		if err == nil {
			if err := process.Kill(); err != nil {
				// The child may have exited independently after the timeout.
				t.Logf("kill inherited-pipe fixture child after timeout: %v", err)
			}
		}
		<-closed
	}
}

func TestStreamLifecycleRetriesAACAfterCopyFailure(t *testing.T) {
	e := newEnv(t)
	ctx := t.Context()
	var books []db.Book
	if err := e.d.Select(ctx, &books, `SELECT * FROM books`); err != nil {
		t.Fatal(err)
	}
	for _, book := range books {
		for i := range book.AudioFiles.V {
			book.AudioFiles.V[i].Metadata.Ext = ".m4b"
		}
		if err := e.d.Update(ctx, "books", &book, "audioFiles"); err != nil {
			t.Fatal(err)
		}
	}
	seen := filepath.Join(t.TempDir(), "codecs")
	bin := filepath.Join(t.TempDir(), "ffmpeg")
	script := "#!/bin/sh\nprevious=\ncodec=\nfor arg in \"$@\"; do if [ \"$previous\" = -c:a ]; then codec=$arg; fi; previous=$arg; done\nprintf '%s\\n' \"$codec\" >> '" + seen + "'\nif [ \"$codec\" = copy ]; then echo 'Invalid data found when processing input' >&2; exit 1; fi\nexit 0\n"
	if err := os.WriteFile(bin, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("FFMPEG_PATH", bin)
	id, _ := lifecycleStart(t, e)
	waitLifecycleEvent(e, "stream_open")
	codecs, err := os.ReadFile(seen)
	if err != nil {
		t.Fatal(err)
	}
	if string(codecs) != "copy\naac\n" {
		t.Fatalf("FFmpeg codecs = %q, want copy then aac", codecs)
	}
	if e.events("stream_error") != 0 {
		t.Fatal("AAC retry emitted stream_error")
	}
	st := e.streamOf(id)
	e.Handler.Close() // cancel while the AAC retry's delayed ready loop is pending
	select {
	case <-st.done:
	case <-time.After(time.Second):
		t.Fatal("close did not stop the completed retry supervisor")
	}
}

// Failure may detach the stream before the play HTTP response arrives. Observe
// the event and capability URL rather than requiring an internal stream pointer.
func lifecycleStart(t *testing.T, e *env) (string, string) {
	t.Helper()
	w := e.expect(t, 200, listenerID, "POST", "/api/items/"+sagaItem+"/play", `{"forceTranscode":true}`)
	var session struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &session); err != nil {
		t.Fatal(err)
	}
	return session.ID, filepath.Join(e.streams, session.ID)
}

func waitLifecycleEvent(e *env, name string) {
	deadline := time.Now().Add(3 * time.Second)
	for e.events(name) == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
}

func assertStreamError(t *testing.T, e *env, id string) {
	t.Helper()
	e.mu.Lock()
	defer e.mu.Unlock()
	raw, err := json.Marshal(e.payload["stream_error"])
	if err != nil {
		t.Fatal(err)
	}
	var payload struct {
		ID    string `json:"id"`
		Error string `json:"error"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		t.Fatal(err)
	}
	if payload.ID != id || payload.Error == "" {
		t.Errorf("error payload = %s", raw)
	}
	for _, event := range e.sent {
		if event.Event == "stream_error" && event.Audience != listenerID {
			t.Errorf("error audience = %q", event.Audience)
		}
	}
}
