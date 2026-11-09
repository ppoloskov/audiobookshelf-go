package scanner

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"sort"
	"strconv"
	"syscall"
	"testing"
	"time"
)

// fakeInfo is an os.FileInfo whose inode comes from a synthetic Stat_t, so
// benchmarks match on inodes without touching the disk.
type fakeInfo struct{ st syscall.Stat_t }

func newFakeInfo(ino uint64) os.FileInfo {
	fi := &fakeInfo{}
	fi.st.Ino = ino
	return fi
}
func (f *fakeInfo) Name() string       { return "f" }
func (f *fakeInfo) Size() int64        { return 1 }
func (f *fakeInfo) Mode() fs.FileMode  { return 0o644 }
func (f *fakeInfo) ModTime() time.Time { return time.Time{} }
func (f *fakeInfo) IsDir() bool        { return false }
func (f *fakeInfo) Sys() any           { return &f.st }

func newMatchAll(groups []*group, items []existingItem) (matched int) {
	m := newGroupMatcher(groups)
	for _, ex := range items {
		if g := m.match(ex); g != nil {
			m.claim(g)
			matched++
		}
	}
	_ = m.unclaimed()
	return matched
}

// benchLibrary builds n stored items and the groups a scan finds: ~5% of the
// items moved (new path, same inodes, so they need the inode fallback), ~1%
// deleted (no group) and the rest unchanged. Group paths do not exist on
// disk, so os.Stat fails identically for the old and new code; inodes come
// from the files.
func benchLibrary(n int) ([]*group, []existingItem) {
	var groups []*group
	items := make([]existingItem, 0, n)
	for i := 0; i < n; i++ {
		path := fmt.Sprintf("/bench/lib/item%06d", i)
		inos := []uint64{uint64(i)*3 + 1, uint64(i)*3 + 2, uint64(i)*3 + 3}
		var stored []libraryFile
		for _, ino := range inos {
			stored = append(stored, libraryFile{Ino: strconv.FormatUint(ino, 10)})
		}
		raw := marshalStored(stored)
		items = append(items, existingItem{id: "id" + strconv.Itoa(i), path: path, ino: "d" + strconv.Itoa(i), files: raw})
		if i%100 == 7 { // ~1% deleted
			continue
		}
		gp := path
		if i%20 == 3 { // ~5% moved
			gp = fmt.Sprintf("/bench/lib/moved%06d", i)
		}
		g := &group{path: gp}
		for _, ino := range inos {
			g.files = append(g.files, file{path: gp + "/" + strconv.FormatUint(ino, 10), info: newFakeInfo(ino)})
		}
		groups = append(groups, g)
	}
	// Scan order is path order.
	sortGroups(groups)
	return groups, items
}

func BenchmarkMatch(b *testing.B) {
	for _, n := range []int{1000, 10000, 50000} {
		groups, items := benchLibrary(n)
		want := n - (n+92)/100 // hand count: every item but the deleted i%100==7
		b.Run(fmt.Sprintf("new/n=%d", n), func(b *testing.B) {
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				if got := newMatchAll(groups, items); got != want {
					b.Fatalf("matched %d, want %d", got, want)
				}
			}
		})
	}
}

// Every stored item that has a group is paired with the group that holds its
// files, whether it stayed (same path) or moved (new path, same inodes); the
// deleted ones (i%100==7) match nothing. Expected values come from how
// benchLibrary constructs the library, not from the matcher.
func TestBenchLibraryPairsEachItemWithItsOwnGroup(t *testing.T) {
	const n = 2000
	groups, items := benchLibrary(n)
	m := newGroupMatcher(groups)
	matched := 0
	for i, ex := range items {
		got := m.match(ex)
		if i%100 == 7 {
			if got != nil {
				t.Fatalf("deleted item %d matched %s", i, got.path)
			}
			continue
		}
		want := fmt.Sprintf("/bench/lib/item%06d", i)
		if i%20 == 3 {
			want = fmt.Sprintf("/bench/lib/moved%06d", i)
		}
		if got == nil || got.path != want {
			t.Fatalf("item %d matched %v, want %s", i, got, want)
		}
		m.claim(got)
		matched++
	}
	if left := len(m.unclaimed()); left != 0 || matched != n-n/100 {
		t.Fatalf("matched %d items, %d groups unclaimed; want %d and 0", matched, left, n-n/100)
	}
}

func marshalStored(files []libraryFile) []byte {
	raw, _ := json.Marshal(files)
	return raw
}

func sortGroups(groups []*group) {
	sort.Slice(groups, func(i, j int) bool { return groups[i].path < groups[j].path })
}
