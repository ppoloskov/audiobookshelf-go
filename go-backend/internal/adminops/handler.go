// Package adminops implements administrative misc APIs for the pinned schema.
package adminops

import (
	"audiobookshelf-compat/internal/dbtime"
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"audiobookshelf-compat/internal/auth"
	"audiobookshelf-compat/internal/config"
	"audiobookshelf-compat/internal/store"
	"github.com/robfig/cron/v3"
)

type Handler struct {
	db              *sql.DB
	cfg             config.Config
	emit            func(userID, event string, payload any)
	settingsChanged func(context.Context, map[string]any) error
	itemChanged     func(context.Context, string) (any, error)
	watcherUpdate   func(context.Context, string, string, string, string) error
	loggerDataFn    func() any
}

func New(db *sql.DB, cfg config.Config, emit func(userID, event string, payload any)) *Handler {
	return &Handler{db: db, cfg: cfg, emit: emit}
}

func (h *Handler) SetSettingsChanged(fn func(context.Context, map[string]any) error) {
	h.settingsChanged = fn
}
func (h *Handler) SetItemChanged(fn func(context.Context, string) (any, error)) { h.itemChanged = fn }
func (h *Handler) SetWatcherUpdate(fn func(context.Context, string, string, string, string) error) {
	h.watcherUpdate = fn
}
func (h *Handler) SetLoggerData(fn func() any) { h.loggerDataFn = fn }

func (h *Handler) Handle(w http.ResponseWriter, r *http.Request, user store.User) bool {
	p := r.URL.Path
	switch {
	case r.Method == http.MethodPatch && p == "/api/settings":
		h.patchSettings(w, r, user)
	case r.Method == http.MethodPatch && p == "/api/sorting-prefixes":
		h.sortingPrefixes(w, r, user)
	case r.Method == http.MethodGet && p == "/api/auth-settings":
		h.authSettings(w, r, user, false)
	case r.Method == http.MethodPatch && p == "/api/auth-settings":
		h.authSettings(w, r, user, true)
	case r.Method == http.MethodPost && p == "/api/validate-cron":
		h.validateCron(w, r, user)
	case r.Method == http.MethodGet && p == "/api/logger-data":
		h.loggerData(w, r, user)
	case r.Method == http.MethodPost && (p == "/api/cache/purge" || p == "/api/cache/items/purge"):
		h.purgeCache(w, r, user, p == "/api/cache/items/purge")
	case r.Method == http.MethodGet && p == "/api/stats/server":
		h.serverStats(w, r, user)
	case r.Method == http.MethodPost && p == "/api/watcher/update":
		h.updateWatchedPath(w, r, user)
	case r.Method == http.MethodGet && p == "/api/tags":
		h.listTaxonomy(w, r, user, "tags")
	case r.Method == http.MethodGet && p == "/api/genres":
		h.listTaxonomy(w, r, user, "genres")
	case r.Method == http.MethodPost && p == "/api/tags/rename":
		h.renameTaxonomy(w, r, user, "tags", "tag", "newTag")
	case r.Method == http.MethodPost && p == "/api/genres/rename":
		h.renameTaxonomy(w, r, user, "genres", "genre", "newGenre")
	case r.Method == http.MethodDelete && strings.HasPrefix(p, "/api/tags/"):
		h.deleteTaxonomy(w, r, user, "tags")
	case r.Method == http.MethodDelete && strings.HasPrefix(p, "/api/genres/"):
		h.deleteTaxonomy(w, r, user, "genres")
	case r.Method == http.MethodGet && strings.HasPrefix(p, "/api/stats/year/"):
		h.statsForYear(w, r, user)
	case r.Method == http.MethodGet && p == "/api/filesystem":
		h.filesystem(w, r, user)
	case r.Method == http.MethodPost && p == "/api/filesystem/pathexists":
		h.pathExists(w, r, user)
	case r.Method == http.MethodPost && p == "/api/upload":
		h.upload(w, r, user)
	default:
		return false
	}
	return true
}

