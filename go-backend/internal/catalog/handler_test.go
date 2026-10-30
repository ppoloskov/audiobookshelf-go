package catalog

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"audiobookshelf-compat/internal/config"
	"audiobookshelf-compat/internal/store"
	_ "modernc.org/sqlite"
)

const catalogSchema = `
CREATE TABLE libraries (id TEXT PRIMARY KEY,name TEXT,displayOrder INTEGER,icon TEXT,mediaType TEXT,provider TEXT,lastScan DATETIME,lastScanVersion TEXT,settings JSON,extraData JSON,createdAt DATETIME,updatedAt DATETIME);
CREATE TABLE libraryFolders (id TEXT PRIMARY KEY,path TEXT,libraryId TEXT,createdAt DATETIME,updatedAt DATETIME);
CREATE TABLE books (id TEXT PRIMARY KEY,title TEXT,titleIgnorePrefix TEXT,subtitle TEXT,publishedYear TEXT,publishedDate TEXT,publisher TEXT,description TEXT,isbn TEXT,asin TEXT,language TEXT,explicit BOOLEAN,abridged BOOLEAN,coverPath TEXT,duration FLOAT,narrators JSON,audioFiles JSON,ebookFile JSON,chapters JSON,tags JSON,genres JSON,createdAt DATETIME,updatedAt DATETIME);
CREATE TABLE libraryItems (id TEXT PRIMARY KEY,ino TEXT,path TEXT,relPath TEXT,mediaId TEXT,mediaType TEXT,isFile BOOLEAN,isMissing BOOLEAN,isInvalid BOOLEAN,mtime DATETIME,ctime DATETIME,birthtime DATETIME,size BIGINT,lastScan DATETIME,lastScanVersion TEXT,libraryFiles JSON,extraData JSON,title TEXT,titleIgnorePrefix TEXT,authorNamesFirstLast TEXT,authorNamesLastFirst TEXT,libraryId TEXT,libraryFolderId TEXT,createdAt DATETIME,updatedAt DATETIME);
CREATE TABLE authors (id TEXT PRIMARY KEY,name TEXT,lastFirst TEXT,asin TEXT,description TEXT,imagePath TEXT,libraryId TEXT,createdAt DATETIME,updatedAt DATETIME);
CREATE TABLE series (id TEXT PRIMARY KEY,name TEXT,nameIgnorePrefix TEXT,description TEXT,libraryId TEXT,createdAt DATETIME,updatedAt DATETIME);
CREATE TABLE bookAuthors (id TEXT PRIMARY KEY,bookId TEXT,authorId TEXT,createdAt DATETIME);
CREATE TABLE bookSeries (id TEXT PRIMARY KEY,sequence TEXT,bookId TEXT,seriesId TEXT,createdAt DATETIME);
`

