// Package audiotools implements the audiobook file tools and their process-local
// task queue. It reads the existing catalog schema and writes only files the
// requested tool is meant to update; no database migrations are performed.
package audiotools

import (
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
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"audiobookshelf-compat/internal/config"
	"audiobookshelf-compat/internal/store"
)

type EmitFunc func(userID, event string, payload any)
type Handler struct {
	db     *sql.DB
	cfg    config.Config
	emit   EmitFunc
	mu     sync.Mutex
	tasks  map[string]*task
	queued []*task
	closed bool
}
type task struct {
	ID              string         `json:"id"`
	Action          string         `json:"action"`
	Data            map[string]any `json:"data"`
	Title           string         `json:"title"`
	TitleKey        string         `json:"titleKey"`
	TitleSubs       []string       `json:"titleSubs"`
	Description     string         `json:"description"`
	DescriptionKey  string         `json:"descriptionKey"`
	DescriptionSubs []string       `json:"descriptionSubs"`
	Error           string         `json:"error"`
	ErrorKey        string         `json:"errorKey"`
	ErrorSubs       []string       `json:"errorSubs"`
	ShowSuccess     bool           `json:"showSuccess"`
	IsFailed        bool           `json:"isFailed"`
	IsFinished      bool           `json:"isFinished"`
	StartedAt       int64          `json:"startedAt"`
	FinishedAt      *int64         `json:"finishedAt"`
	userID          string
	itemID          string
	ctx             context.Context
	cancel          context.CancelFunc
	cmd             *exec.Cmd
	cache           string
	queuedFlag      bool
}
type item struct {
	id, library, mediaID, typ, path, title, author, subtitle, cover string
	publishedYear, publisher, description                           string
	genres, narrators                                               []string
	files                                                           []audio
	chapters                                                        []chapter
	duration                                                        float64
}
type audio struct {
	ino, path string
	duration  float64
	metadata  map[string]any
}
type chapter struct {
	start, end float64
	title      string
}

func New(db *sql.DB, cfg config.Config, emit func(userID, event string, payload any)) *Handler {
	if emit == nil {
		emit = func(string, string, any) {}
	}
	return &Handler{db: db, cfg: cfg, emit: EmitFunc(emit), tasks: map[string]*task{}}
}

// Handle handles tool routes relative to /api.
func (h *Handler) Handle(w http.ResponseWriter, r *http.Request, user store.User) bool {
	p := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/api"), "/")
	if p == "" {
		p = "/"
	}
	if p == "/tasks" && r.Method == http.MethodGet {
		return false
	} // shared runtime calls Tasks
	if p == "/tools/batch/embed-metadata" && r.Method == http.MethodPost {
		h.batch(w, r, user)
		return true
	}
	if !strings.HasPrefix(p, "/tools/item/") {
		return false
	}
	tail := strings.TrimPrefix(p, "/tools/item/")
	parts := strings.Split(tail, "/")
	if len(parts) != 2 || parts[0] == "" {
		return false
	}
	id, err := urlPathUnescape(parts[0])
	if err != nil {
		status(w, 400)
		return true
	}
	if user.Type != "admin" && user.Type != "root" {
		status(w, 403)
		return true
	}
	if parts[1] == "encode-m4b" && r.Method == http.MethodDelete {
		h.cancelEncode(w, id)
		return true
	}
	it, err := h.loadItem(r.Context(), id)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			status(w, 404)
		} else {
			http.Error(w, "database read failed", 500)
		}
		return true
	}
	if !canAccess(user, it.library) {
		status(w, 403)
		return true
	}
	switch {
	case parts[1] == "encode-m4b" && r.Method == http.MethodPost:
		h.encode(w, r, user, it)
	case parts[1] == "embed-metadata" && r.Method == http.MethodPost:
		h.embed(w, r, user, it)
	default:
		return false
	}
	return true
}

func (h *Handler) Tasks(ctx context.Context, includeQueue bool) (any, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	list := make([]*task, 0, len(h.tasks))
	for _, t := range h.tasks {
		list = append(list, snapshotTask(t))
	}
	// Stable order for clients and tests.
	for i := 1; i < len(list); i++ {
		for j := i; j > 0 && list[j].StartedAt < list[j-1].StartedAt; j-- {
			list[j], list[j-1] = list[j-1], list[j]
		}
	}
	out := map[string]any{"tasks": list}
	if includeQueue {
		q := make([]map[string]any, 0, len(h.queued))
		for _, t := range h.queued {
			q = append(q, cloneData(t.Data))
		}
		out["queuedTaskData"] = map[string]any{"embedMetadata": q}
	}
	return out, nil
}

