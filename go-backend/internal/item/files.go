package item

import (
	"archive/zip"
	"context"
	"database/sql"
	"errors"
	"io"
	"io/fs"
	"log/slog"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"audiobookshelf-compat/internal/auth"
	"audiobookshelf-compat/internal/db"
	"audiobookshelf-compat/internal/filekind"
	"audiobookshelf-compat/internal/httpx"
	"audiobookshelf-compat/internal/playlistview"
	"audiobookshelf-compat/internal/store"
)

// readable loads the item (404) and checks the user's library access (403).
func (h *Handler) readable(ctx context.Context, user *auth.Principal, id string) (itemRef, error) {
	ref, err := h.loadRef(ctx, id)
	if err == nil && !user.CanAccessLibrary(ref.LibraryID) {
		err = httpx.Forbidden()
	}
	return ref, err
}

// writable is readable plus the update permission (403).
func (h *Handler) writable(ctx context.Context, user *auth.Principal, id string) (itemRef, error) {
	ref, err := h.readable(ctx, user, id)
	if err == nil && !user.CanUpdate() {
		err = httpx.Forbidden()
	}
	return ref, err
}

// libraryFiles is the item's libraryFiles column.
func (h *Handler) libraryFiles(ctx context.Context, itemID string) ([]db.LibraryFile, error) {
	var row struct {
		Files db.JSON[[]db.LibraryFile] `db:"libraryFiles"`
	}
	if err := h.db.Get(ctx, &row, `SELECT libraryFiles FROM libraryItems WHERE id = ?`, itemID); err != nil {
		return nil, httpx.Internal("database read failed", err)
	}
	return row.Files.V, nil
}

// fileKind is LibraryFile.fileType, judged by the extension.
func fileKind(f db.FileMetadata) string { return filekind.Of("." + strings.TrimPrefix(f.Ext, ".")) }

// inLibrary reports whether target, with symlinks resolved, lies inside one of
// the library's folders. Files are served and removed only from there.
func (h *Handler) inLibrary(ctx context.Context, target, libraryID string) bool {
	var roots []string
	if err := h.db.Select(ctx, &roots, `SELECT path FROM libraryFolders WHERE libraryId = ? AND path IS NOT NULL`, libraryID); err != nil {
		slog.Error("folders of library", "library", libraryID, "error", err)
		return false
	}
	return within(target, roots)
}

// libraryFile finds the item's library file with inode ino and checks that it
// lies inside the library: 404 otherwise.
func (h *Handler) libraryFile(ctx context.Context, ref itemRef, ino string) (db.LibraryFile, error) {
	files, err := h.libraryFiles(ctx, ref.ID)
	if err != nil {
		return db.LibraryFile{}, err
	}
	i := slices.IndexFunc(files, func(f db.LibraryFile) bool { return f.Ino == ino })
	if i < 0 || files[i].Metadata.Path == "" || !h.inLibrary(ctx, files[i].Metadata.Path, ref.LibraryID) {
		return db.LibraryFile{}, httpx.NotFound()
	}
	return files[i], nil
}

// audioType sets the Content-Type of a known audio extension; other files get
// the type net/http derives from the name.
func audioType(w http.ResponseWriter, path string) {
	if kind := filekind.KnownAudioMime(filepath.Ext(path)); kind != "" {
		w.Header().Set("Content-Type", kind)
	}
}

// serveFile is LibraryItemController.getLibraryFile: one file of the item,
// with ranges. Order: 404, 403 (library), 404 for an unknown or outside file.
func (h *Handler) serveFile(w http.ResponseWriter, r *http.Request, user *auth.Principal) error {
	ref, err := h.readable(r.Context(), user, r.PathValue("id"))
	if err != nil {
		return err
	}
	file, err := h.libraryFile(r.Context(), ref, r.PathValue("ino"))
	if err != nil {
		return err
	}
	audioType(w, file.Metadata.Path)
	return httpx.ServeFile(w, r, file.Metadata.Path)
}

