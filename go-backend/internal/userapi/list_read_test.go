package userapi

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"audiobookshelf-compat/internal/store"
)

func TestListReadValuesAndPermissions(t *testing.T) {
	db, h, owner := playlistOrderFixture(t)
	seedListReadValues(t, db)

	playlist := getListReadJSON(t, h, owner, "/api/playlists/list-read", http.StatusOK)
	if playlist["id"] != "list-read" || playlist["name"] != "Mixed Queue" || playlist["userId"] != owner.ID {
		t.Fatalf("playlist identity=%#v", playlist)
	}
	if description, exists := playlist["description"]; !exists || description != nil {
		t.Fatalf("playlist description=%#v, present=%t; want explicit null", description, exists)
	}
	entries, ok := playlist["items"].([]any)
	if !ok || len(entries) != 2 {
		t.Fatalf("playlist items=%#v, want two ordered entries", playlist["items"])
	}
	bookEntry := entries[0].(map[string]any)
	bookItem := bookEntry["libraryItem"].(map[string]any)
	bookMedia := bookItem["media"].(map[string]any)
	bookMetadata := bookMedia["metadata"].(map[string]any)
	if bookEntry["libraryItemId"] != "item-book-first" || bookItem["id"] != "item-book-first" || bookMedia["id"] != "book-first" || bookMetadata["title"] != "First Shelf Book" || bookMetadata["description"] != "A shelf book." {
		t.Fatalf("first playlist entry=%#v", bookEntry)
	}
	episodeEntry := entries[1].(map[string]any)
	if episodeEntry["episodeId"] != "episode-one" || episodeEntry["libraryItemId"] != "item-podcast" {
		t.Fatalf("second playlist entry identity=%#v", episodeEntry)
	}
	episode := episodeEntry["episode"].(map[string]any)
	if episode["id"] != "episode-one" || episode["title"] != "Pilot Episode" {
		t.Fatalf("expanded episode=%#v", episode)
	}
	parent := episodeEntry["libraryItem"].(map[string]any)
	parentMedia := parent["media"].(map[string]any)
	if parent["id"] != "item-podcast" || parentMedia["id"] != "podcast-one" {
		t.Fatalf("minified podcast parent=%#v", parent)
	}
	if _, hasEpisodes := parentMedia["episodes"]; hasEpisodes {
		t.Fatalf("minified parent unexpectedly has episodes: %#v", parentMedia)
	}

	other := listReadUser(t, db, "u2", `{"accessAllLibraries":true,"accessAllTags":true,"accessExplicitContent":true}`)
	getListReadJSON(t, h, other, "/api/playlists/list-read", http.StatusForbidden)
	otherList := getListReadJSON(t, h, other, "/api/playlists", http.StatusOK)["playlists"].([]any)
	if !listReadHasID(otherList, "other-playlist") || listReadHasID(otherList, "list-read") {
		t.Fatalf("other user's playlists=%#v", otherList)
	}

	noLibrary := owner
	noLibrary.Permissions = json.RawMessage(`{"accessAllLibraries":false,"librariesAccessible":[],"accessAllTags":true,"accessExplicitContent":false}`)
	getListReadJSON(t, h, noLibrary, "/api/playlists/list-read", http.StatusNotFound)
	ownerList := getListReadJSON(t, h, noLibrary, "/api/playlists", http.StatusOK)["playlists"].([]any)
	if len(ownerList) != 0 {
		t.Fatalf("no-library user's playlist list=%#v", ownerList)
	}
	partialLibrary := owner
	partialLibrary.Permissions = json.RawMessage(`{"accessAllLibraries":false,"librariesAccessible":["lib1"],"accessAllTags":true,"accessExplicitContent":true}`)
	partialPlaylists := getListReadJSON(t, h, partialLibrary, "/api/playlists", http.StatusOK)["playlists"].([]any)
	if !listReadHasID(partialPlaylists, "list-read") || listReadHasID(partialPlaylists, "other-library-playlist") {
		t.Fatalf("partial-library playlist list=%#v", partialPlaylists)
	}
	getListReadJSON(t, h, noLibrary, "/api/libraries/lib1/playlists", http.StatusForbidden)
	libraryPlaylists := getListReadJSON(t, h, owner, "/api/libraries/lib1/playlists", http.StatusOK)["results"].([]any)
	if !listReadHasID(libraryPlaylists, "list-read") || listReadHasID(libraryPlaylists, "other-playlist") {
		t.Fatalf("library playlist results=%#v", libraryPlaylists)
	}
	otherLibraryPlaylists := getListReadJSON(t, h, owner, "/api/libraries/lib2/playlists", http.StatusOK)["results"].([]any)
	if len(otherLibraryPlaylists) != 1 || !listReadHasID(otherLibraryPlaylists, "other-library-playlist") {
		t.Fatalf("other library playlist results=%#v", otherLibraryPlaylists)
	}

	collection := getListReadJSON(t, h, owner, "/api/collections/collection-read", http.StatusOK)
	if collection["id"] != "collection-read" || collection["libraryId"] != "lib1" {
		t.Fatalf("collection identity=%#v", collection)
	}
	if description, exists := collection["description"]; !exists || description != nil {
		t.Fatalf("collection description=%#v, present=%t; want explicit null", description, exists)
	}
	books := collection["books"].([]any)
	if len(books) != 2 {
		t.Fatalf("collection books=%#v, want two accessible books", books)
	}
	for i, want := range []struct{ itemID, bookID, title string }{
		{"item-book-second", "book-second", "Second Shelf Book"},
		{"item-book-first", "book-first", "First Shelf Book"},
	} {
		item := books[i].(map[string]any)
		media := item["media"].(map[string]any)
		metadata := media["metadata"].(map[string]any)
		if item["id"] != want.itemID || media["id"] != want.bookID || metadata["title"] != want.title {
			t.Errorf("collection book %d=%#v, want %s/%s/%s", i, item, want.itemID, want.bookID, want.title)
		}
	}
	// The current collectionDTO treats an empty visible result as visible. A
	// same-library collection whose only item fails item ACL remains readable.
	hiddenMembers := getListReadJSON(t, h, owner, "/api/collections/collection-hidden", http.StatusOK)
	if got := hiddenMembers["books"].([]any); len(got) != 0 {
		t.Fatalf("all-denied same-library collection books=%#v, want empty", got)
	}
	getListReadJSON(t, h, other, "/api/collections/collection-read", http.StatusOK)
	getListReadJSON(t, h, noLibrary, "/api/collections/collection-read", http.StatusNotFound)
	ownerCollections := getListReadJSON(t, h, owner, "/api/collections", http.StatusOK)["collections"].([]any)
	if !listReadHasID(ownerCollections, "collection-read") || !listReadHasID(ownerCollections, "collection-hidden") || !listReadHasID(ownerCollections, "collection-other-library") {
		t.Fatalf("owner collection list=%#v", ownerCollections)
	}
	noLibraryCollections := getListReadJSON(t, h, noLibrary, "/api/collections", http.StatusOK)["collections"].([]any)
	if len(noLibraryCollections) != 0 {
		t.Fatalf("no-library collection list=%#v, want empty", noLibraryCollections)
	}
	partialCollections := getListReadJSON(t, h, partialLibrary, "/api/collections", http.StatusOK)["collections"].([]any)
	if !listReadHasID(partialCollections, "collection-read") || !listReadHasID(partialCollections, "collection-hidden") || listReadHasID(partialCollections, "collection-other-library") {
		t.Fatalf("partial-library collection list=%#v", partialCollections)
	}
	getListReadJSON(t, h, noLibrary, "/api/collections/collection-other-library", http.StatusNotFound)
	getListReadJSON(t, h, noLibrary, "/api/libraries/lib1/collections", http.StatusForbidden)
	libraryCollections := getListReadJSON(t, h, owner, "/api/libraries/lib1/collections", http.StatusOK)["results"].([]any)
	if !listReadHasID(libraryCollections, "collection-read") || !listReadHasID(libraryCollections, "collection-hidden") || listReadHasID(libraryCollections, "collection-other-library") {
		t.Fatalf("library collection results=%#v", libraryCollections)
	}
}

