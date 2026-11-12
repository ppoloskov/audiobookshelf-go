package admin

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"audiobookshelf-compat/internal/auth"
	"audiobookshelf-compat/internal/config"
	"audiobookshelf-compat/internal/db"
	"audiobookshelf-compat/internal/db/dbtest"
	"audiobookshelf-compat/internal/logging"
	"audiobookshelf-compat/internal/settings"
)

const (
	rootID     = "c2eedd44-2b7f-44cf-a18f-2687b798c386"
	listenerID = "f6af59cf-3988-45fe-b0ad-81420a953f9d"
	audiobooks = "51bf3bdf-f246-4f7d-b0a6-9059e4d0ed41"
	abFolder   = "bc88289a-fc01-4256-a39e-90649091e152"
)

type env struct {
	t        *testing.T
	d        *db.DB
	store    *settings.Store
	mux      *http.ServeMux
	meta     string
	touched  []string
	watched  []string
	changes  int
	getenv   map[string]string
	taxonomy int
}

func newEnv(t *testing.T) *env {
	t.Helper()
	e := &env{t: t, d: dbtest.Fixture(t), mux: http.NewServeMux(), meta: t.TempDir(), getenv: map[string]string{}}
	cfg := config.Config{MetadataPath: e.meta}
	var err error
	e.store, err = settings.NewStore(t.Context(), e.d, func(k string) string { return e.getenv[k] }, cfg.Dirs().Backups)
	if err != nil {
		t.Fatal(err)
	}
	e.store.OnChange(func(settings.Server) { e.changes++ })
	New(e.d, e.store, cfg, func(k string) string { return e.getenv[k] }, Deps{
		TaxonomyChanged: func() { e.taxonomy++ },
		ItemChanged:     func(_ context.Context, id string) error { e.touched = append(e.touched, id); return nil },
		WatcherUpdate:   func(l, p, k string) { e.watched = append(e.watched, l+"|"+p+"|"+k) },
	}).Routes(e.mux)
	return e
}

func (e *env) exec(query string, args ...any) {
	e.t.Helper()
	if _, err := e.d.Exec(e.t.Context(), query, args...); err != nil {
		e.t.Fatal(err)
	}
}

func (e *env) rows(query string) string {
	e.t.Helper()
	var rows []string
	if err := e.d.Select(e.t.Context(), &rows, query); err != nil {
		e.t.Fatal(err)
	}
	return strings.Join(rows, " ")
}

// do sends the request as user ("" is anonymous); upload says whether the user may upload.
func (e *env) do(user string, upload bool, r *http.Request) *httptest.ResponseRecorder {
	e.t.Helper()
	if user != "" {
		var u db.User
		if err := e.d.Get(e.t.Context(), &u, "SELECT * FROM users WHERE id = ?", user); err != nil {
			e.t.Fatal(err)
		}
		u.Permissions.V.Upload = upload
		r = r.WithContext(auth.WithUser(r.Context(), auth.NewPrincipal(u)))
	}
	w := httptest.NewRecorder()
	e.mux.ServeHTTP(w, r)
	return w
}

func (e *env) admin(method, path, body string) *httptest.ResponseRecorder {
	return e.do(rootID, true, httptest.NewRequest(method, path, strings.NewReader(body)))
}

func decode(t *testing.T, w *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var out map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("body %q: %v", w.Body.String(), err)
	}
	return out
}

