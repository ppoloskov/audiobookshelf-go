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

// Vectors below were captured from the pre-library signers before they were
// deleted: identity.sign (access, refresh, legacy, API key payloads with fixed
// iat/exp) and userapi.signToken (legacy shape, iat set at capture time). The
// TS vectors were issued by server/libs/jsonwebtoken (the vendored library
// TokenManager.js calls) with the same payloads and expiresIn values.
const (
	vectorSecret = "vector-secret-0123456789"

	goAccessVector  = "eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9.eyJleHAiOjE4MDAwMDM2MDAsImlhdCI6MTgwMDAwMDAwMCwianRpIjoianRpLTEiLCJ0eXBlIjoiYWNjZXNzIiwidXNlcklkIjoidXNlci0xIiwidXNlcm5hbWUiOiJyZWFkZXIifQ.tOoDGdLsXy2UJX5YS_FoKjNBFKQzk5nP8MmiuKoDgH4"
	goRefreshVector = "eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9.eyJleHAiOjE4MDI1OTIwMDAsImlhdCI6MTgwMDAwMDAwMCwianRpIjoianRpLTIiLCJ0eXBlIjoicmVmcmVzaCIsInVzZXJJZCI6InVzZXItMSIsInVzZXJuYW1lIjoicmVhZGVyIn0.WAMzu-rVnLRjh6jnlrZolYFuWxtMGMIwws-clyCm9JA"
	goLegacyVector  = "eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9.eyJpYXQiOjE3MDAwMDAwMDAsInVzZXJJZCI6InVzZXItMSIsInVzZXJuYW1lIjoicmVhZGVyIn0.Kh-9L3p4D74Qxy5BJ0JCVr3fxZxEL_Pzz7eYVf_5mvM"
	goAPIKeyVector  = "eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9.eyJleHAiOjE4MDAwMDM2MDAsImlhdCI6MTgwMDAwMDAwMCwia2V5SWQiOiJrZXktMSIsIm5hbWUiOiJjaSIsInR5cGUiOiJhcGkifQ.JEe4gSNdggISBFCiy9IEvuW0fenxScW8f4S_x1Zs0Ko"
	goUserAPIVector = "eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9.eyJpYXQiOjE3OTM3MzgxOTQsInVzZXJJZCI6InVzZXItMSIsInVzZXJuYW1lIjoicmVhZGVyIn0.42ILI7Ucy4GURA66U7rUpbO8KcyksBi_aBifHW4hGG4"

	tsAccessVector  = "eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9.eyJ1c2VySWQiOiJ1c2VyLTEiLCJ1c2VybmFtZSI6InJlYWRlciIsImp0aSI6Imp0aS0xIiwidHlwZSI6ImFjY2VzcyIsImlhdCI6MTgwMDAwMDAwMCwiZXhwIjoxODAwMDAzNjAwfQ.C4dBP0QkgMOxi4J0MW0Xe0IPC4_b9uqKmyQeaRmXynE"
	tsRefreshVector = "eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9.eyJ1c2VySWQiOiJ1c2VyLTEiLCJ1c2VybmFtZSI6InJlYWRlciIsImp0aSI6Imp0aS0yIiwidHlwZSI6InJlZnJlc2giLCJpYXQiOjE4MDAwMDAwMDAsImV4cCI6MTgwMjU5MjAwMH0.1NXVpnXxXWyAYVsj4vhw0pBT66EQMvhe3nNlmf2Pbco"
	tsLegacyVector  = "eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9.eyJ1c2VySWQiOiJ1c2VyLTEiLCJ1c2VybmFtZSI6InJlYWRlciIsImlhdCI6MTcwMDAwMDAwMH0.NLS-dhRKu1hHCGWg-PFJf7JDDV5cBkDZZmsRp201Z0A"
)

var vectorNow = time.Unix(1800000100, 0)

