package browse

import (
	"encoding/json"
	"testing"

	"audiobookshelf-compat/tests/replacement/harness"
)

// shelfLabels are the label and labelStringKey the clients render per shelf id
// (LibraryItem.js:352-520).
var shelfLabels = map[string][2]string{
	"continue-listening": {"Continue Listening", "LabelContinueListening"},
	"continue-reading":   {"Continue Reading", "LabelContinueReading"},
	"continue-series":    {"Continue Series", "LabelContinueSeries"},
	"recently-added":     {"Recently Added", "LabelRecentlyAdded"},
	"recent-series":      {"Recent Series", "LabelRecentSeries"},
	"discover":           {"Discover", "LabelDiscover"},
	"listen-again":       {"Listen Again", "LabelListenAgain"},
	"read-again":         {"Read Again", "LabelReadAgain"},
	"newest-authors":     {"Newest Authors", "LabelNewestAuthors"},
	"newest-episodes":    {"Newest Episodes", "LabelNewestEpisodes"},
}

// expectLabels fails unless every shelf carries its label and string key.
func expectLabels(t *testing.T, shelves []shelf) {
	t.Helper()
	for _, s := range shelves {
		want, ok := shelfLabels[s.ID]
		if !ok || s.Label != want[0] || s.LabelKey != want[1] {
			t.Fatalf("shelf %s: label %q key %q, want %v", s.ID, s.Label, s.LabelKey, want)
		}
	}
}

// shelfByID returns the shelf or fails.
func shelfByID(t *testing.T, shelves []shelf, id string) shelf {
	t.Helper()
	for _, s := range shelves {
		if s.ID == id {
			return s
		}
	}
	t.Fatalf("shelf %s missing", id)
	return shelf{}
}

// shelfItems decodes the entities of an item shelf.
func shelfItems(t *testing.T, s shelf) []listItem {
	t.Helper()
	out := make([]listItem, 0, len(s.Entities))
	for _, raw := range s.Entities {
		var item listItem
		if err := json.Unmarshal(raw, &item); err != nil {
			t.Fatal(err)
		}
		out = append(out, item)
	}
	return out
}

