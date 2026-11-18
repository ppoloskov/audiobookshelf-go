package browse

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"testing"

	"audiobookshelf-compat/testmachine/scenarios/harness"
)

const (
	srcShelves  = "server/models/LibraryItem.js:338-520"
	srcShelfLib = "server/utils/queries/libraryFilters.js:41-440"
)

// shelf is one personalized shelf; entities keep their raw JSON because items,
// series, authors and episodes have different shapes.
type shelf struct {
	ID       string            `json:"id"`
	Label    string            `json:"label"`
	LabelKey string            `json:"labelStringKey"`
	Type     string            `json:"type"`
	Entities []json.RawMessage `json:"entities"`
	Total    int               `json:"total"`
}

// getShelves requests the personalized shelves of a library.
func getShelves(t *testing.T, e *harness.Env, token, libraryKey string, values url.Values) []shelf {
	t.Helper()
	resp := e.Do(t, harness.Request{Path: libraryPath(e, libraryKey, "/personalized"), Token: token, Query: values})
	mustOK(t, "personalized "+values.Encode(), resp)
	var shelves []shelf
	resp.JSON(t, &shelves)
	return shelves
}

// entityName names a shelf entity: a fixture key for items, "podcast/epN" for
// recent episodes, the name for series and authors.
func entityName(t *testing.T, e *harness.Env, raw json.RawMessage) string {
	t.Helper()
	var entity struct {
		ID        string `json:"id"`
		Name      string `json:"name"`
		MediaType string `json:"mediaType"`
		Media     struct {
			Metadata struct {
				Title string `json:"title"`
			} `json:"metadata"`
		} `json:"media"`
		RecentEpisode *struct {
			ID string `json:"id"`
		} `json:"recentEpisode"`
	}
	if err := json.Unmarshal(raw, &entity); err != nil {
		t.Fatalf("entity: %v", err)
	}
	if entity.MediaType == "" {
		return entity.Name
	}
	name := keysOf(e, []string{entity.ID})[0]
	if name == entity.ID {
		name = "title:" + entity.Media.Metadata.Title // an item the packet created itself
	}
	if entity.RecentEpisode != nil {
		name += "/" + keysOf(e, []string{entity.RecentEpisode.ID})[0]
	}
	return name
}

// summary renders the shelves as "id|type|names|total" lines.
func summary(t *testing.T, e *harness.Env, shelves []shelf, sortSets ...string) []string {
	t.Helper()
	out := make([]string, 0, len(shelves))
	for _, s := range shelves {
		names := make([]string, 0, len(s.Entities))
		for _, raw := range s.Entities {
			names = append(names, entityName(t, e, raw))
		}
		if slices.Contains(sortSets, s.ID) {
			slices.Sort(names) // random shelf: only the set is defined
		}
		out = append(out, fmt.Sprintf("%s|%s|%s|%d", s.ID, s.Type, strings.Join(names, ","), s.Total))
	}
	return out
}

