package metadata

import (
	"context"
	"encoding/json"
	"net/url"
	"slices"
	"strconv"
	"strings"
)

// fantLab searches FantLab.ru works.
type fantLab struct {
	getter
	base string
}

// fantLabSkippedTypes are work types that are not books (articles, poems...).
var fantLabSkippedTypes = []int{7, 11, 12, 22, 23, 24, 25, 26, 46, 47, 49, 51, 52, 55, 56, 57}

type fantLabName struct {
	Name string `json:"name"`
}

type fantLabWork struct {
	WorkID         json.RawMessage `json:"work_id"`
	Name           looseText       `json:"work_name"`
	Alts           []looseText     `json:"work_name_alts"`
	Authors        []fantLabName   `json:"authors"`
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
	query := url.Values{"q": {search}, "page": {"1"}, "onlymatches": {"1"}}
	var matches []struct {
		WorkID     json.RawMessage `json:"work_id"`
		WorkTypeID looseText       `json:"work_type_id"`
	}
	if f.json(ctx, f.base+"/search-works?"+query.Encode(), &matches) != nil {
		return nil, nil // a failing provider is an empty answer
	}
	var out []BookResult
	for _, match := range matches {
		workType, _ := strconv.Atoi(string(match.WorkTypeID)) // not a number: not skipped
		id := rawText(match.WorkID)
		if id == "" || slices.Contains(fantLabSkippedTypes, workType) {
			continue
		}
		var work fantLabWork
		if f.json(ctx, f.base+"/work/"+url.PathEscape(id)+"/extended", &work) != nil || len(work.WorkID) == 0 && work.Name == "" {
			continue
		}
		out = append(out, f.book(ctx, work))
	}
	return out, nil
}

func (f fantLab) book(ctx context.Context, work fantLabWork) BookResult {
	book := BookResult{ID: work.WorkID, Title: string(work.Name), PublishedYear: string(work.Year), Description: string(work.Description)}
	var authors []string
	for _, author := range work.Authors {
		if name := strings.TrimSpace(author.Name); name != "" {
			authors = append(authors, name)
		}
	}
	book.Author = strings.Join(authors, ", ")
	if len(work.Alts) > 0 {
		book.Subtitle = string(work.Alts[0])
	}
	if work.Image != "" {
		book.Cover = "https://fantlab.ru" + work.Image
	}
	// Genre group 1 is the genre tree: its first root and that root's children.
	for _, group := range work.Classificatory.GenreGroup {
		if group.ID != 1 {
			continue
		}
		if len(group.Genre) > 0 {
			root := group.Genre[0]
			book.Genres = append(book.Genres, root.Label)
			for _, sub := range root.Genre {
				book.Genres = append(book.Genres, sub.Label)
			}
			book.Genres = slices.DeleteFunc(book.Genres, func(label string) bool { return label == "" })
		}
		break
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
			var edition struct {
				Image string `json:"image"`
			}
			if f.json(ctx, f.base+"/edition/"+url.PathEscape(id), &edition) == nil && edition.Image != "" {
				book.Cover = "https://fantlab.ru" + edition.Image
			}
		}
	}
	return book
}
