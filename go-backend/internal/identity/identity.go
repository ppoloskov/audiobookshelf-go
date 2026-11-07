// Package identity implements local authentication and refresh-token sessions
// for the pinned Audiobookshelf schema. It validates existing schema only; it
// never creates tables or runs migrations.
package identity

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"math"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	schemaassets "audiobookshelf-compat/contracts/parity"
	"audiobookshelf-compat/internal/auth"
	"audiobookshelf-compat/internal/config"
	"audiobookshelf-compat/internal/db/sqlc"
	"audiobookshelf-compat/internal/dbtime"
	"audiobookshelf-compat/internal/httputil"
	"audiobookshelf-compat/internal/ids"
	"audiobookshelf-compat/internal/logging"
	"audiobookshelf-compat/internal/progressdto"
	"audiobookshelf-compat/internal/store"
	"audiobookshelf-compat/internal/textclean"
	"golang.org/x/crypto/bcrypt"
	"golang.org/x/time/rate"
)

const (
	accessTokenTTL  = time.Hour
	refreshTokenTTL = 30 * 24 * time.Hour
	refreshGraceTTL = 10 * time.Minute
)

type Handler struct {
	db               *sql.DB
	cfg              config.Config
	secret           []byte
	accessTTL        time.Duration
	refreshTTL       time.Duration
	refreshGrace     time.Duration
	userAddedEmitter func(map[string]any)
	rateMax          int
	rateWindow       time.Duration
	rateMessage      string
	rateMu           sync.Mutex
	rateLimiters     map[string]*rate.Limiter
	rateSwept        time.Time
	oidcMu           sync.Mutex
	oidcFlows        map[string]oidcFlow
	mobileFlows      map[string]mobileFlow
}

// SetUserAddedEmitter installs the realtime event used by OpenID auto-registration.
// Runtime composition supplies the shared Socket.IO hub before serving requests.
func (h *Handler) SetUserAddedEmitter(emit func(map[string]any)) {
	h.userAddedEmitter = emit
}

type session struct {
	id                      string
	userID                  string
	refreshToken            string
	expiresAt               time.Time
	lastRefreshToken        sql.NullString
	lastRefreshTokenExpires nullableDate
}

type nullableDate struct {
	Time  time.Time
	Valid bool
}

func (d *nullableDate) Scan(value any) error {
	if value == nil {
		d.Time, d.Valid = time.Time{}, false
		return nil
	}
	var text string
	switch v := value.(type) {
	case time.Time:
		d.Time, d.Valid = v, true
		return nil
	case string:
		text = v
	case []byte:
		text = string(v)
	default:
		return fmt.Errorf("identity: unsupported timestamp value %T", value)
	}
	if parsed, ok := dbtime.Parse(text); ok {
		d.Time, d.Valid = parsed, true
		return nil
	}
	return fmt.Errorf("identity: invalid timestamp %q", text)
}

// New validates the baseline auth schema and obtains the signing key. If no
// JWT_SECRET_KEY is configured and settings.tokenSecret is absent, it stores a
// generated secret in the existing server-settings row (upstream behavior).
func New(db *sql.DB, cfg config.Config) (*Handler, error) {
	if db == nil {
		return nil, errors.New("identity: database is required")
	}
	for table, columns := range map[string][]string{
		"users":     {"id", "username", "email", "pash", "type", "token", "isActive", "isLocked", "lastSeen", "permissions", "bookmarks", "extraData", "createdAt", "updatedAt"},
		"settings":  {"key", "value"},
		"libraries": {"id", "displayOrder"},
		"sessions":  {"id", "ipAddress", "userAgent", "refreshToken", "expiresAt", "createdAt", "updatedAt", "userId", "lastRefreshToken", "lastRefreshTokenExpiresAt"},
		"apiKeys":   {"id", "isActive", "expiresAt", "lastUsedAt", "userId", "updatedAt"},
	} {
		if err := requireColumns(db, table, columns); err != nil {
			return nil, err
		}
	}
	h := &Handler{
		db: db, cfg: cfg, secret: []byte(cfg.JWTSecret),
		accessTTL:    durationFromEnv("ACCESS_TOKEN_EXPIRY", accessTokenTTL),
		refreshTTL:   durationFromEnv("REFRESH_TOKEN_EXPIRY", refreshTokenTTL),
		refreshGrace: durationFromEnv("REFRESH_TOKEN_GRACE_PERIOD", refreshGraceTTL),
		rateMax:      intFromEnv("RATE_LIMIT_AUTH_MAX", 40, true),
		rateWindow:   time.Duration(intFromEnv("RATE_LIMIT_AUTH_WINDOW", 10*60*1000, false)) * time.Millisecond,
		rateMessage:  envString("RATE_LIMIT_AUTH_MESSAGE", "Too many authentication requests"),
		rateLimiters: make(map[string]*rate.Limiter),
		oidcFlows:    make(map[string]oidcFlow),
		mobileFlows:  make(map[string]mobileFlow),
	}
	if len(h.secret) == 0 {
		settings, err := h.loadSettings(context.Background(), "server-settings")
		if err != nil {
			return nil, fmt.Errorf("identity: load server settings: %w", err)
		}
		if s, ok := settings["tokenSecret"].(string); ok {
			h.secret = []byte(s)
		}
		if len(h.secret) == 0 {
			secret := make([]byte, 256)
			if _, err := rand.Read(secret); err != nil {
				return nil, fmt.Errorf("identity: generate token secret: %w", err)
			}
			h.secret = []byte(base64.StdEncoding.EncodeToString(secret))
			settings["tokenSecret"] = string(h.secret)
			encoded, err := json.Marshal(settings)
			if err != nil {
				return nil, fmt.Errorf("identity: encode server settings: %w", err)
			}
			result, err := db.Exec(`UPDATE settings SET value = ? WHERE key = 'server-settings'`, string(encoded))
			if err != nil {
				return nil, fmt.Errorf("identity: persist token secret: %w", err)
			}
			if n, _ := result.RowsAffected(); n != 1 {
				return nil, errors.New("identity: server-settings row disappeared while storing token secret")
			}
		}
	}
	if len(h.secret) == 0 {
		return nil, errors.New("identity: token secret is empty")
	}
	return h, nil
}

