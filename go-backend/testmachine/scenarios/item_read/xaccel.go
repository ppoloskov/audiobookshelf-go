package item_read

import (
	"net/http"
	"net/url"
	"testing"

	"audiobookshelf-compat/testmachine/scenarios/harness"
)

// xAccelScenario starts a server with USE_X_ACCEL=/protected and checks that
// the route hands the file to the reverse proxy: 204, no body, and an
// X-Accel-Redirect header holding the prefix and the encoded file path.
func xAccelScenario(requirement, invariant, assertion, claim string, sources []string, itemKey, fileName string, route func(item string, file fileInfo) string) harness.Scenario {
	return harness.Scenario{
		Requirement: requirement, Invariant: invariant, Case: "edge", State: "stateless", Assertion: assertion,
		Sources: append([]string{"server/Server.js:86", "server/utils/fileUtils.js:442-448"}, sources...), Claim: claim,
		Run: func(t *testing.T, _ *harness.Env) {
			e := harness.Start(t, harness.Options{Env: map[string]string{"USE_X_ACCEL": "/protected"}})
			id := e.IDs[itemKey]
			file := fileNamed(t, e, id, fileName)
			resp := e.Get(t, e.Token(t, "listener"), route(id, file))
			wantStatus(t, "X-Accel route", resp, http.StatusNoContent)
			want := (&url.URL{Path: "/protected" + file.Path}).EscapedPath()
			if got := resp.Header.Get("X-Accel-Redirect"); got != want {
				t.Fatalf("X-Accel-Redirect %q, want %q", got, want)
			}
			if len(resp.Body) != 0 {
				t.Fatalf("X-Accel response has a body of %d bytes", len(resp.Body))
			}
		},
	}
}

// xAccelScenarios covers the reverse-proxy hand-off of the three file routes.
func xAccelScenarios(*setup) []harness.Scenario {
	return []harness.Scenario{
		xAccelScenario("route-r066", "INV-R066-SAMPLED", "file-x-accel-redirect",
			"With USE_X_ACCEL set, the stream route answers 204 with an X-Accel-Redirect of the prefix and the encoded file path instead of the bytes.",
			[]string{getFileSource}, "saga1", "01 - Part 1.mp3", fileRoute),
		xAccelScenario("route-r068", "INV-R068-HEADERS-001", "file-download-x-accel-redirect",
			"With USE_X_ACCEL set, the file download answers 204 with the X-Accel-Redirect header.",
			[]string{downloadFileSource}, "saga1", "01 - Part 1.mp3", downloadRoute),
		xAccelScenario("route-r069", "INV-ROUTE-R069-CONTRACT", "ebook-x-accel-redirect",
			"With USE_X_ACCEL set, the ebook route answers 204 with the X-Accel-Redirect header.",
			[]string{ebookSource}, "ebook", "Ebook Only.epub", func(item string, _ fileInfo) string { return "/api/items/" + item + "/ebook" }),
	}
}
