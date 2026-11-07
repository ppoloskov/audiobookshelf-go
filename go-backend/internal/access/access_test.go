package access

import (
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

// TestDecisionsMatchRecorded replays testdata/decisions.json. It was first
// recorded from the per-package helpers this package replaced, then
// re-recorded once (docs/SIMPLIFICATION.md, M7 fixes) for three deliberate
// changes, each also pinned by its own test below: a permissions document
// that does not decode completely grants nothing; explicit items need an
// active user; an empty allow list hides every item from SQL too.
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

func TestContentAllowlistPartialOverlap(t *testing.T) {
	permissions, _ := json.Marshal(map[string]any{"accessExplicitContent": true, "itemTagsSelected": []string{"x"}})
	p := Of(store.User{IsActive: true, Permissions: permissions})
	if !p.Content(libitem.Access{Tags: []string{"x", "y"}}) {
		t.Fatal("allowlist must accept an item with at least one selected tag")
	}
	if p.Content(libitem.Access{Tags: []string{"y"}}) {
		t.Fatal("allowlist must reject an item with no selected tag")
	}
}
