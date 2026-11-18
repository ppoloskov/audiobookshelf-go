package item_cover

import (
	"net/http"
	"testing"

	"audiobookshelf-compat/testmachine/scenarios/harness"
)

// socketQuickMatchScenarios covers the batch_quickmatch_complete event.
func socketQuickMatchScenarios(s *setup) []harness.Scenario {
	const req, inv = "socket-outgoing-batch-quickmatch-complete", "INV-SOCKET-OUTGOING-BATCH-QUICKMATCH-COMPLETE"
	return []harness.Scenario{
		scenario(req, inv, "normal", "initial", "batch-quickmatch-complete-payload-and-audience",
			"After a successful quickmatch the requesting admin's sockets receive batch_quickmatch_complete {success:true,updates:1,unmatched:0} after the item_updated event, and a listener's socket receives neither.",
			srcs(controllerSrc+":807-812", "server/SocketAuthority.js:68-78", "client/layouts/default.vue:352-363"),
			func(t *testing.T, e *harness.Env) {
				id := e.IDs["ebook"]
				stub := newMatchProvider(t, map[string]any{"description": "Socket description"})
				slug := "custom-" + createProvider(t, e, "Socket Provider", stub.server.URL)
				o := observe(t, e, map[string]string{"root": e.Token(t, "root"), "root2": e.Token(t, "root"), "listener": e.Token(t, "listener")})
				wantStatus(t, "quickmatch", quickMatch(t, e, e.Token(t, "root"), []string{id}, map[string]any{"provider": slug}), http.StatusOK)
				requireEqual(t, "result", waitQuickMatch(t, o, "root"), quickMatchResult{Success: true, Updates: 1})
				requireEqual(t, "description", mediaRow(t, e, id)["description"], "Socket description")
				o.itemUpdated(t, e, id, []string{"root"}, nil)
				requireEqual(t, "second socket result", waitQuickMatch(t, o, "root2"), quickMatchResult{Success: true, Updates: 1})
				o.sockets["listener"].None(t, o.marks["listener"], "batch_quickmatch_complete", absentWindow, anyMatch)
				// Ordering on one socket: item_updated precedes batch_quickmatch_complete.
				items, batch := -1, -1
				for i, ev := range o.sockets["root"].Events()[o.marks["root"]:] {
					switch ev.Name {
					case "item_updated":
						if items < 0 {
							items = i
						}
					case "batch_quickmatch_complete":
						if batch < 0 {
							batch = i
						}
					}
				}
				if items < 0 || batch < 0 || items > batch {
					t.Fatalf("event order item_updated=%d batch_quickmatch_complete=%d", items, batch)
				}
			}),
		scenario(req, inv, "normal", "force", "batch-quickmatch-complete-override",
			"An override quickmatch reports its update count in batch_quickmatch_complete for the requesting admin and not for another user.",
			srcs(controllerSrc+":768-812"),
			func(t *testing.T, e *harness.Env) {
				id := e.IDs["ebook"]
				stub := newMatchProvider(t, map[string]any{"title": "Override Socket", "author": "Ada Example"})
				slug := "custom-" + createProvider(t, e, "Socket Override Provider", stub.server.URL)
				o := observe(t, e, map[string]string{"root": e.Token(t, "root"), "listener": e.Token(t, "listener")})
				options := map[string]any{"provider": slug, "overrideCover": true, "overrideDetails": true}
				wantStatus(t, "override", quickMatch(t, e, e.Token(t, "root"), []string{id}, options), http.StatusOK)
				requireEqual(t, "result", waitQuickMatch(t, o, "root"), quickMatchResult{Success: true, Updates: 1})
				o.sockets["listener"].None(t, o.marks["listener"], "batch_quickmatch_complete", absentWindow, anyMatch)
			}),
		scenario(req, inv, "edge", "repeat", "batch-quickmatch-complete-no-updates",
			"A repeated quickmatch that changes nothing still emits batch_quickmatch_complete {success:false,updates:0,unmatched:0}.",
			srcs(controllerSrc+":807-812"),
			func(t *testing.T, e *harness.Env) {
				id := e.IDs["ebook"]
				title, _ := mediaRow(t, e, id)["title"].(string)
				stub := newMatchProvider(t, map[string]any{"title": title, "author": "Ada Example"})
				slug := "custom-" + createProvider(t, e, "Socket Repeat Provider", stub.server.URL)
				o := observe(t, e, map[string]string{"root": e.Token(t, "root")})
				// The item was overridden to this title by the previous scenario; the
				// provider still offers the stub cover, which is already stored.
				wantStatus(t, "repeat", quickMatch(t, e, e.Token(t, "root"), []string{id}, map[string]any{"provider": slug}), http.StatusOK)
				requireEqual(t, "result", waitQuickMatch(t, o, "root"), quickMatchResult{})
			}),
		scenario(req, inv, "normal", "changed", "batch-quickmatch-complete-follows-the-changed-item",
			"After another user's change to the item, the next quickmatch still reports its own update count to the requesting admin only.",
			srcs(controllerSrc+":786-812", emitterSrc),
			func(t *testing.T, e *harness.Env) {
				id := e.IDs["ebook"]
				stub := newMatchProvider(t, map[string]any{"title": "Changed Socket", "author": "Ada Example"})
				slug := "custom-" + createProvider(t, e, "Socket Changed Provider", stub.server.URL)
				o := observe(t, e, map[string]string{"root": e.Token(t, "root"), "listener": e.Token(t, "listener")})
				wantStatus(t, "change item", e.Patch(t, e.Token(t, "root"), "/api/items/"+id+"/media", map[string]any{"metadata": map[string]any{"subtitle": "Changed by another user"}}), http.StatusOK)
				wantStatus(t, "quickmatch", quickMatch(t, e, e.Token(t, "root"), []string{id}, map[string]any{"provider": slug, "overrideDetails": true}), http.StatusOK)
				requireEqual(t, "result", waitQuickMatch(t, o, "root"), quickMatchResult{Success: true, Updates: 1})
				o.sockets["listener"].None(t, o.marks["listener"], "batch_quickmatch_complete", absentWindow, anyMatch)
			}),
		scenario(req, inv, "error", "failure", "batch-quickmatch-complete-not-emitted-on-rejection",
			"A rejected batch quickmatch (non-admin, empty ids, unknown ids) emits no batch_quickmatch_complete to any socket.",
			srcs(controllerSrc+":763-785"),
			func(t *testing.T, e *harness.Env) {
				o := observe(t, e, map[string]string{"root": e.Token(t, "root"), "listener": e.Token(t, "listener")})
				wantStatus(t, "non-admin", quickMatch(t, e, e.Token(t, "listener"), []string{e.IDs["ebook"]}, nil), http.StatusForbidden)
				wantStatus(t, "empty", quickMatch(t, e, e.Token(t, "root"), nil, nil), http.StatusBadRequest)
				wantStatus(t, "unknown", quickMatch(t, e, e.Token(t, "root"), []string{"00000000-0000-4000-8000-000000000000"}, nil), http.StatusBadRequest)
				o.none(t, "batch_quickmatch_complete", anyMatch)
			}),
	}
}
