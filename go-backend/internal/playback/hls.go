package playback

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"audiobookshelf-compat/internal/db"
	"audiobookshelf-compat/internal/httpx"
	"audiobookshelf-compat/internal/ids"
	"audiobookshelf-compat/internal/media"
)

// segmentSeconds is the HLS segment length (Stream.js hls_time).
const segmentSeconds = 6

// stream supervises the ffmpeg process that writes the HLS segments of one
// transcoded session into root. The playlist for the whole duration is written
// up front; ffmpeg starts 30 seconds before the requested position. A request
// for a segment ffmpeg will not reach soon restarts it there (checkSegment).
type stream struct {
	root       string
	audioCodec string

	mu           sync.Mutex // guards the fields below
	startTime    float64    // where the current ffmpeg run was asked to start
	segmentStart int        // the first segment of the current run
	cancel       context.CancelFunc
	done         chan struct{} // closed when the current run exits
	complete     bool          // the current run exited
	resetting    bool          // a restart is in flight
	closed       bool          // a pending restart must not start ffmpeg again
}

// startStream creates the session's stream directory and playlist and starts
// ffmpeg at position start.
func (h *Handler) startStream(sessionID string, files []db.AudioFile, duration, start float64) (*stream, error) {
	root := filepath.Join(h.cfg.Dirs().Streams, sessionID)
	if err := os.MkdirAll(root, 0700); err != nil {
		return nil, fmt.Errorf("create HLS stream directory: %w", err)
	}
	var list strings.Builder
	for _, file := range files {
		path := file.Metadata.Path
		if path == "" {
			return nil, errors.New("audio track path is empty")
		}
		if _, err := os.Stat(path); err != nil {
			return nil, fmt.Errorf("audio track is unavailable: %w", err)
		}
		list.WriteString("file '" + strings.ReplaceAll(path, "'", "'\\''") + "'\n")
	}
	if err := os.WriteFile(filepath.Join(root, "files.txt"), []byte(list.String()), 0600); err != nil {
		return nil, fmt.Errorf("write FFmpeg concat file: %w", err)
	}
	t := &stream{root: root, audioCodec: transcodeAudioCodec(files), startTime: start, segmentStart: segmentOf(start)}
	if err := t.writePlaylist(duration); err != nil {
		return nil, err
	}
	t.start()
	return t, nil
}

// segmentOf is the segment ffmpeg starts at for a seek to position (30 seconds early).
func segmentOf(position float64) int {
	return int(math.Floor(math.Max(position-30, 0) / segmentSeconds))
}

// transcodeAudioCodec is Stream.js: AAC is copied, and the first file's formats
// that browsers cannot play are re-encoded as AAC.
func transcodeAudioCodec(files []db.AudioFile) string {
	if len(files) == 0 {
		return "copy"
	}
	switch files[0].MimeType {
	case "audio/flac", "audio/ogg", "audio/x-ms-wma", "audio/x-aiff", "audio/webm", "audio/amr-wb", "audio/x-caf":
		return "aac"
	}
	switch strings.ToLower(db.Val(files[0].Codec)) {
	case "alac", "ac3", "eac3", "opus":
		return "aac"
	}
	return "copy"
}

func hlsPlaylist(sessionID string) string { return "/hls/" + sessionID + "/output.m3u8" }

// writePlaylist writes the VOD playlist of the whole duration.
func (t *stream) writePlaylist(duration float64) error {
	whole := int(math.Floor(duration / segmentSeconds))
	var b strings.Builder
	fmt.Fprintf(&b, "#EXTM3U\n#EXT-X-VERSION:3\n#EXT-X-ALLOW-CACHE:NO\n#EXT-X-TARGETDURATION:%d\n#EXT-X-MEDIA-SEQUENCE:0\n#EXT-X-PLAYLIST-TYPE:VOD\n", segmentSeconds)
	for i := range whole {
		fmt.Fprintf(&b, "#EXTINF:%d,\noutput-%d.ts\n", segmentSeconds, i)
	}
	if rest := duration - float64(whole*segmentSeconds); rest > 0 {
		fmt.Fprintf(&b, "#EXTINF:%v,\noutput-%d.ts\n", rest, whole)
	}
	b.WriteString("#EXT-X-ENDLIST")
	return os.WriteFile(filepath.Join(t.root, "output.m3u8"), []byte(b.String()), 0600)
}

// start runs ffmpeg from startTime/segmentStart, unless the stream is closed.
func (t *stream) start() {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.closed {
		return
	}
	var args []string
	if seek := math.Max(t.startTime-30, 0); seek > 0 {
		args = append(args, "-ss", strconv.FormatFloat(math.Round(seek*10)/10, 'f', 1, 64)+"s")
	}
	args = append(args, "-f", "concat", "-safe", "0", "-i", filepath.Join(t.root, "files.txt"), "-map", "0:a", "-c:a", t.audioCodec,
		"-f", "hls", "-copyts", "-avoid_negative_ts", "make_non_negative", "-max_delay", "5000000", "-max_muxing_queue_size", "2048",
		"-hls_time", strconv.Itoa(segmentSeconds), "-hls_segment_type", "mpegts", "-start_number", strconv.Itoa(t.segmentStart),
		"-hls_playlist_type", "vod", "-hls_list_size", "0", "-hls_allow_cache", "0",
		"-hls_segment_filename", filepath.Join(t.root, "output-%d.ts"), filepath.Join(t.root, "final-output.m3u8"))
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	t.cancel, t.done, t.complete, t.resetting = cancel, done, false, false
	go func() {
		defer close(done)
		err := media.Run(ctx, args...)
		t.mu.Lock()
		t.complete = true
		t.mu.Unlock()
		if err != nil && !errors.Is(err, context.Canceled) && !killed(err) {
			slog.Error("ffmpeg transcode failed", "dir", t.root, "error", err)
		}
	}()
}

