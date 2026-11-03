package catalog

import (
	"archive/zip"
	"bytes"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"audiobookshelf-compat/internal/store"
)

// testdata/opml-golden.json holds what opmlGenerator.generate
// (server/utils/generators/opmlGenerator.js) returns for each podcast list,
// produced by running the pinned JavaScript.
func TestGenerateOPMLMatchesUpstream(t *testing.T) {
	raw, err := os.ReadFile("testdata/opml-golden.json")
	if err != nil {
		t.Fatal(err)
	}
	var golden map[string]struct {
		Podcasts []struct {
			Title         *string `json:"title"`
			FeedURL       *string `json:"feedURL"`
			Description   *string `json:"description"`
			ItunesPageURL *string `json:"itunesPageURL"`
			Language      *string `json:"language"`
		} `json:"podcasts"`
		XML string `json:"xml"`
	}
	if err := json.Unmarshal(raw, &golden); err != nil {
		t.Fatal(err)
	}
	ns := func(s *string) sql.NullString {
		if s == nil {
			return sql.NullString{}
		}
		return sql.NullString{String: *s, Valid: true}
	}
	if len(golden) < 6 {
		t.Fatalf("golden has %d cases", len(golden))
	}
	for name, c := range golden {
		var podcasts []opmlPodcast
		for _, p := range c.Podcasts {
			podcasts = append(podcasts, opmlPodcast{title: ns(p.Title), feedURL: ns(p.FeedURL), description: ns(p.Description), language: ns(p.Language)})
		}
		if got := generateOPML(podcasts); got != c.XML {
			t.Errorf("%s:\n got %q\nwant %q", name, got, c.XML)
		}
	}
}

func TestLibraryOPMLRoute(t *testing.T) {
	rig := newAuthorRig(t, nil)
	if _, err := rig.db.Exec(`CREATE TABLE podcasts (id TEXT PRIMARY KEY,title TEXT,feedURL TEXT,description TEXT,itunesPageURL TEXT,language TEXT,explicit BOOLEAN,tags JSON)`); err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		`INSERT INTO podcasts VALUES ('p1','Cast','http://x/feed','About','http://x/page','en',0,'[]')`,
		`INSERT INTO podcasts VALUES ('p2','Explicit Cast','http://y/feed',NULL,NULL,NULL,1,'[]')`,
		`INSERT INTO libraryItems VALUES ('pi1','ino-p1','/p/1','1','p1','podcast',0,0,0,'2024-01-01T00:00:00Z','2024-01-01T00:00:00Z','2024-01-01T00:00:00Z',1,NULL,NULL,'[]','{}','Cast','Cast','','','l2','f1','2024-01-01T00:00:00Z','2024-01-01T00:00:00Z')`,
		`INSERT INTO libraryItems VALUES ('pi2','ino-p2','/p/2','2','p2','podcast',0,0,0,'2024-01-01T00:00:00Z','2024-01-01T00:00:00Z','2024-01-01T00:00:00Z',1,NULL,NULL,'[]','{}','Explicit Cast','Explicit Cast','','','l2','f1','2024-01-01T00:00:00Z','2024-01-01T00:00:00Z')`,
	} {
		if _, err := rig.db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	w := rig.do("GET", "/api/libraries/l2/opml", "", rootUser())
	if w.Code != 200 || w.Header().Get("Content-Type") != "application/xml; charset=utf-8" || !strings.Contains(w.Body.String(), `xmlUrl="http://y/feed"`) || strings.Contains(w.Body.String(), "htmlUrl") {
		t.Fatalf("%d %q\n%s", w.Code, w.Header().Get("Content-Type"), w.Body)
	}
	clean := store.User{ID: "u9", IsActive: true, Permissions: json.RawMessage(`{"accessAllLibraries":true,"accessAllTags":true}`)}
	body := rig.do("GET", "/api/libraries/l2/opml", "", clean).Body.String()
	if !strings.Contains(body, "http://x/feed") || strings.Contains(body, "http://y/feed") {
		t.Fatalf("explicit podcast listed for a user without explicit access:\n%s", body)
	}
	if w := rig.do("GET", "/api/libraries/l1/opml", "", rootUser()); !strings.Contains(w.Body.String(), "<body>\n    </body>") {
		t.Fatalf("book library: %s", w.Body)
	}
	if w := rig.do("GET", "/api/libraries/nope/opml", "", rootUser()); w.Code != 404 || w.Body.String() != "Library not found" {
		t.Fatalf("unknown library %d %q", w.Code, w.Body)
	}
	other := store.User{ID: "u8", IsActive: true, Permissions: json.RawMessage(`{"librariesAccessible":["l1"]}`)}
	if w := rig.do("GET", "/api/libraries/l2/opml", "", other); w.Code != 403 {
		t.Fatalf("inaccessible library %d", w.Code)
	}
	if w := rig.do("GET", "/api/libraries/l2/opml?limit=1.5", "", rootUser()); w.Code != 400 || w.Body.String() != "Invalid request. limit must be a positive integer" {
		t.Fatalf("bad limit %d %q", w.Code, w.Body)
	}
}

