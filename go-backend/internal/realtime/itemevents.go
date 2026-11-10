package realtime

import (
	"audiobookshelf-compat/internal/access"
	"audiobookshelf-compat/internal/items"
)

// accessor is implemented by the typed item payloads (items.Expanded, ...).
type accessor interface{ Access() access.Access }

func filter[T accessor](g *access.Permissions, values []T) (any, bool) {
	out := make([]T, 0, len(values))
	for _, v := range values {
		if g.Item(v.Access()) {
			out = append(out, v)
		}
	}
	return out, len(out) > 0
}

// payloadAccessible filters one payload. Typed item payloads (items.Full,
// Expanded, Minified) apply the library, explicit and tag rules. Any other
// payload that carries a libraryId (collections, item_removed) is limited to
// the library check; global payloads are unrestricted. A map that looks like an
// item (has "media") is never delivered: item payloads must be typed.
func payloadAccessible(g *access.Permissions, payload any) bool {
	if a, ok := payload.(accessor); ok {
		return g.Item(a.Access())
	}
	metadata, ok := payload.(map[string]any)
	if !ok {
		return true
	}
	libraryID, _ := metadata["libraryId"].(string)
	if libraryID == "" {
		return true
	}
	if _, isItem := metadata["media"]; isItem {
		return false
	}
	return g.Library(libraryID)
}

func payloadForUser(g *access.Permissions, payload any) (any, bool) {
	switch values := payload.(type) {
	case []items.Expanded:
		return filter(g, values)
	case []items.Minified:
		return filter(g, values)
	case []items.Full:
		return filter(g, values)
	case []any:
		filtered := make([]any, 0, len(values))
		restricted := false
		for _, value := range values {
			if hasAccessMetadata(value) {
				restricted = true
			}
			if payloadAccessible(g, value) {
				filtered = append(filtered, value)
			}
		}
		if !restricted {
			return payload, true
		}
		return filtered, len(filtered) > 0
	default:
		return payload, payloadAccessible(g, payload)
	}
}

// hasAccessMetadata reports whether a list element carries access inputs: a
// typed item or a map with a libraryId.
func hasAccessMetadata(raw any) bool {
	if _, ok := raw.(accessor); ok {
		return true
	}
	item, ok := raw.(map[string]any)
	if !ok {
		return false
	}
	_, ok = item["libraryId"].(string)
	return ok
}
