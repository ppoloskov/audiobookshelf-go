package metadata

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"net/url"
	"path"
	"regexp"
	"slices"
	"strconv"
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
	resp, err := fetchJSON[struct {
		Items []googleVolume `json:"items"`
	}](ctx, g.getter, queryURL(g.base, "q", query))
	if err != nil {
		return nil, nil // a failing provider is an empty answer
	}
	var out []BookResult
	for _, item := range resp.Items {
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
	for _, id := range identifiers {
		if id.Type == "ISBN_13" {
			return id.Identifier
		}
		if isbn == "" && id.Type == "ISBN_10" {
			isbn = id.Identifier
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
	base := strings.TrimRight(o.base, "/")
	resp, err := fetchJSON[struct {
		Docs []openLibraryDoc `json:"docs"`
	}](ctx, o.getter, queryURL(base+"/search.json", "title", q.Title, "author", q.Author))
	if err != nil {
		return nil, nil // a failing provider is an empty answer
	}
	var out []BookResult
	for _, doc := range resp.Docs {
		if doc.Key == "" {
			continue
		}
		work, _ := fetchJSON[openLibraryWork](ctx, o.getter, base+"/"+strings.TrimPrefix(doc.Key, "/")+".json") // a work without details still lists
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

// fantLab searches FantLab.ru works.
type fantLab struct {
	getter
	base string
}

// fantLabSkippedTypes are work types that are not books (articles, poems...).
var fantLabSkippedTypes = []int{7, 11, 12, 22, 23, 24, 25, 26, 46, 47, 49, 51, 52, 55, 56, 57}

type fantLabWork struct {
	WorkID         json.RawMessage `json:"work_id"`
	Name           looseText       `json:"work_name"`
	Alts           []looseText     `json:"work_name_alts"`
	Authors        []namedItem     `json:"authors"`
	Year           looseText       `json:"work_year"`
	Description    looseText       `json:"work_description"`
	Image          string          `json:"image"`
	Classificatory struct {
		GenreGroup []struct {
			ID    int `json:"genre_group_id"`
			Genre []struct {
				Label string `json:"label"`
				Genre []struct {
					Label string `json:"label"`
				} `json:"genre"`
			} `json:"genre"`
		} `json:"genre_group"`
	} `json:"classificatory"`
	Editions map[string]struct {
		List []struct {
			ISBN      looseText       `json:"isbn"`
			EditionID json.RawMessage `json:"edition_id"`
		} `json:"list"`
	} `json:"editions_blocks"`
}

func (f fantLab) searchBooks(ctx context.Context, q BookQuery) ([]BookResult, error) {
	search := q.Title
	if q.Author != "" {
		search += " " + q.Author
	}
	matches, err := fetchJSON[[]struct {
		WorkID     json.RawMessage `json:"work_id"`
		WorkTypeID looseText       `json:"work_type_id"`
	}](ctx, f.getter, queryURL(f.base+"/search-works", "q", search, "page", "1", "onlymatches", "1"))
	if err != nil {
		return nil, nil // a failing provider is an empty answer
	}
	var out []BookResult
	for _, match := range matches {
		workType, _ := strconv.Atoi(string(match.WorkTypeID)) // not a number: not skipped
		id := rawText(match.WorkID)
		if id == "" || slices.Contains(fantLabSkippedTypes, workType) {
			continue
		}
		work, err := fetchJSON[fantLabWork](ctx, f.getter, f.base+"/work/"+url.PathEscape(id)+"/extended")
		if err != nil || (len(work.WorkID) == 0 && work.Name == "") {
			continue
		}
		out = append(out, f.book(ctx, work))
	}
	return out, nil
}

func (f fantLab) book(ctx context.Context, work fantLabWork) BookResult {
	book := BookResult{
		ID: work.WorkID, Title: string(work.Name), Author: joinNames(work.Authors),
		PublishedYear: string(work.Year), Description: string(work.Description),
	}
	if len(work.Alts) > 0 {
		book.Subtitle = string(work.Alts[0])
	}
	if work.Image != "" {
		book.Cover = "https://fantlab.ru" + work.Image
	}
	// Genre group 1 is the genre tree: its first root and that root's children.
	for _, group := range work.Classificatory.GenreGroup {
		if group.ID == 1 && len(group.Genre) > 0 {
			root := group.Genre[0]
			if root.Label != "" {
				book.Genres = append(book.Genres, root.Label)
			}
			for _, sub := range root.Genre {
				if sub.Label != "" {
					book.Genres = append(book.Genres, sub.Label)
				}
			}
			break
		}
	}
	// The newest edition of block 30 (audiobooks), else block 10, gives the ISBN and a better cover.
	block, ok := work.Editions["30"]
	if !ok {
		block = work.Editions["10"]
	}
	if len(block.List) > 0 {
		last := block.List[len(block.List)-1]
		book.ISBN = string(last.ISBN)
		if id := rawText(last.EditionID); id != "" {
			if ed, err := fetchJSON[struct {
				Image string `json:"image"`
			}](ctx, f.getter, f.base+"/edition/"+url.PathEscape(id)); err == nil && ed.Image != "" {
				book.Cover = "https://fantlab.ru" + ed.Image
			}
		}
	}
	return book
}

// audiobookCovers searches audiobookcovers.com; it returns covers only.
type audiobookCovers struct {
	getter
	base string
}

func (a audiobookCovers) searchBooks(ctx context.Context, q BookQuery) ([]BookResult, error) {
	if q.Title == "" {
		return nil, nil
	}
	items, err := fetchJSON[[]struct {
		Versions struct {
			PNG struct {
				Original string `json:"original"`
			} `json:"png"`
		} `json:"versions"`
	}](ctx, a.getter, queryURL(a.base, "q", q.Title))
	if err != nil {
		return nil, nil // a failing provider is an empty answer
	}
	var out []BookResult
	for _, item := range items {
		if cover := item.Versions.PNG.Original; cover != "" {
			out = append(out, BookResult{Cover: cover})
		}
	}
	return out, nil
}
