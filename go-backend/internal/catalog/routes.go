package catalog

import (
	"audiobookshelf-compat/internal/httputil"
	"net/http"
)

// Routes registers the catalog routes that use method patterns on mux
// (ARCHITECTURE.md "Routing"). The server authenticates a request and attaches
// the user to its context before dispatching (store.ContextWithUser); the
// public image read gets none.
func (h *Handler) Routes(mux *http.ServeMux) {
	// server/routers/ApiRouter.js:217-223.
	mux.Handle("GET /api/authors/{id}", httputil.Handler(h.authorFindOne))
	mux.Handle("PATCH /api/authors/{id}", httputil.Handler(h.authorUpdate))
	mux.Handle("DELETE /api/authors/{id}", httputil.Handler(h.authorDelete))
	mux.Handle("POST /api/authors/{id}/match", httputil.Handler(h.authorMatch))
	mux.Handle("GET /api/authors/{id}/image", httputil.Handler(h.authorImage))
	mux.Handle("POST /api/authors/{id}/image", httputil.Handler(h.authorUploadImage))
	mux.Handle("DELETE /api/authors/{id}/image", httputil.Handler(h.authorDeleteImage))

	// server/routers/ApiRouter.js:228-229.
	mux.Handle("GET /api/series/{id}", httputil.Handler(h.seriesFindOne))
	mux.Handle("PATCH /api/series/{id}", httputil.Handler(h.seriesUpdate))

	// server/routers/ApiRouter.js:102-103.
	mux.Handle("POST /api/items/batch/delete", httputil.Handler(h.batchDelete))
	mux.Handle("POST /api/items/batch/update", httputil.Handler(h.batchUpdate))

	// server/routers/ApiRouter.js:90,105,116.
	mux.Handle("GET /api/libraries/{id}/matchall", h.inLibrary(h.libraryMatchAll))
	mux.Handle("POST /api/items/batch/quickmatch", httputil.Handler(h.batchQuickMatch))
	mux.Handle("POST /api/items/{id}/match", httputil.Handler(h.itemMatch))
	mux.Handle("GET /api/libraries/{id}/opml", h.inLibrary(h.libraryOPML))
	mux.Handle("POST /api/libraries/{id}/remove-metadata", h.inLibrary(h.libraryRemoveMetadata))
	mux.Handle("GET /api/libraries/{id}/download", h.inLibrary(h.libraryDownload))

	// server/routers/ApiRouter.js:60-91 (LibraryController), Express GET covers HEAD.
	mux.Handle("GET /api/libraries", httputil.Handler(h.libraries))
	mux.Handle("POST /api/libraries", httputil.Handler(h.createLibrary))
	mux.Handle("POST /api/libraries/order", httputil.Handler(h.reorderLibraries))
	mux.Handle("GET /api/libraries/{id}", h.inLibrary(h.library))
	mux.Handle("PATCH /api/libraries/{id}", httputil.Handler(h.updateLibrary))
	mux.Handle("DELETE /api/libraries/{id}", httputil.Handler(h.deleteLibrary))
	mux.Handle("GET /api/libraries/{id}/items", h.inLibrary(h.libraryItems))
	mux.Handle("DELETE /api/libraries/{id}/issues", h.inLibrary(h.removeLibraryItemsWithIssues))
	mux.Handle("GET /api/libraries/{id}/stats", h.inLibrary(h.libraryStats))
	mux.Handle("GET /api/libraries/{id}/filterdata", h.inLibrary(h.libraryFilterData))
	mux.Handle("GET /api/libraries/{id}/personalized", h.inLibrary(h.personalized))
	mux.Handle("GET /api/libraries/{id}/authors", h.inLibrary(h.authors))
	mux.Handle("GET /api/libraries/{id}/narrators", h.inLibrary(h.narrators))
	mux.Handle("PATCH /api/libraries/{id}/narrators/{narratorId}", h.inLibrary(h.updateNarrator))
	mux.Handle("DELETE /api/libraries/{id}/narrators/{narratorId}", h.inLibrary(h.updateNarrator))
	mux.Handle("GET /api/libraries/{id}/podcast-titles", h.inLibrary(h.podcastTitles))
	mux.Handle("GET /api/libraries/{id}/series", h.inLibrary(h.series))
	mux.Handle("GET /api/libraries/{id}/series/{seriesId}", h.inLibrary(h.seriesByID))
	mux.Handle("GET /api/libraries/{id}/search", h.inLibrary(h.search))

	// server/routers/ApiRouter.js:92-130 (LibraryItemController).
	mux.Handle("GET /api/items/{id}", httputil.Handler(h.item))
	mux.Handle("DELETE /api/items/{id}", httputil.Handler(h.deleteItem))
	mux.Handle("GET /api/items/{id}/download", httputil.Handler(h.itemDownload))
	mux.Handle("GET /api/items/{id}/file/{ino}", httputil.Handler(h.mediaFile))
	mux.Handle("GET /api/items/{id}/file/{ino}/download", httputil.Handler(h.mediaFile))
	mux.Handle("DELETE /api/items/{id}/file/{ino}", httputil.Handler(h.deleteLibraryFile))
	mux.Handle("GET /api/items/{id}/ffprobe/{ino}", httputil.Handler(h.ffprobe))
	mux.Handle("POST /api/items/batch/get", httputil.Handler(h.batchGet))
	mux.Handle("POST /api/items/{id}/chapters", httputil.Handler(h.updateChapters))
	mux.Handle("PATCH /api/items/{id}/tracks", httputil.Handler(h.updateTracks))
	mux.Handle("PATCH /api/items/{id}/ebook/{ino}/status", httputil.Handler(h.updateEbookStatus))
	mux.Handle("GET /api/items/{id}/metadata-object", httputil.Handler(h.metadataObject))
	mux.Handle("PATCH /api/items/{id}/media", httputil.Handler(h.updateMedia))
	mux.Handle("GET /api/items/{id}/ebook", httputil.Handler(h.ebookFile))
	mux.Handle("GET /api/items/{id}/ebook/{fileId}", httputil.Handler(h.ebookFile))
	mux.Handle("GET /api/items/{id}/cover", httputil.Handler(h.cover))
	mux.Handle("POST /api/items/{id}/cover", httputil.Handler(h.uploadCover))
	mux.Handle("PATCH /api/items/{id}/cover", httputil.Handler(h.updateCover))
	mux.Handle("DELETE /api/items/{id}/cover", httputil.Handler(h.removeCover))
}
