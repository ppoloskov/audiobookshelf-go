package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"

	"audiobookshelf-compat/internal/adminops"
	"audiobookshelf-compat/internal/audiotools"
	"audiobookshelf-compat/internal/auth"
	"audiobookshelf-compat/internal/backup"
	"audiobookshelf-compat/internal/catalog"
	"audiobookshelf-compat/internal/config"
	"audiobookshelf-compat/internal/emailapi"
	"audiobookshelf-compat/internal/identity"
	"audiobookshelf-compat/internal/notifications"
	"audiobookshelf-compat/internal/playback"
	"audiobookshelf-compat/internal/podcasts"
	"audiobookshelf-compat/internal/providers"
	"audiobookshelf-compat/internal/realtime"
	"audiobookshelf-compat/internal/scanner"
	"audiobookshelf-compat/internal/sharing"
	"audiobookshelf-compat/internal/store"
	"audiobookshelf-compat/internal/userapi"
)

type Runtime struct {
	cfg           config.Config
	db            *sql.DB
	repo          Repository
	identity      *identity.Handler
	realtime      *realtime.Hub
	closeRealtime bool
	catalog       *catalog.Handler
	playback      *playback.Handler
	userapi       *userapi.Handler
	adminops      *adminops.Handler
	scanner       *scanner.Scanner
	audiotools    *audiotools.Handler
	backup        *backup.Handler
	emailapi      *emailapi.Handler
	notifications *notifications.Handler
	sharing       *sharing.Handler
	podcasts      *podcasts.Handler
	providers     *providers.Handler
	logMu         sync.RWMutex
	logListeners  map[string]int
}

// NewRuntime assembles the single-process implementation around an already
// opened, schema-validated Audiobookshelf database.
func NewRuntime(db *sql.DB, cfg config.Config, repo Repository) (*Runtime, error) {
	return newRuntime(db, cfg, repo, nil)
}

