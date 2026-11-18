package playlists

import (
	"net/http"
	"reflect"
	"testing"

	"audiobookshelf-compat/testmachine/scenarios/harness"
)

func restartScenario() harness.Scenario {
	return harness.Scenario{
		Requirement: "flow-client-list-ownership", Invariant: "INV-FLOW-CLIENT-LIST-OWNERSHIP-001",
		Case: "normal", State: "changed", Assertion: "reopen-reordered-playlist",
		Sources: []string{"server/controllers/PlaylistController.js:152-155", "server/controllers/PlaylistController.js:206-263", "server/models/Playlist.js:297-319"},
		Claim:   "A reordered playlist retains its owner, library, name, timestamps and ordered book links across server restart and stays private to its owner.",
		Run: func(t *testing.T, e *harness.Env) {
			token := e.Token(t, "listener")
			fx := fixtureOf(e)
			p := createAs(t, e, token, "Restart order", book(fx.saga1), book(fx.novel))
			path := "/api/playlists/" + p.ID
			updated := decode(t, e.Patch(t, token, path, map[string]any{"items": []any{book(fx.novel), book(fx.saga1)}}))
			if updated.ID != p.ID || updated.Name != "Restart order" || updated.UserID != e.IDs["listener"] || updated.LibraryID != e.IDs["library"] {
				t.Fatalf("unexpected playlist after reorder: %+v", updated)
			}
			sameStrings(t, "updated order", updated.refs(), []string{fx.novel, fx.saga1})
			wantRow(t, e, updated)
			wantBookLinks(t, e, p.ID, fx.novel, fx.saga1)
			before := playlistRow(t, e, p.ID)
			e.Restart(t)
			reopened := fetchAs(t, e, token, p.ID)
			if after := playlistRow(t, e, p.ID); !reflect.DeepEqual(before, after) {
				t.Fatalf("playlist row changed across restart: before %v, after %v", before, after)
			}
			wantRow(t, e, reopened)
			if reopened.ID != p.ID {
				t.Fatalf("reopened playlist ID %q, want %q", reopened.ID, p.ID)
			}
			sameStrings(t, "reopened order", reopened.refs(), []string{fx.novel, fx.saga1})
			wantBookLinks(t, e, p.ID, fx.novel, fx.saga1)
			wantStatus(t, e.Get(t, e.Token(t, "finisher"), path), http.StatusForbidden)
		},
	}
}