func admin(user store.User) bool { return user.Type == "root" || user.Type == "admin" }
func (h *Handler) settings(r *http.Request) (map[string]any, error) {
	var raw []byte
	if err := h.db.QueryRowContext(r.Context(), `SELECT value FROM settings WHERE key='server-settings'`).Scan(&raw); err != nil {
		return nil, err
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, err
	}
	return m, nil
}
func (h *Handler) saveSettings(r *http.Request, m map[string]any) error {
	b, e := json.Marshal(m)
	if e != nil {
		return e
	}
	res, e := h.db.ExecContext(r.Context(), `UPDATE settings SET value=?,updatedAt=? WHERE key='server-settings'`, string(b), dbtime.Now())
	if e != nil {
		return e
	}
	n, _ := res.RowsAffected()
	if n != 1 {
		return errors.New("server-settings row missing")
	}
	if h.settingsChanged != nil {
		return h.settingsChanged(r.Context(), m)
	}
	return nil
}
func (h *Handler) browserSettings(m map[string]any) map[string]any {
	out := map[string]any{}
	for k, v := range m {
		out[k] = v
	}
	for _, k := range []string{"tokenSecret", "authOpenIDClientID", "authOpenIDClientSecret", "authOpenIDMobileRedirectURIs", "authOpenIDGroupClaim", "authOpenIDAdvancedPermsClaim"} {
		delete(out, k)
	}
	out["timeZone"] = "UTC"
	return out
}
func (h *Handler) patchSettings(w http.ResponseWriter, r *http.Request, u store.User) {
	if !admin(u) {
		w.WriteHeader(403)
		return
	}
	var patch map[string]any
	if json.NewDecoder(r.Body).Decode(&patch) != nil || patch == nil {
		http.Error(w, "Invalid settings update object", 400)
		return
	}
	allowed := map[string]bool{}
	for _, k := range []string{"scannerParseSubtitle", "scannerFindCovers", "scannerCoverProvider", "scannerPreferMatchedMetadata", "scannerDisableWatcher", "storeCoverWithItem", "storeMetadataWithItem", "allowIframe", "allowedOrigins", "backupSchedule", "backupsToKeep", "maxBackupSize", "logLevel", "homeBookshelfView", "bookshelfView", "dateFormat", "timeFormat", "language", "chromecastEnabled", "sortingIgnorePrefix"} {
		allowed[k] = true
	}
	if patch["allowIframe"] == false && os.Getenv("ALLOW_IFRAME") == "1" {
		http.Error(w, "Cannot disable iframe when ALLOW_IFRAME is enabled in environment", 400)
		return
	}
	if v, ok := patch["allowedOrigins"]; ok {
		if _, ok := v.([]any); !ok {
			http.Error(w, "allowedOrigins must be an array", 400)
			return
		}
	}
	m, e := h.settings(r)
	if e != nil {
		http.Error(w, "database error", 500)
		return
	}
	changed := false
	for k, v := range patch {
		if allowed[k] && !equalJSON(m[k], v) {
			m[k] = v
			changed = true
		}
	}
	if changed {
		if e = h.saveSettings(r, m); e != nil {
			http.Error(w, "database error", 500)
			return
		}
	}
	writeJSON(w, map[string]any{"serverSettings": h.browserSettings(m)})
}
func (h *Handler) authSettings(w http.ResponseWriter, r *http.Request, u store.User, patch bool) {
	if !admin(u) {
		w.WriteHeader(403)
		return
	}
	m, e := h.settings(r)
	if e != nil {
		http.Error(w, "database error", 500)
		return
	}
	keys := []string{"authLoginCustomMessage", "authActiveAuthMethods", "authOpenIDIssuerURL", "authOpenIDAuthorizationURL", "authOpenIDTokenURL", "authOpenIDUserInfoURL", "authOpenIDJwksURL", "authOpenIDLogoutURL", "authOpenIDClientID", "authOpenIDClientSecret", "authOpenIDTokenSigningAlgorithm", "authOpenIDButtonText", "authOpenIDAutoLaunch", "authOpenIDAutoRegister", "authOpenIDMatchExistingBy", "authOpenIDMobileRedirectURIs", "authOpenIDGroupClaim", "authOpenIDAdvancedPermsClaim", "authOpenIDSubfolderForRedirectURLs"}
	if patch {
		var p map[string]any
		if json.NewDecoder(r.Body).Decode(&p) != nil || p == nil {
			http.Error(w, "Invalid auth settings update object", 400)
			return
		}
		allowed := map[string]bool{}
		for _, k := range keys {
			allowed[k] = true
		}
		updated := false
		for k, v := range p {
			if !allowed[k] {
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
				if !ok || (containsAny(values, "*") && len(values) > 1) {
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
					v = auth.SanitizeHTML(value)
				}
			}
			if !equalJSON(m[k], v) {
				m[k] = v
				updated = true
			}
		}
		if updated {
			if e = h.saveSettings(r, m); e != nil {
				http.Error(w, "database error", 500)
				return
			}
		}
		writeJSON(w, map[string]any{"updated": updated, "serverSettings": h.browserSettings(m)})
		return
	}
	out := map[string]any{}
	for _, k := range keys {
		if v, ok := m[k]; ok {
			out[k] = v
		}
	}
	out["authOpenIDSamplePermissions"] = samplePermissions
	writeJSON(w, out)
}
func (h *Handler) sortingPrefixes(w http.ResponseWriter, r *http.Request, u store.User) {
	if !admin(u) {
		w.WriteHeader(403)
		return
	}
	var p struct {
		SortingPrefixes []string `json:"sortingPrefixes"`
	}
	if json.NewDecoder(r.Body).Decode(&p) != nil || len(p.SortingPrefixes) == 0 {
		http.Error(w, "Invalid request body", 400)
		return
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
		http.Error(w, "Invalid sortingPrefixes in request body", 400)
		return
	}
	m, e := h.settings(r)
	if e != nil {
		http.Error(w, "database error", 500)
		return
	}
	m["sortingPrefixes"] = out
	if e = h.saveSettings(r, m); e != nil {
		http.Error(w, "database error", 500)
		return
	}
	rowsUpdated := 0
	for _, table := range []struct{ name, title string }{{"books", "title"}, {"podcasts", "title"}, {"series", "name"}} {
		rows, e := h.db.QueryContext(r.Context(), `SELECT id,`+table.title+`,titleIgnorePrefix FROM `+table.name)
		if table.name == "series" {
			rows, e = h.db.QueryContext(r.Context(), `SELECT id,name,nameIgnorePrefix FROM series`)
		}
		if e != nil {
			continue
		}
		type row struct{ id, title, old string }
		updates := []row{}
		for rows.Next() {
			var x row
			_ = rows.Scan(&x.id, &x.title, &x.old)
			if prefix(x.title, out) != x.old {
				updates = append(updates, x)
			}
		}
		rows.Close()
		col := "titleIgnorePrefix"
		if table.name == "series" {
			col = "nameIgnorePrefix"
		}
		for _, x := range updates {
			if _, e = h.db.ExecContext(r.Context(), `UPDATE `+table.name+` SET `+col+`=? WHERE id=?`, prefix(x.title, out), x.id); e == nil {
				rowsUpdated++
			}
		}
	}
	writeJSON(w, map[string]any{"rowsUpdated": rowsUpdated, "serverSettings": h.browserSettings(m)})
}
func prefix(title string, prefixes []string) string {
	t := strings.ToLower(title)
	for _, p := range prefixes {
		if strings.HasPrefix(t, p+" ") {
			return title[len(p)+1:]
		}
	}
	return title
}

