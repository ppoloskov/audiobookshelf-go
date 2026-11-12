// Package server is the composition root: it builds every domain over one
// database, serves them through one middleware chain, and swaps the whole
// graph when a backup is restored.
package server

import (
	"context"
	"fmt"
	"net/http"

	"audiobookshelf-compat/internal/admin"
	"audiobookshelf-compat/internal/auth"
	"audiobookshelf-compat/internal/author"
	"audiobookshelf-compat/internal/backup"
	"audiobookshelf-compat/internal/browse"
	"audiobookshelf-compat/internal/config"
	"audiobookshelf-compat/internal/db"
	"audiobookshelf-compat/internal/events"
	"audiobookshelf-compat/internal/filterdata"
	"audiobookshelf-compat/internal/item"
	"audiobookshelf-compat/internal/library"
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
	"audiobookshelf-compat/internal/tools"
	"audiobookshelf-compat/internal/user"
)

// Runtime is every domain built over one database. It is replaced as a whole
// on restore; the realtime hub and the database are owned by the Manager.
// It implements realtime.Backend.
type Runtime struct {
	cfg      config.Config
	data     *db.DB
	settings *settings.Store
	auth     *auth.Service
	hub      *realtime.Hub
	scanner  *scan.Scanner
	metadata *metadata.Handler
	playback *playback.Handler
	handler  http.Handler
	closers  []func() // domain Close methods, in construction order
}

// newRuntime allocates the domains over data. It starts no background work
// (see start). restore is the upward call the backup domain makes to swap the
// runtime. On error everything built so far is closed.
func newRuntime(cfg config.Config, data *db.DB, hub *realtime.Hub, getenv func(string) string, restore backup.Restore) (*Runtime, error) {
	rt := &Runtime{cfg: cfg, data: data, hub: hub}
	if err := rt.build(getenv, restore); err != nil {
		rt.Close()
		return nil, err
	}
	return rt, nil
}

// build constructs the domains in dependency order: settings, auth, the
// domains that others call into, then the ones that call upward.
func (rt *Runtime) build(getenv func(string) string, restore backup.Restore) (err error) {
	cfg, ctx := rt.cfg, context.Background()
	emit := events.Emitter(rt.hub.Emit)
	if rt.settings, err = settings.NewStore(ctx, rt.data, getenv, cfg.Dirs().Backups); err != nil {
		return err
	}
	ApplyLoggingSettings(rt.settings.Server()) // a restored database may carry a different logLevel
	if rt.auth, err = auth.New(rt.data, rt.settings, emit, cfg); err != nil {
		return err
	}
	filters := filterdata.NewCache()
	rt.metadata = metadata.New(rt.data, emit)
	notifier := notify.New(rt.settings, emit)
	lists := list.New(rt.data, rt.settings, emit)
	podcasts := podcast.New(rt.data, rt.settings, filters, cfg, emit, notifier.TriggerEvent, lists.AnnouncePlaylists)
	rt.own(podcasts.Close)
	rt.scanner = scan.New(rt.data, rt.settings, emit, filters, cfg, lists.AnnouncePlaylists)
	rt.own(rt.scanner.Close)
	refreshWatchers := func() {
		if err := rt.scanner.Refresh(ctx); err != nil {
			logging.Errorf("[Server] refresh watchers and schedules: %v", err)
		}
	}
	itemMetadataDir := ""
	if cfg.MetadataPath != "" {
		itemMetadataDir = cfg.Dirs().Items
	}
	items := item.New(rt.data, rt.settings, emit, item.Deps{
		Filters:           filters,
		Downloads:         podcasts,
		Books:             rt.metadata,
		AnnouncePlaylists: lists.AnnouncePlaylists,
		WriteMetadata:     rt.scanner.WriteMetadata,
		ScheduleChanged:   podcasts.Reschedule,
		PodcastEpisodes:   podcasts.QuickMatchEpisodes,
		CoverCacheDir:     cfg.Dirs().CoverCache,
		ItemMetadataDir:   itemMetadataDir,
	})
	libs := library.New(rt.data, rt.settings, emit, library.Deps{
		Filters:        filters,
		LibraryChanged: func(string) { refreshWatchers() },
		QuickMatch:     items.QuickMatch,
		Items:          items,
	})
	rt.playback = playback.New(rt.data, rt.settings, cfg, emit)
	rt.own(rt.playback.Close)
	authors := author.New(rt.data, rt.settings, emit, author.Deps{
		Filters:       filters,
		Authors:       rt.metadata,
		WriteMetadata: rt.scanner.WriteMetadata,
		ImageDir:      cfg.Dirs().Authors,
		ImageCacheDir: cfg.Dirs().ImageCache,
	})
	users := user.New(rt.data, rt.auth, rt.settings, emit, rt.playback, user.Deps{UserChanged: rt.hub.Refresh, UsersOnline: rt.hub.UsersOnline})
	adm := admin.New(rt.data, rt.settings, cfg, getenv, admin.Deps{
		TaxonomyChanged: func() { filters.Invalidate("") },
		ItemChanged:     items.ItemChanged,
		WatcherUpdate:   rt.scanner.UpdateWatchedPath,
	})
	backups, err := backup.New(rt.settings, cfg, getenv, emit, notifier.TriggerEvent, restore)
	if err != nil {
		return err
	}
	rt.own(backups.Close)
	toolbox := tools.New(rt.data, cfg, emit)
	rt.own(toolbox.Close)

	// Settings changes reach the running server (log level, watchers and
	// schedules); backup follows them itself.
	rt.settings.OnChange(func(server settings.Server) {
		logging.Default().SetLevel(logging.Level(server.LogLevel))
		refreshWatchers()
	})
	logging.Default().SetSocketSink(func(socketID string, entry logging.Entry) {
		rt.hub.EmitSocket(socketID, "log", entry)
	})

	// Order matters: backup is last so its patterns do not shadow the others.
	routes := http.NewServeMux()
	for _, domain := range []interface{ Routes(*http.ServeMux) }{
		lists, items, libs, authors, browse.New(rt.data, rt.settings, emit, filters), adm, toolbox, rt.metadata,
		share.New(rt.data, emit, cfg.RouterBasePath), users, rt.playback, notifier,
		mail.New(rt.data, rt.settings, emit), rt.scanner, podcasts, backups,
	} {
		domain.Routes(routes)
	}
	rt.handler = rt.newHandler(routes, getenv("ALLOW_CORS") == "1")
	return nil
}

