package lists

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	schemaassets "audiobookshelf-compat/contracts/parity"
	"audiobookshelf-compat/internal/events"
	"audiobookshelf-compat/internal/store"
)

type sentEvent struct {
	audience, event string
	payload         any
}

// listRig serves the list routes over the production schema with foreign keys
// on, so deletions cascade the way they do in the real database.
type listRig struct {
	t      *testing.T
	db     *sql.DB
	mux    *http.ServeMux
	events []sentEvent
}

func newListRig(t *testing.T) *listRig {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+filepath.Join(t.TempDir(), "db.sqlite")+"?_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	exec(t, db, schemaassets.SchemaSQL)
	exec(t, db, `INSERT INTO libraries(id,name,displayOrder,icon,mediaType,provider,settings,extraData,createdAt,updatedAt) VALUES('library-1','Books',1,'books','book','google','{}','{}',?,?)`, stamp, stamp)
	exec(t, db, `INSERT INTO users(id,username,type,isActive,permissions,createdAt,updatedAt) VALUES('owner','owner','user',1,'{}',?,?),('other','other','user',1,'{}',?,?)`, stamp, stamp, stamp, stamp)
	addBook(t, db, "item-1", "book-1", "First")
	addBook(t, db, "item-2", "book-2", "Second")
	r := &listRig{t: t, db: db, mux: http.NewServeMux()}
	New(db, events.Emitter(func(audience, event string, payload any) {
		r.events = append(r.events, sentEvent{audience, event, payload})
	})).Routes(r.mux)
	return r
}

func (r *listRig) do(user, method, target, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, target, strings.NewReader(body))
	u := store.User{ID: user, Username: user, Type: "user", IsActive: true,
		Permissions: json.RawMessage(`{"accessAllLibraries":true,"accessAllTags":true,"accessExplicitContent":true,"update":true,"delete":true}`)}
	w := httptest.NewRecorder()
	r.mux.ServeHTTP(w, req.WithContext(store.ContextWithUser(req.Context(), u)))
	return w
}

func (r *listRig) count(query string, args ...any) int {
	r.t.Helper()
	var n int
	if err := r.db.QueryRow(query, args...).Scan(&n); err != nil {
		r.t.Fatal(err)
	}
	return n
}

func (r *listRig) take() []sentEvent {
	out := r.events
	r.events = nil
	return out
}

func (r *listRig) playlist(id string, items ...string) {
	r.t.Helper()
	exec(r.t, r.db, `INSERT INTO playlists(id,name,libraryId,userId,createdAt,updatedAt) VALUES(?,?,'library-1','owner',?,?)`, id, id, stamp, stamp)
	for i, mediaID := range items {
		exec(r.t, r.db, `INSERT INTO playlistMediaItems(id,mediaItemId,mediaItemType,"order",playlistId,createdAt) VALUES(?,?,'book',?,?,?)`, id+"-"+mediaID, mediaID, i+1, id, stamp)
	}
}

