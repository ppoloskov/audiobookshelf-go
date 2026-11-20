package dbtest

import (
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"testing"
)

// RoundTrip marshals v and decodes it into generic JSON for comparison by value.
func RoundTrip(t testing.TB, v any) any {
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

// DiffJSON lists every path at which two decoded JSON values differ (null vs
// absent and [] vs null count). Decode both sides with RoundTrip first.
func DiffJSON(path string, want, got any) []string {
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
				out = append(out, DiffJSON(path+"."+k, wv, gv)...)
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
			out = append(out, DiffJSON(fmt.Sprintf("%s[%d]", path, i), w[i], g[i])...)
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

// EqualJSON fails the test with a path-level diff unless old and new marshal
// to the same JSON value.
func EqualJSON(t testing.TB, label string, old, new any) {
	t.Helper()
	if diffs := DiffJSON("$", RoundTrip(t, old), RoundTrip(t, new)); len(diffs) > 0 {
		t.Errorf("%s differs:\n  %s", label, strings.Join(diffs, "\n  "))
	}
}
