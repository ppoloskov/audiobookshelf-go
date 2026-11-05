// Package sharing implements the pinned RSS feed and public media-share routes.
package sharing

import (
	"archive/zip"
	"context"
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
	Metadata    map[string]any
	Chapters    []any
	CoverAspect *float64
	Device      map[string]any
	BookID      string
}

func New(db *sql.DB, cfg config.Config, emit func(string, string, any)) *Handler {
	return &Handler{db: db, cfg: cfg, emit: emit, sessions: map[string]*shareSession{}}
}
func (h *Handler) Handle(w http.ResponseWriter, r *http.Request, u store.User) bool {
	p := r.URL.Path
	switch {
	case p == "/api/feeds" && r.Method == http.MethodGet:
		h.listFeeds(w, r, u)
	case strings.HasPrefix(p, "/api/feeds/item/") && strings.HasSuffix(p, "/open") && r.Method == http.MethodPost:
		h.openFeed(w, r, u, "libraryItem")
	case strings.HasPrefix(p, "/api/feeds/collection/") && strings.HasSuffix(p, "/open") && r.Method == http.MethodPost:
		h.openFeed(w, r, u, "collection")
	case strings.HasPrefix(p, "/api/feeds/series/") && strings.HasSuffix(p, "/open") && r.Method == http.MethodPost:
		h.openFeed(w, r, u, "series")
	case strings.HasPrefix(p, "/api/feeds/") && strings.HasSuffix(p, "/close") && r.Method == http.MethodPost:
		h.closeFeed(w, r, u)
	case p == "/api/share/mediaitem" && r.Method == http.MethodPost:
		h.createShare(w, r, u)
	case strings.HasPrefix(p, "/api/share/mediaitem/") && r.Method == http.MethodDelete:
		h.deleteShare(w, r, u)
	case strings.HasPrefix(p, "/feed/") && r.Method == http.MethodGet:
		h.publicFeed(w, r)
	case strings.HasPrefix(p, "/public/share/") && r.Method == http.MethodGet:
		if len(strings.Split(strings.Trim(p, "/"), "/")) == 3 {
			h.publicShare(w, r)
		} else {
			h.publicShareFile(w, r)
		}
	case strings.HasPrefix(p, "/public/share/") && r.Method == http.MethodPatch:
		h.shareProgress(w, r)
	default:
		return false
	}
	return true
}
func admin(u store.User) bool { return u.Type == "root" || u.Type == "admin" }

// oldBookMetadata is Book.oldMetadataToJSON (server/models/Book.js:550):
// authors and series in bookAuthors/bookSeries createdAt order, NULL columns
// as null.
func (h *Handler) oldBookMetadata(ctx context.Context, id string) map[string]any {
	var title, subtitle, year, date, publisher, desc, isbn, asin, language sql.NullString
	var exp, abr int
	var narr, genres []byte
	_ = h.db.QueryRowContext(ctx, `SELECT title,subtitle,publishedYear,publishedDate,publisher,description,isbn,asin,language,explicit,abridged,narrators,genres FROM books WHERE id=?`, id).Scan(&title, &subtitle, &year, &date, &publisher, &desc, &isbn, &asin, &language, &exp, &abr, &narr, &genres)
	narrators, genreList := []string{}, []string{}
	_ = json.Unmarshal(narr, &narrators)
	_ = json.Unmarshal(genres, &genreList)
	authors := []map[string]any{}
	rows, e := h.db.QueryContext(ctx, `SELECT a.id,a.name FROM authors a JOIN bookAuthors ba ON ba.authorId=a.id WHERE ba.bookId=? ORDER BY ba.createdAt ASC, ba.rowid ASC`, id)
	if e == nil {
		for rows.Next() {
			var i, n string
			if rows.Scan(&i, &n) == nil {
				authors = append(authors, map[string]any{"id": i, "name": n})
			}
		}
		rows.Close()
	}
	series := []map[string]any{}
	rows, e = h.db.QueryContext(ctx, `SELECT s.id,s.name,bs.sequence FROM series s JOIN bookSeries bs ON bs.seriesId=s.id WHERE bs.bookId=? ORDER BY bs.createdAt ASC, bs.rowid ASC`, id)
	if e == nil {
		for rows.Next() {
			var i, n string
			var seq sql.NullString
			if rows.Scan(&i, &n, &seq) == nil {
				series = append(series, map[string]any{"id": i, "name": n, "sequence": store.NullString(seq)})
			}
		}
		rows.Close()
	}
	return map[string]any{"title": title.String, "subtitle": store.NullString(subtitle), "authors": authors, "narrators": narrators, "series": series, "genres": genreList, "publishedYear": store.NullString(year), "publishedDate": store.NullString(date), "publisher": store.NullString(publisher), "description": store.NullString(desc), "isbn": store.NullString(isbn), "asin": store.NullString(asin), "language": store.NullString(language), "explicit": exp != 0, "abridged": abr != 0}
}
func write(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(v)
}
func routeID(path, suffix string) string {
	p := strings.Split(strings.Trim(path, "/"), "/")
	if suffix != "" {
		for i, x := range p {
			if x == suffix && i > 0 {
				return p[i-1]
			}
		}
	}
	if len(p) > 0 {
		return p[len(p)-1]
	}
	return ""
}
func libraryAccess(u store.User, id string) bool {
	var p map[string]any
	if json.Unmarshal(u.Permissions, &p) != nil {
		return false
	}
	if p["accessAllLibraries"] == true {
		return true
	}
	a, _ := p["librariesAccessible"].([]any)
	for _, v := range a {
		if v == id {
			return true
		}
	}
	return false
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
