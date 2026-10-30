package server

import (
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"audiobookshelf-compat/internal/config"
)

func TestServeClientDynamicRoutesAndUnknownPath404(t *testing.T) {
	root := t.TempDir()
	assets := filepath.Join(root, "js")
	if err := os.MkdirAll(assets, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "index.html"), []byte("<!doctype html>"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(assets, "app.js"), []byte("console.log(1)"), 0o644); err != nil {
		t.Fatal(err)
	}
	rt := &Runtime{cfg: config.Config{ClientPath: root}}

	cases := []struct {
		name   string
		method string
		path   string
		status int
		body   string
	}{
		{"asset wins", "GET", "/js/app.js", 200, "console.log(1)"},
		{"index at root", "GET", "/", 200, "<!doctype html>"},
		{"library dynamic route", "GET", "/library/lib-1", 200, "<!doctype html>"},
		{"library optional segment", "GET", "/library/lib-1/bookshelf", 200, "<!doctype html>"},
		{"library bookshelf id", "GET", "/library/lib-1/bookshelf/abc", 200, "<!doctype html>"},
		{"item dynamic route", "GET", "/item/abc-123", 200, "<!doctype html>"},
		{"share slug", "GET", "/share/s1", 200, "<!doctype html>"},
		{"trailing slash tolerated", "GET", "/library/lib-1/", 200, "<!doctype html>"},
		{"config user sessions", "GET", "/config/users/u1/sessions", 200, "<!doctype html>"},
		{"head dynamic route", "HEAD", "/item/abc-123", 200, ""},
		{"unknown path 404", "GET", "/totally-unknown-page", 404, ""},
		{"unknown nested 404", "GET", "/library/lib-1/unknown-nested", 404, ""},
		{"post not served", "POST", "/item/abc-123", 404, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			r := httptest.NewRequest(c.method, "http://127.0.0.1"+c.path, nil)
			rt.serveClient(w, r, c.path)
			if w.Code != c.status {
				t.Fatalf("status = %d, want %d", w.Code, c.status)
			}
			if c.body != "" && w.Body.String() != c.body {
				t.Fatalf("body = %q, want %q", w.Body.String(), c.body)
			}
		})
	}
}

func TestMissingClientAssetMatchesExpressFinalhandler(t *testing.T) {
	rt := &Runtime{cfg: config.Config{ClientPath: t.TempDir()}}
	const want = "<!DOCTYPE html>\n<html lang=\"en\">\n<head>\n<meta charset=\"utf-8\">\n<title>Error</title>\n</head>\n<body>\n<pre>Cannot GET /audiobookshelf/_nuxt/missing.js</pre>\n</body>\n</html>\n"
	for _, method := range []string{"GET", "HEAD"} {
		r := httptest.NewRequest(method, "/audiobookshelf/_nuxt/missing.js?cache=1", nil)
		w := httptest.NewRecorder()
		rt.serveClient(w, r, r.URL.Path)
		if w.Code != 404 || w.Header().Get("Content-Type") != "text/html; charset=utf-8" {
			t.Fatalf("%s missing asset: status=%d headers=%v", method, w.Code, w.Header())
		}
		if method == "GET" && w.Body.String() != want {
			t.Fatalf("missing asset body = %q, want %q", w.Body.String(), want)
		}
		if method == "HEAD" && w.Body.Len() != 0 {
			t.Fatalf("HEAD wrote a body: %q", w.Body.String())
		}
	}
}
