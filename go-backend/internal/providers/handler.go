// Package providers implements the pinned source search-provider catalog,
// custom provider administration, and the upstream custom-provider adapter.
package providers

import (
	"audiobookshelf-compat/internal/access"
	"audiobookshelf-compat/internal/dbtime"
	"audiobookshelf-compat/internal/events"
	"audiobookshelf-compat/internal/httputil"
	"audiobookshelf-compat/internal/ids"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"
)

type Handler struct {
	db            *sql.DB
	client        *http.Client
	emit          events.Emitter
	endpointMu    sync.RWMutex
	endpoints     map[string]string
	coverMu       sync.Mutex
	coverSearches map[string]context.CancelFunc
}

func New(db *sql.DB, emit events.Emitter) (*Handler, error) {
	if db == nil {
		return nil, errors.New("providers: database is nil")
	}
	return &Handler{db: db, client: &http.Client{Timeout: 10 * time.Second}, emit: emit, coverSearches: map[string]context.CancelFunc{}, endpoints: map[string]string{"google": "https://www.googleapis.com/books/v1/volumes", "openlibrary": "https://openlibrary.org", "itunes": "https://itunes.apple.com/search", "fantlab": "https://api.fantlab.ru", "audible": "https://api.audible.com/1.0/catalog/products", "audnexus": "https://api.audnex.us", "audiobookcovers": "https://api.audiobookcovers.com/cover/bytext/"}}, nil
}

func (h *Handler) endpoint(name string) string {
	h.endpointMu.RLock()
	defer h.endpointMu.RUnlock()
	return h.endpoints[name]
}

// Routes registers the search and custom-provider routes on mux. The server
// attaches the user to the request context before dispatch
// (store.ContextWithUser). The custom-provider routes answer 403 to a user who
// is not an admin.
func (h *Handler) Routes(mux *http.ServeMux) {
	admin := func(fn httputil.Handler) httputil.Handler { return access.RequireAdmin(http.StatusForbidden, fn) }
	mux.Handle("GET /api/search/providers", httputil.Handler(h.getProviders))
	mux.Handle("GET /api/search/books", httputil.Handler(h.searchBooks))
	mux.Handle("GET /api/search/covers", httputil.Handler(h.searchCovers))
	mux.Handle("GET /api/search/podcast", httputil.Handler(h.searchPodcasts))
	mux.Handle("GET /api/search/authors", httputil.Handler(h.searchAuthor))
	mux.Handle("GET /api/search/chapters", httputil.Handler(h.searchChapters))
	mux.Handle("GET /api/custom-metadata-providers", admin(h.getCustomProviders))
	mux.Handle("POST /api/custom-metadata-providers", admin(h.createCustomProvider))
	mux.Handle("DELETE /api/custom-metadata-providers/{id}", admin(h.deleteCustomProviderRoute))
}

// deleteCustomProviderRoute answers 404 to an id with a slash (an escaped %2F):
// the server never issues such an id.
func (h *Handler) deleteCustomProviderRoute(w http.ResponseWriter, r *http.Request) error {
	id := r.PathValue("id")
	if strings.Contains(id, "/") {
		return httputil.Status(http.StatusNotFound, "")
	}
	return h.deleteCustomProvider(w, r, id)
}

type customProvider struct {
	ID              string          `json:"id"`
	Name            string          `json:"name"`
	MediaType       string          `json:"mediaType"`
	URL             string          `json:"url"`
	AuthHeaderValue *string         `json:"authHeaderValue"`
	ExtraData       json.RawMessage `json:"extraData"`
	CreatedAt       string          `json:"createdAt"`
	UpdatedAt       string          `json:"updatedAt"`
}

func (h *Handler) getCustomProviders(w http.ResponseWriter, r *http.Request) error {
	providers, err := h.customProviders(r.Context())
	if err != nil {
		return &httputil.Error{Status: 500, Cause: err}
	}
	if providers == nil {
		providers = []customProvider{}
	}
	for i := range providers {
		providers[i].CreatedAt = isoDate(providers[i].CreatedAt)
		providers[i].UpdatedAt = isoDate(providers[i].UpdatedAt)
	}
	return httputil.JSON(w, r, map[string]any{"providers": providers})
}

