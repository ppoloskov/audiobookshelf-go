// Package auth issues and verifies the JWTs that the TS server also accepts:
// HS256 over one shared secret, with upstream's claim names. It is the only
// package that signs or verifies tokens.
//
// It does not authenticate logins, look up users or API keys, or authorize
// database access; callers must perform those checks.
package auth

import (
	"errors"
	"fmt"
	"time"

	"audiobookshelf-compat/internal/ids"

	"github.com/golang-jwt/jwt/v5"
)

var (
	ErrInvalidToken = errors.New("invalid token")
	ErrExpired      = errors.New("token expired")
	ErrNotYetValid  = errors.New("token not yet valid")
	ErrWrongType    = errors.New("wrong token type")
)

// signingAlgorithm is the only algorithm upstream issues and the only one this
// package accepts.
const signingAlgorithm = "HS256"

// Claims contains the token fields callers use. A nil ExpiresAt means the
// token has no exp claim, which is valid for legacy access tokens and API keys.
type Claims struct {
	UserID    string
	Username  string
	Type      string
	JTI       string
	KeyID     string
	ExpiresAt *time.Time
	IssuedAt  *time.Time
}

// sign issues an HS256 token over payload with secret.
func sign(secret []byte, payload map[string]any) (string, error) {
	if len(secret) == 0 {
		return "", errors.New("auth: empty signing secret")
	}
	return jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims(payload)).SignedString(secret)
}

// IssueAccess and IssueRefresh issue the session credentials of
// TokenManager.generateAccessToken/generateRefreshToken (TokenManager.js:130-165).
func IssueAccess(secret []byte, userID, username string, ttl time.Duration) (string, error) {
	return issueSession(secret, userID, username, "access", ttl)
}

func IssueRefresh(secret []byte, userID, username string, ttl time.Duration) (string, error) {
	return issueSession(secret, userID, username, "refresh", ttl)
}

func issueSession(secret []byte, userID, username, kind string, ttl time.Duration) (string, error) {
	jti, err := ids.New()
	if err != nil {
		return "", err
	}
	now := time.Now().Unix()
	return sign(secret, map[string]any{"userId": userID, "username": username, "jti": jti, "type": kind, "iat": now, "exp": now + int64(ttl.Seconds())})
}

// IssueLegacy issues the permanent user token stored in users.token: no type,
// no exp.
func IssueLegacy(secret []byte, userID, username string) (string, error) {
	return sign(secret, map[string]any{"userId": userID, "username": username, "iat": time.Now().Unix()})
}

// IssueAPIKey issues an API key credential. expiresAtUnix is the exp claim, or
// 0 for a key that does not expire.
func IssueAPIKey(secret []byte, keyID, name string, expiresAtUnix int64) (string, error) {
	claims := map[string]any{"keyId": keyID, "name": name, "type": "api", "iat": time.Now().Unix()}
	if expiresAtUnix > 0 {
		claims["exp"] = expiresAtUnix
	}
	return sign(secret, claims)
}

// ValidateBearer checks a compact HS256 JWT presented to API or socket auth,
// including its exp. Refresh credentials are rejected (TokenManager.js:81-84);
// API-key types are returned for the caller to resolve and authorize.
func ValidateBearer(token string, secret []byte, now time.Time) (Claims, error) {
	claims, err := validateToken(token, secret, now)
	if err != nil {
		return Claims{}, err
	}
	if claims.Type == "refresh" {
		return Claims{}, fmt.Errorf("%w: refresh token is not a bearer access token", ErrInvalidToken)
	}
	return claims, nil
}

// ValidateRefresh checks a refresh credential, including its exp
// (TokenManager.js:353-358). Any other token type is ErrWrongType.
func ValidateRefresh(token string, secret []byte, now time.Time) (Claims, error) {
	claims, err := validateToken(token, secret, now)
	if err != nil {
		return Claims{}, err
	}
	if claims.Type != "refresh" {
		return claims, ErrWrongType
	}
	return claims, nil
}

func validateToken(token string, secret []byte, now time.Time) (Claims, error) {
	var empty Claims
	if token == "" || len(secret) == 0 {
		return empty, ErrInvalidToken
	}
	options := []jwt.ParserOption{
		jwt.WithValidMethods([]string{signingAlgorithm}),
		jwt.WithJSONNumber(),
		jwt.WithTimeFunc(func() time.Time { return now }),
	}
	payload := jwt.MapClaims{}
	keyFunc := func(*jwt.Token) (any, error) { return secret, nil }
	if _, err := jwt.NewParser(options...).ParseWithClaims(token, payload, keyFunc); err != nil {
		return empty, parseError(err)
	}

	username, ok := optionalString(payload, "username")
	if !ok {
		return empty, fmt.Errorf("%w: invalid username", ErrInvalidToken)
	}
	tokenType, ok := optionalString(payload, "type")
	if !ok {
		return empty, fmt.Errorf("%w: invalid type", ErrInvalidToken)
	}
	jti, ok := optionalString(payload, "jti")
	if !ok {
		return empty, fmt.Errorf("%w: invalid jti", ErrInvalidToken)
	}
	userID, ok := optionalString(payload, "userId")
	if !ok {
		return empty, fmt.Errorf("%w: invalid userId", ErrInvalidToken)
	}
	keyID, ok := optionalString(payload, "keyId")
	if !ok {
		return empty, fmt.Errorf("%w: invalid keyId", ErrInvalidToken)
	}
	if userID == "" && tokenType != "refresh" && !((tokenType == "api" || tokenType == "apiKey") && keyID != "") {
		return empty, fmt.Errorf("%w: missing or invalid userId", ErrInvalidToken)
	}

	claims := Claims{UserID: userID, Username: username, Type: tokenType, JTI: jti, KeyID: keyID}
	for _, date := range []struct {
		get  func() (*jwt.NumericDate, error)
		dest **time.Time
	}{{payload.GetExpirationTime, &claims.ExpiresAt}, {payload.GetIssuedAt, &claims.IssuedAt}} {
		d, err := date.get()
		if err != nil {
			return empty, fmt.Errorf("%w: invalid date claim: %v", ErrInvalidToken, err)
		}
		if d != nil {
			t := d.Time
			*date.dest = &t
		}
	}
	return claims, nil
}

// parseError maps the library's errors onto this package's sentinels. Every
// other failure is an invalid token.
func parseError(err error) error {
	switch {
	case errors.Is(err, jwt.ErrTokenExpired):
		return ErrExpired
	case errors.Is(err, jwt.ErrTokenNotValidYet):
		return ErrNotYetValid
	default:
		return fmt.Errorf("%w: %v", ErrInvalidToken, err)
	}
}

func optionalString(payload jwt.MapClaims, key string) (string, bool) {
	v, exists := payload[key]
	if !exists {
		return "", true
	}
	s, ok := v.(string)
	return s, ok
}