// TokenSecret returns the key that signs and verifies this server's JWTs.
func (h *Handler) TokenSecret() []byte { return h.secret }

// Handle consumes the local identity endpoints and /api/authorize. Paths may
// be passed with or without the configured router prefix.
func (h *Handler) Handle(w http.ResponseWriter, r *http.Request) bool {
	path := r.URL.Path
	if base := h.cfg.RouterBasePath; base != "" && strings.HasPrefix(path, base+"/") {
		path = strings.TrimPrefix(path, base)
	}
	var run httputil.Handler
	switch r.Method + " " + path {
	case "POST /login":
		run = h.login
	case "POST /auth/refresh":
		run = h.refresh
	case "POST /logout":
		run = h.logout
	case "POST /init":
		run = h.initialize
	case "GET /auth/openid":
		run = h.oidcAuthorize
	case "GET /auth/openid/mobile-redirect":
		run = h.oidcMobileRedirect
	case "GET /auth/openid/callback":
		run = h.oidcCallback
	case "GET /auth/openid/config":
		run = h.oidcConfig
	case "POST /api/authorize":
		run = h.authorize
	default:
		return false
	}
	run.ServeHTTP(w, r)
	return true
}

// Authenticate validates a bearer or query-parameter token, then resolves an
// active user. API keys are checked against the apiKeys row and its expiration.
func (h *Handler) Authenticate(r *http.Request) (store.User, error) {
	token := requestToken(r)
	claims, err := auth.ValidateTokenIgnoringExpiration(token, h.secret, time.Now())
	if err != nil {
		return store.User{}, err
	}
	if claims.Type == "api" || claims.Type == "apiKey" {
		var active int64
		var expires nullableDate
		var userID sql.NullString
		err := h.db.QueryRowContext(r.Context(), `SELECT isActive, expiresAt, userId FROM apiKeys WHERE id = ? LIMIT 1`, claims.KeyID).Scan(&active, &expires, &userID)
		if err != nil || active == 0 || !userID.Valid || userID.String == "" {
			return store.User{}, errors.New("identity: invalid API key")
		}
		if expires.Valid && !expires.Time.After(time.Now()) {
			// Upstream ApiKey.js:139-155 deactivates expired keys in a separate
			// batch operation; per-request auth never writes to the row.
			return store.User{}, errors.New("identity: expired API key")
		}
		user, err := h.userByID(r.Context(), userID.String)
		if err != nil || !user.IsActive {
			return store.User{}, errors.New("identity: inactive or missing API key user")
		}
		return user, nil
	}
	if claims.ExpiresAt != nil && time.Now().After(*claims.ExpiresAt) {
		return store.User{}, auth.ErrExpired
	}
	if claims.UserID == "" {
		return store.User{}, errors.New("identity: token has no user id")
	}
	user, err := h.userByIDOrOldID(r.Context(), claims.UserID)
	if err != nil || !user.IsActive {
		return store.User{}, errors.New("identity: inactive or missing user")
	}
	return user, nil
}

// authorize answers the upstream /api/authorize route.
func (h *Handler) authorize(w http.ResponseWriter, r *http.Request) error {
	user, err := h.Authenticate(r)
	if err != nil {
		return writePassportDenied(w)
	}
	payload, err := h.loginPayload(r.Context(), user)
	if err != nil {
		logging.RequestError(r, "error", err)
		return writeExpressJSON(w, http.StatusInternalServerError, map[string]string{"error": "Unable to load authorization data"})
	}
	return writeExpressJSON(w, http.StatusOK, payload)
}

// UserBrowser serializes a database user into the legacy browser DTO used by
// upstream. Media progress must be overlaid by the caller when available.
func UserBrowser(user store.User, hideRootToken, minimal bool) map[string]any {
	permissions := decodeMap(user.Permissions)
	libraries := stringSlice(permissions["librariesAccessible"])
	tags := stringSlice(permissions["itemTagsSelected"])
	if libraries == nil {
		libraries = []string{}
	}
	if tags == nil {
		tags = []string{}
	}
	delete(permissions, "librariesAccessible")
	delete(permissions, "itemTagsSelected")
	extra := decodeMap(user.ExtraData)
	series := stringSlice(extra["seriesHideFromContinueListening"])
	bookmarks := decodeArray(user.Bookmarks)
	if bookmarks == nil {
		bookmarks = []any{}
	}
	if user.Type == "root" && hideRootToken {
		user.Token = ""
	}
	var lastSeen any
	if user.LastSeen != nil {
		lastSeen = *user.LastSeen
	}
	created := any(nil)
	if user.CreatedAt != nil {
		created = *user.CreatedAt
	}
	result := map[string]any{
		"id": user.ID, "username": user.Username, "email": store.NullIfEmpty(user.Email), "type": user.Type,
		"token": user.Token, "mediaProgress": []any{}, "seriesHideFromContinueListening": series,
		"bookmarks": bookmarks, "isActive": user.IsActive, "isLocked": user.IsLocked,
		"lastSeen": lastSeen, "createdAt": created, "permissions": permissions,
		"librariesAccessible": libraries, "itemTagsSelected": tags,
		"hasOpenIDLink": extra["authOpenIDSub"] != nil,
	}
	if !minimal {
		result["mediaProgress"] = []any{}
	} else {
		delete(result, "mediaProgress")
		delete(result, "bookmarks")
	}
	return result
}

