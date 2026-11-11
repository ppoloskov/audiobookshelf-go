package item

import (
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"testing"
)

// roundTrip marshals v and decodes it into generic JSON for comparison by value.
func roundTrip(t testing.TB, v any) any {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal %T: %v", v, err)
	}
	var out any
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("unmarshal %s: %v", raw, err)
	}
	return out
}

// diffJSON lists every path at which two decoded JSON values differ.
func diffJSON(path string, want, got any) []string {
	switch w := want.(type) {
	case map[string]any:
		g, ok := got.(map[string]any)
		if !ok {
			return []string{fmt.Sprintf("%s: want object, got %s", path, short(got))}
		}
		var out []string
		keys := map[string]bool{}
		for k := range w {
			keys[k] = true
		}
		for k := range g {
			keys[k] = true
		}
		sorted := make([]string, 0, len(keys))
		for k := range keys {
			sorted = append(sorted, k)
		}
		sort.Strings(sorted)
		for _, k := range sorted {
			wv, wok := w[k]
			gv, gok := g[k]
			switch {
			case !gok:
				out = append(out, fmt.Sprintf("%s.%s: missing in new, old has %s", path, k, short(wv)))
			case !wok:
				out = append(out, fmt.Sprintf("%s.%s: extra in new: %s", path, k, short(gv)))
			default:
				out = append(out, diffJSON(path+"."+k, wv, gv)...)
			}
		}
		return out
	case []any:
		g, ok := got.([]any)
		if !ok {
			return []string{fmt.Sprintf("%s: want array, got %s", path, short(got))}
		}
		if len(w) != len(g) {
			return []string{fmt.Sprintf("%s: old has %d elements, new %d", path, len(w), len(g))}
		}
		var out []string
		for i := range w {
			out = append(out, diffJSON(fmt.Sprintf("%s[%d]", path, i), w[i], g[i])...)
		}
		return out
	}
	if reflect.DeepEqual(want, got) {
		return nil
	}
	return []string{fmt.Sprintf("%s: old %s, new %s", path, short(want), short(got))}
}

func short(v any) string {
	raw, _ := json.Marshal(v)
	if len(raw) > 120 {
		return string(raw[:120]) + "..."
	}
	return string(raw)
}

func TestDiffJSON(t *testing.T) {
	cases := []struct {
		name      string
		want, got string
		diffs     []string
	}{
		{"equal", `{"a":[1,null,{"b":"x"}]}`, `{"a":[1,null,{"b":"x"}]}`, nil},
		{"null vs absent", `{"a":null}`, `{}`, []string{"$.a: missing in new, old has null"}},
		{"extra key", `{}`, `{"a":1}`, []string{"$.a: extra in new: 1"}},
		{"empty vs null array", `{"a":[]}`, `{"a":null}`, []string{"$.a: want array, got null"}},
		{"nested value", `{"a":[{"b":1}]}`, `{"a":[{"b":2}]}`, []string{"$.a[0].b: old 1, new 2"}},
		{"length", `[1,2]`, `[1]`, []string{"$: old has 2 elements, new 1"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var w, g any
			if err := json.Unmarshal([]byte(c.want), &w); err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal([]byte(c.got), &g); err != nil {
				t.Fatal(err)
			}
			if got := diffJSON("$", w, g); strings.Join(got, "|") != strings.Join(c.diffs, "|") {
				t.Fatalf("diff = %v, want %v", got, c.diffs)
			}
		})
	}
}
