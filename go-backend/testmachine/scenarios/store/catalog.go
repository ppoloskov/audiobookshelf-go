package store

import (
	"encoding/json"
	"net/http"
	"path/filepath"
	"reflect"
	"testing"

	"audiobookshelf-compat/testmachine/scenarios/harness"
)

func catalogReopen(t *testing.T, _ *harness.Env) {
	e := harness.Start(t, harness.Options{})
	item := e.IDs["novel"]
	const bookID = "85ab0f32-b17f-4c9a-883e-a61e260f1e46"
	const title = "The Reopened Novel"
	query := "SELECT b.id AS bookId, b.title AS bookTitle, li.id AS itemId, li.title AS itemTitle FROM books b JOIN libraryItems li ON li.mediaId=b.id WHERE li.id = ?"
	before := e.Rows(t, query, item)
	if len(before) != 1 || before[0]["bookId"] != bookID || before[0]["bookTitle"] != "Single File Novel" || before[0]["itemTitle"] != "Single File Novel" {
		t.Fatalf("fixture novel: %v", before)
	}
	otherBooks := e.Rows(t, "SELECT id, title FROM books WHERE id != ? ORDER BY id", bookID)
	otherItems := e.Rows(t, "SELECT id, title FROM libraryItems WHERE id != ? ORDER BY id", item)
	otherFile := filepath.Join(e.Metadata, "items", e.IDs["saga1"], "metadata.json")
	otherData := readFile(t, otherFile)
	file := filepath.Join(e.Metadata, "items", item, "metadata.json")
	if got := metadataTitle(t, file); got != "Single File Novel" {
		t.Fatalf("fixture metadata title %q", got)
	}
	token := e.Token(t, "root")
	response := e.Patch(t, token, "/api/items/"+item+"/media", map[string]any{"metadata": map[string]any{"title": title}})
	if response.Status != http.StatusOK {
		t.Fatalf("catalog PATCH status %d", response.Status)
	}
	var result struct {
		Updated bool `json:"updated"`
	}
	response.JSON(t, &result)
	if !result.Updated {
		t.Fatal("catalog PATCH did not update")
	}
	expected := []map[string]any{{"bookId": bookID, "bookTitle": title, "itemId": item, "itemTitle": title}}
	if got := e.Rows(t, query, item); !reflect.DeepEqual(got, expected) {
		t.Fatalf("catalog rows: %v", got)
	}
	if got := metadataTitle(t, file); got != title {
		t.Fatalf("metadata title %q", got)
	}
	data := readFile(t, file)
	if got := e.Rows(t, "SELECT id, title FROM books WHERE id != ? ORDER BY id", bookID); !reflect.DeepEqual(got, otherBooks) {
		t.Fatal("other book titles changed")
	}
	if got := e.Rows(t, "SELECT id, title FROM libraryItems WHERE id != ? ORDER BY id", item); !reflect.DeepEqual(got, otherItems) {
		t.Fatal("other item titles changed")
	}
	if got := readFile(t, otherFile); !reflect.DeepEqual(got, otherData) {
		t.Fatal("other metadata file changed")
	}
	e.Stop(t)
	reopened := harness.Start(t, harness.Options{Install: e.Install, Upstream: true})
	if got := reopened.Rows(t, query, item); !reflect.DeepEqual(got, expected) {
		t.Fatalf("reopened catalog rows: %v", got)
	}
	if got := readFile(t, file); !reflect.DeepEqual(got, data) || metadataTitle(t, file) != title {
		t.Fatal("metadata file changed on reopen")
	}
	token = reopened.Token(t, "root")
	got := reopened.Get(t, token, "/api/items/"+item)
	if got.Status != http.StatusOK {
		t.Fatalf("catalog GET status %d", got.Status)
	}
	var doc struct {
		ID    string `json:"id"`
		Media struct {
			Metadata struct {
				Title string `json:"title"`
			} `json:"metadata"`
		} `json:"media"`
	}
	got.JSON(t, &doc)
	if doc.ID != item || doc.Media.Metadata.Title != title {
		t.Fatalf("catalog GET: %+v", doc)
	}
}
func metadataTitle(t *testing.T, file string) string {
	t.Helper()
	var value struct {
		Title string `json:"title"`
	}
	if err := json.Unmarshal(readFile(t, file), &value); err != nil {
		t.Fatalf("metadata decode: %v", err)
	}
	return value.Title
}
