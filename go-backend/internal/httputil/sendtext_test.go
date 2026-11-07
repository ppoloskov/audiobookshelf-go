package httputil

import (
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
)

func TestSendTextIsExpressSend(t *testing.T) {
	rec := httptest.NewRecorder()
	body := "Invalid request body"
	SendText(rec, http.StatusBadRequest, body)
	if rec.Code != http.StatusBadRequest || rec.Body.String() != body {
		t.Fatalf("got %d %q", rec.Code, rec.Body.String())
	}
	if got, want := rec.Header().Get("Content-Type"), "text/html; charset=utf-8"; got != want {
		t.Fatalf("Content-Type = %q, want %q", got, want)
	}
	if got, want := rec.Header().Get("Content-Length"), strconv.Itoa(len(body)); got != want {
		t.Fatalf("Content-Length = %q, want %q", got, want)
	}
	assertOpaqueETag(t, rec.Header().Get("ETag"))
}

func TestSendTextContentType(t *testing.T) {
	rec := httptest.NewRecorder()
	SendText(rec, http.StatusOK, "x", ContentType("text/plain; charset=utf-8"))
	if got, want := rec.Header().Get("Content-Type"), "text/plain; charset=utf-8"; got != want {
		t.Fatalf("Content-Type = %q, want %q", got, want)
	}
}
