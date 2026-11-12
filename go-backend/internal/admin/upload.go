package admin

import (
	"database/sql"
	"errors"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"unicode/utf16"
	"unicode/utf8"

	"audiobookshelf-compat/internal/auth"
	"audiobookshelf-compat/internal/db"
	"audiobookshelf-compat/internal/httpx"
)

const maxUploadBytes = 20 << 30 // one request: whole audiobooks are uploaded here

// upload stores the posted files in <library folder>/<author>/<series>/<title>
// (MiscController.handleUpload).
func (h *Handler) upload(w http.ResponseWriter, r *http.Request) error {
	user := auth.User(r.Context())
	if !user.CanUpload() {
		return httpx.Forbidden()
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxUploadBytes)
	if err := r.ParseMultipartForm(32 << 20); err != nil || len(r.MultipartForm.File) == 0 {
		return httpx.BadRequest("")
	}
	defer r.MultipartForm.RemoveAll() //nolint:errcheck // temp files of a finished request
	title, libraryID, folderID := r.FormValue("title"), r.FormValue("library"), r.FormValue("folder")
	if title == "" || libraryID == "" || folderID == "" {
		return httpx.BadRequest("Invalid request body")
	}
	ctx := r.Context()
	var library db.Library
	if err := h.db.Get(ctx, &library, "SELECT * FROM libraries WHERE id = ?", libraryID); err != nil {
		return notFoundOr(err, "Library not found")
	}
	if !user.IsAdmin() && !user.CanAccessLibrary(libraryID) {
		return httpx.Forbidden()
	}
	var folder db.LibraryFolder
	if err := h.db.Get(ctx, &folder, "SELECT * FROM libraryFolders WHERE id = ? AND libraryId = ?", folderID, libraryID); err != nil {
		return notFoundOr(err, "Folder not found")
	}
	parts := []string{title}
	if db.Val(library.MediaType) != "podcast" {
		parts = []string{r.FormValue("author"), r.FormValue("series"), title}
	}
	dir := db.Val(folder.Path)
	for _, part := range parts {
		if part != "" {
			dir = filepath.Join(dir, sanitizeUploadName(part))
		}
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return httpx.Internal("", err)
	}
	// A file that cannot be stored is logged and skipped; the request still
	// answers 200 (MiscController.handleUpload, js:92-104).
	for _, headers := range r.MultipartForm.File {
		for _, header := range headers {
			name := sanitizeUploadName(header.Filename)
			if name == "" {
				continue
			}
			if err := storeUpload(header, filepath.Join(dir, name)); err != nil {
				slog.Error("[MiscController] Failed to move file", "path", filepath.Join(dir, name), "error", err)
			}
		}
	}
	return httpx.Status(w, http.StatusOK)
}

// notFoundOr answers a missing row as 404 with message, anything else as 500.
func notFoundOr(err error, message string) error {
	if errors.Is(err, sql.ErrNoRows) {
		return httpx.Error(http.StatusNotFound, message)
	}
	return httpx.Internal("", err)
}

func storeUpload(header *multipart.FileHeader, path string) error {
	in, err := header.Open()
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(path, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	_, err = io.Copy(out, in)
	return errors.Join(err, out.Close())
}

var (
	illegalUploadChars = regexp.MustCompile(`[/?<>\\:*|"\x00-\x1f\x80-\x9f]`)
	spaceRun           = regexp.MustCompile(`\s+`)
	reservedUploadName = regexp.MustCompile(`(?i)^(con|prn|aux|nul|com[0-9]|lpt[0-9])(\..*)?$`)
)

// sanitizeUploadName is sanitize-filename plus upstream's length cut (255
// UTF-16 units, the extension kept); the result names a folder or file on
// disk. It returns "" for a name that cannot be used.
func sanitizeUploadName(name string) string {
	name = strings.Replace(name, ":", " - ", 1)
	name = illegalUploadChars.ReplaceAllString(name, "")
	if strings.Trim(name, ".") == "" || reservedUploadName.MatchString(name) {
		return ""
	}
	name = spaceRun.ReplaceAllString(strings.TrimRight(name, ". "), " ")
	if ext := filepath.Ext(name); len(utf16.Encode([]rune(name))) > 255 {
		base := strings.TrimSuffix(name, ext)
		for len(utf16.Encode([]rune(base))) > 255-len(utf16.Encode([]rune(ext))) {
			_, size := utf8.DecodeLastRuneInString(base)
			base = base[:len(base)-size]
		}
		name = strings.TrimSpace(base) + ext
	}
	return name
}
