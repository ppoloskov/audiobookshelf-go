package httpx

import (
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
)

func serveJSON(method, ifNoneMatch string, value any) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, "/x", nil)
	if ifNoneMatch != "" {
		r.Header.Set("If-None-Match", ifNoneMatch)
	}
	rec := httptest.NewRecorder()
	if err := JSON(rec, r, value); err != nil {
		panic(err)
	}
	return rec
}

func TestJSON(t *testing.T) {
	value := map[string]string{"a": "<&>"}
	first := serveJSON("GET", "", value)
	if first.Code != 200 || first.Body.String() != `{"a":"<&>"}` {
		t.Fatalf("got %d %q", first.Code, first.Body.String())
	}
	if first.Header().Get("Content-Type") != "application/json; charset=utf-8" {
		t.Fatalf("content type = %q", first.Header().Get("Content-Type"))
	}
	if first.Header().Get("Content-Length") != strconv.Itoa(first.Body.Len()) {
		t.Fatalf("content length = %q", first.Header().Get("Content-Length"))
	}
	etag := first.Header().Get("ETag")
	if len(etag) < 5 || etag[:3] != `W/"` {
		t.Fatalf("etag = %q", etag)
	}

	tests := []struct {
		name        string
		method      string
		ifNoneMatch string
		status      int
		wantBody    bool
	}{
		{"get without header", "GET", "", 200, true},
		{"exact match", "GET", etag, 304, false},
		{"strong form of the tag", "GET", etag[2:], 304, false},
		{"list with match", "GET", `"other", ` + etag, 304, false},
		{"star", "GET", "*", 304, false},
		{"different tag", "GET", `W/"1-abc"`, 200, true},
		{"head", "HEAD", "", 200, false},
		{"head match", "HEAD", etag, 304, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rec := serveJSON(tc.method, tc.ifNoneMatch, value)
			if rec.Code != tc.status || (rec.Body.Len() > 0) != tc.wantBody {
				t.Fatalf("got %d body %q", rec.Code, rec.Body.String())
			}
			if rec.Header().Get("ETag") != etag {
				t.Fatalf("etag = %q, want %q", rec.Header().Get("ETag"), etag)
			}
			if tc.method == "HEAD" && tc.status == 200 && rec.Header().Get("Content-Length") != strconv.Itoa(len(`{"a":"<&>"}`)) {
				t.Fatalf("HEAD content length = %q", rec.Header().Get("Content-Length"))
			}
		})
	}

	if other := serveJSON("GET", "", map[string]string{"a": "b"}); other.Header().Get("ETag") == etag {
		t.Fatal("different bodies share an ETag")
	}
}

func TestJSONStatus(t *testing.T) {
	tests := []struct {
		name   string
		method string
		status int
		etag   bool
	}{
		{"created", "POST", 201, false},
		{"get with non-200 has no etag", "GET", 404, false},
		{"get 200", "GET", 200, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			if err := JSONStatus(rec, httptest.NewRequest(tc.method, "/", nil), tc.status, []int{1}); err != nil {
				t.Fatal(err)
			}
			if rec.Code != tc.status || rec.Body.String() != "[1]" || (rec.Header().Get("ETag") != "") != tc.etag {
				t.Fatalf("got %d %q etag=%q", rec.Code, rec.Body.String(), rec.Header().Get("ETag"))
			}
		})
	}
}

func TestJSONUnencodable(t *testing.T) {
	rec := httptest.NewRecorder()
	err := JSON(rec, httptest.NewRequest("GET", "/", nil), make(chan int))
	if err == nil || rec.Body.Len() != 0 {
		t.Fatalf("err = %v, body = %q", err, rec.Body.String())
	}
	Handler(func(http.ResponseWriter, *http.Request) error { return err }).ServeHTTP(rec, httptest.NewRequest("GET", "/", nil))
	if rec.Code != 500 || rec.Body.String() != "Internal Server Error" {
		t.Fatalf("got %d %q", rec.Code, rec.Body.String())
	}
}