func TestStatsCachePurgeAndWatcher(t *testing.T) {
	e := newEnv(t)
	stats := decode(t, e.admin("GET", "/api/stats/server", ""))
	if total := stats["total"].(map[string]any); total["numItems"] != float64(6) || total["numAudioFiles"] != float64(9) {
		t.Fatalf("stats %v", stats)
	}
	stale := filepath.Join(e.meta, "cache", "items", "stale")
	if err := os.MkdirAll(filepath.Dir(stale), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(stale, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if w := e.admin("POST", "/api/cache/items/purge", ""); w.Code != 200 {
		t.Fatalf("purge status %d", w.Code)
	}
	if _, err := os.Stat(stale); err == nil {
		t.Fatal("stale cache file survived the purge")
	}
	if _, err := os.Stat(filepath.Join(e.meta, "cache", "items")); err != nil {
		t.Fatalf("item cache was not recreated: %v", err)
	}
	for body, status := range map[string]int{
		`{"libraryId":"lib","path":"/new","type":"rename","oldPath":"/old"}`: 200,
		`{"libraryId":"lib","path":"/new","type":"rename"}`:                  400,
		`{"libraryId":"lib","path":"/new","type":"bogus"}`:                   400,
		`{}`: 400,
	} {
		if w := e.admin("POST", "/api/watcher/update", body); w.Code != status {
			t.Errorf("%s: status %d, want %d", body, w.Code, status)
		}
	}
	if !reflect.DeepEqual(e.watched, []string{"lib|/new|rename"}) {
		t.Fatalf("watcher calls %v", e.watched)
	}
}

func TestAdminOnlyRoutes(t *testing.T) {
	e := newEnv(t)
	for _, route := range []string{"PATCH /api/settings", "PATCH /api/sorting-prefixes", "GET /api/auth-settings", "PATCH /api/auth-settings",
		"GET /api/logger-data", "POST /api/cache/purge", "GET /api/stats/server", "GET /api/stats/year/2026", "POST /api/watcher/update",
		"GET /api/tags", "POST /api/tags/rename", "DELETE /api/tags/YQ==", "GET /api/genres", "GET /api/filesystem"} {
		method, path, _ := strings.Cut(route, " ")
		for _, user := range []string{"", listenerID} {
			if w := e.do(user, false, httptest.NewRequest(method, path, strings.NewReader(`{}`))); w.Code != 403 {
				t.Errorf("%s as %q: status %d, want 403", route, user, w.Code)
			}
		}
	}
	for body, status := range map[string]int{`{"expression":"0 * * * *"}`: 200, `{"expression":"never"}`: 400, `{}`: 400, ``: 400} {
		w := e.do(listenerID, false, httptest.NewRequest("POST", "/api/validate-cron", strings.NewReader(body)))
		if w.Code != status {
			t.Errorf("validate-cron %q: status %d, want %d", body, w.Code, status)
		}
	}
}

func TestPatchSettings(t *testing.T) {
	e := newEnv(t)
	w := e.admin("PATCH", "/api/settings", `{"language":"fr","tokenSecret":"attacker","unlisted":true,"authOpenIDClientSecret":"x","logLevel":3}`)
	if w.Code != 200 {
		t.Fatalf("status %d %s", w.Code, w.Body)
	}
	view := decode(t, w)["serverSettings"].(map[string]any)
	if view["language"] != "fr" || view["logLevel"] != float64(3) {
		t.Fatalf("view %v", view)
	}
	for _, key := range []string{"tokenSecret", "authOpenIDClientID", "authOpenIDClientSecret", "authOpenIDMobileRedirectURIs", "authOpenIDGroupClaim", "authOpenIDAdvancedPermsClaim"} {
		if _, ok := view[key]; ok {
			t.Errorf("secret setting %s leaked", key)
		}
	}
	var raw string
	e.exec("SELECT 1")
	if err := e.d.Get(t.Context(), &raw, "SELECT value FROM settings WHERE `key` = 'server-settings'"); err != nil || strings.Contains(raw, "attacker") {
		t.Fatalf("stored token secret changed (%v)", err)
	}
	if e.changes != 1 {
		t.Fatalf("OnChange ran %d times, want 1", e.changes)
	}
	e.admin("PATCH", "/api/settings", `{"language":"fr"}`)
	if e.changes != 1 {
		t.Fatalf("an unchanged patch ran OnChange (%d)", e.changes)
	}
	if got := e.store.Server().LogLevel; got != 3 {
		t.Fatalf("logLevel %d", got)
	}
	for _, body := range []string{`{"allowedOrigins":"x"}`, `{"backupsToKeep":"many"}`, `[]`, `{`, ``} {
		if w := e.admin("PATCH", "/api/settings", body); w.Code != 400 {
			t.Errorf("%q: status %d, want 400", body, w.Code)
		}
	}
	e.getenv["ALLOW_IFRAME"] = "1"
	if w := e.admin("PATCH", "/api/settings", `{"allowIframe":false}`); w.Code != 400 {
		t.Errorf("disabling iframe under ALLOW_IFRAME: status %d, want 400", w.Code)
	}
}

func TestPatchAuthSettings(t *testing.T) {
	e := newEnv(t)
	patch := func(body string) map[string]any {
		t.Helper()
		w := e.admin("PATCH", "/api/auth-settings", body)
		if w.Code != 200 {
			t.Fatalf("%s: status %d %s", body, w.Code, w.Body)
		}
		return decode(t, w)
	}
	if patch(`{"authOpenIDIssuerURL":"https://i","authOpenIDClientID":"id","authOpenIDGroupClaim":"groups","authLoginCustomMessage":"hi <script>x</script>"}`)["updated"] != true {
		t.Fatal("first patch did not update")
	}
	server := e.store.Server()
	if server.OpenIDIssuerURL != "https://i" || server.OpenIDClientID != "id" || server.OpenIDGroupClaim != "groups" || strings.Contains(string(server.LoginCustomMessage), "<script>") {
		t.Fatalf("settings %+v", server.Auth())
	}
	if patch(`{"authOpenIDIssuerURL":"https://i"}`)["updated"] != false {
		t.Fatal("same value reported as updated")
	}
	patch(`{"authOpenIDIssuerURL":null,"authOpenIDMobileRedirectURIs":["*","x://y"],"authOpenIDSubfolderForRedirectURLs":""}`) // null clears; "*" with others is ignored
	server = e.store.Server()
	if server.OpenIDIssuerURL != "" || !reflect.DeepEqual(server.OpenIDMobileRedirectURIs, []string{"audiobookshelf://oauth"}) || server.OpenIDSubfolder == nil || *server.OpenIDSubfolder != "" {
		t.Fatalf("settings %+v", server.Auth())
	}
	patch(`{"authOpenIDSubfolderForRedirectURLs":null,"authActiveAuthMethods":["bogus"]}`)
	if e.store.Server().OpenIDSubfolder != nil {
		t.Fatal("null did not unset the subfolder")
	}
	if w := e.admin("PATCH", "/api/auth-settings", `{"authOpenIDAutoLaunch":"yes"}`); w.Code != 400 {
		t.Fatalf("mistyped value: status %d, want 400", w.Code)
	}
	got := decode(t, e.admin("GET", "/api/auth-settings", ""))
	if got["authOpenIDClientID"] != "id" || !strings.Contains(got["authOpenIDSamplePermissions"].(string), "canDownload") {
		t.Fatalf("auth settings %v", got)
	}
}

func TestSortingPrefixes(t *testing.T) {
	e := newEnv(t)
	e.exec(`UPDATE books SET title = 'Der Hobbit', titleIgnorePrefix = 'Der Hobbit' WHERE title = 'The Middle'`)
	e.exec(`UPDATE series SET name = 'Der Ring', nameIgnorePrefix = 'Der Ring'`)
	w := e.admin("PATCH", "/api/sorting-prefixes", `{"sortingPrefixes":[" Der ","der",""]}`)
	if w.Code != 200 || decode(t, w)["rowsUpdated"] == float64(0) {
		t.Fatalf("status %d %s", w.Code, w.Body)
	}
	if got := e.rows(`SELECT titleIgnorePrefix FROM books WHERE title = 'Der Hobbit'`); got != "Hobbit" {
		t.Fatalf("book %q", got)
	}
	if got := e.rows(`SELECT nameIgnorePrefix FROM series`); got != "Ring" {
		t.Fatalf("series %q", got)
	}
	if got := e.store.Server().SortingPrefixes; !reflect.DeepEqual(got, []string{"der"}) {
		t.Fatalf("prefixes %v", got)
	}
	for _, body := range []string{`{"sortingPrefixes":[]}`, `{"sortingPrefixes":[" "]}`, `x`} {
		if w := e.admin("PATCH", "/api/sorting-prefixes", body); w.Code != 400 {
			t.Errorf("%q: status %d, want 400", body, w.Code)
		}
	}
}

func TestLoggerData(t *testing.T) {
	e := newEnv(t)
	logging.Info("admin logger-data marker")
	var out struct {
		CurrentDailyLogs []logging.Entry `json:"currentDailyLogs"`
	}
	if err := json.Unmarshal(e.admin("GET", "/api/logger-data", "").Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	for _, entry := range out.CurrentDailyLogs {
		if entry.Message == "admin logger-data marker" && entry.LevelName == "INFO" {
			return
		}
	}
	t.Fatal("marker entry missing from currentDailyLogs")
}

// TestTaxonomy pins the tag and genre routes: first-occurrence order,
// de-duplication, the new name appended last, the merged flag and which
// items are announced.
func TestTaxonomy(t *testing.T) {
	enc := func(s string) string { return base64.StdEncoding.EncodeToString([]byte(s)) }
	// novel, middle and cast are the tags (or genres) left on the three items
	for _, c := range []struct {
		method, path, body, response, novel, middle, cast string
		touched                                           int
	}{
		{"POST", "/api/tags/rename", `{"tag":"a","newTag":"z"}`, `{"tagMerged":false,"numItemsUpdated":2}`, `["b","z"]`, `["c"]`, `["z"]`, 2},
		{"POST", "/api/tags/rename", `{"tag":"c","newTag":"b"}`, `{"tagMerged":true,"numItemsUpdated":1}`, `["a","b","a"]`, `["b"]`, `["a"]`, 1},
		{"POST", "/api/genres/rename", `{"genre":"old","newGenre":"Fiction"}`, `{"genreMerged":true,"numItemsUpdated":2}`, `["Fiction"]`, `[]`, `["Fiction"]`, 2},
		{"POST", "/api/tags/rename", `{"tag":"","newTag":"z"}`, ``, "", "", "", 0},
		{"DELETE", "/api/tags/" + enc("a"), ``, `{"numItemsUpdated":2}`, `["b"]`, `["c"]`, `[]`, 2},
		{"DELETE", "/api/genres/" + enc("nothing"), ``, `{"numItemsUpdated":0}`, "", "", "", 0},
		{"DELETE", "/api/tags/***", ``, ``, "", "", "", 0},
	} {
		t.Run(c.method+" "+c.path+" "+c.body, func(t *testing.T) {
			e := newEnv(t)
			e.exec(`UPDATE books SET tags = '["a","b","a"]', genres = '["old","Fiction"]' WHERE title = 'Single File Novel'`)
			e.exec(`UPDATE books SET tags = '["c"]' WHERE title = 'The Middle'`)
			e.exec(`UPDATE podcasts SET tags = '["a"]', genres = '["old"]'`)
			w := e.admin(c.method, c.path, c.body)
			if c.response == "" {
				if w.Code != 400 || len(e.touched) != 0 {
					t.Fatalf("status %d, touched %v", w.Code, e.touched)
				}
				return
			}
			var got, want map[string]any
			if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil || json.Unmarshal([]byte(c.response), &want) != nil || !reflect.DeepEqual(got, want) {
				t.Fatalf("%d %s, want %s", w.Code, w.Body, c.response)
			}
			column := "tags"
			if strings.Contains(c.path, "genre") {
				column = "genres"
			}
			if c.novel != "" {
				for query, want := range map[string]string{
					`SELECT ` + column + ` FROM books WHERE title = 'Single File Novel'`: c.novel,
					`SELECT ` + column + ` FROM books WHERE title = 'The Middle'`:        c.middle,
					`SELECT ` + column + ` FROM podcasts`:                                c.cast,
				} {
					if got := e.rows(query); got != want {
						t.Errorf("%s = %s, want %s", query, got, want)
					}
				}
			}
			if len(e.touched) != c.touched {
				t.Fatalf("touched %v, want %d items", e.touched, c.touched)
			}
		})
	}
	e := newEnv(t)
	e.exec(`UPDATE books SET tags = '["B","a","b"]' WHERE title = 'The Middle'`)
	if got := decode(t, e.admin("GET", "/api/tags", ""))["tags"]; !reflect.DeepEqual(got, []any{"a", "B", "b"}) {
		t.Fatalf("tags %v", got)
	}
	if got := decode(t, e.admin("GET", "/api/genres", ""))["genres"]; !reflect.DeepEqual(got, []any{"Fiction", "Technology"}) {
		t.Fatalf("genres %v", got)
	}
}

func TestPathExistsInvalidBodyIsJSON(t *testing.T) {
	e := newEnv(t)
	for _, body := range []string{`{}`, `{"directory":"a"}`, `{"folderPath":"/x"}`, `not json`} {
		w := e.do(listenerID, true, httptest.NewRequest("POST", "/api/filesystem/pathexists", strings.NewReader(body)))
		if w.Code != 400 || w.Header().Get("Content-Type") != "application/json; charset=utf-8" || strings.TrimSpace(w.Body.String()) != `{"error":"Invalid request body"}` {
			t.Errorf("%s: %d %q %q", body, w.Code, w.Header().Get("Content-Type"), w.Body)
		}
	}
	if w := e.do(listenerID, false, httptest.NewRequest("POST", "/api/filesystem/pathexists", strings.NewReader(`{}`))); w.Code != 403 {
		t.Errorf("without the upload permission: status %d, want 403", w.Code)
	}
}

func TestFilesystemAndPathExists(t *testing.T) {
	e := newEnv(t)
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "Ada", "Novel"), 0o755); err != nil {
		t.Fatal(err)
	}
	e.exec("UPDATE libraryFolders SET path = ? WHERE id = ?", root, abFolder)
	e.exec("UPDATE libraryItems SET path = ? WHERE id = '91bb63f7-97c9-4230-8fd0-1dfd994977ea'", filepath.Join(root, "Ada", "Novel"))
	list := decode(t, e.admin("GET", "/api/filesystem?path="+root+"&level=1", ""))
	if dirs := list["directories"].([]any); len(dirs) != 1 || dirs[0].(map[string]any)["dirname"] != "Ada" || dirs[0].(map[string]any)["level"] != float64(1) {
		t.Fatalf("directories %v", list)
	}
	for _, path := range []string{"relative", "/definitely/not/here"} {
		if w := e.admin("GET", "/api/filesystem?path="+path, ""); w.Code != 400 {
			t.Errorf("path %q: status %d, want 400", path, w.Code)
		}
	}
	exists := func(directory string) map[string]any {
		body, _ := json.Marshal(map[string]string{"folderPath": root, "directory": directory})
		w := e.do(listenerID, true, httptest.NewRequest("POST", "/api/filesystem/pathexists", bytes.NewReader(body)))
		if w.Code != 200 {
			return map[string]any{"status": float64(w.Code)}
		}
		return decode(t, w)
	}
	if got := exists("Ada/Novel"); got["exists"] != true || got["libraryItemTitle"] != nil {
		t.Errorf("existing folder: %v", got)
	}
	if got := exists("Ada/Novel/new"); got["exists"] != true || got["libraryItemTitle"] != "Single File Novel" {
		t.Errorf("inside an item: %v", got)
	}
	if got := exists("Other"); got["exists"] != false {
		t.Errorf("free folder: %v", got)
	}
	if got := exists("../escape"); got["status"] != float64(400) {
		t.Errorf("escape: %v", got)
	}
}

func upload(t *testing.T, fields map[string]string, filename string) *http.Request {
	t.Helper()
	var body bytes.Buffer
	w := multipart.NewWriter(&body)
	for k, v := range fields {
		_ = w.WriteField(k, v)
	}
	if filename != "" {
		part, err := w.CreateFormFile("file", filename)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = part.Write([]byte("upload data"))
	}
	_ = w.Close()
	r := httptest.NewRequest("POST", "/api/upload", &body)
	r.Header.Set("Content-Type", w.FormDataContentType())
	return r
}

func TestUpload(t *testing.T) {
	e := newEnv(t)
	root := t.TempDir()
	e.exec("UPDATE libraryFolders SET path = ? WHERE id = ?", root, abFolder)
	fields := map[string]string{"title": "Title", "library": audiobooks, "folder": abFolder, "author": "A/B"}
	if w := e.do(listenerID, true, upload(t, fields, "a:b?.mp3")); w.Code != 200 {
		t.Fatalf("status %d %s", w.Code, w.Body)
	}
	if data, err := os.ReadFile(filepath.Join(root, "AB", "Title", "a - b.mp3")); err != nil || string(data) != "upload data" {
		t.Fatalf("file %q (%v)", data, err)
	}
	for name, c := range map[string]struct {
		r      *http.Request
		upload bool
		status int
	}{
		"denied":         {upload(t, fields, "x"), false, 403},
		"no file":        {upload(t, map[string]string{"title": "x"}, ""), true, 400},
		"missing fields": {upload(t, map[string]string{}, "x"), true, 400},
		"bad library":    {upload(t, map[string]string{"title": "x", "library": "absent", "folder": abFolder}, "x"), true, 404},
		"bad folder":     {upload(t, map[string]string{"title": "x", "library": audiobooks, "folder": "absent"}, "x"), true, 404},
		"not multipart":  {httptest.NewRequest("POST", "/api/upload", strings.NewReader("no")), true, 400},
	} {
		if w := e.do(listenerID, c.upload, c.r); w.Code != c.status {
			t.Errorf("%s: status %d, want %d (%s)", name, w.Code, c.status, strings.TrimSpace(w.Body.String()))
		}
	}
}

func TestSanitizeUploadName(t *testing.T) {
	long := strings.Repeat("é", 300) + ".mp3"
	for in, want := range map[string]string{
		"a:b?.mp3": "a - b.mp3", "..": "", "CON.txt": "", "x  y. ": "x y", "a/b\\c": "abc", long: strings.Repeat("é", 251) + ".mp3",
	} {
		if got := sanitizeUploadName(in); got != want {
			t.Errorf("%q: got %q, want %q", in, got, want)
		}
	}
}

func TestYearStats(t *testing.T) {
	e := newEnv(t)
	got := decode(t, e.admin("GET", "/api/stats/year/2026", ""))
	if got["numBooksAdded"] != float64(5) || got["numBooks"] != float64(5) || got["numAuthorsAdded"] != float64(3) {
		t.Fatalf("year stats %v", got)
	}
	for _, key := range []string{"topAuthors", "topNarrators", "topGenres", "booksAddedWithCovers"} {
		if _, ok := got[key].([]any); !ok {
			t.Errorf("%s is %v, want a list", key, got[key])
		}
	}
	for _, year := range []string{"1999", "abc"} {
		if w := e.admin("GET", "/api/stats/year/"+year, ""); w.Code != 400 {
			t.Errorf("year %s: status %d, want 400", year, w.Code)
		}
	}
}
