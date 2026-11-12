package author

import (
	"context"
	"fmt"
	"net/http"
	"slices"
	"strings"

	"audiobookshelf-compat/internal/auth"
	"audiobookshelf-compat/internal/db"
	"audiobookshelf-compat/internal/httpx"
	"audiobookshelf-compat/internal/item"
	"audiobookshelf-compat/internal/text"
)

// detail is the body of GET /api/authors/{id}: the author, with include=items
// the library items and with include=items,series also their series groups.
// A pointer to an empty slice still encodes as [].
type detail struct {
	View
	Series       *[]seriesGroup   `json:"series,omitempty"`
	LibraryItems *[]item.Minified `json:"libraryItems,omitempty"`
}

// seriesGroup is one series of an author's books.
type seriesGroup struct {
	ID    string          `json:"id"`
	Name  *string         `json:"name"`
	Items []item.Minified `json:"items"`
}

// seriesRef is the metadata.series of an item inside a series group.
type seriesRef struct {
	ID               string  `json:"id"`
	Name             *string `json:"name"`
	NameIgnorePrefix *string `json:"nameIgnorePrefix"`
	Sequence         *string `json:"sequence"`
}

// get is AuthorController.findOne (js:40-88).
func (h *Handler) get(w http.ResponseWriter, r *http.Request, user *auth.Principal) error {
	ctx := r.Context()
	a, err := h.load(r, user)
	if err != nil {
		return err
	}
	out := detail{View: viewOf(a, nil)}
	include := strings.Split(r.URL.Query().Get("include"), ",")
	if slices.Contains(include, "items") {
		items, err := h.visibleItems(ctx, user, a)
		if err != nil {
			return httpx.Internal("database read failed", err)
		}
		if slices.Contains(include, "series") {
			groups, err := h.seriesGroups(ctx, items)
			if err != nil {
				return httpx.Internal("database read failed", err)
			}
			out.Series = &groups
		}
		minified := make([]item.Minified, len(items))
		for i, it := range items {
			minified[i] = it.Minified(h.settings.Server().SortingPrefixes)
		}
		out.LibraryItems = &minified
	}
	return httpx.JSON(w, r, out)
}

// visibleItems are the author's books in the library the user may see, newest
// first (LibraryItem.getForAuthor, js:567).
func (h *Handler) visibleItems(ctx context.Context, user *auth.Principal, a db.Author) ([]item.Item, error) {
	clause, args := user.MediaClause("b")
	var ids []string
	err := h.db.Select(ctx, &ids, `SELECT li.id FROM libraryItems li JOIN books b ON b.id=li.mediaId WHERE li.libraryId = ? AND li.mediaType='book' AND EXISTS (SELECT 1 FROM bookAuthors ba WHERE ba.bookId=b.id AND ba.authorId = ?)`+clause+` ORDER BY li.createdAt DESC`,
		append([]any{a.LibraryID, a.ID}, args...)...)
	if err != nil {
		return nil, err
	}
	return item.Load(ctx, h.db, ids, item.Options{})
}

// seriesGroups groups the items into their series (js:49-81): groups keep the
// order they are first seen, items are copies whose metadata.series names the
// group, sorted by sequence with the natural comparer.
func (h *Handler) seriesGroups(ctx context.Context, items []item.Item) ([]seriesGroup, error) {
	ids := make([]string, len(items))
	for i, it := range items {
		ids[i] = it.ID
	}
	var rows []struct {
		ItemID           string  `db:"itemId"`
		ID               string  `db:"id"`
		Name             *string `db:"name"`
		NameIgnorePrefix *string `db:"nameIgnorePrefix"`
		Sequence         *string `db:"sequence"`
	}
	err := h.db.Select(ctx, &rows, `SELECT li.id AS itemId, s.id, s.name, s.nameIgnorePrefix, bs.sequence FROM libraryItems li JOIN bookSeries bs ON bs.bookId=li.mediaId JOIN series s ON s.id=bs.seriesId WHERE li.id IN (?) ORDER BY bs.createdAt, bs.rowid`, ids)
	if err != nil {
		return nil, err
	}
	var groups []*seriesGroup
	sequences := map[string][]string{} // series id -> sequence of each item, in group order
	for _, it := range items {
		minified := it.Minified(h.settings.Server().SortingPrefixes)
		book, ok := minified.Media.(item.BookMinified)
		if !ok {
			return nil, fmt.Errorf("item %s has no book media", it.ID)
		}
		for _, row := range rows {
			if row.ItemID != it.ID {
				continue
			}
			i := slices.IndexFunc(groups, func(g *seriesGroup) bool { return g.ID == row.ID })
			if i < 0 {
				groups = append(groups, &seriesGroup{ID: row.ID, Name: row.Name})
				i = len(groups) - 1
			}
			copied := minified
			book.Metadata.Series = seriesRef{row.ID, row.Name, row.NameIgnorePrefix, row.Sequence}
			copied.Media = book
			groups[i].Items = append(groups[i].Items, copied)
			sequences[row.ID] = append(sequences[row.ID], jsText(row.Sequence))
		}
	}
	out := make([]seriesGroup, len(groups))
	for i, g := range groups {
		order := make([]int, len(g.Items))
		for j := range order {
			order[j] = j
		}
		seq := sequences[g.ID]
		slices.SortStableFunc(order, func(x, y int) int { return text.NaturalBaseCompare(seq[x], seq[y]) })
		sorted := make([]item.Minified, len(order))
		for j, k := range order {
			sorted[j] = g.Items[k]
		}
		out[i] = seriesGroup{ID: g.ID, Name: g.Name, Items: sorted}
	}
	return out, nil
}

// jsText is a sequence as JavaScript's comparer sees it: null is "null".
func jsText(s *string) string {
	if s == nil {
		return "null"
	}
	return *s
}