func TestUpstreamAndGoVectorsVerify(t *testing.T) {
	secret := []byte(vectorSecret)
	for _, tc := range []struct {
		name     string
		token    string
		now      time.Time
		wantType string
		wantJTI  string
		wantExp  *int64
		wantIat  int64
	}{
		{name: "go access", token: goAccessVector, now: vectorNow, wantType: "access", wantJTI: "jti-1", wantExp: ptr(1800003600), wantIat: 1800000000},
		{name: "go refresh", token: goRefreshVector, now: vectorNow, wantType: "refresh", wantJTI: "jti-2", wantExp: ptr(1802592000), wantIat: 1800000000},
		{name: "go legacy", token: goLegacyVector, now: vectorNow, wantType: "", wantIat: 1700000000},
		{name: "go api key", token: goAPIKeyVector, now: vectorNow, wantType: "api", wantExp: ptr(1800003600), wantIat: 1800000000},
		{name: "go userapi legacy", token: goUserAPIVector, now: vectorNow, wantIat: 1793738194},
		{name: "ts access", token: tsAccessVector, now: vectorNow, wantType: "access", wantJTI: "jti-1", wantExp: ptr(1800003600), wantIat: 1800000000},
		{name: "ts refresh", token: tsRefreshVector, now: vectorNow, wantType: "refresh", wantJTI: "jti-2", wantExp: ptr(1802592000), wantIat: 1800000000},
		{name: "ts legacy", token: tsLegacyVector, now: vectorNow, wantIat: 1700000000},
	} {
		t.Run(tc.name, func(t *testing.T) {
			claims, err := validateToken(tc.token, secret, tc.now)
			if err != nil {
				t.Fatal(err)
			}
			if claims.Type != tc.wantType || claims.JTI != tc.wantJTI {
				t.Fatalf("type/jti = %q/%q, want %q/%q", claims.Type, claims.JTI, tc.wantType, tc.wantJTI)
			}
			if claims.IssuedAt == nil || claims.IssuedAt.Unix() != tc.wantIat {
				t.Fatalf("iat = %v, want %d", claims.IssuedAt, tc.wantIat)
			}
			if tc.wantExp == nil {
				if claims.ExpiresAt != nil {
					t.Fatalf("unexpected exp %v", claims.ExpiresAt)
				}
				return
			}
			if claims.ExpiresAt == nil || claims.ExpiresAt.Unix() != *tc.wantExp {
				t.Fatalf("exp = %v, want %d", claims.ExpiresAt, *tc.wantExp)
			}
		})
	}
}

// sign's output is checked by what a verifier reads back, not by its byte
// layout (JSON key order is not contract): every claim survives, the token
// verifies only under its own secret, and an empty secret cannot sign.
func TestSignedClaimsRoundTrip(t *testing.T) {
	secret := []byte(vectorSecret)
	got, err := sign(secret, map[string]any{"userId": "user-1", "username": "reader", "jti": "jti-1", "type": "access", "iat": 1800000000, "exp": 1800003600})
	if err != nil {
		t.Fatal(err)
	}
	claims, err := validateToken(got, secret, vectorNow)
	if err != nil {
		t.Fatal(err)
	}
	if claims.UserID != "user-1" || claims.Username != "reader" || claims.JTI != "jti-1" || claims.Type != "access" ||
		claims.IssuedAt.Unix() != 1800000000 || claims.ExpiresAt.Unix() != 1800003600 {
		t.Fatalf("claims changed on the way: %#v", claims)
	}
	if _, err := validateToken(got, []byte("another secret"), vectorNow); !errors.Is(err, ErrInvalidToken) {
		t.Fatalf("token verified under another secret: %v", err)
	}
	if _, err := sign(nil, map[string]any{"userId": "u"}); err == nil {
		t.Fatal("empty secret accepted for signing")
	}
}

func TestGoIssuedTokenRoundTrip(t *testing.T) {
	secret := []byte("round trip secret")
	token, err := sign(secret, map[string]any{"userId": "u", "username": "reader", "type": "access", "iat": 1000, "exp": 2000})
	if err != nil {
		t.Fatal(err)
	}
	claims, err := ValidateBearer(token, secret, time.Unix(1500, 0))
	if err != nil || claims.UserID != "u" || claims.Username != "reader" {
		t.Fatalf("claims=%#v err=%v", claims, err)
	}
}