func (h *Handler) login(w http.ResponseWriter, r *http.Request) error {
	if !h.authMethodEnabled(r.Context(), "local") {
		return writePassportDenied(w)
	}
	if !h.allowAuthRequest(w, r) {
		return nil
	}
	var body struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if decodeBody(r, &body) != nil || body.Username == "" {
		return writePassportDenied(w)
	}
	user, err := h.userByUsername(r.Context(), strings.ToLower(body.Username))
	if err != nil || !user.IsActive {
		if err == nil {
			h.logFailedLogin(r, user.Username, "User is not active")
		} else {
			h.logFailedLogin(r, body.Username, "User not found")
		}
		return writePassportDenied(w)
	}
	if user.Type == "root" && user.Password == "" {
		if body.Password != "" {
			h.logFailedLogin(r, user.Username, "Root user has no password set")
			return writePassportDenied(w)
		}
	} else if user.Password == "" {
		h.logFailedLogin(r, user.Username, "User has no password set. Might have been created with OpenID")
		return writePassportDenied(w)
	} else if bcrypt.CompareHashAndPassword([]byte(user.Password), []byte(body.Password)) != nil {
		h.logFailedLogin(r, user.Username, "Invalid password")
		return writePassportDenied(w)
	}
	logging.Infof("[LocalAuth] User %q logged in from ip %s", user.Username, clientIP(r)) // LocalAuthStrategy.js:74,88
	access, refresh, err := h.createSession(r.Context(), user, r)
	if err != nil {
		logging.RequestError(r, "error", err)
		return httputil.JSONStatus(w, r, http.StatusInternalServerError, map[string]string{"error": "Unable to create session"})
	}
	payload, err := h.loginPayload(r.Context(), user)
	if err != nil {
		logging.RequestError(r, "error", err)
		return httputil.JSONStatus(w, r, http.StatusInternalServerError, map[string]string{"error": "Unable to load login data"})
	}
	responseUser := payload["user"].(map[string]any)
	responseUser["refreshToken"] = nil
	if r.Header.Get("x-return-tokens") == "true" {
		responseUser["refreshToken"] = refresh
	} else {
		setRefreshCookie(w, r, refresh, int(h.refreshTTL.Seconds()))
	}
	responseUser["accessToken"] = access
	// Upstream passport logIn regenerates the express-session, so a successful
	// login responds with a fresh signed connect.sid (Path=/, HttpOnly) in
	// addition to the refresh-token cookie.
	if sessionCookie, cookieErr := h.freshSessionCookieValue(); cookieErr == nil {
		setExpressCookieValue(w, "connect.sid", sessionCookie, 0, true, false, "")
	}
	return writeExpressJSON(w, http.StatusOK, payload)
}

// logFailedLogin is LocalAuthStrategy.logFailedLoginAttempt (LocalAuthStrategy.js:104).
func (h *Handler) logFailedLogin(r *http.Request, username, message string) {
	logging.Errorf("[LocalAuth] Failed login attempt for username %q from ip %s (%s)", username, clientIP(r), message)
}

func (h *Handler) refresh(w http.ResponseWriter, r *http.Request) error {
	if !h.allowAuthRequest(w, r) {
		return nil
	}
	refreshToken := ""
	returnToken := false
	if header := r.Header.Get("x-refresh-token"); header != "" {
		refreshToken, returnToken = header, true
	} else if cookie, err := r.Cookie("refresh_token"); err == nil {
		refreshToken = cookie.Value
	}
	if refreshToken == "" {
		logging.Error("[Auth] Failed to refresh token. No refresh token provided") // Auth.js:343
		return writeExpressJSON(w, http.StatusUnauthorized, map[string]string{"error": "No refresh token provided"})
	}
	claims, err := auth.ValidateToken(refreshToken, h.secret, time.Now())
	if err != nil {
		if errors.Is(err, auth.ErrExpired) {
			logging.Info("[TokenManager] Refresh token expired, cleaning up session") // TokenManager.js:429
			_ = h.queries().DeleteSessionByRefreshToken(r.Context(), refreshToken)
			writeExpressJSON(w, http.StatusUnauthorized, map[string]string{"error": "Refresh token expired"})
		} else {
			logging.Errorf("[TokenManager] Invalid refresh token format: %v", err) // TokenManager.js:445
			writeExpressJSON(w, http.StatusUnauthorized, map[string]string{"error": "Invalid refresh token"})
		}
		return nil
	}
	if claims.Type != "refresh" {
		logging.Errorf("[TokenManager] Failed to refresh token. Invalid token type: %s", claims.Type) // TokenManager.js:356
		return writeExpressJSON(w, http.StatusUnauthorized, map[string]string{"error": "Invalid token type"})
	}
	s, err := h.sessionByToken(r.Context(), refreshToken)
	if err != nil {
		logging.Error("[TokenManager] Failed to refresh token. Session not found") // TokenManager.js:369
		return writeExpressJSON(w, http.StatusUnauthorized, map[string]string{"error": "Invalid refresh token"})
	}
	grace := s.refreshToken != refreshToken
	if grace {
		if !s.lastRefreshTokenExpires.Valid || !s.lastRefreshTokenExpires.Time.After(time.Now()) {
			return writeExpressJSON(w, http.StatusUnauthorized, map[string]string{"error": "Invalid refresh token"})
		}
	} else if !s.expiresAt.After(time.Now()) {
		logging.Info("[TokenManager] Session expired in database, cleaning up") // TokenManager.js:391
		_ = h.queries().DeleteSessionByID(r.Context(), s.id)
		return writeExpressJSON(w, http.StatusUnauthorized, map[string]string{"error": "Refresh token expired"})
	}
	user, err := h.userByID(r.Context(), claims.UserID)
	if err != nil || !user.IsActive {
		logging.Errorf("[TokenManager] Failed to refresh token. User not found or inactive for user id: %s", claims.UserID) // TokenManager.js:401
		return writeExpressJSON(w, http.StatusUnauthorized, map[string]string{"error": "User not found or inactive"})
	}
	var access, newRefresh string
	if grace {
		access, err = h.issueToken(user, "access", h.accessTTL)
		newRefresh = s.refreshToken
	} else {
		access, newRefresh, err = h.rotateSession(r.Context(), s)
	}
	if err != nil {
		logging.RequestError(r, "error", err)
		return writeExpressJSON(w, http.StatusInternalServerError, map[string]string{"error": "Unable to rotate session"})
	}
	payload, err := h.loginPayload(r.Context(), user)
	if err != nil {
		logging.RequestError(r, "error", err)
		return writeExpressJSON(w, http.StatusInternalServerError, map[string]string{"error": "Unable to load login data"})
	}
	responseUser := payload["user"].(map[string]any)
	responseUser["accessToken"] = access
	responseUser["refreshToken"] = nil
	if returnToken {
		responseUser["refreshToken"] = newRefresh
	}
	setRefreshCookie(w, r, newRefresh, int(h.refreshTTL.Seconds()))
	return writeExpressJSON(w, http.StatusOK, payload)
}

