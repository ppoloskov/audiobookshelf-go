// Package audiotools implements the audiobook file tools and their process-local
// task queue. It reads the existing catalog schema and writes only files the
// requested tool is meant to update; no database migrations are performed.
package audiotools

import (
	"audiobookshelf-compat/internal/access"
	"cmp"
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"

	"audiobookshelf-compat/internal/config"
	"audiobookshelf-compat/internal/events"
	"audiobookshelf-compat/internal/httputil"
	"audiobookshelf-compat/internal/libitem"
	"audiobookshelf-compat/internal/logging"
	"audiobookshelf-compat/internal/mediatools"
	"audiobookshelf-compat/internal/store"
)

type Handler struct {
	db     *sql.DB
	cfg    config.Config
	emit   events.Emitter
	mu     sync.Mutex
	tasks  map[string]*task
	queued []*task
	closed bool
}

// task is a running or queued tool task: the client-facing record plus the
// work's own state.
type task struct {
	events.Task
	data   map[string]any // the record's Data, cloned into each snapshot
	itemID string
	ctx    context.Context
	cancel context.CancelFunc
	cache  string

	forceChapters, backup bool // embed-metadata options (also shown in data["options"])
}

// errCanceled is how a user-canceled task ends.
var errCanceled = errors.New("Task canceled by user")

// snapshot is the record as clients see it now; later changes do not reach it.
func (t *task) snapshot() events.Task {
	s := t.Task
	s.Data = cloneData(t.data)
	return s
}

type item struct {
	id, library, mediaID, typ, path, title, author, subtitle, cover string
	publishedYear, publisher, description                           string
	genres, narrators                                               []string
	files                                                           []audio
	chapters                                                        []chapter
	duration                                                        float64
	isFile                                                          bool
}
type audio struct {
	ino, path string
	duration  float64
}
type chapter struct {
	start, end float64
	title      string
}

func New(db *sql.DB, cfg config.Config, emit events.Emitter) *Handler {
	return &Handler{db: db, cfg: cfg, emit: emit, tasks: map[string]*task{}}
}

// Routes registers the task and tool routes on mux. The server attaches the
// user to the request context before dispatch (store.ContextWithUser).
func (h *Handler) Routes(mux *http.ServeMux) {
	mux.Handle("GET /api/tasks", httputil.Handler(h.tasksRoute))
	mux.Handle("POST /api/tools/batch/embed-metadata", access.RequireAdmin(403, h.batch))
	mux.Handle("DELETE /api/tools/item/{id}/encode-m4b", access.RequireAdmin(403, h.cancelEncode))
	mux.Handle("POST /api/tools/item/{id}/encode-m4b", h.itemRoute(h.encode))
	mux.Handle("POST /api/tools/item/{id}/embed-metadata", h.itemRoute(h.embed))
}

func (h *Handler) itemRoute(action func(http.ResponseWriter, *http.Request, string, item) error) httputil.Handler {
	return access.RequireAdmin(403, func(w http.ResponseWriter, r *http.Request) error {
		it, err := h.loadItem(r.Context(), r.PathValue("id"))
		if errors.Is(err, sql.ErrNoRows) {
			return httputil.Status(404, "")
		}
		if err != nil {
			return httputil.Internal("database read failed", err)
		}
		return action(w, r, store.RequestUser(r).ID, it)
	})
}

func (h *Handler) tasksRoute(w http.ResponseWriter, r *http.Request) error {
	return httputil.JSON(w, r, h.Tasks(strings.Contains(","+r.URL.Query().Get("include")+",", ",queue,")))
}

func (h *Handler) Tasks(includeQueue bool) any {
	h.mu.Lock()
	defer h.mu.Unlock()
	list := make([]events.Task, 0, len(h.tasks))
	for _, t := range h.tasks {
		list = append(list, t.snapshot())
	}
	slices.SortStableFunc(list, func(a, b events.Task) int { return cmp.Compare(a.StartedAt, b.StartedAt) })
	out := map[string]any{"tasks": list}
	if includeQueue {
		q := make([]map[string]any, 0, len(h.queued))
		for _, t := range h.queued {
			q = append(q, cloneData(t.data))
		}
		out["queuedTaskData"] = map[string]any{"embedMetadata": q}
	}
	return out
}