func TestRejectsBadTokens(t *testing.T) {
	secret := []byte(vectorSecret)
	tamper := splitToken(goAccessVector)
	tamper[1] = base64.RawURLEncoding.EncodeToString([]byte(`{"userId":"admin","username":"root","type":"access","iat":1800000000,"exp":1800003600}`))
	noneHeader := splitToken(goAccessVector)
	noneHeader[0] = base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"none","typ":"JWT"}`))
	noneSigned := strings.Join([]string{noneHeader[0], noneHeader[1], ""}, ".")
	hs384 := signWithHMAC384ForTest(t, []byte(vectorSecret), map[string]any{"userId": "user-1", "username": "reader", "exp": 1800003600})

	for _, tc := range []struct {
		name    string
		token   string
		now     time.Time
		secret  []byte
		wantErr error
	}{
		{name: "expired at boundary", token: goAccessVector, now: time.Unix(1800003600, 0), wantErr: ErrExpired},
		{name: "expired API key enforced", token: goAPIKeyVector, now: time.Unix(1800003601, 0), wantErr: ErrExpired},
		{name: "bad signature", token: joinToken(tamper), now: vectorNow, wantErr: ErrInvalidToken},
		{name: "alg none", token: noneSigned, now: vectorNow, wantErr: ErrInvalidToken},
		{name: "alg none with signature", token: joinToken(noneHeader), now: vectorNow, wantErr: ErrInvalidToken},
		{name: "HS384 not accepted", token: hs384, now: vectorNow, wantErr: ErrInvalidToken},
		{name: "wrong secret", token: goAccessVector, now: vectorNow, secret: []byte("another secret"), wantErr: ErrInvalidToken},
	} {
		t.Run(tc.name, func(t *testing.T) {
			key := secret
			if tc.secret != nil {
				key = tc.secret
			}
			if _, err := ValidateBearer(tc.token, key, tc.now); err == nil || !errorIs(err, tc.wantErr) {
				t.Fatalf("got error %v, want %v", err, tc.wantErr)
			}
		})
	}
}

func TestRejectsNonNumericDates(t *testing.T) {
	secret := []byte("secret")
	for _, payload := range []map[string]any{
		{"userId": "u", "exp": "200"},
		{"userId": "u", "nbf": "1"},
		{"userId": "u", "iat": "1"},
	} {
		token := signForTest(t, secret, payload)
		if _, err := validateToken(token, secret, time.Unix(100, 0)); !errorIs(err, ErrInvalidToken) {
			t.Fatalf("payload %#v: got %v, want ErrInvalidToken", payload, err)
		}
	}
}

func TestRejectsTamperedSignature(t *testing.T) {
	parts := splitToken(goAccessVector)
	parts[2] = strings.Repeat("A", len(parts[2]))
	if _, err := ValidateBearer(joinToken(parts), []byte(vectorSecret), vectorNow); !errorIs(err, ErrInvalidToken) {
		t.Fatalf("got %v, want ErrInvalidToken", err)
	}
}

func TestNotBeforeEnforced(t *testing.T) {
	secret := []byte("secret")
	token, err := sign(secret, map[string]any{"userId": "u", "nbf": 101})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := validateToken(token, secret, time.Unix(100, 0)); !errorIs(err, ErrNotYetValid) {
		t.Fatal(err)
	}
}

func TestValidateBearerRefreshRejectedAndAPITypeAllowed(t *testing.T) {
	secret := []byte("secret")
	refresh := signForTest(t, secret, map[string]any{"userId": "u", "type": "refresh"})
	if _, err := ValidateBearer(refresh, secret, time.Now()); err == nil {
		t.Fatal("refresh token accepted as bearer")
	}
	apiKey := signForTest(t, secret, map[string]any{"type": "api", "keyId": "key-1"})
	claims, err := ValidateBearer(apiKey, secret, time.Now())
	if err != nil || claims.KeyID != "key-1" || claims.UserID != "" {
		t.Fatalf("API key should be returned for caller DB authorization: claims=%#v err=%v", claims, err)
	}
	apiBearer := signForTest(t, secret, map[string]any{"type": "api", "userId": "u", "keyId": "key-1"})
	claims, err = ValidateBearer(apiBearer, secret, time.Now())
	if err != nil || claims.Type != "api" {
		t.Fatalf("API type token not returned for caller authorization: claims=%#v err=%v", claims, err)
	}
}

func TestValidateBearerRequiresUserID(t *testing.T) {
	secret := []byte("secret")
	for _, payload := range []map[string]any{{"username": "reader"}, {"userId": 42}} {
		if _, err := ValidateBearer(signForTest(t, secret, payload), secret, time.Now()); err == nil {
			t.Fatalf("accepted payload %#v without a valid userId", payload)
		}
	}
}

// signForTest forges an HS256 token with crypto/hmac directly, so tests do not
// depend on Sign.
func signForTest(t *testing.T, secret []byte, payload any) string {
	t.Helper()
	header, err := json.Marshal(map[string]string{"alg": "HS256", "typ": "JWT"})
	if err != nil {
		t.Fatal(err)
	}
	body, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	input := base64.RawURLEncoding.EncodeToString(header) + "." + base64.RawURLEncoding.EncodeToString(body)
	mac := hmac.New(sha256.New, secret)
	_, _ = mac.Write([]byte(input))
	return input + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

func signWithHMAC384ForTest(t *testing.T, secret []byte, payload any) string {
	t.Helper()
	header, _ := json.Marshal(map[string]string{"alg": "HS384", "typ": "JWT"})
	body, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	input := base64.RawURLEncoding.EncodeToString(header) + "." + base64.RawURLEncoding.EncodeToString(body)
	mac := hmac.New(sha512.New384, secret)
	_, _ = mac.Write([]byte(input))
	return input + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

func ptr(v int64) *int64 { return &v }

func splitToken(token string) []string {
	return strings.Split(token, ".")
}

func joinToken(parts []string) string {
	return parts[0] + "." + parts[1] + "." + parts[2]
}

func errorIs(err, target error) bool {
	return errors.Is(err, target)
}

func TestIssueConstructorsRoundTrip(t *testing.T) {
	secret := []byte("secret")
	access, _ := IssueAccess(secret, "u", "reader", time.Hour)
	refresh, _ := IssueRefresh(secret, "u", "reader", time.Hour)
	if c, err := ValidateBearer(access, secret, time.Now()); err != nil || c.Type != "access" || c.UserID != "u" {
		t.Fatalf("access: %+v %v", c, err)
	}
	if _, err := ValidateBearer(refresh, secret, time.Now()); err == nil {
		t.Fatal("refresh accepted as bearer")
	}
	if _, err := ValidateRefresh(refresh, secret, time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err := ValidateRefresh(access, secret, time.Now()); !errorIs(err, ErrWrongType) {
		t.Fatalf("access accepted as refresh: %v", err)
	}
	if _, err := ValidateRefresh(refresh, secret, time.Now().Add(2*time.Hour)); !errorIs(err, ErrExpired) {
		t.Fatalf("expired refresh: %v", err)
	}
	key, _ := IssueAPIKey(secret, "k", "name", 0)
	if c, err := ValidateBearer(key, secret, time.Now()); err != nil || c.KeyID != "k" || c.ExpiresAt != nil {
		t.Fatalf("api key: %+v %v", c, err)
	}
	legacy, _ := IssueLegacy(secret, "u", "reader")
	if c, err := ValidateBearer(legacy, secret, time.Now()); err != nil || c.Type != "" || c.ExpiresAt != nil {
		t.Fatalf("legacy: %+v %v", c, err)
	}
}
