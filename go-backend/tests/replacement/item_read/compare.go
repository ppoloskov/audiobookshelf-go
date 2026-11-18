package item_read

import (
	"encoding/json"
	"fmt"
	"slices"
	"sort"
	"strings"
	"testing"
)

// normalize converts any JSON-marshalable value into the generic decoded form
// (map[string]any, []any, float64, string, bool, nil), so expected values built
// from Go types compare with decoded responses.
func normalize(t *testing.T, value any) any {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("marshal expected value: %v", err)
	}
	var out any
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("unmarshal expected value: %v", err)
	}
	return out
}

// differences lists where got differs from want (decoded JSON), at most limit
// entries. A key present in only one object is a difference; null and absent
// are different.
func differences(want, got any, limit int) []string {
	var out []string
	var walk func(path string, want, got any)
	walk = func(path string, want, got any) {
		if len(out) >= limit {
			return
		}
		switch w := want.(type) {
		case map[string]any:
			g, ok := got.(map[string]any)
			if !ok {
				out = append(out, fmt.Sprintf("%s: want object, got %T", path, got))
				return
			}
			keys := make([]string, 0, len(w)+len(g))
			for key := range w {
				keys = append(keys, key)
			}
			for key := range g {
				if _, ok := w[key]; !ok {
					keys = append(keys, key)
				}
			}
			sort.Strings(keys)
			for _, key := range slices.Compact(keys) {
				wv, wok := w[key]
				gv, gok := g[key]
				switch {
				case !wok:
					out = append(out, fmt.Sprintf("%s.%s: unexpected key (value %v)", path, key, gv))
				case !gok:
					out = append(out, fmt.Sprintf("%s.%s: missing (want %v)", path, key, wv))
				default:
					walk(path+"."+key, wv, gv)
				}
			}
		case []any:
			g, ok := got.([]any)
			if !ok || len(g) != len(w) {
				out = append(out, fmt.Sprintf("%s: want array of %d, got %v", path, len(w), got))
				return
			}
			for i := range w {
				walk(fmt.Sprintf("%s[%d]", path, i), w[i], g[i])
			}
		default:
			if fmt.Sprintf("%T:%v", want, want) != fmt.Sprintf("%T:%v", got, got) {
				out = append(out, fmt.Sprintf("%s: want %T(%v), got %T(%v)", path, want, want, got, got))
			}
		}
	}
	walk("$", want, got)
	return out
}

// requireSame fails the test unless the decoded response equals the expected value.
func requireSame(t *testing.T, what string, want, got any) {
	t.Helper()
	if diffs := differences(normalize(t, want), got, 12); len(diffs) > 0 {
		t.Fatalf("%s differs from the values derived from the fixture:\n  %s", what, strings.Join(diffs, "\n  "))
	}
}

// sortEpisodes orders media.episodes of an item document by id: upstream
// reads them without ORDER BY, so their order is not a contract.
func sortEpisodes(doc any) {
	item, _ := doc.(map[string]any)
	media, _ := item["media"].(map[string]any)
	if episodes, ok := media["episodes"].([]any); ok {
		sortByID(episodes)
	}
}

// requireItem compares a decoded item document with the expected one, ignoring the episode order.
func requireItem(t *testing.T, what string, want object, got any) {
	t.Helper()
	wantDoc := normalize(t, want)
	sortEpisodes(wantDoc)
	sortEpisodes(got)
	requireSame(t, what, wantDoc, got)
}