func (h *Handler) validateCron(w http.ResponseWriter, r *http.Request, u store.User) {
	var v struct {
		Expression string `json:"expression"`
	}
	if json.NewDecoder(r.Body).Decode(&v) != nil || v.Expression == "" {
		w.WriteHeader(400)
		return
	}
	parser := cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow)
	if _, e := parser.Parse(v.Expression); e != nil {
		http.Error(w, "Invalid cron expression", 400)
		return
	}
	w.WriteHeader(200)
}
func (h *Handler) loggerData(w http.ResponseWriter, r *http.Request, u store.User) {
	if !admin(u) {
		w.WriteHeader(403)
		return
	}
	var logs any = []any{}
	if h.loggerDataFn != nil {
		logs = h.loggerDataFn()
	}
	writeJSON(w, map[string]any{"currentDailyLogs": logs})
}

func (h *Handler) purgeCache(w http.ResponseWriter, r *http.Request, u store.User, itemsOnly bool) {
	if !admin(u) {
		w.WriteHeader(403)
		return
	}
	root := filepath.Join(h.cfg.MetadataPath, "cache")
	if itemsOnly {
		root = filepath.Join(root, "items")
	}
	if err := os.RemoveAll(root); err != nil {
		http.Error(w, "Failed to purge cache", 500)
		return
	}
	if err := os.MkdirAll(root, 0755); err != nil {
		http.Error(w, "Failed to purge cache", 500)
		return
	}
	if !itemsOnly {
		for _, child := range []string{"covers", "images", "items"} {
			if err := os.MkdirAll(filepath.Join(root, child), 0755); err != nil {
				http.Error(w, "Failed to purge cache", 500)
				return
			}
		}
	}
	w.WriteHeader(200)
}

