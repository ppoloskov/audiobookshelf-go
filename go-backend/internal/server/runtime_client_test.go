package server

import (
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"audiobookshelf-compat/internal/config"
	"golang.org/x/net/html"
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
	for _, method := range []string{"GET", "HEAD"} {
		r := httptest.NewRequest(method, "/audiobookshelf/_nuxt/missing.js?cache=1", nil)
		w := httptest.NewRecorder()
		rt.serveClient(w, r, r.URL.Path)
		if w.Code != 404 || w.Header().Get("Content-Type") != "text/html; charset=utf-8" {
			t.Fatalf("%s missing asset: status=%d headers=%v", method, w.Code, w.Header())
		}
		if w.Header().Get("Content-Security-Policy") != "default-src 'none'" || w.Header().Get("X-Content-Type-Options") != "nosniff" {
			t.Fatalf("missing asset security headers = %v", w.Header())
		}
		if method == "GET" {
			text := errorHTMLText(t, w.Body.String())
			if !strings.Contains(text, r.URL.Path) || strings.Contains(text, "cache=1") {
				t.Fatalf("missing asset information = %q", text)
			}
			if w.Header().Get("Content-Length") != strconv.Itoa(w.Body.Len()) {
				t.Fatalf("missing asset Content-Length = %q, body length %d", w.Header().Get("Content-Length"), w.Body.Len())
			}
		}
		if method == "HEAD" && w.Body.Len() != 0 {
			t.Fatalf("HEAD wrote a body: %q", w.Body.String())
		}
	}
}

// Check visible error information without fixing an HTML template or tag layout.
func errorHTMLText(t *testing.T, body string) string {
	t.Helper()
	doc, err := html.Parse(strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	var text strings.Builder
	var walk func(*html.Node, bool)
	walk = func(n *html.Node, inBody bool) {
		if n.Type == html.ElementNode && (n.Data == "script" || n.Data == "iframe" || n.Data == "object" || n.Data == "embed" || n.Data == "style" || n.Data == "link") {
			t.Fatalf("active content in error page: %s", n.Data)
		}
		for _, attr := range n.Attr {
			if strings.HasPrefix(attr.Key, "on") || attr.Key == "style" || attr.Key == "http-equiv" || attr.Key == "href" || attr.Key == "src" || attr.Key == "action" || attr.Key == "formaction" {
				t.Fatalf("active attribute in error page: %s", attr.Key)
			}
		}
		inBody = inBody || n.Type == html.ElementNode && n.Data == "body"
		if inBody && n.Type == html.TextNode {
			text.WriteString(n.Data)
		}
		for child := n.FirstChild; child != nil; child = child.NextSibling {
			walk(child, inBody)
		}
	}
	walk(doc, false)
	if strings.TrimSpace(text.String()) == "" {
		t.Fatal("error page has no visible error information")
	}
	return text.String()
}

func TestMissingClientAssetEscapesHostilePath(t *testing.T) {
	rt := &Runtime{cfg: config.Config{ClientPath: t.TempDir()}}
	r := httptest.NewRequest("GET", "/_nuxt/%3Cimg%20src=x%20onerror=alert(1)%3E.js?token=secret", nil)
	w := httptest.NewRecorder()
	rt.serveClient(w, r, r.URL.Path)
	if w.Code != 404 || w.Header().Get("Content-Security-Policy") != "default-src 'none'" || w.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Fatalf("hostile missing path = %d, headers %v", w.Code, w.Header())
	}
	text := errorHTMLText(t, w.Body.String())
	if !strings.Contains(text, r.URL.EscapedPath()) || strings.Contains(w.Body.String(), "<img") || strings.Contains(w.Body.String(), "secret") {
		t.Fatalf("hostile path leaked markup or query: %q", w.Body.String())
	}
}
