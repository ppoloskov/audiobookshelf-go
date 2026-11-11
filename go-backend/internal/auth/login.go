package auth

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"audiobookshelf-compat/internal/db"
	"audiobookshelf-compat/internal/httpx"
)

// login is POST /login: username and password for the token pair. The refresh
// token goes into the refresh_token cookie, or into the answer when the client
// sends x-return-tokens: true (mobile apps).
func (s *Service) login(w http.ResponseWriter, r *http.Request) error {
	if !s.methodEnabled("local") {
		return errUnauthorized
	}
	req, err := httpx.Decode[struct{ Username, Password string }](w, r)
	if err != nil || req.Username == "" {
		return errUnauthorized
	}
	user, err := s.userByUsername(r.Context(), req.Username)
	if err == nil && !user.IsActive {
		err = errors.New("user is not active")
	}
	if err == nil {
		err = checkPassword(user, req.Password)
	}
	if err != nil {
		slog.Error("failed login attempt", "username", req.Username, "ip", httpx.ClientIP(r), "reason", err)
		return errUnauthorized
	}
	slog.Info("user logged in", "username", db.Val(user.Username), "ip", httpx.ClientIP(r))
	access, refresh, err := s.createSession(r.Context(), user, r)
	if err != nil {
		return httpx.Internal("Unable to create session", err)
	}
	payload, err := s.payloadFor(r.Context(), user)
	if err != nil {
		return httpx.Internal("Unable to load login data", err)
	}
	var returned *string
	if r.Header.Get("x-return-tokens") == "true" {
		returned = &refresh
	} else {
		s.setRefreshCookie(w, r, refresh)
	}
	return httpx.JSON(w, r, signedIn(payload, access, returned))
}

// refresh is POST /auth/refresh: it trades the refresh token (cookie, or the
// x-refresh-token header of mobile apps, which also gets the new token in the
// answer) for a new access token and rotates the session. The previous token
// keeps working for the grace period, so overlapping requests do not lock a
// client out.
func (s *Service) refresh(w http.ResponseWriter, r *http.Request) error {
	token, fromHeader := r.Header.Get("x-refresh-token"), true
	if token == "" {
		token, fromHeader = cookieValue(r, "refresh_token"), false
	}
	if token == "" {
		return jsonError(w, r, http.StatusUnauthorized, "No refresh token provided")
	}
	ctx := r.Context()
	claims, err := parse(s.secret, token, time.Now())
	switch {
	case errors.Is(err, ErrExpired):
		if _, err := s.db.Exec(ctx, `DELETE FROM sessions WHERE refreshToken = ?`, token); err != nil {
			return httpx.Internal("", err)
		}
		return jsonError(w, r, http.StatusUnauthorized, "Refresh token expired")
	case err != nil:
		return jsonError(w, r, http.StatusUnauthorized, "Invalid refresh token")
	case claims.Type != typeRefresh:
		return jsonError(w, r, http.StatusUnauthorized, "Invalid token type")
	}
	session, err := s.sessionByToken(ctx, token)
	if err != nil {
		return jsonError(w, r, http.StatusUnauthorized, "Invalid refresh token")
	}
	grace := session.RefreshToken != token // the previous token, still within its grace period
	switch {
	case grace && !session.LastRefreshTokenExpiresAt.After(time.Now()):
		return jsonError(w, r, http.StatusUnauthorized, "Invalid refresh token")
	case !grace && expiredAt(session.ExpiresAt):
		if _, err := s.db.Exec(ctx, `DELETE FROM sessions WHERE id = ?`, session.ID); err != nil {
			return httpx.Internal("", err)
		}
		return jsonError(w, r, http.StatusUnauthorized, "Refresh token expired")
	}
	user, err := s.userByID(ctx, session.UserID)
	if err != nil || !user.IsActive {
		return jsonError(w, r, http.StatusUnauthorized, "User not found or inactive")
	}
	var access, newRefresh string
	if grace {
		newRefresh = session.RefreshToken
		access, err = s.sessionToken(typeAccess, user.ID, db.Val(user.Username), s.cfg.Auth.AccessTTL)
	} else {
		access, newRefresh, err = s.rotate(ctx, session)
	}
	if err != nil {
		return httpx.Internal("Unable to rotate session", err)
	}
	payload, err := s.payloadFor(ctx, user)
	if err != nil {
		return httpx.Internal("Unable to load login data", err)
	}
	var returned *string
	if fromHeader {
		returned = &newRefresh
	}
	s.setRefreshCookie(w, r, newRefresh)
	return httpx.JSON(w, r, signedIn(payload, access, returned))
}

