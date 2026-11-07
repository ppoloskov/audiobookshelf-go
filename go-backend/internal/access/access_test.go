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

func TestDecisionsMatchRecordedHelpers(t *testing.T) {
	m, d := load(t)
	check := func(name, key string, got bool) {
		t.Helper()
		if want := d[name][key]; want != got {
			t.Errorf("%s[%s] = %v, recorded %v", name, key, got, want)
		}
	}
	for ui, u := range m.Users {
		user := store.User{Type: u.Type, IsActive: u.Active}
		if u.Permissions != nil {
			user.Permissions = json.RawMessage(*u.Permissions)
		}
		p := Of(user)
		k := fmt.Sprint(ui)
		for _, lib := range []string{"lib1", "lib2", "lib3"} {
			key := k + ":" + lib
			for _, name := range []string{"catalogLibrary", "userapiLibrary", "shareLibrary", "rtLibrary"} {
				check(name, key, p.Library(lib))
			}
			for _, name := range []string{"podLibrary", "emailLibrary", "adminLibrary", "audioLibrary"} {
				check(name, key, p.IsAdmin() || p.Library(lib))
			}
			check("catalogList", key, p.ListLibrary(lib))
		}
		for ii, it := range m.Items {
			key := k + ":" + fmt.Sprint(ii)
			a := libitem.Access{LibraryID: it.Lib, Explicit: it.Explicit, Tags: it.Tags}
			for _, name := range []string{"catalogItem", "libitemAllowed", "playAccess", "rtItem"} {
				check(name, key, p.Item(a))
			}
			check("podAccess", key, p.IsAdmin() || p.Item(a))
			check("emailContent", key, p.Content(a))
		}
		check("catalogAuthUpdate", k, p.Update())
		check("catalogAuthDelete", k, p.Delete())
		check("catalogAuthUpload", k, p.Upload())
		check("catalogAuthDownload", k, p.Download())
		check("adminUpload", k, p.Upload())
		// Moved toward TS (User.js:560-571, `!!flag && isActive`): the helpers
		// recorded for these two took only literal true and ignored isActive.
		if loose := u.Permissions != nil && strings.Contains(*u.Permissions, `"yes"`); !loose {
			check("playDelete", k, p.Delete())
			if u.Active {
				check("podUpdate", k, p.IsAdmin() || p.Update())
			}
		}
		check("catalogAdminUser", k, p.Active && p.IsAdmin())
		clause, args := p.MediaClause("b")
		if got, want := visible(t, m, clause, args), fmt.Sprint(d["catalogSQL"][k]); fmt.Sprint(got) != want {
			t.Errorf("MediaClause user %d = %v, recorded %v", ui, got, want)
		}
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