func (h *Handler) createCustomProvider(w http.ResponseWriter, r *http.Request) error {
	var request map[string]any
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&request); err != nil {
		return httputil.Status(400, "Invalid request body")
	}
	name, _ := request["name"].(string)
	rawURL, _ := request["url"].(string)
	mediaType, _ := request["mediaType"].(string)
	if name == "" || rawURL == "" || mediaType == "" {
		return httputil.Status(400, "Invalid request body")
	}
	parsed, err := url.Parse(rawURL)
	if err != nil || parsed.Scheme == "" || parsed.Opaque != "" {
		return httputil.Status(400, "Invalid url")
	}
	if parsed.Host != "" && parsed.Path == "" {
		parsed.Path = "/"
	}
	normalized := parsed.String()
	var authValue any
	if value, ok := request["authHeaderValue"].(string); ok && value != "" {
		authValue = value
	}
	id := ids.Must()
	now := dbNow()
	extra := request["extraData"]
	var extraValue any
	if extra != nil {
		encoded, err := json.Marshal(extra)
		if err != nil {
			return httputil.Status(400, "Invalid request body")
		}
		extraValue = string(encoded)
	}
	if _, err := h.db.ExecContext(r.Context(), `INSERT INTO customMetadataProviders(id,name,mediaType,url,authHeaderValue,extraData,createdAt,updatedAt) VALUES(?,?,?,?,?,?,?,?)`, id, name, mediaType, normalized, authValue, extraValue, now, now); err != nil {
		return &httputil.Error{Status: 500, Cause: err}
	}
	p := customProvider{ID: id, Name: name, MediaType: mediaType, URL: normalized, CreatedAt: isoDate(now), UpdatedAt: isoDate(now)}
	if authValue != nil {
		value := authValue.(string)
		p.AuthHeaderValue = &value
	}
	if extraValue != nil {
		p.ExtraData = []byte(extraValue.(string))
	}
	h.emit.ToAll("custom_metadata_provider_added", map[string]any{"id": id, "name": name, "mediaType": mediaType, "slug": "custom-" + id})
	// The created instance has no extraData attribute unless one was sent, so
	// upstream's response omits it (the list reads the column, which is null).
	created := struct {
		customProvider
		ExtraData json.RawMessage `json:"extraData,omitempty"`
	}{customProvider: p, ExtraData: p.ExtraData}
	return httputil.JSON(w, r, map[string]any{"provider": created})
}

