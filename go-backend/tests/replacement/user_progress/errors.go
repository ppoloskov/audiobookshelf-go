package user_progress

import (
	"net/http"
	"strings"
	"testing"

	"audiobookshelf-compat/tests/replacement/harness"
)

func errorScenarios() []harness.Scenario {
	return []harness.Scenario{
		{
			Requirement: "route-r111", Invariant: "INV-R111-ERROR-001", Case: "error", State: "failure", Assertion: "patch-requires-authentication",
			Sources: []string{"server/Server.js:354", "server/Server.js:160", "server/Server.js:352", patchSource},
			Claim:   "PATCH /api/me/progress/:libraryItemId answers 401 without a token and with an unknown bearer and stores nothing.",
			Run: func(t *testing.T, e *harness.Env) {
				requireUnauthenticated(t, e, http.MethodPatch, progressPath(e.IDs["saga1"], ""), map[string]any{"currentTime": 1})
				requireUnauthenticated(t, e, http.MethodPatch, progressPath(e.IDs["podcast"], e.IDs["ep1"]), map[string]any{"currentTime": 1})
			},
		},
		{
			Requirement: "route-r111", Invariant: "INV-R111-ERROR-001", Case: "error", State: "failure", Assertion: "patch-rejections-change-nothing",
			Sources: []string{"server/models/User.js:735-790", patchSource},
			Claim:   "PATCH answers 404 for an unknown library item or episode, 400 for a podcast item without an episode and 404 for a book item id sent as the episode; none of them stores a row or emits user_updated, and the episode id is checked first.",
			Run: func(t *testing.T, e *harness.Env) {
				who := newActor(t, e)
				sockets := connectAudiences(t, e, who)
				mark := sockets.mark()
				body := map[string]any{"currentTime": 3, "duration": 60, "progress": 0.05}
				for _, c := range []struct {
					what, item, episode string
					status              int
				}{
					{"unknown item", unknownID, "", http.StatusNotFound},
					{"unknown episode", e.IDs["podcast"], unknownID, http.StatusNotFound},
					{"unknown item and unknown episode", unknownID, unknownID, http.StatusNotFound},
					{"podcast item without episode", e.IDs["podcast"], "", http.StatusBadRequest},
					{"book id as episode", e.IDs["podcast"], e.IDs["saga1"], http.StatusNotFound},
				} {
					wantStatus(t, e.Patch(t, who.token, progressPath(c.item, c.episode), body), c.status, c.what)
				}
				requireNoRows(t, e, who.id)
				sockets.nobody(t, mark, "user_updated")
			},
		},
		{
			Requirement: "route-r111", Invariant: "INV-R111-ERROR-001", Case: "error", State: "failure", Assertion: "patch-malformed-json",
			Sources: []string{"server/Server.js:354", "server/Server.js:160", "server/Server.js:352", patchSource},
			Claim:   "A PATCH whose body is not valid JSON answers 400 and stores nothing.",
			Run: func(t *testing.T, e *harness.Env) {
				who := newActor(t, e)
				resp := e.Do(t, harness.Request{Method: http.MethodPatch, Path: progressPath(e.IDs["saga1"], ""), Token: who.token, Header: map[string]string{"Content-Type": "application/json"}, Body: strings.NewReader("{\"currentTime\": ")})
				wantStatus(t, resp, http.StatusBadRequest, "malformed body")
				requireNoRows(t, e, who.id)
			},
		},
	}
}
