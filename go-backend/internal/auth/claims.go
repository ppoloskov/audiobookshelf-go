package auth

import (
	"errors"
	"fmt"
	"time"

	"audiobookshelf-compat/internal/ids"

	"github.com/golang-jwt/jwt/v5"
)

// Credential kinds, the "type" claim of TokenManager.js. Legacy user tokens
// (users.token) have no type at all.
const (
	typeAccess  = "access"
	typeRefresh = "refresh"
	typeAPI     = "api"
	typeAPIKey  = "apiKey" // older spelling of typeAPI that is still accepted
)

// ErrExpired reports a token whose exp has passed. Socket auth words its
// failure message from it.
var ErrExpired = errors.New("token expired")

// errInvalidToken wraps every other token failure; errWrongType is a valid
// token of the wrong kind (an access token presented for refresh).
var (
	errInvalidToken = errors.New("invalid token")
	errWrongType    = errors.New("wrong token type")
)

// Claims is the payload of every credential the server signs, in the layout
// the TypeScript server (and so every stored token) uses: HS256 over one
// secret. Access and refresh tokens carry the user and a jti, API keys carry
// keyId and name, legacy user tokens carry only userId, username and iat.
// Dates are the standard registered claims, so a missing exp means the token
// does not expire.
type Claims struct {
	jwt.RegisteredClaims
	UserID   string `json:"userId,omitempty"`
	Username string `json:"username,omitempty"`
	Type     string `json:"type,omitempty"`
	KeyID    string `json:"keyId,omitempty"`
	Name     string `json:"name,omitempty"`
}

func (c Claims) isAPIKey() bool { return c.Type == typeAPI || c.Type == typeAPIKey }

// sign issues the HS256 token for claims.
func sign(secret []byte, claims Claims) (string, error) {
	return jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString(secret)
}

// parse verifies token at time now: signature, HS256 only, exp and nbf. A
// token must name a user, except an API key, which names its key.
func parse(secret []byte, token string, now time.Time) (Claims, error) {
	var claims Claims
	if token == "" {
		return claims, errInvalidToken
	}
	_, err := jwt.NewParser(jwt.WithValidMethods([]string{"HS256"}), jwt.WithTimeFunc(func() time.Time { return now })).
		ParseWithClaims(token, &claims, func(*jwt.Token) (any, error) { return secret, nil })
	switch {
	case errors.Is(err, jwt.ErrTokenExpired):
		return Claims{}, ErrExpired
	case err != nil:
		return Claims{}, fmt.Errorf("%w: %v", errInvalidToken, err)
	case claims.UserID == "" && claims.Type != typeRefresh && !(claims.isAPIKey() && claims.KeyID != ""):
		return Claims{}, fmt.Errorf("%w: no userId", errInvalidToken)
	}
	return claims, nil
}

// sessionToken issues an access or refresh token for a user.
func (s *Service) sessionToken(kind, userID, username string, ttl time.Duration) (string, error) {
	jti, err := ids.New()
	if err != nil {
		return "", fmt.Errorf("token id: %w", err)
	}
	now := time.Now()
	return sign(s.secret, Claims{
		RegisteredClaims: jwt.RegisteredClaims{ID: jti, IssuedAt: jwt.NewNumericDate(now), ExpiresAt: jwt.NewNumericDate(now.Add(ttl))},
		UserID:           userID, Username: username, Type: kind,
	})
}

// legacyToken issues the permanent user token stored in users.token: no type,
// no exp.
func (s *Service) legacyToken(userID, username string) (string, error) {
	return sign(s.secret, Claims{RegisteredClaims: jwt.RegisteredClaims{IssuedAt: jwt.NewNumericDate(time.Now())}, UserID: userID, Username: username})
}

// LegacyToken is legacyToken for the user administration, which re-issues the
// token of a renamed user.
func (s *Service) LegacyToken(userID, username string) (string, error) {
	return s.legacyToken(userID, username)
}

// apiKeyToken issues the bearer token of an API key; a zero expires never
// expires.
func (s *Service) apiKeyToken(keyID, name string, expires time.Time) (string, error) {
	claims := Claims{RegisteredClaims: jwt.RegisteredClaims{IssuedAt: jwt.NewNumericDate(time.Now())}, KeyID: keyID, Name: name, Type: typeAPI}
	if !expires.IsZero() {
		claims.ExpiresAt = jwt.NewNumericDate(expires)
	}
	return sign(s.secret, claims)
}
