package browse

import (
	"net/http"
	"strings"
	"testing"

	"audiobookshelf-compat/tests/replacement/harness"
)

// detailPath is the library-scoped series path.
func detailPath(e *harness.Env, id string) string { return libraryPath(e, bookLibrary, "/series/"+id) }

// openSeriesFeed opens an RSS feed for the series once.
func (s *suite) openSeriesFeed(t *testing.T, e *harness.Env, seriesID string) {
	t.Helper()
	if s.seriesFeed {
		return
	}
	resp := postJSON(t, e, e.Token(t, "root"), "/api/feeds/series/"+seriesID+"/open", `{"serverAddress":"http://127.0.0.1","slug":"browse-long-saga"}`)
	mustOK(t, "open series feed", resp)
	s.seriesFeed = true
}

func (s *suite) seriesDetailScenarios() []harness.Scenario {
	return []harness.Scenario{
		{
			Requirement: "route-r026", Invariant: "INV-ROUTE-R026-CONTRACT", Case: "normal", State: "stateless", Assertion: "library-series-detail-and-progress",
			Sources: []string{srcSeriesDetail},
			Claim:   "GET library series/:id returns the series object without books, and include=progress adds the accessible book ids and the ones this user finished.",
			Run: func(t *testing.T, e *harness.Env) {
				s.enrich(t, e)
				id := seriesIDByName(t, e, long)
				plain := getSeries(t, e, e.Token(t, "listener"), detailPath(e, id), nil)
				if plain.ID != id || plain.Name != long || plain.NameIgnore != long || plain.LibraryID != e.IDs[bookLibrary] || plain.Description != nil || plain.AddedAt == 0 || plain.UpdatedAt == 0 || plain.Books != nil || plain.Progress != nil || plain.RssFeed != nil {
					t.Fatalf("plain detail: %+v", plain)
				}
				listener := getSeries(t, e, e.Token(t, "listener"), detailPath(e, id), query("include", "progress"))
				expectOrder(t, "listener items", keysOf(e, listener.Progress.LibraryItemIDs), []string{"saga1", "saga2"})
				expectOrder(t, "listener finished", keysOf(e, listener.Progress.LibraryItemIDsFinished), []string{})
				if listener.Progress.IsFinished {
					t.Fatalf("listener finished nothing")
				}
				finisher := getSeries(t, e, e.Token(t, "finisher"), detailPath(e, id), query("include", "progress"))
				expectOrder(t, "finisher finished", keysOf(e, finisher.Progress.LibraryItemIDsFinished), []string{"saga1"})
				if finisher.Progress.IsFinished {
					t.Fatalf("finisher has one of two finished")
				}
				solo := getSeries(t, e, e.Token(t, "listener"), detailPath(e, seriesIDByName(t, e, solo)), query("include", "progress"))
				expectOrder(t, "solo finished", keysOf(e, solo.Progress.LibraryItemIDsFinished), []string{"novel"})
				if !solo.Progress.IsFinished {
					t.Fatalf("listener finished the only solo book")
				}
			},
		},
		{
			Requirement: "route-r026", Invariant: "INV-ROUTE-R026-CONTRACT", Case: "normal", State: "stateless", Assertion: "library-series-detail-rssfeed",
			Sources: []string{srcSeriesDetail, "server/managers/RssFeedManager.js"},
			Claim:   "include=rssfeed adds rssFeed null for a series without feed and the minified feed once a feed is open for it.",
			Run: func(t *testing.T, e *harness.Env) {
				s.enrich(t, e)
				id := seriesIDByName(t, e, long)
				none := e.Do(t, harness.Request{Path: detailPath(e, id), Token: e.Token(t, "root"), Query: query("include", "rssfeed")})
				mustOK(t, "detail", none)
				if value, present := none.Object(t)["rssFeed"]; !present || value != nil {
					t.Fatalf("rssFeed must be present and null: %s", none.Body)
				}
				s.openSeriesFeed(t, e, id)
				open := getSeries(t, e, e.Token(t, "listener"), detailPath(e, id), query("include", "rssfeed", "again", "1"))
				if open.RssFeed == nil || open.RssFeed.EntityID != id || open.RssFeed.EntityType != "series" || !strings.HasSuffix(open.RssFeed.FeedURL, "/feed/browse-long-saga") {
					t.Fatalf("series feed: %+v", open.RssFeed)
				}
				list := getSeriesPage(t, e, e.Token(t, "listener"), query("limit", "10", "include", "rssfeed", "sort", "name"))
				for _, series := range list.Results {
					if (series.Name == long) != (series.RssFeed != nil) {
						t.Fatalf("list rssFeed for %s: %+v", series.Name, series.RssFeed)
					}
				}
			},
		},
		{
			Requirement: "route-r026", Invariant: "INV-ROUTE-R026-CONTRACT", Case: "error", State: "stateless", Assertion: "library-series-detail-errors",
			Sources: []string{srcSeriesDetail, srcMiddle},
			Claim:   "Library series/:id is 401 without a token, 404 for an unknown series or one of another library, and 403 for a user without the library.",
			Run: func(t *testing.T, e *harness.Env) {
				s.enrich(t, e)
				id := seriesIDByName(t, e, long)
				expectStatus(t, "no token", e.Get(t, "", detailPath(e, id)), http.StatusUnauthorized)
				expectStatus(t, "unknown", e.Get(t, e.Token(t, "root"), detailPath(e, "00000000-0000-4000-8000-000000000000")), http.StatusNotFound)
				other := libraryPath(e, podcastLibrary, "/series/"+id)
				expectStatus(t, "series of another library", e.Get(t, e.Token(t, "root"), other), http.StatusNotFound)
				expectStatus(t, "library not accessible", e.Get(t, s.restrictedToken(t, e, "browse-podonly"), detailPath(e, id)), http.StatusForbidden)
			},
		},
		{
			Requirement: "route-r026", Invariant: "INV-ROUTE-R026-CONTRACT", Case: "edge", State: "stateless", Assertion: "library-series-detail-lists-only-accessible-books",
			Sources: []string{srcSeriesDetail},
			Claim:   "include=progress lists only the books the user may access: the finisher, who may not see the explicit book, gets just the other book of the series.",
			Run: func(t *testing.T, e *harness.Env) {
				s.enrich(t, e)
				id := seriesIDByName(t, e, third)
				visible := getSeries(t, e, e.Token(t, "finisher"), detailPath(e, id), query("include", "progress"))
				expectOrder(t, "finisher sees the non-explicit book only", keysOf(e, visible.Progress.LibraryItemIDs), []string{"disposable"})
				root := getSeries(t, e, e.Token(t, "root"), detailPath(e, id), query("include", "progress"))
				expectOrder(t, "root sees both", keysOf(e, root.Progress.LibraryItemIDs), []string{"ebook", "disposable"})
			},
		},
	}
}

