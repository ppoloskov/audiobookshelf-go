package catalog

import (
	"audiobookshelf-compat/internal/dbtime"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"audiobookshelf-compat/internal/store"
	"audiobookshelf-compat/internal/wire"
)

// personalized implements the book shelves whose upstream source is
// LibraryItem.getPersonalizedShelves / libraryFilters. Shelves are omitted
// when they have no visible entities, as in the reference controller.
func (h *Handler) personalized(w http.ResponseWriter, r *http.Request, user store.User, libraryID string) {
	if !userCanAccessLibrary(user, libraryID) {
		writeError(w, http.StatusForbidden, "")
		return
	}
	var mediaType string
	if err := h.db.QueryRowContext(r.Context(), `SELECT mediaType FROM libraries WHERE id=?`, libraryID).Scan(&mediaType); errors.Is(err, sql.ErrNoRows) {
		writeError(w, http.StatusNotFound, "Library not found")
		return
	} else if err != nil {
		writeError(w, http.StatusInternalServerError, "database read failed")
		return
	}
	limit := 10
	if raw := r.URL.Query().Get("limit"); raw != "" {
		v, err := strconv.Atoi(raw)
		if err != nil || v < 0 {
			writeError(w, http.StatusBadRequest, "invalid limit")
			return
		}
		if v != 0 {
			limit = v
		}
	}
	if mediaType != "book" {
		if mediaType == "podcast" {
			h.personalizedPodcasts(w, r, user, libraryID, limit)
			return
		}
		writeError(w, http.StatusNotFound, "Library not found")
		return
	}
	permission, permissionArgs := userMediaClause(user, "book")
	shelves := make([]map[string]any, 0, 5)
	for _, spec := range []struct {
		id, label, key, query string
	}{
		{"continue-listening", "Continue Listening", "LabelContinueListening", `SELECT li.id FROM libraryItems li JOIN books b ON b.id=li.mediaId JOIN mediaProgresses mp ON mp.mediaItemId=b.id WHERE li.libraryId=? AND li.mediaType='book' AND mp.userId=? AND mp.hideFromContinueListening=0 AND mp.isFinished=0 AND (mp.currentTime>0 OR mp.ebookProgress>0)` + permission + ` ORDER BY mp.updatedAt DESC`},
		{"recently-added", "Recently Added", "LabelRecentlyAdded", `SELECT li.id FROM libraryItems li JOIN books b ON b.id=li.mediaId WHERE li.libraryId=? AND li.mediaType='book' AND li.createdAt>=?` + permission + ` ORDER BY li.createdAt DESC`},
		{"listen-again", "Listen Again", "LabelListenAgain", `SELECT li.id FROM libraryItems li JOIN books b ON b.id=li.mediaId JOIN mediaProgresses mp ON mp.mediaItemId=b.id WHERE li.libraryId=? AND li.mediaType='book' AND mp.userId=? AND mp.isFinished=1` + permission + ` ORDER BY mp.updatedAt DESC`},
	} {
		// The progress shelves have the same user scope and permission clauses;
		// recent items do not have a progress user argument.
		args := []any{libraryID}
		if spec.id == "continue-listening" || spec.id == "listen-again" {
			args = append(args, user.ID)
		} else {
			args = append(args, dbtime.Format(time.Now().Add(-60*24*time.Hour)))
		}
		args = append(args, permissionArgs...)
		totalQuery := "SELECT COUNT(*) FROM (" + spec.query + ")"
		var total int
		if err := h.db.QueryRowContext(r.Context(), totalQuery, args...).Scan(&total); err != nil {
			writeError(w, http.StatusInternalServerError, "database read failed")
			return
		}
		items, err := h.personalizedItems(r, spec.query+` LIMIT ?`, append(append([]any{}, args...), limit))
		if err != nil {
			writeError(w, http.StatusInternalServerError, "database read failed")
			return
		}
		if spec.id == "continue-listening" || spec.id == "listen-again" {
			audio := make([]map[string]any, 0, len(items))
			reading := make([]map[string]any, 0, len(items))
			for _, item := range items {
				var tracks int
				var ebookFormat any
				switch media := item["media"].(type) {
				case map[string]any:
					tracks = int(number(media["numTracks"]))
					ebookFormat = media["ebookFormat"]
				case wire.BookMinified:
					tracks = media.NumTracks
					if len(media.EbookFormat) > 0 && string(media.EbookFormat) != "null" {
						ebookFormat = media.EbookFormat
					}
				}
				if tracks > 0 {
					audio = append(audio, item)
				} else if ebookFormat != nil {
					reading = append(reading, item)
				}
			}
			if len(audio) > 0 {
				id, label, key := spec.id, spec.label, spec.key
				if id == "listen-again" {
					id, label, key = "listen-again", "Listen Again", "LabelListenAgain"
				}
				shelves = append(shelves, map[string]any{"id": id, "label": label, "labelStringKey": key, "type": "book", "entities": audio, "total": total})
			}
			if len(reading) > 0 {
				id, label, key := spec.id, spec.label, spec.key
				if id == "continue-listening" {
					id, label, key = "continue-reading", "Continue Reading", "LabelContinueReading"
				} else {
					id, label, key = "read-again", "Read Again", "LabelReadAgain"
				}
				shelves = append(shelves, map[string]any{"id": id, "label": label, "labelStringKey": key, "type": "book", "entities": reading, "total": total})
			}
		} else if len(items) > 0 {
			shelves = append(shelves, map[string]any{"id": spec.id, "label": spec.label, "labelStringKey": spec.key, "type": "book", "entities": items, "total": total})
		}
	}
	writeJSON(w, r, http.StatusOK, shelves)
}

