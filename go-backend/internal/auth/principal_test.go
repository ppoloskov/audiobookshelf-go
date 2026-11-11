package auth

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"audiobookshelf-compat/internal/db"
)

func principal(userType string, active bool, perms db.Permissions) *Principal {
	return NewPrincipal(db.User{
		ID: "u", Type: &userType, IsActive: db.Bool(active),
		Permissions: db.JSON[db.Permissions]{V: perms},
	})
}

// stored builds a principal from the permissions text of a users row.
func stored(t *testing.T, userType string, active bool, text string) *Principal {
	t.Helper()
	u := db.User{ID: "u", Type: &userType, IsActive: db.Bool(active)}
	if err := u.Permissions.Scan(text); err != nil {
		t.Fatal(err)
	}
	return NewPrincipal(u)
}

func TestTypeDecisions(t *testing.T) {
	for _, tc := range []struct {
		userType        string
		root, admin     bool
		wantAdminAccess bool
	}{
		{"root", true, true, true},
		{"admin", false, true, true},
		{"user", false, false, false},
		{"guest", false, false, false},
		{"", false, false, false},
	} {
		p := principal(tc.userType, true, db.Permissions{})
		if p.IsRoot() != tc.root || p.IsAdmin() != tc.admin {
			t.Errorf("%q: root=%v admin=%v", tc.userType, p.IsRoot(), p.IsAdmin())
		}
	}
	if NewPrincipal(db.User{}).IsAdmin() {
		t.Error("a row without a type is not admin")
	}
}

func TestActionFlagsNeedAnActiveUser(t *testing.T) {
	all := db.Permissions{Update: true, Delete: true, Download: true, Upload: true}
	for _, tc := range []struct {
		name   string
		p      *Principal
		update bool
		del    bool
		down   bool
		up     bool
	}{
		{"active, all flags", principal("user", true, all), true, true, true, true},
		{"inactive, all flags", principal("user", false, all), false, false, false, false},
		{"active, no flags", principal("user", true, db.Permissions{}), false, false, false, false},
		{"download only", principal("guest", true, db.Permissions{Download: true}), false, false, true, false},
		{"nil", nil, false, false, false, false},
	} {
		if tc.p.CanUpdate() != tc.update || tc.p.CanDelete() != tc.del || tc.p.CanDownload() != tc.down || tc.p.CanUpload() != tc.up {
			t.Errorf("%s: update=%v delete=%v download=%v upload=%v", tc.name, tc.p.CanUpdate(), tc.p.CanDelete(), tc.p.CanDownload(), tc.p.CanUpload())
		}
	}
}

func TestLibraryRules(t *testing.T) {
	for _, tc := range []struct {
		name         string
		p            *Principal
		access, list bool // for library "a"
		otherAccess  bool // for library "b"
		otherList    bool
	}{
		{"all libraries", principal("user", true, db.Permissions{AccessAllLibraries: true}), true, true, true, true},
		{"listed library", principal("user", true, db.Permissions{LibrariesAccessible: []string{"a"}}), true, true, false, false},
		{"all libraries and a list: list wins in listing only", principal("user", true, db.Permissions{AccessAllLibraries: true, LibrariesAccessible: []string{"a"}}), true, true, true, false},
		{"nothing", principal("user", true, db.Permissions{}), false, true, false, true},
		{"nil", nil, false, false, false, false},
	} {
		if tc.p.CanAccessLibrary("a") != tc.access || tc.p.CanListLibrary("a") != tc.list ||
			tc.p.CanAccessLibrary("b") != tc.otherAccess || tc.p.CanListLibrary("b") != tc.otherList {
			t.Errorf("%s: access a=%v b=%v, list a=%v b=%v", tc.name,
				tc.p.CanAccessLibrary("a"), tc.p.CanAccessLibrary("b"), tc.p.CanListLibrary("a"), tc.p.CanListLibrary("b"))
		}
	}
}

func TestContentRules(t *testing.T) {
	tags := func(selected ...string) db.Permissions {
		return db.Permissions{ItemTagsSelected: selected}
	}
	denied := func(selected ...string) db.Permissions {
		p := tags(selected...)
		p.SelectedTagsNotAccessible = true
		return p
	}
	for _, tc := range []struct {
		name     string
		perms    db.Permissions
		active   bool
		explicit bool
		itemTags []string
		want     bool
	}{
		{"all tags", db.Permissions{AccessAllTags: true}, true, false, nil, true},
		{"all tags overrides a deny list", db.Permissions{AccessAllTags: true, ItemTagsSelected: []string{"x"}, SelectedTagsNotAccessible: true}, true, false, []string{"x"}, true},
		{"allow list, shared tag", tags("x"), true, false, []string{"y", "x"}, true},
		{"allow list, no shared tag", tags("x"), true, false, []string{"y"}, false},
		{"allow list, untagged item", tags("x"), true, false, nil, false},
		{"empty allow list denies tagged", tags(), true, false, []string{"x"}, false},
		{"empty allow list denies untagged", tags(), true, false, nil, false},
		{"deny list, untagged item", denied("x"), true, false, nil, true},
		{"deny list, denied tag among others", denied("x"), true, false, []string{"y", "x"}, false},
		{"deny list, other tags", denied("x"), true, false, []string{"y"}, true},
		{"empty deny list admits all", denied(), true, false, []string{"y"}, true},
		{"explicit without permission", db.Permissions{AccessAllTags: true}, true, true, nil, false},
		{"explicit with permission", db.Permissions{AccessAllTags: true, AccessExplicitContent: true}, true, true, nil, true},
		{"explicit with permission but inactive", db.Permissions{AccessAllTags: true, AccessExplicitContent: true}, false, true, nil, false},
		{"not explicit, inactive", db.Permissions{AccessAllTags: true}, false, false, nil, true},
	} {
		p := principal("user", tc.active, tc.perms)
		if got := p.CanAccessContent(tc.explicit, tc.itemTags); got != tc.want {
			t.Errorf("%s: CanAccessContent = %v, want %v", tc.name, got, tc.want)
		}
	}
	var nilPrincipal *Principal
	if nilPrincipal.CanAccessContent(false, nil) || nilPrincipal.CanAccessItem("a", false, nil) {
		t.Error("nil principal must be denied")
	}
}

