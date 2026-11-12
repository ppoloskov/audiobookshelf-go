package libraries

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"audiobookshelf-compat/internal/auth"
	"audiobookshelf-compat/internal/db"
	"audiobookshelf-compat/internal/db/dbtest"
	"audiobookshelf-compat/internal/filterdata"
	"audiobookshelf-compat/internal/item"
	"audiobookshelf-compat/internal/settings"
)

type rig struct {
	t       *testing.T
	d       *db.DB
	mux     *http.ServeMux
	mu      sync.Mutex
	names   []string
	changed int
	matched []string
}

func newRig(t *testing.T) *rig {
	t.Helper()
	r := &rig{t: t, d: dbtest.Fixture(t), mux: http.NewServeMux()}
	store, err := settings.NewStore(context.Background(), r.d, func(string) string { return "" }, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	filters := filterdata.NewCache()
	emit := func(_, name string, _ any) { r.mu.Lock(); r.names = append(r.names, name); r.mu.Unlock() }
	items := item.New(r.d, store, emit, item.Deps{Filters: filters, CoverCacheDir: t.TempDir(), ItemMetadataDir: t.TempDir()})
	New(r.d, store, func(_, name string, _ any) { r.mu.Lock(); r.names = append(r.names, name); r.mu.Unlock() }, Deps{
		Filters:        filters,
		LibraryChanged: func(string) { r.changed++ },
		QuickMatch: func(_ context.Context, id string, o item.MatchOptions) (item.MatchResult, error) {
			r.mu.Lock()
			defer r.mu.Unlock()
			r.matched = append(r.matched, id+"/"+o.Provider)
			return item.MatchResult{Updated: db.Ptr(true)}, nil
		},
		Items: items,
	}).Routes(r.mux)
	return r
}

func (r *rig) do(method, target, body, who string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, target, strings.NewReader(body))
	req = req.WithContext(auth.WithUser(req.Context(), auth.NewPrincipal(users[who].row)))
	w := httptest.NewRecorder()
	r.mux.ServeHTTP(w, req)
	return w
}

func (r *rig) count(query string, args ...any) int {
	var n int
	if err := r.d.Get(context.Background(), &n, query, args...); err != nil {
		r.t.Fatal(err)
	}
	return n
}

func (r *rig) eventCount(name string) (n int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, e := range r.names {
		if e == name {
			n++
		}
	}
	return n
}

func TestCreateUpdateRefreshWatchersAfterCommit(t *testing.T) {
	r := newRig(t)
	folder := filepath.Join(t.TempDir(), "new")
	w := r.do("POST", "/api/libraries", `{"name":"New","folders":[{"fullPath":"`+folder+`"}]}`, "root")
	var created map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &created); err != nil || w.Code != 200 {
		t.Fatalf("create %d %s", w.Code, w.Body)
	}
	if _, ok := created["lastScanVersion"]; ok || r.changed != 1 || r.eventCount("library_added") != 1 {
		t.Fatalf("create: %v changed=%d", created, r.changed)
	}
	id := created["id"].(string)
	w = r.do("PATCH", "/api/libraries/"+id, `{"name":"Renamed","settings":{"disableWatcher":true}}`, "root")
	if w.Code != 200 || r.changed != 2 || r.count(`SELECT COUNT(*) FROM libraries WHERE id = ? AND name = 'Renamed' AND settings LIKE '%"disableWatcher":true%'`, id) != 1 {
		t.Fatalf("update %d changed=%d %s", w.Code, r.changed, w.Body)
	}
	// A cron change alone does not refresh; null clears a nullable setting.
	r.do("PATCH", "/api/libraries/"+id, `{"settings":{"autoScanCronExpression":"0 * * * *"}}`, "root")
	r.do("PATCH", "/api/libraries/"+id, `{"settings":{"autoScanCronExpression":null}}`, "root")
	if r.changed != 2 || r.count(`SELECT COUNT(*) FROM libraries WHERE id = ? AND settings LIKE '%"autoScanCronExpression":null%'`, id) != 1 {
		t.Fatalf("changed=%d", r.changed)
	}
	if w := r.do("PATCH", "/api/libraries/"+id, `{"settings":{"disableWatcher":"x"}}`, "root"); w.Code != 400 {
		t.Fatalf("bad setting type %d", w.Code)
	}
	for _, c := range []struct{ method, target, who string }{{"POST", "/api/libraries", "editor"}, {"PATCH", "/api/libraries/" + id, "editor"}, {"DELETE", "/api/libraries/" + id, "editor"}, {"POST", "/api/libraries/order", "editor"}} {
		if w := r.do(c.method, c.target, `{}`, c.who); w.Code != 403 {
			t.Errorf("%s %s by %s: %d", c.method, c.target, c.who, w.Code)
		}
	}
}

