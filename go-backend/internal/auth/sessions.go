package auth

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"audiobookshelf-compat/internal/db"
	"audiobookshelf-compat/internal/httpx"
	"audiobookshelf-compat/internal/ids"
)

// createSession starts a session for user and returns its access and refresh
// tokens. The refresh token is the session's credential; it lives in
// sessions.refreshToken.
func (s *Service) createSession(ctx context.Context, user db.User, r *http.Request) (access, refresh string, err error) {
	name := db.Val(user.Username)
	if access, err = s.sessionToken(typeAccess, user.ID, name, s.cfg.Auth.AccessTTL); err != nil {
		return "", "", err
	}
	if refresh, err = s.sessionToken(typeRefresh, user.ID, name, s.cfg.Auth.RefreshTTL); err != nil {
		return "", "", err
	}
	id, err := ids.New()
	if err != nil {
		return "", "", fmt.Errorf("session id: %w", err)
	}
	now := db.Now()
	ip, agent := httpx.ClientIP(r), r.UserAgent()
	err = s.db.Insert(ctx, "sessions", &db.Session{
		ID: id, IPAddress: &ip, UserAgent: &agent, RefreshToken: refresh,
		ExpiresAt: db.Time{Time: now.Add(s.cfg.Auth.RefreshTTL)}, CreatedAt: now, UpdatedAt: now, UserID: user.ID,
	})
	return access, refresh, err
}

// sessionByToken finds the session a refresh token belongs to, as its current
// token or as the previous one still in its grace period.
func (s *Service) sessionByToken(ctx context.Context, token string) (db.Session, error) {
	var session db.Session
	err := s.db.Get(ctx, &session, `SELECT * FROM sessions WHERE refreshToken = ? OR lastRefreshToken = ? LIMIT 1`, token, token)
	return session, err
}

// rotate replaces the refresh token of session with a new one; the old token
// stays valid for the grace period. The update only applies while the session
// still holds the token it was loaded with, so of two concurrent refreshes the
// second returns the token the first stored. It returns a fresh access token
// and the session's current refresh token.
func (s *Service) rotate(ctx context.Context, session db.Session) (access, refresh string, err error) {
	user, err := s.userByID(ctx, session.UserID)
	if err != nil {
		return "", "", err
	}
	name := db.Val(user.Username)
	if access, err = s.sessionToken(typeAccess, user.ID, name, s.cfg.Auth.AccessTTL); err != nil {
		return "", "", err
	}
	if refresh, err = s.sessionToken(typeRefresh, user.ID, name, s.cfg.Auth.RefreshTTL); err != nil {
		return "", "", err
	}
	now := db.Now()
	result, err := s.db.Exec(ctx, `UPDATE sessions SET refreshToken = ?, expiresAt = ?, lastRefreshToken = ?, lastRefreshTokenExpiresAt = ?, updatedAt = ?
WHERE id = ? AND refreshToken = ?`, refresh, db.Time{Time: now.Add(s.cfg.Auth.RefreshTTL)}, session.RefreshToken,
		db.Time{Time: now.Add(s.cfg.Auth.RefreshGrace)}, now, session.ID, session.RefreshToken)
	if err != nil {
		return "", "", err
	}
	if n, _ := result.RowsAffected(); n == 0 { // lost the race; the error is irrelevant to the fallback below
		err = s.db.Get(ctx, &refresh, `SELECT refreshToken FROM sessions WHERE id = ?`, session.ID)
	}
	return access, refresh, err
}

// InvalidateUserSessions deletes every session of userID after a credential
// change. When currentRefreshToken names one of the user's sessions that
// session survives with rotated tokens, which are returned; otherwise all go
// and the returned tokens are empty.
func (s *Service) InvalidateUserSessions(ctx context.Context, userID, currentRefreshToken string) (access, refresh string, err error) {
	if currentRefreshToken != "" {
		if session, err := s.sessionByToken(ctx, currentRefreshToken); err == nil && session.UserID == userID {
			if access, refresh, err = s.rotate(ctx, session); err != nil {
				return "", "", err
			}
			_, err = s.db.Exec(ctx, `DELETE FROM sessions WHERE userId = ? AND id <> ?`, userID, session.ID)
			return access, refresh, err
		}
	}
	_, err = s.db.Exec(ctx, `DELETE FROM sessions WHERE userId = ?`, userID)
	return "", "", err
}

// Sessions lists the unexpired sessions of a user, newest activity first, and
// their total count.
func (s *Service) Sessions(ctx context.Context, userID string, page, perPage int) (total int, sessions []db.Session, err error) {
	now := db.Now()
	if err = s.db.Get(ctx, &total, `SELECT COUNT(*) FROM sessions WHERE userId = ? AND expiresAt > ?`, userID, now); err != nil {
		return 0, nil, err
	}
	err = s.db.Select(ctx, &sessions, `SELECT * FROM sessions WHERE userId = ? AND expiresAt > ? ORDER BY updatedAt DESC LIMIT ? OFFSET ?`,
		userID, now, perPage, perPage*page)
	return total, sessions, err
}

// DeleteSession deletes one session of a user and reports whether it existed.
func (s *Service) DeleteSession(ctx context.Context, userID, sessionID string) (bool, error) {
	result, err := s.db.Exec(ctx, `DELETE FROM sessions WHERE id = ? AND userId = ?`, sessionID, userID)
	if err != nil {
		return false, err
	}
	n, _ := result.RowsAffected() // sqlite always reports it
	return n > 0, nil
}

// expiredAt reports a deadline that has passed.
func expiredAt(t db.Time) bool { return !t.After(time.Now()) }
