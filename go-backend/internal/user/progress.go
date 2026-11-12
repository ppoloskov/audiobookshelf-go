package user

import (
	"cmp"
	"context"
	"net/http"
	"slices"

	"audiobookshelf-compat/internal/auth"
	"audiobookshelf-compat/internal/db"
	"audiobookshelf-compat/internal/httpx"
	"audiobookshelf-compat/internal/item"
	"audiobookshelf-compat/internal/playback"
)

func (h *Handler) allProgress(w http.ResponseWriter, r *http.Request, me *auth.Principal) error {
	progress, err := h.playback.Progress().Read(r.Context(), me.ID)
	if err != nil {
		return err
	}
	return httpx.JSON(w, r, wrap("mediaProgress", progress))
}

func (h *Handler) getProgress(w http.ResponseWriter, r *http.Request, me *auth.Principal) error {
	progress, err := h.playback.Progress().ReadOne(r.Context(), me.ID, r.PathValue("id"), r.PathValue("episodeId"))
	if err != nil {
		return err
	}
	if progress == nil {
		return httpx.NotFound()
	}
	return httpx.JSON(w, r, progress)
}

// hideProgress hides the progress row id from continue listening and answers the account.
func (h *Handler) hideProgress(w http.ResponseWriter, r *http.Request, me *auth.Principal) error {
	result, err := h.db.Exec(r.Context(), `UPDATE mediaProgresses SET hideFromContinueListening = 1, updatedAt = ? WHERE id = ? AND userId = ?`,
		db.Now(), r.PathValue("id"), me.ID)
	if err != nil {
		return err
	}
	if n, _ := result.RowsAffected(); n == 0 { // sqlite always reports it
		return httpx.NotFound()
	}
	view, err := h.emitUserUpdate(r.Context(), me.User)
	if err != nil {
		return err
	}
	return httpx.JSON(w, r, view)
}

func (h *Handler) deleteProgress(w http.ResponseWriter, r *http.Request, me *auth.Principal) error {
	result, err := h.db.Exec(r.Context(), `DELETE FROM mediaProgresses WHERE id = ? AND userId = ?`, r.PathValue("id"), me.ID)
	if err != nil {
		return err
	}
	if n, _ := result.RowsAffected(); n == 0 { // sqlite always reports it
		return httpx.NotFound()
	}
	return ok(w)
}

// batchUpdateProgress is PATCH /api/me/progress/batch/update; a refused update is skipped.
func (h *Handler) batchUpdateProgress(w http.ResponseWriter, r *http.Request, me *auth.Principal) error {
	batch, err := httpx.Decode[[]playback.Update](w, r)
	if err != nil || len(batch) == 0 {
		return httpx.BadRequest("Missing request payload")
	}
	updated := false
	for _, update := range batch {
		if _, err := h.playback.Progress().Apply(r.Context(), me, update); err == nil {
			updated = true
		}
	}
	if updated {
		if _, err := h.emitUserUpdate(r.Context(), me.User); err != nil {
			return err
		}
	}
	return ok(w)
}

// updateProgress is PATCH /api/me/progress/{id}[/{episodeId}]; the URL names the item and episode.
func (h *Handler) updateProgress(w http.ResponseWriter, r *http.Request, me *auth.Principal) error {
	update, err := httpx.Decode[playback.Update](w, r)
	if err != nil {
		return err
	}
	update.LibraryItemID, update.EpisodeID = r.PathValue("id"), r.PathValue("episodeId")
	if _, err := h.playback.Progress().Apply(r.Context(), me, update); err != nil {
		return err
	}
	if _, err := h.emitUserUpdate(r.Context(), me.User); err != nil {
		return err
	}
	return ok(w)
}