func TestLibraryRemoveMetadata(t *testing.T) {
	rig := newAuthorRig(t, nil)
	dir := t.TempDir()
	file := filepath.Join(dir, "metadata.json")
	if err := os.WriteFile(file, []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	files := `[{"metadata":{"filename":"01.mp3","path":"/x/01.mp3"}},{"metadata":{"filename":"metadata.json","path":` + jsonString(file) + `}}]`
	if _, err := rig.db.Exec(`UPDATE libraryItems SET libraryFiles=? WHERE id='i1'`, files); err != nil {
		t.Fatal(err)
	}
	if w := rig.do("POST", "/api/libraries/l1/remove-metadata", "{}", readOnlyUser()); w.Code != 403 {
		t.Fatalf("non-admin %d", w.Code)
	}
	if w := rig.do("POST", "/api/libraries/l1/remove-metadata?ext=abs", "{}", rootUser()); w.Code != 200 || w.Body.String() != `{"found":0}` {
		t.Fatalf("abs: %d %s", w.Code, w.Body)
	}
	w := rig.do("POST", "/api/libraries/l1/remove-metadata", "{}", rootUser())
	if w.Code != 200 || w.Body.String() != `{"found":1,"removed":1}` {
		t.Fatalf("json: %d %s", w.Code, w.Body)
	}
	if _, err := os.Stat(file); err == nil {
		t.Fatal("file kept")
	}
	// The database still lists the file, and a file that is gone still counts.
	if w := rig.do("POST", "/api/libraries/l1/remove-metadata", "{}", rootUser()); w.Body.String() != `{"found":1,"removed":1}` {
		t.Fatalf("again: %s", w.Body)
	}
}

func jsonString(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

func zipNames(t *testing.T, body []byte) []string {
	t.Helper()
	zr, err := zip.NewReader(bytes.NewReader(body), int64(len(body)))
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, f := range zr.File {
		names = append(names, f.Name)
	}
	sort.Strings(names)
	return names
}

func TestLibraryDownload(t *testing.T) {
	rig := newAuthorRig(t, nil)
	root := t.TempDir()
	dir := filepath.Join(root, "The Book")
	if err := os.MkdirAll(filepath.Join(dir, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	for name, content := range map[string]string{"01.mp3": "audio", "sub/notes.txt": "notes"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	single := filepath.Join(root, "Single.m4b")
	if err := os.WriteFile(single, []byte("m4b"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := rig.db.Exec(`UPDATE libraryItems SET path=? WHERE id='i1'`, dir); err != nil {
		t.Fatal(err)
	}
	if _, err := rig.db.Exec(`UPDATE libraryItems SET path=?, isFile=1 WHERE id='i2'`, single); err != nil {
		t.Fatal(err)
	}
	downloader := store.User{ID: "u7", IsActive: true, Permissions: json.RawMessage(`{"accessAllLibraries":true,"accessAllTags":true,"accessExplicitContent":true,"download":true}`)}
	if w := rig.do("GET", "/api/libraries/l1/download?ids=i1", "", readOnlyUser()); w.Code != 403 {
		t.Fatalf("no download permission %d", w.Code)
	}
	for _, target := range []string{"/api/libraries/l1/download", "/api/libraries/l1/download?ids="} {
		if w := rig.do("GET", target, "", downloader); w.Code != 400 || w.Body.String() != "Invalid request. ids must be a string" {
			t.Fatalf("%s: %d %q", target, w.Code, w.Body)
		}
	}
	if w := rig.do("GET", "/api/libraries/l1/download?ids=nope,l2", "", downloader); w.Code != 404 || w.Body.String() != "Library items not found" {
		t.Fatalf("unknown ids: %d %q", w.Code, w.Body)
	}
	w := rig.do("GET", "/api/libraries/l1/download?ids=i1,i2,nope", "", downloader)
	if w.Code != 200 || w.Header().Get("Content-Type") != "application/zip" || !strings.HasPrefix(w.Header().Get("Content-Disposition"), `attachment; filename="LibraryItems-`) {
		t.Fatalf("%d %v", w.Code, w.Header())
	}
	got := strings.Join(zipNames(t, w.Body.Bytes()), ",")
	if want := "Single.m4b,The Book/01.mp3,The Book/sub/,The Book/sub/notes.txt"; got != want {
		t.Fatalf("zip entries %q want %q", got, want)
	}
}
