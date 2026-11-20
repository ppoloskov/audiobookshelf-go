package metadata

import (
	"cmp"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"monstrobook/internal/db"
	"monstrobook/internal/httpx"
	"monstrobook/internal/ids"
)

// maxQueryRunes bounds the podcast and author search terms, as upstream does.
const maxQueryRunes = 1000

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
	results, err := h.searchBookOnce(r.Context(), q)
	if err != nil {
		slog.Warn("metadata: custom provider search failed", "provider", q.Provider, "error", err)
	}
	if results == nil {
		results = []BookResult{}
	}
	return httpx.JSON(w, r, results)
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

type providerView struct {
	ID              string          `json:"id"`
	Name            string          `json:"name"`
	MediaType       string          `json:"mediaType"`
	URL             string          `json:"url"`
	AuthHeaderValue *string         `json:"authHeaderValue"`
	ExtraData       json.RawMessage `json:"extraData"`
	CreatedAt       string          `json:"createdAt"`
	UpdatedAt       string          `json:"updatedAt"`
}

func viewOf(row db.CustomMetadataProvider) providerView {
	return providerView{
		ID: row.ID, Name: db.Val(row.Name), MediaType: db.Val(row.MediaType), URL: db.Val(row.URL),
		AuthHeaderValue: row.AuthHeaderValue, ExtraData: row.ExtraData.V,
		CreatedAt: row.CreatedAt.UTC().Format(time.RFC3339Nano), UpdatedAt: row.UpdatedAt.UTC().Format(time.RFC3339Nano),
	}
}

type providerEvent struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	MediaType string `json:"mediaType"`
	Slug      string `json:"slug"`
}

func (h *Handler) allCustom(ctx context.Context) ([]db.CustomMetadataProvider, error) {
	var rows []db.CustomMetadataProvider
	if err := h.d.Select(ctx, &rows, `SELECT * FROM customMetadataProviders ORDER BY createdAt ASC`); err != nil {
		return nil, fmt.Errorf("list custom providers: %w", err)
	}
	return rows, nil
}

// listCustom is GET /api/custom-metadata-providers (admin only).
func (h *Handler) listCustom(w http.ResponseWriter, r *http.Request) error {
	rows, err := h.allCustom(r.Context())
	if err != nil {
		return httpx.Internal("", err)
	}
	views := make([]providerView, len(rows))
	for i, row := range rows {
		views[i] = viewOf(row)
	}
	return httpx.JSON(w, r, struct {
		Providers []providerView `json:"providers"`
	}{views})
}

type createCustomRequest struct {
	Name            string          `json:"name"`
	URL             string          `json:"url"`
	MediaType       string          `json:"mediaType"`
	AuthHeaderValue string          `json:"authHeaderValue"`
	ExtraData       json.RawMessage `json:"extraData"`
}

func normalizeProviderURL(raw string) (string, error) {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme == "" || parsed.Opaque != "" {
		return "", httpx.BadRequest("Invalid url")
	}
	if parsed.Host != "" && parsed.Path == "" {
		parsed.Path = "/"
	}
	return parsed.String(), nil
}

// createCustom is POST /api/custom-metadata-providers (admin only).
func (h *Handler) createCustom(w http.ResponseWriter, r *http.Request) error {
	req, err := httpx.Decode[createCustomRequest](w, r)
	if err != nil {
		return err
	}
	if req.Name == "" || req.URL == "" || req.MediaType == "" {
		return httpx.BadRequest("Invalid request body")
	}
	providerURL, err := normalizeProviderURL(req.URL)
	if err != nil {
		return err
	}
	now := db.Now()
	row := db.CustomMetadataProvider{
		ID: ids.Must(), Name: &req.Name, MediaType: &req.MediaType, URL: &providerURL, CreatedAt: now, UpdatedAt: now,
	}
	if req.AuthHeaderValue != "" {
		row.AuthHeaderValue = &req.AuthHeaderValue
	}
	if len(req.ExtraData) > 0 && string(req.ExtraData) != "null" {
		row.ExtraData.V = req.ExtraData
	}
	if err := h.d.Insert(r.Context(), "customMetadataProviders", &row); err != nil {
		return httpx.Internal("", fmt.Errorf("insert custom provider: %w", err))
	}
	h.emit.ToAll("custom_metadata_provider_added", providerEvent{row.ID, req.Name, req.MediaType, customPrefix + row.ID})
	// extraData is left out of the answer unless one was sent (upstream's
	// created instance has no such attribute).
	var resp struct {
		Provider struct {
			providerView
			ExtraData json.RawMessage `json:"extraData,omitempty"`
		} `json:"provider"`
	}
	resp.Provider.providerView = viewOf(row)
	resp.Provider.ExtraData = row.ExtraData.V
	return httpx.JSON(w, r, resp)
}

