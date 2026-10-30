package providers

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"

	"audiobookshelf-compat/internal/store"
	_ "modernc.org/sqlite"
)

func providersTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "providers.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	for _, statement := range []string{`CREATE TABLE customMetadataProviders(id UUID PRIMARY KEY,name VARCHAR(255),mediaType VARCHAR(255),url VARCHAR(255),authHeaderValue VARCHAR(255),extraData JSON,createdAt DATETIME NOT NULL,updatedAt DATETIME NOT NULL)`, `CREATE TABLE libraries(id UUID PRIMARY KEY,provider VARCHAR(255))`} {
		if _, err := db.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func TestCustomProviderCRUDPermissionsAndFallback(t *testing.T) {
	db := providersTestDB(t)
	var emitted []string
	h, err := New(db, func(scope, event string, _ any) { emitted = append(emitted, scope+":"+event) })
	if err != nil {
		t.Fatal(err)
	}
	request := func(method, path, body string, user store.User) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		handled := h.Handle(w, r, user)
		if !handled {
			t.Fatalf("route not handled: %s %s", method, path)
		}
		return w
	}
	forbidden := request(http.MethodGet, "/api/custom-metadata-providers", "", store.User{Type: "user"})
	if forbidden.Code != http.StatusForbidden {
		t.Fatalf("user access status=%d", forbidden.Code)
	}
	bad := request(http.MethodPost, "/api/custom-metadata-providers", `{"name":"Provider","mediaType":"book","url":"not a url"}`, store.User{Type: "admin"})
	if bad.Code != 400 || bad.Body.String() != "Invalid url" {
		t.Fatalf("invalid url response=%d %q", bad.Code, bad.Body.String())
	}
	created := request(http.MethodPost, "/api/custom-metadata-providers", `{"name":"Archive","mediaType":"book","url":"https://provider.test/api","authHeaderValue":"Bearer token","extraData":{"x":1}}`, store.User{Type: "root"})
	if created.Code != 200 {
		t.Fatalf("create status=%d body=%s", created.Code, created.Body.String())
	}
	var response struct {
		Provider map[string]any `json:"provider"`
	}
	if err := json.Unmarshal(created.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	id, _ := response.Provider["id"].(string)
	if id == "" || response.Provider["url"] != "https://provider.test/api" {
		t.Fatalf("created provider=%v", response.Provider)
	}
	if len(emitted) != 1 || emitted[0] != "*:custom_metadata_provider_added" {
		t.Fatalf("create events=%v", emitted)
	}
	if _, err := db.Exec(`INSERT INTO libraries(id,provider) VALUES('lib-1',?)`, "custom-"+id); err != nil {
		t.Fatal(err)
	}
	listed := request(http.MethodGet, "/api/custom-metadata-providers", "", store.User{Type: "admin"})
	if listed.Code != 200 {
		t.Fatalf("list status=%d", listed.Code)
	}
	var list map[string][]map[string]any
	if err := json.Unmarshal(listed.Body.Bytes(), &list); err != nil {
		t.Fatal(err)
	}
	if len(list["providers"]) != 1 || list["providers"][0]["authHeaderValue"] != "Bearer token" {
		t.Fatalf("list payload=%v", list)
	}
	deleted := request(http.MethodDelete, "/api/custom-metadata-providers/"+url.PathEscape(id), "", store.User{Type: "admin"})
	if deleted.Code != 200 || deleted.Body.String() != "OK" {
		t.Fatalf("delete response=%d %q", deleted.Code, deleted.Body.String())
	}
	var fallback string
	if err := db.QueryRow(`SELECT provider FROM libraries WHERE id='lib-1'`).Scan(&fallback); err != nil {
		t.Fatal(err)
	}
	if fallback != "google" {
		t.Fatalf("book provider fallback=%q", fallback)
	}
	if len(emitted) != 2 || emitted[1] != "*:custom_metadata_provider_removed" {
		t.Fatalf("events=%v", emitted)
	}
}

