package library

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	schemaassets "audiobookshelf-compat/contracts/parity"
	"audiobookshelf-compat/internal/events"
	"audiobookshelf-compat/internal/filterdata"
	"audiobookshelf-compat/internal/store"
)

type recordedEvent struct {
	event   string
	payload any
}

type seriesRig struct {
	t      *testing.T
	db     *sql.DB
	mux    *http.ServeMux
	mu     sync.Mutex
	events []recordedEvent
}

func newSeriesRig(t *testing.T) *seriesRig {
	t.Helper()
	db, err := sql.Open("sqlite", "file:series-"+strings.ReplaceAll(t.Name(), "/", "-")+"?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.Exec(schemaassets.SchemaSQL); err != nil {
		t.Fatal(err)
	}
	const now = "2024-01-01 00:00:00.000 +00:00"
	for _, q := range []string{
		`INSERT INTO libraries(id,name,displayOrder,icon,mediaType,provider,settings,extraData,createdAt,updatedAt) VALUES('l1','Shelf',0,'database','book','google','{}','{}','` + now + `','` + now + `')`,
		`INSERT INTO libraryFolders(id,path,libraryId,createdAt,updatedAt) VALUES('f1','/media/books','l1','` + now + `','` + now + `')`,
		`INSERT INTO series(id,name,nameIgnorePrefix,description,libraryId,createdAt,updatedAt) VALUES('s1','First Series','First Series',NULL,'l1','` + now + `','` + now + `')`,
		`INSERT INTO authors(id,name,lastFirst,libraryId,createdAt,updatedAt) VALUES('a1','Ada Author','Author, Ada','l1','` + now + `','` + now + `')`,
		`INSERT INTO books(id,title,titleIgnorePrefix,description,explicit,abridged,duration,narrators,audioFiles,ebookFile,chapters,tags,genres,createdAt,updatedAt) VALUES('b1','The Book','Book, The','<p>A story</p>',0,0,12.5,'["Reader"]','[{"x":1}]','null','[{"title":"Chapter 1"}]','["tag"]','["fiction"]','` + now + `','` + now + `'),('b2','Second','Second',NULL,0,0,8,'[]','[]','null','[]','[]','[]','` + now + `','` + now + `')`,
		`INSERT INTO libraryItems(id,ino,path,relPath,mediaId,mediaType,isFile,isMissing,isInvalid,libraryFiles,extraData,size,title,titleIgnorePrefix,authorNamesFirstLast,authorNamesLastFirst,createdAt,updatedAt,libraryId,libraryFolderId) VALUES('i1','ino1','/media/books/The Book','b1','b1','book',0,0,0,'[]','{}',100,'The Book','Book, The','','','` + now + `','` + now + `','l1','f1'),('i2','ino2','/media/books/Second','b2','b2','book',0,0,0,'[]','{}',80,'Second','Second','','','` + now + `','` + now + `','l1','f1')`,
		`INSERT INTO bookAuthors(id,bookId,authorId,createdAt) VALUES('ba1','b1','a1','` + now + `')`,
		`INSERT INTO bookSeries(id,sequence,bookId,seriesId,createdAt) VALUES('bs1','1','b1','s1','` + now + `'),('bs2','2','b2','s1','` + now + `')`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatalf("%v\n%s", err, q)
		}
	}
	rig := &seriesRig{t: t, db: db}
	emit := events.Emitter(func(_ string, event string, payload any) {
		rig.mu.Lock()
		defer rig.mu.Unlock()
		rig.events = append(rig.events, recordedEvent{event, payload})
	})
	rig.mux = http.NewServeMux()
	New(db, emit, Deps{Filters: filterdata.NewCache()}).Routes(rig.mux)
	return rig
}

func (r *seriesRig) do(method, target, body string, user store.User) *httptest.ResponseRecorder {
	r.t.Helper()
	req := httptest.NewRequest(method, target, strings.NewReader(body))
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	req = req.WithContext(store.ContextWithUser(req.Context(), user))
	w := httptest.NewRecorder()
	r.mux.ServeHTTP(w, req)
	return w
}

func (r *seriesRig) takeEvents() []recordedEvent {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := r.events
	r.events = nil
	return out
}

func decodeSeriesBody(t *testing.T, w *httptest.ResponseRecorder, into any) {
	t.Helper()
	if err := json.Unmarshal(w.Body.Bytes(), into); err != nil {
		t.Fatalf("body %q: %v", w.Body.String(), err)
	}
}

func seriesRootUser() store.User {
	return store.User{ID: "u1", Username: "root", Type: "root", IsActive: true, Permissions: json.RawMessage(`{"accessAllLibraries":true,"accessAllTags":true,"accessExplicitContent":true,"update":true,"delete":true,"upload":true}`)}
}

func seriesReadOnlyUser() store.User {
	return store.User{ID: "u2", Username: "reader", Type: "user", IsActive: true, Permissions: json.RawMessage(`{"accessAllLibraries":true,"accessAllTags":true,"accessExplicitContent":true}`)}
}

