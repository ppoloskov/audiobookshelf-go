package library

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	schemaassets "audiobookshelf-compat/contracts/parity"
	"audiobookshelf-compat/internal/filterdata"
	"audiobookshelf-compat/internal/itemwrite"
	"audiobookshelf-compat/internal/store"
)

type writeRig struct {
	t       *testing.T
	db      *sql.DB
	mux     *http.ServeMux
	changed int
	events  []string
	deleter itemwrite.Deleter
}

func newWriteRig(t *testing.T) *writeRig {
	t.Helper()
	db, err := sql.Open("sqlite", "file:write-"+strings.ReplaceAll(t.Name(), "/", "-")+"?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.Exec(schemaassets.SchemaSQL); err != nil {
		t.Fatal(err)
	}
	rig := &writeRig{t: t, db: db}
	filters := filterdata.NewCache()
	emit := func(_, event string, _ any) { rig.events = append(rig.events, event) }
	rig.deleter = itemwrite.Deleter{Env: itemwrite.Env{DB: db, Filters: filters, Emit: emit}, CoverCacheDir: t.TempDir(), ItemMetadataDir: t.TempDir()}
	rig.mux = http.NewServeMux()
	New(db, emit, Deps{
		Filters:        filters,
		LibraryChanged: func(string) { rig.changed++ },
		ItemDeleter:    rig.deleter,
	}).Routes(rig.mux)
	return rig
}

func (r *writeRig) do(method, target, body string, user store.User) *httptest.ResponseRecorder {
	r.t.Helper()
	req := httptest.NewRequest(method, target, strings.NewReader(body))
	req = req.WithContext(store.ContextWithUser(req.Context(), user))
	w := httptest.NewRecorder()
	r.mux.ServeHTTP(w, req)
	return w
}

func TestLibraryCreateAndUpdateRefreshWatcherAfterCommit(t *testing.T) {
	rig := newWriteRig(t)
	admin := store.User{ID: "root", Type: "root", IsActive: true, Permissions: json.RawMessage(`{"accessAllLibraries":true}`)}
	folder := filepath.Join(t.TempDir(), "new-books")
	body := `{"name":"New Library","mediaType":"book","folders":[{"fullPath":"` + folder + `"}]}`
	w := rig.do(http.MethodPost, "/api/libraries", body, admin)
	if w.Code != http.StatusOK {
		t.Fatalf("create status=%d body=%s", w.Code, w.Body.String())
	}
	var created map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	id := created["id"].(string)
	if rig.changed != 1 {
		t.Fatalf("watcher refresh count after create=%d", rig.changed)
	}
	second := filepath.Join(t.TempDir(), "new-books-2")
	updateBody := `{"name":"Renamed","settings":{"disableWatcher":true},"folders":[{"id":"` + created["folders"].([]any)[0].(map[string]any)["id"].(string) + `"},{"fullPath":"` + second + `"}]}`
	w = rig.do(http.MethodPatch, "/api/libraries/"+id, updateBody, admin)
	if w.Code != http.StatusOK {
		t.Fatalf("update status=%d body=%s", w.Code, w.Body.String())
	}
	if rig.changed != 2 {
		t.Fatalf("watcher refresh count after update=%d", rig.changed)
	}
	var name string
	var settingsRaw []byte
	var folderCount int
	if err := rig.db.QueryRow(`SELECT name,settings FROM libraries WHERE id=?`, id).Scan(&name, &settingsRaw); err != nil {
		t.Fatal(err)
	}
	if err := rig.db.QueryRow(`SELECT COUNT(*) FROM libraryFolders WHERE libraryId=?`, id).Scan(&folderCount); err != nil {
		t.Fatal(err)
	}
	if name != "Renamed" || folderCount != 2 || store.DecodeJSONObject(settingsRaw)["disableWatcher"] != true {
		t.Fatalf("name=%s folders=%d settings=%s", name, folderCount, settingsRaw)
	}
}

