package item

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"slices"
	"strings"

	"audiobookshelf-compat/internal/auth"
	"audiobookshelf-compat/internal/db"
	"audiobookshelf-compat/internal/filekind"
	"audiobookshelf-compat/internal/httpx"
	"audiobookshelf-compat/internal/mediatools"
)

// editableAudio is the number of audio files a book plays: those not excluded.
func editableAudio(files []db.AudioFile) int {
	return len(slices.DeleteFunc(slices.Clone(files), func(f db.AudioFile) bool { return f.Exclude }))
}

// Refusals found inside a transaction, answered 400 once it has rolled back.
var (
	errUnknownTrack = errors.New("track is not an audio file of the book")
	errNotEbook     = errors.New("file is not an ebook of the item")
)

// chaptersResult is the body of POST /api/items/{id}/chapters.
type chaptersResult struct {
	Success bool `json:"success"`
	Updated bool `json:"updated"`
}

// updateChapters is LibraryItemController.updateMediaChapters (js:552-600).
// Order: 404, 403, then a 500 for a podcast, a missing item or a book without
// playable audio (upstream answers these with an error status and no message),
// then the body (400). Chapters keep their ids when only their count is equal.
func (h *Handler) updateChapters(w http.ResponseWriter, r *http.Request, user *auth.Principal) error {
	ctx := r.Context()
	ref, err := h.writable(ctx, user, r.PathValue("id"))
	if err != nil {
		return err
	}
	if ref.MediaType != "book" {
		return httpx.Error(http.StatusInternalServerError, "")
	}
	var book db.Book
	var missing db.Bool
	if err := h.db.Get(ctx, &book, `SELECT * FROM books WHERE id = ?`, ref.MediaID); err != nil {
		return httpx.Internal("database read failed", err)
	}
	if err := h.db.Get(ctx, &missing, `SELECT isMissing FROM libraryItems WHERE id = ?`, ref.ID); err != nil {
		return httpx.Internal("database read failed", err)
	}
	if bool(missing) || editableAudio(book.AudioFiles.V) == 0 {
		return httpx.Error(http.StatusInternalServerError, "")
	}
	body, err := httpx.DecodeLimit[struct {
		Chapters []db.Chapter `json:"chapters"` // nil (absent or null) is invalid, [] is valid
	}](w, r, 2<<20)
	if err != nil || body.Chapters == nil || slices.ContainsFunc(body.Chapters, func(c db.Chapter) bool { return c.Title == "" }) {
		return httpx.BadRequest("Invalid payload")
	}
	updated := false
	err = h.db.Tx(ctx, func(ctx context.Context) error {
		if err := h.db.Get(ctx, &book, `SELECT * FROM books WHERE id = ?`, ref.MediaID); err != nil {
			return err
		}
		previous := book.Chapters.V
		chapters := make([]db.Chapter, len(body.Chapters))
		updated = len(previous) != len(chapters)
		for i, c := range body.Chapters {
			chapters[i] = db.Chapter{ID: int64(i), Start: c.Start, End: c.End, Title: c.Title}
			if len(previous) == len(chapters) {
				chapters[i].ID = previous[i].ID
				updated = updated || previous[i].Title != c.Title || previous[i].Start != c.Start || previous[i].End != c.End
			}
		}
		if !updated {
			return nil
		}
		book.Chapters.V, book.UpdatedAt = chapters, db.Now()
		return h.db.Update(ctx, "books", &book, "chapters", "updatedAt")
	})
	if err != nil {
		return httpx.Internal("database write failed", err)
	}
	if updated {
		if err := h.ItemChanged(ctx, ref.ID); err != nil {
			return httpx.Internal("metadata file write failed", err)
		}
	}
	return httpx.JSON(w, r, chaptersResult{true, updated})
}

// trackOrder is one entry of the new track list.
type trackOrder struct {
	Ino     string `json:"ino"`
	Exclude bool   `json:"exclude"`
}