func (h *Handler) logout(w http.ResponseWriter, r *http.Request) error {
	authMethod := cookieValue(r, "auth_method")
	idToken := cookieValue(r, "openid_id_token")
	refresh := ""
	if cookie, err := r.Cookie("refresh_token"); err == nil {
		refresh = cookie.Value
	}
	if refresh == "" {
		refresh = r.Header.Get("x-refresh-token")
	}
	clearExpressCookie(w, "refresh_token")
	clearExpressCookie(w, "auth_method")
	var logoutURL any
	if authMethod == "openid" || authMethod == "openid-mobile" {
		logoutURL = h.openIDLogoutURL(r, idToken, authMethod)
		clearExpressCookie(w, "openid_id_token")
	}
	if refresh == "" {
		logging.Info("[Auth] logout: No refresh token on request") // Auth.js:501
	}
	if r.URL.Query().Get("allDevices") == "1" && refresh != "" {
		userID, err := h.queries().GetSessionUserIDByToken(r.Context(), refresh)
		if err == nil {
			_ = h.queries().DeleteSessionsByUserID(r.Context(), userID)
		}
	} else if refresh != "" {
		_ = h.queries().DeleteSessionByRefreshToken(r.Context(), refresh)
	}
	// Upstream passport+express-session regenerate the session on logout
	// (sessionManager.logOut destroys and creates a new sessionID), so every
	// logout response carries a FRESH signed connect.sid value; the previously
	// presented identifier must not survive across logout responses.
	if sessionCookie, cookieErr := h.freshSessionCookieValue(); cookieErr == nil {
		setExpressCookieValue(w, "connect.sid", sessionCookie, 0, true, false, "")
	}
	return writeExpressJSON(w, http.StatusOK, map[string]any{"redirect_url": logoutURL})
}

func (h *Handler) initialize(w http.ResponseWriter, r *http.Request) error {
	logging.Info("[Server] Initializing new server") // Server.js:486
	tx, err := h.db.BeginTx(r.Context(), nil)
	if err != nil {
		return &httputil.Error{Status: 500, Cause: err}
	}
	defer tx.Rollback()
	count, err := sqlc.New(tx).CountRootUsers(r.Context())
	if err != nil || count > 0 {
		if err == nil {
			logging.Error("[Server] attempt to init server when server already has a root user") // Server.js:493
		}
		return &httputil.Error{Status: 500, Cause: err}
	}
	var body struct {
		NewRoot struct {
			Username string `json:"username"`
			Password string `json:"password"`
		} `json:"newRoot"`
	}
	if decodeBody(r, &body) != nil {
		return httputil.Status(400, "")
	}
	username := body.NewRoot.Username
	if username == "" {
		username = "root"
	}
	passwordHash := ""
	if body.NewRoot.Password == "" {
		logging.Warn("[Server] Creating root user with no password") // Server.js:490
	}
	if body.NewRoot.Password != "" {
		hash, err := bcrypt.GenerateFromPassword([]byte(body.NewRoot.Password), 8)
		if err != nil {
			return &httputil.Error{Status: 500, Cause: err}
		}
		passwordHash = string(hash)
	}
	id, err := ids.New()
	if err != nil {
		return &httputil.Error{Status: 500, Cause: err}
	}
	legacyToken, err := h.issueLegacyToken(id, username)
	if err != nil {
		return &httputil.Error{Status: 500, Cause: err}
	}
	permissions := map[string]any{"download": true, "update": true, "delete": true, "upload": true, "createEreader": true, "accessAllLibraries": true, "accessAllTags": true, "accessExplicitContent": true, "selectedTagsNotAccessible": false, "librariesAccessible": []string{}, "itemTagsSelected": []string{}}
	extra := map[string]any{"seriesHideFromContinueListening": []string{}}
	now := sqlDate(time.Now())
	err = sqlc.New(tx).InsertRootUser(r.Context(), sqlc.InsertRootUserParams{
		ID: id, Username: store.Text(username), Pash: store.Text(passwordHash), Token: store.Text(legacyToken),
		Permissions: store.Text(mustJSON(permissions)), Extradata: store.Text(mustJSON(extra)), Createdat: now, Updatedat: now,
	})
	if err == nil {
		err = tx.Commit()
	}
	if err != nil {
		return &httputil.Error{Status: 500, Cause: err}
	}
	return httputil.OK(w, r)
}

func (h *Handler) createSession(ctx context.Context, user store.User, r *http.Request) (string, string, error) {
	access, err := h.issueToken(user, "access", h.accessTTL)
	if err != nil {
		return "", "", err
	}
	refresh, err := h.issueToken(user, "refresh", h.refreshTTL)
	if err != nil {
		return "", "", err
	}
	id, err := ids.New()
	if err != nil {
		return "", "", err
	}
	now := time.Now().UTC()
	err = h.queries().InsertSession(ctx, sqlc.InsertSessionParams{
		ID: id, Ipaddress: store.Text(clientIP(r)), Useragent: store.Text(r.UserAgent()), Refreshtoken: refresh,
		Expiresat: sqlDate(now.Add(h.refreshTTL)), Createdat: sqlDate(now), Updatedat: sqlDate(now), Userid: user.ID,
	})
	if err != nil {
		return "", "", err
	}
	return access, refresh, nil
}

