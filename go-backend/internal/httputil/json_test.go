package httputil

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestWriteJSONFramesLikeExpress(t *testing.T) {
	rec := httptest.NewRecorder()
	WriteJSON(rec, http.StatusCreated, map[string]any{"b": 1, "a": "<&>"})
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d", rec.Code)
	}
	if got, want := rec.Header().Get("Content-Type"), "application/json; charset=utf-8"; got != want {
		t.Fatalf("Content-Type = %q, want %q", got, want)
	}
	body := rec.Body.String()
	if want := `{"a":"\u003c\u0026\u003e","b":1}`; body != want {
		t.Fatalf("body = %q, want %q", body, want)
	}
	if got, want := rec.Header().Get("Content-Length"), "32"; got != want {
		t.Fatalf("Content-Length = %q, want %q", got, want)
	}
}

func TestWriteJSONHeadKeepsHeadersAndDropsBody(t *testing.T) {
	req := httptest.NewRequest(http.MethodHead, "/ping", nil)
	rec := httptest.NewRecorder()
	WriteJSON(rec, http.StatusOK, map[string]bool{"success": true}, ForRequest(req))
	if rec.Body.Len() != 0 {
		t.Fatalf("HEAD body = %q, want empty", rec.Body.String())
	}
	if got, want := rec.Header().Get("Content-Length"), "16"; got != want {
		t.Fatalf("HEAD Content-Length = %q, want %q", got, want)
	}

	get := httptest.NewRequest(http.MethodGet, "/ping", nil)
	rec = httptest.NewRecorder()
	WriteJSON(rec, http.StatusOK, map[string]bool{"success": true}, ForRequest(get))
	if got, want := rec.Body.String(), `{"success":true}`; got != want {
		t.Fatalf("GET body = %q, want %q", got, want)
	}
}

func TestWriteJSONStreamedKeepsEncoderFraming(t *testing.T) {
	rec := httptest.NewRecorder()
	WriteJSON(rec, http.StatusOK, map[string]int{"a": 1}, Streamed())
	if got, want := rec.Body.String(), "{\"a\":1}\n"; got != want {
		t.Fatalf("body = %q, want %q", got, want)
	}
	if got := rec.Header().Get("Content-Length"); got != "" {
		t.Fatalf("Content-Length = %q, want none", got)
	}

	head := httptest.NewRequest(http.MethodHead, "/x", nil)
	rec = httptest.NewRecorder()
	WriteJSON(rec, http.StatusOK, map[string]int{"a": 1}, Streamed(), ForRequest(head))
	if rec.Body.Len() != 0 || rec.Header().Get("Content-Type") != "application/json; charset=utf-8" {
		t.Fatalf("streamed HEAD = %q %q", rec.Body.String(), rec.Header().Get("Content-Type"))
	}
}

func TestWriteJSONWithoutContentLength(t *testing.T) {
	rec := httptest.NewRecorder()
	WriteJSON(rec, http.StatusOK, []int{1}, WithoutContentLength())
	if got := rec.Header().Get("Content-Length"); got != "" {
		t.Fatalf("Content-Length = %q, want none", got)
	}
	if got, want := rec.Body.String(), "[1]"; got != want {
		t.Fatalf("body = %q, want %q", got, want)
	}
}

func TestWriteJSONMarshalFailureIsPlain500(t *testing.T) {
	rec := httptest.NewRecorder()
	WriteJSON(rec, http.StatusOK, map[string]any{"c": make(chan int)})
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}
}

func TestWriteJSONOrderedKeys(t *testing.T) {
	rec := httptest.NewRecorder()
	value := map[string]any{"b": 2, "a": nil, "z": "dropped", "c": []int{1}}
	WriteJSON(rec, http.StatusOK, value, OrderedKeys([]string{"c", "a", "missing", "b"}))
	if got, want := rec.Body.String(), `{"c":[1],"a":null,"b":2}`; got != want {
		t.Fatalf("body = %q, want %q", got, want)
	}
	if got, want := rec.Header().Get("Content-Length"), "24"; got != want {
		t.Fatalf("Content-Length = %q, want %q", got, want)
	}
}