func (s *suite) shelfDetailScenarios() []harness.Scenario {
	return []harness.Scenario{
		{
			Requirement: "route-r029", Invariant: "INV-ROUTE-R029-CONTRACT", Case: "normal", State: "stateless", Assertion: "personalized-labels-and-entity-fields",
			Sources: []string{srcShelves, "server/utils/queries/libraryFilters.js:155-200", "server/utils/queries/libraryFilters.js:260-285", "client/components/app/BookShelfCategorized.vue:182"},
			Claim:   "Every shelf carries its label and labelStringKey; newest-authors entities carry numBooks, recent-series entities their books in sequence order, continue-series items their series {id,name,sequence}, and episode shelf items the recentEpisode fields.",
			Run: func(t *testing.T, e *harness.Env) {
				s.enrich(t, e)
				all := getShelves(t, e, e.Token(t, "root"), bookLibrary, nil)
				expectLabels(t, all)
				authors := shelfByID(t, all, "newest-authors")
				counts := map[string]int{}
				for _, raw := range authors.Entities {
					var author struct {
						Name     string `json:"name"`
						NumBooks int    `json:"numBooks"`
					}
					if err := json.Unmarshal(raw, &author); err != nil {
						t.Fatal(err)
					}
					counts[author.Name] = author.NumBooks
				}
				if len(counts) != 2 || counts["Terry Fixture"] != 2 || counts["Ada Example"] != 2 {
					t.Fatalf("author numBooks: %v", counts)
				}
				recent := shelfByID(t, all, "recent-series")
				wantBooks := map[string][]string{third: {"ebook", "disposable"}, solo: {"novel"}, long: {"saga1", "saga2"}}
				for _, raw := range recent.Entities {
					var series seriesJSON
					if err := json.Unmarshal(raw, &series); err != nil {
						t.Fatal(err)
					}
					expectOrder(t, "recent-series books of "+series.Name, bookKeys(e, series), wantBooks[series.Name])
					if series.LibraryID != e.IDs[bookLibrary] || series.ID != seriesIDByName(t, e, series.Name) {
						t.Fatalf("series entity: %+v", series)
					}
				}
				finisher := getShelves(t, e, e.Token(t, "finisher"), bookLibrary, nil)
				expectLabels(t, finisher)
				next := shelfItems(t, shelfByID(t, finisher, "continue-series"))
				var ref *seriesRef
				if err := json.Unmarshal(next[0].Media.Metadata.Series, &ref); err != nil || ref == nil {
					t.Fatalf("continue-series series ref: %v %s", err, next[0].Media.Metadata.Series)
				}
				if next[0].ID != e.IDs["saga2"] || ref.ID != seriesIDByName(t, e, long) || ref.Name != long || ref.Sequence == nil || *ref.Sequence != "2" {
					t.Fatalf("continue-series entity: %+v %+v", next[0].ID, ref)
				}

				pods := getShelves(t, e, e.Token(t, "listener"), podcastLibrary, nil)
				expectLabels(t, pods)
				for _, c := range []struct{ shelf, key, title, season, episode string }{
					{"newest-episodes", "ep3", "Third Time", "1", "3"},
					{"continue-listening", "ep3", "Third Time", "1", "3"},
					{"listen-again", "ep2", "Second Outing", "1", "2"},
				} {
					items := shelfItems(t, shelfByID(t, pods, c.shelf))
					var found bool
					for i, raw := range shelfByID(t, pods, c.shelf).Entities {
						var entity struct {
							RecentEpisode struct {
								ID, LibraryItemID, PodcastID, Title, Season, Episode string
							} `json:"recentEpisode"`
						}
						if err := json.Unmarshal(raw, &entity); err != nil {
							t.Fatal(err)
						}
						if entity.RecentEpisode.ID != e.IDs[c.key] {
							continue
						}
						found = true
						ep := entity.RecentEpisode
						if ep.Title != c.title || ep.Season != c.season || ep.Episode != c.episode || ep.LibraryItemID != e.IDs["podcast"] || ep.PodcastID != items[i].Media.ID {
							t.Fatalf("%s recentEpisode: %+v", c.shelf, ep)
						}
					}
					if !found {
						t.Fatalf("%s lacks %s", c.shelf, c.key)
					}
				}
			},
		},
		{
			Requirement: "route-r029", Invariant: "INV-ROUTE-R029-CONTRACT", Case: "normal", State: "stateless", Assertion: "personalized-include-rssfeed-share-numepisodes",
			Sources: []string{"server/utils/queries/libraryFilters.js:60-130", "server/utils/queries/libraryFilters.js:146-186", "server/utils/queries/libraryItemsBookFilters.js:540-546", "client/components/app/BookShelfCategorized.vue:182"},
			Claim:   "With the client's include list, items on every shelf get rssFeed when a feed is open, mediaItemShare for admins only, recent-series entities get their series feed, and podcast shelf items get rssFeed and numEpisodesIncomplete per user.",
			Run: func(t *testing.T, e *harness.Env) {
				s.enrich(t, e)
				include := query("include", "rssfeed,numEpisodesIncomplete,share")
				longID := seriesIDByName(t, e, long)
				s.openSeriesFeed(t, e, longID)
				feedOf := func(items []listItem, key string) *feedRef {
					for _, item := range items {
						if item.ID == e.IDs[key] {
							return item.RssFeed
						}
					}
					t.Fatalf("%s missing", key)
					return nil
				}
				listener := getShelves(t, e, e.Token(t, "listener"), bookLibrary, include)
				for _, id := range []string{"continue-listening", "recently-added"} {
					items := shelfItems(t, shelfByID(t, listener, id))
					if feed := feedOf(items, "saga1"); feed == nil || feed.EntityID != e.IDs["saga1"] {
						t.Fatalf("%s: saga1 rssFeed %+v", id, feed)
					}
					for _, item := range items {
						if item.MediaItemShare != nil {
							t.Fatalf("%s: listener must not get shares", id)
						}
					}
				}
				again := shelfItems(t, shelfByID(t, listener, "listen-again"))
				if feed := feedOf(again, "novel"); feed == nil {
					t.Fatalf("listen-again: novel rssFeed missing")
				}
				finisher := getShelves(t, e, e.Token(t, "finisher"), bookLibrary, include)
				if feed := feedOf(shelfItems(t, shelfByID(t, finisher, "listen-again")), "saga1"); feed == nil {
					t.Fatalf("finisher listen-again saga1 rssFeed missing")
				}
				if feed := feedOf(shelfItems(t, shelfByID(t, finisher, "continue-series")), "saga2"); feed != nil {
					t.Fatalf("saga2 has no feed: %+v", feed)
				}
				root := getShelves(t, e, e.Token(t, "root"), bookLibrary, include)
				added := shelfItems(t, shelfByID(t, root, "recently-added"))
				shared := map[string]string{}
				for _, item := range added {
					if item.MediaItemShare != nil {
						shared[item.ID] = item.MediaItemShare.Slug
					}
				}
				if len(shared) != 2 || shared[e.IDs["saga2"]] != "trial-share-saga2" || shared[e.IDs["novel"]] != "trial-share-novel" {
					t.Fatalf("admin shares on recently-added: %v", shared)
				}
				for _, raw := range shelfByID(t, root, "recent-series").Entities {
					var series seriesJSON
					if err := json.Unmarshal(raw, &series); err != nil {
						t.Fatal(err)
					}
					if (series.ID == longID) != (series.RssFeed != nil) {
						t.Fatalf("recent-series feed of %s: %+v", series.Name, series.RssFeed)
					}
				}
				pods := shelfItems(t, shelfByID(t, getShelves(t, e, e.Token(t, "listener"), podcastLibrary, include), "recently-added"))
				for _, item := range pods {
					if item.ID != e.IDs["podcast"] {
						continue
					}
					if item.RssFeed == nil || item.NumEpisodesIncomplete == nil || *item.NumEpisodesIncomplete != 2 {
						t.Fatalf("podcast recently-added: %+v %v", item.RssFeed, item.NumEpisodesIncomplete)
					}
				}
				other := shelfItems(t, shelfByID(t, getShelves(t, e, e.Token(t, "finisher"), podcastLibrary, include), "recently-added"))
				if other[0].NumEpisodesIncomplete == nil || *other[0].NumEpisodesIncomplete != 3 {
					t.Fatalf("finisher numEpisodesIncomplete: %v", other[0].NumEpisodesIncomplete)
				}
			},
		},
	}
}
