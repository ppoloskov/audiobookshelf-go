package catalog

import (
	"archive/zip"
	"bytes"
	"database/sql"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
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
		if err := compareOPMLSemantics(c.XML, generateOPML(podcasts)); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
}

type opmlSemantic struct {
	Version    string
	RootAttrs  map[string]string
	HeadAttrs  map[string]string
	Title      string
	TitleAttrs map[string]string
	BodyAttrs  map[string]string
	Outlines   []map[string]string
}

func parseOPMLSemantics(raw string) (opmlSemantic, error) {
	d := xml.NewDecoder(strings.NewReader(raw))
	var got opmlSemantic
	var stack []string
	seen := make(map[string]int, 4)
	var title strings.Builder
	for {
		tok, err := d.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return opmlSemantic{}, err
		}
		switch tok := tok.(type) {
		case xml.StartElement:
			if tok.Name.Space != "" {
				return opmlSemantic{}, fmt.Errorf("namespaced OPML element %q is unsupported", tok.Name.Local)
			}
			path := strings.Join(append(stack, tok.Name.Local), "/")
			if path != "opml" && path != "opml/head" && path != "opml/head/title" && path != "opml/body" && path != "opml/body/outline" {
				return opmlSemantic{}, fmt.Errorf("unexpected OPML element %q", path)
			}
			if path == "opml/body" && seen["opml/head/title"] != 1 {
				return opmlSemantic{}, fmt.Errorf("OPML body must follow head/title")
			}
			if path != "opml/body/outline" && seen[path] > 0 {
				return opmlSemantic{}, fmt.Errorf("duplicate OPML element %q", path)
			}
			seen[path]++
			attrs, err := opmlAttributes(tok)
			if err != nil {
				return opmlSemantic{}, err
			}
			switch path {
			case "opml":
				got.RootAttrs = attrs
				got.Version = attrs["version"]
			case "opml/head":
				got.HeadAttrs = attrs
			case "opml/head/title":
				got.TitleAttrs = attrs
			case "opml/body":
				got.BodyAttrs = attrs
			case "opml/body/outline":
				got.Outlines = append(got.Outlines, attrs)
			}
			stack = append(stack, tok.Name.Local)
		case xml.EndElement:
			if tok.Name.Space != "" || len(stack) == 0 || stack[len(stack)-1] != tok.Name.Local {
				return opmlSemantic{}, fmt.Errorf("unexpected OPML closing element %q", tok.Name.Local)
			}
			stack = stack[:len(stack)-1]
		case xml.CharData:
			if len(stack) == 3 && stack[0] == "opml" && stack[1] == "head" && stack[2] == "title" {
				title.Write(tok)
			} else if strings.TrimSpace(string(tok)) != "" {
				return opmlSemantic{}, fmt.Errorf("unexpected OPML text %q", tok)
			}
		case xml.Comment, xml.ProcInst:
			// Formatting comments and processing instructions do not change OPML meaning.
		default:
			return opmlSemantic{}, fmt.Errorf("unexpected OPML token %T", tok)
		}
	}
	if len(stack) != 0 || seen["opml"] != 1 || seen["opml/head"] != 1 || seen["opml/head/title"] != 1 || seen["opml/body"] != 1 {
		return opmlSemantic{}, fmt.Errorf("OPML requires one root, head/title, and body")
	}
	if got.Version == "" {
		return opmlSemantic{}, fmt.Errorf("opml version attribute is missing")
	}
	got.Title = title.String()
	return got, nil
}

func opmlAttributes(start xml.StartElement) (map[string]string, error) {
	attrs := make(map[string]string, len(start.Attr))
	for _, attr := range start.Attr {
		if attr.Name.Space != "" || attr.Name.Local == "xmlns" {
			return nil, fmt.Errorf("namespaced OPML attribute %q is unsupported", attr.Name.Local)
		}
		if _, exists := attrs[attr.Name.Local]; exists {
			return nil, fmt.Errorf("duplicate OPML attribute %q", attr.Name.Local)
		}
		attrs[attr.Name.Local] = attr.Value
	}
	return attrs, nil
}

func compareOPMLSemantics(expected, actual string) error {
	want, err := parseOPMLSemantics(expected)
	if err != nil {
		return fmt.Errorf("parse expected OPML: %w", err)
	}
	got, err := parseOPMLSemantics(actual)
	if err != nil {
		return fmt.Errorf("parse actual OPML: %w", err)
	}
	if !reflect.DeepEqual(got, want) {
		return fmt.Errorf("semantic document = %#v, want %#v", got, want)
	}
	return nil
}

