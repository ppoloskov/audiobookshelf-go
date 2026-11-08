package playback

import (
	"context"
	"errors"
	"fmt"
	"io"
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

	"audiobookshelf-compat/internal/httputil"
	"audiobookshelf-compat/internal/logging"
)

type transcode struct {
	mu           sync.Mutex
	root         string
	audioCodec   string
	cmd          *exec.Cmd
	ctx          context.Context
	cancel       context.CancelFunc
	startTime    float64
	segmentStart int
	complete     bool
	resetting    bool
	done         chan struct{}
}

// segmentSeconds is the HLS segment length (Stream.js hls_time).
const segmentSeconds = 6

func (h *Handler) handleHLS(w http.ResponseWriter, r *http.Request) error {
	id, name := r.PathValue("id"), r.PathValue("name")
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		return httputil.Status(http.StatusMethodNotAllowed, "")
	}
	h.mu.RLock()
	s := h.sessions[id]
	h.mu.RUnlock()
	if s == nil || s.stream == nil {
		return httputil.Status(http.StatusNotFound, "")
	}
	t := s.stream
	if filepath.Base(name) != name || name == "." || name == ".." {
		return httputil.Status(400, "")
	}
	ext := filepath.Ext(name)
	if ext != ".ts" && ext != ".m3u8" {
		return httputil.Status(400, "")
	}
	path := filepath.Join(t.root, name)
	rel, err := filepath.Rel(t.root, path)
	if err != nil || strings.HasPrefix(rel, "..") || filepath.IsAbs(rel) {
		return httputil.Status(400, "")
	}
	if _, err := os.Stat(path); err != nil {
		if ext == ".ts" {
			if seg, ok := segmentNumber(name); ok {
				if at := t.checkSegment(seg); at >= 0 {
					h.emit("*", "stream_reset", map[string]any{"startTime": at, "streamId": id})
				}
			}
		}
		return httputil.Status(http.StatusNotFound, "")
	}
	contentType := "video/mp2t"
	if ext == ".m3u8" {
		contentType = "application/vnd.apple.mpegurl"
	}
	f, err := os.Open(path)
	if err != nil {
		return httputil.Status(http.StatusNotFound, "")
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return httputil.Status(http.StatusNotFound, "")
	}
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Content-Length", strconv.FormatInt(st.Size(), 10))
	w.Header().Set("Cache-Control", "no-cache")
	http.ServeContent(w, r, name, st.ModTime(), f)
	return nil
}

func (h *Handler) startTranscode(s *session, item *itemData, start float64) (*transcode, error) {
	root := filepath.Join(h.cfg.MetadataPath, "streams", s.ID)
	if err := os.MkdirAll(root, 0700); err != nil {
		return nil, fmt.Errorf("create HLS stream directory: %w", err)
	}
	concat := filepath.Join(root, "files.txt")
	var b strings.Builder
	for _, f := range item.Files {
		p, _ := f.Metadata["path"].(string)
		if p == "" {
			return nil, errors.New("audio track path is empty")
		}
		if _, err := os.Stat(p); err != nil {
			return nil, fmt.Errorf("audio track is unavailable: %w", err)
		}
		b.WriteString("file '")
		b.WriteString(strings.ReplaceAll(p, "'", "'\\''"))
		b.WriteString("'\n")
	}
	if err := os.WriteFile(concat, []byte(b.String()), 0600); err != nil {
		return nil, fmt.Errorf("write FFmpeg concat file: %w", err)
	}
	t := &transcode{root: root, audioCodec: transcodeAudioCodec(item.Files), startTime: start, segmentStart: int(math.Floor(math.Max(start-30, 0) / segmentSeconds))}
	if err := t.generatePlaylist(s.Duration); err != nil {
		return nil, err
	}
	if err := t.start(concat); err != nil {
		return nil, err
	}
	return t, nil
}

// transcodeAudioCodec mirrors Stream.js: AAC is copied by default and only
// the source's explicit MIME/codec set is re-encoded as AAC.
func transcodeAudioCodec(files []audioFile) string {
	if len(files) == 0 {
		return "copy"
	}
	file := files[0]
	codec := strings.ToLower(file.Codec)
	switch file.MimeType {
	case "audio/flac", "audio/ogg", "audio/x-ms-wma", "audio/x-aiff", "audio/webm", "audio/amr-wb", "audio/x-caf":
		return "aac"
	}
	switch codec {
	case "alac", "ac3", "eac3", "opus":
		return "aac"
	default:
		return "copy"
	}
}

func (t *transcode) generatePlaylist(duration float64) error {
	num := int(math.Floor(duration / segmentSeconds))
	last := duration - float64(num*segmentSeconds)
	var b strings.Builder
	fmt.Fprintf(&b, "#EXTM3U\n#EXT-X-VERSION:3\n#EXT-X-ALLOW-CACHE:NO\n#EXT-X-TARGETDURATION:%d\n#EXT-X-MEDIA-SEQUENCE:0\n#EXT-X-PLAYLIST-TYPE:VOD\n", segmentSeconds)
	for i := 0; i < num; i++ {
		fmt.Fprintf(&b, "#EXTINF:%d,\noutput-%d.ts\n", segmentSeconds, i)
	}
	if last > 0 {
		fmt.Fprintf(&b, "#EXTINF:%v,\noutput-%d.ts\n", last, num)
	}
	b.WriteString("#EXT-X-ENDLIST")
	return os.WriteFile(filepath.Join(t.root, "output.m3u8"), []byte(b.String()), 0600)
}

