package libraries

import (
	"cmp"
	"context"
	"encoding/base64"
	"maps"
	"net/http"
	"net/url"
	"slices"
	"strings"

	"audiobookshelf-compat/internal/author"
	"audiobookshelf-compat/internal/db"
	"audiobookshelf-compat/internal/httpx"
	"audiobookshelf-compat/internal/item"
	"audiobookshelf-compat/internal/textsort"
)

// narratorID is the id clients use for a narrator: the URL-escaped base64 of
// the name.
func narratorID(name string) string {
	return url.QueryEscape(base64.StdEncoding.EncodeToString([]byte(name)))
}

// narrators is GET /api/libraries/{id}/narrators: every narrator with the
// number of books naming them, in natural order.
func (h *Handler) narrators(w http.ResponseWriter, r *http.Request, scope Scope) error {
	rows, err := selectAll[struct {
		Narrators db.JSON[[]string] `db:"narrators"`
	}](r.Context(), h.db, `SELECT b.narrators FROM books b JOIN libraryItems li ON li.mediaId=b.id WHERE li.libraryId = ? AND json_array_length(COALESCE(b.narrators,'[]'))>0`, scope.ID)
	if err != nil {
		return httpx.Internal("database read failed", err)
	}
	counts := map[string]int{}
	for _, row := range rows {
		for name := range maps.Keys(setOf(row.Narrators.V)) {
			counts[name]++
		}
	}
	type narrator struct {
		ID       string `json:"id"`
		Name     string `json:"name"`
		NumBooks int    `json:"numBooks"`
	}
	list := make([]narrator, 0, len(counts))
	for _, name := range slices.Sorted(maps.Keys(counts)) {
		list = append(list, narrator{narratorID(name), name, counts[name]})
	}
	slices.SortStableFunc(list, func(a, b narrator) int { return textsort.NaturalBaseCompare(a.Name, b.Name) })
	return httpx.JSON(w, r, struct {
		Narrators []narrator `json:"narrators"`
	}{list})
}

// setOf is the distinct non-empty names.
func setOf(names []string) map[string]struct{} {
	set := map[string]struct{}{}
	for _, name := range names {
		if name != "" {
			set[name] = struct{}{}
		}
	}
	return set
}

// renameNarrator is PATCH /api/libraries/{id}/narrators/{narratorId}; the body
// names the new narrator. Needs the update permission.
func (h *Handler) renameNarrator(w http.ResponseWriter, r *http.Request, scope Scope) error {
	if !scope.User.CanUpdate() {
		return httpx.Forbidden()
	}
	body, err := httpx.Decode[struct {
		Name string `json:"name"`
	}](w, r)
	if err != nil || body.Name == "" {
		return httpx.BadRequest("Invalid request payload. Name not specified.")
	}
	return h.editNarrator(w, r, scope, body.Name)
}

// removeNarrator is DELETE /api/libraries/{id}/narrators/{narratorId}. Needs
// the update permission.
func (h *Handler) removeNarrator(w http.ResponseWriter, r *http.Request, scope Scope) error {
	if !scope.User.CanUpdate() {
		return httpx.Forbidden()
	}
	return h.editNarrator(w, r, scope, "")
}

