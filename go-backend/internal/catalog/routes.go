package catalog

import (
	"audiobookshelf-compat/internal/httputil"
	"audiobookshelf-compat/internal/library"
	"net/http"
)

// Routes registers the catalog routes that use method patterns on mux
// (ARCHITECTURE.md "Routing"). The server authenticates a request and attaches
// the user to its context before dispatching (store.ContextWithUser); the
// public image read gets none.
func (h *Handler) Routes(mux *http.ServeMux) {
	// server/routers/ApiRouter.js:102-103.

	// server/routers/ApiRouter.js:90,105,116.
	mux.Handle("GET /api/libraries/{id}/matchall", library.InLibrary(h.db, h.libraryMatchAll))
	mux.Handle("GET /api/libraries/{id}/download", library.InLibrary(h.db, h.libraryDownload))

	// server/routers/ApiRouter.js:60-91 (LibraryController), Express GET covers HEAD.
	mux.Handle("DELETE /api/libraries/{id}", httputil.Handler(h.deleteLibrary))
	mux.Handle("DELETE /api/libraries/{id}/issues", library.InLibrary(h.db, h.removeLibraryItemsWithIssues))

	// server/routers/ApiRouter.js:92-130 (LibraryItemController).
	mux.Handle("GET /api/items/{id}/download", httputil.Handler(h.itemDownload))
	mux.Handle("GET /api/items/{id}/file/{ino}", httputil.Handler(h.mediaFile))
	mux.Handle("GET /api/items/{id}/file/{ino}/download", httputil.Handler(h.mediaFile))
	mux.Handle("DELETE /api/items/{id}/file/{ino}", httputil.Handler(h.deleteLibraryFile))
	mux.Handle("GET /api/items/{id}/ffprobe/{ino}", httputil.Handler(h.ffprobe))
	mux.Handle("POST /api/items/{id}/chapters", httputil.Handler(h.updateChapters))
	mux.Handle("PATCH /api/items/{id}/tracks", httputil.Handler(h.updateTracks))
	mux.Handle("PATCH /api/items/{id}/ebook/{ino}/status", httputil.Handler(h.updateEbookStatus))
	mux.Handle("GET /api/items/{id}/metadata-object", httputil.Handler(h.metadataObject))
	mux.Handle("GET /api/items/{id}/ebook", httputil.Handler(h.ebookFile))
	mux.Handle("GET /api/items/{id}/ebook/{fileId}", httputil.Handler(h.ebookFile))
	mux.Handle("GET /api/items/{id}/cover", httputil.Handler(h.cover))
	mux.Handle("POST /api/items/{id}/cover", httputil.Handler(h.uploadCover))
	mux.Handle("PATCH /api/items/{id}/cover", httputil.Handler(h.updateCover))
	mux.Handle("DELETE /api/items/{id}/cover", httputil.Handler(h.removeCover))
}