func catalogFixture(t *testing.T) *sql.DB {
	t.Helper()
	path := filepath.Join(t.TempDir(), "catalog.sqlite")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(catalogSchema); err != nil {
		t.Fatal(err)
	}
	fixtures := []string{
		`INSERT INTO libraries VALUES ('l1','Shelf',1,'books','book','google',NULL,NULL,'{}','{}','2024-01-01T00:00:00Z','2024-01-02T00:00:00Z')`,
		`INSERT INTO libraries VALUES ('l2','Podcast Shelf',2,'podcast','podcast','podcastIndex',NULL,NULL,'{}','{}','2024-01-01T00:00:00Z','2024-01-02T00:00:00Z')`,
		`INSERT INTO libraryFolders VALUES ('f1','/media/books','l1','2024-01-01T00:00:00Z','2024-01-01T00:00:00Z')`,
		`INSERT INTO authors VALUES ('a1','Ada Author','Author, Ada',NULL,NULL,NULL,'l1','2024-01-01T00:00:00Z','2024-01-01T00:00:00Z')`,
		`INSERT INTO series VALUES ('s1','First Series','First Series',NULL,'l1','2024-01-01T00:00:00Z','2024-01-01T00:00:00Z')`,
		`INSERT INTO bookAuthors VALUES ('ba1','b1','a1','2024-01-01T00:00:00Z')`,
		`INSERT INTO bookSeries VALUES ('bs1','1','b1','s1','2024-01-01T00:00:00Z')`,
		`INSERT INTO books VALUES ('b1','The Book',NULL,NULL,'2024',NULL,NULL,'<p>A story</p>',NULL,NULL,'en',0,0,'/metadata/covers/b1.jpg',12.5,'["Reader"]','[{"ino":"audio-1","metadata":{"filename":"01.mp3","ext":".mp3","path":"/media/books/01.mp3","relPath":"01.mp3","size":100,"mtimeMs":1704067200000,"ctimeMs":1704067200000,"birthtimeMs":1704067200000},"duration":12.5,"mimeType":"audio/mpeg","exclude":false}]','null','[{"id":0,"start":0,"end":12.5,"title":"Chapter 1"}]','["tag"]','["fiction"]','2024-01-01T00:00:00Z','2024-01-02T00:00:00Z')`,
		`INSERT INTO libraryItems VALUES ('i1','ino-1','/media/books/The Book','The Book','b1','book',0,0,0,'2024-01-01T00:00:00Z','2024-01-01T00:00:00Z','2024-01-01T00:00:00Z',100,NULL,NULL,'[]','{}','The Book','Book, The','Ada Author','Author, Ada','l1','f1','2024-01-01T00:00:00Z','2024-01-02T00:00:00Z')`,
	}
	for _, q := range fixtures {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	return db
}

func TestHandleLibraryItemsAndItemDetail(t *testing.T) {
	db := catalogFixture(t)
	defer db.Close()
	h := New(db, config.Config{}, nil)
	u := store.User{ID: "u1", Permissions: json.RawMessage(`{"accessAllLibraries":true,"accessAllTags":true,"accessExplicitContent":true}`)}
	list := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/api/libraries/l1/items", nil)
	if !h.Handle(list, req, u) {
		t.Fatal("library items route did not match")
	}
	if list.Code != 200 {
		t.Fatalf("list status=%d body=%s", list.Code, list.Body.String())
	}
	var payload struct {
		Results []map[string]any `json:"results"`
		Total   int              `json:"total"`
	}
	if err := json.Unmarshal(list.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Total != 1 || len(payload.Results) != 1 {
		t.Fatalf("payload=%s", list.Body.String())
	}
	media := payload.Results[0]["media"].(map[string]any)
	mediaKeys := make([]string, 0, len(media))
	for key := range media {
		mediaKeys = append(mediaKeys, key)
	}
	sort.Strings(mediaKeys)
	if !reflect.DeepEqual(mediaKeys, []string{"coverPath", "duration", "id", "metadata", "numAudioFiles", "numChapters", "numTracks", "size", "tags"}) {
		t.Fatalf("minified book media keys=%v", mediaKeys)
	}
	if _, ok := media["libraryItemId"]; ok {
		t.Fatal("minified book media has libraryItemId")
	}
	if _, ok := media["tracks"]; ok {
		t.Fatal("minified book media has tracks")
	}
	metadata := media["metadata"].(map[string]any)
	if _, ok := metadata["descriptionPlain"]; ok {
		t.Fatal("minified metadata has descriptionPlain")
	}
	if metadata["authorName"] != "Ada Author" || metadata["seriesName"] != "First Series #1" {
		t.Fatalf("metadata=%#v", metadata)
	}

	detail := httptest.NewRecorder()
	if !h.Handle(detail, httptest.NewRequest("GET", "/api/items/i1", nil), u) {
		t.Fatal("item route did not match")
	}
	if detail.Code != 200 {
		t.Fatalf("detail status=%d body=%s", detail.Code, detail.Body.String())
	}
	var item map[string]any
	if err := json.Unmarshal(detail.Body.Bytes(), &item); err != nil {
		t.Fatal(err)
	}
	fullMedia := item["media"].(map[string]any)
	fullMetadata := fullMedia["metadata"].(map[string]any)
	if fullMetadata["title"] != "The Book" {
		t.Fatalf("full metadata=%#v", fullMetadata)
	}
	if item["mediaType"] != "book" || item["libraryId"] != "l1" {
		t.Fatalf("item=%#v", item)
	}
	expanded := httptest.NewRecorder()
	if !h.Handle(expanded, httptest.NewRequest("GET", "/api/items/i1?expanded=1", nil), u) || expanded.Code != http.StatusOK {
		t.Fatalf("expanded status=%d body=%s", expanded.Code, expanded.Body.String())
	}
	var expandedItem map[string]any
	if err := json.Unmarshal(expanded.Body.Bytes(), &expandedItem); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"lastScan", "libraryFiles", "scanVersion"} {
		if _, ok := expandedItem[key]; !ok {
			t.Errorf("expanded item missing %q: %s", key, expanded.Body.String())
		}
	}
	expandedMedia := expandedItem["media"].(map[string]any)
	for _, key := range []string{"chapters", "audioFiles", "ebookFile", "tracks"} {
		if _, ok := expandedMedia[key]; !ok {
			t.Errorf("expanded media missing %q: %s", key, expanded.Body.String())
		}
	}
	if expandedMedia["numAudioFiles"] != float64(1) {
		t.Error("expanded media does not retain minified counters")
	}
}

func TestItemDetailEnforcesExplicitAndTagPermissions(t *testing.T) {
	db := catalogFixture(t)
	defer db.Close()
	h := New(db, config.Config{}, nil)
	if _, err := db.Exec(`UPDATE books SET explicit=1,tags='["tag-x"]' WHERE id='b1'`); err != nil {
		t.Fatal(err)
	}
	user := store.User{ID: "reader", IsActive: true, Permissions: json.RawMessage(`{"accessAllLibraries":true,"accessAllTags":false,"accessExplicitContent":false,"itemTagsSelected":["tag-a"]}`)}
	for _, tc := range []struct {
		name        string
		permissions string
		want        int
	}{
		{name: "explicit denied despite matching tag", permissions: `{"accessAllLibraries":true,"accessAllTags":false,"accessExplicitContent":false,"itemTagsSelected":["tag-x"]}`, want: http.StatusForbidden},
		{name: "non-overlapping tag denied", permissions: `{"accessAllLibraries":true,"accessAllTags":false,"accessExplicitContent":true,"itemTagsSelected":["tag-a"]}`, want: http.StatusForbidden},
		{name: "matching tag and explicit allowed", permissions: `{"accessAllLibraries":true,"accessAllTags":false,"accessExplicitContent":true,"itemTagsSelected":["tag-x"]}`, want: http.StatusOK},
	} {
		t.Run(tc.name, func(t *testing.T) {
			user.Permissions = json.RawMessage(tc.permissions)
			w := httptest.NewRecorder()
			if !h.Handle(w, httptest.NewRequest(http.MethodGet, "/api/items/i1", nil), user) {
				t.Fatal("item route did not match")
			}
			if w.Code != tc.want {
				t.Fatalf("status=%d want=%d body=%s", w.Code, tc.want, w.Body.String())
			}
		})
	}
}

func TestMissingItemUsesSendStatusNotFound(t *testing.T) {
	db := catalogFixture(t)
	defer db.Close()
	h := New(db, config.Config{}, nil)
	u := store.User{ID: "u1", Permissions: json.RawMessage(`{"accessAllLibraries":true}`)}
	for _, id := range []string{"missing", "00000000-0000-0000-0000-000000000000"} {
		w := httptest.NewRecorder()
		if !h.Handle(w, httptest.NewRequest(http.MethodGet, "/api/items/"+id, nil), u) {
			t.Fatalf("item %q route did not match", id)
		}
		const body = "Not Found"
		if w.Code != http.StatusNotFound || w.Body.String() != body || w.Header().Get("Content-Type") != "text/plain; charset=utf-8" || w.Header().Get("ETag") != expressETag([]byte(body)) {
			t.Fatalf("item %q status=%d headers=%v body=%q", id, w.Code, w.Header(), w.Body.String())
		}
	}
}

func TestDeniedItemDownloadUsesSendStatusForbidden(t *testing.T) {
	db := catalogFixture(t)
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE podcasts (id TEXT PRIMARY KEY, title TEXT)`); err != nil {
		t.Fatal(err)
	}
	h := New(db, config.Config{}, nil)
	u := store.User{ID: "u1", IsActive: true, Permissions: json.RawMessage(`{"accessAllLibraries":true,"download":false}`)}
	w := httptest.NewRecorder()
	if !h.Handle(w, httptest.NewRequest(http.MethodGet, "/api/items/i1/download", nil), u) {
		t.Fatal("download route did not match")
	}
	const body = "Forbidden"
	if w.Code != http.StatusForbidden || w.Body.String() != body || w.Header().Get("Content-Type") != "text/plain; charset=utf-8" || w.Header().Get("ETag") != expressETag([]byte(body)) {
		t.Fatalf("status=%d headers=%v body=%q", w.Code, w.Header(), w.Body.String())
	}
}

func TestLibraryJSONIncludesNullLastScanVersion(t *testing.T) {
	db := catalogFixture(t)
	defer db.Close()
	h := New(db, config.Config{}, nil)

	got, err := h.libraryJSON(t.Context(), "l1")
	if err != nil {
		t.Fatal(err)
	}
	if value, ok := got["lastScanVersion"]; !ok || value != nil {
		t.Fatalf("lastScanVersion should be present as null: %#v", got)
	}
	if _, ok := got["lastScan"]; !ok || got["lastScan"] != nil {
		t.Fatalf("lastScan must remain present as null: %#v", got)
	}
	if _, err := db.Exec(`UPDATE libraries SET lastScanVersion='2.37.2' WHERE id='l1'`); err != nil {
		t.Fatal(err)
	}
	got, err = h.libraryJSON(t.Context(), "l1")
	if err != nil {
		t.Fatal(err)
	}
	if got["lastScanVersion"] != "2.37.2" {
		t.Fatalf("lastScanVersion=%#v", got["lastScanVersion"])
	}
}

func TestLibraryItemsMaterializeBeforeNestedDTOQueries(t *testing.T) {
	db := catalogFixture(t)
	defer db.Close()
	for _, fixture := range []struct{ id, media string }{{"i2", "b2"}, {"i3", "b3"}} {
		if _, err := db.Exec(`INSERT INTO books SELECT 'b2',title,titleIgnorePrefix,subtitle,publishedYear,publishedDate,publisher,description,isbn,asin,language,explicit,abridged,coverPath,duration,narrators,audioFiles,ebookFile,chapters,tags,genres,createdAt,updatedAt FROM books WHERE id='b1'`); fixture.media == "b2" && err != nil {
			t.Fatal(err)
		}
		if fixture.media == "b3" {
			if _, err := db.Exec(`INSERT INTO books SELECT 'b3',title,titleIgnorePrefix,subtitle,publishedYear,publishedDate,publisher,description,isbn,asin,language,explicit,abridged,coverPath,duration,narrators,audioFiles,ebookFile,chapters,tags,genres,createdAt,updatedAt FROM books WHERE id='b1'`); err != nil {
				t.Fatal(err)
			}
		}
		now := time.Now().UTC().Format(time.RFC3339Nano)
		if _, err := db.Exec(`INSERT INTO libraryItems(id,ino,path,relPath,mediaId,mediaType,isFile,isMissing,isInvalid,libraryFiles,extraData,title,authorNamesFirstLast,authorNamesLastFirst,libraryId,libraryFolderId,createdAt,updatedAt) VALUES(?,?,?, ?,?,'book',0,0,0,'[]','{}','The Book','Ada Author','Author, Ada','l1','f1',?,?)`, fixture.id, fixture.id, "/media/books/"+fixture.id, fixture.id, fixture.media, now, now); err != nil {
			t.Fatal(err)
		}
	}
	h := New(db, config.Config{}, nil)
	user := store.User{ID: "u1", Permissions: json.RawMessage(`{"accessAllLibraries":true,"accessAllTags":true,"accessExplicitContent":true}`)}
	w := httptest.NewRecorder()
	if !h.Handle(w, httptest.NewRequest(http.MethodGet, "/api/libraries/l1/items", nil), user) {
		t.Fatal("library items route did not match")
	}
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	var response struct {
		Results []any `json:"results"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if len(response.Results) != 3 {
		t.Fatalf("got %d results, want 3: %s", len(response.Results), w.Body.String())
	}
}

func TestBatchGetAndChapterMutationRespectCatalogPermissions(t *testing.T) {
	db := catalogFixture(t)
	defer db.Close()
	h := New(db, config.Config{}, nil)
	u := store.User{ID: "u1", IsActive: true, Permissions: json.RawMessage(`{"accessAllLibraries":true,"accessAllTags":true,"update":true}`)}
	batch := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/api/items/batch/get", strings.NewReader(`{"libraryItemIds":["i1"]}`))
	if !h.Handle(batch, req, u) || batch.Code != http.StatusOK {
		t.Fatalf("batch route status=%d body=%s", batch.Code, batch.Body.String())
	}
	var response map[string]any
	if err := json.Unmarshal(batch.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if len(response["libraryItems"].([]any)) != 1 {
		t.Fatalf("batch result=%s", batch.Body.String())
	}
	chapters := httptest.NewRecorder()
	chapterReq := httptest.NewRequest("POST", "/api/items/i1/chapters", strings.NewReader(`{"chapters":[{"title":"Opening","start":0,"end":12.5}]}`))
	if !h.Handle(chapters, chapterReq, u) || chapters.Code != http.StatusOK || !strings.Contains(chapters.Body.String(), `"updated":true`) {
		t.Fatalf("chapter update status=%d body=%s", chapters.Code, chapters.Body.String())
	}
	var stored string
	if err := db.QueryRow(`SELECT chapters FROM books WHERE id='b1'`).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stored, `"title":"Opening"`) {
		t.Fatalf("chapters not saved: %s", stored)
	}
	denied := httptest.NewRecorder()
	deniedReq := httptest.NewRequest("POST", "/api/items/i1/chapters", strings.NewReader(`{"chapters":[]}`))
	if !h.Handle(denied, deniedReq, store.User{ID: "reader", IsActive: true, Permissions: json.RawMessage(`{"accessAllLibraries":true,"update":false}`)}) || denied.Code != http.StatusForbidden {
		t.Fatalf("chapter permission status=%d body=%s", denied.Code, denied.Body.String())
	}
}

func TestFileDownloadChecksDownloadPermission(t *testing.T) {
	db := catalogFixture(t)
	defer db.Close()
	if _, err := db.Exec(`UPDATE libraryItems SET libraryFiles=? WHERE id='i1'`, `[{"ino":"audio-1","metadata":{"filename":"01.mp3","ext":".mp3","path":"/media/books/01.mp3"}}]`); err != nil {
		t.Fatal(err)
	}
	h := New(db, config.Config{}, nil)
	u := store.User{ID: "u1", IsActive: true, Permissions: json.RawMessage(`{"accessAllLibraries":true,"download":false}`)}
	w := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/api/items/i1/file/audio-1/download", nil)
	if !h.Handle(w, req, u) || w.Code != http.StatusForbidden {
		t.Fatalf("download permission status=%d body=%s", w.Code, w.Body.String())
	}
}

func TestFileDownloadResolvesFileBeforeCheckingDownloadPermission(t *testing.T) {
	db := catalogFixture(t)
	defer db.Close()
	h := New(db, config.Config{}, nil)
	u := store.User{ID: "u1", IsActive: true, Permissions: json.RawMessage(`{"accessAllLibraries":true,"download":false}`)}
	w := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/api/items/i1/file/absent-ino/download", nil)
	if !h.Handle(w, req, u) {
		t.Fatal("file download route did not match")
	}
	if w.Code != http.StatusNotFound || w.Body.String() != "Not Found" {
		t.Fatalf("missing file status=%d body=%q", w.Code, w.Body.String())
	}
	if got := w.Header().Get("Content-Type"); got != "text/plain; charset=utf-8" {
		t.Fatalf("missing file content type=%q", got)
	}
}

func TestFileDownloadMissingDiskFileUsesSourceErrorResponse(t *testing.T) {
	db := catalogFixture(t)
	defer db.Close()
	if _, err := db.Exec(`UPDATE libraryItems SET libraryFiles=? WHERE id='i1'`, `[{"ino":"audio-1","metadata":{"filename":"01.m4b","ext":".m4b","path":"/media/books/missing.m4b"}}]`); err != nil {
		t.Fatal(err)
	}
	h := New(db, config.Config{}, nil)
	u := store.User{ID: "u1", IsActive: true, Permissions: json.RawMessage(`{"accessAllLibraries":true,"download":true}`)}
	w := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/api/items/i1/file/audio-1/download", nil)
	if !h.Handle(w, req, u) {
		t.Fatal("file download route did not match")
	}
	if w.Code != http.StatusNotFound || w.Body.String() != "File not found" {
		t.Fatalf("missing disk file status=%d body=%q", w.Code, w.Body.String())
	}
	if got := w.Header().Get("Content-Type"); got != "audio/mp4; charset=utf-8" {
		t.Fatalf("missing disk file content type=%q", got)
	}
}

func TestMetadataObjectRequiresAdminAndBuildsAudioTags(t *testing.T) {
	db := catalogFixture(t)
	defer db.Close()
	h := New(db, config.Config{}, nil)
	admin := store.User{ID: "a1", Type: "admin", IsActive: true, Permissions: json.RawMessage(`{"accessAllLibraries":true}`)}
	w := httptest.NewRecorder()
	if !h.Handle(w, httptest.NewRequest("GET", "/api/items/i1/metadata-object", nil), admin) || w.Code != http.StatusOK {
		t.Fatalf("metadata object status=%d body=%s", w.Code, w.Body.String())
	}
	var metadata map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &metadata); err != nil {
		t.Fatal(err)
	}
	if metadata["title"] != "The Book" || metadata["artist"] != "Ada Author" || metadata["TRACKTOTAL"] != "1" || metadata["grouping"] != "First Series #1" {
		t.Fatalf("metadata object=%#v", metadata)
	}
	reader := store.User{ID: "u1", IsActive: true, Permissions: json.RawMessage(`{"accessAllLibraries":true}`)}
	w = httptest.NewRecorder()
	if !h.Handle(w, httptest.NewRequest("GET", "/api/items/i1/metadata-object", nil), reader) || w.Code != http.StatusForbidden {
		t.Fatalf("reader status=%d body=%s", w.Code, w.Body.String())
	}
}

