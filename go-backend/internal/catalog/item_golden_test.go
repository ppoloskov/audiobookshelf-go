package catalog

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"audiobookshelf-compat/internal/config"
)

// itemGoldenPath holds the output of ItemJSON (plain and expanded) and
// ItemMinifiedJSON for the fixture items. It was captured from the
// map-based serializers before they were typed, so a typed
// rewrite must reproduce it. Keys are compared as canonical JSON (sorted
// object keys, as encoding/json writes maps), values exactly.
//
// Regenerate on purpose with: UPDATE_GOLDEN=1 go test ./internal/catalog -run TestItemGolden
const itemGoldenPath = "testdata/item-golden.json"

const itemGoldenExtraFixtures = `
INSERT INTO authors VALUES ('a2','Bo Second','Second, Bo',NULL,NULL,NULL,'l1','2024-01-01T00:00:00Z','2024-01-01T00:00:00Z');
INSERT INTO series VALUES ('s2','Second Series','Second Series','About it','l1','2024-01-01T00:00:00Z','2024-01-01T00:00:00Z');
INSERT INTO books VALUES ('b2','An Edge Case','Edge Case, An','A subtitle',NULL,'2020-02-29','Pub House',NULL,'9780000000002','B000000002',NULL,1,1,NULL,0,'["Reader One","Reader Two"]','[{"index":1,"ino":"audio-2a","metadata":{"filename":"01.m4b","ext":".m4b","path":"/media/books/Edge/01.m4b","relPath":"01.m4b","size":200,"mtimeMs":1704067200000,"ctimeMs":1704067200000,"birthtimeMs":1704067200000},"addedAt":1704067200000,"updatedAt":1704067200000,"trackNumFromMeta":1,"discNumFromMeta":null,"trackNumFromFilename":1,"discNumFromFilename":null,"manuallyVerified":false,"format":"MPEG-4","duration":100.25,"bitRate":64000,"language":null,"codec":"aac","timeBase":"1/44100","channels":2,"channelLayout":"stereo","chapters":[],"embeddedCoverArt":null,"metaTags":{"tagTitle":"An Edge Case"},"mimeType":"audio/mp4"},{"index":2,"ino":"audio-2b","metadata":{"filename":"02.m4b","ext":".m4b","path":"/media/books/Edge/02.m4b","relPath":"02.m4b","size":300,"mtimeMs":1704067200000,"ctimeMs":1704067200000,"birthtimeMs":1704067200000},"addedAt":1704067200000,"updatedAt":1704067200000,"trackNumFromMeta":2,"discNumFromMeta":null,"trackNumFromFilename":2,"discNumFromFilename":null,"manuallyVerified":false,"format":"MPEG-4","duration":50.5,"bitRate":64000,"language":null,"codec":"aac","timeBase":"1/44100","channels":2,"channelLayout":"stereo","chapters":[],"embeddedCoverArt":null,"metaTags":{},"mimeType":"audio/mp4"}]','null','[{"id":0,"start":0,"end":100.25,"title":"One"},{"id":1,"start":100.25,"end":150.75,"title":"Two"}]','["tag-a","tag-b"]','["Fantasy","Sci-Fi & Fantasy"]','2024-01-03T00:00:00Z','2024-01-04T00:00:00Z');
INSERT INTO libraryItems VALUES ('i2','ino-2','/media/books/Edge','Edge','b2','book',0,0,0,'2024-01-03T00:00:00Z','2024-01-03T00:00:00Z','2024-01-03T00:00:00Z',500,NULL,NULL,'[]','{}','An Edge Case','Edge Case, An','Ada Author, Bo Second','Author, Ada, Second, Bo','l1','f1','2024-01-03T00:00:00Z','2024-01-04T00:00:00Z');
INSERT INTO bookAuthors VALUES ('ba2','b2','a1','2024-01-01T00:00:00Z'),('ba3','b2','a2','2024-01-01T00:00:01Z');
INSERT INTO bookSeries VALUES ('bs2','','b2','s1','2024-01-01T00:00:00Z'),('bs3','2.5','b2','s2','2024-01-01T00:00:01Z');
INSERT INTO bookSeries VALUES ('bs4',NULL,'b1','s2','2024-01-01T00:00:02Z');
INSERT INTO books VALUES ('b3','Ebook Only',NULL,NULL,NULL,NULL,NULL,'Plain text',NULL,NULL,'de',0,0,NULL,NULL,'[]','[]','{"ino":"ebook-3","metadata":{"filename":"b.epub","ext":".epub","path":"/media/books/Ebook/b.epub","relPath":"b.epub","size":50,"mtimeMs":1704067200000,"ctimeMs":1704067200000,"birthtimeMs":1704067200000},"ebookFormat":"epub","addedAt":1704067200000,"updatedAt":1704067200000}','[]','[]','[]','2024-01-05T00:00:00Z','2024-01-06T00:00:00Z');
INSERT INTO libraryItems VALUES ('i3','ino-3','/media/books/Ebook','Ebook','b3','book',0,1,1,'2024-01-05T00:00:00Z','2024-01-05T00:00:00Z','2024-01-05T00:00:00Z',50,'2024-01-06T00:00:00Z','2.3.0','[{"ino":"ebook-3","metadata":{"filename":"b.epub","ext":".epub","path":"/media/books/Ebook/b.epub","relPath":"b.epub","size":50,"mtimeMs":1704067200000,"ctimeMs":1704067200000,"birthtimeMs":1704067200000},"isSupplementary":false,"fileType":"ebook","addedAt":1704067200000,"updatedAt":1704067200000}]','{"note":"x"}','Ebook Only','Ebook Only','','','l1','f1','2024-01-05T00:00:00Z','2024-01-06T00:00:00Z');
`

