package item

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"audiobookshelf-compat/internal/auth"
	"audiobookshelf-compat/internal/db"
	"audiobookshelf-compat/internal/db/dbtest"
	"audiobookshelf-compat/internal/events"
	"audiobookshelf-compat/internal/filterdata"
	"audiobookshelf-compat/internal/settings"
	"audiobookshelf-compat/internal/wire"
)

type sentEvent struct {
	audience, name string
	payload        any
}

// fakeBooks is the provider search behind quick match.
type fakeBooks struct {
	books    []wire.BookMatch
	podcasts []wire.PodcastMatch
	queries  []wire.BookSearch
}

func (f *fakeBooks) SearchBooks(_ context.Context, q wire.BookSearch) []wire.BookMatch {
	f.queries = append(f.queries, q)
	return f.books
}

func (f *fakeBooks) SearchPodcasts(context.Context, string) []wire.PodcastMatch { return f.podcasts }

// rig serves the item routes over the pinned schema (foreign keys on).
//
// Library l1 (books) holds i1 "The Book" (author Ada Author, series First
// Series #1) and i2 "Second" (author Ben Writer); library lp (podcasts) holds
// the podcast ip with one episode.
type rig struct {
	t         *testing.T
	d         *db.DB
	h         *Handler
	mux       *http.ServeMux
	filters   *filterdata.Cache
	finder    *fakeBooks
	cacheDir  string
	metaDir   string
	mu        sync.Mutex
	events    []sentEvent
	touched   []string
	scheduled []string
}

const ts = "2024-01-01 00:00:00.000 +00:00"

