package adminops

import (
	"database/sql"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"unicode/utf16"

	"audiobookshelf-compat/internal/store"
)

func (h *Handler) upload(w http.ResponseWriter, r *http.Request, u store.User) {
	if !canUpload(u) {
		w.WriteHeader(http.StatusForbidden)
		return
	}
	if err := r.ParseMultipartForm(32 << 20); err != nil || r.MultipartForm == nil || len(r.MultipartForm.File) == 0 {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	defer r.MultipartForm.RemoveAll()
	files := make([]*multipart.FileHeader, 0)
	for _, list := range r.MultipartForm.File {
		files = append(files, list...)
	}
	title, libraryID, folderID := r.FormValue("title"), r.FormValue("library"), r.FormValue("folder")
	if title == "" || libraryID == "" || folderID == "" {
		http.Error(w, "Invalid request body", http.StatusBadRequest)
		return
	}
	var mediaType string
	if err := h.db.QueryRowContext(r.Context(), `SELECT mediaType FROM libraries WHERE id=?`, libraryID).Scan(&mediaType); err != nil {
		if err == sql.ErrNoRows {
			http.Error(w, "Library not found", http.StatusNotFound)
		} else {
			http.Error(w, "Internal server error", http.StatusInternalServerError)
		}
		return
	}
	if !canAccessLibrary(u, libraryID) {
		w.WriteHeader(http.StatusForbidden)
		return
	}
	var folderPath string
	if err := h.db.QueryRowContext(r.Context(), `SELECT path FROM libraryFolders WHERE id=? AND libraryId=?`, folderID, libraryID).Scan(&folderPath); err != nil {
		if err == sql.ErrNoRows {
			http.Error(w, "Folder not found", http.StatusNotFound)
		} else {
			http.Error(w, "Internal server error", http.StatusInternalServerError)
		}
		return
	}
	parts := []string{}
	if mediaType == "podcast" {
		parts = append(parts, title)
	} else {
		for _, value := range []string{r.FormValue("author"), r.FormValue("series"), title} {
			if value != "" {
				parts = append(parts, value)
			}
		}
	}
	for i := range parts {
		parts[i] = sanitizeUploadName(parts[i])
	}
	dir := filepath.Join(append([]string{folderPath}, parts...)...)
	if err := os.MkdirAll(dir, 0755); err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	for _, fh := range files {
		file, err := fh.Open()
		if err != nil {
			continue
		}
		name := sanitizeUploadName(fh.Filename)
		if name == "" {
			file.Close()
			continue
		}
		out, err := os.OpenFile(filepath.Join(dir, name), os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0644)
		if err == nil {
			_, _ = io.Copy(out, file)
			_ = out.Close()
		}
		_ = file.Close()
	}
	w.WriteHeader(http.StatusOK)
}

var (
	illegalUploadChars = regexp.MustCompile(`[/?<>\\:*|"\x00-\x1f\x80-\x9f]`)
	spaceRun           = regexp.MustCompile(`\s+`)
	reservedUploadName = regexp.MustCompile(`(?i)^(con|prn|aux|nul|com[0-9]|lpt[0-9])(\..*)?$`)
)

func sanitizeUploadName(name string) string {
	name = strings.Replace(name, ":", " - ", 1)
	name = illegalUploadChars.ReplaceAllString(name, "")
	if strings.Trim(name, ".") == "" || reservedUploadName.MatchString(name) {
		return ""
	}
	name = strings.TrimRight(name, ". ")
	name = spaceRun.ReplaceAllString(name, " ")
	if len(utf16.Encode([]rune(name))) > 255 {
		ext := filepath.Ext(name)
		base := strings.TrimSuffix(name, ext)
		max := 255 - len(utf16.Encode([]rune(ext)))
		var trimmed []rune
		for _, r := range base {
			if len(utf16.Encode(append(trimmed, r))) > max {
				break
			}
			trimmed = append(trimmed, r)
		}
		name = strings.TrimSpace(string(trimmed)) + ext
	}
	return name
}
