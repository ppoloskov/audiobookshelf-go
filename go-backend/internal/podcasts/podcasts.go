// Package podcasts implements the podcast RSS and episode service surfaces.
// It keeps the pinned SQLite schema and public response shapes at the boundary.
package podcasts

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"audiobookshelf-compat/internal/config"
	"audiobookshelf-compat/internal/dbtime"
	"audiobookshelf-compat/internal/store"
	"github.com/robfig/cron/v3"
)

type EmitFunc func(userID, event string, payload any)

var errPodcastExists = errors.New("podcast already exists")

type Handler struct {
	db                  *sql.DB
	cfg                 config.Config
	emit                EmitFunc
	client              *http.Client
	allowPrivateNetwork bool
	queueMu             sync.Mutex
	queue               []*downloadTask
	current             *downloadTask
	scheduler           *cron.Cron
	scheduleIDs         map[string]cron.EntryID
	failedCheckMap      map[string]int
	maxFailedChecks     int
	quickMatch          func(context.Context, string, bool) (int, error)
}

func New(db *sql.DB, cfg config.Config, emit EmitFunc) *Handler {
	if emit == nil {
		emit = func(string, string, any) {}
	}
	maxFailed := 24
	if raw, err := strconv.Atoi(os.Getenv("MAX_FAILED_EPISODE_CHECKS")); err == nil && raw >= 0 {
		maxFailed = raw
	}
	h := &Handler{db: db, cfg: cfg, emit: emit, client: secureClient(), scheduleIDs: map[string]cron.EntryID{}, failedCheckMap: map[string]int{}, maxFailedChecks: maxFailed}
	h.startSchedules()
	return h
}

// SetQuickMatchHandler wires the scanner's podcast episode matcher without a
// package dependency cycle. A nil callback leaves the route unavailable.
func (h *Handler) SetQuickMatchHandler(fn func(context.Context, string, bool) (int, error)) {
	h.queueMu.Lock()
	h.quickMatch = fn
	h.queueMu.Unlock()
}

// Handle handles feed and OPML routes under /api/podcasts. Unknown paths are
// left to the runtime's explicit unsupported-route handler.
func (h *Handler) Handle(w http.ResponseWriter, r *http.Request, user store.User) bool {
	p := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/api"), "/")
	if p == "" {
		p = "/"
	}
	if p == "/podcasts/feed" && r.Method == http.MethodPost {
		if !admin(user) {
			w.WriteHeader(403)
			return true
		}
		var body struct {
			RSSFeed string `json:"rssFeed"`
		}
		if json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&body) != nil || validFeedURL(body.RSSFeed) == "" {
			text(w, 400, `Invalid request body. "rssFeed" must be a valid URL`)
			return true
		}
		feed, err := h.getFeed(r.Context(), body.RSSFeed, false)
		if err != nil {
			text(w, 404, "Podcast RSS feed request failed or invalid response data")
			return true
		}
		jsonResponse(w, 200, map[string]any{"podcast": feed})
		return true
	}
	if p == "/podcasts" && r.Method == http.MethodPost {
		if !admin(user) {
			w.WriteHeader(403)
			return true
		}
		h.createPodcast(w, r)
		return true
	}
	if p == "/podcasts/opml/parse" && r.Method == http.MethodPost {
		if !admin(user) {
			w.WriteHeader(403)
			return true
		}
		var body struct {
			OPMLText string `json:"opmlText"`
		}
		if json.NewDecoder(io.LimitReader(r.Body, 4<<20)).Decode(&body) != nil || body.OPMLText == "" {
			w.WriteHeader(400)
			return true
		}
		jsonResponse(w, 200, map[string]any{"feeds": parseOPML(body.OPMLText)})
		return true
	}
	if p == "/podcasts/opml/create" && r.Method == http.MethodPost {
		if !admin(user) {
			w.WriteHeader(403)
			return true
		}
		h.createFromOPML(w, r)
		return true
	}
	parts := strings.Split(strings.TrimPrefix(p, "/podcasts/"), "/")
	if !strings.HasPrefix(p, "/podcasts/") || len(parts) < 2 {
		return false
	}
	id, err := url.PathUnescape(parts[0])
	if err != nil || id == "" {
		w.WriteHeader(400)
		return true
	}
	item, err := h.loadItem(r.Context(), id)
	if errors.Is(err, sql.ErrNoRows) {
		w.WriteHeader(404)
		return true
	}
	if err != nil {
		http.Error(w, "database read failed", 500)
		return true
	}
	if !canAccessPodcast(user, item) {
		w.WriteHeader(403)
		return true
	}
	switch {
	case len(parts) == 2 && parts[1] == "downloads" && r.Method == http.MethodGet:
		jsonResponse(w, 200, map[string]any{"downloads": h.downloadsFor(id)})
		return true
	case len(parts) == 2 && parts[1] == "clear-queue" && r.Method == http.MethodGet:
		h.clearQueue(id)
		w.WriteHeader(200)
		return true
	case len(parts) == 2 && parts[1] == "download-episodes" && r.Method == http.MethodPost:
		if !admin(user) {
			w.WriteHeader(403)
			return true
		}
		var episodes []map[string]any
		if json.NewDecoder(io.LimitReader(r.Body, 8<<20)).Decode(&episodes) != nil || len(episodes) == 0 {
			w.WriteHeader(400)
			return true
		}
		if err := h.enqueue(item, episodes, false); err != nil {
			text(w, 400, err.Error())
			return true
		}
		w.WriteHeader(200)
		return true
	case len(parts) == 2 && parts[1] == "search-episode" && r.Method == http.MethodGet:
		if !item.FeedURL.Valid || item.FeedURL.String == "" {
			text(w, 400, "Podcast does not have an RSS feed URL")
			return true
		}
		title := r.URL.Query().Get("title")
		if title == "" {
			w.WriteHeader(500)
			return true
		}
		feed, e := h.getFeed(r.Context(), item.FeedURL.String, false)
		if e != nil {
			jsonResponse(w, 200, map[string]any{"episodes": nil})
			return true
		}
		jsonResponse(w, 200, map[string]any{"episodes": matchEpisodes(feed, title)})
		return true
	case len(parts) == 2 && parts[1] == "match-episodes" && r.Method == http.MethodPost:
		if !canUpdatePodcast(user) {
			w.WriteHeader(403)
			return true
		}
		h.queueMu.Lock()
		matcher := h.quickMatch
		h.queueMu.Unlock()
		if matcher == nil {
			http.Error(w, "Podcast episode matcher unavailable", http.StatusServiceUnavailable)
			return true
		}
		updated, err := matcher(r.Context(), item.ID, r.URL.Query().Get("override") == "1")
		if err != nil {
			http.Error(w, "Podcast episode matching failed", http.StatusInternalServerError)
			return true
		}
		if updated > 0 {
			if fresh, e := h.loadItem(r.Context(), item.ID); e == nil {
				h.emit("@admins", "item_updated", fresh.oldJSON())
			}
		}
		jsonResponse(w, 200, map[string]any{"numEpisodesUpdated": updated})
		return true
	case len(parts) == 2 && parts[1] == "checknew" && r.Method == http.MethodGet:
		if !admin(user) {
			w.WriteHeader(403)
			return true
		}
		if !item.FeedURL.Valid || item.FeedURL.String == "" {
			text(w, 400, "Podcast has no rss feed url")
			return true
		}
		h.checkNew(w, r, item)
		return true
	case len(parts) == 3 && parts[1] == "episode" && r.Method == http.MethodGet:
		episodeID, decodeErr := url.PathUnescape(parts[2])
		if decodeErr != nil {
			w.WriteHeader(400)
			return true
		}
		ep, ok := item.episode(episodeID)
		if !ok {
			text(w, 404, "Episode not found")
			return true
		}
		jsonResponse(w, 200, ep.oldJSON(item.ID))
		return true
	case len(parts) == 3 && parts[1] == "episode" && r.Method == http.MethodPatch:
		if !canUpdatePodcast(user) {
			w.WriteHeader(403)
			return true
		}
		episodeID, decodeErr := url.PathUnescape(parts[2])
		if decodeErr != nil {
			w.WriteHeader(400)
			return true
		}
		h.updateEpisode(w, r, item, episodeID)
		return true
	case len(parts) == 3 && parts[1] == "episode" && r.Method == http.MethodDelete:
		if !canDeletePodcast(user) {
			w.WriteHeader(403)
			return true
		}
		episodeID, decodeErr := url.PathUnescape(parts[2])
		if decodeErr != nil {
			w.WriteHeader(400)
			return true
		}
		h.removeEpisode(w, r, item, episodeID)
		return true
	}
	return false
}

