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

// oldContainsString, oldGroupInodes and oldMatchGroupByInode are the
// pre-S2 implementation (git HEAD go-backend/internal/scanner scanner.go and
// files.go), copied verbatim apart from the old* names.
func oldContainsString(values []string, value string) bool {
	for _, candidate := range values {
		if candidate == value {
			return true
		}
	}
	return false
}

func oldGroupInodes(g *group) []string {
	var inos []string
	if info, err := os.Stat(g.path); err == nil {
		inos = append(inos, inode(info))
	}
	for _, f := range g.files {
		inos = append(inos, inode(f.info))
	}
	for _, f := range g.companions {
		inos = append(inos, inode(f.info))
	}
	return inos
}

func oldMatchGroupByInode(ex existingItem, groups []*group, consumed map[*group]bool) *group {
	stored := parseStoredLibraryFiles(ex.files)
	for _, g := range groups {
		if consumed[g] {
			continue
		}
		inos := oldGroupInodes(g)
		if ex.ino != "" && oldContainsString(inos, ex.ino) {
			return g
		}
		for _, lf := range stored {
			if lf.Ino != "" && oldContainsString(inos, lf.Ino) {
				return g
			}
		}
	}
	return nil
}

// oldMatchAll is the pre-S2 per-scan matching loop (byPath, then inode scan).
func oldMatchAll(groups []*group, items []existingItem) (matched int) {
	byPath := make(map[string]*group, len(groups))
	for _, g := range groups {
		byPath[g.path] = g
	}
	consumed := map[*group]bool{}
	for _, ex := range items {
		g := byPath[ex.path]
		if g != nil && consumed[g] {
			g = nil
		}
		if g == nil {
			g = oldMatchGroupByInode(ex, groups, consumed)
		}
		if g != nil {
			consumed[g] = true
			matched++
		}
	}
	return matched
}

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
		want := newMatchAll(groups, items)
		b.Run(fmt.Sprintf("new/n=%d", n), func(b *testing.B) {
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				if got := newMatchAll(groups, items); got != want {
					b.Fatalf("matched %d, want %d", got, want)
				}
			}
		})
		if n > 10000 && os.Getenv("BENCH_OLD_LARGE") == "" {
			// The quadratic old algorithm needs minutes at 50k; opt in.
			continue
		}
		b.Run(fmt.Sprintf("old/n=%d", n), func(b *testing.B) {
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				if got := oldMatchAll(groups, items); got != want {
					b.Fatalf("matched %d, want %d", got, want)
				}
			}
		})
	}
}

// Old and new matching pair identical groups on the benchmark library.
func TestBenchLibraryOldAndNewAgree(t *testing.T) {
	groups, items := benchLibrary(2000)
	byPath := map[string]*group{}
	for _, g := range groups {
		byPath[g.path] = g
	}
	consumed := map[*group]bool{}
	m := newGroupMatcher(groups)
	for _, ex := range items {
		g := byPath[ex.path]
		if g != nil && consumed[g] {
			g = nil
		}
		if g == nil {
			g = oldMatchGroupByInode(ex, groups, consumed)
		}
		if g != nil {
			consumed[g] = true
		}
		if got := m.match(ex); got != g {
			t.Fatalf("item %s: new matched %v, old %v", ex.path, got, g)
		} else if got != nil {
			m.claim(got)
		}
	}
}

func marshalStored(files []libraryFile) []byte {
	raw, _ := json.Marshal(files)
	return raw
}

func sortGroups(groups []*group) {
	sort.Slice(groups, func(i, j int) bool { return groups[i].path < groups[j].path })
}
