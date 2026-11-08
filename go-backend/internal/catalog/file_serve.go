package catalog

import (
	"audiobookshelf-compat/internal/access"
	"audiobookshelf-compat/internal/filekind"
	"audiobookshelf-compat/internal/httputil"
	"audiobookshelf-compat/internal/libitem"
	"audiobookshelf-compat/internal/store"
	"context"
	"fmt"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

func (h *Handler) mediaFile(w http.ResponseWriter, r *http.Request) error {
	download := strings.HasSuffix(r.Pattern, "/download")
	user, itemID, ino := requestUser(r), r.PathValue("id"), r.PathValue("ino")
	scope, err := h.itemFor(r.Context(), user, itemID, false)
	if err != nil {
		return err
	}
	raw, err := h.libraryFiles(r.Context(), itemID)
	if err != nil {
		return err
	}
	var target string
	for _, file := range store.DecodeJSONSlice[libitem.File](raw) {
		if file.Ino() == ino {
			target = file.Path()
			break
		}
	}
	if target == "" {
		return httputil.Status(http.StatusNotFound, "")
	}
	if download {
		if !access.For(r.Context(), user).Download() {
			return httputil.Status(http.StatusForbidden, "")
		}
		if mimeType := filekind.KnownAudioMime(filepath.Ext(target)); mimeType != "" {
			w.Header().Set("Content-Type", mimeType)
		}
	}
	if !h.pathAllowed(r.Context(), target, scope.libraryID) {
		if download {
			if _, statErr := os.Stat(target); statErr != nil {
				if os.IsNotExist(statErr) {
					return httputil.Status(http.StatusNotFound, "File not found")
				}
				return httputil.Internal("Download failed", statErr)
			}
		}
		return httputil.Status(http.StatusNotFound, "")
	}
	if download {
		w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": filepath.Base(target)}))
	}
	return h.servePath(w, r, target)
}

func (h *Handler) ebookFile(w http.ResponseWriter, r *http.Request) error {
	user, itemID, fileID := requestUser(r), r.PathValue("id"), r.PathValue("fileId")
	scope, err := h.itemFor(r.Context(), user, itemID, false)
	if err != nil {
		return err
	}
	raw, err := h.libraryFiles(r.Context(), itemID)
	if err != nil {
		return err
	}
	target := ""
	if fileID != "" {
		for _, file := range store.DecodeJSONSlice[libitem.File](raw) {
			if file.Ino() == fileID && file.Kind() == filekind.Ebook {
				target = file.Path()
				break
			}
		}
		if target == "" {
			return httputil.Status(http.StatusBadRequest, "Invalid ebook file id")
		}
	} else if scope.mediaType == "book" {
		var ebookRaw []byte
		if err := h.db.QueryRowContext(r.Context(), `SELECT ebookFile FROM books WHERE id=?`, scope.mediaID).Scan(&ebookRaw); err != nil {
			return httputil.Status(http.StatusNotFound, "")
		}
		target = libitem.File(store.DecodeJSONObject(ebookRaw)).Path()
	}
	if target == "" {
		return httputil.Status(http.StatusNotFound, "")
	}
	if !h.pathAllowed(r.Context(), target, scope.libraryID) {
		return httputil.Status(http.StatusNotFound, "")
	}
	return h.servePath(w, r, target)
}

// libraryFiles is the stored libraryFiles JSON of the item.
func (h *Handler) libraryFiles(ctx context.Context, itemID string) ([]byte, error) {
	var raw []byte
	if err := h.db.QueryRowContext(ctx, `SELECT libraryFiles FROM libraryItems WHERE id=?`, itemID).Scan(&raw); err != nil {
		return nil, httputil.Internal("database read failed", err)
	}
	return raw, nil
}

func (h *Handler) cover(w http.ResponseWriter, r *http.Request) error {
	itemID := r.PathValue("id")
	scope, err := h.loadItem(r.Context(), itemID)
	if err != nil {
		return err
	}
	if r.URL.Query().Get("ts") != "" {
		// LibraryItemController.getCover sets the cache header before any
		// existence checks when a cache-busting ts is present.
		w.Header().Set("Cache-Control", "private, max-age=86400")
	}
	if r.URL.Query().Get("raw") != "" {
		var target string
		if scope.mediaType == "book" {
			err = h.db.QueryRowContext(r.Context(), `SELECT coverPath FROM books WHERE id=?`, scope.mediaID).Scan(&target)
		} else if scope.mediaType == "podcast" {
			err = h.db.QueryRowContext(r.Context(), `SELECT coverPath FROM podcasts WHERE id=?`, scope.mediaID).Scan(&target)
		}
		if err != nil || target == "" {
			return httputil.Status(http.StatusNotFound, "")
		}
		if !(h.pathAllowed(r.Context(), target, scope.libraryID) || h.cfg.MetadataPath != "" && withinAny(target, []string{h.cfg.MetadataPath})) {
			return httputil.Status(http.StatusNotFound, "")
		}
		return h.coverRaw(w, r, target)
	}
	return h.handleCoverCache(w, r, itemID)
}

func (h *Handler) servePath(w http.ResponseWriter, r *http.Request, target string) error {
	f, err := os.Open(target)
	if err != nil {
		if os.IsNotExist(err) {
			return httputil.Status(http.StatusNotFound, "File not found")
		}
		return httputil.Internal("File read failed", err)
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return httputil.Internal("File read failed", err)
	}
	if !info.Mode().IsRegular() {
		return httputil.Status(http.StatusNotFound, "")
	}
	ext := strings.ToLower(filepath.Ext(target))
	media := filekind.KnownAudioMime(ext)
	if ext == ".wav" {
		media = "audio/wav"
	}
	if media == "" {
		media = mime.TypeByExtension(ext)
	}
	if media != "" {
		w.Header().Set("Content-Type", media)
	}
	// Express's etag helper uses stat.mtime.getTime(), which rounds the
	// filesystem timestamp to the nearest millisecond before hex encoding.
	fileETag := fmt.Sprintf(`W/"%x-%x"`, info.Size(), info.ModTime().Round(time.Millisecond).UnixMilli())
	w.Header().Set("ETag", fileETag)
	http.ServeContent(&sendFileRangeErrorWriter{
		ResponseWriter: w,
		method:         r.Method,
		etag:           fileETag,
		lastModified:   info.ModTime().UTC().Format(http.TimeFormat),
	}, r, filepath.Base(target), info.ModTime(), f)
	return nil
}

const expressRangeNotSatisfiableBody = "<!DOCTYPE html>\n<html lang=\"en\">\n<head>\n<meta charset=\"utf-8\">\n<title>Error</title>\n</head>\n<body>\n<pre>Range Not Satisfiable</pre>\n</body>\n</html>\n"

// sendFileRangeErrorWriter preserves streaming for successful responses while
// translating net/http's plain-text 416 into Express sendFile's finalhandler
// response. It buffers only the status until the 416 body arrives.
type sendFileRangeErrorWriter struct {
	http.ResponseWriter
	method       string
	etag         string
	lastModified string
	status       int
}

func (w *sendFileRangeErrorWriter) WriteHeader(status int) {
	if status == http.StatusRequestedRangeNotSatisfiable {
		w.status = status
		return
	}
	w.ResponseWriter.WriteHeader(status)
}

func (w *sendFileRangeErrorWriter) Write(p []byte) (int, error) {
	if w.status != http.StatusRequestedRangeNotSatisfiable {
		return w.ResponseWriter.Write(p)
	}
	body := []byte(expressRangeNotSatisfiableBody)
	header := w.Header()
	header.Set("Content-Type", "text/html; charset=utf-8")
	header.Set("Content-Length", strconv.Itoa(len(body)))
	header.Set("Accept-Ranges", "bytes")
	header.Set("ETag", w.etag)
	header.Set("Last-Modified", w.lastModified)
	w.ResponseWriter.WriteHeader(w.status)
	if w.method != http.MethodHead {
		if _, err := w.ResponseWriter.Write(body); err != nil {
			return 0, err
		}
	}
	return len(p), nil
}
