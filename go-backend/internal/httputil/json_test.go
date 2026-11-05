package httputil

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
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

type closeReader struct {
	*strings.Reader
	closed bool
}

func (r *closeReader) Close() error { r.closed = true; return nil }

func TestDecodeObjectRequest(t *testing.T) {
	body := &closeReader{Reader: strings.NewReader(`{"n":12345678901234567890} trailing`)}
	req := httptest.NewRequest(http.MethodPost, "/", nil)
	req.Body = body
	got, err := DecodeObjectRequest(req)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := got["n"].(json.Number); !ok {
		t.Fatalf("number type = %T, want json.Number", got["n"])
	}
	if !body.closed {
		t.Fatal("request body was not closed")
	}
	for _, raw := range []string{"null", "[]"} {
		req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(raw))
		if _, err := DecodeObjectRequest(req); err == nil {
			t.Errorf("DecodeObjectRequest(%s) succeeded, want error", raw)
		}
	}
}
