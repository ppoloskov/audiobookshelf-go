package people

import (
	"audiobookshelf-compat/internal/httputil"
	"audiobookshelf-compat/internal/logging"
	"audiobookshelf-compat/internal/store"
	"audiobookshelf-compat/internal/wire"
	"encoding/json"
	"net/http"
	"regexp"
	"strings"
)

var asinPattern = regexp.MustCompile(`^[A-Z0-9]{10}$`)

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

// authorMatch is AuthorController.match (AuthorController.js:337-383).
func (h *Handler) authorMatch(w http.ResponseWriter, r *http.Request) error {
	a, err := AuthorRequest(r.Context(), h.db, r, store.RequestUser(r))
	if err != nil {
		return err
	}
	ctx := r.Context()
	body := httputil.DecodeObjectFields(r)
	bodyString := func(key string) string {
		var s string
		_ = json.Unmarshal(body[key], &s)
		return s
	}
	region := bodyString("region")
	if region == "" {
		region = "us"
	}
	var data *wire.AuthorMatch
	if h.deps.Authors != nil {
		if asin := bodyString("asin"); asin != "" && asinPattern.MatchString(strings.ToUpper(asin)) {
			data = h.deps.Authors.FindAuthorByASIN(ctx, asin, region)
		} else {
			data = h.deps.Authors.FindAuthorByName(ctx, bodyString("q"), region)
		}
	}
	if data == nil {
		return httputil.Status(http.StatusNotFound, "Author not found")
	}
	logging.Debugf(`[AuthorController] match author with "%s"`, firstNonEmpty(bodyString("q"), bodyString("asin")))
	update := &AuthorColumnUpdate{}
	hasUpdates := false
	if data.ASIN != "" && (!a.ASIN.Valid || a.ASIN.String != data.ASIN) {
		asin := data.ASIN
		update.Change("asin", store.NullStringPtr(a.ASIN), &asin)
		hasUpdates = true
	}
	// Only updates the image if there was none before or the ASIN was updated.
	if data.Image != nil && *data.Image != "" && (!a.ImagePath.Valid || a.ImagePath.String == "" || hasUpdates) {
		h.purgeImageCache(a.ID)
		if imagePath, message := h.saveAuthorImage(ctx, a.ID, *data.Image); message == "" {
			update.Change("imagePath", store.NullStringPtr(a.ImagePath), &imagePath)
			hasUpdates = true
		}
	}
	if data.Description != "" && (!a.Description.Valid || a.Description.String != data.Description) {
		description := data.Description
		update.Change("description", store.NullStringPtr(a.Description), &description)
		hasUpdates = true
	}
	if hasUpdates {
		if err := update.Save(ctx, h.db, a.ID); err != nil {
			return httputil.Internal("database write failed", err)
		}
		expanded, err := AuthorExpanded(ctx, h.db, a.ID)
		if err != nil {
			return httputil.Internal("database read failed", err)
		}
		h.emit.ToAll("author_updated", expanded)
	}
	saved, err := LoadAuthor(ctx, h.db, a.ID)
	if err != nil {
		return httputil.Internal("database read failed", err)
	}
	oldJSON, err := saved.ToOldJSON()
	if err != nil {
		return httputil.Internal("database read failed", err)
	}
	return httputil.JSON(w, r, AuthorMatchResult{Updated: hasUpdates, Author: oldJSON})
}
