package list

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"audiobookshelf-compat/internal/auth"
	"audiobookshelf-compat/internal/db"
	"audiobookshelf-compat/internal/db/dbtest"
	"audiobookshelf-compat/internal/events"
	"audiobookshelf-compat/internal/item"
	"audiobookshelf-compat/internal/settings"
)

const stamp = "2024-01-02 03:04:05.000 +00:00"

type sentEvent struct {
	audience, event string
	payload         any
}

// rig serves the list routes over the pinned schema with foreign keys on, so
// deletions cascade as in a real database.
type rig struct {
	t      *testing.T
	db     *db.DB
	h      *Handler
	mux    *http.ServeMux
	events []sentEvent
}

func newRig(t *testing.T) *rig {
	t.Helper()
	d := dbtest.New(t)
	r := &rig{t: t, db: d, mux: http.NewServeMux()}
	r.exec(`INSERT INTO libraries(id,name,displayOrder,icon,mediaType,provider,settings,extraData,createdAt,updatedAt) VALUES('library-1','Books',1,'books','book','google','{}','{}',?,?)`, stamp, stamp)
	r.exec(`INSERT INTO users(id,username,type,isActive,permissions,createdAt,updatedAt) VALUES('owner','owner','user',1,'{}',?,?),('other','other','user',1,'{}',?,?)`, stamp, stamp, stamp, stamp)
	r.addBook("item-1", "book-1", "First", false)
	r.addBook("item-2", "book-2", "Second", false)
	store, err := settings.NewStore(context.Background(), d, func(string) string { return "" }, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	r.h = New(d, store, events.Emitter(func(audience, event string, payload any) {
		r.events = append(r.events, sentEvent{audience, event, payload})
	}))
	r.h.Routes(r.mux)
	return r
}

func (r *rig) exec(query string, args ...any) {
	r.t.Helper()
	if _, err := r.db.Exec(context.Background(), query, args...); err != nil {
		r.t.Fatalf("%s: %v", query, err)
	}
}

func (r *rig) addBook(itemID, mediaID, title string, explicit bool) {
	r.t.Helper()
	r.exec(`INSERT INTO books (id,title,explicit,abridged,duration,tags,genres,narrators,audioFiles,chapters,createdAt,updatedAt) VALUES (?,?,?,0,10,'[]','[]','[]','[]','[]',?,?)`, mediaID, title, explicit, stamp, stamp)
	r.exec(`INSERT INTO libraryItems (id,path,relPath,mediaId,mediaType,isFile,isMissing,isInvalid,size,libraryFiles,extraData,createdAt,updatedAt,libraryId) VALUES (?,?,?,?, 'book',0,0,0,1,'[]','{}',?,?,'library-1')`, itemID, "/"+itemID, itemID, mediaID, stamp, stamp)
}

// principal builds a user of the rig; update and delete are the action flags.
func principal(id string, update, deleteIt, explicit bool) *auth.Principal {
	return auth.NewPrincipal(db.User{ID: id, Type: db.Ptr("user"), IsActive: true, Permissions: db.JSON[db.Permissions]{V: db.Permissions{
		Update: update, Delete: deleteIt, AccessAllLibraries: true, AccessAllTags: true, AccessExplicitContent: explicit}}})
}

func (r *rig) doAs(user *auth.Principal, method, target, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, target, strings.NewReader(body))
	w := httptest.NewRecorder()
	r.mux.ServeHTTP(w, req.WithContext(auth.WithUser(req.Context(), user)))
	return w
}

// do sends the request as the user id, who may update and delete.
func (r *rig) do(id, method, target, body string) *httptest.ResponseRecorder {
	return r.doAs(principal(id, true, true, true), method, target, body)
}

func (r *rig) count(query string, args ...any) int {
	r.t.Helper()
	var n int
	if err := r.db.Get(context.Background(), &n, query, args...); err != nil {
		r.t.Fatal(err)
	}
	return n
}

