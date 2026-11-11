// TEMPORARY: parity with internal/access, delete with it.

package auth

import (
	"encoding/json"
	"fmt"
	"slices"
	"testing"

	"audiobookshelf-compat/internal/access"
	"audiobookshelf-compat/internal/db"
	"audiobookshelf-compat/internal/db/dbtest"
	"audiobookshelf-compat/internal/store"
)

// parityDocs is ~200 users: type x active x permission document.
func parityDocs() (docs []string) {
	bools := []string{"true", "false"}
	for _, all := range bools {
		for _, tags := range bools {
			for _, explicit := range bools {
				for _, denied := range bools {
					for _, list := range []string{`[]`, `["x"]`, `["x","y"]`} {
						docs = append(docs, fmt.Sprintf(`{"accessAllLibraries":%s,"accessAllTags":%s,"accessExplicitContent":%s,"selectedTagsNotAccessible":%s,"itemTagsSelected":%s,"librariesAccessible":%s,"update":%s,"delete":%s,"download":%s,"upload":%s}`,
							all, tags, explicit, denied, list, list, explicit, tags, denied, all))
					}
				}
			}
		}
	}
	return append(docs, `{}`, `null`, `[]`, `bad`, `{"update":true,"librariesAccessible":"x"}`)
}

func bothPrincipals(userType string, active bool, doc string) (*Principal, access.Permissions) {
	old := access.Of(store.User{Type: userType, IsActive: active, Permissions: json.RawMessage(doc)})
	u := db.User{ID: "u", Type: &userType, IsActive: db.Bool(active)}
	_ = u.Permissions.Scan(doc) // malformed reads as zero, as in a row scan
	return NewPrincipal(u), old
}

func TestParityDecisions(t *testing.T) {
	itemTags := [][]string{nil, {"x"}, {"y"}, {"x", "y"}, {"z"}}
	n := 0
	for _, doc := range parityDocs() {
		for _, userType := range []string{"root", "admin", "user", "guest"} {
			for _, active := range []bool{true, false} {
				p, old := bothPrincipals(userType, active, doc)
				n++
				if p.IsAdmin() != old.IsAdmin() || p.CanUpdate() != old.Update() || p.CanDelete() != old.Delete() ||
					p.CanDownload() != old.Download() || p.CanUpload() != old.Upload() {
					t.Fatalf("%s %v %s: flags differ", userType, active, doc)
				}
				for _, lib := range []string{"x", "other"} {
					if p.CanAccessLibrary(lib) != old.Library(lib) || p.CanListLibrary(lib) != old.ListLibrary(lib) {
						t.Fatalf("%s %v %s: library %s differs", userType, active, doc, lib)
					}
					for _, explicit := range []bool{false, true} {
						for _, tags := range itemTags {
							a := access.Access{LibraryID: lib, Explicit: explicit, Tags: tags}
							if p.CanAccessItem(lib, explicit, tags) != old.Item(a) || p.CanAccessContent(explicit, tags) != old.Content(a) {
								t.Fatalf("%s %v %s: item %+v differs", userType, active, doc, a)
							}
						}
					}
				}
			}
		}
	}
	t.Logf("%d user combinations compared", n)
}

func TestParityFixtureUsers(t *testing.T) {
	d := dbtest.Fixture(t)
	var users []db.User
	if err := d.Select(t.Context(), &users, `SELECT * FROM users`); err != nil || len(users) == 0 {
		t.Fatalf("fixture users: %d, %v", len(users), err)
	}
	for _, u := range users {
		var doc string
		if err := d.Get(t.Context(), &doc, `SELECT COALESCE(permissions,'') FROM users WHERE id=?`, u.ID); err != nil {
			t.Fatal(err)
		}
		old := access.Of(store.User{Type: db.Val(u.Type), IsActive: bool(u.IsActive), Permissions: json.RawMessage(doc)})
		p := NewPrincipal(u)
		if p.IsAdmin() != old.IsAdmin() || p.CanUpdate() != old.Update() || p.CanDelete() != old.Delete() ||
			p.CanDownload() != old.Download() || p.CanUpload() != old.Upload() ||
			p.CanAccessLibrary("any") != old.Library("any") || p.CanAccessContent(true, []string{"x"}) != old.Content(access.Access{Explicit: true, Tags: []string{"x"}}) {
			t.Errorf("fixture user %s differs", u.ID)
		}
	}
}

func TestParityMediaClause(t *testing.T) {
	d := dbtest.New(t)
	for i, tags := range []string{`[]`, `["x"]`, `["y"]`, `["x","y"]`, `["z"]`} {
		seedBook(t, d, fmt.Sprintf("a%d", i), 0, tags)
		seedBook(t, d, fmt.Sprintf("e%d", i), 1, tags)
	}
	ids := func(clause string, args []any, expand bool) []string {
		var got []string
		query := `SELECT b.id FROM books b WHERE b.title<>''` + clause + ` ORDER BY b.id`
		if expand {
			if err := d.Select(t.Context(), &got, query, args...); err != nil {
				t.Fatal(err)
			}
			return got
		}
		rows, err := d.SQL().QueryContext(t.Context(), query, args...)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				t.Fatal(err)
			}
			got = append(got, id)
		}
		return got
	}
	for _, doc := range parityDocs() {
		for _, active := range []bool{true, false} {
			p, old := bothPrincipals("user", active, doc)
			oldClause, oldArgs := old.MediaClause("b")
			newClause, newArgs := p.MediaClause("b")
			if want, got := ids(oldClause, oldArgs, false), ids(newClause, newArgs, true); !slices.Equal(want, got) {
				t.Fatalf("%v %s: old %v new %v", active, doc, want, got)
			}
		}
	}
}
