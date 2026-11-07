// Package sharing implements the pinned RSS feed and public media-share routes.
package sharing

import (
	"archive/zip"
	"database/sql"
	"encoding/json"
	"io"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"audiobookshelf-compat/internal/config"
	"audiobookshelf-compat/internal/db/sqlc"
	"audiobookshelf-compat/internal/httputil"
	"audiobookshelf-compat/internal/store"
)

type Handler struct {
	db       *sql.DB
	cfg      config.Config
	emit     func(string, string, any)
	mu       sync.Mutex
	sessions map[string]*shareSession
}

// queries returns the generated query set on the handler's pool; sqlc.New only
// stores the handle.
func (h *Handler) queries() *sqlc.Queries { return sqlc.New(h.db) }

type shareSession struct {
	ID          string
	ShareID     string
	Slug        string
	ItemID      string
	LibraryID   string
	Title       string
	Author      string
	CoverPath   string
	Duration    float64
	Current     float64
	Start       float64
	Updated     int64
	Tracks      []shareTrack
	Metadata    any
	Chapters    []map[string]any
	CoverAspect *float64
	Device      map[string]any
	BookID      string
}

func New(db *sql.DB, cfg config.Config, emit func(string, string, any)) *Handler {
	h := &Handler{db: db, cfg: cfg, emit: emit, sessions: map[string]*shareSession{}}
	return h
}

// Routes registers the feed and share routes. The /feed and /public/share
// routes are public: the runtime mounts them without a user.
func (h *Handler) Routes(mux *http.ServeMux) {
	authed := func(f func(http.ResponseWriter, *http.Request, store.User) error) http.Handler {
		return httputil.Handler(func(w http.ResponseWriter, r *http.Request) error {
			u, _ := store.UserFromContext(r.Context())
			return f(w, r, u)
		})
	}
	open := func(entityType string) http.Handler {
		return authed(func(w http.ResponseWriter, r *http.Request, u store.User) error {
			return h.openFeed(w, r, u, entityType)
		})
	}
	mux.Handle("GET /api/feeds", authed(h.listFeeds))
	mux.Handle("POST /api/feeds/item/{id}/open", open("libraryItem"))
	mux.Handle("POST /api/feeds/collection/{id}/open", open("collection"))
	mux.Handle("POST /api/feeds/series/{id}/open", open("series"))
	mux.Handle("POST /api/feeds/{id}/close", authed(h.closeFeed))
	mux.Handle("POST /api/share/mediaitem", authed(h.createShare))
	mux.Handle("DELETE /api/share/mediaitem/{id}", authed(h.deleteShare))
	h.publicRoutes(mux)
}

// publicRoutes registers the bearer-free routes.
func (h *Handler) publicRoutes(mux *http.ServeMux) {
	mux.Handle("GET /feed/{slug}", httputil.Handler(h.publicFeed))
	mux.Handle("GET /feed/{slug}/{tail...}", httputil.Handler(h.publicFeed))
	mux.Handle("GET /public/share/{slug}", httputil.Handler(h.publicShare))
	mux.Handle("GET /public/share/{slug}/cover", httputil.Handler(h.shareCover))
	mux.Handle("GET /public/share/{slug}/track/{index}", httputil.Handler(h.shareTrack))
	mux.Handle("GET /public/share/{slug}/download", httputil.Handler(h.shareDownload))
	mux.Handle("PATCH /public/share/{slug}/progress", httputil.Handler(h.shareProgress))
}

func write(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(v)
}

// hostPrefix is req.originalHostPrefix (server/Server.js:324): the request
// origin, plus the router base path when the request URL carried it.
func hostPrefix(r *http.Request, base string) string {
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	for _, proto := range strings.Split(strings.ToLower(r.Header.Get("X-Forwarded-Proto")), ",") {
		if strings.TrimSpace(proto) == "https" {
			scheme = "https"
		}
	}
	prefix := ""
	if strings.HasPrefix(r.RequestURI, base) {
		prefix = base
	}
	return scheme + "://" + r.Host + prefix
}
func serveMedia(w http.ResponseWriter, r *http.Request, p string) {
	ext := strings.ToLower(filepath.Ext(p))
	mt := map[string]string{".m4b": "audio/mp4", ".m4a": "audio/mp4", ".mp3": "audio/mpeg", ".ogg": "audio/ogg", ".opus": "audio/ogg", ".flac": "audio/flac", ".wav": "audio/wav", ".aac": "audio/aac", ".jpg": "image/jpeg", ".jpeg": "image/jpeg", ".png": "image/png", ".webp": "image/webp", ".gif": "image/gif"}[ext]
	if mt == "" {
		mt = mime.TypeByExtension(ext)
	}
	if mt != "" {
		w.Header().Set("Content-Type", mt)
	}
	http.ServeFile(w, r, p)
}
func safeFilename(s string) string {
	s = filepath.Base(s)
	s = strings.ReplaceAll(s, "\"", "_")
	if s == "." || s == "/" || s == "" {
		return "download"
	}
	return s
}
func zipDirectory(w io.Writer, root string) error {
	z := zip.NewWriter(w)
	err := filepath.Walk(root, func(path string, info os.FileInfo, e error) error {
		if e != nil {
			return e
		}
		if info.IsDir() {
			return nil
		}
		rel, e := filepath.Rel(root, path)
		if e != nil {
			return e
		}
		header, e := zip.FileInfoHeader(info)
		if e != nil {
			return e
		}
		header.Name = filepath.ToSlash(rel)
		header.Method = zip.Deflate
		out, e := z.CreateHeader(header)
		if e != nil {
			return e
		}
		f, e := os.Open(path)
		if e != nil {
			return e
		}
		_, e = io.Copy(out, f)
		ce := f.Close()
		if e != nil {
			return e
		}
		return ce
	})
	closeErr := z.Close()
	if err != nil {
		return err
	}
	return closeErr
}
