package metadata

import (
	"cmp"
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strings"

	"audiobookshelf-compat/internal/httpx"
)

// maxQueryRunes bounds the podcast search terms, as upstream does.
const maxQueryRunes = 1000

// errorBody is the JSON error the search routes send.
type errorBody struct {
	Error     string `json:"error"`
	StringKey string `json:"stringKey,omitempty"`
}

func jsonError(w http.ResponseWriter, r *http.Request, status int, message string) error {
	return httpx.JSONStatus(w, r, status, errorBody{Error: message})
}

// searchBooksRoute is GET /api/search/books: one provider, no retries. A
// failing provider answers an empty list.
func (h *Handler) searchBooksRoute(w http.ResponseWriter, r *http.Request) error {
	query := r.URL.Query()
	// The match modal names the item it matches; an unknown item is a 404
	// before any provider is asked.
	if id := query.Get("id"); id != "" {
		var found int
		if err := h.d.Get(r.Context(), &found, `SELECT COUNT(*) FROM libraryItems WHERE id = ?`, id); err != nil {
			return httpx.Internal("", fmt.Errorf("find library item: %w", err))
		}
		if found == 0 {
			return jsonError(w, r, http.StatusNotFound, fmt.Sprintf("library item %q not found", id))
		}
	}
	q := BookQuery{
		Provider: cmp.Or(query.Get("provider"), "google"), Title: query.Get("title"), Author: query.Get("author"),
		ISBN: query.Get("isbn"), ASIN: query.Get("asin"), Region: query.Get("region"),
	}
	var results []BookResult
	switch {
	case isCustom(q.Provider):
		var err error
		if results, err = h.searchCustom(r.Context(), q.Provider, "book", q); err != nil {
			slog.Warn("metadata: custom provider search failed", "provider", q.Provider, "error", err)
		}
	case q.Title != "":
		results = h.searchBuiltin(r.Context(), q)
	}
	if results == nil {
		results = []BookResult{}
	}
	return httpx.JSON(w, r, results)
}

// searchBuiltin runs a built-in provider and cleans the descriptions.
func (h *Handler) searchBuiltin(ctx context.Context, q BookQuery) []BookResult {
	results, _ := h.svc.bookProvider(q.Provider).searchBooks(ctx, q) // built-ins answer failure as no results
	for i := range results {
		results[i].sanitize()
	}
	return results
}

// searchCoversRoute is GET /api/search/covers: the covers of every provider
// the request names, distinct, in provider order.
func (h *Handler) searchCoversRoute(w http.ResponseWriter, r *http.Request) error {
	query := r.URL.Query()
	if !query.Has("title") {
		return jsonError(w, r, http.StatusBadRequest, "Missing required query parameter: title")
	}
	type coversBody struct {
		Results []string `json:"results"`
	}
	req := CoverRequest{Title: query.Get("title"), Author: query.Get("author"), Provider: cmp.Or(query.Get("provider"), "google"), Podcast: query.Get("podcast") == "1"}
	if req.Title == "" {
		return httpx.JSON(w, r, coversBody{})
	}
	var covers []string
	for _, provider := range coverProviders(req) {
		list, err := h.providerCovers(r.Context(), provider, req)
		if err != nil {
			continue // the socket search reports it; here a provider that fails adds nothing
		}
		covers = append(covers, list...)
	}
	return httpx.JSON(w, r, coversBody{Results: append([]string{}, uniqueTrimmed(covers)...)})
}

// queryParam is one single-valued query parameter: absent is fallback (an
// error when required), and an array or an overlong value is an error.
func queryParam(query url.Values, name, fallback string, required bool) (string, error) {
	values, present := query[name]
	switch {
	case !present && required:
		return "", fmt.Errorf("Query parameter %q is required", name)
	case !present:
		return fallback, nil
	case len(values) > 1:
		return "", fmt.Errorf("Query parameter %q is an array", name)
	case len([]rune(values[0])) > maxQueryRunes:
		return "", fmt.Errorf("Query parameter %q is too long", name)
	}
	return values[0], nil
}

// searchPodcastsRoute is GET /api/search/podcast over iTunes.
func (h *Handler) searchPodcastsRoute(w http.ResponseWriter, r *http.Request) error {
	term, err := queryParam(r.URL.Query(), "term", "", true)
	if err != nil {
		return jsonError(w, r, http.StatusBadRequest, err.Error())
	}
	country, err := queryParam(r.URL.Query(), "country", "us", false)
	if err != nil {
		return jsonError(w, r, http.StatusBadRequest, err.Error())
	}
	if term == "" {
		return httpx.JSON(w, r, nil)
	}
	results, err := h.svc.itunes.searchPodcasts(r.Context(), term, country)
	if err != nil {
		results = []PodcastResult{} // a failing provider is an empty answer
	}
	return httpx.JSON(w, r, results)
}

// searchAuthorsRoute is GET /api/search/authors?q=: the Audnexus author
// closest to the name, or null.
func (h *Handler) searchAuthorsRoute(w http.ResponseWriter, r *http.Request) error {
	name, err := queryParam(r.URL.Query(), "q", "", true)
	if err != nil {
		return jsonError(w, r, http.StatusBadRequest, err.Error())
	}
	if name == "" {
		return httpx.JSON(w, r, nil)
	}
	return httpx.JSON(w, r, h.FindAuthorByName(r.Context(), name, r.URL.Query().Get("region")))
}

// searchChaptersRoute is GET /api/search/chapters?asin=&region=. The web
// client reads the error key of a 200 answer, so failures are 200s.
func (h *Handler) searchChaptersRoute(w http.ResponseWriter, r *http.Request) error {
	query := r.URL.Query()
	asin := strings.ToUpper(query.Get("asin"))
	if !validASIN(asin) {
		return httpx.JSON(w, r, errorBody{Error: "Invalid ASIN", StringKey: "MessageInvalidAsin"})
	}
	chapters, err := h.svc.audnexus.chapters(r.Context(), asin, strings.ToLower(cmp.Or(query.Get("region"), "us")))
	if err != nil {
		return httpx.JSON(w, r, errorBody{Error: "Chapters not found", StringKey: "MessageChaptersNotFound"})
	}
	return httpx.JSON(w, r, chapters)
}
