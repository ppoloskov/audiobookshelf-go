package libraries

import (
	"net/http"
	"slices"
	"strings"

	"audiobookshelf-compat/internal/auth"
	"audiobookshelf-compat/internal/db"
	"audiobookshelf-compat/internal/filterdata"
	"audiobookshelf-compat/internal/httpx"
)

// list is GET /api/libraries: the libraries the user may list, by display
// order, with stats when include=stats.
func (h *Handler) list(w http.ResponseWriter, r *http.Request, user *auth.Principal) error {
	ctx := r.Context()
	all, err := selectAll[db.Library](ctx, h.db, `SELECT * FROM libraries ORDER BY displayOrder ASC`)
	if err != nil {
		return httpx.Internal("database read failed", err)
	}
	libs := slices.DeleteFunc(all, func(lib db.Library) bool { return !user.CanListLibrary(lib.ID) })
	views, err := h.views(ctx, libs)
	if err != nil {
		return httpx.Internal("database read failed", err)
	}
	if slices.Contains(strings.Split(r.URL.Query().Get("include"), ","), "stats") {
		for i := range views {
			if views[i].Stats, err = h.statsOf(ctx, views[i].ID, db.Val(views[i].MediaType)); err != nil {
				return httpx.Internal("database read failed", err)
			}
		}
	}
	return httpx.JSON(w, r, struct {
		Libraries []View `json:"libraries"`
	}{views})
}

// get is GET /api/libraries/{id}. With include=filterdata it answers the filter
// data, its issue count, the user's playlists in the library and the library.
func (h *Handler) get(w http.ResponseWriter, r *http.Request, scope Scope) error {
	ctx := r.Context()
	view, err := h.view(ctx, scope.ID)
	if err != nil {
		return httpx.Internal("database read failed", err)
	}
	if !slices.Contains(strings.Split(r.URL.Query().Get("include"), ","), "filterdata") {
		return httpx.JSON(w, r, view)
	}
	data, err := h.deps.Filters.Get(ctx, h.db.SQL(), scope.ID, db.Val(scope.MediaType))
	if err != nil {
		return httpx.Internal("database read failed", err)
	}
	var playlists int
	if err := h.db.Get(ctx, &playlists, `SELECT COUNT(*) FROM playlists WHERE userId = ? AND libraryId = ?`, scope.User.ID, scope.ID); err != nil {
		return httpx.Internal("database read failed", err)
	}
	return httpx.JSON(w, r, struct {
		FilterData       filterdata.Data `json:"filterdata"`
		Issues           int             `json:"issues"`
		NumUserPlaylists int             `json:"numUserPlaylists"`
		Library          View            `json:"library"`
	}{data, data.NumIssues, playlists, view})
}

// filterData is GET /api/libraries/{id}/filterdata.
func (h *Handler) filterData(w http.ResponseWriter, r *http.Request, scope Scope) error {
	data, err := h.deps.Filters.Get(r.Context(), h.db.SQL(), scope.ID, db.Val(scope.MediaType))
	if err != nil {
		return httpx.Internal("database read failed", err)
	}
	return httpx.JSON(w, r, data)
}