func (h *Handler) Close() {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.closed = true
	for _, t := range h.tasks {
		if t.cancel != nil {
			t.cancel()
		}
	}
}

func snapshotTask(t *task) *task {
	return &task{ID: t.ID, Action: t.Action, Data: cloneData(t.Data), Title: t.Title, TitleKey: t.TitleKey,
		TitleSubs: append([]string(nil), t.TitleSubs...), Description: t.Description, DescriptionKey: t.DescriptionKey,
		DescriptionSubs: append([]string(nil), t.DescriptionSubs...), Error: t.Error, ErrorKey: t.ErrorKey,
		ErrorSubs: append([]string(nil), t.ErrorSubs...), ShowSuccess: t.ShowSuccess, IsFailed: t.IsFailed,
		IsFinished: t.IsFinished, StartedAt: t.StartedAt, FinishedAt: cloneTime(t.FinishedAt)}
}

func cloneTime(t *int64) *int64 {
	if t == nil {
		return nil
	}
	v := *t
	return &v
}

func cloneData(data map[string]any) map[string]any {
	encoded, err := json.Marshal(data)
	if err != nil {
		return map[string]any{}
	}
	var copy map[string]any
	if json.Unmarshal(encoded, &copy) != nil || copy == nil {
		return map[string]any{}
	}
	return copy
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
	err := h.db.QueryRowContext(ctx, `SELECT li.id,li.libraryId,li.mediaId,li.mediaType,li.path,b.title,b.subtitle,b.coverPath,b.duration,b.audioFiles,b.chapters,li.authorNamesFirstLast,b.publishedYear,b.publisher,b.description,b.genres,b.narrators FROM libraryItems li LEFT JOIN books b ON b.id=li.mediaId WHERE li.id=?`, id).Scan(&x.id, &x.library, &x.mediaID, &mediaType, &x.path, &x.title, &subtitle, &cover, &x.duration, &rawAudio, &rawChapters, &author, &year, &publisher, &description, &rawGenres, &rawNarrators)
	if err != nil {
		return x, err
	}
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
	var files []map[string]any
	if json.Unmarshal([]byte(rawAudio.String), &files) == nil {
		for i, f := range files {
			if f["exclude"] == true {
				continue
			}
			m, _ := f["metadata"].(map[string]any)
			ino := str(f["ino"])
			path := str(f["path"])
			if path == "" {
				path = filepath.Join(x.path, filepath.Base(str(f["filename"])))
			}
			d := num(m["duration"])
			if d == 0 {
				d = num(f["duration"])
			}
			x.files = append(x.files, audio{ino: ino, path: path, duration: d, metadata: m})
			if i == 0 && x.duration == 0 {
				x.duration = d
			}
		}
	}
	var ch []map[string]any
	if json.Unmarshal([]byte(rawChapters.String), &ch) == nil {
		for _, c := range ch {
			x.chapters = append(x.chapters, chapter{num(c["start"]), num(c["end"]), str(c["title"])})
		}
	}
	return x, nil
}