func newRuntime(db *sql.DB, cfg config.Config, repo Repository, manager *Manager, preservedHub ...*realtime.Hub) (*Runtime, error) {
	installLogRing()
	ident, err := identity.New(db, cfg)
	if err != nil {
		return nil, err
	}
	var hub *realtime.Hub
	closeHub := true
	if len(preservedHub) > 0 && preservedHub[0] != nil {
		hub = preservedHub[0]
		closeHub = false
		if err := hub.SetDatabase(db, ident.Authenticate); err != nil {
			return nil, err
		}
	} else {
		hub, err = realtime.New(db, cfg, ident.Authenticate)
		if err != nil {
			return nil, err
		}
	}
	ident.SetUserAddedEmitter(func(payload map[string]any) {
		hub.Emit("@admins", "user_added", payload)
	})
	sc, err := scanner.New(db, cfg, hub.Emit)
	if err != nil {
		hub.Close()
		return nil, err
	}
	prov, err := providers.New(db, hub.Emit)
	if err != nil {
		sc.Close()
		hub.Close()
		return nil, err
	}
	rt := &Runtime{
		cfg: cfg, db: db, repo: repo, identity: ident, realtime: hub, closeRealtime: closeHub,
		catalog:       catalog.New(db, cfg, hub.Emit),
		playback:      playback.New(db, cfg, hub.Emit),
		userapi:       userapi.New(db, cfg, hub.Emit),
		adminops:      adminops.New(db, cfg, hub.Emit),
		emailapi:      emailapi.New(db, cfg, hub.Emit),
		notifications: notifications.New(db, cfg, hub.Emit),
		sharing:       sharing.New(db, cfg, hub.Emit),
		podcasts:      podcasts.New(db, cfg, hub.Emit),
		providers:     prov,
		logListeners:  map[string]int{},
		scanner:       sc, audiotools: audiotools.New(db, cfg, hub.Emit),
	}
	rt.podcasts.SetQuickMatchHandler(sc.QuickMatchPodcastEpisodes)
	sc.SetPodcastFeedFetcher(rt.podcasts.FetchFeedEpisodes)
	rt.userapi.SetCredentialHooks(ident.InvalidateUserSessions, ident.ChangePassword, ident.SetRefreshTokenCookie)
	rt.userapi.SetOnlineData(hub.UsersOnline, rt.playback.OpenSessions)
	hub.SetSessionLookup(func(userID string) any {
		for _, session := range rt.playback.OpenSessions() {
			if session["userId"] == userID {
				return session
			}
		}
		return nil
	})
	rt.catalog.SetItemChanged(func(ctx context.Context, itemID string) error { return sc.WriteMetadata(ctx, itemID) })
	rt.adminops.SetWatcherUpdate(func(ctx context.Context, libraryID, path, kind, oldPath string) error {
		mapped := map[string]string{"add": "added", "unlink": "deleted", "rename": "renamed"}[kind]
		if mapped == "" {
			return fmt.Errorf("unsupported watcher update %q", kind)
		}
		return sc.UpdateWatchedPath(ctx, libraryID, path, mapped, oldPath)
	})
	rt.adminops.SetLoggerData(runtimeLogBuffer.Snapshot)
	runtimeLogBuffer.SetNotify(func(entry map[string]any) {
		rt.logMu.RLock()
		listeners := make(map[string]int, len(rt.logListeners))
		for socketID, level := range rt.logListeners {
			listeners[socketID] = level
		}
		rt.logMu.RUnlock()
		entryLevel, _ := entry["level"].(int)
		for socketID, level := range listeners {
			if entryLevel <= level {
				hub.EmitSocket(socketID, "log", entry)
			}
		}
	})
	hub.SetDispatcher(func(ctx context.Context, action realtime.Action) error {
		switch action.Event {
		case "cancel_scan":
			libraryID, ok := action.Payload.(string)
			if !ok || libraryID == "" {
				return fmt.Errorf("invalid cancel_scan payload")
			}
			sc.Cancel(libraryID)
		case "set_log_listener":
			level, ok := logLevel(action.Payload)
			if !ok {
				return fmt.Errorf("invalid log listener level")
			}
			rt.logMu.Lock()
			rt.logListeners[action.SocketID] = level
			rt.logMu.Unlock()
		case "remove_log_listener", "disconnect":
			rt.logMu.Lock()
			delete(rt.logListeners, action.SocketID)
			rt.logMu.Unlock()
		}
		return nil
	})
	if manager != nil {
		rt.catalog.SetLibraryChanged(func(string) { _ = sc.Refresh(context.Background()) })
		rt.adminops.SetSettingsChanged(func(ctx context.Context, settings map[string]any) error {
			if rt.backup != nil {
				backupPath := strings.TrimSpace(os.Getenv("BACKUP_PATH"))
				if backupPath == "" {
					backupPath, _ = settings["backupPath"].(string)
				}
				maxSize, _ := settings["maxBackupSize"].(float64)
				if maxSize <= 0 {
					maxSize = 100
				}
				options := backup.Options{BackupPath: backupPath, BackupPathEnvSet: strings.TrimSpace(os.Getenv("BACKUP_PATH")) != "", BackupSchedule: stringSetting(settings, "backupSchedule"), BackupsToKeep: intSetting(settings, "backupsToKeep", 2), MaxBackupSizeGB: maxSize, Restore: manager.restore, UpdatePath: manager.updateBackupPath}
				if err := rt.backup.UpdateOptions(ctx, options); err != nil {
					return err
				}
			}
			return sc.Refresh(ctx)
		})
		rt.adminops.SetItemChanged(func(ctx context.Context, mediaID string) (any, error) {
			var itemID string
			if err := db.QueryRowContext(ctx, `SELECT id FROM libraryItems WHERE mediaId=? LIMIT 1`, mediaID).Scan(&itemID); err != nil {
				return nil, err
			}
			if err := sc.WriteMetadata(ctx, itemID); err != nil {
				return nil, err
			}
			return rt.catalog.ItemJSON(ctx, itemID, true)
		})
		if err := sc.Start(context.Background()); err != nil {
			rt.Close()
			return nil, err
		}
		settings, settingsErr := repo.ServerSettings(context.Background())
		if settingsErr != nil {
			rt.Close()
			return nil, settingsErr
		}
		backupPath := strings.TrimSpace(os.Getenv("BACKUP_PATH"))
		if backupPath == "" {
			backupPath, _ = settings["backupPath"].(string)
		}
		maxSize, _ := settings["maxBackupSize"].(float64)
		if maxSize <= 0 {
			maxSize = 100
		}
		backupSettings := backup.Options{BackupPath: backupPath, BackupPathEnvSet: strings.TrimSpace(os.Getenv("BACKUP_PATH")) != "", BackupSchedule: stringSetting(settings, "backupSchedule"), BackupsToKeep: intSetting(settings, "backupsToKeep", 2), MaxBackupSizeGB: maxSize, Restore: manager.restore, UpdatePath: manager.updateBackupPath}
		rt.backup, err = backup.New(db, cfg, backupSettings, hub.Emit)
		if err != nil {
			rt.Close()
			return nil, err
		}
		rt.backup.SetNotificationHandler(rt.notifications.TriggerEvent)
	}
	return rt, nil
}