func TestCanAccessItemCombinesLibraryAndContent(t *testing.T) {
	p := principal("user", true, db.Permissions{LibrariesAccessible: []string{"a"}, AccessAllTags: true})
	if !p.CanAccessItem("a", false, nil) || p.CanAccessItem("b", false, nil) || p.CanAccessItem("a", true, nil) {
		t.Error("an item needs its library and its content to be allowed")
	}
}

func TestMalformedStoredPermissionsGrantNothing(t *testing.T) {
	for _, doc := range []string{
		`{"accessAllLibraries": true, "accessAllTags": true, "update": true, "librariesAccessible": "lib1"}`,
		`{"accessAllLibraries": true, "accessAllTags": "yes", "update": true}`,
		`{"accessAllLibraries": true,`,
		`[]`,
		`null`,
		``,
	} {
		p := stored(t, "user", true, doc)
		clause, _ := p.MediaClause("b")
		if p.CanAccessLibrary("lib1") || p.CanUpdate() || p.CanAccessContent(false, []string{"a"}) || p.CanAccessContent(false, nil) || clause != " AND b.explicit=0 AND 0" {
			t.Errorf("%q granted access: %+v, clause %q", doc, p.Permissions.V, clause)
		}
	}
}

func TestDefaultPermissions(t *testing.T) {
	for _, tc := range []struct {
		userType string
		want     string
	}{
		{"root", `{"download":true,"update":true,"delete":true,"upload":true,"createEreader":true,"accessAllLibraries":true,"accessAllTags":true,"accessExplicitContent":true,"selectedTagsNotAccessible":false,"librariesAccessible":[],"itemTagsSelected":[]}`},
		{"admin", `{"download":true,"update":true,"delete":false,"upload":true,"createEreader":true,"accessAllLibraries":true,"accessAllTags":true,"accessExplicitContent":true,"selectedTagsNotAccessible":false,"librariesAccessible":[],"itemTagsSelected":[]}`},
		{"user", `{"download":true,"update":false,"delete":false,"upload":false,"createEreader":false,"accessAllLibraries":true,"accessAllTags":true,"accessExplicitContent":false,"selectedTagsNotAccessible":false,"librariesAccessible":[],"itemTagsSelected":[]}`},
		{"guest", `{"download":true,"update":false,"delete":false,"upload":false,"createEreader":false,"accessAllLibraries":true,"accessAllTags":true,"accessExplicitContent":false,"selectedTagsNotAccessible":false,"librariesAccessible":[],"itemTagsSelected":[]}`},
	} {
		got, err := json.Marshal(DefaultPermissions(tc.userType))
		if err != nil || string(got) != tc.want {
			t.Errorf("%s: %s (%v)", tc.userType, got, err)
		}
	}
}

func TestUserContext(t *testing.T) {
	ctx := t.Context()
	if User(ctx) != nil {
		t.Fatal("a plain context has no principal")
	}
	p := principal("user", true, db.Permissions{})
	if User(WithUser(ctx, p)) != p {
		t.Fatal("WithUser must carry the principal")
	}
}

func TestAdminMiddleware(t *testing.T) {
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) })
	for _, tc := range []struct {
		name string
		p    *Principal
		want int
	}{
		{"root", principal("root", true, db.Permissions{}), 200},
		{"admin", principal("admin", true, db.Permissions{}), 200},
		{"user", principal("user", true, db.Permissions{}), 403},
		{"guest", principal("guest", true, db.Permissions{}), 403},
		{"inactive admin", principal("admin", false, db.Permissions{}), 403},
		{"unauthenticated", nil, 403},
	} {
		r := httptest.NewRequest(http.MethodGet, "/", nil)
		if tc.p != nil {
			r = r.WithContext(WithUser(r.Context(), tc.p))
		}
		w := httptest.NewRecorder()
		Admin(next).ServeHTTP(w, r)
		if w.Code != tc.want {
			t.Errorf("%s: status %d, want %d", tc.name, w.Code, tc.want)
		}
	}
}