// own registers a domain's Close; Close runs them in reverse order.
func (rt *Runtime) own(closer func()) { rt.closers = append(rt.closers, closer) }

// start begins the background work: scanner watchers and schedules.
func (rt *Runtime) start(ctx context.Context) error {
	if err := rt.scanner.Start(ctx); err != nil {
		return fmt.Errorf("start scanner: %w", err)
	}
	return nil
}

// Close stops the domains, last built first, and waits for them. The
// database and the hub belong to the Manager.
func (rt *Runtime) Close() {
	for i := len(rt.closers) - 1; i >= 0; i-- {
		rt.closers[i]()
	}
	rt.closers = nil
}

// ServeHTTP serves the whole application: see newHandler.
func (rt *Runtime) ServeHTTP(w http.ResponseWriter, r *http.Request) { rt.handler.ServeHTTP(w, r) }

// DB is the data layer the realtime hub writes lastSeen through.
func (rt *Runtime) DB() *db.DB { return rt.data }

// Authenticate is the realtime hub's token check: the HTTP bearer check.
func (rt *Runtime) Authenticate(r *http.Request) (*auth.Principal, error) {
	return rt.auth.Authenticate(r)
}

// Session is the playback session of a user, for the hub's online list.
func (rt *Runtime) Session(userID string) any { return rt.playback.SessionOf(userID) }

// The remaining methods are realtime.Actions: what clients can ask over the
// socket. The hub has validated the payloads and the caller's rights.

// CancelScan cancels a running library scan.
func (rt *Runtime) CancelScan(libraryID string) { rt.scanner.Cancel(libraryID) }

// SetLogListener streams log entries at level and above to the socket.
func (rt *Runtime) SetLogListener(socketID string, level int) {
	logging.Default().AddListener(socketID, logging.Level(level))
}

// RemoveLogListener stops streaming log entries to the socket.
func (rt *Runtime) RemoveLogListener(socketID string) { logging.Default().RemoveListener(socketID) }

// SearchCovers starts a cover search and answers through reply.
func (rt *Runtime) SearchCovers(search realtime.CoverSearch, reply realtime.Reply) {
	rt.metadata.StartCoverSearch(metadata.CoverRequest{RequestID: search.RequestID, Title: search.Title, Author: search.Author, Provider: search.Provider, Podcast: search.Podcast}, reply)
}

// CancelCoverSearch cancels a cover search by request id.
func (rt *Runtime) CancelCoverSearch(requestID string, reply realtime.Reply) {
	rt.metadata.CancelCoverSearch(requestID, reply)
}