// downloadFile is serveFile as an attachment; it needs the download permission,
// which is checked after the file is found (js:675-707).
func (h *Handler) downloadFile(w http.ResponseWriter, r *http.Request, user *auth.Principal) error {
	ref, err := h.readable(r.Context(), user, r.PathValue("id"))
	if err != nil {
		return err
	}
	file, err := h.libraryFile(r.Context(), ref, r.PathValue("ino"))
	if err != nil {
		return err
	}
	if !user.CanDownload() {
		return httpx.Forbidden()
	}
	audioType(w, file.Metadata.Path)
	return httpx.Download(w, r, file.Metadata.Path, filepath.Base(file.Metadata.Path))
}

// serveEbook is LibraryItemController.getEBookFile: the ebook file with the
// given inode, or without one the book's primary ebook. Order: 404, 403, then
// 400 for an inode that is not an ebook of the item.
func (h *Handler) serveEbook(w http.ResponseWriter, r *http.Request, user *auth.Principal) error {
	ctx := r.Context()
	ref, err := h.readable(ctx, user, r.PathValue("id"))
	if err != nil {
		return err
	}
	var path string
	if fileID := r.PathValue("fileId"); fileID != "" {
		files, err := h.libraryFiles(ctx, ref.ID)
		if err != nil {
			return err
		}
		i := slices.IndexFunc(files, func(f db.LibraryFile) bool { return f.Ino == fileID && fileKind(f.Metadata) == filekind.Ebook })
		if i < 0 {
			return httpx.BadRequest("Invalid ebook file id")
		}
		path = files[i].Metadata.Path
	} else if ref.MediaType == "book" {
		var book db.Book
		if err := h.db.Get(ctx, &book, `SELECT * FROM books WHERE id = ?`, ref.MediaID); err != nil {
			return httpx.NotFound()
		}
		if ebook := book.EbookFile.V; ebook != nil {
			path = ebook.Metadata.Path
		}
	}
	if path == "" || !h.inLibrary(ctx, path, ref.LibraryID) {
		return httpx.NotFound()
	}
	return httpx.ServeFile(w, r, path)
}

// deleteFile is LibraryItemController.deleteLibraryFile (js:709-749): the file
// leaves the item's records and then the disk. A book left with no audio and no
// ebook is marked missing; a podcast loses the episode that used the file.
// Order: 404, 403 (library, update), 404 for an unknown or outside file.
func (h *Handler) deleteFile(w http.ResponseWriter, r *http.Request, user *auth.Principal) error {
	ctx := r.Context()
	ref, err := h.writable(ctx, user, r.PathValue("id"))
	if err != nil {
		return err
	}
	if ref.MediaType != "book" && ref.MediaType != "podcast" {
		return httpx.BadRequest("")
	}
	ino := r.PathValue("ino")
	removed, err := h.libraryFile(ctx, ref, ino)
	if err != nil {
		return err
	}
	var playlists []store.PlaylistChange
	err = h.db.Tx(ctx, func(ctx context.Context) error {
		files, err := h.libraryFiles(ctx, ref.ID)
		if err != nil {
			return err
		}
		item := db.LibraryItem{ID: ref.ID, UpdatedAt: db.Now()}
		item.LibraryFiles.V = slices.DeleteFunc(slices.Clone(files), func(f db.LibraryFile) bool { return f.Ino == ino })
		if ref.MediaType == "book" {
			err = h.dropBookFile(ctx, ref, ino, &item)
		} else {
			playlists, err = h.dropEpisode(ctx, ref, ino)
		}
		if err != nil {
			return err
		}
		cols := []string{"libraryFiles", "updatedAt"}
		if ref.MediaType == "book" {
			cols = append(cols, "isMissing")
		}
		return h.db.Update(ctx, "libraryItems", &item, cols...)
	})
	if errors.Is(err, sql.ErrNoRows) { // no episode uses the file
		return httpx.NotFound()
	}
	if err != nil {
		return httpx.Internal("database write failed", err)
	}
	if err := os.Remove(removed.Metadata.Path); err != nil {
		slog.Warn("remove library file", "path", removed.Metadata.Path, "error", err)
	}
	playlistview.Announce(ctx, h.db.SQL(), h.emit, playlists)
	if err := h.announce(ctx, ref.ID); err != nil {
		return httpx.Internal("database read failed", err)
	}
	return httpx.Status(w, http.StatusOK)
}

