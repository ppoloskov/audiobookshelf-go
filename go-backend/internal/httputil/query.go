package httputil

import (
	"net/http"
	"slices"
	"strings"
)

// NormalizedInclude is the response echo of the include parameter after
// splitting, trimming and lowercasing every value.
func NormalizedInclude(r *http.Request) string {
	values := strings.Split(r.URL.Query().Get("include"), ",")
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.ToLower(strings.TrimSpace(value))
		if value != "" {
			out = append(out, value)
		}
	}
	return strings.Join(out, ",")
}

// IncludeHas reports whether the normalized include parameter contains value.
func IncludeHas(r *http.Request, value string) bool {
	return slices.Contains(strings.Split(NormalizedInclude(r), ","), value)
}
