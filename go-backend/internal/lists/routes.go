package lists

import (
	"audiobookshelf-compat/internal/store"
	"net/http"

	"audiobookshelf-compat/internal/httputil"
)

// readOnly answers HEAD with the 404 Express gives these routes; GET patterns
// would otherwise serve it.
func readOnly(h httputil.Handler) httputil.Handler {
	return func(w http.ResponseWriter, r *http.Request) error {
		if r.Method == http.MethodHead {
			return httputil.Status(http.StatusNotFound, "")
		}
		return h(w, r)
	}
}

func (h *Handler) Routes(mux *http.ServeMux) {
	mux.Handle("GET /api/collections", readOnly(h.collectionRoot))
	mux.Handle("POST /api/collections", httputil.Handler(h.createCollection))
	mux.Handle("GET /api/collections/{id}", readOnly(h.getCollection))
	mux.Handle("PATCH /api/collections/{id}", httputil.Handler(h.updateCollection))
	mux.Handle("DELETE /api/collections/{id}", httputil.Handler(h.deleteCollection))
	mux.Handle("POST /api/collections/{id}/book", httputil.Handler(h.addCollectionBook))
	mux.Handle("DELETE /api/collections/{id}/book/{itemID}", httputil.Handler(h.removeCollectionBook))
	mux.Handle("POST /api/collections/{id}/batch/{action}", httputil.Handler(h.batchCollectionBooks))
	mux.Handle("GET /api/playlists", readOnly(h.listPlaylists))
	mux.Handle("POST /api/playlists", httputil.Handler(h.createPlaylist))
	mux.Handle("GET /api/playlists/{id}", readOnly(h.getPlaylist))
	mux.Handle("PATCH /api/playlists/{id}", httputil.Handler(h.updatePlaylist))
	mux.Handle("DELETE /api/playlists/{id}", httputil.Handler(h.deletePlaylist))
	mux.Handle("POST /api/playlists/{id}/{action}", httputil.Handler(h.playlistAction))
	mux.Handle("POST /api/playlists/{id}/batch/add", httputil.Handler(func(w http.ResponseWriter, r *http.Request) error {
		return h.addPlaylistItem(w, r, r.PathValue("id"), store.RequestUser(r), true)
	}))
	mux.Handle("POST /api/playlists/{id}/batch/remove", httputil.Handler(h.removePlaylistItems))
	mux.Handle("DELETE /api/playlists/{id}/item/{itemPath...}", httputil.Handler(h.removePlaylistItem))
	mux.Handle("GET /api/libraries/{libraryID}/collections", readOnly(h.libraryCollections))
	mux.Handle("GET /api/libraries/{libraryID}/playlists", readOnly(h.libraryPlaylists))
}

func (h *Handler) playlistAction(w http.ResponseWriter, r *http.Request) error {
	id, action, user := r.PathValue("id"), r.PathValue("action"), store.RequestUser(r)
	switch {
	case id == "collection":
		return h.createPlaylistFromCollection(w, r, user, action)
	case action == "item":
		return h.addPlaylistItem(w, r, id, user, false)
	}
	return httputil.Status(http.StatusNotFound, "")
}
