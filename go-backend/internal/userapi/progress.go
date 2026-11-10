package userapi

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"sort"

	"audiobookshelf-compat/internal/access"
	"audiobookshelf-compat/internal/db/sqlc"
	"audiobookshelf-compat/internal/dbtime"
	"audiobookshelf-compat/internal/httputil"
	"audiobookshelf-compat/internal/items"
	"audiobookshelf-compat/internal/jsonvalue"
	"audiobookshelf-compat/internal/podcasts/read"
	"audiobookshelf-compat/internal/progressdto"
	"audiobookshelf-compat/internal/serversettings"
	"audiobookshelf-compat/internal/store"
	"audiobookshelf-compat/internal/wire"
)

func (h *Handler) getAllProgress(w http.ResponseWriter, r *http.Request, user store.User) error {
	progress, err := progressdto.Read(r.Context(), h.db, user.ID)
	if err != nil {
		return httputil.Internal("database read failed", err)
	}
	return httputil.JSON(w, r, map[string]any{"mediaProgress": progress})
}

func (h *Handler) removeFromContinueListening(w http.ResponseWriter, r *http.Request, user store.User) error {
	res, err := sqlc.New(h.db).HideMediaProgressFromContinueListening(r.Context(), sqlc.HideMediaProgressFromContinueListeningParams{UpdatedAt: dbtime.Now(), ID: r.PathValue("id"), UserID: store.Text(user.ID)})
	if err != nil {
		return httputil.Internal("database write failed", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return httputil.Status(404, "")
	}
	h.emitUserUpdate(r.Context(), user)
	payload, err := h.userPayload(r.Context(), user)
	if err != nil {
		return httputil.Internal("database read failed", err)
	}
	return httputil.JSON(w, r, payload)
}

func (h *Handler) getProgress(w http.ResponseWriter, r *http.Request, user store.User) error {
	p, err := progressdto.ReadOne(r.Context(), h.db, user.ID, r.PathValue("id"), r.PathValue("episodeId"))
	if err != nil {
		return httputil.Internal("database read failed", err)
	}
	if p == nil {
		return httputil.Status(404, "")
	}
	return httputil.JSON(w, r, p)
}

func (h *Handler) deleteProgress(w http.ResponseWriter, r *http.Request, user store.User) error {
	res, err := sqlc.New(h.db).DeleteMediaProgressForUser(r.Context(), sqlc.DeleteMediaProgressForUserParams{ID: r.PathValue("id"), UserID: store.Text(user.ID)})
	if err != nil {
		return httputil.Internal("database write failed", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return httputil.Status(404, "")
	}
	return httputil.OK(w, r)
}

func (h *Handler) batchUpdateProgress(w http.ResponseWriter, r *http.Request, user store.User) error {
	var batch []progressdto.Payload
	defer r.Body.Close()
	if err := json.NewDecoder(r.Body).Decode(&batch); err != nil || len(batch) == 0 {
		return httputil.Status(400, "Missing request payload")
	}
	updated := false
	for _, payload := range batch {
		if _, err := h.updateProgress(r.Context(), user, payload); err == nil {
			updated = true
		}
	}
	if updated {
		h.emitUserUpdate(r.Context(), user)
	}
	return httputil.OK(w, r)
}

func (h *Handler) updateOneProgress(w http.ResponseWriter, r *http.Request, user store.User) error {
	// MeController.createUpdateMediaProgress (MeController.js:277): the
	// body is spread first, so the URL's libraryItemId and (possibly
	// absent) episodeId always win.
	var payload progressdto.Payload
	defer r.Body.Close()
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		return httputil.Status(400, "Invalid request body")
	}
	payload.LibraryItemID, payload.EpisodeID = r.PathValue("id"), r.PathValue("episodeId")
	if _, err := h.updateProgress(r.Context(), user, payload); err != nil {
		var refused *progressdto.StatusError
		if errors.As(err, &refused) {
			// Express res.status(n).send(string): text/html, no trailing newline.
			return httputil.Status(refused.Status, refused.Message)
		}
		return httputil.Internal("database write failed", err)
	}
	h.emitUserUpdate(r.Context(), user)
	return httputil.OK(w, r)
}

// updateProgress applies one progress payload for the user
// (User.createUpdateMediaProgressFromPayload, server/models/User.js:728). The
// library access check is stricter than upstream, which has none here.
func (h *Handler) updateProgress(ctx context.Context, user store.User, p progressdto.Payload) (wire.MediaProgress, error) {
	perms := access.For(ctx, user)
	return progressdto.ApplyChecked(ctx, h.db, user.ID, p, perms.Library)
}

// adminProgress is a progress with the media fields UserController.findOne
// attaches (server/controllers/UserController.js:86-98): displayTitle, plus
// displaySubtitle for episodes, coverPath and mediaUpdatedAt (a JS Date, so an
// ISO string). A nil RawMessage leaves the key out (no media row); the JSON
// text null keeps it as null.

type adminProgress struct {
	wire.MediaProgress
	DisplayTitle    json.RawMessage `json:"displayTitle,omitempty"`
	DisplaySubtitle json.RawMessage `json:"displaySubtitle,omitempty"`
	CoverPath       json.RawMessage `json:"coverPath,omitempty"`
	MediaUpdatedAt  json.RawMessage `json:"mediaUpdatedAt,omitempty"`
}

func jsonText(v any) json.RawMessage {
	data, _ := json.Marshal(v)
	return data
}

func (h *Handler) decorateAdminProgress(ctx context.Context, progress []wire.MediaProgress) ([]adminProgress, error) {
	out := make([]adminProgress, 0, len(progress))
	for _, p := range progress {
		d := adminProgress{MediaProgress: p}
		var title, subtitle, cover sql.NullString
		var updated any
		var err error
		episode := p.MediaItemType == "podcastEpisode"
		if episode {
			err = h.db.QueryRowContext(ctx, `SELECT pe.title, po.title, po.coverPath, po.updatedAt FROM podcastEpisodes pe LEFT JOIN podcasts po ON po.id = pe.podcastId WHERE pe.id = ?`, p.MediaItemID).Scan(&title, &subtitle, &cover, &updated)
		} else {
			err = h.db.QueryRowContext(ctx, `SELECT title, NULL, coverPath, updatedAt FROM books WHERE id = ?`, p.MediaItemID).Scan(&title, &subtitle, &cover, &updated)
		}
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return nil, err
		}
		if err == nil {
			d.DisplayTitle, d.CoverPath, d.MediaUpdatedAt = jsonText(store.NullString(title)), jsonText(store.NullString(cover)), jsonText(nil)
			if episode {
				d.DisplaySubtitle = jsonText(store.NullString(subtitle))
			}
			if t := dbtime.Time(updated); !t.IsZero() {
				d.MediaUpdatedAt = jsonText(dbtime.JSDate(t))
			}
		}
		out = append(out, d)
	}
	return out, nil
}

