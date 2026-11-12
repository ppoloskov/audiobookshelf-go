package tools

import (
	"cmp"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"audiobookshelf-compat/internal/auth"
	"audiobookshelf-compat/internal/db"
	"audiobookshelf-compat/internal/httpx"
	"audiobookshelf-compat/internal/mediatools"
)

type queueUpdate struct {
	LibraryItemID string `json:"libraryItemId"`
	Queued        bool   `json:"queued"`
}

type trackFinished struct {
	LibraryItemID string `json:"libraryItemId"`
	Ino           string `json:"ino"`
}

// batchEmbed queues every listed item, or none when any is unknown or unfit.
func (h *Handler) batchEmbed(w http.ResponseWriter, r *http.Request) error {
	body, err := httpx.Decode[struct {
		LibraryItemIDs []string `json:"libraryItemIds"`
	}](w, r)
	if err != nil || len(body.LibraryItemIDs) == 0 {
		return httpx.BadRequest("Invalid request payload")
	}
	books := make([]book, 0, len(body.LibraryItemIDs))
	for _, id := range body.LibraryItemIDs {
		b, err := h.loadBook(r.Context(), id)
		if err != nil {
			return notFoundOr(err)
		}
		books = append(books, b)
	}
	return h.queue(w, r, books...)
}

// queue checks every book, then queues them all and starts the first.
func (h *Handler) queue(w http.ResponseWriter, r *http.Request, books ...book) error {
	for _, b := range books {
		if err := h.embeddable(b); err != nil {
			return err
		}
	}
	for _, b := range books {
		h.enqueue(auth.User(r.Context()).ID, b, r.URL.Query())
	}
	h.next()
	return httpx.Status(w, http.StatusOK)
}

// embeddable is the precondition of an embed-metadata task for the item.
func (h *Handler) embeddable(b book) error {
	if !b.isBook() || len(b.files) == 0 {
		return httpx.Error(http.StatusBadRequest, "")
	}
	if h.has(b.item.ID, "embed-metadata") {
		return httpx.BadRequest("Library item is already in queue or processing")
	}
	return nil
}

func (h *Handler) enqueue(userID string, b book, query url.Values) {
	j := h.newJob("embed-metadata", userID, b, "Embedding Metadata", "MessageTaskEmbeddingMetadata",
		fmt.Sprintf("Embedding metadata in audiobook %q.", db.Val(b.media.Title)), "MessageTaskEmbeddingMetadataDescription")
	j.data.Options = &embedOptions{ForceEmbedChapters: query.Get("forceEmbedChapters") == "1", BackupFiles: query.Get("backup") == "1"}
	if j.data.Options.ForceEmbedChapters || len(b.files) == 1 {
		j.data.Chapters = append([]db.Chapter{}, b.media.Chapters.V...) // the client shows an empty list, not null
	}
	j.Data = j.data
	h.mu.Lock()
	h.queued = append(h.queued, j)
	h.mu.Unlock()
	h.emit.ToAdmins("metadata_embed_queue_update", queueUpdate{b.item.ID, true})
}

// next starts the first queued embed unless one is running: they run one at a time.
func (h *Handler) next() {
	h.mu.Lock()
	if h.closed || len(h.queued) == 0 || slices.ContainsFunc(h.running, func(j *job) bool { return j.Action == "embed-metadata" }) {
		h.mu.Unlock()
		return
	}
	j := h.queued[0]
	h.queued = h.queued[1:]
	h.mu.Unlock()
	h.emit.ToAll("metadata_embed_queue_update", queueUpdate{j.itemID, false})
	h.start(j, func() error { return h.runEmbed(j) })
}

