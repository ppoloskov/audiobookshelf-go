package catalog

import "net/http"

// RouteDeps are the collaborators of the routes Routes registers. They are
// interfaces defined here and satisfied by feature packages, because feature
// packages do not import each other; the composition root wires them.
type RouteDeps struct {
	// Authors looks authors up at the metadata provider (POST /api/authors/:id/match).
	Authors AuthorFinder
}

// Routes registers the catalog routes that use method patterns on mux
// (ARCHITECTURE.md "Routing"). The server authenticates a request and attaches
// the user to its context before dispatching (store.ContextWithUser); the
// public image read gets none. Routes that Handle still serves are not
// registered here.
func (h *Handler) Routes(mux *http.ServeMux, deps RouteDeps) {
	h.authorFinder = deps.Authors

	// server/routers/ApiRouter.js:217-223.
	mux.HandleFunc("GET /api/authors/{id}", h.authorFindOne)
	mux.HandleFunc("PATCH /api/authors/{id}", h.authorUpdate)
	mux.HandleFunc("DELETE /api/authors/{id}", h.authorDelete)
	mux.HandleFunc("POST /api/authors/{id}/match", h.authorMatch)
	mux.HandleFunc("GET /api/authors/{id}/image", h.authorImage)
	mux.HandleFunc("POST /api/authors/{id}/image", h.authorUploadImage)
	mux.HandleFunc("DELETE /api/authors/{id}/image", h.authorDeleteImage)
}
