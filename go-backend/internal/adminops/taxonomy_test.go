package adminops

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"audiobookshelf-compat/internal/config"
	"audiobookshelf-compat/internal/store"
)

// TestTaxonomyRenameAndDeleteGolden pins the tag and genre routes: first
// occurrence order, de-duplication, the new name appended last, the merged
// flag, and which items are announced.
func TestTaxonomyRenameAndDeleteGolden(t *testing.T) {
	type call struct {
		method, path, body string
		response           string
		books, podcasts    string // stored tags (or genres) by id, in id order
		touched            string
	}
	fixture := func(t *testing.T) (*sql.DB, *Handler, *[]string) {
		db := adminopsDB(t)
		_, err := db.Exec(`CREATE TABLE books(id TEXT PRIMARY KEY,tags JSON,genres JSON,updatedAt DATETIME);
CREATE TABLE podcasts(id TEXT PRIMARY KEY,tags JSON,genres JSON,updatedAt DATETIME);
CREATE TABLE libraryItems(id TEXT PRIMARY KEY,mediaId TEXT);
INSERT INTO books VALUES('b1','["a","b","a"]','["g"]',NULL),('b2','["b"]','[]',NULL),('b3','["c","new"]','["old","new"]',NULL),('b4',NULL,NULL,NULL),('b5','[ "a" , "x" ]','["old"]',NULL);
INSERT INTO podcasts VALUES('p1','["a"]','["old","g"]',NULL),('p2','[]','[]',NULL);
INSERT INTO libraryItems VALUES('i-b1','b1'),('i-b2','b2'),('i-b3','b3'),('i-b4','b4'),('i-b5','b5'),('i-p1','p1'),('i-p2','p2')`)
		if err != nil {
			t.Fatal(err)
		}
		var touched []string
		h := New(db, config.Config{}, nil, Deps{ItemChanged: func(_ context.Context, id string) error {
			touched = append(touched, id)
			return nil
		}})
		return db, h, &touched
	}
	enc := func(s string) string { return base64.StdEncoding.EncodeToString([]byte(s)) }
	calls := []call{
		{method: "POST", path: "/api/tags/rename", body: `{"tag":"a","newTag":"z"}`, response: `{"tagMerged":false,"numItemsUpdated":3}`, books: "b1=[\"b\",\"z\"] b2=[\"b\"] b3=[\"c\",\"new\"] b4=<nil> b5=[\"x\",\"z\"]", podcasts: "p1=[\"z\"] p2=[]", touched: "i-b1,i-b5,i-p1"},
		{method: "POST", path: "/api/tags/rename", body: `{"tag":"c","newTag":"new"}`, response: `{"tagMerged":true,"numItemsUpdated":1}`, books: "b1=[\"a\",\"b\",\"a\"] b2=[\"b\"] b3=[\"new\"] b4=<nil> b5=[ \"a\" , \"x\" ]", podcasts: "p1=[\"a\"] p2=[]", touched: "i-b3"},
		{method: "POST", path: "/api/genres/rename", body: `{"genre":"old","newGenre":"g"}`, response: `{"genreMerged":true,"numItemsUpdated":3}`, books: "b1=[\"g\"] b2=[] b3=[\"new\",\"g\"] b4=<nil> b5=[\"g\"]", podcasts: "p1=[\"g\"] p2=[]", touched: "i-b3,i-b5,i-p1"},
		{method: "POST", path: "/api/tags/rename", body: `{"tag":"","newTag":"z"}`, response: ``},
		{method: "DELETE", path: "/api/tags/" + enc("a"), response: `{"numItemsUpdated":3}`, books: "b1=[\"b\"] b2=[\"b\"] b3=[\"c\",\"new\"] b4=<nil> b5=[\"x\"]", podcasts: "p1=[] p2=[]", touched: "i-b1,i-b5,i-p1"},
		{method: "DELETE", path: "/api/genres/" + enc("nothing"), response: `{"numItemsUpdated":0}`, books: "b1=[\"g\"] b2=[] b3=[\"old\",\"new\"] b4=<nil> b5=[\"old\"]", podcasts: "p1=[\"old\",\"g\"] p2=[]"},
	}
	for _, c := range calls {
		t.Run(c.method+" "+c.path+" "+c.body, func(t *testing.T) {
			db, h, touched := fixture(t)
			w := httptest.NewRecorder()
			serve(h, w, httptest.NewRequest(c.method, c.path, strings.NewReader(c.body)), store.User{Type: "admin"})
			if c.response == "" {
				if w.Code != http.StatusBadRequest {
					t.Fatalf("status %d", w.Code)
				}
				return
			}
			if w.Code != 200 || !sameJSON(w.Body.String(), c.response) {
				t.Fatalf("%d %s", w.Code, w.Body)
			}
			column := "tags"
			if strings.Contains(c.path, "genre") {
				column = "genres"
			}
			dump := func(table string) string {
				rows, err := db.Query(`SELECT id,` + column + ` FROM ` + table + ` ORDER BY id`)
				if err != nil {
					t.Fatal(err)
				}
				defer rows.Close()
				var out []string
				for rows.Next() {
					var id string
					var v sql.NullString
					_ = rows.Scan(&id, &v)
					if v.Valid {
						out = append(out, id+"="+v.String)
					} else {
						out = append(out, id+"=<nil>")
					}
				}
				return strings.Join(out, " ")
			}
			if got := dump("books"); got != c.books {
				t.Fatalf("books\n got %s\nwant %s", got, c.books)
			}
			if got := dump("podcasts"); got != c.podcasts {
				t.Fatalf("podcasts\n got %s\nwant %s", got, c.podcasts)
			}
			if got := strings.Join(*touched, ","); got != c.touched {
				t.Fatalf("touched %q want %q", got, c.touched)
			}
		})
	}
}

func sameJSON(got, want string) bool {
	var g, w any
	return json.Unmarshal([]byte(got), &g) == nil && json.Unmarshal([]byte(want), &w) == nil && reflect.DeepEqual(g, w)
}
