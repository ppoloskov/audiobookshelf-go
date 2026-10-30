package providers

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"audiobookshelf-compat/internal/auth"
)

func (h *Handler) searchCustom(ctx context.Context, slug, mediaType, title, author, isbn string, timeout time.Duration) ([]map[string]any, error) {
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
		return []map[string]any{}, nil
	}
	defer response.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(response.Body, 8<<20))
	if err != nil {
		return nil, err
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return []map[string]any{}, nil
	}
	var envelope map[string]json.RawMessage
	if json.Unmarshal(raw, &envelope) != nil {
		return nil, fmt.Errorf("Custom provider returned malformed response")
	}
	matchesRaw, ok := envelope["matches"]
	if !ok {
		return nil, fmt.Errorf("Custom provider returned malformed response")
	}
	var matches []map[string]any
	if json.Unmarshal(matchesRaw, &matches) != nil || matches == nil {
		return nil, fmt.Errorf("Custom provider returned malformed response")
	}
	results := make([]map[string]any, 0, len(matches))
	for _, match := range matches {
		results = append(results, normalizeCustomMatch(match))
	}
	return results, nil
}

func normalizeCustomMatch(match map[string]any) map[string]any {
	fields := []string{"title", "subtitle", "author", "narrator", "publisher", "publishedYear", "description", "cover", "isbn", "asin", "genres", "tags", "series", "language", "duration"}
	out := map[string]any{}
	for _, key := range fields {
		raw, ok := match[key]
		if !ok || raw == nil {
			continue
		}
		switch key {
		case "description":
			if text, ok := raw.(string); ok {
				out[key] = auth.SanitizeHTML(text)
			}
		case "genres", "tags":
			if cleaned, ok := normalizeTags(raw); ok {
				out[key] = cleaned
			}
		case "series":
			if cleaned := normalizeSeries(raw); len(cleaned) > 0 {
				out[key] = cleaned
			}
		case "duration":
			if number, ok := numberValue(raw); ok && !math.IsNaN(number) {
				out[key] = number
			}
		default:
			if value, ok := stringValue(raw); ok {
				out[key] = value
			}
		}
	}
	return out
}

func stringValue(v any) (string, bool) {
	switch value := v.(type) {
	case string:
		return value, true
	case json.Number:
		return value.String(), true
	case float64:
		return strconv.FormatFloat(value, 'f', -1, 64), true
	case int:
		return strconv.Itoa(value), true
	case int64:
		return strconv.FormatInt(value, 10), true
	case []any:
		parts := make([]string, len(value))
		for i, item := range value {
			part, ok := stringValue(item)
			if !ok {
				return "", false
			}
			parts[i] = part
		}
		return strings.Join(parts, ","), true
	default:
		return "", false
	}
}
func numberValue(v any) (float64, bool) {
	switch value := v.(type) {
	case float64:
		return value, true
	case int:
		return float64(value), true
	case int64:
		return float64(value), true
	case json.Number:
		n, err := value.Float64()
		return n, err == nil
	case string:
		n, err := strconv.ParseFloat(value, 64)
		return n, err == nil
	default:
		return 0, false
	}
}
func normalizeTags(value any) ([]string, bool) {
	values := []string{}
	switch v := value.(type) {
	case string:
		for _, part := range strings.Split(v, ",") {
			values = append(values, part)
		}
	case []any:
		for _, part := range v {
			item, ok := part.(string)
			if !ok {
				return nil, false
			}
			values = append(values, item)
		}
	default:
		return nil, false
	}
	values = uniqueStrings(values)
	if len(values) == 0 {
		return nil, false
	}
	return values, true
}
func normalizeSeries(value any) []map[string]any {
	values, ok := value.([]any)
	if !ok {
		return nil
	}
	out := []map[string]any{}
	for _, value := range values {
		item, ok := value.(map[string]any)
		if !ok {
			continue
		}
		name, ok := item["series"].(string)
		if !ok || name == "" {
			continue
		}
		result := map[string]any{"series": name}
		if sequence, ok := stringValue(item["sequence"]); ok && sequence != "" {
			result["sequence"] = sequence
		}
		out = append(out, result)
	}
	return out
}

var _ = strconv.IntSize
