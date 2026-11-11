// Package adminops implements administrative misc APIs for the pinned schema.
package adminops

import (
	"audiobookshelf-compat/internal/access"
	"audiobookshelf-compat/internal/cronspec"
	"audiobookshelf-compat/internal/dbtime"
	"audiobookshelf-compat/internal/events"
	"audiobookshelf-compat/internal/httputil"
	"audiobookshelf-compat/internal/serversettings"
	"audiobookshelf-compat/internal/textsort"
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"

	"audiobookshelf-compat/internal/config"
	"audiobookshelf-compat/internal/logging"
	"audiobookshelf-compat/internal/store"
	"audiobookshelf-compat/internal/textclean"
)

type Handler struct {
	db   *sql.DB
	cfg  config.Config
	emit events.Emitter
	deps Deps
}

// Deps are the collaborators of the routes that act outside this package.
type Deps struct {
	// AfterWrite runs once a write route (any method but GET) has answered.
	// Tag, genre and author edits patch the catalog's filter data cache in
	// place, so the composition root drops it here. Optional.
	AfterWrite func()
	// SettingsChanged applies saved server settings to the running server
	// (backup schedule, watcher, cron jobs). Optional.
	SettingsChanged func(context.Context, map[string]any) error
	// ItemChanged ends an item mutation: the metadata file and the
	// item_updated event of one library item.
	ItemChanged func(context.Context, string) error
	// WatcherUpdate applies one watcher notification (type add, unlink or
	// rename) for a path in a library.
	WatcherUpdate func(libraryID, path, kind string)
}

func New(db *sql.DB, cfg config.Config, emit events.Emitter, deps Deps) *Handler {
	return &Handler{db: db, cfg: cfg, emit: emit, deps: deps}
}

// Routes registers the admin routes on mux. The server attaches the user to
// the request context before dispatch (store.ContextWithUser). Every route but
// validate-cron, pathexists and upload answers 403 to a non-admin.
func (h *Handler) Routes(mux *http.ServeMux) {
	afterWrite := func(fn httputil.Handler) httputil.Handler {
		return func(w http.ResponseWriter, r *http.Request) error {
			err := fn(w, r)
			if h.deps.AfterWrite != nil {
				h.deps.AfterWrite()
			}
			return err
		}
	}
	admin := func(fn httputil.Handler) httputil.Handler { return access.RequireAdmin(http.StatusForbidden, fn) }
	adminWrite := func(fn httputil.Handler) httputil.Handler { return afterWrite(admin(fn)) }
	mux.Handle("PATCH /api/settings", adminWrite(h.patchSettings))
	mux.Handle("PATCH /api/sorting-prefixes", adminWrite(h.sortingPrefixes))
	mux.Handle("GET /api/auth-settings", admin(func(w http.ResponseWriter, r *http.Request) error {
		return h.authSettings(w, r, false)
	}))
	mux.Handle("PATCH /api/auth-settings", adminWrite(func(w http.ResponseWriter, r *http.Request) error { return h.authSettings(w, r, true) }))
	mux.Handle("POST /api/validate-cron", httputil.Handler(h.validateCron))
	mux.Handle("GET /api/logger-data", admin(h.loggerData))
	mux.Handle("POST /api/cache/purge", adminWrite(func(w http.ResponseWriter, r *http.Request) error { return h.purgeCache(w, r, false) }))
	mux.Handle("POST /api/cache/items/purge", adminWrite(func(w http.ResponseWriter, r *http.Request) error { return h.purgeCache(w, r, true) }))
	mux.Handle("GET /api/stats/server", admin(h.serverStats))
	mux.Handle("POST /api/watcher/update", adminWrite(h.updateWatchedPath))
	mux.Handle("GET /api/tags", admin(func(w http.ResponseWriter, r *http.Request) error { return h.listTaxonomy(w, r, "tags") }))
	mux.Handle("GET /api/genres", admin(func(w http.ResponseWriter, r *http.Request) error { return h.listTaxonomy(w, r, "genres") }))
	mux.Handle("POST /api/tags/rename", adminWrite(func(w http.ResponseWriter, r *http.Request) error {
		return h.renameTaxonomy(w, r, "tags", "tag", "newTag")
	}))
	mux.Handle("POST /api/genres/rename", adminWrite(func(w http.ResponseWriter, r *http.Request) error {
		return h.renameTaxonomy(w, r, "genres", "genre", "newGenre")
	}))
	mux.Handle("DELETE /api/tags/{name}", adminWrite(func(w http.ResponseWriter, r *http.Request) error {
		return h.deleteTaxonomy(w, r, "tags")
	}))
	mux.Handle("DELETE /api/genres/{name}", adminWrite(func(w http.ResponseWriter, r *http.Request) error {
		return h.deleteTaxonomy(w, r, "genres")
	}))
	mux.Handle("GET /api/stats/year/{year}", admin(h.statsForYear))
	mux.Handle("GET /api/filesystem", admin(h.filesystem))
	mux.Handle("POST /api/filesystem/pathexists", userRoute(h.pathExists))
	mux.Handle("POST /api/upload", afterWrite(userRoute(h.upload)))
}

