// Package server composes the Audiobookshelf runtime: every handler, the
// realtime hub, and the manager that swaps them on backup restore.
package server

import (
	"audiobookshelf-compat/internal/serversettings"
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"os"
	pathpkg "path"
	"path/filepath"
	"regexp"
	"strings"

	"audiobookshelf-compat/internal/admin"
	"audiobookshelf-compat/internal/auth"
	"audiobookshelf-compat/internal/author"
	"audiobookshelf-compat/internal/backup"
	"audiobookshelf-compat/internal/browse"
	"audiobookshelf-compat/internal/config"
	"audiobookshelf-compat/internal/db"
	"audiobookshelf-compat/internal/db/sqlc"
	"audiobookshelf-compat/internal/events"
	"audiobookshelf-compat/internal/filterdata"
	"audiobookshelf-compat/internal/httputil"
	"audiobookshelf-compat/internal/item"
	"audiobookshelf-compat/internal/libraries"
	"audiobookshelf-compat/internal/list"
	"audiobookshelf-compat/internal/logging"
	"audiobookshelf-compat/internal/mail"
	"audiobookshelf-compat/internal/metadata"
	"audiobookshelf-compat/internal/notify"
	"audiobookshelf-compat/internal/playback"
	"audiobookshelf-compat/internal/podcast"
	"audiobookshelf-compat/internal/realtime"
	"audiobookshelf-compat/internal/scan"
	"audiobookshelf-compat/internal/settings"
	"audiobookshelf-compat/internal/share"
	"audiobookshelf-compat/internal/textclean"
	"audiobookshelf-compat/internal/tools"
	"audiobookshelf-compat/internal/user"
	"audiobookshelf-compat/internal/version"
)

type Runtime struct {
	cfg           config.Config
	db            *sql.DB
	data          *db.DB          // the new data layer; rewritten packages use it
	settings      *settings.Store // typed settings for rewritten packages
	auth          *auth.Service
	authRoutes    *http.ServeMux // the public authentication routes
	realtime      *realtime.Hub
	libraries     *libraries.Handler
	playback      *playback.Handler
	users         *user.Handler
	admin         *admin.Handler
	scanner       *scan.Scanner
	tools         *tools.Handler
	backup        *backup.Handler
	emailapi      *mail.Handler
	notifications *notify.Handler
	share         *share.Handler
	podcasts      *podcast.Handler
	metadata      *metadata.Handler
	routes        *http.ServeMux
}

func isGet(r *http.Request) bool { return r.Method == http.MethodGet || r.Method == http.MethodHead }