func (r *writeRig) seedFolderItem(t *testing.T) {
	t.Helper()
	const now = "2024-01-01 00:00:00.000 +00:00"
	for _, q := range []string{
		`INSERT INTO libraries(id,name,displayOrder,icon,mediaType,provider,settings,extraData,createdAt,updatedAt) VALUES('l1','Shelf',0,'database','book','google','{}','{}','` + now + `','` + now + `')`,
		`INSERT INTO libraryFolders(id,path,libraryId,createdAt,updatedAt) VALUES('f1','/books','l1','` + now + `','` + now + `')`,
		`INSERT INTO books(id,title,explicit,abridged,duration,narrators,audioFiles,ebookFile,chapters,tags,genres,createdAt,updatedAt) VALUES('b1','Book',0,0,1,'[]','[]','null','[]','[]','[]','` + now + `','` + now + `')`,
		`INSERT INTO libraryItems(id,ino,path,relPath,mediaId,mediaType,isFile,isMissing,isInvalid,libraryFiles,extraData,size,title,titleIgnorePrefix,authorNamesFirstLast,authorNamesLastFirst,createdAt,updatedAt,libraryId,libraryFolderId) VALUES('i1','ino1','/books/Book','Book','b1','book',0,0,0,'[]','{}',1,'Book','Book','','','` + now + `','` + now + `','l1','f1')`,
	} {
		if _, err := r.db.Exec(q); err != nil {
			t.Fatalf("%v\n%s", err, q)
		}
	}
}

func eventCount(events []string, name string) int {
	n := 0
	for _, event := range events {
		if event == name {
			n++
		}
	}
	return n
}

func TestLibraryUpdateFolderRemovalOnlyDeletesAndRefreshes(t *testing.T) {
	rig := newWriteRig(t)
	rig.seedFolderItem(t)
	w := rig.do(http.MethodPatch, "/api/libraries/l1", `{"folders":[]}`, seriesRootUser())
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	if rig.changed != 1 || eventCount(rig.events, "library_updated") != 1 {
		t.Fatalf("changed=%d events=%v", rig.changed, rig.events)
	}
	var folders, items int
	if err := rig.db.QueryRow(`SELECT COUNT(*) FROM libraryFolders WHERE libraryId='l1'`).Scan(&folders); err != nil {
		t.Fatal(err)
	}
	if err := rig.db.QueryRow(`SELECT COUNT(*) FROM libraryItems WHERE libraryId='l1'`).Scan(&items); err != nil {
		t.Fatal(err)
	}
	if folders != 0 || items != 0 {
		t.Fatalf("folders=%d items=%d", folders, items)
	}
}

func TestLibraryUpdateFolderRemovalErrorKeepsFolderAndSkipsRefresh(t *testing.T) {
	rig := newWriteRig(t)
	rig.seedFolderItem(t)
	if _, err := rig.db.Exec(`CREATE TRIGGER fail_item_delete BEFORE DELETE ON libraryItems BEGIN SELECT RAISE(ABORT,'test'); END`); err != nil {
		t.Fatal(err)
	}
	w := rig.do(http.MethodPatch, "/api/libraries/l1", `{"folders":[]}`, seriesRootUser())
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	if rig.changed != 0 || eventCount(rig.events, "library_updated") != 0 {
		t.Fatalf("changed=%d events=%v", rig.changed, rig.events)
	}
	var folders, items int
	if err := rig.db.QueryRow(`SELECT COUNT(*) FROM libraryFolders WHERE libraryId='l1'`).Scan(&folders); err != nil {
		t.Fatal(err)
	}
	if err := rig.db.QueryRow(`SELECT COUNT(*) FROM libraryItems WHERE libraryId='l1'`).Scan(&items); err != nil {
		t.Fatal(err)
	}
	if folders != 1 || items != 1 {
		t.Fatalf("folders=%d items=%d", folders, items)
	}
}

func TestLibraryUpdateFolderRemovalRepeatIsIdempotent(t *testing.T) {
	rig := newWriteRig(t)
	rig.seedFolderItem(t)
	for i := 0; i < 2; i++ {
		w := rig.do(http.MethodPatch, "/api/libraries/l1", `{"folders":[]}`, seriesRootUser())
		if w.Code != http.StatusOK {
			t.Fatalf("pass %d status=%d body=%s", i, w.Code, w.Body.String())
		}
	}
	if rig.changed != 1 || eventCount(rig.events, "library_updated") != 1 {
		t.Fatalf("changed=%d events=%v", rig.changed, rig.events)
	}
}