// dropBookFile removes the file from the book's audio files and ebook and sets
// item.IsMissing for the result.
func (h *Handler) dropBookFile(ctx context.Context, ref itemRef, ino string, item *db.LibraryItem) error {
	var book db.Book
	if err := h.db.Get(ctx, &book, `SELECT * FROM books WHERE id = ?`, ref.MediaID); err != nil {
		return err
	}
	audio := slices.DeleteFunc(slices.Clone(book.AudioFiles.V), func(f db.AudioFile) bool { return f.Ino == ino })
	if audio == nil {
		audio = []db.AudioFile{}
	}
	ebookGone := book.EbookFile.V != nil && book.EbookFile.V.Ino == ino
	if len(audio) != len(book.AudioFiles.V) || ebookGone {
		book.AudioFiles.V = audio
		if ebookGone {
			book.EbookFile.V = nil
		}
		book.UpdatedAt = db.Now()
		if err := h.db.Update(ctx, "books", &book, "audioFiles", "ebookFile", "updatedAt"); err != nil {
			return err
		}
	}
	item.IsMissing = db.Bool(len(audio) == 0 && book.EbookFile.V == nil)
	return nil
}

// dropEpisode removes the podcast episode whose audio file has inode ino, with
// its progress and playlist entries (sql.ErrNoRows when there is none).
func (h *Handler) dropEpisode(ctx context.Context, ref itemRef, ino string) ([]store.PlaylistChange, error) {
	var episodeID string
	if err := h.db.Get(ctx, &episodeID, `SELECT id FROM podcastEpisodes WHERE podcastId = ? AND json_extract(audioFile, '$.ino') = ?`, ref.MediaID, ino); err != nil {
		return nil, err
	}
	changes, err := h.removeFromPlaylists(ctx, []string{episodeID})
	if err != nil {
		return nil, err
	}
	for _, query := range []string{`DELETE FROM mediaProgresses WHERE mediaItemId = ?`, `DELETE FROM podcastEpisodes WHERE id = ?`} {
		if _, err := h.db.Exec(ctx, query, episodeID); err != nil {
			return nil, err
		}
	}
	return changes, nil
}

// downloadItem is LibraryItemController.download (js:130-168): a single-file
// item as itself, any other as a zip of its folder. The zip stores files
// without compression (zipHelpers.js:10-13) and is streamed, so it has no
// Content-Length. Order: 404, 403 (library, download), 404 for a missing path.
func (h *Handler) downloadItem(w http.ResponseWriter, r *http.Request, user *auth.Principal) error {
	it, err := Get(r.Context(), h.db, r.PathValue("id"), Options{})
	if errors.Is(err, sql.ErrNoRows) {
		return httpx.NotFound()
	}
	if err != nil {
		return httpx.Internal("database read failed", err)
	}
	if !user.CanAccessLibrary(db.Val(it.LibraryID)) || !user.CanDownload() {
		return httpx.Forbidden()
	}
	path := filepath.Clean(db.Val(it.Path))
	if it.IsFile {
		audioType(w, path)
		return httpx.Download(w, r, path, filepath.Base(db.Val(it.RelPath)))
	}
	if info, err := os.Stat(path); err != nil || !info.IsDir() {
		return httpx.NotFound()
	}
	title := filepath.Base(path)
	switch {
	case it.Book != nil && db.Val(it.Book.Title) != "":
		title = db.Val(it.Book.Title)
	case it.Podcast != nil && db.Val(it.Podcast.Title) != "":
		title = db.Val(it.Podcast.Title)
	}
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": title + ".zip"}))
	w.WriteHeader(http.StatusOK)
	if flusher, ok := w.(http.Flusher); ok {
		flusher.Flush() // no Content-Length: the archive is streamed
	}
	zipFolder(w, path)
	return nil
}

// zipFolder writes the files under root to w as a stored zip. The status is
// already sent, so a failure is logged and ends the archive.
func zipFolder(w http.ResponseWriter, root string) {
	archive := zip.NewWriter(w)
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return err
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		name, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		header := &zip.FileHeader{Name: filepath.ToSlash(name), Method: zip.Store, Modified: info.ModTime()}
		header.SetMode(info.Mode())
		out, err := archive.CreateHeader(header)
		if err != nil {
			return err
		}
		source, err := os.Open(path)
		if err != nil {
			return err
		}
		defer source.Close()
		_, err = io.Copy(out, source)
		return err
	})
	if err = errors.Join(err, archive.Close()); err != nil {
		slog.Error("zip item", "path", root, "error", err)
	}
}
