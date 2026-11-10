package identity

import (
	"net/http/httptest"
	"testing"

	"audiobookshelf-compat/internal/users"
)

func TestWaveGAPIKeyRevocationAppliesOnNextAuthentication(t *testing.T) {
	db, h := identityFixture(t)
	defer db.Close()
	key, token, err := h.CreateAPIKey(t.Context(), users.NewAPIKey{
		Name: "revocation-check", UserID: "user-1", CreatedBy: "user-1", Active: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	authenticate := func() error {
		r := httptest.NewRequest("GET", "/api/me", nil)
		r.Header.Set("Authorization", "Bearer "+token)
		_, err := h.Authenticate(r)
		return err
	}
	if err := authenticate(); err != nil {
		t.Fatalf("new key did not authenticate: %v", err)
	}
	inactive := false
	if err := h.UpdateAPIKey(t.Context(), key.ID, "user-1", &inactive); err != nil {
		t.Fatal(err)
	}
	if err := authenticate(); err == nil {
		t.Fatal("deactivated API key still authenticates")
	}
	if deleted, err := h.DeleteAPIKey(t.Context(), key.ID); err != nil || !deleted {
		t.Fatalf("delete key: deleted=%v err=%v", deleted, err)
	}
	if err := authenticate(); err == nil {
		t.Fatal("deleted API key still authenticates")
	}
}
