package logging

import (
	"fmt"
	"net/http"
	"runtime/debug"
)

// RequestError records the cause behind a 5xx response at ERROR, with the
// route as context: `[HTTP] GET /api/items/abc: database read failed: <err>`.
// Upstream logs the failure at the throw site (`Logger.error('[Controller]
// message', error)`) and then sends the status; handlers call this just
// before they write the unchanged response. Only the path is logged, never
// the query string, because it can carry an API token. The reported source is
// the calling handler's file:line.
func RequestError(r *http.Request, what string, err error) {
	method, path := "?", "?"
	if r != nil {
		method = r.Method
		if r.URL != nil {
			path = r.URL.Path
		}
	}
	if err != nil {
		defaultLogger.write(LevelError, "", fmt.Sprintf("[HTTP] %s %s: %s: %v", method, path, what, err), 2)
		return
	}
	defaultLogger.write(LevelError, "", fmt.Sprintf("[HTTP] %s %s: %s", method, path, what), 2)
}

// RecoverRequest logs a handler panic (the value from recover()) and its
// stack at ERROR. The caller answers the request.
func RecoverRequest(r *http.Request, recovered any) {
	method, path := "?", "?"
	if r != nil {
		method = r.Method
		if r.URL != nil {
			path = r.URL.Path
		}
	}
	defaultLogger.write(LevelError, "", fmt.Sprintf("[HTTP] %s %s: panic: %v\n%s", method, path, recovered, debug.Stack()), 2)
}