func canUpdatePodcast(u store.User) bool {
	if admin(u) {
		return true
	}
	var p map[string]any
	_ = json.Unmarshal(u.Permissions, &p)
	return p["update"] == true
}
func canDeletePodcast(u store.User) bool {
	if admin(u) {
		return true
	}
	var p map[string]any
	_ = json.Unmarshal(u.Permissions, &p)
	return p["delete"] == true
}

func (h *Handler) updateEpisode(w http.ResponseWriter, r *http.Request, item podcastItem, episodeID string) {
	ep, ok := item.episode(episodeID)
	if !ok {
		text(w, 404, "Episode not found")
		return
	}
	var body map[string]any
	if json.NewDecoder(io.LimitReader(r.Body, 2<<20)).Decode(&body) != nil {
		text(w, 400, "Invalid request body")
		return
	}
	fields := map[string]any{}
	allowed := map[string]string{"title": "title", "subtitle": "subtitle", "description": "description", "pubDate": "pubDate", "episode": "episode", "season": "season", "episodeType": "episodeType"}
	for key, column := range allowed {
		if v, exists := body[key]; exists {
			if s, ok := v.(string); ok {
				if (key == "description" || key == "subtitle") && s != "" {
					s, _ = sanitizeHTML(s)
				}
				old := nullableSQL(episodeStringField(ep, key))
				if old != s {
					fields[column] = s
				}
			}
		}
	}
	if raw, exists := body["enclosure"]; exists {
		if raw == nil {
			if ep.EnclosureURL.Valid || ep.EnclosureSize.Valid || ep.EnclosureType.Valid {
				fields["enclosureURL"] = nil
				fields["enclosureSize"] = nil
				fields["enclosureType"] = nil
			}
		} else if enclosure, ok := raw.(map[string]any); ok {
			if u, ok := enclosure["url"].(string); ok {
				size := any(nil)
				if n, ok := enclosure["length"].(float64); ok {
					size = int64(n)
				}
				typ, _ := enclosure["type"].(string)
				if !ep.EnclosureURL.Valid || ep.EnclosureURL.String != u {
					fields["enclosureURL"] = u
				}
				if !ep.EnclosureSize.Valid || size == nil || ep.EnclosureSize.Int64 != size.(int64) {
					fields["enclosureSize"] = size
				}
				if !ep.EnclosureType.Valid || ep.EnclosureType.String != typ {
					fields["enclosureType"] = nullableAny(typ)
				}
			}
		}
	}
	if raw, exists := body["chapters"]; exists {
		if list, ok := raw.([]any); ok {
			valid := true
			for _, value := range list {
				chapter, yes := value.(map[string]any)
				if !yes || chapter["title"] == nil || chapter["start"] == nil || chapter["start"] == float64(0) {
					valid = false
					break
				}
			}
			if valid {
				encoded, _ := json.Marshal(list)
				var prior any
				_ = json.Unmarshal([]byte(ep.Chapters.String), &prior)
				old, _ := json.Marshal(prior)
				if string(encoded) != string(old) {
					fields["chapters"] = string(encoded)
				}
			}
		}
	}
	if n, ok := body["publishedAt"].(float64); ok {
		millis := int64(n)
		if current := sqlMillis(ep.PublishedAt); current != millis {
			fields["publishedAt"] = time.UnixMilli(millis).UTC().Format(time.RFC3339Nano)
		}
	}
	if len(fields) > 0 {
		keys := make([]string, 0, len(fields))
		for k := range fields {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		set := make([]string, 0, len(keys))
		args := make([]any, 0, len(keys)+2)
		for _, k := range keys {
			set = append(set, k+"=?")
			args = append(args, fields[k])
		}
		args = append(args, dbtime.Now(), episodeID)
		set = append(set, "updatedAt=?")
		if _, err := h.db.ExecContext(r.Context(), `UPDATE podcastEpisodes SET `+strings.Join(set, ",")+` WHERE id=?`, args...); err != nil {
			http.Error(w, "database write failed", 500)
			return
		}
		updated, err := h.loadItem(r.Context(), item.ID)
		if err == nil {
			h.emit("@admins", "item_updated", updated.oldJSON())
			item = updated
		}
	}
	jsonResponse(w, 200, item.oldJSON())
}

func episodeStringField(e podcastEpisode, key string) sql.NullString {
	switch key {
	case "title":
		return e.Title
	case "subtitle":
		return e.Subtitle
	case "description":
		return e.Description
	case "pubDate":
		return e.PubDate
	case "episode":
		return e.Episode
	case "season":
		return e.Season
	default:
		return e.EpisodeType
	}
}

func (h *Handler) removeEpisode(w http.ResponseWriter, r *http.Request, item podcastItem, episodeID string) {
	ep, ok := item.episode(episodeID)
	if !ok {
		text(w, 404, "Episode not found")
		return
	}
	if r.URL.Query().Get("hard") == "1" {
		var audio map[string]any
		_ = json.Unmarshal([]byte(ep.AudioFile.String), &audio)
		var metadata map[string]any
		_ = json.Unmarshal(mustJSON(audio["metadata"]), &metadata)
		if path, ok := metadata["path"].(string); ok && path != "" {
			_ = os.Remove(path)
		}
	}
	var audio map[string]any
	_ = json.Unmarshal([]byte(ep.AudioFile.String), &audio)
	ino := fmt.Sprint(audio["ino"])
	files := []map[string]any{}
	var raw []map[string]any
	_ = json.Unmarshal([]byte(item.LibraryFiles.String), &raw)
	for _, f := range raw {
		if f["ino"] != ino {
			files = append(files, f)
		}
	}
	fileBytes, _ := json.Marshal(files)
	tx, err := h.db.BeginTx(r.Context(), nil)
	if err != nil {
		http.Error(w, "database write failed", 500)
		return
	}
	now := dbtime.Now()
	if _, err = tx.ExecContext(r.Context(), `DELETE FROM playlistMediaItems WHERE mediaItemId=?`, episodeID); err == nil {
		_, err = tx.ExecContext(r.Context(), `DELETE FROM mediaProgresses WHERE mediaItemId=?`, episodeID)
	}
	if err == nil {
		_, err = tx.ExecContext(r.Context(), `DELETE FROM podcastEpisodes WHERE id=?`, episodeID)
	}
	if err == nil {
		_, err = tx.ExecContext(r.Context(), `UPDATE libraryItems SET libraryFiles=?,updatedAt=? WHERE id=?`, string(fileBytes), now, item.ID)
	}
	if err == nil {
		_, err = tx.ExecContext(r.Context(), `UPDATE podcasts SET numEpisodes=?,updatedAt=? WHERE id=?`, len(item.Episodes)-1, now, item.MediaID)
	}
	if err != nil {
		_ = tx.Rollback()
		http.Error(w, "database write failed", 500)
		return
	}
	if err = tx.Commit(); err != nil {
		http.Error(w, "database write failed", 500)
		return
	}
	updated, err := h.loadItem(r.Context(), item.ID)
	if err != nil {
		http.Error(w, "database read failed", 500)
		return
	}
	h.emit("@admins", "item_updated", updated.oldJSON())
	jsonResponse(w, 200, updated.legacyJSON())
}

func (h *Handler) checkNew(w http.ResponseWriter, r *http.Request, item podcastItem) {
	feed, err := h.getFeed(r.Context(), item.FeedURL.String, false)
	if err != nil {
		jsonResponse(w, 200, map[string]any{"episodes": []any{}})
		return
	}
	after := sqlMillis(item.LastEpisodeCheck)
	afterMS := int64(0)
	if f, ok := after.(int64); ok {
		afterMS = f
	}
	known := map[string]bool{}
	for _, ep := range item.Episodes {
		var extra map[string]any
		_ = json.Unmarshal([]byte(ep.ExtraData.String), &extra)
		if guid, ok := extra["guid"].(string); ok && guid != "" {
			known["g:"+guid] = true
		}
		if ep.EnclosureURL.Valid {
			known["u:"+ep.EnclosureURL.String] = true
		}
	}
	all, _ := feed["episodes"].([]map[string]any)
	limit := 3
	if raw := r.URL.Query().Get("limit"); raw != "" {
		if n, e := strconv.Atoi(raw); e == nil {
			limit = n
		}
	}
	found := make([]map[string]any, 0)
	for _, ep := range all {
		pub, _ := ep["publishedAt"].(float64)
		if pub == 0 {
			if n, ok := ep["publishedAt"].(int64); ok {
				pub = float64(n)
			}
		}
		enc, _ := ep["enclosure"].(map[string]any)
		guid, _ := ep["guid"].(string)
		urlValue, _ := enc["url"].(string)
		if int64(pub) <= afterMS || known["g:"+guid] || known["u:"+urlValue] {
			continue
		}
		found = append(found, ep)
		if limit > 0 && len(found) >= limit {
			break
		}
	}
	now := dbtime.Now()
	if _, err := h.db.ExecContext(r.Context(), `UPDATE podcasts SET lastEpisodeCheck=?,updatedAt=? WHERE id=?`, now, now, item.MediaID); err != nil {
		http.Error(w, "database write failed", http.StatusInternalServerError)
		return
	}
	if len(found) > 0 {
		if err := h.enqueue(item, found, false); err != nil {
			log.Printf("[podcasts] manual episode check enqueue failed for %s: %v", item.ID, err)
		}
	}
	jsonResponse(w, 200, map[string]any{"episodes": found})
}

func (x podcastItem) episode(id string) (podcastEpisode, bool) {
	for _, ep := range x.Episodes {
		if ep.ID == id {
			return ep, true
		}
	}
	return podcastEpisode{}, false
}
func matchEpisodes(feed map[string]any, query string) []map[string]any {
	eps, _ := feed["episodes"].([]map[string]any)
	type score struct {
		v map[string]any
		s float64
	}
	var found []score
	needle := strings.ToLower(strings.TrimSpace(query))
	for _, ep := range eps {
		title, _ := ep["title"].(string)
		sub, _ := ep["subtitle"].(string)
		a := similarity(needle, strings.ToLower(title))
		b := similarity(needle, strings.ToLower(sub))
		s := a*.7 + b*.3
		if a > 0.6 || b > 0.6 {
			found = append(found, score{ep, s})
		}
	}
	sort.SliceStable(found, func(i, j int) bool { return found[i].s > found[j].s })
	out := make([]map[string]any, len(found))
	for i, x := range found {
		out[i] = map[string]any{"episode": x.v}
	}
	return out
}

// FetchFeedEpisodes fetches and parses a podcast feed using the handler's
// configured HTTP client and network policy. It is intended for scanner
// integration and returns parsed episode maps in API shape.
func (h *Handler) FetchFeedEpisodes(ctx context.Context, feedURL string) ([]map[string]any, error) {
	feed, err := h.getFeed(ctx, feedURL, false)
	if err != nil {
		return nil, err
	}
	episodes, _ := feed["episodes"].([]map[string]any)
	return episodes, nil
}
func similarity(a, b string) float64 {
	if a == "" || b == "" {
		return 0
	}
	distance := levenshtein(a, b)
	maxLen := len([]rune(a))
	if n := len([]rune(b)); n > maxLen {
		maxLen = n
	}
	return 1 - float64(distance)/float64(maxLen)
}
func levenshtein(a, b string) int {
	x, y := []rune(a), []rune(b)
	prev := make([]int, len(y)+1)
	for j := range prev {
		prev[j] = j
	}
	for i, rx := range x {
		cur := make([]int, len(y)+1)
		cur[0] = i + 1
		for j, ry := range y {
			cost := 0
			if rx != ry {
				cost = 1
			}
			cur[j+1] = min(cur[j]+1, prev[j+1]+1, prev[j]+cost)
		}
		prev = cur
	}
	return prev[len(y)]
}

func (h *Handler) Close() {
	if h.scheduler != nil {
		stopped := h.scheduler.Stop()
		<-stopped.Done()
	}
	h.queueMu.Lock()
	if h.current != nil && h.current.cancel != nil {
		h.current.cancel()
	}
	h.queue = nil
	h.queueMu.Unlock()
}

func (h *Handler) createPodcast(w http.ResponseWriter, r *http.Request) {
	var body map[string]any
	if json.NewDecoder(io.LimitReader(r.Body, 4<<20)).Decode(&body) != nil {
		text(w, 400, "Invalid request body")
		return
	}
	item, status, message, err := h.createPodcastFromBody(r.Context(), body)
	if err != nil {
		if status == http.StatusInternalServerError {
			http.Error(w, message, status)
		} else {
			text(w, status, message)
		}
		return
	}
	jsonResponse(w, 200, item.oldJSON())
	if item.AutoDownload {
		h.addSchedule(item.ID)
	}
}

// createPodcastFromBody performs podcast creation without coupling the operation
// to an HTTP request or response. The returned status and message preserve the
// create endpoint's existing validation and failure distinctions.
func (h *Handler) createPodcastFromBody(ctx context.Context, body map[string]any) (podcastItem, int, string, error) {
	media, _ := body["media"].(map[string]any)
	meta, _ := media["metadata"].(map[string]any)
	if meta == nil {
		return podcastItem{}, 400, `Invalid request body. "media" and "media.metadata" are required`, errors.New("missing podcast metadata")
	}
	libID, _ := body["libraryId"].(string)
	folderID, _ := body["folderId"].(string)
	rawPath, _ := body["path"].(string)
	if strings.TrimSpace(rawPath) == "" {
		return podcastItem{}, 400, `Invalid request body. "path" must be a non-empty string`, errors.New("empty podcast path")
	}
	var folderPath, folderLibrary string
	var libraryType string
	err := h.db.QueryRowContext(ctx, `SELECT f.path,f.libraryId,l.mediaType FROM libraryFolders f JOIN libraries l ON l.id=f.libraryId WHERE f.id=?`, folderID).Scan(&folderPath, &folderLibrary, &libraryType)
	if errors.Is(err, sql.ErrNoRows) {
		return podcastItem{}, 404, "Folder not found", err
	}
	if err != nil {
		return podcastItem{}, 500, "database read failed", err
	}
	if folderLibrary != libID || libraryType != "podcast" {
		return podcastItem{}, 404, "Folder not found", sql.ErrNoRows
	}
	root, err := filepath.Abs(filepath.Clean(folderPath))
	if err != nil {
		return podcastItem{}, 400, "Invalid podcast path", err
	}
	path, err := filepath.Abs(filepath.Clean(rawPath))
	if err != nil {
		return podcastItem{}, 400, "Invalid podcast path", err
	}
	rel, err := filepath.Rel(root, path)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
		return podcastItem{}, 400, "Podcast path must be inside the selected library folder", errors.New("podcast path outside library folder")
	}
	var existing int
	if err = h.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM libraryItems WHERE path=?`, filepath.ToSlash(path)).Scan(&existing); err != nil {
		return podcastItem{}, 500, "database read failed", err
	}
	if existing > 0 {
		return podcastItem{}, 400, "Podcast already exists", errPodcastExists
	}
	schedule, _ := body["autoDownloadSchedule"].(string)
	if schedule != "" && !validCron(schedule) {
		return podcastItem{}, 400, "Invalid auto download schedule cron expression", errors.New("invalid auto download schedule")
	}
	settings := h.serverSettings(ctx)
	if schedule == "" {
		schedule, _ = settings["podcastEpisodeSchedule"].(string)
		if schedule == "" {
			schedule = "0 * * * *"
		}
	}
	createdPath := false
	if _, e := os.Stat(path); os.IsNotExist(e) {
		if e = os.MkdirAll(path, 0755); e != nil {
			return podcastItem{}, 400, "Invalid podcast path", e
		}
		createdPath = true
	} else if e != nil {
		return podcastItem{}, 400, "Invalid podcast path", e
	}
	st, err := os.Stat(path)
	if err != nil || !st.IsDir() {
		if createdPath {
			_ = os.Remove(path)
		}
		return podcastItem{}, 400, "Invalid podcast path", errors.New("podcast path is not a directory")
	}
	now := time.Now().UTC()
	timestamp := dbtime.Format(now)
	id := newUUID()
	mediaID := newUUID()
	ino := strconv.FormatUint(uint64(st.ModTime().UnixNano()), 10)
	if raw := st.Sys(); raw != nil {
		if info, ok := raw.(*syscall.Stat_t); ok {
			ino = strconv.FormatUint(info.Ino, 10)
		}
	}
	title := anyString(meta["title"])
	sortTitle := title
	prefixes := []string{"the", "a"}
	if v, ok := settings["sortingPrefixes"].([]any); ok && len(v) > 0 {
		prefixes = nil
		for _, x := range v {
			if s, ok := x.(string); ok {
				prefixes = append(prefixes, strings.ToLower(s))
			}
		}
	}
	for _, prefix := range prefixes {
		if strings.HasPrefix(strings.ToLower(title), prefix+" ") {
			sortTitle = title[len(prefix)+1:]
			break
		}
	}
	stringsMap := map[string]string{"author": "author", "releaseDate": "releaseDate", "feedUrl": "feedURL", "imageUrl": "imageURL", "description": "description", "itunesPageUrl": "itunesPageURL", "itunesId": "itunesId", "itunesArtistId": "itunesArtistId", "language": "language", "type": "podcastType"}
	vals := map[string]any{}
	for source, target := range stringsMap {
		if s, ok := stringLike(meta[source]); ok && s != "" {
			vals[target] = s
		} else {
			vals[target] = nil
		}
	}
	if desc, ok := vals["description"].(string); ok {
		sanitized, _ := sanitizeHTML(desc)
		vals["description"] = sanitized
	}
	genres := stringArray(media["metadata"].(map[string]any)["genres"])
	tags := stringArray(body["tags"])
	genreJSON, _ := json.Marshal(genres)
	tagJSON, _ := json.Marshal(tags)
	explicit := meta["explicit"] == true
	auto := body["autoDownloadEpisodes"] == true
	tx, err := h.db.BeginTx(ctx, nil)
	if err != nil {
		if createdPath {
			_ = os.Remove(path)
		}
		return podcastItem{}, 500, "Failed to create podcast", err
	}
	rollback := func() {
		_ = tx.Rollback()
		if createdPath {
			_ = os.Remove(path)
		}
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO podcasts (id,title,titleIgnorePrefix,author,releaseDate,feedURL,imageURL,description,itunesPageURL,itunesId,itunesArtistId,language,podcastType,explicit,autoDownloadEpisodes,autoDownloadSchedule,lastEpisodeCheck,maxEpisodesToKeep,maxNewEpisodesToDownload,coverPath,tags,genres,numEpisodes,createdAt,updatedAt) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?, ?,?)`, mediaID, nullableAny(title), nullableAny(sortTitle), vals["author"], vals["releaseDate"], vals["feedURL"], vals["imageURL"], vals["description"], vals["itunesPageURL"], vals["itunesId"], vals["itunesArtistId"], vals["language"], vals["podcastType"], explicit, auto, schedule, timestamp, 0, 3, nil, string(tagJSON), string(genreJSON), 0, timestamp, timestamp)
	if err != nil {
		rollback()
		return podcastItem{}, 500, "Failed to create podcast", err
	}
	libRel := filepath.ToSlash(rel)
	if libRel == "." {
		libRel = ""
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO libraryItems (id,ino,path,relPath,mediaId,mediaType,isFile,isMissing,isInvalid,mtime,ctime,birthtime,size,libraryFiles,extraData,title,titleIgnorePrefix,libraryId,libraryFolderId,createdAt,updatedAt) VALUES (?,?,?,?,?,'podcast',0,0,0,?,?,?,0,'[]','{}',?,?,?,?,?,?)`, id, ino, filepath.ToSlash(path), libRel, mediaID, timestamp, timestamp, timestamp, nullableAny(title), nullableAny(sortTitle), libID, folderID, timestamp, timestamp)
	if err != nil {
		rollback()
		return podcastItem{}, 500, "Failed to create podcast", err
	}
	if err = tx.Commit(); err != nil {
		rollback()
		return podcastItem{}, 500, "Failed to create podcast", err
	}
	item, err := h.loadItem(ctx, id)
	if err != nil {
		return podcastItem{}, 500, "database read failed", err
	}
	h.emit("@admins", "item_added", item.oldJSON())
	return item, 200, "", nil
}

func (h *Handler) createFromOPML(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Feeds                []string `json:"feeds"`
		LibraryID            string   `json:"libraryId"`
		FolderID             string   `json:"folderId"`
		AutoDownloadEpisodes bool     `json:"autoDownloadEpisodes"`
	}
	if json.NewDecoder(io.LimitReader(r.Body, 8<<20)).Decode(&body) != nil || len(body.Feeds) == 0 {
		text(w, 400, `Invalid request body. "feeds" must be an array of RSS feed URLs`)
		return
	}
	for _, raw := range body.Feeds {
		if validFeedURL(raw) == "" {
			text(w, 400, `Invalid request body. "feeds" must be an array of RSS feed URLs`)
			return
		}
	}
	if body.LibraryID == "" || body.FolderID == "" {
		text(w, 400, `Invalid request body. "libraryId" and "folderId" are required`)
		return
	}
	var folderPath, libraryID string
	if err := h.db.QueryRowContext(r.Context(), `SELECT path,libraryId FROM libraryFolders WHERE id=?`, body.FolderID).Scan(&folderPath, &libraryID); errors.Is(err, sql.ErrNoRows) {
		text(w, 404, "Folder not found")
		return
	} else if err != nil {
		http.Error(w, "database read failed", 500)
		return
	}
	if libraryID != body.LibraryID {
		text(w, 404, "Folder not found")
		return
	}
	id := newUUID()
	started := time.Now().UnixMilli()
	task := map[string]any{"id": id, "action": "opml-import", "data": map[string]any{}, "title": "OPML import", "titleKey": "MessageTaskOpmlImport", "titleSubs": nil, "description": fmt.Sprintf("Creating podcasts from %d RSS feeds", len(body.Feeds)), "descriptionKey": "MessageTaskOpmlImportDescription", "descriptionSubs": []any{len(body.Feeds)}, "error": nil, "errorKey": nil, "errorSubs": nil, "showSuccess": true, "isFailed": false, "isFinished": false, "startedAt": started, "finishedAt": nil}
	h.emit("*", "task_started", task)
	w.WriteHeader(200)
	go h.runOPMLImport(body.Feeds, body.LibraryID, body.FolderID, folderPath, body.AutoDownloadEpisodes, task)
}

func (h *Handler) runOPMLImport(feeds []string, libraryID, folderID, folderPath string, auto bool, task map[string]any) {
	added := 0
	for _, feedURL := range feeds {
		feed, err := h.getFeed(context.Background(), feedURL, false)
		if err != nil {
			// PodcastManager.js:543-557: feed-fetch failures describe the feed URL.
			h.emitFailedOPMLFeed(feedURL, `Importing RSS feed "`+feedURL+`"`, "MessageTaskOpmlImportFeedDescription", "Failed to get podcast feed", "MessageTaskOpmlImportFeedFailed")
			continue
		}
		metadata, _ := feed["metadata"].(map[string]any)
		title := anyString(metadata["title"])
		if title == "" {
			h.emitFailedOPMLFeed(feedURL, `Importing RSS feed "`+feedURL+`"`, "MessageTaskOpmlImportFeedDescription", "Failed to get podcast feed", "MessageTaskOpmlImportFeedFailed")
			continue
		}
		path := filepath.Join(folderPath, sanitizePodcastFilename(title))
		payload := map[string]any{"libraryId": libraryID, "folderId": folderID, "path": filepath.ToSlash(path), "autoDownloadEpisodes": auto, "media": map[string]any{"autoDownloadEpisodes": auto, "metadata": map[string]any{"title": metadata["title"], "author": metadata["author"], "description": metadata["description"], "releaseDate": "", "genres": metadata["categories"], "feedUrl": metadata["feedUrl"], "imageUrl": metadata["image"], "itunesPageUrl": "", "itunesId": "", "itunesArtistId": "", "language": "", "numEpisodes": feed["numEpisodes"]}}}
		item, _, _, createErr := h.createPodcastFromBody(context.Background(), payload)
		if createErr == nil {
			added++
			if item.AutoDownload {
				h.addSchedule(item.ID)
			}
			continue
		}
		errorText := "Failed to create podcast library item"
		errorKey := "MessageTaskOpmlImportFeedPodcastFailed"
		if errors.Is(createErr, errPodcastExists) {
			errorText = "Podcast already exists at path"
			errorKey = "MessageTaskOpmlImportFeedPodcastExists"
		}
		// PodcastManager.js:565-584/605-622/662-678: creation failures describe
		// the podcast title, not the feed URL.
		h.emitFailedOPMLFeed(title, `Creating podcast "`+title+`"`, "MessageTaskOpmlImportFeedPodcastDescription", errorText, errorKey)
	}
	finished := time.Now().UnixMilli()
	task["description"] = fmt.Sprintf("Added %d podcasts", added)
	task["descriptionKey"] = "MessageTaskOpmlImportFinished"
	task["descriptionSubs"] = []any{added}
	task["isFinished"] = true
	task["finishedAt"] = finished
	h.emit("*", "task_finished", task)
}

func (h *Handler) emitFailedOPMLFeed(title, descriptionText, descriptionKey, errorText, errorKey string) {
	now := time.Now().UnixMilli()
	task := map[string]any{"id": newUUID(), "action": "opml-import-feed", "data": map[string]any{}, "title": "OPML import feed", "titleKey": "MessageTaskOpmlImportFeed", "titleSubs": nil, "description": descriptionText, "descriptionKey": descriptionKey, "descriptionSubs": []string{title}, "error": errorText, "errorKey": errorKey, "errorSubs": nil, "showSuccess": false, "isFailed": true, "isFinished": true, "startedAt": now, "finishedAt": now}
	h.emit("*", "task_started", task)
}

func sanitizePodcastFilename(title string) string {
	title = strings.TrimSpace(title)
	title = strings.NewReplacer("/", " ", "\\", " ", ":", " - ", "?", "", "*", "", "|", "", "<", "", ">", "", `"`, "").Replace(title)
	title = strings.Join(strings.Fields(title), " ")
	if title == "" || title == "." || title == ".." {
		return "Podcast"
	}
	return title
}

