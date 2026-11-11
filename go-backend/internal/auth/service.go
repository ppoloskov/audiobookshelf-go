package auth

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"audiobookshelf-compat/internal/config"
	"audiobookshelf-compat/internal/db"
	"audiobookshelf-compat/internal/events"
	"audiobookshelf-compat/internal/httpx"
	"audiobookshelf-compat/internal/settings"
)

// Service owns authentication: tokens, passwords, refresh-token sessions, API
// keys and the OpenID Connect flows. Build it with New and mount its public
// routes with Routes.
type Service struct {
	db       *db.DB
	settings *settings.Store
	emit     events.Emitter
	cfg      config.Config
	secret   []byte
	limiter  *limiter
	client   *http.Client // for the identity provider
	web      *flows[webFlow]
	mobile   *flows[string]
}

// New builds the service. The signing secret is JWT_SECRET_KEY (cfg.JWTSecret),
// else the tokenSecret of the server settings; when neither exists a new
// secret is generated and stored (upstream does the same). Changing the
// secret invalidates every token, session and API key. st may be nil only
// when cfg.JWTSecret is set and the login routes are not used.
func New(d *db.DB, st *settings.Store, emit events.Emitter, cfg config.Config) (*Service, error) {
	if cfg.Auth == (config.Auth{}) {
		cfg.Auth = config.DefaultAuth()
	}
	s := &Service{
		db: d, settings: st, emit: emit, cfg: cfg, secret: []byte(cfg.JWTSecret),
		limiter: newLimiter(cfg.Auth.RateMax, cfg.Auth.RateWindow),
		client:  &http.Client{Timeout: 10 * time.Second},
		web:     newFlows[webFlow](24 * time.Hour),
		mobile:  newFlows[string](10 * time.Minute),
	}
	if len(s.secret) > 0 {
		return s, nil
	}
	_, err := st.UpdateServer(context.Background(), func(server *settings.Server) error {
		if server.TokenSecret == "" {
			raw := make([]byte, 256)
			if _, err := rand.Read(raw); err != nil {
				return fmt.Errorf("generate token secret: %w", err)
			}
			server.TokenSecret = settings.Secret(base64.StdEncoding.EncodeToString(raw))
		}
		s.secret = []byte(server.TokenSecret)
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("auth: token secret: %w", err)
	}
	return s, nil
}

// Routes mounts the public authentication routes. They authenticate
// themselves where they need to (the runtime must not wrap them in
// Middleware). The login, refresh and OpenID routes are rate limited per IP.
func (s *Service) Routes(mux *http.ServeMux) {
	limited := func(h httpx.Handler) http.Handler { return s.limiter.wrap(h, s.cfg.Auth.RateMessage) }
	mux.Handle("POST /login", limited(s.login))
	mux.Handle("POST /auth/refresh", limited(s.refresh))
	mux.Handle("POST /logout", httpx.Handler(s.logout))
	mux.Handle("POST /init", httpx.Handler(s.initialize))
	mux.Handle("POST /api/authorize", httpx.Handler(s.authorize))
	mux.Handle("GET /auth/openid", limited(s.oidcStart))
	mux.Handle("GET /auth/openid/mobile-redirect", limited(s.oidcMobileRedirect))
	mux.Handle("GET /auth/openid/callback", limited(s.oidcCallback))
	mux.Handle("GET /auth/openid/config", limited(s.oidcDiscovery))
}

// methodEnabled reports whether method ("local" or "openid") is an active
// login method.
func (s *Service) methodEnabled(method string) bool {
	for _, active := range s.settings.Server().ActiveAuthMethods {
		if active == method {
			return true
		}
	}
	return false
}

// requestSecure reports an HTTPS request, directly or behind a proxy.
func requestSecure(r *http.Request) bool {
	if r.TLS != nil {
		return true
	}
	for _, proto := range strings.Split(strings.ToLower(r.Header.Get("X-Forwarded-Proto")), ",") {
		if strings.TrimSpace(proto) == "https" {
			return true
		}
	}
	return false
}

// requestOrigin is scheme://host of the request as the browser addressed it.
func requestOrigin(r *http.Request) string {
	scheme := "http"
	if requestSecure(r) {
		scheme = "https"
	}
	return scheme + "://" + r.Host
}

// setRefreshCookie stores the refresh token for the web client.
func (s *Service) setRefreshCookie(w http.ResponseWriter, r *http.Request, token string) {
	ttl := s.cfg.Auth.RefreshTTL
	http.SetCookie(w, &http.Cookie{
		Name: "refresh_token", Value: token, Path: "/", MaxAge: int(ttl.Seconds()), Expires: time.Now().Add(ttl),
		HttpOnly: true, Secure: requestSecure(r), SameSite: http.SameSiteLaxMode,
	})
}

// SetRefreshTokenCookie is setRefreshCookie for callers outside the package.
func (s *Service) SetRefreshTokenCookie(w http.ResponseWriter, r *http.Request, token string) {
	s.setRefreshCookie(w, r, token)
}

// setCookie sets a short-lived flow cookie. The value is query-escaped so any
// text survives; cookieValue reverses it.
func setCookie(w http.ResponseWriter, name, value string, maxAge time.Duration) {
	http.SetCookie(w, &http.Cookie{Name: name, Value: url.QueryEscape(value), Path: "/", MaxAge: int(maxAge.Seconds()), HttpOnly: true})
}

// clearCookies expires the named cookies.
func clearCookies(w http.ResponseWriter, names ...string) {
	for _, name := range names {
		http.SetCookie(w, &http.Cookie{Name: name, Path: "/", MaxAge: -1, Expires: time.Unix(0, 0)})
	}
}

// cookieValue is the unescaped value of a cookie, or "" when absent.
func cookieValue(r *http.Request, name string) string {
	cookie, err := r.Cookie(name)
	if err != nil {
		return ""
	}
	if value, err := url.QueryUnescape(cookie.Value); err == nil {
		return value
	}
	return cookie.Value
}

// randomToken is n random bytes in URL-safe base64.
func randomToken(n int) (string, error) {
	raw := make([]byte, n)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("random token: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}

var errUnauthorized = httpx.Error(http.StatusUnauthorized, "Unauthorized")

// jsonError answers status with {"error": message}, the body the web client
// reads from refresh and rate-limit failures.
func jsonError(w http.ResponseWriter, r *http.Request, status int, message string) error {
	return httpx.JSONStatus(w, r, status, struct {
		Error string `json:"error"`
	}{message})
}

var errNoUser = errors.New("auth: inactive or missing user")
