package catalog

import (
	"audiobookshelf-compat/internal/serversettings"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"net/url"
	"reflect"
	"testing"
	"time"

	"audiobookshelf-compat/internal/config"
	"audiobookshelf-compat/internal/store"
)

func TestBookPageContentsIndependentOfPageSize(t *testing.T) {
	for _, size := range []int{10, 50} {
		t.Run(fmt.Sprintf("page-%d", size), func(t *testing.T) {
			db := catalogFixture(t)
			seedBookPage(t, db, size)
			h := New(db, config.Config{}, nil, testDeps())
			user := store.User{
				ID: "page-user", IsActive: true,
				Permissions: json.RawMessage(`{"accessAllLibraries":true,"accessAllTags":true,"accessExplicitContent":false}`),
			}

			for _, minified := range []bool{false, true} {
				flag := ""
				if minified {
					flag = "&minified=1"
				}
				path := "/api/libraries/l1/items?filter=" + url.QueryEscape("genres.ZmljdGlvbg==") + "&sort=addedAt&limit=" + fmt.Sprint(size) + flag
				payload := requestBookPage(t, h, user, path)
				if payload["minified"] != minified || payload["total"] != float64(size) {
					t.Fatalf("page %d minified=%t envelope=%#v", size, minified, payload)
				}
				results := payload["results"].([]any)
				if len(results) != size {
					t.Fatalf("page %d minified=%t result count=%d, want %d", size, minified, len(results), size)
				}
				wantIDs := []string{"i1"}
				for n := 2; n <= size*2; n++ {
					if n%2 == 0 && n != 4 {
						wantIDs = append(wantIDs, fmt.Sprintf("i%d", n))
					}
				}
				gotIDs := make([]string, 0, len(results))
				for _, result := range results {
					item := result.(map[string]any)
					gotIDs = append(gotIDs, item["id"].(string))
					if item["id"] == "i8" {
						metadata := item["media"].(map[string]any)["metadata"].(map[string]any)
						if title, ok := metadata["title"]; !ok || title != nil {
							t.Errorf("nullable-title metadata title=%#v present=%t", title, ok)
						}
						if prefix, ok := metadata["titleIgnorePrefix"]; !ok || prefix != nil {
							t.Errorf("nullable-title prefix=%#v present=%t", prefix, ok)
						}
					}
				}
				if !reflect.DeepEqual(gotIDs, wantIDs) {
					t.Fatalf("page %d minified=%t result IDs=%v, want %v", size, minified, gotIDs, wantIDs)
				}
				first := results[0].(map[string]any)
				if first["id"] != "i1" {
					t.Errorf("page %d first item=%v, want i1", size, first["id"])
				}
				media := first["media"].(map[string]any)
				metadata := media["metadata"].(map[string]any)
				if metadata["description"] != "<p>A story</p>" || metadata["titleIgnorePrefix"] != "Book, The" {
					t.Errorf("page %d first metadata=%#v", size, metadata)
				}
				last := results[len(results)-1].(map[string]any)["media"].(map[string]any)["metadata"].(map[string]any)
				if last["description"] != nil {
					t.Errorf("page %d last description=%#v, want null", size, last["description"])
				}

				page := requestBookPage(t, h, user, "/api/libraries/l1/items?filter="+url.QueryEscape("genres.ZmljdGlvbg==")+"&sort=addedAt&limit=2&page=1"+flag)
				pageIDs := make([]string, 0, 2)
				for _, result := range page["results"].([]any) {
					pageIDs = append(pageIDs, result.(map[string]any)["id"].(string))
				}
				if !reflect.DeepEqual(pageIDs, []string{"i6", "i8"}) {
					t.Errorf("page %d pagination/filter/ACL IDs=%v, want [i6 i8]", size, pageIDs)
				}
			}

			assertBookPrefixValues(t, h, user)
		})
	}
}