func (h *Handler) serverSettings(ctx context.Context) map[string]any {
	var raw []byte
	if err := h.db.QueryRowContext(ctx, `SELECT value FROM settings WHERE key='server-settings'`).Scan(&raw); err != nil {
		return map[string]any{}
	}
	var settings map[string]any
	_ = json.Unmarshal(raw, &settings)
	if settings == nil {
		settings = map[string]any{}
	}
	return settings
}

func (h *Handler) startSchedules() {
	if h.db == nil {
		return
	}
	h.scheduler = cron.New(cron.WithParser(cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow)))
	rows, err := h.db.Query(`SELECT li.id FROM libraryItems li JOIN podcasts p ON p.id=li.mediaId WHERE li.mediaType='podcast' AND p.autoDownloadEpisodes=1`)
	if err != nil {
		h.scheduler = nil
		return
	}
	var ids []string
	for rows.Next() {
		var id string
		if rows.Scan(&id) == nil {
			ids = append(ids, id)
		}
	}
	_ = rows.Close()
	for _, id := range ids {
		h.addSchedule(id)
	}
	h.scheduler.Start()
}

func (h *Handler) addSchedule(itemID string) {
	if h.scheduler == nil {
		return
	}
	item, err := h.loadItem(context.Background(), itemID)
	if err != nil || !item.AutoDownload || !item.AutoSchedule.Valid || !validCron(item.AutoSchedule.String) {
		return
	}
	entry, err := h.scheduler.AddFunc(item.AutoSchedule.String, func() { h.runScheduled(itemID) })
	if err == nil {
		h.queueMu.Lock()
		h.scheduleIDs[itemID] = entry
		h.queueMu.Unlock()
	}
}

