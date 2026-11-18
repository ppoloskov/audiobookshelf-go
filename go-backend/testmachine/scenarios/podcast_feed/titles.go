package podcast_feed

import (
	"net/http"
	"testing"

	"audiobookshelf-compat/testmachine/scenarios/harness"
)

const (
	titlesReq     = "route-r043"
	titlesInv     = "INV-R043-TITLES-001"
	titlesAuthInv = "INV-R043-SAMPLED"
	titlesSrc     = "server/controllers/LibraryController.js:1386-1413"
	titlesMidSrc  = "server/controllers/LibraryController.js:1475-1500"
)

func titlesScenarios() []harness.Scenario {
	return []harness.Scenario{
		{
			Requirement: titlesReq, Invariant: titlesInv, Case: "normal", State: "stateless", Assertion: "podcast-titles-lists-library-podcasts",
			Sources: []string{titlesSrc, titlesMidSrc, "client/pages/library/_library/podcast/search.vue:214-219"},
			Claim:   "GET /api/libraries/:id/podcast-titles answers {podcasts: [{title, itunesId, libraryItemId, libraryId}]} for every podcast of that library, with the item id the client stores and the itunes id the id match uses.",
			Run: func(t *testing.T, e *harness.Env) {
				payload := podcastTitles(t, e, e.Token(t, "root"), e.IDs["podcastLibrary"])
				byTitle := map[string]struct {
					itunesID      *string
					libraryItemID string
				}{}
				for _, podcast := range payload.Podcasts {
					if podcast.LibraryID != e.IDs["podcastLibrary"] || podcast.LibraryItemID == "" {
						t.Fatalf("entry %+v does not point into the podcast library", podcast)
					}
					byTitle[podcast.Title] = struct {
						itunesID      *string
						libraryItemID string
					}{podcast.ItunesID, podcast.LibraryItemID}
				}
				want := []string{"Fixture Cast", "Signal Show", "Minimal Show", "Explicit Show", "Tagged Show", "Wire Signal", "Night Wire", "Fresh Wire"}
				if len(byTitle) != len(want) {
					t.Fatalf("%d titles, want %d: %+v", len(byTitle), len(want), byTitle)
				}
				for _, title := range want {
					if _, ok := byTitle[title]; !ok {
						t.Fatalf("title %q missing from %+v", title, byTitle)
					}
				}
				if entry := byTitle["Fixture Cast"]; entry.libraryItemID != e.IDs["podcast"] || entry.itunesID != nil {
					t.Fatalf("fixture entry = %+v, want item %s and no itunes id", entry, e.IDs["podcast"])
				}
				if entry := byTitle["Signal Show"]; strPtr(entry.itunesID) != "123456789" {
					t.Fatalf("Signal Show itunesId = %v", entry.itunesID)
				}
				if entry := byTitle["Wire Signal"]; strPtr(entry.itunesID) != "" {
					t.Fatalf("an OPML import stores an empty itunes id, got %v", entry.itunesID)
				}
			},
		},
		{
			Requirement: titlesReq, Invariant: titlesInv, Case: "edge", State: "stateless", Assertion: "podcast-titles-empty-for-book-library",
			Sources: []string{titlesSrc},
			Claim:   "The audiobook library has no podcasts, so its podcast-titles answer is an empty array.",
			Run: func(t *testing.T, e *harness.Env) {
				payload := podcastTitles(t, e, e.Token(t, "root"), e.IDs["library"])
				if payload.Podcasts == nil || len(payload.Podcasts) != 0 {
					t.Fatalf("podcasts = %+v, want []", payload.Podcasts)
				}
			},
		},
		{
			Requirement: titlesReq, Invariant: titlesAuthInv, Case: "error", State: "stateless", Assertion: "podcast-titles-rejections",
			Sources: []string{titlesSrc, titlesMidSrc},
			Claim:   "GET /api/libraries/:id/podcast-titles answers 401 without a token, 403 for a non-admin, 403 for an admin without access to the library and 404 for an unknown library id.",
			Run: func(t *testing.T, e *harness.Env) {
				library := e.IDs["podcastLibrary"]
				wantStatus(t, "anonymous", e.Get(t, "", "/api/libraries/"+library+"/podcast-titles"), http.StatusUnauthorized)
				wantStatus(t, "listener", e.Get(t, e.Token(t, "listener"), "/api/libraries/"+library+"/podcast-titles"), http.StatusForbidden)
				wantStatus(t, "finisher", e.Get(t, e.Token(t, "finisher"), "/api/libraries/"+library+"/podcast-titles"), http.StatusForbidden)
				restricted := createAdmin(t, e, "titles-restricted-admin", e.IDs["library"])
				wantStatus(t, "admin of another library", e.Get(t, restricted, "/api/libraries/"+library+"/podcast-titles"), http.StatusForbidden)
				wantStatus(t, "unknown library", e.Get(t, e.Token(t, "root"), "/api/libraries/not-a-library/podcast-titles"), http.StatusNotFound)
			},
		},
	}
}

// createAdmin creates an admin account with access to the listed libraries only.
func createAdmin(t *testing.T, e *harness.Env, username string, libraries ...string) string {
	t.Helper()
	body := map[string]any{
		"username": username, "password": username + "-password", "type": "admin", "isActive": true,
		"permissions": map[string]any{"accessAllLibraries": false, "librariesAccessible": libraries},
	}
	wantStatus(t, "create admin", e.Post(t, e.Token(t, "root"), "/api/users", body), http.StatusOK)
	token, _ := e.Login(t, username, username+"-password")
	return token
}