func (t *transcode) start(concat string) error {
	ctx, cancel := context.WithCancel(context.Background())
	t.mu.Lock()
	startTime, segmentStart := t.startTime, t.segmentStart
	t.mu.Unlock()
	args := []string{"-hide_banner", "-loglevel", "error"}
	adjusted := math.Max(startTime-30, 0)
	if adjusted > 0 {
		args = append(args, "-ss", strconv.FormatFloat(math.Round(adjusted*10)/10, 'f', 1, 64)+"s")
	}
	args = append(args, "-f", "concat", "-safe", "0", "-i", concat, "-map", "0:a", "-c:a", t.audioCodec, "-f", "hls", "-copyts", "-avoid_negative_ts", "make_non_negative", "-max_delay", "5000000", "-max_muxing_queue_size", "2048", "-hls_time", strconv.Itoa(segmentSeconds), "-hls_segment_type", "mpegts", "-start_number", strconv.Itoa(segmentStart), "-hls_playlist_type", "vod", "-hls_list_size", "0", "-hls_allow_cache", "0", "-hls_segment_filename", filepath.Join(t.root, "output-%d.ts"), filepath.Join(t.root, "final-output.m3u8"))
	cmd := exec.CommandContext(ctx, ffmpegExecutable(), args...)
	cmd.Stdout = io.Discard
	stderrTail := &tailBuffer{limit: 4096}
	cmd.Stderr = stderrTail
	done := make(chan struct{})
	t.mu.Lock()
	t.ctx = ctx
	t.cancel = cancel
	t.cmd = cmd
	t.done = done
	t.complete = false
	t.resetting = false
	t.mu.Unlock()
	if err := cmd.Start(); err != nil {
		cancel()
		return fmt.Errorf("start FFmpeg: %w", err)
	}
	go func() {
		defer close(done)
		err := cmd.Wait()
		t.mu.Lock()
		t.complete = true
		t.cmd = nil
		t.mu.Unlock()
		if err != nil && !errors.Is(err, context.Canceled) {
			// Stream.js:318-321: a SIGKILL is the intentional stop on
			// session close or seek, logged at INFO, not as a failure.
			var exitErr *exec.ExitError
			if errors.As(err, &exitErr) {
				if status, ok := exitErr.Sys().(syscall.WaitStatus); ok && status.Signaled() && status.Signal() == syscall.SIGKILL {
					logging.Infof("[FFMPEG] Transcode Killed")
					return
				}
			}
			// Upstream runs ffmpeg with -loglevel error in production
			// (Stream.js:268) and logs its failure.
			logging.Errorf("[Stream] FFmpeg transcode in %q failed: %v: %s", t.root, err, stderrTail.String())
			return
		}
		if err == nil {
			if promoteErr := t.promoteFinalPlaylist(); promoteErr != nil {
				logging.Errorf("[Stream] Failed to publish the final playlist in %q: %v", t.root, promoteErr)
			}
		}
	}()
	return nil
}

// tailBuffer keeps the last limit bytes written to it (ffmpeg's stderr).
type tailBuffer struct {
	mu    sync.Mutex
	limit int
	data  []byte
}

func (b *tailBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.data = append(b.data, p...)
	if len(b.data) > b.limit {
		b.data = append([]byte(nil), b.data[len(b.data)-b.limit:]...)
	}
	return len(p), nil
}

func (b *tailBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return strings.TrimSpace(string(b.data))
}

func (t *transcode) promoteFinalPlaylist() error {
	src := filepath.Join(t.root, "final-output.m3u8")
	dst := filepath.Join(t.root, "output.m3u8")
	b, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	// Keep the upstream public playlist URI stable while letting FFmpeg finalize
	// segment ordering and durations.
	return os.WriteFile(dst, b, 0600)
}

// checkSegment returns a nonnegative reset seek time or -1 when no reset is due.
func (t *transcode) checkSegment(seg int) float64 {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.resetting || t.complete {
		return -1
	}
	start := seg * segmentSeconds
	reset := t.segmentStart > seg
	if !reset {
		return -1
	}
	seek := math.Max(float64(start-30), 0)
	t.resetting = true
	oldCancel := t.cancel
	root := t.root
	concat := filepath.Join(root, "files.txt")
	if oldCancel != nil {
		oldCancel()
	}
	go func() {
		time.Sleep(100 * time.Millisecond)
		entries, _ := os.ReadDir(root)
		for _, e := range entries {
			if e.Name() != "files.txt" {
				_ = os.Remove(filepath.Join(root, e.Name()))
			}
		}
		t.mu.Lock()
		t.startTime = seek
		t.segmentStart = int(math.Floor(seek / segmentSeconds))
		t.mu.Unlock()
		_ = t.generatePlaylist(float64(math.Max(float64(seg*segmentSeconds+segmentSeconds), segmentSeconds)))
		_ = t.start(concat)
	}()
	return float64(start)
}

func (t *transcode) close() error {
	t.mu.Lock()
	cancel := t.cancel
	done := t.done
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
	return os.RemoveAll(t.root)
}

func hlsPlaylist(sessionID string) string { return "/hls/" + sessionID + "/output.m3u8" }

func segmentNumber(name string) (int, bool) {
	base := strings.TrimSuffix(name, filepath.Ext(name))
	parts := strings.Split(base, "-")
	if len(parts) != 2 {
		return 0, false
	}
	n, e := strconv.Atoi(parts[1])
	return n, e == nil && n >= 0
}

func ffmpegExecutable() string {
	if p := strings.TrimSpace(os.Getenv("FFMPEG_PATH")); p != "" {
		return p
	}
	return "ffmpeg"
}