// deleteCustom is DELETE /api/custom-metadata-providers/{id} (admin only).
// Libraries that used the provider fall back to Google Books (book) or iTunes.
func (h *Handler) deleteCustom(w http.ResponseWriter, r *http.Request) error {
	id := r.PathValue("id")
	var row db.CustomMetadataProvider
	err := h.d.Tx(r.Context(), func(ctx context.Context) error {
		if err := h.d.Get(ctx, &row, `SELECT * FROM customMetadataProviders WHERE id = ?`, id); err != nil {
			return err
		}
		if _, err := h.d.Exec(ctx, `DELETE FROM customMetadataProviders WHERE id = ?`, id); err != nil {
			return err
		}
		fallback := "itunes"
		if db.Val(row.MediaType) == "book" {
			fallback = "google"
		}
		_, err := h.d.Exec(ctx, `UPDATE libraries SET provider = ? WHERE provider = ?`, fallback, customPrefix+id)
		return err
	})
	if errors.Is(err, sql.ErrNoRows) {
		return httpx.NotFound()
	}
	if err != nil {
		return httpx.Internal("", fmt.Errorf("delete custom provider: %w", err))
	}
	h.emit.ToAll("custom_metadata_provider_removed", providerEvent{row.ID, db.Val(row.Name), db.Val(row.MediaType), customPrefix + id})
	return httpx.Text(w, http.StatusOK, "OK")
}

type providerOption struct {
	Value string `json:"value"`
	Text  string `json:"text"`
}

var bookProviderNames = []providerOption{
	{"google", "Google Books"}, {"itunes", "iTunes"}, {"openlibrary", "Open Library"}, {"fantlab", "FantLab.ru"},
	{"audiobookcovers", "AudiobookCovers.com"}, {"audible", "Audible.com"}, {"audible.ca", "Audible.ca"},
	{"audible.uk", "Audible.co.uk"}, {"audible.au", "Audible.com.au"}, {"audible.fr", "Audible.fr"},
	{"audible.de", "Audible.de"}, {"audible.jp", "Audible.co.jp"}, {"audible.it", "Audible.it"},
	{"audible.in", "Audible.in"}, {"audible.es", "Audible.es"},
}

// providers is GET /api/search/providers: the dropdown contents for book
// matching, cover search and podcast matching, custom providers included.
func (h *Handler) providers(w http.ResponseWriter, r *http.Request) error {
	rows, err := h.allCustom(r.Context())
	if err != nil {
		return httpx.Internal("", err)
	}
	var bookCustom, podcastCustom []providerOption
	for _, row := range rows {
		option := providerOption{customPrefix + row.ID, db.Val(row.Name)}
		switch db.Val(row.MediaType) {
		case "book":
			bookCustom = append(bookCustom, option)
		case "podcast":
			podcastCustom = append(podcastCustom, option)
		}
	}
	books := []providerOption{}
	covers := []providerOption{{"best", "Best"}}
	for _, option := range bookProviderNames {
		if option.Value != "audiobookcovers" { // covers only, not a book match source
			books = append(books, option)
		}
		covers = append(covers, option)
	}
	var resp struct {
		Providers struct {
			Books       []providerOption `json:"books"`
			BooksCovers []providerOption `json:"booksCovers"`
			Podcasts    []providerOption `json:"podcasts"`
		} `json:"providers"`
	}
	resp.Providers.Books = append(books, bookCustom...)
	resp.Providers.BooksCovers = append(append(covers, bookCustom...), providerOption{"all", "All"})
	resp.Providers.Podcasts = append([]providerOption{{"itunes", "iTunes"}}, podcastCustom...)
	return httpx.JSON(w, r, resp)
}