func (h *Handler) serverStats(w http.ResponseWriter, r *http.Request, u store.User) {
	if !admin(u) {
		w.WriteHeader(403)
		return
	}
	var booksSize, booksCount, podcastsSize, podcastsCount int64
	if err := h.db.QueryRowContext(r.Context(), `SELECT COALESCE(SUM(size),0),COUNT(*) FROM libraryItems WHERE mediaType='book'`).Scan(&booksSize, &booksCount); err != nil {
		http.Error(w, "Internal server error", 500)
		return
	}
	if err := h.db.QueryRowContext(r.Context(), `SELECT COALESCE(SUM(size),0),COUNT(*) FROM libraryItems WHERE mediaType='podcast'`).Scan(&podcastsSize, &podcastsCount); err != nil {
		http.Error(w, "Internal server error", 500)
		return
	}
	var bookFiles, podcastFiles int64
	if err := h.db.QueryRowContext(r.Context(), `SELECT COALESCE(SUM(json_array_length(audioFiles)),0) FROM books`).Scan(&bookFiles); err != nil {
		http.Error(w, "Internal server error", 500)
		return
	}
	if err := h.db.QueryRowContext(r.Context(), `SELECT COUNT(*) FROM podcastEpisodes`).Scan(&podcastFiles); err != nil {
		http.Error(w, "Internal server error", 500)
		return
	}
	writeJSON(w, map[string]any{"books": map[string]any{"totalSize": booksSize, "numItems": booksCount, "numAudioFiles": bookFiles}, "podcasts": map[string]any{"totalSize": podcastsSize, "numItems": podcastsCount, "numAudioFiles": podcastFiles}, "total": map[string]any{"totalSize": booksSize + podcastsSize, "numItems": booksCount + podcastsCount, "numAudioFiles": bookFiles + podcastFiles}})
}