func seedListReadValues(t *testing.T, db *sql.DB) {
	t.Helper()
	now := time.Date(2024, time.January, 2, 3, 4, 5, 0, time.UTC).Format("2006-01-02 15:04:05.000 +00:00")
	for _, q := range []string{
		`INSERT INTO users(id,username,type,token,isActive,isLocked,permissions,bookmarks,extraData,createdAt,updatedAt) VALUES('u2','other','user','tok2',1,0,'{"accessAllLibraries":true,"accessAllTags":true,"accessExplicitContent":true}','[]','{}','` + now + `','` + now + `')`,
		`INSERT INTO libraries(id,name,displayOrder,icon,mediaType,provider,settings,extraData,createdAt,updatedAt) VALUES('lib2','Other Library',1,'books','book','google','{}','{}','` + now + `','` + now + `')`,
		`INSERT INTO playlists(id,name,description,libraryId,userId,createdAt,updatedAt) VALUES('list-read','Mixed Queue',NULL,'lib1','u1','` + now + `','` + now + `')`,
		`INSERT INTO playlists(id,name,description,libraryId,userId,createdAt,updatedAt) VALUES('other-playlist','Other Queue',NULL,'lib1','u2','` + now + `','` + now + `')`,
		`INSERT INTO playlists(id,name,description,libraryId,userId,createdAt,updatedAt) VALUES('other-library-playlist','Other Library Queue',NULL,'lib2','u1','` + now + `','` + now + `')`,
		`INSERT INTO books(id,title,description,explicit,abridged,narrators,audioFiles,ebookFile,chapters,tags,genres,createdAt,updatedAt) VALUES('book-first','First Shelf Book','A shelf book.',0,0,'[]','[]','null','[]','[]','[]','` + now + `','` + now + `')`,
		`INSERT INTO books(id,title,description,explicit,abridged,narrators,audioFiles,ebookFile,chapters,tags,genres,createdAt,updatedAt) VALUES('book-second','Second Shelf Book','Second description.',0,0,'[]','[]','null','[]','[]','[]','` + now + `','` + now + `')`,
		`INSERT INTO books(id,title,description,explicit,abridged,narrators,audioFiles,ebookFile,chapters,tags,genres,createdAt,updatedAt) VALUES('book-denied','Explicit Shelf Book','Hidden from this user.',1,0,'[]','[]','null','[]','[]','[]','` + now + `','` + now + `')`,
		`INSERT INTO libraryItems(id,ino,path,relPath,mediaId,mediaType,isFile,isMissing,isInvalid,size,extraData,libraryFiles,title,libraryId,createdAt,updatedAt) VALUES('item-book-first','ino-book-first','/books/first','first','book-first','book',0,0,0,5,'{}','[]','First Shelf Book','lib1','` + now + `','` + now + `')`,
		`INSERT INTO libraryItems(id,ino,path,relPath,mediaId,mediaType,isFile,isMissing,isInvalid,size,extraData,libraryFiles,title,libraryId,createdAt,updatedAt) VALUES('item-book-second','ino-book-second','/books/second','second','book-second','book',0,0,0,6,'{}','[]','Second Shelf Book','lib1','` + now + `','` + now + `')`,
		`INSERT INTO libraryItems(id,ino,path,relPath,mediaId,mediaType,isFile,isMissing,isInvalid,size,extraData,libraryFiles,title,libraryId,createdAt,updatedAt) VALUES('item-book-denied','ino-book-denied','/books/denied','denied','book-denied','book',0,0,0,7,'{}','[]','Explicit Shelf Book','lib1','` + now + `','` + now + `')`,
		`INSERT INTO podcasts(id,title,author,releaseDate,feedURL,description,language,podcastType,explicit,autoDownloadEpisodes,tags,genres,numEpisodes,createdAt,updatedAt) VALUES('podcast-one','One Podcast','Host','2024-01-01','https://example.test/feed','Podcast description.','en','episodic',0,0,'[]','[]',1,'` + now + `','` + now + `')`,
		`INSERT INTO podcastEpisodes(id,"index",season,episode,episodeType,title,subtitle,description,pubDate,enclosureURL,enclosureSize,enclosureType,publishedAt,audioFile,chapters,extraData,podcastId,createdAt,updatedAt) VALUES('episode-one',1,'1','1','full','Pilot Episode',NULL,'Episode description.','2024-01-01','https://example.test/episode.mp3',123,'audio/mpeg','` + now + `','{"ino":"episode-audio","metadata":{"filename":"episode.mp3","ext":".mp3","path":"/podcasts/episode.mp3","relPath":"episode.mp3","size":123},"duration":30,"mimeType":"audio/mpeg"}','[]','{}','podcast-one','` + now + `','` + now + `')`,
		`INSERT INTO libraryItems(id,ino,path,relPath,mediaId,mediaType,isFile,isMissing,isInvalid,size,extraData,libraryFiles,title,libraryId,createdAt,updatedAt) VALUES('item-podcast','ino-podcast','/podcasts/one','one','podcast-one','podcast',0,0,0,123,'{}','[]','One Podcast','lib1','` + now + `','` + now + `')`,
		`INSERT INTO playlistMediaItems(id,mediaItemId,mediaItemType,"order",createdAt,playlistId) VALUES('playlist-book','book-first','book',1,'` + now + `','list-read')`,
		`INSERT INTO playlistMediaItems(id,mediaItemId,mediaItemType,"order",createdAt,playlistId) VALUES('playlist-episode','episode-one','podcastEpisode',2,'` + now + `','list-read')`,
		`INSERT INTO collections(id,name,description,createdAt,updatedAt,libraryId) VALUES('collection-read','Reading List',NULL,'` + now + `','` + now + `','lib1')`,
		`INSERT INTO collectionBooks(id,"order",createdAt,bookId,collectionId) VALUES('collection-second',1,'` + now + `','book-second','collection-read')`,
		`INSERT INTO collectionBooks(id,"order",createdAt,bookId,collectionId) VALUES('collection-first',2,'` + now + `','book-first','collection-read')`,
		`INSERT INTO collectionBooks(id,"order",createdAt,bookId,collectionId) VALUES('collection-denied',3,'` + now + `','book-denied','collection-read')`,
		`INSERT INTO collections(id,name,description,createdAt,updatedAt,libraryId) VALUES('collection-hidden','Hidden by member ACL',NULL,'` + now + `','` + now + `','lib1')`,
		`INSERT INTO collectionBooks(id,"order",createdAt,bookId,collectionId) VALUES('collection-hidden-member',1,'` + now + `','book-denied','collection-hidden')`,
		`INSERT INTO collections(id,name,description,createdAt,updatedAt,libraryId) VALUES('collection-other-library','Other Library Collection',NULL,'` + now + `','` + now + `','lib2')`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatalf("list fixture: %v\n%s", err, q)
		}
	}
}

func listReadUser(t *testing.T, db *sql.DB, id, permissions string) store.User {
	t.Helper()
	var username string
	if err := db.QueryRow(`SELECT username FROM users WHERE id=?`, id).Scan(&username); err != nil {
		t.Fatal(err)
	}
	return store.User{ID: id, Username: username, Type: "user", IsActive: true, Permissions: json.RawMessage(permissions), Bookmarks: json.RawMessage(`[]`)}
}

func getListReadJSON(t *testing.T, h *userAPIForTest, user store.User, path string, status int) map[string]any {
	t.Helper()
	w := httptest.NewRecorder()
	if !h.Handle(w, httptest.NewRequest(http.MethodGet, path, nil), user) || w.Code != status {
		t.Fatalf("GET %s status=%d body=%s, want %d", path, w.Code, w.Body.String(), status)
	}
	if status == http.StatusNotFound || status == http.StatusForbidden {
		return nil
	}
	var payload map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode GET %s: %v body=%s", path, err, w.Body.String())
	}
	return payload
}

func listReadHasID(values []any, want string) bool {
	for _, value := range values {
		if value.(map[string]any)["id"] == want {
			return true
		}
	}
	return false
}
