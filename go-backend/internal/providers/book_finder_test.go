package providers

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"audiobookshelf-compat/internal/store"
	"audiobookshelf-compat/internal/wire"
)

func TestCleanForCompares(t *testing.T) {
	for in, want := range map[string]string{
		"Ender's Game (Ender's Saga): A Novel": "enders game",
		"  Café   Society ":                    "cafe society",
		"Title - Subtitle":                     "title",
	} {
		if got := cleanTitleForCompares(in); got != want {
			t.Errorf("title %q = %q, want %q", in, got, want)
		}
	}
	for in, want := range map[string]string{
		"J.R.R. Tolkien":          "j. r.r. tolkien",
		"John Q. Smith":           "john smith",
		"Jane Doe et al.":         "jane doe",
		"Ursula K. Le Guin":       "ursula le guin",
		"Isaac Asimov":            "isaac asimov",
		"Al B. Cee Dee":           "al cee dee",
		"Robert A. Heinlein, Jr.": "robert heinlein, jr.",
	} {
		if got := cleanAuthorForCompares(in); got != want {
			t.Errorf("author %q = %q, want %q", in, got, want)
		}
	}
}

func TestTitleCandidatesOrdering(t *testing.T) {
	got := titleCandidates("", []string{"the hobbit: an unexpected journey", "12"})
	if len(got) != 2 || got[0] != "the hobbit" {
		t.Fatalf("candidates %v", got)
	}
	if got[len(got)-1] != "12" {
		t.Fatalf("digits-only candidate must come last: %v", got)
	}
}

func TestSearchBooksFuzzyFallbackAndCustomProvider(t *testing.T) {
	var queries []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/volumes":
			q := r.URL.Query().Get("q")
			queries = append(queries, q)
			if !strings.Contains(q, "intitle:the hobbit") || strings.Contains(q, "inauthor") {
				_, _ = w.Write([]byte(`{"items":[]}`))
				return
			}
			_, _ = w.Write([]byte(`{"items":[{"id":"1","volumeInfo":{"title":"The Hobbit","authors":["J. R. R. Tolkien"],"description":"<script>x</script><b>b</b>"}}]}`))
		case "/authors":
			_, _ = w.Write([]byte(`[]`))
		}
	}))
	defer server.Close()
	db := providersTestDB(t)
	h, err := New(db, nil)
	if err != nil {
		t.Fatal(err)
	}
	h.SetEndpoint("google", server.URL+"/volumes")
	h.SetEndpoint("audnexus", server.URL)
	got := h.SearchBooks(context.Background(), wire.BookSearch{Provider: "google", Title: "The Hobbit: An Unexpected Journey [mp3]", Author: "Tolkien", MaxFuzzySearches: 2})
	if len(got) != 1 || string(got[0].Title) != `"The Hobbit"` {
		t.Fatalf("results %+v (queries %v)", got, queries)
	}
	var description string
	_ = json.Unmarshal(got[0].Description, &description)
	if description != "<b>b</b>" || !strings.Contains(string(got[0].DescriptionPlain), `"b"`) {
		t.Fatalf("description %q plain %s", description, got[0].DescriptionPlain)
	}
	if len(queries) < 2 || queries[0] != "intitle:The Hobbit: An Unexpected Journey [mp3] inauthor:Tolkien" {
		t.Fatalf("first query is the verbatim search: %v", queries)
	}
	if none := h.SearchBooks(context.Background(), wire.BookSearch{Provider: "google", Title: ""}); len(none) != 0 {
		t.Fatalf("empty title %v", none)
	}
	if none := h.SearchBooks(context.Background(), wire.BookSearch{Provider: "google", Title: "zzz", MaxFuzzySearches: 0}); len(none) != 0 {
		t.Fatalf("no fuzzy: %v", none)
	}
}

func TestSearchPodcastRoute(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"results":[{"collectionId":7,"collectionName":"Cast","artistName":"Host","feedUrl":"http://x/feed","description":"<b>d</b>","trackExplicitness":"explicit","genres":["Tech"]}]}`))
	}))
	defer server.Close()
	h, err := New(providersTestDB(t), nil)
	if err != nil {
		t.Fatal(err)
	}
	h.SetEndpoint("itunes", server.URL)
	call := func(target string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		if !serve(h, w, httptest.NewRequest("GET", target, nil), store.User{}) {
			t.Fatalf("not routed: %s", target)
		}
		return w
	}
	if w := call("/api/search/podcast"); w.Code != 400 || !strings.Contains(w.Body.String(), `Query parameter \"term\" is required`) && !strings.Contains(w.Body.String(), `Query parameter "term" is required`) {
		t.Fatalf("missing term: %d %s", w.Code, w.Body)
	}
	if w := call("/api/search/podcast?term=a&term=b"); w.Code != 400 {
		t.Fatalf("array: %d", w.Code)
	}
	if w := call("/api/search/podcast?term="); w.Code != 200 || strings.TrimSpace(w.Body.String()) != "null" {
		t.Fatalf("empty term: %d %s", w.Code, w.Body)
	}
	w := call("/api/search/podcast?term=cast")
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"title":"Cast"`) || !strings.Contains(w.Body.String(), `"explicit":true`) {
		t.Fatalf("results: %d %s", w.Code, w.Body)
	}
	if match := h.SearchPodcasts(context.Background(), "cast"); len(match) != 1 || string(match[0].FeedURL) != `"http://x/feed"` {
		t.Fatalf("quick match podcast: %+v", match)
	}
	// The old plural path is not an upstream route.
	if serve(h, httptest.NewRecorder(), httptest.NewRequest("GET", "/api/search/podcasts?term=a", nil), store.User{}) {
		t.Fatal("/api/search/podcasts must not be routed")
	}
}
