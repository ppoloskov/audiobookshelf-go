package providers

import "strings"

// SetEndpoint points one upstream at a fixture server. Production never
// overrides an upstream URL, so the hook lives in tests only.
func (h *Handler) SetEndpoint(name, baseURL string) {
	h.endpointMu.Lock()
	h.endpoints[name] = strings.TrimRight(baseURL, "/")
	h.endpointMu.Unlock()
}
