package server

import (
	"io"
	"io/fs"
	"net/http"
	"os"
	"path"
	"strings"
)

// clientHandler serves the web client from root. The client is a Nuxt SPA
// (ssr false, target static) whose router renders its own 404 page, so any
// GET that is not a file and does not look like an asset (no extension) gets
// index.html. A missing asset is a plain 404. Directories are never listed.
func clientHandler(root string) http.Handler {
	if root == "" {
		return http.NotFoundHandler()
	}
	files := os.DirFS(root)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			http.NotFound(w, r)
			return
		}
		name := strings.TrimPrefix(path.Clean(r.URL.Path), "/")
		if name == "" {
			name = "index.html"
		}
		switch {
		case serveFile(w, r, files, name):
		case path.Ext(name) == "":
			if !serveFile(w, r, files, "index.html") {
				http.NotFound(w, r)
			}
		default:
			http.NotFound(w, r)
		}
	})
}

// serveFile serves name when it is a regular file and reports whether it did.
func serveFile(w http.ResponseWriter, r *http.Request, files fs.FS, name string) bool {
	file, err := files.Open(name)
	if err != nil {
		return false
	}
	defer file.Close()
	info, err := file.Stat()
	content, seekable := file.(io.ReadSeeker)
	if err != nil || !info.Mode().IsRegular() || !seekable {
		return false
	}
	http.ServeContent(w, r, info.Name(), info.ModTime(), content)
	return true
}
