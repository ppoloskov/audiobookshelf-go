package metadata

import (
	"context"
	"net/url"
	"strings"
)

// audiobookCovers searches audiobookcovers.com; it returns covers only.
type audiobookCovers struct {
	getter
	base string
}

func (a audiobookCovers) searchBooks(ctx context.Context, q BookQuery) ([]BookResult, error) {
	if q.Title == "" {
		return nil, nil
	}
	endpoint := a.base
	if !strings.Contains(endpoint, "?") {
		endpoint += "?"
	}
	var response []struct {
		Versions struct {
			PNG struct {
				Original string `json:"original"`
			} `json:"png"`
		} `json:"versions"`
	}
	if a.json(ctx, endpoint+url.Values{"q": {q.Title}}.Encode(), &response) != nil {
		return nil, nil // a failing provider is an empty answer
	}
	var out []BookResult
	for _, item := range response {
		if cover := item.Versions.PNG.Original; cover != "" {
			out = append(out, BookResult{Cover: cover})
		}
	}
	return out, nil
}
