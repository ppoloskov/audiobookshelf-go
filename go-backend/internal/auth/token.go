// Package auth contains read-only JWT validation primitives for the Go server.
//
// This package validates bearer token structure, HMAC signatures, and standard
// time claims. It does not issue tokens, authenticate logins, look up users or
// API keys, or authorize database access; callers must perform those checks.
package auth

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"hash"
	"io"
	"math"
	"strings"
	"time"
)

var (
	ErrInvalidToken = errors.New("invalid token")
	ErrExpired      = errors.New("token expired")
	ErrNotYetValid  = errors.New("token not yet valid")
)

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

// ValidateBearer checks a compact JWT signed using one of jsonwebtoken's
// default symmetric algorithms (HS256, HS384, or HS512). It rejects refresh
// credentials because they cannot authenticate bearer requests in upstream.
// API-key token types are returned for the caller to resolve and authorize.
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
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return empty, ErrInvalidToken
	}

	headerBytes, err := decodeSegment(parts[0])
	if err != nil {
		return empty, ErrInvalidToken
	}
	var header map[string]any
	if err := decodeJSONObject(headerBytes, &header); err != nil {
		return empty, ErrInvalidToken
	}
	alg, ok := header["alg"].(string)
	if !ok {
		return empty, ErrInvalidToken
	}
	newHash, err := hashForAlgorithm(alg)
	if err != nil {
		return empty, err
	}

	signature, err := decodeSegment(parts[2])
	if err != nil {
		return empty, ErrInvalidToken
	}
	mac := hmac.New(newHash, secret)
	_, _ = mac.Write([]byte(parts[0] + "." + parts[1]))
	if !hmac.Equal(signature, mac.Sum(nil)) {
		return empty, ErrInvalidToken
	}

	payloadBytes, err := decodeSegment(parts[1])
	if err != nil {
		return empty, ErrInvalidToken
	}
	var payload map[string]any
	if err := decodeJSONObject(payloadBytes, &payload); err != nil {
		return empty, ErrInvalidToken
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
	for _, dateClaim := range []struct {
		name string
		dest **time.Time
	}{{"exp", &claims.ExpiresAt}, {"nbf", &claims.NotBefore}, {"iat", &claims.IssuedAt}} {
		seconds, present, err := numericDate(payload, dateClaim.name)
		if err != nil {
			return empty, err
		}
		if present {
			t := time.Unix(int64(math.Floor(seconds)), fractionalNanos(seconds)).UTC()
			*dateClaim.dest = &t
		}
		if enforceExpiration && dateClaim.name == "exp" && present && nowUnixSeconds(now) >= seconds {
			return empty, ErrExpired
		}
		if dateClaim.name == "nbf" && present && seconds > nowUnixSeconds(now) {
			return empty, ErrNotYetValid
		}
	}
	return claims, nil
}

func optionalString(payload map[string]any, key string) (string, bool) {
	v, exists := payload[key]
	if !exists {
		return "", true
	}
	s, ok := v.(string)
	return s, ok
}

func numericDate(payload map[string]any, key string) (float64, bool, error) {
	v, present := payload[key]
	if !present {
		return 0, false, nil
	}
	n, ok := v.(json.Number)
	if !ok {
		return 0, true, fmt.Errorf("%w: invalid %s value", ErrInvalidToken, key)
	}
	seconds, err := n.Float64()
	if err != nil || math.IsNaN(seconds) || math.IsInf(seconds, 0) || math.Abs(seconds) >= float64(math.MaxInt64) {
		return 0, true, fmt.Errorf("%w: invalid %s value", ErrInvalidToken, key)
	}
	return seconds, true, nil
}

func nowUnixSeconds(now time.Time) float64 {
	return float64(now.Unix()) + float64(now.Nanosecond())/1e9
}

func fractionalNanos(seconds float64) int64 {
	fraction := seconds - math.Floor(seconds)
	return int64(math.Round(fraction * 1e9))
}

func decodeSegment(segment string) ([]byte, error) {
	if segment == "" || strings.Contains(segment, "=") {
		return nil, ErrInvalidToken
	}
	b, err := base64.RawURLEncoding.DecodeString(segment)
	if err != nil || base64.RawURLEncoding.EncodeToString(b) != segment {
		return nil, ErrInvalidToken
	}
	return b, nil
}

func decodeJSONObject(data []byte, dest *map[string]any) error {
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	decoder.UseNumber()
	if err := decoder.Decode(dest); err != nil || *dest == nil {
		return ErrInvalidToken
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return ErrInvalidToken
	}
	return nil
}

func hashForAlgorithm(alg string) (func() hash.Hash, error) {
	switch alg {
	case "HS256":
		return sha256.New, nil
	case "HS384":
		return sha512.New384, nil
	case "HS512":
		return sha512.New, nil
	default:
		return nil, fmt.Errorf("%w: unsupported signing algorithm", ErrInvalidToken)
	}
}