func (h *Handler) rotateSession(ctx context.Context, s session) (string, string, error) {
	user, err := h.userByID(ctx, s.userID)
	if err != nil {
		return "", "", err
	}
	access, err := h.issueToken(user, "access", h.accessTTL)
	if err != nil {
		return "", "", err
	}
	newRefresh, err := h.issueToken(user, "refresh", h.refreshTTL)
	if err != nil {
		return "", "", err
	}
	now := time.Now().UTC()
	result, err := h.queries().RotateSession(ctx, sqlc.RotateSessionParams{
		NewRefreshToken: newRefresh, ExpiresAt: sqlDate(now.Add(h.refreshTTL)), LastRefreshToken: store.Text(s.refreshToken),
		LastRefreshTokenExpiresAt: sqlDate(now.Add(h.refreshGrace)), UpdatedAt: sqlDate(now), ID: s.id, CurrentRefreshToken: s.refreshToken,
	})
	if err != nil {
		return "", "", err
	}
	updated, _ := result.RowsAffected()
	if updated == 0 {
		current, err := h.queries().GetSessionRefreshToken(ctx, s.id)
		if err != nil {
			return "", "", err
		}
		newRefresh = current
	}
	return access, newRefresh, nil
}

func (h *Handler) sessionByToken(ctx context.Context, token string) (session, error) {
	row, err := h.queries().GetSessionByToken(ctx, token)
	if err != nil {
		return session{}, err
	}
	var exp, grace nullableDate
	if err := exp.Scan(row.Expiresat); err != nil {
		return session{}, err
	}
	if err := grace.Scan(row.Lastrefreshtokenexpiresat); err != nil {
		return session{}, err
	}
	if !exp.Valid {
		return session{}, errors.New("identity: session has no expiry")
	}
	return session{
		id: row.ID, userID: row.Userid, refreshToken: row.Refreshtoken, expiresAt: exp.Time,
		lastRefreshToken: row.Lastrefreshtoken, lastRefreshTokenExpires: grace,
	}, nil
}

func (h *Handler) issueToken(user store.User, tokenType string, ttl time.Duration) (string, error) {
	jti, err := ids.New()
	if err != nil {
		return "", err
	}
	now := time.Now().Unix()
	payload := map[string]any{"userId": user.ID, "username": user.Username, "jti": jti, "type": tokenType, "iat": now, "exp": now + int64(ttl.Seconds())}
	return auth.Sign(h.secret, payload)
}

func (h *Handler) issueLegacyToken(id, username string) (string, error) {
	return auth.Sign(h.secret, map[string]any{"userId": id, "username": username, "iat": time.Now().Unix()})
}

func (h *Handler) loginPayload(ctx context.Context, user store.User) (map[string]any, error) {
	libraryIDs := []string{}
	rows, err := h.db.QueryContext(ctx, `SELECT id FROM libraries ORDER BY displayOrder ASC`)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		libraryIDs = append(libraryIDs, id)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close()
	progress, err := progressdto.Read(ctx, h.db, user.ID)
	if err != nil {
		return nil, fmt.Errorf("identity: load user progress: %w", err)
	}
	userDTO := UserBrowser(user, false, false)
	userDTO["mediaProgress"] = progress
	email, err := h.queries().GetUserEmail(ctx, user.ID)
	if err != nil {
		return nil, fmt.Errorf("identity: load user email: %w", err)
	}
	if email.Valid {
		userDTO["email"] = email.String
	} else {
		userDTO["email"] = nil
	}
	permissions := decodeMap(user.Permissions)
	accessible := stringSlice(permissions["librariesAccessible"])
	defaultLibrary := any(nil)
	if permissions["accessAllLibraries"] == true {
		if len(libraryIDs) > 0 {
			defaultLibrary = libraryIDs[0]
		}
	} else {
		for _, id := range libraryIDs {
			for _, allowed := range accessible {
				if id == allowed {
					defaultLibrary = id
					break
				}
			}
			if defaultLibrary != nil {
				break
			}
		}
	}
	serverSettings, err := h.browserServerSettings(ctx)
	if err != nil {
		return nil, err
	}
	for _, key := range []string{"tokenSecret", "authOpenIDClientID", "authOpenIDClientSecret", "authOpenIDMobileRedirectURIs", "authOpenIDGroupClaim", "authOpenIDAdvancedPermsClaim"} {
		delete(serverSettings, key)
	}
	if message, ok := serverSettings["authLoginCustomMessage"].(string); ok {
		serverSettings["authLoginCustomMessage"] = textclean.Sanitize(message)
	}
	emailSettings, _ := h.loadSettings(ctx, "email-settings")
	devices := filterDevices(emailSettings, user)
	return map[string]any{"user": userDTO, "userDefaultLibraryId": defaultLibrary, "serverSettings": serverSettings, "ereaderDevices": devices, "Source": h.cfg.Source}, nil
}

