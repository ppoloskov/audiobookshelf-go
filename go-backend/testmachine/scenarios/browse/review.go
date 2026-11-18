package browse

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"audiobookshelf-compat/testmachine/scenarios/harness"
)

// plainBook adds a book without author and series by copying one audio file
// into a new top-level library folder and scanning the library. It is created
// now, so it is inside the 60-day window. The id is stored as fixture key "plain".
func (s *suite) plainBook(t *testing.T, e *harness.Env) {
	t.Helper()
	if s.plain {
		return
	}
	source := filepath.Join(e.Media, "audiobooks", "Terry Fixture", "Long Saga", "Book 1 - The Beginning", "01 - Part 1.mp3")
	dir := filepath.Join(e.Media, "audiobooks", "Plain Book")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	in, err := os.Open(source)
	if err != nil {
		t.Fatal(err)
	}
	defer in.Close() // read-only
	out, err := os.Create(filepath.Join(dir, "Plain Book.mp3"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.Copy(out, in); err != nil {
		t.Fatal(err)
	}
	if err := out.Close(); err != nil {
		t.Fatal(err)
	}
	root := e.Token(t, "root")
	mustOK(t, "scan", e.Post(t, root, libraryPath(e, bookLibrary, "/scan"), nil))
	known := map[string]bool{}
	for _, id := range e.IDs {
		known[id] = true
	}
	probe := 0
	harness.Eventually(t, 30*time.Second, "the scanned book", func() bool {
		probe++
		page := getItems(t, e, root, bookLibrary, query("limit", strconv.Itoa(100+probe)))
		for _, item := range page.Results {
			if !known[item.ID] {
				e.IDs["plain"] = item.ID
				return true
			}
		}
		return false
	})
	// The scanner takes title and author from the audio tags; make the book plain.
	mustOK(t, "plain metadata", patchJSON(t, e, root, "/api/items/"+e.IDs["plain"]+"/media", `{"metadata":{"title":"Plain Book","authors":[],"series":[]}}`))
	s.plain = true
}

// setLibrarySettings patches settings of the book library through the API.
func setLibrarySettings(t *testing.T, e *harness.Env, settings string) {
	t.Helper()
	mustOK(t, "library settings", patchJSON(t, e, e.Token(t, "root"), "/api/libraries/"+e.IDs[bookLibrary], `{"settings":`+settings+`}`))
}

// execStopped runs statements with the server stopped and restarts it.
func execStopped(t *testing.T, e *harness.Env, statements ...[]any) {
	t.Helper()
	e.Stop(t)
	for _, statement := range statements {
		e.Exec(t, statement[0].(string), statement[1:]...)
	}
	e.Restart(t)
}

func (s *suite) reviewScenarios() []harness.Scenario {
	return []harness.Scenario{
		{
			Requirement: "route-r022", Invariant: "INV-R022-FILTER-001", Case: "normal", State: "stateless", Assertion: "book-items-filters-select-books-without-series-and-author",
			Sources: []string{srcBookGroup, "server/utils/queries/libraryItemsBookFilters.js:370-381"},
			Claim:   "With a book that has no author and no series, series.no-series, missing.series and missing.authors select exactly that book, with and without collapseseries.",
			Run: func(t *testing.T, e *harness.Env) {
				s.enrich(t, e)
				s.plainBook(t, e)
				token := e.Token(t, "listener")
				for _, f := range [][2]string{{"series", "no-series"}, {"missing", "series"}, {"missing", "authors"}} {
					for _, collapse := range []string{"", "1"} {
						page := getItems(t, e, token, bookLibrary, query("filter", filterParam(f[0], f[1]), "collapseseries", collapse))
						expectKeys(t, e, f[0]+"."+f[1]+" collapse="+collapse, page, "plain")
						expectInt(t, "total", page.Total, 1)
					}
				}
				meta := getItems(t, e, token, bookLibrary, query("filter", filterParam("missing", "authors"))).Results[0].Media.Metadata
				if meta.Title != "Plain Book" || meta.AuthorName != "" || meta.SeriesName != "" {
					t.Fatalf("plain book metadata: %+v", meta)
				}
			},
		},
		{
			Requirement: "route-r022", Invariant: "INV-R022-COLLAPSE-001", Case: "normal", State: "stateless", Assertion: "book-items-collapse-with-filters",
			Sources: []string{srcCollapse, "server/utils/queries/libraryItemsBookFilters.js:49-99", "client/components/app/LazyBookshelf.vue:482-486"},
			Claim:   "collapseseries=1 combined with progress, genre and tag filters represents each series by its first matching book (sorted by series name), and the finisher and root see their own progress.",
			Run: func(t *testing.T, e *harness.Env) {
				s.enrich(t, e)
				s.plainBook(t, e)
				for _, c := range []struct {
					who, group, value string
					collapsed, flat   []string
				}{
					{"listener", "progress", "finished", []string{"saga1", "novel"}, []string{"novel", "saga1"}},
					{"listener", "progress", "in-progress", []string{"ebook", "saga2"}, []string{"ebook", "saga2"}},
					{"listener", "progress", "not-started", []string{"ebook", "plain"}, []string{"ebook", "plain"}},
					{"listener", "progress", "not-finished", []string{"ebook", "saga2", "plain"}, []string{"ebook", "plain", "saga2"}},
					{"listener", "genres", "Mystery", []string{"ebook", "saga2", "novel"}, []string{"ebook", "novel", "saga2"}},
					{"listener", "tags", "beta", []string{"ebook", "novel"}, []string{"ebook", "novel"}},
					{"root", "progress", "not-started", []string{"ebook", "saga1", "plain", "novel"}, []string{"ebook", "plain", "novel", "saga1", "saga2"}},
					{"root", "progress", "finished", nil, nil},
				} {
					token := e.Token(t, c.who)
					filter := filterParam(c.group, c.value)
					flat := getItems(t, e, token, bookLibrary, query("filter", filter, "sort", "media.metadata.title"))
					expectKeys(t, e, c.who+" "+c.group+"."+c.value+" flat", flat, c.flat...)
					collapsed := getItems(t, e, token, bookLibrary, query("filter", filter, "collapseseries", "1", "sort", "media.metadata.title"))
					expectKeys(t, e, c.who+" "+c.group+"."+c.value+" collapsed", collapsed, c.collapsed...)
					expectInt(t, "collapsed total", collapsed.Total, len(c.collapsed))
				}
			},
		},
		{
			Requirement: "route-r022", Invariant: "INV-R022-COLLAPSE-001", Case: "edge", State: "stateless", Assertion: "book-items-collapse-title-sort-ignores-prefix-setting",
			Sources: []string{"server/utils/queries/libraryItemsBookFilters.js:344-352", "server/utils/queries/libraryItemsBookFilters.js:599"},
			Claim:   "With sortingIgnorePrefix on, a collapsed list sorts by the series name without its article, so A Third Saga sorts under T.",
			Run: func(t *testing.T, e *harness.Env) {
				s.enrich(t, e)
				s.plainBook(t, e)
				root := e.Token(t, "root")
				token := e.Token(t, "listener")
				plain := getItems(t, e, token, bookLibrary, query("collapseseries", "1", "sort", "media.metadata.title", "limit", "50"))
				expectKeys(t, e, "plain order", plain, "ebook", "saga1", "plain", "novel")
				mustOK(t, "enable", patchJSON(t, e, root, "/api/settings", `{"sortingIgnorePrefix":true}`))
				page := getItems(t, e, token, bookLibrary, query("collapseseries", "1", "sort", "media.metadata.title", "limit", "51"))
				mustOK(t, "disable", patchJSON(t, e, root, "/api/settings", `{"sortingIgnorePrefix":false}`))
				expectKeys(t, e, "ignore prefix", page, "saga1", "plain", "novel", "ebook")
			},
		},
		{
			Requirement: "route-r022", Invariant: "INV-R022-FILTER-001", Case: "normal", State: "stateless", Assertion: "book-items-filter-issues-and-supplementary-ebooks",
			Sources: []string{"server/utils/queries/libraryItemsBookFilters.js:289-300", "server/utils/queries/libraryItemsPodcastFilters.js:164-172", "client/components/controls/LibraryFilterSelect.vue:226-246"},
			Claim:   "The issues filter selects the missing or invalid book and podcast, and ebooks.supplementary and no-supplementary split on a supplementary ebook file; each is proven non-empty, then restored.",
			Run: func(t *testing.T, e *harness.Env) {
				s.enrich(t, e)
				s.createSecondPodcast(t, e)
				s.plainBook(t, e)
				podcastRow := e.Rows(t, "SELECT li.id FROM libraryItems li JOIN podcasts p ON p.id = li.mediaId WHERE p.title = 'Alpha Cast'")[0]["id"].(string)
				execStopped(t,
					e,
					[]any{"UPDATE libraryItems SET isMissing = 1 WHERE id = ?", e.IDs["plain"]},
					[]any{"UPDATE libraryItems SET isInvalid = 1 WHERE id = ?", podcastRow},
					[]any{"UPDATE libraryItems SET libraryFiles = replace(libraryFiles, '\"isSupplementary\":false', '\"isSupplementary\":true') WHERE id = ?", e.IDs["ebook"]},
				)
				token := e.Token(t, "root")
				issues := getItems(t, e, token, bookLibrary, query("filter", "issues"))
				expectKeys(t, e, "book issues", issues, "plain")
				if !issues.Results[0].IsMissing || issues.Results[0].IsInvalid {
					t.Fatalf("issue flags: %+v", issues.Results[0])
				}
				pods := getItems(t, e, token, podcastLibrary, query("filter", "issues"))
				expectOrder(t, "podcast issues", pods.titles(), []string{"Alpha Cast"})
				if !pods.Results[0].IsInvalid {
					t.Fatalf("podcast must be flagged invalid")
				}
				with := getItems(t, e, token, bookLibrary, query("filter", filterParam("ebooks", "supplementary")))
				expectKeys(t, e, "supplementary", with, "ebook")
				without := getItems(t, e, token, bookLibrary, query("filter", filterParam("ebooks", "no-supplementary"), "sort", "media.metadata.title"))
				expectKeys(t, e, "no supplementary", without, "plain", "novel", "saga1", "saga2")
				execStopped(t,
					e,
					[]any{"UPDATE libraryItems SET isMissing = 0, isInvalid = 0"},
					[]any{"UPDATE libraryItems SET libraryFiles = replace(libraryFiles, '\"isSupplementary\":true', '\"isSupplementary\":false')"},
				)
				token = e.Token(t, "root")
				expectKeys(t, e, "issues after restore", getItems(t, e, token, bookLibrary, query("filter", "issues", "again", "1")))
			},
		},
		{
			Requirement: "route-r022", Invariant: "INV-R022-FILTER-001", Case: "edge", State: "stateless", Assertion: "book-and-series-filters-with-non-ascii-and-base64-symbols",
			Sources: []string{srcLibFilter, "server/utils/queries/seriesFilters.js:12-20", "client/plugins/init.client.js:170-173"},
			Claim:   "Filter values with non-ASCII text and base64 containing + and / (encodeURIComponent of base64 of UTF-8) select the right books and series.",
			Run: func(t *testing.T, e *harness.Env) {
				s.enrich(t, e)
				s.plainBook(t, e)
				root := e.Token(t, "root")
				mustOK(t, "plain", patchJSON(t, e, root, "/api/items/"+e.IDs["plain"]+"/media", `{"metadata":{"genres":["Ünïcode Genre"],"narrators":["Zoë Ñandú"],"publisher":"Éditions Ünï"},"tags":["???>>>","日本語タグ"]}`))
				mustOK(t, "novel", patchJSON(t, e, root, "/api/items/"+e.IDs["novel"]+"/media", `{"metadata":{"genres":["Fiction","Mystery","Ünïcode Genre"]},"tags":["alpha","beta","???>>>"]}`))
				if got := filterParam("tags", "???>>>"); got != "tags.Pz8%2FPj4%2B" {
					t.Fatalf("test value must contain + and / in base64: %s", got)
				}
				for _, c := range []struct {
					group, value string
					want         []string
					series       []string
				}{
					{"tags", "???>>>", []string{"plain", "novel"}, []string{solo}},
					{"tags", "日本語タグ", []string{"plain"}, nil},
					{"genres", "Ünïcode Genre", []string{"plain", "novel"}, []string{solo}},
					{"narrators", "Zoë Ñandú", []string{"plain"}, nil},
					{"publishers", "Éditions Ünï", []string{"plain"}, nil},
				} {
					page := getItems(t, e, root, bookLibrary, query("filter", filterParam(c.group, c.value), "sort", "media.metadata.title"))
					expectKeys(t, e, "items "+c.group+"."+c.value, page, c.want...)
					list := getSeriesPage(t, e, root, query("limit", "10", "sort", "name", "filter", filterParam(c.group, c.value)))
					expectOrder(t, "series "+c.group+"."+c.value, list.names(), append([]string{}, c.series...))
				}
			},
		},
		{
			Requirement: "route-r022", Invariant: "INV-R022-RESTRICT-001", Case: "normal", State: "stateless", Assertion: "podcast-items-tag-restrictions",
			Sources: []string{"server/utils/queries/libraryItemsPodcastFilters.js:25-45", "server/utils/queries/libraryItemsPodcastFilters.js:366-440"},
			Claim:   "A user limited to the tag pod-tag sees only the tagged podcast in lists, search hits and shelves; a user for whom pod-tag is not accessible sees only the other podcast, and no episode shelf or episode hit.",
			Run: func(t *testing.T, e *harness.Env) {
				s.enrich(t, e)
				s.createSecondPodcast(t, e)
				allow := s.restrictedToken(t, e, "browse-podtag")
				deny := s.restrictedToken(t, e, "browse-podnotag")
				expectOrder(t, "allow list", getItems(t, e, allow, podcastLibrary, query("sort", "media.metadata.title")).titles(), []string{"Fixture Cast"})
				expectOrder(t, "deny list", getItems(t, e, deny, podcastLibrary, query("sort", "media.metadata.title")).titles(), []string{"Alpha Cast"})
				if page := getItems(t, e, allow, podcastLibrary, query("sort", "media.metadata.title", "limit", "1")); *page.Total != 1 {
					t.Fatalf("allow total %d", *page.Total)
				}
				hits := search(t, e, allow, podcastLibrary, query("q", "cast"))
				if len(hits.Podcast) != 1 || hits.Podcast[0].LibraryItem.Media.Metadata.Title != "Fixture Cast" {
					t.Fatalf("allow search: %+v", hits.Podcast)
				}
				if got := len(search(t, e, allow, podcastLibrary, query("q", "third")).Episodes); got != 1 {
					t.Fatalf("allow episodes: %d", got)
				}
				hits = search(t, e, deny, podcastLibrary, query("q", "cast"))
				if len(hits.Podcast) != 1 || hits.Podcast[0].LibraryItem.Media.Metadata.Title != "Alpha Cast" {
					t.Fatalf("deny search: %+v", hits.Podcast)
				}
				if got := len(search(t, e, deny, podcastLibrary, query("q", "third")).Episodes); got != 0 {
					t.Fatalf("deny episodes: %d", got)
				}
				expectOrder(t, "allow shelves", summary(t, e, getShelves(t, e, allow, podcastLibrary, nil)), []string{
					"newest-episodes|episode|podcast/ep3,podcast/ep2,podcast/ep1|3", "recently-added|podcast|podcast|1"})
				expectOrder(t, "deny shelves", summary(t, e, getShelves(t, e, deny, podcastLibrary, nil)), []string{"recently-added|podcast|title:Alpha Cast|1"})
			},
		},
		{
			Requirement: "route-r029", Invariant: "INV-ROUTE-R029-RESTRICT-001", Case: "normal", State: "stateless", Assertion: "personalized-tag-excluded-user-recent-series",
			Sources: []string{"server/utils/queries/libraryFilters.js:226-250", "server/utils/queries/libraryItemsBookFilters.js:22-35"},
			Claim:   "For a user for whom the tag beta is not accessible, the recent-series shelf drops the series whose only books are tagged beta and lists the other series with their accessible books.",
			Run: func(t *testing.T, e *harness.Env) {
				s.enrich(t, e)
				s.plainBook(t, e)
				shelves := getShelves(t, e, s.restrictedToken(t, e, "browse-notag"), bookLibrary, nil)
				recent := shelfByID(t, shelves, "recent-series")
				if len(recent.Entities) != 1 || recent.Total != 1 {
					t.Fatalf("recent-series: %d entities, total %d", len(recent.Entities), recent.Total)
				}
				var series seriesJSON
				if err := json.Unmarshal(recent.Entities[0], &series); err != nil {
					t.Fatal(err)
				}
				expectOrder(t, "series "+series.Name, bookKeys(e, series), []string{"saga1", "saga2"})
				added := shelfItems(t, shelfByID(t, shelves, "recently-added"))
				ids := make([]string, 0, len(added))
				for _, item := range added {
					ids = append(ids, item.ID)
				}
				expectOrder(t, "recently-added", keysOf(e, ids), []string{"plain", "saga1", "saga2"})
			},
		},
		{
			Requirement: "route-r025", Invariant: "INV-R025-FILTER-001", Case: "edge", State: "stateless", Assertion: "series-and-shelves-hide-single-book-series-setting",
			Sources: []string{"server/utils/queries/seriesFilters.js:56-63", "server/utils/queries/libraryFilters.js:229-237", "client/components/modals/libraries/LibrarySettings.vue"},
			Claim:   "With the library setting hideSingleBookSeries on, the series list and the recent-series shelf omit series with a single book; switching it off restores them.",
			Run: func(t *testing.T, e *harness.Env) {
				s.enrich(t, e)
				token := e.Token(t, "listener")
				setLibrarySettings(t, e, `{"hideSingleBookSeries":true}`)
				page := getSeriesPage(t, e, token, query("limit", "10", "sort", "name", "hide", "1"))
				expectOrder(t, "hidden", page.names(), []string{long})
				expectInt(t, "total", page.Total, 1)
				recent := shelfByID(t, getShelves(t, e, token, bookLibrary, query("hide", "1")), "recent-series")
				if len(recent.Entities) != 1 || recent.Total != 1 {
					t.Fatalf("recent-series with hide: %d/%d", len(recent.Entities), recent.Total)
				}
				setLibrarySettings(t, e, `{"hideSingleBookSeries":false}`)
				page = getSeriesPage(t, e, token, query("limit", "10", "sort", "name", "hide", "0"))
				expectOrder(t, "shown", page.names(), []string{third, long, solo})
			},
		},
		{
			Requirement: "route-r029", Invariant: "INV-ROUTE-R029-CONTRACT", Case: "normal", State: "stateless", Assertion: "personalized-read-again-and-only-later-books-setting",
			Sources: []string{"server/models/LibraryItem.js:446-470", "server/utils/queries/libraryItemsBookFilters.js:716-730", "server/utils/queries/libraryItemsBookFilters.js:803-815"},
			Claim:   "A finished ebook-only book appears on read-again and a finished audiobook on listen-again; continue-series lists the first unfinished book, and with onlyShowLaterBooksInContinueSeries a series whose last finished book is the final one is dropped.",
			Run: func(t *testing.T, e *harness.Env) {
				s.enrich(t, e)
				root := e.Token(t, "root")
				mustOK(t, "finish saga2", patchJSON(t, e, root, "/api/me/progress/"+e.IDs["saga2"], `{"isFinished":true}`))
				mustOK(t, "finish ebook", patchJSON(t, e, root, "/api/me/progress/"+e.IDs["ebook"], `{"isFinished":true}`))
				shelves := getShelves(t, e, root, bookLibrary, nil)
				expectLabels(t, shelves)
				got := summary(t, e, shelves, "discover")
				for _, want := range []string{"continue-series|book|saga1|1", "listen-again|book|saga2|2", "read-again|book|ebook|2"} {
					if !slices.Contains(got, want) {
						t.Fatalf("shelves %v lack %s", got, want)
					}
				}
				setLibrarySettings(t, e, `{"onlyShowLaterBooksInContinueSeries":true}`)
				later := summary(t, e, getShelves(t, e, root, bookLibrary, query("later", "1")), "discover")
				setLibrarySettings(t, e, `{"onlyShowLaterBooksInContinueSeries":false}`)
				for _, line := range later {
					if strings.HasPrefix(line, "continue-series|") {
						t.Fatalf("no later book exists, continue-series must be absent: %v", later)
					}
				}
				if !slices.Contains(later, "read-again|book|ebook|2") {
					t.Fatalf("other shelves stay: %v", later)
				}
				for _, key := range []string{"saga2", "ebook"} {
					id := e.Rows(t, "SELECT mp.id FROM mediaProgresses mp JOIN users u ON u.id = mp.userId JOIN libraryItems li ON li.mediaId = mp.mediaItemId WHERE u.username = 'trial-root' AND li.id = ?", e.IDs[key])[0]["id"].(string)
					mustOK(t, "remove progress "+key, e.Delete(t, root, "/api/me/progress/"+id))
				}
			},
		},
		{
			Requirement: "route-r029", Invariant: "INV-ROUTE-R029-CONTRACT", Case: "normal", State: "stateless", Assertion: "personalized-hidden-from-continue-listening",
			Sources: []string{"server/utils/queries/libraryItemsBookFilters.js:316-330", "server/utils/queries/libraryItemsBookFilters.js:690-720", "server/controllers/MeController.js:532-587"},
			Claim:   "An item hidden from continue listening leaves the continue-listening and continue-reading shelves, and a series hidden by the user leaves continue-series until it is added back.",
			Run: func(t *testing.T, e *harness.Env) {
				s.enrich(t, e)
				listener := e.Token(t, "listener")
				progressID := func(key string) string {
					return e.Rows(t, "SELECT mp.id FROM mediaProgresses mp JOIN users u ON u.id = mp.userId JOIN libraryItems li ON li.mediaId = mp.mediaItemId WHERE u.username = 'trial-listener' AND li.id = ?", e.IDs[key])[0]["id"].(string)
				}
				before := summary(t, e, getShelves(t, e, listener, bookLibrary, nil), "discover")
				for _, want := range []string{"continue-listening|book|saga2|2", "continue-reading|book|ebook|2"} {
					if !slices.Contains(before, want) {
						t.Fatalf("precondition %v lacks %s", before, want)
					}
				}
				mustOK(t, "hide saga2", e.Get(t, listener, "/api/me/progress/"+progressID("saga2")+"/remove-from-continue-listening"))
				after := summary(t, e, getShelves(t, e, listener, bookLibrary, nil), "discover")
				for _, line := range after {
					if strings.HasPrefix(line, "continue-listening|") {
						t.Fatalf("hidden item still on continue-listening: %v", after)
					}
				}
				if !slices.Contains(after, "continue-reading|book|ebook|1") {
					t.Fatalf("continue-reading keeps the ebook with a smaller total: %v", after)
				}
				mustOK(t, "hide ebook", e.Get(t, listener, "/api/me/progress/"+progressID("ebook")+"/remove-from-continue-listening"))
				for _, line := range summary(t, e, getShelves(t, e, listener, bookLibrary, query("again", "1")), "discover") {
					if strings.HasPrefix(line, "continue-") {
						t.Fatalf("all hidden, yet %s", line)
					}
				}
				finisher := e.Token(t, "finisher")
				longID := seriesIDByName(t, e, long)
				if !slices.Contains(summary(t, e, getShelves(t, e, finisher, bookLibrary, nil), "discover"), "continue-series|book|saga2|1") {
					t.Fatalf("precondition: continue-series for the finisher")
				}
				mustOK(t, "hide series", e.Get(t, finisher, "/api/me/series/"+longID+"/remove-from-continue-listening"))
				for _, line := range summary(t, e, getShelves(t, e, finisher, bookLibrary, query("again", "1")), "discover") {
					if strings.HasPrefix(line, "continue-series|") {
						t.Fatalf("hidden series on continue-series: %s", line)
					}
				}
				mustOK(t, "readd series", e.Get(t, finisher, "/api/me/series/"+longID+"/readd-to-continue-listening"))
				if !slices.Contains(summary(t, e, getShelves(t, e, finisher, bookLibrary, query("again", "2")), "discover"), "continue-series|book|saga2|1") {
					t.Fatalf("series must return after readd")
				}
			},
		},
		{
			Requirement: "route-r031", Invariant: "INV-ROUTE-R031-CONTRACT", Case: "edge", State: "stateless", Assertion: "search-unicode-query-and-default-limit",
			Sources: []string{srcSearch, srcBookSearch, "server/Database.js (createTextSearchQuery)"},
			Claim:   "An accented query written in the title's own case matches the accented title (ASCII letters match in any case), and each section is capped at 12 by default, at the given limit otherwise.",
			Run: func(t *testing.T, e *harness.Env) {
				s.enrich(t, e)
				s.plainBook(t, e)
				root := e.Token(t, "root")
				tags := `"???>>>","日本語タグ"`
				for i := 1; i <= 14; i++ {
					tags += `,"limit-tag-` + fmt.Sprintf("%02d", i) + `"`
				}
				mustOK(t, "retitle", patchJSON(t, e, root, "/api/items/"+e.IDs["plain"]+"/media", `{"metadata":{"title":"Café Ünïcode Buch"},"tags":[`+tags+`]}`))
				for _, q := range []string{"Café", "café", "Café Ünïcode", "BUCH"} {
					hits := hitKeys(e, search(t, e, root, bookLibrary, query("q", q)).Book)
					if !slices.Equal(hits, []string{"plain"}) {
						t.Fatalf("query %q: %v", q, hits)
					}
				}
				for _, c := range []struct {
					limit string
					want  int
				}{{"", 12}, {"3", 3}, {"20", 14}} {
					q := query("q", "limit-tag")
					if c.limit != "" {
						q.Set("limit", c.limit)
					}
					if got := len(search(t, e, root, bookLibrary, q).Tags); got != c.want {
						t.Fatalf("limit %q: %d tags, want %d", c.limit, got, c.want)
					}
				}
				mustOK(t, "restore", patchJSON(t, e, root, "/api/items/"+e.IDs["plain"]+"/media", `{"metadata":{"title":"Plain Book"},"tags":["???>>>","日本語タグ"]}`))
			},
		},
	}
}
