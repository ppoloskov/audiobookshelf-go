package metadata

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"time"

	"audiobookshelf-compat/internal/db"
	"audiobookshelf-compat/internal/httpx"
	"audiobookshelf-compat/internal/ids"
)

// providerView is a custom provider as the settings page lists it. Times are
// RFC 3339 strings, as upstream's model sends them.
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

// providerEvent is the payload of custom_metadata_provider_added and _removed.
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

// normalizeProviderURL checks that raw has a scheme and gives a host a path.
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

// createCustom is POST /api/custom-metadata-providers (admin only). The
// provider's search is GET {url}/search; see searchCustom.
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
	view := viewOf(row)
	return httpx.JSON(w, r, struct {
		Provider struct {
			providerView
			ExtraData json.RawMessage `json:"extraData,omitempty"`
		} `json:"provider"`
	}{Provider: struct {
		providerView
		ExtraData json.RawMessage `json:"extraData,omitempty"`
	}{view, row.ExtraData.V}})
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

// providerOption is one entry of a provider dropdown.
type providerOption struct {
	Value string `json:"value"`
	Text  string `json:"text"`
}

// bookProviderNames are the built-in providers in dropdown order.
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
	books = append(books, bookCustom...)
	covers = append(covers, bookCustom...)
	covers = append(covers, providerOption{"all", "All"})
	podcasts := append([]providerOption{{"itunes", "iTunes"}}, podcastCustom...)
	return httpx.JSON(w, r, struct {
		Providers struct {
			Books       []providerOption `json:"books"`
			BooksCovers []providerOption `json:"booksCovers"`
			Podcasts    []providerOption `json:"podcasts"`
		} `json:"providers"`
	}{Providers: struct {
		Books       []providerOption `json:"books"`
		BooksCovers []providerOption `json:"booksCovers"`
		Podcasts    []providerOption `json:"podcasts"`
	}{books, covers, podcasts}})
}
