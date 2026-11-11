package auth

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

// Vectors captured from the old signers and from the TypeScript server's
// jsonwebtoken (TokenManager.js) with the same payloads.
const (
	vectorSecret    = "vector-secret-0123456789"
	goAccessVector  = "eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9.eyJleHAiOjE4MDAwMDM2MDAsImlhdCI6MTgwMDAwMDAwMCwianRpIjoianRpLTEiLCJ0eXBlIjoiYWNjZXNzIiwidXNlcklkIjoidXNlci0xIiwidXNlcm5hbWUiOiJyZWFkZXIifQ.tOoDGdLsXy2UJX5YS_FoKjNBFKQzk5nP8MmiuKoDgH4"
	goRefreshVector = "eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9.eyJleHAiOjE4MDI1OTIwMDAsImlhdCI6MTgwMDAwMDAwMCwianRpIjoianRpLTIiLCJ0eXBlIjoicmVmcmVzaCIsInVzZXJJZCI6InVzZXItMSIsInVzZXJuYW1lIjoicmVhZGVyIn0.WAMzu-rVnLRjh6jnlrZolYFuWxtMGMIwws-clyCm9JA"
	goLegacyVector  = "eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9.eyJpYXQiOjE3MDAwMDAwMDAsInVzZXJJZCI6InVzZXItMSIsInVzZXJuYW1lIjoicmVhZGVyIn0.Kh-9L3p4D74Qxy5BJ0JCVr3fxZxEL_Pzz7eYVf_5mvM"
	goAPIKeyVector  = "eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9.eyJleHAiOjE4MDAwMDM2MDAsImlhdCI6MTgwMDAwMDAwMCwia2V5SWQiOiJrZXktMSIsIm5hbWUiOiJjaSIsInR5cGUiOiJhcGkifQ.JEe4gSNdggISBFCiy9IEvuW0fenxScW8f4S_x1Zs0Ko"
	tsAccessVector  = "eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9.eyJ1c2VySWQiOiJ1c2VyLTEiLCJ1c2VybmFtZSI6InJlYWRlciIsImp0aSI6Imp0aS0xIiwidHlwZSI6ImFjY2VzcyIsImlhdCI6MTgwMDAwMDAwMCwiZXhwIjoxODAwMDAzNjAwfQ.C4dBP0QkgMOxi4J0MW0Xe0IPC4_b9uqKmyQeaRmXynE"
	tsRefreshVector = "eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9.eyJ1c2VySWQiOiJ1c2VyLTEiLCJ1c2VybmFtZSI6InJlYWRlciIsImp0aSI6Imp0aS0yIiwidHlwZSI6InJlZnJlc2giLCJpYXQiOjE4MDAwMDAwMDAsImV4cCI6MTgwMjU5MjAwMH0.1NXVpnXxXWyAYVsj4vhw0pBT66EQMvhe3nNlmf2Pbco"
	tsLegacyVector  = "eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9.eyJ1c2VySWQiOiJ1c2VyLTEiLCJ1c2VybmFtZSI6InJlYWRlciIsImlhdCI6MTcwMDAwMDAwMH0.NLS-dhRKu1hHCGWg-PFJf7JDDV5cBkDZZmsRp201Z0A"
)

var vectorNow = time.Unix(1800000100, 0)

func TestUpstreamAndGoVectorsVerify(t *testing.T) {
	for _, tc := range []struct {
		name, token, wantType, wantJTI string
		wantExp, wantIat               int64 // exp 0: none
	}{
		{"go access", goAccessVector, "access", "jti-1", 1800003600, 1800000000},
		{"go refresh", goRefreshVector, "refresh", "jti-2", 1802592000, 1800000000},
		{"go legacy", goLegacyVector, "", "", 0, 1700000000},
		{"go api key", goAPIKeyVector, "api", "", 1800003600, 1800000000},
		{"ts access", tsAccessVector, "access", "jti-1", 1800003600, 1800000000},
		{"ts refresh", tsRefreshVector, "refresh", "jti-2", 1802592000, 1800000000},
		{"ts legacy", tsLegacyVector, "", "", 0, 1700000000},
	} {
		t.Run(tc.name, func(t *testing.T) {
			claims, err := parse([]byte(vectorSecret), tc.token, vectorNow)
			if err != nil {
				t.Fatal(err)
			}
			if claims.Type != tc.wantType || claims.ID != tc.wantJTI || claims.IssuedAt.Unix() != tc.wantIat {
				t.Fatalf("claims = %+v", claims)
			}
			if (claims.ExpiresAt == nil) != (tc.wantExp == 0) || (tc.wantExp != 0 && claims.ExpiresAt.Unix() != tc.wantExp) {
				t.Fatalf("exp = %v, want %d", claims.ExpiresAt, tc.wantExp)
			}
		})
	}
}

