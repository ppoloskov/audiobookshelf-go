package catalog

import (
	"audiobookshelf-compat/internal/dbtime"
	"audiobookshelf-compat/internal/httputil"
	"audiobookshelf-compat/internal/libitem"
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
func (h *Handler) personalized(w http.ResponseWriter, r *http.Request) error {
	user, libraryID := requestUser(r), r.PathValue("id")
	if !userCanAccessLibrary(user, libraryID) {
		return httputil.Status(http.StatusForbidden, "")
	}
	var mediaType string
	if err := h.db.QueryRowContext(r.Context(), `SELECT mediaType FROM libraries WHERE id=?`, libraryID).Scan(&mediaType); errors.Is(err, sql.ErrNoRows) {
		return httputil.Status(http.StatusNotFound, "Library not found")
	} else if err != nil {
		return httputil.Internal("database read failed", err)
	}
	limit := 10
	if raw := r.URL.Query().Get("limit"); raw != "" {
		v, err := strconv.Atoi(raw)
		if err != nil || v < 0 {
			return httputil.Status(http.StatusBadRequest, "invalid limit")
		}
		if v != 0 {
			limit = v
		}
	}
	if mediaType != "book" {
		if mediaType == "podcast" {
			return h.personalizedPodcasts(w, r, user, libraryID, limit)
		}
		return httputil.Status(http.StatusNotFound, "Library not found")
	}
	permission, permissionArgs := userMediaClause(user, "book")
	shelves := make([]wire.Shelf, 0, 5)
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
			return httputil.Internal("database read failed", err)
		}
		items, err := h.personalizedItems(r, user, spec.query+` LIMIT ?`, append(append([]any{}, args...), limit))
		if err != nil {
			return httputil.Internal("database read failed", err)
		}
		if spec.id == "continue-listening" || spec.id == "listen-again" {
			audio := make([]libitem.Minified, 0, len(items))
			reading := make([]libitem.Minified, 0, len(items))
			for _, item := range items {
				var tracks int
				var hasEbookFormat bool
				if media, ok := item.Media.(libitem.BookMinified); ok {
					tracks = media.NumTracks
					hasEbookFormat = len(media.EbookFormat) > 0 && string(media.EbookFormat) != "null"
				}
				if tracks > 0 {
					audio = append(audio, item)
				} else if hasEbookFormat {
					reading = append(reading, item)
				}
			}
			if len(audio) > 0 {
				id, label, key := spec.id, spec.label, spec.key
				if id == "listen-again" {
					id, label, key = "listen-again", "Listen Again", "LabelListenAgain"
				}
				shelves = append(shelves, wire.Shelf{ID: id, Label: label, LabelStringKey: key, Type: "book", Entities: audio, Total: total})
			}
			if len(reading) > 0 {
				id, label, key := spec.id, spec.label, spec.key
				if id == "continue-listening" {
					id, label, key = "continue-reading", "Continue Reading", "LabelContinueReading"
				} else {
					id, label, key = "read-again", "Read Again", "LabelReadAgain"
				}
				shelves = append(shelves, wire.Shelf{ID: id, Label: label, LabelStringKey: key, Type: "book", Entities: reading, Total: total})
			}
		} else if len(items) > 0 {
			shelves = append(shelves, wire.Shelf{ID: spec.id, Label: spec.label, LabelStringKey: spec.key, Type: "book", Entities: items, Total: total})
		}
		// Remaining book shelves, in LibraryItem.getPersonalizedShelves order
		// (server/models/LibraryItem.js:384-491).
		var shelfErr error
		switch spec.id {
		case "continue-listening":
			shelfErr = h.appendContinueSeries(r, user, libraryID, limit, &shelves)
		case "recently-added":
			shelfErr = h.appendRecentSeriesAndDiscover(r, user, libraryID, limit, &shelves)
		case "listen-again":
			shelfErr = h.appendNewestAuthors(r, user, libraryID, limit, &shelves)
		}
		if shelfErr != nil {
			return httputil.Internal("database read failed", shelfErr)
		}
	}
	httputil.WriteJSON(w, http.StatusOK, shelves, httputil.ForRequest(r))
	return nil
}

func (h *Handler) appendContinueSeries(r *http.Request, user store.User, libraryID string, limit int, shelves *[]wire.Shelf) error {
	items, count, err := h.continueSeriesShelf(r, user, libraryID, limit)
	if err != nil {
		return err
	}
	if len(items) > 0 {
		*shelves = append(*shelves, wire.Shelf{ID: "continue-series", Label: "Continue Series", LabelStringKey: "LabelContinueSeries", Type: "book", Entities: items, Total: count})
	}
	return nil
}

