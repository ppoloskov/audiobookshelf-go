package scanner

import "os"

// groupMatcher pairs stored items with the groups a scan found. It is built
// once per scan: lookups by path and by inode are map reads, so matching N
// stored items costs O(N + groups + files) rather than O(N x groups).
type groupMatcher struct {
	groups   []*group
	byPath   map[string]int
	pos      map[*group]int
	byIno    map[string][]int
	consumed []bool
	// visits counts list entries examined by inode lookups (for tests).
	visits int
}

func newGroupMatcher(groups []*group) *groupMatcher {
	m := &groupMatcher{
		groups:   groups,
		byPath:   make(map[string]int, len(groups)),
		pos:      make(map[*group]int, len(groups)),
		byIno:    map[string][]int{},
		consumed: make([]bool, len(groups)),
	}
	for i, g := range groups {
		m.byPath[g.path] = i
		m.pos[g] = i
		for _, ino := range groupInodes(g) {
			if ino == "" {
				continue
			}
			if list := m.byIno[ino]; len(list) == 0 || list[len(list)-1] != i {
				m.byIno[ino] = append(list, i)
			}
		}
	}
	return m
}

func groupInodes(g *group) []string {
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

// match returns the unclaimed group a stored item belongs to, or nil: the
// group at its path first, then the first group (in scan order) holding the
// item's inode or one of its stored files' inodes, which finds an item that
// moved or was renamed (LibraryScanner.js:171-178). It does not claim the group.
func (m *groupMatcher) match(ex existingItem) *group {
	if i, ok := m.byPath[ex.path]; ok && !m.consumed[i] {
		return m.groups[i]
	}
	best := -1
	consider := func(ino string) {
		if ino == "" {
			return
		}
		if i := m.firstFree(ino); i >= 0 && (best < 0 || i < best) {
			best = i
		}
	}
	consider(ex.ino)
	for _, lf := range parseStoredLibraryFiles(ex.files) {
		consider(lf.Ino)
	}
	if best < 0 {
		return nil
	}
	return m.groups[best]
}

// firstFree drops claimed groups from the front of an inode's list (a
// claimed group stays claimed, so each entry is dropped at most once).
func (m *groupMatcher) firstFree(ino string) int {
	list := m.byIno[ino]
	for len(list) > 0 && m.consumed[list[0]] {
		m.visits++
		list = list[1:]
	}
	m.byIno[ino] = list
	m.visits++
	if len(list) == 0 {
		return -1
	}
	return list[0]
}

func (m *groupMatcher) claim(g *group) { m.consumed[m.pos[g]] = true }

func (m *groupMatcher) unclaimed() []*group {
	var out []*group
	for i, g := range m.groups {
		if !m.consumed[i] {
			out = append(out, g)
		}
	}
	return out
}
