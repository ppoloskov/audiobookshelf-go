package userapi

import (
	"database/sql"
	"encoding/json"
	"maps"
	"math"
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"audiobookshelf-compat/internal/db/sqlc"
	"audiobookshelf-compat/internal/dbtime"
	"audiobookshelf-compat/internal/httputil"
	"audiobookshelf-compat/internal/jsonvalue"

	"audiobookshelf-compat/internal/store"
)

func (h *Handler) getItemListeningSessions(w http.ResponseWriter, r *http.Request, user store.User) error {
	itemID, episodeID := r.PathValue("id"), r.PathValue("episodeId")
	access, err := h.canAccessItem(r.Context(), user, itemID)
	if err != nil {
		return writeInternalError(w, r, "database read failed", err)
	}
	if !access.exists {
		return httputil.Status(404, "")
	}
	var mediaID, mediaType string
	if err := h.db.QueryRowContext(r.Context(), `SELECT mediaId,mediaType FROM libraryItems WHERE id=?`, itemID).Scan(&mediaID, &mediaType); err != nil {
		return httputil.Status(404, "")
	}
	// MeController.getItemListeningSessions (MeController.js:185): the episode
	// is looked up by its id alone, a podcast without one is a 404 before the
	// access check, and an existing episode id replaces the media item id.
	episodeFound := false
	if episodeID != "" {
		var exists int
		if err := h.db.QueryRowContext(r.Context(), `SELECT COUNT(*) FROM podcastEpisodes WHERE id=?`, episodeID).Scan(&exists); err != nil {
			return writeInternalError(w, r, "database read failed", err)
		}
		episodeFound = exists > 0
	}
	if mediaType == "podcast" && !episodeFound {
		return httputil.Status(404, "")
	}
	if !access.allowed {
		return httputil.Status(403, "")
	}
	if episodeFound {
		mediaID = episodeID
	}
	return h.writeListeningSessions(w, r, user.ID, mediaID, false)
}