func (h *Handler) Close() {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.closed = true
	for _, t := range h.tasks {
		t.cancel()
	}
}

func cloneData(data map[string]any) map[string]any {
	encoded, err := json.Marshal(data)
	if err != nil {
		return map[string]any{}
	}
	var out map[string]any
	if json.Unmarshal(encoded, &out) != nil || out == nil {
		return map[string]any{}
	}
	return out
}

func chapterDTOs(chapters []chapter) []map[string]any {
	out := make([]map[string]any, 0, len(chapters))
	for _, c := range chapters {
		out = append(out, map[string]any{"start": c.start, "end": c.end, "title": c.title})
	}
	return out
}

func (h *Handler) loadItem(ctx context.Context, id string) (item, error) {
	var x item
	var mediaType, rawAudio, rawChapters, cover, subtitle, author, year, publisher, description, rawGenres, rawNarrators sql.NullString
	var isFile sql.NullBool
	err := h.db.QueryRowContext(ctx, `SELECT li.id,li.libraryId,li.mediaId,li.mediaType,li.path,b.title,b.subtitle,b.coverPath,b.duration,b.audioFiles,b.chapters,li.authorNamesFirstLast,b.publishedYear,b.publisher,b.description,b.genres,b.narrators,li.isFile FROM libraryItems li LEFT JOIN books b ON b.id=li.mediaId WHERE li.id=?`, id).Scan(&x.id, &x.library, &x.mediaID, &mediaType, &x.path, &x.title, &subtitle, &cover, &x.duration, &rawAudio, &rawChapters, &author, &year, &publisher, &description, &rawGenres, &rawNarrators, &isFile)
	if err != nil {
		return x, err
	}
	x.isFile = isFile.Bool
	x.typ = mediaType.String
	x.subtitle = subtitle.String
	x.cover = cover.String
	x.author = author.String
	x.publishedYear, x.publisher, x.description = year.String, publisher.String, description.String
	_ = json.Unmarshal([]byte(rawGenres.String), &x.genres)
	_ = json.Unmarshal([]byte(rawNarrators.String), &x.narrators)
	if x.typ != "book" {
		return x, nil
	}
	var files []libitem.AudioFile
	_ = json.Unmarshal([]byte(rawAudio.String), &files)
	for i, f := range files {
		if f.Exclude {
			continue
		}
		// The file's path and name are in its metadata (TS AudioFile.metadata).
		path := f.Metadata.Path
		if path == "" {
			path = filepath.Join(x.path, filepath.Base(f.Metadata.Filename))
		}
		x.files = append(x.files, audio{ino: f.Ino, path: path, duration: f.Duration})
		if i == 0 && x.duration == 0 {
			x.duration = f.Duration
		}
	}
	var chapters []libitem.AudioChapter
	_ = json.Unmarshal([]byte(rawChapters.String), &chapters)
	for _, c := range chapters {
		x.chapters = append(x.chapters, chapter{c.Start, c.End, c.Title})
	}
	return x, nil
}

func (h *Handler) encode(w http.ResponseWriter, r *http.Request, userID string, it item) error {
	if it.typ != "book" {
		return httputil.Status(400, "Invalid library item: not a book")
	}
	if len(it.files) == 0 {
		return httputil.Status(400, "Invalid audiobook: no audio tracks")
	}
	if h.has(it.id, "encode-m4b") {
		return httputil.Status(400, "Audiobook is already processing")
	}
	t := h.newTask("encode-m4b", userID, it, "Encoding M4b", "MessageTaskEncodingM4b", fmt.Sprintf("Encoding audiobook %q into a single m4b file.", it.title), "MessageTaskEncodingM4bDescription")
	h.mu.Lock()
	h.tasks[t.ID] = t
	started := t.snapshot()
	h.mu.Unlock()
	h.emit.TaskStarted(started)
	go h.runEncode(t, it, r.URL.Query())
	return httputil.OK(w, r)
}
func (h *Handler) embed(w http.ResponseWriter, r *http.Request, userID string, it item) error {
	if err := h.embeddable(it); err != nil {
		return err
	}
	h.enqueueEmbed(userID, it, r.URL.Query())
	h.runNextEmbed()
	return httputil.OK(w, r)
}

