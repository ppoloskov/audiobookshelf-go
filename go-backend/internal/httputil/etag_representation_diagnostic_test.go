//go:build tsdiagnostic

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

// TestTSRepresentationR01 retains the helper-level Express ETag algorithm
// comparisons as an opt-in representation diagnostic.
func TestTSRepresentationR01(t *testing.T) {
	status := httptest.NewRecorder()
	SendStatus(status, httptest.NewRequest(http.MethodGet, "/", nil), http.StatusForbidden)
	if got, want := status.Header().Get("ETag"), ExpressETag([]byte("Forbidden")); got != want {
		t.Fatalf("status ETag = %q, want helper result %q", got, want)
	}

	text := httptest.NewRecorder()
	SendText(text, httptest.NewRequest(http.MethodGet, "/", nil), http.StatusBadRequest, "Invalid request body")
	if got, want := text.Header().Get("ETag"), ExpressETag([]byte("Invalid request body")); got != want {
		t.Fatalf("text ETag = %q, want helper result %q", got, want)
	}
}
