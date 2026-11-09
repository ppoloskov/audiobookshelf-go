package providers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"audiobookshelf-compat/internal/store"
)

// Stored SQLite datetimes reach clients as RFC 3339 UTC (the TS model's
// toJSON); unparseable values pass through unchanged.
func TestCustomProviderListReportsRFC3339Timestamps(t *testing.T) {
	cases := map[string]string{
		"2024-01-01 00:00:00.123 +00:00": "2024-01-01T00:00:00.123Z",
		"2024-01-01 02:00:00.5 +02:00":   "2024-01-01T00:00:00.5Z",
		"2024-01-01T00:00:00Z":           "2024-01-01T00:00:00Z",
		"2024-01-01 00:00:00 +00:00":     "2024-01-01T00:00:00Z",
		"not a date":                     "not a date",
	}
	db := providersTestDB(t)
	i := 0
	for stored := range cases {
		i++
		if _, err := db.Exec(`INSERT INTO customMetadataProviders(id,name,mediaType,url,createdAt,updatedAt) VALUES(?,?,'book','http://x.test',?,?)`, string(rune('a'+i)), stored, stored, stored); err != nil {
			t.Fatal(err)
		}
	}
	h, err := New(db, nil)
	if err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	serve(h, w, httptest.NewRequest(http.MethodGet, "/api/custom-metadata-providers", nil), store.User{Type: "admin"})
	var body struct {
		Providers []struct{ Name, CreatedAt, UpdatedAt string }
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil || len(body.Providers) != len(cases) {
		t.Fatalf("list = %d %s (%v)", w.Code, w.Body, err)
	}
	for _, p := range body.Providers {
		if want := cases[p.Name]; p.CreatedAt != want || p.UpdatedAt != want {
			t.Errorf("stored %q -> created %q updated %q, want %q", p.Name, p.CreatedAt, p.UpdatedAt, want)
		}
	}
}