func seedBookPage(t *testing.T, db *sql.DB, pageSize int) {
	t.Helper()
	if _, err := db.Exec(`CREATE TABLE settings (key TEXT PRIMARY KEY,value JSON)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO settings VALUES ('server-settings','{"sortingIgnorePrefix":false,"sortingPrefixes":["the","a"]}')`); err != nil {
		t.Fatal(err)
	}
	for n := 2; n <= pageSize*2; n++ {
		bookID := fmt.Sprintf("book-%02d", n)
		itemID := fmt.Sprintf("i%d", n)
		genre, explicit := "mystery", 0
		var title any = fmt.Sprintf("The Book %03d", n)
		if n%2 == 0 {
			genre = "fiction"
		}
		if n == 2 {
			title = "An Book"
		}
		if n == 8 {
			title = nil
		}
		if n == 4 {
			explicit = 1
		}
		if _, err := db.Exec(`INSERT INTO books (id,title,explicit,abridged,narrators,audioFiles,ebookFile,chapters,tags,genres) VALUES (?,?,?,0,'[]','[]','null','[]','[]',?)`, bookID, title, explicit, fmt.Sprintf(`["%s"]`, genre)); err != nil {
			t.Fatal(err)
		}
		createdAt := time.Date(2024, time.January, n+3, 0, 0, 0, 0, time.UTC).Format(time.RFC3339)
		if _, err := db.Exec(`INSERT INTO libraryItems (id,ino,path,relPath,mediaId,mediaType,isFile,isMissing,isInvalid,size,extraData,libraryFiles,title,libraryId,libraryFolderId,createdAt,updatedAt) VALUES (?,?,?, ?,?,'book',0,0,0,0,'{}','[]',?,'l1','f1',?,?)`, itemID, itemID, "/media/books/"+bookID, bookID, bookID, title, createdAt, createdAt); err != nil {
			t.Fatal(err)
		}
	}
}

func requestBookPage(t *testing.T, h *Handler, user store.User, path string) map[string]any {
	t.Helper()
	w := httptest.NewRecorder()
	if !h.Handle(w, httptest.NewRequest("GET", path, nil), user) || w.Code != 200 {
		t.Fatalf("book page status=%d body=%s", w.Code, w.Body.String())
	}
	var payload map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode book page: %v; body=%s", err, w.Body.String())
	}
	return payload
}

// assertBookPrefixValues changes the server's sorting prefixes and checks the
// titleIgnorePrefix the items API reports (Book.titleIgnorePrefix with the
// configured prefixes; an explicit empty list means none).
func assertBookPrefixValues(t *testing.T, h *Handler, user store.User) {
	t.Helper()
	set := func(value string) {
		t.Helper()
		if _, err := h.db.Exec(`UPDATE settings SET value=? WHERE key='server-settings'`, value); err != nil {
			t.Fatal(err)
		}
		serversettings.Invalidate(h.db)
	}
	set(`{"sortingIgnorePrefix":false}`)
	assertBatchPagePrefix(t, h, user, 0, "The Book", "Book, The")
	set(`{"sortingPrefixes":["an"]}`)
	assertBatchPagePrefix(t, h, user, 1, "An Book", "Book, An")
	set(`{"sortingPrefixes":["the",false,"",3]}`)
	assertBatchPagePrefix(t, h, user, 0, "The Book", "Book, The")
	set(`{"sortingPrefixes":[]}`)
	assertBatchPagePrefix(t, h, user, 0, "The Book", "The Book")
}

func assertBatchPagePrefix(t *testing.T, h *Handler, user store.User, page int, wantTitle, wantPrefix string) {
	t.Helper()
	path := "/api/libraries/l1/items?filter=" + url.QueryEscape("genres.ZmljdGlvbg==") + "&sort=addedAt&limit=1&page=" + fmt.Sprint(page)
	payload := requestBookPage(t, h, user, path)
	results := payload["results"].([]any)
	if len(results) != 1 {
		t.Fatalf("prefix page %d returned %d items", page, len(results))
	}
	metadata := results[0].(map[string]any)["media"].(map[string]any)["metadata"].(map[string]any)
	if metadata["title"] != wantTitle || metadata["titleIgnorePrefix"] != wantPrefix {
		t.Errorf("batched title prefix: title=%#v prefix=%#v, want %q / %q", metadata["title"], metadata["titleIgnorePrefix"], wantTitle, wantPrefix)
	}
}