func (h *Handler) deleteCustomProvider(w http.ResponseWriter, r *http.Request, id string) error {
	var p customProvider
	var authValue sql.NullString
	var extra []byte
	err := h.db.QueryRowContext(r.Context(), `SELECT id,name,mediaType,url,authHeaderValue,extraData,createdAt,updatedAt FROM customMetadataProviders WHERE id=?`, id).Scan(&p.ID, &p.Name, &p.MediaType, &p.URL, &authValue, &extra, &p.CreatedAt, &p.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return httputil.Status(http.StatusNotFound, "")
	}
	if err != nil {
		return &httputil.Error{Status: 500, Cause: err}
	}
	if authValue.Valid {
		p.AuthHeaderValue = &authValue.String
	}
	p.ExtraData = extra
	tx, err := h.db.BeginTx(r.Context(), nil)
	if err != nil {
		return &httputil.Error{Status: 500, Cause: err}
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(r.Context(), `DELETE FROM customMetadataProviders WHERE id=?`, id); err != nil {
		return &httputil.Error{Status: 500, Cause: err}
	}
	fallback := "itunes"
	if p.MediaType == "book" {
		fallback = "google"
	}
	if _, err = tx.ExecContext(r.Context(), `UPDATE libraries SET provider=? WHERE provider=?`, fallback, "custom-"+id); err != nil {
		return &httputil.Error{Status: 500, Cause: err}
	}
	if err = tx.Commit(); err != nil {
		return &httputil.Error{Status: 500, Cause: err}
	}
	h.emit.ToAll("custom_metadata_provider_removed", map[string]any{"id": id, "name": p.Name, "mediaType": p.MediaType, "slug": "custom-" + id})
	return httputil.OK(w, r)
}

var bookProviderNames = map[string]string{"all": "All", "best": "Best", "google": "Google Books", "itunes": "iTunes", "openlibrary": "Open Library", "fantlab": "FantLab.ru", "audiobookcovers": "AudiobookCovers.com", "audible": "Audible.com", "audible.ca": "Audible.ca", "audible.uk": "Audible.co.uk", "audible.au": "Audible.com.au", "audible.fr": "Audible.fr", "audible.de": "Audible.de", "audible.jp": "Audible.co.jp", "audible.it": "Audible.it", "audible.in": "Audible.in", "audible.es": "Audible.es", "audnexus": "Audnexus"}
var bookProviderValues = []string{"google", "itunes", "openlibrary", "fantlab", "audiobookcovers", "audible", "audible.ca", "audible.uk", "audible.au", "audible.fr", "audible.de", "audible.jp", "audible.it", "audible.in", "audible.es"}

func (h *Handler) getProviders(w http.ResponseWriter, r *http.Request) error {
	custom, err := h.customProviders(r.Context())
	if err != nil {
		return &httputil.Error{Status: 500, Cause: err}
	}
	bookCustom, podcastCustom := []map[string]string{}, []map[string]string{}
	for _, p := range custom {
		item := map[string]string{"value": "custom-" + p.ID, "text": p.Name}
		if p.MediaType == "book" {
			bookCustom = append(bookCustom, item)
		} else if p.MediaType == "podcast" {
			podcastCustom = append(podcastCustom, item)
		}
	}
	books := []map[string]string{}
	for _, provider := range bookProviderValues {
		if provider != "audiobookcovers" {
			books = append(books, providerObject(provider))
		}
	}
	books = append(books, bookCustom...)
	covers := []map[string]string{providerObject("best")}
	for _, provider := range bookProviderValues {
		covers = append(covers, providerObject(provider))
	}
	covers = append(covers, bookCustom...)
	covers = append(covers, providerObject("all"))
	podcasts := []map[string]string{providerObject("itunes")}
	podcasts = append(podcasts, podcastCustom...)
	return httputil.JSON(w, r, map[string]any{"providers": map[string]any{"books": books, "booksCovers": covers, "podcasts": podcasts}})
}

func providerObject(value string) map[string]string {
	name := bookProviderNames[value]
	if name == "" {
		name = value
	}
	return map[string]string{"value": value, "text": name}
}

func (h *Handler) customProviders(ctx context.Context) ([]customProvider, error) {
	rows, err := h.db.QueryContext(ctx, `SELECT id,name,mediaType,url,authHeaderValue,extraData,createdAt,updatedAt FROM customMetadataProviders ORDER BY createdAt ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []customProvider
	for rows.Next() {
		var p customProvider
		var authValue sql.NullString
		var extra []byte
		if err := rows.Scan(&p.ID, &p.Name, &p.MediaType, &p.URL, &authValue, &extra, &p.CreatedAt, &p.UpdatedAt); err != nil {
			return nil, err
		}
		if authValue.Valid {
			p.AuthHeaderValue = &authValue.String
		}
		if len(extra) > 0 {
			p.ExtraData = append(json.RawMessage(nil), extra...)
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func isoDate(value string) string {
	if parsed, ok := dbtime.Parse(value); ok {
		return parsed.UTC().Format(time.RFC3339Nano)
	}
	return value
}
func dbNow() string { return dbtime.Now() }

func queryValue(r *http.Request, key, fallback string) string {
	if value, ok := r.URL.Query()[key]; ok && len(value) > 0 {
		return value[0]
	}
	return fallback
}
func uniqueStrings(values []string) []string {
	seen := map[string]bool{}
	out := []string{}
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" && !seen[value] {
			seen[value] = true
			out = append(out, value)
		}
	}
	return out
}
func sortedKeys[K ~string, V any](values map[K]V) []K {
	keys := make([]K, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool { return keys[i] < keys[j] })
	return keys
}
