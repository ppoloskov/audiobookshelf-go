package userapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"audiobookshelf-compat/internal/catalog"
	"audiobookshelf-compat/internal/config"
)

func TestCreatePlaylistFromCollectionPersistsExpandedBookItems(t *testing.T) {
	db, user := userFixture(t)
	defer db.Close()
	// The catalog reader uses its normal production queries and returns a
	// wire.BookExpanded inside ItemJSON's media field.
	stmts := []string{
		`ALTER TABLE books ADD COLUMN title TEXT`,
		`ALTER TABLE books ADD COLUMN subtitle TEXT`,
		`ALTER TABLE books ADD COLUMN publishedYear TEXT`,
		`ALTER TABLE books ADD COLUMN publishedDate TEXT`,
		`ALTER TABLE books ADD COLUMN publisher TEXT`,
		`ALTER TABLE books ADD COLUMN description TEXT`,
		`ALTER TABLE books ADD COLUMN isbn TEXT`,
		`ALTER TABLE books ADD COLUMN asin TEXT`,
		`ALTER TABLE books ADD COLUMN language TEXT`,
		`ALTER TABLE books ADD COLUMN abridged INTEGER`,
		`ALTER TABLE books ADD COLUMN coverPath TEXT`,
		`ALTER TABLE books ADD COLUMN duration REAL`,
		`ALTER TABLE books ADD COLUMN narrators TEXT`,
		`ALTER TABLE books ADD COLUMN audioFiles TEXT`,
		`ALTER TABLE books ADD COLUMN ebookFile TEXT`,
		`ALTER TABLE books ADD COLUMN chapters TEXT`,
		`ALTER TABLE books ADD COLUMN genres TEXT`,
		`CREATE TABLE libraryFolders (id TEXT PRIMARY KEY,path TEXT,libraryId TEXT,createdAt DATETIME,updatedAt DATETIME)`,
		`CREATE TABLE authors (id TEXT PRIMARY KEY,name TEXT,lastFirst TEXT,libraryId TEXT,createdAt DATETIME,updatedAt DATETIME)`,
		`CREATE TABLE series (id TEXT PRIMARY KEY,name TEXT,nameIgnorePrefix TEXT,libraryId TEXT,createdAt DATETIME,updatedAt DATETIME)`,
		`CREATE TABLE bookAuthors (id TEXT PRIMARY KEY,bookId TEXT,authorId TEXT,createdAt DATETIME)`,
		`CREATE TABLE bookSeries (id TEXT PRIMARY KEY,sequence TEXT,bookId TEXT,seriesId TEXT,createdAt DATETIME)`,
		`CREATE TABLE collections (id TEXT PRIMARY KEY,libraryId TEXT,name TEXT,description TEXT,createdAt DATETIME,updatedAt DATETIME)`,
		`CREATE TABLE collectionBooks (id TEXT PRIMARY KEY,collectionId TEXT,bookId TEXT,"order" INTEGER,createdAt DATETIME)`,
		`INSERT INTO libraries(id) VALUES ('lib1')`,
		`INSERT INTO libraryFolders(id,path,libraryId) VALUES ('f1','/books','lib1')`,
		`INSERT INTO books(id,title,explicit,abridged,duration,narrators,audioFiles,ebookFile,chapters,tags,genres) VALUES ('b1','First',0,0,10,'[]','[]','null','[]','[]','[]')`,
		`INSERT INTO books(id,title,explicit,abridged,duration,narrators,audioFiles,ebookFile,chapters,tags,genres) VALUES ('b2','Second',0,0,20,'[]','[]','null','[]','[]','[]')`,
		`INSERT INTO libraryItems(id,mediaId,mediaType,libraryId,libraryFolderId,path,relPath,isFile,mtime,ctime,birthtime,createdAt,updatedAt,lastScan,lastScanVersion,isMissing,isInvalid,size,extraData,libraryFiles,ino) VALUES ('li1','b1','book','lib1','f1','/books/1','1',0,1,1,1,1,1,NULL,NULL,0,0,1,'{}','[]','ino1'),('li2','b2','book','lib1','f1','/books/2','2',0,1,1,1,1,1,NULL,NULL,0,0,1,'{}','[]','ino2')`,
		`INSERT INTO collections(id,libraryId,name,description,createdAt,updatedAt) VALUES ('c1','lib1','Queue',NULL,1,1)`,
		`INSERT INTO collectionBooks(id,collectionId,bookId,"order") VALUES ('cb1','c1','b2',1),('cb2','c1','b1',2)`,
	}
	for _, stmt := range stmts {
		if _, err := db.Exec(stmt); err != nil {
			t.Fatalf("fixture: %v\n%s", err, stmt)
		}
	}
	var eventUser, eventName string
	var eventPayload map[string]any
	h, err := New(db, config.Config{}, func(userID, event string, payload any) {
		eventUser, eventName = userID, event
		eventPayload, _ = payload.(map[string]any)
	}, catalog.New(db, config.Config{}, nil))
	if err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/api/playlists/collection/c1", strings.NewReader(`{}`))
	if !h.Handle(w, r, user) || w.Code != http.StatusOK {
		t.Fatalf("POST status=%d body=%s", w.Code, w.Body.String())
	}
	var response map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	items, ok := response["items"].([]any)
	if !ok || len(items) != 2 {
		t.Fatalf("response items = %#v; want both collection books", response["items"])
	}
	for i, want := range []string{"li2", "li1"} {
		entry := items[i].(map[string]any)
		if entry["libraryItemId"] != want {
			t.Errorf("response item %d libraryItemId=%v, want %s", i, entry["libraryItemId"], want)
		}
	}
	rows, err := db.Query(`SELECT mediaItemId,"order" FROM playlistMediaItems WHERE playlistId=? ORDER BY "order"`, response["id"])
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for i, want := range []string{"b2", "b1"} {
		var id string
		var order int
		if !rows.Next() {
			t.Fatalf("persisted item %d missing", i)
		}
		if err := rows.Scan(&id, &order); err != nil {
			t.Fatal(err)
		}
		if id != want || order != i+1 {
			t.Errorf("persisted item %d=(%s,%d), want (%s,%d)", i, id, order, want, i+1)
		}
	}
	if rows.Next() {
		t.Error("unexpected extra persisted playlist item")
	}
	if eventUser != user.ID || eventName != "playlist_added" || eventPayload == nil {
		t.Fatalf("event=(%q,%q,%#v), want user playlist_added payload", eventUser, eventName, eventPayload)
	}
	eventItems, ok := eventPayload["items"].([]any)
	if !ok || len(eventItems) != 2 {
		t.Fatalf("event items=%#v, want two", eventPayload["items"])
	}
}
