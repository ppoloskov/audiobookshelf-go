package contracttest

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"testing"

	"audiobookshelf-compat/internal/catalog"
	"audiobookshelf-compat/internal/config"
	"audiobookshelf-compat/internal/libitem"
)

// TestLibitemEquivalence proves that internal/libitem serializes every library
// item of the fixture exactly as catalog's map-based code does today, by JSON
// value (key order is not contract, ADR-002).
func TestLibitemEquivalence(t *testing.T) {
	in := Boot(t)
	compareAllItems(t, in)

	// Null and zero columns the fixture does not exercise.
	for _, stmt := range []string{
		`UPDATE books SET title=NULL, subtitle=NULL, explicit=1, duration=NULL, ebookFile=NULL WHERE id=(SELECT mediaId FROM libraryItems WHERE mediaType='book' ORDER BY id LIMIT 1)`,
		`UPDATE books SET description='<p>Hi <b>there</b></p>', narrators='["A B","C"]', abridged=1 WHERE id=(SELECT mediaId FROM libraryItems WHERE mediaType='book' ORDER BY id DESC LIMIT 1)`,
		`UPDATE libraryItems SET lastScan=NULL, ino=NULL, mtime=NULL, lastScanVersion=NULL, size=0 WHERE mediaType='book'`,
		`UPDATE libraryItems SET lastScan=NULL, size=1234 WHERE mediaType='podcast'`,
		`UPDATE podcasts SET title=NULL, numEpisodes=NULL, lastEpisodeCheck=0, maxEpisodesToKeep=NULL, explicit=1`,
		`UPDATE podcastEpisodes SET enclosureURL='http://x/y.mp3', enclosureSize=77, "index"=NULL WHERE rowid=(SELECT min(rowid) FROM podcastEpisodes)`,
	} {
		if _, err := in.DB.Exec(stmt); err != nil {
			t.Fatalf("%s: %v", stmt, err)
		}
	}
	compareAllItems(t, in)
}

func compareAllItems(t *testing.T, in *Install) {
	t.Helper()
	ctx := context.Background()
	h := catalog.New(in.DB, config.Config{}, nil)

	var settings []byte
	_ = in.DB.QueryRow(`SELECT value FROM settings WHERE key='server-settings'`).Scan(&settings)
	prefixes := libitem.SortingPrefixes(settings)

	rows, err := in.DB.Query(`SELECT id FROM libraryItems ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
	}
	_ = rows.Close()
	if len(ids) < 5 {
		t.Fatalf("fixture has %d library items", len(ids))
	}
	plain, err := libitem.Load(ctx, in.DB, ids, libitem.Options{})
	if err != nil {
		t.Fatal(err)
	}
	withEpisodes, err := libitem.Load(ctx, in.DB, ids, libitem.Options{Episodes: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(plain) != len(ids) || len(withEpisodes) != len(ids) {
		t.Fatalf("Load returned %d/%d items for %d ids", len(plain), len(withEpisodes), len(ids))
	}

	// The library items page rows come from the HTTP list (the only builder).
	root := in.Login(t, in.Users["root"].Username, in.Users["root"].Password)
	listRows := map[string]any{}
	var libraries []string
	lrows, err := in.DB.Query(`SELECT id FROM libraries`)
	if err != nil {
		t.Fatal(err)
	}
	for lrows.Next() {
		var id string
		_ = lrows.Scan(&id)
		libraries = append(libraries, id)
	}
	_ = lrows.Close()
	for _, lib := range libraries {
		resp, body := in.Do(t, "GET", "/api/libraries/"+lib+"/items?limit=0", root, nil)
		if resp.StatusCode != 200 {
			t.Fatalf("list %s: %d %s", lib, resp.StatusCode, body)
		}
		var page struct {
			Results []map[string]any `json:"results"`
		}
		if err := json.Unmarshal(body, &page); err != nil {
			t.Fatal(err)
		}
		for _, row := range page.Results {
			listRows[row["id"].(string)] = row
		}
	}

	kinds := map[string]int{}
	for i, id := range ids {
		it, full := plain[i], withEpisodes[i]
		if it.ID != id {
			t.Fatalf("Load order: got %s want %s", it.ID, id)
		}
		kinds[it.MediaType]++
		oldFull, err := h.ItemJSON(ctx, id, false)
		if err != nil {
			t.Fatal(err)
		}
		oldExpanded, err := h.ItemJSON(ctx, id, true)
		if err != nil {
			t.Fatal(err)
		}
		oldMin, err := h.ItemMinifiedJSON(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		compare(t, id+" Full", oldFull, full.Full())
		compare(t, id+" Expanded", oldExpanded, full.Expanded(prefixes))
		compare(t, id+" Minified", oldMin, it.Minified(prefixes))
		if row, ok := listRows[id]; ok {
			compare(t, id+" ListRow", row, it.ListRow(prefixes))
			delete(listRows, id)
		} else {
			t.Errorf("%s: not in any library list", id)
		}
	}
	if len(listRows) != 0 {
		t.Errorf("list rows without a loaded item: %d", len(listRows))
	}
	if kinds["book"] == 0 || kinds["podcast"] == 0 {
		t.Errorf("fixture must cover books and podcasts, got %v", kinds)
	}
	t.Logf("compared %v items in 4 shapes", kinds)
}

func compare(t *testing.T, name string, old, new any) {
	t.Helper()
	a, b := normalize(t, old), normalize(t, new)
	for _, diff := range diffJSON("$", a, b) {
		t.Errorf("%s: %s", name, diff)
	}
}

func normalize(t *testing.T, v any) any {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	var out any
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func diffJSON(path string, a, b any) []string {
	switch x := a.(type) {
	case map[string]any:
		y, ok := b.(map[string]any)
		if !ok {
			return []string{fmt.Sprintf("%s: old object, new %T", path, b)}
		}
		keys := map[string]bool{}
		for k := range x {
			keys[k] = true
		}
		for k := range y {
			keys[k] = true
		}
		sorted := make([]string, 0, len(keys))
		for k := range keys {
			sorted = append(sorted, k)
		}
		sort.Strings(sorted)
		var out []string
		for _, k := range sorted {
			av, aok := x[k]
			bv, bok := y[k]
			switch {
			case !aok:
				out = append(out, fmt.Sprintf("%s.%s: only in new (%s)", path, k, short(bv)))
			case !bok:
				out = append(out, fmt.Sprintf("%s.%s: only in old (%s)", path, k, short(av)))
			default:
				out = append(out, diffJSON(path+"."+k, av, bv)...)
			}
		}
		return out
	case []any:
		y, ok := b.([]any)
		if !ok {
			return []string{fmt.Sprintf("%s: old array, new %T", path, b)}
		}
		if len(x) != len(y) {
			return []string{fmt.Sprintf("%s: old len %d, new len %d", path, len(x), len(y))}
		}
		var out []string
		for i := range x {
			out = append(out, diffJSON(fmt.Sprintf("%s[%d]", path, i), x[i], y[i])...)
		}
		return out
	}
	if !reflect.DeepEqual(a, b) {
		return []string{fmt.Sprintf("%s: old %s, new %s", path, short(a), short(b))}
	}
	return nil
}

func short(v any) string {
	raw, _ := json.Marshal(v)
	s := string(raw)
	if len(s) > 80 {
		s = s[:80] + "..."
	}
	return strings.TrimSpace(s)
}
