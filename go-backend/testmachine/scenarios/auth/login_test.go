package auth

import (
	"net/http"
	"testing"
)

func TestLoginAssertionRejectsMissingNullAndWrongIdentity(t *testing.T) {
	cookie := &http.Cookie{Name: "refresh_token", Value: "fixture-token", HttpOnly: true, Secure: true, Path: "/", SameSite: http.SameSiteLaxMode, MaxAge: 60}
	valid := []byte(`{"user":{"id":"fixture-listener","accessToken":"access","refreshToken":null,"bookmarks":[]}}`)
	if err := checkBrowserLogin(200, valid, []*http.Cookie{cookie}, "fixture-listener", "https"); err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string]string{
		"omitted-refresh":       `{"user":{"id":"fixture-listener","accessToken":"access","bookmarks":[]}}`,
		"wrong-user":            `{"user":{"id":"someone-else","accessToken":"access","refreshToken":null,"bookmarks":[]}}`,
		"token-in-browser-body": `{"user":{"id":"fixture-listener","accessToken":"access","refreshToken":"secret","bookmarks":[]}}`,
	} {
		t.Run(name, func(t *testing.T) {
			if err := checkBrowserLogin(200, []byte(body), []*http.Cookie{cookie}, "fixture-listener", "https"); err == nil {
				t.Fatal("invalid login payload earned proof")
			}
		})
	}
	if err := checkBrowserLogin(200, valid, []*http.Cookie{cookie}, "fixture-listener", "http"); err == nil {
		t.Fatal("request-security mismatch accepted")
	}
	cookie.Secure = false
	if err := checkBrowserLogin(200, valid, []*http.Cookie{cookie}, "fixture-listener", "http"); err != nil {
		t.Fatal("source permits an HTTP-scoped refresh cookie:", err)
	}
}
