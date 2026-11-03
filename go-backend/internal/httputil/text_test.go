package httputil

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestExpressETagMatchesExpress(t *testing.T) {
	// Express: the ETag of res.sendStatus(200), body "OK".
	if got, want := ExpressETag([]byte("OK")), `W/"2-nOO9QiTIwXgNtWtBJezz8kv3SLc"`; got != want {
		t.Fatalf("ETag(OK) = %s, want %s", got, want)
	}
}

func TestSendStatusIsExpressSendStatus(t *testing.T) {
	rec := httptest.NewRecorder()
	SendStatus(rec, http.StatusForbidden)
	if rec.Code != http.StatusForbidden || rec.Body.String() != "Forbidden" {
		t.Fatalf("got %d %q", rec.Code, rec.Body.String())
	}
	if got, want := rec.Header().Get("Content-Type"), "text/plain; charset=utf-8"; got != want {
		t.Fatalf("Content-Type = %q, want %q", got, want)
	}
	if got, want := rec.Header().Get("Content-Length"), "9"; got != want {
		t.Fatalf("Content-Length = %q, want %q", got, want)
	}
	if got, want := rec.Header().Get("ETag"), ExpressETag([]byte("Forbidden")); got != want {
		t.Fatalf("ETag = %q, want %q", got, want)
	}
}

func TestSendStatusOptionsDropHeaders(t *testing.T) {
	rec := httptest.NewRecorder()
	SendStatus(rec, http.StatusOK, WithoutContentLength(), WithoutETag())
	if rec.Body.String() != "OK" {
		t.Fatalf("body = %q", rec.Body.String())
	}
	if rec.Header().Get("Content-Length") != "" || rec.Header().Get("ETag") != "" {
		t.Fatalf("headers = %v", rec.Header())
	}
}