func (r *writeRig) seedSecondFolderItem(t *testing.T) {
	t.Helper()
	const now = "2024-01-01 00:00:00.000 +00:00"
	for _, q := range []string{
		`INSERT INTO libraryFolders(id,path,libraryId,createdAt,updatedAt) VALUES('f2','/books-2','l1','` + now + `','` + now + `')`,
		`INSERT INTO books(id,title,explicit,abridged,duration,narrators,audioFiles,ebookFile,chapters,tags,genres,createdAt,updatedAt) VALUES('b2','Second',0,0,1,'[]','[]','null','[]','[]','[]','` + now + `','` + now + `')`,
		`INSERT INTO libraryItems(id,ino,path,relPath,mediaId,mediaType,isFile,isMissing,isInvalid,libraryFiles,extraData,size,title,titleIgnorePrefix,authorNamesFirstLast,authorNamesLastFirst,createdAt,updatedAt,libraryId,libraryFolderId) VALUES('i2','ino2','/books-2/Second','Second','b2','book',0,0,0,'[]','{}',1,'Second','Second','','','` + now + `','` + now + `','l1','f2')`,
	} {
		if _, err := r.db.Exec(q); err != nil {
			t.Fatalf("%v\n%s", err, q)
		}
	}
}

func TestLibraryUpdateFolderRemovalRetainsKeptFolder(t *testing.T) {
	rig := newWriteRig(t)
	rig.seedFolderItem(t)
	rig.seedSecondFolderItem(t)
	w := rig.do(http.MethodPatch, "/api/libraries/l1", `{"folders":[{"id":"f2"}]}`, seriesRootUser())
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	var folders, keptItems, removedItems int
	if err := rig.db.QueryRow(`SELECT COUNT(*) FROM libraryFolders WHERE libraryId='l1'`).Scan(&folders); err != nil {
		t.Fatal(err)
	}
	if err := rig.db.QueryRow(`SELECT COUNT(*) FROM libraryItems WHERE libraryFolderId='f2'`).Scan(&keptItems); err != nil {
		t.Fatal(err)
	}
	if err := rig.db.QueryRow(`SELECT COUNT(*) FROM libraryItems WHERE libraryFolderId='f1'`).Scan(&removedItems); err != nil {
		t.Fatal(err)
	}
	if folders != 1 || keptItems != 1 || removedItems != 0 || rig.changed != 1 || eventCount(rig.events, "library_updated") != 1 {
		t.Fatalf("folders=%d kept=%d removed=%d changed=%d events=%v", folders, keptItems, removedItems, rig.changed, rig.events)
	}
}

func TestLibraryUpdateFolderRemovalRequiresWiring(t *testing.T) {
	rig := newWriteRig(t)
	rig.seedFolderItem(t)
	emit := func(_, event string, _ any) { rig.events = append(rig.events, event) }
	mux := http.NewServeMux()
	New(rig.db, emit, Deps{
		Filters:        filterdata.NewCache(),
		LibraryChanged: func(string) { rig.changed++ },
	}).Routes(mux)
	req := httptest.NewRequest(http.MethodPatch, "/api/libraries/l1", strings.NewReader(`{"folders":[]}`))
	req = req.WithContext(store.ContextWithUser(req.Context(), seriesRootUser()))
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	if rig.changed != 0 || eventCount(rig.events, "library_updated") != 0 {
		t.Fatalf("changed=%d events=%v", rig.changed, rig.events)
	}
	var folders, items int
	if err := rig.db.QueryRow(`SELECT COUNT(*) FROM libraryFolders WHERE libraryId='l1'`).Scan(&folders); err != nil {
		t.Fatal(err)
	}
	if err := rig.db.QueryRow(`SELECT COUNT(*) FROM libraryItems WHERE libraryId='l1'`).Scan(&items); err != nil {
		t.Fatal(err)
	}
	if folders != 1 || items != 1 {
		t.Fatalf("folders=%d items=%d", folders, items)
	}
}