func newRig(t *testing.T) *rig {
	t.Helper()
	r := &rig{t: t, d: dbtest.New(t), filters: filterdata.NewCache(), finder: &fakeBooks{}, cacheDir: filepath.Join(t.TempDir(), "cache"), metaDir: filepath.Join(t.TempDir(), "items")}
	for _, q := range []string{
		`INSERT INTO users(id,username,type,isActive,permissions,createdAt,updatedAt) VALUES('u1','root','root',1,'{}',?,?),('u2','reader','user',1,'{}',?,?)`,
		`INSERT INTO libraries(id,name,displayOrder,icon,mediaType,provider,settings,extraData,createdAt,updatedAt) VALUES('l1','Books',1,'books','book','google','{}','{}',?,?),('lp','Podcasts',2,'podcast','podcast','itunes','{}','{}',?,?)`,
		`INSERT INTO libraryFolders(id,path,libraryId,createdAt,updatedAt) VALUES('f1','/nonexistent/books','l1',?,?)`,
		`INSERT INTO authors(id,name,lastFirst,libraryId,createdAt,updatedAt) VALUES('a1','Ada Author','Author, Ada','l1',?,?),('a2','Ben Writer','Writer, Ben','l1',?,?)`,
		`INSERT INTO series(id,name,nameIgnorePrefix,libraryId,createdAt,updatedAt) VALUES('s1','First Series','First Series','l1',?,?)`,
		`INSERT INTO books(id,title,titleIgnorePrefix,publishedYear,description,language,duration,narrators,audioFiles,chapters,tags,genres,createdAt,updatedAt) VALUES
			('b1','The Book','Book, The','2024','<p>A story</p>','en',12.5,'["Reader"]','[{"index":1,"ino":"audio-1","metadata":{"filename":"01.mp3","ext":".mp3","path":"/nonexistent/books/The Book/01.mp3","relPath":"01.mp3","size":100},"duration":12.5,"mimeType":"audio/mpeg"}]','[]','["tag"]','["fiction"]',?,?),
			('b2','Second','Second',NULL,NULL,'en',5,'[]','[]','[]','[]','[]',?,?)`,
		`INSERT INTO libraryItems(id,ino,path,relPath,mediaId,mediaType,isFile,isMissing,isInvalid,size,libraryFiles,extraData,title,titleIgnorePrefix,libraryId,libraryFolderId,createdAt,updatedAt) VALUES
			('i1','1','/nonexistent/books/The Book','The Book','b1','book',0,0,0,100,'[]','{}','The Book','Book, The','l1','f1',?,?),
			('i2','2','/nonexistent/books/Second','Second','b2','book',0,0,0,1,'[]','{}','Second','Second','l1','f1',?,?)`,
		`INSERT INTO bookAuthors(id,bookId,authorId,createdAt) VALUES('ba1','b1','a1',?),('ba2','b2','a2',?)`,
		`INSERT INTO bookSeries(id,sequence,bookId,seriesId,createdAt) VALUES('bs1','1','b1','s1',?)`,
		`INSERT INTO podcasts(id,title,titleIgnorePrefix,author,feedURL,explicit,autoDownloadEpisodes,autoDownloadSchedule,maxEpisodesToKeep,maxNewEpisodesToDownload,tags,genres,numEpisodes,createdAt,updatedAt) VALUES('p1','Science Now','Science Now','Host','https://example.test/feed',0,0,'0 * * * *',0,3,'["science"]','[]',1,?,?)`,
		`INSERT INTO podcastEpisodes(id,"index",title,audioFile,chapters,extraData,podcastId,createdAt,updatedAt) VALUES('e1',1,'Launch','{"index":1,"ino":"ep-audio","metadata":{"filename":"episode.mp3","ext":".mp3","path":"/nonexistent/podcasts/episode.mp3","relPath":"episode.mp3","size":123},"duration":30}','[]','{"guid":"guid-e1"}','p1',?,?)`,
		`INSERT INTO libraryItems(id,ino,path,relPath,mediaId,mediaType,isFile,isMissing,isInvalid,size,libraryFiles,extraData,title,titleIgnorePrefix,libraryId,createdAt,updatedAt) VALUES('ip','3','/nonexistent/podcasts/Science','Science','p1','podcast',0,0,0,123,'[]','{}','Science Now','Science Now','lp',?,?)`,
	} {
		r.exec(q, args(strings.Count(q, "?"))...)
	}
	store, err := settings.NewStore(context.Background(), r.d, func(string) string { return "" }, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	r.h = New(r.d, store, events.Emitter(func(audience, name string, payload any) {
		r.mu.Lock()
		defer r.mu.Unlock()
		r.events = append(r.events, sentEvent{audience, name, payload})
	}), Deps{
		Filters:         r.filters,
		Books:           r.finder,
		WriteMetadata:   func(_ context.Context, id string) error { r.touched = append(r.touched, id); return nil },
		ScheduleChanged: func(_ context.Context, id string) { r.scheduled = append(r.scheduled, id) },
		CoverCacheDir:   r.cacheDir,
		ItemMetadataDir: r.metaDir,
	})
	r.mux = http.NewServeMux()
	r.h.Routes(r.mux)
	return r
}

func args(n int) []any {
	out := make([]any, n)
	for i := range out {
		out[i] = ts
	}
	return out
}

func (r *rig) exec(q string, a ...any) {
	r.t.Helper()
	if _, err := r.d.Exec(context.Background(), q, a...); err != nil {
		r.t.Fatalf("%s: %v", q, err)
	}
}

// values returns the first column of a query, comma-joined in query order.
func (r *rig) values(query string, a ...any) string {
	r.t.Helper()
	var out []string
	if err := r.d.Select(context.Background(), &out, query, a...); err != nil {
		r.t.Fatalf("%s: %v", query, err)
	}
	return strings.Join(out, ",")
}

func (r *rig) do(user *auth.Principal, method, target, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, target, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	r.mux.ServeHTTP(rec, req.WithContext(auth.WithUser(req.Context(), user)))
	return rec
}

// takeEvents returns and clears the events sent so far.
func (r *rig) takeEvents() []sentEvent {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := r.events
	r.events = nil
	return out
}

func (r *rig) takeEventNames() string {
	var names []string
	for _, e := range r.takeEvents() {
		names = append(names, e.name)
	}
	return strings.Join(names, ",")
}

func user(id, typ, permissions string) *auth.Principal {
	var p db.Permissions
	if err := json.Unmarshal([]byte(permissions), &p); err != nil {
		panic(err)
	}
	return auth.NewPrincipal(db.User{ID: id, Username: &id, Type: &typ, IsActive: true, Permissions: db.JSON[db.Permissions]{V: p}})
}

var (
	rootUser   = user("u1", "root", `{"accessAllLibraries":true,"accessAllTags":true,"accessExplicitContent":true,"update":true,"delete":true}`)
	adminUser  = user("u1", "admin", `{"accessAllLibraries":true,"accessAllTags":true,"accessExplicitContent":true,"update":true}`)
	readerUser = user("u2", "user", `{"accessAllLibraries":true,"accessAllTags":true,"accessExplicitContent":true}`)
	podUser    = user("u3", "user", `{"librariesAccessible":["lp"],"accessAllTags":true,"accessExplicitContent":true,"update":true,"delete":true}`)
)

func decode[T any](t *testing.T, rec *httptest.ResponseRecorder) T {
	t.Helper()
	var out T
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("body %q: %v", rec.Body.String(), err)
	}
	return out
}

func mustWrite(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func toJSON(v any) any {
	raw, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	var out any
	if err := json.Unmarshal(raw, &out); err != nil {
		panic(err)
	}
	return out
}
