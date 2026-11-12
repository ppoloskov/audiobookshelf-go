package server

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"

	"audiobookshelf-compat/internal/config"
	"audiobookshelf-compat/internal/logging"
	"audiobookshelf-compat/internal/settings"
)

// newPipelineManager is a manager over a fresh install with one root user,
// served under the router base path, with a client directory.
func newPipelineManager(t *testing.T) (*Manager, string) {
	t.Helper()
	client := t.TempDir()
	for name, body := range map[string]string{"index.html": "<!doctype html>", "js/app.js": "console.log(1)"} {
		if err := os.MkdirAll(filepath.Dir(filepath.Join(client, name)), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(client, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	cfg := config.Config{ConfigPath: filepath.Join(t.TempDir(), "config"), MetadataPath: filepath.Join(t.TempDir(), "metadata"), RouterBasePath: "/audiobookshelf", ClientPath: client, JWTSecret: "test-secret"}
	manager, _ := newTestManager(t, cfg, "pipeline-user", "password")
	t.Cleanup(manager.Close)
	return manager, makeToken(t, cfg.JWTSecret, `{"userId":"root-test","username":"pipeline-user","type":"access","exp":4102444800}`)
}

func serve(h http.Handler, method, target, token string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, target, nil)
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func TestPublicEndpoints(t *testing.T) {
	manager, _ := newPipelineManager(t)
	cases := []struct{ path, contentType, body string }{
		{"/ping", "application/json", `{"success":true}`},
		{"/audiobookshelf/ping", "application/json", `{"success":true}`},
		{"/ping/", "application/json", `{"success":true}`},
		{"/healthcheck", "text/plain; charset=utf-8", "OK"},
		{"/audiobookshelf/healthcheck", "text/plain; charset=utf-8", "OK"},
	}
	for _, c := range cases {
		w := serve(manager, "GET", c.path, "")
		if w.Code != 200 || !strings.HasPrefix(w.Header().Get("Content-Type"), c.contentType) || strings.TrimSpace(w.Body.String()) != c.body {
			t.Errorf("%s: status=%d type=%q body=%q", c.path, w.Code, w.Header().Get("Content-Type"), w.Body.String())
		}
	}
}

func TestStatus(t *testing.T) {
	manager, _ := newPipelineManager(t)
	for _, path := range []string{"/status", "/audiobookshelf/status"} {
		w := serve(manager, "GET", path, "")
		want := `{"app":"audiobookshelf","serverVersion":"2.37.2","isInit":true,"language":"en-us","authMethods":["local"],"authFormData":{"authLoginCustomMessage":""}}`
		if w.Code != 200 || strings.TrimSpace(w.Body.String()) != want {
			t.Errorf("%s: status=%d body=%s", path, w.Code, w.Body.String())
		}
	}
}

func TestAPIRequiresAuthAndUnknownRoutesAre404(t *testing.T) {
	manager, token := newPipelineManager(t)
	cases := []struct {
		name, method, path, token string
		status                    int
	}{
		{"no token", "GET", "/api/libraries", "", 401},
		{"no token under base path", "GET", "/audiobookshelf/api/libraries", "", 401},
		{"bare /api", "GET", "/api", "", 401},
		{"unknown route", "GET", "/api/no-such-route", token, 404},
		{"unknown route without token", "GET", "/api/no-such-route", "", 401},
		{"known route", "GET", "/api/libraries", token, 200},
		{"one trailing slash", "GET", "/api/libraries/", token, 200},
		{"trailing slash under base path", "GET", "/audiobookshelf/api/libraries/", token, 200},
		{"wrong method", "DELETE", "/api/libraries", token, 404},
		{"public cover needs no token", "GET", "/api/items/00000000-0000-0000-0000-000000000000/cover", "", 404},
		{"public author image needs no token", "GET", "/api/authors/00000000-0000-0000-0000-000000000000/image", "", 404},
		{"item without token", "GET", "/api/items/00000000-0000-0000-0000-000000000000", "", 401},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			w := serve(manager, c.method, c.path, c.token)
			if w.Code != c.status {
				t.Fatalf("status=%d body=%q, want %d", w.Code, w.Body.String(), c.status)
			}
			if c.status == 401 && w.Body.String() != "Unauthorized" {
				t.Fatalf("401 body=%q", w.Body.String())
			}
		})
	}
}

func TestClientServedWithSPAFallbackAndNoListing(t *testing.T) {
	manager, _ := newPipelineManager(t)
	cases := []struct {
		name, method, path string
		status             int
		body               string
	}{
		{"asset", "GET", "/js/app.js", 200, "console.log(1)"},
		{"asset under base path", "GET", "/audiobookshelf/js/app.js", 200, "console.log(1)"},
		{"root", "GET", "/", 200, "<!doctype html>"},
		{"base path root", "GET", "/audiobookshelf/", 200, "<!doctype html>"},
		{"client route", "GET", "/library/lib-1/bookshelf", 200, "<!doctype html>"},
		{"client route with trailing slash", "GET", "/item/abc/", 200, "<!doctype html>"},
		{"directory is not listed", "GET", "/js", 200, "<!doctype html>"},
		{"head", "HEAD", "/item/abc", 200, ""},
		{"missing asset", "GET", "/_nuxt/missing.js", 404, ""},
		{"post is not served", "POST", "/item/abc", 404, ""},
		{"traversal", "GET", "/../../etc/passwd", 404, ""},
		{"double slash", "GET", "/item//abc", 404, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			w := serve(manager, c.method, c.path, "")
			if w.Code != c.status || (c.body != "" && w.Body.String() != c.body) {
				t.Fatalf("status=%d body=%q", w.Code, w.Body.String())
			}
			if strings.Contains(w.Body.String(), "app.js") && c.name == "directory is not listed" {
				t.Fatal("directory listing")
			}
		})
	}
}