// newRuntime builds the handlers over db. The hub belongs to the manager and
// survives a restore.
func newRuntime(sqlDB *sql.DB, cfg config.Config, m *Manager) (rt *Runtime, err error) {
	hub := m.hub
	emit := events.Emitter(hub.Emit)
	rt = &Runtime{cfg: cfg, db: sqlDB, realtime: hub}
	defer func() {
		if err != nil {
			rt.Close()
			rt = nil
		}
	}()
	rt.data = db.New(sqlDB)
	if rt.settings, err = settings.NewStore(context.Background(), rt.data, os.Getenv, cfg.Dirs().Backups); err != nil {
		return rt, err
	}
	ApplyLoggingSettings(rt.settings.Server()) // a restored database may carry a different logLevel
	// Transitional: old packages still save server settings through
	// serversettings; keep the typed cache current until they are gone.
	serversettings.SetOnSave(func(*sql.DB) {
		if err := rt.settings.Reload(context.Background()); err != nil {
			logging.Errorf("[Server] reload settings: %v", err)
		}
	})
	filters := filterdata.NewCache()
	if rt.auth, err = auth.New(rt.data, rt.settings, emit, cfg); err != nil {
		return rt, err
	}
	// Transitional: auth may have stored a new token secret; the old
	// serversettings cache must not save a stale document over it.
	serversettings.Invalidate(sqlDB)
	rt.authRoutes = http.NewServeMux()
	rt.auth.Routes(rt.authRoutes)
	rt.metadata = metadata.New(rt.data, emit)
	rt.notifications = notify.New(rt.settings, emit)
	listHandler := list.New(rt.data, rt.settings, emit)
	rt.podcasts = podcast.New(rt.data, rt.settings, cfg, emit, rt.notifications.TriggerEvent, listHandler.AnnouncePlaylists)
	rt.scanner = scan.New(rt.data, rt.settings, emit, filters, cfg, listHandler.AnnouncePlaylists)
	libraryChanged := func(string) {
		if err := rt.scanner.Refresh(context.Background()); err != nil {
			logging.Errorf("[Server] refresh watchers and schedules: %v", err)
		}
	}
	itemMetadataDir := ""
	if cfg.MetadataPath != "" {
		itemMetadataDir = cfg.Dirs().Items
	}
	itemHandler := item.New(rt.data, rt.settings, emit, item.Deps{
		Filters:           filters,
		Downloads:         rt.podcasts,
		Books:             rt.metadata,
		AnnouncePlaylists: listHandler.AnnouncePlaylists,
		WriteMetadata:     rt.scanner.WriteMetadata,
		ScheduleChanged:   rt.podcasts.Reschedule,
		PodcastEpisodes:   rt.podcasts.QuickMatchEpisodes,
		CoverCacheDir:     cfg.Dirs().CoverCache,
		ItemMetadataDir:   itemMetadataDir,
	})
	rt.libraries = libraries.New(rt.data, rt.settings, emit, libraries.Deps{
		Filters:        filters,
		LibraryChanged: libraryChanged,
		QuickMatch:     itemHandler.QuickMatch,
		Items:          itemHandler,
	})
	rt.playback = playback.New(rt.data, rt.settings, cfg, emit)
	rt.users = user.New(rt.data, rt.auth, rt.settings, emit, rt.playback, user.Deps{UserChanged: hub.Refresh, UsersOnline: func() []map[string]any { return onlineUsers(hub) }})
	if rt.backup, err = backup.New(rt.settings, cfg, os.Getenv, emit, rt.notifications.TriggerEvent, m.restore); err != nil {
		return rt, err
	}
	// Settings changes reach the old serversettings cache and the running
	// server (log level, watchers and schedules); backup follows them itself.
	rt.settings.OnChange(func(server settings.Server) {
		serversettings.Invalidate(sqlDB)
		logging.Default().SetLevel(logging.Level(server.LogLevel))
		if err := rt.scanner.Refresh(context.Background()); err != nil {
			logging.Errorf("[Server] refresh watchers and schedules: %v", err)
		}
	})
	rt.admin = admin.New(rt.data, rt.settings, cfg, os.Getenv, admin.Deps{
		TaxonomyChanged: func() { filters.Invalidate("") },
		ItemChanged:     itemHandler.ItemChanged,
		WatcherUpdate:   rt.scanner.UpdateWatchedPath,
	})
	rt.emailapi = mail.New(rt.data, rt.settings, emit)
	rt.share = share.New(rt.data, emit, cfg.RouterBasePath)
	rt.tools = tools.New(rt.data, cfg, emit)

	// Routes register before backup.Routes so its patterns do not shadow them.
	rt.routes = http.NewServeMux()
	listHandler.Routes(rt.routes)
	itemHandler.Routes(rt.routes)
	rt.libraries.Routes(rt.routes)
	author.New(rt.data, rt.settings, emit, author.Deps{
		Filters:       filters,
		Authors:       rt.metadata,
		WriteMetadata: rt.scanner.WriteMetadata,
		ImageDir:      cfg.Dirs().Authors,
		ImageCacheDir: cfg.Dirs().ImageCache,
	}).Routes(rt.routes)
	browse.New(rt.data, rt.settings, emit).Routes(rt.routes)
	for _, f := range []interface{ Routes(*http.ServeMux) }{rt.admin, rt.tools, rt.metadata, rt.share, rt.users, rt.playback, rt.notifications, rt.emailapi, rt.scanner, rt.podcasts, rt.backup} {
		f.Routes(rt.routes)
	}
	logging.Default().SetSocketSink(func(socketID string, entry logging.Entry) {
		hub.EmitSocket(socketID, "log", entry)
	})
	if err = rt.scanner.Start(context.Background()); err != nil {
		return rt, err
	}
	return rt, nil
}