func (h *Handler) browserServerSettings(ctx context.Context) (map[string]any, error) {
	var defaults struct {
		Rows []struct {
			Key   string         `json:"key"`
			Value map[string]any `json:"value"`
		} `json:"rows"`
	}
	if err := json.Unmarshal(schemaassets.DefaultSettingsJSON, &defaults); err != nil {
		return nil, err
	}
	settings := map[string]any{}
	for _, row := range defaults.Rows {
		if row.Key == "server-settings" {
			for key, value := range row.Value {
				settings[key] = value
			}
			break
		}
	}
	settings["backupPath"] = filepath.Join(h.cfg.MetadataPath, "backups")
	settings["authOpenIDTokenSigningAlgorithm"] = "RS256"
	stored, err := h.loadSettings(ctx, "server-settings")
	if err != nil {
		return nil, err
	}
	for key, value := range stored {
		settings[key] = value
	}
	// TS ServerSettings keeps an unset authOpenIDTokenURL as null (`settings.x || null`),
	// and the fresh default row does not store the key. The login response sends it.
	if _, ok := settings["authOpenIDTokenURL"]; !ok {
		settings["authOpenIDTokenURL"] = nil
	}
	if value := os.Getenv("BACKUP_PATH"); value != "" {
		settings["backupPath"] = value
	}
	if os.Getenv("ALLOW_IFRAME") == "1" {
		settings["allowIframe"] = true
	}
	settings["timeZone"] = hostTimeZone()
	return settings, nil
}

// hostTimeZone mirrors upstream ServerSettings.getHostTimeZone, which answers
// the process IANA zone name (Intl.DateTimeFormat().resolvedOptions().timeZone)
// and falls back to UTC. Go's time.Local.String() returns the unnamed "Local"
// zone when TZ is unset, so resolve the zone from the environment or the
// /etc/localtime symlink.
func hostTimeZone() string {
	if tz := strings.TrimSpace(os.Getenv("TZ")); tz != "" {
		return icuZoneName(tz)
	}
	if link, err := filepath.EvalSymlinks("/etc/localtime"); err == nil {
		if index := strings.LastIndex(link, "zoneinfo/"); index >= 0 {
			return icuZoneName(link[index+len("zoneinfo/"):])
		}
	}
	return "UTC"
}

// icuZoneName returns the zone name Node 22's ICU reports for a TZ value
// (for example Etc/UTC -> UTC, US/Pacific -> America/Los_Angeles). The table
// icuZones is generated by tools/icuzones/generate.cjs from
// TZ=<id> node -e 'Intl.DateTimeFormat().resolvedOptions().timeZone'; names it
// does not list resolve to themselves.
func icuZoneName(zone string) string {
	if canonical, ok := icuZones[zone]; ok {
		return canonical
	}
	return zone
}

func filterDevices(settings map[string]any, user store.User) []any {
	devices, _ := settings["ereaderDevices"].([]any)
	result := []any{}
	for _, raw := range devices {
		device, ok := raw.(map[string]any)
		if !ok || device["name"] == nil || device["email"] == nil {
			continue
		}
		availability, _ := device["availabilityOption"].(string)
		if availability == "" {
			availability = "adminOrUp"
		}
		allowed := availability == "guestOrUp" || (availability == "adminOrUp" && (user.Type == "root" || user.Type == "admin")) || (availability == "userOrUp" && user.Type != "guest")
		if availability == "specificUsers" {
			for _, id := range stringSlice(device["users"]) {
				allowed = allowed || id == user.ID
			}
		}
		if allowed {
			result = append(result, device)
		}
	}
	return result
}

func (h *Handler) loadSettings(ctx context.Context, key string) (map[string]any, error) {
	var raw []byte
	if err := h.db.QueryRowContext(ctx, `SELECT value FROM settings WHERE key = ? LIMIT 1`, key).Scan(&raw); err != nil {
		return nil, err
	}
	var value map[string]any
	if err := json.Unmarshal(raw, &value); err != nil {
		return nil, err
	}
	if value == nil {
		value = map[string]any{}
	}
	return value, nil
}

func (h *Handler) userByUsername(ctx context.Context, username string) (store.User, error) {
	return userResult(h.queries().GetUserByUsername(ctx, store.Text(username)))
}

func (h *Handler) userByID(ctx context.Context, id string) (store.User, error) {
	return userResult(h.queries().GetUserByID(ctx, id))
}

func (h *Handler) userByIDOrOldID(ctx context.Context, id string) (store.User, error) {
	user, err := h.userByID(ctx, id)
	if err == nil || !errors.Is(err, sql.ErrNoRows) {
		return user, err
	}
	matched, err := h.queries().GetUserIDByOldUserID(ctx, id)
	if err != nil {
		return store.User{}, err
	}
	return h.userByID(ctx, matched)
}

// queries returns the generated query set on the handler's pool. It is cheap:
// sqlc.New only stores the handle.
func (h *Handler) queries() *sqlc.Queries { return sqlc.New(h.db) }

// userResult maps a generated users row to store.User; sql.ErrNoRows passes
// through unchanged for callers that branch on it.
func userResult(row sqlc.User, err error) (store.User, error) {
	if err != nil {
		return store.User{}, err
	}
	return store.UserFromRow(row)
}

func requireColumns(db *sql.DB, table string, required []string) error {
	rows, err := db.Query(`PRAGMA table_info("` + table + `")`)
	if err != nil {
		return err
	}
	defer rows.Close()
	cols := map[string]bool{}
	for rows.Next() {
		var cid, notnull, pk int
		var name, typ string
		var def any
		if err := rows.Scan(&cid, &name, &typ, &notnull, &def, &pk); err != nil {
			return err
		}
		cols[name] = true
	}
	for _, col := range required {
		if !cols[col] {
			return fmt.Errorf("identity: existing %s table missing column %s", table, col)
		}
	}
	return rows.Err()
}

func decodeBody(r *http.Request, dest any) error {
	defer r.Body.Close()
	d := json.NewDecoder(io.LimitReader(r.Body, 1<<20))
	if err := d.Decode(dest); err != nil {
		return err
	}
	var extra any
	if err := d.Decode(&extra); err != io.EOF {
		return errors.New("identity: trailing request body data")
	}
	return nil
}

func requestToken(r *http.Request) string {
	h := r.Header.Get("Authorization")
	if len(h) >= 7 && strings.EqualFold(h[:7], "Bearer ") {
		return strings.TrimSpace(h[7:])
	}
	return r.URL.Query().Get("token")
}

