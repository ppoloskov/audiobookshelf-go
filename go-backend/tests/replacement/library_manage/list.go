package library_manage

import (
	"net/http"
	"slices"
	"strings"
	"testing"

	"audiobookshelf-compat/tests/replacement/harness"
)

const listRoute = "server/routers/ApiRouter.js:70"

// libraryStats is the stats object of GET /api/libraries?include=stats.
type libraryStats struct {
	TotalItems    *int     `json:"totalItems"`
	NumAudioFiles *int     `json:"numAudioFiles"`
	TotalDuration *float64 `json:"totalDuration"`
	TotalSize     *int64   `json:"totalSize"`
}

func listScenarios() []harness.Scenario {
	return []harness.Scenario{
		{
			Requirement: "route-r018", Invariant: "INV-R018-SAMPLED", Case: "normal", State: "stateless", Assertion: "list-fixture-libraries-in-display-order",
			Sources: []string{listRoute, "server/controllers/LibraryController.js:188-210", "server/models/Library.js:92-97", "server/models/Library.js:202-219"},
			Claim:   "GET /api/libraries answers {libraries} ordered by displayOrder with id, name, folders (id, fullPath, libraryId, addedAt), displayOrder, icon, mediaType, provider, settings, lastScan, lastScanVersion and timestamps, equal to the stored rows.",
			Run: func(t *testing.T, e *harness.Env) {
				for _, who := range []string{"root", "listener"} {
					libs := listLibraries(t, e, e.Token(t, who))
					if len(libs) < 2 || libs[0].ID != e.IDs["library"] || libs[1].ID != e.IDs["podcastLibrary"] {
						t.Fatalf("%s: fixture libraries must come first in displayOrder: %v", who, libraryIDs(libs))
					}
					for i := 1; i < len(libs); i++ {
						if libs[i].DisplayOrder < libs[i-1].DisplayOrder {
							t.Fatalf("%s: libraries are not ordered by displayOrder", who)
						}
					}
					books, podcasts := libs[0], libs[1]
					if books.Name != "Audiobooks" || books.MediaType != "book" || books.Provider != "google" || books.Icon != "database" || books.DisplayOrder != 1 {
						t.Fatalf("unexpected book library: %+v", books)
					}
					if podcasts.Name != "Podcasts" || podcasts.MediaType != "podcast" || podcasts.Provider != "itunes" || podcasts.DisplayOrder != 2 {
						t.Fatalf("unexpected podcast library: %+v", podcasts)
					}
					if len(books.Folders) != 1 || books.Folders[0].FullPath != e.Media+"/audiobooks" || books.Folders[0].LibraryID != books.ID || books.Folders[0].AddedAt == 0 {
						t.Fatalf("unexpected folders: %+v", books.Folders)
					}
					if books.LastScan == nil || books.LastScanVersion == nil || *books.LastScanVersion != "2.37.2" || books.CreatedAt == 0 || books.LastUpdate == 0 {
						t.Fatalf("scan fields and timestamps must be filled: %+v", books)
					}
					for _, lib := range libs {
						checkRow(t, e, lib)
					}
				}
			},
		},
		{
			Requirement: "route-r018", Invariant: "INV-R018-AUTH-001", Case: "error", State: "stateless", Assertion: "list-requires-authentication",
			Sources: []string{listRoute, "server/Auth.js:54-62"},
			Claim:   "GET /api/libraries without a token answers 401 and no library data.",
			Run: func(t *testing.T, e *harness.Env) {
				resp := e.Get(t, "", "/api/libraries")
				requireStatus(t, resp, http.StatusUnauthorized, "anonymous list")
				if strings.Contains(string(resp.Body), "libraries") {
					t.Fatalf("unauthenticated response must not carry libraries: %s", resp.Body)
				}
			},
		},
		{
			Requirement: "route-r018", Invariant: "INV-R018-STATS-001", Case: "edge", State: "stateless", Assertion: "list-include-stats",
			Sources: []string{listRoute, "server/controllers/LibraryController.js:198-211"},
			Claim:   "GET /api/libraries?include=stats adds a stats object to each book and podcast library (item and audio file counts of the fixture) and plain GET carries none.",
			Run: func(t *testing.T, e *harness.Env) {
				root := e.Token(t, "root")
				var plain struct {
					Libraries []map[string]any `json:"libraries"`
				}
				e.Get(t, root, "/api/libraries").JSON(t, &plain)
				for _, lib := range plain.Libraries {
					if _, has := lib["stats"]; has {
						t.Fatal("stats must only be present when requested")
					}
				}
				resp := e.Get(t, root, "/api/libraries?include=name,stats")
				var body struct {
					Libraries []struct {
						ID    string        `json:"id"`
						Stats *libraryStats `json:"stats"`
					} `json:"libraries"`
				}
				resp.JSON(t, &body)
				requireStatus(t, resp, http.StatusOK, "list with stats")
				want := map[string][2]int{e.IDs["library"]: {5, 6}, e.IDs["podcastLibrary"]: {1, 3}}
				for _, lib := range body.Libraries {
					counts, fixture := want[lib.ID]
					if !fixture {
						continue
					}
					s := lib.Stats
					if s == nil || s.TotalItems == nil || s.NumAudioFiles == nil || s.TotalDuration == nil || s.TotalSize == nil ||
						*s.TotalItems != counts[0] || *s.NumAudioFiles != counts[1] || *s.TotalDuration <= 0 || *s.TotalSize <= 0 {
						t.Fatalf("library %s stats: %s", lib.ID, resp.Body)
					}
					delete(want, lib.ID)
				}
				if len(want) != 0 {
					t.Fatalf("fixture libraries missing from the list: %v", want)
				}
			},
		},
		{
			Requirement: "route-r018", Invariant: "INV-R018-ACCESS-001", Case: "edge", State: "stateless", Assertion: "list-limited-user-sees-accessible-libraries",
			Sources: []string{listRoute, "server/controllers/LibraryController.js:191-195"},
			Claim:   "GET /api/libraries for a user whose librariesAccessible names libraries lists exactly those, in displayOrder, and an all-access user sees every library.",
			Run: func(t *testing.T, e *harness.Env) {
				extra := newBookLibrary(t, e, "access")
				limited := createUser(t, e, "lm-limited", false, []string{e.IDs["podcastLibrary"], extra.ID})
				got := libraryIDs(listLibraries(t, e, limited.Token))
				if want := []string{e.IDs["podcastLibrary"], extra.ID}; !slices.Equal(got, want) {
					t.Fatalf("limited user sees %v, want %v", got, want)
				}
				all := libraryIDs(listLibraries(t, e, e.Token(t, "listener")))
				if !slices.Contains(all, e.IDs["library"]) || !slices.Contains(all, extra.ID) || len(all) <= 2 {
					t.Fatalf("all-access user must see every library: %v", all)
				}
			},
		},
	}
}
