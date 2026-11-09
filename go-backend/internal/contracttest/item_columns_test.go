package contracttest

import (
	"encoding/json"
	"testing"
)

// itemDoc is GET /api/items/:id?expanded=1 as the root user decodes it.
func itemDoc(t *testing.T, in *Install, token, id string) map[string]any {
	t.Helper()
	resp, body := in.Do(t, "GET", "/api/items/"+id+"?expanded=1", token, nil)
	if resp.StatusCode != 200 {
		t.Fatalf("item %s: %d %s", id, resp.StatusCode, body)
	}
	var out map[string]any
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func at(doc map[string]any, path ...string) any {
	var cur any = doc
	for _, key := range path {
		m, ok := cur.(map[string]any)
		if !ok {
			return nil
		}
		cur = m[key]
	}
	return cur
}

func oneID(t *testing.T, in *Install, query string) string {
	t.Helper()
	var id string
	if err := in.DB.QueryRow(query).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

// TestItemJSONReflectsStoredColumns checks what the item routes report for the
// stored rows, including the NULL and zero columns the fixture does not use.
// The expected values follow LibraryItem/Book/Podcast toOldJSONExpanded in the
// upstream models.
func TestItemJSONReflectsStoredColumns(t *testing.T) {
	in := Boot(t)
	root := in.Login(t, in.Users["root"].Username, in.Users["root"].Password)
	firstBook := oneID(t, in, `SELECT id FROM libraryItems WHERE mediaType='book' ORDER BY id LIMIT 1`)
	lastBook := oneID(t, in, `SELECT id FROM libraryItems WHERE mediaType='book' ORDER BY id DESC LIMIT 1`)
	podcast := oneID(t, in, `SELECT id FROM libraryItems WHERE mediaType='podcast' LIMIT 1`)

	for _, stmt := range []string{
		`UPDATE books SET title=NULL, subtitle=NULL, explicit=1, duration=NULL, ebookFile=NULL WHERE id=(SELECT mediaId FROM libraryItems WHERE id='` + firstBook + `')`,
		`UPDATE books SET description='<p>Hi <b>there</b></p>', narrators='["A B","C"]', abridged=1 WHERE id=(SELECT mediaId FROM libraryItems WHERE id='` + lastBook + `')`,
		`UPDATE libraryItems SET lastScan=NULL, lastScanVersion=NULL, ino=NULL WHERE mediaType='book'`,
		`UPDATE libraryItems SET lastScan=NULL, size=1234 WHERE mediaType='podcast'`,
		`UPDATE podcasts SET title=NULL, numEpisodes=NULL, lastEpisodeCheck=0, maxEpisodesToKeep=NULL, explicit=1`,
	} {
		if _, err := in.DB.Exec(stmt); err != nil {
			t.Fatalf("%s: %v", stmt, err)
		}
	}

	book := itemDoc(t, in, root, firstBook)
	for path, want := range map[string]any{
		"media.metadata.title": nil, "media.metadata.subtitle": nil, "media.metadata.explicit": true,
		"media.duration": nil, "media.ebookFile": nil,
		"lastScan": nil, "scanVersion": nil, "ino": nil,
	} {
		if got := at(book, splitPath(path)...); got != want {
			t.Errorf("book %s = %#v, want %#v", path, got, want)
		}
	}
	if at(book, "media", "metadata", "authorName") != "Terry Fixture" || at(book, "mediaType") != "book" {
		t.Errorf("columns that were not touched must still be reported: %v", at(book, "media", "metadata"))
	}

	described := itemDoc(t, in, root, lastBook)
	for path, want := range map[string]any{
		"media.metadata.description":      "<p>Hi <b>there</b></p>",
		"media.metadata.descriptionPlain": "Hi there",
		"media.metadata.narratorName":     "A B, C",
		"media.metadata.abridged":         true,
	} {
		if got := at(described, splitPath(path)...); got != want {
			t.Errorf("book %s = %#v, want %#v", path, got, want)
		}
	}
	if narrators, _ := at(described, "media", "metadata", "narrators").([]any); len(narrators) != 2 || narrators[0] != "A B" || narrators[1] != "C" {
		t.Errorf("narrators = %v", narrators)
	}

	cast := itemDoc(t, in, root, podcast)
	for path, want := range map[string]any{
		"media.metadata.title": nil, "media.metadata.explicit": true,
		"media.lastEpisodeCheck": nil, "media.maxEpisodesToKeep": float64(0),
		"media.numEpisodes": float64(3), // counted from the episode rows, not the column
		"size":              float64(1234), "lastScan": nil,
	} {
		if got := at(cast, splitPath(path)...); got != want {
			t.Errorf("podcast %s = %#v, want %#v", path, got, want)
		}
	}
}

func splitPath(path string) []string {
	var out []string
	start := 0
	for i := 0; i <= len(path); i++ {
		if i == len(path) || path[i] == '.' {
			out = append(out, path[start:i])
			start = i + 1
		}
	}
	return out
}