func TestBareBasePathRedirectsToItsRoot(t *testing.T) {
	manager, _ := newPipelineManager(t)
	w := serve(manager, "GET", "/audiobookshelf", "")
	if w.Code != http.StatusPermanentRedirect || w.Header().Get("Location") != "/audiobookshelf/" {
		t.Fatalf("status=%d location=%q", w.Code, w.Header().Get("Location"))
	}
}

// Recover is outermost: a panic deep in the chain still answers 500 as plain
// text and keeps the headers the inner middleware set.
func TestPanicAnswers500WithSecurityHeaders(t *testing.T) {
	manager, _ := newPipelineManager(t)
	rt := manager.current.Load()
	panicking := chain(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { panic("boom") }), rt.middlewares(false)...)
	r := httptest.NewRequest("GET", "/api/items/abc?token=secret", nil)
	w := httptest.NewRecorder()
	panicking.ServeHTTP(w, r)
	if w.Code != 500 || w.Body.String() != "Internal Server Error" || !strings.HasPrefix(w.Header().Get("Content-Type"), "text/plain") {
		t.Fatalf("status=%d type=%q body=%q", w.Code, w.Header().Get("Content-Type"), w.Body.String())
	}
	if w.Header().Get("Referrer-Policy") != "no-referrer" || w.Header().Get("Content-Security-Policy") != "frame-ancestors 'self'" {
		t.Fatalf("security headers missing: %v", w.Header())
	}
	found := false
	for _, e := range logging.Default().Entries() {
		if e.LevelName == "ERROR" && strings.Contains(e.Message, "[HTTP] GET /api/items/abc: panic: boom") && strings.Contains(e.Message, "goroutine") {
			found = true
			if strings.Contains(e.Message, "secret") {
				t.Fatal("query string leaked into the log")
			}
		}
	}
	if !found {
		t.Fatal("panic with stack not logged at ERROR")
	}
}

func TestPanicAfterResponseStartedOnlyLogs(t *testing.T) {
	w := httptest.NewRecorder()
	recoverPanics(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(200)
		_, _ = w.Write([]byte("partial"))
		panic("late")
	})).ServeHTTP(w, httptest.NewRequest("GET", "/x", nil))
	if w.Code != 200 || w.Body.String() != "partial" {
		t.Fatalf("status=%d body=%q", w.Code, w.Body.String())
	}
}

func TestAbortHandlerPanicPropagates(t *testing.T) {
	defer func() {
		if recover() != http.ErrAbortHandler {
			t.Fatal("http.ErrAbortHandler must propagate")
		}
	}()
	recoverPanics(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { panic(http.ErrAbortHandler) })).ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/", nil))
}

func TestAllowIframeDropsFrameAncestors(t *testing.T) {
	manager, _ := newPipelineManager(t)
	if got := serve(manager, "GET", "/ping", "").Header().Get("Content-Security-Policy"); got != "frame-ancestors 'self'" {
		t.Fatalf("CSP = %q", got)
	}
	rt := manager.current.Load()
	if _, err := rt.settings.UpdateServer(t.Context(), func(s *settings.Server) error { s.AllowIframe = true; return nil }); err != nil {
		t.Fatal(err)
	}
	w := serve(manager, "GET", "/ping", "")
	if w.Header().Get("Content-Security-Policy") != "" || w.Header().Get("Referrer-Policy") != "no-referrer" {
		t.Fatalf("headers = %v", w.Header())
	}
}

