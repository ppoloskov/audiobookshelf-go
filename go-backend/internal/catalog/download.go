// Item download endpoint, mirroring upstream GET /api/items/:id/download
// (LibraryItemController.download + server/utils/zipHelpers.js).
package catalog

import (
	"archive/zip"
	"audiobookshelf-compat/internal/access"
	"audiobookshelf-compat/internal/filekind"
	"audiobookshelf-compat/internal/httputil"
	"database/sql"
	"io"
	"io/fs"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

// itemDownload streams a library item: a single-file item is served directly
// (Express res.download semantics: attachment disposition, configured audio
// mime type, byte-range support via http.ServeFile); a directory item is
// streamed as a STORE-mode zip named "<media title>.zip" because upstream
// zips with compression level 0 (zipHelpers.js:10-13). Disk errors keep the
// upstream messages: ENOENT → 404 'File not found', otherwise 500
// 'Download failed' (LibraryItemController.js:152-160) — only until headers
// are sent, exactly like the source.
func (h *Handler) itemDownload(w http.ResponseWriter, r *http.Request) error {
	user, itemID := requestUser(r), r.PathValue("id")
	var libraryID, itemPath, relPath, mediaType, title string
	var isFile int
	title = ""
	err := h.db.QueryRowContext(r.Context(), `SELECT li.libraryId, li.path, li.relPath, COALESCE(li.isFile,0), li.mediaType, COALESCE(b.title, p.title) FROM libraryItems li LEFT JOIN books b ON b.id = li.mediaId AND li.mediaType = 'book' LEFT JOIN podcasts p ON p.id = li.mediaId AND li.mediaType = 'podcast' WHERE li.id = ?`, itemID).Scan(&libraryID, &itemPath, &relPath, &isFile, &mediaType, &title)
	if err != nil {
		if err == sql.ErrNoRows {
			return httputil.Status(http.StatusNotFound, "")
		}
		return httputil.Internal("database read failed", err)
	}
	if !access.For(r.Context(), user).Library(libraryID) {
		return httputil.Status(http.StatusForbidden, "")
	}
	if !access.For(r.Context(), user).Download() {
		return httputil.Status(http.StatusForbidden, "")
	}
	if isFile != 0 {
		if mimeType := filekind.KnownAudioMime(filepath.Ext(itemPath)); mimeType != "" {
			w.Header().Set("Content-Type", mimeType)
		}
	}
	itemPath = filepath.Clean(itemPath)
	if _, statErr := os.Stat(itemPath); os.IsNotExist(statErr) {
		return httputil.Status(http.StatusNotFound, "File not found")
	}
	if isFile != 0 {
		name := filepath.Base(relPath)
		if name == "" || name == "." || name == string(filepath.Separator) {
			name = filepath.Base(itemPath)
		}
		w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": name}))
		ext := strings.ToLower(filepath.Ext(itemPath))
		if mimeType := filekind.KnownAudioMime(ext); mimeType != "" {
			w.Header().Set("Content-Type", mimeType)
		}
		http.ServeFile(w, r, itemPath)
		return nil
	}
	name := title
	if name == "" {
		name = filepath.Base(itemPath)
	}
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": name + ".zip"}))
	// Upstream streams the archive without a Content-Length header (Express
	// chunked encoding); flushing immediately prevents net/http from buffering
	// small archives into a single content-length body.
	w.WriteHeader(http.StatusOK)
	if flusher, ok := w.(http.Flusher); ok {
		flusher.Flush()
	}
	zipWriter := zip.NewWriter(w)
	// Headers are already sent; upstream logs and ends the response the same
	// way once the zip has started streaming.
	_ = filepath.WalkDir(itemPath, func(p string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		info, infoErr := entry.Info()
		if infoErr != nil {
			return infoErr
		}
		rel, relErr := filepath.Rel(itemPath, p)
		if relErr != nil {
			return relErr
		}
		header := &zip.FileHeader{Name: filepath.ToSlash(rel), Method: zip.Store, Modified: info.ModTime()}
		header.SetMode(info.Mode())
		target, openErr := zipWriter.CreateHeader(header)
		if openErr != nil {
			return openErr
		}
		source, openErr := os.Open(p)
		if openErr != nil {
			return openErr
		}
		defer source.Close()
		if _, copyErr := io.Copy(target, source); copyErr != nil {
			return copyErr
		}
		return nil
	})
	_ = zipWriter.Close()
	return nil
}
