package catalog

import (
	"encoding/json"
	"reflect"
	"testing"
)

// The shelf struct must encode to the object the clients read: entities as an
// array (empty stays [], an absent list is null) and the label keys by name.
func TestShelfEncodesToTheClientShape(t *testing.T) {
	cases := []struct {
		name     string
		entities any
		want     string
	}{
		{"entities", []string{"a"}, `{"id":"recently-added","label":"Recently Added","labelStringKey":"LabelRecentlyAdded","type":"book","entities":["a"],"total":3}`},
		{"empty list", []map[string]any{}, `{"id":"recently-added","label":"Recently Added","labelStringKey":"LabelRecentlyAdded","type":"book","entities":[],"total":3}`},
		{"nil list", []map[string]any(nil), `{"id":"recently-added","label":"Recently Added","labelStringKey":"LabelRecentlyAdded","type":"book","entities":null,"total":3}`},
		{"nil", nil, `{"id":"recently-added","label":"Recently Added","labelStringKey":"LabelRecentlyAdded","type":"book","entities":null,"total":3}`},
	}
	for _, c := range cases {
		raw, err := json.Marshal(shelf{ID: "recently-added", Label: "Recently Added", LabelStringKey: "LabelRecentlyAdded", Type: "book", Entities: c.entities, Total: 3})
		if err != nil {
			t.Fatal(err)
		}
		var got, want any
		if err := json.Unmarshal(raw, &got); err != nil {
			t.Fatal(err)
		}
		_ = json.Unmarshal([]byte(c.want), &want)
		if !reflect.DeepEqual(got, want) {
			t.Errorf("%s: got %s, want %s", c.name, raw, c.want)
		}
	}
}