func (h *Handler) runScheduled(itemID string) {
	item, err := h.loadItem(context.Background(), itemID)
	if err != nil || !item.FeedURL.Valid || !item.AutoDownload {
		return
	}
	feed, err := h.getFeed(context.Background(), item.FeedURL.String, false)
	if err != nil {
		h.queueMu.Lock()
		h.failedCheckMap[itemID]++
		failed := h.failedCheckMap[itemID]
		disable := h.maxFailedChecks > 0 && failed >= h.maxFailedChecks
		if disable {
			delete(h.failedCheckMap, itemID)
		}
		h.queueMu.Unlock()
		if disable {
			now := dbtime.Now()
			if _, err := h.db.Exec(`UPDATE podcasts SET autoDownloadEpisodes=0,lastEpisodeCheck=?,updatedAt=? WHERE id=?`, now, now, item.MediaID); err != nil {
				log.Printf("[podcasts] auto-download disable update failed for %s: %v", item.MediaID, err)
			}
			h.queueMu.Lock()
			entry, ok := h.scheduleIDs[itemID]
			if ok && h.scheduler != nil {
				h.scheduler.Remove(entry)
				delete(h.scheduleIDs, itemID)
			}
			h.queueMu.Unlock()
			if updated, loadErr := h.loadItem(context.Background(), itemID); loadErr == nil {
				h.emit("@admins", "item_updated", updated.oldJSON())
			}
		} else {
			now := dbtime.Now()
			if _, err := h.db.Exec(`UPDATE podcasts SET lastEpisodeCheck=?,updatedAt=? WHERE id=?`, now, now, item.MediaID); err != nil {
				log.Printf("[podcasts] episode check bookkeeping update failed for %s: %v", item.MediaID, err)
			}
		}
		return
	}
	h.queueMu.Lock()
	delete(h.failedCheckMap, itemID)
	h.queueMu.Unlock()
	after := int64(0)
	if v, ok := sqlMillis(item.LastEpisodeCheck).(int64); ok {
		after = v
	}
	for _, ep := range item.Episodes {
		if v, ok := sqlMillis(ep.PublishedAt).(int64); ok && v > after {
			after = v
		}
	}
	all, _ := feed["episodes"].([]map[string]any)
	found := filterNew(all, item, after, item.MaxNew)
	if len(found) > 0 {
		if err := h.enqueue(item, found, true); err != nil {
			log.Printf("[podcasts] scheduled episode enqueue failed for %s: %v", item.MediaID, err)
		}
	}
	now := dbtime.Now()
	if _, err := h.db.Exec(`UPDATE podcasts SET lastEpisodeCheck=?,updatedAt=? WHERE id=?`, now, now, item.MediaID); err != nil {
		log.Printf("[podcasts] episode check bookkeeping update failed for %s: %v", item.MediaID, err)
	}
}

