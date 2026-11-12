package admin

import (
	"database/sql"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"audiobookshelf-compat/internal/auth"
	"audiobookshelf-compat/internal/db"
	"audiobookshelf-compat/internal/httpx"
)

type directory struct {
	Path    string `json:"path"`
	Dirname string `json:"dirname"`
	Level   int    `json:"level"`
}

// filesystem lists the folders below ?path= (default "/") for the folder picker.
func (h *Handler) filesystem(w http.ResponseWriter, r *http.Request) error {
	root := r.URL.Query().Get("path")
	if root == "" {
		root = "/"
	} else if _, err := os.Stat(root); err != nil || !filepath.IsAbs(root) {
		return httpx.BadRequest(`Invalid "path" query string`)
	}
	level := max(httpx.QueryInt(r, "level", 0), 0)
	dirs := []directory{}
	entries, _ := os.ReadDir(root) // an unreadable folder lists as empty
	for _, entry := range entries {
		if entry.IsDir() {
			dirs = append(dirs, directory{filepath.ToSlash(filepath.Join(root, entry.Name())), entry.Name(), level})
		}
	}
	return httpx.JSON(w, r, struct {
		Posix       bool        `json:"posix"`
		Directories []directory `json:"directories"`
	}{os.PathSeparator == '/', dirs})
}

// pathExists is FileSystemController.checkPathExists.
func (h *Handler) pathExists(w http.ResponseWriter, r *http.Request) error {
	user := auth.User(r.Context())
	if !user.CanUpload() {
		return httpx.Forbidden()
	}
	body, err := httpx.Decode[struct {
		Directory  string `json:"directory"`
		FolderPath string `json:"folderPath"`
	}](w, r)
	if err != nil || body.Directory == "" || body.FolderPath == "" {
		// Upstream answers res.status(400).json({error}), FileSystemController.js:94-97.
		return httpx.JSONStatus(w, r, http.StatusBadRequest, map[string]string{"error": "Invalid request body"})
	}
	var folder db.LibraryFolder
	if err := h.db.Get(r.Context(), &folder, "SELECT * FROM libraryFolders WHERE path = ? LIMIT 1", body.FolderPath); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return httpx.NotFound()
		}
		return httpx.Internal("", err)
	}
	if !user.IsAdmin() && !user.CanAccessLibrary(db.Val(folder.LibraryID)) {
		return httpx.Forbidden()
	}
	base, _ := filepath.Abs(body.FolderPath) // Abs only fails without a working directory
	target, _ := filepath.Abs(filepath.Join(base, body.Directory))
	if rel, err := filepath.Rel(base, target); err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
		return httpx.BadRequest("")
	}
	type reply struct {
		Exists           bool    `json:"exists"`
		LibraryItemTitle *string `json:"libraryItemTitle,omitempty"`
	}
	if _, err := os.Stat(target); err == nil {
		return httpx.JSON(w, r, reply{Exists: true})
	}
	// A new folder two levels below an existing item would nest inside it.
	if cleaned := filepath.ToSlash(filepath.Clean(body.Directory)); strings.Contains(cleaned, "/") {
		subdir := filepath.Dir(cleaned)
		paths := []string{filepath.ToSlash(filepath.Join(body.FolderPath, subdir))}
		if strings.Contains(subdir, "/") {
			paths = append(paths, filepath.ToSlash(filepath.Join(body.FolderPath, filepath.Dir(subdir))))
		}
		var item db.LibraryItem
		err := h.db.Get(r.Context(), &item, "SELECT * FROM libraryItems WHERE path IN (?) LIMIT 1", paths)
		if err == nil {
			title := db.Val(item.Title)
			return httpx.JSON(w, r, reply{Exists: true, LibraryItemTitle: &title})
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return httpx.Internal("", err)
		}
	}
	return httpx.JSON(w, r, reply{})
}
