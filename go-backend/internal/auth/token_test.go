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

func TestValidateBearerUpstreamHMACAlgorithms(t *testing.T) {
	secret := []byte("test upstream secret")
	for _, alg := range []string{"HS256", "HS384", "HS512"} {
		t.Run(alg, func(t *testing.T) {
			token := signForTest(t, alg, secret, map[string]any{
				"userId": "user-1", "username": "reader", "type": "access",
				"jti": "session-1", "exp": 2000, "iat": 1000,
			})
			claims, err := ValidateBearer(token, secret, time.Unix(1500, 0))
			if err != nil {
				t.Fatal(err)
			}
			if claims.UserID != "user-1" || claims.Username != "reader" || claims.Type != "access" || claims.JTI != "session-1" {
				t.Fatalf("unexpected claims: %#v", claims)
			}
			if claims.ExpiresAt == nil || !claims.ExpiresAt.Equal(time.Unix(2000, 0)) {
				t.Fatalf("unexpected expiration: %v", claims.ExpiresAt)
			}
		})
	}
}

func TestValidateBearerSignatureTampering(t *testing.T) {
	secret := []byte("secret")
	token := signForTest(t, "HS256", secret, map[string]any{"userId": "u"})
	parts := []byte(token)
	parts[len(parts)-1] ^= 1
	if _, err := ValidateBearer(string(parts), secret, time.Unix(0, 0)); err == nil {
		t.Fatal("tampered signature accepted")
	}
}

func TestValidateBearerTimeClaimsAndLegacy(t *testing.T) {
	secret := []byte("secret")
	now := time.Unix(100, 0)
	for _, tc := range []struct {
		name    string
		payload map[string]any
		wantErr error
	}{
		{name: "expiration boundary", payload: map[string]any{"userId": "u", "exp": 100}, wantErr: ErrExpired},
		{name: "future not before", payload: map[string]any{"userId": "u", "nbf": 101}, wantErr: ErrNotYetValid},
		{name: "numeric string expiration", payload: map[string]any{"userId": "u", "exp": "200"}, wantErr: ErrInvalidToken},
		{name: "legacy no expiration", payload: map[string]any{"userId": "u", "username": "old"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			token := signForTest(t, "HS256", secret, tc.payload)
			claims, err := ValidateBearer(token, secret, now)
			if tc.wantErr != nil {
				if err == nil || !errorIs(err, tc.wantErr) {
					t.Fatalf("got error %v, want %v", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if claims.ExpiresAt != nil {
				t.Fatalf("legacy token unexpectedly has expiration: %v", claims.ExpiresAt)
			}
		})
	}
}

func TestValidateBearerRefreshRejectedAndAPITypeAllowed(t *testing.T) {
	secret := []byte("secret")
	refresh := signForTest(t, "HS256", secret, map[string]any{"userId": "u", "type": "refresh"})
	if _, err := ValidateBearer(refresh, secret, time.Now()); err == nil {
		t.Fatal("refresh token accepted as bearer")
	}
	apiKey := signForTest(t, "HS256", secret, map[string]any{"type": "api", "keyId": "key-1"})
	claims, err := ValidateBearer(apiKey, secret, time.Now())
	if err != nil || claims.KeyID != "key-1" || claims.UserID != "" {
		t.Fatalf("API key should be returned for caller DB authorization: claims=%#v err=%v", claims, err)
	}
	apiBearer := signForTest(t, "HS256", secret, map[string]any{"type": "api", "userId": "u", "keyId": "key-1"})
	claims, err = ValidateBearer(apiBearer, secret, time.Now())
	if err != nil || claims.Type != "api" {
		t.Fatalf("API type token not returned for caller authorization: claims=%#v err=%v", claims, err)
	}
}

func TestValidateBearerRequiresUserIDAndRejectsUnsupportedAlgorithm(t *testing.T) {
	secret := []byte("secret")
	for _, payload := range []map[string]any{{"username": "reader"}, {"userId": 42}} {
		if _, err := ValidateBearer(signForTest(t, "HS256", secret, payload), secret, time.Now()); err == nil {
			t.Fatalf("accepted payload %#v without a valid userId", payload)
		}
	}
	token := signForTest(t, "HS256", secret, map[string]any{"userId": "u"})
	parts := splitToken(token)
	parts[0] = base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"none"}`))
	if _, err := ValidateBearer(joinToken(parts), secret, time.Now()); err == nil {
		t.Fatal("unsupported algorithm accepted")
	}
}

func signForTest(t *testing.T, alg string, secret []byte, payload any) string {
	t.Helper()
	header, err := json.Marshal(map[string]string{"alg": alg, "typ": "JWT"})
	if err != nil {
		t.Fatal(err)
	}
	body, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	input := base64.RawURLEncoding.EncodeToString(header) + "." + base64.RawURLEncoding.EncodeToString(body)
	var sum []byte
	switch alg {
	case "HS256":
		mac := hmac.New(sha256.New, secret)
		_, _ = mac.Write([]byte(input))
		sum = mac.Sum(nil)
	case "HS384":
		mac := hmac.New(sha512.New384, secret)
		_, _ = mac.Write([]byte(input))
		sum = mac.Sum(nil)
	case "HS512":
		mac := hmac.New(sha512.New, secret)
		_, _ = mac.Write([]byte(input))
		sum = mac.Sum(nil)
	default:
		t.Fatalf("unsupported test algorithm: %s", alg)
	}
	return input + "." + base64.RawURLEncoding.EncodeToString(sum)
}

func splitToken(token string) []string {
	return strings.Split(token, ".")
}

func joinToken(parts []string) string {
	return parts[0] + "." + parts[1] + "." + parts[2]
}

func errorIs(err, target error) bool {
	return errors.Is(err, target)
}