func setRefreshCookie(w http.ResponseWriter, r *http.Request, value string, maxAge int) {
	setExpressCookie(w, "refresh_token", value, maxAge, true, requestSecure(r), "Lax")
}

func setExpressCookie(w http.ResponseWriter, name, value string, maxAge int, httpOnly, secure bool, sameSite string) {
	setExpressCookieValue(w, name, encodeCookieValue(value), maxAge, httpOnly, secure, sameSite)
}

func encodeCookieValue(value string) string {
	encoded := url.QueryEscape(value)
	return strings.NewReplacer("+", "%20", "%21", "!", "%27", "'", "%28", "(", "%29", ")", "%2A", "*").Replace(encoded)
}

func setExpressCookieValue(w http.ResponseWriter, name, encodedValue string, maxAge int, httpOnly, secure bool, sameSite string) {
	parts := []string{name + "=" + encodedValue}
	if maxAge > 0 {
		parts = append(parts, "Max-Age="+strconv.Itoa(maxAge), "Path=/", "Expires="+time.Now().Add(time.Duration(maxAge)*time.Second).UTC().Format(http.TimeFormat))
	} else {
		parts = append(parts, "Path=/")
	}
	if httpOnly {
		parts = append(parts, "HttpOnly")
	}
	if secure {
		parts = append(parts, "Secure")
	}
	if sameSite != "" {
		parts = append(parts, "SameSite="+sameSite)
	}
	w.Header().Add("Set-Cookie", strings.Join(parts, "; "))
}

func (h *Handler) sessionCookieValue(r *http.Request) (string, error) {
	if cookie, err := r.Cookie("connect.sid"); err == nil {
		if decoded, decodeErr := url.QueryUnescape(cookie.Value); decodeErr == nil && h.validSessionCookie(decoded) {
			return encodeCookieValue(decoded), nil
		}
	}
	return h.freshSessionCookieValue()
}

// freshSessionCookieValue mints a new signed session cookie value. Upstream
// express-session+passport mint a fresh random sessionID on every successful
// login and regenerate on logout (passport sessionManager logIn/logOut),
// so the family always responds with a new connect.sid value.
func (h *Handler) freshSessionCookieValue() (string, error) {
	sessionID, err := randomURLToken(24)
	if err != nil {
		return "", err
	}
	mac := hmac.New(sha256.New, h.secret)
	_, _ = mac.Write([]byte(sessionID))
	signature := strings.TrimRight(base64.StdEncoding.EncodeToString(mac.Sum(nil)), "=")
	return encodeCookieValue("s:" + sessionID + "." + signature), nil
}

func (h *Handler) validSessionCookie(value string) bool {
	if !strings.HasPrefix(value, "s:") {
		return false
	}
	unsigned, signature, ok := strings.Cut(strings.TrimPrefix(value, "s:"), ".")
	if !ok || unsigned == "" || signature == "" {
		return false
	}
	mac := hmac.New(sha256.New, h.secret)
	_, _ = mac.Write([]byte(unsigned))
	want := strings.TrimRight(base64.StdEncoding.EncodeToString(mac.Sum(nil)), "=")
	return hmac.Equal([]byte(signature), []byte(want))
}

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

func clientIP(r *http.Request) string {
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		return strings.TrimSpace(strings.Split(xff, ",")[0])
	}
	if host, _, err := strings.Cut(r.RemoteAddr, ":"); err {
		return host
	}
	return r.RemoteAddr
}

func sqlDate(t time.Time) string { return dbtime.Format(t) }

func durationFromEnv(name string, fallback time.Duration) time.Duration {
	value, err := strconv.Atoi(strings.TrimSpace(os.Getenv(name)))
	if err != nil || value == 0 {
		return fallback
	}
	return time.Duration(value) * time.Second
}

func intFromEnv(name string, fallback int, allowZero bool) int {
	value, err := strconv.Atoi(strings.TrimSpace(os.Getenv(name)))
	if err != nil || value < 0 || (value == 0 && !allowZero) {
		return fallback
	}
	return value
}

func envString(name, fallback string) string {
	if value, ok := os.LookupEnv(name); ok && value != "" {
		return value
	}
	return fallback
}

// allowAuthRequest spends a token from the client's bucket: a burst of rateMax
// refilled at rateMax per rateWindow, the practical limit of TS's fixed window
// (express-rate-limit), which also admits up to 2x across a window boundary.
func (h *Handler) allowAuthRequest(w http.ResponseWriter, r *http.Request) bool {
	if h.rateMax <= 0 || h.rateWindow <= 0 {
		return true
	}
	key := clientIP(r)
	h.rateMu.Lock()
	if time.Since(h.rateSwept) >= h.rateWindow {
		h.rateSwept = time.Now()
		// A full bucket is what a new client gets, so forgetting it changes nothing.
		maps.DeleteFunc(h.rateLimiters, func(_ string, l *rate.Limiter) bool { return l.Tokens() >= float64(l.Burst()) })
	}
	lim := h.rateLimiters[key]
	if lim == nil {
		lim = rate.NewLimiter(rate.Limit(float64(h.rateMax)/h.rateWindow.Seconds()), h.rateMax)
		h.rateLimiters[key] = lim
	}
	allowed, tokens := lim.Allow(), lim.Tokens()
	h.rateMu.Unlock()
	w.Header().Set("RateLimit-Policy", fmt.Sprintf("%d;w=%d", h.rateMax, int(h.rateWindow.Seconds())))
	w.Header().Set("RateLimit", fmt.Sprintf("limit=%d, remaining=%d, reset=%d", h.rateMax, int(tokens), int(math.Ceil((float64(lim.Burst())-tokens)/float64(lim.Limit())))))
	if allowed {
		return true
	}
	httputil.WriteJSON(w, http.StatusTooManyRequests, map[string]string{"error": h.rateMessage})
	return false
}

func decodeMap(raw json.RawMessage) map[string]any {
	var result map[string]any
	_ = json.Unmarshal(raw, &result)
	if result == nil {
		return map[string]any{}
	}
	return result
}

