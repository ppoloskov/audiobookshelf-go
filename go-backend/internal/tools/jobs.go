package tools

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"path/filepath"
	"slices"

	"audiobookshelf-compat/internal/db"
	"audiobookshelf-compat/internal/events"
	"audiobookshelf-compat/internal/httpx"
	"audiobookshelf-compat/internal/ids"
)

// taskData is the data of a task record as the client reads it.
type taskData struct {
	LibraryItemID  string        `json:"libraryItemId"`
	LibraryItemDir string        `json:"libraryItemDir"`
	UserID         string        `json:"userId"`
	Duration       float64       `json:"duration"`
	Options        *embedOptions `json:"options,omitempty"` // embed-metadata only
	Chapters       []db.Chapter  `json:"chapters,omitzero"` // embed-metadata of one file or with forced chapters
}

type embedOptions struct {
	ForceEmbedChapters bool `json:"forceEmbedChapters"`
	BackupFiles        bool `json:"backupFiles"`
}

// job is a running or queued task: the client's record plus the work's state.
type job struct {
	events.Task // Data holds the taskData
	data        taskData
	itemID      string
	ctx         context.Context
	cancel      context.CancelFunc
	cache       string // the item's folder in the item cache
}

var errCanceled = errors.New("Task canceled by user")

func (h *Handler) newJob(action, userID string, b book, title, key, description, descriptionKey string) *job {
	ctx, cancel := context.WithCancel(context.Background())
	j := &job{
		data:   taskData{LibraryItemID: b.item.ID, LibraryItemDir: db.Val(b.item.Path), UserID: userID, Duration: b.duration},
		itemID: b.item.ID, ctx: ctx, cancel: cancel, cache: filepath.Join(h.cfg.Dirs().ItemCache, b.item.ID),
	}
	j.Task = events.NewTask(ids.Must(), action, nil).Titled(title, key, []string{}).Described(description, descriptionKey, db.Val(b.media.Title))
	j.Data = j.data
	return j
}

// start registers a running job, announces it and runs work in the background.
func (h *Handler) start(j *job, work func() error) {
	h.mu.Lock()
	h.running = append(h.running, j)
	started := j.Task
	h.mu.Unlock()
	h.emit.TaskStarted(started)
	go func() { h.finish(j, work()); h.next() }() // a finished embed lets the next queued one run
}

// finish ends j once: failed with err (when not nil) and announced.
func (h *Handler) finish(j *job, err error) {
	h.mu.Lock()
	i := slices.Index(h.running, j)
	if i < 0 {
		h.mu.Unlock()
		return
	}
	h.running = slices.Delete(h.running, i, i+1)
	j.Task = j.Task.Finished()
	if err != nil {
		// Every failure carries the canceled key: the only message the client translates (AbMergeManager.js:45).
		j.Task = j.Task.Failed(err.Error(), "MessageTaskCanceledByUser")
		if !errors.Is(err, errCanceled) {
			slog.Error("[AbMergeManager] task failed", "action", j.Action, "id", j.ID, "error", err)
		}
	}
	finished := j.Task
	h.mu.Unlock()
	h.emit.TaskFinished(finished)
}

// cancelEncode ends the running encode of the item as canceled and kills ffmpeg.
func (h *Handler) cancelEncode(w http.ResponseWriter, r *http.Request) error {
	h.mu.Lock()
	i := slices.IndexFunc(h.running, func(j *job) bool { return j.itemID == r.PathValue("id") && j.Action == "encode-m4b" })
	var j *job
	if i >= 0 {
		j = h.running[i]
	}
	h.mu.Unlock()
	if j == nil {
		return httpx.NotFound()
	}
	j.cancel()
	h.finish(j, errCanceled)
	return httpx.Status(w, http.StatusOK)
}
