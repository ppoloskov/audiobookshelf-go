package library

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	schemaassets "audiobookshelf-compat/contracts/parity"
	"audiobookshelf-compat/internal/filterdata"
	"audiobookshelf-compat/internal/store"
)

type writeRig struct {
	t       *testing.T
	db      *sql.DB
	mux     *http.ServeMux
	changed int
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
	rig.mux = http.NewServeMux()
	New(db, nil, Deps{
		Filters:        filterdata.NewCache(),
		LibraryChanged: func(string) { rig.changed++ },
		RemoveFolderItems: func(_ context.Context, _, _ string) error {
			return nil
		},
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
