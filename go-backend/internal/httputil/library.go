package httputil

import (
	"context"
	"database/sql"
	"errors"
	"math"
	"net/http"
	"strconv"
	"strings"
)

// LibraryRequest is what every /api/libraries/{id}/* route starts from.
type LibraryRequest struct {
	ID, MediaType string
	Limit, Page   int
}

// RowQuerier is the part of *sql.DB OpenLibrary reads.
type RowQuerier interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// OpenLibrary is LibraryController.middleware (server/controllers/
// LibraryController.js:1475-1497): 403 when canAccess(id) is false, 404
// "Library not found", then optional limit and page must be non-negative
// integers (Number() of the text, non-numeric becoming 0), else 400.
func OpenLibrary(r *http.Request, db RowQuerier, canAccess func(id string) bool, id string) (LibraryRequest, error) {
	lib := LibraryRequest{ID: id}
	if !canAccess(id) {
		return lib, Status(http.StatusForbidden, "")
	}
	switch err := db.QueryRowContext(r.Context(), `SELECT mediaType FROM libraries WHERE id=?`, id).Scan(&lib.MediaType); {
	case errors.Is(err, sql.ErrNoRows):
		return lib, Status(http.StatusNotFound, "Library not found")
	case err != nil:
		return lib, Internal("database read failed", err)
	}
	for _, q := range []struct {
		key string
		dst *int
	}{{"limit", &lib.Limit}, {"page", &lib.Page}} {
		raw, present := r.URL.Query()[q.key]
		if !present {
			continue
		}
		n, err := strconv.ParseFloat(strings.TrimSpace(raw[0]), 64)
		if err != nil || math.IsNaN(n) {
			n = 0
		}
		if n != math.Trunc(n) || n < 0 || math.IsInf(n, 0) {
			return lib, Status(http.StatusBadRequest, "Invalid request. "+q.key+" must be a positive integer")
		}
		*q.dst = int(n)
	}
	return lib, nil
}
