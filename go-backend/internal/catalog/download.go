// Item download endpoint, mirroring upstream GET /api/items/:id/download
// (LibraryItemController.download + server/utils/zipHelpers.js).
package catalog

import (
	"archive/zip"
	"database/sql"
	"encoding/json"
	"io"
	"io/fs"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"audiobookshelf-compat/internal/store"
)

// audioMIMEType mirrors utils/constants.js AudioMimeType +
// getAudioMimeTypeFromExtname (fileUtils.js:424-431); unknown extensions yield
// "" and leave the default Content-Type in place.
func audioMIMEType(ext string) string {
	formatUpper := strings.TrimPrefix(strings.ToUpper(strings.TrimPrefix(ext, ".")), ".")
	return map[string]string{
		"MP3":   "audio/mpeg",
		"M4B":   "audio/mp4",
		"M4A":   "audio/mp4",
		"MP4":   "audio/mp4",
		"OGG":   "audio/ogg",
		"OGA":   "audio/ogg",
		"OPUS":  "audio/ogg",
		"AAC":   "audio/aac",
		"FLAC":  "audio/flac",
		"WMA":   "audio/x-ms-wma",
		"AIFF":  "audio/x-aiff",
		"AIF":   "audio/x-aiff",
		"WEBM":  "audio/webm",
		"WEBMA": "audio/webm",
		"MKA":   "audio/x-matroska",
		"AWB":   "audio/amr-wb",
		"CAF":   "audio/x-caf",
		"MPEG":  "audio/mpeg",
		"MPG":   "audio/mpeg",
	}[formatUpper]
}

// itemDownload streams a library item: a single-file item is served directly
// (Express res.download semantics: attachment disposition, configured audio
// mime type, byte-range support via http.ServeFile); a directory item is
// streamed as a STORE-mode zip named "<media title>.zip" because upstream
// zips with compression level 0 (zipHelpers.js:10-13). Disk errors keep the
// upstream messages: ENOENT → 404 'File not found', otherwise 500
// 'Download failed' (LibraryItemController.js:152-160) — only until headers
// are sent, exactly like the source.
func (h *Handler) itemDownload(w http.ResponseWriter, r *http.Request, user store.User, itemID string) {
	var libraryID, itemPath, relPath, mediaType, title string
	var isFile int
	title = ""
	err := h.db.QueryRowContext(r.Context(), `SELECT li.libraryId, li.path, li.relPath, COALESCE(li.isFile,0), li.mediaType, COALESCE(b.title, p.title) FROM libraryItems li LEFT JOIN books b ON b.id = li.mediaId AND li.mediaType = 'book' LEFT JOIN podcasts p ON p.id = li.mediaId AND li.mediaType = 'podcast' WHERE li.id = ?`, itemID).Scan(&libraryID, &itemPath, &relPath, &isFile, &mediaType, &title)
	if err != nil {
		if err == sql.ErrNoRows {
			writeError(w, http.StatusNotFound, "")
			return
		}
		writeError(w, http.StatusInternalServerError, "database read failed")
		return
	}
	if !userCanAccessLibrary(user, libraryID) {
		writeStatus(w, http.StatusForbidden)
		return
	}
	var permissions map[string]any
	_ = json.Unmarshal(user.Permissions, &permissions)
	if !user.IsActive || permissions["download"] != true {
		writeStatus(w, http.StatusForbidden)
		return
	}
	if isFile != 0 {
		if mimeType := audioMIMEType(filepath.Ext(itemPath)); mimeType != "" {
			w.Header().Set("Content-Type", mimeType)
		}
	}
	itemPath = filepath.Clean(itemPath)
	if _, statErr := os.Stat(itemPath); os.IsNotExist(statErr) {
		sendDownloadText(w, http.StatusNotFound, "File not found")
		return
	}
	if isFile != 0 {
		name := filepath.Base(relPath)
		if name == "" || name == "." || name == string(filepath.Separator) {
			name = filepath.Base(itemPath)
		}
		w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": name}))
		ext := strings.ToLower(filepath.Ext(itemPath))
		if mimeType := audioMIMEType(ext); mimeType != "" {
			w.Header().Set("Content-Type", mimeType)
		}
		http.ServeFile(w, r, itemPath)
		return
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
	walkErr := filepath.WalkDir(itemPath, func(p string, entry fs.DirEntry, walkErr error) error {
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
	if walkErr != nil {
		// Headers are already sent; upstream logs and ends the response the
		// same way once the zip has started streaming.
		_ = walkErr
	}
	_ = zipWriter.Close()
}

// sendDownloadText matches res.status(...).send('...'), preserving a MIME type
// already selected for a file download while adding Express's string charset.
func sendDownloadText(w http.ResponseWriter, status int, message string) {
	body := []byte(message)
	contentType := w.Header().Get("Content-Type")
	if contentType == "" {
		contentType = "text/html; charset=utf-8"
	} else if !strings.Contains(strings.ToLower(contentType), "charset=") {
		contentType += "; charset=utf-8"
	}
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Content-Length", strconv.Itoa(len(body)))
	w.Header().Set("ETag", expressETag(body))
	w.WriteHeader(status)
	_, _ = w.Write(body)
}
