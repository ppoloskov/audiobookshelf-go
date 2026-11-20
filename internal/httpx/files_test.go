package httpx

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeTemp(t *testing.T, name, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func serve(h Handler, r *http.Request) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r)
	return rec
}

func TestServeFile(t *testing.T) {
	path := writeTemp(t, "book.mp3", "0123456789")
	handler := func(p string) Handler {
		return func(w http.ResponseWriter, r *http.Request) error { return ServeFile(w, r, p) }
	}
	t.Run("full", func(t *testing.T) {
		rec := serve(handler(path), httptest.NewRequest("GET", "/", nil))
		if rec.Code != 200 || rec.Body.String() != "0123456789" || rec.Header().Get("Accept-Ranges") != "bytes" {
			t.Fatalf("got %d %q %v", rec.Code, rec.Body.String(), rec.Header())
		}
	})
	t.Run("range", func(t *testing.T) {
		r := httptest.NewRequest("GET", "/", nil)
		r.Header.Set("Range", "bytes=2-5")
		rec := serve(handler(path), r)
		if rec.Code != 206 || rec.Body.String() != "2345" || rec.Header().Get("Content-Range") != "bytes 2-5/10" {
			t.Fatalf("got %d %q %q", rec.Code, rec.Body.String(), rec.Header().Get("Content-Range"))
		}
	})
	t.Run("conditional", func(t *testing.T) {
		first := serve(handler(path), httptest.NewRequest("GET", "/", nil))
		r := httptest.NewRequest("GET", "/", nil)
		r.Header.Set("If-None-Match", first.Header().Get("ETag"))
		if rec := serve(handler(path), r); rec.Code != 304 {
			t.Fatalf("status = %d", rec.Code)
		}
	})
	for name, missing := range map[string]string{
		"missing":   filepath.Join(t.TempDir(), "nope.mp3"),
		"directory": t.TempDir(),
	} {
		t.Run(name, func(t *testing.T) {
			logs := captureLog(t)
			rec := serve(handler(missing), httptest.NewRequest("GET", "/", nil))
			if rec.Code != 404 || rec.Body.String() != "Not Found" || rec.Header().Get("Content-Disposition") != "" || logs.Len() != 0 {
				t.Fatalf("got %d %q log=%q", rec.Code, rec.Body.String(), logs.String())
			}
		})
	}
}

func TestDownload(t *testing.T) {
	path := writeTemp(t, "stored.bin", "data")
	tests := []struct{ name, filename, want string }{
		{"ascii", "My Book.m4b", `attachment; filename="My Book.m4b"`},
		{"non-ascii", "Война и мир.m4b", "attachment; filename*=utf-8''%D0%92%D0%BE%D0%B9%D0%BD%D0%B0%20%D0%B8%20%D0%BC%D0%B8%D1%80.m4b"},
		{"quotes", `a"b.txt`, `attachment; filename="a\"b.txt"`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rec := serve(func(w http.ResponseWriter, r *http.Request) error { return Download(w, r, path, tc.filename) }, httptest.NewRequest("GET", "/", nil))
			if rec.Code != 200 || rec.Body.String() != "data" || rec.Header().Get("Content-Disposition") != tc.want {
				t.Fatalf("got %d %q disposition=%q", rec.Code, rec.Body.String(), rec.Header().Get("Content-Disposition"))
			}
		})
	}
	t.Run("header injection", func(t *testing.T) {
		rec := serve(func(w http.ResponseWriter, r *http.Request) error { return Download(w, r, path, "a\r\nX-Evil: 1") }, httptest.NewRequest("GET", "/", nil))
		if rec.Header().Get("X-Evil") != "" || strings.ContainsAny(rec.Header().Get("Content-Disposition"), "\r\n") {
			t.Fatalf("header injected: %v", rec.Header())
		}
	})
	t.Run("missing", func(t *testing.T) {
		rec := serve(func(w http.ResponseWriter, r *http.Request) error { return Download(w, r, path+".gone", "x") }, httptest.NewRequest("GET", "/", nil))
		if rec.Code != 404 || rec.Header().Get("Content-Disposition") != "" {
			t.Fatalf("got %d %v", rec.Code, rec.Header())
		}
	})
}
