package browse

import (
	"encoding/json"
	"net/http"
	"net/url"
	"slices"
	"testing"

	"audiobookshelf-compat/tests/replacement/harness"
)

const (
	srcSearch     = "server/controllers/LibraryController.js:959-972"
	srcBookSearch = "server/utils/queries/libraryItemsBookFilters.js:1093-1290"
	srcPodSearch  = "server/utils/queries/libraryItemsPodcastFilters.js:361-480"
)

// countName is a tag, genre or narrator hit.
type countName struct {
	Name     string `json:"name"`
	NumItems *int   `json:"numItems"`
	NumBooks *int   `json:"numBooks"`
}

// searchHit wraps a matched item.
type searchHit struct {
	LibraryItem listItem `json:"libraryItem"`
}

// searchResult is the payload of GET /api/libraries/:id/search.
type searchResult struct {
	Book      []searchHit `json:"book"`
	Podcast   []searchHit `json:"podcast"`
	Episodes  []searchHit `json:"episodes"`
	Narrators []countName `json:"narrators"`
	Tags      []countName `json:"tags"`
	Genres    []countName `json:"genres"`
	Series    []struct {
		Series seriesJSON `json:"series"`
		Books  []listItem `json:"books"`
	} `json:"series"`
	Authors []struct {
		Name     string `json:"name"`
		NumBooks int    `json:"numBooks"`
	} `json:"authors"`
}

// search runs the search route and decodes it.
func search(t *testing.T, e *harness.Env, token, libraryKey string, values url.Values) searchResult {
	t.Helper()
	resp := e.Do(t, harness.Request{Path: libraryPath(e, libraryKey, "/search"), Token: token, Query: values})
	mustOK(t, "search "+values.Encode(), resp)
	var result searchResult
	resp.JSON(t, &result)
	return result
}

// hitKeys maps matched items to fixture keys.
func hitKeys(e *harness.Env, hits []searchHit) []string {
	ids := make([]string, 0, len(hits))
	for _, hit := range hits {
		ids = append(ids, hit.LibraryItem.ID)
	}
	return keysOf(e, ids)
}

// names lists the names of count hits.
func countNames(hits []countName) []string {
	out := make([]string, 0, len(hits))
	for _, hit := range hits {
		out = append(out, hit.Name)
	}
	return out
}

// seriesHits lists the matched series names.
func (r searchResult) seriesNames() []string {
	out := make([]string, 0, len(r.Series))
	for _, hit := range r.Series {
		out = append(out, hit.Series.Name)
	}
	return out
}

// authorNames lists the matched author names.
func (r searchResult) authorNames() []string {
	out := make([]string, 0, len(r.Authors))
	for _, hit := range r.Authors {
		out = append(out, hit.Name)
	}
	return out
}