// PlaylistController.removeItem (PlaylistController.js:347-385): taking out the
// last item removes the playlist and tells its owner with playlist_removed;
// otherwise the rest is renumbered and the owner gets playlist_updated.
func TestPlaylistRemoveItemRemovesEmptiedPlaylist(t *testing.T) {
	r := newListRig(t)
	r.playlist("pl", "book-1", "book-2")

	if w := r.do("owner", "DELETE", "/api/playlists/pl/item/item-1", ""); w.Code != 200 {
		t.Fatalf("remove first: %d %s", w.Code, w.Body)
	}
	ev := r.take()
	if len(ev) != 1 || ev[0].event != "playlist_updated" || ev[0].audience != "owner" {
		t.Fatalf("events %+v", ev)
	}
	if got := r.count(`SELECT "order" FROM playlistMediaItems WHERE playlistId='pl' AND mediaItemId='book-2'`); got != 1 {
		t.Fatalf("remaining entry kept order %d, want 1", got)
	}

	if w := r.do("owner", "DELETE", "/api/playlists/pl/item/item-1", ""); w.Code != 404 {
		t.Fatalf("item already gone: %d", w.Code)
	}
	if len(r.take()) != 0 {
		t.Fatal("a refused removal announced something")
	}

	w := r.do("owner", "DELETE", "/api/playlists/pl/item/item-2", "")
	if w.Code != 200 {
		t.Fatalf("remove last: %d %s", w.Code, w.Body)
	}
	var body struct {
		ID    string `json:"id"`
		Items []any  `json:"items"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil || body.ID != "pl" || len(body.Items) != 0 {
		t.Fatalf("response %s (%v)", w.Body, err)
	}
	if r.count(`SELECT COUNT(*) FROM playlists WHERE id='pl'`) != 0 || r.count(`SELECT COUNT(*) FROM playlistMediaItems`) != 0 {
		t.Fatal("emptied playlist still stored")
	}
	ev = r.take()
	if len(ev) != 1 || ev[0].event != "playlist_removed" || ev[0].audience != "owner" {
		t.Fatalf("events %+v", ev)
	}
	// The library items themselves are untouched.
	if r.count(`SELECT COUNT(*) FROM libraryItems`) != 2 {
		t.Fatal("removing playlist entries deleted library items")
	}
}

// Deleting a playlist drops its entries but not the items; only its owner may
// (PlaylistController.middleware, js:575-593 answers 403 to anyone else).
func TestPlaylistDeleteIsOwnerOnlyAndKeepsItems(t *testing.T) {
	r := newListRig(t)
	r.playlist("pl", "book-1", "book-2")
	if w := r.do("other", "DELETE", "/api/playlists/pl", ""); w.Code != 403 {
		t.Fatalf("non-owner: %d", w.Code)
	}
	if r.count(`SELECT COUNT(*) FROM playlistMediaItems`) != 2 || len(r.take()) != 0 {
		t.Fatal("a refused delete changed something")
	}
	if w := r.do("owner", "DELETE", "/api/playlists/missing", ""); w.Code != 404 {
		t.Fatalf("unknown playlist: %d", w.Code)
	}
	if w := r.do("owner", "DELETE", "/api/playlists/pl", ""); w.Code != 200 {
		t.Fatalf("owner: %d", w.Code)
	}
	if r.count(`SELECT COUNT(*) FROM playlists`) != 0 || r.count(`SELECT COUNT(*) FROM playlistMediaItems`) != 0 || r.count(`SELECT COUNT(*) FROM libraryItems`) != 2 {
		t.Fatal("playlist, entries or items wrong after delete")
	}
	if ev := r.take(); len(ev) != 1 || ev[0].event != "playlist_removed" || ev[0].audience != "owner" {
		t.Fatalf("events %+v", ev)
	}
}

// CollectionController.delete: the collection and its entries go, the books
// stay, everyone is told, and a user without the delete permission changes nothing.
func TestCollectionDeleteRemovesEntriesKeepsBooks(t *testing.T) {
	r := newListRig(t)
	exec(t, r.db, `INSERT INTO collections(id,name,libraryId,createdAt,updatedAt) VALUES('col','Shelf','library-1',?,?)`, stamp, stamp)
	exec(t, r.db, `INSERT INTO collectionBooks(id,"order",bookId,collectionId,createdAt) VALUES('cb1',1,'book-1','col',?),('cb2',2,'book-2','col',?)`, stamp, stamp)

	req := httptest.NewRequest("DELETE", "/api/collections/col", nil)
	noDelete := store.User{ID: "other", Type: "user", IsActive: true, Permissions: json.RawMessage(`{"accessAllLibraries":true,"accessAllTags":true,"accessExplicitContent":true}`)}
	w := httptest.NewRecorder()
	r.mux.ServeHTTP(w, req.WithContext(store.ContextWithUser(req.Context(), noDelete)))
	if w.Code != 403 || r.count(`SELECT COUNT(*) FROM collections`) != 1 || len(r.take()) != 0 {
		t.Fatalf("without delete permission: %d", w.Code)
	}
	if w := r.do("owner", "DELETE", "/api/collections/missing", ""); w.Code != 404 {
		t.Fatalf("unknown collection: %d", w.Code)
	}

	if w := r.do("owner", "DELETE", "/api/collections/col", ""); w.Code != 200 {
		t.Fatalf("delete: %d %s", w.Code, w.Body)
	}
	if r.count(`SELECT COUNT(*) FROM collections`) != 0 || r.count(`SELECT COUNT(*) FROM collectionBooks`) != 0 {
		t.Fatal("collection or its entries remain")
	}
	if r.count(`SELECT COUNT(*) FROM books`) != 2 || r.count(`SELECT COUNT(*) FROM libraryItems`) != 2 {
		t.Fatal("deleting a collection deleted books")
	}
	ev := r.take()
	if len(ev) != 1 || ev[0].event != "collection_removed" || ev[0].audience != events.AllUsers {
		t.Fatalf("events %+v", ev)
	}
}
