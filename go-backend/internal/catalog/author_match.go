package catalog

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

// authorMatch is AuthorController.match (AuthorController.js:337-383).
func (h *Handler) authorMatch(w http.ResponseWriter, r *http.Request) error {
	a, err := h.authorRequest(r, store.RequestUser(r))
	if err != nil {
		return err
	}
	ctx := r.Context()
	body := jsonBodyObject(r)
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
	update := &authorColumnUpdate{}
	hasUpdates := false
	if data.ASIN != "" && (!a.asin.Valid || a.asin.String != data.ASIN) {
		asin := data.ASIN
		update.set("asin", store.NullStringPtr(a.asin), &asin)
		hasUpdates = true
	}
	// Only updates the image if there was none before or the ASIN was updated.
	if data.Image != nil && *data.Image != "" && (!a.imagePath.Valid || a.imagePath.String == "" || hasUpdates) {
		h.purgeImageCache(a.id)
		if imagePath, message := h.saveAuthorImage(ctx, a.id, *data.Image); message == "" {
			update.set("imagePath", store.NullStringPtr(a.imagePath), &imagePath)
			hasUpdates = true
		}
	}
	if data.Description != "" && (!a.description.Valid || a.description.String != data.Description) {
		description := data.Description
		update.set("description", store.NullStringPtr(a.description), &description)
		hasUpdates = true
	}
	if hasUpdates {
		if err := update.save(ctx, h.db, a.id); err != nil {
			return httputil.Internal("database write failed", err)
		}
		expanded, err := h.authorExpanded(ctx, a.id)
		if err != nil {
			return httputil.Internal("database read failed", err)
		}
		h.emit.ToAll("author_updated", expanded)
	}
	saved, err := h.loadAuthor(ctx, a.id)
	if err != nil {
		return httputil.Internal("database read failed", err)
	}
	oldJSON, err := saved.toOldJSON()
	if err != nil {
		return httputil.Internal("database read failed", err)
	}
	return httputil.JSON(w, r, authorMatchResult{Updated: hasUpdates, Author: oldJSON})
}