func (h *Handler) encode(w http.ResponseWriter, r *http.Request, u store.User, it item) {
	if it.typ != "book" {
		text(w, 400, "Invalid library item: not a book")
		return
	}
	if len(it.files) == 0 {
		text(w, 400, "Invalid audiobook: no audio tracks")
		return
	}
	if h.has(it.id, "encode-m4b") {
		text(w, 400, "Audiobook is already processing")
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	t := h.newTask("encode-m4b", u, it, "Encoding M4b", "MessageTaskEncodingM4b", fmt.Sprintf("Encoding audiobook %q into a single m4b file.", it.title), "MessageTaskEncodingM4bDescription", false, ctx, cancel)
	if err := os.MkdirAll(t.cache, 0755); err != nil {
		cancel()
		http.Error(w, "Unable to create task cache", 500)
		return
	}
	go h.runEncode(t, it, r.URL.Query())
	sendOK(w)
}
func (h *Handler) embed(w http.ResponseWriter, r *http.Request, u store.User, it item) {
	if it.typ != "book" || len(it.files) == 0 {
		status(w, 400)
		return
	}
	if h.has(it.id, "embed-metadata") {
		text(w, 400, "Library item is already in queue or processing")
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	t := h.newTask("embed-metadata", u, it, "Embedding Metadata", "MessageTaskEmbeddingMetadata", fmt.Sprintf("Embedding metadata in audiobook %q.", it.title), "MessageTaskEmbeddingMetadataDescription", true, ctx, cancel)
	backup := r.URL.Query().Get("backup") == "1"
	force := r.URL.Query().Get("forceEmbedChapters") == "1"
	t.Data["options"] = map[string]any{"forceEmbedChapters": force, "backupFiles": backup}
	if force || len(it.files) == 1 {
		t.Data["chapters"] = chapterDTOs(it.chapters)
	}
	h.mu.Lock()
	h.queued = append(h.queued, t)
	t.queuedFlag = true
	h.mu.Unlock()
	h.emit("@admins", "metadata_embed_queue_update", map[string]any{"libraryItemId": it.id, "queued": true})
	h.runNextEmbed()
	sendOK(w)
}

func (h *Handler) batch(w http.ResponseWriter, r *http.Request, u store.User) {
	if u.Type != "admin" && u.Type != "root" {
		status(w, 403)
		return
	}
	var body struct {
		LibraryItemIDs []string `json:"libraryItemIds"`
	}
	if json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&body) != nil || len(body.LibraryItemIDs) == 0 {
		text(w, 400, "Invalid request payload")
		return
	}
	items := make([]item, 0, len(body.LibraryItemIDs))
	for _, id := range body.LibraryItemIDs {
		it, e := h.loadItem(r.Context(), id)
		if errors.Is(e, sql.ErrNoRows) {
			status(w, 404)
			return
		}
		if e != nil {
			http.Error(w, "database read failed", 500)
			return
		}
		if !canAccess(u, it.library) {
			status(w, 403)
			return
		}
		if it.typ != "book" || len(it.files) == 0 {
			status(w, 400)
			return
		}
		if h.has(id, "embed-metadata") {
			text(w, 400, "Library item is already in queue or processing")
			return
		}
		items = append(items, it)
	}
	for _, it := range items {
		ctx, cancel := context.WithCancel(context.Background())
		t := h.newTask("embed-metadata", u, it, "Embedding Metadata", "MessageTaskEmbeddingMetadata", fmt.Sprintf("Embedding metadata in audiobook %q.", it.title), "MessageTaskEmbeddingMetadataDescription", true, ctx, cancel)
		t.Data["options"] = map[string]any{"forceEmbedChapters": r.URL.Query().Get("forceEmbedChapters") == "1", "backupFiles": r.URL.Query().Get("backup") == "1"}
		if r.URL.Query().Get("forceEmbedChapters") == "1" || len(it.files) == 1 {
			t.Data["chapters"] = chapterDTOs(it.chapters)
		}
		h.mu.Lock()
		h.queued = append(h.queued, t)
		h.mu.Unlock()
		h.emit("@admins", "metadata_embed_queue_update", map[string]any{"libraryItemId": it.id, "queued": true})
	}
	h.runNextEmbed()
	sendOK(w)
}

func (h *Handler) newTask(action string, u store.User, it item, title, key, desc, descKey string, queued bool, ctx context.Context, cancel context.CancelFunc) *task {
	id := randomID()
	now := time.Now().UnixMilli()
	cache := filepath.Join(h.cfg.MetadataPath, "cache", "items", it.id)
	t := &task{ID: id, Action: action, Data: map[string]any{"libraryItemId": it.id, "libraryItemDir": it.path, "userId": u.ID, "duration": it.duration}, Title: title, TitleKey: key, TitleSubs: []string{}, Description: desc, DescriptionKey: descKey, DescriptionSubs: []string{it.title}, ShowSuccess: false, StartedAt: now, userID: u.ID, itemID: it.id, ctx: ctx, cancel: cancel, cache: cache, queuedFlag: queued}
	h.mu.Lock()
	if !queued {
		h.tasks[id] = t
	}
	snapshot := snapshotTask(t)
	h.mu.Unlock()
	if !queued {
		h.emit("*", "task_started", snapshot)
	}
	return t
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
func (h *Handler) cancelEncode(w http.ResponseWriter, id string) {
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
		status(w, 404)
		return
	}
	t.cancel()
	h.finish(t, errors.New("Task canceled by user"))
	sendOK(w)
}
func (h *Handler) finish(t *task, err error) {
	h.mu.Lock()
	if t.IsFinished {
		h.mu.Unlock()
		return
	}
	t.IsFinished = true
	n := time.Now().UnixMilli()
	t.FinishedAt = &n
	if err != nil {
		t.IsFailed = true
		t.Error = err.Error()
		t.ErrorKey = "MessageTaskCanceledByUser"
	}
	snapshot := snapshotTask(t)
	delete(h.tasks, t.ID)
	h.mu.Unlock()
	h.emit("*", "task_finished", snapshot)
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
	t.queuedFlag = false
	h.tasks[t.ID] = t
	snapshot := snapshotTask(t)
	h.mu.Unlock()
	h.emit("*", "metadata_embed_queue_update", map[string]any{"libraryItemId": t.itemID, "queued": false})
	h.emit("*", "task_started", snapshot)
	go func() { e := h.runEmbed(t); h.finish(t, e); h.runNextEmbed() }()
}

func (h *Handler) runEmbed(t *task) error {
	it, e := h.loadItem(t.ctx, t.itemID)
	if e != nil {
		return e
	}
	opt, _ := t.Data["options"].(map[string]any)
	backup := opt["backupFiles"] == true
	if e = os.MkdirAll(t.cache, 0755); e != nil {
		return e
	}
	metaPath := filepath.Join(t.cache, "metadata.txt")
	if e = os.WriteFile(metaPath, []byte(ffmetadata(it, len(it.files) == 1 || opt["forceEmbedChapters"] == true)), 0600); e != nil {
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
		args := []string{"-y", "-i", f.path, "-i", metaPath, "-map", "0:a", "-map_metadata", "1", "-map_metadata", "0", "-map_chapters", "1", "-c", "copy"}
		if it.cover != "" {
			if _, statErr := os.Stat(it.cover); statErr == nil {
				args = append(args, "-i", it.cover, "-map", "2:v?", "-disposition:v", "attached_pic")
			}
		}
		args = append(args, "-f", muxer(f.path), out)
		cmd := exec.CommandContext(t.ctx, ffmpegPath(), args...)
		t.cmd = cmd
		if e = cmd.Run(); e != nil {
			_ = os.Remove(out)
			return fmt.Errorf("ffmpeg metadata embed track %d: %w", i, e)
		}
		if e = os.Rename(out, f.path); e != nil {
			_ = os.Remove(out)
			return e
		}
		h.emit("@admins", "track_finished", map[string]any{"libraryItemId": it.id, "ino": f.ino})
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
	if len(it.files) == 0 {
		return fmt.Errorf("no audio files")
	}
	if e := os.MkdirAll(t.cache, 0755); e != nil {
		return e
	}
	base := strings.TrimSuffix(filepath.Base(it.path), filepath.Ext(it.path))
	target := filepath.Join(filepath.Dir(it.path), base+".m4b")
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
	args = append(args, "-i", metaPath, "-map", "0:a", "-map_metadata", "1", "-map_chapters", "1", "-acodec", qvalue(q, "codec", "aac"), "-ac", qvalue(q, "channels", "2"), "-b:a", qvalue(q, "bitrate", "128k"), "-f", "mp4", temp)
	if it.cover != "" {
		if _, e := os.Stat(it.cover); e == nil {
			args = append(args[:len(args)-1], "-i", it.cover, "-map", "2:v?", "-disposition:v", "attached_pic", args[len(args)-1])
		}
	}
	cmd := exec.CommandContext(t.ctx, ffmpegPath(), args...)
	t.cmd = cmd
	cmd.Stdout = io.Discard
	cmd.Stderr = io.Discard
	if e := cmd.Run(); e != nil {
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
func ffmpegPath() string {
	if p := strings.TrimSpace(os.Getenv("FFMPEG_PATH")); p != "" {
		return p
	}
	return "ffmpeg"
}

func canAccess(u store.User, library string) bool {
	if u.Type == "admin" || u.Type == "root" {
		return true
	}
	var p map[string]any
	_ = json.Unmarshal(u.Permissions, &p)
	if p["accessAllLibraries"] == true {
		return true
	}
	ids, _ := p["librariesAccessible"].([]any)
	for _, v := range ids {
		if v == library {
			return true
		}
	}
	return false
}
func randomID() string { var b [16]byte; _, _ = rand.Read(b[:]); return fmt.Sprintf("%x", b[:]) }
func str(v any) string { s, _ := v.(string); return s }
func num(v any) float64 {
	switch n := v.(type) {
	case float64:
		return n
	case int:
		return float64(n)
	case string:
		f, _ := strconv.ParseFloat(n, 64)
		return f
	}
	return 0
}
func urlPathUnescape(s string) (string, error) { return url.PathUnescape(s) }
func status(w http.ResponseWriter, n int)      { w.WriteHeader(n) }
func text(w http.ResponseWriter, n int, s string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(n)
	_, _ = io.WriteString(w, s)
}
func sendOK(w http.ResponseWriter) { text(w, 200, "OK") }
