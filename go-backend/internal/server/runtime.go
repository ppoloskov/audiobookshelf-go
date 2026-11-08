// Package server composes the Audiobookshelf runtime: every handler, the
// realtime hub, and the manager that swaps them on backup restore.
package server

import (
	"audiobookshelf-compat/internal/serversettings"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"maps"
	"net/http"
	"os"
	pathpkg "path"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"audiobookshelf-compat/internal/access"
	"audiobookshelf-compat/internal/adminops"
	"audiobookshelf-compat/internal/audiotools"
	"audiobookshelf-compat/internal/backup"
	"audiobookshelf-compat/internal/catalog"
	"audiobookshelf-compat/internal/config"
	"audiobookshelf-compat/internal/emailapi"
	"audiobookshelf-compat/internal/events"
	"audiobookshelf-compat/internal/filterdata"
	"audiobookshelf-compat/internal/httputil"
	"audiobookshelf-compat/internal/identity"
	"audiobookshelf-compat/internal/lists"
	"audiobookshelf-compat/internal/logging"
	"audiobookshelf-compat/internal/notifications"
	"audiobookshelf-compat/internal/playback"
	"audiobookshelf-compat/internal/podcasts"
	"audiobookshelf-compat/internal/providers"
	"audiobookshelf-compat/internal/realtime"
	"audiobookshelf-compat/internal/scanner"
	"audiobookshelf-compat/internal/sharing"
	"audiobookshelf-compat/internal/store"
	"audiobookshelf-compat/internal/textclean"
	"audiobookshelf-compat/internal/userapi"
	"audiobookshelf-compat/internal/version"
)

type Runtime struct {
	cfg           config.Config
	db            *sql.DB
	identity      *identity.Handler
	realtime      *realtime.Hub
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
	routes        *http.ServeMux
}

func isGet(r *http.Request) bool { return r.Method == http.MethodGet || r.Method == http.MethodHead }

// newRuntime builds the handlers over db. The hub belongs to the manager and
// survives a restore.
func newRuntime(db *sql.DB, cfg config.Config, m *Manager) (rt *Runtime, err error) {
	// A restored database may carry a different logLevel.
	if settings, err := serversettings.Get(context.Background(), db); err == nil {
		ApplyLoggingSettings(settings.Doc())
	}
	hub := m.hub
	emit := events.Emitter(hub.Emit)
	rt = &Runtime{cfg: cfg, db: db, realtime: hub}
	defer func() {
		if err != nil {
			rt.Close()
			rt = nil
		}
	}()
	filters := filterdata.NewCache()
	if rt.identity, err = identity.New(db, cfg, emit); err != nil {
		return rt, err
	}
	if rt.providers, err = providers.New(db, emit); err != nil {
		return rt, err
	}
	rt.podcasts = podcasts.New(db, cfg, emit)
	if rt.scanner, err = scanner.New(db, cfg, emit, filters); err != nil {
		return rt, err
	}
	rt.catalog = catalog.New(db, cfg, emit, catalog.Deps{
		Filters:     filters,
		Downloads:   rt.podcasts,
		ItemChanged: rt.scanner.WriteMetadata,
		LibraryChanged: func(string) {
			if err := rt.scanner.Refresh(context.Background()); err != nil {
				logging.Errorf("[Server] refresh watchers and schedules: %v", err)
			}
		},
		Authors:                rt.providers,
		Books:                  rt.providers,
		PodcastEpisodes:        rt.podcasts.QuickMatchEpisodes,
		PodcastScheduleChanged: rt.podcasts.Reschedule,
	})
	rt.playback = playback.New(db, cfg, emit)
	if rt.userapi, err = userapi.New(db, rt.identity, emit, userapi.Deps{UserChanged: hub.Refresh, UsersOnline: hub.UsersOnline, OpenSessions: rt.playback.OpenSessions}); err != nil {
		return rt, err
	}
	rt.notifications = notifications.New(db, emit)
	stored, err := serversettings.Get(context.Background(), db)
	if err != nil {
		return rt, err
	}
	if rt.backup, err = backup.New(cfg, backupOptions(stored.Doc(), m), emit, rt.notifications.TriggerEvent); err != nil {
		return rt, err
	}
	rt.adminops = adminops.New(db, cfg, emit, adminops.Deps{
		AfterWrite: func() { filters.Invalidate("") },
		SettingsChanged: func(ctx context.Context, settings map[string]any) error {
			if err := rt.backup.UpdateOptions(ctx, backupOptions(settings, m)); err != nil {
				return err
			}
			return rt.scanner.Refresh(ctx)
		},
		ItemChanged: rt.catalog.ItemChanged,
		WatcherUpdate: func(_ context.Context, libraryID, path, kind string) error {
			rt.scanner.UpdateWatchedPath(libraryID, path, kind)
			return nil
		},
	})
	rt.emailapi = emailapi.New(db, emit)
	rt.sharing = sharing.New(db, cfg, emit)
	rt.audiotools = audiotools.New(db, cfg, emit)
	listHandler := lists.New(db, emit)

	// Routes register before backup.Routes so its patterns do not shadow them.
	rt.routes = http.NewServeMux()
	listHandler.Routes(rt.routes)
	rt.catalog.Routes(rt.routes)
	for _, f := range []interface{ Routes(*http.ServeMux) }{rt.adminops, rt.audiotools, rt.providers, rt.sharing, rt.userapi, rt.playback, rt.notifications, rt.emailapi, rt.scanner, rt.podcasts, rt.backup} {
		f.Routes(rt.routes)
	}
	logging.Default().BindSockets(func(socketID string, entry logging.LogEntry) {
		hub.EmitSocket(socketID, "log", entry)
	})
	if err = rt.scanner.Start(context.Background()); err != nil {
		return rt, err
	}
	return rt, nil
}

