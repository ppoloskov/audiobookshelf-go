package list

import (
	"slices"
	"testing"

	"audiobookshelf-compat/internal/auth"
	"audiobookshelf-compat/internal/db"
)

// seedMixed adds a podcast episode, an explicit book, a second library, and
// the playlists and collections the read tests share.
func (r *rig) seedMixed() {
	r.t.Helper()
	r.addBook("item-denied", "book-denied", "Explicit Shelf Book", true)
	r.exec(`INSERT INTO libraries(id,name,displayOrder,icon,mediaType,provider,settings,extraData,createdAt,updatedAt) VALUES('library-2','Other Library',2,'books','book','google','{}','{}',?,?)`, stamp, stamp)
	r.exec(`INSERT INTO podcasts (id,title,explicit,autoDownloadEpisodes,tags,genres,numEpisodes,createdAt,updatedAt) VALUES ('podcast-1','Show',0,0,'[]','[]',1,?,?)`, stamp, stamp)
	r.exec(`INSERT INTO libraryItems (id,path,relPath,mediaId,mediaType,isFile,isMissing,isInvalid,size,libraryFiles,extraData,createdAt,updatedAt,libraryId) VALUES ('item-podcast','/p','p','podcast-1','podcast',0,0,0,5,'[]','{}',?,?,'library-1')`, stamp, stamp)
	r.exec(`INSERT INTO podcastEpisodes (id,podcastId,title,audioFile,chapters,extraData,createdAt,updatedAt) VALUES ('episode-1','podcast-1','Pilot Episode','{"ino":"e1","duration":3,"metadata":{"filename":"e.mp3","size":7}}','[]','{}',?,?)`, stamp, stamp)

	r.exec(`INSERT INTO playlists(id,name,libraryId,userId,createdAt,updatedAt) VALUES('list-read','Mixed Queue','library-1','owner',?,?),('other-playlist','Other Queue','library-1','other',?,?),('other-library-playlist','Other Library Queue','library-2','owner',?,?)`, stamp, stamp, stamp, stamp, stamp, stamp)
	r.exec(`INSERT INTO playlistMediaItems(id,mediaItemId,mediaItemType,"order",createdAt,playlistId) VALUES('pe-book','book-1','book',1,?,'list-read'),('pe-episode','episode-1','podcastEpisode',2,?,'list-read')`, stamp, stamp)

	r.collection("collection-read", "book-2", "book-1", "book-denied")
	r.collection("collection-hidden", "book-denied")
	r.exec(`INSERT INTO collections(id,name,libraryId,createdAt,updatedAt) VALUES('collection-other-library','Other Library Collection','library-2',?,?)`, stamp, stamp)
}

func hasID(values []any, want string) bool {
	for _, value := range values {
		if value.(map[string]any)["id"] == want {
			return true
		}
	}
	return false
}

func userWith(id string, p db.Permissions) *auth.Principal {
	return auth.NewPrincipal(db.User{ID: id, Type: db.Ptr("user"), IsActive: true, Permissions: db.JSON[db.Permissions]{V: p}})
}

