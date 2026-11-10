package catalog

import (
	"audiobookshelf-compat/internal/access"
	"audiobookshelf-compat/internal/httputil"
	"audiobookshelf-compat/internal/people"
	"audiobookshelf-compat/internal/store"
	"net/http"
	"sort"
	"strings"
)

func (h *Handler) authors(w http.ResponseWriter, r *http.Request, lib libraryScope) error {
	user, ctx := lib.user, r.Context()
	rows, err := h.db.QueryContext(ctx, `SELECT `+people.AuthorColumns+` FROM authors WHERE libraryId=?`, lib.id)
	if err != nil {
		return httputil.Internal("database read failed", err)
	}
	type listed struct {
		people.AuthorRecord
		numBooks int64
	}
	var data []listed
	for rows.Next() {
		a, err := people.ScanAuthor(rows)
		if err != nil {
			rows.Close()
			return httputil.Internal("database read failed", err)
		}
		data = append(data, listed{AuthorRecord: a})
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return httputil.Internal("database read failed", err)
	}
	rows.Close()
	admin := access.AdminUser(user)
	where, args := access.For(ctx, user).MediaClause("b")
	filtered := make([]listed, 0, len(data))
	for _, a := range data {
		q := `SELECT COUNT(DISTINCT b.id) FROM bookAuthors ba JOIN books b ON b.id=ba.bookId WHERE ba.authorId=?` + where
		if err := h.db.QueryRowContext(ctx, q, append([]any{a.ID}, args...)...).Scan(&a.numBooks); err != nil {
			return httputil.Internal("database read failed", err)
		}
		if admin || a.numBooks > 0 {
			filtered = append(filtered, a)
		}
	}
	sortBy := r.URL.Query().Get("sort")
	desc := r.URL.Query().Get("desc") == "1"
	sort.SliceStable(filtered, func(i, j int) bool {
		a, b := filtered[i], filtered[j]
		var less bool
		switch sortBy {
		case "name":
			less = strings.ToLower(a.Name.String) < strings.ToLower(b.Name.String)
		case "lastFirst":
			less = strings.ToLower(a.LastFirst.String) < strings.ToLower(b.LastFirst.String)
		case "addedAt":
			less = stringValue(a.CreatedAt) < stringValue(b.CreatedAt)
		case "updatedAt":
			less = stringValue(a.UpdatedAt) < stringValue(b.UpdatedAt)
		case "numBooks":
			less = a.numBooks < b.numBooks
		default:
			return false
		}
		return less != desc
	})
	// isPaginated: `req.query.limit && !isNaN(req.query.page)` (LibraryController.js:1022).
	limit, page, paginated := lib.limit, lib.page, lib.limit != 0 && lib.hasPage
	if paginated {
		filtered = pageOf(filtered, page, limit)
	}
	authors := make([]people.AuthorListEntry, 0, len(filtered))
	for _, a := range filtered {
		view, err := a.ToOldJSON()
		if err != nil {
			return httputil.Internal("database read failed", err)
		}
		authors = append(authors, people.AuthorListEntry{AuthorWithBooks: people.AuthorWithBooks{AuthorView: view, NumBooks: int(a.numBooks)}, LastFirst: store.NullStringPtr(a.LastFirst)})
	}
	if !paginated {
		return httputil.JSON(w, r, struct {
			Authors []people.AuthorListEntry `json:"authors"`
		}{authors})
	}
	payload := map[string]any{"results": authors, "total": len(data), "limit": limit, "page": page, "sortDesc": desc, "minified": r.URL.Query().Get("minified") == "1"}
	if sortBy != "" {
		payload["sortBy"] = sortBy
	}
	if v := r.URL.Query().Get("filter"); v != "" {
		payload["filterBy"] = v
	}
	if v := r.URL.Query().Get("include"); v != "" {
		payload["include"] = v
	}
	return httputil.JSON(w, r, payload)
}
