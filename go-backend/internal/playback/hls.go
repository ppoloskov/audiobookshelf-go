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
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"audiobookshelf-compat/internal/db"
	"audiobookshelf-compat/internal/httpx"
	"audiobookshelf-compat/internal/ids"
	"audiobookshelf-compat/internal/item"
	"audiobookshelf-compat/internal/media"
)

// segmentSeconds is the HLS segment length (Stream.js hls_time).
const segmentSeconds = 6

// stream supervises the ffmpeg process that writes the HLS segments of one
// transcoded session into root. The playlist for the whole duration is written
// up front; ffmpeg starts 30 seconds before the requested position. A request
// for a segment ffmpeg will not reach soon restarts it there (checkSegment).
type stream struct {
	root         string
	handler      *Handler
	userID       string
	libraryItem  item.Expanded
	episode      *item.EpisodeExpanded
	duration     float64
	aacEncodable bool

	mu           sync.Mutex // guards the mutable fields below
	audioCodec   string
	resetDelay   time.Duration
	startTime    float64 // where the current ffmpeg run was asked to start
	segmentStart int     // the first segment of the current run
	cancel       context.CancelFunc
	done         chan struct{} // closed when the current run exits
	complete     bool          // the current run exited
	resetting    bool          // a restart is in flight
	closed       bool          // a pending restart must not start ffmpeg again
	initialized  bool
	segments     map[int]struct{}
	runReset     bool
}

type streamOpen struct {
	ID                 string                `json:"id"`
	UserID             string                `json:"userId"`
	LibraryItem        item.Expanded         `json:"libraryItem"`
	Episode            *item.EpisodeExpanded `json:"episode"`
	SegmentLength      int                   `json:"segmentLength"`
	PlaylistPath       string                `json:"playlistPath"`
	ClientPlaylistURI  string                `json:"clientPlaylistUri"`
	StartTime          float64               `json:"startTime"`
	SegmentStartNumber int                   `json:"segmentStartNumber"`
	Complete           bool                  `json:"isTranscodeComplete"`
}

type streamProgress struct {
	Stream      string `json:"stream"`
	Percent     string `json:"percent"`
	Chunks      []any  `json:"chunks"`
	NumSegments int    `json:"numSegments"`
}

type streamError struct {
	ID    string `json:"id"`
	Error string `json:"error"`
}

type streamReset struct {
	StartTime float64 `json:"startTime"`
	StreamID  string  `json:"streamId"`
}

func (t *stream) emit(event string, payload any) {
	if t.handler != nil {
		t.handler.emit.ToUser(t.userID, event, payload)
	}
}

// startStream prepares the session's stream directory and playlist. The caller
// registers the session before starting the prepared transcode.
func (h *Handler) startStream(sessionID, userID string, libraryItem item.Expanded, episode *item.EpisodeExpanded, files []db.AudioFile, duration, start float64) (*stream, error) {
	root := filepath.Join(h.cfg.Dirs().Streams, sessionID)
	if err := os.MkdirAll(root, 0700); err != nil {
		return nil, fmt.Errorf("create HLS stream directory: %w", err)
	}
	ready := false
	defer func() {
		if !ready {
			_ = os.RemoveAll(root) // best-effort cleanup after setup failure
		}
	}()
	var list strings.Builder
	for _, file := range files {
		path := file.Metadata.Path
		if path == "" {
			return nil, errors.New("audio track path is empty")
		}
		list.WriteString("file '" + strings.ReplaceAll(path, "'", "'\\''") + "'\n")
	}
	if err := os.WriteFile(filepath.Join(root, "files.txt"), []byte(list.String()), 0600); err != nil {
		return nil, fmt.Errorf("write FFmpeg concat file: %w", err)
	}
	codec := transcodeAudioCodec(files)
	resetDelay := 500 * time.Millisecond
	encodable := false
	if len(files) > 0 {
		ext := strings.ToLower(strings.TrimPrefix(files[0].Metadata.Ext, "."))
		encodable = ext == "mp4" || ext == "m4a" || ext == "m4b"
	}
	t := &stream{root: root, audioCodec: codec, handler: h, userID: userID, libraryItem: libraryItem, episode: episode,
		duration: duration, resetDelay: resetDelay, startTime: start,
		aacEncodable: encodable, segmentStart: segmentOf(start), segments: map[int]struct{}{}}
	if err := t.writePlaylist(duration); err != nil {
		return nil, err
	}
	ready = true
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
	t.startLocked()
}

