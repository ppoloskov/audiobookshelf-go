package server

import (
	"net/http"
	"testing"

	"audiobookshelf-compat/testmachine/scenarios/harness"
)

func (s *suite) cacheScenarios() []harness.Scenario {
	return []harness.Scenario{
		{
			Requirement: "route-r016", Invariant: "INV-ROUTE-R016-CACHE", Case: "normal", State: "stateless", Assertion: "libraries-list-and-conditional",
			Sources: []string{"server/routers/ApiRouter.js:68", "server/managers/ApiCacheManager.js:79-115"},
			Claim:   "GET /api/libraries answers both fixture libraries with their media type, repeats the same body and ETag, answers 304 to a matching If-None-Match, and serves a random sort without the cache.",
			Run: func(t *testing.T, e *harness.Env) {
				first := e.Get(t, root(t, e), "/api/libraries")
				requireStatus(t, first, http.StatusOK, "GET /api/libraries")
				var body librariesResponse
				first.JSON(t, &body)
				if len(body.Libraries) != 2 {
					t.Fatalf("library count = %d, want the two fixture libraries", len(body.Libraries))
				}
				want := map[string]string{e.IDs["library"]: "Audiobooks", e.IDs["podcastLibrary"]: "Podcasts"}
				for _, item := range body.Libraries {
					name, ok := want[item.ID]
					if !ok || item.Name != name || item.MediaType == "" || len(item.Folders) != 1 || item.Folders[0].LibraryID != item.ID {
						t.Fatalf("library entry = %+v, want one of the fixture libraries with its folders", item)
					}
				}
				second := e.Get(t, root(t, e), "/api/libraries")
				requireStatus(t, second, http.StatusOK, "repeated GET /api/libraries")
				if string(second.Body) != string(first.Body) {
					t.Fatalf("repeated library list changed: %s vs %s", second.Body, first.Body)
				}
				etag := first.Header.Get("Etag")
				if etag == "" || second.Header.Get("Etag") != etag {
					t.Fatalf("library ETag = %q / %q, want a stable validator", etag, second.Header.Get("Etag"))
				}
				conditional := e.Do(t, harness.Request{Path: "/api/libraries", Token: root(t, e), Header: map[string]string{"If-None-Match": etag}})
				requireStatus(t, conditional, http.StatusNotModified, "conditional GET /api/libraries")
				if len(conditional.Body) != 0 {
					t.Fatalf("304 body = %q, want empty", conditional.Body)
				}
				random := e.Do(t, harness.Request{Path: "/api/libraries", Token: root(t, e), Query: map[string][]string{"sort": {"random"}}})
				requireStatus(t, random, http.StatusOK, "GET /api/libraries?sort=random")
				var randomBody librariesResponse
				random.JSON(t, &randomBody)
				if len(randomBody.Libraries) != 2 {
					t.Fatalf("random-sort library count = %d, want 2", len(randomBody.Libraries))
				}
			},
		},
		{
			Requirement: "route-r016", Invariant: "INV-ROUTE-R016-CACHE", Case: "error", State: "stateless", Assertion: "libraries-requires-authentication",
			Sources: []string{"server/Server.js:354", "server/routers/ApiRouter.js:68"},
			Claim:   "GET /api/libraries without a token or with an invalid token answers 401 and changes no library row.",
			Run: func(t *testing.T, e *harness.Env) {
				before := e.Count(t, "SELECT COUNT(*) FROM libraries")
				requireStatus(t, e.Get(t, "", "/api/libraries"), http.StatusUnauthorized, "unauthenticated GET /api/libraries")
				requireStatus(t, e.Do(t, harness.Request{Path: "/api/libraries", Token: "not-a-token"}), http.StatusUnauthorized, "GET /api/libraries with an invalid token")
				if after := e.Count(t, "SELECT COUNT(*) FROM libraries"); after != before {
					t.Fatalf("library count changed from %d to %d after rejected reads", before, after)
				}
			},
		},
		{
			Requirement: "route-r016", Invariant: "INV-ROUTE-R016-CACHE", Case: "edge", State: "stateless", Assertion: "libraries-user-isolation-and-write-freshness",
			Sources: []string{"server/routers/ApiRouter.js:68", "server/managers/ApiCacheManager.js:24-30", "server/controllers/LibraryController.js:74-90"},
			Claim:   "Library GETs stay user-scoped (a restricted user sees only its library) and a committed rename is visible to every caller on the next GET, so no stale cache entry is served.",
			Run: func(t *testing.T, e *harness.Env) {
				const name = "cache-restricted-user"
				create := e.Post(t, root(t, e), "/api/users", map[string]any{
					"username": name, "password": "cache-restricted-password", "isActive": true, "type": "user",
					"permissions": map[string]any{"accessAllLibraries": false, "librariesAccessible": []string{e.IDs["library"]}},
				})
				requireStatus(t, create, http.StatusOK, "create restricted user")
				var created struct {
					User struct {
						ID string `json:"id"`
					} `json:"user"`
				}
				create.JSON(t, &created)
				defer func() {
					requireStatus(t, e.Delete(t, root(t, e), "/api/users/"+created.User.ID), http.StatusOK, "delete restricted user")
				}()
				token, _ := e.Login(t, name, "cache-restricted-password")

				if got := len(getLibraries(t, e, root(t, e)).Libraries); got != 2 {
					t.Fatalf("root library count = %d, want 2", got)
				}
				restricted := getLibraries(t, e, token).Libraries
				if len(restricted) != 1 || restricted[0].ID != e.IDs["library"] || restricted[0].Name != "Audiobooks" {
					t.Fatalf("restricted library list = %+v, want only the Audiobooks library", restricted)
				}

				patchLibraryName(t, e, e.IDs["library"], "Cache Freshness")
				defer patchLibraryName(t, e, e.IDs["library"], "Audiobooks")
				if got := libraryName(t, e, root(t, e), e.IDs["library"]); got != "Cache Freshness" {
					t.Fatalf("library name after rename = %q, want Cache Freshness", got)
				}
				after := getLibraries(t, e, root(t, e)).Libraries
				if len(after) != 2 || nameOf(after, e.IDs["library"]) != "Cache Freshness" {
					t.Fatalf("root library list after rename = %+v, want the committed name", after)
				}
				restrictedAfter := getLibraries(t, e, token).Libraries
				if len(restrictedAfter) != 1 || restrictedAfter[0].Name != "Cache Freshness" {
					t.Fatalf("restricted library list after rename = %+v, want one renamed library", restrictedAfter)
				}
			},
		},
	}
}
