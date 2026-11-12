package libraries

import (
	"archive/zip"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"audiobookshelf-compat/internal/db"
	"audiobookshelf-compat/internal/httpx"
)

// download is GET /api/libraries/{id}/download?ids=a,b (LibraryController.
// downloadMultiple, js:1422-1465): the named items of this library as one zip,
// each under its folder name (a single-file item as that file), stored without
// compression and streamed. Needs the download permission and read access to
// every item.
func (h *Handler) download(w http.ResponseWriter, r *http.Request, scope Scope) error {
	ctx, user := r.Context(), scope.User
	if !user.CanDownload() {
		slog.Warn("user attempted to download without permission", "user", db.Val(user.Username))
		return httpx.Forbidden()
	}
	param := r.URL.Query()["ids"]
	if len(param) != 1 || param[0] == "" {
		return httpx.BadRequest("Invalid request. ids must be a string")
	}
	itemIDs := strings.Split(param[0], ",")
	// The order Sequelize returns for LibraryItem.findAllExpandedWhere
	// (LibraryItem.js:150-183): by the first joined row, ordered by bookAuthor
	// then bookSeries creation time.
	rows, err := selectAll[struct {
		ID        string `db:"id"`
		LibraryID string `db:"libraryId"`
		Path      string `db:"path"`
	}](ctx, h.db, `SELECT li.id AS id, COALESCE(li.libraryId,'') AS libraryId, COALESCE(li.path,'') AS path FROM libraryItems li LEFT JOIN books b ON b.id=li.mediaId AND li.mediaType='book' LEFT JOIN bookAuthors ba ON ba.bookId=b.id LEFT JOIN bookSeries bs ON bs.bookId=b.id WHERE li.id IN (?) ORDER BY ba.createdAt ASC, bs.createdAt ASC`, itemIDs)
	if err != nil {
		return httpx.Internal("database read failed", err)
	}
	var paths []string
	seen := map[string]bool{}
	for _, row := range rows {
		if row.LibraryID != scope.ID || seen[row.ID] {
			continue
		}
		seen[row.ID] = true
		if _, exists, allowed, err := user.ReadItem(ctx, h.db, row.ID); err != nil {
			return httpx.Internal("database read failed", err)
		} else if !exists || !allowed {
			return httpx.Forbidden()
		}
		paths = append(paths, row.Path)
	}
	if len(paths) == 0 {
		return httpx.Error(http.StatusNotFound, "Library items not found")
	}
	slog.Info("library download", "user", db.Val(user.Username), "items", len(paths))
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", `attachment; filename="LibraryItems-`+strconv.FormatInt(time.Now().UnixMilli(), 10)+`.zip"`)
	w.WriteHeader(http.StatusOK)
	if flusher, ok := w.(http.Flusher); ok {
		flusher.Flush() // no Content-Length: the archive is streamed
	}
	archive := zip.NewWriter(w)
	for _, path := range paths {
		if err := addToZip(archive, path); err != nil {
			// The status is sent; upstream logs the archiver error and ends the response.
			slog.Error("library download: archive", "path", path, "error", err)
			break
		}
	}
	_ = archive.Close() // a client that went away is not a server error
	return nil
}

// addToZip stores the file at path as its base name, or every file below a
// folder as base/relative-path.
func addToZip(archive *zip.Writer, path string) error {
	base := filepath.Base(path)
	return filepath.WalkDir(path, func(file string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return err
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		name := base
		if rel, _ := filepath.Rel(path, file); rel != "." {
			name = base + "/" + filepath.ToSlash(rel)
		}
		header := &zip.FileHeader{Name: name, Method: zip.Store, Modified: info.ModTime()}
		header.SetMode(info.Mode())
		out, err := archive.CreateHeader(header)
		if err != nil {
			return err
		}
		source, err := os.Open(file)
		if err != nil {
			return err
		}
		defer source.Close()
		_, err = io.Copy(out, source)
		return err
	})
}