// updateTracks is LibraryItemController.updateTracks (js:602-640): the audio
// files become exactly the listed ones, in that order, numbered from 1 unless
// excluded (index -1, which sorts first). Order: 404, 403, 400 for a podcast,
// an unreadable or empty list, or an inode the book does not have.
func (h *Handler) updateTracks(w http.ResponseWriter, r *http.Request, user *auth.Principal) error {
	ctx := r.Context()
	ref, err := h.writable(ctx, user, r.PathValue("id"))
	if err != nil {
		return err
	}
	if ref.MediaType != "book" {
		return httpx.BadRequest("")
	}
	body, err := httpx.DecodeLimit[struct {
		Ordered []trackOrder `json:"orderedFileData"`
	}](w, r, 2<<20)
	if err != nil || len(body.Ordered) == 0 {
		return httpx.BadRequest("")
	}
	err = h.db.Tx(ctx, func(ctx context.Context) error {
		var book db.Book
		if err := h.db.Get(ctx, &book, `SELECT * FROM books WHERE id = ?`, ref.MediaID); err != nil {
			return err
		}
		files := make([]db.AudioFile, 0, len(body.Ordered))
		next := int64(1)
		for _, order := range body.Ordered {
			i := slices.IndexFunc(book.AudioFiles.V, func(f db.AudioFile) bool { return f.Ino == order.Ino })
			if order.Ino == "" || i < 0 {
				return errUnknownTrack
			}
			file := book.AudioFiles.V[i]
			file.ManuallyVerified, file.Exclude, file.Index = true, order.Exclude, -1
			if !order.Exclude {
				file.Index, next = next, next+1
			}
			files = append(files, file)
		}
		slices.SortStableFunc(files, func(a, b db.AudioFile) int { return int(a.Index - b.Index) })
		book.AudioFiles.V, book.UpdatedAt = files, db.Now()
		return h.db.Update(ctx, "books", &book, "audioFiles", "updatedAt")
	})
	if errors.Is(err, errUnknownTrack) {
		return httpx.BadRequest("")
	}
	if err != nil {
		return httpx.Internal("database write failed", err)
	}
	it, err := Get(ctx, h.db, ref.ID, Options{Episodes: true})
	if err != nil {
		return httpx.Internal("database read failed", err)
	}
	expanded := it.Expanded(h.prefixes())
	if h.emit != nil {
		h.emit.ToAll("item_updated", expanded)
	}
	return httpx.JSON(w, r, expanded)
}

// updateEbookStatus is LibraryItemController.updateEbookFileStatus: it makes
// the given ebook the book's primary ebook, or, when it already is, demotes it
// to supplementary. Order: 404, 403, 400 for a podcast or a file that is not
// an ebook of the item.
func (h *Handler) updateEbookStatus(w http.ResponseWriter, r *http.Request, user *auth.Principal) error {
	ctx := r.Context()
	ref, err := h.writable(ctx, user, r.PathValue("id"))
	if err != nil {
		return err
	}
	if ref.MediaType != "book" {
		return httpx.BadRequest("")
	}
	ino := r.PathValue("ino")
	err = h.db.Tx(ctx, func(ctx context.Context) error {
		files, err := h.libraryFiles(ctx, ref.ID)
		if err != nil {
			return err
		}
		i := slices.IndexFunc(files, func(f db.LibraryFile) bool { return f.Ino == ino && fileKind(f.Metadata) == filekind.Ebook })
		if i < 0 {
			return errNotEbook
		}
		var book db.Book
		if err := h.db.Get(ctx, &book, `SELECT * FROM books WHERE id = ?`, ref.MediaID); err != nil {
			return err
		}
		book.EbookFile.V = nil
		if db.Val(files[i].IsSupplementary) {
			// EBookFile.toJSON: the library file without its supplementary flag
			// and file type, plus the format (extension without dot, lower case).
			book.EbookFile.V = &db.EbookFile{Ino: files[i].Ino, Metadata: files[i].Metadata, AddedAt: files[i].AddedAt, UpdatedAt: files[i].UpdatedAt,
				EbookFormat: strings.ToLower(strings.TrimPrefix(files[i].Metadata.Ext, "."))}
		}
		for j := range files {
			if fileKind(files[j].Metadata) == filekind.Ebook {
				files[j].IsSupplementary = db.Ptr(book.EbookFile.V == nil || files[j].Ino != ino)
			}
		}
		now := db.Now()
		book.UpdatedAt = now
		item := db.LibraryItem{ID: ref.ID, UpdatedAt: now, IsMissing: db.Bool(book.EbookFile.V == nil && editableAudio(book.AudioFiles.V) == 0)}
		item.LibraryFiles.V = files
		if err := h.db.Update(ctx, "books", &book, "ebookFile", "updatedAt"); err != nil {
			return err
		}
		return h.db.Update(ctx, "libraryItems", &item, "libraryFiles", "isMissing", "updatedAt")
	})
	if errors.Is(err, errNotEbook) {
		return httpx.BadRequest("Invalid ebook file id")
	}
	if err != nil {
		return httpx.Internal("database write failed", err)
	}
	if err := h.announce(ctx, ref.ID); err != nil {
		return httpx.Internal("database read failed", err)
	}
	return httpx.Status(w, http.StatusOK)
}