func filterNew(all []map[string]any, item podcastItem, after int64, limit int) []map[string]any {
	known := map[string]bool{}
	for _, ep := range item.Episodes {
		var extra map[string]any
		_ = json.Unmarshal([]byte(ep.ExtraData.String), &extra)
		if guid, ok := extra["guid"].(string); ok && guid != "" {
			known["g:"+guid] = true
		}
		if ep.EnclosureURL.Valid {
			known["u:"+ep.EnclosureURL.String] = true
		}
	}
	found := []map[string]any{}
	for _, ep := range all {
		var published int64
		switch n := ep["publishedAt"].(type) {
		case int64:
			published = n
		case float64:
			published = int64(n)
		}
		enc, _ := ep["enclosure"].(map[string]any)
		guid, _ := ep["guid"].(string)
		urlValue, _ := enc["url"].(string)
		if published <= after || known["g:"+guid] || known["u:"+urlValue] {
			continue
		}
		found = append(found, ep)
		if limit > 0 && len(found) >= limit {
			break
		}
	}
	return found
}

func validCron(value string) bool {
	parser := cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow)
	_, err := parser.Parse(value)
	return err == nil
}
func stringLike(v any) (string, bool) {
	switch x := v.(type) {
	case string:
		return x, true
	case float64:
		return strconv.FormatFloat(x, 'f', -1, 64), true
	default:
		return "", false
	}
}
func anyString(v any) string { s, _ := stringLike(v); return s }
func stringArray(v any) []string {
	arr, ok := v.([]any)
	if !ok {
		return []string{}
	}
	out := make([]string, 0, len(arr))
	for _, item := range arr {
		s, ok := item.(string)
		if !ok || s == "" {
			return []string{}
		}
		out = append(out, s)
	}
	return out
}
func nullableAny(v string) any {
	if v == "" {
		return nil
	}
	return v
}
func newUUID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	hex := fmt.Sprintf("%x", b)
	return hex[:8] + "-" + hex[8:12] + "-" + hex[12:16] + "-" + hex[16:20] + "-" + hex[20:]
}

