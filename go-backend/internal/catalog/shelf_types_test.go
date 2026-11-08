package catalog

import (
	"encoding/json"
	"testing"
)

// Shelf replaced a map[string]any literal; encoding/json writes map keys
// sorted, so the struct has to produce the same bytes.
func TestShelfEncodesLikeTheMapItReplaced(t *testing.T) {
	for _, entities := range []any{[]string{"a"}, []map[string]any{}, []map[string]any(nil), nil} {
		want, err := json.Marshal(map[string]any{"id": "recently-added", "label": "Recently Added", "labelStringKey": "LabelRecentlyAdded", "type": "book", "entities": entities, "total": 3})
		if err != nil {
			t.Fatal(err)
		}
		got, err := json.Marshal(shelf{ID: "recently-added", Label: "Recently Added", LabelStringKey: "LabelRecentlyAdded", Type: "book", Entities: entities, Total: 3})
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != string(want) {
			t.Errorf("entities %#v: got %s, want %s", entities, got, want)
		}
	}
}