func TestPortedBatchMutationResponses(t *testing.T) {
	t.Run("playlist add", func(t *testing.T) {
		r := newRig(t)
		r.seedMixed()
		w := r.do("owner", "POST", "/api/playlists/list-read/batch/add", `{"items":[{"libraryItemId":"item-2"}]}`)
		if w.Code != 200 {
			t.Fatalf("status %d %s", w.Code, w.Body)
		}
		body := decode(t, w)
		for _, field := range []string{"id", "name", "libraryId", "userId", "description", "lastUpdate", "createdAt", "items"} {
			if _, has := body[field]; !has {
				t.Errorf("response lacks %q: %s", field, w.Body)
			}
		}
		if body["id"] != "list-read" || body["name"] != "Mixed Queue" || body["libraryId"] != "library-1" || body["userId"] != "owner" || body["description"] != nil {
			t.Errorf("identity fields = %v", body)
		}
		const wantTime = float64(1704164645000) // stamp as epoch milliseconds
		if body["lastUpdate"] != wantTime || body["createdAt"] != wantTime {
			t.Errorf("timestamps = %v/%v, want %v", body["lastUpdate"], body["createdAt"], wantTime)
		}
		items := body["items"].([]any)
		if len(items) != 3 {
			t.Fatalf("items = %v, want three", items)
		}
		for i, want := range []string{"item-1", "item-podcast", "item-2"} {
			entry := items[i].(map[string]any)
			if entry["libraryItemId"] != want || entry["libraryItem"].(map[string]any)["id"] != want {
				t.Errorf("item %d = %v, want %s", i, entry, want)
			}
		}
		if episode := items[1].(map[string]any); episode["episodeId"] != "episode-1" || episode["episode"].(map[string]any)["title"] != "Pilot Episode" {
			t.Errorf("episode entry = %v", episode)
		}
		media := items[2].(map[string]any)["libraryItem"].(map[string]any)["media"].(map[string]any)
		if media["id"] != "book-2" || media["metadata"].(map[string]any)["title"] != "Second" {
			t.Errorf("book media = %v", media)
		}
	})

	t.Run("collection add", func(t *testing.T) {
		r := newRig(t)
		r.seedMixed()
		w := r.doAs(principal("owner", true, false, false), "POST", "/api/collections/collection-read/batch/add", `{"books":["item-2"]}`)
		if w.Code != 200 {
			t.Fatalf("status %d %s", w.Code, w.Body)
		}
		body := decode(t, w)
		for _, field := range []string{"id", "libraryId", "name", "description", "books", "lastUpdate", "createdAt"} {
			if _, has := body[field]; !has {
				t.Errorf("response lacks %q: %s", field, w.Body)
			}
		}
		if body["id"] != "collection-read" || body["libraryId"] != "library-1" || body["name"] != "Shelf" || body["description"] != nil {
			t.Errorf("identity fields = %v", body)
		}
		const wantTime = float64(1704164645000)
		if body["lastUpdate"] != wantTime || body["createdAt"] != wantTime {
			t.Errorf("timestamps = %v/%v, want %v", body["lastUpdate"], body["createdAt"], wantTime)
		}
		books := body["books"].([]any)
		if len(books) != 2 {
			t.Fatalf("books = %v, want the two readable ones", books)
		}
		for i, want := range []string{"item-2", "item-1"} {
			item := books[i].(map[string]any)
			if item["id"] != want {
				t.Errorf("book %d = %v, want %s", i, item["id"], want)
			}
		}
	})
}

