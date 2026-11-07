package httputil

import (
	"encoding/json"
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
	ErrorJSON(rec, http.StatusBadRequest, "bad <input>")
	var got map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil || len(got) != 1 || got["error"] != "bad <input>" {
		t.Fatalf("body = %q (decode %v)", rec.Body.String(), err)
	}
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d", rec.Code)
	}
}