func (rt *Runtime) DB() *sql.DB { return rt.db }

func (rt *Runtime) Authenticate(r *http.Request) (store.User, error) {
	return rt.identity.Authenticate(r)
}

func (rt *Runtime) Session(userID string) any { return rt.playback.SessionOf(userID) }

func (rt *Runtime) Dispatch(_ context.Context, action realtime.Action) error {
	switch action.Event {
	case "cancel_scan":
		libraryID, ok := action.Payload.(string)
		if !ok || libraryID == "" {
			return fmt.Errorf("invalid cancel_scan payload")
		}
		rt.scanner.Cancel(libraryID)
	case "set_log_listener":
		level, ok := logLevel(action.Payload)
		if !ok {
			return fmt.Errorf("invalid log listener level")
		}
		logging.Default().AddSocketListener(action.SocketID, logging.Level(level))
	case "remove_log_listener", "disconnect":
		logging.Default().RemoveSocketListener(action.SocketID)
	case "search_covers":
		payload, _ := action.Payload.(map[string]any)
		requestID, _ := payload["requestId"].(string)
		title, _ := payload["title"].(string)
		author, _ := payload["author"].(string)
		provider, _ := payload["provider"].(string)
		podcast, _ := payload["podcast"].(bool)
		rt.providers.StartCoverSearch(providers.CoverRequest{RequestID: requestID, Title: title, Author: author, Provider: provider, Podcast: podcast}, action.Reply)
	case "cancel_cover_search":
		requestID, _ := action.Payload.(string)
		rt.providers.CancelCoverSearch(requestID, action.Reply)
	}
	return nil
}

func (rt *Runtime) Close() {
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
}

func (rt *Runtime) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Referrer-Policy", "no-referrer")
	// Engine.IO is mounted both at the legacy root and under the configured
	// router prefix, so dispatch it using the original URL before stripping.
	// It is not wrapped by the panic guard because WebSocket upgrades need the
	// original ResponseWriter's Hijacker.
	for _, socketPath := range rt.realtime.Paths() {
		if r.URL.Path == socketPath || strings.HasPrefix(r.URL.Path, socketPath+"/") {
			rt.realtime.ServeHTTP(w, r)
			return
		}
	}
	guardPanics(w, r, rt.serveHTTP)
}