// audioTags is the body of GET /api/items/{id}/metadata-object: the tags an
// audio file of the book should carry (server/utils/ffmpegHelpers.js
// writeMetadata input). Empty tags are left out.
type audioTags struct {
	Title       string `json:"title,omitempty"`
	Artist      string `json:"artist,omitempty"`
	AlbumArtist string `json:"album_artist,omitempty"`
	Album       string `json:"album,omitempty"`
	Subtitle    string `json:"TIT3,omitempty"`
	Genre       string `json:"genre,omitempty"`
	Date        string `json:"date,omitempty"`
	Comment     string `json:"comment,omitempty"`
	Description string `json:"description,omitempty"`
	Composer    string `json:"composer,omitempty"`
	Copyright   string `json:"copyright,omitempty"`
	Publisher   string `json:"publisher,omitempty"`
	TrackTotal  string `json:"TRACKTOTAL"`
	Grouping    string `json:"grouping,omitempty"`
}

// metadataObject is LibraryItemController.getMetadataObject. Admin only (403
// before anything else); 404, 403 (library); 400 unless the item is a book
// that is not missing and has playable audio.
func (h *Handler) metadataObject(w http.ResponseWriter, r *http.Request, user *auth.Principal) error {
	if !user.IsAdmin() {
		return httpx.Forbidden()
	}
	it, err := Get(r.Context(), h.db, r.PathValue("id"), Options{})
	if errors.Is(err, sql.ErrNoRows) {
		return httpx.NotFound()
	}
	if err != nil {
		return httpx.Internal("database read failed", err)
	}
	if !user.CanAccessLibrary(db.Val(it.LibraryID)) {
		return httpx.Forbidden()
	}
	if it.Book == nil || bool(it.IsMissing) || editableAudio(it.Book.AudioFiles.V) == 0 {
		return httpx.BadRequest("")
	}
	b := it.Book
	authors := make([]string, len(b.Authors))
	for i, a := range b.Authors {
		authors[i] = a.Name
	}
	groups := make([]string, len(b.Series))
	for i, s := range b.Series {
		groups[i] = strings.TrimSpace(s.Name + " #" + db.Val(s.Sequence))
		if db.Val(s.Sequence) == "" {
			groups[i] = s.Name
		}
	}
	album := db.Val(b.Title)
	if subtitle := db.Val(b.Subtitle); subtitle != "" {
		album += ": " + subtitle
	}
	names := strings.Join(authors, ", ")
	return httpx.JSON(w, r, audioTags{
		Title: db.Val(b.Title), Artist: names, AlbumArtist: names, Album: album, Subtitle: db.Val(b.Subtitle),
		Genre: strings.Join(b.Genres.V, "; "), Date: db.Val(b.PublishedYear), Comment: db.Val(b.Description), Description: db.Val(b.Description),
		Composer: strings.Join(b.Narrators.V, ", "), Copyright: db.Val(b.Publisher), Publisher: db.Val(b.Publisher),
		TrackTotal: fmt.Sprint(editableAudio(b.AudioFiles.V)), Grouping: strings.Join(groups, "; "),
	})
}

// ffprobe is LibraryItemController.getFFprobeData: ffprobe's report of one
// audio file of the item (a book's track or a podcast episode's file). Admin
// only; 404, 403 (library), 404 for an unknown or outside file.
func (h *Handler) ffprobe(w http.ResponseWriter, r *http.Request, user *auth.Principal) error {
	ctx := r.Context()
	if !user.IsAdmin() {
		return httpx.Forbidden()
	}
	ref, err := h.readable(ctx, user, r.PathValue("id"))
	if err != nil {
		return err
	}
	var files []db.AudioFile
	switch ref.MediaType {
	case "book":
		var book db.Book
		if err := h.db.Get(ctx, &book, `SELECT * FROM books WHERE id = ?`, ref.MediaID); err != nil {
			return httpx.Internal("database read failed", err)
		}
		files = book.AudioFiles.V
	case "podcast":
		var episodes []db.PodcastEpisode
		if err := h.db.Select(ctx, &episodes, `SELECT * FROM podcastEpisodes WHERE podcastId = ?`, ref.MediaID); err != nil {
			return httpx.Internal("database read failed", err)
		}
		for _, e := range episodes {
			files = append(files, e.AudioFile.V)
		}
	}
	i := slices.IndexFunc(files, func(f db.AudioFile) bool { return f.Ino == r.PathValue("ino") })
	if i < 0 || files[i].Metadata.Path == "" || !h.inLibrary(ctx, files[i].Metadata.Path, ref.LibraryID) {
		return httpx.NotFound()
	}
	report, err := mediatools.ProbeRaw(ctx, files[i].Metadata.Path)
	if err != nil {
		slog.Error("ffprobe", "path", files[i].Metadata.Path, "error", err)
		return httpx.Internal("ffprobe failed", err)
	}
	return httpx.JSON(w, r, report)
}
