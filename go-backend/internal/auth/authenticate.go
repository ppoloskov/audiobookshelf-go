package auth

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"audiobookshelf-compat/internal/db"
	"audiobookshelf-compat/internal/httpx"
)

// Authenticate resolves the credential of r to its active user. The
// credential is a bearer token or the ?token= query parameter: an access
// token, a legacy user token or an API key. Refresh tokens are not accepted. A
// failure is ErrExpired for an expired token; every other message is for logs
// and socket auth only.
func (s *Service) Authenticate(r *http.Request) (*Principal, error) {
	claims, err := parse(s.secret, requestToken(r), time.Now())
	if err != nil {
		return nil, err
	}
	if claims.Type == typeRefresh {
		return nil, errInvalidToken
	}
	var user db.User
	if claims.isAPIKey() {
		user, err = s.apiKeyUser(r.Context(), claims.KeyID)
	} else {
		user, err = s.userByIDOrOldID(r.Context(), claims.UserID)
		if err != nil || !user.IsActive {
			err = errNoUser
		}
	}
	if err != nil {
		return nil, err
	}
	return NewPrincipal(user), nil
}

// Middleware authenticates every request with Authenticate, answers 401 when
// that fails and otherwise runs next with the principal in the context.
func (s *Service) Middleware(next http.Handler) http.Handler {
	return httpx.Handler(func(w http.ResponseWriter, r *http.Request) error {
		principal, err := s.Authenticate(r)
		if err != nil {
			return errUnauthorized
		}
		next.ServeHTTP(w, r.WithContext(WithUser(r.Context(), principal)))
		return nil
	})
}

// requestToken is the bearer token of the Authorization header, else ?token=.
func requestToken(r *http.Request) string {
	if h := r.Header.Get("Authorization"); len(h) >= 7 && strings.EqualFold(h[:7], "Bearer ") {
		return strings.TrimSpace(h[7:])
	}
	return r.URL.Query().Get("token")
}

// apiKeyUser resolves an API key to its owner. The key must be active and
// unexpired and so must its owner. Expired keys are only refused: upstream
// deactivates them in a separate job, never while authenticating.
func (s *Service) apiKeyUser(ctx context.Context, keyID string) (db.User, error) {
	key, err := s.APIKey(ctx, keyID)
	if err != nil || !key.IsActive || db.Val(key.UserID) == "" {
		return db.User{}, errors.New("auth: invalid API key")
	}
	if !key.ExpiresAt.IsZero() && !key.ExpiresAt.After(time.Now()) {
		return db.User{}, errors.New("auth: expired API key")
	}
	user, err := s.userByID(ctx, *key.UserID)
	if err != nil || !user.IsActive {
		return db.User{}, errors.New("auth: inactive or missing API key user")
	}
	return user, nil
}