func (rt *Runtime) Close() {
	rt.close(rt.closeRealtime)
}

// close releases runtime-owned resources and optionally the shared transport.
func (rt *Runtime) close(closeRealtime bool) {
	if rt.backup != nil {
		rt.backup.Close()
	}
	if rt.podcasts != nil {
		rt.podcasts.Close()
	}
	if rt.scanner != nil {
		rt.scanner.Close()
	}
	if rt.audiotools != nil {
		rt.audiotools.Close()
	}
	if rt.playback != nil {
		rt.playback.Close()
	}
	if closeRealtime && rt.realtime != nil {
		rt.realtime.Close()
	}
}

func (rt *Runtime) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Referrer-Policy", "no-referrer")
	// Engine.IO is mounted both at the legacy root and under the configured
	// router prefix, so dispatch it using the original URL before stripping.
	for _, socketPath := range rt.realtime.Paths() {
		if r.URL.Path == socketPath || strings.HasPrefix(r.URL.Path, socketPath+"/") {
			rt.realtime.ServeHTTP(w, r)
			return
		}
	}
	path := rt.stripBase(r.URL.Path)
	if path == "" {
		path = "/"
	}
	if !rt.settingsAllowIframe() {
		w.Header().Set("Content-Security-Policy", "frame-ancestors 'self'")
	}
	if (strings.HasPrefix(path, "/feed/") || strings.HasPrefix(path, "/public/share/")) && rt.sharing.Handle(w, withPath(r, path), store.User{}) {
		return
	}
	if isGet(r) && path == "/ping" {
		writeJSONForRequest(w, r, 200, map[string]any{"success": true})
		return
	}
	if isGet(r) && path == "/healthcheck" {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.Header().Set("Content-Length", "2")
		w.WriteHeader(200)
		if r.Method != http.MethodHead {
			_, _ = w.Write([]byte("OK"))
		}
		return
	}
	if isGet(r) && path == "/status" {
		rt.status(w, r)
		return
	}
	if rt.identity.Handle(w, withPath(r, path)) {
		return
	}
	// Capability URLs and explicitly public images bypass API bearer auth.
	if isPublicPlayback(path) {
		if rt.playback.Handle(w, withPath(r, path), store.User{}) {
			return
		}
	}
	if !strings.HasPrefix(path, "/api/") && path != "/api" {
		rt.serveClient(w, r, path)
		return
	}
	// The upstream intentionally leaves item covers and author images public.
	publicRead := isGet(r) && (isPublicCover(path) || isPublicAuthorImage(path))
	var user store.User
	if !publicRead {
		var err error
		user, err = rt.identity.Authenticate(r)
		if err != nil {
			writePlain(w, 401, "Unauthorized")
			return
		}
	}
	if rt.catalog.Handle(w, withPath(r, path), user) {
		return
	}
	if rt.sharing.Handle(w, withPath(r, path), user) {
		return
	}
	if rt.userapi.Handle(w, withPath(r, path), user) {
		return
	}
	if rt.playback.Handle(w, withPath(r, path), user) {
		return
	}
	if rt.adminops.Handle(w, withPath(r, path), user) {
		return
	}
	if rt.notifications.Handle(w, withPath(r, path), user) {
		return
	}
	if rt.emailapi.Handle(w, withPath(r, path), user) {
		return
	}
	if rt.scanner.Handle(w, withPath(r, path), user) {
		return
	}
	if rt.audiotools.Handle(w, withPath(r, path), user) {
		return
	}
	if rt.podcasts.Handle(w, withPath(r, path), user) {
		return
	}
	if rt.providers.Handle(w, withPath(r, path), user) {
		return
	}
	if path == "/api/tasks" && isGet(r) {
		payload, err := rt.audiotools.Tasks(r.Context(), strings.Contains(","+r.URL.Query().Get("include")+",", ",queue,"))
		if err != nil {
			writeJSON(w, 500, map[string]string{"error": "failed to load tasks"})
			return
		}
		writeJSON(w, 200, payload)
		return
	}
	if rt.backup != nil {
		if rt.backup.Handle(w, withPath(r, path), user) {
			return
		}
	}
	writeJSON(w, http.StatusNotImplemented, map[string]string{"error": "This route is not implemented in the experimental Go backend"})
}