// seriesContinueListening is GET /api/me/series/{id}/{action}: hide or show a series; answers the account.
func (h *Handler) seriesContinueListening(w http.ResponseWriter, r *http.Request, me *auth.Principal) error {
	seriesID, action := r.PathValue("id"), r.PathValue("action")
	hide := action == "remove-from-continue-listening"
	if !hide && action != "readd-to-continue-listening" {
		return httpx.NotFound()
	}
	var found int
	if err := h.db.Get(r.Context(), &found, `SELECT COUNT(*) FROM series WHERE id = ?`, seriesID); err != nil {
		return err
	}
	if found == 0 {
		return httpx.NotFound()
	}
	user, changed, err := h.editUser(r.Context(), me.ID, func(_ context.Context, u *db.User) error {
		if u.ExtraData.V == nil {
			u.ExtraData.V = &db.UserExtra{}
		}
		hidden := &u.ExtraData.V.SeriesHideFromContinueListening
		if hide == slices.Contains(*hidden, seriesID) {
			return errUnchanged
		}
		if hide {
			*hidden = append(*hidden, seriesID)
		} else {
			*hidden = slices.DeleteFunc(*hidden, func(id string) bool { return id == seriesID })
		}
		return nil
	}, "extraData")
	if err != nil {
		return err
	}
	view, err := h.auth.ViewWithProgress(r.Context(), user)
	if err != nil {
		return err
	}
	if changed {
		h.emit.ToUser(user.ID, "user_updated", view)
	}
	return httpx.JSON(w, r, view)
}

// itemsInProgress is GET /api/me/items-in-progress: unfinished items, most recent first, at most limit.
func (h *Handler) itemsInProgress(w http.ResponseWriter, r *http.Request, me *auth.Principal) error {
	ctx := r.Context()
	limit := httpx.QueryInt(r, "limit", 25)
	if limit <= 0 {
		limit = 25
	}
	var rows []db.MediaProgress
	if err := h.db.Select(ctx, &rows, `SELECT * FROM mediaProgresses WHERE userId = ? AND isFinished = 0 AND (currentTime > 0 OR ebookProgress > 0) ORDER BY updatedAt DESC, rowid`, me.ID); err != nil {
		return err
	}
	itemOf := func(row db.MediaProgress) string { // "" when the row names no item
		if extra := row.ExtraData.V; extra != nil {
			return db.Val(extra.LibraryItemID)
		}
		return ""
	}
	var itemIDs []string
	for _, row := range rows {
		itemIDs = append(itemIDs, itemOf(row))
	}
	loaded, err := item.Load(ctx, h.db, itemIDs, item.Options{Episodes: true})
	if err != nil {
		return err
	}
	byID := make(map[string]item.Item, len(loaded))
	for _, it := range loaded {
		byID[it.ID] = it
	}
	prefixes := h.settings.Server().SortingPrefixes
	result := []item.Minified{}
	for _, row := range rows {
		it, found := byID[itemOf(row)]
		if a := it.Access(); !found || !me.CanAccessItem(a.LibraryID, a.Explicit, a.Tags) {
			continue
		}
		view := it.Minified(prefixes)
		view.ProgressLastUpdate = row.UpdatedAt.Millis()
		if media, ok := view.Media.(item.PodcastMinified); ok && db.Val(row.MediaItemType) == "podcastEpisode" {
			// findAllExpandedWhere loads the episodes, so the minified media
			// reports their count and total size (MeController.js:490).
			expanded, _ := it.Expanded(prefixes).Media.(item.PodcastExpanded)
			media.NumEpisodes, media.Size = expanded.NumEpisodes, expanded.Size
			view.Media = media
			// Upstream sends episode.toOldJSON, not the expanded form.
			if i := slices.IndexFunc(it.Podcast.Episodes, func(e item.Episode) bool { return e.ID == db.Val(row.MediaItemID) }); i >= 0 {
				view.RecentEpisode = it.Podcast.Episodes[i].Full(it.ID)
			}
		}
		result = append(result, view)
	}
	slices.SortStableFunc(result, func(a, b item.Minified) int { return cmp.Compare(b.ProgressLastUpdate, a.ProgressLastUpdate) })
	return httpx.JSON(w, r, wrap("libraryItems", result[:min(len(result), limit)]))
}