// Removing the only folder deletes the folder row and its items, refreshes the
// watchers once and emits library_updated once; repeating it changes nothing.
func TestFolderRemovalOnlyDeletesAndRefreshesOnce(t *testing.T) {
	r := newRig(t)
	for i := 0; i < 2; i++ {
		if w := r.do("PATCH", "/api/libraries/"+bookLib, `{"folders":[]}`, "root"); w.Code != 200 {
			t.Fatalf("pass %d: %d %s", i, w.Code, w.Body)
		}
	}
	if r.count(`SELECT COUNT(*) FROM libraryFolders WHERE libraryId = ?`, bookLib) != 0 || r.count(`SELECT COUNT(*) FROM libraryItems WHERE libraryId = ?`, bookLib) != 0 {
		t.Fatal("folder or items remain")
	}
	if r.changed != 1 || r.eventCount("library_updated") != 1 || r.eventCount("item_removed") != 5 {
		t.Fatalf("changed=%d updated=%d removed=%d", r.changed, r.eventCount("library_updated"), r.eventCount("item_removed"))
	}
}

func TestFolderRemovalFailureKeepsFolder(t *testing.T) {
	r := newRig(t)
	if _, err := r.d.Exec(context.Background(), `CREATE TRIGGER fail_delete BEFORE DELETE ON libraryItems BEGIN SELECT RAISE(ABORT,'test'); END`); err != nil {
		t.Fatal(err)
	}
	if w := r.do("PATCH", "/api/libraries/"+bookLib, `{"folders":[]}`, "root"); w.Code != 500 {
		t.Fatalf("status %d", w.Code)
	}
	if r.count(`SELECT COUNT(*) FROM libraryFolders WHERE libraryId = ?`, bookLib) != 1 || r.changed != 0 || r.eventCount("library_updated") != 0 {
		t.Fatal("a failed removal changed state or announced")
	}
}

func TestDeleteLibraryIsOneTransactionWithEffectsAfter(t *testing.T) {
	r := newRig(t)
	if w := r.do("DELETE", "/api/libraries/"+bookLib, "", "root"); w.Code != 200 {
		t.Fatalf("status %d %s", w.Code, w.Body)
	}
	for _, q := range []string{`SELECT COUNT(*) FROM libraryItems WHERE libraryId = '` + bookLib + `'`, `SELECT COUNT(*) FROM books`, `SELECT COUNT(*) FROM authors`, `SELECT COUNT(*) FROM series`, `SELECT COUNT(*) FROM collections`, `SELECT COUNT(*) FROM playbackSessions WHERE libraryId = '` + bookLib + `'`} {
		if n := r.count(q); n != 0 {
			t.Errorf("%s = %d", q, n)
		}
	}
	if r.count(`SELECT COUNT(*) FROM playbackSessions`) != 7 || r.count(`SELECT displayOrder FROM libraries WHERE id = ?`, podcastLib) != 1 {
		t.Error("sessions must stay without a library; the other library is renumbered to 1")
	}
	if r.eventCount("library_removed") != 1 || r.eventCount("item_removed") != 5 || r.changed != 1 {
		t.Errorf("events %v changed=%d", r.names, r.changed)
	}
	if w := r.do("DELETE", "/api/libraries/"+bookLib, "", "root"); w.Code != 404 {
		t.Errorf("second delete %d", w.Code)
	}
}

