package catalog

import (
	"database/sql"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	schemaassets "audiobookshelf-compat/contracts/parity"
	"audiobookshelf-compat/internal/config"
	"audiobookshelf-compat/internal/filterdata"
	"audiobookshelf-compat/internal/store"
)

// cascadeRig is the production schema (foreign keys on) with two books that
// share one author, so a deletion shows both what goes and what must stay.
type cascadeRig struct {
	t      *testing.T
	db     *sql.DB
	mux    *http.ServeMux
	mu     sync.Mutex
	events []recordedEvent
	to     map[string][]string // event -> audiences
}

func newCascadeRig(t *testing.T) *cascadeRig {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+filepath.Join(t.TempDir(), "db.sqlite")+"?_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { db.Close() })
	r := &cascadeRig{t: t, db: db, to: map[string][]string{}}
	r.exec(schemaassets.SchemaSQL)
	const ts = "2024-01-01 00:00:00.000 +00:00"
	for _, q := range []string{
		`INSERT INTO users(id,username,type,isActive,permissions,createdAt,updatedAt) VALUES('u1','root','root',1,'{}',?,?),('u2','reader','user',1,'{}',?,?)`,
		`INSERT INTO libraries(id,name,displayOrder,icon,mediaType,provider,settings,extraData,createdAt,updatedAt) VALUES('l1','Books',1,'books','book','google','{}','{}',?,?),('l2','Other',2,'books','book','google','{}','{}',?,?)`,
		`INSERT INTO libraryFolders(id,path,libraryId,createdAt,updatedAt) VALUES('f1','/nonexistent/books','l1',?,?)`,
		`INSERT INTO authors(id,name,lastFirst,libraryId,createdAt,updatedAt) VALUES('a-only1','Only One','One, Only','l1',?,?),('a-both','Both Books','Books, Both','l1',?,?)`,
		`INSERT INTO series(id,name,nameIgnorePrefix,libraryId,createdAt,updatedAt) VALUES('s1','Saga','Saga','l1',?,?)`,
		`INSERT INTO books(id,title,titleIgnorePrefix,narrators,audioFiles,chapters,tags,genres,createdAt,updatedAt) VALUES('b1','First','First','[]','[]','[]','[]','[]',?,?),('b2','Second','Second','[]','[]','[]','[]','[]',?,?)`,
		`INSERT INTO libraryItems(id,ino,path,relPath,mediaId,mediaType,isFile,isMissing,isInvalid,size,libraryFiles,extraData,title,titleIgnorePrefix,libraryId,libraryFolderId,createdAt,updatedAt) VALUES
			('i1','1','/nonexistent/books/First','First','b1','book',0,0,0,1,'[]','{}','First','First','l1','f1',?,?),
			('i2','2','/nonexistent/books/Second','Second','b2','book',0,0,0,1,'[]','{}','Second','Second','l1','f1',?,?)`,
		`INSERT INTO bookAuthors(id,bookId,authorId,createdAt) VALUES('ba1','b1','a-only1',?),('ba2','b1','a-both',?),('ba3','b2','a-both',?)`,
		`INSERT INTO bookSeries(id,sequence,bookId,seriesId,createdAt) VALUES('bs1','1','b1','s1',?)`,
		`INSERT INTO mediaProgresses(id,mediaItemId,mediaItemType,duration,currentTime,isFinished,userId,createdAt,updatedAt) VALUES('mp1','b1','book',10,5,0,'u1',?,?),('mp2','b1','book',10,10,1,'u2',?,?),('mp3','b2','book',10,1,0,'u1',?,?)`,
		`INSERT INTO playlists(id,name,libraryId,userId,createdAt,updatedAt) VALUES('p-shared','Mix','l1','u1',?,?),('p-solo','Solo','l1','u2',?,?)`,
		`INSERT INTO playlistMediaItems(id,mediaItemId,mediaItemType,"order",playlistId,createdAt) VALUES('x1','b1','book',1,'p-shared',?),('x2','b2','book',2,'p-shared',?),('x3','b1','book',1,'p-solo',?)`,
		`INSERT INTO collections(id,name,libraryId,createdAt,updatedAt) VALUES('c-mixed','Mixed','l1',?,?),('c-solo','OnlyFirst','l1',?,?)`,
		`INSERT INTO collectionBooks(id,"order",bookId,collectionId,createdAt) VALUES('cb1',1,'b1','c-mixed',?),('cb2',2,'b2','c-mixed',?),('cb3',1,'b1','c-solo',?)`,
		`INSERT INTO feeds(id,slug,entityType,entityId,createdAt,updatedAt) VALUES('feed1','f','libraryItem','i1',?,?),('feed2','g','libraryItem','i2',?,?)`,
		`INSERT INTO mediaItemShares(id,mediaItemId,mediaItemType,slug,createdAt,updatedAt) VALUES('sh1','b1','book','one',?,?),('sh2','b2','book','two',?,?)`,
		`INSERT INTO playbackSessions(id,mediaItemId,mediaItemType,userId,libraryId,createdAt,updatedAt) VALUES('ps1','b2','book','u1','l1',?,?)`,
	} {
		args := make([]any, strings.Count(q, "?"))
		for i := range args {
			args[i] = ts
		}
		r.exec(q, args...)
	}
	h := New(db, config.Config{MetadataPath: t.TempDir()}, func(audience, event string, payload any) {
		r.mu.Lock()
		defer r.mu.Unlock()
		r.events = append(r.events, recordedEvent{event, payload})
		r.to[event] = append(r.to[event], audience)
	}, Deps{Filters: filterdata.NewCache()})
	r.mux = http.NewServeMux()
	h.Routes(r.mux)
	return r
}

