// Package server composes the Audiobookshelf runtime and its restricted
// read-only development HTTP surface.
package server

import (
	"audiobookshelf-compat/internal/access"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"audiobookshelf-compat/internal/auth"
	"audiobookshelf-compat/internal/config"
	"audiobookshelf-compat/internal/httputil"
	"audiobookshelf-compat/internal/logging"
	"audiobookshelf-compat/internal/store"
)

const Version = "2.37.2"

type Repository interface {
	UserByIDOrOldID(context.Context, string) (store.User, error)
	ServerSettings(context.Context) (map[string]any, error)
	Libraries(context.Context) ([]map[string]any, error)
	DB() *sql.DB
}

type Handler struct {
	Config   config.Config
	Store    Repository
	Settings map[string]any
	Secret   []byte
}

// NewHandler loads settings and verifies readiness from the already-open
// read-only database. An absent/invalid database is a startup error.
func NewHandler(ctx context.Context, cfg config.Config, repo Repository) (*Handler, error) {
	settings, err := repo.ServerSettings(ctx)
	if err != nil {
		return nil, fmt.Errorf("load server settings: %w", err)
	}
	secret := []byte(cfg.JWTSecret)
	if len(secret) == 0 {
		if s, ok := settings["tokenSecret"].(string); ok {
			secret = []byte(s)
		}
	}
	if len(secret) == 0 {
		return nil, errors.New("server settings do not contain a JWT secret; read-only mode cannot initialize one")
	}
	return &Handler{Config: cfg, Store: repo, Settings: settings, Secret: secret}, nil
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	guardPanics(w, r, h.serveHTTP)
}

func (h *Handler) serveHTTP(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, h.Config.RouterBasePath)
	switch {
	case isGet(r) && path == "/ping":
		httputil.WriteJSON(w, http.StatusOK, map[string]any{"success": true}, httputil.ForRequest(r))
	case isGet(r) && path == "/healthcheck":
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.Header().Set("Content-Length", "2")
		w.WriteHeader(http.StatusOK)
		if r.Method != http.MethodHead {
			_, _ = w.Write([]byte("OK"))
		}
	case isGet(r) && path == "/status":
		h.status(w, r)
	case strings.HasPrefix(path, "/api"):
		h.api(w, r, path)
	default:
		h.unsupported(w)
	}
}

func (h *Handler) status(w http.ResponseWriter, r *http.Request) {
	var rootCount int
	if err := h.Store.DB().QueryRowContext(r.Context(), `SELECT COUNT(*) FROM users WHERE type = 'root'`).Scan(&rootCount); err != nil {
		http.Error(w, "database not ready", http.StatusServiceUnavailable)
		return
	}
	methods, ok := h.Settings["authActiveAuthMethods"].([]any)
	if !ok {
		if names, found := h.Settings["authActiveAuthMethods"].([]string); found {
			methods = make([]any, len(names))
			for i, name := range names {
				methods[i] = name
			}
		} else {
			methods = []any{"local"}
		}
	}
	language := h.Settings["language"]
	if language == nil || language == "" {
		language = "en-us"
	}
	customMessage := h.Settings["authLoginCustomMessage"]
	if message, ok := customMessage.(string); ok && message != "" {
		// Upstream sanitizes this HTML before returning it. Do not expose the
		// raw database value until an equivalent allowlist sanitizer exists.
		h.unsupported(w)
		return
	}
	customMessage = ""
	authFormData := map[string]any{"authLoginCustomMessage": customMessage}
	for _, method := range methods {
		if method == "openid" {
			button, exists := h.Settings["authOpenIDButtonText"]
			if !exists || button == "" {
				button = "Login with OpenId"
			}
			autoLaunch, _ := h.Settings["authOpenIDAutoLaunch"].(bool)
			authFormData["authOpenIDButtonText"] = button
			authFormData["authOpenIDAutoLaunch"] = autoLaunch
			break
		}
	}
	payload := map[string]any{
		"app": "audiobookshelf", "serverVersion": Version, "isInit": rootCount > 0,
		"language": language, "authMethods": methods,
		"authFormData": authFormData,
	}
	if rootCount == 0 {
		payload["ConfigPath"] = h.Config.ConfigPath
		payload["MetadataPath"] = h.Config.MetadataPath
	}
	httputil.WriteJSON(w, http.StatusOK, payload, httputil.ForRequest(r))
}

func (h *Handler) api(w http.ResponseWriter, r *http.Request, path string) {
	claims, user, err := h.authenticate(r)
	if err != nil {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.Header().Set("Content-Length", "12")
		w.WriteHeader(http.StatusUnauthorized)
		if r.Method != http.MethodHead {
			_, _ = w.Write([]byte("Unauthorized"))
		}
		return
	}
	_ = claims
	if isGet(r) && path == "/api/libraries" {
		if strings.Contains(","+r.URL.Query().Get("include")+",", ",stats,") {
			h.unsupported(w)
			return
		}
		libraries, err := h.Store.Libraries(r.Context())
		if err != nil {
			logging.RequestError(r, "database read failed", err)
			http.Error(w, "database read failed", http.StatusInternalServerError)
			return
		}
		// LibraryController.findAll filters on a non-empty librariesAccessible
		// array and does not special-case accessAllLibraries.
		perms := access.Of(user)
		filtered := make([]map[string]any, 0, len(libraries))
		for _, lib := range libraries {
			if id, ok := lib["id"].(string); ok && perms.ListLibrary(id) {
				filtered = append(filtered, lib)
			}
		}
		libraries = filtered
		httputil.WriteJSON(w, http.StatusOK, map[string]any{"libraries": libraries}, httputil.ForRequest(r))
		return
	}
	// /authorize requires media-progress, browser-user, server-settings and
	// device serializers. Login and token issuance are also intentionally absent.
	if r.Method == http.MethodPost && path == "/api/authorize" {
		h.unsupported(w)
		return
	}
	h.unsupported(w)
}

func (h *Handler) authenticate(r *http.Request) (auth.Claims, store.User, error) {
	var empty auth.Claims
	if len(h.Secret) == 0 {
		return empty, store.User{}, errors.New("JWT secret is unavailable")
	}
	token := ""
	if hdr := r.Header.Get("Authorization"); strings.HasPrefix(strings.ToLower(hdr), "bearer ") {
		token = strings.TrimSpace(hdr[len("Bearer "):])
	}
	if token == "" {
		token = r.URL.Query().Get("token")
	}
	claims, err := auth.ValidateBearer(token, h.Secret, time.Now())
	if err != nil {
		return empty, store.User{}, err
	}
	// Upstream resolves type=api through the apiKeys table. This read-only slice
	// has no complete API-key adapter, so do not treat it as a user token.
	if claims.Type == "api" || claims.Type == "apiKey" || claims.UserID == "" {
		return empty, store.User{}, errors.New("unsupported token type")
	}
	user, err := h.Store.UserByIDOrOldID(r.Context(), claims.UserID)
	if err != nil || !user.IsActive {
		return empty, store.User{}, errors.New("inactive or missing user")
	}
	return claims, user, nil
}

func (h *Handler) unsupported(w http.ResponseWriter) {
	httputil.WriteJSON(w, http.StatusNotImplemented, map[string]string{"error": "This route is not implemented in the experimental Go backend"})
}

func isGet(r *http.Request) bool { return r.Method == http.MethodGet || r.Method == http.MethodHead }
