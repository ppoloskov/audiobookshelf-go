// Package podcasts implements the podcast RSS and episode service surfaces.
// It keeps the pinned SQLite schema and public response shapes at the boundary.
package podcasts

import (
	"audiobookshelf-compat/internal/access"
	"audiobookshelf-compat/internal/serversettings"
	"bufio"
	"compress/flate"
	"compress/gzip"
	"compress/zlib"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
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
	"audiobookshelf-compat/internal/cronspec"
	"audiobookshelf-compat/internal/db/sqlc"
	"audiobookshelf-compat/internal/dbtime"
	"audiobookshelf-compat/internal/events"
	"audiobookshelf-compat/internal/httputil"
	"audiobookshelf-compat/internal/ids"
	"audiobookshelf-compat/internal/libitem"
	"audiobookshelf-compat/internal/logging"
	"audiobookshelf-compat/internal/store"
	"audiobookshelf-compat/internal/textclean"
	"audiobookshelf-compat/internal/textdist"
	"audiobookshelf-compat/internal/wire"
	"github.com/robfig/cron/v3"
)

var errPodcastExists = errors.New("podcast already exists")

type Handler struct {
	db                  *sql.DB
	cfg                 config.Config
	emit                events.Emitter
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

func New(db *sql.DB, cfg config.Config, emit events.Emitter) *Handler {
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

// Routes registers the podcast routes under /api/podcasts and the two
// podcast-only library routes. The server attaches the user to the request
// context before dispatch (store.ContextWithUser).
func (h *Handler) Routes(mux *http.ServeMux) {
	adminOnly := func(fn httputil.Handler) httputil.Handler {
		return func(w http.ResponseWriter, r *http.Request) error {
			if !access.AdminUser(requestUser(r)) {
				return httputil.Status(403, "")
			}
			return fn(w, r)
		}
	}
	mux.Handle("GET /api/libraries/{id}/recent-episodes", h.libraryRoute(true))
	mux.Handle("GET /api/libraries/{id}/episode-downloads", h.libraryRoute(false))
	mux.Handle("POST /api/podcasts/feed", adminOnly(h.feed))
	mux.Handle("POST /api/podcasts", adminOnly(h.createPodcast))
	mux.Handle("POST /api/podcasts/opml/parse", adminOnly(h.parseOPML))
	mux.Handle("POST /api/podcasts/opml/create", adminOnly(h.createFromOPML))
	mux.Handle("GET /api/podcasts/{id}/downloads", h.onItem(func(w http.ResponseWriter, r *http.Request, item libitem.Item) error {
		return httputil.JSON(w, r, wire.EpisodeDownloads{Downloads: h.downloadsFor(item.ID)})
	}))
	mux.Handle("GET /api/podcasts/{id}/clear-queue", h.onItem(func(w http.ResponseWriter, r *http.Request, item libitem.Item) error {
		h.clearQueue(item.ID)
		return httputil.OK(w, r)
	}))
	mux.Handle("POST /api/podcasts/{id}/download-episodes", h.onItem(h.downloadEpisodes))
	mux.Handle("GET /api/podcasts/{id}/search-episode", h.onItem(h.searchEpisode))
	mux.Handle("POST /api/podcasts/{id}/match-episodes", h.onItem(h.matchEpisodes))
	mux.Handle("GET /api/podcasts/{id}/checknew", h.onItem(h.checkNew))
	mux.Handle("GET /api/podcasts/{id}/episode/{episodeId}", h.onItem(func(w http.ResponseWriter, r *http.Request, item libitem.Item) error {
		ep, ok := episodeOf(item, r.PathValue("episodeId"))
		if !ok {
			return httputil.Status(404, "")
		}
		return httputil.JSON(w, r, ep.Expanded(item.ID).EpisodeJSON)
	}))
	mux.Handle("PATCH /api/podcasts/{id}/episode/{episodeId}", h.onItem(h.updateEpisode))
	mux.Handle("DELETE /api/podcasts/{id}/episode/{episodeId}", h.onItem(h.removeEpisode))
}

// onItem is PodcastController.middleware (server/controllers/PodcastController.js:
// 503-530): unknown item 404, non-podcast item 500, no library access 403,
// then the update/delete permission by HTTP method.
func (h *Handler) onItem(fn func(http.ResponseWriter, *http.Request, libitem.Item) error) httputil.Handler {
	return func(w http.ResponseWriter, r *http.Request) error {
		user, id := requestUser(r), r.PathValue("id")
		item, err := h.loadItem(r.Context(), id)
		if errors.Is(err, sql.ErrNoRows) {
			var other string
			if e := h.db.QueryRowContext(r.Context(), `SELECT mediaType FROM libraryItems WHERE id=?`, id).Scan(&other); e == nil {
				// PodcastController.middleware answers a bare 500 for a non-podcast
				// item (PodcastController.js:565); upstream logs nothing.
				logging.RequestDebug(r, "responding 500: library item "+id+" is a "+other+" item, not a podcast")
				return &httputil.Error{Status: 500}
			}
			return httputil.Status(404, "")
		}
		if err != nil {
			return httputil.Internal("database read failed", err)
		}
		perms := access.Of(user)
		if !perms.IsAdmin() && !perms.Item(item.Access()) ||
			r.Method == http.MethodDelete && !perms.IsAdmin() && !perms.Delete() ||
			(r.Method == http.MethodPatch || r.Method == http.MethodPost) && !perms.IsAdmin() && !perms.Update() {
			return httputil.Status(403, "")
		}
		return fn(w, r, item)
	}
}

func (h *Handler) feed(w http.ResponseWriter, r *http.Request) error {
	var body struct {
		RSSFeed string `json:"rssFeed"`
	}
	if json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&body) != nil || validFeedURL(body.RSSFeed) == "" {
		return httputil.Status(400, `Invalid request body. "rssFeed" must be a valid URL`)
	}
	feed, err := h.getFeed(r.Context(), body.RSSFeed, false)
	if err != nil {
		return httputil.Status(404, "Podcast RSS feed request failed or invalid response data")
	}
	return httputil.JSON(w, r, map[string]any{"podcast": feed})
}

func (h *Handler) parseOPML(w http.ResponseWriter, r *http.Request) error {
	var body struct {
		OPMLText string `json:"opmlText"`
	}
	if json.NewDecoder(io.LimitReader(r.Body, 4<<20)).Decode(&body) != nil || body.OPMLText == "" {
		return httputil.Status(400, "")
	}
	return httputil.JSON(w, r, map[string]any{"feeds": parseOPML(body.OPMLText)})
}

func (h *Handler) downloadEpisodes(w http.ResponseWriter, r *http.Request, item libitem.Item) error {
	if !access.AdminUser(requestUser(r)) {
		return httputil.Status(403, "")
	}
	var episodes []map[string]any
	if json.NewDecoder(io.LimitReader(r.Body, 8<<20)).Decode(&episodes) != nil || len(episodes) == 0 {
		return httputil.Status(400, "")
	}
	if err := h.enqueue(item, episodes, false); err != nil {
		return httputil.Status(400, err.Error())
	}
	return httputil.OK(w, r)
}

func (h *Handler) searchEpisode(w http.ResponseWriter, r *http.Request, item libitem.Item) error {
	if str(item.Podcast.FeedURL) == "" {
		return httputil.Status(400, "Podcast does not have an RSS feed URL")
	}
	title := r.URL.Query().Get("title")
	if title == "" {
		logging.RequestDebug(r, "responding 500: query parameter title is empty")
		return &httputil.Error{Status: 500}
	}
	feed, err := h.getFeed(r.Context(), str(item.Podcast.FeedURL), false)
	if err != nil {
		return httputil.JSON(w, r, map[string]any{"episodes": nil})
	}
	return httputil.JSON(w, r, map[string]any{"episodes": matchEpisodes(feed, title)})
}

func (h *Handler) matchEpisodes(w http.ResponseWriter, r *http.Request, item libitem.Item) error {
	h.queueMu.Lock()
	matcher := h.quickMatch
	h.queueMu.Unlock()
	if matcher == nil {
		http.Error(w, "Podcast episode matcher unavailable", http.StatusServiceUnavailable)
		return nil
	}
	updated, err := matcher(r.Context(), item.ID, r.URL.Query().Get("override") == "1")
	if err != nil {
		return httputil.Internal("Podcast episode matching failed", err)
	}
	if updated > 0 {
		if fresh, e := h.loadItem(r.Context(), item.ID); e == nil {
			h.emit("@admins", "item_updated", h.expanded(r.Context(), fresh))
		}
	}
	return httputil.JSON(w, r, map[string]any{"numEpisodesUpdated": updated})
}

func (h *Handler) updateEpisode(w http.ResponseWriter, r *http.Request, item libitem.Item) error {
	episodeID := r.PathValue("episodeId")
	ep, ok := episodeOf(item, episodeID)
	if !ok {
		return httputil.Status(404, "Episode not found")
	}
	var body map[string]any
	if json.NewDecoder(io.LimitReader(r.Body, 2<<20)).Decode(&body) != nil {
		return httputil.Status(400, "Invalid request body")
	}
	fields := map[string]any{}
	allowed := map[string]string{"title": "title", "subtitle": "subtitle", "description": "description", "pubDate": "pubDate", "episode": "episode", "season": "season", "episodeType": "episodeType"}
	for key, column := range allowed {
		if v, exists := body[key]; exists {
			if s, ok := v.(string); ok {
				if (key == "description" || key == "subtitle") && s != "" {
					s = textclean.Sanitize(s)
				}
				old := store.StringPtrValue(episodeStringField(ep, key))
				if old != s {
					fields[column] = s
				}
			}
		}
	}
	if raw, exists := body["enclosure"]; exists {
		if raw == nil {
			if ep.Enclosure != nil {
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
				enc := ep.Enclosure
				if enc == nil || enc.URL != u {
					fields["enclosureURL"] = u
				}
				if enc == nil || enc.Length == nil || size == nil || *enc.Length != strconv.FormatInt(size.(int64), 10) {
					fields["enclosureSize"] = size
				}
				if enc == nil || enc.Type == nil || *enc.Type != typ {
					fields["enclosureType"] = store.NullIfEmpty(typ)
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
				old, _ := json.Marshal(ep.Chapters)
				if string(encoded) != string(old) {
					fields["chapters"] = string(encoded)
				}
			}
		}
	}
	if n, ok := body["publishedAt"].(float64); ok {
		millis := int64(n)
		if ep.PublishedAt == nil || *ep.PublishedAt != millis {
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
			return httputil.Internal("database write failed", err)
		}
		updated, err := h.loadItem(r.Context(), item.ID)
		if err == nil {
			h.emit("@admins", "item_updated", h.expanded(r.Context(), updated))
			item = updated
		}
	}
	return httputil.JSON(w, r, h.expanded(r.Context(), item))
}

func episodeStringField(e libitem.Episode, key string) *string {
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

func (h *Handler) removeEpisode(w http.ResponseWriter, r *http.Request, item libitem.Item) error {
	episodeID := r.PathValue("episodeId")
	ep, ok := episodeOf(item, episodeID)
	if !ok {
		return httputil.Status(404, "")
	}
	if r.URL.Query().Get("hard") == "1" {
		metadata, _ := ep.AudioFile["metadata"].(map[string]any)
		if path, ok := metadata["path"].(string); ok && path != "" {
			_ = os.Remove(path)
		}
	}
	ino := fmt.Sprint(ep.AudioFile["ino"])
	files := []map[string]any{}
	for _, f := range h.rawLibraryFiles(r.Context(), item.ID) {
		if f["ino"] != ino {
			files = append(files, f)
		}
	}
	fileBytes, _ := json.Marshal(files)
	tx, err := h.db.BeginTx(r.Context(), nil)
	if err != nil {
		return httputil.Internal("database write failed", err)
	}
	now := dbtime.Now()
	if err = sqlc.New(tx).DeletePlaylistMediaItemsByMediaID(r.Context(), episodeID); err == nil {
		_, err = tx.ExecContext(r.Context(), `DELETE FROM mediaProgresses WHERE mediaItemId=?`, episodeID)
	}
	if err == nil {
		_, err = tx.ExecContext(r.Context(), `DELETE FROM podcastEpisodes WHERE id=?`, episodeID)
	}
	if err == nil {
		_, err = tx.ExecContext(r.Context(), `UPDATE libraryItems SET libraryFiles=?,updatedAt=? WHERE id=?`, string(fileBytes), now, item.ID)
	}
	if err == nil {
		_, err = tx.ExecContext(r.Context(), `UPDATE podcasts SET numEpisodes=?,updatedAt=? WHERE id=?`, len(item.Podcast.Episodes)-1, now, item.MediaID)
	}
	if err != nil {
		_ = tx.Rollback()
		return httputil.Internal("database write failed", err)
	}
	if err = tx.Commit(); err != nil {
		return httputil.Internal("database write failed", err)
	}
	updated, err := h.loadItem(r.Context(), item.ID)
	if err != nil {
		return httputil.Internal("database read failed", err)
	}
	h.emit("@admins", "item_updated", h.expanded(r.Context(), updated))
	return httputil.JSON(w, r, updated.Full())
}

func (h *Handler) checkNew(w http.ResponseWriter, r *http.Request, item libitem.Item) error {
	if !access.AdminUser(requestUser(r)) {
		return httputil.Status(403, "")
	}
	if str(item.Podcast.FeedURL) == "" {
		return httputil.Status(400, "Podcast has no rss feed url")
	}
	feed, err := h.getFeed(r.Context(), str(item.Podcast.FeedURL), false)
	if err != nil {
		return httputil.JSON(w, r, map[string]any{"episodes": []any{}})
	}
	afterMS := int64(0)
	if t := item.Podcast.LastEpisodeCheck; t != nil {
		afterMS = *t
	}
	known := knownEpisodes(item)
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
		return httputil.Internal("database write failed", err)
	}
	if len(found) > 0 {
		if err := h.enqueue(item, found, false); err != nil {
			logging.Errorf("[podcasts] manual episode check enqueue failed for %s: %v", item.ID, err)
		}
	}
	return httputil.JSON(w, r, map[string]any{"episodes": found})
}

func episodeOf(it libitem.Item, id string) (libitem.Episode, bool) {
	for _, ep := range it.Podcast.Episodes {
		if ep.ID == id {
			return ep, true
		}
	}
	return libitem.Episode{}, false
}

// knownEpisodes keys an item's episodes by guid and enclosure URL.
func knownEpisodes(it libitem.Item) map[string]bool {
	known := map[string]bool{}
	for _, ep := range it.Podcast.Episodes {
		if ep.GUID != "" {
			known["g:"+ep.GUID] = true
		}
		if ep.Enclosure != nil {
			known["u:"+ep.Enclosure.URL] = true
		}
	}
	return known
}

// loadItem loads a podcast item with its episodes; any other item is not found.
func (h *Handler) loadItem(ctx context.Context, id string) (libitem.Item, error) {
	it, err := libitem.LoadOne(ctx, h.db, id, libitem.Options{Episodes: true})
	if err == nil && it.Podcast == nil {
		err = sql.ErrNoRows
	}
	return it, err
}

// expanded is the item's socket and detail JSON.
func (h *Handler) expanded(ctx context.Context, it libitem.Item) libitem.Expanded {
	return it.Expanded(serversettings.Load(ctx, h.db).SortingPrefixes)
}

// rawLibraryFiles reads the stored libraryFiles without the derived fileType.
func (h *Handler) rawLibraryFiles(ctx context.Context, itemID string) []map[string]any {
	var raw []byte
	_ = h.db.QueryRowContext(ctx, `SELECT libraryFiles FROM libraryItems WHERE id=?`, itemID).Scan(&raw)
	return store.DecodeJSONSlice[map[string]any](raw)
}

func str(s *string) string {
	if s == nil {
		return ""
	}
	return *s
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
	distance := textdist.Levenshtein(a, b)
	maxLen := len([]rune(a))
	if n := len([]rune(b)); n > maxLen {
		maxLen = n
	}
	return 1 - float64(distance)/float64(maxLen)
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

func (h *Handler) createPodcast(w http.ResponseWriter, r *http.Request) error {
	var body map[string]any
	if json.NewDecoder(io.LimitReader(r.Body, 4<<20)).Decode(&body) != nil {
		return httputil.Status(400, "Invalid request body")
	}
	item, status, message, err := h.createPodcastFromBody(r.Context(), body)
	if status == http.StatusInternalServerError && err != nil {
		return httputil.Internal(message, err)
	}
	if err != nil {
		return httputil.Status(status, message)
	}
	if item.Podcast.AutoDownloadEpisodes {
		h.addSchedule(item.ID)
	}
	return httputil.JSON(w, r, h.expanded(r.Context(), item))
}

// createPodcastFromBody performs podcast creation without coupling the operation
// to an HTTP request or response. The returned status and message preserve the
// create endpoint's existing validation and failure distinctions.
func (h *Handler) createPodcastFromBody(ctx context.Context, body map[string]any) (libitem.Item, int, string, error) {
	media, _ := body["media"].(map[string]any)
	meta, _ := media["metadata"].(map[string]any)
	if meta == nil {
		return libitem.Item{}, 400, `Invalid request body. "media" and "media.metadata" are required`, errors.New("missing podcast metadata")
	}
	libID, _ := body["libraryId"].(string)
	folderID, _ := body["folderId"].(string)
	rawPath, _ := body["path"].(string)
	if strings.TrimSpace(rawPath) == "" {
		return libitem.Item{}, 400, `Invalid request body. "path" must be a non-empty string`, errors.New("empty podcast path")
	}
	var folderPath, folderLibrary string
	var libraryType string
	err := h.db.QueryRowContext(ctx, `SELECT f.path,f.libraryId,l.mediaType FROM libraryFolders f JOIN libraries l ON l.id=f.libraryId WHERE f.id=?`, folderID).Scan(&folderPath, &folderLibrary, &libraryType)
	if errors.Is(err, sql.ErrNoRows) {
		return libitem.Item{}, 404, "Folder not found", err
	}
	if err != nil {
		return libitem.Item{}, 500, "database read failed", err
	}
	if folderLibrary != libID || libraryType != "podcast" {
		return libitem.Item{}, 404, "Folder not found", sql.ErrNoRows
	}
	root, err := filepath.Abs(filepath.Clean(folderPath))
	if err != nil {
		return libitem.Item{}, 400, "Invalid podcast path", err
	}
	path, err := filepath.Abs(filepath.Clean(rawPath))
	if err != nil {
		return libitem.Item{}, 400, "Invalid podcast path", err
	}
	rel, err := filepath.Rel(root, path)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
		return libitem.Item{}, 400, "Podcast path must be inside the selected library folder", errors.New("podcast path outside library folder")
	}
	var existing int
	if err = h.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM libraryItems WHERE path=?`, filepath.ToSlash(path)).Scan(&existing); err != nil {
		return libitem.Item{}, 500, "database read failed", err
	}
	if existing > 0 {
		return libitem.Item{}, 400, "Podcast already exists", errPodcastExists
	}
	schedule, _ := body["autoDownloadSchedule"].(string)
	if schedule != "" && !cronspec.Valid(schedule) {
		return libitem.Item{}, 400, "Invalid auto download schedule cron expression", errors.New("invalid auto download schedule")
	}
	settings := serversettings.Load(ctx, h.db).Doc
	if schedule == "" {
		schedule, _ = settings["podcastEpisodeSchedule"].(string)
		if schedule == "" {
			schedule = "0 * * * *"
		}
	}
	createdPath := false
	if _, e := os.Stat(path); os.IsNotExist(e) {
		if e = os.MkdirAll(path, 0755); e != nil {
			return libitem.Item{}, 400, "Invalid podcast path", e
		}
		createdPath = true
	} else if e != nil {
		return libitem.Item{}, 400, "Invalid podcast path", e
	}
	st, err := os.Stat(path)
	if err != nil || !st.IsDir() {
		if createdPath {
			_ = os.Remove(path)
		}
		return libitem.Item{}, 400, "Invalid podcast path", errors.New("podcast path is not a directory")
	}
	now := time.Now().UTC()
	timestamp := dbtime.Format(now)
	id, err := ids.New()
	var mediaID string
	if err == nil {
		mediaID, err = ids.New()
	}
	if err != nil {
		if createdPath {
			_ = os.Remove(path)
		}
		return libitem.Item{}, 500, "Failed to create podcast", err
	}
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
		sanitized := textclean.Sanitize(desc)
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
		return libitem.Item{}, 500, "Failed to create podcast", err
	}
	rollback := func() {
		_ = tx.Rollback()
		if createdPath {
			_ = os.Remove(path)
		}
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO podcasts (id,title,titleIgnorePrefix,author,releaseDate,feedURL,imageURL,description,itunesPageURL,itunesId,itunesArtistId,language,podcastType,explicit,autoDownloadEpisodes,autoDownloadSchedule,lastEpisodeCheck,maxEpisodesToKeep,maxNewEpisodesToDownload,coverPath,tags,genres,numEpisodes,createdAt,updatedAt) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?, ?,?)`, mediaID, store.NullIfEmpty(title), store.NullIfEmpty(sortTitle), vals["author"], vals["releaseDate"], vals["feedURL"], vals["imageURL"], vals["description"], vals["itunesPageURL"], vals["itunesId"], vals["itunesArtistId"], vals["language"], vals["podcastType"], explicit, auto, schedule, timestamp, 0, 3, nil, string(tagJSON), string(genreJSON), 0, timestamp, timestamp)
	if err != nil {
		rollback()
		return libitem.Item{}, 500, "Failed to create podcast", err
	}
	libRel := filepath.ToSlash(rel)
	if libRel == "." {
		libRel = ""
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO libraryItems (id,ino,path,relPath,mediaId,mediaType,isFile,isMissing,isInvalid,mtime,ctime,birthtime,size,libraryFiles,extraData,title,titleIgnorePrefix,libraryId,libraryFolderId,createdAt,updatedAt) VALUES (?,?,?,?,?,'podcast',0,0,0,?,?,?,0,'[]','{}',?,?,?,?,?,?)`, id, ino, filepath.ToSlash(path), libRel, mediaID, timestamp, timestamp, timestamp, store.NullIfEmpty(title), store.NullIfEmpty(sortTitle), libID, folderID, timestamp, timestamp)
	if err != nil {
		rollback()
		return libitem.Item{}, 500, "Failed to create podcast", err
	}
	if err = tx.Commit(); err != nil {
		rollback()
		return libitem.Item{}, 500, "Failed to create podcast", err
	}
	item, err := h.loadItem(ctx, id)
	if err != nil {
		return libitem.Item{}, 500, "database read failed", err
	}
	h.emit("@admins", "item_added", h.expanded(ctx, item))
	return item, 200, "", nil
}

func (h *Handler) createFromOPML(w http.ResponseWriter, r *http.Request) error {
	var body struct {
		Feeds                []string `json:"feeds"`
		LibraryID            string   `json:"libraryId"`
		FolderID             string   `json:"folderId"`
		AutoDownloadEpisodes bool     `json:"autoDownloadEpisodes"`
	}
	if json.NewDecoder(io.LimitReader(r.Body, 8<<20)).Decode(&body) != nil || len(body.Feeds) == 0 {
		return httputil.Status(400, `Invalid request body. "feeds" must be an array of RSS feed URLs`)
	}
	for _, raw := range body.Feeds {
		if validFeedURL(raw) == "" {
			return httputil.Status(400, `Invalid request body. "feeds" must be an array of RSS feed URLs`)
		}
	}
	if body.LibraryID == "" || body.FolderID == "" {
		return httputil.Status(400, `Invalid request body. "libraryId" and "folderId" are required`)
	}
	var folderPath, libraryID string
	if err := h.db.QueryRowContext(r.Context(), `SELECT path,libraryId FROM libraryFolders WHERE id=?`, body.FolderID).Scan(&folderPath, &libraryID); errors.Is(err, sql.ErrNoRows) {
		return httputil.Status(404, "Folder not found")
	} else if err != nil {
		return httputil.Internal("database read failed", err)
	}
	if libraryID != body.LibraryID {
		return httputil.Status(404, "Folder not found")
	}
	id, err := ids.New()
	if err != nil {
		return &httputil.Error{Status: 500, Cause: err}
	}
	started := time.Now().UnixMilli()
	task := map[string]any{"id": id, "action": "opml-import", "data": map[string]any{}, "title": "OPML import", "titleKey": "MessageTaskOpmlImport", "titleSubs": nil, "description": fmt.Sprintf("Creating podcasts from %d RSS feeds", len(body.Feeds)), "descriptionKey": "MessageTaskOpmlImportDescription", "descriptionSubs": []any{len(body.Feeds)}, "error": nil, "errorKey": nil, "errorSubs": nil, "showSuccess": true, "isFailed": false, "isFinished": false, "startedAt": started, "finishedAt": nil}
	h.emit("*", "task_started", task)
	go h.runOPMLImport(body.Feeds, body.LibraryID, body.FolderID, folderPath, body.AutoDownloadEpisodes, task)
	return httputil.OK(w, r)
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
			if item.Podcast.AutoDownloadEpisodes {
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
	taskID, err := ids.New()
	if err != nil {
		logging.Errorf("[PodcastManager] could not create an id for the OPML feed failure task: %v", err)
		return
	}
	task := map[string]any{"id": taskID, "action": "opml-import-feed", "data": map[string]any{}, "title": "OPML import feed", "titleKey": "MessageTaskOpmlImportFeed", "titleSubs": nil, "description": descriptionText, "descriptionKey": descriptionKey, "descriptionSubs": []string{title}, "error": errorText, "errorKey": errorKey, "errorSubs": nil, "showSuccess": false, "isFailed": true, "isFinished": true, "startedAt": now, "finishedAt": now}
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

func (h *Handler) startSchedules() {
	if h.db == nil {
		return
	}
	h.scheduler = cronspec.New()
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
	if err != nil || !item.Podcast.AutoDownloadEpisodes || !cronspec.Valid(str(item.Podcast.AutoDownloadSchedule)) {
		return
	}
	schedule := str(item.Podcast.AutoDownloadSchedule)
	entry, err := h.scheduler.AddFunc(schedule, func() { h.runScheduled(itemID) })
	if err == nil {
		h.queueMu.Lock()
		h.scheduleIDs[itemID] = entry
		h.queueMu.Unlock()
		logging.Infof("[CronManager] Added podcast %q to auto dl episode cron %q", str(item.Podcast.Title), schedule) // CronManager.js:249
	} else {
		logging.Errorf("[PodcastManager] Failed to schedule podcast cron %s: %v", schedule, err) // CronManager.js:175
	}
}

func (h *Handler) runScheduled(itemID string) {
	item, err := h.loadItem(context.Background(), itemID)
	if err != nil || item.Podcast.FeedURL == nil || !item.Podcast.AutoDownloadEpisodes {
		return
	}
	after := int64(0)
	if v := item.Podcast.LastEpisodeCheck; v != nil {
		after = *v
	}
	logging.Infof("[PodcastManager] runEpisodeCheck: %q | Last check: %v", str(item.Podcast.Title), time.UnixMilli(after).UTC().Format(time.RFC3339)) // PodcastManager.js:329
	feed, err := h.getFeed(context.Background(), str(item.Podcast.FeedURL), false)
	if err != nil {
		logging.Errorf("[PodcastManager] Failed to get feed for podcast %q (%s): %v", str(item.Podcast.Title), str(item.Podcast.FeedURL), err)
		h.queueMu.Lock()
		h.failedCheckMap[itemID]++
		failed := h.failedCheckMap[itemID]
		disable := h.maxFailedChecks > 0 && failed >= h.maxFailedChecks
		if disable {
			delete(h.failedCheckMap, itemID)
		}
		h.queueMu.Unlock()
		if disable {
			logging.Errorf("[PodcastManager] runEpisodeCheck %d failed attempts at checking episodes for %q - disabling auto download", failed, str(item.Podcast.Title)) // PodcastManager.js:343
			now := dbtime.Now()
			if _, err := h.db.Exec(`UPDATE podcasts SET autoDownloadEpisodes=0,lastEpisodeCheck=?,updatedAt=? WHERE id=?`, now, now, item.MediaID); err != nil {
				logging.Errorf("[podcasts] auto-download disable update failed for %s: %v", item.MediaID, err)
			}
			h.queueMu.Lock()
			entry, ok := h.scheduleIDs[itemID]
			if ok && h.scheduler != nil {
				h.scheduler.Remove(entry)
				delete(h.scheduleIDs, itemID)
			}
			h.queueMu.Unlock()
			if updated, loadErr := h.loadItem(context.Background(), itemID); loadErr == nil {
				h.emit("@admins", "item_updated", h.expanded(context.Background(), updated))
			}
		} else {
			now := dbtime.Now()
			if _, err := h.db.Exec(`UPDATE podcasts SET lastEpisodeCheck=?,updatedAt=? WHERE id=?`, now, now, item.MediaID); err != nil {
				logging.Errorf("[podcasts] episode check bookkeeping update failed for %s: %v", item.MediaID, err)
			}
		}
		return
	}
	h.queueMu.Lock()
	delete(h.failedCheckMap, itemID)
	h.queueMu.Unlock()
	for _, ep := range item.Podcast.Episodes {
		if v := ep.PublishedAt; v != nil && *v > after {
			after = *v
		}
	}
	all, _ := feed["episodes"].([]map[string]any)
	found := filterNew(all, item, after, int(item.Podcast.MaxNewEpisodesToDownload))
	if len(found) > 0 {
		if err := h.enqueue(item, found, true); err != nil {
			logging.Errorf("[podcasts] scheduled episode enqueue failed for %s: %v", item.MediaID, err)
		}
	}
	now := dbtime.Now()
	if _, err := h.db.Exec(`UPDATE podcasts SET lastEpisodeCheck=?,updatedAt=? WHERE id=?`, now, now, item.MediaID); err != nil {
		logging.Errorf("[podcasts] episode check bookkeeping update failed for %s: %v", item.MediaID, err)
	}
}

func filterNew(all []map[string]any, item libitem.Item, after int64, limit int) []map[string]any {
	known := knownEpisodes(item)
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
	// Setting Accept-Encoding by hand turns off net/http's transparent gzip
	// handling, so decode here like axios does (feeds served gzip-compressed
	// otherwise reach the XML parser as binary).
	body, err := decodedBody(resp)
	if err != nil {
		return nil, err
	}
	defer body.Close()
	limit := int64(32 << 20)
	raw, err := io.ReadAll(io.LimitReader(body, limit+1))
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
			if !httputil.SSRFFilterDisabled(host) && (ip.IP.IsPrivate() || ip.IP.IsLoopback() || ip.IP.IsLinkLocalUnicast() || ip.IP.IsLinkLocalMulticast() || ip.IP.IsUnspecified()) {
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
	if httputil.SSRFFilterDisabled(host) {
		return nil
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

func requestUser(r *http.Request) store.User {
	user, _ := store.UserFromContext(r.Context())
	return user
}

// SetHTTPClientForTests allows a local fixture server to exercise feed parsing
// without weakening the production private-address checks.
func (h *Handler) SetHTTPClientForTests(c *http.Client) { h.client = c; h.allowPrivateNetwork = true }

// decodedBody unwraps a gzip or deflate Content-Encoding.
func decodedBody(resp *http.Response) (io.ReadCloser, error) {
	switch strings.ToLower(strings.TrimSpace(resp.Header.Get("Content-Encoding"))) {
	case "gzip", "x-gzip":
		return gzip.NewReader(resp.Body)
	case "deflate":
		// RFC 9110 deflate is zlib-wrapped; some servers send raw deflate.
		br := bufio.NewReader(resp.Body)
		if head, err := br.Peek(2); err == nil && head[0]&0x0f == 8 && (uint16(head[0])<<8|uint16(head[1]))%31 == 0 {
			return zlib.NewReader(br)
		}
		return flate.NewReader(br), nil
	}
	return io.NopCloser(resp.Body), nil
}
