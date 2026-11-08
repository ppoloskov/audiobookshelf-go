package textsort

import (
	"encoding/json"
	"strings"
	"unicode/utf8"
)

// DefaultPrefixes are the sorting prefixes of a fresh server.
var DefaultPrefixes = []string{"the", "a"}

// ParsePrefixes reads sortingPrefixes from the server-settings JSON; non-string
// entries are skipped and the default is ["the","a"] when the key is absent,
// null or not an array.
func ParsePrefixes(settings []byte) []string {
	var parsed struct {
		Prefixes []any `json:"sortingPrefixes"`
	}
	if json.Unmarshal(settings, &parsed) != nil || parsed.Prefixes == nil {
		return append([]string(nil), DefaultPrefixes...)
	}
	out := make([]string, 0, len(parsed.Prefixes))
	for _, v := range parsed.Prefixes {
		if s, ok := v.(string); ok {
			out = append(out, s)
		}
	}
	return out
}

// titleParts is upstream getTitleParts (server/utils/index.js:180-190): the
// title without its leading sorting prefix and that prefix capitalised. The
// prefix match is case-insensitive and needs a following space.
func titleParts(title string, prefixes []string) (rest, prefix string) {
	lower := strings.ToLower(title)
	for _, p := range prefixes {
		if strings.HasPrefix(lower, p+" ") && len(title) > len(p) {
			r, n := utf8.DecodeRuneInString(p)
			return title[len(p)+1:], strings.ToUpper(string(r)) + p[n:]
		}
	}
	return title, ""
}

// TitleIgnorePrefix is the stored titleIgnorePrefix column: the title without
// a leading sorting prefix ("The Hobbit" becomes "Hobbit"), getTitleIgnorePrefix.
func TitleIgnorePrefix(title string, prefixes []string) string {
	rest, _ := titleParts(title, prefixes)
	return rest
}

// PrefixAtEnd is the display form: the leading sorting prefix moved to the end
// ("The Hobbit" becomes "Hobbit, The"), getTitlePrefixAtEnd.
func PrefixAtEnd(title string, prefixes []string) string {
	rest, prefix := titleParts(title, prefixes)
	if prefix == "" {
		return title
	}
	return rest + ", " + prefix
}