func (h *Handler) batch(w http.ResponseWriter, r *http.Request) error {
	userID := store.RequestUser(r).ID
	var body struct {
		LibraryItemIDs []string `json:"libraryItemIds"`
	}
	if json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&body) != nil || len(body.LibraryItemIDs) == 0 {
		return httputil.Status(400, "Invalid request payload")
	}
	items := make([]item, 0, len(body.LibraryItemIDs))
	for _, id := range body.LibraryItemIDs {
		it, e := h.loadItem(r.Context(), id)
		if errors.Is(e, sql.ErrNoRows) {
			return httputil.Status(404, "")
		}
		if e != nil {
			return httputil.Internal("database read failed", e)
		}
		if err := h.embeddable(it); err != nil {
			return err
		}
		items = append(items, it)
	}
	for _, it := range items {
		h.enqueueEmbed(userID, it, r.URL.Query())
	}
	h.runNextEmbed()
	return httputil.OK(w, r)
}

// embeddable is the precondition of an embed-metadata task for the item.
func (h *Handler) embeddable(it item) error {
	if it.typ != "book" || len(it.files) == 0 {
		return httputil.Status(400, "")
	}
	if h.has(it.id, "embed-metadata") {
		return httputil.Status(400, "Library item is already in queue or processing")
	}
	return nil
}

func (h *Handler) enqueueEmbed(userID string, it item, q url.Values) {
	t := h.newTask("embed-metadata", userID, it, "Embedding Metadata", "MessageTaskEmbeddingMetadata", fmt.Sprintf("Embedding metadata in audiobook %q.", it.title), "MessageTaskEmbeddingMetadataDescription")
	force := q.Get("forceEmbedChapters") == "1"
	t.forceChapters, t.backup = force, q.Get("backup") == "1"
	t.data["options"] = map[string]any{"forceEmbedChapters": t.forceChapters, "backupFiles": t.backup}
	if force || len(it.files) == 1 {
		t.data["chapters"] = chapterDTOs(it.chapters)
	}
	h.mu.Lock()
	h.queued = append(h.queued, t)
	h.mu.Unlock()
	h.emit.ToAdmins("metadata_embed_queue_update", map[string]any{"libraryItemId": it.id, "queued": true})
}

func (h *Handler) newTask(action string, userID string, it item, title, key, desc, descKey string) *task {
	ctx, cancel := context.WithCancel(context.Background())
	return &task{
		Task:   events.NewTask(randomID(), action, nil).Titled(title, key, []string{}).Described(desc, descKey, it.title),
		data:   map[string]any{"libraryItemId": it.id, "libraryItemDir": it.path, "userId": userID, "duration": it.duration},
		itemID: it.id, ctx: ctx, cancel: cancel,
		cache: filepath.Join(h.cfg.Dirs().ItemCache, it.id),
	}
}
func (h *Handler) has(itemID, action string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, t := range h.tasks {
		if t.itemID == itemID && t.Action == action {
			return true
		}
	}
	for _, t := range h.queued {
		if t.itemID == itemID && t.Action == action {
			return true
		}
	}
	return false
}
func (h *Handler) cancelEncode(w http.ResponseWriter, r *http.Request) error {
	id := r.PathValue("id")
	h.mu.Lock()
	var t *task
	for _, x := range h.tasks {
		if x.itemID == id && x.Action == "encode-m4b" {
			t = x
			break
		}
	}
	h.mu.Unlock()
	if t == nil {
		return httputil.Status(404, "")
	}
	t.cancel()
	h.finish(t, errCanceled)
	return httputil.OK(w, r)
}
func (h *Handler) finish(t *task, err error) {
	h.mu.Lock()
	if t.IsFinished {
		h.mu.Unlock()
		return
	}
	t.Task = t.Task.Finished()
	if err != nil {
		// Every failure carries the canceled key (AbMergeManager.js:45 is the
		// only message the client translates for these tasks).
		t.Task = t.Task.Failed(err.Error(), "MessageTaskCanceledByUser")
		if !errors.Is(err, errCanceled) {
			logging.Errorf("[AbMergeManager] Task %q (%s) failed: %v", t.Action, t.ID, err)
		}
	}
	snapshot := t.snapshot()
	delete(h.tasks, t.ID)
	h.mu.Unlock()
	h.emit.TaskFinished(snapshot)
}