func (t *stream) startLocked() {
	if t.closed {
		return
	}
	var args []string
	if seek := math.Max(t.startTime-30, 0); seek > 0 {
		args = append(args, "-ss", strconv.FormatFloat(math.Round(seek*10)/10, 'f', 1, 64)+"s")
	}
	args = append(args, "-y", "-f", "concat", "-safe", "0", "-i", filepath.Join(t.root, "files.txt"), "-map", "0:a", "-c:a", t.audioCodec,
		"-f", "hls", "-copyts", "-avoid_negative_ts", "make_non_negative", "-max_delay", "5000000", "-max_muxing_queue_size", "2048",
		"-hls_time", strconv.Itoa(segmentSeconds), "-hls_segment_type", "mpegts", "-start_number", strconv.Itoa(t.segmentStart),
		"-hls_playlist_type", "vod", "-hls_list_size", "0", "-hls_allow_cache", "0",
		"-hls_segment_filename", filepath.Join(t.root, "output-%d.ts"), filepath.Join(t.root, "final-output.m3u8"))
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	runReset := t.resetting
	t.cancel, t.done, t.complete = cancel, done, false
	t.runReset = runReset
	go t.supervise(ctx, cancel, done, args)
}

func (t *stream) supervise(ctx context.Context, cancel context.CancelFunc, done chan struct{}, args []string) {
	defer close(done)
	started := make(chan struct{})
	finished := make(chan error, 1)
	go func() {
		err := media.RunStarted(ctx, func() {
			select {
			case started <- struct{}{}:
			case <-ctx.Done():
			}
		}, args...)
		finished <- err
	}()
	var ticker *time.Ticker
	var tick <-chan time.Time
	var delayed *time.Timer
	var delayedTick <-chan time.Time
	startLoop := func() {
		t.emit("stream_progress", streamProgress{Stream: t.id(), Percent: "0%", Chunks: []any{}, NumSegments: 0})
		ticker = time.NewTicker(2 * time.Second)
		tick = ticker.C
	}
	defer func() {
		if ticker != nil {
			ticker.Stop()
		}
		if delayed != nil {
			delayed.Stop()
		}
		cancel()
	}()
	startedOK, complete := false, false
	for {
		select {
		case <-started:
			if startedOK {
				continue
			}
			startedOK = true
			t.mu.Lock()
			resetting := t.runReset
			t.mu.Unlock()
			if resetting {
				delayed = time.NewTimer(t.resetDelay)
				delayedTick = delayed.C
			} else {
				startLoop()
			}
		case <-delayedTick:
			delayedTick = nil
			t.mu.Lock()
			t.resetting = false
			t.mu.Unlock()
			startLoop()
		case <-tick:
			if complete {
				t.emit("stream_ready", nil)
				return
			}
			t.reportFiles()
		case err := <-finished:
			if errors.Is(err, context.Canceled) || killed(err) {
				return
			}
			if err != nil {
				if ctx.Err() != nil {
					return
				}
				if t.retryAAC(ctx, err) {
					return
				}
				slog.Error("ffmpeg transcode failed", "dir", t.root, "error", err)
				t.fail()
				return
			}
			complete = true
			t.mu.Lock()
			initialized, resetting := t.initialized, t.runReset
			t.mu.Unlock()
			if !initialized {
				t.open()
			}
			t.mu.Lock()
			t.complete = true
			t.mu.Unlock()
			// Stream.js clears the regular completion loop. A reset's delayed
			// startLoop can still observe completion and emit stream_ready.
			if !resetting || delayedTick == nil {
				return
			}
		case <-ctx.Done():
			if !complete {
				<-finished //nolint:errcheck // cancellation is intentional; wait for reaping and discard the stopped command error
			}
			return
		}
	}
}

