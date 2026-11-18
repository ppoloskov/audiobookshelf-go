package item_read

import (
	"net/http"
	"testing"

	"audiobookshelf-compat/tests/replacement/harness"
)

// quietWatcher turns the folder watcher off for both libraries, so the
// outcome of scenarios that add or delete library files depends only on the
// requests they make and never on a delayed rescan.
func quietWatcher(t *testing.T, e *harness.Env) {
	t.Helper()
	for _, key := range []string{"library", "podcastLibrary"} {
		resp := e.Patch(t, e.Token(t, "root"), "/api/libraries/"+e.IDs[key], map[string]any{"settings": map[string]any{"disableWatcher": true}})
		wantStatus(t, "disable watcher", resp, http.StatusOK)
	}
}

// startQuiet starts a private server over a fresh fixture copy with the watcher off.
func startQuiet(t *testing.T) *harness.Env {
	t.Helper()
	e := harness.Start(t, harness.Options{})
	quietWatcher(t, e)
	return e
}