func TestPortedWritesPersistAndEmit(t *testing.T) {
	t.Run("playlist", func(t *testing.T) {
		r := newRig(t)
		r.seedMixed()
		mediaOrder := func() []string {
			var ids []string
			if err := r.db.Select(t.Context(), &ids, `SELECT mediaItemId FROM playlistMediaItems WHERE playlistId='list-read' ORDER BY "order"`); err != nil {
				t.Fatal(err)
			}
			return ids
		}
		request := func(method, path, body string) {
			t.Helper()
			if w := r.do("owner", method, path, body); w.Code != 200 {
				t.Fatalf("%s %s: %d %s", method, path, w.Code, w.Body)
			}
		}
		request("PATCH", "/api/playlists/list-read", `{"name":"Updated Queue","description":"Notes","items":[{"libraryItemId":"item-podcast","episodeId":"episode-1"},{"libraryItemId":"item-1"}]}`)
		var row struct{ Name, Description string }
		if err := r.db.Get(t.Context(), &row, `SELECT name AS Name, description AS Description FROM playlists WHERE id='list-read'`); err != nil || row.Name != "Updated Queue" || row.Description != "Notes" {
			t.Fatalf("persisted metadata = %+v, err %v", row, err)
		}
		if got := mediaOrder(); !slices.Equal(got, []string{"episode-1", "book-1"}) {
			t.Fatalf("order = %v", got)
		}
		request("POST", "/api/playlists/list-read/batch/add", `{"items":[{"libraryItemId":"item-2"}]}`)
		if got := mediaOrder(); !slices.Equal(got, []string{"episode-1", "book-1", "book-2"}) {
			t.Fatalf("order after add = %v", got)
		}
		request("DELETE", "/api/playlists/list-read/item/item-2", "")
		if got := mediaOrder(); !slices.Equal(got, []string{"episode-1", "book-1"}) {
			t.Fatalf("order after remove = %v", got)
		}
		request("DELETE", "/api/playlists/list-read", "")
		if r.count(`SELECT COUNT(*) FROM playlists WHERE id='list-read'`) != 0 || r.count(`SELECT COUNT(*) FROM playlistMediaItems WHERE playlistId='list-read'`) != 0 {
			t.Fatal("deleted playlist remains")
		}
		var events, audiences []string
		for _, ev := range r.take() {
			events = append(events, ev.event)
			audiences = append(audiences, ev.audience)
		}
		if !slices.Equal(events, []string{"playlist_updated", "playlist_updated", "playlist_updated", "playlist_removed"}) ||
			!slices.Equal(audiences, []string{"owner", "owner", "owner", "owner"}) {
			t.Fatalf("events %v audiences %v", events, audiences)
		}
	})

	t.Run("collection", func(t *testing.T) {
		r := newRig(t)
		r.seedMixed()
		if w := r.do("owner", "PATCH", "/api/collections/collection-read", `{"name":"Updated Reading","description":"Notes"}`); w.Code != 200 {
			t.Fatalf("patch: %d %s", w.Code, w.Body)
		}
		var row struct{ Name, Description string }
		if err := r.db.Get(t.Context(), &row, `SELECT name AS Name, description AS Description FROM collections WHERE id='collection-read'`); err != nil || row.Name != "Updated Reading" || row.Description != "Notes" {
			t.Fatalf("persisted metadata = %+v, err %v", row, err)
		}
		if w := r.do("owner", "DELETE", "/api/collections/collection-read/book/item-2", ""); w.Code != 200 {
			t.Fatalf("remove: %d %s", w.Code, w.Body)
		}
		var rows []struct {
			BookID string `db:"bookId"`
			Order  int    `db:"order"`
		}
		if err := r.db.Select(t.Context(), &rows, `SELECT bookId, "order" FROM collectionBooks WHERE collectionId='collection-read' ORDER BY "order"`); err != nil {
			t.Fatal(err)
		}
		if len(rows) != 2 || rows[0].BookID != "book-1" || rows[0].Order != 1 || rows[1].BookID != "book-denied" || rows[1].Order != 2 {
			t.Fatalf("entries = %+v", rows)
		}
		var events, audiences []string
		for _, ev := range r.take() {
			events = append(events, ev.event)
			audiences = append(audiences, ev.audience)
		}
		if !slices.Equal(events, []string{"collection_updated", "collection_updated"}) || !slices.Equal(audiences, []string{"*", "*"}) {
			t.Fatalf("events %v audiences %v", events, audiences)
		}
	})
}