func TestTrackAndEbookStatusUpdates(t *testing.T) {
	db := catalogFixture(t)
	defer db.Close()
	if _, err := db.Exec(`UPDATE libraryItems SET libraryFiles=? WHERE id='i1'`, `[{"ino":"ebook-1","metadata":{"filename":"companion.epub","ext":".epub","path":"/media/books/companion.epub","format":"epub"},"isSupplementary":true}]`); err != nil {
		t.Fatal(err)
	}
	h := New(db, config.Config{}, nil)
	u := store.User{ID: "u1", IsActive: true, Permissions: json.RawMessage(`{"accessAllLibraries":true,"accessAllTags":true,"update":true}`)}
	tracks := httptest.NewRecorder()
	trackReq := httptest.NewRequest("PATCH", "/api/items/i1/tracks", strings.NewReader(`{"orderedFileData":[{"ino":"audio-1","exclude":true}]}`))
	if !h.Handle(tracks, trackReq, u) || tracks.Code != http.StatusOK {
		t.Fatalf("tracks status=%d body=%s", tracks.Code, tracks.Body.String())
	}
	var audioJSON string
	if err := db.QueryRow(`SELECT audioFiles FROM books WHERE id='b1'`).Scan(&audioJSON); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(audioJSON, `"exclude":true`) || !strings.Contains(audioJSON, `"index":-1`) {
		t.Fatalf("audioFiles=%s", audioJSON)
	}
	ebook := httptest.NewRecorder()
	ebookReq := httptest.NewRequest("PATCH", "/api/items/i1/ebook/ebook-1/status", nil)
	if !h.Handle(ebook, ebookReq, u) || ebook.Code != http.StatusOK {
		t.Fatalf("ebook status=%d body=%s", ebook.Code, ebook.Body.String())
	}
	var ebookJSON string
	if err := db.QueryRow(`SELECT ebookFile FROM books WHERE id='b1'`).Scan(&ebookJSON); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(ebookJSON, `"ino":"ebook-1"`) || !strings.Contains(ebookJSON, `"ebookFormat":"epub"`) {
		t.Fatalf("ebookFile=%s", ebookJSON)
	}
}