// editNarrator replaces the narrator named by the path with newName (appended
// when the book lacks it), or drops them when newName is empty. It answers the
// number of books changed and announces them with items_updated.
func (h *Handler) editNarrator(w http.ResponseWriter, r *http.Request, scope Scope, newName string) error {
	ctx := r.Context()
	// Narrator edits patch the filter data upstream (LibraryController.js:1170,
	// 1214); reloading it gives the same content.
	defer h.deps.Filters.Invalidate(scope.ID)
	decoded, err := url.QueryUnescape(r.PathValue("narratorId"))
	if err != nil {
		return httpx.BadRequest("")
	}
	nameBytes, err := base64.StdEncoding.DecodeString(decoded)
	if err != nil {
		nameBytes, err = base64.RawStdEncoding.DecodeString(decoded)
	}
	if err != nil {
		return httpx.BadRequest("")
	}
	oldName := string(nameBytes)
	var books []struct {
		ItemID    string            `db:"itemId"`
		BookID    string            `db:"bookId"`
		Narrators db.JSON[[]string] `db:"narrators"`
	}
	err = h.db.Tx(ctx, func(ctx context.Context) error {
		if err := h.db.Select(ctx, &books, `SELECT li.id AS itemId, b.id AS bookId, b.narrators FROM books b JOIN libraryItems li ON li.mediaId=b.id WHERE li.libraryId = ? AND EXISTS(SELECT 1 FROM json_each(COALESCE(b.narrators,'[]')) WHERE json_each.value = ?)`, scope.ID, oldName); err != nil {
			return err
		}
		for _, book := range books {
			next := slices.DeleteFunc(slices.Clone(book.Narrators.V), func(name string) bool { return name == oldName })
			if newName != "" && !slices.Contains(next, newName) {
				next = append(next, newName)
			}
			if next == nil {
				next = []string{}
			}
			if _, err := h.db.Exec(ctx, `UPDATE books SET narrators = ?, updatedAt = ? WHERE id = ?`, db.JSON[[]string]{V: next}, db.Now(), book.BookID); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return httpx.Internal("database write failed", err)
	}
	if len(books) > 0 {
		itemIDs := make([]string, len(books))
		for i, book := range books {
			itemIDs[i] = book.ItemID
		}
		if loaded, err := item.Load(ctx, h.db, itemIDs, item.Options{Episodes: true}); err == nil && len(loaded) > 0 {
			prefixes := h.settings.Server().SortingPrefixes
			expanded := make([]item.Expanded, len(loaded))
			for i, it := range loaded {
				expanded[i] = it.Expanded(prefixes)
			}
			h.emit.ToAll("items_updated", expanded)
		}
	}
	return httpx.JSON(w, r, struct {
		Updated int `json:"updated"`
	}{len(books)})
}

// podcastTitles is GET /api/libraries/{id}/podcast-titles (admin only).
func (h *Handler) podcastTitles(w http.ResponseWriter, r *http.Request, scope Scope) error {
	if !isAdmin(scope.User) {
		return httpx.Forbidden()
	}
	podcasts, err := selectAll[struct {
		Title         *string `json:"title" db:"title"`
		ItunesID      *string `json:"itunesId" db:"itunesId"`
		LibraryItemID string  `json:"libraryItemId" db:"libraryItemId"`
		LibraryID     string  `json:"libraryId" db:"libraryId"`
	}](r.Context(), h.db, `SELECT p.title AS title, p.itunesId AS itunesId, li.id AS libraryItemId, li.libraryId AS libraryId FROM podcasts p JOIN libraryItems li ON li.mediaId=p.id WHERE li.libraryId = ?`, scope.ID)
	if err != nil {
		return httpx.Internal("database read failed", err)
	}
	return httpx.JSON(w, r, struct {
		Podcasts any `json:"podcasts"`
	}{podcasts})
}

// authors is GET /api/libraries/{id}/authors: the authors with a book the user
// may see (all of them for admins), sorted by ?sort (name, lastFirst, addedAt,
// updatedAt or numBooks; ?desc=1 reverses) and paged when limit and page are
// given, in which case the answer is the page with its paging fields.
func (h *Handler) authors(w http.ResponseWriter, r *http.Request, scope Scope) error {
	list, err := author.List(r.Context(), h.db, scope.User, scope.ID)
	if err != nil {
		return httpx.Internal("database read failed", err)
	}
	query := r.URL.Query()
	sortBy, desc := query.Get("sort"), query.Get("desc") == "1"
	slices.SortStableFunc(list, func(a, b author.Listed) int {
		var order int
		switch sortBy { // another key keeps the order of the library
		case "name":
			order = cmp.Compare(strings.ToLower(db.Val(a.Name)), strings.ToLower(db.Val(b.Name)))
		case "lastFirst":
			order = cmp.Compare(strings.ToLower(db.Val(a.LastFirst)), strings.ToLower(db.Val(b.LastFirst)))
		case "addedAt":
			order = cmp.Compare(a.AddedAt, b.AddedAt)
		case "updatedAt":
			order = cmp.Compare(a.UpdatedAt, b.UpdatedAt)
		case "numBooks":
			order = cmp.Compare(db.Val(a.NumBooks), db.Val(b.NumBooks))
		}
		if desc {
			return -order
		}
		return order
	})
	// Paged when `limit && !isNaN(page)` (LibraryController.js:1022).
	if scope.Limit == 0 || !scope.HasPage {
		return httpx.JSON(w, r, struct {
			Authors []author.Listed `json:"authors"`
		}{list})
	}
	start := min(scope.Page*scope.Limit, len(list))
	return httpx.JSON(w, r, struct {
		Results  []author.Listed `json:"results"`
		Total    int             `json:"total"`
		Limit    int             `json:"limit"`
		Page     int             `json:"page"`
		SortBy   string          `json:"sortBy,omitempty"`
		SortDesc bool            `json:"sortDesc"`
		FilterBy string          `json:"filterBy,omitempty"`
		Minified bool            `json:"minified"`
		Include  string          `json:"include,omitempty"`
	}{list[start:min(start+scope.Limit, len(list))], len(list), scope.Limit, scope.Page, sortBy, desc, query.Get("filter"), query.Get("minified") == "1", query.Get("include")})
}
