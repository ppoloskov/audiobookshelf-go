// Package tools implements the audiobook file tools (encode to m4b, embed
// metadata) and the in-memory registry of their background tasks. The tools
// read the catalog and change only files; they write nothing to the database.
package tools

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"

	"audiobookshelf-compat/internal/auth"
	"audiobookshelf-compat/internal/config"
	"audiobookshelf-compat/internal/db"
	"audiobookshelf-compat/internal/events"
	"audiobookshelf-compat/internal/httpx"
)

// Handler owns the routes and the task registry. Build one with New and
// release it with Close.
type Handler struct {
	db   *db.DB
	cfg  config.Config
	emit events.Emitter

	mu      sync.Mutex // guards the fields below
	running []*job     // started order
	queued  []*job     // embed-metadata jobs waiting for their turn
	closed  bool
}

// New returns a Handler. A nil emit discards events.
func New(d *db.DB, cfg config.Config, emit events.Emitter) *Handler {
	return &Handler{db: d, cfg: cfg, emit: emit}
}

// Routes registers the routes on mux. GET /api/tasks is open to any
// authenticated user; the tool routes are admin-only.
func (h *Handler) Routes(mux *http.ServeMux) {
	mux.Handle("GET /api/tasks", httpx.Handler(h.listTasks))
	admin := func(pattern string, f httpx.Handler) { mux.Handle(pattern, auth.Admin(f)) }
	admin("POST /api/tools/batch/embed-metadata", h.batchEmbed)
	admin("DELETE /api/tools/item/{id}/encode-m4b", h.cancelEncode)
	admin("POST /api/tools/item/{id}/encode-m4b", h.onItem(h.encode))
	admin("POST /api/tools/item/{id}/embed-metadata", h.onItem(func(w http.ResponseWriter, r *http.Request, b book) error { return h.queue(w, r, b) }))
}

// Close cancels the running tasks and starts no queued one.
func (h *Handler) Close() {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.closed = true
	for _, j := range h.running {
		j.cancel()
	}
}

// onItem loads the item of {id} (404 when unknown) before it runs action.
func (h *Handler) onItem(action func(http.ResponseWriter, *http.Request, book) error) httpx.Handler {
	return func(w http.ResponseWriter, r *http.Request) error {
		b, err := h.loadBook(r.Context(), r.PathValue("id"))
		if err != nil {
			return notFoundOr(err)
		}
		return action(w, r, b)
	}
}

// book is a library item as the tools need it. A podcast item has no files.
type book struct {
	item     db.LibraryItem
	media    db.Book
	files    []track
	duration float64
}

type track struct{ ino, path string }

// cover is the path of the cover when its file exists, else "".
func (b book) cover() string {
	if _, err := os.Stat(db.Val(b.media.CoverPath)); err != nil {
		return ""
	}
	return db.Val(b.media.CoverPath)
}

func (b book) isBook() bool { return db.Val(b.item.MediaType) == "book" }

func (h *Handler) loadBook(ctx context.Context, id string) (book, error) {
	var b book
	if err := h.db.Get(ctx, &b.item, "SELECT * FROM libraryItems WHERE id = ?", id); err != nil {
		return b, err
	}
	if !b.isBook() {
		return b, nil
	}
	if err := h.db.Get(ctx, &b.media, "SELECT * FROM books WHERE id = ?", db.Val(b.item.MediaID)); err != nil {
		return b, err
	}
	b.duration = db.Val(b.media.Duration)
	for _, f := range b.media.AudioFiles.V {
		if f.Exclude {
			continue
		}
		// The file's path and name are in its metadata (TS AudioFile.metadata).
		path := f.Metadata.Path
		if path == "" {
			path = filepath.Join(db.Val(b.item.Path), filepath.Base(f.Metadata.Filename))
		}
		b.files = append(b.files, track{f.Ino, path})
		if b.duration == 0 {
			b.duration = db.Val(f.Duration)
		}
	}
	return b, nil
}

// has reports a running or queued job of the action for the item.
func (h *Handler) has(itemID, action string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return slices.ContainsFunc(slices.Concat(h.running, h.queued), func(j *job) bool { return j.itemID == itemID && j.Action == action })
}

func (h *Handler) listTasks(w http.ResponseWriter, r *http.Request) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	reply := tasksReply{Tasks: []events.Task{}}
	for _, j := range h.running {
		reply.Tasks = append(reply.Tasks, j.Task)
	}
	if slices.Contains(strings.Split(r.URL.Query().Get("include"), ","), "queue") {
		reply.Queued = &queuedData{EmbedMetadata: []taskData{}}
		for _, j := range h.queued {
			reply.Queued.EmbedMetadata = append(reply.Queued.EmbedMetadata, j.data)
		}
	}
	return httpx.JSON(w, r, reply)
}

// tasksReply is the answer of GET /api/tasks.
type tasksReply struct {
	Tasks  []events.Task `json:"tasks"`
	Queued *queuedData   `json:"queuedTaskData,omitempty"` // only with ?include=queue
}

type queuedData struct {
	EmbedMetadata []taskData `json:"embedMetadata"`
}

func notFoundOr(err error) error {
	if errors.Is(err, sql.ErrNoRows) {
		return httpx.NotFound()
	}
	return httpx.Internal("database read failed", err)
}