type episodeShelfRow struct {
	itemID  string
	episode map[string]any
}

func (h *Handler) personalizedPodcasts(w http.ResponseWriter, r *http.Request, user store.User, libraryID string, limit int) {
	permission, args := podcastPermissionClause(user)
	cutoff := dbtime.Format(time.Now().Add(-60 * 24 * time.Hour))
	shelves := make([]map[string]any, 0, 3)
	var listenAgain map[string]any
	for _, spec := range []struct {
		id, label, key, kind string
		filter               string
		progress             bool
	}{
		{"continue-listening", "Continue Listening", "LabelContinueListening", "episode", `mp.isFinished=0 AND mp.currentTime>0 AND mp.hideFromContinueListening=0`, true},
		{"newest-episodes", "Newest Episodes", "LabelNewestEpisodes", "episode", `ep.createdAt>=?`, false},
		{"listen-again", "Listen Again", "LabelListenAgain", "episode", `mp.isFinished=1`, true},
	} {
		where := spec.filter
		queryArgs := []any{libraryID}
		if spec.progress {
			queryArgs = append(queryArgs, user.ID)
		} else {
			queryArgs = append(queryArgs, cutoff)
		}
		queryArgs = append(queryArgs, args...)
		join := ``
		if spec.progress {
			join = ` JOIN mediaProgresses mp ON mp.mediaItemId=ep.id AND mp.userId=?`
		}
		base := ` FROM podcastEpisodes ep JOIN podcasts p ON p.id=ep.podcastId JOIN libraryItems li ON li.mediaId=p.id` + join + ` WHERE li.libraryId=? AND li.mediaType='podcast' AND ` + where + permission
		// Keep bind order aligned with the SQL text: join progress precedes the library predicate.
		if spec.progress {
			queryArgs = []any{user.ID, libraryID}
			queryArgs = append(queryArgs, args...)
		} else {
			queryArgs = []any{libraryID, cutoff}
			queryArgs = append(queryArgs, args...)
		}
		var total int
		if err := h.db.QueryRowContext(r.Context(), `SELECT COUNT(DISTINCT ep.id)`+base, queryArgs...).Scan(&total); err != nil {
			writeError(w, 500, "database read failed")
			return
		}
		rows, err := h.db.QueryContext(r.Context(), `SELECT li.id,ep.id,ep."index",ep.season,ep.episode,ep.episodeType,ep.title,ep.subtitle,ep.description,ep.pubDate,ep.enclosureURL,ep.enclosureSize,ep.enclosureType,ep.publishedAt,ep.audioFile,ep.chapters,ep.extraData,ep.createdAt,ep.updatedAt`+base+` ORDER BY `+map[bool]string{true: "mp.updatedAt", false: "ep.createdAt"}[spec.progress]+` DESC LIMIT ?`, append(queryArgs, limit)...)
		if err != nil {
			writeError(w, 500, "database read failed")
			return
		}
		records := make([]episodeShelfRow, 0)
		for rows.Next() {
			var itemID, episodeID string
			var index sql.NullInt64
			var season, episode, episodeType, title, subtitle, description, pubDate, enclosureURL, enclosureType sql.NullString
			var enclosureSize sql.NullInt64
			var published, created, updated any
			var audioRaw, chaptersRaw, extraRaw []byte
			if err := rows.Scan(&itemID, &episodeID, &index, &season, &episode, &episodeType, &title, &subtitle, &description, &pubDate, &enclosureURL, &enclosureSize, &enclosureType, &published, &audioRaw, &chaptersRaw, &extraRaw, &created, &updated); err != nil {
				rows.Close()
				writeError(w, 500, "database read failed")
				return
			}
			pub, e := dateMillis(published)
			if e != nil {
				rows.Close()
				writeError(w, 500, "database read failed")
				return
			}
			createdMs, e := dateMillis(created)
			if e != nil {
				rows.Close()
				writeError(w, 500, "database read failed")
				return
			}
			updatedMs, e := dateMillis(updated)
			if e != nil {
				rows.Close()
				writeError(w, 500, "database read failed")
				return
			}
			enclosure := any(nil)
			if enclosureURL.Valid {
				enclosure = map[string]any{"url": enclosureURL.String, "type": nullString(enclosureType), "length": func() any {
					if enclosureSize.Valid {
						return strconv.FormatInt(enclosureSize.Int64, 10)
					}
					return nil
				}()}
			}
			audio := decodeObject(audioRaw)
			extra := decodeObject(extraRaw)
			episodeJSON := map[string]any{"libraryItemId": itemID, "podcastId": stringValue(audio["podcastId"]), "id": episodeID, "oldEpisodeId": valueOrNull(stringValue(extra["oldEpisodeId"])), "index": nullInt(index), "season": nullString(season), "episode": nullString(episode), "episodeType": nullString(episodeType), "title": nullString(title), "subtitle": nullString(subtitle), "description": nullString(description), "enclosure": enclosure, "guid": valueOrNull(stringValue(extra["guid"])), "pubDate": nullString(pubDate), "chapters": decodeArray(chaptersRaw), "audioFile": audio, "publishedAt": deref(pub), "addedAt": deref(createdMs), "updatedAt": deref(updatedMs)}
			records = append(records, episodeShelfRow{itemID, episodeJSON})
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			writeError(w, 500, "database read failed")
			return
		}
		rows.Close()
		entities := make([]map[string]any, 0, len(records))
		for _, record := range records {
			item, e := h.ItemMinifiedJSON(r.Context(), record.itemID)
			if e != nil {
				writeError(w, 500, "database read failed")
				return
			}
			record.episode["podcastId"] = item["media"].(map[string]any)["id"]
			item["recentEpisode"] = record.episode
			entities = append(entities, item)
		}
		if len(entities) > 0 {
			shelf := map[string]any{"id": spec.id, "label": spec.label, "labelStringKey": spec.key, "type": spec.kind, "entities": entities, "total": total}
			if spec.id == "listen-again" {
				listenAgain = shelf
			} else {
				shelves = append(shelves, shelf)
			}
		}
	}
	// Recently-added podcasts are parent items, separate from episode shelves.
	var total int
	if err := h.db.QueryRowContext(r.Context(), `SELECT COUNT(*) FROM podcasts p JOIN libraryItems li ON li.mediaId=p.id WHERE li.libraryId=? AND li.createdAt>=?`+permission, append([]any{libraryID, cutoff}, args...)...).Scan(&total); err != nil {
		writeError(w, 500, "database read failed")
		return
	}
	ids, err := h.db.QueryContext(r.Context(), `SELECT li.id FROM podcasts p JOIN libraryItems li ON li.mediaId=p.id WHERE li.libraryId=? AND li.createdAt>=?`+permission+` ORDER BY li.createdAt DESC LIMIT ?`, append(append([]any{libraryID, cutoff}, args...), limit)...)
	if err != nil {
		writeError(w, 500, "database read failed")
		return
	}
	itemIDs := []string{}
	for ids.Next() {
		var id string
		if ids.Scan(&id) == nil {
			itemIDs = append(itemIDs, id)
		}
	}
	if err := ids.Err(); err != nil {
		ids.Close()
		writeError(w, 500, "database read failed")
		return
	}
	ids.Close()
	entities := make([]map[string]any, 0, len(itemIDs))
	for _, id := range itemIDs {
		item, e := h.ItemMinifiedJSON(r.Context(), id)
		if e != nil {
			writeError(w, 500, "database read failed")
			return
		}
		entities = append(entities, item)
	}
	if len(entities) > 0 {
		shelves = append(shelves, map[string]any{"id": "recently-added", "label": "Recently Added", "labelStringKey": "LabelRecentlyAdded", "type": "podcast", "entities": entities, "total": total})
	}
	if listenAgain != nil {
		shelves = append(shelves, listenAgain)
	}
	writeJSON(w, r, 200, shelves)
}

