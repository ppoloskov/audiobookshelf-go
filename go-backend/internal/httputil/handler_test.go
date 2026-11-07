package httputil

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestHandlerMapsErrors(t *testing.T) {
	for _, tc := range []struct {
		err    error
		status int
		body   string
	}{
		{Status(http.StatusNotFound, ""), 404, "Not Found"},
		{Status(http.StatusBadRequest, "Invalid request body"), 400, "Invalid request body"},
		{Internal("database read failed", errors.New("boom")), 500, "database read failed"},
		{errors.New("plain"), 500, "Internal Server Error"},
		{Empty(http.StatusNoContent), 204, ""},
	} {
		w := httptest.NewRecorder()
		Handler(func(http.ResponseWriter, *http.Request) error { return tc.err }).ServeHTTP(w, httptest.NewRequest("GET", "/x", nil))
		if w.Code != tc.status || w.Body.String() != tc.body {
			t.Errorf("%v: got %d %q, want %d %q", tc.err, w.Code, w.Body.String(), tc.status, tc.body)
		}
	}
}

func TestJSONConditionalGet(t *testing.T) {
	serve := func(method, inm string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		r := httptest.NewRequest(method, "/x", nil)
		if inm != "" {
			r.Header.Set("If-None-Match", inm)
		}
		_ = JSON(w, r, map[string]int{"a": 1})
		return w
	}
	first := serve("GET", "")
	etag := first.Header().Get("ETag")
	if first.Code != 200 || etag == "" || first.Body.String() != `{"a":1}` {
		t.Fatalf("first: %d etag=%q body=%q", first.Code, etag, first.Body.String())
	}
	if w := serve("GET", etag); w.Code != http.StatusNotModified || w.Body.Len() != 0 {
		t.Fatalf("revalidate: %d body=%q", w.Code, w.Body.String())
	}
	if w := serve("GET", `"other", `+etag[2:]); w.Code != http.StatusNotModified {
		t.Fatalf("weak list match: %d", w.Code)
	}
	if w := serve("GET", `"stale"`); w.Code != 200 {
		t.Fatalf("stale validator: %d", w.Code)
	}
	if w := serve("POST", etag); w.Code != 200 || w.Header().Get("ETag") != "" {
		t.Fatalf("POST must not be conditional: %d etag=%q", w.Code, w.Header().Get("ETag"))
	}
	if w := serve("HEAD", ""); w.Code != 200 || w.Body.Len() != 0 || w.Header().Get("ETag") != etag {
		t.Fatalf("HEAD: %d body=%q", w.Code, w.Body.String())
	}
}