func TestOrderIsAllOrNothing(t *testing.T) {
	r := newRig(t)
	if w := r.do("POST", "/api/libraries/order", `[{"id":"`+bookLib+`","newOrder":9},{"id":"nope","newOrder":1}]`, "root"); w.Code != 400 {
		t.Fatalf("status %d", w.Code)
	}
	if r.count(`SELECT displayOrder FROM libraries WHERE id = ?`, bookLib) != 1 {
		t.Error("a failed order changed a library")
	}
}

func TestMatchAllIsAdminOnlyAndFinishesItsTask(t *testing.T) {
	r := newRig(t)
	if w := r.do("GET", "/api/libraries/"+bookLib+"/matchall", "", "listener"); w.Code != 403 {
		t.Fatalf("non-admin %d", w.Code)
	}
	if w := r.do("GET", "/api/libraries/"+bookLib+"/matchall", "", "root"); w.Code != 200 {
		t.Fatalf("admin %d", w.Code)
	}
	for deadline := time.Now().Add(3 * time.Second); r.eventCount("task_finished") == 0; time.Sleep(5 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("task did not finish")
		}
	}
	if len(r.matched) != 5 { // no fixture book has an ASIN or ISBN, so none is skipped
		t.Errorf("matched %v", r.matched)
	}
}

func TestLimitedUserCannotOpenOtherLibraries(t *testing.T) {
	r := newRig(t)
	for _, target := range []string{"", "/stats", "/narrators", "/authors", "/filterdata"} {
		if w := r.do("GET", "/api/libraries/"+bookLib+target, "", "limited"); w.Code != 403 {
			t.Errorf("%s: %d", target, w.Code)
		}
	}
	if w := r.do("GET", "/api/libraries", "", "limited"); !strings.Contains(w.Body.String(), podcastLib) || strings.Contains(w.Body.String(), bookLib) {
		t.Errorf("list: %s", w.Body)
	}
}

type testUser struct{ row db.User }

var users = func() map[string]testUser {
	all := db.Permissions{Download: true, AccessAllLibraries: true, AccessAllTags: true, AccessExplicitContent: true}
	root, editor, limited := all, all, all
	root.Update, root.Delete, root.Upload = true, true, true
	editor.Update = true
	limited.AccessAllLibraries, limited.LibrariesAccessible = false, []string{podcastLib}
	user := func(p db.Permissions, userType, id string) testUser {
		return testUser{db.User{ID: id, Username: db.Ptr(id), Type: db.Ptr(userType), IsActive: true, Permissions: db.JSON[db.Permissions]{V: p}}}
	}
	return map[string]testUser{"root": user(root, "root", "u-root"), "listener": user(all, "user", "u-listener"), "editor": user(editor, "user", "u-editor"), "limited": user(limited, "user", "u-limited")}
}()

const (
	bookLib    = "51bf3bdf-f246-4f7d-b0a6-9059e4d0ed41"
	podcastLib = "61d2b487-8714-4575-ad70-147fd4ee924b"
)

// A failure after the item rows were deleted rolls the whole library delete
// back: every row stays and nothing is announced.
func TestDeleteLibraryRollsBackItemRows(t *testing.T) {
	r := newRig(t)
	if _, err := r.d.Exec(context.Background(), `CREATE TRIGGER fail_lib BEFORE DELETE ON libraries BEGIN SELECT RAISE(ABORT,'test'); END`); err != nil {
		t.Fatal(err)
	}
	if w := r.do("DELETE", "/api/libraries/"+bookLib, "", "root"); w.Code != 500 {
		t.Fatalf("status %d", w.Code)
	}
	if r.count(`SELECT COUNT(*) FROM libraryItems WHERE libraryId = ?`, bookLib) != 5 || r.count(`SELECT COUNT(*) FROM books`) != 5 || r.count(`SELECT COUNT(*) FROM collections`) != 1 || len(r.names) != 0 || r.changed != 0 {
		t.Fatalf("state changed: events %v", r.names)
	}
}
