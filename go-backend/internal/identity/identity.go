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
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
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
	"audiobookshelf-compat/internal/dbtime"
	"audiobookshelf-compat/internal/progressdto"
	"audiobookshelf-compat/internal/store"
	"golang.org/x/crypto/bcrypt"
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
	rateBuckets      map[string]rateBucket
	oidcMu           sync.Mutex
	oidcFlows        map[string]oidcFlow
	mobileFlows      map[string]mobileFlow
}

// SetUserAddedEmitter installs the realtime event used by OpenID auto-registration.
// Runtime composition supplies the shared Socket.IO hub before serving requests.
func (h *Handler) SetUserAddedEmitter(emit func(map[string]any)) {
	h.userAddedEmitter = emit
}

type rateBucket struct {
	started time.Time
	count   int
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
	for _, layout := range []string{
		time.RFC3339Nano,
		"2006-01-02 15:04:05.999999999 -07:00",
		"2006-01-02 15:04:05.000 -07:00",
		"2006-01-02 15:04:05 -07:00",
		"2006-01-02 15:04:05.999999999",
		"2006-01-02 15:04:05.000",
		"2006-01-02 15:04:05",
	} {
		if parsed, err := time.Parse(layout, text); err == nil {
			d.Time, d.Valid = parsed, true
			return nil
		}
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
		rateBuckets:  make(map[string]rateBucket),
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

// Handle consumes the local identity endpoints and /api/authorize. Paths may
// be passed with or without the configured router prefix.
func (h *Handler) Handle(w http.ResponseWriter, r *http.Request) bool {
	path := r.URL.Path
	if base := h.cfg.RouterBasePath; base != "" && strings.HasPrefix(path, base+"/") {
		path = strings.TrimPrefix(path, base)
	}
	switch {
	case r.Method == http.MethodPost && path == "/login":
		h.login(w, r)
	case r.Method == http.MethodPost && path == "/auth/refresh":
		h.refresh(w, r)
	case r.Method == http.MethodPost && path == "/logout":
		h.logout(w, r)
	case r.Method == http.MethodPost && path == "/init":
		h.initialize(w, r)
	case r.Method == http.MethodGet && path == "/auth/openid":
		h.oidcAuthorize(w, r)
	case r.Method == http.MethodGet && path == "/auth/openid/mobile-redirect":
		h.oidcMobileRedirect(w, r)
	case r.Method == http.MethodGet && path == "/auth/openid/callback":
		h.oidcCallback(w, r)
	case r.Method == http.MethodGet && path == "/auth/openid/config":
		h.oidcConfig(w, r)
	case r.Method == http.MethodPost && path == "/api/authorize":
		user, err := h.Authenticate(r)
		if err != nil {
			writePassportDenied(w)
			return true
		}
		h.Authorize(w, r, user)
	default:
		return false
	}
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

// Authorize writes the payload returned by the upstream /api/authorize route.
func (h *Handler) Authorize(w http.ResponseWriter, r *http.Request, user store.User) {
	payload, err := h.loginPayload(r.Context(), user)
	if err != nil {
		writeExpressJSON(w, http.StatusInternalServerError, map[string]string{"error": "Unable to load authorization data"})
		return
	}
	writeExpressJSON(w, http.StatusOK, payload)
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
		"id": user.ID, "username": user.Username, "email": nullableUserEmail(user.Email), "type": user.Type,
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

func nullableUserEmail(email string) any {
	if email == "" {
		return nil
	}
	return email
}

func (h *Handler) login(w http.ResponseWriter, r *http.Request) {
	if !h.authMethodEnabled(r.Context(), "local") {
		writePassportDenied(w)
		return
	}
	if !h.allowAuthRequest(w, r) {
		return
	}
	var body struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if decodeBody(r, &body) != nil || body.Username == "" {
		writePassportDenied(w)
		return
	}
	user, err := h.userByUsername(r.Context(), strings.ToLower(body.Username))
	if err != nil || !user.IsActive {
		writePassportDenied(w)
		return
	}
	if user.Type == "root" && user.Password == "" {
		if body.Password != "" {
			writePassportDenied(w)
			return
		}
	} else if user.Password == "" || bcrypt.CompareHashAndPassword([]byte(user.Password), []byte(body.Password)) != nil {
		writePassportDenied(w)
		return
	}
	access, refresh, err := h.createSession(r.Context(), user, r)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Unable to create session"})
		return
	}
	payload, err := h.loginPayload(r.Context(), user)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Unable to load login data"})
		return
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
	writeExpressJSON(w, http.StatusOK, payload)
}

func (h *Handler) refresh(w http.ResponseWriter, r *http.Request) {
	if !h.allowAuthRequest(w, r) {
		return
	}
	refreshToken := ""
	returnToken := false
	if header := r.Header.Get("x-refresh-token"); header != "" {
		refreshToken, returnToken = header, true
	} else if cookie, err := r.Cookie("refresh_token"); err == nil {
		refreshToken = cookie.Value
	}
	if refreshToken == "" {
		writeExpressJSON(w, http.StatusUnauthorized, map[string]string{"error": "No refresh token provided"})
		return
	}
	claims, err := auth.ValidateToken(refreshToken, h.secret, time.Now())
	if err != nil {
		if errors.Is(err, auth.ErrExpired) {
			_, _ = h.db.ExecContext(r.Context(), `DELETE FROM sessions WHERE refreshToken = ?`, refreshToken)
			writeExpressJSON(w, http.StatusUnauthorized, map[string]string{"error": "Refresh token expired"})
		} else {
			writeExpressJSON(w, http.StatusUnauthorized, map[string]string{"error": "Invalid refresh token"})
		}
		return
	}
	if claims.Type != "refresh" {
		writeExpressJSON(w, http.StatusUnauthorized, map[string]string{"error": "Invalid token type"})
		return
	}
	s, err := h.sessionByToken(r.Context(), refreshToken)
	if err != nil {
		writeExpressJSON(w, http.StatusUnauthorized, map[string]string{"error": "Invalid refresh token"})
		return
	}
	grace := s.refreshToken != refreshToken
	if grace {
		if !s.lastRefreshTokenExpires.Valid || !s.lastRefreshTokenExpires.Time.After(time.Now()) {
			writeExpressJSON(w, http.StatusUnauthorized, map[string]string{"error": "Invalid refresh token"})
			return
		}
	} else if !s.expiresAt.After(time.Now()) {
		_, _ = h.db.ExecContext(r.Context(), `DELETE FROM sessions WHERE id = ?`, s.id)
		writeExpressJSON(w, http.StatusUnauthorized, map[string]string{"error": "Refresh token expired"})
		return
	}
	user, err := h.userByID(r.Context(), claims.UserID)
	if err != nil || !user.IsActive {
		writeExpressJSON(w, http.StatusUnauthorized, map[string]string{"error": "User not found or inactive"})
		return
	}
	var access, newRefresh string
	if grace {
		access, err = h.issueToken(user, "access", h.accessTTL)
		newRefresh = s.refreshToken
	} else {
		access, newRefresh, err = h.rotateSession(r.Context(), s)
	}
	if err != nil {
		writeExpressJSON(w, http.StatusInternalServerError, map[string]string{"error": "Unable to rotate session"})
		return
	}
	payload, err := h.loginPayload(r.Context(), user)
	if err != nil {
		writeExpressJSON(w, http.StatusInternalServerError, map[string]string{"error": "Unable to load login data"})
		return
	}
	responseUser := payload["user"].(map[string]any)
	responseUser["accessToken"] = access
	responseUser["refreshToken"] = nil
	if returnToken {
		responseUser["refreshToken"] = newRefresh
	}
	setRefreshCookie(w, r, newRefresh, int(h.refreshTTL.Seconds()))
	writeExpressJSON(w, http.StatusOK, payload)
}

func (h *Handler) logout(w http.ResponseWriter, r *http.Request) {
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
	if r.URL.Query().Get("allDevices") == "1" && refresh != "" {
		var userID string
		err := h.db.QueryRowContext(r.Context(), `SELECT userId FROM sessions WHERE refreshToken = ? OR lastRefreshToken = ? LIMIT 1`, refresh, refresh).Scan(&userID)
		if err == nil {
			_, _ = h.db.ExecContext(r.Context(), `DELETE FROM sessions WHERE userId = ?`, userID)
		}
	} else if refresh != "" {
		_, _ = h.db.ExecContext(r.Context(), `DELETE FROM sessions WHERE refreshToken = ?`, refresh)
	}
	// Upstream passport+express-session regenerate the session on logout
	// (sessionManager.logOut destroys and creates a new sessionID), so every
	// logout response carries a FRESH signed connect.sid value; the previously
	// presented identifier must not survive across logout responses.
	if sessionCookie, cookieErr := h.freshSessionCookieValue(); cookieErr == nil {
		setExpressCookieValue(w, "connect.sid", sessionCookie, 0, true, false, "")
	}
	writeExpressJSON(w, http.StatusOK, map[string]any{"redirect_url": logoutURL})
}

func (h *Handler) initialize(w http.ResponseWriter, r *http.Request) {
	tx, err := h.db.BeginTx(r.Context(), nil)
	if err != nil {
		writePlain(w, http.StatusInternalServerError, "Internal Server Error")
		return
	}
	defer tx.Rollback()
	var count int
	if err := tx.QueryRowContext(r.Context(), `SELECT COUNT(*) FROM users WHERE type = 'root'`).Scan(&count); err != nil || count > 0 {
		writePlain(w, http.StatusInternalServerError, "Internal Server Error")
		return
	}
	var body struct {
		NewRoot struct {
			Username string `json:"username"`
			Password string `json:"password"`
		} `json:"newRoot"`
	}
	if decodeBody(r, &body) != nil {
		writePlain(w, http.StatusBadRequest, "Bad Request")
		return
	}
	username := body.NewRoot.Username
	if username == "" {
		username = "root"
	}
	passwordHash := ""
	if body.NewRoot.Password != "" {
		hash, err := bcrypt.GenerateFromPassword([]byte(body.NewRoot.Password), 8)
		if err != nil {
			writePlain(w, http.StatusInternalServerError, "Internal Server Error")
			return
		}
		passwordHash = string(hash)
	}
	id, err := randomUUID()
	if err != nil {
		writePlain(w, http.StatusInternalServerError, "Internal Server Error")
		return
	}
	legacyToken, err := h.issueLegacyToken(id, username)
	if err != nil {
		writePlain(w, http.StatusInternalServerError, "Internal Server Error")
		return
	}
	permissions := map[string]any{"download": true, "update": true, "delete": true, "upload": true, "createEreader": true, "accessAllLibraries": true, "accessAllTags": true, "accessExplicitContent": true, "selectedTagsNotAccessible": false, "librariesAccessible": []string{}, "itemTagsSelected": []string{}}
	extra := map[string]any{"seriesHideFromContinueListening": []string{}}
	now := sqlDate(time.Now())
	_, err = tx.ExecContext(r.Context(), `INSERT INTO users (id, username, email, pash, type, token, isActive, isLocked, lastSeen, permissions, bookmarks, extraData, createdAt, updatedAt) VALUES (?, ?, '', ?, 'root', ?, 1, 0, NULL, ?, '[]', ?, ?, ?)`, id, username, passwordHash, legacyToken, mustJSON(permissions), mustJSON(extra), now, now)
	if err != nil || tx.Commit() != nil {
		writePlain(w, http.StatusInternalServerError, "Internal Server Error")
		return
	}
	writePlain(w, http.StatusOK, "OK")
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
	id, err := randomUUID()
	if err != nil {
		return "", "", err
	}
	now := time.Now().UTC()
	_, err = h.db.ExecContext(ctx, `INSERT INTO sessions (id, ipAddress, userAgent, refreshToken, expiresAt, createdAt, updatedAt, userId, lastRefreshToken, lastRefreshTokenExpiresAt) VALUES (?, ?, ?, ?, ?, ?, ?, ?, NULL, NULL)`, id, clientIP(r), r.UserAgent(), refresh, sqlDate(now.Add(h.refreshTTL)), sqlDate(now), sqlDate(now), user.ID)
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
	result, err := h.db.ExecContext(ctx, `UPDATE sessions SET refreshToken = ?, expiresAt = ?, lastRefreshToken = ?, lastRefreshTokenExpiresAt = ?, updatedAt = ? WHERE id = ? AND refreshToken = ?`, newRefresh, sqlDate(now.Add(h.refreshTTL)), s.refreshToken, sqlDate(now.Add(h.refreshGrace)), sqlDate(now), s.id, s.refreshToken)
	if err != nil {
		return "", "", err
	}
	updated, _ := result.RowsAffected()
	if updated == 0 {
		var current string
		if err := h.db.QueryRowContext(ctx, `SELECT refreshToken FROM sessions WHERE id = ?`, s.id).Scan(&current); err != nil {
			return "", "", err
		}
		newRefresh = current
	}
	return access, newRefresh, nil
}

func (h *Handler) sessionByToken(ctx context.Context, token string) (session, error) {
	var s session
	var exp, grace nullableDate
	err := h.db.QueryRowContext(ctx, `SELECT id, userId, refreshToken, expiresAt, lastRefreshToken, lastRefreshTokenExpiresAt FROM sessions WHERE refreshToken = ? OR lastRefreshToken = ? LIMIT 1`, token, token).Scan(&s.id, &s.userID, &s.refreshToken, &exp, &s.lastRefreshToken, &grace)
	if err != nil {
		return session{}, err
	}
	if !exp.Valid {
		return session{}, errors.New("identity: session has no expiry")
	}
	s.expiresAt = exp.Time
	s.lastRefreshTokenExpires = grace
	return s, nil
}

func (h *Handler) issueToken(user store.User, tokenType string, ttl time.Duration) (string, error) {
	jti, err := randomUUID()
	if err != nil {
		return "", err
	}
	now := time.Now().Unix()
	payload := map[string]any{"userId": user.ID, "username": user.Username, "jti": jti, "type": tokenType, "iat": now, "exp": now + int64(ttl.Seconds())}
	return h.sign(payload)
}

func (h *Handler) issueLegacyToken(id, username string) (string, error) {
	return h.sign(map[string]any{"userId": id, "username": username, "iat": time.Now().Unix()})
}

func (h *Handler) sign(payload any) (string, error) {
	header, _ := json.Marshal(map[string]string{"alg": "HS256", "typ": "JWT"})
	body, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	input := base64.RawURLEncoding.EncodeToString(header) + "." + base64.RawURLEncoding.EncodeToString(body)
	mac := hmac.New(sha256.New, h.secret)
	_, _ = mac.Write([]byte(input))
	return input + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil)), nil
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
	var email sql.NullString
	if err := h.db.QueryRowContext(ctx, `SELECT email FROM users WHERE id = ?`, user.ID).Scan(&email); err != nil {
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
		serverSettings["authLoginCustomMessage"] = auth.SanitizeHTML(message)
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
		return tz
	}
	if link, err := filepath.EvalSymlinks("/etc/localtime"); err == nil {
		if index := strings.LastIndex(link, "zoneinfo/"); index >= 0 {
			return link[index+len("zoneinfo/"):]
		}
	}
	return "UTC"
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
	return h.userQuery(ctx, `SELECT id, username, email, pash, type, token, isActive, isLocked, lastSeen, permissions, bookmarks, extraData, createdAt, updatedAt FROM users WHERE lower(username) = ? LIMIT 1`, username)
}

func (h *Handler) userByID(ctx context.Context, id string) (store.User, error) {
	return h.userQuery(ctx, `SELECT id, username, email, pash, type, token, isActive, isLocked, lastSeen, permissions, bookmarks, extraData, createdAt, updatedAt FROM users WHERE id = ? LIMIT 1`, id)
}

func (h *Handler) userByIDOrOldID(ctx context.Context, id string) (store.User, error) {
	user, err := h.userByID(ctx, id)
	if err == nil || !errors.Is(err, sql.ErrNoRows) {
		return user, err
	}
	rows, err := h.db.QueryContext(ctx, `SELECT id, extraData FROM users`)
	if err != nil {
		return store.User{}, err
	}
	defer rows.Close()
	for rows.Next() {
		var userID string
		var raw []byte
		if err := rows.Scan(&userID, &raw); err != nil {
			return store.User{}, err
		}
		var extra map[string]any
		if json.Unmarshal(raw, &extra) == nil && extra["oldUserId"] == id {
			return h.userByID(ctx, userID)
		}
	}
	if err := rows.Err(); err != nil {
		return store.User{}, err
	}
	return store.User{}, sql.ErrNoRows
}

func (h *Handler) userQuery(ctx context.Context, query string, args ...any) (store.User, error) {
	var u store.User
	var lastSeen, created, updated nullableDate
	var active, locked int64
	var email, pash, typ, token sql.NullString
	var permissions, bookmarks, extra []byte
	err := h.db.QueryRowContext(ctx, query, args...).Scan(&u.ID, &u.Username, &email, &pash, &typ, &token, &active, &locked, &lastSeen, &permissions, &bookmarks, &extra, &created, &updated)
	if err != nil {
		return store.User{}, err
	}
	u.Email, u.Password, u.Type, u.Token = email.String, pash.String, typ.String, token.String
	u.IsActive, u.IsLocked = active != 0, locked != 0
	u.LastSeen = timeMillis(lastSeen)
	u.CreatedAt = timeMillis(created)
	u.UpdatedAt = timeMillis(updated)
	u.Permissions, u.Bookmarks, u.ExtraData = jsonRaw(permissions), jsonRaw(bookmarks), jsonRaw(extra)
	return u, nil
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

func (h *Handler) allowAuthRequest(w http.ResponseWriter, r *http.Request) bool {
	if h.rateMax == 0 || h.rateMax < 0 || h.rateWindow <= 0 {
		return true
	}
	now := time.Now()
	key := clientIP(r)
	h.rateMu.Lock()
	bucket := h.rateBuckets[key]
	if bucket.started.IsZero() || now.Sub(bucket.started) >= h.rateWindow {
		bucket = rateBucket{started: now}
	}
	bucket.count++
	h.rateBuckets[key] = bucket
	count := bucket.count
	reset := int(math.Ceil(math.Max(0, h.rateWindow.Seconds()-now.Sub(bucket.started).Seconds())))
	h.rateMu.Unlock()
	remaining := h.rateMax - count
	if remaining < 0 {
		remaining = 0
	}
	w.Header().Set("RateLimit-Policy", fmt.Sprintf("%d;w=%d", h.rateMax, int(h.rateWindow.Seconds())))
	w.Header().Set("RateLimit", fmt.Sprintf("limit=%d, remaining=%d, reset=%d", h.rateMax, remaining, reset))
	if count <= h.rateMax {
		return true
	}
	writeJSON(w, http.StatusTooManyRequests, map[string]string{"error": h.rateMessage})
	return false
}

func timeMillis(v nullableDate) *int64 {
	if !v.Valid {
		return nil
	}
	n := v.Time.UnixMilli()
	return &n
}

func jsonRaw(raw []byte) json.RawMessage {
	if len(raw) == 0 || !json.Valid(raw) {
		return json.RawMessage("null")
	}
	return append(json.RawMessage(nil), raw...)
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

func randomUUID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	encoded := hex.EncodeToString(b[:])
	return encoded[:8] + "-" + encoded[8:12] + "-" + encoded[12:16] + "-" + encoded[16:20] + "-" + encoded[20:], nil
}

func mustJSON(value any) string {
	data, _ := json.Marshal(value)
	return string(data)
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	data, err := json.Marshal(value)
	if err != nil {
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Content-Length", fmt.Sprint(len(data)))
	w.WriteHeader(status)
	_, _ = w.Write(data)
}

// writePassportDenied reproduces the default passport failure response
// (passport authenticate.js): res.statusCode = 401 and a bare "Unauthorized"
// body written by res.end, which carries Content-Length:12 and NO Content-Type.
// A present nil header suppresses net/http's content-type sniffing while
// retaining its buffered Content-Length calculation.
func writePassportDenied(w http.ResponseWriter) {
	w.Header()["Content-Type"] = nil
	w.WriteHeader(http.StatusUnauthorized)
	_, _ = w.Write([]byte("Unauthorized"))
}

func writePlain(w http.ResponseWriter, status int, body string) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Content-Length", fmt.Sprint(len(body)))
	w.WriteHeader(status)
	_, _ = w.Write([]byte(body))
}

func writeExpressText(w http.ResponseWriter, status int, body string) {
	writeExpressBody(w, status, "text/html; charset=utf-8", []byte(body))
}

func writeExpressJSON(w http.ResponseWriter, status int, value any) {
	var buffer bytes.Buffer
	encoder := json.NewEncoder(&buffer)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(value); err != nil {
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	body := bytes.TrimSuffix(buffer.Bytes(), []byte{'\n'})
	writeExpressBody(w, status, "application/json; charset=utf-8", body)
}

func writeExpressBody(w http.ResponseWriter, status int, contentType string, body []byte) {
	digest := sha1.Sum(body)
	etag := fmt.Sprintf(`W/"%x-%s"`, len(body), strings.TrimRight(base64.StdEncoding.EncodeToString(digest[:]), "="))
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Content-Length", fmt.Sprint(len(body)))
	w.Header().Set("ETag", etag)
	w.WriteHeader(status)
	_, _ = w.Write(body)
}

func clearExpressCookie(w http.ResponseWriter, name string) {
	w.Header().Add("Set-Cookie", name+"=; Path=/; Expires="+time.UnixMilli(1).UTC().Format(http.TimeFormat))
}

func redirectExpress(w http.ResponseWriter, status int, target string) {
	body := http.StatusText(status) + ". Redirecting to " + target
	w.Header().Set("Location", target)
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Content-Length", fmt.Sprint(len(body)))
	w.WriteHeader(status)
	_, _ = w.Write([]byte(body))
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
			if _, err = h.db.ExecContext(ctx, `DELETE FROM sessions WHERE userId = ? AND id <> ?`, userID, s.id); err != nil {
				return "", "", err
			}
			return accessToken, refreshToken, nil
		}
	}
	_, err = h.db.ExecContext(ctx, `DELETE FROM sessions WHERE userId = ?`, userID)
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
	_, err = h.db.ExecContext(ctx, `UPDATE users SET pash = ?, updatedAt = ? WHERE id = ?`, encoded, sqlDate(time.Now()), userID)
	return err
}

// SetRefreshTokenCookie writes the standard refresh cookie used by login,
// refresh, and credential-change flows.
func (h *Handler) SetRefreshTokenCookie(w http.ResponseWriter, r *http.Request, refreshToken string) {
	setRefreshCookie(w, r, refreshToken, int(h.refreshTTL.Seconds()))
}