func (h *Handler) updateWatchedPath(w http.ResponseWriter, r *http.Request, u store.User) {
	if !admin(u) {
		w.WriteHeader(403)
		return
	}
	var body struct {
		LibraryID string `json:"libraryId"`
		Path      string `json:"path"`
		Type      string `json:"type"`
		OldPath   string `json:"oldPath"`
	}
	if json.NewDecoder(r.Body).Decode(&body) != nil || body.LibraryID == "" || body.Path == "" || body.Type == "" {
		w.WriteHeader(400)
		return
	}
	if body.Type != "add" && body.Type != "unlink" && body.Type != "rename" {
		w.WriteHeader(400)
		return
	}
	if body.Type == "rename" && body.OldPath == "" {
		w.WriteHeader(400)
		return
	}
	if h.watcherUpdate == nil {
		http.Error(w, "Watcher is unavailable", 501)
		return
	}
	if err := h.watcherUpdate(r.Context(), body.LibraryID, body.Path, body.Type, body.OldPath); err != nil {
		http.Error(w, "Failed to update watched path", 500)
		return
	}
	w.WriteHeader(200)
}
func (h *Handler) listTaxonomy(w http.ResponseWriter, r *http.Request, u store.User, column string) {
	if !admin(u) {
		w.WriteHeader(403)
		return
	}
	values := []string{}
	seen := map[string]bool{}
	for _, table := range []string{"books", "podcasts"} {
		rows, e := h.db.QueryContext(r.Context(), `SELECT `+column+` FROM `+table)
		if e != nil {
			continue
		}
		for rows.Next() {
			var raw []byte
			if rows.Scan(&raw) != nil {
				continue
			}
			var arr []string
			if json.Unmarshal(raw, &arr) == nil {
				for _, v := range arr {
					if !seen[v] {
						seen[v] = true
						values = append(values, v)
					}
				}
			}
		}
		rows.Close()
	}
	if column == "tags" {
		sort.Slice(values, func(i, j int) bool { return strings.ToLower(values[i]) < strings.ToLower(values[j]) })
	}
	writeJSON(w, map[string]any{column: values})
}
func (h *Handler) renameTaxonomy(w http.ResponseWriter, r *http.Request, u store.User, column, oldKey, newKey string) {
	if !admin(u) {
		w.WriteHeader(403)
		return
	}
	if h.itemChanged == nil {
		http.Error(w, "item metadata writer is unavailable", http.StatusNotImplemented)
		return
	}
	var p map[string]string
	if json.NewDecoder(r.Body).Decode(&p) != nil || p[oldKey] == "" || p[newKey] == "" {
		w.WriteHeader(400)
		return
	}
	old, newv := p[oldKey], p[newKey]
	merged, changed := false, 0
	for _, table := range []string{"books", "podcasts"} {
		rows, e := h.db.QueryContext(r.Context(), `SELECT id,`+column+` FROM `+table+` WHERE `+column+` IS NOT NULL`)
		if e != nil {
			continue
		}
		type item struct {
			id   string
			tags []string
		}
		items := []item{}
		for rows.Next() {
			var id string
			var raw []byte
			if rows.Scan(&id, &raw) != nil {
				continue
			}
			var a []string
			if json.Unmarshal(raw, &a) == nil {
				has := false
				for _, v := range a {
					if v == old {
						has = true
					}
					if v == newv {
						merged = true
					}
				}
				if has {
					out := []string{}
					for _, v := range a {
						if v == old {
							continue
						}
						if !contains(out, v) {
							out = append(out, v)
						}
					}
					if !contains(out, newv) {
						out = append(out, newv)
					}
					items = append(items, item{id, out})
				}
			}
		}
		rows.Close()
		for _, it := range items {
			b, _ := json.Marshal(it.tags)
			res, e := h.db.ExecContext(r.Context(), `UPDATE `+table+` SET `+column+`=?,updatedAt=? WHERE id=?`, string(b), sqlDate(), it.id)
			if e == nil {
				n, _ := res.RowsAffected()
				if n > 0 {
					changed++
					if e := h.changedItem(r.Context(), it.id); e != nil {
						http.Error(w, "failed to update item metadata", 500)
						return
					}
				}
			}
		}
	}
	writeJSON(w, map[string]any{map[string]string{"tag": "tagMerged", "genre": "genreMerged"}[oldKey]: merged, "numItemsUpdated": changed})
}
func (h *Handler) deleteTaxonomy(w http.ResponseWriter, r *http.Request, u store.User, column string) {
	if !admin(u) {
		w.WriteHeader(403)
		return
	}
	if h.itemChanged == nil {
		http.Error(w, "item metadata writer is unavailable", http.StatusNotImplemented)
		return
	}
	escaped := r.URL.EscapedPath()
	seg := escaped[strings.LastIndex(escaped, "/")+1:]
	decoded, e := url.PathUnescape(seg)
	if e != nil {
		w.WriteHeader(400)
		return
	}
	raw, e := base64.StdEncoding.DecodeString(decoded)
	if e != nil {
		raw, e = base64.RawURLEncoding.DecodeString(decoded)
	}
	if e != nil {
		w.WriteHeader(400)
		return
	}
	target := string(raw)
	changed := 0
	for _, table := range []string{"books", "podcasts"} {
		rows, e := h.db.QueryContext(r.Context(), `SELECT id,`+column+` FROM `+table+` WHERE `+column+` IS NOT NULL`)
		if e != nil {
			continue
		}
		type item struct {
			id  string
			arr []string
		}
		items := []item{}
		for rows.Next() {
			var id string
			var b []byte
			if rows.Scan(&id, &b) != nil {
				continue
			}
			var a []string
			if json.Unmarshal(b, &a) == nil && contains(a, target) {
				out := []string{}
				for _, v := range a {
					if v != target {
						out = append(out, v)
					}
				}
				items = append(items, item{id, out})
			}
		}
		rows.Close()
		for _, it := range items {
			b, _ := json.Marshal(it.arr)
			res, e := h.db.ExecContext(r.Context(), `UPDATE `+table+` SET `+column+`=?,updatedAt=? WHERE id=?`, string(b), sqlDate(), it.id)
			if e == nil {
				n, _ := res.RowsAffected()
				if n > 0 {
					changed++
					if e := h.changedItem(r.Context(), it.id); e != nil {
						http.Error(w, "failed to update item metadata", 500)
						return
					}
				}
			}
		}
	}
	writeJSON(w, map[string]any{"numItemsUpdated": changed})
}
func contains(a []string, s string) bool {
	for _, v := range a {
		if v == s {
			return true
		}
	}
	return false
}
func sqlDate() string { return dbtime.Now() }
func (h *Handler) changedItem(ctx context.Context, id string) error {
	payload, err := h.itemChanged(ctx, id)
	if err != nil {
		return err
	}
	if h.emit != nil {
		h.emit("*", "item_updated", payload)
	}
	return nil
}

