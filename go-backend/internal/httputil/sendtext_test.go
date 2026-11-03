package httputil

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestSendTextIsExpressSend(t *testing.T) {
	rec := httptest.NewRecorder()
	SendText(rec, http.StatusBadRequest, "Invalid request body")
	if rec.Code != http.StatusBadRequest || rec.Body.String() != "Invalid request body" {
		t.Fatalf("got %d %q", rec.Code, rec.Body.String())
	}
	if got, want := rec.Header().Get("Content-Type"), "text/html; charset=utf-8"; got != want {
		t.Fatalf("Content-Type = %q, want %q", got, want)
	}
	if got, want := rec.Header().Get("Content-Length"), "20"; got != want {
		t.Fatalf("Content-Length = %q, want %q", got, want)
	}
	if got, want := rec.Header().Get("ETag"), ExpressETag([]byte("Invalid request body")); got != want {
		t.Fatalf("ETag = %q, want %q", got, want)
	}
}

func TestSendTextOptions(t *testing.T) {
	rec := httptest.NewRecorder()
	SendText(rec, http.StatusOK, "x", ContentType("text/plain; charset=utf-8"), WithoutContentLength(), WithoutETag())
	if got, want := rec.Header().Get("Content-Type"), "text/plain; charset=utf-8"; got != want {
		t.Fatalf("Content-Type = %q, want %q", got, want)
	}
	if rec.Header().Get("Content-Length") != "" || rec.Header().Get("ETag") != "" {
		t.Fatalf("headers = %v", rec.Header())
	}
}
