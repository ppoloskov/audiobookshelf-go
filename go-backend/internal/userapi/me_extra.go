package userapi

import (
	"database/sql"
	"encoding/json"
	"math"
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"audiobookshelf-compat/internal/dbtime"

	"audiobookshelf-compat/internal/store"
)

func (h *Handler) getItemListeningSessions(w http.ResponseWriter, r *http.Request, user store.User, tail string) {
	parts := strings.Split(tail, "/")
	if len(parts) == 0 || len(parts) > 2 || parts[0] == "" {
		sendStatus(w, 404)
		return
	}
	access, err := h.canAccessItem(r.Context(), user, parts[0])
	if err != nil {
		http.Error(w, "database read failed", 500)
		return
	}
	if !access.exists {
		sendStatus(w, 404)
		return
	}
	if !access.allowed {
		sendStatus(w, 403)
		return
	}
	var mediaID, mediaType string
	if err := h.db.QueryRowContext(r.Context(), `SELECT mediaId,mediaType FROM libraryItems WHERE id=?`, parts[0]).Scan(&mediaID, &mediaType); err != nil {
		sendStatus(w, 404)
		return
	}
	if len(parts) == 2 && parts[1] != "" {
		if mediaType != "podcast" {
			sendStatus(w, 404)
			return
		}
		var exists int
		if err := h.db.QueryRowContext(r.Context(), `SELECT COUNT(*) FROM podcastEpisodes WHERE id=? AND podcastId=?`, parts[1], mediaID).Scan(&exists); err != nil {
			http.Error(w, "database read failed", 500)
			return
		}
		if exists == 0 {
			sendStatus(w, 404)
			return
		}
		mediaID = parts[1]
	}
	h.writeListeningSessions(w, r, user.ID, mediaID, false)
}

func (h *Handler) updateSeriesContinueListening(w http.ResponseWriter, r *http.Request, user store.User, tail string) {
	parts := strings.Split(tail, "/")
	if len(parts) != 2 || parts[0] == "" || (parts[1] != "remove-from-continue-listening" && parts[1] != "readd-to-continue-listening") {
		sendStatus(w, 404)
		return
	}
	var exists int
	if err := h.db.QueryRowContext(r.Context(), `SELECT COUNT(*) FROM series WHERE id=?`, parts[0]).Scan(&exists); err != nil {
		http.Error(w, "database read failed", 500)
		return
	}
	if exists == 0 {
		sendStatus(w, 404)
		return
	}
	var extra map[string]any
	_ = json.Unmarshal(user.ExtraData, &extra)
	if extra == nil {
		extra = map[string]any{}
	}
	ids := stringSlice(extra["seriesHideFromContinueListening"])
	seen := false
	filtered := make([]string, 0, len(ids)+1)
	for _, id := range ids {
		if id == parts[0] {
			seen = true
			if parts[1] == "readd-to-continue-listening" {
				continue
			}
		}
		filtered = append(filtered, id)
	}
	changed := false
	if parts[1] == "remove-from-continue-listening" && !seen {
		filtered = append(filtered, parts[0])
		changed = true
	}
	if parts[1] == "readd-to-continue-listening" && seen {
		changed = true
	}
	if changed {
		extra["seriesHideFromContinueListening"] = filtered
		raw, _ := json.Marshal(extra)
		if _, err := h.db.ExecContext(r.Context(), `UPDATE users SET extraData=?,updatedAt=? WHERE id=?`, raw, dbtime.Now(), user.ID); err != nil {
			http.Error(w, "database write failed", 500)
			return
		}
		user.ExtraData = raw
		h.emitUserUpdate(r.Context(), user)
	}
	payload, err := h.userPayload(r.Context(), user)
	if err != nil {
		http.Error(w, "database read failed", 500)
		return
	}
	writeJSON(w, 200, payload)
}

