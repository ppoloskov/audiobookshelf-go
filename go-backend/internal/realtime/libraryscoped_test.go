package realtime

import (
	"testing"

	"audiobookshelf-compat/internal/auth"
	"audiobookshelf-compat/internal/db"
)

type scopedPayload struct{ library string }

func (p scopedPayload) EventLibraryID() string { return p.library }

func TestLibraryScopedPayloadNeedsLibraryAccess(t *testing.T) {
	user := func(libraries ...string) *auth.Principal {
		return auth.NewPrincipal(db.User{IsActive: true, Permissions: db.JSON[db.Permissions]{V: db.Permissions{LibrariesAccessible: libraries, AccessAllTags: true}}})
	}
	member, outsider := user("lib-1"), user("lib-2")
	if _, ok := payloadForUser(member, scopedPayload{"lib-1"}); !ok {
		t.Error("a library-scoped payload must reach a user of its library")
	}
	if _, ok := payloadForUser(outsider, scopedPayload{"lib-1"}); ok {
		t.Error("a library-scoped payload must not reach other users")
	}
}
