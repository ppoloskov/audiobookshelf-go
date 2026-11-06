//go:build tsdiagnostic

package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"audiobookshelf-compat/internal/config"
)

func TestTSRepresentationR04Server(t *testing.T) {
	rt := &Runtime{cfg: config.Config{ClientPath: t.TempDir()}}
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/audiobookshelf/_nuxt/missing.js?cache=1", nil)
	rt.serveClient(w, r, r.URL.Path)
	const want = "<!DOCTYPE html>\n<html lang=\"en\">\n<head>\n<meta charset=\"utf-8\">\n<title>Error</title>\n</head>\n<body>\n<pre>Cannot GET /audiobookshelf/_nuxt/missing.js</pre>\n</body>\n</html>\n"
	if w.Body.String() != want {
		t.Fatalf("finalhandler bytes = %q, want %q", w.Body.String(), want)
	}
	w = httptest.NewRecorder()
	guardPanics(w, httptest.NewRequest(http.MethodGet, "/api/items/abc?token=secret", nil), func(http.ResponseWriter, *http.Request) { panic("boom") })
	if !strings.Contains(w.Body.String(), "<pre>Internal Server Error</pre>") {
		t.Fatalf("panic finalhandler envelope = %q", w.Body.String())
	}
}