func (h *Handler) updateSeriesContinueListening(w http.ResponseWriter, r *http.Request, user store.User) error {
	seriesID, action := r.PathValue("id"), r.PathValue("action")
	if action != "remove-from-continue-listening" && action != "readd-to-continue-listening" {
		return httputil.Status(404, "")
	}
	var exists int
	if err := h.db.QueryRowContext(r.Context(), `SELECT COUNT(*) FROM series WHERE id=?`, seriesID).Scan(&exists); err != nil {
		return httputil.Internal("database read failed", err)
	}
	if exists == 0 {
		return httputil.Status(404, "")
	}
	var extra map[string]any
	_ = json.Unmarshal(user.ExtraData, &extra)
	if extra == nil {
		extra = map[string]any{}
	}
	ids := jsonvalue.Strings(extra["seriesHideFromContinueListening"])
	filtered := ids
	if action == "remove-from-continue-listening" && !slices.Contains(ids, seriesID) {
		filtered = append(filtered, seriesID)
	} else if action == "readd-to-continue-listening" {
		filtered = slices.DeleteFunc(filtered, func(id string) bool { return id == seriesID })
	}
	changed := len(filtered) != len(ids)
	if changed {
		extra["seriesHideFromContinueListening"] = filtered
		raw, _ := json.Marshal(extra)
		if _, err := h.db.ExecContext(r.Context(), `UPDATE users SET extraData=?,updatedAt=? WHERE id=?`, string(raw), dbtime.Now(), user.ID); err != nil {
			return httputil.Internal("database write failed", err)
		}
		user.ExtraData = raw
		h.emitUserUpdate(r.Context(), user)
	}
	payload, err := h.userPayload(r.Context(), user)
	if err != nil {
		return httputil.Internal("database read failed", err)
	}
	return httputil.JSON(w, r, payload)
}

