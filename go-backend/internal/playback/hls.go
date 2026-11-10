package playback

import (
	"audiobookshelf-compat/internal/mediatools"
	"context"
	"errors"
	"fmt"
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
	ctx          context.Context
	cancel       context.CancelFunc
	startTime    float64
	segmentStart int
	complete     bool
	resetting    bool
	closed       bool // set by close; a pending seek restart must not start ffmpeg again
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
				if at := t.checkSegment(seg); at > 0 {
					h.emit.ToAll("stream_reset", map[string]any{"startTime": at, "streamId": id})
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
	root := filepath.Join(h.cfg.Dirs().Streams, s.ID)
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
	t.mu.Lock()
	if t.closed {
		t.mu.Unlock()
		return nil
	}
	startTime, segmentStart := t.startTime, t.segmentStart
	t.mu.Unlock()
	ctx, cancel := context.WithCancel(context.Background())
	var args []string
	adjusted := math.Max(startTime-30, 0)
	if adjusted > 0 {
		args = append(args, "-ss", strconv.FormatFloat(math.Round(adjusted*10)/10, 'f', 1, 64)+"s")
	}
	args = append(args, "-f", "concat", "-safe", "0", "-i", concat, "-map", "0:a", "-c:a", t.audioCodec, "-f", "hls", "-copyts", "-avoid_negative_ts", "make_non_negative", "-max_delay", "5000000", "-max_muxing_queue_size", "2048", "-hls_time", strconv.Itoa(segmentSeconds), "-hls_segment_type", "mpegts", "-start_number", strconv.Itoa(segmentStart), "-hls_playlist_type", "vod", "-hls_list_size", "0", "-hls_allow_cache", "0", "-hls_segment_filename", filepath.Join(t.root, "output-%d.ts"), filepath.Join(t.root, "final-output.m3u8"))
	done := make(chan struct{})
	t.mu.Lock()
	if t.closed {
		t.mu.Unlock()
		cancel()
		return nil
	}
	t.ctx = ctx
	t.cancel = cancel
	t.done = done
	t.complete = false
	t.resetting = false
	t.mu.Unlock()
	go func() {
		defer close(done)
		err := mediatools.Run(ctx, args...)
		t.mu.Lock()
		t.complete = true
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
			logging.Errorf("[Stream] FFmpeg transcode in %q failed: %v", t.root, err)
			return
		}
	}()
	return nil
}

// checkSegment returns a nonnegative reset seek time or -1 when no reset is due.
func (t *transcode) checkSegment(seg int) float64 {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.resetting || t.closed {
		return -1
	}
	start := seg * segmentSeconds
	reset := t.segmentStart > seg
	if !reset && !t.complete {
		if entries, err := os.ReadDir(t.root); err == nil {
			furthest := 0
			for _, entry := range entries {
				if filepath.Ext(entry.Name()) != ".ts" {
					continue
				}
				if n, ok := segmentNumber(entry.Name()); ok && n > furthest {
					furthest = n
				}
			}
			reset = furthest > 0 && seg-furthest > 10
		}
	}
	if !reset {
		return -1
	}
	seek := math.Max(float64(start-30), 0)
	t.resetting = true
	oldCancel := t.cancel
	oldDone := t.done
	root := t.root
	concat := filepath.Join(root, "files.txt")
	if oldCancel != nil {
		oldCancel()
	}
	go func() {
		if oldDone != nil {
			<-oldDone
		}
		t.mu.Lock()
		if t.closed {
			t.mu.Unlock()
			return
		}
		entries, _ := os.ReadDir(root)
		for _, e := range entries {
			if e.Name() != "files.txt" && e.Name() != "output.m3u8" {
				_ = os.Remove(filepath.Join(root, e.Name()))
			}
		}
		t.startTime = seek
		t.segmentStart = int(math.Floor(math.Max(seek-30, 0) / segmentSeconds))
		t.mu.Unlock()
		_ = t.start(concat)
	}()
	return float64(start)
}

func (t *transcode) close() error {
	t.mu.Lock()
	t.closed = true
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
