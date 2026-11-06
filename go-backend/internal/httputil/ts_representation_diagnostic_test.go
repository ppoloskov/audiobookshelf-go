//go:build tsdiagnostic

package httputil

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestTSRepresentationR02 retains the historical encoding fingerprint as an
// opt-in diagnostic; client contract assertions live in json_test.go.
func TestTSRepresentationR02(t *testing.T) {
	rec := httptest.NewRecorder()
	WriteJSON(rec, http.StatusCreated, map[string]any{"b": 1, "a": "<&>"})
	if got, want := rec.Body.String(), `{"a":"\u003c\u0026\u003e","b":1}`; got != want {
		t.Fatalf("body = %q, want %q", got, want)
	}
	if got, want := rec.Header().Get("Content-Length"), "32"; got != want {
		t.Fatalf("Content-Length = %q, want %q", got, want)
	}
}

// TestTSRepresentationR03 retains the encoder newline fingerprint as an
// opt-in diagnostic; the default test checks the decoded document and framing.
func TestTSRepresentationR03(t *testing.T) {
	rec := httptest.NewRecorder()
	WriteJSON(rec, http.StatusOK, map[string]int{"a": 1}, Streamed())
	if got, want := rec.Body.String(), "{\"a\":1}\n"; got != want {
		t.Fatalf("body = %q, want %q", got, want)
	}
}