func TestOPMLSemanticComparison(t *testing.T) {
	const expected = `<?xml version="1.0"?><opml version="1.0"><head><title>Cast</title></head><body><outline type="rss" text="Cast" title="Cast" xmlUrl="https://example.test/feed"/></body></opml>`
	const reformatted = `<?xml version="1.0"?>
<opml version="1.0">
  <head><title>Cast</title></head>
  <body>
    <outline xmlUrl="https://example.test/feed" title="Cast" text="Cast" type="rss" />
  </body>
</opml>`
	if err := compareOPMLSemantics(expected, reformatted); err != nil {
		t.Fatalf("benign formatting and attribute order rejected: %v", err)
	}
	for name, actual := range map[string]string{
		"missing feed":                `<opml version="1.0"><head><title>Cast</title></head><body></body></opml>`,
		"wrong feed":                  `<opml version="1.0"><head><title>Cast</title></head><body><outline type="rss" text="Cast" title="Cast" xmlUrl="https://wrong.test/feed"/></body></opml>`,
		"wrong title":                 `<opml version="1.0"><head><title>Other</title></head><body><outline type="rss" text="Cast" title="Cast" xmlUrl="https://example.test/feed"/></body></opml>`,
		"permission leak":             `<opml version="1.0"><head><title>Cast</title></head><body><outline type="rss" text="Cast" title="Cast" xmlUrl="https://example.test/feed"/><outline type="rss" text="Private" title="Private" xmlUrl="https://private.test/feed"/></body></opml>`,
		"extra root attribute":        strings.Replace(expected, `<opml version`, `<opml unexpected="x" version`, 1),
		"extra head attribute":        strings.Replace(expected, `<head>`, `<head unexpected="x">`, 1),
		"extra title attribute":       strings.Replace(expected, `<title>`, `<title unexpected="x">`, 1),
		"extra body attribute":        strings.Replace(expected, `<body>`, `<body unexpected="x">`, 1),
		"extra outline attribute":     strings.Replace(expected, `xmlUrl="https://example.test/feed"`, `xmlUrl="https://example.test/feed" extra="x"`, 1),
		"nested unauthorized outline": `<opml version="1.0"><head><title>Cast</title></head><body><folder><outline type="rss" text="Private" title="Private" xmlUrl="https://private.test/feed"/></folder></body></opml>`,
		"duplicate body":              `<opml version="1.0"><head><title>Cast</title></head><body><outline type="rss" text="Cast" title="Cast" xmlUrl="https://example.test/feed"/></body><body></body></opml>`,
		"second document":             expected + `<other/>`,
		"trailing text":               expected + ` unexpected`,
		"namespaced root":             strings.Replace(expected, `<opml version`, `<opml xmlns="urn:unexpected" version`, 1),
	} {
		t.Run(name, func(t *testing.T) {
			if err := compareOPMLSemantics(expected, actual); err == nil {
				t.Fatal("semantic change was accepted")
			}
		})
	}
}

func newOPMLRouteRig(t *testing.T) *authorRig {
	t.Helper()
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
	return rig
}

func TestLibraryOPMLRoute(t *testing.T) {
	rig := newOPMLRouteRig(t)
	w := rig.do("GET", "/api/libraries/l2/opml", "", rootUser())
	if w.Code != 200 || w.Header().Get("Content-Type") != "application/xml; charset=utf-8" {
		t.Fatalf("%d %q\n%s", w.Code, w.Header().Get("Content-Type"), w.Body)
	}
	wantRoot := `<opml version="1.0"><head><title>Audiobookshelf Podcast Subscriptions</title></head><body><outline type="rss" text="Cast" title="Cast" xmlUrl="http://x/feed" description="About" language="en"/><outline type="rss" text="Explicit Cast" title="Explicit Cast" xmlUrl="http://y/feed"/></body></opml>`
	if err := compareOPMLSemantics(wantRoot, w.Body.String()); err != nil {
		t.Fatalf("root OPML: %v\n%s", err, w.Body)
	}
	clean := store.User{ID: "u9", IsActive: true, Permissions: json.RawMessage(`{"accessAllLibraries":true,"accessAllTags":true}`)}
	cleanResponse := rig.do("GET", "/api/libraries/l2/opml", "", clean)
	if cleanResponse.Code != 200 || cleanResponse.Header().Get("Content-Type") != "application/xml; charset=utf-8" {
		t.Fatalf("clean user response = %d %q", cleanResponse.Code, cleanResponse.Header().Get("Content-Type"))
	}
	wantClean := `<opml version="1.0"><head><title>Audiobookshelf Podcast Subscriptions</title></head><body><outline type="rss" text="Cast" title="Cast" xmlUrl="http://x/feed" description="About" language="en"/></body></opml>`
	if err := compareOPMLSemantics(wantClean, cleanResponse.Body.String()); err != nil {
		t.Fatalf("explicit podcast leaked or allowed podcast missing: %v\n%s", err, cleanResponse.Body)
	}
	w = rig.do("GET", "/api/libraries/l1/opml", "", rootUser())
	if w.Code != 200 || w.Header().Get("Content-Type") != "application/xml; charset=utf-8" {
		t.Fatalf("book library response = %d %q", w.Code, w.Header().Get("Content-Type"))
	}
	wantEmpty := `<opml version="1.0"><head><title>Audiobookshelf Podcast Subscriptions</title></head><body></body></opml>`
	if err := compareOPMLSemantics(wantEmpty, w.Body.String()); err != nil {
		t.Fatalf("book library must return empty OPML: %v\n%s", err, w.Body)
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