func (h *Handler) runNextEmbed() {
	h.mu.Lock()
	if h.closed || len(h.queued) == 0 {
		h.mu.Unlock()
		return
	}
	for _, x := range h.tasks {
		if x.Action == "embed-metadata" {
			h.mu.Unlock()
			return
		}
	}
	t := h.queued[0]
	h.queued = h.queued[1:]
	h.tasks[t.ID] = t
	snapshot := t.snapshot()
	h.mu.Unlock()
	h.emit.ToAll("metadata_embed_queue_update", map[string]any{"libraryItemId": t.itemID, "queued": false})
	h.emit.TaskStarted(snapshot)
	go func() { e := h.runEmbed(t); h.finish(t, e); h.runNextEmbed() }()
}

func (h *Handler) runEmbed(t *task) error {
	it, e := h.loadItem(t.ctx, t.itemID)
	if e != nil {
		return e
	}
	backup := t.backup
	if e = os.MkdirAll(t.cache, 0755); e != nil {
		return e
	}
	metaPath := filepath.Join(t.cache, "metadata.txt")
	if e = os.WriteFile(metaPath, []byte(ffmetadata(it, len(it.files) == 1 || t.forceChapters)), 0600); e != nil {
		return e
	}
	defer os.Remove(metaPath)
	for i, f := range it.files {
		if e = t.ctx.Err(); e != nil {
			return e
		}
		if _, e = os.Stat(f.path); e != nil {
			return fmt.Errorf("audio file %s: %w", f.path, e)
		}
		if backup {
			if e = copyFile(f.path, filepath.Join(t.cache, filepath.Base(f.path))); e != nil {
				return e
			}
		}
		out := f.path + ".abs-tmp"
		_ = os.Remove(out)
		// Every input comes before the output options: an option that follows
		// "-i cover" and precedes it is read as the cover input's option.
		args := []string{"-y", "-i", f.path, "-i", metaPath}
		hasCover := false
		if it.cover != "" {
			if _, statErr := os.Stat(it.cover); statErr == nil {
				hasCover = true
				args = append(args, "-i", it.cover)
			}
		}
		args = append(args, "-map", "0:a", "-map_metadata", "1", "-map_metadata", "0", "-map_chapters", "1")
		if hasCover {
			args = append(args, "-map", "2:v?", "-disposition:v", "attached_pic")
		}
		args = append(args, "-c", "copy", "-f", muxer(f.path), out)
		if e = mediatools.Run(t.ctx, args...); e != nil {
			_ = os.Remove(out)
			return fmt.Errorf("ffmpeg metadata embed track %d: %w", i, e)
		}
		if e = os.Rename(out, f.path); e != nil {
			_ = os.Remove(out)
			return e
		}
		h.emit.ToAdmins("track_finished", map[string]any{"libraryItemId": it.id, "ino": f.ino})
	}
	if !backup {
		_ = os.RemoveAll(t.cache)
	}
	return nil
}
func (h *Handler) runEncode(t *task, it item, q map[string][]string) {
	if e := h.merge(t, it, q); e != nil {
		h.finish(t, e)
		return
	}
	h.finish(t, nil)
}
func (h *Handler) merge(t *task, it item, q map[string][]string) error {
	if e := os.MkdirAll(t.cache, 0755); e != nil {
		return e
	}
	// The merged file is named after the item and goes beside a single-file
	// item, or inside a folder item (TS AbMergeManager.startAudiobookMerge).
	base, dir := filepath.Base(it.path), it.path
	if it.isFile {
		base, dir = strings.TrimSuffix(base, filepath.Ext(base)), filepath.Dir(it.path)
	}
	target := filepath.Join(dir, base+".m4b")
	temp := filepath.Join(t.cache, base+".tmp.m4b")
	_ = os.Remove(temp)
	args := []string{"-y"}
	list := filepath.Join(t.cache, "inputs.txt")
	if len(it.files) > 1 {
		var b strings.Builder
		for _, f := range it.files {
			p, e := filepath.Abs(f.path)
			if e != nil {
				return e
			}
			fmt.Fprintf(&b, "file '%s'\n", strings.ReplaceAll(p, "'", "'\\''"))
		}
		if e := os.WriteFile(list, []byte(b.String()), 0600); e != nil {
			return e
		}
		args = append(args, "-safe", "0", "-f", "concat", "-i", list)
	} else {
		args = append(args, "-i", it.files[0].path)
	}
	metaPath := filepath.Join(t.cache, "metadata.txt")
	if e := os.WriteFile(metaPath, []byte(ffmetadata(it, true)), 0600); e != nil {
		return e
	}
	defer os.Remove(metaPath)
	// Every input comes before the output options: an option that follows
	// "-i cover" and precedes it is read as the cover input's option. The cover
	// is a still image, so it is encoded as mjpeg (the mp4 muxer takes it).
	args = append(args, "-i", metaPath)
	var cover []string
	if _, e := os.Stat(it.cover); it.cover != "" && e == nil {
		args = append(args, "-i", it.cover)
		cover = []string{"-map", "2:v?", "-c:v", "mjpeg", "-disposition:v", "attached_pic"}
	}
	args = append(args, "-map", "0:a", "-map_metadata", "1", "-map_chapters", "1")
	args = append(args, cover...)
	args = append(args, "-acodec", qvalue(q, "codec", "aac"), "-ac", qvalue(q, "channels", "2"), "-b:a", qvalue(q, "bitrate", "128k"), "-f", "mp4", temp)
	if e := mediatools.Run(t.ctx, args...); e != nil {
		_ = os.Remove(temp)
		return fmt.Errorf("ffmpeg m4b encode: %w", e)
	}
	for _, f := range it.files {
		dst := filepath.Join(t.cache, filepath.Base(f.path))
		if _, e := os.Stat(dst); e == nil {
			continue
		}
		if e := copyFile(f.path, dst); e != nil {
			_ = os.Remove(temp)
			return e
		}
	}
	if e := os.Rename(temp, target); e != nil {
		_ = os.Remove(temp)
		return e
	}
	_ = os.Remove(list)
	return nil
}
func ffmetadata(it item, withChapters bool) string {
	var b strings.Builder
	b.WriteString(";FFMETADATA1\n")
	album := it.title
	if it.subtitle != "" {
		album += ": " + it.subtitle
	}
	fields := [][2]string{{"title", it.title}, {"artist", it.author}, {"album_artist", it.author}, {"album", album}, {"TIT3", it.subtitle}, {"genre", strings.Join(it.genres, "; ")}, {"date", it.publishedYear}, {"comment", it.description}, {"description", it.description}, {"composer", strings.Join(it.narrators, ", ")}, {"copyright", it.publisher}, {"publisher", it.publisher}, {"TRACKTOTAL", strconv.Itoa(len(it.files))}}
	for _, field := range fields {
		k, v := field[0], field[1]
		if v != "" {
			fmt.Fprintf(&b, "%s=%s\n", k, escape(v))
		}
	}
	if withChapters {
		for _, c := range it.chapters {
			fmt.Fprintf(&b, "\n[CHAPTER]\nTIMEBASE=1/1000\nSTART=%d\nEND=%d\ntitle=%s\n", int64(c.start*1000), int64(c.end*1000), escape(c.title))
		}
	}
	return b.String()
}
func escape(s string) string {
	return strings.NewReplacer("\\", "\\\\", ";", "\\;", "=", "\\=", "\n", "\\\n", "#", "\\#").Replace(s)
}
func muxer(p string) string {
	switch strings.ToLower(filepath.Ext(p)) {
	case ".mp3":
		return "mp3"
	case ".m4b", ".m4a", ".mp4":
		return "mp4"
	case ".ogg", ".oga":
		return "ogg"
	case ".flac":
		return "flac"
	default:
		return "matroska"
	}
}
func qvalue(q map[string][]string, k, d string) string {
	if len(q[k]) > 0 && q[k][0] != "" {
		return q[k][0]
	}
	return d
}
func copyFile(src, dst string) error {
	in, e := os.Open(src)
	if e != nil {
		return e
	}
	defer in.Close()
	if e = os.MkdirAll(filepath.Dir(dst), 0755); e != nil {
		return e
	}
	out, e := os.OpenFile(dst, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0600)
	if e != nil {
		return e
	}
	_, ce := io.Copy(out, in)
	se := out.Sync()
	oe := out.Close()
	if ce != nil {
		return ce
	}
	if se != nil {
		return se
	}
	return oe
}
func randomID() string { var b [16]byte; _, _ = rand.Read(b[:]); return fmt.Sprintf("%x", b[:]) }