func (h *Handler) getItemsInProgress(w http.ResponseWriter, r *http.Request, user store.User) error {
	limit := queryInt(r, "limit", 25)
	if limit == 0 {
		limit = 25
	}
	rows, err := sqlc.New(h.db).ListItemsInProgress(r.Context(), store.Text(user.ID))
	if err != nil {
		return httputil.Internal("database read failed", err)
	}
	type progress struct {
		itemID, typ, mediaID string
		updated              any
	}
	entries := make([]progress, 0, len(rows))
	var itemIDs []string
	for _, row := range rows {
		var data map[string]any
		_ = json.Unmarshal([]byte(row.Extradata.String), &data)
		itemID, _ := data["libraryItemId"].(string)
		if itemID == "" {
			continue
		}
		entries = append(entries, progress{itemID: itemID, typ: row.Mediaitemtype.String, mediaID: row.Mediaitemid.String, updated: row.Updatedat})
		itemIDs = append(itemIDs, itemID)
	}
	loaded, err := items.Load(r.Context(), h.db, itemIDs, items.Options{Episodes: true})
	if err != nil {
		return httputil.Internal("database read failed", err)
	}
	byID := make(map[string]items.Item, len(loaded))
	for _, it := range loaded {
		byID[it.ID] = it
	}
	prefixes := serversettings.Load(r.Context(), h.db).SortingPrefixes()
	perms := access.For(r.Context(), user)
	result := make([]items.Minified, 0)
	for _, p := range entries {
		it, ok := byID[p.itemID]
		if !ok || !perms.Item(it.Access()) {
			continue
		}
		li := it.Minified(prefixes)
		li.ProgressLastUpdate, _ = dbtime.Millis(p.updated).(int64)
		if media, ok := li.Media.(podcastread.PodcastMedia); ok && p.typ == "podcastEpisode" {
			// findAllExpandedWhere loads the episodes, so the minified media
			// reports their count and total size (MeController.js:490).
			loadedMedia, _ := it.Expanded(prefixes).Media.(podcastread.PodcastMedia)
			media.NumEpisodes, media.Size = loadedMedia.NumEpisodes, loadedMedia.Size
			li.Media = media
			// Upstream sends episode.toOldJSON, not the expanded form
			// (MeController.js getAllLibraryItemsInProgress).
			if i := slices.IndexFunc(it.Podcast.Episodes, func(e podcastread.Episode) bool { return e.ID == p.mediaID }); i >= 0 {
				li.RecentEpisode = it.Podcast.Episodes[i].JSON(it.ID)
			}
		}
		result = append(result, li)
	}
	sort.SliceStable(result, func(i, j int) bool { return result[i].ProgressLastUpdate > result[j].ProgressLastUpdate })
	if limit < 0 {
		limit = 25
	}
	if len(result) > limit {
		result = result[:limit]
	}
	return httputil.JSON(w, r, map[string]any{"libraryItems": result})
}
