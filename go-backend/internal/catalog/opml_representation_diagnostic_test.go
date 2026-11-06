//go:build tsdiagnostic

package catalog

import (
	"database/sql"
	"encoding/json"
	"os"
	"testing"
)

// TestTSRepresentationR06 preserves exact upstream bytes for diagnostics while
// the default contract tests compare parsed OPML meaning.
func TestTSRepresentationR06(t *testing.T) {
	raw, err := os.ReadFile("testdata/opml-golden.json")
	if err != nil {
		t.Fatal(err)
	}
	var golden map[string]struct {
		Podcasts []struct {
			Title         *string `json:"title"`
			FeedURL       *string `json:"feedURL"`
			Description   *string `json:"description"`
			ItunesPageURL *string `json:"itunesPageURL"`
			Language      *string `json:"language"`
		} `json:"podcasts"`
		XML string `json:"xml"`
	}
	if err := json.Unmarshal(raw, &golden); err != nil {
		t.Fatal(err)
	}
	ns := func(s *string) sql.NullString {
		if s == nil {
			return sql.NullString{}
		}
		return sql.NullString{String: *s, Valid: true}
	}
	for name, c := range golden {
		var podcasts []opmlPodcast
		for _, p := range c.Podcasts {
			podcasts = append(podcasts, opmlPodcast{
				title: ns(p.Title), feedURL: ns(p.FeedURL), description: ns(p.Description), language: ns(p.Language),
			})
		}
		if got := generateOPML(podcasts); got != c.XML {
			t.Errorf("%s bytes:\n got %q\nwant %q", name, got, c.XML)
		}
	}

	rig := newOPMLRouteRig(t)
	book := rig.do("GET", "/api/libraries/l1/opml", "", rootUser())
	if book.Code != 200 {
		t.Fatalf("empty book library status = %d", book.Code)
	}
	if book.Body.String() != golden["empty"].XML {
		t.Fatalf("empty library bytes = %q, want %q", book.Body.String(), golden["empty"].XML)
	}
}