func TestSeriesFindOneProgressAndFeed(t *testing.T) {
	rig := newSeriesRig(t)
	if _, err := rig.db.Exec(`INSERT INTO mediaProgresses(id,userId,mediaItemId,isFinished,createdAt,updatedAt) VALUES('p1','u2','b1',1,'2024-01-01 00:00:00.000 +00:00','2024-01-01 00:00:00.000 +00:00')`); err != nil {
		t.Fatal(err)
	}
	reader := seriesReadOnlyUser()
	w := rig.do("GET", "/api/series/s1?include=%20progress%20,%20rssfeed,,", "", reader)
	if w.Code != 200 {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	var body struct {
		SeriesView
		Progress *SeriesProgress `json:"progress"`
		RSSFeed  json.RawMessage `json:"rssFeed"`
	}
	decodeSeriesBody(t, w, &body)
	if body.ID != "s1" || body.NameIgnorePrefix == nil || *body.NameIgnorePrefix != "First Series" {
		t.Fatalf("series %+v", body.SeriesView)
	}
	if body.Progress == nil || strings.Join(body.Progress.LibraryItemIDs, ",") != "i1,i2" || strings.Join(body.Progress.LibraryItemIDsFinished, ",") != "i1" || body.Progress.IsFinished {
		t.Fatalf("progress %+v", body.Progress)
	}
	if string(body.RSSFeed) != "null" {
		t.Fatalf("rssFeed %s", body.RSSFeed)
	}
	if _, err := rig.db.Exec(`INSERT INTO mediaProgresses(id,userId,mediaItemId,isFinished,createdAt,updatedAt) VALUES('p2','u2','b2',1,'2024-01-01 00:00:00.000 +00:00','2024-01-01 00:00:00.000 +00:00')`); err != nil {
		t.Fatal(err)
	}
	if _, err := rig.db.Exec(`INSERT INTO feeds(id,entityType,entityId,feedURL,title,description,preventIndexing,ownerName,ownerEmail,createdAt,updatedAt) VALUES('f1','series','s1','http://x/feed','First Series',NULL,1,NULL,NULL,'2024-01-01 00:00:00.000 +00:00','2024-01-01 00:00:00.000 +00:00')`); err != nil {
		t.Fatal(err)
	}
	decodeSeriesBody(t, rig.do("GET", "/api/series/s1?include=progress,rssfeed", "", reader), &body)
	if !body.Progress.IsFinished || len(body.Progress.LibraryItemIDsFinished) != 2 {
		t.Fatalf("progress %+v", body.Progress)
	}
	var feed struct {
		ID, EntityType string
		Meta           struct{ PreventIndexing bool }
	}
	if err := json.Unmarshal(body.RSSFeed, &feed); err != nil || feed.ID != "f1" || !feed.Meta.PreventIndexing {
		t.Fatalf("feed %s %v", body.RSSFeed, err)
	}
	var plain map[string]json.RawMessage
	decodeSeriesBody(t, rig.do("GET", "/api/series/s1", "", reader), &plain)
	if _, ok := plain["progress"]; ok {
		t.Fatal("progress without include")
	}
	if _, ok := plain["rssFeed"]; ok {
		t.Fatal("rssFeed without include")
	}
}

func TestSeriesAccessRules(t *testing.T) {
	rig := newSeriesRig(t)
	if w := rig.do("GET", "/api/series/nope", "", seriesRootUser()); w.Code != 404 {
		t.Fatalf("unknown series %d", w.Code)
	}
	if _, err := rig.db.Exec(`UPDATE books SET explicit=1`); err != nil {
		t.Fatal(err)
	}
	clean := store.User{ID: "u9", IsActive: true, Permissions: json.RawMessage(`{"accessAllLibraries":true,"accessAllTags":true,"update":true}`)}
	if w := rig.do("GET", "/api/series/s1", "", clean); w.Code != 404 {
		t.Fatalf("no accessible books: %d", w.Code)
	}
	if w := rig.do("PATCH", "/api/series/s1", `{"name":"x"}`, clean); w.Code != 404 {
		t.Fatalf("patch without accessible books: %d", w.Code)
	}
	if w := rig.do("PATCH", "/api/series/s1", `{"name":"x"}`, seriesReadOnlyUser()); w.Code != 403 {
		t.Fatalf("update permission: %d", w.Code)
	}
}

func TestSeriesUpdate(t *testing.T) {
	rig := newSeriesRig(t)
	for _, body := range []string{`{}`, `{"name":5,"description":null}`, `[1]`} {
		if w := rig.do("PATCH", "/api/series/s1", body, seriesRootUser()); w.Code != 400 || w.Body.String() != "No valid fields to update" {
			t.Fatalf("body %s: %d %q", body, w.Code, w.Body)
		}
	}
	w := rig.do("PATCH", "/api/series/s1", `{"name":"The Renamed","description":"About"}`, seriesRootUser())
	if w.Code != 200 {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	var got SeriesView
	decodeSeriesBody(t, w, &got)
	if got.Name == nil || *got.Name != "The Renamed" || got.NameIgnorePrefix == nil || *got.NameIgnorePrefix != "Renamed, The" || got.Description == nil || *got.Description != "About" || got.UpdatedAt <= got.AddedAt {
		t.Fatalf("series %+v", got)
	}
	var column string
	if err := rig.db.QueryRow(`SELECT nameIgnorePrefix FROM series WHERE id='s1'`).Scan(&column); err != nil || column != "First Series" {
		t.Fatalf("the prefix column is not recomputed upstream: %q %v", column, err)
	}
	recorded := rig.takeEvents()
	if len(recorded) != 1 || recorded[0].event != "series_updated" {
		t.Fatalf("events %+v", recorded)
	}
	if payload, ok := recorded[0].payload.(SeriesView); !ok || payload.ID != "s1" {
		t.Fatalf("payload %+v", recorded[0].payload)
	}
	var again SeriesView
	decodeSeriesBody(t, rig.do("PATCH", "/api/series/s1", `{"name":"The Renamed","description":"About"}`, seriesRootUser()), &again)
	if again.UpdatedAt != got.UpdatedAt || len(rig.takeEvents()) != 0 {
		t.Fatalf("no-op changed state: %+v", again)
	}
	decodeSeriesBody(t, rig.do("PATCH", "/api/series/s1", `{"description":""}`, seriesRootUser()), &again)
	if again.Description == nil || *again.Description != "" {
		t.Fatalf("description %v", again.Description)
	}
}