func (r *rig) take() []sentEvent {
	out := r.events
	r.events = nil
	return out
}

func (r *rig) playlist(id string, items ...string) {
	r.t.Helper()
	r.exec(`INSERT INTO playlists(id,name,libraryId,userId,createdAt,updatedAt) VALUES(?,?,'library-1','owner',?,?)`, id, id, stamp, stamp)
	for i, mediaID := range items {
		r.exec(`INSERT INTO playlistMediaItems(id,mediaItemId,mediaItemType,"order",playlistId,createdAt) VALUES(?,?,'book',?,?,?)`, id+"-"+mediaID, mediaID, i+1, id, stamp)
	}
}

func (r *rig) collection(id string, books ...string) {
	r.t.Helper()
	r.exec(`INSERT INTO collections(id,name,libraryId,createdAt,updatedAt) VALUES(?,'Shelf','library-1',?,?)`, id, stamp, stamp)
	for i, bookID := range books {
		r.exec(`INSERT INTO collectionBooks(id,"order",bookId,collectionId,createdAt) VALUES(?,?,?,?,?)`, id+"-"+bookID, i+1, bookID, id, stamp)
	}
}

func decode(t *testing.T, w *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var out map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("response %q: %v", w.Body, err)
	}
	return out
}

// Taking out the last playlist item removes the playlist and tells its owner
// with playlist_removed; otherwise the rest is renumbered and the owner gets
// playlist_updated.
func TestPlaylistRemoveItemRemovesEmptiedPlaylist(t *testing.T) {
	r := newRig(t)
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
	if w := r.do("owner", "DELETE", "/api/playlists/pl/item/item-1", ""); w.Code != 404 || len(r.take()) != 0 {
		t.Fatalf("item already gone: %d", w.Code)
	}

	w := r.do("owner", "DELETE", "/api/playlists/pl/item/item-2", "")
	if w.Code != 200 {
		t.Fatalf("remove last: %d %s", w.Code, w.Body)
	}
	if body := decode(t, w); body["id"] != "pl" || len(body["items"].([]any)) != 0 {
		t.Fatalf("response %s", w.Body)
	}
	if r.count(`SELECT COUNT(*) FROM playlists`) != 0 || r.count(`SELECT COUNT(*) FROM playlistMediaItems`) != 0 {
		t.Fatal("emptied playlist still stored")
	}
	if ev = r.take(); len(ev) != 1 || ev[0].event != "playlist_removed" || ev[0].audience != "owner" {
		t.Fatalf("events %+v", ev)
	}
	if r.count(`SELECT COUNT(*) FROM libraryItems`) != 2 {
		t.Fatal("removing playlist entries deleted library items")
	}
}

