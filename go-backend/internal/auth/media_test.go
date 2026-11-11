package auth

import (
	"fmt"
	"slices"
	"testing"

	"audiobookshelf-compat/internal/db"
	"audiobookshelf-compat/internal/db/dbtest"
)

const stamp = "2024-01-02 03:04:05.000 +00:00"

func seedBook(t *testing.T, d *db.DB, id string, explicit int, tags string) {
	t.Helper()
	_, err := d.Exec(t.Context(), fmt.Sprintf(`INSERT INTO books (id,title,explicit,abridged,duration,tags,genres,narrators,audioFiles,chapters,createdAt,updatedAt) VALUES (?,?,?,0,1,?,'[]','[]','[]','[]','%s','%s')`, stamp, stamp), id, id, explicit, tags)
	if err != nil {
		t.Fatal(err)
	}
}

func seedItem(t *testing.T, d *db.DB, id, mediaType, mediaID, libraryID string) {
	t.Helper()
	_, err := d.Exec(t.Context(), fmt.Sprintf(`INSERT INTO libraryItems (id,path,relPath,mediaId,mediaType,isFile,isMissing,isInvalid,size,libraryFiles,createdAt,updatedAt,libraryId) VALUES (?,?,?,?,?,0,0,0,1,'[]','%s','%s',?)`, stamp, stamp), id, "/"+id, id, mediaID, mediaType, libraryID)
	if err != nil {
		t.Fatal(err)
	}
}

func TestMediaClauseFiltersRows(t *testing.T) {
	d := dbtest.New(t)
	seedBook(t, d, "plain", 0, `[]`)
	seedBook(t, d, "tagX", 0, `["x"]`)
	seedBook(t, d, "tagXY", 0, `["x","y"]`)
	seedBook(t, d, "tagY", 0, `["y"]`)
	seedBook(t, d, "explicitX", 1, `["x"]`)
	seedBook(t, d, "explicitPlain", 1, `[]`)
	for _, tc := range []struct {
		name   string
		active bool
		perms  db.Permissions
		want   []string
	}{
		{"all tags, no explicit", true, db.Permissions{AccessAllTags: true}, []string{"plain", "tagX", "tagXY", "tagY"}},
		{"all tags, explicit", true, db.Permissions{AccessAllTags: true, AccessExplicitContent: true}, []string{"explicitPlain", "explicitX", "plain", "tagX", "tagXY", "tagY"}},
		{"explicit but inactive", false, db.Permissions{AccessAllTags: true, AccessExplicitContent: true}, []string{"plain", "tagX", "tagXY", "tagY"}},
		{"allow x", true, db.Permissions{ItemTagsSelected: []string{"x"}}, []string{"tagX", "tagXY"}},
		{"allow x and y, explicit", true, db.Permissions{ItemTagsSelected: []string{"x", "y"}, AccessExplicitContent: true}, []string{"explicitX", "tagX", "tagXY", "tagY"}},
		{"deny x", true, db.Permissions{ItemTagsSelected: []string{"x"}, SelectedTagsNotAccessible: true}, []string{"plain", "tagY"}},
		{"empty allow list", true, db.Permissions{}, nil},
		{"empty deny list", true, db.Permissions{SelectedTagsNotAccessible: true}, []string{"plain", "tagX", "tagXY", "tagY"}},
	} {
		clause, args := principal("user", tc.active, tc.perms).MediaClause("b")
		var got []string
		if err := d.Select(t.Context(), &got, `SELECT b.id FROM books b WHERE b.title<>''`+clause+` ORDER BY b.id`, args...); err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		if !slices.Equal(got, tc.want) {
			t.Errorf("%s: got %v, want %v (clause %q)", tc.name, got, tc.want, clause)
		}
		// The clause selects exactly what CanAccessContent admits.
		for _, id := range []string{"plain", "tagX", "tagXY", "tagY", "explicitX", "explicitPlain"} {
			var tags db.JSON[[]string]
			if err := d.Get(t.Context(), &tags, `SELECT tags FROM books WHERE id=?`, id); err != nil {
				t.Fatal(err)
			}
			allowed := principal("user", tc.active, tc.perms).CanAccessContent(id[:min(len(id), 8)] == "explicit", tags.V)
			if allowed != slices.Contains(got, id) {
				t.Errorf("%s: %s allowed=%v but clause selects=%v", tc.name, id, allowed, slices.Contains(got, id))
			}
		}
	}
	var nilPrincipal *Principal
	if clause, _ := nilPrincipal.MediaClause("b"); clause != " AND 0" {
		t.Errorf("nil principal clause %q", clause)
	}
}

func TestReadItem(t *testing.T) {
	d := dbtest.New(t)
	seedBook(t, d, "book1", 0, `["tag"]`)
	seedBook(t, d, "book2", 1, `[]`)
	for _, id := range []string{"lib1", "lib2"} {
		if _, err := d.Exec(t.Context(), `INSERT INTO libraries (id,name,createdAt,updatedAt) VALUES (?,?,?,?)`, id, id, stamp, stamp); err != nil {
			t.Fatal(err)
		}
	}
	seedItem(t, d, "li-book", "book", "book1", "lib1")
	seedItem(t, d, "li-explicit", "book", "book2", "lib1")
	seedItem(t, d, "li-orphan", "podcast", "missing", "lib1")
	reader := principal("user", true, db.Permissions{LibrariesAccessible: []string{"lib1"}, ItemTagsSelected: []string{"tag"}})
	other := principal("user", true, db.Permissions{AccessAllTags: true, LibrariesAccessible: []string{"lib2"}})
	for _, tc := range []struct {
		name           string
		p              *Principal
		id             string
		exists, allows bool
	}{
		{"tagged item", reader, "li-book", true, true},
		{"explicit item", reader, "li-explicit", true, false},
		{"other library", other, "li-book", true, false},
		{"missing media row", reader, "li-orphan", false, false},
		{"missing item", reader, "nope", false, false},
		{"nil principal", nil, "li-book", true, false},
	} {
		access, exists, allowed, err := tc.p.ReadItem(t.Context(), d, tc.id)
		if err != nil || exists != tc.exists || allowed != tc.allows {
			t.Errorf("%s: exists=%v allowed=%v err=%v", tc.name, exists, allowed, err)
		}
		if tc.name == "tagged item" && (access.LibraryID != "lib1" || access.Explicit || !slices.Equal(access.Tags, []string{"tag"})) {
			t.Errorf("access = %+v", access)
		}
	}
}