func TestProviderCatalogAndCustomSearchHTTPTransform(t *testing.T) {
	var requestSeen *http.Request
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestSeen = r.Clone(context.Background())
		if r.Header.Get("Authorization") != "Bearer local-test" {
			t.Errorf("authorization header=%q", r.Header.Get("Authorization"))
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"matches":[{"title":42,"author":["A","B"],"description":"<p>Safe <script>bad</script><a href=\"javascript:bad\">link</a></p>","genres":"Mystery, Mystery, Thriller","tags":[" Classic ","Classic"],"series":[{"series":"Saga","sequence":2},{"sequence":false}],"duration":"12.5","cover":"https://img.test/cover.jpg","drop":{"x":1}}]}`))
	}))
	defer remote.Close()
	db := providersTestDB(t)
	now := dbNow()
	if _, err := db.Exec(`INSERT INTO customMetadataProviders(id,name,mediaType,url,authHeaderValue,createdAt,updatedAt) VALUES('custom-1','Local Books','book',?,'Bearer local-test',?,?)`, remote.URL, now, now); err != nil {
		t.Fatal(err)
	}
	h, err := New(db, nil)
	if err != nil {
		t.Fatal(err)
	}
	admin := store.User{Type: "admin"}
	catalog := httptest.NewRecorder()
	if !h.Handle(catalog, httptest.NewRequest(http.MethodGet, "/api/search/providers", nil), store.User{Type: "user"}) {
		t.Fatal("provider catalog not handled")
	}
	if catalog.Code != 200 {
		t.Fatalf("catalog status=%d", catalog.Code)
	}
	var catalogJSON map[string]any
	if err := json.Unmarshal(catalog.Body.Bytes(), &catalogJSON); err != nil {
		t.Fatal(err)
	}
	providers := catalogJSON["providers"].(map[string]any)
	books := providers["books"].([]any)
	if len(books) < 2 {
		t.Fatalf("book providers=%v", books)
	}
	search := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/api/search/books?provider=custom-custom-1&title=The+Book&author=A+Writer&isbn=123", nil)
	if !h.Handle(search, request, admin) {
		t.Fatal("custom book search not handled")
	}
	if search.Code != 200 {
		t.Fatalf("search status=%d body=%s", search.Code, search.Body.String())
	}
	if requestSeen == nil || requestSeen.URL.Query().Get("mediaType") != "book" || requestSeen.URL.Query().Get("query") != "The Book" || requestSeen.URL.Query().Get("author") != "A Writer" || requestSeen.URL.Query().Get("isbn") != "123" {
		t.Fatalf("custom request=%v", requestSeen)
	}
	var results []map[string]any
	if err := json.Unmarshal(search.Body.Bytes(), &results); err != nil || len(results) != 1 {
		t.Fatalf("search results=%s err=%v", search.Body.String(), err)
	}
	result := results[0]
	if result["title"] != "42" || result["author"] != "A,B" || result["duration"] != 12.5 {
		t.Fatalf("normalized fields=%v", result)
	}
	if result["description"] != "<p>Safe </p><p><a>link</a></p>" && result["description"] != "<p>Safe <a>link</a></p>" {
		t.Fatalf("sanitized description=%q", result["description"])
	}
	if len(result["genres"].([]any)) != 2 || len(result["tags"].([]any)) != 1 || len(result["series"].([]any)) != 1 {
		t.Fatalf("normalized arrays=%v", result)
	}
}

func TestCustomSearchMalformedAndRemoteFailureReturnEmpty(t *testing.T) {
	for _, body := range []string{`{}`, `{"matches":null}`, `not-json`} {
		t.Run(body, func(t *testing.T) {
			remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(body)) }))
			defer remote.Close()
			db := providersTestDB(t)
			now := dbNow()
			_, err := db.Exec(`INSERT INTO customMetadataProviders(id,name,mediaType,url,createdAt,updatedAt) VALUES('id-1','local','book',?,?,?)`, remote.URL, now, now)
			if err != nil {
				t.Fatal(err)
			}
			h, _ := New(db, nil)
			results, err := h.searchCustom(context.Background(), "custom-id-1", "book", "title", "", "", 0)
			if err == nil {
				t.Fatalf("malformed response %q accepted: %#v", body, results)
			}
		})
	}
}

func TestBuiltinProvidersUseInjectableHTTPAndSourceTransforms(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasPrefix(r.URL.Path, "/books/v1/volumes"):
			_, _ = w.Write([]byte(`{"items":[{"id":"g1","volumeInfo":{"title":"Title","authors":["Writer"],"publishedDate":"2020-03-01","industryIdentifiers":[{"type":"ISBN_13","identifier":"9780000000001"}],"imageLinks":{"thumbnail":"http://image.test/book.jpg"}}}]}`))
		case r.URL.Path == "/search-works":
			if r.URL.Query().Get("q") != "Title Writer" || r.URL.Query().Get("onlymatches") != "1" {
				t.Errorf("FantLab search query=%v", r.URL.Query())
			}
			_, _ = w.Write([]byte(`[{"work_id":12,"work_type_id":1}]`))
		case r.URL.Path == "/work/12/extended":
			_, _ = w.Write([]byte(`{"work_id":12,"work_name":"Title","authors":[{"name":"Writer"}],"work_year":2021,"classificatory":{"genre_group":[{"genre_group_id":1,"genre":[{"label":"Fantasy","genre":[{"label":"Epic"}]}]}]}}`))
		case r.URL.Path == "/1.0/catalog/products":
			if r.URL.Query().Get("title") != "Title" {
				t.Errorf("Audible catalog query=%v", r.URL.Query())
			}
			_, _ = w.Write([]byte(`{"products":[{"asin":"B000000001"}]}`))
		case r.URL.Path == "/books/B000000001":
			_, _ = w.Write([]byte(`{"asin":"B000000001","title":"Title","authors":[{"name":"Writer"}],"narrators":[{"name":"Reader"}],"releaseDate":"2022-01-01","runtimeLengthMin":12.5,"formatType":"abridged","genres":[{"type":"genre","name":"Mystery"},{"type":"genre","name":"Mystery"},{"type":"tag","name":"Classic"}],"seriesPrimary":{"name":"Saga","position":"Book 1.5"}}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	db := providersTestDB(t)
	h, err := New(db, nil)
	if err != nil {
		t.Fatal(err)
	}
	h.SetEndpoint("google", server.URL+"/books/v1/volumes")
	h.SetEndpoint("fantlab", server.URL)
	h.SetEndpoint("audible", server.URL+"/1.0/catalog/products")
	h.SetEndpoint("audnexus", server.URL)
	google, err := h.searchBuiltinBooks(context.Background(), "google", "Title", "Writer", "", "")
	if err != nil || len(google) != 1 || google[0]["publishedYear"] != "2020" || google[0]["isbn"] != "9780000000001" || google[0]["cover"] != "https://image.test/book.jpg" {
		t.Fatalf("Google result=%v err=%v", google, err)
	}
	fantlab, err := h.searchBuiltinBooks(context.Background(), "fantlab", "Title", "Writer", "", "")
	if err != nil || len(fantlab) != 1 || fantlab[0]["author"] != "Writer" || len(fantlab[0]["genres"].([]string)) != 2 {
		t.Fatalf("FantLab result=%v err=%v", fantlab, err)
	}
	audible, err := h.searchBuiltinBooks(context.Background(), "audible", "Title", "Writer", "", "")
	if err != nil || len(audible) != 1 || audible[0]["narrator"] != "Reader" || audible[0]["duration"] != 12.5 || audible[0]["abridged"] != true || audible[0]["series"].([]map[string]any)[0]["sequence"] != "1.5" {
		t.Fatalf("Audible result=%v err=%v", audible, err)
	}
}