// runEmbed rewrites every track in place (through <track>.abs-tmp), optionally
// keeping the originals in the item cache.
func (h *Handler) runEmbed(j *job) error {
	b, err := h.loadBook(j.ctx, j.itemID)
	if err != nil {
		return err
	}
	backup := j.data.Options.BackupFiles
	if err = os.MkdirAll(j.cache, 0o755); err != nil {
		return err
	}
	metadata := filepath.Join(j.cache, "metadata.txt")
	if err = os.WriteFile(metadata, []byte(ffmetadata(b, len(b.files) == 1 || j.data.Options.ForceEmbedChapters)), 0o600); err != nil {
		return err
	}
	defer os.Remove(metadata)
	cover := b.cover()
	for i, f := range b.files {
		if _, err = os.Stat(f.path); err != nil {
			return fmt.Errorf("audio file %s: %w", f.path, err)
		}
		if backup {
			if err = copyFile(f.path, filepath.Join(j.cache, filepath.Base(f.path))); err != nil {
				return err
			}
		}
		out := f.path + ".abs-tmp"
		_ = os.Remove(out) // a leftover of an earlier attempt
		// Every input comes before the output options: an option that follows
		// "-i cover" and precedes it is read as the cover input's option.
		args := []string{"-y", "-i", f.path, "-i", metadata}
		if cover != "" {
			args = append(args, "-i", cover)
		}
		args = append(args, "-map", "0:a", "-map_metadata", "1", "-map_metadata", "0", "-map_chapters", "1")
		if cover != "" {
			args = append(args, "-map", "2:v?", "-disposition:v", "attached_pic")
		}
		args = append(args, "-c", "copy", "-f", muxer(f.path), out)
		if err = mediatools.Run(j.ctx, args...); err != nil {
			_ = os.Remove(out)
			return fmt.Errorf("ffmpeg metadata embed track %d: %w", i, err)
		}
		if err = os.Rename(out, f.path); err != nil {
			_ = os.Remove(out)
			return err
		}
		h.emit.ToAdmins("track_finished", trackFinished{b.item.ID, f.ino})
	}
	if !backup {
		_ = os.RemoveAll(j.cache)
	}
	return nil
}

// ffmetadata is the ffmpeg metadata file of the audiobook.
func ffmetadata(b book, withChapters bool) string {
	var out strings.Builder
	out.WriteString(";FFMETADATA1\n")
	title, subtitle := db.Val(b.media.Title), db.Val(b.media.Subtitle)
	album := title
	if subtitle != "" {
		album += ": " + subtitle
	}
	author := db.Val(b.item.AuthorNamesFirstLast)
	for _, field := range [][2]string{{"title", title}, {"artist", author}, {"album_artist", author}, {"album", album}, {"TIT3", subtitle},
		{"genre", strings.Join(b.media.Genres.V, "; ")}, {"date", db.Val(b.media.PublishedYear)}, {"comment", db.Val(b.media.Description)},
		{"description", db.Val(b.media.Description)}, {"composer", strings.Join(b.media.Narrators.V, ", ")}, {"copyright", db.Val(b.media.Publisher)},
		{"publisher", db.Val(b.media.Publisher)}, {"TRACKTOTAL", strconv.Itoa(len(b.files))}} {
		if field[1] != "" {
			fmt.Fprintf(&out, "%s=%s\n", field[0], ffescape(field[1]))
		}
	}
	if withChapters {
		for _, c := range b.media.Chapters.V {
			fmt.Fprintf(&out, "\n[CHAPTER]\nTIMEBASE=1/1000\nSTART=%d\nEND=%d\ntitle=%s\n", int64(c.Start*1000), int64(c.End*1000), ffescape(c.Title))
		}
	}
	return out.String()
}

var ffescaper = strings.NewReplacer("\\", "\\\\", ";", "\\;", "=", "\\=", "\n", "\\\n", "#", "\\#")

func ffescape(s string) string { return ffescaper.Replace(s) }

// muxers maps the extension of a track to the ffmpeg output format that keeps its container.
var muxers = map[string]string{".mp3": "mp3", ".m4b": "mp4", ".m4a": "mp4", ".mp4": "mp4", ".ogg": "ogg", ".oga": "ogg", ".flac": "flac"}

func muxer(path string) string {
	return cmp.Or(muxers[strings.ToLower(filepath.Ext(path))], "matroska")
}