func (r *cascadeRig) exec(q string, args ...any) {
	r.t.Helper()
	if _, err := r.db.Exec(q, args...); err != nil {
		r.t.Fatalf("%s: %v", q, err)
	}
}

func (r *cascadeRig) do(method, target string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, target, nil)
	req = req.WithContext(store.ContextWithUser(req.Context(), rootUser()))
	w := httptest.NewRecorder()
	r.mux.ServeHTTP(w, req)
	return w
}

// ids returns the sorted first column of a query.
func (r *cascadeRig) ids(query string) string {
	r.t.Helper()
	rows, err := r.db.Query(query)
	if err != nil {
		r.t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			r.t.Fatal(err)
		}
		out = append(out, s)
	}
	return strings.Join(out, ",")
}

func (r *cascadeRig) filterData() filterdata.Data {
	r.t.Helper()
	w := r.do("GET", "/api/libraries/l1/filterdata")
	if w.Code != 200 {
		r.t.Fatalf("filterdata %d %s", w.Code, w.Body)
	}
	var d filterdata.Data
	decodeAuthorBody(r.t, w, &d)
	return d
}

func names(list []filterdata.Named) string {
	var out []string
	for _, n := range list {
		out = append(out, n.Name)
	}
	return strings.Join(out, ",")
}

