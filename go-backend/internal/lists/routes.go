package lists

import "net/http"

func register(mux *http.ServeMux, method, pattern string, action http.HandlerFunc) {
	mux.HandleFunc(method+" "+pattern, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodHead {
			notImplemented(w)
			return
		}
		action(w, r)
	})
}

// Routes mounts the list API on the runtime's authenticated mux.
func (h *Handler) Routes(mux *http.ServeMux) {
	register(mux, "GET", "/api/collections", h.collectionRoot)
	register(mux, "POST", "/api/collections", h.createCollection)
	register(mux, "GET", "/api/collections/{id}", h.collectionID)
	register(mux, "PATCH", "/api/collections/{id}", h.collectionID)
	register(mux, "DELETE", "/api/collections/{id}", h.collectionID)
	register(mux, "POST", "/api/collections/{id}/book", h.addCollectionBook)
	register(mux, "DELETE", "/api/collections/{id}/book/{itemID}", h.removeCollectionBook)
	register(mux, "POST", "/api/collections/{id}/batch/{action}", h.batchCollectionBooks)
	register(mux, "GET", "/api/playlists", h.listPlaylists)
	register(mux, "POST", "/api/playlists", h.createPlaylist)
	register(mux, "GET", "/api/playlists/{id}", h.playlistID)
	register(mux, "PATCH", "/api/playlists/{id}", h.playlistID)
	register(mux, "DELETE", "/api/playlists/{id}", h.playlistID)
	register(mux, "POST", "/api/playlists/{id}/{action}", h.playlistAction)
	register(mux, "POST", "/api/playlists/{id}/batch/add", func(w http.ResponseWriter, r *http.Request) {
		h.addPlaylistItem(w, r, r.PathValue("id"), requestUser(r), true)
	})
	register(mux, "POST", "/api/playlists/{id}/batch/remove", func(w http.ResponseWriter, r *http.Request) {
		h.removePlaylistItems(w, r, r.PathValue("id"), requestUser(r))
	})
	register(mux, "DELETE", "/api/playlists/{id}/item/{itemPath...}", h.removePlaylistItem)
	register(mux, "GET", "/api/libraries/{libraryID}/collections", func(w http.ResponseWriter, r *http.Request) {
		h.libraryCollections(w, r, requestUser(r), r.PathValue("libraryID"))
	})
	register(mux, "GET", "/api/libraries/{libraryID}/playlists", func(w http.ResponseWriter, r *http.Request) {
		h.libraryPlaylists(w, r, requestUser(r), r.PathValue("libraryID"))
	})
}

func (h *Handler) playlistAction(w http.ResponseWriter, r *http.Request) {
	id, action, user := r.PathValue("id"), r.PathValue("action"), requestUser(r)
	if id == "collection" {
		h.createPlaylistFromCollection(w, r, user, action)
	} else if action == "item" {
		h.addPlaylistItem(w, r, id, user, false)
	} else {
		notImplemented(w)
	}
}
