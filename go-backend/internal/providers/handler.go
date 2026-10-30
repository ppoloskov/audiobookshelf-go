// Package providers implements the pinned source search-provider catalog,
// custom provider administration, and the upstream custom-provider adapter.
package providers

import (
	"audiobookshelf-compat/internal/dbtime"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"path"
	"sort"
	"strings"
	"sync"
	"time"

	"audiobookshelf-compat/internal/store"
	"github.com/google/uuid"
)

type EmitFunc func(userID, event string, payload any)

type Handler struct {
	db         *sql.DB
	client     *http.Client
	emit       EmitFunc
	endpointMu sync.RWMutex
	endpoints  map[string]string
}

func New(db *sql.DB, emit EmitFunc) (*Handler, error) {
	if db == nil {
		return nil, errors.New("providers: database is nil")
	}
	return &Handler{db: db, client: &http.Client{Timeout: 10 * time.Second}, emit: emit, endpoints: map[string]string{"google": "https://www.googleapis.com/books/v1/volumes", "openlibrary": "https://openlibrary.org", "itunes": "https://itunes.apple.com/search", "fantlab": "https://api.fantlab.ru", "audible": "https://api.audible.com/1.0/catalog/products", "audnexus": "https://api.audnex.us", "audiobookcovers": "https://api.audiobookcovers.com/cover/bytext/"}}, nil
}

// SetEndpoint overrides one public upstream base URL. It supports isolated
// tests and deployments that proxy public metadata services.
func (h *Handler) SetEndpoint(name, baseURL string) {
	h.endpointMu.Lock()
	h.endpoints[name] = strings.TrimRight(baseURL, "/")
	h.endpointMu.Unlock()
}
func (h *Handler) endpoint(name string) string {
	h.endpointMu.RLock()
	defer h.endpointMu.RUnlock()
	return h.endpoints[name]
}

func (h *Handler) Handle(w http.ResponseWriter, r *http.Request, user store.User) bool {
	if !strings.HasPrefix(path.Clean(r.URL.Path), "/api/") {
		return false
	}
	p := path.Clean(r.URL.Path)
	if p == "/api/search/providers" && r.Method == http.MethodGet {
		h.getProviders(w, r)
		return true
	}
	if p == "/api/search/books" && r.Method == http.MethodGet {
		h.searchBooks(w, r)
		return true
	}
	if p == "/api/search/covers" && r.Method == http.MethodGet {
		h.searchCovers(w, r)
		return true
	}
	if p == "/api/search/podcasts" && r.Method == http.MethodGet {
		h.searchPodcasts(w, r)
		return true
	}
	if p == "/api/search/authors" && r.Method == http.MethodGet {
		h.searchAuthor(w, r)
		return true
	}
	if p == "/api/search/chapters" && r.Method == http.MethodGet {
		h.searchChapters(w, r)
		return true
	}
	if p == "/api/custom-metadata-providers" {
		if !isAdmin(user) {
			sendStatus(w, http.StatusForbidden)
			return true
		}
		switch r.Method {
		case http.MethodGet:
			h.getCustomProviders(w, r)
		case http.MethodPost:
			h.createCustomProvider(w, r)
		default:
			return false
		}
		return true
	}
	if strings.HasPrefix(p, "/api/custom-metadata-providers/") && r.Method == http.MethodDelete {
		if !isAdmin(user) {
			sendStatus(w, http.StatusForbidden)
			return true
		}
		id, err := url.PathUnescape(strings.TrimPrefix(p, "/api/custom-metadata-providers/"))
		if err != nil || id == "" || strings.Contains(id, "/") {
			sendStatus(w, http.StatusNotFound)
			return true
		}
		h.deleteCustomProvider(w, r, id)
		return true
	}
	return false
}

func isAdmin(user store.User) bool { return user.Type == "admin" || user.Type == "root" }

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

func (h *Handler) getCustomProviders(w http.ResponseWriter, r *http.Request) {
	rows, err := h.db.QueryContext(r.Context(), `SELECT id,name,mediaType,url,authHeaderValue,extraData,createdAt,updatedAt FROM customMetadataProviders ORDER BY createdAt ASC`)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	defer rows.Close()
	providers := []customProvider{}
	for rows.Next() {
		var p customProvider
		var authValue sql.NullString
		var extra []byte
		if err := rows.Scan(&p.ID, &p.Name, &p.MediaType, &p.URL, &authValue, &extra, &p.CreatedAt, &p.UpdatedAt); err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		if authValue.Valid {
			p.AuthHeaderValue = &authValue.String
		}
		if len(extra) > 0 {
			p.ExtraData = append(json.RawMessage(nil), extra...)
		}
		p.CreatedAt = isoDate(p.CreatedAt)
		p.UpdatedAt = isoDate(p.UpdatedAt)
		providers = append(providers, p)
	}
	if err := rows.Err(); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"providers": providers})
}

