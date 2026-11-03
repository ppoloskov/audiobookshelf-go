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

	"github.com/golang-jwt/jwt/v5"
)

var (
	ErrInvalidToken = errors.New("invalid token")
	ErrExpired      = errors.New("token expired")
	ErrNotYetValid  = errors.New("token not yet valid")
)

// signingAlgorithm is the only algorithm upstream issues and the only one this
// package accepts.
const signingAlgorithm = "HS256"

// Claims contains the commonly used Audiobookshelf token fields. Raw retains
// all decoded payload claims, with JSON numbers represented as json.Number.
// A nil ExpiresAt means the token has no exp claim, which is valid for legacy
// access tokens and API keys; callers decide whether that is acceptable.
type Claims struct {
	UserID    string
	Username  string
	Type      string
	JTI       string
	KeyID     string
	ExpiresAt *time.Time
	NotBefore *time.Time
	IssuedAt  *time.Time
	Raw       map[string]any
}

// Sign issues an HS256 token over payload with secret. The caller sets iat,
// exp and the other claims; Sign adds none.
func Sign(secret []byte, payload map[string]any) (string, error) {
	if len(secret) == 0 {
		return "", errors.New("auth: empty signing secret")
	}
	return jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims(payload)).SignedString(secret)
}

// ValidateBearer checks a compact HS256 JWT. It rejects refresh credentials
// because they cannot authenticate bearer requests in upstream. API-key token
// types are returned for the caller to resolve and authorize.
func ValidateBearer(token string, secret []byte, now time.Time) (Claims, error) {
	claims, err := ValidateToken(token, secret, now)
	if err != nil {
		return Claims{}, err
	}
	if claims.Type == "refresh" {
		return Claims{}, fmt.Errorf("%w: refresh token is not a bearer access token", ErrInvalidToken)
	}
	return claims, nil
}

// ValidateToken validates a signed token without enforcing its purpose. Use
// ValidateBearer for credentials presented to API/socket bearer auth; refresh
// endpoints should call this and then explicitly require Type == "refresh".
func ValidateToken(token string, secret []byte, now time.Time) (Claims, error) {
	return validateToken(token, secret, now, true)
}

// ValidateTokenIgnoringExpiration verifies signature and all claim formats but
// leaves exp enforcement to the caller. This matches upstream's API-key path,
// which resolves the database key and deactivates it when expired.
func ValidateTokenIgnoringExpiration(token string, secret []byte, now time.Time) (Claims, error) {
	return validateToken(token, secret, now, false)
}

func validateToken(token string, secret []byte, now time.Time, enforceExpiration bool) (Claims, error) {
	var empty Claims
	if token == "" || len(secret) == 0 {
		return empty, ErrInvalidToken
	}
	options := []jwt.ParserOption{
		jwt.WithValidMethods([]string{signingAlgorithm}),
		jwt.WithJSONNumber(),
		jwt.WithTimeFunc(func() time.Time { return now }),
	}
	if !enforceExpiration {
		options = append(options, jwt.WithoutClaimsValidation())
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

	claims := Claims{UserID: userID, Username: username, Type: tokenType, JTI: jti, KeyID: keyID, Raw: payload}
	for _, date := range []struct {
		get  func() (*jwt.NumericDate, error)
		dest **time.Time
	}{{payload.GetExpirationTime, &claims.ExpiresAt}, {payload.GetNotBefore, &claims.NotBefore}, {payload.GetIssuedAt, &claims.IssuedAt}} {
		d, err := date.get()
		if err != nil {
			return empty, fmt.Errorf("%w: invalid date claim: %v", ErrInvalidToken, err)
		}
		if d != nil {
			t := d.Time
			*date.dest = &t
		}
	}
	// The library's claim validation covers nbf and exp; when exp is left to the
	// caller it is skipped, so nbf is checked here.
	if !enforceExpiration && claims.NotBefore != nil && now.Before(*claims.NotBefore) {
		return empty, ErrNotYetValid
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
