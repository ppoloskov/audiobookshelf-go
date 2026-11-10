// Library download route mirroring LibraryController.downloadMultiple.
package catalog

import (
	"archive/zip"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"audiobookshelf-compat/internal/access"
	"audiobookshelf-compat/internal/httputil"
	"audiobookshelf-compat/internal/library"
	"audiobookshelf-compat/internal/logging"
)

// libraryDownload is LibraryController.downloadMultiple
// (LibraryController.js:1422-1465): the items named by ?ids (comma separated)
// of this library as one zip, each item under its folder name (a single-file
// item as that file), stored without compression.
func (h *Handler) libraryDownload(w http.ResponseWriter, r *http.Request, library library.Scope) error {
	user := library.User
	if !access.For(r.Context(), user).Download() {
		logging.Warnf(`User "%s" attempted to download without permission`, user.Username)
		return httputil.Status(http.StatusForbidden, "")
	}
	idsParam, present := r.URL.Query()["ids"]
	if !present || len(idsParam) != 1 || idsParam[0] == "" {
		return httputil.Status(http.StatusBadRequest, "Invalid request. ids must be a string")
	}
	itemIDs := strings.Split(idsParam[0], ",")
	ctx := r.Context()
	found, err := h.itemsExpandedWhere(ctx, itemIDs)
	if err != nil {
		return httputil.Internal("database read failed", err)
	}
	var items []itemScope
	for _, item := range found {
		if item.libraryID == library.ID {
			items = append(items, item)
		}
	}
	allowed, err := h.userCanAccessItems(ctx, user, items)
	if err != nil {
		return httputil.Internal("database read failed", err)
	}
	if !allowed {
		return httputil.Status(http.StatusForbidden, "")
	}
	if len(items) < len(itemIDs) {
		logging.Warnf(`[LibraryController] User "%s" requested %d items but only %d are in library "%s"`, user.Username, len(itemIDs), len(items), library.ID)
	}
	logging.Infof(`[LibraryController] User "%s" requested download for items "%s"`, user.Username, strings.Join(itemIDs, ","))
	filename := fmt.Sprintf("LibraryItems-%d.zip", time.Now().UnixMilli())
	if len(items) == 0 {
		logging.Warnf(`[LibraryController] No library items found for ids "%s"`, strings.Join(itemIDs, ","))
		return httputil.Status(http.StatusNotFound, "Library items not found")
	}
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", `attachment; filename="`+filename+`"`)
	// Streamed without a Content-Length, like archiver piped into the response.
	w.WriteHeader(http.StatusOK)
	if flusher, ok := w.(http.Flusher); ok {
		flusher.Flush()
	}
	zw := zip.NewWriter(w)
	for _, item := range items {
		var err error
		if item.isFile {
			err = addFileToZip(zw, item.path, filepath.Base(item.path))
		} else {
			err = addTreeToZip(zw, item.path, filepath.Base(item.path))
		}
		if err != nil {
			// Headers are sent; upstream logs the archiver error and the
			// response ends (zipHelpers.js:73-79).
			logging.Errorf(`[DownloadManager] Archiver error: %v`, err)
			break
		}
	}
	_ = zw.Close()
	logging.Infof(`[LibraryController] Downloaded %d items "%s"`, len(items), filename)
	return nil
}

func zipHeader(name string, info fs.FileInfo) *zip.FileHeader {
	header := &zip.FileHeader{Name: name, Method: zip.Store, Modified: info.ModTime()}
	header.SetMode(info.Mode())
	return header
}

func addFileToZip(zw *zip.Writer, path, name string) error {
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	target, err := zw.CreateHeader(zipHeader(name, info))
	if err != nil {
		return err
	}
	source, err := os.Open(path)
	if err != nil {
		return err
	}
	defer source.Close()
	_, err = io.Copy(target, source)
	return err
}

// addTreeToZip is archiver's directory(path, name): every file and every
// directory below path (as "name/rel/") under the name prefix.
func addTreeToZip(zw *zip.Writer, root, name string) error {
	return filepath.WalkDir(root, func(p string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		entryName := name + "/" + filepath.ToSlash(rel)
		if entry.IsDir() {
			_, err := zw.CreateHeader(zipHeader(entryName+"/", info))
			return err
		}
		return addFileToZip(zw, p, entryName)
	})
}