func (h *Handler) statsForYear(w http.ResponseWriter, r *http.Request, u store.User) {
	if !admin(u) {
		w.WriteHeader(403)
		return
	}
	year, err := strconv.Atoi(strings.TrimPrefix(r.URL.Path, "/api/stats/year/"))
	if err != nil || year < 2000 || year > 9999 {
		http.Error(w, "Invalid year", 400)
		return
	}
	start, end := strconv.Itoa(year)+"-01-01", strconv.Itoa(year+1)+"-01-01"
	var sessions, authors int
	var totalListen int64
	_ = h.db.QueryRowContext(r.Context(), `SELECT COUNT(*),COALESCE(SUM(timeListening),0) FROM playbackSessions WHERE createdAt>=? AND createdAt<?`, start, end).Scan(&sessions, &totalListen)
	_ = h.db.QueryRowContext(r.Context(), `SELECT COUNT(*) FROM authors WHERE createdAt>=? AND createdAt<?`, start, end).Scan(&authors)
	var bookCount int
	var sizeSum, durationSum float64
	var totalSize, totalDuration float64
	_ = h.db.QueryRowContext(r.Context(), `SELECT COUNT(*),COALESCE(SUM(li.size),0),COALESCE(SUM(b.duration),0) FROM libraryItems li JOIN books b ON b.id=li.mediaId WHERE li.mediaType='book' AND li.createdAt<?`, end).Scan(&bookCount, &totalSize, &totalDuration)
	rows, err := h.db.QueryContext(r.Context(), `SELECT b.id,b.coverPath,b.duration,li.size FROM books b JOIN libraryItems li ON li.mediaId=b.id AND li.mediaType='book' WHERE b.createdAt>=? AND b.createdAt<? ORDER BY RANDOM()`, start, end)
	covers := []string{}
	booksAdded := 0
	if err == nil {
		defer rows.Close()
		for rows.Next() {
			var id string
			var cover sql.NullString
			var duration, size sql.NullFloat64
			if rows.Scan(&id, &cover, &duration, &size) != nil {
				continue
			}
			booksAdded++
			if duration.Valid {
				durationSum += duration.Float64
			}
			if size.Valid {
				sizeSum += size.Float64
			}
			if cover.Valid && len(covers) < 25 {
				if _, e := os.Stat(cover.String); e == nil {
					covers = append(covers, id)
				}
			}
		}
	}
	var authorTimes, narratorTimes, genreTimes map[string]int64 = map[string]int64{}, map[string]int64{}, map[string]int64{}
	rows, err = h.db.QueryContext(r.Context(), `SELECT timeListening,mediaMetadata FROM playbackSessions WHERE createdAt>=? AND createdAt<?`, start, end)
	if err == nil {
		defer rows.Close()
		for rows.Next() {
			var listen sql.NullInt64
			var raw []byte
			if rows.Scan(&listen, &raw) != nil {
				continue
			}
			n := listen.Int64
			var meta map[string]any
			if json.Unmarshal(raw, &meta) != nil {
				continue
			}
			if values, ok := meta["authors"].([]any); ok {
				for _, v := range values {
					if item, ok := v.(map[string]any); ok {
						name, _ := item["name"].(string)
						if name != "" {
							authorTimes[name] += n
						}
					}
				}
			}
			if values, ok := meta["narrators"].([]any); ok {
				for _, v := range values {
					if name, ok := v.(string); ok && name != "" {
						narratorTimes[name] += n
					}
				}
			}
			if values, ok := meta["genres"].([]any); ok {
				for _, v := range values {
					if name, ok := v.(string); ok && name != "" && !strings.Contains(strings.ToLower(name), "audiobook") && !strings.Contains(strings.ToLower(name), "audio book") {
						genreTimes[name] += n
					}
				}
			}
		}
	}
	writeJSON(w, map[string]any{"numListeningSessions": sessions, "numBooksAdded": booksAdded, "numAuthorsAdded": authors, "totalBooksAddedSize": sizeSum, "totalBooksAddedDuration": math.Round(durationSum), "booksAddedWithCovers": covers, "totalBooksSize": totalSize, "totalBooksDuration": totalDuration, "totalListeningTime": totalListen, "numBooks": bookCount, "topAuthors": topTimes(authorTimes, "name"), "topNarrators": topTimes(narratorTimes, "name"), "topGenres": topTimes(genreTimes, "genre")})
}
func topTimes(m map[string]int64, key string) []map[string]any {
	a := make([]map[string]any, 0, len(m))
	for n, t := range m {
		a = append(a, map[string]any{key: n, "time": math.Round(float64(t))})
	}
	sort.SliceStable(a, func(i, j int) bool { return a[i]["time"].(float64) > a[j]["time"].(float64) })
	if len(a) > 3 {
		a = a[:3]
	}
	return a
}