type itemGoldenCase struct {
	Plain    json.RawMessage `json:"itemJSON"`
	Expanded json.RawMessage `json:"itemJSONExpanded"`
	Minified json.RawMessage `json:"itemMinifiedJSON"`
}

func canonicalJSON(t *testing.T, value any) json.RawMessage {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	// Round-trip through any so the golden does not depend on the Go type
	// that produced the value, only on its JSON content.
	var generic any
	if err := json.Unmarshal(encoded, &generic); err != nil {
		t.Fatal(err)
	}
	canonical, err := json.Marshal(generic)
	if err != nil {
		t.Fatal(err)
	}
	return canonical
}

func TestItemGolden(t *testing.T) {
	db := catalogFixture(t)
	defer db.Close()
	if _, err := db.Exec(itemGoldenExtraFixtures); err != nil {
		t.Fatal(err)
	}
	h := New(db, config.Config{}, nil)
	ctx := context.Background()

	got := map[string]itemGoldenCase{}
	for _, id := range []string{"i1", "i2", "i3"} {
		plain, err := h.ItemJSON(ctx, id, false)
		if err != nil {
			t.Fatalf("ItemJSON(%s, false): %v", id, err)
		}
		expanded, err := h.ItemJSON(ctx, id, true)
		if err != nil {
			t.Fatalf("ItemJSON(%s, true): %v", id, err)
		}
		minified, err := h.ItemMinifiedJSON(ctx, id)
		if err != nil {
			t.Fatalf("ItemMinifiedJSON(%s): %v", id, err)
		}
		got[id] = itemGoldenCase{canonicalJSON(t, plain), canonicalJSON(t, expanded), canonicalJSON(t, minified)}
	}
	encoded, err := json.MarshalIndent(got, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	encoded = append(encoded, '\n')

	if os.Getenv("UPDATE_GOLDEN") != "" {
		if err := os.MkdirAll(filepath.Dir(itemGoldenPath), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(itemGoldenPath, encoded, 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(itemGoldenPath)
	if err != nil {
		t.Fatalf("read golden (regenerate with UPDATE_GOLDEN=1): %v", err)
	}
	if !bytes.Equal(encoded, want) {
		t.Fatalf("item JSON differs from %s; regenerate with UPDATE_GOLDEN=1 only if the change is intended\n got: %s\nwant: %s", itemGoldenPath, encoded, want)
	}
}
