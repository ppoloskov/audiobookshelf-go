package scanner

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

// matcherTree writes n item directories, each holding two audio files, and
// returns the groups (in path order) and the stored rows describing them.
func matcherTree(t *testing.T, root string, n int) ([]*group, []existingItem) {
	t.Helper()
	groups, items := make([]*group, 0, n), make([]existingItem, 0, n)
	for i := 0; i < n; i++ {
		dir := filepath.Join(root, fmt.Sprintf("item%04d", i))
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		for _, name := range []string{"a.mp3", "b.mp3"} {
			if err := os.WriteFile(filepath.Join(dir, name), []byte(dir+name), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		g, ex := readGroup(t, dir)
		groups, items = append(groups, g), append(items, ex)
	}
	return groups, items
}

// readGroup scans dir into a group and the stored row it would have produced.
func readGroup(t *testing.T, dir string) (*group, existingItem) {
	t.Helper()
	g := &group{path: dir}
	var stored []libraryFile
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		info, err := e.Info()
		if err != nil {
			t.Fatal(err)
		}
		g.files = append(g.files, file{path: filepath.Join(dir, e.Name()), info: info})
		stored = append(stored, libraryFile{Ino: inode(info)})
	}
	raw, _ := json.Marshal(stored)
	info, err := os.Stat(dir)
	if err != nil {
		t.Fatal(err)
	}
	return g, existingItem{id: "id-" + filepath.Base(dir), path: dir, ino: inode(info), files: raw}
}

func TestGroupMatcherRules(t *testing.T) {
	root := t.TempDir()
	_, items := matcherTree(t, root, 6)
	// item0001 moved, item0002 renamed, item0003 renamed and its directory
	// replaced (only its files keep their inodes), item0004 deleted.
	move := func(from, to string) {
		if err := os.Rename(filepath.Join(root, from), filepath.Join(root, to)); err != nil {
			t.Fatal(err)
		}
	}
	move("item0001", "moved")
	move("item0002", "zz-renamed")
	move("item0003", "tmp3")
	if err := os.Mkdir(filepath.Join(root, "item0003"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, n := range []string{"a.mp3", "b.mp3"} {
		if err := os.Rename(filepath.Join(root, "tmp3", n), filepath.Join(root, "item0003", n)); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Remove(filepath.Join(root, "tmp3")); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(filepath.Join(root, "item0004")); err != nil {
		t.Fatal(err)
	}
	var scanned []*group
	for _, name := range []string{"item0000", "item0003", "item0005", "moved", "zz-renamed"} {
		g, _ := readGroup(t, filepath.Join(root, name))
		scanned = append(scanned, g)
	}
	m := newGroupMatcher(scanned)
	pathOf := func(g *group) string {
		if g == nil {
			return "<nil>"
		}
		return filepath.Base(g.path)
	}
	want := []string{"item0000", "moved", "zz-renamed", "item0003", "<nil>", "item0005"}
	for i, ex := range items {
		g := m.match(ex)
		if pathOf(g) != want[i] {
			t.Errorf("item %d matched %s, want %s", i, pathOf(g), want[i])
		}
		if g != nil {
			m.claim(g)
		}
	}
	if left := m.unclaimed(); len(left) != 0 {
		t.Fatalf("unclaimed groups = %d, want 0", len(left))
	}
}

func TestGroupMatcherPathBeatsInodeAndClaimedGroupsAreSkipped(t *testing.T) {
	root := t.TempDir()
	groups, items := matcherTree(t, root, 3)
	m := newGroupMatcher(groups)
	// A stored item whose path is group 2 but whose inode points at group 0:
	// the path wins.
	ex := items[0]
	ex.path = groups[2].path
	if g := m.match(ex); g != groups[2] {
		t.Fatalf("path match = %s", g.path)
	}
	m.claim(groups[2])
	// Group 2 is claimed: the same item falls back to its inode.
	if g := m.match(ex); g != groups[0] {
		t.Fatalf("inode fallback = %v", g)
	}
	// Ambiguity: an item whose stored files sit in groups 1 and 0 (and a
	// directory inode that matches nothing) takes the earliest group in scan
	// order, not the order of its stored files.
	both := existingItem{path: "/gone", ino: "no-such-inode"}
	both.files, _ = json.Marshal([]libraryFile{{Ino: items1Ino(t, items[1])}, {Ino: items1Ino(t, items[0])}})
	if g := m.match(both); g != groups[0] {
		t.Fatalf("ambiguous match = %s, want first group", g.path)
	}
	m.claim(groups[0])
	if g := m.match(both); g != groups[1] {
		t.Fatalf("after claiming the first group, match = %v, want second", g)
	}
	m.claim(groups[1])
	if g := m.match(both); g != nil {
		t.Fatalf("everything claimed but matched %s", g.path)
	}
	// Empty inodes never match.
	if g := m.match(existingItem{path: "/gone", files: []byte(`[{"ino":""}]`)}); g != nil {
		t.Fatalf("empty inode matched %s", g.path)
	}
}

func items1Ino(t *testing.T, ex existingItem) string {
	t.Helper()
	return parseStoredLibraryFiles(ex.files)[0].Ino
}

// Matching N moved items scales linearly in N, not as groups x items: ten
// times the items may take far less than the hundredfold a quadratic matcher
// needs. The bound (40x for 10x) leaves room for timer noise, and the best of
// several runs is compared.
func TestGroupMatcherScalesLinearly(t *testing.T) {
	if testing.Short() {
		t.Skip("timing comparison")
	}
	elapsed := func(n int) time.Duration {
		groups, items := fakeMovedLibrary(n)
		best := time.Duration(1<<63 - 1)
		for run := 0; run < 5; run++ {
			start := time.Now()
			m := newGroupMatcher(groups)
			for i := len(items) - 1; i >= 0; i-- { // worst order for a scan from the front
				g := m.match(items[i])
				if g != groups[i] {
					t.Fatalf("item %d matched %v", i, g)
				}
				m.claim(g)
			}
			if d := time.Since(start); d < best {
				best = d
			}
		}
		return best
	}
	small, large := elapsed(2000), elapsed(20000)
	if large > 40*small {
		t.Fatalf("20000 moved items took %v, 2000 took %v: more than 40x for 10x the work", large, small)
	}
}

// fakeMovedLibrary builds n groups of two files each and n stored items whose
// paths no longer exist, so every match goes through the inode lookup.
func fakeMovedLibrary(n int) ([]*group, []existingItem) {
	groups, items := make([]*group, 0, n), make([]existingItem, 0, n)
	for i := 0; i < n; i++ {
		inos := []uint64{uint64(i)*2 + 1, uint64(i)*2 + 2}
		g := &group{path: fmt.Sprintf("/lib/g%06d", i)}
		var stored []libraryFile
		for _, ino := range inos {
			g.files = append(g.files, file{path: g.path + "/f", info: newFakeInfo(ino)})
			stored = append(stored, libraryFile{Ino: strconv.FormatUint(ino, 10)})
		}
		groups = append(groups, g)
		items = append(items, existingItem{id: fmt.Sprint(i), path: fmt.Sprintf("/moved/%d", i), files: marshalStored(stored)})
	}
	return groups, items
}
