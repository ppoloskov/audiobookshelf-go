package realtime

import (
	"audiobookshelf-compat/internal/auth"
	"audiobookshelf-compat/internal/item"
)

// itemView is implemented by the typed item views (item.Full, Expanded,
// Minified).
type itemView interface{ Access() item.Access }

// libraryScoped is implemented by typed payloads that belong to one library
// (collections): they are limited to users who can access it.
type libraryScoped interface{ EventLibraryID() string }

func canSee[T itemView](user *auth.Principal, view T) bool {
	a := view.Access()
	return user.CanAccessItem(a.LibraryID, a.Explicit, a.Tags)
}

// filterItems keeps the items user may see; it reports false when none remain.
func filterItems[T itemView](user *auth.Principal, views []T) (any, bool) {
	out := make([]T, 0, len(views))
	for _, view := range views {
		if canSee(user, view) {
			out = append(out, view)
		}
	}
	return out, len(out) > 0
}

// payloadForUser returns what user may receive of a broadcast payload, and
// whether anything is left. Typed item views and lists of them apply the
// library, explicit and tag rules, a payload with an EventLibraryID applies
// the library rule, and every other payload is global. Item payloads must be
// typed: an untyped map is never inspected.
func payloadForUser(user *auth.Principal, payload any) (any, bool) {
	switch value := payload.(type) {
	case []item.Expanded:
		return filterItems(user, value)
	case []item.Minified:
		return filterItems(user, value)
	case []item.Full:
		return filterItems(user, value)
	case itemView:
		return payload, canSee(user, value)
	case libraryScoped:
		return payload, user.CanAccessLibrary(value.EventLibraryID())
	default:
		return payload, true
	}
}