func TestPortedReadValuesAndPermissions(t *testing.T) {
	r := newRig(t)
	r.seedMixed()
	get := func(user *auth.Principal, path string, status int) map[string]any {
		t.Helper()
		w := r.doAs(user, "GET", path, "")
		if w.Code != status {
			t.Fatalf("GET %s: %d %s, want %d", path, w.Code, w.Body, status)
		}
		if status != 200 {
			return nil
		}
		return decode(t, w)
	}
	all := db.Permissions{AccessAllLibraries: true, AccessAllTags: true, AccessExplicitContent: false}
	owner := userWith("owner", all)
	other := userWith("other", db.Permissions{AccessAllLibraries: true, AccessAllTags: true, AccessExplicitContent: true})
	noLibrary := userWith("owner", db.Permissions{AccessAllTags: true})
	partial := userWith("owner", db.Permissions{AccessAllTags: true, AccessExplicitContent: true, LibrariesAccessible: []string{"library-1"}})

	playlist := get(owner, "/api/playlists/list-read", 200)
	if playlist["id"] != "list-read" || playlist["name"] != "Mixed Queue" || playlist["userId"] != "owner" {
		t.Fatalf("playlist = %v", playlist)
	}
	if d, has := playlist["description"]; !has || d != nil {
		t.Errorf("description = %v present %t, want explicit null", d, has)
	}
	entries := playlist["items"].([]any)
	if len(entries) != 2 {
		t.Fatalf("items = %v", entries)
	}
	book := entries[0].(map[string]any)
	bookMedia := book["libraryItem"].(map[string]any)["media"].(map[string]any)
	if book["libraryItemId"] != "item-1" || bookMedia["id"] != "book-1" || bookMedia["metadata"].(map[string]any)["title"] != "First" {
		t.Errorf("book entry = %v", book)
	}
	episode := entries[1].(map[string]any)
	parent := episode["libraryItem"].(map[string]any)
	if episode["episodeId"] != "episode-1" || episode["libraryItemId"] != "item-podcast" || episode["episode"].(map[string]any)["title"] != "Pilot Episode" || parent["id"] != "item-podcast" {
		t.Errorf("episode entry = %v", episode)
	}
	if _, has := parent["media"].(map[string]any)["episodes"]; has {
		t.Errorf("minified parent has episodes")
	}

	get(other, "/api/playlists/list-read", 403)
	otherList := get(other, "/api/playlists", 200)["playlists"].([]any)
	if !hasID(otherList, "other-playlist") || hasID(otherList, "list-read") {
		t.Errorf("other user's playlists = %v", otherList)
	}
	get(noLibrary, "/api/playlists/list-read", 404)
	if got := get(noLibrary, "/api/playlists", 200)["playlists"].([]any); len(got) != 0 {
		t.Errorf("no-library playlists = %v", got)
	}
	partialLists := get(partial, "/api/playlists", 200)["playlists"].([]any)
	if !hasID(partialLists, "list-read") || hasID(partialLists, "other-library-playlist") {
		t.Errorf("partial-library playlists = %v", partialLists)
	}
	get(noLibrary, "/api/libraries/library-1/playlists", 403)
	libPlaylists := get(owner, "/api/libraries/library-1/playlists", 200)["results"].([]any)
	if !hasID(libPlaylists, "list-read") || hasID(libPlaylists, "other-playlist") {
		t.Errorf("library playlists = %v", libPlaylists)
	}
	lib2 := get(owner, "/api/libraries/library-2/playlists", 200)["results"].([]any)
	if len(lib2) != 1 || !hasID(lib2, "other-library-playlist") {
		t.Errorf("library-2 playlists = %v", lib2)
	}

	collection := get(owner, "/api/collections/collection-read", 200)
	if collection["id"] != "collection-read" || collection["libraryId"] != "library-1" {
		t.Fatalf("collection = %v", collection)
	}
	if d, has := collection["description"]; !has || d != nil {
		t.Errorf("collection description = %v present %t, want explicit null", d, has)
	}
	books := collection["books"].([]any)
	if len(books) != 2 {
		t.Fatalf("books = %v, want two readable", books)
	}
	for i, want := range [][3]string{{"item-2", "book-2", "Second"}, {"item-1", "book-1", "First"}} {
		item := books[i].(map[string]any)
		media := item["media"].(map[string]any)
		if item["id"] != want[0] || media["id"] != want[1] || media["metadata"].(map[string]any)["title"] != want[2] {
			t.Errorf("book %d = %v, want %v", i, item, want)
		}
	}
	// A same-library collection whose only book fails the item ACL stays readable.
	if got := get(owner, "/api/collections/collection-hidden", 200)["books"].([]any); len(got) != 0 {
		t.Errorf("all-denied collection books = %v, want empty", got)
	}
	get(other, "/api/collections/collection-read", 200)
	get(noLibrary, "/api/collections/collection-read", 404)
	get(noLibrary, "/api/collections/collection-other-library", 404)
	ownerCollections := get(owner, "/api/collections", 200)["collections"].([]any)
	for _, id := range []string{"collection-read", "collection-hidden", "collection-other-library"} {
		if !hasID(ownerCollections, id) {
			t.Errorf("owner collections lack %s", id)
		}
	}
	if got := get(noLibrary, "/api/collections", 200)["collections"].([]any); len(got) != 0 {
		t.Errorf("no-library collections = %v", got)
	}
	partialCollections := get(partial, "/api/collections", 200)["collections"].([]any)
	if !hasID(partialCollections, "collection-read") || !hasID(partialCollections, "collection-hidden") || hasID(partialCollections, "collection-other-library") {
		t.Errorf("partial collections = %v", partialCollections)
	}
	get(noLibrary, "/api/libraries/library-1/collections", 403)
	libCollections := get(owner, "/api/libraries/library-1/collections", 200)["results"].([]any)
	if !hasID(libCollections, "collection-read") || !hasID(libCollections, "collection-hidden") || hasID(libCollections, "collection-other-library") {
		t.Errorf("library collections = %v", libCollections)
	}
}

