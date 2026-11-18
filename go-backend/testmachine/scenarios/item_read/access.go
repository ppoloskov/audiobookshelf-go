package item_read

import (
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	"audiobookshelf-compat/testmachine/scenarios/harness"
)

const missingID = "00000000-0000-4000-8000-000000000000"

// routeSpec describes how a scenario builds the path of one item route.
type routeSpec struct {
	requirement string // capability id
	invariant   string // invariant holding the access obligations
	label       string // assertion prefix
	sources     []string
	path        func(t *testing.T, e *harness.Env, itemID string) string
	// allowedAdmin is true for admin-only routes: the denial matrix then
	// uses the library-limited admin instead of the restricted users.
	allowedAdmin bool
	// adminOKStatus is the status the limited admin gets on the accessible
	// podcast (200 unless the route rejects podcasts after the access check).
	adminOKStatus int
}

var audioExts = map[string]bool{".mp3": true, ".m4b": true, ".flac": true}

// anyFile returns the first stored file of the item (an audio file when
// audioOnly is set).
func anyFile(t *testing.T, e *harness.Env, itemID string, audioOnly bool) fileInfo {
	t.Helper()
	for _, file := range filesOfItem(t, e, itemID) {
		if !audioOnly || audioExts[strings.ToLower(filepath.Ext(file.Name))] {
			return file
		}
	}
	t.Fatalf("item %s has no suitable file", itemID)
	return fileInfo{}
}

// denial is one user that must be refused one item.
type denial struct{ user, item, why string }

func denials(admin bool) []denial {
	if admin {
		return []denial{{"limitedadmin", "saga1", "library not accessible"}}
	}
	return []denial{
		{"podcastonly", "saga1", "library not accessible"},
		{"tagallow", "saga1", "item without tags under a tag allow list"},
		{"tagdeny", "ebook", "item with a denied tag"},
		{"plain", "disposable", "explicit item for a user without explicit access"},
	}
}

// accessScenarios returns the unauthenticated, unknown-item and access-denied scenarios of a route.
func accessScenarios(s *setup, spec routeSpec) []harness.Scenario {
	sources := append([]string{middlewareSource, "server/models/User.js:646-683"}, spec.sources...)
	build := func(suffix, caseName, claim string, run func(t *testing.T, e *harness.Env)) harness.Scenario {
		return harness.Scenario{
			Requirement: spec.requirement, Invariant: spec.invariant, Case: caseName, State: "stateless",
			Assertion: spec.label + "-" + suffix, Sources: sources, Claim: claim, Run: run,
		}
	}
	return []harness.Scenario{
		build("unauthenticated", "error", "Without a valid token the route answers 401.", func(t *testing.T, e *harness.Env) {
			path := spec.path(t, e, e.IDs["saga1"])
			for _, token := range []string{"", "not-a-token"} {
				wantStatus(t, "token "+token, e.Get(t, token, path), http.StatusUnauthorized)
			}
		}),
		build("unknown-item", "error", "An item id that does not exist, well formed or not, answers 404.", func(t *testing.T, e *harness.Env) {
			for _, id := range []string{missingID, "not-a-uuid"} {
				path := spec.path(t, e, e.IDs["saga1"])
				path = strings.Replace(path, e.IDs["saga1"], id, 1)
				wantStatus(t, "item "+id, e.Get(t, e.Token(t, "root"), path), http.StatusNotFound)
			}
		}),
		build("item-access-denied", "error", "A user refused by library access, item tags or the explicit-content restriction gets 403, while the same user can open an item outside the restriction.", func(t *testing.T, e *harness.Env) {
			for _, d := range denials(spec.allowedAdmin) {
				token := s.token(t, e, d.user)
				wantStatus(t, d.user+": "+d.why, e.Get(t, token, spec.path(t, e, e.IDs[d.item])), http.StatusForbidden)
			}
			if spec.allowedAdmin {
				want := http.StatusOK
				if spec.adminOKStatus != 0 {
					want = spec.adminOKStatus
				}
				wantStatus(t, "limited admin on an accessible podcast", e.Get(t, s.token(t, e, "limitedadmin"), spec.path(t, e, e.IDs["podcast"])), want)
			}
		}),
	}
}

// itemPathFor returns the path builder of GET /api/items/:id.
func itemRoute(t *testing.T, e *harness.Env, itemID string) string { return "/api/items/" + itemID }

// itemAccessScenarios covers the middleware of GET /api/items/:id.
func itemAccessScenarios(s *setup) []harness.Scenario {
	scenarios := accessScenarios(s, routeSpec{
		requirement: "route-r050", invariant: "INV-R050-ACCESS-001", label: "item", sources: []string{findOneSource},
		path: itemRoute,
	})
	scenarios = append(scenarios,
		harness.Scenario{
			Requirement: "route-r050", Invariant: "INV-R050-ACCESS-001", Case: "edge", State: "stateless", Assertion: "item-allowed-sides-of-restrictions",
			Sources: []string{middlewareSource, "server/models/User.js:646-683"},
			Claim:   "The restricted users still read what their restriction allows (podcast library, tagged item, untagged item under a deny list, non-explicit item) and the document is the same as the root user's.",
			Run: func(t *testing.T, e *harness.Env) {
				for _, c := range []struct{ user, item string }{
					{"podcastonly", "podcast"}, {"tagallow", "ebook"}, {"tagdeny", "saga1"}, {"plain", "saga1"}, {"nodownload", "saga1"},
				} {
					resp := e.Get(t, s.token(t, e, c.user), "/api/items/"+e.IDs[c.item])
					wantStatus(t, c.user+" reads "+c.item, resp, http.StatusOK)
					requireItem(t, c.user+" "+c.item, plainItem(t, e, e.IDs[c.item]), resp.Object(t))
				}
			},
		},
		harness.Scenario{
			Requirement: "route-r050", Invariant: "INV-R050-ACCESS-001", Case: "edge", State: "stateless", Assertion: "item-explicit-user-and-token-query",
			Sources: []string{middlewareSource, "server/Auth.js:126"},
			Claim:   "A user with explicit access reads the explicit item, and the access token is also accepted as the token query parameter.",
			Run: func(t *testing.T, e *harness.Env) {
				id := e.IDs["disposable"]
				wantStatus(t, "listener", e.Get(t, e.Token(t, "listener"), "/api/items/"+id), http.StatusOK)
				resp := e.Do(t, harness.Request{Path: "/api/items/" + id, Query: map[string][]string{"token": {e.Token(t, "listener")}}})
				wantStatus(t, "token query parameter", resp, http.StatusOK)
				requireItem(t, "item", plainItem(t, e, id), resp.Object(t))
				if got := resp.Object(t)["id"]; got != id {
					t.Fatalf("item id %v, want %s", got, id)
				}
			},
		},
	)
	return scenarios
}