func decodeArray(raw json.RawMessage) []any {
	var result []any
	_ = json.Unmarshal(raw, &result)
	return result
}

func stringSlice(value any) []string {
	values, _ := value.([]any)
	if s, ok := value.([]string); ok {
		return append([]string(nil), s...)
	}
	result := []string{}
	for _, v := range values {
		if s, ok := v.(string); ok {
			result = append(result, s)
		}
	}
	return result
}

func mustJSON(value any) string {
	data, _ := json.Marshal(value)
	return string(data)
}

// writePassportDenied reproduces the default passport failure response
// (passport authenticate.js): res.statusCode = 401 and a bare "Unauthorized"
// body written by res.end, which carries Content-Length:12 and NO Content-Type.
// A present nil header suppresses net/http's content-type sniffing while
// retaining its buffered Content-Length calculation.
func writePassportDenied(w http.ResponseWriter) error {
	w.Header()["Content-Type"] = nil
	w.WriteHeader(http.StatusUnauthorized)
	_, _ = w.Write([]byte("Unauthorized"))
	return nil
}

// writePlain answers the 401 "Unauthorized" of OIDC routes (auth contract, kept as is).
func writePlain(w http.ResponseWriter, status int, body string) error {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Content-Length", fmt.Sprint(len(body)))
	w.WriteHeader(status)
	_, _ = w.Write([]byte(body))
	return nil
}

func writeExpressText(w http.ResponseWriter, status int, body string) error {
	return writeExpressBody(w, status, "text/html; charset=utf-8", []byte(body))
}

func writeExpressJSON(w http.ResponseWriter, status int, value any) error {
	var buffer bytes.Buffer
	encoder := json.NewEncoder(&buffer)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(value); err != nil {
		logging.Errorf("[identity] writeExpressJSON: internal server error: %v", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return nil
	}
	body := bytes.TrimSuffix(buffer.Bytes(), []byte{'\n'})
	return writeExpressBody(w, status, "application/json; charset=utf-8", body)
}

func writeExpressBody(w http.ResponseWriter, status int, contentType string, body []byte) error {
	digest := sha1.Sum(body)
	etag := fmt.Sprintf(`W/"%x-%s"`, len(body), strings.TrimRight(base64.StdEncoding.EncodeToString(digest[:]), "="))
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Content-Length", fmt.Sprint(len(body)))
	w.Header().Set("ETag", etag)
	w.WriteHeader(status)
	_, _ = w.Write(body)
	return nil
}

func clearExpressCookie(w http.ResponseWriter, name string) {
	w.Header().Add("Set-Cookie", name+"=; Path=/; Expires="+time.UnixMilli(1).UTC().Format(http.TimeFormat))
}

func redirectExpress(w http.ResponseWriter, status int, target string) error {
	body := http.StatusText(status) + ". Redirecting to " + target
	w.Header().Set("Location", target)
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Content-Length", fmt.Sprint(len(body)))
	w.WriteHeader(status)
	_, _ = w.Write([]byte(body))
	return nil
}

// InvalidateUserSessions removes every refresh session for userID, preserving
// and rotating the current session when currentRefreshToken names its active
// or grace-period refresh token. Empty or unknown tokens invalidate all.
func (h *Handler) InvalidateUserSessions(ctx context.Context, userID, currentRefreshToken string) (accessToken, refreshToken string, err error) {
	if currentRefreshToken != "" {
		s, lookupErr := h.sessionByToken(ctx, currentRefreshToken)
		if lookupErr == nil && s.userID == userID {
			accessToken, refreshToken, err = h.rotateSession(ctx, s)
			if err != nil {
				return "", "", err
			}
			if err = h.queries().DeleteOtherSessionsByUserID(ctx, sqlc.DeleteOtherSessionsByUserIDParams{Userid: userID, ID: s.id}); err != nil {
				return "", "", err
			}
			return accessToken, refreshToken, nil
		}
	}
	err = h.queries().DeleteSessionsByUserID(ctx, userID)
	return "", "", err
}

// VerifyPassword applies the pinned local strategy's passwordless-root and
// bcrypt comparison rules, returning the upstream error text on failure.
func (h *Handler) VerifyPassword(ctx context.Context, userID, password string) error {
	user, err := h.userByID(ctx, userID)
	if err != nil {
		return err
	}
	if user.Type == "root" && user.Password == "" && password == "" {
		return nil
	}
	if password == "" || user.Password == "" || bcrypt.CompareHashAndPassword([]byte(user.Password), []byte(password)) != nil {
		return errors.New("Invalid password")
	}
	return nil
}

// ChangePassword matches LocalAuthStrategy.changePassword: only root may use
// an empty new password, and existing passwords are verified before hashing.
func (h *Handler) ChangePassword(ctx context.Context, userID, password, newPassword string) error {
	user, err := h.userByID(ctx, userID)
	if err != nil {
		return err
	}
	if user.Type != "root" && newPassword == "" {
		return errors.New("Invalid new password - Only root can have an empty password")
	}
	if err := h.VerifyPassword(ctx, userID, password); err != nil {
		return err
	}
	encoded := ""
	if newPassword != "" {
		hash, err := bcrypt.GenerateFromPassword([]byte(newPassword), 8)
		if err != nil {
			return errors.New("Hash failed")
		}
		encoded = string(hash)
	}
	return h.queries().UpdateUserPassword(ctx, sqlc.UpdateUserPasswordParams{Pash: store.Text(encoded), Updatedat: sqlDate(time.Now()), ID: userID})
}

// SetRefreshTokenCookie writes the standard refresh cookie used by login,
// refresh, and credential-change flows.
func (h *Handler) SetRefreshTokenCookie(w http.ResponseWriter, r *http.Request, refreshToken string) {
	setRefreshCookie(w, r, refreshToken, int(h.refreshTTL.Seconds()))
}