// The CORS rule of server/Server.js:270-295, case by case.
func TestCORSAllowed(t *testing.T) {
	const cover = "/api/items/b3c61dfc-dd4b-47ee-8998-0340c1228f52/cover"
	const ebook = "/audiobookshelf/api/items/b3c61dfc-dd4b-47ee-8998-0340c1228f52/ebook/3"
	configured := []string{"https://abs.example"}
	cases := []struct {
		name     string
		allowAll bool
		allowed  []string
		path     string
		origin   string
		want     bool
	}{
		{"cover from capacitor", false, nil, cover, "capacitor://localhost", true},
		{"cover from localhost", false, nil, cover, "http://localhost", true},
		{"ebook with file index under base path", false, nil, ebook, "capacitor://localhost", true},
		{"cover from unknown origin", false, nil, cover, "https://evil.example", false},
		{"cover from configured origin", false, configured, cover, "https://abs.example", true},
		{"other api path, nothing configured", false, nil, "/api/libraries", "capacitor://localhost", false},
		{"other api path, origins configured", false, configured, "/api/libraries", "https://abs.example", true},
		{"other api path, mobile origin with origins configured", false, configured, "/api/libraries", "http://localhost", true},
		{"other api path, unknown origin with origins configured", false, configured, "/api/libraries", "https://evil.example", false},
		{"cover item id is not a uuid", false, nil, "/api/items/short/cover", "capacitor://localhost", false},
		{"cover item id uppercase", false, nil, "/api/items/B3C61DFC-DD4B-47EE-8998-0340C1228F52/cover", "capacitor://localhost", false},
		{"allow all", true, nil, "/api/libraries", "https://anything.example", true},
		{"no origin", false, configured, cover, "", false},
		{"no origin, allow all", true, nil, cover, "", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := corsAllowed(c.allowAll, c.allowed, c.path, c.origin); got != c.want {
				t.Fatalf("corsAllowed = %v, want %v", got, c.want)
			}
		})
	}
}

func TestCORSHeadersAndPreflight(t *testing.T) {
	manager, _ := newPipelineManager(t)
	const cover = "/audiobookshelf/api/items/b3c61dfc-dd4b-47ee-8998-0340c1228f52/cover"
	r := httptest.NewRequest("OPTIONS", cover, nil)
	r.Header.Set("Origin", "capacitor://localhost")
	w := httptest.NewRecorder()
	manager.ServeHTTP(w, r)
	want := map[string]string{
		"Access-Control-Allow-Origin":      "capacitor://localhost",
		"Access-Control-Allow-Methods":     "GET, POST, PATCH, PUT, DELETE, OPTIONS",
		"Access-Control-Allow-Headers":     "*",
		"Access-Control-Allow-Credentials": "true",
	}
	for name, value := range want {
		if w.Header().Get(name) != value {
			t.Errorf("%s = %q, want %q", name, w.Header().Get(name), value)
		}
	}
	if w.Code != 200 || w.Body.String() != "OK" {
		t.Errorf("preflight status=%d body=%q", w.Code, w.Body.String())
	}
	r = httptest.NewRequest("OPTIONS", cover, nil)
	r.Header.Set("Origin", "https://evil.example")
	w = httptest.NewRecorder()
	manager.ServeHTTP(w, r)
	if w.Header().Get("Access-Control-Allow-Origin") != "" || w.Code == 200 && w.Body.String() == "OK" {
		t.Errorf("disallowed origin got CORS: status=%d headers=%v", w.Code, w.Header())
	}
}

func TestMiddlewareOrder(t *testing.T) {
	manager, _ := newPipelineManager(t)
	var names []string
	for _, m := range manager.current.Load().middlewares(false) {
		names = append(names, funcName(m))
	}
	want := []string{"recoverPanics", "securityHeaders", "corsPolicy", "basePath", "cleanPath"}
	if strings.Join(names, ",") != strings.Join(want, ",") {
		t.Fatalf("pipeline = %v, want %v", names, want)
	}
}

// funcName is the declared name of a middleware constructor or middleware.
func funcName(m middleware) string {
	name := runtime.FuncForPC(reflect.ValueOf(m).Pointer()).Name()
	return strings.Split(name[strings.LastIndex(name, "/")+1:], ".")[1] // "server.name[.func1]"
}

// The access line is DEBUG, the liveness probes TRACE, and nothing is logged
// at the default level.
func TestAccessLineLevels(t *testing.T) {
	l := logging.Default()
	before := l.Level()
	t.Cleanup(func() { l.SetLevel(before) })
	count := func(path string, level logging.Level) int {
		n := 0
		for _, e := range l.Entries() {
			if strings.Contains(e.Message, "[HTTP] GET "+path+" ") && e.Level == int(level) {
				n++
			}
		}
		return n
	}
	visit := func(path string) {
		recoverPanics(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(204) })).ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", path, nil))
	}
	for _, c := range []struct {
		path        string
		level, deny logging.Level
	}{
		{"/access-line-probe", logging.LevelDebug, logging.LevelTrace},
		{"/healthcheck", logging.LevelTrace, logging.LevelDebug},
		{"/audiobookshelf/ping", logging.LevelTrace, logging.LevelDebug},
	} {
		l.SetLevel(logging.LevelInfo)
		seen := count(c.path, c.level)
		visit(c.path)
		if count(c.path, c.level) != seen {
			t.Errorf("%s logged at INFO", c.path)
		}
		l.SetLevel(c.level)
		visit(c.path)
		if count(c.path, c.level)-seen != 1 || count(c.path, c.deny) != 0 {
			t.Errorf("%s not logged once at %v", c.path, c.level)
		}
	}
}

func TestCloseStopsServingAndIsIdempotent(t *testing.T) {
	manager, _ := newPipelineManager(t)
	manager.Close()
	if w := serve(manager, "GET", "/ping", ""); w.Code != http.StatusServiceUnavailable {
		t.Fatalf("status after Close = %d", w.Code)
	}
	manager.Close()
}