func (h *Handler) updateSeriesContinueListening(w http.ResponseWriter, r *http.Request, user store.User) error {
	seriesID, action := r.PathValue("id"), r.PathValue("action")
	if action != "remove-from-continue-listening" && action != "readd-to-continue-listening" {
		return httputil.Status(404, "")
	}
	var exists int
	if err := h.db.QueryRowContext(r.Context(), `SELECT COUNT(*) FROM series WHERE id=?`, seriesID).Scan(&exists); err != nil {
		return writeInternalError(w, r, "database read failed", err)
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
	seen := false
	filtered := make([]string, 0, len(ids)+1)
	for _, id := range ids {
		if id == seriesID {
			seen = true
			if action == "readd-to-continue-listening" {
				continue
			}
		}
		filtered = append(filtered, id)
	}
	changed := false
	if action == "remove-from-continue-listening" && !seen {
		filtered = append(filtered, seriesID)
		changed = true
	}
	if action == "readd-to-continue-listening" && seen {
		changed = true
	}
	if changed {
		extra["seriesHideFromContinueListening"] = filtered
		raw, _ := json.Marshal(extra)
		if _, err := h.db.ExecContext(r.Context(), `UPDATE users SET extraData=?,updatedAt=? WHERE id=?`, string(raw), dbtime.Now(), user.ID); err != nil {
			return writeInternalError(w, r, "database write failed", err)
		}
		user.ExtraData = raw
		h.emitUserUpdate(r.Context(), user)
	}
	payload, err := h.userPayload(r.Context(), user)
	if err != nil {
		return writeInternalError(w, r, "database read failed", err)
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
		return writeInternalError(w, r, "database read failed", err)
	}
	type progress struct {
		id, itemID, typ, mediaID string
		updated                  any
	}
	items := make([]progress, 0, len(rows))
	for _, row := range rows {
		var data map[string]any
		_ = json.Unmarshal([]byte(row.Extradata.String), &data)
		itemID, _ := data["libraryItemId"].(string)
		items = append(items, progress{id: row.ID, itemID: itemID, typ: row.Mediaitemtype.String, mediaID: row.Mediaitemid.String, updated: row.Updatedat})
	}
	result := make([]map[string]any, 0)
	seen := map[string]bool{}
	for _, p := range items {
		if p.itemID == "" || seen[p.id] {
			continue
		}
		seen[p.id] = true
		access, err := h.canAccessItem(r.Context(), user, p.itemID)
		if err != nil {
			return writeInternalError(w, r, "database read failed", err)
		}
		if !access.allowed {
			continue
		}
		li, err := h.items.ItemMinifiedJSON(r.Context(), p.itemID)
		if err != nil {
			continue
		}
		li["progressLastUpdate"] = dbtime.Millis(p.updated)
		if p.typ == "podcastEpisode" {
			full, e := h.items.ItemJSON(r.Context(), p.itemID, true)
			if e == nil {
				media, _ := full["media"].(map[string]any)
				// findAllExpandedWhere loads the episodes, so the minified media
				// reports their count and total size (MeController.js:490).
				copyEpisodeTotals(li, media)
				if episodes, ok := media["episodes"].([]map[string]any); ok {
					for _, episode := range episodes {
						if episode["id"] == p.mediaID {
							// Upstream sends episode.toOldJSON, not the expanded
							// form (MeController.js getAllLibraryItemsInProgress).
							trimmed := maps.Clone(episode)
							delete(trimmed, "audioTrack")
							delete(trimmed, "size")
							delete(trimmed, "duration")
							li["recentEpisode"] = trimmed
							break
						}
					}
				}
			}
		}
		result = append(result, li)
	}
	sort.SliceStable(result, func(i, j int) bool {
		return numberValue(result[i]["progressLastUpdate"]) > numberValue(result[j]["progressLastUpdate"])
	})
	if limit < 0 {
		limit = 25
	}
	if len(result) > limit {
		result = result[:limit]
	}
	return httputil.JSON(w, r, map[string]any{"libraryItems": result})
}

func (h *Handler) updateEReaderDevices(w http.ResponseWriter, r *http.Request, user store.User) error {
	body, err := httputil.DecodeObjectRequest(r)
	if err != nil {
		return plainError(w, "Invalid payload. ereaderDevices array required", 400)
	}
	provided, ok := body["ereaderDevices"].([]any)
	if !ok {
		return plainError(w, "Invalid payload. ereaderDevices array required", 400)
	}
	for _, raw := range provided {
		d, ok := raw.(map[string]any)
		if !ok || !jsonvalue.Truthy(d["name"]) || !jsonvalue.Truthy(d["email"]) {
			return plainError(w, "Invalid payload. ereaderDevices array items must have name and email", 400)
		}
		users, ok := d["users"].([]any)
		if d["availabilityOption"] != "specificUsers" || !ok || len(users) != 1 || users[0] != user.ID {
			return plainError(w, `Invalid payload. ereaderDevices array items must have availabilityOption "specificUsers" and only the current user`, 400)
		}
	}
	// A database without an email-settings row starts from the defaults TS
	// EmailSettings (objects/settings/EmailSettings.js) and emailapi's load use,
	// and the write below creates the row, as TS's updateSetting does.
	settings := map[string]any{"id": "email-settings", "host": nil, "port": 465, "secure": true,
		"rejectUnauthorized": true, "user": nil, "pass": nil, "testAddress": nil, "fromAddress": nil}
	var raw []byte
	err = h.db.QueryRowContext(r.Context(), `SELECT value FROM settings WHERE key='email-settings'`).Scan(&raw)
	if err != nil && err != sql.ErrNoRows {
		return writeInternalError(w, r, "database read failed", err)
	}
	if err == nil {
		var stored map[string]any
		_ = json.Unmarshal(raw, &stored)
		if stored != nil {
			settings = stored
		}
	}
	all, _ := settings["ereaderDevices"].([]any)
	kept := make([]any, 0)
	for _, entry := range all {
		d, ok := entry.(map[string]any)
		if !ok {
			continue
		}
		users, _ := d["users"].([]any)
		accessible := d["availabilityOption"] == "adminOrUp" && isAdmin(user)
		for _, uid := range users {
			if uid == user.ID {
				accessible = true
			}
		}
		if !accessible || len(users) != 1 {
			kept = append(kept, d)
		}
	}
	combined := append(kept, provided...)
	names := map[string]bool{}
	for _, entry := range combined {
		d, _ := entry.(map[string]any)
		name := jsonvalue.String(d["name"])
		if names[name] {
			return plainError(w, `Invalid payload. Duplicate "name" field found.`, 400)
		}
		names[name] = true
	}
	settings["ereaderDevices"] = combined
	encoded, _ := json.Marshal(settings)
	now := dbtime.Now()
	if _, err := h.db.ExecContext(r.Context(), `INSERT INTO settings (key,value,createdAt,updatedAt) VALUES ('email-settings',?,?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value,updatedAt=excluded.updatedAt`, string(encoded), now, now); err != nil {
		return writeInternalError(w, r, "database write failed", err)
	}
	visible := make([]any, 0)
	for _, entry := range combined {
		d, _ := entry.(map[string]any)
		users, _ := d["users"].([]any)
		if d["availabilityOption"] == "adminOrUp" && isAdmin(user) {
			visible = append(visible, d)
			continue
		}
		for _, uid := range users {
			if uid == user.ID {
				visible = append(visible, d)
				break
			}
		}
	}
	if h.emit != nil {
		h.emit(user.ID, "ereader-devices-updated", map[string]any{"ereaderDevices": visible})
	}
	return httputil.JSON(w, r, map[string]any{"ereaderDevices": visible})
}

func (h *Handler) getYearStats(w http.ResponseWriter, r *http.Request, user store.User) error {
	year, err := strconv.Atoi(r.PathValue("year"))
	if err != nil || year < 2000 || year > 9999 {
		return plainError(w, "Invalid year", 400)
	}
	start, end := strconv.Itoa(year)+"-01-01", strconv.Itoa(year+1)+"-01-01"
	// userStats.getStatsForYear sums raw float seconds (`ls.timeListening
	// || 0`) and Math.rounds only the totals (server/utils/queries/userStats.js:114-190).
	var sessions int
	var total, bookTime, podcastTime float64
	sessionRows, err := sqlc.New(h.db).ListPlaybackSessionsCreatedBetween(r.Context(), sqlc.ListPlaybackSessionsCreatedBetweenParams{UserID: store.Text(user.ID), StartAt: start, EndAt: end})
	if err != nil {
		return writeInternalError(w, r, "database read failed", err)
	}
	months := map[int]float64{}
	books := map[string]float64{}
	authors := map[string]float64{}
	genres := map[string]float64{}
	narrators := map[string]float64{}
	for _, row := range sessionRows {
		typ, title := row.Mediaitemtype, row.Displaytitle
		metaRaw, created := []byte(row.Mediametadata.String), row.Createdat
		secs := row.Timelistening.Float64
		sessions++
		total += secs
		// userStats.getStatsForYear (server/utils/queries/userStats.js:108-150):
		// the month is ls.createdAt.getMonth() in server-local time; book
		// sessions count toward a title only when displayTitle is non-empty,
		// and authors, narrators and genres are tallied for book sessions
		// only (podcast sessions add to totalPodcastListeningTime alone).
		if t := dbtime.Time(created); !t.IsZero() {
			months[int(t.In(time.Local).Month())-1] += secs
		}
		if typ.String != "book" {
			podcastTime += secs
			continue
		}
		bookTime += secs
		if title.String != "" {
			books[title.String] += secs
		}
		var m map[string]any
		_ = json.Unmarshal(metaRaw, &m)
		for _, x := range anyArray(m["authors"]) {
			a, _ := x.(map[string]any)
			authors[jsonvalue.String(a["name"])] += secs
		}
		for _, x := range anyArray(m["narrators"]) {
			n, _ := x.(string)
			narrators[n] += secs
		}
		for _, x := range anyArray(m["genres"]) {
			g, _ := x.(string)
			if g != "" && !strings.Contains(strings.ToLower(g), "audiobook") && !strings.Contains(strings.ToLower(g), "audio book") {
				genres[g] += secs
			}
		}
	}
	finishedRows, err := sqlc.New(h.db).ListFinishedBookProgressBetween(r.Context(), sqlc.ListFinishedBookProgressBetweenParams{UserID: store.Text(user.ID), StartAt: start, EndAt: end})
	if err != nil {
		return writeInternalError(w, r, "database read failed", err)
	}
	finished := 0
	finishedCovers := make([]any, 0, 5)
	longest := map[string]any(nil)
	longestDuration := float64(0)
	for _, row := range finishedRows {
		finished++
		if row.Duration.Valid && row.Duration.Float64 > longestDuration {
			longestDuration = row.Duration.Float64
			longest = map[string]any{"id": row.Mediaitemid.String, "title": row.Title.String, "duration": int(math.Round(row.Duration.Float64)), "finishedAt": dbtime.Millis(row.Finishedat)}
		}
		if row.Coverpath.Valid && len(finishedCovers) < 5 {
			if _, err := os.Stat(row.Coverpath.String); err == nil {
				finishedCovers = append(finishedCovers, row.Libraryitemid)
			}
		}
	}
	finishedSet := map[string]bool{}
	for _, v := range finishedCovers {
		finishedSet[v.(string)] = true
	}
	booksWithCovers := make([]any, 0, 25)
	booksWithCoversSet := map[string]bool{}
	yearSessions, err := h.listeningSessions(r, user.ID, "")
	if err != nil {
		return writeInternalError(w, r, "database read failed", err)
	}
	for _, session := range yearSessions {
		created := dbtime.Time(session.CreatedAt)
		if created.IsZero() || created.Before(time.Date(year, 1, 1, 0, 0, 0, 0, time.UTC)) || !created.Before(time.Date(year+1, 1, 1, 0, 0, 0, 0, time.UTC)) {
			continue
		}
		// Upstream joins the session's book and uses its current coverPath
		// and library item id (userStats.js:8-30, 108-111), not the cover
		// path stored on the session. Its session order is RANDOM(), so the
		// order of booksWithCovers is unspecified upstream.
		if session.MediaItemType.String != "book" || len(booksWithCovers) >= 25 {
			continue
		}
		var itemID string
		var coverPath sql.NullString
		if err := h.db.QueryRowContext(r.Context(), `SELECT li.id, b.coverPath FROM books b JOIN libraryItems li ON li.mediaId = b.id AND li.mediaType = 'book' WHERE b.id = ?`, session.MediaItemID.String).Scan(&itemID, &coverPath); err != nil {
			continue
		}
		if finishedSet[itemID] || booksWithCoversSet[itemID] || coverPath.String == "" {
			continue
		}
		if _, err := os.Stat(coverPath.String); err == nil {
			booksWithCovers = append(booksWithCovers, itemID)
			booksWithCoversSet[itemID] = true
		}
	}
	return httputil.JSON(w, r, map[string]any{"totalListeningSessions": sessions, "totalListeningTime": int(math.Round(total)), "totalBookListeningTime": int(math.Round(bookTime)), "totalPodcastListeningTime": int(math.Round(podcastTime)), "topAuthors": topEntries(roundTimes(authors), "name", 3), "topGenres": topEntries(roundTimes(genres), "genre", 3), "mostListenedNarrator": topOne(roundTimes(narrators), "name"), "mostListenedMonth": topMonth(roundMonths(months)), "numBooksFinished": finished, "numBooksListened": len(books), "longestAudiobookFinished": longest, "booksWithCovers": booksWithCovers, "finishedBooksWithCovers": finishedCovers})
}

// roundTimes applies upstream's per-entry Math.round to summed seconds.
func roundTimes(m map[string]float64) map[string]int {
	out := make(map[string]int, len(m))
	for k, v := range m {
		out[k] = int(math.Round(v))
	}
	return out
}

func roundMonths(m map[int]float64) map[int]int {
	out := make(map[int]int, len(m))
	for k, v := range m {
		out[k] = int(math.Round(v))
	}
	return out
}
func anyArray(v any) []any { x, _ := v.([]any); return x }
func topEntries(m map[string]int, key string, n int) []any {
	type kv struct {
		k string
		v int
	}
	a := []kv{}
	for k, v := range m {
		if k != "" {
			a = append(a, kv{k, v})
		}
	}
	sort.Slice(a, func(i, j int) bool {
		if a[i].v == a[j].v {
			return a[i].k < a[j].k
		}
		return a[i].v > a[j].v
	})
	if len(a) > n {
		a = a[:n]
	}
	out := make([]any, 0, len(a))
	for _, x := range a {
		if key == "name" {
			out = append(out, map[string]any{"name": x.k, "time": x.v})
		} else {
			out = append(out, map[string]any{"genre": x.k, "time": x.v})
		}
	}
	return out
}
func topOne(m map[string]int, key string) any {
	a := topEntries(m, key, 1)
	if len(a) == 0 {
		return nil
	}
	return a[0]
}
func topMonth(m map[int]int) any {
	bestMonth, best := 0, 0
	for k, v := range m {
		if v > best {
			bestMonth, best = k, v
		}
	}
	if best == 0 {
		return nil
	}
	return map[string]any{"month": bestMonth, "time": best}
}

// copyEpisodeTotals sets numEpisodes and size on a minified item's media from
// the expanded media. Upstream minifies library items whose episodes ARE
// loaded in some responses (items in progress, a freshly added playlist
// episode) and whose episodes are NOT in others (shelves, playlist reloads);
// the shared minified reader models the latter.
func copyEpisodeTotals(minified map[string]any, expandedMedia map[string]any) {
	if media, ok := minified["media"].(map[string]any); ok {
		media["numEpisodes"] = expandedMedia["numEpisodes"]
		media["size"] = expandedMedia["size"]
	}
}