func (h *Handler) getItemsInProgress(w http.ResponseWriter, r *http.Request, user store.User) {
	limit := queryInt(r, "limit", 25)
	if limit == 0 {
		limit = 25
	}
	rows, err := h.db.QueryContext(r.Context(), `SELECT id,mediaItemId,mediaItemType,extraData,updatedAt FROM mediaProgresses WHERE userId=? AND isFinished=0 AND (currentTime>0 OR ebookProgress>0) ORDER BY updatedAt DESC`, user.ID)
	if err != nil {
		http.Error(w, "database read failed", 500)
		return
	}
	type progress struct {
		id, itemID, typ, mediaID string
		updated                  any
	}
	items := make([]progress, 0)
	for rows.Next() {
		var p progress
		var extra []byte
		if err := rows.Scan(&p.id, &p.mediaID, &p.typ, &extra, &p.updated); err != nil {
			rows.Close()
			http.Error(w, "database read failed", 500)
			return
		}
		var data map[string]any
		_ = json.Unmarshal(extra, &data)
		p.itemID, _ = data["libraryItemId"].(string)
		items = append(items, p)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		http.Error(w, "database read failed", 500)
		return
	}
	rows.Close()
	result := make([]map[string]any, 0)
	seen := map[string]bool{}
	for _, p := range items {
		if p.itemID == "" || seen[p.id] {
			continue
		}
		seen[p.id] = true
		access, err := h.canAccessItem(r.Context(), user, p.itemID)
		if err != nil {
			http.Error(w, "database read failed", 500)
			return
		}
		if !access.allowed {
			continue
		}
		li, err := h.items.ItemMinifiedJSON(r.Context(), p.itemID)
		if err != nil {
			continue
		}
		li["progressLastUpdate"] = millis(p.updated)
		if p.typ == "podcastEpisode" {
			full, e := h.items.ItemJSON(r.Context(), p.itemID, true)
			if e == nil {
				media, _ := full["media"].(map[string]any)
				if episodes, ok := media["episodes"].([]map[string]any); ok {
					for _, episode := range episodes {
						if episode["id"] == p.mediaID {
							li["recentEpisode"] = episode
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
	writeJSON(w, 200, map[string]any{"libraryItems": result})
}

func (h *Handler) updateEReaderDevices(w http.ResponseWriter, r *http.Request, user store.User) {
	body, err := decodeBody(r)
	if err != nil {
		http.Error(w, "Invalid payload. ereaderDevices array required", 400)
		return
	}
	provided, ok := body["ereaderDevices"].([]any)
	if !ok {
		http.Error(w, "Invalid payload. ereaderDevices array required", 400)
		return
	}
	for _, raw := range provided {
		d, ok := raw.(map[string]any)
		if !ok || !jsTruthy(d["name"]) || !jsTruthy(d["email"]) {
			http.Error(w, "Invalid payload. ereaderDevices array items must have name and email", 400)
			return
		}
		users, ok := d["users"].([]any)
		if d["availabilityOption"] != "specificUsers" || !ok || len(users) != 1 || users[0] != user.ID {
			http.Error(w, `Invalid payload. ereaderDevices array items must have availabilityOption "specificUsers" and only the current user`, 400)
			return
		}
	}
	var raw []byte
	if err := h.db.QueryRowContext(r.Context(), `SELECT value FROM settings WHERE key='email-settings'`).Scan(&raw); err != nil {
		http.Error(w, "database read failed", 500)
		return
	}
	var settings map[string]any
	_ = json.Unmarshal(raw, &settings)
	if settings == nil {
		settings = map[string]any{}
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
		name := stringValue(d["name"])
		if names[name] {
			http.Error(w, `Invalid payload. Duplicate "name" field found.`, 400)
			return
		}
		names[name] = true
	}
	settings["ereaderDevices"] = combined
	encoded, _ := json.Marshal(settings)
	if _, err := h.db.ExecContext(r.Context(), `UPDATE settings SET value=?,updatedAt=? WHERE key='email-settings'`, encoded, dbtime.Now()); err != nil {
		http.Error(w, "database write failed", 500)
		return
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
	writeJSON(w, 200, map[string]any{"ereaderDevices": visible})
}

func (h *Handler) getYearStats(w http.ResponseWriter, r *http.Request, user store.User, rawYear string) {
	year, err := strconv.Atoi(rawYear)
	if err != nil || year < 2000 || year > 9999 {
		http.Error(w, "Invalid year", 400)
		return
	}
	start, end := strconv.Itoa(year)+"-01-01", strconv.Itoa(year+1)+"-01-01"
	var sessions, total, bookTime, podcastTime int
	rows, err := h.db.QueryContext(r.Context(), `SELECT mediaItemType,timeListening,displayTitle,mediaMetadata,createdAt FROM playbackSessions WHERE userId=? AND createdAt>=? AND createdAt<?`, user.ID, start, end)
	if err != nil {
		http.Error(w, "database read failed", 500)
		return
	}
	months := map[int]int{}
	books := map[string]int{}
	authors := map[string]int{}
	genres := map[string]int{}
	narrators := map[string]int{}
	for rows.Next() {
		var typ, title string
		var secs int
		var metaRaw []byte
		var created any
		if err := rows.Scan(&typ, &secs, &title, &metaRaw, &created); err != nil {
			rows.Close()
			http.Error(w, "database read failed", 500)
			return
		}
		sessions++
		total += secs
		if typ == "book" {
			bookTime += secs
			books[title] += secs
		} else {
			podcastTime += secs
		}
		if t := parseTime(created); !t.IsZero() {
			months[int(t.Month())-1] += secs
		}
		var m map[string]any
		_ = json.Unmarshal(metaRaw, &m)
		for _, x := range anyArray(m["authors"]) {
			a, _ := x.(map[string]any)
			authors[stringValue(a["name"])] += secs
		}
		for _, x := range anyArray(m["genres"]) {
			g, _ := x.(string)
			if !strings.Contains(strings.ToLower(g), "audiobook") && !strings.Contains(strings.ToLower(g), "audio book") {
				genres[g] += secs
			}
		}
		for _, x := range anyArray(m["narrators"]) {
			n, _ := x.(string)
			narrators[n] += secs
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		http.Error(w, "database read failed", 500)
		return
	}
	rows.Close()
	finishedRows, err := h.db.QueryContext(r.Context(), `SELECT mp.mediaItemId,b.title,mp.duration,mp.finishedAt,li.id,b.coverPath FROM mediaProgresses mp JOIN books b ON b.id=mp.mediaItemId JOIN libraryItems li ON li.mediaId=b.id AND li.mediaType='book' WHERE mp.userId=? AND mp.mediaItemType='book' AND mp.finishedAt>=? AND mp.finishedAt<?`, user.ID, start, end)
	if err != nil {
		http.Error(w, "database read failed", 500)
		return
	}
	finished := 0
	finishedCovers := make([]any, 0, 5)
	longest := map[string]any(nil)
	longestDuration := float64(0)
	for finishedRows.Next() {
		var mediaID, title, itemID string
		var duration sql.NullFloat64
		var finishedAt, cover sql.NullString
		if err := finishedRows.Scan(&mediaID, &title, &duration, &finishedAt, &itemID, &cover); err != nil {
			finishedRows.Close()
			http.Error(w, "database read failed", 500)
			return
		}
		finished++
		if duration.Valid && duration.Float64 > longestDuration {
			longestDuration = duration.Float64
			longest = map[string]any{"id": mediaID, "title": title, "duration": int(math.Round(duration.Float64)), "finishedAt": millis(finishedAt.String)}
		}
		if cover.Valid && len(finishedCovers) < 5 {
			if _, err := os.Stat(cover.String); err == nil {
				finishedCovers = append(finishedCovers, itemID)
			}
		}
	}
	if err := finishedRows.Err(); err != nil {
		finishedRows.Close()
		http.Error(w, "database read failed", 500)
		return
	}
	finishedRows.Close()
	finishedSet := map[string]bool{}
	for _, v := range finishedCovers {
		finishedSet[v.(string)] = true
	}
	booksWithCovers := make([]any, 0, 25)
	booksWithCoversSet := map[string]bool{}
	yearSessions, err := h.listeningSessions(r, user.ID, "")
	if err != nil {
		http.Error(w, "database read failed", 500)
		return
	}
	for _, session := range yearSessions {
		created := parseTime(session.createdAt)
		if created.IsZero() || created.Before(time.Date(year, 1, 1, 0, 0, 0, 0, time.UTC)) || !created.Before(time.Date(year+1, 1, 1, 0, 0, 0, 0, time.UTC)) {
			continue
		}
		var extra map[string]any
		_ = json.Unmarshal(session.extraData, &extra)
		itemID, _ := extra["libraryItemId"].(string)
		if itemID == "" || finishedSet[itemID] || booksWithCoversSet[itemID] || len(booksWithCovers) >= 25 || session.coverPath == "" {
			continue
		}
		if _, err := os.Stat(session.coverPath); err == nil {
			booksWithCovers = append(booksWithCovers, itemID)
			booksWithCoversSet[itemID] = true
		}
	}
	writeJSON(w, 200, map[string]any{"totalListeningSessions": sessions, "totalListeningTime": total, "totalBookListeningTime": bookTime, "totalPodcastListeningTime": podcastTime, "topAuthors": topEntries(authors, "name", 3), "topGenres": topEntries(genres, "genre", 3), "mostListenedNarrator": topOne(narrators, "name"), "mostListenedMonth": topMonth(months), "numBooksFinished": finished, "numBooksListened": len(books), "longestAudiobookFinished": longest, "booksWithCovers": booksWithCovers, "finishedBooksWithCovers": finishedCovers})
}

func parseTime(v any) time.Time {
	switch x := v.(type) {
	case time.Time:
		return x
	case string:
		t, _ := time.Parse("2006-01-02 15:04:05.999999999", x)
		return t
	case []byte:
		t, _ := time.Parse("2006-01-02 15:04:05.999999999", string(x))
		return t
	}
	return time.Time{}
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
