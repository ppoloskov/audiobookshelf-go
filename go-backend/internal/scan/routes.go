package scan

import (
	"context"
	"log/slog"
	"net/http"
	"net/url"
	"strings"

	"audiobookshelf-compat/internal/auth"
	"audiobookshelf-compat/internal/db"
	"audiobookshelf-compat/internal/httpx"
)

// Routes registers the scan endpoints; all of them are admin-only.
func (s *Scanner) Routes(mux *http.ServeMux) {
	mux.Handle("POST /api/items/batch/scan", auth.Admin(httpx.Handler(s.batchScan)))
	mux.Handle("POST /api/libraries/{id}/scan", auth.Admin(httpx.Handler(s.scanLibraryRoute)))
	mux.Handle("POST /api/items/{id}/scan", auth.Admin(httpx.Handler(s.scanItemRoute)))
}

// batchScan rescans the listed items in the background.
func (s *Scanner) batchScan(w http.ResponseWriter, r *http.Request) error {
	body, err := httpx.Decode[struct {
		LibraryItemIDs []string `json:"libraryItemIds"`
	}](w, r)
	if err != nil || len(body.LibraryItemIDs) == 0 {
		return httpx.BadRequest("")
	}
	var found []db.LibraryItem
	if err := s.db.Select(r.Context(), &found, `SELECT * FROM libraryItems WHERE id IN (?)`, body.LibraryItemIDs); err != nil {
		return httpx.Internal("", err)
	}
	if len(found) == 0 {
		return httpx.BadRequest("")
	}
	go func() {
		for _, it := range found {
			if it.IsFile {
				continue
			}
			if _, err := s.RescanItem(context.Background(), it.ID); err != nil {
				slog.Error("item scan failed", "item", it.ID, "error", err)
			}
		}
	}()
	return httpx.Status(w, http.StatusOK)
}

func (s *Scanner) scanLibraryRoute(w http.ResponseWriter, r *http.Request) error {
	id, err := url.PathUnescape(r.PathValue("id"))
	if err != nil {
		return httpx.BadRequest("")
	}
	if _, err := s.loadLibrary(r.Context(), id); err != nil {
		return httpx.NotFound()
	}
	force := httpx.QueryBool(r, "force") || strings.EqualFold(r.URL.Query().Get("force"), "true")
	go func() {
		if _, err := s.Scan(context.Background(), id, force); err != nil {
			slog.Error("library scan failed", "library", id, "error", err)
		}
	}()
	return httpx.Text(w, http.StatusOK, "OK")
}

func (s *Scanner) scanItemRoute(w http.ResponseWriter, r *http.Request) error {
	var row db.LibraryItem
	if err := s.db.Get(r.Context(), &row, `SELECT * FROM libraryItems WHERE id = ?`, r.PathValue("id")); err != nil {
		return httpx.NotFound()
	}
	if row.IsFile {
		return httpx.Error(http.StatusInternalServerError, "")
	}
	res, err := s.RescanItem(r.Context(), row.ID)
	if err != nil && !res.Canceled {
		return httpx.Internal("", err)
	}
	result := "UPTODATE"
	switch {
	case res.Added+res.Updated > 0:
		result = "UPDATED"
	case res.Nothing:
		result = "NOTHING"
	}
	return httpx.JSON(w, r, map[string]string{"result": result})
}