// killed reports a SIGKILL, the intentional stop on close or seek (Stream.js:318).
func killed(err error) bool {
	var exit *exec.ExitError
	if !errors.As(err, &exit) {
		return false
	}
	status, ok := exit.Sys().(syscall.WaitStatus)
	return ok && status.Signaled() && status.Signal() == syscall.SIGKILL
}

// checkSegment decides whether a request for the missing segment seg needs a new
// ffmpeg run: when it is before the run's first segment, or more than 10 beyond
// the furthest written one. It starts the restart and returns the time ffmpeg is
// sent to, or -1 when no restart is due or one is already in flight.
func (t *stream) checkSegment(seg int) float64 {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.resetting || t.closed {
		return -1
	}
	reset := t.segmentStart > seg
	if !reset && !t.complete {
		furthest := 0
		if entries, err := os.ReadDir(t.root); err == nil {
			for _, entry := range entries {
				if n, ok := segmentNumber(entry.Name()); ok && filepath.Ext(entry.Name()) == ".ts" {
					furthest = max(furthest, n)
				}
			}
		}
		reset = furthest > 0 && seg-furthest > 10
	}
	if !reset {
		return -1
	}
	at := seg * segmentSeconds
	t.resetting = true
	if t.cancel != nil {
		t.cancel()
	}
	previous := t.done
	go func() {
		if previous != nil {
			<-previous
		}
		t.mu.Lock()
		if t.closed {
			t.mu.Unlock()
			return
		}
		entries, _ := os.ReadDir(t.root) // an unreadable directory has nothing to wipe
		for _, entry := range entries {
			if name := entry.Name(); name != "files.txt" && name != "output.m3u8" {
				_ = os.Remove(filepath.Join(t.root, name)) // a leftover segment is overwritten or ignored
			}
		}
		t.startTime = math.Max(float64(at-30), 0)
		t.segmentStart = segmentOf(t.startTime)
		t.mu.Unlock()
		t.start()
	}()
	return float64(at)
}

// stop kills ffmpeg and removes the stream directory; it is safe on a nil stream.
func (t *stream) stop(sessionID string) {
	if t == nil {
		return
	}
	t.mu.Lock()
	t.closed = true
	cancel, done := t.cancel, t.done
	t.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	if done != nil {
		select {
		case <-done:
		case <-time.After(3 * time.Second):
		}
	}
	if err := os.RemoveAll(t.root); err != nil {
		slog.Error("remove HLS stream directory", "session", sessionID, "error", err)
	}
}

// segmentNumber parses "output-12.ts".
func segmentNumber(name string) (int, bool) {
	_, number, found := strings.Cut(strings.TrimSuffix(name, filepath.Ext(name)), "-")
	n, err := strconv.Atoi(number)
	return n, found && err == nil && n >= 0
}

// hls is /hls/{id}/{name}: playlist and segments of a transcoded session, a
// capability URL that needs no user. A missing segment may restart ffmpeg.
func (h *Handler) hls(w http.ResponseWriter, r *http.Request) error {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		return httpx.Error(http.StatusMethodNotAllowed, "")
	}
	id, name := r.PathValue("id"), r.PathValue("name")
	s, found := h.get(id)
	if !found || s.stream == nil {
		return httpx.NotFound()
	}
	ext := filepath.Ext(name)
	if filepath.Base(name) != name || (ext != ".ts" && ext != ".m3u8") {
		return httpx.BadRequest("")
	}
	path := filepath.Join(s.stream.root, name)
	if _, err := os.Stat(path); err != nil {
		if seg, isSegment := segmentNumber(name); isSegment && ext == ".ts" {
			if at := s.stream.checkSegment(seg); at > 0 {
				h.emit.ToAll("stream_reset", map[string]any{"startTime": at, "streamId": id})
			}
		}
		return httpx.NotFound()
	}
	w.Header().Set("Content-Type", "video/mp2t")
	if ext == ".m3u8" {
		w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
	}
	w.Header().Set("Cache-Control", "no-cache")
	return httpx.ServeFile(w, r, path)
}

// CleanupOrphanStreams removes the stream directories of a previous process; call
// it at startup, before any session exists.
func CleanupOrphanStreams(root string) error {
	if err := os.MkdirAll(root, 0700); err != nil {
		return err
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if !entry.IsDir() || !ids.ValidV4(entry.Name()) {
			continue
		}
		if err := os.RemoveAll(filepath.Join(root, entry.Name())); err != nil {
			slog.Error("remove orphan stream", "dir", entry.Name(), "error", err)
		}
	}
	return nil
}