func TestPortedCreatePlaylistFromCollection(t *testing.T) {
	r := newRig(t)
	r.collection("c1", "book-2", "book-1")
	w := r.do("owner", "POST", "/api/playlists/collection/c1", `{}`)
	if w.Code != 200 {
		t.Fatalf("status %d %s", w.Code, w.Body)
	}
	body := decode(t, w)
	items := body["items"].([]any)
	if len(items) != 2 {
		t.Fatalf("items = %v, want both books", items)
	}
	for i, want := range []string{"item-2", "item-1"} {
		if items[i].(map[string]any)["libraryItemId"] != want {
			t.Errorf("item %d = %v, want %s", i, items[i], want)
		}
	}
	var rows []struct {
		MediaItemID string `db:"mediaItemId"`
		Order       int    `db:"order"`
	}
	if err := r.db.Select(t.Context(), &rows, `SELECT mediaItemId, "order" FROM playlistMediaItems WHERE playlistId=? ORDER BY "order"`, body["id"]); err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 || rows[0].MediaItemID != "book-2" || rows[0].Order != 1 || rows[1].MediaItemID != "book-1" || rows[1].Order != 2 {
		t.Errorf("persisted = %+v", rows)
	}
	ev := r.take()
	if len(ev) != 1 || ev[0].event != "playlist_added" || ev[0].audience != "owner" {
		t.Fatalf("events %+v", ev)
	}
}

// The expected order is the output of
// node -e 'console.log(names.sort((a, b) => a.localeCompare(b)))' (Node 22, ICU 78).
func TestPortedPlaylistListsSortByLocaleCompare(t *testing.T) {
	names := []string{"b", "B", "a", "A", "Á", "á", "10", "9", "Zoe", "zoe", "Éclair", "eclair", "_x", "-x", "Commute"}
	want := []string{"_x", "-x", "10", "9", "a", "A", "á", "Á", "b", "B", "Commute", "eclair", "Éclair", "zoe", "Zoe"}
	r := newRig(t)
	for i, name := range names {
		r.exec(`INSERT INTO playlists(id,name,libraryId,userId,createdAt,updatedAt) VALUES(?,?,'library-1','owner',?,?)`, "p"+string(rune('a'+i)), name, stamp, stamp)
	}
	for _, tc := range []struct{ path, key string }{
		{"/api/playlists", "playlists"},
		{"/api/libraries/library-1/playlists", "results"},
	} {
		w := r.do("owner", "GET", tc.path, "")
		if w.Code != 200 {
			t.Fatalf("GET %s: %d %s", tc.path, w.Code, w.Body)
		}
		var got []string
		for _, p := range decode(t, w)[tc.key].([]any) {
			got = append(got, p.(map[string]any)["name"].(string))
		}
		if !slices.Equal(got, want) {
			t.Errorf("GET %s order = %q, want %q", tc.path, got, want)
		}
	}
}

func TestPortedErrorStatuses(t *testing.T) {
	r := newRig(t)
	for path, want := range map[string]string{"/api/collections/missing": "Collection not found", "/api/playlists/missing": "Playlist not found"} {
		w := r.do("owner", "GET", path, "")
		if w.Code != 404 || w.Body.String() != want {
			t.Errorf("GET %s: %d %q, want 404 %q", path, w.Code, w.Body, want)
		}
	}
	r.playlist("p1")
	if w := r.do("owner", "POST", "/api/playlists/p1/batch/add", `{"items":[{"libraryItemId":"missing"}]}`); w.Code != 400 {
		t.Errorf("batch add of a missing library item: %d %s, want 400", w.Code, w.Body)
	}
	if w := r.do("owner", "POST", "/api/collections", `{"name":"","libraryId":"library-1","books":["item-1"]}`); w.Code != 400 {
		t.Errorf("empty collection name: %d %s, want 400", w.Code, w.Body)
	}
	for _, path := range []string{"/api/playlists/p1/add", "/api/playlists/p1/batch/item"} {
		if w := r.do("owner", "POST", path, `{"items":[]}`); w.Code != 404 {
			t.Errorf("POST %s: %d, want 404 (unknown route)", path, w.Code)
		}
	}
}
