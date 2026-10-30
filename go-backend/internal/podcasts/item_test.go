package podcasts

import (
	"encoding/json"
	"testing"

	"audiobookshelf-compat/internal/store"
)

func TestCanAccessPodcastExplicitContentPermission(t *testing.T) {
	// Upstream server/controllers/PodcastController.js:569 calls
	// User.checkCanAccessLibraryItem; User.js:675-683 includes explicit ACL.
	permissions, _ := json.Marshal(map[string]any{
		"accessAllLibraries":    true,
		"accessAllTags":         true,
		"accessExplicitContent": false,
	})
	u := store.User{Type: "user", Permissions: permissions}
	item := podcastItem{Explicit: true}
	if canAccessPodcast(u, item) {
		t.Fatal("explicit podcast must be denied without explicit-content permission")
	}
	permissions, _ = json.Marshal(map[string]any{
		"accessAllLibraries":    true,
		"accessAllTags":         true,
		"accessExplicitContent": true,
	})
	u.Permissions = permissions
	if !canAccessPodcast(u, item) {
		t.Fatal("explicit podcast should be allowed when explicit-content permission is granted")
	}
	u.Type = "admin"
	u.Permissions = []byte(`{}`)
	if !canAccessPodcast(u, item) {
		t.Fatal("admin bypass must remain intact")
	}
}
