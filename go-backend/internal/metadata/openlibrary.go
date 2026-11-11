package metadata

import (
	"context"
	"fmt"
	"net/url"
	"path"
	"regexp"
	"strings"
)

// openLibrary searches Open Library works and reads each work's details.
type openLibrary struct {
	getter
	base string
}

var onlyDigits = regexp.MustCompile(`^\d+$`)

type openLibraryDoc struct {
	Title        string    `json:"title"`
	AuthorNames  []string  `json:"author_name"`
	FirstYear    looseText `json:"first_publish_year"`
	CoverEdition string    `json:"cover_edition_key"`
	Key          string    `json:"key"`
}

// openLibraryWork is a work's own record; its description is a string or a
// {type, value} object, which looseText reads both.
type openLibraryWork struct {
	Covers           []int     `json:"covers"`
	FirstPublishDate looseText `json:"first_publish_date"`
	Description      looseText `json:"description"`
}

func (o openLibrary) searchBooks(ctx context.Context, q BookQuery) ([]BookResult, error) {
	query := url.Values{"title": {q.Title}}
	if q.Author != "" {
		query.Set("author", q.Author)
	}
	base := strings.TrimRight(o.base, "/")
	var response struct {
		Docs []openLibraryDoc `json:"docs"`
	}
	if o.json(ctx, base+"/search.json?"+query.Encode(), &response) != nil {
		return nil, nil // a failing provider is an empty answer
	}
	var out []BookResult
	for _, doc := range response.Docs {
		if doc.Key == "" {
			continue
		}
		var work openLibraryWork
		_ = o.json(ctx, base+"/"+strings.TrimPrefix(doc.Key, "/")+".json", &work) // a work without details still lists
		book := BookResult{
			ID: jsonString(path.Base(doc.Key)), Key: doc.Key, Edition: doc.CoverEdition,
			FirstPublishDate: string(work.FirstPublishDate), Title: doc.Title, Author: strings.Join(doc.AuthorNames, ", "),
			PublishedYear: string(doc.FirstYear), Description: string(work.Description),
		}
		if doc.CoverEdition != "" {
			book.Cover = "https://covers.openlibrary.org/b/OLID/" + doc.CoverEdition + "-L.jpg"
		}
		for _, id := range work.Covers {
			if id > 0 {
				book.Covers = append(book.Covers, fmt.Sprintf("https://covers.openlibrary.org/b/id/%d-L.jpg", id))
			}
		}
		if book.PublishedYear == "" {
			if year := before(string(work.FirstPublishDate), "-"); onlyDigits.MatchString(year) {
				book.PublishedYear = year
			}
		}
		out = append(out, book)
	}
	return out, nil
}
