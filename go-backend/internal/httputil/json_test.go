package httputil

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
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
	var got map[string]any
	decodeOneJSONDocument(t, rec.Body.String(), &got)
	if len(got) != 2 || got["a"] != "<&>" || got["b"] != float64(1) {
		t.Fatalf("decoded body = %#v, want a string and b number", got)
	}
	if got, want := rec.Header().Get("Content-Length"), strconv.Itoa(rec.Body.Len()); got != want {
		t.Fatalf("Content-Length = %q, want emitted body length %q", got, want)
	}
}

func TestWriteJSONHeadKeepsHeadersAndDropsBody(t *testing.T) {
	req := httptest.NewRequest(http.MethodHead, "/ping", nil)
	rec := httptest.NewRecorder()
	WriteJSON(rec, http.StatusOK, map[string]bool{"success": true}, ForRequest(req))
	if rec.Body.Len() != 0 {
		t.Fatalf("HEAD body = %q, want empty", rec.Body.String())
	}
	headLength := rec.Header().Get("Content-Length")

	get := httptest.NewRequest(http.MethodGet, "/ping", nil)
	rec = httptest.NewRecorder()
	WriteJSON(rec, http.StatusOK, map[string]bool{"success": true}, ForRequest(get))
	var got map[string]any
	decodeOneJSONDocument(t, rec.Body.String(), &got)
	if len(got) != 1 || got["success"] != true {
		t.Fatalf("GET body = %#v, want success=true", got)
	}
	if want := strconv.Itoa(rec.Body.Len()); headLength != want {
		t.Fatalf("HEAD Content-Length = %q, want GET body length %q", headLength, want)
	}
}

func TestWriteJSONStreamedKeepsEncoderFraming(t *testing.T) {
	rec := httptest.NewRecorder()
	WriteJSON(rec, http.StatusOK, map[string]int{"a": 1}, Streamed())
	var got map[string]any
	decodeOneJSONDocument(t, rec.Body.String(), &got)
	if len(got) != 1 || got["a"] != float64(1) {
		t.Fatalf("decoded streamed body = %#v, want a=1", got)
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
	var got []int
	decodeOneJSONDocument(t, rec.Body.String(), &got)
	if len(got) != 1 || got[0] != 1 {
		t.Fatalf("decoded body = %v, want [1]", got)
	}
}

func decodeOneJSONDocument(t *testing.T, body string, dst any) {
	t.Helper()
	dec := json.NewDecoder(strings.NewReader(body))
	if err := dec.Decode(dst); err != nil {
		t.Fatalf("decode JSON document: %v", err)
	}
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		t.Fatalf("trailing JSON data: decode error = %v", err)
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
