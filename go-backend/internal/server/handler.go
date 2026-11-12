package server

import (
	"io"
	"log/slog"
	"net/http"
	"slices"

	"audiobookshelf-compat/internal/httpx"
	"audiobookshelf-compat/internal/text"
	"audiobookshelf-compat/internal/version"
)

// newHandler composes the request pipeline once:
//
//	recover -> security headers -> CORS -> base path -> trailing slash -> route
//
// Routes are in two groups. The public mux answers without a user: login and
// the other authentication flows, /ping, /healthcheck, /status, Socket.IO, the
// two public reads (item cover, author image), the capability URLs that
// domains register outside /api (feeds, shares, HLS, session tracks) and the
// web client. Everything else under /api runs behind auth.Middleware; an
// unknown /api route is a 404, which clients fall back on.
// allowAllCORS is upstream's ALLOW_CORS=1 switch (global.AllowCors).
func (rt *Runtime) newHandler(routes *http.ServeMux, allowAllCORS bool) http.Handler {
	public := http.NewServeMux()
	rt.auth.Routes(public)
	public.Handle("GET /ping", httpx.Handler(ping))
	public.Handle("GET /healthcheck", http.HandlerFunc(healthcheck))
	public.Handle("GET /status", httpx.Handler(rt.status))
	for _, pattern := range []string{"/socket.io", "/socket.io/"} {
		public.Handle(pattern, rt.hub)
	}
	api := rt.auth.Middleware(matching(routes, httpx.Handler(func(w http.ResponseWriter, _ *http.Request) error {
		return httpx.Status(w, http.StatusNotFound)
	})))
	for _, pattern := range []string{"/api", "/api/"} {
		public.Handle(pattern, api)
	}
	// The upstream leaves item covers and author images readable without a token.
	public.Handle("GET /api/items/{id}/cover", routes)
	public.Handle("GET /api/authors/{id}/image", routes)
	public.Handle("/", matching(routes, clientHandler(rt.cfg.ClientPath)))

	return chain(public, rt.middlewares(allowAllCORS)...)
}

// middlewares is the pipeline, outermost first. Recover is outside everything
// so that a panic anywhere still carries the security headers.
func (rt *Runtime) middlewares(allowAllCORS bool) []middleware {
	return []middleware{
		recoverPanics,
		securityHeaders(rt.settings),
		corsPolicy(allowAllCORS, rt.settings),
		basePath(rt.cfg.RouterBasePath),
		cleanPath, // Express matched a route with one trailing slash
	}
}

// matching serves r from routes when one of its patterns matches and from
// otherwise when none does.
func matching(routes *http.ServeMux, otherwise http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, pattern := routes.Handler(r); pattern != "" {
			routes.ServeHTTP(w, r)
			return
		}
		otherwise.ServeHTTP(w, r)
	})
}

type pingResponse struct {
	Success bool `json:"success"`
}

func ping(w http.ResponseWriter, r *http.Request) error {
	return httpx.JSON(w, r, pingResponse{Success: true})
}

func healthcheck(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = io.WriteString(w, "OK") // a client that went away is not a server error
}

// statusResponse is the answer of GET /status: what a client needs before it
// logs in. The config paths are only given while the server is not set up.
type statusResponse struct {
	App           string       `json:"app"`
	ServerVersion string       `json:"serverVersion"`
	IsInit        bool         `json:"isInit"`
	Language      string       `json:"language"`
	AuthMethods   []string     `json:"authMethods"`
	AuthFormData  authFormData `json:"authFormData"`
	ConfigPath    *string      `json:"ConfigPath,omitempty"`
	MetadataPath  *string      `json:"MetadataPath,omitempty"`
}

type authFormData struct {
	LoginCustomMessage string  `json:"authLoginCustomMessage"`
	OpenIDButtonText   *string `json:"authOpenIDButtonText,omitempty"`
	OpenIDAutoLaunch   *bool   `json:"authOpenIDAutoLaunch,omitempty"`
}

func (rt *Runtime) status(w http.ResponseWriter, r *http.Request) error {
	var roots int
	if err := rt.data.Get(r.Context(), &roots, "SELECT COUNT(*) FROM users WHERE type = 'root'"); err != nil {
		slog.Error("status: count root users", "error", err)
		return httpx.Error(http.StatusServiceUnavailable, "database not ready")
	}
	server := rt.settings.Server()
	response := statusResponse{
		App:           "audiobookshelf",
		ServerVersion: version.Server,
		IsInit:        roots > 0,
		Language:      server.Language,
		AuthMethods:   server.ActiveAuthMethods,
		AuthFormData:  authFormData{LoginCustomMessage: text.Sanitize(string(server.LoginCustomMessage))},
	}
	if response.Language == "" {
		response.Language = "en-us"
	}
	if len(response.AuthMethods) == 0 {
		response.AuthMethods = []string{"local"}
	}
	if slices.Contains(response.AuthMethods, "openid") {
		text := server.OpenIDButtonText
		if text == "" {
			text = "Login with OpenId"
		}
		response.AuthFormData.OpenIDButtonText = &text
		response.AuthFormData.OpenIDAutoLaunch = &server.OpenIDAutoLaunch
	}
	if roots == 0 {
		response.ConfigPath, response.MetadataPath = &rt.cfg.ConfigPath, &rt.cfg.MetadataPath
	}
	return httpx.JSON(w, r, response)
}