// Deleting a book item (ApiRouter.handleDeleteLibraryItem, ApiRouter.js:373-407
// and LibraryItemController.delete) removes what hangs off that item - progress
// of every user, playlist entries (and playlists left empty), collection
// entries, its RSS feed and shares, the authors and series it leaves empty -
// and nothing that belongs to the other book.
func TestDeleteItemCascadesToEverythingReferencingIt(t *testing.T) {
	r := newCascadeRig(t)
	before := r.filterData() // cached: the deletion must patch it
	if names(before.Authors) != "Both Books,Only One" || names(before.Series) != "Saga" {
		t.Fatalf("filter data before: authors=%q series=%q", names(before.Authors), names(before.Series))
	}

	if w := r.do("DELETE", "/api/items/i1"); w.Code != 200 {
		t.Fatalf("delete: %d %s", w.Code, w.Body)
	}

	for _, c := range []struct{ what, query, want string }{
		{"items", `SELECT id FROM libraryItems ORDER BY id`, "i2"},
		{"books", `SELECT id FROM books ORDER BY id`, "b2"},
		{"progress of every user", `SELECT id FROM mediaProgresses ORDER BY id`, "mp3"},
		{"playlists (emptied one removed)", `SELECT id FROM playlists ORDER BY id`, "p-shared"},
		{"playlist entries", `SELECT id FROM playlistMediaItems ORDER BY id`, "x2"},
		{"collection entries", `SELECT id FROM collectionBooks ORDER BY id`, "cb2"},
		{"collections themselves stay", `SELECT id FROM collections ORDER BY id`, "c-mixed,c-solo"},
		{"feeds", `SELECT id FROM feeds ORDER BY id`, "feed2"},
		{"shares", `SELECT id FROM mediaItemShares ORDER BY id`, "sh2"},
		{"author left empty goes, shared author stays", `SELECT id FROM authors ORDER BY id`, "a-both"},
		{"author links", `SELECT id FROM bookAuthors ORDER BY id`, "ba3"},
		{"series left empty", `SELECT id FROM series`, ""},
		{"playback sessions are history, kept", `SELECT id FROM playbackSessions`, "ps1"},
	} {
		if got := r.ids(c.query); got != c.want {
			t.Errorf("%s: %q, want %q", c.what, got, c.want)
		}
	}
	// The remaining playlist's entry keeps its place for the other book.
	if got := r.ids(`SELECT mediaItemId FROM playlistMediaItems WHERE playlistId='p-shared'`); got != "b2" {
		t.Errorf("p-shared entries %q", got)
	}

	// Clients are told: the item, the owner of each touched playlist, the swept author and series.
	if got := r.to["item_removed"]; len(got) != 1 {
		t.Errorf("item_removed audiences %v", got)
	}
	if got := r.to["playlist_updated"]; len(got) != 1 || got[0] != "u1" {
		t.Errorf("playlist_updated audiences %v", got)
	}
	if got := r.to["playlist_removed"]; len(got) != 1 || got[0] != "u2" {
		t.Errorf("playlist_removed audiences %v", got)
	}

	// The served item is gone, and the cached filter data was patched.
	if w := r.do("GET", "/api/items/i1"); w.Code != 404 {
		t.Errorf("deleted item still served: %d", w.Code)
	}
	if w := r.do("GET", "/api/items/i2"); w.Code != 200 {
		t.Errorf("other item: %d", w.Code)
	}
	after := r.filterData()
	if names(after.Authors) != "Both Books" || names(after.Series) != "" {
		t.Errorf("filter data after: authors=%q series=%q", names(after.Authors), names(after.Series))
	}
}

// Deleting a library (LibraryController.delete, LibraryController.js:535-580)
// removes its items with the same cascade, its collections, authors and
// series, its folders and its cached filter data, and only detaches playback
// sessions. Another library is untouched.
func TestDeleteLibraryCascades(t *testing.T) {
	r := newCascadeRig(t)
	r.exec(`INSERT INTO authors(id,name,lastFirst,libraryId,createdAt,updatedAt) VALUES('a-l2','Elsewhere','Elsewhere','l2','2024-01-01 00:00:00.000 +00:00','2024-01-01 00:00:00.000 +00:00')`)
	r.filterData() // cache it; the library's removal must drop it

	if w := r.do("DELETE", "/api/libraries/l1"); w.Code != 200 {
		t.Fatalf("delete: %d %s", w.Code, w.Body)
	}
	for _, c := range []struct{ what, query, want string }{
		{"libraries", `SELECT id FROM libraries`, "l2"},
		{"folders", `SELECT id FROM libraryFolders`, ""},
		{"items", `SELECT id FROM libraryItems`, ""},
		{"books", `SELECT id FROM books`, ""},
		{"progress", `SELECT id FROM mediaProgresses`, ""},
		{"playlists emptied by the removal", `SELECT id FROM playlists`, ""},
		{"playlist entries", `SELECT id FROM playlistMediaItems`, ""},
		{"collections", `SELECT id FROM collections`, ""},
		{"collection entries", `SELECT id FROM collectionBooks`, ""},
		{"feeds", `SELECT id FROM feeds`, ""},
		{"shares", `SELECT id FROM mediaItemShares`, ""},
		{"authors of the library only", `SELECT id FROM authors`, "a-l2"},
		{"series", `SELECT id FROM series`, ""},
		{"sessions kept, detached from the library", `SELECT id||':'||COALESCE(libraryId,'null') FROM playbackSessions`, "ps1:null"},
	} {
		if got := r.ids(c.query); got != c.want {
			t.Errorf("%s: %q, want %q", c.what, got, c.want)
		}
	}
	if w := r.do("GET", "/api/libraries/l1/filterdata"); w.Code != 404 {
		t.Errorf("filter data of the deleted library: %d", w.Code)
	}
	if got := r.to["library_removed"]; len(got) != 1 {
		t.Errorf("library_removed audiences %v", got)
	}
}
