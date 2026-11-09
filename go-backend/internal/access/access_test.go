package access

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"audiobookshelf-compat/internal/httputil"
	"audiobookshelf-compat/internal/libitem"
	"audiobookshelf-compat/internal/store"

	_ "modernc.org/sqlite"
)

// testdata/matrix.json is the input of TestMediaClauseMatchesContent: 128
// users (root, admin, user, guest; active or not; 16 permission documents) by
// 30 items. It carries no expected values; the SQL clause is checked against
// Content, and Content against the hand-written cases below.

type matrix struct {
	Users []struct {
		Type        string
		Active      bool
		Permissions *string
	}
	Items []struct {
		Lib      string
		Explicit bool
		Tags     []string
	}
}

func load(t *testing.T) matrix {
	var m matrix
	raw, err := os.ReadFile("testdata/matrix.json")
	if err == nil {
		err = json.Unmarshal(raw, &m)
	}
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func userOf(typ string, active bool, permissions *string) store.User {
	user := store.User{Type: typ, IsActive: active}
	if permissions != nil {
		user.Permissions = json.RawMessage(*permissions)
	}
	return user
}

// TestMediaClauseMatchesContent is the invariant between list queries and
// item reads: the SQL clause selects exactly the media Content allows, for
// every user and item of the matrix.
func TestMediaClauseMatchesContent(t *testing.T) {
	m := load(t)
	for ui, u := range m.Users {
		p := Of(userOf(u.Type, u.Active, u.Permissions))
		clause, args := p.MediaClause("b")
		selected := map[float64]bool{}
		for _, i := range visible(t, m, clause, args) {
			selected[i.(float64)] = true
		}
		for ii, it := range m.Items {
			allowed := p.Content(libitem.Access{Explicit: it.Explicit, Tags: it.Tags})
			if selected[float64(ii)] != allowed {
				t.Errorf("user %d item %d: SQL selects %v, Content allows %v", ui, ii, selected[float64(ii)], allowed)
			}
		}
	}
}

func TestMalformedPermissionsGrantNothing(t *testing.T) {
	for _, doc := range []string{
		`{"accessAllLibraries": true, "accessAllTags": true, "update": true, "librariesAccessible": "lib1"}`,
		`{"accessAllLibraries": true, "accessAllTags": "yes"}`,
		`{"accessAllLibraries": true,`,
		`[]`,
	} {
		p := Of(store.User{Type: "user", IsActive: true, Permissions: json.RawMessage(doc)})
		if p.Library("lib1") || p.Update() || p.Content(libitem.Access{Tags: []string{"a"}}) {
			t.Errorf("%s: malformed permissions granted access: %+v", doc, p)
		}
		if clause, _ := p.MediaClause("b"); !strings.Contains(clause, "AND 0") {
			t.Errorf("%s: malformed permissions must hide all media, clause %q", doc, clause)
		}
	}
}

func TestExplicitNeedsActiveUser(t *testing.T) {
	doc := json.RawMessage(`{"accessAllLibraries": true, "accessAllTags": true, "accessExplicitContent": true}`)
	explicit := libitem.Access{LibraryID: "lib1", Explicit: true}
	if !Of(store.User{IsActive: true, Permissions: doc}).Item(explicit) {
		t.Fatal("active user with accessExplicitContent must see explicit items")
	}
	if Of(store.User{IsActive: false, Permissions: doc}).Item(explicit) {
		t.Fatal("inactive user must not see explicit items (User.canAccessExplicitContent)")
	}
}

func TestEmptyAllowListHidesEverything(t *testing.T) {
	p := Of(store.User{IsActive: true, Permissions: json.RawMessage(`{"accessAllLibraries": true, "accessAllTags": false, "itemTagsSelected": [], "selectedTagsNotAccessible": false}`)})
	if p.Content(libitem.Access{Tags: []string{"a"}}) || p.Content(libitem.Access{}) {
		t.Fatal("empty allow list must deny every item")
	}
	if clause, _ := p.MediaClause("b"); !strings.Contains(clause, "AND 0") {
		t.Fatalf("empty allow list must hide every item from list queries, clause %q", clause)
	}
}

func visible(t *testing.T, m matrix, clause string, args []any) []any {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	db.Exec(`CREATE TABLE b(i INTEGER, explicit INTEGER, tags TEXT)`)
	for i, it := range m.Items {
		tags, _ := json.Marshal(append([]string{}, it.Tags...))
		db.Exec(`INSERT INTO b VALUES(?,?,?)`, i, it.Explicit, string(tags))
	}
	rows, err := db.Query(`SELECT i FROM b b WHERE 1=1`+clause+` ORDER BY i`, args...)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	out := []any{}
	for rows.Next() {
		var i float64
		rows.Scan(&i)
		out = append(out, i)
	}
	return out
}

func perms(typ string, active bool, doc string) Permissions {
	return Of(store.User{ID: "u", Type: typ, IsActive: active, Permissions: json.RawMessage(doc)})
}

// User.checkCanAccessLibrary (server/models/User.js:641-645).
func TestLibraryRule(t *testing.T) {
	if !perms("user", true, `{"accessAllLibraries":true}`).Library("any") {
		t.Error("accessAllLibraries grants every library")
	}
	p := perms("user", true, `{"accessAllLibraries":false,"librariesAccessible":["a"]}`)
	if !p.Library("a") || p.Library("b") {
		t.Error("a listed library is the only one accessible")
	}
	if perms("user", true, `{"accessAllLibraries":false}`).Library("a") {
		t.Error("no list and not all libraries: nothing")
	}
}

// LibraryController.findAll filters only by a non-empty librariesAccessible.
func TestListLibraryIgnoresAccessAllLibraries(t *testing.T) {
	if !perms("user", true, `{"accessAllLibraries":false}`).ListLibrary("x") {
		t.Error("an empty list restricts nothing in the library list")
	}
	p := perms("user", true, `{"accessAllLibraries":true,"librariesAccessible":["a"]}`)
	if !p.ListLibrary("a") || p.ListLibrary("b") {
		t.Error("a non-empty list restricts the library list even with accessAllLibraries")
	}
}

// User.checkCanAccessLibraryItemWithTags (User.js:658-669), every branch.
func TestTagRule(t *testing.T) {
	for _, tc := range []struct {
		name    string
		doc     string
		itemTag []string
		want    bool
	}{
		{"all tags", `{"accessAllTags":true}`, nil, true},
		{"all tags overrides the list", `{"accessAllTags":true,"itemTagsSelected":["x"],"selectedTagsNotAccessible":true}`, []string{"x"}, true},
		{"allow list, item shares a tag", `{"itemTagsSelected":["x"]}`, []string{"x", "y"}, true},
		{"allow list, no shared tag", `{"itemTagsSelected":["x"]}`, []string{"y"}, false},
		{"allow list, untagged item", `{"itemTagsSelected":["x"]}`, nil, false},
		{"empty allow list", `{"itemTagsSelected":[]}`, []string{"x"}, false},
		{"deny list, untagged item", `{"itemTagsSelected":["x"],"selectedTagsNotAccessible":true}`, nil, true},
		{"deny list, item has a denied tag among others", `{"itemTagsSelected":["x"],"selectedTagsNotAccessible":true}`, []string{"y", "x"}, false},
		{"deny list, item has other tags only", `{"itemTagsSelected":["x"],"selectedTagsNotAccessible":true}`, []string{"y"}, true},
		{"empty deny list admits everything", `{"itemTagsSelected":[],"selectedTagsNotAccessible":true}`, []string{"y"}, true},
	} {
		if got := perms("user", true, tc.doc).Content(libitem.Access{Tags: tc.itemTag}); got != tc.want {
			t.Errorf("%s: Content = %v, want %v", tc.name, got, tc.want)
		}
	}
}

// User.canDelete/canUpdate/canDownload/canUpload: the flag is JS-truthy and the
// user active (User.js:560-571).
func TestActionFlagsNeedAnActiveUserAndTruthyFlag(t *testing.T) {
	doc := `{"update":true,"delete":1,"download":"yes","upload":false}`
	active, inactive := perms("user", true, doc), perms("user", false, doc)
	if !active.Update() || !active.Delete() || !active.Download() || active.Upload() {
		t.Errorf("active user: update=%v delete=%v download=%v upload=%v", active.Update(), active.Delete(), active.Download(), active.Upload())
	}
	if inactive.Update() || inactive.Delete() || inactive.Download() || inactive.Upload() {
		t.Error("an inactive user has no action permission")
	}
	if perms("admin", true, `{}`).Update() {
		t.Error("admin type alone grants no flag; the permissions document decides")
	}
}

func TestIsAdminIsRootOrAdmin(t *testing.T) {
	for typ, want := range map[string]bool{"root": true, "admin": true, "user": false, "guest": false, "": false} {
		if got := perms(typ, true, `{}`).IsAdmin(); got != want {
			t.Errorf("IsAdmin(%q) = %v", typ, got)
		}
		if got := AdminUser(store.User{Type: typ}); got != want {
			t.Errorf("AdminUser(%q) = %v", typ, got)
		}
	}
}

// The request snapshot exists so a request decodes a user's permissions once.
// Reuse shows as no allocation; a different user, or the same user with a
// changed permissions document, is decoded afresh and never served stale.
func TestRequestSnapshotIsReusedOnlyForTheSameUserRow(t *testing.T) {
	user := store.User{ID: "u1", Type: "user", IsActive: true, Permissions: json.RawMessage(`{"accessAllLibraries":false,"librariesAccessible":["a"],"update":true}`)}
	ctx := WithRequest(context.Background(), user)
	if allocs := testing.AllocsPerRun(50, func() { _ = For(ctx, user) }); allocs != 0 {
		t.Errorf("For on the request's own user allocated %v times: the snapshot was not reused", allocs)
	}
	if allocs := testing.AllocsPerRun(50, func() { _ = Of(user) }); allocs == 0 {
		t.Fatal("Of is expected to allocate; the reuse check above proves nothing")
	}
	if !For(ctx, user).Library("a") {
		t.Fatal("snapshot lost the user's permissions")
	}
	changed := user
	changed.Permissions = json.RawMessage(`{"accessAllLibraries":false,"librariesAccessible":["b"]}`)
	if p := For(ctx, changed); p.Library("a") || !p.Library("b") {
		t.Error("a changed permissions document must be decoded afresh, not served from the snapshot")
	}
	other := store.User{ID: "u2", Type: "user", IsActive: true, Permissions: json.RawMessage(`{"accessAllLibraries":true}`)}
	if !For(ctx, other).Library("zzz") {
		t.Error("another user must not get the request user's permissions")
	}
	if For(context.Background(), user).Update() != true {
		t.Error("without a snapshot For decodes the row")
	}
}

// User.checkCanAccessLibraryItem (User.js:648-656) joins the library rule with
// the explicit and tag rules: every one must pass.
func TestItemNeedsLibraryExplicitAndTagRules(t *testing.T) {
	const doc = `{"accessAllLibraries":false,"librariesAccessible":["lib1"],"accessAllTags":false,"itemTagsSelected":["ok"],"accessExplicitContent":false}`
	for _, tc := range []struct {
		name string
		item libitem.Access
		want bool
	}{
		{"listed library, allowed tag", libitem.Access{LibraryID: "lib1", Tags: []string{"ok"}}, true},
		{"other library", libitem.Access{LibraryID: "lib2", Tags: []string{"ok"}}, false},
		{"disallowed tag", libitem.Access{LibraryID: "lib1", Tags: []string{"no"}}, false},
		{"explicit item without permission", libitem.Access{LibraryID: "lib1", Explicit: true, Tags: []string{"ok"}}, false},
	} {
		if got := perms("user", true, doc).Item(tc.item); got != tc.want {
			t.Errorf("%s: Item = %v, want %v", tc.name, got, tc.want)
		}
	}
	open := `{"accessAllLibraries":true,"accessAllTags":true,"accessExplicitContent":true}`
	if !perms("user", true, open).Item(libitem.Access{LibraryID: "any", Explicit: true}) {
		t.Error("an unrestricted active user reads explicit items")
	}
	if perms("guest", false, open).Item(libitem.Access{LibraryID: "any", Explicit: true}) {
		t.Error("a deactivated user reads no explicit item even with every flag set")
	}
}

// Admin routes answer the configured status to everyone but
// root and admin, including a request without a user, and never run the handler.
func TestRequireAdminDeniesNonAdminsAndMissingUser(t *testing.T) {
	for _, tc := range []struct {
		typ  string
		user bool
		want int
	}{{"root", true, 204}, {"admin", true, 204}, {"user", true, 404}, {"guest", true, 404}, {"", false, 404}} {
		ran := false
		h := RequireAdmin(http.StatusNotFound, func(w http.ResponseWriter, r *http.Request) error {
			ran = true
			w.WriteHeader(http.StatusNoContent)
			return nil
		})
		r := httptest.NewRequest("GET", "/", nil)
		if tc.user {
			r = r.WithContext(store.ContextWithUser(r.Context(), store.User{ID: "u", Type: tc.typ, IsActive: true}))
		}
		w := httptest.NewRecorder()
		httputil.Handler(h).ServeHTTP(w, r)
		if w.Code != tc.want || ran != (tc.want == 204) {
			t.Errorf("type %q (user=%v): status %d ran=%v, want %d", tc.typ, tc.user, w.Code, ran, tc.want)
		}
	}
}
