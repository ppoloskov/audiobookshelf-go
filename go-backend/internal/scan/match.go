package scan

import "audiobookshelf-compat/internal/db"

// keyed is something to be recognised again: a path and every inode it may be
// known by.
type keyed struct {
	path string
	inos []string
}

// pair is the one matching rule of the scanner (items to folders, library
// files to files, stored audio to files): each want takes the found entry with
// the same path, else the first free found entry (lowest index, so scan order)
// that shares an inode with it; an entry is used once and wants are served in
// order. The result holds the index of each want's entry, or -1.
func pair(want, found []keyed) []int {
	byPath := make(map[string]int, len(found))
	byIno := map[string][]int{}
	for i, f := range found {
		byPath[f.path] = i
		for _, ino := range f.inos {
			if list := byIno[ino]; ino != "" && (len(list) == 0 || list[len(list)-1] != i) {
				byIno[ino] = append(list, i)
			}
		}
	}
	used := make([]bool, len(found))
	out := make([]int, len(want))
	for w, k := range want {
		out[w] = -1
		if i, ok := byPath[k.path]; ok && !used[i] {
			out[w], used[i] = i, true
			continue
		}
		best := -1
		for _, ino := range k.inos {
			list := byIno[ino]
			for len(list) > 0 && used[list[0]] {
				list = list[1:]
			}
			byIno[ino] = list
			if len(list) > 0 && (best < 0 || list[0] < best) {
				best = list[0]
			}
		}
		if best >= 0 {
			out[w], used[best] = best, true
		}
	}
	return out
}

// itemKey is a stored item's path and its own inode and those of its files.
func itemKey(it db.LibraryItem) keyed {
	k := keyed{path: db.Val(it.Path), inos: []string{db.Val(it.Ino)}}
	for _, f := range it.LibraryFiles.V {
		k.inos = append(k.inos, f.Ino)
	}
	return k
}

// matchItems pairs stored items with the candidates found for them. It returns
// each item's candidate (nil: none) and the candidates no item claimed, in scan order.
func matchItems(items []db.LibraryItem, cands []*candidate) (matched []*candidate, unclaimed []*candidate) {
	want := make([]keyed, len(items))
	for i, it := range items {
		want[i] = itemKey(it)
	}
	found := make([]keyed, len(cands))
	for i, c := range cands {
		found[i] = c.key()
	}
	taken := make([]bool, len(cands))
	matched = make([]*candidate, len(items))
	for i, j := range pair(want, found) {
		if j >= 0 {
			matched[i], taken[j] = cands[j], true
		}
	}
	for j, c := range cands {
		if !taken[j] {
			unclaimed = append(unclaimed, c)
		}
	}
	return matched, unclaimed
}