func forge(t *testing.T, alg string, secret []byte, payload any) string {
	t.Helper()
	header, _ := json.Marshal(map[string]string{"alg": alg, "typ": "JWT"})
	body, _ := json.Marshal(payload)
	input := base64.RawURLEncoding.EncodeToString(header) + "." + base64.RawURLEncoding.EncodeToString(body)
	switch alg {
	case "none":
		return input + "."
	case "HS384":
		mac := hmac.New(sha512.New384, secret)
		mac.Write([]byte(input))
		return input + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	}
	mac := hmac.New(sha256.New, secret)
	mac.Write([]byte(input))
	return input + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

func TestParseRejectsBadTokens(t *testing.T) {
	secret := []byte("secret")
	good := map[string]any{"userId": "u", "username": "reader", "type": "access"}
	parts := strings.Split(goAccessVector, ".")
	tampered := parts[0] + "." + base64.RawURLEncoding.EncodeToString([]byte(`{"userId":"admin","type":"access"}`)) + "." + parts[2]
	for _, tc := range []struct {
		name, token string
		secret      []byte
		now         time.Time
		want        error
	}{
		{"expired at the boundary", goAccessVector, []byte(vectorSecret), time.Unix(1800003600, 0), ErrExpired},
		{"expired API key", goAPIKeyVector, []byte(vectorSecret), time.Unix(1800003601, 0), ErrExpired},
		{"tampered payload", tampered, []byte(vectorSecret), vectorNow, errInvalidToken},
		{"wrong secret", goAccessVector, secret, vectorNow, errInvalidToken},
		{"alg none", forge(t, "none", nil, good), secret, vectorNow, errInvalidToken},
		{"HS384", forge(t, "HS384", secret, good), secret, vectorNow, errInvalidToken},
		{"empty", "", secret, vectorNow, errInvalidToken},
		{"no userId", forge(t, "HS256", secret, map[string]any{"username": "reader"}), secret, vectorNow, errInvalidToken},
		{"numeric userId", forge(t, "HS256", secret, map[string]any{"userId": 42}), secret, vectorNow, errInvalidToken},
		{"string exp", forge(t, "HS256", secret, map[string]any{"userId": "u", "exp": "soon"}), secret, vectorNow, errInvalidToken},
		{"not yet valid", forge(t, "HS256", secret, map[string]any{"userId": "u", "nbf": vectorNow.Unix() + 10}), secret, vectorNow, errInvalidToken},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := parse(tc.secret, tc.token, tc.now); !errors.Is(err, tc.want) {
				t.Fatalf("err = %v, want %v", err, tc.want)
			}
		})
	}
	// An API key names its key, not a user.
	claims, err := parse(secret, forge(t, "HS256", secret, map[string]any{"type": "apiKey", "keyId": "k"}), vectorNow)
	if err != nil || !claims.isAPIKey() || claims.KeyID != "k" {
		t.Fatalf("API key claims = %+v, %v", claims, err)
	}
}

func TestIssuedTokensRoundTrip(t *testing.T) {
	s := &Service{secret: []byte("secret")}
	access, _ := s.sessionToken(typeAccess, "u", "reader", time.Hour)
	legacy, _ := s.legacyToken("u", "reader")
	key, _ := s.apiKeyToken("k", "name", time.Time{})
	for token, want := range map[string]Claims{access: {Type: typeAccess, UserID: "u"}, legacy: {UserID: "u"}, key: {Type: typeAPI, KeyID: "k"}} {
		claims, err := parse(s.secret, token, time.Now())
		if err != nil || claims.Type != want.Type || claims.UserID != want.UserID || claims.KeyID != want.KeyID {
			t.Fatalf("claims = %+v, %v; want %+v", claims, err, want)
		}
	}
	if c, _ := parse(s.secret, legacy, time.Now()); c.ExpiresAt != nil || c.IssuedAt == nil {
		t.Fatalf("legacy token must have iat and no exp: %+v", c)
	}
	if c, _ := parse(s.secret, key, time.Now()); c.ExpiresAt != nil {
		t.Fatal("API key without expiry has an exp")
	}
}
