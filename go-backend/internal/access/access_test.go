package access

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"

	"audiobookshelf-compat/internal/libitem"
	"audiobookshelf-compat/internal/store"

	_ "modernc.org/sqlite"
)

// decisions.json was recorded from the per-package helpers this package
// replaced (catalog, libitem, playback, podcasts, emailapi, realtime, userapi,
// sharing, adminops, audiotools) over testdata/matrix.json: 128 users (root,
// admin, user, guest; active or not; 16 permission documents) by 30 items.

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

func load(t *testing.T) (matrix, map[string]map[string]any) {
	var m matrix
	var d map[string]map[string]any
	for file, into := range map[string]any{"testdata/matrix.json": &m, "testdata/decisions.json": &d} {
		raw, err := os.ReadFile(file)
		if err == nil {
			err = json.Unmarshal(raw, into)
		}
		if err != nil {
			t.Fatal(err)
		}
	}
	return m, d
}

// decide computes every recorded decision of the current rules, keyed like
// testdata/decisions.json. ACCESS_RECORD=1 rewrites that file from it.
func decide(t *testing.T, m matrix) map[string]map[string]any {
	d := map[string]map[string]any{}
	put := func(name, key string, v any) {
		if d[name] == nil {
			d[name] = map[string]any{}
		}
		d[name][key] = v
	}
	for ui, u := range m.Users {
		p := Of(userOf(u.Type, u.Active, u.Permissions))
		k := fmt.Sprint(ui)
		for _, lib := range []string{"lib1", "lib2", "lib3"} {
			key := k + ":" + lib
			for _, name := range []string{"catalogLibrary", "userapiLibrary", "shareLibrary", "rtLibrary"} {
				put(name, key, p.Library(lib))
			}
			for _, name := range []string{"podLibrary", "emailLibrary", "adminLibrary", "audioLibrary"} {
				put(name, key, p.IsAdmin() || p.Library(lib))
			}
			put("catalogList", key, p.ListLibrary(lib))
		}
		for ii, it := range m.Items {
			key := k + ":" + fmt.Sprint(ii)
			a := libitem.Access{LibraryID: it.Lib, Explicit: it.Explicit, Tags: it.Tags}
			for _, name := range []string{"catalogItem", "libitemAllowed", "playAccess", "rtItem"} {
				put(name, key, p.Item(a))
			}
			put("podAccess", key, p.IsAdmin() || p.Item(a))
			put("emailContent", key, p.Content(a))
		}
		for _, name := range []string{"catalogAuthUpdate", "catalogUpdate"} {
			put(name, k, p.Update())
		}
		put("podUpdate", k, p.IsAdmin() || p.Update())
		for _, name := range []string{"catalogAuthDelete", "playDelete"} {
			put(name, k, p.Delete())
		}
		put("podDelete", k, p.IsAdmin() || p.Delete())
		put("catalogAuthUpload", k, p.Upload())
		put("adminUpload", k, p.Upload())
		put("catalogAuthDownload", k, p.Download())
		put("catalogAdminUser", k, p.Active && p.IsAdmin())
		clause, args := p.MediaClause("b")
		put("catalogSQL", k, visible(t, m, clause, args))
		put("podSQL", k, visible(t, m, clause, args)) // MediaClause differs only by alias
	}
	return d
}

func userOf(typ string, active bool, permissions *string) store.User {
	user := store.User{Type: typ, IsActive: active}
	if permissions != nil {
		user.Permissions = json.RawMessage(*permissions)
	}
	return user
}

// TestDecisionsMatchRecorded replays testdata/decisions.json. The file is a
// regression lock only: it was recorded from this package's own rules (first
// from the per-package helpers it replaced, then re-recorded for three
// deliberate changes), so it proves nothing about upstream. The contract is
// carried by TestMediaClauseMatchesContent and by the named tests below, which
// state User.js rules by hand. Re-record with ACCESS_RECORD=1 only after a
// deliberate rule change, and review the diff.
func TestDecisionsMatchRecorded(t *testing.T) {
	m, want := load(t)
	got := decide(t, m)
	if os.Getenv("ACCESS_RECORD") == "1" {
		raw, _ := json.MarshalIndent(got, "", " ")
		if err := os.WriteFile("testdata/decisions.json", append(raw, '\n'), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	for name, cells := range got {
		for key, v := range cells {
			if fmt.Sprint(v) != fmt.Sprint(want[name][key]) {
				t.Errorf("%s[%s] = %v, recorded %v", name, key, v, want[name][key])
			}
		}
	}
}

// TestMediaClauseMatchesContent is the invariant between list queries and
// item reads: the SQL clause selects exactly the media Content allows, for
// every user and item of the matrix.
func TestMediaClauseMatchesContent(t *testing.T) {
	m, _ := load(t)
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
