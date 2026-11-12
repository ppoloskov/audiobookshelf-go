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
}
