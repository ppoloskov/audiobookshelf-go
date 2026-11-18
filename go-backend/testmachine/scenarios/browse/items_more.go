package browse

import (
	"encoding/json"
	"strings"
	"testing"

	"audiobookshelf-compat/testmachine/scenarios/harness"
)

const srcCollapse = "server/utils/queries/libraryItemsBookFilters.js:318-366"

func (s *suite) includeCollapseScenarios() []harness.Scenario {
	return []harness.Scenario{
		{
			Requirement: "route-r022", Invariant: "INV-R022-INCLUDE-001", Case: "normal", State: "stateless", Assertion: "book-items-include-rssfeed-and-share",
			Sources: []string{"server/utils/queries/libraryItemsBookFilters.js:420-432", "server/utils/queries/libraryItemsBookFilters.js:540-546", "server/models/LibraryItem.js:305-322"},
			Claim:   "include=rssfeed,share adds rssFeed to items with an open feed for everyone and mediaItemShare to shared items for admins only; without include neither appears; include is echoed lower-cased.",
			Run: func(t *testing.T, e *harness.Env) {
				s.enrich(t, e)
				for _, who := range []string{"root", "listener"} {
					plain := getItems(t, e, e.Token(t, who), bookLibrary, query("sort", "media.metadata.title"))
					for _, item := range plain.Results {
						if item.RssFeed != nil || item.MediaItemShare != nil {
							t.Fatalf("%s: %s has feed/share without include", who, item.ID)
						}
					}
					page := getItems(t, e, e.Token(t, who), bookLibrary, query("sort", "media.metadata.title", "include", "RSSFeed, share", "minified", "1"))
					if page.Include != "rssfeed,share" {
						t.Fatalf("include echo %q", page.Include)
					}
					feeds := map[string]string{}
					shares := map[string]string{}
					for _, item := range page.Results {
						if item.RssFeed != nil {
							if item.RssFeed.EntityID != item.ID {
								t.Fatalf("feed entity %s for item %s", item.RssFeed.EntityID, item.ID)
							}
							feeds[item.ID] = item.RssFeed.FeedURL
						}
						if item.MediaItemShare != nil {
							shares[item.ID] = item.MediaItemShare.Slug
						}
						if item.NumEpisodesIncomplete != nil {
							t.Fatalf("book item has numEpisodesIncomplete")
						}
					}
					if len(feeds) != 2 || !strings.HasSuffix(feeds[e.IDs["saga1"]], "/feed/trial-feed-saga1") || !strings.HasSuffix(feeds[e.IDs["novel"]], "/feed/trial-feed-novel") {
						t.Fatalf("%s feeds: %v", who, feeds)
					}
					if who == "root" {
						if len(shares) != 2 || shares[e.IDs["saga2"]] != "trial-share-saga2" || shares[e.IDs["novel"]] != "trial-share-novel" {
							t.Fatalf("root shares: %v", shares)
						}
					} else if len(shares) != 0 {
						t.Fatalf("listener must not see shares: %v", shares)
					}
				}
			},
		},
		{
			Requirement: "route-r022", Invariant: "INV-R022-COLLAPSE-001", Case: "normal", State: "stateless", Assertion: "book-items-collapse-series",
			Sources: []string{srcCollapse, "server/utils/queries/libraryItemsBookFilters.js:486-500"},
			Claim:   "collapseseries=1 shows only the first book of each series (by sequence) carrying collapsedSeries, sorts by series name, and counts the collapsed list.",
			Run: func(t *testing.T, e *harness.Env) {
				s.enrich(t, e)
				token := e.Token(t, "listener")
				page := getItems(t, e, token, bookLibrary, query("collapseseries", "1", "sort", "media.metadata.title"))
				expectKeys(t, e, "collapsed", page, "ebook", "saga1", "novel")
				expectInt(t, "total", page.Total, 3)
				if !page.Collapse {
					t.Fatalf("collapseseries echo missing")
				}
				third, long, solo := page.Results[0].CollapsedSeries, page.Results[1].CollapsedSeries, page.Results[2].CollapsedSeries
				if third == nil || third.Name != "A Third Saga" || third.NumBooks != 2 || third.Sequence == nil || *third.Sequence != "2" {
					t.Fatalf("third series: %+v", third)
				}
				if long == nil || long.Name != "Long Saga" || long.NameIgnore != "Long Saga" || long.NumBooks != 2 || *long.Sequence != "1" || len(long.LibraryItemIDs) != 2 {
					t.Fatalf("long series: %+v", long)
				}
				expectSet(t, "long series items", long.LibraryItemIDs, fixtureIDs(e, "saga1", "saga2"))
				if solo == nil || solo.Name != "Solo Series" || solo.NumBooks != 1 {
					t.Fatalf("solo series: %+v", solo)
				}
				desc := getItems(t, e, token, bookLibrary, query("collapseseries", "1", "sort", "addedAt", "desc", "1"))
				expectKeys(t, e, "collapsed addedAt desc", desc, "saga1", "ebook", "novel")
			},
		},
		{
			Requirement: "route-r022", Invariant: "INV-R022-COLLAPSE-001", Case: "edge", State: "stateless", Assertion: "book-items-collapse-with-series-filter",
			Sources: []string{"server/controllers/LibraryController.js:622-631", "server/utils/libraryHelpers.js:105-215"},
			Claim:   "Filtering by one series with collapseseries=1 lists that series' books by sequence, pages them with limit and page, and reports the full count as total.",
			Run: func(t *testing.T, e *harness.Env) {
				s.enrich(t, e)
				token := e.Token(t, "listener")
				filter := filterParam("series", seriesIDByName(t, e, "Long Saga"))
				page := getItems(t, e, token, bookLibrary, query("filter", filter, "collapseseries", "1", "limit", "1"))
				expectKeys(t, e, "sub-series page 0", page, "saga1")
				expectInt(t, "total", page.Total, 2)
				page = getItems(t, e, token, bookLibrary, query("filter", filter, "collapseseries", "1", "limit", "1", "page", "1"))
				expectKeys(t, e, "sub-series page 1", page, "saga2")
				page = getItems(t, e, token, bookLibrary, query("filter", filter, "collapseseries", "1"))
				expectKeys(t, e, "sub-series all", page, "saga1", "saga2")
			},
		},
		{
			Requirement: "route-r022", Invariant: "INV-R022-COLLAPSE-001", Case: "edge", State: "stateless", Assertion: "book-items-collapse-subseries-invalid-and-sort",
			Sources: []string{srcCollapse, "server/utils/libraryHelpers.js:105-109", "server/controllers/LibraryController.js:626-636"},
			Claim:   "On the sub-series collapse path an unknown series id and a filter value whose decode fails answer an empty list with total 0, an invalid sort falls back to the sequence order, and a title sort orders by series or title.",
			Run: func(t *testing.T, e *harness.Env) {
				s.enrich(t, e)
				token := e.Token(t, "listener")
				unknown := query("filter", filterParam("series", "00000000-0000-4000-8000-000000000000"), "collapseseries", "1")
				page := getItems(t, e, token, bookLibrary, unknown)
				if len(page.Results) != 0 {
					t.Fatalf("unknown series with collapse: %v", keysOf(e, page.ids()))
				}
				expectInt(t, "unknown series total", page.Total, 0)
				// The raw value "%ZZ" reaches the server as a bare % inside decodeURIComponent and is decoded to null.
				broken := getItems(t, e, token, bookLibrary, query("filter", "series.%ZZ", "collapseseries", "1"))
				if len(broken.Results) != 0 {
					t.Fatalf("undecodable filter with collapse: %v", keysOf(e, broken.ids()))
				}
				expectInt(t, "undecodable total", broken.Total, 0)
				page = getItems(t, e, token, bookLibrary, query("filter", filterParam("series", seriesIDByName(t, e, long)), "collapseseries", "1", "sort", "nonsense"))
				expectKeys(t, e, "invalid sort falls back to sequence", page, "saga1", "saga2")
				page = getItems(t, e, token, bookLibrary, query("filter", filterParam("series", seriesIDByName(t, e, long)), "collapseseries", "1", "sort", "addedAt", "desc", "1"))
				expectKeys(t, e, "addedAt desc on the sub-series path", page, "saga2", "saga1")
				filter := filterParam("series", seriesIDByName(t, e, long))
				page = getItems(t, e, token, bookLibrary, query("filter", filter, "collapseseries", "1", "sort", "media.metadata.title"))
				expectKeys(t, e, "title sort on the sub-series path", page, "saga1", "saga2")
				root := e.Token(t, "root")
				mustOK(t, "enable prefix", patchJSON(t, e, root, "/api/settings", `{"sortingIgnorePrefix":true}`))
				page = getItems(t, e, token, bookLibrary, query("filter", filter, "collapseseries", "1", "sort", "media.metadata.title", "prefix", "1"))
				expectKeys(t, e, "title sort with prefix ignored", page, "saga1", "saga2")
				mustOK(t, "disable prefix", patchJSON(t, e, root, "/api/settings", `{"sortingIgnorePrefix":false}`))
			},
		},
		{
			Requirement: "route-r022", Invariant: "INV-R022-COLLAPSE-001", Case: "edge", State: "stateless", Assertion: "book-items-collapse-subseries-collapsed-json",
			Sources: []string{srcCollapse, "server/utils/libraryHelpers.js:232-260"},
			Claim:   "On the sub-series collapse path a book that also belongs to another series is represented by that series with collapsedSeries carrying the sequence inside the filtered series and the seriesSequenceList ranges.",
			Run: func(t *testing.T, e *harness.Env) {
				s.enrich(t, e)
				root := e.Token(t, "root")
				mustOK(t, "add novel to Long Saga", patchJSON(t, e, root, "/api/items/"+e.IDs["novel"]+"/media", `{"metadata":{"series":[{"name":"Solo Series","sequence":"1"},{"name":"Long Saga","sequence":"3"}]}}`))
				filter := filterParam("series", seriesIDByName(t, e, long))
				page := getItems(t, e, e.Token(t, "listener"), bookLibrary, query("filter", filter, "collapseseries", "1"))
				expectKeys(t, e, "sub-series with a sub-collapsed book", page, "saga1", "saga2", "novel")
				expectInt(t, "total", page.Total, 3)
				collapsed := page.Results[2].CollapsedSeries
				soloID := seriesIDByName(t, e, solo)
				// On the sub-series path the collapsed libraryItemIds are library item ids.
				if collapsed == nil || collapsed.ID != soloID || collapsed.Name != solo || collapsed.NumBooks != 1 || len(collapsed.LibraryItemIDs) != 1 || collapsed.LibraryItemIDs[0] != e.IDs["novel"] {
					t.Fatalf("collapsed series of novel: %+v (soloID=%s)", collapsed, soloID)
				}
				// The sub-series collapsedSeries carries no sequence, only the sequence list inside the filtered series.
				if collapsed.Sequence != nil || collapsed.SeriesSequenceList != "3" {
					t.Fatalf("collapsed series sequences: %+v", collapsed)
				}
				// With hideSingleBookSeries on, a single-book sub-series is not collapsed: the book stays flat.
				setLibrarySettings(t, e, `{"hideSingleBookSeries":true}`)
				hidden := getItems(t, e, e.Token(t, "listener"), bookLibrary, query("filter", filter, "collapseseries", "1", "hidden", "1"))
				expectKeys(t, e, "single-book sub-series stays flat", hidden, "saga1", "saga2", "novel")
				if collapsed := hidden.Results[2].CollapsedSeries; collapsed != nil {
					t.Fatalf("novel must be flat with the hide setting: %+v", collapsed)
				}
				setLibrarySettings(t, e, `{"hideSingleBookSeries":false}`)
				mustOK(t, "restore novel series", patchJSON(t, e, root, "/api/items/"+e.IDs["novel"]+"/media", `{"metadata":{"series":[{"name":"Solo Series","sequence":"1"}]}}`))
				page = getItems(t, e, e.Token(t, "listener"), bookLibrary, query("filter", filter, "collapseseries", "1"))
				expectKeys(t, e, "restored sub-series", page, "saga1", "saga2")
			},
		},
		{
			Requirement: "route-r022", Invariant: "INV-R022-COLLAPSE-001", Case: "edge", State: "stateless", Assertion: "book-items-collapse-subseries-sorts-and-merge",
			Sources: []string{srcCollapse, "server/utils/libraryHelpers.js:173-182", "server/utils/libraryHelpers.js:184-213", "server/utils/libraryHelpers.js:240-259"},
			Claim:   "On the sub-series collapse path a two-book sub-series merges its sequences into one range, and the collapsed series sorts by name under non-title sorts and by its name or the title under title sorts, with and without sortingIgnorePrefix.",
			Run: func(t *testing.T, e *harness.Env) {
				s.enrich(t, e)
				root := e.Token(t, "root")
				defer func() {
					mustOK(t, "restore novel", patchJSON(t, e, root, "/api/items/"+e.IDs["novel"]+"/media", `{"metadata":{"series":[{"name":"Solo Series","sequence":"1"}]}}`))
					mustOK(t, "restore saga2", patchJSON(t, e, root, "/api/items/"+e.IDs["saga2"]+"/media", `{"metadata":{"series":[{"name":"Long Saga","sequence":"2"}]}}`))
				}()
				mustOK(t, "novel joins Long Saga", patchJSON(t, e, root, "/api/items/"+e.IDs["novel"]+"/media", `{"metadata":{"series":[{"name":"Solo Series","sequence":"1"},{"name":"Long Saga","sequence":"3"}]}}`))
				mustOK(t, "saga2 joins Solo Series", patchJSON(t, e, root, "/api/items/"+e.IDs["saga2"]+"/media", `{"metadata":{"series":[{"name":"Long Saga","sequence":"2"},{"name":"Solo Series","sequence":"2"}]}}`))
				filter := filterParam("series", seriesIDByName(t, e, long))
				page := getItems(t, e, e.Token(t, "listener"), bookLibrary, query("filter", filter, "collapseseries", "1"))
				expectKeys(t, e, "two-book sub-series", page, "saga1", "novel")
				collapsed := page.Results[1].CollapsedSeries
				if collapsed == nil || collapsed.Name != solo || collapsed.NumBooks != 2 || len(collapsed.LibraryItemIDs) != 2 || collapsed.LibraryItemIDs[0] != e.IDs["novel"] || collapsed.LibraryItemIDs[1] != e.IDs["saga2"] {
					t.Fatalf("merged collapsed series: %+v", collapsed)
				}
				if collapsed.SeriesSequenceList != "2-3" {
					t.Fatalf("merged sequence list: %+v", collapsed)
				}
				page = getItems(t, e, e.Token(t, "listener"), bookLibrary, query("filter", filter, "collapseseries", "1", "sort", "addedAt", "desc", "1", "merge", "1"))
				expectKeys(t, e, "addedAt desc sends the collapsed series last", page, "saga1", "novel")
				mustOK(t, "enable prefix", patchJSON(t, e, root, "/api/settings", `{"sortingIgnorePrefix":true}`))
				page = getItems(t, e, e.Token(t, "listener"), bookLibrary, query("filter", filter, "collapseseries", "1", "sort", "addedAt", "desc", "1", "merge", "2"))
				expectKeys(t, e, "addedAt desc with the prefix ignored", page, "saga1", "novel")
				page = getItems(t, e, e.Token(t, "listener"), bookLibrary, query("filter", filter, "collapseseries", "1", "sort", "media.metadata.title", "merge", "3"))
				expectKeys(t, e, "title sort with the prefix ignored", page, "saga1", "novel")
				mustOK(t, "disable prefix", patchJSON(t, e, root, "/api/settings", `{"sortingIgnorePrefix":false}`))
				page = getItems(t, e, e.Token(t, "listener"), bookLibrary, query("filter", filter, "collapseseries", "1", "sort", "media.metadata.title", "merge", "4"))
				expectKeys(t, e, "title sort", page, "novel", "saga1")
			},
		},
	}
}

