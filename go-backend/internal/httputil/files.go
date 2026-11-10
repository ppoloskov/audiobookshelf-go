package httputil

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
)

// SupportsWebP reports whether Accept prefers/accepts WebP.
func SupportsWebP(accept string) bool {
	return accept == "*/*" || strings.Contains(accept, "image/webp")
}

// ClampDimension parses a positive image dimension, capped at max.
func ClampDimension(raw string, max int) *int {
	parsed, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil || parsed <= 0 {
		return nil
	}
	if parsed > max {
		parsed = max
	}
	return &parsed
}

// ExpressFileETag is the weak ETag send/index.js uses for a file.
func ExpressFileETag(size int64, modTime int64) string {
	return fmt.Sprintf(`W/"%x-%x"`, size, modTime)
}

// RawFile is the raw sendFile branch: public,max-age=0, weak ETag,
// Last-Modified, and http.ServeFile range semantics.
func RawFile(w http.ResponseWriter, r *http.Request, path string) error {
	info, err := os.Stat(path)
	if err != nil {
		return Status(http.StatusNotFound, "")
	}
	if w.Header().Get("Cache-Control") == "" {
		w.Header().Set("Cache-Control", "public, max-age=0")
	}
	if info.Mode().IsRegular() {
		w.Header().Set("ETag", ExpressFileETag(info.Size(), info.ModTime().UnixMilli()))
	}
	http.ServeFile(w, r, path)
	return nil
}

// StreamCache streams a cache file with node pipe semantics: no
// Content-Length, chunked framing, and no body for HEAD.
func StreamCache(w http.ResponseWriter, r *http.Request, path string) error {
	file, err := os.Open(path)
	if err != nil {
		return Internal("", err)
	}
	defer file.Close()
	w.WriteHeader(http.StatusOK)
	if flusher, ok := w.(http.Flusher); ok {
		flusher.Flush()
	}
	if r.Method != http.MethodHead {
		_, _ = io.Copy(w, file)
	}
	return nil
}