func (h *Handler) getFeed(ctx context.Context, rawURL string, excludeEpisodes bool) (map[string]any, error) {
	feedURL := validFeedURL(rawURL)
	if feedURL == "" {
		return nil, errors.New("invalid feed URL")
	}
	if !h.allowPrivateNetwork {
		if err := rejectPrivateHost(ctx, feedURL); err != nil {
			return nil, err
		}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, feedURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/rss+xml, application/xhtml+xml, application/xml, */*;q=0.8")
	req.Header.Set("Accept-Encoding", "gzip, deflate")
	agent := "audiobookshelf (+https://audiobookshelf.org; like iTMS)"
	if strings.HasPrefix(feedURL, "https://www.cbc.ca") {
		agent += " - CBC"
	}
	req.Header.Set("User-Agent", agent)
	resp, err := h.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("feed returned %s", resp.Status)
	}
	limit := int64(32 << 20)
	raw, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(raw)) > limit {
		return nil, errors.New("podcast feed is too large")
	}
	feed, err := parseFeed(raw, feedURL)
	if err != nil {
		return nil, err
	}
	if excludeEpisodes {
		feed["numEpisodes"] = len(feed["episodes"].([]map[string]any))
		delete(feed, "episodes")
	}
	return feed, nil
}

func secureClient() *http.Client {
	transport := &http.Transport{DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(address)
		if err != nil {
			return nil, err
		}
		ips, err := net.DefaultResolver.LookupIPAddr(ctx, host)
		if err != nil {
			return nil, err
		}
		var last error
		for _, ip := range ips {
			if ip.IP.IsPrivate() || ip.IP.IsLoopback() || ip.IP.IsLinkLocalUnicast() || ip.IP.IsLinkLocalMulticast() || ip.IP.IsUnspecified() {
				last = fmt.Errorf("private or non-routable feed host blocked")
				continue
			}
			conn, e := (&net.Dialer{Timeout: 8 * time.Second, KeepAlive: 30 * time.Second}).DialContext(ctx, network, net.JoinHostPort(ip.IP.String(), port))
			if e == nil {
				return conn, nil
			}
			last = e
		}
		if last == nil {
			last = errors.New("feed host has no public IP")
		}
		return nil, last
	}}
	return &http.Client{Transport: transport, Timeout: 20 * time.Second, CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if len(via) >= 5 {
			return errors.New("too many redirects")
		}
		if err := rejectPrivateHost(req.Context(), req.URL.String()); err != nil {
			return err
		}
		return nil
	}}
}

func rejectPrivateHost(ctx context.Context, raw string) error {
	u, err := url.Parse(raw)
	if err != nil {
		return err
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return errors.New("feed URL must use HTTP or HTTPS")
	}
	host := u.Hostname()
	if host == "" {
		return errors.New("feed URL has no host")
	}
	ips, err := net.DefaultResolver.LookupIPAddr(ctx, host)
	if err != nil {
		return err
	}
	for _, x := range ips {
		if x.IP.IsPrivate() || x.IP.IsLoopback() || x.IP.IsLinkLocalUnicast() || x.IP.IsLinkLocalMulticast() || x.IP.IsUnspecified() {
			return errors.New("private or non-routable feed host blocked")
		}
	}
	return nil
}

func validFeedURL(raw string) string {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u == nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return ""
	}
	return u.String()
}
func admin(u store.User) bool { return u.Type == "admin" || u.Type == "root" }
func text(w http.ResponseWriter, status int, s string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	_, _ = io.WriteString(w, s)
}
func jsonResponse(w http.ResponseWriter, status int, v any) {
	b, err := json.Marshal(v)
	if err != nil {
		http.Error(w, "internal server error", 500)
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_, _ = w.Write(b)
}

// SetHTTPClientForTests allows a local fixture server to exercise feed parsing
// without weakening the production private-address checks.
func (h *Handler) SetHTTPClientForTests(c *http.Client) { h.client = c; h.allowPrivateNetwork = true }