func TestLibraryItemIncludeAttachesMinifiedFeed(t *testing.T) {
	db := catalogFixture(t)
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE feeds (id TEXT PRIMARY KEY,entityType TEXT,entityId TEXT,feedURL TEXT,title TEXT,description TEXT,preventIndexing BOOLEAN,ownerName TEXT,ownerEmail TEXT)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO feeds VALUES ('feed-1','book','b1','https://example.test/feed','The Feed','Description',1,'Owner','owner@example.test')`); err != nil {
		t.Fatal(err)
	}
	h := New(db, config.Config{}, nil)
	u := store.User{ID: "u1", IsActive: true, Permissions: json.RawMessage(`{"accessAllLibraries":true,"accessAllTags":true}`)}
	w := httptest.NewRecorder()
	if !h.Handle(w, httptest.NewRequest("GET", "/api/libraries/l1/items?include=RSSFEED", nil), u) || w.Code != http.StatusOK {
		t.Fatalf("item include status=%d body=%s", w.Code, w.Body.String())
	}
	var payload map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if payload["include"] != "rssfeed" {
		t.Fatalf("include=%v", payload["include"])
	}
	result := payload["results"].([]any)[0].(map[string]any)
	feed := result["rssFeed"].(map[string]any)
	meta := feed["meta"].(map[string]any)
	if feed["feedUrl"] != "https://example.test/feed" || meta["preventIndexing"] != true {
		t.Fatalf("rssFeed=%#v", feed)
	}
}

func TestCatalogPermissionsMatchRouteSemantics(t *testing.T) {
	db := catalogFixture(t)
	defer db.Close()
	h := New(db, config.Config{}, nil)
	user := store.User{ID: "u2", Permissions: json.RawMessage(`{"accessAllLibraries":false,"librariesAccessible":["l1"]}`)}
	w := httptest.NewRecorder()
	if !h.Handle(w, httptest.NewRequest("GET", "/api/libraries/l2", nil), user) {
		t.Fatal("route did not match")
	}
	if w.Code != http.StatusForbidden {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	list := httptest.NewRecorder()
	if !h.Handle(list, httptest.NewRequest("GET", "/api/libraries", nil), user) {
		t.Fatal("route did not match")
	}
	if list.Code != 200 {
		t.Fatal(list.Code)
	}
	const libraryPrefix = `{"libraries":[{"id":"l1","name":"Shelf","folders":[{`
	if !strings.HasPrefix(list.Body.String(), libraryPrefix) {
		t.Fatalf("filtered library serialization order/body mismatch: %s", list.Body.String())
	}
	if strings.HasSuffix(list.Body.String(), "\n") {
		t.Fatalf("filtered library response has trailing newline: %q", list.Body.String())
	}
	var got map[string]any
	if err := json.Unmarshal(list.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if len(got["libraries"].([]any)) != 1 || got["libraries"].([]any)[0].(map[string]any)["id"] != "l1" {
		t.Fatalf("inaccessible library leaked: %s", list.Body.String())
	}
}

func TestLibraryStatsUseActualBookQueries(t *testing.T) {
	db := catalogFixture(t)
	defer db.Close()
	h := New(db, config.Config{}, nil)
	if _, err := h.libraryStatsValue(t.Context(), "l1", "book"); err != nil {
		t.Fatalf("stats implementation: %v", err)
	}
	u := store.User{ID: "u1", Permissions: json.RawMessage(`{"accessAllLibraries":true}`)}
	w := httptest.NewRecorder()
	if !h.Handle(w, httptest.NewRequest("GET", "/api/libraries/l1/stats", nil), u) {
		t.Fatal("route did not match")
	}
	if w.Code != 200 {
		t.Fatalf("stats status=%d body=%s", w.Code, w.Body.String())
	}
	var stats map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &stats); err != nil {
		t.Fatal(err)
	}
	if stats["totalItems"] != float64(1) || stats["totalAuthors"] != float64(1) || stats["totalGenres"] != float64(1) {
		t.Fatalf("stats=%#v", stats)
	}
}

func TestBookProgressFiltersAndSortUseCurrentUser(t *testing.T) {
	db := catalogFixture(t)
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE mediaProgresses (id TEXT PRIMARY KEY,userId TEXT,mediaItemId TEXT,currentTime FLOAT,ebookProgress FLOAT,isFinished BOOLEAN,updatedAt DATETIME,createdAt DATETIME,finishedAt DATETIME)`); err != nil {
		t.Fatal(err)
	}
	_, _ = db.Exec(`INSERT INTO mediaProgresses VALUES ('mp1','u1','b1',12,0,0,'2024-01-03T00:00:00Z','2024-01-03T00:00:00Z',NULL)`)
	_, _ = db.Exec(`INSERT INTO mediaProgresses VALUES ('mp2','u2','b1',12,0,1,'2024-01-04T00:00:00Z','2024-01-04T00:00:00Z',NULL)`)
	h := New(db, config.Config{}, nil)
	u := store.User{ID: "u1", IsActive: true, Permissions: json.RawMessage(`{"accessAllLibraries":true,"accessExplicitContent":true}`)}
	w := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/api/libraries/l1/items?filter=progress.in-progress&sort=progress", nil)
	if !h.Handle(w, req, u) || w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body["total"] != float64(1) {
		t.Fatalf("progress filter leaked other user's row or missed match: %s", w.Body.String())
	}
}

func TestLibraryFilterDataRoute(t *testing.T) {
	db := catalogFixture(t)
	defer db.Close()
	h := New(db, config.Config{}, nil)
	u := store.User{ID: "u1", Permissions: json.RawMessage(`{"accessAllLibraries":true}`)}
	w := httptest.NewRecorder()
	if !h.Handle(w, httptest.NewRequest("GET", "/api/libraries/l1/filterdata", nil), u) || w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	var got map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got["bookCount"] != float64(1) || got["numIssues"] != float64(0) {
		t.Fatalf("filterdata=%s", w.Body.String())
	}
}

func TestPersonalizedBookShelvesUsePerUserProgress(t *testing.T) {
	db := catalogFixture(t)
	defer db.Close()
	if _, err := db.Exec(`UPDATE libraryItems SET createdAt=datetime('now') WHERE id='i1'`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE mediaProgresses (id TEXT PRIMARY KEY,userId TEXT,mediaItemId TEXT,currentTime FLOAT,ebookProgress FLOAT,isFinished BOOLEAN,hideFromContinueListening BOOLEAN,updatedAt DATETIME,createdAt DATETIME,finishedAt DATETIME)`); err != nil {
		t.Fatal(err)
	}
	_, _ = db.Exec(`INSERT INTO mediaProgresses VALUES ('mp1','u1','b1',4,0,0,0,datetime('now'),'2024-01-01T00:00:00Z',NULL)`)
	_, _ = db.Exec(`INSERT INTO mediaProgresses VALUES ('mp2','u2','b1',4,0,1,0,datetime('now'),'2024-01-01T00:00:00Z',NULL)`)
	h := New(db, config.Config{}, nil)
	u := store.User{ID: "u1", IsActive: true, Permissions: json.RawMessage(`{"accessAllLibraries":true,"accessExplicitContent":true,"accessAllTags":true}`)}
	w := httptest.NewRecorder()
	if !h.Handle(w, httptest.NewRequest("GET", "/api/libraries/l1/personalized", nil), u) || w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	var shelves []map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &shelves); err != nil {
		t.Fatal(err)
	}
	if len(shelves) != 2 || shelves[0]["id"] != "continue-listening" || shelves[1]["id"] != "recently-added" {
		t.Fatalf("shelves=%s", w.Body.String())
	}
}

func TestUpdateMediaPersistsPresentFieldsAndLeavesAbsentFieldsUntouched(t *testing.T) {
	db := catalogFixture(t)
	defer db.Close()
	h := New(db, config.Config{}, nil)
	u := store.User{ID: "u1", IsActive: true, Permissions: json.RawMessage(`{"accessAllLibraries":true,"update":true,"accessExplicitContent":true,"accessAllTags":true}`)}
	body := `{"metadata":{"title":"Changed Book","subtitle":null,"publishedYear":2030,"description":"<script>bad()</script><p>Safe</p>","authors":[{"name":"Ada Author"},{"name":"New Author"}],"series":[{"name":"First Series","sequence":"2"},{"name":"Another Series","sequence":"1"}]},"tags":["new","tag"]}`
	w := httptest.NewRecorder()
	req := httptest.NewRequest("PATCH", "/api/items/i1/media", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if !h.Handle(w, req, u) || w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	var title string
	var subtitle, publishedDate, year, safeDescription sql.NullString
	var tagJSON string
	if err := db.QueryRow(`SELECT title,subtitle,publishedDate,publishedYear,description,tags FROM books WHERE id='b1'`).Scan(&title, &subtitle, &publishedDate, &year, &safeDescription, &tagJSON); err != nil {
		t.Fatal(err)
	}
	if title != "Changed Book" || subtitle.Valid || publishedDate.Valid || year.String != "2030" || !safeDescription.Valid || strings.Contains(safeDescription.String, "<script>") {
		t.Fatalf("stored fields title=%q subtitle=%#v date=%#v year=%#v description=%#v", title, subtitle, publishedDate, year, safeDescription)
	}
	if tagJSON != `["new","tag"]` {
		t.Fatalf("tags=%s", tagJSON)
	}
	var authorCount, seriesCount int
	if err := db.QueryRow(`SELECT COUNT(*) FROM bookAuthors WHERE bookId='b1'`).Scan(&authorCount); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM bookSeries WHERE bookId='b1'`).Scan(&seriesCount); err != nil {
		t.Fatal(err)
	}
	if authorCount != 2 || seriesCount != 2 {
		t.Fatalf("relations authors=%d series=%d", authorCount, seriesCount)
	}
}

func TestDeleteLibraryFileRemovesManagedMediaAndMarksEmptyBookMissing(t *testing.T) {
	db := catalogFixture(t)
	defer db.Close()
	root := t.TempDir()
	target := filepath.Join(root, "only.mp3")
	if err := os.WriteFile(target, []byte("audio"), 0600); err != nil {
		t.Fatal(err)
	}
	_, _ = db.Exec(`UPDATE libraryFolders SET path=? WHERE id='f1'`, root)
	files := `[{"ino":"file-x","metadata":{"filename":"only.mp3","ext":".mp3","path":"` + target + `","size":5}}]`
	_, _ = db.Exec(`UPDATE libraryItems SET libraryFiles=? WHERE id='i1'`, files)
	_, _ = db.Exec(`UPDATE books SET audioFiles=? WHERE id='b1'`, `[{"ino":"file-x","metadata":{"filename":"only.mp3","ext":".mp3","path":"`+target+`","size":5}}]`)
	h := New(db, config.Config{}, nil)
	u := store.User{ID: "u1", IsActive: true, Permissions: json.RawMessage(`{"accessAllLibraries":true,"update":true}`)}
	w := httptest.NewRecorder()
	if !h.Handle(w, httptest.NewRequest("DELETE", "/api/items/i1/file/file-x", nil), u) || w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	if _, err := os.Stat(target); !os.IsNotExist(err) {
		t.Fatalf("media still exists, stat err=%v", err)
	}
	var missing bool
	var fileJSON, audioJSON string
	if err := db.QueryRow(`SELECT isMissing,libraryFiles FROM libraryItems WHERE id='i1'`).Scan(&missing, &fileJSON); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT audioFiles FROM books WHERE id='b1'`).Scan(&audioJSON); err != nil {
		t.Fatal(err)
	}
	if !missing || fileJSON != "[]" || audioJSON != "[]" {
		t.Fatalf("missing=%v libraryFiles=%s audioFiles=%s", missing, fileJSON, audioJSON)
	}
}

func TestLibraryCreateAndUpdateRefreshWatcherAfterCommit(t *testing.T) {
	db := catalogFixture(t)
	defer db.Close()
	h := New(db, config.Config{}, nil)
	changed := 0
	h.SetLibraryChanged(func(string) { changed++ })
	u := store.User{ID: "root", Type: "root", IsActive: true, Permissions: json.RawMessage(`{"accessAllLibraries":true}`)}
	folder := filepath.Join(t.TempDir(), "new-books")
	body := `{"name":"New Library","mediaType":"book","folders":[{"fullPath":"` + folder + `"}]}`
	w := httptest.NewRecorder()
	if !h.Handle(w, httptest.NewRequest("POST", "/api/libraries", strings.NewReader(body)), u) || w.Code != http.StatusOK {
		t.Fatalf("create status=%d body=%s", w.Code, w.Body.String())
	}
	var created map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	id := created["id"].(string)
	if changed != 1 {
		t.Fatalf("watcher refresh count after create=%d", changed)
	}
	second := filepath.Join(t.TempDir(), "new-books-2")
	updateBody := `{"name":"Renamed","settings":{"disableWatcher":true},"folders":[{"id":"` + created["folders"].([]any)[0].(map[string]any)["id"].(string) + `"},{"fullPath":"` + second + `"}]}`
	w = httptest.NewRecorder()
	if !h.Handle(w, httptest.NewRequest("PATCH", "/api/libraries/"+id, strings.NewReader(updateBody)), u) || w.Code != http.StatusOK {
		t.Fatalf("update status=%d body=%s", w.Code, w.Body.String())
	}
	if changed != 2 {
		t.Fatalf("watcher refresh count after update=%d", changed)
	}
	var name string
	var settingsRaw []byte
	var folderCount int
	if err := db.QueryRow(`SELECT name,settings FROM libraries WHERE id=?`, id).Scan(&name, &settingsRaw); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM libraryFolders WHERE libraryId=?`, id).Scan(&folderCount); err != nil {
		t.Fatal(err)
	}
	if name != "Renamed" || folderCount != 2 || decodeObject(settingsRaw)["disableWatcher"] != true {
		t.Fatalf("name=%s folders=%d settings=%s", name, folderCount, settingsRaw)
	}
}

func TestDeleteItemRemovesMediaProgressAndEmptyRelations(t *testing.T) {
	db := catalogFixture(t)
	defer db.Close()
	for _, ddl := range []string{`CREATE TABLE mediaProgresses (id TEXT,userId TEXT,mediaItemId TEXT)`, `CREATE TABLE playlistMediaItems (id TEXT,mediaItemId TEXT,mediaItemType TEXT,"order" INTEGER,playlistId TEXT)`, `CREATE TABLE playlists (id TEXT,userId TEXT)`, `CREATE TABLE feeds (id TEXT,entityType TEXT,entityId TEXT)`, `CREATE TABLE mediaItemShares (id TEXT,mediaItemType TEXT,mediaItemId TEXT)`} {
		if _, err := db.Exec(ddl); err != nil {
			t.Fatal(err)
		}
	}
	_, _ = db.Exec(`INSERT INTO mediaProgresses VALUES ('mp','u1','b1')`)
	_, _ = db.Exec(`INSERT INTO playlists VALUES ('pl','u1')`)
	_, _ = db.Exec(`INSERT INTO playlistMediaItems VALUES ('pli','b1','book',1,'pl')`)
	_, _ = db.Exec(`INSERT INTO feeds VALUES ('feed','libraryItem','i1')`)
	h := New(db, config.Config{}, nil)
	u := store.User{ID: "u1", IsActive: true, Permissions: json.RawMessage(`{"accessAllLibraries":true,"delete":true}`)}
	w := httptest.NewRecorder()
	if !h.Handle(w, httptest.NewRequest("DELETE", "/api/items/i1", nil), u) || w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	for _, table := range []string{"libraryItems", "books", "mediaProgresses", "playlistMediaItems", "playlists", "feeds", "authors", "series"} {
		var count int
		if err := db.QueryRow(`SELECT COUNT(*) FROM ` + table).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != 0 {
			t.Fatalf("%s still has %d rows", table, count)
		}
	}
}

func TestLibraryDeleteClearsLibraryRowsAndRefreshesWatcher(t *testing.T) {
	db := catalogFixture(t)
	defer db.Close()
	for _, ddl := range []string{`CREATE TABLE mediaProgresses (id TEXT,userId TEXT,mediaItemId TEXT)`, `CREATE TABLE playlistMediaItems (id TEXT,mediaItemId TEXT,mediaItemType TEXT,"order" INTEGER,playlistId TEXT)`, `CREATE TABLE playlists (id TEXT,userId TEXT)`, `CREATE TABLE feeds (id TEXT,entityType TEXT,entityId TEXT)`, `CREATE TABLE mediaItemShares (id TEXT,mediaItemType TEXT,mediaItemId TEXT)`, `CREATE TABLE collections (id TEXT,libraryId TEXT)`, `CREATE TABLE collectionBooks (id TEXT,collectionId TEXT,bookId TEXT)`, `CREATE TABLE playbackSessions (id TEXT,libraryId TEXT)`} {
		if _, err := db.Exec(ddl); err != nil {
			t.Fatal(err)
		}
	}
	_, _ = db.Exec(`INSERT INTO mediaProgresses VALUES ('mp','u1','b1')`)
	h := New(db, config.Config{}, nil)
	refresh := 0
	h.SetLibraryChanged(func(string) { refresh++ })
	u := store.User{ID: "root", Type: "admin", IsActive: true}
	w := httptest.NewRecorder()
	if !h.Handle(w, httptest.NewRequest("DELETE", "/api/libraries/l1", nil), u) || w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	if refresh != 1 {
		t.Fatalf("watcher refreshes=%d", refresh)
	}
	for _, table := range []string{"libraries", "libraryFolders", "libraryItems", "books", "mediaProgresses", "authors", "series"} {
		var count int
		query := `SELECT COUNT(*) FROM ` + table
		switch table {
		case "libraries":
			query += ` WHERE id='l1'`
		case "libraryFolders", "libraryItems", "authors", "series":
			query += ` WHERE libraryId='l1'`
		}
		if err := db.QueryRow(query).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != 0 {
			t.Fatalf("%s still has %d rows", table, count)
		}
	}
}

func TestCoverRawServesOriginalAndHonorsTimestampCacheHeader(t *testing.T) {
	db := catalogFixture(t)
	defer db.Close()
	metadata := t.TempDir()
	cover := filepath.Join(metadata, "cover.png")
	original := []byte("raw-cover-bytes")
	if err := os.WriteFile(cover, original, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE books SET coverPath=? WHERE id='b1'`, cover); err != nil {
		t.Fatal(err)
	}
	h := New(db, config.Config{MetadataPath: metadata}, nil)
	w := httptest.NewRecorder()
	if !h.Handle(w, httptest.NewRequest("GET", "/api/items/i1/cover?raw=1&ts=123", nil), store.User{}) {
		t.Fatal("cover route did not match")
	}
	if w.Code != http.StatusOK || w.Body.String() != string(original) || w.Header().Get("Cache-Control") != "private, max-age=86400" || w.Header().Get("Content-Type") != "image/png" {
		t.Fatalf("status=%d headers=%v body=%q", w.Code, w.Header(), w.Body.String())
	}
}

func TestSeriesFiltersAndIncludesReturnBooksProgressAndFeed(t *testing.T) {
	db := catalogFixture(t)
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE feeds (id TEXT,entityType TEXT,entityId TEXT,feedURL TEXT,title TEXT,description TEXT,preventIndexing BOOLEAN,ownerName TEXT,ownerEmail TEXT)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE mediaProgresses (id TEXT,userId TEXT,mediaItemId TEXT,isFinished BOOLEAN,currentTime FLOAT,ebookProgress FLOAT,updatedAt DATETIME)`); err != nil {
		t.Fatal(err)
	}
	_, _ = db.Exec(`INSERT INTO feeds VALUES ('feed-s','series','s1','https://example.test/rss','Feed','Description',0,'Owner','owner@example.test')`)
	_, _ = db.Exec(`INSERT INTO mediaProgresses VALUES ('mp1','u1','b1',0,1,0,'2024-01-03T00:00:00Z')`)
	h := New(db, config.Config{}, nil)
	user := store.User{ID: "u1", IsActive: true, Permissions: json.RawMessage(`{"accessAllLibraries":true,"accessExplicitContent":true,"accessAllTags":true}`)}
	w := httptest.NewRecorder()
	if !h.Handle(w, httptest.NewRequest("GET", "/api/libraries/l1/series?filter=genres.ZmljdGlvbg==&include=rssfeed", nil), user) || w.Code != http.StatusOK {
		t.Fatalf("series status=%d body=%s", w.Code, w.Body.String())
	}
	var list map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &list); err != nil {
		t.Fatal(err)
	}
	entry := list["results"].([]any)[0].(map[string]any)
	if entry["rssFeed"].(map[string]any)["feedUrl"] != "https://example.test/rss" {
		t.Fatalf("series include=%s", w.Body.String())
	}
	w = httptest.NewRecorder()
	if !h.Handle(w, httptest.NewRequest("GET", "/api/libraries/l1/series/s1?include=progress,rssfeed", nil), user) || w.Code != http.StatusOK {
		t.Fatalf("series detail status=%d body=%s", w.Code, w.Body.String())
	}
	var detail map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &detail); err != nil {
		t.Fatal(err)
	}
	progress := detail["progress"].(map[string]any)
	if progress["isFinished"] != false || len(progress["libraryItemIds"].([]any)) != 1 || len(progress["libraryItemIdsFinished"].([]any)) != 0 {
		t.Fatalf("series progress=%#v", progress)
	}
}

func TestSeriesBooksUseMinifiedBookMediaShape(t *testing.T) {
	db := catalogFixture(t)
	defer db.Close()
	h := New(db, config.Config{}, nil)
	user := store.User{ID: "u1", IsActive: true, Permissions: json.RawMessage(`{"accessAllLibraries":true,"accessExplicitContent":true,"accessAllTags":true}`)}
	w := httptest.NewRecorder()
	if !h.Handle(w, httptest.NewRequest(http.MethodGet, "/api/libraries/l1/series", nil), user) || w.Code != http.StatusOK {
		t.Fatalf("series status=%d body=%s", w.Code, w.Body.String())
	}
	var payload map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	series := payload["results"].([]any)[0].(map[string]any)
	item := series["books"].([]any)[0].(map[string]any)
	media := item["media"].(map[string]any)
	// Upstream seriesFilters.js:208-213 assigns each book to LibraryItem.toOldJSONMinified;
	// LibraryItem.js:1000-1024 serializes media with Book.toOldJSONMinified;
	// Book.js:640-659 defines this exact reduced media key set.
	wantMediaKeys := []string{"coverPath", "duration", "id", "metadata", "numAudioFiles", "numChapters", "numTracks", "size", "tags"}
	gotMediaKeys := make([]string, 0, len(media))
	for key := range media {
		gotMediaKeys = append(gotMediaKeys, key)
	}
	sort.Strings(gotMediaKeys)
	if !reflect.DeepEqual(gotMediaKeys, wantMediaKeys) {
		t.Fatalf("series book media keys=%v want %v", gotMediaKeys, wantMediaKeys)
	}
	if _, ok := media["ebookFormat"]; ok {
		t.Error("series book includes ebookFormat when its ebookFile is null")
	}
	metadata := media["metadata"].(map[string]any)
	if _, ok := metadata["descriptionPlain"]; ok {
		t.Error("series book minified metadata contains descriptionPlain")
	}
	if metadata["description"] != "<p>A story</p>" {
		t.Errorf("minified description=%#v", metadata["description"])
	}
	for _, key := range []string{"titleIgnorePrefix", "authorName", "authorNameLF", "narratorName", "seriesName", "genres"} {
		if _, ok := metadata[key]; !ok {
			t.Errorf("series book minified metadata missing %q", key)
		}
	}
	if _, ok := media["libraryItemId"]; ok {
		t.Error("series book minified media contains libraryItemId")
	}
	if _, ok := media["tracks"]; ok {
		t.Error("series book minified media contains tracks")
	}
	for _, key := range []string{"audioFiles", "chapters", "ebookFile"} {
		if _, ok := media[key]; ok {
			t.Errorf("series book minified media contains %q", key)
		}
	}
}

func TestSeriesPaginationUsesPageThenLimit(t *testing.T) {
	db := catalogFixture(t)
	defer db.Close()
	if _, err := db.Exec(`INSERT INTO series VALUES ('s2','Second Series','Second Series',NULL,'l1','2024-01-01T00:00:00Z','2024-01-01T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO bookSeries VALUES ('bs2','2','b1','s2','2024-01-01T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO series VALUES ('s3','Third Series','Third Series',NULL,'l1','2024-01-01T00:00:00Z','2024-01-01T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO bookSeries VALUES ('bs3','3','b1','s3','2024-01-01T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO series VALUES ('s4','Fourth Series','Fourth Series',NULL,'l1','2024-01-01T00:00:00Z','2024-01-01T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO bookSeries VALUES ('bs4','4','b1','s4','2024-01-01T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	h := New(db, config.Config{}, nil)
	user := store.User{ID: "u1", Permissions: json.RawMessage(`{"accessAllLibraries":true,"accessAllTags":true,"accessExplicitContent":true}`)}
	w := httptest.NewRecorder()
	if !h.Handle(w, httptest.NewRequest("GET", "/api/libraries/l1/series?page=1&limit=2", nil), user) || w.Code != http.StatusOK {
		t.Fatalf("series status=%d body=%s", w.Code, w.Body.String())
	}
	var payload struct {
		Results []map[string]any `json:"results"`
		Total   int              `json:"total"`
		Page    int              `json:"page"`
		Limit   int              `json:"limit"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Total != 4 || payload.Page != 1 || payload.Limit != 2 || len(payload.Results) != 2 || payload.Results[0]["id"] != "s3" || payload.Results[1]["id"] != "s4" {
		t.Fatalf("unexpected pagination response: %+v", payload)
	}
}

func TestInvalidLibraryItemsPaginationUsesExpressSend(t *testing.T) {
	db := catalogFixture(t)
	defer db.Close()
	h := New(db, config.Config{}, nil)
	w := httptest.NewRecorder()
	u := store.User{ID: "u1", Permissions: json.RawMessage(`{"accessAllLibraries":true}`)}
	if !h.Handle(w, httptest.NewRequest(http.MethodGet, "/api/libraries/l1/items?page=-1", nil), u) {
		t.Fatal("route did not match")
	}
	const body = "Invalid request. page must be a positive integer"
	if w.Code != http.StatusBadRequest || w.Body.String() != body || w.Header().Get("Content-Type") != "text/html; charset=utf-8" || w.Header().Get("ETag") != expressETag([]byte(body)) {
		t.Fatalf("status=%d headers=%v body=%q", w.Code, w.Header(), w.Body.String())
	}
}

func TestMediaFileUsesRangeAndRejectsPathsOutsideLibrary(t *testing.T) {
	db := catalogFixture(t)
	defer db.Close()
	root := t.TempDir()
	mediaPath := filepath.Join(root, "audio.mp3")
	if err := os.WriteFile(mediaPath, []byte("0123456789"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE libraryFolders SET path=? WHERE id='f1'`, root); err != nil {
		t.Fatal(err)
	}
	files, _ := json.Marshal([]map[string]any{{"ino": "audio-1", "metadata": map[string]any{"filename": "audio.mp3", "ext": ".mp3", "path": mediaPath}}})
	if _, err := db.Exec(`UPDATE libraryItems SET libraryFiles=? WHERE id='i1'`, string(files)); err != nil {
		t.Fatal(err)
	}
	h := New(db, config.Config{}, nil)
	u := store.User{ID: "u1", Permissions: json.RawMessage(`{"accessAllLibraries":true}`)}
	w := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/api/items/i1/file/audio-1", nil)
	req.Header.Set("Range", "bytes=2-4")
	if !h.Handle(w, req, u) {
		t.Fatal("route did not match")
	}
	if w.Code != http.StatusPartialContent || w.Body.String() != "234" {
		t.Fatalf("status=%d body=%q", w.Code, w.Body.String())
	}
	m4bPath := filepath.Join(root, "audio.m4b")
	if err := os.WriteFile(m4bPath, []byte("m4b"), 0600); err != nil {
		t.Fatal(err)
	}
	files, _ = json.Marshal([]map[string]any{{"ino": "m4b", "metadata": map[string]any{"filename": "audio.m4b", "ext": ".m4b", "path": m4bPath}}})
	if _, err := db.Exec(`UPDATE libraryItems SET libraryFiles=? WHERE id='i1'`, string(files)); err != nil {
		t.Fatal(err)
	}
	m4b := httptest.NewRecorder()
	if !h.Handle(m4b, httptest.NewRequest("GET", "/api/items/i1/file/m4b", nil), u) {
		t.Fatal("route did not match")
	}
	if got := m4b.Header().Get("Content-Type"); got != "audio/mp4" {
		t.Fatalf("m4b Content-Type=%q", got)
	}
	outside := filepath.Join(t.TempDir(), "outside.mp3")
	if err := os.WriteFile(outside, []byte("secret"), 0600); err != nil {
		t.Fatal(err)
	}
	files, _ = json.Marshal([]map[string]any{{"ino": "escape", "metadata": map[string]any{"filename": "outside.mp3", "ext": ".mp3", "path": outside}}})
	if _, err := db.Exec(`UPDATE libraryItems SET libraryFiles=? WHERE id='i1'`, string(files)); err != nil {
		t.Fatal(err)
	}
	blocked := httptest.NewRecorder()
	if !h.Handle(blocked, httptest.NewRequest("GET", "/api/items/i1/file/escape", nil), u) {
		t.Fatal("route did not match")
	}
	if blocked.Code != http.StatusNotFound {
		t.Fatalf("outside path status=%d", blocked.Code)
	}
}

func TestPodcastItemDetailSerializesEpisodeAndTrack(t *testing.T) {
	db := catalogFixture(t)
	defer db.Close()
	ddl := []string{
		`CREATE TABLE podcasts (id TEXT PRIMARY KEY,title TEXT,titleIgnorePrefix TEXT,author TEXT,releaseDate TEXT,feedURL TEXT,imageURL TEXT,description TEXT,itunesPageURL TEXT,itunesId TEXT,itunesArtistId TEXT,language TEXT,podcastType TEXT,explicit BOOLEAN,autoDownloadEpisodes BOOLEAN,autoDownloadSchedule TEXT,lastEpisodeCheck DATETIME,maxEpisodesToKeep INTEGER,maxNewEpisodesToDownload INTEGER,coverPath TEXT,tags JSON,genres JSON,numEpisodes INTEGER,createdAt DATETIME,updatedAt DATETIME)`,
		`CREATE TABLE podcastEpisodes (id TEXT PRIMARY KEY,"index" INTEGER,season TEXT,episode TEXT,episodeType TEXT,title TEXT,subtitle TEXT,description TEXT,pubDate TEXT,enclosureURL TEXT,enclosureSize BIGINT,enclosureType TEXT,publishedAt DATETIME,audioFile JSON,chapters JSON,extraData JSON,podcastId TEXT,createdAt DATETIME,updatedAt DATETIME)`,
		`INSERT INTO libraries VALUES ('lp','Podcasts',2,'podcast','podcast','podcastIndex',NULL,NULL,'{}','{}','2024-01-01T00:00:00Z','2024-01-02T00:00:00Z')`,
		`INSERT INTO podcasts VALUES ('p1','Science Now','Science Now','Host','2024-01-01','https://example.test/feed','https://example.test/image.jpg','<p>About</p>',NULL,NULL,NULL,'en','episodic',0,1,'0 * * * *',NULL,0,3,'/metadata/covers/p1.jpg','["science"]','["science"]',1,'2024-01-01T00:00:00Z','2024-01-02T00:00:00Z')`,
		`INSERT INTO podcastEpisodes VALUES ('e1',1,'1','1','full','Launch','Pilot','First episode','2024-01-01','https://example.test/e.mp3',123,'audio/mpeg','2024-01-01T00:00:00Z','{"ino":"ep-audio","metadata":{"filename":"episode.mp3","ext":".mp3","path":"/media/podcasts/episode.mp3","relPath":"episode.mp3","size":123,"mtimeMs":1704067200000,"ctimeMs":1704067200000,"birthtimeMs":1704067200000},"duration":30,"mimeType":"audio/mpeg"}','[]','{"guid":"guid-e1"}','p1','2024-01-01T00:00:00Z','2024-01-02T00:00:00Z')`,
		`INSERT INTO libraryItems VALUES ('ip','ino-p','/media/podcasts','podcast','p1','podcast',0,0,0,'2024-01-01T00:00:00Z','2024-01-01T00:00:00Z','2024-01-01T00:00:00Z',123,NULL,NULL,'[]','{}','Science Now','Science Now',NULL,NULL,'lp',NULL,'2024-01-01T00:00:00Z','2024-01-02T00:00:00Z')`,
	}
	for _, q := range ddl {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Exec(`CREATE TABLE mediaProgresses (id TEXT PRIMARY KEY,userId TEXT,mediaItemId TEXT,isFinished BOOLEAN,currentTime FLOAT,hideFromContinueListening BOOLEAN,updatedAt DATETIME,createdAt DATETIME,finishedAt DATETIME)`); err != nil {
		t.Fatal(err)
	}
	_, _ = db.Exec(`INSERT INTO mediaProgresses VALUES ('mp-ep','u1','e1',0,10,0,'2024-01-03T00:00:00Z','2024-01-03T00:00:00Z',NULL)`)
	h := New(db, config.Config{}, nil)
	user := store.User{ID: "u1", Permissions: json.RawMessage(`{"accessAllLibraries":true,"accessAllTags":true,"accessExplicitContent":true}`)}
	w := httptest.NewRecorder()
	if !h.Handle(w, httptest.NewRequest("GET", "/api/items/ip?expanded=1", nil), user) {
		t.Fatal("route did not match")
	}
	if w.Code != 200 {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	var item map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &item); err != nil {
		t.Fatal(err)
	}
	media := item["media"].(map[string]any)
	episodes := media["episodes"].([]any)
	if len(episodes) != 1 {
		t.Fatalf("media=%s", w.Body.String())
	}
	ep := episodes[0].(map[string]any)
	track := ep["audioTrack"].(map[string]any)
	if ep["guid"] != "guid-e1" || track["contentUrl"] != "/api/items/ip/file/ep-audio" || track["index"] != float64(1) {
		t.Fatalf("episode=%#v", ep)
	}
	personalized := httptest.NewRecorder()
	if !h.Handle(personalized, httptest.NewRequest("GET", "/api/libraries/lp/personalized", nil), user) || personalized.Code != http.StatusOK {
		t.Fatalf("personalized status=%d body=%s", personalized.Code, personalized.Body.String())
	}
	var shelves []map[string]any
	if err := json.Unmarshal(personalized.Body.Bytes(), &shelves); err != nil {
		t.Fatal(err)
	}
	if len(shelves) != 1 || shelves[0]["id"] != "continue-listening" {
		t.Fatalf("podcast shelves=%s", personalized.Body.String())
	}
	entity := shelves[0]["entities"].([]any)[0].(map[string]any)
	if entity["recentEpisode"].(map[string]any)["guid"] != "guid-e1" {
		t.Fatalf("episode shelf entity=%#v", entity)
	}
}
