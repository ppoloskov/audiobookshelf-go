package providers

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"audiobookshelf-compat/internal/textclean"
)

func (h *Handler) searchCustom(ctx context.Context, slug, mediaType, title, author, isbn string, timeout time.Duration) ([]bookMatch, error) {
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	id := strings.TrimPrefix(slug, "custom-")
	if id == "" || id == slug {
		return nil, fmt.Errorf("providers: invalid custom provider slug")
	}
	var name, providerType, baseURL string
	var authValue sql.NullString
	if err := h.db.QueryRowContext(ctx, `SELECT name,mediaType,url,authHeaderValue FROM customMetadataProviders WHERE id=?`, id).Scan(&name, &providerType, &baseURL, &authValue); err != nil {
		return nil, fmt.Errorf("Custom provider not found for the given id")
	}
	query := url.Values{"mediaType": {mediaType}, "query": {title}}
	if author != "" {
		query.Set("author", author)
	}
	if isbn != "" {
		query.Set("isbn", isbn)
	}
	endpoint := strings.TrimRight(baseURL, "/") + "/search?" + query.Encode()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	request.Header.Set("Accept", "application/json")
	if authValue.Valid && authValue.String != "" {
		request.Header.Set("Authorization", authValue.String)
	}
	client := h.client
	if client == nil {
		client = &http.Client{Timeout: timeout}
	} else {
		copy := *client
		if copy.Timeout == 0 || copy.Timeout > timeout {
			copy.Timeout = timeout
		}
		client = &copy
	}
	response, err := client.Do(request)
	if err != nil {
		return []bookMatch{}, nil
	}
	defer response.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(response.Body, 8<<20))
	if err != nil {
		return nil, err
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return []bookMatch{}, nil
	}
	var envelope struct {
		Matches *[]matchCore `json:"matches"`
	}
	if json.Unmarshal(raw, &envelope) != nil || envelope.Matches == nil {
		return nil, fmt.Errorf("Custom provider returned malformed response")
	}
	results := make([]bookMatch, len(*envelope.Matches))
	for i, core := range *envelope.Matches {
		core.Description = text(textclean.Sanitize(string(core.Description)))
		results[i] = bookMatch{matchCore: core}
	}
	return results, nil
}
