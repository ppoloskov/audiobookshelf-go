package scan

import (
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

var (
	publishedYearPrefix = regexp.MustCompile(`^ *\(?([0-9]{4})\)? * - *(.+)`)
	asinInName          = regexp.MustCompile(`(?: |^)\[([A-Z0-9]{10})\](?: |$)`)
	sequenceInName      = regexp.MustCompile(`(?i)^(?:(?:vol\.? |volume |book ))?(\d{0,3}(?:\.\d{1,2})?)(\.?)(?: (.*))?$`)
)

// folderMeta reads the title, author, series and sequence from the item's
// place under the library folder: Author/Series/Title (getBookDataFromDir,
// scandir.js:153). A single-file item is titled by its file name.
func folderMeta(itemPath, root string, isFile, parseSubtitle bool) (title, author, series, sequence string) {
	rel, _ := filepath.Rel(root, itemPath)
	parts := strings.Split(filepath.ToSlash(rel), "/")
	// A series folder only exists with more than one directory above the title.
	if len(parts) > 2 {
		series, author = parts[len(parts)-2], parts[len(parts)-3]
	} else if len(parts) > 1 {
		author = parts[len(parts)-2]
	}
	title = removeASIN(filepath.Base(itemPath))
	if i := strings.LastIndex(title, " {"); i >= 0 && strings.HasSuffix(title, "}") {
		title = title[:i]
	}
	if series != "" {
		title, sequence = extractSequence(title)
	}
	if m := publishedYearPrefix.FindStringSubmatch(title); m != nil {
		title = m[2]
	}
	if parseSubtitle {
		title, _, _ = strings.Cut(title, " - ")
	}
	if isFile {
		title = strings.TrimSuffix(filepath.Base(itemPath), filepath.Ext(itemPath))
	}
	return title, author, series, sequence
}

// removeASIN drops a " [B000000000]" tag from a folder name.
func removeASIN(name string) string {
	m := asinInName.FindStringSubmatchIndex(name)
	if m == nil {
		return name
	}
	return strings.TrimSpace(name[:m[0]] + name[m[1]:])
}

// extractSequence splits "Vol 1 - Title" style names into title and sequence.
func extractSequence(name string) (title, sequence string) {
	parts := strings.Split(name, " - ")
	for i, part := range parts {
		m := sequenceInName.FindStringSubmatch(part)
		if m == nil {
			continue
		}
		lower := strings.ToLower(part)
		labeled := strings.HasPrefix(lower, "vol") || strings.HasPrefix(lower, "book ")
		if m[3] != "" && !labeled && m[2] == "" {
			continue
		}
		sequence = m[1]
		if f, err := strconv.ParseFloat(sequence, 64); err == nil {
			sequence = strconv.FormatFloat(f, 'f', -1, 64)
		}
		if m[3] != "" {
			parts[i] = m[3]
		} else {
			parts = append(parts[:i], parts[i+1:]...)
		}
		return strings.Join(parts, " - "), sequence
	}
	return name, ""
}
