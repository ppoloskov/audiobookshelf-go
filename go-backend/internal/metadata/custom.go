package metadata

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"audiobookshelf-compat/internal/db"
)

// customPrefix starts the provider value of a custom provider: "custom-<id>".
const customPrefix = "custom-"

// customBook is a book as a custom provider sends it. Its fields decode
// leniently (numbers as text, comma joined lists, {value:} objects) because
// custom providers are written by users; a field of another shape is left out.
type customBook struct {
	Title         looseText   `json:"title"`
	Subtitle      looseText   `json:"subtitle"`
	Author        looseText   `json:"author"`
	Narrator      looseText   `json:"narrator"`
	Publisher     looseText   `json:"publisher"`
	PublishedYear looseText   `json:"publishedYear"`
	Description   looseText   `json:"description"`
	Cover         looseText   `json:"cover"`
	ISBN          looseText   `json:"isbn"`
	ASIN          looseText   `json:"asin"`
	Genres        looseList   `json:"genres"`
	Tags          looseList   `json:"tags"`
	Series        looseSeries `json:"series"`
	Language      looseText   `json:"language"`
	Duration      looseNumber `json:"duration"`
}

func (c customBook) result() BookResult {
	book := BookResult{
		Title: string(c.Title), Subtitle: string(c.Subtitle), Author: string(c.Author), Narrator: string(c.Narrator),
		Publisher: string(c.Publisher), PublishedYear: string(c.PublishedYear), Description: string(c.Description),
		Cover: string(c.Cover), ISBN: string(c.ISBN), ASIN: string(c.ASIN), Genres: c.Genres, Tags: c.Tags,
		Series: c.Series, Language: string(c.Language), Duration: float64(c.Duration),
	}
	book.sanitize()
	return book
}

// isCustom reports whether provider names a custom provider.
func isCustom(provider string) bool { return strings.HasPrefix(provider, customPrefix) }

var errCustomNotFound = errors.New("Custom provider not found for the given id")

// searchCustom asks the custom provider named by provider (GET {url}/search
// with mediaType, query, author and isbn). An unreachable provider or a
// non-2xx answer is an empty result; a body without a matches array is an
// error. The URL is admin-set, so the request goes through the SSRF-guarded
// client.
func (h *Handler) searchCustom(ctx context.Context, provider, mediaType string, q BookQuery) ([]BookResult, error) {
	var row db.CustomMetadataProvider
	err := h.d.Get(ctx, &row, `SELECT * FROM customMetadataProviders WHERE id = ?`, strings.TrimPrefix(provider, customPrefix))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, errCustomNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("load custom provider: %w", err)
	}
	query := url.Values{"mediaType": {mediaType}, "query": {q.Title}}
	if q.Author != "" {
		query.Set("author", q.Author)
	}
	if q.ISBN != "" {
		query.Set("isbn", q.ISBN)
	}
	header := http.Header{}
	if auth := db.Val(row.AuthHeaderValue); auth != "" {
		header.Set("Authorization", auth) // verbatim, as the admin typed it
	}
	body, err := h.custom.body(ctx, strings.TrimRight(db.Val(row.URL), "/")+"/search?"+query.Encode(), header)
	if err != nil {
		return nil, nil // unreachable or failing: no matches
	}
	var envelope struct {
		Matches *[]customBook `json:"matches"`
	}
	if json.Unmarshal(body, &envelope) != nil || envelope.Matches == nil {
		return nil, errors.New("Custom provider returned malformed response")
	}
	results := make([]BookResult, len(*envelope.Matches))
	for i, match := range *envelope.Matches {
		results[i] = match.result()
	}
	return results, nil
}
