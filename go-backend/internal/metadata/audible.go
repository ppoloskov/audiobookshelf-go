package metadata

import (
	"context"
	"net/url"
	"strings"
)

// audible searches the Audible catalog and reads each hit from Audnexus.
type audible struct {
	getter
	catalog  string
	audnexus audnexus
}

// audibleDomains maps a region to its catalog API domain suffix.
var audibleDomains = map[string]string{"us": ".com", "ca": ".ca", "uk": ".co.uk", "au": ".com.au", "fr": ".fr", "de": ".de", "jp": ".co.jp", "it": ".it", "in": ".in", "es": ".es"}

func (a audible) searchBooks(ctx context.Context, q BookQuery) ([]BookResult, error) {
	region := q.Region
	if region == "" {
		if _, suffix, ok := strings.Cut(q.Provider, "."); ok { // "audible.uk"
			region = suffix
		}
	}
	if _, known := audibleDomains[region]; !known {
		region = ""
	}
	var books []audnexusBook
	if asin := firstASIN(q.ASIN, q.Title); asin != "" {
		if book, ok := a.audnexus.book(ctx, asin, region); ok {
			books = append(books, book)
		}
	} else {
		params := url.Values{"num_results": {"10"}, "products_sort_by": {"Relevance"}, "title": {q.Title}}
		if q.Author != "" {
			params.Set("author", q.Author)
		}
		catalog := a.catalog
		if region != "" && strings.Contains(catalog, "api.audible.com") {
			catalog = strings.Replace(catalog, "api.audible.com", "api.audible"+audibleDomains[region], 1)
		}
		var response struct {
			Products []struct {
				ASIN string `json:"asin"`
			} `json:"products"`
		}
		if a.json(ctx, catalog+"?"+params.Encode(), &response) != nil {
			return nil, nil // a failing provider is an empty answer
		}
		for _, product := range response.Products {
			if asin := strings.ToUpper(product.ASIN); validASIN(asin) {
				if book, ok := a.audnexus.book(ctx, asin, region); ok {
					books = append(books, book)
				}
			}
		}
	}
	out := make([]BookResult, len(books))
	for i, book := range books {
		out[i] = book.result()
	}
	return out, nil
}

// firstASIN is the first candidate that is an ASIN, upper-cased.
func firstASIN(candidates ...string) string {
	for _, candidate := range candidates {
		if candidate = strings.ToUpper(candidate); validASIN(candidate) {
			return candidate
		}
	}
	return ""
}