func (s *suite) searchScenarios() []harness.Scenario {
	return []harness.Scenario{
		{
			Requirement: "route-r031", Invariant: "INV-ROUTE-R031-CONTRACT", Case: "normal", State: "stateless", Assertion: "search-book-sections",
			Sources: []string{srcSearch, srcBookSearch, "client/components/controls/GlobalSearch.vue:180"},
			Claim:   "Book search matches titles, subtitles and asin as books, and names as series, authors, narrators, tags and genres, case-insensitively and trimmed, with counts.",
			Run: func(t *testing.T, e *harness.Env) {
				s.enrich(t, e)
				token := e.Token(t, "listener")
				titleCase := search(t, e, token, bookLibrary, query("q", "SAGA"))
				expectSet(t, "saga series", titleCase.seriesNames(), []string{long, third})
				if len(titleCase.Book) != 0 {
					t.Fatalf("saga books: %v", hitKeys(e, titleCase.Book))
				}
				for _, hit := range titleCase.Series {
					if hit.Series.LibraryID != e.IDs[bookLibrary] {
						t.Fatalf("series libraryId")
					}
					if hit.Series.Name == long {
						ids := make([]string, 0, 2)
						for _, book := range hit.Books {
							ids = append(ids, book.ID)
						}
						expectSet(t, "long saga books", keysOf(e, ids), []string{"saga1", "saga2"})
					}
				}
				the := search(t, e, token, bookLibrary, query("q", "  the "))
				expectSet(t, "the", hitKeys(e, the.Book), []string{"saga1", "saga2"})
				if the.Book[0].LibraryItem.Media.Metadata.Title == "" || the.Book[0].LibraryItem.MediaType != "book" {
					t.Fatalf("book hit is an item: %+v", the.Book[0])
				}
				if hits := hitKeys(e, search(t, e, token, bookLibrary, query("q", "middle")).Book); !slices.Equal(hits, []string{"saga2"}) {
					t.Fatalf("middle: %v", hits)
				}
				if hits := hitKeys(e, search(t, e, token, bookLibrary, query("q", "a sub")).Book); !slices.Equal(hits, []string{"novel"}) {
					t.Fatalf("subtitle: %v", hits)
				}
				if hits := hitKeys(e, search(t, e, token, bookLibrary, query("q", "B000TEST01")).Book); !slices.Equal(hits, []string{"novel"}) {
					t.Fatalf("asin: %v", hits)
				}
				fiction := search(t, e, token, bookLibrary, query("q", "fiction"))
				if len(fiction.Genres) != 1 || fiction.Genres[0].Name != "Fiction" || fiction.Genres[0].NumItems == nil || *fiction.Genres[0].NumItems != 1 || len(fiction.Book) != 0 {
					t.Fatalf("genres: %+v", fiction.Genres)
				}
				alpha := search(t, e, token, bookLibrary, query("q", "alpha"))
				if len(alpha.Tags) != 1 || alpha.Tags[0].Name != "alpha" || *alpha.Tags[0].NumItems != 2 {
					t.Fatalf("tags: %+v", alpha.Tags)
				}
				nora := search(t, e, token, bookLibrary, query("q", "nora"))
				if len(nora.Narrators) != 1 || nora.Narrators[0].Name != "Nora Narrator" || *nora.Narrators[0].NumBooks != 2 {
					t.Fatalf("narrators: %+v", nora.Narrators)
				}
				ada := search(t, e, token, bookLibrary, query("q", "ada"))
				if len(ada.Authors) != 1 || ada.Authors[0].Name != "Ada Example" || ada.Authors[0].NumBooks != 2 {
					t.Fatalf("authors: %+v", ada.Authors)
				}
				soloSeries := search(t, e, token, bookLibrary, query("q", "solo"))
				if got := soloSeries.seriesNames(); !slices.Equal(got, []string{solo}) {
					t.Fatalf("solo: %v", got)
				}
			},
		},
		{
			Requirement: "route-r031", Invariant: "INV-ROUTE-R031-CONTRACT", Case: "edge", State: "stateless", Assertion: "search-no-match-and-limit",
			Sources: []string{srcSearch, srcBookSearch},
			Claim:   "A query without matches returns every section empty, and limit caps each section's matches.",
			Run: func(t *testing.T, e *harness.Env) {
				s.enrich(t, e)
				token := e.Token(t, "listener")
				resp := e.Do(t, harness.Request{Path: libraryPath(e, bookLibrary, "/search"), Token: token, Query: query("q", "zzzzqq")})
				mustOK(t, "no match", resp)
				var raw map[string]json.RawMessage
				resp.JSON(t, &raw)
				for _, section := range []string{"book", "narrators", "tags", "genres", "series", "authors"} {
					if string(raw[section]) != "[]" {
						t.Fatalf("section %s: %s", section, raw[section])
					}
				}
				limited := search(t, e, token, bookLibrary, query("q", "the", "limit", "1"))
				if len(limited.Book) != 1 {
					t.Fatalf("limit 1 must cap books: %d", len(limited.Book))
				}
				unlimited := search(t, e, token, bookLibrary, query("q", "the", "limit", "0"))
				if len(unlimited.Book) != 2 {
					t.Fatalf("limit 0 means the default of 12: %d", len(unlimited.Book))
				}
			},
		},
		{
			Requirement: "route-r031", Invariant: "INV-ROUTE-R031-CONTRACT", Case: "error", State: "stateless", Assertion: "search-invalid-query-and-access",
			Sources: []string{srcSearch, srcMiddle},
			Claim:   "Search is 400 without q, with an empty q, with a repeated q or a negative limit, 401 without a token, 404 for an unknown library and 403 outside the user's libraries.",
			Run: func(t *testing.T, e *harness.Env) {
				root := e.Token(t, "root")
				path := libraryPath(e, bookLibrary, "/search")
				for _, values := range []url.Values{query(), query("q", ""), {"q": {"a", "b"}}, query("q", "the", "limit", "-1")} {
					expectStatus(t, "search "+values.Encode(), e.Do(t, harness.Request{Path: path, Token: root, Query: values}), http.StatusBadRequest)
				}
				expectStatus(t, "no token", e.Do(t, harness.Request{Path: path, Query: query("q", "the")}), http.StatusUnauthorized)
				expectStatus(t, "unknown", e.Do(t, harness.Request{Path: "/api/libraries/00000000-0000-4000-8000-000000000000/search", Token: root, Query: query("q", "the")}), http.StatusNotFound)
				expectStatus(t, "podonly", e.Do(t, harness.Request{Path: path, Token: s.restrictedToken(t, e, "browse-podonly"), Query: query("q", "the")}), http.StatusForbidden)
			},
		},
		{
			Requirement: "route-r031", Invariant: "INV-ROUTE-R031-PODCAST-001", Case: "normal", State: "stateless", Assertion: "search-podcast-sections",
			Sources: []string{srcPodSearch},
			Claim:   "Podcast search matches podcast title and author as podcasts, episode titles as episodes carrying the recent episode, and tags and genres with counts.",
			Run: func(t *testing.T, e *harness.Env) {
				s.enrich(t, e)
				token := e.Token(t, "listener")
				fixture := search(t, e, token, podcastLibrary, query("q", "fixture"))
				if hits := hitKeys(e, fixture.Podcast); !slices.Equal(hits, []string{"podcast"}) || len(fixture.Episodes) != 0 {
					t.Fatalf("fixture: %v %d", hits, len(fixture.Episodes))
				}
				if hits := hitKeys(e, search(t, e, token, podcastLibrary, query("q", "FIXTURE host")).Podcast); !slices.Equal(hits, []string{"podcast"}) {
					t.Fatalf("author: %v", hits)
				}
				third := search(t, e, token, podcastLibrary, query("q", "third"))
				if len(third.Podcast) != 0 || len(third.Episodes) != 1 || third.Episodes[0].LibraryItem.RecentEpisode == nil || third.Episodes[0].LibraryItem.RecentEpisode.ID != e.IDs["ep3"] || third.Episodes[0].LibraryItem.RecentEpisode.Title != "Third Time" {
					t.Fatalf("episode: %+v", third.Episodes)
				}
				tag := search(t, e, token, podcastLibrary, query("q", "pod-tag"))
				if len(tag.Tags) != 1 || tag.Tags[0].Name != "pod-tag" || *tag.Tags[0].NumItems != 1 {
					t.Fatalf("tag: %+v", tag.Tags)
				}
				genre := search(t, e, token, podcastLibrary, query("q", "technology"))
				if len(genre.Genres) != 1 || genre.Genres[0].Name != "Technology" {
					t.Fatalf("genre: %+v", genre.Genres)
				}
			},
		},
		{
			Requirement: "route-r031", Invariant: "INV-ROUTE-R031-RESTRICT-001", Case: "normal", State: "stateless", Assertion: "search-user-restrictions",
			Sources: []string{srcBookSearch, srcPodSearch, "server/utils/queries/libraryItemsBookFilters.js:15-37"},
			Claim:   "Search hides items the user cannot access: explicit books and podcasts for a user without explicit access, books without the allowed tag, and series left with no accessible book.",
			Run: func(t *testing.T, e *harness.Env) {
				s.enrich(t, e)
				s.createSecondPodcast(t, e)
				finisher := e.Token(t, "finisher")
				if hits := hitKeys(e, search(t, e, finisher, bookLibrary, query("q", "ebook")).Book); len(hits) != 0 {
					t.Fatalf("explicit book visible: %v", hits)
				}
				if hits := hitKeys(e, search(t, e, e.Token(t, "root"), bookLibrary, query("q", "ebook")).Book); !slices.Equal(hits, []string{"ebook"}) {
					t.Fatalf("root: %v", hits)
				}
				pods := search(t, e, finisher, podcastLibrary, query("q", "cast"))
				if len(pods.Podcast) != 1 || pods.Podcast[0].LibraryItem.Media.Metadata.Title != "Fixture Cast" {
					t.Fatalf("finisher podcasts: %+v", pods.Podcast)
				}
				tagged := s.restrictedToken(t, e, "browse-tagged")
				if hits := hitKeys(e, search(t, e, tagged, bookLibrary, query("q", "the")).Book); !slices.Equal(hits, []string{"saga1"}) {
					t.Fatalf("tagged the: %v", hits)
				}
				if names := search(t, e, tagged, bookLibrary, query("q", "third")).seriesNames(); len(names) != 0 {
					t.Fatalf("series without accessible books: %v", names)
				}
				if names := search(t, e, e.Token(t, "root"), bookLibrary, query("q", "third")).seriesNames(); !slices.Equal(names, []string{third}) {
					t.Fatalf("root third: %v", names)
				}
			},
		},
	}
}