// retryAAC preserves Stream.js' copy-to-AAC recovery for encodable media when
// ffmpeg rejects the copied audio. Missing input files do not benefit from it.
func (t *stream) retryAAC(ctx context.Context, err error) bool {
	var exit *exec.ExitError
	if !t.aacEncodable || !errors.As(err, &exit) || exit.ExitCode() != 1 || strings.Contains(err.Error(), "No such file or directory") {
		return false
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if ctx.Err() != nil || t.closed || t.audioCodec != "copy" {
		return false
	}
	t.audioCodec = "aac"
	t.resetting = true
	t.resetDelay = 3 * time.Second
	t.startLocked()
	return true
}

func (t *stream) id() string { return filepath.Base(t.root) }

func (t *stream) open() {
	t.mu.Lock()
	if t.initialized || t.closed {
		t.mu.Unlock()
		return
	}
	t.initialized = true
	payload := t.openPayloadLocked()
	t.mu.Unlock()
	t.emit("stream_open", payload)
}

// openPayloadLocked snapshots the open event while the stream mutex is held.
func (t *stream) openPayloadLocked() streamOpen {
	return streamOpen{ID: t.id(), UserID: t.userID, LibraryItem: t.libraryItem, Episode: t.episode,
		SegmentLength: segmentSeconds, PlaylistPath: filepath.Join(t.root, "output.m3u8"), ClientPlaylistURI: hlsPlaylist(t.id()),
		StartTime: t.startTime, SegmentStartNumber: segmentOf(t.startTime), Complete: t.complete}
}

func (t *stream) reportFiles() {
	entries, err := os.ReadDir(t.root)
	if err != nil {
		return
	}
	t.mu.Lock()
	if t.closed {
		t.mu.Unlock()
		return
	}
	for _, entry := range entries {
		if n, ok := segmentNumber(entry.Name()); ok && filepath.Ext(entry.Name()) == ".ts" {
			t.segments[n] = struct{}{}
		}
	}
	segments := make([]int, 0, len(t.segments))
	for n := range t.segments {
		segments = append(segments, n)
	}
	if len(segments) == 0 {
		t.mu.Unlock()
		return
	}
	slices.Sort(segments)
	open := len(segments) > 6 && !t.initialized
	if open {
		t.initialized = true
	}
	chunks := make([]any, 0, len(segments)) // numbers for singletons, string ranges for adjacent segments
	for i := 0; i < len(segments); {
		j := i
		for j+1 < len(segments) && segments[j+1] == segments[j]+1 {
			j++
		}
		if i == j {
			chunks = append(chunks, segments[i])
		} else {
			chunks = append(chunks, fmt.Sprintf("%d-%d", segments[i], segments[j]))
		}
		i = j + 1
	}
	count := int(math.Ceil(t.duration / segmentSeconds))
	percent := "0%"
	if count > 0 {
		percent = fmt.Sprintf("%.2f%%", float64(len(segments))*100/float64(count))
	}
	payload := streamProgress{Stream: t.id(), Percent: percent, Chunks: chunks, NumSegments: count}
	var openPayload streamOpen
	if open {
		openPayload = t.openPayloadLocked()
	}
	t.mu.Unlock()
	if open {
		t.emit("stream_open", openPayload)
	}
	t.emit("stream_progress", payload)
}

func (t *stream) fail() {
	t.mu.Lock()
	if t.closed {
		t.mu.Unlock()
		return
	}
	t.closed = true
	t.mu.Unlock()
	_ = os.RemoveAll(t.root) // failed stream output must not remain available
	t.emit("stream_error", streamError{ID: t.id(), Error: "Failed to transcode stream"})
	if t.handler == nil {
		return
	}
	t.handler.mu.Lock()
	if s := t.handler.sessions[t.id()]; s != nil && s.stream == t {
		s.stream = nil
	}
	t.handler.mu.Unlock()
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
	wasClosed := t.closed
	t.closed = true
	cancel, done := t.cancel, t.done
	t.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	if done != nil {
		<-done
	}
	if err := os.RemoveAll(t.root); err != nil {
		slog.Error("remove HLS stream directory", "session", sessionID, "error", err)
	}
	if !wasClosed {
		t.emit("stream_closed", sessionID)
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
				h.emit.ToAll("stream_reset", streamReset{StartTime: at, StreamID: id})
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