func podcastPermissionClause(user store.User) (string, []any) {
	var permissions map[string]any
	_ = json.Unmarshal(user.Permissions, &permissions)
	parts := []string{}
	args := []any{}
	if !user.IsActive || permissions["accessExplicitContent"] != true {
		parts = append(parts, " AND p.explicit=0")
	}
	if permissions["accessAllTags"] != true {
		selected, _ := permissions["itemTagsSelected"].([]any)
		if len(selected) > 0 {
			marks := strings.TrimRight(strings.Repeat("?,", len(selected)), ",")
			count := `(SELECT count(*) FROM json_each(p.tags) WHERE json_each.value IN (` + marks + `))`
			if permissions["selectedTagsNotAccessible"] == true {
				parts = append(parts, " AND "+count+"=0")
			} else {
				parts = append(parts, " AND "+count+">0")
			}
			args = append(args, selected...)
		}
	}
	return strings.Join(parts, ""), args
}

func (h *Handler) personalizedItems(r *http.Request, query string, args []any) ([]map[string]any, error) {
	rows, err := h.db.QueryContext(r.Context(), query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	ids := make([]string, 0)
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	items := make([]map[string]any, 0, len(ids))
	for _, id := range ids {
		item, err := h.ItemMinifiedJSON(r.Context(), id)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, nil
}