func (rt *Runtime) stripBase(path string) string {
	base := rt.cfg.RouterBasePath
	if base != "" && strings.HasPrefix(path, base) {
		return strings.TrimPrefix(path, base)
	}
	return path
}

func stringSetting(m map[string]any, key string) string { value, _ := m[key].(string); return value }
func logLevel(value any) (int, bool) {
	switch v := value.(type) {
	case int:
		return v, v >= 0 && v <= 6
	case int64:
		return int(v), v >= 0 && v <= 6
	case float64:
		return int(v), v == float64(int(v)) && v >= 0 && v <= 6
	default:
		return 0, false
	}
}
func intSetting(m map[string]any, key string, fallback int) int {
	switch value := m[key].(type) {
	case float64:
		if int(value) > 0 {
			return int(value)
		}
	case int:
		if value > 0 {
			return value
		}
	case json.Number:
		if n, err := strconv.Atoi(value.String()); err == nil && n > 0 {
			return n
		}
	}
	return fallback
}
func withPath(r *http.Request, path string) *http.Request {
	clone := r.Clone(r.Context())
	u := *r.URL
	u.Path = path
	clone.URL = &u
	return clone
}

func (rt *Runtime) status(w http.ResponseWriter, r *http.Request) {
	settings, err := rt.repo.ServerSettings(r.Context())
	if err != nil {
		http.Error(w, "database not ready", 503)
		return
	}
	var roots int
	if err := rt.db.QueryRowContext(r.Context(), `SELECT COUNT(*) FROM users WHERE type='root'`).Scan(&roots); err != nil {
		http.Error(w, "database not ready", 503)
		return
	}
	methods, ok := settings["authActiveAuthMethods"].([]any)
	if !ok {
		methods = []any{"local"}
	}
	language := settings["language"]
	if language == nil || language == "" {
		language = "en-us"
	}
	message, _ := settings["authLoginCustomMessage"].(string)
	form := map[string]any{"authLoginCustomMessage": auth.SanitizeHTML(message)}
	for _, m := range methods {
		if m == "openid" {
			button := settings["authOpenIDButtonText"]
			if button == nil || button == "" {
				button = "Login with OpenId"
			}
			form["authOpenIDButtonText"] = button
			form["authOpenIDAutoLaunch"] = settings["authOpenIDAutoLaunch"] == true
			break
		}
	}
	payload := map[string]any{"app": "audiobookshelf", "serverVersion": Version, "isInit": roots > 0, "language": language, "authMethods": methods, "authFormData": form}
	if roots == 0 {
		payload["ConfigPath"] = rt.cfg.ConfigPath
		payload["MetadataPath"] = rt.cfg.MetadataPath
	}
	writeJSONForRequest(w, r, 200, payload)
}
func (rt *Runtime) settingsAllowIframe() bool {
	settings, err := rt.repo.ServerSettings(context.Background())
	if err != nil {
		return false
	}
	return settings["allowIframe"] == true || os.Getenv("ALLOW_IFRAME") == "1"
}