func (s *suite) personalizedScenarios() []harness.Scenario {
	return []harness.Scenario{
		{
			Requirement: "route-r029", Invariant: "INV-ROUTE-R029-CONTRACT", Case: "normal", State: "stateless", Assertion: "personalized-book-shelves-listener",
			Sources: []string{srcShelves, srcShelfLib},
			Claim:   "For the listener the book library shows continue-listening, continue-reading, recently-added, recent-series, discover, listen-again and newest-authors with the expected entities, types and totals.",
			Run: func(t *testing.T, e *harness.Env) {
				s.enrich(t, e)
				got := summary(t, e, getShelves(t, e, e.Token(t, "listener"), bookLibrary, nil), "discover")
				want := []string{
					"continue-listening|book|saga2,saga1|3", "continue-reading|book|ebook|3",
					"recently-added|book|saga1,saga2,ebook,novel|4",
					"recent-series|series|A Third Saga,Solo Series,Long Saga|3",
					"discover|book|ebook|1", "listen-again|book|novel|1",
					"newest-authors|authors|Terry Fixture,Ada Example|2",
				}
				expectOrder(t, "listener shelves", got, want)
			},
		},
		{
			Requirement: "route-r029", Invariant: "INV-ROUTE-R029-CONTRACT", Case: "normal", State: "stateless", Assertion: "personalized-book-shelves-finisher-and-root",
			Sources: []string{srcShelves, srcShelfLib},
			Claim:   "The finisher gets continue-series and listen-again from the finished first book and no explicit book, root without progress gets no continue or again shelves.",
			Run: func(t *testing.T, e *harness.Env) {
				s.enrich(t, e)
				want := []string{
					"continue-series|book|saga2|1",
					"recently-added|book|saga1,saga2,novel|3",
					"recent-series|series|A Third Saga,Solo Series,Long Saga|3",
					"discover|book|novel|1", "listen-again|book|saga1|1",
					"newest-authors|authors|Terry Fixture,Ada Example|2",
				}
				expectOrder(t, "finisher shelves", summary(t, e, getShelves(t, e, e.Token(t, "finisher"), bookLibrary, nil), "discover"), want)

				want = []string{
					"recently-added|book|saga1,saga2,ebook,novel|4",
					"recent-series|series|A Third Saga,Solo Series,Long Saga|3",
					"discover|book|ebook,novel,saga1|3",
					"newest-authors|authors|Terry Fixture,Ada Example|2",
				}
				expectOrder(t, "root shelves", summary(t, e, getShelves(t, e, e.Token(t, "root"), bookLibrary, nil), "discover"), want)
			},
		},
		{
			Requirement: "route-r029", Invariant: "INV-ROUTE-R029-CONTRACT", Case: "normal", State: "stateless", Assertion: "personalized-podcast-shelves",
			Sources: []string{"server/models/LibraryItem.js:501-570", srcShelfLib},
			Claim:   "The podcast library shows continue-listening and listen-again episodes per user, newest-episodes and recently-added podcasts.",
			Run: func(t *testing.T, e *harness.Env) {
				s.enrich(t, e)
				want := []string{
					"continue-listening|episode|podcast/ep3,podcast/ep1|2",
					"newest-episodes|episode|podcast/ep3,podcast/ep2,podcast/ep1|3",
					"recently-added|podcast|title:Alpha Cast,podcast|2",
					"listen-again|episode|podcast/ep2|1",
				}
				expectOrder(t, "listener", summary(t, e, getShelves(t, e, e.Token(t, "listener"), podcastLibrary, nil)), want)
				want = []string{"newest-episodes|episode|podcast/ep3,podcast/ep2,podcast/ep1|3", "recently-added|podcast|podcast|1"}
				expectOrder(t, "finisher", summary(t, e, getShelves(t, e, e.Token(t, "finisher"), podcastLibrary, nil)), want)
			},
		},
		{
			Requirement: "route-r029", Invariant: "INV-ROUTE-R029-CONTRACT", Case: "edge", State: "stateless", Assertion: "personalized-limit-and-include",
			Sources: []string{"server/controllers/LibraryController.js:890-899", srcShelves},
			Claim:   "limit caps the entities of every shelf while total keeps the full count, and the client's include list leaves the shelf set unchanged.",
			Run: func(t *testing.T, e *harness.Env) {
				s.enrich(t, e)
				token := e.Token(t, "listener")
				shelves := getShelves(t, e, token, bookLibrary, query("limit", "1", "include", "rssfeed,numEpisodesIncomplete,share"))
				for _, shelf := range shelves {
					if shelf.ID == "recent-series" {
						continue // no client reads the entity count of this shelf under limit; only its total below
					}
					if len(shelf.Entities) != 1 {
						t.Fatalf("shelf %s has %d entities with limit 1", shelf.ID, len(shelf.Entities))
					}
				}
				totals := map[string]int{}
				for _, shelf := range shelves {
					totals[shelf.ID] = shelf.Total
				}
				if totals["continue-listening"] != 3 || totals["listen-again"] != 1 || totals["recent-series"] != 3 {
					t.Fatalf("totals: %v", totals)
				}
				first := entityName(t, e, shelves[0].Entities[0])
				if shelves[0].ID != "continue-listening" || first != "saga2" {
					t.Fatalf("first shelf: %s %s", shelves[0].ID, first)
				}
				withRSS := getShelves(t, e, e.Token(t, "root"), bookLibrary, query("include", "rssfeed"))
				for _, shelf := range withRSS {
					for _, raw := range shelf.Entities {
						var item listItem
						if err := json.Unmarshal(raw, &item); err != nil {
							t.Fatal(err)
						}
						if item.MediaType == "book" && item.ID == e.IDs["novel"] && shelf.ID == "discover" && item.RssFeed == nil {
							t.Fatalf("include=rssfeed must add the open feed to the novel on the discover shelf")
						}
					}
				}
			},
		},
		{
			Requirement: "route-r029", Invariant: "INV-ROUTE-R029-AUTH-001", Case: "error", State: "stateless", Assertion: "personalized-auth-errors",
			Sources: []string{srcMiddle, "server/Auth.js:322"},
			Claim:   "Personalized shelves are 401 without a token, 404 for an unknown library and 403 outside the user's libraries.",
			Run: func(t *testing.T, e *harness.Env) {
				path := libraryPath(e, bookLibrary, "/personalized")
				expectStatus(t, "no token", e.Get(t, "", path), http.StatusUnauthorized)
				expectStatus(t, "unknown", e.Get(t, e.Token(t, "root"), "/api/libraries/00000000-0000-4000-8000-000000000000/personalized"), http.StatusNotFound)
				expectStatus(t, "podonly", e.Get(t, s.restrictedToken(t, e, "browse-podonly"), path), http.StatusForbidden)
				expectStatus(t, "bad limit", e.Do(t, harness.Request{Path: path, Token: e.Token(t, "root"), Query: query("limit", "-1")}), http.StatusBadRequest)
			},
		},
		{
			Requirement: "route-r029", Invariant: "INV-ROUTE-R029-RESTRICT-001", Case: "normal", State: "stateless", Assertion: "personalized-user-restrictions",
			Sources: []string{srcShelfLib, "server/utils/queries/libraryItemsBookFilters.js:15-37"},
			Claim:   "Shelves of a tag-limited user contain only accessible books, series and authors.",
			Run: func(t *testing.T, e *harness.Env) {
				s.enrich(t, e)
				got := summary(t, e, getShelves(t, e, s.restrictedToken(t, e, "browse-tagged"), bookLibrary, nil), "discover")
				want := []string{
					"recently-added|book|saga1,novel|2",
					"recent-series|series|Solo Series,Long Saga|2",
					"discover|book|novel,saga1|2",
					"newest-authors|authors|Terry Fixture,Ada Example|2",
				}
				expectOrder(t, "tagged shelves", got, want)
			},
		},
		{
			Requirement: "route-r029", Invariant: "INV-ROUTE-R029-CONTRACT", Case: "normal", State: "stateless", Assertion: "personalized-after-progress-change",
			Sources: []string{srcShelves, "server/managers/ApiCacheManager.js:7-12"},
			Claim:   "After the listener finishes the first saga book and starts the first book of the third saga, the continue, continue-series, discover and listen-again shelves follow.",
			Run: func(t *testing.T, e *harness.Env) {
				s.enrich(t, e)
				listener := e.Token(t, "listener")
				before := summary(t, e, getShelves(t, e, listener, bookLibrary, nil), "discover")
				if !slices.Contains(before, "listen-again|book|novel|1") {
					t.Fatalf("precondition: %v", before)
				}
				resp := patchJSON(t, e, listener, "/api/me/progress/"+e.IDs["saga1"], `{"isFinished":true}`)
				mustOK(t, "finish saga1", resp)
				resp = patchJSON(t, e, listener, "/api/me/progress/"+e.IDs["disposable"], `{"currentTime":1.5,"duration":3.056327,"progress":0.49}`)
				mustOK(t, "start disposable", resp)
				got := summary(t, e, getShelves(t, e, listener, bookLibrary, nil), "discover")
				want := []string{
					"continue-listening|book|disposable,saga2|3", "continue-reading|book|ebook|3",
					"recently-added|book|saga1,saga2,ebook,novel|4",
					"recent-series|series|A Third Saga,Solo Series,Long Saga|3",
					"listen-again|book|saga1,novel|2",
					"newest-authors|authors|Terry Fixture,Ada Example|2",
				}
				expectOrder(t, "after progress change", got, want)
			},
		},
	}
}