func (rt *Runtime) serveHTTP(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, rt.cfg.RouterBasePath)
	if path == "" {
		path = "/"
	}
	if !rt.settingsAllowIframe() {
		w.Header().Set("Content-Security-Policy", "frame-ancestors 'self'")
	}
	if isGet(r) && path == "/ping" {
		httputil.WriteJSON(w, 200, map[string]any{"success": true}, httputil.ForRequest(r))
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
	if !strings.HasPrefix(path, "/api/") && path != "/api" {
		// Non-API patterns on the mux are public capability URLs (feeds,
		// shares, HLS, session tracks), as in TS: they run with no user.
		if rt.serveRoute(w, withPath(r, path), store.User{}) {
			return
		}
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
	if rt.serveRoute(w, withPath(r, path), user) {
		return
	}
	// Express answers an unknown route 404; clients branch on it (Absorb
	// falls back to older endpoints on 404).
	httputil.Fail(w, r, httputil.Status(http.StatusNotFound, ""))
}

// serveRoute dispatches a request to a handler registered on the route mux and
// reports whether one matched. The user was authenticated already (the zero
// user for the public image and cover reads) and travels in the context. A
// path that is not in canonical form is not matched, because the mux would
// answer it with a redirect.
func (rt *Runtime) serveRoute(w http.ResponseWriter, r *http.Request, user store.User) bool {
	if r.URL.Path != pathpkg.Clean(r.URL.Path) {
		canonical, ok := routeAlias(r.URL.Path)
		if !ok {
			return false
		}
		r = withPath(r, canonical)
	}
	if _, pattern := rt.routes.Handler(r); pattern == "" {
		return false
	}
	ctx := store.ContextWithUser(r.Context(), user)
	if user.ID != "" {
		ctx = access.WithRequest(ctx, user)
	}
	rt.routes.ServeHTTP(w, r.WithContext(ctx))
	return true
}

// routeAlias maps a path Express would route to its canonical form: Express
// routing is non-strict, so one trailing slash matches the route without it.
// The list roots also answer "//" (TS router.use("/collections", ...) quirk).
func routeAlias(path string) (string, bool) {
	canonical := path
	switch path {
	case "/api/collections//":
		canonical = "/api/collections"
	case "/api/playlists//":
		canonical = "/api/playlists"
	default:
		if !strings.HasSuffix(path, "/") || strings.HasSuffix(path, "//") {
			return "", false
		}
		canonical = strings.TrimSuffix(path, "/")
	}
	return canonical, pathpkg.Clean(canonical) == canonical
}

// backupOptions is the backup configuration the stored settings and
// BACKUP_PATH describe; both the first start and every settings change use it.
func backupOptions(settings map[string]any, manager *Manager) backup.Options {
	settings = maps.Clone(settings)
	serversettings.ApplyEnv(settings)
	backupPath, _ := settings["backupPath"].(string)
	maxSize, _ := settings["maxBackupSize"].(float64) // 0 is unlimited: BackupManager.js:46
	return backup.Options{BackupPath: backupPath, BackupPathEnvSet: serversettings.EnvBackupPath() != "", BackupSchedule: stringSetting(settings, "backupSchedule"), BackupsToKeep: intSetting(settings, "backupsToKeep", 0), MaxBackupSizeGB: maxSize, Restore: manager.restore, UpdatePath: manager.updateBackupPath}
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
	stored, err := serversettings.Get(r.Context(), rt.db)
	if err != nil {
		http.Error(w, "database not ready", 503)
		return
	}
	var roots int
	if err := rt.db.QueryRowContext(r.Context(), `SELECT COUNT(*) FROM users WHERE type='root'`).Scan(&roots); err != nil {
		http.Error(w, "database not ready", 503)
		return
	}
	settings := stored.Doc()
	methods, ok := settings["authActiveAuthMethods"].([]any)
	if !ok {
		methods = []any{"local"}
	}
	language := settings["language"]
	if language == nil || language == "" {
		language = "en-us"
	}
	message, _ := settings["authLoginCustomMessage"].(string)
	form := map[string]any{"authLoginCustomMessage": textclean.Sanitize(message)}
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
	payload := map[string]any{"app": "audiobookshelf", "serverVersion": version.Server, "isInit": roots > 0, "language": language, "authMethods": methods, "authFormData": form}
	if roots == 0 {
		payload["ConfigPath"] = rt.cfg.ConfigPath
		payload["MetadataPath"] = rt.cfg.MetadataPath
	}
	httputil.WriteJSON(w, 200, payload, httputil.ForRequest(r))
}
func (rt *Runtime) settingsAllowIframe() bool {
	settings, err := serversettings.Get(context.Background(), rt.db)
	if err != nil {
		return false
	}
	return settings.AllowIframe()
}

func isPublicCover(path string) bool {
	p := strings.Split(strings.Trim(path, "/"), "/")
	return len(p) == 4 && p[0] == "api" && p[1] == "items" && p[3] == "cover"
}
func isPublicAuthorImage(path string) bool {
	p := strings.Split(strings.Trim(path, "/"), "/")
	return len(p) == 4 && p[0] == "api" && p[1] == "authors" && p[3] == "image"
}
func (rt *Runtime) serveClient(w http.ResponseWriter, r *http.Request, path string) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		writeClientNotFound(w, r)
		return
	}
	root := rt.cfg.ClientPath
	if root == "" {
		writeClientNotFound(w, r)
		return
	}
	rel := strings.TrimPrefix(filepath.Clean("/"+path), "/")
	target := filepath.Join(root, rel)
	if !strings.HasPrefix(target, filepath.Clean(root)+string(os.PathSeparator)) && target != filepath.Clean(root) {
		writeClientNotFound(w, r)
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
		writeClientNotFound(w, r)
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
			writeClientNotFound(w, r)
			return
		}
		http.ServeFile(w, r, index)
		return
	}
	writeClientNotFound(w, r)
}

// writeClientNotFound is the Express default 404 of the client branch.
func writeClientNotFound(w http.ResponseWriter, r *http.Request) {
	writeExpressError(w, r, http.StatusNotFound, "Cannot "+r.Method+" "+r.URL.EscapedPath())
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