func isPublicCover(path string) bool {
	p := strings.Split(strings.Trim(path, "/"), "/")
	return len(p) == 4 && p[0] == "api" && p[1] == "items" && p[3] == "cover"
}
func isPublicAuthorImage(path string) bool {
	p := strings.Split(strings.Trim(path, "/"), "/")
	return len(p) == 4 && p[0] == "api" && p[1] == "authors" && p[3] == "image"
}
func isPublicPlayback(path string) bool {
	return strings.HasPrefix(path, "/hls/") || strings.HasPrefix(path, "/public/")
}

func (rt *Runtime) serveClient(w http.ResponseWriter, r *http.Request, path string) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		http.NotFound(w, r)
		return
	}
	root := rt.cfg.ClientPath
	if root == "" {
		http.NotFound(w, r)
		return
	}
	rel := strings.TrimPrefix(filepath.Clean("/"+path), "/")
	target := filepath.Join(root, rel)
	if !strings.HasPrefix(target, filepath.Clean(root)+string(os.PathSeparator)) && target != filepath.Clean(root) {
		http.NotFound(w, r)
		return
	}
	if info, err := os.Stat(target); err == nil && !info.IsDir() {
		http.ServeFile(w, r, target)
		return
	}
	// express.static serves index.html for the mount root and for directories
	// containing an index.html file; it never lists directories.
	if rel == "" {
		index := filepath.Join(root, "index.html")
		if _, err := os.Stat(index); err == nil {
			http.ServeFile(w, r, index)
			return
		}
		http.NotFound(w, r)
		return
	}
	index := filepath.Join(target, "index.html")
	if info, err := os.Stat(index); err == nil && info.Mode().IsRegular() {
		http.ServeFile(w, r, index)
		return
	}
	// Upstream (server/Server.js, default Nuxt client branch) serves index.html
	// only for the enumerated dynamic client routes; every other non-asset path
	// falls through to Express default 404.
	if matchesClientDynamicRoute(path) {
		index := filepath.Join(root, "index.html")
		if _, err := os.Stat(index); err != nil {
			http.NotFound(w, r)
			return
		}
		http.ServeFile(w, r, index)
		return
	}
	http.NotFound(w, r)
}

// clientDynamicRoutes mirrors the enumerated Vue/Nuxt client dynamic routes
// registered in server/Server.js for the default (non-REACT_CLIENT_PATH) client.
var clientDynamicRoutes = []string{
	"/item/:id",
	"/author/:id",
	"/audiobook/:id/chapters",
	"/audiobook/:id/edit",
	"/audiobook/:id/manage",
	"/library/:library",
	"/library/:library/search",
	"/library/:library/bookshelf/:id?",
	"/library/:library/authors",
	"/library/:library/narrators",
	"/library/:library/stats",
	"/library/:library/series/:id?",
	"/library/:library/podcast/search",
	"/library/:library/podcast/latest",
	"/library/:library/podcast/download-queue",
	"/config/users/:id",
	"/config/users/:id/sessions",
	"/config/item-metadata-utils/:id",
	"/collection/:id",
	"/playlist/:id",
	"/share/:slug",
}

var clientDynamicRoutePatterns = buildClientDynamicRoutePatterns()

func buildClientDynamicRoutePatterns() []*regexp.Regexp {
	patterns := make([]*regexp.Regexp, 0, len(clientDynamicRoutes))
	for _, route := range clientDynamicRoutes {
		out := "^"
		for _, segment := range strings.Split(strings.Trim(route, "/"), "/") {
			switch {
			case strings.HasPrefix(segment, ":") && strings.HasSuffix(segment, "?"):
				out += "(?:/[^/]+)?"
			case strings.HasPrefix(segment, ":"):
				out += "/[^/]+"
			default:
				out += "/" + regexp.QuoteMeta(segment)
			}
		}
		patterns = append(patterns, regexp.MustCompile(out+"\\/?$"))
	}
	return patterns
}

func matchesClientDynamicRoute(path string) bool {
	for _, pattern := range clientDynamicRoutePatterns {
		if pattern.MatchString(path) {
			return true
		}
	}
	return false
}

func writePlain(w http.ResponseWriter, status int, body string) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(status)
	_, _ = w.Write([]byte(body))
}
