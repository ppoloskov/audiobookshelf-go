package metadata

import (
	"context"
	"maps"
	"net/url"
	"slices"
	"strings"
)

// google searches Google Books.
type google struct {
	getter
	base string
}

type googleIdentifier struct {
	Type       string `json:"type"`
	Identifier string `json:"identifier"`
}

type googleVolume struct {
	ID         string `json:"id"`
	VolumeInfo struct {
		Title               string             `json:"title"`
		Subtitle            string             `json:"subtitle"`
		Authors             []string           `json:"authors"`
		Publisher           string             `json:"publisher"`
		PublishedDate       string             `json:"publishedDate"`
		Description         string             `json:"description"`
		IndustryIdentifiers []googleIdentifier `json:"industryIdentifiers"`
		Categories          []string           `json:"categories"`
		ImageLinks          map[string]string  `json:"imageLinks"`
	} `json:"volumeInfo"`
}

func (g google) searchBooks(ctx context.Context, q BookQuery) ([]BookResult, error) {
	query := "intitle:" + q.Title
	if q.Author != "" {
		query += " inauthor:" + q.Author
	}
	var response struct {
		Items []googleVolume `json:"items"`
	}
	if g.json(ctx, g.base+"?q="+url.QueryEscape(query), &response) != nil {
		return nil, nil // a failing provider is an empty answer
	}
	var out []BookResult
	for _, item := range response.Items {
		v := item.VolumeInfo
		if v.Title == "" {
			continue
		}
		out = append(out, BookResult{
			ID: jsonString(item.ID), Title: v.Title, Subtitle: v.Subtitle, Author: strings.Join(v.Authors, ", "),
			Publisher: v.Publisher, PublishedYear: before(v.PublishedDate, "-"), Description: v.Description,
			Cover: largestImage(v.ImageLinks), Genres: v.Categories, ISBN: googleISBN(v.IndustryIdentifiers),
		})
	}
	return out, nil
}

// googleISBN is the ISBN-13, else the first ISBN-10.
func googleISBN(identifiers []googleIdentifier) string {
	var isbn string
	for _, identifier := range identifiers {
		if identifier.Type == "ISBN_13" {
			return identifier.Identifier
		}
		if isbn == "" && identifier.Type == "ISBN_10" {
			isbn = identifier.Identifier
		}
	}
	return isbn
}

// largestImage is the biggest named image, over https, or the last by name.
func largestImage(images map[string]string) string {
	for _, key := range []string{"extraLarge", "large", "medium", "small", "thumbnail", "smallThumbnail"} {
		if value := images[key]; value != "" {
			return strings.Replace(value, "http:", "https:", 1)
		}
	}
	if len(images) == 0 {
		return ""
	}
	keys := slices.Sorted(maps.Keys(images))
	return strings.Replace(images[keys[len(keys)-1]], "http:", "https:", 1)
}
