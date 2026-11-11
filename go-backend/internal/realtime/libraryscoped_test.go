package realtime

import (
	"encoding/json"
	"testing"

	"audiobookshelf-compat/internal/access"
	"audiobookshelf-compat/internal/store"
)

type scopedPayload struct{ library string }

func (p scopedPayload) EventLibraryID() string { return p.library }

func TestLibraryScopedPayloadNeedsLibraryAccess(t *testing.T) {
	user := func(perms string) *access.Permissions {
		p := access.Of(store.User{Type: "user", IsActive: true, Permissions: json.RawMessage(perms)})
		return &p
	}
	member := user(`{"accessAllLibraries":false,"librariesAccessible":["lib-1"],"accessAllTags":true}`)
	outsider := user(`{"accessAllLibraries":false,"librariesAccessible":["lib-2"],"accessAllTags":true}`)
	if !payloadAccessible(member, scopedPayload{"lib-1"}) || payloadAccessible(outsider, scopedPayload{"lib-1"}) {
		t.Error("a library-scoped payload must reach only users of its library")
	}
	filtered, ok := payloadForUser(outsider, []any{scopedPayload{"lib-1"}})
	if ok || len(filtered.([]any)) != 0 {
		t.Errorf("a list of scoped payloads must be filtered, got %v", filtered)
	}
}
