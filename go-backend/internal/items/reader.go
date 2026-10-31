// Package items defines the shared read-only item serialization boundary.
package items

import (
	"context"

	"audiobookshelf-compat/internal/wire"
)

// Reader serializes library items for feature handlers that need catalog data.
type Reader interface {
	ItemJSON(context.Context, string, bool) (map[string]any, error)
	ItemMinifiedJSON(context.Context, string) (map[string]any, error)
}

// MediaAccess is the small, shared view of media needed by item ACL and
// realtime filtering. It is computed once per media value instead of being
// reconstructed by each consumer.
type MediaAccess struct {
	ID       string
	Explicit bool
	Tags     []string
}

// AccessMedia returns the identity and access-relevant fields for a typed book
// media value or a legacy podcast media map. ok is false for missing or
// unrecognized media so access checks can fail closed.
func AccessMedia(media any) (MediaAccess, bool) {
	switch value := media.(type) {
	case wire.BookFull:
		return MediaAccess{ID: value.ID, Explicit: value.Metadata.Explicit, Tags: value.Tags}, true
	case wire.BookMinified:
		return MediaAccess{ID: value.ID, Explicit: value.Metadata.Explicit, Tags: value.Tags}, true
	case wire.BookExpanded:
		return MediaAccess{ID: value.ID, Explicit: value.Metadata.Explicit, Tags: value.Tags}, true
	case map[string]any:
		access := MediaAccess{ID: stringField(value, "id"), Tags: stringSlice(value["tags"])}
		access.Explicit = boolField(value, "explicit")
		if metadata, ok := value["metadata"].(map[string]any); ok {
			access.Explicit = access.Explicit || boolField(metadata, "explicit")
		}
		return access, true
	default:
		return MediaAccess{}, false
	}
}

func stringField(values map[string]any, key string) string {
	value, _ := values[key].(string)
	return value
}

func boolField(values map[string]any, key string) bool {
	value, _ := values[key].(bool)
	return value
}

func stringSlice(value any) []string {
	switch values := value.(type) {
	case []string:
		return values
	case []any:
		out := make([]string, 0, len(values))
		for _, entry := range values {
			if text, ok := entry.(string); ok {
				out = append(out, text)
			}
		}
		return out
	default:
		return nil
	}
}