func (h *Handler) appendRecentSeriesAndDiscover(r *http.Request, user store.User, libraryID string, limit int, shelves *[]wire.Shelf) error {
	series, count, err := h.recentSeriesShelf(r, user, libraryID, includeHas(r, "rssfeed"))
	if err != nil {
		return err
	}
	if len(series) > 0 {
		*shelves = append(*shelves, wire.Shelf{ID: "recent-series", Label: "Recent Series", LabelStringKey: "LabelRecentSeries", Type: "series", Entities: series, Total: count})
	}
	items, count, err := h.discoverShelf(r, user, libraryID, limit)
	if err != nil {
		return err
	}
	if len(items) > 0 {
		*shelves = append(*shelves, wire.Shelf{ID: "discover", Label: "Discover", LabelStringKey: "LabelDiscover", Type: "book", Entities: items, Total: count})
	}
	return nil
}

func (h *Handler) appendNewestAuthors(r *http.Request, user store.User, libraryID string, limit int, shelves *[]wire.Shelf) error {
	authors, count, err := h.newestAuthorsShelf(r, user, libraryID, limit)
	if err != nil {
		return err
	}
	if len(authors) > 0 {
		*shelves = append(*shelves, wire.Shelf{ID: "newest-authors", Label: "Newest Authors", LabelStringKey: "LabelNewestAuthors", Type: "authors", Entities: authors, Total: count})
	}
	return nil
}

type episodeShelfRow struct {
	itemID  string
	episode libitem.EpisodeJSON
}