// DB is the data layer the realtime hub writes lastSeen through.
func (rt *Runtime) DB() *db.DB { return rt.data }

// Authenticate is the realtime hub's token check: the HTTP bearer check.
func (rt *Runtime) Authenticate(r *http.Request) (*auth.Principal, error) {
	return rt.auth.Authenticate(r)
}

func (rt *Runtime) Session(userID string) any { return rt.playback.SessionOf(userID) }

// The remaining methods are realtime.Actions: what clients can ask over the
// socket. The hub has validated the payloads and the caller's rights.

func (rt *Runtime) CancelScan(libraryID string) { rt.scanner.Cancel(libraryID) }

func (rt *Runtime) SetLogListener(socketID string, level int) {
	logging.Default().AddListener(socketID, logging.Level(level))
}

func (rt *Runtime) RemoveLogListener(socketID string) { logging.Default().RemoveListener(socketID) }

func (rt *Runtime) SearchCovers(search realtime.CoverSearch, reply realtime.Reply) {
	rt.metadata.StartCoverSearch(metadata.CoverRequest{RequestID: search.RequestID, Title: search.Title, Author: search.Author, Provider: search.Provider, Podcast: search.Podcast}, reply)
}

func (rt *Runtime) CancelCoverSearch(requestID string, reply realtime.Reply) {
	rt.metadata.CancelCoverSearch(requestID, reply)
}

// onlineUsers adapts the hub's typed list to user.Deps, which takes maps; it
// is only ever marshalled again.
func onlineUsers(hub *realtime.Hub) []map[string]any {
	var maps []map[string]any
	if raw, err := json.Marshal(hub.UsersOnline()); err == nil {
		_ = json.Unmarshal(raw, &maps) // our own output; an empty list stays empty
	}
	if maps == nil {
		maps = []map[string]any{}
	}
	return maps
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
	if rt.tools != nil {
		rt.tools.Close()
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
		httputil.WriteJSON(w, r, 200, map[string]any{"success": true})
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
	if _, pattern := rt.authRoutes.Handler(withPath(r, path)); pattern != "" {
		rt.authRoutes.ServeHTTP(w, withPath(r, path))
		return
	}
	if !strings.HasPrefix(path, "/api/") && path != "/api" {
		// Non-API patterns on the mux are public capability URLs (feeds,
		// shares, HLS, session tracks), as in TS: they run with no user.
		if rt.serveRoute(w, withPath(r, path), nil) {
			return
		}
		rt.serveClient(w, r, path)
		return
	}
	// The upstream intentionally leaves item covers and author images public.
	publicRead := isGet(r) && (isPublicCover(path) || isPublicAuthorImage(path))
	serve := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !rt.serveRoute(w, r, auth.User(r.Context())) {
			// Express answers an unknown route 404; clients branch on it (Absorb
			// falls back to older endpoints on 404).
			httputil.Fail(w, r, httputil.Status(http.StatusNotFound, ""))
		}
	})
	if publicRead {
		serve.ServeHTTP(w, withPath(r, path))
		return
	}
	rt.auth.Middleware(serve).ServeHTTP(w, withPath(r, path))
}

// serveRoute dispatches a request to a handler registered on the route mux and
// reports whether one matched. The principal was authenticated already (nil for
// the public image and cover reads) and travels in the context. A path that is
// not in canonical form is not matched, because the mux would answer it with a
// redirect.
func (rt *Runtime) serveRoute(w http.ResponseWriter, r *http.Request, principal *auth.Principal) bool {
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
	if principal != nil {
		r = r.WithContext(auth.WithUser(r.Context(), principal))
	}
	rt.routes.ServeHTTP(w, r)
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
	roots, err := sqlc.New(rt.db).CountRootUsers(r.Context())
	if err != nil {
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
	httputil.WriteJSON(w, r, 200, payload)
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
