package httputil

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

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