func (h *Handler) filesystem(w http.ResponseWriter, r *http.Request, u store.User) {
	if !admin(u) {
		w.WriteHeader(403)
		return
	}
	p := r.URL.Query().Get("path")
	level, _ := strconv.Atoi(r.URL.Query().Get("level"))
	if p != "" {
		if !filepath.IsAbs(p) {
			http.Error(w, `Invalid "path" query string`, 400)
			return
		}
		if _, e := os.Stat(p); e != nil {
			http.Error(w, `Invalid "path" query string`, 400)
			return
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
	writeJSON(w, map[string]any{"posix": os.PathSeparator == '/', "directories": dirs})
}
func (h *Handler) pathExists(w http.ResponseWriter, r *http.Request, u store.User) {
	if !canUpload(u) {
		w.WriteHeader(403)
		return
	}
	var p struct {
		Directory  string `json:"directory"`
		FolderPath string `json:"folderPath"`
	}
	if json.NewDecoder(r.Body).Decode(&p) != nil || p.Directory == "" || p.FolderPath == "" {
		writeJSONStatus(w, 400, map[string]any{"error": "Invalid request body"})
		return
	}
	var libraryID string
	e := h.db.QueryRowContext(r.Context(), `SELECT libraryId FROM libraryFolders WHERE path=? LIMIT 1`, p.FolderPath).Scan(&libraryID)
	if e != nil {
		w.WriteHeader(404)
		return
	}
	if !canAccessLibrary(u, libraryID) {
		w.WriteHeader(403)
		return
	}
	base, _ := filepath.Abs(filepath.Clean(p.FolderPath))
	target, _ := filepath.Abs(filepath.Join(base, p.Directory))
	rel, e := filepath.Rel(base, target)
	if e != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
		w.WriteHeader(400)
		return
	}
	if _, e = os.Stat(target); e == nil {
		writeJSON(w, map[string]any{"exists": true})
		return
	}
	cleaned := filepath.ToSlash(filepath.Clean(p.Directory))
	if strings.Contains(cleaned, "/") {
		subdir := filepath.Dir(cleaned)
		paths := []string{filepath.ToSlash(filepath.Join(p.FolderPath, subdir))}
		if strings.Contains(subdir, "/") {
			paths = append(paths, filepath.ToSlash(filepath.Join(p.FolderPath, filepath.Dir(subdir))))
		}
		marks := strings.TrimRight(strings.Repeat("?,", len(paths)), ",")
		args := make([]any, len(paths))
		for i, path := range paths {
			args[i] = path
		}
		var title sql.NullString
		if h.db.QueryRowContext(r.Context(), `SELECT title FROM libraryItems WHERE path IN (`+marks+`) LIMIT 1`, args...).Scan(&title) == nil {
			writeJSON(w, map[string]any{"exists": true, "libraryItemTitle": title.String})
			return
		}
	}
	writeJSON(w, map[string]any{"exists": false})
}
func canUpload(u store.User) bool {
	var p map[string]any
	if json.Unmarshal(u.Permissions, &p) != nil {
		return false
	}
	v, _ := p["upload"].(bool)
	return v && u.IsActive
}
func canAccessLibrary(u store.User, id string) bool {
	if admin(u) {
		return true
	}
	var p map[string]any
	if json.Unmarshal(u.Permissions, &p) != nil {
		return false
	}
	if p["accessAllLibraries"] == true {
		return true
	}
	a, _ := p["librariesAccessible"].([]any)
	for _, v := range a {
		if v == id {
			return true
		}
	}
	return false
}
func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(v)
}
func writeJSONStatus(w http.ResponseWriter, status int, v any) {
	w.WriteHeader(status)
	writeJSON(w, v)
}

func containsAny(a []any, s string) bool {
	for _, v := range a {
		if v == s {
			return true
		}
	}
	return false
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
