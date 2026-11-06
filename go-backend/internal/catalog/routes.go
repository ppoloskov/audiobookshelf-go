package catalog

import (
	"context"
	"net/http"

	"audiobookshelf-compat/internal/httputil"
	"audiobookshelf-compat/internal/store"
)

// RouteDeps are the collaborators of the routes Routes registers. They are
// interfaces defined here and satisfied by feature packages, because feature
// packages do not import each other; the composition root wires them.
type RouteDeps struct {
	// Authors looks authors up at the metadata provider (POST /api/authors/:id/match).
	Authors AuthorFinder
	// Books searches the metadata providers for a quick match.
	Books BookFinder
	// PodcastEpisodes quick matches a podcast's unmatched episodes against its
	// feed (Scanner.quickMatchPodcastEpisodes); it returns the number updated.
	PodcastEpisodes func(ctx context.Context, itemID string, overrideDetails bool) (int, error)
}

// Routes registers the catalog routes that use method patterns on mux
// (ARCHITECTURE.md "Routing"). The server authenticates a request and attaches
// the user to its context before dispatching (store.ContextWithUser); the
// public image read gets none.
func (h *Handler) Routes(mux *http.ServeMux, deps RouteDeps) {
	h.authorFinder = deps.Authors
	h.finder = deps.Books
	h.episodes = deps.PodcastEpisodes

	// server/routers/ApiRouter.js:217-223.
	mux.HandleFunc("GET /api/authors/{id}", h.authorFindOne)
	mux.HandleFunc("PATCH /api/authors/{id}", h.authorUpdate)
	mux.HandleFunc("DELETE /api/authors/{id}", h.authorDelete)
	mux.HandleFunc("POST /api/authors/{id}/match", h.authorMatch)
	mux.HandleFunc("GET /api/authors/{id}/image", h.authorImage)
	mux.HandleFunc("POST /api/authors/{id}/image", h.authorUploadImage)
	mux.HandleFunc("DELETE /api/authors/{id}/image", h.authorDeleteImage)

	// server/routers/ApiRouter.js:228-229.
	mux.HandleFunc("GET /api/series/{id}", h.seriesFindOne)
	mux.HandleFunc("PATCH /api/series/{id}", h.seriesUpdate)

	// server/routers/ApiRouter.js:102-103.
	mux.HandleFunc("POST /api/items/batch/delete", h.batchDelete)
	mux.HandleFunc("POST /api/items/batch/update", h.batchUpdate)

	// server/routers/ApiRouter.js:93-97.
	// server/routers/ApiRouter.js:90,105,116.
	mux.HandleFunc("GET /api/libraries/{id}/matchall", h.libraryMatchAll)
	mux.HandleFunc("POST /api/items/batch/quickmatch", h.batchQuickMatch)
	mux.HandleFunc("POST /api/items/{id}/match", h.itemMatch)
	mux.Handle("GET /api/libraries/{id}/opml", httputil.Handler(h.libraryOPML))
	mux.Handle("POST /api/libraries/{id}/remove-metadata", httputil.Handler(h.libraryRemoveMetadata))
	mux.Handle("GET /api/libraries/{id}/download", httputil.Handler(h.libraryDownload))

	// server/routers/ApiRouter.js:60-91 (LibraryController), Express GET covers HEAD.
	mux.Handle("GET /api/libraries", httputil.Handler(h.libraries))
	mux.Handle("POST /api/libraries", httputil.Handler(h.createLibrary))
	mux.Handle("POST /api/libraries/order", httputil.Handler(h.reorderLibraries))
	mux.Handle("GET /api/libraries/{id}", httputil.Handler(h.library))
	mux.Handle("PATCH /api/libraries/{id}", httputil.Handler(h.updateLibrary))
	mux.Handle("DELETE /api/libraries/{id}", httputil.Handler(h.deleteLibrary))
	mux.Handle("GET /api/libraries/{id}/items", httputil.Handler(h.libraryItems))
	mux.Handle("DELETE /api/libraries/{id}/issues", httputil.Handler(h.removeLibraryItemsWithIssues))
	mux.Handle("GET /api/libraries/{id}/stats", httputil.Handler(h.libraryStats))
	mux.Handle("GET /api/libraries/{id}/filterdata", httputil.Handler(h.libraryFilterData))
	mux.Handle("GET /api/libraries/{id}/personalized", httputil.Handler(h.personalized))
	mux.Handle("GET /api/libraries/{id}/authors", httputil.Handler(h.authors))
	mux.Handle("GET /api/libraries/{id}/narrators", httputil.Handler(h.narrators))
	mux.Handle("PATCH /api/libraries/{id}/narrators/{narratorId}", httputil.Handler(h.updateNarrator))
	mux.Handle("DELETE /api/libraries/{id}/narrators/{narratorId}", httputil.Handler(h.updateNarrator))
	mux.Handle("GET /api/libraries/{id}/podcast-titles", httputil.Handler(h.podcastTitles))
	mux.Handle("GET /api/libraries/{id}/series", httputil.Handler(h.series))
	mux.Handle("GET /api/libraries/{id}/series/{seriesId}", httputil.Handler(h.seriesByID))
	mux.Handle("GET /api/libraries/{id}/search", httputil.Handler(h.search))

	// server/routers/ApiRouter.js:92-130 (LibraryItemController). The handlers
	// in batch.go, batch_chapters.go and writes.go still take the user and ids
	// as arguments; byItem adapts them until they are converted.
	mux.Handle("GET /api/items/{id}", httputil.Handler(h.item))
	mux.Handle("DELETE /api/items/{id}", byItem(h.deleteItem))
	mux.Handle("GET /api/items/{id}/download", httputil.Handler(h.itemDownload))
	mux.Handle("GET /api/items/{id}/file/{ino}", httputil.Handler(h.mediaFile))
	mux.Handle("GET /api/items/{id}/file/{ino}/download", httputil.Handler(h.mediaFile))
	mux.HandleFunc("DELETE /api/items/{id}/file/{ino}", func(w http.ResponseWriter, r *http.Request) {
		h.deleteLibraryFile(w, r, requestUser(r), r.PathValue("id"), r.PathValue("ino"))
	})
	mux.HandleFunc("GET /api/items/{id}/ffprobe/{ino}", func(w http.ResponseWriter, r *http.Request) {
		h.ffprobe(w, r, requestUser(r), r.PathValue("id"), r.PathValue("ino"))
	})
	mux.HandleFunc("POST /api/items/batch/get", func(w http.ResponseWriter, r *http.Request) {
		h.batchGet(w, r, requestUser(r))
	})
	mux.Handle("POST /api/items/{id}/chapters", byItem(h.updateChapters))
	mux.Handle("PATCH /api/items/{id}/tracks", byItem(h.updateTracks))
	mux.HandleFunc("PATCH /api/items/{id}/ebook/{ino}/status", func(w http.ResponseWriter, r *http.Request) {
		h.updateEbookStatus(w, r, requestUser(r), r.PathValue("id"), r.PathValue("ino"))
	})
	mux.Handle("GET /api/items/{id}/metadata-object", byItem(h.metadataObject))
	mux.Handle("PATCH /api/items/{id}/media", byItem(h.updateMedia))
	mux.Handle("GET /api/items/{id}/ebook", httputil.Handler(h.ebookFile))
	mux.Handle("GET /api/items/{id}/ebook/{fileId}", httputil.Handler(h.ebookFile))
	mux.Handle("GET /api/items/{id}/cover", httputil.Handler(h.cover))
	mux.Handle("POST /api/items/{id}/cover", httputil.Handler(h.uploadCover))
	mux.Handle("PATCH /api/items/{id}/cover", byItem(h.updateCover))
	mux.Handle("DELETE /api/items/{id}/cover", byItem(h.removeCover))
}

// byItem adapts a handler that takes the user and the {id} item id as arguments.
func byItem(f func(http.ResponseWriter, *http.Request, store.User, string)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) { f(w, r, requestUser(r), r.PathValue("id")) }
}