// createSecondPodcast adds a second podcast so the podcast library has an
// order to assert; it is explicit, English, by another author.
func (s *suite) createSecondPodcast(t *testing.T, e *harness.Env) {
	t.Helper()
	if s.podcastReady {
		return
	}
	root := e.Token(t, "root")
	var library struct {
		Folders []struct{ ID, FullPath string } `json:"folders"`
	}
	resp := e.Get(t, root, libraryPath(e, podcastLibrary, ""))
	mustOK(t, "podcast library", resp)
	resp.JSON(t, &library)
	body, err := json.Marshal(map[string]any{
		"libraryId": e.IDs[podcastLibrary], "folderId": library.Folders[0].ID, "path": library.Folders[0].FullPath + "/Alpha Cast",
		"media": map[string]any{"tags": []string{"alpha-pod"}, "metadata": map[string]any{
			"title": "Alpha Cast", "author": "Zed Host", "language": "English", "explicit": true, "genres": []string{"Comedy"}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	mustOK(t, "create podcast", postJSON(t, e, root, "/api/podcasts", string(body)))
	s.podcastReady = true
}

func (s *suite) podcastScenarios() []harness.Scenario {
	return []harness.Scenario{
		{
			Requirement: "route-r022", Invariant: "INV-R022-PODCAST-001", Case: "normal", State: "stateless", Assertion: "podcast-items-shape-and-include",
			Sources: []string{"server/utils/queries/libraryItemsPodcastFilters.js:131-250", "server/models/LibraryItem.js:305-322"},
			Claim:   "The podcast library lists its podcast with author, genres, numEpisodes, rssFeed under include=rssfeed, and numEpisodesIncomplete counted per user.",
			Run: func(t *testing.T, e *harness.Env) {
				s.enrich(t, e)
				include := "rssfeed,numEpisodesIncomplete,share"
				page := getItems(t, e, e.Token(t, "listener"), podcastLibrary, query("include", include, "minified", "1", "limit", "10", "page", "0"))
				expectKeys(t, e, "podcast list", page, "podcast")
				expectInt(t, "total", page.Total, 1)
				item := page.Results[0]
				if page.MediaType != "podcast" || item.MediaType != "podcast" || item.Media.Metadata.Title != "Fixture Cast" || item.Media.Metadata.Author != "Fixture Host" || len(item.Media.Metadata.Genres) != 1 || item.Media.Metadata.Genres[0] != "Technology" {
					t.Fatalf("podcast item: %+v", item)
				}
				if item.Media.NumEpisodes == nil || *item.Media.NumEpisodes != 3 || item.Size != 219867 {
					t.Fatalf("podcast counts: %+v", item.Media.NumEpisodes)
				}
				if item.RssFeed == nil || !strings.HasSuffix(item.RssFeed.FeedURL, "/feed/fixture-cast-feed") {
					t.Fatalf("rssFeed: %+v", item.RssFeed)
				}
				expectInt(t, "listener incomplete (one of three finished)", item.NumEpisodesIncomplete, 2)
				other := getItems(t, e, e.Token(t, "finisher"), podcastLibrary, query("include", include))
				expectInt(t, "finisher incomplete", other.Results[0].NumEpisodesIncomplete, 3)
				bare := getItems(t, e, e.Token(t, "listener"), podcastLibrary, nil)
				if bare.Results[0].NumEpisodesIncomplete != nil || bare.Results[0].RssFeed != nil {
					t.Fatalf("podcast fields without include")
				}
			},
		},
		{
			Requirement: "route-r022", Invariant: "INV-R022-PODCAST-001", Case: "normal", State: "stateless", Assertion: "podcast-items-sorts",
			Sources: []string{"server/utils/queries/libraryItemsPodcastFilters.js:67-95", "client/components/controls/LibrarySortSelect.vue:58-95"},
			Claim:   "With a second podcast, every podcast sort key of the client menu orders the two podcasts as expected.",
			Run: func(t *testing.T, e *harness.Env) {
				s.createSecondPodcast(t, e)
				token := e.Token(t, "root")
				second := "Alpha Cast"
				for _, c := range []struct {
					sort string
					desc bool
					want []string
				}{
					{"media.metadata.title", false, []string{second, "Fixture Cast"}},
					{"media.metadata.title", true, []string{"Fixture Cast", second}},
					{"media.metadata.author", false, []string{"Fixture Cast", second}},
					{"media.metadata.author", true, []string{second, "Fixture Cast"}},
					{"addedAt", false, []string{"Fixture Cast", second}},
					{"addedAt", true, []string{second, "Fixture Cast"}},
					{"birthtimeMs", false, []string{"Fixture Cast", second}},
					{"birthtimeMs", true, []string{second, "Fixture Cast"}},
					{"mtimeMs", false, []string{"Fixture Cast", second}},
					{"mtimeMs", true, []string{second, "Fixture Cast"}},
					{"size", false, []string{second, "Fixture Cast"}},
					{"size", true, []string{"Fixture Cast", second}},
					{"media.numTracks", false, []string{second, "Fixture Cast"}},
					{"media.numTracks", true, []string{"Fixture Cast", second}},
				} {
					q := query("sort", c.sort)
					if c.desc {
						q.Set("desc", "1")
					}
					page := getItems(t, e, token, podcastLibrary, q)
					expectOrder(t, "podcast sort "+q.Encode(), page.titles(), c.want)
					expectInt(t, "total", page.Total, 2)
				}
				random := getItems(t, e, token, podcastLibrary, query("sort", "random"))
				expectSet(t, "podcast random", random.titles(), []string{second, "Fixture Cast"})
				page := getItems(t, e, token, podcastLibrary, query("sort", "media.metadata.title", "limit", "1", "page", "1"))
				expectOrder(t, "podcast page 1", page.titles(), []string{"Fixture Cast"})
				// The title sort honours the server setting for podcasts too.
				root := e.Token(t, "root")
				mustOK(t, "enable prefix", patchJSON(t, e, root, "/api/settings", `{"sortingIgnorePrefix":true}`))
				page = getItems(t, e, token, podcastLibrary, query("sort", "media.metadata.title"))
				expectOrder(t, "podcast title sort with prefix ignored", page.titles(), []string{second, "Fixture Cast"})
				mustOK(t, "disable prefix", patchJSON(t, e, root, "/api/settings", `{"sortingIgnorePrefix":false}`))
			},
		},
		{
			Requirement: "route-r022", Invariant: "INV-R022-PODCAST-001", Case: "normal", State: "stateless", Assertion: "podcast-items-filters",
			Sources: []string{"server/utils/queries/libraryItemsPodcastFilters.js:46-65", "client/components/controls/LibraryFilterSelect.vue:262-305"},
			Claim:   "The podcast filter groups genres, tags, languages, explicit, issues and feed-open select the expected podcasts, and book-only groups do not filter.",
			Run: func(t *testing.T, e *harness.Env) {
				s.createSecondPodcast(t, e)
				token := e.Token(t, "root")
				both := []string{"Alpha Cast", "Fixture Cast"}
				for _, c := range []struct {
					group, value string
					want         []string
				}{
					{"genres", "Technology", []string{"Fixture Cast"}},
					{"genres", "Comedy", []string{"Alpha Cast"}},
					{"genres", "Absent", nil},
					{"tags", "pod-tag", []string{"Fixture Cast"}},
					{"tags", "alpha-pod", []string{"Alpha Cast"}},
					{"languages", "English", both},
					{"explicit", "", []string{"Alpha Cast"}},
					{"issues", "", nil},
					{"feed-open", "", []string{"Fixture Cast"}},
					{"progress", "finished", both},
					{"missing", "asin", both},
				} {
					page := getItems(t, e, token, podcastLibrary, query("filter", filterParam(c.group, c.value), "sort", "media.metadata.title"))
					expectOrder(t, "podcast filter "+c.group+"."+c.value, page.titles(), append([]string{}, c.want...))
					expectInt(t, "total", page.Total, len(c.want))
				}
				page := getItems(t, e, token, podcastLibrary, query("filter", "recent", "sort", "media.metadata.title"))
				expectOrder(t, "podcast recent", page.titles(), both)
			},
		},
	}
}

func (s *suite) restrictionScenarios() []harness.Scenario {
	return []harness.Scenario{
		{
			Requirement: "route-r022", Invariant: "INV-R022-RESTRICT-001", Case: "normal", State: "stateless", Assertion: "book-items-explicit-restriction",
			Sources: []string{"server/utils/queries/libraryItemsBookFilters.js:15-37", "server/utils/queries/libraryItemsPodcastFilters.js:16-24"},
			Claim:   "A user without explicit access never gets explicit books or podcasts in the lists, their totals or the explicit filter.",
			Run: func(t *testing.T, e *harness.Env) {
				s.enrich(t, e)
				s.createSecondPodcast(t, e)
				finisher := e.Token(t, "finisher")
				page := getItems(t, e, finisher, bookLibrary, query("sort", "media.metadata.title", "limit", "10"))
				expectKeys(t, e, "finisher books", page, "disposable", "novel", "saga1", "saga2")
				expectInt(t, "finisher total", page.Total, 4)
				page = getItems(t, e, finisher, bookLibrary, query("filter", "explicit"))
				expectKeys(t, e, "finisher explicit filter", page)
				pods := getItems(t, e, finisher, podcastLibrary, query("sort", "media.metadata.title"))
				expectOrder(t, "finisher podcasts", pods.titles(), []string{"Fixture Cast"})
				expectInt(t, "finisher podcast total", pods.Total, 1)
			},
		},
		{
			Requirement: "route-r022", Invariant: "INV-R022-RESTRICT-001", Case: "normal", State: "stateless", Assertion: "book-items-tag-restriction",
			Sources: []string{"server/utils/queries/libraryItemsBookFilters.js:22-35"},
			Claim:   "A user limited to the tag alpha sees only books tagged alpha, and a user with tag beta not accessible sees all books except those tagged beta.",
			Run: func(t *testing.T, e *harness.Env) {
				s.enrich(t, e)
				tagged := getItems(t, e, s.restrictedToken(t, e, "browse-tagged"), bookLibrary, query("sort", "media.metadata.title"))
				expectKeys(t, e, "tagged", tagged, "novel", "saga1")
				expectInt(t, "tagged total", tagged.Total, 2)
				notag := getItems(t, e, s.restrictedToken(t, e, "browse-notag"), bookLibrary, query("sort", "media.metadata.title"))
				expectKeys(t, e, "notag", notag, "disposable", "saga1", "saga2")
				expectInt(t, "notag total", notag.Total, 3)
				withFilter := getItems(t, e, s.restrictedToken(t, e, "browse-tagged"), bookLibrary, query("filter", filterParam("tags", "beta")))
				expectKeys(t, e, "tagged beta filter", withFilter, "novel")
			},
		},
		{
			Requirement: "route-r022", Invariant: "INV-R022-RESTRICT-001", Case: "edge", State: "stateless", Assertion: "book-items-restriction-in-collapsed-list",
			Sources: []string{srcCollapse},
			Claim:   "Collapsing series respects the user's tag restriction: a series is represented by its first accessible book and counts of the collapsed list follow.",
			Run: func(t *testing.T, e *harness.Env) {
				s.enrich(t, e)
				page := getItems(t, e, s.restrictedToken(t, e, "browse-notag"), bookLibrary, query("collapseseries", "1", "sort", "media.metadata.title"))
				expectKeys(t, e, "notag collapsed", page, "disposable", "saga1")
				expectInt(t, "total", page.Total, 2)
			},
		},
	}
}