// logout is POST /logout: it deletes the session of the refresh token, every
// session of its user with ?allDevices=1, clears the cookies and answers the
// OpenID end-session URL to visit, or null.
func (s *Service) logout(w http.ResponseWriter, r *http.Request) error {
	refresh := cookieValue(r, "refresh_token")
	if refresh == "" {
		refresh = r.Header.Get("x-refresh-token")
	}
	method := cookieValue(r, "auth_method")
	var logoutURL *string
	if method == "openid" || method == "openid-mobile" {
		logoutURL = s.endSessionURL(r, cookieValue(r, "openid_id_token"), method)
	}
	clearCookies(w, "refresh_token", "auth_method")
	if logoutURL != nil || method == "openid" || method == "openid-mobile" {
		clearCookies(w, "openid_id_token")
	}
	if refresh != "" {
		query, args := `DELETE FROM sessions WHERE refreshToken = ?`, []any{refresh}
		if r.URL.Query().Get("allDevices") == "1" {
			query = `DELETE FROM sessions WHERE userId = (SELECT userId FROM sessions WHERE refreshToken = ? OR lastRefreshToken = ? LIMIT 1)`
			args = []any{refresh, refresh}
		}
		if _, err := s.db.Exec(r.Context(), query, args...); err != nil {
			return httpx.Internal("", err)
		}
	}
	return httpx.JSON(w, r, struct {
		RedirectURL *string `json:"redirect_url"`
	}{logoutURL})
}

// initialize is POST /init: it creates the root user of a fresh server. It
// fails (500, like upstream) once a root user exists.
func (s *Service) initialize(w http.ResponseWriter, r *http.Request) error {
	req, err := httpx.Decode[struct {
		NewRoot struct{ Username, Password string } `json:"newRoot"`
	}](w, r)
	if err != nil {
		return err
	}
	username, hash := req.NewRoot.Username, ""
	if username == "" {
		username = "root"
	}
	if req.NewRoot.Password == "" {
		slog.Warn("creating the root user with no password")
	} else if hash, err = HashPassword(req.NewRoot.Password); err != nil {
		return err
	}
	err = s.db.Tx(r.Context(), func(ctx context.Context) error {
		var roots int
		if err := s.db.Get(ctx, &roots, `SELECT COUNT(*) FROM users WHERE type = 'root'`); err != nil {
			return err
		}
		if roots > 0 {
			return httpx.Error(http.StatusInternalServerError, "")
		}
		_, err := s.CreateUser(ctx, NewUser{Username: username, PasswordHash: &hash, Type: "root", Active: true})
		return err
	})
	if err != nil {
		return err
	}
	return httpx.Status(w, http.StatusOK)
}

// authorize is POST /api/authorize: the login answer for an already
// authenticated client (the token the client holds, in the header or ?token=).
func (s *Service) authorize(w http.ResponseWriter, r *http.Request) error {
	principal, err := s.Authenticate(r)
	if err != nil {
		return errUnauthorized
	}
	payload, err := s.payloadFor(r.Context(), principal.User)
	if err != nil {
		return httpx.Internal("Unable to load authorization data", err)
	}
	return httpx.JSON(w, r, payload)
}