func (h *Handler) personalizedPodcasts(w http.ResponseWriter, r *http.Request, user store.User, libraryID string, limit int) error {
	permission, args := podcastPermissionClause(user)
	cutoff := dbtime.Format(time.Now().Add(-60 * 24 * time.Hour))
	shelves := make([]wire.Shelf, 0, 3)
	var listenAgain *wire.Shelf
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
			return httputil.Internal("database read failed", err)
		}
		rows, err := h.db.QueryContext(r.Context(), `SELECT li.id,ep.id,ep."index",ep.season,ep.episode,ep.episodeType,ep.title,ep.subtitle,ep.description,ep.pubDate,ep.enclosureURL,ep.enclosureSize,ep.enclosureType,ep.publishedAt,ep.audioFile,ep.chapters,ep.extraData,ep.createdAt,ep.updatedAt`+base+` ORDER BY `+map[bool]string{true: "mp.updatedAt", false: "ep.createdAt"}[spec.progress]+` DESC LIMIT ?`, append(queryArgs, limit)...)
		if err != nil {
			return httputil.Internal("database read failed", err)
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
				return httputil.Internal("database read failed", err)
			}
			pub, e := dbtime.OptionalMillis(published)
			if e != nil {
				rows.Close()
				return httputil.Internal("database read failed", e)
			}
			createdMs, e := dbtime.OptionalMillis(created)
			if e != nil {
				rows.Close()
				return httputil.Internal("database read failed", e)
			}
			updatedMs, e := dbtime.OptionalMillis(updated)
			if e != nil {
				rows.Close()
				return httputil.Internal("database read failed", e)
			}
			var enclosure *libitem.Enclosure
			if enclosureURL.Valid {
				enclosure = &libitem.Enclosure{URL: enclosureURL.String, Type: store.NullStringPtr(enclosureType)}
				if enclosureSize.Valid {
					length := strconv.FormatInt(enclosureSize.Int64, 10)
					enclosure.Length = &length
				}
			}
			audio := decodeObject(audioRaw)
			extra := decodeObject(extraRaw)
			var episodeIndex *int64
			if index.Valid {
				episodeIndex = &index.Int64
			}
			episodeJSON := libitem.EpisodeJSON{LibraryItemID: itemID, PodcastID: stringValue(audio["podcastId"]), ID: episodeID,
				OldEpisodeID: nonEmpty(stringValue(extra["oldEpisodeId"])), Index: episodeIndex, Season: store.NullStringPtr(season),
				Episode: store.NullStringPtr(episode), EpisodeType: store.NullStringPtr(episodeType), Title: store.NullStringPtr(title),
				Subtitle: store.NullStringPtr(subtitle), Description: store.NullStringPtr(description), Enclosure: enclosure,
				GUID: nonEmpty(stringValue(extra["guid"])), PubDate: store.NullStringPtr(pubDate), Chapters: decodeArray(chaptersRaw),
				AudioFile: audio, PublishedAt: pub, AddedAt: createdMs, UpdatedAt: updatedMs}
			records = append(records, episodeShelfRow{itemID, episodeJSON})
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return httputil.Internal("database read failed", err)
		}
		rows.Close()
		recordIDs := make([]string, len(records))
		for i, record := range records {
			recordIDs[i] = record.itemID
		}
		entities, e := h.minifiedItems(r.Context(), recordIDs)
		if e != nil {
			return httputil.Internal("database read failed", e)
		}
		for i := range entities {
			records[i].episode.PodcastID = entities[i].MediaID()
			entities[i].RecentEpisode = records[i].episode
		}
		if len(entities) > 0 {
			shelf := wire.Shelf{ID: spec.id, Label: spec.label, LabelStringKey: spec.key, Type: spec.kind, Entities: entities, Total: total}
			if spec.id == "listen-again" {
				listenAgain = &shelf
			} else {
				shelves = append(shelves, shelf)
			}
		}
	}
	// Recently-added podcasts are parent items, separate from episode shelves.
	var total int
	if err := h.db.QueryRowContext(r.Context(), `SELECT COUNT(*) FROM podcasts p JOIN libraryItems li ON li.mediaId=p.id WHERE li.libraryId=? AND li.createdAt>=?`+permission, append([]any{libraryID, cutoff}, args...)...).Scan(&total); err != nil {
		return httputil.Internal("database read failed", err)
	}
	ids, err := h.db.QueryContext(r.Context(), `SELECT li.id FROM podcasts p JOIN libraryItems li ON li.mediaId=p.id WHERE li.libraryId=? AND li.createdAt>=?`+permission+` ORDER BY li.createdAt DESC LIMIT ?`, append(append([]any{libraryID, cutoff}, args...), limit)...)
	if err != nil {
		return httputil.Internal("database read failed", err)
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
		return httputil.Internal("database read failed", err)
	}
	ids.Close()
	entities, err := h.minifiedItems(r.Context(), itemIDs)
	if err != nil {
		return httputil.Internal("database read failed", err)
	}
	for i := range entities {
		item := &entities[i]
		if e := h.attachItemIncludes(r.Context(), requestItemIncludes(r, user), item.Head, item.MediaID(), &item.Extras); e != nil {
			return httputil.Internal("database read failed", e)
		}
		// LibraryFilters.getLibraryItemsMostRecentlyAdded podcast branch
		// (server/utils/queries/libraryFilters.js:113-128).
		media, _ := item.Media.(libitem.PodcastMedia)
		if item.Size != nil && *item.Size != 0 && (media.Size == nil || *media.Size == 0) {
			size := *item.Size
			media.Size = &size
			item.Media = media
		}
		if includeHas(r, "numepisodesincomplete") {
			incomplete, incErr := h.numEpisodesIncomplete(r.Context(), user.ID, media.ID)
			if incErr != nil {
				return httputil.Internal("database read failed", incErr)
			}
			item.NumEpisodesIncomplete = incomplete
		}
	}
	if len(entities) > 0 {
		shelves = append(shelves, wire.Shelf{ID: "recently-added", Label: "Recently Added", LabelStringKey: "LabelRecentlyAdded", Type: "podcast", Entities: entities, Total: total})
	}
	if listenAgain != nil {
		shelves = append(shelves, *listenAgain)
	}
	httputil.WriteJSON(w, 200, shelves, httputil.ForRequest(r))
	return nil
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

func (h *Handler) personalizedItems(r *http.Request, user store.User, query string, args []any) ([]libitem.Minified, error) {
	rows, err := h.db.QueryContext(r.Context(), query, args...)
	if err != nil {
		return nil, err
	}
	ids := make([]string, 0)
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			_ = rows.Close()
			return nil, err
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}

	items, err := h.minifiedItems(r.Context(), ids)
	if err != nil {
		return nil, err
	}
	includes := requestItemIncludes(r, user)
	for i := range items {
		item := &items[i]
		if err := h.attachItemIncludes(r.Context(), includes, item.Head, item.MediaID(), &item.Extras); err != nil {
			return nil, err
		}
	}
	return items, nil
}
