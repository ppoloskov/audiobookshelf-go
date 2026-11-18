package auth

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
)

// checkBrowserLogin validates the independently source-derived browser contract.
// RawMessage distinguishes explicit null from an omitted refreshToken field.
func checkBrowserLogin(status int, raw []byte, cookies []*http.Cookie, expectedID, scheme string) error {
	var value struct {
		User struct {
			ID           string            `json:"id"`
			AccessToken  string            `json:"accessToken"`
			RefreshToken json.RawMessage   `json:"refreshToken"`
			Bookmarks    []json.RawMessage `json:"bookmarks"`
		} `json:"user"`
	}
	if status != 200 || json.Unmarshal(raw, &value) != nil || expectedID == "" || value.User.ID != expectedID || value.User.AccessToken == "" || !bytes.Equal(bytes.TrimSpace(value.User.RefreshToken), []byte("null")) || value.User.Bookmarks == nil {
		return fmt.Errorf("browser login must return the fixture user/access token, explicit refreshToken:null and bookmarks array")
	}
	for _, cookie := range cookies {
		if cookie.Name == "refresh_token" {
			if cookie.Value == "" || !cookie.HttpOnly || cookie.Secure != (scheme == "https") || cookie.Path != "/" || cookie.SameSite != http.SameSiteLaxMode || cookie.MaxAge <= 0 {
				return fmt.Errorf("refresh cookie must retain request-security, lifetime and scope semantics")
			}
			return nil
		}
	}
	return fmt.Errorf("browser refresh cookie is missing")
}