func userRoute(fn func(http.ResponseWriter, *http.Request, store.User) error) httputil.Handler {
	return func(w http.ResponseWriter, r *http.Request) error {
		user, _ := store.UserFromContext(r.Context())
		return fn(w, r, user)
	}
}

func (h *Handler) settings(r *http.Request) (map[string]any, error) {
	stored, err := serversettings.Get(r.Context(), h.db)
	return stored.Doc(), err
}
func (h *Handler) saveSettings(r *http.Request, m map[string]any) error {
	if err := serversettings.Save(r.Context(), h.db, m); err != nil {
		return err
	}
	if h.deps.SettingsChanged != nil {
		return h.deps.SettingsChanged(r.Context(), m)
	}
	return nil
}

// writeSettings answers body plus the server settings as the browser sees them.
func (h *Handler) writeSettings(w http.ResponseWriter, r *http.Request, body map[string]any) error {
	stored, err := serversettings.Get(r.Context(), h.db)
	if err != nil {
		return httputil.Internal("database error", err)
	}
	body["serverSettings"] = stored.ForBrowser(h.cfg.Dirs().Backups)
	return httputil.JSON(w, r, body)
}
func (h *Handler) patchSettings(w http.ResponseWriter, r *http.Request) error {
	var patch map[string]any
	if json.NewDecoder(r.Body).Decode(&patch) != nil || patch == nil {
		return httputil.Status(400, "Invalid settings update object")
	}
	allowed := map[string]bool{}
	for _, k := range []string{"scannerParseSubtitle", "scannerFindCovers", "scannerCoverProvider", "scannerPreferMatchedMetadata", "scannerDisableWatcher", "storeCoverWithItem", "storeMetadataWithItem", "allowIframe", "allowedOrigins", "backupSchedule", "backupsToKeep", "maxBackupSize", "logLevel", "homeBookshelfView", "bookshelfView", "dateFormat", "timeFormat", "language", "chromecastEnabled", "sortingIgnorePrefix"} {
		allowed[k] = true
	}
	if patch["allowIframe"] == false && serversettings.EnvAllowIframe() {
		return httputil.Status(400, "Cannot disable iframe when ALLOW_IFRAME is enabled in environment")
	}
	if v, ok := patch["allowedOrigins"]; ok {
		if _, ok := v.([]any); !ok {
			return httputil.Status(400, "allowedOrigins must be an array")
		}
	}
	m, e := h.settings(r)
	if e != nil {
		return httputil.Internal("database error", e)
	}
	changed := false
	for k, v := range patch {
		if allowed[k] && !equalJSON(m[k], v) {
			if level, isNumber := v.(float64); k == "logLevel" && isNumber {
				// ServerSettings.update (ServerSettings.js:371)
				logging.Default().SetLevel(logging.Level(int(level)))
			}
			m[k] = v
			changed = true
		}
	}
	if changed {
		if e = h.saveSettings(r, m); e != nil {
			return httputil.Internal("database error", e)
		}
	}
	return h.writeSettings(w, r, map[string]any{})
}
func (h *Handler) authSettings(w http.ResponseWriter, r *http.Request, patch bool) error {
	m, e := h.settings(r)
	if e != nil {
		return httputil.Internal("database error", e)
	}
	if patch {
		var p map[string]any
		if json.NewDecoder(r.Body).Decode(&p) != nil || p == nil {
			return httputil.Status(400, "Invalid auth settings update object")
		}
		updated := false
		for k, v := range p {
			if !slices.Contains(serversettings.AuthKeys, k) {
				continue
			}
			if k == "authActiveAuthMethods" {
				values, ok := v.([]any)
				if !ok {
					continue
				}
				filtered := []string{}
				seen := map[string]bool{}
				for _, value := range values {
					method, ok := value.(string)
					if ok && (method == "local" || method == "openid") && !seen[method] {
						filtered = append(filtered, method)
						seen[method] = true
					}
				}
				if len(filtered) == 0 {
					continue
				}
				sort.Strings(filtered)
				v = filtered
			} else if k == "authOpenIDMobileRedirectURIs" {
				values, ok := v.([]any)
				if !ok || (slices.Contains(values, any("*")) && len(values) > 1) {
					continue
				}
				valid := true
				for _, value := range values {
					uri, ok := value.(string)
					if !ok || (uri != "*" && !validMobileURI(uri)) {
						valid = false
					}
				}
				if !valid {
					continue
				}
			} else if k == "authOpenIDAutoLaunch" || k == "authOpenIDAutoRegister" {
				if _, ok := v.(bool); !ok {
					continue
				}
			} else if v != nil {
				if _, ok := v.(string); !ok {
					continue
				}
			}
			if value, ok := v.(string); ok && value == "" && k != "authOpenIDSubfolderForRedirectURLs" {
				v = nil
			}
			if k == "authLoginCustomMessage" {
				if value, ok := v.(string); ok && value != "" {
					v = textclean.Sanitize(value)
				}
			}
			if !equalJSON(m[k], v) {
				m[k] = v
				updated = true
			}
		}
		if updated {
			if e = h.saveSettings(r, m); e != nil {
				return httputil.Internal("database error", e)
			}
		}
		return h.writeSettings(w, r, map[string]any{"updated": updated})
	}
	out := map[string]any{}
	for _, k := range serversettings.AuthKeys {
		if v, ok := m[k]; ok {
			out[k] = v
		}
	}
	out["authOpenIDSamplePermissions"] = samplePermissions
	return httputil.JSON(w, r, out)
}
func (h *Handler) sortingPrefixes(w http.ResponseWriter, r *http.Request) error {
	var p struct {
		SortingPrefixes []string `json:"sortingPrefixes"`
	}
	if json.NewDecoder(r.Body).Decode(&p) != nil || len(p.SortingPrefixes) == 0 {
		return httputil.Status(400, "Invalid request body")
	}
	seen := map[string]bool{}
	out := []string{}
	for _, s := range p.SortingPrefixes {
		s = strings.ToLower(strings.TrimSpace(s))
		if s != "" && !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	if len(out) == 0 {
		return httputil.Status(400, "Invalid sortingPrefixes in request body")
	}
	m, e := h.settings(r)
	if e != nil {
		return httputil.Internal("database error", e)
	}
	m["sortingPrefixes"] = out
	if e = h.saveSettings(r, m); e != nil {
		return httputil.Internal("database error", e)
	}
	rowsUpdated := 0
	for _, table := range []struct{ name, title string }{{"books", "title"}, {"podcasts", "title"}, {"series", "name"}} {
		// One query per table: a second QueryContext while the first cursor is
		// open would wait for it forever on the one-connection pool.
		query := `SELECT id,` + table.title + `,titleIgnorePrefix FROM ` + table.name
		if table.name == "series" {
			query = `SELECT id,name,nameIgnorePrefix FROM series`
		}
		rows, e := h.db.QueryContext(r.Context(), query)
		if e != nil {
			return httputil.Internal("database error", e)
		}
		type row struct{ id, title, old string }
		updates := []row{}
		for rows.Next() {
			var x row
			var title, old sql.NullString
			if e = rows.Scan(&x.id, &title, &old); e != nil {
				break
			}
			x.title, x.old = title.String, old.String
			if textsort.TitleIgnorePrefix(x.title, out) != x.old {
				updates = append(updates, x)
			}
		}
		if e = errors.Join(e, rows.Err(), rows.Close()); e != nil {
			return httputil.Internal("database error", e)
		}
		col := "titleIgnorePrefix"
		if table.name == "series" {
			col = "nameIgnorePrefix"
		}
		for _, x := range updates {
			if _, e = h.db.ExecContext(r.Context(), `UPDATE `+table.name+` SET `+col+`=? WHERE id=?`, textsort.TitleIgnorePrefix(x.title, out), x.id); e != nil {
				return httputil.Internal("database error", e)
			}
			rowsUpdated++
		}
	}
	return h.writeSettings(w, r, map[string]any{"rowsUpdated": rowsUpdated})
}
func (h *Handler) validateCron(w http.ResponseWriter, r *http.Request) error {
	var v struct {
		Expression string `json:"expression"`
	}
	if json.NewDecoder(r.Body).Decode(&v) != nil || v.Expression == "" {
		return httputil.Status(400, "")
	}
	if e := cronspec.Validate(v.Expression); e != nil {
		return httputil.Status(400, "Invalid cron expression")
	}
	return httputil.OK(w, r)
}
func (h *Handler) loggerData(w http.ResponseWriter, r *http.Request) error {
	return httputil.JSON(w, r, struct {
		CurrentDailyLogs []logging.Entry `json:"currentDailyLogs"`
	}{logging.Default().Entries()})
}

func (h *Handler) purgeCache(w http.ResponseWriter, r *http.Request, itemsOnly bool) error {
	root := h.cfg.Dirs().Cache
	if itemsOnly {
		root = h.cfg.Dirs().ItemCache
	}
	if itemsOnly {
		logging.Infof("[CacheManager] Purging items cache at %q", root) // CacheManager.js:139
	} else {
		logging.Infof("[CacheManager] Purging all cache at %q", root) // CacheManager.js:129
	}
	if err := os.RemoveAll(root); err != nil {
		return httputil.Internal("Failed to purge cache", err)
	}
	if err := os.MkdirAll(root, 0755); err != nil {
		return httputil.Internal("Failed to purge cache", err)
	}
	if !itemsOnly {
		for _, dir := range []string{h.cfg.Dirs().CoverCache, h.cfg.Dirs().ImageCache, h.cfg.Dirs().ItemCache} {
			if err := os.MkdirAll(dir, 0755); err != nil {
				return httputil.Internal("Failed to purge cache", err)
			}
		}
	}
	return httputil.OK(w, r)
}

func (h *Handler) serverStats(w http.ResponseWriter, r *http.Request) error {
	var booksSize, booksCount, podcastsSize, podcastsCount int64
	if err := h.db.QueryRowContext(r.Context(), `SELECT COALESCE(SUM(size),0),COUNT(*) FROM libraryItems WHERE mediaType='book'`).Scan(&booksSize, &booksCount); err != nil {
		return httputil.Internal("Internal server error", err)
	}
	if err := h.db.QueryRowContext(r.Context(), `SELECT COALESCE(SUM(size),0),COUNT(*) FROM libraryItems WHERE mediaType='podcast'`).Scan(&podcastsSize, &podcastsCount); err != nil {
		return httputil.Internal("Internal server error", err)
	}
	var bookFiles, podcastFiles int64
	if err := h.db.QueryRowContext(r.Context(), `SELECT COALESCE(SUM(json_array_length(audioFiles)),0) FROM books`).Scan(&bookFiles); err != nil {
		return httputil.Internal("Internal server error", err)
	}
	if err := h.db.QueryRowContext(r.Context(), `SELECT COUNT(*) FROM podcastEpisodes`).Scan(&podcastFiles); err != nil {
		return httputil.Internal("Internal server error", err)
	}
	return httputil.JSON(w, r, map[string]any{"books": map[string]any{"totalSize": booksSize, "numItems": booksCount, "numAudioFiles": bookFiles}, "podcasts": map[string]any{"totalSize": podcastsSize, "numItems": podcastsCount, "numAudioFiles": podcastFiles}, "total": map[string]any{"totalSize": booksSize + podcastsSize, "numItems": booksCount + podcastsCount, "numAudioFiles": bookFiles + podcastFiles}})
}

func (h *Handler) updateWatchedPath(w http.ResponseWriter, r *http.Request) error {
	var body struct {
		LibraryID string `json:"libraryId"`
		Path      string `json:"path"`
		Type      string `json:"type"`
		OldPath   string `json:"oldPath"`
	}
	if json.NewDecoder(r.Body).Decode(&body) != nil || body.LibraryID == "" || body.Path == "" || body.Type == "" {
		return httputil.Status(400, "")
	}
	if body.Type != "add" && body.Type != "unlink" && body.Type != "rename" {
		return httputil.Status(400, "")
	}
	if body.Type == "rename" && body.OldPath == "" {
		return httputil.Status(400, "")
	}
	h.deps.WatcherUpdate(body.LibraryID, body.Path, body.Type)
	return httputil.OK(w, r)
}
func (h *Handler) listTaxonomy(w http.ResponseWriter, r *http.Request, column string) error {
	values := []string{}
	seen := map[string]bool{}
	for _, table := range []string{"books", "podcasts"} {
		rows, e := h.db.QueryContext(r.Context(), `SELECT `+column+` FROM `+table)
		if e != nil {
			return httputil.Internal("database error", e)
		}
		for rows.Next() {
			var raw []byte
			if e = rows.Scan(&raw); e != nil {
				break
			}
			var arr []string
			if json.Unmarshal(raw, &arr) == nil { // a malformed stored list contributes nothing
				for _, v := range arr {
					if !seen[v] {
						seen[v] = true
						values = append(values, v)
					}
				}
			}
		}
		if e = errors.Join(e, rows.Err(), rows.Close()); e != nil {
			return httputil.Internal("database error", e)
		}
	}
	if column == "tags" {
		sort.Slice(values, func(i, j int) bool { return strings.ToLower(values[i]) < strings.ToLower(values[j]) })
	}
	return httputil.JSON(w, r, map[string]any{column: values})
}
func (h *Handler) renameTaxonomy(w http.ResponseWriter, r *http.Request, column, oldKey, newKey string) error {
	var p map[string]string
	if json.NewDecoder(r.Body).Decode(&p) != nil || p[oldKey] == "" || p[newKey] == "" {
		return httputil.Status(400, "")
	}
	old, newv := p[oldKey], p[newKey]
	merged := false
	changed, err := h.rewriteTaxonomy(r.Context(), column, func(list []string) ([]string, bool) {
		merged = merged || slices.Contains(list, newv)
		if !slices.Contains(list, old) {
			return nil, false
		}
		out := []string{}
		for _, v := range list {
			if v != old && !slices.Contains(out, v) {
				out = append(out, v)
			}
		}
		if !slices.Contains(out, newv) {
			out = append(out, newv)
		}
		return out, true
	})
	if err != nil {
		return httputil.Internal("failed to update item metadata", err)
	}
	return httputil.JSON(w, r, map[string]any{map[string]string{"tag": "tagMerged", "genre": "genreMerged"}[oldKey]: merged, "numItemsUpdated": changed})
}
func (h *Handler) deleteTaxonomy(w http.ResponseWriter, r *http.Request, column string) error {
	// The client sends the name base64-encoded and URL-escaped; PathValue
	// returns it unescaped.
	seg := r.PathValue("name")
	raw, e := base64.StdEncoding.DecodeString(seg)
	if e != nil {
		raw, e = base64.RawURLEncoding.DecodeString(seg)
	}
	if e != nil {
		return httputil.Status(400, "")
	}
	target := string(raw)
	changed, err := h.rewriteTaxonomy(r.Context(), column, func(list []string) ([]string, bool) {
		if !slices.Contains(list, target) {
			return nil, false
		}
		return slices.DeleteFunc(slices.Clone(list), func(v string) bool { return v == target }), true
	})
	if err != nil {
		return httputil.Internal("failed to update item metadata", err)
	}
	return httputil.JSON(w, r, map[string]any{"numItemsUpdated": changed})
}

func (h *Handler) rewriteTaxonomy(ctx context.Context, column string, edit func([]string) ([]string, bool)) (int, error) {
	changed := 0
	for _, table := range []string{"books", "podcasts"} {
		rows, err := h.db.QueryContext(ctx, `SELECT m.id,li.id,m.`+column+` FROM `+table+` m JOIN libraryItems li ON li.mediaId=m.id WHERE m.`+column+` IS NOT NULL`)
		if err != nil {
			return changed, err
		}
		type update struct{ mediaID, itemID, list string }
		var updates []update
		for rows.Next() {
			var u update
			var raw []byte
			if err = rows.Scan(&u.mediaID, &u.itemID, &raw); err != nil {
				break
			}
			var list []string
			if json.Unmarshal(raw, &list) != nil { // a malformed stored list is left alone
				continue
			}
			if edited, ok := edit(list); ok {
				encoded, _ := json.Marshal(edited) // a []string always encodes
				u.list = string(encoded)
				updates = append(updates, u)
			}
		}
		if err = errors.Join(err, rows.Err(), rows.Close()); err != nil {
			return changed, err
		}
		for _, u := range updates {
			res, err := h.db.ExecContext(ctx, `UPDATE `+table+` SET `+column+`=?,updatedAt=? WHERE id=?`, u.list, dbtime.Now(), u.mediaID)
			if err != nil {
				return changed, err
			}
			if n, err := res.RowsAffected(); err != nil {
				return changed, err
			} else if n > 0 {
				changed++
				if err := h.deps.ItemChanged(ctx, u.itemID); err != nil {
					return changed, err
				}
			}
		}
	}
	return changed, nil
}

func (h *Handler) statsForYear(w http.ResponseWriter, r *http.Request) error {
	year, err := strconv.Atoi(r.PathValue("year"))
	if err != nil || year < 2000 || year > 9999 {
		return httputil.Status(400, "Invalid year")
	}
	start, end := strconv.Itoa(year)+"-01-01", strconv.Itoa(year+1)+"-01-01"
	var sessions, authors int
	var totalListen float64
	if err := h.db.QueryRowContext(r.Context(), `SELECT COUNT(*),COALESCE(SUM(timeListening),0) FROM playbackSessions WHERE createdAt>=? AND createdAt<?`, start, end).Scan(&sessions, &totalListen); err != nil {
		return httputil.Internal("database error", err)
	}
	if err := h.db.QueryRowContext(r.Context(), `SELECT COUNT(*) FROM authors WHERE createdAt>=? AND createdAt<?`, start, end).Scan(&authors); err != nil {
		return httputil.Internal("database error", err)
	}
	var bookCount int
	var sizeSum, durationSum float64
	var totalSize, totalDuration float64
	if err := h.db.QueryRowContext(r.Context(), `SELECT COUNT(*),COALESCE(SUM(li.size),0),COALESCE(SUM(b.duration),0) FROM libraryItems li JOIN books b ON b.id=li.mediaId WHERE li.mediaType='book' AND li.createdAt<?`, end).Scan(&bookCount, &totalSize, &totalDuration); err != nil {
		return httputil.Internal("database error", err)
	}
	rows, err := h.db.QueryContext(r.Context(), `SELECT li.id,b.coverPath,b.duration,li.size FROM books b JOIN libraryItems li ON li.mediaId=b.id AND li.mediaType='book' WHERE b.createdAt>=? AND b.createdAt<? ORDER BY RANDOM()`, start, end)
	if err != nil {
		return httputil.Internal("database error", err)
	}
	defer rows.Close()
	covers := []string{}
	booksAdded := 0
	for rows.Next() {
		var id string
		var cover sql.NullString
		var duration, size sql.NullFloat64
		if err := rows.Scan(&id, &cover, &duration, &size); err != nil {
			return httputil.Internal("database error", err)
		}
		booksAdded++
		if duration.Valid {
			durationSum += duration.Float64
		}
		if size.Valid {
			sizeSum += size.Float64
		}
		// adminStats.getStatsForYear pushes book.libraryItem.id, the id the
		// web client loads covers by (server/utils/queries/adminStats.js:76-80).
		if cover.Valid && len(covers) < 25 {
			if _, e := os.Stat(cover.String); e == nil {
				covers = append(covers, id)
			}
		}
	}
	if err := rows.Err(); err != nil {
		return httputil.Internal("database error", err)
	}
	authorTimes, narratorTimes, genreTimes := &orderedTimes{}, &orderedTimes{}, &orderedTimes{}
	rows, err = h.db.QueryContext(r.Context(), `SELECT timeListening,mediaMetadata FROM playbackSessions WHERE createdAt>=? AND createdAt<? ORDER BY rowid`, start, end)
	if err != nil {
		return httputil.Internal("database error", err)
	}
	defer rows.Close()
	for rows.Next() {
		// REAL and NULL timeListening both occur; adminStats.js sums
		// `ls.timeListening || 0` as floats.
		var listen sql.NullFloat64
		var raw []byte
		if err := rows.Scan(&listen, &raw); err != nil {
			return httputil.Internal("database error", err)
		}
		n := listen.Float64
		var meta map[string]any
		if json.Unmarshal(raw, &meta) != nil { // a session without readable metadata counts for no author
			continue
		}
		if values, ok := meta["authors"].([]any); ok {
			for _, v := range values {
				if item, ok := v.(map[string]any); ok {
					name, _ := item["name"].(string)
					if name != "" {
						authorTimes.add(name, n)
					}
				}
			}
		}
		if values, ok := meta["narrators"].([]any); ok {
			for _, v := range values {
				if name, ok := v.(string); ok && name != "" {
					narratorTimes.add(name, n)
				}
			}
		}
		if values, ok := meta["genres"].([]any); ok {
			for _, v := range values {
				if name, ok := v.(string); ok && name != "" && !strings.Contains(strings.ToLower(name), "audiobook") && !strings.Contains(strings.ToLower(name), "audio book") {
					genreTimes.add(name, n)
				}
			}
		}
	}
	if err := rows.Err(); err != nil {
		return httputil.Internal("database error", err)
	}
	return httputil.JSON(w, r, map[string]any{"numListeningSessions": sessions, "numBooksAdded": booksAdded, "numAuthorsAdded": authors, "totalBooksAddedSize": sizeSum, "totalBooksAddedDuration": math.Round(durationSum), "booksAddedWithCovers": covers, "totalBooksSize": totalSize, "totalBooksDuration": totalDuration, "totalListeningTime": totalListen, "numBooks": bookCount, "topAuthors": topTimes(authorTimes, "name"), "topNarrators": topTimes(narratorTimes, "name"), "topGenres": topTimes(genreTimes, "genre")})
}

// orderedTimes is a JS object used as a map: keys keep first-insertion
// order, which Object.keys returns and a stable sort preserves for ties.
type orderedTimes struct {
	order []string
	m     map[string]float64
}

func (o *orderedTimes) add(k string, v float64) {
	if o.m == nil {
		o.m = map[string]float64{}
	}
	if _, ok := o.m[k]; !ok {
		o.order = append(o.order, k)
	}
	o.m[k] += v
}

// topTimes is adminStats.getStatsForYear's top list
// (server/utils/queries/adminStats.js:120-146): Object.keys in insertion
// order, time Math.round-ed, stable sort by time descending, first 3.
func topTimes(o *orderedTimes, key string) []map[string]any {
	a := make([]map[string]any, 0, len(o.order))
	for _, n := range o.order {
		a = append(a, map[string]any{key: n, "time": math.Round(o.m[n])})
	}
	sort.SliceStable(a, func(i, j int) bool { return a[i]["time"].(float64) > a[j]["time"].(float64) })
	if len(a) > 3 {
		a = a[:3]
	}
	return a
}

func (h *Handler) filesystem(w http.ResponseWriter, r *http.Request) error {
	p := r.URL.Query().Get("path")
	level, _ := strconv.Atoi(r.URL.Query().Get("level"))
	if p != "" {
		if !filepath.IsAbs(p) {
			return httputil.Status(400, `Invalid "path" query string`)
		}
		if _, e := os.Stat(p); e != nil {
			return httputil.Status(400, `Invalid "path" query string`)
		}
	} else {
		p = "/"
	}
	dirs := []any{}
	if level < 0 {
		level = 0
	}
	if entries, e := os.ReadDir(p); e == nil {
		for _, entry := range entries {
			if !entry.IsDir() {
				continue
			}
			full := filepath.Join(p, entry.Name())
			dirs = append(dirs, map[string]any{"path": filepath.ToSlash(full), "dirname": entry.Name(), "level": level})
		}
	}
	return httputil.JSON(w, r, map[string]any{"posix": os.PathSeparator == '/', "directories": dirs})
}
func (h *Handler) pathExists(w http.ResponseWriter, r *http.Request, u store.User) error {
	if !access.For(r.Context(), u).Upload() {
		return httputil.Status(403, "")
	}
	var p struct {
		Directory  string `json:"directory"`
		FolderPath string `json:"folderPath"`
	}
	if json.NewDecoder(r.Body).Decode(&p) != nil || p.Directory == "" || p.FolderPath == "" {
		// Upstream answers res.status(400).json({error}), FileSystemController.js:94-97.
		return httputil.JSONStatus(w, r, http.StatusBadRequest, map[string]string{"error": "Invalid request body"})
	}
	var libraryID string
	e := h.db.QueryRowContext(r.Context(), `SELECT libraryId FROM libraryFolders WHERE path=? LIMIT 1`, p.FolderPath).Scan(&libraryID)
	if e != nil {
		return httputil.Status(404, "")
	}
	if !access.AdminUser(u) && !access.For(r.Context(), u).Library(libraryID) {
		return httputil.Status(403, "")
	}
	base, _ := filepath.Abs(filepath.Clean(p.FolderPath))
	target, _ := filepath.Abs(filepath.Join(base, p.Directory))
	rel, e := filepath.Rel(base, target)
	if e != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
		return httputil.Status(400, "")
	}
	if _, e = os.Stat(target); e == nil {
		return httputil.JSON(w, r, map[string]any{"exists": true})
	}
	cleaned := filepath.ToSlash(filepath.Clean(p.Directory))
	if strings.Contains(cleaned, "/") {
		subdir := filepath.Dir(cleaned)
		paths := []string{filepath.ToSlash(filepath.Join(p.FolderPath, subdir))}
		if strings.Contains(subdir, "/") {
			paths = append(paths, filepath.ToSlash(filepath.Join(p.FolderPath, filepath.Dir(subdir))))
		}
		marks := store.Placeholders(len(paths))
		args := make([]any, len(paths))
		for i, path := range paths {
			args[i] = path
		}
		var title sql.NullString
		if h.db.QueryRowContext(r.Context(), `SELECT title FROM libraryItems WHERE path IN (`+marks+`) LIMIT 1`, args...).Scan(&title) == nil {
			return httputil.JSON(w, r, map[string]any{"exists": true, "libraryItemTitle": title.String})
		}
	}
	return httputil.JSON(w, r, map[string]any{"exists": false})
}
func validMobileURI(v string) bool {
	return regexp.MustCompile(`(?i)^\w+://[\w.-]+(/[\w./-]*)*$`).MatchString(v)
}

func equalJSON(a, b any) bool {
	x, _ := json.Marshal(a)
	y, _ := json.Marshal(b)
	return string(x) == string(y)
}

const samplePermissions = `{
  "canDownload": false,
  "canUpload": false,
  "canDelete": false,
  "canUpdate": false,
  "canAccessExplicitContent": false,
  "canAccessAllLibraries": false,
  "canAccessAllTags": false,
  "canCreateEReader": false,
  "tagsAreDenylist": false,
  "allowedLibraries": [
    "5406ba8a-16e1-451d-96d7-4931b0a0d966",
    "918fd848-7c1d-4a02-818a-847435a879ca"
  ],
  "allowedTags": [
    "ExampleTag",
    "AnotherTag",
    "ThirdTag"
  ]
}`
