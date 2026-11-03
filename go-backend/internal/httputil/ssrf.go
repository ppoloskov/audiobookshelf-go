package httputil

import (
	"os"
	"strings"
)

// SSRFFilterDisabled reports whether outbound requests to host skip the private
// address filter: the filter is off, or host is named in
// SSRF_REQUEST_FILTER_WHITELIST. TS applies the same rule to every axios request
// (server/Server.js, DisableSsrfRequestFilter), feeds included.
func SSRFFilterDisabled(host string) bool {
	if os.Getenv("DISABLE_SSRF_REQUEST_FILTER") == "1" || os.Getenv("EXP_PROXY_SUPPORT") == "1" {
		return true
	}
	for _, candidate := range strings.Split(os.Getenv("SSRF_REQUEST_FILTER_WHITELIST"), ",") {
		if strings.TrimSpace(candidate) == host && host != "" {
			return true
		}
	}
	return false
}
