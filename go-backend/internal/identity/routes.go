package identity

import (
	"net/http"

	"audiobookshelf-compat/internal/httputil"
)

// RegisterRoutes mounts the public auth routes (Server.js:334+ mounts them
// before the /api auth middleware; the routes perform their own session and
// bearer checks). Method mismatches return 404; unknown auth paths stay with
// the legacy dispatch (base stripping is the runtime's job now, not the
// route's).
func (h *Handler) RegisterRoutes(mux *http.ServeMux) {
	notFound := func(w http.ResponseWriter, r *http.Request) {
		httputil.Fail(w, r, httputil.Status(http.StatusNotFound, ""))
	}
	post := func(pattern string, handler httputil.Handler) {
		mux.HandleFunc(pattern, func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodPost {
				notFound(w, r)
				return
			}
			handler.ServeHTTP(w, r)
		})
	}
	get := func(pattern string, handler httputil.Handler) {
		mux.HandleFunc(pattern, func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodGet && r.Method != http.MethodHead {
				notFound(w, r)
				return
			}
			handler.ServeHTTP(w, r)
		})
	}
	post("/api/authorize", h.authorize)
	post("/login", h.login)
	post("/auth/refresh", h.refresh)
	post("/logout", h.logout)
	post("/init", h.initialize)
	get("/auth/openid", h.oidcAuthorize)
	get("/auth/openid/mobile-redirect", h.oidcMobileRedirect)
	get("/auth/openid/callback", h.oidcCallback)
	get("/auth/openid/config", h.oidcConfig)
}
