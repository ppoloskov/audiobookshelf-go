package httputil

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestWriteErrorSendsMessageAsExpressText(t *testing.T) {
	rec := httptest.NewRecorder()
	WriteError(rec, http.StatusNotFound, "Item not found")
	if rec.Code != http.StatusNotFound || rec.Body.String() != "Item not found" {
		t.Fatalf("got %d %q", rec.Code, rec.Body.String())
	}
	if got, want := rec.Header().Get("Content-Type"), "text/html; charset=utf-8"; got != want {
		t.Fatalf("Content-Type = %q, want %q", got, want)
	}
}

func TestWriteErrorFallsBackToStatusText(t *testing.T) {
	rec := httptest.NewRecorder()
	WriteError(rec, http.StatusInternalServerError, "")
	if rec.Body.String() != "Internal Server Error" {
		t.Fatalf("body = %q", rec.Body.String())
	}
	// Unlike SendStatus, an empty message keeps the res.send content type.
	if got, want := rec.Header().Get("Content-Type"), "text/html; charset=utf-8"; got != want {
		t.Fatalf("Content-Type = %q, want %q", got, want)
	}
}

func TestErrorJSONWritesErrorProperty(t *testing.T) {
	rec := httptest.NewRecorder()
	ErrorJSON(rec, http.StatusBadRequest, "bad <input>", Streamed())
	if got, want := rec.Body.String(), "{\"error\":\"bad \\u003cinput\\u003e\"}\n"; got != want {
		t.Fatalf("body = %q, want %q", got, want)
	}
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d", rec.Code)
	}
}
