package author

import (
	"log/slog"
	"net/http"
	"regexp"
	"strings"

	"audiobookshelf-compat/internal/auth"
	"audiobookshelf-compat/internal/db"
	"audiobookshelf-compat/internal/httpx"
	"audiobookshelf-compat/internal/metadata"
)

var asinPattern = regexp.MustCompile(`^[A-Z0-9]{10}$`)

// match is AuthorController.match (js:337-383): look the author up by asin
// (when the body has a valid one) or by name and fill in what is missing.
func (h *Handler) match(w http.ResponseWriter, r *http.Request, user *auth.Principal) error {
	ctx := r.Context()
	a, err := h.load(r, user)
	if err != nil {
		return err
	}
	body, err := httpx.Decode[struct{ Q, ASIN, Region optional }](w, r)
	if err != nil {
		return err
	}
	q, asin, region := db.Val(body.Q.Val), db.Val(body.ASIN.Val), db.Val(body.Region.Val)
	if region == "" {
		region = "us"
	}
	var found *metadata.AuthorResult
	if h.deps.Authors != nil {
		if asinPattern.MatchString(strings.ToUpper(asin)) {
			found = h.deps.Authors.FindAuthorByASIN(ctx, asin, region)
		} else {
			found = h.deps.Authors.FindAuthorByName(ctx, q, region)
		}
	}
	if found == nil {
		return httpx.Error(http.StatusNotFound, "Author not found")
	}
	var cols []string
	updated := false
	if found.ASIN != "" && db.Val(a.ASIN) != found.ASIN {
		a.ASIN, updated = db.Ptr(found.ASIN), true
		cols = append(cols, "asin")
	}
	// Only fetches the image when there was none before or the asin changed.
	if image := db.Val(found.Image); image != "" && (db.Val(a.ImagePath) == "" || updated) {
		h.purgeImageCache(a.ID)
		if path, err := h.saveImage(ctx, a.ID, image); err != nil {
			slog.Error("download author image", "url", image, "error", err)
		} else {
			updated = true
			if db.Val(a.ImagePath) != path {
				a.ImagePath = &path
				cols = append(cols, "imagePath")
			}
		}
	}
	if found.Description != "" && db.Val(a.Description) != found.Description {
		a.Description, updated = db.Ptr(found.Description), true
		cols = append(cols, "description")
	}
	if updated {
		if len(cols) > 0 {
			if err := h.save(ctx, &a, cols...); err != nil {
				return httpx.Internal("database write failed", err)
			}
		}
		if err := h.announce(ctx, a); err != nil {
			return httpx.Internal("database read failed", err)
		}
	}
	return httpx.JSON(w, r, result{Updated: &updated, Author: viewOf(a, nil)})
}