func (h *Handler) createCustomProvider(w http.ResponseWriter, r *http.Request) {
	var request map[string]any
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&request); err != nil {
		sendText(w, http.StatusBadRequest, "Invalid request body")
		return
	}
	name, _ := request["name"].(string)
	rawURL, _ := request["url"].(string)
	mediaType, _ := request["mediaType"].(string)
	if name == "" || rawURL == "" || mediaType == "" {
		sendText(w, http.StatusBadRequest, "Invalid request body")
		return
	}
	parsed, err := url.Parse(rawURL)
	if err != nil || parsed.Scheme == "" || parsed.Opaque != "" {
		sendText(w, http.StatusBadRequest, "Invalid url")
		return
	}
	if parsed.Host != "" && parsed.Path == "" {
		parsed.Path = "/"
	}
	normalized := parsed.String()
	var authValue any
	if value, ok := request["authHeaderValue"].(string); ok && value != "" {
		authValue = value
	}
	id := uuid.NewString()
	now := dbNow()
	extra := request["extraData"]
	var extraValue any
	if extra != nil {
		encoded, err := json.Marshal(extra)
		if err != nil {
			sendText(w, http.StatusBadRequest, "Invalid request body")
			return
		}
		extraValue = string(encoded)
	}
	if _, err := h.db.ExecContext(r.Context(), `INSERT INTO customMetadataProviders(id,name,mediaType,url,authHeaderValue,extraData,createdAt,updatedAt) VALUES(?,?,?,?,?,?,?,?)`, id, name, mediaType, normalized, authValue, extraValue, now, now); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	p := customProvider{ID: id, Name: name, MediaType: mediaType, URL: normalized, CreatedAt: isoDate(now), UpdatedAt: isoDate(now)}
	if authValue != nil {
		value := authValue.(string)
		p.AuthHeaderValue = &value
	}
	if extraValue != nil {
		p.ExtraData = []byte(extraValue.(string))
	}
	if h.emit != nil {
		h.emit("*", "custom_metadata_provider_added", map[string]any{"id": id, "name": name, "mediaType": mediaType, "slug": "custom-" + id})
	}
	writeJSON(w, http.StatusOK, map[string]any{"provider": p})
}

func (h *Handler) deleteCustomProvider(w http.ResponseWriter, r *http.Request, id string) {
	var p customProvider
	var authValue sql.NullString
	var extra []byte
	err := h.db.QueryRowContext(r.Context(), `SELECT id,name,mediaType,url,authHeaderValue,extraData,createdAt,updatedAt FROM customMetadataProviders WHERE id=?`, id).Scan(&p.ID, &p.Name, &p.MediaType, &p.URL, &authValue, &extra, &p.CreatedAt, &p.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		sendStatus(w, http.StatusNotFound)
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	if authValue.Valid {
		p.AuthHeaderValue = &authValue.String
	}
	p.ExtraData = extra
	tx, err := h.db.BeginTx(r.Context(), nil)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(r.Context(), `DELETE FROM customMetadataProviders WHERE id=?`, id); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	fallback := "itunes"
	if p.MediaType == "book" {
		fallback = "google"
	}
	if _, err = tx.ExecContext(r.Context(), `UPDATE libraries SET provider=? WHERE provider=?`, fallback, "custom-"+id); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	if err = tx.Commit(); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	if h.emit != nil {
		h.emit("*", "custom_metadata_provider_removed", map[string]any{"id": id, "name": p.Name, "mediaType": p.MediaType, "slug": "custom-" + id})
	}
	sendStatus(w, http.StatusOK)
}

var bookProviderNames = map[string]string{"all": "All", "best": "Best", "google": "Google Books", "itunes": "iTunes", "openlibrary": "Open Library", "fantlab": "FantLab.ru", "audiobookcovers": "AudiobookCovers.com", "audible": "Audible.com", "audible.ca": "Audible.ca", "audible.uk": "Audible.co.uk", "audible.au": "Audible.com.au", "audible.fr": "Audible.fr", "audible.de": "Audible.de", "audible.jp": "Audible.co.jp", "audible.it": "Audible.it", "audible.in": "Audible.in", "audible.es": "Audible.es", "audnexus": "Audnexus"}
var bookProviderValues = []string{"google", "itunes", "openlibrary", "fantlab", "audiobookcovers", "audible", "audible.ca", "audible.uk", "audible.au", "audible.fr", "audible.de", "audible.jp", "audible.it", "audible.in", "audible.es"}

func (h *Handler) getProviders(w http.ResponseWriter, r *http.Request) {
	custom, err := h.customProviders(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
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
	writeJSON(w, http.StatusOK, map[string]any{"providers": map[string]any{"books": books, "booksCovers": covers, "podcasts": podcasts}})
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
		p.ExtraData = extra
		out = append(out, p)
	}
	return out, rows.Err()
}

func isoDate(value string) string {
	for _, layout := range []string{"2006-01-02 15:04:05.000 -07:00", time.RFC3339Nano, "2006-01-02 15:04:05 -07:00"} {
		if parsed, err := time.Parse(layout, value); err == nil {
			return parsed.UTC().Format(time.RFC3339Nano)
		}
	}
	return value
}
func dbNow() string { return dbtime.Now() }
func sendStatus(w http.ResponseWriter, status int) {
	if status == http.StatusOK {
		sendText(w, status, "OK")
		return
	}
	sendText(w, status, http.StatusText(status))
}
func sendText(w http.ResponseWriter, status int, body string) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(status)
	_, _ = io.WriteString(w, body)
}
func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}
func writeError(w http.ResponseWriter, status int, err error) {
	writeJSON(w, status, map[string]string{"error": err.Error()})
}

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