func (s *suite) seriesByIDScenarios() []harness.Scenario {
	return []harness.Scenario{
		{
			Requirement: "route-r138", Invariant: "INV-ROUTE-R138-CONTRACT", Case: "normal", State: "stateless", Assertion: "series-by-id-and-progress",
			Sources: []string{"server/controllers/SeriesController.js:28-57", "server/controllers/SeriesController.js:95-120"},
			Claim:   "GET /api/series/:id returns the series object and include=progress lists the accessible book ids and the user's finished ones.",
			Run: func(t *testing.T, e *harness.Env) {
				s.enrich(t, e)
				id := seriesIDByName(t, e, long)
				plain := getSeries(t, e, e.Token(t, "listener"), "/api/series/"+id, nil)
				if plain.ID != id || plain.Name != long || plain.LibraryID != e.IDs[bookLibrary] || plain.Progress != nil || plain.Books != nil {
					t.Fatalf("series: %+v", plain)
				}
				finisher := getSeries(t, e, e.Token(t, "finisher"), "/api/series/"+id, query("include", "progress"))
				expectOrder(t, "items", keysOf(e, finisher.Progress.LibraryItemIDs), []string{"saga1", "saga2"})
				expectOrder(t, "finished", keysOf(e, finisher.Progress.LibraryItemIDsFinished), []string{"saga1"})
				if finisher.Progress.IsFinished {
					t.Fatalf("one of two finished")
				}
				solo := getSeries(t, e, e.Token(t, "listener"), "/api/series/"+seriesIDByName(t, e, solo), query("include", "progress"))
				if !solo.Progress.IsFinished {
					t.Fatalf("solo finished by listener")
				}
				include := getSeries(t, e, e.Token(t, "root"), "/api/series/"+id, query("include", "rssfeed,progress"))
				if include.Progress == nil || len(include.Progress.LibraryItemIDs) != 2 {
					t.Fatalf("combined include: %+v", include)
				}
			},
		},
		{
			Requirement: "route-r138", Invariant: "INV-ROUTE-R138-CONTRACT", Case: "normal", State: "stateless", Assertion: "series-by-id-rssfeed",
			Sources: []string{"server/controllers/SeriesController.js:52-55"},
			Claim:   "include=rssfeed on /api/series/:id gives rssFeed null without a feed and the open feed otherwise.",
			Run: func(t *testing.T, e *harness.Env) {
				s.enrich(t, e)
				none := e.Do(t, harness.Request{Path: "/api/series/" + seriesIDByName(t, e, solo), Token: e.Token(t, "root"), Query: query("include", "rssfeed")})
				mustOK(t, "solo", none)
				raw := none.Object(t)
				if value, present := raw["rssFeed"]; !present || value != nil {
					t.Fatalf("rssFeed must be present and null: %s", none.Body)
				}
				id := seriesIDByName(t, e, long)
				s.openSeriesFeed(t, e, id)
				open := getSeries(t, e, e.Token(t, "listener"), "/api/series/"+id, query("include", "rssfeed"))
				if open.RssFeed == nil || open.RssFeed.EntityID != id {
					t.Fatalf("feed: %+v", open.RssFeed)
				}
			},
		},
		{
			Requirement: "route-r138", Invariant: "INV-ROUTE-R138-CONTRACT", Case: "edge", State: "stateless", Assertion: "series-by-id-lists-only-accessible-books",
			Sources: []string{"server/controllers/SeriesController.js:28-45", srcSeriesMiddle},
			Claim:   "For a user who may access only some of the series' books, include=progress lists just those books and judges isFinished among them.",
			Run: func(t *testing.T, e *harness.Env) {
				s.enrich(t, e)
				id := seriesIDByName(t, e, third)
				notag := getSeries(t, e, s.restrictedToken(t, e, "browse-notag"), "/api/series/"+id, query("include", "progress"))
				expectOrder(t, "tag-excluded user", keysOf(e, notag.Progress.LibraryItemIDs), []string{"disposable"})
				if notag.Progress.IsFinished || len(notag.Progress.LibraryItemIDsFinished) != 0 {
					t.Fatalf("progress: %+v", notag.Progress)
				}
				root := getSeries(t, e, e.Token(t, "root"), "/api/series/"+id, query("include", "progress"))
				expectOrder(t, "root", keysOf(e, root.Progress.LibraryItemIDs), []string{"ebook", "disposable"})
			},
		},
		{
			Requirement: "route-r138", Invariant: "INV-ROUTE-R138-CONTRACT", Case: "error", State: "stateless", Assertion: "series-by-id-errors",
			Sources: []string{"server/controllers/SeriesController.js:95-120", "server/Auth.js:322"},
			Claim:   "GET /api/series/:id is 401 without a token, 404 for an unknown id and 404 when the user may access none of the series' books.",
			Run: func(t *testing.T, e *harness.Env) {
				s.enrich(t, e)
				id := seriesIDByName(t, e, third)
				expectStatus(t, "no token", e.Get(t, "", "/api/series/"+id), http.StatusUnauthorized)
				expectStatus(t, "unknown", e.Get(t, e.Token(t, "root"), "/api/series/00000000-0000-4000-8000-000000000000"), http.StatusNotFound)
				expectStatus(t, "no accessible books", e.Get(t, s.restrictedToken(t, e, "browse-tagged"), "/api/series/"+id), http.StatusNotFound)
				expectStatus(t, "some accessible books", e.Get(t, e.Token(t, "finisher"), "/api/series/"+id), http.StatusOK)
			},
		},
	}
}