// Only the owner may touch a playlist; deleting it drops its entries, not the items.
func TestPlaylistDeleteIsOwnerOnlyAndKeepsItems(t *testing.T) {
	r := newRig(t)
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

// Deleting a collection removes it and its entries, keeps the books, tells
// everyone, and a user without the delete permission changes nothing.
func TestCollectionDeleteRemovesEntriesKeepsBooks(t *testing.T) {
	r := newRig(t)
	r.collection("col", "book-1", "book-2")

	if w := r.doAs(principal("other", true, false, true), "DELETE", "/api/collections/col", ""); w.Code != 403 || r.count(`SELECT COUNT(*) FROM collections`) != 1 || len(r.take()) != 0 {
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
	// The hub filters by libraryId, which only a generic object exposes.
	if payload, ok := ev[0].payload.(interface{ EventLibraryID() string }); !ok || payload.EventLibraryID() != "library-1" {
		t.Fatalf("collection event payload %#v must be library scoped", ev[0].payload)
	}
}

func (r *rig) collectionWithFeed() {
	r.collection("col", "book-1")
	r.exec(`INSERT INTO feeds(id,slug,entityType,entityId,createdAt,updatedAt) VALUES('feed','shelf','collection','col',?,?)`, stamp, stamp)
	r.exec(`INSERT INTO feedEpisodes(id,title,createdAt,updatedAt,feedId) VALUES('feed-episode','One',?,?,'feed')`, stamp, stamp)
}

// Deleting a collection closes its RSS feed first and says so before the
// collection is announced as removed.
func TestCollectionDeleteClosesRSSFeed(t *testing.T) {
	r := newRig(t)
	r.collectionWithFeed()
	if w := r.do("owner", "DELETE", "/api/collections/col", ""); w.Code != 200 {
		t.Fatalf("delete: %d %s", w.Code, w.Body)
	}
	if r.count(`SELECT COUNT(*) FROM feeds`) != 0 || r.count(`SELECT COUNT(*) FROM feedEpisodes`) != 0 {
		t.Error("feed rows remain")
	}
	ev := r.take()
	if len(ev) != 2 || ev[0].event != "rss_feed_closed" || ev[1].event != "collection_removed" || ev[0].audience != events.AllUsers {
		t.Errorf("events = %+v, want rss_feed_closed then collection_removed to all users", ev)
	}
	if w := r.do("owner", "DELETE", "/api/collections/col", ""); w.Code != 404 || len(r.take()) != 0 {
		t.Errorf("repeat delete = %d, want 404 and no events", w.Code)
	}
}

func TestCollectionDeleteFailureRollsBackAndStaysSilent(t *testing.T) {
	for name, trigger := range map[string]string{
		"feed_close":                         `CREATE TRIGGER reject_feed_close BEFORE DELETE ON feeds BEGIN SELECT RAISE(ABORT,'close rejected'); END`,
		"collection_delete_after_feed_close": `CREATE TRIGGER reject_collection_delete BEFORE DELETE ON collections BEGIN SELECT RAISE(ABORT,'delete rejected'); END`,
	} {
		t.Run(name, func(t *testing.T) {
			r := newRig(t)
			r.collectionWithFeed()
			r.exec(trigger)
			if w := r.do("owner", "DELETE", "/api/collections/col", ""); w.Code != http.StatusInternalServerError {
				t.Fatalf("status = %d, want 500", w.Code)
			}
			for _, query := range []string{`SELECT COUNT(*) FROM collections`, `SELECT COUNT(*) FROM feeds`, `SELECT COUNT(*) FROM feedEpisodes`} {
				if got := r.count(query); got != 1 {
					t.Errorf("%s = %d, want 1 after rollback", query, got)
				}
			}
			if got := r.take(); len(got) != 0 {
				t.Errorf("failed delete emitted %+v", got)
			}
		})
	}
}

// A collection shows only the books the user may read, in collection order,
// with an explicit null description.
func TestCollectionHidesBooksTheUserCannotRead(t *testing.T) {
	r := newRig(t)
	r.addBook("item-3", "book-3", "Third", true)
	r.collection("col", "book-1", "book-3", "book-2")

	w := r.doAs(principal("other", false, false, false), "GET", "/api/collections/col", "")
	if w.Code != 200 {
		t.Fatalf("get: %d %s", w.Code, w.Body)
	}
	body := decode(t, w)
	if description, present := body["description"]; !present || description != nil {
		t.Errorf("description = %v (present %v), want explicit null", description, present)
	}
	books := body["books"].([]any)
	if len(books) != 2 || books[0].(map[string]any)["id"] != "item-1" || books[1].(map[string]any)["id"] != "item-2" {
		t.Errorf("books = %v, want item-1 and item-2", books)
	}
	if w := r.do("owner", "GET", "/api/collections/col", ""); len(decode(t, w)["books"].([]any)) != 3 {
		t.Errorf("a user with explicit access sees %s", w.Body)
	}
}

// Playlist entries follow their order, embed the expanded book or the episode
// with its minified podcast, and skip entries whose media is gone.
func TestPlaylistShowsEntriesInOrder(t *testing.T) {
	r := newRig(t)
	r.exec(`INSERT INTO libraries(id,name,displayOrder,icon,mediaType,provider,settings,extraData,createdAt,updatedAt) VALUES('library-2','Casts',2,'podcasts','podcast','itunes','{}','{}',?,?)`, stamp, stamp)
	r.exec(`INSERT INTO podcasts (id,title,explicit,autoDownloadEpisodes,tags,genres,numEpisodes,createdAt,updatedAt) VALUES ('podcast-1','Show',0,0,'[]','[]',1,?,?)`, stamp, stamp)
	r.exec(`INSERT INTO libraryItems (id,path,relPath,mediaId,mediaType,isFile,isMissing,isInvalid,size,libraryFiles,createdAt,updatedAt,libraryId) VALUES ('item-podcast','/p','p','podcast-1','podcast',0,0,0,5,'[]',?,?,'library-1')`, stamp, stamp)
	r.exec(`INSERT INTO podcastEpisodes (id,podcastId,title,audioFile,chapters,extraData,createdAt,updatedAt) VALUES ('episode-1','podcast-1','Episode 1','{"ino":"e1","duration":3,"metadata":{"filename":"e.mp3","size":7}}','[]','{}',?,?)`, stamp, stamp)
	r.exec(`INSERT INTO playlists(id,name,libraryId,userId,createdAt,updatedAt) VALUES('pl','Road trip','library-1','owner',?,?)`, stamp, stamp)
	for i, row := range [][2]string{{"book-1", "book"}, {"episode-1", "podcastEpisode"}, {"missing-book", "book"}, {"missing-episode", "podcastEpisode"}, {"book-2", "book"}} {
		r.exec(`INSERT INTO playlistMediaItems (id,mediaItemId,mediaItemType,"order",createdAt,playlistId) VALUES (?,?,?,?,?, 'pl')`, "entry-"+row[0], row[0], row[1], i+1, stamp)
	}
	w := r.do("owner", "GET", "/api/playlists/pl", "")
	if w.Code != 200 {
		t.Fatalf("get: %d %s", w.Code, w.Body)
	}
	body := decode(t, w)
	if body["description"] != nil {
		t.Errorf("description = %v, want null", body["description"])
	}
	items := body["items"].([]any)
	if len(items) != 3 {
		t.Fatalf("items = %v, want book, episode, book", items)
	}
	var itemIDs []any
	for _, raw := range items {
		itemIDs = append(itemIDs, raw.(map[string]any)["libraryItemId"])
	}
	if itemIDs[0] != "item-1" || itemIDs[1] != "item-podcast" || itemIDs[2] != "item-2" {
		t.Errorf("order = %v", itemIDs)
	}
	book := items[0].(map[string]any)
	if _, has := book["episodeId"]; has || book["libraryItem"].(map[string]any)["media"].(map[string]any)["audioFiles"] == nil {
		t.Errorf("book entry should embed the expanded item: %v", book)
	}
	episode := items[1].(map[string]any)
	media := episode["libraryItem"].(map[string]any)["media"].(map[string]any)
	if episode["episodeId"] != "episode-1" || episode["episode"].(map[string]any)["title"] != "Episode 1" || episode["episode"].(map[string]any)["audioTrack"] == nil {
		t.Errorf("episode entry = %v", episode)
	}
	if _, has := media["episodes"]; has || media["numEpisodes"] != float64(0) {
		t.Errorf("an episode entry's podcast must be minified: %v", media)
	}
}

func TestCollectionCreateReorderAndBatch(t *testing.T) {
	r := newRig(t)
	w := r.do("owner", "POST", "/api/collections", `{"name":"<b>Road</b> &amp; Rail","libraryId":"library-1","description":"","books":["item-1"]}`)
	if w.Code != 200 {
		t.Fatalf("create: %d %s", w.Code, w.Body)
	}
	body := decode(t, w)
	id := body["id"].(string)
	if body["name"] != "Road & Rail" || body["description"] != nil {
		t.Errorf("name %q description %v, want a cleaned name and a null description", body["name"], body["description"])
	}
	if ev := r.take(); len(ev) != 1 || ev[0].event != "collection_added" {
		t.Fatalf("events %+v", ev)
	}
	if w := r.do("owner", "POST", "/api/collections", `{"name":"x","libraryId":"library-1","books":["nope"]}`); w.Code != 400 {
		t.Errorf("unknown book: %d", w.Code)
	}
	if w := r.doAs(principal("other", false, false, true), "POST", "/api/collections", `{"name":"x","libraryId":"library-1","books":["item-1"]}`); w.Code != 403 {
		t.Errorf("without update permission: %d", w.Code)
	}

	r.do("owner", "POST", "/api/collections/"+id+"/batch/add", `{"books":["","item-2","item-1","nope"]}`)
	if got := r.count(`SELECT "order" FROM collectionBooks WHERE collectionId=? AND bookId='book-2'`, id); got != 2 {
		t.Errorf("added book order = %d, want 2", got)
	}
	if ev := r.take(); len(ev) != 1 || ev[0].event != "collection_updated" {
		t.Fatalf("events after batch add %+v", ev)
	}
	if w := r.do("owner", "POST", "/api/collections/"+id+"/batch/add", `{"books":["item-2"]}`); w.Code != 200 || len(r.take()) != 0 {
		t.Errorf("adding a member again changes nothing: %d", w.Code)
	}
	r.do("owner", "PATCH", "/api/collections/"+id, `{"books":["item-2","item-1"]}`)
	if got := r.count(`SELECT "order" FROM collectionBooks WHERE collectionId=? AND bookId='book-2'`, id); got != 1 {
		t.Errorf("reordered book order = %d, want 1", got)
	}
	if ev := r.take(); len(ev) != 1 || ev[0].event != "collection_updated" {
		t.Fatalf("events after reorder %+v", ev)
	}
	r.do("owner", "POST", "/api/collections/"+id+"/batch/remove", `{"books":["item-2"]}`)
	if got := r.count(`SELECT "order" FROM collectionBooks WHERE collectionId=? AND bookId='book-1'`, id); got != 1 {
		t.Errorf("remaining book order = %d, want 1", got)
	}
}

func TestPlaylistCreateAddAndReorder(t *testing.T) {
	r := newRig(t)
	w := r.do("owner", "POST", "/api/playlists", `{"name":"Mix","libraryId":"library-1","items":[{"libraryItemId":"item-1"}]}`)
	if w.Code != 200 {
		t.Fatalf("create: %d %s", w.Code, w.Body)
	}
	id := decode(t, w)["id"].(string)
	if ev := r.take(); len(ev) != 1 || ev[0].event != "playlist_added" || ev[0].audience != "owner" {
		t.Fatalf("events %+v", ev)
	}
	if w := r.do("owner", "POST", "/api/playlists/"+id+"/item", `{"libraryItemId":"item-1"}`); w.Code != 400 {
		t.Errorf("duplicate item: %d", w.Code)
	}
	if w := r.do("owner", "POST", "/api/playlists/"+id+"/batch/add", `{"items":[{"libraryItemId":"item-1"},{"libraryItemId":"item-2"}]}`); w.Code != 200 {
		t.Fatalf("batch add: %d %s", w.Code, w.Body)
	}
	if r.count(`SELECT COUNT(*) FROM playlistMediaItems WHERE playlistId=?`, id) != 2 {
		t.Error("batch add must skip the member and add the other")
	}
	r.take()
	if w := r.do("owner", "PATCH", "/api/playlists/"+id, `{"items":[{"libraryItemId":"item-2"}]}`); w.Code != 400 {
		t.Errorf("length mismatch: %d", w.Code)
	}
	if w := r.do("owner", "PATCH", "/api/playlists/"+id, `{"libraryId":"library-1"}`); w.Code != 400 {
		t.Errorf("changing the library: %d", w.Code)
	}
	if w := r.do("owner", "PATCH", "/api/playlists/"+id, `{"items":[{"libraryItemId":"item-2"},{"libraryItemId":"item-1"}]}`); w.Code != 200 {
		t.Fatalf("reorder: %d %s", w.Code, w.Body)
	}
	if got := r.count(`SELECT "order" FROM playlistMediaItems WHERE playlistId=? AND mediaItemId='book-2'`, id); got != 1 {
		t.Errorf("reordered entry order = %d, want 1", got)
	}
	if ev := r.take(); len(ev) != 1 || ev[0].event != "playlist_updated" {
		t.Errorf("events %+v", ev)
	}
}

func TestLibraryListingsNeedLibraryAccess(t *testing.T) {
	r := newRig(t)
	r.playlist("pl", "book-1")
	nowhere := auth.NewPrincipal(db.User{ID: "owner", Type: db.Ptr("user"), IsActive: true, Permissions: db.JSON[db.Permissions]{V: db.Permissions{AccessAllTags: true}}})
	for _, path := range []string{"/api/libraries/library-1/collections", "/api/libraries/library-1/playlists"} {
		if w := r.doAs(nowhere, "GET", path, ""); w.Code != 403 {
			t.Errorf("%s without library access: %d", path, w.Code)
		}
		if w := r.do("owner", "GET", path+"?limit=-1", ""); w.Code != 400 {
			t.Errorf("%s with a negative limit: %d", path, w.Code)
		}
		if w := r.do("owner", "HEAD", path, ""); w.Code != 404 {
			t.Errorf("HEAD %s: %d", path, w.Code)
		}
	}
	w := r.do("owner", "GET", "/api/libraries/library-1/playlists?limit=1&page=1", "")
	body := decode(t, w)
	if w.Code != 200 || body["total"] != float64(1) || len(body["results"].([]any)) != 0 {
		t.Errorf("second page of one playlist: %d %s", w.Code, w.Body)
	}
	if w := r.do("owner", "GET", "/api/libraries/missing/playlists", ""); w.Code != 404 {
		t.Errorf("unknown library: %d", w.Code)
	}
}

// Deleting a book announces the playlists it left behind to their owner: the
// shrunken one expanded, the emptied one removed and shown without items.
func TestAnnouncePlaylistsAfterItemDeletion(t *testing.T) {
	r := newRig(t)
	r.playlist("mix", "book-1", "book-2")
	r.playlist("solo", "book-1")
	var changes []item.PlaylistChange
	err := r.db.Tx(context.Background(), func(ctx context.Context) error {
		var err error
		changes, err = item.RemoveFromPlaylists(ctx, r.db, []string{"book-1"})
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	r.h.AnnouncePlaylists(context.Background(), changes)
	events := r.take()
	if len(events) != 2 || events[0].event != "playlist_updated" || events[1].event != "playlist_removed" || events[0].audience != "owner" || events[1].audience != "owner" {
		t.Fatalf("events %+v", events)
	}
	for i, wantID := range []string{"mix", "solo"} {
		raw, err := json.Marshal(events[i].payload)
		if err != nil {
			t.Fatal(err)
		}
		var body struct {
			ID    string `json:"id"`
			Items []struct {
				LibraryItemID string `json:"libraryItemId"`
			} `json:"items"`
		}
		if err := json.Unmarshal(raw, &body); err != nil || body.ID != wantID {
			t.Fatalf("payload %s: %v", raw, err)
		}
		if wantItems := 1 - i; len(body.Items) != wantItems || (wantItems == 1 && body.Items[0].LibraryItemID != "item-2") {
			t.Errorf("%s items: %s", wantID, raw)
		}
		if i == 1 && !strings.Contains(string(raw), `"items":[]`) {
			t.Errorf("removed playlist must show an empty list: %s", raw)
		}
	}
}
